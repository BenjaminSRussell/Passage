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
| `print_rule`, `text_block`, `correspondence_type`, `dq_rule`, `archive_partition` | docgen, catalog, analytics-ingest | rule → document type, account; archive partition → retention policy (wave 48) |
| `esign_requirement`, `esign_record`, `audit_policy`, `audit_violation`, `audit_case`, `e_message` | compliance, ereporting | esign record → record version hash; violation → document, case; message → er_format run |
| `screening_list`, `screening_hit`, `trade_classification`, `export_license`, `elevated_session`, `security_incident` | trade-compliance, security | hit → party/document (blocks release); incident → breach regimes |

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
| `dev.passage.documents.print.routed.v1` | docgen | print agent, audit | render_id, rule_id, destinations[] |
| `dev.passage.compliance.esign.signed.v1` | compliance | quality, production, audit | esign_id, record, meaning, signer |
| `dev.passage.trade.screening.hit.v1` | trade-compliance | sales, procurement, payments | subject, list_code, score |
| `dev.passage.security.incident.breach_confirmed.v1` | security | privacy, notifications | incident_id, regimes[], deadlines[] |

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
| Print management routing | Print management setup (`PrintMgmtSettings`, `PrintMgmtDocInstance`, conditional settings by query, original/copy destinations), Form setup, Form sorting | `NACE` output determination (condition tables, access sequences, `NAST`), S/4 Output Parameter Determination (BRF+ `OPD`), `SPAD` printers | `print_rule` with condition expressions, original/copy destinations and print agent (FR-46.14) | Full |
| Form notes and standard texts | Form notes (`FormLetterRemarks`), document notes (`DocuRef`) printed per document type | `SO10` standard texts, text IDs (`SE75`), text determination (`VOTXN`) | `text_block` with placeholders and language versions (FR-46.15) | Full |
| Correspondence | Customer/vendor account statements (`CustAccountStatementExt`), payment advice | `F.27` periodic statements, `F.64` correspondence (`OB77` types, `T048`), `SAP01`–`SAP19` | `correspondence_type` with trigger and template (FR-46.15) | Full |
| Data quality monitoring | Data validation checklist (DMF), none for production data | Information Steward rules and scorecards | `dq_rule` on catalog assets with scorecard tile (FR-46.16) | Full |
| Process mining | None | Signavio Process Intelligence, Process Observer (`POC`), Process AI | `mart.process_event` event log, variant explorer, OCEL/XES export (FR-46.17) | Partial (no conformance engine in v1) |
| Data archiving | D365 Archive (long-term retention via Dataverse), `SysDatabaseLog` cleanup | `SARA` archiving objects, `SARI` archive info system, ILM retention warehouse | Archive tier `archive.<domain>.<table>` in Iceberg driven by wave-48 residence times (FR-46.18) | Full |

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

- **FR-46.14 Print management routing** — Must. The system shall resolve, for every document render, the destinations of the ORIGINAL and each COPY from `print_rule` sets maintained per (document type, legal entity), with account-level overrides (customer, vendor, project, bank) and conditional rules evaluated by priority.
  - Notes: destinations are PRINTER (IPP queue reached through the site print agent), EMAIL (template, recipients from party contact roles such as `INVOICE_TO`, `REMIT_TO`), E_INVOICE (wave-35 channel), ARCHIVE_ONLY, PORTAL, NONE; resolution order is account override → first matching conditional rule → legal-entity default → tenant default; conditions use the wave-3 expression subset (FR-3.3) over the document read model, or reference a FR-3.13 decision table by code (`rules.eval`) (e.g. `amount_total >= 10000 AND customer.group = 'KEY'`); a rule may override the template version, copy count (0–9) and the caption printed per copy (`ORIGINAL`, `COPY 1`, `FILE COPY`); the resolved destination is shown on the posting dialog and recorded on `document_render.destinations_json`; the print agent is a Go sidecar per site polling `/api/v1/documents/print-queue`, buffering 24 h offline and acknowledging each job with the printer's job id.
- **FR-46.15 Standard text blocks and correspondence** — Should. The system shall maintain `text_block` (id, language versions, placeholders such as `{{customer.name}}`, `{{document.due_date|date}}`), form notes per (document type, legal entity, position header/footer), and `correspondence_type` definitions (periodic account statement, payment notice, balance confirmation, dunning cover letter, remittance cover) with trigger (schedule, event, manual), selection (party group, open-item age) and template.
  - Notes: placeholders are resolved against the document read model; a missing placeholder renders empty and raises `DOC-0407`; periodic statements run on the wave-46 schedule engine (default last calendar day, 20:00 local); correspondence output follows FR-46.14 routing; text blocks are exported with country packs for statutory wording (wave 48).
- **FR-46.16 Data-quality rules** — Should. The system shall evaluate `dq_rule` definitions (completeness: null share per column; validity: regex or domain list; uniqueness; referential; freshness: max age of `_ingested_at`) against catalog assets every 60 min, compute a score per entity (0–100), and show a scorecard tile with drill-down to failing rows.
  - Notes: rules compile to dbt tests or Trino queries; a rule has an owner, a threshold (default: fail below 98% pass rate) and a severity; failures create a task in the owner's wave-4 inbox once per rule per day; scores are stored in `dq_score` (asset, rule, date, pass_pct, failed_rows) and are exposed as Cube measures.
- **FR-46.17 Process event log and mining views** — Could. The system shall build `mart.process_event` (case_id, case_type, activity, event_ts, actor, resource, amount, source_event_type) from the CloudEvents stream, chaining documents into cases (P2P: requisition → PO → receipt → invoice → payment; O2C: quote → order → shipment → invoice → cash; production: order → release → RAF → close), and provide a variant explorer tile (top 20 variants, median and p90 cycle time per step, rework count, automation share) with export to OCEL 2.0 JSON and XES.
  - Notes: case stitching uses document reference fields, not heuristics; events older than 24 months are rolled up; conformance checking against a reference model is not in v1 (external tools consume the export).
- **FR-46.18 Archive tier** — Should. The system shall move closed OLTP partitions (journal lines, inventory transactions, audit, workflow history, agent runs) whose age exceeds the wave-48 `retention_policy.residence_months` (default 24) to Iceberg `archive.<domain>.<table>` in a Temporal job, verify row count and content hash per partition, then detach and drop the OLTP partition.
  - Notes: records under legal hold or in an open fiscal period are never moved; archived records stay readable from the record detail (badge `ARCHIVED`) and in explorer with `source=archive`; a partition can be restored into a side table for 30 days on `ArchiveAdmin` request; archive moves are evidence for wave-48 control CC6.5 (data disposal).

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

**Additional entities (FR-46.14–46.18).** `print_rule`: rule_id, tenant_id, legal_entity_id, document_type, scope (DEFAULT, ACCOUNT, CONDITIONAL), account_ref (party or project), priority, condition_expr (wave-3 expression subset or `rules.eval` reference, ≤ 2 KB), destinations_json (per ORIGINAL/COPY n: type, target, template_version, caption), copies int default 1, status (ACTIVE, RETIRED), version. `text_block`: block_id, tenant_id, code (unique per tenant), language (BCP 47), body (≤ 16 KB), placeholders_json, status. `correspondence_type`: type_id, tenant_id, code, trigger (SCHEDULE, EVENT, MANUAL), cron, selection_json, template_id, status. `dq_rule`: rule_id, asset_fqn, rule_type (COMPLETENESS, VALIDITY, UNIQUENESS, REFERENTIAL, FRESHNESS), expression, threshold_pct numeric(5,2) default 98, severity, owner_user_id, status. `archive_partition`: partition_id, domain, table_name, period, rows_oltp, rows_archive, content_hash, moved_at, status (PENDING, VERIFIED, DROPPED, RESTORED), legal_hold_checked_at.

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
| GET/PUT | `/api/v1/documents/print-rules` | list/maintain print rules per document type and entity | ETag | PrintManagementSetup |
| POST | `/api/v1/documents/print-rules/resolve` | resolve destinations for a record without rendering | n/a (read) | per-document duty |
| GET | `/api/v1/documents/print-queue?site=` | print agent pulls pending jobs; `PATCH` acknowledges | by job id | PrintAgent (service account) |
| GET/PUT | `/api/v1/documents/text-blocks/{code}` | maintain text block language versions | ETag | DocumentDesign |
| POST | `/api/v1/documents/correspondence/{type}/run` | run a correspondence type (statements, notices) | `Idempotency-Key` | per-module duty |
| GET/PUT | `/api/v1/catalog/dq-rules` | maintain data-quality rules; `GET /scores?asset=` returns history | ETag | CatalogSteward |
| POST | `/api/v1/analytics/process-events/export` | export OCEL/XES for a case type and period | `Idempotency-Key` | AnalyticsExport |
| POST | `/api/v1/archive/partitions/{id}/restore` | restore an archived partition for 30 days | by partition id | ArchiveAdmin |

