package engines

import (
	"errors"
	"strings"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter/llamacpp"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter/sglang"
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
	models := f.LabelValues(c.metric, c.label)
	if len(models) > 1 {
		return d, ErrAmbiguous
	}
	if len(models) == 1 {
		d.Model = models[0]
	}
	return d, nil
}
