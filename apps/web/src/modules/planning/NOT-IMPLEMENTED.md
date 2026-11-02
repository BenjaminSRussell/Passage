# NOT-IMPLEMENTED (PSG-22, planning web module, slices 1 to 24)

Screens of slices 1 to 13 (slice 19 adds the quota arrangement link and source on the planned-order detail; slice 20 adds the DDMRP buffer priority and zone as a Priority column on the planned orders list and on the planned-order detail, and the red-zone message action; slice 21 adds a Sort by selector on the planned orders and messages screens, whose default puts the buffer orders of priority-based items first by planning priority and whose "Planning priority" option does so for every buffer order, and a Priority column on messages; slice 23 adds the sales-on-top (PLAN-0015) message action to the messages list and its Action filter; slice 24 adds the no-vendor (PLAN-0008) action and a Message column with the catalog code and text), all registered in `module.ts` and built from the
shell primitives (`src/shell/ui`) with CSS Modules:

- `/planning/strategies` and `/planning/strategies/:code` (slice 22): the six planning strategies of FR-22.19 with their stored
  setup or defaults, and a form for the consumption mode, backward and forward windows, marking and segregated stock (create on
  the first save with an Idempotency-Key, then update under If-Match, with a reload offer on 412 and on a 409 `PLAN-STRATEGY-EXISTS` create). Nav item "Planning
  strategies" on `planning.read`; the writes need `planning.setup.maintain`, which the service enforces.
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
  shows the order it became. Since slice 11 Shift with a click or Space on a
  box ticks (or unticks) every open order from the last box ticked, and `F`
  outside a text field firms the selection (`aria-keyshortcuts` on the button;
  tests in `keys.test.tsx`).
- `/planning/messages`: action and delay messages with an action filter
  (slice 4 adds cancel and decrease, slice 6 increase, slice 20 red zone).
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
- `/planning/buffer-profiles` (slice 10, `BufferPages.tsx`): DDMRP buffer
  profiles (code, item type, LTF, VF, order cycle, MOQ, spike threshold, ADU
  window) with loading, empty and error states; a row opens
  `/planning/buffer-profiles/:id`, which edits it under If-Match (reload offer
  on 412) and links to a recalculation with the profile chosen.
  `/planning/buffer-profiles/new` creates one from the 4.22.13 defaults with an
  Idempotency-Key kept across retries, checks the service's rules first
  (numeric(5,3) factors, spike 1 to 500%, ADU window of at least 7 days) and
  names 409 `PLAN-PROFILE-EXISTS`.
- `/planning/ddmrp-buffers` (slice 10): the DDMRP buffer status inquiry
  (4.22.14) filtered by site, zone and a priority ceiling in percent
  (`?site=&zone=&maxPct=`), most urgent first: item, site / warehouse, profile,
  net flow, tops of red, yellow and green, priority as a percent (decimal
  shifted without floats), zone badge, planned quantity and calculation time.
  A "Recalculate a buffer" card posts today's state (ADU, DLT, DAF, on-hand,
  open supply, qualified demand as day 0) with a stored profile, shows the
  zone, net flow, priority and order, and reloads the list.
- `/planning/quota-arrangements` (slice 10, `QuotaPages.tsx`): arrangements
  filtered by item, site and a valid-on date, with validity, sources and
  quotas, the source the next requirement goes to (lowest BR-22.12 rating,
  skipping blocked and quota-0 sources) and the split flag; a row opens
  `/planning/quota-arrangements/:id` with the sources' allocated quantity,
  rating ("Fallback" for quota 0) and status, and edits the arrangement and
  all its sources under If-Match. `/planning/quota-arrangements/new` creates
  one with any number of sources (vendor, own production or transfer, each
  with its reference, quota, allocated and base quantity, priority, optional
  maximum per period, blocked) under an Idempotency-Key, checks the rules
  first and names 409 `PLAN-QUOTA-OVERLAP`. Tests in `buffers.test.tsx` and
  `quota.test.tsx`.
- `/planning/scenarios` (slice 13, `ScenarioPages.tsx`): the planning scenarios
  of the plan in view (code, name, plan, demand factor, number of item
  changes, active badge) with loading, empty and error states; a row opens
  `/planning/scenarios/:code`, which edits the scenario under If-Match
  (reload offer on 412) and holds the "Run simulation" card: plan date, site
  and the snapshot as JSON, posted with an Idempotency-Key kept across
  retries; the result links to the simulation's planned orders
  (`/planning/planned-orders?version=N`, read only, no firming card) and to
  the version comparison, which compares a simulation with the current
  operational version by default and labels simulation and current versions
  in its pickers. `/planning/scenarios/new` creates one (code, plan,
  name, demand factor, item factors, added demand and lead-time overrides as
  JSON lines) for a plan that has run at least once, checks the service's
  rules first and names 409 `PLAN-SCENARIO-EXISTS` and `PLAN-SCENARIO-LIMIT`
  (404 `PLAN-NOT-FOUND` is shown as the service words it). The run history marks
  simulation versions with their scenario. Tests in `scenario.test.tsx` and `plandiff.test.tsx`.

