{{- define "budgeting.image" -}}
{{- if .Values.image.digest -}}
{{ .Values.image.repository }}@{{ .Values.image.digest }}
{{- else -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}

{{- define "budgeting.serviceEnv" -}}
- name: PASSAGE_SERVICE_NAME
  value: budgeting
{{- if .Values.ledgerUrl }}
- name: LEDGER_URL
  value: {{ .Values.ledgerUrl | quote }}
{{- end }}
{{- if .Values.serviceIdentity.publicKeys }}
- name: PASSAGE_SERVICE_PUBLIC_KEYS
  value: {{ .Values.serviceIdentity.publicKeys | quote }}
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

{{- define "budgeting.identityVolume" -}}
{{- if .Values.serviceIdentity.signingKeySecret }}
volumes:
  - name: service-identity
    secret:
      secretName: {{ .Values.serviceIdentity.signingKeySecret }}
      defaultMode: 0440
{{- end }}
{{- end -}}

{{- define "budgeting.identityMount" -}}
{{- if .Values.serviceIdentity.signingKeySecret }}
volumeMounts:
  - { name: service-identity, mountPath: /var/run/passage/service-identity, readOnly: true }
{{- end }}
{{- end -}}
