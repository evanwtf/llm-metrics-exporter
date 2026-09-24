# Documentation reconciliation audit

## Issue 13 implementation migration

Source: `a084ee903b59d329d4fd11fc3fce3727957796e5` (main), clean before feature
work; September 2026. This is an implementation-authorized policy extension,
not a documentation-only change. Original audit below remains historical.

| Source / distinct items | Destination / disposition |
| --- | --- |
| QUICKSTART introduction, vLLM/curl prerequisites, no Prometheus needed; engine URL/model lookup, exact metric label, nodes and backend replacement warning | Full manual procedure retained in `pinned-quickstart.md`, linked from new automatic QUICKSTART; automatic prerequisites/identity now in QUICKSTART and discovery |
| QUICKSTART native Make/Go override, registration fields/run ID, foreground/cancellation, raw Go build and cache troubleshooting | Retained in pinned quickstart; automatic entry omits mandatory registration and links the manual path |
| QUICKSTART Compose prerequisites, native macOS/VM restriction, exported path/host/listener, mkdir, build, UID/GID writable one-shot mount, read-only long-running mount, up/ps, shell overrides/.env, permissions and credential caveat | Retained in pinned quickstart; add `LLM_EXPORTER_DISCOVERY=off` for deliberate manual-only semantics. Automatic root path uses Compose .env directly, preserving directory/access prerequisites |
| QUICKSTART curl/grep and retry, backend quickstart, up/mismatch/availability, computed/cache/idle/missing semantics, liveness, request/delta link | Retained in pinned quickstart; auto root diagnostics add discovery status and observed identity |
| QUICKSTART scraping/access control, remote write/duplicates, static/CLI/development links, native and Docker cleanup/run ID/no model stop | Retained in pinned quickstart; auto links to manual cleanup and warns removal of a pin exposes a local auto target |
| AGENTS registration-authority rule | Extended for automatic evidence-based identity; original exact rule and variable-port/third-party-launcher rationale retained in discovery § Pins and migration. Pin authority unchanged |
| README launcher-only introduction and Bootstrap | Current default explained; original commands retained under Pinned bootstrap, adding `--discovery=off` so deregister still stops observation. All existing examples and query semantics retained with transition warning |
| design Labels, Discovery, Health | Original pin table/procedure retained; explicit auto extension links scope, unknown topology, bounded down retention and reset guard. Registered-engine health wording broadened to identified engines, not mere port reachability |
| compose hostname claim | Corrected: network namespace sharing does not share hostname. Original comment claimed host networking gives the host hostname; README already contradicted it. Explicit host setting remains required for stable identity |

Preservation check: original quickstart compared in full with the relocated
manual guide; only title, relative links and explicit manual-only discovery
settings differ, plus the migration introduction. README examples and detailed
design policies remain in place. No historical measurements or fixture evidence
were changed. No secret files were read, changed or published by this migration.

Source: commit `7932886b44f22d66cfc5e4a7d31e2c7c37b0b205`, clean checkout;
documentation review dated 2026-09-23. No uncommitted source material was
replaced. This is a single established Go application. Both root entry points
are reconciled, not bootstrapped. Existing detailed operational and evidence
guides remain authoritative; new reference material fills specific gaps.

## Entry-point inventory and destinations

Each row accounts for distinct information in the original entry points.
“Retained” means the same scope and qualifications remain, not just a heading.

