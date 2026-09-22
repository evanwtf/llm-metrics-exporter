package registration

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validYAML = `version: 1
run_id: r-1
engine: vllm
endpoint: http://127.0.0.1:8000
model: qwen3.6-27b
backend: qwen36dense
nodes: 2
issue: 648
served_model: qwen3.6-27b-nvfp4
`

func TestParseValid(t *testing.T) {
	r, err := Parse([]byte(validYAML), "qwen36dense.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := Registration{
		Version: 1, RunID: "r-1", Engine: "vllm", Endpoint: "http://127.0.0.1:8000",
		Model: "qwen3.6-27b", Backend: "qwen36dense", Nodes: 2, Issue: 648,
		ServedModel: "qwen3.6-27b-nvfp4",
	}
	if r != want {
		t.Fatalf("got %+v\nwant %+v", r, want)
	}
}

// JSON is valid YAML, so a launcher can write either.
func TestParseJSON(t *testing.T) {
	body := `{"version":1,"run_id":"r","engine":"ds4","endpoint":"http://127.0.0.1:1",` +
		`"model":"m","backend":"b","nodes":1,"log_path":"/tmp/ds4.log"}`
	r, err := Parse([]byte(body), "b.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if r.LogPath != "/tmp/ds4.log" {
		t.Fatalf("log_path %q", r.LogPath)
	}
}

func TestParseRejects(t *testing.T) {
	replace := func(old, new string) string { return strings.Replace(validYAML, old, new, 1) }
	cases := map[string]struct{ body, file string }{
		"no version":          {replace("version: 1\n", ""), "qwen36dense.yaml"},
		"version 2":           {replace("version: 1", "version: 2"), "qwen36dense.yaml"},
		"unknown field":       {validYAML + "colour: blue\n", "qwen36dense.yaml"},
		"typo of a field":     {replace("served_model", "servedmodel"), "qwen36dense.yaml"},
		"no run_id":           {replace("run_id: r-1\n", ""), "qwen36dense.yaml"},
		"unknown engine":      {replace("engine: vllm", "engine: tgi"), "qwen36dense.yaml"},
		"no model":            {replace("model: qwen3.6-27b\n", ""), "qwen36dense.yaml"},
		"model with space":    {replace("model: qwen3.6-27b", "model: qwen 3.6"), "qwen36dense.yaml"},
		"nodes zero":          {replace("nodes: 2", "nodes: 0"), "qwen36dense.yaml"},
		"no nodes":            {replace("nodes: 2\n", ""), "qwen36dense.yaml"},
		"endpoint not http":   {replace("http://127.0.0.1:8000", "127.0.0.1:8000"), "qwen36dense.yaml"},
		"endpoint with path":  {replace("http://127.0.0.1:8000", "http://127.0.0.1:8000/v1"), "qwen36dense.yaml"},
		"backend not file":    {validYAML, "other.yaml"},
		"backend bad chars":   {replace("backend: qwen36dense", "backend: ../x"), "../x.yaml"},
		"negative issue":      {replace("issue: 648", "issue: -1"), "qwen36dense.yaml"},
		"log_path on vllm":    {validYAML + "log_path: /tmp/x\n", "qwen36dense.yaml"},
		"trace_path on vllm":  {validYAML + "trace_path: /tmp/x\n", "qwen36dense.yaml"},
		"two documents":       {validYAML + "---\n" + validYAML, "qwen36dense.yaml"},
		"empty":               {"", "qwen36dense.yaml"},
		"not a mapping":       {"- 1\n- 2\n", "qwen36dense.yaml"},
		"relative log_path":   {ds4YAML("log_path: ds4.log"), "b.yaml"},
		"ds4 without a log":   {ds4YAML(""), "b.yaml"},
		"mtplx without trace": {strings.Replace(ds4YAML(""), "engine: ds4", "engine: mtplx", 1), "b.yaml"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if r, err := Parse([]byte(c.body), c.file); err == nil {
				t.Fatalf("accepted %+v", r)
			}
		})
	}
}

func ds4YAML(extra string) string {
	return "version: 1\nrun_id: r\nengine: ds4\nendpoint: http://127.0.0.1:1\n" +
		"model: m\nbackend: b\nnodes: 1\n" + extra + "\n"
}

