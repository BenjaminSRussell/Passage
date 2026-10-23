# Not implemented in PSG-4 slice 1

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: Strimzi Kafka | manifests only | `deploy/kafka/kafka.yaml` is written from the architecture literals and not applied to a cluster; no Argo CD Application points at `deploy/kafka/` |
| Deliverable 1: CloudEvents envelope | stock converter | `deploy/kafka/connect.yaml` uses `EventRouter` plus `CloudEventsConverter`; the `passage-cloudevents-enricher` SMT that adds `tenantid`, `sequence` and `traceparent` is a wave 4 spike and is not built |
| Deliverable 1: Apicurio schema registry | slice 2 of PSG-4 | line budget; part of AC4 |
| Deliverable 1: AsyncAPI generated from entity metadata | slice 2 of PSG-4 | needs the PSG-3 metadata registry, which is not in the repo |
| AC4: registry rejects a breaking schema change in CI | slice 2 of PSG-4 | needs the registry manifest and the compatibility checker |
| Deliverable 2: transactional outbox | built | `internal/eventing` relay with ack-after-publish and `PgInbox.Once` dedupe; `migrations/0001_eventing.sql` carries `outbox` and `consumer_inbox` |
| Deliverable 2: relay `Publisher` for Kafka | not built | production delivery is Debezium; no Kafka client dependency is approved for Go |
| Deliverable 2: outbox row cleanup by LSN and `consumer_inbox` 30 day prune | not built | needs the batch job framework (slice 3) |
| Outbox and relay counters on an admin port | slice 2 of PSG-4 | this slice has no binary |
| Deliverable 3: Temporal cluster, SDKs, approval template (AC2) | slice 2 of PSG-4 | new TypeScript deployable in `services/workflow-ts/` and `deploy/temporal/` |
| Deliverable 4: batch job framework and job monitor UI (AC3) | slice 3 of PSG-4 | checkpoint tables, partitioning and UI do not fit this slice |
| Deliverable 5: workflow designer | not built | depends on the workflow service in slice 2 |
| Deliverable 6: notify service | not built | depends on workflow and queue work in later slices |
| Deliverable 7: message templates and notification preferences | not built | depends on the notify service |
| Web module `apps/web/src/modules/workflow/` | not built | depends on the workflow service API |
| Argo CD wiring for `deploy/kafka/` | handoff | `deploy/envs/dev/apps/` is outside wave 4 ownership |
| Outbox `partition_key` and `headers` written by `outbox.Enqueue` | handoff | `platform/` is not editable; `partition_key` is a generated column here and `headers` stays null until the kernel owner writes it |
