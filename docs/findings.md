# Findings: what the engines actually export

Read on 2026-09-22, before any adapter was written. Every claim here comes from
one of three sources, named on each line:

- **live**: a GET of a running engine's `/metrics`;
- **stored**: series the central Prometheus already holds from the old
  per-port scrape jobs, read through Grafana;
- **source**: the engine's own code, at the version named.

## vLLM

Version: `0.29.1rc1.dev347+gdee37d891` (source: the `vllm/vllm-openai`
nightly aarch64 image; stored series came from builds of about that age).

- **Prompt tokens by source exist.** `vllm:prompt_tokens_by_source_total{source}`
  with `source` in `local_compute`, `local_cache_hit`, `external_kv_transfer`
  (source: `v1/metrics/stats.py`, `PromptTokenStats.ALL_SOURCES`). It
  increments per scheduler iteration.
- **`vllm:prompt_tokens_total` includes cached tokens.** Stored, for one model:
  `prompt_tokens_total` 5,029,615 = `local_compute` 363,231 +
  `local_cache_hit` 4,666,384. The cached share is 93%.
- `vllm:prompt_tokens_cached_total` is "cached prompt tokens (local + external)"
  (source). It equals `local_cache_hit` + `external_kv_transfer` in every
  stored series.
- **Phase times are request-clock and recorded at finish.**
  `request_prefill_time_seconds` is first SCHEDULED to first token;
  `request_decode_time_seconds` is first token to last token; both include
  preemptions (source: `v1/metrics/stats.py`,
  `update_from_finished_request`). Overlapping requests are all counted.
- The decode interval covers `n - 1` tokens of an `n`-token response, because
  the first token comes from the prefill step. `generation_tokens_total`
  counts all `n`. Per-stream decode speed is therefore slightly high for short
  responses; at 500 tokens the error is 0.2%.
