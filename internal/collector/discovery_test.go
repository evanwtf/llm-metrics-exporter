package collector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evanwtf/llm-metrics-exporter/internal/discovery"
	"github.com/prometheus/client_golang/prometheus"
)

// Actual OS listener enumeration, filtered to test-owned ports so the test
// never probes the developer's production services. Exporter and scope closure
// stay alive while listeners appear, disappear and run concurrently.
func TestDiscoveredPortsConcurrentModelsAndWorkerResets(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("OS listener discovery")
	}
	var mu sync.Mutex
	owned := map[string]bool{}
	start := func(body *string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mu.Lock(); defer mu.Unlock(); fmt.Fprint(w, *body) }))
		owned[s.URL] = true
		return s
	}
	vbody := "vllm:generation_tokens_total{model_name=\"first\",engine=\"0\"} 100\nvllm:generation_tokens_total{model_name=\"first\",engine=\"1\"} 200\nvllm:cache_config_info{model_name=\"first\"} 1\n"
	a := start(&vbody)
	defer a.Close()
	manager := discovery.New(discovery.Options{Candidates: func(ctx context.Context) ([]discovery.Target, error) {
		all, err := discovery.LocalListeners(ctx)
		var chosen []discovery.Target
		for _, target := range all {
			if owned[target.URL] {
				chosen = append(chosen, target)
			}
		}
		return chosen, err
	}, Nodes: 2, Timeout: time.Second})
	c := New(Options{Dir: t.TempDir(), Host: "test-host", Timeout: time.Second, Discovery: manager, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	defer c.Close()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	gather := func() map[string]float64 {
		t.Helper()
		families, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]float64{}
		for _, f := range families {
			if f.GetName() != "llme_tokens_total" {
				continue
			}
			for _, m := range f.Metric {
				labels := map[string]string{}
				for _, l := range m.Label {
					labels[l.GetName()] = l.GetValue()
				}
				if labels["nodes"] != "2" {
					t.Fatal("lost asserted topology")
				}
				if labels["phase"] == "decode" {
					out[labels["model"]+"/"+labels["worker"]] = m.GetCounter().GetValue()
				}
			}
		}
		return out
	}
	got := gather()
	if len(got) != 2 || got["first/0"] != 100 || got["first/1"] != 200 {
		t.Fatal(got)
	}
	mu.Lock()
	vbody = strings.Replace(vbody, " 100\n", " 3\n", 1)
	mu.Unlock()
	got = gather()
	if got["first/0"] != 3 || got["first/1"] != 200 {
		t.Fatal("worker reset was flattened", got)
	}
	lbody := "llamacpp:tokens_predicted_total{model=\"second\"} 7\nllamacpp:prompt_tokens_total{model=\"second\"} 30\n"
	b := start(&lbody)
	defer b.Close()
	got = gather()
	if len(got) != 3 || got["second/default"] != 7 {
		t.Fatal("new/concurrent port not discovered", got)
	}
	a.Close()
	got = gather()
	if len(got) != 1 || got["second/default"] != 7 {
		t.Fatal("retired counters survived", got)
	}
	mu.Lock()
	lbody = strings.ReplaceAll(lbody, "second", "third")
	mu.Unlock()
	got = gather()
	if len(got) != 1 || got["third/default"] != 7 {
		t.Fatal("model change not followed", got)
	}
}
