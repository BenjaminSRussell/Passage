# NOT-IMPLEMENTED: transport (PSG-34, wave 34, slice 1)

Every ticket deliverable and acceptance criterion that slice 1 does not build, with its status.
Paths below use the PRD's `/tms/v1/...` names; the service mounts them under `/v1/transport/...`.
Status is either `later slice of PSG-34` (same wave, next slice) or `handoff` (needs another
owner's work, or a change outside this wave's paths).

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| D1 | Load building by rule and manually (FR-34.1), load capacity checks (BR-34.8), `POST /tms/v1/loads:build` | later slice of PSG-34 | Slice 1 builds rating and reconciliation; load aggregate and store are not written yet |
| D1 | Route planning endpoint `POST /tms/v1/routes:optimize` (FR-34.2) | later slice of PSG-34 | The solver exists in `services/routing-py`; the Go-to-optimizer HTTP client, request hash idempotency and route persistence are not built |
| D1 | Load planning workbench UI | later slice of PSG-34 | The web module has the workspace, rate shop, rate cards, freight bills and carrier invoices; no load or route screens yet |
| D2 | Rate shopping cache (10 min per input hash, FR-34.4) and route guides with fallbacks | later slice of PSG-34 | Rate shop is computed per call over every card of the tenant; no cache, route guide entity or GiST validity index |
| D2 | Weighted ranking by transit time or score (FR-34.4) | later slice of PSG-34 | Options rank by total only; transit days are not modelled on carrier services |
| D2 | Fuel index (TMSFuelIndex): weekly index effective on the ship date, `fuel_index_id` on the card (BR-34.2) | later slice of PSG-34 | The card carries a fixed `fuelPct`; there is no fuel index table or weekly import |
| D2 | Currency conversion of a quote to the shipment currency at wave-12 rate type FREIGHT (BR-34.2) | handoff (wave 12 FX) | Quotes are in the card currency; no FX client in this service |
| D2 | Class-based chargeable weight for LTL and the parcel DIM factor in in³/lb per carrier service (BR-34.2, `dim_factor_parcel`) | later slice of PSG-34 | Dimensional weight uses one metric divisor per card (cm³/kg, default 5000) |
| D2 | Rate card publish flow (draft, approve) and `dev.passage.tms.shipment.rated.v1` | later slice of PSG-34 | Cards are active when saved; rating publishes no event |
| D2 | Service-area, mode, hazmat and max-weight eligibility filter (FR-34.4) | later slice of PSG-34 | Carrier service master data is owned by the carrier/party work; eligibility fields not modelled |
| D2 | Tender workflow and endpoints `POST /tms/v1/loads/{id}:tender`, `POST /tms/v1/tenders/{id}:respond` (FR-34.5) | later slice of PSG-34 | Temporal `TenderWorkflow` not written; only the domain state machine exists (`tender.go`) |
| D2 | Broadcast tendering option and EDI 204 send (FR-34.5) | later slice of PSG-34 / handoff (EDI 204 via wave 35) | Depends on the tender workflow and wave 35 EDI |
| D3 | Tracking ingest `POST /tms/v1/tracking-events`, EDI 214 codes, carrier APIs, late-delivery alerts, proof of delivery (FR-34.6, BR-34.9) | later slice of PSG-34 | Not started; no tracking_event table |
| D3 | POD attach to the sales shipment for wave-29 invoicing holds | handoff (wave 29 sales) | Needs the sales shipment API |
| D4 | Freight bill created at ship confirmation (FR-34.7) | later slice of PSG-34 | Slice 1 creates bills by API; the ship-confirmation trigger needs the shipment event from wave 28 |
| D4 | Approve and release to AP `POST /tms/v1/freight-bills/{id}:approve` (BR-34.4 APPROVED / SETTLED) | later slice of PSG-34 | Approval duty and state transitions beyond MATCHED/VARIANCE not built |
| D4 | DISPUTED state and dispute workflow (BR-34.4) | later slice of PSG-34 | Only MATCHED, VARIANCE and UNEXPECTED_INVOICE are stored |
| D4 | Per-carrier tolerance parameters `match_tolerance_abs` / `match_tolerance_pct` and `TMS_MATCH_TOL_*` (§6.34.13) | later slice of PSG-34 | Every carrier uses the default $5.00 / 2 % |
| D4 | Carrier invoice auto-match job and matching by load when the invoice names no bill (§6.34.16) | later slice of PSG-34 | An invoice is matched only to the bill it names; otherwise it is UNEXPECTED_INVOICE |
| D4 | Linking an UNEXPECTED_INVOICE to a bill afterwards (TMS-0109 remediation) | later slice of PSG-34 | No endpoint changes an invoice after registration |
| D4 | Freight accrual and reversal postings through the wave-11 journal API, account 2150, variance account 5250 (FR-34.8, BR-34.5) | handoff (wave 11 posting API) + later slice of PSG-34 | Journal posting client is not in the kernel; the accrual service call is not wired |
| D4 | Vendor invoice creation in wave-19 AP (FR-34.7 "creates a vendor invoice"), `ap_invoice_id` | handoff (wave 19 AP) | Needs the AP invoice API |
| D4 | Customer-billed freight creates a wave-29 invoice charge line (FR-34.8) | handoff (wave 29) | Needs sales invoice API |
| D5 | Landed-cost allocation endpoint `POST /tms/v1/landed-cost/voyages/{id}:allocate` and posting of `dev.passage.tms.landedcost.posted.v1` | later slice of PSG-34 | Allocation by weight, volume, value, quantity and equal split exists and is property tested (`landedcost.go`); voyage store and endpoint are not built |
| D5 | Cost adjustment into wave-20 inventory costing | handoff (wave 20 costing) | Needs the costing adjustment API |
| D6 | Landed-cost voyages, legs, shipping containers, folios, estimated and actual costs, goods in transit (FR-34.12, BR-34.11) | later slice of PSG-34 | Only the ISO 6346 check digit (`container.go`) exists; no voyage tables or endpoints `POST /tms/v1/voyages`, `POST /tms/v1/goods-in-transit/{id}:ship` |
| D6 | Auto-cost templates and in-transit warehouse setup (§6.34.13) | later slice of PSG-34 | Parameter model not defined |
| D7 | Yard locations, transport units, yard moves `POST /tms/v1/yard-moves`, detention (FR-34.13, BR-34.13) | later slice of PSG-34 | Not started |
| D7 | Driver check-in and check-out `POST /tms/v1/gate/check-in`, `POST /tms/v1/gate/check-out`, gate log, seal check TMS-0129, PII pseudonymization (FR-34.14, BR-34.14) | later slice of PSG-34 | Not started; the `dev.passage.tms.gate.checkedin.v1` event is not published |
| D7 | Door release in wms (`wms` work type YARD_MOVE, DockAppointment) | handoff (wave 31 wms, wave 32 mobile) | Needs the wms API |
| D8 | DG checks at rating, load build, tender and ship (FR-34.15, BR-34.15), `POST /tms/v1/loads:dg-check` | handoff (wave 31 HazmatProfile) + later slice of PSG-34 | Depends on the HazmatProfile master data from wave 31 |
| D8 | DG shipping-paper lines and placard requirements | later slice of PSG-34 | Document generation not built |
| D9 | Own-fleet vehicles, trailers, drivers, hours-of-service rules as VRP constraints (FR-34.16, BR-34.16) | later slice of PSG-34 (routing-py side) | Driver-hours rule set and fleet tables not built; solver has no heterogeneous fleet |
| D9 | Lane RFQ `POST /tms/v1/freight-rfqs/{id}:award` (FR-34.17, BR-34.18) | handoff (wave 17 RFQ engine) + later slice of PSG-34 | Award logic needs the RFQ engine. Award into rate cards uses this service's rate card API |
| D9 | Layer-based load stacking (FR-34.18) | later slice of PSG-34 | Not in `services/routing-py` yet |
| D10 | Customs data on shipments, commercial invoice and customs packing list (FR-34.19) | later slice of PSG-34 | Not started |
| D10 | `ComplianceScreeningProvider` and `CustomsFilingProvider` hooks, screening gate and holds `POST /tms/v1/loads/{id}:screen`, TRADE_COMPLIANCE_RELEASE (BR-34.17, TMS-0133, TMS-0134) | later slice of PSG-34 (hooks); provider implementations handoff (wave 48) | No provider interface is defined yet |
| Deploy | Demo carriers, 12 services and rate cards (fixture `carriers-demo`) | later slice of PSG-34 | Seed data not written |
| Deploy | Transportation web screens for load planning, tender board, tracking, landed cost voyages, yard and gate | later slice of PSG-34 | The web module covers what slice 1 builds: rate shop, rate cards, freight bills (with the reconciliation grid) and carrier invoices |
| Deploy | Feature flags `tms.enabled`, `tms.landed_cost`, `tms.vrp` | later slice of PSG-34 | The chart sets `TMS_FEATURE_*` env vars but the binary does not read them |
| Deploy | Scheduled jobs: fuel index import, tender expiry sweep, carrier invoice auto-match, accrual tie-out, KPI materialization (§6.34.16) | later slice of PSG-34 | No scheduler code yet |
| Migration | AX/SAP migration packs (§6.34.11) | handoff (migration-framework, `integration` DB) | Owned by the migration framework |
| Reporting | KPIs (FR-34.11) | handoff (analytics, Cube) | Metrics are published from the analytics lakehouse |
| Portal | Carrier portal, carrier identities (FR-34.10) | handoff (wave 20 supplier portal) | Depends on the supplier portal framework |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 Solomon VRP benchmarks C101, R101, RC101, C201, R201 within 110 % of best known | later slice of PSG-34 | Solomon instance files are not in the repo; the solver is covered by feasibility tests only (routing-py) |
| AC2 200-shipment rating fixture (`rating-200.json`) matched to the cent | later slice of PSG-34 | Fixture and hand-verified spreadsheet not committed; rating unit and property tests cover the lane, minimum, dimensional, accessorial, fuel, weight-break deficit, distance and card-selection rules |
| AC3 Match tolerance on 50 invoices and landed-cost sums | partial | Match rules and landed-cost sums are unit and property tested; the 50-invoice fixture (`carrier-invoices-50.json`) is not committed |
| AC4 Tender cascade with timeouts on a test clock | later slice of PSG-34 | Depends on the Temporal workflow |
| AC5 Each deliverable has code or a line here | met by this file | |
| AC6 vet and test in Go, vitest and tsc in the web module, OpenAPI and AsyncAPI match code, chart lints | met for the slice | Store tests ran against Postgres 16 as a non-owner application role during review; `pytest` in routing-py passes but has no wave pipeline check kind |

