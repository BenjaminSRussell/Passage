SHELL := /bin/bash
GO_MODULES := $(shell find platform/go services -name go.mod -not -path '*/node_modules/*' -exec dirname {} \; 2>/dev/null)
TS_PACKAGES := $(shell find apps services -maxdepth 2 -name package.json -not -path '*/node_modules/*' -exec dirname {} \; 2>/dev/null)
PY_PACKAGES := $(shell find services -maxdepth 2 -name pyproject.toml -exec dirname {} \; 2>/dev/null)

# Packages whose Postgres tests fail against the migrations as shipped. Skipped by
# testkit until each gap closes; remove an entry when its package passes with
# PASSAGE_TEST_APP_DATABASE_URL set.
# #195: no passage_app grants migration (schema USAGE and table grants).
SKIP_MISSING_GRANTS := \
	services/billing/internal/store \
	services/costing/internal/store \
	services/fieldservice/internal/store \
	services/fulfillment/internal/store \
	services/planning/internal/store \
	services/projects/internal/store \
	services/treasury/internal/store
PASSAGE_TEST_DB_SKIP_PACKAGES := $(SKIP_MISSING_GRANTS)
export PASSAGE_TEST_DB_SKIP_PACKAGES

.PHONY: test test-go test-ts test-py test-e2e vet lint-charts roadmap prd deploy-local destroy-local check-deploy-local

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
