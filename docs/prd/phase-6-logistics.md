# Part 6 — Phase 6: Warehouse, Quality & Logistics (waves 31–35)

## 6.0 Phase overview

**Business outcome at phase exit.** At the end of wave 35 the demo manufacturer (live since wave 30) runs its warehouses on location-directed work executed from handheld scanners, inspects inbound, in-process and outbound inventory under rule-generated quality orders, plans and rates outbound freight against real carrier rate cards, reconciles carrier invoices and lands freight into inventory cost, and exchanges purchase orders, ASNs and invoices with a demo distributor over X12 EDI and Peppol. Every one of those flows posts into the wave-16 inventory ledger and the wave-11 universal journal through the existing posting APIs; Phase 6 adds no new financial truth, only new operational sources of it. The Phase 6 services are `wms` (Go, `wms_db`), `wms-mobile` BFF (TypeScript), `quality` (Go, `quality_db`), `tms` (Go, `tms_db`) with a Python `tms-optimizer` sidecar (OR-Tools), and `integration-hub` plus `edi` (TypeScript, `integration_db`). All run on the ADR-0001 stack; nothing in this phase introduces new infrastructure.

**Phase-exit sign-off criteria**
- Demo distributor ↔ demo manufacturer exchange PO (850/ORDERS) → ASN (856/DESADV) → invoice (810/INVOIC and Peppol UBL) end-to-end with zero manual re-keying; the invoice lands as a wave-19 vendor invoice at the distributor and matches 3-way.
- 100 location-directive fixtures resolve to the expected location; 500-order wave release creates work and replenishment with no deadlocks and p95 task assignment under 1 s.
- Mobile E2E inbound-to-outbound run on an emulated Zebra device using scanned GS1-128 labels passes in CI; 200 queued offline transactions sync without loss or duplicate.
- Quality property test: no pick, ship or consume succeeds against a quant with `quality_status = BLOCKED` across 10,000 randomized attempts; AQL sample sizes equal ANSI/ASQ Z1.4 Table I/II-A for every lot-size class.
- VRP solutions on Solomon C1/R1/RC1 instances are feasible and within 10% of best-known distance; 200 rating fixtures produce expected charges to the cent; landed cost allocation sums to the carrier invoice exactly.
- Daily reconciliation: `wms` on-hand by location sums to the wave-16 inventory ledger on-hand per item/warehouse; freight accruals in `tms` tie to journal account 2150 (accrued freight) to the cent.

**Cross-wave data model summary**

| Entity | Owning service | Key relationships |
|---|---|---|
| Location, Zone, LocationProfile | wms | Warehouse (wave 6); capacity used by Work and Quant |
| LicensePlate (nested) | wms | Parent LP; Quant; Load; ShipmentContainer (wave 28) |
| Quant (on-hand by item × dims × location × LP) | wms | Projection of inventory ledger (wave 16); carries `quality_status` |
| LocationDirective, WorkTemplate, WaveTemplate, ReplenishmentTemplate | wms | Metadata; evaluated by the directive engine |
| Work, WorkLine, Wave, Load, DockAppointment | wms | Sales shipment (wave 28), purchase receipt (wave 18), production order (wave 21), TMS Load |
| MobileFlow, MobileMenu, DeviceSession, OfflineTransaction | wms-mobile | Work; WorkUser (Keycloak subject) |
| Test, TestGroup, SamplingPlan, QualityAssociation | quality | Item/product (wave 8); Party (wave 7) |
| QualityOrder, QualityResult, UsageDecision | quality | Quant (blocking), PurchaseReceipt, ProductionOrder, Shipment |
| Nonconformance, Correction (CAPA), Instrument | quality | QualityOrder; Vendor; Temporal workflow id |
| Carrier, CarrierService, RateCard, Accessorial, FuelIndex | tms | Party (wave 7) |
| Shipment, Route, Load (TMS view), Tender, TrackingEvent | tms | wms Load; Sales shipment (wave 28) |
| FreightBill, CarrierInvoice, LandedCostVoyage | tms | Vendor invoice (wave 19); inventory cost (wave 20); journal (wave 11) |
| TradingPartner, EDIProfile, Map, Interchange, Message, Acknowledgment | edi | Party; PurchaseOrder, SalesOrder, Shipment, Invoice |
| ApiClient, Subscription (webhook/Kafka), DeliveryAttempt, Quota | integration-hub | Keycloak client; Kafka topics |
| CrossDockTemplate, CrossDockLink, SortTemplate, SortPosition, VasOrder, PackagingSpec | wms | Work; Shipment (wave 28); kit BOM (wave 9); added by FR-31.13–FR-31.17 |
| LaborStandard, LaborLine, PerformanceDocument | wms | Work, WorkUser; payroll export (wave 42); FR-31.18 |
| HazmatProfile, SegregationMatrix, FireContainmentSection | wms | Item (wave 8); LocationProfile; read by tms DG checks (FR-34.15); FR-31.19 |
| AutomationChannel, TelegramProfile, CommunicationPoint, TransportOrder | wms | Location; Work; edge gateway (wave 25); FR-32.12 |
| Printer, DeviceType, PrintQueue, RoutingRule | print service | PrintJob; FR-32.13 |
| ComplianceList, ItemComposition, ComplianceResult, SafetyDataSheet, SdsDelivery | quality | Item, BOM (wave 9), Shipment (wave 28); FR-33.13–FR-33.14 |
| IncomingCertificate, PhysicalSample, SpecificationSet, InspectionPoint | quality | QualityOrder; PO line (wave 17); production operation (wave 21); FR-33.15, FR-33.19–FR-33.22 |
| StabilityStudy, PullPoint, Audit, QuestionList, Finding, Fmea, ControlPlan | quality | Batch (wave 16); Nonconformance/CAPA; TestGroup; FR-33.16–FR-33.18 |
| VoyageLeg, ShippingContainer, Folio, GoodsInTransitOrder | tms | LandedCostVoyage; PO lines (wave 17); in-transit warehouse (wave 16); FR-34.12 |
| YardLocation, TransportUnit, YardMove, DriverVisit, GateEvent | tms | DockAppointment (wave 31); Load; Carrier; FR-34.13–FR-34.14 |
| DgCheckResult, Vehicle, Driver, FreightRfq, LoadPlan, ScreeningResult | tms | HazmatProfile (wave 31); RFQ (wave 17); Party (wave 7); FR-34.15–FR-34.19 |
| PunchoutCatalog, PunchoutSession, SelfBillingDocument, ConnectorConfig | integration-hub / edi | Requisition (wave 17); customer invoice (wave 29); ECR (wave 9); FR-35.13–FR-35.16 |

**Cross-wave event catalog**

| Event (CloudEvents type) | Producer | Consumers | Payload summary |
|---|---|---|---|
| dev.passage.wms.work.created.v1 | wms | wms-mobile, analytics | work_id, work_type, warehouse, priority, lines[] |
| dev.passage.wms.work.completed.v1 | wms | inventory, sales-fulfillment, production | work_id, lines with from/to location, LP, qty |
| dev.passage.wms.load.shipped.v1 | wms | tms, sales-fulfillment, edi (856) | load_id, shipments[], LPs/SSCCs, carrier, ship_date |
| dev.passage.wms.receipt.registered.v1 | wms | purchasing (wave 18), quality | receipt_id, po lines, LPs, qty |
| dev.passage.quality.order.created.v1 | quality | wms (block quant), analytics | quality_order_id, trigger, item, quant_ref, sample_size |
| dev.passage.quality.usagedecision.posted.v1 | quality | wms, inventory, purchasing, production | quality_order_id, decision, accept_qty, reject_qty |
| dev.passage.quality.nonconformance.created.v1 | quality | workflow, vendor-scorecard | ncr_id, source, severity, item, vendor |
| dev.passage.tms.shipment.rated.v1 | tms | sales (freight charge), analytics | shipment_id, carrier, service, charges[], total |
| dev.passage.tms.tender.accepted.v1 | tms | wms (dock), edi (204/990) | load_id, carrier, pickup_window |
| dev.passage.tms.freightbill.matched.v1 | tms | accounts-payable (wave 19), costing (wave 20) | freight_bill_id, invoice_id, variance, landed_cost_lines[] |
| dev.passage.edi.message.received.v1 | edi | purchasing, sales, ap | partner, standard, transaction_set, control numbers, document ref |
| dev.passage.edi.message.sent.v1 | edi | analytics, integration-hub | partner, transaction_set, ack status |
| dev.passage.hub.webhook.delivered.v1 | integration-hub | analytics | subscription_id, event_id, attempt, status |
| dev.passage.wms.crossdock.allocated.v1 | wms | sales-fulfillment, analytics | receipt_id, demand_line_ref, qty, staging_location |
| dev.passage.wms.automation.fault.v1 | wms | wms-mobile, alerting | channel_id, communication_point, lp_id, fault_code |
| dev.passage.quality.compliance.blocked.v1 | quality | sales, purchasing | item_id, region, list_id, action (BLOCK/WARN), source_ref |
| dev.passage.quality.sds.distributed.v1 | quality | sales-fulfillment, customer portal | item_id, sds_version, customer_id, shipment_id |
| dev.passage.tms.goodsintransit.shipped.v1 | tms | inventory, costing | voyage_id, container_ids, lines[], estimated_costs[] |
| dev.passage.tms.gate.checkedin.v1 / checkedout.v1 | tms | wms (door work), analytics | visit_id, appointment_id, tu_id, seal_no, door_or_slot |
| dev.passage.tms.dgcheck.failed.v1 | tms | planners, wms | load_id, rule_ids[], classes[] |
| dev.passage.tms.screening.held.v1 | tms | sales, compliance tasks | load_id, party_id, status, list_version |
| dev.passage.edi.selfbilling.matched.v1 | edi | accounts receivable (wave 29), cash application (wave 30) | document_id, customer_id, matched_lines, differences[] |
| dev.passage.hub.punchout.returned.v1 | integration-hub | purchasing (wave 17) | session_id, supplier_id, lines[], rejected[] |

**Phase-level risks**

| Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|
| Directive engine performance collapses on large location sets (50k bins) | Medium | High | Indexed candidate pre-filter per zone/profile; benchmark at 50k locations in wave 31 CI; p95 < 200 ms per line | wms lead |
| Scanner hardware variance (Zebra DataWedge vs Honeywell) breaks scan capture | Medium | Medium | Keyboard-wedge fallback plus intent API adapters; emulator E2E for both profiles | mobile lead |
| Quality blocking is bypassed by a path not covered by the property test | Low | High | Blocking enforced in `inventory` ledger reservation, not only in `wms`; property test enumerates all transaction types | quality lead |
| VRP solver time exceeds planning window on 1,000-stop problems | Medium | Medium | Time-limited local search (60 s) with feasible first solution guaranteed by sweep heuristic | tms lead |
| EDI partner specifics (implementation guides) diverge from base maps | High | Medium | Partner-level map overrides; simulator fixtures per partner; 997/CONTRL negative tests | integration lead |
| Peppol access point certification delays | Medium | Medium | Certified third-party AP (SMP/AS4) adapter first; own AS4 endpoint deferred to v1.x | integration lead |

## 6.31 Wave 31: Advanced Warehouse Management

### 6.31.1 Scope and business context

Wave 28 ships sales orders from simple warehouses (item × location on-hand, manual pick lists). Wave 31 replaces that with the D365 WHS / SAP EWM model: a location hierarchy with capacity and profiles, license plates (LP) nested to any depth, and work that the system creates and directs by rule. After this wave a warehouse manager configures put-away and pick location directives, work templates and wave templates in metadata; releasing sales, transfer and production orders to the warehouse creates work with explicit from/to locations; replenishment runs on min/max, demand or wave triggers; loads are built and docks scheduled; receiving is ASN-driven with LPs. Out of scope: mobile execution (wave 32 — in wave 31 work is confirmed from the web workspace), quality blocking (wave 33), carrier rating (wave 34), cross-docking and catch-weight work (v1.x), labor standards (v1.x).

**Scope amendment (coverage closure).** The AX and SAP gap registers (coverage-ax §12.99, coverage-sap §13.99) move four items into this wave that the paragraph above lists as v1.x or does not name: planned and opportunistic cross-docking (FR-31.13), outbound sorting (FR-31.14), value-added services and kitting (FR-31.15), and engineered labor standards with planned-vs-actual reporting (FR-31.18). Transfer-order warehouse flows (FR-31.16), packaging specifications (FR-31.17) and hazardous-material storage compatibility (FR-31.19) are now explicit requirements. Catch-weight work and incentive pay calculation stay out of scope. Yard management moves to wave 34 (FR-34.13). §6.31.21 lists every register row and its disposition.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| D365 location directives (WHSLocationDirective form) fail silently: work is created with blank put location or "no location found" only visible in the work details after wave processing; debugging requires the "Location directive failures" log (10.0.x) or SQL on WHSWorkLine | Directive evaluation is a batch-side X++ class with no trace; query ranges are opaque | `/location-directives:simulate` returns a rule-by-rule trace; blocked work has status `BLOCKED_NO_LOCATION` with the failing step on the wave | Time to diagnose a directive miss: 30–60 min (consultant with SQL) → < 1 min from the trace; 0 silent blanks |
| EWM put-away bin determination (/SCWM/T333 storage type search sequence + bin type + storage section) spans 6 IMG nodes; a change needs a transport and a test cycle | Configuration split across customizing tables with cross-dependencies | One LocationDirective entity with ordered lines/actions, versioned metadata, simulate-before-save | Setup clicks for a new put-away rule: ~40 across 6 transactions → ≤ 8 in one designer; change lead time days → minutes |
| D365 wave processing (WHSWaveProcessing batch) runs every 5–15 min; pickers wait; a failed wave leaves partial work and the wave in "Held" with a generic infolog | Batch framework polling; non-atomic processing per order | Temporal `WaveReleaseWorkflow` starts on release, per-order activities with per-line failure reasons | Order release to assignable work: 5–15 min batch → < 5 s p95 for a 50-order wave; failures itemized 100% |
| EWM /SCWM/MON and D365 "Work details" cannot show capacity or why a location is full; over-capacity bins happen because capacity check is advisory (D365 `Location stocking limits`) | Capacity is a soft check in some paths | Capacity is an invariant: creation and confirmation both enforce it; property-tested | Over-capacity incidents: tolerated today → 0 (test-enforced) |
| On-hand by LP in AX (WHSInventOnhand vs InventSum) drifts and is reconciled manually by inventory journals | Two on-hand tables maintained by separate code paths | Quants are a projection of the single inventory ledger; daily automated reconciliation with alert | Monthly reconciliation effort: 4–8 h → 0 h; drift detected within 24 h |

### 6.31.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Location master | WMSLocation, WHSLocationProfile, WHSZone, WHSLocationFormat; form WHSLocation | /SCWM/LAGP storage bins, /SCWM/T301 storage types, sections /SCWM/T302, bin types /SCWM/T303 | wms.Location, Zone, LocationProfile | full |
| License plates / HUs | WHSLicensePlate, WHSContainerTable | /SCWM/HUHDR, /SCWM/HUITM, packaging material | wms.LicensePlate (nested), Quant | full |
| Put-away/pick strategy | WHSLocationDirective, WHSLocationDirectiveLine, WHSLocationDirectiveAction; form WHSLocationDirective | Put-away strategies (/SCWM/T333 storage type search, bin determination), stock removal strategies (FIFO/LIFO/partial) | wms.LocationDirective with ordered rules | full |
| Work templates | WHSWorkTemplateTable, WHSWorkTemplateLine, WHSWorkTransType | Warehouse process types /SCWM/TPROCTY, warehouse order creation rules /SCWM/WOCR | wms.WorkTemplate | full |
| Work / tasks | WHSWorkTable, WHSWorkLine; form WHSWorkTable | /SCWM/ORDIM_O warehouse tasks, /SCWM/WHO warehouse orders | wms.Work, WorkLine | full |
| Wave management | WHSWaveTemplateTable, WHSWaveTable, WHSWaveLine; class WHSWaveProcessing | /SCWM/WAVE, /SCWM/WAVEHDR, /SCWM/WAVEITM; wave templates /SCWM/WAVETMP | wms.WaveTemplate, Wave; Temporal `WaveReleaseWorkflow` | full |
| Replenishment | WHSReplenishmentTemplate, WHSReplenishmentTemplateLine; class WHSReplenishmentProcess | Replenishment control (/SCWM/REPL, planned/order-related/automatic) | wms.ReplenishmentTemplate | full |
| Loads / dock | WHSLoadTable, WHSLoadLine, WHSDockAppointment (appointment scheduling) | Yard management, door/transportation units /SCWM/TU, /SCWM/DOOR | wms.Load, DockAppointment | full |
| ASN receiving | WHSASNItem, WHSUOMStructure, packing structure import | Inbound delivery /SCWM/INB (EWM delivery), expected goods receipt | wms.AsnPackingStructure → LP receiving | full |
| Cycle counting | WHSCycleCountPlan, WHSCycleCountThreshold, WHSCountingWork | Physical inventory /SCWM/PI_DOCUMENT, cycle counting indicator | wms.CountPlan, counting Work (web in 31, mobile in 32) | full |
| Slotting | WHSSlottingTemplate (D365 10.0.x) | EWM slotting (/SCWM/SLOT) | wms.SlottingRun (ABC by picks, recommendations) | partial — recommendations only, no auto-move |
| Labor management | n/a (ISV) | EWM labor management | — | dropped: v1.x |
| Cross-docking | WHSCrossDocking, cross-docking templates, planned cross docking (D365 10.0.x), opportunistic cross docking on work policies | LE-WM LXDCA/LXDCK cross-docking monitor; EWM planned and opportunistic cross-docking (push deployment, pick from goods receipt) | wms.CrossDockTemplate, CrossDockLink; work type CROSS_DOCK | full |
| Outbound sorting | Outbound sorting templates and sort positions (D365 10.0.x) | EWM consolidation groups, sorter integration, /SCWM/PACK staging | wms.SortTemplate, SortPosition | full |
| VAS and kitting | Kit orders (D365 10.0.x), BOM journals for assembly | /SCWM/VASEXEC VAS orders, kit-to-order / kit-to-stock | wms.VasOrder; work type VAS; kit BOM (wave 9) | full |
| Packaging specifications | Unit sequence groups, WHSPhysDimUOM, container types, container build templates (WHSContainerBuildTemplate) | /SCWM/PACKSPEC packaging specifications with condition-based determination | wms.PackagingSpec | full |
| Labor management (revised) | n/a (ISV); work audit and workload visualization only | EWM labor management /SCWM/LM: engineered labor standards, planned vs actual, performance documents | wms.LaborStandard, LaborLine, PerformanceDocument | partial — standards and reports; incentive pay calculation out of scope (replaces the "dropped: v1.x" row above) |
| Hazardous-material storage | Hazardous material item flags; product compliance (D365 PI) | LE-WM hazardous material storage (LX06 fire-containment sections, LX24 hazmat list); EWM hazardous substance storage checks (storage class, water hazard class) | wms.HazmatProfile, SegregationMatrix, FireContainmentSection | full |
| Transfer order warehouse flow | WHS transfer order shipping and receiving with LP; release to warehouse | Stock transport order with EWM outbound/inbound delivery (/SCWM/PRDO, /SCWM/PRDI) | Work types TRANSFER_ISSUE/TRANSFER_RECEIPT with LP identity across sites | full |

### 6.31.3 Functional requirements

- **FR-31.1 Location hierarchy with profiles** — Must. The system shall model locations as `warehouse / zone / aisle / rack / shelf / bin` with a LocationProfile that defines capacity (weight, volume, LP count, item count), mixing rules and allowed LP behavior.
  - Notes: capacity limits are checked when work is created and again at confirmation; a location with `allow_mixed_items = false` rejects a second item; location name is generated from a LocationFormat (`A-01-02-03` → aisle 1, rack 2, shelf 3); maximum 1,000,000 locations per warehouse.
- **FR-31.2 Nested license plates** — Must. The system shall track inventory on license plates that nest (pallet → case → inner) to depth 5, with a single move of a parent moving all children atomically.
  - Notes: LP id is an SSCC-18 when generated by Passage (extension digit 0, GS1 company prefix from wave 8 config); external LPs are accepted up to 40 characters; an LP can be at exactly one location or in transit on one Work.
- **FR-31.3 Location directives** — Must. The system shall resolve a destination (put-away) or source (pick) location for each work line by evaluating ordered directives, lines and actions as specified in BR-31.1.
  - Notes: directive scope = work order type × warehouse × optional query on item/customer/vendor; strategies: `consolidate`, `fifo`, `lifo`, `fefo`, `empty_location`, `match_packing_quantity`, `round_up_to_lp`; evaluation p95 < 200 ms per line at 50,000 locations.
- **FR-31.4 Work templates** — Must. The system shall create Work from templates whose lines define pick/put pairs, work class, stop-after-line and max lines/LPs per work header.
  - Notes: templates have a query (e.g., `shipping_method = LTL`); `work_break` rules split by customer, shipment or load; work class restricts which users may execute.
- **FR-31.5 Wave templates and release** — Must. The system shall group released order lines into waves by template (query, max lines 5,000, max weight, scheduled or manual release), create work and replenishment on release, and lock or allow late additions.
  - Notes: release runs as Temporal `WaveReleaseWorkflow` with per-order activities; automatic wave release on schedule (cron) or on order release; failed lines are listed on the wave with reason codes.
- **FR-31.6 Replenishment** — Must. The system shall create replenishment work by min/max (per location × item), by demand (wave shortage) and by load demand, from configured source zones.
  - Notes: min/max runs every 15 minutes by default; demand replenishment is created inside the wave before pick work; replenishment work carries priority 10 (higher than picks at 50) and must complete before the dependent pick is assignable.
- **FR-31.7 Work execution and exceptions** — Must. The system shall assign work by priority, work class and user filter; support short pick with reason, skip location, over-pick within tolerance and cancel-with-reversal.
  - Notes: short pick re-queues remaining qty as new work after a configurable retry; assignment is pessimistic (`SELECT … FOR UPDATE SKIP LOCKED`) so two users never receive the same work; p95 assignment < 1 s under 10k lines/day simulation.
- **FR-31.8 Loads and dock appointments** — Must. The system shall build loads from shipments (manual, rule, or by route), confirm loads, and schedule dock appointments with 15-minute slot granularity per door.
  - Notes: load confirmation posts packing slips via the wave-28 API; dock doors have calendars (wave 6); overlapping appointments on one door are rejected.
- **FR-31.9 ASN-driven receiving** — Must. The system shall import an ASN packing structure (from EDI 856 in wave 35, or JSON API now) and receive by scanning/selecting the pallet LP, creating quants per nested LP.
  - Notes: receipt without ASN remains supported (item + qty + LP); variance to ASN produces a receipt discrepancy with tolerance ±2% default.
- **FR-31.10 Cycle counting** — Should. The system shall generate count work by plan (location set, frequency), threshold (on-hand ≤ n) and ad hoc; count differences post via the wave-16 counting journal after supervisor approval above a tolerance.
  - Notes: tolerance default 1% of value or 5 units; blind counts hide expected qty.
- **FR-31.11 Slotting analysis** — Could. The system shall compute ABC velocity by picks/30 days and recommend item-to-location moves to forward pick zones.
  - Notes: output is a recommendation list; applying generates movement Work.
- **FR-31.12 Warehouse KPIs** — Should. The system shall expose lines picked per hour, work aging, dock utilization, replenishment lead time, short-pick rate via the Cube semantic layer.
  - Notes: materialized hourly; lag ≤ 60 s per PRD §8.
- **FR-31.13 Cross-docking (planned and opportunistic)** — Must. The system shall move inbound quantity from a receiving location to an outbound staging or shipping location without put-away to storage, either planned (a CrossDockTemplate links an expected supply — PO line, ASN or transfer receipt — to a released demand line before arrival) or opportunistic (at receipt registration, released demand with an open shortage for the item is found and CROSS_DOCK work is created instead of put-away).
  - Notes: allocation per BR-31.11; demand priority `SHIP_DATE`, `ORDER_PRIORITY` or `FIFO_RELEASE`; supply window default 48 h before the demand ship date; partial cross-dock splits the receipt (remainder follows put-away directives); CROSS_DOCK work uses PUT directives with the new strategy `outbound_staging`; the reservation moves from supply to demand in the wave-16 ledger so ATP never counts the quantity twice.
- **FR-31.14 Outbound sorting** — Should. The system shall sort picked LPs or cartons at a sortation area into sort positions defined by a SortTemplate (criteria: shipment, load, customer, route or carrier) and close a position into an outbound LP when it is full, when the last expected LP arrives, or on demand.
  - Notes: position capacity by LP count, weight and volume; scanning at the sort station returns the target position (p95 < 300 ms); closing prints the shipping label through the wave-32 print service and builds the LP hierarchy used by the 856 (BR-35.2).
- **FR-31.15 Value-added services and kitting** — Should. The system shall create VAS orders from sales-order line requirements or customer-item VAS profiles (labeling, tagging, gift wrap, repack, kit assembly) and run them as VAS work steps between pick and pack; kit-to-order assembles a kit BOM (wave 9 BOM type `KIT`) in the warehouse and kit-to-stock receives finished kits into inventory.
  - Notes: VAS work targets a VAS station location; assembly posts per BR-31.12; disassembly reverses the posting; VAS step time feeds labor standards (FR-31.18) and customer VAS charges (wave 29 charge code `VAS`).
- **FR-31.16 Transfer order warehouse flow** — Must. The system shall release transfer orders (wave 16) to the warehouse, create TRANSFER_ISSUE work, ship LPs to the in-transit warehouse, and receive the same LP ids at the destination with TRANSFER_RECEIPT work, applying the transfer order's under- and over-delivery tolerances.
  - Notes: LP identity is preserved across sites; an LP not on the transfer shipment is rejected (BR-31.16); in-transit quants belong to the in-transit warehouse in the ledger; a line within under-delivery tolerance (default 0%) closes on receipt.
- **FR-31.17 Packaging specifications** — Should. The system shall define packaging specifications per item or item group, optionally per customer: levels (each, inner, case, pallet) with quantity per level, packaging material item, LP type, label template and stacking limit. Work creation, the `round_up_to_lp` strategy, cartonization at pack and VAS repack use the specification found by most-specific match.
  - Notes: determination order item+customer → item → item group+customer → item group → none; packaging material consumption posts through wave 16 when `consume_material = true`; replaces D365 unit sequence groups plus container build templates and SAP /SCWM/PACKSPEC.
- **FR-31.18 Labor standards and performance** — Should. The system shall compute an engineered labor standard (planned seconds) per work line from formulas over travel distance, quantity, LP count, weight and fixed setup time, capture actual time from work confirmations and wave-32 step telemetry, and report performance (Σ planned ÷ Σ actual × 100) per user, work class, shift and warehouse.
  - Notes: formula per BR-31.14, written in the wave-3 expression engine and effective-dated; travel distance from location coordinates (x, y, z), Manhattan by default; indirect activities (wave 32 indirect activity codes) are included or excluded per setting; PerformanceDocuments are immutable once the shift closes; incentive pay is not calculated here — closed documents export to payroll (wave 42) as a time type; named per-user views require WMS_LABOR_VIEW and `labor_personal_visibility = NAMED` (§6.31.20).
- **FR-31.19 Hazardous-material storage compatibility** — Should. The system shall hold a HazmatProfile per item (UN number, proper shipping name, class/division, packing group, storage class per TRGS 510 or NFPA 30 hazard category, water hazard class, flash point in °C, net explosive mass) and enforce storage compatibility: location profiles declare allowed storage classes and a fire-containment section, and the directive engine excludes locations where the line would sit next to an incompatible class or exceed a section quantity limit.
  - Notes: segregation per BR-31.15 against a reference-data matrix (allowed / separate / prohibited per class pair); section limits in kg or L; hazmat inventory per section is an inquiry (LX06/LX24 equivalent); wave 34 reuses the same profile for transport DG checks (FR-34.15), and wave 33 links it to SDS versions (FR-33.14).

### 6.31.4 Data model

**Location**

| Field | Type | Required | Notes |
|---|---|---|---|
| location_id | uuid | yes | PK |
| warehouse_id | uuid | yes | FK warehouse (wave 6) |
| name | text | yes | unique per warehouse, generated from LocationFormat |
| zone_id | uuid | yes | FK Zone |
| profile_id | uuid | yes | FK LocationProfile |
| aisle, rack, shelf, bin | text | no | parsed components; indexed for sort order |
| max_weight_kg, max_volume_m3, max_lps, max_items | numeric(18,6)/int | no | null = unlimited |
| pick_sequence | int | yes | travel-path ordering within zone |
| status | enum | yes | ACTIVE, BLOCKED_IN, BLOCKED_OUT, BLOCKED, INACTIVE |

**LicensePlate**

| Field | Type | Required | Notes |
|---|---|---|---|
| lp_id | text(40) | yes | PK; SSCC-18 when generated |
| warehouse_id | uuid | yes | FK |
| parent_lp_id | text(40) | no | FK LicensePlate; depth ≤ 5 (trigger-enforced) |
| location_id | uuid | no | null only when on an in-progress Work |
| in_transit_work_id | uuid | no | FK Work; mutually exclusive with location_id |
| lp_type | enum | yes | PALLET, CASE, TOTE, INNER, CONTAINER |
| gross_weight_kg, volume_m3 | numeric(18,6) | no | computed from children when nested |
| created_at | timestamptz | yes | |

**Work / WorkLine**

| Field | Type | Required | Notes |
|---|---|---|---|
| work_id | uuid | yes | PK; number sequence WRK-* (wave 5) |
| warehouse_id | uuid | yes | FK |
| work_type | enum | yes | PURCHASE_PUTAWAY, SALES_PICK, TRANSFER_ISSUE, TRANSFER_RECEIPT, PRODUCTION_PICK, RAF_PUTAWAY, REPLENISHMENT, MOVEMENT, COUNT, RETURN |
| status | enum | yes | see BR-31.3 state machine |
| priority | smallint | yes | 1 (highest)–99; default by template |
| work_class_id | uuid | yes | FK; filters eligible users |
| wave_id, load_id, order_ref | uuid/text | no | FK Wave, Load; polymorphic order ref |
| assigned_user_id, locked_at | text/timestamptz | no | pessimistic lock |
| line_no, line_type (PICK/PUT), item_id, dims jsonb, qty, uom, from_location_id, to_location_id, lp_id, target_lp_id, line_status | — | yes | WorkLine; line_status OPEN/IN_PROGRESS/COMPLETED/SKIPPED/CANCELLED |

**LocationDirective**

| Field | Type | Required | Notes |
|---|---|---|---|
| directive_id | uuid | yes | PK |
| sequence | int | yes | evaluation order; unique per (work_type, warehouse, direction) |
| work_type, direction | enum | yes | direction PUT or PICK |
| warehouse_id | uuid | yes | |
| query | jsonb | yes | metadata query (wave 3 query model) against order line |
| lines | jsonb[] | yes | each: seq, from_qty, to_qty, uom, locate_packing_qty bool, restrict_by_uom |
| actions | jsonb[] | yes | per line: seq, strategy, fixed_location bool, location_query, allow_negative |
| multiple_sku | bool | yes | true = directive used for multi-SKU LPs |

Invariants: Σ quant qty per (item, dims, warehouse) in `wms_db` equals inventory ledger physical on-hand for that warehouse at every ledger commit (eventual, reconciled daily and after each work completion); an LP is at exactly one location or on exactly one Work; location capacity ≥ Σ (quants + inbound in-progress work) at all times (property test); a WorkLine PUT always follows its PICK in `line_no` order within the same work. Indexing: `quant(warehouse_id, item_id, dims_hash, location_id)`; partial index `work(warehouse_id, priority, created_at) WHERE status = 'OPEN'`; `location(zone_id, pick_sequence)`; `license_plate(parent_lp_id)`. `wms_db` is partitioned by `tenant_id` list partitions over RLS.

### 6.31.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /wms/v1/locations:bulk | Create/update locations from format | Idempotency-Key header, 24 h | WMS_SETUP_MAINTAIN |
| POST | /wms/v1/location-directives | Create directive | key | WMS_SETUP_MAINTAIN |
| POST | /wms/v1/location-directives:simulate | Evaluate directives for a hypothetical line; returns trace | n/a (read) | WMS_SETUP_VIEW |
| POST | /wms/v1/waves/{id}:release | Release wave (starts Temporal workflow) | key; second call returns workflow id | WMS_WAVE_RELEASE |
| POST | /wms/v1/work:assign | Assign next work for user by filter | key per user+device | WMS_WORK_EXECUTE |
| POST | /wms/v1/work/{id}/lines/{n}:confirm | Confirm pick/put with actual qty, LP, location | key | WMS_WORK_EXECUTE |
| POST | /wms/v1/work/{id}:exception | Short pick, skip, cancel with reason | key | WMS_WORK_EXECUTE |
| POST | /wms/v1/receipts:from-asn | Register receipt from ASN packing structure | key = ASN id | WMS_RECEIVE |
| POST | /wms/v1/loads/{id}:confirm-shipment | Confirm load, post packing slips | key | WMS_SHIP_CONFIRM |
| GET | /wms/v1/quants?item=&location=&lp= | On-hand inquiry | — | WMS_INQUIRY |
| POST | /wms/v1/replenishment:run | Run min/max for a warehouse | key = warehouse+minute | WMS_REPLENISH |
| POST | /wms/v1/cross-dock-templates | Create or version a cross-dock template | key | WMS_SETUP_MAINTAIN |
| POST | /wms/v1/sort-stations/{id}:scan | Return the sort position for a scanned LP | — | WMS_WORK_EXECUTE |
| POST | /wms/v1/vas-orders | Create a VAS or kit order | key | WMS_VAS_MAINTAIN |
| POST | /wms/v1/labor/standards | Create or version a labor standard formula | key | WMS_LABOR_ADMIN |
| GET | /wms/v1/labor/performance?user=&shift= | Performance by user, work class or shift | — | WMS_LABOR_VIEW |
| POST | /wms/v1/hazmat-profiles | Maintain a hazmat profile | key | WMS_HAZMAT_MAINTAIN |

Commands (CQRS): `CreateWorkFromOrderLines` validates all lines belong to one warehouse and are released; `ConfirmWorkLine` validates qty ≤ open qty + over-pick tolerance, LP exists or is new at a PUT, location status allows direction, capacity; `ReleaseWave` validates template limits before starting the workflow.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.wms.work.created.v1 | Work header committed | work_id, work_type, warehouse_id, priority, wave_id, lines[] | wms-mobile, analytics |
| dev.passage.wms.work.completed.v1 | last line confirmed | work_id, lines[{item, dims, qty, from, to, lp}], user, duration_s | inventory (post transfer), sales-fulfillment, production |
| dev.passage.wms.wave.released.v1 | workflow finished | wave_id, orders, work_count, replenishment_count, failures[] | analytics, sales-fulfillment |
| dev.passage.wms.load.shipped.v1 | load confirmed | load_id, shipment_ids, lp_ids, carrier_id, ship_datetime | tms, edi, sales-fulfillment |
| dev.passage.wms.receipt.registered.v1 | receipt committed | receipt_id, po_line_refs, lp_ids, qty by item | purchasing, quality |

### 6.31.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Warehouse setup workspace | Locations grid, profiles, zones, formats; generate locations | name, zone, profile, capacity, status | WMS_SETUP_MAINTAIN |
| Location directive designer | Order directives, add lines/actions, simulate | sequence, work type, query, strategy, trace | WMS_SETUP_MAINTAIN |
| Wave workbench | Create/release/hold waves, view failures | wave id, template, lines, weight, status, failures | WMS_WAVE_RELEASE |
| Work list | Filter, reassign, change priority, cancel | work id, type, status, user, age, lines | WMS_WORK_SUPERVISE |
| Load planning workbench | Build loads from shipments, assign door, confirm | load, shipments, weight/volume, door, appointment | WMS_SHIP_CONFIRM |
| Receiving workspace | Receive from ASN/PO, create LPs, print | PO, ASN, LP, item, qty, discrepancy | WMS_RECEIVE |
| On-hand by location/LP | Inquiry, movement, LP tree | item, dims, location, LP, qty, status | WMS_INQUIRY |

Interaction rules: grids support keyboard range selection and bulk priority change (max 500 rows per action); directive designer validates on save and shows a simulation trace (rule hit/miss per step); personalization (column sets, saved filters) per user from wave 5 shell; destructive actions (cancel work with inventory in transit) require a reason code and confirmation.

### 6.31.7 Business rules and edge cases

