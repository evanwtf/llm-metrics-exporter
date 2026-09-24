// Package engines lists the adapters compiled into the exporter.
package engines

import (
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter/ds4"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter/llamacpp"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter/sglang"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter/vllm"
)

// All returns every compiled-in adapter. A registered engine that is not
// here exports llme_engine_up 0.
func All() []adapter.Info {
	return []adapter.Info{vllm.Info, llamacpp.Info, sglang.Info, ds4.Info}
}
