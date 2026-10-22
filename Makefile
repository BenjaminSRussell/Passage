.PHONY: lint test web-build helm-template tofu-validate ci-negative ci run deploy-local tools

tools:
	bash scripts/install-dev-tools.sh

lint:
	@test -z "$$(gofmt -l services/hello-ledger)" || (gofmt -l services/hello-ledger && exit 1)
	node --check services/platform-bff/src/server.js
	node --check services/platform-bff/src/server.test.js
	python3 -m py_compile scripts/build_roadmap.py tools/promotion.py tools/test_promotion.py

test:
	cd services/hello-ledger && go test ./...
	node --test services/platform-bff/src/server.test.js
	python3 tools/test_promotion.py
	python3 scripts/build_roadmap.py

web-build:
	npm ci --prefix web/shell
	npm run build --prefix web/shell

helm-template:
	helm template hello-ledger deploy/helm/hello-ledger -f deploy/helm/hello-ledger/values-stage.yaml > /tmp/hello-ledger.yaml
	grep -q 'image: "ghcr.io/benjaminsrussell/passage/hello-ledger@sha256:' /tmp/hello-ledger.yaml

tofu-validate:
	@for env in local eks gke aks baremetal; do \
		echo "validating $$env"; \
		(cd infra/tofu/envs/$$env && tofu init -backend=false -input=false >/dev/null && tofu validate); \
	done

ci-negative:
	bash scripts/ci-negative.sh

ci: lint test web-build helm-template tofu-validate ci-negative

run:
	cd services/hello-ledger && go run ./cmd/hello-ledger

deploy-local:
	bash scripts/deploy-local.sh
