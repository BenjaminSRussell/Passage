# Part 9 — Phase 9: Intelligence, Compliance & Go-Live (waves 46–50)

## 9.0 Phase overview

**Business outcome at phase exit.** A manufacturer runs its entire business on Passage in production, off AX/SAP, with the vendor dependencies of the legacy systems removed: analytics no longer needs Entity store, BYOD or BW extractors because every domain streams into one governed lakehouse; users ask questions and delegate work to agents that act only within their own permissions; the tenant runs legally in seven countries with statutory outputs validated against regulator schemas; segregation of duties, SOC 2 evidence and privacy requests are system functions rather than spreadsheets; the customer's open items, balances and history were moved by a migration factory that reconciled every control total; and the platform has demonstrated 10k concurrent users, a region failover and a reconciled production month-end. v1.0 is tagged, signed and published.

**Phase-exit sign-off criteria** (all measured in the production-candidate environment unless stated)

- Lakehouse freshness: p95 OLTP-commit-to-dashboard-visible lag < 60 s while the wave-50 load profile runs.
- Semantic-layer `revenue` equals GL revenue (universal journal, account category REVENUE) to the cent for 30 consecutive days; `inventory_value` equals the inventory subledger within USD 0.01 / EUR 0.01.
- Agent evals in CI: AP coding ≥ 92% top-1 accuracy, cash application ≥ 90% precision; permission-inheritance contract tests 100% pass; per-tenant token budget enforced with an alert fired in a test.
- Seven country packs (US, UK, DE, FR, IN, MX, CA) each ship a statutory-report fixture that validates against the regulator's XSD/JSON schema in CI.
- 50 planted SoD conflicts detected (0 misses); mitigation workflow records approver, reason and expiry.
- Backup/restore drill: tenant restored to a point in time with RPO ≤ 5 min and RTO ≤ 1 h; external penetration test shows zero open high/critical findings.
- Synthetic AX and SAP datasets (full year, 3 entities each) migrate with 100% match on all control totals; a parallel-run month shows zero unexplained variances; cutover rehearsal < 48 h; rollback rehearsal restores legacy coexistence.
- Load test: 10k concurrent users, 1M journal lines/h, 100k order lines/h with p99 within the SLO table in 9.50.10.
- DR game day: region failover RTO ≤ 1 h, RPO ≤ 5 min, zero committed-transaction loss (ledger hash chain verified).
- First customer closes a full production month-end with all subledgers reconciled to the journal; hypercare exit criteria (9.50.1) met.
- v1.0 tag signed (cosign), SBOMs attached, release notes and documentation published, v2 roadmap opened.

**Cross-wave data model summary**

| Entity | Owning service | Key relationships |
|---|---|---|
| `lake_table` / `cdc_pipeline_state` | analytics-ingest | 1 per (domain, table); references schema-registry subject |
| `semantic_metric`, `semantic_model` | semantic (Cube) | metric → model → lake tables; metric → `reconciliation_check` |
| `dashboard`, `dashboard_tile`, `report_schedule` | analytics-ui | tile → metric; schedule → dashboard or export |
| `document_template`, `document_render` | docgen | template → source entity type (SalesInvoice, PurchOrder, PackingSlip, Check) |
| `tool_definition`, `agent`, `agent_run`, `agent_action`, `approval_task` | mcp-gateway, agent-runtime | run → agent version; action → tool_definition; approval → Temporal task |
| `eval_dataset`, `eval_run`, `prompt_version`, `token_budget` | agent-evals | eval_run → agent version; budget → tenant |
| `country_pack`, `er_format`, `er_mapping`, `mandate` | localization, ereporting | format → mandate → country; pack → legal entity |
| `sod_rule`, `sod_conflict`, `mitigation`, `certification_campaign` | sod | rule → (duty_a, duty_b); conflict → user, rule; mitigation → conflict |
| `control`, `control_evidence`, `subject_request`, `retention_policy`, `legal_hold` | compliance, privacy | evidence → control; hold overrides policy |
| `migration_pack`, `pack_run`, `control_total`, `legacy_xref`, `parallel_run`, `variance` | migration-factory | run → pack; variance → parallel_run; xref → every migrated entity |
| `cutover_plan`, `cutover_step`, `go_nogo_item` | cutover | step → plan; item → step, owner |
| `region`, `failover_event`, `capacity_model`, `tenant_cost` | platform-ops | failover → region pair; cost → tenant, month |

**Cross-wave event catalog**

| Event (CloudEvents type) | Producer | Consumers | Payload summary |
|---|---|---|---|
| `dev.passage.analytics.snapshot.committed.v1` | analytics-ingest | semantic, catalog | table, snapshot_id, row_delta, max_source_lsn, watermark |
| `dev.passage.analytics.metric.reconciled.v1` | semantic | compliance, alerting | metric_id, date, semantic_value, source_value, delta, status |
| `dev.passage.agent.run.completed.v1` | agent-runtime | audit, copilot-ui, evals | run_id, agent, version, tool_calls[], tokens, outcome |
| `dev.passage.agent.approval.requested.v1` | agent-runtime | workflow inbox | run_id, action_id, proposed_change, risk_score |
| `dev.passage.ereporting.document.submitted.v1` | ereporting | AR/AP, compliance | mandate, format_version, document_ref, regulator_ack |
| `dev.passage.sod.conflict.detected.v1` | sod | compliance, workflow | user_id, rule_id, roles[], severity |
| `dev.passage.privacy.request.completed.v1` | privacy | audit | request_id, type, subject_ref, affected_domains[] |
| `dev.passage.migration.pack.reconciled.v1` | migration-factory | cutover | run_id, pack, control_totals[], status |
| `dev.passage.migration.parallelrun.diffed.v1` | migration-factory | cutover | run_id, period, variances_unexplained, variances_explained |
| `dev.passage.ops.failover.completed.v1` | platform-ops | all services (fencing) | from_region, to_region, promoted_lsn, rpo_seconds, rto_seconds |

**Phase-level risks**

| Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|
| Lakehouse metrics drift from GL (duplicate CDC, late data) | Medium | High | Daily reconciliation job with hard fail; equality-delete upserts keyed on LSN | Analytics lead |
| Agent takes an action outside user permission | Low | Critical | Tool calls carry the user's token; OPA evaluated at the API, never in the agent; contract tests per tool | Security lead |
| Country-pack legal error (wrong tax box, wrong XML) | Medium | High | Regulator schema fixtures in CI; provider adapters (Avalara, Sovos) as fallback; packs behind flags | Localization lead |
| Migration reconciliation hides netting errors | Medium | High | Control totals at line grain, not just sums (counts, hashes, by-dimension) | Migration lead |
| Cutover window overrun | Medium | High | Two full rehearsals; runbook timed per step; abort criteria at T+36 h | Cutover manager |
| Region failover loses committed journal entries | Low | Critical | Synchronous quorum replication for ledger Postgres; hash-chain verification post-failover | SRE lead |
| Two-person team cannot staff hypercare | High | High | Implementation partner on-call rota; agents in suggest-mode triage support tickets | Product |

## 9.46 Wave 46: Data Platform, Analytics & Embedded BI

### 9.46.1 Scope and business context

AX customers build analytics on Entity store aggregate measurements refreshed by batch, BYOD exports to Azure SQL and Power BI embedded workspaces; SSRS runs document layouts through `SrsReportDataProvider` classes. SAP customers extract into BW/4HANA InfoProviders via ODP, expose `@Analytics.dataCategory: #CUBE` CDS views and embed SAC stories. Both put analytical load on OLTP or on a copy that is hours stale. This wave gives every Passage domain a CDC path into an Apache Iceberg lakehouse, a Cube semantic layer with governed metrics whose row-level security mirrors OPA, embedded dashboards in each workspace, a self-service explorer, scheduled reports, an operational document designer that replaces SSRS layouts, and OpenMetadata for catalog and lineage. At wave exit a controller sees revenue on a dashboard within 60 s of posting and knows it equals the GL. Out of scope: natural-language querying (wave 47), historical legacy data (wave 49), customer-built ML models.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Entity store refresh (`AggregateMeasurement`, batch job "Refresh entity store") runs hourly at best; Power BI tiles show numbers up to 4 h old and controllers re-query the GL to confirm | Batch copy from OLTP into a separate star schema; no change feed | Debezium CDC → Iceberg → Cube; freshness badge on every tile | Latency 4 h batch → p95 < 60 s |
| Revenue on a dashboard and revenue on the trial balance disagree; nobody knows which is right (BYOD export gaps, BW extractor delta failures in `RSA7`) | Export jobs drop deltas silently; no reconciliation step | Daily automated metric ↔ GL reconciliation with hard fail and banner | Reconciliation effort 2 person-days/month → 0; delta tolerance 0.005 |
| Changing an SSRS invoice layout needs a developer: edit `.rdl`, extend `SrsReportDataProvider`, build, deploy through LCS; SAP needs `SE71`/`SFP` ABAP skills | Layout bound to code artifacts in a deployable package | Metadata document designer with fixture preview and visual regression | Turnaround 2 weeks → same day; 0 code deploys |
| Building one BW query or SSAS cube perspective for a new KPI takes an analyst through `RSA1`, transformations, DTPs and authorizations (`RSECADMIN`) | Separate security and modeling stack from the application | Metric registry; RLS compiled from the same OPA policies as OLTP | New governed metric 10 days → 1 h; 0 separate security objects |
| Exporting to Excel from a data entity (`Open in Office`) caps at 10k rows or times out; users build shadow spreadsheets | OLTP OData serving analytical loads | Explorer exports via Trino as Temporal jobs | 1M rows in < 10 min; OLTP load from reporting 0 |

### 9.46.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Analytical store | Entity store, `AggregateMeasurement` (e.g. `LedgerActivityMeasure`), batch refresh | BW/4HANA ADSO, CompositeProvider, ODP extractors (`RSA1`) | Debezium CDC → Iceberg `raw.*`, dbt `core.*`/`mart.*` | Improved (streaming, no batch refresh) |
| Data export for BI | BYOD (`DMFDataEntity` export to Azure SQL), Export to Data Lake | SLT, Datasphere replication flows | Iceberg tables queryable via Trino; CDC is the export | Improved |
| Embedded dashboards | Power BI embedded workspaces (`PBIWorkspace`, `SysPBIReportController`) | SAC stories in Fiori launchpad, Overview pages | `<DashboardHost>` web component per workspace | Full |
| Operational documents | SSRS `SalesInvoice.Report`, `PurchPurchaseOrder.Report`, `SrsReportDataProvider`, Print management | SAPscript (`SE71`), Smart Forms, Adobe Forms (`SFP`), `NACE` output | Document designer, `document_template` with print-management rules | Full |
| OLAP cubes | SSAS cubes (`SalesCube`, `GeneralLedgerCube`), perspectives | BEx queries (`RSRT`), BW queries | Cube semantic models and measures | Improved |
| Embedded analytical views | Data entities with `IsAggregateDataEntity` | CDS `@Analytics.dataCategory #CUBE`, `@Analytics.query` | Cube `cubes`/`views` generated from entity metadata | Full |
| Self-service / Excel | Open in Excel (`DataEntity` + OData), Power BI Desktop over BYOD | Analysis for Office, Query Browser (`/UI2/QUERY_BROWSER`) | Explorer; export to XLSX, Power BI (ODBC via Trino), Tableau | Full |
| Row-level security | XDS policies, Power BI RLS roles | Analysis authorizations (`RSECADMIN`) | OPA policy compiled to Cube `queryRewrite` filters | Full |
| Financial statements | Management Reporter | FSV (`FSE2`), Fiori financial statement apps | Wave-15 statement designer; marts feed trend tiles | Full |
| Catalog and lineage | None (AOT only) | BW metadata repository, Datasphere catalog | OpenMetadata with Debezium/dbt/Cube lineage ingestion | Improved |

### 9.46.3 Functional requirements

- **FR-46.1 CDC from every domain** — Must. The system shall run one Debezium Postgres connector per domain database using `pgoutput`, publishing to topics `cdc.<domain>.<table>` keyed on `(tenant_id, primary key)`, Avro-encoded with schema-registry compatibility `BACKWARD_TRANSITIVE`.
  - Notes: every table listed in entity metadata with `analytics: true` is captured; `REPLICA IDENTITY FULL` enforced by the Atlas migration generator; snapshot mode `initial` on first run then `never`; heartbeat every 10 s to advance slots on idle domains; slot lag > 1 GB alerts.
- **FR-46.2 Iceberg landing contract** — Must. The system shall write each CDC topic into Iceberg table `raw.<domain>.<table>` (format v2, merge-on-read, equality deletes on the key) partitioned by `days(_source_ts)` and `bucket(16, tenant_id)`.
  - Notes: every row carries `_op` (c/u/d), `_source_lsn`, `_source_ts`, `_ingested_at`; commits every 30 s or 50 MB, whichever first; snapshot expiry 7 days, orphan-file cleanup daily; compaction rewrites files < 64 MB nightly.
- **FR-46.3 Schema evolution** — Must. The system shall apply additive source changes automatically and quarantine incompatible ones.
  - Notes: add column → add nullable Iceberg column; type widening (`int→bigint`, `decimal(18,4)→decimal(28,6)`) applied; dropped column retained and tagged `deprecated_since`; rename → new column plus `renamed_from` property in catalog; narrowing or NOT NULL additions pause the sink, route to `cdc.dlq.<domain>` and alert within 60 s.
- **FR-46.4 Late-arriving data and exactly-once effect** — Must. The system shall produce exactly one logical state per key regardless of Kafka redelivery.
  - Notes: sink deduplicates on `(tenant_id, pk, _source_lsn)`; Iceberg commit and Kafka offset are written atomically by the sink coordinator (control topic `iceberg.control`), so replay after crash is idempotent; watermark = max(`_source_ts`) − 5 min; rows with `_source_ts` older than the watermark are merged into closed partitions; dbt incremental models use a 3-day lookback; a row arriving more than 30 days late is written and flagged `late=true` for reconciliation.
- **FR-46.5 dbt transformation layers** — Must. The system shall maintain `core.*` (deduplicated current-state and history tables, SCD2 on master data) and `mart.*` (finance, sales, procurement, inventory, production, projects, HR) dbt models with tests on every primary key, every FK and every reconciled measure.
  - Notes: `mart.fin_journal_line` is the only source for monetary metrics; models run every 60 s incremental, nightly full for SCD2 rebuild; dbt test failure blocks downstream models and raises `metric.reconciled` with status `BLOCKED`.
- **FR-46.6 Governed metrics** — Must. The system shall define metrics in the semantic layer with owner, version, grain, allowed dimensions and formula; the initial set is:
  - `revenue` = `-SUM(amount_reporting) FROM mart.fin_journal_line WHERE account_category='REVENUE' AND status='POSTED'` (credit shown positive).
  - `cogs` = `SUM(amount_reporting) WHERE account_category='COGS'`; `gross_margin` = `revenue - cogs`; `gross_margin_pct` = `gross_margin / NULLIF(revenue,0)`.
  - `dso_days` = `ar_open_balance_at(end) / NULLIF(revenue_trailing(90d),0) * 90`; countback variant selectable per tenant.
  - `dpo_days` = `ap_open_balance_at(end) / NULLIF(cogs_trailing(90d),0) * 90`.
  - `inventory_turns` = `cogs_trailing(365d) / NULLIF(AVG(inventory_value_eom over last 13 month-ends),0)`.
  - `otif_pct` = `COUNT(sales_line WHERE shipped_ts <= requested_ship_date AND qty_shipped = qty_ordered) / NULLIF(COUNT(sales_line shipped),0)`.
  - `oee_pct` = `availability * performance * quality` where `availability = run_time / planned_time`, `performance = (ideal_cycle_time * total_count) / run_time`, `quality = good_count / total_count`, sourced from `mart.prod_resource_shift`.
