# NOT-IMPLEMENTED — warehouse (PSG-31, wave 31)

Slice 1 built the pure directive engine, the location and license-plate rules,
the work state machine, one stateless simulate endpoint, the web simulator page,
the chart and the contracts. Slice 2 adds hazmat storage compatibility (FR-31.19)
to the engine and simulate endpoint, the cross-dock receipt allocation (FR-31.13
planning), packaging-specification determination and round-up (FR-31.17), and
the engineered labor formula, travel metrics and performance calculation
(FR-31.18). Slice 3 persists locations (with their stock), location
directives and work, and builds work execution: create work placed by the
persisted directives with open-line reservations, assign with
`FOR UPDATE SKIP LOCKED` and lock timeout, confirm, short pick and cancel, with
the work events through the outbox and idempotent commands; and the web pages
for locations, directives and work. Everything below is missing and is listed with its reason. Status is
`later slice of PSG-31` (same wave, not yet built) or `handoff` (needs another owner).

## Deliverables (ticket list 1–10)

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Location model: LocationProfile and Zone as entities, LocationFormat persisted, location update, LPs and nested handling units in Postgres | later slice of PSG-31 | Slice 3 persists locations (`POST /locations:bulk`, `GET /locations`) with capacity limits, status, zone and pick sequence on each location, and their stock by item (`location_stock`, no LP or dims). Not built: a separate LocationProfile and Zone record (limits are copied per location), parsed aisle/rack/shelf/bin columns, updating or deactivating a location, the 1,000,000-location limit check, and the `license_plate` table with nesting (the LP id and depth rules exist in `lp.go`). The web page generates names from a format but the format is not stored. |
| 2 | Rules engine: directive versioning and effective dating, update and delete, work templates, wave templates, replenishment templates | later slice of PSG-31 | Slice 3 persists directives (`POST`/`GET /location-directives`) and work creation evaluates them. Directives cannot be changed or removed yet, have no version or effective date, and the `multiple_sku` flag is not modeled. Work templates (pick/put pairs, work class, work breaks, max lines) and wave templates are not modeled, so a work's lines are exactly the request's lines. |
| 3 | Work execution: work class and user filters on assignment, skip location, configurable over-pick tolerance, cancel-with-reversal, supervisor reassign/unassign/priority change, blocked-work re-evaluation | later slice of PSG-31 | Slice 3 builds create, list, read, assign (best priority, then oldest; `FOR UPDATE SKIP LOCKED`; idle ASSIGNED work returns to OPEN after `WMS_LOCK_TIMEOUT_MIN`), confirm (assignee only, row locks, capacity and mixing re-checked), short pick with reason and a child remainder work, and cancel with reason. Not built: work classes and user filters (FR-31.4/31.7), skip location (`SKIPPED` line status), over-pick tolerance above 0% (needs per-warehouse/item settings; BR-31.7 serial and quality exclusions with it), confirming another LP or location than planned, cancel-with-reversal of confirmed lines (compensating MOVEMENT work), supervisor reassign/unassign/priority change, and re-evaluating BLOCKED_NO_LOCATION work to OPEN. A work holds no wave, load or order reference yet. |
| 4 | Load planning, dock appointments, ASN-driven receiving with LPs | later slice of PSG-31 | Requires LPs in the store and the wave-28 shipment and wave-18 receipt contracts. |
| 5 | Slotting analysis and warehouse KPIs | later slice of PSG-31 (KPIs), later slice of PSG-31 (slotting) | Needs historical picks and the Cube semantic layer from wave 5/analytics. |
| 6 | Outbound sorting (sort templates, sort positions, sort-station scan); persisted cross-dock templates, links and CROSS_DOCK work | later slice of PSG-31 | Slice 2 has the pure receipt allocation `PlanReceipt` (planned links, then opportunistic demand in window, with quality hold); no endpoint or screen calls it yet, because the PRD API (`POST /cross-dock-templates`) and the receipt registration it runs at need the store. Persisted templates and links, CROSS_DOCK work creation, the re-plan job and sort positions are not built. The `outbound_staging` strategy name is accepted by the engine. |
| 7 | Value-added services and kitting (VAS orders, kit-to-order, kit-to-stock, disassembly) | later slice of PSG-31 | Needs VAS orders, kit BOMs from wave 9 and the wave-16 posting path. Packaging specifications are built as domain functions (`DetermineSpec`, `RoundUpToLP`) with no endpoint or screen; their persistence and maintenance API, the freeze on work lines (BR-31.13), the `packaging_spec_required` block, the `round_up_to_lp` engine strategy using them, cartonization and material consumption through wave 16 are not. |
| 8 | Transfer-order warehouse flow with LP identity across sites | later slice of PSG-31 | Needs wave-16 transfer orders and the in-transit ledger. |
| 9 | Engineered labor standards and performance reporting | later slice of PSG-31 | Slice 2 has the formula (`Formula.Planned`), travel metrics, `Actual` from timestamps and pauses, `LaborLine` with the outlier band and its WMS-0118 text, and `Performances` per group, as domain functions with no endpoint or screen. The formula is a fixed linear shape, not a wave-3 expression. Versioned, effective-dated standards, the `/labor/standards` and `/labor/performance` endpoints, persisted confirmations, shift close and performance documents (LPD-*) are not built. Wave-32 step telemetry is not wired. Indirect activity codes and the GRAPH travel metric are not built. |
| 10 | Hazardous-material profiles and storage compatibility: persistence and maintenance | later slice of PSG-31 | Slice 2 built the storage check (`EvaluateWithCompliance`, BR-31.15) and the simulate `compliance` block. Not built: persisted `HazmatProfile`, segregation matrix and `FireContainmentSection` with `POST /hazmat-profiles` (duty WMS_HAZMAT_MAINTAIN), the tenant matrix version `hazmat_segregation_matrix`, section limits in litres (only kg), the hazmat-inventory-by-section inquiry (LX06/LX24) and its list, and WMS-0115/WMS-0116 as HTTP errors on work confirmation (they appear as trace reasons only). Section quantity is the used weight of locations holding a profiled item, because stock carries quantities, not per-item weights, until quants are persisted. Work creation (slice 3) runs the directives without hazmat data, because no profile is persisted: a hazmat put-away is only checked in the simulator. |

