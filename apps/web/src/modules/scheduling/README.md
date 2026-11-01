# Scheduling web module (wave 23)

Screens for `services/scheduling` (PRD 4.23.6). Paths are `/api/v1/scheduling/...`
from `services/scheduling/api/openapi.yaml`; the BFF rewrites them to the service.

| Route | Page | Service calls |
|---|---|---|
| `/scheduling` | Gantt workspace: resource rows over a 1-14 day UTC window, the changeover at the start of a job hatched over its bar, drag or Move to preview (`dryRun`, with network, block and changeover violations) and Confirm (Idempotency-Key per preview), "Move pegged orders too" (`propagate`) with the pegged orders that move and their delay in the preview and a SCHED-0007 warning when the walk was cut, Firm / Unfirm, jobs table with changeover minutes, the matrix cost and cleaning set (also in each bar's title) for keyboard use; jobs outside open capacity (`outsideCapacity`, e.g. inside a later downtime) are outlined and badged | `GET /resources`, `GET /gantt`, `POST /jobs/{id}:move`, `POST /jobs/{id}:firm` |
| `/scheduling/load` | Capacity load by resource and UTC week (bottleneck badges) or local day of the resource's time zone, with a Jobs link per row | `GET /capacity-load`, `GET /resources` |
| `/scheduling/load/jobs?resourceId&from&to` | Jobs booked behind a load row: `from` and `to` are the row's `start` and `end` instants | `GET /capacity-load/jobs`, `GET /resources/{id}` |
| `/scheduling/runs`, `/runs/new`, `/runs/:id` | Run list, start a site or selection run (finite or infinite), run detail with plan measures (jobs the run still holds, late orders, changeover time, utilization per resource including secondary resources, against each resource's local calendar), warnings (SCHED-0010, SCHED-0012, SCHED-0014) and unplaced operations (including SCHED-0009 `NETWORK_INFEASIBLE`) | `GET/POST /runs`, `GET /runs/{id}`, `GET /runs/{id}/kpis` |
| `/scheduling/late-orders` | Late orders (PRD 4.23.14): orders with a job that ends after its due date, most late first, then orders with an unplaced operation, with the delivery date, planned end, lateness and the pegged orders that consume each one; threshold from any lateness to a week. Loading, empty and error states with retry | `GET /late-orders` |
| `/scheduling/resources`, `/resources/new`, `/resources/:id` | Resource list, create with one shift on chosen weekdays, detail with weekly calendar, next 7 days of open capacity and the capacity block windows of those days with held, used and unused minutes (its own loading, empty and error states) | `GET/POST /resources`, `GET /resources/{id}`, `GET /resources/{id}/capacity`, `GET /resources/{id}/block-utilization` |
| `/scheduling/operations`, `/operations/new` | Operation list with status filter, setup group, network and attributes; create with candidate resources in the order checked, setup group, route setup, network and attributes (one `name = value` per line) | `GET/POST /operations`, `GET /resources`, `GET /networks` |
| `/scheduling/order-pegs`, `/order-pegs/new` | Order pegs with an order filter and Remove; create by choosing the parent and dependent from the orders that have operations (loop refusal SCHED-0121 on the field) | `GET/POST /order-pegs`, `DELETE /order-pegs/{id}`, `GET /operations` |
| `/scheduling/campaigns`, `/campaigns/new`, `/campaigns/:id` | Campaign list with status filter; create (resource, key, planned start, rule including the CP-SAT optimizer, limits); detail with orders (add, remove), Sequence and Start (If-Match with the version, Idempotency-Key per command, reload on SCHED-0115, "sequence again" on SCHED-0123), the plan (SCHED-0006 optimizer outcome, leading setup, changeovers, SCHED-0012 warnings, cleanouts, the cleanout counter carried in and out) and the cost allocation | `GET/POST /campaigns`, `GET /campaigns/{id}`, `POST/DELETE /campaigns/{id}/orders`, `POST :sequence`, `:start`, `:allocate-cost` |
| `/scheduling/campaign-proposals` | Campaign proposals (FR-23.14): scheduled orders outside any campaign that share a campaign key on one resource, filtered by start date, window (7 to 90 days) and resource; each proposal lists its orders with their scheduled start, due date, run time and firmed flag, takes a quantity per order and a sequence rule, and creates the campaign (one Idempotency-Key per edit, row errors mapped from `orders[i]`, SCHED-0114 explained). Loading, empty and error states with retry; linked from the campaign list | `GET /campaign-proposals`, `POST /campaigns`, `GET /resources` |
| `/scheduling/setup-matrices`, `/setup-matrices/new`, `/setup-matrices/:id` | Matrix versions by resource; create with one changeover per line (from, to, minutes, cost, cleaning set); detail | `GET/POST /setup-matrices`, `GET /setup-matrices/{id}` |
| `/scheduling/cleanout-rules`, `/cleanout-rules/new` | Cleanout rules with their triggers; create | `GET/POST /cleanout-rules` |
| `/scheduling/networks`, `/networks/new`, `/networks/:id` | Resource networks; create with a row per allowed transition; detail with transfer, maximum wait and transfer resource, and a rename and end-date form | `GET/POST /networks`, `GET/PATCH /networks/{id}` |
| `/scheduling/capacity-blocks`, `/capacity-blocks/new` | Blocks by resource with window and condition; create weekly, by a recurrence rule (RRULE subset, first date, window; checked in the form and by the service) or dated, with one predicate per line | `GET/POST /capacity-blocks` |
| `/scheduling/capacity-blocks/:id` | Change a block's priority, condition and active flag; a stale version offers the latest | `GET/PATCH /capacity-blocks/{id}` |
| `/scheduling/capacity-overrides`, `/capacity-overrides/new` | Downtimes and extra shifts by resource (list with remove; create with UTC times and a reason); a created downtime lists the booked jobs it overlaps | `GET/POST /resources/{id}/capacity-overrides`, `DELETE /resources/{id}/capacity-overrides/{overrideId}` |

Money and quantities stay decimal strings from form to service; forms never
parse them into floats. Field checks shared by the slice 2 forms live in
`forms.ts`, and `ResourceGate` gives forms that need a resource their loading,
error and empty states.

The Gantt is SVG: bars are placed with geometry attributes and coloured by
order through CSS classes, so there are no inline styles. The SVG is a pointer
surface; the jobs table under it offers Move and Firm from the keyboard.

Not built here (see `services/scheduling/NOT-IMPLEMENTED.md`): canvas rendering
above 1,000 bars, zoom, Shift+drag of a whole order, the undo stack, drag across
rows to reassign (use the Resource field of the move form), and open-interval
shading.
