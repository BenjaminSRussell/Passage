# expense

Expense management, wave 43 (PSG-43), slices 1-5.

## What it does

Slice 5 persists cash advances, travel requisitions and card feed imports
(`internal/store/advances.go`, `requisitions.go`, `cardfeed.go`, migrations
`0004_travel_requisitions.sql` to `0006_card_feed.sql`, grants in `0007_slice5_grants.sql`):

- Cash advances (FR-43.15, BR-43.9): a worker requests an advance (amount, currency,
  purpose, trip end, optional travel requisition); it is stored SUBMITTED with its due
  settlement date = trip end + 30 days (8.43.13 default). The request is refused above
  the per-request maximum 2,000.00 (`EXP-ADV-001`), while the worker has a PAID or
  PARTIALLY_SETTLED advance still open after its due date (`EXP-ADV-002`; the due date
  itself is in time), and when the requisition belongs to another worker or legal
  entity or is REJECTED or CLOSED (`EXP-ADV-004`). An approver (MssApprove) approves or
  rejects it; an expense administrator records the AP prepayment that paid it, which
  sets paid = amount, moves it to PAID and writes
  `dev.passage.expense.cashadvance.paid.v1` to the outbox in the same transaction, and
  records cash returns, capped at the open balance (`EXP-ADV-007`), with the status
  re-derived from the amounts (PARTIALLY_SETTLED, SETTLED). AC-43.9's return of the
  180.00 remainder makes the advance SETTLED.
- Travel requisitions (FR-43.16): a worker creates a DRAFT requisition (purpose, trip
  dates, 1-20 destinations, 1-100 estimate lines by category); the estimated total is the
  sum of the lines. Submit moves it to SUBMITTED; an approver approves or rejects it.
- Own items blocked (8.43.17): the user who requested an advance or requisition may not
  decide it (`EXP-SOD-001`); a decision needs an authenticated user (`AUTH-0001`). The
  check compares gateway user ids, since the principal carries no worker id yet.
- Card feed import (FR-43.13): an expense administrator posts a CSV or OFX statement with
  the map of card last-four digits to cardholder worker ids. Each parsed row of a listed
  card becomes an UNASSIGNED card transaction in that worker's pool; rows the parser
  rejected and rows of unlisted cards are kept on the import as its quarantine. The file
  hash is unique per tenant, so the same file again is `EXP-CARD-002`. The pool is listed
  by worker and status.
- Every create and custom method takes an `Idempotency-Key`; the custom methods hash the
  operation, the record, the body and If-Match, so a key cannot be replayed onto another
  action. Custom methods honour `If-Match` (`EXP-ADV-008`, `EXP-TR-005`). Every table has
  `tenant_id`, forced row-level security with the tenant policy and the legal-entity
  scope policy; the application role `passage_app` has the grants of `0007`.

Slice 4 persists expense reports and lines (FR-43.11, `internal/store/reports.go`,
migration `0002_reports.sql`):

- A worker creates a DRAFT report (legal entity, worker, purpose, currency), adds lines
  (category, date, amount, merchant, attendees, receipt flag, justification, paid by
  employee or company card), removes lines while the report is DRAFT, and submits it.
- Lines are evaluated at commit (FR-43.12): every add and every delete re-evaluates the
  whole report with the default rule set at that day's date, so the PER_DAY meal rule
  and duplicate detection can change an earlier line's results, and each line stores its
  results. A line is stored whatever its outcome.
- Submit re-evaluates at the submit date. When any BLOCK applies or any REQUIRE_* is
  unsatisfied (8.43.5), the report stays DRAFT and the answer is a 422 with the first
  unsatisfied result's code and one error per result; each BLOCK result is still
  written to the outbox as `dev.passage.expense.policy.violated.v1` in the same
  transaction. A clean submit moves the report to SUBMITTED.
- Stored totals follow the lines in the same transaction and satisfy
  total = reimbursable + company-paid (a table check constraint, 8.43.4).
- Creating POSTs and submit take a required `Idempotency-Key`. Report and line keys are
  stored on the row (line keys per report); the submit outcome, success or refusal, is
  stored in `idempotency_keys`, so a retried refusal never publishes twice. The same key
  with another request is `EXP-IDEM-001`. Responses carry `ETag: "<version>"`; submit and
  line delete honour `If-Match` (`EXP-REP-004`).
