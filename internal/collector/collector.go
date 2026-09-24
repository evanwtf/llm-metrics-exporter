// Package collector is the prometheus.Collector at the center of the
// exporter. On every scrape it re-reads the registration directory, keeps one
// adapter per registered run, collects them in parallel under a timeout, and
// emits canonical series with the identity labels from the registration.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/discovery"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/registration"
)

// Options configure a Collector.
type Options struct {
	Dir             string        // the registration directory
	Host            string        // the host label
	Timeout         time.Duration // per arm, per scrape
	Client          *http.Client
	MaxBody         int64 // /metrics body cap
	MaxRead         int64 // log bytes per scrape
	Adapters        []adapter.Info
	ExporterVersion string
	Logger          *slog.Logger
	Discovery       *discovery.Manager
}

// Collector implements prometheus.Collector.
type Collector struct {
	opts     Options
	adapters map[string]adapter.Info
	descs    map[string]*prometheus.Desc
	log      *slog.Logger

	mu       sync.Mutex // one scrape at a time: log adapters are not re-entrant
	arms     map[string]*arm
	retired  map[string]*arm
	autoArms map[string]*arm
}

type arm struct {
	life        sync.Mutex
	busy        bool
	closed      bool
	reg         registration.Registration
	info        adapter.Info
	ad          adapter.Adapter // nil when the engine has no adapter
	errors      float64
	lastSuccess float64
	lastErr     string // to log on change, not on every scrape
}

// New returns a Collector. Call Close to release the adapters.
func New(opts Options) *Collector {
	c := &Collector{
		opts:     opts,
		adapters: map[string]adapter.Info{},
		descs:    map[string]*prometheus.Desc{},
		log:      opts.Logger,
		arms:     map[string]*arm{},
		retired:  map[string]*arm{},
		autoArms: map[string]*arm{},
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	for _, a := range opts.Adapters {
		c.adapters[a.Engine] = a
	}
	for _, d := range append(metrics.AdapterDefs(), metrics.ExporterDefs()...) {
		c.descs[d.Name] = prometheus.NewDesc(d.Name, d.Help, d.AllLabels(), nil)
	}
	return c
}

// Describe sends nothing: the series depend on what is registered, so this
// is an unchecked collector.
func (c *Collector) Describe(chan<- *prometheus.Desc) {}

// Close releases every adapter.
func (c *Collector) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, a := range c.arms {
		c.closeArm(a)
		delete(c.arms, k)
	}
}

// Collect reads the registrations and every arm.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	valid, invalid := registration.LoadDir(c.opts.Dir)
	for _, inv := range invalid {
		c.log.Warn("invalid registration", "file", inv.File, "err", inv.Err)
		c.emit(ch, &metrics.RegistrationInvalid, 1, inv.Engine, inv.Model, c.opts.Host, inv.File)
	}
	c.sync(valid)

	type outcome struct {
		res adapter.Result
		err error
	}
	results := make(map[string]outcome, len(c.arms))
	var wg sync.WaitGroup
	var rmu sync.Mutex
	var observed []discovery.Observation
	if c.opts.Discovery != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			observed = c.opts.Discovery.Observe(context.Background(), valid)
		}()
	}
	for key, a := range c.arms {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := c.collectArm(a)
			rmu.Lock()
			results[key] = outcome{res, err}
			rmu.Unlock()
		}()
	}
	wg.Wait()

	now := float64(time.Now().UnixNano()) / 1e9
	for key, a := range c.arms {
		out := results[key]
		c.report(ch, a, out.res, out.err, now)
	}
	seenAuto := map[string]bool{}
	for _, o := range observed {
		r := o.Registration
		seenAuto[r.Backend] = true
		a := c.autoArms[r.Backend]
		if a == nil || a.reg != r {
			a = &arm{reg: r, info: adapter.Info{Version: o.Version}}
			c.autoArms[r.Backend] = a
		}
		c.report(ch, a, o.Result, o.Err, now)
		id := identity(r, c.opts.Host)
		c.emit(ch, &metrics.DiscoveryStatus, 1, append(id, o.State)...)
		c.emit(ch, &metrics.DiscoveryChanged, o.ChangedAt, id...)
	}
	for key := range c.autoArms {
		if !seenAuto[key] {
			delete(c.autoArms, key)
		}
	}
}

