#!/usr/bin/env bash
# Admit an image only when an SBOM exists and cosign verification succeeds.
set -euo pipefail

image=${1:?image ref required}
sbom=$(mktemp)
trap 'rm -f "$sbom"' EXIT

# Local registries used by CI speak plain HTTP.
if [[ "$image" == 127.0.0.1:* || "$image" == localhost:* || "$image" == *"/127.0.0.1/"* || "$image" == *"localhost/"* ]]; then
  export SYFT_REGISTRY_INSECURE_USE_HTTP=true
  verify_flags=(--allow-insecure-registry)
else
  verify_flags=()
fi

syft "$image" -o spdx-json > "$sbom"
if [ ! -s "$sbom" ]; then
  echo "sbom missing" >&2
  exit 1
fi

if [ -z "${COSIGN_PUBLIC_KEY:-}" ] || [ ! -f "$COSIGN_PUBLIC_KEY" ]; then
  echo "unsigned image blocked: COSIGN_PUBLIC_KEY is missing" >&2
  exit 1
fi

if ! cosign verify --key "$COSIGN_PUBLIC_KEY" --insecure-ignore-tlog "${verify_flags[@]}" "$image"; then
  echo "unsigned image blocked" >&2
  exit 1
fi

echo "image admitted: $image"