- **FR-46.7 Row-level security mirrored from OPA** — Must. The system shall compile the user's OPA data-scope decision (legal entities, sites, dimension ranges) into Cube `queryRewrite` filters on every query, using the same Keycloak token as the OLTP APIs.
  - Notes: cache keyed by `(user_id, policy_bundle_version)` for 5 min; a query touching a model with no scope mapping is rejected (`403 SCOPE_UNMAPPED`); aggregations never bypass filters (pre-aggregations are built per security context).
- **FR-46.8 Embedded dashboards** — Must. The system shall render a dashboard host in every workspace with tiles bound to semantic metrics, auto-refresh ≤ 60 s, and drill-through to the OLTP record.
- **FR-46.9 Self-service explorer and export** — Should. The system shall let users compose metric × dimension × filter queries, save them, and export to XLSX (≤ 1M rows), CSV, Power BI (Trino ODBC/JDBC connection string) and Tableau (same).
  - Notes: exports > 100k rows run as Temporal jobs and deliver via signed URL valid 24 h; every export is audit-logged with row count and filter.
- **FR-46.10 Scheduled reports** — Should. The system shall schedule dashboards and explorer queries (cron, time-zone aware) with delivery to email, SFTP or in-app inbox, PDF/XLSX, retry 3× with backoff.
- **FR-46.11 Operational document designer** — Must. The system shall provide a layout designer (bands, repeaters, barcodes, conditional sections, locale formats) for SalesInvoice, PurchOrder, PackingSlip, Check, Statement, PickList and Remittance with versioned templates and print-management rules (per entity, per customer/vendor, per destination).
  - Notes: render via headless Chromium to PDF/A-3; data source is the document's REST read model, not the lakehouse; p95 render < 800 ms for a 50-line invoice.
- **FR-46.12 Catalog and lineage** — Should. The system shall register every Postgres table, Iceberg table, dbt model, Cube metric and dashboard in OpenMetadata with column-level lineage and owner, refreshed hourly.
- **FR-46.13 Daily metric reconciliation** — Must. The system shall compare `revenue`, `cogs`, `ar_open_balance`, `ap_open_balance`, `inventory_value` against the universal journal and subledgers at 02:00 tenant local time and fail the dashboard with a banner if |delta| > 0.005.

### 9.46.4 Data model

**`semantic_metric`** (semantic service, Postgres `analytics`)

| Field | Type | Required | Notes |
|---|---|---|---|
| metric_id | text | Y | PK, e.g. `revenue` |
| version | int | Y | monotonically increasing; (metric_id, version) unique |
| formula | text | Y | Cube measure SQL |
| grain | text | Y | `journal_line`, `sales_line`, `resource_shift` |
| allowed_dimensions | text[] | Y | from `semantic_model.dimensions` |
| owner_user_id | uuid | Y | FK users |
| status | enum | Y | DRAFT, PUBLISHED, DEPRECATED |
| reconciliation_source | text | N | e.g. `ledger.balance_by_account` |
| tolerance | numeric(18,6) | N | default 0.005 |

**`cdc_pipeline_state`** (analytics-ingest)

| Field | Type | Required | Notes |
|---|---|---|---|
| domain | text | Y | PK part |
| table_name | text | Y | PK part |
| schema_version | int | Y | registry version last applied |
| last_lsn | bigint | Y | last committed source LSN |
| last_snapshot_id | bigint | Y | Iceberg snapshot |
| watermark_ts | timestamptz | Y | |
| status | enum | Y | RUNNING, PAUSED_SCHEMA, PAUSED_ERROR |
| dlq_count_24h | int | Y | default 0 |

**`document_template`** (docgen)

| Field | Type | Required | Notes |
|---|---|---|---|
| template_id | uuid | Y | PK |
| tenant_id | uuid | Y | RLS |
| document_type | enum | Y | SalesInvoice, PurchOrder, PackingSlip, Check, Statement, PickList, Remittance |
| version | int | Y | unique with template_id |
| locale | text | Y | BCP 47, e.g. `de-DE` |
| layout_json | jsonb | Y | band tree; max 512 KB |
| status | enum | Y | DRAFT, PUBLISHED, RETIRED |
| visual_hash | text | N | perceptual hash of fixture render |
| published_at | timestamptz | N | |

**`report_schedule`**: schedule_id, tenant_id, target_type (DASHBOARD/QUERY), target_id, cron, timezone, format, destination_json, last_run_at, next_run_at, failure_count.

Invariants: a PUBLISHED metric is immutable (changes create a new version); every mart measure has exactly one reconciliation source or is tagged `non_financial`; a template can be PUBLISHED only if its visual regression delta ≤ 0.1%; `last_lsn` never decreases.
Indexing: `cdc_pipeline_state` PK (domain, table_name); Iceberg partition spec as FR-46.2 plus sort order `(tenant_id, _source_lsn)`; `document_template` index (tenant_id, document_type, status).

### 9.46.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| GET | `/api/v1/analytics/metrics` | list published metrics with formulas | n/a | AnalyticsView |
| POST | `/api/v1/analytics/query` | Cube query (measures, dimensions, filters, timeDimensions) | n/a (read) | AnalyticsView + data scope |
| GET | `/api/v1/analytics/dashboards/{id}` | dashboard definition and tiles | n/a | AnalyticsView |
| PUT | `/api/v1/analytics/dashboards/{id}` | create/update dashboard | ETag | AnalyticsAuthor |
| POST | `/api/v1/analytics/exports` | start export job | `Idempotency-Key` | AnalyticsExport |
| POST | `/api/v1/analytics/schedules` | create schedule | `Idempotency-Key` | AnalyticsAuthor |
| GET | `/api/v1/analytics/reconciliation?date=` | reconciliation results per metric | n/a | FinanceView |
| POST | `/api/v1/documents/templates/{id}/versions` | publish template version | `Idempotency-Key` | DocumentDesign |
| POST | `/api/v1/documents/render` | render document (type, record_id, template override) | by (record, version) | per-document duty |
| GET | `/api/v1/catalog/lineage/{asset_fqn}` | upstream/downstream lineage | n/a | CatalogView |

