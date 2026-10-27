# services/hr — Passage HR (wave 42)

Human resources core: workers, employments, positions and position assignments
with effective dating, the as-of manager query, the audited PII vault, and
absence (plans, accrual, year end and carry-over, balances, requests and
approval), and benefits (plans with CEL eligibility, a workforce simulation,
life events and enrollments). Built for PSG-42, **slices 1 to 3**. The wave's remaining
deliverables are listed in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## What slice 1 does

- **Workers** (FR-42.1): EMPLOYEE or CONTRACTOR, one wave-7 person party,
  personnel number, legal and preferred name. National ID and bank account are
  sealed with AES-256-GCM (`internal/domain/pii.go`) in `worker_pii`. Reads return
  masked values (`***-**-6789`, `****5432`). The unmasked read needs `HcmPIIRead`
  and a reason of at least 10 characters, and every read writes `audit_event`
  in the same transaction.
- **Employments** (FR-42.2, FR-42.23): validity is a `[from, to)` range. The
  termination date is the last day employed, so `validTo` is the day after it;
  a termination on the hire date is a one-day employment. `status` is derived
  from the range as-of the read (PENDING, ACTIVE, TERMINATED). One PRIMARY
  employment per worker per legal entity at any instant (exclusion
  constraint). CONCURRENT and GLOBAL_HOST are allowed. GLOBAL_HOST needs a home
  entity different from the employing entity and a planned end date.
- **Positions and assignments** (FR-42.3): FTE is a decimal string from 0.05 to
  2.00, default 1.00. Assignments cannot exceed FTE unless the position allows
  overfill. An assignment must lie inside the position's and the employment's
  validity, and the employment must be the worker's, in the position's legal
  entity. A position may report to another position that exists on its start
  date.
- **As-of** (FR-42.4, BR-42.1): the worker's employments, primary position and
  manager on a date. The manager is the incumbent of the reports-to position. If
  that position is vacant, the walk goes up to `HR_MANAGER_MAX_DEPTH` levels
  (default 10), then returns none. The range end is exclusive.
- **Reads**: worker list (search, keyset paging), worker employments, position
  list and position detail with assignments.
- **Events** via the outbox, in the same transaction as the write (see
  `events/asyncapi.yaml`).
- **Web**: `apps/web/src/modules/hr` has the screens for all of the above.

## What slice 2 does (absence)

- **Plans** (FR-42.6): unit DAYS or HOURS, frequency, rate, accrual day,
  proration, max balance, carry-over max and expiry months, plan-year start
  (MM-DD; 02-29 is refused), negative balance allowed, minimum service days.
  Omitted fields take the 8.42.13 defaults. All quantities are decimal strings
  (`shopspring/decimal`, `numeric` columns); a JSON number is refused.
- **Accrual** (BR-42.3, `POST /absence/accrue`): one whole accrual period of
  the plan (calendar month, 14 days, or the plan year; `PER_HOUR_WORKED` needs
  wave-25 time and is refused) whose accrual day has come. Every worker with a
  PRIMARY employment in the period (BR-42.14) accrues
  `rate × days employed ÷ days in period`, rounded half-up to 2 places, capped
  at max balance (the excess is discarded and logged). The amount is booked to
  the plan year of the accrual day. Each worker gets one accrual line per
  (plan, period) — the `hr-accrue` key in 8.42.16 — so a rerun credits only
  workers not yet accrued: a retry is safe, and an admin whose legal-entity
  scope hides some employments leaves them for a later run. Overlapping
  periods of one plan are refused by an exclusion constraint.
- **Year end and carry-over expiry** (BR-42.3): `POST /absence/plans/{id}/year-end`
  closes a plan year once it has ended: each worker's total carries into the
  next year's CARRY_OVER bucket up to carry-over max and the rest expires (a
  negative total carries as is). Accrual into a new plan year is refused
  until the previous one is closed, and a closed year takes no more
  requests, approvals or accrual (HCM-ABS-YEAR-CLOSED). `POST /absence/plans/{id}/carry-over-expiry`
  expires unused carry-over once `carryOverExpiryMonths` have passed since the
  year started (0 means it lasts the whole year). Both are keyed per (plan,
  plan year, worker).
