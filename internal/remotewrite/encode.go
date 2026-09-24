// Package remotewrite collects the exporter's registry and delivers Remote
// Write 1.0 requests through a bounded, persistent FIFO.
package remotewrite

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/prompb"
)

const staleBits uint64 = 0x7ff0000000000002

func seriesKey(labels []prompb.Label) string {
	// Length prefixes avoid collisions with arbitrary label values.
	key := ""
	for _, l := range labels {
		key += fmt.Sprintf("%d:%s%d:%s", len(l.Name), l.Name, len(l.Value), l.Value)
	}
	return key
}

func encodeFamilies(families []*dto.MetricFamily, host string, timestamp int64) ([]prompb.TimeSeries, error) {
	var result []prompb.TimeSeries
	for _, f := range families {
		for _, m := range f.Metric {
			base := map[string]string{"job": "llm-metrics-exporter", "instance": host}
			for _, l := range m.Label {
				if l.GetValue() != "" {
					base[l.GetName()] = l.GetValue()
				}
			}
			add := func(name string, value float64, extra, extraValue string) {
				labels := make([]prompb.Label, 0, len(base)+2)
				for k, v := range base {
					labels = append(labels, prompb.Label{Name: k, Value: v})
				}
				labels = append(labels, prompb.Label{Name: "__name__", Value: name})
				if extra != "" {
					labels = append(labels, prompb.Label{Name: extra, Value: extraValue})
				}
				sort.Slice(labels, func(i, j int) bool { return labels[i].Name < labels[j].Name })
				result = append(result, prompb.TimeSeries{Labels: labels, Samples: []prompb.Sample{{Value: value, Timestamp: timestamp}}})
			}
			switch f.GetType() {
			case dto.MetricType_COUNTER:
				add(f.GetName(), m.GetCounter().GetValue(), "", "")
			case dto.MetricType_GAUGE:
				add(f.GetName(), m.GetGauge().GetValue(), "", "")
			case dto.MetricType_HISTOGRAM:
				h := m.GetHistogram()
				for _, b := range h.Bucket {
					if !math.IsInf(b.GetUpperBound(), 1) {
						add(f.GetName()+"_bucket", float64(b.GetCumulativeCount()), "le", strconv.FormatFloat(b.GetUpperBound(), 'g', -1, 64))
					}
				}
				add(f.GetName()+"_bucket", float64(h.GetSampleCount()), "le", "+Inf")
				add(f.GetName()+"_count", float64(h.GetSampleCount()), "", "")
				add(f.GetName()+"_sum", h.GetSampleSum(), "", "")
			default:
				return nil, fmt.Errorf("unsupported metric type for %s", f.GetName())
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return seriesKey(result[i].Labels) < seriesKey(result[j].Labels) })
	return result, nil
}

func withStale(current, previous []prompb.TimeSeries, timestamp int64) []prompb.TimeSeries {
	seen := map[string]bool{}
	for _, s := range current {
		seen[seriesKey(s.Labels)] = true
	}
	out := append([]prompb.TimeSeries(nil), current...)
	for _, s := range previous {
		if !seen[seriesKey(s.Labels)] {
			out = append(out, prompb.TimeSeries{Labels: s.Labels, Samples: []prompb.Sample{{Value: math.Float64frombits(staleBits), Timestamp: timestamp}}})
		}
	}
	return out
}

func activeSeries(request *prompb.WriteRequest) []prompb.TimeSeries {
	var out []prompb.TimeSeries
	for _, s := range request.Timeseries {
		if len(s.Samples) > 0 && math.Float64bits(s.Samples[0].Value) != staleBits {
			out = append(out, s)
		}
	}
	return out
}
