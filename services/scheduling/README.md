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
the campaign cost split (FR-23.16) and capacity blocks (FR-23.17). Slice 3
makes schedule runs and moves apply networks, setup matrix changeovers and
capacity blocks (see the rules below). Slice 4 adds order pegs and moves that
push pegged orders behind their parents (FR-23.8, BR-23.10), keeps changeovers
in one piece (BR-23.3), adds recurring capacity blocks (FR-23.17), and gives
the optimizer in `services/scheduler-py` network steps and campaign
sequencing. Slice 5 adds the block utilization inquiry: each window of a
resource's capacity blocks over a range with the open minutes it holds and how
many of them matching jobs use (FR-23.17, PRD 4.23.14). Runs do not place
campaigns yet. What is not built is listed in `NOT-IMPLEMENTED.md`.

## Model

- **Resource** (`scheduling_resource`): code, name, `finite`, IANA time zone and
  1-14 weekly shifts (`weekday` 0 = Sunday, `startMinute`/`endMinute` in site-local
  minutes). Open capacity is materialized on read in UTC from the site calendar,
  so DST days have 23 or 25 hours (BR-23.7). Slice 1 keeps its own resources;
  wave 9 resources and calendars replace them (see NOT-IMPLEMENTED).
- **Operation** (`scheduling_operation`): order id, operation number, minutes,
  optional due and ready times, priority, and candidate resources in priority
  order (named resource first, FR-23.2); optional setup group and route setup
  minutes (FR-23.15), attributes for block conditions (FR-23.17) and the
  network governing the step from the order's previous operation (FR-23.13).
  Status `open`, `scheduled` or `unplaced`.
- **ScheduleRun** (`schedule_run`): scope `site`, `selection` or `move`, mode,
  direction (`forward` or `backward`), `finite`, algorithm (`heuristic`),
  metrics, messages (unplaced operations) and warnings (SCHED-0003,
  SCHED-0010, SCHED-0012, SCHED-0014).
- **ScheduledJob** (`scheduled_job`): one row per contiguous segment of an
  operation on a resource, with `run_id`, `firmed`, `finite`, `late_minutes`
  and, on the first segment, `changeover_minutes` with the `changeover_cost`
  (numeric(20,6)), `changeover_currency` and `cleaning_operation_set_id` of the
  setup matrix entry that priced it (null when none did).
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
  `started` (frozen), the stored plan (changeovers, cleanouts, SCHED-0006 and
  SCHED-0012 messages, the cleanout counter carried in and out, and for
  `cp-sat` the solver status) and `version` for If-Match. An order belongs to
  one campaign. The `cp-sat` rule asks scheduler-opt (`SCHED_OPT_URL`) for the
  minimum-changeover order before the sequencing transaction, with a 20 s
  limit, at most 50 orders; the answer is kept only when OPTIMAL or FEASIBLE,
  complete and, priced with the matrix read in the transaction, strictly
  cheaper than nearest-neighbour (BR-23.9). Anything else keeps
  nearest-neighbour and says why in SCHED-0006.
- **CleanoutCounter** (`scheduling_cleanout_counter`): batches and run minutes
  a resource has run since its last cleanout (BR-23.13). Sequencing reads it;
  starting a campaign sets it to the plan's carry-out and refuses with
  SCHED-0123 when another campaign moved it after this one was sequenced.
  Cancelling a started campaign sets it back to the plan's carry-in, but only
  while that campaign is still the last to have moved it.
- **CapacityBlock** (`scheduling_capacity_block`): weekly windows in the
  resource's time zone, one dated window, or a recurrence (an RRULE subset,
  `FREQ=DAILY|WEEKLY` with `INTERVAL`, `BYDAY`, and `COUNT` or `UNTIL`, from a
  first local date, with a site-local window), an attribute condition (`=`,
  `!=`, `>=`, `<=`, `>`, `<`), priority and `active`. Exactly one window shape
  is set (a table check).
