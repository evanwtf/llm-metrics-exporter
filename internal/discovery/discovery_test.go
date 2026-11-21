package discovery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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

// TensorFold (patch 0150) takes its model from /v1/models; the metrics label
// must agree, or the observation carries no measurements.
func TestTensorFoldModelFromMetadata(t *testing.T) {
	body, err := os.ReadFile("../../testdata/tensorfold/v0.3.4-glm53-e9c8cbb.metrics.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id string
		up bool
	}{{"GLM-5.3-Flash-EXL3", true}, {"other", false}} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				fmt.Fprintf(w, `{"object":"list","data":[{"id":%q,"object":"model","owned_by":"tensorfold"}]}`, tc.id)
				return
			}
			_, _ = w.Write(body)
		}))
		m := New(Options{Candidates: func(context.Context) ([]Target, error) { return []Target{{URL: s.URL}}, nil }, Timeout: time.Second})
		o := m.Observe(context.Background(), nil)[0]
		s.Close()
		if o.Registration.Engine != "tensorfold" || o.Registration.Model != tc.id || (o.Err == nil) != tc.up {
			t.Fatalf("%s: %+v", tc.id, o)
		}
		decode := -1.0
		for _, x := range o.Result.Samples {
			if x.Def.Name == metrics.Tokens.Name && x.Labels[0] == metrics.Decode {
				decode = x.Value
			}
		}
		if tc.up && decode != 2557 || !tc.up && decode != -1 {
			t.Fatalf("%s: decode tokens %v", tc.id, decode)
		}
	}
}

// TensorFold's own /metrics (v0.6, the MiaAI-Lab recipe) labels no model, so
// identity comes from /v1/models alone. Both fixtures are served unchanged.
func TestTensorFoldNativeModelFromMetadata(t *testing.T) {
	body, err := os.ReadFile("../../testdata/tensorfold/v0.6.0-miaai-glm-v1.8.metrics.txt")
	if err != nil {
		t.Fatal(err)
	}
	models, err := os.ReadFile("../../testdata/tensorfold/v0.6.0-miaai-glm-v1.8.models.json")
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write(models)
			return
		}
		_, _ = w.Write(body)
	}))
	defer s.Close()
	m := New(Options{Candidates: func(context.Context) ([]Target, error) { return []Target{{URL: s.URL}}, nil }, Timeout: time.Second})
	o := m.Observe(context.Background(), nil)[0]
	if o.Err != nil || o.State != "ready" || o.Registration.Engine != "tensorfold" || o.Registration.Model != "GLM-5.3-Flash-EXL3" {
		t.Fatalf("%+v", o)
	}
	got := map[string]float64{}
	for _, x := range o.Result.Samples {
		if x.Hist == nil {
			got[x.Def.Name+"/"+strings.Join(x.Labels, ",")] = x.Value
		}
	}
	for k, want := range map[string]float64{
		metrics.Tokens.Name + "/" + metrics.Decode: 504646,
		metrics.PromptCachedTokens.Name + "/":      39296704,
		metrics.RequestsRunning.Name + "/":         1,
		metrics.SpecDraftTokens.Name + "/":         532754,
		metrics.SpecAcceptedTokens.Name + "/":      354764,
	} {
		if v, ok := got[k]; !ok || v != want {
			t.Errorf("%s: %v (present %v), want %v", k, v, ok, want)
		}
	}
}

// inklingServer serves the live Inkling 2 captures. /metrics answers with
// metricsStatus (FastAPI's 404 by default), and health, when set, replaces
// the /health handler. It counts requests per path.
type inklingServer struct {
	*httptest.Server
	reads sync.Map // path -> *atomic.Int32
}

func (s *inklingServer) count(path string) int32 {
	v, ok := s.reads.Load(path)
	if !ok {
		return 0
	}
	return v.(*atomic.Int32).Load()
}

