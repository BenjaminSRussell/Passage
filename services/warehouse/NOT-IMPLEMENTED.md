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
for locations, directives and work. Slice 4 adds dock doors and appointments
with the 15-minute grid and the overlap rule (WMS-0108, AC-31.8), and ASN
receiving: register the ASN's pallets, then receive each pallet with its
variance and discrepancy (BR-31.10, AC-31.7) into PURCHASE_PUTAWAY work, with
`receipt.registered` through the outbox; and the receiving and dock schedule
pages. Everything below is missing and is listed with its reason. Status is
`later slice of PSG-31` (same wave, not yet built) or `handoff` (needs another owner).

## Deliverables (ticket list 1–10)

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Location model: LocationProfile and Zone as entities, LocationFormat persisted, location update, LPs and nested handling units in Postgres | later slice of PSG-31 | Slice 3 persists locations (`POST /locations:bulk`, `GET /locations`) with capacity limits, status, zone and pick sequence on each location, and their stock by item (`location_stock`, no LP or dims). Not built: a separate LocationProfile and Zone record (limits are copied per location), parsed aisle/rack/shelf/bin columns, updating or deactivating a location, the 1,000,000-location limit check, and the `license_plate` table with nesting (the LP id and depth rules exist in `lp.go`). The web page generates names from a format but the format is not stored. |
| 2 | Rules engine: directive versioning and effective dating, update and delete, work templates, wave templates, replenishment templates | later slice of PSG-31 | Slice 3 persists directives (`POST`/`GET /location-directives`) and work creation evaluates them. Directives cannot be changed or removed yet, have no version or effective date, and the `multiple_sku` flag is not modeled. Work templates (pick/put pairs, work class, work breaks, max lines) and wave templates are not modeled, so a work's lines are exactly the request's lines. |
| 3 | Work execution: work class and user filters on assignment, skip location, configurable over-pick tolerance, cancel-with-reversal, supervisor reassign/unassign/priority change, blocked-work re-evaluation | later slice of PSG-31 | Slice 3 builds create, list, read, assign (best priority, then oldest; `FOR UPDATE SKIP LOCKED`; idle ASSIGNED work returns to OPEN after `WMS_LOCK_TIMEOUT_MIN`), confirm (assignee only, row locks, capacity and mixing re-checked), short pick with reason and a child remainder work, and cancel with reason. Not built: work classes and user filters (FR-31.4/31.7), skip location (`SKIPPED` line status), over-pick tolerance above 0% (needs per-warehouse/item settings; BR-31.7 serial and quality exclusions with it), confirming another LP or location than planned, cancel-with-reversal of confirmed lines (compensating MOVEMENT work), supervisor reassign/unassign/priority change, and re-evaluating BLOCKED_NO_LOCATION work to OPEN. A work holds no wave, load or order reference yet. |
| 4 | Load planning, dock appointments, ASN-driven receiving with LPs | partly built | Slice 4 builds dock doors, appointments (15-minute grid, at most 24 h, no overlap on a door; create and list) and ASN receiving (register the packing structure; receive each pallet; variance and discrepancy; put-away work per pallet; `receipt.registered`). Not built, later slice of PSG-31: loads and load building from shipments (manual, rule, route), load confirmation posting packing slips through the wave-28 API, `load.shipped.v1`, assigning a load to a door, moving or cancelling an appointment, and closing a receipt (marking pallets that never arrived as −100% discrepancies). Receipt without an ASN (item + qty + LP) is not built. The LP stays an attribute of the put-away line: no `license_plate` row or quant is created at receipt, and nested LPs inside an ASN pallet (cases on a pallet) are not modeled, because the LP and quant tables are not built (row 1). Door calendars (opening hours, closures) are handoff (wave 6 calendars). The ASN tolerance is per receipt (request field, default 2%), not a warehouse or vendor setting (§6.31.13), until settings exist. |
| 5 | Slotting analysis and warehouse KPIs | later slice of PSG-31 (KPIs), later slice of PSG-31 (slotting) | Needs historical picks and the Cube semantic layer from wave 5/analytics. |
| 6 | Outbound sorting (sort templates, sort positions, sort-station scan); persisted cross-dock templates, links and CROSS_DOCK work | later slice of PSG-31 | Slice 2 has the pure receipt allocation `PlanReceipt` (planned links, then opportunistic demand in window, with quality hold); no endpoint or screen calls it yet, because the PRD API (`POST /cross-dock-templates`) and the receipt registration it runs at need the store. Persisted templates and links, CROSS_DOCK work creation, the re-plan job and sort positions are not built. The `outbound_staging` strategy name is accepted by the engine. |
| 7 | Value-added services and kitting (VAS orders, kit-to-order, kit-to-stock, disassembly) | later slice of PSG-31 | Needs VAS orders, kit BOMs from wave 9 and the wave-16 posting path. Packaging specifications are built as domain functions (`DetermineSpec`, `RoundUpToLP`) with no endpoint or screen; their persistence and maintenance API, the freeze on work lines (BR-31.13), the `packaging_spec_required` block, the `round_up_to_lp` engine strategy using them, cartonization and material consumption through wave 16 are not. |
| 8 | Transfer-order warehouse flow with LP identity across sites | later slice of PSG-31 | Needs wave-16 transfer orders and the in-transit ledger. |
| 9 | Engineered labor standards and performance reporting | later slice of PSG-31 | Slice 2 has the formula (`Formula.Planned`), travel metrics, `Actual` from timestamps and pauses, `LaborLine` with the outlier band and its WMS-0118 text, and `Performances` per group, as domain functions with no endpoint or screen. The formula is a fixed linear shape, not a wave-3 expression. Versioned, effective-dated standards, the `/labor/standards` and `/labor/performance` endpoints, persisted confirmations, shift close and performance documents (LPD-*) are not built. Wave-32 step telemetry is not wired. Indirect activity codes and the GRAPH travel metric are not built. |
| 10 | Hazardous-material profiles and storage compatibility: persistence and maintenance | later slice of PSG-31 | Slice 2 built the storage check (`EvaluateWithCompliance`, BR-31.15) and the simulate `compliance` block. Not built: persisted `HazmatProfile`, segregation matrix and `FireContainmentSection` with `POST /hazmat-profiles` (duty WMS_HAZMAT_MAINTAIN), the tenant matrix version `hazmat_segregation_matrix`, section limits in litres (only kg), the hazmat-inventory-by-section inquiry (LX06/LX24) and its list, and WMS-0115/WMS-0116 as HTTP errors on work confirmation (they appear as trace reasons only). Section quantity is the used weight of locations holding a profiled item, because stock carries quantities, not per-item weights, until quants are persisted. Work creation (slice 3) runs the directives without hazmat data, because no profile is persisted: a hazmat put-away is only checked in the simulator. |

