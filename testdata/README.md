# Fixtures

Every adapter is tested against real output from its engine (`AGENTS.md`).
Each fixture is listed here with where it came from and the engine version.
Nothing in this tree may identify a host: strip `instance`, `job` and any
address or path before adding a file. “Unchanged” below means unchanged
measurements apart from this mandatory sanitization; it does not permit
publishing private identities from live output.

Three kinds of provenance:

- **live**: the body of a GET of the engine's `/metrics`, unchanged.
- **stored**: one instant of the series the central Prometheus stored from the
  engine's `/metrics`, as the Prometheus query API returned it, with `instance`
  and `job` removed. Values, names and labels are the engine's; `# HELP` and
  `# TYPE` lines are not stored, so the test harness infers types
  (`internal/promtext.FromQueryJSON`). Replace with a live capture when one is
  available.
- **log**: an engine log, unchanged.

| file | kind | engine version | what it shows |
|---|---|---|---|
| `llamacpp/b10809-5266f24da.metrics.txt` | live, 2026-09-22 | llama.cpp b10809 (`5266f24da`), macOS arm64, SmolLM2-135M Q4_K_M, `--metrics -np 2` | after the 2026-08-13 metrics rewrite: `prompt_tokens_cached_total` present. Two requests sharing a 256-token prefix, then two concurrent requests |
| `llamacpp/b10809-5266f24da-idle.metrics.txt` | live, 2026-09-22 | same | the same server before any request: every counter 0 |
| `sglang/nightly-dev-cu13-20260921-0f6761b5.metrics.txt` | live, 2026-09-22 | `lmsysorg/sglang` nightly dev cu13 image of 2026-09-21 | a two-node tensor-parallel server with speculative decoding, mid-benchmark |
| `vllm/stored-qwen3.8-flash-next.promql.json` | stored, 2026-09-15 | vLLM nightly aarch64, about `0.29.1rc1` | speculative decoding and prefix-cache hits, both small |
| `vllm/stored-gpt-oss-20b.promql.json` | stored, 2026-09-15 | same | 93% of prompt tokens are cache hits; no speculative decoding |
| `ds4/qwen-mtp-sweep.log` | log | ds4-metal, Qwen MTP path (local-llm evidence 0039) | `Qwen MTP timing` cycle lines inside a real server log |
| `tensorfold/v0.3.4-glm53-e9c8cbb.metrics.txt` | live, 2026-09-29 | TensorFold v0.3.4 + `jayleaton/glm53-tensorfold-spark` @`e9c8cbb` (patch 0150's `/metrics`), two-node GLM-5.3-Flash EXL3, `GLM53_TF_BATCH=4` | `curl` of `/metrics` on the cluster head during a benchmark replay: 32 requests, 175,040 of 250,456 prompt tokens cached |
| `tensorfold/v0.3.4-glm53-e9c8cbb-warmup.metrics.txt` | live, 2026-09-29 | same | the same server 41 s after start, after its warmup requests: no cache hits yet (a measured zero) |
| `tensorfold/v0.3.4-glm53-e9c8cbb.requests.jsonl` | log, 2026-09-29 | same, `GLM53_TF_REQUEST_LOG` on | patch 0300's request log, copied unchanged from the host at 08:22 EDT during the benchmark replay: 181 lines, 147 `tool_calls` and 34 `stop`. Token ids appear only as hashes |
| `tensorfold/v0.3.4-glm53-e9c8cbb.models.json` | live, 2026-09-29 | same | `/v1/models`: one id, `owned_by: "tensorfold"` |
| `tensorfold/v0.6.0-miaai-glm-v1.8.metrics.txt` | live, 2026-10-06 | TensorFold v0.6.0 + `MiaAI-Lab/GLM-5.3-Flash-EXL3-2x-DGX-Sparks-TensorFold` v1.8 (image `v0.6.0-31557ed1cef6`, 82 patches; patch 0045 adds `tensorfold_health:*`), two-node GLM-5.3-Flash EXL3 with a DFlash2 drafter | TensorFold's own `/metrics` on the cluster head under load: 1,049 finished requests, 39,296,704 of 40,840,385 prompt tokens cached, 354,764 of 532,754 draft tokens kept. No series has a model label |
| `tensorfold/v0.6.0-miaai-glm-v1.8.models.json` | live, 2026-10-06 | same | `/v1/models`: one id, `GLM-5.3-Flash-EXL3`, `owned_by: "tensorfold"` |
| `ollama/ollama-exporter-1.0.1.metrics.txt` | live, 2026-09-22 | `lucabecker42/ollama-exporter:1.0.1` | evidence for `docs/findings.md`: model inventory only, no token counts. No adapter reads it |

The vLLM fixtures are stored, not live, because no vLLM server was running when
the adapter was written. Replacing them with a live capture is tracked in
`docs/plan.md`.
