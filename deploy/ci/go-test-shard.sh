#!/usr/bin/env bash
# Run one Go CI shard. Slow packages get dedicated shards; everything else is
# hashed into rest-0..rest-2 so `go list` stays the source of package membership.
# Packages get 20 minutes, not go test's 10-minute default: the identity package checks every duty
# against the real policy and outgrew the default on a slow runner.
set -euo pipefail

shard=${1:?usage: go-test-shard.sh SHARD}

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"

shard_for() {
  local pkg=$1
  case $pkg in
    github.com/BenjaminSRussell/passage/services/pricing/internal/store) echo pricing-store ;;
    github.com/BenjaminSRussell/passage/services/identity|github.com/BenjaminSRussell/passage/services/identity/*|github.com/BenjaminSRussell/passage/tools/e2e|github.com/BenjaminSRussell/passage/tools/e2e/*) echo identity ;;
    github.com/BenjaminSRussell/passage/services/inventory/internal/store) echo inventory-store ;;
    github.com/BenjaminSRussell/passage/services/ledger/internal/store) echo ledger-store ;;
    github.com/BenjaminSRussell/passage/services/org/internal/store) echo org-store ;;
    github.com/BenjaminSRussell/passage/services/engineering/internal/store) echo engineering-store ;;
    *)
      local n
      n=$(printf '%s' "$pkg" | cksum | awk '{print $1}')
      echo "rest-$((n % 3))"
      ;;
  esac
}

modules=()
while IFS= read -r dir; do
  modules+=("$dir")
done < <(find platform/go services tools/rlscheck tools/tenant-restore tools/e2e tools/passage-server -name go.mod -not -path '*/node_modules/*' -exec dirname {} \; | sort)

matched=0
failed=0
for mod in "${modules[@]}"; do
  prefix=$(cd "$mod" && go list -m)
  pkgs=()
  if ! listed=$(cd "$mod" && go list ./...); then
    echo "go-test-shard: go list ./... failed in $mod" >&2
    failed=1
    continue
  fi
  while IFS= read -r pkg; do
    [ -n "$pkg" ] || continue
    [ "$(shard_for "$pkg")" = "$shard" ] || continue
    matched=$((matched + 1))
    rel=${pkg#"$prefix"}
    if [ -z "$rel" ]; then
      pkgs+=(".")
    else
      pkgs+=(".$rel")
    fi
  done <<< "$listed"
  [ "${#pkgs[@]}" -eq 0 ] && continue
  echo "== go test $mod ${pkgs[*]}"
  if ! (cd "$mod" && go test -count=1 -p 4 -timeout 20m "${pkgs[@]}"); then
    failed=1
  fi
done

if [ "$matched" -eq 0 ]; then
  echo "go-test-shard: no packages assigned to $shard" >&2
  exit 1
fi
echo "== shard $shard ($matched packages)"
exit "$failed"
