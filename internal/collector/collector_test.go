package collector

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter/llamacpp"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/registration"
)

const llamaFixture = "../../testdata/llamacpp/b10809-5266f24da.metrics.txt"

type env struct {
	dir string
	c   *Collector
	reg *prometheus.Registry
}

func newEnv(t *testing.T, infos ...adapter.Info) *env {
	t.Helper()
	if len(infos) == 0 {
		infos = []adapter.Info{llamacpp.Info}
	}
	dir := t.TempDir()
	c := New(Options{
		Dir: dir, Host: "h1", Timeout: 200 * time.Millisecond,
		Client: http.DefaultClient, MaxBody: 1 << 20, MaxRead: 1 << 20,
		Adapters: infos, ExporterVersion: "0.0.0-test",
	})
	t.Cleanup(c.Close)
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(c)
	return &env{dir: dir, c: c, reg: reg}
}

func (e *env) register(t *testing.T, r registration.Registration) {
	t.Helper()
	if err := registration.Write(e.dir, r); err != nil {
		t.Fatal(err)
	}
}

func testArm(engine, endpoint string) registration.Registration {
	return registration.Registration{
		Version: 1, RunID: "run-1", Engine: engine, Endpoint: endpoint,
		Model: "smollm2-135m", Backend: "tiny", Nodes: 1, Issue: 675,
	}
}

func fixtureServer(t *testing.T, path string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (e *env) gather(t *testing.T) []*dto.MetricFamily {
	t.Helper()
	fams, err := e.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	return fams
}

// value finds one series by name and label subset.
func value(fams []*dto.MetricFamily, name string, labels map[string]string) (float64, bool) {
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
	next:
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			for k, v := range labels {
				if got[k] != v {
					continue next
				}
			}
			switch {
			case m.Counter != nil:
				return m.Counter.GetValue(), true
			case m.Gauge != nil:
				return m.Gauge.GetValue(), true
			case m.Histogram != nil:
				return float64(m.Histogram.GetSampleCount()), true
			}
		}
	}
	return 0, false
}

func want(t *testing.T, fams []*dto.MetricFamily, name string, labels map[string]string, v float64) {
	t.Helper()
	got, ok := value(fams, name, labels)
	if !ok {
		t.Errorf("%s%v: absent, want %v", name, labels, v)
	} else if got != v {
		t.Errorf("%s%v: %v, want %v", name, labels, got, v)
	}
}

// Operator requirement: every series carries engine and model.
func requireEngineAndModel(t *testing.T, fams []*dto.MetricFamily) {
	t.Helper()
	for _, f := range fams {
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			if got["engine"] == "" || got["model"] == "" {
				t.Errorf("%s %v: engine or model missing", f.GetName(), got)
			}
		}
	}
}

func TestNoRegistrationsNoSeries(t *testing.T) {
	e := newEnv(t)
	if fams := e.gather(t); len(fams) != 0 {
		t.Fatalf("series with nothing registered: %v", fams)
	}
}

func TestHealthyArm(t *testing.T) {
	e := newEnv(t)
	srv := fixtureServer(t, llamaFixture)
	e.register(t, testArm("llamacpp", srv.URL))
	fams := e.gather(t)
	requireEngineAndModel(t, fams)
	id := map[string]string{
		"engine": "llamacpp", "model": "smollm2-135m", "backend": "tiny", "host": "h1", "nodes": "1",
	}
	with := func(k, v string) map[string]string {
		m := map[string]string{k: v}
		for a, b := range id {
			m[a] = b
		}
		return m
	}
	want(t, fams, "llme_engine_up", id, 1)
	want(t, fams, "llme_exporter_registration_mismatch", id, 0)
	want(t, fams, "llme_tokens_total", with("phase", "decode"), 224)
	want(t, fams, "llme_tokens_total", with("phase", "prefill"), 278)
	want(t, fams, "llme_engine_phase_seconds_total", with("phase", "prefill"), 0.13508)
	want(t, fams, "llme_exporter_scrape_errors_total", id, 0)
	want(t, fams, "llme_exporter_arm_info", map[string]string{
		"engine": "llamacpp", "model": "smollm2-135m", "issue": "675",
		"adapter_version": "1", "exporter_version": "0.0.0-test",
	}, 1)
	if v, _ := value(fams, "llme_exporter_last_success_timestamp_seconds", id); v < float64(time.Now().Add(-time.Minute).Unix()) {
		t.Errorf("last success %v", v)
	}
}

// Silence is a bug: an arm that stops answering keeps its labels and reads 0.
func TestDownArmIsLoud(t *testing.T) {
	e := newEnv(t)
	srv := fixtureServer(t, llamaFixture)
	e.register(t, testArm("llamacpp", srv.URL))
	srv.Close()
	id := map[string]string{"engine": "llamacpp", "model": "smollm2-135m", "backend": "tiny"}
	fams := e.gather(t)
	requireEngineAndModel(t, fams)
	want(t, fams, "llme_engine_up", id, 0)
	want(t, fams, "llme_exporter_scrape_errors_total", id, 1)
	want(t, fams, "llme_exporter_last_success_timestamp_seconds", id, 0)
	if _, ok := value(fams, "llme_tokens_total", nil); ok {
		t.Error("token series emitted for a down arm")
	}
	fams = e.gather(t)
	want(t, fams, "llme_exporter_scrape_errors_total", id, 2)
}

func TestMismatchIsExported(t *testing.T) {
	e := newEnv(t)
	body := "llamacpp:tokens_predicted_total{model=\"other\"} 5\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	defer srv.Close()
	r := testArm("llamacpp", srv.URL)
	r.ServedModel = "smollm2-135m"
	e.register(t, r)
	fams := e.gather(t)
	want(t, fams, "llme_exporter_registration_mismatch", map[string]string{"backend": "tiny"}, 1)
	want(t, fams, "llme_engine_up", map[string]string{"backend": "tiny"}, 0)
}

