# Part 4 — Phase 4: Manufacturing & Planning (waves 21–25)

## 4.0 Phase overview

**Business outcome at phase exit.** A discrete or process manufacturer can run its plant on Passage: master planning nets demand against supply across a multi-level BOM and generates planned purchase, production and transfer orders; planners firm those into production or batch orders, schedule them against finite resource capacity on a Gantt, release them to the floor, and operators execute them from a kiosk while machines report counts over OPC-UA/MQTT. Every material issue, route consumption and report-as-finished posts WIP through the universal journal (wave 11) and the inventory ledger (wave 16); order end settles actual against planned cost and posts variances. Lean plants run kanban replenishment and production flows with periodic backflush costing. The phase-exit demo (wave 25 proof) manufactures a configured product from planned order to finished goods with full costing, and the WIP, inventory and variance accounts reconcile to the journal.

**Phase-exit sign-off criteria**
- 100% of production order status transitions in the wave-21 state machine are covered by a property test; every illegal transition is rejected and every legal one posts the fixture-defined inventory and GL entries.
- WIP control account balance = Σ(open production orders' posted consumption − posted RAF value) to the cent, verified by the nightly subledger reconciliation job for 30 consecutive days in `stage`.
- The 40 APICS-style MRP fixtures (wave 22) pass; a 50k-item × 12-period × 5-level plan completes in < 10 min on the 3-node reference cluster and produces byte-identical planned orders on re-run.
- Scheduler produces feasible, non-overlapping schedules on all 30 OR-Library job-shop instances; 5k operations scheduled in < 60 s; CP-SAT mode within 5% of known optimum on instances ≤ 10×10.
- Process fixtures: potency consumption, co/by-product allocation (sum equals batch cost exactly), catch-weight dual quantities, and the event-kanban simulation pass.
- Kiosk E2E: an 8-hour shift of 6 operators clocks jobs offline for 30 min, syncs without duplicates, and the production order's labor cost equals Σ(hours × pay rate) from the approved registrations.
- IoT: a simulated machine publishes 10,000 events/min for 60 min with a broker restart at minute 30; zero events lost (sequence check); OEE tile lag p95 < 5 s.
- A practicing cost accountant signs off the WIP/variance fixture set (50 orders) against a reference workbook.

**Cross-wave data model summary**

| Entity | Owning service (Postgres DB) | Key relationships |
|---|---|---|
| ProductionOrder | `production` (production_db) | → ReleasedProduct (w8), BomVersion/RouteVersion (w9), Site/Warehouse (w6), PlannedOrder (w22), SalesOrderLine (w27, pegged) |
| ProductionBomLine, ProductionOperation, ProductionJob | `production` | child of ProductionOrder; ProductionJob → Resource (w9) |
| ProductionJournal (+ lines) | `production` | → ProductionOrder; posts to InventoryTransaction (w16) and JournalEntry (w11) |
| CostEstimate, WipPosting, Variance | `production` | → ProductionOrder; → JournalEntry |
| BatchOrder, Formula, CoProductLine, BatchAttribute, CatchWeightQty | `production` (process extension) | BatchOrder is a ProductionOrder subtype; → InventoryBatch (w16) |
| PlanVersion, Requirement, PlannedOrder, Pegging, ActionMessage, CoverageGroup, ForecastLine | `planning` (planning_db) | Requirement ↔ Pegging ↔ Requirement; PlannedOrder → ProductionOrder/PurchaseOrder/TransferOrder on firming |
| ResourceCapacity, ScheduledJob, ScheduleRun | `scheduling` (scheduling_db) | ScheduledJob → ProductionJob; ResourceCapacity → Resource + WorkCalendar (w9) |
| ValueStream, ProductionFlow, FlowActivity, KanbanRule, Kanban, KanbanCard | `lean` (lean_db) | Kanban → ProductionOrder (manufacturing kanban) or TransferOrder (withdrawal) |
| Worker, TimeRegistration, PayLine, ProfileDay | `shopfloor` (shopfloor_db) | TimeRegistration → ProductionJob; PayLine → payroll export (w43) |
| MachineEvent, OeeBucket, DowntimeRecord | `shopfloor` | MachineEvent → Resource; OeeBucket aggregated per resource × 15 min |

**Cross-wave event catalog**

| Event (CloudEvents type) | Producer | Consumers | Payload summary |
|---|---|---|---|
| `passage.production.order.status-changed.v1` | production | planning, scheduling, shopfloor, lean, analytics | orderId, from, to, qty, dates, actor |
| `passage.production.journal.posted.v1` | production | inventory (w16), ledger (w11), planning | journalId, type, lines[item, qty, cost], journalEntryId |
| `passage.production.order.settled.v1` | production | ledger, costing (w20), analytics | orderId, actualCost, plannedCost, variances[] |
| `passage.planning.plan.completed.v1` | planning | production, purchasing (w17), scheduling | planVersionId, counts, duration, messages |
| `passage.planning.planned-order.firmed.v1` | planning | production, purchasing, inventory | plannedOrderId, targetType, targetId |
| `passage.scheduling.job.scheduled.v1` | scheduling | production, shopfloor, planning | jobId, resourceId, start, end, runId |
| `passage.lean.kanban.state-changed.v1` | lean | production, inventory, shopfloor | kanbanId, from, to, handlingUnits |
| `passage.shopfloor.registration.posted.v1` | shopfloor | production, payroll (w43) | registrationId, workerId, jobId, hours, payType |
| `passage.shopfloor.machine.event.v1` | iot-edge → shopfloor | shopfloor (OEE), production, analytics | machineId, resourceId, kind, counts, state, ts |

**Phase-level risks**

| Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|
| WIP postings drift from the journal under concurrent journal posting on one order | Medium | High | Per-order advisory lock; idempotency key per journal; nightly WIP reconciliation job with alert at any non-zero delta | Engineering |
| MRP non-determinism from map iteration / parallelism | Medium | High | Deterministic sort keys (item, date, source id) at every stage; plan hash in `plan.completed` event; CI compares hashes across 3 runs | Engineering |
| Scheduling heuristic produces infeasible schedules on calendar edge cases (DST, zero-capacity days) | Medium | Medium | Calendar materialized in UTC minutes per resource; property test "no job outside open capacity" over 10k jobs | Engineering |
| Process-industry semantics (potency, co-products) misunderstood | Medium | High | Fixtures reviewed by a process-industry consultant before wave 24 close | Product |
| IoT connector drops events on broker restart | Low | High | Edge buffer (SQLite ring, 24 h) + Kafka idempotent producer + per-machine sequence numbers | Engineering |
| Two-person team on five engine-heavy waves | High | High | Reuse OR-Tools, statsforecast, frePPLe comparison harness; cut scope per the open questions rather than slip proofs | Product |

## 4.21 Wave 21: Production Orders, WIP & Order-Based Costing

### 4.21.1 Scope and business context

Wave 21 is where Passage first consumes components and produces finished goods. Before it, the demo plant buys, receives, pays and values inventory (phase 3) but cannot transform it. After it, a production supervisor can create a production order for a BOM/route item, estimate its planned cost, release it, start it (auto-consuming materials by backflush or manually via a picking list), report route time on route and job cards, report quantities as finished including by-/co-products and scrap, and end the order so that actual cost settles against planned cost with material, labor and overhead variances posted to the GL. Subcontracted operations create a linked purchase order to the vendor.

Out of scope: scheduling against capacity (wave 23; wave 21 schedules with infinite capacity only), MRP-generated orders (wave 22), formula/batch orders, co-product allocation methods beyond the basic by-quantity method (wave 24), operator kiosk and time registration (wave 25), standard-cost roll-up and activation (wave 39; wave 21 reads an active standard or weighted-average cost from wave 20).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX ProdTable status update dialogs (Estimate → Schedule → Release → Start) each open a modal with 8–15 parameters (ProdParmStartUp, ProdParmRelease); a supervisor clicks ~40 times to take one order to Started; SAP CO02 needs CO01 → release → CO11N → MIGO 261 across four transactions | Each status is a separate parameter table and batch class (ProdMultiStartUp); no sensible defaults persisted per site | One `:start` command with site-level defaults; parameter overrides only in an "advanced" drawer; bulk start on the list page | Clicks per order to Started 40 → 3; bulk 500 orders in < 5 min |
| WIP vs GL drift is found at month-end: AX ProdCalcTrans and the ledger disagree after a failed ProdUpd posting left half a journal; SAP KKAX WIP calculation runs as a batch (CO88 settlement) hours before close | Non-atomic multi-table posting (ProdJournal → InventTrans → LedgerTrans) without idempotency; WIP computed periodically rather than per posting | Journal post is one transaction with idempotency key; WipPosting rows per journal; nightly reconciliation job, delta alerts | WIP reconciliation effort 2 days/month → 0; drift detected < 24 h instead of at close |
| Backflush quantities silently rounded: AX BOM lines with `Round-up` = No and InventTable unit precision truncate 0.3333 to 0.33, accumulating variance; SAP RESB rounding profile (MARC.RDPRF) applied inconsistently to backflush | Rounding applied at explosion, not at posting; precision taken from the unit not the line | BR-21.2 rounds at posting with 6-decimal internal precision and explicit per-line round-up flag; variance of rounding reported | Rounding-induced variance < 0.01% of material cost (today 0.3–1%) |
| Cost analysis requires re-keying into Excel: AX "Price calculation" form (ProdCalcTrans) and SAP CO03 cost itemization have no drill to the GL line; auditors re-key order costs into spreadsheets | Cost itemization stored separately from the ledger posting; no shared trace id | Costs tab drills from variance → WipPosting → JournalEntry (w15 drill) with one trace id; `/costs` API for BI | Time to explain one order's variance 30 min → < 1 min; 100% of cost lines link to a journal entry |
| Epicor Kinetic and Infor LN require a separate MES (Plex, Infor MES) to see live order progress; AX users run the ProdTable list with manual refresh | Status and quantities updated only by batch posting classes | `order.status-changed` and `journal.posted` CloudEvents on Kafka within 1 s; any MES/BI consumes them without middleware | Order-status latency to external systems 15 min polling → < 2 s event |

### 4.21.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Production order header | ProdTable, ProdStatus enum, form ProdTable, menu item ProdTable | AUFK/AFKO/AFPO, CO01/CO02/CO03 | `production` service, entity ProductionOrder, screen Production orders | full |
| Order BOM and route snapshot | ProdBOM, ProdRoute, ProdRouteJob (ProdUpdCreate explosion) | RESB (components), AFVC/AFVV (operations), order read of BOM/routing | ProductionBomLine, ProductionOperation, ProductionJob | full |
| Estimation (planned cost) | ProdParmCostEstimation, ProdCalcTrans, status Estimated | Preliminary costing (CO01 → cost), KKF6N | CostEstimate entity, `:estimate` action | full |
| Picking list | ProdJournalTable (JournalType=Bom), ProdJournalBOM, backflush via BOM line flushing principle | Goods issue MIGO 261, backflush at CO11N | ProductionJournal type `picking-list` | full |
| Route card / job card | ProdJournalRoute (route card), ProdJournalRoute by job (job card), ProdParmReportFinished | CO11N confirmation (AFRU), CO15 order confirmation | ProductionJournal types `route-card`, `job-card` | full |
| Report as finished | ProdJournalProd, ProdParmReportFinished, accept error quantity | Goods receipt MIGO 101 for order, CO15 final confirmation | ProductionJournal type `report-as-finished` | full |
| WIP posting | Production posting profiles (ProdPosting), ledger accounts WIP issue/receipt, Picking list issue/offset | WIP calculation KKAX/KKAO, results analysis (RA key) | WipPosting rows, posting profile per item group × site | full |
| Order end and settlement | ProdParmCostUpdate (End), ProdCalcTrans actual, variance accounts | Settlement KO88/CO88, variance calculation KKS2 | `:end` action, Variance entity, settlement to journal | full |
| Subcontracting | Vendor resource type, BOM line type Vendor, auto PO | External processing operation (PP02 control key), PO item category L | ProductionOperation.subcontract → PurchaseOrderLine (w17) | full |
| Production order inquiry | ProdTable listpage, ProdCalcTrans inquiry, ProdJournal inquiries | COOIS, CO03 cost analysis | Production order detail, Cost tab, Status log | improved (single trace id per transaction) |
| Scrap and error quantity | ProdJournalProd.QtyError, BOM line scrap %, route scrap % | Assembly scrap (MARC), component scrap (STPO), operation scrap | Scrap fields on BOM line, operation and RAF line | full |
| Reset status | ProdTable reset status (ProdUpdStatusDown) | Not native (reversal via CO13 cancel confirmation) | `:reset-status` with reversal journals | full |
| Production pools / groups | ProdPool, ProdGroup | Production scheduler profile, order type | OrderGroup (posting profile key) | partial — pools dropped; replaced by saved views |

### 4.21.3 Functional requirements

- **FR-21.1 Create production order** — Must. The system shall create a ProductionOrder for a released product with a BOM version and route version resolved as of the order's delivery date and site, and explode them into ProductionBomLine, ProductionOperation and ProductionJob rows at creation.
  - Notes: Order number from the `production-order` number sequence (continuous not required). Default quantity from released-product default order settings; multiple/min/max enforced. Manual BOM/route version override allowed only before Estimated. Phantom BOM lines are exploded in place; line numbering keeps the parent's line number with a suffix (`30.1`, `30.2`). Explosion of a 7-level, 2,000-component product completes in < 1 s (wave 9 explosion API).
- **FR-21.2 Status lifecycle** — Must. The system shall enforce the state machine in BR-21.1 with statuses Created, Estimated, Scheduled, Released, Started, ReportedAsFinished, Ended and Cancelled.
  - Notes: Transitions are commands (`:estimate`, `:schedule`, `:release`, `:start`, `:report-finished`, `:end`, `:cancel`, `:reset-status`); each is idempotent by `Idempotency-Key`. Each transition writes a StatusLog row with actor, timestamp, trace id. SAP mapping on export: CRTD→Created, REL→Released, PCNF→Started, CNF/DLV→ReportedAsFinished, TECO/CLSD→Ended.
- **FR-21.3 Estimation** — Must. The system shall compute planned cost per order: material (Σ BOM line qty × (1 + scrap%) × unit cost from the active costing version), labor (Σ operation setup + run time × resource cost category rate), overhead (costing-sheet surcharges from wave 20; full costing sheets in wave 39), and subcontract (PO price).
  - Notes: Creates inventory transactions: receipt `ordered` for the finished item, issues `on-order` for BOM lines (w16 statuses). Re-estimate is allowed until Started and replaces the estimate atomically. Rounding: cost amounts to 4 decimals internally, 2 on posting.
- **FR-21.4 Release and printing** — Must. The system shall release an order, generate the picking list and job card documents (PDF), and set BOM-line reservations according to the order's reservation policy (none, estimated, released, started).
  - Notes: Release is blocked if any BOM line has an unreleased product in this legal entity. Release with shortages raises a warning listing short lines; a `strict` flag turns it into an error.
- **FR-21.5 Picking list journal** — Must. The system shall post picking-list journals that issue BOM-line quantities from inventory to WIP, with proposal modes: remaining quantity, consumption proportional to RAF quantity, or manual.
  - Notes: Flushing principle per BOM line: `manual`, `start` (consumed at Start), `finish` (consumed at RAF), `available-at-location` (consumed when job reports). Over-consumption beyond 100% + line tolerance (default 10%) requires duty *Post production over-consumption*. Posting creates one InventoryTransaction per line with status `deducted` and the GL entry Dr WIP / Cr Inventory.
- **FR-21.6 Route card and job card journals** — Must. The system shall post route consumption (setup hours, run hours, quantity good, quantity error) per operation (route card) or per job (job card), valued at the resource's cost category rate, posting Dr WIP / Cr Labor absorption (and Cr Overhead absorption for indirect costs).
  - Notes: Hours in decimals to 2 places; minimum postable 0.01 h. Reporting on an operation of a non-Started order auto-starts the order if the order's parameter `autoStartOnFeedback` is true (default true, AX parity). Operation `finished` flag closes it to further feedback unless reopened.
- **FR-21.7 Report as finished** — Must. The system shall post RAF journals that receive the finished item (and by-/co-product lines) into inventory at the estimated unit cost (standard cost if the item's costing method is standard), consume `finish`-flushed BOM lines, and post Dr Finished goods / Cr WIP.
  - Notes: `acceptError` flag posts error quantity as scrap without inventory receipt. RAF quantity > remaining + tolerance (default 0%) is rejected. The last RAF sets `endJob = true` on all jobs when the `endJobs` parameter is set. Tracking dimensions (batch/serial) required per the item's tracking dimension group; serials must be unique across the tenant (w16).
- **FR-21.8 End and settle** — Must. The system shall end an order by computing actual cost from posted journals, reversing WIP, posting actual cost to the consumption and inventory accounts, and posting variances per BR-21.6.
  - Notes: End date must be ≥ last journal date and in an open inventory and GL period (w14 period control). Remaining `on-order` and `ordered` inventory transactions are cancelled. Orders with RAF quantity 0 and posted consumption are ended with 100% of cost to variance (AX "end with zero quantity" behavior) and require duty *End production order with zero output*.
- **FR-21.9 Reset status** — Should. The system shall reset an order to an earlier status, reversing postings made by later statuses (reversal journals with negative quantities, linked to originals).
  - Notes: Reset from Ended is not allowed (AX parity); reset from ReportedAsFinished to Started reverses all RAF journals. Reset is blocked if any reversed inventory transaction has been consumed by a later issue (on-hand would go negative).
- **FR-21.10 Subcontracting** — Should. The system shall create a purchase order line (w17) for an operation whose resource is of type Vendor, linked to the operation, and shall post the vendor invoice amount as the operation's actual cost on product receipt.
  - Notes: PO line quantity = operation quantity in the service item's UoM; delivery date = operation end. Changes to order quantity after PO confirmation raise a PO change (w17 versioning).
- **FR-21.11 Order inquiry and cost view** — Must. The system shall show, per order, planned vs actual cost by cost group (material, labor, overhead, subcontract), the journal list, inventory transactions, GL entries with drill-down to the journal entry (w15 drill-down) and the status log.
- **FR-21.12 Bulk status update** — Should. The system shall apply a status transition to up to 500 selected orders in one batch job (Temporal), reporting per-order success/failure.
- **FR-21.13 Posting profiles** — Must. The system shall resolve ledger accounts for each posting type (picking list issue, picking list offset, WIP issue, WIP account, RAF receipt, RAF offset, variance accounts per variance type) by item group × site, falling back to All.
- **FR-21.14 Co-product RAF (basic)** — Could. The system shall accept RAF lines for co-/by-products declared on the BOM (w9), allocating cost by quantity only; allocation methods arrive in wave 24.

### 4.21.4 Data model

**ProductionOrder**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id, legal_entity_id | uuid | yes | RLS |
| order_number | text | yes | unique per legal entity; number sequence |
| product_id, variant_id | uuid | yes/no | FK ReleasedProduct (w8) |
| site_id, warehouse_id | uuid | yes | FK (w6) |
| quantity | numeric(18,6) | yes | > 0, in inventory UoM |
| qty_started, qty_raf, qty_error | numeric(18,6) | yes | default 0 |
| bom_version_id, route_version_id | uuid | yes | FK (w9), snapshot taken at creation |
| status | enum | yes | Created…Cancelled |
| delivery_date, start_date, end_date | date | yes/no/no | start/end set by scheduling |
| scheduling_method | enum | yes | `operations` or `jobs` |
| reservation_policy | enum | yes | none/estimated/released/started |
| posting_profile_key | text | yes | item group × site resolved key |
| origin_type, origin_id | enum, uuid | no | PlannedOrder, SalesOrderLine, Manual, Kanban |
| version | int | yes | optimistic concurrency |

**ProductionBomLine**: id, order_id (FK), line_number (text), item_id, variant_id, bom_qty_per (numeric 18,6), bom_series_size (numeric), scrap_pct (numeric 7,4, 0–100), qty_required (numeric 18,6, computed), qty_consumed (numeric 18,6), flushing_principle (enum), line_type (enum: item, phantom, pegged-supply, vendor), warehouse_id, location_id, reservation_status (enum), unit_cost_est (numeric 18,6).

**ProductionOperation**: id, order_id, oper_number (int), next_oper_number (int, 0 = last), priority (enum: primary, secondary1–4), resource_group_id, resource_id (nullable until scheduled), setup_time_h, run_time_h_per (numeric 10,4), process_qty (numeric), queue_before_h, queue_after_h, transit_h, scrap_pct, cost_category_setup_id, cost_category_run_id, subcontract (bool), purchase_line_id (nullable), status (enum: open, started, finished), scheduled_start, scheduled_end (timestamptz).

**ProductionJob**: id, operation_id, job_type (enum: queue-before, setup, process, overlap, transport, queue-after), resource_id, planned_start, planned_end, planned_hours, reported_hours, qty_good, qty_error, status.

**ProductionJournal**: id, order_id, journal_type (enum: picking-list, route-card, job-card, report-as-finished), journal_number, posting_date, posted (bool), posted_at, posted_by, journal_entry_id (w11 FK), reversal_of (nullable self FK), idempotency_key (unique).

**CostEstimate / WipPosting / Variance**: CostEstimate(order_id, cost_group, planned_qty, planned_unit_cost, planned_amount, costing_version_id); WipPosting(order_id, journal_id, posting_type, amount_dr, amount_cr, ledger_account, journal_entry_id); Variance(order_id, variance_type enum {lot-size, production-quantity, production-price, production-substitution}, cost_group, amount, journal_entry_id).

Invariants:
- Σ WipPosting.amount_dr − Σ amount_cr for an order = 0 once status = Ended.
- qty_raf + qty_error ≤ quantity × (1 + raf_tolerance_pct/100).
- Every posted ProductionJournal has exactly one journal_entry_id and its inventory transactions sum to the journal's quantities.
- A ProductionOperation with subcontract = true has exactly one purchase_line_id once the order is Released.

Indexing: ProductionOrder (tenant_id, legal_entity_id, status, delivery_date); (tenant_id, product_id, status); ProductionJournal unique (tenant_id, idempotency_key); WipPosting (order_id); partition ProductionJournal and WipPosting by legal_entity_id hash (16) once rows > 50M.

### 4.21.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/production/orders | Create order (explodes BOM/route) | Idempotency-Key header | Maintain production orders |
| GET | /api/v1/production/orders/{id} | Read order with lines, ops, jobs, cost summary | n/a | View production orders |
| PATCH | /api/v1/production/orders/{id} | Edit header fields allowed for current status | ETag/version | Maintain production orders |
| POST | /api/v1/production/orders/{id}:estimate | Estimate | Idempotency-Key | Maintain production orders |
| POST | /api/v1/production/orders/{id}:release | Release (and :schedule, :start, :end, :cancel, :reset-status) | Idempotency-Key | Release production orders / End production orders |
| POST | /api/v1/production/orders/{id}/journals | Create journal with proposal (`proposal=remaining|proportional|none`) | Idempotency-Key | Post production journals |
| POST | /api/v1/production/journals/{id}:post | Post journal | Idempotency-Key | Post production journals |
| GET | /api/v1/production/orders/{id}/costs | Planned vs actual by cost group | n/a | View production costs |
| POST | /api/v1/production/orders:bulk-status | Batch transition up to 500 ids | Idempotency-Key | Release production orders |
| GET | /api/v1/production/posting-profiles | Resolve accounts for item group × site | n/a | View production setup |

Commands: `CreateOrder` (validates product released, BOM/route active as of delivery date, qty multiples); `PostJournal` (validates order status allows journal type, period open, quantities within tolerance, tracking dims present); `EndOrder` (validates no unposted journals, all subcontract POs invoiced or explicitly accrued).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `passage.production.order.created.v1` | order committed | orderId, orderNumber, productId, qty, siteId, deliveryDate, originType, originId | planning, analytics |
| `passage.production.order.status-changed.v1` | any transition | orderId, from, to, qty, startDate, endDate, actor, occurredAt | planning, scheduling, shopfloor, lean |
| `passage.production.journal.posted.v1` | journal posted | journalId, orderId, type, postingDate, lines[{itemId, qty, unitCost, operNumber, hours}], journalEntryId | inventory, ledger, planning |
| `passage.production.order.settled.v1` | End posted | orderId, plannedCost{material,labor,overhead,subcontract}, actualCost{…}, variances[{type,costGroup,amount}] | ledger, costing, analytics |
| `passage.production.subcontract.po-requested.v1` | release of subcontract op | orderId, operNumber, vendorId, serviceItemId, qty, dueDate | purchasing |

### 4.21.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Production orders (list) | Create, Estimate, Release, Start, RAF, End, bulk status, saved views by status/site | Order, Item, Qty, Remaining, Status, Delivery date, Site, Planned cost, Actual cost | View production orders |
| Production order detail | Tabs: Lines (BOM), Route, Jobs, Journals, Costs, Inventory transactions, Status log | Header fields; per-tab grids | Maintain production orders |
| Journal editor | Add lines, proposal, validate, post, print | Item/operation, qty, hours, error qty, reason, batch/serial | Post production journals |
| Cost analysis | Compare planned vs actual, drill to journal entry | Cost group, planned, actual, variance, variance % | View production costs |
| Posting profile setup | Maintain accounts by item group × site | Posting type, item group, site, account, dimensions | Maintain production setup |

Interaction rules: `Ctrl+Shift+S` opens the status action menu; list page supports multi-select with inline status badge; validation runs on blur per field and fully on post; journal editor keeps unsaved lines in `localStorage` and warns on navigation; the Costs tab is read-only and recomputes on each journal post event (live update via SSE from the BFF).

### 4.21.7 Business rules and edge cases

- **BR-21.1 State machine.** Allowed transitions and their posting effects:

| From → To | Preconditions | Posting |
|---|---|---|
| Created → Estimated | BOM/route exploded | Inventory transactions `ordered`/`on-order`; CostEstimate rows |
| Estimated → Scheduled | estimate exists | Operation/job dates (infinite capacity in w21) |
| Scheduled → Released | no unreleased components | Reservations per policy; documents |
| Released → Started | period open | Auto-post picking list for `start`-flushed lines; route card for ops with `autoStart`; Dr WIP / Cr Inventory |
| Started → ReportedAsFinished | qty_raf > 0 | Dr FG inventory (est. cost) / Cr WIP; consume `finish`-flushed lines |
| ReportedAsFinished → Ended | no open journals; period open | Reverse WIP; post actual; post variances |
| Started → Ended | qty_raf = 0 and duty held | Same as above with 100% variance |
| Created/Estimated/Scheduled/Released → Cancelled | no posted journals | Cancel `ordered`/`on-order` transactions |
| Any non-Ended → earlier status (`:reset-status`) | reversal possible (BR-21.5) | Reversal journals |

  Any other transition returns HTTP 409 `PRODUCTION_ILLEGAL_TRANSITION`.
- **BR-21.2 Quantity rounding.** Required quantity = ceil-to-UoM-precision(qty_per × order_qty / series_size × (1 + scrap/100)); UoM precision from the unit definition (w6), default 6 decimals; "round up to whole unit" flag on the BOM line forces integer.
- **BR-21.3 Posting date.** A journal's posting date must be ≥ order creation date and within an open inventory period and GL period for the Production module; otherwise 422 `PERIOD_CLOSED`.
- **BR-21.4 Concurrency.** Journal posting takes `pg_advisory_xact_lock(hash(order_id))`; concurrent posts serialize; a stale `version` on PATCH returns 412.
- **BR-21.5 Reversal safety.** Reset status is refused when any inventory transaction created by a to-be-reversed journal has a downstream issue with status `deducted` or `picked`.
- **BR-21.6 Variance decomposition at End** (standard-cost items; weighted-average items post only a total variance to the item's inventory adjustment account):
  - Production quantity variance = Σ (actual qty − planned qty for actual output) × standard price.
  - Production price variance = Σ actual qty × (actual price − standard price).
  - Lot-size variance = planned fixed cost (setup, constant overhead) × (1 − actual output / planned output).
  - Production substitution variance = cost of consumed items/operations not on the estimate minus estimate of items not consumed.
  - Sum of the four equals actual cost − (qty_raf × standard unit cost); property-tested.
- **BR-21.7 Worked settlement example.** Item FG-100, standard cost USD 50.00/unit = material 30.00 + labor 15.00 + overhead 5.00. Order 100 units. Estimate: 300 kg RM @ 10.00 = 3,000.00; 50 h @ 30.00 = 1,500.00; overhead 10.00/h × 50 = 500.00; total 5,000.00. Actual: picking list 305 kg at weighted-average 10.20 = 3,111.00; route cards 46 h = 1,380.00; overhead 46 × 10.00 = 460.00; actual total 4,951.00. RAF 100 units posts Dr Finished goods 5,000.00 / Cr WIP 5,000.00. WIP before End: Dr 4,951.00, Cr 5,000.00, balance Cr 49.00. End posts: material quantity variance Dr 50.00 ((305 − 300) × 10.00), material price variance Dr 61.00 (305 × 0.20), labor quantity variance Cr 120.00 ((46 − 50) × 30.00), overhead quantity variance Cr 40.00; WIP cleared to 0.00; net variance Cr 49.00 (favorable). EUR equivalents are carried on each line at the posting-date rate (w12).
- **BR-21.8 Scrap.** Operation scrap compounds backward along the route: quantity to start at operation n = order qty / Π(1 − scrap_k) for k ≥ n, rounded up at each operation.
- **BR-21.9 Negative inventory.** Picking list posting that would drive on-hand negative is rejected unless the item model group allows physical negative inventory (w16 policy).
- **BR-21.10 Serial/batch at RAF.** A RAF line for a serial-tracked item must carry exactly qty_good serial numbers; duplicates across the tenant are rejected at 422 `SERIAL_DUPLICATE`.

### 4.21.8 Acceptance criteria

- **AC-21.1** (FR-21.1) Given a released product with a 3-level BOM and a 4-operation route, when I create an order for 100 units, then the order has the exploded lines with phantoms flattened and quantities per BR-21.2.
- **AC-21.2** (FR-21.2) Given an order in Created, when I call `:start`, then I get 409 `PRODUCTION_ILLEGAL_TRANSITION`.
- **AC-21.3** (FR-21.3) Given an estimated order, when I read `/costs`, then planned material equals Σ line qty × unit cost from the active costing version to 4 decimals.
- **AC-21.4** (FR-21.5) Given a Started order with `start`-flushed lines, when I RAF 100 units with proportional consumption, then consumed quantities equal BOM qty × 100 × (1 + scrap%) exactly (wave proof).
- **AC-21.5** (FR-21.7) Given a batch-tracked finished item, when I post RAF without a batch, then the post fails with 422 and no inventory transaction is created.
- **AC-21.6** (FR-21.8) Given the BR-21.7 fixture, when I end the order, then the four variance rows total −49.00 and the WIP account balance for the order is 0.00.
- **AC-21.7** (FR-21.8) Given a partially RAF'd order (60 of 100) ended with duty, then remaining WIP equals posted consumption − 60 × standard cost and reconciles to the GL WIP account.
- **AC-21.8** (FR-21.9) Given an order in ReportedAsFinished whose finished goods were already shipped, when I reset to Started, then the reset is refused with `REVERSAL_BLOCKED_DOWNSTREAM`.
- **AC-21.9** (FR-21.10) Given an operation on a Vendor resource, when I release the order, then a PO line exists for the vendor with the operation quantity and due date.
- **AC-21.10** (FR-21.12) Given 500 Released orders, when I bulk-start them, then the batch completes in < 5 min and the report lists per-order results.

### 4.21.9 Test plan and fixtures

Fixtures: `prod-orders-50` (50 orders with known consumptions, standard and weighted-average items, generated from the demo plant BOMs; expected WIP and variance postings in a reviewed XLSX); `prod-bom-7level` (from wave 9); `prod-state-machine` (table of all 9×9 transition pairs with expected result); `prod-subcontract-5`.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| State machine | property (Go rapid) | transitions | every illegal transition rejected, every legal one posts expected entries |
| Settlement fixtures | unit | `settle()` | 50/50 orders match workbook to the cent |
| Backflush | unit | RAF proportional | consumed = BOM × qty × (1+scrap) |
| Posting contract | contract | ledger/inventory APIs | journals produce balanced entries, idempotent on replay |
| Period control | contract | journal post | closed period rejected on every journal type |
| Order E2E | Playwright | create→end | demo order ends with 0 WIP |
| Journal post latency | performance | 200-line picking list | p95 < 2 s |

Reconciliation: Σ open-order WIP = GL WIP control account; Σ RAF value = inventory receipts from production (w16) for the period; Σ variances = GL variance accounts.

### 4.21.10 Deployment and operations

Helm chart `production` (Go service, 3 replicas, HPA on CPU 70%), `production-db` (CloudNativePG, 2 instances, 100 GB). Config: `PRODUCTION_RAF_TOLERANCE_PCT` (0), `PRODUCTION_OVERCONSUMPTION_PCT` (10), `PRODUCTION_AUTOSTART_ON_FEEDBACK` (true). Migrations via Atlas from entity metadata. Feature flags: `prod.subcontracting`, `prod.coproduct-basic`. Scheduled jobs: nightly `wip-reconcile` (Temporal, 02:00 tenant local), `cancel-stale-created-orders` (weekly). SLOs: create p95 < 300 ms; transition p95 < 500 ms; journal post p95 < 2 s; availability 99.9%. Alerts: WIP reconciliation delta ≠ 0 (page); journal post error rate > 1% 5 min (warn). Runbook: "WIP delta" — list orders with unbalanced WipPosting, check outbox lag, replay. Rollback: Argo CD to prior digest; migrations are additive within the wave.

### 4.21.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| ProdTable (AX) / AUFK+AFKO+AFPO (SAP) | ProductionOrder | status map per FR-21.2; only open orders (status < Ended / not TECO) |
| ProdBOM / RESB | ProductionBomLine | qty_required recomputed; consumed qty from InventTrans / MSEG 261 |
| ProdRoute, ProdRouteJob / AFVC, AFVV | ProductionOperation, ProductionJob | reported hours from ProdRouteTrans / AFRU |
| ProdJournalTable+Bom/Route/Prod / AFRU, MSEG | ProductionJournal (history) | posted journals loaded read-only, no re-posting |
| ProdCalcTrans / CO cost itemization | CostEstimate (planned), WipPosting (actual, as opening balance) | WIP opening = Σ actual − Σ RAF value, posted as one opening journal per order |

Validation: per-order WIP opening = legacy WIP report (AX ProdCalcTrans actual, SAP KKAX) within 0.01; counts of open orders by status match. Not migrated: ended/TECO orders (go to the lakehouse via w46 history load), production pools, legacy posting-profile accounts (re-mapped to the new chart). Recommended cutover: end all orders in legacy that are ≥ 95% complete.

### 4.21.12 Dependencies, risks, open questions

Depends on: w9 BOM/route explosion and resources; w16 inventory transactions and statuses; w11 posting API; w14 period control; w17 PO creation; w20 costing versions and standard cost.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Variance semantics differ between AX (4 types) and SAP (price/quantity/resource-usage/lot-size/…) | High | Medium | Ship AX's four; document SAP mapping; w39 adds decomposition |
| Backflush under weighted-average produces cost at post time not at End | Medium | Low | Revalue at End using inventory close (w20) settlement |

Open questions: (1) Should Cancelled be exposed for Released orders with reservations? Default yes with automatic un-reserve; decide by wave 21 mid-point. (2) Co-product basic allocation in w21 or defer entirely to w24? Default defer; decide at w21 kickoff.

## 4.22 Wave 22: Master Planning: MRP, MPS & Forecasting

### 4.22.1 Scope and business context

Wave 22 gives planners the engine that decides what to make, buy and move. Before it, orders are created by hand. After it, a nightly (or on-demand) plan nets all demand (sales orders, forecasts, safety stock, dependent demand, transfer demand) against all supply (on-hand, purchase orders, production orders, transfers, planned orders) level by level through the BOM, applying coverage groups and lead times, and produces planned purchase, production and transfer orders with action and futures/delay messages, each fully pegged to the demand it serves. Planners firm planned orders into real orders, and a statistical forecast (ETS/Croston) seeds demand with manual override and a consensus workflow. Out of scope: finite capacity (wave 23; wave 22 plans infinite), DDMRP buffers, multi-site network optimization, and ATP (wave 27 reuses the engine's supply picture).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Nightly MRP regeneration takes hours: AX ReqCalcTask on 50k items runs 3–6 h with helper threads contending on ReqTrans; SAP MD01 NEUPL regenerative run 2–4 h; Epicor Kinetic "MRP Full Regen" and Infor LN "Order Planning" routinely 4+ h, so planners work on yesterday's plan | Row-at-a-time netting with database round trips per item; planned orders deleted and recreated in place | In-memory level-by-level engine with sparse buckets, snapshot per PlanVersion, pointer swap; dynamic net-change within 60 s of an order event | 50k items 4 h → < 10 min; net change 4 h → 60 s |
| No live pegging: AX ReqTransCov is only populated during the run and lost on re-run; SAP MD04 pegged requirements (MD09) require multi-level order report and are unavailable for planned orders mid-run; Plex "Where Used/Demand" pegging is single-level | Pegging stored as a transient by-product of the run | Pegging rows persisted per version; `/pegging` explain tree to any depth; "Explain this plan" in the UI | Time to answer "why is this planned order here" 20 min → < 5 s; 100% of qty explained |
| Exception noise: SAP MD06 exception groups and AX action messages generate tens of thousands of rows planners ignore; Infor LN exception messages cannot be filtered by cause | Messages generated for every supply without fences or root-cause grouping; no deduplication | Action/futures fences per coverage group; messages grouped by root cause (one delayed PO → one message with propagated effects) | Messages per planner per run 5,000 → < 300; 90% of messages actionable (survey) |
| Non-deterministic results: AX plans differ run to run under helper threads; SAP parallel MRP (OMIQ) ordering varies, so planners cannot diff two runs | Parallel processing without stable ordering | Deterministic sort keys and plan hash; version-to-version diff API | Identical input → identical hash 100% of runs; diff of two versions in < 2 s |
| Forecast re-keying: AX ForecastSales grid vs D365 demand forecasting Excel round-trips; SAP MD61 PIR upload by LSMW; Epicor forecast import by CSV | Forecasting done outside the ERP and pasted back | Statistical baseline in the `forecast` service, in-grid override, consensus workflow, published as a version | Forecast cycle 2 days → 2 h; zero CSV round-trips |

### 4.22.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Plan definition | ReqPlan (static/dynamic), ReqPlanVersion | MRP area, planning run parameters (MD01/MD02 processing keys NETCH/NEUPL) | Plan, PlanVersion | full |
| Coverage group | ReqGroup (ReqPOType: Requirement/Period/MinMax/Manual) | MRP type (PD/VB/ND), lot-sizing procedure (T438A: EX, TB, FX, WB), MRP group | CoverageGroup | full |
| Item coverage overrides | ReqItemTable | MARC MRP views 1–4 (DISMM, DISLS, EISBE) | ItemCoverage | full |
| Requirement transactions | ReqTrans (ReqRefType), ReqPO (planned orders) | MDTB (MRP list), PLAF (planned orders), MD04 stock/requirements list | Requirement, PlannedOrder | full |
| Pegging | ReqTransCov | Pegged requirements (MD09), order report | Pegging | improved (explain tree API) |
| Action messages | ReqTrans action (Advance/Postpone/Decrease/Increase/Derived) | Exception messages (MD06, groups 1–8) | ActionMessage | full |
| Futures/delay messages | ReqTrans futures date | Exception 10/15 (reschedule in/out), 30 (plan process according to schedule) | DelayMessage | full |
| Low-level codes | BOMCalcLevel / ReqCalcTask level loop | Low-level code (MARA.DISST) | Item.low_level_code | full |
| Forecast | ForecastSales, ForecastPurch, reduction keys, D365 demand forecasting (Azure ML) | PIR (MD61), consumption mode (MARC.VRMOD), flexible planning/SOP | ForecastLine, forecast service | full |
| MPS | Master scheduling via item coverage + planning time fence | MPS (MD40), MPS items (MRP type M0–M4) | plan scope `mps` with time fence | full |
| Firming | ReqTransPO firming to ProdTable/PurchTable | MD14/MD15 convert planned order | `:firm` | full |
| Intercompany planned orders | Intercompany master planning | Special procurement key 40/70 | planned transfer orders only; intercompany in w40 | partial |

### 4.22.3 Functional requirements

- **FR-22.1 Plan versions** — Must. The system shall run a plan as a batch job (Temporal) that snapshots inputs into a PlanVersion and writes outputs there, swapping the "current" pointer atomically on success.
  - Notes: Two plan kinds, `static` (nightly, full regeneration) and `dynamic` (net-change, triggered by order events within 60 s, limited to affected items). Regeneration deletes unfirmed planned orders of the version; firmed ones are supply. Max 20 retained versions per plan; older ones purged by nightly job.
- **FR-22.2 Coverage groups** — Must. The system shall support coverage codes Requirement (lot-for-lot), Period (coverage time fence in days, orders grouped per period), Min/Max (reorder to max when PAB < min) and Manual (no planned orders, only messages), with safety stock, positive/negative days, coverage time fence, firming time fence, freeze time fence, action and futures message settings.
  - Notes: Defaults: coverage time fence 100 days, negative days 1, positive days 0 (ignore existing supply earlier than requirement when 0 means unlimited; AX semantics documented in BR-22.4), firming fence 0, freeze fence 0. SAP mapping: PD→Requirement/Period; VB→Min/Max with reorder point = min; ND→Manual.
- **FR-22.3 Netting algorithm** — Must. The system shall compute net requirements per BR-22.1 and produce planned orders with order type by the item's default order settings (purchase, production, transfer, kanban).
- **FR-22.4 Lot sizing** — Must. The system shall apply minimum, maximum, multiple and standard order quantities from default order settings per site, and period grouping.
  - Notes: After rounding up to multiple, if the result exceeds maximum, split into ⌈qty/max⌉ orders. Max 1,000 splits per requirement.
- **FR-22.5 Lead time and calendars** — Must. The system shall offset dates by purchase lead time (vendor-specific from trade agreements, else item), production lead time (route-based: Σ operation times at infinite capacity, else item lead time), transfer lead time, plus safety margins (receipt, issue, reorder margins) using the site's working calendar.
- **FR-22.6 Pegging** — Must. The system shall record Pegging rows linking each unit of supply to the demand it covers, FIFO by requirement date then by priority (sales order > forecast > safety stock), and expose an explain tree.
- **FR-22.7 Action and delay messages** — Must. The system shall generate Advance, Postpone, Decrease, Increase, Cancel and Derived actions for existing supply, and Futures/Delay messages when a planned date falls before today (plus the delay propagates upward to the demand).
- **FR-22.8 Forecast and reduction** — Must. The system shall accept forecast lines by item/site/period (and allocation keys) and reduce them by sales orders using reduction principles: none, percent per period, transactions (forecast minus open orders in the period), transactions-dynamic period.
- **FR-22.9 Statistical forecast** — Should. The system shall generate a baseline forecast per item/site from 24+ months of history using ETS (seasonal/non-seasonal selected by AICc) or Croston/TSB for intermittent items (ADI > 1.32), via the Python `forecast` service, with MAPE/wMAPE reported per item.
  - Notes: Batch for 50k items < 15 min. Manual override per cell; consensus workflow (Temporal approval) locks the forecast version.
- **FR-22.10 Firming** — Must. The system shall firm a planned order into a ProductionOrder (`:estimate` optional), PurchaseOrder line (grouped by vendor/date per setting) or TransferOrder, preserving pegging to the firmed supply.
- **FR-22.11 Planned order maintenance** — Must. The system shall allow planners to change quantity/date/vendor on a planned order, approve it (becomes firmed on next run), or delete it.
- **FR-22.12 Net requirements view** — Must. The system shall show per item/site the MD04-style time-phased list: period, gross requirements, scheduled receipts, planned receipts, PAB, net requirement, with drill to pegging.
- **FR-22.13 Plan scope** — Should. The system shall support plan filters by site, item group, planner, low-level-code range, and `mps` scope (MPS items only; MRP uses MPS output as fixed supply).
- **FR-22.14 Determinism** — Must. The system shall produce byte-identical outputs for identical inputs; the plan hash is emitted in `plan.completed`.

### 4.22.4 Data model

**CoverageGroup**: id, tenant_id, code (text, unique), coverage_code (enum), coverage_time_fence_days (int, default 100), negative_days, positive_days (int), safety_stock_policy (enum: today, first-issue), firming_fence_days, freeze_fence_days, action_fence_days, futures_fence_days, period_days (int, Period only), rescheduling_margin_days, generate_actions (bool), generate_futures (bool).

**ItemCoverage**: id, item_id, site_id, warehouse_id (nullable), coverage_group_id, min_qty, max_qty, safety_margin_receipt_days, lead_time_days (override), vendor_id (default), planned_order_type (enum).

**PlanVersion**: id, plan_id, number (int), kind (static/dynamic), snapshot_at (timestamptz), status (running/complete/failed/current), items_planned, planned_orders_count, messages_count, duration_ms, plan_hash (bytea, sha256).

**Requirement** (ReqTrans): id, plan_version_id, item_id, variant_id, site_id, warehouse_id, ref_type (enum: sales-line, forecast, safety-stock, bom-line, transfer-issue, on-hand, purchase-line, production-order, transfer-receipt, planned-order), ref_id, direction (demand/supply), qty (numeric 18,6, +supply/−demand), requirement_date (date), futures_date (date, nullable), low_level_code (int), priority (int).

**PlannedOrder** (ReqPO): id, plan_version_id, number, item_id, site_id, warehouse_id, order_type (purchase/production/transfer/kanban), qty, order_date, delivery_date, vendor_id, from_warehouse_id, status (enum: unprocessed, approved, firmed), firmed_target_type, firmed_target_id, bom_version_id, route_version_id, lot_rule_applied (text).

**Pegging** (ReqTransCov): id, supply_requirement_id, demand_requirement_id, qty, is_marking (bool).

**ActionMessage**: id, requirement_id, action (enum), suggested_qty, suggested_date, reason, delay_days.

**ForecastLine**: id, model_id, item_id, site_id, period_start, period_end, qty, source (statistical/manual/consensus), reduced_qty, version.

Invariants: Σ Pegging.qty per supply ≤ supply qty; Σ Pegging.qty per demand = demand qty when fully covered (else a `Cancel`-free shortage message exists); PAB(t) = PAB(t−1) + Σ supply(t) − Σ demand(t) for every period; only one PlanVersion per plan has status `current`.

Indexing: Requirement (plan_version_id, item_id, site_id, requirement_date); Pegging (demand_requirement_id), (supply_requirement_id); PlanVersion rows partitioned by plan_version_id (one partition per version, dropped on purge — avoids 50M-row deletes).

### 4.22.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/planning/plans/{planId}:run | Start plan (static/dynamic, scope filter) | Idempotency-Key; one running version per plan | Run master planning |
| GET | /api/v1/planning/plans/{planId}/versions/{v} | Version status, counts, hash | n/a | View planning |
| GET | /api/v1/planning/planned-orders?planId&status&site&itemGroup | List/filter planned orders | n/a | View planning |
| PATCH | /api/v1/planning/planned-orders/{id} | Change qty/date/vendor; approve | ETag | Maintain planned orders |
| POST | /api/v1/planning/planned-orders:firm | Firm up to 1,000 ids with grouping options | Idempotency-Key | Firm planned orders |
| GET | /api/v1/planning/net-requirements?itemId&siteId&planId | MD04-style time-phased view | n/a | View planning |
| GET | /api/v1/planning/planned-orders/{id}/pegging | Explain tree (depth ≤ 20) | n/a | View planning |
| GET | /api/v1/planning/messages?planId&type | Action/delay messages | n/a | View planning |
| POST | /api/v1/planning/forecasts/{modelId}:generate | Statistical forecast batch | Idempotency-Key | Maintain forecasts |
| PUT | /api/v1/planning/forecasts/{modelId}/lines | Bulk upsert manual lines | full replace per item/site/period | Maintain forecasts |
| GET | /api/v1/planning/coverage-groups | Setup | n/a | View planning setup |

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `passage.planning.plan.started.v1` | run begins | planId, versionId, kind, scope | analytics |
| `passage.planning.plan.completed.v1` | version becomes current | planId, versionId, itemsPlanned, plannedOrders{purchase,production,transfer}, messages, durationMs, planHash | production, purchasing, scheduling |
| `passage.planning.planned-order.firmed.v1` | firm | plannedOrderId, orderType, targetId, qty, dates | production, purchasing, inventory |
| `passage.planning.message.raised.v1` | message created | requirementId, refType, refId, action, delayDays | purchasing, sales (w27), notifications |
| `passage.planning.forecast.published.v1` | consensus approved | modelId, version, items, horizonPeriods | planning run |

### 4.22.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Master planning workspace | Run plan, view last run, tiles: delayed orders, actions, planned orders by type | Plan, version, status, duration, counts | View planning |
| Planned orders | Filter, approve, firm (grouped), change, delete, bulk | Number, item, site, type, qty, order/delivery date, vendor, status, delay | Maintain planned orders |
| Net requirements (MD04) | Time-phased grid, pegging drill, simulate (what-if qty change) | Period, GR, SR, PR, PAB, net req, messages | View planning |
| Action messages | Apply action to the source order (advance/postpone PO date), dismiss | Action, order, qty, date, suggested date, delay | Maintain planned orders |
| Forecast grid | Edit cells, generate statistical, submit consensus | Item, site, period columns, source badge, MAPE | Maintain forecasts |
| Coverage groups / item coverage | Setup | per data model | Maintain planning setup |

Interaction rules: planned-orders grid supports keyboard range selection and `F` to firm; cell edits in the forecast grid validate ≥ 0 immediately; the net-requirements grid is virtualized to 365 periods × 20 columns; pegging tree lazy-loads children.

### 4.22.7 Business rules and edge cases

- **BR-22.1 Netting algorithm.** Per plan run:
  1. Snapshot inputs (on-hand by dimension at physical-available, open orders, forecasts after reduction, safety stock, item coverage, BOM/route versions as of each requirement date, calendars) into the PlanVersion.
  2. Assign low-level codes: LLC(item) = max over all active BOMs of the depth at which the item appears (finished items 0). Recomputed when any BOM changes (w9 event). Cycles are rejected at BOM activation.
  3. For LLC = 0 … max: for each item/site in that level, sort requirements by date then priority then ref id (determinism).
  4. Build time buckets by day; PAB₀ = on-hand − safety stock (policy `today`) or on-hand (policy `first-issue`, safety stock added as a demand on the first issue date).
  5. For each day t: PAB_t = PAB_{t−1} + SR_t + PR_t − GR_t. If PAB_t < 0 (Requirement/Period) or PAB_t < min (Min/Max), net requirement N = −PAB_t (or max − PAB_t).
  6. Lot-size N: Requirement → N; Period → aggregate N over the next `period_days`; Min/Max → max − PAB; then apply min/multiple/max/standard qty.
  7. Create planned order with delivery date t − receipt margin; order date = delivery date − lead time (working days); if order date < today → futures_date = today + lead time; raise Delay message with delay_days and propagate the futures date to pegged demand.
  8. Peg the planned order to the demand(s) that caused it, FIFO.
  9. Explode planned production orders: for each BOM line, create dependent demand Requirement (ref_type bom-line) on the item at date = order start (or operation start when route-based), qty per BR-21.2; these appear at a higher LLC and are processed in step 3 of that level.
  10. For existing supply earlier than its earliest pegged demand by more than positive_days (0 = unlimited) raise Postpone; later than demand by more than negative_days raise Advance; unpegged supply beyond the fence raises Cancel/Decrease.
  11. Write outputs, compute plan hash over (planned orders, pegging, messages) sorted, mark version current.
- **BR-22.2 Worked example (3-level BOM, weekly buckets, today = Monday of week 1).** FG-100 (LLC 0) uses 2 × SA-200 (LLC 1) and 1 × RM-300; SA-200 uses 3 × RM-300, so RM-300 has LLC 2. Coverage: all Requirement; FG-100 lead time 1 wk, safety stock 10, on-hand 20; SA-200 lead time 1 wk, multiple 100, on-hand 50; RM-300 purchase lead time 2 wk, minimum 500, on-hand 200, open PO 300 due week 2. Demand: sales order 120 FG week 4; forecast 80 FG week 5.
  - FG-100: PAB₀ = 20 − 10 = 10. Week 4: 10 − 120 = −110 → planned production PP-1 110, due wk 4, start wk 3. Week 5: 0 − 80 = −80 → PP-2 80, due wk 5, start wk 4. Dependent demand: SA-200 220 wk 3, 160 wk 4; RM-300 110 wk 3, 80 wk 4.
  - SA-200: PAB₀ = 50. Week 3: 50 − 220 = −170 → multiple 100 → PP-3 200, due wk 3, start wk 2; PAB 30. Week 4: 30 − 160 = −130 → PP-4 200, due wk 4, start wk 3; PAB 70. Dependent RM-300: 600 wk 2, 600 wk 3.
  - RM-300: PAB₀ = 200. Week 2: 200 + 300 (PO) − 600 = −100 → min 500 → planned purchase PO-1 500 due wk 2, order date wk 0 < today → futures date wk 3, Delay message 7 days; PAB 400. Week 3: 400 − 600 − 110 = −310 → PO-2 500 due wk 3, order date wk 1 = today (no delay); PAB 190. Week 4: 190 − 80 = 110.
  - Propagation: PP-3 pegged to PO-1 inherits futures wk 3 → PP-3 cannot finish before wk 4 → PP-1 futures wk 5 → Delay message on sales order line (requested wk 4, expected wk 5, delay 7 days). Pegging explains 100% of each planned order: PP-1 → SO 110 of 120 (10 from on-hand after safety), PO-1 → PP-3 (100 of 500; 400 surplus unpegged, no Cancel because within coverage time fence).
- **BR-22.3 Forecast reduction.** `transactions` principle: reduced forecast for a period = max(0, forecast − Σ sales order qty in the period); periods are forecast-line periods, not calendar months.
- **BR-22.4 Fences.** Requirements inside the freeze fence generate no planned orders; inside the firming fence, planned orders are auto-firmed; action messages are generated only inside the action fence; futures messages only inside the futures fence.
- **BR-22.5 Safety stock replenishment date.** With policy `today`, the safety-stock planned order is dated today + lead time; with `first-issue`, the first demand date.
- **BR-22.6 Dynamic plan scope.** A dynamic run replans the changed item and all items whose LLC is greater and that are reachable through its BOM; it never changes planned orders outside that set.
- **BR-22.7 Transfer planning.** Warehouse-level coverage with `transfer` order type creates demand at the from-warehouse at delivery date − transfer lead time.
- **BR-22.8 Calendar edge cases.** Lead-time offsets skip closed days; if a date falls on a closed day it moves backward (supply) or forward (demand) to the nearest open day.
- **BR-22.9 Firming concurrency.** Firming is a saga: create target order → mark planned order firmed → on failure compensate by deleting the target; a planned order already firmed returns 409.
- **BR-22.10 Quantity precision.** Requirements carry the item's inventory UoM to 6 decimals; forecast lines in forecast UoM converted at load.

### 4.22.8 Acceptance criteria

- **AC-22.1** (FR-22.3, 22.4, 22.5) Given the BR-22.2 fixture, when the static plan runs, then the planned orders PP-1…PP-4, PO-1, PO-2 exist with the stated quantities and dates and a 7-day delay message on the sales line.
- **AC-22.2** (FR-22.6) Given any planned order in the 40-fixture set, when I read its pegging, then Σ pegged qty = planned qty.
- **AC-22.3** (FR-22.14) Given the 50k-item dataset, when the plan runs three times, then all three plan hashes are equal and each run completes in < 10 min.
- **AC-22.4** (FR-22.2) Given a Min/Max item with min 100, max 400 and PAB 90, when planned, then one planned order of 310 is created.
- **AC-22.5** (FR-22.7) Given a PO due 10 working days before its earliest pegged demand and positive_days = 5, when planned, then a Postpone message suggests the demand date − receipt margin.
- **AC-22.6** (FR-22.8) Given forecast 100 and sales orders 30 in a period with `transactions` reduction, then gross requirement from forecast is 70.
- **AC-22.7** (FR-22.9) Given 24 months of intermittent history (ADI 2.1), when the statistical forecast runs, then the Croston/TSB model is chosen and MAPE is reported.
- **AC-22.8** (FR-22.10) Given 3 approved planned purchase orders for one vendor on one date with grouping on, when firmed, then one PO with 3 lines is created and the planned orders show status firmed with target id.
- **AC-22.9** (FR-22.1) Given a running static version, when a second run is requested, then 409 `PLAN_ALREADY_RUNNING`.
- **AC-22.10** (FR-22.12) Given item RM-300 in the example, when I open net requirements, then the PAB column shows 400, 190, 110 for weeks 2–4.

### 4.22.9 Test plan and fixtures

Fixtures: `mrp-textbook-40` (APICS CPIM-style scenarios: lot-for-lot, FOQ, POQ, safety stock, scrap, phantom, multi-source, delays; expected planned orders as JSON); `mrp-3level` (BR-22.2); `mrp-50k` (generated: 50k items, 5-level BOMs, 12 monthly periods, 200k sales lines); `forecast-history-2k` (24 months, mix of smooth/intermittent); `frepple-compare` (shared dataset exported to frePPLe, expected within ±1 day and ±1% qty).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Textbook scenarios | unit | engine | 40/40 match |
| PAB identity | property | engine | PAB recurrence holds for every item/day |
| Pegging completeness | property | pegging | Σ peg = planned qty for all planned orders |
| Determinism | integration | plan run | 3 identical hashes |
| Scale | performance | 50k plan | < 10 min, memory < 8 GB |
| Firming saga | contract | planning ↔ production/purchasing | compensations on injected failure |
| Forecast models | unit (Python) | statsforecast wrapper | model selection matches fixture |
| frePPLe comparison | integration (optional) | shared dataset | within tolerance |

Reconciliation: Σ supply − Σ demand per item/site over the horizon = closing PAB; planned production orders firmed = ProductionOrders with origin PlannedOrder.

### 4.22.10 Deployment and operations

Helm charts `planning` (Go, 2 replicas API + 1 planner worker pool with 8 CPU / 16 GB request, Temporal worker), `planning-db` (CloudNativePG, 200 GB), `forecast` (Python, 2 replicas). Config: `PLANNING_MAX_VERSIONS` (20), `PLANNING_DYNAMIC_DEBOUNCE_SECONDS` (60), `PLANNING_HORIZON_DAYS` (365). Flags: `planning.dynamic`, `planning.statistical-forecast`, `planning.mps-scope`. Scheduled: nightly static plan 01:00 site-local; weekly forecast generation Sunday 22:00; version purge daily. SLOs: 50k-item plan < 10 min; planned-order list p95 < 300 ms; pegging read p95 < 500 ms. Alerts: plan failed or > 20 min (page); dynamic-plan lag > 5 min (warn). Runbook: "plan stuck" — inspect Temporal workflow, cancel, re-run; "hash mismatch" — compare sorted outputs between versions. Rollback: prior digest; versions are immutable so rollback never corrupts plans.

### 4.22.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| ReqGroup (AX) / T438A + MRP type config (SAP) | CoverageGroup | ReqPOType→coverage_code; SAP lot-size EX→Requirement, TB→Period, FX→fixed qty via ItemCoverage standard qty, WB→Period(7) |
| ReqItemTable / MARC MRP views | ItemCoverage | DISMM PD/VB/ND; EISBE→safety; DISLS; PLIFZ→lead time |
| ForecastSales / PIR (PBIM/PBED) | ForecastLine | periods normalized to weeks or months |
| ReqPO (firmed only) / PLAF (firmed, PLAF-AUFFX = X) | PlannedOrder status firmed | unfirmed planned orders are not migrated — regenerated by the first run |
| ReqTrans, ReqTransCov / MDTB | not migrated | regenerated |

Validation: first Passage plan vs last legacy plan on identical snapshot: planned order count within ±5% and 95% of planned orders match item/date ±1 day. Not migrated: planning history, AX ReqCalcTask logs, SAP MRP lists.

### 4.22.12 Dependencies, risks, open questions

Depends on: w21 production orders (firming target, route-based lead times); w17 PO creation; w16 on-hand and inventory transactions; w9 BOM/LLC; w6 calendars; w27 sales orders (demand arrives later — until then sales demand is loaded via fixtures and the `sales-line` ref type).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Without sales orders until w27 the plan is demo-only | Certain | Low | Forecast + fixture demand; re-proof at w27 |
| Memory blow-up on 50k × 365 daily buckets | Medium | Medium | Sparse buckets (only dates with movement); weekly buckets beyond 90 days |

Open questions: (1) Daily vs weekly buckets beyond 90 days — default weekly; decide at w22 kickoff. (2) Whether to expose `positive_days = 0` as "unlimited" (AX) or 0 — default AX semantics; decide by w22 mid-point.

## 4.23 Wave 23: Capacity Planning & Finite Scheduling

### 4.23.1 Scope and business context

Wave 23 replaces the infinite-capacity dating of waves 21–22 with a scheduler that places operations and jobs on resources with calendars and finite capacity, forward or backward, honoring precedence, setup, queue and transit times, primary/secondary resources and capabilities. Planners see a resource × time Gantt, drag jobs, see capacity load and bottlenecks, and optionally run a CP-SAT optimization that minimizes weighted lateness and makespan. Schedule dates flow back to production orders (start/end, job dates) and to master planning (planned production orders use scheduled lead times). Out of scope: shop-floor dispatch lists on the kiosk (wave 25 reads this wave's jobs), tool/fixture management, multi-plant capacity.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX job scheduling on a 2,000-job plant takes 20–40 min (WrkCtrCapRes rebuilt per order in X++); SAP CM21 capacity leveling is manual dispatch per work center and PP/DS needs a separate APO/embedded install with CIF integration | Scheduling runs per order inside the status-update dialog; capacity reservations stored row-by-row; finite scheduling is an add-on product | Scheduler service with materialized UTC capacity intervals, GiST exclusion constraint, site-level run with checkpoints | 5,000 operations 30 min → < 60 s; no separate product |
| Gantt is read-mostly: D365 Gantt drag does not validate secondary resources or calendars until save; SAP CM21 planning board shows overloads but cannot propagate to dependent orders; Epicor Kinetic Multi-Resource Scheduling requires the APS module and locks the board during save | Validation happens server-side after commit; no dependency graph in the board | `:move` with `dryRun` preview showing violations and dependent impact before commit; canvas Gantt | Reschedule-and-verify 15 min → < 30 s; zero committed double-bookings |
| Load reports lag reality: AX "Capacity load" form (WrkCtrCapRes) and SAP CM01 are as-of the last scheduling run; Infor LN "Capacity Utilization" is a batch report | Load is not maintained incrementally | CapacityLoad recomputed per run and per move; bottleneck events | Load view staleness hours → < 5 s after any move |
| Optimization is unavailable or opaque: no optimizer in AX; SAP PP/DS optimizer configuration is a consulting project; Plex scheduling is rule-based only | Optimization tied to proprietary engines | CP-SAT via OR-Tools with a stated objective, time limit and reported solver status; heuristic kept if no improvement | Known-optimum gap ≤ 5% on small instances; optimization run self-service in 1 click |
| Scheduled dates do not reach planning: AX master planning re-derives lead times from the route rather than from the schedule; SAP MRP ignores PP/DS results unless CIF is configured | Planning and scheduling keep separate date models | `order.dates-changed` events feed planning; `planning.finite-capacity` flag uses scheduled dates | Date mismatch between plan and schedule 100% of orders → 0 |

### 4.23.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Operations scheduling | ProdTable scheduling (operations), WrkCtrCapRes (capacity reservations), scheduling direction | Lead-time scheduling (CA97), capacity requirements KBED/KBEZ | ScheduleRun mode `operations`, ResourceCapacity | full |
| Job scheduling | Job scheduling (ProdRouteJob), WrkCtrCapRes by job | Detailed scheduling (PP/DS), SAP APO | mode `jobs` (minute level) | full |
| Finite/infinite capacity | ReqGroup/parameters finite capacity, finite capacity on resource | Capacity leveling CM21/CM22, finite scheduling flag | per-resource `finite` flag; run option | full |
| Capacity load view | WrkCtrCapRes load inquiry, Capacity load graph | CM01 (load), CM05, CM07 | Capacity load screen | full |
| Gantt | Gantt chart (D365) | Planning board CM21, PP/DS detailed scheduling board | Scheduling workspace Gantt | improved (drag with constraint check) |
| Calendars/efficiency | WorkCalendar, efficiency % on resource, capacity per calendar day | Factory calendar, capacity header (CR02), utilization % | WorkCalendar (w9), ResourceCapacity.efficiency_pct | full |
| Primary/secondary resources | Route operation resource requirements (capability/group/resource/type), priority | Alternative sequences, work center hierarchy, split (AFVV) | ProductionOperation.priority; capability matching | full |
| Optimization | none native | PP/DS optimizer (genetic), S/4 pMRP | CP-SAT mode | improved |
| Reschedule dependents | Reschedule with reference orders | Rescheduling from MRP, CM21 dispatch | `:reschedule` with dependency propagation | full |
| Bottleneck resource | Bottleneck resource flag (D365 scheduling) | Bottleneck work center (CM21) | utilization > threshold detection | full |

### 4.23.3 Functional requirements

- **FR-23.1 Capacity model** — Must. The system shall materialize ResourceCapacity as open intervals (UTC minutes) per resource per day from its WorkCalendar (shifts), efficiency % and capacity-per-day override, for a rolling horizon of 365 days.
  - Notes: Rebuilt incrementally when a calendar changes (w9 event). Finite resources cannot be loaded beyond open interval minutes; infinite resources ignore the limit but still only schedule in open intervals.
- **FR-23.2 Operations scheduling** — Must. The system shall schedule operations in daily buckets, forward from a start date or backward from a delivery date, selecting a resource per operation by priority (named resource > resource group > capability) and finite availability.
- **FR-23.3 Job scheduling** — Must. The system shall schedule jobs (queue-before, setup, process, overlap, transport, queue-after) at minute granularity with the same direction and resource choice rules, splitting process jobs across shifts but not across resources.
- **FR-23.4 Constraints** — Must. The scheduler shall honor: precedence (next_oper_number), max one process job per finite resource at any minute, secondary resources booked concurrently with the primary, setup before process, overlap transfer batch (operation n+1 may start after `overlap_qty` units), transit time between resources, calendar open intervals, order start ≥ today (forward) and end ≤ delivery date (backward, else convert to forward and flag late), operation `finished` jobs frozen, firmed jobs frozen.
- **FR-23.5 Dispatch heuristic** — Must. The default algorithm is priority-list scheduling: orders sorted by (delivery date, priority, order number); each operation placed at the earliest feasible interval on the best resource (least finish time, tie → least load); complexity O(n log n) per resource.
- **FR-23.6 CP-SAT optimization** — Should. The system shall offer an optimization run via OR-Tools CP-SAT (Python `scheduler-opt` service) with objective minimize Σ weight × lateness + λ × makespan, time limit configurable (default 60 s), horizon ≤ 2,000 operations per run (larger sets are windowed).
- **FR-23.7 Gantt** — Must. The system shall render resources × time with jobs as bars colored by order, allow drag to move/reassign a job within its capability set, and show constraint violations (red outline + tooltip) before commit.
- **FR-23.8 Reschedule with propagation** — Must. Moving a job shall reschedule all successors of the same order forward and, optionally, the dependents (orders pegged to the moved order's output) with a preview of affected orders before commit.
- **FR-23.9 Capacity load** — Must. The system shall show load % = Σ booked minutes / Σ open minutes per resource/group/period (day/week), with bottleneck flag when load > 90% in ≥ 3 consecutive weeks (threshold configurable).
- **FR-23.10 Feedback to planning** — Must. The system shall publish scheduled dates so that production orders update start/end, and master planning uses scheduled lead times for firmed orders and route-based estimates for planned orders (optionally finite in the plan run via flag `planning.finite-capacity`).
- **FR-23.11 Schedule runs** — Must. Scheduling can run per order (on `:schedule`), per selection, or for the whole site nightly, as Temporal jobs with checkpoints every 500 operations.
- **FR-23.12 Capability matching** — Should. Resource selection shall honor capability requirements with minimum level and expiry dates (w9 capabilities).

### 4.23.4 Data model

**ResourceCapacity**: id, tenant_id, resource_id, day (date), interval_start (timestamptz), interval_end, open_minutes (int), efficiency_pct (numeric 5,2), finite (bool), source (calendar/override).

**ScheduledJob**: id, job_id (FK ProductionJob), order_id, oper_number, resource_id, start (timestamptz), end, minutes, run_id, firmed (bool), late_minutes (int), overlap_qty (numeric), secondary_resource_ids (uuid[]).

**ScheduleRun**: id, scope (order/selection/site), mode (operations/jobs), direction (forward/backward), finite (bool), algorithm (heuristic/cp-sat), time_limit_s, status, started_at, finished_at, operations_count, makespan_minutes, total_lateness_minutes, objective_value, solver_status (OPTIMAL/FEASIBLE/UNKNOWN).

**CapacityLoad** (materialized): resource_id, period_start, period_type, open_minutes, booked_minutes, load_pct, bottleneck (bool).

Invariants: no two ScheduledJob rows on the same finite resource overlap in [start, end); every ScheduledJob lies within ResourceCapacity open intervals of its resource; for jobs j, k in the same operation chain with k successor of j: k.start ≥ j.end + transit; a ScheduledJob with firmed = true is unchanged by any run without `force`.

Indexing: ScheduledJob GiST exclusion constraint `EXCLUDE USING gist (resource_id WITH =, tstzrange(start,end) WITH &&) WHERE (finite)`; ResourceCapacity (resource_id, day); CapacityLoad refreshed per run.

### 4.23.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/scheduling/runs | Start run (scope, mode, direction, finite, algorithm) | Idempotency-Key | Run scheduling |
| GET | /api/v1/scheduling/runs/{id} | Run status/metrics | n/a | View scheduling |
| GET | /api/v1/scheduling/gantt?siteId&from&to&resourceIds | Jobs for Gantt window (≤ 5,000 bars) | n/a | View scheduling |
| POST | /api/v1/scheduling/jobs/{id}:move | Move/reassign with `dryRun` preview | Idempotency-Key | Maintain schedule |
| POST | /api/v1/scheduling/jobs/{id}:firm | Firm/unfirm | Idempotency-Key | Maintain schedule |
| GET | /api/v1/scheduling/capacity-load?resourceIds&period=week | Load and bottlenecks | n/a | View scheduling |
| GET | /api/v1/scheduling/resources/{id}/capacity?from&to | Open intervals | n/a | View scheduling |
| POST | /api/v1/scheduling/resources/{id}/capacity-overrides | Add downtime/extra shift | Idempotency-Key | Maintain capacity |

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `passage.scheduling.run.completed.v1` | run done | runId, scope, operations, makespanMinutes, totalLatenessMinutes, solverStatus | production, planning |
| `passage.scheduling.job.scheduled.v1` | job placed/moved | jobId, orderId, operNumber, resourceId, start, end, runId, firmed | production, shopfloor |
| `passage.scheduling.order.dates-changed.v1` | order start/end changed | orderId, start, end, lateMinutes | production, planning, sales (w27) |
| `passage.scheduling.bottleneck.detected.v1` | load > threshold | resourceId, periods[], loadPct | notifications |

### 4.23.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Scheduling workspace (Gantt) | Zoom day/week, drag job, reassign, firm, run heuristic/optimize, preview propagation | Resource rows, job bars (order, item, qty, start/end), late markers, open-interval shading | Maintain schedule |
| Capacity load | Filter resource/group/period, open bottleneck, drill to jobs | Resource, period, open h, booked h, load %, bottleneck | View scheduling |
| Schedule runs | Start, monitor, compare runs | Scope, mode, algorithm, status, makespan, lateness, duration | Run scheduling |
| Resource capacity | Overrides, view calendar materialization | Day, intervals, efficiency, finite | Maintain capacity |

Interaction rules: drag snaps to 5-minute grid; `Shift+drag` moves the whole order; violations block commit unless the user holds *Override scheduling constraints*; Gantt virtualizes rows and uses canvas rendering for > 1,000 bars; undo stack of 20 moves per session.

### 4.23.7 Business rules and edge cases

- **BR-23.1 Duration.** Process minutes = (run_time_h_per × qty / process_qty) × 60 / (efficiency_pct/100); setup minutes = setup_time_h × 60 / efficiency; both rounded up to whole minutes.
- **BR-23.2 Backward fallback.** If backward scheduling places any job before now + 1 h, the run re-schedules the order forward from now and sets late_minutes = end − delivery date end-of-day.
- **BR-23.3 Shift splitting.** A process job crossing a closed interval is split into segments on the same resource; setup is never split (if it does not fit before close, it moves to the next open interval).
- **BR-23.4 Secondary resources.** Booked for the same interval as the primary; if no secondary is free, the primary slot is skipped.
- **BR-23.5 Overlap.** With overlap_qty q > 0, operation n+1 earliest start = start_n + setup_n + process minutes for q units + transit.
- **BR-23.6 Firmed and reported jobs.** Jobs with reported hours or `finished` status keep their actual start; remaining minutes = planned − reported, floor 0.
- **BR-23.7 DST and time zones.** All intervals stored in UTC; calendars defined in site time zone; a 23-hour DST day yields fewer open minutes — property test covers 2026-03-29 (EU) and 2026-11-01 (US).
- **BR-23.8 Resource selection tie-break.** Equal finish time → lower load % → lower resource code (deterministic).
- **BR-23.9 CP-SAT model.** Interval variables per job, `NoOverlap` per finite resource, `AddNoOverlap2D` not used; precedence as start ≥ end + transit; objective Σ w_i × max(0, end_i − due_i) + λ × makespan with λ = 0.1 default; `num_workers` = 8; result accepted if FEASIBLE; heuristic result kept if solver returns UNKNOWN.
- **BR-23.10 Propagation limit.** Dependent rescheduling stops after 500 orders or 5 pegging levels and reports the truncation.

### 4.23.8 Acceptance criteria

- **AC-23.1** (FR-23.2, 23.4) Given OR-Library instance `ft06`, when scheduled with the heuristic, then the schedule is feasible (no overlap, precedence held); when optimized with CP-SAT 60 s, makespan = 55 (known optimum).
- **AC-23.2** (FR-23.4) Given 10k random jobs across 50 finite resources, then the exclusion constraint never fires and the property test "every job inside open intervals" passes.
- **AC-23.3** (FR-23.8) Given an order whose output is pegged to a sales line, when I move its last job 2 days later, then the preview lists the sales line delay and after commit the order's end date and `order.dates-changed` event carry the new date.
- **AC-23.4** (FR-23.11) Given 5,000 operations on the demo plant, when the site run executes, then it completes in < 60 s.
- **AC-23.5** (FR-23.3) Given a 300-minute process job on a resource with a shift ending in 120 minutes, then two ScheduledJob segments of 120 and 180 minutes exist on the same resource.
- **AC-23.6** (FR-23.9) Given booked 150 h against 160 h open for 3 consecutive weeks, then the resource is flagged bottleneck and an event is emitted once (not per run).
- **AC-23.7** (FR-23.1) Given a calendar change closing next Friday, then the ResourceCapacity rows for that day are removed within 30 s and affected jobs show violations.
- **AC-23.8** (FR-23.10) Given a scheduled order, when planning runs with `planning.finite-capacity` on, then its supply date equals the scheduled end date.
- **AC-23.9** (FR-23.6) Given an optimization run that times out with UNKNOWN, then the heuristic schedule remains and the run reports solver_status UNKNOWN.

### 4.23.9 Test plan and fixtures

Fixtures: `orlib-jobshop-30` (ft06, ft10, ft20, la01–la20, abz5–abz9 with known optima); `sched-calendar-edge` (DST days, 24/7, zero-capacity holidays); `sched-5k-ops` (demo plant scaled); `sched-propagation` (pegged chains 5 deep).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Job-shop feasibility | unit | heuristic | 30/30 feasible |
| CP-SAT optimality | integration | optimizer | within 5% on instances ≤ 10×10 |
| No double-booking | property | ScheduledJob | 0 violations over 10k jobs |
| Calendar edges | unit | capacity materialization | open minutes match fixture |
| Gantt move | Playwright | move + propagate | persisted and dependents updated |
| Scale | performance | 5k ops | < 60 s, p95 Gantt load < 1 s |

Reconciliation: Σ booked minutes per resource (CapacityLoad) = Σ ScheduledJob minutes; production order start/end = min/max of its scheduled jobs.

### 4.23.10 Deployment and operations

Helm: `scheduling` (Go, 2 API replicas + 2 worker replicas, 4 CPU / 8 GB), `scheduler-opt` (Python OR-Tools, 1 replica, 8 CPU / 16 GB, scale-to-zero via KEDA on queue depth), `scheduling-db` (CloudNativePG 50 GB). Config: `SCHED_BOTTLENECK_PCT` (90), `SCHED_OPT_TIME_LIMIT_S` (60), `SCHED_HORIZON_DAYS` (365). Flags: `scheduling.cp-sat`, `scheduling.propagate-dependents`, `planning.finite-capacity`. Jobs: nightly site run 00:30 (before planning); capacity materialization daily. SLOs: 5k-op run < 60 s; Gantt window p95 < 1 s; move p95 < 400 ms. Alerts: run failure (page), optimizer OOM (warn). Rollback: prior digest; ScheduledJob rows versioned by run_id so a bad run is reverted by re-pointing to the previous run.

### 4.23.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| WrkCtrCapRes (AX) / KBED+KBEZ (SAP) | ScheduledJob | only for open orders; re-scheduled on first run, legacy dates kept as `firmed` if the order is Started |
| WrkCtrTable capacity/efficiency, WorkCalendar / CR02 capacity header, factory calendar | ResourceCapacity (materialized) | from w9 resources and calendars |
| ProdRouteJob dates / AFVV dates | ProductionJob planned dates | preserved for reference |

Not migrated: historical capacity reservations, PP/DS optimizer profiles, AX scheduling parameters (re-set in Passage).

### 4.23.12 Dependencies, risks, open questions

Depends on: w21 jobs/operations; w22 planned orders and pegging; w9 resources, capabilities, calendars.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Heuristic quality far from optimum on large instances | High | Low | CP-SAT windowing; planners can firm; measure lateness trend |
| Gantt performance in the browser | Medium | Medium | Canvas rendering, windowed fetch ≤ 5,000 bars |

Open questions: (1) Should optimization run automatically nightly or only on demand? Default on demand; decide by w23 mid-point. (2) Tool/fixture resources as secondary resources in w23 or w44 (EAM)? Default w44.

## 4.24 Wave 24: Process Manufacturing & Lean (Formulas, Batches, Kanban)

### 4.24.1 Scope and business context

Wave 24 extends production to two further modes. Process manufacturers (food, chemicals, pharma) create batch orders from formulas whose lines scale with batch size, consume ingredients by potency, produce co- and by-products with cost allocation, track batch attributes, shelf life and disposition, and handle catch-weight items with a dual quantity. Lean plants define value streams and production flows with activities, replenish through kanban rules (manufacturing and withdrawal; fixed, scheduled and event replenishment), work kanban cards on a board, and cost production flows by periodic backflush. Formulas and recipes are version-controlled through engineering change (w9). After this wave a second demo plant (food/chem line) runs a batch order and a kanban-driven cell. Out of scope: PI sheets with electronic signatures (w33 quality covers signatures), process control system integration beyond the IoT connector of w25, recipe scale-up optimization.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Rescaling a batch order in AX (PmfProdTable "Rescale batch") recomputes lines but silently rounds to the inventory unit precision and does not preserve percent totals; SAP COR2 quantity change re-reads the recipe and drops manual line edits | Scaling implemented as re-explosion; rounding residual not tracked | BR-24.1 scaling with residual assigned to the largest scalable line, side-by-side preview, manual edits preserved | Percent-controlled Σ = 100.0000 on 100% of batches; rescale 12 clicks → 2 |
| Potency adjustment is manual: AX requires the batch potency on InventBatch to be keyed before picking and the picking list does not recompute compensating lines automatically; SAP active-ingredient management needs batch characteristics plus custom proportion units | Potency not part of the consumption proposal | BR-24.2 proposal computes active and compensating quantities from recorded batch attributes | Potency re-keying 0 (attributes recorded once); consumption error < 0.1% |
| Co-product costing errors at End: AX TCA allocation misallocates when a co-product has zero standard cost; SAP apportionment structures (TKKAA) must be maintained per material and settlement KO88 fails silently with status messages | Allocation data in separate setup objects, no invariant check | Allocation normalized with residual rule and property test Σ = batch cost; preview before End | Allocation mismatches 0; End failures surfaced synchronously with reason |
| Kanban cards and boards are disconnected: AX kanban board (KanbanBoardScheduleView) refreshes manually and cannot be used on a tablet; SAP PK13N is a GUI-only status screen; Plex and Epicor Kinetic kanban rely on printed cards and scanner re-keying | Board is a list page over KanbanJob with no push updates | Touch board with SSE refresh, GS1 DataMatrix cards, event kanbans within 5 s | Card-to-board latency 5 min refresh → < 2 s; re-keying 0 |
| Shelf-life and disposition rules are enforced only at picking in AX (PdsShelfLife) and by batch status in SAP (MSC2N), so expired or on-hold batches appear in proposals | Rules applied post hoc rather than in the proposal query | Proposals exclude expired/on-hold batches; override requires duty + reason | Expired-batch consumption incidents → 0; audit reason on 100% of overrides |

### 4.24.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Formula | PmfFormula, BOMVersion with PmfFormulaSize, PmfFormulaLine (scalable, percent-controlled) | Master recipe (C201), material list in recipe, base quantity | Formula (BomVersion subtype), FormulaLine | full |
| Batch order | ProdTable with ProdType=Batch (PmfProdTable), batch order status = ProdStatus | Process order (COR1/COR2), AUFK with order category 40 | BatchOrder (ProductionOrder subtype) | full |
| Co-/by-products | PmfCoByProd, cost allocation (TCA, manual, none), by-product cost type | Co-product settlement rule (apportionment structure), by-product with negative qty | CoProductLine, allocation_method | full |
| Potency | PdsBatchAttrib, PdsBatchAttribItem, base attribute, potency unit, active-ingredient line type | Active ingredient management (batch characteristic, proportion unit) | BatchAttribute with `potency` role; consumption rule | full |
| Batch attributes / disposition | PdsBatchAttrib, PdsBatchAttributes on InventBatch, PdsDispositionCode | Batch classification (MSC1N characteristics), batch status, usage decision | BatchAttribute, InventoryBatch.disposition (w16 batch) | full |
| Shelf life | InventBatch expiry, best-before, shelf-advice, PdsShelfLife | Shelf-life expiration date (SLED), minimum remaining shelf life | shelf_life_days, best_before, expiry on batch | full |
| Catch weight | PdsCW, CW unit, nominal/min/max qty | Catch-weight management (S/4 CWM, parallel UoM) | CatchWeightQty on product and transactions | full |
| PI sheet / process instructions | n/a (route card notes) | PI sheets (CORA/CO60), control recipes, process messages | operation instructions on kiosk (w25); messages via events | partial — no XSteps |
| Production flow / value stream | LeanProductionFlow, LeanProductionFlowActivity, ValueStream (OMOperatingUnit) | PP-REM production version, product cost collector, MF50 planning table | ProductionFlow, FlowActivity, ValueStream (w6 OperatingUnit) | full |
| Kanban rules and kanbans | KanbanRule (fixed/scheduled/event), Kanban, KanbanJob, KanbanJobSchedule, kanban board | Control cycle PK01, kanban board PK13N, PK05 demand source, PK17 | KanbanRule, Kanban, KanbanJob | full |
| Backflush costing | Lean backflush costing, production flow costing (periodic) | Product cost collector (KKF6N), REM backflush MFBF, periodic settlement | FlowCostingPeriod | full |
| Repetitive planning table | n/a | MF50 | kanban board takt view | partial |

### 4.24.3 Functional requirements

- **FR-24.1 Formulas** — Must. The system shall define a Formula as a BomVersion with formula_size and FormulaLine fields: scalable (bool), percent_controlled (bool, lines sum to 100%), line_type (ingredient, active-ingredient, phantom, co-product, by-product), per_series/quantity.
  - Notes: Percent-controlled formulas validate Σ scalable line % = 100.00 ± 0.01. Formula version changes go through ECO (w9); a batch order snapshots the formula at creation.
- **FR-24.2 Batch orders** — Must. The system shall create BatchOrder from a formula, scaling scalable lines proportionally to batch size (non-scalable lines fixed), with the same state machine and journals as w21 plus co-/by-product RAF lines.
- **FR-24.3 Potency** — Must. The system shall consume active-ingredient lines by potency: consumed qty = planned qty × (standard potency / actual batch potency), with compensating lines (fillers) adjusted so the batch size is kept.
  - Notes: Potency attribute defined per item (base attribute, target value, min/max); batch potency recorded at receipt or by quality (w33) on the InventoryBatch. Batches outside min/max are not pickable for the line.
- **FR-24.4 Co-/by-product costing** — Must. The system shall allocate batch cost to co-products by method: none (all cost to main item), manual (fixed %), total cost allocation (TCA: Σ co-product standard cost shares), by quantity; by-products reduce cost by burden: per series, per quantity, percent, or recycled (negative issue).
  - Notes: Allocation percentages are normalized to sum 100.0000; rounding residual goes to the main product; property test Σ allocated = batch cost.
- **FR-24.5 Batch attributes and disposition** — Must. The system shall record BatchAttribute values per inventory batch (numeric/text/enum/date with min/max/target), block batches by disposition code (available, on-hold, rejected, quarantine) and restrict picking by customer-specific attribute ranges (sales linkage in w27).
- **FR-24.6 Shelf life** — Must. The system shall compute best_before = manufacturing date + best_before_days and expiry = manufacturing date + shelf_life_days at RAF, support shelf-advice (advice days before expiry) and FEFO picking on batch orders, and block consumption of expired batches unless duty *Consume expired batch* is held.
- **FR-24.7 Catch weight** — Must. The system shall support catch-weight items with inventory quantity in the CW unit (e.g. cases) and a nominal weight with min/max, carrying both quantities on every inventory and production transaction; cost is per inventory (weight) quantity.
  - Notes: RAF requires actual weight per CW unit; variance outside min/max rejected; sales/purchase price is per weight (w26 pricing uses nominal until actual known).
- **FR-24.8 Production flows** — Must. The system shall model ValueStream → ProductionFlow (versioned, with takt time, activation dates) → FlowActivity (process or transfer; cycle time, resource/work cell, input/output locations, picking activities) and validate connectivity (every process activity has an output).
- **FR-24.9 Kanban rules** — Must. The system shall define KanbanRule with type (manufacturing/withdrawal), replenishment strategy (fixed: number of kanbans and product quantity; scheduled: via planning as planned kanbans; event: generated from sales line, BOM line, kanban line, minimum stock), lead time, fixed quantity, activity sequence, and automatic planning/firming.
- **FR-24.10 Kanbans and board** — Must. The system shall create Kanban (card) with KanbanJobs per activity, states Not planned → Planned → Prepared → In progress → Completed → Emptied (card returns), print cards with barcodes (GS1 DataMatrix), and provide a board per work cell (columns by job status) with drag completion and quantity/scrap entry.
- **FR-24.11 Kanban events** — Must. Event kanbans shall be generated within 5 s of a triggering sales line (w27 or fixture) or BOM line of a production order, and withdrawal kanbans create transfer jobs that post inventory transfers on completion.
- **FR-24.12 Lean backflush costing** — Should. The system shall cost production flows periodically: per costing period, issue materials at standard/weighted-average on kanban completion, accumulate conversion cost by activity (cycle time × cost rate), and settle at period end with variances to the flow's posting profile.
- **FR-24.13 Kanban manufacturing orders** — Must. Manufacturing kanbans shall create a lightweight ProductionOrder of type `kanban` (no routing jobs; single process activity) that reports as finished on kanban completion.
- **FR-24.14 Takt and board metrics** — Could. The board shall show takt vs cycle, WIP count per column and kanbans overdue (planned < now).

### 4.24.4 Data model

**FormulaLine**: id, bom_version_id, line_number, item_id, line_type (enum), qty (numeric 18,6), percent (numeric 9,4), scalable (bool), percent_controlled (bool), potency_base_item (bool), unit_id, scrap_pct, is_co_product (bool), allocation_pct (numeric 7,4), by_product_cost_type (enum: per-series, per-quantity, percent, recycled).

**BatchOrder** (extends ProductionOrder): formula_size (numeric), batch_size (numeric), scale_factor (numeric 18,9, computed), planning_item_id (for co-product planning), potency_mode (enum), manufacturing_date, batch_number (text, generated at RAF via `batch` sequence).

**CoProductLine**: id, order_id, item_id, planned_qty, raf_qty, allocation_method (enum: none, manual, tca, quantity), allocation_pct, allocated_cost (numeric 18,4), is_by_product (bool), by_product_cost_type.

**BatchAttribute**: id, item_id, attribute_id, data_type (numeric/text/enum/date), target, min, max, role (enum: potency, generic), unit_id; **BatchAttributeValue**: batch_id (w16 InventoryBatch), attribute_id, value_numeric, value_text, value_date, recorded_at, recorded_by.

**CatchWeightQty** (on product): cw_unit_id, nominal_qty (numeric 18,6), min_qty, max_qty; on InventoryTransaction and journal lines: cw_qty (numeric 18,6) alongside qty.

**ProductionFlow**: id, value_stream_id (OperatingUnit), version, name, site_id, takt_time_s (int), status (draft/active/expired), active_from, active_to; **FlowActivity**: id, flow_id, name, kind (process/transfer), cycle_time_s, work_cell_resource_id, input_location_id, output_location_id, predecessor_ids (uuid[]), picking_required (bool).

**KanbanRule**: id, item_id, site_id, warehouse_id, location_id, rule_type (manufacturing/withdrawal), replenishment (fixed/scheduled/event), fixed_qty, number_of_kanbans (int), min_kanbans, max_kanbans, lead_time_h, event_source (sales/bom/kanban/min-stock), auto_plan (bool), auto_firm (bool), flow_activity_sequence (uuid[]), effective_from, effective_to.

**Kanban**: id, rule_id, card_number, qty, cw_qty, state (enum), handling_units (int), planned_start, due, production_order_id / transfer_id, event_ref_type, event_ref_id, printed_at; **KanbanJob**: id, kanban_id, activity_id, sequence, state (not-planned/planned/prepared/in-progress/completed), start, end, reported_qty, scrap_qty.

**FlowCostingPeriod**: id, flow_id, period_start, period_end, status (open/settled), material_cost, conversion_cost, output_value, variance, journal_entry_id.

Invariants: Σ FormulaLine.percent for percent-controlled scalable lines = 100.0000; Σ CoProductLine.allocated_cost + main-item cost = batch actual cost (after by-product burden); Kanban state transitions only forward except `Emptied → Not planned` on card reuse; fixed-rule active kanbans count ≤ number_of_kanbans; cw_qty present iff the item is catch-weight.

Indexing: Kanban (rule_id, state); KanbanJob (activity_id, state, planned_start) for the board; BatchAttributeValue (batch_id); FormulaLine (bom_version_id, line_number).

### 4.24.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/production/batch-orders | Create batch order from formula with batch_size | Idempotency-Key | Maintain batch orders |
| POST | /api/v1/production/batch-orders/{id}:rescale | Change batch size before Started | Idempotency-Key | Maintain batch orders |
| POST | /api/v1/production/batch-orders/{id}/co-products:allocate | Preview/apply allocation | Idempotency-Key | Post production journals |
| PUT | /api/v1/inventory/batches/{id}/attributes | Record attribute values | full replace | Maintain batch attributes |
| POST | /api/v1/inventory/batches/{id}:disposition | Change disposition | Idempotency-Key | Maintain batch disposition |
| POST | /api/v1/lean/flows | Create/version production flow | Idempotency-Key | Maintain production flows |
| POST | /api/v1/lean/flows/{id}:activate | Validate and activate | Idempotency-Key | Maintain production flows |
| POST | /api/v1/lean/kanban-rules | Create rule | Idempotency-Key | Maintain kanban rules |
| POST | /api/v1/lean/kanbans:generate | Generate kanbans for a rule (fixed/event) | Idempotency-Key | Maintain kanbans |
| POST | /api/v1/lean/kanban-jobs/{id}:complete | Complete job with qty/scrap/handling units | Idempotency-Key | Work kanban board |
| GET | /api/v1/lean/board?workCellId | Board view (jobs by column) | n/a | Work kanban board |
| POST | /api/v1/lean/flows/{id}/costing-periods/{p}:settle | Settle period | Idempotency-Key | Settle production flows |

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `passage.production.batch-order.created.v1` | batch order created | orderId, formulaVersionId, batchSize, scaleFactor, coProducts[] | planning, analytics |
| `passage.production.batch.attributes-recorded.v1` | attribute values saved | batchId, itemId, values[{attributeId, value}], potency | quality (w33), sales (w27) |
| `passage.production.co-product.allocated.v1` | End of batch order | orderId, lines[{itemId, method, pct, amount}], byProductBurden | ledger, costing |
| `passage.lean.kanban.created.v1` | kanban generated | kanbanId, ruleId, qty, eventRefType, eventRefId, due | production, shopfloor |
| `passage.lean.kanban.state-changed.v1` | job/kanban transition | kanbanId, jobId, from, to, qty, scrap, handlingUnits | production, inventory, shopfloor |
| `passage.lean.flow-period.settled.v1` | period settled | flowId, period, materialCost, conversionCost, outputValue, variance, journalEntryId | ledger, analytics |

### 4.24.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Formula designer | Add lines, toggle scalable/percent, define co/by-products, validate 100%, submit ECO | Line, item, type, qty/%, scalable, potency base, allocation % | Maintain formulas |
| Batch order detail | Rescale, estimate, release, RAF with co-products, attributes tab | Batch size, scale factor, co-product lines, batch number, dates, attributes | Maintain batch orders |
| Batch attributes / disposition | Record values, change disposition, view history | Attribute, value, target/min/max, pass flag, disposition | Maintain batch attributes |
| Production flow designer | Canvas of activities, connect, set cycle times, activate | Activity, kind, cycle time, work cell, locations | Maintain production flows |
| Kanban rules | Create fixed/scheduled/event rules, compute number of kanbans | Item, type, replenishment, qty, kanbans, lead time | Maintain kanban rules |
| Kanban board (work cell) | Drag job between columns, complete with qty, print cards, filter | Columns Not planned/Planned/Prepared/In progress/Completed; card: item, qty, due, order | Work kanban board |
| Flow costing | Open/settle period, variance drill | Period, material, conversion, output, variance | Settle production flows |

Interaction rules: formula percent cells show a running total with red when ≠ 100; board is touch-friendly (min 44 px targets) and refreshes via SSE; card print uses the w5 document service with GS1 DataMatrix encoding `(240)kanbanId`; rescale shows before/after line quantities side-by-side.

### 4.24.7 Business rules and edge cases

- **BR-24.1 Scaling.** scale_factor = batch_size / formula_size; scalable line qty = formula qty × scale_factor; non-scalable lines unchanged; percent-controlled lines qty = batch_size × percent/100; rounding to UoM precision with residual added to the largest scalable line so Σ = batch_size.
- **BR-24.2 Potency consumption.** For an active-ingredient line with standard potency P_s and picked batch potency P_a: qty = planned × P_s / P_a; the compensating line qty = planned_comp − (qty − planned); negative compensating qty is rejected with 422 `POTENCY_COMPENSATION_NEGATIVE`. Example: 100 kg at 50% standard, batch at 40% → 125 kg; filler reduced by 25 kg.
- **BR-24.3 Co-product allocation (TCA).** share_i = std_cost_i × raf_qty_i / Σ_j std_cost_j × raf_qty_j; allocated_i = round(batch_cost_after_by_products × share_i, 4); residual to the main product. Example: batch cost USD 10,000.00; main A (std 40, 200 units = 8,000), co-product B (std 20, 100 = 2,000): A gets 8,000.00, B 2,000.00. By-product C per-quantity burden 1.50 × 50 = 75.00 reduces batch cost first (9,925.00 allocated 7,940.00 / 1,985.00; C received at 75.00).
- **BR-24.4 Shelf life.** best_before/expiry computed at RAF from manufacturing_date (defaults to RAF date); if the formula has `inherit-shortest-shelf-life`, the output expiry = min(ingredient batch expiries) when earlier.
- **BR-24.5 Catch-weight RAF.** cw_qty integer ≥ 1; qty (weight) must satisfy min_qty × cw_qty ≤ qty ≤ max_qty × cw_qty; cost per weight unit.
- **BR-24.6 Kanban state machine.** Not planned → Planned (date set) → Prepared (picking complete) → In progress (first job started) → Completed (last job completed; manufacturing: RAF posted; withdrawal: transfer posted) → Emptied (consumed; card returns to Not planned for fixed rules, or is deleted for event rules). Backward moves only Planned → Not planned. Illegal moves return 409.
- **BR-24.7 Fixed rule kanban count.** number_of_kanbans = ⌈(daily demand × lead_time_days × (1 + safety%)) / fixed_qty⌉ as a suggestion; rule cannot have more active (non-Emptied) kanbans than number_of_kanbans.
- **BR-24.8 Event kanban quantity.** Event kanbans for demand D create ⌈D / fixed_qty⌉ kanbans; last kanban is full-size (overproduction allowed) unless `exact_qty` flag.
- **BR-24.9 Flow period settlement.** variance = material_cost + conversion_cost − output_value; settled at period end only if all kanbans with jobs in the period are Completed or Emptied, else 409 `FLOW_PERIOD_OPEN_KANBANS` listing them.
- **BR-24.10 Expired batch.** A picking line selecting a batch with expiry < posting date fails unless duty held, in which case the audit log records the override reason (required, ≥ 10 chars).

### 4.24.8 Acceptance criteria

- **AC-24.1** (FR-24.2, BR-24.1) Given a 1,000 kg formula with a 50 kg non-scalable line, when I create a 2,500 kg batch, then scalable lines are ×2.5 and the non-scalable line stays 50 kg.
- **AC-24.2** (FR-24.3) Given the BR-24.2 example, when the picking list proposes consumption, then the active line is 125 kg and the filler is reduced by 25 kg.
- **AC-24.3** (FR-24.4) Given the BR-24.3 fixture for each allocation method, when the order ends, then Σ allocated cost + by-product burden = batch actual cost exactly.
- **AC-24.4** (FR-24.6) Given an expired ingredient batch, when picked without the duty, then 422 `BATCH_EXPIRED`.
- **AC-24.5** (FR-24.7) Given a catch-weight item (nominal 10 kg, min 9, max 11), when I RAF 10 cases at 95 kg, then the post fails; at 102 kg it succeeds with qty 102 and cw_qty 10.
- **AC-24.6** (FR-24.11) Given an event rule with fixed_qty 20, when a sales line for 50 arrives, then 3 kanbans exist within 5 s and completing them through the board posts RAF of 60.
- **AC-24.7** (FR-24.10) Given a kanban in Planned, when I drag it to Completed, then 409 and the board reverts.
- **AC-24.8** (FR-24.12) Given a flow period with 10 completed kanbans, when settled, then the journal entry balances and the variance equals material + conversion − output.
- **AC-24.9** (FR-24.1) Given a percent-controlled formula summing to 99.5%, when I activate it, then validation fails.
- **AC-24.10** (FR-24.5) Given a batch with disposition on-hold, when a batch order picks it, then the batch is excluded from proposals.

### 4.24.9 Test plan and fixtures

Fixtures: `pmf-potency-20` (standard/actual potency pairs with expected consumption); `pmf-coby-12` (allocation methods × by-product cost types); `pmf-catchweight-10`; `lean-flow-demo` (3-activity flow, 2 fixed rules, 1 event rule); `lean-kanban-sim` (sales lines → event kanbans → board completion).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Potency | unit | consumption proposal | 20/20 |
| Allocation sums | property | allocation | Σ = cost for random costs/quantities |
| Catch weight | unit | RAF validation | min/max enforced |
| Kanban state machine | property | transitions | illegal rejected |
| Event kanban latency | integration | event→kanban | p95 < 5 s |
| Board E2E | Playwright | board | kanbans complete, RAF posted |
| Flow settlement | contract | ledger | balanced entry |

Reconciliation: Σ co-product allocated cost = batch cost; Σ kanban RAF = inventory receipts from kanban orders; flow period variance = GL flow variance account.

### 4.24.10 Deployment and operations

Helm: `production` extended (flag `prod.process`), `lean` (Go, 2 replicas), `lean-db` (CloudNativePG 50 GB). Config: `LEAN_EVENT_KANBAN_SLA_S` (5), `PMF_POTENCY_TOLERANCE_PCT` (0.5). Flags: `prod.process`, `prod.catch-weight`, `lean.kanban`, `lean.flow-costing`. Jobs: shelf-advice notification daily 06:00; flow period auto-settle (optional) monthly. SLOs: board load p95 < 500 ms; kanban generation p95 < 5 s. Alerts: event-kanban backlog > 100 (warn); flow settlement failure (page). Rollback: prior digest; flags off disables the second plant's workspaces without data loss.

### 4.24.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| PmfFormula/BOMVersion+BOM lines (AX) / PLKO+PLPO+STPO recipe (SAP) | Formula, FormulaLine | formula size → base quantity; percent lines derived from quantities when SAP recipe is in base units |
| PmfCoByProd / co-product apportionment (TKKAA) | CoProductLine on formula | TCA percentages from apportionment structure |
| PdsBatchAttrib, PdsBatchAttribItem, PdsBatchAttributes / batch classification (AUSP, CABN) | BatchAttribute, BatchAttributeValue | characteristic → attribute; numeric/char/date types |
| InventBatch expiry/best-before / MCH1 VFDAT, HSDAT | InventoryBatch (w16) fields | already migrated in w16; this wave adds attributes |
| PdsCW / CWM units | CatchWeightQty | nominal/min/max |
| LeanProductionFlow, LeanProductionFlowActivity / production versions, REM profiles | ProductionFlow, FlowActivity | active versions only |
| KanbanRule, Kanban, KanbanJob / PKHD, PKPS | KanbanRule, Kanban (active cards) | completed/emptied cards not migrated |

Validation: formula explosion of 100 sampled batches matches legacy consumption to 0.01%; active kanban count per rule equals legacy. Not migrated: process-order history, PI sheet archives, SAP XSteps.

### 4.24.12 Dependencies, risks, open questions

Depends on: w21 order engine; w16 batches and FEFO; w9 ECO; w6 operating units (value streams); w22 scheduled kanbans via planning.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Potency/attribute semantics vary by industry | High | Medium | Configurable role model; consultant review |
| Lean costing conflicts with order-based WIP reconciliation | Medium | High | Kanban orders are ProductionOrders — same WIP accounts; flow periods settle through the same End logic |

Open questions: (1) Should by-product `recycled` (negative issue) be supported in v1? Default yes. (2) Board per work cell vs per flow? Default work cell; decide by w24 kickoff.

## 4.25 Wave 25: Shop Floor Execution, Time Registration & IoT

### 4.25.1 Scope and business context

Wave 25 puts production on the floor. Operators use a touch kiosk (PWA) to clock in and out, start and stop jobs (including bundling several jobs), report good and scrap quantities with reason codes, run quality checks and view instructions and drawings, and keep working for up to 30 minutes offline. Registrations are turned into pay lines (overtime, shift premiums, flex) through profiles, approved by supervisors and exported to payroll (w43). Machines publish counts, states and downtime over OPC-UA/MQTT through an edge connector into Kafka; the shopfloor service computes OEE per resource in 15-minute buckets and drives andon alerts. Labor and machine time flows into the production order's route-card postings. This is the phase-exit wave: the demo plant manufactures a configured product from planned order to finished goods with full costing. Out of scope: payroll calculation itself (w43), quality orders and usage decisions (w33; kiosk checks are simple pass/fail capture), maintenance work orders from machine events (w44 consumes the events), label printing beyond kanban cards (w32).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Job-card re-keying: AX JmgTermReg registrations are "calculated" and "transferred" by a supervisor before ProdJournalRoute lines exist, so route cards are re-keyed or posted a day late; SAP CO11N confirmations are typed by a clerk from paper tickets; Infor LN and Epicor Kinetic MES data collection posts labor in a nightly batch | Registration and production posting are separate modules joined by a transfer job | Registration stop generates and posts the job-card journal (option immediate); reversal on edit | Labor posting latency 24 h → < 5 s; re-keyed tickets 0 |
| Kiosk needs the AOS: AX terminal registration (JmgTermReg form) is a thick client; D365 production floor execution requires connectivity; Plex is cloud-only with no offline mode | No client-side queue or idempotent replay | PWA with IndexedDB queue, client UUIDs, conflict flags | Lost registrations during outages → 0 over a 30-min disconnect |
| Machine data lives outside the ERP: AX has no native machine integration; SAP MII/PCo is a separate licensed stack with custom BLS transactions; Plex offers machine connectivity only through its own gateway | Proprietary connectors and point-to-point integration | `iot-edge` (OPC-UA/MQTT) → Kafka CloudEvents; any system subscribes to `shopfloor.machine-events` | Time to connect a new machine 2 weeks → 1 day; integration middleware 0 |
| OEE is computed in spreadsheets or a BI tool from exported confirmations; SAP DM OEE requires its own master data; Epicor Advanced MES OEE updates per shift | No event-level counts in the ERP; ratios averaged incorrectly across shifts | OeeBucket per 15 min from events, ISO 22400 formula, roll-up by summing minutes/counts | OEE visibility per shift → per 15 min with p95 lag < 5 s |
| Pay calculation surprises: AX JmgPayTable overtime depends on profile day types supervisors do not see, so corrections happen after payroll export; SAP time evaluation (RPTIME00) is a batch run with cryptic log | Rules engine hidden from the people approving | PayLine per rule with source registrations shown on the approval screen; recalculation on edit | Post-export corrections 5%/period → < 0.5%; approval time per team 2 h → 15 min |

### 4.25.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Job registration terminal | JmgTermReg (terminal registration form), JmgJobTable, JmgRegistration, D365 production floor execution | SAP MII / Digital Manufacturing shop floor UI, PP-PDC terminals, CO11N | Kiosk PWA, `shopfloor-bff`, TimeRegistration | full |
| Clock in/out, absence | JmgStampJournalTable (raw), JmgTmpRegistration, JmgAbsenceCode, JmgProfileTable | Time events (CATS/PT), HR time types | Registration kinds clock-in/out, absence; ProfileDay | full |
| Job bundling | Jmg bundle jobs (JmgJobBundle) | n/a (multi-order confirmation) | bundle_id on registrations, cost split by quantity/hours | full |
| Feedback (qty/scrap) | JmgJobTable feedback, ProdJournalRoute from Jmg, error cause codes | CO11N yield/scrap/rework, reason for variance (AFRU.GRUND) | job-card journals auto-generated; ScrapReason | full |
| Pay calculation | JmgPayTable, pay agreements (JmgPayAgreement), overtime/flex, pay-item transfer | HR time evaluation (RPTIME00), wage types | PayLine, PayRule engine | full |
| Approval and transfer | Jmg calculate → approve → transfer to payroll (JmgPayTransfer) | CATS approval, transfer to HR | Approve + export (w43 payroll) | full |
| Machine integration | n/a native (IoT Intelligence for D365, Sensor Data Intelligence) | SAP MII PCo/OPC, Plant Connectivity, SAP DM | `iot-edge` connector (OPC-UA/MQTT) → Kafka | improved |
| OEE | D365 Sensor Data Intelligence (partial) | SAP DM OEE module | OeeBucket per resource × 15 min; ISO 22400 KPIs | full |
| Downtime reasons | Jmg indirect activities | PM notification/downtime reasons | DowntimeRecord with reason code | full |
| Andon | n/a | SAP DM andon | AndonAlert via event + kiosk banner | improved |
| Instructions/drawings | Route notes, document attachments (DocuRef) | PI sheets, DMS documents on operation | Operation documents (w5 attachments) on kiosk | full |
| Offline kiosk | n/a (requires AOS) | n/a | PWA with IndexedDB queue | improved |

### 4.25.3 Functional requirements

- **FR-25.1 Kiosk login** — Must. The system shall let a worker log in on a shared kiosk by badge scan (GS1 barcode/NFC) or PIN mapped to a Keycloak user, with session timeout 10 min idle and switch-user in ≤ 2 taps.
- **FR-25.2 Clock in/out and absence** — Must. The system shall record clock-in/out and absence registrations with the device id and server timestamp, applying profile rounding (BR-25.2).
- **FR-25.3 Job start/stop and bundling** — Must. The system shall list dispatchable jobs for the worker's resource/work cell (from w23 ScheduledJob, ordered by start), allow start/stop of one job or a bundle, and split bundled time by quantity reported (default) or equally.
  - Notes: A worker may have one open job/bundle at a time; starting another auto-stops the previous. Jobs in status `finished` are hidden unless the supervisor reopens.
- **FR-25.4 Feedback** — Must. The system shall capture good qty, scrap qty with reason code, and "finished" flag per job; on stop it generates a job-card journal (w21) with hours and quantities and posts it (configurable: immediately or at approval).
- **FR-25.5 Quality checks** — Should. The system shall present check items attached to the operation (text/numeric/pass-fail with limits) and record results; a failed mandatory check blocks `finished` until a supervisor override.
- **FR-25.6 Instructions and documents** — Must. The kiosk shall show operation instructions (markdown) and attachments (PDF, images, video ≤ 50 MB) cached for offline use.
- **FR-25.7 Pay calculation** — Must. The system shall calculate PayLines per worker-day from registrations using the worker's profile (day types, shift windows, breaks) and pay agreement rules: base, overtime after daily/weekly thresholds, shift premium, flex balance.
  - Notes: Defaults: overtime > 8.00 h/day at 1.5×, > 40.00 h/week at 1.5×, Sunday 2.0×; rounding per BR-25.2; max 24 h/day validation.
- **FR-25.8 Approval and export** — Must. Supervisors shall approve per worker-day (bulk by team), with edits audited; approved days are locked and exported as `passage.shopfloor.payday.approved.v1` for w43 and as CSV.
- **FR-25.9 IoT ingestion** — Must. The `iot-edge` connector shall subscribe to OPC-UA nodes (via `gopcua`) and MQTT topics (Mosquitto/EMQX, QoS 1), normalize to the MachineEvent schema (BR-25.4), buffer locally (SQLite ring, 24 h), and publish to Kafka topic `shopfloor.machine-events` with idempotent producer and per-machine monotonic sequence.
- **FR-25.10 OEE** — Must. The system shall compute OEE per resource per 15-minute bucket and shift (BR-25.5) from machine events and planned time from the w23 schedule/calendar, with dashboard lag p95 < 5 s.
- **FR-25.11 Downtime and andon** — Must. The system shall open a DowntimeRecord when a machine reports DOWN or no count for > `idle_threshold_s` (default 300), require a reason code within the kiosk, and raise an AndonAlert (kiosk banner + notification) on downtime > 10 min, scrap rate > 5% in a bucket, or quality fail.
- **FR-25.12 Offline kiosk** — Must. The PWA shall queue registrations and feedback in IndexedDB while disconnected (≥ 30 min, ≤ 5,000 actions), replay in order on reconnect with client-generated UUIDs for idempotency, and resolve conflicts per BR-25.7.
- **FR-25.13 Machine-to-job linkage** — Should. Machine counts shall be attributed to the job currently running on that resource (from registrations or the schedule), and may auto-fill good/scrap quantities on the kiosk for operator confirmation.
- **FR-25.14 Supervisor view** — Must. A supervisor workspace shall show who is clocked in, current jobs, open downtimes, OEE tiles, and pending approvals.

### 4.25.4 Data model

**Worker** (shopfloor projection of Party/Employee w7): id, party_id, badge_id (unique), pin_hash, default_resource_id, work_cell_id, profile_id, pay_agreement_id, cost_rate (numeric 18,4), active.

**TimeRegistration**: id (client UUID), tenant_id, worker_id, kind (enum: clock-in, clock-out, job-start, job-stop, absence, indirect), job_id (nullable), bundle_id (nullable), qty_good, qty_scrap, scrap_reason_id, started_at, stopped_at (timestamptz), server_received_at, device_id, offline (bool), hours (numeric 10,4, computed), status (enum: raw, calculated, approved, exported), journal_id (w21 job card), note.

**ProfileDay**: id, profile_id, day_type, shift_start, shift_end, break_windows (jsonb), rounding_minutes (int), flex_allowed (bool), tolerance_before_min, tolerance_after_min.

**PayLine**: id, worker_id, work_date, pay_type (enum: base, overtime-1.5, overtime-2.0, shift-premium, flex-in, flex-out, absence-paid, absence-unpaid), hours, rate_multiplier, amount (numeric 18,4), currency, source_registration_ids (uuid[]), approved_by, approved_at, export_batch_id.

**MachineEvent**: id, machine_id, resource_id, seq (bigint), kind (enum: count, state, downtime, alarm, measurement), ts (timestamptz, device), received_at, good_count, reject_count, state (RUNNING/IDLE/DOWN/SETUP/OFF), reason_code, measurement_name, measurement_value, unit, job_id (attributed), payload (jsonb).

**OeeBucket**: resource_id, bucket_start (15 min), shift_id, planned_minutes, run_minutes, down_minutes, ideal_cycle_s, total_count, good_count, availability, performance, quality, oee (numeric 7,4 each), source_events (int).

**DowntimeRecord**: id, resource_id, started_at, ended_at, reason_code (nullable until coded), category (planned/unplanned), job_id, worker_id, andon_raised (bool).

Invariants: for each worker at most one open job-start without job-stop; PayLine hours per worker-day ≤ 24.00; OeeBucket.run_minutes + down_minutes ≤ planned_minutes; MachineEvent (machine_id, seq) unique; approved PayLines are immutable (new lines + reversal on correction).

Indexing: TimeRegistration (worker_id, started_at), (job_id), unique id (client UUID); MachineEvent partitioned by month (TimescaleDB-free: native range partitions), index (resource_id, ts); OeeBucket PK (resource_id, bucket_start); retention: raw MachineEvent 90 days in Postgres, forever in Iceberg (w46); OeeBucket 3 years.

### 4.25.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/shopfloor/sessions:badge | Kiosk login by badge/PIN | n/a | Kiosk login |
| POST | /api/v1/shopfloor/registrations | Create registration(s), batch of ≤ 500 (offline replay) | client UUID per item | Register time |
| GET | /api/v1/shopfloor/jobs?workerId&resourceId&status | Dispatch list | n/a | Register time |
| POST | /api/v1/shopfloor/jobs/{id}:feedback | Qty good/scrap/finished, checks | client UUID | Register time |
| GET | /api/v1/shopfloor/operations/{id}/instructions | Instructions + attachment URLs (signed, cacheable) | n/a | Register time |
| POST | /api/v1/shopfloor/pay:calculate | Calculate pay lines for worker-days | Idempotency-Key | Calculate time registrations |
| POST | /api/v1/shopfloor/pay:approve | Approve worker-days (bulk) | Idempotency-Key | Approve time registrations |
| POST | /api/v1/shopfloor/pay:export | Export batch (CSV + event) | Idempotency-Key | Export time to payroll |
| GET | /api/v1/shopfloor/oee?resourceIds&from&to&bucket=15m | OEE series | n/a | View shop floor |
| POST | /api/v1/shopfloor/downtimes/{id}:code | Assign reason | Idempotency-Key | Register time |
| GET | /api/v1/shopfloor/andon?siteId | Open alerts | n/a | View shop floor |
| POST | /api/v1/shopfloor/machines | Register machine → resource mapping, tags | Idempotency-Key | Maintain shop floor setup |

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `passage.shopfloor.registration.posted.v1` | registration stored | registrationId, workerId, kind, jobId, hours, qtyGood, qtyScrap, offline | production (job card), analytics |
| `passage.shopfloor.machine.event.v1` | edge publish | see BR-25.4 | shopfloor OEE, production, maintenance (w44) |
| `passage.shopfloor.oee.bucket-updated.v1` | bucket recomputed | resourceId, bucketStart, availability, performance, quality, oee | dashboards (w46) |
| `passage.shopfloor.andon.raised.v1` | alert condition | alertId, resourceId, kind, severity, jobId, startedAt | notifications, kiosk |
| `passage.shopfloor.payday.approved.v1` | approval | workerId, workDate, payLines[{type, hours, multiplier, amount}], approvedBy | payroll (w43) |

### 4.25.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Kiosk home | Badge in/out, my jobs, start/stop, bundle, report, downtime reason, andon | Worker, clock status, current job, elapsed, qty good/scrap, offline indicator | Kiosk login / Register time |
| Job detail (kiosk) | View instructions/drawings, checks, report | Operation, order, item, qty planned/reported, checks | Register time |
| Supervisor board | Who is in, current jobs, downtimes, OEE tiles, approve days | Worker, job, elapsed, resource state, OEE A/P/Q, pending | Approve time registrations |
| Time approval | Calculate, edit with reason, approve, export | Worker, date, registrations, pay lines, flags (missing clock-out) | Approve time registrations |
| OEE dashboard | Resource/period filter, drill to downtime and buckets | OEE, A, P, Q, top losses (Pareto) | View shop floor |
| Machine setup | Map machine → resource, tags, ideal cycle time, thresholds | Machine, resource, protocol, node ids/topics, cycle s | Maintain shop floor setup |

Interaction rules: kiosk targets ≥ 48 px, no hover-only affordances, numeric keypad for quantities, high-contrast theme, every action confirmed in ≤ 300 ms (optimistic UI); offline banner shows queued count; supervisor edits require a reason; OEE dashboard uses the w46 dashboard type when available, otherwise the Grafana panel embedded via the shell.

### 4.25.7 Business rules and edge cases

- **BR-25.1 Time attribution.** Hours for a job registration = (stopped_at − started_at) minus breaks that fall inside per the profile; bundle hours split by Σ qty_good (fallback equal split); hours rounded to 0.01 h on the job-card journal.
- **BR-25.2 Rounding and tolerances.** Clock-in within tolerance_before_min (default 10) of shift start rounds to shift start; clock-out within tolerance_after_min (default 5) rounds to shift end; otherwise round to nearest rounding_minutes (default 1, max 15).
- **BR-25.3 Missing clock-out.** At shift end + 4 h a missing clock-out auto-closes at shift end with flag `missing-clock-out`; the day cannot be approved until a supervisor confirms or edits.
- **BR-25.4 Machine event schema.** CloudEvents envelope `type: passage.shopfloor.machine.event.v1`, `source: urn:passage:iot-edge:<site>:<edgeId>`, `subject: <machineId>`, `id: <machineId>-<seq>`, `time: <device ts RFC 3339>`, `datacontenttype: application/json`, `data`: `{ "machineId": "M-101", "resourceId": "uuid", "seq": 184233, "kind": "count|state|downtime|alarm|measurement", "ts": "2026-10-21T08:15:03.250Z", "goodCount": 12, "rejectCount": 1, "state": "RUNNING", "reasonCode": null, "measurement": {"name": "temp_c", "value": 71.4, "unit": "Cel"}, "jobId": null, "tags": {"line": "L1"} }`. Counts are deltas since the previous count event; absolute counters are converted at the edge with rollover detection (32-bit). Events older than 7 days or with seq ≤ last seen are dropped and counted in a metric.
- **BR-25.5 OEE formula (ISO 22400 aligned).** Availability = run_minutes / planned_minutes; Performance = (ideal_cycle_s × total_count / 60) / run_minutes, capped at 1.0 with an over-rate flag; Quality = good_count / total_count (1.0 if total = 0); OEE = A × P × Q. Planned minutes = scheduled open minutes minus planned downtime (category planned). Example shift: planned 480 min, unplanned down 60 → A = 0.875; ideal cycle 60 s, total 380 → P = 380 / 420 = 0.9048; good 361 → Q = 0.95; OEE = 0.7522. Buckets roll up to shift/day by summing minutes and counts, not by averaging ratios.
- **BR-25.6 Idle detection.** No count event for idle_threshold_s while state = RUNNING flips the bucket to IDLE from the last count; a later count closes the idle gap.
- **BR-25.7 Offline conflict resolution.** Replayed registrations are accepted in client timestamp order; a job-start for a job that was finished server-side while offline is accepted but flagged `job-finished-conflict` for supervisor review; a duplicate client UUID is a no-op; clock skew > 5 min between device and server is corrected by the offset measured at reconnect and recorded.
- **BR-25.8 Pay thresholds.** Weekly overtime counts hours after 40.00 across the pay week (profile-defined start day); daily and weekly overtime do not double-count: hours already paid at 1.5× daily are excluded from the weekly threshold calculation.
- **BR-25.9 Posting to production.** Job-card journals from registrations post at cost_rate of the worker (if set) else the resource's cost category; a registration edited after posting creates a reversing journal and a new one.
- **BR-25.10 Andon de-duplication.** One open AndonAlert per (resource, kind); re-raising extends it; auto-closes when the condition clears for 5 min.

### 4.25.8 Acceptance criteria

- **AC-25.1** (FR-25.3, 25.4, BR-25.1) Given 6 operators on a shift with the fixture schedule, when they clock jobs on the kiosk (Playwright) and the day is approved, then each production order's labor cost = Σ hours × worker cost rate to the cent.
- **AC-25.2** (FR-25.7) Given a worker registering 09:00–19:30 with a 30-min break on a Tuesday, then pay lines are base 8.00 h and overtime-1.5 2.00 h.
- **AC-25.3** (FR-25.9) Given a simulated machine publishing 10,000 events/min for 60 min with a Kafka broker restart at minute 30, then every seq is present in MachineEvent and no duplicates exist.
- **AC-25.4** (FR-25.10, BR-25.5) Given the BR-25.5 shift example, then the shift OEE reads 0.7522 and the tile updates within 5 s of the last event.
- **AC-25.5** (FR-25.12) Given a kiosk disconnected for 30 min with 40 actions queued, when it reconnects, then all 40 are stored once in order and conflicts are flagged, not lost.
- **AC-25.6** (FR-25.11) Given a machine DOWN for 11 min without a reason, then an andon alert is open, the kiosk shows a banner, and coding the reason closes the downtime but keeps the alert until cleared.
- **AC-25.7** (FR-25.2, BR-25.2) Given shift start 07:00 and clock-in 06:53, then the registration time is 07:00.
- **AC-25.8** (FR-25.8) Given an approved day, when a supervisor edits a registration, then a reversal and a new PayLine are created and the export batch includes both.
- **AC-25.9** (FR-25.5) Given a mandatory numeric check with limits 10–12 and a value 13, then `finished` is blocked until override with reason.
- **AC-25.10** (phase exit) Given the demo plant, when the configured product (w8 configurator) is planned (w22), scheduled (w23), produced through kiosk registrations and machine counts (w25) and ended (w21), then the order's actual cost, variances and WIP reconcile to the journal and the recorded walkthrough is signed off.

### 4.25.9 Test plan and fixtures

Fixtures: `shift-6-operators` (schedule, badge ids, profiles, expected pay lines and order costs); `pay-rules-30` (daily/weekly overtime, Sunday, flex, missing clock-out); `iot-sim-10k` (Go simulator producing counts/states with controllable gaps and rollover); `oee-shift-cases` (10 shifts with hand-computed A/P/Q); `offline-replay-40`.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Pay rules | unit | PayRule engine | 30/30 |
| OEE | unit | bucket calc | matches hand-computed to 4 decimals |
| Event ordering/loss | integration | iot-edge → Kafka → DB | 0 lost, 0 dup over 600k events with broker restart |
| Kiosk shift E2E | Playwright | kiosk + production | labor cost ties |
| Offline sync | Playwright (offline mode) | PWA queue | 40/40 once, in order |
| Throughput | performance | ingest | 10k events/min sustained, consumer lag < 5 s |
| Accessibility | axe | kiosk screens | WCAG 2.1 AA |

Reconciliation: Σ approved hours per order = Σ job-card hours posted; Σ PayLine hours = Σ registration hours per worker-day; Σ MachineEvent good counts attributed to a job = kiosk-confirmed good qty (variance report).

### 4.25.10 Deployment and operations

Helm: `shopfloor` (Go, 3 replicas; Kafka consumer group `shopfloor-oee`), `shopfloor-bff` (TS, 2 replicas, SSE), `kiosk` (static PWA served by the shell CDN path `/kiosk`), `iot-edge` (Go, deployed per plant; DaemonSet or single-node k3s at the edge; Mosquitto bundled optionally), `shopfloor-db` (CloudNativePG 200 GB, monthly partitions). Config: `SF_IDLE_THRESHOLD_S` (300), `SF_ANDON_DOWNTIME_MIN` (10), `SF_OFFLINE_MAX_ACTIONS` (5000), `IOT_BUFFER_HOURS` (24), `IOT_OPCUA_ENDPOINTS`, `IOT_MQTT_BROKER`. Flags: `shopfloor.iot`, `shopfloor.oee`, `shopfloor.pay-export`. Jobs: auto clock-out (hourly), OEE rollup (every 15 min), MachineEvent partition drop (daily, > 90 days). SLOs: kiosk action p95 < 200 ms server-side; ingest 10k events/min with consumer lag p95 < 5 s; availability 99.9%; edge buffer never exceeds 80% of 24 h. Alerts: consumer lag > 30 s (page), edge offline > 5 min (warn), seq gap detected (warn), andon storm > 50 open (warn). Runbook: "edge offline" — check MQTT/OPC-UA connectivity, buffer fill, restart connector; "seq gap" — replay from edge buffer by seq range. Rollback: prior digest; edge connector version pinned per plant with staged rollout.

### 4.25.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| JmgEmployee / HR mini-master (PA0001) | Worker | badge ids, default resource, profile |
| JmgProfileTable, JmgProfileDay / work schedule rules (T508A) | ProfileDay | shifts, breaks, rounding |
| JmgPayAgreement, JmgPayTable / wage types, time evaluation rules | PayRule config | overtime thresholds and multipliers |
| JmgAbsenceCode, Jmg error cause codes / absence types, AFRU.GRUND | reason codes | mapped to Passage code lists |
| JmgStampJournalTable, JmgRegistration (last 90 days) / CATSDB, AFRU | TimeRegistration (history, status exported) | read-only history for balances |
| Flex balances (JmgFlexTrans) / time accounts (PT_BAL00) | opening flex balance per worker | one opening line |

Validation: flex balances per worker equal legacy at cutover; pay lines for the last closed week recomputed in Passage match legacy within 0.01 h per worker. Not migrated: raw terminal logs older than 90 days, machine data (none in AX/SAP core), MII configurations.

### 4.25.12 Dependencies, risks, open questions

Depends on: w21 job-card journals; w23 scheduled jobs (dispatch list); w24 kanban jobs on the kiosk; w7 employee party; w5 attachments and document service; w4 Kafka/Temporal; w43 payroll (consumer, later).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| OPC-UA server diversity (security policies, node models) | High | Medium | Support None/Basic256Sha256 and companion-spec-agnostic tag mapping; certify against open62541 and a PLC simulator |
| Offline conflicts corrupt labor cost | Medium | High | Client UUID idempotency, flagged conflicts, supervisor review before posting option |
| Phase-exit demo depends on four prior waves' stability | High | High | Freeze w21–24 APIs two weeks before w25 close; rehearsal run weekly |

Open questions: (1) Post job cards immediately or at approval? Default immediately with reversal on edit; decide by w25 kickoff. (2) Edge connector as DaemonSet in the plant cluster vs standalone binary? Default standalone k3s node per plant; decide by w25 mid-point. (3) Which OEE loss taxonomy (six big losses vs ISO 22400 element set)? Default six big losses mapped to ISO 22400 KPIs; decide by w25 kickoff.
