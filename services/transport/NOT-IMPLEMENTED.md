# NOT-IMPLEMENTED: transport (PSG-34, wave 34, slices 1 to 4)

Every ticket deliverable and acceptance criterion that slices 1 to 4 do not build, with its status.
Paths below use the PRD's `/tms/v1/...` names; the service mounts them under `/v1/transport/...`.
Status is either `later slice of PSG-34` (same wave, next slice) or `handoff` (needs another
owner's work, or a change outside this wave's paths).

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| D1 | Manual load planning: add or move a shipment by hand, the over-capacity warning with a logged planner override (BR-34.8 first half, TMS-0103) | later slice of PSG-34 | Slice 3 builds rule-based building (`POST /v1/transport/loads:build`, hard split, TMS-0115, TMS-0116); there is no endpoint that edits a load |
| D1 | TMS shipments created from sales, transfer and purchase shipments (FR-34.1) | handoff (wave 28 shipping, purchasing receipts) | The build request carries each shipment's site, region, mode, pickup date, weight, volume and pallets; nothing consumes the shipment events yet or checks the ids |
| D1 | Date window grouping (FR-34.1 "date window") and time-window constraints | later slice of PSG-34 | Loads group on the exact pickup date |
| D1 | Load status moves after PLANNED (RATED, TENDERED, ACCEPTED, DISPATCHED, IN_TRANSIT, DELIVERED, CLOSED, CANCELLED) and the `version`/`If-Match` on load commands | later slice of PSG-34 | Loads stay PLANNED; tenders and tracking events do not update the load row yet |
| D1 | Route planning endpoint `POST /tms/v1/routes:optimize` (FR-34.2) | later slice of PSG-34 | The solver exists in `services/routing-py`; the Go-to-optimizer HTTP client, request hash idempotency and route persistence are not built |
| D1 | Load planning workbench map, route sequence and drag-and-drop between loads (§6.34.6) | later slice of PSG-34 | The web module has the load list, the build-loads form and the load detail; no route or map screens |
| D2 | Rate shopping cache (10 min per input hash, FR-34.4) and route guides with fallbacks | later slice of PSG-34 | Rate shop is computed per call over every card of the tenant; no cache, route guide entity or GiST validity index |
| D2 | Weighted ranking by transit time or score (FR-34.4) | later slice of PSG-34 | Options rank by total only; transit days are not modelled on carrier services |
| D2 | Fuel index (TMSFuelIndex): weekly index effective on the ship date, `fuel_index_id` on the card (BR-34.2) | later slice of PSG-34 | The card carries a fixed `fuelPct`; there is no fuel index table or weekly import |
| D2 | Currency conversion of a quote to the shipment currency at wave-12 rate type FREIGHT (BR-34.2) | handoff (wave 12 FX) | Quotes are in the card currency; no FX client in this service |
| D2 | Class-based chargeable weight for LTL and the parcel DIM factor in in³/lb per carrier service (BR-34.2, `dim_factor_parcel`) | later slice of PSG-34 | Dimensional weight uses one metric divisor per card (cm³/kg, default 5000) |
| D2 | Rate card publish flow (draft, approve) and `dev.passage.tms.shipment.rated.v1` | later slice of PSG-34 | Cards are active when saved; rating publishes no event |
| D2 | Service-area, mode, hazmat and max-weight eligibility filter (FR-34.4) | later slice of PSG-34 | Carrier service master data is owned by the carrier/party work; eligibility fields not modelled |
| D2 | Temporal `TenderWorkflow` per load with timers (FR-34.5, §6.34.16 "Tender expiry") | later slice of PSG-34 | Slice 2 builds the cascade, the start/respond endpoints and the expiry sweep `POST /v1/transport/tenders:expire`; deadlines advance when the sweep runs or a carrier responds. No worker calls the sweep on a schedule yet, so a planner runs it from the tender board |
| D2 | Tender ranks from the route guide (BR-34.3 "Rank from route guide") and `tender_timeout_min` per carrier or route guide (§6.34.13) | later slice of PSG-34 | The caller sends the carriers in rank order and one timeout per tender; there is no route-guide entity yet (see the rate-shopping row) |
| D2 | Broadcast tendering option (first accept wins) and the tender board's planner override (§6.34.6) | later slice of PSG-34 | Only the sequential cascade is built; a planner cannot skip or force a carrier |
| D2 | Tender history filters by carrier, outcome and period (§6.34.14) | later slice of PSG-34 | The list filters by status and load |
| D2 | EDI 204 tender send and EDI 990 response receipt | handoff (wave 35 EDI) | Wave 35 maps 204/990 onto `tender.*` events and `:respond`; nothing is sent to carriers from this service |
| D2 | Planner task when a tender fails (BR-34.3, TMS-0106) | handoff (workflow tasks) | The tender carries `plannerTask: true` and the board shows it; creating a task in the workflow inbox needs that service's API |
| D2 | Accepted tender sets the load's carrier and status ACCEPTED (§6.34.4 Load) | later slice of PSG-34 | Slice 3 adds the load table, but tenders still take any `loadId` (loads built outside this service, such as by wms, are tendered too) and do not update the load |
| D2 | Denied-party screening gate before a tender is sent (BR-34.17, AC-34.16) | later slice of PSG-34 | The DG gate is built: a load whose latest stored DG check is FAIL is refused with TMS-0130 and no tender is stored (AC-34.13). Screening decisions are not stored, so there is no hold to check (see D10) |
| D3 | EDI 214 codes beyond X3, AF, X1 and D1 (for example AG estimated delivery, X6 en route, SD delays) | later slice of PSG-34 | Ingest rejects other codes rather than guessing their stop status |
| D3 | Expected vs actual per stop and late-delivery alerts when ETA > window end (FR-34.6) | later slice of PSG-34 | Needs the planned stops and time windows of the load and route (D1); the timeline shows actuals only |
| D3 | Proof of delivery upload (signature, photo) | later slice of PSG-34 | No file storage in this service yet |
| D3 | Carrier API adapters and EDI 214 parsing | handoff (wave 35 EDI, carrier integrations) | They post the parsed status to `POST /v1/transport/tracking-events` |
| D3 | POD and delivered timestamp on the sales shipment (AC-34.7 second half, wave-29 invoicing holds) | handoff (wave 28/29 sales) | `dev.passage.tms.tracking.event.v1` carries the D1; the sales side consumes it |
| D3 | `tracking_event` monthly partitions and Kafka buffering (§6.34.19) | later slice of PSG-34 | One table with a `(tenant_id, load_id, occurred_at)` index |
| D4 | Freight bill created at ship confirmation (FR-34.7) | later slice of PSG-34 | Slice 1 creates bills by API; the ship-confirmation trigger needs the shipment event from wave 28 |
| D4 | SETTLED when the AP invoice is posted (BR-34.4) | handoff (wave 19 AP) | Slice 3 builds `:approve` (TMS_RECONCILE_APPROVE, If-Match) and publishes `dev.passage.tms.freightbill.approved.v1`; nothing consumes the AP posted event to set SETTLED |
| D4 | Dispute resolved by a corrected carrier invoice, `DISPUTED → MATCHED` (BR-34.4) | later slice of PSG-34 | Slice 3 builds `:dispute` (VARIANCE to DISPUTED with a reason) and approval of a DISPUTED bill at the invoiced amount; a bill takes one invoice (TMS-0111), so a credit note or replacement invoice cannot re-match it. Disputes are not sent to the carrier (EDI or portal) |
| D4 | Per-carrier tolerance parameters `match_tolerance_abs` / `match_tolerance_pct` and `TMS_MATCH_TOL_*` (§6.34.13) | later slice of PSG-34 | Every carrier uses the default $5.00 / 2 % |
| D4 | Carrier invoice auto-match job and matching by load when the invoice names no bill (§6.34.16) | later slice of PSG-34 | An invoice is matched only to the bill it names; otherwise it is UNEXPECTED_INVOICE |
| D4 | Linking an UNEXPECTED_INVOICE to a bill afterwards (TMS-0109 remediation) | later slice of PSG-34 | No endpoint changes an invoice after registration |
| D4 | Freight accrual and reversal postings through the wave-11 journal API, account 2150, variance account 5250 (FR-34.8, BR-34.5) | handoff (wave 11 posting API) + later slice of PSG-34 | Journal posting client is not in the kernel; the accrual service call is not wired |
| D4 | Vendor invoice creation in wave-19 AP (FR-34.7 "creates a vendor invoice"), `ap_invoice_id` | handoff (wave 19 AP) | Needs the AP invoice API |
| D4 | Customer-billed freight creates a wave-29 invoice charge line (FR-34.8) | handoff (wave 29) | Needs sales invoice API |
| D5 | Cost adjustment into wave-20 inventory costing | handoff (wave 20 costing) | Slice 2 publishes `dev.passage.tms.landedcost.posted.v1` with the amount per receipt line; costing consumes it |
| D5 | Receipt lines taken from purchase receipts | handoff (purchasing receipts) | The voyage form takes receipt line numbers with their value, weight, volume and quantity; they are not looked up or checked against a receipt |
| D5 | Estimated vs actual differences to inventory or purchase price variance 5260 (BR-34.6 second sentence) | later slice of PSG-34 | Needs actual cost invoices and on-hand quantities; a voyage is allocated once from its cost lines |
| D5 | Allocation per container first, then per line (FR-34.12 notes) | later slice of PSG-34 | Cost lines allocate directly to receipt lines |
| D6 | Per vendor × Incoterm override of the goods-in-transit transfer event (`goods_in_transit_transfer_event`, BR-34.11 "set per vendor × Incoterm rule") | later slice of PSG-34 | Slice 4 builds goods-in-transit orders on the default Incoterms 2020 table: `POST /v1/transport/goods-in-transit` (origin rules only; DAP, DPU, DDP are TMS-0120) and `:ship` (TMS-0121, `goodsintransit.shipped`). The request names the Incoterm; there is no vendor parameter model |
| D6 | Goods-in-transit receipt at the destination warehouse, and the in-transit inventory movement and account in wave 20 (FR-34.12 "receive at the destination warehouse", AC-34.9 "move ownership") | later slice of PSG-34 (receipt) + handoff (wave 20 inventory and costing) | An order is PENDING or IN_TRANSIT. `dev.passage.tms.goodsintransit.shipped.v1` carries each line's value, quantity and estimated cost per cost type for inventory and costing to move the stock and value; nothing consumes it yet |
| D6 | `in_transit_warehouse` site parameter (§6.34.13) | later slice of PSG-34 | The order names the default `TRANSIT-{site}` warehouse; no site parameter overrides it |
| D6 | Actual costs on a goods-in-transit order and the variance per BR-34.6 (AC-34.9 second half) | later slice of PSG-34 | The order carries the voyage's estimated costs (ESTIMATED voyages only, TMS-0119 otherwise); actual cost invoices are not modelled (see the next rows) |
| D6 | Voyage statuses IN_TRANSIT, ARRIVED, RECEIVED, COSTED and tracking stages from TrackingEvent (FR-34.12) | later slice of PSG-34 | A voyage is ESTIMATED or POSTED (the §6.34.4 data model states); tracking events are per load, not per voyage |
| D6 | Actual cost invoices on a voyage (ACTUAL state, §6.34.4) and the voyage estimate refresh job (§6.34.16) | later slice of PSG-34 | Cost lines are entered once on creation |
| D6 | Auto-cost templates, cost type to accrual and clearing accounts, in-transit warehouse setup (§6.34.13) | later slice of PSG-34 | Cost types are free codes; no parameter model |
| D6 | `container_check_digit_required` tenant parameter | later slice of PSG-34 | The check digit is always enforced (the default) |
| D6 | Voyage cost statement PDF/XLSX (§6.34.15) | later slice of PSG-34 | The voyage detail screen shows the allocation per receipt line and cost line |
| D7 | Yard locations, transport units, yard moves `POST /tms/v1/yard-moves`, slot conflicts TMS-0125, yard slot or door assignment at check-in (FR-34.13, FR-34.14 "assign a yard slot or door") | later slice of PSG-34 | No yard tables, endpoints or screens; a visit records the trailer in the site's yard without a slot |
| D7 | DETENTION charge added to the carrier's freight bill, the carrier's hourly accessorial rate and free time looked up from carrier setup, and the hourly detention sweep (BR-34.13, §6.34.16) | later slice of PSG-34 | Slice 4 computes detention at check-out (AC-34.11: 08:00 to 11:20 with 2 h free time is 2 h, TMS-0126) from the `freeTimeMin` sent at check-in (default 120) and the `hourlyRate` sent at check-out; the charge is shown on the visit and the gate log but no freight bill line is created |
| D7 | Appointment matching by appointment id, else SCAC + trailer/container number + date, against DockAppointment (BR-34.14) | handoff (wave 31 wms DockAppointment) | The kiosk or guard sends `appointmentId` with `scheduledAt`; the arrival is classified against it (TMS-0127) but the appointment is not looked up |
| D7 | Site settings `walk_in_allowed` and `gate_tolerance_min` (§6.34.13) | later slice of PSG-34 | Walk-ins are always allowed, so TMS-0128 is not reachable through the API yet (the rule is tested in the domain); the tolerance is sent per check-in (default 60) |
| D7 | Check-out blocked when the outbound load is not confirmed in wms (FR-34.14) | handoff (wave 31 wms load confirmation) | The seal recorded at load confirmation (`POST /v1/transport/gate/visits/{id}:seal`) stands for the confirmation: an OUTBOUND visit with no seal cannot check out (TMS-0129). Nothing checks the load status in wms |
| D7 | Gate PII pseudonymization after `gate_pii_retention_days` (default 90) and the daily job (FR-34.14, §6.34.16) | later slice of PSG-34 | Driver name and license id are stored on the visit and kept out of the gate log detail and the event; no job pseudonymizes them yet |
| D7 | Gate pass / check-in receipt (kiosk print or PDF, §6.34.15) | later slice of PSG-34 | The visit detail screen shows the check-in |
| D7 | Door release in wms (`wms` work type YARD_MOVE, DockAppointment) | handoff (wave 31 wms, wave 32 mobile) | Needs the wms API |
| D8 | DG lines from the wave-31 HazmatProfile and the emergency phone from legal-entity setup (FR-34.15) | handoff (wave 31 HazmatProfile, legal-entity setup) | Slice 3 builds `POST /v1/transport/loads/{id}:dg-check` (ADR and 49CFR): the caller sends the UN number, class, packing group, quantity, transport category and limited-quantity flag per line, and the phone |
| D8 | DG checks at rating (carrier-service DG eligibility in rate shopping), at load build and at ship confirmation (FR-34.15, BR-34.15) | later slice of PSG-34 + handoff (wave 28 ship confirmation) | The check runs on request and the tender gate reads the latest stored result; rating and load build do not call it |
| D8 | RID, ADN, IMDG, IATA DGR and Canada TDG rule sets; the full segregation tables (class 1 compatibility groups, ADR label 9, 6.2) from the `DgRegulationPack` (§6.34.17) | handoff (wave 48 DgRegulationPack) | A reference subset is coded: divisions 1.1 and 1.2 against classes 2 to 8 |
| D8 | `dg_check_mode` per legal entity (BLOCK / WARN) and `dg_regulation_set` per mode × country (§6.34.13) | later slice of PSG-34 | BLOCK is fixed and the caller picks the regulation |
| D8 | Placard requirements and the DG transport document PDF (§6.34.15) | later slice of PSG-34 | Slice 3 stores and shows the shipping-paper lines (UN number, proper shipping name, class, packing group, quantity, emergency phone); no placards or document rendering |
| D9 | Own-fleet vehicles, trailers, drivers, hours-of-service rules as VRP constraints (FR-34.16, BR-34.16) | later slice of PSG-34 (routing-py side) | Driver-hours rule set and fleet tables not built; solver has no heterogeneous fleet |
| D9 | Lane RFQ `POST /tms/v1/freight-rfqs/{id}:award` (FR-34.17, BR-34.18) | handoff (wave 17 RFQ engine) + later slice of PSG-34 | Award logic needs the RFQ engine. Award into rate cards uses this service's rate card API |
| D9 | Layer-based load stacking (FR-34.18) | later slice of PSG-34 | Not in `services/routing-py` yet |
| D10 | Customs data on shipments, commercial invoice and customs packing list (FR-34.19) | later slice of PSG-34 | Not started |
| D10 | Screening holds stored on the load, release by TRADE_COMPLIANCE_RELEASE with a logged reason, and the gate at load release, tender and ship confirmation (BR-34.17, AC-34.16) | later slice of PSG-34 | Slice 3 builds the `ScreeningProvider` hook (`domain.ScreeningProvider`, 5 s timeout, never auto-clears) and `POST /v1/transport/loads/{id}:screen`, which returns CLEAR, WARN (TMS-0134) or HOLD (TMS-0133) without storing it |
| D10 | Screening result cache `screening_cache_h` per party and list version | later slice of PSG-34 | Every call goes to the provider |
| D10 | Provider implementations (denied-party lists) and the `CustomsFilingProvider` hook (FR-34.19) | handoff (wave 48) + later slice of PSG-34 (filing hook) | No provider is registered, so cross-border loads screen as NOT_SCREENED; no filing interface is defined |
| Deploy | Demo carriers, 12 services and rate cards (fixture `carriers-demo`) | later slice of PSG-34 | Seed data not written |
| Deploy | Transportation web screens for the yard, route planning and a tracking map | later slice of PSG-34 | The web module covers what slices 1 to 4 build: rate shop, rate cards, freight bills (reconciliation grid, approve, dispute), carrier invoices, tender board, tracking timelines, landed cost voyages, loads (list, build, DG check, screening), the gate log with check-in, seal and check-out, and goods-in-transit orders (list, form, ship) |
| Deploy | Feature flags `tms.enabled`, `tms.landed_cost`, `tms.vrp` | later slice of PSG-34 | The chart sets `TMS_FEATURE_*` env vars but the binary does not read them |
| Deploy | Scheduled jobs: fuel index import, carrier invoice auto-match, accrual tie-out, KPI materialization (§6.34.16); a schedule for the tender expiry sweep | later slice of PSG-34 | No scheduler code yet; the sweep itself is `POST /v1/transport/tenders:expire` |
| Migration | AX/SAP migration packs (§6.34.11) | handoff (migration-framework, `integration` DB) | Owned by the migration framework |
| Reporting | KPIs (FR-34.11) | handoff (analytics, Cube) | Metrics are published from the analytics lakehouse |
| Portal | Carrier portal, carrier identities (FR-34.10); scoping TMS_CARRIER_RESPOND to the caller's own carrier and loads | handoff (wave 20 supplier portal, edge identity) | Depends on the supplier portal framework. Until then `:respond` takes the carrier id from the body and checks only the duty |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 Solomon VRP benchmarks C101, R101, RC101, C201, R201 within 110 % of best known | later slice of PSG-34 | Solomon instance files are not in the repo; the solver is covered by feasibility tests only (routing-py) |
| AC2 200-shipment rating fixture (`rating-200.json`) matched to the cent | later slice of PSG-34 | Fixture and hand-verified spreadsheet not committed; rating unit and property tests cover the lane, minimum, dimensional, accessorial, fuel, weight-break deficit, distance and card-selection rules |
| AC3 Match tolerance on 50 invoices and landed-cost sums | partial | Match rules and landed-cost sums are unit and property tested, and a posted voyage's allocations equal its cost total in the store test; the 50-invoice fixture (`carrier-invoices-50.json`) is not committed |
| AC4 Tender cascade with timeouts on a test clock | met for the cascade | AC-34.3 is tested on a test clock in the domain, through the API and against Postgres (expired, declined, accepted events); the Temporal time-skip test waits for the workflow |
| AC5 Each deliverable has code or a line here | met by this file | |
| AC6 vet and test in Go, vitest and tsc in the web module, OpenAPI and AsyncAPI match code, chart lints | met for slices 1 to 4 | Store tests ran against Postgres 16 as a non-owner application role during review; `TestOpenAPI_MatchesRoutes` and `TestAsyncAPI_ListsEveryEventType` keep the specs and code in step; `pytest` in routing-py passes but has no wave pipeline check kind |
| AC-34.9 goods in transit with actual-cost variance | partial | The FOB orders, their estimated freight and duty per line and the ship event are tested against Postgres (`TestGoodsInTransit_AC349`; the shipped estimates sum to the voyage cost total to the cent, also as a property). The actual invoices and the BR-34.6 variance are not built (D6) |
| AC-34.11 detention at check-out | met at the gate; the freight bill line is not | `TestGate_AC3412BlockedAttemptIsLogged` checks 08:00 to 11:20 with 2 h free time as 2 h × the rate (TMS-0126) through the store; the DETENTION charge is not added to the freight bill (D7) |
| AC-34.12 seal mismatch blocks check-out and is logged | met | The outbound load sealed 778812 is refused with 778813 (TMS-0129), the CHECK_OUT_BLOCKED row commits in the append-only gate log, and the right seal then checks out (domain, API and Postgres tests) |

