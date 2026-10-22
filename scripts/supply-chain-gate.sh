#!/usr/bin/env bash
# Admit an image only when an SBOM exists and cosign verification succeeds.
set -euo pipefail

image=${1:?image ref required}
sbom=$(mktemp)
trap 'rm -f "$sbom"' EXIT

syft "$image" -o spdx-json > "$sbom"
if [ ! -s "$sbom" ]; then
  echo "sbom missing" >&2
  exit 1
fi

if [ -z "${COSIGN_PUBLIC_KEY:-}" ]; then
  echo "unsigned image blocked: COSIGN_PUBLIC_KEY is not set" >&2
  exit 1
fi

if ! cosign verify --key "$COSIGN_PUBLIC_KEY" --insecure-ignore-tlog "$image"; then
  echo "unsigned image blocked" >&2
  exit 1
fi

echo "image admitted: $image"