Each list has loading, empty and error states; the plan in view is kept in the
`?plan=` query. Needs: none (uses only libraries already in `apps/web`).

| Screen or rule (PRD 4.22.6) | Status | Reason |
|---|---|---|
| Run plan from source data (no pasted snapshot) | later slice of PSG-22 | The run API takes its snapshot inline until sales, inventory and purchasing feed the planning service (see the service NOT-IMPLEMENTED.md). The form takes the snapshot as JSON. |
| Planned orders: change, approve, delete one order | built | Maintenance card on the planned-order page (`MaintainOrder.tsx`). |
| Planned orders: firm (grouped) | built | Firm card on `/planning/planned-orders` (`Firming.tsx`). |
| Planned orders: keyboard range selection with `F` to firm | built | Shift range selection and the `F` shortcut on `/planning/planned-orders` (`Firming.tsx`, `keys.test.tsx`). |
| Planned orders: bulk approve, change and delete | later slice of PSG-22 | The selection is used for firming only; maintenance stays one order at a time. |
| Firming result report (PDF, email) | later slice of PSG-22 | The service renders no document yet; the card lists the orders created. |
| Hide maintenance, firming, consensus, coverage-group, item-coverage, buffer-profile, recalculation and quota-arrangement controls from users without `planning.order.maintain`, `planning.order.firm`, `planning.forecast.maintain`, `planning.forecast.approve`, `planning.setup.maintain` or `planning.run` | later slice of PSG-22 | The shell exposes no per-duty check to pages yet; the service refuses the write with 403 and the card shows the problem. |
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
| Run items without coverage use the stored item coverage | built | Since slice 11 the service fills coverage, order type and lead time from the item's site coverage; the run form's hint says so and the service's 400 names an item left without either. |
| Choosing a coverage group on the run form | later slice of PSG-22 | The form takes the snapshot as JSON; a group is chosen through the item coverage, not per run. |
| Mark auto-firmed planned orders apart from those a planner firmed | later slice of PSG-22 | Both show as firmed with the order they became; the service does not return the firming batch on a planned order. |
| Pegging tree lazy-loads children per node | later slice of PSG-22 | The API returns the tree to depth 20 in one response; it is rendered fully. |
| Supply schedule and inventory forecast on entered figures | built | Calculator card on `/planning/calculators` (`calculators.tsx`). |
| Supply schedule over the stored plan (item selection, bucket, horizon, value) | built | `/planning/supply-schedule` (`SupplySchedulePage.tsx`, `supplyschedule.test.tsx`) reads `GET /plans/{planCode}/projection`; the selection lives in the URL. |
| Supply schedule workbench actions (inline planned-order creation, quantity edits, firming in a cell, printable report) | later slice of PSG-22 | The page is read-only; orders are changed and firmed on the planned-orders screens. |
| Buffer profiles: list, create, edit; DDMRP buffer status by site, zone and priority; recalculate one buffer | built | `BufferPages.tsx` (slice 10). |
| Quota arrangements: list, create, edit with sources and ratings | built | `QuotaPages.tsx` (slice 10). |
| Scenarios: list, create, edit, run a simulation, read its planned orders, mark simulations in the run history | built | `ScenarioPages.tsx` (slice 13). |
| Count firmed orders as supply on the run and simulation forms (FR-22.1, FR-22.10) | built | "Count firmed orders as supply" on `/planning/runs/new` (`RunPlanPage.tsx`) and the scenario run card (`ScenarioPages.tsx`) sends `carryForward`; off by default (tests in `pages.test.tsx`, `scenario.test.tsx`). |
| Scenario comparison report (planned qty and value deltas by item group, capacity load, purchase volume by vendor) | later slice of PSG-22 | The comparison is the existing planned-order version diff; value, item groups, capacity and vendor volume are not computed (service NOT-IMPLEMENTED.md, deliverable 6). |
| Scenario lines as a table instead of JSON; messages and net requirements of a simulation | later slice of PSG-22 | The three line lists are JSON text like the run snapshot; the service reads messages and net requirements of the current version only. |
| Buffer profile chosen by code on the item coverage form | later slice of PSG-22 | The item coverage form still takes the profile id; the service now checks it (422 `PLAN-PROFILE-NOT-FOUND`, shown by the form). |
| Planner worklist sorted by planning priority with the planned orders of red buffers; quota arrangement analysis (planned vs actual split, MEQ6/MEQ8); expiring-supply messages | later slice of PSG-22 | The run does not plan from buffers or arrangements and books no allocated quantity yet, and batches are not persisted (service NOT-IMPLEMENTED.md, deliverables 7 to 9). |
| Paging beyond 500 rows | later slice of PSG-22 | The list endpoints return at most 500 rows and no cursor yet; the workspace tiles say when they count only the first 500. |
| Duty-gated visibility of nav entries | built | Reads and the forecast page declare `planning.read`, the run form `planning.run`; the shell enforces them. |
