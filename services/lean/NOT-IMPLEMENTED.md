# Not implemented in PSG-24 slice 1 (lean)

Built in slice 1: production flows with activities, connectivity check and activation
(FR-24.8, LEAN-0004); kanban rules (FR-24.9); kanban generation for fixed rules with the
capacity limit (BR-24.7) and for event rules from a demand line (FR-24.11, BR-24.8, AC-24.6);
the card state machine with optimistic versioning (BR-24.6, AC-24.7); the board by state
(FR-24.10) with WIP and overdue counts (FR-24.14); the fixed-count suggestion (BR-24.7);
Idempotency-Key on every command, outbox events, RLS; and the web screens in
`apps/web/src/modules/lean`.

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 3: flow versions (a new version of an active flow, expiry of the previous one) | later slice of PSG-24 | slice 1 creates draft flows and activates them; flows cannot be edited or versioned yet. `value_stream_id` refers to the wave-6 operating unit by id only |
| Deliverable 3: KanbanJob per activity, `/kanban-jobs/{id}:complete` with quantity, scrap and handling units, board per work cell (`?workCellId`) | later slice of PSG-24 | slice 1 moves the card itself; the board filters by site, flow or rule. Jobs and work-cell boards arrive with the activity sequence of rules (`flow_activity_sequence`) |
| Deliverable 3: GS1 DataMatrix card print | handoff | wave-5 document service and wave-32 print service |
| Deliverable 3: board push refresh over SSE, drag between columns | later slice of PSG-24 | the board polls every 30 s (the PRD fallback) and moves through buttons and a column picker; SSE needs a shell event-stream helper (wave 5) |
| Deliverable 3: event kanbans triggered by a wave-27 sales line or a wave-21 BOM line within 5 s, `LEAN_EVENT_KANBAN_SLA_S` | handoff | the generation itself is built (`POST /v1/lean/kanbans:generate` with the demand and its reference); the event consumer for sales lines (wave 27) and production BOM lines (wave 21) is the integration slice |
| Deliverable 3: scheduled kanbans through planning (`replenishment = scheduled`) | handoff | master planning (wave 22) creates them; `kanbans:generate` refuses scheduled rules with LEAN-0107 |
| Deliverable 3: rule fields `warehouse_id`, `location_id`, `min_kanbans`, `max_kanbans`, `auto_plan`, `auto_firm`, `flow_activity_sequence` (PRD 4.24.4) | later slice of PSG-24 | they drive jobs, automatic planning and firming, which are not built |
| Kanban manufacturing orders (FR-24.13) and withdrawal transfers on completion (FR-24.11, AC-24.6 "posts RAF of 60") | handoff | `dev.passage.lean.kanban.statechanged.v1` with `to = completed` carries rule type, item and quantity; the production order of type `kanban` is wave 21 (services/production) and the transfer posting is wave 16 (services/inventory) |
| Deliverable 4: periodic backflush costing (FR-24.12, AC-24.8), FlowCostingPeriod, `/flows/{id}/costing-periods/{p}:settle`, `flow-period.settled` event | later slice of PSG-24 | the variance rule and the open-card gate (BR-24.9, LEAN-0003) are built and tested in the domain; the period store, material issue at standard cost, conversion by cycle time and the ledger journal (contract test) are not |
| Deliverable 8: lean subcontracting (FR-24.20, BR-24.16, AC-24.17): service activity on a vendor work cell, purchase agreement pricing, auto PO and receipt, `lean-po-close` job, LEAN-0005 | later slice of PSG-24 | the `service` activity kind is stored and validated; PO creation needs the wave-17 purchasing service and agreements, and vendor work cells need the wave-9 vendor resource |
| Event name `passage.lean.kanban.state-changed.v1` (PRD 4.24.5) | deviation | the kernel rule `dev.passage.<context>.<entity>.<verb>.v<n>` allows no hyphen, so the type is `dev.passage.lean.kanban.statechanged.v1` (see events/asyncapi.yaml) |
| Fields on Kanban: `cw_qty`, `handling_units`, `planned_start`, `production_order_id` / `transfer_id`, `printed_at` | later slice of PSG-24 | they belong with jobs, catch weight and the order and transfer integrations above |
| Reports: kanban rule analysis, flow costing period (PRD 4.24.14) | later slice of PSG-24 | need jobs and costing periods |
| Board load SLO (p95 < 500 ms), kanban generation SLO (p95 < 5 s), load tests | later slice of PSG-24 | no load path yet |
| `/metrics`; feature flags `lean.kanban`, `lean.flow-costing` | later slice of PSG-24 | no counters yet (the chart's ServiceMonitor is off by default); flags need the deploy owner |
| Second demo plant (food and chemicals line) with a kanban-driven cell, `lean-db` CloudNativePG | handoff | deploy/ and demo seed belong to the deployment owner |
| Jobs: flow period auto-settle (monthly), fixed-kanban count suggestion (weekly Monday 05:00) | later slice of PSG-24 | need Temporal (wave 4); the suggestion itself is available on demand |
| Duty entries for `lean.read`, `lean.flow.write`, `lean.kanban-rule.write`, `lean.kanban.write`, `lean.kanban.work` in OPA data | handoff | policies/ is outside this wave's ownership; the service enforces duties from forwarded headers |
| Fixture files `lean-flow-demo` and `lean-kanban-sim` under testdata/ (PRD 4.24.9) | deviation | both fixtures are built in Go where they are used: `demoFlow` in internal/store/store_test.go and the AC-24.6 simulation in internal/domain/kanban_test.go and internal/store/store_test.go |
| Playwright board E2E (PRD 4.24.9) | later slice of PSG-24 | needs the end-to-end stack with RAF posting; the board is covered by vitest page tests |
