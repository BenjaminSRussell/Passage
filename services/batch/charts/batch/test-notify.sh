#!/usr/bin/env bash
# Renders the batch chart and checks the notify adapters get their credentials only from the ESO-managed Secret:
# the ExternalSecret maps exactly the keys each enabled adapter reads, the Secret is mounted at NOTIFY_SECRETS_DIR,
# and no credential is rendered as env or plain value. Uses the real dev values (deploy/envs/dev/values/batch.yaml).
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$chart_dir/../../../.." && pwd)"
dev_values="$repo_root/deploy/envs/dev/values/batch.yaml"
digest=sha256:0000000000000000000000000000000000000000000000000000000000000000

render() {
  helm template batch "$chart_dir" --namespace batch --set image.digest="$digest" "$@"
}

# check <label> <expected ExternalSecret keys, comma-separated or empty> <expected NOTIFY_*_ENABLED, comma-separated>
check() {
  local label="$1" keys="$2" enabled="$3"
  ruby -ryaml -e '
    label, keys, enabled = ARGV
    keys = keys.split(",")
    enabled = enabled.split(",")
    docs = YAML.load_stream(STDIN.read).compact
    deployment = docs.find { |d| d["kind"] == "Deployment" } or abort "#{label}: no Deployment"
    pod = deployment.dig("spec", "template", "spec")
    container = pod["containers"].find { |c| c["name"] == "batch" }
    env = (container["env"] || []).to_h { |e| [e["name"], e] }
    credential_env = %w[NOTIFY_SMTP_HOST NOTIFY_SMTP_PORT NOTIFY_SMTP_USER NOTIFY_SMTP_PASSWORD NOTIFY_WEBHOOK_TOKEN]
    leaked = credential_env & env.keys
    abort "#{label}: credentials rendered as env: #{leaked}" unless leaked.empty?
    on = env.keys.grep(/\ANOTIFY_(SMTP|WEBHOOK)_ENABLED\z/).sort
    abort "#{label}: enabled adapters #{on}, want #{enabled.sort}" unless on == enabled.sort
    es = docs.select { |d| d["kind"] == "ExternalSecret" }
    if keys.empty?
      abort "#{label}: unexpected ExternalSecret" unless es.empty?
      abort "#{label}: NOTIFY_SECRETS_DIR set without notify" if env.key?("NOTIFY_SECRETS_DIR") && enabled.empty?
      exit 0
    end
    abort "#{label}: want one ExternalSecret, got #{es.size}" unless es.size == 1
    es = es.first
    got = es.dig("spec", "data").map { |d| d["secretKey"] }
    abort "#{label}: ExternalSecret keys #{got}, want #{keys}" unless got == keys
    es.dig("spec", "data").each do |d|
      ref = d["remoteRef"]
      abort "#{label}: #{d["secretKey"]} remoteRef #{ref}" unless ref["key"] == "passage/dev/batch/notify/#{d["secretKey"]}" && ref["property"] == "value"
    end
    abort "#{label}: ExternalSecret store" unless es.dig("spec", "secretStoreRef") == { "kind" => "ClusterSecretStore", "name" => "passage" }
    secret = es.dig("spec", "target", "name")
    dir = env.dig("NOTIFY_SECRETS_DIR", "value") or abort "#{label}: NOTIFY_SECRETS_DIR not set"
    mount = (container["volumeMounts"] || []).find { |m| m["mountPath"] == dir } or abort "#{label}: nothing mounted at #{dir}"
    abort "#{label}: secrets mount must be read-only" unless mount["readOnly"]
    volume = pod["volumes"].find { |v| v["name"] == mount["name"] } or abort "#{label}: no volume #{mount["name"]}"
    abort "#{label}: #{dir} mounts #{volume}, want Secret #{secret}" unless volume.dig("secret", "secretName") == secret
    abort "#{label}: the Secret must not be projected to a subset of keys" if volume.dig("secret", "items")
  ' "$label" "$keys" "$enabled"
}

smtp_keys=smtp-host,smtp-port,smtp-user,smtp-password

check "defaults" "" "" <<<"$(render)"
check "dev values" "$smtp_keys" NOTIFY_SMTP_ENABLED <<<"$(render -f "$dev_values")"
check "dev values + webhook" "$smtp_keys,webhook-token" NOTIFY_SMTP_ENABLED,NOTIFY_WEBHOOK_ENABLED \
  <<<"$(render -f "$dev_values" --set notify.webhook.enabled=true --set notify.webhook.url=https://hooks.example.test/notify)"
check "webhook only" "webhook-token" NOTIFY_WEBHOOK_ENABLED \
  <<<"$(render -f "$dev_values" --set notify.smtp.enabled=false --set notify.webhook.enabled=true --set notify.webhook.url=https://hooks.example.test/notify)"

# An enabled adapter without its non-secret settings, or notify without an adapter, must not render.
expect_fail() {
  local label="$1"; shift
  if render "$@" >/dev/null 2>&1; then
    printf '%s: rendered, want failure\n' "$label" >&2
    exit 1
  fi
}
expect_fail "smtp without from" -f "$dev_values" --set notify.smtp.from=
expect_fail "webhook without url" -f "$dev_values" --set notify.webhook.enabled=true
expect_fail "notify without adapter" --set notify.enabled=true
expect_fail "ExternalSecret without environment" -f "$dev_values" --set notify.externalSecret.environment=

# The Go loader must read the same keys (internal/notifyout/config.go).
config_go="$repo_root/services/batch/internal/notifyout/config.go"
for key in ${smtp_keys//,/ } webhook-token; do
  if ! grep -Fq -- "\"$key\"" "$config_go"; then
    printf 'internal/notifyout/config.go no longer reads secret key %s\n' "$key" >&2
    exit 1
  fi
done
echo "batch notify chart: ok"