- Every table has `tenant_id`, forced row-level security with the tenant policy, and the
  legal-entity scope policy (lines follow their report); every query also names the
  tenant, and a report for a legal entity outside the caller's scope is refused
  (`AUTH-0004`).

Slices 1-3 are the pure expense rules and calculations, also behind stateless HTTP
previews:

- Policy evaluation (BR-43.5, `internal/domain/policy.go`): typed, versioned rules
  evaluated over a report's lines at the submit date; the highest version effective on
  a line's transaction date wins, and a rule's basis is PER_LINE or PER_DAY. Shipped
  default set: receipt required for lines of 25.00 or more (`EXP-RCPT-025`,
  REQUIRE_RECEIPT), alcohol blocked (`EXP-ALC-001`, BLOCK), submission more than 60
  calendar days after the transaction date (`EXP-SUB-060`, BLOCK), duplicates of an
  earlier line's merchant, amount and date (`EXP-DUP-001`, BLOCK), meals over 75.00 a
  day without attendees need a justification (`EXP-MEAL-075` v3, PER_DAY,
  REQUIRE_JUSTIFICATION). A report is submittable when no BLOCK applies and every
  REQUIRE_* is satisfied.
- Travel requisition required by category (FR-43.16 parameter, `internal/domain/travel.go`):
  a line whose category the caller lists in `requisitionCategories` (ignoring case) is
  an `EXP-TR-002` BLOCK unless the report has an approved requisition
  (`hasRequisition`). The result follows the line's own rule results.
- Lodging cap (BR-43.5, `CheckLodging`): a stay costing more than the per-diem lodging
  rate × 1.5 × nights needs a justification (`EXP-LODG-150`, REQUIRE_JUSTIFICATION,
  `EXP-POL-001`); exactly the cap is allowed. The test never divides, so no per-night
  rounding moves the boundary. The rate is caller input until the pack tables load.
- Mileage (BR-43.5, `MileageAmount`): distance × rate, rounded half away from zero to
  the cent. The rate is the pack table value (IRS 0.70 USD/mile, BMF 0.30 EUR/km in
  2025), caller input.
- Cash-advance settlement at report approval (BR-43.9, `internal/domain/settlement.go`):
  the worker's PAID advances are applied oldest first, each taking min(its open amount,
  what is still unapplied); the AP invoice carries the rest. Each advance reports its
  open balance and status (PAID, PARTIALLY_SETTLED, SETTLED).
- Travel requisition variance (BR-43.13): actual above estimate × (1 + tolerance)
  requires justification (`EXP-TR-001`).
- Card-to-receipt matching (FR-43.13): same worker, merchant equal ignoring case,
  date within 2 calendar days, amount within 1% (defaults), never across workers;
  `MatchReceipts` pairs cards and receipts one to one, closest amount first.
  `MatchAll` (slice 3) adds what stayed unmatched and the auto-match rate (matched
  cards over all cards, AC-43.7) behind `POST /v1/expense/card-matches`, with the
  tenant tolerance as optional input (0-10 % and 0-10 days). Nothing is stored.
- Per diem (BR-43.6, `internal/domain/perdiem.go`): one trip of consecutive days.
  Lodging per charged night. The first and last day pay 75% of the M&IE rate (a
  one-day trip is both); provided breakfast, lunch and dinner are deducted at
  20/30/50% of the full-day rate, also on partial days, and a day never goes below
  zero. That is how GSA (FTR 301-11) and the German BMF rules take meal deductions:
  a partial day with breakfast pays 75% - 20% = 55% of the rate, not 75% of 80%.
  Each day is rounded half away from zero to the rate currency's minor unit. The
  rate table is caller-supplied pack data.
- Cash advance lifecycle and aging (FR-43.15, `internal/domain/advance.go`): legal
  status edges; open = paid - settled - returned - recovered - written off (BR-43.9
  options a-c), never negative; status derived with the same rule as report
  settlement; cash return capped at the open balance; overdue when open after trip
  end + limit (default 30 days; the due date is still in time); overdue advances
  block new advances (`EXP-ADV-002`).
- Travel requisition window and closure (BR-43.13, `internal/domain/requisition.go`):
  dates outside the approved window (inclusive); closure when the report posts or on
  the 60th day after the last travel date.