- **OrderPeg** (`scheduling_order_peg`): the dependent order consumes the
  parent order's output (FR-23.8). Both orders need operations in scheduling,
  a pair is pegged once, and a peg that would close a loop is refused. Pegs
  are entered through the API until wave 22 pegging publishes them. Schedule
  runs honour pegs: parents are placed first, a dependent's first operation
  starts no earlier than its parents' last end (in this run, or the stored
  jobs of a parent outside the selection), and a dependent behind an unplaced
  parent (in the run, or outside it with an unplaced operation) is unplaced.
  Forward and backward runs both apply it.

Secondary resources (slice 12, BR-23.4): an operation's `secondaryResourceIds`
(at most 5, distinct, none a candidate) are booked for the same segments as the
primary in `scheduled_job_secondary`, which carries its own exclusion
constraint. A slot whose secondary is booked or closed is skipped; moves and the
jobs they push need their secondaries free (SCHED-0005). Secondary bookings
count in the capacity load and in downtime `affectedJobs`, and a bar is flagged
`outsideCapacity` when a secondary is closed for it. Adjacent open intervals on
a secondary (back-to-back shifts, midnight on a 24-hour calendar) count as one.
An operation whose secondaries are never free in the horizon is unplaced with a
SCHED-0005 reason. A primary job and a secondary booking on the same resource
are kept apart by the scheduler, not by a database constraint.

Rules the heuristic applies: orders sorted by (earliest due date, priority,
order id), operations in number order; each operation starts after its
predecessor ends and at or after its ready time and the run start; it goes to
the candidate with the earliest finish, then the lower load ratio, then the
lower resource code (BR-23.8). Firmed jobs, and jobs of operations outside a
selection, stay where they are and hold back their successors. An infinite run
(`finite: false`) keeps jobs inside open calendar time but ignores other
bookings and sequences no changeovers; its rows do not reserve capacity.

Constraints (slice 3):

- **Networks** (FR-23.13, BR-23.11): an operation that names a network runs
  only on a candidate with an edge from its predecessor's resource, no earlier
  than the edge's transfer time and no later than its maximum wait. When no
  candidate meets the wait the predecessor is delayed by the overrun and the
  order tried again, up to 7 days from the predecessor's first start; past
  that, or with no edge at all, the whole order is unplaced with SCHED-0009
  `NETWORK_INFEASIBLE`. A firmed predecessor cannot move, so the successor
  takes the best edge candidate and the run warns SCHED-0010. Networks active
  on the run date are used; a maximum wait shorter than the transfer is
  refused when a network is created.
- **Changeovers** (FR-23.15): on a finite resource with a setup matrix in
  force on the run date, a job with a setup group starts with the changeover
  from the group the resource is in (the last job with a group before it, or
  the group it was left in before the run). A missing matrix entry uses the
  operation's route setup and warns SCHED-0012. A job without a group leaves
  the setup as it was. A job is not put in front of a booked job whose stored
  changeover would be too short after it.
- **Capacity blocks** (FR-23.17, BR-23.15): a job uses unblocked open time and
  the windows of the active blocks its attributes match (higher priority wins
  where blocks overlap). A job that matches no block on its resource warns
  SCHED-0014.
- **Moves** keep all three: changeovers are repriced, a start inside a block
  the job does not match is SCHED-0002, a later job on a touched resource left
  short of changeover is SCHED-0001, and a changed network step needs an edge
  (SCHED-0009) within its wait (SCHED-0010).

Slice 4:

- **Changeovers are never split** (BR-23.3): a changeover that does not fit
  before its open interval closes moves the job to the next open interval; a
  move whose start would split it is SCHED-0002. Process minutes still split
  across closed time.
- **Recurring blocks** are expanded over the run or move horizon in the
  resource's time zone, so a DST day keeps its true length; `COUNT` counts
  from the first date, not from the horizon.
