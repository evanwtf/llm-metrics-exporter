# Working in this repo

These rules apply to people and agents alike. Read [`docs/problem.md`](docs/problem.md),
[`docs/environment.md`](docs/environment.md) and [`docs/design.md`](docs/design.md)
before changing anything.

## This repo is public

- **No hostnames, IP addresses, ports of specific machines, network names, or
  anything else that identifies the private network.** Describe machines by
  role and hardware class ("cluster head", "DGX Spark"). Real values live in
  per-host configuration that is not committed.
- Examples use `127.0.0.1`, placeholders like `<port>`, or documentation ranges.
- Editing a file does not remove what an earlier commit published. Check
  before you push.

## Measurement rules

This exporter feeds published benchmark numbers, so:

- **Counters are the source of truth.** Rates are derived at query time. v1
  exposes no tok/s gauge.
- **Prefill and decode are always separate.** Never emit a single blended
  tok/s.
- **Silence is a bug.** If a registered server cannot be read, export
  `llm_engine_up 0`. Do not drop the series.
- **Never guess a label.** If an adapter cannot determine the model, it
  reports that. It does not invent a value.
- **Same name, same meaning.** Two engines' numbers share a canonical series
  only if they measure the same interval. Each adapter documents the upstream
  source and interval for every series it emits (`docs/design.md`, *Semantic
  equivalence*).
- **Registration is identity.** Labels come from the registration file, and the
  engine's own model name only validates it.
- **A counter reset is not negative throughput.** Handle it explicitly.
- Every adapter has tests against **captured real output** from that engine,
  checked in as fixtures, with the engine version it came from.

## Footprint

The hosts share GPU and CPU memory. The exporter must stay small, hold no
unbounded buffers, and never block on a slow engine: use a timeout on every
read, and report the failure as `llm_engine_up 0`.

## Scope

Token, request, cache and speculative-decoding metrics from inference engines.
Not GPU, power, temperature or host memory, which other exporters already
cover.
