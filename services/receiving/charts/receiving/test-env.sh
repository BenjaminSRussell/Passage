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

assert_env() {
  local rendered="$1" name="$2" expected="$3"
  ruby -ryaml -e '
    docs = YAML.load_stream(STDIN.read).compact
    deployment = docs.find { |d| d["kind"] == "Deployment" }
    env = deployment.dig("spec", "template", "spec", "containers", 0, "env")
    entry = env.find { |e| e["name"] == ARGV[0] }
    actual = entry && entry["value"]
    abort "#{ARGV[0]}: expected #{ARGV[1].inspect}, got #{actual.inspect}" unless actual == ARGV[1]
  ' "$name" "$expected" <<<"$rendered"
}

assert_env "$defaults" LEDGER_URL "http://ledger.hello-ledger.svc:8080"
assert_env "$defaults" CLOSE_URL "http://close.hello-ledger.svc:8080"
assert_env "$defaults" PURCHASING_URL "http://purchasing-purchasing.hello-ledger.svc"
assert_env "$dev" LEDGER_URL "http://ledger.hello-ledger.svc:8080"
assert_env "$dev" CLOSE_URL "http://close.hello-ledger.svc:8080"
assert_env "$dev" PURCHASING_URL "http://purchasing-purchasing.hello-ledger.svc"
assert_env "$dev" POSTING_LEDGER_ID "00000000-0000-4000-8000-000000000001"
assert_env "$dev" POSTING_ACCOUNT_INVENTORY "1400"
assert_env "$dev" POSTING_ACCOUNT_GRIR "2100"
assert_env "$dev" POSTING_ACCOUNT_PPV "5200"
assert_env "$dev" POSTING_ACCOUNT_VENDOR_CONTROL "2000"
assert_env "$dev" POSTING_ACCOUNT_EXPENSE "6000"
assert_env "$dev" POSTING_ACCOUNT_INPUT_TAX "1570"

ports_go="$repo_root/services/receiving/cmd/receiving/ports.go"
for name in LEDGER_URL CLOSE_URL PURCHASING_URL POSTING_LEDGER_ID \
  POSTING_ACCOUNT_INVENTORY POSTING_ACCOUNT_GRIR POSTING_ACCOUNT_PPV \
  POSTING_ACCOUNT_VENDOR_CONTROL POSTING_ACCOUNT_EXPENSE POSTING_ACCOUNT_INPUT_TAX TAX_URL; do
  if ! grep -Fq -- "\"$name\"" "$ports_go"; then
    printf 'cmd/receiving/ports.go no longer reads %s\n' "$name" >&2
    exit 1
  fi
done
