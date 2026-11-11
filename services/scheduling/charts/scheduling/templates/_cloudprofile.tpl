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
{{- end -}}
