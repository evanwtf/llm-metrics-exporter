package llamacpp

import (
	"errors"
	"strings"
	"testing"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	at "github.com/evanwtf/llm-metrics-exporter/internal/adapter/adaptertest"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
)

const (
	live = "../../../testdata/llamacpp/b10809-5266f24da.metrics.txt"
	idle = "../../../testdata/llamacpp/b10809-5266f24da-idle.metrics.txt"
)

// b10809 is after the 2026-08-13 metrics rewrite: prefill seconds are on the
// engine clock, decode seconds on the request clock.
func TestAfterTheRewrite(t *testing.T) {
	res, err := Map(at.LoadText(t, live), "")
	if err != nil {
		t.Fatal(err)
	}
	s := res.Samples
	at.Valid(t, s)
	at.Want(t, s, &metrics.Tokens, 278, metrics.Prefill)
	at.Want(t, s, &metrics.Tokens, 224, metrics.Decode)
	at.Want(t, s, &metrics.PromptCachedTokens, 254)
	at.Want(t, s, &metrics.EnginePhaseSeconds, 0.13508, metrics.Prefill)
	at.Want(t, s, &metrics.RequestPhaseSeconds, 0.434104, metrics.Decode)
	if _, ok := at.Find(s, &metrics.RequestPhaseSeconds, metrics.Prefill); ok {
		t.Error("prefill seconds on the request clock after the rewrite")
	}
	at.Want(t, s, &metrics.RequestsRunning, 0)
	at.Want(t, s, &metrics.SpecDraftTokens, 0)
	at.Want(t, s, &metrics.SpecAcceptedTokens, 0)
	at.Want(t, s, &metrics.SpecVerifySteps, 0)
	// llama.cpp has no request counter, KV-cache gauge or TTFT histogram.
	at.Absent(t, s, &metrics.Requests)
	at.Absent(t, s, &metrics.KVCacheUsage)
	at.Absent(t, s, &metrics.TimeToFirstToken)
}

// A server that has served nothing is up, with every counter at zero.
func TestIdleIsUpAndZero(t *testing.T) {
	res, err := Map(at.LoadText(t, idle), "")
	if err != nil {
		t.Fatal(err)
	}
	at.Valid(t, res.Samples)
	at.Want(t, res.Samples, &metrics.Tokens, 0, metrics.Decode)
}

// Before upstream decaf508b (2026-08-13) there is no prompt_tokens_cached_total,
// and prompt seconds are per-slot sums: the request clock. The body follows
// the pre-rewrite source (on_prompt_eval, t_prompt_processing_total).
func TestBeforeTheRewrite(t *testing.T) {
	fams, err := promtext.Parse(strings.NewReader(`# HELP llamacpp:prompt_tokens_total Number of prompt tokens processed.
# TYPE llamacpp:prompt_tokens_total counter
llamacpp:prompt_tokens_total 500
# TYPE llamacpp:prompt_seconds_total counter
llamacpp:prompt_seconds_total 0.5
# TYPE llamacpp:tokens_predicted_total counter
llamacpp:tokens_predicted_total 100
# TYPE llamacpp:tokens_predicted_seconds_total counter
llamacpp:tokens_predicted_seconds_total 2
# TYPE llamacpp:requests_processing gauge
llamacpp:requests_processing 1
`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Map(fams, "")
	if err != nil {
		t.Fatal(err)
	}
	s := res.Samples
	at.Valid(t, s)
	at.Want(t, s, &metrics.RequestPhaseSeconds, 0.5, metrics.Prefill)
	at.Absent(t, s, &metrics.EnginePhaseSeconds)
	at.Absent(t, s, &metrics.PromptCachedTokens)
	at.Want(t, s, &metrics.Tokens, 500, metrics.Prefill)
}

// Some builds label every sample with model; that validates served_model.
func TestModelLabel(t *testing.T) {
	fams, _ := promtext.Parse(strings.NewReader(`
llamacpp:prompt_tokens_total{model="qwen3.8-flash-next-q3-nothink"} 1336740
llamacpp:prompt_tokens_cached_total{model="qwen3.8-flash-next-q3-nothink"} 15369300
llamacpp:tokens_predicted_total{model="qwen3.8-flash-next-q3-nothink"} 85445
`))
	res, err := Map(fams, "qwen3.8-flash-next-q3-nothink")
	if err != nil {
		t.Fatal(err)
	}
	at.Want(t, res.Samples, &metrics.Tokens, 85445, metrics.Decode)
	if _, err := Map(fams, "other"); !errors.Is(err, adapter.ErrModelMismatch) {
		t.Fatalf("err %v, want a mismatch", err)
	}
}

func TestNotLlamaCpp(t *testing.T) {
	fams := at.LoadJSON(t, "../../../testdata/vllm/stored-gpt-oss-20b.promql.json")
	if _, err := Map(fams, ""); err == nil {
		t.Fatal("mapped a vLLM body as llama.cpp")
	}
}
