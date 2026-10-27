# revrec

Passage revenue recognition context (wave 29, PSG-29). It allocates a contract's
transaction price across its performance obligations by standalone selling price
(SSP, ASC 606 / IFRS 15, BR-29.1 to BR-29.3), plans and recognizes the revenue
schedules, and reports the deferred revenue roll-forward (FR-29.7 to FR-29.9).

- **Slice 1**: the pure allocation and ratable schedule, served as a stateless preview.
- **Slice 2**: persisted revenue contracts with obligations and schedule lines,
  milestone satisfaction, idempotent period recognition runs, the roll-forward
  (AC-29.4 reproduced against Postgres), events through the outbox, and the
  billing web module's revenue screens.

What is still missing is listed in `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/revrec/             entrypoint: dbx.Connect → (MIGRATE_ON_START) dbx.Migrate → router on HTTP_ADDR, /metrics on ADMIN_ADDR
internal/domain/        pure rules: Allocate, RatableSchedule, ScheduleLines, CheckSatisfy, CheckSSPSources, FullyRecognized, NewRollForward
internal/store/         Postgres repository (pgx, RLS through dbx.WithTenantTx, outbox in the same transaction)
internal/api/           HTTP handlers, DTOs, validation, duties
internal/events/        event types and payloads
migrations/             0001_revrec_init.sql: revenue_contract, performance_obligation, revenue_schedule, recognition_run, outbox
api/openapi.yaml        OpenAPI 3.1 for every endpoint
events/asyncapi.yaml    AsyncAPI 3 for contract.allocated and schedule.recognized
charts/revrec/          Helm chart (DATABASE_URL from the revrec-db secret, admin port, ServiceMonitor toggle)
```

