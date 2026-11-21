package engines

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter/inkling2"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter/llamacpp"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter/sglang"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter/tensorfold"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter/vllm"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
)

var (
	ErrUnknown     = errors.New("unknown telemetry signature")
	ErrAmbiguous   = errors.New("ambiguous engine or model identity")
	ErrUnsupported = errors.New("identified engine has no automatic telemetry adapter")
)

// Detection is evidence from this snapshot, not a configured identity. An
// empty Model requires verified metadata before measurements can be labeled.
type Detection struct {
	Engine, Version, Model string
	Map                    func(promtext.Families, string) (adapter.Result, error)
}

// Detect requires characteristic native counters, not an OpenAI API or a
// prefix alone. Known compatibility layers explicitly veto a native match.
func Detect(f promtext.Families) (Detection, error) {
	mlx := false
	for name := range f {
		if strings.HasPrefix(name, "mlx_serve:") {
			mlx = true
		}
	}
	type candidate struct {
		info          adapter.Info
		metric, label string
		mapFn         func(promtext.Families, string) (adapter.Result, error)
	}
	var matches []candidate
	if f.Has("vllm:generation_tokens_total") && (f.Has("vllm:prompt_tokens_by_source_total") || f.Has("vllm:cache_config_info")) && !mlx {
		matches = append(matches, candidate{vllm.Info, "vllm:generation_tokens_total", "model_name", vllm.Map})
	}
	if f.Has("llamacpp:tokens_predicted_total") && f.Has("llamacpp:prompt_tokens_total") {
		matches = append(matches, candidate{llamacpp.Info, "llamacpp:tokens_predicted_total", "model", llamacpp.Map})
	}
	if f.Has("sglang:generation_tokens_total") && f.Has("sglang:realtime_tokens_total") {
		matches = append(matches, candidate{sglang.Info, "sglang:generation_tokens_total", "model_name", sglang.Map})
	}
	// TensorFold: the model comes from /v1/models, not a label. Patch 0150's
	// mapping then validates its model label against it; TensorFold v0.6
	// labels no model. Each exposition is its own candidate, so a body with
	// both is ambiguous.
	if tensorfold.IsPatch0150(f) {
		matches = append(matches, candidate{tensorfold.Info, "", "", tensorfold.MapPatch0150})
	}
	if tensorfold.IsNative(f) {
		matches = append(matches, candidate{tensorfold.Info, "", "", tensorfold.MapNative})
	}
	if len(matches) > 1 || mlx && len(matches) > 0 {
		return Detection{}, ErrAmbiguous
	}
	if mlx {
		return Detection{Engine: "mlx-serve"}, ErrUnsupported
	}
	if len(matches) == 0 {
		return Detection{}, ErrUnknown
	}
	c := matches[0]
	d := Detection{Engine: c.info.Engine, Version: c.info.Version, Map: c.mapFn}
	if c.label == "" {
		return d, nil
	}
	models := f.LabelValues(c.metric, c.label)
	if len(models) > 1 {
		return d, ErrAmbiguous
	}
	if len(models) == 1 {
		d.Model = models[0]
	}
	return d, nil
}

// HealthCandidate reports whether a /v1/models body permits a GET /health
// probe, and returns the model id. Only the Inkling 2 server's shape
// qualifies. Other engines' /health can have side effects (SGLang's runs a
// generation), so discovery never reads it.
func HealthCandidate(models []byte) (string, bool) { return inkling2.IsModels(models) }

// DetectHealth identifies an engine that serves no /metrics by its JSON
// /health body. Discovery calls it only after /metrics answered with an HTTP
// error status and HealthCandidate accepted /v1/models. Its /health names no
// model, so Model is empty. The Result is that snapshot's measurements; Map
// is nil. A body without the signature wraps ErrUnknown; a known but
// unsupported version wraps ErrUnsupported; a failed request returns its
// own error, which is neither.
func DetectHealth(ctx context.Context, client *http.Client, endpoint string, maxBody int64) (Detection, adapter.Result, error) {
	h, err := inkling2.Fetch(ctx, adapter.Config{Endpoint: endpoint, Client: client, MaxBody: maxBody})
	d := Detection{Engine: inkling2.Info.Engine, Version: inkling2.Info.Version}
	switch {
	case errors.Is(err, inkling2.ErrMixedSteps):
		return d, adapter.Result{}, fmt.Errorf("%w: %w", ErrUnsupported, err)
	case errors.Is(err, inkling2.ErrNotInkling):
		return Detection{}, adapter.Result{}, fmt.Errorf("%w: %w", ErrUnknown, err)
	case err != nil:
		return Detection{}, adapter.Result{}, err
	}
	return d, inkling2.Map(h), nil
}
