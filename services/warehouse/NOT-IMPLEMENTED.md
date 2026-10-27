# NOT-IMPLEMENTED — warehouse (PSG-31, wave 31)

Slice 1 builds the pure directive engine, the location and license-plate rules,
the work state machine, one stateless simulate endpoint, the web simulator page,
the chart and the contracts. Everything below is missing and is listed with its
reason. Status is `later slice of PSG-31` (same wave, not yet built) or `handoff`
(needs another owner).

## Deliverables (ticket list 1–10)

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Location model persisted: zones, aisles, racks, bins, profiles, LPs and nested handling units in Postgres | later slice of PSG-31 | Slice 1 has no store or migrations. The domain types, admission rules and LP id/nesting rules exist and are tested. |
| 2 | Rules engine: directives as metadata (versioned, effective-dated), work templates, wave templates, replenishment templates | later slice of PSG-31 | Directives are evaluated from request input only. No persistence, versioning or designer API. Work and wave templates are not modeled. |
| 3 | Work execution: tasks, priorities, user assignment (`FOR UPDATE SKIP LOCKED`), exceptions, short picks | later slice of PSG-31 | Requires the store. The state machine (`CheckTransition`) exists; assignment and confirmation endpoints do not. |
| 4 | Load planning, dock appointments, ASN-driven receiving with LPs | later slice of PSG-31 | Requires the store and the wave-28 shipment and wave-18 receipt contracts. |
| 5 | Slotting analysis and warehouse KPIs | later slice of PSG-31 (KPIs), later slice of PSG-31 (slotting) | Needs historical picks and the Cube semantic layer from wave 5/analytics. |
| 6 | Cross-docking (planned and opportunistic) and outbound sorting | later slice of PSG-31 | Needs CROSS_DOCK work, the `outbound_staging` strategy in persisted form, and sort positions. The strategy name is accepted by the engine. |
| 7 | Value-added services and kitting; packaging specifications | later slice of PSG-31 | Needs VAS orders, kit BOMs from wave 9 and the wave-16 posting path. |
| 8 | Transfer-order warehouse flow with LP identity across sites | later slice of PSG-31 | Needs wave-16 transfer orders and the in-transit ledger. |
| 9 | Engineered labor standards and performance reporting | later slice of PSG-31 | Needs confirmed work lines with timestamps (store) and wave-32 step telemetry. |
| 10 | Hazardous-material profiles and storage-class segregation | later slice of PSG-31 | The engine has the hook point (admission per location). HazmatProfile, the segregation matrix and section limits are not modeled. |

## Acceptance criteria (ticket and PRD §6.31.8)

| Criterion | Status | Reason |
|---|---|---|
| AC1 / AC-31.1 directive fixtures: 100 scenarios | partly built | `testdata/directive-scenarios-100.json` holds 100 put-away scenarios (10 quantities × 10 occupancy layouts) with hand-derived location, directive, line and action; `TestSimulate_DirectiveFixture` posts each to the simulate endpoint. Missing: PICK (outbound) scenarios, which need quant dates for `fifo`/`fefo`, and the review of the fixture against D365 USMF behavior. Later slice of PSG-31. |
| AC2 simulation: 10k lines/day, p95 assignment < 1 s, no deadlocks | later slice of PSG-31 | No assignment or work runtime in slice 1. |
| AC3 capacity never exceeded (property) | partly built | The property test covers receive sequences on one location (`location_test.go`). The work-confirmation and persistence paths come later. |
| AC4 wave release of 500 orders creates correct work and replenishment | later slice of PSG-31 | No waves, Temporal workflow or replenishment. |
| AC-31.2 max_lps = 1 trace `capacity: lps 1/1` | built | `TestEvaluate_BlockedTraceExplainsRejection`. |
| AC-31.3 wave release 500 orders, 41 replenishments, < 120 s | later slice of PSG-31 | Wave release is not built. |
| AC-31.4 50 concurrent `work:assign`, no duplicates | later slice of PSG-31 | Assignment is not built. |
| AC-31.5 short pick to PARTIALLY_COMPLETED with 4 EA remainder | later slice of PSG-31 | Requires persisted work. The transition rule (BR-31.3) is tested in `work_test.go`. |
| AC-31.6 pallet move moves 13 LPs, one `work.completed` | later slice of PSG-31 | Moves are not built. The nesting rule is tested. |
| AC-31.7 ASN receipt discrepancy −5 | later slice of PSG-31 | Receiving is not built. |
| AC-31.8 `DOOR_SLOT_OVERLAP` | later slice of PSG-31 | Dock appointments are not built. |
| AC-31.9 property over 10,000 work/confirmation sequences | later slice of PSG-31 | Requires the work and confirmation paths. Capacity-only half is partly built (see AC3). |
| AC-31.10 cross-dock to outbound staging, ATP counts once | later slice of PSG-31 | Cross-dock allocation is not built. |
| AC-31.11 sort positions | later slice of PSG-31 | Sort templates are not built. |
| AC-31.12 kit posting atomic, WMS-0114 | later slice of PSG-31 | VAS and kit posting are not built. |
| AC-31.13 labor standard fixture to 0.1 s | later slice of PSG-31 | Labor standards are not built. |
| AC-31.14 hazmat segregation FCS-02 | later slice of PSG-31 | Hazmat is not built. |
| AC-31.15 transfer LP integrity WMS-0117 | later slice of PSG-31 | Transfer receipt is not built. |
| AC6 `go vet`/`go test`, OpenAPI/AsyncAPI match code, chart lints | built (see slice 1 checks) | Web: `npx vitest --run` and `npx tsc --noEmit` in the module. |