## Endpoints

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/revrec/allocations:preview` | `revrec.view` (RevRecView) | pure computation; stores nothing |
| POST | `/v1/revrec/contracts` | `revrec.maintain` (RevRecMaintain) | `Idempotency-Key` required; 201, or 200 replay; emits `contract.allocated` |
| GET | `/v1/revrec/contracts` | `revrec.view` | newest first; filters `legalEntityId`, `customerId`, `status`, `limit` ≤ 200; recognized and remaining per contract |
| GET | `/v1/revrec/contracts/{id}` | `revrec.view` | obligations with their schedule lines; `ETag` = version |
| POST | `/v1/revrec/milestones/{obligationId}:satisfy` | `revrec.maintain` | recognizes a MILESTONE; optional `If-Match`; same-date retry replays; emits `schedule.recognized` |
| POST | `/v1/revrec/recognition-runs` | `revrec.post` (RevRecPost) | key = (legal entity, period end); 201, or 200 replay; emits `schedule.recognized` per line |
| GET | `/v1/revrec/recognition-runs` | `revrec.view` | latest period first |
| GET | `/v1/revrec/roll-forward?legalEntityId=&currency=&period=YYYY-MM` | `revrec.view` | opening, additions, recognized, adjustments, closing |

Duties are enforced with `platform/authz` (401 without a principal, 403 `AUTH-0003`
without the duty). A principal scoped to one legal entity (`X-Passage-Legal-Entity`)
gets 403 `AUTH-0003` when it creates a contract, runs recognition or reads a
roll-forward for another entity, and row-level security hides other entities'
contracts and runs from its reads.

## Rules

- **Allocation** (BR-29.1, BR-29.3): shares proportional to SSP, truncated to the
  minor unit; the residual goes to the obligation chosen by `residualRule`
  (LARGEST by default). The BR-29.1 bundle gives 7,200.00 / 1,350.00 / 2,250.00;
  at 10,799.99 the hardware takes 7,200.01.
- **SSP source** (BR-29.2): the caller sends the resolved SSP and its source. At
  most one obligation per contract may be RESIDUAL (REV-0003); a RESIDUAL
  obligation is returned with the BIL-0009 warning.
- **Schedules** (FR-29.8): POINT_IN_TIME is one line recognized at `satisfiedOn`,
  the transfer date, when the contract is created; the invoice credits that
  revenue to 4000, so no `schedule.recognized` is emitted for it. MILESTONE is
  one undated line recognized by `:satisfy` (on or after the contract date,
  REV-0006). RATABLE is one line per calendar month of the term, dated at the
  period end, each month weighted by its days; the last line absorbs the
  residual. USAGE has no line until usage posts (later slice).
- **Recognition run**: recognizes every SCHEDULED RATABLE line of the entity
  dated on or before the period end, on the period end. One run per (entity,
  period end); a rerun returns the recorded run. A period end after today is
  refused (REV-0007). A line that falls due in a period already run (a contract
  entered late) is recognized by the next run, on that run's period end.
- **Roll-forward** (FR-29.9): only deferred methods (RATABLE, MILESTONE) enter it.
  Additions are their allocations on contracts dated in the month; recognized are
  their lines recognized in the month; opening is everything before the month.
  Adjustments are 0.00 until credit notes reverse schedules (BR-29.6). AC-29.4:
  March 2027 additions 3,600.00, recognized 1,483.06 (1,350.00 + 133.06),
  closing 2,116.94.
- **Versions**: a contract starts at version 1; every satisfaction and every run
  that recognizes one of its lines bumps it. A contract becomes COMPLETE when no
  line is SCHEDULED and it has no USAGE obligation.

## Events

| Type | When |
|---|---|
| `dev.passage.revrec.contract.allocated.v1` | contract created |
| `dev.passage.revrec.schedule.recognized.v1` | milestone satisfied; each line of a recognition run |

Both are written to the outbox in the same transaction and keyed by contract id.
`journal_posting_id` is null until the ledger posts Dr 2400 / Cr 4000 (wave 11).

## Run

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true HTTP_ADDR=:8080 ADMIN_ADDR=:9090 go run ./cmd/revrec
```

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./internal/store/   # Postgres tests (skipped without the URL)
```

The domain tests cover the BR-29.1 example, the residual cases, the schedule
plan, the satisfaction rules and the roll-forward arithmetic. The API tests use an
in-memory store and cover validation, idempotent replays, If-Match, scope, duties
and readiness. The store tests (Postgres, non-superuser role so RLS applies)
reproduce AC-29.4 end to end, April's carry-forward, contract completion, the
outbox payloads, tenant and legal-entity isolation and the duplicate-key race.

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `COM-0001` | 400 | Validation failed (see `errors` per field), missing Idempotency-Key, malformed If-Match |
| `AUTH-0001` | 401 | Missing tenant context |
| `AUTH-0003` | 403 | Missing duty, or a legal entity outside the caller's scope |
| `REV-0001` | 422 | Rule violation: no obligations, non-positive SSP, negative price, missing transfer date |
| `REV-0002` | 422 | Ratable term missing or ending before it starts |
| `REV-0003` | 422 | More than one RESIDUAL obligation in a contract (BR-29.2) |
| `REV-0004` | 409 | Milestone already satisfied on another date (or reversed) |
| `REV-0005` | 422 | Only a MILESTONE obligation can be satisfied |
| `REV-0006` | 422 | Milestone satisfied before the contract date |
| `REV-0007` | 422 | Recognition run for a period that has not ended |
| `REV-0100` | 404 | Contract or obligation not found |
| `REV-0103` | 412 | If-Match names a contract version that is no longer current |
| `idempotency_key_reuse` | 422 | Idempotency-Key reused with a different body |
| `idempotency_in_progress` | 409 | A concurrent request holds the same key, or a concurrent run the same period |
| `BIL-0009` | warning | SSP missing for an obligation; residual method used (returned in `warnings`) |

## Notes

- Money is `numeric(20,6)` with `currency char(3)` and `shopspring/decimal` in Go; amounts leave the API at the currency's minor unit.
- Dependencies: chi, pgx, shopspring/decimal, rapid (tests), platform kernel only.
