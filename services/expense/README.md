# expense

Expense management, wave 43 (PSG-43), slice 1.

## What it does

Slice 1 is the pure expense rules engine behind three stateless HTTP previews:

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
  only in this slice (no endpoint; feed import is not built).

Nothing is persisted or posted in this slice. See `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/expense/            entrypoint: dbx.Connect -> dbx.Migrate -> httpx.NewRouter -> Serve
internal/domain/        pure rules, settlement, variance, card matching (no I/O)
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
returns `EXP-ADV-003` when any advance was applied.

## Dependencies

Platform kernel only, plus shopspring/decimal. No new third-party dependencies.
CEL is not used; the rule schema is typed (see NOT-IMPLEMENTED.md).

## Wave reference

PSG-43 / wave 43, PRD section 8.43. Depends on wave 18 (AP), wave 19 (payments),
wave 12 (FX), wave 13 (tax codes), wave 4 (workflow).
