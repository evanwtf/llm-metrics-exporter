# Working in this repo

These rules apply to people and agents alike. Read [`docs/problem.md`](docs/problem.md),
[`docs/environment.md`](docs/environment.md), [`docs/design.md`](docs/design.md) and
[`docs/adapters.md`](docs/adapters.md) before changing anything.

## Commands

```sh
go test ./...                    # all tests; add -race where cgo is available
go vet ./...
gofmt -l .                       # must print nothing
sh scripts/check-public.sh       # the public-repo guard, over every tracked file
uvx pre-commit run --all-files   # everything CI checks, locally
```

Go only; no other toolchain is needed. `internal/version` is the one place the
version is declared, and `docs/changelog.md` needs a section for it before a
release tag.

## This repo is public

- **No hostnames, IP addresses, ports of specific machines, network names, or
  anything else that identifies the private network.** Describe machines by
  role and hardware class ("cluster head", "DGX Spark"). Real values live in
  per-host configuration that is not committed.
- Examples use `127.0.0.1`, placeholders like `<port>`, or documentation ranges.
- Editing a file does not remove what an earlier commit published. Check
  before you push.
- `scripts/check-public.sh` enforces the generic patterns (private address
  ranges, private DNS suffixes, home-directory paths). Names that look
  ordinary go in `.public-denylist`, which is git-ignored. Fixtures from a
  live host lose `instance`, `job` and any path before they are committed.

## Measurement rules

This exporter feeds published benchmark numbers, so:

- **Counters are the source of truth.** Rates are derived at query time. v1
  exposes no tok/s gauge.
- **Prefill and decode are always separate.** Never emit a single blended
  tok/s.
- **Silence is a bug.** If a registered server cannot be read, export
  `llm_engine_up 0`. Do not drop the series.
- **Every series carries `engine` and `model`** (operator requirement). A
  schema test and a full-scrape test enforce it. This is why `/metrics` has no
  Go runtime or process series.
- **Never guess a label.** If an adapter cannot determine the model, it
  reports that. It does not invent a value.
- **Same name, same meaning.** Two engines' numbers share a canonical series
  only if they measure the same interval. Each adapter documents the upstream
  source, clock and update timing for every series it emits, in
  `docs/adapters.md`. Prefill tokens are computed tokens, never cache hits;
  request-clock and engine-clock seconds are different names.
- **Registration is identity.** Labels come from the registration file, and the
  engine's own model name only validates it.
- **A counter reset is not negative throughput.** Handle it explicitly.
- Every adapter has tests against **captured real output** from that engine,
  checked in as fixtures, with the engine version it came from
  (`testdata/README.md`). Expected values are read from the fixture
  independently (jq, or the reference parser), not from the adapter.

## Footprint

The hosts share GPU and CPU memory. The exporter must stay small, hold no
unbounded buffers, and never block on a slow engine: use a timeout on every
read, and report the failure as `llm_engine_up 0`.

## Scope

Token, request, cache and speculative-decoding metrics from inference engines.
Not GPU, power, temperature or host memory, which other exporters already
cover.
