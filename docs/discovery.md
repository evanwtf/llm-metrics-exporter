# Automatic engine discovery

`serve` defaults to `--discovery=local`. One unchanged exporter URL follows
supported local engines across engine, model and port changes. Discovery runs
on every collection (an HTTP scrape or a built-in remote-write tick), not just
at startup or after errors. No background polling occurs without collections.
See the [quickstart](../QUICKSTART.md) for native/Compose commands.

## Scope and safety

- Linux enumerates TCP listeners from `/proc/net/tcp` and `/proc/net/tcp6` in
  the exporter's network namespace. Loopback (`127.0.0.1`, `::1`) and wildcard
  listeners are probed over loopback. Use native execution or host-networked
  Linux Compose to see host engines. No root, Docker socket, or LAN scan is used.
- macOS uses `/usr/sbin/lsof -nP -iTCP -sTCP:LISTEN -Fpn`. Run natively as the
  engine user; permissions can limit visible listeners. No elevated privilege
  is requested. Missing/failed enumeration reports `scope_error`.
- Only plain HTTP `GET /metrics` and, when necessary, `GET /v1/models` are
  issued. No inference, launch, stop, log discovery or model-loading requests.
  These GETs reach other local services too; if this is undesirable, restrict
  discovery endpoints or disable it. Probes bypass proxy environment variables
  and never follow redirects. There is no engine authentication integration.
- `--discovery-endpoints=http://127.0.0.1:8000,http://127.0.0.1:8080` replaces
  enumeration with a fixed, deduplicated scope. Engine/model still follow live
  changes. Only loopback IPs or `localhost` are allowed; no credentials, custom
  paths, queries, fragments or DNS names. This cannot follow a new port outside
  that list. Remote/HTTPS targets remain available through explicit pins.
- Default enumeration treats one port as one service and prefers IPv4 over IPv6
  to avoid counting a dual-stack listener twice. Separate IPv4/IPv6 services on
  the same port need explicit endpoint scope. Aliased forwarding/proxy ports
  cannot reliably be identified as one deployment: restrict scope to one
  endpoint per deployment. Do not scrape the same cluster-head telemetry twice.
- The exporter's own configured port is excluded. Automatic mode requires a
  fixed nonzero listen port. Another exporter on another port is not an engine.

Limits: four probes at once, 64 candidates per collection, 1 MiB per metrics or
metadata body and per listener-table/command output, one second per candidate,
and a shared `--timeout` deadline (default 5s). Probe order rotates so slow ports
cannot permanently starve later ones. Excess candidates report `candidate_limit`;
the deadline reports `deadline`; failed enumeration reports `scope_error`.
There is no negative-cache backoff: retry is the next collection. A healthy
engine normally appears on the next collection; with competing listeners it
can take several rotations, and there is no guaranteed detection latency for
an engine whose responses exceed the deadline. Late request results cannot
mutate later collections. HTTP responses are never logged by discovery.

## Evidence, identity and availability

Detection reuses the existing adapters and captured fixture evidence; see
[adapter semantics](adapters.md) and [fixture provenance](../testdata/README.md).
Synthetic switching tests supplement, not replace, those real captures.

| Engine | Required signature | Model evidence |
| --- | --- | --- |
| vLLM | `vllm:generation_tokens_total` plus `vllm:prompt_tokens_by_source_total` or `vllm:cache_config_info` | Unique `model_name` on generation counters |
| llama.cpp | `llamacpp:tokens_predicted_total` and `llamacpp:prompt_tokens_total` | Unique `model` label, otherwise `/v1/models` |
| SGLang | `sglang:generation_tokens_total` and `sglang:realtime_tokens_total` | Unique `model_name` on generation counters |

A generic OpenAI API, HTTP 200 or `vllm:` prefix is insufficient. Any
`mlx_serve:` family vetoes a native vLLM match: mlx-serve is recognized but
unsupported. Mixed native signatures or multiple model identities are
`ambiguous`, with no measurements. Signatures are supported-version evidence,
not cryptographic proof: unknown forks/compatibility layers may require new
fixtures and detection rules. ds4 remains explicitly configured/log-only and
supplies speculative counters, not general token totals. MTPLX and Ollama
remain unimplemented; no automatic HTTP fallback invents their counts.

If telemetry lacks a model label, `/v1/models` must return exactly one nonempty
ID. Metadata is read before and after a fresh metrics snapshot; engine signature
and model evidence must agree. Failure is `identity_missing` or `ambiguous`,
never a fabricated model name. Non-atomic APIs cannot prove instance continuity
through an invisible replacement; see reset limitations below.

Automatic identity:

- `engine` and `model`: observed identity. Unknown diagnostic identity uses
  `unknown`, never token measurements with a guessed model.
- `backend`: `auto-` plus a stable hash of the canonical endpoint. Concurrent
  ports remain distinct. No raw URL or per-scrape/run ID is a label.
- `host`: existing `--host` policy. `worker`: existing adapter reset boundaries.
- `nodes`: `unknown` by default, not guessed from worker/rank/RDMA counts.
  `--discovery-nodes=1..64` asserts **all** discovered engines always use that
  physical node count. Compose uses `LLM_EXPORTER_DISCOVERY_NODES`. Use unknown
  for heterogeneous/changing topology or pins for per-deployment topology;
  a static assertion cannot automatically verify that topology changed.