## Test plan items

| Item | Status | Reason |
|---|---|---|
| Carrier invoice fixture `carrier-invoices-50.json` with 10 variance cases | later slice of PSG-34 | Match tolerance is covered by table tests and the AC-34.4 store test |
| Reconciliation: accrued freight = account 2150; landed cost posted = voyage actuals; shipped loads in wms = DISPATCHED+ in tms | later slice of PSG-34 / handoff (ledger, wms) | Needs the accrual, voyage and wms data |
| 214 ingest ordering contract test (BR-34.9) | later slice of PSG-34 | Ingest not built |

## Platform and contract gaps

| Gap | Status | Reason |
|---|---|---|
| `Idempotency-Key` 24 h expiry and a stored-response replay table (`dbx.IdempotencyDDL`) | later slice of PSG-34 | Keys live on the created row with a request fingerprint: same body replays the row, another body is 422 `idempotency_key_reuse`; keys do not expire |
| OpenTelemetry traces and latency histograms (rating p95 SLO) | handoff (platform kernel, wave 0) | The admin port serves `/metrics` with request counters only |
| `/readyz` checks only the process; no Postgres SELECT 1, OPA bundle or migration check | handoff (platform kernel) | `httpx.NewRouter` provides the static probe |
| OPA duty decisions (`data.passage.authz.decision`) | handoff (platform kernel, edge identity) | Slice 1 uses the kernel's local duty check through `authz.RequireDuty` |
| `legal_entity_scope` RLS policy | later slice of PSG-34 | The tables do not carry `legal_entity_id` yet; add with the legal-entity-scoped tables |
| Rate-card overlap enforced at database level | later slice of PSG-34 | Enforced in the store (`Overlaps`) only |
| Events beyond `dev.passage.tms.freightbill.matched.v1` (`shipment.rated`, `tender.*`, `tracking.event`, `landedcost.posted`, `gate.checkedin`) | later slice of PSG-34 | Only the match event is published in slice 1 |
| BFF route registration for `/api/v1/transport` | handoff (web-bff owner) | The service follows the `/v1/<service>` convention the BFF rewrites; registering the upstream is outside this wave's paths |
| Error codes TMS-0103, 0104, 0107, 0121, 0125-0135 | later slice of PSG-34 | Raised only by features not built in slice 1 |
