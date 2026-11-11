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
