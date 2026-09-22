// Package checkpublic tests scripts/check-public.sh, the guard for the rule
// that this public repo names no private host or address (AGENTS.md).
package checkpublic

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func check(t *testing.T, content string, denylist string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "../check-public.sh", file)
	cmd.Env = append(os.Environ(), "PUBLIC_DENYLIST="+filepath.Join(dir, "deny"))
	if denylist != "" {
		os.WriteFile(filepath.Join(dir, "deny"), []byte(denylist), 0o644)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out)
		}
		t.Fatal(err)
	}
	return 0, string(out)
}

func TestRefuses(t *testing.T) {
	for name, content := range map[string]string{
		"10/8":            "scrape 10.0.4.7 now",
		"172.16/12":       "at 172.20.1.1:9109",
		"192.168/16":      "endpoint http://192.168.1.180:8030",
		".internal host":  "curl http://gpu-box.internal:8888/metrics",
		".lan host":       "ssh nas.lan",
		".home.arpa host": "router.home.arpa",
		"home directory":  "log at /Users/someone/models/x.gguf",
		"linux home":      "log at /home/someone/models",
	} {
		t.Run(name, func(t *testing.T) {
			if code, out := check(t, content, ""); code != 1 {
				t.Fatalf("exit %d, want 1: %s", code, out)
			}
		})
	}
}

func TestDenylist(t *testing.T) {
	code, out := check(t, "served from bigbox yesterday", "# private names\nbigbox\n")
	if code != 1 || !strings.Contains(out, "bigbox") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestAllows(t *testing.T) {
	for name, content := range map[string]string{
		"loopback":        "http://127.0.0.1:8000 and 0.0.0.0:9109",
		"doc ranges":      "192.0.2.10 198.51.100.1 203.0.113.9",
		"version numbers": "vLLM 0.29.1rc1, llama.cpp b10809, go1.27.1, 10.1.2 not an address",
		"public 172":      "172.32.0.1 is outside 172.16/12",
		"xdg path":        "~/.local/state/llm-metrics-exporter",
		"empty denylist":  "anything",
	} {
		t.Run(name, func(t *testing.T) {
			if code, out := check(t, content, "\n# only comments\n"); code != 0 {
				t.Fatalf("exit %d, want 0: %s", code, out)
			}
		})
	}
}
