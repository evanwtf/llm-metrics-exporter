# Design

Decided in [evanwtf/local-llm#675](https://github.com/evanwtf/local-llm/issues/675)
on 2026-09-22 and tightened by the design review in
[#1](https://github.com/evanwtf/llm-metrics-exporter/issues/1). Open points are
marked **open**.

**Language: Go.** The review decided it. Operationally this is closer to
`node_exporter` than to a benchmark script:

- `client_golang` and `expfmt` are first-class;
- the footprint is small and long-running;
- context timeouts are cheap;
- it ships as one release binary per platform (Linux arm64, Linux amd64, macOS arm64) under systemd or launchd;
- there is no Python runtime to drift on inference hosts.

local-llm stays Python: orchestration, the ledger, analysis.

## Architecture

```
vLLM / llama.cpp / SGLang / Ollama / ds4 / mlx-serve / MTPLX
        │  each read in its own dialect
        ▼
llm-metrics-exporter  :9109/metrics     one per host; starts with the machine
        │  scrape
        ▼
Prometheus Agent (same host)
        │  remote_write, outbound only
        ▼
central Prometheus  ──►  Grafana, and the benchmark harness
```

- **One exporter per host**, started with the machine, not with the arm, so it
  sees load phases and restarts too.
- **No Pushgateway.** It suits batch jobs. For a host that is always on, it
  leaves stale series behind, and a stale tok/s is worse than a missing one.
- **`remote_write`**, not central scraping. The host needs only an outbound
  connection, and adding an arm needs no change on the monitoring host.

## Metric schema

Counters are canonical. Rates are computed in PromQL.

```
llm_tokens_total{phase="prefill|decode", engine, model, backend, host, nodes}
llm_phase_seconds_total{phase="prefill|decode", <same labels>}
llm_requests_total{<labels>, status}
llm_spec_draft_tokens_total{<labels>}       # speculative decoding, where the engine has it
llm_spec_accepted_tokens_total{<labels>}
llm_requests_running{<labels>}
llm_kv_cache_usage_ratio{<labels>}
llm_time_to_first_token_seconds             # histogram
llm_engine_up{<labels>}                     # see Health
llm_exporter_scrape_errors_total{<labels>}
llm_exporter_last_success_timestamp_seconds{<labels>}
llm_exporter_adapter_info{engine, adapter_version} 1
```

Queries:

```promql
# decode tok/s
sum by (host, engine, model) (rate(llm_tokens_total{phase="decode"}[1m]))
# prefill tok/s
sum by (host, engine, model) (rate(llm_tokens_total{phase="prefill"}[1m]))
```

**No `llm_tokens_per_second` gauge in v1.** Two throughput values computed
over different windows would disagree. Grafana uses `rate()`, and the harness
uses counter deltas.

**Aggregate vs per-stream.** `rate(llm_tokens_total)` is **aggregate**
throughput across every concurrent request. If an engine's phase seconds are
summed per request, requests that overlap in time are all counted, so
`rate(tokens) / rate(phase_seconds)` is **per-stream** throughput. Both are
useful, and they are different numbers. Each adapter states which one its
seconds support (see *Semantic equivalence*).

### Counter ownership

Sources come in two kinds, and they restart differently:

- **Pass-through counters** (vLLM, llama.cpp, likely SGLang): the engine
  exposes a cumulative counter, and the exporter renames and relabels it
  without re-accumulating. An engine restart appears as a counter reset.
- **Exporter-owned counters** (Ollama response stats, ds4 timing events,
  MTPLX trace events, likely mlx-serve): the source emits per-request or
  per-event observations, and the exporter accumulates them. An **exporter**
  restart resets them.

**Resets are strict.** PromQL `rate()` handles a reset. The harness treats
`end < start` as an invalid delta and rejects the trial's throughput figure.
There is **no persisted counter state** to survive an exporter restart: a
benchmark crossed by a restart is invalid, and that is simpler than
persistence.

### Semantic equivalence

Canonical metrics normalize **only engine measurements with equivalent
semantics**. Every adapter documents, for each canonical series, the upstream
metric or event it comes from and **the interval it measures**. That might be
GPU compute time, request wall-clock evaluation time, or summed batch-cycle
time; under concurrency these are not interchangeable. Measurements whose
semantics differ materially are **never** silently mapped to the same counter.
They get a distinct series, or they are omitted with the reason documented.
This matters most for `prefill`/`decode` tokens and seconds, which feed
published benchmark numbers.

## Labels

| label | required | value | why |
|---|---|---|---|
| `engine` | **yes** (operator) | `vllm`, `llamacpp`, `sglang`, `ollama`, `ds4`, `mlx-serve`, `mtplx` | the thing being compared |
| `model` | **yes** (operator) | the benchmark's model slug, as the arm registers it | joins to benchmark rows and heartbeats |
| `backend` | yes | the benchmark's backend name | two arms can share engine and model and differ only in flags. Without this their series merge |
| `host` | yes | the host serving the API | where it ran |
| `nodes` | yes | `1` or `2` | a two-node server exposes metrics on its head only, so the series has to say it spans two |

**No unbounded labels.** Quantization, context size, batch size, GPU type and
the like go in an info-style series (`llm_arm_info{backend,...} 1`), not on
every sample.

## Adapters

Adapters are either pull-based or event-driven, so the interface covers both:

```go
type Adapter interface {
    Start(ctx context.Context) error                // begin tailing a log or trace, or no-op for pull adapters
    Collect(ctx context.Context) ([]Metric, error)  // canonical names only
    Close() error
}
```

- **Stateless, pass-through:** vLLM, llama.cpp, SGLang. `Collect` reads `/metrics`.
- **Stateful, accumulating:** Ollama, ds4, MTPLX, probably mlx-serve. `Start`
  begins observing, and `Collect` returns the running totals.

| adapter | mechanism | difficulty |
|---|---|---|
| vLLM | GET `/metrics`, parse, rename | trivial |
| llama.cpp | GET `/metrics` (needs `--metrics`), rename | trivial |
| SGLang | GET `/metrics` (needs `--enable-metrics`), rename; the accept rate may need its log | easy to moderate |
| Ollama | observe responses; evaluate the existing third-party exporter first | moderate |
| ds4 | tail the timing log | moderate |
| MTPLX | tail the decode-trace JSONL | moderate |
| mlx-serve | unknown | **open** |

## Discovery: arms register themselves

Ports vary by recipe, and some launchers are third-party scripts. So the
exporter is not configured with ports. Whatever launches a model writes a
**registration file** on its own host, and deletes it at stop:

```yaml
# <registration dir>/<backend>.yaml
version: 1
run_id: <opaque, written by the launcher>
engine: vllm
endpoint: http://127.0.0.1:<port>
model: <model slug>
backend: <backend name>
nodes: 2
issue: 648            # optional: the tracking issue in local-llm
```

- **`version: 1` is required** in every registration, so fields can be added later
  (`metrics_endpoint`, `log_path`, `trace_path`, `adapter_options`) without an
  ambiguous migration.
- **The registration is the authoritative identity.** `model` and `backend` labels
  come from it. The model name the engine reports is used only to **validate**.
  A mismatch is logged and exported (`llm_registration_mismatch{...} 1`), never
  used to relabel.
- Each registration carries an internal **`run_id`**, which is not a Prometheus
  label. Writes are **atomic** (write a temp file, then rename). Deletion is
  **identity-aware**: a cleanup removes a file only if its `run_id` matches, so a
  late stop cannot delete a newer arm's registration.

The exporter re-reads the directory on every scrape.

### Health

`llm_engine_up 1` means **the exporter got telemetry from this registered engine
on its most recent collection attempt**, not merely that the HTTP port answered.
Anything else is `0`, with the labels intact, so a missing metric is visible and
alertable instead of an empty panel. `llm_exporter_scrape_errors_total` and
`llm_exporter_last_success_timestamp_seconds` say why and since when.

## The benchmark contract

The local-llm harness reads the exporter's counters for the trial's `backend`
at trial start and trial end, and writes into the row:
`prefill_tokens`, `prefill_seconds`, `decode_tokens`, `decode_seconds`, and
`spec_drafted` / `spec_accepted` where they exist. The harness must refuse a
delta across a counter reset. That contract lives in local-llm. This repo's
job is to make the counters exist and be correct.

## Rollout

1. Enable the remote-write receiver on the central Prometheus.
2. The exporter with vLLM, SGLang and llama.cpp adapters, and a Prometheus
   Agent, on both Sparks. The two-node arms are the ones recording nothing
   today.
3. The harness counter deltas in local-llm rows.
4. Ollama, then the Mac engines (ds4, MTPLX, mlx-serve).
5. Retire the per-engine scrape jobs in the central config.

## Repository layout

```
cmd/llm-metrics-exporter/main.go
internal/adapter/{adapter.go, vllm, llamacpp, sglang, ollama, ds4, mtplx, mlxserve}
internal/registration/
internal/collector/
internal/metrics/
testdata/<engine>/        # captured real output, engine version recorded
```

## Open decisions

- **Service management.** systemd on Linux, launchd on macOS. Where the
  registration directory lives on each OS.
- **SGLang.** Its counter names, whether there is a decode-seconds counter, and
  where the speculative accept rate lives, verified against a live `/metrics`.
- **Ollama.** Proxy the requests, or rely on the existing exporter.
- **mlx-serve.** What it exposes at all.