- `request_prefill_kv_computed_tokens` (histogram, "new KV tokens computed
  during prefill, excluding cached tokens") does **not** equal
  `local_compute`: stored, 356,378 vs 363,231 for the same series. It is
  recorded per finished request; `local_compute` is per iteration and includes
  recomputation after preemption. The adapter uses `local_compute`.
- Speculative: `spec_decode_num_drafts_total`, `_num_draft_tokens_total`,
  `_num_accepted_tokens_total`, present only when a speculative config is
  loaded (stored: three models).
- `request_success_total{finished_reason}`: `stop`, `length`, `abort`,
  `error`, `repetition` (stored).
- Labels: `model_name` and `engine` (the data-parallel index) (stored).

## llama.cpp

Versions: upstream `911f6cdc8` (source); a build on the cluster reporting
build 139, commit `d3f3811` (stored).

- **The metrics changed on 2026-08-13**, in upstream commit `decaf508b`
  ("server: refactor + correctness fixes for metrics"). The same commit added
  `llamacpp:prompt_tokens_cached_total`, so its presence identifies the new
  semantics.
- **Before:** `prompt_tokens_total` and `prompt_seconds_total` are per-slot sums
  of each request's prompt processing (`on_prompt_eval`): request clock.
- **After:** prompt tokens and prompt seconds are accumulated per batch, from
  the start of the first batch with queued prompt tokens to the synchronized
  output (`metrics_queue_prompt` / `metrics_flush_prompt`): engine clock.
  Decode (`tokens_predicted_seconds_total`) is still a per-slot sum, flushed
  when a slot resets: request clock.
- **`prompt_tokens_total` excludes cached tokens in both.** HELP after the
  change: "Number of prompt tokens processed, excluding cached tokens".
  Stored: 7,915,720 computed against 87,719,700 cached.
- Speculative counters exist after the change: `spec_decode_num_draft_tokens_total`,
  `_num_accepted_tokens_total`, `_num_drafts_total` ("verification steps").
  They are flushed per slot at request completion.
- No request counter and no KV-cache usage gauge. `requests_processing` is
  the running count.
- Some builds add a `model` label to every sample (stored); upstream has none.

## SGLang

Version: `lmsysorg/sglang` nightly dev cu13 image of 2026-09-21 (live, from a
two-node tensor-parallel server).

- `sglang:prompt_tokens_total` includes cached tokens: 4,668,037 against
  `realtime_tokens_total{mode="prefill_compute"}` 971,400 and
  `{mode="prefill_cache"}` 3,829,888 (live).
- `sglang:realtime_tokens_total{mode}`: `prefill_compute`, `prefill_cache`,
  `decode`. HELP: "updated on each log interval" (live). The adapter uses this
  family for all three token counters, so the three share one update rule.
- `generation_tokens_total` (147,139) and `realtime_tokens_total{mode="decode"}`
  (156,755) differ by 9,616 with one request running. The difference is not
  explained yet; the adapter uses `realtime_tokens_total` and this is recorded
  in `adapters.md`.
- **No per-phase seconds counter.** `scheduler_stage_seconds_total{category}`
  splits the scheduler loop by stage, and `run_batch` is prefill and decode
  forward time together.
- **Speculative acceptance is gauges only**: `spec_accept_rate`,
  `spec_accept_length`, `spec_num_draft_tokens`. `spec_verify_calls_total` is a
  counter, but whether it counts per request or per batch is unverified.
- `token_usage` is KV tokens used over KV capacity; `num_running_reqs` is the
  running count.
- Scheduler series carry `tp_rank`, `pp_rank`, `moe_ep_rank` (and `dp_rank`
  on some); request series carry `is_streaming`. On the two-node server only
  rank 0 reported.

## TensorFold

Version: TensorFold v0.3.4 (vendored at `2f8e514`) with the patches of
`jayleaton/glm53-tensorfold-spark` at `e9c8cbb` (source).

- Stock TensorFold's server has `/v1/models` (`owned_by: "tensorfold"`) and a
  `/health` that always says ok. It has no `/metrics` (source:
  `src/tensorfold/cuda/server.py`).
- Patch 0150 (`patches/0150-glm-mia-wins.patch`, `cuda/health.py`) adds
  `/metrics`: nine counters and four gauges, `tensorfold_*`, each with one
  `model` label. Values are process totals kept under one lock, and a
  completion adds all its counters at once when it finishes. A completion that
  raises adds only to `requests_total` and `request_errors_total` (source).
- `prompt_tokens` is the whole prompt; `cached` is the prompt position the
  request resumed from, so the engine computed `prompt - cached` (source:
  `families/glm5_next/cuda/batch.py`, patched). Patch 0300's request log
  derives prefill speed the same way.
- `prefill_s` runs from batch-slot admission to the first token; `decode_s`
  from the first token to the last. Both are per request, so they are the
  request clock (source: `batch.py`).
- `requests_inflight` counts requests inside `generate`. With
  `GLM53_TF_BATCH` (the recipe's production config sets 4), the app does not
  serialize requests, so a request waiting for a slot is counted (source:
  `families/glm5_next/cuda/app.py`).
- `decode_rounds` is `Stepper.rounds`; the request log's `tokens_per_round`
  is `(tokens - 1) / rounds` (source: `batch.py`).
- Patch 0300's request log (`reqlog.py`) writes one JSON line per request
  after it ends, errors and cancels included, with `finish` one of `stop`,
  `length`, `tool_calls`, `cancelled`, `error`. A preempted background request
  is re-queued, not ended, so it writes one line (source).
- The log has no first-token time. `first_s` is set by the first call to the
  app's emit, which only fires for visible text: live, 95 of 181 lines have
  `first_s: null`, and one line's `first_s` (13.385 s) is its prefill plus its
  whole decode. `queued_s` counts from the original submission, so for 9
  re-queued title requests `queue_s + prefill_s` exceeds `first_s` by up to
  148.8 s (live, `testdata/tensorfold/*.requests.jsonl`).

### TensorFold v0.6 (MiaAI-Lab recipe)

Version: TensorFold v0.6.0 with the MiaAI-Lab GLM recipe v1.8 (image
`v0.6.0-31557ed1cef6`, 82 patches); source is upstream `v0.6.0` plus the
recipe's `patches/0045-cuda-metrics.patch`.

- TensorFold v0.6 serves its own `/metrics` (`src/tensorfold/server/metrics.py`):
  `tensorfold:*` families with no model label. Patch 0045 appends the CUDA
  server's `/health` fields as `tensorfold_health:*` (source).
- `cuda/health.py` folds every request when its `generate` returns, in a
  `finally`, so a request that raises is counted too. The fold adds
  `len(out)` to `generation_tokens_total` and `completion_tokens_total`, and
  the engine's per-request `prefill_s`, `decode_s`, `cached`, `rounds`,
  `drafted` and `accepted` stats to their totals (source).
- `completion_tokens_total` at scrape time adds the running requests' tokens
  so far; `generation_tokens_total` does not (source; live: 504,733 against
  504,646 with one request running).
- `kv_cache_usage_ratio{pool}` is one sample per live stream: its tokens over
  the effective context window. It is not cache-pool use (source: `_pools`).
  Live, it read 0.0767 while the pool was 17.4% used (423,936 of 2,433,024
  rows held by streams or kept prompts).
- TTFT runs from arrival to the first token in `out`; latency from arrival to
  the end of `generate` (source). Live, TTFT sum plus decode seconds
  (9,843.9 s) is within 0.7 s of the latency sum (9,844.6 s).
- Live, `generation = rounds + accepted + requests` exactly: 148,833 +
  354,764 + 1,049 = 504,646.

## mlx-serve

Version: `v26.9.1-24-g25de4d5` (source).

- `/metrics` exists, behind `--metrics`, and reuses vLLM names for
  compatibility: `vllm:prompt_tokens_total` (all prompt tokens),
  `vllm:generation_tokens_total`, `vllm:request_prefill_time_seconds`,
  `vllm:request_decode_time_seconds`, `vllm:time_to_first_token_seconds`.
- It adds `mlx_serve:prefill_tokens_total` (computed) and
  `mlx_serve:prefix_cache_tokens_total` (restored from cache), with the
  invariant that the two sum to `vllm:prompt_tokens_total`.
- All counters and histograms update when a request completes.
- So mlx-serve is a pass-through adapter, not a stateful one.

## Ollama

- No Prometheus endpoint in the server (source: upstream at `e5a81899`,
  no `/metrics` route).
- The third-party exporter already deployed on the hosts
  (`lucabecker42/ollama-exporter:1.0.1`) exports `ollama_up`,
  `ollama_version_info`, `ollama_models_total`, `ollama_model_info`,
  `ollama_model_size_bytes`, `ollama_model_modified_timestamp_seconds` and its
  own scrape metrics (live). **No token counts.** It cannot replace an adapter.

## ds4

- No Prometheus endpoint and no `/metrics` route (source).
- With `DS4_MTP_TIMING` set, ds4 prints one line per speculative cycle to
  stderr. Two formats exist, and they differ on the free first token; see
  `adapters.md`. Real logs from local-llm's evidence tree are the fixtures.
- The lines give speculative counters only. A cycle line does not prove that
  every decoded token appears in some cycle, so decode tokens are not derived
  from them.
