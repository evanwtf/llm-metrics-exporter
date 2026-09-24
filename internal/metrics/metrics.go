// Package metrics is the canonical schema. Every llm_* name the exporter
// emits is defined here, and nowhere else.
package metrics

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Kind is the Prometheus type of a series.
type Kind int

const (
	Counter Kind = iota
	Gauge
	Histogram
)

// IdentityLabels come from the registration file, in this order, and are on
// every per-arm series.
var IdentityLabels = []string{"engine", "model", "backend", "host", "nodes"}

// Def defines one canonical series.
type Def struct {
	Name string
	Help string
	Kind Kind
	// Labels are the labels after the identity labels.
	Labels []string
	// Identity is true when the identity labels come first.
	Identity bool
}

// Phase label values. Prefill and decode are never blended (AGENTS.md).
const (
	Prefill = "prefill"
	Decode  = "decode"
)

// Series that adapters emit. docs/design.md, "Metric schema", is the prose
// version of this block; docs/adapters.md says which engine fills which.
var (
	Tokens = Def{
		Name: "llm_tokens_total", Kind: Counter, Identity: true, Labels: []string{"phase"},
		Help: "Tokens processed. phase=prefill counts prompt tokens the engine computed, excluding cache hits; phase=decode counts generated tokens.",
	}
	PromptCachedTokens = Def{
		Name: "llm_prompt_cached_tokens_total", Kind: Counter, Identity: true,
		Help: "Prompt tokens reused from a cache instead of computed.",
	}
	RequestPhaseSeconds = Def{
		Name: "llm_request_phase_seconds_total", Kind: Counter, Identity: true, Labels: []string{"phase"},
		Help: "Request clock: each request's own phase interval, summed over requests. Overlapping requests are all counted.",
	}
	EnginePhaseSeconds = Def{
		Name: "llm_engine_phase_seconds_total", Kind: Counter, Identity: true, Labels: []string{"phase"},
		Help: "Engine clock: wall time the engine spent in the phase. Overlap is counted once.",
	}
	Requests = Def{
		Name: "llm_requests_total", Kind: Counter, Identity: true, Labels: []string{"status"},
		Help: "Finished requests, by the engine's finish reason.",
	}
	RequestsRunning = Def{
		Name: "llm_requests_running", Kind: Gauge, Identity: true,
		Help: "Requests the engine is processing now.",
	}
	KVCacheUsage = Def{
		Name: "llm_kv_cache_usage_ratio", Kind: Gauge, Identity: true,
		Help: "KV-cache usage, 0 to 1.",
	}
	TimeToFirstToken = Def{
		Name: "llm_time_to_first_token_seconds", Kind: Histogram, Identity: true,
		Help: "Time to first token, in the engine's own buckets.",
	}
	SpecDraftTokens = Def{
		Name: "llm_spec_draft_tokens_total", Kind: Counter, Identity: true,
		Help: "Speculative draft tokens proposed. Never includes the free first token.",
	}
	SpecAcceptedTokens = Def{
		Name: "llm_spec_accepted_tokens_total", Kind: Counter, Identity: true,
		Help: "Speculative draft tokens the target model kept.",
	}
	SpecVerifySteps = Def{
		Name: "llm_spec_verify_steps_total", Kind: Counter, Identity: true,
		Help: "Speculative verification steps that proposed at least one token.",
	}
)

// Series that only the exporter emits.
var (
	EngineUp = Def{
		Name: "llm_engine_up", Kind: Gauge, Identity: true,
		Help: "1 if the exporter got telemetry from this identified engine on its last attempt, else 0.",
	}
	RegistrationMismatch = Def{
		Name: "llm_registration_mismatch", Kind: Gauge, Identity: true,
		Help: "1 if the engine serves no model named served_model in the registration.",
	}
	ArmInfo = Def{
		Name: "llm_arm_info", Kind: Gauge, Identity: true,
		Labels: []string{"issue", "adapter_version", "exporter_version"},
		Help:   "Registration and build metadata for this arm. adapter_version changes when the adapter's mapping changes. Always 1.",
	}
	ScrapeErrors = Def{
		Name: "llm_exporter_scrape_errors_total", Kind: Counter, Identity: true,
		Help: "Failed collection attempts for this arm since its registration appeared.",
	}
	LastSuccess = Def{
		Name: "llm_exporter_last_success_timestamp_seconds", Kind: Gauge, Identity: true,
		Help: "Unix time of the last collection that got telemetry. 0 if none has.",
	}
	// RegistrationInvalid has no identity label set to borrow: the file did
	// not validate. engine and model are the file's own values, or "unknown"
	// when it has none, so the series still has both labels without a guess.
	RegistrationInvalid = Def{
		Name: "llm_registration_invalid", Kind: Gauge,
		Labels: []string{"engine", "model", "host", "file"},
		Help:   "1 for each registration file that did not parse or validate. Its arm is not measured.",
	}
)

