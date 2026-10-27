# scheduling

Finite capacity scheduling for Passage (wave 23, PSG-23; PRD
`docs/prd/phase-4-manufacturing.md` section 4.23). Slice 1 places operations on
resources with weekly calendars, forward from a start time, with the
priority-list heuristic (FR-23.5), splits work across shifts (BR-23.3), stops
double booking on finite resources with a GiST exclusion constraint, lets a
planner preview and commit Gantt moves that push successors forward (FR-23.8),
firms jobs, and reports weekly capacity load with bottleneck flags (FR-23.9).
What is not built is listed in `NOT-IMPLEMENTED.md`.

## Model

- **Resource** (`scheduling_resource`): code, name, `finite`, IANA time zone and
  1-14 weekly shifts (`weekday` 0 = Sunday, `startMinute`/`endMinute` in site-local
  minutes). Open capacity is materialized on read in UTC from the site calendar,
  so DST days have 23 or 25 hours (BR-23.7). Slice 1 keeps its own resources;
  wave 9 resources and calendars replace them (see NOT-IMPLEMENTED).
- **Operation** (`scheduling_operation`): order id, operation number, minutes,
  optional due and ready times, priority, and candidate resources in priority
  order (named resource first, FR-23.2). Status `open`, `scheduled` or `unplaced`.
- **ScheduleRun** (`schedule_run`): scope `site`, `selection` or `move`, mode,
  direction (`forward`), `finite`, algorithm (`heuristic`), metrics and messages.
- **ScheduledJob** (`scheduled_job`): one row per contiguous segment of an
  operation on a resource, with `run_id`, `firmed`, `finite` and `late_minutes`.
  `EXCLUDE USING gist (tenant_id WITH =, resource_id WITH =, tstzrange(start_at, end_at) WITH &&) WHERE (finite)`.

Rules the heuristic applies: orders sorted by (earliest due date, priority,
order id), operations in number order; each operation starts after its
predecessor ends and at or after its ready time and the run start; it goes to
the candidate with the earliest finish, then the lower load ratio, then the
lower resource code (BR-23.8). Firmed jobs, and jobs of operations outside a
selection, stay where they are and hold back their successors. An infinite run
(`finite: false`) keeps jobs inside open calendar time but ignores other
bookings; its rows do not reserve capacity.

## API

All routes need the tenant headers set by the gateway; duties in brackets.

| Method | Path | Duty |
|---|---|---|
| POST | `/v1/scheduling/resources` (Idempotency-Key) | `scheduling.capacity.write` |
| GET | `/v1/scheduling/resources`, `/v1/scheduling/resources/{id}` | `scheduling.read` |
| GET | `/v1/scheduling/resources/{id}/capacity?from&to` | `scheduling.read` |
| POST | `/v1/scheduling/operations` (Idempotency-Key) | `scheduling.schedule.write` |
| GET | `/v1/scheduling/operations?orderId&status&limit` | `scheduling.read` |
| POST | `/v1/scheduling/runs` (Idempotency-Key) | `scheduling.run` |
| GET | `/v1/scheduling/runs`, `/v1/scheduling/runs/{id}` | `scheduling.read` |
| GET | `/v1/scheduling/gantt?from&to&resourceIds` (at most 5,000 bars) | `scheduling.read` |
| POST | `/v1/scheduling/jobs/{id}:move` (Idempotency-Key unless `dryRun`) | `scheduling.schedule.write` |
| POST | `/v1/scheduling/jobs/{id}:firm` (Idempotency-Key) | `scheduling.schedule.write` |
| GET | `/v1/scheduling/capacity-load?from&weeks&resourceIds` | `scheduling.read` |

The browser reaches these as `/api/v1/scheduling/...` through the web BFF.
`api/openapi.yaml` is checked against the router by `internal/api/openapi_test.go`.

## Events (outbox, same transaction as the change)

| Type | When |
|---|---|
| `dev.passage.scheduling.run.completed.v1` | a site or selection run finishes |
| `dev.passage.scheduling.job.scheduled.v1` | a job is placed, moved, firmed or unfirmed |
| `dev.passage.scheduling.orderdates.changed.v1` | an order's scheduled start, end or lateness changed |

The PRD names the last one `passage.scheduling.order.dates-changed.v1`; the
platform naming rule allows no hyphen in the verb, so the entity is
`orderdates` and the verb `changed`. Schemas: `events/asyncapi.yaml`.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| `COM-0001` | 400 | Validation failed (`errors` maps field to message) or Idempotency-Key missing |
| `AUTH-0001` / `AUTH-0003` | 401 / 403 | No tenant / missing duty |
| `idempotency_key_reuse` | 422 | The Idempotency-Key was used with a different body or job |
| `SCHED-0001` | 409 / 422 | Overlaps a booked job on a finite resource (a move conflict, or a concurrent booking caught by the exclusion constraint) |
| `SCHED-0002` | 422 | Move start is outside open capacity, or no capacity is left in the horizon |
| `SCHED-0004` | 422 | Target resource is not a candidate of the operation |
| `SCHED-0008` | 422 | A firmed job cannot be moved, or the move would push a firmed successor |
| `SCHED-0101` | 409 | Resource code already exists |
| `SCHED-0102` | 404 | Resource not found |
| `SCHED-0103` | 409 | The order already has an operation with that number |
| `SCHED-0104` | 404 | Schedule run not found |
| `SCHED-0105` | 404 | Scheduled job not found |
| `SCHED-0106` | 422 | Gantt window holds more than 5,000 bars |
| `SCHED-0107` | 422 | A candidate resource does not exist |

A dry-run move never fails for a constraint: it returns `feasible: false` and
the violations.

## Configuration

| Variable | Default | Range |
|---|---|---|
| `DATABASE_URL` | required | |
| `MIGRATE_ON_START` | `false` | `true` applies `migrations/*.sql` |
| `HTTP_ADDR` / `ADMIN_ADDR` | `:8080` / `:9090` | `/metrics` is on the admin port |
| `SCHED_HORIZON_DAYS` | 365 | 30-730 |
| `SCHED_BOTTLENECK_PCT` | 90 | 50-200 |
| `SCHED_BOTTLENECK_WEEKS` | 3 | 1-12 |

## Run and test

```
go test ./...                     # unit, API and property tests; DB tests skip
PASSAGE_TEST_DATABASE_URL=postgres://owner@localhost/passage \
PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app@localhost/passage \
go test ./...                     # adds the store tests under RLS
```

The store tests run as a NOSUPERUSER, NOBYPASSRLS role and cover the outbox,
idempotent replays, firmed jobs, moves with propagation, the exclusion
constraint, infinite runs, tenant isolation, and that every table forces RLS.
Domain tests include the ft06 heuristic run, a 10,000-job no-double-booking
check (AC-23.2), a 5,000-operation timing check (AC-23.4) and rapid property
tests.

Dependencies: the platform module plus `pgregory.net/rapid` (tests only, already
in the platform go.mod). The optimizer lives in `services/scheduler-py`.
