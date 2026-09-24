# Development and releases

This is one Go application, not a monorepo. Use the Go version in
[`go.mod`](../go.mod) (currently `1.27.1`); dependencies are pinned in `go.mod`
and `go.sum`. The core binary needs no Python runtime. Hook tooling and Docker
validation have their own prerequisites; “Go only” does not describe all CI.

## Checks and build

The optional [Makefile](../Makefile) provides shared local/CI shortcuts. Install
Make separately if it is not available; plain Go commands below still work.
Run from the repository root:

```sh
make help                       # also the default target; requires no Go
make build                      # dist/llm-metrics-exporter, with -trimpath
make test                       # all Go tests
make race                       # requires cgo and a C compiler
make check                      # formatting, vet, dependency/public checks, tests
make build GO="$HOME/go/bin/go"  # explicit executable when Go is not on PATH
```

`GO` can also be set in the environment. It selects one executable, not a
command plus arguments. `fmt-check` finds gofmt in that executable's GOROOT,
so a Go override also selects the matching formatter. All check targets are
read-only with respect to source files: `fmt-check` reports formatting changes
needed; `deps-check` uses `go mod tidy -diff`. Build/tests can write artifacts
and Go caches. Individual checks are `fmt-check`, `vet`, `deps-check`, and
`public-check`. `make -k check` runs independent checks even after a failure
and still exits nonzero; Linux CI uses this form. Make does not publish releases
or start/deploy services.

Equivalent underlying commands (with Go on PATH), plus single-test selection:

```sh
go test ./...                    # all tests
go test -race ./...              # requires cgo and a C compiler
go test ./internal/registration -run '^TestParseValid$' -count=1 # single test
go vet ./...
gofmt -l .                       # must print nothing
go mod tidy -diff                # dependency consistency, as in CI
sh scripts/check-public.sh       # every tracked file
go build -o dist/llm-metrics-exporter ./cmd/llm-metrics-exporter
```

Use `gofmt -w` on the Go files you intentionally changed. Go compilation and
`go vet` provide the project's type/build checks; there is no separate
type-checking tool or configured `staticcheck` step. Adding staticcheck remains
an open item in [the plan](plan.md).

If Go is not on PATH, the [live-test guide](cheat-sheet.md#1-build) uses
`GO_BIN="${GO_BIN:-$HOME/go/bin/go}"` and describes isolated writable caches.
Its curl, jq and ripgrep requirements apply to that walkthrough, not the binary.

Install hooks once with `pre-commit install` (or `uvx pre-commit install`), then
run `uvx pre-commit run --all-files`. This requires pre-commit, or uv for the
`uvx` form. The hook configuration checks public-repo hygiene, large files,
merge conflicts, YAML, whitespace, private keys, gitleaks, gofmt, vet and tests.
It is **not all of CI**: [CI](../.github/workflows/ci.yml) also checks dependency
tidiness, builds four release targets, runs the Linux binary, validates Compose,
builds/runs the container, and runs macOS race tests and a native build. CI runs
on pushes to `main` and on pull requests, using self-hosted Linux X64 and macOS
ARM64 runners with per-run Go caches.
CI runners now require Make: Linux uses `make -k check`, and macOS uses `make
race` and `make build`. Platform cross-build and container checks remain in CI.
Makefile regression tests in `scripts/makefile` exercise command dispatch and
failure propagation with a fake toolchain; they skip if Make is unavailable.

For documentation-only work restricted to static checks, do not execute these
build/test/hook commands just to verify the text. Check commands against their
source and report that they were not executed.

## Implementation map and change checks

| Area | Authoritative implementation / guidance |
| --- | --- |
| Commands, flags, HTTP routes | [`main.go`](../cmd/llm-metrics-exporter/main.go), [CLI reference](cli.md) |
| Compiled adapter set | [`internal/engines`](../internal/engines) |
| Per-engine translation | [`internal/adapter`](../internal/adapter), [adapter semantics](adapters.md) |
| Canonical names and labels | [`internal/metrics`](../internal/metrics), [schema](design.md#metric-schema) |
| Collection and lifecycle | [`internal/collector`](../internal/collector) |
| Strict YAML, atomic writes, identity-aware removal | [`internal/registration`](../internal/registration) |
| Bounded log reads / exposition parsing | [`internal/tail`](../internal/tail), [`internal/promtext`](../internal/promtext) |
| Durable outbound delivery | [`internal/remotewrite`](../internal/remotewrite), [remote write](remote-write.md) |
| Version / packaging / release helpers | [`internal/version`](../internal/version), [`packaging`](../packaging), [`scripts`](../scripts) |
| Captured engine evidence | [`testdata/README.md`](../testdata/README.md), [findings](findings.md) |

Preserve the existing Go package boundaries and error/health reporting patterns.
For an adapter change, document source, clock and update timing and test against
sanitized real output with recorded version/provenance. Read expected values
independently with jq or the reference parser, not from the adapter under test.
For label/schema changes, preserve the schema and full-scrape tests requiring
`engine` and `model`. For registration/lifecycle changes, retain atomic writes,
run-ID-checked deletion, timeout and worker-reset coverage.

## Releases

Publishing is manual. Nothing publishes on a push or a tag. Do not add an
automatic trigger without the operator's approval.

1. Set the version in `internal/version/version.go` (the single declaration)
   and add its section to [the changelog](changelog.md). Commit and push.
2. Run **Actions → publish → Run workflow**, or:

   ```sh
   gh workflow run publish.yml -f tag=v0.1.0
   ```

Use the version being published in place of `v0.1.0` (`vX.Y.Z` in general).
The [publish workflow](../.github/workflows/publish.yml) refuses a tag that
disagrees with `internal/version`, an existing tag, or a version without a
changelog section. It tests, builds four zips, runs the linux/amd64 binary from
its zip, and creates the tag and GitHub release with that changelog section as
the notes. Documentation of this workflow is not evidence that a release has
already been published; the changelog currently labels 0.1.0 unreleased.

Each release archive is `llm-metrics-exporter-<version>-<os>-<arch>.zip`, for
darwin/linux × arm64/amd64, containing the binary, LICENSE and README. A macOS
browser download is quarantined; after verifying the download is trusted, the
unsigned binary can be allowed with:

```sh
xattr -d com.apple.quarantine llm-metrics-exporter
```

## Documentation maintenance

Update existing authoritative sections instead of creating competing versions.
Keep root entry points concise; put detailed procedures, rationale and incident
narratives in the relevant supporting guide. When moving or correcting existing
knowledge, retain distinct examples, exceptions, provenance and historical
context and update the [migration audit](documentation-audit.md) as appropriate.
Check relative links, heading anchors, symlinks and `wc -w` counts. Root agent
guidance plus universally required reading must stay at or below 4,000 words.
