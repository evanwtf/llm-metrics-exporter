package remotewrite

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestQueueCannotChangeDestinationOrHost(t *testing.T) {
	reg, _ := testRegistry()
	o := testOptions(t, "http://127.0.0.1:1/api/v1/write")
	s := newTestSender(t, o, reg)
	s.Close()
	o.Host = "other-host"
	if other, err := New(o, reg, nil); err == nil {
		other.Close()
		t.Fatal("queue identity changed")
	}
	o.Host = "laptop-test"
	o.URL = "http://127.0.0.1:2/api/v1/write"
	if other, err := New(o, reg, nil); err == nil {
		other.Close()
		t.Fatal("queue destination changed")
	}
}

func TestTLSVerificationAndRedirects(t *testing.T) {
	reg, _ := testRegistry()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer tls.Close()
	s := newTestSender(t, testOptions(t, tls.URL), reg)
	if err := s.collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sendOne(context.Background()); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	s.Close()
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	s = newTestSender(t, testOptions(t, redirect.URL), reg)
	defer s.Close()
	if err := s.collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sendOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if redirected || s.Status().DroppedBatches != 1 {
		t.Fatal("redirect followed or not reported")
	}
}

func TestDeliveryBackoffAndCancellation(t *testing.T) {
	var mu sync.Mutex
	var attempts []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts = append(attempts, time.Now())
		mu.Unlock()
		w.WriteHeader(503)
	}))
	defer server.Close()
	reg, _ := testRegistry()
	s := newTestSender(t, testOptions(t, server.URL), reg)
	defer s.Close()
	if err := s.collect(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.deliver(ctx); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(attempts)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("backoff ignored cancellation")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 || attempts[1].Sub(attempts[0]) < 900*time.Millisecond {
		t.Fatal("retry backoff missing")
	}
	if s.Status().QueuedBatches != 1 {
		t.Fatal("retry discarded batch")
	}
}
