# NOT-IMPLEMENTED (PSG-22, planning web module, slice 1)

Slice 1 screens, all registered in `module.ts` and built from the shell
primitives (`src/shell/ui`) with CSS Modules:

- `/planning`: master planning workspace with plan picker, summary tiles
  (planned orders by type, delayed orders, messages) and the last run.
- `/planning/planned-orders`: planned orders of the current version with a
  status filter; a row opens `/planning/planned-orders/:id`, which shows the
  order and its explain-this-plan pegging tree.
- `/planning/messages`: action and delay messages with an action filter.
- `/planning/runs`: plan run history (versions, counts, plan hash).
- `/planning/runs/new`: run-plan form that posts a snapshot with an
  Idempotency-Key (kept across retries of the same input).

Each list has loading, empty and error states; the plan in view is kept in the
`?plan=` query. Needs: none (uses only libraries already in `apps/web`).

| Screen or rule (PRD 4.22.6) | Status | Reason |
|---|---|---|
| Run plan from source data (no pasted snapshot) | later slice of PSG-22 | The run API takes its snapshot inline until sales, inventory and purchasing feed the planning service (see the service NOT-IMPLEMENTED.md). The form takes the snapshot as JSON. |
| Planned orders: approve, firm (grouped), change, delete, bulk, keyboard range selection with `F` | later slice of PSG-22 | Maintenance and firming endpoints not built. |
| Net requirements (MD04) grid, what-if simulate, virtualized 365 x 20 grid | later slice of PSG-22 | Net-requirements API not built. |
| Action messages: apply action to the source order, dismiss | later slice of PSG-22 | Action endpoints not built. |
| Forecast grid: edit cells, generate statistical, submit consensus, MAPE badge | later slice of PSG-22 | Forecast API in the planning service not built (the forecast-py baseline exists). |
| Coverage groups and item coverage setup | later slice of PSG-22 | Setup API not built; coverage travels on the run snapshot. |
| Pegging tree lazy-loads children per node | later slice of PSG-22 | The API returns the tree to depth 20 in one response; it is rendered fully. |
| Supply schedule workbench and inventory forecast views | later slice of PSG-22 | FR-22.21 and FR-22.22 not built. |
| Paging beyond 500 rows | later slice of PSG-22 | The list endpoints return at most 500 rows and no cursor yet; the workspace tiles say when they count only the first 500. |
| Duty-gated visibility of nav entries | built | Reads declare `planning.read`, the run form `planning.run`; the shell enforces them. |
