# services/hr — Passage HR (wave 42)

Human resources core: workers, employments, positions and position assignments
with effective dating, the as-of manager query, and the audited PII vault.
Built for PSG-42, **slice 1**. The wave's remaining deliverables are listed in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

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

Domain rules that are not yet exposed by an endpoint, but are implemented and
tested: accrual with proration, caps and carry-over (BR-42.3), absence consumption
with the negative floor (BR-42.4), hierarchy cycle detection (BR-42.7), and
the reveal gate (`CheckReveal`).

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
  chain, FTE without overfill, the accrual ledger cap, the proration table, and
  the BR-42.3 `US-PTO-15` scenario. They run without a database.
- `internal/api`: no-database tests for duties, validation, the reveal gate and
  reason, the 404/401 paths and metrics.
- `internal/api`: also checks that `api/openapi.yaml` documents exactly the
  mounted routes and that each route enforces its documented duty.
- `internal/store`: integration tests for PII masking and audit, as-of across a
  vacancy, FTE overfill, primary-employment overlap and concurrent employment,
  the inclusive termination date, assignment range and entity checks, the
  outbox payloads, idempotent replay and key reuse, and list paging, search and
  tenant separation. They use `testkit.DB(t)` and **skip unless
  `PASSAGE_TEST_DATABASE_URL` is set**. `TestRowLevelSecurity` also needs
  `PASSAGE_TEST_APP_DATABASE_URL` (a role without BYPASSRLS) and checks every
  table, including the legal-entity scope on employments and positions.

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
| `idempotency_key_reuse` | 422 | Same key used with a different request body |
| `idempotency_key_in_flight` | 409 | A concurrent request with the same key committed first; retry |
| `AUTH-0003` | 403 | Caller lacks the duty (kernel `authz`) |
| `AUTH-0001` | 401 | No or malformed tenant on the request (kernel) |

`HCM-ABS-002` and the other catalog codes in PRD 8.42.18 belong to later slices.

## Layout

```
cmd/hr/            entrypoint: config, dbx.Connect, api.Register, httpx.Serve
internal/domain/   pure rules: effective dating, hierarchy, accrual, PII sealing
internal/store/    pgx repositories, RLS-scoped transactions, outbox writes
internal/api/      HTTP handlers, DTOs, validation, duties
internal/events/   event type constants and payloads
migrations/        0001_hr_core.sql (applied in order by dbx.Migrate)
api/openapi.yaml   OpenAPI 3.1 for every endpoint above
events/asyncapi.yaml  AsyncAPI 3 for the three events published
charts/hr/         Helm chart passage-hr (lint: helm lint charts/hr)
```

## Dependencies

Only the platform module and what it already uses (chi, pgx/v5,
shopspring/decimal, google/uuid, pgregory.net/rapid for tests).