## Acceptance criteria (ticket and PRD §6.31.8)

| Criterion | Status | Reason |
|---|---|---|
| AC1 / AC-31.1 directive fixtures: 100 scenarios | partly built | `testdata/directive-scenarios-100.json` holds 100 put-away scenarios (10 quantities × 10 occupancy layouts) with hand-derived location, directive, line and action; `TestSimulate_DirectiveFixture` posts each to the simulate endpoint. Missing: PICK (outbound) scenarios, which need quant dates for `fifo`/`fefo`, and the review of the fixture against D365 USMF behavior. Later slice of PSG-31. |
| AC2 simulation: 10k lines/day, p95 assignment < 1 s, no deadlocks | later slice of PSG-31 | Assignment exists and `TestStore_ConcurrentAssignment` runs 25 concurrent assigners over 20 works with no duplicate and no error, but the 10k lines/day simulation and its latency measurement (fixture `sim-10k-lines-day`) are not built. |
| AC3 capacity never exceeded (property) | partly built | Property tests cover receive sequences (`location_test.go`) and mixed receive/issue sequences through `Location.Move`, the path confirmations use (`execution_test.go`). Confirmation re-checks capacity under a row lock. Planning reserves open lines but takes no lock, so two concurrent plans can target the same room; the later confirmation is then refused (WMS-0102) rather than exceeding capacity. |
| AC4 wave release of 500 orders creates correct work and replenishment | later slice of PSG-31 | No waves, Temporal workflow or replenishment. |
| AC-31.2 max_lps = 1 trace `capacity: lps 1/1` | built | `TestEvaluate_BlockedTraceExplainsRejection`. |
| AC-31.3 wave release 500 orders, 41 replenishments, < 120 s | later slice of PSG-31 | Wave release is not built. |
| AC-31.4 50 concurrent `work:assign`, no duplicates | partly built | `FOR UPDATE SKIP LOCKED` assignment; the store test runs 25 concurrent callers over 20 works. The 50-user, 1,000-work run with the p95 < 1 s measurement is not built. |
| AC-31.5 short pick to PARTIALLY_COMPLETED with 4 EA remainder | built (warehouse side) | `TestStore_ShortPick`: the pick of 10 short picked at 6 with `EMPTY` is PARTIALLY_COMPLETED, a child OPEN work of 4 EA references it, and the location loses exactly 6. The inventory ledger posting of the 6 EA transfer is wave 16's consumer of `work.completed` (handoff). |
| AC-31.6 pallet move moves 13 LPs, one `work.completed` | later slice of PSG-31 | Moves are not built. The nesting rule is tested. |
| AC-31.7 ASN receipt discrepancy −5 | later slice of PSG-31 | Receiving is not built. |
| AC-31.8 `DOOR_SLOT_OVERLAP` | later slice of PSG-31 | Dock appointments are not built. |
| AC-31.9 property over 10,000 work/confirmation sequences | partly built | The domain property test runs random receive/issue sequences through `Location.Move`. A property test over persisted work plan/assign/confirm sequences is not built. |
| AC-31.10 cross-dock to outbound staging, ATP counts once | partly built | `PlanReceipt` gives one 40 EA CROSS_DOCK allocation and a 60 EA put-away remainder (`TestPlanReceipt_AC31_10...`). CROSS_DOCK work, the wave-16 reservation move that keeps ATP counting once, and the staging location are later slice of PSG-31 / handoff (wave 16 ledger). |
| AC-31.11 sort positions | later slice of PSG-31 | Sort templates are not built. |
| AC-31.12 kit posting atomic, WMS-0114 | later slice of PSG-31 | VAS and kit posting are not built. |
| AC-31.13 labor standard fixture to 0.1 s | partly built | The formula fixture (planned seconds to 0.1 s) and the performance sum over non-outlier lines are tested in `labor_test.go`. The 1,000 persisted confirmed lines and the shift-close document are later slice of PSG-31. |
| AC-31.14 hazmat segregation FCS-02 | built (simulation) | `TestEvaluateWithCompliance_SegregationAndLimits` and `TestSimulate_HazmatComplianceExcludesIncompatibleSection`: FCS-02 is excluded and the trace reads `hazmat: class 3 vs 5.1 prohibited in FCS-02`. The web simulator runs the same scenario ("Load hazmat example"). Hazmat data is supplied per request until profiles are persisted (deliverable 10 row). |
| AC-31.15 transfer LP integrity WMS-0117 | later slice of PSG-31 | Transfer receipt is not built. |
| AC6 `go vet`/`go test`, OpenAPI/AsyncAPI match code, chart lints | built | `TestOpenAPIDocumentsEveryOperation` walks every served route (with a store) against `api/openapi.yaml` and its duties; `TestAsyncAPIDocumentsEveryEvent` checks each published type and payload field. | Web: `npx vitest --run` and `npx tsc --noEmit` in the module. |

