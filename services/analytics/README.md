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

## Layout

```
cmd/analytics/            entrypoint: dbx.Connect, optional Migrate, httpx.Serve
internal/domain/          pure rules: metrics, formulas, query, scope, reconcile, cdc
internal/api/             HTTP handlers and duty middleware
internal/store/           Postgres store (RLS via dbx.WithTenantTx) and outbox
internal/events/          event type constants and payloads
migrations/               0001_init.sql (outbox, metric_reconciliation), embed FS
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
(`AnalyticsView`, `FinanceView`) and optionally `X-Passage-Legal-Entity`.

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
| ANA-0103 | 403 | Model has no security mapping (SCOPE_UNMAPPED) |
| AUTH-0001 | 401 | No principal |
| AUTH-0003 | 403 | Missing duty |

`NO_SCOPE` is a successful response (`plan.reason`), not an error (BR-46.9).

## Dependencies

Only the platform kernel, chi (through the kernel router), pgx, decimal and
the standard library. No new third-party modules.

## Not in slice 1

See `NOT-IMPLEMENTED.md` for the deliverables that are later slices or handoffs.
