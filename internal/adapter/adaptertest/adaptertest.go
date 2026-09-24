// Package adaptertest holds assertions shared by the adapter tests.
package adaptertest

import (
	"math"
	"os"
	"strings"
	"testing"

	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
)

// LoadText parses a fixture in exposition format.
func LoadText(t *testing.T, path string) promtext.Families {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fams, err := promtext.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return fams
}

// LoadJSON parses a stored Prometheus snapshot fixture.
func LoadJSON(t *testing.T, path string) promtext.Families {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fams, err := promtext.FromQueryJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	return fams
}

// Valid fails the test if a sample breaks the schema or two samples share a
// series.
func Valid(t *testing.T, samples []metrics.Sample) {
	t.Helper()
	seen := map[string]bool{}
	for _, s := range samples {
		if err := s.Validate(); err != nil {
			t.Error(err)
			continue
		}
		if seen[s.Key()] {
			t.Errorf("%s %v: emitted twice", s.Def.Name, s.Labels)
		}
		seen[s.Key()] = true
	}
}

// Find returns the sample for def with the label values.
func Find(samples []metrics.Sample, def *metrics.Def, labels ...string) (metrics.Sample, bool) {
	for _, s := range samples {
		if s.Def.Name == def.Name && strings.Join(s.Labels, "\x00") == strings.Join(labels, "\x00") {
			return s, true
		}
	}
	return metrics.Sample{}, false
}

// Want fails the test unless the sample exists with the value.
func Want(t *testing.T, samples []metrics.Sample, def *metrics.Def, want float64, labels ...string) {
	t.Helper()
	s, ok := Find(samples, def, labels...)
	if !ok {
		t.Errorf("%s %v: absent, want %v", def.Name, labels, want)
		return
	}
	if math.Abs(s.Value-want) > 1e-9*math.Max(1, math.Abs(want)) {
		t.Errorf("%s %v: %v, want %v", def.Name, labels, s.Value, want)
	}
}

// WantWorker checks a measurement for one independent upstream worker.
func WantWorker(t *testing.T, samples []metrics.Sample, worker string, def *metrics.Def, want float64, labels ...string) {
	t.Helper()
	var selected []metrics.Sample
	for _, s := range samples {
		if s.WorkerID() == worker {
			selected = append(selected, s)
		}
	}
	Want(t, selected, def, want, labels...)
}

func Absent(t *testing.T, samples []metrics.Sample, def *metrics.Def) {
	t.Helper()
	for _, s := range samples {
		if s.Def.Name == def.Name {
			t.Errorf("%s %v: present (%v), want absent", def.Name, s.Labels, s.Value)
		}
	}
}
