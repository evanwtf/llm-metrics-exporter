// Package adapter defines how an engine's own telemetry becomes canonical
// samples. Each engine has a package below this one.
package adapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
)

// Adapter reads one registered engine. Pull adapters do all their work in
// Collect. Stateful adapters keep running totals between calls, and Start
// opens that state.
type Adapter interface {
	Start(ctx context.Context) error
	// Collect returns canonical samples. An error means no telemetry was
	// obtained on this attempt, and the arm exports llm_engine_up 0.
	Collect(ctx context.Context) (Result, error)
	Close() error
}

// Config is what an adapter knows about its arm, from the registration.
type Config struct {
	Endpoint    string
	ServedModel string
	LogPath     string
	TracePath   string
	Client      *http.Client
	// MaxBody caps how much of a /metrics body is read.
	MaxBody int64
	// MaxRead caps how many log bytes one Collect reads.
	MaxRead int64
}

// Result is one collection.
type Result struct {
	Samples []metrics.Sample
}

// Info describes a compiled-in adapter.
type Info struct {
	Engine string
	// Version changes when the adapter's mapping changes, so a change in
	// meaning is visible in the data (llm_exporter_adapter_info).
	Version string
	New     func(Config) Adapter
}

// ErrModelMismatch is the sentinel for a served_model the engine does not serve.
var ErrModelMismatch = errors.New("engine does not serve the registered served_model")

// MismatchError says which models the engine did report.
type MismatchError struct {
	Want     string
	Reported []string
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("served_model %q not found; engine reports %s", e.Want, strings.Join(e.Reported, ", "))
}

// Unwrap lets errors.Is match ErrModelMismatch.
func (e *MismatchError) Unwrap() error { return ErrModelMismatch }

// SelectModel returns the Match for the registered served_model, using label
// on metric to find the names the engine reports. With no served_model, or an
// engine that labels no model, it returns nil (select everything): there is
// nothing to validate against.
func SelectModel(fams promtext.Families, metric, label, served string) (promtext.Match, error) {
	if served == "" {
		return nil, nil
	}
	reported := fams.LabelValues(metric, label)
	if len(reported) == 0 {
		return nil, nil
	}
	if !slices.Contains(reported, served) {
		return nil, &MismatchError{Want: served, Reported: reported}
	}
	return promtext.LabelIs(label, served), nil
}

// FetchMetrics GETs <endpoint>/metrics and parses it. It reads at most
// maxBody bytes; a longer body is an error rather than a truncated parse.
func FetchMetrics(ctx context.Context, client *http.Client, endpoint string, maxBody int64) (promtext.Families, error) {
	url := strings.TrimSuffix(endpoint, "/") + "/metrics"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBody {
		return nil, fmt.Errorf("GET %s: body larger than %d bytes", url, maxBody)
	}
	return promtext.Parse(strings.NewReader(string(body)))
}

// Probe GETs <endpoint><path> and succeeds on any 2xx. Stateful adapters use
// it so a log that is still readable after its engine died does not read as up.
func Probe(ctx context.Context, client *http.Client, endpoint, path string) error {
	url := strings.TrimSuffix(endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return nil
}

// Builder turns engine series into canonical samples. Every read is scoped by
// the model Match given to NewBuilder. A source series that is absent adds no
// sample: absence is "not measured", never zero.
type Builder struct {
	fams    promtext.Families
	model   promtext.Match
	samples []metrics.Sample
	errs    []error
}

// NewBuilder starts a Builder over fams, selecting series with model.
func NewBuilder(fams promtext.Families, model promtext.Match) *Builder {
	return &Builder{fams: fams, model: model}
}

// Value adds def from the sum of the selected series of source.
func (b *Builder) Value(def *metrics.Def, labels []string, source string, m promtext.Match) {
	if v, ok := b.fams.Sum(source, promtext.AllOf(b.model, m)); ok {
		b.samples = append(b.samples, metrics.Sample{Def: def, Labels: labels, Value: v})
	}
}

// HistogramSum adds def from the _sum of the selected histograms of source.
func (b *Builder) HistogramSum(def *metrics.Def, labels []string, source string, m promtext.Match) {
	h, ok, err := b.fams.Histogram(source, promtext.AllOf(b.model, m))
	if err != nil {
		b.errs = append(b.errs, err)
		return
	}
	if ok {
		b.samples = append(b.samples, metrics.Sample{Def: def, Labels: labels, Value: h.Sum})
	}
}

// Histogram adds def as the sum of the selected histograms of source.
func (b *Builder) Histogram(def *metrics.Def, source string, m promtext.Match) {
	h, ok, err := b.fams.Histogram(source, promtext.AllOf(b.model, m))
	if err != nil {
		b.errs = append(b.errs, err)
		return
	}
	if ok {
		b.samples = append(b.samples, metrics.Sample{Def: def, Hist: &h})
	}
}

// Add appends a sample built elsewhere.
func (b *Builder) Add(s metrics.Sample) { b.samples = append(b.samples, s) }

// Samples returns the samples added so far.
func (b *Builder) Samples() []metrics.Sample { return b.samples }

// Err returns any error met while building.
func (b *Builder) Err() error { return errors.Join(b.errs...) }

// Pull is an Adapter that reads /metrics and maps it with Map.
type Pull struct {
	Config
	// Map turns the engine's families into samples. It returns an error when
	// the body is not this engine's, so a wrong port reads as down.
	Map func(fams promtext.Families, servedModel string) (Result, error)
}

// Start does nothing: a pull adapter keeps no state.
func (p *Pull) Start(context.Context) error { return nil }

// Collect fetches /metrics and maps it.
func (p *Pull) Collect(ctx context.Context) (Result, error) {
	fams, err := FetchMetrics(ctx, p.Client, p.Endpoint, p.MaxBody)
	if err != nil {
		return Result{}, err
	}
	return p.Map(fams, p.ServedModel)
}

// Close does nothing.
func (p *Pull) Close() error { return nil }
