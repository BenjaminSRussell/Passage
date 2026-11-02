# NOT-IMPLEMENTED — scheduling and scheduler-py (PSG-23, wave 23)

Slice 1 builds: resources with weekly site-local calendars and open capacity
materialized in UTC on read (DST-correct); schedulable operations with candidate
resources in priority order; the forward priority-list heuristic with shift
splitting, finite and infinite runs, firmed jobs and the BR-23.8 tie-break;
site and selection runs with idempotency; the GiST exclusion constraint against
double booking; the Gantt window read; `:move` with dry-run preview, successor
propagation and SCHED-0001/0002/0004/0008 conflicts; `:firm`; weekly capacity
load with bottleneck flags; the open-interval inquiry; outbox events for runs,
jobs and order dates; OpenAPI, AsyncAPI, chart, Dockerfile; the CP-SAT job-shop
optimizer in `services/scheduler-py` (ft06 reaches the optimum 55); and web
screens (Gantt workspace with drag, preview and firm; capacity load; runs list,
start run and run detail; resources list, create and detail; operations list
and create).

Slice 10 builds: backward schedule runs with the BR-23.2 forward fallback and
SCHED-0003 (domain, store, `direction` on `POST /runs`, migration 0008, direction
selector and warning label on the web run screens); the OpenAPI and AsyncAPI
specs have no FAIL lines on the doc gates.

Slice 11 builds: schedule runs honour order pegs (FR-23.8). Parents are placed
before their dependents, a dependent's first operation starts no earlier than
its parents' last end (forward and backward runs, parents inside or outside the
run selection), and a dependent behind an unplaced parent (placed in this run,
or outside it with an operation stored as unplaced) is unplaced. No migration,
no new endpoint; the createRun description and README say so.

Slice 12 builds secondary resources booked with the primary (FR-23.4,
BR-23.4, SCHED-0005). `POST /operations` takes `secondaryResourceIds` (at most
5, distinct, existing, none a candidate; migration `0009_secondary_resources.sql`
adds `scheduling_operation.secondary_resource_ids` and the
`scheduled_job_secondary` table with its own GiST exclusion constraint, RLS and
grants). A run books each secondary for the same segments as the primary: a
slot whose secondary is booked or has no open capacity for a segment is
skipped and the next start tried, in forward and backward runs; an infinite run
ignores secondary bookings but still needs open time. A move and every job it
pushes (including pegged dependents) needs its secondaries free, otherwise
SCHED-0005; the booking moves with the job. Secondary bookings count in the
weekly capacity load and in the `affectedJobs` of a downtime on a secondary,
and a bar is flagged `outsideCapacity` when its secondary is closed for it.
Back-to-back intervals on the secondary (two adjacent shifts, midnight on a
24-hour calendar) count as one open stretch. An operation that never finds its
secondaries free in the horizon is unplaced with a SCHED-0005 reason.
The Gantt bars, `job.scheduled` and the jobs table carry
`secondaryResourceIds`. Web: secondary resource pickers on the new-operation
form (a candidate cannot be picked), a Secondary column on the operations list
and the jobs table, and the bar title. No new duty. The OpenAPI and AsyncAPI
specs have no FAIL lines on the doc gates.

Slice 13 builds resource calendar maintenance (FR-23.1, AC-23.7).
`PATCH /resources/{id}` (duty `scheduling.capacity.write`, Idempotency-Key and
If-Match with the resource version, stale version 412 SCHED-0124) changes the
name, time zone and the whole weekly calendar; the code and the finite flag
stay. The answer carries `affectedJobs`, the booked jobs whose booking after
the request time now lies outside the resource's open capacity (calendar and
overrides applied; hours a running job already used do not count), as primary
or as secondary resource, each with the span of all its segments; they stay
where they are until moved or re-run, and every Gantt bar outside open
capacity is flagged as before. A retry that races the first request waits on
the resource lock and replays its answer. `resource.updated`
goes through the outbox with the new calendar, version and affected job ids.
`GET` and `POST` on resources now send an ETag. No migration (the table and
its UPDATE grant exist). Web: an Edit calendar screen
(`/scheduling/resources/:id/edit`: name, time zone, add and remove shifts,
affected jobs after saving, stale-version message with a reload of the
latest version), linked from the resource detail. The OpenAPI and AsyncAPI specs have no FAIL lines on the doc gates.
No new duty.

Slice 14 builds campaign completion and cancellation (FR-23.14).
`POST /campaigns/{id}:complete` (a started campaign only, SCHED-0125 otherwise)
and `POST /campaigns/{id}:cancel` (optional `reason`, at most 500 characters;
any campaign that is not completed or cancelled), both duty
`scheduling.campaign.write`, Idempotency-Key and If-Match with the campaign
version. Cancelling releases the orders (their rows are removed, so they can
join another campaign; the list is in `campaign.cancelled`), and a started
campaign gives back the resource's cleanout counter (its plan's carry-in) only
while it is still the last campaign to have moved the counter and the counter
still equals its carry-out. A completed campaign keeps its orders. Migration
`0011_campaign_close.sql` adds `completed_at`, `cancelled_at`, `cancel_reason`
with a check that each time is set exactly for its status and a reason only on
a cancelled campaign (no new table; the campaign UPDATE and DELETE grants of
0002 apply). `campaign.completed` and `campaign.cancelled` go through the
outbox. A cancelled campaign no longer lists its orders; the released order ids
are only in `campaign.cancelled`. Web: Complete on a started campaign and a
Cancel campaign confirmation step with an optional reason on the campaign
detail screen, and the completion or cancellation time and reason in its facts.
No new duty. The OpenAPI and AsyncAPI specs have no FAIL lines on the doc gates.

Slice 2 builds: resource networks with allowed transitions, transfer time,
maximum wait and transfer resource (FR-23.13 data and the BR-23.11 edge
rules in `domain/network.go`); versioned setup matrices per resource with
changeover minutes, cost (exact decimals) and recipe-dependent cleaning sets,
and cleanout rules per setup group (FR-23.15); campaigns with order, quantity
and duration limits, join and leave until started, `:sequence` by
nearest-neighbour (BR-23.12) or manual order with the leading setup, SCHED-0012
fallback to route setup and BR-23.13 cleanouts, `:start` freezing the campaign
(SCHED-0011), If-Match versioning, idempotent commands and
`campaign.sequenced` / `campaign.started` events through the outbox
(FR-23.14); the BR-23.14 cost split as a computation (`:allocate-cost`,
FR-23.16); capacity blocks with weekly or dated windows, attribute
predicates and priority, and the BR-23.15 placement rule in
`domain/block.go` (FR-23.17); OpenAPI, AsyncAPI, store tests under RLS and web
screens (campaigns list, create and detail with orders, sequence, start and
cost allocation; setup matrices list, create and detail; cleanout rules list
and create; resource networks list, create and detail; capacity blocks list
and create).