## Directive engine gaps (slice 1)

| Item | Status | Reason |
|---|---|---|
| `fifo`/`lifo`/`fefo` ordering by quant receipt date or expiry | later slice of PSG-31 | Needs persisted quants with dates. The strategies are accepted and order by pick sequence. |
| `match_packing_quantity`, `round_up_to_lp` (full-LP quantities first), `outbound_staging` | later slice of PSG-31 | Need the persisted packaging specification of the line (FR-31.17; slice 2 has `DetermineSpec` and `RoundUpToLP` but the engine does not receive a specification) and staging locations (FR-31.13). Accepted; order by pick sequence. |
| Split pick of one line across locations | later slice of PSG-31 | A PICK candidate must hold the whole line quantity. |
| Unit conversion to the line's UoM, `location_query` beyond a zone, items+dims in `consolidate` | later slice of PSG-31 | Needs the wave-6 UoM API and the metadata query model; the query is equality on line attributes and an action filters by zone only. |
| In-progress PUT lines counted in remaining capacity in the simulator (BR-31.2) | later slice of PSG-31 | Work creation counts every open line (`Reserve`); the stateless simulator does not read work, so its caller passes `used`. |

## Persistence, events and runtime

| Item | Status | Reason |
|---|---|---|
| Quant model (LP, dims, quality status, receipt and expiry dates), optimistic `version` with 3 retries on quant updates (BR-31.8) | later slice of PSG-31 | Stock is `location_stock` (location × item quantity). Confirmations lock the location row (`FOR UPDATE`) instead of retrying an optimistic version. |
| `work.created` payload `wave_id`; events `wave.released.v1`, `load.shipped.v1`, `receipt.registered.v1` | later slice of PSG-31 | `work.created.v1` and `work.completed.v1` are published (outbox, AsyncAPI). Waves, loads and receipts do not exist yet. Cancelling work publishes no event (the PRD names none). |
| Temporal `WaveReleaseWorkflow` and scheduled jobs (replenishment, count plans, reconciliation, KPI materialization) | later slice of PSG-31 | No workflows yet. |
| Quant ⇄ inventory ledger reconciliation (§6.31.16) | later slice of PSG-31 (store) and handoff (wave 16 ledger projection) | The ledger and quant projection are wave 16 contracts. Wave 16 owns the ledger. |
| Feature flag `wms.advanced` and per-warehouse settings (§6.31.13) | later slice of PSG-31 | No per-warehouse settings store. `WMS_LOCK_TIMEOUT_MIN` is a process setting (5–240, default 30) rather than per warehouse. |
| Directive trace store with 30-day retention | later slice of PSG-31 | The trace is returned in the response only. |
| OPA duty check beyond the local `X-Passage-Duties` header | handoff (platform kernel, wave 2) | `authz` uses the local `DutyPolicy` until the kernel swaps in OPA. Duties follow `<service>.<area>.<verb>`: `warehouse.setup.view` / `.setup.maintain` / `.work.supervise` / `.work.execute` / `.inquiry.view` are the PRD's WMS_SETUP_VIEW, WMS_SETUP_MAINTAIN, WMS_WORK_SUPERVISE, WMS_WORK_EXECUTE and WMS_INQUIRY. The segregation of duties in §6.31.17 is not enforced here. |
| Warehouse id checked against the organization service (wave 6/8) | handoff (org) | Locations, directives and work take any warehouse UUID; there is no org client or warehouse projection yet. |
| Work number sequence WRK-* (wave 5) | handoff (numbering) | Work is identified by its UUID. |
| Web BFF route `/api/v1/warehouse/*` → service `/v1/warehouse/*` | handoff (web BFF / deploy) | The web module calls `/api/v1/warehouse/...` like every module; the BFF and ingress routing live outside this wave's paths. |

