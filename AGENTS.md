# Working in this repo

These rules apply to people and agents alike. This is one Go application:
one exporter per inference host observes registered engines through HTTP or
logs, exposes canonical Prometheus metrics, and optionally sends remote write.
It does not launch models or implement provider billing. Four adapters are
implemented; mlx-serve, MTPLX and Ollama remain planned.

## Change policy and reading map

* Make minimal, focused changes; avoid broad refactors unless requested.
* Preserve existing architecture and patterns.
* Don't introduce new dependencies without justification.
* Update tests when behavior changes; update docs when user-visible
  behavior, configuration, or workflows change.

Read the following before the indicated task; linked policies are mandatory
within their scope. There is no universally required companion document.

| Before doing this | Read |
| --- | --- |
| Changing measurement, labels, queries or collector lifecycle | [Design](docs/design.md), [adapter semantics](docs/adapters.md) |
| Adding/changing an adapter or fixture | [Adapters](docs/adapters.md), [findings](docs/findings.md), [fixture provenance](testdata/README.md) |
| Changing registrations or CLI behavior | [CLI](docs/cli.md), [static targets](docs/static-targets.md) |
| Operating vLLM or changing scrape/service instructions | [Prometheus setup](docs/prometheus-setup.md); [cheat sheet](docs/cheat-sheet.md) for live measurements |
| Changing remote-write delivery, queueing or receiver setup | [Remote write](docs/remote-write.md) |
| Handling credentials, captures or publication hygiene | [Security](docs/security.md) |
| Building, changing dependencies/CI, publishing or restructuring docs | [Development](docs/development.md) |
| Changing scope or rollout decisions | [Problem](docs/problem.md), [environment](docs/environment.md), [plan](docs/plan.md) (historical context and open decisions are labeled) |

`cmd/llm-metrics-exporter` owns commands; `internal/metrics` owns the schema;
`internal/adapter` translates engines; `internal/collector` manages lifecycle;
`internal/registration` owns identity; `internal/remotewrite` owns delivery.
See the [implementation map](docs/development.md#implementation-map-and-change-checks)
for parsing, bounded tailing, packaging and test locations.

## Commands

```sh
go test ./...                    # all tests; add -race where cgo is available
go vet ./...
gofmt -l .                       # must print nothing
sh scripts/check-public.sh       # the public-repo guard, over every tracked file
go build -o dist/llm-metrics-exporter ./cmd/llm-metrics-exporter
```

Use the Go version in `go.mod` (currently 1.27.1). Full checks, focused-test
selection, hook prerequisites and CI differences are in
[development](docs/development.md#checks-and-build). These commands describe
development checks, not authorization to execute them during a static-only task.
`internal/version` is the one place the
version is declared, and `docs/changelog.md` needs a section for it before
publishing. Publishing is manual (`gh workflow run publish.yml -f tag=vX.Y.Z`);
do not add an automatic trigger without the operator's say-so.

## Public repository and secrets

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

- Never put secret values in chat, transcript-visible tool output, documentation,
  examples, logs, commits or PR text. Never ask users to paste secrets. Inspect
  names, paths and metadata; redact values **before** output, not after.
- Explicitly ignore local secret files in the project's `.gitignore` and include
  applicable rules in Git delivery. Global ignores and `.git/info/exclude` alone
  are insufficient. Existing `.env` and `*.local.yml` rules cover documented local
  configuration; keep sanitized `.env.example` trackable. Add narrow rules for
  actual new secret paths, not invented filenames or broad source/certificate masks.
- Prefer a maintained secret vault/manager as authoritative storage. None is
  established here; leave the choice open without requiring a vendor or account.
  Retrieve at runtime through its supported integration, never literal command
  arguments, shell history or debug output. The existing remote-write interface
  reads a private token file per send; follow [security](docs/security.md).
- Do not retrieve real secrets merely to verify documentation. Ignore rules do
  not protect tracked files or remove secrets from history. If exposure is found,
  report only the path and required revocation/rotation follow-up. Do not display
  the value, rewrite history, delete credentials or rotate them during docs work.

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

This is a safety requirement, not a claim that all underlying I/O is cancellable.
The [collector lifecycle rules](docs/design.md#health) bound in-flight work and
describe what happens when a read ignores cancellation. Preserve those bounds.

## Scope

Token, request, cache and speculative-decoding metrics from inference engines.
Not GPU, power, temperature or host memory, which other exporters already
cover.

## Documentation maintenance

Update authoritative sections in place; put long explanations and incidents in
supporting docs, keeping entry points concise. Check links, anchors, and the
relative `CLAUDE.md -> AGENTS.md` alias. Keep root plus any universally required
reading within 4,000 `wc -w` words. Preserve moved knowledge and record migrations
in the [documentation audit](docs/documentation-audit.md).
