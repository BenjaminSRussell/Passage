#!/usr/bin/env bash
set -euo pipefail

image="${1:?usage: negative-unsigned-image.sh <ghcr.io/benjaminsrussell/IMAGE@sha256:DIGEST of a pushed but unsigned image> (CONTEXT, NAMESPACE optional)}"
namespace="${NAMESPACE:-hello-ledger}"
manifest=$(printf 'apiVersion: v1\nkind: Pod\nmetadata:\n  name: unsigned-image-probe\n  namespace: %s\nspec:\n  containers:\n    - name: probe\n      image: %s\n' "$namespace" "$image")

if output=$(printf '%s' "$manifest" | kubectl ${CONTEXT:+--context "$CONTEXT"} apply --dry-run=server -f - 2>&1); then
  echo "FAIL: unsigned image $image was admitted" >&2
  exit 1
fi

if grep -Eqi "no matching signatures|no signatures found|signature verification failed" <<<"$output"; then
  echo "PASS: unsigned image rejected by signature verification"
  exit 0
fi

echo "FAIL: request rejected, but not by signature verification:" >&2
echo "$output" >&2
exit 1
