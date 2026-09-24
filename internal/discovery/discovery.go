package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/engines"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/registration"
)

type Options struct {
	Candidates  func(context.Context) ([]Target, error)
	Timeout     time.Duration
	Nodes       int // 0 means unknown, not an inferred single-machine deployment.
	ExcludePort string
	MaxTargets  int
	MaxBody     int64
	ForgetAfter time.Duration
}

type Observation struct {
	Registration   registration.Registration
	Result         adapter.Result
	Err            error
	Version, State string
	ChangedAt      float64 // changes/recovery guard for rate windows and benchmark deltas
	generation     string
	seen           time.Time
}

type Manager struct {
	opts     Options
	client   *http.Client
	mu       sync.Mutex
	previous map[string]Observation
	cursor   int
}

func New(opts Options) *Manager {
	if opts.Candidates == nil {
		opts.Candidates = LocalListeners
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	if opts.MaxTargets <= 0 {
		opts.MaxTargets = 64
	}
	if opts.MaxBody <= 0 {
		opts.MaxBody = 1 << 20
	}
	if opts.ForgetAfter <= 0 {
		opts.ForgetAfter = 5 * time.Minute
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.MaxIdleConns = 8
	tr.MaxIdleConnsPerHost = 1
	tr.DisableKeepAlives = true
	return &Manager{opts: opts, previous: map[string]Observation{}, client: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("discovery redirects are disabled") }}}
}

// Observe is serialized, bounded by one collection deadline, and retains only
// health (never old samples) for disappeared endpoints until expiry. Workers
// return immutable snapshots; late results cannot modify the next collection.
func (m *Manager) Observe(parent context.Context, pinned []registration.Registration) []Observation {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(parent, m.opts.Timeout)
	defer cancel()
	now := time.Now()
	stamp := float64(now.UnixNano()) / 1e9
	blocked := map[string]bool{}
	backends := map[string]bool{}
	for _, r := range pinned {
		if t, e := LocalTarget(r.Endpoint); e == nil {
			u, _ := url.Parse(t.URL)
			blocked[u.Port()] = true
		}
		backends[r.Backend] = true
	}
	targets, listErr := m.opts.Candidates(ctx)
	unique := map[string]Target{}
	for _, t := range targets {
		safe, err := LocalTarget(t.URL)
		if err != nil {
			continue
		}
		u, _ := url.Parse(safe.URL)
		if blocked[u.Port()] || u.Port() == m.opts.ExcludePort {
			continue
		}
		safe.Generation = t.Generation
		unique[safe.URL] = safe
	}
	keys := make([]string, 0, len(unique))
	for k := range unique {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Rotate even below the cap: slow low ports must not monopolize deadlines.
	limited := len(keys) > m.opts.MaxTargets
	if len(keys) > 0 {
		start := m.cursor % len(keys)
		rot := append(append([]string{}, keys[start:]...), keys[:start]...)
		keys = rot[:min(len(rot), m.opts.MaxTargets)]
		m.cursor = (start + min(4, len(keys))) % len(unique)
	}
	type response struct {
		key string
		obs Observation
	}
	done := make(chan response, len(keys))
	work := make(chan string, len(keys))
	for _, k := range keys {
		work <- k
	}
	close(work)
	for i := 0; i < min(4, len(keys)); i++ {
		go func() {
			for k := range work {
				if ctx.Err() != nil {
					return
				}
				done <- response{k, m.probe(ctx, unique[k])}
			}
		}()
	}
	fresh := map[string]Observation{}
	for range keys {
		select {
		case r := <-done:
			fresh[r.key] = r.obs
		case <-ctx.Done():
			goto collected
		}
	}
collected:
	for key, old := range m.previous {
		u, _ := url.Parse(key)
		if blocked[u.Port()] || backends[old.Registration.Backend] || now.Sub(old.seen) > m.opts.ForgetAfter {
			delete(m.previous, key)
			continue
		}
		if _, ok := fresh[key]; !ok {
			old.Result = adapter.Result{}
			old.Err = errors.New("endpoint absent or discovery deadline exceeded")
			old.State = "unavailable"
			m.previous[key] = old
		}
	}
	for key, o := range fresh {
		old, existed := m.previous[key]
		if o.State == "unavailable" && existed {
			o.Registration = old.Registration
			o.Version = old.Version
		}
		if backends[o.Registration.Backend] {
			continue
		}
		o.ChangedAt = old.ChangedAt
		if !existed || o.Registration.Engine != old.Registration.Engine || o.Registration.Model != old.Registration.Model || o.generation != old.generation || (old.Err != nil && o.Err == nil) {
			o.ChangedAt = stamp
		}
		o.seen = now
		m.previous[key] = o
	}
	// Bound retained diagnostics as well as active probing. Evict oldest first.
	if len(m.previous) > 4*m.opts.MaxTargets {
		all := make([]string, 0, len(m.previous))
		for k := range m.previous {
			all = append(all, k)
		}
		sort.Slice(all, func(i, j int) bool { return m.previous[all[i]].seen.Before(m.previous[all[j]].seen) })
		for _, k := range all[:len(all)-4*m.opts.MaxTargets] {
			delete(m.previous, k)
		}
	}
	out := make([]Observation, 0, len(m.previous)+1)
	for _, o := range m.previous {
		out = append(out, o)
	}
	state := ""
	if len(out) == 0 {
		state = "discovering"
	}
	if limited {
		state = "candidate_limit"
	}
	if ctx.Err() != nil {
		state = "deadline"
	}
	if listErr != nil {
		state = "scope_error"
	}
	if state != "" && !backends["auto-discovery"] {
		out = append(out, Observation{Registration: registration.Registration{Engine: metrics.Unknown, Model: metrics.Unknown, Backend: "auto-discovery", Nodes: m.opts.Nodes}, Err: errors.New("discovery incomplete: " + state), State: state, ChangedAt: stamp})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Registration.Backend < out[j].Registration.Backend })
	return out
}

