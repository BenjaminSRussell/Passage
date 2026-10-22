{{- define "hello-ledger.image" -}}
{{- if .Values.imageDigest -}}
{{- printf "%s@%s" .Values.imageRepository .Values.imageDigest -}}
{{- else if .Values.allowMutableTag -}}
{{- if not .Values.imageTag -}}
{{- fail "imageTag is required when allowMutableTag is true" -}}
{{- end -}}
{{- printf "%s:%s" .Values.imageRepository .Values.imageTag -}}
{{- else -}}
{{- fail "imageDigest is required unless allowMutableTag is true" -}}
{{- end -}}
{{- end -}}