var BacklogBytes = Def{Name: "llm_telemetry_backlog_bytes", Kind: Gauge, Identity: true, Help: "Unread telemetry bytes. Nonzero means counters are withheld until catch-up."}
var MetricAvailable = Def{Name: "llm_metric_available", Kind: Gauge, Identity: true, Labels: []string{"metric", "phase"}, Help: "1 when this collection provides the named measurement, including measured zero; 0 when unavailable."}
var DiscoveryStatus = Def{Name: "llm_discovery_status", Kind: Gauge, Identity: true, Labels: []string{"state"}, Help: "Current automatic discovery state. Always 1 for the reported state."}
var DiscoveryChanged = Def{Name: "llm_discovery_changed_timestamp_seconds", Kind: Gauge, Identity: true, Help: "Unix time of last observed identity, listener generation, or recovery transition. Exclude rate windows crossing this boundary."}

// Unknown is the label value for an engine or model a file does not state.
const Unknown = "unknown"

// AdapterDefs are the series an adapter may emit.
func AdapterDefs() []Def {
	return []Def{
		Tokens, PromptCachedTokens, RequestPhaseSeconds, EnginePhaseSeconds,
		Requests, RequestsRunning, KVCacheUsage, TimeToFirstToken,
		SpecDraftTokens, SpecAcceptedTokens, SpecVerifySteps,
	}
}

// ExporterDefs are the series only the exporter emits.
func ExporterDefs() []Def {
	return []Def{
		BacklogBytes, MetricAvailable, DiscoveryStatus, DiscoveryChanged,
		EngineUp, RegistrationMismatch, ArmInfo, ScrapeErrors, LastSuccess,
		RegistrationInvalid,
	}
}

// AllLabels is the full label list of a series: identity labels, if any,
// then Labels.
func (d Def) AllLabels() []string {
	if !d.Identity {
		return d.Labels
	}
	labels := append(append([]string{}, IdentityLabels...), d.Labels...)
	if adapterNames[d.Name] {
		labels = append(labels, "worker")
	}
	return labels
}

// Hist is a histogram snapshot with cumulative buckets keyed by upper bound.
type Hist struct {
	Count   uint64
	Sum     float64
	Buckets map[float64]uint64
}

// Sample is one value an adapter reports. Labels are the values of
// Def.Labels, in order; the collector adds the identity labels.
type Sample struct {
	Worker string
	Def    *Def
	Labels []string
	Value  float64
	Hist   *Hist
}

var adapterNames = func() map[string]bool {
	m := map[string]bool{}
	for _, d := range AdapterDefs() {
		m[d.Name] = true
	}
	return m
}()

// Validate refuses a sample that does not fit the canonical schema.
func (s Sample) Validate() error {
	if s.Def == nil {
		return errors.New("sample has no definition")
	}
	name := s.Def.Name
	if !adapterNames[name] {
		return fmt.Errorf("%s: not a series an adapter may emit", name)
	}
	if len(s.Labels) != len(s.Def.Labels) {
		return fmt.Errorf("%s: %d label values for labels %v", name, len(s.Labels), s.Def.Labels)
	}
	for i, l := range s.Def.Labels {
		v := s.Labels[i]
		if v == "" {
			return fmt.Errorf("%s: empty %s", name, l)
		}
		if l == "phase" && v != Prefill && v != Decode {
			return fmt.Errorf("%s: phase %q is not prefill or decode", name, v)
		}
	}
	switch s.Def.Kind {
	case Histogram:
		return s.validateHist()
	case Counter:
		if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) || s.Value < 0 {
			return fmt.Errorf("%s: counter value %v", name, s.Value)
		}
	case Gauge:
		if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
			return fmt.Errorf("%s: gauge value %v", name, s.Value)
		}
		if strings.HasSuffix(name, "_ratio") && (s.Value < 0 || s.Value > 1) {
			return fmt.Errorf("%s: ratio %v outside 0..1", name, s.Value)
		}
	}
	return nil
}

func (s Sample) validateHist() error {
	h := s.Hist
	if h == nil {
		return fmt.Errorf("%s: histogram sample without a histogram", s.Def.Name)
	}
	bounds := make([]float64, 0, len(h.Buckets))
	for b := range h.Buckets {
		bounds = append(bounds, b)
	}
	sort.Float64s(bounds)
	var prev uint64
	for _, b := range bounds {
		c := h.Buckets[b]
		if c < prev {
			return fmt.Errorf("%s: bucket le=%v count %d below the previous %d", s.Def.Name, b, c, prev)
		}
		if c > h.Count {
			return fmt.Errorf("%s: bucket le=%v count %d above the total %d", s.Def.Name, b, c, h.Count)
		}
		prev = c
	}
	return nil
}

// Key identifies the series a sample belongs to within one arm.
func (s Sample) Key() string {
	return s.Def.Name + "\x00" + strings.Join(s.Labels, "\x00") + "\x00" + s.WorkerID()
}

func (s Sample) WorkerID() string {
	if s.Worker == "" {
		return "default"
	}
	return s.Worker
}
