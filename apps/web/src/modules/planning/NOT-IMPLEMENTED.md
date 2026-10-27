# NOT-IMPLEMENTED (PSG-22, planning web module, slices 1 to 4)

Screens of slices 1 to 4, all registered in `module.ts` and built from the
shell primitives (`src/shell/ui`) with CSS Modules:

- `/planning`: master planning workspace with plan picker, summary tiles
  (planned orders by type, delayed orders, messages) and the last run.
- `/planning/planned-orders`: planned orders of the current version with a
  status filter; a row opens `/planning/planned-orders/:id`, which shows the
  order, its maintenance card (slice 2: change quantity, dates and vendor,
  approve, delete after confirmation, all under If-Match with a reload offer
  on 412) and its explain-this-plan pegging tree. Since slice 4 the list has a
  firm card: tick open orders (or select all open), choose the purchase order
  grouping and firm them; the card shows the orders created (type, id, vendor,
  delivery, quantity), keeps the Idempotency-Key across a retry of the same
  selection, shows the 409 problem, and reloads the list. A firmed order's page
  shows the order it became.
- `/planning/messages`: action and delay messages with an action filter
  (slice 4 adds cancel and decrease).
- `/planning/runs`: plan run history (versions, counts, plan hash).
- `/planning/runs/new`: run-plan form that posts a snapshot with an
  Idempotency-Key (kept across retries of the same input) and, since slice 2,
  an optional forecast model whose reduced forecast the run nets.
- `/planning/forecast` (slice 2): forecast cells of a model (`?model=`, default
  `BASE`) with forecast, open sales and reduced forecast, and a form that adds
  or changes a cell and the model's reduction principle under If-Match.
- `/planning/calculators` (slice 3): one card per stateless calculator of the
  service (DDMRP buffer, quota arrangement, forecast consumption, supply
  schedule and inventory forecast, shelf-life check), each a form prefilled
  with its PRD worked example and a result (details or table) with loading,
  empty and error states.

Each list has loading, empty and error states; the plan in view is kept in the
`?plan=` query. Needs: none (uses only libraries already in `apps/web`).

| Screen or rule (PRD 4.22.6) | Status | Reason |
|---|---|---|
| Run plan from source data (no pasted snapshot) | later slice of PSG-22 | The run API takes its snapshot inline until sales, inventory and purchasing feed the planning service (see the service NOT-IMPLEMENTED.md). The form takes the snapshot as JSON. |
| Planned orders: change, approve, delete one order | built | Maintenance card on the planned-order page (`MaintainOrder.tsx`). |
| Planned orders: firm (grouped) | built | Firm card on `/planning/planned-orders` (`Firming.tsx`). |
| Planned orders: bulk approve, change and delete; keyboard range selection with `F` to firm | later slice of PSG-22 | Selection is by checkbox (or all open orders) and is used for firming only; maintenance stays one order at a time. |
| Firming result report (PDF, email) | later slice of PSG-22 | The service renders no document yet; the card lists the orders created. |
| Hide maintenance and firming controls from users without `planning.order.maintain` or `planning.order.firm` | later slice of PSG-22 | The shell exposes no per-duty check to pages yet; the service refuses the write with 403 and the card shows the problem. |
| Net requirements (MD04) grid, what-if simulate, virtualized 365 x 20 grid | later slice of PSG-22 | Net-requirements API not built. |
| Action messages: apply action to the source order, dismiss | later slice of PSG-22 | Action endpoints not built. |
| Forecast: list cells and edit one cell with source badge | built | `/planning/forecast` (`ForecastPage.tsx`); a row opens in the form. |
| Forecast grid with in-place cell editing (item x period columns) | later slice of PSG-22 | Cells are a row list with a form, not a pivoted grid. |
| Forecast: generate statistical, submit consensus, MAPE badge | later slice of PSG-22 | `POST /forecasts/{model}:generate` and the consensus workflow are not built in the planning service. |
| Coverage groups and item coverage setup | later slice of PSG-22 | Setup API not built; coverage travels on the run snapshot. |
| Pegging tree lazy-loads children per node | later slice of PSG-22 | The API returns the tree to depth 20 in one response; it is rendered fully. |
| Supply schedule and inventory forecast on entered figures | built | Calculator card on `/planning/calculators` (`calculators.tsx`). |
| Supply schedule workbench over the stored plan (item selection, inline planned-order creation, quantity edits, firming, printable report) | later slice of PSG-22 | The service projects only the figures sent to it; requirements of a plan version are not readable per period yet. |
| DDMRP buffer, quota and shelf-life screens on stored setup (buffer list by priority, quota arrangement maintenance and analysis, expiring-supply messages) | later slice of PSG-22 | Buffers, quota arrangements and batches are not persisted by the service yet; the calculators work on entered figures. |
| Paging beyond 500 rows | later slice of PSG-22 | The list endpoints return at most 500 rows and no cursor yet; the workspace tiles say when they count only the first 500. |
| Duty-gated visibility of nav entries | built | Reads and the forecast page declare `planning.read`, the run form `planning.run`; the shell enforces them. |
