# finreport: financial reporting (wave 15, PSG-15)

Report definitions (row, column and tree designer model), statement runs over
the reporting read model, and drill-down from a statement cell to its balance
keys. Phase 2 exit wave. Reads from its own `reporting` database, fed by ledger
events.

## Status: slice 1

Built in this slice:

- Definition model: rows (header, account_range, dimension_filter, total,
  calculation), columns (period expressions P, P-1, YTD, PY, PYTD, Q, FY, CALC),
  validation with expression references and cycle detection (FR-15.2, FR-15.3).
- Period expression evaluation and calendar handling (BR-15.1, BR-15.9).
- Statement evaluation over `reporting_balance` rows with a SHA-256 result hash
  that is reproducible for the same balances and definition (FR-15.6, BR-15.6).
  Balance sheets are cumulative from the opening period (period 0, which holds
  the brought-forward entries) of the column's fiscal year; income statements
  cover the column's periods and leave out closing entries (BR-15.2). Credit
  rows show `-net`; a calculation prefixed `raw:` uses stored net (BR-15.3).
  A column without a currency type uses the run's currency type.
- Calculation rows and CALC columns are stored as computed cells with no
  balance keys; every other cell's keys sum exactly to its value (BR-15.4).
- The run reads the balances and the ledger's HWM in one statement, so the
  recorded HWM is exactly the snapshot the cells reflect (BR-15.6).
- Cash-flow derivation with `CF_UNRECONCILED` and unclassified checks (BR-15.5).
- Definition list, drafts with revision-checked edits (ETag / If-Match),
  immutable published versions, publish, validate, JSON export and import with
  schema version, duplicate check and Idempotency-Key replay (FR-15.11, BR-15.8).
- Statement runs with Idempotency-Key replay, stored cells and balance keys,
  `statement.generated.v1` and `definition.published.v1` in the outbox.
- Run list (report library without outputs), drill level 1 (balance keys of a
  cell, sum checked) and the read-model lag view.
- Web module `apps/web/src/modules/finreport`: statement runs list, run form,
  statement grid with drill panel, definition list with JSON import, and the
  definition designer (rows, columns, validate, save draft, publish, export).

See `NOT-IMPLEMENTED.md` for what is deferred.

## Layout

```
cmd/finreport/          entrypoint (dbx.Connect, optional Migrate, httpx.Serve)
internal/domain/        definitions, expressions, periods, evaluation, cash flow (pure, no I/O)
internal/store/         Postgres store (pgx), tenant transactions, outbox writes
internal/api/           HTTP handlers under /api/v1/reporting
internal/events/        event types and payloads
migrations/             SQL applied in order; FS embedded in migrations.go
api/openapi.yaml        OpenAPI 3.1 for every endpoint
events/asyncapi.yaml    AsyncAPI 3 for every event
charts/finreport/       Helm chart
```

## Run

```
export DATABASE_URL=postgres://passage_migrator@localhost:5432/passage_reporting?sslmode=disable
export MIGRATE_ON_START=true   # dev only
go run ./cmd/finreport
```

Requests need the gateway headers `X-Passage-Tenant`, `X-Passage-User` and
`X-Passage-Duties` (see `platform/go/tenant`).

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./internal/store/...   # DB tests
```

Store tests are skipped without `PASSAGE_TEST_DATABASE_URL`. The row-level
security test also needs `PASSAGE_TEST_APP_DATABASE_URL` (a non-owner role) and
is skipped without it. The domain tests include a 200-row by 12-column
export/import round trip and a rapid property test (drill keys sum to the cell
value); the store tests cover export, re-import under a new code and identical
cells (AC-15.5) and hash reproducibility at the same HWM (AC-15.7). Contract
tests check that `api/openapi.yaml` lists exactly the registered routes and that
`events/asyncapi.yaml` lists every published event.

## Duties

| Duty | Routes |
| --- | --- |
| `reporting.definition.maintain` | definition PUT, publish, validate, export, import |
| `reporting.statement.run` | `POST /runs` |
| `reporting.statement.view` | definition list and GET, run list and reads, cells, drill level 1, lag |

## Error codes

Problem details use `type` `https://passage.dev/problems/<code>`.

| Code | Status | Meaning |
| --- | --- | --- |
| REPORT-0001 | 422 | Definition has no published version (`REPORT_DEFINITION_NOT_PUBLISHED`) |
| REPORT-0002 | 422 | Expression or reference invalid; body code mismatches the path |
| REPORT-0003 | 422 | Period does not exist in the supplied calendar (`REPORT_PERIOD_NOT_FOUND`) |
| REPORT-0008 | 422 | Cash flow does not reconcile (`CF_UNRECONCILED`), raised by the cash-flow derivation |
| REPORT-0009 | 422 | Balance-sheet accounts without a cash-flow tag (`CF_UNCLASSIFIED_ACCOUNTS`) |
| REPORT-0011 | 409 | Import of an existing code without `overwrite=true` (`REPORT_IMPORT_DUPLICATE`) |
| REPORT-0012 | 422 | Unsupported schema version on import (`REPORT_IMPORT_SCHEMA`) |
| REPORT-0020 | 404 | Definition or version not found |
| REPORT-0024 | 404 | Statement run not found (also for a run id that is not a UUID) |
| REPORT-0025 | 404 | Cell not found in the run |
| REPORT-0099 | 500 | Drill invariant failed: balance keys do not sum to the cell (never served) |
| conflict_version | 412 | Draft revision in `If-Match` is stale |
| precondition_required | 428 | Draft exists and `If-Match` was not sent |
| idempotency_key_reuse | 422 | Idempotency-Key reused for a different run or import request |
| idempotency_key_in_flight | 409 | A concurrent request with the same Idempotency-Key won; retry to get the replay |
| COM-0001 | 422 | Shape violation in a definition (code, name, kind, row and column fields) |
| COM-0002 | 400/422 | Malformed body, bad If-Match, bad list limit or run parameters (ledgerId not a UUID, calendar quarter not 1-4) |
| COM-0003 | 400 | `Idempotency-Key` header missing on `POST /runs` |
| REPORT-0026 | 422 | Definition has no rows or columns, or more than 2,000 rows or 60 columns |
| COM-0404 | 404 | Unknown definition action |
| AUTH-0001 | 401 | Missing tenant |
| AUTH-0003 | 403 | Missing duty (authz middleware) |

## Dependencies

Go modules only: the platform module and what it already pulls in (chi,
pgx/v5, shopspring/decimal, google/uuid) plus `pgregory.net/rapid` for tests.