## Test plan items

| Item | Status | Reason |
|---|---|---|
| Carrier invoice fixture `carrier-invoices-50.json` with 10 variance cases | later slice of PSG-34 | Match tolerance is covered by table tests and the AC-34.4 store test |
| Reconciliation: accrued freight = account 2150; shipped loads in wms = DISPATCHED+ in tms | later slice of PSG-34 / handoff (ledger, wms) | Needs the accrual and wms data. "Landed cost posted = voyage costs" is tested for estimated costs (`TestVoyage_AllocateAC346AndReconcile`); actual costs come with D6 |
| Fixture `voyage-3.json` | later slice of PSG-34 | The AC-34.6 voyage is built inline in the domain and store tests |

## Platform and contract gaps

| Gap | Status | Reason |
|---|---|---|
| `Idempotency-Key` 24 h expiry and a stored-response replay table (`dbx.IdempotencyDDL`) | later slice of PSG-34 | Keys live on the created row with a request fingerprint: same body replays the row, another body is 422 `idempotency_key_reuse`; keys do not expire |
| `If-Match` optimistic concurrency on tender, voyage, load, gate visit and goods-in-transit commands | later slice of PSG-34 | Freight bill approve and dispute require `If-Match` (slice 3). Tenders, voyages, gate visits and goods-in-transit orders return `version`; their commands serialise with a row lock (and advisory locks for tender start and per-trailer check-in) instead of comparing a client version |
| OpenTelemetry traces and latency histograms (rating p95 SLO) | handoff (platform kernel, wave 0) | The admin port serves `/metrics` with request counters only |
| `/readyz` checks only the process; no Postgres SELECT 1, OPA bundle or migration check | handoff (platform kernel) | `httpx.NewRouter` provides the static probe |
| OPA duty decisions (`data.passage.authz.decision`) | handoff (platform kernel, edge identity) | Slice 1 uses the kernel's local duty check through `authz.RequireDuty` |
| `legal_entity_scope` RLS policy | later slice of PSG-34 | The tables do not carry `legal_entity_id` yet; add with the legal-entity-scoped tables |
| Rate-card overlap enforced at database level | later slice of PSG-34 | Enforced in the store (`Overlaps`) only |
| Events `dev.passage.tms.shipment.rated.v1`, and load, DG or gate check-out events | later slice of PSG-34 | Published: `freightbill.matched`, `freightbill.approved` (slice 3), `tender.accepted/declined/expired`, `tracking.event`, `landedcost.posted`, `gate.checkedin` and `goodsintransit.shipped` (slice 4; the latter is not in the PRD's event table and is added for wave 20). Load builds, DG checks and check-outs publish nothing yet |
| BFF route registration for `/api/v1/transport` | handoff (web-bff owner) | The service follows the `/v1/<service>` convention the BFF rewrites; registering the upstream is outside this wave's paths |
| Error codes TMS-0103, 0104, 0107, 0125, 0132, 0135 | later slice of PSG-34 | Raised only by features not built in slices 1 to 4. TMS-0128 is raised by the check-in rule but cannot be reached until sites can refuse walk-ins (D7) |
