# Pinned-registration quickstart

This is the manual workflow previously used by the root quickstart (before
issue #13). Use it for deliberate fixed identity, remote endpoints or log
adapters. For engine switching without configuration, use the
[automatic quickstart](../QUICKSTART.md). `--discovery=off` makes deregistration
stop observation; otherwise a removed local pin becomes discoverable again.

Run these commands from the cloned repository root. Choose **native** or
**Docker**, not both on the same port. You need an existing vLLM server and
curl; this exporter observes the server, it does not launch a model. No
Prometheus server is needed to see local token counts.

## 1. Point at your engine

Replace the placeholders; keep real endpoints and model identities out of Git.
The engine URL is its base URL, without `/metrics`:

```sh
ENGINE_URL='http://127.0.0.1:<vllm-port>'
curl -fsS --max-time 10 "$ENGINE_URL/v1/models"
SERVED_MODEL='<exact model_name from the engine metrics>'
NODES=1                         # use 2 for a two-node server
```

Use the reported model ID, confirming it against `model_name` in
`curl -fsS --max-time 10 "$ENGINE_URL/metrics"` if needed. Choose an unused
backend name in the commands below if `quickstart` already names a deployment;
registering it again replaces its registration.

## 2A. Native: Linux or macOS

Requires Go from `go.mod` and Make. If Go is not on PATH, replace `make build`
with `make build GO="$HOME/go/bin/go"`.

```sh
make build
./dist/llm-metrics-exporter register \
  --backend quickstart --engine vllm --endpoint "$ENGINE_URL" \
  --model "$SERVED_MODEL" --served-model "$SERVED_MODEL" --nodes "$NODES" \
  --run-id quickstart
./dist/llm-metrics-exporter serve --discovery=off --listen 127.0.0.1:9109
```

The exporter runs in the foreground; leave it running and open another terminal
for step 3. Ctrl-C stops the exporter, not vLLM. Without Make, build with
`go build -trimpath -o dist/llm-metrics-exporter ./cmd/llm-metrics-exporter`.
For read-only cache/GOPATH problems, see [build troubleshooting](cheat-sheet.md#1-build).

## 2B. Docker Compose: Linux

Requires Docker with Compose; no host Go or Make installation is needed. Native
execution is the supported path on macOS because Docker's VM cannot read host
loopback engines. These commands use the checked-in Compose service and keep
the exporter listener local-only:

```sh
export LLM_EXPORTER_REGISTRATION_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/llm-metrics-exporter/registrations"
export LLM_EXPORTER_HOST='inference-example' # choose a unique stable identity
export LLM_EXPORTER_DISCOVERY=off
export LLM_EXPORTER_LISTEN='127.0.0.1:9109'
mkdir -p "$LLM_EXPORTER_REGISTRATION_DIR"
docker compose build exporter

# One-shot registration: writable mount and your host UID/GID for this command.
docker compose run --rm --no-deps --user "$(id -u):$(id -g)" \
  --volume "$LLM_EXPORTER_REGISTRATION_DIR:/registrations:rw" \
  exporter register --registration-dir /registrations \
  --backend quickstart --engine vllm --endpoint "$ENGINE_URL" \
  --model "$SERVED_MODEL" --served-model "$SERVED_MODEL" --nodes "$NODES" \
  --run-id quickstart

# The long-running service keeps the normal read-only registration mount.
docker compose up -d --build exporter
docker compose ps exporter
```

Run subsequent Compose commands from this same shell so the overrides remain
set. For persistent overrides, use the ignored `.env` based on `.env.example`.
The image runs as nonroot: it needs read/traverse access to the registration
directory and files. Keep credentials out of registrations; see
[private configuration](security.md). The initial build makes the binary
available for registration; `up --build` can then reuse its cache. For the
same registration driven by `.env` (no exports), use the one-shot `register`
service in [Compose settings](compose.md) with `LLM_EXPORTER_DISCOVERY=off`.

## 3. Confirm collection

Run after either startup path (retry if the listener is still starting):

```sh
curl -fsS --max-time 10 http://127.0.0.1:9109/metrics |
  grep -E '^llm_(engine_up|registration_mismatch|metric_available|tokens_total|prompt_cached_tokens_total)\{'
```

For `backend="quickstart"`, expect engine up `1`, mismatch `0`, and token
counters where the engine supports them. Prefill counts computed tokens;
cache hits are separate. Idle counters may be zero or flat. Missing prefill
can mean unsupported upstream telemetry, not zero work. `/healthz` checks only
process liveness. See the [live-test cheat sheet](cheat-sheet.md) to generate
a request and verify counter deltas.

## Next steps

- [Prometheus scraping and native services](prometheus-setup.md): the
  local-only listener above must be made reachable for a remote scraper; restrict
  access to trusted clients because the exporter has no built-in TLS/authentication.
- [Remote write](remote-write.md): outbound delivery for laptops or changing
  IPs, receiver setup, persistent buffering and authentication. Do not also scrape
  the same series into that receiver without handling duplicates.
- [Other engines and static targets](static-targets.md),
  [CLI reference](cli.md), and [development checks](development.md).

When retiring the deployment, deregister `quickstart` with its matching run ID.
For the native path:

```sh
./dist/llm-metrics-exporter deregister --backend quickstart --run-id quickstart
```

For Docker, reuse the one-shot `docker compose run` prefix above with
`exporter deregister --registration-dir /registrations --backend quickstart
--run-id quickstart`, then `docker compose stop exporter` to stop that service.
Deregistration only stops observation; it never stops the model server.
