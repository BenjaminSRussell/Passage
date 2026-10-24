# Not implemented in PSG-5 slice 1

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 4: live Debezium/Kafka CDC consumer | interface only | `internal/cdc.Consumer` is the wave-4 seam; HTTP ingest is the adapter this slice ships. `deploy/kafka` is owned by wave 4 |
| Deliverable 7: S3 locations, malware scan, previews, attachment text search, retention jobs | domain only | `domain.BlobStore` / `Scanner` have in-package fakes; object storage and ClamAV/Tideways are a follow-up |
| Deliverable 8: email/notify delivery | partial | Change and due-date matching plus an in-app inbox are built; the notify service does not exist yet |
| Deliverable 9: persisted multilingual text library and document-layout references | partial | Placeholder expansion is built; language versions and layout binding need wave-3 metadata |
| Tempo live UI-click drill-down in a cluster | model + dashboards | `deploy/observability` ships Grafana/Tempo/Loki/Prometheus manifests and the shell emits W3C `traceparent`. A running Tempo stack is a wave-1 deploy handoff |
| Argo CD Application, Secret `audit-db`, database `passage_audit` | handoff | outside wave 5 service-chart ownership |