- **Balances**: per worker, plan, plan year and bucket (CURRENT, CARRY_OVER);
  balance = accrued − used + adjusted − expired. The read takes `asOf` and
  returns the plan year that contains it.
- **Requests** (FR-42.7, BR-42.4, BR-42.5): submitted in one call (no DRAFT).
  The quantity must fit the span (one day, or 24 hours, per calendar day) and
  the plan-year balance plus the negative allowance of −(rate × 2)
  (HCM-ABS-002). A request may not cross a plan-year start. An overlap with
  an APPROVED request is refused (HCM-ABS-001), with a SUBMITTED one it warns
  (HCM-ABS-003). Approval rechecks both, spends CARRY_OVER first and writes
  `dev.passage.hr.absence.approved.v1` in the same transaction. A SUBMITTED
  request can be cancelled. Every balance change locks the worker row first,
  so concurrent approvals, accruals and year ends cannot overspend or lose
  an update.
- **Own data**: `EssAbsence` alone reads and requests only for the worker whose
  sign-in user (`worker.userId`) the caller is; `MssApprove` or
  `HcmAbsenceAdmin` read anyone's balances and requests, and
  `HcmAbsenceAdmin` may request or cancel for anyone. An approver cannot
  decide their own request (8.42.17).

## What slice 3 does (benefits)

- **Plans** (FR-42.8): code, type (MEDICAL, DENTAL, VISION, LIFE,
  RETIREMENT_401K, HSA, FSA, OTHER), provider party, currency, eligibility
  rule, open enrollment window (inclusive dates), waiting days, pre-tax flag,
  payroll deduction code and 1–20 options (coverage level, employee and
  employer rate, PER_PAY_PERIOD or MONTHLY). Rates are decimal strings with at
  most the currency's minor-unit places (`money.MinorUnits`), stored as
  `numeric(20,6)`.
- **Eligibility** (BR-42.6, `internal/domain/cel.go`): a CEL subset over
  `employment_type`, `fte` (sum of the employment's position assignments on
  the date), `service_days` (from the hire date) and `legal_entity`, with
  `&& || ! == != < <= > >= in`, list literals, numbers, strings and booleans.
  The rule is compiled when the plan is saved, so a broken rule is a 400. A
  fact the worker lacks (`fte` without an assignment, `age`, `job.type`) makes
  the rule fail with a reason rather than guess. The waiting period is checked
  first. A worker with PRIMARY employments in several legal entities is
  eligible when one of them is.
- **Simulation** (`GET /benefits/plans/{id}/simulate?asOf=`, AC-42.5): the
  same evaluation for every worker with a PRIMARY employment on the date, one
  row per worker with the reason. Nothing is written. The `hr-benefit-100`
  fixture (`testdata/`) checks 100 employees against 6 plans with expected sets
  computed by an independent reference.
- **Life events** (8.42.5): MARRIAGE, BIRTH, ADOPTION, DIVORCE,
  LOSS_OF_COVERAGE, OTHER; each opens an enrollment window from its date to 30
  days after (`windowEnd`).
- **Enrollments**: one option of one plan from an effective date, inside the
  open window or a life event's window (HCM-BEN-001), for an eligible worker
  (HCM-BEN-002), with a dependent count. One enrollment per worker and plan at
  any instant (exclusion constraint, HCM-BEN-004). Ending takes the last
  covered day and a reason; a TERMINATION keeps coverage to the end of that
  month and marks a COBRA qualifying event on health plans. Both write
  `dev.passage.hr.benefitenrollment.changed.v1` (START or END, with the rates,
  currency, pre-tax flag and deduction code payroll needs) in the same
  transaction. Enrollment and ending lock the rows they change.
- **Own data**: `EssBenefits` alone enrolls, records life events and reads
  benefits only for the caller's own worker; `HcmWorkerMaintain` acts for
  anyone and `HcmWorkerInquire` reads anyone's.

