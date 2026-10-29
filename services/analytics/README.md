# analytics

Wave 46 (PSG-46): data platform, analytics and embedded BI. This service owns
the governed metric catalog, the row-scope mirror that compiles every query
into Cube filters, and the daily metric reconciliation against the ledger. The
lakehouse, dbt models and Cube semantic layer live in `../../data/`.

## What slice 1 covers

- **Governed metrics (FR-46.6).** `internal/domain/metric.go` defines the
  initial published catalog (revenue, cogs, gross margin, DSO/DPO, inventory
  turns, OTIF, OEE) with version, grain, allowed dimensions and formula (no
  owner yet). Published versions are immutable; `Revise` yields the next draft.
  `cube_contract_test.go` keeps the catalog in step with the Cube model and the
  `cube.js` scope map in `../../data/cube`.
- **Query planning and scope mirror (FR-46.7, BR-46.9).** `POST
  /v1/analytics/query` checks the shape limits (10 measures, 8 dimensions,
  5-year day-grain range), resolves the metrics, and returns the Cube plan with
  the caller's legal-entity filter. No scope returns reason `NO_SCOPE`. A model
  without a security mapping fails closed with `ANA-0103`.
- **Reconciliation (FR-46.13, BR-46.10).** Tolerance comparison (`Compare`),
  the 30-day trust counter, and a store that writes each result with its
  `metric.reconciled` outbox event in one transaction. The store derives delta
  and status itself (`FinalizeReconciliation`); only BLOCKED is taken from the
  caller. Re-recording identical values emits nothing.
- **CDC schema-evolution classifier (FR-46.3, BR-46.6).** `internal/domain/cdc.go`
  classifies source changes as auto-apply or pause, and holds the pipeline state
  machine (RUNNING, PAUSED_SCHEMA, PAUSED_ERROR; LSN never decreases).
- **Test fixtures.** `testdata/fx-revenue-20.csv` (20-row revenue fixture) and
  `testdata/fx-schema-evolution.json` (schema-evolution matrix).

## What slice 2 covers

- **Data-quality rules and scores (FR-46.16, BR-46.15).** `GET /v1/catalog/dq-rules`,
  `GET/PUT /v1/catalog/dq-rules/{ruleId}` (create without `If-Match`, replace
  with `If-Match` of the current version; the version is the `ETag`), and
  `GET /v1/catalog/dq-rules/scores` for history. `RecordDQScore` stores one
  evaluation per rule and day with the below-threshold streak and writes
  `dev.passage.catalog.dq.scored.v1` in the same transaction: one owner task per
  rule and day, one steward escalation per run of three or more failures, and
  nothing while the rule is muted (7 days). Its caller, the hourly evaluation
  job, is a later slice.
- **Archive tier (FR-46.18, BR-46.14).** `archive_partition` state machine
  (PENDING, VERIFIED, DROPPED, RESTORED). The store registers a partition,
  records the mover's counts and content hashes (a mismatch stays PENDING with
  both copies and returns `ANA-0110`; a match is VERIFIED and writes
  `dev.passage.archive.partition.moved.v1`), drops, and restores.
  `GET /v1/archive/partitions[/{id}]` and `POST /v1/archive/partitions/{id}/restore`
  (30 days, idempotent by partition id) are served to `ArchiveAdmin`.
- **Report schedules (FR-46.10).** Five-field time-zone aware cron with `L`,
  destinations (e-mail, SFTP, in-app inbox), `POST /v1/analytics/schedules` with
  `Idempotency-Key` (24 h replay, `ANA-0114` on reuse with another body), list
  and get. Export policy (FR-46.9, BR-46.7) and retry delays are domain rules.
- **Process event log (FR-46.17, BR-46.16).** Case stitching by document
  reference, `UNLINKED`, top-20 variants with cycle, rework and automation
  statistics, OCEL 2.0 JSON and XES exporters (`internal/domain/process.go`).
- **Web.** `apps/web/src/modules/analytics`: data-quality scorecard, rule
  create and detail (score history, mute), scheduled reports list, create and
  detail, archive partitions list and detail with restore.

## What slice 3 covers

The swarm draft of slice 3 rebuilt the slice-2 store and API (DQ rules,
archive restore, schedules, idempotency) a second time; main already had the
reviewed versions, so none of it was taken. In particular the draft's
`If-Match` on archive restore was not adopted: the PRD makes restore idempotent
by partition id (§9.46.5). What slice 3 adds is the inquiry filters of §9.46.14
on the existing endpoints:

- `GET /v1/catalog/dq-rules?asset=&ruleType=&owner=` (scorecard: asset, rule
  type, owner; the owner id matches case-insensitively).
- `GET /v1/archive/partitions?domain=&period=&status=` (period is `YYYY-MM`).
- The scorecard and the archive list screens carry the matching filter forms.

## What slice 4 covers

- **Dashboards (FR-46.8).** `GET /v1/analytics/dashboards`, `GET/PUT
  /v1/analytics/dashboards/{id}` (create without `If-Match`, replace with the
  current version; the version is the `ETag`). A dashboard has 1 to 12 tiles,
  each bound to a published governed metric and a period (MTD, QTD, YTD,
  TRAILING_12M), and a refresh interval of 15 to 600 s (default 60). Tiles show
  their metric definition; tile values need Cube execution (later slice).
