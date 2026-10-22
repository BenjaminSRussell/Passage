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

**Cross-wave event catalog**

| Event (CloudEvents type) | Producer | Consumers | Payload summary |
|---|---|---|---|
| com.passage.wms.work.created.v1 | wms | wms-mobile, analytics | work_id, work_type, warehouse, priority, lines[] |
| com.passage.wms.work.completed.v1 | wms | inventory, sales-fulfillment, production | work_id, lines with from/to location, LP, qty |
| com.passage.wms.load.shipped.v1 | wms | tms, sales-fulfillment, edi (856) | load_id, shipments[], LPs/SSCCs, carrier, ship_date |
| com.passage.wms.receipt.registered.v1 | wms | purchasing (wave 18), quality | receipt_id, po lines, LPs, qty |
| com.passage.quality.order.created.v1 | quality | wms (block quant), analytics | quality_order_id, trigger, item, quant_ref, sample_size |
| com.passage.quality.usagedecision.posted.v1 | quality | wms, inventory, purchasing, production | quality_order_id, decision, accept_qty, reject_qty |
| com.passage.quality.nonconformance.created.v1 | quality | workflow, vendor-scorecard | ncr_id, source, severity, item, vendor |
| com.passage.tms.shipment.rated.v1 | tms | sales (freight charge), analytics | shipment_id, carrier, service, charges[], total |
| com.passage.tms.tender.accepted.v1 | tms | wms (dock), edi (204/990) | load_id, carrier, pickup_window |
| com.passage.tms.freightbill.matched.v1 | tms | accounts-payable (wave 19), costing (wave 20) | freight_bill_id, invoice_id, variance, landed_cost_lines[] |
| com.passage.edi.message.received.v1 | edi | purchasing, sales, ap | partner, standard, transaction_set, control numbers, document ref |
| com.passage.edi.message.sent.v1 | edi | analytics, integration-hub | partner, transaction_set, ack status |
| com.passage.hub.webhook.delivered.v1 | integration-hub | analytics | subscription_id, event_id, attempt, status |

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

Commands (CQRS): `CreateWorkFromOrderLines` validates all lines belong to one warehouse and are released; `ConfirmWorkLine` validates qty ≤ open qty + over-pick tolerance, LP exists or is new at a PUT, location status allows direction, capacity; `ReleaseWave` validates template limits before starting the workflow.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| com.passage.wms.work.created.v1 | Work header committed | work_id, work_type, warehouse_id, priority, wave_id, lines[] | wms-mobile, analytics |
| com.passage.wms.work.completed.v1 | last line confirmed | work_id, lines[{item, dims, qty, from, to, lp}], user, duration_s | inventory (post transfer), sales-fulfillment, production |
| com.passage.wms.wave.released.v1 | workflow finished | wave_id, orders, work_count, replenishment_count, failures[] | analytics, sales-fulfillment |
| com.passage.wms.load.shipped.v1 | load confirmed | load_id, shipment_ids, lp_ids, carrier_id, ship_datetime | tms, edi, sales-fulfillment |
| com.passage.wms.receipt.registered.v1 | receipt committed | receipt_id, po_line_refs, lp_ids, qty by item | purchasing, quality |

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

## 6.32 Wave 32: Mobile Warehouse Execution (Scanners, RF, Voice)

### 6.32.1 Scope and business context

Wave 31 creates work; wave 32 lets warehouse workers execute it on handheld scanners. This replaces the D365 Warehouse Management mobile app (mobile device menus, menu items and steps) and the SAP EWM RF framework (/SCWM/RFUI, ITSmobile). After this wave a worker signs in on a Zebra or Honeywell Android device (or any PWA-capable browser), picks from a configured menu, scans GS1-128 labels that the system parses into LP/GTIN/lot/expiry/serial/weight, and completes receive, put-away, pick, pack, move, count and inquiry flows with offline tolerance. An administrator changes flows without an app release. Label printing (ZPL) ships through a print service. Out of scope: full voice-directed picking (adapter interface only; a voice vendor integration is v1.x), RFID portal reads beyond the EPC ingest endpoint, iOS native shell (PWA covers iOS).

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

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| com.passage.mobile.flow.published.v1 | publish | flow_id, code, version, warehouses | devices (push via SSE), analytics |
| com.passage.mobile.session.started.v1 / ended.v1 | session lifecycle | session_id, device_id, user_id, warehouse | analytics |
| com.passage.mobile.offline.conflict.v1 | replay conflict | idempotency_key, command, reason | supervisor queue |
| com.passage.print.job.completed.v1 | printer ack | job_id, printer, status | wms |

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