Domain rules that are not yet exposed by an endpoint, but are implemented and
tested: hierarchy cycle detection (BR-42.7) and the reveal gate (`CheckReveal`).

## Run

Configuration is read from the environment and validated at startup:

| Variable | Required | Meaning |
|---|---|---|
| `DATABASE_URL` | yes | Postgres as `passage_app` (no BYPASSRLS) |
| `HR_PII_KEY` | yes | base64 of 32 random bytes; the service refuses to start without it |
| `HR_MANAGER_MAX_DEPTH` | no | vacant-manager walk depth, 1–20, default 10 |
| `MIGRATE_ON_START` | no | `true` applies migrations at start (dev only) |
| `HTTP_ADDR`, `ADMIN_ADDR` | no | default `:8080` and `:9090` (`/metrics`) |

```sh
cd services/hr
go run ./cmd/hr
```

Health: `GET /healthz` (liveness), `GET /readyz` (Postgres ping, 500 ms).

## Test

```sh
cd services/hr
go vet ./... && go test ./...
```

- `internal/domain`: table-driven and `rapid` property tests. They include the
  as-of manager against an independent reference model, cycle detection on a
  chain, FTE without overfill, the accrual ledger cap, the proration table,
  the BR-42.3 `US-PTO-15` scenario including the year end and carry-over
  expiry, accrual period shapes, plan-year starts, overlap and request state
  rules, and a property that the year end splits the total exactly into
  carried and expired. They run without a database.
- `internal/api`: no-database tests for duties, validation, the reveal gate and
  reason, the 404/401 paths and metrics.
- `internal/api`: also checks that `api/openapi.yaml` documents exactly the
  mounted routes and that each route enforces its documented duty.
- `internal/store`: integration tests for PII masking and audit, as-of across a
  vacancy, FTE overfill, primary-employment overlap and concurrent employment,
  the inclusive termination date, assignment range and entity checks, the
  outbox payloads, idempotent replay and key reuse, list paging, search and
  tenant separation, and absence: AC-42.3 end to end through accrual, approval,
  year end and expiry; per-worker accrual keys, overlapping and early periods;
  AC-42.4 and the overlap, span, plan-year and cancel rules with the approval
  event; own-data and self-approval refusals; and two concurrent approvals
  that cannot overspend one balance. They use `testkit.DB(t)` and **skip unless
  `PASSAGE_TEST_DATABASE_URL` is set**. `TestRowLevelSecurity` also needs
  `PASSAGE_TEST_APP_DATABASE_URL` (a role without BYPASSRLS) and checks every
  table, including the legal-entity scope on employments and positions and a
  scoped accrual run followed by an all-entity rerun. Benefits: plan replay
  and duplicates, the window, life-event, eligibility, option and overlap
  rules with the outbox events, the end rules, the simulation agreeing with
  enrollment, and own-data refusals.

To run the store tests:

```sh
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./internal/store/...
```

## Endpoints

Specification: [api/openapi.yaml](api/openapi.yaml). Duties are checked by the
kernel `authz.RequireDuty` (`x-passage-duty` in the spec).

