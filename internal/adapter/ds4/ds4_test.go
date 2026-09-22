package ds4

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

const fixture = "../../../testdata/ds4/qwen-mtp-sweep.log"

func TestParseLine(t *testing.T) {
	cases := []struct {
		line               string
		ok                 bool
		proposed, accepted uint64
	}{
		// Qwen path (real, from the fixture): accepted is already draft-only.
		{"ds4: Qwen MTP timing drafted=7 accepted=3 target_tokens=4 cycle=99.800 ms verifier=block", true, 7, 3},
		// Scheduler bypass: plain decode, nothing proposed.
		{"ds4: Qwen MTP timing drafted=0 accepted=0 target_tokens=1 cycle=21.8 ms verifier=scheduler-bypass", true, 0, 0},
		// decode2/micro paths (format strings from ds4.c at 9ab70534):
		// committed includes the free first token, and so does drafted.
		{"ds4: mtp timing micro drafted=7 committed=5 draft=1.000 ms snapshot=0.100 ms verify=2.000 ms total=3.100 ms", true, 6, 4},
		{"ds4: mtp timing decode2 drafted=2 committed=2 draft=1.000 ms snapshot=0.100 ms verify=2.000 ms total=3.100 ms", true, 1, 1},
		// A declined draft still prints committed=1: nothing accepted.
		{"ds4: mtp timing margin-skip drafted=2 committed=1 margin=0.100 threshold=0.200 draft=1.000 ms verify=2.000 ms total=3.000 ms", true, 1, 0},
		{"ds4: mtp spec miss first draft=42", false, 0, 0},
		{"ds4: Qwen prefill selected chunk=2048 microtile=512", false, 0, 0},
		{"  ds4: Qwen MTP timing drafted=7 accepted=3 target_tokens=4", false, 0, 0},
		{"", false, 0, 0},
	}
	for _, c := range cases {
		cy, ok := ParseLine([]byte(c.line))
		if ok != c.ok || cy.Proposed != c.proposed || cy.Accepted != c.accepted {
			t.Errorf("%q: got ok=%v %+v, want ok=%v proposed=%d accepted=%d",
				c.line, ok, cy, c.ok, c.proposed, c.accepted)
		}
	}
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newAdapter(t *testing.T, endpoint, log string) adapter.Adapter {
	t.Helper()
	a := Info.New(adapter.Config{
		Endpoint: endpoint, LogPath: log, Client: http.DefaultClient, MaxRead: 1 << 20,
	})
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// The expected totals come from local-llm's mtp_timing.py run on the same
// file: 179 cycles, 362 proposed, 220 accepted, 57 drafting, 122 bypassed.
func TestFixtureMatchesLocalLLM(t *testing.T) {
	a := newAdapter(t, newServer(t).URL, fixture)
	res, err := a.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	at.Valid(t, res.Samples)
	at.Want(t, res.Samples, &metrics.SpecDraftTokens, 362)
	at.Want(t, res.Samples, &metrics.SpecAcceptedTokens, 220)
	at.Want(t, res.Samples, &metrics.SpecVerifySteps, 57)
	// Cycle lines do not prove every decoded token is counted.
	at.Absent(t, res.Samples, &metrics.Tokens)
}

func TestCountersAccumulateAndReset(t *testing.T) {
	log := filepath.Join(t.TempDir(), "ds4.log")
	write := func(s string, flag int) {
		f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|flag, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	line := "ds4: Qwen MTP timing drafted=7 accepted=3 target_tokens=4 cycle=1 ms verifier=block\n"
	write("ds4: loading\n", os.O_TRUNC)
	a := newAdapter(t, newServer(t).URL, log)
	collect := func() []metrics.Sample {
		res, err := a.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return res.Samples
	}

	// No cycle line yet: the counters are absent, not zero. Without
	// DS4_MTP_TIMING there are never any, and zero would claim "no drafting".
	if s := collect(); len(s) != 0 {
		t.Fatalf("samples before any cycle: %+v", s)
	}
	write(line, os.O_APPEND)
	at.Want(t, collect(), &metrics.SpecAcceptedTokens, 3)
	write(line+line, os.O_APPEND)
	at.Want(t, collect(), &metrics.SpecAcceptedTokens, 9)

	// A new server truncates the log: the exporter-owned counter resets,
	// which rate() and the harness both treat as a reset.
	write(line, os.O_TRUNC)
	at.Want(t, collect(), &metrics.SpecAcceptedTokens, 3)
}

// A log that is still readable after its engine died must not read as up.
func TestEngineDownIsAnError(t *testing.T) {
	srv := newServer(t)
	a := newAdapter(t, srv.URL, fixture)
	srv.Close()
	if _, err := a.Collect(context.Background()); err == nil {
		t.Fatal("no error with the engine down")
	}
}

func TestMissingLogIsAnError(t *testing.T) {
	a := newAdapter(t, newServer(t).URL, filepath.Join(t.TempDir(), "absent.log"))
	if _, err := a.Collect(context.Background()); err == nil {
		t.Fatal("no error with no log")
	}
}
