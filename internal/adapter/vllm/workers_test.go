package vllm

import (
	"fmt"
	at "github.com/evanwtf/llm-metrics-exporter/internal/adapter/adaptertest"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
	"strings"
	"testing"
)

func TestWorkerResetAndMembership(t *testing.T) {
	// Both visible and aggregate-hidden resets, then disappearance/reappearance.
	for _, counts := range [][2]int{{100, 100}, {5, 110}, {2, 500}, {-1, 510}, {3, 520}} {
		text := fmt.Sprintf("vllm:generation_tokens_total{engine=\"1\",model_name=\"m\"} %d\n", counts[1])
		if counts[0] >= 0 {
			text += fmt.Sprintf("vllm:generation_tokens_total{engine=\"0\",model_name=\"m\"} %d\n", counts[0])
		}
		f, err := promtext.Parse(strings.NewReader(text))
		if err != nil {
			t.Fatal(err)
		}
		r, err := Map(f, "m")
		if err != nil {
			t.Fatal(err)
		}
		at.Valid(t, r.Samples)
		at.WantWorker(t, r.Samples, "1", &metrics.Tokens, float64(counts[1]), metrics.Decode)
		if counts[0] >= 0 {
			at.WantWorker(t, r.Samples, "0", &metrics.Tokens, float64(counts[0]), metrics.Decode)
		} else if len(r.Samples) != 1 {
			t.Fatal("departed worker retained")
		}
	}
}

func TestCacheRatiosStayPerWorker(t *testing.T) {
	for _, ratio := range []float64{0.4, 0.6} {
		text := "vllm:generation_tokens_total{model_name=\"m\"} 1\n"
		for i := 0; i < 2; i++ {
			text += fmt.Sprintf("vllm:kv_cache_usage_perc{engine=\"%d\",model_name=\"m\"} %g\n", i, ratio)
		}
		f, _ := promtext.Parse(strings.NewReader(text))
		r, err := Map(f, "m")
		if err != nil {
			t.Fatal(err)
		}
		at.Valid(t, r.Samples)
		for _, worker := range []string{"0", "1"} {
			at.WantWorker(t, r.Samples, worker, &metrics.KVCacheUsage, ratio)
		}
	}
}
