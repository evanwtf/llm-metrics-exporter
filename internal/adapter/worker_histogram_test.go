package adapter

import (
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
	"strings"
	"testing"
)

func TestWorkerHistogramResetBoundaries(t *testing.T) {
	text := `# TYPE h histogram
h_bucket{engine="0",le="1"} 2
h_bucket{engine="0",le="+Inf"} 2
h_count{engine="0"} 2
h_sum{engine="0"} 1
h_bucket{engine="1",le="1"} 100
h_bucket{engine="1",le="+Inf"} 100
h_count{engine="1"} 100
h_sum{engine="1"} 50
`
	for _, input := range []string{text, strings.ReplaceAll(text, "} 2\n", "} 0\n")} {
		fams, err := promtext.Parse(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		b := NewWorkerBuilder(fams, nil, "engine")
		b.Histogram(&metrics.TimeToFirstToken, "h", nil)
		b.HistogramSum(&metrics.RequestPhaseSeconds, []string{"decode"}, "h", nil)
		if err = b.Err(); err != nil {
			t.Fatal(err)
		}
		if len(b.Samples()) != 4 {
			t.Fatal("histogram workers merged")
		}
		for _, s := range b.Samples() {
			if s.Worker == "1" {
				if s.Hist != nil && s.Hist.Count != 100 || s.Hist == nil && s.Value != 50 {
					t.Fatal("unaffected worker changed")
				}
			}
		}
	}
}
