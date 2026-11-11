{{/*
Cloud profile helpers (#599). Identical in every service chart; the values come from
deploy/cloud-profiles/values/<profile>.yaml (global.*).

passage.imageRepository: the image repository with the registry mirror applied. global.imageRegistry replaces the
registry host and organisation (ghcr.io/benjaminsrussell/<name> becomes <registry>/<name>). A profile that pulls only
from a mirror (global.imageRegistryRequired) fails the render when none is set.
*/}}
{{- define "passage.imageRepository" -}}
{{- $g := .Values.global | default dict -}}
{{- if $g.imageRegistry -}}
{{- printf "%s/%s" (trimSuffix "/" $g.imageRegistry) (regexReplaceAll "^[^/]+/[^/]+/" .Values.image.repository "") -}}
{{- else -}}
{{- if $g.imageRegistryRequired -}}{{- fail "global.imageRegistry is required: this cloud profile pulls images only from a registry mirror (docs/ops/cloud-profiles.md)" -}}{{- end -}}
{{- .Values.image.repository -}}
{{- end -}}
{{- end -}}

{{/* passage.cloudEnv: the PASSAGE_CLOUD_PROFILE env entry (profile plus overlays, "gcc+hipaa") a Go service reads at start. */}}
{{- define "passage.cloudEnv" -}}
{{- $g := .Values.global | default dict -}}
{{- with $g.cloudProfile }}
- name: PASSAGE_CLOUD_PROFILE
  value: {{ concat (list .) ($g.cloudOverlays | default list) | join "+" | quote }}
{{- end }}
{{- /* global.fieldcryptKeyId: the Key Vault key that wraps FIELDCRYPT_KEYS entries written id=wrapped:... (#600). */}}
{{- with $g.fieldcryptKeyId }}
- name: FIELDCRYPT_AZURE_KEY_ID
  value: {{ . | quote }}
{{- end }}
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
