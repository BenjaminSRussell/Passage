SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c

REPO_ROOT := $(abspath ..)
CLUSTER := depot-local
IMAGE := depot-local/hello-ledger:local
REVISION ?= main
PYTHON ?= python3
IMAGE_DIGEST ?=
SKIP_VERIFY ?=

.PHONY: test-tofu test-cloud-profiles test-offline offline-bundle deploy-local destroy-local promote-stage rollback-stage promote-prod rollback-prod test-stage-release test-policies test-gateway validate-gateway-schema test-argocd test-migrate-k3d smoke-consumers

deploy-local:
	tofu -chdir=tofu/cluster init -input=false
	tofu -chdir=tofu/cluster apply -input=false -auto-approve
	docker build -f $(REPO_ROOT)/services/platform-health/Dockerfile -t $(IMAGE) $(REPO_ROOT)
	k3d image import $(IMAGE) -c $(CLUSTER)
	kubectl --context k3d-$(CLUSTER) create namespace hello-ledger --dry-run=client -o yaml | kubectl --context k3d-$(CLUSTER) apply -f -
	[ -n "$$(kubectl --context k3d-$(CLUSTER) -n hello-ledger get secret hello-ledger-db -o jsonpath='{.data.APP_PASSWORD}' 2>/dev/null)" ] || { \
		kubectl --context k3d-$(CLUSTER) -n hello-ledger delete secret hello-ledger-db --ignore-not-found; \
		pw=$$(openssl rand -hex 16); apw=$$(openssl rand -hex 16); opw=$$(openssl rand -hex 16); \
		kubectl --context k3d-$(CLUSTER) -n hello-ledger create secret generic hello-ledger-db \
			--from-literal=POSTGRES_PASSWORD=$$pw \
			--from-literal=APP_PASSWORD=$$apw \
			--from-literal=ORG_OWNER_PASSWORD=$$opw \
			--from-literal=DATABASE_URL="postgres://depot_app:$$apw@postgres:5432/hello?sslmode=disable"; \
		if kubectl --context k3d-$(CLUSTER) -n hello-ledger get deploy/postgres >/dev/null 2>&1; then \
			kubectl --context k3d-$(CLUSTER) -n hello-ledger rollout restart deploy/postgres; fi; }
	[ -n "$$(kubectl --context k3d-$(CLUSTER) -n hello-ledger get secret hello-ledger-db -o jsonpath='{.data.ORG_OWNER_PASSWORD}')" ] || { \
		opw=$$(openssl rand -hex 16); \
		printf '{"data":{"ORG_OWNER_PASSWORD":"%s"}}' "$$(printf %s "$$opw" | base64 | tr -d '\n')" | kubectl --context k3d-$(CLUSTER) -n hello-ledger patch secret hello-ledger-db --type merge --patch-file /dev/stdin >/dev/null; \
		if kubectl --context k3d-$(CLUSTER) -n hello-ledger get deploy/postgres >/dev/null 2>&1; then \
			kubectl --context k3d-$(CLUSTER) -n hello-ledger rollout restart deploy/postgres; fi; }
	sed "s|targetRevision: main|targetRevision: $(REVISION)|" argocd/root-local.yaml | kubectl --context k3d-$(CLUSTER) apply -f -
	sed "s|targetRevision: main|targetRevision: $(REVISION)|" argocd/hello-ledger-local.yaml | kubectl --context k3d-$(CLUSTER) apply -f -
	until kubectl --context k3d-$(CLUSTER) -n hello-ledger get deploy/hello-ledger >/dev/null 2>&1; do sleep 5; done
	kubectl --context k3d-$(CLUSTER) -n hello-ledger rollout status deploy/hello-ledger --timeout=300s
	@echo "hello-ledger is up: add '127.0.0.1 app.local.depot.dev' to /etc/hosts and open https://app.local.depot.dev/readyz"

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
	$(PYTHON) -m unittest discover -s scripts -p 'test_stage_release.py' -v

test-policies:
	@which kyverno >/dev/null || { echo "kyverno CLI not installed; install it from https://kyverno.io/docs/kyverno-cli/ to run the policy tests" >&2; exit 2; }
	kyverno test policies/tests

GATEWAY_CRDS := third_party/envoy-gateway-v1.4.0/crds.yaml
GATEWAY_SCHEMAS ?= $(CURDIR)/.gateway-schemas

# cert-manager's CRDs are not vendored: the AS2 edge Certificate is skipped here and its fields are checked by
# scripts/test_gateway.py instead. So is ReferenceGrant (Gateway API standard channel, installed by the Envoy Gateway chart but
# not in the vendored file), which lets the generated routes reach the batch and metadata Services in their own namespaces, and
# PodMonitor (prometheus-operator, installed by kube-prometheus-stack): its fields are checked by scripts/test_gateway.py.
validate-gateway-schema:
	@which kubeconform >/dev/null || { echo "kubeconform not installed; run: go install github.com/yannh/kubeconform/cmd/kubeconform@v0.6.7" >&2; exit 2; }
	$(PYTHON) scripts/crd_schemas.py $(GATEWAY_CRDS) $(GATEWAY_SCHEMAS)
	kubectl kustomize gateway | kubeconform -strict -summary -skip Certificate,ReferenceGrant,PodMonitor -schema-location default -schema-location '$(GATEWAY_SCHEMAS)/{{.ResourceKind}}-{{.Group}}-{{.ResourceAPIVersion}}.json' -

