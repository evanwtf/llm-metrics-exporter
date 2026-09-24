# Quickstart

Run these commands from the cloned repository root. Choose **native** or
**Docker**, not both on the same port. You need an existing vLLM server and
curl; this exporter observes the server, it does not launch a model. No
Prometheus server is needed to see local token counts.

## 1. Point at your engine

For Docker, skip directly to **2B** and put these settings in `.env` instead.
For native execution:

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
./dist/llm-metrics-exporter serve --listen 127.0.0.1:9109
```

The exporter runs in the foreground; leave it running and open another terminal
for step 3. Ctrl-C stops the exporter, not vLLM. Without Make, build with
`go build -trimpath -o dist/llm-metrics-exporter ./cmd/llm-metrics-exporter`.
For read-only cache/GOPATH problems, see [build troubleshooting](docs/cheat-sheet.md#1-build).

## 2B. Docker Compose: Linux

Requires Docker with Compose; no host Go or Make installation is needed. Native
execution is the supported path on macOS because Docker's VM cannot read host
loopback engines. From the repository root:

```sh
cp -n .env.example .env          # preserve an existing local .env
id -u                          # put this value in LLM_EXPORTER_UID
id -g                          # put this value in LLM_EXPORTER_GID
```

Edit `.env`: set `LLM_ENGINE_ENDPOINT`, `LLM_ENGINE_MODEL`, and
`LLM_ENGINE_SERVED_MODEL` for your engine; set a unique stable
`LLM_EXPORTER_HOST`. Set `LLM_EXPORTER_LISTEN=127.0.0.1:9109` for local-only
access. Set UID/GID from the commands above. Adjust engine type and node count
if needed. Then:

```sh
mkdir -p "$HOME/.local/state/llm-metrics-exporter/registrations"
docker compose run --rm --build register &&
  docker compose up -d --build exporter
docker compose ps exporter
```

No exports, sourcing or engine flags are needed: Compose reads `.env` itself.
If you set a custom registration directory, create that exact path instead.
After editing engine settings, rerun the registration command; restarting the
exporter alone does not apply engine edits. Defaults are backend `local-model`
and run ID `static`. Changing backend leaves the old registration intact.
Choose an unused backend if it already exists. The writer needs write access;
the nonroot exporter needs read/traverse access and retains a read-only mount.
See [Compose settings](docs/compose.md) for all options, custom directories,
multiple targets, cleanup and the previous ad hoc shell workflow, and
[private configuration](docs/security.md) for safe handling.

## 3. Confirm collection

Run after either startup path (retry if the listener is still starting):

```sh
curl -fsS --max-time 10 http://127.0.0.1:9109/metrics |
  grep -E '^llm_(engine_up|registration_mismatch|metric_available|tokens_total|prompt_cached_tokens_total)\{'
```

For your backend (`quickstart` natively, `local-model` by default with Compose),
expect engine up `1`, mismatch `0`, and token
counters where the engine supports them. Prefill counts computed tokens;
cache hits are separate. Idle counters may be zero or flat. Missing prefill
can mean unsupported upstream telemetry, not zero work. `/healthz` checks only
process liveness. See the [live-test cheat sheet](docs/cheat-sheet.md) to generate
a request and verify counter deltas.

## Next steps

- [Prometheus scraping and native services](docs/prometheus-setup.md): the
  local-only listener above must be made reachable for a remote scraper; restrict
  access to trusted clients because the exporter has no built-in TLS/authentication.
- [Remote write](docs/remote-write.md): outbound delivery for laptops or changing
  IPs, receiver setup, persistent buffering and authentication. Do not also scrape
  the same series into that receiver without handling duplicates.
- [Other engines and static targets](docs/static-targets.md),
  [CLI reference](docs/cli.md), and [development checks](docs/development.md).

When retiring the deployment, deregister `quickstart` with its matching run ID.
For the native path:

```sh
./dist/llm-metrics-exporter deregister --backend quickstart --run-id quickstart
```

For Docker, use the backend/run ID you configured (defaults below):

```sh
docker compose run --rm register deregister --registration-dir /registrations \
  --backend local-model --run-id static
docker compose stop exporter
```

Deregistration only stops observation; it never stops the model server.
