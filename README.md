# llm-metrics-exporter

One Prometheus schema for LLM inference throughput, whatever engine is serving.

Every inference host runs one exporter. By default it continuously discovers
local vLLM, llama.cpp and SGLang servers and infers engine/model identity.
Launchers can still pin each model server ("arm") through registrations.
The exporter reads each server in its own
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

## Automatic startup

After cloning, `make build` then
`./dist/llm-metrics-exporter serve --listen 127.0.0.1:9109` is enough for supported
local engines with metrics enabled. Stop vLLM and start llama.cpp—even on a
different local port—and the same exporter URL follows without a restart or
configuration change. See the [quickstart](QUICKSTART.md) for Compose and
[discovery](docs/discovery.md) for scope, model evidence, unknown topology,
diagnostics, and safe rate windows. Unsupported/log-only engines need explicit
configuration or future adapters; automatic does not mean universal support.

## Pinned bootstrap (optional)

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
"$BIN/llm-metrics-exporter" serve --discovery=off &
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

- **Engine settings in `.env`:** use the [Docker quickstart](QUICKSTART.md#docker-compose-linux)
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
These basic queries retain historical samples within the rate window. For
automatic switching, use the [health and transition guards](docs/discovery.md#throughput-and-reset-boundaries)
to exclude unavailable targets and known replacement boundaries.

```promql
sum by (host, engine, model, backend) (rate(llm_tokens_total{phase="decode"}[1m]))   # decode tok/s
sum by (host, engine, model, backend) (rate(llm_tokens_total{phase="prefill"}[1m]))  # prefill tok/s
```

`phase="prefill"` counts only prompt tokens the engine computed; prefix-cache
hits are `llm_prompt_cached_tokens_total`. The full schema is in
[`docs/design.md`](docs/design.md).

Every series below carries `engine`, `model`, `backend`, `host` and `nodes`;
engine measurements also carry `worker`. `llm_registration_invalid` is the
exception: it has `engine`, `model`, `host` and `file` only. Which engine
supplies which series is in [`docs/adapters.md`](docs/adapters.md); check
`llm_metric_available` rather than assuming an absent series means zero.

| metric | type | extra labels | meaning |
|---|---|---|---|
| `llm_tokens_total` | counter | `phase` | prefill (computed only) or decode tokens |
| `llm_prompt_cached_tokens_total` | counter | | prompt tokens served from prefix cache |
| `llm_request_phase_seconds_total` | counter | `phase` | per-request phase time, summed (per-stream clock) |
| `llm_engine_phase_seconds_total` | counter | `phase` | engine wall time in phase (aggregate clock) |
| `llm_requests_total` | counter | `status` | finished requests by finish reason |
| `llm_requests_running` | gauge | | requests in flight |
| `llm_kv_cache_usage_ratio` | gauge | | KV cache usage, 0..1 |
| `llm_time_to_first_token_seconds` | histogram | | TTFT, engine's own buckets |
| `llm_spec_draft_tokens_total` | counter | | speculative draft tokens proposed |
| `llm_spec_accepted_tokens_total` | counter | | draft tokens accepted |
| `llm_spec_verify_steps_total` | counter | | verification steps |
| `llm_engine_up` | gauge | | 1 = telemetry obtained on the last attempt |
| `llm_metric_available` | gauge | `metric`, `phase` | 1 = measurement provided, 0 = unavailable |
| `llm_registration_mismatch` | gauge | | 1 = engine serves a different model than registered |
| `llm_registration_invalid` | gauge | `file` | 1 per registration file that failed to validate |
| `llm_arm_info` | gauge | `issue`, `adapter_version`, `exporter_version` | build and registration metadata, always 1 |
| `llm_exporter_scrape_errors_total` | counter | | failed collection attempts |
| `llm_exporter_last_success_timestamp_seconds` | gauge | | Unix time of last successful collection |
| `llm_telemetry_backlog_bytes` | gauge | | unread log bytes; counters withheld until caught up |
| `llm_discovery_status` | gauge | `state` | 1 for the current discovery state |
| `llm_discovery_changed_timestamp_seconds` | gauge | | last identity/listener transition; exclude rate windows crossing it |

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
| [Automatic discovery](docs/discovery.md) | continuous engine switching, supported scope, identity, limits, safe rate queries |
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