## Directive engine gaps (slice 1)

| Item | Status | Reason |
|---|---|---|
| `fifo`/`lifo`/`fefo` ordering by quant receipt date or expiry | later slice of PSG-31 | Needs persisted quants with dates. The strategies are accepted and order by pick sequence. |
| `match_packing_quantity`, `round_up_to_lp` (full-LP quantities first), `outbound_staging` | later slice of PSG-31 | Need packaging specifications (FR-31.17) and staging locations (FR-31.13). Accepted; order by pick sequence. |
| Split pick of one line across locations | later slice of PSG-31 | A PICK candidate must hold the whole line quantity. |
| Unit conversion to the line's UoM, `location_query` beyond a zone, items+dims in `consolidate` | later slice of PSG-31 | Needs the wave-6 UoM API and the metadata query model; the query is equality on line attributes and an action filters by zone only. |
| In-progress PUT lines counted in remaining capacity (BR-31.2) | later slice of PSG-31 | Needs persisted work; the caller passes `used`. |

## Persistence, events and runtime

| Item | Status | Reason |
|---|---|---|
| `migrations/` with RLS, `tenant_id` first, outbox, idempotency tables | later slice of PSG-31 | No store yet. Idempotency-Key handling is not needed for the stateless simulate endpoint (it is a read). |
| Events `dev.passage.wms.work.created.v1`, `work.completed.v1`, `wave.released.v1`, `load.shipped.v1`, `receipt.registered.v1` | later slice of PSG-31 | No state changes yet. The AsyncAPI file says so explicitly. |
| Temporal `WaveReleaseWorkflow` and scheduled jobs (replenishment, count plans, reconciliation, KPI materialization) | later slice of PSG-31 | No workflows yet. |
| Quant ⇄ inventory ledger reconciliation (§6.31.16) | later slice of PSG-31 (store) and handoff (wave 16 ledger projection) | The ledger and quant projection are wave 16 contracts. Wave 16 owns the ledger. |
| Feature flag `wms.advanced` and `WMS_*` config values beyond the defaults | later slice of PSG-31 | No per-warehouse settings store. |
| Directive trace store with 30-day retention | later slice of PSG-31 | The trace is returned in the response only. |
| OPA duty check beyond the local `X-Passage-Duties` header | handoff (platform kernel, wave 2) | `authz` uses the local `DutyPolicy` until the kernel swaps in OPA. Duty `warehouse.setup.view` is the PRD's `WMS_SETUP_VIEW`. |
| Web BFF route `/api/v1/warehouse/*` → service `/v1/warehouse/*` | handoff (web BFF / deploy) | The web module calls `/api/v1/warehouse/...` like every module; the BFF and ingress routing live outside this wave's paths. |

## Web (`apps/web/src/modules/warehouse`)

| Item | Status | Reason |
|---|---|---|
| Warehouse setup workspace (locations grid, profiles, zones, formats) | later slice of PSG-31 | Needs the persisted location API. |
| Location directive designer with save | later slice of PSG-31 | Needs the directive API. Slice 1 simulates a line entered in a form against directives and locations pasted as JSON. |
| List and detail pages for locations, profiles, LPs, directives, work | later slice of PSG-31 | Nothing is persisted in slice 1, so there is nothing to list; each page arrives with its API. |
| Wave workbench, work list, load planning, receiving, on-hand inquiry | later slice of PSG-31 | Each needs its API. |
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
| Helm chart `passage-wms` multi-deployment layout (`wms-api`, `wms-directive-engine`, `wms-worker`), HPA, CNPG `wms-db` | later slice of PSG-31 | Slice 1 ships one deployment (`warehouse`). The DB cluster is wave-4 platform infrastructure. |
| Alerts (`WmsWorkBlockedNoLocation`, `WmsAssignmentP95`, `WmsQuantLedgerDrift`, `WmsWaveWorkflowFailed`) | handoff (deployment-cicd) | Alert rules live in the deploy tree, which this wave does not own. |
| Demo warehouse re-modeled with locations and directives (ticket Deploy; fixtures `wms-demo-warehouse` 8,000 locations, `wave-500-orders`, `asn-3-pallet.json`, `sim-10k-lines-day`) | later slice of PSG-31 | Fixtures are generated from the store and wave-28 order data. No `testdata/` datasets exist yet; the scenario table is in Go. |
| Migration from AX / SAP (§6.31.11) | later slice of PSG-31 | Needs the persisted model and the migration framework (handoff: migration-framework). |
| Country compliance packs (TRGS 510, NFPA 30) | handoff (wave 48) | Packs supply the segregation matrix through `WarehouseCompliancePack`. |
| Labor privacy OPA bundle `LaborPrivacyPolicy` | handoff (edge-identity, OPA policies) | Policies live under `policies/`, which this wave does not own. |
| GS1 company prefix for SSCC generation | handoff (wave 8 configuration) | Slice 1 generates SSCC-18 from a caller-supplied prefix; the tenant prefix comes from wave 8. |
