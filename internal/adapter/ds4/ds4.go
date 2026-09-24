// Package ds4 reads ds4's speculative-decoding timing lines (DS4_MTP_TIMING,
// on stderr) from the arm's log and accumulates them. ds4 has no /metrics,
// so these counters are exporter-owned. docs/adapters.md, "ds4", records
// the formats; local-llm's benchmarks/agent/mtp_timing.py is the reference
// parser, and the tests pin agreement with it.
package ds4

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"sync"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/tail"
)

// Info registers the adapter.
var Info = adapter.Info{
	Engine:  "ds4",
	Version: "1",
	New:     func(c adapter.Config) adapter.Adapter { return &Adapter{cfg: c} },
}

var (
	// The Qwen path. accepted is already draft-only; the free first token
	// is the +1 in target_tokens.
	qwenRE = regexp.MustCompile(`^ds4: Qwen MTP timing drafted=(\d+) accepted=(\d+) target_tokens=(\d+)`)
	// The decode2, micro and margin-skip paths. committed includes the free
	// first token, and so does drafted: a declined draft prints
	// drafted=2 committed=1.
	cycleRE = regexp.MustCompile(`^ds4: mtp timing [\w-]+ drafted=(\d+) committed=(\d+)\b`)
)

// Cycle is one speculative cycle with the free first token removed.
type Cycle struct {
	Proposed uint64
	Accepted uint64
}

// ParseLine reads one log line. ok is false for any other line.
func ParseLine(line []byte) (c Cycle, ok bool) {
	if m := qwenRE.FindSubmatch(line); m != nil {
		drafted, accepted, target := num(m[1]), num(m[2]), num(m[3])
		if target != accepted+1 {
			// The line's meaning has changed; say so rather than count on.
			slog.Warn("ds4: unexpected Qwen MTP line, the format may have changed",
				"target_tokens", target, "accepted", accepted)
		}
		return Cycle{Proposed: drafted, Accepted: accepted}, true
	}
	if m := cycleRE.FindSubmatch(line); m != nil {
		return Cycle{Proposed: dec(num(m[1])), Accepted: dec(num(m[2]))}, true
	}
	return Cycle{}, false
}

func num(b []byte) uint64 {
	v, _ := strconv.ParseUint(string(b), 10, 64) // the regexp admits digits only
	return v
}

func dec(v uint64) uint64 {
	if v == 0 {
		return 0
	}
	return v - 1
}

// Adapter accumulates cycles from one ds4 log.
type Adapter struct {
	cfg  adapter.Config
	mu   sync.Mutex
	tail *tail.Reader

	seen                        bool // a cycle line since the last reset
	proposed, accepted, verifys uint64
}

// Start opens the log position at the start of the file.
func (a *Adapter) Start(context.Context) error {
	a.tail = tail.New(a.cfg.LogPath, a.cfg.MaxRead, 64<<10)
	return nil
}

// Collect checks that ds4 answers, reads the new log lines, and returns the
// running totals.
func (a *Adapter) Collect(ctx context.Context) (adapter.Result, error) {
	if err := adapter.Probe(ctx, a.cfg.Client, a.cfg.Endpoint, "/v1/models"); err != nil {
		return adapter.Result{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.tail.Read(a.reset, func(line []byte) {
		if !bytes.HasPrefix(line, []byte("ds4: ")) {
			return
		}
		c, ok := ParseLine(line)
		if !ok {
			return
		}
		a.seen = true
		a.proposed += c.Proposed
		a.accepted += c.Accepted
		if c.Proposed > 0 {
			a.verifys++
		}
	})
	if err != nil {
		return adapter.Result{}, err
	}
	if !a.seen {
		if backlog := a.tail.Backlog(); backlog > 0 {
			return adapter.Result{BacklogBytes: backlog}, nil
		}
		// Absent, not zero: without DS4_MTP_TIMING there is never a line.
		return adapter.Result{}, nil
	}
	if backlog := a.tail.Backlog(); backlog > 0 {
		return adapter.Result{BacklogBytes: backlog}, nil
	}
	return adapter.Result{Samples: []metrics.Sample{
		{Def: &metrics.SpecDraftTokens, Value: float64(a.proposed)},
		{Def: &metrics.SpecAcceptedTokens, Value: float64(a.accepted)},
		{Def: &metrics.SpecVerifySteps, Value: float64(a.verifys)},
	}}, nil
}

func (a *Adapter) reset() {
	a.seen, a.proposed, a.accepted, a.verifys = false, 0, 0, 0
}

// Close releases nothing: the log is opened per Collect.
func (a *Adapter) Close() error { return nil }
