package vllm

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
	gptOSS = "../../../testdata/vllm/stored-gpt-oss-20b.promql.json"
	qwen38 = "../../../testdata/vllm/stored-qwen3.8-flash-next.promql.json"
)

// Expected values are read from the fixtures with jq, not from this code.
func TestGptOSS(t *testing.T) {
	res, err := Map(at.LoadJSON(t, gptOSS), "")
	if err != nil {
		t.Fatal(err)
	}
	s := res.Samples
	at.Valid(t, s)
	// Prefill is computed tokens only: local_compute, not prompt_tokens_total
	// (5,029,615), which includes 4,666,384 cache hits.
	at.Want(t, s, &metrics.Tokens, 363231, metrics.Prefill)
	at.Want(t, s, &metrics.Tokens, 111129, metrics.Decode)
	at.Want(t, s, &metrics.PromptCachedTokens, 4666384)
	at.Want(t, s, &metrics.RequestPhaseSeconds, 70.97365798191458, metrics.Prefill)
	at.Want(t, s, &metrics.RequestPhaseSeconds, 2605.092228509093, metrics.Decode)
	at.Absent(t, s, &metrics.EnginePhaseSeconds) // vLLM has no engine clock
	at.Want(t, s, &metrics.Requests, 409, "stop")
	at.Want(t, s, &metrics.Requests, 0, "abort")
	at.Want(t, s, &metrics.RequestsRunning, 0)
	at.Want(t, s, &metrics.KVCacheUsage, 0)
	// No speculative config was loaded, so there are no spec counters: absent,
	// not zero.
	at.Absent(t, s, &metrics.SpecDraftTokens)
	at.Absent(t, s, &metrics.SpecAcceptedTokens)
	at.Absent(t, s, &metrics.SpecVerifySteps)

	ttft, ok := at.Find(s, &metrics.TimeToFirstToken)
	if !ok || ttft.Hist.Count != 414 || ttft.Hist.Sum != 74.48973751068115 {
		t.Errorf("ttft %+v", ttft.Hist)
	}
}

func TestSpeculative(t *testing.T) {
	res, err := Map(at.LoadJSON(t, qwen38), "")
	if err != nil {
		t.Fatal(err)
	}
	s := res.Samples
	at.Valid(t, s)
	at.Want(t, s, &metrics.Tokens, 33686, metrics.Prefill)
	at.Want(t, s, &metrics.PromptCachedTokens, 19200)
	at.Want(t, s, &metrics.SpecDraftTokens, 24)
	at.Want(t, s, &metrics.SpecAcceptedTokens, 20)
	at.Want(t, s, &metrics.SpecVerifySteps, 8)
}

func TestServedModel(t *testing.T) {
	fams := at.LoadJSON(t, gptOSS)
	res, err := Map(fams, "gpt-oss-20b")
	if err != nil {
		t.Fatal(err)
	}
	at.Want(t, res.Samples, &metrics.Tokens, 111129, metrics.Decode)

	_, err = Map(fams, "some-other-model")
	var mm *adapter.MismatchError
	if !errors.As(err, &mm) || mm.Reported[0] != "gpt-oss-20b" {
		t.Fatalf("err %v, want a mismatch reporting gpt-oss-20b", err)
	}
}

// A data-parallel server exports one series per engine index. They are
// different work, so they add.
func TestDataParallelEnginesAdd(t *testing.T) {
	fams, err := promtext.Parse(strings.NewReader(`
vllm:generation_tokens_total{engine="0",model_name="m"} 40
vllm:generation_tokens_total{engine="1",model_name="m"} 37
vllm:prompt_tokens_by_source_total{engine="0",model_name="m",source="local_compute"} 5
vllm:prompt_tokens_by_source_total{engine="1",model_name="m",source="local_compute"} 6
`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Map(fams, "m")
	if err != nil {
		t.Fatal(err)
	}
	at.WantWorker(t, res.Samples, "0", &metrics.Tokens, 40, metrics.Decode)
	at.WantWorker(t, res.Samples, "1", &metrics.Tokens, 37, metrics.Decode)
	at.WantWorker(t, res.Samples, "0", &metrics.Tokens, 5, metrics.Prefill)
	at.WantWorker(t, res.Samples, "1", &metrics.Tokens, 6, metrics.Prefill)
}

// An older vLLM without prompt_tokens_by_source must not fall back to
// prompt_tokens_total, which counts cache hits as prefill.
func TestNoPrefillWithoutBySource(t *testing.T) {
	fams, _ := promtext.Parse(strings.NewReader(`
vllm:prompt_tokens_total{engine="0",model_name="m"} 1000
vllm:generation_tokens_total{engine="0",model_name="m"} 10
`))
	res, err := Map(fams, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := at.Find(res.Samples, &metrics.Tokens, metrics.Prefill); ok {
		t.Fatal("prefill emitted from prompt_tokens_total")
	}
}

// A body with no vLLM series is some other server on the port.
func TestNotVLLM(t *testing.T) {
	fams := at.LoadText(t, "../../../testdata/llamacpp/b10809-5266f24da.metrics.txt")
	if _, err := Map(fams, ""); err == nil {
		t.Fatal("mapped a llama.cpp body as vLLM")
	}
}
