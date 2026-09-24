# Static targets and measurement availability

These are optional **pinned** targets. For no-configuration local engine/model
switching use [automatic discovery](discovery.md), now the default. To collect
only the registrations below, also pass `--discovery=off`.

No benchmark harness or launcher integration is required. Create a directory
of YAML registrations and run `llm-metrics-exporter serve --registration-dir
<directory>`. The filename must match `backend`, for example `local-model.yaml`:

```yaml
version: 1
engine: vllm
endpoint: http://127.0.0.1:<port>
model: my-model
served_model: <exact-upstream-model-name>
backend: local-model
nodes: 1
```

`run_id` is optional for static files and defaults to `static`; `issue` is
optional benchmark metadata. `backend` distinguishes deployments with the
same engine/model. Edit the file atomically to change the target; remove it to
stop observing. For a lifecycle-managed launcher, supply a unique run ID so
late cleanup cannot remove a newer deployment. The CLI `register` also defaults
to `--run-id static --nodes 1`; `deregister --run-id static` removes static
registrations. Unknown YAML fields still fail validation.

When `served_model` is absent, a single upstream model can be labeled with the
registration's chosen alias. Multiple upstream models require explicit
selection and produce a mismatch/down signal otherwise. Model-less engines
cannot validate identity against upstream; registration remains authoritative.

## Token meanings

| Measurement | Meaning |
|---|---|
| `llme_tokens_total{phase="prefill"}` | Prompt tokens actually computed, excluding cache hits |
| `llme_tokens_total{phase="decode"}` | Generated tokens reported by the engine |
| `llme_prompt_cached_tokens_total` | Prompt tokens reused from cache |

These measure engine work. Provider usage/billing input tokens are a separate
concept: they may include cache hits, accounting adjustments, or work not
observable at this engine. A future provider adapter must use a separate usage
schema (for example, input/output usage counters), with documented cache and
request-completion semantics; it must not map total input usage to computed
prefill. This version does not claim provider billing/accounting support.

See [adapters.md](adapters.md) for upstream versions, supported measurements,
and update timing. Query current availability independently from engine health:

```promql
llme_exporter_metric_available{metric="llme_tokens_total",phase="prefill"}
llme_exporter_metric_available{metric="llme_tokens_total",phase="decode"}
llme_exporter_metric_available{metric="llme_prompt_cached_tokens_total",phase="none"}
```

`1` means at least one valid worker sample was supplied on this collection,
including a genuine zero. `0` means unavailable (unsupported, not initialized,
backlogged, or collection failed); it does not claim the underlying count is
zero. `llme_engine_up` distinguishes failed reads from readable telemetry with
missing measurements. Inspect worker series separately if you need completeness
across workers. `llme_exporter_telemetry_backlog_bytes` identifies log catch-up.

Use direct scraping, built-in remote write, or a separate Prometheus Agent as
appropriate. None is required to read `/metrics` locally.
