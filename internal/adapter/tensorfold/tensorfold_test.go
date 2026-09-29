package tensorfold

import (
	"errors"
	"testing"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	at "github.com/evanwtf/llm-metrics-exporter/internal/adapter/adaptertest"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
)

const (
	live   = "../../../testdata/tensorfold/v0.3.4-glm53-e9c8cbb.metrics.txt"
	warmup = "../../../testdata/tensorfold/v0.3.4-glm53-e9c8cbb-warmup.metrics.txt"
	served = "GLM-5.3-Flash-EXL3"
)

// Expected values are read from the fixture with grep, not from Map.
func TestLive(t *testing.T) {
	res, err := Map(at.LoadText(t, live), served)
	if err != nil {
		t.Fatal(err)
	}
	s := res.Samples
	at.Valid(t, s)
	at.Want(t, s, &metrics.Tokens, 2557, metrics.Decode)
	at.Want(t, s, &metrics.PromptCachedTokens, 175040)
	at.Want(t, s, &metrics.RequestPhaseSeconds, 70.3779, metrics.Prefill)
	at.Want(t, s, &metrics.RequestPhaseSeconds, 52.2505, metrics.Decode)

	// prompt_tokens_total (250,456) includes the 175,040 cached tokens.
	// Prefill tokens are never derived by subtraction, so they are absent.
	if _, ok := at.Find(s, &metrics.Tokens, metrics.Prefill); ok {
		t.Error("prefill tokens emitted: TensorFold has no computed-token counter")
	}
	// requests_total has no finish reason; requests_inflight counts
	// completions queued for a batch slot; decode_rounds counts rounds
	// with no draft too. None has a canonical home.
	at.Absent(t, s, &metrics.Requests)
	at.Absent(t, s, &metrics.RequestsRunning)
	at.Absent(t, s, &metrics.EnginePhaseSeconds)
	at.Absent(t, s, &metrics.KVCacheUsage)
	at.Absent(t, s, &metrics.TimeToFirstToken)
	at.Absent(t, s, &metrics.SpecDraftTokens)
	at.Absent(t, s, &metrics.SpecAcceptedTokens)
	at.Absent(t, s, &metrics.SpecVerifySteps)
}

// Before any cache hit the cached counter is a measured zero, not absent.
func TestWarmupZeroIsMeasured(t *testing.T) {
	res, err := Map(at.LoadText(t, warmup), served)
	if err != nil {
		t.Fatal(err)
	}
	at.Valid(t, res.Samples)
	at.Want(t, res.Samples, &metrics.PromptCachedTokens, 0)
	at.Want(t, res.Samples, &metrics.Tokens, 47, metrics.Decode)
}

func TestMismatch(t *testing.T) {
	_, err := Map(at.LoadText(t, live), "other")
	if !errors.Is(err, adapter.ErrModelMismatch) {
		t.Fatalf("err %v, want a mismatch", err)
	}
}

func TestNotTensorFold(t *testing.T) {
	fams := at.LoadText(t, "../../../testdata/llamacpp/b10809-5266f24da.metrics.txt")
	if _, err := Map(fams, ""); err == nil {
		t.Fatal("mapped a llama.cpp body as TensorFold")
	}
}