func newInklingServer(t *testing.T, models string, metricsHandler, health http.HandlerFunc) *inklingServer {
	t.Helper()
	body, err := os.ReadFile("../../testdata/inkling2-exl3/299eeaf3-live-busy.health.json")
	if err != nil {
		t.Fatal(err)
	}
	if models == "" {
		b, err := os.ReadFile("../../testdata/inkling2-exl3/299eeaf3-live.models.json")
		if err != nil {
			t.Fatal(err)
		}
		models = string(b)
	}
	if health == nil {
		health = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }
	}
	if metricsHandler == nil {
		metricsHandler = func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"detail":"Not Found"}`, http.StatusNotFound)
		}
	}
	s := &inklingServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := s.reads.LoadOrStore(r.URL.Path, new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
		switch r.URL.Path {
		case "/health":
			health(w, r)
		case "/v1/models":
			fmt.Fprint(w, models)
		case "/metrics":
			metricsHandler(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func observeOne(t *testing.T, m *Manager) Observation {
	t.Helper()
	got := m.Observe(context.Background(), nil)
	if len(got) != 1 {
		t.Fatalf("observations: %+v", got)
	}
	return got[0]
}

func newManager(url string, timeout time.Duration) *Manager {
	return New(Options{Candidates: func(context.Context) ([]Target, error) { return []Target{{URL: url}}, nil }, Timeout: timeout})
}

// The Inkling 2 server has no /metrics (FastAPI answers 404). Discovery reads
// /v1/models, then /health, then /v1/models again. Both fixtures are live
// captures, served unchanged.
func TestInklingFromHealth(t *testing.T) {
	s := newInklingServer(t, "", nil, nil)
	o := observeOne(t, newManager(s.URL, time.Second))
	if o.Err != nil || o.State != "ready" || o.Registration.Engine != "inkling2-exl3" || o.Registration.Model != "inkling-2-small" || o.Version != "1" {
		t.Fatalf("%+v", o)
	}
	if s.count("/metrics") != 1 || s.count("/v1/models") != 2 || s.count("/health") != 1 {
		t.Errorf("requests: metrics %d, models %d, health %d; want 1, 2, 1", s.count("/metrics"), s.count("/v1/models"), s.count("/health"))
	}
	got := map[string]float64{}
	for _, x := range o.Result.Samples {
		got[x.Def.Name+"/"+strings.Join(x.Labels, ",")] = x.Value
	}
	want := map[string]float64{
		metrics.Tokens.Name + "/" + metrics.Prefill: 148,
		metrics.Tokens.Name + "/" + metrics.Decode:  1002,
		metrics.PromptCachedTokens.Name + "/":       0,
	}
	if len(got) != len(want) {
		t.Errorf("samples %v, want only %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %v, want %v", k, got[k], v)
		}
	}
}

// The model must not change while discovery reads /health.
func TestInklingModelChangesDuringLookup(t *testing.T) {
	var reads atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprintf(w, `{"object":"list","data":[{"id":"model-%d","object":"model","owned_by":"local","max_model_len":524288}]}`, reads.Add(1))
		case "/health":
			b, _ := os.ReadFile("../../testdata/inkling2-exl3/299eeaf3-live-busy.health.json")
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	o := observeOne(t, newManager(s.URL, time.Second))
	if o.Err == nil || o.State != "ambiguous" || len(o.Result.Samples) != 0 {
		t.Fatalf("mixed model identities: %+v", o)
	}
}

// A /health that fails after a good observation (slow past the probe
// deadline, or a 503) is "unavailable" with the last labels kept, as a failed
// /metrics is: no identity flip and no new change timestamp.
func TestInklingHealthFailureKeepsIdentity(t *testing.T) {
	for name, fail := range map[string]http.HandlerFunc{
		"slow": func(w http.ResponseWriter, r *http.Request) { time.Sleep(1300 * time.Millisecond) },
		"503":  func(w http.ResponseWriter, r *http.Request) { http.Error(w, "busy", http.StatusServiceUnavailable) },
	} {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile("../../testdata/inkling2-exl3/299eeaf3-live-busy.health.json")
			if err != nil {
				t.Fatal(err)
			}
			var failing atomic.Bool
			s := newInklingServer(t, "", nil, func(w http.ResponseWriter, r *http.Request) {
				if failing.Load() {
					fail(w, r)
					return
				}
				_, _ = w.Write(body)
			})
			m := newManager(s.URL, 3*time.Second)
			first := observeOne(t, m)
			if first.State != "ready" {
				t.Fatalf("first: %+v", first)
			}
			failing.Store(true)
			o := observeOne(t, m)
			if o.Err == nil || o.State != "unavailable" || o.Registration != first.Registration || o.ChangedAt != first.ChangedAt || len(o.Result.Samples) != 0 {
				t.Fatalf("after a failed /health: %+v, first %+v", o, first)
			}
		})
	}
}

// The main line (d982916) counts mixed steps into mix_* keys, which this
// mapping does not read. Discovery reports it as unsupported, with no
// measurements.
func TestInklingMixedStepsUnsupported(t *testing.T) {
	s := newInklingServer(t, "", nil, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ok","stats":{"dec_steps":1,"dec_time":0.1,"dec_rows":1,"dec_tok":2,"pre_time":0.1,"pre_tok":5,"spec_steps":1,"plain_steps":0,"mix_steps":1,"mix_pre_tok":9,"mix_tok":3},"spec":true,"kv_tokens":64,"active":0,"queued":0,"prefix":{"entries":0,"free_pages":0}}`)
	})
	o := observeOne(t, newManager(s.URL, time.Second))
	if o.Err == nil || o.State != "unsupported" || o.Registration.Engine != "inkling2-exl3" || len(o.Result.Samples) != 0 {
		t.Fatalf("%+v", o)
	}
}

