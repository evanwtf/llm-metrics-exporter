# Design

Decided in [evanwtf/local-llm#675](https://github.com/evanwtf/local-llm/issues/675)
on 2026-09-22 and tightened by the design review in
[#1](https://github.com/evanwtf/llm-metrics-exporter/issues/1). Revised on
2026-09-22 after the engines' real output was read (see
[`findings.md`](findings.md)). Open points are marked **open**.

**Language: Go.** The review decided it. Operationally this is closer to
`node_exporter` than to a benchmark script:

- `client_golang` and `expfmt` are first-class;
- the footprint is small and long-running;
- context timeouts are cheap;
- it ships as one static binary per platform (Linux amd64, Linux arm64,
  macOS arm64, macOS amd64) under systemd or launchd;
- there is no Python runtime to drift on inference hosts.

local-llm stays Python: orchestration, the ledger, analysis.

## Architecture

The diagram shows the original Prometheus Agent topology. The delivery modes
below also include direct scraping and the now-implemented built-in sender;
the listed engines include planned adapters, not only compiled ones.

```
vLLM / llama.cpp / SGLang / mlx-serve / Ollama / ds4 / MTPLX
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
- Delivery is configurable: direct scraping, the built-in remote-write sender,
  or a separate Prometheus Agent. Outbound delivery needs only a connection to
  the receiver, and adding an arm needs no monitoring-host change. See
  [remote-write.md](remote-write.md) for buffering and receiver setup.

## Metric schema

Counters are canonical. Rates are computed in PromQL. `<id>` is the identity
label set: `engine, model, backend, host, nodes` (see *Labels*).

**Names.** `llme_` (LLM Metrics Exporter) is the namespace; it is distinct
enough that a query for another tool's `llm_*` series cannot match it.
Engine measurements, the series an adapter emits, are `llme_*`. The exporter's
own state (registrations, discovery, collection health, availability) is
`llme_exporter_*`, with one exception: `llme_engine_up`, the conventional
`*_up` health series. `internal/metrics` enforces this split in a test.
The schema shorthand below omits `worker` on engine measurements; those
measurements also carry that label, as specified in *Labels*. Health and
provenance series do not acquire worker identity by implication.

```
# tokens
llme_tokens_total{<id>, phase="prefill"}                 prompt tokens the engine COMPUTED (cache hits excluded)
llme_tokens_total{<id>, phase="decode"}                  tokens generated
llme_prompt_cached_tokens_total{<id>}                    prompt tokens reused from a cache instead of computed

# time: two clocks, never mixed (see "Two clocks")
llme_request_phase_seconds_total{<id>, phase}            sum of each request's own phase interval
llme_engine_phase_seconds_total{<id>, phase}             engine wall time in the phase, no overlap

# requests and state
llme_requests_total{<id>, status}                        finished requests, by finish reason
llme_requests_running{<id>}
llme_kv_cache_usage_ratio{<id>}                          0..1
llme_time_to_first_token_seconds{<id>}                   histogram, engine's own buckets

# speculative decoding, where the engine counts it
llme_spec_draft_tokens_total{<id>}                       draft tokens proposed (never the free first token)
llme_spec_accepted_tokens_total{<id>}                    draft tokens the target kept
llme_spec_verify_steps_total{<id>}                       verification steps that proposed at least one token

# health and provenance
llme_engine_up{<id>}                                     1 = telemetry obtained on the last attempt
llme_exporter_registration_mismatch{<id>}                1 = engine serves a model other than the registered one
llme_exporter_arm_info{<id>, issue, adapter_version, exporter_version} 1
llme_exporter_scrape_errors_total{<id>}
llme_exporter_last_success_timestamp_seconds{<id>}
llme_exporter_registration_invalid{engine, model, host, file} 1    engine/model: the file's own, or "unknown"
llme_exporter_telemetry_backlog_bytes{<id>}              unread log bytes; counters withheld until caught up
llme_exporter_metric_available{<id>, metric, phase}      1 = valid token measurement present, 0 = unavailable

# automatic discovery (see discovery.md)
llme_exporter_discovery_status{<id>, state}              1 for the current discovery state
llme_exporter_discovery_changed_timestamp_seconds{<id>}  last identity/listener/recovery transition; exclude rate windows crossing it
```

**Every series carries `engine` and `model`** (operator requirement,
2026-09-22). So there is no exporter-wide series: the adapter and exporter
versions ride on each arm's `llme_exporter_arm_info`, and `/metrics` has no Go runtime
or process series. `up{job="llm-metrics-exporter"}`, which the Prometheus
Agent writes about the scrape itself, is the one series without them, and it
is what says the exporter is down.

Queries:

```promql
# decode tok/s, aggregate across concurrent requests
sum by (host, engine, model, backend) (rate(llme_tokens_total{phase="decode"}[1m]))
# prefill tok/s (computed tokens only)
sum by (host, engine, model, backend) (rate(llme_tokens_total{phase="prefill"}[1m]))
# prefix-cache share of prompt tokens
rate(llme_prompt_cached_tokens_total[5m])
  / (rate(llme_prompt_cached_tokens_total[5m]) + ignoring (phase) rate(llme_tokens_total{phase="prefill"}[5m]))
```

**No `llme_tokens_per_second` gauge in v1.** Two throughput values computed
over different windows would disagree. Grafana uses `rate()`, and the harness
uses counter deltas.

### Prefill tokens are computed tokens

The engines disagree on what "prompt tokens" means. vLLM, SGLang and mlx-serve
count every prompt token in `prompt_tokens_total`, cached or not. llama.cpp
counts only the tokens it evaluated. On an agent workload most prompt tokens are
prefix-cache hits: one stored vLLM series has 363,231 computed and 4,666,384
cached. Dividing all prompt tokens by prefill time overstates prefill speed by
more than ten times.

So `phase="prefill"` is **computed** tokens, on every engine, and cache hits go
to `llme_prompt_cached_tokens_total`. Each adapter reads the engine's own
computed-token counter. No adapter derives it by subtraction.

### Two clocks

An engine's phase time is one of two different intervals:

- **Request clock** (`llme_request_phase_seconds_total`). Each request's own
  interval, summed over requests. Two requests that overlap are both counted,
  so `Δtokens / Δseconds` is **per-stream** speed.
- **Engine clock** (`llme_engine_phase_seconds_total`). Wall time the engine
  spent in the phase. Overlap is counted once, so `Δtokens / Δseconds` is
  **aggregate** speed.

At concurrency 1, which is how the benchmark harness runs, the two agree to
within scheduling overhead. Under load they differ by up to the concurrency.
They are separate metric names so that no `sum()` can merge them. The harness
records which clock a row's seconds came from.

### Counter ownership

Sources come in two kinds, and they restart differently:

The classifications include intended adapters: mlx-serve and MTPLX are not
implemented, and Ollama's event acquisition method remains open. See the
adapter status table before treating any classification as implemented support.

- **Pass-through counters** (vLLM, llama.cpp, SGLang, mlx-serve): the engine
  exposes a cumulative counter, and the exporter renames and relabels it
  without re-accumulating. An engine restart appears as a counter reset.
- **Exporter-owned counters** (ds4 timing lines, MTPLX trace records, Ollama
  response stats): the source emits per-event observations, and the exporter
  accumulates them. An **exporter** restart resets them, and so does a log that
  shrinks or is replaced.

**Resets are strict.** PromQL `rate()` handles a reset. The harness treats
`end < start` as an invalid delta and rejects the trial's throughput figure.
There is **no persisted counter state** to survive an exporter restart: a
benchmark crossed by a restart is invalid, and that is simpler than
persistence.
This means adapter counter state, not the remote-write queue and series
checkpoint, which are persisted. ds4 rereads existing log history after restart;
its caught-up totals can therefore reappear, but that is a new baseline, not
newly performed work. A restart still invalidates a benchmark crossing it.

**Update timing differs.** Some engine counters move per scheduler iteration,
and some move when a request finishes (see the adapter table). Mid-request, a
token counter can be ahead of its seconds counter. A delta taken between trials,
with no request in flight, is exact only after upstream reporting has caught
up and log backlog is clear. SGLang may lag by one reporting interval, as
documented in the adapter table; idle alone is not sufficient.

### Semantic equivalence

Canonical metrics normalize **only engine measurements with equivalent
semantics**. Every adapter documents, for each canonical series, the upstream
metric or event it comes from, **the interval it measures**, and when it
updates. The per-engine record is [`adapters.md`](adapters.md), and the
adapter's tests pin it against captured output. Measurements whose semantics
differ materially are **never** silently mapped to the same counter. They get a
distinct series, or they are omitted with the reason documented.

## Labels

For automatic targets, the [discovery identity rules](discovery.md#evidence-identity-and-availability)
extend this original pinned-label table: observed engine/model, hashed endpoint
backend, and `nodes="unknown"` unless explicitly asserted. Discovery diagnostics
add `state`; `llme_exporter_discovery_changed_timestamp_seconds` marks rate-window boundaries.

| label | required | value | why |
|---|---|---|---|
| `engine` | **yes** (operator) | `vllm`, `llamacpp`, `sglang`, `mlx-serve`, `ollama`, `ds4`, `mtplx` | the thing being compared |
| `model` | **yes** (operator) | the benchmark's model slug, as the arm registers it | joins to benchmark rows and heartbeats |
| `backend` | yes | the benchmark's backend name | two arms can share engine and model and differ only in flags. Without this their series merge |
| `host` | yes | the host serving the API (`--host`, default: the short hostname) | where it ran |
| `nodes` | yes | integer `1`–`64`; the original fleet examples use `1` or `2` | a two-node server exposes metrics on its head only, so the series has to say it spans two |
| `worker` | engine measurements | upstream worker index, or `default` | preserves independent reset boundaries |

Remote-write delivery adds `job="llm-metrics-exporter"` and `instance=<host>`.
Local `/metrics` leaves those to a scraping Prometheus. Additional labels are
`phase` on token/time series, `status` on request counts, `le` on histogram
buckets, and `issue`, `adapter_version`, `exporter_version` on `llme_exporter_arm_info`.
Availability has `metric` and `phase` (`none` for cached tokens). Invalid-file
diagnostics have `engine`, `model`, `host`, `file`, without deployment labels.

**No unbounded labels.** Quantization, context size, batch size, GPU type and
the like go in an info-style series, not on every sample. `llme_exporter_arm_info` carries
the registration's `issue`.

**Worker reset boundaries are preserved.** Adapter measurements carry `worker`:
vLLM's `engine` index, SGLang's `dp_rank`, or `default` when unpartitioned.
SGLang keeps rank 0 of tensor, pipeline and expert-parallel groups. Apply
`rate()` per worker before summing. Histograms, phase seconds, and gauges retain
the same worker boundary. Cache usage ratios are per worker, never summed.
Absent workers disappear; reappearing workers expose their current counters.
As with any sampled counter, resets entirely between observations can be missed.

## Adapters

```go
type Adapter interface {
    Start(ctx context.Context) error          // open state; no-op for pull adapters
    Collect(ctx context.Context) (Result, error) // canonical names only
    Close() error
}
```

- **Stateless, pass-through:** vLLM, llama.cpp, SGLang, mlx-serve. `Collect`
  reads `/metrics`.
- **Stateful, accumulating:** ds4, MTPLX, Ollama. `Collect` reads the bytes
  appended to a log since the last call, adds them to running totals, and
  also checks that the endpoint answers.

Only ds4 implements that stateful mechanism today. MTPLX is planned; the
original grouping of Ollama with log readers was a proposal, not a resolved
proxy-versus-log decision. mlx-serve is likewise a planned pass-through adapter.

| adapter | mechanism | status |
|---|---|---|
| vLLM | GET `/metrics`, parse, rename | v0.1 |
| llama.cpp | GET `/metrics` (needs `--metrics`), rename; detects the 2026-08-13 metrics rewrite | v0.1 |
| SGLang | GET `/metrics` (needs `--enable-metrics`), rename | v0.1; no phase seconds, no spec counters (see `adapters.md`) |
| ds4 | tail the `DS4_MTP_TIMING` log | v0.1; speculative counters only |
| mlx-serve | GET `/metrics` (needs `--metrics`), rename | next; needs a captured fixture |
| MTPLX | tail the decode-trace JSONL | next; needs a captured fixture |
| Ollama | **open**: no Prometheus surface, and the deployed third-party exporter reports model inventory only | later |

## Discovery: arms register themselves

This section describes **explicit pins**. Since issue #13, the default is
[continuous local discovery](discovery.md), without launcher registration.
Its evidence-based identity, bounded probing, expiry and transition rules are
authoritative for automatic targets; the rules below remain binding for pins.
The original registration-only rationale and policy are retained in
[migration history](discovery.md#pins-and-migration).

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
issue: 648                 # optional: the tracking issue in local-llm
served_model: <name>       # optional: the name the engine reports; enables validation
log_path: /path/to/log     # ds4 only: the stderr log with DS4_MTP_TIMING lines
trace_path: /path/to/jsonl # MTPLX only: MTPLX_DECODE_TRACE_JSONL
```

- **`version: 1` is required.** Unknown fields are an error, so a typo is
  loud and a new field needs a new version.
- **The file name is `<backend>.yaml`**, and the `backend` field must match it.
  One backend per host has one registration, so two files cannot claim the
  same label set.
- The file is YAML. JSON is valid YAML, so a launcher can write JSON.
- **The registration is the authoritative identity.** `model` and `backend`
  labels come from it. If `served_model` is set, the adapter keeps only the
  samples the engine labels with that name. If there are none, the adapter
  exports `llme_exporter_registration_mismatch 1` and `llme_engine_up 0`, and logs the
  names the engine did report. It never relabels. If `served_model` is not set,
  a single model is accepted but multiple models are rejected as ambiguous.
  An upstream without model labels cannot be validated.
- Each registration can carry an internal **`run_id`**, which is not a Prometheus
  label. Writes are **atomic** (write a temp file, then rename). Deletion is
  **identity-aware**: a cleanup removes a file only if its `run_id` matches, so a
  late stop cannot delete a newer arm's registration.
- Static YAML may omit `run_id` (defaults to `static`) and `issue`. `backend`
  is a deployment identifier, not necessarily a benchmark arm. See
  [static targets](static-targets.md) for a launcher-free workflow.
- **The binary writes them.** `llm-metrics-exporter register` and
  `llm-metrics-exporter deregister` implement the atomic write and the
  identity-aware delete, so a launcher in any language gets both by calling it.

The exporter re-reads the directory on every scrape. A file that does not parse
or validate is logged and exported as `llme_exporter_registration_invalid`.
A new `run_id` for a backend restarts that backend's adapter and its
exporter-owned counters.

### Health

`llme_engine_up 1` means **the exporter got telemetry from this identified engine
on its most recent collection attempt**, not merely that the HTTP port answered.
Anything else is `0`, with the labels intact, so a missing metric is visible and
alertable instead of an empty panel. `llme_exporter_scrape_errors_total` and
`llme_exporter_last_success_timestamp_seconds` say why and since when.

`llme_exporter_metric_available{metric,phase}` distinguishes available token measurements
from unavailable ones, independently of reachability. A measured zero is
available. `llme_exporter_telemetry_backlog_bytes > 0` means a log reader is catching up;
counters are withheld and last-success does not advance until it catches up.
Use only caught-up observations as benchmark boundaries. A catch-up snapshot
is a new baseline, not evidence that historical work happened just now.

Only one Collect call can be in flight per deployment. A timed-out call that
ignores cancellation prevents further calls until it returns. Replacement is
deferred and exported as down; Close runs only after Collect returns. Shutdown
does not wait for stuck I/O. A permanently blocked backend stays unavailable
until the I/O finishes or the process restarts, without spawning more calls.

A registration left behind by a crashed launcher or a reboot keeps exporting
`llme_engine_up 0` until someone deregisters it. That is deliberate: the
exporter cannot tell "stopped on purpose" from "died".

Automatic targets instead retain down health only for the bounded interval
specified in [discovery](discovery.md#evidence-identity-and-availability), and
never retain old measurement samples. Use the [transition-aware rate query](discovery.md#throughput-and-reset-boundaries)
for automatic switching: ordinary `rate()` can retain history inside its window.

## Service management

The exporter runs **as the user that launches the engines**, so both sides can
write and read the registration directory without shared-group setup. The
default directory is the same on both operating systems:
`$XDG_STATE_HOME/llm-metrics-exporter/registrations`, which is
`~/.local/state/llm-metrics-exporter/registrations` when `XDG_STATE_HOME` is not
set. `register` and `serve` use the same default, so they agree without
configuration. `--registration-dir` overrides it on both.

- **Linux:** `docker compose` (`compose.yaml`), with host networking and the
  registration directory mounted read-only; or a systemd user unit
  (`packaging/systemd/`). Launchers use the host binary to register.
- **macOS:** a LaunchAgent for that user (`packaging/launchd/`), native. A
  container on macOS runs in a VM and cannot reach the host's engines on
  `127.0.0.1`.

## The benchmark contract

This is the intended contract with the external local-llm project, not evidence
that its integration is deployed. The [plan](plan.md#phase-3-the-harness-reads-counters)
still tracks the work. The normative reset and clock requirements remain.

The local-llm harness reads the exporter's counters for the trial's `backend`
at trial start and trial end, and writes into the row:
`prefill_tokens`, `prefill_cached_tokens`, `prefill_seconds`, `decode_tokens`,
`decode_seconds`, the clock the seconds came from (`request` or `engine`), and
`spec_drafted` / `spec_accepted` / `spec_verify_steps` where they exist. The
harness must refuse a delta across a counter reset. That contract lives in
local-llm. This repo's job is to make the counters exist and be correct.

## Rollout

This is the original 2026-09-22 deployment order, retained as historical intent.
It is not a current fleet status report: ds4 is now compiled and the separate
Agent is optional. See [plan](plan.md) for implementation/deployment distinctions.

1. Enable the remote-write receiver on the central Prometheus.
2. The exporter with vLLM, SGLang and llama.cpp adapters, and a Prometheus
   Agent, on both Sparks. The two-node arms are the ones recording nothing
   today.
3. The harness counter deltas in local-llm rows.
4. mlx-serve, MTPLX and ds4 on the Mac; then Ollama.
5. Retire the per-engine scrape jobs in the central config.

## Repository layout

```
cmd/llm-metrics-exporter/     main: serve, register, deregister, version
internal/adapter/             the interface and the registry of engines
internal/adapter/<engine>/    one package per engine
internal/collector/           prometheus.Collector: registrations -> adapters -> samples
internal/metrics/             the canonical schema; the only place names are defined
internal/promtext/            reading an engine's exposition text
internal/registration/        parse, validate, atomic write, identity-aware delete
internal/tail/                bounded incremental file reading for log adapters
internal/version/             the version, set in one place
testdata/<engine>/            captured real output, with its provenance
packaging/                    systemd, launchd, Prometheus Agent examples
scripts/                      release and repository checks
```

## Open decisions

- **Ollama.** It has no Prometheus endpoint, and the deployed third-party
  exporter reports model inventory only. The choice is a proxy in front of the
  API, or reading the server log. A proxy adds a hop to benchmark requests.
- **SGLang speculative counters.** SGLang exports acceptance as gauges only
  (`spec_accept_rate`, `spec_accept_length`), plus a `spec_verify_calls_total`
  counter whose unit under concurrency is unverified. No counter can be
  derived without guessing.
- **SGLang phase seconds.** There is no per-phase seconds counter.
  `scheduler_stage_seconds_total{category="run_batch"}` is engine-clock forward
  time, but for prefill and decode together.
