# NOT-IMPLEMENTED (PSG-22, planning service, slices 1 to 30)

Every ticket deliverable and acceptance criterion that slices 1 to 18 do not
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
`message.raised.v1` events. Slice 6 added Increase messages for shortages
inside the freeze fence (FR-22.7), the forecast consensus workflow (submit,
publish, reject) with the segregation of duties of 4.22.17 and
`forecast.published.v1` (FR-22.9), the reach set of a dynamic net-change run
(BR-22.6, domain only), and the forecast history window in `forecast-py`.
Slice 7 added coverage groups as persisted setup (FR-22.2), auto-firming inside
the firming time fence during a run (BR-22.4), and resumable batch generation
in `forecast-py` (FR-22.9). Slice 8 (net requirements and coverage-group
screens, vendor change in the planned-order editor) found all three already
built and reviewed in slices 2, 5 and 7 (`apps/web/src/modules/planning`); it
only brings the OpenAPI description up to date. Nothing new is unbuilt.
Slice 9 added item coverage as persisted setup (data model ItemCoverage with
the FR-22.16..FR-22.20 fields: planning scope per site, warehouse or
subcontractor, planning strategy, decoupling point, buffer profile, required
shelf life, priority-based) with list, get, create and update and the
item-coverage screens. The plan run does not read item coverage yet; that is
the open part of deliverables 1, 7, 8 and 9 below.
Slice 10 added DDMRP buffer profiles as persisted setup, stored buffers with a
recalculation that upserts per item, site and warehouse and the buffer status
inquiry (FR-22.16), and quota arrangements with their sources and the
non-overlapping validity rule (FR-22.17), each with list, get, create and
update and their screens. A run uses neither yet; that is the open part of
deliverables 7 and 8 below.
Slice 11 makes the plan run read stored setup: an item sent without a coverage
code, order type or lead time takes them from its site-scope item coverage at
the run's site and that coverage's group (FR-22.2, FR-22.5; inline values win,
an item left without coverage or order type is a 400 naming the field). It
also reads and validates the feature flags at startup (`PLANNING_FEATURE_FLAGS`,
4.22.10), and the web grid gains Shift range selection and `F` to firm.
Slice 12 adds the version-to-version diff (FR-22.14): `GET /plans/{planCode}/diff?from&to`
compares the planned orders of two versions (added, removed, changed, quantity
delta per order type, message delta, hash equality) with a "Compare plan
versions" screen, and brings the AsyncAPI spec to zero FAIL lines on the doc gate.
Slice 13 adds simulation plans and planning scenarios (FR-22.15, BR-22.14): a persisted
scenario (demand factor overall and per item, added sales demand, lead-time overrides,
at most 10 active per plan) with list, get, create and update (duty `planning.simulate`),
`POST /scenarios/{code}:run`, which applies it to the snapshot a plan run takes and stores
a new version of kind `simulation` that is never current, publishes only
`scenario.simulated.v1`, and whose planned orders cannot be firmed or changed (409
`PLAN-0003`), the `version` filter on `GET /planned-orders`, and the scenario screens and
simulation marks in the run history of the web module.
Slice 14 adds `carryForward` on the run input (also used by scenario runs): the plan's
firmed planned orders at the site that are not yet delivered (delivery date on or after
the plan date, from every operational version, never a simulation) are added as
scheduled receipts of their items (`FirmedSupply` in the store), so a firmed order is
supply of the next run and is not planned a second time (FR-22.1, FR-22.10); the run
screen has a "Count firmed orders as supply" option.
Slice 15 adds the safety-stock policy `first-issue` (FR-22.2, BR-22.5): `safetyStockPolicy`
(`today` default, `first-issue`) is stored on the coverage group (migration `0015`), is sent on
the run input's coverage block (a bare item takes its stored group's policy), and moves the safety
stock demand, and so its planned order and its net requirements row, from today to the item's first
demand date (today when the item has no demand); the coverage-group screens show and edit it.
Slice 16 firms approved planned orders on the next run (FR-22.11): before a run demotes the
plan's current version, every order of it with status `approved` is grouped (default grouping
`vendor-date`), stored in one firming batch (`plan-run-approved:<version id>`), marked `firmed`
with its target order id, and written as one `plannedorder.firmed.v1` each, in the run
transaction (migration `0016` adds `plan_version.approved_firmed_count`, shown as
`approvedFirmed` on the version, the run result, the last-run card and the run history table).
With `carryForward` the approved orders of the current version are read as supply of the run that
firms them, so their demand is not planned a second time. A simulation run does not firm approved
orders, but with `carryForward` counts them as supply like the operational run would.
Slice 17 adds the FR-22.5 issue and reorder margins beside the receipt margin: item coverage carries
`safetyMarginIssueDays` and `safetyMarginReorderDays` (migration `0017`, 0 to 365 working days), a run
reads them from the stored coverage or from `coverage.issueMarginDays` / `reorderMarginDays` on the input.
The issue margin moves each demand of the item earlier before netting, counted in working days from the day
the demand is netted on without it and never before today, so a delay is measured against the earlier date;
safety stock is not an issue, but under `first-issue` it is due at the first issue as moved. A component's
issue margin is a cushion before its parent starts, so a component that misses it delays the parent by the
shortfall. The reorder margin lengthens the lead time of its planned orders (order date earlier, also for the
today-plus-lead futures date and delay propagation to parents). On the run input `marginDays`,
`issueMarginDays` and `reorderMarginDays` are 0 to 365 like the stored margins. The item-coverage screens edit
both.
Slice 18 adds the planning horizon and the plan scope filter (FR-22.13): a run ignores demand beyond
`horizonDays` (default `PLANNING_HORIZON_DAYS`, 365) and reports only the items inside its `scope`
(item ids, item groups, planners, low-level code range), with the run page fields and the OpenAPI
schemas `PlanScope` and the new run properties (the OpenAPI and AsyncAPI specs have zero FAIL lines
on the doc gates).
Slice 19 puts quota arrangements into the plan run (FR-22.17, BR-22.12, AC-22.13): after the run, each planned
order of an item whose arrangement is valid on the order's delivery day goes to a source (lowest rating first, orders
in delivery order so ratings move during the run; a source takes only orders of its type; a per-period maximum
counts in periods from the arrangement's validity start and includes the firmed and approved orders already
allocated to the source in that period; a split share above a source's remainder is cut to it and the rest divided
over the other sources; a split arrangement turns the order into one order per source, numbered `<number>-<n>`,
with its pegging and, for a production order, the bom-line demand it drives divided over the parts). The plan hash
then covers the source of each allocated order. The order stores `quota_arrangement_id`, `quota_source_id` and, for
a vendor source, `vendor_id` (migration `0018`); firming, approved-order firming and the firming fence book the
quantity on the source (`quota_source.allocated_qty`, bumping the arrangement's version so a stale update is a 412)
and `plannedorder.firmed.v1` names arrangement and source. An arrangement update keeps the id of a source that names
the same type and reference, and a planner's vendor change moves an open order to that vendor's source of the
arrangement (or out of it). The planned-order screen links the arrangement. Simulation runs do not apply quota.

Slice 20 puts DDMRP buffers into the plan run (FR-22.16, BR-22.11, AC-22.12): an item sent in the run whose site-scope
item coverage is a `decouplingPoint` and that has a stored site-scope buffer (`ddmrp_buffer`, of the plan's legal entity)
is replenished by the buffer's planned quantity from its last recalculation instead of being netted. The quantity is one
demand `ddmrp:<zone>:<item>` (ref type `buffer`) due after the decoupled lead time, and no earlier than the item's lead
time, so the planned order carries the buffer quantity (115 in the AC-22.12 example), the item's calendar, margins, lot
rules and BOM explosion. The item's own demand, receipts, on-hand, safety stock and min/max are not used (the net-flow
position already counts them), and the item is removed from the components of the other items: a decoupling point
absorbs the demand above it, while its own components still explode from its replenishment order. A green buffer plans
nothing. The buffer's planned orders store its planning priority and zone (`planned_order.planning_priority`,
`buffer_zone`, migration `0019`; `planningPriority` and `bufferZone` on the API and a Priority column on the planned
orders screen), so AC-22.12's order of 115 reads priority 0.5306, yellow. A buffer in the red zone raises the `red-zone`
action message (PLAN-0010) on each of its orders, after any quota split so the message names the part (migration `0019`
admits the action; OpenAPI and AsyncAPI enums, the action filter and the messages screen know it). Simulation runs read
the same buffers and apply them after the scenario's overrides.

Slice 21 sorts the worklist by planning priority (FR-22.16): `GET /planned-orders?sort=priority` and
`GET /messages?sort=priority` list the orders of buffers (and the messages about them) by planning priority, lowest
(most urgent) first, followed by everything else in the default order; any other `sort` is a 400. Without `sort` the
item's `priorityBased` flag decides: the buffer orders of an item whose site-scope item coverage at the version's site,
in the plan's legal entity, is `priorityBased` (and the messages about them) lead by priority, and every other order
keeps number order (messages: action, supply, demand). `decouplingPoint` alone makes an item buffer-managed;
`priorityBased` only changes the ordering. A message carries `planningPriority` and `bufferZone` of the planned order it
names (a join on tenant, plan version and order number, which is unique in a version; no migration). The planned orders
and messages screens have a "Sort by" selector and the messages screen a Priority column.

