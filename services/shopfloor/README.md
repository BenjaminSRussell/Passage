# shopfloor

Shop floor execution service. Wave reference: **PSG-25 / Wave 25**
(Shop Floor Execution, Time Registration & IoT), PRD section 4.25.

## Slice 1 (this build): time and attendance core

- Workers (shop floor projection of an employee): create with name, badge, sign-in user,
  cost rate and currency; list and get. A badge is unique per tenant (SF-0105).
- Batch registrations from the kiosk: clock-in/out, job start/stop, absence and indirect
  time, each identified by a client UUID so an offline replay is idempotent (BR-25.7).
  A new item emits `dev.passage.shopfloor.registration.posted.v1` in the same transaction.
- Pay calculation per worker and date range (`pay:calculate`): rounding and tolerances
  (BR-25.2), break deduction (BR-25.1), daily overtime over 8.00 h at 1.5x, weekly overtime
  over 40.00 h at 1.5x per pay week (Monday start) without double counting (BR-25.8; the
  earlier days of the week are read even when outside the range), Sunday at 2.0x, missing
  clock-out flags (BR-25.3), 24.00 h per day cap (SF-0007). Amounts use the worker's cost
  rate, rounded once to the currency's minor unit through `platform/money`.
- Approval (`pay:approve`): locks calculated days, refuses days with open flags (SF-0006),
  days whose registrations arrived after calculation (SF-0104) and approval of one's own day
  (SF-0106); already approved days are returned unchanged, and each newly approved day emits
  `dev.passage.shopfloor.payday.approved.v1` (consumed by payroll, wave 43).
- Review lists for the approval screen: worker-days with pay lines and flags, and a
  worker's registrations.
- `Idempotency-Key` on every command except the registration batch (idempotent per item):
  the outcome is stored with the change and a repeated key returns it.
- OEE arithmetic (ISO 22400 aligned, BR-25.5) as a pure function, verified against the
  PRD shift example (the formula gives 0.7521; the PRD text says 0.7522, see NOT-IMPLEMENTED).
- Web: `apps/web/src/modules/shopfloor` (workers list, new worker, worker detail with
  registrations and days, time approval).

Everything else in PSG-25 is listed in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## Layout

```
cmd/shopfloor          entrypoint: dbx.Connect -> (dbx.Migrate) -> httpx routes -> serve
internal/domain        pure rules: punch pairing, rounding, pay lines, OEE (no I/O)
internal/store         pgx repository, tenant-scoped transactions, outbox writes
internal/api           HTTP handlers, validation, problem details
internal/events        event payload structs
migrations/            0001_init.sql (worker, time_registration, worker_day, pay_line, idempotency_keys, outbox)
api/openapi.yaml       OpenAPI 3.1 for every endpoint
events/asyncapi.yaml   AsyncAPI 3 for every event
charts/shopfloor       Helm chart (3 replicas, /healthz and /readyz probes, ServiceMonitor toggle)
```

## Run

```
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_shopfloor
export MIGRATE_ON_START=true   # dev only; stage/prod run migrations as an Argo PreSync Job
go run ./cmd/shopfloor
```

Ports: `HTTP_ADDR` (default `:8080`), `ADMIN_ADDR` (default `:9090`, serves `/metrics`).

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app... go test ./...
```

Domain tests are pure table-driven tests plus `pgregory.net/rapid` properties. Store tests
and the HTTP end-to-end test use `platform/go/testkit` (`DBWithApp`: migrations as the admin
role, queries as `passage_app`, so RLS is enforced) and skip when
`PASSAGE_TEST_DATABASE_URL` is unset. `openapi_test.go` and `events_test.go` keep the
OpenAPI and AsyncAPI files in step with the router and the published event types.

## Authorization

| Route | Duty (PRD 4.25.17) |
|---|---|
| POST `/api/v1/shopfloor/workers` | `shopfloor.setup.maintain` (Maintain shop floor setup) |
| GET `/api/v1/shopfloor/workers`, `/workers/{id}` | `shopfloor.view` (View shop floor) |
| POST `/api/v1/shopfloor/registrations` | `shopfloor.time.register` (Register time) |
| GET `/api/v1/shopfloor/registrations` | `shopfloor.view` |
| POST `/api/v1/shopfloor/pay:calculate` | `shopfloor.pay.calculate` (Calculate time registrations) |
| POST `/api/v1/shopfloor/pay:approve` | `shopfloor.pay.approve` (Approve time registrations) |
| GET `/api/v1/shopfloor/worker-days` | `shopfloor.view` |

Duties are checked by the platform `authz` middleware. The OPA decision point replaces the
local check in a later wave, behind the same interface.

## Error codes

Problem details use `application/problem+json` with `type` `https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| `AUTH-0001` | 401 | Missing or malformed tenant, or no authenticated user for approval |
| `COM-0001` | 400 | Request failed validation (or no `Idempotency-Key`); `errors` lists the fields |
| `AUTH-0003` | 403 | Missing duty |
| `SF-0006` | 422 | Day cannot be approved: flags are open |
| `SF-0007` | 422 | Worker-day exceeds 24.00 hours |
| `SF-0101` | 422 (404 on GET) | Worker not found or inactive |
| `SF-0102` | 422 | Approved days cannot be recalculated; a correction is required |
| `SF-0103` | 422 | No calculated days in the approval range |
| `SF-0104` | 422 | Registrations arrived after calculation; recalculate before approving |
| `SF-0105` | 409 | Badge already assigned to another worker |
| `SF-0106` | 403 | You cannot approve your own time registrations (segregation of duties) |
| `SF-0107` | 409 | A concurrent request used the same `Idempotency-Key`; retry |
| `idempotency_key_reuse` | 422 | `Idempotency-Key` already used with a different request |

Catalog entries SF-0001..SF-0020 (PRD 4.25.18) are reserved; only the codes above are raised
in this slice.

## Dependencies

Only the platform module and what it already pulls in (chi, pgx/v5, shopspring/decimal,
google/uuid, rapid for tests). No new dependencies in this slice.
