#!/usr/bin/env bash
# Install the pinned linux/amd64 CLIs. Re-runs replace a binary whose version
# does not match the pin. Each download is checked against the publisher checksum.
set -euo pipefail

if [ "$(uname -s)" != "Linux" ] || [ "$(uname -m)" != "x86_64" ]; then
  echo "install-dev-tools.sh verifies linux/amd64 checksums only" >&2
  exit 1
fi

HELM_VERSION=v3.22.1
HELM_SHA256=896cf63684a42a723d61c980369d6e588330927deb13676ca0d85006dd6da0ea
TOFU_VERSION=v1.13.2
TOFU_SHA256=723bebb0df8411daff1e5fc3862e28ff5581fe5634087f4c29a9aa41d78208b5
K3D_VERSION=v5.9.0
K3D_SHA256=06d8f25bc3a971c4eb29e0ff08429b180402db0f4dec838c9eac427e296800a0
SYFT_VERSION=v1.55.0
SYFT_SHA256=bb65249542db5fedeeb4d3f9b018b75e7264dbfbbb10486f044581a92f90490d
TRIVY_VERSION=v0.76.1
TRIVY_SHA256=c7cbe3137fb8a48a17574bede3dd6beb16742d1e9772929940fc90082c39d9c0
COSIGN_VERSION=v3.1.4
COSIGN_SHA256=424e2c97d0b45fd7a6b608a87f090f35633f4c23ece5f092d48bba11aaf4bbfa
KUBECTL_VERSION=v1.37.1
KUBECTL_SHA256=65691ff77eb6fa44c908b77a1082c9f092c3b9733b5cefabec0d1104890e21a8

install_file() {
  local src=$1
  local name=$2
  if [ -w /usr/local/bin ]; then
    install -m 0755 "$src" "/usr/local/bin/$name"
  else
    sudo install -m 0755 "$src" "/usr/local/bin/$name"
  fi
}

fetch_verified() {
  local url=$1
  local dest=$2
  local sum=$3
  curl -fsSL "$url" -o "$dest"
  echo "${sum}  ${dest}" | sha256sum -c -
}

version_ok() {
  local bin=$1
  local expect=$2
  shift 2
  if ! command -v "$bin" >/dev/null 2>&1; then
    return 1
  fi
  if ! "$bin" "$@" 2>&1 | grep -F -q "$expect"; then
    return 1
  fi
}

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

if ! version_ok helm "$HELM_VERSION" version --short; then
  fetch_verified "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz" "$tmpdir/helm.tgz" "$HELM_SHA256"
  tar -xzf "$tmpdir/helm.tgz" -C "$tmpdir"
  install_file "$tmpdir/linux-amd64/helm" helm
fi

if ! version_ok tofu "${TOFU_VERSION#v}" version; then
  fetch_verified "https://github.com/opentofu/opentofu/releases/download/${TOFU_VERSION}/tofu_${TOFU_VERSION#v}_linux_amd64.zip" "$tmpdir/tofu.zip" "$TOFU_SHA256"
  unzip -o "$tmpdir/tofu.zip" -d "$tmpdir/tofu"
  install_file "$tmpdir/tofu/tofu" tofu
fi

if ! version_ok k3d "$K3D_VERSION" version; then
  fetch_verified "https://github.com/k3d-io/k3d/releases/download/${K3D_VERSION}/k3d-linux-amd64" "$tmpdir/k3d" "$K3D_SHA256"
  install_file "$tmpdir/k3d" k3d
fi

if ! version_ok kubectl "$KUBECTL_VERSION" version --client; then
  fetch_verified "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" "$tmpdir/kubectl" "$KUBECTL_SHA256"
  install_file "$tmpdir/kubectl" kubectl
fi

if ! version_ok syft "${SYFT_VERSION#v}" version; then
  fetch_verified "https://github.com/anchore/syft/releases/download/${SYFT_VERSION}/syft_${SYFT_VERSION#v}_linux_amd64.tar.gz" "$tmpdir/syft.tgz" "$SYFT_SHA256"
  tar -xzf "$tmpdir/syft.tgz" -C "$tmpdir" syft
  install_file "$tmpdir/syft" syft
fi

if ! version_ok trivy "${TRIVY_VERSION#v}" version; then
  fetch_verified "https://github.com/aquasecurity/trivy/releases/download/${TRIVY_VERSION}/trivy_${TRIVY_VERSION#v}_Linux-64bit.tar.gz" "$tmpdir/trivy.tgz" "$TRIVY_SHA256"
  tar -xzf "$tmpdir/trivy.tgz" -C "$tmpdir" trivy
  install_file "$tmpdir/trivy" trivy
fi

if ! version_ok cosign "${COSIGN_VERSION#v}" version; then
  fetch_verified "https://github.com/sigstore/cosign/releases/download/${COSIGN_VERSION}/cosign-linux-amd64" "$tmpdir/cosign" "$COSIGN_SHA256"
  install_file "$tmpdir/cosign" cosign
fi

echo "dev tools ready: helm tofu k3d kubectl syft trivy cosign"
