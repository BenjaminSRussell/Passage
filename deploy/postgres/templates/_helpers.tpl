{{- define "outbox-postgres.secretName" -}}
{{ default (printf "%s-postgres" .Release.Name) .Values.secretName }}
{{- end }}
