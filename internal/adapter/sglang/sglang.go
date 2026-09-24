// Package sglang maps SGLang's /metrics (with --enable-metrics) to the
// canonical schema. docs/adapters.md, "SGLang", records the source and
// interval of every series, and what SGLang does not export.
package sglang

import (
	"errors"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
)

// Info registers the adapter.
var Info = adapter.Info{
	Engine:  "sglang",
	Version: "2",
	New: func(c adapter.Config) adapter.Adapter {
		return &adapter.Pull{Config: c, Map: Map}
	},
}

// rankZero keeps one reporter per tensor, pipeline and expert-parallel group.
// Those ranks share one batch, so adding them would count it again.
// Data-parallel ranks (dp_rank) run different batches and remain separate. A
// label that is absent reads as "".
var rankZero = promtext.AllOf(
	promtext.LabelIn("tp_rank", "", "0"),
	promtext.LabelIn("pp_rank", "", "0"),
	promtext.LabelIn("moe_ep_rank", "", "0"),
)

// Map turns an SGLang exposition into canonical samples.
func Map(fams promtext.Families, served string) (adapter.Result, error) {
	if !fams.Has("sglang:generation_tokens_total") {
		return adapter.Result{}, errors.New("no sglang:generation_tokens_total: not an SGLang /metrics body")
	}
	model, err := adapter.SelectModel(fams, "sglang:generation_tokens_total", "model_name", served)
	if err != nil {
		return adapter.Result{}, err
	}
	b := adapter.NewWorkerBuilder(fams, promtext.AllOf(model, rankZero), "dp_rank")

	// All three token counters come from realtime_tokens_total, so they share
	// one update rule (each scheduler log interval). prompt_tokens_total
	// includes cache hits.
	mode := func(m string) promtext.Match { return promtext.LabelIs("mode", m) }
	b.Value(&metrics.Tokens, []string{metrics.Prefill}, "sglang:realtime_tokens_total", mode("prefill_compute"))
	b.Value(&metrics.PromptCachedTokens, nil, "sglang:realtime_tokens_total", mode("prefill_cache"))
	b.Value(&metrics.Tokens, []string{metrics.Decode}, "sglang:realtime_tokens_total", mode("decode"))

	b.Value(&metrics.RequestsRunning, nil, "sglang:num_running_reqs", nil)
	// token_usage is one cache's used share. Data-parallel ranks each have
	// their own cache; retain their worker identity instead of summing ratios.
	b.Value(&metrics.KVCacheUsage, nil, "sglang:token_usage", nil)
	// Request series carry is_streaming; both halves share bucket bounds.
	b.Histogram(&metrics.TimeToFirstToken, "sglang:time_to_first_token_seconds", nil)

	return adapter.Result{Samples: b.Samples()}, b.Err()
}