### 6.32.8 Acceptance criteria

- **AC-32.1** (FR-32.3, BR-32.1) Given the GS1 AI test suite vectors (fixed and variable, with and without symbology identifiers), when parsed, then 100% match expected AI maps and check-digit failures are reported.
- **AC-32.2** (FR-32.4) Given the emulated-device Playwright suite, when a receive → put-away → pick → pack → ship flow is run with scanned GS1-128 labels from the ASN fixture, then the load ships and the inventory ledger reflects each move.
- **AC-32.3** (FR-32.5, BR-32.3) Given a device offline with 200 queued confirmations, when it reconnects, then all 200 are APPLIED exactly once, in order, and replaying the same batch again changes nothing.
- **AC-32.4** (FR-32.2) Given a published flow change adding a lot-confirmation step, when a device loads the menu, then the new version runs within 60 s with no app update.
- **AC-32.5** (FR-32.7) Given an LP label template, when a receive step completes, then a ZPL job with `^BC` SSCC is sent to the step's printer and status PRINTED is recorded.
- **AC-32.6** (BR-32.4) Given a queued confirmation for a line cancelled meanwhile, when replayed, then it is CONFLICT and appears in the supervisor queue.
- **AC-32.7** (FR-32.10) Given a Zebra DataWedge intent with `]C1` prefix, when received on a non-scan step, then it is ignored and logged.
- **AC-32.8** (FR-32.6, BR-32.6) Given a session with no heartbeat for 31 min, when another user requests work, then the stale session's work is assignable.

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

## 6.33 Wave 33: Quality Management & Nonconformance

### 6.33.1 Scope and business context

Wave 33 replaces AX quality management (test groups, quality associations, quality orders, nonconformances, corrections) and SAP QM (inspection plans, inspection lots, results recording, usage decisions, quality notifications, vendor evaluation). After this wave, quality orders are generated by association rules at purchase receipt, production report-as-finished and before shipment; samples are sized by AQL plans; results are recorded (variable and attribute); a usage decision releases or rejects inventory that was blocked from the moment the order was created; nonconformances drive corrections/CAPA through a Temporal 8D workflow; certificates of analysis print on shipment; supplier scorecards and SPC charts are computed from results; instruments have calibration schedules. Out of scope: stability studies, audit management (ISO 19011), FMEA, and the SAP QM-PM calibration order integration (Passage tracks calibration due dates and blocks results from overdue instruments).

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

Commands: `CreateQualityOrderFromTrigger` validates association match and that the source line is in the required state; `PostUsageDecision` validates all required tests have results (or `allow_incomplete` with deviation reason) and quantity split sums to lot qty.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| com.passage.quality.order.created.v1 | order created | quality_order_id, trigger, source_ref, item, dims, lot_qty, sample_qty | inventory (hold), wms, analytics |
| com.passage.quality.result.recorded.v1 | result batch | quality_order_id, results[] | SPC projection |
| com.passage.quality.usagedecision.posted.v1 | decision | quality_order_id, decision, accept_qty, reject_qty, deviation_qty, user | inventory (release/hold), wms, purchasing, production |
| com.passage.quality.nonconformance.created.v1 | NCR | ncr_id, source, severity, item, vendor_id | vendor scorecard, workflow |
| com.passage.quality.capa.closed.v1 | 8D closed | ncr_id, effectiveness_result | analytics |

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

## 6.34 Wave 34: Transportation Management & Freight

### 6.34.1 Scope and business context

Wave 28 integrated parcel carriers for labels; wave 34 adds full transportation management replacing D365 TMS and SAP TM: shipments consolidated into loads and routes, rated against carrier rate cards with accessorials and fuel surcharges, rate-shopped, tendered to carriers with timeouts, tracked by EDI 214 or carrier APIs, and reconciled against carrier freight invoices with variance disputes and accruals. Freight and duties are allocated to inventory as landed cost feeding wave-20 costing. VRP route optimization runs on OR-Tools in the Python `tms-optimizer`. Out of scope: own-fleet driver apps and ELD, ocean booking, customs filing (hooks only), multi-leg international routing guides beyond two legs.

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

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| com.passage.tms.shipment.rated.v1 | rating | shipment_id, options[{carrier, service, total, transit_days}], chosen | sales (freight charge), analytics |
| com.passage.tms.tender.accepted.v1 / declined.v1 / expired.v1 | tender response | load_id, carrier_id, rank, rate | wms, edi (204/990), analytics |
| com.passage.tms.tracking.event.v1 | ingest | load_id, stop_no, code, occurred_at | sales-fulfillment, customer portal |
| com.passage.tms.freightbill.matched.v1 | match | freight_bill_id, carrier_invoice_id, variance, lines[] | AP, costing |
| com.passage.tms.landedcost.posted.v1 | allocation posted | voyage_id, receipt_lines[{line, amount}] | costing (wave 20) |

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