| Method | Path | Duty |
|---|---|---|
| POST | `/v1/hr/workers` | HcmWorkerMaintain |
| GET | `/v1/hr/workers?q=&limit=&cursor=` | HcmWorkerInquire |
| GET | `/v1/hr/workers/{id}` | HcmWorkerInquire |
| GET | `/v1/hr/workers/{id}/employments` | HcmWorkerInquire |
| GET | `/v1/hr/workers/{id}/as-of?date=` | HcmWorkerInquire |
| GET | `/v1/hr/workers/{id}/pii?reason=` | HcmPIIRead |
| POST | `/v1/hr/workers/{id}/employments` | HcmWorkerMaintain |
| POST | `/v1/hr/positions` | HcmOrgMaintain |
| GET | `/v1/hr/positions?limit=&cursor=` | HcmOrgInquire |
| GET | `/v1/hr/positions/{id}` | HcmOrgInquire |
| POST | `/v1/hr/positions/{id}/assignments` | HcmActionMaintain |
| POST | `/v1/hr/absence/plans` | HcmAbsenceAdmin |
| GET | `/v1/hr/absence/plans?limit=&cursor=` | EssAbsence |
| GET | `/v1/hr/absence/plans/{id}` | HcmAbsenceAdmin |
| POST | `/v1/hr/absence/plans/{id}/year-end` | HcmAbsenceAdmin |
| POST | `/v1/hr/absence/plans/{id}/carry-over-expiry` | HcmAbsenceAdmin |
| POST | `/v1/hr/absence/accrue` | HcmAbsenceAdmin |
| POST | `/v1/hr/absence/requests` | EssAbsence (own worker unless HcmAbsenceAdmin) |
| GET | `/v1/hr/absence/requests?status=&workerId=&limit=&cursor=` | EssAbsence (own unless MssApprove or HcmAbsenceAdmin) |
| POST | `/v1/hr/absence/requests/{id}/decide` | MssApprove |
| POST | `/v1/hr/absence/requests/{id}/cancel` | EssAbsence (own unless HcmAbsenceAdmin) |
| GET | `/v1/hr/workers/{id}/absence/balances?asOf=` | EssAbsence (own unless MssApprove or HcmAbsenceAdmin) |
| POST | `/v1/hr/benefits/plans` | HcmSetupAdmin |
| GET | `/v1/hr/benefits/plans?limit=&cursor=` | EssBenefits |
| GET | `/v1/hr/benefits/plans/{id}` | EssBenefits |
| GET | `/v1/hr/benefits/plans/{id}/simulate?asOf=` | HcmSetupAdmin |
| POST | `/v1/hr/benefits/life-events` | EssBenefits (own worker unless HcmWorkerMaintain) |
| POST | `/v1/hr/benefits/enrollments` | EssBenefits (own worker unless HcmWorkerMaintain) |
| POST | `/v1/hr/benefits/enrollments/{id}/end` | HcmWorkerMaintain |
| GET | `/v1/hr/workers/{id}/benefits` | EssBenefits (own unless HcmWorkerInquire or HcmWorkerMaintain) |

## Error codes

Responses are RFC 9457 problems (`application/problem+json`) with `code`. The
type is `https://passage.dev/problems/<code>`.

