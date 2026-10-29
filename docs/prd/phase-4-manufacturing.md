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
| `dev.passage.production.journal.posted.v1` | production | inventory (w16), ledger (w11), planning | journalId, type, lines[item, qty, cost], journalEntryId |
| `dev.passage.production.order.settled.v1` | production | ledger, costing (w20), analytics | orderId, actualCost, plannedCost, variances[] |
| `dev.passage.planning.plan.completed.v1` | planning | production, purchasing (w17), scheduling | planVersionId, counts, duration, messages |
| `passage.planning.planned-order.firmed.v1` | planning | production, purchasing, inventory | plannedOrderId, targetType, targetId |
| `dev.passage.scheduling.job.scheduled.v1` | scheduling | production, shopfloor, planning | jobId, resourceId, start, end, runId |
| `passage.lean.kanban.state-changed.v1` | lean | production, inventory, shopfloor | kanbanId, from, to, handlingUnits |
| `dev.passage.shopfloor.registration.posted.v1` | shopfloor | production, payroll (w43) | registrationId, workerId, jobId, hours, payType |
| `dev.passage.shopfloor.machine.event.v1` | iot-edge → shopfloor | shopfloor (OEE), production, analytics | machineId, resourceId, kind, counts, state, ts |

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
| Order split / group | ProdSplit (Split production order), ProdGroup | CO02 → Functions → Split order (AFKO.SPLIT), order group | `:split` and `:merge` commands (FR-21.15) | full |
| Order change management | ECO re-explosion on ProdTable before Started (BOMCalc), no change-cost tracking | OCM (COCM / COCM1), change profiles, change costs (COCM*) | OrderChangeRecord, `:apply-change` with cost preview (FR-21.16) | full |
| Trigger points / rework operation insertion | n/a (manual route edit) | Trigger points (CO31/CO33, OPMP/OPM, TTRIG), rework order CO07 | TriggerPoint rules on operation feedback (FR-21.17); rework order (FR-21.20) | full |
| Order-specific BOM / route (ETO) | ProdBOM / ProdRoute line edits on the order (ProdBOM form) | Order BOM CU51/CU52/CU55/CU56, order routing | Order BOM editor with audit (FR-21.18) | full |
| As-built genealogy (serial → component serial) | InventTrans tracing by serial (InventDim), no as-built record | Serial numbers at component level (IQ09, as-built configuration) | AsBuiltRecord captured at picking/RAF (FR-21.19) | full |

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
- **FR-21.15 Order split and merge** — Should. The system shall split a production order in status Created…Started into two orders (original keeps the remaining quantity; the new order receives `splitQty`), carrying BOM lines, operations and jobs proportionally, and shall merge orders of the same product, BOM/route version and site that are in status ≤ Released into one.
  - Notes: Split of a Started order distributes posted consumption and route hours proportionally to the split ratio unless `allocation=keep-on-original`; WIP postings move via an internal transfer journal (Dr WIP new / Cr WIP original) so each order's WIP stays reconcilable. Split quantity must be ≥ the item's minimum order quantity and a multiple of the item's multiple. Scheduled jobs of the new order are rescheduled (w23) with the original's dates as the preferred window. Merge is refused if either order has posted journals. AX ProdSplit and SAP CO02 split both stop at Released; Passage allows Started with the allocation rules above.
- **FR-21.16 Order change management** — Should. The system shall detect, for open orders (status < Started, or Started with `ocm.allowStarted`), that the BOM or route version they snapshot has been superseded by an ECO (w9 `bom.version.activated` / `route.version.activated` events) and shall offer `:apply-change` that re-explodes the order and reports the delta (added/removed/changed lines and operations) with a change cost = Σ (new planned cost − old planned cost) plus a configurable change-cost surcharge per OCM profile.
  - Notes: OCM profile per item group × site: `automatic` (apply at next status transition), `manual` (planner confirms), `none` (ignore). Lines already consumed are never removed; they become `substituted` and feed the production substitution variance (BR-21.6). Each application writes an OrderChangeRecord (ECO id, before/after snapshot hash, change cost, actor). Configured products (w8) re-explode through the configurator with the stored configuration; a changed model that no longer yields the same configuration raises `OCM_CONFIG_DRIFT` and requires manual review.
- **FR-21.17 Trigger points** — Should. The system shall evaluate TriggerPoint rules on operation feedback events (route card / job card post, operation status change, qty_error > 0, quality check fail from w25) and perform the configured function: insert a rework operation sequence after the current operation (standard operation set from w9), create a rework order (FR-21.20) for the error quantity, release the next operation, start a Temporal workflow, or raise a notification.
  - Notes: Trigger points are defined on route operations (w9 route version) and copied to ProductionOperation at explosion; conditions use the same expression language as alert rules (w5) over the feedback payload. Inserted operations get oper_number = current + 5 (renumbered if collision) and are scheduled forward (w23). One trigger point fires at most once per operation unless `repeatable`.
- **FR-21.18 Order-specific BOM and route** — Should. The system shall allow editing BOM lines and operations on an order after explosion and before Started (add, remove, change quantity/item/resource requirement), marking the order `orderBomModified = true`, keeping an audit diff against the source versions, and optionally saving the result as a new BOM/route version for the product (ETO reuse).
  - Notes: Edits after Released require duty *Maintain order BOM after release* and re-run the reservation policy. Planned cost is re-estimated after each edit. Edited orders are excluded from automatic OCM `automatic` profile and flagged for manual review instead.
- **FR-21.19 As-built genealogy** — Should. For serial- or batch-tracked finished items the system shall record, per finished serial/batch, which component serials/batches were consumed (AsBuiltRecord), captured on picking-list posting (component serials against the order) and resolved per finished serial at RAF (operator assignment on the kiosk in w25, or automatic when the order quantity is 1).
  - Notes: Genealogy is queryable in both directions (where-used of a component serial; as-built of a finished serial) and feeds the w16 trace inquiry and w33 nonconformance. Unresolved component serials at End raise a warning, not an error, unless the item's tracking policy sets `asBuiltRequired`.
- **FR-21.20 Rework production orders** — Should. The system shall create a rework order for a finished item quantity (optionally referencing the original order, batch or serial): no BOM explosion by default (components added manually), a route of rework operations, and a `reworkReference` so that the rework cost is posted to the item's rework cost group and reported against the original order.
  - Notes: Rework output is received at the original item's current cost; rework cost is posted as a variance of type `rework` (added to the Variance enum) and not capitalized into inventory unless the parameter `rework.capitalize` is set. SAP CO07 (order without material) semantics; AX equivalent is a production order with an empty BOM and the "rework" posting profile.

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

**OrderChangeRecord** (FR-21.16): id, order_id (FK), eco_id (w9 FK), source_version_old_id, source_version_new_id, delta (jsonb: added/removed/changed lines and operations), change_cost (numeric 18,4), surcharge (numeric 18,4), profile (enum automatic/manual/none), status (enum detected/previewed/applied/dismissed), applied_by, applied_at, snapshot_hash_before, snapshot_hash_after (bytea).

**TriggerPoint** (FR-21.17): id, order_id, oper_number, sequence (int), condition (text, w5 expression), function (enum: insert-operations, create-rework-order, release-next, start-workflow, notify), standard_operation_set_id (nullable, w9), workflow_key (nullable), repeatable (bool), fired_count (int), last_fired_at.

**AsBuiltRecord** (FR-21.19): id, order_id, finished_item_id, finished_serial_or_batch (text), component_item_id, component_serial_or_batch (text), qty (numeric 18,6), journal_id (picking list), raf_journal_id, assigned_by, assigned_at; unique (tenant_id, component_item_id, component_serial_or_batch) for serialized components.

**ProductionOrder additions**: split_from_order_id (nullable self FK), merged_into_order_id (nullable), order_bom_modified (bool, default false), rework_reference_type (enum: order/batch/serial, nullable), rework_reference_id; ProductionOrder.origin_type gains `Split` and `Rework`; Variance.variance_type gains `rework`.

Invariants (additions): Σ quantity of an original order and orders split from it = original quantity at split time; an order with merged_into_order_id set has status Cancelled and no posted journals; each serialized component serial appears in at most one AsBuiltRecord. Indexing: AsBuiltRecord (finished_serial_or_batch), (component_serial_or_batch); OrderChangeRecord (status, order_id).

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
| `dev.passage.production.order.created.v1` | order committed | orderId, orderNumber, productId, qty, siteId, deliveryDate, originType, originId | planning, analytics |
| `passage.production.order.status-changed.v1` | any transition | orderId, from, to, qty, startDate, endDate, actor, occurredAt | planning, scheduling, shopfloor, lean |
| `dev.passage.production.journal.posted.v1` | journal posted | journalId, orderId, type, postingDate, lines[{itemId, qty, unitCost, operNumber, hours}], journalEntryId | inventory, ledger, planning |
| `dev.passage.production.order.settled.v1` | End posted | orderId, plannedCost{material,labor,overhead,subcontract}, actualCost{…}, variances[{type,costGroup,amount}] | ledger, costing, analytics |
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
- **BR-21.11 Split allocation.** With split ratio r = splitQty / order qty: posted consumption qty, route hours and WIP amounts move to the new order at round(x × r, 6) (quantities) and round(x × r, 2) (amounts); the rounding residual stays on the original. Each order's Σ WipPosting must equal its moved consumption after the transfer journal; property-tested.
- **BR-21.12 OCM change cost.** change_cost = Σ_new planned − Σ_old planned + surcharge(profile), where surcharge is a fixed amount or % of the delta; a negative delta still carries the fixed surcharge. Applying a change to an order whose scheduled jobs are firmed (w23) requires un-firming or the `ocm.forceReschedule` option.
- **BR-21.13 Trigger point evaluation order.** Trigger points on one operation evaluate in ascending sequence number; a trigger that inserts operations re-evaluates nothing on the inserted operations in the same feedback transaction (prevents loops). A trigger's function failing (e.g., no standard operation set) raises `TRIGGER_FUNCTION_FAILED` and does not block the journal post.
- **BR-21.14 As-built resolution.** Σ component serials assigned across finished serials ≤ component serials consumed on the order; one component serial belongs to exactly one finished serial; reversal of a RAF journal detaches the finished serial's record.

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
- **AC-21.11** (FR-21.15, BR-21.11) Given a Started order of 100 with 305 kg consumed and 20.00 h posted, when I split 40 units with proportional allocation, then the new order shows 122.000000 kg and 8.00 h, the original 183.000000 kg and 12.00 h, and both WIP balances reconcile to the GL.
- **AC-21.12** (FR-21.16, BR-21.12) Given a Released order whose BOM version was superseded by an ECO adding one line, when I preview `:apply-change`, then the delta lists the added line and a change cost equal to the line's planned cost plus the profile surcharge; applying it re-explodes without touching consumed lines.
- **AC-21.13** (FR-21.17) Given a trigger point "qty_error > 0 → insert rework operations R10, R20", when a job card posts 3 error units, then operations 25 and 26 (renumbered) exist after the current operation and are scheduled forward.
- **AC-21.14** (FR-21.19, BR-21.14) Given an order for 2 serialized units consuming 2 serialized motors, when RAF assigns motor M-1 to unit S-1 and M-2 to S-2, then the as-built inquiry for S-2 returns M-2 and the where-used of M-1 returns S-1.
- **AC-21.15** (FR-21.20) Given a rework order for 5 units of FG-100 with 2.00 h of rework at 30.00/h, when ended, then a variance of type `rework` of 60.00 is posted and the 5 units are received at their existing unit cost.

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

### 4.21.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| Production order number sequence | legal entity | sequence ref | `production-order` | any w5 sequence | Numbers new orders | Production control parameters → Number sequences | CORU/OPJH number ranges per order type (CO82) |
| Default reservation policy | site | enum | `released` | none / estimated / released / started | When BOM-line reservations are created | Production control parameters → General → Reservation | Order type-dependent parameters OPL8 (reservation/purchase req. creation) |
| RAF tolerance % | site | numeric(5,2) | 0 | 0–100 | Max over-delivery on RAF | Production parameters → Report as finished → Accept error / over-delivery | Overdelivery tolerance in material master (MARC.UEETO) / order |
| Over-consumption tolerance % | site | numeric(5,2) | 10 | 0–1000 | Picking beyond required qty before duty is needed | Production parameters → Journals → Over-consumption | None native (user status / exit) |
| Auto-start on feedback | site | bool | true | true/false | Route card on a non-Started order starts it | Production parameters → Status update → Auto-start | Confirmation parameters OPK4 → "Set order status PCNF/CNF automatically" |
| End jobs on last RAF | site | bool | true | true/false | Sets endJob on all jobs at final RAF | ProdParmReportFinished.EndJob | Final confirmation flag (AFRU.AUERU) |
| Estimate on create | site | bool | false | true/false | Runs `:estimate` immediately after creation | Production parameters → Estimation on creation | Preliminary costing at order creation (OPL8 costing variant) |
| Picking-list proposal default | site | enum | `remaining` | remaining / proportional / none | Default journal proposal mode | ProdParmBOMConsumption → Quantity | Backflush indicator (MARC.RGEKZ / work center) |
| Route-card proposal basis | site | enum | `remaining` | remaining / proportional | Default hours proposal | ProdParmRouteConsumption | Confirmation proposal (OPK4 → propose quantities/activities) |
| Posting profile | legal entity | table | per item group × site | item group, site, All | Ledger accounts per posting type | Production posting profiles (ProdPosting) | OBYC keys GBB-VBR/AUF, WIP via results analysis OKG1–OKG4 |
| Variance accounts per type | legal entity | table | per posting profile | account per variance type | GL account for lot-size/qty/price/substitution/rework | ProdPosting variance accounts | Variance categories OKV1, settlement profile |
| Period control for Production module | legal entity | ref | w14 module `PROD` | open/closed per period | Blocks journal posting in closed periods | Ledger calendar module access | Posting period OB52 (MM/CO) |
| OCM profile | item group × site | enum | `manual` | automatic / manual / none | FR-21.16 behavior | n/a | OCM change profile (COCM profiles) |
| OCM change-cost surcharge | tenant | amount or % | 0 | ≥ 0 | Added to change cost | n/a | Change cost in OCM profile |
| Trigger points enabled | site | bool | false | true/false | FR-21.17 evaluation | n/a | OPMP standard trigger points |
| Split allocation default | site | enum | `proportional` | proportional / keep-on-original | FR-21.15 allocation | ProdSplit dialog | CO02 split dialog |
| Rework capitalize | legal entity | bool | false | true/false | FR-21.20 cost treatment | n/a | Settlement rule of rework order (CO07) |
| As-built required | item tracking policy | bool | false | true/false | FR-21.19 End enforcement | n/a | Serial number profile (OIS2) procedure PPAU/PPRL |
| Status transition duties | tenant | duty map | per 4.21.17 | duties | Which duty each `:command` needs | Security roles on ProdTable menu items | Authorization object C_AFKO_AWK per status/activity |

### 4.21.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Production orders list | list inquiry | status, site, product, delivery date range, origin, saved view | order, item, qty, remaining, status, dates, planned/actual cost | `production.order_summary` | ProdTable list page | COOIS (order headers) |
| Production order detail / status log | drill-down | order id | header, lines, operations, jobs, journals, status log with actor/trace id | order aggregate | ProdTable form, ProdTable status log | CO03, order status log (AFKO status, JEST) |
| Cost analysis per order | drill-down | order, cost group | planned, actual, variance, variance %, drill to journal entry | `production.cost_view` | ProdCalcTrans "Price calculation" inquiry | CO03 → Cost analysis (KKBC_ORD) |
| Production journals | list inquiry | journal type, date range, posted/unposted, order | journal number, type, posting date, lines, journal entry id | ProductionJournal | ProdJournalTable inquiries (Picking list, Route card, Job card, RAF) | CO14 confirmation display, MB51 (movement types 261/101) |
| Operations overview | list inquiry | site, resource, status, date range | order, oper, resource, planned/reported hours, qty good/error, status | ProductionOperation | ProdRoute inquiry "Operations" | COOIS (operations), CO26 |
| WIP balance by order | operational report | site, as-of date, status | order, posted consumption, posted RAF value, WIP balance, GL WIP account delta | WipPosting | SSRS ProdCalcTrans (WIP), "Production orders in process" | KKAO/KKAX WIP calculation list, S_ALR_87013127 |
| Variance report | operational report | period, site, item group, variance type | order, variance type, cost group, amount, % of planned | Variance | SSRS "Production variance" (ProdVariance) | KKS1/KKS2 variance report, S_ALR_87013139 |
| Material shortage / release check | list inquiry | order selection | line, item, required, available, short qty | ProductionBomLine × on-hand (w16) | Release warning list, "Missing materials" | CO24 missing parts list, CO09 availability |
| Subcontract operations | list inquiry | vendor, status, due date | order, oper, vendor, PO line, qty, price, receipt status | ProductionOperation.subcontract | Vendor resource operations (ProdRoute) | ME2O subcontracting stock monitor, COOIS (external ops) |
| Order change management worklist | list inquiry | site, OCM status | order, ECO, delta count, change cost, profile | OrderChangeRecord | n/a | COCM / COCM1 worklist |
| As-built / where-used by serial | drill-down | finished serial or component serial | serial tree, order, posting date | AsBuiltRecord | InventTrans serial trace | IQ09 / serial number history |
| Production order throughput KPI | KPI | site, period | orders ended, on-time %, average cycle days, scrap % | `production.order_kpi` (w46) | n/a (BI) | SAP MC.* PP information system (MCP1/MCP3) |

### 4.21.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Picking list | Release, journal create, manual print | Header: order, item, qty, site/warehouse, dates; line: line no., item, variant, qty required/issued, location, batch/serial, barcode; footer: signatures, page x/y | PDF (w5 document service), email, print queue | `prod-doc` per legal entity | None statutory; keep with order 7 years where production records are GxP (w24 EBR) |
| Job card | Release, re-print | Header: order, operation, resource, scheduled start/end; line: job type, planned hours, qty; barcode (GS1 `(90)jobId`) for kiosk scan | PDF, print queue | `prod-doc` | None |
| Route card | Release | Header: order, route version; line per operation: resource, setup/run time, scrap %, instructions | PDF | `prod-doc` | None |
| Production order summary (shop packet) | On demand | Header, BOM lines, operations, documents (w5 attachments list), QR code to order | PDF | `prod-doc` | None |
| Rework instruction | Rework order release (FR-21.20) | Reference order/batch/serial, defect reason, rework operations | PDF | `prod-doc` | Kept with original order record |
| Order change notice | OCM apply (FR-21.16) | ECO id, before/after lines and operations, change cost, approver | PDF, email to planner | `prod-doc` | None |
| Subcontract dispatch note | Release of subcontract operation | Vendor, PO line, components shipped, qty, due date | PDF, email, EDI DESADV via w35 | `prod-doc` | Customs / transit documents in w34 where goods cross borders |
| Electronic order events | Each status change / journal post | CloudEvents on Kafka (4.21.5) | JSON | n/a | Retained 7 years in Iceberg (w46) |

### 4.21.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `wip-reconcile` | daily 02:00 tenant local | legal entity | Compares Σ WipPosting per order vs GL WIP account; writes ReconcileRun row; re-runnable | < 5 min per 50k open orders | Per-entity failure isolates; retries 3× | Delta ≠ 0 pages on-call |
| `bulk-status-update` (Temporal) | on demand | order id batches of 50 | Idempotency-Key per batch; per-order result rows | 500 orders < 5 min | Continue-on-error, per-order status | Error rate > 5% warns |
| `cancel-stale-created-orders` | weekly Sunday 03:00 | site | Orders Created > N days (param, 90) without estimate → Cancelled; checkpoint per 1,000 | < 2 min | Skip orders with origin PlannedOrder | Count cancelled reported |
| `ocm-detect` | on ECO event + daily 04:00 | site | Marks orders whose snapshot version is superseded; idempotent by (order, ECO) | < 1 min per 10k orders | Logs and continues | Worklist size > 500 warns |
| `subcontract-accrual` | month-end (w14 close task) | legal entity | Accrues received-not-invoiced subcontract ops; reversal next period | < 3 min | Blocks close task on failure | Close checklist |
| `order-cost-snapshot` | daily 23:30 | legal entity | Snapshots planned vs actual per open order for trend KPI; upsert by (order, date) | < 5 min | Non-blocking | Missing snapshot warns |
| `auto-end-fully-reported` | daily 22:00 (opt-in) | site | Ends orders with qty_raf = qty and no open journals; param `autoEnd.enabled` | < 5 min | Per-order errors listed | Report emailed to supervisor |

