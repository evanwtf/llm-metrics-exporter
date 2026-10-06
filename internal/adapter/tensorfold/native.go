package tensorfold

import (
	"errors"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
)

// MapNative turns TensorFold's own exposition (tensorfold/server/metrics.py,
// v0.6) into canonical samples. The tensorfold_health:* families come from
// the MiaAI-Lab recipe's patch 0045; without them, those series stay absent.
// No series carries a model label, so served_model cannot be validated here:
// discovery reads the model from /v1/models.
func MapNative(fams promtext.Families, served string) (adapter.Result, error) {
	if !fams.Has(nativeDecode) || fams.Has(patchDecode) {
		return adapter.Result{}, errors.New("no tensorfold:generation_tokens_total: not a TensorFold v0.6 /metrics body")
	}
	model, err := adapter.SelectModel(fams, nativeDecode, "model", served)
	if err != nil {
		return adapter.Result{}, err
	}
	b := adapter.NewBuilder(fams, model)

	// Finished requests, folded when generate returns (a request that raises
	// is folded too). Decode tokens are the reply's tokens, the first one
	// included. No prefill tokens: prompt_tokens_total includes the cached
	// tokens, and the schema never derives computed tokens by subtraction.
	b.Value(&metrics.Tokens, []string{metrics.Decode}, nativeDecode, nil)
	b.Value(&metrics.PromptCachedTokens, nil, "tensorfold_health:cached_tokens_total", nil)

	// Request clock: each finished request's own engine stats, summed.
	b.Value(&metrics.RequestPhaseSeconds, []string{metrics.Prefill}, "tensorfold_health:prefill_seconds_total", nil)
	b.Value(&metrics.RequestPhaseSeconds, []string{metrics.Decode}, "tensorfold_health:decode_seconds_total", nil)

	// Streams that decode or fill now. Queued requests are requests_waiting,
	// which has no canonical series.
	b.Value(&metrics.RequestsRunning, nil, "tensorfold:requests_running", nil)
	// Arrival to the first generated token, as vLLM's.
	b.Histogram(&metrics.TimeToFirstToken, "tensorfold:time_to_first_token_seconds", nil)

	// The drafter's tokens verified and kept, on finished requests. The
	// target's own token in each round is not a draft token.
	b.Value(&metrics.SpecDraftTokens, nil, "tensorfold:mtp_drafted_total", nil)
	b.Value(&metrics.SpecAcceptedTokens, nil, "tensorfold:mtp_accepted_total", nil)

	return adapter.Result{Samples: b.Samples()}, b.Err()
}
