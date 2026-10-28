#!/usr/bin/env bash
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$chart_dir/../../../.." && pwd)"
dev_values="$(mktemp)"
trap 'rm -f "$dev_values"' EXIT

# Render the valuesObject from the real Argo CD dev Application so this test
# catches drift or removal in deploy/envs/dev/apps/receiving.yaml.
ruby -ryaml -e '
  app = YAML.load_file(ARGV[0])
  values = app.dig("spec", "source", "helm", "valuesObject")
  abort "Application is missing spec.source.helm.valuesObject" unless values.is_a?(Hash)
  File.write(ARGV[1], values.to_yaml)
' "$repo_root/deploy/envs/dev/apps/receiving.yaml" "$dev_values"

defaults="$(helm template receiving "$chart_dir" --namespace hello-ledger)"
dev="$(helm template receiving "$chart_dir" --namespace receiving-test --values "$dev_values")"

assert_contains() {
  local rendered="$1" expected="$2"
  if ! grep -Fq -- "$expected" <<<"$rendered"; then
    printf 'rendered chart is missing %s\n' "$expected" >&2
    exit 1
  fi
}

for name in LEDGER_URL CLOSE_URL PURCHASING_URL POSTING_LEDGER_ID \
  POSTING_ACCOUNT_INVENTORY POSTING_ACCOUNT_GRIR POSTING_ACCOUNT_PPV \
  POSTING_ACCOUNT_VENDOR_CONTROL; do
  assert_contains "$dev" "- name: $name"
done

assert_contains "$defaults" 'value: "http://ledger.hello-ledger.svc:8080"'
assert_contains "$defaults" 'value: "http://close.hello-ledger.svc:8080"'
assert_contains "$defaults" 'value: "http://purchasing.hello-ledger.svc:8080"'
assert_contains "$dev" 'value: "http://ledger.hello-ledger.svc:8080"'
assert_contains "$dev" 'value: "http://close.hello-ledger.svc:8080"'
assert_contains "$dev" 'value: "http://purchasing.hello-ledger.svc:8080"'
assert_contains "$dev" 'value: "00000000-0000-4000-8000-000000000001"'
assert_contains "$dev" 'value: "1400"'
assert_contains "$dev" 'value: "2100"'
assert_contains "$dev" 'value: "5200"'
assert_contains "$dev" 'value: "2000"'
