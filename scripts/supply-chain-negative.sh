#!/usr/bin/env bash
# Build hello-ledger, reject it while unsigned, then sign it and admit it.
# Cosign v3 signs a registry digest. CI uses a local registry so no credentials
# are required, and a signing config with no transparency log so the check
# does not depend on the public Rekor service.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
registry=${REGISTRY:-127.0.0.1:5000}
local_image=${IMAGE:-hello-ledger:ci}
remote_image="${registry}/hello-ledger:ci"
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

docker build -t "$local_image" "$root/services/hello-ledger"

if ! docker inspect passage-registry >/dev/null 2>&1; then
  docker run -d --name passage-registry -p 127.0.0.1:5000:5000 registry:2
fi
docker start passage-registry >/dev/null
for _ in $(seq 1 30); do
  if curl -sf "http://${registry}/v2/" >/dev/null; then
    break
  fi
  sleep 1
done
curl -sf "http://${registry}/v2/" >/dev/null

docker tag "$local_image" "$remote_image"
docker push "$remote_image"
digest=$(docker image inspect --format '{{index .RepoDigests 0}}' "$remote_image")
if [[ "$digest" != *"@sha256:"* ]]; then
  echo "push did not produce a digest for $remote_image" >&2
  exit 1
fi

export COSIGN_PASSWORD="${COSIGN_PASSWORD:-passage-ci}"
(cd "$workdir" && cosign generate-key-pair)
cosign signing-config create \
  --no-default-fulcio \
  --no-default-oidc \
  --no-default-rekor \
  --no-default-tsa \
  --out "$workdir/signing-config.json"

set +e
COSIGN_PUBLIC_KEY="$workdir/cosign.pub" "$root/scripts/supply-chain-gate.sh" "$digest"
unsigned_code=$?
set -e
if [ "$unsigned_code" -eq 0 ]; then
  echo "unsigned image was admitted" >&2
  exit 1
fi
echo "unsigned image blocked as expected (exit $unsigned_code)"

cosign sign --key "$workdir/cosign.key" --yes \
  --signing-config "$workdir/signing-config.json" \
  --allow-insecure-registry \
  "$digest"
sbom="$workdir/sbom.spdx.json"
SYFT_REGISTRY_INSECURE_USE_HTTP=true syft "$digest" -o spdx-json > "$sbom"
cosign attest --key "$workdir/cosign.key" --yes \
  --signing-config "$workdir/signing-config.json" \
  --allow-insecure-registry --allow-http-registry \
  --predicate "$sbom" --type spdxjson \
  "$digest"
COSIGN_PUBLIC_KEY="$workdir/cosign.pub" "$root/scripts/supply-chain-gate.sh" "$digest"

trivy image --severity CRITICAL --exit-code 1 --ignore-unfixed --scanners vuln "$local_image"
echo "supply-chain gate passed for $digest"
