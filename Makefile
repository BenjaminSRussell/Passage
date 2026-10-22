SHELL := /bin/bash
GO_MODULES := $(shell find platform/go services -name go.mod -not -path '*/node_modules/*' -exec dirname {} \; 2>/dev/null)
TS_PACKAGES := $(shell find apps services -maxdepth 2 -name package.json -not -path '*/node_modules/*' -exec dirname {} \; 2>/dev/null)
PY_PACKAGES := $(shell find services -maxdepth 2 -name pyproject.toml -exec dirname {} \; 2>/dev/null)

.PHONY: test test-go test-ts test-py vet lint-charts roadmap prd

test: test-go test-ts test-py

test-go:
	@for m in $(GO_MODULES); do echo "== go test $$m"; (cd $$m && go test ./... ) || exit 1; done

vet:
	@for m in $(GO_MODULES); do echo "== go vet $$m"; (cd $$m && go vet ./... ) || exit 1; done

test-ts:
	@for p in $(TS_PACKAGES); do echo "== test $$p"; (cd $$p && npm test --silent -- --run ) || exit 1; done

test-py:
	@for p in $(PY_PACKAGES); do echo "== pytest $$p"; (cd $$p && python3 -m pytest -q ) || exit 1; done

lint-charts:
	@for c in $$(find services -type d -path '*/charts/*' -maxdepth 3); do echo "== helm lint $$c"; helm lint $$c || exit 1; done

roadmap:
	python3 scripts/build_roadmap.py

prd:
	python3 scripts/build_prd.py --pdf
