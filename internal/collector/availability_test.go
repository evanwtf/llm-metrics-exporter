package collector

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/registration"
)

func TestAvailabilityAndBacklog(t *testing.T) {
	var starts, closes atomic.Int32
	result := adapter.Result{BacklogBytes: 100}
	e := newEnv(t, fakeInfo(&starts, &closes, func(context.Context) (adapter.Result, error) { return result, nil }))
	e.register(t, testArm("vllm", "http://127.0.0.1:1"))
	f := e.gather(t)
	want(t, f, metrics.BacklogBytes.Name, nil, 100)
	want(t, f, metrics.LastSuccess.Name, nil, 0)
	want(t, f, metrics.MetricAvailable.Name, map[string]string{"metric": metrics.Tokens.Name, "phase": "decode"}, 0)
	result = adapter.Result{Samples: []metrics.Sample{{Def: &metrics.Tokens, Labels: []string{"decode"}, Value: 0}}}
	f = e.gather(t)
	want(t, f, metrics.BacklogBytes.Name, nil, 0)
	want(t, f, metrics.MetricAvailable.Name, map[string]string{"metric": metrics.Tokens.Name, "phase": "decode"}, 1)
	want(t, f, metrics.MetricAvailable.Name, map[string]string{"metric": metrics.Tokens.Name, "phase": "prefill"}, 0)
}

func TestReplacementWaitsForBlockedAdapter(t *testing.T) {
	var starts, closes, calls atomic.Int32
	release := make(chan struct{})
	e := newEnv(t, fakeInfo(&starts, &closes, func(context.Context) (adapter.Result, error) {
		if calls.Add(1) == 1 {
			<-release
		}
		return adapter.Result{}, nil
	}))
	e.c.opts.Timeout = time.Millisecond
	r := testArm("vllm", "http://127.0.0.1:1")
	e.register(t, r)
	e.gather(t)
	r.RunID = "next"
	r.Model = "new-model"
	e.register(t, r)
	for range 3 {
		f := e.gather(t)
		want(t, f, metrics.EngineUp.Name, map[string]string{"model": "new-model"}, 0)
	}
	if starts.Load() != 1 || closes.Load() != 0 {
		t.Fatal("overlapping lifecycle")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for closes.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	e.gather(t)
	if starts.Load() != 2 || closes.Load() != 1 {
		t.Fatalf("starts=%d closes=%d", starts.Load(), closes.Load())
	}
	if err := registration.Remove(e.dir, r.Backend, r.RunID); err != nil {
		t.Fatal(err)
	}
	if f := e.gather(t); len(f) != 0 {
		t.Fatal("removed deployment retained")
	}
}
