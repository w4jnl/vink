BIN        := $(CURDIR)/bin
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE       ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w -buildid= \
  -X github.com/w4jnl/vink/internal/version.Version=$(VERSION) \
  -X github.com/w4jnl/vink/internal/version.Commit=$(COMMIT) \
  -X github.com/w4jnl/vink/internal/version.Date=$(DATE)
DB         ?= data/vink.db

SQLC_VERSION        := v1.31.1
GOLANGCI_VERSION    := v2.14.0
AIR_VERSION         := v1.67.4
GORELEASER_VERSION  := v2.18.2

# Prefer the pinned tool in ./bin, fall back to PATH.
tool = $(shell test -x $(BIN)/$(1) && echo $(BIN)/$(1) || echo $(1))
SQLC       := $(call tool,sqlc)
GOLANGCI   := $(call tool,golangci-lint)
AIR        := $(call tool,air)
GORELEASER := $(call tool,goreleaser)

.PHONY: build dev generate generate-check migrate lint fmt test e2e tools check-tenancy golden release-check clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/vink ./cmd/vink

dev:
	@mkdir -p data
	VINK_DB_PATH=$(DB) $(AIR)

generate:
	$(SQLC) generate

generate-check: generate
	git diff --exit-code -- internal/db

migrate:
	@mkdir -p data db
	go run ./cmd/vink migrate up --db $(DB)
	go run ./cmd/vink migrate dump --db $(DB) --out db/schema.sql

lint: check-tenancy
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "gofmt:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	$(GOLANGCI) run

fmt:
	$(GOLANGCI) fmt

test:
	go test -race ./...

e2e:
	go test -race -count=1 -tags e2e -v ./e2e/...

check-tenancy:
	@scripts/check-tenancy.sh

golden:
	node internal/http/web/gen_golden.mjs

release-check:
	$(GORELEASER) check

tools:
	GOBIN=$(BIN) go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	GOBIN=$(BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	GOBIN=$(BIN) go install github.com/air-verse/air@$(AIR_VERSION)
	GOBIN=$(BIN) go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
	GOBIN=$(BIN) go install golang.org/x/vuln/cmd/govulncheck@latest

clean:
	rm -rf $(BIN)/vink tmp dist
