# Design

Decided in [evanwtf/local-llm#675](https://github.com/evanwtf/local-llm/issues/675)
on 2026-09-22. Open points are marked **open**.

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
llm_engine_up{<labels>}                     # 0 when a registered server stops answering
```

Queries:

```promql
# decode tok/s
sum by (host, engine, model) (rate(llm_tokens_total{phase="decode"}[1m]))
# prefill tok/s
sum by (host, engine, model) (rate(llm_tokens_total{phase="prefill"}[1m]))
```

A `llm_tokens_per_second` gauge may be exposed for convenience. It is never
the source of a published number.

**A counter that resets** (a server restart) is normal. PromQL `rate()` handles
it, and the harness's delta logic must detect it (end < start) and refuse the
delta rather than record a negative or wrapped value.

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

```
interface EngineAdapter
    discover_model() -> string
    scrape() -> Metrics      # canonical names only
```

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
engine: vllm
endpoint: http://127.0.0.1:<port>
model: <model slug>
backend: <backend name>
nodes: 2
issue: 648            # optional: the tracking issue in local-llm
```

The exporter re-reads the directory on every scrape. A registered arm whose
endpoint does not answer exports `llm_engine_up 0` with its labels, so a
missing metric is visible and alertable instead of an empty panel.

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

## Open decisions

- **Language: Go or Python.** Leaning **Go**:
  - one static binary per platform (Linux aarch64 and x86_64, macOS arm64), with no virtualenv to drift on each host;
  - Prometheus's own client and text-format parser (`client_golang`, `expfmt`);
  - a small resident footprint on hosts where GPU memory is host memory.

  For Python: every script in local-llm is Python, and the ds4/MTPLX log readers exist there already. Those readers are about a hundred lines each, though, and the harness only talks to the exporter over HTTP.
- **Service management.** systemd on Linux, launchd on macOS. Where the
  registration directory lives on each OS.
- **SGLang.** Its counter names, whether there is a decode-seconds counter, and
  where the speculative accept rate lives, verified against a live `/metrics`.
- **Ollama.** Proxy the requests, or rely on the existing exporter.
- **mlx-serve.** What it exposes at all.
