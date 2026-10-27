# Scheduling web module (wave 23)

Screens for `services/scheduling` (PRD 4.23.6). Paths are `/api/v1/scheduling/...`
from `services/scheduling/api/openapi.yaml`; the BFF rewrites them to the service.

| Route | Page | Service calls |
|---|---|---|
| `/scheduling` | Gantt workspace: resource rows over a 1-14 day UTC window, the changeover at the start of a job hatched over its bar, drag or Move to preview (`dryRun`, with network, block and changeover violations) and Confirm (Idempotency-Key per preview), "Move pegged orders too" (`propagate`) with the pegged orders that move and their delay in the preview and a SCHED-0007 warning when the walk was cut, Firm / Unfirm, jobs table with changeover minutes for keyboard use | `GET /resources`, `GET /gantt`, `POST /jobs/{id}:move`, `POST /jobs/{id}:firm` |
| `/scheduling/load` | Capacity load by resource and week with bottleneck badges | `GET /capacity-load`, `GET /resources` |
| `/scheduling/runs`, `/runs/new`, `/runs/:id` | Run list, start a site or selection run (finite or infinite), run detail with warnings (SCHED-0010, SCHED-0012, SCHED-0014) and unplaced operations (including SCHED-0009 `NETWORK_INFEASIBLE`) | `GET/POST /runs`, `GET /runs/{id}` |
| `/scheduling/resources`, `/resources/new`, `/resources/:id` | Resource list, create with one shift on chosen weekdays, detail with weekly calendar and next 7 days of open capacity | `GET/POST /resources`, `GET /resources/{id}`, `GET /resources/{id}/capacity` |
| `/scheduling/operations`, `/operations/new` | Operation list with status filter, setup group, network and attributes; create with candidate resources in the order checked, setup group, route setup, network and attributes (one `name = value` per line) | `GET/POST /operations`, `GET /resources`, `GET /networks` |
| `/scheduling/order-pegs`, `/order-pegs/new` | Order pegs with an order filter and Remove; create by choosing the parent and dependent from the orders that have operations (loop refusal SCHED-0121 on the field) | `GET/POST /order-pegs`, `DELETE /order-pegs/{id}`, `GET /operations` |
| `/scheduling/campaigns`, `/campaigns/new`, `/campaigns/:id` | Campaign list with status filter; create (resource, key, planned start, rule, limits); detail with orders (add, remove), Sequence and Start (If-Match with the version, Idempotency-Key per command, reload on SCHED-0115), the plan (leading setup, changeovers, SCHED-0012 warnings, cleanouts) and the cost allocation | `GET/POST /campaigns`, `GET /campaigns/{id}`, `POST/DELETE /campaigns/{id}/orders`, `POST :sequence`, `:start`, `:allocate-cost` |
| `/scheduling/setup-matrices`, `/setup-matrices/new`, `/setup-matrices/:id` | Matrix versions by resource; create with one changeover per line (from, to, minutes, cost, cleaning set); detail | `GET/POST /setup-matrices`, `GET /setup-matrices/{id}` |
| `/scheduling/cleanout-rules`, `/cleanout-rules/new` | Cleanout rules with their triggers; create | `GET/POST /cleanout-rules` |
| `/scheduling/networks`, `/networks/new`, `/networks/:id` | Resource networks; create with a row per allowed transition; detail with transfer, maximum wait and transfer resource | `GET/POST /networks`, `GET /networks/{id}` |
| `/scheduling/capacity-blocks`, `/capacity-blocks/new` | Blocks by resource with window and condition; create weekly, by a recurrence rule (RRULE subset, first date, window; checked in the form and by the service) or dated, with one predicate per line | `GET/POST /capacity-blocks` |

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
