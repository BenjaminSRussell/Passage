#!/usr/bin/env bash
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ingress_rules() {
  ruby -ryaml -rjson -e '
    docs = YAML.load_stream(STDIN.read).compact
    policy = docs.find { |d| d["kind"] == "NetworkPolicy" }
    abort "no NetworkPolicy rendered" unless policy
    puts JSON.generate(policy.dig("spec", "ingress") || [])
  '
}

assert_rules() {
  local label="$1" rendered="$2" port="$3" mode="$4"
  ruby -rjson -e '
    label, port, mode = ARGV
    rules = JSON.parse(STDIN.read).select { |r| (r["ports"] || []).any? { |p| p["port"].to_s == port } }
    from = rules.flat_map { |r| r["from"] || [] }
    bare = from.any? { |f| f["podSelector"] == {} && !f.key?("namespaceSelector") }
    open_rule = rules.any? { |r| (r["from"] || []).empty? }
    abort "#{label}: port #{port} is admitted from every source" if open_rule
    case mode
    when "scoped"
      abort "#{label}: no rule admits port #{port}" if from.empty?
      abort "#{label}: port #{port} admits every pod in the namespace" if bare
      from.each do |f|
        labels = f.dig("podSelector", "matchLabels")
        abort "#{label}: peer #{f} lacks a pod label selector" if labels.nil? || labels.empty?
      end
      abort "#{label}: peers must pair a namespaceSelector with pod labels" unless from.all? { |f| f.key?("namespaceSelector") }
    when "labelled"
      abort "#{label}: no rule admits port #{port}" if from.empty?
      from.each { |f| abort "#{label}: peer #{f} lacks pod labels" if (f.dig("podSelector", "matchLabels") || {}).empty? }
    when "closed"
      abort "#{label}: port #{port} should be closed, got #{from}" unless from.empty?
    when "same-namespace"
      abort "#{label}: opt-in same-namespace peer missing" unless bare
    end
  ' "$label" "$port" "$mode" <<<"$rendered"
}

render() {
  helm template identity "$chart_dir" --namespace hello-ledger "$@" | ingress_rules
}

assert_rules defaults "$(render)" 8080 scoped
grep -Fq 'gateway.envoyproxy.io/owning-gateway-name: passage-api' <(helm template identity "$chart_dir")

gateway_ns_render="$(helm template identity "$chart_dir" --namespace hello-ledger --set-json 'networkPolicy.gatewayNamespaces=["hello-ledger"]')"
grep -Fq 'kubernetes.io/metadata.name: hello-ledger' <<<"$gateway_ns_render"
assert_rules gateway-namespace-mode "$(ingress_rules <<<"$gateway_ns_render")" 8080 scoped

assert_rules same-namespace-opt-in "$(render --set networkPolicy.allowSameNamespace=true)" 8080 same-namespace
assert_rules no-peers "$(render --set-json 'networkPolicy.gatewayNamespaces=[]')" 8080 closed
assert_rules bundle "$(render --set bundle.enabled=true)" 8443 labelled
assert_rules metrics-default "$(render)" 9090 closed
assert_rules metrics-scrape "$(render --set-json 'networkPolicy.metricsNamespaces=["monitoring"]')" 9090 scoped

if helm template identity "$chart_dir" --set bundle.enabled=true --set networkPolicy.opaPodLabels=null >/dev/null 2>&1; then
  echo "bundle.enabled with empty opaPodLabels must fail to render" >&2
  exit 1
fi

helm template identity "$chart_dir" --set migrateDatabaseSecret=identity-db-owner | grep -Fq 'MIGRATE_DATABASE_URL'
if helm template identity "$chart_dir" | grep -Fq 'MIGRATE_DATABASE_URL'; then
  echo "MIGRATE_DATABASE_URL must render only when migrateDatabaseSecret is set" >&2
  exit 1
fi
