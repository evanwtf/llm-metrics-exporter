package collector

import (
	"context"
	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"sync/atomic"
	"testing"
	"time"
)

func TestBlockedCallBoundedAndCloseDeferred(t *testing.T) {
	var starts, closes, calls atomic.Int32
	release := make(chan struct{})
	info := fakeInfo(&starts, &closes, func(context.Context) (adapter.Result, error) { calls.Add(1); <-release; return adapter.Result{}, nil })
	c := New(Options{Timeout: time.Millisecond})
	a := &arm{ad: info.New(adapter.Config{})}
	for range 5 {
		if _, err := c.collectArm(a); err == nil {
			t.Fatal("expected timeout/busy")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls %d", calls.Load())
	}
	c.closeArm(a)
	if closes.Load() != 0 {
		t.Fatal("closed during collection")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for closes.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if closes.Load() != 1 {
		t.Fatal("adapter not closed after release")
	}
}

func TestTimedOutAdapterRecovers(t *testing.T) {
	var starts, closes atomic.Int32
	release := make(chan struct{})
	info := fakeInfo(&starts, &closes, func(context.Context) (adapter.Result, error) { <-release; return adapter.Result{}, nil })
	c := New(Options{Timeout: time.Millisecond})
	a := &arm{ad: info.New(adapter.Config{})}
	c.collectArm(a)
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := c.collectArm(a); err == nil {
			c.closeArm(a)
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("did not recover")
}