Query validation: ≤ 10 measures, ≤ 8 dimensions, time range ≤ 5 years at day grain, result cap 50k rows interactive; otherwise redirected to export.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.analytics.snapshot.committed.v1` | Iceberg commit | domain, table, snapshot_id, rows_added, rows_deleted, max_lsn, watermark_ts | semantic (cache invalidation), catalog |
| `dev.passage.analytics.metric.reconciled.v1` | daily job | metric_id, tenant_id, date, semantic_value, source_value, delta, status | compliance evidence, alerting |
| `dev.passage.analytics.report.delivered.v1` | schedule run | schedule_id, run_id, format, destination, row_count, bytes | audit |
| `dev.passage.documents.template.published.v1` | version publish | template_id, version, document_type, visual_hash | print management, catalog |

### 9.46.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Workspace dashboard host | refresh, drill-through, change period, pin tile | metric tiles, sparklines, freshness badge | AnalyticsView |
| Explorer | add measure/dimension, filter, pivot, save, export | metric picker with formula tooltip, dimension tree | AnalyticsView |
| Metric registry | publish version, set owner, view reconciliation history | metric_id, formula, grain, tolerance, last delta | AnalyticsAuthor |
| Pipeline health | pause/resume, replay from LSN, view DLQ | domain, table, lag_s, status, dlq_count | AnalyticsAdmin |
| Document designer | drag bands, bind fields, preview with fixture, publish | band tree, locale, print-management rules | DocumentDesign |
| Schedules | create, run now, view deliveries | cron, timezone, destination, last status | AnalyticsAuthor |

Interaction rules: explorer queries run on 500 ms debounce; `Ctrl+E` exports current grid; freshness badge turns amber at lag > 60 s, red at > 300 s; designer preview reruns on each property change with a 300 ms debounce; personalization (pinned tiles, saved explorer views) stored per user and exported with user profile.

### 9.46.7 Business rules and edge cases

- **BR-46.1** Monetary metrics use `amount_reporting` in the ledger's reporting currency; rounding at display only, half-even, to the currency's minor units.
- **BR-46.2** A deleted OLTP row (`_op='d'`) is tombstoned in `raw.*`, removed from `core.*` current tables and kept in history with `valid_to`.
- **BR-46.3** Reversal journals net to zero in `revenue` on the reversal's posting date, not the original date.
- **BR-46.4** DSO/DPO use calendar days; trailing windows end at the selected period end inclusive; if the trailing window has zero revenue, the metric is NULL, never infinity.
- **BR-46.5** OEE `planned_time` excludes shifts flagged `no_production`; `performance` is capped at 100%.
- **BR-46.6** Pipeline state machine: RUNNING → PAUSED_SCHEMA (incompatible change) → RUNNING after operator approves mapping; RUNNING → PAUSED_ERROR after 5 consecutive sink failures → RUNNING on replay from `last_lsn`.
- **BR-46.7** Export files are deleted 24 h after creation; a second identical request within 1 h returns the existing file.
- **BR-46.8** Template version state: DRAFT → PUBLISHED (requires visual regression pass) → RETIRED; a RETIRED version remains renderable for re-prints of documents posted under it.
- **BR-46.9** Dashboard queries for a user with no legal-entity scope return an empty result with reason `NO_SCOPE`, not an error.
- **BR-46.10** A reconciliation delta inside tolerance for 30 days auto-marks the metric `TRUSTED`; one failure resets the counter.

### 9.46.8 Acceptance criteria

- **AC-46.1** (FR-46.1, 46.2) Given a journal posted in `ledger` with 1,000 lines, when 60 s elapse, then `mart.fin_journal_line` contains all 1,000 rows with matching `_source_lsn` order.
- **AC-46.2** (FR-46.3) Given a migration adds nullable column `memo2` to `sales_line`, when CDC resumes, then the Iceberg table has the column and no DLQ rows.
- **AC-46.3** (FR-46.3) Given a migration changes `qty` from `decimal(28,6)` to `decimal(18,4)`, when CDC resumes, then the pipeline is PAUSED_SCHEMA within 60 s and an alert fires.
- **AC-46.4** (FR-46.4) Given Kafka redelivers 10,000 already-applied change events, when the sink processes them, then row counts and sums in `core.*` are unchanged.
- **AC-46.5** (FR-46.4) Given a row with `_source_ts` 10 days in the past arrives, when dbt runs, then the row is present in the correct day partition and `late=true`.
- **AC-46.6** (FR-46.6, 46.13) Given a day's postings, when reconciliation runs, then semantic `revenue` equals ledger REVENUE balance movement to USD 0.00 and EUR 0.00.
- **AC-46.7** (FR-46.7) Given user A scoped to legal entity USMF and user B to DEMF, when both open the sales dashboard, then A sees only USMF rows and B only DEMF rows, including in totals.
- **AC-46.8** (FR-46.11) Given the 7 standard document templates and their fixtures, when rendered in CI, then perceptual-hash delta vs baseline ≤ 0.1% for each.
- **AC-46.9** (FR-46.9) Given an explorer query returning 250k rows, when the user exports XLSX, then a Temporal job produces the file and a signed URL within 5 min.
- **AC-46.10** (FR-46.10) Given a schedule `0 6 * * 1` Europe/Berlin, when Monday 06:00 CET arrives, then the PDF is delivered and `report.delivered` is emitted.

### 9.46.9 Test plan and fixtures

Fixtures: `fx-lake-usmf-year` (one fiscal year, 3 entities, 2.4M journal lines, 180k sales lines, from the wave-30 demo dataset replayed); `fx-schema-evolution` (12 migration scripts: add, widen, drop, rename, narrow, not-null); `fx-late-data` (5k events with `_source_ts` skewed 1 h–45 d); `fx-docs-7` (7 document types × 3 locales × fixture records); `fx-oee-shifts` (90 days, 12 resources).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Sink idempotency | property | Iceberg sink | any permutation/duplication of events yields identical table state |
| Schema evolution matrix | contract | connector + sink | 12 scripts classified correctly (8 auto, 4 pause) |
| Metric formula unit tests | unit | dbt/Cube | each metric matches hand-computed value on 20-row fixture |
| OPA→Cube filter equivalence | contract | semantic | for 200 users, Cube result set ⊆ OLTP API result set |
| Dashboard lag | performance | end-to-end | p95 < 60 s at 1M journal lines/h |
| Visual regression | E2E | docgen | 21 renders ≤ 0.1% delta |
| Export at scale | performance | explorer | 1M-row XLSX in < 10 min |
| Lineage completeness | contract | OpenMetadata | 100% of dbt models and metrics have upstream edges |

Reconciliation: `revenue`, `cogs` ↔ ledger balance movements by account category; `ar_open_balance`, `ap_open_balance` ↔ open-item subledgers; `inventory_value` ↔ inventory close valuation; row counts `raw.*` ↔ `core.*` current ↔ Postgres `COUNT(*)` per tenant.

### 9.46.10 Deployment and operations

Helm charts: `passage-cdc` (Kafka Connect cluster with Debezium and Iceberg sink, 3 workers), `passage-lakehouse` (Iceberg REST catalog, Nessie-compatible, object storage via S3 API), `passage-trino` (coordinator + 4 workers, autoscaling to 16), `passage-clickhouse` (optional accelerator for `mart.*` pre-aggregations), `passage-cube` (API + refresh workers), `passage-dbt-runner` (Temporal worker), `passage-docgen`, `passage-openmetadata`.
Config: `CDC_DOMAINS` list, `ICEBERG_COMMIT_INTERVAL=30s`, `LATE_DATA_WATERMARK=5m`, `RECON_TOLERANCE=0.005`, object-store credentials from Vault.
Migrations: `analytics` database (Atlas); Iceberg schemas managed by the sink.
Feature flags: `analytics.dashboards`, `analytics.explorer`, `docgen.designer` per tenant.
Jobs: dbt incremental every 60 s, SCD2 nightly 01:00, reconciliation 02:00, compaction 03:00, lineage sync hourly.
SLOs: dashboard lag p95 < 60 s; query p95 < 2 s for ≤ 50k rows; document render p95 < 800 ms; pipeline availability 99.9%.
Alerts: slot lag > 1 GB, DLQ > 0 for 5 min, reconciliation delta > tolerance, Trino queue > 50.
Runbook: replay from LSN, rebuild `core.*` from `raw.*`, rotate object-store keys.
Rollback: Helm rollback of services; Iceberg `rollback_to_snapshot` per table; connectors restart from stored offsets.

### 9.46.11 Migration from AX / SAP

| Source | Passage | Notes |
|---|---|---|
| Entity store `AggregateMeasurement` definitions | `semantic_metric` | mapped by hand; measures become Cube measures, dimensions become joins |
| Power BI `.pbix` over BYOD | Dashboards | not converted; rebuilt on Cube; Power BI Desktop can connect to Trino |
| SSRS `.rdl` layouts via `SrsReportDataProvider` | `document_template` | semi-automatic: bands, fields and expressions imported; SSRS expressions in VB are flagged for manual rewrite |
| Print management settings (`PrintMgmtSettings`) | print-management rules | full |
| BW InfoProviders, BEx queries | `mart.*` + metrics | metadata import creates draft metrics; data not migrated here (wave 49) |
| SAC stories | Dashboards | rebuilt |
| Smart Forms / Adobe Forms | `document_template` | layout imported from XDP where possible; scripting rewritten |

Not migrated: SSAS cube partitions, BYOD Azure SQL contents, Power BI datasets — all derivable from the lakehouse.

### 9.46.12 Dependencies, risks, open questions

Depends on: wave 3 (outbox/CDC infrastructure), wave 5 (audit from CDC), wave 11–15 (universal journal, statements), wave 30 (sales and AR marts), wave 25 (OEE source data), wave 45 (all domains emitting).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Iceberg small-file explosion at 30 s commits | High | Medium | nightly compaction; adaptive commit size |
| Cube pre-aggregations leak across security contexts | Low | Critical | pre-aggregations partitioned by legal entity; contract test |
| Trino cost at 10k users | Medium | Medium | ClickHouse accelerator for top-20 dashboards |

Open questions: (1) Trino-only vs Trino+ClickHouse by default — proposed default: Trino only, ClickHouse behind flag; decide by wave 47. (2) Document rendering engine fallback (Chromium vs Typst) — default Chromium; decide by wave 48.

## 9.47 Wave 47: AI Copilots, Agents & Automation

### 9.47.1 Scope and business context

D365 ships Copilot for finance and operations (summaries, draft e-mails, Copilot in workflow history) and Power Automate flows triggered from business events; SAP ships Joule and SAP Build Process Automation. Neither lets a governed agent execute a multi-step ERP process under the invoking user's exact permissions with an eval gate. This wave exposes every Passage API, entity and report as MCP tools through a gateway that forwards the user's token to OPA-protected APIs; adds a copilot in the shell (ask-the-data over the wave-46 semantic layer, explain-this-record, draft documents, summarize workflows); runs agents on Temporal with approval gates; gates deployment on an eval harness; and enforces responsible-AI controls with a provider-agnostic model layer (Claude default; Bedrock, Azure OpenAI, open-weights via vLLM selectable). Out of scope: training custom models, autonomous posting without approval in v1, voice.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Coding a vendor invoice in `VendInvoiceJour`/`MIRO` means opening the PO, the vendor history and the chart of accounts in three forms, then keying account, dimensions and tax code per line | No suggestion engine; copilots cannot read the posting context | AP coding agent proposes account, dimensions, tax code, PO match per line; approver confirms | Clicks per invoice 18 → 2; coding time 4 min → 20 s; top-1 accuracy ≥ 92% |
| Cash application: `CustPaymMatch` rules and `F-28` match exact amounts only; partials and multi-invoice remittances go to a manual queue | Rule-based matching without reading remittance text | Cash application agent ranks settlements from remittance text and tolerances | Auto-matched share 60% → 90%+; unapplied cash aged > 7 d reduced 10× |
| Power Automate / SAP Build flows need a connector project, a service account with broad rights and a separate audit trail | Automation runs outside the ERP's authorization model | MCP tools with user-token inheritance; Temporal agent runtime; `agent_action` audit | Integration project 6 weeks → 0 (tools generated); 0 actions beyond user permission |
| "Why is this number what it is?" requires reading workflow history, `SysDatabaseLog` and voucher transactions across forms | Context scattered across tables with no narrative | Explain-this-record over audit trail and related documents with citations | Investigation 15 min → 30 s |
| D365 Copilot / Joule answer from a vendor model with no accuracy gate, no cost control and no kill switch | Vendor-managed black box | Eval harness in CI, token budgets, kill switch, provider choice | 100% of agent releases gated by evals; kill in ≤ 10 s |

### 9.47.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Conversational assistant | D365 Copilot sidecar (`CopilotFeatureManagement`), Copilot summaries on workflow history | Joule in S/4HANA Cloud, Fiori launchpad | Copilot panel in shell | Improved (acts, not only answers) |
| Ask the data | Copilot "ask about data" over Dataverse virtual entities | Joule analytics via SAC | `ask_data` tool → Cube query with OPA scope | Full |
| Process automation | Power Automate flows on business events (`BusinessEventsCatalog`), Flow connectors | SAP Build Process Automation, workflow agents | Agent runtime on Temporal, triggered by CloudEvents | Full |
| Invoice coding suggestions | Vendor invoice automation, `VendInvoiceAutomation` | Cash Application ML, Invoice assistant (Business AI) | AP coding agent (suggest → approve) | Full |
| Cash application | Customer payment advanced matching rules (`CustPaymMatch`) | Cash Application ML (`FINS_CASH_ML`) | Cash application agent | Full |
| Planning exceptions | Planning Optimization action messages | MRP Cockpit (`MD04`, `PPH_MRP_COCKPIT`), Situation Handling | MRP exception triage agent | Improved |
| Collections outreach | Collections workspace, `CustCollectionLetter` | FSCM Collections worklist (`UDM_SUPERVISOR`) | Collections agent drafts outreach | Full |
| Anomaly detection | Finance insights (cash-flow forecast, late-payment prediction) | Business AI journal anomaly detection | Journal anomaly agent | Full |
| Audit of AI actions | None dedicated | None dedicated | `agent_action` audit with inputs, tool calls, outputs, approver | Improved |

### 9.47.3 Functional requirements

- **FR-47.1 MCP gateway** — Must. The system shall expose Passage capabilities as MCP tools from a single gateway (`mcp.passage.<tenant>`), generated from OpenAPI/AsyncAPI and entity metadata.
  - Notes: tool names `passage.<domain>.<entity>.<verb>` (e.g. `passage.ap.invoice.list`, `passage.ap.invoice.propose_coding`, `passage.ledger.journal.get`); `inputSchema` is the endpoint's JSON Schema with tenant_id removed (injected from token); each tool declares `annotations.readOnlyHint`, `destructiveHint`, `idempotentHint`, and a Passage extension `x-passage-duty` (required duty) and `x-passage-approval` (`none`, `suggest`, `approve_before_commit`); read tools return ≤ 200 rows per call with cursor; a tool call carries `traceparent` and `x-agent-run-id`.
- **FR-47.2 Permission inheritance** — Must. The system shall execute every tool call with the invoking user's Keycloak token (on-behalf-of exchange, `act` claim = agent id), so OPA evaluates the user's duties and data scope at the API; agents hold no credentials of their own.
  - Notes: token lifetime for agent runs 15 min, refreshed by Temporal activity; a tool whose `x-passage-duty` the user lacks is not listed to the model; contract test: for each of 400 tools, a user without the duty receives 403 from the gateway and the API.
- **FR-47.3 Copilot UI** — Must. The system shall provide a shell panel with modes ask-the-data, explain-this-record, draft (e-mail, PO text, dunning letter), summarize (workflow history, case) with citations to records and metrics used.
  - Notes: streaming responses; every answer lists tool calls made; "explain" uses the record's audit trail and related documents; drafts are inserted only on user click.
- **FR-47.4 Agent runtime** — Must. The system shall run agents as Temporal workflows (`AgentRunWorkflow`) with steps plan → tool calls → proposal → approval gate → commit, each tool call an activity with retry policy and 60 s timeout, run timeout 24 h including approval wait.
- **FR-47.5 AP invoice coding agent** — Must. The system shall propose account, dimensions, tax code and PO match for an unposted vendor invoice using vendor history, PO lines and text, in suggest mode (approver confirms) or auto mode when confidence ≥ 0.95 and amount < tenant threshold (default USD 5,000 / EUR 4,500).
- **FR-47.6 Cash application agent** — Must. The system shall propose settlements for unapplied customer payments (remittance text, amounts, tolerances), ranked by confidence; applies only after approval in v1.
- **FR-47.7 MRP exception triage, collections outreach, journal anomaly agents** — Should. The system shall ship these three in suggest mode: triage groups action messages by cause and proposes expedite/postpone; collections drafts outreach per dunning policy; anomaly scores journals on amount, account pairing, time of posting and user, flagging score > 0.8.
- **FR-47.8 Approval gates** — Must. The system shall route any `approve_before_commit` action to the wave-4 workflow inbox with proposed change, diff, confidence and rationale; approver decisions are recorded on `agent_action`.
- **FR-47.9 Eval harness** — Must. The system shall maintain golden datasets per agent and run evals in CI on every prompt, model or tool change, blocking deploy below thresholds.
  - Metrics and thresholds: AP coding top-1 accuracy ≥ 0.92, top-3 ≥ 0.98; cash application precision ≥ 0.90, recall ≥ 0.75; MRP triage macro-F1 ≥ 0.80; anomaly AUROC ≥ 0.85, false-positive rate ≤ 0.05; ask-the-data metric-selection accuracy ≥ 0.95 and SQL-equivalence ≥ 0.90; refusal correctness ≥ 0.99 on an adversarial set of 500 prompts; p95 latency per agent run < 30 s excluding approval; cost per run ≤ budgeted USD; regression gate = no metric drops > 1 point vs last release.
- **FR-47.10 Prompt and version registry** — Must. The system shall version system prompts, tool lists, model id and parameters as `agent_version`; production pins a version; rollback is a pointer change.
- **FR-47.11 Token budgets** — Must. The system shall enforce per-tenant monthly token budgets (default 50M input / 10M output), soft alert at 80%, hard stop at 100% with copilot degraded to deterministic features; per-run cap 400k tokens.
- **FR-47.12 Responsible-AI controls** — Must. The system shall: redact PII (names, IBAN, tax ids, emails) from prompts to external providers using the wave-42 PII classification, replacing with stable pseudonyms resolved after response; log every action (prompt hash, tool calls, outputs, approver); provide a tenant and global kill switch that halts all runs within 10 s; prohibit model-initiated posting without `approve_before_commit` or auto-mode policy; label all generated text in UI; keep a model card per agent version; allow provider selection per tenant with data-residency constraint checked against wave-48 tenant residency.
- **FR-47.13 Provider layer** — Must. The system shall abstract chat completion, tool use and embeddings behind one interface with adapters for Anthropic API, Amazon Bedrock, Azure, and OpenAI-compatible open-weights endpoints; feature parity test per adapter.

### 9.47.4 Data model

**`tool_definition`** (mcp-gateway)

| Field | Type | Required | Notes |
|---|---|---|---|
| tool_name | text | Y | PK, `passage.<domain>.<entity>.<verb>` |
| version | int | Y | from OpenAPI version |
| input_schema | jsonb | Y | JSON Schema draft 2020-12 |
| output_schema | jsonb | Y | |
| required_duty | text | Y | OPA duty id |
| approval_mode | enum | Y | none, suggest, approve_before_commit |
| read_only | bool | Y | |
| source_ref | text | Y | OpenAPI operationId or report id |

**`agent_run`** (agent-runtime)

| Field | Type | Required | Notes |
|---|---|---|---|
| run_id | uuid | Y | PK = Temporal workflow id |
| tenant_id | uuid | Y | RLS |
| agent_id | text | Y | e.g. `ap_coding` |
| agent_version | int | Y | FK agent_version |
| invoked_by_user_id | uuid | Y | whose permissions apply |
| trigger | enum | Y | USER, EVENT, SCHEDULE |
| status | enum | Y | PLANNING, ACTING, AWAITING_APPROVAL, COMMITTED, REJECTED, FAILED, KILLED |
| tokens_in / tokens_out | bigint | Y | |
| cost_usd | numeric(12,6) | Y | |
| started_at / ended_at | timestamptz | Y/N | |

**`agent_action`**: action_id, run_id, seq, tool_name, input_json (redacted), output_hash, output_json (redacted, ≤ 64 KB), approval_task_id, approver_user_id, decision (APPROVED/REJECTED/AUTO), decided_at, committed_entity_ref.

**`eval_run`**: eval_run_id, agent_id, agent_version, dataset_id, dataset_version, metrics_json, passed bool, ci_ref, created_at. **`token_budget`**: tenant_id, month, limit_in, limit_out, used_in, used_out, state (OK/SOFT/HARD).

Invariants: `agent_action.input_json` never contains raw PII (checked by a redaction scanner at write); an action with approval_mode `approve_before_commit` has `committed_entity_ref` only if decision ∈ {APPROVED}; a run's `invoked_by_user_id` is immutable; a production `agent_version` has a passing `eval_run`.
Indexing: `agent_run` (tenant_id, status, started_at), partition by month; `agent_action` (run_id, seq).

### 9.47.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| GET | `/mcp` (SSE/streamable HTTP) | MCP server endpoint; `tools/list`, `tools/call` | per JSON-RPC id | user token |
| POST | `/api/v1/copilot/chat` | copilot conversation turn (stream) | n/a | CopilotUse |
| POST | `/api/v1/agents/{agent_id}/runs` | start a run | `Idempotency-Key` | AgentInvoke + agent's duty |
| GET | `/api/v1/agents/runs/{run_id}` | run status, actions, costs | n/a | AgentView |
| POST | `/api/v1/agents/runs/{run_id}/actions/{id}/decision` | approve/reject | by action id | approver duty |
| POST | `/api/v1/agents/kill-switch` | halt tenant or global | by scope | AgentAdmin / PlatformAdmin |
| GET | `/api/v1/agents/budgets/{tenant}` | budget status | n/a | AgentAdmin |
| POST | `/api/v1/agents/evals/runs` | trigger eval (CI) | by commit sha | CI service account |
| PUT | `/api/v1/agents/{agent_id}/versions/{v}/promote` | pin production version | ETag | AgentAdmin |

Validation: run start rejects when tenant budget state HARD, kill switch active, or agent version lacks passing eval; decision endpoint rejects if the approver equals `invoked_by_user_id` and the action's amount exceeds the SoD self-approval limit (wave 48).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.agent.run.completed.v1` | run end | run_id, agent_id, version, status, tool_calls[], tokens, cost_usd | audit, evals, copilot |
| `dev.passage.agent.approval.requested.v1` | gate reached | run_id, action_id, tool_name, proposal, confidence, risk_score | workflow inbox |
| `dev.passage.agent.budget.threshold.v1` | 80%/100% | tenant_id, month, pct, state | alerting, admin UI |
| `dev.passage.agent.killswitch.changed.v1` | toggle | scope, active, by_user | all agent workers |

### 9.47.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Copilot panel (shell) | ask, explain record, draft, summarize, view tool calls | conversation, citations, "insert draft" | CopilotUse |
| Agent inbox (in workflow inbox) | approve, reject, edit proposal, bulk approve ≤ 50 | proposal diff, confidence, rationale, amount | approver duty |
| Agent runs | filter, inspect timeline, replay in sandbox | run_id, agent, status, tokens, cost | AgentView |
| Agent administration | enable/disable per entity, set mode, thresholds, kill switch | agent, version, mode, auto threshold | AgentAdmin |
| Eval dashboard | compare versions, open failing cases | metric per version, trend | AgentAdmin |