Query validation: ≤ 10 measures, ≤ 8 dimensions, time range ≤ 5 years at day grain, result cap 50k rows interactive; otherwise redirected to export.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.analytics.snapshot.committed.v1` | Iceberg commit | domain, table, snapshot_id, rows_added, rows_deleted, max_lsn, watermark_ts | semantic (cache invalidation), catalog |
| `dev.passage.analytics.metric.reconciled.v1` | daily job | metric_id, tenant_id, date, semantic_value, source_value, delta, status | compliance evidence, alerting |
| `dev.passage.analytics.report.delivered.v1` | schedule run | schedule_id, run_id, format, destination, row_count, bytes | audit |
| `dev.passage.documents.template.published.v1` | version publish | template_id, version, document_type, visual_hash | print management, catalog |
| `dev.passage.documents.print.routed.v1` | destination resolved at render | render_id, document_type, record_id, rule_id, destinations[] | print agent, audit |
| `dev.passage.catalog.dq.scored.v1` | hourly evaluation | asset_fqn, rule_id, pass_pct, failed_rows, severity | data owner inbox, dashboard |
| `dev.passage.archive.partition.moved.v1` | archive job verified a partition | domain, table, period, rows, content_hash | compliance evidence (CC6.5), catalog |

### 9.46.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Workspace dashboard host | refresh, drill-through, change period, pin tile | metric tiles, sparklines, freshness badge | AnalyticsView |
| Explorer | add measure/dimension, filter, pivot, save, export | metric picker with formula tooltip, dimension tree | AnalyticsView |
| Metric registry | publish version, set owner, view reconciliation history | metric_id, formula, grain, tolerance, last delta | AnalyticsAuthor |
| Pipeline health | pause/resume, replay from LSN, view DLQ | domain, table, lag_s, status, dlq_count | AnalyticsAdmin |
| Document designer | drag bands, bind fields, preview with fixture, publish | band tree, locale, print-management rules | DocumentDesign |
| Schedules | create, run now, view deliveries | cron, timezone, destination, last status | AnalyticsAuthor |
| Print management setup | add rule, set condition, order by priority, preview resolution for a record | document type, scope, account, condition, destinations, copies | PrintManagementSetup |
| Text blocks and correspondence | edit language versions, insert placeholder, run statement job | code, language, body, placeholders, correspondence trigger | DocumentDesign |
| Data-quality scorecard | drill failing rows, assign owner, mute rule for 7 days | asset, rule, pass %, failed rows, trend | CatalogSteward |
| Process variant explorer | pick case type and period, compare variants, export OCEL | variant, count, median cycle time, rework | AnalyticsView |
| Archive partitions | view moved partitions, request restore | domain, table, period, rows, hash, status | ArchiveAdmin |

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

- **BR-46.11** Print rule resolution is deterministic: account override beats conditional rules; among conditional rules the lowest `priority` number wins and ties are rejected at save; a rule referencing a RETIRED template version cannot be ACTIVE.
- **BR-46.12** Copies are numbered from 1 and carry the caption of their rule; a re-print of a posted document always renders with the template version recorded on `document_render`, never the current one.
- **BR-46.13** A print job unacknowledged by the print agent for 30 min is marked `STALE`, re-queued once, then `FAILED` with `DOC-0412` to the requesting user.
- **BR-46.14** An archive partition is dropped from OLTP only after `rows_oltp = rows_archive` and the content hash matches; a mismatch leaves both copies and opens an incident of severity Sev-3.
- **BR-46.15** DQ scores below threshold for 3 consecutive evaluations escalate to the catalog steward; a muted rule still scores but does not notify.
- **BR-46.16** Process event case ids are the originating document id; a document that cannot be chained lands in case type `UNLINKED` and is excluded from cycle-time measures.

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

- **AC-46.11** (FR-46.14) Given a customer with account override EMAIL and a conditional rule `amount_total >= 10000 → PRINTER + COPY 1 ARCHIVE_ONLY`, when a USD 12,000 invoice is posted, then the original goes to the customer's e-mail (override wins) and `document_render.destinations_json` records the rule id.
- **AC-46.12** (FR-46.14) Given the site print agent offline for 2 h, when 40 packing slips are posted, then all 40 print within 5 min of the agent reconnecting and none duplicates.
- **AC-46.13** (FR-46.15) Given correspondence type `ACCOUNT_STATEMENT` scheduled monthly for customer group `KEY`, when the schedule fires, then one statement per customer with open items is rendered in the customer's language and routed per FR-46.14.
- **AC-46.14** (FR-46.16) Given a completeness rule on `vendor.tax_id` with threshold 98%, when 3% of vendors lack a tax id, then the scorecard shows the rule failed, the owner receives one inbox task, and the failing vendor list is downloadable.
- **AC-46.15** (FR-46.18) Given journal-line partitions older than 24 months and no legal hold, when the archive job runs, then the partitions exist in `archive.ledger.journal_line`, hashes match, the OLTP partitions are dropped, and the records still open from the journal inquiry with badge `ARCHIVED`.
- **AC-46.16** (FR-46.17) Given one month of P2P documents, when `mart.process_event` is built, then every paid invoice traces to its PO case and the variant explorer lists the straight-through variant with its share.

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

### 9.46.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `analytics.cdc.commit_interval_s` | tenant (platform default) | int | 30 | 10–300 | Iceberg commit cadence per sink | Entity store refresh batch recurrence (`AggregateMeasurement`) | ODP delta queue schedule (`ODQMON`), DTP frequency |
| `analytics.cdc.late_watermark_min` | tenant | int | 5 | 1–60 | watermark lag before partitions close | none | BW late-arriving delta handling |
| `analytics.recon.tolerance` | legal entity | numeric | 0.005 | 0–1.00 | max |delta| before dashboard banner | none (manual tie-out) | none (manual BW–GL reconciliation) |
| `analytics.recon.run_time_local` | tenant | time | 02:00 | any | daily reconciliation start | batch job recurrence | `SM36` job start |
| `analytics.dashboard.refresh_s` | user | int | 60 | 15–600 | tile auto-refresh | Power BI tile refresh | SAC story auto-refresh |
| `analytics.freshness.amber_s` / `red_s` | tenant | int | 60 / 300 | ≥ 10 | badge thresholds | none | none |
| `analytics.query.max_rows_interactive` | tenant | int | 50,000 | 1,000–200,000 | redirect to export above cap | OData `maxpagesize` | BICS result limit (`RSADMIN` `MAX_NUMBER_OF_CELLS`) |
| `analytics.export.max_rows` | tenant | int | 1,000,000 | ≤ 5,000,000 | export job cap | Open in Excel row limit | ALV/AfO export limits |
| `analytics.export.link_ttl_h` | tenant | int | 24 | 1–168 | signed URL validity | none | none |
| `analytics.schedule.retry` | tenant | int/backoff | 3 / 1,4,16 min | 0–5 | delivery retry | batch retry count | `SM37` restart |
| `docgen.engine` | tenant | enum | CHROMIUM | CHROMIUM, TYPST (flag) | render engine | SSRS | ADS (`SFP`) |
| `docgen.pdf_profile` | legal entity | enum | PDF/A-3 | PDF/A-3, PDF 1.7 | archival format | none | ADS PDF/A setting |
| `docgen.visual_regression_max_pct` | tenant | numeric | 0.1 | 0–5 | publish gate | none | none |
| `print.default_destination.<document_type>` | legal entity | enum | ARCHIVE_ONLY | PRINTER, EMAIL, E_INVOICE, ARCHIVE_ONLY, PORTAL, NONE | fallback when no rule matches | Print management default | `NACE` default condition record |
| `print.copies_default` | legal entity | int | 1 | 0–9 | copies when rule silent | Print management copies | `NAST` number of messages |
| `print.agent_offline_buffer_h` | site | int | 24 | 1–72 | job retention while agent offline | Document routing agent | `SPAD` spool retention |
| `print.stale_job_min` | site | int | 30 | 5–240 | BR-46.13 threshold | none | spool request timeout |
| `text.default_language` | legal entity | BCP 47 | entity language | installed languages | fallback for text blocks | Form notes language | `SO10` logon language |
| `correspondence.statement_schedule` | legal entity | cron | `0 20 L * *` | cron | periodic statements | Account statement batch | `F.27` variant |
| `dq.eval_interval_min` | tenant | int | 60 | 15–1440 | rule evaluation cadence | none | Information Steward task schedule |
| `dq.default_threshold_pct` | tenant | numeric | 98 | 50–100 | default rule threshold | none | IS rule threshold |
| `process.event_retention_months` | tenant | int | 24 | 6–120 | event-log roll-up age | none | Process Observer retention |
| `archive.residence_months.<domain>` | tenant | int | 24 | 6–120 (≥ wave-48 statutory minimum) | FR-46.18 move age | none | `AOBJ` residence time |
| `archive.restore_ttl_days` | tenant | int | 30 | 1–90 | restored partition lifetime | none | `SARI` retrieval |
| `catalog.sync_interval_min` | tenant | int | 60 | 15–1440 | lineage refresh | none | BW metadata repository refresh |

Setup sequence per legal entity: install document templates → map party contact roles for e-mail routing → define print rules (defaults, then overrides) → load text blocks and form notes → enable dashboards per workspace → register reconciliation sources for financial metrics → assign DQ rule owners → set archive residence per domain (must not undercut the wave-48 retention minimum for the entity's country).

### 9.46.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Workspace dashboards | KPI | period, legal entity, site, dimension filters | governed metrics, sparklines, freshness | Cube over `mart.*` | Workspace tiles, Power BI embedded, role center KPIs | Fiori Overview Pages, SAC stories, Smart Business tiles |
| Explorer | drill-down | measures × dimensions × filters, time grain | any published metric and dimension | Cube/Trino | Open in Excel, Power BI Desktop over BYOD | Analysis for Office, Query Browser, Multidimensional apps |
| Metric reconciliation results | operational report | metric, date range, status | semantic value, source value, delta, status, days trusted | `metric_reconciliation` | none | none |
| Pipeline health | list inquiry | domain, status | lag seconds, last LSN, DLQ count, snapshot id | `cdc_pipeline_state` | Entity store refresh status | `RSA7` delta queue, `ODQMON` |
| Export log | list inquiry | user, date, format | rows, bytes, filter hash, destination | `export_job` | none | download audit (`SM20` partial) |
| Schedule deliveries | list inquiry | schedule, status | run time, destination, format, retries, error | `report_delivery` | Batch job history | `SM37`, `SOST`, `RSRD_LOG` |
| Document output log | list inquiry | document type, record, destination, status | render id, template version, destinations, agent ack, error | `document_render` | Print management journal / document history | `VV13`/`VV23` NAST status, Manage Output Items |
| Print rule resolution trace | drill-down | record | matched rule, evaluated conditions, final destinations | `print_rule` + read model | none | `NACE` determination analysis |
| Data-quality scorecard | KPI | asset, rule type, owner | pass %, failed rows, trend, score | `dq_score` | none | Information Steward scorecard |
| Process variant explorer | drill-down | case type, period, entity | variants, cycle times, rework, automation share | `mart.process_event` | none | Signavio Process Intelligence |
| Archive partition inquiry | list inquiry | domain, period, status | rows, hash, moved_at, restore state | `archive_partition` | none | `SARI`, `SARA` job log |
| Lineage view | drill-down | asset | upstream/downstream assets, owners, last refresh | OpenMetadata | none | BW metadata repository, Datasphere catalog |
| Usage statistics | KPI | period | dashboard opens, explorer queries, exports per user | `analytics_usage` | none | `RSRT` statistics, SAC usage |

### 9.46.15 Printed and electronic documents

| Document | Trigger | Layout elements (header/line/footer) | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Sales invoice | invoice posting (wave 30) | entity branding, bill-to/ship-to, tax ids, invoice number/date, payment terms, IBAN; lines with item, qty, unit price, discount, tax code; totals per tax rate, footer text blocks | PDF/A-3 with embedded e-invoice XML (wave 35), e-mail, EDI, portal | invoice sequence per legal entity (gap-free where the country pack requires) | country pack statutory content; retain 7–10 years per wave-48 policy |
| Purchase order | PO confirmation (wave 17) | vendor, delivery address, terms, buyer; lines with item, qty, price, requested date; footer terms text block | PDF, e-mail, cXML/EDI (wave 35), portal | PO number | retain with PO record |
| Packing slip | shipment confirmation (wave 28) | ship-to, carrier, tracking; lines with item, qty, batch/serial, weight | PDF, label printer (wave 32) | packing slip sequence | customs copy per wave-48 export rules |
| Check | payment run (wave 19) | MICR line, payee, amount in words, remittance stub | PDF to check printer; positive-pay file | check number per bank account | bank retention; voided checks kept |
| Customer/vendor statement | correspondence schedule or manual | balance forward, open items by age, totals | PDF, e-mail, portal | statement run id | none |
| Pick list | wave release (wave 31) | wave, locations in pick sequence, item, qty, license plate | PDF, mobile | pick list id | none |
| Remittance advice | payment run | payee, invoices paid, discounts, net | PDF, e-mail, ISO 20022 `remt.001` | payment id | none |
| Payment notice / balance confirmation | correspondence type | party, period, items, confirmation text block, signature line | PDF, e-mail | correspondence run id | audit support |
| Scheduled report | schedule | dashboard snapshot or explorer grid, parameters, generated-at, freshness | PDF, XLSX, CSV | run id | none; deleted after 30 days |
| Export file | explorer export | grid as requested, filter manifest sheet | XLSX, CSV | export job id | audit log kept 1 year; file deleted after 24 h |
| Process event export | FR-46.17 | cases, events, objects | OCEL 2.0 JSON, XES | export job id | none |

### 9.46.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| dbt incremental run | every 60 s | domain | `_source_lsn` high-water mark per model | < 45 s for 1M lines/h | skip cycle, alert after 5 consecutive failures | `dbt_run_duration_s`, lag p95 |
| SCD2 rebuild | nightly 01:00 | master-data entity | full rebuild into staging then swap | < 20 min | keep previous tables; alert | job status |
| Metric reconciliation | daily 02:00 local | tenant, metric | (tenant, metric, date) unique | < 5 min | banner on dashboard; `metric.reconciled` BLOCKED | delta > tolerance |
| Iceberg compaction and snapshot expiry | nightly 03:00 | table | Iceberg commit | < 60 min | retry next night; small-file count alert | files < 64 MB count |
| Lineage sync | hourly | asset type | OpenMetadata ingestion checkpoint | < 10 min | retry 3× | missing-edge count |
| Schedule dispatcher | every minute | schedule | `(schedule_id, next_run_at)` | < 1 s dispatch | BR: retry 3× with backoff, then `report_delivery` FAILED | failed deliveries |
| Export cleanup | hourly | export job | delete files > 24 h | < 1 min | none | none |
| Periodic correspondence (statements) | per `correspondence_type.cron` | legal entity, party group | run id | < 15 min per 10k statements | partial run resumable per party | failed renders |
| Print queue sweeper | every 5 min | site | job state | < 1 s | BR-46.13 | stale jobs > 0 |
| DQ evaluation | every 60 min | asset | `(rule, evaluated_at)` | < 10 min | skip rule after timeout 5 min | rules failing |
| Process event build | every 15 min | case type | event offset | < 5 min | resume from offset | build lag |
| Archive mover | weekly Sunday 04:00 | domain, partition | `archive_partition` state machine | < 2 h per 100M rows | BR-46.14 | hash mismatch, partitions pending > 7 days |

### 9.46.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| AnalyticsView | `GET /analytics/metrics`, `POST /analytics/query`, dashboard host, explorer (read) | none | every business role |
| AnalyticsAuthor | `PUT /analytics/dashboards`, `POST /analytics/schedules`, metric registry publish | with FinanceClose when publishing financial metrics (MEDIUM, `RPT-001`) | Controller, Analytics author |
| AnalyticsExport | `POST /analytics/exports`, process-event export | subject to wave-48 export governance | Controller, Analyst |
| AnalyticsAdmin | pipeline health, replay from LSN, DLQ release, Cube cache flush | with AnalyticsAuthor (LOW) | Platform operator |
| DocumentDesign | template designer, `POST /documents/templates/{id}/versions`, text blocks | with PrintManagementSetup when the same user routes payments (`P2P-007`, MEDIUM) | Application administrator |
| PrintManagementSetup | `PUT /documents/print-rules`, resolution trace | with ProcessVendorPayments (HIGH, check and remittance redirection) | Application administrator |
| PrintAgent (service) | `GET/PATCH /documents/print-queue` | n/a | site print agent identity |
| CatalogView | lineage, catalog browse | none | every business role |
| CatalogSteward | DQ rules, owners, asset metadata | none | Data steward |
| ArchiveAdmin | archive partitions, restore | with LegalHoldAdmin (MEDIUM) | Platform operator, Compliance |

### 9.46.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| ANA-0101 | error | Query exceeds the interactive limit of {max_rows} rows. Use Export. | result cap | run as export job | FR-46.9 |
| ANA-0102 | error | You have no data scope for this model (NO_SCOPE). | OPA scope returns empty | request legal-entity access | BR-46.9 |
| ANA-0103 | error | Model {model} has no security mapping (SCOPE_UNMAPPED). | missing scope mapping | AnalyticsAdmin maps the model | FR-46.7 |
| ANA-0104 | warning | Metric {metric} failed reconciliation on {date}: delta {delta}. | |delta| > tolerance | investigate source; dashboard banner until green | FR-46.13 |
| ANA-0105 | error | CDC for {domain}.{table} paused: incompatible schema change ({detail}). | narrowing or NOT NULL | operator approves mapping, resume | FR-46.3 |
| ANA-0106 | warning | Data is {lag} s old. | freshness above amber threshold | wait or check pipeline health | FR-46.8 |
| ANA-0107 | info | Export ready: {rows} rows, link valid until {expires_at}. | export completed | download | FR-46.9 |
| ANA-0108 | error | Schedule {name} failed after 3 attempts: {error}. | delivery failure | fix destination, run now | FR-46.10 |
| ANA-0109 | warning | Data-quality rule {rule} on {asset} at {pass_pct}% (threshold {threshold}%). | rule below threshold | owner reviews failing rows | FR-46.16 |
| ANA-0110 | error | Archive verification failed for {domain}.{table} {period}: counts {a} vs {b}. | mismatch | incident opened; partition kept | BR-46.14 |
| DOC-0401 | error | Template version {v} is RETIRED and cannot be assigned to a print rule. | rule references retired version | pick a PUBLISHED version | BR-46.11 |
| DOC-0402 | error | Two print rules have priority {p} for {document_type} in {entity}. | tie | change priority | BR-46.11 |
| DOC-0403 | error | Visual regression delta {pct}% exceeds {max}%; version not published. | render drift | review diff, re-baseline with reason | BR-46.8 |
| DOC-0404 | error | Render failed for {document_type} {id}: {reason}. | data or engine error | retry; check read model | FR-46.11 |
| DOC-0405 | warning | No print rule matched; using entity default {destination}. | fallback | add rule if unwanted | FR-46.14 |
| DOC-0406 | error | Recipient role {role} has no e-mail on party {party}. | missing contact | maintain contact | FR-46.14 |
| DOC-0407 | warning | Placeholder {name} is empty in text block {code}. | unresolved placeholder | fix binding or text | FR-46.15 |
| DOC-0412 | error | Print job {id} failed: no acknowledgment from print agent at {site}. | agent offline > 2 × stale window | check agent, reprint | BR-46.13 |

### 9.46.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation under 3× load |
|---|---|---|---|---|---|---|
| CDC commit to `raw.*` | 1M journal lines/h peak, 20M change events/day | lag 10 s / 30 s / 60 s | 6k events/s per domain | 400 GB raw (Parquet, zstd) | `days(_source_ts)`, `bucket(16, tenant_id)` | commit size adapts to 100 MB; lag badge amber |
| dbt incremental cycle | 60 s cadence | 20 s / 45 s / 60 s | n/a | `core.*` 250 GB, `mart.*` 120 GB | lookback 3 days | cycle stretches to 120 s; no data loss |
| Cube query (≤ 50k rows) | 10k users, 5% querying, peak 500 queries/min | 300 ms / 2 s / 4 s | 50 queries/s | pre-aggregation 50 GB | pre-aggregations per legal entity and day | queue; p95 4 s; explorer debounce raised to 1 s |
| Dashboard load (8 tiles) | 10k opens/day | 800 ms / 2 s / 3 s | 100 loads/s | n/a | cached per (user scope, tile, minute) | cache TTL 2 min |
| Export job | 200/day, up to 1M rows | 1 min / 8 min / 10 min | 4 concurrent per tenant | 24 h retention only | Trino spill to disk | queued; ETA shown |
| Document render (50 lines) | 50k documents/day, peak 2k/min | 300 ms / 800 ms / 1.5 s | 40 renders/s per docgen pod | 1.5 TB PDF/A per 10M docs (object store) | by tenant prefix and month | autoscale to 12 pods; print queue absorbs |
| Print rule resolution | per render | 5 ms / 20 ms / 50 ms | n/a | negligible | index (tenant, document_type, scope, priority) | n/a |
| DQ evaluation | 500 rules | 2 min / 8 min / 10 min | n/a | `dq_score` 2 GB | by (asset, date) | interval doubles |
| Archive move | 100M rows/week | < 2 h | 15k rows/s | OLTP shrinks ~40% | partition by month | job spans two windows |

### 9.46.20 Localization and compliance hooks

- Document layouts vary by country: legal mentions (DE §14 UStG invoice contents, FR mention of late-payment penalties, IN GSTIN and HSN codes, MX CFDI UUID and QR, UK VAT number), address formats and number/date formats come from the wave-48 `country_pack.document_layouts[]` which installs template variants keyed by `locale`; the designer refuses to publish a `SalesInvoice` variant lacking the pack's mandatory elements.
- Currency rounding at display uses the currency's minor units (JPY 0, KWD 3) and the entity's rounding rule from wave 11; amounts in words on checks use the pack's language rules.
- Fiscal calendars (wave 11) drive the Cube `timeDimension` for period comparisons; 4-4-5 and IN April–March years are first-class.
- Retention of rendered documents, exports and archive partitions follows wave-48 `retention_policy` per record class and country (DE 10 years GoBD, US 7 years, FR 10 years, IN 8 years); the archive mover reads the minimum and never undercuts it.
- Privacy: explorer and exports are subject to wave-48 export governance (FR-48.24) and the PII classification from wave 42; the process event log stores actor ids, not names, and is pseudonymized on erasure.
- Language: dashboards, metric names and text blocks are translated through the wave-2 label framework; text blocks keep per-language bodies and the document locale picks them.
- Extension points: `country_pack.document_layouts[]`, `country_pack.text_blocks[]`, `print_rule.condition_expr` (wave-3 expression subset / FR-3.13 decision tables with a `country` input column), Cube `queryRewrite` for country-specific data scopes, `retention_policy.residence_months` per domain.

### 9.46.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.99.4 row 17 — 12.2.4 Forms → Print management (AP) | PARTIAL-46 | ADDED as FR-46.14 | conditional routing per vendor and query, original/copy destinations |
| AX 12.99.4 row 30 — 12.3.4 Form setup (sales documents) / Form notes / Print management | PARTIAL-46 | ADDED as FR-46.14, FR-46.15 | form notes via text blocks; routing via print rules |
| AX 12.99.4 row 102 — 12.11.4 Form setup (project invoices) / Print management | PARTIAL-46 | ADDED as FR-46.14 | project account override scope |
| AX 12.99.4 row 167 — 12.16.4 Form setup (purchase documents) / Form notes / Print management (purchase) | PARTIAL-46 | ADDED as FR-46.14, FR-46.15 | |
| AX 12.99.4 row 173 — 12.17.4 Form setup (sales), Form notes, Print management (sales), Form sorting | PARTIAL-46 | ADDED as FR-46.14, FR-46.15 | form sorting already in designer (line sort band property) |
| AX 12.99.4 row 240 — 12.29.1 Print management (org-wide) (`PrintMgmtDocType`) | PARTIAL-46 | ADDED as FR-46.14 | tenant and legal-entity defaults |
| AX 12.99.4 row 259 — 12.29.4 Print management setup (per module/document: original/copy destinations, conditional settings by query) | PARTIAL-46 | ADDED as FR-46.14 | query conditions via wave-3 expression subset or FR-3.13 decision table |
| AX 12.30 inventory rows "Data archiving / Long-term retention", "Archive/purge jobs (D365)" | COVERED-46 (by name only) | ADDED as FR-46.18 | archive tier now specified; retention minimums from wave 48 |
| SAP 13.99.2 row 33 — 13.2 `F.64` AP correspondence (PARTIAL-19, "define on wave 46 designer") | PARTIAL-19 | ADDED as FR-46.15 | correspondence types with triggers; wave 19/30 bind their events |
| SAP 13.99.2 row 349 — 13.31 `SO10`/`SO72`/`SE75` standard texts (PARTIAL-5, "propose wave 46 designer text blocks") | PARTIAL-5 | ADDED as FR-46.15 | text blocks with placeholders and language versions |
| SAP 13.99.2 row 353 — 13.31 SAP Process Mining (Signavio) / Process Observer (`POC`) | PARTIAL-46 | ADDED as FR-46.17 (partial: event log, variants, OCEL/XES export; no conformance engine) | conformance checking remains OUT-OF-SCOPE for v1 — external tools consume the export |
| SAP 13.99.2 row 399 — 13.36 Data quality dashboards (Information Steward) | PARTIAL-46 | ADDED as FR-46.16 | DQ rules on catalog assets, scorecard tile |
| SAP 13.99.2 row 401 — 13.37 Signavio Process AI / Process Insights | PARTIAL-46 | SUPERSEDED by FR-46.17 + wave-47 copilot (`ask_data` over `mart.process_event`) | recommendations are copilot answers over the event log, not a separate product |
| SAP 13.31 rows "S/4 Output Management (BRF+ OPD)", "output control: output types, determination" | SUPERSEDED-46 | ADDED as FR-46.14 | determination rules are print rules; channels map 1:1 |
| SAP 13.99.2 rows 348/352/354 — BRF+ / decision tables (PARTIAL-3, not this wave) | PARTIAL-3 | SUPERSEDED by FR-3.13 (wave 3 `RuleSet`/`DecisionTable`); output determination here consumes it via `print_rule.condition_expr`, no private rules engine | listed for traceability only |
| AX 12.99.1 theme "Alerts and notification rules" (wave 5, not this wave) | GAP-5 | DEFERRED to wave 5 (owner); reconciliation banners, DQ failures and schedule failures deliver through the wave-5 notification center | listed for traceability only |

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
| Payment-date prediction | Customer payment predictions (Finance insights, Dataverse ML) | Cash Application ML, FSCM collections scoring | `payment_prediction` model and scores on open invoices (FR-47.15) | Full |
| Knowledge retrieval | Copilot over Microsoft Learn docs | Joule answers from SAP Help, solution database (`IS01`/`IS02`) | `search_knowledge` tool over wave-45 knowledge articles and the wave-50 docs site (FR-47.14) | Full |
| AI scenario activation and thresholds | Feature management flags, `VendInvoiceAutomation` parameters | `ISLM` intelligent scenarios, Business AI activation per company code | `agent.<id>.mode` per legal entity with thresholds (FR-47.5, 9.47.13) | Full |

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

- **FR-47.14 Knowledge retrieval tool** — Should. The system shall expose `passage.knowledge.search` (hybrid BM25 + embedding search over wave-45 knowledge articles, wave-50 documentation, and tenant policy documents uploaded by an administrator) so copilot answers to "how do I" questions cite the article or document section; the tool respects article visibility (internal, portal) through the user's token.
  - Notes: embeddings are computed by the provider layer (FR-47.13) and stored in Postgres `pgvector` in the `knowledge` database; index refresh within 5 min of article publish; answers without a citation above similarity 0.75 say so instead of guessing.
- **FR-47.15 Payment-date prediction** — Should. The system shall train a per-tenant model (Python, gradient-boosted trees) weekly on paid invoices (customer, terms, amount, invoice day-of-month, dunning history, disputes, seasonality) and score every open customer invoice with `predicted_payment_date` and `p_late_30d`, feeding the collections agent (FR-47.7), the wave-30 collections workspace and the `dso_days` forecast tile.
  - Notes: minimum 500 paid invoices per tenant before scoring is enabled, else a terms-plus-average-delay heuristic is used and labeled; model card per version; MAE on held-out set ≤ 6 days or the previous model stays active.

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

**Additional entities (FR-47.14, 47.15).** `knowledge_chunk`: chunk_id, tenant_id, source_type (ARTICLE, DOC, POLICY), source_ref, section, text (≤ 2 KB), embedding vector(1024), visibility, updated_at. `payment_prediction`: invoice_id, tenant_id, model_version, predicted_payment_date, p_late_30d numeric(5,4), features_hash, scored_at; `prediction_model`: model_version, tenant_id, trained_at, rows, mae_days, status (CANDIDATE, ACTIVE, RETIRED).

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
| POST | `/api/v1/knowledge/search` | hybrid search with citations (also exposed as MCP tool) | n/a (read) | CopilotUse |
| POST | `/api/v1/knowledge/documents` | upload tenant policy document for retrieval | `Idempotency-Key` | KnowledgeAdmin |
| GET | `/api/v1/ar/invoices/{id}/prediction` | payment prediction for an open invoice | n/a | CollectionsView |
| POST | `/api/v1/predictions/payment/retrain` | trigger retrain (also weekly job) | by week | AgentAdmin |

Validation: run start rejects when tenant budget state HARD, kill switch active, or agent version lacks passing eval; decision endpoint rejects if the approver equals `invoked_by_user_id` and the action's amount exceeds the SoD self-approval limit (wave 48).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.agent.run.completed.v1` | run end | run_id, agent_id, version, status, tool_calls[], tokens, cost_usd | audit, evals, copilot |
| `dev.passage.agent.approval.requested.v1` | gate reached | run_id, action_id, tool_name, proposal, confidence, risk_score | workflow inbox |
| `dev.passage.agent.budget.threshold.v1` | 80%/100% | tenant_id, month, pct, state | alerting, admin UI |
| `dev.passage.agent.killswitch.changed.v1` | toggle | scope, active, by_user | all agent workers |
| `dev.passage.prediction.payment.scored.v1` | weekly scoring or invoice change | invoice_id, model_version, predicted_payment_date, p_late_30d | collections workspace, analytics |
| `dev.passage.agent.version.promoted.v1` | production pointer change | agent_id, from_version, to_version, eval_run_id, by_user | audit, workers |

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

