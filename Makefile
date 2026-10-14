SHELL := /bin/bash
COMPOSE := docker compose
TOOLS := $(COMPOSE) --profile tools run --rm tools

SQLC_IMAGE := sqlc/sqlc:1.30.0
LINT_IMAGE := golangci/golangci-lint:v2.14.0
REDOCLY_IMAGE := redocly/cli:2.63.0

.PHONY: up seed follow demo down clean logs \
        test test-go cover lint lint-go fmt sqlc sqlc-check openapi-lint \
        web-install web-check e2e screenshots lighthouse bench ci

## Stack

up:            ## build and start postgres, api, web, prometheus
	$(COMPOSE) up -d --build --wait

seed:          ## register the Lumen & Loom catalogue and replay its pipeline history
	$(TOOLS) seed

follow:        ## keep the healthy pipelines running (Ctrl-C to stop)
	$(TOOLS) follow

demo:          ## walk through ownership, a breaking change, lineage and freshness
	$(TOOLS) demo

logs:
	$(COMPOSE) logs -f api

down:          ## stop the stack, keep data
	$(COMPOSE) --profile tools down

clean:         ## stop the stack and delete its data
	$(COMPOSE) --profile tools down -v

## Go

test: test-go

test-go:       ## unit and integration tests (integration needs Docker)
	cd api && go test -race -count=1 ./...

cover:
	cd api && go test -count=1 -coverpkg=./internal/... -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1

fmt:
	cd api && gofmt -w .

lint-go:
	@test -z "$$(cd api && gofmt -l .)" || (echo "gofmt needed:"; cd api && gofmt -l .; exit 1)
	cd api && go vet ./...
	docker run --rm -v "$$PWD/api":/src -w /src $(LINT_IMAGE) golangci-lint run ./...

sqlc:          ## regenerate typed queries
	docker run --rm -v "$$PWD/api":/src -w /src $(SQLC_IMAGE) generate

sqlc-check:    ## fail if generated code is stale
	docker run --rm -v "$$PWD/api":/src -w /src $(SQLC_IMAGE) diff

openapi-lint:
	docker run --rm -v "$$PWD":/spec -w /spec $(REDOCLY_IMAGE) lint api/openapi/openapi.yaml

lint: lint-go sqlc-check openapi-lint web-check

## Web

web-install:
	cd web && npm ci --no-audit --no-fund

web-check:     ## eslint, tsc, next build
	cd web && npm run lint && npm run typecheck && npm run build

e2e:           ## Playwright: starts its own API (needs PostgreSQL on DR_PG_PORT) and the built UI
	cd web && npx playwright test e2e/flow.spec.ts

screenshots:   ## PNGs of the seeded stack for the README (after make up seed)
	cd web && E2E_BASE_URL=http://localhost:$${DR_WEB_PORT:-3088} npx playwright test e2e/screenshots.spec.ts

lighthouse:    ## Lighthouse against the seeded stack, with Playwright's Chromium
	cd web && node scripts/lighthouse.mjs http://localhost:$${DR_WEB_PORT:-3088}

## Measurements (see MEASUREMENTS.md)

bench:
	mkdir -p bench/results
	$(TOOLS) compat -out /results/compat.json
	$(TOOLS) ingest -out /results/ingest.json
	$(TOOLS) lineage -out /results/lineage.json
	$(TOOLS) reads -out /results/reads.json
	$(TOOLS) freshness -out /results/freshness.json

ci: lint test
