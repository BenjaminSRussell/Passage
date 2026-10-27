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

The Gantt is SVG: bars are placed with geometry attributes and coloured by
order through CSS classes, so there are no inline styles. The SVG is a pointer
surface; the jobs table under it offers Move and Firm from the keyboard.

Not built here (see `services/scheduling/NOT-IMPLEMENTED.md`): canvas rendering
above 1,000 bars, zoom, Shift+drag of a whole order, the undo stack, drag across
rows to reassign (use the Resource field of the move form), and open-interval
shading.