- **BR-47.11** A copilot answer that would require a tool the user lacks is answered with the reason (`AGT-0203`) and never with partial data from another user's cache.
- **BR-47.12** Payment predictions are recomputed for an invoice on any dunning, dispute or partial-payment event; a prediction older than 7 days is shown with a stale badge.
- **BR-47.13** Generated drafts inserted into a dunning letter or e-mail keep `origin=agent` and the run id on the resulting document; editing by the user clears neither.

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

- **AC-47.11** (FR-47.14) Given a published knowledge article "How to reverse a posted journal", when a user asks the copilot how to reverse a journal, then the answer cites the article section and no tool call touches ledger data.
- **AC-47.12** (FR-47.15) Given a tenant with 2,000 paid invoices, when the weekly retrain runs, then a model with MAE ≤ 6 days becomes ACTIVE and every open invoice has a prediction within 10 min.

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

### 9.47.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `ai.provider.default` | tenant | enum | ANTHROPIC | ANTHROPIC, BEDROCK, AZURE, OPENAI_COMPATIBLE | provider for new runs | Copilot (Azure OpenAI, Microsoft-managed) | Joule (SAP-managed), AI Core deployments |
| `ai.provider.secondary` | tenant | enum | none | same, residency-checked (BR-47.9) | failover target | none | none |
| `ai.residency_region` | tenant | enum | from wave-48 residency | EU, US, IN, CA, UK, MX | restricts provider endpoints | LCS region | RISE region / AI Core region |
| `ai.model.<agent_id>` | tenant | text | per `agent_version` | provider model ids | model pinned per agent version | none | `ISLM` model version |
| `copilot.enabled` | tenant / legal entity | bool | true | | shell panel on/off | Feature management "Copilot" | Joule activation |
| `copilot.modes` | tenant | set | ask, explain, draft, summarize | subset | enabled modes | `CopilotFeatureManagement` | Joule capabilities |
| `agent.<id>.mode` | legal entity | enum | SUGGEST | OFF, SUGGEST, AUTO | FR-47.5 behavior | `VendInvoiceAutomation` automatic posting switch | Business AI "propose vs auto-apply" |
| `agent.ap_coding.auto_threshold` | legal entity | numeric | 0.95 | 0.80–1.00 | min confidence for AUTO | none | Cash App ML threshold (`FINS_CASH_ML`) |
| `agent.ap_coding.auto_amount_limit` | legal entity | money | USD 5,000 / EUR 4,500 | ≥ 0 | AUTO cap | invoice automation tolerance | none |
| `agent.cash_app.max_invoices_per_payment` | tenant | int | 20 | 1–50 | BR-47.10 | `CustPaymMatch` settings | Cash App ML parameters |
| `agent.approval_expiry_h` | tenant | int | 72 | 1–240 | BR-47.3 | workflow escalation timeout | `SWWA` deadline |
| `agent.run_timeout_h` | tenant | int | 24 | 1–72 | run including approval wait | none | none |
| `agent.tool_timeout_s` | tenant | int | 60 | 10–300 | per activity | none | none |
| `ai.budget.monthly_in` / `monthly_out` | tenant | bigint | 50M / 10M | ≥ 0 | FR-47.11 | none | AI Core quota |
| `ai.budget.soft_pct` | tenant | int | 80 | 50–99 | alert threshold | none | none |
| `ai.run_token_cap` | tenant | int | 400,000 | ≤ 2M | per run | none | none |
| `ai.redaction.external` | tenant | bool | true (locked for external providers) | true | FR-47.12 | none | Joule "no training on customer data" |
| `ai.pseudonym_ttl_h` | tenant | int | 24 | 1–72 | BR-47.7 | none | none |
| `ai.killswitch` | tenant / global | bool | false | | halt runs | none | `ISLM` deactivate scenario |
| `ai.run_log_retention_months` | tenant | int | 24 | 6–120 | `agent_run`/`agent_action` retention (archived per FR-46.18) | none | none |
| `ai.eval.regression_points` | global | numeric | 1.0 | 0–5 | FR-47.9 gate | none | none |
| `knowledge.sources` | tenant | set | ARTICLES, DOCS | ARTICLES, DOCS, POLICY | FR-47.14 | none | none |
| `prediction.payment.min_rows` | tenant | int | 500 | 100–10,000 | FR-47.15 enablement | none | none |
| `prediction.payment.mae_max_days` | tenant | numeric | 6 | 1–30 | promotion gate | none | none |

Setup sequence: confirm tenant residency (wave 48) → select provider and store keys in Vault → enable copilot per legal entity → set each agent to OFF/SUGGEST/AUTO with thresholds → assign approver duties → set budgets → run the eval suite against the tenant's labeled holdout before switching any agent to AUTO.

### 9.47.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Agent runs | list inquiry | agent, status, user, date, entity | run id, version, trigger, tokens, cost, duration, outcome | `agent_run` | Vendor invoice automation workspace, Workflow history | `ISLM` scenario monitoring, Cash App ML log |
| Agent action audit | drill-down | run | seq, tool, redacted input/output hash, decision, approver, committed ref | `agent_action` | `SysDatabaseLog` (partial) | change documents (`CDHDR`) partial |
| Approval queue (agent) | list inquiry | agent, amount, confidence | proposal, diff, rationale, age | workflow inbox view | Workflow work items | `SBWP` inbox |
| Eval dashboard | KPI | agent, version, dataset | accuracy, precision, recall, F1, AUROC, latency, cost per run, trend | `eval_run` | none | none |
| Token budget status | KPI | tenant, month | used/limit in and out, state, forecast exhaustion date | `token_budget` | none | AI Core quota usage |
| Copilot usage | KPI | period, workspace | sessions, questions per mode, citation rate, refusal rate | `copilot_session` | none | Joule usage analytics |
| Tool catalog | list inquiry | domain, duty, approval mode | tool name, version, read-only, required duty | `tool_definition` | none | none |
| Kill-switch and promotion log | list inquiry | scope, date | action, by user, reason, timestamp | audit events | none | none |
| Prompt and version registry | drill-down | agent | versions, model id, prompt hash, eval status, production pointer | `agent_version` | none | `ISLM` versions |
| Payment prediction accuracy | operational report | period, customer group | MAE days, p_late calibration, predicted vs actual | `payment_prediction` | Customer payment predictions (Finance insights) | FSCM collections scoring reports |
| Provider error and latency | operational report | provider, period | 5xx rate, p95 latency, failovers | OpenTelemetry metrics | none | none |

### 9.47.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Model card | agent version promoted | agent, version, model id, training/eval data description, metrics, limitations, residency, approver | Markdown + PDF | agent version | retained with version (EU AI Act transparency support); evidence for CC7.2 |
| Eval report | CI eval run | dataset version, per-metric results vs thresholds, regressions, failing cases sample | HTML/PDF attached to CI | eval run id | 24 months |
| Agent run audit extract | request by auditor | run timeline, actions, approvals, redaction markers | JSON + PDF | run id | per wave-48 retention (24 months then archive) |
| Generated drafts (dunning letter, e-mail, PO text) | user clicks "insert draft" | the wave-30/17 document carries the text; badge `origin=agent` | as the host document | host document's sequence | host document's rules |

No document is posted or sent by this wave on its own; every outbound artifact is a host document from another wave with the agent origin recorded.

### 9.47.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Budget rollover | monthly, 1st 00:00 UTC | tenant | (tenant, month) unique | < 1 min | retry; previous month frozen | missing rollover |
| Token reconciliation | daily 03:00 | tenant, provider | provider usage report id | < 5 min | flag `estimated` rows | variance > 1% |
| Nightly production eval | daily 01:00 | agent | (agent_version, dataset_version, date) | < 30 min | alert on regression | metric drop > 1 pt |
| Approval expiry sweeper | hourly | tenant | action state | < 1 min | BR-47.3 | expired count |
| Model-card export | weekly Monday | agent | version | < 5 min | retry | none |
| Tool catalog regeneration | on deploy | domain | OpenAPI hash | < 2 min | deploy blocked if schema invalid | catalog drift |
| Knowledge index refresh | every 5 min / on publish event | source | source updated_at | < 1 min | retry 3× | index lag > 15 min |
| Payment model retrain and scoring | weekly Sunday 02:00 | tenant | model_version | < 20 min per tenant | keep previous ACTIVE | MAE > max |
| Run-log archive | weekly | tenant, month | FR-46.18 mover | < 30 min | as FR-46.18 | pending > 7 days |
| Provider health probe | every 30 s | provider | n/a | < 1 s | BR-47.9 failover | error rate > 5% |

### 9.47.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| CopilotUse | `POST /copilot/chat`, `/mcp` tools/list and read tools, `POST /knowledge/search` | none (tools are gated by the user's own duties) | every business role when `copilot.enabled` |
| AgentInvoke | `POST /agents/{id}/runs` | with the agent's commit duty when in AUTO mode (`AI-001`, HIGH: a user cannot both invoke AUTO and approve) | AP clerk (ap_coding), Cash clerk (cash_app), Planner (mrp_triage), Collector (collections) |
| AgentApprove (per agent) | `POST /agents/runs/{id}/actions/{a}/decision` | with AgentInvoke for the same run (BR: approver ≠ invoker) and SoD rules of the underlying posting duty | AP supervisor, AR supervisor, Planning lead, Credit manager |
| AgentView | `GET /agents/runs`, timeline, costs | none | Auditor, Finance manager |
| AgentAdmin | mode and thresholds, `PUT /versions/{v}/promote`, tenant kill switch, budgets | with AgentApprove (MEDIUM, `AI-002`) | Application administrator |
| PlatformAdmin (global kill switch) | `POST /agents/kill-switch` scope=global | n/a | Platform operator (break-glass, wave 48 FR-48.23) |
| PromptAuthor | create `agent_version` drafts, edit prompts and tool lists | with AgentAdmin promote (MEDIUM, `AI-003`: author cannot promote own version) | AI engineer |
| EvalRun (service) | `POST /agents/evals/runs` | n/a | CI service account |
| KnowledgeAdmin | `POST /knowledge/documents`, source settings | none | Application administrator |

### 9.47.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| AGT-0201 | error | Agent runs are halted by the {scope} kill switch. | kill switch active | AgentAdmin/PlatformAdmin clears it | FR-47.12 |
| AGT-0202 | error | Monthly token budget exhausted (BUDGET_EXHAUSTED); copilot runs in deterministic mode until {reset_date}. | budget state HARD | raise budget or wait | FR-47.11 |
| AGT-0203 | info | I cannot do that: you do not hold duty {duty} required by {tool}. | missing duty | request access | FR-47.2 / BR-47.11 |
| AGT-0204 | error | Agent version {v} has no passing evaluation and cannot be promoted. | eval missing or failed | run evals | FR-47.9 |
| AGT-0205 | warning | Token budget at {pct}% for {month}. | soft threshold | review usage | FR-47.11 |
| AGT-0206 | error | Provider {provider} is unavailable and no residency-compliant secondary is configured; run paused. | provider outage | configure secondary or wait | BR-47.9 |
| AGT-0207 | error | Provider endpoint {endpoint} is outside tenant residency {region}. | residency violation | choose compliant endpoint | FR-47.12 / BR-48.7 |
| AGT-0208 | warning | Approval request expired after {hours} h; nothing was posted. | BR-47.3 | re-run agent | BR-47.3 |
| AGT-0209 | error | You cannot approve an action from a run you started above the self-approval limit. | approver = invoker | route to another approver | 9.47.5 validation |
| AGT-0210 | error | Prompt rejected: unredacted PII detected before provider call. | redaction scanner | report; run is FAILED | FR-47.12 |
| AGT-0211 | info | Token usage estimated (provider reported no usage). | BR-47.4 | none | BR-47.4 |
| AGT-0212 | warning | No knowledge source matches your question with sufficient confidence. | similarity < 0.75 | rephrase or ask an administrator | FR-47.14 |
| AGT-0213 | warning | Payment prediction not available: fewer than {min_rows} paid invoices. | enablement threshold | heuristic shown | FR-47.15 |
| AGT-0214 | error | Tool {tool} failed with {status}; not retried. | 4xx | fix input or permissions | BR-47.6 |

### 9.47.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation under 3× load |
|---|---|---|---|---|---|---|
| Copilot first token | 10k users, 3 questions/user/day, peak 200 questions/min | 800 ms / 2 s / 4 s | 50 concurrent streams per BFF pod | `copilot_session` 20 GB | by (tenant, month) | queue with position shown; deterministic answers for ask-data when provider saturated |
| `tools/list` | per session | 20 ms / 80 ms / 150 ms | 500/s | n/a | cached per (user duties hash, catalog version) 5 min | n/a |
| Tool call via gateway | 5M/month | +5 ms / +15 ms / +30 ms over API | 1k/s | n/a | n/a | gateway autoscale |
| Agent run (excl. approval) | 20k runs/day, peak 500 concurrent | 8 s / 30 s / 60 s | 500 concurrent per Temporal namespace | `agent_run` 15 GB, `agent_action` 80 GB (redacted) | partition by month | runs queue; SLA warning at 2× |
| Eval suite (5 agents) | per commit | < 30 min | n/a | `eval_run` 5 GB | n/a | nightly only |
| Redaction pass | per prompt ≤ 100 KB | 20 ms / 60 ms / 120 ms | n/a | n/a | n/a | n/a |
| Knowledge search | 50k/day | 100 ms / 300 ms / 600 ms | 50/s | `knowledge_chunk` 10 GB per 100k articles | `pgvector` HNSW (m=16, ef=64) per tenant | ef lowered to 32 |
| Payment scoring | 1M open invoices | < 10 min batch | 2k invoices/s | `payment_prediction` 3 GB | (tenant, invoice) | spreads over 2 windows |

### 9.47.20 Localization and compliance hooks

- Response language follows the user's locale (wave 2); glossary terms (duty names, document types) come from the label framework so German users see "Buchungsbeleg", not a translation of the English.
- PII categories redacted before external providers follow the wave-42 classification extended by country packs (DE: religion, works-council membership; IN: Aadhaar; US: SSN; MX: CURP/RFC; FR: NIR); pseudonymization is mandatory for all tenants with residency EU or UK.
- Provider residency: tenants with residency EU/UK/IN may only use endpoints in-region (BR-48.7); the open-weights adapter is the fallback where no in-region hosted provider exists; the admin console shows the compliant provider list per region.
- Transparency: generated text is labeled in the UI in the user's language; the model card and action audit are the evidence objects for AI-governance questions (EU AI Act Article 50 transparency support; NIST AI RMF map/measure functions).
- Extension points: `country_pack.pii_categories[]`, `ai.residency_region` from wave-48 tenant residency, `agent.<id>.mode` per legal entity, prompt glossaries per language in `agent_version`.