| Original source / item | Disposition and destination |
| --- | --- |
| AGENTS opening: rules apply to people and agents | Retained in root introduction |
| AGENTS opening: read problem, environment, design and adapters before every change | Superseded by this task's explicit requirement for task-specific reading; all four remain discoverable in the root reading map, mandatory for their stated tasks. Original universal rule recorded here as historical, not active |
| Commands: test all, race with cgo, vet, gofmt must print nothing, public guard over every tracked file | Retained essential commands in root; full forms and qualifications in development § Checks and build |
| Commands: `uvx pre-commit run --all-files`, “everything CI checks, locally” | Command moved to development; overbroad CI claim corrected there. Hooks do not cover CI's cross-builds, container execution, dependency tidiness or macOS race job |
| Commands: “Go only; no other toolchain is needed” | Core Go runtime/build intent retained; hook/uv and Docker prerequisites qualified in development, based on hook and CI configuration |
| Commands: version declaration, changelog prerequisite, manual `gh workflow run publish.yml -f tag=vX.Y.Z`, no automatic trigger without operator approval | Retained actionable root policy; exact workflow, ordering, generic tag and example in development § Releases |
| Public repo: prohibit private machine hostnames, IPs, ports, network names and other identifying details; role/hardware examples; uncommitted per-host values | Retained root public-repository rules |
| Public repo: loopback/placeholders/documentation ranges; edits do not remove earlier publication; check before push | Retained root; expanded safe review in security |
| Public repo: guard patterns, ordinary private names in ignored `.public-denylist`; strip fixture instance/job/paths | Retained root, security § Local files and publication checks; fixture sanitization also in testdata README |
| Measurement: counters are truth, query-time rates, no v1 tok/s gauge; separate prefill/decode | Retained verbatim root measurement bullets |
| Measurement: read failure must emit `llm_engine_up 0`, not drop series | Retained verbatim root |
| Measurement: every series engine/model, operator requirement, schema and full-scrape tests, no runtime/process metrics | Retained verbatim root |
| Measurement: never guess model; registration authoritative, upstream validates | Retained verbatim root |
| Measurement: same interval for same canonical name; document upstream/clock/update timing; computed prefill not cache; separate clocks | Retained verbatim root; adapter task trigger retained |
| Measurement: explicit resets, not negative throughput | Retained verbatim root; worker-specific rule linked to design |
| Measurement: captured real output, engine version/provenance in testdata README, independent jq/reference expected values | Retained verbatim root; development links to fixtures |
| Footprint: shared CPU/GPU memory, small process, bounded buffers, every read timeout, slow-read failure down | Retained verbatim root; enforcement boundary clarified below it |
| Scope: token/request/cache/speculation, not GPU/power/temperature/host memory | Retained verbatim root; observation-only purpose added from problem document |
| README introduction: per-host exporter, launcher arms, HTTP/log dialects, canonical counters, identity/worker labels, static YAML, three delivery modes, Grafana/harness consumers | Retained; harness actual integration distinguished from intended use via linked plan |
| Bootstrap: Go version from manifest; Linux/macOS arm64/amd64; SRC/BIN/ENGINE_URL exports, clone URL, test/build/version, llama-server --metrics; demo registration fields/run ID | Retained commands; added `mkdir -p "$BIN"` to ensure install destination exists |
| Bootstrap: serve background on :9109, curl/grep metrics, deregister matching backend/run ID | Retained; old “Stop the arm” comment corrected: deregister stops observation, not either process (main.go Remove call) |
| Bootstrap: missing engine remains down; “A missing metric is never silent” | Down-series requirement retained; overbroad wording corrected to distinguish token availability from engine reachability (collector availability and adapters' unsupported measurements) |
| Bootstrap: Linux Compose host loopback/read-only registration mount, mkdir prerequisite, Agent profile, host launcher binary; systemd alternative; native macOS LaunchAgent/VM-loopback caveat | Retained, with explicit host-identity caveat for containers; Linux host networking alone does not establish hostname identity |
| Bootstrap: release zip naming, four platforms, binary/LICENSE/README; browser quarantine and unsigned macOS xattr command | Moved to development § Releases, linked from README Bootstrap and Releases |
| Bootstrap: install hooks once, pre-commit and uvx forms | Moved to development § Checks and build, linked from README |
| Exports: decode and prefill rate examples, computed prefill/cache split, schema link | Retained verbatim |
| Exports: vLLM/llama.cpp/SGLang/ds4 support and source flags/limits; mlx-serve/MTPLX/Ollama planned with open source decision | Retained table unchanged |
| Docs: all eleven supporting links and AGENTS link with purposes | Retained, with more precise current/historical descriptions and new reference/audit links |
| Origin: local-llm issue 675 link, benchmark need and generic collector relationship | Retained |
| Releases: manual only, no push/tag publication, version/changelog commit/push ordering, Actions and gh command; mismatch/existing-tag/no-section refusals; tests/four zips/Linux execution/tag/release/notes | Full details moved to development § Releases; root retains manual policy and direct link |
| License: MIT | Retained with link to existing LICENSE |

## Supporting-document corrections and retained history

| Source / original item | Correction and destination / evidence |
| --- | --- |
| design § Architecture: Agent-only diagram | Diagram retained and identified as original Agent topology; existing paragraph still describes all three current modes |
| design § Metric schema: `<id>` list omits worker in shorthand | Explicit note clarifies worker on measurements without altering health/provenance identity; internal/metrics and Labels section |
| design § Metric schema: cache-share query added cached rate to prefill rate without label matching | Added `ignoring (phase)` to the addition because only prefill has `phase`; schema supplies this evidence. Original denominator was `(rate(llm_prompt_cached_tokens_total[5m]) + rate(llm_tokens_total{phase="prefill"}[5m]))`, so mismatched label sets could yield no result. Numerator, 5m window and per-worker identity remain unchanged; no PromQL engine execution was performed |
| design § Labels: nodes value “1 or 2” | Corrected to allowed 1–64; original fleet examples 1/2 and head-only two-node rationale retained. registration.Validate is authority |
| design § Counter ownership: lists mlx-serve/MTPLX/Ollama as though built | Existing classification retained as planned where applicable; Ollama event-source choice remains unresolved, not newly decided |
| design § Counter ownership: “no persisted counter state”; idle delta “is exact” | Clarified adapter counters versus durable remote-write queue/checkpoint, and requirement for reporting/catch-up completion. Original idle-only assertion was too broad: adapters documents SGLang interval lag; ds4 withholds backlog. Restart invalidates benchmark still required; rereading ds4 history is not new work |
| design § Adapters: MTPLX/Ollama log mechanism written as current | Original proposal retained explicitly as a proposal; Ollama proxy/log decision remains open; compiled set is internal/engines |
| design § Benchmark contract / Rollout: present-tense integration and deployment ordering | Preserved as intended external contract and original rollout, not proof of deployment. plan still has unchecked external integration/deployment items |
| environment: fleet/current monitoring/receiver “not on today”/ledger missing tokens | Whole snapshot retained, explicitly historical 2026-09-22 design context, not fresh fleet inspection |
| problem: “missing today”, 30-trial incident and done criteria | All retained, prefaced as original motivation and acceptance intent; current support bounded by adapter implementation |
| plan: core “done in 0.1.0”, unchecked deployment/auto-publish/Agent/stale-registration proposals | Retained original checklist; new status note distinguishes implemented but unreleased work, delivery alternatives and binding manual-publish/stale-down policy from open proposals |
| testdata: live/log “unchanged” vs required sanitization | Clarified unchanged measurements subject to mandatory identity/path sanitization; all provenance rows, versions, dates, exact values and future live-vLLM capture item retained |
| remote-write: Docker queue-volume example without permissions prerequisite | Example retained; explicit writable-by-nonroot prerequisite added, inferred from Dockerfile USER and queue file creation; not a tested deployment assertion |
| cheat-sheet: issue #2 named without implementation status | Retained issue citation, clarified worker preservation is implemented and original issue is historical context; live 2026-09-23 measurements/limits unchanged |

Unchanged source documents (adapters, findings, static-targets, changelog and
Prometheus setup) retain their detailed policies, examples and evidence in place.
No original document was deleted or replaced by a verbatim archive. No GEMINI
file is created or modified, per operator instruction. CLAUDE remains a relative
alias of root AGENTS.

## Scope and verification record

New CLI reference is grounded in main.go and registration validation; new
development guide in manifests, hooks and workflows; credential guidance in
the existing token-file interface and the operator's explicit safety policy.
No particular secret manager is established. Known local paths already have
project ignore coverage, so no speculative ignore filenames are added.

The original root counts (`wc -w`) are AGENTS 515 and README 808. The original
universal agent reading set was 5,518 words (AGENTS, problem, environment,
design, adapters). The final universal set is root AGENTS only; links require
reading solely for identified tasks. Final counts and delivery checks are
reported below. Original and destination content were compared for qualifiers,
examples, provenance, policies and exceptions; the migration mapping above is
complete. All distinct replaced material has an in-checkout destination.

Verification is static: repository evidence, local links/anchors, relative
symlink target, word budgets, whitespace/diff and ignore/tracking checks.
Application tests, builds, services and live deployment procedures are not run.
External deployments, upstream versions and release existence are not revalidated
by executing procedures; historical evidence is preserved, not presented as a
new measurement. CI triggered by PR delivery is separate from these local
documentation checks.

Completed checks:

- `wc -w`: AGENTS 515 → 1,057; README 808 → 858. Final universal agent
  instructions: **1,057 words, AGENTS.md only** (CLAUDE symlink not counted twice).
  README is 134 lines; no independently maintained components need entry points.
- All 106 local Markdown links/anchors across 18 documents resolve. All eight
  distinct external Markdown destinations return HTTP 200; the two external
  fragment anchors exist. This checks reachability, not the truth of cited claims.
- CLAUDE is the existing relative symlink to AGENTS and resolves. GEMINI is
  untouched. The four baseline policies match the requested text verbatim.
- Public-repository guard passes for all 18 Markdown documents; diff whitespace
  checks pass. Review found only intentional documentation changes, with no
  secret values introduced. No secret files were retrieved for verification.
- `.env`, `prometheus-agent.local.yml` and `.public-denylist` match project
  ignore rules and are not tracked; sanitized `.env.example` remains tracked
  and not ignored. No `.gitignore` change was necessary. This is not a secret
  scan of Git history or proof about operator-selected paths outside this repo.
- Commands were checked against code, manifests and workflows, not run. No
  application tests/builds, service launches or live measurements were performed.

Delivery uses PR mode: the established remote defaults to `main`, shortlog shows
multiple authors, and CI triggers on `pull_request`; the direct-mode exception
does not apply. A working branch was created from `origin/main` before editing.
No code, runtime configuration, credentials or deployment state was changed.
