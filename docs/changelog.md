# Changelog

Each release's notes are its section here. The release workflow publishes
the section and refuses a version without one.

## 0.1.0 (unreleased)

The first build: one exporter per host, one schema for every engine.

- **Schema.** Canonical `llm_*` counters with `engine`, `model`, `backend`,
  `host` and `nodes` on every series. Prefill tokens are computed tokens only,
  and cache hits have their own counter, because vLLM, SGLang and mlx-serve
  count cache hits in `prompt_tokens_total` and llama.cpp does not. Phase
  seconds come on two clocks, request and engine, under two names. No tok/s
  gauge.
- **Adapters.** vLLM, llama.cpp (both sides of its 2026-08-13 metrics
  rewrite) and SGLang read `/metrics`; ds4 reads its `DS4_MTP_TIMING` log.
  Every mapping is recorded in `docs/adapters.md` and tested on real output.
- **Registration.** Launchers run `llm-metrics-exporter register` and
  `deregister`: an atomic write, and a delete that checks `run_id` so a late
  stop cannot remove a newer arm.
- **Health.** A registered arm that cannot be read exports
  `llm_engine_up 0`, with its error count and last success time. An invalid
  registration exports `llm_registration_invalid`.
- **Packaging.** A systemd user unit, a LaunchAgent, and a Prometheus Agent
  configuration.
