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
and create). Everything else in the ticket and PRD 4.23 is listed here.

No file outside the wave 23 ownership entry was changed, so there are no
moved-out handoff rows.

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Forward operation scheduling, finite and infinite | built | `domain.Schedule`; `finite: false` runs ignore bookings but keep calendar time. |
| 1 | Backward scheduling and the BR-23.2 fallback to forward with `late_minutes` | later slice of PSG-23 | `schedule_run.direction` is constrained to `forward`. Late minutes are already computed against the due time on forward runs. |
| 1 | Job scheduling (queue-before, setup, process, overlap, transport, queue-after; FR-23.3) and operations mode in daily buckets (FR-23.2) | later slice of PSG-23 | An operation is one block of process minutes. `mode` is accepted and recorded, and both modes place at minute granularity. Setup that must not split (BR-23.3), overlap quantity (BR-23.5) and transit time are not modelled. |
| 1 | Duration from route times and efficiency (BR-23.1) | handoff — wave 21 (production operations) | Operations arrive with their minutes through `POST /v1/scheduling/operations`; deriving minutes from `run_time_h_per`, quantity and efficiency needs wave 21 route operations. |
| 1 | Secondary resources booked with the primary (BR-23.4, SCHED-0005) | later slice of PSG-23 | No `secondary_resource_ids` on operations or jobs yet. |
| 1 | Resource selection by resource group and capability with level and expiry (FR-23.2, FR-23.12, SCHED-0004 for capabilities) | handoff — wave 9 (resources, groups, capabilities) | Candidates are explicit resource ids in priority order. Slice 1 keeps its own `scheduling_resource` table with a weekly calendar; wave 9 resources, calendars, efficiency % and capacity-per-day overrides replace it. |
| 1 | Setup optimization inside schedule runs: changeover jobs from the matrix replacing the route setup between consecutive jobs on a resource | later slice of PSG-23 | Campaign sequencing uses the matrix (deliverable 7); the run heuristic still places one block of process minutes per operation and does not read the matrix. |
| 1 | Reported hours and `finished` jobs frozen with remaining minutes (BR-23.6) | handoff — wave 25 (shop-floor feedback) | Only firmed jobs are frozen; there is no reported-hours input yet. |
| 2 | Gantt with drag-reschedule, dry-run preview, constraint warnings and firm | built | `apps/web/src/modules/scheduling`. Bars are DOM buttons; a keyboard move form gives the same preview and commit. |
| 2 | Canvas rendering above 1,000 bars, row virtualization, zoom, Shift+drag of a whole order, undo stack of 20 moves, user drag-snap setting | later slice of PSG-23 | The page fetches a 1-14 day window and snaps to 5 minutes. |
| 2 | *Override scheduling constraints* duty to commit a move with violations | later slice of PSG-23 | Every violation blocks the commit. |
| 3 | Capacity load by resource and ISO week with bottleneck flag (FR-23.9) | built | `GET /v1/scheduling/capacity-load`; weeks start Monday 00:00 UTC. |
| 3 | Load by resource group and by day; drill-down to jobs; US week numbering | later slice of PSG-23 | Groups come from wave 9. |
| 3 | Materialized CapacityLoad table refreshed per run and move; `bottleneck.detected` event once per episode (AC-23.6, SCHED-0015) | later slice of PSG-23 | Load is computed on read, so it is never stale; the event needs episode state and is not declared in AsyncAPI yet. |
| 4 | CP-SAT job-shop optimizer (FR-23.6, BR-23.9) | built in `services/scheduler-py` | Weighted lateness + lambda x makespan, 2,000-operation window, time limit, `num_workers` 8; ft06 proves 55. |
| 4 | Optimization runs from the scheduling service (`algorithm: cp-sat`, solver_status, heuristic kept on UNKNOWN, AC-23.9, SCHED-0006) | later slice of PSG-23 | The Go service does not call scheduler-opt yet; `schedule_run.algorithm` is constrained to `heuristic` and `solver_status` stays null. |
| 4 | OR-Library fixture set `orlib-jobshop-30` (ft10, ft20, la01-la20, abz5-abz9) and the 30/30 feasibility test (AC1) | later slice of PSG-23 | Only ft06 is checked in (`services/scheduler-py/testdata/orlib/ft06.txt`, also embedded in the Go heuristic test). The instance files must be copied from OR-Library verbatim, with their published optima. |
| 5 | Schedule dates flow to production orders and planning (FR-23.10) | partial — consumers are waves 21 and 22 | `job.scheduled` and `orderdates.changed` are published through the outbox with start, end and late minutes. Production order date updates and the `planning.finite-capacity` flag (AC-23.8) are owned by the consuming waves. |
| 5 | Operations fed from wave 21 production orders and jobs instead of the API | handoff — wave 21 (production orders) | Slice 1 registers operations through its own API; a consumer of wave 21 events is not built. |
| 6 | Resource networks: definition, edges, edge rules (FR-23.13, BR-23.11 wait check) | built | `POST/GET /v1/scheduling/networks`; `domain.ResourceNetwork` (`Successors` in transfer-time order, `ChainAllowed`, `WaitOK`). |
| 6 | Networks as scheduler constraints: the heuristic restricting operation n+1 to an edge of operation n, delaying the predecessor up to 7 days for the maximum wait, SCHED-0009 `NETWORK_INFEASIBLE` and SCHED-0010 warnings (AC-23.10); CP-SAT `AddAllowedAssignments` | later slice of PSG-23 | Runs do not read networks yet; the domain functions they will call are built and tested. |
| 6 | Network per site and assigned to a route operation chain; network update and deactivation endpoints | handoff — wave 9 (sites) and wave 21 (route operation chains) | A network is tenant-wide and applies to no chain until operations carry a network reference. Networks are create-and-read in this slice. |
| 7 | Campaigns, setup matrix sequencing, leading setup and cleanouts in the plan (FR-23.14, FR-23.15, BR-23.12, BR-23.13, AC-23.11, SCHED-0011, SCHED-0012) | built | `/v1/scheduling/campaigns`, `:sequence`, `:start`, `/setup-matrices`, `/cleanout-rules`. |
| 7 | Placing a sequenced campaign contiguously on the Gantt (changeover and cleanout jobs as scheduled jobs; cleanout never split, BR-23.13) | later slice of PSG-23 | The plan is stored on the campaign; schedule runs do not place campaign jobs yet. |
| 7 | Cleanout and setup orders as lightweight production orders of type `cleanout` (`cleanout_order_id`, `setup_order_id`), consuming cleaning materials and labour | handoff — wave 21 (production orders) | Columns exist and stay null; `campaign.sequenced` carries the cleanouts for wave 21 to create them. |
| 7 | Campaign start from the first order Started; complete and cancel transitions | partial — wave 21 (order status events) | `:start` freezes a planned campaign explicitly; there are no complete or cancel endpoints yet (later slice of PSG-23). |
| 7 | `campaign-proposal` job proposing campaigns from planned and firmed orders sharing a campaign key within 14 days | later slice of PSG-23 (needs wave 22 planned orders) | Campaigns are created by hand with their orders. |
| 7 | CP-SAT circuit sequencing (`AddCircuit`, 30 s, at most 50 orders) | later slice of PSG-23 | `sequence_rule` accepts `nearest-neighbor` and `manual`; `cp-sat` is refused with a field error. |
| 7 | Setup matrix and campaign per resource group; setup group taken from the product (w8) or formula (w24) | handoff — wave 9 (resource groups), waves 8 and 24 | Matrices and campaigns are per resource; each campaign order carries its setup group. |
| 7 | Batch and run-hour counters carried in from the resource's history | later slice of PSG-23 (needs wave 25 feedback) | Each campaign plan starts its counters at zero. |
| 7 | Posting the campaign changeover allocation as `campaign-changeover` overhead cost lines before orders end (FR-23.16) | handoff — wave 21 (order costing and journals) | `:allocate-cost` computes the BR-23.14 split (amounts sum exactly to the cost, residual to the largest order) and writes nothing. |
| 7 | SCHED-0013: refuse to end an order while the campaign cleanout is unended; `campaign.allowEarlyEnd` estimate and true-up | handoff — wave 21 (order end) | The order end lives in wave 21. |
| 8 | Capacity blocks: definition and the BR-23.15 matching and priority rule (FR-23.17) | built | `POST/GET /v1/scheduling/capacity-blocks`; `domain.Placeable`, `MatchesAny`. |
| 8 | Blocks applied in schedule runs (job placed inside a matching block, otherwise unblocked capacity or late; SCHED-0014 warning; AC-23.13 end to end) | later slice of PSG-23 | Runs do not read blocks yet; jobs carry no product or batch attributes until waves 8 and 21 feed them. |
| 8 | RRULE recurrence, block update and deactivation | later slice of PSG-23 | Blocks are weekly shifts or one dated window; `active` is set at creation only. |

