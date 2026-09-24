# Run the exporter and scrape it with Prometheus

The binary and recommended job name are `llm-metrics-exporter`.
This guide uses direct scraping: Prometheus → exporter → vLLM. A Prometheus
Agent and remote-write receiver are not needed for this topology.
Replace placeholders with your local settings; keep real host details outside
this public repository. For an isolated live test, see the
[cheat sheet](cheat-sheet.md).
For laptops or hosts that cannot accept inbound scrapes, use
[built-in remote write](remote-write.md) instead.

## A. Start the exporter on the inference host

From this repository's root, build and install:

```bash
GO_BIN="${GO_BIN:-$HOME/go/bin/go}"
"$GO_BIN" build -o dist/llm-metrics-exporter ./cmd/llm-metrics-exporter
install -d "$HOME/.local/bin"
install -m 0755 dist/llm-metrics-exporter "$HOME/.local/bin/llm-metrics-exporter"
```

Find the running engine's base URL and exact model name:

```bash
ENGINE_URL='http://127.0.0.1:<vllm-port>'
curl -fsS --max-time 10 "$ENGINE_URL/v1/models" | jq -r '.data[].id'
curl -fsS --max-time 10 "$ENGINE_URL/metrics" |
  rg '^vllm:(generation_tokens_total|prompt_tokens_by_source_total)\{'
```

Register it as the same user that will run the exporter:

```bash
SERVED_MODEL='<exact model_name from metrics>'
BACKEND='vllm-main'
RUN_ID="manual-$(date +%s)"
NODES=1   # use 2 when the server spans two physical nodes
BIN="$HOME/.local/bin/llm-metrics-exporter"

"$BIN" register --engine vllm --endpoint "$ENGINE_URL" \
  --model "$SERVED_MODEL" --served-model "$SERVED_MODEL" \
  --backend "$BACKEND" --nodes "$NODES" --run-id "$RUN_ID"
```

`--model` is the exported identity; `--served-model` selects and validates the
upstream model. Using the same value is the simplest setup. Record `RUN_ID` if
you will deregister later. The default registration directory is
`${XDG_STATE_HOME:-$HOME/.local/state}/llm-metrics-exporter/registrations`.
If you override it, pass the same `--registration-dir` to register and serve.

Run in the foreground first:

```bash
"$BIN" serve --listen :9109 --timeout 5s
```

This listens on all interfaces so a remote Prometheus can connect. Restrict
network access to trusted monitoring clients; this listener has no built-in
authentication or TLS. vLLM itself may remain on loopback because the exporter
reads it locally. From another terminal:

```bash
curl -fsS --max-time 10 http://127.0.0.1:9109/metrics |
  rg '^llm_(engine_up|registration_mismatch|tokens_total|prompt_cached_tokens_total)\{'
```

Expect engine up `1`, mismatch `0`, and decode/prefill counters when the
upstream supports them. `/healthz` alone does not verify engine collection.

For persistent Linux operation, stop the foreground exporter with Ctrl-C,
then install the supplied user service:

```bash
install -d "$HOME/.config/systemd/user"
install -m 0644 packaging/systemd/llm-metrics-exporter.service \
  "$HOME/.config/systemd/user/llm-metrics-exporter.service"
systemctl --user daemon-reload
systemctl --user enable --now llm-metrics-exporter
systemctl --user status llm-metrics-exporter
journalctl --user -u llm-metrics-exporter -n 50 --no-pager
```

For startup before login, enable lingering once with
`sudo loginctl enable-linger "$USER"`. The unit uses the default registration
directory and listener. If your shell sets `XDG_STATE_HOME`, ensure the service
has the same setting or use an explicit registration directory in a unit
override. macOS and Docker alternatives are in the [README](../README.md).

The exporter re-reads registrations on every scrape, so changing the vLLM port
requires re-registering that backend, not changing Prometheus. When stopping
the engine intentionally, remove its registration using the original run ID:

```bash
"$BIN" deregister --backend "$BACKEND" --run-id "$RUN_ID"
```

## B. Configure the Prometheus server

Add this job under the existing `scrape_configs:` list in `prometheus.yml`.
Do not add a second top-level `scrape_configs` key or replace the other jobs.
The example follows a server with a 10-second global scrape interval:

```yaml
  - job_name: llm-metrics-exporter
    scrape_interval: 10s
    scrape_timeout: 8s
    metrics_path: /metrics
    static_configs:
      - targets:
          - '<inference-host>:9109'
```

Use the inference host's address reachable **from Prometheus**, not the vLLM
port. In a bridge-networked Prometheus container, `127.0.0.1` refers to that
container. Add one target per exporter host; a two-node engine needs a
registration only on the API/head host, not a duplicate on its worker.
The 8-second scrape timeout leaves room above the exporter's 5-second timeout
and stays below the 10-second interval. See the official
[scrape configuration reference](https://prometheus.io/docs/prometheus/latest/configuration/configuration/).

The exporter supplies `engine`, `model`, `backend`, `host`, and `nodes`.
Prometheus adds `job` and `instance`. Avoid setting conflicting identity labels
in `static_configs`; the default label handling works for this example.

### Docker-hosted Prometheus: validate and reload

Edit the version-controlled config and deploy it to the host file bind-mounted
at `/etc/prometheus/prometheus.yml` in the container. Editing a local repository
copy does not update a remote container automatically. Use your infrastructure
repository's config deployment procedure and inspect the proposed diff first.

On the Prometheus host, after the updated config is mounted:

```bash
docker exec prometheus promtool check config /etc/prometheus/prometheus.yml &&
  docker kill --signal=HUP prometheus
docker logs --since 2m prometheus
```

Despite the command name, this sends SIGHUP to reload configuration; it does
not terminate the container. Only reload after validation passes. A config-sync
tool that reloads nginx does not also reload Prometheus. If a deployment uses
atomic file replacement and the container still sees old contents through a
single-file bind mount, recreate just the Prometheus service using its Compose
file so the mount picks up the new file, then validate again.

An HTTP `POST /-/reload` is an alternative only when Prometheus was started
with `--web.enable-lifecycle`. SIGHUP works without that flag. See the official
[reload documentation](https://prometheus.io/docs/prometheus/latest/management_api/#reload).

### Verify ingestion

Open Prometheus's Targets page and find `llm-metrics-exporter`. It should be UP.
Then query:

```promql
up{job="llm-metrics-exporter"}
llm_engine_up{job="llm-metrics-exporter"}
llm_tokens_total{job="llm-metrics-exporter"}
```

`up=1` means Prometheus scraped the exporter; `llm_engine_up=1` means the
exporter read the registered engine. Both checks matter. No registrations can
produce a successful scrape with no engine/token series.

After several scrapes, query throughput separately by phase:

```promql
sum by (host, engine, model, backend) (
  rate(llm_tokens_total{job="llm-metrics-exporter",phase="decode"}[1m])
)
sum by (host, engine, model, backend) (
  rate(llm_tokens_total{job="llm-metrics-exporter",phase="prefill"}[1m])
)
```

Prefill excludes cache hits; cached tokens are
`llm_prompt_cached_tokens_total`. Idle counters yield zero throughput.
Keep existing native engine scrape jobs until dashboards are migrated; their
`vllm:*`/`llamacpp:*` metric names differ from `llm_*`. Avoid collecting the same
exporter via both direct scraping and an Agent's remote write into the same
destination unless you deliberately handle duplicate ingestion.