Slice 22 puts planning strategies into the plan run (FR-22.19, BR-22.13, AC-22.14): `planning_strategy` (migration
`0020`, RLS, tenant-level setup like coverage groups) stores per strategy code the consumption mode, backward and forward
windows (defaults backward, 30 days, 0), `usesMarking` and `segregatedStock`, with `GET`/`POST /strategies` and
`GET`/`PUT /strategies/{code}` (Idempotency-Key, If-Match, `planning.setup.maintain` to write; the list always shows the six
strategies, with defaults and `configured: false` for those without setup). A run that nets a `forecastModel` reads the strategy
on each item's site-scope item coverage (an item with no stored coverage is `mts-net`, as before) and plans its forecast by
it (`domain.PlanForecast`): `mts-net` the model's reduced forecast, `mts-gross` the forecast as entered (sales orders do not
reduce it), `mto` no forecast (supply follows the sales lines), and `assemble-to-order-final` the forecast as entered less what
the item's sales lines of the run consume inside the window (a sale on day d takes from [d - backward, d] latest first, then
(d, d + forward] earliest first; unconsumed sales stay in the sales demand on top), so forecast 100 with a sale of 40 plans
60 + 40 = 100, not 140. A simulation run reads the same strategies. The planning strategies screens list the six and edit
one (the first save of an unconfigured strategy stores it).
Review of slice 22: strategy setup stays tenant-level with no `legal_entity_id` (the parameter table scopes the windows per strategy, and coverage groups and buffer profiles, the other shared planning setup, are tenant-level too; the item coverage that names an item's strategy is entity-scoped and the run reads it under the plan's entity), and a setup write emits no event, like the other planning setup writes (no AsyncAPI change). The plan hash is over the run's result, so a run after a strategy change that alters the plan has a new hash (`TestRun_AssembleToOrderConsumesForecast`); a retry under the same Idempotency-Key still replays, as it does after an item coverage change. Window edges are inclusive on both sides (`TestPlanForecast_WindowEdgesAreInclusive`) and only the item's own sales lines of the run consume its forecast (`TestRun_AssembleToOrderConsumesOnlyTheItemsOwnSales`; a run is one site, so sales of other sites or entities are never in it). The setup form offers a reload when another user stored the strategy first (409 `PLAN-STRATEGY-EXISTS`).
Slice 23 finishes the strategy part of the run that slice 22 left open (FR-22.19, BR-22.13, PLAN-0015). `mts-gross` is now
planned gross: its forecast demand is marked `Gross` and the engine does not net it against the item's on-hand (it is still
netted against expected receipts and planned supply), so the forecast is covered by supply whatever the stock is while sales
and other demand still take the on-hand; this affects `requirement` and `period` coverage (a `minmax` item's stock level
counts on-hand either way). A simulation applies its scenario before the forecast is planned and scales the model's cells
by the item's factor, so under `assemble-to-order-final` a scenario's `addedDemand` consumes the scaled forecast like a
(scaled) real sale (forecast 100 and an added sale of 40 plan 60 + 40); the added lines themselves are never scaled. A sales line that finds less forecast
than its quantity raises the information message `sales-on-top` (PLAN-0015, migration `0021` admits the action; `demandRef`
is the sales line, `qty` the excess, `suggestedDate` its date; the plan hash covers it; OpenAPI, AsyncAPI, the messages
screen and its action filter know it). The run's `coverage` block takes an optional `strategy` (FR-22.19): it wins over the
strategy of the stored site-scope coverage and lets an item with no stored coverage carry one, with the windows of that
strategy's stored setup or its defaults (an unknown code is a 400). Tests: `TestPlanForecastNotes_*`,
`TestRun_GrossDemandIsNotNettedAgainstOnHand`, `TestRun_SalesOnTopRaisesTheInformationMessage`,
`TestRun_GrossForecastIgnoresOnHand`, `TestRun_AssembleToOrderRaisesSalesOnTop`, `TestRun_InlineStrategyOnCoverage`,
`TestScenarioRun_AddedSalesConsumeAssembleToOrderForecast`, store `TestRunPlan_StoresSalesOnTopMessage`.
Review of slice 23: the builder consumed the forecast with the added lines before the factor scaled it, so with factor 2 an
added sale of 40 took 80 of forecast (2 × 60 + 40) and a PLAN-0015 excess of an added line was doubled; the scenario is now
applied first and the cells scaled before consumption (`TestScenarioRun_FactorScalesForecastBeforeAddedSalesConsumeIt`:
factor 2 plans 200, an added 230 reports 110). The net requirements view (FR-22.12) now nets a gross demand like the
engine does, so the on-hand left over by sales is counted once in PAB and does not hide the gross forecast's net
requirement (`TestRun_GrossDemandIsNotNettedAgainstOnHand`). Under `mts-gross` the forecast is also not reduced by sales
(slice 22: the forecast as entered), so sales take the on-hand and are planned on top of the whole forecast. PLAN-0015 is
one message per sales line, as the catalog text names the line (`Sales demand {ref} exceeds consumable forecast by
{qty}`); a run raises it only for an `assemble-to-order-final` item that has forecast cells in the run's model (an item
with none is not forecast-planned). A strategy sent on the coverage block is part of the run's request fingerprint
(`input_hash`) and its effect is in the plan hash, but, like every other inline coverage field, the run stores no copy of
its input snapshot, so the strategy a past run used cannot be read back (not built).

Slice 24 gives planned purchases their vendor and renders the message catalog (FR-22.3, FR-22.5, PLAN-0006 to PLAN-0015).
A run item takes `vendorId` (UUID) and, left out, the default vendor of its stored site-scope item coverage
(`RunCoverage.VendorID`; inline wins). After quota allocation (a quota source's vendor stays) `domain.ApplyDefaultVendors`
gives every planned purchase order without a vendor the item's default vendor, in operational runs and simulations alike
(simulations apply no quota, so there a quota item's purchase takes the default vendor too); production and transfer orders take none. An item whose purchases are left with no vendor raises one PLAN-0008 warning
(`no-vendor`, migration `0022` admits the action; `supplyRef` is its earliest vendorless order, `suggestedDate` that order's
date, `qty` the item's vendorless purchase quantity: the catalog text names `{item}`, not an order, and one line per order would
flood a 50k-item run; the order is still planned, "using none"). The plan hash covers each vendor given and each message added. `GET /messages` returns the catalog
`code` and rendered `text` of the actions that have a catalog line (`domain.CatalogCode`, `CatalogText`: postpone PLAN-0006,
delay PLAN-0007, no-vendor PLAN-0008, red-zone PLAN-0010 with the priority as a percentage, sales-on-top PLAN-0015; the item is
named by its id because the service has no item master; the text is rendered in English when read, not stored, so a later
translation needs no data change) and the messages screen shows them. `message.raised.v1` gains the `no-vendor` action, an
additive v1 change flagged `x-passage-added-in`; the event carries no catalog code or text. Tests: `TestApplyDefaultVendors_*`,
`TestCatalogTexts`, `TestRun_DefaultVendorFromStoredCoverage`, `TestRun_PurchaseWithoutVendorRaisesPlan0008`,
`TestRun_QuotaVendorWinsOverDefaultVendor`, `TestRun_RejectsMalformedVendor`, `TestScenarioRun_DefaultVendorAndPlan0008`, store `TestRunCoverages` (vendor),
`TestRunPlan_StoresNoVendorMessageWithCatalogText`. Not built: the vendor from trade agreements and its lead time (PSG-17), a
vendor existence check (UUID only), and the vendor on a planned order of a run that sends no item coverage and no `vendorId`
(PLAN-0008 says so). Existing runs behave as before except that a purchase with no vendor now carries the warning.

Slice 25 reads the supply schedule and inventory forecast from the stored plan (FR-22.21, FR-22.22):
`GET /plans/{planCode}/projection?itemId=...` (duty `planning.read`, up to 20 items, bucket day, ISO week (Monday to Sunday) or calendar month, horizon 1 to 730 days, optional `unitCost` with `currency`) buckets the
`net_requirement` rows of the plan's current version (one site, returned as `siteId`) from its as-of date through
`domain.ProjectPlanned`. The opening on-hand is read back from the item's first stored day (always read, even past the
window, so an item whose first movement is past the horizon keeps its on-hand), so each bucket's closing balance is the
run's own PAB; demand is the gross requirements (which include the safety stock the run holds back, so a shortage is a
balance below zero), supply the scheduled receipts, planned the planned receipts. Week buckets are ISO weeks
(`domain.CalendarWeekEnds`, like SAP MD04 and AX: the first runs from the as-of date to the Sunday of its week) and month
buckets follow the calendar
(`domain.CalendarMonthEnds`: the first runs from the as-of date to the end of its month; the calculator, which has no
dates, keeps 7-day weeks and 30-day months). Value and days of supply come from the same `Project` as the calculator. A plan outside the
caller's legal entities is 404 by row-level security. The web module gets a Supply schedule page
(`/planning/supply-schedule`). Tests: `TestProjectPlanned_*`, `TestCalendarMonthEnds`, `TestPlanProjection_*`, store
`TestReadProjectionSource` and the legal-entity case in `TestLegalEntityScope_PlansForecastsAndQuotas`,
`supplyschedule.test.tsx`. No migration or event. An item the run saw with on-hand but no movement has no stored rows,
so it reads as not planned with a zero opening (the version does not store on-hand per item).

Slice 26 changes the week bucket of that projection to ISO weeks (`domain.CalendarWeekEnds`, tested in `TestCalendarWeekEnds`, including ISO week 53 across the year end, and `TestPlanProjection_WeekBucketsAreISOWeeks`) and
brings the statistical forecast service forward (damped-trend ETS and automatic season-length detection, see
`services/forecast-py/NOT-IMPLEMENTED.md`). Slice 28 makes the planning service call it.

