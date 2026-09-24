# vLLM live-test cheat sheet

Build the exporter, point an isolated registration at an existing vLLM server,
and verify token counters end to end. Run from the repository root in Bash.
Tools: Go (see `go.mod`), curl, jq, and ripgrep (`rg`). No Prometheus server is
needed for this check. Do not commit real endpoints, hostnames, or raw captures.
For persistent operation and direct Prometheus scraping, see
[Run the exporter and scrape it with Prometheus](prometheus-setup.md).

## 1. Build

```bash
# Use an explicit path when Go is not on PATH.
GO_BIN="${GO_BIN:-$HOME/go/bin/go}"
"$GO_BIN" version
"$GO_BIN" build -o dist/llm-metrics-exporter ./cmd/llm-metrics-exporter
./dist/llm-metrics-exporter version
```

If the normal Go cache is read-only, or GOPATH equals GOROOT, set separate
writable directories before building:

```bash
export GOCACHE="$(mktemp -d /tmp/llm-go-cache.XXXXXX)"
export GOPATH="$(mktemp -d /tmp/llm-go-path.XXXXXX)"
```

`dist/` is ignored by Git. Optional code checks:

```bash
"$GO_BIN" test -race ./...   # needs cgo and a C compiler
"$GO_BIN" vet ./...
```

## 2. Find and inspect the engine

On Linux, inspect listening ports and running containers:

```bash
ss -ltnp
docker ps --format '{{.Image}} {{.Ports}}'   # if using Docker
```

Host-networked containers may have an empty Ports column. Use the engine's
launch configuration and confirm the endpoint below. Replace the placeholders:

```bash
ENGINE_URL='http://127.0.0.1:<engine-port>'
curl -fsS --max-time 10 "$ENGINE_URL/v1/models" | jq -r '.data[].id'
curl -fsS --max-time 10 "$ENGINE_URL/metrics" |
  rg '^vllm:(generation_tokens_total|prompt_tokens_by_source_total|prompt_tokens_cached_total|num_requests_running|num_requests_waiting)\{'

SERVED_MODEL='<exact model_name label from metrics>'
NODES=1   # physical nodes serving this model; use 2 for a two-node deployment
```

Choose the exact upstream `model_name`, not a filesystem path or guessed alias.
Always pass `--served-model` so other models are not accidentally included.
The current exporter expects an accessible base URL and appends `/metrics`;
it has no configurable bearer-token header or custom metrics path.

## 3. Register and start an isolated exporter

Choose a free local exporter port. This temporary registration directory avoids
altering registrations used by an existing exporter service.

```bash
TEST_DIR="$(mktemp -d /tmp/llm-exporter-test.XXXXXX)"
EXPORTER_ADDR='127.0.0.1:<free-exporter-port>'
EXPORTER_URL="http://$EXPORTER_ADDR"
BIN="$PWD/dist/llm-metrics-exporter"

"$BIN" register --registration-dir "$TEST_DIR/registrations" \
  --backend smoke-test --engine vllm --endpoint "$ENGINE_URL" \
  --model "$SERVED_MODEL" --served-model "$SERVED_MODEL" \
  --nodes "$NODES" --run-id smoke-test

"$BIN" serve --registration-dir "$TEST_DIR/registrations" \
  --host local-test --listen "$EXPORTER_ADDR" &
EXPORTER_PID=$!

# Retry briefly while the listener starts.
curl -fsS --retry 5 --retry-connrefused --retry-delay 1 \
  --max-time 10 "$EXPORTER_URL/healthz"
```

Keep this shell open for the remaining commands. `healthz` checks exporter
liveness only; the next step verifies actual engine collection.

## 4. Compare counters

```bash
curl -fsS --max-time 10 "$EXPORTER_URL/metrics" |
  rg '^llm_(engine_up|registration_mismatch|exporter_scrape_errors_total|tokens_total|prompt_cached_tokens_total)\{'
```