### 6.34.8 Acceptance criteria

- **AC-34.1** (FR-34.2, BR-34.1) Given Solomon C101, R101 and RC101 (100 customers), when optimized with a 60 s limit, then every route satisfies capacity and time windows and total distance is within 10% of best-known.
- **AC-34.2** (FR-34.3, BR-34.2) Given the 200-shipment rating fixture, when rated, then totals equal the expected charges to the cent and the explain trace lists weight-break, minimum, accessorial and fuel steps.
- **AC-34.3** (FR-34.5, BR-34.3) Given a route guide with 3 carriers and 10-minute timeouts (test clock), when carrier 1 ignores and carrier 2 declines, then carrier 3 receives the tender and, on acceptance, the load is ACCEPTED with carrier 3.
- **AC-34.4** (FR-34.7, BR-34.4) Given a freight bill of $412.50 and a carrier invoice of $418.00, when matched with tolerance $5/2%, then the bill is VARIANCE with $5.50 and no AP invoice exists until approval.
- **AC-34.5** (FR-34.8, BR-34.5) Given a load shipped and later matched, when postings are inspected, then accrual, reversal and AP invoice tie to zero on account 2150 for that freight bill.
- **AC-34.6** (FR-34.9, BR-34.6) Given a voyage with freight $1,000.00 allocated by weight over 3 receipt lines (weights 1, 1, 1 kg), when allocated, then shares are 333.33 / 333.33 / 333.34 and Σ = 1,000.00.
- **AC-34.7** (FR-34.6) Given a 214 with status D1, when ingested, then the stop is DELIVERED and the sales shipment shows delivered timestamp.
- **AC-34.8** (FR-34.1, BR-34.8) Given 30 shipments totaling 48,000 kg for a 20,000 kg TL rule, when loads are built, then 3 loads exist, none over capacity.

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

## 6.35 Wave 35: B2B Integration: EDI, e-Invoicing & Integration Hub

### 6.35.1 Scope and business context

Wave 35 closes Phase 6 by replacing AX AIF / D365 data entities, Dual-write and Electronic Reporting (ER) for e-invoicing, and SAP IDoc/ALE, PI/PO (Integration Suite) and Document and Reporting Compliance (DRC). It delivers an EDI engine (X12 850/855/856/810/820/214/204/990/997 and EDIFACT ORDERS/ORDRSP/DESADV/INVOIC/CONTRL) with AS2 and SFTP transport, partner profiles and acknowledgments; Peppol BIS Billing 3.0 UBL 2.1 e-invoicing through a certified access point, Factur-X/ZUGFeRD, and a country mandate framework with hooks for IT SDI and MX CFDI; and an integration hub with public REST/GraphQL APIs, webhooks, Kafka topic subscriptions, a Backstage API catalog and developer portal with keys and quotas, plus reference connectors for Salesforce, Shopify and Stripe. Out of scope: full implementation of country e-invoice mandates beyond the framework and hooks (wave 48 country packs), EDIFACT subsets beyond the listed messages, a self-operated Peppol AS4 gateway (v1.x).

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

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| com.passage.edi.message.received.v1 | inbound parsed | partner_id, standard, transaction_set, control numbers, canonical_ref | purchasing, sales, AP, tms |
| com.passage.edi.message.posted.v1 / failed.v1 | posting outcome | message_id, document_ref, error | monitor, tasks |
| com.passage.edi.message.sent.v1 / acked.v1 | transmit / ack | message_id, partner_id, ack_status | analytics |
| com.passage.einvoice.status.changed.v1 | validation/transmission | einvoice_id, invoice_id, status, clearance_id | AR/AP, customer portal |
| com.passage.hub.webhook.delivered.v1 / deadlettered.v1 | delivery | subscription_id, event_id, attempts | analytics, alerting |

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
