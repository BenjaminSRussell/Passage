# scheduling

Finite capacity scheduling for Passage (wave 23, PSG-23; PRD
`docs/prd/phase-4-manufacturing.md` section 4.23). Slice 1 places operations on
resources with weekly calendars, forward from a start time, with the
priority-list heuristic (FR-23.5), splits work across shifts (BR-23.3), stops
double booking on finite resources with a GiST exclusion constraint, lets a
planner preview and commit Gantt moves that push successors forward (FR-23.8),
firms jobs, and reports weekly capacity load with bottleneck flags (FR-23.9).
Slice 2 adds resource networks (FR-23.13), setup matrices and cleanout rules
(FR-23.15), campaigns sequenced by the setup matrix with cleanouts (FR-23.14),
the campaign cost split (FR-23.16) and capacity blocks (FR-23.17). Runs do not
read networks, blocks or campaigns yet. What is not built is listed in
`NOT-IMPLEMENTED.md`.

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

- **ResourceNetwork / NetworkEdge** (`scheduling_resource_network`,
  `scheduling_network_edge`): code, name, active dates and allowed transitions
  with transfer minutes, optional maximum wait and optional transfer resource.
- **SetupMatrix / entry** (`scheduling_setup_matrix`, `scheduling_setup_matrix_entry`):
  one version per resource (`version` counts up, the highest version active on
  a day is in force), currency, and per group pair changeover minutes,
  `numeric(20,6)` cost and an optional cleaning operation set.
- **CleanoutRule** (`scheduling_cleanout_rule`): one per setup group, duration,
  operation set, after N batches and/or M run hours, and at campaign end.
- **Campaign / CampaignOrder** (`scheduling_campaign`, `scheduling_campaign_order`):
  resource, key, planned start, limits (orders, quantity, minutes), sequence
  rule, current setup group, status `proposed` → `planned` (sequenced) →
  `started` (frozen), the stored plan (changeovers, cleanouts, SCHED-0012
  warnings) and `version` for If-Match. An order belongs to one campaign.
- **CapacityBlock** (`scheduling_capacity_block`): weekly windows in the
  resource's time zone or one dated window, an attribute condition (`=`, `!=`,
  `>=`, `<=`, `>`, `<`), priority and `active`.

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
| POST | `/v1/scheduling/networks`, `/setup-matrices`, `/cleanout-rules` (Idempotency-Key) | `scheduling.setup.write` |
| GET | `/v1/scheduling/networks[/{id}]`, `/setup-matrices[/{id}]?resourceId`, `/cleanout-rules` | `scheduling.read` |
| POST | `/v1/scheduling/capacity-blocks` (Idempotency-Key) | `scheduling.capacity.write` |
| GET | `/v1/scheduling/capacity-blocks?resourceId` | `scheduling.read` |
| POST | `/v1/scheduling/campaigns` (Idempotency-Key) | `scheduling.campaign.write` |
| GET | `/v1/scheduling/campaigns?status&resourceId&limit`, `/v1/scheduling/campaigns/{id}` (ETag) | `scheduling.read` |
| POST / DELETE | `/v1/scheduling/campaigns/{id}/orders`, `/orders/{orderId}` (Idempotency-Key, If-Match) | `scheduling.campaign.write` |
| POST | `/v1/scheduling/campaigns/{id}:sequence` (Idempotency-Key, If-Match) | `scheduling.schedule.write` |
| POST | `/v1/scheduling/campaigns/{id}:start` (Idempotency-Key, If-Match) | `scheduling.campaign.write` |
| POST | `/v1/scheduling/campaigns/{id}:allocate-cost` (computes only; nothing is stored) | `scheduling.campaign.write` |

The browser reaches these as `/api/v1/scheduling/...` through the web BFF.
`api/openapi.yaml` is checked against the router by `internal/api/openapi_test.go`.

## Events (outbox, same transaction as the change)

| Type | When |
|---|---|
| `dev.passage.scheduling.run.completed.v1` | a site or selection run finishes |
| `dev.passage.scheduling.job.scheduled.v1` | a job is placed, moved, firmed or unfirmed |
| `dev.passage.scheduling.orderdates.changed.v1` | an order's scheduled start, end or lateness changed |
| `dev.passage.scheduling.campaign.sequenced.v1` | a campaign is sequenced (order sequence and cleanouts for production) |
| `dev.passage.scheduling.campaign.started.v1` | a campaign starts and is frozen |

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
| `SCHED-0011` | 422 | The campaign has started (or is completed or cancelled); orders cannot join, leave or be resequenced |
| `SCHED-0012` | warning | In a campaign plan: no setup matrix entry for a group pair; the route setup time was used |
| `SCHED-0101` | 409 | Resource code already exists |
| `SCHED-0102` | 404 | Resource not found |
| `SCHED-0103` | 409 | The order already has an operation with that number |
| `SCHED-0104` | 404 | Schedule run not found |
| `SCHED-0105` | 404 | Scheduled job not found |
| `SCHED-0106` | 422 | Gantt window holds more than 5,000 bars |
| `SCHED-0107` | 422 | A candidate resource does not exist (also any resource a network, matrix, block or campaign names) |
| `SCHED-0108` | 409 | Resource network code already exists |
| `SCHED-0109` | 404 | Resource network not found |
| `SCHED-0110` | 404 | Setup matrix not found |
| `SCHED-0111` | 409 | The setup group already has a cleanout rule |
| `SCHED-0112` | 409 | The resource already has a capacity block with that code |
| `SCHED-0113` | 404 | Campaign not found |
| `SCHED-0114` | 409 | An order already belongs to a campaign |
| `SCHED-0115` | 412 | If-Match is stale: the campaign changed since it was read |
| `SCHED-0116` | 422 | The campaign breaks a rule: over its limits, no orders, or a cancelled campaign has no cost to allocate |
| `SCHED-0117` | 422 | Start needs a sequenced (planned) campaign |
| `SCHED-0118` | 404 | The order is not in this campaign |
| `SCHED-0119` | 409 | A concurrent request created a matrix version for the resource; retry |
| `precondition_required` | 428 | A campaign command was sent without If-Match |

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
constraint, infinite runs, tenant isolation, networks, matrix versions,
cleanout rules, blocks, campaign sequencing (AC-23.11), join, leave, start,
stale If-Match and replays, and that every table forces RLS.
Domain tests include the ft06 heuristic run, a 10,000-job no-double-booking
check (AC-23.2), a 5,000-operation timing check (AC-23.4) and rapid property
tests.

Dependencies: the platform module, `github.com/shopspring/decimal` for exact
costs and quantities (the platform money package uses it), and
`pgregory.net/rapid` (tests only); both are already in the platform go.mod. The optimizer lives in `services/scheduler-py`.
