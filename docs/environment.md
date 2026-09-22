# The environment

This repo is public. It describes the fleet by role and hardware class only.
Hostnames, addresses and ports belong in per-host configuration, never here
(see [`AGENTS.md`](../AGENTS.md)).

## The fleet

| role | hardware | OS / arch | engines that run there |
|---|---|---|---|
| **cluster head** | NVIDIA DGX Spark: GB10, 128 GB unified CPU/GPU memory | Linux aarch64 | vLLM, SGLang, llama.cpp, Ollama, ds4 |
| **cluster worker** | a second DGX Spark, joined to the head by 200 Gb/s ConnectX-7 RoCE | Linux aarch64 | rank 1 of a two-node server; also single-node work |
| **Mac** | MacBook Pro, M5 Max, 128 GB unified memory | macOS arm64 | ds4, llama.cpp, mlx-serve, MTPLX, Ollama |
| **desktop** | Ryzen 9 7900X + RTX 3080 Ti 12 GB | Linux x86_64 | llama.cpp, Ollama |
| **benchmark client** | Core i3-7100, 16 GB | Linux x86_64 | none. It runs the agent harness against a server over the LAN |

Things that shape the design:

- **Unified memory.** On the Sparks and the Mac, GPU memory *is* host memory.
  A model server can leave the host a few GiB of headroom, and the kernel OOM
  killer takes small daemons first. The exporter must be small and must never
  buffer unboundedly.
- **Two-node servers.** A model can be tensor-parallel across both Sparks.
  The API, and the engine's metrics endpoint, are on the head only. The worker
  runs a rank with no endpoint of its own.
- **Remote clients.** The agent harness usually runs on a separate machine
  from the server, so anything the harness needs from the server has to be
  readable over HTTP.
- **Servers come and go.** An arm runs for tens of minutes to hours, then a
  different engine takes the port. Nothing is long-lived except the host.
- **Mixed architectures.** aarch64 Linux, x86_64 Linux, and arm64 macOS.

## The engines

Seven local engines are in use.

| engine | where its numbers come from | notes |
|---|---|---|
| **vLLM** | native Prometheus `/metrics` | `prompt_tokens_by_source_total` splits computed from cached prompt tokens; request-clock prefill and decode histograms; `spec_decode_*` when speculative decoding is on. Labelled with `model_name` |
| **llama.cpp** (`llama-server`) | native `/metrics`, only with `--metrics` | `prompt_tokens_total` (computed only), `prompt_tokens_cached_total`, `prompt_seconds_total`, `tokens_predicted_total`, `tokens_predicted_seconds_total`, `spec_decode_*`. The meaning of `prompt_seconds_total` changed on 2026-08-13 |
| **SGLang** | native `/metrics`, only with `--enable-metrics` | `realtime_tokens_total{mode}` splits computed, cached and decode tokens. No per-phase seconds counter; speculative acceptance is gauges only |
| **mlx-serve** | native `/metrics`, only with `--metrics` | vLLM names for compatibility, plus `mlx_serve:prefill_tokens_total` (computed) and `mlx_serve:prefix_cache_tokens_total` |
| **Ollama** | per-response stats: `prompt_eval_count`, `prompt_eval_duration`, `eval_count`, `eval_duration` (durations in ns) | not cumulative counters, and no `/metrics`. The third-party exporter deployed on some hosts reports model inventory only, no token counts |
| **ds4** | a timing log to stderr, switched on by `DS4_MTP_TIMING` | a DeepSeek-family engine, Mac and Spark. One line per speculative cycle; no `/metrics` |
| **MTPLX** | a decode trace in JSONL, switched on by `MTPLX_DECODE_TRACE_JSONL` | Mac |

The evidence for each row is in [`findings.md`](findings.md).

Several recipes run their engine inside Docker, and one engine can serve on
different ports depending on the recipe.

## The monitoring stack this plugs into

- **Central Prometheus**, with Grafana on top. It currently scrapes per-engine
  ports directly, the pattern this project replaces. It needs its
  **remote-write receiver** enabled (`--web.enable-remote-write-receiver`),
  which is **not** on today.
- **Already scraped on every host:** `node_exporter` (host metrics), DCGM (GPU
  metrics on the NVIDIA hosts), a Mac SMC/power exporter, and cAdvisor. These
  are out of scope here.
- **Smart plugs** report wall power per machine through Home Assistant into
  InfluxDB. Also out of scope, but it is why "GPU watts" and "wall watts" are
  different panels.

## The consumer that matters most

The local-llm harness records one row per benchmark trial in a
`results.jsonl` ledger. Today those rows carry wall-clock time per trial, but
no prefill or decode token counts. The plan is for the harness to read this
exporter's counters at trial start and end, and store the deltas in the row.
That makes the exporter part of the measurement, which is why correctness and
provenance matter more here than dashboard polish.