Rules: `Ctrl+/` opens copilot with current record context; approvals support keyboard `A`/`R`; generated text is marked with a badge and never auto-saved; copilot remembers mode per workspace per user.

### 9.47.7 Business rules and edge cases

- **BR-47.1** Run state machine: PLANNING → ACTING → (AWAITING_APPROVAL → COMMITTED | REJECTED) | COMMITTED (auto) | FAILED | KILLED; no transition out of COMMITTED/REJECTED/KILLED.
- **BR-47.2** Auto mode commits only when confidence ≥ threshold AND amount < limit AND the vendor/customer is not flagged `high_risk` AND no SoD conflict exists for the invoking user.
- **BR-47.3** Approval requests expire after 72 h → REJECTED with reason `EXPIRED`; the source document stays unposted.
- **BR-47.4** Token accounting uses provider-reported usage; if absent, estimated at 4 chars/token and flagged `estimated`.
- **BR-47.5** Kill switch active → running activities finish their current API call, no new tool calls; runs move to KILLED within 10 s.
- **BR-47.6** A tool error 4xx is not retried; 5xx/timeouts retry 3× with exponential backoff (1 s, 4 s, 16 s).
- **BR-47.7** Redaction pseudonyms are stable per (tenant, value) for 24 h so the model can refer back; the mapping is stored only in memory of the run worker and in Vault transit-encrypted form on `agent_run`.
- **BR-47.8** Eval datasets are versioned; a dataset change requires re-baselining all agent versions before promotion.
- **BR-47.9** Provider failover: if the default provider returns 5xx for 60 s, runs switch to the tenant's secondary provider only if it satisfies residency; otherwise runs pause.
- **BR-47.10** Cash application proposals never split a payment across more than 20 invoices, and never create write-offs above the wave-30 tolerance.

### 9.47.8 Acceptance criteria

- **AC-47.1** (FR-47.1) Given an authenticated user, when `tools/list` is called, then only tools whose `required_duty` the user holds are returned.
- **AC-47.2** (FR-47.2) Given user without duty `APInvoicePost`, when the agent calls `passage.ap.invoice.post`, then gateway returns 403 and the action is logged `DENIED`.
- **AC-47.3** (FR-47.5, 47.9) Given the `eval-ap-coding-v3` dataset (2,000 invoices), when evals run, then top-1 accuracy ≥ 0.92 and the CI job passes.
- **AC-47.4** (FR-47.6, 47.9) Given `eval-cash-app-v2` (1,500 payments), when evals run, then precision ≥ 0.90.
- **AC-47.5** (FR-47.8) Given a coding proposal with confidence 0.80, when the run reaches the gate, then an approval task appears in the inbox and nothing is posted.
- **AC-47.6** (FR-47.12) Given a tenant kill switch toggled, when 10 s elapse, then all runs for that tenant are KILLED and no provider calls occur.
- **AC-47.7** (FR-47.11) Given a tenant at 100% budget, when a run is started, then it is rejected with `BUDGET_EXHAUSTED` and an alert was raised at 80%.
- **AC-47.8** (FR-47.12) Given an invoice containing a vendor IBAN, when the prompt is sent, then the provider request contains a pseudonym and the response shows the real IBAN only in the UI.
- **AC-47.9** (FR-47.3) Given "what was USMF revenue last month vs the month before", when asked, then the answer cites metric `revenue`, the two periods, and matches the explorer to the cent.
- **AC-47.10** (FR-47.13) Given provider switched to Bedrock, when the AP coding eval runs, then thresholds are still met.

### 9.47.9 Test plan and fixtures

Fixtures: `eval-ap-coding-v3` (2,000 labeled invoices, 40 vendors, 3 entities, from wave-19 fixtures plus hand labels); `eval-cash-app-v2` (1,500 payments with remittances, 12% partials, 5% multi-invoice); `eval-mrp-triage-v1` (800 action messages); `eval-anomaly-v1` (50k journals, 400 injected anomalies); `eval-ask-data-v1` (300 questions with expected Cube queries); `adv-prompts-500` (injection, exfiltration, policy bypass).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Permission inheritance | contract | gateway + 400 tools | 0 cases where agent exceeds user |
| Tool schema conformance | contract | gateway | every tool validates against MCP spec and `x-passage-*` |
| Agent evals | eval | 5 agents | thresholds in FR-47.9 |
| Adversarial refusal | eval | copilot | ≥ 0.99 correct refusals |
| Redaction scanner | unit/property | redaction | 0 PII patterns in stored prompts across 100k generated samples |
| Kill switch latency | E2E | runtime | ≤ 10 s |
| Run throughput | performance | runtime | 500 concurrent runs, p95 < 30 s |
| Provider parity | contract | 4 adapters | identical tool-use round trips |

Reconciliation: tokens billed by provider ↔ `agent_run.tokens_*` within 1%; `agent_action` COMMITTED count ↔ posted documents tagged `origin=agent`.

### 9.47.10 Deployment and operations

Helm: `passage-mcp-gateway` (TS, 3 replicas), `passage-agent-runtime` (TS workers on Temporal namespace `agents`), `passage-copilot-bff`, `passage-agent-evals` (Python job runner). Config: `MODEL_PROVIDER_DEFAULT=anthropic`, `MODEL_ID` pinned per agent version, provider keys in Vault, `TOKEN_BUDGET_DEFAULT_IN=50000000`. Flags: `copilot.enabled`, `agent.<id>.mode` (off/suggest/auto) per tenant and legal entity. Jobs: budget rollover monthly, eval nightly on production datasets, model-card export weekly. SLOs: copilot first-token p95 < 2 s; run success rate ≥ 99%; gateway availability 99.9%. Alerts: eval regression, budget 80/100%, provider error rate > 5%, kill switch toggled. Runbook: rotate provider keys, replay a run in sandbox, demote version. Rollback: `promote` previous `agent_version`; Helm rollback.

### 9.47.11 Migration from AX / SAP

| Source | Passage | Notes |
|---|---|---|
| Power Automate flows on business events | Agent or Temporal workflow definitions | catalogued; rebuilt by hand; the business-event catalog maps to CloudEvents types |
| `VendInvoiceAutomation` parameters | AP coding agent thresholds | tolerance and auto-post limits imported |
| `CustPaymMatch` rules | Cash application agent rules | matching rules become deterministic pre-filters |
| SAP Build Process Automation projects | Agent definitions | not convertible; documented |
| Joule / D365 Copilot settings | Copilot flags | n/a |

Not migrated: conversation histories, Copilot usage data (no business value).

### 9.47.12 Dependencies, risks, open questions

Depends on: wave 46 (semantic layer for ask-the-data), wave 4 (Temporal, workflow inbox), wave 2 (OPA duties, token exchange), wave 19/30 (AP invoices, cash application APIs), wave 42 (PII classification).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Prompt injection via invoice text | High | High | tool allow-list per agent; adversarial eval; `approve_before_commit` |
| Eval datasets overfit to demo data | Medium | Medium | customer-labeled holdout from wave 49 parallel run |
| Provider cost spikes | Medium | Medium | budgets; prompt caching; smaller models for classification |

Open questions: (1) Auto mode for AP coding in v1 for amounts below threshold — proposed default on, decide by wave 49 with first customer. (2) Open-weights model as residency fallback for DE tenants — default: vLLM adapter shipped, not certified; decide by wave 50.

## 9.48 Wave 48: Globalization, Localization & Compliance Controls

### 9.48.1 Scope and business context

AX delivers country/region localizations, Electronic Reporting (`ERModel`, `ERFormat`, `ERModelMapping` configurations from the Regulatory Configuration Service), segregation of duties (`SysSecSegregationOfDutiesRule`, conflict table `SysSecSegregationOfDutiesConflict`) and `SysDatabaseLog`. SAP delivers country versions, Document and Reporting Compliance (e-documents, statutory reports), GRC Access Control rulesets with risk ids, and the Audit Information System. This wave adds a localization framework with seven country packs, an electronic reporting designer with a mandates registry, an SoD engine with mitigation and access certification, security hardening with SOC 2 control mapping and evidence automation, and GDPR/CCPA subject requests with retention and legal holds. Out of scope: additional countries (v1.x), payroll localizations beyond wave 43, ISO 27001 certification itself (evidence only).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| A regulator changes an XML schema; the ER configuration must be re-imported from RCS/LCS, tested in a sandbox and deployed, or an SAP note applied via `SNOTE` with a transport | Formats bundled with the application release cycle | Mandates registry with effective-dated `er_format` versions, schema validation in CI | Schema update 3 weeks → 1 day; 0 regulator rejections for schema errors |
| SoD conflicts in `SysSecSegregationOfDutiesConflict` are found after assignment; GRC AC risk analysis runs as a batch and mitigations live in a separate system | Detection decoupled from role assignment | `sod/simulate` before save; nightly evaluation; mitigation workflow in the same workspace | Detection latency 1 day batch → < 2 s at assignment; unmitigated HIGH conflicts 0 |
| SOC 2 evidence is screenshots and exports gathered by hand each audit (realm settings, backup logs, scan reports) | No system of record for controls | Control library with scheduled collectors and hashed artifacts | Evidence effort 20 person-days/audit → 1; 100% evidence machine-collected |
| GDPR requests require searching every table by hand (person search report in AX, IRF in SAP) and erasure risks breaking postings | PII not classified; no fan-out workflow | Classification-driven discovery, Temporal fan-out per domain, pseudonymization | Request fulfillment 25 days → 2; 0 broken financial references |
| `SysDatabaseLog` slows posting and fills the database; `CDHDR/CDPOS` is incomplete for custom tables | Application-side audit writes | CDC-derived audit, configuration only chooses what to surface | Posting overhead from audit 15% → 0%; 100% field coverage |

### 9.48.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Country localization | Country/region-specific features gated by `CountryRegionCodes`, Localization feature keys | Country versions, `T005` country settings, localization BC sets | `country_pack` with tax, reports, layouts, bank formats, calendars | Full (7 countries) |
| Electronic reporting | ER `ERModel`, `ERFormat`, `ERModelMapping`, RCS, `ERFormatMappingRun` | DRC e-documents (`EDOC_COCKPIT`), statutory reports (`SRF`), Advanced Compliance Reporting | ER designer: `er_model`, `er_mapping`, `er_format` with mandates registry | Full |
| E-invoicing mandates | Electronic invoicing service (Italy SDI, Mexico CFDI) | DRC eDocument Framework (SDI, CFDI, Peppol, India IRN) | Mandate adapters: MX CFDI 4.0, IN IRP e-invoice, Peppol BIS 3 (wave 35), UK MTD VAT | Full for first set |
| Statutory tax reports | Sales tax reporting, EU sales list, Intrastat (`Intrastat` form) | `RFUMSV00` VAT return, ESL (`RFASLM00`), Intrastat (`VE01`) | ER formats: US sales tax, UK MTD VAT return, DE UStVA/ZM, FR CA3/DEB, IN GSTR-1/3B, MX DIOT, CA GST/HST | Full |
| Segregation of duties | `SysSecSegregationOfDutiesRule`, conflict detection, `SysSecSegregationOfDutiesConflict` resolution | GRC Access Control rulesets, risk ids (e.g. P001, F002), mitigating controls, UAR | `sod_rule`, `sod_conflict`, `mitigation`, `certification_campaign` | Improved (campaigns included) |
| Audit log | `SysDatabaseLog`, `DatabaseLog` form | `CDHDR/CDPOS`, AIS (`SECR`), `SM20` security audit log | wave-5 CDC audit + compliance evidence views | Full |
| Security compliance | Microsoft-managed SOC reports | SAP-managed for RISE; customer for on-prem | SOC 2 control mapping with automated evidence | Improved (customer-visible) |
| Data privacy | Person search report, GDPR request form (`GDPR request`) | Information Retrieval Framework (IRF), ILM retention (`IRM_CUST`), blocking | `subject_request`, `retention_policy`, `legal_hold` | Full |
| Data residency | LCS region selection | RISE region | tenant residency policy enforced on storage, lakehouse, AI provider | Full |

### 9.48.3 Functional requirements

- **FR-48.1 Country pack framework** — Must. The system shall package per-country tax rules, statutory report formats, document layouts, bank formats (NACHA, BACS, SEPA CT/DD, NEFT/RTGS, SPEI, EFT), holiday calendars and address/tax-id validators as a versioned `country_pack`, installable per legal entity.
  - Notes: packs are metadata plus ER formats, never code forks; a legal entity has exactly one primary pack; pack upgrade is a versioned migration with preview.
- **FR-48.2 First seven packs** — Must. US (sales/use tax, 1099-NEC/MISC, ACH), UK (MTD VAT, BACS, Companies House XBRL tags), DE (UStVA, ZM, SEPA, GoBD export, DATEV), FR (CA3, DEB, FEC file, SEPA), IN (GST returns GSTR-1/3B, e-invoice IRP, TDS), MX (CFDI 4.0 with PAC, DIOT, SPEI), CA (GST/HST/PST, EFT, T4A).
- **FR-48.3 Electronic reporting designer** — Must. The system shall provide `er_model` (logical data model), `er_mapping` (Passage entities → model), `er_format` (XML/JSON/CSV/fixed-width output with XSD/JSON-schema validation), versioned with effective dates and a mandates registry (`mandate`: regulator, jurisdiction, schema version, effective_from, sunset).
  - Notes: format execution is a Temporal job; every output is stored with hash and regulator acknowledgment; schema validation failure blocks submission.
- **FR-48.4 SoD rules engine** — Must. The system shall evaluate rules of the form `duty_a CONFLICTS duty_b` (and privilege-level) against every user's effective roles on each assignment change and nightly, raising conflicts with severity.
  - Rule schema: `rule_id`, `name`, `duty_a`, `duty_b`, `severity` (HIGH/MEDIUM/LOW), `risk_id` (GRC-style, e.g. `P2P-001`), `description`, `mitigation_allowed` bool, `max_mitigation_days` (default 365), `status`.
  - Seed rules (10 of 50): `P2P-001` MaintainVendors ↔ ProcessVendorPayments (HIGH); `P2P-002` CreatePurchaseOrders ↔ ApprovePurchaseOrders (HIGH); `P2P-003` PostVendorInvoices ↔ ApproveVendorInvoices (HIGH); `P2P-004` ReceiveGoods ↔ PostVendorInvoices (MEDIUM); `O2C-001` MaintainCustomers ↔ PostCustomerPayments (HIGH); `O2C-002` CreateSalesOrders ↔ ApproveCreditLimits (HIGH); `O2C-003` IssueCreditNotes ↔ PostCustomerPayments (HIGH); `GL-001` PostGeneralJournals ↔ ApproveGeneralJournals (HIGH); `GL-002` MaintainChartOfAccounts ↔ PostGeneralJournals (MEDIUM); `SEC-001` MaintainUsersAndRoles ↔ any posting duty (HIGH); `HR-001` MaintainWorkerBankDetails ↔ RunPayroll (HIGH).