- **BR-31.1 Location directive evaluation algorithm.** For a work line (item, dims, qty, uom, order attributes), evaluate: (1) select directives where `work_type`, `direction`, `warehouse` match and `query` is true, ordered by `sequence`; (2) within a directive, select the first line where `from_qty ≤ qty_in_line_uom < to_qty` (uom converted via wave 6 conversions; `locate_packing_qty` uses the item's packing hierarchy); (3) evaluate actions of that line in `seq` order: each action produces candidate locations = locations matching `location_query` ∩ status allows direction ∩ (for PUT) capacity remaining ≥ line qty/weight/volume ∩ profile mixing rules; the strategy orders candidates (`consolidate`: locations already holding item+dims, then by pick_sequence; `empty_location`: empty only; `fifo`/`fefo` for PICK: quants by receipt date / expiry ascending, tie-break pick_sequence; `round_up_to_lp`: full-LP quantities first); (4) the first action yielding ≥ 1 candidate wins and the top candidate is returned; (5) if no action of the matched line yields a candidate, continue with the next *directive* (lines within a directive are mutually exclusive by quantity band, so no other line of the same directive is tried); (6) if no directive yields a location, the line is created with `to_location = null` and work status `BLOCKED_NO_LOCATION`, and the wave lists it as a failure. Worked example: PUT line, item `BOLT-M8`, 1,200 EA, packing 100/case, 12 cases/pallet. Directives for PURCHASE_PUTAWAY in `WH1`: seq 10 query `item_group = FASTENERS`; seq 20 catch-all. Directive 10 lines: L1 0–99 EA (actions: A1 consolidate in zone `FWD`), L2 100–1199 EA (A1 consolidate zone `CASE`, A2 empty_location zone `CASE`), L3 1200–∞ EA (A1 empty_location zone `RESERVE` fixed pallet locations, A2 empty_location zone `OVERFLOW`). Qty 1,200 falls in L3. A1 candidates: RESERVE locations with status ACTIVE, 0 LPs, max_weight ≥ 1,200 × 0.012 kg = 14.4 kg → `R-05-02-01` (pick_sequence 5021) wins. If RESERVE were full, A2 would choose the first empty OVERFLOW location; if none, directive 20 is tried; if it also fails, the line is BLOCKED_NO_LOCATION.
- **BR-31.2 Capacity arithmetic.** Remaining capacity = max − Σ(quant) − Σ(in-progress PUT lines targeting the location). Rounding: weight and volume to 6 decimals; LP count and item count are integers. A location with remaining weight 0.000001 kg does not accept a 0.000002 kg line.
- **BR-31.3 Work state machine.** `OPEN → ASSIGNED → IN_PROGRESS → COMPLETED`; `OPEN|ASSIGNED → CANCELLED` (only if no line confirmed); `IN_PROGRESS → PARTIALLY_COMPLETED` (short pick; remainder re-queued as new OPEN work with `parent_work_id`); `ASSIGNED → OPEN` on unassign or lock timeout (default 30 min idle); `BLOCKED_NO_LOCATION → OPEN` after a re-evaluation succeeds. No transition out of COMPLETED; reversal creates compensating MOVEMENT work.
- **BR-31.4 Wave creation state machine.** `CREATED → RELEASING → RELEASED` or `→ RELEASE_FAILED` (any line failed and template `fail_whole_wave = true`) or `→ RELEASED_WITH_ERRORS`; `RELEASED → HELD → RELEASED`; `CREATED|RELEASED_WITH_ERRORS → CANCELLED` reverses all unstarted work.
- **BR-31.5 Replenishment precedence.** Pick work whose source location has insufficient quant after replenishment demand is created carries `depends_on_work_id`; it is not assignable until the dependency is COMPLETED. Dependency cycles are impossible because replenishment sources are restricted to zones not used as pick zones in the same directive.
- **BR-31.6 FEFO with equal expiry** breaks ties by receipt timestamp then pick_sequence; expiry is compared as a date in the warehouse's time zone.
- **BR-31.7 Over-pick tolerance** default 0%; max configurable 10%; over-pick never applies to serial-tracked items or to PICK lines sourcing from a quant with `quality_status ≠ ACCEPTED` (wave 33).
- **BR-31.8 Concurrency.** Work assignment uses `FOR UPDATE SKIP LOCKED` on the open-work partial index; quant updates use optimistic `version` with retry 3×; two confirmations of the same line with the same idempotency key return the first result; with different keys the second fails `409 LINE_ALREADY_CONFIRMED`.
- **BR-31.9 Nested LP moves** are a single transaction updating `location_id` on the root only; children derive location from the root (`location_id` null on children with a parent). Un-nesting a child sets its own location to the parent's current location.
- **BR-31.10 ASN variance.** Received qty outside ±2% (configurable) of ASN qty creates a `ReceiptDiscrepancy` with reason; receipt still posts actual qty.
- **BR-31.11 Cross-dock allocation.** At receipt registration of quantity R for item i: candidate demand D = released outbound lines for i in the same warehouse with `ship_date ≤ receipt_date + window` and open unpicked quantity > 0, ordered by template priority, then ship date, then order line id; allocate greedily; cross-dock quantity X = min(R, Σ D_open); R − X follows put-away directives. Planned links are consumed before opportunistic allocation. Supply with `quality_status ≠ ACCEPTED` (wave 33) is never cross-docked; its CROSS_DOCK work is created after the accepting usage decision if the demand is still open.
- **BR-31.12 Kit assembly posting.** One ledger transaction issues every component at BOM quantity × kit quantity (BOM scrap % applied, rounded up to the component UoM decimals) and receives the kit; failure of any component issue rolls back the whole posting; kit cost = Σ component cost + VAS labor at standard rate (wave 20).
- **BR-31.13 Packaging specification freeze.** The specification is determined at work creation and stored on the work line (`packaging_spec_id`, `version`); changing a specification does not alter open work.
- **BR-31.14 Labor standard formula.** planned_s = setup_s + travel_m × s_per_m + qty_base_uom × s_per_unit + lps × s_per_lp + weight_kg × s_per_kg, rounded to 0.1 s; actual_s = confirmed_at − started_at minus pauses longer than `labor_indirect_threshold_s`, which are logged as indirect time; a line with actual_s < 0.2 × planned_s or > 5 × planned_s is an outlier and excluded from performance.
- **BR-31.15 Hazmat segregation.** For a PUT candidate location L in section S: reject if any quant in S has class c with matrix(c, c_new) = prohibited; reject if matrix = separate and L is in the same location group (rack) as such a quant; reject if Σ hazmat quantity in S + line quantity > section limit. Each rejection appears in the simulation trace, e.g. `hazmat: class 3 vs 5.1 prohibited in FCS-02`.
- **BR-31.16 Transfer LP integrity.** TRANSFER_RECEIPT confirms only LPs on the shipped transfer; quantity variance per LP beyond tolerance creates a ReceiptDiscrepancy under BR-31.10; the in-transit quant is relieved exactly once.

### 6.31.8 Acceptance criteria

- **AC-31.1** (FR-31.3) Given the 100-scenario directive fixture, when each line is simulated via `/location-directives:simulate`, then every result equals the expected location and the trace shows the expected winning directive/line/action.
- **AC-31.2** (FR-31.1, BR-31.2) Given a location with max_lps = 1 holding one LP, when a PUT line targets it, then the action skips it and the simulation trace states `capacity: lps 1/1`.
- **AC-31.3** (FR-31.5, FR-31.6) Given 500 released sales orders (2,300 lines) and the demo directives, when the wave is released, then 2,300 pick lines and the expected 41 replenishment works are created, no work is BLOCKED_NO_LOCATION, and the wave is RELEASED within 120 s.
- **AC-31.4** (FR-31.7, BR-31.8) Given 50 concurrent users calling `work:assign`, when 1,000 open works exist, then no work id is returned to two users and p95 latency < 1 s.
- **AC-31.5** (FR-31.7, BR-31.3) Given an IN_PROGRESS pick of 10 EA, when the user short-picks 6 with reason `EMPTY`, then the work becomes PARTIALLY_COMPLETED, a new OPEN work for 4 EA exists with parent reference, and the inventory ledger shows a 6 EA transfer only.
- **AC-31.6** (FR-31.2, BR-31.9) Given a pallet LP with 12 case LPs, when the pallet is moved, then all 13 LPs report the new location and one `work.completed` event is emitted.
- **AC-31.7** (FR-31.9) Given an ASN with 3 pallets, when the receiver confirms pallet 2 with qty 95 of 100, then a discrepancy of −5 (−5%) is recorded and quants exist for pallet 2 only.
- **AC-31.8** (FR-31.8) Given door D1 with an appointment 09:00–09:45, when a second appointment 09:30–10:00 is requested, then it is rejected with `DOOR_SLOT_OVERLAP`.
- **AC-31.9** (property) For 10,000 generated work and confirmation sequences, location capacity is never exceeded and LP location/transit exclusivity holds.
- **AC-31.10** (FR-31.13, BR-31.11) Given a released sales line of 40 EA shipping tomorrow and an ASN pallet of 100 EA arriving today, when the pallet is received, then one CROSS_DOCK work of 40 EA to outbound staging and one put-away work of 60 EA exist and ATP counts the 40 EA once.
- **AC-31.11** (FR-31.14) Given a sort template by shipment with 12 positions, when 30 cartons for 8 shipments are scanned, then each carton is directed to its shipment's position and closing a position creates one outbound LP containing exactly those cartons.
- **AC-31.12** (FR-31.15, BR-31.12) Given a kit BOM of 3 components and a kit-to-order line of 10, when the VAS work is confirmed, then one ledger transaction issues 30 component units and receives 10 kits; with one component short, nothing posts and WMS-0114 is shown.
- **AC-31.13** (FR-31.18, BR-31.14) Given the labor formula fixture and 1,000 confirmed lines, when the shift closes, then planned seconds per line equal the fixture to 0.1 s and performance per user equals Σ planned ÷ Σ actual over non-outlier lines.
- **AC-31.14** (FR-31.19, BR-31.15) Given section FCS-02 holding class 5.1 oxidizers, when a class 3 flammable-liquid PUT line is simulated, then FCS-02 locations are excluded and the trace states the segregation reason.
- **AC-31.15** (FR-31.16, BR-31.16) Given a transfer of 3 LPs shipped from WH1, when WH2 receives 2 of them and scans an LP that is not on the transfer, then the foreign LP is rejected with WMS-0117, the 2 LPs are received with unchanged ids, and the third remains in transit.

### 6.31.9 Test plan and fixtures

Fixtures: `wms-demo-warehouse` (1 warehouse, 4 zones, 8,000 locations, generated from format), `directive-scenarios-100.json` (hand-authored, reviewed against D365 demo data USMF behavior), `wave-500-orders` (generated from wave-28 order fixtures), `asn-3-pallet.json`, `sim-10k-lines-day` (synthetic order stream for the simulation harness).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Directive resolution | unit/table-driven | 100 scenarios | 100% expected location |
| Capacity invariant | property (rapid/Go) | 10k sequences | zero violations |
| Work state machine | unit | all transitions | illegal transitions rejected |
| Wave release | E2E (Playwright + API) | 500 orders | work counts, < 120 s |
| Assignment contention | performance (k6) | 50 VUs | p95 < 1 s, no duplicates |
| Simulation | performance | 10k lines/day × 3 days | no deadlocked work, no BLOCKED_NO_LOCATION |
| Directive perf | performance | 50k locations | p95 < 200 ms/line |
| Nested LP | contract | API | depth 6 rejected |

Reconciliation: Σ quant per item/warehouse = inventory ledger on-hand; Σ in-transit on Work = ledger "in transit" status; load shipped quantities = packing slip quantities (wave 28).

### 6.31.10 Deployment and operations

Helm chart `passage-wms` (deployments `wms-api`, `wms-directive-engine`, `wms-worker` for Temporal activities; HPA 2–10 on CPU 70%); CloudNativePG cluster `wms-db` (3 instances, 100 GB); Atlas migrations generated from entity metadata; config: `WMS_LOCK_TIMEOUT_MIN=30`, `WMS_REPLENISH_INTERVAL_MIN=15`, `WMS_MAX_WAVE_LINES=5000`. Feature flags: `wms.advanced` per warehouse (off = wave-28 simple mode), `wms.slotting`. Scheduled jobs (Temporal schedules): min/max replenishment, automatic wave release, count plan generation (daily 02:00 warehouse-local), KPI materialization hourly. SLOs: assignment p95 < 1 s; directive simulate p95 < 200 ms; wave release of 5,000 lines < 10 min; availability 99.9%. Alerts: `WmsWorkBlockedNoLocation > 50 in 15 min`, `WmsAssignmentP95 > 2 s`, `WmsQuantLedgerDrift ≠ 0 after daily reconciliation`, `WmsWaveWorkflowFailed`. Runbook: re-evaluate blocked work; release stuck locks; reconcile drift (compare `quant` vs ledger projection, apply correction journal with approval). Rollback: Argo CD to previous digest; migrations are additive within a wave; flag `wms.advanced=false` returns a warehouse to simple mode only if no OPEN work exists.

### 6.31.11 Migration from AX / SAP

| Source | Passage entity | Transformation |
|---|---|---|
| WMSLocation, WHSLocationProfile, WHSZone, WHSLocationFormat | Location, LocationProfile, Zone, LocationFormat | parse name by format; capacity from profile; status from `WHSLocationStatus` |
| /SCWM/LAGP, /SCWM/T301, /SCWM/T302, /SCWM/T303 | Location, Zone (storage type/section), LocationProfile (bin type) | LGTYP → zone, LGBER → sub-zone attribute; capacity from LAGP `MAX_WEIGHT`/`MAX_CAPA` |
| WHSLicensePlate, WHSInventOnhand by LP | LicensePlate, Quant | parent LP via `WHSLicensePlate.ParentLicensePlateId` |
| /SCWM/HUHDR, /SCWM/HUITM, /SCWM/AQUA | LicensePlate, Quant | HU nesting via `HUHDR.TOP`/`HIGHER_LEVEL`; quants from AQUA by LGPLA |
| WHSLocationDirective/Line/Action | LocationDirective | strategy enum mapped 1:1; AX queries translated to metadata query |
| WHSWorkTemplateTable/Line, WHSWaveTemplateTable, WHSReplenishmentTemplate | WorkTemplate, WaveTemplate, ReplenishmentTemplate | direct |
| EWM put-away/removal strategies (/SCWM/T333, /SCWM/T334) | LocationDirective | storage type search sequence → ordered actions |
| WHSWorkTable/Line (status Open/In process), /SCWM/ORDIM_O | Work, WorkLine | only open work migrated at cutover; completed work is history (not migrated) |
| WHSLoadTable/Line (not shipped) | Load | open loads only |

Validation: location count and names match; on-hand by location/LP equals source on-hand snapshot (wave-16 reconciliation extended by location); open work count matches. Not migrated: closed work history (WHSWorkTable confirmed, /SCWM/ORDIM_C) — kept in the wave-46 lakehouse as archived source data; labor standards; EWM yard data (v1.x).

### 6.31.12 Dependencies, risks, open questions

Depends on: wave 30 (go-live baseline), wave 28 (shipments, packing slips), wave 18 (purchase receipts), wave 21 (production picking/RAF), wave 16 (inventory ledger, reservations, counting journal), wave 8 (GS1 company prefix, packing hierarchy), wave 6 (warehouses, calendars, UoM), wave 4 (Temporal, outbox).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Directive semantics differ subtly from D365 and confuse migrating customers | Medium | Medium | Simulation trace; parity fixtures from D365 USMF; documented differences |
| Quant/ledger drift under high concurrency | Low | High | Single posting path through inventory ledger; daily reconciliation alert |

Open questions: (1) Should quants be owned by `wms` or remain a projection of `inventory`? Proposed: projection with `wms` as the only writer of location/LP dims; decide by wave 31 design review. (2) SSCC extension digit per warehouse or per tenant? Proposed: per warehouse; decide by wave 32.

### 6.31.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| wms.advanced_enabled | warehouse | bool | false | true / false | Switches the warehouse from wave-28 simple mode to directed work | Warehouse form: "Use warehouse management processes" (InventLocation.WHSEnabled) | SPRO EWM › Master Data › Assign Warehouse Numbers (/SCWM/T300_MD) |
| lp_number_sequence | warehouse | sequence ref | SSCC-18 per warehouse | SSCC / custom pattern | Format of generated LP ids | Warehouse management parameters › Number sequences › License plate | SPRO EWM › Master Data › Define Number Ranges for Handling Units |
| sscc_extension_digit | warehouse | int | 0 | 0–9 | First digit of generated SSCCs | Warehouse management parameters › General › SSCC / GS1 setup | GS1 company prefix and SSCC number range (HU number range object) |
| lock_timeout_min | warehouse | int | 30 | 5–240 | Idle ASSIGNED work returns to OPEN (BR-31.3) | Work user session expiry | Resource type logon timeout (/SCWM/TRSRC_TYP) |
| max_wave_lines | wave template | int | 5,000 | 1–20,000 | Wave size cap (FR-31.5) | Wave template › Wave threshold | Wave template capacity profile (/SCWM/WAVETMP) |
| fail_whole_wave | wave template | bool | false | true / false | RELEASE_FAILED vs RELEASED_WITH_ERRORS (BR-31.4) | Wave template "Process wave at release" plus held wave status | Wave release method in wave template |
| over_pick_tolerance_pct | warehouse, item | numeric(5,2) | 0 | 0–10 | Over-pick limit (BR-31.7) | Mobile device menu item "Allow over-picking" with product over-pick % | Warehouse process type (/SCWM/T333) over-pick indicator |
| replenishment_interval_min | warehouse | int | 15 | 5–1,440 | Min/max run frequency (FR-31.6) | Replenishment batch job recurrence (WHSReplenishmentSchedule) | /SCWM/REPL background job variant |
| asn_variance_tolerance_pct | warehouse, vendor | numeric(5,2) | 2 | 0–20 | ReceiptDiscrepancy threshold (BR-31.10) | PurchLine over/under delivery % | Inbound delivery tolerance (MM tolerance keys T169G) |
| count_approval_tolerance | warehouse | value % / units | 1% or 5 units | ≥ 0 | Count differences above it need approval (FR-31.10) | Cycle count thresholds and approval parameters (WHSCycleCountThreshold) | SPRO EWM › Physical Inventory › Define Tolerance Groups for Difference Analyzer |
| blind_count | warehouse | bool | true | true / false | Hides expected quantity on count work | Mobile device menu item "Display expected quantity" | Physical inventory procedure without book quantity (/SCWM/PI) |
| capacity_dimensions | location profile | set | {weight, volume, lps, items} | any subset | Which dimensions BR-31.2 enforces | Location profile "Use volumetrics" and location stocking limits | Storage type capacity check method (/SCWM/T331) |
| directive_trace_retention_days | tenant | int | 30 | 1–365 | Retention of simulate/evaluation traces | Warehouse management parameters › Location directive failures logging | Application log retention (SLG1 object /SCWM/) |
| crossdock_opportunistic | warehouse | bool | false | true / false | Enables opportunistic cross-dock at receipt (FR-31.13) | Work policy "Opportunistic cross docking" | SPRO EWM › Cross-Process Settings › Cross-Docking › Opportunistic Cross-Docking › Activate |
| crossdock_supply_window_h | cross-dock template | int | 48 | 0–336 | Supply-to-demand window (BR-31.11) | Cross docking template "Re-plan frequency" and demand window | Opportunistic cross-docking settings for inbound (time horizon) |
| crossdock_demand_priority | cross-dock template | enum | SHIP_DATE | SHIP_DATE / ORDER_PRIORITY / FIFO_RELEASE | Demand ordering (BR-31.11) | Cross docking template "Demand requirement prioritization" | Push deployment strategy |
| sort_position_close_rule | sort template | enum | MANUAL | MANUAL / FULL / LAST_LP | When a sort position closes (FR-31.14) | Outbound sorting template "Close position" | Consolidation group completion rule |
| vas_station_location | warehouse | location ref | none | locations of type VAS | Default target of VAS work (FR-31.15) | Kit order location | SPRO EWM › Value-Added Services › Define Work Center for VAS (/SCWM/TWORKST) |
| packaging_spec_required | warehouse | bool | false | true / false | Blocks work creation when no specification matches (FR-31.17) | Unit sequence group mandatory on WHS items | Packaging specification determination procedure (condition technique) |
| labor_travel_metric | warehouse | enum | MANHATTAN | MANHATTAN / EUCLIDEAN / GRAPH | Travel distance method (BR-31.14) | n/a | SPRO EWM › Labor Management › Travel Distance Calculation settings |
| labor_indirect_threshold_s | warehouse | int | 120 | 30–900 | Pause length counted as indirect (BR-31.14) | n/a | Indirect labor task settings (/SCWM/LM) |
| labor_personal_visibility | legal entity | enum | AGGREGATE_ONLY | AGGREGATE_ONLY / NAMED | Whether performance is shown per named user (FR-31.18) | n/a | Authorization for employee performance documents (/SCWM/LM) |
| hazmat_segregation_matrix | tenant | reference data version | TRGS510-2023 | TRGS510 / NFPA30 / custom | Matrix used by BR-31.15 | Product compliance / hazardous material setup | SPRO EWM › Master Data › Hazardous Substances › Define Storage Classes and storage type checks |

### 6.31.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| On-hand by location / LP | list inquiry | warehouse, item, dims, location, LP, status | qty, reserved, available, LP tree, quality status | `quant` | On-hand by location, WHSInventOnhand | LS24/LS26, LX02, /SCWM/MON stock node |
| Work list and work audit | list inquiry | status, work type, user, age, wave | work id, lines, from/to, user, duration | `work`, `work_line` | All work list page, work audit templates | LT22–LT28, /SCWM/MON warehouse orders/tasks |
| Wave status and failures | drill-down | wave, template, status, date | lines, work count, replenishment count, failures by reason | `wave` | Wave details (WHSWaveTable) | /SCWM/WAVE, LT41/LT42 |
| Location capacity and empty bins | operational report | zone, profile, utilization % | used/max per dimension, empty flag | `location` + `quant` | Location load, location stocking limits inquiry | LX01, LX04, /SCWM/MON storage bins |
| Directive failure trace | drill-down | work, wave, date range | directive, line, action, rejection reason | directive trace store | Location directive failures (D365) | Application log SLG1 (no dedicated report) |
| Cycle count variances | operational report | warehouse, period, counter | expected, counted, difference qty and value, approver | count work | Cycle count work approval, SSRS count sheets | LX17, LX18, LI20 |
| Quant–ledger reconciliation | operational report | warehouse, date | Σ quant vs ledger on-hand, drift per item | reconciliation job output | Manual InventSum vs WHS comparison | LX23 stock comparison IM–WM, EWM–ERP stock comparison |
| Cross-docking report | operational report | warehouse, date, template | supply, demand, qty cross-docked, dwell minutes, missed opportunities | `crossdock_link` | SSRS Cross docking report | LXDCA cross-docking monitor, /SCWM/MON cross-docking node |
| Sort position status | list inquiry | sort station, status | position, LPs, fill %, open/closed, target shipment | `sort_position` | Outbound sorting position inquiry | /SCWM/MON consolidation groups |
| VAS order backlog | list inquiry | station, status, due date | order, steps, estimated vs actual time | `vas_order` | Kit orders list | /SCWM/VASEXEC worklist |
| Labor performance | KPI | warehouse, shift, work class, user (duty-gated) | planned s, actual s, performance %, indirect %, outliers | `labor_line` | Warehouse workload visualization (no standards) | /SCWM/MON labor management nodes, performance documents |
| Hazmat inventory by fire-containment section | operational / statutory report | warehouse, section, class | qty kg/L vs limit, classes present, emergency contact | `quant` + `hazmat_profile` | Product compliance regulated products inquiry | LX06, LX24 |
| Warehouse KPIs | KPI | warehouse, hour/day | lines/h, dock-to-stock min, pick accuracy %, short-pick rate, replenishment lead time | Cube | Warehouse management workspace tiles | Fiori Warehouse KPIs, LX47 |

### 6.31.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Pick list (work document) | Work created or printed on demand | Header: work id barcode, wave, priority, warehouse; lines: sequence, from location, item, lot, qty, to location; footer: line count, total weight | PDF; ZPL variant | WRK-* (wave 5) | Operational, 90 days |
| Load manifest | Load confirmed | Shipper, consignee, carrier SCAC, LPs/SSCCs, weights, hazmat lines with proper shipping names (FR-31.19) | PDF | Load id | Kept with the shipment record (wave 28 retention) |
| Count sheet | Count work created | Location, item, lot, expected qty (blank for blind counts), counted qty field | PDF | COUNT-* | Count adjustments are ledger records (wave 16 retention, 10 years) |
| Cross-dock tag | CROSS_DOCK work created | Outbound order, ship date, door, SSCC | ZPL | none | None |
| Kit assembly ticket | VAS order released | Kit BOM, component locations, VAS steps, kit LP | PDF | VAS-* | Operational, 90 days |
| Hazmat section inventory list | On demand and daily | Section, classes, qty vs limit, emergency contact | PDF / CSV | none | Supplied to fire authority on request; kept 1 year |
| Labor performance document | Shift close | User, planned, actual, performance, indirect time | PDF / CSV export to payroll | LPD-* | Employee data retention per wave 42, default 2 years |

### 6.31.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Min/max replenishment | Every 15 min | warehouse | Key warehouse + minute | < 30 s at 50,000 locations | Retry 3×, then skip to next interval | WmsReplenishRunFailed |
| Automatic wave release | Per wave template cron | wave template | Temporal workflow id per wave | 500 orders < 120 s | Per-line failures; RELEASED_WITH_ERRORS | WmsWaveWorkflowFailed |
| Count plan generation | Daily 02:00 warehouse-local | warehouse × plan | plan id + date | < 5 min | Resume from last generated location | Zero works generated for an active plan |
| Quant–ledger reconciliation | Daily 03:00 | warehouse × 16 item-hash partitions | snapshot timestamp | < 15 min for 2 M quants | Report only; corrections need approval | WmsQuantLedgerDrift ≠ 0 |
| Planned cross-dock re-plan | Every 30 min | warehouse | link version | < 60 s | Keep previous links on failure | WmsCrossDockReplanFailed |
| Labor shift close | Shift end + 30 min | warehouse × shift | shift id | < 2 min | Idempotent rerun | Shift not closed after 2 h |
| KPI materialization | Hourly | warehouse | hour bucket | < 2 min | Recompute bucket | KPI lag > 60 min |
| Empty LP and work history cleanup | Weekly Sunday 01:00 | warehouse | LP id / work id | < 30 min | Resume by id | none (log) |
| Directive trace purge | Daily 04:00 | tenant | trace date | < 5 min | Retry next day | none (log) |

### 6.31.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| WMS_SETUP_MAINTAIN | /wms/v1/locations:bulk, /location-directives, /cross-dock-templates; setup workspace; directive designer | WMS_COUNT_APPROVE (could change locations, then approve the resulting differences) | Warehouse administrator |
| WMS_SETUP_VIEW | /location-directives:simulate; setup screens read-only | none | Warehouse manager, supervisor |
| WMS_WAVE_RELEASE | /waves/{id}:release; wave workbench | none | Warehouse supervisor, warehouse manager |
| WMS_WORK_EXECUTE | /work:assign, /lines/{n}:confirm, /work/{id}:exception, /sort-stations/{id}:scan | WMS_COUNT_APPROVE on the same count document (approver ≠ counter, enforced) | Warehouse worker |
| WMS_WORK_SUPERVISE | Work list reassign, priority change, cancel, blocked-work re-evaluation | none | Warehouse supervisor |
| WMS_RECEIVE | /receipts:from-asn; receiving workspace | AP_INVOICE_APPROVE (wave 19): receiving and approving the same vendor's invoice | Receiving clerk |
| WMS_SHIP_CONFIRM | /loads/{id}:confirm-shipment; load planning workbench | none in wave; flagged with CUST_CREDIT_RELEASE (wave 26) | Shipping clerk |
| WMS_COUNT_APPROVE | Count approval screen; counting journal post | WMS_WORK_EXECUTE on same count; WMS_SETUP_MAINTAIN | Inventory controller |
| WMS_REPLENISH | /replenishment:run | none | Warehouse supervisor |
| WMS_VAS_MAINTAIN | /vas-orders; VAS backlog | none | VAS lead |
| WMS_LABOR_ADMIN | /labor/standards; formula designer | WMS_WORK_EXECUTE (could tune the standard for own work) | Labor analyst |
| WMS_LABOR_VIEW | /labor/performance with named users | none; available only where `labor_personal_visibility = NAMED` | Warehouse manager |
| WMS_HAZMAT_MAINTAIN | /hazmat-profiles; segregation matrix; section limits | none | EHS officer |
| WMS_INQUIRY | GET /wms/v1/quants and all inquiries above except labor | none | All warehouse roles |

### 6.31.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| WMS-0101 | error | No location found for {qty} {uom} of {item}; work {work} is blocked. | No directive action yielded a candidate (BLOCKED_NO_LOCATION) | Run simulate, adjust directive or free capacity, then re-evaluate | BR-31.1 |
| WMS-0102 | error | Location {loc} capacity exceeded: {dimension} {used}/{max}. | PUT would exceed remaining capacity | Choose another location or split the LP | BR-31.2 |
| WMS-0103 | error | Location {loc} does not allow mixed items. | Profile `allow_mixed_items = false` | Use an empty or same-item location | FR-31.1 |
| WMS-0104 | error | License plate nesting would exceed 5 levels. | Nest depth 6 attempted | Un-nest an intermediate level | FR-31.2 |
| WMS-0105 | error | Line {n} of work {work} is already confirmed. | Second confirmation with a different key (409 LINE_ALREADY_CONFIRMED) | Refresh the work | BR-31.8 |
| WMS-0106 | error | Quantity {qty} exceeds open quantity plus over-pick tolerance {tol}%. | Over-pick beyond tolerance or serial item | Confirm the open quantity | BR-31.7 |
| WMS-0107 | warning | Received {qty} differs from ASN {asn_qty} by {pct}%; discrepancy recorded. | Variance beyond ASN tolerance | Review the discrepancy with purchasing | BR-31.10 |
| WMS-0108 | error | Door {door} is already booked {from}–{to}. | Overlapping appointment (DOOR_SLOT_OVERLAP) | Pick another slot or door | FR-31.8 |
| WMS-0109 | info | Pick work {work} waits for replenishment {repl}. | Dependency on open replenishment | None; work becomes assignable automatically | BR-31.5 |
| WMS-0110 | error | Transition {from} → {to} is not allowed for work {work}. | Illegal state change | Use cancel or compensating movement | BR-31.3 |
| WMS-0111 | warning | Count difference {diff} exceeds tolerance; supervisor approval required. | Above count tolerance | Approve or recount | FR-31.10 |
| WMS-0112 | info | {qty} cross-docked to {loc} for order {order}; {rest} to put-away. | Cross-dock allocation | None | BR-31.11 |
| WMS-0113 | warning | Supply is under quality hold; cross-dock deferred to usage decision. | Supply not ACCEPTED | Wait for usage decision | BR-31.11 |
| WMS-0114 | error | Kit component {item} short by {qty}; kit assembly not posted. | Component issue failed | Replenish component or reduce kit qty | BR-31.12 |
| WMS-0115 | error | Storage class {c1} cannot be stored with {c2} in section {fcs}. | Segregation matrix prohibits | Use another fire-containment section | BR-31.15 |
| WMS-0116 | error | Section {fcs} limit {limit} {uom} would be exceeded. | Section quantity limit | Move stock or use another section | BR-31.15 |
| WMS-0117 | error | License plate {lp} is not on transfer shipment {ship}. | Foreign LP scanned at transfer receipt | Receive against the correct transfer | BR-31.16 |
| WMS-0118 | warning | Labor line {line} excluded as outlier (actual {a} s vs planned {p} s). | Outside 0.2×–5× planned | Review standard or confirmation | BR-31.14 |
| WMS-0119 | error | Daily reconciliation found drift for {n} item/warehouse pairs. | Quant ≠ ledger on-hand | Run drift runbook; approve correction journal | §6.31.10 |

### 6.31.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Directive evaluation | 10,000 lines/day; peak 500 lines/min | 40 / 200 / 350 ms per line | 50 lines/s per directive-engine pod | Traces ≈ 3 GB (30-day rolling) | Candidate pre-filter by zone and profile; `location(zone_id, pick_sequence)` | Wave workflow queues lines; release time grows; no line dropped |
| Work assignment | 50 concurrent users; 3,000 assigns/h | 80 / 400 / 1,000 ms | 200 assigns/s | — | Partial index on OPEN work | SKIP LOCKED keeps correctness; alert at p95 > 2 s |
| Line confirmation | 30,000/day; peak 1,200/min | 60 / 250 / 600 ms | 100/s per pod | Work + lines ≈ 15 GB | Monthly range partitions on `created_at` inside tenant list partitions | HPA to 10 pods; beyond, 429 with Retry-After (mobile queues offline) |
| Wave release | 50 waves/day, ≤ 5,000 lines each | 2,300 lines < 120 s; 5,000 lines < 10 min | 40 lines/s | — | Per-order Temporal activities | Task-queue backlog; scheduled waves start late; alert at 15 min delay |
| Cross-dock allocation at receipt | 2,000 receipts/day | +50 / 150 / 300 ms on receipt | 30/s | Links ≈ 0.5 GB | `demand(warehouse_id, item_id, ship_date) WHERE open` | Falls back to put-away and records a missed opportunity |
| Sort scan | 20,000 cartons/day; 30/min per station | 40 / 150 / 300 ms | 20/s per station | — | In-memory position map per station | Correct but slower; latency shown on the station screen |
| Labor shift close | 3 shifts × 200 users | < 2 min per shift | — | Performance documents ≈ 1 GB | `labor_line` monthly partitions | Close delayed; no data loss |
| Quant reconciliation | 2 M quants | < 15 min | 16 parallel partitions | Reports < 100 MB | Hash on item id | Runtime grows linearly; alert only on drift |

### 6.31.20 Localization and compliance hooks

- **Hazardous-material storage.** Segregation and quantity limits differ by jurisdiction: Germany TRGS 510 storage classes (LGK 1–13) with water hazard classes under AwSV; United States NFPA 30, International Fire Code chapter 57 and OSHA 29 CFR 1910.106 for flammable liquids; United Kingdom HSG71 and DSEAR. Country packs (wave 48) supply the segregation matrix and section limits through the `WarehouseCompliancePack` extension point (versioned reference data; BR-31.15 is regulation-neutral).
- **Labor monitoring.** Per-person performance data is personal data under GDPR (Art. 88; DPIA under Art. 35); Germany requires works-council co-determination (BetrVG §87(1) no. 6); France requires CSE consultation. EU legal entities default to `labor_personal_visibility = AGGREGATE_ONLY`; the country pack sets defaults and an OPA policy bundle (`LaborPrivacyPolicy`) gates the named view.
- **Physical inventory.** Statutory stock-take rules (Germany HGB §241(2) permanent inventory requires each item counted at least once per fiscal year) are checked by a count-plan coverage report supplied by the country pack (`InventoryCountPolicy`).
- **GS1 and units.** SSCC company prefixes come from the GS1 member organization of the legal entity; weights and volumes are stored in SI and displayed in kg/lb and m³/ft³ by user locale (wave 5).
- **Retention.** Work, count and transfer records that support inventory valuation follow the wave-46 archive policy (10 years default; Germany GoBD/HGB §257, United States 7 years typical).
- **Calendars and time zones.** FEFO date comparison (BR-31.6), shift close and count plans use the warehouse time zone and wave-6 calendars.

### 6.31.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX PARTIAL #217 — §12.26.1 Outbound → Cross docking (WHSCrossDocking) | PARTIAL-31 | ADDED as FR-31.13 | Planned and opportunistic; BR-31.11, AC-31.10 |
| AX PARTIAL #218 — §12.26.1 WHS → Transfer order WHS flow (ship/receive with LP) | PARTIAL-31 | ADDED as FR-31.16 | Transfer order document itself is wave 16; BR-31.16, AC-31.15 |
| AX PARTIAL #219 — §12.26.2 SSRS: Cross docking report | PARTIAL-31 | ADDED as FR-31.13 | "Cross-docking report" in §6.31.14 |
| AX PARTIAL #220 — §12.26.3 Cross docking → Planned cross docking processing | PARTIAL-31 | ADDED as FR-31.13 | "Planned cross-dock re-plan" job in §6.31.16 |
| AX PARTIAL #221 — §12.26.4 Outbound → Cross docking templates | PARTIAL-31 | ADDED as FR-31.13 | `crossdock_*` parameters in §6.31.13 |
| AX PARTIAL #222 — §12.26.4 Outbound → Outbound sorting (sort positions) | PARTIAL-31 | ADDED as FR-31.14 | AC-31.11 |
| SAP #187 — §13.16 LXDCA / LXDCK planned cross-docking | GAP (propose 31) | ADDED as FR-31.13 | Directive strategy `outbound_staging` as the register proposed |
| SAP #188 — §13.16 /SCWM/PACK, /SCWM/PACKSPEC, /SCWM/PSSTAGE packaging specifications | PARTIAL-31 | ADDED as FR-31.17 | Packing station flow stays FR-32.4 with wave-28 containers |
| SAP #189 — §13.16 /SCWM/VASEXEC VAS orders | GAP (propose 31) | ADDED as FR-31.15 | Work type VAS; charge code `VAS` to wave 29 |
| SAP #190 — §13.16 /SCWM/LM labor management | PARTIAL-31 | ADDED as FR-31.18 | Incentive pay calculation OUT-OF-SCOPE: performance documents export to payroll (wave 42) |
| SAP #191 — §13.16 /SCWM/YM_* yard management | PARTIAL-31 | DEFERRED to wave 34 | ADDED there as FR-34.13; AX register also proposes wave 34 |
| SAP #194 — §13.16 EWM kit-to-stock / kit-to-order | GAP (propose 31) | ADDED as FR-31.15 | BR-31.12, AC-31.12 |
| SAP #196 — §13.16 LX06 / LX07 fire-containment sections / check storage | PARTIAL-31 | ADDED as FR-31.19 | LX07 consistency check is covered by quant–ledger reconciliation (§6.31.16) |
| SAP #197 — §13.16 /SCWM/LM labor reports (planned vs actual) | PARTIAL-31 | ADDED as FR-31.18 | "Labor performance" in §6.31.14 |
| SAP #198 — §13.16 Labor management period close (/SCWM/LM) | PARTIAL-31 | ADDED as FR-31.18 | "Labor shift close" job in §6.31.16 |
| SAP #200 — §13.16 EWM yard management customizing | PARTIAL-31 | DEFERRED to wave 34 | Yard parameters in §6.34.13 |
| SAP #202 — §13.16 Labor management: formulas, ELS, incentive | PARTIAL-31 | ADDED as FR-31.18 | Formulas and ELS added (BR-31.14); incentive OUT-OF-SCOPE as for #190 |

## 6.32 Wave 32: Mobile Warehouse Execution (Scanners, RF, Voice)

### 6.32.1 Scope and business context

Wave 31 creates work; wave 32 lets warehouse workers execute it on handheld scanners. This replaces the D365 Warehouse Management mobile app (mobile device menus, menu items and steps) and the SAP EWM RF framework (/SCWM/RFUI, ITSmobile). After this wave a worker signs in on a Zebra or Honeywell Android device (or any PWA-capable browser), picks from a configured menu, scans GS1-128 labels that the system parses into LP/GTIN/lot/expiry/serial/weight, and completes receive, put-away, pick, pack, move, count and inquiry flows with offline tolerance. An administrator changes flows without an app release. Label printing (ZPL) ships through a print service. Out of scope: full voice-directed picking (adapter interface only; a voice vendor integration is v1.x), RFID portal reads beyond the EPC ingest endpoint, iOS native shell (PWA covers iOS).

**Scope amendment (coverage closure).** The gap registers add two items to this wave: an automation adapter that exchanges telegrams with material flow systems (AS/RS, conveyors, sorters) through the wave-25 edge gateway (FR-32.12), and management of printers and output queues, covering device types, queue failover and browser printing beyond labels (FR-32.13). §6.32.21 lists the dispositions.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| D365 mobile menu items (WHSRFMenuItemTable) expose ~60 option fields per item; a flow change (e.g., add a lot confirmation step) needs a consultant, a config move across environments and a device re-login; D365 "mobile device steps configuration" cannot add a new step, only hide/rename | Flows are hard-coded X++ step classes parameterized by options | Flow designer with versioned step graphs, simulate, publish; devices pick up the version in ≤ 60 s | Flow change lead time: 1–3 days → < 30 min; 0 app releases per flow change |
| Both D365 mobile app and EWM RF (/SCWM/RFUI, ITSmobile) stop dead without Wi-Fi; workers in freezers, yards and trailers lose scans | Request/response screens with no client state | Offline queue with ordered, idempotent replay and a supervisor conflict queue | Lost transactions on disconnect: every one → 0; worker wait on reconnect: re-key → 0 keystrokes |
| GS1-128 labels must be scanned AI by AI in older D365 and ECC RF screens (one scan for GTIN, one for lot, one for expiry); mis-typed expiry dates reach the batch master | No composite-barcode parsing in the RF screen | GS1 parser (AI 00/01/10/17/21/310n…) fills all expected fields from one scan with check-digit verification | Scans per inbound case: 3–4 → 1; expiry/lot keying errors: ~1% of receipts → 0 (no keying) |
| Label printing via D365 document routing (WHSDocumentRoutingLayout) depends on the Document Routing Agent Windows service; failures are invisible on the device | Printing routed through a client-side agent with no status feedback | In-cluster print service with job status, retries and device-visible failure | Print failure detection: minutes to hours (someone notices) → < 2 s on device |
| EWM RF screen layouts (/SCWM/TPRES) and ITSmobile templates need ABAP/HTML work for scanner model differences | Screen rendering coupled to device generation | Responsive PWA from 320 px, DataWedge/Honeywell intent bridge, keyboard-wedge fallback | New scanner model onboarding: weeks of template work → same day (profile only) |

### 6.32.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Mobile menu | WHSMobileAppMenu, WHSRFMenuTable, WHSRFMenuLine | /SCWM/TMENU, /SCWM/T_RF_MENU (RF menu manager /SCWM/RFUI) | wms-mobile.MobileMenu | full |
| Menu items / flows | WHSRFMenuItemTable (WHSRFMenuItem), work creation vs existing work modes | Logical transactions /SCWM/TLOGTR, steps /SCWM/T_RF_STEP | MobileFlow (step graph) | improved — visual designer, versioned |
| Detours / field config | Mobile device steps configuration (D365 10.0.x), WHSRFDetour | Presentation profile /SCWM/TPRES, personalization | FlowStep config, detours | full |
| Scanner integration | DataWedge via app; GS1 parsing (D365 10.0.21+) | RF barcode profiles, GS1 parsing in /SCWM/RFUI | GS1 parser service, DataWedge/Honeywell intents | full |
| Work user / session | WHSWorkUser, WHSWorkUserSessionState | Resource /SCWM/RSRC, /SCWM/TRSRC queue | DeviceSession, WorkUser (Keycloak) | full |
| Offline | n/a (online only) | n/a (online only) | OfflineTransaction queue | improved |
| Label printing | Document routing layouts (WHSDocumentRoutingLayout) to ZPL printers | Smart Forms / Adobe Forms via /SCWM/PRINT condition technique | PrintJob, ZPL templates | full |
| Voice | ISV | SAP EWM voice (ISV) | VoiceAdapter interface | partial — adapter only |
| RFID | n/a (ISV) | SAP AII / EWM RFID | EPC read endpoint | partial |
| Packing station | WHSPackingStation, container packing | Packing work center /SCWM/PACK | Pack flow + ShipmentContainer (wave 28) | full |
| Material flow system (automation) | Warehouse automation integration via business events and ISV warehouse control systems | EWM MFS /SCWM/MFS_*: PLC communication channels, telegram structures, communication points, conveyor segments, MFS monitor | wms.AutomationChannel, TelegramProfile, CommunicationPoint, TransportOrder | full |
| Output devices and queues | Network printers, Document Routing Agent, print management setup (PrintMgmtDocInstance) | SPAD output devices, device types (e.g. LB_ZEB for ZPL), access methods, spool SP01/SP02, front-end printing | Printer, DeviceType, PrintQueue, RoutingRule; browser print | full |

### 6.32.3 Functional requirements

- **FR-32.1 Mobile app shell** — Must. The system shall provide a PWA and an Android native shell (WebView + scanner intent bridge) that run the same flow runtime, with login via Keycloak device flow and PIN re-auth after 8 h.
  - Notes: supports screen widths from 320 px; large-target mode; app updates are flow metadata, not binaries; shell release only for scanner bridge changes.
- **FR-32.2 Flow designer** — Must. The system shall let an administrator compose flows from step types (`scan`, `input`, `select`, `confirm`, `display`, `branch`, `call`), with validations, scan pattern expectations and error handling, versioned and published per warehouse.
  - Notes: a step declares expected scan classes (LP, GTIN, LOCATION, ANY); publishing validates the graph (no unreachable steps, every branch terminates); devices fetch new versions on next menu load (≤ 60 s).
- **FR-32.3 GS1 parsing** — Must. The system shall parse GS1-128 and GS1 DataMatrix element strings into application identifiers per BR-32.1, and route values to the expected step fields.
  - Notes: symbology identifiers `]C1`, `]d2`, `]Q3` accepted; FNC1 as GS (0x1D); passes the GS1 AI test suite in CI.
- **FR-32.4 Core flows** — Must. The system shall ship flows: Receive (PO/ASN/transfer), Put-away (system-directed and user-directed), Pick (work-directed, cluster to 8 orders, zone), Pack, Move (LP/qty), Count (planned/spot), Inquiry (item/location/LP).
  - Notes: all flows call the wave-31 work APIs; cluster picking assigns up to 8 works to positions on a cart with per-position LP.
- **FR-32.5 Offline queue** — Must. The system shall queue confirmations locally when offline (max 500 transactions or 24 h), replay them in order on reconnect with per-transaction idempotency keys, and surface conflicts to the user.
  - Notes: queue persisted in IndexedDB encrypted with a device key; replay uses `Idempotency-Key = device_id:seq`; conflict policy per BR-32.4.
- **FR-32.6 Device and session management** — Must. The system shall register devices, show active sessions, force sign-out, and bind a device to warehouses and menus.
  - Notes: device heartbeat every 30 s; session idle timeout 30 min aligned to the work lock.
- **FR-32.7 Label printing** — Must. The system shall render ZPL from templates (LP, location, shipping, item labels) and print via a print service supporting IPP/raw TCP 9100 printers and printer selection by step/user/location.
  - Notes: templates are Handlebars-style with GS1-128 (`^BC`) and DataMatrix (`^BX`) helpers; print service retries 3× and reports status.
- **FR-32.8 Exceptions on device** — Must. The system shall let users short pick, skip, report damaged, and request supervisor override by PIN on device.
  - Notes: override reasons configurable; all exceptions logged with user, device, location, timestamp.
- **FR-32.9 Voice and RFID integration points** — Could. The system shall expose a `VoiceAdapter` interface (prompt/response per step) and an EPC ingest endpoint mapping EPC SGTIN/SSCC to GTIN/LP.
  - Notes: EPC URI parsing for `urn:epc:id:sgtin` and `urn:epc:id:sscc`; reads deduplicated in a 2 s window.
- **FR-32.10 Scanner hardware integration** — Must. The system shall receive scans from Zebra DataWedge intents and Honeywell Data Collection intents in the native shell and from keyboard wedge in the PWA.
  - Notes: a scan is accepted only on the focused scan step; symbology prefix enabled in DataWedge profile provisioned by the shell.
- **FR-32.11 Telemetry** — Should. The system shall record per-step timing and error counts per device and user for the KPI layer.
  - Notes: OpenTelemetry spans per step; retention 90 days.
- **FR-32.12 Automation adapter (AS/RS, conveyors, sorters)** — Should. The system shall exchange work with material flow systems: send transport orders (LP, source point, destination point, priority) as telegrams to a PLC or warehouse control system, receive handshake, arrival, scan-result and fault telegrams, and confirm or raise exceptions on wms work lines accordingly.
  - Notes: transports through the wave-25 edge gateway on site: OPC UA method calls, MQTT 5 (Sparkplug B payloads optional) or raw TCP fixed-length telegrams with an EWM-style header (sender, receiver, sequence, telegram type, handshake flag); telegram layouts are versioned metadata (field, offset, length, type, padding); communication points map to wms locations; LIFE telegram every 10 s, channel DOWN after 3 missed; an LP failing scan or contour check at an identification point is routed to the reject point and raises MOB-0116.
- **FR-32.13 Printer and output-queue management** — Should. The system shall register output devices per site with a device type (ZPL II, PDF over IPP, PCL 6; 203/300/600 dpi; media sizes), group them into queues with failover, route every document type (labels and office documents such as pick lists, CoAs and bills of lading) by rule, and offer browser printing of PDF documents to users without a routed printer.
  - Notes: routing precedence per BR-32.12; a queue fails over to the next device after 2 failed attempts; job log kept 30 days; reprints carry `reprint_of` (BR-32.8); printers on site networks are reached by the site print-service pod only (no inbound connections to the cluster).

### 6.32.4 Data model

**MobileFlow (versioned)**

| Field | Type | Required | Notes |
|---|---|---|---|
| flow_id | uuid | yes | PK |
| code | text | yes | unique per tenant (e.g., `PICK_CLUSTER`) |
| version | int | yes | increments on publish |
| status | enum | yes | DRAFT, PUBLISHED, RETIRED |
| steps | jsonb | yes | array of {id, type, prompt, expected_scan[], validations[], next{}} |
| entry_step_id | text | yes | |
| warehouses | uuid[] | yes | scope |
| published_at, published_by | timestamptz/text | no | |

**DeviceSession**

| Field | Type | Required | Notes |
|---|---|---|---|
| session_id | uuid | yes | PK |
| device_id | text | yes | FK Device (serial or generated) |
| user_id | text | yes | Keycloak subject |
| warehouse_id | uuid | yes | |
| menu_id | uuid | yes | |
| current_work_id | uuid | no | FK wms.Work |
| last_seen_at | timestamptz | yes | heartbeat |
| state | jsonb | yes | step pointer and field values |

**OfflineTransaction**

| Field | Type | Required | Notes |
|---|---|---|---|
| idempotency_key | text | yes | PK `device_id:seq` |
| device_id, session_id | text/uuid | yes | |
| seq | bigint | yes | monotonic per device |
| command | text | yes | e.g., `ConfirmWorkLine` |
| payload | jsonb | yes | |
| captured_at | timestamptz | yes | device clock + server skew |
| replay_status | enum | yes | PENDING, APPLIED, CONFLICT, REJECTED |
| conflict_reason | text | no | |

**PrintJob**: job_id, template_id, printer_id, zpl text, status (QUEUED/SENT/PRINTED/FAILED), attempts, created_by.

Invariants: a device has at most one active session; `seq` is strictly increasing per device and gaps are rejected; a PUBLISHED flow version is immutable. Indexing: `offline_transaction(device_id, seq)`; `device_session(user_id) WHERE state_active`.

### 6.32.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /mobile/v1/sessions | Start session (device, warehouse, menu) | key = device_id | WMS_WORK_EXECUTE |
| GET | /mobile/v1/menus/{id} | Menu with published flow versions | — | WMS_WORK_EXECUTE |
| POST | /mobile/v1/flows/{code}/steps/{id}:submit | Submit step input (scan or value) | key | WMS_WORK_EXECUTE |
| POST | /mobile/v1/scan:parse | Parse a barcode into AI map | — | WMS_WORK_EXECUTE |
| POST | /mobile/v1/offline:replay | Replay a batch of queued transactions | per-item keys | WMS_WORK_EXECUTE |
| POST | /mobile/v1/flows | Create/update draft flow | key | WMS_MOBILE_ADMIN |
| POST | /mobile/v1/flows/{id}:publish | Validate and publish | key | WMS_MOBILE_ADMIN |
| POST | /print/v1/jobs | Render template and print | key | WMS_PRINT |
| POST | /rfid/v1/reads | Ingest EPC reads | key = reader+epc+ts | WMS_RFID_INGEST |
| POST | /wms/v1/automation/channels | Define an MFS channel, telegram profile and communication points | key | WMS_AUTOMATION_ADMIN |
| POST | /wms/v1/automation/telegrams | Inbound telegram relayed by the edge gateway | key = channel + sequence | edge service account |
| POST | /wms/v1/automation/transport-orders/{id}:resend | Resend a pending transport order | key | WMS_AUTOMATION_OPERATE |
| POST | /print/v1/printers | Register a printer, device type and queue | key | WMS_PRINT_ADMIN |
| POST | /print/v1/routing-rules | Create a document routing rule | key | WMS_PRINT_ADMIN |

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.mobile.flow.published.v1 | publish | flow_id, code, version, warehouses | devices (push via SSE), analytics |
| dev.passage.mobile.session.started.v1 / ended.v1 | session lifecycle | session_id, device_id, user_id, warehouse | analytics |
| dev.passage.mobile.offline.conflict.v1 | replay conflict | idempotency_key, command, reason | supervisor queue |
| dev.passage.print.job.completed.v1 | printer ack | job_id, printer, status | wms |

### 6.32.6 User experience

| Screen | Primary actions | Key fields | Duty |
|---|---|---|---|
| Device login / menu | Sign in, pick warehouse/menu | user, warehouse, menu items | WMS_WORK_EXECUTE |
| Flow runner (device) | Scan, enter, confirm, exception | prompt, expected scan class, context (work, location, LP) | WMS_WORK_EXECUTE |
| Flow designer (web) | Build graph, validate, simulate, publish | steps, branches, validations, versions | WMS_MOBILE_ADMIN |
| Device management | Register, assign, force sign-out | device id, model, last seen, session | WMS_MOBILE_ADMIN |
| Offline conflicts | Resolve, re-apply, reject | key, command, reason | WMS_WORK_SUPERVISE |
| Print management | Printers, templates, job log | printer, template, status | WMS_PRINT |

Interaction rules: device UI uses one input per screen; Enter/scan advances; hardware back returns one step; audio and haptic feedback on success/error; font ≥ 16 px; no mouse-only actions; designer simulation runs a flow in a browser with a virtual scanner.

### 6.32.7 Business rules and edge cases

- **BR-32.1 GS1 AI parsing rules.** Input begins with an optional symbology identifier (`]C1` GS1-128, `]d2` DataMatrix, `]Q3` QR), then element strings. Parse left to right: read the AI (2–4 digits per the GS1 AI table), then its data per the table; fixed-length AIs have no separator, variable-length AIs end at FNC1 (GS, 0x1D) or end of string. Supported in this wave: **AI 00** SSCC, fixed 18 numeric, check digit (mod-10, weights 3/1) verified; **AI 01** GTIN, fixed 14 numeric, check digit verified, GTIN-12/13 zero-padded on lookup; **AI 10** batch/lot, variable ≤ 20 characters (GS1 AI encodable set 82); **AI 11** production date and **AI 17** expiry, fixed 6 numeric `YYMMDD` where `DD = 00` means last day of month, century by GS1 rule (year within −49/+50 of current year); **AI 21** serial, variable ≤ 20; **AI 310n** net weight kg, fixed 6 numeric with `n` implied decimal places (`3102000125` → 1.25 kg); **AI 37** count, variable ≤ 8 numeric; **AI 400** PO number, variable ≤ 30; **AI 410/412** GLN, fixed 13; **AI 91–99** company-internal, variable ≤ 90. Unknown AI → parse error `GS1_UNKNOWN_AI` with position. Example: `]C1010950123456789310123ABC` + GS + `17261231` + `21SN0007` → {01: 09501234567893, 10: 123ABC, 17: 2026-12-31, 21: SN0007}. A barcode without symbology identifier is parsed as GS1 only if it starts with a valid AI and the step expects a GS1 class; otherwise it is a plain value.
- **BR-32.2 Scan routing.** A `scan` step expecting `LP` accepts AI 00 or a plain value matching the LP format; expecting `GTIN` accepts AI 01 or an item barcode (wave 8 item barcodes); a mismatch shows "Expected location, scanned LP" and does not advance.
- **BR-32.3 Offline ordering.** Replay applies transactions in `seq` order; the first failure stops replay for that device and marks the rest PENDING until resolved, because later steps may depend on it.
- **BR-32.4 Conflict policy.** If a replayed `ConfirmWorkLine` targets a line that was cancelled or completed by another user, the transaction is CONFLICT; a supervisor may "apply as movement" (inventory moves as scanned, order link dropped) or reject. Timestamps use the captured device time corrected by the measured server skew at the last heartbeat.
- **BR-32.5 Cluster picking** assigns at most 8 works; a cart position is bound to one order's LP; scanning the wrong position is rejected; a short pick on one position does not release the others.
- **BR-32.6 Session lock.** A device session holds the work lock; heartbeat loss > 30 min releases the lock and sets the session to STALE; resuming re-locks if the work is still OPEN.
- **BR-32.7 PIN override** authorizes exactly one action within 60 s and is logged with the supervisor's subject.
- **BR-32.8 Print idempotency.** A print job with the same key within 10 min is not re-rendered; "print again" creates a new job with `reprint_of`.
- **BR-32.9 Flow publish** requires the simulation of each core path to succeed (designer runs a smoke script attached to the flow); devices mid-flow on the previous version finish on it.
- **BR-32.10 Expiry date `DD=00`** resolves to the last calendar day of the month; `YYMMDD` with an invalid month is a parse error.
- **BR-32.11 Telegram sequencing and handshake.** Each channel keeps send and receive sequences 1–9,999 that wrap; a telegram without handshake acknowledgment after `mfs_resend_timeout_s` (default 2 s) is resent up to 3 times, after which the channel is DOWN and open transport orders show `MFS_PENDING`; a duplicate receive sequence is acknowledged and ignored; a transport order confirms the wms work line only on the arrival telegram at its final destination, never on send.
- **BR-32.12 Print routing precedence.** Rules match most specific first: user + document type → zone + document type → warehouse + document type → document-type default; ties go to the lowest rule sequence; with no match, PDF documents fall back to browser printing and ZPL jobs fail with MOB-0112.

### 6.32.8 Acceptance criteria

- **AC-32.1** (FR-32.3, BR-32.1) Given the GS1 AI test suite vectors (fixed and variable, with and without symbology identifiers), when parsed, then 100% match expected AI maps and check-digit failures are reported.
- **AC-32.2** (FR-32.4) Given the emulated-device Playwright suite, when a receive → put-away → pick → pack → ship flow is run with scanned GS1-128 labels from the ASN fixture, then the load ships and the inventory ledger reflects each move.
- **AC-32.3** (FR-32.5, BR-32.3) Given a device offline with 200 queued confirmations, when it reconnects, then all 200 are APPLIED exactly once, in order, and replaying the same batch again changes nothing.
- **AC-32.4** (FR-32.2) Given a published flow change adding a lot-confirmation step, when a device loads the menu, then the new version runs within 60 s with no app update.
- **AC-32.5** (FR-32.7) Given an LP label template, when a receive step completes, then a ZPL job with `^BC` SSCC is sent to the step's printer and status PRINTED is recorded.
- **AC-32.6** (BR-32.4) Given a queued confirmation for a line cancelled meanwhile, when replayed, then it is CONFLICT and appears in the supervisor queue.
- **AC-32.7** (FR-32.10) Given a Zebra DataWedge intent with `]C1` prefix, when received on a non-scan step, then it is ignored and logged.
- **AC-32.8** (FR-32.6, BR-32.6) Given a session with no heartbeat for 31 min, when another user requests work, then the stale session's work is assignable.
- **AC-32.9** (FR-32.12, BR-32.11) Given a simulated conveyor PLC on the edge gateway, when 100 LPs are routed to an AS/RS induction point and the PLC drops 5 acknowledgments, then each affected transport order is resent, no LP is confirmed before its arrival telegram, and all 100 work lines complete.
- **AC-32.10** (FR-32.13, BR-32.12) Given a queue of two printers with the first offline, when 20 LP labels print, then all 20 complete on the second printer and the job log records the failover.

### 6.32.9 Test plan and fixtures

Fixtures: `gs1-ai-vectors.json` (GS1 test suite plus 60 Passage vectors for edge cases), `labels-asn-3-pallet.zpl` rendered to PNG for the emulator's camera-free scan injection, `offline-200.json`, `flows-core-v1` (seven flows), device profiles `zebra-tc52`, `honeywell-ct45`.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| GS1 parser | unit | parser | 100% vectors |
| Flow graph validation | unit | designer | unreachable/non-terminating graphs rejected |
| Offline replay | property | queue | exactly-once under random disconnects |
| Device E2E | E2E (Playwright, Android emulator) | 7 flows | inbound-to-outbound passes on both device profiles |
| Replay throughput | performance | 1,000 devices × 50 tx | all applied < 5 min |
| ZPL rendering | snapshot | templates | byte-identical to golden ZPL |
| Scan latency | performance | step submit | p95 < 300 ms |

Reconciliation: Σ mobile confirmations per day = Σ work line confirmations in `wms`; print jobs PRINTED = labels confirmed on device.

### 6.32.10 Deployment and operations

Helm `passage-wms-mobile` (BFF, SSE push, 2–8 replicas), `passage-print` (print service with printer registry; one per site network, outbound TCP 9100/IPP), PWA assets served by the shell CDN path; Android shell signed APK distributed via MDM (SOTI/Workspace ONE) or sideload. Config: `MOBILE_OFFLINE_MAX_TX=500`, `MOBILE_SESSION_IDLE_MIN=30`, `PRINT_RETRY=3`. Flags: `mobile.offline`, `mobile.voice_adapter`. SLOs: step submit p95 < 300 ms; menu load < 1 s; print job dispatched < 2 s; offline replay 200 tx < 30 s. Alerts: offline conflicts > 10/h per warehouse; print failures > 5/15 min per printer; stale sessions > 20. Runbook: printer unreachable (check print service pod on site, re-queue jobs); device clock skew > 5 min (block replay, instruct re-sync). Rollback: flow versions are retained — republish the previous version; BFF rollback via Argo CD.

### 6.32.11 Migration from AX / SAP

| Source | Passage entity | Transformation |
|---|---|---|
| WHSMobileAppMenu, WHSRFMenuTable, WHSRFMenuLine | MobileMenu | hierarchy preserved |
| WHSRFMenuItemTable (mode, work class, directed/user-directed, detours) | MobileFlow draft | generated from the menu item's options into the equivalent core flow with configured branches; requires manual review |
| WHSWorkUser | WorkUser attributes on Keycloak user | user id mapping from wave 2 |
| WHSDocumentRoutingLayout, printers | Print templates, printers | ZPL layouts imported as-is where ZPL; others re-authored |
| /SCWM/TMENU, /SCWM/T_RF_MENU, /SCWM/TLOGTR | MobileMenu, MobileFlow draft | logical transaction → core flow mapping table |
| /SCWM/RSRC, /SCWM/TRSRC | WorkUser, device bindings | resource type → work class |

Not migrated: RF screen layouts (/SCWM/TPRES) and D365 step field layouts — Passage flows are redesigned; mobile session state; print spool history.

### 6.32.12 Dependencies, risks, open questions

Depends on: wave 31 (work APIs, LPs), wave 25 (shop-floor PWA offline pattern and service-worker library), wave 8 (item barcodes, GS1 prefix), wave 2 (Keycloak device flow), wave 28 (containers/packing).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Offline replay creates subtle inventory errors | Medium | High | Strict ordering, conflict queue, property tests; offline off by default for count flows |
| Android WebView versions on older scanners | Medium | Medium | Minimum WebView 100; PWA fallback |

Open questions: (1) Native iOS shell — proposed: no, PWA only; decide by wave 33. (2) Voice vendor for v1.x reference adapter (Honeywell Voice vs Lydia) — proposed: defer; decide by wave 35.

### 6.32.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| session_idle_timeout_min | warehouse | int | 30 | 5–240 | Session goes STALE and releases the work lock (BR-32.6) | Work user session expiry (Warehouse app settings) | RF presentation device logon timeout (/SCWM/RFUI) |
| pin_reauth_hours | tenant | int | 8 | 1–24 | PIN re-authentication interval (FR-32.1) | Warehouse app authentication (device code, user PIN) | ITS session timeout (SICF service settings) |
| offline_enabled | warehouse × flow | bool | true for pick/put, false for count | true / false | Allows offline queueing for the flow (FR-32.5) | n/a (online only) | n/a (online only) |
| offline_max_tx | device | int | 500 | 50–2,000 | Queue cap (FR-32.5) | n/a | n/a |
| offline_max_age_h | device | int | 24 | 1–72 | Oldest transaction allowed in queue | n/a | n/a |
| flow_publish_four_eyes | tenant | bool | false | true / false | Production publish needs a second approver | Mobile device menu item change via data management (no approval) | Transport of logical transactions (SE01/STMS) |
| gs1_require_symbology_id | warehouse | bool | false | true / false | Unprefixed strings parsed as plain values when true (BR-32.1) | Mobile device barcode setup › GS1 symbology prefix | RF barcode verification profile |
| gs1_fnc1_substitute | warehouse | char | GS (0x1D) | GS / `~` / `|` | Separator accepted for FNC1 | Mobile device barcode setup › FNC1 character | Barcode prefix definitions (/SCWM/RFUI) |
| scanner_profile | device model | enum | ZEBRA_DATAWEDGE | ZEBRA_DATAWEDGE / HONEYWELL_DC / KEYBOARD_WEDGE | Scan capture bridge (FR-32.10) | Warehouse app device settings | ITSmobile device template |
| cluster_max_positions | menu / flow | int | 8 | 1–24 | Cluster pick positions (BR-32.5) | Cluster profile "Number of positions" | Multiple-order picking positions in RF |
| heartbeat_interval_s | tenant | int | 30 | 10–120 | Device heartbeat (FR-32.6) | n/a | n/a |
| print_retry_count | site | int | 3 | 0–10 | Retries before failover (FR-32.7) | Document routing agent retry | Spool retry settings (SPAD) |
| print_job_dedupe_min | tenant | int | 10 | 0–60 | Duplicate-key window (BR-32.8) | n/a | n/a |
| label_default_dpi | printer | enum | 203 | 203 / 300 / 600 | Template rendering resolution | Document routing layout printer DPI | SPAD device type (LB_ZEB 203 dpi Zebra) |
| browser_print_fallback | warehouse | bool | true | true / false | PDF fallback when no device routed (BR-32.12) | Print to screen / user default printer | Front-end printing (access method F/G) |
| mfs_heartbeat_s | automation channel | int | 10 | 5–60 | LIFE telegram interval (FR-32.12) | n/a | SPRO EWM › Material Flow System › Define PLC communication channel (life telegram) |
| mfs_resend_timeout_s | automation channel | int | 2 | 1–30 | Handshake timeout (BR-32.11) | n/a | MFS channel resend settings |
| mfs_reject_point | communication point | location ref | none | locations of type REJECT | Target for failed identification | n/a | MFS communication point "NIO" exception point |
| telemetry_retention_days | tenant | int | 90 | 30–365 | Step telemetry retention (FR-32.11) | n/a | n/a |

### 6.32.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Active sessions and devices | list inquiry | warehouse, model, last seen | device, user, menu, current work, app version | `device_session` | Work user sessions | LRF1 RF monitor, /SCWM/MON resources |
| Offline conflict queue | list inquiry | warehouse, reason, age | key, command, captured_at, conflict reason | `offline_transaction` | n/a | n/a |
| Flow version history | drill-down | flow code | version, published by/at, step diff | `mobile_flow` | Mobile device menu item change history (database log) | Transport history of /SCWM/TLOGTR (SE03) |
| Step timing by user and flow | KPI | flow, step, user (duty-gated), date | median s, p95 s, error count | telemetry rollup | Work user activity inquiry | LRF2 queue/resource statistics |
| Scan error log | operational report | error type, device, date | masked raw value, AI, error position | parser log | n/a | Application log SLG1 (/SCWM/RFUI) |
| Print job log | list inquiry | printer, status, template, user | job, attempts, reprint_of, duration | `print_job` | Document routing job history | SP01 spool requests |
| Printer status | operational report | site, queue | online, queue depth, last error | printer registry | Document routing agent status | SPAD/SP01 output device status |
| MFS channel monitor | operational report | channel, communication point | state, last telegram, pending transport orders, faults | `automation_channel` | n/a | MFS monitor nodes in /SCWM/MON |
| Device fleet health | KPI | warehouse | step submit p95, replay backlog, stale sessions | telemetry | n/a | n/a |

### 6.32.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| LP label | Receive or LP build | SSCC barcode (`^BC`, AI 00), human-readable SSCC, item, qty, lot, expiry, ship-from | ZPL; PDF preview | SSCC from LP sequence | GS1 logistic label guideline |
| Location label | On demand | Location barcode, check characters, direction arrow | ZPL | none | None |
| Item / GTIN label | On demand or VAS | GTIN-14 (AI 01), lot (AI 10), expiry (AI 17), serial (AI 21) | ZPL / PDF | none | Regulated serialization labels (EU FMD, US DSCSA) when item is flagged |
| GS1 carton shipping label | Pack or sort position close | Ship-from/to, carrier, SSCC, PO (AI 400), ship-to postal code (AI 420) | ZPL 4 × 6 in | SSCC | Partner label specifications (wave 35 partner profile) |
| Routed office documents | Rule match (BR-32.12) | Pick list, count sheet, CoA, BOL from their owning waves | PDF via IPP or browser | per document | per owning wave |
| Offline conflict report | Daily | Conflicts by reason, resolution, supervisor | PDF / CSV | none | Audit evidence, 1 year |

### 6.32.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Stale session sweep | Every 1 min | warehouse | session `last_seen_at` | < 5 s | Releases locks; idempotent | Stale sessions > 20 |
| Offline replay drain | Event-driven on reconnect; backlog sweep every 30 s | device_id | `device_id:seq` | 200 tx < 30 s per device | Stops at first failure per device (BR-32.3) | Conflicts > 10/h per warehouse |
| Print queue retry and failover | Every 15 s | printer | job id + attempt | < 2 s per job | Fail over after 2 attempts | Print failures > 5/15 min per printer |
| MFS channel heartbeat check | Every 10 s | channel | channel id | < 100 ms | DOWN status, pending transport orders listed | MfsChannelDown |
| Telemetry rollup | Hourly | warehouse | hour bucket | < 1 min | Recompute bucket | Rollup lag > 2 h |
| Telemetry purge | Daily 01:00 | tenant | day partition | < 5 min | Retry next day | none (log) |
| Device app-version audit | Daily 06:00 | warehouse | device id | < 1 min | Report only | Devices below minimum shell version |

### 6.32.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| WMS_WORK_EXECUTE (mobile) | /mobile/v1/sessions, /menus/{id}, /steps/{id}:submit, /scan:parse, /offline:replay | WMS_COUNT_APPROVE on the same count | Warehouse worker |
| WMS_MOBILE_ADMIN | /mobile/v1/flows, /flows/{id}:publish; device management | Second approver required when `flow_publish_four_eyes = true` | WMS administrator |
| WMS_WORK_SUPERVISE | Offline conflict resolution, PIN override, force sign-out | Overriding own work is blocked (BR-32.7) | Warehouse supervisor |
| WMS_PRINT | /print/v1/jobs | none | Warehouse worker, receiving and shipping clerks |
| WMS_PRINT_ADMIN | /print/v1/printers, /print/v1/routing-rules; print management | none | WMS administrator, site IT |
| WMS_RFID_INGEST | /rfid/v1/reads | Service accounts only; never assigned to people | Reader service account |
| WMS_AUTOMATION_ADMIN | /wms/v1/automation/channels; telegram profile designer | WMS_AUTOMATION_OPERATE in production (change and operate the same channel) | Automation engineer |
| WMS_AUTOMATION_OPERATE | MFS monitor: resend, block communication point, clear fault | none | Control-room operator |

### 6.32.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| MOB-0101 | error | Expected {expected}, scanned {actual}. | Scan class mismatch | Scan the requested object | BR-32.2 |
| MOB-0102 | error | Check digit invalid for AI {ai}. | SSCC/GTIN check digit failure | Rescan or relabel | BR-32.1 |
| MOB-0103 | error | Unknown GS1 application identifier at position {pos}. | GS1_UNKNOWN_AI | Check label against GS1 AI table | BR-32.1 |
| MOB-0104 | error | Invalid date {value} for AI {ai}. | Bad YYMMDD (month 13 etc.) | Relabel | BR-32.10 |
| MOB-0105 | warning | Offline: {n} transactions queued; {remaining} left before the limit. | Device offline | Move to coverage when possible | FR-32.5 |
| MOB-0106 | error | Offline queue is full; reconnect to continue. | 500 transactions or 24 h reached | Reconnect | FR-32.5 |
| MOB-0107 | warning | Transaction {key} conflicts: line was {status} by {user}. | Replay CONFLICT | Supervisor resolves | BR-32.4 |
| MOB-0108 | error | Sequence gap on device {device}: expected {n}, received {m}. | Missing offline transaction | Device resync; support ticket | BR-32.3 |
| MOB-0109 | error | Wrong cart position: scanned {pos}, expected {exp}. | Cluster pick mismatch | Place item in the correct position | BR-32.5 |
| MOB-0110 | error | Supervisor PIN expired or already used. | Override outside 60 s or reused | Request a new override | BR-32.7 |
| MOB-0111 | error | Printer {printer} unreachable after {n} attempts. | Network or device failure | Check printer; job fails over | FR-32.7 |
| MOB-0112 | error | No printer routed for {document} at {location}. | No routing rule match for ZPL | Add routing rule | BR-32.12 |
| MOB-0113 | error | Flow {code} v{n} failed validation: step {id} is unreachable. | Graph validation | Fix the flow graph | FR-32.2 |
| MOB-0114 | warning | Device clock skew {s} s exceeds 300 s; replay blocked. | Device clock drift | Resync device time | §6.32.10 |
| MOB-0115 | error | Automation channel {ch} is down; transport order {to} pending. | 3 missed handshakes or LIFE telegrams | Check PLC link; resend after recovery | BR-32.11 |
| MOB-0116 | warning | LP {lp} rejected at identification point {cp}: {reason}. | Scan or contour check failed | Correct LP at reject point and re-induct | FR-32.12 |
| MOB-0117 | info | Session resumed on flow version {n}. | Device was mid-flow during publish | None | BR-32.9 |

### 6.32.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Step submit | 1,000 devices; 300,000 steps/day; peak 6,000/min | 80 / 300 / 600 ms | 100/s per BFF pod | Telemetry ≈ 40 GB (90-day rolling) | Telemetry daily partitions | Offline-capable flows queue locally; others show "busy, retry" |
| Scan parse | 400,000/day | 2 / 10 / 20 ms (in-process) | 2,000/s per pod | — | — | CPU-bound; HPA adds pods |
| Offline replay | 1,000 devices × 50 tx after an outage | 200 tx < 30 s per device; fleet < 5 min | 300 tx/s | Offline log 90 days ≈ 2 GB | `offline_transaction(device_id, seq)` | Replay throttled per device; ordering preserved |
| Menu load | 20,000/day | 150 / 600 / 1,000 ms | 50/s | — | Published flows cached per warehouse | Cached version served; publish propagation may exceed 60 s |
| Print dispatch | 50,000 labels/day; peak 300/min | 300 ms / 1.2 s / 2 s to printer acknowledgment | 20 jobs/s per site service | Job log 30 days ≈ 1 GB | `print_job(printer_id, created_at)` | FIFO backlog per printer; failover to queue peers |
| MFS telegram round-trip | 100,000 telegrams/day; peak 50/s per site | 20 / 80 / 200 ms gateway ↔ wms | 200/s per site | Telegram log 30 days ≈ 3 GB | `transport_order(channel_id, status)` | Dedicated worker pool keeps automation ahead of manual traffic |

### 6.32.20 Localization and compliance hooks

- **Language and layout.** Device UI strings come from wave-5 i18n bundles per user locale; the PWA supports right-to-left layouts (Arabic, Hebrew); voice prompts (FR-32.9) use the adapter's language model per locale.
- **Regulated serialization.** EU Falsified Medicines Directive (Delegated Regulation 2016/161) requires 2D DataMatrix with AI 01, 21, 10, 17 and national reimbursement numbers (AI 710–715); US DSCSA requires interoperable serialized traceability (EPCIS). Country packs (wave 48) add AI validation and label templates through `LabelTemplatePack`; the EPC ingest (FR-32.9) carries SGTIN data.
- **Character sets.** ZPL templates use `^CI28` (UTF-8) for non-Latin scripts; human-readable dates on labels follow locale while barcode data stays `YYMMDD`.
- **Privacy.** Per-user step telemetry is personal data (GDPR); retention 90 days; named per-user timing reports follow the wave-31 `LaborPrivacyPolicy`; works-council rules as §6.31.20.
- **Device and radio rules.** Scanner hardware certification is the device vendor's responsibility; Passage stores no location data from devices beyond the warehouse and session.

### 6.32.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| SAP #193 — §13.16 EWM /SCWM/MFS* Material Flow System (PLC interface, conveyors, AS/RS) | GAP (propose 32) | ADDED as FR-32.12 | On the wave-25 edge gateway as the register proposed; BR-32.11, AC-32.9 |
| SAP #201 — §13.16 EWM MFS customizing (PLC communication channels, telegrams) | GAP (propose 32) | ADDED as FR-32.12 | `mfs_*` parameters in §6.32.13; telegram profiles are versioned metadata |
| SAP #346 — §13.30 Printing: device types, output devices, access methods (SPAD), front-end printing, print queues (TSP03*) | PARTIAL-32 | ADDED as FR-32.13 | Device types, queues with failover, routing, browser printing; BR-32.12, AC-32.10 |

## 6.33 Wave 33: Quality Management & Nonconformance

### 6.33.1 Scope and business context

Wave 33 replaces AX quality management (test groups, quality associations, quality orders, nonconformances, corrections) and SAP QM (inspection plans, inspection lots, results recording, usage decisions, quality notifications, vendor evaluation). After this wave, quality orders are generated by association rules at purchase receipt, production report-as-finished and before shipment; samples are sized by AQL plans; results are recorded (variable and attribute); a usage decision releases or rejects inventory that was blocked from the moment the order was created; nonconformances drive corrections/CAPA through a Temporal 8D workflow; certificates of analysis print on shipment; supplier scorecards and SPC charts are computed from results; instruments have calibration schedules. Out of scope: stability studies, audit management (ISO 19011), FMEA, and the SAP QM-PM calibration order integration (Passage tracks calibration due dates and blocks results from overdue instruments).

**Scope amendment (coverage closure).** The gap registers move into this wave items that the paragraph above excludes or does not name: product compliance with regulated and restricted lists (FR-33.13), safety data sheets (FR-33.14), incoming vendor certificates (FR-33.15), stability studies (FR-33.16), audit management (FR-33.17), FMEA and control plans (FR-33.18), source inspection (FR-33.19), physical sample management (FR-33.20), multiple specifications (FR-33.21) and in-process inspection points (FR-33.22). The SAP QM-PM calibration-order integration stays out of scope; Instrument due-date tracking (BR-33.8) remains the Passage mechanism. §6.33.21 lists the dispositions.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX quality order blocking (InventQualityOrderTable → InventBlockingTable) blocks only the quantity registered on the receipt; production and sales paths can still pick inventory registered under a different dimension set, and "Inventory blocking" is bypassed by movement journals | Blocking is a reservation-like record checked by some, not all, transaction types | Hold lives in the inventory ledger reservation path and is checked by every transaction type; property test enumerates all | Movements of held stock: occasional (audit findings) → 0 across 10,000 randomized attempts |
| SAP usage decision (QA11) is a separate transaction from results recording (QE01), each 8–12 clicks with stock posting on a third tab; partial accept/reject needs manual quantity split and MIGO 321/322 follow-up | Inspection lot, results and stock posting are separate objects with separate screens | Results and decision on one order; quantity split posts both ledger legs automatically | Clicks per decision: ~25 across QE01/QA11/MIGO → ≤ 5; follow-up postings keyed manually: 2 → 0 |
| AQL sampling in AX is fixed qty or % (InventTestItemSampling); Z1.4 tables are kept in spreadsheets and sample sizes keyed by hand; SAP QDP1 sampling procedures require master data per AQL/level combination | No standard sampling tables in the product | Z1.4 Tables I and II-A/B/C as versioned reference data with switching rules | Sample-size keying errors: ~2% of lots (spreadsheet) → 0; sampling setup per item: 15 min → 0 (plan reuse) |
| SAP quality notification 8D (QM01/QM02 tasks) has no timers; overdue corrective actions are found by report ZQM or not at all; effectiveness checks are forgotten | Notification tasks are static records without workflow timers | Temporal 8D workflow with due-date timers, escalations and an automatic 90-day effectiveness-check task | Overdue CAPA actions discovered: weeks later via report → escalated within 1 business day; effectiveness checks executed: ~50% → 100% |
| CoA generation (AX CoA report / SAP QC21) prints whatever results exist; amended results after shipment produce inconsistent certificates | Certificate is a report over mutable results, not a versioned document | CoA generated from the accepting decision, revisioned with `supersedes` | Inconsistent certificates reported by customers: tolerated → 0 (immutable revision chain) |

### 6.33.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Tests and groups | InventTestTable, InventTestGroup, InventTestGroupTest (lines), InventTestVariable/Outcome | Master inspection characteristics QPMK (QS21), inspection plans QP01 (PLKO/PLPO/PLMK), catalogs QPCT | quality.Test, TestGroup, Characteristic catalog | full |
| Sampling | InventTestItemSampling (fixed qty/%) | Sampling procedures QDP1 (QDPS), sampling schemes QDS1, dynamic modification | SamplingPlan (fixed, %, AQL Z1.4) | improved — AQL built in |
| Triggers | InventTestAssociationTable (quality associations) | QMAT inspection setup per material/plant; lot origins 01/04/10/89 | QualityAssociation | full |
| Quality orders | InventQualityOrderTable, InventQualityOrderLine, InventQualityOrderLineResults; form InventQualityOrderTable | Inspection lot QALS (QA01/QA02), results QAMR/QASR (QE01), QA11 usage decision (QAVE) | QualityOrder, QualityResult, UsageDecision | full |
| Inventory blocking | InventBlockingTable, InventQualityOrderTable blocking | Stock type Q (quality inspection), QM stock posting at UD | Quant `quality_status`; ledger status QUALITY_HOLD | full |
| Nonconformance | InventNonConformanceTable, problem/diagnostic types, InventNonConformanceCorrection, InventNonConformanceOperation | Quality notifications QMEL/QMFE/QMMA/QMSM (QM01), 8D report | Nonconformance, Correction (CAPA) | full |
| Certificates | Certificate of analysis (InventTestCertificateOfAnalysis report) | QC21/QC22 certificate profiles (QCVK) | CoA template + generation | full |
| Vendor evaluation | Vendor quality on nonconformance (reports) | ME61–ME65 vendor evaluation (ELBK/ELBM/ELBP) | SupplierScorecard | full |
| Calibration | InventTestInstrument (instrument master only) | QM-PM calibration (inspection lot type 14, equipment) | Instrument with calibration schedule | partial |
| SPC | n/a | QM control charts (QGC1) | SPC (X-bar/R, p-chart) | full |
| Cost of quality | n/a | QM-CO appraisal costs | CostOfQuality projection | improved |
| Product compliance | Product compliance (D365 process industries): regulated products, restricted lists, reporting lists | S/4 Product Compliance (product marketability checks), EHS Product Safety and Stewardship | quality.ComplianceList, ItemComposition, ComplianceResult | full |
| Safety data sheets | Safety data sheets with validity and send-on-sales (D365 PI) | EHS report shipping (CVD1), SDS distribution | quality.SafetyDataSheet, SdsDelivery | full |
| Incoming certificates | n/a (attachments on product receipts) | QC51/QC52/QC53 certificate receipt, QC55 overview; certificate type in QI01 | quality.IncomingCertificate | full |
| Stability study | n/a | QST01–QST07 stability study (storage conditions, testing schedule) | quality.StabilityStudy, PullPoint | full |
| Audit management | n/a | PLMD_AUDIT audit plans, question lists, findings, corrective actions | quality.Audit, QuestionList, Finding | full |
| FMEA / control plan | n/a | S/4 QM FMEA (FMEA cockpit), control plans | quality.Fmea, ControlPlan | full |
| Source inspection | n/a (quality orders only after receipt) | Source inspection flag in QI01, QI07 trigger, inspection type 0101 | QualityOrder trigger SOURCE_INSPECTION | full |
| Sample management | Item sampling quantity only (InventTestItemSampling) | QPR1–QPR7 physical samples, sample-drawing procedures, sample types | quality.PhysicalSample | full |
| Multiple specifications | One test group per item/association | S/4 Multiple Specifications (customer, country, pharmacopeia objects) | quality.SpecificationSet | full |
| In-process inspection points | Quality associations on route operations (before/after) | Inspection points (TQ79), lot origin 03, QE11 results per point | InspectionPoint on production operations | full |

### 6.33.3 Functional requirements

- **FR-33.1 Tests, instruments and test groups** — Must. The system shall define tests (variable with UoM, target, min/max; attribute with outcome catalog), instruments, and test groups with sequence, sampling, and acceptance per test.
  - Notes: variable results compare to tolerances with 6-decimal precision; test group versions are effective-dated; a test marked `destructive` reduces sample qty on completion.
- **FR-33.2 Sampling plans** — Must. The system shall size samples by fixed quantity, percentage, or ANSI/ASQ Z1.4 single sampling (normal, tightened, reduced) at general inspection level II by default.
  - Notes: table excerpt and switching rules in BR-33.1/BR-33.2; sample qty is capped at lot qty; percentage rounding up.
- **FR-33.3 Quality associations** — Must. The system shall generate quality orders when a trigger matches: purchase receipt (registration or product receipt), production RAF (before/after put-away), sales order (at pick/before packing slip), transfer receipt, quarantine, and manual.
  - Notes: association scope = item / item group / all × vendor/customer / group / all × site; most specific wins (BR-33.3); one order per source line per event.
- **FR-33.4 Quality orders and results** — Must. The system shall record results per test per sample, compute pass/fail per test and order, and support retest and partial sampling.
  - Notes: results recorded with instrument id and user; a result from an instrument overdue for calibration is rejected; order status per BR-33.4.
- **FR-33.5 Inventory blocking** — Must. The system shall block the inspected quantity from pick, ship, consume and transfer-out when the quality order is created and release it only on an accepting usage decision; rejection moves qty to a quarantine location or blocked status.
  - Notes: enforcement lives in the inventory ledger reservation path (wave 16), not only in `wms`; blocked qty is visible as "quality hold" in ATP (wave 27) as unavailable.
- **FR-33.6 Usage decision** — Must. The system shall record a usage decision (accept, reject, accept-with-deviation, rework, return-to-vendor, scrap) with partial quantities, and post consequential transactions.
  - Notes: reject → nonconformance auto-created; return-to-vendor → wave-18 return order draft; scrap → wave-16 scrap journal with reason.
- **FR-33.7 Nonconformance** — Must. The system shall create nonconformances (sources: customer, vendor, internal, production, service request) with problem type, severity, quarantine action, related quality order and quantity.
  - Notes: statuses NEW → APPROVED → IN_CORRECTION → CLOSED, or REFUSED; attachments and photos from the mobile app.
- **FR-33.8 Correction / CAPA workflow** — Must. The system shall run an 8D correction workflow (D1 team … D8 close) on Temporal with root cause, containment, corrective action, due dates, approvals and an effectiveness check after a configurable period (default 90 days).
  - Notes: overdue actions escalate per wave-4 workflow escalation; effectiveness check task auto-created by a Temporal timer.
- **FR-33.9 Certificates of analysis** — Must. The system shall generate CoA documents per shipment/batch from accepted results using a template per customer/item, with PDF output and e-mail/portal delivery.
  - Notes: CoA shows result, specification, method, instrument; signature block from the usage-decision user.
- **FR-33.10 Supplier quality scorecard** — Should. The system shall compute per vendor: lots inspected, lot acceptance rate, PPM defective, NCR count by severity, on-time delivery (from wave 18), and a weighted score 0–100.
  - Notes: weights configurable; monthly materialization; exposed to the wave-20 supplier portal.
- **FR-33.11 SPC charts** — Should. The system shall compute X-bar/R (variable) and p/np (attribute) charts with Western Electric rules 1–4 flagged.
  - Notes: subgroup size from sampling plan; control limits from the last 25 subgroups.
- **FR-33.12 Cost of quality** — Could. The system shall tag inspection labor (wave 24 time), scrap, rework and returns to prevention/appraisal/internal/external failure categories for reporting.
  - Notes: amounts sourced from journal entries by dimension `coq_category`.
- **FR-33.13 Regulated and restricted product lists** — Should. The system shall maintain compliance lists (regulated regions; restricted-substance lists such as REACH SVHC, RoHS Annex II, California Proposition 65, TSCA; reporting lists with thresholds) and item compositions (substance, CAS number, concentration % w/w), evaluate every item against every applicable list per BR-33.11, and block or warn on sales order lines, shipments and purchase receipts to or from regions where the item is restricted.
  - Notes: composition rolls up through the wave-9 BOM by mass fraction; results are versioned per (item, list version); block or warn per list (`compliance_block_mode`); a reporting extract per period lists quantities sold per region for reporting lists (e.g. REACH Art. 33 communication, SCIP notification data).
- **FR-33.14 Safety data sheets** — Should. The system shall store SDS versions per item × language × region (GHS 16-section reference, PDF, version, issue date, validity), record which version each customer received, and send the current SDS with the first shipment and after every new version per BR-33.12.
  - Notes: shipment confirmation (wave 28) checks `sds_required`; missing SDS holds or warns per `sds_missing_action`; validity default 1,095 days; an expiry job creates review tasks 60 days ahead; the delivery log is the distribution evidence.
- **FR-33.15 Incoming vendor certificates** — Should. The system shall require certificates (certificate of analysis, certificate of conformance, EN 10204 type 3.1 inspection certificate) per item × vendor through the quality association, record their receipt (upload, EDI attachment or supplier portal), check certificate values against the specification, and keep the quality order from acceptance until the certificate is received and accepted (BR-33.13).
  - Notes: certificate types are required or optional; a receipt without a required certificate sets `certificate_status = MISSING`; waivers need QM_USAGE_DECISION and a reason; the supplier portal (wave 20) offers upload against the ASN or PO line.
- **FR-33.16 Stability studies** — Could. The system shall plan stability studies (item, batch, protocol), storage conditions (e.g. 25 °C/60 % RH long-term, 40 °C/75 % RH accelerated per ICH Q1A(R2)), pull points (0, 3, 6, 9, 12, 18, 24, 36 months) with a test group per pull point, and create stability quality orders on schedule from retained samples (FR-33.20).
  - Notes: one Temporal workflow per study schedules pulls (BR-33.14); status PLANNED → ACTIVE → COMPLETED or TERMINATED; out-of-specification results raise an NCR and, after approval, can shorten the batch shelf life (wave 16 batch expiry).
- **FR-33.17 Audit management** — Could. The system shall plan audits (internal, supplier, customer; ISO 9001 clause references), maintain weighted question lists, record answers and findings, and convert findings into NCRs and CAPA (FR-33.8); supplier audit scores feed the scorecard (FR-33.10).
  - Notes: status PLANNED → IN_PROGRESS → REPORTED → CLOSED; score = Σ weighted answer points ÷ maximum × 100; finding severity MAJOR, MINOR or OBSERVATION; closure requires a CAPA for every major finding; an auditor cannot audit their own org unit.
- **FR-33.18 FMEA and control plans** — Could. The system shall maintain design and process FMEAs in the AIAG-VDA (2019) structure — structure, function, failure analysis, and risk analysis with severity, occurrence and detection 1–10 and action priority H/M/L — and control plans per item × routing operation whose characteristics generate test group lines and in-process inspection points (FR-33.22).
  - Notes: action priority from the AIAG-VDA AP table held as reference data (BR-33.15); RPN shown for legacy comparison only; control plan phases PROTOTYPE, PRE_LAUNCH, PRODUCTION; a control plan revision versions the linked test group; a PPAP element checklist is stored as a document set on the item.
- **FR-33.19 Source inspection** — Should. The system shall create a quality order with trigger SOURCE_INSPECTION for a PO line or ASN a configurable lead time before the confirmed delivery date, let an inspector or the vendor (portal) record results, and on acceptance mark the inspected batches so that their receipt skips receiving inspection (BR-33.16).
  - Notes: lead time default 5 days; rejection notifies purchasing and blocks the ASN; non-matching batches at receipt get the normal receipt association.
- **FR-33.20 Physical sample management** — Should. The system shall record physical samples drawn for a quality order — primary, pooled and reserve/retention samples — with sample id, quantity, container, storage location and condition, label (ZPL through wave 32) and retention expiry, under a sample-drawing procedure.
  - Notes: retention default 1 year past batch expiry (21 CFR 211.170); destructive consumption posts per BR-33.5; a daily job marks expired retention samples DUE_FOR_DISPOSAL and creates a disposal task; pooled samples reference their primary samples.
- **FR-33.21 Multiple specifications** — Should. The system shall allow specification sets per test group (default, customer, country/region, pharmacopeia) with their own limits per test, evaluate results against every applicable set, and record per set whether the lot conforms; the CoA and usage decision use the set that applies to the customer or destination (BR-33.17).
  - Notes: a lot conforms to set S if and only if every required test passes under S's limits; the lot carries `spec_conformance[]`, which wave-27 ATP uses to allocate the lot only to customers whose set it meets.
- **FR-33.22 In-process inspection points** — Should. The system shall define inspection points on production route operations by time interval (e.g. every 2 h), quantity interval (e.g. every 500 units) or free definition, generate result slots per point during production from wave-21/25 operation feedback, and halt the operation when a critical characteristic fails if configured.
  - Notes: point id = (order, operation, point number, time or quantity stamp); results are recorded on the shop-floor PWA (wave 25); a critical failure sets the operation to HALTED_QUALITY and notifies the supervisor; output since the last good point can be dispositioned as a partial lot; missed points per BR-33.18.

### 6.33.4 Data model

**QualityOrder**

| Field | Type | Required | Notes |
|---|---|---|---|
| quality_order_id | uuid | yes | PK; number sequence QO-* |
| trigger | enum | yes | PURCHASE_RECEIPT, PRODUCTION_RAF, SALES_PICK, TRANSFER_RECEIPT, QUARANTINE, MANUAL |
| source_ref | jsonb | yes | {type, id, line} polymorphic |
| item_id, dims | uuid/jsonb | yes | batch/serial dims |
| site_id, warehouse_id | uuid | yes | |
| lot_qty, sample_qty, uom | numeric(18,6)/text | yes | |
| test_group_id, test_group_version | uuid/int | yes | |
| sampling_plan_id, inspection_severity | uuid/enum | yes | NORMAL/TIGHTENED/REDUCED |
| status | enum | yes | BR-33.4 |
| block_ref | uuid | no | inventory ledger hold id |
| created_at, closed_at | timestamptz | — | |

**QualityResult**

| Field | Type | Required | Notes |
|---|---|---|---|
| result_id | uuid | yes | PK |
| quality_order_id, test_id, sample_no | uuid/uuid/int | yes | unique together with `retest_no` |
| value_numeric | numeric(18,6) | no | variable tests |
| outcome_id | uuid | no | attribute tests |
| passed | bool | yes | computed |
| instrument_id | uuid | no | must be in calibration |
| recorded_by, recorded_at | text/timestamptz | yes | |
| retest_no | int | yes | default 0 |

**Nonconformance**

| Field | Type | Required | Notes |
|---|---|---|---|
| ncr_id | uuid | yes | PK; NCR-* |
| source | enum | yes | CUSTOMER, VENDOR, INTERNAL, PRODUCTION, SERVICE |
| item_id, dims, qty | — | yes | |
| problem_type_id, severity | uuid/enum | yes | severity MINOR/MAJOR/CRITICAL |
| quality_order_id, vendor_id, customer_id, production_order_id | uuid | no | |
| status | enum | yes | NEW, APPROVED, REFUSED, IN_CORRECTION, CLOSED |
| quarantine_order_id | uuid | no | |
| correction_workflow_id | text | no | Temporal workflow id |

**SamplingPlan**: plan_id, method (FIXED/PERCENT/AQL), fixed_qty, percent, aql (numeric 3,2), inspection_level (S1–S4, I–III), severity default, switching_enabled. **Instrument**: instrument_id, calibration_interval_days, last_calibrated_at, due_at, status.

Invariants: a quant with an open QualityOrder referencing it has `quality_status ∈ {PENDING, BLOCKED}`; `Σ accept_qty + reject_qty + deviation_qty` of a usage decision = lot_qty; a QualityOrder is CLOSED only with a usage decision; a result cannot reference an instrument whose `due_at < recorded_at`. Indexing: `quality_order(status, site_id)`, `quality_result(quality_order_id, test_id, sample_no, retest_no)` unique, `nonconformance(vendor_id, created_at)`.

### 6.33.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /quality/v1/test-groups | Create/version test group | key | QM_SETUP_MAINTAIN |
| POST | /quality/v1/associations | Create association | key | QM_SETUP_MAINTAIN |
| POST | /quality/v1/sampling-plans:size | Compute sample size for lot qty/plan | — | QM_VIEW |
| POST | /quality/v1/quality-orders | Create manual order | key | QM_ORDER_MAINTAIN |
| POST | /quality/v1/quality-orders/{id}/results | Record results (batch) | key per batch | QM_RESULTS_RECORD |
| POST | /quality/v1/quality-orders/{id}:usage-decision | Post decision with quantities | key | QM_USAGE_DECISION |
| POST | /quality/v1/nonconformances | Create NCR | key | QM_NCR_MAINTAIN |
| POST | /quality/v1/nonconformances/{id}/corrections | Start CAPA workflow | key | QM_CAPA_MAINTAIN |
| POST | /quality/v1/coa:generate | Generate CoA for shipment/batch | key = shipment+batch | QM_COA |
| GET | /quality/v1/scorecards/vendors/{id}?period= | Scorecard | — | QM_VIEW |
| POST | /quality/v1/compliance-lists | Create or version a compliance list | key | QM_COMPLIANCE_MAINTAIN |
| POST | /quality/v1/items/{id}/sds | Upload an SDS version | key | QM_COMPLIANCE_MAINTAIN |
| POST | /quality/v1/quality-orders/{id}/certificates | Record an incoming certificate | key | QM_RESULTS_RECORD |
| POST | /quality/v1/stability-studies | Create a study and generate its schedule | key | QM_STABILITY_MAINTAIN |
| POST | /quality/v1/audits | Plan an audit | key | QM_AUDIT_MAINTAIN |
| POST | /quality/v1/fmeas | Create or version an FMEA or control plan | key | QM_FMEA_MAINTAIN |
| POST | /quality/v1/samples | Register a physical sample | key | QM_RESULTS_RECORD |

Commands: `CreateQualityOrderFromTrigger` validates association match and that the source line is in the required state; `PostUsageDecision` validates all required tests have results (or `allow_incomplete` with deviation reason) and quantity split sums to lot qty.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.quality.order.created.v1 | order created | quality_order_id, trigger, source_ref, item, dims, lot_qty, sample_qty | inventory (hold), wms, analytics |
| dev.passage.quality.result.recorded.v1 | result batch | quality_order_id, results[] | SPC projection |
| dev.passage.quality.usagedecision.posted.v1 | decision | quality_order_id, decision, accept_qty, reject_qty, deviation_qty, user | inventory (release/hold), wms, purchasing, production |
| dev.passage.quality.nonconformance.created.v1 | NCR | ncr_id, source, severity, item, vendor_id | vendor scorecard, workflow |
| dev.passage.quality.capa.closed.v1 | 8D closed | ncr_id, effectiveness_result | analytics |

### 6.33.6 User experience

| Screen | Primary actions | Key fields | Duty |
|---|---|---|---|
| Quality setup | Tests, groups, sampling plans, associations, instruments | test, type, tolerance, AQL, trigger scope | QM_SETUP_MAINTAIN |
| Quality order workbench | List by status/site, open, record, decide | order, item, trigger, sample, due, status | QM_ORDER_MAINTAIN |
| Results recording (web + mobile step) | Enter values per sample/test, attach | test, sample, value, outcome, instrument | QM_RESULTS_RECORD |
| Usage decision | Split quantities, choose decision, reason | accept/reject/deviation qty, disposition | QM_USAGE_DECISION |
| Nonconformance / 8D | Create, approve, run 8D, close | NCR, severity, D1–D8 tasks, due dates | QM_NCR_MAINTAIN |
| Supplier scorecard | Period filter, drill to NCRs | vendor, score, PPM, acceptance rate | QM_VIEW |
| SPC dashboard | Chart per item/test, rule violations | subgroups, limits, rule hits | QM_VIEW |

Interaction rules: results grid is keyboard-first (Tab/Enter down a sample column); out-of-tolerance values highlight immediately on blur; usage decision disabled until required results exist; CoA preview before release.

### 6.33.7 Business rules and edge cases

- **BR-33.1 AQL table (ANSI/ASQ Z1.4, general inspection level II, single sampling, normal inspection) — excerpt used by the sizing function.**

| Lot size | Code | Sample n | AQL 1.0 Ac/Re | AQL 2.5 Ac/Re | AQL 4.0 Ac/Re |
|---|---|---|---|---|---|
| 2–8 | A | 2 | use E (13): 0/1 | use D (8): 0/1 | use C (5): 0/1 |
| 9–15 | B | 3 | use E: 0/1 | use D: 0/1 | use C: 0/1 |
| 16–25 | C | 5 | use E: 0/1 | use D: 0/1 | 0/1 |
| 26–50 | D | 8 | use E: 0/1 | 0/1 | use E (13): 1/2 |
| 51–90 | E | 13 | 0/1 | use F (20): 1/2 | 1/2 |
| 91–150 | F | 20 | use G (32): 1/2 | 1/2 | 2/3 |
| 151–280 | G | 32 | 1/2 | 2/3 | 3/4 |
| 281–500 | H | 50 | 2/3 | 3/4 | 5/6 |
| 501–1,200 | J | 80 | 3/4 | 5/6 | 7/8 |
| 1,201–3,200 | K | 125 | 5/6 | 7/8 | 10/11 |
| 3,201–10,000 | L | 200 | 7/8 | 10/11 | 14/15 |
| 10,001–35,000 | M | 315 | 10/11 | 14/15 | 21/22 |
| 35,001–150,000 | N | 500 | 14/15 | 21/22 | 21/22 |
| 150,001–500,000 | P | 800 | 21/22 | 21/22 | 21/22 |
| 500,001+ | Q | 1,250 | 21/22 | 21/22 | 21/22 |

Arrow cells ("use X") follow the Z1.4 rule: when the table points to another sample size, use that sample size and its Ac/Re; if it exceeds lot size, inspect 100%. The full Table I (code letters for levels S-1…S-4, I–III) and Tables II-A/B/C (normal/tightened/reduced) are stored as versioned reference data `z14_table` and tested cell by cell.
- **BR-33.2 Switching rules.** Normal → tightened when 2 of 5 consecutive lots are rejected; tightened → normal after 5 consecutive accepted; normal → reduced after 10 consecutive accepted (and switching enabled); reduced → normal on a rejection. Severity is tracked per (item, vendor, test group).
- **BR-33.3 Association specificity.** Score = 4·(item match) + 2·(party match) + 1·(site match); highest score wins; ties resolved by lowest `sequence`; `item group` counts as item match with weight 3. Exactly one order per source line per trigger event; a re-triggered event (e.g., second receipt line) creates a separate order.
- **BR-33.4 Quality order state machine.** `CREATED → OPEN (sample drawn) → RESULTS_IN_PROGRESS → PENDING_DECISION → CLOSED`; `CREATED|OPEN → CANCELLED` (only if source reversed; hold released); `PENDING_DECISION → RESULTS_IN_PROGRESS` on retest request; CLOSED is final; a decision can be *amended* only by a new NCR referencing the order.
- **BR-33.5 Blocking semantics.** The hold is placed synchronously in the same transaction as source line registration (receipt registration / RAF) through the inventory ledger `PlaceHold` API; if the hold cannot be placed the source transaction fails. Hold qty = lot qty (not sample qty). Destructive test consumption posts a scrap journal for the destroyed sample qty at decision time.
- **BR-33.6 Variable test evaluation** passes if `min ≤ value ≤ max` inclusive after rounding to the test's declared decimals (half-up); attribute passes if the outcome is flagged `pass`. Order passes if every test marked `required` passes on every sample within Ac/Re for AQL plans.
- **BR-33.7 Partial decisions.** Accept 90, reject 10: accepted qty released; rejected qty moved to the quarantine location with `quality_status = REJECTED`; both legs are ledger transactions with the quality order as reference.
- **BR-33.8 Instrument calibration.** `due_at = last_calibrated_at + interval`; results are rejected if `due_at < now` with error `INSTRUMENT_OVERDUE`; a 14-day warning is raised.
- **BR-33.9 CAPA timers.** D3 containment due in 2 business days, D5 corrective action in 30 days, effectiveness check at 90 days after D7 close; business days from the site calendar.
- **BR-33.10 CoA** includes only results from the accepting decision; amended results require a new CoA revision with `supersedes`.
- **BR-33.11 Compliance evaluation.** For item i and list L, the concentration c_s of substance s = Σ over BOM components (mass fraction of the component × c_s in the component), applied recursively; i is restricted under L in region r if any s on L has c_s > threshold_s (strictly greater) and L applies to r; evaluation reruns on BOM, composition or list-version change and stores a new result version.
- **BR-33.12 SDS dispatch.** On shipment confirmation for an item with `sds_required`: select the SDS with region = ship-to country (or its region group), language = customer language else region default, status VALID; if the customer has not received that version, attach it and log the delivery; if none is valid, hold (default) or warn per `sds_missing_action`.
- **BR-33.13 Certificate gating.** A usage decision ACCEPT requires `certificate_status ∈ {RECEIVED_OK, WAIVED}` when the association demands a certificate; certificate values outside the specification set `certificate_status = RECEIVED_NOK` and create an NCR with source VENDOR.
- **BR-33.14 Stability pull scheduling.** Pull date = study start + pull-point months (same day of month; clamped to month end); the quality order is created `stability_lead_days` (default 7) before the pull date; a pull recorded more than `stability_pull_window_days` (default 3) late is flagged as a deviation.
- **BR-33.15 FMEA action priority.** Action priority = AP(S, O, D) from the AIAG-VDA reference table; an FMEA cannot be released while any H item lacks an action with owner and due date.
- **BR-33.16 Source-inspection skip.** A receipt skips receiving inspection if and only if (PO line, batch) has an accepted source inspection whose accepted quantity ≥ received quantity; excess quantity gets a normal quality order.
- **BR-33.17 Specification set precedence.** customer > country > pharmacopeia > default; conformance is computed for all sets; the CoA prints the limits of the set used for the customer.
- **BR-33.18 Inspection point frequency.** Time-based points fall due every interval from operation start in the plant time zone; quantity-based points fall due when cumulative good quantity crosses k × interval; a point without a result after a grace of `inspection_point_grace_pct` (default 25 %) of the interval is missed, and when `block_on_missed_point = true` RAF of output since the last recorded point is blocked.

### 6.33.8 Acceptance criteria

- **AC-33.1** (FR-33.3, BR-33.3) Given the trigger fixture of 60 receipts and 40 RAFs with overlapping associations, when posted, then exactly the expected 73 quality orders exist with the expected test groups.
- **AC-33.2** (FR-33.2, BR-33.1) Given lot sizes {5, 50, 51, 500, 501, 3200, 10001, 600000} at AQL 2.5 normal level II, when sized, then sample sizes are {5, 8, 20, 50, 80, 125, 315, 1250} (lot 51 → code E, whose AQL 2.5 cell points down to F, n = 20) and Ac/Re are {0/1, 0/1, 1/2, 3/4, 5/6, 7/8, 14/15, 21/22}, matching the Z1.4 reference cells.
- **AC-33.3** (FR-33.5) Given 10,000 random attempts to pick, ship, consume or transfer quants with open quality orders, when executed, then every attempt fails with `QUALITY_HOLD` and no inventory transaction is written.
- **AC-33.4** (FR-33.6, BR-33.7) Given a lot of 100 with results, when the decision is accept 90 / reject 10, then 90 are available, 10 sit in quarantine as REJECTED, and an NCR exists for 10.
- **AC-33.5** (FR-33.8, BR-33.9) Given an NCR with a started 8D workflow, when D7 closes and 90 days elapse (Temporal time-skipping test), then an effectiveness-check task is created and the workflow completes on its approval.
- **AC-33.6** (FR-33.4, BR-33.8) Given an instrument overdue since yesterday, when a result references it, then the request is rejected with `INSTRUMENT_OVERDUE`.
- **AC-33.7** (FR-33.9) Given an accepted batch shipped to a customer with a CoA template, when the shipment is confirmed, then a PDF CoA with every required test appears on the shipment and the customer portal.
- **AC-33.8** (BR-33.2) Given 5 consecutive lots of which 2 are rejected, when the 6th order is created, then its severity is TIGHTENED.
- **AC-33.9** (FR-33.10) Given 12 months of decisions and NCRs, when the scorecard is computed, then PPM = rejected units / inspected units × 10⁶ and score matches the weighted formula fixture.
- **AC-33.10** (FR-33.13, BR-33.11) Given an item whose BOM yields 0.12 % w/w of an SVHC, when a sales order line ships to an EU customer, then the REACH Art. 33 warning QM-0121 appears and the item is on the period reporting extract; at 0.09 % w/w neither happens.
- **AC-33.11** (FR-33.14, BR-33.12) Given an item with an English SDS only and a German customer with `sds_missing_action = HOLD`, when the shipment is confirmed, then it is held with QM-0124; after a valid German SDS is uploaded the shipment releases with the SDS attached and logged.
- **AC-33.12** (FR-33.15, BR-33.13) Given an association requiring an EN 10204 3.1 certificate, when the receipt posts without one, then ACCEPT is rejected with QM-0125; after an in-specification certificate is recorded, ACCEPT succeeds.
- **AC-33.13** (FR-33.16, BR-33.14) Given a 36-month study starting 2026-01-31 with pull points 0/3/6/…/36, when the schedule is generated, then pull dates are 2026-01-31, 2026-04-30, 2026-07-31 and so on, and quality orders appear 7 days before each (Temporal time-skipping test).
- **AC-33.14** (FR-33.19, BR-33.16) Given an accepted source inspection for batch B of 1,000 units, when 1,000 units of B are received, then no receiving quality order is created; when 1,000 units of batch C arrive, a normal quality order is created.
- **AC-33.15** (FR-33.21, BR-33.17) Given a lot that meets default limits but fails customer X's tighter limit, when ATP allocates, then the lot is not allocated to customer X and remains allocatable to other customers.
- **AC-33.16** (FR-33.22, BR-33.18) Given a 2-hour inspection point and `block_on_missed_point = true`, when no result exists 2.5 h after operation start, then RAF of output since the last point is blocked and the supervisor receives QM-0130.
- **AC-33.17** (FR-33.18, BR-33.15) Given an FMEA line with S = 10, O = 8, D = 7 and no action, when release is attempted, then action priority is H and release fails with QM-0127.
- **AC-33.18** (FR-33.20) Given a retention sample whose retention expiry is today, when the daily job runs, then the sample is DUE_FOR_DISPOSAL and a disposal task exists.
- **AC-33.19** (FR-33.17) Given an audit with 2 major findings and one CAPA, when closure is attempted, then it is rejected with QM-0131.

### 6.33.9 Test plan and fixtures

Fixtures: `z14-tables.json` (full Z1.4 Tables I, II-A/B/C transcribed and double-checked), `quality-triggers-100.json`, `results-variable-attribute.json`, `instruments-12.json`, `ncr-8d-sample`, `vendor-scorecard-12m`.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| AQL sizing | unit | every cell of II-A/B/C | equals transcribed table |
| Association precedence | unit | 100 triggers | expected orders |
| Blocking property | property | inventory ledger + wms | 0 successful movements on held quants |
| 8D workflow | Temporal replay/time-skip | CAPA | completes with effectiveness check |
| Usage decision quantities | unit | splits | sum invariant |
| CoA rendering | snapshot | PDF | matches golden |
| Results throughput | performance | 1,000 results batch | < 2 s |

Reconciliation: Σ quality-held qty in ledger = Σ lot_qty of open orders; rejected qty in quarantine = Σ reject_qty of decisions − dispositions.

### 6.33.10 Deployment and operations

Helm `passage-quality` (api, worker, 2–6 replicas), CloudNativePG `quality-db` (3 instances, 50 GB). Config: `QM_DEFAULT_INSPECTION_LEVEL=II`, `QM_EFFECTIVENESS_DAYS=90`, `QM_INSTRUMENT_WARN_DAYS=14`. Flags: `quality.enabled` per site, `quality.spc`. Scheduled: scorecard materialization monthly (1st, 03:00), instrument due scan daily, SPC recompute hourly. SLOs: order creation added to receipt latency < 100 ms p95; results batch p95 < 2 s; decision posting p95 < 500 ms. Alerts: orders PENDING_DECISION > 5 days; held qty vs open orders drift; instrument overdue with results attempted. Runbook: release orphan holds (order cancelled but hold remains); reprocess association for a trigger event. Rollback: `quality.enabled=false` stops new orders; existing holds remain until decided (never auto-release on rollback).

### 6.33.11 Migration from AX / SAP

| Source | Passage entity | Transformation |
|---|---|---|
| InventTestTable, InventTestVariable, InventTestVariableOutcome | Test, outcome catalog | type Integer/Fraction → variable; Option → attribute |
| InventTestGroup + lines, InventTestItemSampling | TestGroup, SamplingPlan | fixed/percent plans direct; AQL plans created new |
| InventTestAssociationTable | QualityAssociation | event type mapping (Registration/Product receipt/RAF/Pick) |
| InventQualityOrderTable/Line/LineResults (open) | QualityOrder, QualityResult | open orders only; holds recreated via ledger API |
| InventNonConformanceTable, InventNonConformanceCorrection (open) | Nonconformance, Correction | statuses mapped; closed NCRs loaded as read-only history (last 24 months) |
| QPMK, PLKO/PLPO/PLMK | Test, TestGroup | one group per plan usage; characteristics by `MERKNR` |
| QDPS/QDS1, QMAT | SamplingPlan, QualityAssociation | SAP sampling procedure type → method; `QMAT` lot origin → trigger |
| QALS (open, no UD), QAMR/QASR | QualityOrder, QualityResult | lot origin 01→PURCHASE_RECEIPT, 04→PRODUCTION_RAF, 10→SALES_PICK, 89→MANUAL |
| QMEL/QMFE/QMMA/QMSM (open) | Nonconformance, Correction tasks | notification type Q1/Q2/Q3 → source |
| ELBK/ELBM/ELBP | SupplierScorecard history | last 12 months of scores only |

Not migrated: closed inspection lots with UD (history to lakehouse), SAP control chart history (recomputed), certificate print history.

### 6.33.12 Dependencies, risks, open questions

Depends on: wave 32 (mobile results step), wave 31 (quarantine locations, work), wave 28 (shipment hooks for sales trigger and CoA), wave 21 (RAF hook), wave 18 (receipt hooks, return orders), wave 16 (ledger hold API — extended in this wave), wave 4 (Temporal).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Z1.4 transcription error | Low | High | Two independent transcriptions diffed; spot-check against the ASQ tables |
| Hold placement adds latency to receipts | Medium | Low | Synchronous but local DB call via ledger API; measured in CI |

Open questions: (1) Should sales-pick quality orders block the *order line* or the *quant*? Proposed: quant; decide by wave 33 design review. (2) Cost of quality category as a journal dimension or a tag? Proposed: dimension `coq_category`; decide by wave 37 (cost accounting).

### 6.33.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| quality.enabled | site | bool | false | true / false | Activates quality orders for the site | Inventory and warehouse management parameters › Quality management › "Use quality management" | QM activation per plant; material master QM view (QCC0) |
| default_inspection_level | tenant | enum | II | S-1…S-4, I–III | Default Z1.4 inspection level (FR-33.2) | Item sampling (no AQL levels) | Sampling scheme (QDP1) |
| switching_enabled | sampling plan | bool | true | true / false | Normal/tightened/reduced switching (BR-33.2) | n/a | Dynamic modification rule (QDB1) |
| block_on_order_create | site | bool | true | true / false | Hold placed at order creation (BR-33.5) | Quality order blocking parameters / association "Event blocking" | QM control key "post to inspection stock" (TQ08) |
| auto_ncr_on_reject | site | bool | true | true / false | NCR created on reject decision (FR-33.6) | Nonconformance defaults | Usage decision follow-up action "create notification" |
| effectiveness_check_days | site | int | 90 | 30–365 | CAPA effectiveness timer (BR-33.9) | n/a | Notification task follow-up (effectiveness check) |
| capa_calendar | site | calendar ref | site calendar | wave-6 calendars | Business days for CAPA due dates | Quality management parameters "Hours per day" | Notification priority response times (T356) |
| instrument_warn_days | site | int | 14 | 0–90 | Calibration warning horizon (BR-33.8) | Test instruments | Maintenance plan call horizon (IP10) |
| results_decimals | test | int | per test | 0–6 | Rounding before evaluation (BR-33.6) | Test "Precision" | MIC decimal places (QS21) |
| coa_auto_on_shipment | customer | bool | false | true / false | CoA generated at ship confirmation (FR-33.9) | Certificate of analysis "Print with packing slip" | Quality info record sales (QV51) + output determination application QC |
| scorecard_weights | tenant | jsonb | quality 40 / delivery 30 / NCR 20 / audit 10 | Σ = 100 | Scorecard formula (FR-33.10) | Vendor evaluation criteria (VendEvaluationCriterion) | Weighting keys and criteria (T147*) |
| compliance_block_mode | compliance list | enum | BLOCK | BLOCK / WARN | Line behavior on restriction (FR-33.13) | Restricted list "Restricted" vs "Warning" | Product marketability check rule result |
| sds_validity_days | item × region | int | 1,095 | 30–3,650 | SDS expiry (FR-33.14) | Product compliance › SDS parameters › "Product safety data sheet validity" / MSDS validity days | Product compliance SDS validity |
| sds_missing_action | legal entity | enum | HOLD | HOLD / WARN / NONE | Shipment behavior without valid SDS (BR-33.12) | SDS "Send on sales" check | EHS report shipping check in delivery (CVD1) |
| certificate_required_default | quality association | enum | NONE | NONE / COA / COC / EN10204_31 | Certificate demand (FR-33.15) | n/a | QI01 certificate type; QM control key "certificate required" |
| source_inspection_lead_days | vendor × item | int | 5 | 0–60 | Source inspection trigger (FR-33.19) | n/a | QI01 source inspection indicator with QI07 lead time |
| retention_sample_years_after_expiry | site | int | 1 | 0–10 | Retention sample expiry (FR-33.20) | n/a | Sample type retention period (sample management customizing) |
| stability_lead_days | site | int | 7 | 0–30 | Order creation ahead of pull (BR-33.14) | n/a | Stability study scheduling parameters (QST) |
| stability_pull_window_days | study | int | 3 | 0–14 | Late pull deviation threshold (BR-33.14) | n/a | Stability study customizing |
| inspection_point_grace_pct | operation | int | 25 | 0–100 | Missed-point grace (BR-33.18) | n/a | Inspection point identification (TQ79) |
| block_on_missed_point | operation | bool | false | true / false | RAF block on missed point (BR-33.18) | n/a | Inspection point completion requirement |
| fmea_ap_table_version | tenant | reference data | AIAG-VDA-2019 | versioned | Action priority lookup (BR-33.15) | n/a | FMEA customizing: risk evaluation catalog |
| four_eyes_decision | site | bool | false | true / false | Decision user ≠ results user | n/a | Digital signature / UD authorization (QM) |

### 6.33.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Quality order worklist | list inquiry | status, site, trigger, age, item | order, lot qty, sample, due, status | `quality_order` | Quality orders list page | QA32 / QA33 |
| Results worklist | list inquiry | characteristic, work center, status | open result slots, inspection point, due | `quality_result` | Quality order line results | QE51N |
| Results history by item and test | drill-down | item, test, vendor, period | value, limits, pass/fail, instrument | `quality_result` | Test result statistics | QGA3, QE03 |
| NCR and CAPA status | list inquiry | source, severity, status, overdue | NCR, current D-step, due date, owner | `nonconformance` + workflow | Nonconformances list, corrections inquiry | QM10–QM12 |
| Supplier quality scorecard | KPI | vendor, period | lots, acceptance rate, PPM, NCRs, audit score, total | scorecard | Vendor evaluation, SSRS vendor quality performance | ME6H, QGA2 |
| SPC control chart | KPI | item, test, plant | X̄/R or p/np, limits, rule violations | SPC projection | n/a | QGC1–QGC3 |
| Cost of quality | KPI | period, category, site | prevention, appraisal, internal and external failure | journal by `coq_category` | SSRS cost of quality | QM order cost reports (KOB1) |
| Quality hold reconciliation | operational report | site | held qty in ledger vs open orders | reconciliation job | n/a | QA32 with MMBE inspection stock |
| Certificate receipt overview | list inquiry | vendor, item, status | required, received, waived, NOK | `incoming_certificate` | n/a | QC55 |
| Regulated and restricted product hits | list inquiry | list, region, item | substance, % w/w, threshold, action | `compliance_result` | Product compliance inquiries (regulated items) | Product marketability workbench |
| SDS due and distribution log | operational report | item, region, expiry window | version, valid_to, customers sent, pending | `safety_data_sheet`, `sds_delivery` | SDS due inquiry, SSRS Safety data sheet | CVD1 report shipping orders |
| Stability study plan | drill-down | study, condition | pull points, status, result trend | `stability_study` | n/a | QST03 |
| Audit findings | list inquiry | audit, severity, status | finding, clause, CAPA link | `audit` | n/a | PLMD_AUDIT |
| Instrument calibration due | list inquiry | due within n days, site | instrument, due_at, status | `instrument` | Test instruments | IP19 / IP24 scheduling overview |

### 6.33.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Certificate of analysis | Ship confirmation or accepting decision | Header: customer, PO, item, batch, manufacturing and expiry dates; lines: test, method, limits of the applicable specification set (FR-33.21), result, unit; footer: signer, revision, supersedes | PDF/A-2; email; customer portal; EDI attachment | COA-* | Retained ≥ 1 year past batch expiry (21 CFR 211.180) or 10 years, configurable |
| Quality order and sampling instruction | Order created | Lot, sample qty, tests, sample-drawing procedure | PDF | QO-* | Operational, per quality record retention |
| Sample label | Sample registered | Sample id barcode, order, storage condition, retention expiry | ZPL | SMP-* | None beyond sample record |
| Nonconformance report / NC tag | NCR created | NCR id, source, severity, qty, quarantine location | PDF; ZPL tag | NCR-* | Quality record, 10 years default |
| 8D report | CAPA closed | D1–D8 contents, effectiveness check result | PDF | NCR id | Customer deliverable; IATF 16949 retention per customer |
| Safety data sheet | First shipment and each new version (BR-33.12) | GHS 16 sections | PDF attached to shipment email or portal | SDS version | REACH Art. 36: information kept 10 years |
| Stability study report | Pull point and study completion | Conditions, pull points, results, trend, conclusion | PDF | STB-* | Product registration file retention |
| Audit report | Audit REPORTED | Scope, criteria, findings, score | PDF | AUD-* | ISO 9001 §7.5 documented information |
| FMEA and control plan | Release | AIAG-VDA form, control plan columns | PDF / XLSX | Version | PPAP submission |
| Supplier scorecard | Monthly | Score components, trend, NCRs | PDF via supplier portal | none | None |

### 6.33.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Scorecard materialization | Monthly, 1st 03:00 | vendor | period | < 10 min | Rerun period | Scorecard missing on 2nd |
| Instrument due scan | Daily 05:00 | site | date | < 1 min | Rerun | Overdue instrument with results attempted |
| SPC recompute | Hourly | item × test | last result id | < 5 min | Resume from last id | Lag > 3 h |
| Compliance re-evaluation | On BOM/list change; full run nightly 01:00 | item hash range | (item, list version) | 50,000 items < 30 min | Resume by hash range | Failed items > 0 |
| SDS expiry and redistribution | Daily 04:00 | item × region | SDS version | < 5 min | Rerun | SDS expiring in < 60 days without review task |
| Stability pull scheduling | Temporal timer per study | study | workflow id | n/a | Workflow retry | Missed pull deviation |
| Retention sample disposal | Daily 06:00 | site | sample id | < 1 min | Rerun | none (tasks) |
| Source inspection trigger | Daily 06:00 | vendor | PO line + due date | < 2 min | Rerun | Orders not created for due lines |
| Quality hold reconciliation | Daily 02:30 | site | snapshot | < 5 min | Report only | Held qty ≠ open lot qty |

### 6.33.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| QM_SETUP_MAINTAIN | /quality/v1/test-groups, /associations, specification sets, sampling plans | QM_USAGE_DECISION (change limits, then accept against them) | Quality administrator |
| QM_ORDER_MAINTAIN | /quality-orders (manual); workbench | none | Quality inspector |
| QM_RESULTS_RECORD | /quality-orders/{id}/results, /certificates, /samples | QM_USAGE_DECISION on the same order when `four_eyes_decision = true` | Lab technician, inspector |
| QM_USAGE_DECISION | /quality-orders/{id}:usage-decision; certificate waiver | QM_SETUP_MAINTAIN | QA release officer |
| QM_NCR_MAINTAIN | /nonconformances | none | Quality engineer |
| QM_CAPA_MAINTAIN | /nonconformances/{id}/corrections | Effectiveness check must be approved by another user | Quality engineer |
| QM_COA | /coa:generate | none | QA clerk |
| QM_COMPLIANCE_MAINTAIN | /compliance-lists, /items/{id}/sds | SALES_ORDER_MAINTAIN (wave 26): clearing a block on own order | Product steward / EHS |
| QM_STABILITY_MAINTAIN | /stability-studies | none | Stability coordinator |
| QM_AUDIT_MAINTAIN | /audits | Auditor may not audit own org unit (checked) | Internal auditor |
| QM_FMEA_MAINTAIN | /fmeas, control plans | none | Quality planner |
| QM_VIEW | /sampling-plans:size, /scorecards, SPC, all inquiries | none | All quality roles, purchasing |

### 6.33.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| QM-0101 | error | {item} is on quality hold (order {qo}); {action} not allowed. | QUALITY_HOLD on quant | Wait for usage decision | FR-33.5 |
| QM-0102 | error | Instrument {inst} is overdue for calibration since {date}. | INSTRUMENT_OVERDUE | Calibrate or use another instrument | BR-33.8 |
| QM-0103 | warning | Instrument {inst} calibration is due in {n} days. | Warning horizon reached | Schedule calibration | BR-33.8 |
| QM-0104 | error | Decision quantities {sum} do not equal lot quantity {lot}. | Split mismatch | Correct the split | FR-33.6 |
| QM-0105 | error | Required test {test} has no result. | Decision before results | Record result or deviate with reason | §6.33.5 commands |
| QM-0106 | error | Inventory hold could not be placed; source transaction rolled back. | Ledger PlaceHold failed | Retry; check ledger health | BR-33.5 |
| QM-0107 | info | Inspection severity changed to {sev} for {item}/{vendor}. | Switching rule fired | None | BR-33.2 |
| QM-0108 | warning | Value {v} is outside {min}–{max} for {test}. | Out of tolerance | Retest or record failure | BR-33.6 |
| QM-0109 | error | Order {qo} is closed; amend through a nonconformance. | Change on CLOSED order | Create NCR | BR-33.4 |
| QM-0110 | warning | CAPA step {d} is {n} business days overdue; escalated. | Timer expired | Complete step | BR-33.9 |
| QM-0121 | warning | {item} contains {substance} at {c} % (threshold {t} %); {list} duty applies in {region}. | Reporting threshold exceeded | Issue required communication | BR-33.11 |
| QM-0122 | error | {item} is restricted in {region} under {list}; line blocked. | Restricted substance above limit | Change source or destination | BR-33.11 |
| QM-0123 | info | SDS {version} attached to shipment {ship}. | New version for customer | None | BR-33.12 |
| QM-0124 | error | No valid SDS for {item} in {language}/{region}; shipment held. | Missing SDS | Upload SDS | BR-33.12 |
| QM-0125 | error | Certificate {type} is required before acceptance. | Missing or NOK certificate | Obtain certificate or waive with reason | BR-33.13 |
| QM-0126 | warning | Stability pull {n} for study {s} is {d} days late. | Late pull | Record deviation | BR-33.14 |
| QM-0127 | error | FMEA {f} has high-priority items without actions. | H items without action | Add actions | BR-33.15 |
| QM-0128 | info | Batch {b} was source-inspected; receiving inspection skipped. | Accepted source inspection | None | BR-33.16 |
| QM-0129 | warning | Lot does not conform to specification set {set}. | Fails set limits | Allocate elsewhere or rework | BR-33.17 |
| QM-0130 | error | Inspection point {p} missed on operation {op}; RAF blocked. | No result within grace | Record result; supervisor review | BR-33.18 |
| QM-0131 | error | Audit cannot close: {n} major findings without CAPA. | Missing CAPA | Create CAPA | FR-33.17 |

### 6.33.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Quality order creation in receipt path | 2,000 receipts/day; peak 200/min | +20 / 60 / 100 ms on receipt | 40/s | Orders ≈ 1 GB | `quality_order(status, site_id)` | Hold stays synchronous; order body completes via outbox |
| Results batch | 50,000 results/day; batches of 1,000 | 400 ms / 2 s / 3 s per 1,000 | 2,000 results/s | Results ≈ 8 GB | `quality_result` monthly partitions | Batch accepted asynchronously with status polling |
| Usage decision posting | 1,500/day | 120 / 500 / 900 ms | 20/s | — | — | Queued per order; no partial postings |
| Compliance evaluation | 50,000 items × 40 lists | 20 / 80 / 200 ms per item; full run < 30 min | 50 items/s | Results ≈ 2 GB | `compliance_result(item_id, list_version)` | Incremental runs continue; full run deferred to night |
| SDS check at shipment | 5,000 shipments/day | 5 / 20 / 40 ms (cached) | 100/s | SDS PDFs ≈ 5 GB in object storage | `safety_data_sheet(item_id, region, language, status)` | Cache serves; holds still applied |
| SPC recompute | 2,000 item × test series | < 5 min per hour | — | Chart points ≈ 1 GB | Series key | Recompute interval stretches to 2 h |
| Inspection point due evaluation | 500 active operations | 1-min resolution | — | Points ≈ 0.5 GB | `inspection_point(operation_id, due_at)` | Evaluation lag ≤ 3 min; RAF block decision waits for evaluation |

### 6.33.20 Localization and compliance hooks

- **Regulated quality.** US 21 CFR Part 11 (electronic records and signatures: usage decisions, CoA signatures and CAPA approvals re-authenticate with the meaning of signature and keep an audit trail), 21 CFR 211.166 (stability), 211.170 (reserve samples), 211.180 (record retention); EU GMP Annex 11 and Annex 16 (Qualified Person certification); medical devices 21 CFR 820 and ISO 13485; automotive IATF 16949 with AIAG-VDA FMEA and PPAP. The `RegulatedQualityPack` extension point (wave 48) sets signature policies, four-eyes defaults and retention.
- **Stability conditions.** ICH climatic zones I, II, III, IVa (30 °C/65 % RH) and IVb (30 °C/75 % RH) are reference data selected by country pack for the target market.
- **Product compliance lists.** EU REACH and CLP, RoHS; UK REACH; US TSCA and California Proposition 65; China RoHS. Lists arrive through `ComplianceListProvider` (country pack or subscription feed). SDS language follows the destination member state (REACH Art. 31) and the GHS revision adopted by each jurisdiction.
- **Certificates.** EN 10204 types 2.1, 2.2, 3.1 and 3.2 are reference data; country packs may add national forms.
- **Retention.** Batch records and CoAs: US 1 year past expiry; EU GMP 1 year past expiry or 5 years after QP certification, whichever is longer; configured per pack through the wave-46 archive.
- **Units and formats.** Results are stored with declared decimals; decimal separator and unit display follow user locale.
- **Privacy.** Audit participants, CAPA owners and inspectors are personal data under GDPR; quality records keep names under the legal-obligation basis for the retention period.

### 6.33.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX GAP #48 — §12.15.1 Released product → Product compliance (regulated products, restricted lists, reporting details, SDS with validity and send on sales) | GAP (propose 33) | ADDED as FR-33.13 and FR-33.14 | BR-33.11, BR-33.12 |
| AX GAP #49 — §12.15.2 Product compliance inquiries (regulated items, SDS due) | GAP (propose 33) | ADDED as FR-33.13 and FR-33.14 | Inquiries in §6.33.14 |
| AX GAP #50 — §12.15.2 SSRS: Safety data sheet | GAP (propose 33) | ADDED as FR-33.14 | Document in §6.33.15 |
| AX GAP #51 — §12.15.3 Product compliance → Expire SDS / Reporting | GAP (propose 33) | ADDED as FR-33.14 and FR-33.13 | Jobs in §6.33.16 |
| AX GAP #52 — §12.15.4 Product compliance setup (regulated regions, restricted lists, reporting lists, SDS parameters, MSDS validity days) | GAP (propose 33) | ADDED as FR-33.13 and FR-33.14 | Parameters in §6.33.13 |
| AX GAP #66 — §12.23.1 Process manufacturing: Product compliance / SDS (link) | GAP (propose 33) | ADDED as FR-33.14 | Same mechanism as #48 |
| AX GAP #67 — §12.23.2 SSRS: Safety data sheet (link) | GAP (propose 33) | ADDED as FR-33.14 | Same document as #50 |
| AX GAP #68 — §12.23.3 Compliance → SDS expiry / regulated product reporting | GAP (propose 33) | ADDED as FR-33.14 and FR-33.13 | Same jobs as #51 |
| AX GAP #70 — §12.23.4 Product compliance → regulated/restricted lists, reporting lists, SDS setup | GAP (propose 33) | ADDED as FR-33.13 and FR-33.14 | Same parameters as #52 |
| AX GAP #72 — §12.25.4 Quality management → Product compliance (link) | GAP (propose 33) | ADDED as FR-33.13 | Owned by `quality` service |
| AX GAP #80 — §12.29.1 Organization administration: Product safety data sheets (link) | GAP (propose 33) | ADDED as FR-33.14 | Same mechanism as #48 |
| AX GAP #87 — §12.29.4 Product safety data sheets setup (link) | GAP (propose 33) | ADDED as FR-33.14 | Same parameters as #52 |
| SAP #282 — §13.24 QC51 / QC52 / QC53 / QC55 certificate receipt | PARTIAL-33 | ADDED as FR-33.15 | BR-33.13, AC-33.12 |
| SAP #283 — §13.24 QST01–QST07 stability study | GAP (propose 33) | ADDED as FR-33.16 | BR-33.14, AC-33.13 |
| SAP #284 — §13.24 PLMD_AUDIT audit management | PARTIAL-33 | ADDED as FR-33.17 | Was out of scope in §6.33.1; scope amended |
| SAP #285 — §13.24 FMEA (QM-FMEA, S/4), control plan | GAP (propose 33) | ADDED as FR-33.18 | BR-33.15, AC-33.17 |
| SAP #286 — §13.24 Source inspection (QI01 flag, QI07) | PARTIAL-33 | ADDED as FR-33.19 | BR-33.16, AC-33.14 |
| SAP #287 — §13.24 Sample management (QPR1–QPR7) | PARTIAL-33 | ADDED as FR-33.20 | AC-33.18 |
| SAP #288 — §13.24 Multiple specifications | PARTIAL-33 | ADDED as FR-33.21 | BR-33.17, AC-33.15 |
| SAP #289 — §13.24 QC55 certificate receipt overview | PARTIAL-33 | ADDED as FR-33.15 | "Certificate receipt overview" in §6.33.14 |
| SAP #290 — §13.24 QST stability schedule generation | GAP (propose 33) | ADDED as FR-33.16 | "Stability pull scheduling" job |
| SAP #291 — §13.24 TQ79 inspection points (time/quantity/free), partial lots | PARTIAL-33 | ADDED as FR-33.22 | BR-33.18, AC-33.16 |
| SAP #292 — §13.24 Stability study customizing | GAP (propose 33) | ADDED as FR-33.16 | `stability_*` parameters |
| SAP #293 — §13.24 FMEA / control plan customizing (S/4) | GAP (propose 33) | ADDED as FR-33.18 | `fmea_ap_table_version` |
| SAP #294 — §13.24 Audit management customizing (audit types, question lists) | PARTIAL-33 | ADDED as FR-33.17 | Question lists are versioned metadata |
| SAP #295 — §13.24 Multiple specifications customizing | PARTIAL-33 | ADDED as FR-33.21 | Specification set types reference data |
| SAP #296 — §13.24 Sample management customizing (sample types, sample-drawing procedures) | PARTIAL-33 | ADDED as FR-33.20 | `retention_sample_years_after_expiry` |

## 6.34 Wave 34: Transportation Management & Freight

### 6.34.1 Scope and business context

Wave 28 integrated parcel carriers for labels; wave 34 adds full transportation management replacing D365 TMS and SAP TM: shipments consolidated into loads and routes, rated against carrier rate cards with accessorials and fuel surcharges, rate-shopped, tendered to carriers with timeouts, tracked by EDI 214 or carrier APIs, and reconciled against carrier freight invoices with variance disputes and accruals. Freight and duties are allocated to inventory as landed cost feeding wave-20 costing. VRP route optimization runs on OR-Tools in the Python `tms-optimizer`. Out of scope: own-fleet driver apps and ELD, ocean booking, customs filing (hooks only), multi-leg international routing guides beyond two legs.

**Scope amendment (coverage closure).** The gap registers add the following to this wave: landed-cost voyages with shipping containers, folios and goods-in-transit orders (FR-34.12, extending FR-34.9); yard management with transportation units (FR-34.13); driver check-in/out with a gate log (FR-34.14); dangerous-goods checks for transport (FR-34.15); own-fleet resources as planning constraints (FR-34.16); lane RFQs for strategic freight procurement (FR-34.17); layer-based load stacking (FR-34.18); and foreign-trade document and screening hooks (FR-34.19). Driver mobile apps and ELD/tachograph integration stay out of scope. Customs filing, preference determination and the denied-party screening provider are wave 48 country and compliance pack content behind the hooks in FR-34.19. §6.34.21 lists the dispositions.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| D365 rate route workbench (TMSRateRouteWorkbench) shows a total per carrier with no breakdown; a wrong rate (missing rate base assignment, fuel index not loaded) is found when the carrier invoice arrives weeks later | Rate engines (TMSRateEngineBase subclasses) return totals; no explain | Rating returns an explain trace per charge step; `NO_VALID_RATE_CARD` is explicit | Rate errors detected: at invoice (2–6 weeks) → at rating (< 300 ms); rating disputes caused by setup: ~5% of invoices → < 0.5% |
| SAP TM carrier selection and tendering (freight order subcontracting) require TM configuration plus PI/PO for EDI 204/990; tender timeouts are monitored manually in the transportation cockpit | Tendering is a status field, not a durable process | Temporal `TenderWorkflow` with cascade, timeouts and carrier portal/EDI responses | Planner interventions per 100 tenders: ~30 (watching expiry) → < 3; tender-to-acceptance: hours of chasing → automatic cascade within the timeout |
| D365 freight reconciliation (TMSFreightBill ↔ TMSCarrierInvoice) is matched by hand per detail line; D365 lacks EDI 210 intake; accrual reversal needs a separate periodic job and often double-counts | Matching is a form, not a rule set; accrual lifecycle disconnected from the match | Rule-based line matching with tolerances, state machine to AP, accrual/reversal/invoice share one journal reference | Reconciliation effort: ~10 min per invoice → < 30 s for in-tolerance invoices (≥ 80% auto); account 2150 ties to zero per bill, 100% |
| Landed cost allocation (D365 Landed cost module voyages; SAP delivery-cost conditions FRA1/FRB1 on PO lines) rounds per line and leaves cent residuals in a variance account; multi-PO containers need manual apportionment | Per-line rounding without residual assignment; cost types not linked to receipts across POs | Allocation with residual to the highest-value line; voyage spans POs; estimated/actual variance rules by on-hand | Allocation residual: cents per voyage in variance → 0; time to land a 3-PO container: 1–2 h → < 5 min |
| Route planning in D365 is manual load building; SAP TM VSR optimizer requires PP/DS-style setup and licensing | No general VRP in D365; SAP optimizer is a separate engine with heavy configuration | OR-Tools VRP with explicit constraints, 60 s limit, planner override | Multi-stop planning time: 30–60 min per day of loads → < 1 min; route distance vs manual: −10% or better |

### 6.34.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Carriers / services | TMSCarrier, TMSCarrierService, TMSCarrierServiceGroup | Business partner carrier role, /SCMTMS/ means of transport | tms.Carrier, CarrierService | full |
| Rate cards | TMSRateMaster, TMSRateBase, TMSRateBaseAssignment, TMSRateEngine classes (TMSRateEngineBase) | Freight agreements, rate tables, calculation sheets (/SCMTMS/TCM, /SCMTMS/TCCS) | RateCard, RateTable, CalcSheet | full |
| Accessorials / fuel | TMSAccessorialMaster, TMSAccessorialAssignment, TMSFuelIndex, TMSFuelIndexRegion | Charge types, fuel surcharge index tables | Accessorial, FuelIndex | full |
| Rate shopping / routing | Rate route workbench (TMSRateRouteWorkbench), TMSRoutePlan, TMSRouteGuide, TMSRouteSegment | Carrier selection (/SCMTMS/CAR_SEL), transportation cockpit | RateShop, RouteGuide | full |
| Loads / freight orders | WHSLoadTable (TMS fields), load building strategies | Freight units, freight orders /SCMTMS/TOR (TOR_ROOT/TOR_ITEM/TOR_STOP), freight bookings | Shipment, Load, Route | full |
| Tendering | Load tendering (manual/EDI 204, TMSLoadTender status) | Tendering (freight order subcontracting, EDI 204/990 via SAP TM) | Tender workflow (Temporal) | full |
| Tracking | n/a (ISV, driver check-in) | Event management (EM) / TM execution tracking | TrackingEvent (EDI 214/API) | improved |
| Freight reconciliation | TMSFreightBill, TMSFreightBillDetail, TMSCarrierInvoice, TMSCarrierInvoiceDetail, freight bill matching | Freight settlement document (/SCMTMS/SFIR), ERS/MIRO | FreightBill, CarrierInvoice, Match | full |
| Landed cost | Landed cost module (voyage, cost type, allocation) | Delivery costs on PO (FRA1/FRB1 conditions), actual costing | LandedCostVoyage, allocation to inventory | full |
| Route optimization | n/a (ISV) | PP/DS-based VSR optimizer | VRP (OR-Tools) | improved |
| Dock scheduling | WHSDockAppointment (wave 31) | Yard | wms.DockAppointment | full (wave 31) |
| Landed cost voyages, containers, folios, goods in transit | Landed cost module: voyages (ITMVoyage*), shipping containers, folios, cost types and auto costs, goods-in-transit orders, voyage tracking | Delivery cost conditions FRA1/FRB1, MRDC settlement, inbound delivery with transportation units, stock in transit (S/4 SIT) | LandedCostVoyage + VoyageLeg, ShippingContainer, Folio, GoodsInTransitOrder | full |
| Yard management | D365 yard management (yard locations, trailers, yard moves) | EWM yard management /SCWM/YM_* (yard bins, checkpoints, TU/vehicle), /SCWM/TU, /SCWM/VEH | tms.YardLocation, TransportUnit, YardMove; wms work type YARD_MOVE | full |
| Driver check-in / gate | Driver check-in and check-out (appointment scheduling) | EWM check-in/check-out at checkpoints (/SCWM/CICO) | DriverVisit, GateEvent | full |
| Dangerous goods | Hazardous material flags (limited); product compliance | EHS-DG: DG master, DG check methods and profiles in SD/LE/TM, DG documents | HazmatProfile (wave 31) + DgCheckResult at rating, load building, tender, ship | full |
| Fleet resources | n/a (ISV) | TM resources: vehicles, trailers, drivers, resource calendars | tms.Vehicle, Trailer, Driver as VRP constraints | partial — planning only; no driver app or ELD |
| Strategic freight procurement | n/a | TM strategic freight procurement (freight RFQ, bid analysis, award to freight agreements) | FreightRfq on the wave-17 RFQ engine → RateCard | full |
| Load plan stacking | Load building templates (weight/volume only) | TM load planning (package building, 3D load plan) | LoadPlan in tms-optimizer (layer heuristic) | partial — layer stacking, no free-form 3D carton packing |
| Foreign trade documents and screening | Foreign trade parameters (Intrastat only) | SAP GTS: customs declarations, export control, sanctioned-party list screening, preference | Customs data on shipments, commercial invoice, `ComplianceScreeningProvider` and `CustomsFilingProvider` hooks | partial — hooks; filing and preference in wave 48 |

### 6.34.3 Functional requirements

- **FR-34.1 Shipments and loads** — Must. The system shall create TMS shipments from sales/transfer/purchase (inbound freight) shipments and consolidate them into loads by rule (same ship-from, ship-to region, mode, date window) or manually.
  - Notes: load capacity by weight, volume, pallet positions; load types TL, LTL, PARCEL, INTERMODAL; inbound loads for vendor-collect freight.
- **FR-34.2 Route planning with VRP** — Must. The system shall plan multi-stop routes with OR-Tools under the constraints in BR-34.1 and return feasible routes within 60 s for ≤ 500 stops.
  - Notes: distance/time matrix from OSRM (self-hosted) or stored lanes; first solution by parallel cheapest insertion; guided local search until the time limit.
- **FR-34.3 Rating engine** — Must. The system shall compute charges per BR-34.2 from rate cards (lane-based, zone-based, distance-based, weight-break, per pallet, per container), accessorials and fuel surcharges, with an explain trace.
  - Notes: rate cards effective-dated with currency; minimum charge; rounding to cents half-up; deterministic given the same inputs.
- **FR-34.4 Rate shopping and route guides** — Must. The system shall rate a shipment across eligible carriers/services and rank by cost, transit time or a weighted score; route guides pre-select carriers by lane with fallbacks.
  - Notes: eligibility by service area, mode, hazmat flag, max weight; results cached 10 min per input hash.
- **FR-34.5 Tendering** — Must. The system shall tender loads to carriers (e-mail/portal, EDI 204 via wave 35, API) with accept/decline/timeout, cascading to the next carrier by route guide rank.
  - Notes: Temporal `TenderWorkflow` per load; default timeout 2 h; broadcast tendering option (first accept wins).
- **FR-34.6 Tracking and POD** — Must. The system shall ingest status events (EDI 214 codes AF/X1/X3/D1 etc., carrier APIs, driver check-in) and proof of delivery (signature/photo), and expose expected vs actual per stop.
  - Notes: late-delivery alerts when ETA > window end; POD attaches to the sales shipment for wave-29 invoicing holds if required.
- **FR-34.7 Freight bills and reconciliation** — Must. The system shall create a freight bill (expected charges) at ship confirmation, match carrier invoices (EDI 210 or manual/API) line by line with tolerances, and route variances to dispute.
  - Notes: tolerance default $5 / 2% per charge; match states per BR-34.4; matched invoices create a vendor invoice in wave 19 AP.
- **FR-34.8 Freight accrual** — Must. The system shall post freight accruals (Dr freight expense or inventory-in-transit, Cr accrued freight 2150) at ship confirmation and reverse on invoice match through the universal journal.
  - Notes: journal postings via the wave-11 posting API with idempotency key = freight bill id; customer-billed freight creates a wave-29 invoice charge line.
- **FR-34.9 Landed cost** — Must. The system shall allocate freight, duty, brokerage and insurance to purchase receipt lines by weight, volume, value, quantity or equal split, and post the allocation to inventory cost (wave 20) as a cost adjustment.
  - Notes: voyage groups multiple POs/containers; estimated cost at receipt, actual on invoice with variance posting; allocation residual cents go to the highest-value line.
- **FR-34.10 Carrier portal** — Should. The system shall let carriers accept tenders, update status and upload POD via the wave-20 supplier portal framework.
  - Notes: carrier users are Keycloak external identities scoped to their loads.
- **FR-34.11 Transportation KPIs** — Should. The system shall report cost per shipment/cwt/mile, on-time pickup/delivery, tender acceptance rate, invoice variance rate.
  - Notes: Cube metrics; daily materialization.
- **FR-34.12 Landed cost voyages, containers, folios and goods in transit** — Must. The system shall track inbound international shipments as voyages with legs (origin port → destination port → warehouse), shipping containers (ISO 6346 number with check digit per BR-34.12, type 20GP/40GP/40HC/45HC/reefer, seal number), folios grouping containers per vendor or forwarder, and goods-in-transit orders that move ownership to an in-transit warehouse at the Incoterm transfer event (BR-34.11) and receive at the destination warehouse.
  - Notes: voyage status ESTIMATED → IN_TRANSIT → ARRIVED → RECEIVED → COSTED; estimated costs per cost type come from auto-cost templates (freight % of value or per container, duty from HS-code rate, insurance %) and post at goods-in-transit; actuals post at invoice with BR-34.6 variance rules; cost type codes map to accrual and clearing accounts (wave 11); goods-in-transit value sits on the wave-20 in-transit inventory account; voyage tracking stages (shipped, departed, arrived port, customs cleared, arrived warehouse) come from TrackingEvent; allocation per FR-34.9 can run per container first, then per line.
- **FR-34.13 Yard management** — Should. The system shall model yard locations (parking slots, door waiting lanes, checkpoints), transportation units (trailer, container, swap body) and vehicles, track TU position and status (EMPTY, LOADED, SEALED; reefer setpoint), and create yard-move work (wms work type YARD_MOVE) between slot and dock door, executed on the wave-32 mobile app.
  - Notes: TU ↔ load link from tender acceptance; door assignment respects DockAppointment (wave 31); slot capacity is one TU; dwell time per TU drives detention (BR-34.13, accessorial DETENTION after free time, default 2 h); a yard check is a count work over yard slots.
- **FR-34.14 Driver check-in/out and gate log** — Should. The system shall check drivers in at the gate or a kiosk against the dock appointment (appointment id or carrier SCAC + trailer/container number), capture seal number, driver name and license id, assign a yard slot or door, and check out with seal verification, recording each event in an append-only gate log.
  - Notes: matching and tolerance per BR-34.14 (default ±60 min); walk-ins allowed per site setting; check-out blocked if the load is not confirmed or the seal does not match; driver PII kept 90 days, then pseudonymized; check-in emits `dev.passage.tms.gate.checkedin.v1`, which releases the door work in wms.
- **FR-34.15 Dangerous goods checks in transport** — Should. The system shall use the wave-31 HazmatProfile to check DG compliance at rating, load building, tendering and ship confirmation: mode-specific regulation (49 CFR, ADR, RID, IMDG, IATA DGR), carrier-service DG eligibility, mixed-load segregation (49 CFR 177.848, ADR 7.5.2), ADR 1.1.3.6 quantity thresholds and limited-quantity rules, and produce DG shipping-paper lines and placard requirements.
  - Notes: result PASS/WARN/FAIL with rule ids (BR-34.15); FAIL blocks tender and ship confirmation when `dg_check_mode = BLOCK`; a 24-hour emergency response phone from legal-entity setup is mandatory before a DG shipment; DG lines print on the BOL with UN number, proper shipping name, class, packing group and quantity.
- **FR-34.16 Fleet resources** — Could. The system shall define own-fleet vehicles (weight/volume/pallet capacity, equipment type, depot, cost per km and per hour, availability calendar), trailers and drivers (qualifications such as hazmat endorsement or ADR certificate with expiry, availability calendar, hours-of-service rule set) and pass them to the VRP as heterogeneous fleet and driver-hours constraints (BR-34.16).
  - Notes: dispatch assigns vehicle + driver to a route; telematics positions ingest as TrackingEvent with source TELEMATICS; driver apps, ELD and tachograph download remain out of scope.
- **FR-34.17 Strategic freight procurement (lane RFQ)** — Could. The system shall run a freight RFQ on the wave-17 RFQ engine with lanes (origin and destination zones, mode, equipment, annual volume, weight bands) as lines, collect carrier bids through the carrier portal (FR-34.10) or spreadsheet upload, compare bids by scenario, and award lanes into effective-dated RateCards and route-guide ranks (BR-34.18).
  - Notes: bid cost = Σ lane annual volume × bid rate + fuel at the current index; scenarios: lowest cost per lane, maximum carriers per lane, minimum incumbent share; awarded cards are drafts that need TMS_RATE_APPROVE.
- **FR-34.18 Load plan stacking** — Could. The system shall compute a load plan for a trailer or container with layer-based stacking (pallet footprint and height, maximum stack weight, stackable flag, orientation lock, load sequence in reverse stop order) and show a top view with layers, floor positions used and an axle-weight estimate.
  - Notes: first-fit decreasing by footprint with layer building in `tms-optimizer`; non-stackable pallets and DG segregation respected; < 5 s for 60 pallets; free-form 3D carton packing is not in scope.
- **FR-34.19 Foreign-trade document and screening hooks** — Should. The system shall carry customs data on shipments and loads (commodity code per item and country such as HS/HTS/Schedule B, country of origin, export classification such as ECCN, Incoterms 2020 rule with named place, customs value and currency), generate commercial invoice and customs packing list documents, call a `ComplianceScreeningProvider` for denied-party and sanctions screening at load release, tender and ship confirmation (BR-34.17), and call a `CustomsFilingProvider` (e.g. US AES/EEI, EU export declaration) at ship confirmation.
  - Notes: screening result CLEAR / POTENTIAL_MATCH / BLOCKED / SCREENING_PENDING; with no provider configured, cross-border shipments carry `NOT_SCREENED` and warning TMS-0134; provider implementations, preference/origin determination (USMCA, EU origin) and filing rules ship in wave 48.

### 6.34.4 Data model

**Load (TMS)**

| Field | Type | Required | Notes |
|---|---|---|---|
| load_id | uuid | yes | PK; shared id with wms.Load |
| mode, equipment | enum/text | yes | TL/LTL/PARCEL/INTERMODAL; `53FT_DRY`, `REEFER` |
| ship_from_site_id | uuid | yes | |
| carrier_id, service_id | uuid | no | set on tender accept |
| status | enum | yes | PLANNED, RATED, TENDERED, ACCEPTED, DISPATCHED, IN_TRANSIT, DELIVERED, CLOSED, CANCELLED |
| total_weight_kg, total_volume_m3, pallet_positions | numeric/int | yes | |
| planned_pickup_start/end | timestamptz | yes | window |
| route_id | uuid | no | FK Route |
| shipments | uuid[] | yes | via LoadShipment |

**RateCard / RateTable**

| Field | Type | Required | Notes |
|---|---|---|---|
| rate_card_id | uuid | yes | PK |
| carrier_id, service_id | uuid | yes | |
| basis | enum | yes | LANE, ZONE, DISTANCE, WEIGHT_BREAK, PER_PALLET, PER_CONTAINER, FLAT |
| currency | char(3) | yes | |
| valid_from, valid_to | date | yes | no overlap per carrier/service/basis |
| minimum_charge | numeric(18,2) | no | |
| rows | jsonb | yes | [{origin, destination, break_from, break_to, rate, unit}] |
| fuel_index_id | uuid | no | FK FuelIndex |
| accessorial_ids | uuid[] | no | |

**FreightBill / CarrierInvoice**

| Field | Type | Required | Notes |
|---|---|---|---|
| freight_bill_id | uuid | yes | PK |
| load_id, carrier_id | uuid | yes | |
| expected_lines | jsonb | yes | [{charge_code, amount, currency, basis_trace}] |
| expected_total | numeric(18,2) | yes | |
| accrual_journal_id | uuid | no | |
| match_status | enum | yes | UNMATCHED, MATCHED, VARIANCE, DISPUTED, APPROVED, SETTLED |
| carrier_invoice_id | uuid | no | FK CarrierInvoice (number, date, lines, total, source EDI210/MANUAL) |
| variance_total | numeric(18,2) | no | |
| ap_invoice_id | uuid | no | wave-19 vendor invoice |

**Tender**: tender_id, load_id, carrier_id, rank, sent_at, expires_at, response (PENDING/ACCEPTED/DECLINED/EXPIRED), rate_quoted. **LandedCostVoyage**: voyage_id, cost lines (type, amount, currency, allocation_method), receipt lines, status (ESTIMATED/ACTUAL/POSTED). **TrackingEvent**: load_id, stop_no, code (EDI 214 status), occurred_at, location, source.

Invariants: a load has at most one ACCEPTED tender; Σ allocated landed cost per voyage cost line = cost line amount (to the cent); a freight bill's accrual is reversed exactly once; shipments on a load share ship-from site and mode. Indexing: `load(status, planned_pickup_start)`, `rate_card(carrier_id, service_id, valid_from, valid_to)`, `freight_bill(match_status)`, `tracking_event(load_id, occurred_at)`.

### 6.34.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /tms/v1/loads:build | Build loads from shipments by rule | key | TMS_PLAN |
| POST | /tms/v1/routes:optimize | VRP over loads/stops; returns routes | key = request hash | TMS_PLAN |
| POST | /tms/v1/shipments/{id}:rate | Rate across carriers, return ranked with explain | — (cached) | TMS_RATE |
| POST | /tms/v1/loads/{id}:tender | Start tender workflow | key | TMS_TENDER |
| POST | /tms/v1/tenders/{id}:respond | Carrier accept/decline | key | TMS_CARRIER_RESPOND |
| POST | /tms/v1/tracking-events | Ingest events (214/API) | key = source+ref | TMS_TRACK_INGEST |
| POST | /tms/v1/carrier-invoices | Register carrier invoice; triggers match | key = carrier+invoice no | TMS_RECONCILE |
| POST | /tms/v1/freight-bills/{id}:approve | Approve variance / release to AP | key | TMS_RECONCILE_APPROVE |
| POST | /tms/v1/landed-cost/voyages/{id}:allocate | Allocate and post | key | TMS_LANDED_COST |
| GET | /tms/v1/loads/{id}/tracking | Timeline | — | TMS_VIEW |
| POST | /tms/v1/voyages | Create a voyage with legs, containers and folios | key | TMS_LANDED_COST |
| POST | /tms/v1/goods-in-transit/{id}:ship | Post goods in transit at the transfer event | key | TMS_LANDED_COST |
| POST | /tms/v1/gate/check-in | Driver and TU check-in | key = appointment + timestamp | TMS_GATE |
| POST | /tms/v1/gate/check-out | Check-out with seal verification | key | TMS_GATE |
| POST | /tms/v1/yard-moves | Create yard-move work | key | TMS_YARD |
| POST | /tms/v1/loads/{id}:dg-check | Run DG checks for a load | — | TMS_PLAN |
| POST | /tms/v1/loads/{id}:screen | Run trade-compliance screening | — | TMS_PLAN |
| POST | /tms/v1/freight-rfqs/{id}:award | Award lanes into rate cards | key | TMS_RATE_APPROVE |

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.tms.shipment.rated.v1 | rating | shipment_id, options[{carrier, service, total, transit_days}], chosen | sales (freight charge), analytics |
| dev.passage.tms.tender.accepted.v1 / declined.v1 / expired.v1 | tender response | load_id, carrier_id, rank, rate | wms, edi (204/990), analytics |
| dev.passage.tms.tracking.event.v1 | ingest | load_id, stop_no, code, occurred_at | sales-fulfillment, customer portal |
| dev.passage.tms.freightbill.matched.v1 | match | freight_bill_id, carrier_invoice_id, variance, lines[] | AP, costing |
| dev.passage.tms.landedcost.posted.v1 | allocation posted | voyage_id, receipt_lines[{line, amount}] | costing (wave 20) |

### 6.34.6 User experience

| Screen | Primary actions | Key fields | Duty |
|---|---|---|---|
| Transportation workspace | Shipments to plan, loads, tenders pending, exceptions | shipment, weight, window, status | TMS_VIEW |
| Load planning workbench | Drag shipments to loads, optimize routes, map view | load, stops, capacity bars, route distance/time | TMS_PLAN |
| Rate shop | Compare carriers, explain, select | carrier, service, charges, transit, score | TMS_RATE |
| Tender board | Tender, cascade, override | load, carrier rank, expires, response | TMS_TENDER |
| Tracking | Timeline, map, POD | event code, time, location, ETA | TMS_VIEW |
| Freight reconciliation | Match, dispute, approve, release to AP | expected vs invoiced per charge, variance | TMS_RECONCILE |
| Landed cost voyages | Add costs, allocate, post | cost type, amount, method, allocation | TMS_LANDED_COST |

Interaction rules: planning workbench supports keyboard move (Alt+arrow) and multi-select; capacity bars turn red above 100%; route map uses self-hosted tiles; reconciliation grid highlights lines outside tolerance.

### 6.34.7 Business rules and edge cases

- **BR-34.1 VRP constraints.** Model as capacitated VRP with time windows and pickup/delivery (OR-Tools `RoutingModel`): (1) vehicle capacity dimensions weight_kg, volume_m3, pallets (hard); (2) stop time windows [earliest, latest] with soft lateness penalty $50/min up to 60 min, hard beyond; (3) max route duration 11 h driving + service times, max 14 h on-duty; (4) depot start/end per vehicle; (5) pickup-before-delivery precedence for milk runs; (6) heterogeneous fleet (equipment type must match shipment requirement, e.g., reefer); (7) vehicle-location compatibility (dock height, hazmat ban); (8) max stops per route 25; (9) objective = Σ distance cost + Σ time cost + fixed vehicle cost + lateness penalties; (10) time limit 60 s, first solution strategy `PARALLEL_CHEAPEST_INSERTION`, metaheuristic `GUIDED_LOCAL_SEARCH`. Infeasible stops are dropped with penalty 10,000 and reported, never silently.
- **BR-34.2 Rating algorithm.** For each eligible (carrier, service): select the rate card valid on ship date; compute base charge by basis (weight-break: find row with `break_from ≤ chargeable_weight < break_to`, charge = max(rate × weight, next-break deficit rule: compare with next break's minimum and take the lower); zone: origin/destination zone lookup; distance: miles × rate); chargeable weight = max(actual, dimensional weight = volume_in³ / DIM factor 139 for parcel, class-based for LTL); apply minimum charge; add accessorials (flat or % of base); add fuel surcharge = base × index % effective on ship date (weekly index); convert to shipment currency at the wave-12 rate type `FREIGHT`; round each charge to cents half-up; total = Σ charges. Explain trace lists each step with inputs.
- **BR-34.3 Tender cascade.** Rank from route guide; send to rank 1; on DECLINED or EXPIRED (timeout) send to next; after the last, status `TENDER_FAILED` and a planner task is created; a late acceptance after expiry is rejected with `TENDER_EXPIRED`.
- **BR-34.4 Match state machine.** `UNMATCHED → MATCHED` (all lines within tolerance) `→ APPROVED → SETTLED` (AP invoice posted); `UNMATCHED → VARIANCE → DISPUTED → (MATCHED | APPROVED with adjustment)`; a carrier invoice without a freight bill creates an `UNEXPECTED_INVOICE` exception and is never auto-approved. Duplicate carrier invoice numbers per carrier are rejected.
- **BR-34.5 Accrual and reversal.** Accrual posts on `load.shipped` with amount = expected total; on APPROVED, post reversal of the accrual and the AP invoice for the invoiced amount; variance to freight variance account 5250. All three postings share the freight bill id in the journal reference.
- **BR-34.6 Landed cost allocation.** Allocate each cost line by chosen method; compute each receipt line's share = round(cost × weight_i / Σ weight, 2); residual = cost − Σ shares assigned to the line with the highest value (ties: lowest line number). Estimated vs actual differences post to inventory if the item is still on hand (proportional to remaining qty) else to a purchase price variance account 5260 (wave 20 rules).
- **BR-34.7 Time windows** are stored in UTC with the stop's time zone; VRP uses the stop's local calendar; a window spanning midnight is valid.
- **BR-34.8 Load capacity exceeded** on manual planning is a warning (planner override logged); on rule-based building it is a hard split into a second load.
- **BR-34.9 Tracking event ordering**: events are applied by `occurred_at`; an out-of-order delivery event (D1) after pickup (AF) missing sets stop status DELIVERED and backfills PICKED_UP as inferred.
- **BR-34.10 Rate card overlap** on save is rejected; a card with `valid_to < ship_date` is ignored and the explain trace states `NO_VALID_RATE_CARD`.
- **BR-34.11 Goods-in-transit transfer event.** The event that moves goods into the in-transit warehouse is set per vendor × Incoterm rule; defaults: EXW, FCA, FAS, FOB, CFR, CIF, CPT, CIP → origin shipment event (risk passes at origin); DAP, DPU, DDP → destination receipt (no goods-in-transit order). Estimated landed cost posts at the transfer event; Incoterms define risk, so title transfer can be overridden per contract.
- **BR-34.12 ISO 6346 check digit.** Letters map to values A = 10, B = 12 … skipping multiples of 11 (K = 21, L = 23, U = 32, V = 34 …); each of the first 10 characters is multiplied by 2^position (position 0–9); check digit = (Σ mod 11) mod 10. Example: CSQU305438 → check digit 3, so CSQU3054383 is valid. Invalid numbers are rejected.
- **BR-34.13 Yard moves and detention.** Dwell = check-out − check-in; detention minutes = max(0, dwell − free time); expected DETENTION charge = ceil(detention minutes ÷ 60) × the carrier's hourly accessorial rate; a yard move cannot target an occupied slot or a door holding another TU's active appointment.
- **BR-34.14 Gate check-in matching.** Match by appointment id, else by carrier SCAC + trailer/container number + date; arrival within `gate_tolerance_min` → ON_TIME, otherwise EARLY or LATE with reason; at check-out the declared seal must equal the seal recorded at load confirmation (outbound) or at check-in (inbound sealed); mismatch blocks check-out with TMS-0129.
- **BR-34.15 DG checks.** Mixed-load segregation uses the regulation's table as reference data: FAIL if two classes marked prohibited share a load; ADR 1.1.3.6 points = Σ quantity × multiplier by transport category (category 1: × 50, 2: × 3, 3: × 1, 4: × 0; category 0 always requires full ADR); points > 1,000 → WARN with full ADR marking, documentation and driver training required; limited-quantity packages within the per-inner limit → WARN for marking only; a carrier service without DG eligibility is excluded from rate shopping (FR-34.4).
- **BR-34.16 Driver hours in VRP.** Per driver rule set: US property-carrying — driving ≤ 11 h within a 14-h on-duty window, 30-min break after 8 h cumulative driving; EU 561/2006 — daily driving ≤ 9 h (10 h when `extended_day_available`), 45-min break after 4.5 h driving; violations make the route infeasible for that driver (hard constraint).
- **BR-34.17 Screening gating.** Load release, tender and ship confirmation screen every party on the shipment (sold-to, ship-to, consignee, forwarder, carrier); results are cached `screening_cache_h` (default 24 h) per party + list version; BLOCKED stays until the list changes; POTENTIAL_MATCH and SCREENING_PENDING (provider timeout) hold the load until released by TRADE_COMPLIANCE_RELEASE with a logged reason; the hook never auto-clears.
- **BR-34.18 Lane RFQ award.** Each lane is awarded to the bid with the lowest normalized annual cost subject to maximum carriers per lane and minimum incumbent share; ties go to the higher on-time %; awarded RateCards start on the award effective date and overlapping existing cards are end-dated the day before (BR-34.10 still holds).

### 6.34.8 Acceptance criteria

- **AC-34.1** (FR-34.2, BR-34.1) Given Solomon C101, R101 and RC101 (100 customers), when optimized with a 60 s limit, then every route satisfies capacity and time windows and total distance is within 10% of best-known.
- **AC-34.2** (FR-34.3, BR-34.2) Given the 200-shipment rating fixture, when rated, then totals equal the expected charges to the cent and the explain trace lists weight-break, minimum, accessorial and fuel steps.
- **AC-34.3** (FR-34.5, BR-34.3) Given a route guide with 3 carriers and 10-minute timeouts (test clock), when carrier 1 ignores and carrier 2 declines, then carrier 3 receives the tender and, on acceptance, the load is ACCEPTED with carrier 3.
- **AC-34.4** (FR-34.7, BR-34.4) Given a freight bill of $412.50 and a carrier invoice of $418.00, when matched with tolerance $5/2%, then the bill is VARIANCE with $5.50 and no AP invoice exists until approval.
- **AC-34.5** (FR-34.8, BR-34.5) Given a load shipped and later matched, when postings are inspected, then accrual, reversal and AP invoice tie to zero on account 2150 for that freight bill.
- **AC-34.6** (FR-34.9, BR-34.6) Given a voyage with freight $1,000.00 allocated by weight over 3 receipt lines (weights 1, 1, 1 kg), when allocated, then shares are 333.33 / 333.33 / 333.34 and Σ = 1,000.00.
- **AC-34.7** (FR-34.6) Given a 214 with status D1, when ingested, then the stop is DELIVERED and the sales shipment shows delivered timestamp.
- **AC-34.8** (FR-34.1, BR-34.8) Given 30 shipments totaling 48,000 kg for a 20,000 kg TL rule, when loads are built, then 3 loads exist, none over capacity.
- **AC-34.9** (FR-34.12, BR-34.11) Given a voyage with 2 containers for 3 POs on FOB Shanghai, when the forwarder's departure event posts, then goods-in-transit orders move ownership to the in-transit warehouse with estimated freight and duty, and after the actual invoices the variance posts per BR-34.6 with Σ allocated = Σ actual costs to the cent.
- **AC-34.10** (BR-34.12) Given container number CSQU3054383, when entered, then it is accepted; CSQU3054384 is rejected with TMS-0124.
- **AC-34.11** (FR-34.13, BR-34.13) Given a trailer checked in at 08:00 with free time 2 h and checked out at 11:20, when the freight bill is built, then a DETENTION charge of 2 h × the carrier rate is expected.
- **AC-34.12** (FR-34.14, BR-34.14) Given an outbound load sealed with 778812, when the driver declares 778813 at check-out, then check-out is blocked with TMS-0129 and the gate log records the attempt.
- **AC-34.13** (FR-34.15, BR-34.15) Given a road load with a class 1.1D explosive and a class 3 flammable liquid under 49 CFR, when the load is tendered, then the DG check FAILs with the segregation rule id and no tender is sent.
- **AC-34.14** (FR-34.15, BR-34.15) Given a fixture load of 400 L of a transport-category-2 substance, when checked under ADR, then points = 1,200 and TMS-0131 is raised; at 300 L points = 900 and no warning is raised.
- **AC-34.15** (FR-34.16, BR-34.16) Given an EU driver without an extended day available and a route needing 10.5 h of driving, when optimized, then the route is infeasible for that driver and is reassigned or split.
- **AC-34.16** (FR-34.19, BR-34.17) Given a ship-to party returning POTENTIAL_MATCH from the screening provider stub, when the load is released, then it is held with TMS-0133; after release by a TRADE_COMPLIANCE_RELEASE user with a reason, tendering proceeds and the log shows user and reason.
- **AC-34.17** (FR-34.17, BR-34.18) Given a lane RFQ with 10 lanes and 4 carrier bids, when awarded with at most 2 carriers per lane, then draft RateCards exist per awarded lane starting on the award date and prior overlapping cards end the day before.
- **AC-34.18** (FR-34.18) Given 24 pallets of which 6 are non-stackable for a 53-ft trailer, when planned, then no pallet is stacked on a non-stackable pallet and the plan reports floor positions used.

### 6.34.9 Test plan and fixtures

Fixtures: Solomon instances (C101, R101, RC101, C201, R201), `rating-200.json` (lanes, weights, accessorials, expected totals verified by hand in a spreadsheet committed alongside), `carriers-demo` (5 carriers, 12 services, 3 rate bases), `carrier-invoices-50.json` with 10 variance cases, `voyage-3.json`.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| VRP benchmark | performance/quality | optimizer | feasible, ≤ 110% best-known |
| Rating determinism | unit/property | engine | same input → same output; totals exact |
| Tender cascade | Temporal time-skip | workflow | cascades per BR-34.3 |
| Match tolerance | unit | 50 invoices | expected statuses |
| Accrual tie-out | contract | journal API | zero balance per bill |
| Landed cost sums | property | allocation | Σ shares = cost for random inputs |
| 214 ingest | contract | API | ordering per BR-34.9 |

Reconciliation: Σ accrued freight by open freight bill = journal account 2150 balance; Σ landed cost posted = Σ voyage actual costs; loads shipped in wms = loads DISPATCHED+ in tms.

### 6.34.10 Deployment and operations

Helm `passage-tms` (api, worker), `passage-tms-optimizer` (Python, OR-Tools, 1–4 replicas, 2 CPU/4 GB each), `passage-osrm` (optional self-hosted routing with regional extracts). CloudNativePG `tms-db` (3 instances, 50 GB). Config: `TMS_VRP_TIME_LIMIT_S=60`, `TMS_TENDER_TIMEOUT_MIN=120`, `TMS_MATCH_TOL_ABS=5`, `TMS_MATCH_TOL_PCT=2`, `TMS_DIM_FACTOR_PARCEL=139`. Flags: `tms.enabled`, `tms.landed_cost`, `tms.vrp`. Scheduled: fuel index import weekly (Monday 06:00), tender expiry sweep (Temporal timers), KPI daily. SLOs: rating p95 < 300 ms per shipment per carrier; VRP 500 stops < 60 s; match p95 < 1 s; availability 99.9%. Alerts: tender failures > 3/day; unexpected invoices > 10/day; accrual drift ≠ 0; optimizer queue depth > 20. Runbook: optimizer OOM (reduce batch or raise limits); OSRM down (fallback to stored lane distances with warning). Rollback: `tms.enabled=false` returns to wave-28 carrier labels; open tenders are cancelled with notification.

### 6.34.11 Migration from AX / SAP

| Source | Passage entity | Transformation |
|---|---|---|
| TMSCarrier, TMSCarrierService, TMSCarrierServiceGroup | Carrier, CarrierService | linked to Party by vendor account |
| TMSRateMaster, TMSRateBase, TMSRateBaseAssignment, rate engine metadata | RateCard, RateTable | basis per rate engine type; effective dates from assignment |
| TMSAccessorialMaster/Assignment, TMSFuelIndex/Region | Accessorial, FuelIndex | direct |
| TMSRoutePlan, TMSRouteGuide, TMSRouteSegment | RouteGuide | segments → lanes |
| TMSFreightBill/Detail, TMSCarrierInvoice/Detail (open) | FreightBill, CarrierInvoice | unmatched/unapproved only |
| Landed cost voyages (open) | LandedCostVoyage | estimated costs only |
| /SCMTMS/TOR_ROOT/ITEM/STOP (open freight orders) | Load, Shipment, Route | TOR category FO → Load, FU → Shipment |
| Freight agreements, /SCMTMS/TCM rate tables, calc sheets | RateCard, CalcSheet | scales → weight breaks |
| /SCMTMS/SFIR (open settlements) | FreightBill | status mapping |
| PO delivery cost conditions (FRA1/FRB1) | LandedCostVoyage estimated | per PO line |

Not migrated: historical freight invoices and tracking events (lakehouse); SAP EM event handlers; ISV route optimization data.

### 6.34.12 Dependencies, risks, open questions

Depends on: wave 33 (phase ordering), wave 31 (loads, docks), wave 28 (shipments, carrier labels), wave 29 (customer freight charges), wave 20 (costing adjustments, supplier portal), wave 19 (AP invoices), wave 18 (purchase receipts), wave 12 (FX rate type), wave 11 (journal), wave 7 (carrier parties).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| OR-Tools solution quality on real (non-benchmark) data | Medium | Medium | Planner override; parameter presets; report gap vs heuristic baseline |
| Carrier invoice formats vary (EDI 210 vs PDF) | High | Low | Manual/API entry path; OCR is v1.x |

Open questions: (1) OSRM self-hosted vs stored lane matrix only? Proposed: OSRM optional, lanes default; decide by wave 34 design review. (2) Should customer-billed freight use rated or actual cost? Proposed: rated at ship confirmation; decide by wave 34.

### 6.34.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| tender_timeout_min | carrier / route guide | int | 120 | 5–1,440 | Tender expiry (BR-34.3) | Transportation management parameters › tendering; tender response deadline | SPRO TM › Freight Order Management › Tendering › Define Tendering Profiles |
| broadcast_tendering | route guide | bool | false | true / false | First acceptance wins (FR-34.5) | Load tender broadcast | Tendering process type "broadcast" |
| vrp_time_limit_s | tenant | int | 60 | 5–600 | Optimizer time budget (BR-34.1) | n/a | Planning profile › optimizer settings (runtime) |
| lateness_penalty_per_min | planning profile | money | 50.00 USD | ≥ 0 | Soft time-window penalty (BR-34.1) | n/a | Optimizer cost function settings |
| dim_factor_parcel | carrier service | int | 139 (in³/lb) | 139 / 166 / 5,000 (cm³/kg) | Dimensional weight (BR-34.2) | Rate engine parameters (dimensional weight) | Charge calculation scale base "chargeable weight" |
| match_tolerance_abs / match_tolerance_pct | carrier | money / pct | 5.00 USD / 2 % | ≥ 0 | Freight invoice tolerance (BR-34.4) | Freight reconciliation › Audit masters | Freight settlement invoice verification tolerance |
| fuel_index_source | carrier | enum | DOE_WEEKLY | DOE_WEEKLY / EU_WEEKLY_OIL_BULLETIN / MANUAL | Fuel surcharge index (BR-34.2) | Fuel indexes (TMSFuelIndex) | Charge type FUEL with index table |
| freight_accrual_account | legal entity | account | 2150 | GL accounts | Accrual posting (BR-34.5) | Transportation management parameters › freight reconciliation posting accounts | Settlement accrual account (T030 key FR1) |
| freight_variance_account | legal entity | account | 5250 | GL accounts | Variance posting (BR-34.5) | Freight bill type posting setup | Price difference account (PRD) |
| landed_cost_auto_costs | vendor / item group | template ref | none | auto-cost templates | Estimated costs at goods in transit (FR-34.12) | Landed cost › Auto costs | Pricing procedure with FRA1/FRB1 conditions |
| goods_in_transit_transfer_event | vendor × Incoterm | enum | per BR-34.11 | ORIGIN_SHIPMENT / DESTINATION_RECEIPT | Ownership timing (BR-34.11) | Landed cost parameters › goods-in-transit ownership change | Stock in transit settings (movement types 107/109) |
| in_transit_warehouse | site | warehouse ref | TRANSIT-{site} | in-transit warehouses | Holds goods in transit | Landed cost parameters › goods-in-transit warehouse | SIT plant/storage location |
| container_check_digit_required | tenant | bool | true | true / false | Enforces BR-34.12 | Shipping container validation | Transportation unit identification check |
| detention_free_time_min | carrier | int | 120 | 0–1,440 | Detention start (BR-34.13) | Accessorial (detention) | Charge type detention/demurrage |
| gate_tolerance_min | site | int | 60 | 0–480 | ON_TIME window (BR-34.14) | Appointment scheduling early/late tolerance | SPRO EWM › Yard Management › Define Checkpoints (tolerance) |
| walk_in_allowed | site | bool | true | true / false | Check-in without appointment (FR-34.14) | Driver check-in setup | Check-in without planned TU |
| gate_pii_retention_days | legal entity | int | 90 | 30–3,650 | Driver PII pseudonymization (FR-34.14) | n/a | n/a (ILM policy) |
| dg_regulation_set | mode × country | enum | ADR (EU road), 49CFR (US) | ADR / RID / 49CFR / IMDG / IATA | Rules applied (FR-34.15) | n/a | EHS-DG: specify DG regulations per mode-transport category |
| dg_check_mode | legal entity | enum | BLOCK | BLOCK / WARN | FAIL behavior (FR-34.15) | n/a | DG check schema and check methods (SD/LE/TM) |
| emergency_response_phone | legal entity | text | none (required for DG) | E.164 number | Printed on DG papers | n/a | DG master emergency telephone |
| driver_hours_rule_set | depot | enum | FMCSA_PROPERTY | FMCSA_PROPERTY / EU_561 / NONE | Driver-hours constraint (BR-34.16) | n/a | TM resource driving-time rules |
| screening_provider | legal entity | adapter id | none | registered providers | Denied-party screening (FR-34.19) | n/a | SAP GTS / Watch List Screening configuration |
| screening_cache_h | tenant | int | 24 | 0–168 | Result cache (BR-34.17) | n/a | GTS screening buffer settings |
| customs_filing_provider | legal entity × country | adapter id | none | registered providers | Export filing call (FR-34.19) | n/a | GTS customs management communication |
| rating_cache_min | tenant | int | 10 | 0–60 | Rate shop cache (FR-34.4) | n/a | n/a |

### 6.34.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Loads by status | list inquiry | status, pickup window, carrier, mode | load, weight, stops, tender state | `load` | Transportation management workspace; loads list | Fiori Freight Order Worklist; transportation cockpit |
| Rate shop log | drill-down | shipment, date | carriers, charges, explain trace | rating log | Rating results inquiry; rate route workbench | Carrier selection log |
| Tender history | list inquiry | carrier, outcome, period | sent, response, expiry, rank | `tender` | Tender status inquiry | Tendering overview (TM) |
| Late shipments / tracking timeline | operational report | late > n min, carrier | ETA vs window per stop | `tracking_event` | Load tracking inquiry | Track Freight Orders (event management) |
| Freight reconciliation status | operational report | carrier, match status, age | expected, invoiced, variance, dispute | `freight_bill` | Freight bill invoice reconciliation | Manage Freight Settlement Documents; VI12 |
| Freight accrual tie-out | operational report | legal entity, period | open bills, accrual total, GL 2150 balance, difference | reconciliation | SSRS freight accrual report | VI12 with FBL3N on accrual account |
| Voyage cost and variance | drill-down | voyage, container, cost type | estimated, actual, variance, allocation per receipt line | `landed_cost_voyage` | Voyage cost inquiry; SSRS landed cost reports | MRDC log; MB5S GR/IR balances |
| Goods in transit | list inquiry | voyage, status, vendor | qty and value in transit, ETA | in-transit quants | Goods-in-transit orders | MB5T stock in transit |
| Yard status board | operational report | site | slots, TUs, status, dwell, reefer setpoints | `transport_unit` | Yard management workspace | /SCWM/MON yard nodes (TU, vehicle, yard bins) |
| Gate log | list inquiry | site, date, carrier | check-in/out times, trailer, seal, on-time flag | `gate_event` | Driver check-in log | Check-in/check-out log (/SCWM/CICO) |
| DG load report | operational report | load, mode, date | UN numbers, classes, ADR points, result, rule ids | `dg_check_result` | n/a | DG check log in delivery |
| Carrier KPIs | KPI | carrier, lane, period | cost/cwt, cost/mile, OTD %, tender acceptance %, invoice variance rate | Cube | Transportation KPIs | TM analytics / freight cost KPIs |
| Lane RFQ bid comparison | drill-down | RFQ, lane | bids, scenario totals, award | `freight_rfq` | n/a | Freight RFQ bid analysis |
| Screening holds | list inquiry | status, party, age | load, party, list version, released by | `screening_result` | n/a | GTS blocked documents |

### 6.34.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Bill of lading (VICS straight BOL) | Load confirmed | Shipper, consignee, third-party bill-to, SCAC, PRO, handling units, NMFC class, weight, DG lines (UN, proper shipping name, class, PG, qty, emergency phone), seal | PDF | BOL-* gap-free per site | DG shipping papers: 2 years (49 CFR 172.201(e)) |
| CMR consignment note / e-CMR | EU road load confirmed | CMR boxes 1–24 | PDF; e-CMR (UN/CEFACT) | CMR-* | Per CMR convention and national law (country pack) |
| DG transport document | Load with DG lines | ADR 5.4.1 / 49 CFR 172.202 fields | PDF; on BOL | BOL number | ADR: 3 months minimum (5.4.4) |
| Load tender | Tender sent | Load, stops, windows, equipment, rate, DG flag | EDI 204; email PDF | Tender id | Contract evidence with freight bill |
| Freight bill reconciliation report | Match result | Expected vs invoiced per charge, variance, decision | PDF | FB-* | AP support, 10 years (wave 19) |
| Commercial invoice and customs packing list | Cross-border shipment confirmed | Exporter, consignee, Incoterm, commodity codes, origin, values, weights, export classification | PDF; data to `CustomsFilingProvider` | CI-* | US export records 5 years (15 CFR 762.6); country pack may extend |
| Gate pass / check-in receipt | Check-in | Appointment, door or slot, trailer, instructions | Kiosk print / PDF | GATE-* | Gate log PII 90 days |
| Voyage cost statement | Voyage COSTED | Containers, cost types, allocation per receipt line | PDF / XLSX | Voyage id | Inventory valuation support, 10 years |
| Load plan sheet | Load plan computed | Top view, layers, load sequence, axle estimate | PDF | Load id | None |

### 6.34.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Fuel index import | Weekly, Monday 06:00 | index | index + week | < 1 min | Retry hourly; warn on stale index | Index older than 8 days |
| Tender expiry | Temporal timers | load | workflow id | n/a | Cascade (BR-34.3) | Tender failures > 3/day |
| Carrier invoice auto-match | Every 15 min | carrier | carrier invoice id | 1,000 invoices < 5 min | Unmatched stay in queue | Unexpected invoices > 10/day |
| Freight accrual tie-out | Daily 02:00 | legal entity | date | < 5 min | Report only | Accrual drift ≠ 0 |
| Voyage estimate refresh | Daily 01:00 | voyage | voyage version | < 5 min | Rerun | Voyages without estimates at transfer |
| Detention computation | On check-out + hourly sweep | site | TU visit id | < 1 min | Rerun | none |
| Gate PII pseudonymization | Daily 03:00 | legal entity | visit id | < 2 min | Rerun | Visits past retention not pseudonymized |
| Screening re-check on list update | On provider list-version change | party hash range | (party, list version) | 100,000 parties < 30 min | Resume by range | New BLOCKED on open loads |
| DG reference data update | On new regulation edition (ADR biennial, IMDG amendment) | tenant | edition | < 10 min | Keep previous edition active | Edition not applied by effective date |
| KPI materialization | Daily 04:00 | carrier | day | < 5 min | Rerun day | KPI lag > 24 h |

### 6.34.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| TMS_VIEW | /loads/{id}/tracking; transportation workspace | none | All transportation roles |
| TMS_PLAN | /loads:build, /routes:optimize, /loads/{id}:dg-check, /loads/{id}:screen | TMS_RECONCILE_APPROVE | Transportation planner |
| TMS_RATE | /shipments/{id}:rate | none | Planner, customer service |
| TMS_RATE_APPROVE | Rate card publish; /freight-rfqs/{id}:award | TMS_RECONCILE_APPROVE (sets rates and approves invoices against them) | Transportation manager |
| TMS_TENDER | /loads/{id}:tender; tender board | none | Transportation planner |
| TMS_CARRIER_RESPOND | /tenders/{id}:respond; POD upload | External identities only; never combined with internal duties | Carrier user |
| TMS_RECONCILE | /carrier-invoices; reconciliation grid | none | Freight auditor |
| TMS_RECONCILE_APPROVE | /freight-bills/{id}:approve | AP_PAYMENT_RELEASE (wave 19); TMS_RATE_APPROVE | Freight audit lead |
| TMS_LANDED_COST | /voyages, /goods-in-transit/{id}:ship, /landed-cost/voyages/{id}:allocate | COST_PERIOD_CLOSE (wave 20) | Import coordinator |
| TMS_YARD | /yard-moves; yard board | none | Yard clerk, yard driver |
| TMS_GATE | /gate/check-in, /gate/check-out | none | Gate guard |
| TMS_DG_MAINTAIN | DG reference data; WARN overrides | TMS_PLAN (overriding own load) | Dangerous goods safety adviser |
| TRADE_COMPLIANCE_RELEASE | Release POTENTIAL_MATCH / SCREENING_PENDING holds | SALES_ORDER_MAINTAIN (wave 26) | Trade compliance officer |
| TMS_FLEET_ADMIN | Vehicles, trailers, drivers, calendars | none | Fleet manager |

### 6.34.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| TMS-0101 | error | No valid rate card for {carrier}/{service} on {date}. | NO_VALID_RATE_CARD | Maintain rate card | BR-34.10 |
| TMS-0102 | error | Rate card overlaps {card} ({from}–{to}). | Overlap on save | Adjust validity | BR-34.10 |
| TMS-0103 | warning | Load {load} exceeds {dimension} capacity ({used}/{max}); override logged. | Manual over-capacity | Split load or confirm override | BR-34.8 |
| TMS-0104 | warning | {n} stops dropped as infeasible; see route report. | VRP dropped stops | Adjust windows or add vehicles | BR-34.1 |
| TMS-0105 | error | Tender {t} expired at {time}; acceptance rejected. | TENDER_EXPIRED | Re-tender | BR-34.3 |
| TMS-0106 | error | All carriers declined or timed out for load {load}; planner task created. | TENDER_FAILED | Spot quote or new route guide | BR-34.3 |
| TMS-0107 | warning | Charge {charge} differs by {amount} ({pct} %), outside tolerance. | Invoice variance | Dispute or approve | BR-34.4 |
| TMS-0108 | error | Carrier invoice {no} is already registered for {carrier}. | Duplicate invoice | Reject duplicate | BR-34.4 |
| TMS-0109 | error | Invoice has no freight bill; manual review required. | UNEXPECTED_INVOICE | Link to load or reject | BR-34.4 |
| TMS-0110 | info | Allocation residual {amount} assigned to line {line}. | Rounding residual | None | BR-34.6 |
| TMS-0121 | info | Ownership moved to in-transit warehouse at {event} ({incoterm}). | Goods-in-transit posted | None | BR-34.11 |
| TMS-0124 | error | Container number {no} fails the ISO 6346 check digit. | Invalid container number | Correct the number | BR-34.12 |
| TMS-0125 | error | Yard slot {slot} is occupied by {tu}. | Slot conflict | Choose another slot | BR-34.13 |
| TMS-0126 | info | Detention of {min} min accrued for {tu}. | Dwell beyond free time | None; charge expected | BR-34.13 |
| TMS-0127 | warning | Driver arrived {n} min {direction} for appointment {appt}. | EARLY or LATE | Reschedule door if needed | BR-34.14 |
| TMS-0128 | error | No appointment found; walk-ins are not allowed at {site}. | Unmatched check-in | Book appointment | FR-34.14 |
| TMS-0129 | error | Seal number {declared} does not match {recorded}. | Seal mismatch | Inspect trailer; supervisor decision | BR-34.14 |
| TMS-0130 | error | Dangerous goods check failed: {rule} ({class1} with {class2}). | Prohibited mixed load | Split load | BR-34.15 |
| TMS-0131 | warning | ADR 1.1.3.6 points {p} exceed 1,000; full ADR requirements apply. | Threshold exceeded | Mark vehicle; ADR-trained driver | BR-34.15 |
| TMS-0132 | error | Driver {d} would exceed {rule} ({hours} h). | Hours-of-service limit | Reassign driver or split route | BR-34.16 |
| TMS-0133 | error | Party {party} screening result {status}; load held. | Screening hit or timeout | Trade compliance review | BR-34.17 |
| TMS-0134 | warning | No screening provider configured; export shipment not screened. | NOT_SCREENED | Configure provider (wave 48) | FR-34.19 |
| TMS-0135 | error | Commodity code missing for {item} in {country}. | Customs data incomplete | Maintain commodity code | FR-34.19 |

### 6.34.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Rating | 5,000 shipments/day × 8 carriers; peak 400 rate calls/min | 60 / 300 / 500 ms per carrier; full rate shop p95 < 800 ms (parallel) | 50 rate calls/s | Rating log 90 days ≈ 6 GB | GiST on `rate_card` validity daterange per carrier/service | Cached results served; rate shop narrows to route-guide top 3 |
| VRP optimization | 20 runs/day, ≤ 500 stops | ≤ 60 s hard limit | 4 concurrent runs per 2 optimizer replicas | — | — | Queue (alert at depth 20); first feasible solution returned at 50 % of budget |
| Tracking ingest | 50,000 events/day; peak 2,000/min | 20 / 80 / 200 ms | 100/s | ≈ 10 GB | `tracking_event` monthly partitions; `(load_id, occurred_at)` | Kafka buffering; per-load ordering preserved |
| Freight invoice match | 1,000 invoices/day | 200 ms / 1 s / 2 s | 20/s | Invoices ≈ 1 GB | `freight_bill(match_status)` | Auto-match interval stretches; no auto-approval changes |
| Landed cost allocation | 200 voyages/month × 3,000 receipt lines | < 10 s per voyage | — | Voyages ≈ 1 GB | `voyage(status)`; receipt line FK | Allocations queued per voyage |
| Gate check-in | 300 visits/day per site; peak 40/h | 100 / 300 / 600 ms | 5/s per site | Gate log ≈ 0.2 GB | `gate_event(site_id, occurred_at)` | Kiosk retries; paper fallback entered later with original time |
| DG check | Every load at plan, tender and ship | 20 / 80 / 150 ms | 100/s | Results ≈ 0.5 GB | Reference data in memory per edition | Unchanged (CPU-bound, small) |
| Screening call | 3,000 parties/day at release | Provider budget p95 1.5 s; timeout 5 s | 20/s | Results ≈ 0.3 GB | Cache keyed (party, list version) | Timeout → SCREENING_PENDING hold; never auto-clear |
| Load plan stacking | 200 plans/day | < 5 s for 60 pallets | 2/s per optimizer replica | — | — | Queued behind VRP with lower priority |

### 6.34.20 Localization and compliance hooks

- **Dangerous goods.** US 49 CFR parts 171–180 (PHMSA), EU ADR (road), RID (rail), ADN (inland waterways), IMDG Code (sea), IATA DGR (air), Canada TDG. New editions (ADR biennial, IMDG amendments) arrive as reference data through `DgRegulationPack` (wave 48); BR-34.15 is edition-neutral.
- **Driver hours.** US FMCSA 49 CFR part 395; EU Regulation 561/2006 with tachograph Regulation 165/2014; UK domestic rules. Rule sets plug in through `DriverHoursRuleSet`.
- **Transport documents.** CMR convention and e-CMR protocol for EU road; EU eFTI Regulation 2020/1056 for electronic freight information to authorities; Mexico CFDI with Carta Porte complement for goods in transit (country pack MX, submitted through the wave-35 `MandateProvider`); Brazil CT-e/MDF-e and India e-way bill are country-pack documents on the same hook.
- **Customs, export control and sanctions.** US AES/EEI (15 CFR part 30) and EAR classification (15 CFR part 774); EU Union Customs Code export declarations and ICS2 for imports; UK CDS; denied-party lists such as the US Consolidated Screening List, EU consolidated financial sanctions list and UK OFSI list. Providers plug in through `ComplianceScreeningProvider`, `CustomsFilingProvider` and `PreferenceDeterminationProvider` (wave 48).
- **Landed cost.** Duty rates by commodity code and origin come from country-pack tariff tables; import VAT (including EU postponed accounting) posts through the wave-13 tax engine.
- **Currency and units.** Freight is rated in the carrier's currency and converted at the wave-12 rate type FREIGHT; miles/km, lb/kg and cwt (US 100 lb) vs per 100 kg follow carrier and locale.
- **Privacy and retention.** Driver names and license ids are personal data (GDPR, CCPA): 90-day retention, then pseudonymization; DG shipping papers 2 years (US) / 3 months (ADR); export records 5 years (US).

### 6.34.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX PARTIAL #47 — §12.8.1 Landed cost (voyages, shipping containers, folios, cost estimates, goods in transit; ITMVoyage*) | PARTIAL-34 | ADDED as FR-34.12 | BR-34.11, BR-34.12, AC-34.9, AC-34.10 |
| AX PARTIAL #49 — §12.8.2 Landed cost reports (voyage cost, cost variance by voyage) | PARTIAL-34 | ADDED as FR-34.12 | "Voyage cost and variance" in §6.34.14 |
| AX PARTIAL #51 — §12.8.3 Landed cost → Voyage cost allocation / Goods in transit receipts | PARTIAL-34 | ADDED as FR-34.12 | "Voyage estimate refresh" job; goods-in-transit receipt |
| AX PARTIAL #53 — §12.8.4 Landed cost setup (parameters, cost types, auto cost, container types, voyage templates, legs, tracking) | PARTIAL-34 | ADDED as FR-34.12 | `landed_cost_auto_costs`, `goods_in_transit_transfer_event`, `in_transit_warehouse` |
| AX PARTIAL #223 — §12.27.1 Landed cost (voyages, link) | PARTIAL-34 | ADDED as FR-34.12 | Same mechanism as #47 |
| AX PARTIAL #224 — §12.27.2 SSRS: Landed cost reports (link) | PARTIAL-34 | ADDED as FR-34.12 | "Voyage cost statement" in §6.34.15 |
| AX PARTIAL #225 — §12.27.3 Landed cost → Voyage cost actualization (link) | PARTIAL-34 | ADDED as FR-34.12 | Actuals at invoice with BR-34.6 variance |
| AX PARTIAL #226 — §12.27.4 Landed cost parameters / cost types / voyage templates (link) | PARTIAL-34 | ADDED as FR-34.12 | Same parameters as #53 |
| AX GAP #73 — §12.26.4 Warehouse → Yard management (yard, trailers, yard moves) | GAP (propose 34) | ADDED as FR-34.13 | BR-34.13, AC-34.11 |
| AX GAP #74 — §12.27.1 Driver check-in / check-out | GAP (propose 34) | ADDED as FR-34.14 | BR-34.14, AC-34.12 |
| AX GAP #75 — §12.27.2 Driver check-in log | GAP (propose 34) | ADDED as FR-34.14 | "Gate log" in §6.34.14 |
| AX GAP #76 — §12.27.3 Driver check-in → Expire appointments / Gate log archive | GAP (propose 34) | ADDED as FR-34.14 | "Gate PII pseudonymization" job; appointment expiry via DockAppointment status |
| AX GAP #77 — §12.27.4 Driver check-in setup / Gate configuration | GAP (propose 34) | ADDED as FR-34.14 | `gate_tolerance_min`, `walk_in_allowed`, `gate_pii_retention_days` |
| AX GAP #78 — §12.27.4 Transportation → Yard management setup | GAP (propose 34) | ADDED as FR-34.13 | Yard locations and detention parameters |
| SAP #192 — §13.16 EWM /SCWM/TU, /SCWM/VEH transportation units / vehicles | PARTIAL-34 | ADDED as FR-34.13 | TransportUnit linked to load and yard |
| SAP #195 — §13.16 EWM dangerous goods / hazmat checks in warehouse (EHS) | GAP (propose 34) | ADDED as FR-34.15 | Warehouse storage side ADDED as FR-31.19 |
| SAP #199 — §13.16 Hazardous material: regions, water pollution classes, storage classes, fire-containment sections (MGEF) | GAP (propose 34) | ADDED as FR-31.19 | Master data introduced in wave 31 (first consumer); transport use FR-34.15 |
| SAP #204 — §13.17 Dangerous goods check in delivery (EHS-DG), DG documents | GAP (propose 34) | ADDED as FR-34.15 | DG transport document in §6.34.15 |
| SAP #209 — §13.17 Dangerous goods: DG profiles, DG check methods in SD/LE (DGT*) | GAP (propose 34) | ADDED as FR-34.15 | `dg_regulation_set`, `dg_check_mode` |
| SAP #210 — §13.18 TM transportation units / resources (vehicles, trailers, drivers), calendars | PARTIAL-34 | ADDED as FR-34.16 | Driver app / ELD OUT-OF-SCOPE (carriers' and telematics vendors' systems) |
| SAP #211 — §13.18 Dangerous goods and DG checks in TM | GAP (propose 34) | ADDED as FR-34.15 | Checks at rate, build, tender, ship |
| SAP #213 — §13.18 Strategic freight procurement (lane RFQ, bid analysis) | GAP (propose 34) | ADDED as FR-34.17 | Reuses wave-17 RFQ as the register proposed |
| SAP #214 — §13.18 Load consolidation / container packing optimization (3D load plan) | PARTIAL-34 | ADDED as FR-34.18 | Layer-based stacking; free-form 3D carton packing OUT-OF-SCOPE (low demand, high solver cost) |
| SAP #215 — §13.18 Dangerous goods in TM customizing | GAP (propose 34) | ADDED as FR-34.15 | DG reference data job in §6.34.16 |
| SAP #205 — §13.17 Foreign trade / export documents in delivery (customs, commercial invoice, EUR.1; GTS) — flagged for this wave by coordinator | PARTIAL-13 | ADDED as FR-34.19 | Commercial invoice and customs data added; EUR.1/origin certificates and customs declarations DEFERRED to wave 48 |
| SAP #206 — §13.17 Preference determination (SAP GTS / foreign trade) — flagged | GAP (propose 48) | DEFERRED to wave 48 | `PreferenceDeterminationProvider` hook; origin and commodity data captured by FR-34.19 |
| SAP #208 — §13.17 Foreign trade: export control, preference, Intrastat settings (T604, T605) — flagged | PARTIAL-13 | ADDED as FR-34.19 | Export-control data and provider settings here; Intrastat settings stay wave 13; preference DEFERRED to wave 48 |
| SAP #212 — §13.18 Customs declarations via GTS from TM — flagged | PARTIAL-13 | DEFERRED to wave 48 | `CustomsFilingProvider` call defined in FR-34.19; filing implementations are country-pack content |
| SAP #350 — §13.31 Business Partner Screening / Watch List Screening (GTS) — flagged | GAP (propose 48) | ADDED as FR-34.19 | Screening gate and holds (BR-34.17) added; screening list provider DEFERRED to wave 48 |

## 6.35 Wave 35: B2B Integration: EDI, e-Invoicing & Integration Hub

### 6.35.1 Scope and business context

Wave 35 closes Phase 6 by replacing AX AIF / D365 data entities, Dual-write and Electronic Reporting (ER) for e-invoicing, and SAP IDoc/ALE, PI/PO (Integration Suite) and Document and Reporting Compliance (DRC). It delivers an EDI engine (X12 850/855/856/810/820/214/204/990/997 and EDIFACT ORDERS/ORDRSP/DESADV/INVOIC/CONTRL) with AS2 and SFTP transport, partner profiles and acknowledgments; Peppol BIS Billing 3.0 UBL 2.1 e-invoicing through a certified access point, Factur-X/ZUGFeRD, and a country mandate framework with hooks for IT SDI and MX CFDI; and an integration hub with public REST/GraphQL APIs, webhooks, Kafka topic subscriptions, a Backstage API catalog and developer portal with keys and quotas, plus reference connectors for Salesforce, Shopify and Stripe. Out of scope: full implementation of country e-invoice mandates beyond the framework and hooks (wave 48 country packs), EDIFACT subsets beyond the listed messages, a self-operated Peppol AS4 gateway (v1.x).

**Scope amendment (coverage closure).** The gap registers add four items to this wave: catalog punchout over cXML and SAP OCI that returns requisition lines to wave 17 (FR-35.13), cXML document exchange with cXML networks such as SAP Business Network (Ariba) and Coupa (FR-35.14), a PLM reference connector (FR-35.15), and intake and reconciliation of customer self-billing documents (FR-35.16). §6.35.21 lists the dispositions.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| SAP IDoc failures sit in status 51 (WE02/BD87); a 'MATERIAL not found' error on one E1EDP01 segment requires an ABAP-literate operator to read EDIDD SDATA, fix master data, then reprocess; D365 AIF (AifExceptionLog) and the data entity message processor are similar | Monitor shows raw segment data, no canonical view; reprocess is a separate transaction | Message monitor shows raw, canonical and the failing rule side by side; bulk reprocess with dry run | Mean time to resolve a failed inbound order: 30–90 min → < 5 min; operator needs ABAP/X++: yes → no |
| Outbound documents are produced by batch (SAP output determination NAST + RSNAST00 jobs; D365 batch jobs for outbound ports), so an ASN reaches the customer 15–60 min after shipment and 810s go out in nightly runs | Polling batch framework | Outbound documents generated from `load.shipped` / `invoice.posted` events via the outbox | Event-to-document latency: 15–60 min (batch) → < 60 s; nightly invoice runs → continuous |
| Every EDI partner requires a middleware project (PI/PO ICO + mapping, or an ISV EDI add-on for D365) with Java/graphical mappings nobody in the business can read | Mapping logic lives outside the ERP in a separate tool and skill set | Declarative maps in the product with partner overrides, test harness and simulator | New partner onboarding: 4–8 weeks → 1–3 days; middleware licenses for EDI: 1 → 0 |
| External systems (MES, PLM, BI, agents) integrate through OData with no event feed (D365 business events cover few entities) or through BAPIs/RFCs needing SAP Gateway projects; nobody can see which APIs exist | No catalog, no subscription model, no quotas | Backstage catalog with OpenAPI/AsyncAPI for every entity, webhook/Kafka subscriptions with replay, keys and quotas, MCP (wave 47) over the same APIs | Time for an external team to first successful call: weeks (access request, gateway project) → < 1 h self-service; entities with event feeds: ~10% → 100% |
| ER-based e-invoicing (ERFormatMappingTable) and SAP DRC need a configuration import per country/version and validate only on submission; schematron failures come back from the network hours later | Validation happens at the authority or AP, not at creation | Schematron (CEN + Peppol) runs at validate time inside Passage; arithmetic rules checked first | Rejections from the Peppol network: ~3–5% of invoices → < 0.1%; feedback latency: hours → < 2 s |

### 6.35.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Integration ports / endpoints | AIF: AifPort, AifInboundPort, AifOutboundPort, AifService, AifAction; D365 data entities (DMFEntity), OData | IDoc ports WE21, partner profiles WE20 (EDPP1, EDP12/EDP13/EDP21), distribution model BD64 (TBD05) | edi.TradingPartner, EDIProfile, Transport | full |
| Message log / monitor | AifDocumentLog, AifMessageLog, AifExceptionLog, AifGatewayQueue; D365 message processor | IDoc status EDIDS (WE02/WE05), BD87 reprocess, PI/PO message monitor | Message monitor with replay | improved — replay with state reproduction |
| EDI documents | ISV (partner EDI), data entity import/export | IDoc types ORDERS05, DELVRY03 (DESADV), INVOIC02, control record EDI_DC40 (EDIDC), data EDIDD (EDID4) | Map (canonical ↔ X12/EDIFACT) | full |
| e-Invoicing | Electronic reporting ER configurations (ERFormatMappingTable, ERModelMappingTable), Invoicing service | DRC eDocument framework (EDOCUMENT, EDOCUMENTFILE), Peppol exchange | Peppol UBL 2.1, Factur-X, mandate framework | full (Peppol), partial (country mandates: hooks) |
| CRM/commerce sync | Dual-write (entity maps to Dataverse) | SAP CPI prepackaged content | Reference connectors (Salesforce, Shopify, Stripe) | partial — reference connectors only |
| Business events / webhooks | BusinessEventsCatalog, BusinessEventsEndpoint | Event Mesh / enterprise event enablement | Webhook subscriptions, Kafka subscriptions | full |
| Public API catalog | OData $metadata, Open API via custom service | API Business Hub, Gateway (SEGW) | Backstage catalog, developer portal, OpenAPI/AsyncAPI | improved |
| API keys / quotas | Azure AD app registration only | API management (Integration Suite) | ApiClient, Quota | improved |
| Acknowledgments | n/a | STATUS/ALEAUD IDoc, CONTRL | 997/999, CONTRL, MDN (AS2) | full |
| Catalog punchout | External catalogs with cXML punchout (CatVendorExternalCatalog) | OCI web-based catalogs (SPRO MM › Purchasing › Web-Based Catalogs › Define Catalogs); Ariba catalogs | integration-hub PunchoutCatalog, PunchoutSession → wave-17 requisition lines | full |
| cXML documents | n/a (ISV) | Ariba Network integration via Cloud Integration Gateway (cXML OrderRequest, ConfirmationRequest, ShipNoticeRequest, InvoiceDetailRequest) | cXML maps on the canonical document model | full |
| PLM integration | Engineering change management PLM connectors (Teamcenter, Windchill via Dataverse) | PLM system integration, Engineering Control Center | Reference connector `passage-connector-plm` | partial — reference connector |
| Customer self-billing | n/a (ISV for automotive) | Self-billing procedure VKA/VSB1 (IDoc GSVERF), SPRO SD › EDI › Self-Billing Procedure | SelfBillingDocument with line matching to wave-29 invoices | full |

### 6.35.3 Functional requirements

- **FR-35.1 Trading partner profiles** — Must. The system shall define partners with identifiers (ISA sender/receiver qualifier+id, GS id, EDIFACT UNB id, Peppol participant id `0088:GLN`/`0192:orgnr`), transports (AS2, SFTP, HTTPS), maps per transaction set and direction, envelope options, and SLA (ack within n minutes).
  - Notes: a partner may have map overrides (per-segment rules) layered on the base map; test and production profiles; certificate rotation with overlap period.
- **FR-35.2 X12 and EDIFACT parsing/serialization** — Must. The system shall parse and generate X12 (versions 004010 and 005010) and EDIFACT (D96A, D01B) with configurable delimiters, validate against transaction-set schemas, and generate 997/999 and CONTRL acknowledgments.
  - Notes: control numbers (ISA13, GS06, ST02; UNB, UNH) are gap-free per partner per direction; duplicate inbound ISA13 within 30 days is rejected as a duplicate interchange and reported in the 997.
- **FR-35.3 Mapping engine** — Must. The system shall map between a canonical document model (order, order response, shipment notice, invoice, remittance, status, tender) and EDI segments using declarative maps (JSONata/TypeScript expressions), with lookups (partner item cross-reference, location GLN, UoM, qualifier tables).
  - Notes: base maps per BR-35.1–35.3 ship as fixtures; unit-tested; map versioning with effective dates.
- **FR-35.4 Document integration** — Must. The system shall convert inbound 850/ORDERS into wave-26 sales orders (with partner item cross-reference), outbound 855, 856/DESADV from `wms.load.shipped` with SSCC hierarchy, 810/INVOIC from wave-29 invoices, 820 remittance into wave-30 cash application, 214 into/out of TMS, 204/990 for tenders.
  - Notes: inbound order failures create a task with the raw and canonical payload; outbound documents are created from events via the outbox (no polling).
- **FR-35.5 Transport** — Must. The system shall send and receive via AS2 (RFC 4130: signed, encrypted, synchronous/asynchronous MDN) and SFTP (poll every 60 s, archive after pickup), with retries (5× exponential, max 1 h) and alerts on MDN timeout.
  - Notes: AS2 certificates stored in Vault; MDN required by default; SFTP file naming pattern per partner.
- **FR-35.6 Peppol e-invoicing** — Must. The system shall produce UBL 2.1 invoices and credit notes conforming to Peppol BIS Billing 3.0 (BR-35.4), validate against the CEN EN 16931 and Peppol schematron, and send/receive through a certified access point adapter (AS4 via the AP's API), handling the Peppol MLR/transport acks.
  - Notes: participant lookup via SML/SMP through the AP; invoices received create wave-19 vendor invoices; validation errors block sending and are shown on the invoice.
- **FR-35.7 Factur-X / ZUGFeRD and mandate framework** — Should. The system shall embed EN 16931 CII XML in PDF/A-3 (Factur-X profile EN 16931) and provide a country mandate framework (`MandateProvider` interface: validate, submit, poll status, store clearance id) with reference hooks for IT SDI (FatturaPA) and MX CFDI 4.0 (PAC stamping).
  - Notes: full IT/MX packs ship in wave 48; the framework stores clearance ids and statuses on the invoice.
- **FR-35.8 Public APIs and developer portal** — Must. The system shall publish versioned REST (OpenAPI 3.1) and GraphQL APIs for all entities exposed since wave 3, with API clients (Keycloak client credentials), scopes mapped to duties, per-client quotas (default 600 requests/min, burst 100) and a developer portal at `developers.<env>.passage.dev` generated from the Backstage catalog.
  - Notes: additive-change guarantee per PRD §8; deprecation ≥ 12 months with `Sunset` header; sandbox tenant per client.
- **FR-35.9 Webhooks and Kafka subscriptions** — Must. The system shall let clients subscribe to CloudEvents types by filter and receive them by webhook (HTTPS POST, HMAC-SHA256 signature, at-least-once, 8 retries over 24 h, dead-letter with replay) or by a dedicated Kafka topic with ACLs.
  - Notes: ordering per aggregate id; replay from a timestamp reproduces state when consumed idempotently; payload schema from the schema registry.
- **FR-35.10 Message monitor and replay** — Must. The system shall show every interchange/message with status, acks, errors and payloads; allow reprocessing inbound from raw, resending outbound, and SLA dashboards per partner.
  - Notes: payload retention 13 months (configurable); PII fields masked for non-admin duties.
- **FR-35.11 Reference connectors** — Should. The system shall ship connectors: Salesforce (accounts/opportunities ↔ parties/quotes), Shopify (orders/fulfillments/products), Stripe (payments/payouts into cash application), each built only on the public APIs and webhooks.
  - Notes: connectors are separate Helm charts; mapping configurable; they prove the hub, not full products.
- **FR-35.12 Partner simulator** — Must. The system shall include a partner simulator (AS2/SFTP endpoint plus document generator) used in CI and the demo distributor scenario.
  - Notes: generates 850/856/810 and ORDERS/DESADV/INVOIC with randomized but valid content.
- **FR-35.13 Catalog punchout (cXML and OCI)** — Should. The system shall let a requisitioner open a supplier's external catalog from a wave-17 requisition via cXML PunchOutSetupRequest or SAP OCI 4.0/5.0 (HOOK_URL form post), receive the returned cart (cXML PunchOutOrderMessage or OCI NEW_ITEM-* fields), and create requisition lines with supplier part id, description, price, currency, UoM, UNSPSC code and lead time.
  - Notes: supplier configuration holds URL, identity and shared secret (Vault), extrinsics (user email, cost center) and UoM/category mapping; validation per BR-35.13; session timeout 30 min; item-level (level-2) punchout via PunchOutSetupRequest with `ItemOut`; lines unknown to the item master become non-stock lines with a procurement category; catalog prices are flagged as catalog-sourced for wave-17 approval rules.
- **FR-35.14 cXML document exchange** — Should. The system shall exchange cXML 1.2 documents with cXML networks: outbound OrderRequest from wave-17 POs, inbound ConfirmationRequest (→ PO confirmation), ShipNoticeRequest (→ ASN for FR-31.9) and InvoiceDetailRequest (→ wave-19 vendor invoice), over HTTPS POST with shared-secret or certificate authentication and synchronous cXML Response status codes.
  - Notes: cXML maps sit on the same canonical documents as X12/EDIFACT (FR-35.3), so posting, monitoring and replay are shared; `payloadID` is the idempotency key (BR-35.14); responses 200/201 accepted, 4xx rejected with status text shown in the message monitor.
- **FR-35.15 PLM reference connector** — Could. The system shall ship a reference connector that imports released items, engineering BOMs and engineering change orders from a PLM system (Siemens Teamcenter REST services, PTC Windchill REST/OData services, or a neutral JSON/PLMXML file drop) into wave-9 engineering change requests, and writes ERP release status and effectivity back to the PLM system.
  - Notes: built only on public APIs and webhooks, like FR-35.11 connectors; PLM part number + revision map to item + BOM version; scheduled sync (default 15 min) plus event-driven updates; an ECO that targets a BOM version already released in Passage creates a task instead of overwriting; precedence per BR-35.16.
- **FR-35.16 Customer self-billing** — Could. The system shall receive customer self-billing documents (EDIFACT INVOIC with BGM 389 self-billed invoice, VDA 4908 credit notes, X12 810 flagged as self-billed by partner profile), match them line by line to Passage shipments and invoices (wave 29) by delivery note/ASN number, PO line, item and quantity, post matched documents as the legal invoice, and route differences to a dispute worklist (BR-35.15).
  - Notes: enabled per customer with a self-billing agreement reference and validity dates (EU VAT Directive Art. 224); tolerances per customer (default quantity 0, price 0.01 per unit); the customer's document number is stored as the legal invoice number; wave-29 billing rule `SELF_BILLED` suppresses Passage's own invoice for those shipments; a shipment not self-billed after `self_billing_wait_days` (default 30) creates a task; remittance follows through 820/cash application (wave 30).

### 6.35.4 Data model

**TradingPartner / EDIProfile**

| Field | Type | Required | Notes |
|---|---|---|---|
| partner_id | uuid | yes | PK |
| party_id | uuid | yes | FK Party (wave 7) |
| isa_qualifier, isa_id, gs_id | text | no | X12 identifiers |
| unb_qualifier, unb_id | text | no | EDIFACT |
| peppol_participant_id | text | no | `scheme:id` |
| transports | jsonb | yes | [{type AS2/SFTP/HTTPS, url/host, cert_ref, mdn_mode, poll_interval}] |
| maps | jsonb | yes | [{transaction_set, direction, map_id, version, overrides}] |
| ack_sla_minutes | int | yes | default 60 |
| environment | enum | yes | TEST, PROD |

**Interchange / Message**

| Field | Type | Required | Notes |
|---|---|---|---|
| interchange_id | uuid | yes | PK |
| partner_id, direction | uuid/enum | yes | IN/OUT |
| standard, version | enum/text | yes | X12 004010 / EDIFACT D96A / UBL 2.1 |
| control_number | text | yes | unique per partner/direction/standard |
| raw_payload_ref | text | yes | object storage key (MinIO/S3-compatible, in-cluster) |
| received_at / sent_at | timestamptz | yes | |
| ack_status | enum | yes | PENDING, ACCEPTED, ACCEPTED_WITH_ERRORS, REJECTED, TIMEOUT |
| messages | — | — | Message rows: message_id, transaction_set, st_control, canonical_ref (document id), status (RECEIVED, MAPPED, POSTED, FAILED, SENT, ACKED), error |

**Subscription / DeliveryAttempt**

| Field | Type | Required | Notes |
|---|---|---|---|
| subscription_id | uuid | yes | PK |
| client_id | text | yes | Keycloak client |
| kind | enum | yes | WEBHOOK, KAFKA |
| event_types | text[] | yes | CloudEvents type patterns |
| filter | jsonb | no | CESQL-style expression |
| endpoint_url, secret_ref | text | no | webhook |
| topic | text | no | Kafka `hub.<tenant>.<client>` |
| status | enum | yes | ACTIVE, PAUSED, DEAD_LETTERED |
| attempt: attempt_id, event_id, attempt_no, status_code, latency_ms, next_retry_at | — | — | DeliveryAttempt |

**ApiClient**: client_id, scopes[], quota_per_min, burst, sandbox_tenant_id, owner contact, status. **EInvoice**: einvoice_id, invoice_id (wave 29/19), format (PEPPOL_UBL/FACTURX/CII/MANDATE), xml_ref, validation_report, transmission_id, clearance_id, status (DRAFT/VALIDATED/SENT/ACCEPTED/REJECTED).

Invariants: control numbers are strictly increasing without gaps per partner/direction; a Message in POSTED references exactly one Passage document; every outbound webhook event has ≥ 1 DeliveryAttempt or is DEAD_LETTERED; an EInvoice SENT has a passing validation report. Indexing: `interchange(partner_id, direction, control_number)` unique; `message(status, received_at)`; `delivery_attempt(subscription_id, next_retry_at) WHERE status = 'RETRY'`.

### 6.35.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /edi/v1/partners | Create partner profile | key | EDI_ADMIN |
| POST | /edi/v1/inbound/as2 | AS2 receive endpoint (RFC 4130) | AS2 Message-ID | partner cert |
| POST | /edi/v1/interchanges/{id}:reprocess | Re-run mapping/posting from raw | key | EDI_OPERATE |
| POST | /edi/v1/messages/{id}:resend | Re-transmit outbound | key | EDI_OPERATE |
| POST | /edi/v1/maps:test | Run a map against a sample payload | — | EDI_ADMIN |
| POST | /einvoice/v1/invoices/{id}:validate | Build UBL/CII and run schematron | — | EINV_OPERATE |
| POST | /einvoice/v1/invoices/{id}:send | Transmit via AP or mandate provider | key = invoice id + format | EINV_SEND |
| POST | /hub/v1/clients | Register API client, scopes, quota | key | HUB_ADMIN |
| POST | /hub/v1/subscriptions | Create webhook/Kafka subscription | key | HUB_CLIENT |
| POST | /hub/v1/subscriptions/{id}:replay?from= | Replay events from timestamp | key | HUB_CLIENT |
| GET | /hub/v1/catalog | API/AsyncAPI catalog (Backstage-backed) | — | public (authenticated) |
| POST | /graphql | GraphQL endpoint | per mutation key | scopes |
| POST | /hub/v1/punchout/sessions | Start a punchout session (cXML or OCI) | key | PUR_REQUISITION_CREATE |
| POST | /hub/v1/punchout/return/{session} | Receive cart (PunchOutOrderMessage or OCI form post) | session token | session-bound |
| POST | /edi/v1/inbound/cxml | Receive cXML documents | payloadID | partner shared secret or certificate |
| POST | /edi/v1/self-billing/{id}:match | Re-run self-billing matching | key | SELF_BILLING_RECONCILE |
| POST | /hub/v1/connectors/{id}:sync | Trigger a connector sync (PLM, Salesforce, Shopify, Stripe) | key = connector + change token | CONNECTOR_ADMIN |

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.edi.message.received.v1 | inbound parsed | partner_id, standard, transaction_set, control numbers, canonical_ref | purchasing, sales, AP, tms |
| dev.passage.edi.message.posted.v1 / failed.v1 | posting outcome | message_id, document_ref, error | monitor, tasks |
| dev.passage.edi.message.sent.v1 / acked.v1 | transmit / ack | message_id, partner_id, ack_status | analytics |
| dev.passage.einvoice.status.changed.v1 | validation/transmission | einvoice_id, invoice_id, status, clearance_id | AR/AP, customer portal |
| dev.passage.hub.webhook.delivered.v1 / deadlettered.v1 | delivery | subscription_id, event_id, attempts | analytics, alerting |

### 6.35.6 User experience

| Screen | Primary actions | Key fields | Duty |
|---|---|---|---|
| Partner profiles | Create, transports, certificates, maps, test | identifiers, transport, map versions, SLA | EDI_ADMIN |
| Map designer | Edit expressions, test with samples, diff versions | segment, element, expression, lookup | EDI_ADMIN |
| Message monitor | Filter, view raw/canonical, reprocess, resend | partner, set, control no, status, ack, age | EDI_OPERATE |
| Partner SLA dashboard | Ack latency, failure rate, volume | partner, p95 ack, failures | EDI_OPERATE |
| e-Invoice workspace | Validate, send, view clearance, errors | invoice, format, status, schematron errors | EINV_OPERATE |
| Developer portal (external) | Browse APIs/events, create keys, test console, subscriptions, usage | OpenAPI, AsyncAPI, quotas, attempts | HUB_CLIENT |
| Hub admin | Clients, quotas, subscriptions, dead letters | client, scopes, quota, DLQ count | HUB_ADMIN |

Interaction rules: monitor supports bulk reprocess (≤ 200) with a dry run; raw payloads open in a segment-tree viewer with element hover showing the X12 dictionary name; developer portal "try it" executes against the client's sandbox tenant only.

### 6.35.7 Business rules and edge cases

- **BR-35.1 X12 850 inbound mapping (004010) → canonical Order.**

| Segment/element | Canonical field | Rule |
|---|---|---|
| ISA06/ISA08, GS02/GS03 | partner resolution | must match profile; else reject interchange (TA1/997 R) |
| BEG01 | purpose | 00 original, 01 cancel, 04 change → create/cancel/change order |
| BEG02 | order_type | SA standalone, BK blanket, RL release |
| BEG03 | customer_po | required; duplicate per customer → reject with 997 AK5 R |
| BEG05 | po_date | CCYYMMDD |
| CUR02 | currency | ISO 4217; default partner currency |
| REF*DP / REF*IA | department / vendor id | optional |
| DTM*002 / DTM*010 | requested_delivery / requested_ship | DTM02 CCYYMMDD |
| N1*ST, N3, N4 | ship_to (GLN from N104 qualifier 92/UL) | resolve to party address; unknown → task |
| N1*BT | bill_to | |
| PO101 | line_no | |
| PO102, PO103 | qty, uom (EA, CA, LB…) | UoM via qualifier table |
| PO104, PO105 | unit_price, basis (PE per each, TP per thousand) | price verified against wave-26 pricing; variance tolerance 1% else hold |
| PO106/07 pairs | item cross-reference | BP buyer part → partner item xref; VP vendor part → item; UP/EN → GTIN; first match wins in order BP, VP, UP, EN |
| PID*F | line description | informational |
| SAC (loop) | charges/allowances | C=charge, A=allowance; SAC05 amount N2 (implied 2 decimals) |
| CTT01 | line count | must equal PO1 count else reject |

- **BR-35.2 X12 856 outbound mapping (004010, structure S/O/P/I) from `wms.load.shipped`.**

| Segment/element | Source | Rule |
|---|---|---|
| BSN01/02/03/04/05 | 00, shipment id (load+shipment), ship date, time, 0001 | BSN05 hierarchy S-O-P-I |
| HL*1**S | shipment | |
| TD1 | packaging code (PLT94), lading qty, weight (G, LB/KG) | from load totals |
| TD5 | SCAC (TD503), mode (TD504 M motor) | from tms carrier |
| REF*BM, REF*CN | bill of lading, PRO number | from tms load |
| DTM*011 | ship date | |
| N1*ST / N1*SF | ship-to / ship-from with GLN | qualifier 92 or UL |
| HL**1*O | order | PRF01 customer PO, PRF04 PO date |
| HL**2*P | pack (pallet LP) | MAN*GM SSCC-18 (AI 00 without AI) |
| HL**3*I | item | LIN (VP item, UP GTIN, BP buyer part), SN101 line, SN102 qty shipped, SN103 uom |
| CTT01 | number of HL segments | |

- **BR-35.3 X12 810 outbound mapping (004010) from the wave-29 customer invoice.**

| Segment/element | Source | Rule |
|---|---|---|
| BIG01, BIG02, BIG03, BIG04 | invoice date, invoice no, PO date, customer PO | BIG07 type DI (debit) / CR (credit note) |
| REF*BM, REF*CO | BOL, customer order | optional |
| N1*RE / N1*BT / N1*ST | remit-to / bill-to / ship-to | |
| ITD | terms (ITD01 code, ITD03 discount %, ITD05 discount days, ITD07 net days) | from wave-29 terms |
| DTM*011 | ship date | |
| IT1 | IT101 line, IT102 qty, IT103 uom, IT104 unit price, IT106/07 BP/VP | |
| TXI | tax type (TXI01 ST/VA/GS), amount, rate | per tax line (wave 13) |
| SAC | freight (SAC02 D240), allowances | charge lines |
| TDS01 | total invoice amount, N2 | equals invoice total × 100 |
| CAD | carrier, PRO | optional |
| CTT01 | line count | |

EDIFACT ORDERS/DESADV/INVOIC maps follow the same canonical model (BGM/DTM/NAD/LIN/QTY/PRI/MOA/CPS/PAC/GIN SSCC) and are shipped as fixtures.
- **BR-35.4 Peppol BIS Billing 3.0 required elements (UBL 2.1 Invoice).** `CustomizationID = urn:cen.eu:en16931:2017#compliant#urn:fdc:peppol.eu:2017:poacc:billing:3.0`; `ProfileID = urn:fdc:peppol.eu:2017:poacc:billing:01:1.0`; `ID`; `IssueDate`; `DueDate` or `PaymentTerms`; `InvoiceTypeCode` (380 invoice, 381 credit note as `CreditNote` document); `DocumentCurrencyCode`; `BuyerReference` or `OrderReference/ID` (one required); `AccountingSupplierParty/Party`: `EndpointID @schemeID` (e.g., 0088), `PartyIdentification` (optional), `PartyName`, `PostalAddress/Country/IdentificationCode`, `PartyTaxScheme/CompanyID` with `TaxScheme/ID = VAT` when VAT applies, `PartyLegalEntity/RegistrationName`; `AccountingCustomerParty/Party` (same set, `EndpointID` required); `PaymentMeans/PaymentMeansCode` (30 credit transfer, 58 SEPA) with `PayeeFinancialAccount/ID` (IBAN); `TaxTotal/TaxAmount @currencyID`; `TaxTotal/TaxSubtotal` per category: `TaxableAmount`, `TaxAmount`, `TaxCategory/ID` (S, Z, E, AE, K, G, O), `Percent`, `TaxScheme/ID = VAT`; `LegalMonetaryTotal`: `LineExtensionAmount`, `TaxExclusiveAmount`, `TaxInclusiveAmount`, `PayableAmount` (and `AllowanceTotalAmount`/`ChargeTotalAmount` when present); ≥ 1 `InvoiceLine`: `ID`, `InvoicedQuantity @unitCode` (UN/ECE Rec 20, e.g., C62, KGM), `LineExtensionAmount`, `Item/Name`, `Item/ClassifiedTaxCategory/ID` + `Percent` + `TaxScheme`, `Price/PriceAmount`. Arithmetic rules enforced before schematron: Σ line `LineExtensionAmount` = `LineExtensionAmount`; `TaxExclusiveAmount` = that − allowances + charges; `TaxInclusiveAmount` = exclusive + `TaxAmount`; `PayableAmount` = inclusive − prepaid + rounding; amounts to 2 decimals; VAT breakdown per category/rate equals Σ lines. Validation runs CEN-EN16931-UBL then PEPPOL-EN16931-UBL schematron; any `fatal` blocks sending.
- **BR-35.5 Control numbers and duplicates.** Outbound ISA13/GS06/ST02 per partner from a gap-free number sequence (wave 5); inbound duplicate ISA13 within 30 days → TA1 code 025 / reject; a 997 is generated within 60 s for every inbound functional group; missing partner 997 after `ack_sla_minutes` → alert and resend once.
- **BR-35.6 Idempotent posting.** Inbound 850 posting uses idempotency key `partner:BEG03:BEG01`; a re-sent original (BEG01 00) with the same PO number returns the existing order and is acknowledged accepted; a change (BEG01 04) applies as a wave-26 order change with version.
- **BR-35.7 Webhook delivery.** Retries at 10 s, 1 m, 5 m, 30 m, 2 h, 6 h, 12 h, 24 h; HTTP 2xx = success; 410 Gone pauses the subscription; signature header `Passage-Signature: t=<unix>,v1=<hmac>` over `t.body`; receiver must verify within 5 min of `t`. Events per aggregate are delivered in order; a failing event blocks later events for the same aggregate only (per-aggregate queue).
- **BR-35.8 Quotas.** Token bucket per client (rate/min, burst); exceeding returns 429 with `Retry-After`; quota consumption is counted per request and per 100 GraphQL complexity points.
- **BR-35.9 Replay.** Replay from timestamp T re-delivers events with `ce-passagereplay: true`; consumers must treat them idempotently; replay is capped at 1,000,000 events per request and runs as a Temporal workflow with progress.
- **BR-35.10 e-Invoice status.** `DRAFT → VALIDATED → SENT → ACCEPTED|REJECTED`; REJECTED allows correction and a new transmission with the same invoice id and a new `transmission_id`; an ACCEPTED invoice cannot be re-sent, only credited.
- **BR-35.11 AS2 MDN.** Async MDN awaited 1 h, then resend up to 5×, then FAILED with alert; MDN signature mismatch is FAILED immediately.
- **BR-35.12 PII masking** in the monitor hides N1/N3/N4 address lines and party contact elements for users without `EDI_PII_VIEW`.
- **BR-35.13 Punchout return validation.** Each returned line needs a supplier part id, quantity > 0, unit price ≥ 0 in a currency allowed for the supplier, and a UoM mapped through the UN/ECE Recommendation 20 table; failing lines are dropped with EDI-0133 and the rest are created; the cart total is recomputed as Σ quantity × price and compared with the message total; a difference > 0.01 rejects the whole cart with EDI-0134; a price above catalog price × (1 + `punchout_price_tolerance_pct`) flags the line for approval.
- **BR-35.14 cXML idempotency.** Inbound `payloadID` is the idempotency key per partner; a repeat with identical content returns the original response; a repeat with different content is rejected with EDI-0135 and logged.
- **BR-35.15 Self-billing matching.** Key = (customer, delivery note/ASN number, PO line, item). Quantity difference > tolerance or price difference > tolerance → DIFFERENCE with reason; a self-billed line without a matching shipment → UNMATCHED; matched documents post with the customer's number as the external (legal) invoice number and Passage's internal number as reference; VAT is recomputed by the wave-13 tax engine and a difference > 0.01 is a DIFFERENCE.
- **BR-35.16 PLM import precedence.** An imported ECO with effectivity earlier than today is moved to today with EDI-0139; revisions are ordered by the PLM revision scheme; importing the same part + revision twice is idempotent.

### 6.35.8 Acceptance criteria

- **AC-35.1** (FR-35.2, FR-35.3, FR-35.4, FR-35.12) Given the partner simulator, when it sends 850, 855 is returned, 856 and 810 are produced after shipment and invoicing, and the simulator's 997s are matched, then every supported transaction set round-trips with no manual step and the message monitor shows ACKED for each.
- **AC-35.2** (FR-35.6, BR-35.4) Given 50 demo invoices (standard, reverse-charge, zero-rated, with allowances), when UBL is built, then all pass CEN and Peppol schematron with zero fatal errors and the amounts rules hold.
- **AC-35.3** (FR-35.9, BR-35.7) Given a webhook endpoint that fails 3 times then succeeds, when an event is published, then exactly one successful delivery is recorded after 3 retries and ordering per aggregate is preserved.
- **AC-35.4** (FR-35.9, BR-35.9) Given a consumer that rebuilds a projection from scratch, when events are replayed from the tenant's start, then the projection equals the current Passage state for the sales order aggregate fixture.
- **AC-35.5** (BR-35.5) Given an inbound interchange with an ISA13 already seen 10 days ago, when received, then it is rejected with TA1 025 and no order is created.
- **AC-35.6** (FR-35.8, BR-35.8) Given a client with quota 600/min, when 700 requests are sent in a minute, then ≥ 100 receive 429 with `Retry-After` and the portal usage chart shows the burst.
- **AC-35.7** (BR-35.1) Given an 850 with PO104 differing from the wave-26 price by 3%, when posted, then the sales order is created on price hold with a task.
- **AC-35.8** (BR-35.2) Given a shipped load with 2 pallets × 3 cases for 2 orders, when the 856 is generated, then HL levels are S(1) O(2) P(2) I(n) with MAN*GM SSCCs equal to the wms LP ids and CTT01 equals the HL count.
- **AC-35.9** (phase exit) Given the demo distributor and manufacturer tenants, when the distributor sends an 850, then the manufacturer ships and invoices and the distributor's AP shows a matched vendor invoice from the 810 and the Peppol UBL, both within 15 minutes of each event.
- **AC-35.10** (FR-35.13, BR-35.13) Given a cXML punchout simulator returning 3 items with one unmapped UoM, when the cart returns, then 2 requisition lines are created with the catalog flag and the third is reported with EDI-0133.
- **AC-35.11** (FR-35.13) Given an OCI 5.0 catalog, when the HOOK_URL form post returns NEW_ITEM-DESCRIPTION[1] and [2] with prices and units, then 2 requisition lines carry those values.
- **AC-35.12** (FR-35.14, BR-35.14) Given a PO sent as cXML OrderRequest to the network simulator, when ConfirmationRequest, ShipNoticeRequest and InvoiceDetailRequest return, then the PO is confirmed, an ASN is available for receiving, and a vendor invoice is created and 3-way matched; replaying the invoice payloadID creates nothing new.
- **AC-35.13** (FR-35.16, BR-35.15) Given 10 shipments to a self-billing customer and an INVOIC BGM 389 covering 9 of them with one price difference of 0.05 per unit, when processed, then 8 lines settle, 1 goes to the dispute worklist, and after 30 days the unbilled shipment raises EDI-0138.
- **AC-35.14** (FR-35.15, BR-35.16) Given a PLM simulator releasing part P revision B with a 3-line BOM, when the connector syncs, then a wave-9 ECR with that BOM version exists and the release status is written back; a second sync of the same revision changes nothing.

### 6.35.9 Test plan and fixtures

Fixtures: `x12-samples` (004010 850/855/856/810/820/214/204/990/997, 20 variants each from partner implementation guides), `edifact-samples` (ORDERS/ORDRSP/DESADV/INVOIC D96A), `peppol-invoices-50` (UBL built from wave-29 invoices, including all tax category codes), `factur-x-10.pdf`, `partner-simulator` config for the demo distributor, `webhook-chaos` (failing endpoint profiles), `replay-salesorder-projection`.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Parser/serializer | unit/property | X12/EDIFACT | round-trip byte-equal after normalization |
| Map tests | unit | each map | canonical equals expected JSON |
| Round-trip | E2E (simulator) | every transaction set | ACKED, documents posted |
| Schematron | contract | Peppol UBL | 0 fatal on 50 invoices; known-bad fixtures produce expected rule ids |
| Webhook delivery | property | retries/ordering | at-least-once, ordered per aggregate |
| Replay | E2E | projection rebuild | equals current state |
| Quota | performance (k6) | gateway | 429 behavior, p95 overhead < 5 ms |
| AS2 interop | contract | mendelson/OpenAS2 | MDN exchanged signed+encrypted |

Reconciliation: inbound 850 count = sales orders created + rejected + held; outbound 810 count = invoices posted with EDI partners; Peppol SENT = invoices flagged e-invoice for Peppol customers; webhook events published = delivered + dead-lettered.

### 6.35.10 Deployment and operations

Helm `passage-edi` (api, as2 receiver behind Envoy with client-cert passthrough, sftp poller, mapper workers), `passage-einvoice`, `passage-integration-hub` (gateway policies on Envoy Gateway for keys/quotas, webhook dispatcher, Kafka subscription provisioner using Strimzi `KafkaUser`/`KafkaTopic` CRs), `passage-devportal` (Backstage with the catalog plugin, OpenAPI/AsyncAPI docs rendered from the service registry), `passage-partner-simulator` (non-prod only), connector charts `passage-connector-{salesforce,shopify,stripe}`. CloudNativePG `integration-db` (3 instances, 200 GB; payloads in in-cluster object storage, 13-month retention). Config: `EDI_ACK_SLA_MIN=60`, `EDI_DUP_WINDOW_DAYS=30`, `HUB_DEFAULT_QUOTA=600`, `HUB_WEBHOOK_RETRIES=8`, `EINV_AP_PROVIDER=<adapter>`. Flags: `edi.enabled` per partner, `einvoice.peppol`, `einvoice.facturx`, `hub.graphql`, `hub.replay`. Scheduled: SFTP poll 60 s, ack SLA sweep 5 min, DLQ report daily, certificate expiry check daily (alert 30 days before). SLOs: inbound 850 to sales order p95 < 30 s; outbound document generated within 60 s of the source event; 997 within 60 s; webhook first attempt within 5 s of event p95; gateway overhead p95 < 5 ms; availability 99.9%. Alerts: ack SLA breaches; AS2 MDN failures; DLQ depth > 100; certificate expiring; schematron failure rate > 5%. Runbook: partner certificate rotation; reprocess failed interchanges after a map fix; drain and replay DLQ; rotate client secrets. Rollback: per-partner flag off (queue inbound, do not process); map version pin to the previous version; hub gateway policies are versioned in Git with Argo CD rollback.

### 6.35.11 Migration from AX / SAP

| Source | Passage entity | Transformation |
|---|---|---|
| AifPort/AifInboundPort/AifOutboundPort, AifService/AifAction | TradingPartner transports, map assignments | port → transport; service/action → transaction set/direction |
| AifDocumentLog, AifMessageLog (last 13 months) | Interchange/Message history (read-only) | payloads copied to object storage |
| ERFormatMappingTable/ERModelMappingTable (e-invoice configurations) | EInvoice format config | ER formats are not executable in Passage; only the country/format selection and customer flags migrate |
| Dual-write entity maps | Connector mappings (Salesforce reference) | field map table only |
| BusinessEventsEndpoint/Catalog | Subscription | endpoint type → WEBHOOK |
| EDPP1, EDP12/EDP13/EDP21 (WE20) | TradingPartner, maps | partner type LI/KU → vendor/customer party; message type ORDERS/DESADV/INVOIC → maps |
| EDIPOA/port definitions (WE21), TBD05 (BD64) | Transports, subscriptions | file ports → SFTP; tRFC ports → HTTPS webhook |
| EDIDC/EDID4/EDIDS (last 13 months, statuses 03/53/51) | Interchange/Message history | EDI_DC40 control fields (DOCNUM, IDOCTYP, MESTYP, SNDPRN, RCVPRN, STATUS) → message columns; EDIDD SDATA kept raw |
| EDOCUMENT/EDOCUMENTFILE (DRC) | EInvoice with clearance ids | status mapping; XML kept as reference |
| PI/PO integration directory (ICO) | Partner/transport inventory | exported as a spreadsheet for manual re-creation; routing logic not migrated |

Not migrated: AIF transforms (XSLT/.NET), ABAP user exits in IDoc processing, PI/PO mappings (Java/graphical) — mapping logic is re-authored in Passage maps; IDoc history older than 13 months (lakehouse archive).

### 6.35.12 Dependencies, risks, open questions

Depends on: wave 34 (204/990/214 and freight invoices), wave 31 (SSCC/LPs for 856), wave 30 (cash application for 820), wave 29 (customer invoices), wave 26 (sales order intake and change), wave 19 (vendor invoices), wave 17 (purchase orders for outbound 850), wave 13 (tax categories for UBL), wave 7 (parties, GLNs), wave 5 (gap-free sequences), wave 4 (outbox, schema registry), wave 3 (entity APIs).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Peppol access point certification/onboarding timeline | Medium | Medium | Certified third-party AP adapter; two providers evaluated |
| Partner implementation guides require per-partner map work at every customer | High | Medium | Override layer, simulator, map test harness; estimate 1–3 days per partner |
| Public API surface freezes the data model early | Medium | High | Additive-only versioning; v1 scope limited to entities stable since wave 30 |
| Licensing decision (PRD §12 Q1) affects developer portal terms | Medium | Low | Portal terms page templated; decision required before wave 35 close |

Open questions: (1) Which certified Peppol AP provider for the reference adapter? Proposed: evaluate two, pick by wave 35 start. (2) GraphQL as first-class or REST-only for v1 public APIs? Proposed: both, GraphQL read-only at first; decide by wave 35 design review. (3) Where do connector products live long-term (core repo vs marketplace)? Proposed: separate repos under the hub; decide by wave 46.

### 6.35.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| edi.enabled | trading partner | bool | false | true / false | Processes the partner's traffic (rollback queues inbound) | AIF inbound/outbound port "Enabled" | WE20 partner profile, WE21 port |
| ack_sla_minutes | trading partner | int | 60 | 5–1,440 | 997/CONTRL expectation (BR-35.5) | n/a | ALEAUD/STATUS message settings |
| dup_window_days | tenant | int | 30 | 7–365 | Duplicate interchange window (BR-35.5) | n/a | EDI subsystem duplicate check |
| x12_version | partner × transaction set | enum | 004010 | 004010 / 005010 | Parser/serializer version (FR-35.2) | n/a (ISV) | Message type version in WE20 |
| delimiters | trading partner | jsonb | element `*`, segment `~`, component `>`, repetition `^` | any non-alphanumeric | Envelope syntax | n/a | EDI subsystem settings |
| as2_mdn_mode | trading partner | enum | SYNC | SYNC / ASYNC / NONE | MDN handling (FR-35.5) | n/a | AS2 adapter channel (B2B add-on) |
| as2_mdn_timeout_min | trading partner | int | 60 | 5–1,440 | Resend trigger (BR-35.11) | n/a | AS2 channel timeout |
| sftp_poll_s | trading partner | int | 60 | 15–3,600 | Inbound poll (FR-35.5) | Recurring integration recurrence | File port / file adapter poll interval |
| payload_retention_months | tenant | int | 13 | 3–120 | Payload retention (FR-35.10) | AIF message log cleanup | IDoc archiving (SARA object IDOC) |
| pii_masking | tenant | bool | true | true / false | Masks N1/N3/N4 (BR-35.12) | n/a | n/a |
| peppol_ap_provider | legal entity | adapter id | none | certified AP adapters | Peppol transport (FR-35.6) | Electronic invoicing parameters (Invoicing service) | DRC interface connector (Peppol exchange) |
| einvoice_formats | legal entity × customer | set | {PEPPOL_UBL} | PEPPOL_UBL / FACTURX / CII / MANDATE | Formats generated (FR-35.6, FR-35.7) | Customer e-invoice settings + ER format configuration | DRC eDocument types per country |
| schematron_version | tenant | reference data | current Peppol BIS 3.0 release | versioned | Validation rules (BR-35.4) | ER configuration version | DRC note-based updates |
| hub_default_quota_per_min / hub_burst | API client | int | 600 / 100 | 10–100,000 | Token bucket (BR-35.8) | n/a | API Management rate-limit policy |
| webhook_retries | subscription | int | 8 | 0–12 | Retry schedule length (BR-35.7) | Business events endpoint retries | Event Mesh retry settings |
| replay_max_events | tenant | int | 1,000,000 | 1–10,000,000 | Replay cap (BR-35.9) | n/a | n/a |
| api_deprecation_months | tenant | int | 12 | ≥ 12 | Sunset period (FR-35.8) | n/a | n/a |
| punchout_session_timeout_min | supplier | int | 30 | 5–120 | Cart return window (FR-35.13) | External catalog session | OCI catalog call settings |
| punchout_price_tolerance_pct | supplier | numeric(5,2) | 0 | 0–20 | Approval flag threshold (BR-35.13) | n/a | Requisition price check |
| cxml_auth | trading partner | enum | SHARED_SECRET | SHARED_SECRET / CERTIFICATE | cXML sender credential (FR-35.14) | n/a | Ariba Network / CIG authentication |
| self_billing_enabled | customer | bool | false | true / false | Suppresses own invoice; expects self-billing (FR-35.16) | n/a | SPRO SD › EDI › Self-Billing Procedure › Control Self-Billing Procedure |
| self_billing_tolerance | customer | qty / price | 0 / 0.01 per unit | ≥ 0 | DIFFERENCE threshold (BR-35.15) | n/a | Self-billing tolerance settings |
| self_billing_wait_days | customer | int | 30 | 1–120 | Unbilled shipment task (FR-35.16) | n/a | n/a |
| plm_sync_interval_min | connector | int | 15 | 1–1,440 | Scheduled PLM sync (FR-35.15) | Engineering PLM sync batch recurrence | PLM integration schedule |

### 6.35.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Message monitor | list inquiry / drill-down | partner, transaction set, status, age, control number | raw, canonical, failing rule, acks | `interchange`, `message` | AIF exception log; data management job history | WE02 / WE05 / BD87; SXMB_MONI |
| Partner SLA dashboard | KPI | partner, period | p95 ack latency, failure %, volume | message metrics | n/a | AIF interface monitor (/AIF/IFMON) |
| Control number continuity | operational report | partner, direction | expected vs used, gaps | `interchange` | n/a | IDoc number analysis (WE05) |
| e-Invoice status | list inquiry | legal entity, country, status | invoice, format, transmission id, clearance id, errors | `einvoice` | Electronic invoicing submission log | EDOC_COCKPIT |
| Webhook delivery and dead letters | list inquiry | client, subscription, status | attempts, latency, last status code | `delivery_attempt` | Business events endpoint status | Event Mesh monitor |
| API usage by client | KPI | client, period | requests, 429 count, p95 latency | gateway metrics | n/a | API Management analytics |
| Punchout session log | list inquiry | supplier, user, date | session, lines returned, rejected, total | `punchout_session` | n/a | OCI shopping cart log |
| cXML document log | list inquiry | network, document type, status | payloadID, status code, linked document | `message` | n/a | CIG / Ariba Network message monitor |
| Self-billing reconciliation | operational report | customer, period | matched, DIFFERENCE, UNMATCHED, unbilled shipments | `self_billing_document` | n/a | WE02 filtered on message type GSVERF; self-billing analysis |
| Connector sync log | list inquiry | connector, status | objects synced, errors, change token | `connector_config` runs | Dual-write / PLM sync log | Integration Suite message processing log |

### 6.35.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| X12 / EDIFACT interchanges (850, 855, 856, 810, 820, 214, 204, 990, 997; ORDERS, ORDRSP, DESADV, INVOIC, CONTRL) | Source events (outbox) and inbound receipt | ISA/GS/ST or UNB/UNH envelopes; maps per BR-35.1–BR-35.3 | EDI | ISA13 / GS06 / ST02 gap-free per partner and direction | Payloads 13 months (configurable); invoice interchanges archived as tax records (wave 46) |
| Peppol UBL invoice and credit note | Invoice posted for a Peppol customer | BR-35.4 elements | UBL 2.1 XML via access point | Wave-29 invoice number | Original XML archived: DE 8 years (AO §147 as amended 2025), FR and IT 10 years |
| Factur-X / ZUGFeRD invoice | Invoice posted for a flagged customer | PDF/A-3 with embedded EN 16931 CII XML | PDF | Wave-29 invoice number | As Peppol invoices |
| Mandate submissions (FatturaPA, CFDI 4.0) | Invoice posted in mandate country | Country schema | XML via `MandateProvider` | Clearance id from authority | Country pack (wave 48) |
| AS2 MDN receipts | Every AS2 message | Message-ID, MIC, disposition | S/MIME | none | Kept with the payload as non-repudiation evidence |
| cXML documents | PO, confirmation, ship notice, invoice | cXML 1.2 envelope and payload | XML | payloadID | As EDI payloads |
| Self-billed invoice copy | Self-billing document posted | Customer's invoice number, "self-billing" mention, lines, VAT breakdown | PDF render + original EDI | Customer's number (legal); internal reference | Supplier keeps as its sales invoice: 10 years default |

### 6.35.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| SFTP poll | Every 60 s | trading partner | file name + SHA-256 | < 10 s | Retry 5× exponential (max 1 h) | Poll failures > 3 consecutive |
| Ack SLA sweep | Every 5 min | trading partner | interchange id | < 30 s | Resend once (BR-35.5) | Ack SLA breaches |
| Webhook retry dispatcher | Continuous on `next_retry_at` index | subscription | event id + attempt number | First attempt < 5 s p95 | Dead-letter after 8 attempts | DLQ depth > 100 |
| DLQ daily report | Daily 07:00 | API client | date | < 1 min | Rerun | none |
| Certificate expiry check | Daily 05:00 | partner certificate | certificate id | < 1 min | Rerun | Expiry within 30 days |
| e-Invoice status poll | Every 5 min | provider | transmission id | < 1 min | Back-off on provider errors | Invoices SENT > 24 h without status |
| Payload retention purge | Daily 01:00 | tenant × month partition | partition id | < 10 min | Retry next day | Partitions past retention |
| PLM sync | Every 15 min | connector | PLM change token | < 2 min | Resume from token | Sync failures > 3 consecutive |
| Self-billing unbilled check | Daily 06:00 | customer | shipment id | < 2 min | Rerun | Unbilled shipments past wait days |
| Punchout session cleanup | Hourly | supplier | session id | < 1 min | Rerun | none |

### 6.35.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| EDI_ADMIN | /edi/v1/partners, /edi/v1/maps:test; map designer | EDI_OPERATE in production (change map and reprocess the same traffic) | Integration administrator |
| EDI_OPERATE | /interchanges/{id}:reprocess, /messages/{id}:resend; message monitor | none | Integration operator |
| EDI_PII_VIEW | Unmasked payload view (BR-35.12) | none | Integration lead |
| EINV_OPERATE | /einvoice/v1/invoices/{id}:validate; e-invoice workspace | none | Accounts receivable clerk |
| EINV_SEND | /einvoice/v1/invoices/{id}:send | none | Accounts receivable supervisor |
| HUB_ADMIN | /hub/v1/clients, quotas, dead-letter handling | HUB_CLIENT on the same client (self-granting scopes) | Platform administrator |
| HUB_CLIENT | /hub/v1/subscriptions, /subscriptions/{id}:replay; developer portal | none | External developer |
| PUR_REQUISITION_CREATE (wave 17) | /hub/v1/punchout/sessions | PUR_PO_APPROVE (wave 17) | Requisitioner |
| SELF_BILLING_RECONCILE | /edi/v1/self-billing/{id}:match; dispute worklist | AR_CREDIT_NOTE_APPROVE (wave 29) | Accounts receivable specialist |
| CONNECTOR_ADMIN | /hub/v1/connectors/{id}:sync; connector configuration | none | Integration administrator |

### 6.35.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| EDI-0101 | error | Interchange sender {isa06} is not configured for any partner. | Unknown sender | Create or correct the partner profile | BR-35.1 |
| EDI-0102 | error | Duplicate interchange control number {isa13} (seen {date}); TA1 025 sent. | Duplicate ISA13 within window | Partner resends with a new number | BR-35.5 |
| EDI-0103 | error | CTT01 {n} does not match PO1 count {m}. | Truncated or malformed 850 | Partner resends | BR-35.1 |
| EDI-0104 | warning | Price {p} differs from list {l} by {pct} %; order on price hold. | Variance > 1 % | Sales reviews the hold | BR-35.1 |
| EDI-0105 | error | No item cross-reference for {qualifier} {value}. | Unknown buyer/vendor part or GTIN | Add cross-reference; reprocess | BR-35.1 |
| EDI-0106 | error | Ship-to GLN {gln} not found. | Unknown location | Add GLN to party address; reprocess | BR-35.1 |
| EDI-0107 | warning | No 997 from {partner} after {n} min; resent once. | Ack SLA breach | Contact partner | BR-35.5 |
| EDI-0108 | error | AS2 MDN signature mismatch for message {id}. | Certificate or tampering | Verify partner certificate | BR-35.11 |
| EDI-0109 | error | Control number gap for {partner}: expected {e}, generated {g}. | Sequence defect | Incident; do not resend until fixed | §6.35.4 invariants |
| EINV-0110 | error | Schematron rule {rule} failed: {text}. | Invalid UBL/CII | Correct invoice data | BR-35.4 |
| EINV-0111 | error | Arithmetic check failed: TaxInclusiveAmount ≠ TaxExclusiveAmount + TaxAmount. | Rounding or line errors | Correct invoice | BR-35.4 |
| EINV-0112 | error | Invoice {no} is already accepted; issue a credit note. | Resend of ACCEPTED invoice | Credit and reissue | BR-35.10 |
| HUB-0113 | error | Quota exceeded; retry after {s} s. | Token bucket empty (429) | Back off | BR-35.8 |
| HUB-0114 | warning | Subscription {id} paused: endpoint returned 410 Gone. | Receiver gone | Update endpoint and resume | BR-35.7 |
| HUB-0115 | error | Event {event} dead-lettered after 8 attempts. | Receiver failing | Fix receiver; replay from DLQ | BR-35.7 |
| HUB-0116 | info | Replay {id} delivered {n} of {total} events. | Replay progress | None | BR-35.9 |
| EDI-0133 | warning | Punchout line {n} dropped: {reason}. | Missing part id, bad UoM or currency | Correct supplier mapping | BR-35.13 |
| EDI-0134 | error | Punchout cart total {a} differs from line sum {b}; cart rejected. | Inconsistent cart | Repeat punchout; contact supplier | BR-35.13 |
| EDI-0135 | error | cXML payloadID {id} reused with different content. | Partner defect or replay attack | Partner sends a new payloadID | BR-35.14 |
| EDI-0136 | warning | Self-billed line {line} differs: {field} {theirs} vs {ours}. | DIFFERENCE | Dispute or accept | BR-35.15 |
| EDI-0137 | error | Self-billed line {line} has no matching shipment. | UNMATCHED | Investigate with customer | BR-35.15 |
| EDI-0138 | warning | Shipment {s} not self-billed after {n} days. | Customer has not billed | Follow up with customer | FR-35.16 |
| EDI-0139 | warning | PLM ECO {eco} effectivity moved to today. | Backdated effectivity | Review with engineering | BR-35.16 |

### 6.35.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Inbound 850 → sales order | 5,000 orders/day; peak 300/min (Monday batches) | 2 / 30 / 60 s end to end | 10 interchanges/s | Payloads ≈ 50 GB (13-month rolling) | `interchange(partner_id, direction, control_number)` unique; `message` monthly partitions | Kafka backlog; SLA stretches to ≤ 2 min; no loss |
| Outbound document generation | 20,000 documents/day | 5 / 30 / 60 s from source event | 20/s | Included above | Outbox per source service | Backlog alert; order of documents per partner preserved |
| 997 / CONTRL generation | One per inbound functional group | < 60 s | 20/s | — | — | Acks queued with priority over outbound documents |
| Peppol validation | 10,000 invoices/day | 300 ms / 1.5 s / 2 s | 20/s | UBL ≈ 73 GB at 20 KB each | `einvoice(status, created_at)` | Schematron workers scale; sending delayed, never skipped |
| Webhook first attempt | 2 M events/day; peak 5,000/s | 1 / 5 / 10 s | 5,000/s | Attempts 30 days ≈ 100 GB | `delivery_attempt` daily partitions | Per-aggregate queues; slow endpoints isolated per subscription |
| Gateway overhead | 20 M requests/day; peak 2,000 req/s | 1 / 5 / 10 ms | 2,000 req/s | Metrics only | — | Quotas return 429; lowest-tier clients shed first |
| Replay | 1 M events per request | < 10 min | 2,000 events/s | — | Kafka offsets by timestamp | Throttled to protect live delivery |
| Punchout return | 500 sessions/day | 300 ms / 1 s / 2 s | 5/s | Sessions ≈ 0.2 GB | `punchout_session(supplier_id, created_at)` | Return queued; session timeout still applies |
| Self-billing match | 2,000 lines/day | < 1 s per 200-line document | 10 documents/s | ≈ 0.5 GB | (customer, delivery note) index | Matching queued per customer |

### 6.35.20 Localization and compliance hooks

- **e-Invoicing mandates.** Belgium: B2B Peppol mandatory from 2026-01-01. Germany: receipt of EN 16931 e-invoices required since 2025-01-01, issuance from 2027-01-01 (turnover > EUR 800,000) and 2028-01-01 for all, as XRechnung (UBL/CII) or ZUGFeRD ≥ 2.0.1 with EN 16931 profile. France: receipt from 2026-09-01 through certified platforms (PDP), issuance for large and mid-size companies from 2026-09-01 and SMEs from 2027-09-01. Poland KSeF: 2026-02-01 for large taxpayers, 2026-04-01 for others. Italy SDI (FatturaPA) and Mexico CFDI 4.0 (PAC stamping) are clearance models. EU ViDA (Directive (EU) 2025/516) introduces digital reporting for intra-EU B2B from 2030. Each lands as a country pack (wave 48) implementing `MandateProvider`; FR-35.7 stores clearance ids and statuses.
- **Self-billing.** EU VAT Directive 2006/112/EC Art. 224 requires a prior agreement and an acceptance procedure, and Art. 226(18) requires the mention "Self-billing"; Germany UStG §14(2) "Gutschrift"; automotive formats VDA 4908 and Odette INVOIC. Country packs validate the mention and agreement fields through `SelfBillingPolicy`.
- **Retention.** e-Invoices are archived in their original XML format through wave 46 (WORM storage): Germany 8 years (AO §147, amended 2025), France and Italy 10 years (Italy with conservazione sostitutiva).
- **Privacy.** Payloads contain personal data (names, addresses, contacts): masking (BR-35.12), 13-month payload retention, and GDPR erasure requests are refused for documents under legal retention (Art. 17(3)(b)); API clients outside the EEA require a transfer assessment recorded on the ApiClient.
- **Character sets and time.** X12 basic/extended character sets; EDIFACT UNOA/UNOB/UNOC (ISO 8859-1) and UNOW (UTF-8); UBL and cXML UTF-8; ISA09/ISA10 in sender local time; UBL IssueDate in the supplier's legal date.
- **Punchout.** Currency and UoM per supplier; taxes on punchout lines are determined later by the wave-13 tax engine, never taken from the cart.

### 6.35.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX GAP #53 — §12.16.1 Procurement: External catalogs (punchout cXML/OCI) | GAP (propose 35) | ADDED as FR-35.13 | Lines return to the wave-17 requisition; BR-35.13, AC-35.10, AC-35.11 |
| AX PARTIAL #285 — §12.34.1 Engineering → PLM integration (Teamcenter/Windchill connectors) | PARTIAL-35 | ADDED as FR-35.15 | Reference connector on public APIs; AC-35.14 |
| AX PARTIAL #291 — §12.34.3 Engineering → Sync with PLM (batch) | PARTIAL-35 | ADDED as FR-35.15 | "PLM sync" job in §6.35.16 |
| AX PARTIAL #298 — §12.34.4 Engineering → PLM connector configuration | PARTIAL-35 | ADDED as FR-35.15 | `plm_sync_interval_min`; ConnectorConfig |
| SAP #139 — §13.12 MM-PUR Ariba Network integration (cXML, Cloud Integration Gateway) | PARTIAL-35 | ADDED as FR-35.14 | cXML catalogs also covered by FR-35.13; BR-35.14, AC-35.12 |
| SAP #129 — §13.12 Fiori Create Purchase Requisition with catalogs (OCI punch-out) — flagged for this wave | PARTIAL-17 | ADDED as FR-35.13 | Requisition UI stays wave 17; punchout transport and cart intake here |
| SAP #220 — §13.19 VKA / VSB1 self-billing procedure (customer self-billing) — flagged | GAP (propose 29) | ADDED as FR-35.16 | Adds wave-29 billing rule `SELF_BILLED`; BR-35.15, AC-35.13 |
