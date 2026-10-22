#!/usr/bin/env bash
# Build hello-ledger, reject it while unsigned, then sign it and admit it.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
image=${IMAGE:-hello-ledger:ci}
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

docker build -t "$image" "$root/services/hello-ledger"

# Unsigned image: the gate must fail.
set +e
COSIGN_PUBLIC_KEY="$workdir/missing.pub" "$root/scripts/supply-chain-gate.sh" "$image"
unsigned_code=$?
set -e
if [ "$unsigned_code" -eq 0 ]; then
  echo "unsigned image was admitted" >&2
  exit 1
fi
echo "unsigned image blocked as expected (exit $unsigned_code)"

export COSIGN_PASSWORD="${COSIGN_PASSWORD:-passage-ci}"
(cd "$workdir" && cosign generate-key-pair)
cosign sign --key "$workdir/cosign.key" --yes --tlog-upload=false "$image"
COSIGN_PUBLIC_KEY="$workdir/cosign.pub" "$root/scripts/supply-chain-gate.sh" "$image"

trivy image --severity CRITICAL --exit-code 1 --ignore-unfixed "$image"
echo "supply-chain gate passed for $image"