### 4.21.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| View production orders | GET /production/orders*, order list and detail screens, status log | none | Production supervisor, Planner, Cost accountant, Shop floor supervisor |
| Maintain production orders | POST/PATCH /production/orders, `:estimate`, `:schedule`, order BOM editor (FR-21.18 pre-release) | with *Post production journals* when the same user also approves cost (w39 cost approval) | Production supervisor, Planner |
| Release production orders | `:release`, `:start`, `:cancel`, `:reset-status`, `:bulk-status`, `:split`, `:merge` | with *End production orders* for the same order (release and settle) — flagged, not blocked | Production supervisor |
| Post production journals | POST /production/orders/{id}/journals, `:post`, journal editor | with *Maintain production setup* (posting profiles) | Production supervisor, Shop floor supervisor, Kiosk service account (w25) |
| Post production over-consumption | journal post beyond tolerance | — | Production supervisor |
| End production orders | `:end`, `end with zero output` (separate privilege), settlement | with *Maintain production setup*; with *Post production journals* flagged | Production supervisor, Cost accountant |
| End production order with zero output | `:end` when qty_raf = 0 | — | Cost accountant |
| View production costs | GET /production/orders/{id}/costs, cost analysis, variance report | none | Cost accountant, Controller, Production supervisor |
| Maintain production setup | posting profiles, parameters (4.21.13), OCM profiles, trigger points | with *Post production journals*, *End production orders* | ERP administrator, Cost accountant (posting profiles only) |
| Maintain order BOM after release | FR-21.18 edits on Released orders | with *Release production orders* flagged | Production engineer |
| Apply order change | `:apply-change` (FR-21.16) | none | Planner, Production engineer |
| Create rework orders | FR-21.20 create/release | none | Production supervisor, Quality engineer (w33) |

### 4.21.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| PROD-0001 | error | Transition {from} → {to} is not allowed for order {order}. | Illegal state-machine transition | Use an allowed command; see status log | BR-21.1 (`PRODUCTION_ILLEGAL_TRANSITION`) |
| PROD-0002 | error | Posting date {date} is in a closed period for Production. | Period closed in w14 period control | Change posting date or reopen period | BR-21.3 (`PERIOD_CLOSED`) |
| PROD-0003 | error | Order {order} was changed by another user. Reload and retry. | Stale version on PATCH | Reload | BR-21.4 |
| PROD-0004 | error | Reset is blocked: inventory from journal {journal} has been consumed downstream. | Reversal would drive on-hand negative | Reverse the downstream issue first | BR-21.5 (`REVERSAL_BLOCKED_DOWNSTREAM`) |
| PROD-0005 | warning | Release with shortages: {n} lines short (first: {item} short {qty}). | On-hand < required at release | Release anyway, or wait/expedite supply | FR-21.4 |
| PROD-0006 | error | Release blocked: line {line} item {item} is not released in {legalEntity}. | Unreleased product | Release the product (w8) | FR-21.4 |
| PROD-0007 | error | Consumption {qty} exceeds required {required} plus tolerance {pct}%. | Over-consumption without duty | Reduce qty or obtain duty | FR-21.5 |
| PROD-0008 | error | RAF quantity {qty} exceeds remaining {remaining} plus tolerance. | Over-delivery | Reduce qty or raise tolerance | FR-21.7 |
| PROD-0009 | error | Serial {serial} already exists in this tenant. | Duplicate serial at RAF | Use a unique serial | BR-21.10 (`SERIAL_DUPLICATE`) |
| PROD-0010 | error | Tracking dimension {dim} is required for item {item}. | Missing batch/serial | Enter batch/serial | FR-21.7 |
| PROD-0011 | error | Picking would drive on-hand of {item} at {location} negative ({qty}). | Negative inventory not allowed | Receive stock or change location | BR-21.9 |
| PROD-0012 | error | Cannot end: {n} journals are unposted. | Open journals at End | Post or delete the journals | FR-21.8 |
| PROD-0013 | error | Cannot end with zero output without the duty "End production order with zero output". | qty_raf = 0 | Obtain duty or RAF | FR-21.8 |
| PROD-0014 | warning | {n} subcontract operations have no vendor invoice; cost will be accrued at PO price. | Uninvoiced subcontract ops at End | Accept accrual or wait for invoice | FR-21.10 |
| PROD-0015 | error | Split quantity {qty} is below the minimum order quantity {min} or not a multiple of {multiple}. | Invalid split | Adjust split quantity | FR-21.15 |
| PROD-0016 | error | Merge refused: order {order} has posted journals. | Merge precondition | Only merge unposted orders | FR-21.15 |
| PROD-0017 | info | A newer BOM/route version exists (ECO {eco}); change cost {amount}. | OCM detection | Preview and apply the change | FR-21.16 |
| PROD-0018 | error | Configuration drift: the model change no longer yields this order's configuration. | OCM on configured product | Review configuration manually | FR-21.16 (`OCM_CONFIG_DRIFT`) |
| PROD-0019 | warning | Trigger point {tp} on operation {oper} failed: {reason}. Journal was posted. | Trigger function failure | Fix the trigger setup and re-run | BR-21.13 (`TRIGGER_FUNCTION_FAILED`) |
| PROD-0020 | warning | {n} component serials are not assigned to a finished serial. | Unresolved as-built at End | Assign on the order's as-built tab | FR-21.19 |
| PROD-0021 | error | As-built assignment is required for item {item} before End. | `asBuiltRequired` policy | Assign component serials | FR-21.19 |
| PROD-0022 | error | Bulk status update: {succeeded} succeeded, {failed} failed; see result list. | Partial batch failure | Open result list and retry failed orders | FR-21.12 |

### 4.21.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation under 3× load |
|---|---|---|---|---|---|---|
| Create order (explode 7-level, 2,000 components) | 2,000 orders/tenant/day; peak 50/min | 150 / 300 / 800 ms | 100 creates/s per 3 replicas | 2,000 × 365 × ~40 rows × 0.5 KB ≈ 15 GB | ProductionOrder (tenant, legal_entity, status, delivery_date); lines by order_id | Queue creates behind HPA scale-out; p95 ≤ 2 s |
| Status transition | 10,000/day; peak 500/min (bulk) | 100 / 500 / 1,000 ms | 200/s | StatusLog 10k × 365 × 0.3 KB ≈ 1 GB | advisory lock per order | Bulk batches throttle to 50 concurrent |
| Journal post (200 lines) | 5,000 journals/day; peak 100/min | 600 ms / 2 s / 4 s | 50 posts/s | ProductionJournal + lines + WipPosting ≈ 20 GB | unique (tenant, idempotency_key); hash partition by legal entity at > 50M rows | Serialize per order; shed with 429 above 3× for > 60 s |
| Cost view read | 20,000/day | 80 / 300 / 600 ms | 300/s | n/a (projection) | materialized `cost_view` refreshed on journal.posted | Serve last snapshot with staleness header |
| Order list (saved view, 50 rows) | 100,000/day | 100 / 300 / 600 ms | 500/s | n/a | covering index + keyset pagination | Reduce page size to 25 |
| wip-reconcile | nightly over 50k open orders | n/a | 50k orders in < 5 min | ReconcileRun 365 rows/entity | batch by legal entity | Runs serially per entity; alerts if > 15 min |
| Event publishing (outbox) | 50,000 events/day | lag p95 < 1 s | 1,000 events/s | Kafka retention 7 days; Iceberg forever | Debezium outbox per DB | Lag alert at 30 s; consumers idempotent |

### 4.21.20 Localization and compliance hooks

- **Period control and fiscal calendars**: journal posting dates obey the legal entity's fiscal calendar (w14) including 4-4-5 and non-January year starts; no production-specific statutory calendars.
- **Currency rounding**: WIP and variance amounts post in the legal entity's accounting currency with the w12 rounding rule (0.01 USD/EUR; 1 JPY; 0.05 CHF cash rounding does not apply to GL postings); EUR reporting-currency amounts carried per line.
- **Statutory production reporting**: none in v1. Country packs (wave 48) may add production statistics extracts (e.g., EU PRODCOM, Brazil Bloco K of SPED Fiscal, which needs consumption per order by NCM code) through the `production.statutory-extract` extension point on the `journal.posted` projection; Bloco K is registered as a wave 48 BR country-pack item.
- **Retention**: production journals and WIP postings are financial records — 7 years (configurable per legal entity, w48 retention policy); GxP plants extend to batch expiry + 1 year via the w24 EBR profile.
- **Language**: document templates (4.21.15) are per-language in the w5 document service; item descriptions print in the site's language with fallback to the tenant default.
- **Units**: metric/imperial dual display from w6 unit conversions; quantities stored in inventory UoM.
- **Privacy**: actor ids on StatusLog are Keycloak subject ids; worker names resolve at read time so GDPR erasure (w48) pseudonymizes without rewriting history.
- **Extension points**: `production.posting-profile.resolve` (OPA policy hook for country-specific account derivation), `production.document.render` (template override per country), `production.statutory-extract`.