// sync makes the arms match the registrations: a new run_id, or any changed
// field, restarts the adapter; a registration that is gone closes it.
func (c *Collector) sync(valid []registration.Registration) {
	for key, old := range c.retired {
		old.life.Lock()
		busy := old.busy
		old.life.Unlock()
		if !busy {
			delete(c.retired, key)
		}
	}
	seen := map[string]bool{}
	for _, r := range valid {
		seen[r.Backend] = true
		if old := c.retired[r.Backend]; old != nil {
			old.life.Lock()
			busy := old.busy
			old.life.Unlock()
			if busy {
				c.arms[r.Backend] = &arm{reg: r, closed: true}
				continue
			}
			delete(c.retired, r.Backend)
		}
		if old, ok := c.arms[r.Backend]; ok {
			old.life.Lock()
			closed := old.closed
			old.life.Unlock()
			if old.reg == r && !closed {
				continue
			}
			c.closeArm(old)
			old.life.Lock()
			busy := old.busy
			old.life.Unlock()
			if busy {
				c.arms[r.Backend] = &arm{reg: r, closed: true}
				continue
			}
		}
		a := &arm{reg: r}
		if info, ok := c.adapters[r.Engine]; ok {
			a.info = info
			a.ad = info.New(adapter.Config{
				Endpoint: r.Endpoint, ServedModel: r.ServedModel,
				LogPath: r.LogPath, TracePath: r.TracePath,
				Client: c.opts.Client, MaxBody: c.opts.MaxBody, MaxRead: c.opts.MaxRead,
			})
			ctx, cancel := context.WithTimeout(context.Background(), c.opts.Timeout)
			if err := a.ad.Start(ctx); err != nil {
				c.log.Warn("adapter did not start", "backend", r.Backend, "err", err)
				a.ad = nil
			}
			cancel()
		}
		c.log.Info("arm registered", "backend", r.Backend, "engine", r.Engine,
			"model", r.Model, "run_id", r.RunID, "adapter", a.ad != nil)
		c.arms[r.Backend] = a
	}
	for k, a := range c.arms {
		if !seen[k] {
			c.log.Info("arm deregistered", "backend", k, "run_id", a.reg.RunID)
			c.closeArm(a)
			delete(c.arms, k)
		}
	}
}

func (c *Collector) closeArm(a *arm) {
	a.life.Lock()
	defer a.life.Unlock()
	if a.closed {
		return
	}
	a.closed = true
	if a.busy {
		c.retired[a.reg.Backend] = a
		return
	}
	if a.ad != nil {
		if err := a.ad.Close(); err != nil {
			c.log.Warn("adapter close", "backend", a.reg.Backend, "err", err)
		}
	}
}

var errNoAdapter = errors.New("this build has no adapter for the engine")

func (c *Collector) collectArm(a *arm) (adapter.Result, error) {
	a.life.Lock()
	if a.busy || a.closed {
		a.life.Unlock()
		return adapter.Result{}, errors.New("previous collection still running or adapter retiring")
	}
	if a.ad == nil {
		a.life.Unlock()
		return adapter.Result{}, errNoAdapter
	}
	a.busy = true
	a.life.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), c.opts.Timeout)
	defer cancel()
	type ret struct {
		res adapter.Result
		err error
	}
	done := make(chan ret, 1)
	go func() {
		res, err := a.ad.Collect(ctx)
		a.life.Lock()
		if a.closed {
			_ = a.ad.Close()
		}
		a.busy = false
		a.life.Unlock()
		done <- ret{res, err}
	}()
	select {
	case r := <-done:
		return r.res, r.err
	case <-ctx.Done():
		// An adapter that ignores its context must not hold the scrape.
		return adapter.Result{}, fmt.Errorf("collect: %w", ctx.Err())
	}
}

