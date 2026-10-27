# Not implemented in PSG-24 slices 1, 2 and 4 (lean)

Built in slice 1: production flows with activities, connectivity check and activation
(FR-24.8, LEAN-0004); kanban rules (FR-24.9); kanban generation for fixed rules with the
capacity limit (BR-24.7) and for event rules from a demand line (FR-24.11, BR-24.8, AC-24.6);
the card state machine with optimistic versioning (BR-24.6, AC-24.7); the board by state
(FR-24.10) with WIP and overdue counts (FR-24.14); the fixed-count suggestion (BR-24.7);
Idempotency-Key on every command, outbox events, RLS; and the web screens in
`apps/web/src/modules/lean`.

Built in slice 2: takt vs cycle on the board of a flow (FR-24.14: takt time against the slowest
activity), and the lean subcontracting price rule served as `POST /v1/lean/subcontracting:preview`
(FR-24.20, BR-24.16, AC-24.17 pricing, LEAN-0005) with its screen.

Built in slice 4: kanban jobs (PRD 4.24.4 KanbanJob), one per activity of the rule's flow,
created with each card and following it to Not planned, Planned and Prepared; job start and
completion with reported quantity, scrap and handling units (`/kanban-jobs/{id}:start`,
`/kanban-jobs/{id}:complete`, FR-24.10) with optimistic versions and Idempotency-Key; the card's
In progress and Completed states driven by its first started and last completed job (BR-24.6),
with picking required before a job starts on a card that is not yet Prepared; AC-24.7 on jobs
(LEAN-0001); the work-cell board (`GET /board?workCellId`, columns by job status); the card's
`handling_units`; the `dev.passage.lean.kanbanjob.statechanged.v1` event and the job's report on
the card's completion event; and the work-cell board and kanban detail screens.

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 3: flow versions (a new version of an active flow, expiry of the previous one) | later slice of PSG-24 | slice 1 creates draft flows and activates them; flows cannot be edited or versioned yet. `value_stream_id` refers to the wave-6 operating unit by id only |
| Deliverable 3: KanbanJob `planned_start` and the job schedule (KanbanJobSchedule), the board's ordering by planned start | later slice of PSG-24 | jobs carry their card's due date; planned start comes with job scheduling (auto plan and firm, `flow_activity_sequence`) and the wave-23 scheduler |
| Deliverable 3: a card's jobs from a subset of the flow (`flow_activity_sequence` on the rule) | later slice of PSG-24 | every activity of the rule's flow gets a job, in sequence order; cards of a rule without a flow have no jobs and are moved by hand through every state |
| Deliverable 3: GS1 DataMatrix card print | handoff | wave-5 document service and wave-32 print service |
| Deliverable 3: board push refresh over SSE, drag between columns | later slice of PSG-24 | both boards poll every 30 s (the PRD fallback); cards move through buttons and a column picker, and jobs through Start and Complete buttons with a completion form; SSE needs a shell event-stream helper (wave 5) |
| Deliverable 3: event kanbans triggered by a wave-27 sales line or a wave-21 BOM line within 5 s, `LEAN_EVENT_KANBAN_SLA_S` | handoff | the generation itself is built (`POST /v1/lean/kanbans:generate` with the demand and its reference); the event consumer for sales lines (wave 27) and production BOM lines (wave 21) is the integration slice |
| Deliverable 3: scheduled kanbans through planning (`replenishment = scheduled`) | handoff | master planning (wave 22) creates them; `kanbans:generate` refuses scheduled rules with LEAN-0107 |
| Deliverable 3: rule fields `warehouse_id`, `location_id`, `min_kanbans`, `max_kanbans`, `auto_plan`, `auto_firm`, `flow_activity_sequence` (PRD 4.24.4) | later slice of PSG-24 | they drive jobs, automatic planning and firming, which are not built |
| Kanban manufacturing orders (FR-24.13) and withdrawal transfers on completion (FR-24.11, AC-24.6 "posts RAF of 60") | handoff | `dev.passage.lean.kanban.statechanged.v1` with `to = completed` carries rule type, item and quantity; the production order of type `kanban` is wave 21 (services/production) and the transfer posting is wave 16 (services/inventory) |
| Deliverable 4: periodic backflush costing (FR-24.12, AC-24.8), FlowCostingPeriod, `/flows/{id}/costing-periods/{p}:settle`, `flow-period.settled` event | later slice of PSG-24 | the variance rule and the open-card gate (BR-24.9, LEAN-0003) are built and tested in the domain; the period store, material issue at standard cost, conversion by cycle time and the ledger journal (contract test) are not |
| Deliverable 8: lean subcontracting (FR-24.20, BR-24.16, AC-24.17): vendor work cell, auto PO and receipt, `lean-po-close` job | partial | the `service` activity kind is stored and validated, and the PO lines for completed service jobs (one per job or per vendor, item and price; priced by the agreement valid on the completion date; LEAN-0005) are built as a preview. Creating and receiving the PO lines needs the wave-17 purchasing service and its purchase agreements, vendor work cells need the wave-9 vendor resource, service cost as conversion cost needs flow costing (deliverable 4), and the periodic PO close job needs Temporal (wave 4). Service activities now get kanban jobs, and their completions are published as `kanbanjob.statechanged` with `activityKind = service`; the preview still takes the completed jobs as input until the purchasing integration reads them |
| Event name `passage.lean.kanban.state-changed.v1` (PRD 4.24.5) | deviation | the kernel rule `dev.passage.<context>.<entity>.<verb>.v<n>` allows no hyphen, so the type is `dev.passage.lean.kanban.statechanged.v1` (see events/asyncapi.yaml) |
| Fields on Kanban: `cw_qty`, `planned_start`, `production_order_id` / `transfer_id`, `printed_at` | later slice of PSG-24 | they belong with catch weight, job scheduling, print and the order and transfer integrations above (`handling_units` is built in slice 4) |
| Reports: kanban rule analysis, flow costing period (PRD 4.24.14) | later slice of PSG-24 | need costing periods |
| Board load SLO (p95 < 500 ms), kanban generation SLO (p95 < 5 s), load tests | later slice of PSG-24 | no load path yet |
| `/metrics`; feature flags `lean.kanban`, `lean.flow-costing` | later slice of PSG-24 | no counters yet (the chart's ServiceMonitor is off by default); flags need the deploy owner |
| Second demo plant (food and chemicals line) with a kanban-driven cell, `lean-db` CloudNativePG | handoff | deploy/ and demo seed belong to the deployment owner |
| Jobs: flow period auto-settle (monthly), fixed-kanban count suggestion (weekly Monday 05:00) | later slice of PSG-24 | need Temporal (wave 4); the suggestion itself is available on demand |
| Duty entries for `lean.read`, `lean.flow.write`, `lean.kanban-rule.write`, `lean.kanban.write`, `lean.kanban.work` in OPA data | handoff | policies/ is outside this wave's ownership; the service enforces duties from forwarded headers |
| Fixture files `lean-flow-demo` and `lean-kanban-sim` under testdata/ (PRD 4.24.9) | deviation | both fixtures are built in Go where they are used: `demoFlow` in internal/store/store_test.go and the AC-24.6 simulation in internal/domain/kanban_test.go and internal/store/store_test.go |
| Playwright board E2E (PRD 4.24.9) | later slice of PSG-24 | needs the end-to-end stack with RAF posting; the board is covered by vitest page tests |