// Other engines are never sent GET /health: SGLang's runs a generation.
// Without /metrics, only a /v1/models with the Inkling 2 shape (one model,
// owned_by "local", numeric max_model_len) leads to a /health read. With a
// valid /metrics, the /metrics signature decides and /health is not read.
func TestInklingDoesNotCaptureOtherEngines(t *testing.T) {
	for name, models := range map[string]string{
		"sglang":           `{"object":"list","data":[{"id":"m","object":"model","owned_by":"sglang","root":"m","max_model_len":32768}]}`,
		"vllm":             `{"object":"list","data":[{"id":"m","object":"model","owned_by":"vllm","root":"m","max_model_len":32768}]}`,
		"llamacpp":         `{"object":"list","data":[{"id":"m","object":"model","owned_by":"llamacpp","meta":{}}]}`,
		"local, no length": `{"object":"list","data":[{"id":"m","object":"model","owned_by":"local"}]}`,
		"local, two":       `{"object":"list","data":[{"id":"a","owned_by":"local","max_model_len":1},{"id":"b","owned_by":"local","max_model_len":1}]}`,
		"not found":        `{"detail":"Not Found"}`,
	} {
		s := newInklingServer(t, models, nil, nil)
		o := observeOne(t, newManager(s.URL, time.Second))
		if s.count("/health") != 0 || o.Err == nil || o.State != "unavailable" || o.Registration.Engine != metrics.Unknown || len(o.Result.Samples) != 0 {
			t.Errorf("%s: %d /health reads, %+v", name, s.count("/health"), o)
		}
	}
	for _, tc := range []struct{ path, engine string }{
		{"../../testdata/tensorfold/v0.6.0-miaai-glm-v1.8.metrics.txt", "tensorfold"},
		{"../../testdata/tensorfold/v0.3.4-glm53-e9c8cbb.metrics.txt", "tensorfold"},
		{"../../testdata/llamacpp/b10809-5266f24da.metrics.txt", "llamacpp"},
		{"", "vllm"},
	} {
		body := []byte(vllmBody)
		if tc.path != "" {
			var err error
			if body, err = os.ReadFile(tc.path); err != nil {
				t.Fatal(err)
			}
		}
		// Even an Inkling-shaped /v1/models does not lead to /health here.
		s := newInklingServer(t, `{"object":"list","data":[{"id":"GLM-5.3-Flash-EXL3","object":"model","owned_by":"local","max_model_len":1}]}`,
			func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }, nil)
		o := observeOne(t, newManager(s.URL, time.Second))
		if o.Registration.Engine != tc.engine || s.count("/health") != 0 {
			t.Errorf("%s: %d /health reads, %+v", tc.engine, s.count("/health"), o)
		}
	}
}

// A /metrics that redirects or times out is not an HTTP error status from
// the server itself, so neither leads to /v1/models or /health. A 503 is a
// status: it reads as today (unavailable) unless /v1/models has the
// Inkling 2 shape, and a non-Inkling server gets no /health read.
func TestInklingProbeOnlyAfterStatus(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"redirect": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) },
		"timeout":  func(w http.ResponseWriter, r *http.Request) { time.Sleep(1300 * time.Millisecond) },
	} {
		s := newInklingServer(t, "", h, nil)
		o := observeOne(t, newManager(s.URL, 3*time.Second))
		if s.count("/health") != 0 || s.count("/v1/models") != 0 || o.Err == nil || o.State != "unavailable" {
			t.Errorf("%s: health %d, models %d, %+v", name, s.count("/health"), s.count("/v1/models"), o)
		}
	}
	unavailable := func(w http.ResponseWriter, r *http.Request) { http.Error(w, "busy", http.StatusServiceUnavailable) }
	s := newInklingServer(t, `{"object":"list","data":[{"id":"m","owned_by":"llamacpp"}]}`, unavailable, nil)
	o := observeOne(t, newManager(s.URL, time.Second))
	if s.count("/health") != 0 || o.Err == nil || o.State != "unavailable" || o.Registration.Engine != metrics.Unknown {
		t.Errorf("503: health %d, %+v", s.count("/health"), o)
	}
	s = newInklingServer(t, "", unavailable, nil)
	if o := observeOne(t, newManager(s.URL, time.Second)); o.State != "ready" || o.Registration.Engine != "inkling2-exl3" {
		t.Errorf("503 with an Inkling 2 /v1/models: %+v", o)
	}
}
