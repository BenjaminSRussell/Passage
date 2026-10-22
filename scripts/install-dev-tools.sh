#!/usr/bin/env bash
# Install pinned CLI tools used by CI and local delivery. Safe to re-run.
set -euo pipefail

HELM_VERSION=v3.22.1
TOFU_VERSION=v1.13.2
K3D_VERSION=v5.9.0
SYFT_VERSION=v1.55.0
TRIVY_VERSION=v0.76.1
COSIGN_VERSION=v3.1.4
KUBECTL_VERSION=v1.37.1

install_file() {
  local src=$1
  local name=$2
  if [ -w /usr/local/bin ]; then
    install -m 0755 "$src" "/usr/local/bin/$name"
  else
    sudo install -m 0755 "$src" "/usr/local/bin/$name"
  fi
}

fetch() {
  local url=$1
  local dest=$2
  curl -fsSL "$url" -o "$dest"
}

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

if ! command -v helm >/dev/null 2>&1 || ! helm version --short | grep -q "$HELM_VERSION"; then
  fetch "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz" "$tmpdir/helm.tgz"
  tar -xzf "$tmpdir/helm.tgz" -C "$tmpdir"
  install_file "$tmpdir/linux-amd64/helm" helm
fi

if ! command -v tofu >/dev/null 2>&1 || ! tofu version | grep -q "${TOFU_VERSION#v}"; then
  fetch "https://github.com/opentofu/opentofu/releases/download/${TOFU_VERSION}/tofu_${TOFU_VERSION#v}_linux_amd64.zip" "$tmpdir/tofu.zip"
  unzip -o "$tmpdir/tofu.zip" -d "$tmpdir/tofu"
  install_file "$tmpdir/tofu/tofu" tofu
fi

if ! command -v k3d >/dev/null 2>&1; then
  fetch "https://github.com/k3d-io/k3d/releases/download/${K3D_VERSION}/k3d-linux-amd64" "$tmpdir/k3d"
  install_file "$tmpdir/k3d" k3d
fi

if ! command -v kubectl >/dev/null 2>&1; then
  fetch "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" "$tmpdir/kubectl"
  install_file "$tmpdir/kubectl" kubectl
fi

if ! command -v syft >/dev/null 2>&1; then
  fetch "https://github.com/anchore/syft/releases/download/${SYFT_VERSION}/syft_${SYFT_VERSION#v}_linux_amd64.tar.gz" "$tmpdir/syft.tgz"
  tar -xzf "$tmpdir/syft.tgz" -C "$tmpdir" syft
  install_file "$tmpdir/syft" syft
fi

if ! command -v trivy >/dev/null 2>&1; then
  fetch "https://github.com/aquasecurity/trivy/releases/download/${TRIVY_VERSION}/trivy_${TRIVY_VERSION#v}_Linux-64bit.tar.gz" "$tmpdir/trivy.tgz"
  tar -xzf "$tmpdir/trivy.tgz" -C "$tmpdir" trivy
  install_file "$tmpdir/trivy" trivy
fi

if ! command -v cosign >/dev/null 2>&1; then
  fetch "https://github.com/sigstore/cosign/releases/download/${COSIGN_VERSION}/cosign-linux-amd64" "$tmpdir/cosign"
  install_file "$tmpdir/cosign" cosign
fi

echo "dev tools ready: helm tofu k3d kubectl syft trivy cosign"
