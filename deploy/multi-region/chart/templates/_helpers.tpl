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

{{- define "passage-mr.backupStore" -}}{{ .Values.postgres.clusterName }}-backup{{- end -}}

{{/* "true" when this region's cluster archives WAL and takes base backups. */}}
{{- define "passage-mr.backsUp" -}}
{{- if and .Values.backup.enabled (ne .Values.region.role "PASSIVE") -}}true{{- end -}}
{{- end -}}

{{- define "passage-mr.labels" -}}
app.kubernetes.io/part-of: passage
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
passage.dev/region: {{ .Values.region.id }}
passage.dev/region-role: {{ .Values.region.role }}
{{- end -}}

{{/* "true" when backups go to Azure Blob storage. */}}
{{- define "passage-mr.azureBlob" -}}
{{- if eq .Values.backup.store "azure-blob" -}}true{{- end -}}
{{- end -}}

{{/* "true" when this region archives WAL to Azure Blob as a workload identity (no storage key). */}}
{{- define "passage-mr.azureWorkloadIdentity" -}}
{{- if and (include "passage-mr.backsUp" .) (include "passage-mr.azureBlob" .) .Values.backup.azure.useWorkloadIdentity -}}true{{- end -}}
{{- end -}}

{{/* Azure Blob destination: https://<account>.<blob suffix of the cloud profile>/<container>/ */}}
{{- define "passage-mr.azureDestination" -}}
{{- $blob := required "global.endpoints.blobSuffix is required for backup.store azure-blob: render with deploy/cloud-profiles/values/<profile>.yaml" ((.Values.global | default dict).endpoints | default dict).blobSuffix -}}
https://{{ required "backup.azure.storageAccount is required for backup.store azure-blob" .Values.backup.azure.storageAccount }}.{{ $blob }}/{{ .Values.backup.azure.container }}/
{{- end -}}
