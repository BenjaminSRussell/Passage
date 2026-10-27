# NOT-IMPLEMENTED: analytics (PSG-46, wave 46, slice 1)

Every ticket deliverable and acceptance criterion that slice 1 does not build,
with its status and reason. Status `later slice of PSG-46` means the work is
owned by this wave and planned after slice 1. `handoff` means it needs a
directory or owner outside this wave's owned paths (`data/`,
`services/analytics/`, `apps/web/src/modules/analytics/`).

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | CDC Debezium to Kafka to Iceberg; Trino/ClickHouse query layer; dbt models | later slice of PSG-46 | Slice 1 has the connector template (`data/cdc/`, topics `cdc.<domain>.<table>`) and the ledger dbt models: `core` current state of `journal_line`, `journal_entry`, `main_account` and `ledger`, and `mart.fin_journal_line`, written against the real columns in `services/ledger/migrations`. The Iceberg sink, Trino and ClickHouse need their own deployable. Kafka and Debezium runtime deployment belongs to deployment-cicd and is a handoff. The dbt project has not been compiled or run here: there is no Trino or dbt in this repository's toolchain. |
| 2 | Semantic layer (Cube) with governed metrics and RLS mirrored from OPA | later slice of PSG-46 (metrics); handoff (OPA feed) | Built: the metric catalog (revenue, cogs, gross margin, DSO/DPO, inventory turns, OTIF, OEE), the Cube model for the finance measures, and the scope mirror in `cube.js`. Not built: Cube measures for DSO, DPO, inventory turns, OTIF and OEE (their marts come later). The OPA data-scope decision is a handoff to edge-identity and platform (see the OPA row below). |
| 3 | Embedded dashboards in every workspace, self-service explorer, scheduled reports, export to Excel, Power BI, Tableau | later slice of PSG-46; handoff (shell embedding) | Built: web screens for the metric list and definition, an explorer that builds and shows the scoped Cube plan, and the reconciliation results with the failure banner. Not built: running the plan for rows, saved views, pivots, dashboards and tiles, schedules and exports (`POST /analytics/exports`, `POST /analytics/schedules`, `PUT /analytics/dashboards/{id}`) are not built. Embedding into every workspace needs the web shell (wave 5), which is a handoff. |
| 4 | Operational report designer (SSRS replacement) for invoices, POs, packing slips, checks | handoff | Owned paths contain no docgen directory. Needs a document service owner and a path grant. |
| 5 | Data catalog and lineage (OpenMetadata) | later slice of PSG-46 | Not started. Ingestion and `GET /catalog/lineage/{asset_fqn}` will live in this service. The OpenMetadata deployment is a handoff to deployment-cicd. |
| 6 | Print management routing (printer, e-mail, e-invoice, archive, portal) | handoff | `print_rule`, the print agent and `document_render` belong to the document service. No owned path exists for them. |
| 7 | Text blocks, form notes, correspondence types (statements, notices, confirmations) | handoff (designer and text blocks); later slice of PSG-46 (statement scheduling) | Text blocks and correspondence depend on the document renderer (row 4) and print routing (row 6). |
| 8 | Data-quality rules on catalog assets with scorecard tiles | later slice of PSG-46 | Not started. The `dq_rule` and `dq_score` tables and the hourly evaluation job are later work. |
| 9 | Process event log (`mart.process_event`), variant explorer, OCEL 2.0 and XES export | later slice of PSG-46 | Not started. The case-stitching dbt model and the export endpoint are later work. |
| 10 | Archive tier: closed OLTP partitions to Iceberg, hash verification, restore | later slice of PSG-46 | Not started. `archive_partition` and the Temporal mover are later work. |

## Functional requirements (PRD 9.46.3)

