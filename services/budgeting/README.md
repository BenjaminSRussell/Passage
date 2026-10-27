# budgeting

Budget control for Passage (PRD 7.38, ticket PSG-38, wave 38, slice 1). The service holds budget control rule
versions, posts budget register entries, runs budget checks against per-combination balances under row locks,
and relieves encumbrances from matched invoice lines.

## What slice 1 does

- **Control rules (FR-38.3)**: immutable rule versions per legal entity and code, with warn and block thresholds,
  currency and effective date. Versions count up per (legal entity, code).
- **Register entries (FR-38.1)**: Original, Revision, Transfer and CarryForward entries post directly. Transfers must
  net to zero (CO-0604). Entries post to the budget ledger, not the universal journal.
- **Budget check (FR-38.4)**: the fixed order of operations is applied per line. Every combination the document
  touches is locked with `SELECT ... FOR UPDATE` in sorted order, so two concurrent checks cannot both pass against
  the last amount and cannot deadlock. Step 4 reads the line's own prior reservation from its stored encumbrance
  (never from the caller): a revised line is checked for its net change only, and a lowered line releases the
  difference (BR-38.3) but never below what invoices already relieved. The same line on another combination is
  refused (`BUD-0003`). Pass reserves the amount. Warn reserves and is reported. Fail reserves nothing. The
  `budget.override` duty turns a fail into a warn. Every check, including failed ones, is recorded as a
  BudgetCheckResult (step 8) and listed per rule version.
- **Available funds**: `GET /v1/budget/available?rule=&dims=&span=` lists each combination with budget, actuals,
  encumbrances, pre-encumbrances and available.
- **Encumbrance relief (FR-38.5, BR-38.4)**: relief = min(matched share, remaining reservation). The excess over
  the reservation is reported as `excess` so the caller can check it as a new amount.

Money is `numeric(19,4)` with a currency. Every command requires `Idempotency-Key`. Commands emit outbox events
(`dev.passage.budget.register.posted.v1`, `dev.passage.budget.check.completed.v1`,
`dev.passage.budget.encumbrance.relieved.v1`).

## Run and test

```sh
cd services/budgeting
go test ./...                      # unit tests; store and API DB tests skip without a database
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # runs the store tests against Postgres
DATABASE_URL=postgres://... MIGRATE_ON_START=true HTTP_ADDR=:8080 go run ./cmd/budgeting
```

Health: `GET /healthz`, `GET /readyz` (provided by `httpx.NewRouter`).

## Routes

| Method | Path | Duty |
|---|---|---|
| POST | `/v1/budget/control-rules` | `budget.configure` |
| GET | `/v1/budget/control-rules`, `/v1/budget/control-rules/{id}` | `budget.view` |
| POST | `/v1/budget/register-entries` | `budget.maintain` |
| GET | `/v1/budget/register-entries?rule=` | `budget.view` |
| GET | `/v1/budget/available?rule=&dims=&span=` | `budget.view` |
| POST | `/v1/budget/checks` | `budget.check` (`budget.override` turns fail into warn) |
| GET | `/v1/budget/checks?rule=&documentId=`, `/v1/budget/checks/{id}` | `budget.view` |
| GET | `/v1/budget/encumbrances?rule=&status=`, `/v1/budget/encumbrances/{id}` | `budget.view` |
| POST | `/v1/budget/encumbrances/{id}:relieve` | `budget.check` |

Lists return `{"items": [...]}`, at most 500 rows, newest first.

Contract: `api/openapi.yaml`. Events: `events/asyncapi.yaml`.

## Error codes

| Code | Meaning |
|---|---|
| `BUD-0001` | Budget control rule version not found |
| `BUD-0002` | Encumbrance not found |
| `BUD-0003` | The line is already reserved against another control combination or rule version (409) |
| `BUD-0004` | Budget check not found |
| `CO-0604` | Transfer lines do not net to zero |
| `COM-0001` | Malformed request body |
| `AUTH-0001` | No principal on the request |
| `AUTH-0003` | Missing duty |
| `idempotency_key_reuse` | Same Idempotency-Key with a different body (422) |
| `idempotency_in_progress` | A request with this key is still running (409) |

## Dependencies

Go only: the platform kernel and the libraries already in its module graph (chi, pgx/v5, shopspring/decimal,
rapid for property tests).

## Wave reference

Wave 38 of PSG-38. Remaining scope is listed in `NOT-IMPLEMENTED.md`.
