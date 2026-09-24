package discovery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/registration"
)

const vllmBody = "vllm:generation_tokens_total{model_name=\"model-v\",engine=\"0\"} 10\nvllm:prompt_tokens_by_source_total{model_name=\"model-v\",engine=\"0\",source=\"local_compute\"} 20\n"
const llamaBody = "llamacpp:tokens_predicted_total 7\nllamacpp:prompt_tokens_total 30\n"

func TestSwitchWithoutConfigurationChange(t *testing.T) {
	var phase atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if phase.Load() == 1 {
			http.Error(w, "offline", 503)
			return
		}
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"data":[{"id":"model-l"}]}`)
			return
		}
		if phase.Load() == 2 {
			fmt.Fprint(w, llamaBody)
		} else {
			fmt.Fprint(w, vllmBody)
		}
	}))
	defer s.Close()
	m := New(Options{Candidates: func(context.Context) ([]Target, error) { return []Target{{URL: s.URL}}, nil }, Timeout: time.Second, Nodes: 2})
	for _, tc := range []struct {
		phase         int32
		engine, model string
		count         float64
		up            bool
	}{
		{0, "vllm", "model-v", 10, true}, {1, "vllm", "model-v", 0, false},
		{2, "llamacpp", "model-l", 7, true}, {0, "vllm", "model-v", 10, true},
	} {
		phase.Store(tc.phase)
		obs := m.Observe(context.Background(), nil)
		if len(obs) != 1 {
			t.Fatalf("observations: %d", len(obs))
		}
		o := obs[0]
		if o.Registration.Engine != tc.engine || o.Registration.Model != tc.model || o.Registration.Nodes != 2 || (o.Err == nil) != tc.up {
			t.Fatalf("phase %d: %+v", tc.phase, o)
		}
		found := false
		for _, s := range o.Result.Samples {
			if s.Def.Name == metrics.Tokens.Name && s.Labels[0] == metrics.Decode {
				found = true
				if s.Value != tc.count {
					t.Fatalf("mixed counters: %v", s.Value)
				}
			}
		}
		if found != tc.up {
			t.Fatalf("phase %d: stale/missing tokens", tc.phase)
		}
	}
}

func TestDifferentPortsAndPinnedDeduplication(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, vllmBody) }))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.ReplaceAll(vllmBody, "model-v", "second"))
	}))
	defer b.Close()
	active := []Target{{URL: a.URL}}
	m := New(Options{Candidates: func(context.Context) ([]Target, error) { return active, nil }, Timeout: time.Second})
	if got := m.Observe(context.Background(), nil); len(got) != 1 || got[0].Registration.Model != "model-v" {
		t.Fatal(got)
	}
	active = []Target{{URL: b.URL}}
	got := m.Observe(context.Background(), nil)
	up := 0
	for _, o := range got {
		if o.Err == nil {
			up++
			if o.Registration.Model != "second" {
				t.Fatal("old measurements survived")
			}
		}
	}
	if up != 1 {
		t.Fatal(got)
	}
	active = []Target{{URL: a.URL}, {URL: a.URL}, {URL: b.URL}}
	got = m.Observe(context.Background(), []registration.Registration{{Endpoint: a.URL}})
	for _, o := range got {
		if o.Registration.Endpoint == a.URL {
			t.Fatal("pinned target duplicated")
		}
	}
}

func TestProbeBoundaries(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); fmt.Fprint(w, vllmBody) }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer s.Close()
	m := New(Options{Candidates: func(context.Context) ([]Target, error) { return []Target{{URL: s.URL}}, nil }, Timeout: time.Second})
	got := m.Observe(context.Background(), nil)
	if len(got) != 1 || got[0].Err == nil || redirected.Load() != 0 {
		t.Fatal("followed redirect")
	}
	for _, raw := range []string{"http://user:secret@127.0.0.1:8080", "http://127.0.0.1:8080/?key=value", "file:///tmp/metrics", "http://example.com:8080"} {
		if _, err := LocalTarget(raw); err == nil {
			t.Fatalf("unsafe local target accepted: %q", raw)
		}
	}
}

func TestDeadlineIsVisibleAndBounded(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	m := New(Options{Candidates: func(context.Context) ([]Target, error) { return []Target{{URL: s.URL}}, nil }, Timeout: 20 * time.Millisecond})
	start := time.Now()
	got := m.Observe(context.Background(), nil)
	if time.Since(start) > time.Second {
		t.Fatal("deadline not bounded")
	}
	found := false
	for _, o := range got {
		if o.Err != nil {
			found = true
		}
		if len(o.Result.Samples) > 0 {
			t.Fatal("timeout emitted counters")
		}
	}
	if !found {
		t.Fatal("deadline silently produced empty success")
	}
}

func TestGenerationAndRecoveryGuard(t *testing.T) {
	var down atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "offline", 503)
			return
		}
		fmt.Fprint(w, vllmBody)
	}))
	defer s.Close()
	generation := "first"
	m := New(Options{Candidates: func(context.Context) ([]Target, error) { return []Target{{URL: s.URL, Generation: generation}}, nil }})
	first := m.Observe(context.Background(), nil)[0]
	same := m.Observe(context.Background(), nil)[0]
	if first.ChangedAt != same.ChangedAt {
		t.Fatal("healthy scrape reset rate window")
	}
	generation = "replacement"
	next := m.Observe(context.Background(), nil)[0]
	if next.ChangedAt <= first.ChangedAt {
		t.Fatal("replacement bridged raw counters")
	}
	down.Store(true)
	m.Observe(context.Background(), nil)
	down.Store(false)
	recovered := m.Observe(context.Background(), nil)[0]
	if recovered.ChangedAt <= next.ChangedAt {
		t.Fatal("recovery bridged raw counters")
	}
}

func TestIdentityChangesDuringMetadataLookup(t *testing.T) {
	var reads atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprintf(w, `{"data":[{"id":"model-%d"}]}`, reads.Add(1))
			return
		}
		fmt.Fprint(w, llamaBody)
	}))
	defer s.Close()
	m := New(Options{Candidates: func(context.Context) ([]Target, error) { return []Target{{URL: s.URL}}, nil }})
	got := m.Observe(context.Background(), nil)[0]
	if got.Err == nil || got.State != "ambiguous" || len(got.Result.Samples) != 0 {
		t.Fatal("mixed model identities")
	}
}

func TestCandidateLimitIsVisible(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, vllmBody) }))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, vllmBody) }))
	defer b.Close()
	m := New(Options{Candidates: func(context.Context) ([]Target, error) { return []Target{{URL: a.URL}, {URL: b.URL}}, nil }, MaxTargets: 1})
	found := false
	for _, o := range m.Observe(context.Background(), nil) {
		if o.State == "candidate_limit" {
			found = true
		}
	}
	if !found {
		t.Fatal("silent truncated discovery")
	}
}

func TestMalformedOversizedAndIdentitylessBodies(t *testing.T) {
	for _, body := range []string{"not valid telemetry", strings.Repeat("x", 2048), llamaBody} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				fmt.Fprint(w, `{"data":[{"id":"one"},{"id":"two"}]}`)
				return
			}
			fmt.Fprint(w, body)
		}))
		m := New(Options{Candidates: func(context.Context) ([]Target, error) { return []Target{{URL: s.URL}}, nil }, MaxBody: 1024})
		got := m.Observe(context.Background(), nil)
		s.Close()
		if len(got) != 1 || got[0].Err == nil || len(got[0].Result.Samples) != 0 {
			t.Fatal("invalid identity/body produced measurements")
		}
	}
}

func TestEmptyScopeReportsDiscoveryHealth(t *testing.T) {
	m := New(Options{Candidates: func(context.Context) ([]Target, error) { return nil, nil }})
	got := m.Observe(context.Background(), nil)
	if len(got) != 1 || got[0].State != "discovering" || got[0].Err == nil || got[0].Registration.Engine != metrics.Unknown {
		t.Fatal("empty scope is silently healthy", got)
	}
}