## PRD items (section 4.23) not built yet

| Item | Status | Reason |
|---|---|---|
| FR-23.1 ResourceCapacity table materialized for 365 days, rebuilt on calendar events; efficiency % and capacity overrides | later slice of PSG-23 (calendar events from wave 9) | Open intervals are computed from the weekly calendar on every read and run. |
| `POST /v1/scheduling/resources/{id}/capacity-overrides` (downtime, extra shift) and the *Maintain capacity* screen for overrides | later slice of PSG-23 | Needs the materialized capacity table. |
| AC-23.7 calendar change closes capacity and flags affected jobs (SCHED-0002 warnings on existing jobs) | later slice of PSG-23 | Depends on calendar events and the capacity table. |
| FR-23.8 dependents pegged to the moved order (BR-23.10 propagation limit, SCHED-0007, AC-23.3 sales line delay in the preview) | handoff — wave 22 (pegging) | Moves propagate to the successors of the same order only. |
| FR-23.11 runs as Temporal jobs with checkpoints every 500 operations; nightly site run at 00:30; per-order `:schedule` (scope `order`) | later slice of PSG-23 | Runs execute synchronously inside one transaction (5,000 operations schedule in well under the 60 s budget in the domain test). |
| Sites: `siteId` on resources, runs and the Gantt query | handoff — wave 9 (sites on resources) | Slice 1 schedules every resource of the tenant as one plant. |
| Order header data on bars (item, quantity) and the Late orders inquiry | handoff — wave 21 | Bars carry order id, operation number and late minutes only. |
| Run comparison, resource utilization KPI, changeover analysis, network feasibility and block utilization reports (4.23.14) | later slice of PSG-23 | Runs list makespan and lateness; comparison screens are not built. |
| Dispatch list, Gantt print, campaign sheet, cleanout record documents (4.23.15) | later slice of PSG-23 | No document templates yet. |
| Feature flags `scheduling.cp-sat`, `scheduling.propagate-dependents`; config `SCHED_OPT_TIME_LIMIT_S` | later slice of PSG-23 | No flag client; the optimizer takes its time limit per request. |
| Site parameters (default method and direction, resource selection rule, safety/queue time, drag snap, propagation limit) | later slice of PSG-23 | Defaults are fixed: jobs mode, forward, least finish. |
| Extension points `scheduling.constraint`, `scheduling.objective` | later slice of PSG-23 | |
| `ScheduledJob.overlap_qty`, `secondary_resource_ids`; `ScheduleRun.time_limit_s`, `started_at`, `objective_value` | later slice of PSG-23 | Columns arrive with the features that fill them. |
| Rollback by re-pointing to a previous run | later slice of PSG-23 | A run replaces the jobs it schedules; earlier runs stay as history rows only. |
| Optimistic version check on moves (If-Match) | later slice of PSG-23 | Moves lock the order's operations and the exclusion constraint refuses concurrent bookings, so a stale screen gets a fresh preview rather than a silent overwrite; resources have a `version` column but no update endpoint yet. |
| scheduler-opt KEDA scale-to-zero and the `scheduling-db` CloudNativePG cluster | handoff — deploy owner | Charts ship the deployment and service only. |
| Playwright Gantt move test (4.23.9) | later slice of PSG-23 | Covered at unit level by the store move tests and the page tests. |
