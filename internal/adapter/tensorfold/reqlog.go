package tensorfold

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"slices"
	"sync"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/tail"
)

// Finishes are the finish values the request log writes (patch 0300,
// reqlog.py). Any other value is skipped and logged, never counted under a
// label the exporter made up.
var Finishes = []string{"stop", "length", "tool_calls", "cancelled", "error"}

// LogAdapter reads /metrics like the pull adapter, and adds finished
// requests by finish reason from the request log (GLM53_TF_REQUEST_LOG, one
// JSON line per request). Those counts are exporter-owned.
type LogAdapter struct {
	cfg  adapter.Config
	mu   sync.Mutex
	tail *tail.Reader

	finished map[string]uint64
	warned   map[string]bool
}

// Start opens the log position at the start of the file.
func (a *LogAdapter) Start(context.Context) error {
	a.tail = tail.New(a.cfg.LogPath, a.cfg.MaxRead, 1<<20)
	a.finished = map[string]uint64{}
	a.warned = map[string]bool{}
	return nil
}

// Collect maps /metrics, then reads the new request-log lines.
func (a *LogAdapter) Collect(ctx context.Context) (adapter.Result, error) {
	fams, err := adapter.FetchMetrics(ctx, a.cfg.Client, a.cfg.Endpoint, a.cfg.MaxBody)
	if err != nil {
		return adapter.Result{}, err
	}
	res, err := Map(fams, a.cfg.ServedModel)
	if err != nil {
		return adapter.Result{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	err = a.tail.Read(a.reset, a.line)
	if errors.Is(err, fs.ErrNotExist) {
		// TensorFold creates the log at its first request. No file is no
		// request, not an unreadable engine; the counts stay absent.
		return res, nil
	}
	if err != nil {
		return adapter.Result{}, err
	}
	if backlog := a.tail.Backlog(); backlog > 0 {
		return adapter.Result{BacklogBytes: backlog}, nil
	}
	for _, status := range Finishes {
		if n, ok := a.finished[status]; ok {
			res.Samples = append(res.Samples, metrics.Sample{Def: &metrics.Requests, Labels: []string{status}, Value: float64(n)})
		}
	}
	return res, nil
}

func (a *LogAdapter) line(b []byte) {
	status, ok := ParseFinish(b)
	if !ok {
		if !a.warned[status] {
			a.warned[status] = true
			slog.Warn("tensorfold: request-log line without a known finish; not counted", "finish", status)
		}
		return
	}
	a.finished[status]++
}

func (a *LogAdapter) reset() { a.finished = map[string]uint64{} }

// Close releases nothing: the log is opened per Collect.
func (a *LogAdapter) Close() error { return nil }

// ParseFinish returns a request-log line's finish reason. ok is false for a
// line that is not JSON or has no known finish; status is then what the
// line held, for the warning.
func ParseFinish(line []byte) (status string, ok bool) {
	var rec struct {
		Finish *string `json:"finish"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return "<not json>", false
	}
	if rec.Finish == nil {
		return "<null>", false
	}
	return *rec.Finish, slices.Contains(Finishes, *rec.Finish)
}