// A registered engine this build has no adapter for is down, not ignored.
func TestEngineWithoutAnAdapter(t *testing.T) {
	e := newEnv(t)
	e.register(t, testArm("mlx-serve", "http://127.0.0.1:1"))
	fams := e.gather(t)
	requireEngineAndModel(t, fams)
	want(t, fams, "llme_engine_up", map[string]string{"engine": "mlx-serve"}, 0)
}

func TestInvalidRegistration(t *testing.T) {
	e := newEnv(t)
	os.WriteFile(filepath.Join(e.dir, "bad.yaml"), []byte("version: 1\nengine: vllm\n"), 0o644)
	fams := e.gather(t)
	requireEngineAndModel(t, fams)
	want(t, fams, "llme_exporter_registration_invalid", map[string]string{
		"engine": "vllm", "model": "unknown", "host": "h1", "file": "bad.yaml",
	}, 1)
}

// fake counts lifecycle calls.
type fake struct {
	starts, closes *atomic.Int32
	collect        func(context.Context) (adapter.Result, error)
}

func (f *fake) Start(context.Context) error { f.starts.Add(1); return nil }
func (f *fake) Collect(ctx context.Context) (adapter.Result, error) {
	return f.collect(ctx)
}
func (f *fake) Close() error { f.closes.Add(1); return nil }

func fakeInfo(starts, closes *atomic.Int32, collect func(context.Context) (adapter.Result, error)) adapter.Info {
	return adapter.Info{Engine: "vllm", Version: "9", New: func(adapter.Config) adapter.Adapter {
		return &fake{starts: starts, closes: closes, collect: collect}
	}}
}

func okCollect(context.Context) (adapter.Result, error) {
	return adapter.Result{Samples: []metrics.Sample{{Def: &metrics.Tokens, Labels: []string{"decode"}, Value: 1}}}, nil
}

// One adapter per run: kept across scrapes, restarted for a new run_id,
// closed when the registration goes.
func TestAdapterLifecycle(t *testing.T) {
	var starts, closes atomic.Int32
	e := newEnv(t, fakeInfo(&starts, &closes, okCollect))
	r := testArm("vllm", "http://127.0.0.1:1")
	e.register(t, r)
	e.gather(t)
	e.gather(t)
	if starts.Load() != 1 || closes.Load() != 0 {
		t.Fatalf("after two scrapes: starts %d closes %d", starts.Load(), closes.Load())
	}
	r.RunID = "run-2"
	e.register(t, r)
	e.gather(t)
	if starts.Load() != 2 || closes.Load() != 1 {
		t.Fatalf("after a new run: starts %d closes %d", starts.Load(), closes.Load())
	}
	if err := registration.Remove(e.dir, r.Backend, r.RunID); err != nil {
		t.Fatal(err)
	}
	if fams := e.gather(t); len(fams) != 0 {
		t.Fatalf("series after deregistration: %v", fams)
	}
	if closes.Load() != 2 {
		t.Fatalf("closes %d", closes.Load())
	}
}

// A slow engine must not hold the scrape past the timeout.
func TestSlowEngineTimesOut(t *testing.T) {
	var starts, closes atomic.Int32
	slow := func(ctx context.Context) (adapter.Result, error) {
		<-ctx.Done()
		return adapter.Result{}, ctx.Err()
	}
	e := newEnv(t, fakeInfo(&starts, &closes, slow))
	e.register(t, testArm("vllm", "http://127.0.0.1:1"))
	start := time.Now()
	fams := e.gather(t)
	if time.Since(start) > 2*time.Second {
		t.Fatal("scrape waited for the slow engine")
	}
	want(t, fams, "llme_engine_up", map[string]string{"engine": "vllm"}, 0)
}

// A sample that breaks the schema is dropped, and the rest still export.
func TestInvalidSampleIsDropped(t *testing.T) {
	var starts, closes atomic.Int32
	bad := func(context.Context) (adapter.Result, error) {
		return adapter.Result{Samples: []metrics.Sample{
			{Def: &metrics.Tokens, Labels: []string{"decode"}, Value: 7},
			{Def: &metrics.Tokens, Labels: []string{"blended"}, Value: 9},
			{Def: &metrics.Tokens, Labels: []string{"decode"}, Value: 8}, // duplicate
		}}, nil
	}
	e := newEnv(t, fakeInfo(&starts, &closes, bad))
	e.register(t, testArm("vllm", "http://127.0.0.1:1"))
	fams := e.gather(t)
	want(t, fams, "llme_tokens_total", map[string]string{"phase": "decode"}, 7)
	for _, f := range fams {
		if f.GetName() == "llme_tokens_total" && len(f.GetMetric()) != 1 {
			t.Fatalf("got %d token series", len(f.GetMetric()))
		}
	}
}

func TestHistogramDropsInfBucket(t *testing.T) {
	var starts, closes atomic.Int32
	h := func(context.Context) (adapter.Result, error) {
		return adapter.Result{Samples: []metrics.Sample{{Def: &metrics.TimeToFirstToken, Hist: &metrics.Hist{
			Count: 2, Sum: 1, Buckets: map[float64]uint64{0.5: 1, posInf(): 2},
		}}}}, nil
	}
	e := newEnv(t, fakeInfo(&starts, &closes, h))
	e.register(t, testArm("vllm", "http://127.0.0.1:1"))
	fams := e.gather(t)
	want(t, fams, "llme_time_to_first_token_seconds", map[string]string{"engine": "vllm"}, 2)
}

func posInf() float64 { return math.Inf(1) }
