# Not implemented in PSG-4 Wave 04

Delivered: transactional outbox + inbox relay (AC1), Temporal approval workflow with time-skipping SLA/delegation test (AC2), partitioned checkpointed batch runner with 1M-row resume (AC3), schema compatibility gate in CI and on `POST /v1/events/schemas/{type}` (AC4), Go HTTP API, notify templates/queue/preferences, workflow designer + job monitor web module, Strimzi/Debezium/Apicurio/Temporal manifests, OPA duties.

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: Strimzi Kafka | manifests only | `deploy/kafka/kafka.yaml` is written from the architecture literals and not applied to a cluster; no Argo CD Application points at `deploy/kafka/` |
| Deliverable 1: CloudEvents envelope | stock converter | `deploy/kafka/connect.yaml` uses `EventRouter` plus `CloudEventsConverter`; the `passage-cloudevents-enricher` SMT that adds `tenantid`, `sequence` and `traceparent` is a wave 4 spike and is not built |
| Deliverable 1: Apicurio schema registry | configured, not applied | `deploy/kafka/registry.yaml` configures SQL storage and `BACKWARD_TRANSITIVE`; Wave 01 owns dev Argo CD wiring |
| Deliverable 1: AsyncAPI generated from entity metadata | contract + catalog API | `events/asyncapi.yaml` and `GET /v1/events/asyncapi/{domain}` exist; generation from the metadata registry is a Wave 03 adapter |
| Deliverable 2: relay `Publisher` for Kafka | not built | production delivery is Debezium; no Kafka client dependency is approved for Go |
| Deliverable 2: outbox LSN sweep / inbox 30-day prune | handler reserved | `outbox.sweep` is a registered batch handler; the LSN join needs Debezium slot stats in the cluster |
| Deliverable 3: Temporal cluster applied | manifests only | `deploy/temporal/values.yaml` and the worker chart exist; Wave 01 owns Argo CD wiring |
| Deliverable 6: live SMTP/HTTP provider | in-process queue | adapters, bounce ingest and distributor ticker are domain-complete; sending to a real relay waits on ESO secrets |
| Wave 08 `product.explode` / `product.group-change` | handoff | product still runs those jobs in-process; Wave 08 owns `services/product/` so this wave only reserved the handler codes |
| Wave 03 `dev.passage.metadata.entity.published.v1` | backbone ready | topic, CloudEvents type and schema gate exist; metadata's publish and extend paths write the outbox in the same transaction; the connector, ACL and route config for the metadata database are in `deploy/kafka/connect.yaml` (#190); the database, CDC role (`filtered` publication autocreate needs CREATE on the database and ownership of `outbox`, else pre-create the publication) and Kafka wiring they need are tracked in #223 |
| Wave 03 nightly usage job | handler reserved | `metadata.usage` is a registered batch code; metadata still calls the collector in-process |
| Argo CD wiring for `deploy/kafka/` and `deploy/temporal/` | handoff | `deploy/envs/dev/apps/` is outside wave 4 ownership |
| Outbox `headers` written by `outbox.Enqueue` | handoff | `platform/` is not editable |
