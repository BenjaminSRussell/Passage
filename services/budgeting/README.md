# budgeting

Budget control for Passage (PRD 7.38, ticket PSG-38, wave 38, slices 1 to 3). The service holds budget control rule
versions, posts budget register entries, runs budget checks against per-combination balances under row locks,
relieves encumbrances from matched invoice lines, keeps budget reservation documents (earmarked funds) and maintains
investment programs.

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

## What slice 2 adds

- **Budget reservation documents (FR-38.20, BR-38.14)**: FundsReservation, Precommitment, FundsBlocking and
  RevenueForecast documents with per-type numbers per legal entity (`FR-000001`, `PC-`, `FB-`, `RF-`). Creation runs
  the FR-38.4 check on every line under the same row locks and records it with document type `Reservation`; a failing
  line refuses the document (`CO-0601`). A fund-holding document adds its lines to the balance's `reservations`
  column, so available = budget - actuals - encumbrances - pre-encumbrances - reservations. A RevenueForecast is
  recorded but holds nothing (`revenue-increases-budget` is not built).
- **Consumption**: `POST /v1/budget/reservations/{id}:consume` draws a follow-on document line (requisition, PO,
  invoice or journal) on one reservation line. In one transaction the reservation line is relieved, the follow-on
  line's encumbrance is created (or raised) and the balance moves the amount from reservations to encumbrances, so
  available funds do not change (AC-38.12). FundsBlocking cannot be consumed (`CO-0631`). The event
  `dev.passage.budget.reservation.consumed.v1` goes through the outbox in the same transaction.
- **Close and expiry**: close releases the residual (FundsBlocking from Open only). The `reservation-expiry` job
  calls `POST /v1/budget/reservations:expire` with a date; Open and PartiallyConsumed documents whose expiry date is
  on or before it move to Expired and release their residual. The expiry date is the first day a document no longer
  holds funds, so consumption on or after it is refused (`BUD-0009`).

## What slice 3 adds

- **Investment programs (FR-38.21, BR-38.15, AC-38.13)**: a Capex program per legal entity, code and approval year
  with a budget. `:distribute` sets the budgets of child positions under one parent (the program itself, or a
  position), creating positions for new codes one level down (at most 6 levels, `BUD-0020`). Position codes are
  unique within the program; a code used under another parent is `BUD-0026`. Σ child budgets never exceed the
  parent's budget (`CO-0641` with the excess) and a position cannot drop below what it distributes (`BUD-0023`). The
  program row is locked for every command, so concurrent distributions serialize and the invariant holds.
- **Assignment and rollup**: `:assign` puts a capex request, asset under construction or project on a position with
  the plan, actual and commitment its owning service reports; assigning the same object again replaces its amounts
  and may move it. The program view rolls plan, actual and commitment up every subtree; available is budget −
  rolled-up actual − rolled-up commitment.
- **Carry-forward**: `:carry-forward` to the next approval year records, per position, its own unused slice: budget −
  distributed − the actual and commitment of its own assignments, floored at zero. Summed over positions this counts
  each unit of program budget once, so a parent and its children are never both carried for the same money. Each
  position carries once (`BUD-0021` when nothing is left); only approval year + 1 is accepted (`BUD-0032`). The event
  `dev.passage.budget.program.carriedforward.v1` goes through the outbox in the same transaction.

Money is `numeric(19,4)` with a currency. Every command requires `Idempotency-Key`. Commands emit outbox events
(`dev.passage.budget.register.posted.v1`, `dev.passage.budget.check.completed.v1`,
`dev.passage.budget.encumbrance.relieved.v1`, `dev.passage.budget.reservation.consumed.v1`,
`dev.passage.budget.program.carriedforward.v1`).

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
| POST | `/v1/budget/reservations` | `budget.reserve` (`budget.override` turns fail into warn) |
| POST | `/v1/budget/reservations/{id}:close`, `/v1/budget/reservations:expire` | `budget.reserve` |
| POST | `/v1/budget/reservations/{id}:consume` | `budget.check` |
| GET | `/v1/budget/reservations?rule=&type=&status=`, `/v1/budget/reservations/{id}` | `budget.view` |
| POST | `/v1/budget/investment-programs`, `/v1/budget/investment-programs/{id}:distribute`, `:assign`, `:carry-forward` | `budget.program.maintain` (InvestmentProgramMaintain) |
| GET | `/v1/budget/investment-programs?legalEntityId=`, `/v1/budget/investment-programs/{id}` | `budget.view` |

Lists return `{"items": [...]}`, at most 500 rows, newest first.

Contract: `api/openapi.yaml`. Events: `events/asyncapi.yaml`.

## Error codes

| Code | Meaning |
|---|---|
| `BUD-0001` | Budget control rule version not found |
| `BUD-0002` | Encumbrance not found |
| `BUD-0003` | The line is already reserved against another control combination or rule version (409) |
| `BUD-0004` | Budget check not found |
| `BUD-0005` | Reservation (or reservation line) not found |
| `BUD-0006` | Reservation is closed, expired or fully consumed, or cannot be closed from its status (409) |
| `BUD-0007` | Consumption above the line's open amount |
| `BUD-0008` | RevenueForecast holds no funds to consume |
| `BUD-0009` | Reservation reached its expiry date (409) |
| `CO-0601` | Reservation creation check failed: available below the requested amount |
| `CO-0604` | Transfer lines do not net to zero |
| `CO-0631` | Funds blocking cannot be consumed |
| `CO-0641` | Distribution exceeds the parent position budget by the amount named (BR-38.15) |
| `BUD-0020` | A new position would sit below level 6 |
| `BUD-0021` | Every position of the program was already carried forward (409) |
| `BUD-0023` | A position's budget would drop below what it distributes to its children |
| `BUD-0024` | Position not found in this program |
| `BUD-0026` | The position code already exists under another parent of the program (409) |
| `BUD-0030` | Investment program not found |
| `BUD-0031` | A program with this code exists for the legal entity and approval year (409) |
| `BUD-0032` | Carry-forward goes to the program's approval year + 1 only |
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
