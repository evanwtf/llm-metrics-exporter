package remotewrite

import (
	"context"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/prompb"
)

func testOptions(t *testing.T, url string) Options {
	return Options{URL: url, Dir: t.TempDir(), Host: "laptop-test", Version: "test", Interval: 10 * time.Millisecond, Timeout: 100 * time.Millisecond, MaxAge: time.Hour, MaxBytes: 64 << 20}
}
func testRegistry() (*prometheus.Registry, prometheus.Counter) {
	r := prometheus.NewRegistry()
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: "llm_tokens_total", ConstLabels: prometheus.Labels{"engine": "vllm", "model": "m", "phase": "decode"}})
	r.MustRegister(c)
	c.Add(7)
	return r, c
}
func mustGather(t *testing.T, g prometheus.Gatherer) []*dto.MetricFamily {
	t.Helper()
	f, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func newTestSender(t *testing.T, o Options, r prometheus.Gatherer) *Sender {
	t.Helper()
	s, err := New(o, r, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func readRequest(t *testing.T, r *http.Request) prompb.WriteRequest {
	t.Helper()
	if r.Header.Get("Content-Encoding") != "snappy" || r.Header.Get("Content-Type") != "application/x-protobuf" || r.Header.Get("X-Prometheus-Remote-Write-Version") != "0.1.0" || r.Header.Get("User-Agent") == "" {
		t.Error("missing remote-write headers")
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Error(err)
	}
	raw, err := snappy.Decode(nil, body)
	if err != nil {
		t.Error(err)
	}
	var req prompb.WriteRequest
	if err = req.Unmarshal(raw); err != nil {
		t.Error(err)
	}
	return req
}

func TestRetryRestartAndStaleness(t *testing.T) {
	var code atomic.Int32
	code.Store(503)
	var mu sync.Mutex
	var delivered []prompb.WriteRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		if code.Load() == 204 {
			mu.Lock()
			delivered = append(delivered, req)
			mu.Unlock()
		}
		w.WriteHeader(int(code.Load()))
	}))
	defer srv.Close()
	reg, c := testRegistry()
	o := testOptions(t, srv.URL)
	s := newTestSender(t, o, reg)
	if err := s.collect(); err != nil {
		t.Fatal(err)
	}
	if sent, err := s.sendOne(context.Background()); sent || err == nil {
		t.Fatal("503 should retain batch")
	}
	c.Add(3)
	if err := s.collect(); err != nil {
		t.Fatal(err)
	}
	before := s.Status()
	s.Close()
	s = newTestSender(t, o, reg)
	defer s.Close()
	if s.Status().QueuedBatches != before.QueuedBatches {
		t.Fatal("queue not restored")
	}
	code.Store(429)
	if _, err := s.sendOne(context.Background()); err == nil {
		t.Fatal("429 should retry")
	}
	code.Store(204)
	for range 2 {
		if _, err := s.sendOne(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	if len(delivered) != 2 || delivered[0].Timeseries[0].Samples[0].Value != 7 || delivered[1].Timeseries[0].Samples[0].Value != 10 || delivered[0].Timeseries[0].Samples[0].Timestamp >= delivered[1].Timeseries[0].Samples[0].Timestamp {
		t.Fatal("replay lost values/order")
	}
	labels := delivered[0].Timeseries[0].Labels
	mu.Unlock()
	for i, l := range labels {
		if l.Value == "" || i > 0 && labels[i-1].Name >= l.Name {
			t.Fatal("invalid labels")
		}
	}
	if err := s.enqueue(nil, s.timestamp+1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sendOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if math.Float64bits(delivered[2].Timeseries[0].Samples[0].Value) != staleBits {
		t.Fatal("missing stale marker")
	}
}

func TestQueueLimitExpiryRejectionAndOwnership(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	defer srv.Close()
	reg, _ := testRegistry()
	o := testOptions(t, srv.URL)
	s := newTestSender(t, o, reg)
	defer s.Close()
	if other, err := New(o, reg, nil); err == nil {
		other.Close()
		t.Fatal("two queue owners allowed")
	}
	if err := s.collect(); err != nil {
		t.Fatal(err)
	}
	// Use a tiny limit after construction to exercise the full-queue path.
	s.opts.MaxBytes = s.Status().QueuedBytes
	if err := s.collect(); err == nil {
		t.Fatal("queue cap ignored")
	}
	if _, err := s.sendOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.QueuedBatches != 0 || st.DroppedBatches != 2 || st.LastError == "" {
		t.Fatalf("status %+v", st)
	}
	s.opts.MaxBytes = o.MaxBytes
	current, _ := encodeFamilies(mustGather(t, reg), o.Host, time.Now().Add(-2*time.Hour).UnixMilli())
	if err := s.enqueue(current, time.Now().Add(-2*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sendOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Status().DroppedBatches != 3 {
		t.Fatal("expired batch not counted")
	}
}

func TestRunCollectsWithoutScrapesAndCancels(t *testing.T) {
	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { readRequest(t, r); received.Add(1); w.WriteHeader(204) }))
	defer srv.Close()
	reg, _ := testRegistry()
	s := newTestSender(t, testOptions(t, srv.URL), reg)
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.Now().Add(time.Second)
	for received.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked")
	}
	if received.Load() < 2 {
		t.Fatal("no scheduled delivery")
	}
}

func TestHTTPTimeoutAndToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readRequest(t, r)
		if r.Header.Get("Authorization") != "Bearer example-token" {
			t.Error("missing token")
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	reg, _ := testRegistry()
	o := testOptions(t, srv.URL)
	o.TokenFile = filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(o.TokenFile, []byte("example-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := newTestSender(t, o, reg)
	defer s.Close()
	if err := s.collect(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := s.sendOne(context.Background()); err == nil {
		t.Fatal("timeout expected")
	}
	if time.Since(start) > time.Second || s.Status().QueuedBatches != 1 {
		t.Fatal("timeout did not retain batch")
	}
}

func TestHistogramAndStableIdentity(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "llm_time_to_first_token_seconds", Buckets: []float64{1, 2}, ConstLabels: prometheus.Labels{"engine": "vllm", "model": "m"}})
	reg.MustRegister(h)
	h.Observe(0.5)
	h.Observe(3)
	series, err := encodeFamilies(mustGather(t, reg), "stable-laptop", 1234)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 5 {
		t.Fatalf("histogram series %d", len(series))
	}
	values := map[string]float64{}
	for _, s := range series {
		name, le := "", ""
		for _, l := range s.Labels {
			switch l.Name {
			case "__name__":
				name = l.Value
			case "le":
				le = l.Value
			case "instance":
				if l.Value != "stable-laptop" {
					t.Fatal("unstable identity")
				}
			}
		}
		values[name+le] = s.Samples[0].Value
		if s.Samples[0].Timestamp != 1234 {
			t.Fatal("timestamp changed")
		}
	}
	want := map[string]float64{"llm_time_to_first_token_seconds_bucket1": 1, "llm_time_to_first_token_seconds_bucket2": 1, "llm_time_to_first_token_seconds_bucket+Inf": 2, "llm_time_to_first_token_seconds_count": 2, "llm_time_to_first_token_seconds_sum": 3.5}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("histogram %v", values)
	}
}