Expect `llm_engine_up 1`, `llm_registration_mismatch 0`, and scrape errors `0`.
Compare values using this mapping, selecting only the registered model upstream:

| Exporter | vLLM source |
|---|---|
| `llm_tokens_total{phase="decode"}` | `vllm:generation_tokens_total` |
| `llm_tokens_total{phase="prefill"}` | `vllm:prompt_tokens_by_source_total{source="local_compute"}` |
| `llm_prompt_cached_tokens_total` | `vllm:prompt_tokens_cached_total` |

The exporter preserves data-parallel workers in the `worker` label. Compare
matching workers, or sums at an idle instant. Exact equality between HTTP scrapes
requires an idle engine; active traffic may advance counters between reads.

## 5. Prove the counters advance

This sends one small inference request. For an exact per-request comparison,
use a quiet engine with no running or waiting requests and no other clients.
First record the counters from step 4, then run:

```bash
jq -n --arg model "$SERVED_MODEL" \
  '{model: $model, prompt: "Exporter token counter smoke test. Count from one to five:", max_tokens: 16, temperature: 0, stream: false}' |
  curl -fsS --max-time 120 "$ENGINE_URL/v1/completions" \
    -H 'Content-Type: application/json' --data-binary @- |
  jq '{usage, error, finish_reason: .choices[0].finish_reason}'
```

Repeat the upstream scrape in step 2 and exporter scrape in step 4 after the
request finishes. Allow upstream metric reporting to catch up if necessary.
Decode should increase by `usage.completion_tokens`. Computed prefill can be
less than `usage.prompt_tokens` when the prompt hits a cache; inspect the cached
counter separately. Do not treat total prompt usage as computed prefill.

One live check on 2026-09-23 produced these exact matching values in both vLLM
and the exporter:

| Counter | Before | After | Delta |
|---|---:|---:|---:|
| Computed prefill | 183713 | 183725 | 12 |
| Decode | 6340 | 6356 | 16 |
| Cached prompt | 448000 | 448000 | 0 |

The response reported 12 prompt tokens and 16 completion tokens. Engine health
was 1, model mismatch was 0, and scrape errors were 0. This confirms collection
for that deployment; it does not validate independent worker resets or every
vLLM configuration.

## 6. Clean up the test

In the same shell, stop only the exporter started above and remove its temporary
registration. Leave the inference server running.

```bash
"$BIN" deregister --registration-dir "$TEST_DIR/registrations" \
  --backend smoke-test --run-id smoke-test
kill "$EXPORTER_PID"
wait "$EXPORTER_PID"
```

The built binary remains in `dist/`. The temporary directory retains only
registration bookkeeping. For permanent service installation, see the
[README](../README.md#pinned-bootstrap-optional) and `packaging/` examples.

## Troubleshooting and rates

- Engine up is 0: inspect exporter logs for connection, parse, or model errors.
- Model mismatch is 1: compare `--served-model` with upstream `model_name`.
- Prefill is absent: older vLLM versions may lack the computed-token source.
  Absence means unavailable, not zero; do not substitute total prompt usage.
- Counters stay flat: verify traffic reaches the selected model and endpoint.
- Counts differ: check concurrent traffic, worker aggregation, cache hits, and
  restarts. Discard benchmark deltas spanning a known restart even if the final
  counter has already exceeded the initial value.
- Independent worker reset handling is tracked in
  [issue #2](https://github.com/evanwtf/llm-metrics-exporter/issues/2) (historical
  context). Worker preservation is implemented; see
  [the current label contract](design.md#labels). The single live check above
  still does not prove every worker/reset scenario.

Once Prometheus scrapes the exporter, query the phases separately:

```promql
sum by (host, engine, model, backend) (rate(llm_tokens_total{phase="decode"}[1m]))
sum by (host, engine, model, backend) (rate(llm_tokens_total{phase="prefill"}[1m]))
```

These are tokens per wall-clock second over the query window. The raw counters
are cumulative since the engine started, not tokens generated by the scrape.
