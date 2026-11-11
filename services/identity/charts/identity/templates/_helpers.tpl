{{- define "identity.image" -}}
{{- if .Values.image.digest -}}
{{ (include "passage.imageRepository" $) }}@{{ .Values.image.digest }}
{{- else -}}
{{ (include "passage.imageRepository" $) }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}

{{- define "identity.env" -}}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ .Values.databaseSecret }}
      key: DATABASE_URL
{{- if .Values.migrateDatabaseSecret }}
- name: MIGRATE_DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ .Values.migrateDatabaseSecret }}
      key: DATABASE_URL
{{- end }}
- name: IDENTITY_DUTY_CATALOGUE_PATH
  value: /etc/passage/authz/data.json
- name: MIGRATE_ON_START
  value: {{ .Values.migrateOnStart | quote }}
{{- if .Values.operatorTenantId }}
- name: IDENTITY_OPERATOR_TENANT_ID
  value: {{ .Values.operatorTenantId | quote }}
{{- end }}
{{- if .Values.scim.issuerBase }}
- name: IDENTITY_SCIM_ISSUER_BASE
  value: {{ .Values.scim.issuerBase | quote }}
- name: IDENTITY_SCIM_AUDIENCE
  value: {{ .Values.scim.audience | quote }}
- name: IDENTITY_OPA_URL
  value: {{ .Values.scim.opaUrl | quote }}
{{- end }}
{{- if .Values.keycloakDeprovision.enabled }}
- name: IDENTITY_KEYCLOAK_ADMIN_URL
  value: {{ required "keycloakDeprovision.adminUrl is required" .Values.keycloakDeprovision.adminUrl | quote }}
- name: IDENTITY_KEYCLOAK_CLIENT_ID
  value: {{ .Values.keycloakDeprovision.clientId | quote }}
- name: IDENTITY_KEYCLOAK_SECRETS_DIR
  value: /var/run/passage/keycloak
- name: IDENTITY_KEYCLOAK_DEPROVISION_INTERVAL
  value: {{ .Values.keycloakDeprovision.interval | quote }}
{{- end }}
{{- if .Values.bundle.enabled }}
- name: IDENTITY_BUNDLE_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ .Values.bundle.secret }}
      key: token
- name: IDENTITY_BUNDLE_KEY_ID
  value: {{ .Values.bundle.keyId | quote }}
- name: IDENTITY_BUNDLE_ADDR
  value: ":{{ .Values.bundle.port }}"
- name: IDENTITY_BUNDLE_SIGNING_KEY_PATH
  value: /var/run/passage/bundle/signing-key.pem
- name: IDENTITY_BUNDLE_TLS_CERT_PATH
  value: /var/run/passage/bundle/tls.crt
- name: IDENTITY_BUNDLE_TLS_KEY_PATH
  value: /var/run/passage/bundle/tls.key
{{- end }}
{{- end }}