Slice 3 makes schedule runs and moves apply those constraints. Operations
carry a setup group, route setup minutes, attributes and a resource network
(migration `0003_run_constraints.sql`, `POST /operations`). The heuristic
(`domain/constraints.go`) restricts an operation that names a network to
resources with an edge from its predecessor's resource, no earlier than the
transfer time and within the maximum wait, delaying the predecessor up to 7
days and otherwise unplacing the order with SCHED-0009 `NETWORK_INFEASIBLE`
(FR-23.13, BR-23.11, AC-23.10); a frozen predecessor that cannot be delayed
gives a SCHED-0010 warning. On a resource with a setup matrix the changeover
from the group the resource is in is laid in front of the process minutes,
a missing entry falls back to the route setup with SCHED-0012, and a job is
never put in front of a booked job whose stored changeover would then be too
short (FR-23.15, AC-23.11 heuristic half). A job only uses capacity-block
time its attributes match, and a job matching no block on its resource gets
SCHED-0014 (FR-23.17, BR-23.15, AC-23.13). Moves reprice changeovers, refuse
a start inside a block the job does not match, refuse leaving a later job
short of changeover, and check every network step they change. Jobs store
their changeover minutes, the Gantt bar and `job.scheduled` carry them, and
runs keep their warnings. Web: the new-operation form takes the setup group,
route setup, network and attributes; the operations list shows them; run
detail lists the warnings; the Gantt hatches the changeover at the start of
a bar and the jobs table shows its minutes.

