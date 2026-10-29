{{/* "true" when this region's Postgres is fenced: role FENCED, or a game-day fence. */}}
{{- define "passage-mr.fenced" -}}
{{- if or (eq .Values.region.role "FENCED") .Values.fence.enabled -}}true{{- end -}}
{{- end -}}

{{/* "true" when MirrorMaker 2 runs in this region: it runs next to its target cluster. */}}
{{- define "passage-mr.mm2Here" -}}
{{- if and .Values.mm2.enabled (eq .Values.kafka.localAlias .Values.mm2.targetAlias) -}}true{{- end -}}
{{- end -}}

{{/* synchronous_standby_names form of postgres.synchronous, e.g. "ANY 1". */}}
{{- define "passage-mr.syncQuorum" -}}
{{ upper .Values.postgres.synchronous.method }} {{ .Values.postgres.synchronous.number }}
{{- end -}}

{{- define "passage-mr.labels" -}}
app.kubernetes.io/part-of: passage
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
passage.dev/region: {{ .Values.region.id }}
passage.dev/region-role: {{ .Values.region.role }}
{{- end -}}
