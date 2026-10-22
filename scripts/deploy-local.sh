#!/usr/bin/env bash
# Bring up k3d, Argo CD, and hello-ledger. Idempotent.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

for tool in docker k3d kubectl helm; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "$tool is required. Run scripts/install-dev-tools.sh and install Docker." >&2
    exit 1
  fi
done

if ! docker info >/dev/null 2>&1; then
  echo "docker daemon is not running" >&2
  exit 1
fi

if ! k3d cluster list -o json | grep -q '"name":"passage"'; then
  k3d cluster create passage --agents 0 --port "18080:80@loadbalancer" --wait
fi

kubectl config use-context k3d-passage
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n argocd --server-side -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
kubectl -n argocd rollout status deploy/argocd-server --timeout=300s

docker build -t hello-ledger:local services/hello-ledger
k3d image import hello-ledger:local -c passage

helm upgrade --install hello-ledger deploy/helm/hello-ledger \
  -f deploy/helm/hello-ledger/values-local.yaml \
  --namespace passage --create-namespace --wait --timeout 180s

kubectl apply -n argocd -f deploy/argocd/passage.yaml
kubectl apply -n argocd -f deploy/argocd/apps/

for _ in $(seq 1 30); do
  if curl -sf http://127.0.0.1:18080/healthz; then
    echo
    echo "hello-ledger is up at http://127.0.0.1:18080/healthz"
    exit 0
  fi
  sleep 2
done

echo "hello-ledger did not become ready on :18080" >&2
kubectl -n passage describe deploy/hello-ledger || true
exit 1