Slice 27 adds the warehouse planning scope (FR-22.18, BR-22.7, AC-22.16). A run item may carry `warehouses[]`: each is the
item planned in the scope of one warehouse, with its own on-hand, safety stock, demand and receipts (inline, or from the item's stored
warehouse-scope item coverage; inline wins). The engine nets the scope as an item of its own (`domain.ExpandWarehouses`, keyed
`item@warehouse`, split back by `domain.SplitWarehouses`), so warehouse stock never covers site demand. A scope whose order type is
`transfer` lists the site item as its only component, so every planned transfer creates demand for the item at the site on its order
date, the delivery date less the transfer lead time counted on the run calendar (BR-22.7, BR-22.8); `purchase` replenishes the
warehouse directly, and `production` does too and explodes the item's BOM, its components demanded in their site scope. The site
item's `onHand`, demand and receipts are the site's without its warehouse scopes (the caller splits stock by warehouse; a site
`onHand` that includes a scope's stock counts it twice). Planned orders, pegs, action messages and net requirement rows carry a
nullable `warehouse_id` (migration `0023`, catalog-only ALTERs; `0024` builds the net requirement key with the scope in it,
`(tenant, version, item, warehouse, day) NULLS NOT DISTINCT`, and the planned order index; `0025` drops the 0004 key; every step
under `lock_timeout` 5s, the index build under SHARE only; no new table, so no new grant). Reads: `warehouseId` on planned orders,
messages, net requirements and version-diff orders (orders match across versions per scope), and the `warehouseId` filter of
`GET /planned-orders` and `GET /net-requirements` (site scope without it); the supply schedule grid reads the site scope only.
`plannedorder.firmed.v1` and `message.raised.v1` gain a nullable `warehouse_id` (additive within v1), so the owning context creates
the transfer to the right warehouse. Carry-forward counts a firmed warehouse order as supply of its warehouse only, and a firmed
transfer whose order date has not passed is still an issue from the site on that date (a past one is taken as shipped). The web
module shows a Scope column on planned orders, messages and the version diff, and the net requirements page reads one warehouse
scope (`?warehouse=`). Tests: `domain/warehouse_test.go` (`TestWarehouseScopeTransfer_AC2216`,
`TestWarehouseProductionExplodesTheBOM` and others), api `warehouse_test.go` (with `TestRun_WarehouseScopeLeavesQuotaToTheSite`
and `TestRun_CarryForwardFirmedTransferIssuesFromTheSite`), store `warehouse_test.go`, `netreq.test.tsx`, `plandiff.test.tsx`.
Not covered by the scope yet: see the rows for deliverable 8.

Slice 29 adds `legal_entity_id` (the plan version's legal entity, nullable) to `plannedorder.firmed.v1` on all three
paths that write it (planner firming, approved orders firmed by a run, the firming fence), additive within v1. The
payload's top-level `legal_entity_id` also sets the CloudEvents `legalentityid` extension (platform `events.New`), so
production's planned-orders inbox (wave 21 slice 25) and other entity-scoped consumers can filter by it; the AsyncAPI schema
documents it. Tests: `TestFirmPlannedOrders` (data and extension), `TestRunPlan_FirmsApprovedOrders`. It also lets a run plan
the warehouse scopes of stored item coverage (FR-22.18): `warehouseScopesFromCoverage` on the run input (opt-in, default
false, also on scenario runs and the Run plan page) adds to each item the warehouse scopes it has as warehouse-scope item
coverage at the site and does not name in `warehouses[]` (inline wins, no duplicate; at most 50 per item; added in warehouse
id order). A discovered scope takes coverage, order type, lead time, default vendor and min/max from the stored coverage
and is planned as an empty scope: no on-hand, safety stock, demand or receipts of its own beyond the plan's carried-forward
firmed orders. A requirement or period scope therefore plans nothing, and a min/max scope is replenished to its maximum on
every run (a run replaces the version's open orders, so they do not pile up; a firmed one is the scope's receipt with
`carryForward` until it is delivered, after which the scope is empty again unless its stock is sent inline); a transfer's issue
demand lands on the site, which nets it once against the site's on-hand. Planning does not split the site's stock by warehouse (PSG-16 handoff), so the caller names a scope that holds
stock or demand inline and leaves that stock out of the site's `onHand`; otherwise the warehouse's stock is netted at the site
and a min/max scope plans a transfer of stock it may already hold. Coverage code and order type are NOT NULL on stored coverage,
so discovery adds no new 400; going past 50 scopes per item (or 100,000 per run) is a 400, never a truncation. Review: the
per-run scope limit after discovery, and the precise contract above. No migration, table, duty or route. Tests: `TestRun_WarehouseScopeDiscoveredFromStoredCoverage`,
`TestRun_WarehouseScopeFromCoverageIsOptIn`, `TestRun_WarehouseScopeInlineIsNotDuplicatedByDiscovery`,
`TestRun_WarehouseScopeDiscoveredWithoutDemandPlansNothing`, `TestRun_WarehouseScopesFromCoverageOverTheLimitIs400`, store
`TestRunWarehouseCoverages`, `TestRunPlan_FirmingFenceOfAPlanWithoutLegalEntity` (JSON null, no extension), `pages.test.tsx`.

Status is `later slice of PSG-22` (planned work in this wave), `partly built`
(the stated part exists and is tested; the rest is named) or `handoff`
(needs another wave or owner; the handoff is recorded here because this wave may
not edit that code).

## Deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Freeze, action and futures time fences (BR-22.4) | built | `coverage.freezeFenceDays`, `actionFenceDays`, `futuresFenceDays` on the run input; tests in `domain/fences_test.go` (with a property test that frozen demand is pegged or reported short). |
| 1 | Firming time fence: planned orders inside it are auto-firmed (BR-22.4) | partly built | `coverage.firmingFenceDays` and `coverage.autoFirm` on the run input: orders whose order date falls inside the fence are stored `firmed` in one firming batch per run (grouping `vendor-date`) with one `plannedorder.firmed.v1` each, in the run transaction (`domain/firming_fence_test.go`, `store/coverage_test.go` `TestRunPlan_AutoFirmsInsideTheFirmingFence`). Not built: carrying firmed orders of the previous version forward as supply (deliverable 3), so a rerun before the target orders reach the snapshot as receipts plans and firms the same demand again; the caller sends them as `receipts` until then. The legal-entity firming grouping parameter is not persisted (the default applies). |
| 1 | Coverage groups as persisted setup carrying the fences (FR-22.2) | partly built | `coverage_group` (migration `0006`, RLS) with list, get, create (FR-22.2 defaults, optional Idempotency-Key, 409 `PLAN-COVERAGE-EXISTS`) and update under If-Match, duty `planning.setup.maintain`; the coverage-group screens in the web module. Since slice 11 a run resolves the coverage group of an item that has a site-scope item coverage and is sent without a coverage code (see item coverage below). Not built: the legal-entity default coverage group `REQ` (4.22.13) for items with no item coverage (they must still carry coverage on the run input), `rescheduling_margin_days`, `priorityBased` on the group (it is on item coverage, see below), deleting a group, and the run honouring `generateActions`/`generateFutures` (the run input has no such switches; the fences decide). |
| 1 | Item coverage setup (data model ItemCoverage, 4.22.4; FR-22.16..FR-22.20 fields) | partly built | `item_coverage` (migration `0007`, RLS) with `GET`/`POST /item-coverages` (filter by item, optional Idempotency-Key) and `GET`/`PUT /item-coverages/{id}` under If-Match, duty `planning.setup.maintain`; min/max as numeric decimal strings, receipt margin, lead-time override, default vendor, planned order type, strategy, scope, decoupling point, buffer profile id, required shelf life, priority-based. One coverage per item, site and scope (per warehouse, per subcontractor vendor): 409 `PLAN-ITEM-COVERAGE-EXISTS`; an unknown coverage group is 422 `PLAN-COVERAGE-GROUP-UNKNOWN`; a subcontractor scope must plan provision transfers. Screens in the web module. Tests: `domain/itemcoverage_test.go`, `api/itemcoverage_test.go`, `store/itemcoverage_test.go` (database). Since slice 11 the run reads the site-scope item coverage at its site (`store/run_coverage.go`, `api/run_coverage.go`): an item without a coverage code takes the whole coverage block (group code and fences, period, positive and negative days, auto-firm, with the item coverage's min, max and receipt margin), an item without `orderType` the planned order type, and an item without `leadTimeDays` the lead-time override; inline values always win, including an explicit lead time of 0, and the Idempotency-Key fingerprint is the request as sent, so a retry replays after the stored coverage changed (`api/run_coverage_test.go`, `store/run_coverage_test.go` on the database with RLS). Not built: the planning strategy and required shelf life fields (deliverables 8 and 9; since slice 20 the run replenishes a decoupling point from its stored buffer, and since slice 21 priority-based orders the planned-order and message lists, see deliverable 7), warehouse and subcontractor scopes (never read at site level, FR-22.18 below), recording which stored coverage a version used (the plan hash and input hash cover the result and the request only), checking item, site, warehouse and vendor ids against their owning contexts (w6, w9, w16, w17: handoff, ids are checked as UUIDs only), and deleting a coverage. Since slice 10 `bufferProfileId` refers to a stored profile (422 `PLAN-PROFILE-NOT-FOUND`; the constraint is `NOT VALID`, so slice 9 rows naming no stored profile are kept until edited); a decoupling point without a profile is still accepted. |
| 1 | Planning horizon `PLANNING_HORIZON_DAYS` (4.22.13) | partly built | Slice 18: demand dated beyond the horizon is ignored by a run (`domain.RunWithinHorizon`; supply is kept, and supply dated beyond the horizon raises no Cancel or Decrease since the demand it serves there is not seen). The service default is `PLANNING_HORIZON_DAYS` (Helm `horizonDays`, 30 to 1,095, default 365) and a run may set `horizonDays` itself (0 takes the default, 30 to 1,095 otherwise); scenario simulations take the same through the API. Tests `domain/scope_test.go` (`TestRunWithinHorizon_NoCancelForSupplyBeyondTheHorizon`), `api/scope_test.go`. Dependent demand generated from a parent order dated inside the horizon is not clipped. Not built: the PRD makes the horizon a per-plan parameter, and plans have no stored settings, so it is per service and per run; the version does not record which horizon (or scope) it was run with; the scenario run screen does not offer the horizon or scope fields (the API takes them). |
| 1 | Coverage time fence (default 100 days) | partly built | `coverage.coverageFenceDays` on the run input drives Cancel and Decrease (deliverable 3). Not built: requirements beyond the fence are still netted, and the 100-day default is not applied (0 means off) because a default fence that raises Cancel without also limiting netting would flag supply the plan still counts on. Since slice 11 an item whose coverage comes from a stored group carries that group's fence (stored with the 100-day default unless the planner changed it), so Cancel and Decrease apply there as configured; netting is still not limited by it. |
| 1 | Safety-stock policy `first-issue` (BR-22.1 step 4, BR-22.5) | built | Slice 15: `coverage_group.safety_stock_policy` (migration `0015`, check constraint) and `coverage.safetyStockPolicy` on the run (an unknown value is 400 on the API and `ErrInvalidItem` in the engine). The first issue date is the earliest demand of the item after the explosion, so a component's first dependent (BOM) demand counts; past-due demand counts at today. With no demand the safety stock stays at today so the buffer is still built. Tests: `domain/safety_policy_test.go` (orders, net requirements view, dependent demand), `api/safety_policy_test.go`, `store/coverage_test.go` and `store/run_coverage_test.go` (database). Not built: AX's third option `today + procurement time` (the PRD names only `today` and `first-issue`). |
| 1 | Issue and reorder margins (FR-22.5) | built | Slice 17: `item_coverage.safety_margin_issue_days` and `safety_margin_reorder_days` with the receipt margin; tests `domain/margins_test.go` (calendar, first-issue safety stock, component delay), `TestRun_StoredMarginsShiftTheOrder` and `TestRun_RejectsInvalidLines` (API), `TestItemCoverages` and `TestRunCoverages` (store, database). Margins are per item coverage; coverage groups do not carry them. |
| 1 | Vendor-specific purchase lead time from trade agreements (FR-22.5) | handoff (PSG-17) | Trade agreements are not modelled; the item lead time applies. |
| 1 | Default vendor on planned purchases and `PLAN-0008` (FR-22.3, FR-22.5) | built | Slice 24: the item coverage's default vendor (or the run item's `vendorId`) is stamped on purchase orders after quota allocation, and a purchase left without one raises the `no-vendor` warning (see the slice 24 note). The vendor is checked as a UUID only (w17 handoff). |
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
| 3 | Approved orders "become firmed on next run" (FR-22.11), and firmed orders as supply of the next run (FR-22.1) | partly built | Firmed orders as supply: built opt-in (slice 14, `carryForward`; `TestRun_CarryForwardCountsFirmedOrdersAsSupply`, `TestFirmedSupply`). It is off by default because a caller that already sends the target orders as `receipts` would count them twice; an order whose delivery date has passed is taken as received (part of on-hand). Built in slice 16: approved orders become firmed (batch and event, grouped `vendor-date`) by the next operational run and, with `carryForward`, are supply of that same run (`store/approved_firm.go`, `TestRunPlan_FirmsApprovedOrders`). Gaps: the legal-entity grouping parameter is still not persisted (default `vendor-date`); a run without `carryForward` plans the demand of an order it just firmed again; an approved order deleted or edited while a run is computing was counted as supply at its snapshot quantity but is then not firmed (deleted) or firmed at its new quantity (edited). Not built: carrying edited or unprocessed orders of the previous version forward; matching a carried order to the target order once it reaches the snapshot (handoff PSG-17, PSG-21: the target order must name the planned order id), and knowing whether a target order was delivered (an order whose delivery date has passed is dropped rather than read from the target order). The firmed orders are read before the run's transaction, so an order firmed while a run is computing is first carried by the following run. |
| 3 | Vendor check against w17 vendors on PATCH | handoff (PSG-17) | `vendorId` is checked as a UUID and only on purchase orders; whether the vendor exists is not checked. |
| 3 | Order grouping on firm (vendor, vendor-date, vendor-week) | built | `domain.GroupForFirming`: planned purchases of one site and vendor merge per grouping (weeks run Monday to Sunday); production and transfer orders never merge. AC-22.8 is `TestGroupForFirming_AC228` and the store test `TestFirmPlannedOrders`. The legal-entity default is the request default `vendor-date`; the parameter itself is not persisted (setup, see "Coverage groups API"). |
| 3 | Decrease, increase, cancel and derived action messages for existing supply (FR-22.7) | partly built | Built: delay (with propagation to parents and to the sales line or forecast), postpone, advance, shortage, Cancel/Decrease for receipts at or beyond the coverage time fence whose quantity is not (fully) pegged, inside the action fence, and (slice 6) Increase: a shortage inside the freeze fence, where no planned order may be created, raises Increase on the latest existing receipt dated on or before it inside the action fence (`domain/increase_test.go`, with a property that increases never exceed the reported shortages; stored and filtered in `TestRunPlan_StoresIncrease`). Outside the freeze fence a shortfall still becomes a new planned order rather than an Increase of existing supply, and a receipt after the shortage gets no combined Advance-and-Increase. Not built: Derived (a parent's message passed to its components). Slice 30 adds the planner's decision on a message (see the slice 30 handoffs): apply or dismiss with `message.applied.v1` or `message.dismissed.v1`; applying changes no order inside planning because the existing supply belongs to purchasing and production. |
| 3 | Kanban planned-order type (FR-22.3) | later slice of PSG-22 | Order types purchase, production and transfer are planned; kanban needs wave 24 kanban rules. |
| 3 | Planned transfer demand at the from-warehouse (BR-22.7) | built | A transfer order of a warehouse scope creates demand for the item at the site on its order date (delivery date less the transfer lead time), pegged as a `bom-line` and delaying the transfer when the site is late (`domain.ExpandWarehouses`; `TestWarehouseScopeTransfer_AC2216`, `TestWarehouseSiteDemandAddsToTransfer`). The from-warehouse is the site's inventory; a different from-warehouse per transfer needs w16 warehouse topology (handoff PSG-16). |
| 3 | Message catalog codes on messages (PLAN-0006, PLAN-0007, PLAN-0008, PLAN-0010 to PLAN-0015) | partly built | Slice 24: `GET /messages` carries `code` and `text` for postpone (PLAN-0006), delay (PLAN-0007), no-vendor (PLAN-0008), red-zone (PLAN-0010) and sales-on-top (PLAN-0015). Not built: the catalog lines of the actions with none in the PRD (advance, shortage, cancel, decrease, increase), PLAN-0004 BOM cycle, PLAN-0005 lot-size splits, PLAN-0009 as a stored message, PLAN-0011, PLAN-0013 and PLAN-0014 (their causes are not raised yet), item names in the text and translation. Earlier note: messages carry an action (`delay`, `postpone`, `advance`, `shortage`, `cancel`, `decrease`, `increase`, since slice 20 `red-zone` for PLAN-0010 and since slice 23 `sales-on-top` for PLAN-0015), not a catalog code; the user texts of 4.22.18 are not rendered yet. |
| 4 | Statistical forecast via ETS and Croston/TSB (FR-22.9) | partly built | `services/forecast-py` implements ETS A,N / A,A,N / A,A,A (and damped) selected by AICc, Croston, TSB, ADI selection, holdout MAPE/wMAPE, the `historyMonths` window and `autoSeason`. Slice 28 adds `POST /forecasts/{modelCode}:generate` (duty `planning.forecast.maintain`, If-Match, `internal/api/forecast_generate.go`, `internal/forecastclient`): the caller sends monthly histories (1 to 1,000 series of an item and site, up to 600 values, `model`, `seasonLength` or `autoSeason`), planning sends them with `historyMonths` (default 36, 12 to 120) to forecast-py and writes one statistical cell per calendar month from `firstPeriodStart` (1 to 24 months) in one transaction (`store.GenerateStatisticalCells`): a cell replaces the stored cell of the same item, site and month only when that cell is itself `statistical` (a manual or consensus cell stays and is counted in `cellsKept`), the reduced quantities of the touched series are recomputed, the model version rises, a submitted or published model is 409 `PLAN-FORECAST-LOCKED`, and `forecast.generated.v1` is written to the outbox. The route needs the `planning.statistical-forecast` flag and `FORECAST_URL` (chart value `forecastUrl`): off is 409 `PLAN-FEATURE-OFF`, a forecast service that does not answer is 502 `PLAN-FORECAST-UNAVAILABLE`; the response reports per series the model used, season length, MAPE and wMAPE. The forecast page has a Statistical forecast card. Not built: the `statsforecast` library wrapper; reading the sales history here (planning does not own it, the caller or the job sends it, handoff PSG-27/PSG-29); weekly cells (the grain is the calendar month); the tenant parameter "forecast history months" itself (the request value or its default 36 is used). |
| 4 | Forecast batch generation for 50k items under 15 min (FR-22.9) | partly built | `forecast-py` `POST /v1/forecast/forecasts:generate-batches` cuts up to 50,000 series into batches of up to 1,000, skips batches the caller holds and reports failed ones (a run is complete only with none failed). Slice 28 calls the single `:generate` (up to 1,000 series per request), not `:generate-batches`. Not built: the weekly job here that calls the batch route, persists completed batches into a forecast version, retries failed batches and withholds a partial version, the Temporal workflow, and the 15-minute benchmark. |
| 4 | Manual override per cell | built | `PUT /forecasts/{modelCode}/lines` with source `manual` (or `statistical` for a loader); the forecast screen in the web module edits cells. |
| 4 | Consensus workflow, version lock and `forecast.published.v1` (FR-22.9, 4.22.17) | partly built | `POST /forecasts/{modelCode}:submit` (Maintain forecasts) locks a draft with cells; `:publish` and `:reject` (duty `planning.forecast.approve`) decide it. The approver may be neither the submitter nor a writer of any cell of the model (403 `PLAN-CONSENSUS-SOD`); publish marks the cells `consensus`, locks the model and writes `forecast.published.v1` in the same transaction. Cell writes on a submitted or published model are 409 `PLAN-FORECAST-LOCKED`. Tests: `domain/consensus_test.go`, `api/consensus_test.go`, `store/consensus_test.go` (database). Not built: the Temporal approval step (the decision is a synchronous call), notifications to approvers, reopening a published model (the next forecast cycle uses a new model code), several published versions of one model, and restricting a run to published models (a run nets any model it names). |
| 4 | ForecastLine persistence and PUT lines API | built | Migration `0002`, RLS on both tables. |
| 5 | Explain-this-plan pegging tree | built | `GET /v1/planning/planned-orders/{id}/pegging` and the planned-order screen. A bom-line demand expands through the planned order that drives it. The tree is returned in one response (depth up to 20), not lazy-loaded per node as the UI rule asks. |
| 6 | Simulation plans and planning scenarios (FR-22.15, BR-22.14) | partly built | Built (slice 13): `planning_scenario` (migration `0014`, RLS; belongs to a plan that has run and to that plan's legal entity, so 404 `PLAN-NOT-FOUND` on create for a plan that never ran or is outside the caller's scope) with `GET`/`POST /scenarios` and `GET`/`PUT /scenarios/{code}` under If-Match and optional Idempotency-Key (409 `PLAN-SCENARIO-EXISTS`, 409 `PLAN-SCENARIO-LIMIT` for an 11th active scenario of a plan); `POST /scenarios/{code}:run` takes the plan-run snapshot (stored item coverage and forecast model included), applies `domain.ApplyScenario` (sales and forecast demand times the item or overall factor, added sales demand, lead-time overrides, auto-firm off) and writes a `plan_version` of kind `simulation`, status `complete`, never current, with its own orders, pegging, messages and net requirements; the plan row lock is the same NOWAIT lock, so a simulation during a run is 409 `PLAN-0001` rather than queued; the plan must already have an operational run (404 `PLAN-NOT-FOUND`). The scenario-versus-operational diff is the existing version diff, and `GET /planned-orders?version=N` reads a simulation's orders. Not built: demand source from a named forecast version or demand program, multipliers by item group (the service has no item groups; factors are per item), BOM and route effective date, capacity modes (rough-cut, finite), start-inventory-as-of date, bucket and BOM-depth limit (pMRP rough run), rough-cut and purchase-volume evaluations, copying simulation results into a forecast version, net requirements and messages of a simulation over the API (`GET /net-requirements` and `/messages` read the current version only), scenarios queueing behind an operational run, and a retention budget of its own for simulation versions (they count toward `PLANNING_MAX_VERSIONS` with the operational ones, so frequent simulations push older operational versions out sooner; the current version and versions with firmed orders are always kept). |
| 6 | Supplier forecast publishing through w20 | handoff (PSG-20) | Depends on scenarios. |
| 7 | DDMRP buffers, buffer profiles, net-flow equation, priority-based planning (FR-22.16, BR-22.11) | partly built | `POST /ddmrp/buffers:evaluate` (`domain/ddmrp.go`): ADU from a past/future window, DAF, the BR-22.11 zones, qualified demand (past due, today, and days whose total exceeds the spike threshold inside the spike horizon), NFP, planned qty (TOG − NFP, at least MOQ, rounded up to an order multiple), priority NFP / TOG and the zone. Slice 10: `buffer_profile` and `ddmrp_buffer` (migration `0008`, RLS) with `GET`/`POST /buffer-profiles` and `GET`/`PUT /buffer-profiles/{id}` (Idempotency-Key, If-Match, `planning.setup.maintain`, 409 `PLAN-PROFILE-EXISTS`), `POST /ddmrp/buffers:recalculate` (`planning.run`; evaluates with the stored profile and upserts one row per item, site and warehouse) and `GET /ddmrp/buffers` (the buffer status inquiry by site, zone and priority ceiling); item coverage's buffer profile is a checked reference. Tests: `domain/buffer_quota_test.go`, `api/buffer_quota_test.go` (AC-22.12 on the stored path), `store/buffer_quota_test.go` (database, RLS). Not built: ADU from stored history and DLT from the BOM (w9) — the recalculation takes both from the caller; the nightly recalculation job (4.22.16, 00:15) and its zone-change > 30% report; `manual_override` on a buffer and planned adjustment factors per period (DAF is one value per recalculation); and the buffer's open supply and on-hand are those of the caller's last recalculation, so the run never re-reads stock (a buffer is as fresh as its `calculated_at`). Slice 20: the plan run replenishes decoupling points that have a stored buffer by the buffer's planned quantity, stamps the buffer's priority and zone on the order and raises the `PLAN-0010` red-zone message (see the slice 20 note above). Not built: running the buffer evaluation inside the run from the snapshot's own on-hand and supply; the dependent demand of a decoupling point reaching the buffer's qualified demand; and warehouse-scope buffers in the run (the run reads site scope only, FR-22.18). Slice 21: planned orders and messages sort by planning priority (`sort=priority` for every buffer order, and by default for the buffer orders of `priorityBased` items; see the slice 21 note). Not built: `priorityBased` inside the engine (the PRD's "priority ordering for planned-order generation": the run still generates, numbers and allocates quota to orders in date order and only the reads reorder them, and the read follows the item coverage as it is now, not as it was at the run); and a planning priority for a `priorityBased` item that has no buffer (only buffer orders carry one, so such an item's orders stay in number order). |
| 8 | Quota arrangements (FR-22.17, BR-22.12) | partly built | `POST /quota-arrangements:allocate` (`domain/quota.go`): lowest rating wins (ties by priority, then id), split by water-filling to equal ratings with `minSplitQty`, quota-0 sources as fallback, 422 `PLAN-0009` when no source is usable. Slice 10: `quota_arrangement` and `quota_source` (migration `0009`, RLS) with `GET`/`POST /quota-arrangements` (filters item, site, valid-on date) and `GET`/`PUT /quota-arrangements/{id}` (Idempotency-Key, If-Match replacing all sources, `planning.setup.maintain`); validity periods of one item and site may not overlap (409 `PLAN-QUOTA-OVERLAP`, under an advisory lock); each source names exactly the reference of its type once, with quota, base, carried-over allocated qty, maximum per period with its period, priority and blocked; responses carry the BR-22.12 rating. Tests: `domain/buffer_quota_test.go`, `api/buffer_quota_test.go`, `store/buffer_quota_test.go` (database, RLS). Slice 19: the run allocates planned orders to sources, honours the per-period maximum, stamps `quota_source_id` (and the vendor) on the order and books firmed quantity on the source (`TestApplyQuota_*`, `TestRun_Quota*`, `TestRunPlan_QuotaAllocationAndBooking`). Review of slice 19: source ids kept across an arrangement update, the arrangement version bumped by a booking, firmed and approved orders counted against the per-period maximum, split shares cut to a source's remainder, split numbering `<number>-<n>` with bom-line pegs divided, the plan hash over the allocation, and the vendor change re-resolving the source (`TestApplyQuota_SplitCutsAShareToThePeriodRemainder`, `TestApplyQuota_BookedOrdersCountAgainstThePeriodMaximum`, `TestApplyQuota_HashCoversTheAllocation`, `TestApplyQuota_SplitProductionDividesComponentPegs`, `TestRunPlan_QuotaAutoFirmBookingAndVendorChange`). Not built: allocated qty from orders created outside planning (handoff to PSG-17 and PSG-21: carry `quota_source_id` from `plannedorder.firmed.v1` on purchase, production and transfer orders and report cancellations so the booking can be released) and the daily recompute job (4.22.16, 00:45); approved orders of earlier runs are counted against a period maximum but not in the ratings until they are firmed; the arrangement choosing the order type (a source takes only orders of the type the item is planned with, so a mixed vendor and production arrangement divides purchases among its vendors and production orders among its production versions; switching type would need the engine to re-time the order and explode or drop its BOM demand); per-source lead times from trade agreements (PSG-17 handoff; a chosen source keeps the engine's order date); quota in simulation runs; the `PLAN-0009` message as a stored action message when no source can take an order (the order is left without a source and the run logs a `PLAN-0009` warning naming the orders); lot rounding of split shares and of a shortfall beyond the per-period maximum (a split order the capped sources cannot take whole stays without a source rather than partly allocated); checking vendor, production-version and warehouse ids against w17, w21 and w16 (UUIDs only); the legal-entity "Quota arrangements enabled" parameter; deleting an arrangement; and the quota arrangement analysis report (4.22.14). |
| 8 | MRP areas: warehouse planning scope (FR-22.18) | partly built | Warehouse scope is netted apart from the site and replenished by planned transfers or directly (slice 27, AC-22.16, see above). Not built for a warehouse scope: quota arrangements, DDMRP buffers, forecast demand and scenario levers (demand factor, added sales demand, lead-time overrides) apply to the site scope only; stored warehouse-scope coverage is discovered only with the opt-in `warehouseScopesFromCoverage` (slice 29; without it, and for stock, demand and receipts, the scopes are named inline); a scope's firmed order is carried forward but the plan scope filter judges it like its item; a warehouse calendar of its own (the run calendar is the site's, PSG-6); a from-warehouse other than the site and a warehouse scope supplying another warehouse (w16 warehouse topology); splitting the site's on-hand by warehouse inside planning (the caller sends each scope's stock, PSG-16); the issue demand of a carried-forward firmed production order (pre-existing for site orders too, the caller sends the target order's demand). |
| 8 | MRP areas: subcontractor planning scope (FR-22.18) | later slice of PSG-22 | Planning scope `subcontractor` (vendor and provision transfers) is stored on item coverage (slice 9). The provision orders inside the run need w21 vendor operations and w16 vendor locations (handoff). |
| 8 | Planning strategies MTS net/gross, MTO, ATO, PWFA, subassembly (FR-22.19, BR-22.13) | partly built | `POST /strategies:consume-forecast` (`domain/strategy.go`) applies the BR-22.13 consumption windows (backward, forward, backward-then-forward). The strategy code is stored on item coverage (slice 9). Slice 23: `mts-gross` is netted gross (not against on-hand), a scenario's added sales consume the ATO forecast, the PLAN-0015 `sales-on-top` message (migration `0021`) and a `strategy` on the run's coverage block (see the slice 23 note). Slice 22: the `planning_strategy` setup table (migration `0020`) with list, get, create and update (see the slice 22 note), and the plan run applies `mts-net`, `mts-gross`, `mto` and `assemble-to-order-final` to the forecast model (`domain.PlanForecast`, `TestPlanForecast_*`, `TestRun_AssembleToOrderConsumesForecast`, `TestRun_MakeToOrderDropsAndGrossKeepsForecast`, store `TestStrategySetup`, `TestRunStrategies`). Not built: `plan-without-final-assembly` and `subassembly` in the run (both plan the reduced forecast as `mts-net` does: the former needs finished-item supply held back until sales demand and the forecast placed on the components, the latter consumption by dependent demand); `mto` supply created per sales line and marked 1:1 (`usesMarking` is stored and shown but the run does not mark pegging, so a later run can re-peg); order-specific stock segregation (`segregatedStock`, needs w16, handoff); consumption by sales orders outside the run's snapshot (the run consumes against the sales lines it is sent, handoff PSG-27 for loading them); consumption of a forecast against the sales lines of a different forecast period of the model's reduction (the model's own `transactions` reduction is not applied under ATO, the consumption replaces it); the item-group default strategy of the parameter table (an item coverage always names its strategy, `mts-net` by default); the T461P migration mapping. |
| 9 | Shelf-life aware pegging (FR-22.20, BR-22.15) | partly built | `POST /shelf-life:allocate` (`domain/shelflife.go`): FEFO over batches with expiry − demand day ≥ required shelf life, the shortfall, and batches expiring unpegged by a horizon day; AC-22.15 tested. Required shelf-life days are stored on item coverage (slice 9). Not built: shelf-life pegging inside the plan run, planned supply for the shortfall with expiry = delivery + shelf life, and the `PLAN-0011` message. Batch expiry (w16/w24) and sellable shelf-life days (FR-24.19) are handoffs. |
| 9 | Supply schedule workbench (FR-22.21) | partly built | `POST /supply-schedule:project` (`domain/projection.go`) buckets demand, receipts and planned supply by day, week or 30-day month with projected on-hand and a below-safety-stock flag; a PAB-identity property test; the calculator card in the web module. Slice 25: `GET /plans/{planCode}/projection` is the grid over the current plan version for up to 20 items, with a Supply schedule page. Not built: a scenario or past version as the source, inline planned-order creation, quantity edits and firming in a cell, a separate safety-stock line per period (the stored gross requirements include it), calendar-month buckets in the calculator (the stored-plan grid has them), and the printable report. |
| 9 | Inventory forecast projection with value and days of supply (FR-22.22) | partly built | The same projection returns value (closing on-hand × unit cost as platform `money`, rounded to the currency's minor unit) and days of supply (on-hand over the next 30 days' average daily demand). Slice 25 reads the projection from the current plan version (`GET /plans/{planCode}/projection`, value from an entered unit cost). Not built: a scenario as the source, a safety-stock line apart from the run's own netting (the stored demand already includes it) and current cost from inventory costing (w20 handoff; the cost is entered). |

## Plan execution and operations (PRD 4.22.1, 4.22.10, 4.22.16)

| Item | Status | Reason |
|---|---|---|
| Dynamic net-change plan with 60 s debounce (FR-22.1, BR-22.6) | partly built | `domain.ReachSet` gives the changed items plus every item below them through the BOM (`domain/reach_test.go`). Not built: run kind `dynamic` on `:run`, the event consumer with `PLANNING_DYNAMIC_DEBOUNCE_SECONDS`, holding the dependent demand that planned orders outside the set put on items inside it, and merging the replanned set into the current version without changing other orders. |
| Plan run as a Temporal batch job; nightly 01:00 static plan | later slice of PSG-22 | Slice 1 runs synchronously in the request, which is not the target for 50k items. |
| Pointer swap on success, partitions per PlanVersion | partly built | Slice 1 runs in one transaction, marks the new version current and demotes the old one. Per-version partitions are not built. |
| Version purge beyond `PLANNING_MAX_VERSIONS` (20) | partly built | Each run deletes versions beyond the newest `PLANNING_MAX_VERSIONS` (env, 2 to 100, default 20) with their rows, in the run transaction; versions holding a firmed planned order are kept. Not built: the daily 03:00 job, partition drops (no partitions yet) and the per-plan parameter (one setting per deployment). |
| Firm planned orders API: `POST /planned-orders:firm` | built | Duty `planning.order.firm`. The PRD's segregation-of-duties rule (with *Approve purchase orders* above the w17 approval limit) is a handoff to PSG-17, which owns approval limits. |
| `:firm` on a simulation version refused with 409 `PLAN-0003` | built | `FirmPlannedOrders` refuses any order of a simulation version with 409 `PLAN-0003`, and so does PATCH and DELETE of one (`TestRunSimulation_IsolatedFromTheOperationalPlan`). The PRD's AC text spells the code `PLAN_SIMULATION_NOT_FIRMABLE`; the service uses the catalog code `PLAN-0003`. |
| Net requirements MD04 view `GET /net-requirements` (FR-22.12, AC-22.10) | partly built | Each run stores, per item and day with movement, gross requirements, scheduled receipts, planned receipts, PAB and the day's net requirement (`net_requirement`, RLS, purged with its version); `GET /net-requirements?planCode&itemId` reads the current version. AC-22.10 is `TestRun_AC2210NetRequirementsOfRM300`. Not built: Requirement (ReqTrans) rows per demand and supply line (the view is aggregated per day), `siteId` and version or scenario selection on the query (a plan is one site; the current version is read), the messages column, the what-if simulate action, and refreshing the view after a planned order is changed or deleted (it shows the run). |
| Plan scope filters: site, item group, planner, LLC range, `mps` scope (FR-22.13) | partly built | Slice 18: `scope` on the run input (`itemIds`, `itemGroups`, `planners`, `llcMin`/`llcMax`, all given criteria must match) with `itemGroup` and `planner` on each run item; the whole snapshot is still exploded and netted, then orders, pegs, messages and net requirements are restricted to the items inside and the hash is recomputed (`domain.ApplyScope`); `itemsPlanned` counts the items inside. The site filter is the run's own `siteId`. The run page has the scope fields and the planning horizon. Not built: item group and planner are sent inline because the item master (PSG-9) is not read, so they are not stored on item coverage; the `mps` scope with MPS output as fixed supply (flag `planning.mps-scope`); a scoped version replaces the plan's current version like any run, so the planner decides which scope runs when. A scoped version's orders can peg to parent orders outside it (the explain tree stops at them). |
| Version-to-version diff API (FR-22.14) | partly built | `GET /plans/{planCode}/diff?from&to` (duty `planning.read`, `domain.DiffVersions`): planned orders match on item, order type and delivery day; orders equal in quantity, order date and vendor pair first and are unchanged, the rest are reported changed (quantity, order date or vendor), added or removed, with the quantity delta per order type, the message count delta and whether the plan hashes are equal; deleted orders are left out. The counts and deltas cover every order while each list stops at `limit` (default and maximum 500, `truncated` says so). Tests: `domain/versiondiff_test.go`, `store/versiondiff_test.go` (database), `api/versiondiff_test.go`; the compare screen in the web module. Since slice 13 the diff also compares a simulation version with the operational one (versions are chosen by number, whatever their kind). Not built: diffing messages and net requirements item by item, value deltas and deltas by item group (the 4.22.14 scenario comparison report needs item cost and item groups from other services), the `PLAN-0013` hash-mismatch incident for identical inputs, and a measured "diff of two versions in < 2 s" on the 50k-item dataset. |
| UoM conversion of forecast lines and inventory UoM (BR-22.10) | later slice of PSG-22 | Quantities are taken as given (decimal, 6 places in the database). |
| Coverage groups API `GET /coverage-groups` and item coverage setup | partly built | Coverage groups and item coverage are persisted with list, get, create and update (deliverable 1). Since slice 11 a run reads the site-scope item coverage and its group for items sent without coverage, order type or lead time; items without stored coverage still carry it on the run input. |
| Events `plan.started.v1` and `message.raised.v1` | partly built | Both are written to the outbox in the run's transaction: `plan.started.v1` (plan, version, kind `static`, site, as-of, items) ahead of the run's other events, and one `message.raised.v1` per stored message (message id, action, item, supply and demand ref, suggested date, delay days, qty). Because runs are synchronous, `plan.started` reaches consumers together with `plan.completed`; it will lead it once the run is a Temporal job. The PRD's `requirementId`/`refType`/`refId` are carried as `supply_ref` and `demand_ref`, since Requirement rows are not stored. |
| Event `forecast.published.v1` | built | Written to the outbox in the publish transaction (`store/consensus.go`) with model id and code, version, item series, horizon periods, first and last day, submitter and approver; declared in `events/asyncapi.yaml`. The PRD spells the firming event `planned-order.firmed.v1`, but the kernel's event type rule admits no hyphen in the entity segment, so it is `plannedorder.firmed.v1`. |
| Event `forecast.generated.v1` | built | Slice 28: written to the outbox in the generation transaction (`store/forecast_generate.go`) with model id and code, version, series, cells written and kept, first and last day and the user; declared in `events/asyncapi.yaml`. Series stay inside the model; nothing consumes it yet. |
| Feature flags `planning.dynamic`, `planning.statistical-forecast`, `planning.mps-scope` | partly built | `PLANNING_FEATURE_FLAGS` (Helm value `featureFlags`, comma-separated) is parsed at startup before the database is opened; an unknown name stops the service, and the enabled set is logged (`cmd/planning/flags.go`, `flags_test.go`, `main_test.go`). Not built: a per-tenant flag service (the platform has none yet; one setting per deployment), and gating the dynamic plan kind and the MPS scope, which do not exist yet. Slice 28: `planning.statistical-forecast` gates `:generate` and, when on, the service refuses to start without `FORECAST_URL`. |
| Plan run summary email and firming result report (PDF, email; 4.22.15) | later slice of PSG-22 | The firming response and the web module list the target orders of a batch; no document or email is rendered. |
| Sanctions screening block on firming (w48) | handoff (PSG-48) | Firming does not consult screening; PSG-48 owns the screening status of vendors. |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1: 40 textbook MRP scenarios (CPIM) in `testdata/` | built (engine) | `testdata/mrp-textbook`: 42 bucketed CPIM-style scenarios (lot-for-lot, FOQ, POQ, min/multiple/max modifiers, safety stock, safety lead time, past-due releases, 2- to 5-level BOMs with common components, min/max). Expected planned orders (item, qty, release and receipt date, and for the 11 scenarios with a past-due release the futures date and delay, propagated through FIFO pegging to parents per BR-22.1 step 7 and BR-22.2) are in `expected.json`, written by `reference_mrp.py` (an independent bucketed MRP, run with `python3 -I`), which fails unless it reproduces the hand-derived plans and futures in `scenarios.json`. `TestRun_TextbookFixtures` matches all 42, futures dates and delay days included; 40 equal the plain textbook plan and S22/S23 follow BR-22.5 (a safety-stock-only order is dated today + lead time, not past due). The fixtures found two engine defects, fixed with `TestRun_MinMaxChecksTodayAfterTodaysDemand`, `TestRun_MinMaxCountsOverdueReceiptToday` and `TestRun_PeriodCoverageIgnoresSupplyAfterTheShortfall`; review found a third (period coverage with negative days netted a waited-for receipt twice), fixed with `TestRun_PeriodCoverageNetsWaitedReceiptOnce`. Delay messages are not compared by the fixtures (they are covered by `TestRun_BR222DelayPropagatesToSalesLine`). Not covered: scrap and phantom scenarios of the PRD fixture list (the engine has no component scrap or phantom items) and multi-source (quota, tested in `quota_run_test.go`). |
| AC2: 50k items x 12 periods x 5 levels in < 10 min, deterministic | partly built | `PASSAGE_PLANNING_SCALE_ITEMS=50000 go test -run TestRun_Scale50kDeterministic ./internal/domain/` plans the seeded `mrp-50k` snapshot (50,000 items on five low-level codes, 200,000 sales lines over 12 monthly periods, open POs, mixed coverage and lot rules; 2.16 million planned orders) three times, once in reverse input order: about 11 s per run and 5.4 GB peak RSS on a developer laptop, one plan hash, and every order's explain tree covers its quantity. The 50k run is opt-in, because its memory would put the CI runner at risk; plain `go test ./...` runs the same test on a 5,000-item snapshot of the same shape (about 1 s per run, 0.7 GB), and `-short` skips it. CI does not run the 50k proof. Not measured: the run through the API with persistence of the version, on the 3-node reference cluster, as the Temporal job. |
| AC3: pegging explains 100% of each planned order | built | Property tests `TestRun_PropertyPeggingExplainsSupply` and `TestRun_PropertyMultiLevel`; the explain tree (`BuildTree`) of every order of the 42 fixtures and of the 50k plan sums to the order quantity (`assertPeggingExplainsOrders`). |
| AC4: match frePPLe on a shared dataset | later slice of PSG-22 | Optional; needs a frePPLe installation and an export of the shared dataset (`mrp-50k` or `mrp-textbook`). Not attempted. |
| AC5: every deliverable has code or a reason here | built | This file. |
| AC6: `go vet`, `go test`, OpenAPI and AsyncAPI match, chart lints | built | Verified in this slice. `TestOpenAPI_MatchesRoutes` checks routes and duties, `TestAsyncAPIDeclaresEveryEvent` the published event types. |
| AC-22.3 identical hash across three runs of the 50k dataset | partly built | `TestRun_Scale50kDeterministic` with `PASSAGE_PLANNING_SCALE_ITEMS=50000` (engine, opt-in): three runs, equal hashes, each well under 10 min. The stored version's hash over three API runs is not measured (see AC2). |
| AC-22.9 second run returns 409 `PLAN-0001` while one is running | built (synchronous form) | The store takes `FOR UPDATE NOWAIT` on the plan row; a run that cannot take it gets 409 `PLAN-0001` (`TestRunPlan_RefusesConcurrentRun`). With runs synchronous in slice 1, "running" means a run transaction in flight; the `running` version status arrives with the Temporal job. |
| AC-22.1 BR-22.2 worked example, including the 7-day delay message on the sales line | built | `TestRun_BR222WorkedExample`, `TestRun_BR222DelayPropagatesToSalesLine`. |
| AC-22.2 pegging explains every planned order | built | Property test over random snapshots (the 40-fixture set is AC1 above). |
| AC-22.4 min/max 100/400 with PAB 90 gives one order of 310 | built | `TestRun_MinMaxOrdersUpToMax`. |
| AC-22.5 early PO with positive days 5 gets Postpone to demand date − receipt margin | built | `TestRun_EarlyReceiptRaisesPostpone`. |
| AC-22.6 forecast 100, sales 30, `transactions` gives 70 | built | `TestReducePeriods_Principles`, `TestForecastLines` (store), `TestRun_NetsReducedForecastOfTheModel` (API). |
| AC-22.12 BR-22.11 buffer gives an order of 115, priority 0.5306, yellow | built | Calculator tests `TestEvaluateBuffer_NetFlowExample` and `TestBufferEvaluate_WorkedExample`; stored with its profile by `TestRecalculateBuffer_BR2211WorkedExample` (API) and `TestBufferProfilesAndBuffers` (database). Slice 20: the plan run creates the planned order of 115 with priority 0.5306 and zone yellow for a stored buffer of a decoupling point (`TestApplyBuffers_ReplenishesByBufferQuantity`, `TestAddBufferResults_StampsPriorityAndZone`, `TestRun_BufferReplenishesDecouplingPoint`, `TestRunBuffers` with the stored BR-22.11 buffer, `TestRunPlan_StoresBufferPriorityAndRedZoneMessage`). |
| AC-22.13 BR-22.12 quota picks vendor B, B's allocated qty 200 after firming | built (planning side) | `TestAllocateWhole_LowestRatingWins` and `TestQuotaAllocate_WholeGoesToLowestRating` (B chosen, rating 5.00 after); the run picks B and stamps the order (`TestApplyQuota_AC2213LowestRatingGetsTheOrder`, `TestRun_QuotaArrangementPicksVendorAndStampsTheOrder`) and firming adds the order's quantity to B's `allocated_qty` (`TestRunPlan_QuotaAllocationAndBooking`, database). Purchase orders created by PSG-17 do not yet carry `quota_source_id` (handoff). |
| AC-22.14 assemble-to-order gross requirement 100, not 140 | built (run with a forecast model) | `TestConsumeForecast_AC2214AssembleToOrder` and the API test for the calculator; slice 22: `TestPlanForecast_AssembleToOrderConsumesForecast` (domain) and `TestRun_AssembleToOrderConsumesForecast` (the run pegs forecast 60 and sales 40; under `mts-net` the same data pegs 100 and 40). |
| AC-22.15 short shelf-life batch not pegged, planned order covers the demand | partly built | `TestAllocateBatches_AC2215ShortShelfLifeNotPegged` and `TestShelfLife_AC2215` (no allocation, shortfall 30); the planned order for the shortfall needs engine integration. |
| AC-22.8 three approved purchases of one vendor and date firm into one PO with three lines, each firmed with the target id | built | `TestGroupForFirming_AC228` (domain) and `TestFirmPlannedOrders` (store, database). The PO itself is created by PSG-17 from the events. |
| AC-22.10 net requirements of RM-300 show PAB 400, 190, 110 for weeks 2 to 4 | built | `TestRun_AC2210NetRequirementsOfRM300` (engine) and `TestRunPlan_NetRequirementsAndMessageEvents` (stored and read back, database). |
| AC-22.11 scenario "Demand +20% on group FG": simulated production is at least the operational plan's, no ActionMessage reaches purchasing, `:firm` returns 409 | partly built | `TestApplyScenario_AC2211SimulatedQuantityNotBelowOperational` (domain), `TestRunSimulation_IsolatedFromTheOperationalPlan` (database: only `scenario.simulated.v1` in the outbox, `PLAN-0003` on firm). The scenario's factor is per item, not per item group, until the item master offers groups. |
| AC-22.16 | built | `TestWarehouseScopeTransfer_AC2216` (domain), `TestRun_WarehouseScopePlansTransfer` (api) and store `TestWarehouseScope_PersistsAndFirms`: WH-2 demand is covered by a planned transfer from the site, and WH-2 on-hand is not netted against site demand (`TestWarehouseOnHandNotNettedAgainstSite`). |

## Verification

| Item | Status | Reason |
|---|---|---|
| Database-backed tests and `0001_init.sql` to `0026_message_disposition.sql` on an empty database | built | Run in review against Postgres 16 with an admin URL and a non-superuser app role (`PASSAGE_TEST_DATABASE_URL`, `PASSAGE_TEST_APP_DATABASE_URL`). CI must set both. |
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

Slice 28 wires the statistical forecast into planning (FR-22.9): `POST /v1/planning/forecasts/{modelCode}:generate` takes monthly
histories, calls `forecast-py` (`internal/forecastclient`, `FORECAST_URL`, flag `planning.statistical-forecast`) and writes the
result as statistical cells of the model, keeping manual and consensus cells, under If-Match and with `forecast.generated.v1`
(see the table row). Tests: `domain/statforecast_test.go`, `forecastclient/client_test.go`, `api/forecast_generate_test.go`,
store `TestGenerateStatisticalCells`, `cmd/planning` `TestRun_StatisticalForecastNeedsForecastURL`, web `forecastgenerate.test.tsx`.
No migration (the cells use `forecast_line`), no new duty or grant; the route reuses `planning.forecast.maintain` but needs
a route row (authz seed, see the slice 28 handoffs below). Review: the forecast service is called outside the database
transaction, after a lock-free pre-check that refuses a stale If-Match (412) or a submitted or published model (409) without
running the models; the store checks both again under the row lock. The client allows 60 s and reads at most 8 MiB, checks that
every result is for the item and site of its series, and takes `FORECAST_URL` from configuration only (an absolute http or https
URL, checked at startup). Like the other Go callers of the Python compute services (scheduler-opt, the TMS optimizer) it sends no
service token and no tenant: forecast-py is stateless, in-cluster and sees only the series. Forecast values are rounded half away
from zero to 6 decimals (BR-22.10), not to the item's unit decimals (planning holds no unit of measure); a value above the
largest storable quantity is 422 `PLAN-FORECAST-RANGE` and nothing is written. The flags are per deployment (one
`PLANNING_FEATURE_FLAGS` value for every tenant), so the service refuses to start with the flag on and no valid `FORECAST_URL`.

Slice 30 (action message decisions, FR-22.7): `action_message` gains `status` (open, applied, dismissed), `version`, `decided_by`,
`decided_at`, `decision_note` and `decision_key` (migration `0026`, no new table, so no new grant: passage_app already holds SELECT, INSERT,
UPDATE and DELETE on it, under the tenant and legal-entity policies). `GET /messages` lists open messages by default (`status` open, applied,
dismissed or all) and every message carries its `id`, `status` and `version`; `GET /messages/{id}`, `POST /messages/{id}:apply` and
`POST /messages/{id}:dismiss` (duty `planning.order.maintain`, If-Match, optional Idempotency-Key, optional `note` up to 500 characters). Rules:
404 `PLAN-MESSAGE-NOT-FOUND`; 409 `PLAN-MESSAGE-DECIDED` for a message decided before (a retry with the same Idempotency-Key and decision replays it
with 200 and `Idempotent-Replayed`); 409 `PLAN-MESSAGE-STALE` when its plan version is no longer current; 412 for a stale If-Match; 422
`PLAN-MESSAGE-NOT-ACTIONABLE` when applying delay, shortage, red-zone, sales-on-top or no-vendor (they can only be dismissed). The event is written in the deciding
transaction. Decisions across runs (review fix): every run writes new message rows under a new version, so the run now carries each
applied or dismissed decision of the version it replaces onto the identical message of the new version (same item, scope, action, supply,
demand, suggested date, quantity and delay; `carried_from` names the message the planner decided, `carriedFrom` in the API) and raises no
`message.raised.v1` for it; a dismissed message therefore does not come back as open, and an applied one is not applied (and published) twice
while its owner has not changed the order. A message whose date or quantity changed is a new message and is open. A decision locks the
message's version (`for share`) before the message, so a concurrent run either carries it or the decision gets 409 `PLAN-MESSAGE-STALE`.
Tests: `domain/message_decision_test.go`, `api/message_decision_test.go`, store `message_decision_test.go`
(`TestRunPlan_CarriesMessageDecisions`, `TestDecideMessage_TenantAndLegalEntityIsolation`), web `messagedecision.test.tsx`. Not built:
changing the order itself (the owning context does that from the event, and no consumer exists yet, so an applied message about an order
nobody changed keeps being raised and stays applied on every run), undoing a decision, bulk decisions, and applying a message to a planned order
of this service (runs raise no messages of those actions on planned orders). The decision row of a message that no run raises again is deleted
with its version by the retention purge (20 versions by default); the `message.applied.v1` or `message.dismissed.v1` event is the lasting record.

## Handoffs to other waves

- PSG-16 inventory: on-hand by dimension, batch expiry (FR-22.20), warehouse stock segregation (FR-22.19).
- PSG-17 procurement: create the purchase order (lines = planned orders sharing `target_id`) from `plannedorder.firmed.v1`, report a failure for the saga compensation, approval limits on firming; trade-agreement lead times and vendor price; carry `quota_arrangement_id` and `quota_source_id` (both on `plannedorder.firmed.v1` since slice 19) on purchase orders and release the booked quota qty when such an order is cancelled (FR-22.17).
- PSG-21 manufacturing: create the production or transfer order from `plannedorder.firmed.v1` (saga as above); route lead times; subcontractor operations; production versions for quota sources and `quota_arrangement_id` and `quota_source_id` on production and transfer orders, released on cancel (FR-22.17).
- PSG-21 manufacturing, warehouse scopes (slice 27): `plannedorder.firmed.v1` with `warehouse_id` set and `order_type` `transfer` is a transfer from the site to that warehouse (create the transfer order with it); with `production` the order produces into that warehouse. PSG-17 procurement: with `purchase` the order line is delivered to that warehouse (a vendor-date group may mix site and warehouse lines). `message.raised.v1.warehouse_id` names the scope a message is about. No new duty, route row or service grant (authz seed: none).
- PSG-16 inventory, warehouse scopes (slice 27): on-hand split per warehouse so the run snapshot sends the site scope's stock without the stock of the item's warehouse scopes, and each scope's own stock (FR-22.18).
- PSG-27 sales / PSG-29 billing, statistical forecast (slice 28): the monthly sales history per item and site that `:generate` takes (shipped quantity by calendar month, oldest first); until a feed exists the caller sends it. Deployment (`deployment-cicd`): set `PLANNING_FEATURE_FLAGS=planning.statistical-forecast` and `forecastUrl` (the forecast chart's service URL) together, and allow planning to reach the forecast service on the network policy.
- PSG-27 sales: sales-line demand events (ref type `sales-line`).
- PSG-9 product: BOM versions and low-level code change events.
- PSG-6 calendars: site working calendar (closed weekdays and dates) readable by site, so the run need not carry it.
- PSG-20 supplier portal and PSG-35 EDI: supplier forecast publishing.
- PSG-24 batch and shelf life: sellable shelf-life days (FR-24.19).
- PSG-20 inventory costing: current item cost for the inventory forecast value (FR-22.22).
- PSG-48 sanctions screening.
- Policies (`policies/`, not owned by this wave): register the duty `planning.simulate` (Run simulations: create, change and run scenarios) for the planner and S&OP manager roles, and `planning.forecast.approve` (Approve forecast consensus) for the supply chain manager role, apart from `planning.forecast.maintain` (Demand planner), and `planning.setup.maintain` (Maintain planning setup: coverage groups, item coverage, buffer profiles, quota arrangements) for the planning administrator role. The buffer recalculation uses `planning.run` (Run master planning, including the batch service account).

## Legal-entity scope follow-ups (#241)

- `firming_batch` (with its `response` document), `item_coverage` and `ddmrp_buffer`
  carry `legal_entity_id` since #292 (migration 0013). `coverage_group` and
  `buffer_profile` stay tenant-level on purpose: they are shared planning setup that the
  coverages and buffers of every entity refer to by id.
- **handoff**: when the scope-claim hardening of PR #256 lands, `httpx.OwnerLegalEntity`
  must also require `tenant.DutyAllScope` for a caller with no legal entity.
- **handoff**: existing rows keep a NULL `legal_entity_id`; assigning them is a
  data migration owned by the tenant administrator. Until it runs, a scoped caller
  that reuses the code of such a plan or forecast model gets 409 `PLAN-CODE-TAKEN`
  (the message says an administrator must assign a legal entity), and an
  all-entity caller must name the owner when it creates.

## Slice 13 handoffs

- **handoff (gateway and policies)**: the gateway must route `/v1/planning/scenarios` and
  `/v1/planning/scenarios/{code}:run` to this service, and `planning.simulate` must be seeded in
  `policies/authz/data.json` (see the policies handoff above and #313).
- **handoff (consumers)**: `scenario.simulated.v1` is for analytics and the supplier-forecast
  publisher (PSG-20); operational consumers of `plan.completed.v1` need no change because a
  simulation never publishes it.

## Slice 28 handoffs

- **authz seed (main session)**: add the route row `{"resource": "/v1/planning/forecasts/*:generate", "methods": ["POST"]}`
  to duty `planning.forecast.maintain` in `policies/authz/data.json`. The duty exists; without the row the gateway denies
  `POST /v1/planning/forecasts/{modelCode}:generate`. No new duty, service grant or service principal.
- **tenant feature flags (platform)**: `planning.statistical-forecast` is one setting per deployment; per-tenant enablement
  needs the platform feature-flag API (`GET /v1/platform/feature-flags`), which does not exist yet.

## Slice 30 handoffs

- **authz seed (main session)**: add the route rows `{"resource": "/v1/planning/messages/*", "methods": ["GET"]}` to duty `planning.read` and
  `{"resource": "/v1/planning/messages/*:apply", "methods": ["POST"]}` and `{"resource": "/v1/planning/messages/*:dismiss", "methods": ["POST"]}` to duty
  `planning.order.maintain` in `policies/authz/data.json`. Both duties exist. Without the rows the gateway denies the three new routes. No new duty,
  service grant or service principal.
- **PSG-17 procurement / PSG-21 manufacturing (consumers)**: act on `message.applied.v1` (`action`, `supply_ref` the receipt or order number,
  `suggested_date`, `qty`, `warehouse_id`): postpone and advance move the receipt to `suggested_date`, cancel removes it, decrease and increase lower or raise its
  quantity by `qty` (the surplus or the shortage the message reports). `message.dismissed.v1` lets a task mirror close. Neither consumer exists yet.
  Planning publishes one `message.applied.v1` per decision (a run that raises the message again carries the decision without a new event), but
  the consumer must still treat `message_id` as its idempotency key, since the outbox delivers at least once and `decrease`/`increase` are deltas.