### 9.47.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| (no GAP or PARTIAL register row carries current or proposed wave 47) | — | — | rows below are secondary references and inventory COVERED-47 rows verified against this wave |
| SAP 13.99.2 row 304 — 13.26 Solution database `IS01`/`IS02` (GAP, propose 45 "or wave 47 copilot answers") | GAP-45 | ADDED as FR-47.14 (retrieval over wave-45 articles); article authoring itself DEFERRED to wave 45 (owner) | copilot cites knowledge articles; symptom/solution structure lives in wave 45 |
| SAP 13.99.2 row 401 — 13.37 Signavio Process AI / Process Insights (PARTIAL-46) | PARTIAL-46 | SUPERSEDED by FR-46.17 + copilot `ask_data` over `mart.process_event` | closed in 9.46.21 |
| AX 12.3 inventory row "Customer payment predictions (D365 Finance insights)" | COVERED-47 (by name only) | ADDED as FR-47.15 | prediction model now specified with gates |
| AX 12.30 inventory row "AI: model provider configuration, token budgets, kill switch" | COVERED-47 | confirmed by FR-47.11–47.13 and 9.47.13 | |
| AX 12.31 inventory rows "Copilot / Ask-the-data", "Copilot → Agent runs", "Copilot settings" | COVERED-47 | confirmed by FR-47.3, 47.4, 9.47.13 | |
| AX 12.21 inventory row "Planning → MRP exception triage agent" | COVERED-47 | confirmed by FR-47.7 | |
| SAP 13.37 inventory rows "Responsible AI", "Agent action audit", "Token/cost budget", "AI governance policies", "Business AI scenario activation" | COVERED-47 | confirmed by FR-47.5, 47.11, 47.12 and 9.47.13 (`agent.<id>.mode`) | |
| SAP 13.99.2 rows 348/352/354 — BRF+ / decision tables (PARTIAL-3, not this wave) | PARTIAL-3 | SUPERSEDED by FR-3.13 (wave 3); `POST /v1/rules/{code}/evaluate` is exposed as MCP tool `passage.rules.ruleset.evaluate`, agents never embed rules in prompts | listed for traceability only |

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
| Electronic signatures (21 CFR Part 11) | Electronic signature parameters, Signature requirements (`SIGSignatureSetup`), Signature log (`SIGSignatureLog`), Reason codes, user certificates | Digital signature (`DSIG`) in PI sheets/EBR, `ELSIG*` signature strategies, SSF | `esign_requirement`, `esign_record` with re-authentication and meaning (FR-48.13) | Full |
| Audit policies and audit cases | Audit policies (`AuditPolicyRule`, `AuditPolicyViolation`), rule types duplicate/keyword/list search/sampling, Audit cases (`CaseDetail`) | GRC Process Control continuous monitoring, Fraud Management detection strategies, AIS | `audit_policy` rules on FR-3.13 decision tables, `audit_violation`, `audit_case` (FR-48.14, 48.15) | Full |
| Process, risk and control library | Internal controls (`SysComplianceProcess`, `SysComplianceRisk`, `SysComplianceFiles`) | GRC Process Control (`GRFN_*`: processes, subprocesses, controls, risks, test plans) | `process_node`, `risk`, `control` linked to SOC 2 controls and evidence (FR-48.16) | Full |
| Electronic messaging | Electronic messages (`EMElectronicMessage`, `EMMessageProcessing`, processing actions, web service settings) | DRC eDocument cockpit (`EDOC_COCKPIT`), statutory reporting submission and response | `e_message` with type, status machine, processing actions, response tracking (FR-48.17) | Full |
| XBRL / iXBRL disclosure | Financial reporting XBRL export (limited), ER XBRL formats | Disclosure Management, Group Reporting XBRL, SAC for financial statements | `er_format` output type IXBRL with taxonomy registry (FR-48.18) | Full |
| Sanctions / denied-party screening | Third-party add-ons only | GTS Sanctioned Party List screening, Business Partner Screening (`/SAPSLL/SPL*`) | `screening_list`, `screening_hit` on parties and documents (FR-48.19) | Full |
| Export control and customs documents | Foreign trade parameters, Intrastat, commodity codes; export documents via third parties | GTS export management, license determination, customs declarations (`/SAPSLL/*`), `VX11N` | `trade_classification`, `export_license`, export document set (FR-48.20) | Partial (declarations through customs broker adapters) |
| Preference / origin | none | GTS preference processing (vendor declarations, long-term vendor declaration `/SAPSLL/PRE*`) | `origin_determination` from BOM roll-up (FR-48.21) | Full for USMCA and EU PEM |
| Emergency access | none (temporary security role assignment by admin) | GRC EAM firefighter IDs, FF log review | `elevated_session` with reason, time box and reviewer sign-off (FR-48.23) | Full |
| Read access logging | none | Read Access Logging (`SRAL`) | `read_log_policy` on classified fields (FR-48.25) | Full |
| Masked tenant copy | LCS database refresh (no masking) | TDMS, client copy with scrambling (`SCC4`/`SCCL`) | masked tenant clone (FR-48.26) | Improved |
| US workforce compliance | Worker compliance (US): I-9, ADA, visa, union agreements, EEO, VETS, ACA, FMLA | US HR infotypes (0077, 0094, 0442, 0495), ACA reporting (`PU_ACA`), new-hire reporting | US pack workforce data and outputs (FR-48.22, 48.31) | Full |
| Security incident and breach | none | none (customer ITSM) | `security_incident` with breach clock and notification tasks (FR-48.29) | Improved |

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

- **FR-48.13 Electronic signatures** — Must. The system shall require an electronic signature, configured in `esign_requirement` per (entity type, action or field, legal entity), before the guarded action commits: the signer re-authenticates (password plus TOTP or WebAuthn through Keycloak step-up, `acr=esign`), selects a reason code, sees the meaning of the signature (authored, reviewed, approved, released, verified) and the record summary, and the system stores an `esign_record` bound to the record version hash.
  - Notes: satisfies 21 CFR Part 11 §11.50 (name, date/time, meaning on the signed record and its printouts), §11.70 (signature-to-record linking by hash), §11.200 (two components at the first signing of a session, one at subsequent signings within 15 min); optional X.509 user certificate (PAdES/XAdES) for documents leaving the system; three failed re-authentications lock signing for 30 min and raise `ESG-0603`; consumers: quality release (wave 33), batch record and PI-sheet steps (wave 24), master-recipe approval, ECO release (wave 9), payment approval, period close; signatures are never editable — a "reset" creates a revocation record with reason by `ESignAdmin` and invalidates downstream approvals; the signature log is a read projection of `esign_record`.
- **FR-48.14 Audit policies** — Must. The system shall run post-hoc audit policies over expense reports (wave 43), vendor invoices (wave 19), purchase orders (wave 17) and general journals (wave 11), with rule types DUPLICATE (same vendor + amount ± tolerance + date window), KEYWORD (list match in text fields), LIST_SEARCH (party in a watch list, e.g. employees as vendors), SAMPLING (random n% or every k-th, seeded), THRESHOLD (amount above a value per category, premium/mileage limits), WEEKEND_HOLIDAY (posted outside business calendar) and AGGREGATE (sum per party per period above limit).
  - Notes: rule conditions and thresholds are FR-3.13 decision tables (`rules.eval('audit.<rule_type>', …)`), never a private engine; policies are scoped by organization node (legal entity, department) and effective-dated; a run evaluates documents in a date window, creates `audit_violation` rows (document, rule, score, evidence fields) and is idempotent on (policy version, document id); SAMPLING uses a stored seed so a rerun selects the same documents.
- **FR-48.15 Audit cases** — Must. The system shall group violations into `audit_case` records by case category (e.g. duplicate payment, policy breach, sampled review) with assignee, due date, status (OPEN, IN_REVIEW, RESOLVED_NO_ISSUE, RESOLVED_ISSUE, CLOSED, ARCHIVED), findings, recovery amount and linked documents; closing requires a resolution code; closed cases older than `audit.case_archive_days` move to the archive tier (FR-46.18).
- **FR-48.16 Process, risk and control library** — Should. The system shall maintain a hierarchical process library (process → subprocess → activity, up to 5 levels), a risk library (risk id, description, likelihood, impact, owner) and controls (control id, type preventive/detective, automation manual/automated/IT-dependent, frequency, owner, test procedure) linked many-to-many; controls map to SOC 2 TSC ids, SoD rules, audit policies and evidence collectors; process documents are files in the wave-5 document store with templates per document type and versions.
  - Notes: an automated control links to its evidence collector (FR-48.7) so the library shows last evidence and status; control tests (design and operating effectiveness) are recorded with sample, tester, result and exceptions.
- **FR-48.17 Electronic messaging lifecycle** — Must. The system shall track statutory submissions as `e_message` records of a message type (UK MTD VAT return, DE ELSTER UStVA, FR EDI-TVA, IN GSTR-1, MX DIOT, CA GST34 NETFILE, US 1099 FIRE) through statuses defined per type (e.g. CREATED → GENERATED → VALIDATED → SENT → ACKNOWLEDGED | REJECTED → CORRECTED), with processing actions per transition (run ER format, validate schema, call web service, import response, await user input) and additional fields (period key, receipt id, obligation id).
  - Notes: web service settings (endpoint, OAuth client or certificate from Vault, fraud-prevention headers for HMRC MTD) are per message type and legal entity; responses are parsed into structured status and stored with the raw payload; a rejected message keeps its history and a correction creates a new item with `supersedes` (BR-48.3); e-invoice clearance (CFDI, IRP, Peppol) stays in wave 35 and reports its status here for one inbox.
- **FR-48.18 XBRL and iXBRL disclosure** — Should. The system shall add output type IXBRL and XBRL to `er_format`, with a taxonomy registry (UK FRS 102/IFRS taxonomies for Companies House and HMRC CT600, ESEF ESMA taxonomy, US GAAP taxonomy reference) and a tagging map from wave-15 financial statement lines to taxonomy concepts, contexts (entity, period, dimensions) and units.
  - Notes: output validates against the taxonomy (XBRL 2.1 and Inline XBRL 1.1 conformance via Arelle in a Python job); ESEF packages are zipped per the reporting-package spec with the block-tagged notes; filing itself is through electronic messaging (FR-48.17) where the regulator offers an API, else a download.
- **FR-48.19 Denied-party and sanctions screening** — Must. The system shall screen parties (customers, vendors, contacts, banks, workers on request) and documents (sales orders, deliveries, purchase orders, payments) against `screening_list` sources (OFAC SDN and Consolidated, EU Consolidated Financial Sanctions, UK HMT/OFSI, UN Security Council, US BIS Entity List, Denied Persons List, Unverified List) refreshed daily, using normalized fuzzy matching (token-set ratio plus transliteration; default hit threshold 0.88) on name, address and country.
  - Notes: a hit sets the party to `SCREENING_BLOCK` and blocks order release, shipment, PO send and payment until a `TradeComplianceOfficer` clears it as false positive (with reason; whitelist entry per party and list entry) or confirms it; rescreening runs on any list delta and on party change; a provider adapter (e.g. Descartes, LexisNexis Bridger, Dow Jones) can replace the internal matcher per tenant; every decision is retained 5 years.
- **FR-48.20 Export control and foreign trade documents** — Should. The system shall classify products by export classification (US ECCN/EAR99, EU dual-use Annex I number, HS/HTS tariff codes from wave 13 Intrastat setup), determine license requirements per (classification, destination country, end use) from a FR-3.13 decision table shipped by the pack, track `export_license` quantities and values, block deliveries that need a missing or exhausted license, and generate the export document set: commercial invoice, packing list for customs, certificate of origin, EUR.1 / origin statement, and US EEI data (AES) or EU export declaration data for the broker.
  - Notes: shipment execution and carrier documents are wave 34; this FR adds the compliance decision, documents and broker data file (JSON/EDIFACT CUSDEC via wave-35 hub); direct submission to customs systems (ACE, ATLAS, Delta G) is through broker adapters only in v1.
- **FR-48.21 Origin and preference determination** — Could. The system shall compute preferential origin for manufactured items per trade agreement (USMCA, EU–UK TCA, EU PEM) by rolling up the wave-21 BOM with component origins from supplier declarations (`supplier_origin_declaration`, long-term, validity ≤ 24 months), applying tariff-shift and regional-value-content rules (USMCA RVC transaction-value and net-cost methods; EU max non-originating content %) and storing the result per (item, agreement, period) for the commercial invoice and origin statement.
- **FR-48.22 US workforce compliance data** — Should. The system shall add to the US pack the worker compliance fields and workflows: Form I-9 (sections 1–3, document types List A/B/C, expiry tickler 90/30 days, E-Verify case number), ADA accommodation requests, visa/immigration records (type, expiry, sponsor), union agreements (local, seniority date, dues), FMLA leave tracking (12-week entitlement on a rolling 12-month look-back, intermittent leave in hours, eligibility check 1,250 hours/12 months) and workers' compensation class codes per position and state.
  - Notes: fields use the wave-42 country-specific fields framework; I-9 documents are stored with access limited to `HRComplianceUS` and are covered by read access logging (FR-48.25).
- **FR-48.23 Emergency (break-glass) access** — Must. The system shall grant elevated roles through an `elevated_session` request with reason code, ticket reference, requested roles and duration (≤ 4 h, default 1 h), approved by a designated approver (or self-activated with mandatory post-review when `breakglass.self_activate=true`), issued as a short-lived Keycloak token with claim `elev=<session_id>`, and every API call in the session logged; the session log is assigned to a reviewer who signs off within 3 business days.
  - Notes: SoD evaluation still runs and records conflicts for the session; sessions end automatically; unreviewed sessions older than 3 business days raise `SEC-0708` and appear on the CC6.3 evidence report.
- **FR-48.24 Export governance** — Should. The system shall apply download policies per data classification (PII, FINANCIAL, CONFIDENTIAL from wave 42 and entity metadata): allow, allow with watermark and audit, require reason, require approval, deny; per role and per channel (grid export, explorer export, report download, API bulk read > 10k rows, MCP tool bulk read).
  - Notes: every export records user, purpose, row count, columns, classification and file hash; XLSX/PDF carry a visible watermark with user and timestamp when the policy says so; the export log is evidence for CC6.1/CC6.7.
- **FR-48.25 Read access logging** — Should. The system shall log reads of fields marked in `read_log_policy` (default: national ids, bank accounts, salary, health, I-9 documents, date of birth) at the API layer — who, when, which record and field set, channel, purpose — without logging the values, retained per policy (default 2 years) and searchable by subject for subject-access requests.
- **FR-48.26 Masked tenant clone** — Should. The system shall clone a tenant (all domain schemas, lakehouse namespace, documents optional) into a non-production tenant with masking rules applied in-flight per field classification: deterministic tokenization for keys that must stay joinable (party names, e-mails), format-preserving randomization (IBAN with valid checksum, tax ids), date shifting (± 0–30 days per subject, consistent), nulling (free text notes), keeping amounts and quantities.
  - Notes: masking runs in the clone pipeline, never on the source; clones are tagged `masked=true` and refuse outbound integrations (bank, e-invoice, e-mail) by default; used by wave-49 rehearsals and wave-50 load tests (BR-50.5).
- **FR-48.27 India pack depth** — Must. The IN pack shall cover GST components (CGST, SGST/UTGST, IGST, cess) with place-of-supply rules, reverse charge, ITC eligibility and GSTR-2B reconciliation; TDS sections (194C, 194J, 194I, 194Q, 194H) with thresholds, PAN-based rates (206AA), lower-deduction certificates, challan ITNS 281 generation and CIN capture, quarterly 26Q/27Q returns (FVU file) and Form 16A certificates; TCS (206C(1H)) with collection and 27EQ return; 26AS/AIS reconciliation import.
- **FR-48.28 HR legal content per country pack** — Should. Each pack shall ship HR-legal fields, rules and outputs: DE (works-council co-determination flags on HR actions, contract types befristet/unbefristet, statutory leave minimums, Mutterschutz/Elternzeit), FR (CDI/CDD/intérim contract types with CDD maximum durations, DPAE pre-hire declaration data, mutuelle), UK (right-to-work checks, statutory sick/maternity pay eligibility, pension auto-enrolment flags), IN (PF/ESI numbers, gratuity eligibility after 5 years), MX (IMSS registration, aguinaldo and PTU entitlements), CA (Record of Employment data, provincial statutory holidays).
- **FR-48.29 Security incident and breach notification** — Must. The system shall maintain `security_incident` records (source: alert, user report, vendor, pen test; severity Sev-1–4; affected tenants, data categories and approximate subject count; timeline entries; containment, eradication, recovery steps; root cause; post-incident review) and, when a personal-data breach is confirmed, start breach clocks per applicable regime: GDPR Art. 33 supervisory authority within 72 h of awareness, Art. 34 subjects without undue delay when high risk, UK ICO 72 h, US state laws (per state table, e.g. 30–60 days), CA PIPEDA "as soon as feasible", IN CERT-In 6 h for reportable incidents, with notification tasks, templates and a record of what was sent when.
  - Notes: incidents are evidence for CC7.3/CC7.4 (FR-48.7); a Sev-1 incident automatically links its on-call page (wave 50 FR-50.10); customer-facing notifications are generated per affected tenant with the tenant's DPA contact.
- **FR-48.30 Evidence store and auditor access** — Must. The system shall store control evidence artifacts in an object-store bucket with object lock (compliance mode, retention = audit period + 1 year, default 2 years), hash each artifact (SHA-256) into a per-control hash chain, and give external auditors a time-boxed read-only `Auditor` role scoped to an audit period, with sample requests (auditor selects population items, owners attach evidence) tracked to completion.
  - Notes: the audit package export (ZIP with manifest, hashes and chain heads) can be verified offline with a published script; evidence cannot be deleted before lock expiry by any role.
- **FR-48.31 US pack statutory outputs (workforce and assets)** — Should. The US pack shall produce EEO-1 Component 1 (job category × race/ethnicity × sex), VETS-4212, ACA 1094-C/1095-C (IRS AIR XML), state new-hire reports (state formats, 20-day deadline tracking), workers' compensation payroll report by class code and state, OSHA Forms 300/300A/301 and the ITA CSV upload from the wave-42 injury and illness register, and IRS Form 4562 depreciation detail from the wave-37 tax book.

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

**Additional entities (FR-48.13–48.31).**

**`esign_record`** (compliance)

| Field | Type | Required | Notes |
|---|---|---|---|
| esign_id | uuid | Y | PK |
| tenant_id | uuid | Y | RLS |
| requirement_id | uuid | Y | FK `esign_requirement` (entity_type, action, legal_entity, meaning, reason_codes[], certificate_required) |
| entity_type / record_id | text / uuid | Y | signed record |
| record_version_hash | text | Y | SHA-256 of the canonical record version |
| signer_user_id | uuid | Y | |
| meaning | enum | Y | AUTHORED, REVIEWED, APPROVED, RELEASED, VERIFIED |
| reason_code | text | Y | from `esign_reason` |
| auth_method | enum | Y | PASSWORD_TOTP, WEBAUTHN, CERTIFICATE |
| signed_at | timestamptz | Y | server time, UTC |
| certificate_fingerprint | text | N | when certificate signing is used |
| revoked_by_id | uuid | N | FK to revocation record |

**`audit_policy` / `audit_violation` / `audit_case`**: `audit_policy` (policy_id, tenant_id, document_type EXPENSE/VENDOR_INVOICE/PURCHASE_ORDER/GENERAL_JOURNAL, rule_type, ruleset_code (FR-3.13), org_scope_json, effective_from/to, schedule, version, status); `audit_violation` (violation_id, policy_id, policy_version, document_type, document_id, score, evidence_json, run_id, case_id, state OPEN/IN_CASE/DISMISSED); `audit_case` (case_id, tenant_id, category, assignee_id, due_at, status, resolution_code, recovery_amount, currency, findings, closed_at, archived_at).

**`e_message`**: message_id, tenant_id, legal_entity_id, message_type, period_key, status, format_run_id, payload_ref, payload_hash, response_ref, receipt_id, supersedes_id, additional_fields_json, last_action, last_action_at.

**`screening_hit`**: hit_id, tenant_id, subject_type (PARTY/DOCUMENT), subject_id, list_code, list_entry_id, score numeric(4,3), matched_fields, state (OPEN, FALSE_POSITIVE, CONFIRMED), decided_by, reason, decided_at; `screening_list`: list_code, source_url, version, loaded_at, entry_count.

**`trade_classification` / `export_license` / `origin_determination`**: classification (item_id, regime US_EAR/EU_DUALUSE/HS, code, valid_from); license (license_id, regime, number, destination_countries[], items[], qty_limit, value_limit, used_qty, used_value, expires_at); origin (item_id, agreement, period, originating bool, method, rvc_pct, non_originating_pct, computed_at, declaration_refs[]).

**`elevated_session`**: session_id, user_id, roles[], reason_code, ticket_ref, requested_minutes, approved_by, started_at, ended_at, reviewer_id, reviewed_at, review_result.

