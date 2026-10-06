// Package tensorfold maps TensorFold's two /metrics expositions. TensorFold
// v0.6 serves its own tensorfold:* families (the MiaAI-Lab recipe adds
// tensorfold_health:*). TensorFold v0.3.4 serves none, and the
// glm53-tensorfold-spark recipe's patch 0150 adds tensorfold_* families. One
// body never holds both. docs/adapters.md, "TensorFold", records the source
// and interval of every series, and why the others stay unavailable.
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
	Version: "3",
	New: func(c adapter.Config) adapter.Adapter {
		if c.LogPath != "" {
			return &LogAdapter{cfg: c}
		}
		return &adapter.Pull{Config: c, Map: Map}
	},
}

// Each exposition's signature: its own decode and prompt token counters.
const (
	patchDecode  = "tensorfold_completion_tokens_total"
	patchPrompt  = "tensorfold_prompt_tokens_total"
	nativeDecode = "tensorfold:generation_tokens_total"
	nativePrompt = "tensorfold:prompt_tokens_total"
)

// IsPatch0150 reports the glm53-tensorfold-spark patch 0150 exposition.
func IsPatch0150(fams promtext.Families) bool { return fams.Has(patchDecode) && fams.Has(patchPrompt) }

// IsNative reports TensorFold's own exposition (v0.6 and later).
func IsNative(fams promtext.Families) bool { return fams.Has(nativeDecode) && fams.Has(nativePrompt) }

// Map selects the mapping for the exposition in the body. It refuses a body
// with both decode counters: neither mapping may win.
func Map(fams promtext.Families, served string) (adapter.Result, error) {
	native, patch := fams.Has(nativeDecode), fams.Has(patchDecode)
	switch {
	case native && patch:
		return adapter.Result{}, errors.New("both TensorFold expositions in one /metrics body")
	case native:
		return MapNative(fams, served)
	default:
		return MapPatch0150(fams, served)
	}
}

// MapPatch0150 turns a patch 0150 exposition into canonical samples. Every
// series carries model (the served name), which validates served_model.
func MapPatch0150(fams promtext.Families, served string) (adapter.Result, error) {
	if !fams.Has(patchDecode) || fams.Has(nativeDecode) {
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