| Code | Where | Meaning |
|---|---|---|
| `COM-0001` | 400 | Request failed validation; `errors` names each field |
| `HCM-PII-001` | 422 | Reveal reason missing or shorter than 10 characters |
| `HCM-EMP-001` | 409 | Worker already has an ACTIVE PRIMARY employment in that entity on that date |
| `HCM-EMP-002` | 422 | Termination date is before the hire date |
| `HCM-POS-001` | 422 | Position FTE would be exceeded (overfill not allowed) |
| `HCM-WRK-NOT-FOUND` | 404 | Worker does not exist for this tenant |
| `HCM-WRK-DUPLICATE` | 409 | Personnel number already used in this tenant |
| `HCM-POS-NOT-FOUND` | 404 | Position does not exist for this tenant |
| `HCM-POS-DUPLICATE` | 409 | Position number already used in this tenant |
| `HCM-POS-PARENT-INVALID` | 422 | Reports-to position does not exist on the new position's start date |
| `HCM-ASG-001` | 409 | Assignment overlaps a primary assignment of the same worker |
| `HCM-ASG-002` | 422 | Employment does not belong to the worker, or is in another legal entity than the position |
| `HCM-ASG-003` | 422 | Assignment dates are outside the position's or the employment's validity |
| `HCM-ABS-001` | 422 | Request overlaps an approved absence (at submission or approval) |
| `HCM-ABS-002` | 422 | Requested quantity exceeds the balance plus the negative allowance |
| `HCM-ABS-003` | warning | Request overlaps a submitted request; returned in `warnings` with a 201 |
| `HCM-ABS-PLAN-NOT-FOUND` | 404 | Absence plan does not exist for this tenant |
| `HCM-ABS-PLAN-DUPLICATE` | 409 | Absence plan code already used in this tenant |
| `HCM-ABS-REQ-NOT-FOUND` | 404 | Absence request does not exist for this tenant |
| `HCM-ABS-PERIOD` | 422 | Not one accrual period of the plan, not a plan-year start, or a `PER_HOUR_WORKED` plan |
| `HCM-ABS-NOT-DUE` | 422 | The period's accrual day, the plan year's end or the carry-over expiry has not come yet |
| `HCM-ABS-RUN-OVERLAP` | 409 | Another accrual period of the plan already covers some of these days |
| `HCM-ABS-YEAR-OPEN` | 409 | The previous plan year has balances that are not closed yet |
| `HCM-ABS-YEAR-CLOSED` | 409 | The plan year has had its year end; requests, approvals and accrual into it are refused |
| `HCM-ABS-NO-EXPIRY` | 422 | The plan's carry-over lasts the whole year (0 expiry months) |
| `HCM-ABS-YEAR-SPAN` | 422 | The request crosses a plan-year start; split it |
| `HCM-ABS-STATE` | 422 | The request's status does not allow this decision or cancellation |
| `HCM-ABS-SELF-APPROVAL` | 403 | An approver tried to decide their own request |
| `HCM-ABS-NOT-OWN` | 403 | An employee-only caller acted on another worker's absence |
| `HCM-BEN-001` | 422 | Enrollment outside the open window or the life event's 30 days |
| `HCM-BEN-002` | 422 | Worker not eligible for the plan on the effective date; the detail gives the reason |
| `HCM-BEN-003` | 422 | The option is not offered by the plan |
| `HCM-BEN-004` | 409 | The worker already has an enrollment in the plan that overlaps the effective date |
| `HCM-BEN-005` | 422 | The last covered day is before the enrollment start |
| `HCM-BEN-ENDED` | 409 | The enrollment has already ended with another date or reason |
| `HCM-BEN-PLAN-NOT-FOUND` | 404 | Benefit plan does not exist for this tenant |
| `HCM-BEN-PLAN-DUPLICATE` | 409 | Benefit plan code already used in this tenant |
| `HCM-BEN-ENR-NOT-FOUND` | 404 | Benefit enrollment does not exist for this tenant |
| `HCM-BEN-LE-NOT-FOUND` | 422 | The life event does not exist or is another worker's |
| `HCM-BEN-NOT-OWN` | 403 | An employee-only caller acted on another worker's benefits |
| `idempotency_key_reuse` | 422 | Same key used with a different request body |
| `idempotency_key_in_flight` | 409 | A concurrent request with the same key committed first; retry |
| `AUTH-0003` | 403 | Caller lacks the duty (kernel `authz`) |
| `AUTH-0001` | 401 | No or malformed tenant on the request (kernel) |

The other catalog codes in PRD 8.42.18 belong to later slices.

## Layout

```
cmd/hr/            entrypoint: config, dbx.Connect, api.Register, httpx.Serve
internal/domain/   pure rules: effective dating, hierarchy, accrual, PII sealing, benefit eligibility (CEL subset)
internal/store/    pgx repositories, RLS-scoped transactions, outbox writes
internal/api/      HTTP handlers, DTOs, validation, duties
internal/events/   event type constants and payloads
migrations/        0001_hr_core.sql, 0002_absence.sql, 0003_benefits.sql (applied in order by dbx.Migrate)
testdata/          hr-benefit-100.json (AC-42.5 fixture)
api/openapi.yaml   OpenAPI 3.1 for every endpoint above
events/asyncapi.yaml  AsyncAPI 3 for the five events published
charts/hr/         Helm chart passage-hr (lint: helm lint charts/hr)
```

## Dependencies

Only the platform module and what it already uses (chi, pgx/v5,
shopspring/decimal, google/uuid, pgregory.net/rapid for tests).
