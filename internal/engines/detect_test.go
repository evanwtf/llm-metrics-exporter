package engines

import (
	"strings"
	"testing"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter/adaptertest"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
)

func TestDetectCapturedTelemetry(t *testing.T) {
	for _, tc := range []struct {
		engine, path string
		stored       bool
	}{
		{"vllm", "../../testdata/vllm/stored-gpt-oss-20b.promql.json", true},
		{"llamacpp", "../../testdata/llamacpp/b10809-5266f24da.metrics.txt", false},
		{"sglang", "../../testdata/sglang/nightly-dev-cu13-20260921-0f6761b5.metrics.txt", false},
		{"tensorfold", "../../testdata/tensorfold/v0.3.4-glm53-e9c8cbb.metrics.txt", false},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			var f promtext.Families
			if tc.stored {
				f = adaptertest.LoadJSON(t, tc.path)
			} else {
				f = adaptertest.LoadText(t, tc.path)
			}
			d, err := Detect(f)
			if err != nil || d.Engine != tc.engine || d.Map == nil {
				t.Fatalf("detection: %+v, %v", d, err)
			}
			if tc.engine != "llamacpp" && tc.engine != "tensorfold" && d.Model == "" {
				t.Fatal("lost upstream model identity")
			}
			// TensorFold's model comes from /v1/models (discovery), not the label.
			if tc.engine == "tensorfold" && d.Model != "" {
				t.Fatalf("model %q from the label, want it from /v1/models", d.Model)
			}
		})
	}
}

func TestDetectDoesNotGuess(t *testing.T) {
	for _, body := range []string{
		"", "other_metric 1\n", "vllm:generation_tokens_total 1\n",
		"vllm:generation_tokens_total 1\nmlx_serve:prefill_tokens_total 2\n",
		"tensorfold_completion_tokens_total 1\n",
		"tensorfold_completion_tokens_total 1\ntensorfold_prompt_tokens_total 2\nllamacpp:tokens_predicted_total 1\nllamacpp:prompt_tokens_total 2\n",
		"vllm:generation_tokens_total 1\nvllm:prompt_tokens_by_source_total 2\nllamacpp:tokens_predicted_total 1\nllamacpp:prompt_tokens_total 2\n",
		"vllm:generation_tokens_total{model_name=\"a\"} 1\nvllm:generation_tokens_total{model_name=\"b\"} 2\nvllm:prompt_tokens_by_source_total 3\n",
	} {
		f, err := promtext.Parse(strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Detect(f); err == nil {
			t.Errorf("accepted ambiguous/unsupported body %q", body)
		}
	}
}
