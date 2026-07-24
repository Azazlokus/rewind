# Arena — developer harness. `make check` is the gate before every commit.

GO      ?= go
PKGS    ?= ./...
BOTS    ?= 200
DUR     ?= 60s
PPROF_ADDR ?= 127.0.0.1:6060

.PHONY: run test fuzz bench lint check loadtest profile fmt vet tidy integration cover help

## run: start the server (env-configured)
run:
	$(GO) run ./cmd/server

## test: race-enabled unit tests, no cache
test:
	$(GO) test -race -count=1 $(PKGS)

## integration: end-to-end tests behind the integration build tag
integration:
	$(GO) test -race -count=1 -tags=integration ./...

## fuzz: fuzz the decoder briefly (CI-length; run longer locally)
fuzz:
	$(GO) test -run=^$$ -fuzz=FuzzDecode -fuzztime=30s ./internal/protocol

## bench: all benchmarks with allocation stats
bench:
	$(GO) test -bench=. -benchmem -run=^$$ $(PKGS)

## fmt: format the tree
fmt:
	$(GO) fmt $(PKGS)

## vet: go vet
vet:
	$(GO) vet $(PKGS)

## lint: vet plus golangci-lint when it is installed
lint: vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed — ran 'go vet' only (see .golangci.yml for the intended ruleset)"; \
	fi

## check: mandatory pre-commit gate
check: lint test

## cover: unit tests with a coverage summary
cover:
	$(GO) test -race -count=1 -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -func=coverage.out | tail -1

## loadtest: run the bot swarm against a room (added in iteration 6)
loadtest:
	$(GO) run ./cmd/loadtest -bots=$(BOTS) -duration=$(DUR)

## profile: run the server with pprof enabled and print the endpoint
profile:
	@echo "pprof at http://$(PPROF_ADDR)/debug/pprof/"
	ARENA_PPROF_ADDR=$(PPROF_ADDR) $(GO) run ./cmd/server

## tidy: tidy go.mod/go.sum
tidy:
	$(GO) mod tidy

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
