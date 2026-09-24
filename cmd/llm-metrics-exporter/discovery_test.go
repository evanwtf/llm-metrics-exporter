package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// One handler/registry survives the entire sequence; neither registrations nor
// exporter configuration change. Synthetic counters deliberately restart.
func TestAutomaticHTTPHotSwitch(t *testing.T) {
	var phase atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch phase.Load() {
		case 1:
			http.Error(w, "offline", 503)
		case 2:
			if r.URL.Path == "/v1/models" {
				fmt.Fprint(w, `{"data":[{"id":"llama-model"}]}`)
				return
			}
			fmt.Fprint(w, "llamacpp:tokens_predicted_total 7\nllamacpp:prompt_tokens_total 30\n")
		default:
			fmt.Fprint(w, "vllm:generation_tokens_total{model_name=\"v-model\",engine=\"0\"} 10\nvllm:prompt_tokens_by_source_total{model_name=\"v-model\",engine=\"0\",source=\"local_compute\"} 20\n")
		}
	}))
	defer engine.Close()
	cfg := serveConfig{dir: t.TempDir(), host: "test-host", timeout: time.Second, discovery: "local", discoveryEndpoints: engine.URL, listen: "127.0.0.1:9109"}
	h, closeFn := newHandler(cfg, discard())
	defer closeFn()
	exporter := httptest.NewServer(h)
	defer exporter.Close()
	for _, p := range []int32{0, 1, 2, 0} {
		phase.Store(p)
		r, err := http.Get(exporter.URL + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if r.StatusCode != 200 {
			t.Fatal(text)
		}
		hasTokens := strings.Contains(text, "\nllme_tokens_total{")
		if hasTokens != (p != 1) {
			t.Fatalf("phase %d stale/missing counters: %s", p, text)
		}
		if !strings.Contains(text, `nodes="unknown"`) || !strings.Contains(text, "llme_exporter_discovery_changed_timestamp_seconds{") {
			t.Fatal(text)
		}
		if p == 2 && (strings.Contains(text, `engine="vllm"`) || !strings.Contains(text, `model="llama-model"`)) {
			t.Fatal(text)
		}
		if p == 0 && strings.Contains(text, `engine="llamacpp"`) {
			t.Fatal(text)
		}
	}
}

func TestInvalidDiscoveryFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--discovery=guess"}, {"--discovery-nodes=-1"}, {"--discovery-nodes=65"},
		{"--discovery-endpoints=http://example.com:8000"},
		{"--discovery=off", "--discovery-endpoints=http://127.0.0.1:8000"},
		{"--listen=127.0.0.1:0"},
		{"--listen=127.0.0.1:00"},
		{"--listen=127.0.0.1:65536"},
	} {
		if code := serve(args, io.Discard); code != exitUsage {
			t.Fatalf("%v: %d", args, code)
		}
	}
}