func identity(r registration.Registration, host string) []string {
	nodes := strconv.Itoa(r.Nodes)
	if r.Nodes == 0 {
		nodes = metrics.Unknown
	}
	return []string{r.Engine, r.Model, r.Backend, host, nodes}
}

func (c *Collector) report(ch chan<- prometheus.Metric, a *arm, res adapter.Result, err error, now float64) {
	r := a.reg
	id := identity(r, c.opts.Host)
	with := func(extra ...string) []string { return append(append([]string{}, id...), extra...) }

	up, mismatch := 1.0, 0.0
	if err != nil {
		up = 0
		a.errors++
		if errors.Is(err, adapter.ErrModelMismatch) {
			mismatch = 1
		}
		if msg := err.Error(); msg != a.lastErr {
			c.log.Warn("arm not readable", "backend", r.Backend, "engine", r.Engine, "err", msg)
			a.lastErr = msg
		}
	} else if res.BacklogBytes == 0 {
		a.lastSuccess = now
		if a.lastErr != "" {
			c.log.Info("arm readable again", "backend", r.Backend)
			a.lastErr = ""
		}
	}

	issue := ""
	if r.Issue > 0 {
		issue = strconv.Itoa(r.Issue)
	}
	version := a.info.Version
	if version == "" {
		version = "none"
	}
	c.emit(ch, &metrics.EngineUp, up, id...)
	c.emit(ch, &metrics.RegistrationMismatch, mismatch, id...)
	c.emit(ch, &metrics.ArmInfo, 1, with(issue, version, c.opts.ExporterVersion)...)
	c.emit(ch, &metrics.ScrapeErrors, a.errors, id...)
	c.emit(ch, &metrics.LastSuccess, a.lastSuccess, id...)
	c.emit(ch, &metrics.BacklogBytes, float64(res.BacklogBytes), id...)
	for _, measurement := range []struct{ name, phase string }{
		{metrics.Tokens.Name, metrics.Prefill}, {metrics.Tokens.Name, metrics.Decode},
		{metrics.PromptCachedTokens.Name, "none"},
	} {
		available := 0.0
		if err == nil && res.BacklogBytes == 0 {
			for _, s := range res.Samples {
				if s.Validate() == nil && s.Def.Name == measurement.name && (measurement.phase == "none" || len(s.Labels) > 0 && s.Labels[0] == measurement.phase) {
					available = 1
				}
			}
		}
		c.emit(ch, &metrics.MetricAvailable, available, with(measurement.name, measurement.phase)...)
	}
	if err != nil {
		return
	}

	seen := map[string]bool{}
	for _, s := range res.Samples {
		if verr := s.Validate(); verr != nil {
			c.log.Error("adapter sample dropped", "backend", r.Backend, "err", verr)
			continue
		}
		if seen[s.Key()] {
			c.log.Error("duplicate adapter sample dropped", "backend", r.Backend, "metric", s.Def.Name, "labels", s.Labels)
			continue
		}
		seen[s.Key()] = true
		labels := with(s.Labels...)
		labels = append(labels, s.WorkerID())
		if s.Def.Kind == metrics.Histogram {
			buckets := make(map[float64]uint64, len(s.Hist.Buckets))
			for b, n := range s.Hist.Buckets {
				if !math.IsInf(b, 1) { // client_golang adds +Inf itself
					buckets[b] = n
				}
			}
			m, herr := prometheus.NewConstHistogram(c.descs[s.Def.Name], s.Hist.Count, s.Hist.Sum, buckets, labels...)
			c.send(ch, m, herr)
			continue
		}
		c.emit(ch, s.Def, s.Value, labels...)
	}
}

func (c *Collector) emit(ch chan<- prometheus.Metric, d *metrics.Def, v float64, labels ...string) {
	t := prometheus.GaugeValue
	if d.Kind == metrics.Counter {
		t = prometheus.CounterValue
	}
	m, err := prometheus.NewConstMetric(c.descs[d.Name], t, v, labels...)
	c.send(ch, m, err)
}

func (c *Collector) send(ch chan<- prometheus.Metric, m prometheus.Metric, err error) {
	if err != nil {
		c.log.Error("series not built", "err", err)
		return
	}
	ch <- m
}
