package adapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
)

func serve(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestFetchMetrics(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("x_total 3\n"))
	})
	fams, err := FetchMetrics(context.Background(), http.DefaultClient, url, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := fams.Sum("x_total", nil); !ok || v != 3 {
		t.Fatalf("got %v %v", v, ok)
	}
}

func TestFetchMetricsErrors(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"404": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		// llama.cpp without --metrics answers 501.
		"501":     func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(501) },
		"garbage": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{not metrics")) },
		"too big": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(strings.Repeat("# comment\n", 1000)))
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := FetchMetrics(context.Background(), http.DefaultClient, serve(t, h), 1000); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

// A slow engine must not block the exporter (AGENTS.md, Footprint).
func TestFetchMetricsHonorsTheDeadline(t *testing.T) {
	release := make(chan struct{})
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := FetchMetrics(ctx, http.DefaultClient, url, 1<<20); err == nil {
		t.Fatal("no error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("did not return at the deadline")
	}
}

func TestSelectModel(t *testing.T) {
	fams, err := promtext.Parse(strings.NewReader(
		"a_total{model_name=\"x\"} 1\na_total{model_name=\"y\"} 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := SelectModel(fams, "a_total", "model_name", "")
	if !errors.Is(err, ErrModelMismatch) || m != nil {
		t.Fatalf("no served_model: m=%v err=%v", m != nil, err)
	}
	m, err = SelectModel(fams, "a_total", "model_name", "y")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := fams.Sum("a_total", m); v != 2 {
		t.Fatalf("selected %v", v)
	}
	_, err = SelectModel(fams, "a_total", "model_name", "z")
	var mm *MismatchError
	if !errors.As(err, &mm) || !errors.Is(err, ErrModelMismatch) {
		t.Fatalf("err %v, want a MismatchError", err)
	}
	if strings.Join(mm.Reported, ",") != "x,y" {
		t.Fatalf("reported %v", mm.Reported)
	}
}

// An engine that labels no model cannot be validated, and that is not a
// mismatch.
func TestSelectModelWithoutALabel(t *testing.T) {
	fams, _ := promtext.Parse(strings.NewReader("a_total 1\n"))
	m, err := SelectModel(fams, "a_total", "model", "z")
	if err != nil || m != nil {
		t.Fatalf("m=%v err=%v", m != nil, err)
	}
}

func TestSingleModelNeedsNoExplicitSelection(t *testing.T) {
	fams, _ := promtext.Parse(strings.NewReader("a_total{model_name=\"x\"} 3\n"))
	m, err := SelectModel(fams, "a_total", "model_name", "")
	if err != nil {
		t.Fatal(err)
	}
	if value, _ := fams.Sum("a_total", m); value != 3 {
		t.Fatal("single-model alias not supported")
	}
}

func TestBuilder(t *testing.T) {
	fams, _ := promtext.Parse(strings.NewReader(`# TYPE h histogram
h_bucket{le="1"} 1
h_bucket{le="+Inf"} 2
h_sum 3.5
h_count 2
c_total{k="a"} 4
c_total{k="b"} 5
`))
	b := NewBuilder(fams, nil)
	b.Value(&metrics.Tokens, []string{metrics.Decode}, "c_total", nil)
	b.Value(&metrics.PromptCachedTokens, nil, "absent_total", nil) // skipped, not zero
	b.HistogramSum(&metrics.RequestPhaseSeconds, []string{metrics.Prefill}, "h", nil)
	b.Histogram(&metrics.TimeToFirstToken, "h", nil)
	b.Value(&metrics.Requests, []string{"stop"}, "c_total", promtext.LabelIs("k", "a"))
	got := b.Samples()
	if len(got) != 4 {
		t.Fatalf("got %d samples: %+v", len(got), got)
	}
	if got[0].Value != 9 || got[1].Value != 3.5 || got[2].Hist.Count != 2 || got[3].Value != 4 {
		t.Fatalf("got %+v", got)
	}
	if err := b.Err(); err != nil {
		t.Fatal(err)
	}
}
