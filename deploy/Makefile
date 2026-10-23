SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c

REPO_ROOT := $(abspath ..)
CLUSTER := passage-local
IMAGE := passage-local/hello-ledger:local
REVISION ?= main

.PHONY: deploy-local destroy-local

deploy-local:
	tofu -chdir=tofu/cluster init -input=false
	tofu -chdir=tofu/cluster apply -input=false -auto-approve
	docker build -f $(REPO_ROOT)/services/platform-health/Dockerfile -t $(IMAGE) $(REPO_ROOT)
	k3d image import $(IMAGE) -c $(CLUSTER)
	kubectl --context k3d-$(CLUSTER) create namespace hello-ledger --dry-run=client -o yaml | kubectl --context k3d-$(CLUSTER) apply -f -
	kubectl --context k3d-$(CLUSTER) -n hello-ledger get secret hello-ledger-db >/dev/null 2>&1 || { \
		pw=$$(openssl rand -hex 16); \
		kubectl --context k3d-$(CLUSTER) -n hello-ledger create secret generic hello-ledger-db \
			--from-literal=POSTGRES_PASSWORD=$$pw \
			--from-literal=DATABASE_URL="postgres://passage:$$pw@postgres:5432/hello?sslmode=disable"; }
	sed "s|targetRevision: main|targetRevision: $(REVISION)|" argocd/root-local.yaml | kubectl --context k3d-$(CLUSTER) apply -f -
	sed "s|targetRevision: main|targetRevision: $(REVISION)|" argocd/hello-ledger-local.yaml | kubectl --context k3d-$(CLUSTER) apply -f -
	until kubectl --context k3d-$(CLUSTER) -n hello-ledger get deploy/hello-ledger >/dev/null 2>&1; do sleep 5; done
	kubectl --context k3d-$(CLUSTER) -n hello-ledger rollout status deploy/hello-ledger --timeout=300s
	@echo "hello-ledger is up: add '127.0.0.1 app.local.passage.dev' to /etc/hosts and open https://app.local.passage.dev/readyz"

destroy-local:
	tofu -chdir=tofu/cluster destroy -input=false -auto-approve
