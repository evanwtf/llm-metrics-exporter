package remotewrite

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evanwtf/llm-metrics-exporter/internal/collector"
	"github.com/evanwtf/llm-metrics-exporter/internal/discovery"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/prompb"
)

func TestDiscoveryRemoteWriteReplayKeepsOriginalIdentity(t *testing.T) {
	var phase atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch phase.Load() {
		case 1:
			if r.URL.Path == "/v1/models" {
				fmt.Fprint(w, `{"data":[{"id":"llama-model"}]}`)
				return
			}
			fmt.Fprint(w, "llamacpp:tokens_predicted_total 7\nllamacpp:prompt_tokens_total 30\n")
		case 2:
			http.Error(w, "offline", 503)
		default:
			fmt.Fprint(w, "vllm:generation_tokens_total{model_name=\"v-model\",engine=\"0\"} 10\nvllm:prompt_tokens_by_source_total{model_name=\"v-model\",engine=\"0\",source=\"local_compute\"} 20\n")
		}
	}))
	defer upstream.Close()
	var available atomic.Bool
	var mu sync.Mutex
	var delivered []prompb.WriteRequest
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		if !available.Load() {
			w.WriteHeader(503)
			return
		}
		mu.Lock()
		delivered = append(delivered, req)
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	manager := discovery.New(discovery.Options{Candidates: func(context.Context) ([]discovery.Target, error) { return []discovery.Target{{URL: upstream.URL}}, nil }, Timeout: time.Second})
	c := collector.New(collector.Options{Dir: t.TempDir(), Host: "test-host", Timeout: time.Second, Discovery: manager, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	defer c.Close()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	o := testOptions(t, receiver.URL)
	s := newTestSender(t, o, reg)
	// No exporter HTTP scrape: remote write itself drives discovery.
	for _, p := range []int32{0, 1, 2, 0} {
		phase.Store(p)
		if err := s.collect(); err != nil {
			t.Fatal(err)
		}
		if sent, err := s.sendOne(context.Background()); sent || err == nil {
			t.Fatal("outage lost batch")
		}
	}
	s.Close()
	s = newTestSender(t, o, reg)
	defer s.Close()
	available.Store(true)
	for range 4 {
		if _, err := s.sendOne(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 4 {
		t.Fatalf("delivered %d batches", len(delivered))
	}
	var previous int64
	for i, req := range delivered {
		counts, stale := map[string]float64{}, map[string]bool{}
		var timestamp int64
		for _, ts := range req.Timeseries {
			labels := map[string]string{}
			for _, l := range ts.Labels {
				labels[l.Name] = l.Value
			}
			if labels["__name__"] != "llm_tokens_total" || labels["phase"] != "decode" {
				continue
			}
			sample := ts.Samples[0]
			timestamp = sample.Timestamp
			if math.Float64bits(sample.Value) == staleBits {
				stale[labels["engine"]] = true
				continue
			}
			counts[labels["engine"]] = sample.Value
			expected := "v-model"
			if labels["engine"] == "llamacpp" {
				expected = "llama-model"
			}
			if labels["model"] != expected {
				t.Fatal("replay relabeled measurements")
			}
		}
		if timestamp <= previous {
			t.Fatal("replay changed timestamp ordering")
		}
		previous = timestamp
		switch i {
		case 0, 3:
			if len(counts) != 1 || counts["vllm"] != 10 {
				t.Fatal(counts)
			}
		case 1:
			if len(counts) != 1 || counts["llamacpp"] != 7 || !stale["vllm"] {
				t.Fatal("switch lost stale marker", counts, stale)
			}
		case 2:
			if len(counts) != 0 || !stale["llamacpp"] {
				t.Fatal("offline exported stale counters")
			}
		}
	}
}