**`security_incident`**: incident_id, severity, source, detected_at, aware_at, affected_tenants[], data_categories[], subject_estimate, personal_data_breach bool, regimes_json (regime, deadline_at, notified_at), status (TRIAGE, CONTAINED, ERADICATED, RECOVERED, CLOSED), root_cause, pir_ref.

Other entities: `read_log_policy` (field_fqn, classification, retention_days), `read_log` (partitioned by day), `export_policy`, `export_log`, `process_node`, `risk`, `control`, `control_test`, `supplier_origin_declaration`, `taxonomy`, `xbrl_tag_map`, `clone_job`.

Invariants (additional): an `esign_record` is append-only and its `record_version_hash` equals the hash of the version committed by the guarded action; a document blocked by an OPEN `screening_hit` cannot transition to released/shipped/paid; `elevated_session.ended_at − started_at ≤ 240 min`; an `audit_violation` belongs to at most one case; evidence objects are under object lock until `retain_until`.
Indexing: `esign_record` (entity_type, record_id); `audit_violation` (policy_id, state); `screening_hit` (subject_type, subject_id, state); `read_log` partition by day, index (subject_ref, read_at); `e_message` (legal_entity_id, message_type, period_key).

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
| GET/PUT | `/api/v1/compliance/esign/requirements` | maintain signature requirements and reason codes | ETag | ESignAdmin |
| POST | `/api/v1/compliance/esign/sign` | sign a pending action (step-up token required) | by (requirement, record_version_hash) | signer's action duty |
| POST | `/api/v1/compliance/esign/{id}/revoke` | revoke with reason | by id | ESignAdmin |
| POST | `/api/v1/compliance/audit-policies/{id}/runs` | run audit policy for a window | by (policy version, window) | AuditPolicyRun |
| POST | `/api/v1/compliance/audit-cases` | create case from violations | `Idempotency-Key` | AuditCaseManage |
| POST | `/api/v1/ereporting/messages/{id}/actions/{action}` | execute processing action on an electronic message | by (message, action, status) | per report duty |
| POST | `/api/v1/trade/screening/check` | screen a party or document on demand | n/a (read) | TradeComplianceView |
| POST | `/api/v1/trade/screening/hits/{id}/decision` | clear or confirm a hit | by hit | TradeComplianceOfficer |
| POST | `/api/v1/trade/export-checks` | license determination for a delivery | n/a (read) | TradeComplianceView |
| POST | `/api/v1/security/elevated-sessions` | request break-glass session | `Idempotency-Key` | any user with an eligible role mapping |
| POST | `/api/v1/security/incidents` | open security incident | `Idempotency-Key` | SecurityIncidentManage |
| POST | `/api/v1/ops/tenants/{id}/masked-clone` | clone tenant with masking | by clone job id | PlatformAdmin + second approver |

Validation: pack install rejects if the entity's fiscal calendar is open-period incompatible; `sod/simulate` returns conflicts before assignment is saved; a `run` of a format outside `effective_from..to` is rejected.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.ereporting.document.submitted.v1` | regulator ack | mandate_id, format_version, document_ref, ack_id, status | AR/AP (mark invoice sent), compliance |
| `dev.passage.sod.conflict.detected.v1` | evaluation | user_id, rule_id, severity, roles[] | workflow, compliance |
| `dev.passage.sod.mitigation.approved.v1` | approval | conflict_id, approver, expires_at | audit |
| `dev.passage.privacy.request.completed.v1` | close | request_id, type, affected_domains[], delivered_at | audit |
| `dev.passage.compliance.evidence.collected.v1` | collector | control_id, evidence_id, hash | compliance UI |
| `dev.passage.compliance.esign.signed.v1` | signature stored | esign_id, entity_type, record_id, meaning, signer, signed_at | quality, production, audit |
| `dev.passage.compliance.audit.violation.created.v1` | audit policy run | policy_id, document_type, document_id, score | audit case UI, workflow |
| `dev.passage.trade.screening.hit.v1` | screening match | subject_type, subject_id, list_code, score | sales, procurement, payments (block) |
| `dev.passage.security.elevated.started.v1` / `.ended.v1` | break-glass | session_id, user_id, roles[], reason | SOC, audit, reviewer inbox |
| `dev.passage.security.incident.breach_confirmed.v1` | breach flag set | incident_id, regimes[], deadlines[] | privacy, notifications |
| `dev.passage.ereporting.message.status_changed.v1` | e-message transition | message_id, type, from, to, receipt_id | compliance UI, tax |

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
| Signature requirements | add requirement per entity/action, set meaning and reason codes | entity, action, entity scope, meaning, certificate flag | ESignAdmin |
| Signature dialog (shell component) | re-authenticate, pick reason, sign | record summary, meaning, reason, signer | signer |
| Signature log | filter by record, user, meaning; export | signed_at, signer, meaning, reason, record, hash | ComplianceView |
| Audit policies | define rule, link decision table, schedule, run now | policy, rule type, scope, schedule, last run | AuditPolicyAdmin |
| Audit cases | assign, review evidence, resolve, close | case, category, violations, assignee, due, status | AuditCaseManage |
| Process, risk and control library | tree edit, link risks/controls/evidence, record tests | process, risk, control, owner, last test | ComplianceAdmin |
| Electronic messages | run next action, view response, correct | type, period, status, receipt, last action | per report duty |
| Screening workbench | review hits, compare fields, clear/confirm | subject, list, entry, score, matched fields | TradeComplianceOfficer |
| Export compliance | classify items, maintain licenses, view blocked deliveries | item, ECCN/HS, license, usage, blocks | TradeComplianceOfficer |
| Break-glass sessions | request, approve, review log | user, roles, reason, duration, review state | SecurityAdmin / reviewer |
| Security incidents | triage, timeline, breach clocks, notifications | severity, status, deadlines, notified | SecurityIncidentManage |

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

- **BR-48.11** E-signature: the guarded action and the `esign_record` commit in one transaction; if the record changed between dialog open and sign (hash mismatch), signing fails with `ESG-0602` and the user re-reviews.
- **BR-48.12** Within a continuous session a second signature needs only the password component if the first signing was ≤ 15 min ago; after 15 min of inactivity both components are required again.
- **BR-48.13** A signed record's printouts and PDF renders show signer name, date/time in the legal entity's time zone with UTC offset, and meaning (wave-46 template element `esign_block`).
- **BR-48.14** Audit DUPLICATE rule: two vendor invoices are duplicates when vendor equal, amount within max(0.01, 0.5%), invoice date within 30 days and normalized invoice numbers (strip non-alphanumerics, leading zeros) equal or Levenshtein ≤ 1; posted credit notes are excluded.
- **BR-48.15** Screening: a party cleared as false positive is not re-flagged for the same list entry unless the party's name, address or country changes or the list entry changes.
- **BR-48.16** A delivery to a destination with a comprehensive embargo (pack table, e.g. CU, IR, KP, SY, Crimea/DNR/LNR regions) is blocked regardless of classification; only `TradeComplianceOfficer` with a license reference can release.
- **BR-48.17** Electronic message transitions follow the type's status machine; an action not allowed from the current status is rejected; a message in SENT for longer than the type's response timeout (default 72 h) raises `EMS-0904`.
- **BR-48.18** Breach clock starts at `aware_at`, not `detected_at`; deadlines are computed per regime in UTC and displayed in the tenant time zone; a missed deadline cannot be closed without a late-notification justification.
- **BR-48.19** Break-glass sessions cannot grant `SecurityAdmin` or `PlatformAdmin` to the requester's own user; those require a second PlatformAdmin.
- **BR-48.20** FMLA entitlement uses the rolling 12-month period measured backward from the first day of leave; intermittent leave is deducted in hours at the worker's scheduled weekly hours.

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

- **AC-48.11** (FR-48.13) Given a signature requirement on quality-order release with meaning RELEASED, when an inspector releases a lot, then the dialog requires password plus TOTP, the `esign_record` hash matches the released version, and the certificate of analysis PDF shows name, time and meaning.
- **AC-48.12** (FR-48.13) Given three failed re-authentications, when the user tries a fourth time within 30 min, then signing is locked with `ESG-0603` and a security event is logged.
- **AC-48.13** (FR-48.14, 48.15) Given 1,000 vendor invoices with 12 planted duplicates, when the DUPLICATE policy runs, then 12 violations are created, a rerun creates none, and grouping them yields one case per vendor.
- **AC-48.14** (FR-48.17) Given a UK MTD VAT return for period 26A1, when the user runs GENERATE → VALIDATE → SEND, then HMRC sandbox returns a receipt id stored on the message and status ACKNOWLEDGED.
- **AC-48.15** (FR-48.18) Given the UK entity's annual statements, when the iXBRL format runs, then the file passes Arelle validation against the FRS 102 taxonomy with 0 errors.
- **AC-48.16** (FR-48.19) Given a customer named like an OFAC SDN entry at score 0.92, when a sales order is released, then release is blocked, a hit is OPEN, and after clearance as false positive the order releases and no hit recurs on rescreen.
- **AC-48.17** (FR-48.20) Given an item with ECCN 5A002 shipped to a destination requiring a license and no license on file, when the delivery is confirmed, then it is blocked with `TRD-1003`.
- **AC-48.18** (FR-48.23) Given a break-glass session of 60 min, when it ends, then the token is invalid, every call is in the session log, and the reviewer inbox has a review task due in 3 business days.
- **AC-48.19** (FR-48.29) Given a confirmed personal-data breach affecting EU subjects with `aware_at` 2026-11-02T09:00Z, when the incident is saved, then a GDPR authority deadline of 2026-11-05T09:00Z and a notification task exist.
- **AC-48.20** (FR-48.26) Given a masked clone of the demo tenant, when compared to source, then 0 original e-mails, IBANs or tax ids appear, IBAN checksums are valid, and trial balances are identical.
- **AC-48.21** (FR-48.27) Given TDS section 194J at 10% on a vendor without PAN, when an invoice is posted, then 20% is withheld (206AA) and the next 26Q FVU file includes the deduction.

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

### 9.48.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `pack.primary` | legal entity | code | none | US, UK, DE, FR, IN, MX, CA | installed country pack | Legal entity country/region, `CountryRegionCodes` features | Company code country (`OBY6`), country version |
| `pack.version_pin` | legal entity | semver | latest | published versions | blocks auto-upgrade | Feature management | Support package level |
| `er.schema_validation` | tenant | enum | BLOCK | BLOCK, WARN | FR-48.3 gate | ER format validation | DRC validation |
| `er.output_retention_years` | legal entity | int | from pack (DE 10, FR 10, US 7, UK 6, IN 8, MX 5, CA 6) | ≥ pack minimum | output archive | ER job archive document type | DRC archive, ILM |
| `emsg.response_timeout_h.<type>` | legal entity | int | 72 | 1–720 | BR-48.17 | Electronic message processing settings | eDocument cockpit timeout |
| `sod.block_high` | tenant | bool | true | | FR-48.5 enforcement | SoD rule "block" behavior | GRC AC mitigation required before provisioning |
| `sod.max_mitigation_days` | tenant | int | 365 | 1–730 | mitigation expiry cap | none | Mitigating control validity |
| `sod.nightly_time` | tenant | time | 00:30 | | batch evaluation | Verify compliance batch | GRC batch risk analysis (`GRAC_BATCH_RISK_ANALYSIS`) |
| `certification.window_days` | tenant | int | 14 | 3–60 | campaign window | none | UAR review period |
| `certification.auto_revoke` | tenant | bool | false | | BR-48.9 | none | UAR auto-remove |
| `esign.second_component` | tenant | enum | TOTP | TOTP, WEBAUTHN | Part 11 two-component | Electronic signature parameters (certificate/password) | `ELSIG` strategy (system signature with authorization) |
| `esign.session_reuse_min` | tenant | int | 15 | 0–30 | BR-48.12 | none | none |
| `esign.lockout_attempts` / `lockout_min` | tenant | int | 3 / 30 | 1–10 / 5–1440 | lock after failures | none | SU01 lock |
| `audit.case_archive_days` | tenant | int | 365 | 30–3650 | FR-48.15 archiving | Close/archive audit cases | none |
| `audit.sampling_seed_policy` | tenant | enum | PER_RUN_STORED | PER_RUN_STORED, FIXED | reproducible samples | Audit sampling parameters | none |
| `screening.threshold` | tenant | numeric | 0.88 | 0.70–0.99 | hit score | none | SPL comparison threshold (`/SAPSLL/SPL_CUS`) |
| `screening.lists` | tenant | set | OFAC_SDN, OFAC_CONS, EU_CFSP, UK_OFSI, UN_SC, BIS_EL, BIS_DPL, BIS_UVL | | sources | none | SPL list types |
| `screening.provider` | tenant | enum | INTERNAL | INTERNAL, DESCARTES, BRIDGER, DOWJONES | adapter | none | GTS or third party |
| `trade.license_check` | legal entity | enum | BLOCK | BLOCK, WARN, OFF | FR-48.20 | none | GTS legal control (`/SAPSLL/LCLIC*`) |
| `breakglass.max_minutes` | tenant | int | 60 | 15–240 | session length | none | Firefighter validity |
| `breakglass.self_activate` | tenant | bool | false | | FR-48.23 | none | EAM decentralized firefighting |
| `breakglass.review_days` | tenant | int | 3 | 1–10 | review SLA | none | FF log review workflow |
| `readlog.retention_days` | tenant | int | 730 | 90–3650 | FR-48.25 | none | `SRAL` log retention |
| `export.policy.<classification>` | tenant / role | enum | PII: REASON+WATERMARK; FINANCIAL: AUDIT | ALLOW, AUDIT, WATERMARK, REASON, APPROVAL, DENY | FR-48.24 | none | none (custom) |
| `privacy.deadline_days` | tenant | int | 30 (GDPR) / 45 (CCPA) | regulation table | FR-48.10 | GDPR request settings | DPW deadlines |
| `retention.<record_class>` | legal entity | int years | from pack | ≥ statutory | FR-48.11 | D365 data retention (archive) | ILM policies (`IRMPOL`), residence rules |
| `residency.region` | tenant | enum | at provisioning | deployed regions | BR-48.7 | LCS region | RISE region |
| `secrets.max_age_days` | platform | int | 90 | 1–90 | FR-48.8 | none | none |
| `backup.drill_cron` | platform | cron | monthly 1st Sun 03:00 | cron | FR-48.9 | LCS DR | HANA backup test |
| `evidence.lock_years` | tenant | int | 2 | 1–10 | FR-48.30 | none | none |
| `incident.breach_regimes` | tenant | set | from packs + residency | GDPR, UK_GDPR, US_STATE, PIPEDA, CERT_IN, LFPDPPP | FR-48.29 | none | none |
| `masking.profile` | tenant | code | `default-v1` | profiles | FR-48.26 | none | TDMS scrambling rules |

Setup sequence per legal entity: assign primary pack → review pack upgrade preview → set retention per record class (cannot go below pack minimum) → map ER formats to mandates and message types → configure regulator credentials in Vault → load SoD seed rules and GRC import → define signature requirements (regulated tenants) → configure audit policies → enable screening lists → set export policies → schedule evidence collectors.

### 9.48.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Statutory reports catalog | list inquiry | country, mandate, period | report, format version, due date, last run, status | `mandate`, `er_format` | Electronic reporting workspace | DRC Run Statutory Reports |
| Electronic messages | list inquiry | type, entity, period, status | message, status, receipt id, last action, response | `e_message` | Electronic messages | `EDOC_COCKPIT` |
| VAT return (per pack) | statutory report | entity, period | boxes per pack (UK 1–9, DE Kz 81/86/66…, FR CA3 lines) | tax settlement (wave 13) | Sales tax payment report, ER VAT declaration | `RFUMSV00`, DRC VAT return |
| SoD conflicts | list inquiry | state, severity, rule, user | user, rule, roles, age, mitigation expiry | `sod_conflict` | SoD conflicts / unresolved | GRC risk analysis (user level) |
| Mitigation report | operational report | expiry window, approver | conflict, control, approver, expires_at | `mitigation` | none | GRC mitigation reports |
| Certification campaign status | KPI | campaign | % certified, revoked, overdue by manager | `certification_campaign` | none | UAR status |
| Signature log | list inquiry | record, signer, meaning, date | signed_at, signer, meaning, reason, hash | `esign_record` | Electronic signature log / report | `ELSIG` log, DSIG log |
| Audit policy violations | list inquiry | policy, document type, state | document, rule, score, case | `audit_violation` | Audit policy violations | GRC PC exceptions |
| Audit cases | list inquiry / operational report | category, status, assignee | case, violations, recovery, age | `audit_case` | Audit case inquiry / report | Fraud Management alerts |
| Control matrix | operational report | process, TSC, owner | control, risk, last test result, last evidence, exceptions | library + evidence | Internal control documents | GRC PC control matrix |
| Screening hits | list inquiry | state, list, subject type | subject, list entry, score, age, decision | `screening_hit` | none | `/SAPSLL/SPL_BL` blocked documents |
| Export-blocked deliveries | list inquiry | reason, destination | delivery, item, classification, license needed | `export_check` | none | GTS blocked documents (`/SAPSLL/BLRGSD`) |
| Read access log | drill-down | subject, user, field, date | read_at, user, record, fields, channel, purpose | `read_log` | none | `SRALMANAGER` log |
| Export log | list inquiry | user, classification, date | rows, columns, purpose, hash, policy | `export_log` | none | none |
| Break-glass sessions | list inquiry | user, review state | roles, reason, duration, calls, reviewer, result | `elevated_session` | none | GRC firefighter log report |
| Subject requests | list inquiry | type, state, deadline | days remaining, domains, officer | `subject_request` | GDPR request list | DPW, IRF results |
| Security incidents and breach clocks | KPI | severity, status | open incidents, MTTR, deadlines, notified on time % | `security_incident` | none | none |
| Evidence calendar | KPI | control, period | expected vs collected, exceptions | `control_evidence` | none | AIS (`SECR`) |

