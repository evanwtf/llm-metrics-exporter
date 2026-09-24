// Command llm-metrics-exporter exports canonical LLM inference metrics for
// every engine registered on this host, and registers or deregisters arms.
//
//	llm-metrics-exporter [serve] [flags]      run the exporter
//	llm-metrics-exporter register [flags]     write a registration atomically
//	llm-metrics-exporter deregister [flags]   remove one, only if run_id matches
//	llm-metrics-exporter health [--url URL]   exit 0 if the exporter answers (container healthcheck)
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
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/evanwtf/llm-metrics-exporter/internal/collector"
	"github.com/evanwtf/llm-metrics-exporter/internal/discovery"
	"github.com/evanwtf/llm-metrics-exporter/internal/engines"
	"github.com/evanwtf/llm-metrics-exporter/internal/registration"
	"github.com/evanwtf/llm-metrics-exporter/internal/remotewrite"
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
	case "health":
		return health(args, out)
	case "version":
		logger(out, "info").Info("llm-metrics-exporter", versionAttrs()...)
		return exitOK
	default:
		logger(out, "info").Error("unknown command; want serve, register, deregister, health or version", "command", cmd)
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

// defaultHost is $LLM_EXPORTER_HOST, or the short hostname.
func defaultHost() string {
	if h := os.Getenv("LLM_EXPORTER_HOST"); h != "" {
		return h
	}
	return shortHostname()
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
	listen             string
	dir                string
	host               string
	timeout            time.Duration
	discovery          string
	discoveryEndpoints string
	discoveryNodes     int
}

func (cfg serveConfig) discoveryOptions() (discovery.Options, error) {
	o := discovery.Options{Timeout: cfg.timeout, Nodes: cfg.discoveryNodes}
	if cfg.discovery != "local" && cfg.discovery != "off" && cfg.discovery != "" {
		return o, errors.New("discovery must be local or off")
	}
	if cfg.discoveryNodes < 0 || cfg.discoveryNodes > 64 {
		return o, errors.New("discovery-nodes must be 0 (unknown) or 1-64")
	}
	if cfg.discovery != "local" {
		if cfg.discoveryEndpoints != "" {
			return o, errors.New("discovery endpoints require local discovery")
		}
		return o, nil
	}
	_, port, err := net.SplitHostPort(cfg.listen)
	numeric, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || numeric < 1 || numeric > 65535 {
		return o, errors.New("automatic discovery requires a fixed nonzero exporter listen port")
	}
	o.ExcludePort = strconv.Itoa(numeric)
	if cfg.discoveryEndpoints != "" {
		var targets []discovery.Target
		for _, raw := range strings.Split(cfg.discoveryEndpoints, ",") {
			t, err := discovery.LocalTarget(strings.TrimSpace(raw))
			if err != nil {
				return o, err
			}
			targets = append(targets, t)
		}
		o.Candidates = func(context.Context) ([]discovery.Target, error) { return targets, nil }
	}
	return o, nil
}

