package sglang

import (
	"errors"
	"strings"
	"testing"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	at "github.com/evanwtf/llm-metrics-exporter/internal/adapter/adaptertest"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
)

const live = "../../../testdata/sglang/nightly-dev-cu13-20260921-0f6761b5.metrics.txt"

func TestLive(t *testing.T) {
	res, err := Map(at.LoadText(t, live), "MiMo-v2.6-Flash")
	if err != nil {
		t.Fatal(err)
	}
	s := res.Samples
	at.Valid(t, s)
	// realtime_tokens_total, not prompt_tokens_total (4,668,037), which
	// includes cache hits.
	at.Want(t, s, &metrics.Tokens, 971400, metrics.Prefill)
	at.Want(t, s, &metrics.PromptCachedTokens, 3829888)
	at.Want(t, s, &metrics.Tokens, 156755, metrics.Decode)
	at.Want(t, s, &metrics.RequestsRunning, 1)
	at.Want(t, s, &metrics.KVCacheUsage, 0.14)
	ttft, ok := at.Find(s, &metrics.TimeToFirstToken)
	if !ok || ttft.Hist.Count != 183 {
		t.Errorf("ttft %+v", ttft.Hist)
	}
	// SGLang has no phase-seconds counter and no speculative counters; see
	// docs/adapters.md. Absent, not guessed.
	at.Absent(t, s, &metrics.RequestPhaseSeconds)
	at.Absent(t, s, &metrics.EnginePhaseSeconds)
	at.Absent(t, s, &metrics.SpecDraftTokens)
	at.Absent(t, s, &metrics.SpecAcceptedTokens)
	at.Absent(t, s, &metrics.SpecVerifySteps)
	at.Absent(t, s, &metrics.Requests)
}

func TestMismatch(t *testing.T) {
	_, err := Map(at.LoadText(t, live), "MiMo-v2.5")
	if !errors.Is(err, adapter.ErrModelMismatch) {
		t.Fatalf("err %v", err)
	}
}

// Tensor-parallel ranks report the same work; only rank 0 counts.
// Data-parallel ranks do different work, so they add.
func TestRanks(t *testing.T) {
	fams, err := promtext.Parse(strings.NewReader(`
sglang:generation_tokens_total{model_name="m"} 1
sglang:realtime_tokens_total{mode="decode",model_name="m",tp_rank="0",pp_rank="0",moe_ep_rank="0"} 100
sglang:realtime_tokens_total{mode="decode",model_name="m",tp_rank="1",pp_rank="0",moe_ep_rank="0"} 100
sglang:num_running_reqs{model_name="m",tp_rank="0",dp_rank="0"} 2
sglang:num_running_reqs{model_name="m",tp_rank="0",dp_rank="1"} 3
sglang:token_usage{model_name="m",tp_rank="0",dp_rank="0"} 0.5
sglang:token_usage{model_name="m",tp_rank="0",dp_rank="1"} 0.25
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
	at.Want(t, s, &metrics.Tokens, 100, metrics.Decode)
	at.Want(t, s, &metrics.RequestsRunning, 5)
	// Two data-parallel caches have no single usage ratio; adding them
	// would read 0.75 of a cache that does not exist.
	at.Absent(t, s, &metrics.KVCacheUsage)
}

func TestNotSGLang(t *testing.T) {
	if _, err := Map(at.LoadText(t, "../../../testdata/llamacpp/b10809-5266f24da.metrics.txt"), ""); err == nil {
		t.Fatal("mapped a llama.cpp body as SGLang")
	}
}
