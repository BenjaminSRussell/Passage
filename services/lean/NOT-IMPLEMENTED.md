# Not implemented in PSG-24 slices 1, 2, 4, 5, 6, 7, 8, 9 and 10 (lean)

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

Built in slice 5: flow costing periods (PRD 4.24.4 FlowCostingPeriod, FR-24.12): open a period
for a flow and date range (no overlapping periods, Idempotency-Key), list a flow's periods, and
`/flows/{id}/costing-periods/{p}:settle` with the period version and Idempotency-Key. Settlement
happens after the period's last day and only when every card with jobs in the period (a job
started on or before its last day) is Completed or Emptied, else 409 LEAN-0003 listing the cards
(BR-24.9); the variance is material + conversion − output in the period currency through
platform money (AC-24.8 variance); the `dev.passage.lean.flowperiod.settled.v1` event is written
in the same transaction; and the Flow costing screen (`/lean/costing`).

Built in slice 6: an append-only history of card completions (`kanban_cycle`, with the card's
planning time `kanban.planned_at` and due time) and job completions (`kanban_job_completion`),
written in the completing transaction, so a reused fixed card keeps its earlier cycles; the
kanban rule analysis `GET /kanban-rules/{id}/analysis?from&to` (PRD 4.24.14: cycles, average lead
time from planning to completion against the rule's lead time, late share, active cards against
the fixed rule's number of kanbans, open cards overdue now); the backflush preview
`POST /flows/{id}/costing-periods/{p}:backflush` (FR-24.12: per activity, the jobs completed in the
period × cycle time per unit × processed quantity × the hourly rate given, rounded once per
activity in the period currency); the CSV export `GET /flows/{id}/costing-periods:export` of the
flow costing period report; and the screens: the analysis on the kanban rule page, the backflush
preview on the flow costing page (its total can be carried into the settle form) and the CSV
download.

Built in slice 7: retry safety of the shop-floor commands. Card moves (`:move`, which also
empties a card) and job `:start` and `:complete` store their first answer under the
Idempotency-Key in `idempotency_keys` in the same transaction as the transition (no new table
or migration: the slices 1 and 4 commands already did); slice 7 adds the database tests that a
retried completion replays the first answer without a second completion record or event, that the
same key with another report is `idempotency_key_reuse` (422), that a refused command stores no
key, and that a retry arriving while the first start still holds the lock replays it (one start,
one event). Both web boards now keep a command's key until the service answers it, so a retry
after a lost answer is replayed instead of being refused as a stale version
(`apps/web/src/modules/lean/commandKeys.ts`).

Built in slice 8: the fixed-kanban count check of PRD 4.24.16, on demand:
`GET /kanban-rules/{id}/count-check?dailyDemand&safetyPct` computes the BR-24.7 suggestion from
the rule's lead time (hours ÷ 24) and fixed quantity and compares it with the rule's number of
kanbans; a deviation above 25 % is `reportable`. It writes nothing and never changes the rule;
event and scheduled rules are refused with LEAN-0113. The rule detail screen of a fixed rule runs
the check. The swarm draft of this slice also added a `kanban_completion` ledger (migration 0006)
and a separate `/lean/demand` page; both were dropped in review: the ledger duplicated
`kanban_job_completion`, which slice 6 already writes with every job completion and the backflush
already reads, and the demand page duplicated event-kanban generation on the rule detail screen
and called a `kanbans:generate-preview` route the service does not serve.

Slice 9 changed no lean behavior; it gave every message of events/asyncapi.yaml a summary so the
AsyncAPI documentation gate has no FAIL lines.

Built in slice 10: flow versions (Deliverable 3, FR-24.8). `POST /flows/{id}:new-version` (duty
`lean.flow.write`, flow version, Idempotency-Key) copies an active flow into a draft with the next
`flowVersion` and `previousFlowId`, optionally with a new takt time and a full replacement
activity list; only an active flow can be revised (LEAN-0114) and only once (LEAN-0115, a unique
index). Activating the draft expires the version it replaces the day before it starts, in the same
transaction, and writes `dev.passage.lean.flow.expired.v1`; `flow.activated` carries
`flowVersion` and `previousFlowId`. A version must start after the one it replaces (LEAN-0116).
Migration 0005 widens the name uniqueness to (value stream, name, flow version). The flow list and
detail screens show the version and the active-to date, and the detail screen of an active flow
creates the next version (takt time only; the activity editor for a new version is below). The
replaced version turns `expired` when the new one is activated, even when the new one starts later;
its `activeTo` (the day before, or an earlier end it already had) bounds its validity. The PRD lists
versioning under `POST /flows`; the slice uses a separate `:new-version` route so a create never
silently revises an existing flow.

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 3: editing the activities of a new flow version on screen | later slice of PSG-24 | the API accepts a full activity list on `:new-version`; the screen copies the activities and changes the takt time only. A draft cannot be edited after it is created. `value_stream_id` refers to the wave-6 operating unit by id only |
| Deliverable 3: moving kanban rules from an expired flow version to the new one | later slice of PSG-24 | rules keep pointing at the flow id they were made with; a rule of an expired version still generates cards. Repointing needs rule editing, which does not exist yet |
| Deliverable 3: KanbanJob `planned_start` and the job schedule (KanbanJobSchedule), the board's ordering by planned start | later slice of PSG-24 | jobs carry their card's due date; planned start comes with job scheduling (auto plan and firm, `flow_activity_sequence`) and the wave-23 scheduler |
| Deliverable 3: a card's jobs from a subset of the flow (`flow_activity_sequence` on the rule) | later slice of PSG-24 | every activity of the rule's flow gets a job, in sequence order; cards of a rule without a flow have no jobs and are moved by hand through every state |
| Deliverable 3: GS1 DataMatrix card print | handoff | wave-5 document service and wave-32 print service |
| Deliverable 3: board push refresh over SSE, drag between columns | later slice of PSG-24 | both boards poll every 30 s (the PRD fallback); cards move through buttons and a column picker, and jobs through Start and Complete buttons with a completion form; SSE needs a shell event-stream helper (wave 5) |
| Deliverable 3: event kanbans triggered by a wave-27 sales line or a wave-21 BOM line within 5 s, `LEAN_EVENT_KANBAN_SLA_S` | handoff | the generation itself is built (`POST /v1/lean/kanbans:generate` with the demand and its reference); the event consumer for sales lines (wave 27) and production BOM lines (wave 21) is the integration slice |
| Deliverable 3: scheduled kanbans through planning (`replenishment = scheduled`) | handoff | master planning (wave 22) creates them; `kanbans:generate` refuses scheduled rules with LEAN-0107 |
| Deliverable 3: rule fields `warehouse_id`, `location_id`, `min_kanbans`, `max_kanbans`, `auto_plan`, `auto_firm`, `flow_activity_sequence` (PRD 4.24.4) | later slice of PSG-24 | they drive jobs, automatic planning and firming, which are not built |
| Kanban manufacturing orders (FR-24.13) and withdrawal transfers on completion (FR-24.11, AC-24.6 "posts RAF of 60") | handoff | `dev.passage.lean.kanban.statechanged.v1` with `to = completed` carries rule type, item and quantity; the production order of type `kanban` is wave 21 (services/production) and the transfer posting is wave 16 (services/inventory) |
| Deliverable 4 (FR-24.12): period amounts derived by the service: material issued at standard or weighted-average cost on kanban completion, and the activity cost rates | partial | conversion is derived by the backflush preview from the completed jobs and cycle times, but the hourly rates come with the request, because activity cost rates belong to the wave-9 resources, which this service cannot read yet. Material cost and output value need wave-16 inventory costing (standard or weighted-average cost). The settlement still takes all three amounts from the caller (the screen can carry the backflushed conversion into the settle form). Service activities take no rate: their cost is the subcontracting purchase price (row below) |
| Deliverable 4 (FR-24.12, AC-24.8 "the journal entry balances"): variance posted to the flow's posting profile, `journal_entry_id` | handoff | the ledger (wave 13) posts from `dev.passage.lean.flowperiod.settled.v1`, which carries the amounts and the variance; `journalEntryId` stays null and the flow has no posting profile yet. The ledger contract test belongs with that integration |
| Event name `passage.lean.flow-period.settled.v1` (PRD 4.24.5) | deviation | the kernel rule allows no hyphen, so the type is `dev.passage.lean.flowperiod.settled.v1` |
| Deliverable 8: lean subcontracting (FR-24.20, BR-24.16, AC-24.17): vendor work cell, auto PO and receipt, `lean-po-close` job | partial | the `service` activity kind is stored and validated, and the PO lines for completed service jobs (one per job or per vendor, item and price; priced by the agreement valid on the completion date; LEAN-0005) are built as a preview. Creating and receiving the PO lines needs the wave-17 purchasing service and its purchase agreements, vendor work cells need the wave-9 vendor resource, service cost as conversion cost needs flow costing (deliverable 4), and the periodic PO close job needs Temporal (wave 4). Service activities now get kanban jobs, and their completions are published as `kanbanjob.statechanged` with `activityKind = service`; the preview still takes the completed jobs as input until the purchasing integration reads them |
| BR-24.6 "deleted for event rules" on Emptied | deviation | an emptied event-rule card is kept as a spent record (never reused, left off the board) so the audit trail and a settled period's card count stay intact |
| Event name `passage.lean.kanban.state-changed.v1` (PRD 4.24.5) | deviation | the kernel rule `dev.passage.<context>.<entity>.<verb>.v<n>` allows no hyphen, so the type is `dev.passage.lean.kanban.statechanged.v1` (see events/asyncapi.yaml) |
| Fields on Kanban: `cw_qty`, `planned_start`, `production_order_id` / `transfer_id`, `printed_at` | later slice of PSG-24 | they belong with catch weight, job scheduling, print and the order and transfer integrations above (`handling_units` is built in slice 4) |
| Reports: the flow costing period report's variance drill to cards and activities (PRD 4.24.14) | later slice of PSG-24 | the kanban rule analysis, the period list, its CSV export and the conversion by activity (backflush preview) are built; the variance drill to cards needs the derived material cost above |
| Kanban rule analysis: lead time of cycles completed before slice 6 | deviation | migration 0004 backfills the cycle and job history from the current card and job rows, without a planning time, so those cycles count but have no lead time (`timedCycles` excludes them). Earlier cycles of cards reused before the migration were cleared with their jobs and cannot be recovered. `maxKanbans` is the fixed rule's number of kanbans; `max_kanbans` for other rules is a later rule field (row above) |
| Board load SLO (p95 < 500 ms), kanban generation SLO (p95 < 5 s), load tests | later slice of PSG-24 | no load path yet |
| `/metrics`; feature flags `lean.kanban`, `lean.flow-costing` | later slice of PSG-24 | no counters yet (the chart's ServiceMonitor is off by default); flags need the deploy owner |
| Second demo plant (food and chemicals line) with a kanban-driven cell, `lean-db` CloudNativePG | handoff | deploy/ and demo seed belong to the deployment owner |
| Jobs: flow period auto-settle (monthly), fixed-kanban count suggestion (weekly Monday 05:00) with its report of deviations above 25 % | later slice of PSG-24 | need Temporal (wave 4); the check itself is built on demand for one rule (`/kanban-rules/{id}/count-check`, slice 8), with the daily demand supplied by the caller |
| Fixed-kanban count check: daily demand derived by the service (from sales or forecast consumption of the rule's item) | handoff | the demand history lives in sales (wave 27) and demand forecasting (wave 22); until they publish it, the caller enters the daily demand |
| Duty entries for `lean.read`, `lean.flow.write`, `lean.kanban-rule.write`, `lean.kanban.write`, `lean.kanban.work`, `lean.flow.settle` in OPA data | handoff | policies/ is outside this wave's ownership; the service enforces duties from forwarded headers |
| Fixture file `lean-flow-demo` under testdata/ (PRD 4.24.9) | deviation | built in Go where it is used: `demoFlow` in internal/store/store_test.go. `lean-kanban-sim` is now testdata/lean-kanban-sim.json (Wave 24 proof: one sales order, eleven lines over three event rules, written by the independent `gen_lean_kanban_sim.py`), run in the domain and through both work-cell boards in the database, with planned operator reports (scrap at Mix and Fill) checked against the completion events and the per-activity period work. The sales order is fixture data passed to `kanbans:generate`; no sales-line event drives it (the wave-27 consumer in the Deliverable 3 row above) |
| Playwright board E2E (PRD 4.24.9) | later slice of PSG-24 | needs the end-to-end stack with RAF posting; the board is covered by vitest page tests |