- **FR-48.5 Mitigation workflow** — Must. The system shall allow a conflict to be mitigated with a compensating control, approver (not the conflicted user), reason, and expiry ≤ `max_mitigation_days`; unmitigated HIGH conflicts block role assignment when tenant policy `sod.block_high=true`.
- **FR-48.6 Access certification campaigns** — Should. The system shall run campaigns where managers certify or revoke each direct report's roles within a window (default 14 days), auto-revoking uncertified roles at close when policy says so, with evidence export.
- **FR-48.7 SOC 2 control mapping and evidence** — Must. The system shall ship a control library mapped to TSC and automate evidence collection on a schedule.
  - Excerpt: CC6.1 logical access — evidence: Keycloak realm export, OPA bundle hash, RLS policy list (daily); CC6.2 provisioning — SCIM join/leave log, certification results (weekly); CC6.3 role changes — `sod_conflict` report, role-assignment audit (daily); CC6.6 boundary protection — Envoy Gateway TLS config, NetworkPolicy manifests (daily); CC6.7 data in transit/at rest — cert-manager inventory, CNPG encryption settings (weekly); CC6.8 malware/unauthorized software — image signature verification logs, SBOM diff (per deploy); CC7.1 vulnerability detection — Trivy scan results, pen-test tracker (daily/quarterly); CC7.2 anomaly monitoring — alert rule inventory, journal-anomaly agent summary (daily); CC7.3 incident evaluation — incident records with severity and timeline (per incident); CC7.4 incident response — post-incident review, runbook link (per incident); CC7.5 recovery — backup/restore drill results, RPO/RTO measured (monthly).
- **FR-48.8 Security hardening** — Must. The system shall rotate all secrets ≤ 90 days via Vault, enforce signed images, run Trivy and dependency scanning per build, remediate external pen-test findings (high/critical = 0 at exit), and enforce tenant data residency (region for Postgres, object store, lakehouse, AI provider).
- **FR-48.9 Backup and restore** — Must. The system shall take continuous WAL archives and daily base backups per domain cluster (CloudNativePG), support point-in-time restore of one tenant into an isolated namespace, and run a monthly drill measuring RPO ≤ 5 min and RTO ≤ 1 h.
- **FR-48.10 Subject requests** — Must. The system shall process GDPR/CCPA requests of type ACCESS, PORTABILITY, ERASURE, RECTIFICATION, RESTRICTION with the flow: intake (portal or admin) → identity verification (two attributes + e-mail link) → scope discovery (search party, worker, user, contact across all domains via the PII classification) → legal-hold and retention check → domain fan-out (Temporal child workflow per domain) → compile (ACCESS/PORTABILITY: JSON + PDF bundle) or apply (ERASURE: pseudonymize PII fields, keep financial records with `erased=true`) → approval by privacy officer → deliver via signed link → close within 30 days (GDPR) / 45 days (CCPA), with the deadline tracked.
- **FR-48.11 Retention and legal hold** — Must. The system shall apply retention policies per record class (e.g. financial documents 10 years DE, 7 years US; HR 6 years post-termination; logs 1 year) and legal holds that suspend erasure and retention purge for named scopes.
- **FR-48.12 Database log equivalent** — Should. The system shall offer `SysDatabaseLog`-style configuration: pick entities/fields to surface in the compliance audit view, backed by the wave-5 CDC audit, with no extra write path.

### 9.48.4 Data model

**`sod_rule`** (sod)

| Field | Type | Required | Notes |
|---|---|---|---|
| rule_id | text | Y | PK, e.g. `P2P-001` |
| duty_a / duty_b | text | Y | OPA duty ids; `*POST*` wildcard allowed |
| severity | enum | Y | HIGH, MEDIUM, LOW |
| risk_id | text | Y | GRC-style id |
| mitigation_allowed | bool | Y | default true |
| max_mitigation_days | int | Y | default 365 |
| status | enum | Y | ACTIVE, RETIRED |
| version | int | Y | |

**`sod_conflict`**: conflict_id, tenant_id, user_id, rule_id, roles_json (which roles grant each duty), severity, detected_at, state (OPEN, MITIGATED, RESOLVED, ACCEPTED_RISK), mitigation_id, resolved_at.

**`er_format`** (ereporting)

| Field | Type | Required | Notes |
|---|---|---|---|
| format_id | uuid | Y | PK |
| mandate_id | uuid | Y | FK mandate |
| version | int | Y | |
| output_type | enum | Y | XML, JSON, CSV, FIXED, PDF |
| schema_ref | text | N | XSD/JSON schema location, hash |
| template_json | jsonb | Y | element tree with bindings |
| effective_from / effective_to | date | Y/N | |
| status | enum | Y | DRAFT, PUBLISHED, SUPERSEDED |

**`subject_request`**: request_id, tenant_id, type, subject_ref (party/worker/user), verified_at, deadline_at, state (RECEIVED, VERIFIED, DISCOVERED, HELD, APPROVED, DELIVERED, CLOSED, REJECTED), domains_json, bundle_ref, privacy_officer_id.

**`control_evidence`**: evidence_id, control_id (e.g. `CC6.1`), collected_at, collector, artifact_ref (object store), hash, period_start, period_end, status (COLLECTED, REVIEWED, EXCEPTION).

Invariants: a user cannot approve their own mitigation; a PUBLISHED `er_format` with a schema_ref has a validated fixture; an ERASURE cannot complete while a legal hold covers the subject; `control_evidence.hash` matches the stored artifact.
Indexing: `sod_conflict` (tenant_id, state, severity); `subject_request` (tenant_id, deadline_at); `control_evidence` (control_id, collected_at).

### 9.48.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | `/api/v1/localization/packs/{code}/install` | install pack on legal entity | by (entity, version) | LocalizationAdmin |
| POST | `/api/v1/ereporting/formats/{id}/run` | execute format for period | `Idempotency-Key` | per report duty |
| POST | `/api/v1/ereporting/submissions/{id}/submit` | submit to regulator adapter | by submission id | per report duty |
| GET | `/api/v1/sod/conflicts?state=OPEN` | list conflicts | n/a | SoDReview |
| POST | `/api/v1/sod/conflicts/{id}/mitigations` | create mitigation | `Idempotency-Key` | SoDApprove |
| POST | `/api/v1/sod/simulate` | simulate role assignment | n/a | SecurityAdmin |
| POST | `/api/v1/sod/campaigns` | start certification campaign | `Idempotency-Key` | SoDAdmin |
| GET | `/api/v1/compliance/controls/{id}/evidence?period=` | evidence listing | n/a | ComplianceView |
| POST | `/api/v1/privacy/requests` | intake subject request | `Idempotency-Key` | PrivacyIntake or portal |
| POST | `/api/v1/privacy/holds` | create legal hold | `Idempotency-Key` | LegalHoldAdmin |
| POST | `/api/v1/ops/backups/restore-drill` | run PITR drill into namespace | by drill id | PlatformAdmin |

Validation: pack install rejects if the entity's fiscal calendar is open-period incompatible; `sod/simulate` returns conflicts before assignment is saved; a `run` of a format outside `effective_from..to` is rejected.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.ereporting.document.submitted.v1` | regulator ack | mandate_id, format_version, document_ref, ack_id, status | AR/AP (mark invoice sent), compliance |
| `dev.passage.sod.conflict.detected.v1` | evaluation | user_id, rule_id, severity, roles[] | workflow, compliance |
| `dev.passage.sod.mitigation.approved.v1` | approval | conflict_id, approver, expires_at | audit |
| `dev.passage.privacy.request.completed.v1` | close | request_id, type, affected_domains[], delivered_at | audit |
| `dev.passage.compliance.evidence.collected.v1` | collector | control_id, evidence_id, hash | compliance UI |

### 9.48.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Country packs | install, upgrade preview, view contents | code, version, entity, components | LocalizationAdmin |
| ER designer | edit model/mapping/format, validate against schema, run with fixture | element tree, binding, schema status | ERDesign |
| Mandates registry | add mandate, set effective dates, link formats | regulator, jurisdiction, schema version | ERDesign |
| SoD conflicts | mitigate, resolve, accept risk, export | user, rule, severity, roles, age | SoDReview/SoDApprove |
| Role assignment (wave 2) | simulate conflicts before save | conflicts preview | SecurityAdmin |
| Certification campaign | certify/revoke per user, progress | manager, user, roles, decision | Manager |
| Compliance workspace | evidence calendar, exceptions, export audit package | control, last evidence, status | ComplianceView |
| Privacy requests | verify, review scope, approve, deliver | type, subject, deadline, state | PrivacyOfficer |

Rules: SoD grid supports bulk mitigation ≤ 25 with one approver; evidence exports as a ZIP with manifest hash; privacy request deadline shows days remaining with red at ≤ 5.

### 9.48.7 Business rules and edge cases

- **BR-48.1** Conflict state machine: OPEN → MITIGATED (expires → OPEN) | RESOLVED (role removed) | ACCEPTED_RISK (LOW/MEDIUM only, by SoDAdmin).
- **BR-48.2** Conflicts are evaluated on effective duties including inherited roles and temporary delegations; a delegation that creates a HIGH conflict is refused.
- **BR-48.3** ER format outputs are immutable once submitted; corrections create a new submission with `supersedes`.
- **BR-48.4** Tax-period statutory runs lock the source tax settlement; re-running requires reversal of the settlement (wave 13).
- **BR-48.5** Erasure pseudonymizes `name`, `address`, `email`, `phone`, `tax_id`, `bank_account`, `date_of_birth` fields; monetary documents keep amounts, dates and dimension values.
- **BR-48.6** Retention purge runs monthly and skips any record under hold, in an open fiscal period, or referenced by an open document.
- **BR-48.7** Residency: a tenant with `residency=EU` cannot be assigned an AI provider endpoint outside the EU; the API rejects the configuration.
- **BR-48.8** Backup drill failing RPO or RTO opens a Sev-2 incident automatically.
- **BR-48.9** Certification campaign close: uncertified roles are revoked only when `campaign.auto_revoke=true`; otherwise flagged.
- **BR-48.10** Country-pack upgrade with a changed tax rate effective-dates the new rate; it never rewrites posted tax.

### 9.48.8 Acceptance criteria

- **AC-48.1** (FR-48.2, 48.3) Given each of the 7 packs' statutory fixture, when the format runs, then the output validates against the regulator schema with 0 errors.
- **AC-48.2** (FR-48.4) Given 50 users with planted conflicting role combinations, when evaluation runs, then 50 conflicts are detected with the correct rule ids and 0 false negatives.
- **AC-48.3** (FR-48.5) Given an OPEN HIGH conflict and `sod.block_high=true`, when the role assignment is saved, then it is rejected with the rule id.
- **AC-48.4** (FR-48.5) Given a mitigation, when approved by a non-conflicted approver with reason and 180-day expiry, then the conflict is MITIGATED and `mitigation.approved` is emitted.
- **AC-48.5** (FR-48.9) Given a tenant with 10 GB across domains, when a PITR drill to T−20 min runs, then the restored namespace is queryable within 1 h and the lost-data window ≤ 5 min.
- **AC-48.6** (FR-48.10) Given an ACCESS request for a customer contact, when processed, then the bundle contains data from every domain holding the subject and the request closes within 30 days.
- **AC-48.7** (FR-48.10, 48.11) Given an ERASURE request for a party under legal hold, when processed, then state is HELD and no field is changed.
- **AC-48.8** (FR-48.7) Given control CC7.1, when the daily collector runs, then a Trivy report evidence record exists with a matching hash.
- **AC-48.9** (FR-48.8) Given the external pen-test report, when wave exit is reviewed, then open high/critical findings = 0.
- **AC-48.10** (FR-48.1) Given the DE pack installed on DEMF, when an invoice is printed, then the layout uses `de-DE` formats and the UStVA mapping includes the invoice's tax lines.

### 9.48.9 Test plan and fixtures

Fixtures: `fx-statutory-7` (one period of postings per country with expected outputs and regulator schemas: UK HMRC MTD VAT JSON, DE ELSTER UStVA XML, FR FEC, IN GSTR JSON, MX CFDI 4.0 XSD, US 1099 IRS FIRE, CA GST34 fields); `fx-sod-50` (50 planted users); `fx-privacy-subjects` (20 subjects spanning 9 domains); `fx-residency` (3 tenants EU/US/IN).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Schema validation | contract | 7 formats | 0 schema errors |
| SoD detection | unit/property | engine | 50/50; random role sets never miss a seeded conflict |
| Mitigation workflow | E2E | sod | approver ≠ user enforced; expiry reopens |
| Privacy fan-out | E2E | privacy | all domains respond; bundle complete |
| PITR drill | performance | CNPG | RPO ≤ 5 min, RTO ≤ 1 h |
| Secrets rotation | contract | Vault | all leases ≤ 90 days |
| Residency enforcement | contract | config API | cross-region assignment rejected |
| Evidence collectors | unit | 11 controls | artifact + hash per schedule |

Reconciliation: statutory VAT return boxes ↔ tax settlement totals (wave 13) to the cent; conflicts count ↔ independent recomputation from role assignments; evidence count ↔ schedule expectations.

### 9.48.10 Deployment and operations

Helm: `passage-localization`, `passage-ereporting` (TS + Temporal), `passage-sod` (Go), `passage-compliance`, `passage-privacy`. Config: `COUNTRY_PACKS_ENABLED`, regulator adapter credentials (HMRC, PAC, IRP) in Vault, `SOD_BLOCK_HIGH`, `PRIVACY_DEADLINE_DAYS`. Migrations: `compliance` and `localization` databases. Flags per tenant: `pack.<code>`, `sod.enforce`, `privacy.portal`. Jobs: SoD nightly 00:30, evidence collectors per control schedule, retention purge monthly, backup drill monthly, secrets rotation. SLOs: format run p95 < 5 min for 100k lines; SoD evaluation < 2 s per assignment. Alerts: schema validation failure, regulator rejection, HIGH conflict unmitigated > 7 days, drill failure. Rollback: pack version downgrade with preview; ER format supersede; Helm rollback.

### 9.48.11 Migration from AX / SAP

| Source | Passage | Notes |
|---|---|---|
| `SysSecSegregationOfDutiesRule` | `sod_rule` | duties mapped via wave-2 role mapping; severity imported |
| `SysSecSegregationOfDutiesConflict` with resolution | `sod_conflict` + `mitigation` | open conflicts re-evaluated; mitigations imported with original approver and expiry |
| GRC AC rulesets (risk ids, functions, actions) | `sod_rule` | function→duty mapping table; mitigating controls → `mitigation` |
| ER configurations (`ERModel`, `ERFormat` XML exports) | `er_model`, `er_format` | importer parses ER XML; formulas in ER expression language are translated where possible, else flagged |
| `SysDatabaseLog` configuration | FR-48.12 configuration | table/field list |
| `CDHDR/CDPOS` history | wave-49 lakehouse history | read-only |
| GDPR request logs | `subject_request` history | import closed requests for evidence |

Not migrated: Microsoft/SAP SOC reports (vendor-owned), regulator credentials (re-issued).

### 9.48.12 Dependencies, risks, open questions

