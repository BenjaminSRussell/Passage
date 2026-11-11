{{- define "agent-runtime.image" -}}
{{- if .Values.image.digest -}}
{{ (include "passage.imageRepository" $) }}@{{ .Values.image.digest }}
{{- else -}}
{{ (include "passage.imageRepository" $) }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}
