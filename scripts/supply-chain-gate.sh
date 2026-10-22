#!/usr/bin/env bash
# Admit an image only when cosign verifies its signature and its SPDX attestation.
# A local registry (no Rekor) may ignore the transparency log. Any other registry may not.
set -euo pipefail

image=${1:?image ref required}

if [ -z "${COSIGN_PUBLIC_KEY:-}" ] || [ ! -f "$COSIGN_PUBLIC_KEY" ]; then
  echo "unsigned image blocked: COSIGN_PUBLIC_KEY is missing" >&2
  exit 1
fi

verify_flags=(--key "$COSIGN_PUBLIC_KEY")
case "$image" in
  127.0.0.1:*|localhost:*)
    verify_flags+=(--insecure-ignore-tlog --allow-insecure-registry --allow-http-registry)
    ;;
esac

if ! cosign verify "${verify_flags[@]}" "$image"; then
  echo "unsigned image blocked" >&2
  exit 1
fi

if ! cosign verify-attestation "${verify_flags[@]}" --type spdxjson "$image"; then
  echo "sbom attestation missing" >&2
  exit 1
fi

echo "image admitted: $image"
