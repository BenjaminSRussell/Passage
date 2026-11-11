{{- define "workflow.image" -}}
{{- if .Values.image.digest -}}
{{ printf "%s@%s" (include "passage.imageRepository" $) .Values.image.digest }}
{{- else -}}
{{ printf "%s:%s" (include "passage.imageRepository" $) (default .Chart.AppVersion .Values.image.tag) }}
{{- end -}}
{{- end -}}
