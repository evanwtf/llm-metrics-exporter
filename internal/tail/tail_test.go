package tail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recorder struct {
	lines  []string
	resets int
}

func (r *recorder) read(t *testing.T, tr *Reader) {
	t.Helper()
	err := tr.Read(func() { r.resets++; r.lines = nil }, func(l []byte) { r.lines = append(r.lines, string(l)) })
	if err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestReadsOnlyNewCompleteLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	appendTo(t, path, "a\nb\npart")
	tr := New(path, 1<<20, 1<<20)
	var r recorder
	r.read(t, tr)
	if strings.Join(r.lines, ",") != "a,b" {
		t.Fatalf("first read %v", r.lines)
	}
	appendTo(t, path, "ial\nc\n")
	r.read(t, tr)
	if strings.Join(r.lines, ",") != "a,b,partial,c" {
		t.Fatalf("second read %v", r.lines)
	}
	r.read(t, tr) // nothing new
	if len(r.lines) != 4 || r.resets != 0 {
		t.Fatalf("third read %v resets %d", r.lines, r.resets)
	}
}

// A log that shrank belongs to a new server: start over, and say so, so the
// caller resets its counters instead of reading nothing as zero.
func TestTruncationResets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	appendTo(t, path, "one\ntwo\n")
	tr := New(path, 1<<20, 1<<20)
	var r recorder
	r.read(t, tr)
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.read(t, tr)
	if r.resets != 1 || strings.Join(r.lines, ",") != "x" {
		t.Fatalf("resets %d lines %v", r.resets, r.lines)
	}
}

// A replaced file (new inode) resets even when it is longer than the offset.
func TestReplacementResets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	appendTo(t, path, "old\n")
	tr := New(path, 1<<20, 1<<20)
	var r recorder
	r.read(t, tr)
	next := filepath.Join(dir, "log.new")
	appendTo(t, next, "new1\nnew2\nnew3\n")
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	r.read(t, tr)
	if r.resets != 1 || strings.Join(r.lines, ",") != "new1,new2,new3" {
		t.Fatalf("resets %d lines %v", r.resets, r.lines)
	}
}

func TestMissingFileIsAnError(t *testing.T) {
	tr := New(filepath.Join(t.TempDir(), "absent"), 1<<20, 1<<20)
	if err := tr.Read(func() {}, func([]byte) {}); err == nil {
		t.Fatal("no error")
	}
}

// Memory is bounded: an over-long line is dropped whole, and the next line
// still reads.
func TestOverlongLineIsDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	appendTo(t, path, strings.Repeat("x", 100))
	tr := New(path, 1<<20, 10)
	var r recorder
	r.read(t, tr)
	appendTo(t, path, strings.Repeat("y", 50)+"\nok\n")
	r.read(t, tr)
	if strings.Join(r.lines, ",") != "ok" {
		t.Fatalf("lines %v", r.lines)
	}
}

// One Read takes at most maxRead bytes; the rest arrives on later reads.
func TestMaxReadPerCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	appendTo(t, path, "aaaa\nbbbb\ncccc\n")
	tr := New(path, 6, 1<<20)
	var r recorder
	for range 5 {
		r.read(t, tr)
	}
	if strings.Join(r.lines, ",") != "aaaa,bbbb,cccc" {
		t.Fatalf("lines %v", r.lines)
	}
}

func TestCarriageReturnIsStripped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	appendTo(t, path, "a\r\n")
	tr := New(path, 1<<20, 1<<20)
	var r recorder
	r.read(t, tr)
	if len(r.lines) != 1 || r.lines[0] != "a" {
		t.Fatalf("lines %q", r.lines)
	}
}
