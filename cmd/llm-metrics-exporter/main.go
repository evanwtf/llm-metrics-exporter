// Command llm-metrics-exporter exports canonical LLM inference metrics for
// every engine registered on this host, and registers or deregisters arms.
//
//	llm-metrics-exporter [serve] [flags]      run the exporter
//	llm-metrics-exporter register [flags]     write a registration atomically
//	llm-metrics-exporter deregister [flags]   remove one, only if run_id matches
//	llm-metrics-exporter version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/evanwtf/llm-metrics-exporter/internal/collector"
	"github.com/evanwtf/llm-metrics-exporter/internal/engines"
	"github.com/evanwtf/llm-metrics-exporter/internal/registration"
	"github.com/evanwtf/llm-metrics-exporter/internal/version"
)

// Exit codes. Launchers branch on these.
const (
	exitOK            = 0
	exitError         = 1
	exitUsage         = 2
	exitRunIDMismatch = 3 // deregister: a newer run owns the registration
)

const (
	defaultListen  = ":9109"
	defaultTimeout = 5 * time.Second
	maxBody        = 16 << 20 // largest /metrics body read from an engine
	maxRead        = 32 << 20 // most log bytes read per scrape
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

// run is main without the process exit, so tests can drive it.
func run(args []string, out io.Writer) int {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-version") {
		cmd = "version"
	}
	switch cmd {
	case "serve":
		return serve(args, out)
	case "register":
		return register(args, out)
	case "deregister":
		return deregister(args, out)
	case "version":
		logger(out, "info").Info("llm-metrics-exporter", versionAttrs()...)
		return exitOK
	default:
		logger(out, "info").Error("unknown command; want serve, register, deregister or version", "command", cmd)
		return exitUsage
	}
}

func versionAttrs() []any {
	return []any{"version", version.Version, "revision", version.Revision, "goversion", runtime.Version()}
}

// logger writes to out (stdout): one stream for the command's output and its
// diagnostics.
func logger(out io.Writer, level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: l}))
}

func shortHostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	h, _, _ = strings.Cut(h, ".")
	return h
}

type serveConfig struct {
	listen  string
	dir     string
	host    string
	timeout time.Duration
}

func serve(args []string, out io.Writer) int {
	fl := flag.NewFlagSet("serve", flag.ContinueOnError)
	fl.SetOutput(out)
	var cfg serveConfig
	fl.StringVar(&cfg.listen, "listen", defaultListen, "address to serve /metrics on")
	fl.StringVar(&cfg.dir, "registration-dir", registration.DefaultDir(), "directory of arm registrations")
	fl.StringVar(&cfg.host, "host", shortHostname(), "value of the host label")
	fl.DurationVar(&cfg.timeout, "timeout", defaultTimeout, "per-arm collection timeout; keep it below the scrape timeout")
	level := fl.String("log-level", "info", "debug, info, warn or error")
	if err := fl.Parse(args); err != nil {
		return exitUsage
	}
	log := logger(out, *level)
	slog.SetDefault(log)
	if err := os.MkdirAll(cfg.dir, 0o755); err != nil {
		log.Error("cannot create the registration directory", "dir", cfg.dir, "err", err)
		return exitError
	}

	handler, closeCollector := newHandler(cfg, log)
	defer closeCollector()
	srv := &http.Server{Addr: cfg.listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("serving", append([]any{"listen", cfg.listen, "registration_dir", cfg.dir, "host", cfg.host}, versionAttrs()...)...)

	select {
	case err := <-errc:
		log.Error("server stopped", "err", err)
		return exitError
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Error("shutdown", "err", err)
		return exitError
	}
	log.Info("stopped")
	return exitOK
}

// newHandler builds the HTTP handler. The registry holds only the collector:
// no Go runtime or process series, because every exported series must carry
// engine and model.
func newHandler(cfg serveConfig, log *slog.Logger) (http.Handler, func()) {
	c := collector.New(collector.Options{
		Dir: cfg.dir, Host: cfg.host, Timeout: cfg.timeout,
		Client: &http.Client{}, MaxBody: maxBody, MaxRead: maxRead,
		Adapters: engines.All(), ExporterVersion: version.Version, Logger: log,
	})
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		ErrorLog:      slog.NewLogLogger(log.Handler(), slog.LevelError),
		ErrorHandling: promhttp.ContinueOnError,
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "llm-metrics-exporter %s\n\n/metrics  canonical llm_* series\n/healthz  liveness\n", version.Version)
	})
	return mux, c.Close
}

func register(args []string, out io.Writer) int {
	fl := flag.NewFlagSet("register", flag.ContinueOnError)
	fl.SetOutput(out)
	dir := fl.String("registration-dir", registration.DefaultDir(), "directory of arm registrations")
	r := registration.Registration{Version: registration.Version}
	fl.StringVar(&r.RunID, "run-id", "", "opaque id of this launch; deregister needs the same value (required)")
	fl.StringVar(&r.Engine, "engine", "", "one of "+strings.Join(registration.Engines, ", ")+" (required)")
	fl.StringVar(&r.Endpoint, "endpoint", "", "engine base URL, e.g. http://127.0.0.1:<port> (required)")
	fl.StringVar(&r.Model, "model", "", "benchmark model slug: the model label (required)")
	fl.StringVar(&r.Backend, "backend", "", "benchmark backend name: the backend label and file name (required)")
	fl.IntVar(&r.Nodes, "nodes", 0, "nodes the server spans (required)")
	fl.IntVar(&r.Issue, "issue", 0, "tracking issue number")
	fl.StringVar(&r.ServedModel, "served-model", "", "model name the engine reports; enables validation")
	fl.StringVar(&r.LogPath, "log-path", "", "absolute path of the ds4 log")
	fl.StringVar(&r.TracePath, "trace-path", "", "absolute path of the MTPLX decode trace")
	if err := fl.Parse(args); err != nil {
		return exitUsage
	}
	log := logger(out, "info")
	if err := r.Validate(); err != nil {
		log.Error("invalid registration", "err", err)
		return exitUsage
	}
	if err := registration.Write(*dir, r); err != nil {
		log.Error("register failed", "err", err)
		return exitError
	}
	log.Info("registered", "backend", r.Backend, "engine", r.Engine, "model", r.Model, "run_id", r.RunID, "dir", *dir)
	return exitOK
}

func deregister(args []string, out io.Writer) int {
	fl := flag.NewFlagSet("deregister", flag.ContinueOnError)
	fl.SetOutput(out)
	dir := fl.String("registration-dir", registration.DefaultDir(), "directory of arm registrations")
	backend := fl.String("backend", "", "backend name (required)")
	runID := fl.String("run-id", "", "run_id given to register (required)")
	if err := fl.Parse(args); err != nil {
		return exitUsage
	}
	log := logger(out, "info")
	if *backend == "" || *runID == "" {
		log.Error("--backend and --run-id are required")
		return exitUsage
	}
	err := registration.Remove(*dir, *backend, *runID)
	switch {
	case err == nil:
		log.Info("deregistered", "backend", *backend, "run_id", *runID)
		return exitOK
	case errors.Is(err, fs.ErrNotExist):
		log.Info("already deregistered", "backend", *backend, "run_id", *runID)
		return exitOK
	case errors.Is(err, registration.ErrRunIDMismatch):
		log.Warn("left in place: a newer run owns this registration", "err", err)
		return exitRunIDMismatch
	default:
		log.Error("deregister failed", "err", err)
		return exitError
	}
}
