{{- define "production.image" -}}
{{- if .Values.image.digest -}}
{{ (include "passage.imageRepository" $) }}@{{ .Values.image.digest }}
{{- else -}}
{{ (include "passage.imageRepository" $) }}:{{ .Values.image.tag }}
{{- end -}}
{{- end -}}
