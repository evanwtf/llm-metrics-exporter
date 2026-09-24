// Package vllm maps vLLM's native /metrics to the canonical schema.
// docs/adapters.md, "vLLM", records the source and interval of every series.
package vllm

import (
	"errors"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
)

// Info registers the adapter.
var Info = adapter.Info{
	Engine:  "vllm",
	Version: "2",
	New: func(c adapter.Config) adapter.Adapter {
		return &adapter.Pull{Config: c, Map: Map}
	},
}

// Map turns a vLLM exposition into canonical samples. Series carry
// model_name, which validates served_model, and engine (the data-parallel
// index), which is preserved as worker to retain reset boundaries.
func Map(fams promtext.Families, served string) (adapter.Result, error) {
	if !fams.Has("vllm:generation_tokens_total") {
		return adapter.Result{}, errors.New("no vllm:generation_tokens_total: not a vLLM /metrics body")
	}
	model, err := adapter.SelectModel(fams, "vllm:generation_tokens_total", "model_name", served)
	if err != nil {
		return adapter.Result{}, err
	}
	b := adapter.NewWorkerBuilder(fams, model, "engine")

	// Tokens. prompt_tokens_total includes cache hits, so prefill reads the
	// computed source only. There is no fallback: an older vLLM without
	// prompt_tokens_by_source exports no prefill tokens.
	b.Value(&metrics.Tokens, []string{metrics.Prefill}, "vllm:prompt_tokens_by_source_total",
		promtext.LabelIs("source", "local_compute"))
	b.Value(&metrics.Tokens, []string{metrics.Decode}, "vllm:generation_tokens_total", nil)
	b.Value(&metrics.PromptCachedTokens, nil, "vllm:prompt_tokens_cached_total", nil)

	// Request clock, recorded when a request finishes: prefill is first
	// scheduled to first token, decode is first token to last token.
	b.HistogramSum(&metrics.RequestPhaseSeconds, []string{metrics.Prefill}, "vllm:request_prefill_time_seconds", nil)
	b.HistogramSum(&metrics.RequestPhaseSeconds, []string{metrics.Decode}, "vllm:request_decode_time_seconds", nil)

	for _, reason := range fams.LabelValues("vllm:request_success_total", "finished_reason") {
		b.Value(&metrics.Requests, []string{reason}, "vllm:request_success_total",
			promtext.LabelIs("finished_reason", reason))
	}
	b.Value(&metrics.RequestsRunning, nil, "vllm:num_requests_running", nil)
	b.Value(&metrics.KVCacheUsage, nil, "vllm:kv_cache_usage_perc", nil)
	b.Histogram(&metrics.TimeToFirstToken, "vllm:time_to_first_token_seconds", nil)

	// Present only when a speculative config is loaded.
	b.Value(&metrics.SpecDraftTokens, nil, "vllm:spec_decode_num_draft_tokens_total", nil)
	b.Value(&metrics.SpecAcceptedTokens, nil, "vllm:spec_decode_num_accepted_tokens_total", nil)
	b.Value(&metrics.SpecVerifySteps, nil, "vllm:spec_decode_num_drafts_total", nil)

	return adapter.Result{Samples: b.Samples()}, b.Err()
}
