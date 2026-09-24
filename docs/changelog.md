# Changelog

Each release's notes are its section here. The publish workflow uses the
section as the notes and refuses a version without one.

## 0.1.0 (unreleased)

- Rename every series from `llm_*` to the `llme_` namespace so it cannot collide
  with other tools' `llm_*` series. Engine measurements are `llme_*`; exporter
  state is `llme_exporter_*` (for example `llme_exporter_discovery_status`,
  `llme_exporter_arm_info`), except `llme_engine_up`. Update queries,
  dashboards and any Prometheus Agent keep-regex. See issue #16.

- Default continuous local engine discovery: vLLM, llama.cpp and SGLang follow
  engine/model/port changes without exporter reconfiguration. Preserve explicit
  pins, expose discovery diagnostics and transition timestamps, leave topology
  unknown unless asserted, and document guarded rate queries. See issue #13
  and [discovery](discovery.md) for limits and migration.

- Optional built-in Remote Write 1.0 sender with a bounded persistent queue,
  ordered replay, retries, staleness, TLS/bearer authentication, and JSON status.
- Preserve worker reset boundaries and per-worker cache ratios; reject ambiguous
  model selection. Bound in-flight collection and defer replacement/close safely.
- Withhold log counters during catch-up and report backlog. Add token measurement
  availability and static registrations with optional run IDs.

The first build: one exporter per host, one schema for every engine.

- **Schema.** Canonical `llme_*` counters with `engine`, `model`, `backend`,
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
  `llme_engine_up 0`, with its error count and last success time. An invalid
  registration exports `llme_exporter_registration_invalid`.
- **Packaging.** A Dockerfile and `compose.yaml` for Linux hosts (host
  networking, the registration directory mounted read-only, an optional
  Prometheus Agent), a systemd user unit, a LaunchAgent, and a Prometheus
  Agent configuration.
- **Releases.** A manual publish workflow: one zip per platform (macOS and
  Linux, arm64 and amd64) with the binary, gated on the version, an unused
  tag and a changelog section.
