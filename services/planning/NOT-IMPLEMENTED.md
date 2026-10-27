# NOT-IMPLEMENTED (PSG-22, planning service, slices 1 to 5)

Every ticket deliverable and acceptance criterion that slices 1 to 5 do not
build. Slice 2 added the freeze, action and futures fences (BR-22.4),
planned-order maintenance (FR-22.11) and forecast cells with reduction that a
run nets as demand (FR-22.8, AC-22.6). Slice 3 added five stateless calculators
(nothing is stored; each is a POST that returns its result, under
`planning.read`): DDMRP buffer evaluation, quota allocation, assemble-to-order
forecast consumption, the supply schedule with inventory projection, and
shelf-life FEFO allocation. They hold the rules; persistence of buffers, quota
arrangements, strategies and batches and their use inside a plan run are the
open part of deliverables 7 to 9 below. Slice 4 added firming with grouping and
the `plannedorder.firmed.v1` event (FR-22.10, BR-22.9, AC-22.8), Cancel and
Decrease messages beyond the coverage time fence (FR-22.7), and the version
purge (`PLANNING_MAX_VERSIONS`). Slice 5 added the site work calendar on the
run input (FR-22.5, BR-22.8), the stored net requirements view and
`GET /net-requirements` (FR-22.12, AC-22.10), and the `plan.started.v1` and
`message.raised.v1` events.
Status is `later slice of PSG-22` (planned work in this wave), `partly built`
(the stated part exists and is tested; the rest is named) or `handoff`
(needs another wave or owner; the handoff is recorded here because this wave may
not edit that code).

## Deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Freeze, action and futures time fences (BR-22.4) | built | `coverage.freezeFenceDays`, `actionFenceDays`, `futuresFenceDays` on the run input; tests in `domain/fences_test.go` (with a property test that frozen demand is pegged or reported short). |
| 1 | Firming time fence: planned orders inside it are auto-firmed (BR-22.4) | later slice of PSG-22 | Firming exists as a command; auto-firming inside a run needs the run to carry firmed orders of the previous version forward as supply (see deliverable 3). |
| 1 | Coverage groups as persisted setup carrying the fences (FR-22.2) | later slice of PSG-22 | Fences travel per item on the run input, like the rest of the coverage. |
| 1 | Coverage time fence (default 100 days) and `PLANNING_HORIZON_DAYS` | partly built | `coverage.coverageFenceDays` on the run input drives Cancel and Decrease (deliverable 3). Not built: requirements beyond the fence or the horizon are still netted, and the 100-day default is not applied (0 means off) because a default fence that raises Cancel without also limiting netting would flag supply the plan still counts on. |
| 1 | Safety-stock policy `first-issue` (BR-22.1 step 4, BR-22.5) | later slice of PSG-22 | Only policy `today` is built: safety stock is a demand at today, replenished at today + lead time. |
| 1 | Issue and reorder margins (FR-22.5) | later slice of PSG-22 | Only the receipt margin (`coverage.marginDays`) is applied. |
| 1 | Vendor-specific purchase lead time from trade agreements (FR-22.5) and default vendor on planned purchases (`PLAN-0008`) | handoff (PSG-17) | Vendors and trade agreements are not modelled; the item lead time applies. |
| 1 | Working calendars for lead-time offsets (BR-22.8) | partly built | `calendar` on the run (closed weekdays, up to 1,000 closed dates; one weekday must stay open): receipt margins, lead times, futures dates and delay propagation count working days, demand on a closed day moves forward and supply backward (`domain/calendar.go`, tests in `calendar_test.go` with a property that every planned order is ordered, delivered and expected on a working day). Not built: reading the site calendar from w6 (handoff PSG-6, the calendar travels on the run until then), country holiday packs (w48), and period, positive and negative days in working days (they stay calendar days). |
| 1 | Low-level-code recompute on BOM change (w9 event) | later slice of PSG-22 | LLCs are computed per run from the snapshot; no BOM event consumer yet. |
| 1 | Route-based production lead time (Σ operation times, w21) | handoff (PSG-21) | Route data comes from the production context; slice 1 accepts `leadTimeDays`. |
| 2 | Demand and supply loaded from source contexts: sales orders (w27), on-hand (w16), purchase orders (w17), production orders and transfers (w21) | handoff (PSG-27, PSG-16, PSG-17, PSG-21) | The run API takes its snapshot inline. Source contexts do not publish the events or read APIs the snapshot needs yet. |
| 2 | Forecast with reduction principles (FR-22.8, BR-22.3) | built | `forecast_model` and `forecast_line` with principles none, percent, transactions and transactions-dynamic; a run with `forecastModel` nets the reduced quantities as forecast demand. AC-22.6 is covered by the domain, store and API tests. |
| 2 | Sales quantity per forecast period taken from open sales orders | handoff (PSG-27) | Until sales lines reach the planning service, `salesQty` is entered on each forecast cell; the run does not re-reduce the forecast against the sales lines in its snapshot. |
| 2 | Forecast allocation keys (FR-22.8) and forecast UoM conversion (BR-22.10) | later slice of PSG-22 | Cells are per item, site and period in the inventory UoM. |
| 2 | Dependent demand explosion from BOM and route versions (BR-22.1 step 9) | later slice of PSG-22 | Components are accepted on the run input; BOM and route version effectivity is not resolved. |
| 3 | Firming into production, purchase and transfer orders (FR-22.10, BR-22.9) | partly built | `POST /planned-orders:firm` (1 to 1,000 ids, Idempotency-Key, all or nothing) marks each order `firmed` with the id and type of its target order, stores the batch for replay, and writes one `plannedorder.firmed.v1` per order (item, site, vendor, qty, dates, target id) to the outbox in the same transaction; 409 `PLAN-0002` on an order firmed before. Not built: the target orders themselves and the saga compensation of BR-22.9 (handoff PSG-17 purchase orders, PSG-21 production and transfer orders: they create the order with the given id from the event; planning does not yet consume a failure event to un-firm), production `:estimate`, and re-pointing pegging to the created order (a firmed planned order keeps the pegging of its version). |
| 3 | Planned-order maintenance: PATCH change qty/date/vendor, approve, delete (FR-22.11) | built | `PATCH`/`DELETE /planned-orders/{id}` under If-Match on open orders of the current version; delete is a soft delete. A changed quantity is not re-pegged, so after an edit the explain tree shows the pegging of the planned quantity. |
| 3 | Approved orders "become firmed on next run" (FR-22.11), and firmed orders as supply of the next run (FR-22.1) | later slice of PSG-22 | A run replaces the whole version from its inline snapshot and does not carry approved, edited or firmed orders of the previous version forward. Until the target orders reach the snapshot as receipts (source contexts, see deliverable 2), send them as `receipts` on the run. |
| 3 | Vendor check against w17 vendors on PATCH | handoff (PSG-17) | `vendorId` is checked as a UUID and only on purchase orders; whether the vendor exists is not checked. |
| 3 | Order grouping on firm (vendor, vendor-date, vendor-week) | built | `domain.GroupForFirming`: planned purchases of one site and vendor merge per grouping (weeks run Monday to Sunday); production and transfer orders never merge. AC-22.8 is `TestGroupForFirming_AC228` and the store test `TestFirmPlannedOrders`. The legal-entity default is the request default `vendor-date`; the parameter itself is not persisted (setup, see "Coverage groups API"). |
| 3 | Decrease, increase, cancel and derived action messages for existing supply (FR-22.7) | partly built | Built: delay (with propagation to parents and to the sales line or forecast), postpone, advance, shortage, and Cancel/Decrease for receipts at or beyond the coverage time fence whose quantity is not (fully) pegged, inside the action fence. Not built: Increase (a receipt is never raised; the shortfall becomes a new planned order) and Derived (a parent's message passed to its components). |
| 3 | Kanban planned-order type (FR-22.3) | later slice of PSG-22 | Order types purchase, production and transfer are planned; kanban needs wave 24 kanban rules. |
| 3 | Planned transfer demand at the from-warehouse (BR-22.7) | later slice of PSG-22 | Transfer planned orders are proposed, but no issue demand is created at a source warehouse; warehouse scopes are not modelled (deliverable 8). |
| 3 | Message catalog codes on messages (PLAN-0006, PLAN-0007, PLAN-0010 to PLAN-0015) | later slice of PSG-22 | Messages carry an action (`delay`, `postpone`, `advance`, `shortage`), not a catalog code; the user texts of 4.22.18 are not rendered yet. |
| 4 | Statistical forecast via ETS and Croston/TSB (FR-22.9) | partly built | `services/forecast-py` implements ETS A,N / A,A,N / A,A,A selected by AICc, Croston, TSB, ADI selection and holdout MAPE/wMAPE. Not built: the `statsforecast` library wrapper and `POST /forecasts/{modelId}:generate`, which would write statistical cells into a model. |
| 4 | Forecast batch generation for 50k items under 15 min (FR-22.9) | later slice of PSG-22 | Forecast endpoint exists in Python; the batch job, item batching, and Temporal workflow are not built. |
| 4 | Manual override per cell | built | `PUT /forecasts/{modelCode}/lines` with source `manual` (or `statistical` for a loader); the forecast screen in the web module edits cells. |
| 4 | Consensus workflow (Temporal approval), version lock and `forecast.published.v1` | later slice of PSG-22 | Source `consensus` is reserved for that workflow and refused on PUT. Forecast models carry a write version (ETag), not published forecast versions. |
| 4 | ForecastLine persistence and PUT lines API | built | Migration `0002`, RLS on both tables. |
| 5 | Explain-this-plan pegging tree | built | `GET /v1/planning/planned-orders/{id}/pegging` and the planned-order screen. A bom-line demand expands through the planned order that drives it. The tree is returned in one response (depth up to 20), not lazy-loaded per node as the UI rule asks. |
| 6 | Simulation plans and planning scenarios (FR-22.15, BR-22.14) | later slice of PSG-22 | Plan kind `simulation`, ScenarioOverride, rough-cut and purchase-volume evaluations and version diff are not built. |
| 6 | Supplier forecast publishing through w20 | handoff (PSG-20) | Depends on scenarios. |
| 7 | DDMRP buffers, buffer profiles, net-flow equation, priority-based planning (FR-22.16, BR-22.11) | partly built | `POST /ddmrp/buffers:evaluate` (`domain/ddmrp.go`): ADU from a past/future window, DAF, the BR-22.11 zones, qualified demand (past due, today, and days whose total exceeds the spike threshold inside the spike horizon), NFP, planned qty (TOG − NFP, at least MOQ, rounded up to an order multiple), priority NFP / TOG and the zone. Not built: BufferProfile and DdmrpBuffer persistence, DLT from the BOM (w9), nightly buffer recalculation, planned adjustment factors per period, decoupling points and `priorityBased` coverage inside the plan run, and the `PLAN-0010` red-zone message. |
| 8 | Quota arrangements (FR-22.17, BR-22.12) | partly built | `POST /quota-arrangements:allocate` (`domain/quota.go`): lowest rating wins (ties by priority, then id), split by water-filling to equal ratings with `minSplitQty`, quota-0 sources as fallback, 422 `PLAN-0009` when no source is usable. Not built: QuotaArrangement and QuotaSource persistence with validity periods (the overlap rule is in `ArrangementsOverlap`), maximum quantity per period, allocated qty from firmed and open orders, use in the plan run ahead of the item coverage vendor, and quota analysis. |
| 8 | MRP areas: warehouse and subcontractor planning scopes (FR-22.18) | later slice of PSG-22 | Planning scope is not modelled. Subcontractor scope needs w21 vendor operations and w16 vendor locations (handoff). |
| 8 | Planning strategies MTS net/gross, MTO, ATO, PWFA, subassembly (FR-22.19, BR-22.13) | partly built | `POST /strategies:consume-forecast` (`domain/strategy.go`) applies the BR-22.13 consumption windows (backward, forward, backward-then-forward) for `assemble-to-order-final`, AC-22.14 tested. Not built: strategy codes on item coverage, the other five strategies inside the plan run, marking (`is_marking`) for MTO and PWFA, the `PLAN-0015` message, and the T461P migration mapping. Order-specific stock segregation needs w16 (handoff). |
| 9 | Shelf-life aware pegging (FR-22.20, BR-22.15) | partly built | `POST /shelf-life:allocate` (`domain/shelflife.go`): FEFO over batches with expiry − demand day ≥ required shelf life, the shortfall, and batches expiring unpegged by a horizon day; AC-22.15 tested. Not built: shelf-life pegging inside the plan run, planned supply for the shortfall with expiry = delivery + shelf life, and the `PLAN-0011` message. Batch expiry (w16/w24) and sellable shelf-life days (FR-24.19) are handoffs. |
| 9 | Supply schedule workbench (FR-22.21) | partly built | `POST /supply-schedule:project` (`domain/projection.go`) buckets demand, receipts and planned supply by day, week or 30-day month with projected on-hand and a below-safety-stock flag; a PAB-identity property test; the calculator card in the web module. Not built: the grid over a stored plan version for a selection of items, inline planned-order creation, quantity edits and firming in a cell, calendar-month buckets, and the printable report. |
| 9 | Inventory forecast projection with value and days of supply (FR-22.22) | partly built | The same projection returns value (closing on-hand × unit cost as platform `money`, rounded to the currency's minor unit) and days of supply (on-hand over the next 30 days' average daily demand). Not built: the projection read from the current plan version or a scenario (the version's PAB per day is now stored in `net_requirement`, but the projection endpoint does not read it yet) and current cost from inventory costing (w20 handoff; the cost is entered). |

## Plan execution and operations (PRD 4.22.1, 4.22.10, 4.22.16)

| Item | Status | Reason |
|---|---|---|
| Dynamic net-change plan with 60 s debounce (FR-22.1, BR-22.6) | later slice of PSG-22 | Needs the event consumer and reach-set computation. |
| Plan run as a Temporal batch job; nightly 01:00 static plan | later slice of PSG-22 | Slice 1 runs synchronously in the request, which is not the target for 50k items. |
| Pointer swap on success, partitions per PlanVersion | partly built | Slice 1 runs in one transaction, marks the new version current and demotes the old one. Per-version partitions are not built. |
| Version purge beyond `PLANNING_MAX_VERSIONS` (20) | partly built | Each run deletes versions beyond the newest `PLANNING_MAX_VERSIONS` (env, 2 to 100, default 20) with their rows, in the run transaction; versions holding a firmed planned order are kept. Not built: the daily 03:00 job, partition drops (no partitions yet) and the per-plan parameter (one setting per deployment). |
| Firm planned orders API: `POST /planned-orders:firm` | built | Duty `planning.order.firm`. The PRD's segregation-of-duties rule (with *Approve purchase orders* above the w17 approval limit) is a handoff to PSG-17, which owns approval limits. |
| `:firm` on a simulation version refused with 409 `PLAN-0003` | later slice of PSG-22 | Simulation versions (deliverable 6) do not exist yet, so every version is operational. |
| Net requirements MD04 view `GET /net-requirements` (FR-22.12, AC-22.10) | partly built | Each run stores, per item and day with movement, gross requirements, scheduled receipts, planned receipts, PAB and the day's net requirement (`net_requirement`, RLS, purged with its version); `GET /net-requirements?planCode&itemId` reads the current version. AC-22.10 is `TestRun_AC2210NetRequirementsOfRM300`. Not built: Requirement (ReqTrans) rows per demand and supply line (the view is aggregated per day), `siteId` and version or scenario selection on the query (a plan is one site; the current version is read), the messages column, the what-if simulate action, and refreshing the view after a planned order is changed or deleted (it shows the run). |
| Plan scope filters: site, item group, planner, LLC range, `mps` scope (FR-22.13) | later slice of PSG-22 | A run plans the whole snapshot it is given. |
| Version-to-version diff API (FR-22.14) | later slice of PSG-22 | Each version stores its plan hash and input hash; the diff endpoint is not built. |
| UoM conversion of forecast lines and inventory UoM (BR-22.10) | later slice of PSG-22 | Quantities are taken as given (decimal, 6 places in the database). |
| Coverage groups API `GET /coverage-groups` and item coverage setup | later slice of PSG-22 | Setup entities are not persisted; coverage is passed on the run input. |
| Events `plan.started.v1` and `message.raised.v1` | partly built | Both are written to the outbox in the run's transaction: `plan.started.v1` (plan, version, kind `static`, site, as-of, items) ahead of the run's other events, and one `message.raised.v1` per stored message (message id, action, item, supply and demand ref, suggested date, delay days, qty). Because runs are synchronous, `plan.started` reaches consumers together with `plan.completed`; it will lead it once the run is a Temporal job. The PRD's `requirementId`/`refType`/`refId` are carried as `supply_ref` and `demand_ref`, since Requirement rows are not stored. |
| Event `forecast.published.v1` | later slice of PSG-22 | Needs the consensus workflow (deliverable 4). `plan.completed.v1` and `plannedorder.firmed.v1` are emitted (outbox, same transaction); the PRD spells the latter `planned-order.firmed.v1`, but the kernel's event type rule admits no hyphen in the entity segment. |
| Feature flags `planning.dynamic`, `planning.statistical-forecast`, `planning.mps-scope` | later slice of PSG-22 | Not read yet. |
| Plan run summary email and firming result report (PDF, email; 4.22.15) | later slice of PSG-22 | The firming response and the web module list the target orders of a batch; no document or email is rendered. |
| Sanctions screening block on firming (w48) | handoff (PSG-48) | Firming does not consult screening; PSG-48 owns the screening status of vendors. |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1: 40 textbook MRP scenarios (CPIM) in `testdata/` | later slice of PSG-22 | Slice 1 has the BR-22.2 three-level worked example and unit tests, not the 40-scenario fixture set. |
| AC2: 50k items x 12 periods x 5 levels in < 10 min, deterministic | later slice of PSG-22 | Determinism is covered by property tests. The 50k benchmark and dataset generator are not built. |
| AC3: pegging explains 100% of each planned order | built | Property test `TestRun_PropertyPeggingExplainsSupply`. |
| AC4: match frePPLe on a shared dataset | later slice of PSG-22 | Optional; needs the 50k dataset and a frePPLe export. |
| AC5: every deliverable has code or a reason here | built | This file. |
| AC6: `go vet`, `go test`, OpenAPI and AsyncAPI match, chart lints | built | Verified in this slice. `TestOpenAPI_MatchesRoutes` checks routes and duties, `TestAsyncAPIDeclaresEveryEvent` the published event types. |
| AC-22.3 identical hash across three runs of the 50k dataset | later slice of PSG-22 | Determinism of the hash is covered at unit scale. |
| AC-22.9 second run returns 409 `PLAN-0001` while one is running | built (synchronous form) | The store takes `FOR UPDATE NOWAIT` on the plan row; a run that cannot take it gets 409 `PLAN-0001` (`TestRunPlan_RefusesConcurrentRun`). With runs synchronous in slice 1, "running" means a run transaction in flight; the `running` version status arrives with the Temporal job. |
| AC-22.1 BR-22.2 worked example, including the 7-day delay message on the sales line | built | `TestRun_BR222WorkedExample`, `TestRun_BR222DelayPropagatesToSalesLine`. |
| AC-22.2 pegging explains every planned order | built | Property test over random snapshots (the 40-fixture set is AC1 above). |
| AC-22.4 min/max 100/400 with PAB 90 gives one order of 310 | built | `TestRun_MinMaxOrdersUpToMax`. |
| AC-22.5 early PO with positive days 5 gets Postpone to demand date − receipt margin | built | `TestRun_EarlyReceiptRaisesPostpone`. |
| AC-22.6 forecast 100, sales 30, `transactions` gives 70 | built | `TestReducePeriods_Principles`, `TestForecastLines` (store), `TestRun_NetsReducedForecastOfTheModel` (API). |
| AC-22.12 BR-22.11 buffer gives an order of 115, priority 0.5306, yellow | partly built | Calculator tests `TestEvaluateBuffer_NetFlowExample` and `TestBufferEvaluate_WorkedExample`; the plan run does not create the planned order yet. |
| AC-22.13 BR-22.12 quota picks vendor B, B's allocated qty 200 after firming | partly built | `TestAllocateWhole_LowestRatingWins` and `TestQuotaAllocate_WholeGoesToLowestRating` (B chosen, rating 5.00 after). Allocated qty after firming needs firming and persisted arrangements. |
| AC-22.14 assemble-to-order gross requirement 100, not 140 | partly built | `TestConsumeForecast_AC2214AssembleToOrder` and the API test; the plan run does not apply strategies yet. |
| AC-22.15 short shelf-life batch not pegged, planned order covers the demand | partly built | `TestAllocateBatches_AC2215ShortShelfLifeNotPegged` and `TestShelfLife_AC2215` (no allocation, shortfall 30); the planned order for the shortfall needs engine integration. |
| AC-22.8 three approved purchases of one vendor and date firm into one PO with three lines, each firmed with the target id | built | `TestGroupForFirming_AC228` (domain) and `TestFirmPlannedOrders` (store, database). The PO itself is created by PSG-17 from the events. |
| AC-22.10 net requirements of RM-300 show PAB 400, 190, 110 for weeks 2 to 4 | built | `TestRun_AC2210NetRequirementsOfRM300` (engine) and `TestRunPlan_NetRequirementsAndMessageEvents` (stored and read back, database). |
| AC-22.11, AC-22.16 | later slice of PSG-22 | Depend on scenarios and warehouse scopes above. |

## Verification

| Item | Status | Reason |
|---|---|---|
| Database-backed tests and `0001_init.sql` to `0004_net_requirements.sql` on an empty database | built | Run in review against Postgres 16 with an admin URL and a non-superuser app role (`PASSAGE_TEST_DATABASE_URL`, `PASSAGE_TEST_APP_DATABASE_URL`). CI must set both. |
| Pytest run of `services/forecast-py` | built | Run in review with Python 3.12; CI wiring for Python services belongs to the platform owner. |

## Reconciliation and test plan (PRD 4.22.9)

| Test | Status | Reason |
|---|---|---|
| PAB identity property per item and day | built | `TestRun_PropertyNetRequirementsReconcile` holds the recurrence per item and day of every run's net requirements view, and `TestProperty_ProjectionPABIdentity` per bucket of the supply-schedule projection. |
| Firming saga contract tests (compensation on injected failure) | handoff (PSG-17, PSG-21) | Planning's half is tested (one event per order in the outbox, all-or-nothing refusal). The consumers that create the target orders and report a failure are not built. |
| Forecast model unit tests (Python) | built | `services/forecast-py/tests`. |
| frePPLe comparison | later slice of PSG-22 | Optional. |
| Reconciliation Σ supply − Σ demand = closing PAB | built | `TestRun_PropertyNetRequirementsReconcile`: every demand, receipt and planned order of the item is in the view and the closing PAB is on-hand + Σ supply − Σ demand. |
| Projection and events reconciliation | handoff (testing-reconciliation wave) | Test strategy owner. |

## Handoffs to other waves

- PSG-16 inventory: on-hand by dimension, batch expiry (FR-22.20), warehouse stock segregation (FR-22.19).
- PSG-17 procurement: create the purchase order (lines = planned orders sharing `target_id`) from `plannedorder.firmed.v1`, report a failure for the saga compensation, approval limits on firming; trade-agreement lead times and vendor price.
- PSG-21 manufacturing: create the production or transfer order from `plannedorder.firmed.v1` (saga as above); route lead times; subcontractor operations.
- PSG-27 sales: sales-line demand events (ref type `sales-line`).
- PSG-9 product: BOM versions and low-level code change events.
- PSG-6 calendars: site working calendar (closed weekdays and dates) readable by site, so the run need not carry it.
- PSG-20 supplier portal and PSG-35 EDI: supplier forecast publishing.
- PSG-24 batch and shelf life: sellable shelf-life days (FR-24.19).
- PSG-20 inventory costing: current item cost for the inventory forecast value (FR-22.22).
- PSG-48 sanctions screening.