Slice 4 builds: order pegs (`scheduling_order_peg`, migration
`0004_pegs_recurring_blocks.sql`, `POST/GET /order-pegs`,
`DELETE /order-pegs/{id}`, loop refusal SCHED-0121) and `:move` with
`propagate`, which pushes the pegged orders behind their parents, parents
first, up to 500 orders or 5 levels, reports `dependents` with their old and
new dates and `truncated` (SCHED-0007 on the move run), refuses the whole move
on a conflict in any dependent, and announces `orderdates.changed` for every
order that moved (FR-23.8, BR-23.10, AC-23.3 for orders); changeovers that are
never split across closed time (BR-23.3, `fillSlot`); recurring capacity
blocks (an RRULE subset stored on the block and expanded over the run or move
horizon in the resource's time zone, `domain/recurrence.go`, FR-23.17); and in
`services/scheduler-py`, candidate machines and resource-network steps
(`AddAllowedAssignments`, transfer and maximum wait per edge) in the CP-SAT
job shop, plus campaign sequencing with `AddCircuit`
(`POST /v1/optimize/campaign-sequence`, FR-23.14). Web: "Move pegged orders
too" on the Gantt workspace with the pegged orders in the preview; order pegs
list (filter, remove) and create; recurring blocks in the block form and list.

Slice 5 builds the block utilization inquiry
(`GET /resources/{id}/block-utilization?from&to`, `domain.BlockUtilization`):
every window of a resource's active capacity blocks over a range of at most
62 days, expanded in its time zone, with the open minutes it holds after
higher-priority blocks (BR-23.15), the finite booked minutes of matching jobs
and the unused rest (FR-23.17, PRD 4.23.14). The resource detail screen shows
the next 7 days of block windows. The swarm draft of this slice rebuilt the
slice 2 configuration tables, endpoints and screens a second time (its own
migration `0003_config.sql`, cost as bigint minor units); main already holds
the reviewed versions of all of them, so only the expanded-window read was
ported. Its `PUT`-style `cleanout-rules:replace` and
`resources/{id}/setup-matrix` (latest version) routes were not taken: rules are
created one per setup group with idempotency keys, and
`GET /setup-matrices?resourceId` lists every version with its active date.

Slice 6 wires CP-SAT campaign sequencing and carries the cleanout counter
from one campaign to the next. A campaign with `sequenceRule: cp-sat` asks
scheduler-opt (`SCHED_OPT_URL`, `internal/optimizer`) for the
minimum-changeover order before the sequencing transaction, with the
changeover matrix priced exactly as the Go sequencers price it
(`domain.ChangeoverMatrix`); the answer is kept only when OPTIMAL or
FEASIBLE, complete, and strictly cheaper than nearest-neighbour when priced
with the matrix read inside the transaction (BR-23.9). Every other outcome
(UNKNOWN, INFEASIBLE, scheduler-opt not configured or failing, more than 50
orders, an entry above one week) keeps nearest-neighbour; the plan records
`solverStatus`, `optimized` and a SCHED-0006 message, and the command never
fails because of the solver. The resource's cleanout counter
(`scheduling_cleanout_counter`, migration
`0005_campaign_solver_cleanout_counter.sql`) is read when a campaign is
sequenced (`plan.carryIn`) and set to the plan's `carryOut` when it starts;
a start whose counter moved after sequencing is refused with SCHED-0123.
Web: the CP-SAT optimizer rule on the campaign form and list, the optimizer
outcome and the counter on the campaign plan, and the SCHED-0123 message on
start.

The swarm draft of this slice also stored a materialized `resource_capacity`
table with `capacity:materialize` and its own `GET /resources/{id}/capacity`,
and a `cleanout_order` table written at campaign create. Neither was taken:
`GET /resources/{id}/capacity` already serves the FR-23.1 open intervals
computed from the calendar, the table had no reader (runs and moves read the
calendar), and resource calendars cannot change yet, so a stored copy only
added a second source that could go stale; the cleanouts already live in the
campaign plan and in `campaign.sequenced`, and main has no campaign create
step that plans (sequencing does). Its counter was written when a campaign
was created, so re-planning or planning two campaigns would have counted the
same batches twice; here it moves only when a campaign starts.

Slice 7 puts the price of each scheduled changeover on the job. The
heuristic, moves and the jobs a move pushes copy the cost (in the setup
matrix currency, `money.Amount`) and the cleaning standard operation set of
the matrix entry that priced the changeover onto the placement; a job that
keeps its setup group, or that falls back to the route setup (SCHED-0012),
carries none. `scheduled_job` stores them on the first segment (migration
`0006_job_changeover_cost.sql`: `changeover_cost` numeric(20,6),
`changeover_currency`, `cleaning_operation_set_id`, with checks that cost and
currency come together and only on segment 1); the Gantt bar and
`job.scheduled` carry them (FR-23.15, input to the changeover analysis report
and to FR-23.16 settlement). A move that keeps a pushed job's dates but
changes its cost or cleaning set rewrites it. Web: the jobs table on the
workspace shows the changeover cost and cleaning set, and each bar's title
names them.

The swarm draft of this slice was built on its own unreviewed slices 1-6 and
was not cherry-picked as is. Only its placement pricing was ported (it
carried cost as int64 minor units with no currency; here it is the exact
matrix decimal with its currency, and it is also persisted, published and
shown). Not taken: its `PlanMove` repricing (main has repriced the moved job
and its successors since slice 3); its nightly `materialize-capacity`
CronJob, `ListResourceIDs` and `internal/capacityjob` (they rebuild the
`resource_capacity` table that slice 6 did not take, see above, so there is
nothing to rebuild); the OpenAPI entries for that table's endpoints and for a
campaign `cleanouts` field main does not serve; and its `CampaignPage.tsx`
(main already has the campaign list, create and detail screens from slice 2,
with the plan's cleanouts and the slice 6 optimizer outcome).

Slice 8 builds campaign proposals (FR-23.14): `GET /campaign-proposals`
(`domain.ProposeCampaigns`, `store.ProposeCampaigns`) groups the orders
already scheduled, planned or firmed, that belong to no campaign and whose
jobs start inside a window (default 14 days, 1-90) by resource and campaign
key, the operation attribute `campaignKey` or else the setup group, into
proposals of two or more orders in the order their jobs start. Each order is
offered once, with its earliest job in the window. `POST /operations` now
keeps a `campaignKey` attribute within the 64-character campaign key limit.
Web: the campaign proposals screen (filters for start date, window and
resource; each proposal's orders with a quantity per order and a sequence
rule; "Create campaign" posts the campaign with its orders), linked from the
campaign list.

The swarm draft of this slice was built on its own unreviewed slices 1-7 and
was not cherry-picked as is. Only its proposal rules were ported, and changed:
they now read scheduled jobs from the database and are served and shown
(the draft had a domain function with no caller), group by start rather than
due date, never offer an order twice, and split groups over the campaign
order limit. Not taken: its `PlanMove` successor repricing and network
checks (main has repriced successors and checked every network step a move
changes since slice 3, with networks loaded onto operations, which the draft
noted it lacked); its `SetupPage.tsx` and `setup.ts` (main has had list,
create and detail screens for networks, setup matrices, cleanout rules and
capacity blocks since slice 2).

Slice 9 builds capacity overrides (FR-23.1, AC-23.7): downtimes and extra
shifts on a resource calendar (`scheduling_capacity_override`, migration
`0007_capacity_overrides.sql`). `POST/GET /resources/{id}/capacity-overrides`
and `DELETE /resources/{id}/capacity-overrides/{overrideId}`, idempotent, with
`capacityoverride.created` and `capacityoverride.deleted` through the outbox. A
span is at most 31 days; a downtime wins over an overlapping extra shift
(`domain.ApplyOverrides`). Runs, moves, the capacity inquiry, block
utilization and the weekly capacity load all read the calendar with overrides
applied (`Resource.Overrides`, `domain.OpenWithOverrides`). Creating a downtime
returns the booked jobs it overlaps (`affectedJobs`, firmed or not, at most
500, each with the span of all its segments) and lists them in the event;
those jobs are not moved automatically, the planner moves them or runs the
schedule again. Until then every Gantt bar that lies outside its resource's
open capacity, calendar and overrides applied, carries `outsideCapacity`
(SCHED-0002) and the workspace outlines it and badges it in the jobs table.
EAM downtime (PSG-44, #44 AC4): `scheduling consume` reads
`dev.passage.eam.downtime.recorded.v1` from `passage.eam.downtime` (the
`outbox-eam` connector in `deploy/kafka/connect.yaml`) and books a `downtime`
override on the resource named by the event, through the same store call as
the route. It runs as `service:scheduling-downtime` (`tenant.ServicePrincipal`;
duty table `service_principals` in `policies/authz/data.json`, holding only
`scheduling.downtime.apply`, which is also seeded for the one overrides POST
route). The tenant comes from the event; the resource is read under that
tenant's RLS, so a resource of another tenant or an unknown one is not found
and the event is dead-lettered to `passage.eam.downtime.dlq` at once (so are
bad data and a window over 31 days). Scheduling has no legal-entity column,
so there is no legal-entity check beyond the tenant. Idempotent twice over:
the `consumer_inbox` row for the event id (migration `0010`) and the override
idempotency key `eam-downtime:{downtimeId}`, so a republished event with a new
id books nothing more. The window is widened to whole minutes (start down, end
up) and split into consecutive spans of at most 31 days, keyed `eam-downtime:{id}` then `:{n}`. Booked jobs the window overlaps are listed in the log and the
`capacityoverride.created` event, not moved, as for the route. EAM's `resourceId` is the asset's `production_resource_id`, which must be a scheduling resource id; EAM has no scheduling lookup, so it is not checked on asset save (documented in `asyncapi.yaml`) and a wrong one is dead-lettered. Alert on `event_consumer_dead_lettered_total{consumer="scheduling-downtime"}` > 0 (a non-empty `passage.eam.downtime.dlq`); replay per `platform/go/eventing/README.md`. Deploy: `consumer.enabled` in the chart, a
`KafkaUser` `scheduling-consumer` (`deploy/kafka/scheduling-consumer.yaml`)
whose secret (key `password`, plus the cluster CA as `ca.crt`) must be copied
into the release namespace. Not built: planned downtime of RELEASED work
orders (EAM D3).
Web: capacity overrides list
(by resource, remove) and create form (UTC times, reason, affected jobs shown
after saving), linked from the resource detail screen and the navigation. The
OpenAPI document and the AsyncAPI messages (title and summary on every message)
pass the doc gates with no FAIL lines.

Slice 15 builds capacity block and network maintenance (FR-23.13, FR-23.17).
`GET /capacity-blocks/{id}` and `PATCH /capacity-blocks/{id}` (priority 0-1000,
`active`, `condition`; duty `scheduling.capacity.write`) and
`PATCH /networks/{id}` (`name`, `activeTo` not before the start date, SCHED-0129;
duty `scheduling.setup.write`), each with Idempotency-Key and If-Match with the
record version (stale: 412 SCHED-0127 for a block, SCHED-0128 for a network;
unknown block: SCHED-0126; an explicit `activeTo: null` is refused with 400).
Runs and moves read active blocks and the networks active on the run date when
they start, so a retired block or ended network stops constraining the next
run; jobs already placed are not moved. A block change answers with
`affectedJobs`: the jobs booked from now on the block's resource that sit in
open capacity the active blocks no longer leave them (BR-23.15; for example a
job inside a block whose condition it no longer meets), as a calendar change
lists the jobs it no longer covers (AC-23.7); `capacityblock.updated` carries
their IDs. Retiring a block or ending a network only removes a constraint, so
it leaves no job in breach. Events
`capacityblock.updated` and `network.updated` go through the outbox in the
same transaction (AsyncAPI documents both). No migration: the tables, their
version column and their UPDATE grant exist since 0002. Web: the capacity block
list opens a detail screen with priority, condition and active (stale version
offers the latest; the saved change lists the jobs it keeps out), and the
network detail gains a rename and end-date form. No
new duty. A network end date cannot be cleared and block windows cannot be
edited (see the tables below).

Slice 16 builds capacity load by day and its drill-down (FR-23.9, PRD 4.23.14).
`GET /capacity-load?period=day&from&days&resourceIds` (days 1-93, default 14)
returns open and booked minutes and load % per local day of each resource's
calendar time zone (BR-23.7), so a shift is never split at 00:00 UTC and a DST
day has 23 or 25 hours; days carry no bottleneck flag (FR-23.9 flags weeks).
Every item now has `period`, `periodStart` and the UTC instants `start` and
`end` of its bucket; week items keep `weekStart`. `GET /capacity-load/jobs?resourceId&from&to`
(RFC 3339, at most 93 days; duty `scheduling.read`) lists the primary and
secondary (BR-23.4) bookings of the resource that overlap the range, with
`minutesInRange`, so a cell's booked minutes can be traced to its jobs (at most
1,000, then `truncated`). No migration, no event, no new duty. Web: the load
page gains a Weeks/Days period selector and a Jobs link per row, and a new
detail page `/scheduling/load/jobs` lists the jobs with loading, empty and
error states; each Jobs link passes the bucket's `start` and `end`. Weeks stay
Monday 00:00 UTC weeks as built in slice 3, so for a resource outside UTC the
seven days of a week do not add up exactly to that week.

Slice 17 builds the plan measures of a run and the Late orders inquiry (PRD
4.23.14). `GET /runs/{id}/kpis` measures the jobs that still carry the run's id:
placed jobs, orders, late jobs and orders, the latest job's lateness, changeover
minutes, booked minutes, the span of those jobs, and per resource (primary, and
secondary resources held with a job) the booked minutes against the open
minutes of its calendar in that span, read in the resource's own time zone with
capacity overrides applied as in slice 16 (utilization %, most used first). A
later run or a move replaces jobs, so the figures describe what the run still
holds and `placedJobs` against the run's `operationsCount` shows how much is
current; a superseded run answers zeros.
`GET /late-orders?minLateMinutes&limit` lists orders with a job at least that
late (default 1 minute, at most 500 orders) and every order with an operation
stored as unplaced, most late first, with the delivery date (the latest due
date of the order's operations), planned start and end, late and unplaced
operation counts, the firmed flag and the pegged demand (orders pegged to
consume this one). A job is late when it ends after its operation's `dueAt`,
an instant, so no time zone enters the comparison; `late_minutes` is the one
the scheduler stores and the Gantt shows. Both are reads under
`scheduling.read`; no event, no new duty. Migration `0012_run_kpi_indexes.sql`
adds the `(tenant_id, run_id)` index of PRD 4.23.19 on jobs and secondary
bookings and partial indexes on late jobs and unplaced operations, so the
inquiry reads the late part of the plan rather than all of it. Web: the run
detail gains a Plan measures card with its own loading, empty and error states,
and a new Late orders page with a threshold selector. Not built: comparing two
runs side by side (replaced jobs are not kept per run, so a comparison needs
stored run snapshots), run-scoped late orders (an unplaced operation is not
linked to a run), the site filter (resources carry no site yet, see the sites
handoff below) and sales lines as pegged demand (pegs link orders to orders,
see the AC-23.3 handoff).

Slice 18 builds the intake of wave 21 production orders (FR-23.10, ticket
deliverable 5). **Two handoffs kept it from importing real orders
at first (the second is done in slice 19, the first on main):** (a) it did not start until its service principal
`service:scheduling-order-import` with duty `scheduling.order.import` is
seeded, and (b) production's jobs name engineering (wave 09) resource ids,
which are not scheduling resource ids, so every operation of a real order is
left out as `unknown-resource` and the order shows as `rejected` until
resources are mapped (done in slice 19, below).
`scheduling consume` runs a second consumer, `scheduling-order-import`, with
its own Kafka group (`KAFKA_ORDER_GROUP`), principal, duty and dead-letter
topic `passage.production.order.dlq`, reading
`dev.passage.production.order.scheduled.v1` from `passage.production.order`;
the handler refuses any principal without `scheduling.order.import` (the
downtime principal included). Events with reason `finite` (wave 21 slice 20:
production applying scheduling's own finite-capacity dates) are acknowledged
and ignored, recording nothing, so the services do not loop. It registers each route operation of the order
as a scheduling operation: the process hours become the operation's minutes
(rounded up to a whole minute, at least 1), the setup hours its route setup
minutes, the resources of its process jobs its candidates in order, the first
operation is ready no earlier than the order's baseline start, and the
delivery date is the due instant of every operation: the next local midnight
after it in the time zone of the preferred resource of the order's last
registered operation (resources carry no site yet, so the site's zone is not
available; UTC when none is registered). Queue and transport jobs are not
modelled and are ignored. A re-dated order (`reason: redate`) updates the
operations it already holds; an operation whose minutes, setup, candidates or
dates changed loses its non-firmed jobs and is open again (`replanRequired`,
the next run places it), an operation that left the route is deleted with its
jobs, and an operation a firmed job holds is never touched. An operation still
in the event that can no longer be registered keeps its previous registration
and is listed as left out. Imports of one order are serialized by a
transaction advisory lock (also before its first row exists), and an event
older than the stored `orderVersion` changes nothing, so a redelivery or an
out-of-order delivery after a rebalance cannot roll the order back; production
keys the topic by order id, so one order's events share a partition.
Operations that cannot be registered (no process job, hours not a number, more
than 100,000 process minutes or a week of setup, or no resource of the
operation is a scheduling resource) are left out and recorded, never failing
the event; only an undecodable event is dead-lettered.
Migration `0013_order_imports.sql` adds `scheduling_order_import` (one row per
order: the latest event, outcome, counts, the skipped operations; RLS, grants
SELECT, INSERT, UPDATE). `GET /order-imports` (status, limit) and
`GET /order-imports/{orderId}` (duty `scheduling.read`) answer it;
`orderimport.recorded` goes through the outbox with the operations registered
and the ones left out. Web: Production orders received (list with a result
filter) and the order detail with the operations not registered, linked from
the navigation. New duty: `scheduling.order.import`, held only by the service
principal (no route or page action uses it). The OpenAPI and AsyncAPI specs
have no FAIL lines on the doc gates.
Slice 19 builds the resource mapping slice 18 needed (FR-23.10), so real orders
are no longer rejected: a scheduling resource can stand for an engineering
(wave 09) resource. Migration `0014_resource_mapping.sql` adds
`scheduling_resource.engineering_resource_id` (unique per tenant when set).
`POST /resources` takes `engineeringResourceId`; `PATCH /resources/{id}` sets it
or removes it with an empty string (under the same If-Match version, duty
`scheduling.capacity.write`); another resource already mapped to it is refused
(409 SCHED-0132); `resource.updated` carries it; every resource read returns it.
The order intake resolves each job's `resourceId` through the mapping first and
by scheduling resource id second (so slice 18's direct ids still work), keeps a
candidate once, and keeps the route order. The migration also keeps each
received order (`scheduling_order_import.source`) so that
`POST /order-imports/{orderId}:reimport` (duty `scheduling.schedule.write`) can
import it again at its own version after a mapping was added: operations
already registered stay unchanged, operations left out for an unknown resource
are registered, a newer event that arrived meanwhile wins, and an
`orderimport.recorded` event is written. The answer says `reimportable`; an order
received before this migration has no kept source (409 SCHED-0133) and waits for
its next event. Web: an Engineering resource id field on the new and edit
resource pages, the id in the resources list and on the resource detail, and an
Import again button on the production order detail when operations were left
out for an unknown resource. No new duty. The OpenAPI and AsyncAPI specs have no
FAIL lines on the doc gates.
Slice 19 limits and defaults, as built:
- The engineering resource id is checked for UUID form only. Whether wave 09
  has such a resource (or has retired it) is not checked across services: a
  mistyped id is accepted and simply never matches a job. A wave 9 feed that
  creates or validates the mappings is listed below.
- The mapping is unique per tenant (`(tenant_id, engineering_resource_id)`);
  scheduling resources carry no legal entity, so two legal entities of one
  tenant cannot map the same engineering resource to two scheduling resources,
  and the same engineering id in another tenant is independent. A concurrent
  second mapping is refused by the unique index with the same 409 SCHED-0132.
- A changed or removed mapping does not touch operations already registered:
  they keep the candidates resolved when they were imported, with their jobs,
  until production's next event for the order or a `:reimport`. Then the
  candidates are resolved again; an operation whose candidates changed is
  updated and loses its non-firmed jobs (`replanRequired`), firmed work stays,
  and an operation whose only resource is no longer known is listed as
  `unknown-resource` while its existing scheduling operation stays as it was.
- `:reimport` runs in one transaction under the order's import lock (the same
  advisory lock as the intake), so it cannot interleave with a delivery: a
  newer version that commits first is the one imported again, one that
  arrives during the re-import waits and applies on top. The import row keeps
  the version and reason of the kept event, its `eventId` becomes
  `reimport:<uuid>` and `receivedAt` the time of the re-import.
- `source` holds the part of `order.scheduled.v1` scheduling reads: order id,
  number, start, end, delivery date and per job its operation, sequence, type,
  engineering resource id and planned hours. Nothing personal or priced. It is
  bounded by the Kafka message size of the event (one row per order, replaced
  by each applied newer event; stale events do not touch it) and lives as long
  as the order's import row, which has no purge yet (none of scheduling's
  order data does; a retention policy for withdrawn and completed orders is
  still open, see Slice 20).
Slice 20 builds the order withdrawal (FR-23.10). The order intake consumer also
handles production's `order.statuschanged.v1` (same topic, same principal and
duty `scheduling.order.import`). When the order moves to Estimated, Created or
Cancelled and the event carries no start and end date (production reset the
order, cancelled it or merged it into another order, and cleared its schedule)
scheduling deletes the order's operations and their non-firmed jobs, which
frees the capacity they booked (load and bottleneck figures are read from the
jobs); an operation a firmed job holds stays and is listed on the import row
(`keptFirmed`, skipped code `firmed`). Null dates alone do not withdraw:
production's journal posts (auto-start, report as finished) send statuschanged
without dates for an order that is still dated, so a release, start, report as
finished or end changes nothing. The import row becomes status
`withdrawn` with `withdrawnAt` and `withdrawnTo` (migration
`0015_order_withdrawal.sql`, two columns and the widened status check; the
UPDATE grant of 0013 covers it), the kept event source is cleared and one
`orderimport.recorded` event (reason `withdraw`, `withdrawnTo`) goes through
the outbox in the same transaction. Any other status change, an order
scheduling never received and an order already withdrawn change nothing and
write no event. statuschanged carries no order version, so the import row keeps
the CloudEvent time of the latest production event it applied
(`order_event_at`): a statuschanged older than it (a newer schedule event
already imported the order again) withdraws nothing, and a schedule event older
than the withdrawal is stale. A later `order.scheduled.v1` imports the order
again and clears the withdrawal;
`:reimport` of a withdrawn order is 409 SCHED-0134. `GET /order-imports` filters
on `withdrawn`. Web: the production orders list filters and badges withdrawn
orders and the detail page explains the withdrawal.
- Withdrawn import rows are not purged (no retention policy yet).
- The kept firmed operations of a withdrawn order keep their firmed jobs and
  the capacity they hold. Scheduling has no operation delete outside the order
  feed, so nothing removes them: unfirming one makes it schedulable again by the
  next run. Removing them (on unfirm, or on withdrawal to Cancelled) is open.
- A withdrawn order stays in its campaign (`scheduling_campaign_order`) and in
  its order pegs (`scheduling_order_peg`); both are maintained by hand and are
  not touched by the withdrawal.
- Production's journal-post statuschanged events carry null dates for a dated
  order, against its own AsyncAPI; handoff to wave 21 in the table below.
Everything else in the ticket and PRD 4.23 is listed here.

No file outside the wave 23 ownership entry was changed. These handoffs
from slices 16 to 20 lie outside it:

| Item | Status | Reason |
|---|---|---|
| `policies/authz/data.json`: resource row `/v1/scheduling/capacity-load/jobs` (GET) under `scheduling.read` | done (seeded in `policies/authz/data.json`) | GET `/v1/scheduling/capacity-load/jobs` is a row of `scheduling.read`. |
| `policies/authz/data.json`: resource rows `/v1/scheduling/late-orders` and `/v1/scheduling/runs/*/kpis` (GET) under `scheduling.read` | done (seeded in `policies/authz/data.json`) | GET `/v1/scheduling/late-orders` and `/v1/scheduling/runs/*/kpis` are rows of `scheduling.read`. |
| `policies/authz/data.json`: resource rows `/v1/scheduling/order-imports` and `/v1/scheduling/order-imports/*` (GET) under `scheduling.read` | done (seeded in `policies/authz/data.json`) | Both GET routes are rows of `scheduling.read`. |
| `platform/go/tenant/serviceprincipal.go` `servicePrincipals` and `policies/authz/data.json` `service_principals` (kept equal by `TestServicePrincipalTableMatchesPolicyData`): `"scheduling-order-import": ["scheduling.order.import"]`; the duty `scheduling.order.import` (no resource rows: no route uses it) | done (seeded on main, 6de0ddb2) | The order intake consumer starts on its own group at the earliest retained offset. |
| `deploy/kafka/scheduling-consumer.yaml`: ACLs for `KafkaUser` `scheduling-consumer`: Read, Describe on topic `passage.production.order` and group `scheduling-order-import`; Write, Describe on `passage.production.order.dlq`; a `KafkaTopic` `passage.production.order.dlq` like the downtime one. `deploy/kafka/connect.yaml`: an outbox connector for `passage_production` publishing `passage.production.<aggregate_type>` keyed by aggregate id | handoff — deploy owner (#313) | No production outbox connector exists yet, so nothing reaches `passage.production.order`. Alert on `event_consumer_dead_lettered_total{consumer="scheduling-order-import"}` > 0. |
| Mapping production (engineering, wave 09) resource ids to scheduling resources | done (slice 19) | `scheduling_resource.engineering_resource_id`, set on `POST /resources` and `PATCH /resources/{id}`; the intake resolves a job's resource through it first and by scheduling id second. A wave 9 feed that creates the mappings automatically is still a handoff (below). |
| `policies/authz/data.json`: resource row `/v1/scheduling/order-imports/*:reimport` (POST) under `scheduling.schedule.write` (an existing duty) | done (seeded in `policies/authz/data.json`) | `POST /v1/scheduling/order-imports/{orderId}:reimport` uses the existing duty `scheduling.schedule.write`; no new duty. |
| `services/production/internal/store/journal.go` `logStatus`: set `startDate`/`endDate` on the statuschanged of auto-start and report as finished (slice 20) | handoff — wave 21 | Those events carry null dates for a dated order. Scheduling withdraws only on a move to Estimated, Created or Cancelled, so it is not affected; consumers that read the dates are. No new route, duty or topic: the event is on `passage.production.order`, which the order intake already reads. |

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Forward operation scheduling, finite and infinite | built | `domain.Schedule`; `finite: false` runs ignore bookings but keep calendar time. |
| 1 | Backward scheduling and the BR-23.2 fallback to forward with `late_minutes` (FR-23.2) | built (slice 10) | `direction: backward` on `POST /runs`; `domain.placeBackward` searches the latest start of each order's first operation that still ends by the order's earliest due date, using the forward engine so calendars, bookings, blocks, changeovers and networks apply unchanged. A latest start before run start + 1 h, or an order that cannot end on time, is placed forward; late orders carry `late_minutes` and a SCHED-0003 warning. Orders without a due date are placed forward. Operations after the first are placed as early as possible after it (no slack is spread between them), lateness is measured against the due timestamp, not end-of-day of the delivery date, and moves and the CP-SAT campaign path are unchanged. |
| 1 | Job scheduling (queue-before, setup, process, overlap, transport, queue-after; FR-23.3) and operations mode in daily buckets (FR-23.2) | later slice of PSG-23 | An operation is one block of process minutes. `mode` is accepted and recorded, and both modes place at minute granularity. Setup that must not split (BR-23.3), overlap quantity (BR-23.5) and transit time are not modelled. |
| 1 | Duration from route times and efficiency (BR-23.1) | partial (slice 18) | Minutes come from the planned hours of wave 21's process and setup jobs (already route time times quantity); efficiency % of the scheduling resource is not applied. |
| 1 | Secondary resources booked with the primary (BR-23.4, SCHED-0005) | built (slice 12) | `Operation.Secondaries`, `domain/secondary.go`, `scheduled_job_secondary`. A primary job and a secondary booking on the same resource are kept apart by the scheduler (it sees both as busy time), not by a database constraint, because the two live in different tables. Secondary resources are an explicit list per operation; choosing them by capability or resource group is wave 9. Not yet: secondary bookings as their own bars on the secondary resource's Gantt row (they show on the primary bar, in the jobs table and in the load), in the block-utilization inquiry and in the setup changeover sequencing of the secondary resource; secondaries only for setup or only for process (a secondary is held for the whole job, changeover included); and `ScheduledJob.secondary_resource_ids` on the stored job row (the list lives on the operation and the booking rows). The CP-SAT model does not book secondaries yet. |
| 1 | Resource selection by resource group and capability with level and expiry (FR-23.2, FR-23.12, SCHED-0004 for capabilities) | handoff — wave 9 (resources, groups, capabilities) | Candidates are explicit resource ids in priority order. Slice 1 keeps its own `scheduling_resource` table with a weekly calendar; wave 9 resources, calendars, efficiency % and capacity-per-day overrides replace it. |
| 1 | Setup optimization inside schedule runs: the changeover from the matrix (or the route setup, SCHED-0012) laid in front of each job on a resource | built | `domain/constraints.go`; the changeover is part of the job's span and stored as `scheduled_job.changeover_minutes` on its first segment. The run places orders in priority order and prices each changeover from the job before it; it does not reorder jobs to cut changeovers outside a campaign. |
| 1 | Changeover kept in one piece: BR-23.3 says setup is never split across closed time and moves to the next open interval when it does not fit | built | `fillSlot` in `domain/constraints.go`; an exact move start that would split it is SCHED-0002. Route setup without a matrix is not laid as a separate block (see the job scheduling row above). |
| 1 | Changeover cost and the matrix's cleaning operation set on each scheduled changeover | built (slice 7) | `Placement.ChangeoverCost` / `CleaningSetID`, stored on `scheduled_job` (first segment), on the Gantt bar and in `job.scheduled`. A route-setup fallback (SCHED-0012) has no matrix price and carries none. The cleaning set is a reference only: generating the cleaning operations it names is part of the cleanout orders handoff (deliverable 7). |
| 1 | Reported hours and `finished` jobs frozen with remaining minutes (BR-23.6) | handoff — wave 25 (shop-floor feedback) | Only firmed jobs are frozen; there is no reported-hours input yet. |
| 2 | Gantt with drag-reschedule, dry-run preview, constraint warnings and firm | built | `apps/web/src/modules/scheduling`. Bars are DOM buttons; a keyboard move form gives the same preview and commit. |
| 2 | Canvas rendering above 1,000 bars, row virtualization, zoom, Shift+drag of a whole order, undo stack of 20 moves, user drag-snap setting | later slice of PSG-23 | The page fetches a 1-14 day window and snaps to 5 minutes. |
| 2 | *Override scheduling constraints* duty to commit a move with violations | later slice of PSG-23 | Every violation blocks the commit. |
| 3 | Capacity load by resource and ISO week with bottleneck flag (FR-23.9) | built | `GET /v1/scheduling/capacity-load`; weeks start Monday 00:00 UTC. |
| 3 | Load by day and drill-down to jobs | built (slice 16) | `period=day` on `GET /capacity-load` and `GET /capacity-load/jobs`. Days are local days of the resource's calendar time zone. |
| 3 | Load by resource group; US week numbering; weeks in the resource's time zone | later slice of PSG-23 | Groups come from wave 9; weeks start Monday 00:00 UTC (slice 3). |
| 3 | Materialized CapacityLoad table refreshed per run and move; `bottleneck.detected` event once per episode (AC-23.6, SCHED-0015) | later slice of PSG-23 | Load is computed on read, so it is never stale; the event needs episode state and is not declared in AsyncAPI yet. |
| 4 | CP-SAT job-shop optimizer (FR-23.6, BR-23.9) | built in `services/scheduler-py` | Weighted lateness + lambda x makespan, 2,000-operation window, time limit, `num_workers` 8; ft06 proves 55. |
| 4 | Optimization runs from the scheduling service (`algorithm: cp-sat`, solver_status, heuristic kept on UNKNOWN, AC-23.9, SCHED-0006) | later slice of PSG-23 | The Go service calls scheduler-opt for campaign sequencing only (slice 6, deliverable 7 below). Schedule runs do not call the job-shop endpoint yet; `schedule_run.algorithm` is constrained to `heuristic` and `solver_status` stays null. scheduler-opt already takes candidate machines and network steps and returns the chosen machines. |
| 5 | Schedule dates flow to production orders and planning (FR-23.10) | partial — consumers are waves 21 and 22 | `job.scheduled` and `orderdates.changed` are published through the outbox with start, end and late minutes. Production order date updates and the `planning.finite-capacity` flag (AC-23.8) are owned by the consuming waves. |
| 5 | Operations fed from wave 21 production orders and jobs instead of the API | built (slices 18 and 20) for `order.scheduled.v1` and the withdrawal in `order.statuschanged.v1` | `POST /operations` stays for manual entry. Built (slice 20): withdrawing an order's operations when production resets, cancels or merges it (see Slice 20 below). Not built: order priority, quantities, the order's attributes and campaign key, and a wave 9 feed that creates the resource mappings (slice 19 maps by hand: an event's `resourceId` that is neither mapped nor a scheduling resource id is recorded as left out). |
| 6 | Resource networks: definition, edges, edge rules (FR-23.13, BR-23.11 wait check) | built | `POST/GET /v1/scheduling/networks`; `domain.ResourceNetwork` (`Successors` in transfer-time order, `ChainAllowed`, `WaitOK`). |
| 6 | Networks as heuristic constraints: operation n+1 only on an edge from operation n's resource, transfer time, maximum wait with the predecessor delayed up to 7 days, SCHED-0009 `NETWORK_INFEASIBLE`, SCHED-0010 behind a frozen predecessor, move checks (AC-23.10) | built | `domain/constraints.go`, `domain.PlanMove`; networks active on the run date are read by code. The 7-day limit is fixed (`NetworkDelayDays` in the domain) until site parameters exist. A maximum wait shorter than the transfer is now refused when a network is created. |
| 6 | CP-SAT `AddAllowedAssignments` for network transitions in the optimizer | built in `services/scheduler-py` | Each operation may carry the edges of the step into it; allowed machine pairs use `AddAllowedAssignments` on the choice variables, and each pair enforces its transfer time and maximum wait. Not called from the Go service yet (deliverable 4). |
| 6 | Capacity block windows and changeovers in the CP-SAT model | later slice of PSG-23 | The job-shop model has no calendars, blocks or setup times; the heuristic applies them. |
| 6 | Booking the edge's transfer resource (a transfer line) during the transfer | later slice of PSG-23 | `secondary_resource_id` is stored on the edge but not booked; operation secondaries (BR-23.4) are built in slice 12, the edge's transfer line is still not booked for the transfer window. |
| 6 | Network per site and assigned to a route operation chain; network edge editing | handoff — wave 9 (sites) and wave 21 (route operation chains) | A network is tenant-wide and each operation names the network of the step into it (`networkId`) until wave 21 route operation chains carry it. Networks can be renamed and ended on a date since slice 15 (`PATCH /networks/{id}`); their transitions are fixed at creation, so changing an edge means creating a new network, and an end date can be moved but not cleared. |
| 7 | Campaigns, setup matrix sequencing, leading setup and cleanouts in the plan (FR-23.14, FR-23.15, BR-23.12, BR-23.13, AC-23.11, SCHED-0011, SCHED-0012) | built | `/v1/scheduling/campaigns`, `:sequence`, `:start`, `/setup-matrices`, `/cleanout-rules`. |
| 7 | Placing a sequenced campaign contiguously on the Gantt (changeover and cleanout jobs as scheduled jobs; cleanout never split, BR-23.13) | later slice of PSG-23 | The plan is stored on the campaign; schedule runs do not place campaign jobs yet. |
| 7 | Cleanout and setup orders as lightweight production orders of type `cleanout` (`cleanout_order_id`, `setup_order_id`), consuming cleaning materials and labour | handoff — wave 21 (production orders) | Columns exist and stay null; `campaign.sequenced` carries the cleanouts for wave 21 to create them. |
| 7 | Campaign start from the first order Started; complete and cancel transitions | built (slice 14) as explicit commands; automatic transitions are a handoff to wave 21 (order status events) | `:start`, `:complete` and `:cancel` are planner commands. Completing when the last order ends and starting when the first order starts need wave 21 order events. Cancelling does not unplace or cancel the orders themselves; production decides from `campaign.cancelled`. |
| 7 | Campaign proposals from planned and firmed orders sharing a campaign key within a window (default 14 days) | built (slice 8) as an inquiry | `GET /campaign-proposals` over scheduled jobs and the campaign proposals screen; the planner enters quantities and creates the campaign. The window is a query parameter until site parameters exist. |
| 7 | The nightly `campaign-proposal` job (23:00 per site and key) storing `proposed` campaigns and replacing them per key, with the count reported | later slice of PSG-23 | Needs the job runner (FR-23.11) and an order quantity to store: scheduling operations carry no quantity, so proposals are not stored and quantities are typed when a campaign is created. |
| 7 | Proposals from wave 22 planned orders not yet scheduled, and orders excluded once Started | handoff — wave 22 (planned orders) and wave 25 (order start) | Only orders with a scheduled job are proposed; scheduling has no Started status yet, so every scheduled order outside a campaign is eligible. |
| 7 | CP-SAT circuit sequencing (`AddCircuit`, at most 50 orders) | built (slice 6) | `sequenceRule: cp-sat`; `:sequence` calls `POST /v1/optimize/campaign-sequence` and keeps a certified, strictly cheaper order (BR-23.9), otherwise nearest-neighbour with SCHED-0006. The solver gets 20 s, not the PRD's 30 s, because the platform router ends every request at 30 s; a longer limit needs the asynchronous run path (FR-23.11). |
| 7 | Setup matrix and campaign per resource group; setup group taken from the product (w8) or formula (w24) | handoff — wave 9 (resource groups), waves 8 and 24 | Matrices and campaigns are per resource; each campaign order carries its setup group. |
| 7 | Batch and run-hour counters carried in from the resource's history | built for campaigns (slice 6); actual counts are a handoff to wave 25 | `scheduling_cleanout_counter` carries the planned batches and run minutes from each started campaign into the next; a cleaning changeover or a cleanout resets it. Batches run outside campaigns and actual run hours from shop-floor feedback (wave 25) do not move it yet, and there is no endpoint to reset it by hand after an unplanned cleanout. |
| 7 | Posting the campaign changeover allocation as `campaign-changeover` overhead cost lines before orders end (FR-23.16) | handoff — wave 21 (order costing and journals) | `:allocate-cost` computes the BR-23.14 split (amounts sum exactly to the cost, residual to the largest order) and writes nothing. |
| 7 | SCHED-0013: refuse to end an order while the campaign cleanout is unended; `campaign.allowEarlyEnd` estimate and true-up | handoff — wave 21 (order end) | The order end lives in wave 21. |
| 8 | Capacity blocks: definition and the BR-23.15 matching and priority rule (FR-23.17) | built | `POST/GET /v1/scheduling/capacity-blocks`; `domain.Placeable`, `MatchesAny`. |
| 8 | Blocks applied in schedule runs and moves (job placed inside a matching block, otherwise unblocked capacity or late; SCHED-0014 warning; AC-23.13 end to end) | built | Active blocks are expanded over the run horizon in the resource's time zone; attributes are entered on the operation. |
| 8 | Operation attributes fed from product variant dimensions (w8) and order or batch attributes (w21, w24) | handoff — waves 8, 21 and 24 | Attributes are set on `POST /operations` until those waves publish them. |
| 8 | RRULE recurrence on capacity blocks | built | `recurrence {rule, startDate, startMinute, endMinute}` on `POST /capacity-blocks`; the subset is FREQ=DAILY or WEEKLY with INTERVAL, BYDAY (plain weekday codes) and COUNT or UNTIL. Other RRULE parts (BYMONTHDAY, BYSETPOS, ordinal weekdays, MONTHLY) are refused rather than guessed. |
| 8 | Block update and deactivation | built (slice 15) for priority, condition and the active flag | `PATCH /capacity-blocks/{id}` and `GET /capacity-blocks/{id}`, duty `scheduling.capacity.write`, Idempotency-Key and If-Match. The resource, code and window (weekly, dated or recurrence) are fixed at creation: to change them, deactivate the block and create another. Jobs already placed stay where they are until the next run or move; the answer and the event list those the blocks now keep out. |
| 8 | Showing block breaches on the Gantt (a booked job inside a block it may not use) | later slice of PSG-23 | Listed once, in the block update answer and `capacityblock.updated`; the Gantt does not mark them. |
| 6 | Jobs whose transitions break a network after its end date is moved later | later slice of PSG-23 | Ending a network earlier only relaxes the schedule. Moving the end date later re-applies the network to days that runs may have planned without it; those jobs are not listed in `network.updated` and keep their placement until the next run or move. |

## PRD items (section 4.23) not built yet

| Item | Status | Reason |
|---|---|---|
| FR-23.1 ResourceCapacity table materialized for 365 days, rebuilt on calendar events; efficiency % and per-day capacity overrides | later slice of PSG-23 (calendar events from wave 9) | Open intervals are computed from the weekly calendar plus downtimes and extra shifts (slice 9) on every read and run (`GET /resources/{id}/capacity`). A stored table is not needed while nothing else reads it; efficiency % and calendar events are wave 9. |
| AC-23.7 calendar change closes capacity and flags affected jobs (SCHED-0002 warnings on existing jobs) | partial (slice 9) | A downtime closes capacity at once (no 30 s lag: capacity is computed on read), returns the overlapping jobs when it is created, and the Gantt flags every bar outside open capacity (`outsideCapacity`) until it is moved. A change of the weekly calendar itself is built (slice 13, `PATCH /resources/{id}`, affected jobs returned and flagged on the Gantt); calendar events from wave 9 and per-job warning rows are not, and a calendar change does not re-run the schedule. |
| FR-23.8 pegs fed from master planning | handoff — wave 22 (pegging) | Pegs are entered through `POST /order-pegs`; a consumer of wave 22 pegging events would fill the same table. Propagation itself is built (slice 4). |
| AC-23.3 sales line delay in the move preview | handoff — wave 27 (sales lines) and wave 22 (pegging to demand) | Pegs link orders to orders; the preview lists every pegged order with its old and new dates, but sales lines are not pegged here. |
| Schedule runs honouring pegs (a dependent placed no earlier than its parent ends) | built (slice 11) | `domain.Schedule` places parents first (`pegPlan`) and floors the dependent's first operation at the parents' last end (`pegFloor`; `store.pegsForRun` loads pegs and the ends of parents outside the run); an unplaced parent (in the run, or outside it with an operation stored as unplaced) leaves the dependent unplaced. Not yet: a peg-forced delay is not reported as a warning; `:move` without propagate still ignores pegs; a selection run that moves a parent later does not push its dependents outside the selection (run them too, or the site); a parent outside the run that was never scheduled sets no floor; a backward run schedules a parent against its own due date, not back from its dependent's start. |
| PRD 4.23.17 limit of 100 orders in the propagation preview | later slice of PSG-23 | The preview walks the same 500 orders / 5 levels as the commit, so what is previewed is what is saved. |
| FR-23.11 runs as Temporal jobs with checkpoints every 500 operations; nightly site run at 00:30; per-order `:schedule` (scope `order`) | later slice of PSG-23 | Runs execute synchronously inside one transaction (5,000 operations schedule in well under the 60 s budget in the domain test). |
| Sites: `siteId` on resources, runs and the Gantt query | handoff — wave 9 (sites on resources) | Slice 1 schedules every resource of the tenant as one plant. |
| Late orders inquiry (4.23.14) | built (slice 17) | `GET /late-orders` and the Late orders page: late and unplaced orders with due, planned end, lateness. Item and quantity per order are not shown. |
| Order header data on bars and late orders (item, quantity) | handoff — wave 21 | Bars and late orders carry the order id, operation counts and dates only. |
| Block utilization inquiry (4.23.14) | built (slice 5) | `GET /resources/{id}/block-utilization` per resource and range, shown on the resource detail screen. A cross-resource report screen with period totals is not built; the inquiry is per resource. |
| Resource utilization and changeover time of a run (4.23.14) | built (slice 17) | `GET /runs/{id}/kpis` and the Plan measures card on the run detail; they describe the jobs the run still holds. |
| Run comparison and network feasibility reports (4.23.14) | later slice of PSG-23 | Comparison needs per-run snapshots of the jobs, which a run replaces; not stored. |
| Dispatch list, Gantt print, campaign sheet, cleanout record documents (4.23.15) | later slice of PSG-23 | No document templates yet. |
| Feature flags `scheduling.cp-sat`, `scheduling.propagate-dependents`; config `SCHED_OPT_TIME_LIMIT_S` | later slice of PSG-23 | No flag client; CP-SAT is chosen per campaign with its sequence rule and switched off by leaving `SCHED_OPT_URL` empty, propagation is chosen per move with `propagate`, and the campaign solver limit is fixed at 20 s. |
| Site parameters (default method and direction, resource selection rule, safety/queue time, drag snap, propagation limit) | later slice of PSG-23 | Defaults are fixed: jobs mode, forward, least finish, propagation 500 orders / 5 levels (`domain.DefaultPropagation*`). |
| Extension points `scheduling.constraint`, `scheduling.objective` | later slice of PSG-23 | |
| `ScheduledJob.overlap_qty`; `ScheduleRun.time_limit_s`, `started_at`, `objective_value` | later slice of PSG-23 | Columns arrive with the features that fill them. |
| Rollback by re-pointing to a previous run | later slice of PSG-23 | A run replaces the jobs it schedules; earlier runs stay as history rows only. |
| Optimistic version check on moves (If-Match) | later slice of PSG-23 | Moves lock the order's operations and the exclusion constraint refuses concurrent bookings, so a stale screen gets a fresh preview rather than a silent overwrite; resources now have an If-Match update (slice 13), moves still do not take If-Match. |
| scheduler-opt KEDA scale-to-zero and the `scheduling-db` CloudNativePG cluster | handoff — deploy owner | Charts ship the deployment and service only. |
| Playwright Gantt move + propagate test (4.23.9) | later slice of PSG-23 | Covered by the store move and propagation tests and the page tests. |
| `sched-propagation` fixture (pegged chains 5 deep, 4.23.9) | partial | The store test builds a 7-order chain in code and checks the 5-level cut; no fixture file under testdata. |
