package tensorfold

import (
	"errors"
	"testing"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	at "github.com/evanwtf/llm-metrics-exporter/internal/adapter/adaptertest"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
)

const (
	fixture = "../../../testdata/tensorfold/provisional-patch0150-render.metrics.txt"
	served  = "GLM-5.3-Flash-EXL3"
)

// Expected values are read from the fixture with grep, not from Map.
func TestFixture(t *testing.T) {
	res, err := Map(at.LoadText(t, fixture), served)
	if err != nil {
		t.Fatal(err)
	}
	s := res.Samples
	at.Valid(t, s)
	at.Want(t, s, &metrics.Tokens, 768, metrics.Decode)
	at.Want(t, s, &metrics.PromptCachedTokens, 4096)
	at.Want(t, s, &metrics.RequestPhaseSeconds, 2.4375, metrics.Prefill)
	at.Want(t, s, &metrics.RequestPhaseSeconds, 18.75, metrics.Decode)

	// prompt_tokens_total (8,296) includes the 4,096 cached tokens. Prefill
	// tokens are never derived by subtraction, so they are absent.
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

func TestMismatch(t *testing.T) {
	_, err := Map(at.LoadText(t, fixture), "other")
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
