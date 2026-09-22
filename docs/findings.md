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
