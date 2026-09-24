# Enable the Prometheus remote-write receiver

Remote write lets an inference host send observations outbound to a stable
Prometheus address. The inference host can change IP addresses; Prometheus
does not need a scrape target for it.

## Docker Compose server

In the infrastructure repository, edit `docker-compose/prometheus-stack.yml`.
Keep the existing Prometheus command arguments and append:

```yaml
services:
  prometheus:
    command:
      - '--config.file=/etc/prometheus/prometheus.yml'
      - '--storage.tsdb.path=/prometheus'
      - '--storage.tsdb.retention.size=100GB'
      - '--web.enable-remote-write-receiver'
```

This is a **startup flag**, not a `prometheus.yml` setting. On the monitoring
host, validate and recreate only the Prometheus service:

```bash
docker compose -f docker-compose/prometheus-stack.yml config --quiet
docker exec prometheus promtool check config /etc/prometheus/prometheus.yml
docker compose -f docker-compose/prometheus-stack.yml up -d --no-deps prometheus
docker logs --since 2m prometheus
```

Run from the infrastructure repository root. The existing data volume is
retained. SIGHUP or `POST /-/reload` cannot enable a new startup flag. No image
pull, volume deletion, or changes to unrelated services are needed.

For a native service, append the same flag to its existing ExecStart/command
and restart Prometheus using that service manager.

## Endpoint and access

Configure the sender with `https://<prometheus-host>/api/v1/write`, or
`http://<prometheus-host>:9090/api/v1/write` on a trusted private network.
Any reverse proxy must forward POST requests and preserve the body and protocol
headers. Use TLS and authentication when traversing an untrusted network;
enabling the receiver alone does not add authentication. No `remote_write:`
section or new scrape job is needed on the receiving server.

Inspect the running flags from a host that can reach Prometheus:

```bash
PROMETHEUS_URL='http://<prometheus-host>:9090'
curl -fsS "$PROMETHEUS_URL/api/v1/status/flags" |
  jq '.data["web.enable-remote-write-receiver"]'
```

Expect `"true"`. A GET or JSON POST to `/api/v1/write` is not a delivery test:
the endpoint requires Snappy-compressed protobuf. Verify actual delivery by
querying `llme_tokens_total` and `llme_engine_up` after starting a sender.

Remote write does not create Prometheus's scrape-generated `up` series. Monitor
arrival/freshness of the exporter series, and distinguish a disconnected laptop
from a reachable exporter reporting `llme_engine_up 0`. No samples are collected
while a laptop is asleep or the exporter is stopped.

Do not also scrape the same exporter into this Prometheus unless you deliberately
handle duplicate ingestion. A separate Prometheus Agent is another supported
sender; see `packaging/prometheus-agent/agent.yml`.