Depends on: wave 2 (roles/duties, OPA), wave 5 (CDC audit), wave 13 (tax), wave 35 (Peppol, e-invoicing transport), wave 42 (PII classification), wave 46 (evidence from reconciliation).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Regulator schema changes mid-wave | High | Medium | mandates registry with effective dates; schema fixtures pinned |
| SoD rule set incomplete for customer | Medium | High | 50-rule seed plus GRC import |
| Erasure breaks referential integrity | Medium | High | pseudonymize, never delete rows |

Open questions: (1) PAC provider for MX (default: Finkok sandbox); decide by wave 49. (2) Whether to pursue SOC 2 Type I before go-live — proposed yes, readiness assessment by wave 50.

## 9.49 Wave 49: Migration Factory: Full AX/SAP Transactional Cutover Tooling

### 9.49.1 Scope and business context

AX 2012 → D365 uses the data upgrade scripts (`ReleaseUpdateDB` classes, LCS data-upgrade deployable package) and DIXF; SAP uses SUM with DMO for conversion, the Migration Cockpit (`LTMC`/`LTMOM`, staging tables) and Selective Data Transition. None of them moves a customer to a different ERP. Building on the wave-10 framework (connectors, declarative mapping, idempotent runs), this wave adds transactional packs with control totals, historical loads into the lakehouse with legacy cross-references, a parallel-run harness, a cutover runbook generator and coexistence adapters. Out of scope: code migration (X++/ABAP), custom-table migration beyond the mapping DSL, SAP archive (ADK) files.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Open items loaded through DIXF (`DMFDefinitionGroup`) or Migration Cockpit staging tables (`/1LT/DS*`) are reconciled in Excel against `VendTransOpen`/`BSIK` totals; mismatches found days later | No built-in control totals at sub-total grain | `control_total` per pack at (entity, account, currency) grain with hashes, computed in-run | Reconciliation 5 person-days per entity → 10 min automated; mismatches found in-run |
| Re-running a failed load creates duplicates (`InventSum` doubled, duplicate `BKPF` opening documents) | Loaders without natural-key idempotency | Idempotent packs keyed on `legacy_xref` | Duplicate rate after re-run 0 |
| Parallel runs compare printed trial balances by eye; variances are explained in e-mail threads | No diff tooling | Normalized tuple diff with EXPECTED/EXPLAINED/UNEXPLAINED classification and sign-off | Variance review 3 weeks → 2 days; 0 unexplained at sign-off |
| Cutover plans are spreadsheets; step durations are guesses; rollback is untested | No runbook tooling tied to the data loads | Generated `cutover_plan` with timed steps from rehearsals, go/no-go gates, rehearsed rollback | Cutover < 48 h; 2 rehearsals; rollback rehearsed |
| Users lose the legacy voucher/document id after migration and keep the old system online for lookups | No cross-reference model | `legacy_xref` on every record and in the lakehouse history | Legacy lookups needing the old system 100% → 0 |

### 9.49.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Open AP/AR items | `VendTrans`/`VendTransOpen`, `CustTrans`/`CustTransOpen`, `VendSettlement` | `BSIK`/`BSID` (ECC), `ACDOCA` open items (S/4), `BSEG` | pack `open_items` → AP/AR open items with `legacy_xref` | Full |
| GL balances | `GeneralJournalAccountEntry`, `DimensionAttributeValueCombination`, `LedgerBalance*` | `GLT0`/`FAGLFLEXT` (ECC), `ACDOCA` (S/4) | pack `gl_balances` → opening journal per dimension | Full |
| On-hand inventory | `InventSum`, `InventTrans`, `InventDim`, `InventTransOrigin` | `MARD`, `MCHB` (batch), `MSKA`, `MBEW` valuation | pack `onhand` → inventory opening transactions by dimension | Full |
| Open POs / SOs | `PurchTable`/`PurchLine`, `SalesTable`/`SalesLine` with remaining qty | `EKKO`/`EKPO`/`EKET`, `VBAK`/`VBAP`/`VBEP` | pack `open_orders` → orders with delivered/invoiced quantities | Full |
| In-process production | `ProdTable`, `ProdBOM`, `ProdRoute`, `ProdJournalProd` | `AFKO`/`AFPO`/`AFVC`, `AUFK` | pack `wip_prod` → orders at status with consumed/reported quantities and WIP value | Full |
| Fixed assets | `AssetTable`, `AssetBook`, `AssetTrans` | `ANLA`/`ANLB`/`ANLC`/`ANEP` | pack `fixed_assets` → NBV per book with depreciation profile | Full |
| Project WIP | `ProjTable`, `ProjTrans*`, `ProjWIP*` | `PRPS`, `COSP`/`COSS`, RA results | pack `project_wip` | Full |
| Employee/payroll YTD | `HcmWorker`, `PayrollEarningStatement*` | `PA0001`/`PA0008`, payroll results cluster (`PCL2`) | pack `payroll_ytd` | Partial (US + DE only) |
| Upgrade tooling | AX 2012→D365 data upgrade (LCS), DIXF | SUM/DMO, Migration Cockpit, SDT | migration factory packs + runbook generator | Improved (cross-vendor) |
| History | Legacy database kept online | Archive / SDT history | lakehouse `history.*` with `legacy_xref` | Full (read-only) |

### 9.49.3 Functional requirements

- **FR-49.1 Transactional packs** — Must. The system shall provide packs `open_items`, `gl_balances`, `onhand`, `open_orders`, `wip_prod`, `fixed_assets`, `project_wip`, `payroll_ytd`, `bank_balances`, each with AX and SAP extractors, mapping, validation (Great Expectations suites), load and reconciliation steps.
- **FR-49.2 Control totals** — Must. The system shall compute and compare per pack: `open_items`: count, sum amount in transaction currency per currency, sum in accounting currency, per (entity, account, currency) sub-totals, hash of sorted (legacy_id, amount); `gl_balances`: per (entity, account, dimension combination, currency) balance, trial balance debit = credit, count of non-zero combinations; `onhand`: per (item, site, warehouse, location, batch, serial) quantity and per (item, site) value, total value per entity; `open_orders`: order count, line count, remaining quantity, remaining amount; `wip_prod`: order count, WIP value per order; `fixed_assets`: count, acquisition cost, accumulated depreciation, NBV per book; `project_wip`: WIP value per project; `payroll_ytd`: gross, tax, net per worker; `bank_balances`: per account.
  - Notes: every total must match exactly (0.00) unless a tolerance rule is explicitly configured with reason; totals are stored on `control_total` with source and target values and timestamp.
- **FR-49.3 Legacy cross-reference** — Must. The system shall store `legacy_xref` (system, company, table, legacy_id → passage entity, id) for every migrated record and expose it on every record detail and in the lakehouse.
- **FR-49.4 Historical load** — Must. The system shall load closed legacy transactions (journals, invoices, inventory transactions, orders) for N years (default 7) into Iceberg `history.<system>.*` with the legacy schema preserved, mapped views `history.unified.*` on the Passage model, queryable in explorer with `source=legacy` filter.
- **FR-49.5 Parallel-run harness** — Must. The system shall replay a legacy period's source documents (vendor invoices, customer invoices, inventory movements, journals, production postings) into a Passage rehearsal tenant through the public posting APIs in original sequence and compare results.
  - Diff algorithm: (1) normalize both sides to canonical tuples `(entity, account, dimension set, currency, period, amount)` for trial balance, `(item, dims, qty, value)` for inventory, `(subledger, open item id, amount)` for subledgers; (2) outer-join on key; (3) classify each difference: EXPECTED (rule-based: known rounding ≤ 0.01, policy differences registered in `variance_rule`, e.g. different FX rounding step), EXPLAINED (operator attaches a reason), UNEXPLAINED; (4) aggregate by rule; (5) report unexplained count and amount; (6) exit criterion unexplained = 0 and explained amount ≤ 0.1% of period revenue.
- **FR-49.6 Cutover runbook generator** — Must. The system shall generate `cutover_plan` with timed steps from T−14d to T+30d from the selected packs, entities and legacy systems, with owners, durations from the last rehearsal, go/no-go items and rollback steps.
  - Timeline baseline: T−14d freeze master-data changes in legacy, final pack dry run; T−7d second rehearsal, sign-off on variance report; T−3d freeze configuration in Passage; T−1d 18:00 legacy transaction freeze, final extracts of master data and static reference data; T−0 00:00 extract transactional packs; T+4h load and reconcile master and balances; T+12h load open items, orders, on-hand, WIP; T+24h reconciliation complete, go/no-go meeting; T+30h user access opened; T+36h abort deadline if go/no-go fails; T+1d first postings, hypercare starts; T+7d first weekly reconciliation; T+30d first month-end close, hypercare review.
- **FR-49.7 Coexistence adapters** — Should. The system shall support entity-by-entity cutover with intercompany documents and consolidation data exchanged with the legacy system through wave-35 integration hub adapters (AX OData/`DMF` entities; SAP IDoc/BAPI) for `INVOIC`, `ORDERS`, GL postings, and master-data sync one way (legacy → Passage) until the last entity cuts over.
- **FR-49.8 Rehearsal environment** — Must. The system shall provision a rehearsal tenant from pipeline (Terraform + Helm + seed) in ≤ 2 h, with masked extracts, and tear it down.
- **FR-49.9 Rollback plan** — Must. The system shall generate a rollback plan per cutover: stop Passage access, restore legacy write access, export any Passage postings made after go-live as journal/document files for re-keying, with a decision point at T+36h.
- **FR-49.10 Idempotent re-runs** — Must. The system shall re-run any pack without duplicates (wave-10 natural keys plus `legacy_xref` uniqueness) and record every run with inputs' hashes.
- **FR-49.11 Validation suites** — Must. The system shall run Great Expectations suites per pack before load: referential (every vendor on open items exists), domain (currency codes ISO 4217), balance (debit = credit), uniqueness; failures above 0 block load unless the record is quarantined to `pack_quarantine` with reason.

### 9.49.4 Data model

**`pack_run`** (migration-factory)

| Field | Type | Required | Notes |
|---|---|---|---|
| run_id | uuid | Y | PK = Temporal workflow id |
| tenant_id | uuid | Y | |
| pack | enum | Y | the 9 packs |
| source_system | enum | Y | AX2012, D365FO, SAPECC, S4HANA |
| source_company | text | Y | `DataAreaId` / `BUKRS` |
| cutoff_ts | timestamptz | Y | extraction cutoff |
| input_hash | text | Y | sha256 of extract |
| status | enum | Y | EXTRACTED, VALIDATED, LOADED, RECONCILED, FAILED, ROLLED_BACK |
| records_in / records_loaded / records_quarantined | int | Y | |

**`control_total`**: total_id, run_id, name, key_json (e.g. {entity, account, currency}), source_value numeric(28,6), target_value numeric(28,6), delta, tolerance, status (MATCH, WITHIN_TOLERANCE, MISMATCH), computed_at.

**`legacy_xref`**: xref_id, tenant_id, source_system, source_company, source_table, source_id, passage_entity, passage_id, run_id; unique (source_system, source_company, source_table, source_id).

**`parallel_run`**: parallel_run_id, tenant_id, period, source_system, documents_replayed, state (REPLAYING, DIFFING, REVIEW, SIGNED_OFF), unexplained_count, unexplained_amount, explained_amount, signed_off_by. **`variance`**: variance_id, parallel_run_id, area (TB/INV/AP/AR/FA), key_json, legacy_value, passage_value, delta, class (EXPECTED/EXPLAINED/UNEXPLAINED), rule_id, reason.

**`cutover_step`**: step_id, plan_id, seq, offset_minutes (from T−0), name, owner, planned_duration_min, actual_start, actual_end, status, rollback_step_id, go_nogo bool.

Invariants: a pack run reaches RECONCILED only if every `control_total.status != MISMATCH`; `legacy_xref` is append-only; a `parallel_run` can be SIGNED_OFF only when unexplained_count = 0; cutover steps with `go_nogo=true` must all be complete before the "open user access" step can start.
Indexing: `legacy_xref` unique index as above plus (passage_entity, passage_id); `control_total` (run_id, status); `variance` (parallel_run_id, class).

### 9.49.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | `/api/v1/migration/packs/{pack}/runs` | start extract/validate/load | by input_hash | MigrationRun |
| GET | `/api/v1/migration/runs/{id}/control-totals` | totals and status | n/a | MigrationView |
| POST | `/api/v1/migration/runs/{id}/quarantine/{rec}/release` | fix and release quarantined record | by record | MigrationRun |
| POST | `/api/v1/migration/history/loads` | start historical load | by (system, years) | MigrationRun |
| GET | `/api/v1/migration/xref?system=&table=&id=` | resolve legacy id | n/a | any authenticated |
| POST | `/api/v1/migration/parallel-runs` | start replay for period | by (period, system) | MigrationRun |
| POST | `/api/v1/migration/parallel-runs/{id}/variances/{v}/explain` | attach reason | by variance | MigrationReview |
| POST | `/api/v1/cutover/plans/generate` | generate plan from packs | by inputs hash | CutoverManager |
| POST | `/api/v1/cutover/plans/{id}/steps/{s}/complete` | mark step done | by step | step owner |
| POST | `/api/v1/cutover/plans/{id}/rollback` | execute rollback plan | by plan | CutoverManager + second approver |

