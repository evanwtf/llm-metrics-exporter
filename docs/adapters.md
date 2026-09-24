# Adapters: where every series comes from

The design's *Semantic equivalence* rule, made concrete. For each engine and
each canonical series: the upstream source, the interval it measures, and
when it updates. The evidence is in [`findings.md`](findings.md); the tests
pin each row against a fixture ([`../testdata/README.md`](../testdata/README.md)).

"—" means the engine does not export it, so the exporter emits nothing.
Absent is "not measured", never zero.

Automatic selection for the three HTTP adapters uses the signatures and model
evidence in [discovery](discovery.md#evidence-identity-and-availability). It
reuses these mappings unchanged; pinned and log adapters retain their existing
configuration. Captured-signature tests are in `internal/engines/detect_test.go`.

**Clock** is `request` (each request's own interval, summed over requests) or
`engine` (engine wall time, no double counting). See `design.md`, *Two clocks*.

**Updates** is when the upstream number moves: `iteration` (each scheduler
step), `finish` (when a request completes), `interval` (on a timer), or
`line` (when the log line is written).

## vLLM (`vllm`, adapter version 2)

Checked against vLLM `0.29.1rc1.dev347`. Series are selected by
`model_name` (validates `served_model`); the `engine` label (data-parallel
index) is retained as `worker` on every measurement that carries it upstream.
Unpartitioned measurements use `worker="default"`. Never sum raw worker counters
before calculating rates; worker resets and disappearances are independent.

| canonical | source | clock | updates |
|---|---|---|---|
| `llme_tokens_total{phase="prefill"}` | `vllm:prompt_tokens_by_source_total{source="local_compute"}` | — | iteration |
| `llme_tokens_total{phase="decode"}` | `vllm:generation_tokens_total` | — | iteration |
| `llme_prompt_cached_tokens_total` | `vllm:prompt_tokens_cached_total` (local + external) | — | iteration |
| `llme_request_phase_seconds_total{phase="prefill"}` | `vllm:request_prefill_time_seconds_sum`: first scheduled → first token | request | finish |
| `llme_request_phase_seconds_total{phase="decode"}` | `vllm:request_decode_time_seconds_sum`: first token → last token | request | finish |
| `llme_engine_phase_seconds_total` | — | | |
| `llme_requests_total{status}` | `vllm:request_success_total{finished_reason}` | — | finish |
| `llme_requests_running` | `vllm:num_requests_running` | — | iteration |
| `llme_kv_cache_usage_ratio` | `vllm:kv_cache_usage_perc` (0..1 per worker; never summed) | — | iteration |
| `llme_time_to_first_token_seconds` | `vllm:time_to_first_token_seconds` | request | first token |
| `llme_spec_draft_tokens_total` | `vllm:spec_decode_num_draft_tokens_total` | — | iteration |
| `llme_spec_accepted_tokens_total` | `vllm:spec_decode_num_accepted_tokens_total` | — | iteration |
| `llme_spec_verify_steps_total` | `vllm:spec_decode_num_drafts_total` (one per request per step) | — | iteration |

Notes:

- No fallback for prefill. A vLLM without `prompt_tokens_by_source_total`
  exports no prefill tokens, because `prompt_tokens_total` counts cache hits.
- Decode seconds cover `n - 1` of `n` generated tokens. Per-stream decode speed
  reads slightly high on short responses (0.2% at 500 tokens).
- Speculative series exist only when a speculative config is loaded.

## llama.cpp (`llamacpp`, adapter version 1)

Checked against upstream `911f6cdc8` and a live b10809 (`5266f24da`). Upstream
labels no model; a build that adds `model` is validated against
`served_model`.

The adapter detects upstream `decaf508b` (2026-08-13) by the presence of
`llamacpp:prompt_tokens_cached_total`, and the prefill clock depends on it.

| canonical | source | clock | updates |
|---|---|---|---|
| `llme_tokens_total{phase="prefill"}` | `llamacpp:prompt_tokens_total` (excludes cache hits) | — | after: per batch; before: finish |
| `llme_tokens_total{phase="decode"}` | `llamacpp:tokens_predicted_total` | — | finish (slot reset) |
| `llme_prompt_cached_tokens_total` | `llamacpp:prompt_tokens_cached_total` (after only) | — | per batch |
| `llme_engine_phase_seconds_total{phase="prefill"}` | `llamacpp:prompt_seconds_total`, **after** the rewrite: first queued prompt batch → synchronized output | engine | per batch |
| `llme_request_phase_seconds_total{phase="prefill"}` | `llamacpp:prompt_seconds_total`, **before** the rewrite: per-slot sum | request | finish |
| `llme_request_phase_seconds_total{phase="decode"}` | `llamacpp:tokens_predicted_seconds_total`: per-slot generation time | request | finish (slot reset) |
| `llme_requests_total` | — | | |
| `llme_requests_running` | `llamacpp:requests_processing` | — | per scrape |
| `llme_kv_cache_usage_ratio` | — | | |
| `llme_time_to_first_token_seconds` | — | | |
| `llme_spec_draft_tokens_total` | `llamacpp:spec_decode_num_draft_tokens_total` | — | finish |
| `llme_spec_accepted_tokens_total` | `llamacpp:spec_decode_num_accepted_tokens_total` | — | finish |
| `llme_spec_verify_steps_total` | `llamacpp:spec_decode_num_drafts_total` ("verification steps") | — | finish |

Notes:

- The speculative counters are always present after the rewrite, and zero
  without a draft model. Zero is true there: nothing was proposed.
- Decode tokens and seconds move when a request finishes. Mid-request, a
  token delta from this adapter lags the engine.

## SGLang (`sglang`, adapter version 2)

Checked live against the `lmsysorg/sglang` nightly dev cu13 image of
2026-09-21. Series are selected by `model_name`. Scheduler series keep rank 0
of each tensor, pipeline and expert-parallel group (those ranks share one
batch) and preserve data-parallel ranks as `worker`.

| canonical | source | clock | updates |
|---|---|---|---|
| `llme_tokens_total{phase="prefill"}` | `sglang:realtime_tokens_total{mode="prefill_compute"}` | — | interval |
| `llme_tokens_total{phase="decode"}` | `sglang:realtime_tokens_total{mode="decode"}` | — | interval |
| `llme_prompt_cached_tokens_total` | `sglang:realtime_tokens_total{mode="prefill_cache"}` | — | interval |
| `llme_request_phase_seconds_total` | — (no per-phase seconds counter) | | |
| `llme_engine_phase_seconds_total` | — (`scheduler_stage_seconds_total{category="run_batch"}` mixes prefill and decode) | | |
| `llme_requests_total` | — (`num_requests_total` has no finish reason) | | |
| `llme_requests_running` | `sglang:num_running_reqs` | — | interval |
| `llme_kv_cache_usage_ratio` | `sglang:token_usage`, per data-parallel worker | — | interval |
| `llme_time_to_first_token_seconds` | `sglang:time_to_first_token_seconds`, both `is_streaming` values | request | first token |
| `llme_spec_*` | — (acceptance is gauges only; see `design.md`, *Open decisions*) | | |

Notes:

- `realtime_tokens_total` updates on the scheduler's log interval, so a scrape
  can lag by up to one interval. A delta taken with no request in flight is
  exact once one interval has passed.
- `generation_tokens_total` and `realtime_tokens_total{mode="decode"}`
  disagreed by 9,616 tokens (of about 150,000) in the live capture, with one
  request running. Not yet explained; see `plan.md`.
- Pipeline-parallel servers are unverified: rank 0 may not be the stage that
  counts tokens.

## ds4 (`ds4`, adapter version 1)

ds4 has no `/metrics`. With `DS4_MTP_TIMING` set, it prints one line per
speculative cycle to stderr, and the launcher registers that log as
`log_path`. The adapter reads new lines on each scrape (at most 32 MiB per
scrape), probes `GET /v1/models` so a stale log does not read as up, and
resets its totals when the log shrinks or is replaced.

If bytes remain unread after the per-scrape cap, counters are withheld and
`llme_exporter_telemetry_backlog_bytes` reports the lag. On catch-up, totals include the
whole log; that observation establishes a baseline. Historical replay is not
exported as a sequence of partial counter increments.

| canonical | source | updates |
|---|---|---|
| `llme_spec_draft_tokens_total` | Qwen path: `drafted`; other paths: `drafted - 1` | line |
| `llme_spec_accepted_tokens_total` | Qwen path: `accepted`; other paths: `committed - 1` | line |
| `llme_spec_verify_steps_total` | cycle lines that proposed at least one token | line |
| everything else | — | |

The formats, from ds4 source (`ds4.c` at `9ab70534`) and local-llm's
`benchmarks/agent/mtp_timing.py`:

```
ds4: Qwen MTP timing drafted=7 accepted=3 target_tokens=4 cycle=.. verifier=block
ds4: Qwen MTP timing drafted=0 accepted=0 target_tokens=1 cycle=.. verifier=scheduler-bypass
ds4: mtp timing micro drafted=7 committed=5 draft=.. verify=.. total=..
ds4: mtp timing margin-skip drafted=2 committed=1 ...
```

- The Qwen path's `accepted` is already draft-only. The free first token is
  the `+1` in `target_tokens`, which the adapter checks.
- The other paths count the free first token in both `drafted` and
  `committed`, so both lose one. A declined draft prints `committed=1` and
  accepts nothing.
- A scheduler bypass (`drafted=0`) is plain decode: not a verify step.
- The counters stay absent until the first cycle line. Without
  `DS4_MTP_TIMING` there never is one, and zero would claim "no drafting".
- On the fixture the totals match local-llm's parser exactly: 362 proposed,
  220 accepted, 57 drafting cycles.

## Not yet built

- **mlx-serve**: a pass-through adapter over its `/metrics` (with
  `--metrics`). Prefill tokens from `mlx_serve:prefill_tokens_total`, cached
  from `mlx_serve:prefix_cache_tokens_total`, request-clock seconds from its
  vLLM-named histograms. Needs a captured fixture.
- **MTPLX**: sum the `_delta` fields of the decode-trace JSONL (not the
  per-request totals, which restart each request); no free-token subtraction.
  Needs a captured trace.
- **Ollama**: open. See `design.md`.