func serve(args []string, out io.Writer) int {
	fl := flag.NewFlagSet("serve", flag.ContinueOnError)
	fl.SetOutput(out)
	var cfg serveConfig
	var rw remotewrite.Options
	fl.StringVar(&cfg.listen, "listen", defaultListen, "address to serve /metrics on")
	fl.StringVar(&cfg.dir, "registration-dir", registration.DefaultDir(), "directory of arm registrations")
	fl.StringVar(&cfg.host, "host", defaultHost(), "value of the host label (default $LLM_EXPORTER_HOST, else the short hostname)")
	fl.DurationVar(&cfg.timeout, "timeout", defaultTimeout, "per-arm collection timeout; keep it below the scrape timeout")
	fl.StringVar(&cfg.discovery, "discovery", "local", "engine discovery: local or off (pinned registrations only)")
	fl.StringVar(&cfg.discoveryEndpoints, "discovery-endpoints", "", "optional comma-separated loopback HTTP base URLs replacing local listener enumeration")
	fl.IntVar(&cfg.discoveryNodes, "discovery-nodes", 0, "assert node count for all automatically discovered engines; 0 means unknown")
	fl.StringVar(&rw.URL, "remote-write-url", "", "optional Prometheus receiver URL, ending /api/v1/write")
	fl.StringVar(&rw.Dir, "remote-write-dir", filepath.Join(filepath.Dir(registration.DefaultDir()), "remote-write"), "persistent remote-write queue directory")
	fl.StringVar(&rw.TokenFile, "remote-write-token-file", "", "optional bearer token file (read on each send)")
	fl.DurationVar(&rw.Interval, "remote-write-interval", 15*time.Second, "remote-write collection interval")
	fl.DurationVar(&rw.Timeout, "remote-write-timeout", 5*time.Second, "remote-write HTTP timeout")
	fl.DurationVar(&rw.MaxAge, "remote-write-max-age", 24*time.Hour, "discard queued samples older than this")
	fl.Int64Var(&rw.MaxBytes, "remote-write-max-bytes", 64<<20, "maximum queued payload bytes; newest snapshot dropped when full")
	level := fl.String("log-level", "info", "debug, info, warn or error")
	if err := fl.Parse(args); err != nil {
		return exitUsage
	}
	log := logger(out, *level)
	slog.SetDefault(log)
	if _, err := cfg.discoveryOptions(); err != nil {
		log.Error("discovery configuration", "err", err)
		return exitUsage
	}
	if cfg.timeout <= 0 || cfg.host == "" {
		log.Error("timeout must be positive and host nonempty")
		return exitUsage
	}
	rw.Host, rw.Version = cfg.host, version.Version
	if rw.URL != "" {
		if err := rw.Validate(); err != nil {
			log.Error("remote-write configuration", "err", err)
			return exitUsage
		}
	}
	if err := os.MkdirAll(cfg.dir, 0o755); err != nil {
		log.Error("cannot create the registration directory", "dir", cfg.dir, "err", err)
		return exitError
	}

	handler, registry, closeCollector := newRegistryHandler(cfg, log)
	defer closeCollector()
	var sender *remotewrite.Sender
	if rw.URL != "" {
		var err error
		sender, err = remotewrite.New(rw, registry, log)
		if err != nil {
			log.Error("remote-write initialization", "err", err)
			return exitError
		}
		defer sender.Close()
		handler.Handle("/remote-write/status", sender)
	}
	srv := &http.Server{Addr: cfg.listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if sender != nil {
		rwctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); sender.Run(rwctx) }()
		defer func() { cancel(); <-done }()
	}
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
	h, _, closeFn := newRegistryHandler(cfg, log)
	return h, closeFn
}

func newRegistryHandler(cfg serveConfig, log *slog.Logger) (*http.ServeMux, *prometheus.Registry, func()) {
	var auto *discovery.Manager
	if cfg.discovery == "local" {
		o, err := cfg.discoveryOptions()
		if err != nil {
			panic("invalid internally constructed discovery configuration")
		}
		auto = discovery.New(o)
	}
	c := collector.New(collector.Options{
		Dir: cfg.dir, Host: cfg.host, Timeout: cfg.timeout,
		Client: &http.Client{}, MaxBody: maxBody, MaxRead: maxRead,
		Adapters: engines.All(), ExporterVersion: version.Version, Logger: log,
		Discovery: auto,
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
		fmt.Fprintf(w, "llm-metrics-exporter %s\n\n/metrics  canonical llme_* series\n/healthz  liveness\n", version.Version)
	})
	return mux, reg, c.Close
}

func register(args []string, out io.Writer) int {
	fl := flag.NewFlagSet("register", flag.ContinueOnError)
	fl.SetOutput(out)
	dir := fl.String("registration-dir", registration.DefaultDir(), "directory of arm registrations")
	r := registration.Registration{Version: registration.Version}
	fl.StringVar(&r.RunID, "run-id", "static", "opaque launch id; use a unique value for lifecycle-managed deployments")
	fl.StringVar(&r.Engine, "engine", "", "one of "+strings.Join(registration.Engines, ", ")+" (required)")
	fl.StringVar(&r.Endpoint, "endpoint", "", "engine base URL, e.g. http://127.0.0.1:<port> (required)")
	fl.StringVar(&r.Model, "model", "", "model identity label (required)")
	fl.StringVar(&r.Backend, "backend", "", "deployment identifier: backend label and file name (required)")
	fl.IntVar(&r.Nodes, "nodes", 1, "physical nodes the server spans")
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

// health GETs the exporter's /healthz. The container image has no shell or
// curl, so its HEALTHCHECK runs the binary itself.
func health(args []string, out io.Writer) int {
	fl := flag.NewFlagSet("health", flag.ContinueOnError)
	fl.SetOutput(out)
	url := fl.String("url", "http://127.0.0.1"+defaultListen+"/healthz", "URL that must answer 200")
	timeout := fl.Duration("timeout", 3*time.Second, "give up after this long")
	if err := fl.Parse(args); err != nil {
		return exitUsage
	}
	log := logger(out, "info")
	client := &http.Client{Timeout: *timeout}
	resp, err := client.Get(*url)
	if err != nil {
		log.Error("unhealthy", "url", *url, "err", err)
		return exitError
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Error("unhealthy", "url", *url, "status", resp.Status)
		return exitError
	}
	return exitOK
}