### 9.48.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| VAT / GST / sales-tax returns (7 packs) | tax period close | boxes per regulator schema, entity tax id, period, declarant | UK MTD JSON, ELSTER XML, FR EDI-TVA, GSTR JSON, DIOT TXT, GST34 fields, US state files | `e_message` id; regulator receipt | statutory; 6–10 years per pack |
| FEC (FR), GoBD export (DE), DATEV export | on demand / year-end | journal lines with prescribed columns | FEC TXT (18 columns), GoBD index.xml + CSV, DATEV CSV | run id | 10 years |
| SAF-T / audit file (where pack requires) | on demand | header, master files, GL entries, source documents | XML per national schema | run id | statutory |
| iXBRL accounts / ESEF package | annual | tagged statements and notes | XHTML iXBRL, ESEF ZIP | filing id | 10 years |
| 1099-NEC/MISC, 1096 (US) | year-end | payer, recipient, boxes | PDF copy B, IRS FIRE file | form sequence | 7 years |
| ACA 1094-C/1095-C, EEO-1, VETS-4212, OSHA 300/300A/301, Form 4562 (US) | annual | per form | PDF, IRS AIR XML, EEOC/OSHA ITA CSV | form year | 5–7 years (OSHA 5 years) |
| TDS 26Q/27Q, Form 16A, TCS 27EQ (IN) | quarterly | deductor TAN, deductees, sections, challans | FVU text file, PDF certificate | quarter + certificate number | 8 years |
| CFDI 4.0 (MX) | invoice (wave 35 clearance) | per SAT schema with UUID and QR | XML + PDF | series/folio | 5 years |
| Commercial invoice, packing list, certificate of origin, EUR.1/origin statement | export delivery | exporter, consignee, HS codes, origin, Incoterms, values, weights, license refs | PDF, broker JSON/EDIFACT | export document number per delivery | 5 years (customs) |
| E-signature manifest | signed record print | signer, date/time with offset, meaning, reason | PDF block (wave-46 element), PAdES when certificate | n/a | with the signed record |
| Subject access bundle | privacy request | index, data per domain, processing purposes | JSON + PDF | request id | delivery receipt kept 3 years |
| Breach notification letters | breach confirmed | controller, nature, categories, counts, contact, measures | PDF, e-mail, regulator portal forms | incident id | 5 years |
| SOC 2 audit package | on request | manifest, hashes, evidence index | ZIP | package id | evidence lock period |
| Certification campaign evidence | campaign close | manager decisions per user and role | XLSX, PDF | campaign id | 3 years |

### 9.48.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| SoD nightly evaluation | 00:30 tenant time | tenant, user hash | (tenant, date) | < 10 min for 10k users | retry; last good result kept | new HIGH conflicts |
| Mitigation expiry sweep | daily 01:00 | tenant | conflict state | < 1 min | reopen per BR-48.1 | expiring in 30 days |
| Evidence collectors | per control (daily/weekly/per deploy) | control | (control, period) | < 5 min each | EXCEPTION status, retry 3× | missing evidence |
| Statutory format runs | per mandate calendar | entity, period | `Idempotency-Key` per (format, period) | < 5 min per 100k lines | message stays GENERATED with error | schema failure |
| Electronic message response poller | every 15 min | message type | receipt id | < 1 min | BR-48.17 timeout | SENT > timeout |
| Audit policy runs | per policy schedule (default nightly 02:30) | policy, document type | (policy version, document id) | < 15 min per 1M documents | partial resume by window slice | violations spike > 3× baseline |
| Screening list refresh and delta rescreen | daily 05:00 UTC | list | list version | < 20 min for 1M parties | keep previous list; alert | list age > 48 h |
| Retention purge | monthly 2nd Sunday | record class, domain | purge manifest | < 4 h | BR-48.6 skips; manifest diff | purge errors |
| Read-log partition roll | daily | day | partition | < 5 min | retry | none |
| Secrets rotation | per secret ≤ 90 days | secret | Vault lease | < 1 min | rollback to previous version | lease age > 80 days |
| Backup PITR drill | monthly | tenant sample | drill id | < 1 h | Sev-2 (BR-48.8) | RPO/RTO breach |
| Breach deadline monitor | every 15 min | incident | n/a | < 1 s | page at T−12 h | deadline at risk |
| Certification campaign close | at window end | campaign | campaign state | < 10 min | BR-48.9 | overdue managers |
| Masked clone | on request | tenant, domain | clone job checkpoint per table | < 2 h for 100 GB | resume per table | masking rule miss |

### 9.48.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| LocalizationAdmin | pack install/upgrade, `retention.*` minimum checks | with ERDesign on the same pack (LOW) | Application administrator |
| ERDesign | ER designer, mandates registry, message types | with statutory submission duty (MEDIUM, `TAX-002`: design and submit) | Tax technologist |
| TaxReportSubmit | `ereporting/formats/{id}/run`, `submissions/{id}/submit`, message actions | with ERDesign (MEDIUM) | Tax accountant |
| SoDReview / SoDApprove / SoDAdmin | conflicts, mitigations, campaigns, rule maintenance | SoDApprove with SecurityAdmin (HIGH, `SEC-002`) | Internal control, Compliance officer |
| ESignAdmin | signature requirements, reason codes, revoke | with any signing duty on the same requirement (HIGH, `QA-001`) | QA systems administrator |
| AuditPolicyAdmin / AuditPolicyRun | audit policies, decision-table links, runs | with AP invoice posting (MEDIUM, `AUD-001`: audit own postings) | Internal audit |
| AuditCaseManage | cases, findings, recovery | with the audited document's posting duty (HIGH) | Internal audit |
| ComplianceAdmin / ComplianceView | control library, tests, evidence, audit package | ComplianceAdmin with PlatformAdmin (MEDIUM) | Compliance officer; Auditor (view, time-boxed) |
| TradeComplianceOfficer | screening decisions, licenses, classifications, embargo release | with SalesOrderRelease (HIGH, `TRD-001`) | Trade compliance |
| PrivacyOfficer / PrivacyIntake / LegalHoldAdmin | subject requests, holds | LegalHoldAdmin with ArchiveAdmin (MEDIUM) | DPO, Legal |
| SecurityIncidentManage | incidents, breach clocks, notifications | none | Security lead |
| BreakGlassApprove | approve and review elevated sessions | with requesting the same session (HIGH, BR-48.19) | Security lead |
| HRComplianceUS | I-9, visa, ADA, FMLA records | with Payroll run (MEDIUM, `HR-002`) | HR compliance |

### 9.48.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| LOC-0501 | error | Pack {code} {version} cannot be installed: fiscal calendar {cal} has open periods incompatible with the pack. | calendar mismatch | close or align periods | 9.48.5 validation |
| LOC-0502 | error | Retention for {class} cannot be below the statutory minimum of {years} years for {country}. | parameter under minimum | raise value | FR-48.11 |
| ERP-0801 | error | Output fails schema {schema}: {n} errors (first: {xpath} {msg}). | schema validation | fix mapping or data | FR-48.3 |
| ERP-0802 | error | Format {format} is not effective for period {period}. | effective dates | choose correct version | 9.48.5 validation |
| EMS-0901 | error | Action {action} is not allowed in status {status} for message type {type}. | status machine | run allowed action | BR-48.17 |
| EMS-0904 | warning | No response from {regulator} for {hours} h on message {id}. | timeout | poll manually; contact regulator | BR-48.17 |
| EMS-0905 | error | Regulator rejected message {id}: {code} {text}. | rejection | correct and resubmit | FR-48.17 |
| SOD-1101 | error | Assignment blocked: conflict {rule_id} ({severity}) between {duty_a} and {duty_b}. | HIGH conflict with block policy | remove role or mitigate | FR-48.5 |
| SOD-1102 | error | You cannot approve a mitigation for your own conflict. | self-approval | route to another approver | 9.48.4 invariant |
| ESG-0601 | info | Signature required: {meaning} for {entity} {id}. | requirement matched | sign or cancel | FR-48.13 |
| ESG-0602 | error | The record changed since you opened it; review the current version before signing. | hash mismatch | reopen | BR-48.11 |
| ESG-0603 | error | Signing locked for {minutes} min after repeated failed authentication. | lockout | wait or contact ESignAdmin | FR-48.13 |
| AUD-0701 | warning | {n} documents violate policy {policy}. | audit run | review in audit cases | FR-48.14 |
| TRD-1001 | error | {subject} matches {list} entry {entry} (score {score}); processing is blocked. | screening hit | TradeComplianceOfficer decides | FR-48.19 |
| TRD-1002 | error | Destination {country} is under embargo; delivery blocked. | embargo | release with license reference | BR-48.16 |
| TRD-1003 | error | Export license required for {item} ({classification}) to {country}; none valid. | license determination | obtain or assign license | FR-48.20 |
| SEC-0707 | error | Break-glass cannot grant {role} to yourself. | BR-48.19 | request second PlatformAdmin | BR-48.19 |
| SEC-0708 | warning | Break-glass session {id} not reviewed after {days} business days. | review overdue | reviewer signs off | FR-48.23 |
| SEC-0709 | error | Download denied by export policy for {classification} data. | export policy DENY | request approval | FR-48.24 |
| PRV-1201 | error | Erasure blocked: subject is under legal hold {hold}. | hold | wait for hold release | FR-48.11 |
| INC-1301 | warning | Breach notification to {authority} due in {hours} h. | breach clock | send notification | FR-48.29 |
| OPS-1401 | error | Restore drill missed target: RPO {rpo} s / RTO {rto} min. | drill failure | Sev-2 opened | BR-48.8 |

### 9.48.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation under 3× load |
|---|---|---|---|---|---|---|
| SoD simulate at assignment | 500 assignments/day | 200 ms / 2 s / 3 s | 20/s | `sod_conflict` < 1 GB | (tenant, user) | 5 s; never skipped |
| SoD nightly | 10k users × 300 roles | < 10 min | n/a | n/a | in-memory duty bitsets | 30 min |
| ER format run | 100k lines per return | 1 min / 5 min / 8 min | 10 concurrent per tenant | outputs 50 GB (10 years) | by (entity, period) | queued |
| E-signature commit | 5k signatures/day (regulated tenant) | 300 ms / 800 ms / 1.5 s (excl. user step-up) | 20/s | `esign_record` 2 GB | (entity_type, record_id) | n/a |
| Audit policy run | 1M documents/night | < 15 min | 1.5k docs/s | `audit_violation` 1 GB | (policy, state), by month | window split |
| Screening on document release | 50k checks/day, peak 200/min | 30 ms / 120 ms / 250 ms | 50/s | `screening_hit` < 1 GB; lists 2 GB | trigram + phonetic index on list names | fall back to async check that holds release |
| Delta rescreen | 1M parties | < 20 min | 1k parties/s | n/a | name token index | completes in next window |
| Read access logging | 2M sensitive reads/day | +2 ms / +5 ms / +10 ms | async write via Kafka | 300 GB/year | partition by day | buffer in Kafka, never drop |
| Subject request discovery | 50 requests/month | < 10 min per request | n/a | bundles 5 GB | PII classification index | queued |
| Evidence collection | 11 controls, ~400 artifacts/month | < 5 min each | n/a | 50 GB/year (locked) | by control/period | delayed, flagged |
| Masked clone | 100 GB tenant | < 2 h | 15 MB/s per table stream | n/a | per-table parallelism 8 | 4 h |

### 9.48.20 Localization and compliance hooks

This wave is the localization layer for every other wave. What varies and where it plugs in:

| Area | Varies by | Country packs touching it | Extension point |
|---|---|---|---|
| Tax determination and returns | country, state/province, item and party tax groups | all 7 | FR-3.13 decision tables with `country` input (sys layer), `er_format` per mandate, `e_message` types |
| Withholding | country, section | US (backup withholding, 1099), IN (TDS/TCS), MX (ISR/IVA retention) | wave-13 withholding engine rule sets; FR-48.27 |
| Statutory files and audit exports | country | DE (GoBD, DATEV), FR (FEC), IN, MX (contabilidad electrónica), UK (MTD) | `er_format` |
| Document layouts and legal mentions | country, language | all 7 | `country_pack.document_layouts[]`, wave-46 text blocks |
| Bank formats | country, bank | NACHA (US), BACS (UK), SEPA (DE, FR), NEFT/RTGS (IN), SPEI (MX), CPA 005 EFT (CA) | wave-19 payment format registry fed by pack |
| Calendars and fiscal years | country | IN April–March; holidays all | wave-6 calendars from pack |
| Number formats, address formats, tax-id validation | country | all | pack validators (GSTIN, RFC, USt-IdNr, SIREN/SIRET, VAT GB, BN) |
| Currency rounding | currency | JPY 0, KWD 3, CHF 0.05 cash rounding | wave-11 rounding rules |
| Retention | country, record class | DE/FR 10 y, IN 8 y, US 7 y, UK/CA 6 y, MX 5 y | `retention_policy`, archive mover (FR-46.18) |
| Privacy and breach regimes | residency, subject country | GDPR (DE, FR), UK GDPR, CCPA/US state, PIPEDA (CA), DPDP Act 2023 (IN), LFPDPPP (MX) | `incident.breach_regimes`, `privacy.deadline_days` |
| E-signature regimes | industry, country | US 21 CFR Part 11; EU eIDAS advanced signatures via certificate option | `esign_requirement.certificate_required` |
| Export control | exporter country, destination | US EAR, EU dual-use Regulation 2021/821, UK export control order, CA ECL | screening lists, classification regimes, license decision tables |
| HR legal | country | all 7 (FR-48.28, 48.22) | wave-42 country-specific fields framework |
| Language | user and document locale | en-US, en-GB, de-DE, fr-FR, hi-IN/en-IN, es-MX, fr-CA/en-CA | label framework import/export (wave 2 / AX "Translations") |

