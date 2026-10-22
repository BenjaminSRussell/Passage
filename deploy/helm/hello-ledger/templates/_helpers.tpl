{{- define "hello-ledger.image" -}}
{{- if .Values.imageDigest -}}
{{- printf "%s@%s" .Values.imageRepository .Values.imageDigest -}}
{{- else -}}
{{- printf "%s:%s" .Values.imageRepository .Values.imageTag -}}
{{- end -}}
{{- end -}}
