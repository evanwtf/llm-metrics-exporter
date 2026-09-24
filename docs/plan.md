# Plan

Phases follow `design.md`, *Rollout*. Each item says what "done" is.

## Status interpretation

The original rollout checklist below is retained, including unverified external
deployment and integration tasks. “Done in 0.1.0” means implemented in the
checkout, not published: the [changelog](changelog.md) labels 0.1.0 unreleased.
Built-in remote write, worker/reset boundaries, bounded collection, backlog
and availability signals, and static targets have since been implemented; see
the changelog and current operational guides. A separate Agent is now one
delivery option, not a deployment prerequisite.

Open questions are proposals, not permission to change policy. Publishing
remains manual until the operator approves a change. Stale registrations remain
visible as engine down until explicitly deregistered. SGLang pipeline-stage
counting remains unverified despite the implemented rank filtering. No current
fleet deployment, release publication or external harness completion is inferred
from these checkboxes.

## Phase 1: the exporter core — done in 0.1.0

- [x] Canonical schema, one place (`internal/metrics`), with tests that every
      series has `engine` and `model` and that no tok/s gauge exists.
- [x] Registration: strict v1 parsing, atomic write, run_id-checked delete,
      `register` / `deregister` subcommands.
- [x] Collector: per-run adapter lifecycle, parallel collection under a
      timeout, loud health series.
- [x] Adapters: vLLM, llama.cpp, SGLang (pass-through); ds4 (log).
- [x] CI on the self-hosted runners, including building and running the
      Docker image; pre-commit with the public-repo guard.
- [x] A manual publish workflow (zips for darwin/linux × arm64/amd64), gated
      on the version, an unused tag and the changelog.
- [x] Dockerfile and `compose.yaml` for Linux hosts.
- [x] End-to-end run against a live SGLang server.

## Phase 2: deploy on the Sparks

- [ ] Central Prometheus: `--web.enable-remote-write-receiver`.
- [ ] Publish `v0.1.0` (`gh workflow run publish.yml -f tag=v0.1.0`); on both
      Sparks, install the binary for launchers and run the exporter with
      `docker compose up -d` (or the systemd user unit).
- [ ] Decide whether publishing should trigger automatically (on a tag). It is
      manual by decision, 2026-09-22.
- [ ] A Prometheus Agent per Spark (`packaging/prometheus-agent/agent.yml`).
- [ ] local-llm: launchers call `llm-metrics-exporter register` at start and
      `deregister` at stop, including the two-node recipes. Separate issue in
      local-llm.
- [ ] Alert: `llme_engine_up == 0` for 5 minutes; `llme_exporter_registration_invalid`;
      `absent(up{job="llm-metrics-exporter"})`.
- [ ] Verify the systemd unit with `systemd-analyze --user verify` on a Spark
      (not yet run).

## Phase 3: the harness reads counters

- [ ] local-llm reads the counters for the trial's `backend` at trial start and
      end, writes the deltas and the clock into the row, and refuses a delta
      across a reset (`design.md`, *The benchmark contract*).
- [ ] A small read helper, if the harness wants one: `llm-metrics-exporter
      snapshot --backend X` printing JSON. Decide with the harness author.

## Phase 4: Mac engines, then Ollama

- [ ] mlx-serve adapter: capture a live `/metrics` with `--metrics` first.
- [ ] MTPLX adapter: capture a real decode trace first.
- [ ] Ollama: decide proxy vs log (`design.md`, *Open decisions*).
- [ ] Install on the M5 Max with the LaunchAgent.

## Phase 5: retire the old scrape jobs

- [ ] Remove the per-engine `vllm`, `llamacpp` and Ollama-exporter jobs from the
      central config; point `dgx_metrics.py` at `llme_*`.

## Open questions to close with evidence

- [ ] **Replace the stored vLLM fixtures with a live capture** the next time a
      vLLM arm serves.
- [ ] **SGLang decode disagreement**: `generation_tokens_total` vs
      `realtime_tokens_total{mode="decode"}` differ by about 6%. Capture both at
      an idle moment; if they still differ, read the SGLang source.
- [ ] **SGLang speculative counters**: is `spec_verify_calls_total` per request
      or per batch? If a counter of accepted tokens exists upstream, map it.
- [ ] **SGLang pipeline parallelism**: which rank counts tokens.
- [ ] **Stale registrations after a reboot** export `llme_engine_up 0` until
      deregistered. Decide whether that is the wanted behavior once alerts exist.
- [ ] **Lint**: add `staticcheck` to CI and pre-commit.
