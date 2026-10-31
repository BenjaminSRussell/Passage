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
Web: capacity overrides list
(by resource, remove) and create form (UTC times, reason, affected jobs shown
after saving), linked from the resource detail screen and the navigation. The
OpenAPI document and the AsyncAPI messages (title and summary on every message)
pass the doc gates with no FAIL lines.

Everything else in the ticket and PRD 4.23 is listed here.

No file outside the wave 23 ownership entry was changed, so there are no
moved-out handoff rows.

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Forward operation scheduling, finite and infinite | built | `domain.Schedule`; `finite: false` runs ignore bookings but keep calendar time. |
| 1 | Backward scheduling and the BR-23.2 fallback to forward with `late_minutes` (FR-23.2) | built (slice 10) | `direction: backward` on `POST /runs`; `domain.placeBackward` searches the latest start of each order's first operation that still ends by the order's earliest due date, using the forward engine so calendars, bookings, blocks, changeovers and networks apply unchanged. A latest start before run start + 1 h, or an order that cannot end on time, is placed forward; late orders carry `late_minutes` and a SCHED-0003 warning. Orders without a due date are placed forward. Operations after the first are placed as early as possible after it (no slack is spread between them), lateness is measured against the due timestamp, not end-of-day of the delivery date, and moves and the CP-SAT campaign path are unchanged. |
| 1 | Job scheduling (queue-before, setup, process, overlap, transport, queue-after; FR-23.3) and operations mode in daily buckets (FR-23.2) | later slice of PSG-23 | An operation is one block of process minutes. `mode` is accepted and recorded, and both modes place at minute granularity. Setup that must not split (BR-23.3), overlap quantity (BR-23.5) and transit time are not modelled. |
| 1 | Duration from route times and efficiency (BR-23.1) | handoff — wave 21 (production operations) | Operations arrive with their minutes through `POST /v1/scheduling/operations`; deriving minutes from `run_time_h_per`, quantity and efficiency needs wave 21 route operations. |
| 1 | Secondary resources booked with the primary (BR-23.4, SCHED-0005) | later slice of PSG-23 | No `secondary_resource_ids` on operations or jobs yet. |
| 1 | Resource selection by resource group and capability with level and expiry (FR-23.2, FR-23.12, SCHED-0004 for capabilities) | handoff — wave 9 (resources, groups, capabilities) | Candidates are explicit resource ids in priority order. Slice 1 keeps its own `scheduling_resource` table with a weekly calendar; wave 9 resources, calendars, efficiency % and capacity-per-day overrides replace it. |
| 1 | Setup optimization inside schedule runs: the changeover from the matrix (or the route setup, SCHED-0012) laid in front of each job on a resource | built | `domain/constraints.go`; the changeover is part of the job's span and stored as `scheduled_job.changeover_minutes` on its first segment. The run places orders in priority order and prices each changeover from the job before it; it does not reorder jobs to cut changeovers outside a campaign. |
| 1 | Changeover kept in one piece: BR-23.3 says setup is never split across closed time and moves to the next open interval when it does not fit | built | `fillSlot` in `domain/constraints.go`; an exact move start that would split it is SCHED-0002. Route setup without a matrix is not laid as a separate block (see the job scheduling row above). |
| 1 | Changeover cost and the matrix's cleaning operation set on each scheduled changeover | built (slice 7) | `Placement.ChangeoverCost` / `CleaningSetID`, stored on `scheduled_job` (first segment), on the Gantt bar and in `job.scheduled`. A route-setup fallback (SCHED-0012) has no matrix price and carries none. The cleaning set is a reference only: generating the cleaning operations it names is part of the cleanout orders handoff (deliverable 7). |
| 1 | Reported hours and `finished` jobs frozen with remaining minutes (BR-23.6) | handoff — wave 25 (shop-floor feedback) | Only firmed jobs are frozen; there is no reported-hours input yet. |
| 2 | Gantt with drag-reschedule, dry-run preview, constraint warnings and firm | built | `apps/web/src/modules/scheduling`. Bars are DOM buttons; a keyboard move form gives the same preview and commit. |
| 2 | Canvas rendering above 1,000 bars, row virtualization, zoom, Shift+drag of a whole order, undo stack of 20 moves, user drag-snap setting | later slice of PSG-23 | The page fetches a 1-14 day window and snaps to 5 minutes. |
| 2 | *Override scheduling constraints* duty to commit a move with violations | later slice of PSG-23 | Every violation blocks the commit. |
| 3 | Capacity load by resource and ISO week with bottleneck flag (FR-23.9) | built | `GET /v1/scheduling/capacity-load`; weeks start Monday 00:00 UTC. |
| 3 | Load by resource group and by day; drill-down to jobs; US week numbering | later slice of PSG-23 | Groups come from wave 9. |
| 3 | Materialized CapacityLoad table refreshed per run and move; `bottleneck.detected` event once per episode (AC-23.6, SCHED-0015) | later slice of PSG-23 | Load is computed on read, so it is never stale; the event needs episode state and is not declared in AsyncAPI yet. |
| 4 | CP-SAT job-shop optimizer (FR-23.6, BR-23.9) | built in `services/scheduler-py` | Weighted lateness + lambda x makespan, 2,000-operation window, time limit, `num_workers` 8; ft06 proves 55. |
| 4 | Optimization runs from the scheduling service (`algorithm: cp-sat`, solver_status, heuristic kept on UNKNOWN, AC-23.9, SCHED-0006) | later slice of PSG-23 | The Go service calls scheduler-opt for campaign sequencing only (slice 6, deliverable 7 below). Schedule runs do not call the job-shop endpoint yet; `schedule_run.algorithm` is constrained to `heuristic` and `solver_status` stays null. scheduler-opt already takes candidate machines and network steps and returns the chosen machines. |
| 5 | Schedule dates flow to production orders and planning (FR-23.10) | partial — consumers are waves 21 and 22 | `job.scheduled` and `orderdates.changed` are published through the outbox with start, end and late minutes. Production order date updates and the `planning.finite-capacity` flag (AC-23.8) are owned by the consuming waves. |
| 5 | Operations fed from wave 21 production orders and jobs instead of the API | handoff — wave 21 (production orders) | Slice 1 registers operations through its own API; a consumer of wave 21 events is not built. |
| 6 | Resource networks: definition, edges, edge rules (FR-23.13, BR-23.11 wait check) | built | `POST/GET /v1/scheduling/networks`; `domain.ResourceNetwork` (`Successors` in transfer-time order, `ChainAllowed`, `WaitOK`). |
| 6 | Networks as heuristic constraints: operation n+1 only on an edge from operation n's resource, transfer time, maximum wait with the predecessor delayed up to 7 days, SCHED-0009 `NETWORK_INFEASIBLE`, SCHED-0010 behind a frozen predecessor, move checks (AC-23.10) | built | `domain/constraints.go`, `domain.PlanMove`; networks active on the run date are read by code. The 7-day limit is fixed (`NetworkDelayDays` in the domain) until site parameters exist. A maximum wait shorter than the transfer is now refused when a network is created. |
| 6 | CP-SAT `AddAllowedAssignments` for network transitions in the optimizer | built in `services/scheduler-py` | Each operation may carry the edges of the step into it; allowed machine pairs use `AddAllowedAssignments` on the choice variables, and each pair enforces its transfer time and maximum wait. Not called from the Go service yet (deliverable 4). |
| 6 | Capacity block windows and changeovers in the CP-SAT model | later slice of PSG-23 | The job-shop model has no calendars, blocks or setup times; the heuristic applies them. |
| 6 | Booking the edge's transfer resource (a transfer line) during the transfer | later slice of PSG-23 | `secondary_resource_id` is stored on the edge but not booked; secondary resources in general (BR-23.4) are a later slice. |
| 6 | Network per site and assigned to a route operation chain; network update and deactivation endpoints | handoff — wave 9 (sites) and wave 21 (route operation chains) | A network is tenant-wide and each operation names the network of the step into it (`networkId`) until wave 21 route operation chains carry it. Networks are create-and-read; update and deactivation are a later slice of PSG-23. |
| 7 | Campaigns, setup matrix sequencing, leading setup and cleanouts in the plan (FR-23.14, FR-23.15, BR-23.12, BR-23.13, AC-23.11, SCHED-0011, SCHED-0012) | built | `/v1/scheduling/campaigns`, `:sequence`, `:start`, `/setup-matrices`, `/cleanout-rules`. |
| 7 | Placing a sequenced campaign contiguously on the Gantt (changeover and cleanout jobs as scheduled jobs; cleanout never split, BR-23.13) | later slice of PSG-23 | The plan is stored on the campaign; schedule runs do not place campaign jobs yet. |
| 7 | Cleanout and setup orders as lightweight production orders of type `cleanout` (`cleanout_order_id`, `setup_order_id`), consuming cleaning materials and labour | handoff — wave 21 (production orders) | Columns exist and stay null; `campaign.sequenced` carries the cleanouts for wave 21 to create them. |
| 7 | Campaign start from the first order Started; complete and cancel transitions | partial — wave 21 (order status events) | `:start` freezes a planned campaign explicitly; there are no complete or cancel endpoints yet (later slice of PSG-23). |
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
| 8 | Block update and deactivation | later slice of PSG-23 | `active` is set at creation only. |

