# Quickstart

Run from the cloned repository root. Choose native or Docker, not both on the
same port. Start a supported local engine with metrics enabled: vLLM,
llama.cpp (`--metrics`), or SGLang (`--enable-metrics`). This exporter observes
engines; it never launches models or sends inference requests.

## Native: Linux or macOS

Requires Go from `go.mod` and Make (macOS discovery also uses system `lsof`):

```sh
make build
./dist/llm-metrics-exporter serve --listen 127.0.0.1:9109
```

No engine, model, port or registration setting is required. Leave the exporter
running while starting/stopping engines, including on different local ports.
It rediscovers on each collection. Ctrl-C stops only the exporter.
If Go is not on PATH, use `make build GO="$HOME/go/bin/go"`. Without Make:
`go build -trimpath -o dist/llm-metrics-exporter ./cmd/llm-metrics-exporter`.
For writable-cache problems see [build troubleshooting](docs/cheat-sheet.md#1-build).

## Docker Compose: Linux

Requires Docker with Compose, but no host Go/Make. With fresh/default settings:

```sh
cp -n .env.example .env
mkdir -p "${HOME}/.local/state/llm-metrics-exporter/registrations"
docker compose up -d --build exporter
docker compose ps exporter
```

Compose reads `.env` automatically; no shell exports or per-engine settings
are needed. For an existing `.env`, create the directory named by
`LLM_EXPORTER_REGISTRATION_DIR` instead if overridden. Set a stable, unique
`LLM_EXPORTER_HOST` in `.env`; host networking does not share the host's hostname.
Set `LLM_EXPORTER_LISTEN=127.0.0.1:9109` for a loopback-only listener; the default
is all interfaces and has no TLS/authentication. Restrict access to trusted clients.
The registration directory can be empty and is mounted read-only for optional
pins. The nonroot image needs read/traverse access. macOS must use the native
binary: Docker's VM does not see host loopback listeners.

## Confirm collection and switch engines

In another terminal (requires curl):

```sh
curl -fsS --max-time 10 http://127.0.0.1:9109/metrics |
  grep -E '^llm_(engine_up|discovery_status|metric_available|tokens_total|prompt_cached_tokens_total)\{'
```

An identified supported engine has `llm_engine_up 1`, `state="ready"`, its
observed engine/model labels, and available token counters. Idle counters can
be zero or flat. Unavailable measurements are absent, not fabricated zeroes.
Stop vLLM and start llama.cpp with metrics enabled; repeat the same curl.
No exporter restart, registration rewrite or `.env` edit is needed.
The URL stays fixed; counters and identity change to the new engine.

This works for loopback/wildcard listeners visible to this network namespace,
not arbitrary LAN hosts. Model-less telemetry needs a single model from
`/v1/models`. Node count defaults to `unknown`: for a stable two-node deployment
use `--discovery-nodes=2` (native) or `LLM_EXPORTER_DISCOVERY_NODES=2` in `.env`
(Compose). This asserts topology for **all** discovered engines, not just one;
leave unknown if they differ. RDMA/tensor-parallel size cannot prove node count.

For diagnostic states, scope limits, migration from pins, and engine-independent
tok/s queries with recovery warm-up, read [automatic discovery](docs/discovery.md).
`/healthz` checks only exporter liveness.

## Next steps

- [Prometheus scraping and services](docs/prometheus-setup.md): keep one scrape
  target for the exporter, regardless of the engine. Remote scrapers need a
  reachable, access-restricted listener.
- [Remote write](docs/remote-write.md): outbound delivery and persistent offline
  buffering. Do not also scrape the same series into that receiver without
  handling duplicates. Discovery runs on sender collection even without scrapes.
- [Pinned-registration quickstart](docs/pinned-quickstart.md): preserved manual
  setup, Docker registration commands and run-ID-aware cleanup.
- [CLI reference](docs/cli.md), [adapter limitations](docs/adapters.md),
  [live-test cheat sheet](docs/cheat-sheet.md), and [development](docs/development.md).