## Acceptance criteria (ticket and PRD §6.31.8)

| Criterion | Status | Reason |
|---|---|---|
| AC1 / AC-31.1 directive fixtures: 100 scenarios | built (inbound and outbound) | `testdata/directive-scenarios-100.json` holds 100 put-away scenarios with hand-derived location, directive, line and action; `TestSimulate_DirectiveFixture` posts each to the simulate endpoint. `TestEvaluate_OutboundPickFixture` adds 100 PICK scenarios (10 quantities × 10 stock layouts) whose expectation is derived by hand in the test, not from the engine. Not built: `fifo`/`fefo` pick scenarios (need quant dates) and the review against D365 USMF behavior. |
| AC2 simulation: 10k lines/day, p95 assignment < 1 s, no deadlocks | built | `TestStore_Simulation10kLinesPerDay`: 10 waves × 50 orders × 20 lines released through `ReleaseWave` with min/max replenishment while 10 concurrent pickers work the queue (per-call 30 s deadlines); no failed call, every work completed, forward + reserve stock conserved, forward within 1..max, assignment p95 asserted under 1 s (measured about 6 ms, max 12 ms). The default run is a tenth of the day; `PASSAGE_WMS_SIM_10K=1` runs all 10,000 lines (about 7 s). The fixture is generated in the test, not a `sim-10k-lines-day` dataset. |
| AC3 capacity never exceeded (property) | built | Property tests cover receive sequences (`location_test.go`) and mixed receive/issue sequences through `Location.Move`, the path confirmations use (`execution_test.go`); confirmation re-checks capacity under a row lock. Planning reserves open lines but takes no lock, so two concurrent plans can target the same room; the later confirmation is then refused (WMS-0102) rather than exceeding capacity. |
| AC4 wave release of 500 orders creates correct work and replenishment | built (without a persisted wave) | `ReleaseWave` (domain) and `Store.ReleaseWave`: `TestReleaseWave_500Orders` and `TestStore_ReleaseWave500Orders` release 500 orders into 500 pick works and exactly the min/max replenishment the picks need, then execute everything and check the stock. `TestReleaseWave_NeverOverdrawsAnyLocation` is the property that no location is asked for more than it holds. Not built: a wave record and `wave_id` on work, wave templates, the Temporal `WaveReleaseWorkflow`, the HTTP route and the web wave workbench, replenishment by demand or wave (only min/max), the refill is sized for the unmet demand of every order, so it can exceed the maximum by the demand of an order that stays blocked after the refill (reserve short, or another line unplaceable), LP usage on refill lines (weight and volume are carried and cap the refill), and replenishment targets chosen by slotting (a pick location is refilled when it held the item before the wave). |
| AC-31.2 max_lps = 1 trace `capacity: lps 1/1` | built | `TestEvaluate_BlockedTraceExplainsRejection`. |
| AC-31.3 wave release 500 orders, 41 replenishments, < 120 s | partly built | 500 orders release in about a second with the replenishment count the fixture derives (10 here, not the demo warehouse's 41, which needs the `wave-500-orders` dataset). Temporal and the HTTP route are not built. |
| AC-31.4 50 concurrent `work:assign`, no duplicates | partly built | `FOR UPDATE SKIP LOCKED` assignment; the store test runs 25 concurrent callers over 20 works. The 50-user, 1,000-work run with the p95 < 1 s measurement is not built. |
| AC-31.5 short pick to PARTIALLY_COMPLETED with 4 EA remainder | built (warehouse side) | `TestStore_ShortPick`: the pick of 10 short picked at 6 with `EMPTY` is PARTIALLY_COMPLETED, a child OPEN work of 4 EA references it, and the location loses exactly 6. The inventory ledger posting of the 6 EA transfer is wave 16's consumer of `work.completed` (handoff). |
| Wave 33 AC2: pick of held quantity refused with `QUALITY_HOLD` | built | Confirm and short-pick of a PICK line claim the picked quantity at inventory for the whole warehouse (`POST /v1/inventory/issue-claims`, duty `inventory.issue.claim`, kind PICK, key per work line, quantity and attempt; held quantity and stock are both counted warehouse-wide, not per location). Inventory takes the claim under the product's on-hand row locks and refuses when it would reach held stock, counting the claims of every kind already taken (#33), so repeated or concurrent picks, ships and consumptions cannot add up to held stock and a hold placed meanwhile is seen. A PICK line's `issueRef` attribute is sent as the claim's ref, so the later ship or consumption of what was picked counts once and the DELIVERED booking of the order line settles pick and ship together (`TestStore_PickClaimsUnderTheLinesIssueRef`). `POST /v1/warehouse/work` takes `orderLineId` on a PICK line and sets the attribute to `SALES_LINE:<id>` (`TestPersistedRoutes_PassTheCommand`); the web Create work form sets it from its "sales order line" field on a pick line. Fulfillment has no code that creates warehouse work and `domain.ReleaseWave` has no route, so a document-driven pick exists only when the caller passes `orderLineId`. A pick with no source document (ad hoc) has no ref: it counts on its own and no issue settles it, so it is released explicitly at inventory (`POST /v1/inventory/issue-claims:release`). Work has no step after COMPLETED where picked units are handed on (no shipment or load), and releasing at completion would let repeated picks add up to held stock, so the warehouse does not. A ref in `attributes.issueRef` must be canonical (`SALES_LINE:<lower-case uuid>` or `PRODUCTION_ORDER:<uuid>:<uuid>`), else 400. A refusal is 422 `QUALITY_HOLD`; an inventory error or outage is 503 `COM-0503`; a principal without a legal entity is 403; a failed confirmation withdraws its claim. Item ids that are not UUIDs cannot be checked and are refused (warehouse item ids are free text: a deployment with `INVENTORY_URL` set must use product ids). Tests: `TestStore_PickRefusesHeldQuantity`, `TestStore_RepeatedPicksCannotEatHeldStock`, `TestStore_PickChecksTheWarehouseNotTheLocation`, `TestHoldProperty_PickNeverMovesHeldUntilUsageDecision`. PUT is not blocked. Without `INVENTORY_URL` the check is not wired. `WMS-0113` remains the cross-dock warning, not this refusal. |
| AC-31.6 pallet move moves 13 LPs, one `work.completed` | later slice of PSG-31 | Moves are not built. The nesting rule is tested. |
| AC-31.7 ASN receipt discrepancy −5 | built (quants: later slice of PSG-31) | `TestReceive_AC31_7_ShortPalletIsADiscrepancy`, `TestStore_ReceiveASNPallet` and `TestReceivingRoutes_DockAndASNFlow`: pallet 2 of 3 received at 95 of 100 records −5 (−5%), a WMS-0107 discrepancy, and put-away work for pallet 2 only; the 95 reach the location when the put-away is confirmed. "Quants exist for pallet 2 only" is met by the stand-in `location_stock`, not by quants per LP (row 1). |
| AC-31.8 `DOOR_SLOT_OVERLAP` | built | `TestCheckDoorSlot_AC31_8`, `TestStore_DockAppointments` and the HTTP flow test: 09:30–10:00 on D1 after 09:00–09:45 is 409 WMS-0108, whose text names DOOR_SLOT_OVERLAP. `TestStore_ConcurrentBookingsOfOneDoor` books one slot from 8 callers and exactly one wins. |
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
| `work.created` payload `wave_id`; events `wave.released.v1`, `load.shipped.v1` | later slice of PSG-31 | `work.created.v1`, `work.completed.v1` and (slice 4) `receipt.registered.v1` are published (outbox, AsyncAPI). Waves and loads do not exist yet. Cancelling work publishes no event (the PRD names none). `receipt.registered` is one event per received pallet; registering an ASN publishes none (the PRD names none). |
| ASN import from EDI 856 | handoff (wave 35 EDI) | The JSON API is the PRD's "now" path; wave 35 maps the 856 onto `POST /receipts:from-asn`. |
| Opportunistic cross-dock at receipt registration (FR-31.13) and quality inspection on receipt | later slice of PSG-31 / handoff (wave 33 quality) | `ReceiveLine` always creates put-away work; `PlanReceipt` (slice 2) is not called yet, and receipts carry no quality status. |
| Temporal `WaveReleaseWorkflow` and scheduled jobs (replenishment, count plans, reconciliation, KPI materialization) | later slice of PSG-31 | No workflows yet. |
| Quant ⇄ inventory ledger reconciliation (§6.31.16) | later slice of PSG-31 (store) and handoff (wave 16 ledger projection) | The ledger and quant projection are wave 16 contracts. Wave 16 owns the ledger. |
| Feature flag `wms.advanced` and per-warehouse settings (§6.31.13) | later slice of PSG-31 | No per-warehouse settings store. `WMS_LOCK_TIMEOUT_MIN` is a process setting (5–240, default 30) rather than per warehouse. |
| Directive trace store with 30-day retention | later slice of PSG-31 | The trace is returned in the response only. |
| OPA duty check beyond the local `X-Passage-Duties` header | handoff (platform kernel, wave 2) | `authz` uses the local `DutyPolicy` until the kernel swaps in OPA. Duties follow `<service>.<area>.<verb>`: `warehouse.setup.view` / `.setup.maintain` / `.work.supervise` / `.work.execute` / `.inquiry.view` / `.receipt.register` / `.shipment.confirm` are the PRD's WMS_SETUP_VIEW, WMS_SETUP_MAINTAIN, WMS_WORK_SUPERVISE, WMS_WORK_EXECUTE, WMS_INQUIRY, WMS_RECEIVE and WMS_SHIP_CONFIRM. The PRD gives dock appointments no duty of their own; an INBOUND booking needs the receive duty and an OUTBOUND one the ship-confirm duty. The segregation of duties in §6.31.17 (WMS_RECEIVE vs AP_INVOICE_APPROVE) is not enforced here. |
| Warehouse id checked against the organization service (wave 6/8) | handoff (org) | Locations, directives, work, doors and receipts take any warehouse UUID; there is no org client or warehouse projection yet. The ASN's item ids and PO line references are not checked against product or purchasing either. |
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
| Wave workbench, load planning | later slice of PSG-31 | Each needs its API. Slice 4 adds Receiving (list, Register ASN, receive pallets) and Dock schedule (a day's appointments, doors, Book a dock appointment). |
| Receiving: create LPs, print receipt labels; dock: week view, drag to reschedule | partly built | The warehouse now generates the pallet's SSCC and license plate id when it is received (migration `0006`, `PUT /v1/warehouse/gs1-settings`), keeps lot and expiry, and a device prints the label from `GET /v1/warehouse/pallets/{sscc}/label` (wave 32). LPs are still not persisted as nested units, and appointments cannot be moved yet. The schedule shows times in the browser's time zone, not the warehouse's, until the org warehouse projection gives its zone (handoff org). |
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
| GS1 company prefix for SSCC generation | built (wave 32) | The tenant's prefix and extension digit are set with `PUT /v1/warehouse/gs1-settings` (7 to 11 digits; locked once SSCCs were issued); the serial counter is drawn under a row lock. Moving the prefix into the wave 8 configuration is a later choice. |

## RFID and automation (#32, wave 32)

Built: `domain.DecodeEPC` for the 96-bit EPC schemes a warehouse reads (SGTIN, SSCC, SGLN, GRAI, GIAI, GSRN, GDTI; the SGTIN-96
vector of GS1 TDS is a test), `POST /v1/warehouse/rfid-reads:ingest` (up to 500 reads, a read that cannot be decoded is listed
with its position, a tag read again at the same reader within five seconds is a duplicate, an SSCC the warehouse labelled names
its receipt and pallet) and `GET /v1/warehouse/rfid-reads`; automation orders from transport work (`POST
/v1/warehouse/automation/transport-orders`, the line's pallet SSCC and two locations of at most 12 characters, the work assigned
to `automation:<station>`), the connector's pull and report, and `POST /v1/warehouse/automation/events`, where each PLC event is
stored once by station, link session and sequence (the PLC numbers telegrams per session, from 1 again after a reconnect, so a
repeat is the same session and number within ten minutes) and an arrival of a load unit with an order sent to the station confirms
the work line as the station's identity in the same transaction, keyed by the order (migration `0007`). Duties `warehouse.rfid.ingest` and `warehouse.automation.execute`.

Added in round 3 (migration `0008`): SGTIN-198, SGLN-195, GRAI-170, GDTI-174 and GIAI-202 with their 7-bit alphanumeric
serials (tags padded to a 16-bit word are accepted), GSRNP-96 and GID-96; the device app reads SGTIN-198 as a scan too.
RFID rules per reader (`/v1/warehouse/rfid-rules`, duties `warehouse.setup.view`/`warehouse.setup.maintain`):
each rule acts in one warehouse and names the identity that posts the reader's reads (a read posted by anyone else runs
no rule). RECEIVE_PALLET receives the expected ASN pallet whose SSCC or license plate a dock portal read, at the announced
quantity; CONFIRM_PUTAWAY confirms, as `rfid:<reader>`, the open put-away line of that pallet bound for the rule's
location (a put-away a person holds is left for that person). Each read records what its rules
did (DONE, SKIPPED, REFUSED with the reason); a refusal does not stop the batch. A PLC `fault` or `diverted` event notes the
reason on the work line and returns a work still ASSIGNED to the station to OPEN for people; the line can be given to the PLC
again. `POST /v1/warehouse/automation/transport-orders/{id}:cancel` (duty `warehouse.work.supervise`) cancels an order
that has not arrived (a SENT one only with `plcCleared`), with the same effect on the work.

| Item | Status | Reason |
|---|---|---|
| RFID reader hardware and the gateway that posts reads | human/infra (#213) | A Zebra or Impinj reader and its gateway are needed; the endpoint takes any reader that posts JSON. |
| CPI-96/var, SGCN-96, ITIP and ADI-var EPC schemes | later slice | Not read in a warehouse flow yet; rejected as WMS-EPC-INVALID. The device app scans SGTIN-96, SGTIN-198 and SSCC-96 only. |
| More RFID actions (raise an exception on an unexpected read, move a pallet between two locations on a portal read, ship-confirm at a dock door) | later slice | Rules receive a pallet and confirm its put-away only. There is no web screen for rules or for the action of a read; both are API only. |
| An IN_PROGRESS work after a fault or divert | later slice | A work the station already confirmed a line of stays with the station (it cannot go back to OPEN under BR-31.3); its open line gets the reason and can be given to the PLC again, but a person cannot take it over without a reassign command (not built). |
| A cancel telegram to the PLC, and a MOVE for an order the PLC never acknowledged | later slice / #213 | Cancelling a SENT order needs a person to clear it on the PLC (`plcCleared`); the connector sends no cancel telegram because the vendor layout is unknown. An order whose MOVE got no acknowledgement stays pending and is offered again after the connector reconnects. No web screen lists or cancels automation orders. |
| A real vendor's telegram layout | human/infra (#213) | Needs a PLC. |
