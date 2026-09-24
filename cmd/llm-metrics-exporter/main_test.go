package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evanwtf/llm-metrics-exporter/internal/registration"
	"github.com/evanwtf/llm-metrics-exporter/internal/version"
)

func runCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := run(args, &out)
	return code, out.String()
}

func registerArgs(dir string, extra ...string) []string {
	return append([]string{"register", "--registration-dir", dir,
		"--backend", "tiny", "--engine", "llamacpp", "--endpoint", "http://127.0.0.1:8020",
		"--model", "smollm2-135m", "--nodes", "1", "--run-id", "run-1"}, extra...)
}

func TestRegisterAndDeregister(t *testing.T) {
	dir := t.TempDir()
	if code, out := runCLI(t, registerArgs(dir, "--issue", "675")...); code != 0 {
		t.Fatalf("register exit %d: %s", code, out)
	}
	valid, _ := registration.LoadDir(dir)
	if len(valid) != 1 || valid[0].Issue != 675 || valid[0].RunID != "run-1" {
		t.Fatalf("registered %+v", valid)
	}
	// A late stop from another run leaves the file and says so.
	if code, _ := runCLI(t, "deregister", "--registration-dir", dir, "--backend", "tiny", "--run-id", "run-0"); code != exitRunIDMismatch {
		t.Fatalf("deregister of another run: exit %d, want %d", code, exitRunIDMismatch)
	}
	if code, out := runCLI(t, "deregister", "--registration-dir", dir, "--backend", "tiny", "--run-id", "run-1"); code != 0 {
		t.Fatalf("deregister exit %d: %s", code, out)
	}
	// Stopping twice is not an error: the arm is already gone.
	if code, _ := runCLI(t, "deregister", "--registration-dir", dir, "--backend", "tiny", "--run-id", "run-1"); code != 0 {
		t.Fatalf("second deregister exit %d", code)
	}
}

func TestRegisterRefusesInvalid(t *testing.T) {
	dir := t.TempDir()
	code, out := runCLI(t, registerArgs(dir, "--nodes", "0")...)
	if code != exitUsage {
		t.Fatalf("exit %d, want %d: %s", code, exitUsage, out)
	}
	if entries, _ := os.ReadDir(dir); len(entries) > 1 { // the lock file may exist
		t.Fatalf("files: %v", entries)
	}
}

func TestUnknownCommand(t *testing.T) {
	if code, _ := runCLI(t, "frobnicate"); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
}

func TestVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		code, out := runCLI(t, args...)
		if code != 0 || !strings.Contains(out, "version="+version.Version) {
			t.Errorf("%v: exit %d out %q", args, code, out)
		}
	}
}

// /metrics serves only the llm_* series, so every series has engine and
// model: no Go runtime or process series (operator requirement).
func TestHandler(t *testing.T) {
	dir := t.TempDir()
	body, err := os.ReadFile("../../testdata/llamacpp/b10809-5266f24da.metrics.txt")
	if err != nil {
		t.Fatal(err)
	}
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer engine.Close()
	if code, out := runCLI(t, "register", "--registration-dir", dir, "--backend", "tiny",
		"--engine", "llamacpp", "--endpoint", engine.URL, "--model", "smollm2-135m",
		"--nodes", "1", "--run-id", "r"); code != 0 {
		t.Fatalf("register: %s", out)
	}
	h, closeFn := newHandler(serveConfig{dir: dir, host: "h1", timeout: defaultTimeout}, discard())
	defer closeFn()
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	text, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, line := range strings.Split(string(text), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "llm_") {
			t.Errorf("non-llm series: %s", line)
		}
		if !strings.Contains(line, `engine="`) || !strings.Contains(line, `model="`) {
			t.Errorf("series without engine and model: %s", line)
		}
	}
	if !strings.Contains(string(text), `llm_tokens_total{backend="tiny",engine="llamacpp",host="h1",model="smollm2-135m",nodes="1",phase="decode",worker="default"} 224`) {
		t.Errorf("decode tokens missing:\n%s", text)
	}

	for _, path := range []string{"/", "/healthz"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil || resp.StatusCode != 200 {
			t.Errorf("%s: %v %v", path, err, resp.Status)
		}
		resp.Body.Close()
	}
}

func TestDefaultHost(t *testing.T) {
	if h := shortHostname(); h == "" || strings.Contains(h, ".") {
		t.Fatalf("host %q", h)
	}
}

func TestRegistrationDirDefault(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := registration.DefaultDir()
	args := []string{"register", "--backend", "tiny", "--engine", "vllm",
		"--endpoint", "http://127.0.0.1:1", "--model", "m", "--nodes", "1", "--run-id", "r"}
	if code, out := runCLI(t, args...); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "tiny.yaml")); err != nil {
		t.Fatal(err)
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// `health` lets a shell-less container check itself (Docker HEALTHCHECK).
func TestHealth(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
		}
	}))
	defer ok.Close()
	if code, out := runCLI(t, "health", "--url", ok.URL+"/healthz"); code != exitOK {
		t.Fatalf("healthy server: exit %d: %s", code, out)
	}
	if code, _ := runCLI(t, "health", "--url", ok.URL+"/missing"); code != exitError {
		t.Fatalf("404: exit %d", code)
	}
	ok.Close()
	if code, _ := runCLI(t, "health", "--url", ok.URL+"/healthz"); code != exitError {
		t.Fatalf("server down: exit %d", code)
	}
}

// LLM_EXPORTER_HOST sets the host label default; containers use it when the
// container hostname is not the host's.
func TestDefaultHostFromEnv(t *testing.T) {
	t.Setenv("LLM_EXPORTER_HOST", "spark-head")
	if h := defaultHost(); h != "spark-head" {
		t.Fatalf("host %q", h)
	}
	t.Setenv("LLM_EXPORTER_HOST", "")
	if h := defaultHost(); h != shortHostname() {
		t.Fatalf("host %q, want the short hostname", h)
	}
}
