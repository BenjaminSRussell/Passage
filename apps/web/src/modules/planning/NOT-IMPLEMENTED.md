# NOT-IMPLEMENTED (PSG-22, planning web module, slices 1 to 9)

Screens of slices 1 to 9, all registered in `module.ts` and built from the
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
  (slice 4 adds cancel and decrease, slice 6 increase).
- `/planning/net-requirements` (slice 5): the MD04-style view of one item
  (`?plan=` and `?item=`) in the current version: date, gross requirements,
  scheduled and planned receipts, projected balance (negative shown as a
  danger badge) and net requirement, with the item's planned orders below; a
  row opens the planned order and its pegging tree. A planned-order page links
  to the net requirements of its item. Loading, empty, "choose an item" and
  error states.
- `/planning/runs`: plan run history (versions, counts, plan hash).
- `/planning/runs/new`: run-plan form that posts a snapshot with an
  Idempotency-Key (kept across retries of the same input) and, since slice 2,
  an optional forecast model whose reduced forecast the run nets. Since slice
  5 the form takes the site work calendar: closed weekdays (checkboxes, one
  must stay open) and closed dates; with none closed the run counts calendar
  days.
- `/planning/forecast` (slice 2): forecast cells of a model (`?model=`, default
  `BASE`) with forecast, open sales and reduced forecast, and a form that adds
  or changes a cell and the model's reduction principle under If-Match. Since
  slice 6 the model card shows the consensus status, and a Consensus card
  submits a draft for approval, then publishes it or returns it to draft
  (under If-Match), with the publication summary on success and the
  segregation-of-duties refusal (`PLAN-CONSENSUS-SOD`) explained. A submitted
  or published model shows its cells read-only with a "Cells are locked"
  notice instead of the cell form.
- `/planning/calculators` (slice 3): one card per stateless calculator of the
  service (DDMRP buffer, quota arrangement, forecast consumption, supply
  schedule and inventory forecast, shelf-life check), each a form prefilled
  with its PRD worked example and a result (details or table) with loading,
  empty and error states.
- `/planning/coverage-groups` (slice 7): coverage groups of the tenant
  (code, coverage, coverage fence, freeze fence, firming with an auto-firm
  badge, action and futures messages) with loading, empty and error states; a
  row opens `/planning/coverage-groups/:code`, which edits the settings under
  If-Match (reload offer on 412). `/planning/coverage-groups/new` creates one
  from the FR-22.2 defaults with an Idempotency-Key kept across retries of the
  same draft, checks the service's rules first (code pattern, day ranges,
  period days for period coverage, auto-firm needs a firming fence) and shows
  409 `PLAN-COVERAGE-EXISTS`. Tests in `coverage.test.tsx`.
- `/planning/item-coverages` (slice 9): item coverage of the tenant, filtered
  by item (`?item=`): item, site, scope (with warehouse or subcontractor),
  coverage group code, min, max ("No maximum" for 0), order type, strategy and
  decoupling-point / priority badges, with loading, empty and error states; a
  row opens `/planning/item-coverages/:id`, which edits it under If-Match
  (reload offer on 412). `/planning/item-coverages/new` creates one with an
  Idempotency-Key kept across retries of the same draft; the coverage group is
  chosen from the tenant's groups (with a link to create one when there is
  none), the warehouse field shows for a warehouse scope, and a subcontractor
  scope switches the order type to transfer. The form checks the service's
  rules first (UUIDs, decimal quantities with at most 6 decimals compared
  without floats, max 0 or at least min, day ranges) and names 409
  `PLAN-ITEM-COVERAGE-EXISTS`. Tests in `itemcoverage.test.tsx`.

Each list has loading, empty and error states; the plan in view is kept in the
`?plan=` query. Needs: none (uses only libraries already in `apps/web`).

