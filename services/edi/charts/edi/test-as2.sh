#!/usr/bin/env bash
# The AS2 listener's port is reachable only from the AS2 edge's proxies, its settings reach the container, and the
# dev values point it at the AS2 edge hostname.
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$chart_dir/../../../.." && pwd)"
dev_values="$repo_root/deploy/envs/dev/values/edi.yaml"

render() {
  helm template edi "$chart_dir" --namespace hello-ledger "$@"
}

# check RENDERED LABEL RUBY: runs the Ruby check with docs (every rendered document) bound.
check() {
  local label="$1" rendered="$2" code="$3"
  ruby -ryaml -e '
    label, code = ARGV
    docs = YAML.load_stream(STDIN.read).compact
    policy = docs.find { |d| d["kind"] == "NetworkPolicy" }
    deployment = docs.find { |d| d["kind"] == "Deployment" }
    env = (deployment.dig("spec", "template", "spec", "containers", 0, "env") || []).to_h { |e| [e["name"], e["value"]] }
    rules = policy ? (policy.dig("spec", "ingress") || []) : []
    rule_for = ->(port) { rules.select { |r| (r["ports"] || []).any? { |p| p["port"].to_s == port.to_s } } }
    begin
      eval(code)
    rescue => e
      abort "#{label}: #{e.message}"
    end
  ' "$label" "$code" <<<"$rendered"
}

# Defaults: no AS2 listener, so no AS2 port, Service or rule; the API and metrics ports stay open as before.
check defaults "$(render)" '
  raise "no NetworkPolicy" unless policy
  raise "AS2 Service rendered" if docs.any? { |d| d["kind"] == "Service" && d["metadata"]["name"] == "edi-as2" }
  raise "AS2 rule without the listener" unless rules.size == 1
  raise "API rule restricted by default" if rules[0].key?("from")
  raise "EDI_AS2_ADDR set" if env.key?("EDI_AS2_ADDR")
'

# The listener on: its port admits only the Envoy proxies of Gateway passage-as2, and nothing else.
as2="$(render --set transports.as2.enabled=true --set transports.workers=true)"
check as2-enabled "$as2" '
  as2 = rule_for.(8081)
  raise "expected one rule for 8081, got #{as2}" unless as2.size == 1
  raise "8081 shares a rule with the API" unless as2[0]["ports"].map { |p| p["port"] } == [8081]
  peers = as2[0]["from"] || []
  raise "8081 is open to every source" if peers.empty?
  peers.each do |f|
    raise "peer without namespaceSelector: #{f}" unless f.dig("namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") == "envoy-gateway-system"
    labels = f.dig("podSelector", "matchLabels") || {}
    raise "peer is not the passage-as2 gateway: #{labels}" unless labels["gateway.envoyproxy.io/owning-gateway-name"] == "passage-as2" &&
      labels["gateway.envoyproxy.io/owning-gateway-namespace"] == "hello-ledger"
  end
  raise "8081 also admitted by the API rule" unless rule_for.(8080).none? { |r| r["ports"].any? { |p| p["port"] == 8081 } }
  svc = docs.find { |d| d["kind"] == "Service" && d["metadata"]["name"] == "edi-as2" }
  raise "no edi-as2 Service on 8081" unless svc && svc.dig("spec", "ports", 0, "port") == 8081
  { "EDI_AS2_ADDR" => ":8081", "EDI_AS2_RATE_PER_MINUTE" => "120", "EDI_AS2_RATE_BURST" => "60", "EDI_AS2_MDN_TIMEOUT_MINUTES" => "1440" }.each do |k, v|
    raise "#{k}: #{env[k].inspect}, want #{v.inspect}" unless env[k] == v
  end
'

# Without gateway labels the AS2 port would be open to everyone: the chart refuses to render.
if render --set transports.as2.enabled=true --set networkPolicy.as2Gateway.podLabels=null >/dev/null 2>&1; then
  echo "transports.as2.enabled with empty networkPolicy.as2Gateway.podLabels must fail to render" >&2
  exit 1
fi
if render --set transports.as2.enabled=true --set-json 'networkPolicy.as2Gateway.namespaces=[]' >/dev/null 2>&1; then
  echo "transports.as2.enabled with no networkPolicy.as2Gateway.namespaces must fail to render" >&2
  exit 1
fi

# Dev: the listener and the workers on, the public URL on the AS2 edge hostname.
check dev "$(render --values "$dev_values")" '
  raise "dev: EDI_AS2_PUBLIC_URL #{env["EDI_AS2_PUBLIC_URL"].inspect}" unless env["EDI_AS2_PUBLIC_URL"] == "https://as2.dev.passage.dev"
  raise "dev: workers off (the asynchronous MDNs we owe would never be posted)" unless env["EDI_TRANSPORT_WORKERS"] == "true"
  raise "dev: no AS2 rule" unless rule_for.(8081).size == 1
'
echo "edi chart AS2 checks passed"
