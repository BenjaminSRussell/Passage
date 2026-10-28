SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c

REPO_ROOT := $(abspath ..)
CLUSTER := passage-local
IMAGE := passage-local/hello-ledger:local
REVISION ?= main
PYTHON ?= python3
IMAGE_DIGEST ?=
SKIP_VERIFY ?=

.PHONY: deploy-local destroy-local promote-stage rollback-stage promote-prod rollback-prod test-stage-release test-policies

deploy-local:
	tofu -chdir=tofu/cluster init -input=false
	tofu -chdir=tofu/cluster apply -input=false -auto-approve
	docker build -f $(REPO_ROOT)/services/platform-health/Dockerfile -t $(IMAGE) $(REPO_ROOT)
	k3d image import $(IMAGE) -c $(CLUSTER)
	kubectl --context k3d-$(CLUSTER) create namespace hello-ledger --dry-run=client -o yaml | kubectl --context k3d-$(CLUSTER) apply -f -
	[ -n "$$(kubectl --context k3d-$(CLUSTER) -n hello-ledger get secret hello-ledger-db -o jsonpath='{.data.APP_PASSWORD}' 2>/dev/null)" ] || { \
		kubectl --context k3d-$(CLUSTER) -n hello-ledger delete secret hello-ledger-db --ignore-not-found; \
		pw=$$(openssl rand -hex 16); apw=$$(openssl rand -hex 16); \
		kubectl --context k3d-$(CLUSTER) -n hello-ledger create secret generic hello-ledger-db \
			--from-literal=POSTGRES_PASSWORD=$$pw \
			--from-literal=APP_PASSWORD=$$apw \
			--from-literal=DATABASE_URL="postgres://passage_app:$$apw@postgres:5432/hello?sslmode=disable"; \
		if kubectl --context k3d-$(CLUSTER) -n hello-ledger get deploy/postgres >/dev/null 2>&1; then \
			kubectl --context k3d-$(CLUSTER) -n hello-ledger rollout restart deploy/postgres; fi; }
	sed "s|targetRevision: main|targetRevision: $(REVISION)|" argocd/root-local.yaml | kubectl --context k3d-$(CLUSTER) apply -f -
	sed "s|targetRevision: main|targetRevision: $(REVISION)|" argocd/hello-ledger-local.yaml | kubectl --context k3d-$(CLUSTER) apply -f -
	until kubectl --context k3d-$(CLUSTER) -n hello-ledger get deploy/hello-ledger >/dev/null 2>&1; do sleep 5; done
	kubectl --context k3d-$(CLUSTER) -n hello-ledger rollout status deploy/hello-ledger --timeout=300s
	@echo "hello-ledger is up: add '127.0.0.1 app.local.passage.dev' to /etc/hosts and open https://app.local.passage.dev/readyz"

destroy-local:
	tofu -chdir=tofu/cluster destroy -input=false -auto-approve

promote-stage:
	@test -n "$(IMAGE_DIGEST)" || { echo "Usage: make -C deploy promote-stage IMAGE_DIGEST=sha256:<64 lowercase hex characters>" >&2; exit 2; }
	$(if $(filter 1,$(SKIP_VERIFY)),@echo "WARNING: SKIP_VERIFY=1 - signature and SBOM verification disabled" >&2)
	$(PYTHON) scripts/stage_release.py promote --to stage --digest "$(IMAGE_DIGEST)" $(if $(filter 1,$(SKIP_VERIFY)),--skip-verify)

rollback-stage:
	$(PYTHON) scripts/stage_release.py rollback --env stage

promote-prod:
	@test -n "$(IMAGE_DIGEST)" || { echo "Usage: make -C deploy promote-prod IMAGE_DIGEST=<the digest stage pins>" >&2; exit 2; }
	@test "$(SKIP_VERIFY)" != "1" || { echo "SKIP_VERIFY is not allowed for prod" >&2; exit 2; }
	$(PYTHON) scripts/stage_release.py promote --to prod --digest "$(IMAGE_DIGEST)"

rollback-prod:
	$(PYTHON) scripts/stage_release.py rollback --env prod

test-stage-release:
	$(PYTHON) -m unittest discover -s scripts -p 'test_*.py' -v

test-policies:
	@which kyverno >/dev/null || { echo "kyverno CLI not installed; install it from https://kyverno.io/docs/kyverno-cli/ to run the policy tests" >&2; exit 2; }
	kyverno test policies/tests
