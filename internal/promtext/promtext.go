// Package promtext reads an engine's Prometheus exposition text and answers
// the questions adapters ask of it: is this metric here, and what do its
// matching series add up to.
package promtext

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
)

// Families is a parsed exposition, keyed by metric family name.
type Families map[string]*dto.MetricFamily

// Match selects series by their labels. A nil Match selects every series.
type Match func(labels map[string]string) bool

// Parse reads Prometheus text exposition format.
func Parse(r io.Reader) (Families, error) {
	// Engine names contain colons (vllm:..., sglang:...), which the legacy
	// scheme allows.
	p := expfmt.NewTextParser(model.LegacyValidation)
	fams, err := p.TextToMetricFamilies(r)
	if err != nil {
		return nil, err
	}
	return Families(fams), nil
}

// LabelIs matches series whose label has the value. A missing label has the
// value "".
func LabelIs(name, value string) Match {
	return func(l map[string]string) bool { return l[name] == value }
}

// LabelIn matches series whose label has one of the values.
func LabelIn(name string, values ...string) Match {
	return func(l map[string]string) bool {
		for _, v := range values {
			if l[name] == v {
				return true
			}
		}
		return false
	}
}

// AllOf matches series that every non-nil Match matches.
func AllOf(ms ...Match) Match {
	return func(l map[string]string) bool {
		for _, m := range ms {
			if m != nil && !m(l) {
				return false
			}
		}
		return true
	}
}

// Labels returns a series' labels as a map.
func Labels(m *dto.Metric) map[string]string {
	out := make(map[string]string, len(m.GetLabel()))
	for _, lp := range m.GetLabel() {
		out[lp.GetName()] = lp.GetValue()
	}
	return out
}

// Has reports whether the exposition has the family, with any series.
func (f Families) Has(name string) bool {
	fam, ok := f[name]
	return ok && len(fam.GetMetric()) > 0
}

// Sum adds the values of the family's series that m selects. found is false
// when no series was selected, so an absent metric never reads as zero.
func (f Families) Sum(name string, m Match) (total float64, found bool) {
	fam, ok := f[name]
	if !ok {
		return 0, false
	}
	for _, s := range fam.GetMetric() {
		if m != nil && !m(Labels(s)) {
			continue
		}
		v, ok := value(s)
		if !ok {
			continue
		}
		total += v
		found = true
	}
	return total, found
}

func value(s *dto.Metric) (float64, bool) {
	switch {
	case s.Counter != nil:
		return s.Counter.GetValue(), true
	case s.Gauge != nil:
		return s.Gauge.GetValue(), true
	case s.Untyped != nil:
		return s.Untyped.GetValue(), true
	}
	return 0, false
}

// Histogram adds the family's selected histograms. Their bucket bounds must
// agree: adding counts across different bounds would fabricate a
// distribution.
func (f Families) Histogram(name string, m Match) (h metrics.Hist, found bool, err error) {
	fam, ok := f[name]
	if !ok || fam.GetType() != dto.MetricType_HISTOGRAM {
		return h, false, nil
	}
	var bounds []float64
	for _, s := range fam.GetMetric() {
		if m != nil && !m(Labels(s)) {
			continue
		}
		sh := s.GetHistogram()
		if sh == nil {
			continue
		}
		these := make([]float64, 0, len(sh.GetBucket()))
		for _, b := range sh.GetBucket() {
			these = append(these, b.GetUpperBound())
		}
		if !found {
			bounds = these
			h.Buckets = make(map[float64]uint64, len(these))
		} else if !sameBounds(bounds, these) {
			return metrics.Hist{}, false, fmt.Errorf("%s: series have different bucket bounds", name)
		}
		found = true
		h.Count += sh.GetSampleCount()
		h.Sum += sh.GetSampleSum()
		for _, b := range sh.GetBucket() {
			h.Buckets[b.GetUpperBound()] += b.GetCumulativeCount()
		}
	}
	return h, found, nil
}

func sameBounds(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// LabelValues returns the distinct values of a label in the family, sorted.
// Series without the label are skipped.
func (f Families) LabelValues(name, label string) []string {
	fam, ok := f[name]
	if !ok {
		return nil
	}
	set := map[string]bool{}
	for _, s := range fam.GetMetric() {
		if v, ok := Labels(s)[label]; ok {
			set[v] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// queryResponse is the Prometheus HTTP API's instant-query body.
type queryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// FromQueryJSON turns a Prometheus instant-query response into Families, so a
// stored snapshot of an engine's series can stand in for its /metrics body
// (testdata/README.md, "stored"). Storage keeps no TYPE lines, so types are
// inferred: a base name with _bucket series is a histogram and owns its _sum
// and _count; any other name ending in _total is a counter; the rest are
// gauges.
func FromQueryJSON(body []byte) (Families, error) {
	var resp queryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("query status %q", resp.Status)
	}
	if resp.Data.ResultType != "vector" {
		return nil, fmt.Errorf("result type %q, want vector", resp.Data.ResultType)
	}
	byName := map[string][]string{}
	for _, r := range resp.Data.Result {
		name := r.Metric["__name__"]
		if name == "" {
			return nil, errors.New("a series has no __name__")
		}
		v, ok := r.Value[1].(string)
		if !ok {
			return nil, fmt.Errorf("%s: value is not a string", name)
		}
		byName[name] = append(byName[name], name+labelText(r.Metric)+" "+v)
	}
	histBases := map[string]bool{}
	for name := range byName {
		if base, ok := strings.CutSuffix(name, "_bucket"); ok {
			histBases[base] = true
		}
	}
	family := func(name string) (string, string) {
		for _, suf := range []string{"_bucket", "_sum", "_count"} {
			if base, ok := strings.CutSuffix(name, suf); ok && histBases[base] {
				return base, "histogram"
			}
		}
		if strings.HasSuffix(name, "_total") {
			return name, "counter"
		}
		return name, "gauge"
	}
	groups := map[string][]string{}
	types := map[string]string{}
	for name, lines := range byName {
		base, typ := family(name)
		types[base] = typ
		groups[base] = append(groups[base], lines...)
	}
	bases := make([]string, 0, len(groups))
	for b := range groups {
		bases = append(bases, b)
	}
	sort.Strings(bases)
	var buf bytes.Buffer
	for _, b := range bases {
		fmt.Fprintf(&buf, "# TYPE %s %s\n", b, types[b])
		lines := groups[b]
		sort.Strings(lines)
		for _, l := range lines {
			buf.WriteString(l + "\n")
		}
	}
	return Parse(&buf)
}

func labelText(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "__name__" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + `="` + escape(m[k]) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// escape applies the exposition format's label-value escaping.
func escape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}

