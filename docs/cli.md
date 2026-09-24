# CLI and HTTP reference

The binary is `llm-metrics-exporter`. With no subcommand it runs `serve`.
This reference is derived from [`main.go`](../cmd/llm-metrics-exporter/main.go)
and [`registration.go`](../internal/registration/registration.go), not a live
execution. See [static targets](static-targets.md) for YAML and
[Prometheus setup](prometheus-setup.md) for a complete vLLM example.

## Commands

| Command | Purpose |
| --- | --- |
| `serve` | Collect registered engines and expose HTTP; optionally send remote write |
| `register` | Validate and atomically write `<backend>.yaml` |
| `deregister` | Remove only the registration owned by the given run ID |
| `health --url http://127.0.0.1:9109/healthz` | Check exporter liveness; this URL is the default |
| `version` or `--version` | Print version, revision and Go version |

`serve`, `register`, `deregister` and `health` accept `--help`. Flag help returns
exit code 2, not 0. Exit codes are 0 for success, 1 for operational failure,
2 for usage/validation failure, and 3 when deregistration encounters a different
run ID. Deregistering an absent file succeeds. Diagnostics use stdout; `serve`
supports `--log-level info` (default), `debug`, `warn`, or `error`. An invalid log
level falls back to info.
`health` also accepts `--timeout`, default `3s`, and requires an HTTP 200 response.

## Registration and identity

`register` requires `--engine`, `--endpoint` (engine base URL), `--model`
(exported identity), and `--backend` (deployment identity and filename).
Optional flags are `--nodes` (default 1, allowed 1–64), `--run-id` (default
`static`), `--issue` (default 0), and `--served-model` (upstream model filter and
validation). ds4 requires an absolute `--log-path`; the reserved MTPLX engine
requires an absolute `--trace-path`, but its adapter is not implemented.

`deregister` requires `--backend` and `--run-id`; retain the launcher's run ID
for cleanup. It neither stops the model server nor stops the exporter.

All three registration-related commands (`serve`, `register`, `deregister`)
accept `--registration-dir`. The default is
`${XDG_STATE_HOME:-$HOME/.local/state}/llm-metrics-exporter/registrations`.
Pass the same override to all commands and service units. Static YAML requires
`version: 1`, `nodes`, and the required identity fields; unknown fields fail
validation. See [discovery](design.md#discovery-arms-register-themselves) for
model validation, atomic writes and stale-registration policy.

Seven engine names are accepted in registrations, but only `vllm`, `llamacpp`,
`sglang` and `ds4` are compiled adapters. `mlx-serve`, `mtplx` and `ollama` are
planned; registering them does not implement collection and reports engine down.

## Serving and delivery

| Flag | Default / meaning |
| --- | --- |
| `--listen` | `:9109` (all interfaces); use `127.0.0.1:9109` for local-only access |
| `--host` | `LLM_EXPORTER_HOST`, otherwise short hostname; must be nonempty |
| `--timeout` | `5s`, positive per-deployment collection timeout; below scrape timeout |
| `--registration-dir` | State directory described above |
| `--log-level` | `info` |

Remote-write flags, defaults, queue limits, authentication and replay semantics
have one reference in [remote-write.md](remote-write.md#built-in-sender).
Remote write is disabled unless `--remote-write-url` is set. Changing
`--registration-dir` does **not** change the default remote-write directory;
set `--remote-write-dir` explicitly when isolating state.

| HTTP path | Meaning |
| --- | --- |
| `/` | Version and endpoint summary |
| `/metrics` | Canonical `llm_*` series only; no Go/process metrics |
| `/healthz` | Process liveness (`ok`), not engine health or successful delivery |
| `/remote-write/status` | Local delivery-status JSON; only with remote write enabled |

The listener has no built-in authentication or TLS. Restrict access to trusted
clients or loopback. Engine HTTP adapters append `/metrics` to the base URL;
there is no configurable authentication header or custom metrics path. The
optional bearer-token file authenticates **outbound remote write**, not reads
from engines. See [credential handling](security.md).

The binary directly reads `LLM_EXPORTER_HOST` and `XDG_STATE_HOME`; the
`LLM_EXPORTER_REGISTRATION_DIR`, `LLM_EXPORTER_LISTEN`, `REVISION` and
`PROMETHEUS_AGENT_*` variables in [`.env.example`](../.env.example) are Compose
configuration/build inputs, not equivalent native CLI environment flags.
Compose also maps `LLM_ENGINE*` settings to the one-shot `register` service's
CLI flags, and `LLM_EXPORTER_UID`/`LLM_EXPORTER_GID` to that service's user.
The native binary does not load `.env`. See [Compose setup](compose.md).
