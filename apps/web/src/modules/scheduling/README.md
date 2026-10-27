# Scheduling web module (wave 23)

Screens for `services/scheduling` (PRD 4.23.6). Paths are `/api/v1/scheduling/...`
from `services/scheduling/api/openapi.yaml`; the BFF rewrites them to the service.

| Route | Page | Service calls |
|---|---|---|
| `/scheduling` | Gantt workspace: resource rows over a 1-14 day UTC window, drag or Move to preview (`dryRun`) and Confirm (Idempotency-Key per preview), Firm / Unfirm, jobs table for keyboard use | `GET /resources`, `GET /gantt`, `POST /jobs/{id}:move`, `POST /jobs/{id}:firm` |
| `/scheduling/load` | Capacity load by resource and week with bottleneck badges | `GET /capacity-load`, `GET /resources` |
| `/scheduling/runs`, `/runs/new`, `/runs/:id` | Run list, start a site or selection run (finite or infinite), run detail with unplaced operations | `GET/POST /runs`, `GET /runs/{id}` |
| `/scheduling/resources`, `/resources/new`, `/resources/:id` | Resource list, create with one shift on chosen weekdays, detail with weekly calendar and next 7 days of open capacity | `GET/POST /resources`, `GET /resources/{id}`, `GET /resources/{id}/capacity` |
| `/scheduling/operations`, `/operations/new` | Operation list with status filter, create with candidate resources in the order checked | `GET/POST /operations`, `GET /resources` |
| `/scheduling/campaigns`, `/campaigns/new`, `/campaigns/:id` | Campaign list with status filter; create (resource, key, planned start, rule, limits); detail with orders (add, remove), Sequence and Start (If-Match with the version, Idempotency-Key per command, reload on SCHED-0115), the plan (leading setup, changeovers, SCHED-0012 warnings, cleanouts) and the cost allocation | `GET/POST /campaigns`, `GET /campaigns/{id}`, `POST/DELETE /campaigns/{id}/orders`, `POST :sequence`, `:start`, `:allocate-cost` |
| `/scheduling/setup-matrices`, `/setup-matrices/new`, `/setup-matrices/:id` | Matrix versions by resource; create with one changeover per line (from, to, minutes, cost, cleaning set); detail | `GET/POST /setup-matrices`, `GET /setup-matrices/{id}` |
| `/scheduling/cleanout-rules`, `/cleanout-rules/new` | Cleanout rules with their triggers; create | `GET/POST /cleanout-rules` |
| `/scheduling/networks`, `/networks/new`, `/networks/:id` | Resource networks; create with a row per allowed transition; detail with transfer, maximum wait and transfer resource | `GET/POST /networks`, `GET /networks/{id}` |
| `/scheduling/capacity-blocks`, `/capacity-blocks/new` | Blocks by resource with window and condition; create weekly or dated with one predicate per line | `GET/POST /capacity-blocks` |

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
