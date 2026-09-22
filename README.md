# llm-metrics-exporter

One Prometheus schema for LLM inference throughput, whatever engine is serving.

Every inference host runs one exporter. It finds the model servers running on
that host, reads each one in its own dialect (a native `/metrics` endpoint, a
per-response stats block, a decode log), and exposes the result under one set
of canonical metric names and labels. A Prometheus Agent on the same host
scrapes it and `remote_write`s to a central Prometheus. Grafana and the
benchmark harness both read those same counters.

**Status:** design, reviewed ([#1](https://github.com/evanwtf/llm-metrics-exporter/issues/1)). Go. No code yet. Start with the docs:

| doc | read it for |
|---|---|
| [`docs/problem.md`](docs/problem.md) | why this exists, and what "done" means |
| [`docs/environment.md`](docs/environment.md) | the fleet, the engines, and the monitoring stack it plugs into |
| [`docs/design.md`](docs/design.md) | architecture, metric schema, labels, adapters, discovery |
| [`AGENTS.md`](AGENTS.md) | rules for anyone, human or agent, working in this repo |

Origin: [evanwtf/local-llm#675](https://github.com/evanwtf/local-llm/issues/675).
local-llm is the benchmark project that needs these numbers; this repo is the
collector it (and anything else) reads them from.

## License

MIT