### 9.48.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.99.3 row 22 — 12.9.1 Compliance → Electronic signatures (link) | GAP | ADDED as FR-48.13 | |
| AX 12.99.3 row 23 — 12.9.2 Electronic signature log inquiry | GAP | ADDED as FR-48.13 | signature log inquiry in 9.48.14 |
| AX 12.99.3 row 24 — 12.9.2 SSRS: Electronic signature report | GAP | ADDED as FR-48.13 | signature log export / manifest in 9.48.15 |
| AX 12.99.3 row 25 — 12.9.3 Electronic signature → Reset/revoke signatures | GAP | ADDED as FR-48.13 | revocation record by ESignAdmin |
| AX 12.99.3 row 26 — 12.9.4 Electronic signature parameters, Signature requirements, Reason codes | GAP | ADDED as FR-48.13 | parameters in 9.48.13 |
| AX 12.99.3 row 79 — 12.29.1 Electronic signatures → Signature log / Reset signature | GAP | ADDED as FR-48.13 | |
| AX 12.99.3 row 81 — 12.29.1 Electronic signatures → Certificates (user signing certificates) | GAP | ADDED as FR-48.13 | optional certificate signing (PAdES/XAdES) |
| AX 12.99.3 row 84 — 12.29.2 Electronic signature inquiry | GAP | ADDED as FR-48.13 | |
| AX 12.99.3 row 85 — 12.29.3 Electronic signatures → Signature verification / Reset | GAP | ADDED as FR-48.13 | verification = hash recompute against record version |
| AX 12.99.3 row 86 — 12.29.4 Electronic signature parameters / requirements / reason codes / certificates | GAP | ADDED as FR-48.13 | |
| AX 12.99.3 row 90 — 12.30.1 Sys admin → Electronic signature (link) | GAP | ADDED as FR-48.13 | |
| AX 12.99.3 row 92 — 12.30.4 Sys admin → Electronic signature parameters (link) | GAP | ADDED as FR-48.13 | |
| AX 12.99.3 row 37 — 12.12.2 SSRS: OSHA 300/300A/301 (US) (wave 42, US pack output 48) | GAP-42 | ADDED as FR-48.31 (output); incident register DEFERRED to wave 42 (owner) | |
| AX 12.99.4 row 8 — 12.1.4 Tax (IN) → India-specific setup (tax components, TDS/TCS) | PARTIAL-48 | ADDED as FR-48.27 | |
| AX 12.99.4 row 12 — 12.2.3 Vendor invoice policy violations (R3 audit) | PARTIAL-48 | ADDED as FR-48.14 | |
| AX 12.99.4 row 16 — 12.2.4 Vendor invoice policies (R3 audit policy) | PARTIAL-48 | ADDED as FR-48.14 | |
| AX 12.99.4 row 55 — 12.9.1 Internal controls → Files (`SysComplianceFiles`) | PARTIAL-48 | ADDED as FR-48.16 | process documents in wave-5 document store |
| AX 12.99.4 row 56 — 12.9.1 Internal controls → Process library (`SysComplianceProcess`) | PARTIAL-48 | ADDED as FR-48.16 | |
| AX 12.99.4 row 57 — 12.9.1 Internal controls → Risk library (`SysComplianceRisk`) | PARTIAL-48 | ADDED as FR-48.16 | |
| AX 12.99.4 row 58 — 12.9.1 Audit → Audit cases (`CaseDetail`) | PARTIAL-48 | ADDED as FR-48.15 | audit cases here; generic case management stays wave 45 |
| AX 12.99.4 row 59 — 12.9.1 Audit → Audit policy violations list (`AuditPolicyViolation`) | PARTIAL-48 | ADDED as FR-48.14 | |
| AX 12.99.4 row 60 — 12.9.2 Audit policy violations inquiry | PARTIAL-48 | ADDED as FR-48.14 | |
| AX 12.99.4 row 61 — 12.9.2 Audit case inquiry | PARTIAL-48 | ADDED as FR-48.15 | |
| AX 12.99.4 row 62 — 12.9.2 SSRS: Audit policy report / Audit case report | PARTIAL-48 | ADDED as FR-48.14, FR-48.15 | |
| AX 12.99.4 row 63 — 12.9.2 SSRS: Internal control documents | PARTIAL-48 | ADDED as FR-48.16 | control matrix report |
| AX 12.99.4 row 64 — 12.9.3 Run audit policies (batch) | PARTIAL-48 | ADDED as FR-48.14 | job in 9.48.16 |
| AX 12.99.4 row 65 — 12.9.3 Close/archive audit cases | PARTIAL-48 | ADDED as FR-48.15 | archive via FR-46.18 |
| AX 12.99.4 row 66 — 12.9.4 Audit policies (rule types duplicate, keyword, list search, sampling, premium/mileage) | PARTIAL-48 | ADDED as FR-48.14 | rules on FR-3.13 decision tables |
| AX 12.99.4 row 67 — 12.9.4 Audit policy rule types | PARTIAL-48 | ADDED as FR-48.14 | |
| AX 12.99.4 row 68 — 12.9.4 Audit case categories | PARTIAL-48 | ADDED as FR-48.15 | |
| AX 12.99.4 row 69 — 12.9.4 Audit keyword lists / Audit sampling parameters | PARTIAL-48 | ADDED as FR-48.14 | `audit.sampling_seed_policy` |
| AX 12.99.4 row 70 — 12.9.4 Risk/control types, Process document templates | PARTIAL-48 | ADDED as FR-48.16 | |
| AX 12.99.4 row 77 — 12.10.2 SSRS: Expense audit (audit policy results) | PARTIAL-48 | ADDED as FR-48.14 | |
| AX 12.99.4 row 80 — 12.10.3 Run audit policies on expense reports | PARTIAL-48 | ADDED as FR-48.14 | |
| AX 12.99.4 row 87 — 12.10.4 Expense → Audit policies (link) | PARTIAL-48 | ADDED as FR-48.14 | |
| AX 12.99.4 row 105 — 12.12.1 Worker compliance (US) → I-9, ADA, Immigration/visa, Union agreements | PARTIAL-48 | ADDED as FR-48.22 | |
| AX 12.99.4 row 109 — 12.12.2 SSRS: EEO-1 / VETS (US) | PARTIAL-48 | ADDED as FR-48.31 | |
| AX 12.99.4 row 111 — 12.12.3 HR → Year-end (US ACA 1095-C) | PARTIAL-48 | ADDED as FR-48.31 | |
| AX 12.99.4 row 112 — 12.12.4 Worker compliance (US) setup: I-9 document types, ADA, visa types, union agreements | PARTIAL-48 | ADDED as FR-48.22 | |
| AX 12.99.4 row 115 — 12.13.2 SSRS: Workers compensation report | PARTIAL-48 | ADDED as FR-48.22 (class codes), FR-48.31 (report) | |
| AX 12.99.4 row 116 — 12.13.2 SSRS: New hire reporting (US state) | PARTIAL-48 | ADDED as FR-48.31 | |
| AX 12.99.4 row 118 — 12.13.4 Leave of absence / FMLA tracking (US) | PARTIAL-48 | ADDED as FR-48.22 | BR-48.20 |
| AX 12.99.4 row 260 — 12.29.4 Organization → Audit policies (link) | PARTIAL-48 | ADDED as FR-48.14 | |
| AX 12.99.4 row 276 — 12.33.1 Electronic messages (`EMElectronicMessage*`) | PARTIAL-48 | ADDED as FR-48.17 | |
| AX 12.99.4 row 277 — 12.33.2 Electronic messages inquiry | PARTIAL-48 | ADDED as FR-48.17 | |
| AX 12.99.4 row 278 — 12.33.3 Electronic messages → Generate / Send / Receive response | PARTIAL-48 | ADDED as FR-48.17 | poller in 9.48.16 |
| AX 12.99.4 row 279 — 12.33.4 Electronic messages setup (message types, item types, statuses, processing actions, web service settings) | PARTIAL-48 | ADDED as FR-48.17 | |
| SAP 13.99.2 row 112 — 13.10 Disclosure management / XBRL (SAP DM, SAC) | GAP | ADDED as FR-48.18 | |
| SAP 13.99.2 row 206 — 13.17 Preference determination (GTS) | GAP | ADDED as FR-48.21 | Could priority; USMCA and EU PEM first |
| SAP 13.99.2 row 205 — 13.17 Foreign trade / export documents in delivery (GTS) (PARTIAL-13, propose 48) | PARTIAL-13 | ADDED as FR-48.20 | shipment execution stays wave 34; customs submission via broker adapters |
| SAP 13.99.2 row 339 — 13.28 Country-specific HR infotypes and legal changes | PARTIAL-48 | ADDED as FR-48.28 (non-US), FR-48.22 (US) | |
| SAP 13.99.2 row 344 — 13.30 GRC EAM firefighter | PARTIAL-48 | ADDED as FR-48.23 | |
| SAP 13.99.2 row 345 — 13.30 Anonymized production-to-test refresh (TDMS / client copy) | PARTIAL-48 | ADDED as FR-48.26 | used by waves 49 and 50 |
| SAP 13.99.2 row 347 — 13.30 Read Access Logging (`SRAL`) | PARTIAL-48 | ADDED as FR-48.25 | |
| SAP 13.99.2 row 350 — 13.31 Business Partner Screening / Watch List Screening (GTS) | GAP | ADDED as FR-48.19 | |
| SAP 13.99.2 row 355 — 13.31 Watch list screening / GTS compliance configuration | GAP | ADDED as FR-48.19 | parameters in 9.48.13 |
| SAP 13.99.2 row 400 — 13.36 Export governance (Excel export restrictions, download audit) | PARTIAL-48 | ADDED as FR-48.24 | |
| SAP 13.99.2 row 25 — 13.2 `J1INCHLN`/`J1INBANK`/`J1INCERT` (PARTIAL-13, IN pack) | PARTIAL-13 | ADDED as FR-48.27 | challans, CIN, Form 16A |
| SAP 13.99.2 row 53 — 13.4 `S_ALR_87010127`/`87010129` US Form 4562 (PARTIAL-37, US pack) | PARTIAL-37 | ADDED as FR-48.31 | from wave-37 tax book |
| SAP 13.99.2 row 257 — 13.22 `COEBR` electronic batch record (GAP-24, +48) | GAP-24 | ADDED as FR-48.13 (signature service); EBR itself DEFERRED to wave 24 (owner) | |
| SAP 13.99.2 row 261 — 13.22 Digital signature in PI sheets / batch records (`DSIG`) (GAP-24, cross-cutting 24/48) | GAP-24 | ADDED as FR-48.13 | wave 24 binds PI-sheet steps to `esign_requirement` |
| SAP 13.99.2 row 24 — 13.2 `F-40`/`F-36`/`FBW6` bills of exchange (GAP-36, or v1.x pack under 48) | GAP-36 | DEFERRED to wave 36 (owner of instruments); FR LCR and MX pagaré formats ship as pack ER formats once wave 36 provides the instrument lifecycle | none of the first 7 packs is blocked without it |
| SAP 13.99.2 row 383 — 13.35 PLM labeling (GLM) / specification management (PARTIAL-9, propose 48 labeling) | PARTIAL-9 | DEFERRED to wave 33 (product compliance owner); packs supply GHS/CLP phrase sets as text blocks (FR-46.15) | |
| AX 12.99.1 theme "Compliance extras" — SOC 2 evidence store (task brief) | PARTIAL (FR-48.7 collection only) | ADDED as FR-48.30 | object lock, hash chain, auditor role |
| AX 12.99.1 theme "Compliance extras" — incident/breach process (CC7.3/7.4 evidence without an incident record) | PARTIAL | ADDED as FR-48.29 | |
| AX 12.99.1 theme "Compliance extras" — license usage reporting (3) | PARTIAL-3 | SUPERSEDED by FR-3.14 (wave 3 usage snapshot) | listed for traceability |
| SAP 13.99.2 rows 348/352/354 — BRF+ in GRC (PARTIAL-3) | PARTIAL-3 | SUPERSEDED by FR-3.13; audit policies, license determination and embargo rules are FR-3.13 rule sets | |
| AX 12.99.1 theme "Alerts and notification rules" (wave 5) | GAP-5 | DEFERRED to wave 5 (owner); SoD, breach, screening and evidence alerts deliver through it | |

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
| Project beginning balances | Project beginning balance journals (`ProjJournalTable` type Beginning balance, `ProjBegBalJournalTrans_CostSales`, `ProjBegBalJournalTrans_OnAcc`) | Migration Cockpit objects for WBS, settlement rules; RA results transfer (`KKA2`), legacy cost transfer (`KB11N`/`KB15N`) | pack `project_wip` posting `project_opening_line` by category (FR-49.12) | Full |

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

- **FR-49.12 Project beginning balance journals** — Must. The system shall load, per open project, opening transactions by category (hours, expense, item, fee, on-account billing) with quantity, cost, sales value, transaction date, worker/resource, activity and dimensions as `project_opening_line`, flagged `beginning_balance=true`, together with WIP already recognized (WIP cost, WIP sales value, accrued revenue, invoiced on-account) and the remaining estimate, so that wave-41 estimates, billing rules and revenue recognition continue from the cutover state.
  - Notes: opening lines never post to the GL (the GL WIP and revenue balances arrive in pack `gl_balances`); control totals per project and category (cost, sales value, WIP, on-account) must equal the legacy `ProjTrans*`/`COSP`-`COSS` and RA results to 0.00; lines marked "chargeable, not invoiced" are invoiceable in Passage with the legacy transaction date; fixed-price projects carry percent complete and recognized revenue to date; time-and-material projects carry unbilled transactions at legacy sales price.

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

**Additional entity (FR-49.12).** `project_opening_line`: line_id, tenant_id, project_id, category (HOUR, EXPENSE, ITEM, FEE, ON_ACCOUNT), transaction_date, qty numeric(28,6), cost_amount, sales_amount, currency, worker_id, activity_id, dimensions_json, billing_status (CHARGEABLE_UNBILLED, BILLED, NON_CHARGEABLE), wip_cost, wip_sales, legacy_xref_id, run_id; unique (tenant_id, legacy_xref_id).

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
| GET | `/api/v1/migration/runs/{id}/projects/{project}/opening` | opening lines and totals per project and category | n/a | MigrationView |

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

- **BR-49.11** Project opening lines are excluded from GL posting and from the wave-41 cost-to-GL reconciliation for the cutover period; they are included in project estimates, billing and revenue-recognition calculations.
- **BR-49.12** A project whose opening totals do not reconcile to the legacy project subledger cannot be invoiced or estimated in Passage until the mismatch is resolved (status `OPENING_MISMATCH`).

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

- **AC-49.11** (FR-49.12) Given an AX T&M project with 120 chargeable unbilled hour transactions, when `project_wip` loads, then 120 opening lines exist, totals per category match legacy, no GL posting was created, and the first Passage invoice proposal lists the 120 lines at legacy sales price.

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

### 9.49.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `migration.source_system` | legal entity | enum | none | AX2012, D365FO, SAPECC, S4HANA | extractor selection | DIXF source data format | Migration Cockpit project source (`LTMC` direct transfer / staging) |
| `migration.cutoff_ts` | legal entity | timestamptz | none | ≤ freeze | extract cutoff | Data upgrade cutoff | Legacy data transfer date (`OAYE`) |
| `migration.opening_clearing_account` | legal entity | account | per pack | balance-sheet accounts | BR-49.2 offset | Opening balance journal offset | Data transfer clearing accounts (`OASV` offset) |
| `migration.tolerance.<pack>` | legal entity | money + reason | 0.00 | ≥ 0 with mandatory reason | FR-49.2 | none | none |
| `migration.quarantine_max_pct` | tenant | numeric | 1.0 | 0–10 | load blocks above share | DIXF error threshold | Migration Cockpit error handling |
| `migration.history_years` | tenant | int | 7 (10 when DE pack installed) | 1–20 | FR-49.4 | none | SDT / archive |
| `migration.parallel.unexplained_max` | tenant | int | 0 | 0 | FR-49.5 exit | none | none |
| `migration.parallel.explained_max_pct_revenue` | tenant | numeric | 0.1 | 0–1 | FR-49.5 exit | none | none |
| `migration.variance_rules` | tenant | rule set | `RND-001` | FR-3.13 decision table | EXPECTED classification | none | none |
| `migration.extract_parallelism` | tenant | int | 8 | 1–32 | extractor workers | DIXF threads (bundle size) | Migration Cockpit background jobs |
| `migration.load_batch_size` | tenant | int | 5,000 | 500–50,000 | API batch | DIXF bundle size | Migration Cockpit portion size |
| `migration.mask_pii` | rehearsal tenant | bool | true | | FR-48.26 profile | none | TDMS scrambling |
| `cutover.t0` | plan | timestamptz | none | | runbook anchor | none | none |
| `cutover.abort_offset_h` | plan | int | 36 | 12–72 | BR-49.9 | none | none |
| `cutover.step_overrun_amber_pct` / `red_pct` | plan | int | 110 / 125 | ≥ 100 | runbook colors | none | none |
| `coexistence.entities` | tenant | list | none | legal entities | FR-49.7 scope | none | none |
| `coexistence.sync_cron` | tenant | cron | `*/15 * * * *` | cron | master-data sync | none | ALE distribution model schedule (`BD64`) |
| `migration.project_opening.categories` | legal entity | set | HOUR, EXPENSE, ITEM, FEE, ON_ACCOUNT | subset | FR-49.12 | Beginning balance journal line types | Migration object project cost categories |

### 9.49.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Pack runs | list inquiry | pack, system, entity, status | records in/loaded/quarantined, duration, input hash | `pack_run` | DIXF execution history (`DMFExecution`) | Migration Cockpit monitoring (`LTMC` → Monitoring) |
| Control-total board | operational report | run, status | name, key, source, target, delta, tolerance | `control_total` | none (Excel) | Migration Cockpit "Migration results" |
| Quarantine | list inquiry | run, rule | record, rule failed, reason, age | `pack_quarantine` | DIXF staging errors | Staging table messages |
| Opening trial balance vs legacy | statutory/operational report | entity, currency | account × dimension, legacy, Passage, delta | `control_total` + ledger | Trial balance | `S_ALR_87012277`, `FAGLB03` |
| Open items reconciliation | operational report | entity, control account | open items sum, GL balance, delta | AP/AR + ledger | Vendor/customer balance list with aging | `S_ALR_87012082`/`87012172` |
| Inventory opening valuation | operational report | entity, site | qty, value by item/dims vs legacy | inventory + `control_total` | Inventory value report | `MB5L`, `MC.9` |
| Project opening balances | operational report | project, category | cost, sales, WIP, on-account vs legacy | `project_opening_line` | Project beginning balance journal list | `CJI3`/RA results (`KKA3`) |
| Parallel-run variance report | drill-down | parallel run, area, class | key, legacy, Passage, delta, rule, reason | `variance` | none | none |
| Cutover runbook status | KPI | plan | steps done/total, elapsed vs plan, overruns, go/no-go items | `cutover_step` | none | none |
| Legacy cross-reference lookup | list inquiry | system, table, legacy id | Passage entity and id | `legacy_xref` | none | none |
| History queries | drill-down | `source=legacy`, period | any metric | `history.unified.*` | AX legacy database kept online | SARI/archive, SDT history |

### 9.49.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Reconciliation certificate per pack | pack RECONCILED | run, entity, cutoff, each control total with source/target/delta, signer | PDF with e-signature (FR-48.13) | run id | audit evidence; 10 years with opening balances |
| Opening trial balance sign-off | `gl_balances` RECONCILED | entity, currency, account × dimension balances, legacy reference, controller signature | PDF, XLSX | entity + cutoff | statutory link between legacy and Passage books (GoBD/FEC continuity) |
| Parallel-run sign-off report | parallel run SIGNED_OFF | period, unexplained = 0, explained by rule, approvers | PDF | parallel run id | audit evidence |
| Cutover runbook and go/no-go record | plan generated / decision | steps, owners, timings, decision, approvers | PDF, CSV | plan id | 7 years |
| Rollback re-keying files | rollback executed | postings since T−0 as journals and documents | CSV, legacy import formats (AX DIXF package, SAP `LTMC` template) | rollback id | until re-keyed and reconciled |
| Legacy cross-reference export | on demand | system, table, legacy id, Passage id | CSV, Parquet | n/a | retained with history (FR-49.4) |

### 9.49.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Pack run (extract → validate → load → reconcile) | on demand / runbook step | pack, entity | `input_hash`, `legacy_xref` uniqueness | ≥ 50k records/min | resume from last completed stage (BR-49.1) | MISMATCH, quarantine > 1% |
| Historical load | on demand | system, table, year | Iceberg snapshot per (table, year) | ≥ 200k rows/min | resume per year | rows vs source count |
| Parallel-run replay | on demand per period | document type, day | replay cursor (legacy doc id order) | 42k documents in < 6 h | pause on API 5xx; resume | replay errors |
| Variance diff | after replay | area | `parallel_run_id` | < 30 min | rerun idempotent | unexplained > 0 |
| Coexistence delta sync | every 15 min | entity, object | legacy change pointer / timestamp | < 2 min | BR-49.10 dedupe; retry | backlog > 1 h |
| Post-go-live weekly reconciliation | weekly for 30 days | entity | (entity, week) | < 20 min | ticket | delta ≠ 0 |
| Rehearsal tenant provision / teardown | on pipeline trigger | tenant | clone job (FR-48.26) | ≤ 2 h | retry stage | > 2 h |
| Cutover step timer | continuous during cutover | plan | step state | n/a | amber/red per parameters | overrun > 125% |

### 9.49.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| MigrationRun | start pack runs, history loads, release quarantine, parallel runs | with MigrationReview (HIGH, `MIG-001`: load and sign off own variances) | Migration engineer |
| MigrationView | control totals, runs, xref | none | Controller, Auditor, Migration team |
| MigrationReview | classify and explain variances, sign off parallel run | with MigrationRun (HIGH) | Controller |
| OpeningBalanceSign | sign reconciliation certificates and opening trial balance (FR-48.13) | with MigrationRun (HIGH) | Controller, CFO |
| CutoverManager | generate plan, record go/no-go, initiate rollback (with second approver) | with step owner of go/no-go items they approve (MEDIUM) | Cutover manager |
| CutoverStepOwner | complete assigned steps | none | named per step |
| LegacyConnectionAdmin | legacy connection strings, coexistence adapter settings | with MigrationRun in production (MEDIUM) | Platform operator |

### 9.49.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| MIG-1501 | error | Extract cutoff {cutoff} is after the legacy freeze {freeze}. | late cutoff | re-extract | 9.49.5 validation |
| MIG-1502 | error | Control total {name} {key} mismatch: source {s}, target {t}, delta {d}. | load difference | drill to records | FR-49.2 |
| MIG-1503 | warning | {n} records quarantined ({pct}%) in run {run}. | validation failures | fix and release | FR-49.11 |
| MIG-1504 | error | Quarantine share {pct}% exceeds {max}%; load stopped. | too many failures | fix mapping | FR-49.11 |
| MIG-1505 | info | Re-run loaded 0 new records; all {n} already present by cross-reference. | idempotent re-run | none | FR-49.10 |
| MIG-1506 | error | Open items for control account {acct} ({sum}) differ from migrated GL balance ({bal}). | BR-49.2 | reconcile both packs | BR-49.2 |
| MIG-1507 | warning | Asset {asset}: next depreciation {p} differs from legacy projection {l}. | BR-49.6 | review depreciation profile | BR-49.6 |
| MIG-1508 | error | Project {project} opening totals do not reconcile ({category} delta {d}). | FR-49.12 mismatch | fix extract or mapping | BR-49.12 |
| MIG-1509 | error | Parallel run cannot be signed off: {n} unexplained variances ({amount}). | open variances | explain or fix | FR-49.5 |
| CUT-1601 | error | Step {step} cannot start: go/no-go items {items} are open. | sequence rule | complete items | 9.49.4 invariant |
| CUT-1602 | warning | Step {step} at {pct}% of planned duration. | overrun | escalate | 9.49.6 rules |
| CUT-1603 | error | Rollback requires two distinct approvers. | single approver | add second | 9.49.5 validation |
| CUT-1604 | warning | Abort deadline T+{h} h in {minutes} min with open go/no-go items. | BR-49.9 | decide | BR-49.9 |