func TestWriteThenLoad(t *testing.T) {
	dir := t.TempDir()
	r := mustParse(t, validYAML)
	if err := Write(dir, r); err != nil {
		t.Fatal(err)
	}
	valid, invalid := LoadDir(dir)
	if len(invalid) != 0 {
		t.Fatalf("invalid: %v", invalid)
	}
	if len(valid) != 1 || valid[0] != r {
		t.Fatalf("got %+v", valid)
	}
	info, err := os.Stat(filepath.Join(dir, "qwen36dense.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode %v, want 0644", info.Mode().Perm())
	}
	// No temp file is left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
}

func TestWriteRefusesInvalid(t *testing.T) {
	dir := t.TempDir()
	r := mustParse(t, validYAML)
	r.Nodes = 0
	if err := Write(dir, r); err == nil {
		t.Fatal("wrote an invalid registration")
	}
	if entries, _ := os.ReadDir(dir); len(entries) > 1 { // the lock file may exist
		t.Fatalf("files written: %v", entries)
	}
}

func TestWriteCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := Write(dir, mustParse(t, validYAML)); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDirSkipsOtherFilesAndReportsInvalid(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("qwen36dense.yaml", validYAML)
	write("broken.yaml", "version: 1\n")
	write(".qwen36dense.yaml.tmp-123", validYAML) // an in-flight atomic write
	write("notes.txt", "hello")
	valid, invalid := LoadDir(dir)
	if len(valid) != 1 {
		t.Errorf("valid: %+v", valid)
	}
	if len(invalid) != 1 || invalid[0].File != "broken.yaml" {
		t.Errorf("invalid: %+v", invalid)
	}
}

func TestLoadDirMissingIsEmpty(t *testing.T) {
	valid, invalid := LoadDir(filepath.Join(t.TempDir(), "absent"))
	if len(valid) != 0 || len(invalid) != 0 {
		t.Fatalf("valid=%v invalid=%v", valid, invalid)
	}
}

func TestRemoveMatchingRunID(t *testing.T) {
	dir := t.TempDir()
	r := mustParse(t, validYAML)
	if err := Write(dir, r); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, r.Backend, r.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "qwen36dense.yaml")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file still there: %v", err)
	}
}

// A late stop from an old arm must not delete a newer arm's registration.
func TestRemoveRefusesAnotherRunID(t *testing.T) {
	dir := t.TempDir()
	newer := mustParse(t, validYAML)
	newer.RunID = "r-2"
	if err := Write(dir, newer); err != nil {
		t.Fatal(err)
	}
	err := Remove(dir, newer.Backend, "r-1")
	if !errors.Is(err, ErrRunIDMismatch) {
		t.Fatalf("err %v, want ErrRunIDMismatch", err)
	}
	valid, _ := LoadDir(dir)
	if len(valid) != 1 || valid[0].RunID != "r-2" {
		t.Fatalf("newer registration lost: %+v", valid)
	}
}

func TestRemoveAbsent(t *testing.T) {
	err := Remove(t.TempDir(), "qwen36dense", "r-1")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err %v, want not-exist", err)
	}
}

func TestRemoveRejectsBadBackend(t *testing.T) {
	if err := Remove(t.TempDir(), "../etc", "r"); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err %v, want a validation error", err)
	}
}

func TestDefaultDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if got := DefaultDir(); got != "/state/llm-metrics-exporter/registrations" {
		t.Errorf("got %s", got)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/x/u")
	if got := DefaultDir(); got != "/x/u/.local/state/llm-metrics-exporter/registrations" {
		t.Errorf("got %s", got)
	}
}

func mustParse(t *testing.T, body string) Registration {
	t.Helper()
	r, err := Parse([]byte(body), "qwen36dense.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// An invalid file still yields engine and model labels: its own values, or
// "unknown" when it states none (llm_registration_invalid).
func TestInvalidCarriesItsOwnEngineAndModel(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("version: 1\nengine: sglang\nmodel: m1\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.yaml"), []byte(":: not yaml"), 0o644)
	_, invalid := LoadDir(dir)
	if len(invalid) != 2 {
		t.Fatalf("invalid %+v", invalid)
	}
	if invalid[0].Engine != "sglang" || invalid[0].Model != "m1" {
		t.Errorf("a.yaml: %+v", invalid[0])
	}
	if invalid[1].Engine != "unknown" || invalid[1].Model != "unknown" {
		t.Errorf("b.yaml: %+v", invalid[1])
	}
}
