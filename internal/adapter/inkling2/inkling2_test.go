package inkling2

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	at "github.com/evanwtf/llm-metrics-exporter/internal/adapter/adaptertest"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
)

const (
	busy   = "../../../testdata/inkling2-exl3/299eeaf3-live-busy.health.json"
	idle   = "../../../testdata/inkling2-exl3/299eeaf3-live-idle.health.json"
	models = "../../../testdata/inkling2-exl3/299eeaf3-live.models.json"
	served = "inkling-2-small"
)

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Expected values are read from the fixture with jq (.stats.pre_tok,
// .stats.dec_tok, .stats.prefix_hit_tok), not from Map.
func TestBusy(t *testing.T) {
	h, err := Parse(read(t, busy))
	if err != nil {
		t.Fatal(err)
	}
	s := Map(h).Samples
	at.Valid(t, s)
	at.Want(t, s, &metrics.Tokens, 148, metrics.Prefill)
	at.Want(t, s, &metrics.Tokens, 1002, metrics.Decode)
	at.Want(t, s, &metrics.PromptCachedTokens, 0) // four lookups, no hit: a measured zero
	if len(s) != 3 {
		t.Errorf("%d samples, want 3", len(s))
	}
	// pre_time and dec_time split engine time badly (a prefill chunk returns
	// before the GPU finishes it); active omits requests still in prefill;
	// free_pages counts kept prompts as used; no finish reasons, TTFT or
	// proposed-draft counts.
	for _, def := range []*metrics.Def{
		&metrics.RequestPhaseSeconds, &metrics.EnginePhaseSeconds, &metrics.Requests,
		&metrics.RequestsRunning, &metrics.KVCacheUsage, &metrics.TimeToFirstToken,
		&metrics.SpecDraftTokens, &metrics.SpecAcceptedTokens, &metrics.SpecVerifySteps,
	} {
		at.Absent(t, s, def)
	}
}

// A server before its first request: the counters are a measured zero, so
// they are present.
func TestIdleIsMeasuredZero(t *testing.T) {
	h, err := Parse(read(t, idle))
	if err != nil {
		t.Fatal(err)
	}
	s := Map(h).Samples
	at.Valid(t, s)
	at.Want(t, s, &metrics.Tokens, 0, metrics.Prefill)
	at.Want(t, s, &metrics.Tokens, 0, metrics.Decode)
	at.Want(t, s, &metrics.PromptCachedTokens, 0)
}

// Other engines' /health and /metrics bodies never match the signature.
func TestParseRefusesOtherBodies(t *testing.T) {
	bodies := map[string][]byte{
		"vllm /health (empty 200)":   nil,
		"llama.cpp /health":          []byte(`{"status":"ok"}`),
		"FastAPI 404":                []byte(`{"detail":"Not Found"}`),
		"status not ok":              []byte(`{"status":"loading","stats":{},"spec":true,"kv_tokens":1,"active":0,"queued":0,"prefix":{"entries":0,"free_pages":1}}`),
		"stats without scheduler":    []byte(`{"status":"ok","stats":{"pre_tok":1},"spec":true,"kv_tokens":1,"active":0,"queued":0,"prefix":{"entries":0,"free_pages":1}}`),
		"tensorfold-like health":     []byte(`{"status":"ok","streams":1,"streams_max":4,"pool_tokens":100,"pool_free_tokens":50,"requests_total":3}`),
		"tensorfold v0.6 /metrics":   read(t, "../../../testdata/tensorfold/v0.6.0-miaai-glm-v1.8.metrics.txt"),
		"tensorfold v0.3.4 /metrics": read(t, "../../../testdata/tensorfold/v0.3.4-glm53-e9c8cbb.metrics.txt"),
		"llama.cpp /metrics":         read(t, "../../../testdata/llamacpp/b10809-5266f24da.metrics.txt"),
		"vllm stored":                read(t, "../../../testdata/vllm/stored-gpt-oss-20b.promql.json"),
		"inkling /v1/models":         read(t, models),
	}
	for name, body := range bodies {
		if _, err := Parse(body); !errors.Is(err, ErrNotInkling) {
			t.Errorf("%s: %v, want ErrNotInkling", name, err)
		}
	}
}

func server(t *testing.T, modelReads *atomic.Int32) *httptest.Server {
	t.Helper()
	health, ids := read(t, busy), read(t, models)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write(health)
		case "/v1/models":
			modelReads.Add(1)
			_, _ = w.Write(ids)
		default:
			http.Error(w, `{"detail":"Not Found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func collect(t *testing.T, endpoint, servedModel string) (adapter.Result, error) {
	t.Helper()
	a := Info.New(adapter.Config{Endpoint: endpoint, ServedModel: servedModel, Client: &http.Client{Timeout: time.Second}, MaxBody: 1 << 20})
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	return a.Collect(context.Background())
}

// served_model is checked against /v1/models, since /health names no model.
func TestCollectValidatesServedModel(t *testing.T) {
	var reads atomic.Int32
	s := server(t, &reads)
	res, err := collect(t, s.URL, served)
	if err != nil {
		t.Fatal(err)
	}
	at.Want(t, res.Samples, &metrics.Tokens, 1002, metrics.Decode)
	if _, err := collect(t, s.URL, "other"); !errors.Is(err, adapter.ErrModelMismatch) {
		t.Fatalf("served_model other: %v, want a mismatch", err)
	}
	before := reads.Load()
	if _, err := collect(t, s.URL, ""); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != before {
		t.Error("read /v1/models without served_model")
	}
}

// A server without the /health signature reads as down, never as zero.
func TestCollectRefusesOtherEngine(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer s.Close()
	if _, err := collect(t, s.URL, ""); !errors.Is(err, ErrNotInkling) {
		t.Fatalf("%v, want ErrNotInkling", err)
	}
	down := httptest.NewServer(http.NotFoundHandler())
	defer down.Close()
	var status *adapter.StatusError
	if _, err := collect(t, down.URL, ""); !errors.As(err, &status) {
		t.Fatalf("%v, want a status error", err)
	}
}

// 299eeaf3 adds stats.gone after the first client hang-up; 32419261 has no
// such key. Neither the signature nor the mapping depends on it, and it
// maps to no series in either case.
func TestGoneIsOptionalAndUnmapped(t *testing.T) {
	live := string(read(t, busy))
	if strings.Contains(live, `"gone"`) {
		t.Fatal("fixture changed: the live capture has no gone key")
	}
	withGone := strings.Replace(live, `"trim_ms_max":13.5}`, `"trim_ms_max":13.5,"gone":2}`, 1)
	if withGone == live {
		t.Fatal("could not insert gone")
	}
	for name, body := range map[string]string{"without gone": live, "with gone": withGone} {
		h, err := Parse([]byte(body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s := Map(h).Samples; len(s) != 3 {
			t.Errorf("%s: %d samples, want 3", name, len(s))
		}
	}
}

// The main line (d982916, i2_server.py lines 458 and 771-772) counts mixed
// prefill-and-decode steps into mix_pre_tok and mix_tok, not pre_tok and
// dec_tok. The adapter refuses it, with or without the prefix object, so
// tokens are never silently lost.
func TestMixedStepsRefused(t *testing.T) {
	mainLine := `{"status":"ok","stats":{"dec_steps":0,"dec_time":0.0,"dec_rows":0,"dec_tok":0,"pre_time":0.0,"pre_tok":0,"idle_time":0.0,"stall_time":0.0,"tool_ok":0,"tool_bad":0,"mix_steps":0,"mix_time":0.0,"mix_pre_tok":0,"mix_rows":0,"mix_tok":0,"mix_dsteps":0,"aborted":0,"pre_cached_tok":0,"reuse_a":0,"reuse_b":0,"reuse_evict":0,"dec_vrows":0,"dec_prows":0},"spec":true,"kv_tokens":823424,"active":0,"queued":0,"parked":0}`
	live := string(read(t, busy))
	withMix := strings.Replace(live, `"trim_ms_max":13.5}`, `"trim_ms_max":13.5,"mix_tok":7}`, 1)
	for name, body := range map[string]string{"main line": mainLine, "299eeaf3 plus mix_tok": withMix} {
		if _, err := Parse([]byte(body)); !errors.Is(err, ErrMixedSteps) {
			t.Errorf("%s: %v, want ErrMixedSteps", name, err)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(mainLine)) }))
	defer s.Close()
	if _, err := collect(t, s.URL, ""); !errors.Is(err, ErrMixedSteps) {
		t.Fatalf("pinned collect: %v, want ErrMixedSteps", err)
	}
}

// Only this server's /v1/models shape permits a /health read in discovery.
func TestIsModels(t *testing.T) {
	if id, ok := IsModels(read(t, models)); !ok || id != served {
		t.Fatalf("live capture: %q %v", id, ok)
	}
	for name, body := range map[string]string{
		"main line d982916 (no max_model_len)": `{"object":"list","data":[{"id":"x","object":"model","owned_by":"local"}]}`,
		"vllm":                                 `{"object":"list","data":[{"id":"x","object":"model","owned_by":"vllm","max_model_len":4096}]}`,
		"sglang":                               `{"object":"list","data":[{"id":"x","object":"model","owned_by":"sglang","max_model_len":4096}]}`,
		"llamacpp":                             `{"object":"list","data":[{"id":"x","object":"model","owned_by":"llamacpp"}]}`,
		"tensorfold":                           string(read(t, "../../../testdata/tensorfold/v0.6.0-miaai-glm-v1.8.models.json")),
		"string length":                        `{"data":[{"id":"x","owned_by":"local","max_model_len":"4096"}]}`,
		"two models":                           `{"data":[{"id":"a","owned_by":"local","max_model_len":1},{"id":"b","owned_by":"local","max_model_len":1}]}`,
		"empty id":                             `{"data":[{"id":"","owned_by":"local","max_model_len":1}]}`,
	} {
		if _, ok := IsModels([]byte(body)); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}
