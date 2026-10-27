# expense

Expense management, wave 43 (PSG-43), slices 1-2.

## What it does

Slices 1-2 are the pure expense rules and calculations behind stateless HTTP previews:

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
- Cash-advance settlement at report approval (BR-43.9, `internal/domain/settlement.go`):
  the worker's PAID advances are applied oldest first, each taking min(its open amount,
  what is still unapplied); the AP invoice carries the rest. Each advance reports its
  open balance and status (PAID, PARTIALLY_SETTLED, SETTLED).
- Travel requisition variance (BR-43.13): actual above estimate × (1 + tolerance)
  requires justification (`EXP-TR-001`).
- Card-to-receipt matching (FR-43.13): same worker, merchant equal ignoring case,
  date within 2 calendar days, amount within 1% (defaults), never across workers;
  `MatchReceipts` pairs cards and receipts one to one, closest amount first. Domain
  only (no matching endpoint; feed import and match persistence are not built).
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

Nothing is persisted or posted yet. See `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/expense/            entrypoint: dbx.Connect -> dbx.Migrate -> httpx.NewRouter -> Serve
internal/domain/        pure rules, settlement, variance, card matching, per diem,
                        advance aging, requisition window, card feed parsing (no I/O)
internal/api/           HTTP handlers and DTOs
migrations/             NNNN_*.sql applied by dbx.Migrate (infrastructure tables only so far)
api/openapi.yaml        OpenAPI 3.1 for every endpoint
events/asyncapi.yaml    AsyncAPI 3 (no events are published yet)
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

Domain tests are pure. API tests and store tests use `platform/go/testkit` and are
skipped unless `PASSAGE_TEST_DATABASE_URL` is set.

## Endpoints

| Method | Path                                   | Duty                 |
|--------|----------------------------------------|----------------------|
| POST   | /v1/expense/report-evaluations         | expense.report.write |
| POST   | /v1/expense/cash-advance-settlements   | expense.report.write |
| POST   | /v1/expense/travel-variances           | expense.report.write |
| POST   | /v1/expense/per-diem-computations      | expense.report.write |
| POST   | /v1/expense/cash-advance-aging         | expense.report.write |
| POST   | /v1/expense/travel-requisition-checks  | expense.report.write |
| POST   | /v1/expense/card-transactions:parse    | expense.card.import  |
| GET    | /healthz, /readyz                      | public               |

## Error codes

Problem details use `type: https://passage.dev/problems/<code>`.

| Code      | HTTP | Meaning                                                        |
|-----------|------|----------------------------------------------------------------|
| COM-0001  | 400  | Request failed validation (bad decimal, date, category, count, repeated line or advance id) |
| AUTH-0001 | 401  | Unauthenticated                                                |
| AUTH-0003 | 403  | Missing duty                                                   |
| COM-0500  | 500  | Unexpected failure                                             |

Result codes reported in response bodies (not HTTP errors): each policy result carries
`EXP-POL-001` (BLOCK, and REQUIRE_JUSTIFICATION, which stops submission until
justified), `EXP-POL-002` (WARN) or `EXP-REC-001` (REQUIRE_RECEIPT); the variance
check returns `EXP-TR-001` when justification is required; the settlement preview
returns `EXP-ADV-003` when any advance was applied; the aging check returns
`EXP-ADV-002` when new advances are blocked.

Duties: `expense.report.write` is the EssExpense duty for the previews;
`expense.card.import` is the ExpenseAdmin duty for card feeds (PRD 8.43.16 puts card
import with ExpenseAdmin).

## Dependencies

Platform kernel only, plus shopspring/decimal. No new third-party dependencies.
CEL is not used; the rule schema is typed (see NOT-IMPLEMENTED.md).

## Wave reference

PSG-43 / wave 43, PRD section 8.43. Depends on wave 18 (AP), wave 19 (payments),
wave 12 (FX), wave 13 (tax codes), wave 4 (workflow).
