// Package tensorfold maps the /metrics that the glm53-tensorfold-spark
// recipe's patch 0150 adds to TensorFold's server. Stock TensorFold serves no
// /metrics. docs/adapters.md, "TensorFold", records the source and interval of
// every series, and why the others stay unavailable.
package tensorfold

import (
	"errors"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
)

// Info registers the adapter. Without log_path it reads /metrics only; with
// it, it also counts finished requests from the request log.
var Info = adapter.Info{
	Engine:  "tensorfold",
	Version: "2",
	New: func(c adapter.Config) adapter.Adapter {
		if c.LogPath != "" {
			return &LogAdapter{cfg: c}
		}
		return &adapter.Pull{Config: c, Map: Map}
	},
}

// Map turns a TensorFold exposition into canonical samples. Every series
// carries model (the served name), which validates served_model.
func Map(fams promtext.Families, served string) (adapter.Result, error) {
	if !fams.Has("tensorfold_completion_tokens_total") {
		return adapter.Result{}, errors.New("no tensorfold_completion_tokens_total: not a TensorFold /metrics body")
	}
	model, err := adapter.SelectModel(fams, "tensorfold_completion_tokens_total", "model", served)
	if err != nil {
		return adapter.Result{}, err
	}
	b := adapter.NewBuilder(fams, model)

	// All counters move when a completion finishes without an error. No
	// prefill tokens: tensorfold_prompt_tokens_total includes the cached
	// tokens, and the schema reads computed tokens from the engine's own
	// counter, never by subtraction (design.md).
	b.Value(&metrics.Tokens, []string{metrics.Decode}, "tensorfold_completion_tokens_total", nil)
	b.Value(&metrics.PromptCachedTokens, nil, "tensorfold_cached_tokens_total", nil)

	// Request clock: each completion's own interval, summed. Prefill is slot
	// admission (or the start of generate) to the first token; decode is the
	// first token to the last.
	b.Value(&metrics.RequestPhaseSeconds, []string{metrics.Prefill}, "tensorfold_prefill_seconds_total", nil)
	b.Value(&metrics.RequestPhaseSeconds, []string{metrics.Decode}, "tensorfold_decode_seconds_total", nil)

	return adapter.Result{Samples: b.Samples()}, b.Err()
}