- Card feed parsing (FR-43.13, `internal/domain/cardfeed.go`): CSV with required
  columns (UTF-8 BOM ignored; a malformed row is rejected, it never ends the file)
  and OFX 1.x/2.x STMTTRN blocks (DTUSER as transaction date, SIC as MCC, each
  statement's own account and currency). Bad rows are reported with their file line,
  not fatal. Amounts are never rounded: more decimals than the currency allows
  (`platform/money` minor units) rejects the row. SHA-256 file hash for
  de-duplication. Spend is positive, refunds negative. Nothing is stored.

Reports are not approved, posted to AP or paid yet, so advances are not yet settled against them. See `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/expense/            entrypoint: dbx.Connect -> dbx.Migrate -> httpx.NewRouter -> Serve
internal/domain/        pure rules, settlement, variance, card matching, per diem,
                        advance aging, requisition window, card feed parsing,
                        requisition gate, lodging cap, mileage (no I/O)
internal/store/         Postgres persistence of reports, lines, cash advances, travel
                        requisitions and card imports (pgx, tenant transactions)
internal/events/        event type and payload
internal/api/           HTTP handlers and DTOs
migrations/             NNNN_*.sql applied by dbx.Migrate (outbox, idempotency_keys, reports,
                        lines, grants, requisitions, advances, card imports and transactions)
api/openapi.yaml        OpenAPI 3.1 for every endpoint
events/asyncapi.yaml    AsyncAPI 3 for policy.violated.v1 and cashadvance.paid.v1
charts/expense/         Helm chart
```

## Run

```
PASSAGE_ENV=dev SERVICE_NAME=expense DATABASE_URL=postgres://... \
  HTTP_ADDR=:8080 go run ./cmd/expense
```

## Test

```
go vet ./... && go test ./...
```

Domain and API tests are pure (the API tests use an in-memory report store). Store and
migration tests use `platform/go/testkit` and are skipped unless
`PASSAGE_TEST_DATABASE_URL` is set; run them as a role without superuser or BYPASSRLS so
the row-level security test is not skipped. `TestAppRole_CrossTenantRLS` also needs
`PASSAGE_TEST_APP_DATABASE_URL` (the `passage_app` role) and probes every table as that role.

## Endpoints

| Method | Path                                   | Duty                 |
|--------|----------------------------------------|----------------------|
| POST   | /v1/expense/report-evaluations         | expense.report.write |
| POST   | /v1/expense/cash-advance-settlements   | expense.report.write |
| POST   | /v1/expense/travel-variances           | expense.report.write |
| POST   | /v1/expense/per-diem-computations      | expense.report.write |
| POST   | /v1/expense/cash-advance-aging         | expense.report.write |
| POST   | /v1/expense/travel-requisition-checks  | expense.report.write |
| POST   | /v1/expense/lodging-checks             | expense.report.write |
| POST   | /v1/expense/mileage-computations       | expense.report.write |
| POST   | /v1/expense/card-matches               | expense.report.write |
| POST   | /v1/expense/card-transactions:parse    | expense.card.import  |
| GET    | /v1/expense/reports                    | expense.report.write |
| POST   | /v1/expense/reports                    | expense.report.write |
| GET    | /v1/expense/reports/{id}               | expense.report.write |
| POST   | /v1/expense/reports/{id}/lines         | expense.report.write |
| DELETE | /v1/expense/reports/{id}/lines/{lineId}| expense.report.write |
| POST   | /v1/expense/reports/{id}/submit        | expense.report.write |
| GET    | /v1/expense/cash-advances              | expense.report.write |
| POST   | /v1/expense/cash-advances              | expense.report.write |
| GET    | /v1/expense/cash-advances/{id}         | expense.report.write |
| POST   | /v1/expense/cash-advances/{id}/decide  | expense.approve      |
| POST   | /v1/expense/cash-advances/{id}/pay     | expense.advance.admin |
| POST   | /v1/expense/cash-advances/{id}/return  | expense.advance.admin |
| GET    | /v1/expense/travel-requisitions        | expense.report.write |
| POST   | /v1/expense/travel-requisitions        | expense.report.write |
| GET    | /v1/expense/travel-requisitions/{id}   | expense.report.write |
| POST   | /v1/expense/travel-requisitions/{id}/submit | expense.report.write |
| POST   | /v1/expense/travel-requisitions/{id}/decide | expense.approve |
| GET    | /v1/expense/card-transactions          | expense.report.write |
| POST   | /v1/expense/card-transactions/import   | expense.card.import  |
| GET    | /healthz, /readyz                      | public               |

## Error codes

Problem details use `type: https://passage.dev/problems/<code>`.

| Code      | HTTP | Meaning                                                        |
|-----------|------|----------------------------------------------------------------|
| COM-0001  | 400  | Request failed validation (bad decimal, date, category, count, repeated line or advance id) |
| AUTH-0001 | 401  | Unauthenticated                                                |
| AUTH-0003 | 403  | Missing duty                                                   |
| AUTH-0004 | 403  | Legal entity outside the caller's scope                        |
| EXP-REP-001 | 404 | Expense report not found (or not visible to the caller)      |
| EXP-REP-002 | 409 | Report is not DRAFT (add line, delete line, submit)           |
| EXP-REP-003 | 422 | Submit of a report without lines                              |
| EXP-REP-004 | 412 | If-Match names a stale report version                         |
| EXP-LINE-001 | 409 | A line with this id already exists                           |
| EXP-LINE-002 | 404 | Line not found on the report                                 |
| EXP-LINE-003 | 422 | Report already holds 500 lines                               |
| EXP-IDEM-001 | 422 | Idempotency-Key already used with a different request       |
| EXP-ADV-001 | 422 | Advance exceeds the per-request limit                         |
| EXP-ADV-002 | 422 | The worker has advances past settlement due; new advances blocked |
| EXP-ADV-004 | 422 | Travel requisition of another worker or legal entity, or REJECTED / CLOSED |
| EXP-ADV-005 | 409 | Advance is not in a status that allows the action             |
| EXP-ADV-006 | 404 | Cash advance not found                                        |
| EXP-ADV-007 | 422 | Return is more than the open balance                          |
| EXP-ADV-008 | 412 | If-Match names a stale advance version                        |
| EXP-TR-003 | 404 | Travel requisition not found                                   |
| EXP-TR-004 | 409 | Requisition is not in a status that allows the action          |
| EXP-TR-005 | 412 | If-Match names a stale requisition version                     |
| EXP-SOD-001 | 403 | The requester of an advance or requisition may not decide it  |
| EXP-CARD-002 | 409 | Feed file already imported                                   |
| EXP-POL-001, EXP-REC-001, EXP-TR-002 | 422 | Submit refused: the code of the first unsatisfied policy result |
| COM-0503  | 503  | Report, advance, requisition or card persistence not configured |
| COM-0500  | 500  | Unexpected failure                                             |

Result codes reported in response bodies (not HTTP errors): each policy result carries
`EXP-POL-001` (BLOCK, and REQUIRE_JUSTIFICATION, which stops submission until
justified), `EXP-POL-002` (WARN), `EXP-REC-001` (REQUIRE_RECEIPT) or `EXP-TR-002`
(BLOCK, category needs a travel requisition the report lacks); the variance
check returns `EXP-TR-001` when justification is required; the settlement preview
returns `EXP-ADV-003` when any advance was applied; the aging check returns
`EXP-ADV-002` when new advances are blocked.

Duties: `expense.report.write` is the EssExpense duty for the reports and the previews
(card matching is part of the ESS expense report screen, 8.43.6);
`expense.card.import` is the ExpenseAdmin duty for card feeds (PRD 8.43.16 puts card
import with ExpenseAdmin); `expense.approve` is MssApprove (advance and requisition
decisions); `expense.advance.admin` is the ExpenseAdmin duty for recording the AP
payment and cash returns of advances (8.43.17 "advance returns"). Requests, requisitions
and the card pool use `expense.report.write`.

## Dependencies

Platform kernel only, plus shopspring/decimal and pgx/v5 (both already in the platform module). No new third-party dependencies.
CEL is not used; the rule schema is typed (see NOT-IMPLEMENTED.md).

## Wave reference

PSG-43 / wave 43, PRD section 8.43. Depends on wave 18 (AP), wave 19 (payments),
wave 12 (FX), wave 13 (tax codes), wave 4 (workflow).