- **Propagation** (FR-23.8, BR-23.10): a move with `propagate` walks the pegs
  from the moved order breadth-first, at most the propagation limits of the
  scheduling parameters (500 orders and 5 levels until changed); an
  order cut off by a limit sets `truncated` and the move run records
  SCHED-0007. Each reached order whose first operation would start before one
  of its parents ends is pushed forward to the latest parent end, with its
  own later operations, on the same resources and under the same constraints;
  parents are re-planned before their dependents. Dependents never move
  earlier. A firmed job that would have to move (SCHED-0008) or any other
  conflict in a dependent refuses the whole move. The commit announces
  `orderdates.changed` for the moved order and each dependent that moved.

Slice 5:

- **Block utilization** (`GET /resources/{id}/block-utilization?from&to`, at
  most 62 days): the active blocks' windows are expanded in the resource's
  time zone and clipped to the range. A window holds only open calendar time
  that no higher-priority block (BR-23.15), nor an earlier window of the same
  block, has claimed, so no minute counts twice; `matchedMinutes` are the
  finite booked minutes (changeover included) of jobs whose attributes match
  the block condition in that time, `unusedMinutes` the rest. Inactive blocks
  and infinite-run bookings are left out.

Slice 8:

- **Campaign proposals** (`GET /campaign-proposals?from&windowDays&resourceId`,
  window 1 to 90 days, default 14 from the current minute, FR-23.14): the
  first segment of every job that starts in the window, of an operation with
  a setup group whose order is in no campaign, planned or firmed alike. An
  order is offered once, with its earliest such job. Orders are grouped per
  resource by campaign key: the operation attribute `campaignKey` (at most 64
  characters, checked on `POST /operations`) when set, otherwise the setup
  group. Two or more orders make a proposal, in the order their jobs start,
  with the earliest start as planned start; more than 1000 are split into
  consecutive proposals. Nothing is stored: the planner enters each order's
  quantity and creates the campaign with `POST /campaigns`.

Slice 10:

- **Backward runs** (`direction: backward` on `POST /runs`, FR-23.2): each
  order with a due date (the earliest due date of its operations) starts its
  first remaining operation as late as it can while its last operation still
  ends by that date. The latest start is found by a binary search over the
  first operation's earliest start, placing with the forward engine and
  releasing each probe, so calendars, bookings, blocks, changeovers and
  networks apply as they do forward. BR-23.2: when the latest start is less
  than one hour after the run start, or the order cannot end by its due date
  even started at once, the order is placed forward from the run start; when
  it then ends after the due date its jobs carry `late_minutes` and the run
  reports SCHED-0003 on the order's last operation. Orders without a due date
  are placed forward.

## Production order intake (slice 18, FR-23.10)

> **Real orders are not imported yet.** Production's jobs name engineering
> (wave 09) resource ids, and scheduling resources have ids of their own, so
> until resources are mapped (the wave 9 handoff in `NOT-IMPLEMENTED.md`)
> every operation of a real order is left out as `unknown-resource` and the
> order is listed as `rejected`. The intake also does not start until its
> service principal is seeded (see below). Both are handoffs, not defaults.

`scheduling consume` runs a second consumer, `scheduling-order-import`, next
to the EAM downtime one: its own Kafka group (`KAFKA_ORDER_GROUP`, default
`scheduling-order-import`) on `passage.production.order`, its own dead-letter
topic `passage.production.order.dlq`, and its own service principal
`service:scheduling-order-import`, which must hold `scheduling.order.import`
(the handler refuses any other principal, the downtime one included). The
principal table lives in `platform/go/tenant` and `policies/authz/data.json`;
until it lists that principal, the consumer logs a warning and does not start,
and the downtime consumer runs alone. A new group starts at the earliest
retained offset, so orders dated before the principal is seeded are read once
it is.

