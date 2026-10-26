# payroll

Payroll engine, wave 43 (PSG-43), slice 1.

## What it does

Slice 1 is the pure gross-to-net engine behind a stateless HTTP preview:

- Versioned US rule set `US-FED-2025.1` (`internal/domain/ruleset.go`): the 2025
  Pub 15-T Worksheet 1A annual percentage method (automated payroll systems) with the
  standard and Step 2 checkbox schedules for Single / married filing separately,
  married filing jointly and head of household; 2020+ W-4 Steps 2, 3, 4(a), 4(b) and
  4(c), and pre-2020 W-4 allowances (4,300 each). Social Security at 6.2% to the wage
  base (176,100.00), Medicare at 1.45% plus the employee-only 0.9% additional over
  200,000.00, FUTA at 0.6% to 7,000.00, Colorado flat 4.40% state income tax.
- The federal withholding line carries the schedule row it used (`bracket`), so the
  amount can be explained without re-running the engine.
- Pre-tax deductions reduce federal and state wages; a Section 125 deduction
  (`reducesFica`) also reduces FICA and FUTA wages, a 401(k) does not.
- BR-43.1 note: the PRD's federal figure (577.43) omits Worksheet 1A line 1g; the
  engine follows Pub 15-T (504.66). See `NOT-IMPLEMENTED.md`.
- Each tax is computed on the unrounded basis and rounded half-up to 0.01 per line
  (BR-43.2). Every line carries `basisAmount` and `ruleVersion`.
- Statement hash: SHA-256 over the lines (immutability check, FR-43.7).
- Supplemental withholding for off-cycle bonuses at the flat 22% rate, with the 37%
  rate above 1,000,000.00 of YTD supplemental wages (BR-43.11).
- Deduction arrears with ARREARS, NO_ARREARS and PARTIAL policies and recovery limits
  (BR-43.10, `internal/domain/arrears.go`).
- Retro delta computation (BR-43.4, `internal/domain/retro.go`): earning deltas summed
  across the affected periods into RETRO_* earnings that the current run taxes.
- Pay-run lifecycle guards (`internal/domain/run.go`): transitions and the
  calculator/finalizer separation of duties (BR-43.8).

Nothing is persisted or posted in this slice. See `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/payroll/            entrypoint: dbx.Connect -> dbx.Migrate -> httpx.NewRouter -> Serve
internal/domain/        pure rules, tax functions, statement calculation (no I/O)
internal/api/           HTTP handlers and DTOs
migrations/             NNNN_*.sql applied by dbx.Migrate (infrastructure tables only so far)
api/openapi.yaml        OpenAPI 3.1 for every endpoint
events/asyncapi.yaml    AsyncAPI 3 (no events are published yet)
charts/payroll/         Helm chart
```

## Run

```
PASSAGE_ENV=dev SERVICE_NAME=payroll DATABASE_URL=postgres://... \
  HTTP_ADDR=:8080 go run ./cmd/payroll
```

## Test

```
go vet ./... && go test ./...
```

Domain tests are pure. API and store tests use `platform/go/testkit` and are skipped
unless `PASSAGE_TEST_DATABASE_URL` is set.

## Endpoints

| Method | Path                         | Duty                  |
|--------|------------------------------|-----------------------|
| POST   | /v1/payroll/calculations     | payroll.run.maintain  |
| GET    | /healthz, /readyz            | public                |

## Error codes

Problem details use `type: https://passage.dev/problems/<code>`.

| Code         | HTTP | Meaning                                                              |
|--------------|------|----------------------------------------------------------------------|
| COM-0001     | 400  | Request failed validation (bad decimal, unknown field, bad enum)      |
| AUTH-0001    | 401  | Unauthenticated                                                       |
| AUTH-0003    | 403  | Missing duty                                                          |
| PAY-RULE-001 | 422  | No published rule set, schedule or state rate covers the inputs        |
| PAY-RULE-002 | 422  | Rule-set publisher must differ from its author (domain, not yet wired)|
| PAY-RUN-004  | 422  | The user who calculated a run cannot finalize it (domain)             |
| PAY-RUN-005  | 422  | Illegal pay-run state transition (domain)                             |
| PAY-RUN-006  | 422  | Calculation input the engine refuses (pre-tax above gross)            |
| COM-0500     | 500  | Unexpected failure                                                    |

## Dependencies

Platform kernel only (chi, pgx/v5, shopspring/decimal, google/uuid, pgregory.net/rapid
for property tests). No new third-party dependencies.

## Wave reference

PSG-43 / wave 43, PRD section 8.43. Depends on wave 42 (HR), wave 25 (time), wave 18
(AP), wave 19 (payments), wave 36 (bank files), wave 13 (tax codes), wave 12 (FX).
