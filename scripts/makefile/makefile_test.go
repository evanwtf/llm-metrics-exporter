package makefile_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Use a fake toolchain: testing make check must not recursively run go test.
func TestTargets(t *testing.T) {
	makeBin, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is not installed")
	}
	makefile, err := filepath.Abs("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, target, fail, format string
		want                       []string
		keepGoing, wantError       bool
		envGo                      bool
	}{
		{name: "default-help", want: []string{"make build", "make check"}},
		{name: "build", target: "build", want: []string{"build -trimpath -o dist/llm-metrics-exporter ./cmd/llm-metrics-exporter"}},
		{name: "environment-go", target: "build", envGo: true, want: []string{"build -trimpath -o dist/llm-metrics-exporter ./cmd/llm-metrics-exporter"}},
		{name: "test", target: "test", want: []string{"test ./..."}},
		{name: "race", target: "race", want: []string{"test -race ./..."}},
		{name: "check", target: "check", want: []string{"env GOROOT", "gofmt -l .", "vet ./...", "mod tidy -diff", "public-check", "test ./..."}},
		{name: "build-failure", target: "build", fail: "build", wantError: true},
		{name: "test-failure", target: "test", fail: "test", wantError: true},
		{name: "race-failure", target: "race", fail: "test", wantError: true},
		{name: "format-diff", target: "fmt-check", format: "unformatted.go", want: []string{"gofmt needed:", "unformatted.go"}, wantError: true},
		{name: "formatter-failure", target: "fmt-check", fail: "gofmt", wantError: true},
		{name: "goroot-failure", target: "fmt-check", fail: "env", wantError: true},
		{name: "vet-failure", target: "check", fail: "vet", keepGoing: true, want: []string{"mod tidy -diff", "public-check", "test ./..."}, wantError: true},
		{name: "dependency-failure", target: "check", fail: "mod", wantError: true},
		{name: "public-failure", target: "check", fail: "public-check", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			toolRoot := filepath.Join(dir, "tool chain") // Exercise quoted paths.
			write := func(name, body string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			goBin := filepath.Join(toolRoot, "bin", "go")
			write(goBin, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TRACE\"\n[ \"$FAIL\" != \"$1\" ] || exit 7\nif [ \"$1\" = env ]; then printf '%s\\n' \"$TOOL_ROOT\"; fi\n")
			write(filepath.Join(toolRoot, "bin", "gofmt"), "#!/bin/sh\nprintf 'gofmt %s\\n' \"$*\" >> \"$TRACE\"\n[ \"$FAIL\" != gofmt ] || exit 7\n[ -z \"$FORMAT\" ] || printf '%s\\n' \"$FORMAT\"\nexit 0\n")
			write(filepath.Join(dir, "scripts", "check-public.sh"), "#!/bin/sh\nprintf 'public-check\\n' >> \"$TRACE\"\n[ \"$FAIL\" != public-check ]\n")
			args := []string{"-f", makefile}
			if !tc.envGo {
				args = append(args, "GO="+goBin)
			}
			if tc.keepGoing {
				args = append(args, "-k")
			}
			if tc.target != "" {
				args = append(args, tc.target)
			}
			tracePath := filepath.Join(dir, "trace")
			cmd := exec.Command(makeBin, args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "MAKEFLAGS=", "MFLAGS=", "MAKEOVERRIDES=", "MAKELEVEL=0", "GO="+goBin, "TRACE="+tracePath, "TOOL_ROOT="+toolRoot, "FAIL="+tc.fail, "FORMAT="+tc.format)
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v; output: %s", err, output)
			}
			trace, err := os.ReadFile(tracePath)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if tc.target == "" && len(trace) != 0 {
				t.Fatal("default help invoked the toolchain")
			}
			for _, want := range tc.want {
				if !strings.Contains(string(output)+string(trace), want) {
					t.Errorf("missing %q in output/trace: %s%s", want, output, trace)
				}
			}
		})
	}
}