## PRD items (section 4.23) not built yet

| Item | Status | Reason |
|---|---|---|
| FR-23.1 ResourceCapacity table materialized for 365 days, rebuilt on calendar events; efficiency % and per-day capacity overrides | later slice of PSG-23 (calendar events from wave 9) | Open intervals are computed from the weekly calendar plus downtimes and extra shifts (slice 9) on every read and run (`GET /resources/{id}/capacity`). A stored table is not needed while nothing else reads it; efficiency % and calendar events are wave 9. |
| AC-23.7 calendar change closes capacity and flags affected jobs (SCHED-0002 warnings on existing jobs) | partial (slice 9) | A downtime closes capacity at once (no 30 s lag: capacity is computed on read), returns the overlapping jobs when it is created, and the Gantt flags every bar outside open capacity (`outsideCapacity`) until it is moved. A change of the weekly calendar itself needs wave 9 calendar events (scheduling resources have no calendar update endpoint), and no per-job warning rows are stored. |
| FR-23.8 pegs fed from master planning | handoff — wave 22 (pegging) | Pegs are entered through `POST /order-pegs`; a consumer of wave 22 pegging events would fill the same table. Propagation itself is built (slice 4). |
| AC-23.3 sales line delay in the move preview | handoff — wave 27 (sales lines) and wave 22 (pegging to demand) | Pegs link orders to orders; the preview lists every pegged order with its old and new dates, but sales lines are not pegged here. |
| Schedule runs honouring pegs (a dependent placed no earlier than its parent ends) | built (slice 11) | `domain.Schedule` places parents first (`pegPlan`) and floors the dependent's first operation at the parents' last end (`pegFloor`; `store.pegsForRun` loads pegs and the ends of parents outside the run); an unplaced parent (in the run, or outside it with an operation stored as unplaced) leaves the dependent unplaced. Not yet: a peg-forced delay is not reported as a warning; `:move` without propagate still ignores pegs; a selection run that moves a parent later does not push its dependents outside the selection (run them too, or the site); a parent outside the run that was never scheduled sets no floor; a backward run schedules a parent against its own due date, not back from its dependent's start. |
| PRD 4.23.17 limit of 100 orders in the propagation preview | later slice of PSG-23 | The preview walks the same 500 orders / 5 levels as the commit, so what is previewed is what is saved. |
| FR-23.11 runs as Temporal jobs with checkpoints every 500 operations; nightly site run at 00:30; per-order `:schedule` (scope `order`) | later slice of PSG-23 | Runs execute synchronously inside one transaction (5,000 operations schedule in well under the 60 s budget in the domain test). |
| Sites: `siteId` on resources, runs and the Gantt query | handoff — wave 9 (sites on resources) | Slice 1 schedules every resource of the tenant as one plant. |
| Order header data on bars (item, quantity) and the Late orders inquiry | handoff — wave 21 | Bars carry order id, operation number and late minutes only. |
| Block utilization inquiry (4.23.14) | built (slice 5) | `GET /resources/{id}/block-utilization` per resource and range, shown on the resource detail screen. A cross-resource report screen with period totals is not built; the inquiry is per resource. |
| Run comparison, resource utilization KPI, changeover analysis and network feasibility reports (4.23.14) | later slice of PSG-23 | Runs list makespan and lateness; comparison screens are not built. |
| Dispatch list, Gantt print, campaign sheet, cleanout record documents (4.23.15) | later slice of PSG-23 | No document templates yet. |
| Feature flags `scheduling.cp-sat`, `scheduling.propagate-dependents`; config `SCHED_OPT_TIME_LIMIT_S` | later slice of PSG-23 | No flag client; CP-SAT is chosen per campaign with its sequence rule and switched off by leaving `SCHED_OPT_URL` empty, propagation is chosen per move with `propagate`, and the campaign solver limit is fixed at 20 s. |
| Site parameters (default method and direction, resource selection rule, safety/queue time, drag snap, propagation limit) | later slice of PSG-23 | Defaults are fixed: jobs mode, forward, least finish, propagation 500 orders / 5 levels (`domain.DefaultPropagation*`). |
| Extension points `scheduling.constraint`, `scheduling.objective` | later slice of PSG-23 | |
| `ScheduledJob.overlap_qty`, `secondary_resource_ids`; `ScheduleRun.time_limit_s`, `started_at`, `objective_value` | later slice of PSG-23 | Columns arrive with the features that fill them. |
| Rollback by re-pointing to a previous run | later slice of PSG-23 | A run replaces the jobs it schedules; earlier runs stay as history rows only. |
| Optimistic version check on moves (If-Match) | later slice of PSG-23 | Moves lock the order's operations and the exclusion constraint refuses concurrent bookings, so a stale screen gets a fresh preview rather than a silent overwrite; resources have a `version` column but no update endpoint yet. |
| scheduler-opt KEDA scale-to-zero and the `scheduling-db` CloudNativePG cluster | handoff — deploy owner | Charts ship the deployment and service only. |
| Playwright Gantt move + propagate test (4.23.9) | later slice of PSG-23 | Covered by the store move and propagation tests and the page tests. |
| `sched-propagation` fixture (pegged chains 5 deep, 4.23.9) | partial | The store test builds a 7-order chain in code and checks the 5-level cut; no fixture file under testdata. |