### 9.49.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation under 3× load |
|---|---|---|---|---|---|---|
| Pack load | 1.3M rows per synthetic year, up to 50M for large customers | n/a (batch) | ≥ 50k records/min | `legacy_xref` ~1 KB per migrated record (50 GB per 50M) | `legacy_xref` unique (system, company, table, id) + (passage_entity, passage_id) | parallelism capped; window extends, rehearsal timing updated |
| Control-total computation | per run | < 10 min after load | n/a | `control_total` < 1 GB | (run_id, status) | 30 min |
| Historical load | 7 years, 300M rows | n/a | ≥ 200k rows/min | 1–3 TB Parquet | `history.<system>.*` partitioned by (year, company) | longer load before cutover, not during |
| Parallel-run replay | 42k–500k documents/month | per document p95 < 1 s via public APIs | 50 docs/s | rehearsal tenant only | n/a | replay over two nights |
| Xref lookup | per record detail open | 5 ms / 15 ms / 30 ms | 500/s | n/a | as above | n/a |
| Coexistence sync | 20k changes/day | delivery lag p95 < 15 min | 100 msgs/s | n/a | n/a | lag up to 1 h; alert |

### 9.49.20 Localization and compliance hooks

- Opening balances must preserve statutory continuity: DE GoBD and FR FEC require that the closing balances of the legacy books equal the opening balances in Passage and that the link is documented (opening trial balance sign-off, 9.49.15); the FR FEC for the cutover year is produced from legacy history plus Passage postings with `history.unified.*`.
- History years default to 10 when the DE or FR pack is installed (retention minimum), 8 for IN, 7 for US, 6 for UK/CA, 5 for MX (`migration.history_years` cannot go below the wave-48 retention minimum).
- Gap-free document numbering: legacy invoice numbers are kept in `legacy_xref` and shown on reprints; new fiscal sequences start in Passage per pack rules (MX CFDI series/folio, IN invoice series per GSTIN and fiscal year, FR chronological numbering).
- Tax: open AR/AP items keep their original tax amounts and tax codes mapped through the pack tax mapping; IN GST ITC balances and TDS certificates pending issue are migrated as balances with references; MX CFDI UUIDs of open invoices are retained for payment complements (REP).
- Payroll YTD (pack `payroll_ytd`) is limited to US and DE engines in v1; other countries load balances only (open question 2).
- Privacy: rehearsal extracts are masked per FR-48.26; production extracts are encrypted in transit and at rest, retained 90 days after go-live then deleted with a destruction record.
- Extension points: pack-specific mapping tables (tax code, payment method, chart of accounts) as FR-3.13 decision tables; `variance_rule` set per tenant; extractor plug-ins per source schema version.

### 9.49.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.99.4 row 91 — 12.11.1 Project management → Beginning balance journals | PARTIAL-49 | ADDED as FR-49.12 | opening lines by category with WIP and estimate state |
| AX 12.32 inventory row "Parallel-run replay (Passage migration factory)" | COVERED-49 | confirmed by FR-49.5 | |
| SAP 13.4 inventory rows `AS91`–`AS94` legacy asset transfer, `OASV` balance transfer, `OAYE`/`OAYF` legacy transfer date | COVERED-49 | confirmed by FR-49.1 (`fixed_assets`, `gl_balances`) and `migration.cutoff_ts` | |
| SAP 13.99.2 row 345 — TDMS masked refresh (PARTIAL-48) | PARTIAL-48 | ADDED as FR-48.26; consumed here by FR-49.8 rehearsal tenants | closed in 9.48.21 |
| AX 12.99.1 theme "Project depth — beginning balance journals (49)" | PARTIAL | ADDED as FR-49.12 | same row as above, theme view |
| SAP 13.99.2 row 24 — bills of exchange (GAP-36) | GAP-36 | DEFERRED to wave 36 (owner); pack `open_items` carries a `instrument_type` column mapped once wave 36 ships | migration of open bills of exchange depends on the instrument lifecycle |

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

### 9.50.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `REGION_ROLE` | region | enum | PRIMARY / PASSIVE | PRIMARY, PASSIVE, FENCED | write enablement | LCS environment type | HANA system replication role (`hdbnsutil -sr_state`) |
| `FAILOVER_MAX_LAG_S` | platform | int | 300 | 30–3600 | failover rejection threshold | LCS DR configuration | HSR log retention / `logshipping_timeout` |
| `failover.two_person_window_min` | platform | int | 5 | 1–30 | second confirmation window | none | none |
| `dns.ttl_s` | platform | int | 60 | 30–300 | failover propagation | Azure Traffic Manager TTL | DNS/virtual hostname switch |
| `cnpg.sync_quorum` | cluster | text | `ANY 1` | `ANY n`, `FIRST n` | FR-50.2 durability | Azure SQL geo-replication | HSR `SYNC`/`SYNCMEM` mode |
| `cnpg.cross_region_sync.ledger` | tenant (opt-in) | bool | false | | FR-50.4 cross-region sync | none | HSR multi-target sync |
| `kafka.min_insync_replicas` | platform | int | 2 | 2–3 | FR-50.3 | none | none |
| `mm2.lag_alert_messages` | platform | int | 100,000 | ≥ 1,000 | alert | none | none |
| `slo.<id>.target` | platform / tenant | numeric | per FR-50.1 table | | SLO and error budget | LCS SLA | RISE SLA |
| `slo.burn.fast` / `slow` | platform | ratio | 14.4×/1 h, 6×/6 h | | BR-50.6 | none | Focused Run alert thresholds |
| `release.auto_rollback_window_min` | platform | int | 30 | 10–120 | FR-50.7 | One Version rollback (none automatic) | SUM ZDO fallback |
| `release.cadence` | tenant | enum | WEEKLY | WEEKLY, BIWEEKLY, MONTHLY (pinned ring) | update ring | One Version update cadence, pause updates | Maintenance planner, FPS schedule |
| `provisioning.default_tier` | platform | enum | PROD | DEV, TEST, UAT, PROD | resource profile | LCS Tier-1 to Tier-5 | system landscape DEV/QAS/PRD |
| `provisioning.timeout_min` | platform | int | 15 | 5–60 | FR-50.8 | LCS deployment | client copy duration |
| `license.grace_days` | tenant | int | 30 | 0–60 | BR-50.8 | license enforcement grace | SAP license key temporary (28 days) |
| `license.enforce` | tenant | bool | true | | FR-50.12 | none | `SLICENSE` |
| `cost.fx_source` | platform | enum | ECB_MONTH_AVG | ECB_MONTH_AVG, FIXED | BR-50.10 | none | none |
| `capacity.model_version` | platform | text | latest | published | estimates | LCS subscription estimator | Quick Sizer version |
| `gameday.cron` | platform | cron | quarterly first Tuesday 14:00 UTC | cron | FR-50.6 | none | DR test schedule |
| `hypercare.days` | tenant | int | 30 | 14–90 | hypercare window | none | hypercare |
| `hypercare.change_freeze` | tenant | bool | true during hypercare | | Sev-1 fixes only | none | transport freeze (`STMS` lock) |
| `status_page.components` | platform | list | API, UI, Analytics, Integrations, Mobile | | public status | Service health (LCS) | SAP for Me availability |
| `docs.version_retention` | platform | int | 6 releases | 3–24 | docs site versions | Microsoft Learn versions | Help Portal versions |

### 9.50.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Region status | list inquiry | region | role, CNPG lag, MM2 lag, Temporal replication, DNS weight | `region` + Prometheus | LCS environment monitoring | `HDBSettings` replication status, `RZ20` |
| Failover history | list inquiry | date | from/to, RPO, RTO, hash ok, gap-free ok | `failover_event` | LCS DR history | DR test log |
| SLO and error budget | KPI | service, tenant, window | target, current, budget remaining, burn rate | Prometheus recording rules | LCS health metrics | Focused Run / Solution Manager SLR |
| Load-test campaign report | operational report | campaign, scenario | p50/p95/p99, error rate, throughput per S1–S7 | k6 results store | Performance SDK results | `ST03N` workload, LoadRunner reports |
| Top SQL | drill-down | cluster, period | query, calls, mean/p99 time, rows | `pg_stat_statements` | Trace parser, LCS SQL insights | `ST04`/`DBACOCKPIT`, `SQLM` |
| Capacity estimate | operational report | users, lines/h, storage | nodes, PG sizes, brokers, cost | `capacity_model` | LCS subscription estimator | Quick Sizer |
| Tenant cost | KPI | tenant, month | CPU, memory, storage, Kafka, lakehouse, AI tokens, USD/EUR | `tenant_cost` | none | none |
| License status | list inquiry | tenant | user types used vs licensed, state, expiry | `tenant_license` + wave-3 `UsageSnapshot` | License usage report | `USMM`, `SLAW` |
| Tenant inventory | list inquiry | region, tier | tenant, region, tier, residency, version ring | `tenant` | LCS environment list | `SM51`, landscape directory |
| Release history | list inquiry | version | digest, SBOM, migrations, rollbacks | release pipeline | LCS update history | `SPAM`/`SAINT` history |
| Hypercare dashboard | KPI | tenant | Sev-1/2 counts, backlog, daily reconciliation status, SLO | incidents + recon | none | hypercare reports |

### 9.50.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Release notes | each release | version, digest, features by module, migrations, breaking changes (none allowed for N−1), CVE fixes | HTML, Markdown, PDF | semver | kept for all versions |
| SBOM | each release | packages, versions, licenses, hashes | SPDX 2.3 JSON, CycloneDX | per image digest | 5 years (supply-chain evidence, CC8.1) |
| Signature attestation | release tag | cosign signature, Rekor entry | Sigstore bundle | per digest | permanent |
| DR game-day report | after game day | timeline, RTO/RPO measured, hash verification, actions | PDF | game day id | SOC 2 CC7.5 evidence (wave 48 lock) |
| Load-test certificate | campaign pass | scenarios, SLO table, environment, commit | PDF | campaign id | release evidence |
| Tenant license file | license install | tenant, user-type counts, expiry, signature | signed JSON (Ed25519) | license id | contract term |
| Monthly tenant cost statement | month close | resource lines, cost USD/EUR, model version | PDF, CSV | (tenant, month) | billing records 7 years |
| Go/no-go decision record | T+24 h | 14 checklist items, evidence links, approvers | PDF with e-signature (FR-48.13) | plan id | audit evidence |
| Hypercare exit report | hypercare end | exit criteria, incidents, reconciliation results, customer sign-off | PDF | tenant + date | project record |
| Status-page incident notice | incident | component, impact, timeline, resolution | HTML, e-mail, RSS | incident id | 1 year public history |

### 9.50.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Cost meter | monthly, 2nd 02:00 UTC | tenant | (tenant, month) | < 30 min | rerun idempotent | variance vs billing > 5% |
| Capacity model refresh | nightly | provider | model_version | < 10 min | keep previous | none |
| DR game day | quarterly | region pair | Temporal workflow with human tasks | < 4 h including fail-back | abort and fail back | RTO/RPO breach |
| Backup verification on production | weekly | cluster | drill id | < 1 h | Sev-2 | failure |
| SLO report | weekly Monday | service | week | < 5 min | retry | budget < 25% |
| License check | hourly | tenant | state transition | < 1 min | keep last state | expiry 30/7/1 days |
| Soak test | before each minor release | scenario | campaign id | 24 h | release blocked | memory growth > 10% |
| Load campaign S1–S7 | before each minor release and pre-cutover | scenario | campaign id | 2 h | release blocked | any SLO miss |
| Certificate expiry scan | daily | namespace | n/a | < 1 min | auto-renew via cert-manager; page at 7 days | expiry < 14 days |
| Replication slot and bloat check | every 15 min | cluster | n/a | < 10 s | runbook | slot lag > 1 GB |
| Docs build and link check | per release | site | commit | < 10 min | release blocked | broken links > 0 |

### 9.50.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| PlatformAdmin | `POST /ops/tenants`, `DELETE /ops/tenants/{id}` (with second approver), `POST /ops/failover`, `PUT /ops/tenants/{id}/license`, game day | with any tenant business duty (HIGH, `OPS-001`: operators do not post) | Platform operator (break-glass for failover) |
| PlatformView | `GET /ops/regions`, `/ops/slo`, `/ops/capacity/estimate` | none | SRE, Support |
| ReleaseManager | promote release by digest, pause ring, trigger rollback | with code approval of the same change (MEDIUM, `CHG-001`: four-eyes) | Release manager |
| OnCallResponder | runbook actions (restart, scale, replay), incident updates | none | SRE on-call |
| TenantAdmin | `GET /ops/tenants/{id}/cost`, user-type assignment within license | with PlatformAdmin (MEDIUM) | Customer administrator |
| LicenseIssuer (offline) | sign license files with the Ed25519 private key (HSM/Vault transit) | with PlatformAdmin (HIGH) | Passage commercial operations |
| StatusPagePublisher | publish incident notices | none | Support lead |
| CutoverDecisionOwner | record go/no-go at production cutover | with CutoverStepOwner of checklist items (MEDIUM) | Customer CFO / project sponsor |

### 9.50.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| OPS-1701 | error | Failover rejected: target region lag {lag} s exceeds {max} s. Use --accept-data-loss with a second approver. | lag above limit | wait or accept with approval | 9.50.5 validation |
| OPS-1702 | error | Failover needs a second PlatformAdmin confirmation within {min} min. | two-person rule | second confirmation | 9.50.6 rules |
| OPS-1703 | info | This region is read-only during failover; retry in {s} s. | fenced region (503) | client retry | BR-50.2 |
| OPS-1704 | error | Ledger hash chain broken for tenant {tenant} at journal {id}; user access blocked. | BR-50.3 | Sev-1 runbook | BR-50.3 |
| OPS-1705 | error | Residency region {region} is not deployed; tenant not provisioned. | provisioning validation | deploy region or change residency | 9.50.5 validation |
| OPS-1706 | warning | Release {v} rolled back automatically: fast burn on {slo}. | BR-50.6 | investigate before re-release | BR-50.6 |
| OPS-1707 | error | Migration drops column {col} still referenced in this release. | expand/contract linter | split releases | BR-50.7 |
| OPS-1708 | warning | Replication lag {lag} s on {cluster} exceeds 60 s. | lag | check network and WAL volume | 9.50.10 alerts |
| LIC-1801 | warning | License expires in {days} days. | expiry approaching | renew | FR-50.12 |
| LIC-1802 | warning | License in grace period: {used} full users active, {licensed} licensed. | over-use or expiry | true-up or reassign users | BR-50.8 |
| LIC-1803 | error | License expired; sign-in blocked. Administrators can still sign in and export data. | EXPIRED | renew | BR-50.8 |
| LIC-1804 | error | License signature invalid. | tampered or wrong key | reinstall issued file | FR-50.12 |
| COST-1901 | warning | Metered cost for {month} differs from provider billing by {pct}%. | meter drift | review query version | AC-50.7 |

### 9.50.19 Performance budgets and capacity

The SLO table in FR-50.1 is the binding budget. Capacity assumptions per reference tenant (10k concurrent users) follow.

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation under 3× load |
|---|---|---|---|---|---|---|
| Journal posting | 1M lines/h peak, 8M lines/day | 500-line batch 600 ms / 1.2 s / 2 s | 5k lines/s sustained | 1.2 TB ledger (lines + audit) | range by month, hash by tenant (16) | queue in outbox, 429 with `Retry-After` for batch APIs; interactive saves prioritized |
| Order intake | 100k lines/h | 150 ms / 300 ms / 500 ms | 30 lines/s per pod | 200 GB | by month | ATP degrades to cached availability (flagged) |
| Interactive mix | 10k users, think time 10 s | read 20 / 50 / 150 ms; save 120 / 250 / 400 ms | 1k req/s | n/a | PgBouncer transaction pooling | p99 doubles; no errors |
| Mobile scan | 2k scanners, 1 scan/5 s | 80 / 180 / 300 ms | 400 scans/s | 100 GB | by month | offline queue on device (wave 32) |
| Kafka | 60k msgs/s peak | produce p99 < 50 ms | 6 brokers | 30 TB retained 7 days (tiered) | partitions ≥ 2× consumers | consumer lag grows; lakehouse badge amber |
| Cross-region replication | WAL 40 MB/s peak | lag p95 < 30 s | n/a | n/a | n/a | lag alert > 60 s; failover window widens |
| Tenant provisioning | 20 tenants/day | < 15 min | 5 concurrent | ~2 GB per empty tenant | per-tenant schemas | queued |
| Failover | per event | RTO ≤ 1 h, RPO ≤ 5 min | n/a | n/a | n/a | runbook manual steps |

### 9.50.20 Localization and compliance hooks

- Region placement follows wave-48 tenant residency: EU tenants run primary and passive in EU regions only (e.g. `eu-west` ↔ `eu-central`); IN tenants in-country; failover never crosses a residency boundary (`POST /ops/failover` validates the pair).
- Retention on decommission follows the tenant's country packs (BR-50.9): encrypted export kept for the longest applicable record-class minimum (DE/FR 10 years) unless the customer takes custody and signs a release.
- Status page and release notes are published in English with German and French translations for customer-facing incident notices (contractual notice language per tenant).
- Time zones: maintenance windows, game days and hypercare stand-ups are scheduled in the tenant's time zone; SLO reports use UTC.
- Licensing and cost: cost statements show USD and EUR with ECB month-average FX (BR-50.10); taxes on Passage's own invoices are outside the product.
- Compliance evidence produced here (game-day reports, SBOMs, signatures, backup verification, change records) flows into the wave-48 evidence store for CC7.5, CC8.1 and A1.2/A1.3 (availability criteria).
- Extension points: per-provider Terraform modules, Helm values per region, `slo.<id>.target` per contract tier.

### 9.50.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| (no GAP or PARTIAL register row carries current or proposed wave 50) | — | — | rows below are inventory COVERED-50 rows and cross-references verified against this wave |
| AX 12.30 inventory rows "Tenant provisioning / Environment management", "Tenant provisioning templates / Environment catalog" | COVERED-50 | confirmed by FR-50.8 and 9.50.13 `provisioning.*` | |
| AX 12.30 inventory row "DR → Game days / Failover drills" | COVERED-50 | confirmed by FR-50.5, FR-50.6 | |
| AX 12.30 inventory row "Database → Backup / Restore / Refresh from production (LCS)" | COVERED-48 (DR in 50) | confirmed by FR-48.9, FR-48.26 (masked refresh) and FR-50.4 | |
| SAP 13.99.2 row 345 — TDMS masked refresh (PARTIAL-48, "propose wave 48/50") | PARTIAL-48 | ADDED as FR-48.26; consumed here by BR-50.5 load tests | closed in 9.48.21 |
| AX 12.30 rows "License inquiries", "License report / Named user counts", "Named user license count refresh" (PARTIAL-3) | PARTIAL-3 | SUPERSEDED by FR-3.14 (wave 3 `UsageSnapshot`); FR-50.12 enforcement reads it | |
| AX 12.99.1 theme "Alerts and notification rules" (wave 5) | GAP-5 | DEFERRED to wave 5 (owner); platform alerting is FR-5.14 + 9.50.10 | |
