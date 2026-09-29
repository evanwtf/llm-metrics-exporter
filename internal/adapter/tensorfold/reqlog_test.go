package tensorfold

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	at "github.com/evanwtf/llm-metrics-exporter/internal/adapter/adaptertest"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
)

const requestLog = "../../../testdata/tensorfold/v0.3.4-glm53-e9c8cbb.requests.jsonl"

func server(t *testing.T) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(s.Close)
	return s
}

func collect(t *testing.T, logPath string) adapter.Result {
	t.Helper()
	a := Info.New(adapter.Config{Endpoint: server(t).URL, ServedModel: served, LogPath: logPath,
		Client: http.DefaultClient, MaxBody: 1 << 20, MaxRead: 32 << 20})
	if _, ok := a.(*LogAdapter); !ok {
		t.Fatalf("log_path %q: %T, want the log adapter", logPath, a)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := a.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Counts read from the fixture independently:
// jq -r .finish v0.3.4-glm53-e9c8cbb.requests.jsonl | sort | uniq -c (147 tool_calls, 34 stop)
func TestRequestLog(t *testing.T) {
	s := collect(t, requestLog).Samples
	at.Valid(t, s)
	at.Want(t, s, &metrics.Requests, 147, "tool_calls")
	at.Want(t, s, &metrics.Requests, 34, "stop")
	// No line finished for another reason: absent, not zero.
	for _, status := range []string{"length", "cancelled", "error"} {
		if _, ok := at.Find(s, &metrics.Requests, status); ok {
			t.Errorf("status %s: present, no line has it", status)
		}
	}
	// The /metrics mapping is unchanged.
	at.Want(t, s, &metrics.Tokens, 2557, metrics.Decode)
	at.Absent(t, s, &metrics.TimeToFirstToken)
}

// TensorFold creates the log at its first request: no file is not an error.
func TestRequestLogNotYetWritten(t *testing.T) {
	s := collect(t, filepath.Join(t.TempDir(), "requests.jsonl")).Samples
	at.Absent(t, s, &metrics.Requests)
	at.Want(t, s, &metrics.Tokens, 2557, metrics.Decode)
}

// A finish the log format does not define is skipped, never counted under
// a new label; an error and a cancel are the engine's own reasons.
func TestRequestLogFinishes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	lines := `{"finish":"error","error":"RuntimeError"}
{"finish":"cancelled"}
{"finish":"length"}
{"finish":null}
{"finish":"weird"}
not json
{"finish":"stop"}
{"finish":"stop"`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	s := collect(t, path).Samples
	at.Valid(t, s)
	for _, status := range []string{"error", "cancelled", "length", "stop"} {
		at.Want(t, s, &metrics.Requests, 1, status)
	}
	if _, ok := at.Find(s, &metrics.Requests, "weird"); ok {
		t.Error("counted an unknown finish")
	}
}

func TestWithoutLogIsPull(t *testing.T) {
	if _, ok := Info.New(adapter.Config{}).(*adapter.Pull); !ok {
		t.Fatal("no log_path: want the pull adapter")
	}
}