For each `dev.passage.production.order.scheduled.v1` (`reason` `schedule` or
`redate`; an event with `reason` `finite`, which production writes when it
applies scheduling's own finite-capacity dates back onto the order, is
acknowledged and ignored, recording and publishing nothing, so the two
services do not loop), each route operation becomes the scheduling operation with the same
order id and number: the process hours are its minutes (rounded up, at least
1), the setup hours its route setup minutes, the resources of its process jobs
its candidates in order of appearance (only those that are scheduling
resources); queue and transport jobs are ignored. The first operation is ready
no earlier than the order's baseline start. The delivery date is the due
instant of every operation: the end of that day (the next local midnight) in
the time zone of the preferred resource of the order's last registered
operation, where the order finishes (resources carry no site yet, so the site
time zone cannot be used); UTC when no operation is registered.

A re-dated order updates the operations already held: an operation whose
minutes, setup, candidates or dates changed loses its non-firmed jobs and is
open again (`replanRequired`; the next run places it), an operation that left
the route is deleted with its jobs, and an operation a firmed job holds is
left as it is. An operation that is in the event but can no longer be
registered keeps its previous registration and is listed as left out. Imports
of one order are serialized by a transaction advisory lock, and an event whose
`orderVersion` is below the stored one changes nothing, so redeliveries and
reordering cannot roll an order back. Operations that cannot be registered
(`no-process-job`, `bad-hours`, `too-long` over 100,000 minutes,
`setup-too-long` over a week, `unknown-resource`) are recorded on the order's
`scheduling_order_import` row, never failing the event; only an event that
cannot be decoded (no order id, start, end, version or reason, a resource id
that is not a UUID) is dead-lettered. `GET /order-imports` and
`GET /order-imports/{orderId}` answer what happened, and
`orderimport.recorded` is published for every applied event. A job's `resourceId`
is resolved through the scheduling resource's `engineeringResourceId` first and
by scheduling id second; after mapping a resource,
`POST /order-imports/{orderId}:reimport` imports the kept order again.

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
| POST | `/v1/scheduling/orders/{orderId}:schedule` (Idempotency-Key; every operation of one order, scope `order`; 404 `SCHED-0135` when scheduling holds no operation of it) | `scheduling.run` |
| GET | `/v1/scheduling/runs`, `/v1/scheduling/runs/{id}` | `scheduling.read` |
| GET | `/v1/scheduling/gantt?from&to&resourceIds` (at most 5,000 bars; `outsideCapacity` and `blockBreach` per bar) | `scheduling.read` |
| POST | `/v1/scheduling/jobs/{id}:move` (Idempotency-Key unless `dryRun`; `overrideConstraints` also needs `scheduling.constraints.override`, checked in the handler, 403 `SCHED-0141` without it) | `scheduling.schedule.write` |
| POST | `/v1/scheduling/jobs/{id}:firm` (Idempotency-Key) | `scheduling.schedule.write` |
| GET | `/v1/scheduling/capacity-load?period&from&weeks&days&resourceIds` | `scheduling.read` |
| GET | `/v1/scheduling/capacity-load/jobs?resourceId&from&to` | `scheduling.read` |
| GET | `/v1/scheduling/runs/{id}/kpis` | `scheduling.read` |
| GET | `/v1/scheduling/late-orders?minLateMinutes&limit` | `scheduling.read` |
| GET | `/v1/scheduling/dispatch-list?resourceId&date&days&startMinute&endMinute` | `scheduling.read` |
| GET | `/v1/scheduling/order-imports?status&limit` | `scheduling.read` |
| GET | `/v1/scheduling/order-imports/{orderId}` | `scheduling.read` |
| POST | `/v1/scheduling/order-imports/{orderId}:reimport` | `scheduling.schedule.write` |
| POST | `/v1/scheduling/order-pegs` (Idempotency-Key) | `scheduling.schedule.write` |
| GET | `/v1/scheduling/order-pegs?orderId` | `scheduling.read` |
| DELETE | `/v1/scheduling/order-pegs/{id}` | `scheduling.schedule.write` |
| PATCH | `/v1/scheduling/resources/{id}` (Idempotency-Key, If-Match) | `scheduling.capacity.write` |
| POST | `/v1/scheduling/resources/{id}/capacity-overrides` (Idempotency-Key) | `scheduling.capacity.write` |
| GET | `/v1/scheduling/resources/{id}/capacity-overrides?from&to` | `scheduling.read` |
| DELETE | `/v1/scheduling/resources/{id}/capacity-overrides/{overrideId}` | `scheduling.capacity.write` |
| POST | `/v1/scheduling/networks`, `/setup-matrices`, `/cleanout-rules` (Idempotency-Key) | `scheduling.setup.write` |
| GET | `/v1/scheduling/networks[/{id}]`, `/setup-matrices[/{id}]?resourceId`, `/cleanout-rules` | `scheduling.read` |
| POST | `/v1/scheduling/capacity-blocks` (Idempotency-Key) | `scheduling.capacity.write` |
| GET | `/v1/scheduling/capacity-blocks?resourceId`, `/capacity-blocks/{id}` | `scheduling.read` |
| PATCH | `/v1/scheduling/capacity-blocks/{id}` (Idempotency-Key, If-Match) | `scheduling.capacity.write` |
| PATCH | `/v1/scheduling/networks/{id}` (Idempotency-Key, If-Match) | `scheduling.setup.write` |
| GET | `/v1/scheduling/resources/{id}/block-utilization?from&to` | `scheduling.read` |
| POST | `/v1/scheduling/campaigns` (Idempotency-Key) | `scheduling.campaign.write` |
| GET | `/v1/scheduling/campaigns?status&resourceId&limit`, `/v1/scheduling/campaigns/{id}` (ETag) | `scheduling.read` |
| POST / DELETE | `/v1/scheduling/campaigns/{id}/orders`, `/orders/{orderId}` (Idempotency-Key, If-Match) | `scheduling.campaign.write` |
| POST | `/v1/scheduling/campaigns/{id}:sequence` (Idempotency-Key, If-Match) | `scheduling.schedule.write` |
| POST | `/v1/scheduling/campaigns/{id}:start` (Idempotency-Key, If-Match) | `scheduling.campaign.write` |
| POST | `/v1/scheduling/campaigns/{id}:complete` (Idempotency-Key, If-Match) | `scheduling.campaign.write` |
| POST | `/v1/scheduling/campaigns/{id}:cancel` (Idempotency-Key, If-Match, optional `reason`) | `scheduling.campaign.write` |
| POST | `/v1/scheduling/campaigns/{id}:allocate-cost` (computes only; nothing is stored) | `scheduling.campaign.write` |
| GET | `/v1/scheduling/campaign-proposals?from&windowDays&resourceId` (computes only; nothing is stored) | `scheduling.read` |
| GET | `/v1/scheduling/parameters` (ETag; built-in defaults at version 1 until the first change) | `scheduling.read` |
| PATCH | `/v1/scheduling/parameters` (Idempotency-Key, If-Match; 412 `SCHED-0140` when stale) | `scheduling.setup.write` |

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
| `dev.passage.scheduling.campaign.completed.v1` | a started campaign is completed |
| `dev.passage.scheduling.campaign.cancelled.v1` | a campaign is cancelled; carries the released order ids and whether the cleanout counter was given back |
| `dev.passage.scheduling.orderimport.recorded.v1` | a production order event was applied: the operations created, updated or unchanged, the ones left out and why, removed and firmed-kept operation numbers, `replanRequired` |
| `dev.passage.scheduling.parameters.updated.v1` | the tenant's scheduling parameters (run defaults, drag snap, propagation limits) changed |

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
| `SCHED-0002` | 422 | Move start is outside open capacity, would split a changeover across closed time (BR-23.3), or no capacity is left in the horizon |
| `SCHED-0004` | 422 | Target resource is not a candidate of the operation |
| `SCHED-0005` | 422 / run message | A move needs a secondary resource that is booked or closed for the job's segments (BR-23.4); in a run the primary slot is skipped instead, and an operation with no free slot in the horizon is unplaced with this code |
| `SCHED-0007` | move run message / `truncated` | Propagation stopped at the parameters' order or pegging-level limit; run the site schedule |
| `SCHED-0008` | 422 | A firmed job cannot be moved, or the move would push a firmed successor or a firmed job of a pegged order |
| `SCHED-0009` | 422 / run message | A move puts a network step on resources without an edge; in a run, the order is unplaced as `NETWORK_INFEASIBLE` |
| `SCHED-0010` | 422 / run warning | A move breaks a network step's transfer or maximum wait; in a run, a frozen predecessor left the successor waiting too long |
| `SCHED-0006` | campaign plan message | cp-sat campaign: the solver status (OPTIMAL, FEASIBLE, UNKNOWN, INFEASIBLE, UNAVAILABLE when scheduler-opt is not configured or failed, TOO_LARGE above 50 orders) and whether the optimized or the heuristic sequence was kept |
| `SCHED-0011` | 422 | The campaign has started (or is completed or cancelled); orders cannot join, leave or be resequenced, and a completed or cancelled campaign cannot be completed or cancelled again |
| `SCHED-0012` | warning | In a campaign plan or a run: no setup matrix entry for a group pair; the route setup time was used |
| `SCHED-0014` | run warning | The job matches no capacity block on its resource and was placed in unblocked capacity |
| `SCHED-0101` | 409 | Resource code already exists |
| `SCHED-0102` | 404 | Resource not found |
| `SCHED-0103` | 409 | The order already has an operation with that number |
| `SCHED-0104` | 404 | Schedule run not found |
| `SCHED-0105` | 404 | Scheduled job not found |
| `SCHED-0106` | 422 | Gantt window holds more than 5,000 bars |
| `SCHED-0107` | 422 | A candidate resource does not exist (also any resource a network, matrix, block or campaign names) |
| `SCHED-0108` | 409 | Resource network code already exists |
| `SCHED-0109` | 404 / 422 | Resource network not found (422 when an operation names it) |
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
| `SCHED-0120` | 422 | An order to peg has no operations in scheduling |
| `SCHED-0121` | 422 | The peg would make the parent order depend on its own dependent |
| `SCHED-0122` | 404 | Order peg not found |
| `SCHED-0130` | 404 | Capacity override not found |
| `SCHED-0131` | 404 | No production order with that id has been received |
| `SCHED-0123` | 409 | Campaign start: the resource's cleanout counter moved after the campaign was sequenced; sequence it again |
| `SCHED-0124` | 412 | If-Match is stale: the resource changed since it was read |
| `SCHED-0125` | 422 | Complete needs a started campaign |
| `SCHED-0140` | 412 | If-Match is stale: the scheduling parameters changed since they were read |
| `SCHED-0141` | 403 | A move asked for `overrideConstraints` without the `scheduling.constraints.override` duty |
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
| `KAFKA_BROKERS`, `KAFKA_USERNAME`, `KAFKA_PASSWORD(_FILE)`, `KAFKA_TLS_CA_FILE` | | `consume` only; shared by both consumers |
| `KAFKA_GROUP` / `KAFKA_ORDER_GROUP` | `scheduling-downtime` / `scheduling-order-import` | `consume` only: the downtime and the order intake groups |
| `SCHED_OPT_URL` | empty | scheduler-opt base URL for cp-sat campaigns; empty keeps nearest-neighbour (SCHED-0006 UNAVAILABLE) |

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
stale If-Match and replays, runs that apply networks (AC-23.10), changeovers
and blocks (AC-23.13), moves that keep changeovers, pegs (loops, replays,
tenants), propagated moves (AC-23.3 and the 5-level limit), recurring blocks,
and that every table forces RLS.
Domain tests include the ft06 heuristic run, a 10,000-job no-double-booking
check (AC-23.2), a 5,000-operation timing check (AC-23.4) and rapid property
tests.

Dependencies: the platform module, `github.com/shopspring/decimal` for exact
costs and quantities (the platform money package uses it), and
`pgregory.net/rapid` (tests only); both are already in the platform go.mod. The optimizer lives in `services/scheduler-py`.