Validation: pack run rejects extracts with `cutoff_ts` later than the legacy freeze; `complete` rejects out-of-sequence go/no-go steps; `rollback` requires two distinct approvers.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.migration.pack.reconciled.v1` | reconciliation done | run_id, pack, totals_match, totals_mismatch, quarantined | cutover, notifications |
| `dev.passage.migration.parallelrun.diffed.v1` | diff done | parallel_run_id, period, unexplained_count, unexplained_amount | cutover |
| `dev.passage.cutover.step.completed.v1` | step done | plan_id, step_id, actual_duration_min, status | cutover dashboard |
| `dev.passage.cutover.decision.recorded.v1` | go/no-go | plan_id, decision, approvers[], timestamp | audit, platform-ops |

### 9.49.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Migration factory workspace | run pack, view totals, open quarantine, re-run | pack, source, status, records, mismatches | MigrationRun |
| Control-total board | drill mismatch to records, export | name, key, source, target, delta | MigrationView |
| Quarantine | edit field, release, reject | record, rule failed, reason | MigrationRun |
| Parallel-run review | classify variances, attach reason, sign off | area, key, legacy, passage, delta, class | MigrationReview |
| Cutover runbook | start/complete step, timer, go/no-go checklist, rollback | seq, T-offset, owner, planned vs actual, status | CutoverManager |
| Legacy reference (on any record) | open xref | system, company, table, id | all |

Rules: control-total board auto-refreshes every 10 s during a run; variance grid supports bulk classify by rule; runbook shows elapsed vs planned with amber at 110%, red at 125%.

### 9.49.7 Business rules and edge cases

- **BR-49.1** Pack run state machine: EXTRACTED → VALIDATED → LOADED → RECONCILED; any step → FAILED; LOADED/RECONCILED → ROLLED_BACK (compensating journals and deletion of unposted documents).
- **BR-49.2** Open items are loaded against an opening-balance clearing account; the sum of open items per control account must equal the GL balance migrated for that account to 0.00 before RECONCILED.
- **BR-49.3** On-hand is loaded at legacy cost per dimension; the inventory GL account opening balance is migrated as a single line and must equal the loaded value per entity.
- **BR-49.4** Open orders are loaded with `qty_delivered` and `qty_invoiced` history so remaining quantities match; prior deliveries are not re-posted.
- **BR-49.5** WIP production orders carry consumed material and reported hours as a WIP opening entry; remaining operations are rebuilt from the Passage route.
- **BR-49.6** Fixed assets load acquisition date and accumulated depreciation per book; the next depreciation run must equal legacy's projected amount within 0.01 or the asset is flagged.
- **BR-49.7** Amount rounding during replay follows Passage rules; a difference ≤ 0.01 per document is classified EXPECTED by rule `RND-001`.
- **BR-49.8** Historical loads are read-only; a legacy document with no mapped Passage type lands in `history.<system>.unmapped`.
- **BR-49.9** Cutover abort at T+36h: if any go/no-go item is open, the rollback plan executes automatically after CutoverManager confirmation.
- **BR-49.10** Coexistence: a document received from legacy that already exists by `legacy_xref` is acknowledged and ignored.

### 9.49.8 Acceptance criteria

- **AC-49.1** (FR-49.1, 49.2) Given the synthetic AX dataset (3 entities, full year), when all packs run, then every `control_total.status = MATCH`.
- **AC-49.2** (FR-49.1, 49.2) Given the synthetic SAP dataset, when all packs run, then every `control_total.status = MATCH`.
- **AC-49.3** (FR-49.11) Given an open item for a vendor missing in master data, when validation runs, then the record is quarantined and load proceeds for the rest.
- **AC-49.4** (FR-49.10) Given a completed `open_items` run, when the same extract is run again, then records_loaded = 0 and totals are unchanged.
- **AC-49.5** (FR-49.5) Given one month of legacy documents replayed, when the diff runs, then unexplained_count = 0 and explained amount ≤ 0.1% of revenue.
- **AC-49.6** (FR-49.6) Given the generated runbook, when the rehearsal is executed, then all steps from T−0 to "open user access" complete in < 48 h.
- **AC-49.7** (FR-49.9) Given a rehearsal cutover, when rollback executes, then the legacy coexistence adapters resume and Passage postings since T−0 are exported as re-keying files.
- **AC-49.8** (FR-49.3) Given migrated customer invoice legacy id `CIV-00123` (AX `CustInvoiceJour.InvoiceId`), when a user opens the Passage invoice, then the legacy reference panel shows system, company and id.
- **AC-49.9** (FR-49.4) Given 7 years of legacy journals loaded, when the explorer queries `revenue` with `source=legacy` for a closed year, then it equals the legacy trial balance for that year.
- **AC-49.10** (FR-49.8) Given a pipeline trigger, when a rehearsal tenant is requested, then it is ready in ≤ 2 h with masked data.

### 9.49.9 Test plan and fixtures

Fixtures: `fx-ax-synth-year` (AX 2012 R3 schema: `CustTrans`, `VendTrans`, `InventSum`, `GeneralJournalAccountEntry`, `PurchLine`, `SalesLine`, `ProdTable`, `AssetBook`, 3 `DataAreaId`, 1.1M rows); `fx-sap-synth-year` (ECC 6.0 and S/4 2023 variants: `BSID`/`BSIK`/`ACDOCA`, `MARD`/`MBEW`, `EKPO`, `VBAP`, `AFKO`, `ANLC`, 3 `BUKRS`, 1.3M rows); `fx-replay-month` (one month, 42k documents); `fx-cutover-plan` (plan with 84 steps).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Pack reconciliation | E2E | 9 packs × 2 systems | 100% MATCH |
| Idempotent re-run | property | loader | re-run of any subset loads 0 new rows |
| GE suites | unit | validation | all expectations defined per pack with ≥ 95% coverage of mapped fields |
| Parallel-run diff | E2E | harness | 0 unexplained |
| Diff classifier | unit | classifier | 100 seeded variances classified per rule |
| Cutover timing | performance | runbook | < 48 h; each step within 125% of plan |
| Rollback | E2E | rollback | coexistence resumes; exported files complete |
| History load throughput | performance | Iceberg | ≥ 200k rows/min |

Reconciliation: trial balance (Passage opening) ↔ legacy trial balance per entity/currency; open items ↔ control accounts; on-hand value ↔ inventory account; NBV ↔ asset accounts; parallel-run TB ↔ legacy period TB.

### 9.49.10 Deployment and operations

Helm: `passage-migration-factory` (Go extractors, TS orchestration on Temporal namespace `migration`), `passage-legacy-adapters` (AX OData/SQL, SAP RFC/ODP via wave-35 hub), `passage-rehearsal-provisioner` (pipeline job). Config: legacy connection strings in Vault, `HISTORY_YEARS=7`, `MASK_PII=true` for rehearsal. Flags: `migration.coexistence` per entity. Jobs: nightly delta extract during coexistence; weekly reconciliation post go-live for 30 days. SLOs: pack load ≥ 50k records/min; control totals computed within 10 min of load. Alerts: MISMATCH, quarantine > 1%, step overrun > 125%. Runbook entries: resume a failed pack from VALIDATED; rebuild xref; execute rollback. Rollback: per BR-49.1 and FR-49.9.

### 9.49.11 Migration from AX / SAP

This wave is the migration. Summary mapping:

| Source | Passage entity | Transformation |
|---|---|---|
| `CustTransOpen`/`VendTransOpen`, `BSID`/`BSIK` | AR/AP open items | amount in transaction and accounting currency; due date; settlement history collapsed to remaining amount |
| `GeneralJournalAccountEntry` + `DimensionAttributeValueCombination`, `ACDOCA`/`FAGLFLEXT` | opening journal | balances per account × dimension × currency at cutoff; P&L closed to retained earnings if cutoff is year-end |
| `InventSum`/`InventDim`/`MBEW`/`MARD`/`MCHB` | inventory opening transactions | qty and cost per dimension; batch/serial attributes |
| `PurchLine`/`SalesLine`, `EKPO`/`VBAP` | open order lines | remaining = ordered − delivered; price and tax re-derived and compared |
| `ProdTable`/`AFKO` | production orders | status mapping (Started → RELEASED+WIP); WIP value |
| `AssetBook`/`ANLC` | asset books | NBV, accumulated depreciation, remaining life |
| `ProjTrans*`/`COSP` | project WIP | WIP per project, by category |

Not migrated: legacy number sequences (new sequences, old ids kept in xref); AX `SysDatabaseLog` rows and SAP `CDHDR` (loaded to history only); settled items and closed orders (history only); X++/ABAP customizations.

### 9.49.12 Dependencies, risks, open questions

Depends on: wave 10 (framework, connectors), wave 35 (integration hub for coexistence), wave 46 (lakehouse for history), wave 48 (PII masking rules), all transactional waves for posting APIs.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Legacy dimension model does not map 1:1 | High | High | mapping DSL with default dimension and exception report; sign-off before load |
| Extract window exceeds freeze | Medium | High | delta extracts; parallelized extractors; rehearsal timing |
| Coexistence creates duplicate intercompany documents | Medium | Medium | xref dedupe (BR-49.10) |

Open questions: (1) History years default 7 vs 10 for DE GoBD — proposed 10 when DE pack installed; decide by wave 50. (2) Payroll YTD for countries without a Passage payroll engine — proposed: load as balances only; decide by wave 50.

## 9.50 Wave 50: Scale Hardening, Multi-Region & Production Go-Live

### 9.50.1 Scope and business context

AX customers rely on LCS-managed production environments and Microsoft SLAs; SAP customers on Basis operations, HA/DR setups (HANA system replication, cluster failover) or RISE. Passage must prove the same in a self-operated, cloud-agnostic stack: a performance campaign to 10k concurrent users, 1M journal lines/h and 100k order lines/h; active/passive multi-region with cross-region Postgres replication, Kafka MirrorMaker 2 and DNS failover; SRE runbooks, capacity and cost models, zero-downtime upgrades, tenant provisioning automation; complete documentation; the first customer's production go-live and the public v1.0 release. Out of scope: active/active multi-region, per-tenant dedicated clusters, marketplace listings.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| D365 One Version updates and SAP SUM upgrades require a downtime window (hours) and a sandbox cycle; customers defer and accumulate risk | Monolithic application server; schema changes applied in place | Rolling deploys with expand/contract migrations and N/N−1 API contract tests | Upgrade downtime 4–8 h → 0; release cadence quarterly → weekly |
| DR is a service request to LCS or a Basis project with HANA system replication; failover is rarely rehearsed and RPO/RTO are unknown | DR owned by vendor or built per customer | One-command failover, quarterly game day with measured RTO/RPO | RTO ≤ 1 h, RPO ≤ 5 min, measured every quarter |
| Performance regressions appear in production (`SysOperation` batch queues, `ST05` traces after the fact) | No standing load test tied to releases | k6 scenarios S1–S7 and SLO burn-rate gates in the release pipeline | Regressions caught pre-release 100%; p99 SLOs published |
| Environment sizing is a vendor estimator (LCS subscription estimator, Quick Sizer) with opaque cost | No per-tenant metering in the product | Capacity model and `tenant_cost` meter per month | Cost estimate error ±50% → ±5% |
| Provisioning a new company environment takes days of service requests | Manual environment lifecycle | One-call tenant provisioning with residency and license | Provisioning 3 days → ≤ 15 min |

**Cutover go/no-go checklist (phase exit, executed at T+24h of the production cutover):** (1) all wave-49 packs RECONCILED, 0 MISMATCH; (2) parallel run for the prior month SIGNED_OFF with 0 unexplained; (3) opening trial balance signed by the customer controller; (4) SoD evaluation shows 0 unmitigated HIGH conflicts for go-live users; (5) country packs installed and one statutory fixture executed per entity; (6) load test on the production cluster at 50% of target passed within SLOs; (7) DR game day passed within 30 days before cutover; (8) backup verified: PITR drill on production data within 7 days; (9) security: pen-test high/critical = 0, secrets rotated, images signed; (10) integration endpoints (bank, EDI, e-invoicing, carriers) smoke-tested in production; (11) user acceptance sign-off for 12 core processes; (12) support rota, escalation and rollback decision owner named; (13) monitoring dashboards and alerts green for 24 h; (14) legacy freeze confirmed and rollback plan rehearsed. Any open item = NO-GO; the decision is recorded via `cutover.decision.recorded`.

**Hypercare (T+1d to T+30d):** 24×5 on-call with 15-minute response for Sev-1, 1 h for Sev-2; daily 09:00 customer stand-up; daily reconciliation of all subledgers to the journal and of lakehouse metrics; weekly reconciliation against legacy coexistence feeds; agents in suggest mode only; change freeze except Sev-1 fixes; exit criteria: first month-end closed and reconciled, Sev-1 count = 0 for 14 consecutive days, Sev-2 backlog ≤ 5, SLOs met for 30 days, customer sign-off.

**v1.0 release criteria:** all 50 waves' proofs green in CI on the release commit; phase-exit criteria of 9.0 met; CVE scan 0 critical, 0 high older than 30 days; API reference generated from OpenAPI/AsyncAPI with 0 undocumented operations; administrator, implementer and per-module functional guides published; upgrade path from the last stage build tested; license and pricing published; tag `v1.0.0` signed with cosign, SBOM (SPDX) attached, Helm charts and images published by digest.

### 9.50.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Production environment management | LCS production, sandbox tiers (Tier-2–5), service requests | Basis: `SM51` servers, `RZ10` profiles, RISE managed | Tenant provisioning automation, environment tiers via Helm values | Full |
| HA/DR | Microsoft-managed geo-secondary, LCS disaster recovery | HANA system replication, Pacemaker clusters, DR site | CNPG replica clusters cross-region, MirrorMaker 2, DNS failover | Full |
| Capacity and sizing | LCS subscription estimator, usage profile | Quick Sizer, SAPS | Capacity model per tenant (users, lines/h → nodes, storage) | Full |
| Performance tooling | Performance SDK, Trace parser, `SysOperation` batch tuning | `ST05`, `ST12`, `SAT`, `SQLM`, HANA PlanViz | k6 scenarios, OpenTelemetry traces, pg_stat_statements dashboards | Full |
| Monitoring | LCS Environment monitoring, Application Insights | `RZ20` CCMS, Solution Manager, Focused Run | Prometheus/Grafana SLO dashboards, Loki, Tempo | Full |
| Upgrades | One Version monthly updates, LCS deployable packages, downtime | SUM, maintenance planner, zero-downtime option (ZDO) | Expand/contract Atlas migrations, rolling deploys, no downtime | Improved |
| Batch and job scheduling | Batch framework (`SysOperation`), batch groups | `SM36`/`SM37`, Redwood | Temporal schedules (from wave 4) with multi-region continuity | Full |
| Licensing | Dynamics 365 per-user licenses | SAP named user licenses, FUE | Passage license file per tenant, user-type metering | Full |
| Documentation | Microsoft Learn | SAP Help Portal | Docs site: admin, implementer, API, functional guides, training | Full |

### 9.50.3 Functional requirements

- **FR-50.1 Load-test scenarios** — Must. The system shall meet the SLOs below under the k6 scenario set run for 2 h sustained with 10k virtual users on the production-sized cluster:
  - S1 journal posting: 1M lines/h via `/api/v1/ledger/journals` (batches of 500); S2 order intake: 100k order lines/h via sales order API plus pricing and ATP; S3 interactive mix: 10k users with 60% list/detail reads, 25% form saves, 10% dashboard loads, 5% explorer queries (think time 10 s); S4 warehouse mobile: 2k scanners at 1 scan/5 s; S5 MRP: 50k-item regeneration concurrent with S3; S6 month-end: inventory close, FX revaluation, allocations concurrent with S1 at 25%; S7 CDC lag: lakehouse freshness measured during S1+S2.

| Operation | SLO (p99 unless noted) | Error budget |
|---|---|---|
| Entity read (generated) | p95 < 50 ms, p99 < 150 ms | 0.1% |
| Form save | p99 < 400 ms | 0.1% |
| Order intake (line) | p99 < 500 ms | 0.1% |
| Pricing | p95 < 20 ms | 0.1% |
| Journal post (500-line batch) | p99 < 2 s; sustained 5k lines/s | 0.05% |
| Mobile scan round trip | p99 < 300 ms | 0.1% |
| Dashboard load | p95 < 2 s | 0.5% |
| Lakehouse lag | p95 < 60 s | n/a |
| MRP 50k items | < 10 min | n/a |
| Availability per tenant | 99.9% (99.95% with multi-region) | monthly |

- **FR-50.2 Postgres tuning** — Must. The system shall partition the journal line table by month (range) and hash by tenant (16), inventory transactions by month, audit by week; CNPG clusters with 1 primary + 2 synchronous-quorum replicas (`synchronous_standby_names ANY 1`); PgBouncer transaction pooling; `pg_stat_statements` top-50 reviewed with indexes added before exit.
- **FR-50.3 Kafka and cache tuning** — Must. The system shall run Strimzi with 6 brokers, `min.insync.replicas=2`, `acks=all`, topic partitions sized at ≥ 2× consumer parallelism, and Redis-compatible caches for pricing and policy bundles with ≤ 5 s invalidation.
- **FR-50.4 Multi-region active/passive** — Must. The system shall run a passive region with CNPG replica clusters streaming from the primary (async, lag SLO < 30 s; ledger cluster quorum-synchronous across regions when the customer accepts the latency), MirrorMaker 2 for all topics with offset translation, object-store replication for lakehouse and documents, Temporal multi-cluster replication, and DNS failover (health-checked, TTL 60 s).
- **FR-50.5 Failover procedure** — Must. The system shall promote the passive region by one command (`passage-ops failover --to eu-west`), which fences the old primary (revoke writes, scale to zero), promotes CNPG replicas, switches MirrorMaker direction, activates Temporal, flips DNS, and verifies the ledger hash chain; RTO ≤ 1 h, RPO ≤ 5 min.
- **FR-50.6 DR game day** — Must. The system shall run a quarterly game day: (1) announce window, freeze deploys; (2) take baseline hashes (ledger chain head per tenant, row counts per domain, Kafka end offsets); (3) inject failure (primary region network partition via NetworkPolicy, then node pool scale-down); (4) on-call executes failover runbook with timer; (5) verify: hash chain head matches or exceeds baseline, no gap in gap-free sequences, in-flight Temporal workflows resumed, reconciliation jobs green; (6) run smoke suite of 12 processes; (7) measure RTO/RPO; (8) fail back during a planned window; (9) post-game review with actions. Pass: RTO ≤ 1 h, RPO ≤ 5 min, zero committed-transaction loss.
- **FR-50.7 Zero-downtime upgrades** — Must. The system shall deploy services with rolling updates and expand/contract schema migrations (add → backfill → switch → drop in separate releases), with API compatibility tests between N and N−1 and a 30-minute automatic rollback window on SLO breach.
- **FR-50.8 Tenant provisioning** — Must. The system shall create a tenant (Keycloak realm, databases/schemas with RLS, Kafka ACLs, lakehouse namespace, object-store prefixes, residency region, license) from one API call in ≤ 15 min, and decommission with retention export.
- **FR-50.9 Capacity and cost model** — Must. The system shall publish a model mapping (concurrent users, journal lines/h, order lines/h, storage GB) to node counts, Postgres sizes, Kafka brokers and monthly cost per provider, and meter per-tenant resource usage monthly into `tenant_cost`.
- **FR-50.10 SRE runbooks and on-call** — Must. The system shall ship runbooks for the 30 alert rules shipped by the platform, incident severity definitions, escalation, and a status page; every alert links to its runbook.
- **FR-50.11 Documentation** — Must. The system shall publish the administrator guide, implementer guide (migration, localization, cutover), API reference (OpenAPI/AsyncAPI generated), functional guides per module (one per phase 1–8 module), and a training curriculum (role-based, 6 tracks).
- **FR-50.12 Licensing and release** — Must. The system shall enforce a signed license file per tenant (user types: full, operational, self-service; expiry; grace 30 days), and the release pipeline shall tag, sign, attach SBOMs and publish charts and images by digest.

### 9.50.4 Data model

**`region`** (platform-ops)

| Field | Type | Required | Notes |
|---|---|---|---|
| region_id | text | Y | PK, e.g. `us-east`, `eu-west` |
| role | enum | Y | PRIMARY, PASSIVE, FENCED |
| provider | text | Y | eks/gke/aks/bare |
| cnpg_lag_seconds | int | Y | observed |
| mm2_lag_messages | bigint | Y | observed |
| last_failover_event_id | uuid | N | |
| dns_weight | int | Y | 0 or 100 |

**`failover_event`**: event_id, from_region, to_region, initiated_by, started_at, dns_flipped_at, promoted_at, completed_at, rpo_seconds, rto_seconds, ledger_hash_ok bool, gapfree_ok bool, notes.

**`tenant_license`**: tenant_id, license_id, issued_at, expires_at, full_users, operational_users, selfservice_users, signature, state (VALID, GRACE, EXPIRED).

**`tenant_cost`**: tenant_id, month, cpu_core_hours, memory_gb_hours, storage_gb, kafka_gb, lakehouse_gb, ai_tokens, cost_usd, cost_eur, model_version.

**`capacity_model`**: model_version, inputs_json (users, lines/h, storage), outputs_json (nodes, pg size, brokers), provider, cost_usd_month.

Invariants: exactly one region is PRIMARY; a FENCED region accepts no writes (enforced by revoking the application role); license `full_users` ≥ active full users else state GRACE; `tenant_cost` is computed from Prometheus data with a stored query version.
Indexing: `failover_event` (started_at); `tenant_cost` PK (tenant_id, month).

### 9.50.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | `/api/v1/ops/tenants` | provision tenant | `Idempotency-Key` | PlatformAdmin |
| DELETE | `/api/v1/ops/tenants/{id}` | decommission with export | by id | PlatformAdmin + second approver |
| POST | `/api/v1/ops/failover` | initiate failover | by event id | PlatformAdmin (break-glass) |
| GET | `/api/v1/ops/regions` | region status and lag | n/a | PlatformView |
| POST | `/api/v1/ops/gameday` | start game day workflow | by id | PlatformAdmin |
| GET | `/api/v1/ops/capacity/estimate` | capacity estimate for inputs | n/a | PlatformView |
| GET | `/api/v1/ops/tenants/{id}/cost?month=` | metered cost | n/a | TenantAdmin |
| PUT | `/api/v1/ops/tenants/{id}/license` | install license | ETag | PlatformAdmin |
| GET | `/api/v1/ops/slo` | SLO status and error budgets | n/a | PlatformView |
| GET | `/healthz/region` | region health for DNS checks | n/a | public |

Validation: failover rejects if target region replication lag > 300 s unless `--accept-data-loss` with second approver; provisioning rejects a residency region not deployed.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.ops.failover.completed.v1` | promotion done | from_region, to_region, promoted_lsn, rpo_seconds, rto_seconds | all services (reconnect), audit |
| `dev.passage.ops.tenant.provisioned.v1` | provisioning done | tenant_id, region, realm, databases[] | billing, catalog |
| `dev.passage.ops.slo.breached.v1` | SLO burn rate | slo_id, window, burn_rate, tenant_id | alerting, release gate |
| `dev.passage.ops.license.state_changed.v1` | license check | tenant_id, state, expires_at | shell banner, admin |

