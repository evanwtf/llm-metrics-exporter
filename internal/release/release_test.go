package release

import (
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
