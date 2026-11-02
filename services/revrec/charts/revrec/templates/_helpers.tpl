{{- define "revrec.ledgerIds" -}}
{{- $pairs := list -}}
{{- range $le, $ledger := .Values.ledgerIds -}}
{{- $pairs = append $pairs (printf "%s=%s" $le $ledger) -}}
{{- end -}}
{{- join "," $pairs -}}
{{- end -}}