References: [receiver API](https://prometheus.io/docs/prometheus/latest/querying/api/#remote-write-receiver)
and [wire protocol](https://prometheus.io/docs/specs/prw/remote_write_spec/).

## Built-in sender

Register engines normally, then run the exporter with a stable, unique host
identity. Neither the host label nor `instance` depends on the laptop's IP:

```bash
llm-metrics-exporter serve --listen 127.0.0.1:9109 \
  --host laptop-example \
  --remote-write-url 'https://<prometheus-host>/api/v1/write' \
  --remote-write-dir "$HOME/.local/state/llm-metrics-exporter/remote-write" \
  --remote-write-interval 15s
```

For bearer authentication, add `--remote-write-token-file <private-token-file>`.
The file is read per request, so rotation does not require a restart. TLS uses
the system trust store and verifies certificates; there is no insecure TLS
switch. URL credentials, query strings, and redirects are rejected. Token
values and receiver response bodies are not logged. Configure the server/proxy
to accept the same authentication mechanism.
Follow [credential handling](security.md) to obtain the file through a secret
manager integration without putting token values in commands or this checkout.

`job="llm-metrics-exporter"` and `instance=<host>` are added only to remote-write
samples; the canonical identity and worker labels remain intact. Every process
must have a unique stable host identity. Keep that identity on restart, and do
not run two senders for the same deployments into the same receiver.

| Flag | Default | Purpose |
|---|---|---|
| `--remote-write-url` | empty (disabled) | Full receiver URL |
| `--remote-write-dir` | state directory's `remote-write/` sibling of `registrations/` | Durable queue and last-series checkpoint |
| `--remote-write-interval` | `15s` | Scheduled collection, independent of HTTP scrapes |
| `--remote-write-timeout` | `5s` | Maximum duration of one HTTP attempt |
| `--remote-write-max-bytes` | `67108864` (64 MiB) | Queued compressed payload cap |
| `--remote-write-max-age` | `24h` | Age after which queued batches are discarded |
| `--remote-write-token-file` | empty | Optional bearer token file |

Collection and delivery run separately. Delivery is FIFO, retries transport
failures/5xx/429 with exponential backoff (1–60 seconds), and reuses the exact
timestamps and values. Lost acknowledgements can cause identical duplicates.
Other non-2xx responses drop that batch with a logged error. The sender uses
Remote Write 1.0 (Snappy block-compressed protobuf) and expands classic
histograms into bucket/sum/count series.

Queue files are written atomically and synced. A lock prevents concurrent use
of one queue directory. The directory is bound to a destination URL and host
identity; changing either requires a separate directory so old data cannot be
sent to the wrong destination. Keep the directory across restarts. Payloads
are capped at 4 MiB before and after compression; the checkpoint and transient
atomic-write files require additional disk space beyond the queue cap. A
full queue drops the **newest** snapshot; expired or permanently rejected
batches are dropped during delivery. Drops are explicit in logs/status, not
silently treated as successful delivery. Storage errors are logged and may
lose the affected observation. Counters in status reset on exporter restart.

Samples retain collection timestamps during replay. Receiver retention and
out-of-order limits can reject old data even within the sender's configured
retention. Clock synchronization matters. Do not backfill the same series from
another sender while this queue is offline.

The checkpoint remembers active series, including across restart, so removed
registrations/measurements produce stale markers. Graceful shutdown enqueues
staleness and makes one bounded delivery attempt; remaining batches replay on
restart. Abrupt termination or sleep cannot send a final marker. No history is
reconstructed for periods when the exporter was not running, and a lost engine
counter reset cannot be inferred afterward.

## Delivery health and laptop freshness

```bash
curl -fsS http://127.0.0.1:9109/remote-write/status | jq .
```

This local JSON endpoint reports queued bytes/batches, dropped batches, last
successful delivery time, and the latest error. It is separate from `/healthz`
(process liveness) and `llme_engine_up` (engine collection health), and adds no
unlabeled global series to `/metrics`. The endpoint exists only when remote
write is enabled. An empty queue and recent last success indicate delivery;
backlog growing with a transport error indicates a receiver/connectivity issue.

On Prometheus, a named deployment that has not delivered any engine health
observations for ten minutes can be detected with:

```promql
absent_over_time(llme_engine_up{host="laptop-example",backend="local-model"}[10m])
```

Use `llme_engine_up == 0` for a sender that is reporting an unreadable engine.
Historical replay does not make old samples current; use sample timestamps
and the receiver's query time when assessing freshness.

## Service examples

For the supplied systemd user unit, use `systemctl --user edit
llm-metrics-exporter` and retain the same registration location:

```ini
[Service]
ExecStart=
ExecStart=%h/.local/bin/llm-metrics-exporter serve --listen 127.0.0.1:9109 --host laptop-example --remote-write-url https://<prometheus-host>/api/v1/write --remote-write-dir %h/.local/state/llm-metrics-exporter/remote-write
```

Then `systemctl --user daemon-reload` and `systemctl --user restart
llm-metrics-exporter`. For launchd, add the same flag/value pairs as separate
`<string>` entries in `ProgramArguments` in the supplied plist. Use an absolute
queue path because launchd does not expand shell variables. Reload that
LaunchAgent to apply the changes.

For the Compose exporter, append these entries to its existing `command` and
add a writable persistent volume (the image root filesystem may stay read-only):

Provision the volume so the image's nonroot user can write it before starting
the sender. Inference from `Dockerfile` and the sender's file writes: the named
volume declaration alone does not establish suitable ownership/permissions.
Check your runtime's volume provisioning; this example is not a permission-setup
procedure. Keep registration mounts read-only and token files privately readable
by the service user.

```yaml
services:
  exporter:
    command:
      - serve
      - --registration-dir=/registrations
      - --listen=127.0.0.1:9109
      - --host=laptop-example
      - --remote-write-url=https://<prometheus-host>/api/v1/write
      - --remote-write-dir=/remote-write
    volumes:
      # Retain the existing registrations bind mount as well.
      - remote-write-data:/remote-write
volumes:
  remote-write-data:
```

Disable the separate Agent profile when using this sender for the same
destination. Native execution is appropriate on macOS; Docker's VM cannot read
host-loopback inference endpoints.
