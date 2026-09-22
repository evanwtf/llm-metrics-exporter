# llm-metrics-exporter

One Prometheus schema for LLM inference throughput, whatever engine is serving.

Every inference host runs one exporter. Launchers register each model server
("arm") they start. The exporter reads each registered server in its own
dialect (a native `/metrics` endpoint, or a log) and exposes the result under
one set of canonical `llm_*` counters. Every series carries `engine`, `model`,
`backend`, `host` and `nodes`. A Prometheus Agent on the same host scrapes it
and `remote_write`s to a central Prometheus. Grafana and the benchmark harness
both read the same counters.

## Bootstrap

From a machine with Go (the version in `go.mod`) to a running exporter that
reads a registered llama.cpp server. Linux (amd64, arm64) and macOS (arm64,
amd64).

```sh
export SRC=$HOME/src/llm-metrics-exporter
export BIN=$HOME/.local/bin
export ENGINE_URL=http://127.0.0.1:8080   # a llama-server started with --metrics

git clone https://github.com/evanwtf/llm-metrics-exporter "$SRC"
cd "$SRC"
go test ./...
go build -o "$BIN/llm-metrics-exporter" ./cmd/llm-metrics-exporter
"$BIN/llm-metrics-exporter" --version

# Register the arm. The launcher chooses the run id and keeps it for deregister.
"$BIN/llm-metrics-exporter" register --backend demo --engine llamacpp \
  --endpoint "$ENGINE_URL" --model demo-model --nodes 1 --run-id demo-1

# Serve on :9109, then read it.
"$BIN/llm-metrics-exporter" serve &
curl -s http://127.0.0.1:9109/metrics | grep '^llm_'

# Stop the arm.
"$BIN/llm-metrics-exporter" deregister --backend demo --run-id demo-1
```

With no engine at `$ENGINE_URL`, the arm still appears, as
`llm_engine_up{...} 0`. A missing metric is never silent.

To run it as a service:

- **Linux, Docker:** `docker compose up -d` (see [`compose.yaml`](compose.yaml)).
  It uses host networking, so the `127.0.0.1` endpoints in registrations are the
  host's, and mounts the host's registration directory read-only. Create that
  directory first: `mkdir -p ~/.local/state/llm-metrics-exporter/registrations`.
  `docker compose --profile agent up -d` adds a Prometheus Agent. Launchers
  still register with the host binary.
- **Linux, no Docker:** the systemd user unit in [`packaging/systemd`](packaging/systemd).
- **macOS:** run natively with the LaunchAgent in [`packaging/launchd`](packaging/launchd).
  A container on macOS runs in a VM and cannot see the host's engines.

Prebuilt binaries: each GitHub release has one zip per platform
(`llm-metrics-exporter-<version>-<os>-<arch>.zip`, for darwin/linux and
arm64/amd64) with the binary, LICENSE and README. On macOS, a zip downloaded
in a browser is quarantined, and the unsigned binary will not run until you
clear it: `xattr -d com.apple.quarantine llm-metrics-exporter`.

For development, install the hooks once: `pre-commit install` (for example
with `uvx pre-commit install`).

## What it exports

Rates are PromQL over counters; there is no tok/s gauge.

```promql
sum by (host, engine, model, backend) (rate(llm_tokens_total{phase="decode"}[1m]))   # decode tok/s
sum by (host, engine, model, backend) (rate(llm_tokens_total{phase="prefill"}[1m]))  # prefill tok/s
```

`phase="prefill"` counts only prompt tokens the engine computed; prefix-cache
hits are `llm_prompt_cached_tokens_total`. The full schema is in
[`docs/design.md`](docs/design.md).

| engine | source | status |
|---|---|---|
| vLLM | `/metrics` | yes |
| llama.cpp | `/metrics`, with `--metrics` | yes |
| SGLang | `/metrics`, with `--enable-metrics` | yes; no phase seconds or speculative counters upstream |
| ds4 | the `DS4_MTP_TIMING` log | yes; speculative counters |
| mlx-serve | `/metrics`, with `--metrics` | planned |
| MTPLX | the decode-trace JSONL | planned |
| Ollama | open | planned |

## Docs

| doc | read it for |
|---|---|
| [`docs/problem.md`](docs/problem.md) | why this exists, and what "done" means |
| [`docs/environment.md`](docs/environment.md) | the fleet, the engines, and the monitoring stack it plugs into |
| [`docs/design.md`](docs/design.md) | architecture, metric schema, labels, adapters, registration |
| [`docs/adapters.md`](docs/adapters.md) | per engine: the source, clock and update timing of every series |
| [`docs/findings.md`](docs/findings.md) | what each engine was observed to export, with evidence |
| [`docs/plan.md`](docs/plan.md) | what is done, what is next |
| [`docs/changelog.md`](docs/changelog.md) | what shipped, and why; the source of release notes |
| [`AGENTS.md`](AGENTS.md) | rules for anyone, human or agent, working in this repo |

Origin: [evanwtf/local-llm#675](https://github.com/evanwtf/local-llm/issues/675).
local-llm is the benchmark project that needs these numbers; this repo is the
collector it (and anything else) reads them from.

## Releases

Publishing is manual. Nothing publishes on a push or a tag.

1. Set the version in `internal/version/version.go` and add its section to
   `docs/changelog.md`. Commit and push.
2. Run the publish workflow: **Actions → publish → Run workflow**, or
   `gh workflow run publish.yml -f tag=v0.1.0`.

The workflow refuses a tag that disagrees with `internal/version`, a tag that
already exists, and a version with no changelog section. It tests, builds the
four zips, runs the linux/amd64 binary out of its zip, and creates the tag and
the release, with the changelog section as the notes.

## License

MIT
