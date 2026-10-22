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

# host-gw avoids VXLAN. Nested VMs (and many laptops) cannot create a vxlan device,
# and k3s then crash-loops with "operation not supported".
if ! k3d cluster list -o json | grep -q '"name":"passage"'; then
  k3d cluster create passage --agents 0 --port "18080:80@loadbalancer" \
    --k3s-arg "--flannel-backend=host-gw@server:*" \
    --k3s-arg "--disable=traefik@server:0" --wait
fi

k3d kubeconfig merge passage --kubeconfig-merge-default --kubeconfig-switch-context >/dev/null
server=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
if [[ "$server" == https://0.0.0.0:* ]]; then
  kubectl config set-cluster k3d-passage --server="https://127.0.0.1:${server##*:}"
fi

echo "waiting for the Kubernetes API"
ready=0
for _ in $(seq 1 40); do
  if kubectl get nodes >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 3
done
if [ "$ready" != 1 ]; then
  echo "kubernetes API did not become ready" >&2
  exit 1
fi
kubectl wait --for=condition=Ready nodes --all --timeout=180s
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
argo_manifest=$(mktemp)
argo_url=https://raw.githubusercontent.com/argoproj/argo-cd/v3.5.6/manifests/install.yaml
argo_sha256=846c644bc81ce5557dd9048757a9c7353378f25c5eb811c77d32a9e907823599
curl -fsSL "$argo_url" -o "$argo_manifest"
echo "${argo_sha256}  ${argo_manifest}" | sha256sum -c -
kubectl apply -n argocd --server-side -f "$argo_manifest"
rm -f "$argo_manifest"
kubectl -n argocd rollout status deploy/argocd-server --timeout=300s

docker build -t hello-ledger:local services/hello-ledger
k3d image import hello-ledger:local -c passage

helm upgrade --install hello-ledger deploy/helm/hello-ledger \
  -f deploy/helm/hello-ledger/values-local.yaml \
  --namespace passage --create-namespace --wait --timeout 180s

kubectl apply -n argocd -f deploy/argocd/passage.yaml
kubectl apply -n argocd -f deploy/argocd/apps/project.yaml
kubectl apply -n argocd -f deploy/argocd/apps/

kubectl -n passage rollout status deploy/hello-ledger --timeout=180s
kubectl -n passage port-forward svc/hello-ledger 18080:80 >/tmp/hello-ledger-port-forward.log 2>&1 &
port_forward=$!
cleanup() { kill "$port_forward" >/dev/null 2>&1 || true; }
trap cleanup EXIT

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