### 9.50.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Platform operations console | view regions, initiate failover (two-person), view game-day timeline | region, role, lag, last failover | PlatformAdmin |
| Tenant management | provision, license, residency, decommission | tenant, region, license state, users | PlatformAdmin |
| SLO dashboard (Grafana embedded) | filter by tenant/service, view burn rate | SLO, target, current, budget remaining | PlatformView |
| Cost and capacity | estimate, view monthly meter, export | inputs, outputs, cost USD/EUR | TenantAdmin |
| Status page (public) | subscribe | component, status, incidents | public |
| Release notes / docs site | search, version switch | version, module | public |

Rules: failover button requires two distinct PlatformAdmin confirmations within 5 min; SLO dashboard default window 30 days; license banner appears in the shell at GRACE.

### 9.50.7 Business rules and edge cases

- **BR-50.1** Region state machine: PASSIVE → PRIMARY (promotion) with the former PRIMARY → FENCED; FENCED → PASSIVE only after re-sync verified; never two PRIMARY.
- **BR-50.2** During failover, any request to the fenced region returns 503 with `Retry-After: 30`; clients retry idempotent requests with the same key, so no double posting.
- **BR-50.3** Ledger hash chain: each journal carries `prev_hash`; after promotion the chain head per tenant must be verifiable; a break is a Sev-1 and blocks user access.
- **BR-50.4** Gap-free sequences (fiscal invoice numbers) are allocated in the ledger Postgres (synchronous replication), never in caches, so failover cannot duplicate numbers.
- **BR-50.5** Load tests run against a copy of the first customer's masked data, never production.
- **BR-50.6** An SLO burn rate > 14.4× over 1 h (fast burn) pages; > 6× over 6 h (slow burn) tickets; a release with any fast-burn alert in the 30-minute window auto-rolls back.
- **BR-50.7** Expand/contract: a migration that drops a column cannot ship in the same release as the code that stops using it.
- **BR-50.8** License GRACE allows logins for 30 days with a banner; EXPIRED blocks new logins but never data export or admin access.
- **BR-50.9** Tenant decommission retains an encrypted export for the retention period defined by the tenant's country packs before deletion.
- **BR-50.10** Cost model rounds to USD 0.01 / EUR 0.01; FX for EUR at the month-average ECB rate.

### 9.50.8 Acceptance criteria

- **AC-50.1** (FR-50.1) Given scenarios S1–S7 run concurrently for 2 h, when results are published, then every SLO row is met and error rates are within budget.
- **AC-50.2** (FR-50.2) Given 1M journal lines/h, when posting runs, then ledger Postgres replica lag p95 < 5 s and no query exceeds 1 s in pg_stat_statements.
- **AC-50.3** (FR-50.5, 50.6) Given the game day partition, when on-call runs the failover command, then RTO ≤ 1 h, RPO ≤ 5 min, ledger hash chain verified, gap-free sequences continuous.
- **AC-50.4** (FR-50.6) Given failover completed, when the 12-process smoke suite runs in the new primary, then all pass and CDC lag recovers to < 60 s within 15 min.
- **AC-50.5** (FR-50.7) Given release N+1 with an expand migration, when rolled out, then zero failed requests and N clients continue to work.
- **AC-50.6** (FR-50.8) Given a provisioning request with residency EU, when it runs, then the tenant exists in `eu-west` with RLS, realm, ACLs and license in ≤ 15 min.
- **AC-50.7** (FR-50.9) Given one month of production, when the cost meter runs, then `tenant_cost` is within 5% of provider billing for the cluster.
- **AC-50.8** (FR-50.12) Given a tenant whose license expired 31 days ago, when a user logs in, then login is blocked and admin export works.
- **AC-50.9** (9.50.1 checklist) Given the production cutover, when T+24h arrives, then all 14 go/no-go items are complete and the decision is recorded.
- **AC-50.10** (9.50.1 hypercare) Given the first production month, when month-end closes, then all subledgers reconcile to the journal, lakehouse revenue equals GL, and the customer signs off.
- **AC-50.11** (FR-50.11, 50.12) Given the release commit, when the pipeline runs, then `v1.0.0` is tagged and signed, SBOMs attached, docs published with 0 broken links and 0 undocumented API operations.

### 9.50.9 Test plan and fixtures

Fixtures: `fx-load-10k` (masked copy of the first customer plus synthetic growth to 10k users, 3 years of transactions, 40M journal lines); `k6-scenarios` (S1–S7 scripts with data pools); `fx-gameday` (baseline hash manifest generator); `fx-smoke-12` (12 core processes: PO→receipt→invoice→payment, SO→ship→invoice→cash, production order→RAF, journal post, bank reconciliation, asset depreciation, project invoice, payroll run, statutory report, dashboard, copilot query, mobile pick).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Load campaign | performance | whole platform | SLO table met, 2 h |
| Soak | performance | whole platform | 24 h at 50% load, no memory growth > 10% |
| Failover | E2E | multi-region | RTO/RPO met, hash chain ok |
| Fail-back | E2E | multi-region | re-sync and promotion without loss |
| Upgrade compatibility | contract | APIs N vs N−1 | 100% of contract tests pass |
| Expand/contract linter | unit | migrations | no drop in same release as last use |
| Provisioning | E2E | tenant API | ≤ 15 min, all components |
| Chaos | E2E | Kafka broker loss, Postgres primary kill | no failed postings, recovery < 5 min |
| Docs | contract | docs site | 0 broken links, OpenAPI coverage 100% |
| Smoke 12 | E2E | production | all pass post-cutover and post-failover |

Reconciliation: post-failover row counts and ledger hash heads ↔ baseline; production daily subledger ↔ journal; lakehouse metrics ↔ GL; `tenant_cost` ↔ provider billing; license active users ↔ Keycloak sessions.

### 9.50.10 Deployment and operations

Helm: `passage-platform` umbrella with per-region values; `passage-mm2` (MirrorMaker 2), `passage-ops` (Go control plane), `passage-status-page`, `passage-docs`. CNPG `Cluster` with `replica.enabled` in the passive region; Temporal multi-cluster. Config: `REGION_ROLE`, `DNS_PROVIDER` (thin Terraform module per provider), `FAILOVER_MAX_LAG_S=300`, `LICENSE_PUBLIC_KEY`. Migrations: `ops` database. Flags: `ops.failover.enabled`, `license.enforce`. Jobs: cost meter monthly, capacity model nightly, game day quarterly (scheduled Temporal workflow with human task), backup drill monthly, SLO report weekly. SLOs: as FR-50.1; control-plane API p99 < 500 ms. Alerts: replication lag > 60 s, MM2 lag > 100k messages, SLO fast/slow burn, license expiry 30/7/1 days, failover in progress. Runbooks: failover, fail-back, broker replacement, Postgres primary loss, slot bloat, cache stampede, Temporal namespace replication lag, certificate expiry, object-store replication lag, release rollback. Rollback: Argo CD rollback by digest; database contract step deferred to the next release; DNS weight restore.

### 9.50.11 Migration from AX / SAP

| Source | Passage | Notes |
|---|---|---|
| LCS environment inventory (production, sandboxes) | Tenant and environment tiers | documented; sandboxes re-created from masked production via provisioning |
| LCS deployable packages / Basis transports (`STMS`) | GitOps releases | not migrated; release notes map customer changes to metadata extensions |
| Batch groups and job schedules (`BatchJob`, `SM37`) | Temporal schedules | imported in wave 4 and verified for production cadence here |
| Monitoring thresholds (CCMS `RZ20`, LCS alerts) | Prometheus alert rules | mapped per alert category |
| D365 / SAP user licenses | `tenant_license` user types | full ↔ Dynamics full user / SAP professional; operational ↔ activity / functional; self-service ↔ team member / productivity |

Not migrated: Microsoft/SAP SLAs and support contracts (replaced by Passage support terms), proprietary performance traces.

### 9.50.12 Dependencies, risks, open questions

Depends on: wave 1 (pipeline, Argo CD), wave 3 (Kafka, outbox), wave 4 (Temporal), wave 11 (ledger hash chain, gap-free sequences from wave 5), wave 46 (lakehouse lag measurement), wave 48 (backup drills, residency, pen test), wave 49 (customer migration and cutover runbook).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Cross-region synchronous ledger replication adds latency beyond SLO | Medium | High | quorum within region plus async cross-region by default; customer opt-in to cross-region sync |
| First customer month-end reveals unmapped edge cases | High | High | hypercare change process; parallel-run month before cutover; agents in suggest mode |
| Load test environment differs from production | Medium | Medium | same Helm values and node classes; 50% load re-run on production before cutover |
| DNS TTL caching delays failover for some clients | Medium | Medium | TTL 60 s; client retry with alternate endpoint list |

Open questions: (1) Active/active for read-only analytics in the passive region — proposed yes for Trino only; decide before v1.1. (2) Public pricing unit (per full user per month vs per tenant tier) — proposed per user type; decide at v1.0 release. (3) Licensing model (open-core vs source-available) carried from PRD §12 — must be decided before the v1.0 tag.
