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
