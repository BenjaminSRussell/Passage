{{- define "receiving.image" -}}
{{- if .Values.image.digest -}}
{{ .Values.image.repository }}@{{ .Values.image.digest }}
{{- else -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}

{{- define "receiving.env" -}}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ .Values.databaseSecret }}
      key: DATABASE_URL
- name: AUDIT_SAMPLE_SEED
  valueFrom:
    secretKeyRef:
      name: {{ .Values.auditSeedSecret }}
      key: AUDIT_SAMPLE_SEED
- name: PASSAGE_SERVICE_NAME
  value: receiving
{{- if .Values.serviceIdentity.signingKeySecret }}
- name: PASSAGE_SERVICE_SIGNING_KEY_FILE
  value: /var/run/passage/service-identity/signing-key
{{- if .Values.serviceIdentity.keyId }}
- name: PASSAGE_SERVICE_KEY_ID
  value: {{ .Values.serviceIdentity.keyId | quote }}
{{- end }}
{{- end }}
{{- if .Values.fieldCrypt.keysSecret }}
- { name: FIELDCRYPT_KEYS_FILE, value: /var/run/passage/field-crypt/keys }
- { name: FIELDCRYPT_CURRENT_KEY_ID, value: {{ .Values.fieldCrypt.currentKeyId | quote }} }
{{- end }}
- name: MIGRATE_ON_START
  value: {{ .Values.migrateOnStart | quote }}
- name: LEDGER_URL
  value: {{ .Values.serviceUrls.ledger | default (printf "http://ledger.%s.svc:8080" .Release.Namespace) | quote }}
- name: CLOSE_URL
  value: {{ .Values.serviceUrls.close | default (printf "http://close.%s.svc:8080" .Release.Namespace) | quote }}
- name: PURCHASING_URL
  value: {{ .Values.serviceUrls.purchasing | default (printf "http://purchasing-purchasing.%s.svc" .Release.Namespace) | quote }}
- name: INVENTORY_URL
  value: {{ .Values.serviceUrls.inventory | default (printf "http://inventory.%s.svc:8080" .Release.Namespace) | quote }}
- name: PARTY_URL
  value: {{ .Values.serviceUrls.party | default (printf "http://party.%s.svc:8080" .Release.Namespace) | quote }}
- name: TAX_URL
  value: {{ .Values.serviceUrls.tax | quote }}
- name: COMPLIANCE_URL
  value: {{ .Values.serviceUrls.compliance | default (printf "http://compliance-compliance.%s.svc:8080" .Release.Namespace) | quote }}
- name: LEGAL_ENTITY_COUNTRIES
  value: {{ .Values.legalEntityCountries | quote }}
- name: POSTING_LEDGER_ID
  value: {{ .Values.postingProfile.ledgerId | quote }}
- name: POSTING_ACCOUNT_INVENTORY
  value: {{ .Values.postingProfile.inventoryAccount | quote }}
- name: POSTING_ACCOUNT_GRIR
  value: {{ .Values.postingProfile.grirAccount | quote }}
- name: POSTING_ACCOUNT_PPV
  value: {{ .Values.postingProfile.ppvAccount | quote }}
- name: POSTING_ACCOUNT_VENDOR_CONTROL
  value: {{ .Values.postingProfile.vendorControlAccount | quote }}
- name: POSTING_ACCOUNT_EXPENSE
  value: {{ .Values.postingProfile.expenseAccount | quote }}
- name: POSTING_ACCOUNT_INPUT_TAX
  value: {{ .Values.postingProfile.inputTaxAccount | quote }}
{{- with .Values.tax }}
{{- if .code }}
- name: TAX_CODE
  value: {{ .code | quote }}
{{- end }}
{{- if .ratePct }}
- name: TAX_RATE_PCT
  value: {{ .ratePct | quote }}
{{- end }}
{{- if .jurisdiction }}
- name: TAX_JURISDICTION
  value: {{ .jurisdiction | quote }}
{{- end }}
{{- end }}
{{- end }}
