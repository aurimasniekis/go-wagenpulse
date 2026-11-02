SHELL        := /usr/bin/env bash
GO           ?= go
BIN          := bin
PKG          := github.com/aurimasniekis/go-wagenpulse
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT       ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
DATE         ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS      := -s -w -X $(PKG)/internal/buildinfo.Version=$(VERSION) -X $(PKG)/internal/buildinfo.Commit=$(COMMIT) -X $(PKG)/internal/buildinfo.Date=$(DATE)
FUZZTIME     ?= 10s
GITLEAKS     ?= $(GO) run github.com/zricethezav/gitleaks/v8@v8.30.1
GOVULNCHECK  ?= $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.9.0
IMAGE        ?= ghcr.io/aurimasniekis/wagenpulse

.DEFAULT_GOAL := help
.PHONY: help build build-go web web-check test race vet lint fmt-check fuzz-smoke vuln secrets check clean \
        sqlc sqlc-check api api-check bench e2e image dictionaries dev dev-setup-token dev-cli

help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ---- build -------------------------------------------------------------------

build: web build-go ## Build the web UI and the wagenpulse binary

build-go: ## Build bin/wagenpulse without rebuilding the UI
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/wagenpulse ./cmd/wagenpulse

web: ## Build the web UI into internal/webui/dist (embedded into the binary)
	cd web && pnpm install --frozen-lockfile && pnpm build

image: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(DATE) -t $(IMAGE):$(VERSION) .

# Environment of the development instance; every host-side command against it needs the same settings.
# Ports are deliberately unusual so the dev instance does not collide with other local services.
DEV_PORT      ?= 58080
DEV_MGMT_PORT ?= 59464
DEV_ENV := WAGENPULSE_MODE=development WAGENPULSE_DATA_DIR=$(CURDIR)/.wagenpulse/data \
	WAGENPULSE_PUBLIC_URL=http://localhost:$(DEV_PORT) WAGENPULSE_HTTP_LISTEN=127.0.0.1:$(DEV_PORT) \
	WAGENPULSE_MANAGEMENT_LISTEN=127.0.0.1:$(DEV_MGMT_PORT) WAGENPULSE_SECRETS_FILE_DIRS=$(CURDIR)/.wagenpulse/secrets

dev: build-go ## Run a development instance on http://localhost:58080 (DEV_PORT; data in .wagenpulse/)
	@mkdir -p .wagenpulse
	$(DEV_ENV) $(BIN)/wagenpulse serve

dev-setup-token: ## Issue a setup token for the development instance (works while `make dev` runs)
	@$(DEV_ENV) $(BIN)/wagenpulse admin setup-token

dev-cli: ## Run any command against the development instance: make dev-cli ARGS="backup create --out /tmp/b"
	@$(DEV_ENV) $(BIN)/wagenpulse $(ARGS)

# ---- checks ------------------------------------------------------------------

test: ## go test
	$(GO) test ./...

race: ## go test with the race detector (the ADR-0002 transaction budget is relaxed for its ~10x overhead, as in CI)
	WAGENPULSE_TEST_TX_MAX=$${WAGENPULSE_TEST_TX_MAX:-500ms} $(GO) test -race -count=1 ./...

bench: ## Ingestion and query benchmarks on a synthetic 1-year dataset (docs/STORAGE.md; ~3 min)
	$(GO) test -run '^$$' -bench . -benchtime 10x -timeout 30m ./internal/telemetry/...

vet: ## go vet
	$(GO) vet ./...

lint: ## golangci-lint
	golangci-lint run ./...

fmt-check: ## Fail if gofmt would change files
	@out=$$(find . -name '*.go' -not -path '*/node_modules/*' -not -path '*/testdata/*' -print0 | xargs -0 gofmt -l); \
	  if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

fuzz-smoke: ## Run every Fuzz* target for FUZZTIME
	@set -e; for pkg in $$($(GO) list ./...); do \
	  for fz in $$($(GO) test -list '^Fuzz' $$pkg 2>/dev/null | grep '^Fuzz' || true); do \
	    echo "fuzz $$pkg $$fz"; $(GO) test -run '^$$' -fuzz "^$$fz$$" -fuzztime $(FUZZTIME) $$pkg; \
	  done; \
	done

vuln: ## govulncheck
	$(GOVULNCHECK) ./...

secrets: ## gitleaks over the working tree
	GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 $(GITLEAKS) dir --redact --config .gitleaks.toml .

web-check: ## Web lint, typecheck, unit tests, build
	cd web && pnpm install --frozen-lockfile && pnpm lint && pnpm typecheck && pnpm test && pnpm build

check: fmt-check vet lint race sqlc-check api-check web-check vuln secrets ## Everything CI runs (except e2e)

e2e: ## Playwright (Chromium + axe, light and dark) against a fresh instance
	scripts/e2e.sh

# ---- generated code ----------------------------------------------------------

$(BIN)/sqlc:
	./scripts/install-sqlc.sh

sqlc: $(BIN)/sqlc ## Regenerate typed queries
	$(BIN)/sqlc generate

sqlc-check: $(BIN)/sqlc ## Fail if generated queries drift from SQL
	$(BIN)/sqlc diff

api: ## Regenerate api/openapi.json and the web types
	$(GO) run ./cmd/wagenpulse openapi > api/openapi.json
	cd web && pnpm install --frozen-lockfile --silent && pnpm gen:api

api-check: ## Fail if api/openapi.json or the web types are stale
	@tmp=$$(mktemp -d); trap 'rm -rf $$tmp' EXIT; \
	  $(GO) run ./cmd/wagenpulse openapi > $$tmp/openapi.json; \
	  diff -q $$tmp/openapi.json api/openapi.json >/dev/null || { echo "api drift: api/openapi.json is stale; run make api"; exit 1; }; \
	  (cd web && pnpm install --frozen-lockfile --silent && pnpm exec openapi-typescript $$tmp/openapi.json -o $$tmp/schema.gen.ts >/dev/null 2>&1); \
	  diff -q $$tmp/schema.gen.ts web/src/api/schema.gen.ts >/dev/null || { echo "api drift: web/src/api/schema.gen.ts is stale; run make api"; exit 1; }; \
	  echo "api: no drift"

dictionaries: ## Regenerate bundled dictionaries from the official PDFs (DICT_DIR=path with the PDFs)
	$(GO) run ./tools/dictgen -in "$(DICT_DIR)" -out internal/dictionary/bundled

clean: ## Remove build output
	rm -rf $(BIN) dist
