# Optional shortcuts; plain Go commands remain supported.
GO ?= go

.PHONY: help build test race check fmt-check vet deps-check public-check

help:
	@printf '%s\n' \
	  'make build        Build dist/llm-metrics-exporter' \
	  'make test         Run all Go tests' \
	  'make race         Run tests with the race detector (requires cgo)' \
	  'make check        Check formatting, vet, dependencies, public hygiene and tests' \
	  'make fmt-check    Verify formatting without changing files' \
	  'make vet          Run go vet' \
	  'make deps-check   Check go.mod/go.sum without changing them' \
	  'make public-check Run the public-repository guard' \
	  'Override Go with: make build GO="$$HOME/go/bin/go"'

build:
	"$(GO)" build -trimpath -o dist/llm-metrics-exporter ./cmd/llm-metrics-exporter

test:
	"$(GO)" test ./...

race:
	"$(GO)" test -race ./...

check: fmt-check vet deps-check public-check test

fmt-check:
	@root=$$("$(GO)" env GOROOT) || exit $$?; \
	out=$$("$$root/bin/gofmt" -l .) || exit $$?; \
	if [ -n "$$out" ]; then printf 'gofmt needed:\n%s\n' "$$out"; exit 1; fi

vet:
	"$(GO)" vet ./...

deps-check:
	"$(GO)" mod tidy -diff

public-check:
	sh scripts/check-public.sh