| FR | Status | Reason |
|---|---|---|
| FR-46.1 CDC from every domain | partial: connector template only | One connector per domain is not deployed. The `BACKWARD_TRANSITIVE` compatibility is a schema-registry subject setting, not a connector setting, and is not applied. The `REPLICA IDENTITY FULL` migration check, the `snapshot.mode=never` switch after the first run and the slot-lag alert are later work. The `analytics: true` entity-metadata flag that selects tables does not exist yet (wave 3 handoff), so `TABLE_INCLUDE_LIST` is set by hand. |
| FR-46.2 Iceberg landing contract | later slice of PSG-46 | Sink not built. Partition spec, commit cadence and compaction are later work. |
| FR-46.3 Schema evolution | partial: classifier and pipeline state machine (`internal/domain/cdc.go`) | The sink that applies the verdicts, the DLQ `cdc.dlq.<domain>`, and the `cdc_pipeline_state` table are later work. |
| FR-46.4 Late data and exactly-once effect | partial: dbt side (`data/dbt/macros/latest_state.sql`) | Built: per key the highest `_source_lsn` across all raw rows wins, so redelivered or late changes cannot overwrite newer state; incremental runs pick keys by `_ingested_at` so late rows are merged; deletes leave the current table. Not built: the sink coordinator (`iceberg.control`), the sink's `(tenant_id, pk, _source_lsn)` dedupe, the watermark and setting `late=true` (the sink's job; dbt only carries it). The property test "any permutation of events yields identical state" needs a Trino runtime and is not run. |
| FR-46.5 dbt layers | partial: 4 core models and `mart.fin_journal_line` with tests | Tests: key uniqueness and not-null, accepted values, the entry relationship, every entry nets to zero in the mart, and raw live keys equal core rows. Not built: SCD2 history (`valid_to`, BR-46.2 history), the sales, procurement, inventory, production, projects and HR marts, an incremental mart (the mart is rebuilt as a table each run), and the BLOCKED path from a failed dbt test. COGS accounts need rows in `seeds/account_category_map.csv` per tenant; the ledger has no COGS account type. |
| FR-46.6 Governed metrics | partial: 8 published definitions in code | `semantic_metric` table, versioning API and the registry UI are later work. Formulas are in code, not in the database. |
| FR-46.7 RLS mirrored from OPA | partial: scope mirror in planner and `cube.js` (filters every cube a query touches; tested with `node --test data/cube/`) | The OPA data-scope decision is not called. The legal-entity header is trusted as the scope. See the OPA row. The 5-minute decision cache and `pre-aggregations per security context` are later work. |
| FR-46.8 Embedded dashboards | later slice of PSG-46 | Dashboard definitions and tiles are not built. |
| FR-46.9 Explorer and export | later slice of PSG-46 | Only the planner exists. Export jobs (Temporal), signed URLs and the audit log are later work. |
| FR-46.10 Scheduled reports | later slice of PSG-46 | `report_schedule` and the dispatcher are not built. |
| FR-46.11 Document designer | handoff | See deliverable 4. |
| FR-46.12 Catalog and lineage | later slice of PSG-46 | See deliverable 5. |
| FR-46.13 Daily reconciliation | partial: `Compare`, trust counter, store, `GET /analytics/reconciliation` | The 02:00 job that computes semantic and ledger values is not built. Only the record and read paths exist. The `metric.reconciled` BLOCKED path is later work. |
| FR-46.14 Print routing | handoff | See deliverable 6. |
| FR-46.15 Text blocks and correspondence | handoff | See deliverable 7. |
| FR-46.16 DQ rules | later slice of PSG-46 | See deliverable 8. |
| FR-46.17 Process event log | later slice of PSG-46 | See deliverable 9. |
| FR-46.18 Archive tier | later slice of PSG-46 | See deliverable 10. |

## Acceptance criteria (PRD 9.46.8, ticket AC1-AC6)

| AC | Status | Reason |
|---|---|---|
| AC1 lakehouse freshness p95 < 60 s | later slice of PSG-46 | Needs the sink, dbt schedule and a load test (fixture `fx-lake-usmf-year`). |
| AC2 revenue equals GL daily | partial | The reconciliation comparison is tested in `internal/domain`. The end-to-end daily check needs the job (FR-46.13) and the mart feed. |
| AC3 RLS: a user sees only their entity | partial | Domain and API tests prove the planner filter, `data/cube/cube.test.js` proves the `queryRewrite` filter for two users and for multi-cube queries, and the store RLS test proves a legal-entity scoped caller reads only its entity's reconciliation rows. An end-to-end dashboard test needs a live Cube instance. |
| AC4 document visual regression | handoff | Depends on the document designer (deliverable 4). |
| AC5 every deliverable has code or a reason | done | This file. |
| AC6 vet/test/chart/OpenAPI/AsyncAPI | done for this slice | `go vet` and `go test` pass with and without the database. The web module passes vitest and tsc. The chart lints. `internal/api/openapi_test.go` checks OpenAPI against the mounted routes; AsyncAPI matches `MetricReconciledData`. |
| AC-46.1 to AC-46.16 (PRD) | later slice of PSG-46 or handoff | Each depends on a row above. |