### 4.21.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.22.1 Production control — Production order → Split / Group (ProdSplit) [AX #195] | PARTIAL-21 | ADDED as FR-21.15 | Split and merge with proportional WIP allocation (BR-21.11); AX stops at Released, Passage allows Started |
| SAP 13.21 PP — Rework (CO07 / trigger points CO31 / CO33) [SAP #245] | PARTIAL-21 | ADDED as FR-21.17, FR-21.20 | Trigger points insert rework operations or create rework orders; rework order semantics of CO07 |
| SAP 13.21 PP — Order split (CO02 → split), order change management (OCM: COCM / COCM1) [#246] | PARTIAL-21 | ADDED as FR-21.15, FR-21.16 | Split per FR-21.15; OCM detection, delta preview and change cost per FR-21.16 / BR-21.12 |
| SAP 13.21 PP Customizing — OPMP / OPM trigger points (TTRIG) [#249] | PARTIAL-21 | ADDED as FR-21.17 | Trigger point definitions on w9 route operations; parameter "Trigger points enabled" in 4.21.13 |
| SAP 13.21 PP Customizing — OCM profiles (change profiles, change cost) [#252] | PARTIAL-21 | ADDED as FR-21.16 | OCM profile and surcharge parameters in 4.21.13; worklist in 4.21.14 |
| SAP 13.32 LO-VC — CU51 / CU52 / CU55 / CU56 order BOM [#357] | PARTIAL-21 | ADDED as FR-21.18 | Order-specific BOM/route edits with audit diff and save-as-version; sales-order-level order BOM before order creation stays with wave 27 |
| SAP 13.32 LO-VC Periodic — Re-explosion of configured orders after model change (OCM) [#363] | PARTIAL-21 | ADDED as FR-21.16 | Re-explosion through the configurator with `OCM_CONFIG_DRIFT` guard; `ocm-detect` job in 4.21.16 |
| SAP 13.32 LO-VC Customizing — Order BOM settings (CU51 processing) [#364] | PARTIAL-21 | ADDED as FR-21.18 | Duty *Maintain order BOM after release* and OCM exclusion flag replace CU51 processing settings |
| SAP 13.33 Batch/Serial — Serial numbers at component level (as-built genealogy) [#368] | PARTIAL-21 | ADDED as FR-21.19 | AsBuiltRecord captured at picking/RAF; kiosk assignment in w25; inquiry in 4.21.14 |

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
| Simulation / long-term planning | ReqPlan static plans as copies (ReqPlanVersion), forecast plan, D365 planning optimization "simulation" | LTP MS01–MS08, MS31–MS70 planning scenarios, simulative MRP, PURCHIS transfer; S/4 pMRP; APO /SAPAPO/MC01 planning versions | Plan kind `simulation` with ScenarioOverride (FR-22.15) | full |
| DDMRP buffers / priority-based planning | D365 priority-based planning (planning priority on coverage, ReqPlanningPriority) | S/4 DDMRP: buffer positioning, buffer profiles (PPH_DD_*), buffer level calculation, replenishment planning Fiori apps | Coverage code `ddmrp`, DdmrpBuffer, BufferProfile (FR-22.16) | full |
| Quota arrangements | Item coverage vendor + "intercompany/transfer" source; no percentage split | MEQ1/MEQ3/MEQ4/MEQ6/MEQ8, quota arrangement (EQUK/EQUP), source list rules (TMQ1) | QuotaArrangement (FR-22.17) | full |
| MRP areas (sub-site planning) | Warehouse-level coverage (ReqItemTable by InventDim warehouse) | MRP areas OMIZ (MDLV, MDLG, MDLW), storage location and subcontractor MRP areas | Planning scope `warehouse` and `subcontractor` (FR-22.18) | full |
| Planning strategies | Coverage code + forecast reduction keys (ReqReduceKey) + pegged supply BOM lines | Strategy groups OPPS/OPPT (T461S, T461P), requirements types/classes (OVZG/OVZH), consumption mode MARC.VRMOD | PlanningStrategy (FR-22.19) | full |
| Shelf-life planning | Shelf-life on InventBatch, master planning "Use shelf life" (PdsShelfLife on coverage) | PP/DS shelf-life planning (SAP_PP_SL001) | Shelf-life aware pegging (FR-22.20) | full |
| Supply schedule workbench | ReqSupplySchedule (Supply schedule form, R3), SSRS supply schedule report | MD04 period totals, MD07 collective list | Supply schedule screen (FR-22.21) | full |
| Inventory forecast | ForecastInvent (Inventory forecast inquiry) | MD04 projected available stock, MS04 simulation | InventoryForecast projection (FR-22.22) | full |

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
- **FR-22.15 Simulation plans and planning scenarios** — Should. The system shall run plans of kind `simulation` that read the same snapshot inputs as an operational plan but apply a ScenarioOverride (demand source = a named forecast version or demand program, demand multipliers by item group, added/removed sales demand, BOM/route version effective date, lead-time and capacity overrides, starting inventory as of a date), write to their own PlanVersion, never firm or emit action messages to operational consumers, and offer evaluations: rough-cut capacity load per resource group and period (using route-based times, or w23 finite load with flag), purchase volume by vendor × period (value at trade-agreement price), and inventory value projection.
  - Notes: Up to 10 active scenarios per plan; each scenario may run as many times as needed (versions retained per FR-22.1). Simulation results can be copied into a forecast version (LTP "transfer to PURCHIS"/vendor information) and published to suppliers via the w20 supplier portal as a non-binding forecast. pMRP-style rough simulation is the scenario run with `bucket=week` and `bomDepth=1..n` limit for speed (≤ 2 min for 50k items at depth 2). Planned orders in a simulation version carry `simulation = true` and are rejected by `:firm` with 409 `PLAN_SIMULATION_NOT_FIRMABLE`. Version-to-version diff (FR-22.14 hash, diff API) works across a scenario and the current operational version.
- **FR-22.16 DDMRP buffers and priority-based planning** — Should. The system shall support coverage code `ddmrp` for items marked as decoupling points (strategic buffers), computing per item/site: average daily usage (ADU, rolling past and/or forward window), decoupled lead time (DLT), buffer zones per BufferProfile (lead-time factor, variability factor, minimum order cycle, minimum order qty) — red base = ADU × DLT × LTF, red safety = red base × VF, yellow = ADU × DLT, green = max(ADU × DLT × LTF, MOQ, ADU × order cycle) — and planned orders by the net-flow equation (BR-22.11), with planning priority = net-flow position / top of green (TOG) used to sort planned orders and the planner worklist.
  - Notes: Buffer positioning is set per item coverage (`decouplingPoint = true`); DLT is the longest unprotected lead-time path through the BOM to the next decoupling point or purchase (computed from w9 BOM and lead times). Order-spike threshold (default 50% of red zone) and horizon (default DLT) qualify spikes into qualified demand. Dynamic adjustment factors (planned adjustment factor per period, e.g., seasonal ramp) multiply ADU. Non-buffered items below a decoupling point are planned by standard MRP. Buffer recalculation runs nightly (4.22.16). Priority-based planning (D365) uses the same priority field: coverage groups with `priorityBased = true` replace date-ordering with priority ordering for planned-order generation within the horizon.
- **FR-22.17 Quota arrangements** — Should. The system shall split net requirements of an item/site across sources defined in a QuotaArrangement (valid from/to; sources: vendor, own production with production version, transfer from site/warehouse; quota per source; quota base qty; maximum quantity per period; minimum lot per source) by quota rating = (allocated qty + base qty) / quota, assigning each planned order to the source with the lowest rating, optionally splitting a single requirement across sources (`split = true`, SAP quota "splitting quota").
  - Notes: Allocated qty accumulates from firmed and open orders created against the arrangement (purchase orders, production orders, transfer orders carry `quota_arrangement_id`). Sources with quota 0 are used only when all others are blocked (e.g., trade agreement expired). Quota arrangement analysis shows planned vs actual split by source for the validity period. Takes precedence over the item coverage default vendor; trade-agreement lead times apply per chosen source.
- **FR-22.18 Planning scope by warehouse and subcontractor (MRP areas)** — Should. The system shall plan items at site level (default) or at warehouse level when item coverage exists for that warehouse (MRP area), with warehouse-level demand and supply netted separately and replenished from the site by planned transfers or by direct purchase/production per the warehouse's coverage; and shall plan components provided to a subcontractor (w21 FR-21.10 vendor operations) in a subcontractor planning scope per vendor, creating planned transfer (provision) orders to the vendor location.
  - Notes: A warehouse in its own planning scope is excluded from site-level netting. Subcontractor scope requires the vendor to have a w16 vendor-location warehouse (consignment-out type). Planning scope is a field on ItemCoverage (`site`, `warehouse`, `subcontractor`).
- **FR-22.19 Planning strategies** — Should. The system shall assign a PlanningStrategy to item coverage that controls how forecasts and sales demand interact: `mts-net` (SAP 10: forecast is demand, sales orders reduce forecast per reduction principle), `mts-gross` (SAP 11: forecast planned regardless of on-hand), `mto` (SAP 20: no forecast; supply created per sales line and marked/pegged 1:1, segregated as order-specific stock when w16 sales-order stock dimension is active), `assemble-to-order-final` (SAP 40: forecast at the finished item consumed by sales orders with consumption mode backward/forward/backward-then-forward and consumption periods in days), `plan-without-final-assembly` (SAP 50/52: forecast plans components; finished item supply created only on sales demand), `subassembly` (SAP 70: forecast on the subassembly consumed by dependent demand).
  - Notes: Consumption window: backward days (default 30) and forward days (default 0); unconsumed sales demand beyond the window is added on top of forecast. `mto` and `plan-without-final-assembly` set `is_marking = true` on Pegging so the supply cannot be re-pegged by later runs. Strategies map from SAP strategy groups (T461P) on migration; AX customers map ReqReduceKey + coverage code combinations to the nearest strategy (mapping table in 4.22.11 addendum below).
- **FR-22.20 Shelf-life aware pegging** — Should. For items with shelf-life tracking (w24 FR-24.6) the planning engine shall treat batch on-hand and supply as usable for a demand only if expiry (or best-before, per item setting) − demand date ≥ the demand's required remaining shelf life (customer sellable days from w24 FR-24.19, else item default), creating planned supply for the shortfall and a `Expiring supply` message for batches that will expire unpegged.
- **FR-22.21 Supply schedule workbench** — Should. The system shall provide a period-bucketed (day/week/month) grid per item × site for a selection of items showing demand, supply, planned supply, PAB and safety stock per period, with inline creation of planned orders in a period cell, quantity edits, firming, and a printable supply schedule report.
- **FR-22.22 Inventory forecast** — Could. The system shall expose an InventoryForecast projection per item/site/period: projected on-hand = PAB at period end of the current plan version (or a chosen scenario), with value at current cost, and days-of-supply = projected on-hand / average daily demand of the next 30 days.

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

**ScenarioOverride** (FR-22.15): id, plan_id, name, demand_source (enum: operational, forecast-version, demand-program), forecast_version_id, demand_multipliers (jsonb: [{itemGroupId, factor}]), added_demand (jsonb lines), bom_effective_date (date), lead_time_overrides (jsonb), capacity_mode (enum: none, rough-cut, finite), start_inventory_as_of (date), bucket (enum day/week/month), bom_depth_limit (int, nullable), active (bool).

**BufferProfile / DdmrpBuffer** (FR-22.16): BufferProfile(id, code, item_type (manufactured/purchased/distributed), lead_time_factor numeric(5,3), variability_factor numeric(5,3), order_cycle_days int, spike_threshold_pct numeric(5,2) default 50, spike_horizon_days int nullable = DLT); DdmrpBuffer(id, item_id, site_id, warehouse_id, profile_id, adu numeric(18,6), adu_window_past_days int default 90, adu_window_future_days int default 0, dlt_days numeric(8,2), daf numeric(6,3) default 1, red_base, red_safety, yellow, green, top_of_red, top_of_yellow, top_of_green (numeric 18,6), calculated_at, manual_override (bool)).

**QuotaArrangement / QuotaSource** (FR-22.17): QuotaArrangement(id, item_id, site_id, valid_from, valid_to, split (bool), min_split_qty); QuotaSource(id, arrangement_id, source_type (vendor/production/transfer), vendor_id, production_version (bom_version_id, route_version_id), from_warehouse_id, quota numeric(9,4), base_qty numeric(18,6), allocated_qty numeric(18,6), max_qty_per_period, period_days, priority int).

**PlanningStrategy** (FR-22.19): code (enum per FR-22.19), consumption_mode (backward/forward/backward-forward), backward_days int default 30, forward_days int default 0, uses_marking (bool), segregated_stock (bool); ItemCoverage gains planning_strategy_code, planning_scope (site/warehouse/subcontractor), decoupling_point (bool), buffer_profile_id, required_shelf_life_days, priority_based (bool). PlannedOrder gains simulation (bool), planning_priority numeric(7,4), quota_source_id.

Invariants (additions): for a DdmrpBuffer, top_of_red = red_base + red_safety < top_of_yellow < top_of_green; for an active QuotaArrangement Σ quota of non-zero sources > 0 and validity periods of arrangements for the same item/site do not overlap; no PlannedOrder with simulation = true has status firmed.

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
| `dev.passage.planning.plan.started.v1` | run begins | planId, versionId, kind, scope | analytics |
| `dev.passage.planning.plan.completed.v1` | version becomes current | planId, versionId, itemsPlanned, plannedOrders{purchase,production,transfer}, messages, durationMs, planHash | production, purchasing, scheduling |
| `passage.planning.planned-order.firmed.v1` | firm | plannedOrderId, orderType, targetId, qty, dates | production, purchasing, inventory |
| `dev.passage.planning.message.raised.v1` | message created | requirementId, refType, refId, action, delayDays | purchasing, sales (w27), notifications |
| `dev.passage.planning.forecast.published.v1` | consensus approved | modelId, version, items, horizonPeriods | planning run |

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
- **BR-22.11 DDMRP net-flow equation.** Net-flow position NFP = on-hand + open supply (inside DLT) − qualified demand, where qualified demand = past-due demand + demand due today + spikes (single-day demand > spike threshold × red zone inside the spike horizon). If NFP ≤ top of yellow, a planned order is created for TOG − NFP (rounded up to MOQ/multiple), dated today + DLT. Planning priority = NFP / TOG; zone shown red ≤ TOR, yellow ≤ TOY, else green. Worked example: ADU 10, DLT 10 days, LTF 0.5, VF 0.5, MOQ 0, order cycle 7 days → red base 50, red safety 25, TOR 75; yellow 100, TOY 175; green max(50, 0, 70) = 70, TOG 245. On-hand 90, open supply 60, qualified demand 20 → NFP 130 ≤ 175 → order 115, priority 53.06% (yellow).
- **BR-22.12 Quota rating.** rating_s = (allocated_s + base_s) / quota_s; ties broken by priority then source id. Example: vendor A quota 60, allocated 300; vendor B quota 40, allocated 100; requirement 100 → ratings A 5.00, B 2.50 → B is chosen; after allocation B = 200 → 5.00. With split on, the requirement is divided so that post-allocation ratings are as equal as possible, subject to min_split_qty.
- **BR-22.13 Strategy consumption.** For `assemble-to-order-final`, a sales line on date d consumes forecast in [d − backward_days, d] (latest first), then in (d, d + forward_days] (earliest first); unconsumed sales qty adds to gross requirements; forecast never goes negative.
- **BR-22.14 Simulation isolation.** A simulation version reads the operational snapshot by reference (copy-on-write) and writes only its own partitions; operational `plan.completed` consumers ignore events with `simulation = true`; firming, action messages to purchasing and delay messages to sales are suppressed.
- **BR-22.15 Shelf-life usability.** A batch with expiry e is usable for demand on date d only if e − d ≥ required remaining shelf life r; planned production supply is assumed to have expiry = delivery date + shelf_life_days.

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
- **AC-22.11** (FR-22.15, BR-22.14) Given scenario "Demand +20% on group FG", when the simulation runs, then its planned production quantities for FG items are ≥ the operational plan's, no ActionMessage reaches purchasing, and `:firm` on any of its planned orders returns 409 `PLAN_SIMULATION_NOT_FIRMABLE`.
- **AC-22.12** (FR-22.16, BR-22.11) Given the BR-22.11 buffer, when planned, then one planned order of 115 is created with priority 0.5306 and zone yellow.
- **AC-22.13** (FR-22.17, BR-22.12) Given the BR-22.12 quota arrangement, when a requirement of 100 is planned, then the planned purchase order is for vendor B and B's allocated qty becomes 200 after firming.
- **AC-22.14** (FR-22.19, BR-22.13) Given strategy `assemble-to-order-final` with backward 30 days, forecast 100 on 2026-11-30 and a sales line of 40 on 2026-11-20, when planned, then gross requirement is 100 (forecast 60 + sales 40), not 140.
- **AC-22.15** (FR-22.20, BR-22.15) Given a batch expiring in 20 days and a demand 5 days out requiring 30 days remaining shelf life, when planned, then the batch is not pegged and a planned order covers the demand.
- **AC-22.16** (FR-22.18) Given item coverage at warehouse WH-2 in its own scope, when the plan runs, then WH-2 demand is covered by a planned transfer from the site warehouse and WH-2 on-hand is not netted against site demand.

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

**Migration addendum (FR-22.16–22.19).** SAP T461P strategy groups map to PlanningStrategy (10 → mts-net, 11 → mts-gross, 20 → mto, 40 → assemble-to-order-final, 50/52 → plan-without-final-assembly, 70 → subassembly; 25/26/30/59/60/63/74/81/82 → nearest strategy with a migration warning). SAP EQUK/EQUP → QuotaArrangement/QuotaSource with allocated qty carried over. SAP MDLV/MDLG/MDLW → ItemCoverage planning_scope. S/4 DDMRP buffer data (PPH_DD_*) → BufferProfile/DdmrpBuffer with recalculation on first run. AX ReqPlan static plans used as simulations → ScenarioOverride with demand_source = forecast-version. AX ReqReduceKey + ReqPOType combinations → strategy per mapping table delivered with the w10 migration templates.

### 4.22.12 Dependencies, risks, open questions

Depends on: w21 production orders (firming target, route-based lead times); w17 PO creation; w16 on-hand and inventory transactions; w9 BOM/LLC; w6 calendars; w27 sales orders (demand arrives later — until then sales demand is loaded via fixtures and the `sales-line` ref type).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Without sales orders until w27 the plan is demo-only | Certain | Low | Forecast + fixture demand; re-proof at w27 |
| Memory blow-up on 50k × 365 daily buckets | Medium | Medium | Sparse buckets (only dates with movement); weekly buckets beyond 90 days |

Open questions: (1) Daily vs weekly buckets beyond 90 days — default weekly; decide at w22 kickoff. (2) Whether to expose `positive_days = 0` as "unlimited" (AX) or 0 — default AX semantics; decide by w22 mid-point.

### 4.22.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| Current static plan | legal entity | plan ref | `MAIN` | any static plan | Plan whose current version drives firming and messages | Master planning parameters → General → Current static master plan | Planning file / MRP run scope per plant (OMDU, MD01 plant) |
| Current dynamic plan | legal entity | plan ref | `DYN` | any dynamic plan | Net-change plan for order events | Master planning parameters → Current dynamic master plan | Net change planning NETCH (MD02 processing key) |
| Planning horizon days | plan | int | 365 | 30–1,095 | Requirements beyond are ignored | ReqPlan coverage time fence / Master plans → time fences | Planning horizon OMDV/OPPQ (MRP planning horizon in workdays) |
| Daily-bucket window days | plan | int | 90 | 0–365 | Daily buckets inside, weekly beyond | n/a | Period split in MD04 (period totals) |
| Dynamic debounce seconds | tenant | int | 60 | 10–3,600 | Coalesces order events before net change | n/a (D365 auto-planning) | Planning file entry processing (MDAB/MDRE) |
| Max retained versions | plan | int | 20 | 2–100 | Purge threshold | ReqPlanVersion cleanup | n/a (single MRP list per run) |
| Default coverage group | legal entity | ref | `REQ` | any CoverageGroup | Used when item has none | Master planning parameters → Standard coverage → Coverage group | MRP type default per plant (OPPQ), MRP group OPPR |
| Planning priority on forecast vs sales | tenant | enum | sales > forecast > safety | permutations | Pegging priority | Master plans → Sequence of requirements | Requirements class priority (OVZG) |
| Reduction principle default | legal entity | enum | `transactions` | none / percent / transactions / transactions-dynamic | Forecast reduction | Reduction keys (ReqReduceKey) on coverage groups | Consumption mode MARC.VRMOD, OMPA |
| Statistical forecast model selection | tenant | enum | `auto` | auto / ets / croston / tsb / naive | Model per item | Demand forecasting parameters → Forecast algorithm | MP30/MP31 forecast models (MPOP) |
| Forecast history months | tenant | int | 36 | 12–120 | History window | Demand forecasting parameters → Historical horizon | Forecast profile periods (MP38) |
| Firming grouping | legal entity | enum | `vendor-date` | none / vendor / vendor-date / vendor-week | PO grouping on firm | Master planning parameters → Planned orders → Firming → Grouping | MD14/MD15 collective conversion, ME59N grouping |
| Auto-firm in firming fence | coverage group | bool | false | true/false | Firm planned orders automatically | Coverage group → Firming time fence | Firming type in MRP type (T438A.FXHOR) |
| Simulation scenarios per plan | tenant | int | 10 | 0–50 | FR-22.15 limit | Static plan copies | LTP planning scenarios (MS31) |
| DDMRP enabled | legal entity | bool | false | true/false | FR-22.16 | Priority-based planning feature | DDMRP activation (PPH_DD settings) |
| ADU window past/future days | buffer profile | int/int | 90 / 0 | 7–365 | ADU computation | n/a | DDMRP buffer level calculation app parameters |
| Spike threshold % | buffer profile | numeric | 50 | 1–500 | Qualified demand | n/a | DDMRP spike horizon/threshold |
| Quota arrangements enabled | legal entity | bool | false | true/false | FR-22.17 | n/a | Quota usage indicator MARC.USEQU |
| Default planning strategy | item group | enum | `mts-net` | FR-22.19 codes | Strategy when coverage has none | Coverage group + reduction key | Strategy group OPPS / MRP group OPPR |
| Consumption backward/forward days | strategy | int/int | 30 / 0 | 0–365 | BR-22.13 | Reduction key periods | MARC.VINT1/VINT2, OPPR |
| Shelf-life aware planning | item | bool | false | true/false | FR-22.20 | Coverage group "Use shelf life" (PdsShelfLife) | PP/DS shelf-life planning |
| Planning scope per item coverage | item coverage | enum | `site` | site / warehouse / subcontractor | FR-22.18 | Coverage by warehouse dimension | MRP area type 01/02/03 (OMIZ) |

### 4.22.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Net requirements (stock/requirements list) | drill-down | item, site/warehouse, plan/version or scenario | date, ref type, qty, PAB, futures date, messages | Requirement | Net requirements form (ReqTransDisplay) | MD04 / MD05 (MRP list) |
| Collective net requirements | list inquiry | planner, item group, exception group, site | item, shortage date, messages count, coverage days | Requirement × ActionMessage | Net requirements list page | MD06 / MD07 collective display |
| Planned orders | list inquiry | plan, type, status, vendor, date range | number, item, qty, dates, status, priority, quota source | PlannedOrder | Planned orders (ReqPO) list page | MD16 / MD13 planned order list |
| Action messages | list inquiry | plan, action type, buyer/planner | action, ref order, qty, date, suggested date | ActionMessage | Actions (ReqTransAction) form | MD06 exception groups 1–8 |
| Delay (futures) messages | list inquiry | plan, delay days ≥ n | demand ref, requested, expected date, delay | ActionMessage (delay) | Futures messages form | MD06 exception 10/15/30 |
| Pegging / explain tree | drill-down | planned order id, depth | tree of supply → demand with qty | Pegging | Explosion form (ReqTransExplosion) | MD09 pegged requirements, MD4C order report |
| Supply schedule | operational report | items, site, bucket | per period demand, supply, PAB, safety | Requirement buckets | Supply schedule (ReqSupplySchedule), SSRS supply schedule report | MD04 period totals, MS04 |
| Scenario comparison | operational report | scenario vs current version | planned qty/value deltas by item group, capacity load, purchase volume by vendor | PlanVersion diff | Static plan comparison (manual) | MS70 / MCEC (LTP purchasing evaluation), MS44 capacity |
| DDMRP buffer status | list inquiry | site, zone, priority ≤ x | item, NFP, TOR/TOY/TOG, priority %, zone | DdmrpBuffer | Priority-based planning workspace (D365) | DDMRP Replenishment planning Fiori app |
| Quota arrangement analysis | list inquiry | item, period | source, quota %, planned qty, actual qty, deviation | QuotaSource × orders | n/a | MEQ6/MEQ8 |
| Forecast accuracy | KPI | item group, horizon lag | MAPE, wMAPE, bias, forecast vs actual | ForecastLine × sales history | Demand forecast accuracy (D365 Power BI) | MP33/MP32 forecast analysis |
| Inventory forecast | operational report | items, horizon | projected on-hand, value, days of supply | InventoryForecast | Inventory forecast (ForecastInvent) | MD04 projected stock |
| Plan run history | list inquiry | plan, date range | version, kind, duration, counts, hash, status | PlanVersion | Master planning log (ReqLog) | MRP run log (MDKP), MD01 list |

### 4.22.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Supply schedule report | On demand | Header: item selection, site, bucket; per item: periods × demand/supply/PAB; footer: plan version and hash | PDF, XLSX | none | None |
| Supplier forecast (non-binding) | Scenario or plan publish to w20 portal | Header: vendor, horizon, version; lines: item, period, qty, unit | PDF, portal view, EDI DELFOR via w35 | `supplier-forecast` | Non-binding disclaimer text per legal entity |
| Plan run summary | `plan.completed` | Version, duration, counts by type, top delays, hash | email, in-app | none | None |
| Firming result report | Firming batch | Planned orders → created POs/production/transfer orders | PDF, email | none | None |
| Electronic plan events | Run lifecycle | CloudEvents (4.22.5) | JSON | n/a | 1 year in Iceberg |

### 4.22.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Static plan run | daily 01:00 site-local | plan × LLC level | New PlanVersion per run; pointer swap only on success | 50k items < 10 min | Version marked failed; previous stays current | Failed or > 20 min pages |
| Dynamic net change | event-driven, 60 s debounce | item reach set | Version per batch; dedupe by event id | < 60 s after event | Retry 3× then fall back to next static run | Lag > 5 min warns |
| Statistical forecast generation | weekly Sunday 22:00 | item batches of 1,000 | Forecast version per run; resumable per batch | 50k items < 15 min | Failed batches retried; partial version not published | Failure warns |
| DDMRP buffer recalculation | daily 00:15 | site | Upsert per (item, site) with calculated_at | 20k buffers < 2 min | Keep previous zones | Zone change > 30% reported |
| Quota allocation recompute | daily 00:45 | legal entity | Recomputes allocated_qty from orders; idempotent | < 1 min | Non-blocking | Drift vs incremental > 0 warns |
| Version purge | daily 03:00 | plan | Drops partitions beyond max versions | < 1 min | Retry next day | n/a |
| Simulation runs | on demand | scenario | Version per run | 50k items < 10 min (< 2 min rough-cut) | Version failed | none |
| Forecast consumption snapshot | daily 23:00 | legal entity | Snapshot of reduced forecast for accuracy KPI | < 2 min | Non-blocking | Missing warns |

### 4.22.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| View planning | GET /planning/*, master planning workspace, net requirements, pegging, reports | none | Planner, Buyer, Production supervisor, Sales (read delays) |
| Run master planning | POST /plans/{id}:run (static/dynamic) | none | Planner (lead), Batch service account |
| Maintain planned orders | PATCH planned orders, approve, delete, supply schedule edits | none | Planner |
| Firm planned orders | POST planned-orders:firm | with *Approve purchase orders* (w17) for purchase-type firming above approval limit | Planner, Buyer |
| Maintain forecasts | forecasts generate/lines, consensus submit | with *Approve forecast consensus* | Demand planner |
| Approve forecast consensus | Temporal approval step | with *Maintain forecasts* (same version) | Supply chain manager |
| Maintain planning setup | coverage groups, item coverage, strategies, buffer profiles, quota arrangements, parameters | with *Firm planned orders* flagged | Planning administrator |
| Run simulations | scenario create/run/compare, publish supplier forecast | none | Planner, S&OP manager |

### 4.22.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| PLAN-0001 | error | A run of plan {plan} is already in progress. | Concurrent run | Wait or cancel the running version | FR-22.1 (`PLAN_ALREADY_RUNNING`) |
| PLAN-0002 | error | Planned order {po} is already firmed. | Duplicate firm | Refresh | BR-22.9 |
| PLAN-0003 | error | Planned orders of a simulation cannot be firmed. | Firm on simulation | Copy quantities to the operational plan | FR-22.15 (`PLAN_SIMULATION_NOT_FIRMABLE`) |
| PLAN-0004 | warning | BOM cycle detected for {item}; low-level codes not updated. | Cyclic BOM | Fix BOM (w9) | BR-22.1 step 2 |
| PLAN-0005 | warning | Requirement for {item} splits into {n} orders (max {max}). | Lot size max | Review maximum qty | FR-22.4 |
| PLAN-0006 | info | Supply {ref} can be postponed to {date}. | Early supply | Apply action | FR-22.7 |
| PLAN-0007 | warning | Demand {ref} will be delayed {days} days (expected {date}). | Futures date propagation | Expedite supply or accept delay | FR-22.7 |
| PLAN-0008 | warning | No vendor for planned purchase of {item}; using none. | Missing default vendor / trade agreement | Set vendor on item coverage or quota | FR-22.3 |
| PLAN-0009 | error | Quota arrangement {qa} has no usable source in {period}. | All quotas 0 or blocked | Maintain sources | FR-22.17 |
| PLAN-0010 | warning | Buffer for {item} is in red zone (priority {pct}%). | NFP ≤ TOR | Expedite replenishment | FR-22.16 |
| PLAN-0011 | warning | Batch {batch} of {item} expires unpegged on {date}. | Shelf-life aware planning | Use in other demand or scrap | FR-22.20 |
| PLAN-0012 | error | Forecast history for {item} is shorter than {months} months; naive model used. | Short history | Accept or set manual forecast | FR-22.9 |
| PLAN-0013 | error | Plan hash mismatch with previous identical-input run. | Non-determinism | Open incident; compare sorted outputs | FR-22.14 |
| PLAN-0014 | warning | Subcontractor scope for vendor {vendor} has no vendor-location warehouse. | FR-22.18 precondition | Create vendor location (w16) | FR-22.18 |
| PLAN-0015 | info | Sales demand {ref} exceeds consumable forecast by {qty}; added on top. | BR-22.13 | none | FR-22.19 |

### 4.22.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation under 3× load |
|---|---|---|---|---|---|---|
| Static plan run | 50k items × 5 levels, 200k sales lines; 1 run/day + 5 scenarios | run < 6 / 10 / 15 min | 50k items per run | 20 versions × ~15M rows ≈ 30 GB steady (dropped partitions) | partition per PlanVersion | Scenarios queue behind operational run; operational < 20 min |
| Dynamic net change | 5,000 events/day, peak 200/min | 20 / 60 / 120 s after event | 200 events/min coalesced | included above | reach-set computation in memory | Debounce extends to 300 s |
| Net requirements read | 30,000/day | 100 / 300 / 800 ms | 200/s | n/a | (plan_version, item, site, date) | Serve from previous version |
| Pegging explain (depth 20) | 10,000/day | 150 / 500 / 1,200 ms | 100/s | n/a | (demand), (supply) indexes | Lazy depth beyond 5 |
| Firm 1,000 planned orders | 50 batches/day | batch < 2 / 5 / 8 min | 10 orders/s | n/a | saga per order | Batches serialized per plan |
| Forecast generation | 50k items weekly | < 15 min | 60 items/s per worker | ForecastLine 50k × 104 periods ≈ 5M rows/yr ≈ 1 GB | (model, item, site, period) | Additional Python replicas via HPA |
| DDMRP recalculation | 20k buffers daily | < 2 min | 200 buffers/s | 20k × 365 history ≈ 7M rows ≈ 1 GB | (item, site, calculated_at) | Runs after plan; skip on overload with alert |

### 4.22.20 Localization and compliance hooks

- **Calendars**: lead-time offsets use the site work calendar (w6) including country public holidays imported per country pack (wave 48 supplies holiday calendars for the 20 launch countries).
- **Units and decimals**: quantities in inventory UoM to 6 decimals; no currency rounding in planning except value projections (supplier forecast value, inventory forecast) which use w12 rounding.
- **Supplier forecasts**: EDI DELFOR (EDIFACT) / 830 (X12) formats via w35; German VDA 4905/4984 delivery schedules for automotive suppliers are a wave 48 DE pack item through the `planning.supplier-forecast.format` extension point.
- **Export control / restricted parties**: planned purchase orders to vendors flagged by w48 sanctions screening are created but blocked from firming with the w48 screening message.
- **Privacy**: none (no personal data in planning entities except planner ids).
- **Retention**: plan versions are operational data, not records; only the last 20 versions are kept; plan run summaries retained 1 year.
- **Extension points**: `planning.demand-source` (country- or industry-specific demand feeds such as call-off schedules), `planning.supplier-forecast.format`, `planning.lead-time.adjust` (OPA hook, e.g., customs lead time per country of origin from w34).

### 4.22.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.21.1 Master planning — Supply schedule (ReqSupplySchedule) [AX #188] | PARTIAL-22 | ADDED as FR-22.21 | Period-bucketed workbench with inline planned-order creation |
| AX 12.21.1 Master planning — Inventory forecast (ForecastInvent) [AX #189] | PARTIAL-22 | ADDED as FR-22.22 | Projected on-hand, value and days of supply per period |
| AX 12.21.2 Master planning — SSRS: Supply schedule report [AX #191] | PARTIAL-22 | ADDED as FR-22.21 | Report listed in 4.22.14/4.22.15 |
| AX 12.21.4 Master planning — Priority-based planning (D365 planning priority) [AX #194] | PARTIAL-22 | ADDED as FR-22.16 | Planning priority = NFP / TOG; `priorityBased` coverage groups |
| SAP 13.12 MM-PUR — MEQ1 / MEQ3 / MEQ4 / MEQ6 / MEQ8 quota arrangement [SAP #125] | PARTIAL-22 | ADDED as FR-22.17 | Quota rating BR-22.12; analysis report in 4.22.14 |
| SAP 13.12 MM-PUR Customizing — Source determination, quota arrangement rules (T001W, TMQ1) [#137] | PARTIAL-22 | ADDED as FR-22.17 | Quota usage parameter in 4.22.13; source-list requirement stays with w17 |
| SAP 13.21 PP — MM02 MRP areas (MD61 per MRP area) [#237] | PARTIAL-22 | ADDED as FR-22.18 | Planning scope site / warehouse / subcontractor |
| SAP 13.21 PP — MS01–MS08 long-term planning, simulative MRP [#239] | PARTIAL-22 | ADDED as FR-22.15 | Plan kind `simulation` with ScenarioOverride |
| SAP 13.21 PP — MS31/MS32/MS33/MS44/MS47/MS64/MS65/MS70 LTP scenarios and evaluations [#240] | PARTIAL-22 | ADDED as FR-22.15 | Capacity and purchase-volume evaluations; supplier forecast transfer |
| SAP 13.21 PP — DDMRP (buffer positioning, levels, replenishment planning) [#241] | GAP (propose 22) | ADDED as FR-22.16 | Coverage code `ddmrp`, BR-22.11 net-flow equation |
| SAP 13.21 PP — Predictive MRP (pMRP) [#242] | PARTIAL-22 | ADDED as FR-22.15 | Rough-cut scenario with weekly buckets and BOM depth limit |
| SAP 13.21 PP — Planning strategies 20/40/50/70 [#244] | PARTIAL-22 | ADDED as FR-22.19 | Sales-order stock segregation depends on the w16 inventory dimension (register row #145, wave 16) |
| SAP 13.21 PP Customizing — OPPS / OPPT strategy groups (T461S, T461P) [#247] | PARTIAL-22 | ADDED as FR-22.19 | Strategy mapping in migration addendum |
| SAP 13.21 PP Customizing — OMIZ MRP areas (MDLV, MDLG, MDLW) [#248] | PARTIAL-22 | ADDED as FR-22.18 | ItemCoverage.planning_scope |
| SAP 13.21 PP Customizing — DDMRP buffer profiles, classification runs (PPH_DD_*) [#251] | GAP (propose 22) | ADDED as FR-22.16 | BufferProfile entity; ABC/XYZ classification for buffer positioning uses w16 ABC classification |
| SAP 13.23 PP/DS — Shelf-life planning (SAP_PP_SL001) [#278] | PARTIAL-22 | ADDED as FR-22.20 | BR-22.15 usability rule |
| SAP 13.23 PP/DS — /SAPAPO/MC01, /SAPAPO/MVM planning versions [#279] | PARTIAL-22 | ADDED as FR-22.15 | Scenario versions isolated per BR-22.14 |
| SAP 13.23 PP/DS — /SAPAPO/SDP94 (DP), /SAPAPO/SNP94 (SNP), GATP [#280] | PARTIAL-22 | SUPERSEDED by FR-22.9 (DP) and FR-22.17 + BR-22.7 (cross-site sourcing); GATP DEFERRED to wave 27; SNP optimizer OUT-OF-SCOPE | SNP-style network optimization (transport-lane capacity LP) is not in v1: quota and transfer planning cover sourcing across sites, and the w23 CP-SAT service is the extension path if demand justifies it |
| SAP 13.26 CS — Service parts planning (SPP) [#306] | PARTIAL-22 | DEFERRED to wave 45 | Single-echelon MRP with warehouse scopes (FR-22.18) plans field stock; installed-base demand and multi-echelon service parts logic arrive with service management |

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
| Resource networks | n/a (route operation resource requirements only) | Resource network CRC5/CRC1–3 (allowed resource sequences in PP-PI) | ResourceNetwork with allowed transitions (FR-23.13) | full |
| Campaign planning | D365 batch order sequencing by "batch order sequence" / planning priority; no campaign object | PP-PI campaigns (COCP, CMP process order campaigns), PP/DS campaign planning | Campaign entity and campaign scheduling (FR-23.14) | full |
| Changeover / cleanout | Route setup time only; D365 sequencing value | Setup matrix (/SAPAPO/CDPS setup groups), cleanout orders, recipe-dependent cleaning | SetupMatrix, changeover jobs, cleanout orders (FR-23.15) | full |
| Campaign cost settlement | n/a | Campaign settlement / cleanout cost allocation to campaign orders | Cleanout cost allocation (FR-23.16) | full |
| Block planning | n/a | PP/DS block planning (/SAPAPO/CDPSC12, characteristic blocks on resources) | CapacityBlock with attribute constraints (FR-23.17) | full |

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
- **FR-23.13 Resource networks** — Should. The system shall define a ResourceNetwork per site as a directed graph of resources (nodes) with allowed transitions (edges, with optional transfer time and maximum wait minutes), assign a network to a route operation chain, and constrain the scheduler (heuristic and CP-SAT) so that when operation n runs on resource r, operation n+1 is scheduled only on a resource r' with an edge r → r'.
  - Notes: Typical use: mixing vessel V1 feeds fillers F1/F2 only; V2 feeds F3 only. Maximum wait (e.g., product holds 120 min in the vessel) becomes a hard constraint end_n + wait_max ≥ start_{n+1}; violation in heuristic mode moves the predecessor later. A network edge may require a secondary resource (e.g., transfer line). CP-SAT encodes allowed pairs as `AddAllowedAssignments` on the resource choice variables of consecutive operations.
- **FR-23.14 Campaign planning** — Should. The system shall group production/batch orders into a Campaign (resource or resource group, campaign key such as formula family or setup group, planned start, maximum size in orders/quantity/duration, sequence rule) and schedule them contiguously on the campaign resource, inserting a leading setup and a trailing cleanout, with orders inside the campaign sequenced by the setup matrix to minimize Σ changeover minutes.
  - Notes: Campaigns are created manually from a selection, or proposed by the `campaign-proposal` job from planned/firmed orders sharing a campaign key within a horizon window (default 14 days). Orders join or leave a campaign until the first order is Started. A campaign is frozen once Started; added orders go to the next campaign. Sequence rule: setup-matrix nearest neighbor (heuristic) or CP-SAT circuit (`AddCircuit`) with time limit 30 s for ≤ 50 orders.
- **FR-23.15 Setup matrix and changeover/cleanout operations** — Should. The system shall maintain a SetupMatrix per resource or resource group with changeover minutes and cost per (from setup group → to setup group) and a cleanout definition per setup group (cleaning standard operation set, duration, mandatory after N batches or M hours), and shall generate changeover jobs between consecutive jobs with different setup groups and cleanout orders (lightweight production orders of type `cleanout`, no BOM output) at campaign end or when the mandatory condition is reached.
  - Notes: Setup group comes from the product (w8) or formula (w24) and may be overridden on the order. Changeover jobs replace the route's static setup time when a matrix entry exists. Recipe-dependent cleaning: a matrix entry may require a specific cleaning standard operation set (e.g., allergen cleanout from "contains peanut" to "peanut-free" = wet clean 240 min). Cleanout orders consume cleaning materials and labor through w21 journals.
- **FR-23.16 Campaign cost settlement** — Should. The system shall allocate the actual cost of a campaign's cleanout and leading-setup orders to the campaign's production orders by RAF quantity (default), equally, or by run minutes, posting the allocation as an overhead cost line of type `campaign-changeover` before those orders are ended.
  - Notes: Orders cannot be ended while the campaign's cleanout order is unended unless `campaign.allowEarlyEnd` is set, in which case the allocation estimates from the cleanout's planned cost and the difference posts to the last order ended.
- **FR-23.17 Block planning** — Could. The system shall define CapacityBlocks on a resource (recurring or dated time windows, e.g., Monday 06:00–14:00) each restricted to an attribute condition over product or order attributes (variant dimension values such as color = white, or batch attribute targets such as grade ≥ 2), and the scheduler shall place a job inside a block only if the job's attributes satisfy the block condition; jobs outside any matching block are placed in unblocked capacity or flagged late.

### 4.23.4 Data model

**ResourceCapacity**: id, tenant_id, resource_id, day (date), interval_start (timestamptz), interval_end, open_minutes (int), efficiency_pct (numeric 5,2), finite (bool), source (calendar/override).

**ScheduledJob**: id, job_id (FK ProductionJob), order_id, oper_number, resource_id, start (timestamptz), end, minutes, run_id, firmed (bool), late_minutes (int), overlap_qty (numeric), secondary_resource_ids (uuid[]).

**ScheduleRun**: id, scope (order/selection/site), mode (operations/jobs), direction (forward/backward), finite (bool), algorithm (heuristic/cp-sat), time_limit_s, status, started_at, finished_at, operations_count, makespan_minutes, total_lateness_minutes, objective_value, solver_status (OPTIMAL/FEASIBLE/UNKNOWN).

**CapacityLoad** (materialized): resource_id, period_start, period_type, open_minutes, booked_minutes, load_pct, bottleneck (bool).

Invariants: no two ScheduledJob rows on the same finite resource overlap in [start, end); every ScheduledJob lies within ResourceCapacity open intervals of its resource; for jobs j, k in the same operation chain with k successor of j: k.start ≥ j.end + transit; a ScheduledJob with firmed = true is unchanged by any run without `force`.

Indexing: ScheduledJob GiST exclusion constraint `EXCLUDE USING gist (resource_id WITH =, tstzrange(start,end) WITH &&) WHERE (finite)`; ResourceCapacity (resource_id, day); CapacityLoad refreshed per run.

**ResourceNetwork / NetworkEdge** (FR-23.13): ResourceNetwork(id, site_id, code, name, active_from, active_to); NetworkEdge(id, network_id, from_resource_id, to_resource_id, transfer_minutes int, max_wait_minutes int nullable, secondary_resource_id nullable).

**Campaign / CampaignOrder** (FR-23.14): Campaign(id, site_id, resource_id or resource_group_id, campaign_key text, status (proposed/planned/started/completed/cancelled), planned_start, planned_end, max_orders int, max_qty numeric, max_minutes int, sequence_rule (enum nearest-neighbor/cp-sat/manual), cleanout_order_id, setup_order_id, total_changeover_minutes); CampaignOrder(campaign_id, order_id, sequence int).

**SetupMatrix / SetupMatrixEntry / CleanoutRule** (FR-23.15): SetupMatrix(id, resource_id or resource_group_id, version, active_from); SetupMatrixEntry(matrix_id, from_setup_group, to_setup_group, changeover_minutes int, changeover_cost numeric(18,4), cleaning_operation_set_id nullable); CleanoutRule(setup_group, duration_minutes, operation_set_id, after_batches int nullable, after_hours int nullable, at_campaign_end bool).

**CapacityBlock** (FR-23.17): id, resource_id, recurrence (RRULE text) or start/end, condition (jsonb attribute predicate), priority int, active.

Invariants (additions): consecutive scheduled jobs of an order chain using a network have an edge between their resources; jobs of one campaign on the campaign resource are contiguous except for changeover and cleanout jobs; Σ allocated campaign changeover cost = Σ actual cost of the campaign's setup and cleanout orders.

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
| `dev.passage.scheduling.run.completed.v1` | run done | runId, scope, operations, makespanMinutes, totalLatenessMinutes, solverStatus | production, planning |
| `dev.passage.scheduling.job.scheduled.v1` | job placed/moved | jobId, orderId, operNumber, resourceId, start, end, runId, firmed | production, shopfloor |
| `passage.scheduling.order.dates-changed.v1` | order start/end changed | orderId, start, end, lateMinutes | production, planning, sales (w27) |
| `dev.passage.scheduling.bottleneck.detected.v1` | load > threshold | resourceId, periods[], loadPct | notifications |

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
- **BR-23.11 Network wait limit.** For edge r → r' with max wait w: start_{n+1} − end_n ≤ w; the heuristic first tries r' candidates in transfer-time order; if none satisfies w, it delays operation n (and its predecessors) forward until a feasible pair exists, up to 7 days, then flags `NETWORK_INFEASIBLE`.
- **BR-23.12 Changeover sequencing.** Nearest-neighbor: start from the setup group with the lowest changeover from the resource's current state; repeatedly pick the unscheduled order with minimal changeover (tie → earliest due date → order number). Example: groups A→B 30, A→C 90, B→C 20, C→B 60; current A; orders B, C → sequence A→B→C = 50 min vs A→C→B = 150 min.
- **BR-23.13 Mandatory cleanout.** A cleanout is inserted when batches since last cleanout ≥ after_batches or run hours ≥ after_hours, whichever first, and always at campaign end if at_campaign_end; a cleanout is never split across shifts (moves to next open interval like setup, BR-23.3).
- **BR-23.14 Campaign allocation rounding.** Allocation by quantity: share_i = raf_i / Σ raf; amount_i = round(cost × share_i, 2); residual to the largest order.
- **BR-23.15 Block matching.** A job matches a block if every predicate in the block condition is true for the job's product variant dimensions and order attributes; overlapping blocks resolve by block priority; an unmatched job may use unblocked capacity only.

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
- **AC-23.10** (FR-23.13, BR-23.11) Given network V1 → {F1, F2} with max wait 120 min and F1, F2 busy for the next 4 hours, when an order with mixing on V1 is scheduled, then mixing is delayed so that filling starts ≤ 120 min after mixing ends, and no filling job is placed on F3.
- **AC-23.11** (FR-23.14, FR-23.15, BR-23.12) Given the BR-23.12 matrix and two orders B and C in one campaign, when the campaign is scheduled, then the sequence is B then C with 50 changeover minutes and a cleanout job after C.
- **AC-23.12** (FR-23.16, BR-23.14) Given a cleanout costing USD 900.00 and campaign orders with RAF 100, 200 and 300 units, when allocated, then the orders receive 150.00, 300.00 and 450.00.
- **AC-23.13** (FR-23.17, BR-23.15) Given a Monday block restricted to color = white, when a red-variant job is scheduled for Monday, then it is placed outside the block or flagged late; a white job is placed inside it.

### 4.23.9 Test plan and fixtures

Fixtures: `orlib-jobshop-30` (30 instances with known optima: ft06, ft10, ft20, la01–la25, abz5, abz6; abz7–abz9 are 20×15 and too slow for a CI time limit); `sched-calendar-edge` (DST days, 24/7, zero-capacity holidays); `sched-5k-ops` (demo plant scaled); `sched-propagation` (pegged chains 5 deep).

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

### 4.23.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| Default scheduling method | site | enum | `jobs` | operations / jobs | Mode on `:schedule` | Production control parameters → General → Scheduling method | Scheduling type in order type-dependent params (OPU3: forward/backward) |
| Default scheduling direction | site | enum | `backward` | forward / backward | Direction | Scheduling parameters → Direction | OPU3 scheduling type, adjust dates (OPJH) |
| Finite capacity default | site | bool | false | true/false | Run option default | Production control parameters → Scheduling → Finite capacity | Finite scheduling in capacity planning profile (OPD0 / CM21 profile) |
| Capacity horizon days | site | int | 365 | 30–730 | ResourceCapacity materialization | Capacity time fence (ReqGroup / scheduling parameters) | Capacity planning horizon (OPA6 / OPDB profiles) |
| Bottleneck threshold % | site | numeric | 90 | 50–200 | FR-23.9 flag | Bottleneck resource (D365 scheduling) | Overload % in capacity profile (CM01 evaluation profile OPA2) |
| Bottleneck consecutive weeks | site | int | 3 | 1–12 | FR-23.9 | n/a | n/a |
| Optimization time limit s | tenant | int | 60 | 5–1,800 | CP-SAT stop | n/a | PP/DS optimization profile (/SAPAPO/CDPSC11) runtime |
| Lateness weight λ | tenant | numeric | 0.1 | 0–100 | CP-SAT objective | n/a | PP/DS optimizer weighting (makespan/lateness/setup) |
| Drag snap minutes | user | int | 5 | 1–60 | Gantt grid | Gantt chart setup | Planning board profile (CM21 OPD0) time scale |
| Propagation limit | tenant | int pair | 500 orders / 5 levels | 1–5,000 / 1–20 | BR-23.10 | Reschedule references | Rescheduling options |
| Primary resource selection rule | site | enum | `least-finish` | least-finish / least-load / priority | FR-23.5 | Resource selection (ProdParmSchedule) | Work center selection (alternate sequences) |
| Schedule run nightly time | site | time | 00:30 | any | Site run | Batch job ProdMultiSchedule | Collective rescheduling CO05N / CM40 background leveling |
| Safety time / queue defaults | resource group | minutes | 0 | 0–10,080 | Added to queue before | Resource group queue time | Queue time in work center (CR02), float before/after production (OPU3 sched margin key OPJN) |
| Setup matrix active | resource | bool | false | true/false | FR-23.15 | n/a | Setup matrix in PP/DS resource |
| Campaign proposal window days | site | int | 14 | 1–90 | FR-23.14 | n/a | Campaign creation settings |
| Allow early end in campaign | site | bool | false | true/false | FR-23.16 | n/a | Campaign settlement settings |
| Block planning active | resource | bool | false | true/false | FR-23.17 | n/a | Block planning in resource (/SAPAPO/CDPSC12) |
| Network max delay days | site | int | 7 | 1–30 | BR-23.11 | n/a | n/a |

### 4.23.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Capacity load by resource | list inquiry | resource/group, period type, date range | open h, booked h, load %, bottleneck | CapacityLoad | Capacity load (WrkCtrCapRes) form, Capacity load graphically | CM01 / CM02 / CM07 |
| Capacity load detail | drill-down | resource, period | jobs, orders, minutes | ScheduledJob | Capacity reservations (WrkCtrCapRes) | CM03 / CM05 pegged requirements |
| Gantt | operational view | site, resources, window | job bars, late markers, violations | ScheduledJob | Gantt chart | CM21 / PP/DS detailed scheduling board |
| Late orders | list inquiry | site, lateness ≥ n min | order, delivery date, scheduled end, late minutes, pegged demand | ScheduledJob × order | Production orders "delayed" filter | COOIS with late date filter, CM25 |
| Schedule run comparison | operational report | two runs | makespan, total lateness, changeover minutes, utilization | ScheduleRun | n/a | PP/DS optimization log |
| Resource utilization KPI | KPI | site, period | utilization %, idle %, setup % | CapacityLoad + ScheduledJob | SSRS "Resource utilization" (WrkCtrCapResUtil) | MCP7 / work center analysis (PP information system) |
| Campaign overview | list inquiry | site, resource, status | campaign, key, orders, planned start/end, changeover min, cleanout | Campaign | n/a | COCP campaign worklist |
| Changeover analysis | operational report | resource, period | changeovers count, minutes, cost by from/to group | ScheduledJob (changeover) | n/a | PP/DS setup analysis |
| Network feasibility | list inquiry | network, date range | orders flagged NETWORK_INFEASIBLE, wait-time violations | ScheduleRun messages | n/a | n/a (PP-PI scheduling log) |
| Block utilization | list inquiry | resource, period | block, matched minutes, unused block minutes | CapacityBlock × ScheduledJob | n/a | Block planning evaluation |

### 4.23.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Dispatch list (per resource / shift) | On demand, shift start | Header: resource, shift, date; lines: sequence, order, operation, item, qty, start/end, setup group | PDF, kiosk view (w25) | none | None |
| Gantt print | On demand | Resource rows, time scale, bars, legend | PDF (landscape A3/Tabloid) | none | None |
| Campaign sheet | Campaign planned | Campaign key, sequence of orders, changeovers, cleanout instructions | PDF | `campaign` | Allergen cleanout sheets kept 2 years where food safety plans (FSMA/HACCP) require |
| Cleanout record | Cleanout order completed | Cleaning steps, materials, verifier, time | PDF; signature via FR-24.22 where GxP | `prod-doc` | Kept with batch records (w24 EBR) |
| Electronic schedule events | Job placed/moved | CloudEvents (4.23.5) | JSON | n/a | 1 year |

### 4.23.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Site schedule run | daily 00:30 site-local | site | New run_id; checkpoint every 500 operations; pointer to current run | 5k operations < 60 s | Previous run stays current | Failure pages |
| Capacity materialization | daily 00:05 + on calendar event | resource | Upsert per (resource, day) | 1,000 resources × 365 days < 2 min | Retry; stale capacity flagged | Lag > 30 min warns |
| CapacityLoad refresh | after each run/move | resource × period | Recompute deltas | < 5 s per move | Full refresh nightly | Staleness > 60 s warns |
| Campaign proposal | daily 23:00 | site × campaign key | Proposed campaigns replaced per key | < 1 min | Non-blocking | Count reported |
| Bottleneck detection | after nightly run | resource | One event per bottleneck episode | < 30 s | Non-blocking | Event to notifications |
| Optimization (on demand) | user | selection ≤ 2,000 ops (windowed) | Solver result stored; heuristic kept on UNKNOWN | ≤ time limit | OOM → heuristic retained | OOM warns |

### 4.23.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| View scheduling | GET /scheduling/*, Gantt (read), capacity load, reports | none | Planner, Production supervisor, Shop floor supervisor |
| Run scheduling | POST /scheduling/runs (heuristic, optimization) | none | Scheduler, Planner |
| Maintain schedule | `:move`, `:firm`, Gantt drag, campaign sequencing | none | Scheduler |
| Override scheduling constraints | commit moves with violations | with *Maintain capacity* flagged | Scheduling lead |
| Maintain capacity | capacity overrides, calendars on resources, capacity blocks | none | Production engineer |
| Maintain scheduling setup | parameters, setup matrices, resource networks, cleanout rules | none | Production engineer, ERP administrator |
| Maintain campaigns | create/modify/release campaigns, allocate campaign cost | with *End production orders* (w21) flagged for allocation | Scheduler, Cost accountant (allocation) |

### 4.23.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| SCHED-0001 | error | Job {job} overlaps {other} on finite resource {resource}. | Double booking on move | Choose another slot | FR-23.4 |
| SCHED-0002 | warning | Job {job} falls outside open capacity on {date}. | Calendar change | Reschedule or add capacity override | FR-23.1 |
| SCHED-0003 | warning | Order {order} is late by {minutes} min (backward scheduling fell back to forward). | BR-23.2 | Expedite or accept | BR-23.2 |
| SCHED-0004 | error | No resource with capability {cap} level ≥ {level} is available. | Capability matching | Add resource or capability | FR-23.12 |
| SCHED-0005 | error | Secondary resource {resource} is not free for the selected slot. | BR-23.4 | Pick another slot | BR-23.4 |
| SCHED-0006 | info | Optimization ended with status {status}; {kept} schedule kept. | Solver result | none | BR-23.9 |
| SCHED-0007 | warning | Propagation truncated after {n} orders / {levels} levels. | BR-23.10 | Run site schedule | BR-23.10 |
| SCHED-0008 | error | Firmed job {job} cannot be moved without force. | Firmed | Unfirm first | FR-23.4 |
| SCHED-0009 | error | No allowed successor resource in network {network} for operation {oper} within {days} days. | Network infeasible | Add network edge or capacity | BR-23.11 (`NETWORK_INFEASIBLE`) |
| SCHED-0010 | warning | Wait time {minutes} min exceeds network maximum {max} min. | Max wait | Move predecessor later | BR-23.11 |
| SCHED-0011 | error | Order {order} cannot join campaign {campaign}: campaign already started. | Frozen campaign | Use next campaign | FR-23.14 |
| SCHED-0012 | warning | Setup matrix has no entry {from} → {to}; route setup time used. | Missing matrix entry | Maintain matrix | FR-23.15 |
| SCHED-0013 | error | Cannot end order {order}: campaign cleanout {cleanout} is not ended. | FR-23.16 | End the cleanout or allow early end | FR-23.16 |
| SCHED-0014 | warning | Job {job} does not match any block on {resource}; placed in unblocked capacity. | Block planning | Review blocks | BR-23.15 |
| SCHED-0015 | warning | Resource {resource} is a bottleneck: load {pct}% for {weeks} weeks. | FR-23.9 | Add capacity or offload | FR-23.9 |

### 4.23.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation under 3× load |
|---|---|---|---|---|---|---|
| Site schedule run (heuristic) | 5,000 operations, 200 resources, 1 nightly + 20 ad hoc/day | 25 / 60 / 90 s | 100 ops/s | ScheduledJob ~ 5k × 6 jobs × 365 runs retained 30 days ≈ 1M rows | GiST exclusion; (run_id) | Ad hoc runs queued; nightly priority |
| Gantt window load | 5,000 bars; 2,000 loads/day | 300 ms / 1 s / 2 s | 50/s | n/a | (resource_id, start) | Window narrowed to 2,000 bars |
| Move with dryRun | 3,000/day; peak 30/min | 120 / 400 / 800 ms | 20/s | ScheduleRun move log 1M rows ≈ 0.5 GB | exclusion constraint check | Propagation preview limited to 100 orders |
| Capacity materialization | 1,000 resources × 365 days | < 2 min full; < 30 s per calendar change | n/a | 365k rows/yr | (resource_id, day) | Horizon reduced to 90 days for incremental |
| CP-SAT optimization | ≤ 2,000 ops per window, 5 runs/day | ≤ time limit (60 s) | n/a | negligible | n/a | KEDA scale-out; queued beyond 3 concurrent |
| Campaign sequencing | ≤ 50 orders per campaign | 1 / 5 / 30 s (CP-SAT circuit) | n/a | negligible | n/a | Fall back to nearest neighbor |

### 4.23.20 Localization and compliance hooks

- **Calendars and time zones**: capacity is materialized from site time zone calendars into UTC (BR-23.7); country holiday calendars come from wave 48 packs; working-time regulations (e.g., EU Working Time Directive 11-hour rest) are not enforced on resources but on workers in w25/w42.
- **Food safety**: allergen changeover rules (FR-23.15) support FSMA (US 21 CFR 117) and EU Regulation 852/2004 HACCP plans by forcing cleanout entries; cleanout records retained 2 years (configurable).
- **GxP**: for regulated plants, cleanout records require e-signature (w24 FR-24.22) and become part of the EBR.
- **Formats**: Gantt time labels follow the user locale (24h/12h), ISO week numbers in load views (configurable to US week numbering).
- **Privacy**: none (no personal data in scheduling entities).
- **Extension points**: `scheduling.constraint` (pluggable constraint provider evaluated in heuristic and CP-SAT, e.g., country-specific noise-curfew windows for resources), `scheduling.objective` (custom weights).

### 4.23.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| SAP 13.22 PP-PI — CRC5 / CRC resource network [SAP #253] | PARTIAL-23 | ADDED as FR-23.13 | Allowed transitions and max wait; CP-SAT allowed assignments |
| SAP 13.22 PP-PI — Campaign planning (COCP / CMP) [#259] | PARTIAL-23 | ADDED as FR-23.14 | Campaign entity, proposal job, setup-matrix sequencing |
| SAP 13.22 PP-PI — Cleanout / changeover orders, recipe-dependent cleaning [#260] | PARTIAL-23 | ADDED as FR-23.15 | SetupMatrix, CleanoutRule, cleanout orders |
| SAP 13.22 PP-PI Periodic — Campaign settlement / cleanout cost allocation [#270] | PARTIAL-23 | ADDED as FR-23.16 | BR-23.14 allocation by quantity/equal/run minutes |
| SAP 13.22 PP-PI Customizing — Resource networks [#272] | PARTIAL-23 | ADDED as FR-23.13 | Network setup under *Maintain scheduling setup* |
| SAP 13.22 PP-PI Customizing — Campaign planning settings [#273] | PARTIAL-23 | ADDED as FR-23.14 | Parameters in 4.23.13 |
| SAP 13.23 PP/DS — Block planning (/SAPAPO/CDPSC12) [#276] | GAP (propose 23) | ADDED as FR-23.17 | CapacityBlock with attribute predicate |
| SAP 13.23 PP/DS Customizing — Block planning settings, CDP settings [#281] | GAP (propose 23) | ADDED as FR-23.17 (block planning); CDP settings ADDED as FR-24.23 | CDP is planning against batch characteristics and lives with batch attributes in wave 24 |

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
| Batch attribute / shelf-life inheritance (batch derivation) | PdsInheritance (Lot inheritance setup), PdsBatchAttribInherit on formula lines, end-item / co-product inheritance | Batch derivation DVMO / DVSP / DVMAN (sender/receiver rules) | InheritanceRule, derivation on RAF and manual (FR-24.15) | full |
| Batch merge | PdsBatchMerge (Batch merge form) | Transfer posting batch to batch (MIGO 309/311) with manual characteristic update | `:merge` batches with attribute recomputation (FR-24.16) | improved |
| Consolidated batch orders (bulk → packed) | PdsConsolidatedOrder, bulk/pack relations (PdsBulkItemConv) | Process order with co-ordinated packaging orders (manual) | BulkPackRelation, consolidation job (FR-24.17) | full |
| Rework batch orders | PdsRework (Rework batch order), rework parameters | Rework process order (COR1 order without material), trigger points | Rework BatchOrder keeping batch identity (FR-24.18) | full |
| Same-batch reservation; customer sellable days; customer batch attribute requirements | PdsSameBatch, PdsCustSellableDays, PdsCustBatchAttrib | Batch search strategy for sales (VCH1, SD batch determination), minimum remaining shelf life on customer-material info | Same-batch flag, CustomerBatchRequirement (FR-24.19) | full |
| Lean subcontracting | LeanSubcontracting: service activity, vendor work cell, purchase agreement for service | Subcontracting in REM / kanban with external procurement (PK01 replenishment strategy external) | Service FlowActivity with vendor work cell (FR-24.20) | full |
| Electronic batch record | n/a (D365 batch order documents only) | COEBR batch record (creation, approval, archive) | ElectronicBatchRecord with review by exception (FR-24.21) | improved |
| Electronic signatures (21 CFR Part 11) | D365 electronic signature (SIGSignatureLog) on configured events | Digital signature DSIG, signature strategies (O2, DMS signature strategies) | Signature service with step-up re-authentication (FR-24.22) | full |
| Characteristic-dependent planning | n/a (batch attributes on sales only) | PP/DS characteristic-dependent planning (CDP) | Attribute-constrained pegging (FR-24.23) | full |
| Documentary batches | Tracking dimension group with batch active but not physical inventory | S/4 documentary batches | Tracking policy `batch-record-only` (FR-24.24) | full |

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
- **FR-24.15 Batch attribute and shelf-life inheritance (batch derivation)** — Must. The system shall apply InheritanceRules at RAF of batch orders (and on manual derivation) that copy or compute attribute values and shelf-life dates from sender batches (consumed ingredient batches flagged `inherit` on the formula line, or the main product batch for co-products) to receiver batches (product, co-products), with aggregation per attribute: `copy` (single sender), `min`, `max`, `weighted-average` (by consumed quantity), `earliest-date`, `latest-date`, or `fixed`.
  - Notes: Rules are defined per receiving item (AX "Lot inheritance setup") with sender item(s) or formula line; `inherit-shortest-shelf-life` (BR-24.4) is the built-in shelf-life rule. If a sender attribute is missing, the receiver attribute is left empty and the derivation log records `missing-sender-value`. Manual derivation (SAP DVMAN) re-runs rules for a batch after attribute changes upstream and propagates forward to batches already derived from it (max depth 10). Each derivation writes a DerivationLog row (sender batches, rule, values before/after) viewable in the derivation monitor (SAP DVMO).
- **FR-24.16 Batch merge** — Should. The system shall merge two or more batches of the same item and warehouse into a target batch (new or one of the sources) by posting a transfer from each source batch to the target, recomputing target attributes (numeric → weighted average by quantity; text/enum → must be equal or merge is refused unless the attribute is `merge-override`; dates → earliest expiry/best-before) and recording genealogy (all sources as parents).
  - Notes: Sources must have disposition `available` (or the same disposition) and the same catch-weight unit; merge of batches with different potency recomputes potency as weighted average; merge cost per unit = weighted average of source costs (standard-cost items unaffected). Merge requires duty *Merge batches*.
- **FR-24.17 Consolidated batch orders (bulk and packed items)** — Should. The system shall maintain BulkPackRelations (bulk item → packed item, packing quantity per packed unit in bulk UoM, pack formula/BOM) and a consolidation function that groups planned or firmed packed-item orders (planned production orders from w22 or firmed orders) within a date window into one bulk BatchOrder sized to Σ packed demand in bulk UoM (rounded up to formula multiples), creating packing orders that consume the bulk batch and are pegged to it.
  - Notes: Consolidation runs on demand or as a batch job (4.24.16); the bulk order's RAF batch is reserved for its packing orders (same-batch); packing orders inherit bulk batch attributes (FR-24.15) by default. Window default 7 days; maximum bulk batch size = formula maximum.
- **FR-24.18 Rework batch orders** — Should. The system shall create a rework BatchOrder for an existing batch: the batch (or part of it) is the first ingredient line, additional ingredients and rework operations come from a rework formula/route or are entered manually, and the output is received to the same batch number (default) or a new batch per rework parameters, with attributes re-recorded and the original batch genealogy preserved.
  - Notes: Rework parameters (AX "Rework" parameters): `keep-batch-number` (default true), `require-quality-order` (w33, default true), disposition of output (default `quarantine` until quality release). Cost: rework cost posts as variance type `rework` (w21 FR-21.20) unless `rework.capitalize`.
- **FR-24.19 Same-batch reservation and customer batch requirements** — Should. The system shall support (a) a same-batch flag on item (and overridable on sales lines) requiring a reservation or pick of the full line quantity from one batch, (b) customer sellable days (minimum remaining shelf life in days at shipment) per customer × item or item group, and (c) customer batch attribute requirements (attribute ranges per customer × item), enforced in batch reservation proposals, picking (w28) and shelf-life aware planning (w22 FR-22.20).
  - Notes: Batches not meeting the customer's requirements are excluded from proposals; override requires duty *Override customer batch requirements* with reason. Same-batch on a line with quantity larger than any single batch returns 409 `SAME_BATCH_UNAVAILABLE` with the largest available batch quantity. Sales lines arrive in w27; until then the rules apply to transfer and production picking and to fixtures.
- **FR-24.20 Lean subcontracting** — Should. The system shall model a FlowActivity of kind `service` on a vendor work cell (a Vendor resource from w9 linked to a vendor account), priced by a purchase agreement for a service item (w17), and on kanban job completion (or per period, configurable) generate or update purchase order lines for the service quantity, receive them automatically at completion, and post the service cost as conversion cost of the production flow.
  - Notes: Modes: `po-per-kanban` (one PO line per kanban job), `periodic-po` (one PO per vendor per period with released quantities accumulated, receipts per job). Material sent to the vendor moves to the vendor work cell's location via withdrawal kanbans. Vendor invoice matching uses the w18 three-way match against auto-receipts.
- **FR-24.21 Electronic batch record (EBR)** — Should. The system shall compile, for each batch order of an item whose BatchRecordProfile is active, an ElectronicBatchRecord containing: header (product, batch, formula/route version with ECO ids, batch size, dates), every posted journal line with batch/serial, attribute values with recorded-by, executed PI sheet steps and process data (w25 FR-25.16), process messages (w25 FR-25.17), equipment and cleanout records (w23), deviations (w33 nonconformances linked to the order), exceptions (any value outside limits, any override, any signature-required step), and signatures (FR-24.22); and shall route it through review (production review, QA review) with review by exception — reviewers must disposition every exception — before QA approval, after which the record is sealed (hash) and archived as PDF/A-2b plus JSON.
  - Notes: BatchRecordProfile: required sections, reviewer roles, number of signatures per stage, whether batch disposition `available` requires EBR approval (default true for GxP items). A sealed record is immutable; corrections create an addendum record linked to the original. EBR generation is incremental (sections update on events) so the review screen is current while the batch runs.
- **FR-24.22 Electronic signature service** — Must. The system shall provide a signature service usable by any service through `POST /api/v1/signatures` that records a signature only after re-authentication of the signer (Keycloak step-up: password plus a second factor; first signing in a session requires both components, subsequent signings in the same continuous session require at least one per 21 CFR 11.200(a)(1)), binds it to the signed record by a SHA-256 hash of the record's canonical JSON, and stores signer id, printed name, date/time (UTC and signer's local time zone), meaning (authored, reviewed, approved, verified, released, rejected) and reason/comment.
  - Notes: Signature strategies define for an object type and event: number of signers, sequence (parallel/serial), distinct-user rule (signers ≠ record author), required roles/duties, and the meaning per signer. Signature manifestation (11.50) is rendered on every printed or exported signed record. Signatures cannot be copied or transferred (11.70): any change to the record after signing invalidates the hash and the record shows `signature-invalidated` until re-signed. Signature log is append-only (outbox to Iceberg, 15-year retention). Three consecutive failed re-authentications lock signing for the user for 15 minutes and raise a security event (w2). Wave 48 extends this service to configured tables/fields across all modules.
- **FR-24.23 Characteristic-dependent planning** — Could. The planning engine (w22) shall carry required attribute ranges on demand (from customer batch requirements FR-24.19 or entered on the demand line) and target attribute values on supply (batch attribute values for on-hand, formula target values for planned production), and peg supply to demand only when supply attributes satisfy the demand's ranges; unmatched demand yields planned orders with the demand's attribute targets.
- **FR-24.24 Documentary batches** — Could. The system shall support a tracking policy `batch-record-only` for items that are not batch-managed in inventory: batch numbers are captured on receipts, issues and RAF and stored on InventoryTransaction for traceability and genealogy, but on-hand is not split by batch and FEFO/disposition rules do not apply.

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

**InheritanceRule / DerivationLog** (FR-24.15): InheritanceRule(id, receiver_item_id, sender_item_id or formula_line_ref, attribute_id or `shelf-life`, aggregation (enum copy/min/max/weighted-average/earliest-date/latest-date/fixed), fixed_value, applies_to (enum product/co-product/all), active); DerivationLog(id, receiver_batch_id, sender_batch_ids uuid[], rule_id, attribute_id, value_before, value_after, trigger (raf/manual/merge), derived_at, derived_by).

**BulkPackRelation** (FR-24.17): id, bulk_item_id, packed_item_id, pack_qty_per_unit numeric(18,6) (bulk UoM per packed unit), pack_bom_version_id, consolidation_window_days int default 7, active.

**CustomerBatchRequirement** (FR-24.19): id, customer_id or customer_group_id, item_id or item_group_id, sellable_days int, attribute_id nullable, min, max, same_batch (bool), valid_from, valid_to.

**ElectronicBatchRecord / EbrException / BatchRecordProfile** (FR-24.21): ElectronicBatchRecord(id, order_id, batch_id, profile_id, status (compiling/in-review/approved/sealed/addendum), content_hash bytea, pdf_document_id (w5), json_document_id, sealed_at, parent_record_id (addendum)); EbrException(id, record_id, source_type, source_id, description, severity, disposition (accepted/deviation-raised/rejected), dispositioned_by, signature_id); BatchRecordProfile(id, code, item_group_id, required_sections text[], review_stages jsonb, approval_required_for_release bool).

**Signature / SignatureStrategy** (FR-24.22): Signature(id, tenant_id, object_type, object_id, record_hash bytea, signer_subject_id, printed_name, meaning (enum), reason text, signed_at_utc timestamptz, signer_time_zone text, auth_method (enum password+otp/otp/webauthn), session_id, strategy_id, sequence int, valid (bool)); SignatureStrategy(id, object_type, event, signers int, order (parallel/serial), distinct_from_author bool, required_duties text[], meanings text[]).

Invariants (additions): a sealed ElectronicBatchRecord's content_hash equals SHA-256 of its JSON document; every EbrException of an approved record has a disposition and a signature; a Signature with valid = true has record_hash equal to the current hash of the object; DerivationLog rows are append-only.

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
| `dev.passage.lean.kanban.created.v1` | kanban generated | kanbanId, ruleId, qty, eventRefType, eventRefId, due | production, shopfloor |
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
- **BR-24.11 Weighted-average inheritance and merge.** value = Σ (v_i × q_i) / Σ q_i over senders with a value, rounded to the attribute's decimals (default 4); example: batches 200 kg at 40.0% and 300 kg at 45.0% potency → 43.0000%. Earliest-date aggregation for expiry: min(e_i).
- **BR-24.12 Consolidation sizing.** Bulk batch qty = ⌈Σ_j packed_qty_j × pack_qty_per_unit / formula_multiple⌉ × formula_multiple; if it exceeds formula maximum, split into ⌈qty / max⌉ bulk orders, allocating packing orders by due date.
- **BR-24.13 Customer sellable days.** A batch qualifies for a customer line shipping on date d if expiry − d ≥ sellable_days; if both item default and customer value exist, the customer value wins; if both customer-group and customer exist, the customer wins.
- **BR-24.14 Signature re-authentication.** A continuous session is one without idle > 10 minutes or logout; the first signature in a session requires password + second factor; subsequent signatures require password; the signer must match the session subject; a signing attempt with a mismatched subject is rejected and logged.
- **BR-24.15 EBR review by exception.** An exception is created for: a value outside limits, any override of a block (expired batch, disposition, constraint), an unsigned required step, a deviation linked to the order, a manual journal edit after posting, or a missed process message; approval is refused while any exception is undispositioned.
- **BR-24.16 Lean subcontract receipt.** Service receipt qty = completed kanban job qty; periodic POs close at period end with quantity = Σ receipts; price from the purchase agreement valid on the job completion date.

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
- **AC-24.11** (FR-24.15, BR-24.11) Given an inheritance rule `weighted-average` on potency and two ingredient batches (200 kg at 40%, 300 kg at 45%), when the batch order reports as finished, then the product batch potency is 43.0000% and the derivation monitor shows both senders.
- **AC-24.12** (FR-24.16) Given two available batches of 100 kg (expiry 2027-03-01) and 50 kg (expiry 2027-02-01), when merged into a new batch, then the target holds 150 kg with expiry 2027-02-01 and genealogy lists both sources.
- **AC-24.13** (FR-24.17, BR-24.12) Given packed orders for 400 and 600 bottles at 0.5 kg each and formula multiple 100 kg, when consolidated, then one bulk batch order of 500 kg exists and both packing orders are pegged to it.
- **AC-24.14** (FR-24.19, BR-24.13) Given customer sellable days 60 and batches expiring in 45 and 90 days, when proposing reservation for that customer, then only the 90-day batch is proposed.
- **AC-24.15** (FR-24.22, BR-24.14) Given a QA approver signing an EBR, when the second factor is wrong, then no signature is stored; after three failures signing is locked for 15 minutes and a security event exists.
- **AC-24.16** (FR-24.21, BR-24.15) Given a batch with one expired-batch override, when QA attempts approval without dispositioning the exception, then approval is refused; after disposition and signatures the record is sealed and its PDF shows signer name, meaning and UTC time.
- **AC-24.17** (FR-24.20, BR-24.16) Given a service activity on vendor V with mode `po-per-kanban`, when a kanban job of 20 completes, then a PO line for 20 service units at the agreement price is created and received.
- **AC-24.18** (FR-24.18) Given rework of batch B-77 with `keep-batch-number`, when the rework order reports as finished, then batch B-77 has the new quantity, disposition `quarantine`, and the genealogy shows the rework order.

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

**Migration addendum (FR-24.15–24.24).** AX PdsInheritance setup → InheritanceRule; AX PdsCustSellableDays/PdsCustBatchAttrib → CustomerBatchRequirement; AX PdsBulkItemConv → BulkPackRelation; AX LeanSubcontracting service activities → FlowActivity kind `service`; SAP DVSP derivation strategies → InheritanceRule (sender/receiver fields → attributes); SAP customer-material minimum remaining shelf life (KNMT) → sellable_days; SAP signature strategies (DSIG / O2) → SignatureStrategy. Historical EBRs and signature logs are not re-signed: legacy COEBR archives and AX SIGSignatureLog are loaded as read-only archived documents (w5) with their legacy hashes, linked to the batch.

### 4.24.12 Dependencies, risks, open questions

Depends on: w21 order engine; w16 batches and FEFO; w9 ECO; w6 operating units (value streams); w22 scheduled kanbans via planning.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Potency/attribute semantics vary by industry | High | Medium | Configurable role model; consultant review |
| Lean costing conflicts with order-based WIP reconciliation | Medium | High | Kanban orders are ProductionOrders — same WIP accounts; flow periods settle through the same End logic |

Open questions: (1) Should by-product `recycled` (negative issue) be supported in v1? Default yes. (2) Board per work cell vs per flow? Default work cell; decide by w24 kickoff.

### 4.24.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| Batch order number sequence | legal entity | sequence ref | `batch-order` | any | Batch order numbers | Production control parameters → Number sequences (batch order) | Order type PI01 number range (CO82/OPJH) |
| Batch number sequence | item group | sequence ref | `batch` | any | Batch numbers at RAF | Batch number groups (InventNumGroup) | Batch number assignment (OMCZ / internal number range BATCH_CLT) |
| Potency tolerance % | legal entity | numeric | 0.5 | 0–10 | Accepted potency deviation | Process manufacturing parameters → Potency | Active ingredient tolerance (proportion unit settings) |
| Default co-product allocation | item group | enum | `tca` | none/manual/tca/quantity | FR-24.4 default | Formula → Cost allocation | Apportionment structure default (OKG8 / TKKAA) |
| Shelf-advice days default | item group | int | 30 | 0–3,650 | Shelf-advice notification | Item → Shelf advice period | Minimum remaining shelf life (MARA.MHDRZ) |
| Expired batch consumption allowed | site | bool | false | true/false (duty still required) | FR-24.6 | Batch disposition / expiry blocking | Batch status management (OMCT) |
| Catch-weight variance handling | legal entity | enum | `reject` | reject / warn | BR-24.5 | CW min/max validation | CWM tolerance group |
| Inheritance active | legal entity | bool | true | true/false | FR-24.15 | Lot inheritance (PdsInheritance) | Batch derivation activation (DVMO customizing) |
| Merge override attributes | attribute | bool | false | true/false | FR-24.16 | Batch merge setup | n/a |
| Consolidation window days | bulk/pack relation | int | 7 | 1–60 | FR-24.17 | Consolidated batch orders setup | n/a |
| Rework keep batch number | legal entity | bool | true | true/false | FR-24.18 | Rework parameters | Rework order settings |
| Rework requires quality order | legal entity | bool | true | true/false | FR-24.18 | Rework parameters | QM inspection type 04 on rework |
| Customer sellable days default | item group | int | 0 | 0–3,650 | BR-24.13 | Customer sellable days | KNMT minimum remaining shelf life |
| Lean subcontract PO mode | vendor work cell | enum | `po-per-kanban` | po-per-kanban / periodic-po | FR-24.20 | Lean subcontracting purchase settings | Kanban control cycle external procurement (PK01) |
| Batch record profile | item group | ref | none | any BatchRecordProfile | FR-24.21 | n/a | EBR batch record settings (COEBR customizing) |
| EBR approval required for release | profile | bool | true | true/false | Disposition `available` gated | n/a | Batch record approval before usage decision |
| Signature strategy per event | object type × event | ref | none | any SignatureStrategy | FR-24.22 | Electronic signature setup (SIGSetup) | Signature strategies (DSIG / O2) |
| Signing lockout attempts / minutes | tenant | int / int | 3 / 15 | 1–10 / 1–1,440 | FR-24.22 | Security parameters | DSIG settings |
| Kanban card barcode symbology | site | enum | `gs1-datamatrix` | gs1-datamatrix / code128 / qr | Card print | Kanban card setup | PK card print (PK17) |
| Flow costing period length | production flow | enum | `month` | week / month | FR-24.12 | Lean costing periods | Product cost collector period (KKF6N) |

### 4.24.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Batch orders | list inquiry | status, formula, site, dates | order, formula version, batch size, scale factor, status, batch number | BatchOrder | Batch orders list page (PmfProdTable) | COOISPI process order info system |
| Batch attribute values | drill-down | item, batch, attribute | value, target, min, max, pass, recorded by | BatchAttributeValue | Batch attributes form (PdsBatchAttributes) | MSC3N batch classification |
| Batch genealogy (top-down / bottom-up) | drill-down | batch | tree of parent/child batches, orders, qty | InventoryTransaction genealogy + DerivationLog | Batch tracing (InventTrackingRegisterTrans), Item tracing | MB56 batch where-used list |
| Derivation monitor | list inquiry | receiver batch, date, status | rule, senders, values before/after, missing values | DerivationLog | Lot inheritance inquiry | DVMO |
| Co-product allocation | operational report | order, period | method, %, allocated cost by item | CoProductLine | Co-product cost allocation inquiry | KKF6N / CO88 settlement log |
| Shelf-life / expiry report | operational report | site, expiry within n days | batch, item, qty, expiry, best before, shelf advice | InventoryBatch | SSRS "Batch expiry" / shelf-life inquiry | MB5M BBD/SLED list |
| Kanban board / kanban overview | list inquiry | work cell, rule, state | card, item, qty, due, state, order | Kanban, KanbanJob | Kanban board (KanbanBoardScheduleView), Kanbans list | PK13N, PK18 |
| Kanban rule analysis | KPI | rule, period | cycles, average lead time, overdue %, kanbans active vs max | Kanban | Kanban rule inquiry | PK13N control cycle evaluation |
| Flow costing period | operational report | flow, period | material, conversion, output, variance | FlowCostingPeriod | Lean costing (backflush costing) inquiry | KKF6N, MF41 |
| Electronic batch record list | list inquiry | status, product, date, reviewer | record, batch, open exceptions, stage, sealed at | ElectronicBatchRecord | n/a | COEBR batch record list |
| Signature log | list inquiry | object type, signer, date | object, meaning, signer, UTC time, reason, valid | Signature | Electronic signature log inquiry (SIGSignatureLog) | DSIG signature log (DSAL) |
| Customer batch requirement check | list inquiry | customer, item | requirement, qualifying batches, qty available | CustomerBatchRequirement × InventoryBatch | Customer sellable days inquiry | Batch determination log (VCH1/VB(J)) |

### 4.24.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Batch ticket (picking list for batch order) | Release | Header: batch order, formula version, batch size, scale factor; lines: ingredient, qty, potency-adjusted qty, batch, weigh station; signatures block (weighed by / verified by) | PDF, kiosk (w25) | `prod-doc` | Part of EBR for GxP items |
| Kanban card | Kanban creation, reprint | Item, qty, CW qty, source/destination location, rule, GS1 DataMatrix `(240)kanbanId`, due | PDF (card stock 4×6 in / A6) | Kanban card number per rule | None |
| Batch label | RAF | Item, batch, manufacturing date, expiry/best-before, catch weight, GS1-128 `(01)(10)(17)(310n)` | ZPL via w32 printers, PDF | batch sequence | Food/pharma labeling rules per country (w48) |
| Certificate of analysis (batch attributes) | Batch release, on demand | Batch, attribute, specification, result, release signature | PDF, email, EDI (w35) | `coa` | Customer contract; signature manifestation if signed (FR-24.22) |
| Electronic batch record | QA approval (sealed) | Full EBR sections, exceptions with dispositions, signature manifestation (printed name, meaning, date/time) | PDF/A-2b + JSON, SHA-256 hash | `ebr` per legal entity | 21 CFR 211.180: at least 1 year after batch expiry (default expiry + 1 year, min 5 years); EU GMP Annex 11 / EudraLex Vol 4 Ch 4: 1 year after expiry or 5 years after certification, whichever longer |
| Signature manifestation | Any signed record print/export | Signer printed name, date/time, meaning | Embedded in PDF | n/a | 21 CFR 11.50 |
| Lean service purchase order | FR-24.20 | Vendor, service item, qty, agreement reference | PDF, EDI ORDERS (w35) | PO sequence (w17) | Purchasing retention (w17) |

### 4.24.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Shelf-advice notification | daily 06:00 site-local | site | One notification per batch per advice date | < 1 min | Retry next run | Delivery failures warn |
| Consolidate batch orders | daily 21:00 (opt-in) + on demand | site × bulk item | Proposal per window replaced; firm on confirm | < 2 min | Per-bulk errors listed | Result report |
| Inheritance re-derivation | on attribute change event + nightly 02:30 | receiver item | DerivationLog per (batch, rule, trigger id) | < 5 min for 10k batches | Max depth 10; cycles stop | Missing-sender count reported |
| Flow period settlement | monthly (optional auto) | production flow | One settlement per (flow, period); blocked with open kanbans | < 5 min | 409 with open kanbans list | Failure pages |
| EBR compilation | event-driven + hourly sweep | batch order | Section upserts by source event id | < 10 s per event | Sweep repairs missed events | Missed events > 0 warns |
| EBR archive and seal | on approval | record | Seal once; hash stored; archive retry | < 30 s | Retry 5×; record stays approved-unsealed | Unsealed > 1 h pages |
| Lean periodic PO close | period end | vendor | Close once per (vendor, period) | < 1 min | Retry | Failure warns |
| Fixed-kanban count suggestion | weekly Monday 05:00 | rule | Suggestion per rule; not applied automatically | < 1 min | Non-blocking | Deviations > 25% reported |

### 4.24.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| Maintain formulas | Formula designer, formula ECO submit | with *Approve engineering change* (w9) | Formulator, Production engineer |
| Maintain batch orders | batch-orders create, `:rescale`, consolidation, rework orders | none | Production supervisor, Planner |
| Maintain batch attributes | PUT batch attributes, manual derivation | with *Maintain batch disposition* for the same batch flagged | Quality technician |
| Maintain batch disposition | `:disposition` | with *Maintain batch attributes* (same batch) | QA manager |
| Merge batches | batch `:merge` | none | Inventory supervisor |
| Consume expired batch | picking override with reason | with *Maintain batch disposition* flagged | Production supervisor |
| Override customer batch requirements | FR-24.19 override | none | Sales supervisor (w27) |
| Maintain production flows / kanban rules | flows, activities, rules, service activities | none | Lean engineer |
| Work kanban board | board, job complete, card print | none | Operator, Shop floor supervisor |
| Settle production flows | flow period settle | with *Maintain production flows* | Cost accountant |
| Review batch records | EBR review stage, exception disposition | with *Approve batch records* (same record) | Production reviewer |
| Approve batch records | EBR QA approval, seal | with *Review batch records* and with *Maintain batch orders* (same order) | QA manager |
| Maintain signature strategies | SignatureStrategy setup | with *Approve batch records* | Compliance administrator |

### 4.24.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| PMF-0001 | error | Percent-controlled lines sum to {sum}%, not 100%. | Formula validation | Adjust line percentages | FR-24.1 |
| PMF-0002 | error | Compensating line {line} would become negative ({qty}). | Potency adjustment | Choose another batch or change formula | BR-24.2 (`POTENCY_COMPENSATION_NEGATIVE`) |
| PMF-0003 | error | Batch {batch} expired on {date}. | Expired batch picked | Pick another batch or use override duty | BR-24.10 (`BATCH_EXPIRED`) |
| PMF-0004 | error | Catch weight {qty} is outside {min}–{max} for {cw} units. | BR-24.5 | Re-weigh | BR-24.5 |
| PMF-0005 | warning | Batch {batch} potency {p}% is outside {min}–{max}%; not pickable. | FR-24.3 | Choose batch in range | FR-24.3 |
| PMF-0006 | error | Merge refused: attribute {attr} differs ({a} vs {b}). | FR-24.16 | Align values or mark merge-override | FR-24.16 |
| PMF-0007 | warning | Inheritance rule {rule}: sender value missing for {attr}. | FR-24.15 | Record sender attribute and re-derive | FR-24.15 |
| PMF-0008 | error | No single batch can supply {qty} (largest {max}). | Same-batch requirement | Split line or wait for supply | FR-24.19 (`SAME_BATCH_UNAVAILABLE`) |
| PMF-0009 | warning | Batch {batch} does not meet customer {customer} requirements ({rule}). | FR-24.19 | Choose qualifying batch or override | FR-24.19 |
| PMF-0010 | error | Signature rejected: re-authentication failed. | BR-24.14 | Retry with correct credentials | FR-24.22 |
| PMF-0011 | error | Signing locked for {minutes} minutes after {n} failed attempts. | Lockout | Wait or contact security | FR-24.22 |
| PMF-0012 | error | Signer must differ from the record author. | Distinct-user rule | Another user signs | FR-24.22 |
| PMF-0013 | warning | Record changed after signing; signature invalidated. | Hash mismatch | Re-sign | FR-24.22 |
| PMF-0014 | error | Batch record cannot be approved: {n} exceptions without disposition. | BR-24.15 | Disposition exceptions | FR-24.21 |
| PMF-0015 | error | Batch {batch} cannot be released: batch record not approved. | Profile gating | Complete EBR approval | FR-24.21 |
| LEAN-0001 | error | Kanban move {from} → {to} is not allowed. | BR-24.6 | Use allowed move | BR-24.6 |
| LEAN-0002 | error | Rule {rule} already has {n} active kanbans (max {max}). | BR-24.7 | Empty a kanban or raise count | BR-24.7 |
| LEAN-0003 | error | Period cannot be settled: {n} kanbans open. | BR-24.9 | Complete kanbans | BR-24.9 (`FLOW_PERIOD_OPEN_KANBANS`) |
| LEAN-0004 | error | Flow activity {activity} has no output location. | FR-24.8 connectivity | Set output | FR-24.8 |
| LEAN-0005 | error | No valid purchase agreement for service {item} on {date}. | FR-24.20 | Create or extend agreement | FR-24.20 |

### 4.24.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation under 3× load |
|---|---|---|---|---|---|---|
| Batch order create + scale | 500/day; peak 20/min | 150 / 400 / 900 ms | 50/s | ~5 GB (orders + lines) | as w21 | Queue behind HPA |
| Potency proposal | 2,000/day | 100 / 300 / 700 ms | 50/s | n/a | BatchAttributeValue (batch_id) | Proposal without potency sort (warning) |
| Event kanban generation | 10,000/day; peak 200/min | 1 / 5 / 8 s | 50 kanbans/s | Kanban + jobs ~ 10M rows ≈ 4 GB | Kanban (rule_id, state); KanbanJob (activity, state, planned_start) | SLA alert; backlog processed FIFO |
| Board load | 20,000 loads/day | 150 / 500 / 1,000 ms | 200/s | n/a | as above | SSE falls back to 30 s polling |
| Inheritance at RAF | 500 RAFs/day × 20 attributes | 50 / 200 / 500 ms added to RAF | n/a | DerivationLog ~ 4M rows ≈ 1 GB | (receiver_batch_id) | Deferred to async with flag `pending-derivation` |
| EBR compile + seal | 500 batches/day, 2,000 events each | event apply p95 < 2 s; seal p95 < 30 s | 1M events/day | EBR JSON+PDF ~ 5 MB each → 0.9 TB/yr in object storage | record by order; documents in object storage | Compilation lag allowed to 5 min; review screen shows lag |
| Signature | 5,000/day | 300 ms / 1 s / 2 s (excl. user input) | 20/s | Signature 2M rows ≈ 1 GB/yr | (object_type, object_id) | Step-up auth latency dominated by Keycloak; no degradation of hash check |

### 4.24.20 Localization and compliance hooks

- **21 CFR Part 11 (US) and EU GMP Annex 11**: FR-24.22 implements 11.10(e) audit trail via the signature log and w5 audit, 11.50 manifestation, 11.70 record linkage, 11.100 uniqueness (Keycloak subject), 11.200 two-component re-authentication, 11.300 controls (password policy and lockout from w2). Validation package (IQ/OQ scripts, traceability matrix to these clauses) is a wave 48 deliverable; this wave provides the OQ test cases from 4.24.9 and AC-24.15/AC-24.16.
- **Retention**: EBR retention per country/regime (US 21 CFR 211.180: expiry + 1 year; EU: max(expiry + 1 year, 5 years after certification); some countries require 10 years for blood/plasma products) set by the w48 retention policy; legal hold blocks purge.
- **Food labeling**: batch label content (allergen declaration, lot code formats, best-before vs use-by wording, e.g., EU Regulation 1169/2011) is rendered by country-specific templates in wave 48 packs via the `production.label.render` extension point.
- **Catch weight**: legal-for-trade weighing (OIML R76 / NIST Handbook 44) is a device concern; Passage stores the scale id and certification reference on catch-weight RAF lines when provided.
- **Excise / alcohol**: bonded-production reporting (US TTB Form 5110.40, EU EMCS) is a wave 48 country-pack item fed from batch order consumption and RAF events through `production.statutory-extract`.
- **Language**: batch tickets, CoA and EBR render in the site language; signature meanings are stored as codes and rendered per locale.
- **Extension points**: `production.label.render`, `production.statutory-extract`, `signature.strategy.resolve` (OPA), `ebr.section.provider` (additional EBR sections such as environmental monitoring).

### 4.24.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.14.1 Inventory — Batch merge (PI) (PdsBatchMerge) [AX #122] | PARTIAL-24 | ADDED as FR-24.16 | Attribute recomputation BR-24.11 |
| AX 12.23.1 PI — Consolidated batch orders (PdsConsolidatedOrder) [AX #196] | PARTIAL-24 | ADDED as FR-24.17 | Bulk → packed with pegged packing orders |
| AX 12.23.1 PI — Rework batch order (PdsRework) [AX #197] | PARTIAL-24 | ADDED as FR-24.18 | Keeps batch identity by default |
| AX 12.23.1 PI — Batch attribute inheritance (PdsInheritance) [AX #198] | PARTIAL-24 | ADDED as FR-24.15 | Product-to-product and co-product inheritance |
| AX 12.23.1 PI — Batch merge (PdsBatchMerge) [AX #201] | PARTIAL-24 | ADDED as FR-24.16 | Same capability as #122 |
| AX 12.23.1 PI — Same-batch reservation (PdsSameBatch) [AX #202] | PARTIAL-24 | ADDED as FR-24.19 | Sales-line enforcement active from wave 27 |
| AX 12.23.1 PI — Customer-specific batch attribute requirements (PdsCustSellableDays, PdsCustBatchAttrib) [AX #203] | PARTIAL-24 | ADDED as FR-24.19 | BR-24.13 |
| AX 12.23.3 PI Periodic — Consolidate batch orders (batch) [AX #205] | PARTIAL-24 | ADDED as FR-24.17 | Job in 4.24.16 |
| AX 12.23.3 PI Periodic — Attribute inheritance processing on RAF [AX #206] | PARTIAL-24 | ADDED as FR-24.15 | Re-derivation job in 4.24.16 |
| AX 12.23.4 PI Setup — Lot inheritance setup [AX #208] | PARTIAL-24 | ADDED as FR-24.15 | InheritanceRule entity |
| AX 12.23.4 PI Setup — Customer sellable days / customer batch attribute requirements [AX #211] | PARTIAL-24 | ADDED as FR-24.19 | CustomerBatchRequirement entity |
| AX 12.23.4 PI Setup — Rework parameters [AX #212] | PARTIAL-24 | ADDED as FR-24.18 | Parameters in 4.24.13 |
| AX 12.23.4 PI Setup — Bulk/pack item relations [AX #213] | PARTIAL-24 | ADDED as FR-24.17 | BulkPackRelation entity |
| AX 12.24.1 Lean — Subcontracting in lean (LeanSubcontracting) [AX #214] | PARTIAL-24 | ADDED as FR-24.20 | Service activity on vendor work cell |
| AX 12.24.3 Lean Periodic — Generate purchase orders for service activities [AX #215] | PARTIAL-24 | ADDED as FR-24.20 | Lean periodic PO close job |
| AX 12.24.4 Lean Setup — Subcontracting setup (service items, purchase agreements) [AX #216] | PARTIAL-24 | ADDED as FR-24.20 | Agreement pricing per BR-24.16 |
| SAP 13.22 PP-PI — COEBR electronic batch record [SAP #257] | GAP (propose 24) | ADDED as FR-24.21 | Review by exception, sealed PDF/A + JSON |
| SAP 13.22 PP-PI — Digital signature in PI sheets / batch records (DSIG) [#261] | GAP (propose 24) | ADDED as FR-24.22 | Signature service reused by w25 PI sheets and generalized in w48 |
| SAP 13.22 PP-PI — Batch derivation (DVMO / DVMAN) [#262] | PARTIAL-24 | ADDED as FR-24.15 | Manual derivation included |
| SAP 13.22 PP-PI Reports — COEBR batch record list [#264] | GAP (propose 24) | ADDED as FR-24.21 | Report in 4.24.14 |
| SAP 13.22 PP-PI Periodic — COEBR batch record generation/archive [#269] | GAP (propose 24) | ADDED as FR-24.21 | EBR compilation and seal jobs in 4.24.16 |
| SAP 13.22 PP-PI Customizing — EBR batch record profiles, archiving, approval workflow [#274] | GAP (propose 24) | ADDED as FR-24.21 | BatchRecordProfile |
| SAP 13.22 PP-PI Customizing — Batch management in PP-PI (COB1 batch determination, class 023, derivation) [#275] | PARTIAL-24 | ADDED as FR-24.15 (derivation); batch determination SUPERSEDED by w16 FEFO batch selection + FR-24.19 | Class 023 characteristics = BatchAttribute (FR-24.5) |
| SAP 13.23 PP/DS — Characteristic-dependent planning (CDP) [#277] | PARTIAL-24 | ADDED as FR-24.23 | Engine change lives in the w22 planning service |
| SAP 13.33 Batch/Serial — DVMO / DVSP / DVMAN derivation [#365] | PARTIAL-24 | ADDED as FR-24.15 | — |
| SAP 13.33 Batch/Serial — Documentary batches (S/4) [#367] | PARTIAL-24 | ADDED as FR-24.24 | Tracking policy `batch-record-only` (w16 tracking dimension group) |
| SAP 13.33 Batch/Serial Reports — DVMO derivation monitor [#369] | PARTIAL-24 | ADDED as FR-24.15 | Derivation monitor in 4.24.14 |
| SAP 13.33 Batch/Serial Periodic — Batch derivation runs (DVMO) [#370] | PARTIAL-24 | ADDED as FR-24.15 | Re-derivation job |
| SAP 13.33 Batch/Serial Customizing — Batch derivation activation, sender/receiver fields, rules [#371] | PARTIAL-24 | ADDED as FR-24.15 | Parameter "Inheritance active" |
| SAP 13.33 Batch/Serial Customizing — Documentary batch activation (S/4) [#373] | PARTIAL-24 | ADDED as FR-24.24 | Activation via tracking policy |
| SAP 13.35 DMS Customizing — Digital signature for documents (signature strategies) [#397] | GAP (propose 24) | ADDED as FR-24.22 | SignatureStrategy covers any object type, including w5 documents |

Scope note: the 4.24.1 out-of-scope line "PI sheets with electronic signatures (w33 quality covers signatures)" is narrowed by this closure. Electronic signatures are delivered here (FR-24.22) and PI sheets in wave 25 (FR-25.16). Wave 33 uses the same signature service for quality orders, and wave 48 extends it to configured tables and fields (AX register rows #22–#25, wave 48).

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
| Control recipes / process instructions | n/a (route notes, D365 production floor execution instructions) | Control recipe CO53/CO53XT, process instruction categories (O10C/O12C/O13C), control recipe destinations | ControlRecipe generated at batch-order release, sent to kiosk or PCS (FR-25.15) | full |
| PI sheets / XSteps | n/a | PI sheet CO60/CO60XT/CO60_PI, worklist, standard XSteps repository (CMX, SXS) | PiSheetTemplate + StepLibrary, kiosk PI sheet execution (FR-25.16) | full |
| Process messages | n/a | Process messages CO54/CO54XT/CO57/CO58, message categories and destinations (O05C/O06C/O07C) | ProcessMessage with categories, destinations and monitor (FR-25.17) | full |
| PI sheet archive | n/a | CO64 / CO67 archive and print | PDF/A + JSON archive feeding EBR (FR-25.18) | full |
| Edge connector deployment | D365 IoT Intelligence (Azure IoT Hub based) | SAP Plant Connectivity (PCo) installation, agents, notification processes | `iot-edge` deployable with enrollment, mTLS, ringed rollout (FR-25.19) | improved |

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
- **FR-25.8 Approval and export** — Must. Supervisors shall approve per worker-day (bulk by team), with edits audited; approved days are locked and exported as `dev.passage.shopfloor.payday.approved.v1` for w43 and as CSV.
- **FR-25.9 IoT ingestion** — Must. The `iot-edge` connector shall subscribe to OPC-UA nodes (via `gopcua`) and MQTT topics (Mosquitto/EMQX, QoS 1), normalize to the MachineEvent schema (BR-25.4), buffer locally (SQLite ring, 24 h), and publish to Kafka topic `shopfloor.machine-events` with idempotent producer and per-machine monotonic sequence.
- **FR-25.10 OEE** — Must. The system shall compute OEE per resource per 15-minute bucket and shift (BR-25.5) from machine events and planned time from the w23 schedule/calendar, with dashboard lag p95 < 5 s.
- **FR-25.11 Downtime and andon** — Must. The system shall open a DowntimeRecord when a machine reports DOWN or no count for > `idle_threshold_s` (default 300), require a reason code within the kiosk, and raise an AndonAlert (kiosk banner + notification) on downtime > 10 min, scrap rate > 5% in a bucket, or quality fail.
- **FR-25.12 Offline kiosk** — Must. The PWA shall queue registrations and feedback in IndexedDB while disconnected (≥ 30 min, ≤ 5,000 actions), replay in order on reconnect with client-generated UUIDs for idempotency, and resolve conflicts per BR-25.7.
- **FR-25.13 Machine-to-job linkage** — Should. Machine counts shall be attributed to the job currently running on that resource (from registrations or the schedule), and may auto-fill good/scrap quantities on the kiosk for operator confirmation.
- **FR-25.14 Supervisor view** — Must. A supervisor workspace shall show who is clocked in, current jobs, open downtimes, OEE tiles, and pending approvals.
- **FR-25.15 Control recipes** — Should. On release of a batch order (w24) or production order whose route operations carry process instructions, the system shall generate a ControlRecipe per destination (kiosk PI sheet, or a process control system through the edge connector) containing the ordered process instructions of each phase with their category (instruction text, process parameter setpoint, process data request, calculation, inspection characteristic, material list, signature request, sequence definition), and shall track its status (created, sent, received, in-execution, completed, error) in a control recipe monitor with resend.
  - Notes: Destination types: `kiosk` (rendered as a PI sheet), `pcs-opcua` (setpoints written to allowlisted OPC-UA nodes through FR-25.19; process data requests become subscriptions), `pcs-mqtt` (JSON payload to a configured topic, ISA-88 recipe structure: procedure → unit procedure → operation → phase). Process instruction categories are tenant master data with characteristic definitions (data type, unit, limits). Control recipes are generated with values scaled to the batch (BR-24.1) and with potency-adjusted quantities (BR-24.2).
- **FR-25.16 PI sheets and step library** — Should. The system shall provide a PI sheet designer and a versioned StepLibrary (reusable standard steps, SAP XSteps equivalent) with step types: instruction (markdown with images), numeric input (unit, limits, mandatory), text/enum input, checklist, calculation (expression over prior inputs and order data), material consumption (posts picking-list lines), yield/RAF report, machine readout (reads an OPC-UA/MQTT tag value via the edge), timer/hold time, signature (FR-24.22 with meaning), and conditional branch; operators execute PI sheets on the kiosk with a worklist of PI sheets per work cell.
  - Notes: Step libraries are versioned and released through ECO (w9); a PI sheet template references step versions, so later library changes do not alter running batches. Out-of-limit inputs require a comment and create an EBR exception (BR-24.15); configured steps can block continuation until a supervisor signs. Steps that post (consumption, yield) generate w21 journals immediately. PI sheets work offline under FR-25.12 except machine readout and signature steps, which queue until reconnect and block completion of the sheet.
- **FR-25.17 Process messages** — Should. The system shall accept ProcessMessages from kiosk PI sheets, from the edge connector (PCS events) and by manual entry, each with a category (phase-start, phase-end, consumption, yield, process-data, quality-result, deviation, order-status, custom) and characteristic values, validate them against the category definition, and deliver them to the category's destinations: production journal posting (w21), quality inspection results (w33), EBR section (w24), Kafka topic `shopfloor.process-messages` for external consumers, or an outbound webhook (w35).
  - Notes: Process message monitor lists messages by status (received, delivered, partially-delivered, error) with per-destination results; failed destinations retry with exponential backoff (1, 5, 15, 60 min) then stay in error for manual reprocess or deletion (with reason). Messages are idempotent by (source, sourceMessageId). Manual creation requires duty *Create process messages*.
- **FR-25.18 PI sheet archive** — Should. On completion of a PI sheet the system shall render it to PDF/A-2b and canonical JSON (inputs, calculated values, timestamps, signatures with manifestation), compute a SHA-256 hash, store both in the w5 document store with the batch order's retention policy, link them to the EBR, and allow print and retrieval by order, batch or date.
- **FR-25.19 Edge connector as a deployable** — Must. The `iot-edge` connector shall ship as a signed OCI image (amd64/arm64) with a Helm chart for the plant k3s node and a single static binary for Windows/Linux industrial PCs, and shall support: enrollment with a one-time token that obtains an mTLS client certificate (issued by the cluster CA via cert-manager, 90-day validity, auto-renew at 30 days), configuration pulled from the shopfloor service (machines, OPC-UA endpoints and node ids, MQTT topics, tag mappings, write allowlist) as versioned documents, heartbeat every 30 s with buffer fill and per-machine status, ringed rollout of connector versions (canary → ring 1 → all, per plant), local admin page on localhost for diagnostics, and write-back (control recipe setpoints) restricted to allowlisted nodes with per-write audit.
  - Notes: OPC-UA security modes None, Sign and SignAndEncrypt with policies Basic256Sha256 and Aes128_Sha256_RsaOaep; server certificate trust list managed from the central UI. MQTT 3.1.1/5.0 with TLS and per-edge credentials. Store-and-forward per FR-25.9 (SQLite ring, 24 h) survives connector restart and OS reboot. Upgrades are atomic (A/B binary swap) with automatic rollback when heartbeat fails within 5 minutes. Supported deployments are documented as certified configurations (k3s 1.30+, Windows Server 2019+/IoT Enterprise, Ubuntu 22.04+). Runs without inbound ports: all traffic is outbound to Kafka (via the shopfloor ingest gateway over HTTPS/2 or Kafka TLS) and the config endpoint.

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

**ControlRecipe / ProcessInstruction** (FR-25.15): ControlRecipe(id, order_id, destination_id, status (created/sent/received/in-execution/completed/error), generated_at, sent_at, payload_hash, error_text, version); ProcessInstruction(id, control_recipe_id, phase_path text (ISA-88 path), sequence int, category_id, characteristic values jsonb, target_node (nullable OPC-UA node id/topic)); ProcessInstructionCategory(id, code, kind, characteristics jsonb [{name, type, unit, min, max}]); RecipeDestination(id, site_id, type (kiosk/pcs-opcua/pcs-mqtt), edge_id, address, active).

**PiSheetTemplate / LibraryStep / PiSheetRun / PiStepResult** (FR-25.16): LibraryStep(id, code, version, type (enum per FR-25.16), definition jsonb, status (draft/released/obsolete), eco_id); PiSheetTemplate(id, operation_ref (route version + oper), version, steps jsonb [{libraryStepId, version, overrides}]); PiSheetRun(id, order_id, oper_number, template_id, status (open/in-progress/held/completed), started_at, completed_at, archive_pdf_id, archive_json_id, content_hash); PiStepResult(id, run_id, step_seq, value jsonb, out_of_limit bool, comment, recorded_by, recorded_at, signature_id nullable, offline bool).

**ProcessMessage / ProcessMessageDelivery** (FR-25.17): ProcessMessage(id, source (kiosk/edge/manual), source_message_id, category_code, order_id, phase_path, values jsonb, occurred_at, received_at, status); ProcessMessageDelivery(message_id, destination, status, attempts int, last_error, delivered_at); unique (tenant_id, source, source_message_id).

**EdgeNode / EdgeConfigVersion** (FR-25.19): EdgeNode(id, site_id, name, platform (k3s/windows/linux), connector_version, ring (canary/ring1/all), cert_serial, cert_expires_at, last_heartbeat_at, buffer_fill_pct, status); EdgeConfigVersion(id, edge_id, version int, document jsonb, published_at, published_by, applied_at); EdgeWriteAudit(id, edge_id, node_id, value, control_recipe_id, actor, written_at, result).

Invariants (additions): every completed PiSheetRun has an archive_json_id whose hash equals content_hash; a ProcessMessage with status delivered has all destinations delivered; an EdgeNode accepts only one applied EdgeConfigVersion at a time and writes only to nodes in that version's allowlist.

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
| `dev.passage.shopfloor.registration.posted.v1` | registration stored | registrationId, workerId, kind, jobId, hours, qtyGood, qtyScrap, offline | production (job card), analytics |
| `dev.passage.shopfloor.machine.event.v1` | edge publish | see BR-25.4 | shopfloor OEE, production, maintenance (w44) |
| `passage.shopfloor.oee.bucket-updated.v1` | bucket recomputed | resourceId, bucketStart, availability, performance, quality, oee | dashboards (w46) |
| `dev.passage.shopfloor.andon.raised.v1` | alert condition | alertId, resourceId, kind, severity, jobId, startedAt | notifications, kiosk |
| `dev.passage.shopfloor.payday.approved.v1` | approval | workerId, workDate, payLines[{type, hours, multiplier, amount}], approvedBy | payroll (w43) |

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
- **BR-25.4 Machine event schema.** CloudEvents envelope `type: dev.passage.shopfloor.machine.event.v1`, `source: urn:passage:iot-edge:<site>:<edgeId>`, `subject: <machineId>`, `id: <machineId>-<seq>`, `time: <device ts RFC 3339>`, `datacontenttype: application/json`, `data`: `{ "machineId": "M-101", "resourceId": "uuid", "seq": 184233, "kind": "count|state|downtime|alarm|measurement", "ts": "2026-10-21T08:15:03.250Z", "goodCount": 12, "rejectCount": 1, "state": "RUNNING", "reasonCode": null, "measurement": {"name": "temp_c", "value": 71.4, "unit": "Cel"}, "jobId": null, "tags": {"line": "L1"} }`. Counts are deltas since the previous count event; absolute counters are converted at the edge with rollover detection (32-bit). Events older than 7 days or with seq ≤ last seen are dropped and counted in a metric.
- **BR-25.5 OEE formula (ISO 22400 aligned).** Availability = run_minutes / planned_minutes; Performance = (ideal_cycle_s × total_count / 60) / run_minutes, capped at 1.0 with an over-rate flag; Quality = good_count / total_count (1.0 if total = 0); OEE = A × P × Q. Planned minutes = scheduled open minutes minus planned downtime (category planned). Example shift: planned 480 min, unplanned down 60 → A = 0.875; ideal cycle 60 s, total 380 → P = 380 / 420 = 0.9048; good 361 → Q = 0.95; OEE = 0.7522. Buckets roll up to shift/day by summing minutes and counts, not by averaging ratios.
- **BR-25.6 Idle detection.** No count event for idle_threshold_s while state = RUNNING flips the bucket to IDLE from the last count; a later count closes the idle gap.
- **BR-25.7 Offline conflict resolution.** Replayed registrations are accepted in client timestamp order; a job-start for a job that was finished server-side while offline is accepted but flagged `job-finished-conflict` for supervisor review; a duplicate client UUID is a no-op; clock skew > 5 min between device and server is corrected by the offset measured at reconnect and recorded.
- **BR-25.8 Pay thresholds.** Weekly overtime counts hours after 40.00 across the pay week (profile-defined start day); daily and weekly overtime do not double-count: hours already paid at 1.5× daily are excluded from the weekly threshold calculation.
- **BR-25.9 Posting to production.** Job-card journals from registrations post at cost_rate of the worker (if set) else the resource's cost category; a registration edited after posting creates a reversing journal and a new one.
- **BR-25.10 Andon de-duplication.** One open AndonAlert per (resource, kind); re-raising extends it; auto-closes when the condition clears for 5 min.
- **BR-25.11 Control recipe resend.** A control recipe in error can be resent up to 5 times; resending after any process message for the order was received requires `force` and a reason, because the PCS may have partially executed it.
- **BR-25.12 PI sheet step completion.** A sheet completes only when every mandatory step has a value, every out-of-limit value has a comment, every signature step has a valid signature, and no queued offline step remains; a held sheet (supervisor hold) cannot accept inputs except a release signature.
- **BR-25.13 Process message validation.** Values are validated against the category's characteristic types and limits; out-of-limit values are accepted with flag and an EBR exception unless the category is `reject-out-of-limit`; unknown order or phase → status error without delivery.
- **BR-25.14 Edge certificate rotation.** Renewal starts when remaining validity < 30 days; if renewal fails for 7 consecutive days an alert pages; an expired certificate stops publishing but buffering continues (24 h ring).
- **BR-25.15 Edge write safety.** A setpoint write requires the node to be on the allowlist, a value within the instruction's limits, and an active control recipe in-execution for the order on that machine; every write and its readback value are audited; a readback mismatch raises a process message of category deviation.

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
- **AC-25.11** (FR-25.15, BR-25.15) Given a released batch order with a phase setpoint of 72.0 °C for node `ns=2;s=Vessel1.TempSP` on the allowlist, when the control recipe is sent to `pcs-opcua`, then the edge writes 72.0, the readback is audited, and the control recipe status is in-execution.
- **AC-25.12** (FR-25.16, BR-25.12) Given a PI sheet with a mandatory numeric step limits 6.5–7.5 and a signature step, when the operator enters 7.8 without a comment, then completion is refused; with a comment and a valid signature it completes and an EBR exception exists.
- **AC-25.13** (FR-25.17) Given a process message of category quality-result with destinations EBR and w33, when the w33 destination is unavailable, then the message is partially-delivered, retried at 1, 5, 15 and 60 min, and finally delivered without duplicating the EBR entry.
- **AC-25.14** (FR-25.18) Given a completed PI sheet, when archived, then a PDF/A-2b and JSON exist in the document store with equal content hash recorded on the run, and the EBR links to them.
- **AC-25.15** (FR-25.19, BR-25.14) Given a new edge node enrolled with a one-time token, when it starts, then it obtains a certificate, pulls config version 1, and heartbeats within 30 s; a canary upgrade whose heartbeat fails rolls back automatically within 5 minutes.

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

### 4.25.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| Kiosk idle timeout min | site | int | 10 | 1–60 | Session logout | Manufacturing execution parameters → Terminal → Log off after | PP-PDC terminal settings |
| Badge login mode | site | enum | `badge-or-pin` | badge / pin / badge-or-pin | FR-25.1 | Manufacturing execution parameters → Terminal → Badge ID | DM/MII user login |
| Post job card on stop | site | enum | `immediate` | immediate / at-approval | FR-25.4 | Manufacturing execution parameters → Production orders → Transfer (route card journal creation) | Confirmation parameters OPK4 (post immediately vs PDC upload) |
| Bundle split basis | site | enum | `quantity` | quantity / equal | BR-25.1 | Job bundle setup (JmgParameters bundle) | n/a |
| Auto-stop previous job | site | bool | true | true/false | FR-25.3 | Manufacturing execution parameters → Auto stop | n/a |
| Clock-in tolerance before / after min | profile | int / int | 10 / 5 | 0–60 | BR-25.2 | Profile (JmgProfileTable) flex/tolerance | Work schedule rule tolerances (T550A) |
| Rounding minutes | profile | int | 1 | 1–15 | BR-25.2 | Profile rounding | Time evaluation rounding rules |
| Missing clock-out close after h | site | int | 4 | 1–24 | BR-25.3 | Calculation → missing clock-out | Time evaluation error message (RPTIME00) |
| Overtime daily / weekly thresholds | pay agreement | numeric | 8.00 / 40.00 | 0–24 / 0–168 | FR-25.7 | Pay agreement (JmgPayAgreement) | Wage types / time evaluation schema |
| Idle threshold s | machine | int | 300 | 30–3,600 | BR-25.6 | n/a | PCo notification settings |
| Andon downtime min / scrap % | site | int / numeric | 10 / 5 | 1–240 / 0–100 | FR-25.11 | n/a | DM andon rules |
| OEE bucket minutes | tenant | int | 15 | 5 / 15 / 60 | FR-25.10 | n/a | DM OEE time granularity |
| Offline max actions | site | int | 5,000 | 100–20,000 | FR-25.12 | n/a | n/a |
| Edge buffer hours | edge node | int | 24 | 1–168 | FR-25.9 | n/a | PCo storage and forward settings |
| Edge rollout ring | edge node | enum | `all` | canary / ring1 / all | FR-25.19 | n/a | PCo agent versioning (manual) |
| Edge write allowlist | edge node | list | empty | OPC-UA node ids / topics | BR-25.15 | n/a | PCo destination system write permissions |
| Control recipe destination per resource | resource | ref | `kiosk` | RecipeDestination | FR-25.15 | n/a | Control recipe destination (O10C) |
| Process message category destinations | category | list | per category | destinations | FR-25.17 | n/a | Message category destinations (O05C/O06C/O07C) |
| PI sheet hold requires signature | template | bool | true | true/false | BR-25.12 | n/a | PI sheet XStep signature settings |
| Process message retry schedule | tenant | list | 1,5,15,60 min | any | FR-25.17 | n/a | CO54 send interval |

### 4.25.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Who is clocked in | list inquiry | site, work cell | worker, clock-in time, current job, elapsed | TimeRegistration | Manufacturing execution → Clocked-in workers (JmgTermRegList) | PP-PDC attendance, CATS |
| Registrations for approval | list inquiry | team, date, flags | worker, date, registrations, pay lines, flags | TimeRegistration × PayLine | Approve (JmgCalcApprove form) | CATS approval (CATS_APPR_LITE) |
| Pay lines / transfer log | list inquiry | export batch, worker | pay type, hours, amount, batch | PayLine | Pay specification (JmgPayEvents), Transfer log | PT_BAL00 / CATS transfer |
| Job registration history | drill-down | order, operation, worker | start/stop, hours, qty, scrap reason, journal | TimeRegistration | Registrations (JmgStampJournalTrans) | CO14 confirmations (AFRU) |
| OEE by resource | KPI | resource, period, shift | OEE, A, P, Q, top losses | OeeBucket | n/a (Sensor Data Intelligence) | DM OEE dashboard |
| Downtime Pareto | operational report | resource, period, category | reason, minutes, count, % | DowntimeRecord | Indirect activity report | PM breakdown analysis (MCI7) |
| Scrap by reason | operational report | order, resource, period | reason, qty, cost | TimeRegistration scrap | Error cause report (JmgErrorSpecification) | CO11N variance reasons, MCP5 |
| Machine event / sequence gap report | operational report | machine, period | events, gaps, dropped (old/dup), lag | MachineEvent metrics | n/a | PCo logs |
| Control recipe monitor | list inquiry | status, destination, order | control recipe, status, sent/received, error | ControlRecipe | n/a | CO53 / CO53XT |
| PI sheet worklist | list inquiry | work cell, status | sheet, order, phase, status, open steps | PiSheetRun | n/a | CO60 / CMX worklist |
| Process message monitor | list inquiry | status, category, destination | message, category, order, per-destination status, attempts | ProcessMessage | n/a | CO54 / CO54XT |
| Edge fleet status | list inquiry | site, status | edge, version, ring, last heartbeat, buffer fill, cert expiry | EdgeNode | n/a | PCo management console |

### 4.25.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Badge | Worker setup | Name, photo (optional), badge id barcode (Code 128 / NFC encode) | PDF (CR80 card) | none | Personal data — GDPR minimization (photo optional) |
| Time card / registration summary | Day/week approval | Worker, days, clock-in/out, jobs, hours, pay types, approver | PDF, email | none | Wage-and-hour records: US FLSA 29 CFR 516 — 3 years payroll / 2 years time cards; EU Working Time Directive national rules (e.g., DE ArbZG §16 — 2 years) |
| Payroll export file | Export | Worker id, date, pay type, hours, amount | CSV, event (w43) | export batch number | Retained with payroll (w43) |
| PI sheet (executed) | PI sheet completion | Steps, values, limits, comments, signatures with manifestation | PDF/A-2b + JSON | `pi-sheet` per legal entity | Part of EBR (w24 retention) |
| Control recipe | Release | ISA-88 structure, setpoints, destination | JSON (B2MML-style), PDF | `control-recipe` | Part of EBR for GxP items |
| OEE shift report | Shift end | Resource, A/P/Q/OEE, downtimes, scrap | PDF, email | none | None |
| Machine events | Edge publish | CloudEvents (BR-25.4) | JSON | n/a | Postgres 90 days; Iceberg indefinitely |

### 4.25.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Auto clock-out | hourly | site | Closes open clock-ins per BR-25.3 once | < 30 s | Retry next hour | Count of auto-closed reported |
| OEE rollup | every 15 min | resource | Upsert OeeBucket by (resource, bucket_start) | < 60 s | Recompute next run | Lag > 5 min warns |
| Pay calculation | nightly 02:00 + on edit | worker-day | Recompute non-approved days; approved immutable | 1,000 workers < 2 min | Per-worker errors listed | Errors > 0 warn |
| Payroll export | per pay period (configurable) | legal entity | Export batch id; re-export reuses batch | < 1 min | Retry; batch stays open | Failure pages before payroll cutoff |
| MachineEvent partition drop | daily 03:30 | month partition | Drops partitions > 90 days after Iceberg copy confirmed | < 1 min | Skip if copy unconfirmed | Skip warns |
| Process message retry | every 1 min | destination | Per-delivery attempts per schedule | < 10 s | Error status after last attempt | Errors > 20 open warn |
| PI sheet archive sweep | hourly | site | Archives completed runs lacking archive | < 2 min | Retry 5× | Unarchived > 1 h pages |
| Edge certificate renewal check | daily 04:00 | edge node | Renew when < 30 days | < 1 min | Alert after 7 days failing | BR-25.14 |
| Edge rollout promotion | on schedule (ring windows) | site × ring | Promote ring after 24 h healthy | n/a | Halt rollout on failed heartbeat | Rollout halted warns |

### 4.25.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| Kiosk login | sessions:badge, kiosk shell | none | Operator |
| Register time | registrations, jobs, feedback, instructions, downtime coding, PI sheet execution | with *Approve time registrations* (own registrations) | Operator |
| Calculate time registrations | pay:calculate | none | Shop floor supervisor |
| Approve time registrations | pay:approve, edits with reason | with *Register time* for own records — approval of own day blocked | Shop floor supervisor |
| Export time to payroll | pay:export | with *Approve time registrations* flagged | Payroll clerk |
| View shop floor | OEE, andon, supervisor board, reports | none | Supervisor, Plant manager |
| Maintain shop floor setup | machines, thresholds, profiles, pay agreements, reason codes, recipe destinations, message categories | with *Approve time registrations* (pay agreements) flagged | Manufacturing engineer, ERP administrator |
| Maintain PI sheet templates | step library, templates (ECO-released) | with *Approve batch records* (w24) | Process engineer |
| Create process messages | manual process message, reprocess/delete with reason | none | Shop floor supervisor |
| Manage edge fleet | enrollment tokens, config publish, write allowlist, rollout rings | with *Maintain PI sheet templates* (allowlist and recipe content by different people) | OT administrator |

### 4.25.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| SF-0001 | error | Badge {badge} is not recognized. | Unknown badge | Contact supervisor | FR-25.1 |
| SF-0002 | warning | Previous job {job} was stopped automatically. | One open job rule | none | FR-25.3 |
| SF-0003 | warning | Missing clock-out on {date}; closed at shift end. | BR-25.3 | Supervisor confirms or edits | BR-25.3 |
| SF-0004 | warning | Job {job} was finished on the server while you were offline. | BR-25.7 | Supervisor review | BR-25.7 (`job-finished-conflict`) |
| SF-0005 | error | Mandatory check {check} failed; operation cannot be finished. | FR-25.5 | Supervisor override with reason | FR-25.5 |
| SF-0006 | error | Day cannot be approved: {n} flags open. | Approval precondition | Resolve flags | FR-25.8 |
| SF-0007 | error | Hours exceed 24.00 for {worker} on {date}. | PayLine invariant | Correct registrations | FR-25.7 |
| SF-0008 | warning | Machine {machine} sequence gap {from}–{to}. | Lost or delayed events | Replay from edge buffer | FR-25.9 |
| SF-0009 | info | Andon raised on {resource}: {kind}. | FR-25.11 | Respond | FR-25.11 |
| SF-0010 | error | Offline queue is full ({n} actions). | FR-25.12 limit | Reconnect device | FR-25.12 |
| SF-0011 | error | Control recipe {cr} could not be sent: {reason}. | Destination error | Fix destination; resend | FR-25.15 |
| SF-0012 | error | Resend requires force: process messages already received for order {order}. | BR-25.11 | Resend with force and reason | BR-25.11 |
| SF-0013 | error | PI sheet cannot be completed: {n} mandatory steps open. | BR-25.12 | Complete steps | BR-25.12 |
| SF-0014 | warning | Value {value} is outside {min}–{max}; comment required. | Out-of-limit input | Enter comment | FR-25.16 |
| SF-0015 | error | Process message rejected: unknown order or phase {ref}. | BR-25.13 | Correct source mapping | BR-25.13 |
| SF-0016 | warning | Process message {id} delivery to {destination} failed (attempt {n}). | Destination unavailable | Automatic retry; reprocess later | FR-25.17 |
| SF-0017 | error | Write to node {node} refused: not on allowlist or out of limits. | BR-25.15 | Update allowlist via OT admin | BR-25.15 |
| SF-0018 | warning | Edge {edge} certificate expires in {days} days; renewal failing. | BR-25.14 | Check connectivity to CA | BR-25.14 |
| SF-0019 | warning | Edge {edge} rolled back to {version}: heartbeat failed after upgrade. | FR-25.19 | Inspect connector logs | FR-25.19 |
| SF-0020 | error | Enrollment token is invalid or expired. | FR-25.19 | Issue a new token | FR-25.19 |

### 4.25.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation under 3× load |
|---|---|---|---|---|---|---|
| Kiosk action (start/stop/feedback) | 500 workers × 40 actions/day; peak 300/min at shift change | 60 / 200 / 400 ms server-side | 50/s | TimeRegistration 7M rows ≈ 4 GB | (worker_id, started_at), (job_id) | Optimistic UI; queue in PWA |
| Machine event ingest | 200 machines × 50 events/min = 10k/min; peak 30k/min | consumer lag p95 < 5 s | 500 events/s sustained | 5.3B events/yr → Postgres 90 days ≈ 300 GB rolling; Iceberg ≈ 1 TB/yr compressed | monthly range partitions; (resource_id, ts) | Lag allowed to 60 s; OEE marked stale |
| OEE bucket recompute | 200 resources × 96 buckets/day | < 5 s after last event | n/a | OeeBucket 7M rows/yr ≈ 2 GB | PK (resource_id, bucket_start) | Rollup every 30 min |
| Offline replay | ≤ 5,000 actions per device | 40 actions < 5 s; 5,000 < 2 min | 100 actions/s per device | n/a | client UUID unique | Batches of 500 throttled |
| Process message delivery | 50,000/day | 200 ms / 1 s / 3 s per destination | 50/s | ProcessMessage 18M rows ≈ 10 GB | (status, received_at); unique (source, source_message_id) | Retry backlog drained FIFO |
| PI sheet step save | 100,000/day | 80 / 250 / 500 ms | 50/s | PiStepResult 36M rows ≈ 15 GB; archives ≈ 200 GB object storage | (run_id, step_seq) | Offline queue absorbs |
| Edge heartbeat / config pull | 50 edges × 2/min | 50 / 150 / 300 ms | 10/s | EdgeNode status history 50M rows ≈ 5 GB (90-day retention) | (edge_id, ts) | Heartbeat interval auto-extends to 120 s |

### 4.25.20 Localization and compliance hooks

- **Working-time law**: pay rules (overtime thresholds, Sunday/holiday premiums, night work) differ by country and collective agreement; launch defaults are US FLSA (40 h/week) — wave 48 country packs ship pay agreement templates (e.g., DE ArbZG 10 h/day maximum, FR 35 h/week, MX LFT) through the `shopfloor.pay-rule` extension point; holiday calendars come from w48.
- **Time-recording obligations**: EU CJEU C-55/18 (CCOO) requires objective time recording; clock registrations are kept as raw records (immutable, corrections as reversals) to satisfy it; retention per country via w48 retention policy (US 2–3 years, DE 2 years, ES 4 years).
- **Privacy and works councils**: badge, PIN hash and registration data are personal data; machine-to-worker attribution (FR-25.13) and OEE per operator are disabled per site with `shopfloor.operator-analytics = false` (default false in DE/AT/NL packs, where works-council agreements typically require it); GDPR erasure pseudonymizes workers after retention.
- **GxP**: PI sheets and process messages become EBR content under 21 CFR Part 11 / EU Annex 11 (w24 FR-24.21/24.22).
- **OT security**: edge connector deployment follows IEC 62443-3-3 zone/conduit guidance (outbound-only, mTLS, allowlisted writes); NIS2 (EU) incident reporting hooks use the w2 security event stream.
- **Language**: kiosk UI and PI sheet content are translatable per site; instruction attachments per language with fallback.
- **Extension points**: `shopfloor.pay-rule`, `shopfloor.process-message.destination`, `iot-edge.protocol-adapter` (adds protocols such as Modbus TCP or MTConnect without core changes).

### 4.25.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| SAP 13.22 PP-PI — CO53 / CO53XT control recipe monitor [SAP #254] | PARTIAL-25 | ADDED as FR-25.15 | Destinations kiosk / pcs-opcua / pcs-mqtt; monitor in 4.25.14 |
| SAP 13.22 PP-PI — CO54 / CO54XT / CO57 / CO58 process messages [#255] | PARTIAL-25 | ADDED as FR-25.17 | Categories, destinations, retry |
| SAP 13.22 PP-PI — CO60 / CO60XT / CO60_PI / CO64 / CO67 PI sheet [#256] | PARTIAL-25 | ADDED as FR-25.16, FR-25.18 | Designer, execution, archive |
| SAP 13.22 PP-PI — CMX / Standard XSteps (CMXSV) / SXS repository [#258] | PARTIAL-25 | ADDED as FR-25.16 | Versioned StepLibrary via ECO |
| SAP 13.22 PP-PI Reports — CO54 process message log [#263] | PARTIAL-25 | ADDED as FR-25.17 | Process message monitor |
| SAP 13.22 PP-PI Reports — CMX/CO60 PI sheet worklist [#265] | PARTIAL-25 | ADDED as FR-25.16 | PI sheet worklist per work cell |
| SAP 13.22 PP-PI Periodic — CO53 send control recipes (batch) [#266] | PARTIAL-25 | ADDED as FR-25.15 | Sent at release; resend per BR-25.11 |
| SAP 13.22 PP-PI Periodic — CO54 process message processing (batch) [#267] | PARTIAL-25 | ADDED as FR-25.17 | Retry job in 4.25.16 |
| SAP 13.22 PP-PI Periodic — CO64 / CO67 PI sheet archive [#268] | PARTIAL-25 | ADDED as FR-25.18 | Archive sweep job |
| SAP 13.22 PP-PI Customizing — Process management: PI categories (O10C/O12C/O13C), message categories and destinations (O05C/O06C/O07C), PI sheet layouts, XStep settings, signature strategies [#271] | PARTIAL-25 | ADDED as FR-25.15, FR-25.16, FR-25.17 | Signature strategies from w24 FR-24.22 |
| SAP 13.28 HCM — PP61/PP62/PP63/PP64/PP6I/PP6A shift planning [#325] | PARTIAL-25 | DEFERRED to wave 42 | Rostering against staffing requirements belongs with HR core; wave 25 keeps profiles and shift windows (ProfileDay) |
| Coordinator-flagged — OPC-UA/MQTT edge connector as a deployable (AX 12.x Production → IoT, COVERED-25) | COVERED-25 (deployment not specified) | ADDED as FR-25.19 | Enrollment, mTLS, config versions, ringed rollout, write allowlist |

Related row owned by another wave: SAP #193 EWM Material Flow System (propose 32) reuses the FR-25.19 edge connector and the `iot-edge.protocol-adapter` extension point.
