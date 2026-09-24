package ds4

import (
	"context"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	at "github.com/evanwtf/llm-metrics-exporter/internal/adapter/adaptertest"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestBacklogWithholdsCountersUntilCaughtUp(t *testing.T) {
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "log")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	a := Info.New(adapter.Config{Endpoint: newServer(t).URL, Client: http.DefaultClient, LogPath: path, MaxRead: int64(len(body) / 2)})
	if err = a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for cycle := 0; cycle < 2; cycle++ {
		r, err := a.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if r.BacklogBytes == 0 || len(r.Samples) != 0 {
			t.Fatalf("partial counters exposed: %+v", r)
		}
		for r.BacklogBytes > 0 {
			r, err = a.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
		}
		at.Want(t, r.Samples, &metrics.SpecDraftTokens, 362)
		if cycle == 0 {
			next := path + ".new"
			if err = os.WriteFile(next, body, 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.Rename(next, path); err != nil {
				t.Fatal(err)
			}
		}
	}
}
