// Package registration reads and writes the files that launchers leave in the
// registration directory, one per running arm (docs/design.md, "Discovery").
package registration

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"
)

// Version is the only registration schema version this exporter reads.
const Version = 1

// Engines are the valid engine values. An engine here without an adapter is
// still a valid registration: it exports llm_engine_up 0, which is louder than
// rejecting the file.
var Engines = []string{"vllm", "llamacpp", "sglang", "mlx-serve", "ollama", "ds4", "mtplx"}

// ErrRunIDMismatch means the registration belongs to a different run.
var ErrRunIDMismatch = errors.New("registration belongs to a different run_id")

// Registration is one arm. Field order follows docs/design.md.
type Registration struct {
	Version     int    `yaml:"version"`
	RunID       string `yaml:"run_id"`
	Engine      string `yaml:"engine"`
	Endpoint    string `yaml:"endpoint"`
	Model       string `yaml:"model"`
	Backend     string `yaml:"backend"`
	Nodes       int    `yaml:"nodes"`
	Issue       int    `yaml:"issue,omitempty"`
	ServedModel string `yaml:"served_model,omitempty"`
	LogPath     string `yaml:"log_path,omitempty"`
	TracePath   string `yaml:"trace_path,omitempty"`
}

// Invalid is a file in the directory that did not parse or validate.
type Invalid struct {
	File string
	Err  error
}

const suffix = ".yaml"

var (
	// backendRE keeps the backend usable as a file name.
	backendRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// tokenRE is a label value or run_id: printable, no whitespace.
	tokenRE = regexp.MustCompile(`^[[:graph:]]+$`)
)

// DefaultDir is $XDG_STATE_HOME/llm-metrics-exporter/registrations, or
// ~/.local/state/... when XDG_STATE_HOME is not set. The same on Linux and
// macOS, so `register` and `serve` agree without configuration.
func DefaultDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	return filepath.Join(base, "llm-metrics-exporter", "registrations")
}

// Parse decodes and validates one registration. file is its base name, which
// must be <backend>.yaml.
func Parse(body []byte, file string) (Registration, error) {
	var r Registration
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		if errors.Is(err, io.EOF) {
			return r, errors.New("empty registration")
		}
		return r, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return r, errors.New("more than one YAML document")
	}
	if err := r.Validate(); err != nil {
		return r, err
	}
	if want := r.Backend + suffix; file != want {
		return r, fmt.Errorf("file is %q but backend %q needs %q", file, r.Backend, want)
	}
	return r, nil
}

// Validate checks every field. It does not check the file name.
func (r Registration) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if r.Version != Version {
		bad("version is %d, want %d", r.Version, Version)
	}
	if !tokenRE.MatchString(r.RunID) {
		bad("run_id %q: required, no whitespace", r.RunID)
	}
	if !contains(Engines, r.Engine) {
		bad("engine %q: want one of %s", r.Engine, strings.Join(Engines, ", "))
	}
	if err := checkEndpoint(r.Endpoint); err != nil {
		bad("endpoint %q: %v", r.Endpoint, err)
	}
	if !tokenRE.MatchString(r.Model) {
		bad("model %q: required, no whitespace", r.Model)
	}
	if !backendRE.MatchString(r.Backend) {
		bad("backend %q: required; letters, digits, '.', '_', '-'", r.Backend)
	}
	if r.Nodes < 1 || r.Nodes > 64 {
		bad("nodes is %d, want 1 to 64", r.Nodes)
	}
	if r.Issue < 0 {
		bad("issue is %d", r.Issue)
	}
	if r.ServedModel != "" && !tokenRE.MatchString(r.ServedModel) {
		bad("served_model %q: no whitespace", r.ServedModel)
	}
	// A path field on an engine that does not read it is a mistake: the
	// launcher thinks the exporter is reading a file it is not.
	checkPath := func(field, value, engine string) {
		switch {
		case r.Engine == engine && value == "":
			bad("%s is required for engine %s", field, engine)
		case r.Engine != engine && value != "":
			bad("%s applies to engine %s only", field, engine)
		case value != "" && !filepath.IsAbs(value):
			bad("%s %q is not absolute", field, value)
		}
	}
	checkPath("log_path", r.LogPath, "ds4")
	checkPath("trace_path", r.TracePath, "mtplx")
	return errors.Join(errs...)
}

func checkEndpoint(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("scheme must be http or https")
	}
	if u.Host == "" {
		return errors.New("no host")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" {
		return errors.New("base URL only: no path or query")
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// LoadDir reads every <backend>.yaml in dir. Hidden files (in-flight atomic
// writes) and other extensions are skipped. A missing directory has no
// registrations.
func LoadDir(dir string) (valid []Registration, invalid []Invalid) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			invalid = append(invalid, Invalid{File: dir, Err: err})
		}
		return nil, invalid
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, suffix) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			var r Registration
			if r, err = Parse(body, name); err == nil {
				valid = append(valid, r)
				continue
			}
		}
		invalid = append(invalid, Invalid{File: name, Err: err})
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].Backend < valid[j].Backend })
	return valid, invalid
}

// Write stores r as <dir>/<backend>.yaml atomically: a reader sees the old
// file or the new one, never a partial one. It replaces any registration for
// the same backend, because the newest launch is the arm that is running.
func Write(dir string, r Registration) error {
	if err := r.Validate(); err != nil {
		return err
	}
	body, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	unlock, err := lock(dir)
	if err != nil {
		return err
	}
	defer unlock()

	tmp, err := os.CreateTemp(dir, "."+r.Backend+suffix+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, r.Backend+suffix))
}

// Remove deletes <dir>/<backend>.yaml only if its run_id is runID. It
// returns ErrRunIDMismatch and leaves the file when a newer run owns it, and
// an fs.ErrNotExist error when there is no file.
func Remove(dir, backend, runID string) error {
	if !backendRE.MatchString(backend) {
		return fmt.Errorf("backend %q is not a valid backend name", backend)
	}
	path := filepath.Join(dir, backend+suffix)
	if _, err := os.Stat(path); err != nil {
		return err
	}
	unlock, err := lock(dir)
	if err != nil {
		return err
	}
	defer unlock()

	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Match on run_id even if the rest of the file is invalid: the file is
	// still this run's to remove.
	var head struct {
		RunID string `yaml:"run_id"`
	}
	if err := yaml.Unmarshal(body, &head); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if head.RunID != runID {
		return fmt.Errorf("%s has run_id %q, not %q: %w", path, head.RunID, runID, ErrRunIDMismatch)
	}
	return os.Remove(path)
}

// lock takes an exclusive advisory lock on <dir>/.lock, so a Remove cannot
// read one run's file and delete the next run's.
func lock(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