## Other gaps

| Item | Status | Reason |
|---|---|---|
| OPA data-scope decision (`data.passage.authz.decision`) | handoff to edge-identity and platform | The service trusts the gateway's `X-Passage-Legal-Entity` header. The PRD requires the OPA decision and the 5-minute cache. Until then, a request without that header is treated as whole-tenant scope, so the gateway must always set it. |
| Cube execution | later slice of PSG-46 | `POST /analytics/query` returns the compiled plan, not rows. The Cube client and the 50k-row cap enforcement are later work. |
| Kernel endpoints `/metrics`, `/v1/analytics/openapi.yaml`, `/asyncapi.yaml`, `/error-codes` | handoff to platform kernel | The OTel metrics handler and the kernel error-code catalog are not in platform/go. `/healthz` and `/readyz` are implemented in `cmd/analytics`. |
| Idempotency and ETag | later slice of PSG-46 | Slice 1 has no creating or mutating endpoints. `Idempotency-Key`, `If-Match` and the `idempotency_keys` table arrive with the first write. The reconciliation store is idempotent by its key (tenant, legal entity, metric, day). |
| Metric owner (`owner_user_id`), tolerance per metric, `semantic_metric` table | later slice of PSG-46 | The catalog is code; metric owner and per-metric tolerance are not modelled. Journal-line metrics allow only the dimensions `mart.fin_journal_line` has (no `site_id`: ledger lines carry no site column). |
| Reconciliation write path | later slice of PSG-46 | `RecordReconciliation` is called only by tests. The 02:00 job (FR-46.13) that computes both values and calls it is not built, and there is no HTTP write endpoint. |
| BR-46.1 reporting currency and display rounding | handoff to ledger/fx (reporting currency); later slice (rounding) | The ledger journal line carries only the accounting currency amount, so `amount_reporting` is the signed accounting amount in the ledger currency. Translation to a separate reporting currency needs a ledger or fx projection. The web screens show the service's decimal strings as they are; half-even display rounding to the currency's minor units is not applied. |
| Fixtures `fx-lake-usmf-year`, `fx-late-data`, `fx-docs-7`, `fx-oee-shifts` | later slice of PSG-46 (lake, late-data, OEE); handoff (docs) | Need the wave-30 demo replay and the document renderer. |
| Migrations for `semantic_metric`, `cdc_pipeline_state`, `dq_rule`, `dq_score`, `report_schedule`, `archive_partition` | later slice of PSG-46 | Only the outbox and `metric_reconciliation` tables exist. Document tables are handoff with row 4. |
| Analytics events beyond `metric.reconciled.v1` (`snapshot.committed`, `report.delivered`, `dq.scored`, `archive.partition.moved`) | later slice of PSG-46 | Not emitted yet. Print events (`documents.print.routed`, `documents.template.published`) are handoff. |
| Helm charts `passage-cdc`, `passage-lakehouse`, `passage-trino`, `passage-clickhouse`, `passage-cube`, `passage-dbt-runner`, `passage-docgen`, `passage-openmetadata` | handoff to deployment-cicd (except `analytics`) | Owned paths cover only `services/analytics/charts/analytics`. |
| Runtime configuration `CDC_DOMAINS`, `ICEBERG_COMMIT_INTERVAL`, `LATE_DATA_WATERMARK`, `RECON_TOLERANCE` | later slice of PSG-46 | Only `DATABASE_URL`, `MIGRATE_ON_START` and `HTTP_ADDR` are read. |
| dbt profile `passage_trino` and dbt packages install | handoff | Profiles are environment config kept outside the repository. |
| Lakehouse object-store credentials from Vault | handoff to deployment-cicd | Secrets handling is outside owned paths. |
