{{- define "costacct.image" -}}
{{- if .Values.image.digest -}}
{{ (include "passage.imageRepository" $) }}@{{ .Values.image.digest }}
{{- else -}}
{{ (include "passage.imageRepository" $) }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}

{{- define "costacct.ledgerIds" -}}
{{- $pairs := list -}}
{{- range $le, $ledger := .Values.ledgerIds -}}
{{- $pairs = append $pairs (printf "%s=%s" $le $ledger) -}}
{{- end -}}
{{- join "," $pairs -}}
{{- end -}}

{{- define "costacct.ledgerEnv" -}}
- name: PASSAGE_SERVICE_NAME
  value: costacct
{{- if .Values.ledgerUrl }}
- name: LEDGER_URL
  value: {{ .Values.ledgerUrl | quote }}
- name: COSTACCT_LEDGER_IDS
  value: {{ include "costacct.ledgerIds" . | quote }}
{{- end }}
{{- if .Values.serviceIdentity.signingKeySecret }}
- name: PASSAGE_SERVICE_SIGNING_KEY_FILE
  value: /var/run/passage/service-identity/signing-key
{{- if .Values.serviceIdentity.keyId }}
- name: PASSAGE_SERVICE_KEY_ID
  value: {{ .Values.serviceIdentity.keyId | quote }}
{{- end }}
{{- end }}
{{- end -}}

{{- define "costacct.identityVolume" -}}
{{- if .Values.serviceIdentity.signingKeySecret }}
volumes:
  - name: service-identity
    secret:
      secretName: {{ .Values.serviceIdentity.signingKeySecret }}
      defaultMode: 0440
{{- end }}
{{- end -}}

{{- define "costacct.identityMount" -}}
{{- if .Values.serviceIdentity.signingKeySecret }}
volumeMounts:
  - { name: service-identity, mountPath: /var/run/passage/service-identity, readOnly: true }
{{- end }}
{{- end -}}
