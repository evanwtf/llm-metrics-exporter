# llm-metrics-exporter

One Prometheus schema for LLM inference throughput, whatever engine is serving.

Every inference host runs one exporter. Launchers register each model server
("arm") they start. The exporter reads each registered server in its own
dialect (a native `/metrics` endpoint, or a log) and exposes the result under
one set of canonical `llm_*` counters. Every per-deployment series carries
`engine`, `model`, `backend`, `host` and `nodes`; measurements also carry `worker`
to preserve independent resets. Static YAML targets work without a launcher.
Deliver metrics through direct scraping, optional built-in remote write (with
a persistent offline queue), or a separate Prometheus Agent. Grafana and the
benchmark harness can read the same counters; external harness integration
remains tracked in the [plan](docs/plan.md).

Start with the [quickstart](QUICKSTART.md) for native or Docker commands after
cloning. For vLLM service installation and scraping, see
[exporter and Prometheus setup](docs/prometheus-setup.md).
For laptops or changing IP addresses, use [built-in remote write](docs/remote-write.md).
No launcher is required: [static YAML targets](docs/static-targets.md) also work.

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
mkdir -p "$BIN"
go build -o "$BIN/llm-metrics-exporter" ./cmd/llm-metrics-exporter
"$BIN/llm-metrics-exporter" --version

# Register the arm. The launcher chooses the run id and keeps it for deregister.
"$BIN/llm-metrics-exporter" register --backend demo --engine llamacpp \
  --endpoint "$ENGINE_URL" --model demo-model --nodes 1 --run-id demo-1

# Serve on :9109, then read it.
"$BIN/llm-metrics-exporter" serve &
curl -s http://127.0.0.1:9109/metrics | grep '^llm_'

# Stop observing the arm (does not stop the model server or exporter).
"$BIN/llm-metrics-exporter" deregister --backend demo --run-id demo-1
```

With no engine at `$ENGINE_URL`, the arm still appears, as
`llm_engine_up{...} 0`. Unsupported token measurements are unavailable, not zero;
check `llm_metric_available` separately from engine health. The default listener
is all interfaces, without built-in TLS/authentication; restrict access to
trusted clients, or use `--listen 127.0.0.1:9109` for local-only operation.

To run it as a service:

- **Engine settings in `.env`:** use the [Docker quickstart](QUICKSTART.md#2b-docker-compose-linux)
  and [Compose reference](docs/compose.md). `docker compose run --rm --build register`
  reads engine settings from `.env`; no shell exports are needed.
- **Linux, Docker:** `docker compose up -d` (see [`compose.yaml`](compose.yaml)).
  It uses host networking, so the `127.0.0.1` endpoints in registrations are the
  host's, and mounts the host's registration directory read-only. Create that
  directory first: `mkdir -p ~/.local/state/llm-metrics-exporter/registrations`.
  `docker compose --profile agent up -d` adds a Prometheus Agent. Launchers
  still register with the host binary.
  Set `LLM_EXPORTER_HOST` explicitly for stable container identity; host
  networking shares endpoints, not the host's hostname.
- **Linux, no Docker:** the systemd user unit in [`packaging/systemd`](packaging/systemd).
- **macOS:** run natively with the LaunchAgent in [`packaging/launchd`](packaging/launchd).
  A container on macOS runs in a VM and cannot see the host's engines.

For release archive contents, macOS quarantine handling, development checks
and hook installation, see [development and releases](docs/development.md).
With Make installed, `make help` lists the optional shortcuts: `make build`,
`make test`, `make race`, and `make check`. An explicit Go installation works
with `make build GO="$HOME/go/bin/go"`; plain Go commands remain supported.

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
| [CLI reference](docs/cli.md) | commands, flags, environment, HTTP endpoints and exit codes |
| [Development](docs/development.md) | checks, implementation map, hooks, manual releases and documentation maintenance |
| [Security](docs/security.md) | private configuration, runtime credentials and publication checks |
| [`docs/static-targets.md`](docs/static-targets.md) | static deployments, token semantics, and measurement availability |
| [`docs/remote-write.md`](docs/remote-write.md) | enable the Prometheus receiver for outbound delivery |
| [`docs/prometheus-setup.md`](docs/prometheus-setup.md) | register vLLM, run the exporter as a service, and configure direct Prometheus scraping |
| [`docs/cheat-sheet.md`](docs/cheat-sheet.md) | build and live-test against vLLM, verify token deltas, and clean up |
| [`docs/problem.md`](docs/problem.md) | why this exists, and what "done" means |
| [`docs/environment.md`](docs/environment.md) | historical fleet and monitoring context, not a current deployment inventory |
| [`docs/design.md`](docs/design.md) | architecture, metric schema, labels, adapters, registration |
| [`docs/adapters.md`](docs/adapters.md) | per engine: the source, clock and update timing of every series |
| [`docs/findings.md`](docs/findings.md) | what each engine was observed to export, with evidence |
| [`docs/plan.md`](docs/plan.md) | what is done, what is next |
| [`docs/changelog.md`](docs/changelog.md) | release notes and unreleased changes, with reasons |
| [Documentation audit](docs/documentation-audit.md) | preservation mapping and historical corrections |
| [`AGENTS.md`](AGENTS.md) | rules for anyone, human or agent, working in this repo |

Origin: [evanwtf/local-llm#675](https://github.com/evanwtf/local-llm/issues/675).
local-llm is the benchmark project that needs these numbers; this repo is the
collector it (and anything else) reads them from.

## Releases

Publishing is manual. Nothing publishes on a push or a tag.

Follow the [release procedure](docs/development.md#releases) for version and
changelog prerequisites, platform archives and workflow checks. The current
changelog marks 0.1.0 unreleased.

When contributing documentation, update the authoritative guide; keep entry
points short and place lengthy reference material and incidents in supporting docs.

## License

[MIT](LICENSE)