`llme_exporter_discovery_status{<identity>,state}` is 1 for the current state: `ready`,
`discovering` (empty scope), `unavailable`, `unknown`, `unsupported`, `ambiguous`, `identity_missing`, or a
scope/limit/deadline state above. Scope diagnostics use backend `auto-discovery`.
`llme_engine_up` is 1 only for a successful identified adapter result. Token
availability still uses `llme_exporter_metric_available`; readable telemetry need not
provide every measurement. Missing values never become zero.

After disappearance/read failure, last-known identity remains down, with no old
measurement samples. A newly observed incompatible body replaces the old
identity with its diagnostic state rather than pretending the old engine is
still present. Absent endpoints expire after five minutes since their last
completed probe. Retained diagnostics are capped at 256 endpoints, oldest first;
actively probed non-engine ports can remain diagnostics. All diagnostic series
still carry engine/model. Pinned targets remain down indefinitely until removed.

## Pins and migration

Explicit registrations retain their identity, validation, adapter lifecycle,
run-ID-aware removal and stale-registration behavior. Valid pins exclude the
same loopback port from auto discovery, and backend-name collisions yield to
pins. This is deliberately conservative across address families. Arbitrary
DNS/proxy aliases cannot be deduplicated; narrow discovery scope in that case.
Invalid registrations are diagnosed but do not reserve a target.

To migrate a previously pinned local engine to auto: remove the pin with its
matching backend/run ID (see [pinned quickstart](pinned-quickstart.md)), enable
`--discovery=local`, and leave the exporter running. Merely enabling auto does
not override a pin. `--discovery=off` preserves registration-only operation.
Compose reads `LLM_EXPORTER_DISCOVERY`, `LLM_EXPORTER_DISCOVERY_NODES` and
`LLM_EXPORTER_DISCOVERY_ENDPOINTS` from `.env`; the native binary uses CLI flags.
No `LLM_ENGINE`, pinned model or per-switch register command is needed for auto.
Removing a pin in automatic mode makes its local endpoint discoverable again.

Historical policy (before issue #13): “Registration is identity. Labels come
from the registration file, and the engine's own model name only validates it.”
The original rationale was variable recipe ports and third-party launchers,
so launchers registered and deregistered arms. The operator's
[#13](https://github.com/evanwtf/llm-metrics-exporter/issues/13) requirement
supersedes registration-only discovery, **not** the authoritative identity of
deliberately pinned deployments or the prohibition on guessed labels.

## Throughput and reset boundaries

Keep raw canonical counters and separate computed prefill, cached tokens and
decode. Never sum raw worker counters before `rate`. The exporter does not
splice old counts onto new engines or synthesize a tok/s gauge. Both direct
scrapes and remote-write collections use the same manager. Remote write marks
retired series stale; queued history keeps its original labels and timestamps.

PromQL retains historical samples inside a range even when an instant series
disappears. Use this engine-independent query unchanged through switches:

```promql
sum by (host, phase) (
  rate(llme_tokens_total[1m])
  and on (engine, model, backend, host, nodes) (llme_engine_up == 1)
  unless on (engine, model, backend, host, nodes)
    (time() - llme_exporter_discovery_changed_timestamp_seconds < 60)
)
```

This keeps prefill/decode separate, excludes offline/retired engines immediately,
and excludes a full one-minute rate window after an observed transition.
It deliberately shows a gap, not false zero throughput, during downtime/warm-up.
For another window, change both `[1m]` and `60` consistently; retain engine/model/
backend in the aggregation if separate panels are desired. Existing plain rate
queries still work but can retain old history or bridge a same-label replacement.
Pinned targets have no discovery timestamp; their existing reset policy applies.

`llme_exporter_discovery_changed_timestamp_seconds` advances on first observation,
engine/model change, listener generation change (Linux socket inode, macOS PID),
upstream `process_start_time_seconds` change when available, or recovery from a
failed collection. It is a gauge, not an unbounded identity label. Successful
unchanged observations leave it stable. Reject benchmark deltas across a change
in this timestamp as well as counter decreases, identity changes and worker resets.

Sampling limitation: same-engine/model replacements entirely between observations
can be invisible if OS/process-start evidence is absent or reused (notably an
explicit URL behind a proxy). A new process may already have a higher counter,
so `rate()` alone cannot prove continuity. This exporter cannot guarantee detection
of such resets. Exporter restart establishes a new warm-up boundary. Metadata
bracketing also cannot detect an unobserved A→B→A change during the read.

## Regression coverage

`go test ./internal/discovery ./internal/engines ./cmd/llm-metrics-exporter
./internal/remotewrite` covers captured signatures, OS listeners, hot switching,
identity ambiguity, scope restrictions, deadlines, raw counters/transition
timestamps, and outage/restart/replay/staleness without exporter HTTP scrapes.
Tests use controlled HTTP engines; they do not stop a production model server.
Run `make check` and `make race` for the full suite and concurrency checks.
Linux CI also runs `promtool test rules` on
`internal/discovery/testdata/rates.test.yml`, using the existing Compose
Prometheus image. It tests the unchanged query through offline periods,
engine switches and same-label reincarnation with higher raw counters; steady
throughput is 1 tok/s and transition windows are absent, not spikes or zeroes.
