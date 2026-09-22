package release

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const changelog = "# Changelog\n\nIntro.\n\n## 0.2.0 (2026-10-01)\n\nNew `vllm` thing.\n\n" +
	"## 0.1.0 (2026-09-22)\n\n- First release.\n- Uses `backticks`.\n\n## 0.0.1\n\n"

func TestCheckTag(t *testing.T) {
	if err := CheckTag("v0.1.0", "0.1.0"); err != nil {
		t.Fatal(err)
	}
}

// A tag that disagrees with the declared version must stop the release.
func TestCheckTagRefuses(t *testing.T) {
	for _, tag := range []string{"0.1.0", "v0.1.1", "v0.1.0-rc1", "v0.1", "", "refs/tags/v0.1.0"} {
		if err := CheckTag(tag, "0.1.0"); err == nil {
			t.Errorf("accepted tag %q for version 0.1.0", tag)
		}
	}
}

func TestNotes(t *testing.T) {
	got, err := Notes(changelog, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if got != "- First release.\n- Uses `backticks`.\n" {
		t.Fatalf("got %q", got)
	}
	if got, _ := Notes(changelog, "0.2.0"); !strings.Contains(got, "`vllm`") {
		t.Fatalf("0.2.0 notes %q", got)
	}
}

// No section, or an empty one, must stop the release: notes are never
// written by hand into a tag.
func TestNotesRefuses(t *testing.T) {
	for _, v := range []string{"0.3.0", "0.0.1", "0.1"} {
		if got, err := Notes(changelog, v); err == nil {
			t.Errorf("version %s: accepted, notes %q", v, got)
		}
	}
}

func TestZip(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "llm-metrics-exporter")
	os.WriteFile(bin, []byte("binary"), 0o755)
	lic := filepath.Join(dir, "LICENSE")
	os.WriteFile(lic, []byte("MIT"), 0o644)
	out := filepath.Join(dir, "out.zip")
	if err := Zip(out, "llm-metrics-exporter-0.1.0-darwin-arm64", bin, lic); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := map[string]os.FileMode{}
	for _, f := range r.File {
		got[f.Name] = f.Mode()
	}
	// The binary must stay executable after unzip, or it will not run.
	if m := got["llm-metrics-exporter-0.1.0-darwin-arm64/llm-metrics-exporter"]; m.Perm() != 0o755 {
		t.Errorf("binary mode %v, want 0755 (entries %v)", m, got)
	}
	if m := got["llm-metrics-exporter-0.1.0-darwin-arm64/LICENSE"]; m.Perm() != 0o644 {
		t.Errorf("LICENSE mode %v", m)
	}
	if len(got) != 2 {
		t.Errorf("entries %v", got)
	}
}

func TestZipRefuses(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	os.WriteFile(bin, []byte("x"), 0o755)
	existing := filepath.Join(dir, "exists.zip")
	os.WriteFile(existing, []byte("old"), 0o644)
	cases := map[string]func() error{
		"missing input":  func() error { return Zip(filepath.Join(dir, "a.zip"), "d", filepath.Join(dir, "absent")) },
		"no inputs":      func() error { return Zip(filepath.Join(dir, "b.zip"), "d") },
		"existing zip":   func() error { return Zip(existing, "d", bin) },
		"directory name": func() error { return Zip(filepath.Join(dir, "c.zip"), "../d", bin) },
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			if err := f(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if b, _ := os.ReadFile(existing); string(b) != "old" {
		t.Fatal("an existing zip was overwritten")
	}
}