- **Explorer exports (FR-46.9, BR-46.7).** `POST /v1/analytics/exports` with
  `Idempotency-Key` plans the query with the caller's scope exactly like
  `POST /v1/analytics/query` and stores the plan in `export_job` (QUEUED). Above
  100,000 rows the mode is `TEMPORAL_JOB`; above 1,000,000 rows the request is
  `ANA-0101` (the cap is the tenant default and cannot be raised by the caller).
  An identical request by the same user and scope within one hour returns the
  existing job (`reused`, 200). `GET /v1/analytics/exports?user=&format=&from=&to=`
  is the export log. The runner that writes the file is a later slice.
- **Schedule dispatcher and deliveries (FR-46.10, AC-46.10).** `DispatchDue`
  claims due schedules one per transaction with `FOR UPDATE SKIP LOCKED`, calls
  a `Deliverer`, writes one `report_delivery` row per attempt (attempts of one
  run share `run_id`), retries after 1, 4 and 16 minutes, marks the fourth
  failure FAILED and moves to the next cron slot, and writes
  `dev.passage.analytics.report.delivered.v1` with a delivered attempt.
  `GET /v1/analytics/schedules/{id}/deliveries?status=` lists the attempts.
  The every-minute job that calls `DispatchDue` for each tenant and the PDF/XLSX
  renderers and destination adapters are later slices.
- **Catalog lineage (FR-46.12).** `lineage_edge` (source feeds target),
  `POST /v1/catalog/lineage/edges` (CatalogSteward, idempotent by edge) and
  `GET /v1/catalog/lineage/{asset_fqn}?depth=` (CatalogView), which walks the
  edges both ways in SQL and returns each asset with its distance in hops.
- **Web.** Dashboard list, detail and editor; export log and export request;
  deliveries on the schedule detail; lineage view with an add-edge form.

## Layout

```
cmd/analytics/            entrypoint: dbx.Connect, optional Migrate, httpx.Serve
internal/domain/          pure rules: metrics, formulas, query, scope, reconcile, cdc
internal/api/             HTTP handlers and duty middleware
internal/store/           Postgres store (RLS via dbx.WithTenantTx) and outbox
internal/events/          event type constants and payloads
migrations/               0001_init.sql (outbox, metric_reconciliation), 0002_grants.sql,
                          0003_dq_archive_schedule.sql (dq_rule, dq_score, archive_partition,
                          report_schedule, idempotency_keys and their grants),
                          0004_dashboards_exports_lineage.sql (dashboard, export_job,
                          report_delivery, lineage_edge and their grants), embed FS
Dockerfile                distroless image, built from the repository root
api/openapi.yaml          OpenAPI 3.1 for every route
events/asyncapi.yaml      AsyncAPI 3 for every event published
charts/analytics/         Helm chart
```

## Run

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true HTTP_ADDR=:8080 go run ./cmd/analytics
```

Requests need the gateway headers `X-Passage-Tenant`, `X-Passage-Duties`
(`AnalyticsView`, `FinanceView`, `CatalogSteward`, `CatalogView`,
`AnalyticsAuthor`, `AnalyticsExport`, `ArchiveAdmin`) and optionally `X-Passage-Legal-Entity`.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # adds store and API database tests
PASSAGE_TEST_APP_DATABASE_URL=postgres://app-role@... # adds the RLS isolation test
node --test ../../data/cube/                            # Cube queryRewrite RLS tests
```

Domain and API tests run without a database. The OpenAPI file is checked
against the mounted routes by `internal/api/openapi_test.go`.

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| COM-0001 | 400 | Request failed validation (shape, dates, unknown or mixed-grain metrics, disallowed dimension, malformed metric filter); `errors` names the field |
| COM-0428 | 428 | Replacing an existing data-quality rule or dashboard without `If-Match` |
| ANA-0101 | 422 | Export exceeds the 1,000,000-row cap |
| ANA-0102 | 403 | Export requested with no data scope (NO_SCOPE); reachable once the OPA scope decision is wired |
| ANA-0103 | 403 | Model has no security mapping (SCOPE_UNMAPPED) |
| ANA-0110 | 409 | Archive verification failed: counts or content hash differ (BR-46.14); the partition stays PENDING |
| ANA-0111 | 409 | The action does not fit the current state (restore of a partition that is not DROPPED, scoring a RETIRED rule, an evaluation older than the latest) |
| ANA-0112 | 404 | No such rule, partition, schedule or dashboard in the caller's tenant |
| ANA-0113 | 412 | `If-Match` does not name the current version of the rule or dashboard |
| ANA-0114 | 422 | `Idempotency-Key` reused with a different request |
| AUTH-0001 | 401 | No principal |
| AUTH-0003 | 403 | Missing duty |

`NO_SCOPE` is a successful response (`plan.reason`), not an error (BR-46.9).

## Dependencies

Only the platform kernel, chi (through the kernel router), pgx, decimal and
the standard library. No new third-party modules.

## Not built yet

See `NOT-IMPLEMENTED.md` for the deliverables that are later slices or handoffs.
