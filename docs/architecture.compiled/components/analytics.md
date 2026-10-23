# Analytics (supporting context + lakehouse)

## Purpose & Boundaries
Context `analytics` (TS only: `analytics-ts`; database `passage_analytics`, operational tables only; wave 46). Semantic-layer API facade (Cube), dashboards, document designer, catalog/lineage. Lakehouse: Debezium table CDC -> Kafka -> Apache Iceberg (S3-compatible) -> Trino / ClickHouse; dbt; Cube semantic layer; OpenMetadata tags. Backend only exposes the `analytics` facade.

## Interfaces
`POST /v1/analytics/query` (governed metric query proxied to Cube; tenant row filter, P3 exclusion; User:`analytics.query`; limits <= 10 measures, <= 8 dimensions, 50k rows), `GET /metrics`, `GET|POST /dashboards`, `GET /catalog` (catalog + lineage), `POST /document-templates/{id}:render` (fiscal docs stored with object lock), `POST /document-templates`.

## Data Ownership
Operational tables (dashboards, templates, catalog). Iceberg: one table per source CDC table partitioned by tenant_id bucket + date; append-only for journal_entry, journal_line, inventory_transaction, tax_transaction, production_order_event, machine_event (1-min rollups; raw opt-in), audit; merge-on-read for M/ML/F tables. Excluded: `*_secure`, outbox, consumer_inbox, idempotency_key, number_sequence_counter, dimension_set. Masked: P2 via column tags/masked views; erasure propagates by party_id.

## Events Produced
absent — the source document does not address this

## Events Consumed
`analytics-lakehouse` (all event topics via cross-tenant principal -> event-fact/archive Iceberg tables); Iceberg sink consumes CDC topics.

## Dependencies
internal: eventing-backbone (CDC), platform (OPA grants -> Cube queryRewrite RLS), agents (ask-the-data consumer). external: Iceberg, Trino/ClickHouse (choice deferred to wave 47), Cube, dbt, OpenMetadata.

## Gotchas & Constraints
- Lag budget: commit -> Iceberg visible p95 < 60 s (NFR-P10). Replication slot lag alert 1 GiB, page 5 GiB; `max_slot_wal_keep_size` set; recovery = re-snapshot.
- Cube metrics checked against GL (REC-19 semantic layer, tolerance 0.00, wave 46); metrics carry reconciliation status TRUSTED/BLOCKED; empty `NO_SCOPE` result means no access.
- Dashboards enforce RLS; statements beyond ~1M balance rows served from lakehouse.
- Cube semantic RLS via `queryRewrite` from OPA (FR-46.7).

## Source Sections
- Backend 5.18 (953-963), 3.2 (298), 4 Analytics/GraphQL (364-365); Data 11 (3317-3352); Events 10 (4649-4655); Frontend analytics module (4834); AI 13 wave-46 hook (7660)