# The API routes are generated (D31, B-02a): scripts/gen_httproutes.py --check fails when httproutes.yaml or the rate limit's
# targetRefs differ from what the shell's API clients, the OpenAPI documents, the charts and the dev Applications give.
test-gateway: validate-gateway-schema
	$(PYTHON) scripts/gen_httproutes.py --check
	$(PYTHON) -m unittest scripts/test_gateway.py -v
	$(PYTHON) -m unittest scripts/test_payments_webhook_gateway.py -v

# Argo CD Applications: structure, sync policy, chart coverage, event backbone and Temporal order, directory manifest
# coverage and Secret references; renders every dev chart when helm is on PATH. test_kafka_consumers.py (D34, B-05b): every
# consumer in the code has a KafkaUser whose ACLs are exactly its topics, groups and dead-letter topics, and dev runs the
# consumers it can start.
# Stage (B-09 part b): stage_apps.py and gen_databases.py --check (the stage Applications, their release pins, the platform
# copies, every service database and Secret) and the session Valkey chart test (D26).
test-argocd:
	$(PYTHON) -m unittest scripts/test_argocd_apps.py -v
	$(PYTHON) -m unittest scripts/test_kafka_consumers.py -v
	$(PYTHON) scripts/gen_migrate_jobs.py --check
	$(PYTHON) scripts/stage_apps.py --check
	$(PYTHON) scripts/gen_databases.py --check
	$(PYTHON) -m unittest scripts/test_migrate_jobs.py -v
	$(PYTHON) -m unittest discover -s session-valkey -v

# Live check of D34 on the dev cluster (#213): produces one event to a topic of each consumer group dev turns on, as a
# temporary KafkaUser, and waits for the group's committed offset to move past it. Needs kubectl with a context on the dev
# cluster (CONTEXT is required; the script refuses to guess the current one). Not run in CI; `scripts/smoke_consumers.py --dry-run` prints the plan.
CONTEXT ?=
smoke-consumers:
	$(PYTHON) scripts/smoke_consumers.py $(if $(CONTEXT),--context $(CONTEXT))

# Owner-role migration Jobs on a throwaway k3d cluster (D28): builds each service image in SERVICES, helm-installs its chart
# against an empty database and checks the Job applied every migration as the owner role. Needs docker, k3d, kubectl, helm.
# MIGRATE_DEADLINE (seconds, default 600) sets the Jobs' activeDeadlineSeconds; KEEP=1 leaves the cluster up.
SERVICES ?= ledger fx platform-health
test-migrate-k3d:
	SERVICES="$(SERVICES)" bash scripts/k3d_migrate_test.sh

# Cloud profile (#599): the profile table and its generated copies, the egress allowlist render test over every chart
# (needs helm), the region pair checker and the multi-region chart (Azure Blob backups, US pair).
test-cloud-profiles:
	$(PYTHON) cloud-profiles/cloudprofile.py sync --check
	$(PYTHON) -m unittest discover -s cloud-profiles -v
	$(PYTHON) multi-region/tools/check_region_pair.py
	$(PYTHON) multi-region/tools/check_region_pair.py --values-dir multi-region/values/us --cloud-profile gcc
	$(PYTHON) multi-region/tools/check_region_pair.py --values-dir multi-region/values/us --cloud-profile commercial
	$(PYTHON) multi-region/tools/check_region_pair.py --values-dir multi-region/values/eu-azure --cloud-profile commercial+eu-data-boundary
	$(PYTHON) -m unittest discover -s multi-region -p 'test_chart.py' -v
	$(PYTHON) -m unittest discover -s multi-region/tools -p 'test_check_region_pair.py' -v
	$(PYTHON) -m unittest discover -s multi-region/tools -p 'test_residency_report.py' -v
	$(PYTHON) -m unittest discover -s secret-store -v

# AKS (#600): offline OpenTofu tests (providers mocked, no subscription needed) plus validate.
test-tofu:
	tofu -chdir=tofu/cluster init -backend=false -input=false
	tofu -chdir=tofu/cluster validate
	tofu -chdir=tofu/cluster test
	tofu -chdir=tofu/cluster/modules/aks init -backend=false -input=false
	tofu -chdir=tofu/cluster/modules/aks test

# Offline install bundle (#603, IL6 readiness): the tool's tests, with skopeo, cosign and helm replaced by a recorder.
test-offline:
	$(PYTHON) -m unittest discover -s offline -v

# Build the signed offline bundle on a connected machine: every image (pinned by digest, with signature and SBOM), chart and
# policy as one archive. OUT is the output directory, KEY the cosign private key (or a KMS/HSM URI). Needs skopeo, cosign, helm.
# STAGING is a throwaway registry the images are signed and attested in (docker run -p 5000:5000 registry:2); set
# STAGING_INSECURE=1 when it speaks plain http. Needs skopeo, syft, cosign and helm.
OUT ?= ../dist/offline
KEY ?=
STAGING ?= localhost:5000
STAGING_INSECURE ?=
offline-bundle:
	@test -n "$(KEY)" || { echo "Usage: make -C deploy offline-bundle KEY=<cosign private key> [OUT=<dir>] [STAGING=<registry>]" >&2; exit 2; }
	$(PYTHON) offline/offline_bundle.py build --out "$(OUT)" --key "$(KEY)" --staging-registry "$(STAGING)" $(if $(filter 1,$(STAGING_INSECURE)),--staging-insecure)