## Web (`apps/web/src/modules/warehouse`)

| Item | Status | Reason |
|---|---|---|
| Warehouse setup workspace: profiles, zones and formats as records, editing a location | later slice of PSG-31 | Slice 3 has the Locations list (capacity, use, stock; zone filter; paging) and Add locations (names generated from a format, one capacity profile). Profiles, zones and formats are not records, and locations cannot be edited (no API). |
| Location directive designer: structured band and action editor, reorder, edit, simulate a saved directive | later slice of PSG-31 | Slice 3 has the directive list and a New directive form whose bands are JSON. The simulator still takes directives as JSON rather than loading the saved ones. |
| Work list supervisor actions (reassign, change priority, bulk cancel) and location names on work lines | later slice of PSG-31 | Slice 3 has the work list (status filter, assign next work to me), Create work (with where each line went and the last rule), and work details (confirm, short pick, cancel). Supervisor actions need their API. Lines show the location id's first block because there is no location lookup by id yet. |
| Pages for LPs and on-hand by location/LP | later slice of PSG-31 | Each needs its API (LPs and `GET /quants` are not built). |
| Hazmat profile, segregation matrix and section maintenance pages; hazmat inventory by section | later slice of PSG-31 | Slice 2 adds hazmat data to the simulator only (a JSON field and the AC-31.14 example); the maintenance pages arrive with `/hazmat-profiles`. |
| Cross-dock template, packaging specification and labor standard/performance pages | later slice of PSG-31 | Their domain functions have no endpoint yet; each page arrives with its API. |
| Wave workbench, load planning, receiving | later slice of PSG-31 | Each needs its API. |
| Personalization (saved filters) and bulk priority change | handoff (web shell, wave 5) | Shell-owned personalization. |
| Playwright E2E for wave release | later slice of PSG-31 | No wave release yet. |

## Documents and reports

| Item | Status | Reason |
|---|---|---|
| Pick list, load manifest, count sheet, cross-dock tag, kit ticket, hazmat list, labor document | later slice of PSG-31 | Printing depends on the wave-32 print service and on the documents above. |
| Cube KPI models and dashboards | handoff (analytics) | Analytics owns the semantic layer. |

## Operations

| Item | Status | Reason |
|---|---|---|
| Helm chart `passage-wms` multi-deployment layout (`wms-api`, `wms-directive-engine`, `wms-worker`), HPA, CNPG `wms-db` | later slice of PSG-31 | One deployment (`warehouse`) reading `DATABASE_URL` from the `databaseSecret` secret (default `warehouse-db`). The DB cluster is wave-4 platform infrastructure. |
| Alerts (`WmsWorkBlockedNoLocation`, `WmsAssignmentP95`, `WmsQuantLedgerDrift`, `WmsWaveWorkflowFailed`) | handoff (deployment-cicd) | Alert rules live in the deploy tree, which this wave does not own. |
| Demo warehouse re-modeled with locations and directives (ticket Deploy; fixtures `wms-demo-warehouse` 8,000 locations, `wave-500-orders`, `asn-3-pallet.json`, `sim-10k-lines-day`) | later slice of PSG-31 | Fixtures are generated from the store and wave-28 order data. No `testdata/` datasets exist yet; the scenario table is in Go. |
| Migration from AX / SAP (§6.31.11) | later slice of PSG-31 | Needs the persisted model and the migration framework (handoff: migration-framework). |
| Country compliance packs (TRGS 510, NFPA 30) | handoff (wave 48) | Packs supply the segregation matrix through `WarehouseCompliancePack`. |
| Labor privacy OPA bundle `LaborPrivacyPolicy` | handoff (edge-identity, OPA policies) | Policies live under `policies/`, which this wave does not own. |
| GS1 company prefix for SSCC generation | handoff (wave 8 configuration) | Slice 1 generates SSCC-18 from a caller-supplied prefix; the tenant prefix comes from wave 8. |
