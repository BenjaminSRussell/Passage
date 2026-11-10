SHELL := /bin/bash
GO_MODULES := $(shell find platform/go services tools/rlscheck tools/tenant-restore tools/e2e -name go.mod -not -path '*/node_modules/*' -exec dirname {} \; 2>/dev/null)
TS_PACKAGES := $(shell find apps services -maxdepth 2 -name package.json -not -path '*/node_modules/*' -exec dirname {} \; 2>/dev/null)
PY_PACKAGES := $(shell find services -maxdepth 2 -name pyproject.toml -exec dirname {} \; 2>/dev/null)

.PHONY: test test-go test-ts test-py test-e2e bench-ledger vet lint-charts roadmap prd deploy-local destroy-local check-deploy-local dev dev-down dev-render e2e-local

# One command for a laptop: Postgres, Kafka, Keycloak and OPA in Docker, the services, the dev gateway and the web
# app on the host. Needs docker compose, psql, go and node (see deploy/local/README.md).
dev:
	cd tools/e2e && go run ./cmd/passage-dev up

dev-render:
	cd tools/e2e && go run ./cmd/passage-dev render

# The vendor -> PO -> receipt -> AP invoice -> ledger flow against the stack `make dev` is running: real Keycloak
# tokens for the seeded users, the dev gateway, OPA and the real services. Run `make dev` in another terminal first.
e2e-local:
	cd tools/e2e && PASSAGE_E2E_LIVE=1 go test -count=1 -v -run TestLiveStackFlow ./

dev-down:
	PASSAGE_DEV_DIR=$(CURDIR)/.run/dev docker compose -f deploy/local/compose.yaml down

test: test-go test-ts test-py check-deploy-local

test-go:
	@for m in $(GO_MODULES); do echo "== go test $$m"; (cd $$m && go test ./... ) || exit 1; done

vet:
	@for m in $(GO_MODULES); do echo "== go vet $$m"; (cd $$m && go vet ./... ) || exit 1; done

test-ts:
	@for p in $(TS_PACKAGES); do echo "== test $$p"; (cd $$p && npm test --silent -- --run ) || exit 1; done

# Playwright + axe suite for apps/web; needs `npx playwright install chromium` once. Not part of `make test`.
test-e2e:
	cd apps/web && npx playwright test

# Local ledger posting/balance-read benchmark, a proxy for wave 11 AC4 (#476); not part of `make test`.
bench-ledger:
	services/ledger/scripts/bench-local.sh

test-py:
	@for p in $(PY_PACKAGES); do echo "== pytest $$p"; (cd $$p && python3 -m pytest -q ) || exit 1; done

lint-charts:
	@for c in $$(find services -type d -path '*/charts/*' -maxdepth 3); do echo "== helm lint $$c"; helm lint $$c || exit 1; done

roadmap:
	python3 scripts/build_roadmap.py

prd:
	python3 scripts/build_prd.py --pdf

# Wave 1 owns the local deployment implementation under deploy/; keep the
# repository entry point here so `make deploy-local` works from the root.
deploy-local:
	$(MAKE) -C deploy deploy-local

destroy-local:
	$(MAKE) -C deploy destroy-local

# Validate both Makefiles and the forwarding path without touching Docker,
# k3d, kubectl or OpenTofu resources.
check-deploy-local:
	@$(MAKE) --no-print-directory --dry-run deploy-local >/dev/null
	@echo "root deploy-local forwarding target parses successfully"
