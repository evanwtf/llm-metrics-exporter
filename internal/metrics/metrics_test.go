package metrics

import (
	"math"
	"strings"
	"testing"
)

func TestAdapterDefsAreUniqueAndPrefixed(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range AdapterDefs() {
		if !strings.HasPrefix(d.Name, "llm_") {
			t.Errorf("%s: canonical names start with llm_", d.Name)
		}
		if seen[d.Name] {
			t.Errorf("%s: defined twice", d.Name)
		}
		seen[d.Name] = true
		if d.Help == "" {
			t.Errorf("%s: no help text", d.Name)
		}
		if d.Kind == Counter && !strings.HasSuffix(d.Name, "_total") {
			t.Errorf("%s: a counter name ends in _total", d.Name)
		}
	}
}

// v1 exposes no throughput gauge: rates come from counters (AGENTS.md).
func TestNoThroughputGauge(t *testing.T) {
	for _, d := range append(AdapterDefs(), ExporterDefs()...) {
		if strings.Contains(d.Name, "per_second") {
			t.Errorf("%s: v1 exposes no tok/s gauge", d.Name)
		}
	}
}

// The two clocks must be different names, so no sum() can merge them.
func TestTwoClocksAreDistinctNames(t *testing.T) {
	if RequestPhaseSeconds.Name == EnginePhaseSeconds.Name {
		t.Fatal("request clock and engine clock share a name")
	}
}

func TestValidateAcceptsAWellFormedSample(t *testing.T) {
	s := Sample{Def: &Tokens, Labels: []string{"decode"}, Value: 12}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	unknown := Def{Name: "llm_made_up_total", Kind: Counter}
	cases := map[string]Sample{
		"unknown metric":     {Def: &unknown, Value: 1},
		"nil def":            {Value: 1},
		"missing label":      {Def: &Tokens, Value: 1},
		"extra label":        {Def: &PromptCachedTokens, Labels: []string{"x"}, Value: 1},
		"bad phase":          {Def: &Tokens, Labels: []string{"total"}, Value: 1},
		"empty status":       {Def: &Requests, Labels: []string{""}, Value: 1},
		"negative counter":   {Def: &Tokens, Labels: []string{"decode"}, Value: -1},
		"NaN counter":        {Def: &Tokens, Labels: []string{"decode"}, Value: math.NaN()},
		"exporter-only def":  {Def: &EngineUp, Value: 1},
		"histogram no count": {Def: &TimeToFirstToken, Hist: nil},
		"ratio above one":    {Def: &KVCacheUsage, Value: 1.5},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if err := s.Validate(); err == nil {
				t.Fatalf("accepted %+v", s)
			}
		})
	}
}

func TestValidateHistogram(t *testing.T) {
	good := Sample{Def: &TimeToFirstToken, Hist: &Hist{
		Count: 3, Sum: 1.5, Buckets: map[float64]uint64{0.1: 1, 1: 3},
	}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	// Cumulative buckets never decrease, and none exceeds the count.
	bad := Sample{Def: &TimeToFirstToken, Hist: &Hist{
		Count: 3, Sum: 1.5, Buckets: map[float64]uint64{0.1: 2, 1: 1},
	}}
	if err := bad.Validate(); err == nil {
		t.Fatal("accepted decreasing buckets")
	}
	over := Sample{Def: &TimeToFirstToken, Hist: &Hist{
		Count: 1, Sum: 1.5, Buckets: map[float64]uint64{1: 2},
	}}
	if err := over.Validate(); err == nil {
		t.Fatal("accepted a bucket above the count")
	}
}

func TestKeyDistinguishesLabelValues(t *testing.T) {
	a := Sample{Def: &Tokens, Labels: []string{"decode"}}
	b := Sample{Def: &Tokens, Labels: []string{"prefill"}}
	if a.Key() == b.Key() {
		t.Fatal("different phases share a key")
	}
}
