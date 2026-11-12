{{/*
Cloud profile helpers (#599). global.* comes from deploy/cloud-profiles/values/<profile>.yaml.

passage.mirrorImage: an image reference with the registry mirror applied. global.imageRegistry replaces the registry host
(ghcr.io/cloudnative-pg/postgresql:17 becomes <registry>/cloudnative-pg/postgresql:17; postgres:17-alpine becomes
<registry>/postgres:17-alpine). A profile that pulls only from a mirror fails the render when none is set.
Usage: include "passage.mirrorImage" (dict "root" $ "ref" .Values.image)
*/}}
{{- define "passage.mirrorImage" -}}
{{- $g := .root.Values.global | default dict -}}
{{- if $g.imageRegistry -}}
{{- printf "%s/%s" (trimSuffix "/" $g.imageRegistry) (regexReplaceAll "^[^/]*[.:][^/]*/" .ref "") -}}
{{- else -}}
{{- if $g.imageRegistryRequired -}}{{- fail "global.imageRegistry is required: this cloud profile pulls images only from a registry mirror (docs/ops/cloud-profiles.md)" -}}{{- end -}}
{{- .ref -}}
{{- end -}}
{{- end -}}

{{/*
passage.secretKey / passage.secretProperty: ExternalSecret remote references for the configured store. Key Vault secret
names allow only letters, digits and dashes and hold a plain value, so with global.secretStoreProvider "azure-key-vault"
(set by every cloud profile values file) path separators become dashes (passage/dev/x/y becomes passage-dev-x-y) and
`property: value` is left out. Any other store (Vault, the default) keeps the path and the property.
*/}}
{{- define "passage.secretKey" -}}
{{- if eq ((.root.Values.global | default dict).secretStoreProvider | default "") "azure-key-vault" -}}
{{- regexReplaceAll "[^0-9A-Za-z-]" .key "-" -}}
{{- else -}}
{{- .key -}}
{{- end -}}
{{- end -}}

{{- define "passage.secretProperty" -}}
{{- if ne ((.Values.global | default dict).secretStoreProvider | default "") "azure-key-vault" -}}
property: value
{{- end -}}
{{- end -}}

{{/*
Workload identity for services that unwrap fieldcrypt keys (#599, #602). With global.workloadIdentity.fieldcryptClientId
(the fieldcrypt_identity_client_id output of deploy/tofu/cluster) the chart creates a service account named
passage-<service> annotated with the client id, runs the pods as it and labels them azure.workload.identity/use. The
OpenTofu module federates exactly these service account names (fieldcrypt_service_accounts), so the names are fixed.
Unset, the pods keep the namespace's default account and nothing changes.
*/}}
{{- define "passage.wiLabels" -}}
{{- if (((.Values.global | default dict).workloadIdentity | default dict).fieldcryptClientId) }}
azure.workload.identity/use: "true"
{{- end }}
{{- end -}}

{{- define "passage.wiServiceAccount" -}}
{{- if ((((.root.Values.global | default dict).workloadIdentity | default dict).fieldcryptClientId)) }}
serviceAccountName: {{ .name }}
{{- end }}
{{- end -}}
