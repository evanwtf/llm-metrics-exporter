// Package llamacpp maps llama-server's /metrics (with --metrics) to the
// canonical schema. docs/adapters.md, "llama.cpp", records the source and
// interval of every series.
package llamacpp

import (
	"errors"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
)

// Info registers the adapter.
var Info = adapter.Info{
	Engine:  "llamacpp",
	Version: "1",
	New: func(c adapter.Config) adapter.Adapter {
		return &adapter.Pull{Config: c, Map: Map}
	},
}

// Map turns a llama-server exposition into canonical samples.
func Map(fams promtext.Families, served string) (adapter.Result, error) {
	if !fams.Has("llamacpp:tokens_predicted_total") {
		return adapter.Result{}, errors.New("no llamacpp:tokens_predicted_total: not a llama-server /metrics body")
	}
	// Upstream labels no model; some builds add model="..." to every sample.
	model, err := adapter.SelectModel(fams, "llamacpp:tokens_predicted_total", "model", served)
	if err != nil {
		return adapter.Result{}, err
	}
	b := adapter.NewBuilder(fams, model)

	// Upstream decaf508b (2026-08-13) rewrote the metrics and added
	// prompt_tokens_cached_total in the same commit. After it, prompt time is
	// batch wall time (engine clock); before it, a per-slot sum (request
	// clock). Tokens exclude cache hits on both sides.
	rewritten := fams.Has("llamacpp:prompt_tokens_cached_total")
	prefillClock := &metrics.RequestPhaseSeconds
	if rewritten {
		prefillClock = &metrics.EnginePhaseSeconds
	}

	b.Value(&metrics.Tokens, []string{metrics.Prefill}, "llamacpp:prompt_tokens_total", nil)
	b.Value(&metrics.Tokens, []string{metrics.Decode}, "llamacpp:tokens_predicted_total", nil)
	b.Value(&metrics.PromptCachedTokens, nil, "llamacpp:prompt_tokens_cached_total", nil)
	b.Value(prefillClock, []string{metrics.Prefill}, "llamacpp:prompt_seconds_total", nil)
	// Per-slot generation time, flushed when a slot resets: request clock on
	// both sides of the rewrite.
	b.Value(&metrics.RequestPhaseSeconds, []string{metrics.Decode}, "llamacpp:tokens_predicted_seconds_total", nil)
	b.Value(&metrics.RequestsRunning, nil, "llamacpp:requests_processing", nil)

	// Always exported after the rewrite, zero without a draft model: nothing
	// was proposed, which is true, not unmeasured.
	b.Value(&metrics.SpecDraftTokens, nil, "llamacpp:spec_decode_num_draft_tokens_total", nil)
	b.Value(&metrics.SpecAcceptedTokens, nil, "llamacpp:spec_decode_num_accepted_tokens_total", nil)
	b.Value(&metrics.SpecVerifySteps, nil, "llamacpp:spec_decode_num_drafts_total", nil)

	return adapter.Result{Samples: b.Samples()}, b.Err()
}
