# Eventing Backbone (outbox, Debezium, Kafka, consumers, sagas)

## Purpose & Boundaries
Cross-context async plumbing: transactional outbox per database -> Debezium (Strimzi KafkaConnect) -> Kafka (Strimzi) topics `passage.<context>.<entity>` with CloudEvents 1.0 structured JSON, Apicurio schema registry (Q9), consumer inbox/DLQ, Temporal sagas S3b/S4, webhooks via integration. Everything cross-context except `ledger.Post` is an event consumed idempotently. 62 specified event types (42 principal + 5 proposed [P] + 15 added), 51 event topics, 8 snapshot topics, 126 consumer ids.

## Interfaces
- Envelope (required): `specversion`="1.0", `id` (UUIDv7 = outbox.id), `source` (`/passage/<context>/<service>`), `type` (`dev.passage.<context>.<entity>.<verb>.v<n>`, lowercase, past-tense verb), `subject` (aggregate uuid), `time`, `datacontenttype`="application/json", `dataschema` (`https://schemas.passage.dev/<type>.json`), `tenantid`, `correlationid`, `causationid`, `traceparent`, `actor`, `sequence` (>=1, aggregate version), `data`; optional `legalentityid`. Actor pattern `user:<sub>` | `agent:<id>@user:<sub>` | `system:<job>`. Envelope schema `https://schemas.passage.dev/envelope.v1.json`. `eventTime` never used for ordering — `sequence` is.
- Topics: partitions 12 default, 48 for `passage.ledger.journalentry`, `passage.inventory.transaction`, `passage.manufacturing.machineevent`; key `tenantid:subject` (machineevent `tenantid:resource_id`); RF 3, `min.insync.replicas=2`, `unclean.leader.election.enable=false`; retention 30 d (machineevent 7 d, DLQ 30 d); `.snapshot` compacted topics (platform.entitydefinition, platform.legalentity, ledger.periodcontrol, party.party, party.customer, product.product, ledger.exchangerate, platform.numbersequence); DLQ `passage.<context>.<entity>.dlq.<consumer>`; `max.message.bytes=524288`; payload cap 256 KiB. Kafka record header `ce_type`.
- Consumer: group `passage-<consumer-id>`, `enable.auto.commit=false`, CooperativeStickyAssignor, KafkaUser `svc-<consumer-id>` SCRAM-SHA-512 + mTLS. Retry 5 attempts, backoff 1 s,4 s,16 s,60 s ±20% in-process on the same partition. Non-retryable (undeserializable, schema fail, unknown major type version, validation_failed/forbidden_tenant) straight to DLQ. Park up to 60 s for gap-sensitive (G) consumers. Stale (`sequence` <= stored) acknowledged and dropped. DLQ headers: dlq-error-class, dlq-error-message, dlq-attempts, dlq-first-failed-at, dlq-consumer, dlq-origin-topic/partition/offset, traceparent. Alerts DLQ depth >= 100 warn, >= 1,000 page; fiscal consumers (`ledger-*`, `*-saga-bridge`, `inventory-receipts|shipments|raf`) depth > 0 for 15 min page.
- Debezium outbox connector per DB: `PostgresConnector`, pgoutput, `table.include.list=<schema>.outbox`, `EventRouter` (event.id=id, event.key=partition_key, payload=payload, route.by.field=aggregate_type, `route.topic.replacement=passage.<context>.${routedByValue}`), `CloudEventsConverter`; producer `acks=all`, idempotence on. Second connector for table CDC (excludes outbox, `*_secure`, consumer_inbox, idempotency_key).
- Sagas (Temporal TS, shared namespace OQ-7): S3b `production-settlement:<tenantid>:<productionOrderId>` (task queue `saga-production-settlement`), S4 `ic-mirror:<tenantid>:<salesOrderId>` (queue `saga-intercompany-mirror`); bridges use `signalWithStart` with deterministic ids + eventId set. S1, S2, S3a are valuation-owner postings, NOT workflows. Activity key `<saga>:<step>:<aggregate id>`; principal `system:<saga>`. Visibility attrs tenantId, legalEntityId, sagaType, aggregateId, status; stuck > 15 min alerts.
- Schema evolution: additive-only within version; registry compat `BACKWARD_TRANSITIVE`; dual publication of v<n> and v<n+1> for two minor releases; consumer subscribes to exactly one version; consumer with dropped type fails startup readiness; unknown enum -> `other` + warn.

## Data Ownership
Per-DB `outbox`, `consumer_inbox` (30 d prune), replication slots (one per connector; `max_slot_wal_keep_size`, lag alerts 1 GiB/5 GiB; PG17 failover slots). DLQ triage group `passage-dlq-triage-<consumer-id>`.

## Events Produced
All 62 types; see component sheets. Principal list by context: platform(5), party(3), product(3), ledger(9 incl. [P] intercompanypair.matched/mismatched + reconciliation.completed), inventory(7), procurement(6), manufacturing(7), sales(8), warehouse(2), treasury(2), workforce(2), projects(3), agents(5).

## Events Consumed
See `integration`, `inventory`, etc. Registry: 126 consumer ids; `{...}-numbersequence` row stands for nine. External consumers read tenant egress topics via filtering relay (naming conflict: backend `passage.egress.<tenantSlug>.<context>` vs eventing `passage.ext.<tenantslug>.<context>.<entity>`).

## Dependencies
internal: platform-kernel, integration (egress relay, webhooks), platform (workflow tasks). external: Kafka 4.x KRaft (Strimzi), Debezium 3.x, Apicurio, Temporal, Postgres 17 logical replication.

## Gotchas & Constraints
- Outbox rows deleted by kernel job when LSN <= `confirmed_flush_lsn` and age > 1 h (Debezium never deletes); duplicates harmless.
- Handler never performs irreversible external side effects before DB commit; business rejections with defined outcome (inventory_insufficient) emit a rejection event, not an exception; unexpected exceptions rethrown after rollback; empty catch / ignored err banned by lint.
- Document-creating consumers use deterministic `Idempotency-Key = <consumer-id>:<event id>`.
- Journals > ~1,500 lines carry header + `linesUrl`; first 200 lines inline otherwise.
- No P3 in events; P2 only where needed; payrollrun.posted aggregates only.
- Replay: ticket, pause (`passage-ctl consumer pause`), snapshot inbox, offset replay / DLQ replay [P] / snapshot rebuild, resume, verify with I.6; fiscal consumers need controller approval.
- Per-tenant ACLs on shared topics impossible -> egress topics (needs ADR amendment); GDPR tombstones on snapshot topics [P].
- Debezium CloudEvents assembly needs `passage-cloudevents-enricher` SMT (spike wave 4).
- `ledger-intercompany` pair match 48 h timeout; S4 timers: mirror PO 5 days.

## Source Sections
- Events (3705-4706): 0 (3713-3730), 2 (3852-3965), 3 (3969-4086), 4 (4090-4174), 5 (4178-4247), 6 (4251-4324), 7 (4328-4416), 8 (4420-4428), 9 (4432-4645), 10-12 (4649-4706); Backend 7.6 (1189-1197); Open Questions 2,8,37 (7780, 7786, 7816)