| Screen or rule (PRD 4.22.6) | Status | Reason |
|---|---|---|
| Run plan from source data (no pasted snapshot) | later slice of PSG-22 | The run API takes its snapshot inline until sales, inventory and purchasing feed the planning service (see the service NOT-IMPLEMENTED.md). The form takes the snapshot as JSON. |
| Planned orders: change, approve, delete one order | built | Maintenance card on the planned-order page (`MaintainOrder.tsx`). |
| Planned orders: firm (grouped) | built | Firm card on `/planning/planned-orders` (`Firming.tsx`). |
| Planned orders: bulk approve, change and delete; keyboard range selection with `F` to firm | later slice of PSG-22 | Selection is by checkbox (or all open orders) and is used for firming only; maintenance stays one order at a time. |
| Firming result report (PDF, email) | later slice of PSG-22 | The service renders no document yet; the card lists the orders created. |
| Hide maintenance, firming, consensus, coverage-group and item-coverage controls from users without `planning.order.maintain`, `planning.order.firm`, `planning.forecast.maintain`, `planning.forecast.approve` or `planning.setup.maintain` | later slice of PSG-22 | The shell exposes no per-duty check to pages yet; the service refuses the write with 403 and the card shows the problem. |
| Net requirements (MD04) list of one item with drill to pegging | built | `/planning/net-requirements` (`NetRequirementsPage.tsx`); the drill goes through the item's planned orders. |
| Net requirements: what-if simulate, messages column, virtualized 365 x 20 grid, site or version choice | later slice of PSG-22 | The service has no simulate action, stores no messages per day, and reads the current version only; the list is a plain table of days with movement (at most 500). |
| Site work calendar read from the site (w6) instead of entered on the run form | handoff (PSG-6) | The service takes the calendar on the run until site calendars are readable. |
| Action messages: apply action to the source order, dismiss | later slice of PSG-22 | Action endpoints not built. |
| Forecast: list cells and edit one cell with source badge | built | `/planning/forecast` (`ForecastPage.tsx`); a row opens in the form. |
| Forecast grid with in-place cell editing (item x period columns) | later slice of PSG-22 | Cells are a row list with a form, not a pivoted grid. |
| Forecast: submit, publish and reject the consensus | built | Consensus card on `/planning/forecast` (`ForecastPage.tsx`, tests in `consensus.test.tsx`). Both buttons show for the model's status; the service decides who may press them (duty and segregation of duties). |
| Forecast: generate statistical, MAPE badge | later slice of PSG-22 | `POST /forecasts/{model}:generate` is not built in the planning service. |
| Coverage groups: list, create, edit | built | `CoverageGroupsPage.tsx` (slice 7). |
| Item coverage: list, create, edit | built | `ItemCoveragePage.tsx` (slice 9). |
| Item, site, warehouse and vendor pickers by name instead of ids | handoff (PSG-6, PSG-9, PSG-16, PSG-17) | Items, sites, warehouses and vendors are read from their owning modules, which this module may not call yet; the form takes UUIDs. |
| Choosing a coverage group on the run form | later slice of PSG-22 | The run does not resolve coverage groups or item coverage yet; the run form takes coverage (including `firmingFenceDays` and `autoFirm`) in the item JSON. |
| Mark auto-firmed planned orders apart from those a planner firmed | later slice of PSG-22 | Both show as firmed with the order they became; the service does not return the firming batch on a planned order. |
| Pegging tree lazy-loads children per node | later slice of PSG-22 | The API returns the tree to depth 20 in one response; it is rendered fully. |
| Supply schedule and inventory forecast on entered figures | built | Calculator card on `/planning/calculators` (`calculators.tsx`). |
| Supply schedule workbench over the stored plan (item selection, inline planned-order creation, quantity edits, firming, printable report) | later slice of PSG-22 | The service projects only the figures sent to it; requirements of a plan version are not readable per period yet. |
| DDMRP buffer, quota and shelf-life screens on stored setup (buffer list by priority, quota arrangement maintenance and analysis, expiring-supply messages) | later slice of PSG-22 | Buffers, quota arrangements and batches are not persisted by the service yet; the calculators work on entered figures. |
| Paging beyond 500 rows | later slice of PSG-22 | The list endpoints return at most 500 rows and no cursor yet; the workspace tiles say when they count only the first 500. |
| Duty-gated visibility of nav entries | built | Reads and the forecast page declare `planning.read`, the run form `planning.run`; the shell enforces them. |