func (m *Manager) probe(parent context.Context, t Target) Observation {
	ctx, cancel := context.WithTimeout(parent, min(m.opts.Timeout, time.Second))
	defer cancel()
	hash := sha256.Sum256([]byte(t.URL))
	o := Observation{Registration: registration.Registration{Version: 1, Endpoint: t.URL, Engine: metrics.Unknown, Model: metrics.Unknown, Backend: fmt.Sprintf("auto-%x", hash[:8]), RunID: "auto", Nodes: m.opts.Nodes}, generation: t.Generation, State: "unavailable"}
	f, err := adapter.FetchMetrics(ctx, m.client, t.URL, m.opts.MaxBody)
	if err != nil {
		o.Err = errors.New("telemetry request failed")
		return o
	}
	d, err := engines.Detect(f)
	if d.Engine != "" {
		o.Registration.Engine = d.Engine
	}
	o.Version = d.Version
	if err != nil {
		o.Err = err
		o.State = "unknown"
		if errors.Is(err, engines.ErrAmbiguous) {
			o.State = "ambiguous"
		}
		if errors.Is(err, engines.ErrUnsupported) {
			o.State = "unsupported"
		}
		return o
	}
	model := d.Model
	if model == "" {
		// Bracket the measurement with metadata and verify the second snapshot's
		// signature, so a port changing engines during lookup is never mislabeled.
		before, e := m.model(ctx, t.URL)
		if e != nil {
			o.Err = e
			o.State = "identity_missing"
			return o
		}
		f, e = adapter.FetchMetrics(ctx, m.client, t.URL, m.opts.MaxBody)
		if e != nil {
			o.Err = errors.New("telemetry changed during identity lookup")
			return o
		}
		again, e := engines.Detect(f)
		if e != nil || again.Engine != d.Engine {
			o.Err = errors.New("engine changed during identity lookup")
			o.State = "ambiguous"
			return o
		}
		after, e := m.model(ctx, t.URL)
		if e != nil || before != after || again.Model != "" && again.Model != before {
			o.Err = errors.New("model changed during identity lookup")
			o.State = "ambiguous"
			return o
		}
		model = before
	}
	if model == "" || len(model) > 512 || strings.ContainsAny(model, "\x00\r\n\t ") {
		o.Err = errors.New("model identity missing or invalid")
		o.State = "identity_missing"
		return o
	}
	o.Registration.Model = model
	if v, ok := f.Sum("process_start_time_seconds", nil); ok {
		o.generation += "/" + fmt.Sprint(v)
	}
	o.Result, o.Err = d.Map(f, model)
	o.State = "ready"
	if o.Err != nil {
		o.Result = adapter.Result{}
		o.State = "unavailable"
	}
	return o
}

func (m *Manager) model(ctx context.Context, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/v1/models", nil)
	if err != nil {
		return "", err
	}
	r, err := m.client.Do(req)
	if err != nil {
		return "", errors.New("model metadata unavailable")
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return "", errors.New("model metadata unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, m.opts.MaxBody+1))
	if err != nil || int64(len(body)) > m.opts.MaxBody {
		return "", errors.New("invalid model metadata")
	}
	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &data) != nil || len(data.Data) != 1 || data.Data[0].ID == "" {
		return "", errors.New("model metadata must identify exactly one model")
	}
	return data.Data[0].ID, nil
}
