# Part 3 — Phase 3: Inventory & Procure-to-Pay (waves 16–20)

## 3.0 Phase overview

**Business outcome at phase exit.** At the end of wave 20 the demo plant buys, receives, pays for and values physical goods on Passage with no AX or SAP system in the loop. Every quantity movement is an immutable inventory transaction carrying physical and financial status plus storage and tracking dimensions (the InventTrans / MSEG role). Buyers run requisition → RFQ → agreement → purchase order under policy-driven approval (PurchReqTable / EBAN through PurchTable / EKKO). Receiving posts inventory and a GR/IR accrual to the universal journal; vendor invoices match 2-way or 3-way inside tolerances or route to an exception queue (VendInvoiceInfoTable / RBKP-RSEG). Open vendor items settle against payment runs that emit ISO 20022 pain.001 and NACHA files (VendSettlement / F110-REGUH). Inventory is valued under FIFO, LIFO, weighted average or standard cost with a close/recalculation job whose value report ties to the GL inventory accounts (InventClosing / CKMLCP). Suppliers acknowledge POs, send ASNs and submit invoices through a portal.

**Phase-exit sign-off criteria**
- Inventory on-hand projection equals SUM(inventory transactions) for a 1M-transaction fixture after a full rebuild; zero rows differ.
- Each of the 11 journal/posting types in this phase (movement, adjustment, transfer, BOM, counting, product receipt, vendor invoice, payment, settlement, close adjustment, revaluation) produces a balanced universal-journal entry; the daily subledger-to-GL reconciliation for inventory, GR/IR and AP control accounts reports a difference of 0.00 in accounting currency for the demo entity.
- 30 requisitions in the approval matrix route to the expected chain; 200 matching scenarios yield the expected match status and postings; 300 settlement scenarios post the expected discount and FX gain/loss; 100 costing sequences per method tie to the reference workbook to the cent.
- pain.001.001.09 output validates against the ISO 20022 XSD and is accepted by the bank test harness; a payment run killed mid-flight and resumed pays each open item exactly once.
- Inventory close on 1M transactions completes in under 20 minutes on the stage cluster (4 vCPU / 16 GiB inventory pod, Postgres 8 vCPU).
- Recorded end-to-end walkthrough (buy → receive → match → pay → close) signed off by both owners and the reviewing accountant.

**Cross-wave data model summary**

| Entity | Owning service | Key relationships |
|---|---|---|
| InventoryTransaction (InventTrans / MSEG) | inventory (Go, `inventory` Postgres) | → InventoryTransactionOrigin, → Dimension set, → Product (wave 8), → Site/Warehouse (wave 6) |
| InventoryTransactionOrigin (InventTransOrigin / MKPF) | inventory | 1:N InventoryTransaction; references source document id+type (journal line, PO line, receipt line, production order line) |
| OnHand (InventSum / MARD+MCHB) | inventory (projection) | Keyed by product × dimension hash; rebuilt from InventoryTransaction |
| InventoryJournal / JournalLine (InventJournalTable/Trans) | inventory | Lines → InventoryTransactionOrigin; posts via ledger `Post(JournalEntry)` (wave 11) |
| Reservation | inventory | → InventoryTransaction (status change), → batch/serial |
| CountPlan / CountSheet | inventory | → JournalLine (variance) |
| PurchaseRequisition / Line (PurchReqTable / EBAN) | procurement (Go, `procurement` Postgres) | → PurchaseOrderLine on consolidation; → Temporal approval workflow |
| RFQ / RFQReply (PurchRFQTable / EKKO type AN) | procurement | → PurchaseOrder on award |
| PurchaseAgreement (PurchAgreementHeader / EKKO type K/L) | procurement | → PurchaseOrderLine releases |
| TradeAgreement (PriceDiscTable / A017-KONP) | procurement | Vendor pricing lookup |
| PurchaseOrder / Line / Version (PurchTable/PurchLine, EKKO/EKPO) | procurement | → InventoryTransaction (OnOrder), → Receipt, → VendorInvoice |
| ProductReceipt / Line (VendPackingSlipJour/Trans, MKPF/MSEG 101) | receiving (Go, `procurement` Postgres) | → PurchaseOrderLine, → InventoryTransaction (Received), → GR/IR journal entry |
| VendorInvoice / Line (VendInvoiceInfoTable/Line → VendInvoiceJour, RBKP/RSEG) | payables (Go, `payables` Postgres) | → ReceiptLine (3-way), → PurchaseOrderLine (2-way), → VendorTransaction, → GR/IR clearing |
| MatchResult | payables | → VendorInvoiceLine; stores price/qty/total variance and status |
| VendorTransaction (VendTrans / BSIK→BSAK) | payables | Open item; → Settlement |
| Settlement (VendSettlement / BSE_CLR) | payables | Links payment/credit/invoice transactions with discount and FX |
| PaymentProposal / Payment / PaymentFile (LedgerJournalTrans payment journal, REGUH/REGUP) | payables | → VendorTransaction, → bank account (wave 36 placeholder: bank master lite) |
| CostTransaction (InventCostTrans / CKMLPP-CKMLCR) | costing (Go, `inventory` Postgres schema `costing`) | → InventoryTransaction; produced by close/recalc |
| CostingVersion / ItemCost (CostingVersion/InventItemPrice, MBEW) | costing | Standard and planned costs per product × site |
| SupplierPortalUser / Acknowledgement / ASN | supplier-portal (TypeScript BFF + `supplier` Postgres) | → PurchaseOrder, → ProductReceipt (expected) |

**Cross-wave event catalog**

| Event (CloudEvents type) | Producer | Consumers | Payload summary |
|---|---|---|---|
| `dev.passage.inventory.transaction.posted.v1` | inventory | costing, planning (w22), analytics | txn id, origin, product, dims, qty, issue/receipt status, physical/financial dates |
| `dev.passage.inventory.onhand.changed.v1` | inventory | procurement (ATP for PO), sales (w27), warehouse (w31) | product, dim hash, avail_physical, avail_ordered, delta |
| `dev.passage.inventory.journal.posted.v1` | inventory | ledger (reconciliation), costing | journal id, type, line count, ledger entry id |
| `dev.passage.procurement.requisition.approved.v1` | procurement | procurement consolidation, budget control (w38) | req id, lines, approver chain |
| `dev.passage.procurement.po.confirmed.v1` | procurement | inventory (OnOrder txns), supplier-portal, EDI (w35) | po id, version, lines, vendor |
| `dev.passage.procurement.po.version.created.v1` | procurement | supplier-portal, receiving | po id, version, diff summary |
| `dev.passage.receiving.receipt.posted.v1` | receiving | inventory, payables (match candidates), costing | receipt id, lines, po lines, qty, GR/IR amount |
| `dev.passage.payables.invoice.posted.v1` | payables | ledger, costing (PPV/invoice-cost settle), tax (w13) | invoice id, vendor, lines, match status, amounts |
| `dev.passage.payables.invoice.match.exception.v1` | payables | workflow (Temporal), notifications | invoice id, line, variance type, amounts |
| `dev.passage.payables.settlement.posted.v1` | payables | ledger, analytics | settlement id, items, discount, fx gain/loss |
| `dev.passage.payables.payment.sent.v1` | payables | bank connectivity (w36), supplier-portal, cash (w36) | payment id, file id, amount, method, vendor |
| `dev.passage.payables.payment.status.changed.v1` | payables | notifications, analytics | payment id, old/new status, pain.002 reason code |
| `dev.passage.costing.close.completed.v1` | costing | ledger, analytics | close id, period, adjustments, duration |
| `dev.passage.costing.cost.changed.v1` | costing | inventory, planning, product costing (w39) | product, site, version, old/new cost, effective date |
| `dev.passage.supplier.asn.submitted.v1` | supplier-portal | receiving, warehouse (w31) | asn id, po lines, expected qty, carrier |

**Phase-level risks**

| Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|
| On-hand projection drifts from transactions under concurrency | Medium | High | Single-writer per product × dimension partition (advisory lock), nightly rebuild-and-compare job, alert on any diff | Engineering |
| Matching tolerance semantics differ between AX (per item/vendor %) and SAP (tolerance keys) and users expect their old behavior | High | Medium | Tolerance model supports both percentage and absolute limits at vendor, item, vendor×item and entity levels; migration maps both | Product |
| FX gain/loss and discount postings fail accountant review | Medium | High | 300-scenario fixture set reviewed by accountant before wave 19 close; property test that settlement always balances | Product + accountant |
| Inventory close performance on large transaction history | Medium | Medium | Settlement by product partition in Temporal child workflows; incremental close from last closed date; 1M-txn perf gate | Engineering |
| Bank file rejection in production (schema or bank-specific rules) | Medium | High | XSD validation in CI, bank test harness, per-bank profile overrides (SEPA, CH, UK) | Engineering |
| Supplier portal becomes an attack surface | Medium | High | Separate Keycloak realm, OPA policies scoped to vendor id, rate limits 60 req/min, no inbound write to GL | Security |

## 3.16 Wave 16: Inventory Ledger, Dimensions & On-Hand

### 3.16.1 Scope and business context

Wave 16 creates the inventory ledger that every later quantity-bearing module (purchasing, receiving, production, sales, warehouse) writes to. In AX this is InventTrans with InventTransOrigin, InventSum and InventDim; in SAP it is the material document (MKPF/MSEG) with stock segments in MARD (storage location), MCHB (batch) and MSKA/MSPR (special stock). Before this wave Passage has products with dimension groups (wave 8) and sites/warehouses (wave 6) but no quantities.

At the end of the wave a user can: load opening balances through a movement journal; move, adjust, transfer and count stock; reserve stock manually or by FEFO/FIFO policy; block or quarantine a batch; run a cycle-count plan; and see on-hand by any combination of site, warehouse, location, batch, serial, inventory status. Every posting produces a balanced universal-journal entry through the wave 11 `Post(JournalEntry)` API.

Out of scope: purchase/sales-driven transactions (waves 17–18, 27), production consumption (wave 21), warehouse work and license plates (wave 31), quality orders (wave 33), costing and value adjustments beyond the running cost price captured on receipt (wave 20). Inventory in this wave values at the item's current cost price on receipt and issues at the running average; wave 20 replaces that with the full costing engine.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX On-hand form (InventOnhand) requires choosing a dimension display (InventDimParm) and re-querying per combination; SAP MMBE shows plant/storage location/batch only, MB52 is a batch report; users export to Excel to get location × batch × status | InventSum is pre-aggregated per InventDimId and queried through fixed display sets; MARD/MCHB are separate tables with no common query | Single `GET /onhand` with `groupBy` any dimension subset, served from one projection; shell grid pivots client-side | Clicks to answer "how much of X in batch B at location L": 9 → 2; query p95 < 50 ms vs 3–10 s |
| InventSum drift from InventTrans after failed batch or kernel patch; fix is the "Check inventory on-hand" consistency check run at night with users off the system | Dual-write of InventTrans and InventSum under X++ ttsbegin with retry-on-deadlock; no automated compare | Projection updated in the same Postgres transaction; weekly rebuild-and-compare; diff event alerts | Reconciliation effort: 2 h/month manual → 0 (automated, 0 diffs); detection latency: weeks → < 24 h |
| SAP MIGO posts synchronously but stock overview in BW/analytics lags the nightly extract; AX entity store refresh is hourly at best | Analytics copies instead of event stream | `onhand.changed` CloudEvent via outbox within 5 s; any consumer (BI, MES, WMS) subscribes without middleware | Data latency to external systems: 1–24 h → < 5 s |
| Negative inventory silently allowed per InventModelGroup; errors surface at inventory close as unsettled issues (InventClosing "Cannot settle") months later | Check done at posting with a flag, no alerting; financial negative evaluated only at close | Negative check atomic under lock; negative rows surfaced on workspace tile and in daily reconciliation | Time to detect unauthorized negative stock: 30–90 days → same day; close-time settlement errors from negatives: 0 |
| Cycle counting in AX needs the Counting journal, InventCountGroup setup and the "Create lines" dialog with 14 fields; SAP MI01/MI04/MI07 is three transactions and a printed count list | Journal-centric design with no plan object; count execution not mobile | `CountPlan` generates sheets on a cron; PWA count screen with blind count and offline queue; one `:post` | Steps from plan to posted variance: 3 transactions / ~25 clicks → 1 plan + 1 tap per line; counts per hour per operator ×3 |

### 3.16.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity level |
|---|---|---|---|---|
| Inventory transaction record | InventTrans, InventTransOrigin, InventTransPosting; form InventTrans | MSEG/MKPF (material document), MATDOC in S/4; MB03/MIGO display | `inventory.InventoryTransaction`, `InventoryTransactionOrigin`; screen Inventory transactions | full |
| On-hand by dimension | InventSum, InventOnhand form, InventDimParm | MARD, MCHB, MSKA; MMBE stock overview, MB52 | `inventory.OnHand` projection; On-hand inquiry | improved (any dimension combination, real time) |
| Dimensions | InventDim (InventSiteId, InventLocationId, wMSLocationId, InventBatchId, InventSerialId, InventStatusId, LicensePlateId, ConfigId/Size/Color/Style) | Plant (WERKS), storage location (LGORT), batch (CHARG), serial (SERNR via SERI), special stock indicator, valuation type | `DimensionSet` (site, warehouse, location, batch, serial, status, config/size/color/style, license_plate reserved for w31) | full |
| Movement / adjustment journal | InventJournalTable/Trans types Movement, LossProfit; InventJournalCheckPost | MIGO movement types 501/502 (receipt w/o PO), 551/552 (scrapping), 561/562 (initial stock) | `InventoryJournal` type MOVEMENT, ADJUSTMENT | full |
| Transfer journal | InventJournalTrans type Transfer | 301/302 (plant to plant), 311/312 (storage loc), MB1B | `InventoryJournal` type TRANSFER (paired issue/receipt txns) | full |
| BOM journal | InventJournalTrans type BOM (InventBOMJournal) | 261/262 consumption to order, 201 cost center | `InventoryJournal` type BOM (explode wave 9 BOM, issue components, receive parent) | partial (no route/overhead until w21) |
| Counting | InventJournalTrans type Count, InventCountJournal, counting groups (InventCountGroup) | Physical inventory MI01/MI04/MI07, IKPF/ISEG, cycle counting indicator ABC (MIBC) | `CountPlan`, `CountSheet`, journal type COUNT | full |
| Item model group | InventModelGroup (InventModel FIFO/LIFO/WeightedAvg/Std, PhysNegative, FinancialNegative, PostPhysicalInvent) | MBEW price control (S/V), negative stock allowed per plant/storage location (OMJ1) | `ItemModelGroup` (wave 8 entity, enforced here) | full |
| Reservation | InventTrans status ReservPhysical/ReservOrdered, InventMovement, reservation form, FEFO date-controlled (InventBatch expiry, PdsFEFO) | Stock reservations (MB21, RESB), batch determination (FEFO via strategy), batch status | `Reservation` with policy FEFO/FIFO/MANUAL | full |
| Blocking / quarantine | Inventory blocking (InventBlocking), quarantine order (InventQuarantineOrder), inventory status (D365) | Blocked stock (movement 344/343), quality inspection stock, batch restricted status | Inventory status `AVAILABLE`, `BLOCKED`, `QUARANTINE`; `Blocking` record | full |
| Unit conversion on movement | UnitOfMeasureConversion (intra/inter-class, product-specific) | MARM alternative UoM, conversion on MIGO | Wave 6/8 UoM service; quantity stored in inventory unit plus entered unit and qty | full |
| Inventory closing/recalc | InventClosing | CKMLCP | Deferred to wave 20 | dropped-with-reason (wave 20) |

### 3.16.3 Functional requirements

- **FR-16.1 Immutable inventory transactions** — Must. The system shall record every quantity movement as an append-only `InventoryTransaction` row linked to an `InventoryTransactionOrigin`; updates only change status fields and dates, never quantity or identity.
  - Status changes are written as a new `InventoryTransactionStatusChange` audit row; the current status lives on the transaction for query speed.
  - A transaction has exactly one of `issue_status` or `receipt_status` set (sign determined by quantity).
  - Corrections are new transactions with opposite sign referencing `corrects_transaction_id`.
  - Partition key: tenant_id, product_id; up to 50M transactions per tenant supported before range partitioning by `physical_date` year is required.

- **FR-16.2 Transaction status model** — Must. The system shall implement the AX issue/receipt status model: issue statuses `ON_ORDER → RESERVED_ORDERED → RESERVED_PHYSICAL → PICKED → DEDUCTED → SOLD`; receipt statuses `ORDERED → ARRIVED → REGISTERED → RECEIVED → PURCHASED`.
  - Physical statuses (affect physical on-hand): PICKED, DEDUCTED, REGISTERED, RECEIVED. Financial statuses: SOLD, PURCHASED.
  - Transitions may skip forward (ORDERED → RECEIVED directly) but never go backward except via the explicit `Unpick`, `Unregister`, `CancelReservation` commands.
  - SAP mapping on migration: 101 → RECEIVED, 102 → reversal, 261 → DEDUCTED, 301 → DEDUCTED+RECEIVED pair, 561 → RECEIVED+PURCHASED opening.

- **FR-16.3 On-hand projection** — Must. The system shall maintain `OnHand` per product × dimension set with the columns `posted_qty, posted_value, received, registered, arrived, ordered, deducted, picked, reserved_physical, reserved_ordered, on_order` and computed `avail_physical`, `avail_ordered`, `physical_invent`.
  - `physical_invent = posted_qty + received + registered − deducted − picked`.
  - `avail_physical = physical_invent − reserved_physical`.
  - `avail_ordered = avail_physical + ordered + arrived − on_order − reserved_ordered`.
  - Projection is updated in the same Postgres transaction as the inventory transaction (not via event) to guarantee the negative-stock check; the event is emitted from the outbox afterward.
  - Query at any dimension subset: aggregation over the finest-grain rows, p95 < 50 ms for ≤ 10k rows.

- **FR-16.4 Negative inventory policy** — Must. The system shall reject an issue that would make `avail_physical` (physical negative forbidden) or `avail_ordered` (financial negative forbidden) negative when the item model group forbids it.
  - Enforcement uses `SELECT … FOR UPDATE` on the OnHand row; the check and the write are atomic.
  - When allowed, negative on-hand is flagged in the On-hand inquiry and in the daily reconciliation.
  - Error code `INV-NEG-001` with product, dimension set and shortfall quantity.

- **FR-16.5 Inventory dimensions** — Must. The system shall store the dimension set as a normalized `DimensionSet` row keyed by a SHA-256 hash of its canonical values (AX InventDimId pattern), with active dimensions determined by the product's storage and tracking dimension groups.
  - Dimensions: site, warehouse, location, batch, serial, inventory_status, config, size, color, style, license_plate (reserved), owner (reserved for w41 consignment).
  - Site is mandatory on every transaction; warehouse mandatory if the storage dimension group marks it mandatory.
  - A serial-tracked product requires quantity 1 per transaction and a unique serial per product in physical stock (duplicate serial → `INV-SER-001`).

- **FR-16.6 Inventory journals** — Must. The system shall support journal types MOVEMENT, ADJUSTMENT, TRANSFER, BOM, COUNT, OPENING_BALANCE with header/lines, validate, approval workflow (Temporal template from wave 4), and post.
  - Posting creates transactions, updates on-hand and calls `Post(JournalEntry)` with the journal id as idempotency key; a ledger failure rolls back the inventory posting (saga compensating step with explicit reversal if the ledger acknowledged).
  - Journal offset accounts: MOVEMENT requires a user-entered offset account; ADJUSTMENT uses the posting profile's inventory adjustment account; TRANSFER posts no GL unless cost differs by site (wave 20).
  - Maximum 10,000 lines per journal; posting throughput target 2,000 lines/s.

- **FR-16.7 Reservations** — Must. The system shall reserve quantity against on-hand (RESERVED_PHYSICAL) or expected receipts (RESERVED_ORDERED) by manual choice or by policy FIFO, FEFO, LIFO, with batch/serial selection.
  - FEFO orders candidate batches by `expiry_date ASC, receipt_date ASC`; batches with status BLOCKED or QUARANTINE or `expiry_date < required_date + shelf_life_margin_days` are excluded.
  - Reservation holds for `reservation_ttl_hours` (default 0 = until released or consumed).
  - Over-reservation is rejected; partial reservation returns the shortfall.

- **FR-16.8 Blocking and quarantine** — Must. The system shall allow blocking a quantity, batch or serial with reason code; blocked quantity is excluded from `avail_physical` and from policy-based reservation.
  - Blocking is implemented as inventory status change (status dimension) producing a status-transfer transaction pair.
  - Quarantine requires a disposition (release, scrap, return) to exit.

- **FR-16.9 Cycle counting** — Must. The system shall generate count sheets from a `CountPlan` (ABC class frequency, location range, last-count age, random sample) and post counted variances as COUNT journal lines.
  - Counting freezes on-hand for the counted dimension set (`count_lock`) while a sheet is open unless the plan allows movement with recount.
  - Variance tolerance per plan (qty % and value); above tolerance requires supervisor approval before posting.
  - Blind count option hides expected quantity.

- **FR-16.10 Unit conversions on movements** — Must. The system shall accept quantity in any unit with a conversion to the product's inventory unit, storing `qty` (inventory unit), `entered_qty`, `entered_unit`.
  - Rounding to the product's inventory unit decimals (default 2, max 6); residual beyond precision is rejected (`INV-UOM-002`).
  - Catch-weight is deferred to wave 24.

- **FR-16.11 Physical and financial dates** — Must. The system shall store `physical_date` (goods movement) and `financial_date` (value recognition) separately; the ledger entry uses `financial_date` and is validated against period control (wave 14) for module INVENTORY.

- **FR-16.12 Daily reconciliation** — Must. The system shall run a job that compares SUM(OnHand.posted_value) by inventory account and dimension to the ledger balance projection and raises `dev.passage.inventory.reconciliation.diff.v1` on any non-zero difference.

- **FR-16.13 Inventory transaction inquiry** — Should. The system shall provide a filterable, exportable inquiry over transactions with origin, status, dates and GL voucher drill-down.

- **FR-16.14 Opening balance load** — Should. The system shall load opening balances from AX InventSum/InventTrans or SAP MARD/MCHB/MBEW extracts through the wave 10 migration framework into an OPENING_BALANCE journal.

### 3.16.4 Data model

**InventoryTransaction**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | RLS |
| origin_id | uuid | yes | FK InventoryTransactionOrigin |
| product_id | uuid | yes | FK product (wave 8) |
| dimension_set_id | bytea(32) | yes | FK DimensionSet hash |
| qty | numeric(28,6) | yes | signed; inventory unit; ≠ 0 |
| entered_qty / entered_unit | numeric(28,6) / text | yes | as keyed |
| issue_status | enum | cond. | ON_ORDER, RESERVED_ORDERED, RESERVED_PHYSICAL, PICKED, DEDUCTED, SOLD; null for receipts |
| receipt_status | enum | cond. | ORDERED, ARRIVED, REGISTERED, RECEIVED, PURCHASED; null for issues |
| physical_date | date | yes | goods movement date |
| financial_date | date | no | set when financial status reached |
| cost_amount_physical | numeric(19,4) | no | accounting currency, at physical posting |
| cost_amount_posted | numeric(19,4) | no | accounting currency, financial |
| cost_amount_adjustment | numeric(19,4) | no | written by wave 20 close |
| ledger_entry_id | uuid | no | voucher in universal journal |
| corrects_transaction_id | uuid | no | reversal/correction link |
| marked_transaction_id | uuid | no | explicit issue→receipt marking (wave 20) |
| created_at | timestamptz | yes | append-only |

**InventoryTransactionOrigin**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| source_type | enum | yes | INVENT_JOURNAL_LINE, PURCH_LINE, RECEIPT_LINE, SALES_LINE, PROD_LINE, COUNT_LINE, TRANSFER_LINE, OPENING |
| source_id | uuid | yes | polymorphic FK; unique (source_type, source_id) |
| product_id | uuid | yes | denormalized |
| reference_number | text | no | PO number, journal number |
| direction | enum | yes | ISSUE / RECEIPT |
| created_at | timestamptz | yes | |

**OnHand**

| Field | Type | Required | Notes |
|---|---|---|---|
| tenant_id, product_id, dimension_set_id | composite PK | yes | |
| posted_qty, posted_value | numeric | yes | financially posted |
| received, registered, arrived, ordered | numeric | yes | receipt-side buckets |
| deducted, picked, reserved_physical, reserved_ordered, on_order | numeric | yes | issue-side buckets |
| last_txn_seq | bigint | yes | monotonic per row for rebuild diff |
| updated_at | timestamptz | yes | |

**InventoryJournal / InventoryJournalLine**

| Field | Type | Required | Notes |
|---|---|---|---|
| id, journal_number | uuid, text | yes | number sequence `INVJ-` gap-free optional |
| journal_type | enum | yes | MOVEMENT, ADJUSTMENT, TRANSFER, BOM, COUNT, OPENING_BALANCE |
| status | enum | yes | DRAFT, SUBMITTED, APPROVED, POSTING, POSTED, REJECTED, CANCELLED |
| posting_date | date | yes | financial_date default |
| offset_account_id | uuid | cond. | MOVEMENT mandatory |
| line: line_no, product_id, dimension_set_id, qty, entered_unit, cost_price, counted_qty, expected_qty, to_dimension_set_id (TRANSFER), bom_id (BOM) | | | |
| ledger_entry_id | uuid | no | after posting |

**Reservation**: id, product_id, dimension_set_id, qty, demand_type, demand_id, policy (MANUAL/FIFO/FEFO/LIFO), status (ACTIVE, RELEASED, CONSUMED), expires_at, created_by.

**CountPlan / CountSheet**: plan id, name, selection (abc_class[], warehouse_ids[], location_range, max_days_since_count, sample_pct), frequency (cron), tolerance_qty_pct, tolerance_value, blind (bool); sheet id, plan_id, status (OPEN, COUNTED, APPROVED, POSTED), lines (product, dims, expected_qty, counted_qty, variance_qty, counted_by, counted_at).

Invariants: Σ qty over transactions with physical status for a (product, dimension set) = OnHand.physical_invent; posted_value changes only through financial status transitions and wave 20 adjustments; a serial has at most one open physical receipt not matched by an issue; DimensionSet hash is deterministic across services (shared Go package `passage/invdim`). Indexing: InventoryTransaction (tenant, product, dimension_set, physical_date), (origin_id), partial index on open statuses; OnHand clustered by (tenant, product); monthly range partitions on InventoryTransaction by physical_date when a tenant exceeds 20M rows.

### 3.16.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/inventory/journals | Create journal header | client key | InventoryJournalMaintain |
| POST | /api/v1/inventory/journals/{id}/lines:bulk | Add up to 10,000 lines | per-line client key | InventoryJournalMaintain |
| POST | /api/v1/inventory/journals/{id}:validate | Dry-run validation | n/a | InventoryJournalMaintain |
| POST | /api/v1/inventory/journals/{id}:post | Post (starts Temporal `PostInventoryJournal`) | journal id | InventoryJournalPost |
| GET | /api/v1/inventory/onhand?product=&site=&warehouse=&batch=&groupBy= | On-hand inquiry | n/a | InventoryInquire |
| GET | /api/v1/inventory/transactions?product=&status=&from=&to= | Transaction inquiry | n/a | InventoryInquire |
| POST | /api/v1/inventory/reservations | Reserve (manual or policy) | client key | InventoryReserve |
| DELETE | /api/v1/inventory/reservations/{id} | Release | idempotent | InventoryReserve |
| POST | /api/v1/inventory/blockings | Block qty/batch/serial | client key | InventoryBlock |
| POST | /api/v1/inventory/count-plans/{id}:generate | Create count sheets | plan+date | CycleCountMaintain |
| POST | /api/v1/inventory/count-sheets/{id}:post | Post variance journal | sheet id | CycleCountPost |
| POST | /internal/v1/inventory/transactions:apply | Service-to-service status transition (used by waves 17–20) | origin+step | service account |
| POST | /internal/v1/inventory/onhand:rebuild | Rebuild projection from transactions | run id | InventoryAdmin |

Commands: `CreateJournal`, `AddLines`, `PostJournal` (validates period control, negative policy, dimension mandatory flags, UoM conversion existence, offset account structure), `Reserve` (validates qty > 0, policy-eligible batches, status), `Block`, `ApplyTransactionStep` (validates monotonic status order, quantity ≤ remaining at previous status).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.inventory.transaction.posted.v1` | any transaction insert or status change | transaction_id, origin, product_id, dimension_set, qty, issue_status, receipt_status, physical_date, financial_date, cost_amount_* | costing, analytics, planning |
| `dev.passage.inventory.onhand.changed.v1` | OnHand row update | product_id, dimension_set, avail_physical, avail_ordered, delta_bucket, delta_qty | procurement, sales, warehouse |
| `dev.passage.inventory.journal.posted.v1` | journal reaches POSTED | journal_id, type, lines, ledger_entry_id, posting_date | ledger reconciliation, costing |
| `dev.passage.inventory.reservation.created.v1` / `.released.v1` | reservation lifecycle | reservation_id, demand, product, dims, qty, policy | sales, production |
| `dev.passage.inventory.reconciliation.diff.v1` | daily job finds diff ≠ 0 | account, dimension, inventory_value, ledger_balance, diff | alerting |

### 3.16.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Inventory management workspace | Tiles: journals awaiting approval, negative on-hand, open count sheets, reconciliation diffs | counts, last run | InventoryInquire |
| On-hand inquiry | Filter, group by any dimension, drill to transactions, export | product, site, warehouse, location, batch, serial, status, physical_invent, avail_physical, avail_ordered, posted_value | InventoryInquire |
| Inventory journal (header + grid) | New, add lines, validate, submit, post, copy, import CSV | journal_number, type, status, line grid: product, dims, qty, unit, cost_price, offset account | InventoryJournalMaintain |
| Transaction inquiry | Filter by status/date/origin, drill to voucher | reference, status, qty, dates, cost amounts | InventoryInquire |
| Reservation dialog | Reserve by policy or pick batches/serials | candidate batches with expiry, available qty | InventoryReserve |
| Count plan / count sheet | Generate, assign, count (mobile-friendly), approve, post | expected (hidden when blind), counted, variance | CycleCountMaintain |
| Blocking | Block / release with reason | product, dims, qty, reason, expected release date | InventoryBlock |

Interaction rules: grid supports keyboard row entry (Tab across cells, Ctrl+Enter to add line), paste from spreadsheet with dimension column mapping, inline validation on blur for product/dimension and deferred validation (`:validate`) for on-hand and period checks; column sets and filters personalized per user (wave 5 shell). Count sheet works on the PWA shell with offline queue of counted quantities (synced on reconnect, conflict = latest count wins with audit).

### 3.16.7 Business rules and edge cases

- **BR-16.1** Status transitions are monotonic per transaction: issue `ON_ORDER(0) < RESERVED_ORDERED(1) < RESERVED_PHYSICAL(2) < PICKED(3) < DEDUCTED(4) < SOLD(5)`; receipt `ORDERED(0) < ARRIVED(1) < REGISTERED(2) < RECEIVED(3) < PURCHASED(4)`. Backward moves only through named commands that create a status-change audit row and an on-hand delta.
- **BR-16.2** Splitting: applying a step for a quantity less than the transaction quantity splits the transaction into two rows with the same origin (AX InventTrans split); sum of split quantities equals the original.
- **BR-16.3** Transfers create an issue (DEDUCTED) at the from-dimension and a receipt (RECEIVED) at the to-dimension in one database transaction; cost of the receipt equals the cost of the issue at the time of posting.
- **BR-16.4** Negative-policy check reads `avail_physical` under row lock; two concurrent issues against the last unit serialize, the second fails with `INV-NEG-001`. Deadlock avoidance: locks acquired in ascending (product_id, dimension_set_id) order within a journal.
- **BR-16.5** Rounding: quantities rounded half-up to the inventory unit's decimals; values to the accounting currency's minor unit (wave 12 rounding rules). Transfer of 1/3 of a 1.000-unit batch under 3 decimals yields 0.333 and leaves 0.667.
- **BR-16.6** Physical date may be in a closed inventory period only if financial date is in an open period and the item model group allows backdated physical posting; otherwise `INV-PER-001`.
- **BR-16.7** FEFO tie-break: equal expiry → earliest receipt_date → lowest batch_id (lexical). Batches with no expiry sort last.
- **BR-16.8** Count sheet posting posts only lines where counted_qty differs from expected_qty at the time of posting (not generation); movements after generation trigger a recount flag when the plan has `freeze=false`.
- **BR-16.9** Serial-tracked products reject quantities other than ±1 and reject a receipt of a serial already in physical stock for the same product (across all warehouses in the legal entity).
- **BR-16.10** Blocking never changes physical_invent; it moves quantity between inventory statuses so `physical_invent` is unchanged and `avail_physical` decreases.
- **BR-16.11** Journal state machine: `DRAFT → SUBMITTED → APPROVED → POSTING → POSTED`; `SUBMITTED → REJECTED → DRAFT`; `DRAFT|REJECTED → CANCELLED`. POSTING is a crash-safe intermediate state; the Temporal workflow owns it and is the only writer.
- **BR-16.12** Reversal of a posted journal creates a new journal of the same type with negated quantities, referencing the original; original rows are never modified.

### 3.16.8 Acceptance criteria

- **AC-16.1** (FR-16.4) Given item model group forbids physical negative and on-hand 1,000 units, when 10,000 concurrent issues of 1 unit are submitted, then exactly 1,000 succeed, 9,000 return `INV-NEG-001`, and on-hand is 0.
- **AC-16.2** (FR-16.3) Given 1,000,000 posted transactions, when the rebuild job runs, then the rebuilt OnHand table equals the live table row-for-row and the job completes in < 10 minutes.
- **AC-16.3** (FR-16.6) Given a MOVEMENT journal of 100 lines with offset account 500100, when posted, then one ledger entry exists with Σ debits = Σ credits, inventory account per posting profile, and `ledger_entry_id` set on all 100 transactions.
- **AC-16.4** (FR-16.7) Given 100 batches with distinct expiry dates and a FEFO reservation of 50 units, when reserved, then the batch with the earliest non-blocked expiry is selected first and the reservation spans batches in expiry order until 50 units are reached.
- **AC-16.5** (FR-16.5) Given a serial-tracked product with serial S-001 in stock, when a receipt of S-001 is posted, then it is rejected with `INV-SER-001`.
- **AC-16.6** (FR-16.9) Given a count plan with 5% qty tolerance, when a sheet line variance is 8%, then posting requires an approver with CycleCountApprove and the posted journal line equals counted − expected.
- **AC-16.7** (FR-16.11, FR-16.6) Given inventory period 2026-09 closed for module INVENTORY, when a journal with financial_date 2026-09-30 is posted, then it is rejected with the period-control error from wave 14 and no transaction is written.
- **AC-16.8** (FR-16.2) Given a receipt transaction at REGISTERED, when `Unregister` is called, then status returns to ARRIVED, registered bucket decreases, and a status-change audit row exists.
- **AC-16.9** (FR-16.12) Given a manual ledger journal posted to the inventory account without an inventory transaction, when the daily reconciliation runs, then a diff event is emitted with the exact amount.
- **AC-16.10** (FR-16.10) Given product unit EA with conversion 1 BOX = 12 EA, when a line of 2.5 BOX is posted, then qty = 30 EA, entered_qty = 2.5, entered_unit = BOX.

### 3.16.9 Test plan and fixtures

Fixtures: `inv-demo-plant` (200 products, 3 sites, 6 warehouses, 400 locations, 5,000 opening lines; generated); `inv-1m-txn` (1M synthetic transactions over 12 months, Zipf-distributed across 2,000 products; generated by `scripts/fixtures/inv_txn_gen.py`); `inv-fefo-100` (100 batches with expiry grid, 10 blocked); `inv-ax-extract` (InventSum/InventTrans/InventDim CSV sample from AX 2012 demo company USMF, 50k rows); `inv-sap-extract` (MARD/MCHB/MBEW/MSEG sample, 50k rows).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Status transition monotonicity | property (Go rapid) | ApplyTransactionStep | no sequence of commands produces a backward step without a named reversal command |
| On-hand formula | unit | OnHand compute | formulas in FR-16.3 hold for 10k random bucket vectors |
| Concurrency negative | E2E (k6) | journals:post | AC-16.1 |
| Rebuild equality | performance | onhand:rebuild on inv-1m-txn | zero diff, < 10 min |
| Journal GL balance | contract | ledger Post | every journal type → balanced entry |
| FEFO selection | unit + E2E | reservations | AC-16.4 |
| Outbox event schema | contract (AsyncAPI) | all 5 events | schema registry compatibility BACKWARD |
| Period control | contract | journals:post | AC-16.7 |
| PWA offline count | E2E (Playwright) | count sheet | counts queued offline sync without loss |

Reconciliation checks: Σ OnHand.posted_value per inventory account = ledger balance; Σ transaction qty (physical) = OnHand.physical_invent; count sheet variance total = COUNT journal total; AX migration: InventSum.PostedQty + Received + Registered − Deducted − Picked = Passage physical_invent per InventDimId.

### 3.16.10 Deployment and operations

Helm chart `passage-inventory` (Deployment ×3, HPA 3–12 on CPU 70%, Postgres CloudNativePG cluster `inventory-db` 3 instances, outbox Debezium connector `inventory-outbox`). Config: `INV_NEGATIVE_DEFAULT=forbid`, `INV_RESERVATION_TTL_HOURS=0`, `INV_JOURNAL_MAX_LINES=10000`, `INV_RECON_CRON=0 2 * * *`. Migrations via Atlas from entity metadata (`inventory/v16.*`). Feature flags: `inv.fefo_policy`, `inv.count_pwa`, `inv.transfer_cost_diff` (off until w20). Scheduled jobs (Temporal cron): daily reconciliation 02:00 tenant local, count sheet generation per plan cron, weekly rebuild-and-compare Sunday 03:00. SLOs: journal post p95 < 2 s for 1,000 lines; on-hand query p95 < 50 ms; event lag p95 < 5 s. Alerts: reconciliation diff ≠ 0 (page), outbox lag > 60 s (ticket), negative on-hand where forbidden (page, indicates bug). Runbook: rebuild projection, replay outbox, reverse journal. Rollback: Argo CD digest rollback; migrations are additive in this wave (no destructive change), so downgrade is safe.

### 3.16.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX InventSum (per InventDimId) | OPENING_BALANCE journal lines → InventoryTransaction + OnHand | PostedQty+Received+Registered−Deducted−Picked → qty; PostedValue+PhysicalValue → cost; reserved/ordered buckets not migrated (open orders re-created in w17/w27) |
| AX InventDim | DimensionSet | map InventSiteId→site, InventLocationId→warehouse, wMSLocationId→location, InventBatchId→batch, InventSerialId→serial, InventStatusId→inventory_status |
| AX InventTrans (closed history) | InventoryTransaction (read-only history, flag `migrated=true`) | optional; last 24 months; StatusIssue/StatusReceipt mapped 1:1 |
| AX InventBatch, InventSerial | Batch, Serial (wave 8 tracking entities) | expiry, shelf-life advice |
| AX InventModelGroup | ItemModelGroup (wave 8) | already migrated; verified here |
| SAP MARD (LABST unrestricted, INSME quality, SPEME blocked) | OpeningBalance per plant/storage location with inventory_status AVAILABLE / QUARANTINE / BLOCKED | |
| SAP MCHB (CLABS per batch) | OpeningBalance with batch dim | batch master from MCH1/MCHA |
| SAP MBEW (LBKUM, SALK3, VERPR/STPRS) | cost on opening lines | value = SALK3 split proportionally by qty |
| SAP MSEG/MKPF history | InventoryTransaction history | BWART → status mapping table (101, 102, 261, 262, 301, 311, 551, 561) |

Transformation notes: dimension hash computed after mapping; quantities converted to Passage inventory unit via MARM/UnitOfMeasureConversion; opening journal posts with financial_date = cutover date − 1 and offset account = migration clearing account. Validation: Σ migrated qty per product/site equals source extract; Σ value equals source within 0.01 × product count; no serial duplicates. Not migrated: InventSum reservation buckets (recreated by open documents), AX quarantine orders (re-entered manually; typically < 50), SAP special stock K/O (consignment/subcontracting; wave 21/41), InventClosing history (wave 20 decides).

### 3.16.12 Dependencies, risks, open questions

Depends on: wave 6 (sites, warehouses, UoM), wave 8 (products, dimension groups, item model groups, batch/serial masters), wave 11 (`Post(JournalEntry)`, posting profiles), wave 12 (currency rounding), wave 14 (period control module INVENTORY), wave 4 (Temporal, outbox), wave 10 (migration framework).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Row-lock contention on hot SKUs (one dimension set) | Medium | Medium | Lock per OnHand row only; batch issues in a journal sorted; measured by k6 at 10k concurrent |
| Dimension hash collision semantics across services | Low | High | Shared package with golden test vectors; hash includes dimension names |
| Count PWA offline conflicts | Medium | Low | Last-write-wins with audit; recount flag |

Open questions:
1. Store license plate as a dimension now or add in wave 31? Default: reserve the column, null until wave 31. Decide by wave 17 close.
2. Should RESERVED_ORDERED be supported in wave 16 or only when sales/production demand exists? Default: implement now with manual demand type. Decide by wave 16 mid-point.
3. Monthly vs yearly partitioning threshold? Default: monthly when > 20M rows. Decide by wave 20.

## 3.17 Wave 17: Purchasing: Requisitions, RFQs, Agreements & Purchase Orders

### 3.17.1 Scope and business context

Wave 17 gives buyers the procurement document chain: purchase requisition (AX PurchReqTable/PurchReqLine, SAP EBAN), request for quotation with supplier replies (PurchRFQCaseTable/PurchRFQTable/PurchRFQReplyTable, SAP EKKO document type AN with quotation maintenance ME47), purchase agreements with release tracking (PurchAgreementHeader/PurchAgreementLine, SAP contracts type MK/WK and scheduling agreements LP via ME31K/ME31L), vendor trade agreements (PriceDiscTable journal type PriceDiscAdmTrans relation PurchPrice/PurchLineDisc, SAP condition records A017/KONP via MEK1), and purchase orders with versioned change management (PurchTable/PurchLine with PurchTableHistory and change-management parameters, SAP EKKO/EKPO with change documents CDHDR/CDPOS and version management in EREV). Purchase order confirmation writes ON_ORDER/ORDERED inventory transactions from wave 16.

At the end of the wave a requester can raise a requisition that routes by amount band and category to the right approvers, a buyer can consolidate approved requisition lines into POs, run an RFQ to several suppliers and award, create blanket agreements that POs release against, and send a PO as PDF/email with an EDI 850/ORDERS hook stubbed for wave 35. Out of scope: receiving (wave 18), invoice (wave 18), budget control on commitments (wave 38), subcontracting POs from production (wave 21), landed cost (wave 34), supplier portal acknowledgement (wave 20), catalog punch-out (v1.x).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX purchase order confirmation runs PurchFormLetter with totals recalculation and PDF through SSRS; 20–90 s per PO on a loaded AOS; ME21N saves fast but output (NEU message, ME9F) depends on a scheduled RSNAST00 job | Synchronous document class chain plus report server; SAP output via NAST condition technique and batch | Confirmation is a state transition that enqueues rendering (Temporal activity) and emits `po.confirmed`; PDF rendered by a stateless worker within 3 s | Confirm latency: 20–90 s → < 2 s UI, PDF < 5 s; no batch wait |
| Price lookup in AX (PriceDisc class, PriceDiscTable search over 3 account codes × 3 item codes) is opaque; buyers cannot see why a price was chosen; SAP condition analysis (ME23N > Conditions > Analysis) exists but is 6 clicks deep | No explain output; access sequence hidden | Every PO line carries `price_source` (agreement id, trade agreement id, last price, manual) with an "explain" popover; `GET /pricing/explain` | Clicks to see why a price applied: 6–8 → 1; price disputes per 1,000 POs: tracked, target −80% |
| Change management in AX creates a full PurchTableHistory copy only when the parameter is on; comparing versions is a manual form; SAP version management requires EREV config and a reason per change | Version stored as whole-document copies without diff | Versions are stored as JSON-patch diffs against the previous version with a rendered side-by-side diff; re-approval rule evaluated per diff | Time to identify what changed between PO versions: 5 min → 10 s |
| Requisition approval in AX workflow (PurchReqReview) evaluates in the batch workflow processor every 60 s; escalation and delegation are per-user configuration pages; SAP release strategy (CL20N/CL24N classification) is nearly impossible to change without a consultant | Batch-polled workflow runtime; classification-based release strategy | Temporal approval workflow started synchronously on submit; policy is a declarative matrix (amount band × category × org) editable by a procurement admin | Submit-to-first-approver latency: 60–120 s → < 2 s; policy change lead time: days (consultant) → minutes (admin) |
| PO total in AX recomputed by PurchTotals class on demand; header/line totals can disagree until "Totals" is opened; SAP EKKO has no stored total | Totals derived lazily | Totals persisted on every line change with a database constraint test `header.total = Σ lines + charges + tax` | Total mismatches: non-zero → 0 across 1,000 random POs |

### 3.17.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity level |
|---|---|---|---|---|
| Purchase requisition | PurchReqTable, PurchReqLine; forms PurchReqTable, PurchReqReview workflow; policies (PurchReqControlRule) | EBAN/EBKN; ME51N/ME52N/ME53N; release strategy ME54N; ME57 assign source | `procurement.PurchaseRequisition`, `RequisitionLine`; Requisition workspace; policy matrix | full |
| Approval policy | Purchasing policies (PurchPolicy), amount bands via workflow conditions, category access policy | Release strategy (release groups/codes, classification characteristics CEBAN) | `ApprovalPolicy` (bands, category, org unit, requester role) compiled to Temporal workflow input | improved |
| Consolidation into PO | PurchReqConsolidation (consolidation opportunities), release approved lines (PurchReqPurchaseOrderCreate) | ME57/ME58 (assign and process), automatic PO creation ME59N | `POST /requisitions:consolidate` grouping by vendor × currency × site × delivery date window | full |
| RFQ | PurchRFQCaseTable, PurchRFQTable (per vendor), PurchRFQReplyTable, PurchRFQCompare; vendor collaboration portal reply (D365) | RFQ ME41/ME42/ME43 (EKKO BSTYP A), quotation ME47, price comparison ME49, rejection ME9A | `RFQ`, `RFQInvitation`, `RFQReply`; compare screen; award creates PO or agreement | full |
| Purchase agreement | PurchAgreementHeader, PurchAgreementLine, AgreementLineReleasedLine (release tracking); commitment types (product qty/value, category value, value) | Contracts (EKKO BSTYP K, ME31K/ME32K/ME33K), release orders, scheduling agreements (L, ME31L) with delivery schedules ME38 | `PurchaseAgreement`, `AgreementLine` (commitment types QUANTITY, VALUE, CATEGORY_VALUE, TOTAL_VALUE); release tracking | full (scheduling agreement delivery schedules partial until w22 MRP) |
| Vendor pricing | Trade agreements PriceDiscTable (PurchPrice, PurchLineDisc, PurchMultiLnDisc, PurchEndDisc), account/item code hierarchy, date validity, quantity breaks | Conditions A017 (material info record), A018, A019, A025; KONP; info record EINA/EINE; MEK1 | `TradeAgreement` (vendor > vendor group > all; product > product group > all; qty breaks; validity; currency) with explain | full |
| Purchase order | PurchTable, PurchLine, PurchTableHistory/PurchLineHistory (versions), PurchOrderStatus, DocumentStatus, PurchFormLetter_PurchOrder (confirm) | EKKO/EKPO/EKET (schedule lines)/EKKN (account assignment), ME21N/ME22N/ME23N, EREV versions | `PurchaseOrder`, `PurchaseOrderLine`, `DeliverySchedule`, `PurchaseOrderVersion` | full |
| Charges | MarkupTrans (misc. charges header/line), MarkupTable, auto charges (MarkupAutoTable) | Pricing conditions FRA1 (freight), ZOA1 (customs), header/item conditions | `Charge` (header/line; fixed, per unit, percent; debit/credit to item, vendor, ledger) | full |
| Delivery schedules | PurchLine delivery schedule (multiple delivery lines), confirmed delivery date | EKET schedule lines | `DeliverySchedule` rows per PO line | full |
| PO output | Print management (PrintMgmt), SSRS PurchPurchaseOrder report, email, AIF service PurchPurchaseOrderService | Message output NAST, forms MEDRUCK, IDoc ORDERS05 | PDF via render worker, email via wave 4 notification service; EDI hook emits `po.confirmed` for wave 35 | partial (EDI transport in w35) |
| Category hierarchy procurement | EcoResCategory procurement hierarchy, category policy | Material groups (T023), product categories | Wave 8 category hierarchy with procurement purpose | full |
| Vendor on-hold / status | VendTable Blocked (invoice/payment/all) | LFA1 SPERR/LOEVM, purchasing block LFM1 SPERM | Vendor role status (wave 7) checked on PO create/confirm | full |

### 3.17.3 Functional requirements

- **FR-17.1 Purchase requisition** — Must. The system shall let any user with RequisitionCreate raise a requisition with lines for catalog products, non-catalog items (description + category) or services, with requested date, quantity, unit, estimated price, site/warehouse, financial dimensions and attachments.
  - Statuses: DRAFT, IN_REVIEW, APPROVED, REJECTED, CLOSED (all lines consolidated), CANCELLED.
  - Estimated price defaults from TradeAgreement lookup for the preferred vendor, else last PO price, else zero with warning.
  - Maximum 500 lines; account structure validated at submit using wave 11 rules.

- **FR-17.2 Approval policy matrix** — Must. The system shall evaluate an `ApprovalPolicy` with rules (amount band in accounting currency, category, operating unit, requester role) producing an ordered approver chain, executed by the wave 4 Temporal approval template with 48 h SLA timers, escalation and delegation.
  - Bands default: 0–1,000 USD auto-approve, 1,000–10,000 manager, 10,000–100,000 manager + category owner, > 100,000 + CFO.
  - Policy versions are effective-dated; the version at submit time governs the whole instance.
  - Any line edit after submit restarts the workflow.

- **FR-17.3 Consolidation into purchase orders** — Must. The system shall group approved requisition lines into POs by vendor × currency × site × delivery window (default 7 days) with buyer override of vendor and the ability to split lines.
  - Lines with no vendor route to a "Needs sourcing" queue from which an RFQ can be started.
  - Consolidated lines keep `requisition_line_id` on the PO line for commitment tracking.

- **FR-17.4 RFQ cycle** — Must. The system shall create an RFQ with lines, invite N vendors, capture replies (price, lead time, quantity breaks, validity, notes, attachments) manually or via the supplier portal (wave 20), compare, and award whole or per line to create a PO or a purchase agreement.
  - Statuses: DRAFT, SENT, REPLIES_OPEN, CLOSED, AWARDED, CANCELLED; reply deadline enforced (late replies flagged, accepted only with override).
  - Compare view ranks by total landed estimate = price × qty + charges; ranking rule configurable (lowest price, shortest lead time, weighted score).

- **FR-17.5 Purchase agreements** — Must. The system shall support blanket agreements with commitment types QUANTITY (product × qty), VALUE (product × amount), CATEGORY_VALUE and TOTAL_VALUE, validity dates, price/discount terms, max enforcement (hard/soft) and release tracking.
  - A PO line referencing an agreement line draws down `released_qty`/`released_amount`; exceeding a hard limit rejects with `PRC-AGR-003`.
  - Agreement prices override trade agreements when `price_override=true`.

- **FR-17.6 Vendor pricing (trade agreements)** — Must. The system shall resolve unit price and line discount from `TradeAgreement` rows by most-specific match: vendor > vendor group > all vendors, product > product group > all products, with currency, unit, quantity break (from_qty) and validity (from/to dates).
  - Deterministic ordering: specificity score (vendor 4/group 2/all 0 + product 4/group 2/all 0), then latest from_date, then lowest price when still tied (configurable `tie_rule`).
  - Returns `explain` listing considered rows and the winner.
  - Lookup p95 < 20 ms (aligns with wave 26 pricing SLO; wave 26 replaces this implementation with the condition engine while keeping the API).

- **FR-17.7 Purchase order lifecycle** — Must. The system shall manage POs with approval status (DRAFT, IN_REVIEW, APPROVED, REJECTED, CONFIRMED, FINALIZED, CANCELLED) and document status (NONE, RECEIVED_PARTIAL, RECEIVED, INVOICED_PARTIAL, INVOICED) derived from downstream documents.
  - Confirmation requires approval status APPROVED and vendor not blocked; on confirm the service calls inventory `transactions:apply` to create ORDERED receipts (one per delivery schedule line) and renders output.
  - Lines carry product or category, qty, unit, unit price, discount %, line amount, delivery date, confirmed delivery date, site, warehouse, financial dimensions, tax group/item tax group (wave 13), agreement line, requisition line.

- **FR-17.8 Versioned change management** — Must. The system shall snapshot a `PurchaseOrderVersion` at each confirmation and, when change management is enabled for the vendor or legal entity, require re-approval of changes after confirmation according to the policy's `reapproval_rules` (any change / price or qty increase > X % / new line / delivery date change).
  - Version numbers monotonic from 1; diff stored as JSON Patch (RFC 6902); version document rendered and sent to vendor marked "Change order v{n}".
  - Received or invoiced quantities cannot be reduced below received/invoiced totals (`PRC-PO-007`).

- **FR-17.9 Charges** — Must. The system shall support header and line charges with calculation (FIXED, PER_UNIT, PERCENT), debit/credit sides (ITEM = capitalize into inventory cost; LEDGER = expense account; VENDOR = payable to a different vendor), and auto-charges by vendor/product group.
  - Header charges allocate to lines at invoice time by quantity, amount or line count (wave 18 consumes).

- **FR-17.10 Delivery schedules** — Must. The system shall allow one PO line to have multiple delivery schedule rows (qty, date, confirmed date) summing to line qty, each producing its own ORDERED inventory transaction.

- **FR-17.11 Totals and tax** — Must. The system shall persist header totals (`subtotal`, `discount_total`, `charges_total`, `tax_total`, `total`) recalculated on every line/charge change, calling the wave 13 tax engine, with `total = subtotal − discount_total + charges_total + tax_total` to the cent.

- **FR-17.12 PO output** — Must. The system shall render the PO/change order to PDF (template per legal entity, wave 15 document designer basics), email it to the vendor's purchasing contact, and emit `po.confirmed` for EDI/portal consumers; rendering failures do not block confirmation (retry with backoff, alert after 3 failures).

- **FR-17.13 Purchasing policies** — Should. The system shall provide entity-level policy settings: change management on/off, requisition required for PO creation (yes/no per category), PO required before invoice (consumed by wave 18), reapproval rules, consolidation window.

- **FR-17.14 Buyer workspace** — Should. The system shall show the buyer the queue of approved requisition lines, RFQs awaiting replies, POs awaiting confirmation, agreements near expiry (< 30 days) or near limit (> 90 %).

### 3.17.4 Data model

**PurchaseRequisition / RequisitionLine**

| Field | Type | Required | Notes |
|---|---|---|---|
| id, requisition_number | uuid, text | yes | sequence `PR-` |
| requester_id, on_behalf_of_id | uuid | yes/no | wave 7 worker/user |
| legal_entity_id, operating_unit_id | uuid | yes | |
| status | enum | yes | DRAFT, IN_REVIEW, APPROVED, REJECTED, CLOSED, CANCELLED |
| policy_version_id | uuid | cond. | set at submit |
| total_estimated (acct ccy) | numeric(19,4) | yes | recomputed |
| line: line_no, product_id or (description, category_id), qty, unit, estimated_unit_price, currency, requested_date, site_id, warehouse_id, preferred_vendor_id, dimension_default_id, agreement_line_id, status (OPEN, CONSOLIDATED, CANCELLED), po_line_id | | | |

**PurchaseOrder / PurchaseOrderLine / DeliverySchedule / PurchaseOrderVersion**

| Field | Type | Required | Notes |
|---|---|---|---|
| id, po_number | uuid, text | yes | sequence `PO-`, gap-free optional |
| vendor_id, invoice_vendor_id | uuid | yes | wave 7 vendor role; invoice vendor defaults to vendor |
| legal_entity_id, site_id, warehouse_id | uuid | yes | |
| currency, exchange_rate_type | text | yes | wave 12 |
| approval_status | enum | yes | DRAFT, IN_REVIEW, APPROVED, REJECTED, CONFIRMED, FINALIZED, CANCELLED |
| document_status | enum | yes | derived (FR-17.7) |
| version_no | int | yes | current confirmed version; 0 before first confirmation |
| change_management | bool | yes | copied from policy at creation |
| payment_terms_id, delivery_terms_id, mode_of_delivery | uuid/text | yes/no | wave 7 |
| subtotal, discount_total, charges_total, tax_total, total | numeric(19,4) | yes | transaction currency |
| line: line_no, product_id or category_id, description, qty, unit, unit_price, price_unit (per 1/100/1000), discount_pct, discount_amount, line_amount, delivery_date, confirmed_delivery_date, site_id, warehouse_id, dimension_set_defaults, tax_group_id, item_tax_group_id, agreement_line_id, requisition_line_id, price_source, received_qty, invoiced_qty, remaining_qty, status (OPEN, RECEIVED, INVOICED, CANCELLED) | | | |
| schedule: po_line_id, seq, qty, delivery_date, confirmed_date, inventory_transaction_id | | | Σ qty = line qty |
| version: po_id, version_no, confirmed_at, confirmed_by, snapshot (jsonb), patch (jsonb RFC 6902), reapproval_required (bool) | | | |

**PurchaseAgreement / AgreementLine**: id, agreement_number, vendor_id, legal_entity_id, currency, valid_from, valid_to, status (DRAFT, EFFECTIVE, ON_HOLD, CLOSED), default_commitment_type; line: commitment_type, product_id/category_id, committed_qty/committed_amount, unit, unit_price, discount_pct, max_enforced (HARD/SOFT/NONE), price_override (bool), released_qty, released_amount, remaining.

**TradeAgreement**: id, relation (PRICE, LINE_DISC, MULTILINE_DISC, TOTAL_DISC), account_code (VENDOR/GROUP/ALL), account_id, item_code (PRODUCT/GROUP/ALL), item_id, currency, unit, from_qty, to_qty, amount or percent, price_unit, valid_from, valid_to, site_id (optional), lead_time_days, status (DRAFT, ACTIVE, EXPIRED).

**RFQ / RFQInvitation / RFQReply**: rfq id, number, status, reply_deadline, lines (product/category, qty, unit, required_date); invitation (vendor_id, sent_at, channel EMAIL/PORTAL, status INVITED, DECLINED, REPLIED); reply (invitation_id, line_id, unit_price, currency, lead_time_days, qty_breaks jsonb, valid_until, notes, attachments, awarded (bool)).

**Charge**: id, owner_type (PO_HEADER, PO_LINE, RECEIPT, INVOICE), owner_id, charge_code_id, calc (FIXED, PER_UNIT, PERCENT), value, amount, currency, debit_type (ITEM, LEDGER, VENDOR), credit_type, debit_account_id, credit_account_id, allocation_rule.

Invariants: `po.total = Σ line_amount − discount + charges + tax` enforced by a deferred constraint trigger; Σ schedule qty = line qty; `received_qty ≤ qty` and `invoiced_qty ≤ max(received_qty, qty)`; agreement `released ≤ committed` when HARD; exactly one ACTIVE TradeAgreement wins per lookup (ties resolved by `tie_rule`). Indexes: PO (tenant, vendor, approval_status), line (po_id, line_no), schedule (po_line_id), TradeAgreement (tenant, relation, account_code, account_id, item_code, item_id, valid_from) for lookup; requisitions by requester and status.

### 3.17.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/procurement/requisitions | Create requisition | client key | RequisitionCreate |
| POST | /api/v1/procurement/requisitions/{id}:submit | Submit for approval | req id + version | RequisitionCreate |
| POST | /api/v1/procurement/requisitions:consolidate | Create POs from approved lines | line id set hash | PurchaseOrderMaintain |
| POST | /api/v1/procurement/rfqs | Create RFQ | client key | RFQMaintain |
| POST | /api/v1/procurement/rfqs/{id}:send | Send invitations | rfq id | RFQMaintain |
| POST | /api/v1/procurement/rfqs/{id}/replies | Record a reply | invitation+line | RFQMaintain / portal vendor |
| POST | /api/v1/procurement/rfqs/{id}:award | Award to vendor(s) | rfq id | RFQAward |
| POST | /api/v1/procurement/agreements | Create agreement | client key | AgreementMaintain |
| GET | /api/v1/procurement/pricing/lookup?vendor=&product=&qty=&date=&currency= | Resolve price/discount with explain | n/a | PurchaseOrderMaintain |
| POST | /api/v1/procurement/purchase-orders | Create PO | client key | PurchaseOrderMaintain |
| PATCH | /api/v1/procurement/purchase-orders/{id} | Edit header/lines (creates pending change) | ETag | PurchaseOrderMaintain |
| POST | /api/v1/procurement/purchase-orders/{id}:confirm | Confirm (new version) | po id + version_no+1 | PurchaseOrderConfirm |
| POST | /api/v1/procurement/purchase-orders/{id}:cancel | Cancel remaining | po id | PurchaseOrderMaintain |
| GET | /api/v1/procurement/purchase-orders/{id}/versions/{n}/diff | JSON Patch and rendered diff | n/a | PurchaseOrderInquire |
| GET | /api/v1/procurement/purchase-orders/{id}/document.pdf | Rendered PO | n/a | PurchaseOrderInquire |

Commands: `SubmitRequisition` (account structure valid, lines ≥ 1, no closed category), `ConsolidateLines` (same vendor/currency/site/entity, vendor not blocked), `AwardRFQ` (reply not expired, award qty ≤ RFQ qty), `ConfirmPO` (approved, vendor active, agreement limits, delivery schedules sum, tax calculated, totals constraint), `ApplyChange` (post-confirmation changes evaluated against reapproval rules; sets approval_status back to IN_REVIEW if required).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.procurement.requisition.submitted.v1` / `.approved.v1` / `.rejected.v1` | workflow transitions | requisition_id, lines (product, qty, amount), total, approver chain, policy_version | notifications, budget (w38), analytics |
| `dev.passage.procurement.rfq.sent.v1` / `.awarded.v1` | RFQ send/award | rfq_id, vendors, lines, awarded vendor per line | supplier-portal (w20), notifications |
| `dev.passage.procurement.po.confirmed.v1` | confirm | po_id, version_no, vendor_id, lines (product, qty, unit, price, delivery dates, site/warehouse), totals, output channel | inventory, receiving (w18), supplier-portal, EDI (w35), analytics |
| `dev.passage.procurement.po.version.created.v1` | any confirmed version > 1 | po_id, version_no, patch, reapproval_required | supplier-portal, receiving |
| `dev.passage.procurement.po.cancelled.v1` | cancel | po_id, lines cancelled, remaining qty | inventory (cancel ORDERED), supplier-portal |
| `dev.passage.procurement.agreement.threshold.v1` | released ≥ 90 % or expiry < 30 d | agreement_id, line, pct, expiry | notifications |

### 3.17.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Procurement workspace (buyer) | Tiles and lists: lines to source, RFQs open, POs to confirm, agreements expiring, change orders pending re-approval | counts, aging | PurchaseOrderInquire |
| Requisition (requester, self-service) | Add product from catalog search, non-catalog line, submit, track approval | product, qty, price est., date, dimensions, approval timeline | RequisitionCreate |
| Approval task (shell inbox) | Approve, reject, delegate, request change, comment | requisition summary, lines, policy band hit, attachments | RequisitionApprove |
| Consolidation board | Group suggestions, drag lines between POs, change vendor, create POs | vendor, currency, site, lines, estimated total | PurchaseOrderMaintain |
| RFQ | Create, invite vendors, record replies, compare, award | lines, invitations, replies grid (price, lead time, validity), ranking | RFQMaintain |
| Purchase agreement | Create, add lines, view releases | commitment type, committed, released, remaining %, validity | AgreementMaintain |
| Purchase order | Create, add lines (price explain), charges, delivery schedules, confirm, view versions diff, print/email | header, lines grid, totals panel live, version timeline | PurchaseOrderMaintain |
| Trade agreement journal | Create rows in grid, validate overlap, post (activate) | relation, account/item codes, qty break, price, validity | TradeAgreementMaintain |

Interaction rules: line grid keyboard entry with product search-as-you-type (p95 < 100 ms via wave 8 search index); price lookup fires on product/qty/vendor/date change and shows source badge; totals panel updates within 200 ms of any edit (server computed, optimistic client); bulk edit on selected lines (delivery date, site, dimensions); validation timing: inline for required/format, on `:confirm` for agreement limits and tax; approvers can act from email deep links with Keycloak session; personalization: saved views per buyer.

### 3.17.7 Business rules and edge cases

- **BR-17.1** Requisition state machine: `DRAFT → IN_REVIEW → APPROVED → CLOSED`; `IN_REVIEW → REJECTED → DRAFT`; `DRAFT|REJECTED → CANCELLED`; `APPROVED` lines may be individually CANCELLED by the requester before consolidation.
- **BR-17.2** PO approval state machine: `DRAFT → IN_REVIEW → APPROVED → CONFIRMED`; `CONFIRMED —(change requiring re-approval)→ IN_REVIEW`; `CONFIRMED —(change not requiring)→ CONFIRMED (version+1)`; `CONFIRMED → FINALIZED` when document_status = INVOICED and remaining = 0; any non-finalized → CANCELLED if received_qty = 0 and invoiced_qty = 0.
- **BR-17.3** Price resolution date is the PO line creation date by default, switchable to delivery date per policy; a trade agreement with `valid_to` null is open-ended; overlapping rows with equal specificity and dates are rejected at activation (`PRC-TA-002`).
- **BR-17.4** Quantity breaks use `from_qty ≤ qty < to_qty` in the agreement's unit after conversion; conversion failure falls back to the next less-specific row and records the fallback in explain.
- **BR-17.5** Rounding: unit price stored to 4 decimals of currency minor units (e.g., 12.3456 USD per price_unit); line_amount = round_half_even(qty × unit_price / price_unit × (1 − discount_pct/100), minor units); header totals are sums of rounded line amounts (never re-rounded), matching AX PurchTotals and SAP KOMP netwr behavior.
- **BR-17.6** Confirmation in a different fiscal period than creation is allowed; commitment date for budget (wave 38) is the confirmation date.
- **BR-17.7** Reducing PO line qty below `received_qty` is rejected; reducing below `invoiced_qty` but above `received_qty` is allowed only when the policy flag `allow_invoice_before_receipt` is true.
- **BR-17.8** Concurrency: PO edits use optimistic concurrency via ETag (`row_version`); a stale PATCH returns 412 with the current version and diff.
- **BR-17.9** Deleting a line on a confirmed PO cancels its remaining ORDERED inventory transactions; the line remains with status CANCELLED in versions.
- **BR-17.10** Agreement validity: a PO line may reference an agreement line only if the PO's requested delivery date falls within `valid_from..valid_to`; SOFT limit overrun emits a warning and `agreement.threshold`; HARD rejects.
- **BR-17.11** RFQ replies received after `reply_deadline` are stored with `late=true` and excluded from ranking unless the buyer overrides with a reason.
- **BR-17.12** Vendor currency differing from PO currency: PO currency defaults from the vendor's purchase currency; overriding requires an exchange rate available on the PO date (wave 12), else `PRC-FX-001`.

### 3.17.8 Acceptance criteria

- **AC-17.1** (FR-17.2) Given the default band policy, when 30 requisitions spanning 6 amount bands × 5 categories are submitted, then each routes to the expected approver chain per the test matrix in `fixtures/procurement/approval-matrix.csv`.
- **AC-17.2** (FR-17.8) Given a confirmed PO with change management on and rule "qty increase > 5 % requires re-approval", when a line qty is raised from 100 to 110, then version 2 is created with a JSON Patch on `/lines/0/qty`, approval_status = IN_REVIEW, and the vendor is not notified until re-confirmation.
- **AC-17.3** (FR-17.8) Given the same PO, when the delivery date moves by one day and the rule excludes dates, then version 2 is created with approval_status CONFIRMED and the change order PDF is emailed.
- **AC-17.4** (FR-17.6) Given rows for vendor V (price 10), vendor group G containing V (price 9), and all vendors (price 8), all valid, when lookup for V runs, then price 10 is returned and explain lists all three rows (property test over random overlapping sets: most specific valid row always wins).
- **AC-17.5** (FR-17.11) Given 1,000 random POs with 1–50 lines, charges and tax, when totals are read, then `total = subtotal − discount_total + charges_total + tax_total` to the cent for all 1,000.
- **AC-17.6** (FR-17.7) Given an approved PO with 2 delivery schedule rows, when confirmed, then 2 inventory transactions at ORDERED exist with matching quantities and dates and on-hand `ordered` increases accordingly.
- **AC-17.7** (FR-17.5) Given a QUANTITY agreement line of 1,000 EA with HARD enforcement and 950 released, when a PO line of 100 EA references it, then confirmation fails with `PRC-AGR-003`.
- **AC-17.8** (FR-17.4) Given an RFQ sent to 3 vendors with 2 replies before deadline and 1 after, when compared, then the late reply is excluded and awarding to the lowest creates a PO with the reply price and lead time.
- **AC-17.9** (FR-17.3) Given 10 approved requisition lines across 2 vendors and 2 sites, when consolidated with the default window, then 4 POs are created and each requisition line references its PO line.
- **AC-17.10** (FR-17.12) Given the PDF renderer is down, when a PO is confirmed, then confirmation succeeds, `po.confirmed` is emitted, and the render activity retries 3 times before alerting.

### 3.17.9 Test plan and fixtures

Fixtures: `proc-demo-vendors` (40 vendors with groups, payment terms, currencies USD/EUR; 300 trade agreement rows); `proc-approval-matrix` (30 requisitions, expected chains); `proc-po-1000` (1,000 random POs generated with seed for totals test); `proc-rfq-cases` (12 RFQs); `proc-ax-extract` (PurchTable/PurchLine/PurchAgreementHeader/PriceDiscTable from USMF); `proc-sap-extract` (EKKO/EKPO/EKET/KONP/A017 samples).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Price specificity | property | pricing/lookup | most-specific valid row wins over 10k random sets |
| Totals identity | property + E2E | PO totals | AC-17.5 |
| Approval routing | E2E (Temporal test server) | requisition workflow | AC-17.1 |
| Version diff | unit | JSON Patch generation/apply | apply(patch, v_n) = v_{n+1} for all fixtures |
| Confirm → inventory | contract | inventory `transactions:apply` | AC-17.6 |
| Agreement limits | unit | ConfirmPO | AC-17.7 |
| PDF render | E2E | render worker | PDF/A-2b valid, < 5 s |
| Lookup latency | performance | pricing/lookup at 500 rps | p95 < 20 ms |
| Event schemas | contract | 6 events | AsyncAPI BACKWARD compatible |

Reconciliation checks: Σ requisition lines CONSOLIDATED = Σ PO lines with requisition_line_id; Σ agreement released = Σ confirmed PO line qty/amount referencing it; Σ inventory ORDERED qty per PO line = remaining open schedule qty.

### 3.17.10 Deployment and operations

Helm chart `passage-procurement` (API Deployment ×3, render worker ×2, Temporal worker ×2, CloudNativePG `procurement-db`, outbox connector). Config: `PRC_CONSOLIDATION_WINDOW_DAYS=7`, `PRC_CHANGE_MGMT_DEFAULT=true`, `PRC_RFQ_LATE_POLICY=flag`, `PRC_PDF_TIMEOUT_S=20`. Migrations `procurement/v17.*`. Feature flags: `prc.rfq_portal_replies` (on in w20), `prc.edi_output` (w35), `prc.delivery_date_pricing`. Jobs: agreement threshold scan daily 06:00; RFQ deadline closer hourly; approval SLA escalation (Temporal timers). SLOs: PO create/confirm API p95 < 500 ms; requisition submit to workflow start < 2 s; PDF < 5 s p95. Alerts: workflow task backlog > 100 for 10 min; render failures > 5/h; pricing lookup p95 > 50 ms. Runbook: re-render document, restart stuck approval (Temporal signal), re-emit `po.confirmed`. Rollback: additive migrations; flag off `prc.change_mgmt` if version logic misbehaves (POs remain confirmable without versions).

### 3.17.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX PurchTable/PurchLine (open: PurchStatus Backorder, open qty > 0) | PurchaseOrder/Line version 1, approval_status CONFIRMED | remaining qty = PurchQty − received; received history not migrated (opening on-hand covers stock) |
| AX PurchAgreementHeader/Line, AgreementLineReleasedLine | PurchaseAgreement/Line with released totals | released computed from source releases |
| AX PriceDiscTable (relation PurchPrice, PurchLineDisc) | TradeAgreement | AccountCode/ItemCode map 1:1; QuantityAmountFrom/To → from_qty/to_qty |
| AX PurchReqTable approved-not-consolidated | PurchaseRequisition APPROVED | in-review requisitions re-submitted after cutover |
| AX PurchRFQ* | not migrated | open RFQs re-issued |
| SAP EKKO/EKPO/EKET (BSTYP F, open per EKPO ELIKZ ≠ X) | PurchaseOrder/Line/DeliverySchedule | NETPR/PEINH → unit_price/price_unit; MENGE − delivered |
| SAP EKKO BSTYP K/L, EKAB release docs | PurchaseAgreement/Line | KTMNG target qty, KTWRT target value |
| SAP A017/A018 + KONP (PB00, RA01) | TradeAgreement | KBETR/KPEIN; KONM scales → qty breaks |
| SAP EBAN open (BANPR approved) | PurchaseRequisition | |

Validation: count and Σ open amount per vendor equal source; sample 50 POs compared field-by-field; price lookup for 200 random vendor/product pairs matches source price. Not migrated: closed POs (reporting only, in lakehouse wave 46), PurchTableHistory versions, SAP change documents, NAST output logs.

### 3.17.12 Dependencies, risks, open questions

Depends on: wave 16 (ORDERED transactions, on-hand), wave 7 (vendors, groups, terms, blocking), wave 8 (products, categories, search), wave 11–13 (dimensions, currency, tax), wave 4 (approval workflow, notifications), wave 5 (shell inbox, document templates basics).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Wave 26 pricing engine replaces trade agreement lookup; behavior drift | Medium | Medium | Keep `pricing/lookup` contract and fixture set; wave 26 must pass wave 17 tests |
| Re-approval rule complexity grows | High | Low | Rules are a small DSL with fixtures; defaults cover 90 % |
| PDF template per legal entity before document designer (w15 basics only) | Medium | Low | One default template plus logo/address substitution |

Open questions:
1. Should requisitions support punch-out catalogs in v1? Default: no, v1.x. Decide by wave 20.
2. Gap-free PO numbering per legal entity by default? Default: off (sequence with gaps allowed); on for countries requiring it. Decide by wave 18.
3. Scheduling-agreement delivery schedules from MRP: wave 22 or wave 17 manual only? Default: manual only now. Decide by wave 21.

## 3.18 Wave 18: Receiving, Product Receipts & Three-Way Match

### 3.18.1 Scope and business context

Wave 18 closes the physical and financial loop on a purchase order. Receiving moves a PO line's inventory transaction from ORDERED through ARRIVED (arrival journal, SAP inbound delivery VL31N), REGISTERED (registration, SAP GR to quality/blocked stock) to RECEIVED by posting a product receipt (AX VendPackingSlipJour/VendPackingSlipTrans, SAP MKPF/MSEG movement type 101 with EKBE history). The product receipt posts inventory value against the GR/IR accrual account (AX "Purchase, accrual" / "Accrued purchases" in the posting profile, SAP WRX GR/IR clearing via OBYC). A vendor invoice (AX VendInvoiceInfoTable/VendInvoiceInfoLine pending invoice → VendInvoiceJour/VendInvoiceTrans; SAP RBKP/RSEG via MIRO) is captured manually, from an OCR/AP-automation adapter, or (wave 35) as UBL/Peppol, and the matching engine compares invoice price, quantity and total to PO and receipt within tolerances (AX price tolerance per item/vendor, quantity tolerance, invoice totals tolerance; SAP tolerance keys PP, DQ, BD, AN/AP, DW, KW). Matched invoices post the vendor liability (VendTrans / BSIK) and clear GR/IR; exceptions route to a Temporal resolution workflow. Evaluated receipt settlement (ERS / MRRL) generates invoices from receipts for vendors flagged for self-billing.

Out of scope: payment (wave 19), warehouse-directed put-away and license plates (wave 31), quality orders on receipt (wave 33), landed cost (wave 34), Peppol transport (wave 35), invoice-cost adjustments to inventory beyond the direct posting (wave 20 settles invoice-receipt cost differences), non-PO expense invoices with GL distribution (included here as 0-way "invoice register" with coding, approval by wave 4 workflow).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX pending vendor invoice form (VendInvoiceInfoTable) runs "Match product receipts" as a dialog, then matching icons update only after "Update match status"; users re-key invoice lines from paper; SAP MIRO proposes lines but the tolerance log (RBKP block indicator, MRBR release) is a separate transaction | Matching is a form-triggered class (VendInvoiceMatch), not an engine; capture is manual | Matching engine runs on every invoice save and on every receipt posting (re-match candidates), status visible inline; OCR adapter pre-fills lines; `MatchResult` per line with variances | Invoice entry time: 4–6 min → < 60 s; manual re-match steps: 3 → 0 |
| GR/IR reconciliation: AX "Accrued purchases" report and SAP MR11 (GR/IR maintenance) plus F.19 regrouping are month-end chores; differences hide for months | GR/IR balance is an account balance with no per-PO-line open-item ledger in AX; SAP EKBE requires MR11 to clear residuals | `GrirOpenItem` per PO line × receipt, cleared by invoice line; GR/IR report = Σ open items and must equal the account balance (daily reconciliation) | Month-end GR/IR effort: 4–8 h → < 15 min; unexplained residual: non-zero → 0 |
| Receipt posting latency: AX product receipt via PurchFormLetter_PackingSlip takes 10–40 s per receipt; SAP MIGO posts in ~1 s but inventory valuation updates MBEW synchronously causing lock waits (table lock on MBEW/MARD during month-end) | Document class chain and serialized valuation updates | Receipt posting is one inventory `transactions:apply` + one ledger `Post`; value updated per transaction, no material-level lock | Receipt post p95: 10–40 s (AX) / lock waits at close (SAP) → < 1.5 s, no cross-document locks |
| Tolerance configuration: AX has three separate forms (price tolerance per item/vendor/company, quantity tolerance on item, totals tolerance in parameters) with "Allow price tolerance" flags scattered; SAP OMR6 tolerance keys use cryptic codes and a company-code-level default | Settings scattered without a single evaluation order | One `MatchingPolicy` entity with ordered scopes (vendor×item > vendor > item > entity) and explicit percent/absolute limits, previewable with "test an invoice" | Time to configure and verify a tolerance change: 1–2 h with testing → 5 min with the policy tester |
| ERS/self-billing in AX requires a batch "Generate invoices from product receipts"; SAP MRRL is a batch run; both mis-handle partial receipts with price changes | Batch design; invoice creation decoupled from the receipt event | ERS invoice created by the `receipt.posted` consumer within seconds using the price at receipt date and locked PO price | ERS invoice latency: nightly batch → < 10 s; ERS price mismatches: tracked, target 0 |

### 3.18.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity level |
|---|---|---|---|---|
| Arrival / registration | Arrival overview (WMSArrivalOverview), arrival journal (WMSJournalTable type Reception), registration (InventTransRegister) | Inbound delivery VL31N, GR into blocked stock (103/105), quality inspection stock | `Arrival` (ARRIVED), `Registration` (REGISTERED) steps on ReceiptLine; arrival overview screen | full |
| Product receipt | PurchFormLetter_PackingSlip → VendPackingSlipJour/Trans, InventTrans Received, ledger posting PurchPackingSlip/accrual | MIGO 101 → MKPF/MSEG, EKBE (VGABE 1), GR/IR via WRX, inventory BSX | `ProductReceipt`/`ReceiptLine`; RECEIVED transactions; ledger entry Dr inventory / Cr GR/IR | full |
| Receipt reversal / correction | Product receipt correction, cancel (VendPackingSlipJour cancel) | 102 reversal, MBST cancel document | `:correct` (new receipt with delta qty), `:cancel` (full reversal) | full |
| Vendor invoice (PO) | VendInvoiceInfoTable/Line (pending), VendInvoiceJour/Trans (posted), invoice register/approval journals | MIRO → RBKP/RSEG, EKBE VGABE 2; invoice park MIR7 | `VendorInvoice`/`VendorInvoiceLine` (DRAFT → POSTED) | full |
| Non-PO invoice | Invoice journal (LedgerJournalTrans type VendInvoiceRegister / Vend), invoice register + approval | FB60, MIRO without PO | `VendorInvoice` kind EXPENSE with GL distribution lines | full |
| Matching | VendInvoiceMatch, match status (VendInvoiceMatchingStatus), price/qty/totals tolerances (VendInvoicePriceTolerance, InventTable quantity tolerance, Accounts payable parameters) | LIV tolerance keys OMR6 (PP, DQ, BD, AN, AP, DW, KW, ST, VP), blocking indicator RBKP ZLSPR, MRBR release | `MatchingEngine`, `MatchingPolicy`, `MatchResult`; exception workflow | full |
| Accrued purchases / GR/IR | Accrued purchases report (PurchAccrual), posting types PurchaseAccrual/PurchasePackingSlip | GR/IR clearing WRX, MR11, F.19, MB5S | `GrirOpenItem`, GR/IR report | improved |
| ERS | Vendor self-billing: "Generate invoices from product receipts" (PurchFormLetter with VendInvoiceInfoTable auto) | MRRL evaluated receipt settlement, LFM1 XERSY | `ErsPolicy` on vendor; consumer creates invoice from receipt | full |
| Invoice capture adapters | Invoice capture (D365 add-in), AIF VendVendInvoiceService | OpenText VIM, SAP Invoice Management, IDoc INVOIC | `POST /invoices:capture` with OCR adapter interface (JSON), UBL mapping stub | partial (Peppol w35) |
| Charges allocation at invoice | MarkupTrans allocation on invoice | Unplanned delivery costs, planned conditions FRA1 | `Charge` allocation rules applied to invoice lines | full |
| Period control AP | Ledger calendar module Accounts payable | OB52 posting periods account type K | wave 14 period control module AP at `:post` | full |

### 3.18.3 Functional requirements

- **FR-18.1 Arrival and registration** — Must. The system shall record arrival (ARRIVED) with carrier, packing slip number, pallet count and expected PO lines, then registration (REGISTERED) with quantity, batch, serial, location and inventory status, for any confirmed PO line with remaining quantity.
  - Over-receipt beyond `qty × (1 + over_delivery_pct)` (item default 0 %, policy per vendor×item) is rejected with `RCV-OVR-001`; under-delivery below `under_delivery_pct` closes the line only if `close_short=true`.
  - Serial-tracked lines require one serial per unit; duplicate serial rejected by inventory (`INV-SER-001`).

- **FR-18.2 Product receipt posting** — Must. The system shall post a `ProductReceipt` for registered or directly received quantities, moving inventory transactions to RECEIVED, writing `cost_amount_physical = qty × PO unit price (converted to accounting currency at the receipt date rate) + ITEM charges`, and posting Dr inventory (posting profile) / Cr GR/IR accrual through `Post(JournalEntry)` with idempotency key = receipt id.
  - For category (non-stock) lines the debit goes to the line's expense account; GR/IR credit is unchanged.
  - A `GrirOpenItem` is created per receipt line with amount in transaction and accounting currency.
  - Receipt date must be in an open INVENTORY period; packing slip number unique per vendor (`RCV-DUP-001`).

- **FR-18.3 Receipt correction and cancellation** — Must. The system shall support correction (delta quantity, positive or negative) and cancellation (full reversal) of a posted receipt while the receipt line has not been invoiced; invoiced lines require an invoice credit first.

- **FR-18.4 Vendor invoice capture** — Must. The system shall create a `VendorInvoice` in DRAFT from manual entry, from the capture adapter (`:capture` JSON with header fields, lines, confidence scores, page images) or from ERS, with vendor, invoice number, dates, currency, totals, lines linked to PO lines and/or receipt lines, charges, tax groups.
  - Duplicate check: same vendor + invoice number (case-insensitive, whitespace-stripped) within 24 months → `AP-DUP-001`.
  - Lines may be proposed from open receipts (3-way) or open PO lines (2-way); proposal fills qty = uninvoiced received qty.
  - Expense invoices (no PO) carry GL distribution lines validated by account structure.

- **FR-18.5 Matching policy** — Must. The system shall evaluate a `MatchingPolicy` resolved by scope precedence vendor×item > vendor > item > legal entity with fields: `match_level` (NONE, TWO_WAY, THREE_WAY), `price_tol_pct` (default 2.0), `price_tol_abs` (default 25.00 acct ccy per line), `qty_tol_pct` (default 5.0), `total_tol_pct` (default 1.0), `total_tol_abs` (default 50.00), `small_diff_abs` (default 0.50, auto-posted to small-differences account), `price_tol_mode` (UPPER_ONLY / BOTH), `date_tol_days` (0 = off).
  - A line passes price if `|invoice_unit_price − po_unit_price| ≤ max(po_price × pct, abs)` (lower prices always pass in UPPER_ONLY).
  - A line passes quantity (3-way) if `invoice_qty ≤ received_uninvoiced_qty × (1 + qty_tol_pct/100)`; 2-way compares to PO remaining qty.
  - Total passes if `|invoice_total − Σ matched line totals − charges − tax| ≤ max(total_tol_pct, total_tol_abs)`.

- **FR-18.6 Matching engine and status** — Must. The system shall compute `MatchResult` per line and header with statuses `NOT_MATCHED`, `MATCHED`, `PRICE_VARIANCE`, `QTY_VARIANCE`, `TOTAL_VARIANCE`, `NO_RECEIPT`, `WAITING_RECEIPT`; header status is the worst line status; the engine re-runs automatically when a receipt posts for a referenced PO line.
  - Engine is pure (inputs → result) and exposed at `POST /matching:evaluate` for the policy tester.
  - Price variance within tolerance posts the difference to the purchase price variance account (standard-cost items, wave 20 PPV) or to inventory/expense (actual-cost items).

- **FR-18.7 Exception workflow** — Must. The system shall start a Temporal `InvoiceMatchException` workflow for any invoice with a variance status, assigning to the buyer of the PO (price) or receiving supervisor (quantity) with actions: accept variance (post with reason code), request credit note, adjust PO price (creates PO version via wave 17), wait for receipt (timer, re-evaluate), reject invoice.
  - SLA 3 business days then escalation to AP manager; all actions audited on the invoice.

- **FR-18.8 Invoice posting** — Must. The system shall post a matched or accepted invoice: Dr GR/IR (clearing `GrirOpenItem` to the invoiced amount) or Dr expense/inventory (2-way/expense), Dr tax recoverable (wave 13), Cr vendor control account (posting profile) creating a `VendorTransaction` open item with due date and cash discount date per payment terms, moving PO inventory transactions to PURCHASED for invoiced quantity and setting `cost_amount_posted` from the invoice.
  - Idempotency key = invoice id; AP period must be open; posting date ≠ invoice date allowed (both stored).
  - Differences between receipt value and invoice value for stock items post to inventory (actual cost methods) or PPV (standard), and are recorded on the transaction as `cost_amount_adjustment` for wave 20.

- **FR-18.9 GR/IR reconciliation** — Must. The system shall provide the GR/IR report listing open items (received not invoiced, invoiced not received) by vendor, PO, line, age, with totals that equal the GR/IR ledger balance; a daily job emits `reconciliation.diff` if they differ.

- **FR-18.10 Evaluated receipt settlement** — Must. The system shall, for vendors with `ers_enabled`, create and post an invoice from each receipt within 10 s using PO price and the vendor's ERS invoice number format (`ERS-{receipt_number}`), tax per PO line, and send the self-billing document to the vendor.

- **FR-18.11 Charges on receipt and invoice** — Must. The system shall allocate header charges to lines by the rule on the charge (QTY, AMOUNT, LINES) at invoice posting, with ITEM-type charges capitalized into `cost_amount_posted`.

- **FR-18.12 Invoice credit notes** — Should. The system shall create a credit note (negative invoice) referencing an invoice and optionally a receipt correction; it posts the reverse entries and creates a negative `VendorTransaction` for wave 19 settlement.

- **FR-18.13 Receiving workspace and mobile receipt** — Should. The system shall provide a receiving workspace (expected today, overdue, registered not posted) and a PWA receipt screen scanning PO number, product GTIN and batch/serial barcodes (GS1 parsing from wave 8).

### 3.18.4 Data model

**ProductReceipt / ReceiptLine**

| Field | Type | Required | Notes |
|---|---|---|---|
| id, receipt_number | uuid, text | yes | sequence `RCV-` |
| vendor_id, po_id | uuid | yes | one PO per receipt (multi-PO receipts split) |
| packing_slip_number | text | yes | unique per vendor |
| receipt_date, posting_date | date | yes | inventory physical/financial dates |
| status | enum | yes | ARRIVED, REGISTERED, POSTED, CORRECTED, CANCELLED |
| ledger_entry_id | uuid | no | |
| exchange_rate | numeric(18,8) | yes | PO currency → accounting at receipt_date |
| line: po_line_id, schedule_id, qty, unit, dimension_set_id, batch_id, serial_ids[], inventory_status, unit_price (from PO), amount_txn, amount_acct, charges_item_amount, inventory_transaction_id, invoiced_qty, grir_open_item_id | | | |

**VendorInvoice / VendorInvoiceLine / MatchResult**

| Field | Type | Required | Notes |
|---|---|---|---|
| id, internal_number | uuid, text | yes | sequence `VI-` |
| vendor_id, invoice_vendor_id | uuid | yes | |
| vendor_invoice_number | text | yes | duplicate check |
| kind | enum | yes | PO, EXPENSE, CREDIT_NOTE, ERS |
| invoice_date, posting_date, due_date, discount_date | date | yes/no | from payment terms |
| currency, exchange_rate | text, numeric | yes | |
| status | enum | yes | DRAFT, IN_MATCHING, EXCEPTION, APPROVED, POSTED, REJECTED, CANCELLED |
| match_status | enum | yes | header worst-of |
| subtotal, charges_total, tax_total, total, total_entered | numeric(19,4) | yes | `total_entered` keyed from paper; must equal computed total within small_diff |
| source | enum | yes | MANUAL, OCR, UBL, ERS, PORTAL |
| capture_confidence | numeric(5,2) | no | from adapter |
| ledger_entry_id, vendor_transaction_id | uuid | no | after post |
| line: po_line_id, receipt_line_id, product_id/description, qty, unit, unit_price, line_amount, tax_group_id, item_tax_group_id, gl_account_id (expense), dimension_default_id, charges_allocated | | | |
| match_result: line_id, policy_id, price_variance_amt, price_variance_pct, qty_variance, total_variance, status, evaluated_at, accepted_by, reason_code | | | |

**GrirOpenItem**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | |
| po_line_id, receipt_line_id | uuid | yes | |
| vendor_id | uuid | yes | |
| received_qty, invoiced_qty | numeric | yes | |
| received_amount_acct, invoiced_amount_acct | numeric(19,4) | yes | |
| status | enum | yes | OPEN, PARTIAL, CLEARED, WRITTEN_OFF |
| cleared_at | timestamptz | no | |

**MatchingPolicy**: id, scope (ENTITY, ITEM, VENDOR, VENDOR_ITEM), vendor_id, product_id, match_level, price_tol_pct, price_tol_abs, qty_tol_pct, total_tol_pct, total_tol_abs, small_diff_abs, price_tol_mode, date_tol_days, valid_from, valid_to.

Invariants: Σ GrirOpenItem (received − invoiced) in accounting currency = GR/IR ledger balance; receipt line `invoiced_qty ≤ qty`; invoice POSTED implies every line has MATCHED or accepted status; VendorTransaction amount = invoice total in transaction currency. Indexes: receipts (tenant, vendor, packing_slip_number) unique; invoices (tenant, vendor_id, lower(vendor_invoice_number)) unique partial where status ≠ CANCELLED; GrirOpenItem (po_line_id, status).

### 3.18.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/receiving/arrivals | Record arrival | client key | ReceivingMaintain |
| POST | /api/v1/receiving/receipts | Create receipt (register lines) | client key | ReceivingMaintain |
| POST | /api/v1/receiving/receipts/{id}:post | Post product receipt | receipt id | ReceivingPost |
| POST | /api/v1/receiving/receipts/{id}:correct | Delta correction | receipt id + seq | ReceivingPost |
| POST | /api/v1/receiving/receipts/{id}:cancel | Reverse | receipt id | ReceivingPost |
| POST | /api/v1/payables/invoices | Create invoice (manual) | client key | VendorInvoiceMaintain |
| POST | /api/v1/payables/invoices:capture | Adapter/OCR intake | adapter doc id | service account / VendorInvoiceCapture |
| POST | /api/v1/payables/invoices/{id}:propose-lines | Propose from receipts/PO | n/a | VendorInvoiceMaintain |
| POST | /api/v1/payables/invoices/{id}:match | Run matching now | n/a | VendorInvoiceMaintain |
| POST | /api/v1/payables/invoices/{id}:post | Post | invoice id | VendorInvoicePost |
| POST | /api/v1/payables/matching:evaluate | Policy tester (pure) | n/a | MatchingPolicyMaintain |
| GET | /api/v1/payables/grir?vendor=&age_gt= | GR/IR open items | n/a | APInquire |
| POST | /api/v1/payables/invoices/{id}/exceptions/{line}:accept | Accept variance | line+reason | InvoiceExceptionResolve |

Commands: `PostReceipt` (open period, over-delivery, serial uniqueness, PO confirmed, exchange rate exists), `PostInvoice` (AP period open, match status acceptable, totals within small_diff, tax calculated, duplicate check), `AcceptVariance` (reason code required, amount ≤ approver limit from policy).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.receiving.receipt.posted.v1` | receipt POSTED | receipt_id, po_id, lines (po_line, qty, dims, batch, serials, amount_acct), ledger_entry_id | inventory, payables (re-match, ERS), costing, supplier-portal |
| `dev.passage.receiving.receipt.corrected.v1` | correction/cancel | receipt_id, delta lines | payables, costing |
| `dev.passage.payables.invoice.captured.v1` | DRAFT created from adapter | invoice_id, vendor, source, confidence | notifications |
| `dev.passage.payables.invoice.match.exception.v1` | variance status | invoice_id, line_id, status, variances, assignee | workflow, notifications |
| `dev.passage.payables.invoice.posted.v1` | POSTED | invoice_id, vendor_transaction_id, lines, totals, grir cleared amounts, ledger_entry_id | ledger reconciliation, costing, tax, analytics |
| `dev.passage.payables.grir.cleared.v1` | GrirOpenItem CLEARED/WRITTEN_OFF | open_item_id, amounts | analytics |

### 3.18.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Receiving workspace | Expected today/overdue, arrivals, registered-not-posted, post | PO, vendor, line, expected qty, arrived qty | ReceivingMaintain |
| Receipt (desktop + PWA) | Scan PO/GTIN/batch/serial, register, post, print receipt label | lines with remaining qty, batch/serial entry, location, status | ReceivingMaintain |
| Vendor invoice | Enter/review captured header, propose lines, match status badges, resolve, post | header, lines with PO/receipt links, variance columns, totals vs entered | VendorInvoiceMaintain |
| Invoice exception task | Accept, request credit, adjust PO, wait, reject | variance detail, policy limits, history | InvoiceExceptionResolve |
| Matching policy + tester | Create policy rows, "test an invoice" | scopes, tolerances, sample invoice result | MatchingPolicyMaintain |
| GR/IR report | Filter by age/vendor, drill to PO/receipt/invoice, write-off | open items, totals vs ledger balance | APInquire |

Interaction rules: captured fields with confidence < 0.85 are highlighted and require explicit confirmation; totals-entered vs computed difference shown live; keyboard: Ctrl+M match, Ctrl+P post; PWA receipt supports camera scan and offline registration queue; bulk post of registered receipts; personalization of invoice line columns.

### 3.18.7 Business rules and edge cases

- **BR-18.1** Receipt state machine: `ARRIVED → REGISTERED → POSTED`; `POSTED → CORRECTED` (new correction receipt, original unchanged); `POSTED → CANCELLED` only if invoiced_qty = 0 on all lines; `ARRIVED|REGISTERED → CANCELLED` releases inventory statuses back to ORDERED.
- **BR-18.2** Invoice state machine: `DRAFT → IN_MATCHING → (MATCHED→) APPROVED → POSTED`; `IN_MATCHING → EXCEPTION → APPROVED|REJECTED`; `DRAFT|EXCEPTION → CANCELLED`; POSTED is terminal (reverse via credit note).
- **BR-18.3** Price comparison uses PO unit price in PO currency normalized to invoice price unit; invoice in a different currency than the PO is compared after conversion at the invoice date rate and flagged `currency_mismatch=true` (always exception unless policy allows).
- **BR-18.4** Quantity tolerance applies to over-invoicing only; under-invoicing leaves the receipt line partially open (`PARTIAL`) for a later invoice.
- **BR-18.5** Small differences: if the header total variance ≤ `small_diff_abs` the difference posts to the small-differences account and the invoice is MATCHED; the amount is recorded on `MatchResult`.
- **BR-18.6** Rounding: variance percent computed on unrounded values and compared at 4 decimals; amounts rounded half-even to minor unit; the GR/IR clearing amount is the receipt-side accounting amount for the invoiced quantity (pro-rata, rounded), so GR/IR residual after full invoicing of a line is exactly 0.
- **BR-18.7** Receipt date rate vs invoice date rate: GR/IR is cleared at the receipt-side accounting amount; the difference between invoice accounting amount and that clearing amount posts to inventory/expense (not FX gain/loss), matching SAP KDM/AX exchange adjustment on purchase.
- **BR-18.8** Concurrency: posting a receipt and an invoice for the same PO line serializes on the PO line row lock (`SELECT … FOR UPDATE` on `purchase_order_line`); the invoice's matching always sees the committed receipt state.
- **BR-18.9** ERS invoices skip matching (status MATCHED by construction) and are blocked if the PO price has changed since receipt until a buyer confirms the price.
- **BR-18.10** Invoice posting date in a closed AP period is rejected (`PER-AP-001`) even when the invoice date is open; the user may change posting date to the first open day with an audit note.
- **BR-18.11** Invoice vendor ≠ ordering vendor (factoring) is allowed when the ordering vendor's `invoice_account` points to the invoice vendor; the VendorTransaction belongs to the invoice vendor.
- **BR-18.12** Credit note quantity cannot exceed invoiced quantity on the referenced line; posting a credit note re-opens the GR/IR item and reverts inventory transaction status from PURCHASED to RECEIVED for that quantity.

### 3.18.8 Acceptance criteria

- **AC-18.1** (FR-18.5, FR-18.6) Given the 200-scenario matching fixture, when each invoice is evaluated and posted, then every match status, variance amount and ledger posting equals the expected row (0 mismatches).
- **AC-18.2** (FR-18.2, FR-18.8) Given 1,000 random PO lines received then fully invoiced at PO price, when GR/IR open items and the ledger balance are compared, then both are 0.00.
- **AC-18.3** (FR-18.1) Given a serial-tracked PO line of 3 units, when serials S1, S2, S2 are registered, then registration fails on the duplicate and records S1, S2 only after correction.
- **AC-18.4** (FR-18.8) Given AP period 2026-08 closed, when an invoice with posting date 2026-08-15 is posted, then it is rejected with `PER-AP-001` and no VendorTransaction or ledger entry exists.
- **AC-18.5** (FR-18.5) Given policy price_tol_pct 2 and price_tol_abs 25 and a PO price 100.00 USD, when an invoice line at 101.50 is matched, then status MATCHED with price variance 1.50 posted to PPV/inventory; at 103.00 status PRICE_VARIANCE and an exception workflow starts.
- **AC-18.6** (FR-18.7) Given a PRICE_VARIANCE exception, when the buyer accepts with reason "AGREED", then the invoice posts, the variance posts to the variance account, and the audit shows actor, reason and amount.
- **AC-18.7** (FR-18.10) Given an ERS vendor, when a receipt of 50 units at 10.00 EUR posts, then an invoice `ERS-{receipt}` of 500.00 EUR plus tax is posted within 10 s and a self-billing PDF is emailed.
- **AC-18.8** (FR-18.3) Given a posted receipt of 100 with 40 invoiced, when a correction of −70 is attempted, then it is rejected; −60 succeeds and GR/IR open item received_qty becomes 40.
- **AC-18.9** (FR-18.4) Given an invoice captured with confidence 0.6 on the total field, when opened, then the field is flagged and posting is blocked until confirmed.
- **AC-18.10** (FR-18.9) Given a manual journal posted to the GR/IR account, when the daily GR/IR reconciliation runs, then a diff event with the journal amount is emitted.

### 3.18.9 Test plan and fixtures

Fixtures: `match-200` (200 invoice scenarios CSV with expected status/postings, authored with the reviewing accountant; covers over/under-delivery, price up/down, multi-receipt, partial, currency mismatch, small difference, ERS, credit note); `grir-1000` (1,000 PO lines generated); `rcv-serial-batch` (tracked items); `ap-period-closed`; `ocr-samples` (25 invoice JSON payloads with confidence scores).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Matching engine | unit (table-driven) | matching:evaluate | match-200 all pass |
| Tolerance monotonicity | property | engine | increasing invoice price never moves status from variance to matched |
| GR/IR zero | E2E | receipt+invoice flow | AC-18.2 |
| Receipt → inventory | contract | inventory apply | RECEIVED qty and cost equal receipt |
| Invoice → ledger | contract | Post | balanced entry; accounts per posting profile |
| Period control | contract | invoices:post | AC-18.4 |
| ERS latency | E2E | receipt.posted consumer | < 10 s p95 |
| Receipt post latency | performance (k6, 200 receipts/min) | receipts:post | p95 < 1.5 s |
| PWA scan | E2E (Playwright) | receipt screen | GS1 barcode → line, batch, serial filled |

Reconciliation checks: Σ GrirOpenItem = GR/IR ledger balance; Σ VendorTransaction created in period = Σ invoice totals posted; Σ receipt amount_acct = Δ inventory account from receipts; invoiced_qty on receipt lines = Σ invoice line qty referencing them.

### 3.18.10 Deployment and operations

Helm charts: `passage-procurement` gains the receiving module (same Postgres, separate Deployment `receiving-api` ×2); `passage-payables` (API ×3, matching worker ×2, Temporal worker ×2, CloudNativePG `payables-db`, outbox connector). Config: `AP_SMALL_DIFF_DEFAULT=0.50`, `AP_PRICE_TOL_PCT=2.0`, `AP_QTY_TOL_PCT=5.0`, `AP_DUP_WINDOW_MONTHS=24`, `AP_EXCEPTION_SLA_DAYS=3`, `RCV_OVER_DELIVERY_PCT=0`. Migrations `procurement/v18.*`, `payables/v18.*`. Flags: `ap.ocr_adapter`, `ap.ers`, `ap.ubl_intake` (w35), `rcv.pwa`. Jobs: GR/IR reconciliation daily 02:30; exception SLA timers (Temporal); ERS consumer (Kafka consumer group `payables-ers`). SLOs: receipt post p95 < 1.5 s; invoice post p95 < 2 s; matching evaluate p95 < 100 ms; event lag < 5 s. Alerts: GR/IR diff ≠ 0 (page); exception backlog > 50 (ticket); ERS consumer lag > 100 messages. Runbook: re-run match for invoice set, reprocess failed capture payloads from DLQ topic `payables.capture.dlq`, write off GR/IR residual with approval. Rollback: additive migrations; disable `ap.ers` consumer; invoices in EXCEPTION are unaffected by rollback of the matching worker.

### 3.18.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX VendPackingSlipJour/Trans for open PO lines (received not invoiced) | ProductReceipt POSTED + GrirOpenItem | amount from VendPackingSlipTrans value; creates GR/IR opening balance matched to GL migration |
| AX VendInvoiceInfoTable (pending invoices) | VendorInvoice DRAFT | re-matched on load |
| AX VendInvoicePriceTolerance, InventTable tolerance fields, AP parameters | MatchingPolicy rows | scope mapping: item+vendor → VENDOR_ITEM etc. |
| AX VendTable self-billing flags | Vendor `ers_enabled` | |
| SAP EKBE VGABE 1 without matching VGABE 2 (GR not IR) | ProductReceipt + GrirOpenItem | WRX balance per PO item |
| SAP RBKP/RSEG parked (RBSTAT A/B) or blocked (ZLSPR) | VendorInvoice DRAFT / EXCEPTION | |
| SAP OMR6 tolerance keys (T169G) PP, DQ, BD, AN/AP | MatchingPolicy ENTITY scope | PP → price_tol; DQ → qty_tol; BD → small_diff; AN/AP → total_tol |
| SAP LFM1 XERSY | ers_enabled | |

Validation: GR/IR opening Σ equals GL migrated balance per legal entity; count of open receipt lines equals source; parked invoices re-matched produce the same block/no-block result as SAP for a 50-invoice sample. Not migrated: posted invoice history (becomes VendorTransaction open items in wave 19 migration; line detail stays in lakehouse), MRBR release logs, AX invoice register journals already approved (post before cutover).

### 3.18.12 Dependencies, risks, open questions

Depends on: wave 17 (PO lines, versions, charges), wave 16 (transaction steps, serial/batch), wave 11–14 (posting profiles, currency, tax, period control), wave 4 (Temporal workflows), wave 8 (GS1 parsing).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Accountant disagrees with GR/IR FX treatment (BR-18.7) | Medium | Medium | Confirm with accountant in match-200 review before posting logic is frozen |
| OCR adapter quality varies; low-confidence fields | High | Low | Confidence threshold gating; no auto-post below 0.95 |
| Multi-PO invoices | Medium | Low | Supported by line-level PO links; receipts stay one PO per document |

Open questions:
1. Auto-post invoices with 100 % match and confidence ≥ 0.95 without human review? Default: configurable per vendor, off by default. Decide by wave 19.
2. Should quantity tolerance also apply to under-invoicing close-out (auto-close residual GR/IR)? Default: no; manual write-off. Decide by wave 20.
3. Non-PO invoice approval: route by GL account owner or by amount only? Default: amount bands reuse of the wave 17 policy. Decide by wave 18 mid-point.

## 3.19 Wave 19: Accounts Payable, Settlement & Payments

### 3.19.1 Scope and business context

Wave 19 turns posted vendor invoices into paid liabilities. Invoices from wave 18 are open items (AX VendTrans with VendTransOpen; SAP BSIK) that settle against payments, credit notes and prepayments (AX VendSettlement / SpecTrans marking; SAP clearing to BSAK with BSE_CLR, F-53/F-44). A payment proposal (AX LedgerJournalTrans payment journal with VendPaymProposal; SAP F110 proposal run producing REGUH/REGUP) selects items by due date and cash discount, applies holds, approves, generates payment media (ISO 20022 pain.001 for SEPA/ACH-via-ISO, NACHA for US ACH, check print, positive pay file; SAP DMEE / PMW, AX payment formats via ER), posts the payment to the bank clearing account and vendor control account, and ingests bank status (pain.002) to update payment status. Prepayments, credit notes, reversals and vendor aging complete the AP subledger.

Out of scope: bank statement reconciliation and cash positioning (wave 36, which consumes this wave's payment events), bank connectivity transport (wave 36; this wave writes files to an object store path and exposes them for download), customer-side settlement (wave 30), 1099/withholding statutory reporting beyond withholding calculation (wave 13 provides calculation; reporting in wave 48), intercompany payments (wave 40).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX payment proposal (VendPaymProposal) runs as a modal dialog from the payment journal, locks the journal while running, and must be re-run from scratch to change selection; SAP F110 parameters → proposal → edit proposal → payment run → print is five screens, with proposal logs (REGUP exceptions) read in a separate list | Proposal is a transient batch producing journal lines; no proposal entity | `PaymentProposal` is a persisted entity with selection criteria, line-level include/exclude, holds, and re-selection diff; approve → run is one action | Screens per payment run: 5–7 → 2; time from "select" to "approved file": 45 min → < 5 min |
| Settlement in AX via "Settle open transactions" (CustVendOpenTransManager) silently computes discount and FX differences; users discover realized gain/loss or discount-lost postings only in the ledger; SAP F-53 shows the clearing document but FX difference accounts (OB09) are hidden until posted | No preview of settlement postings | `POST /settlements:preview` returns the exact journal (discount, FX gain/loss, write-off) before commit; UI shows it inline | Unexpected settlement postings found at month-end: typical 5–20/month → 0; review time per settlement: 2 min → 10 s |
| Payment formats: AX requires Electronic Reporting configurations per bank (hundreds of nodes), SAP DMEE trees; a bank-specific tag change takes a consultant day | Format defined in a proprietary tree editor | Canonical pain.001.001.09 generator with per-bank profile overrides as JSON (≤ 20 keys), validated against the XSD in CI and a bank harness | Time to onboard a new bank profile: 2–5 days → < 2 h; file rejections at bank: tracked, target < 0.1 % |
| Payment run idempotency: AX journal posting interrupted mid-batch leaves half-posted lines and a generated file; re-posting can pay twice; SAP F110 "restart" needs SM37 analysis | No durable workflow; file generation and posting are separate steps without a shared idempotent record | Temporal `PaymentRun` workflow with idempotency keys per open item; file is derived from posted payments, never the reverse | Duplicate payments from crash/retry: non-zero historical → 0 by test |
| Vendor aging in AX (VendAgingReport) is an SSRS batch over VendTrans; SAP S_ALR_87012078 runs minutes for large vendors; neither reconciles to the control account automatically | Report computed from open items with no tie to GL | Aging served from the `VendorTransaction` projection with bucket config; daily job asserts Σ open = AP control balance per vendor group | Aging report time: 2–10 min → < 2 s; monthly AP-to-GL reconciliation: 2 h → automated, 0 diff |

### 3.19.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity level |
|---|---|---|---|---|
| Vendor open items | VendTrans, VendTransOpen; Vendor transactions form; VendTable balance | BSIK (open), BSAK (cleared), FBL1N | `VendorTransaction` with `open_amount`; vendor transactions screen | full |
| Settlement | VendSettlement, CustVendOpenTransManager (Settle open transactions), marking (SpecTrans), partial settlement, cross-rate | Clearing F-44/F-53/F-51, residual/partial payment, BSE_CLR, AUGBL | `Settlement`, `SettlementLine`; manual, during payment, automatic | full |
| Cash discount | CashDisc (terms), VendTrans CashDiscDate, discount taken/lost accounts, "use cash discount" normal/always/never | Payment terms T052 ZBD1T/ZBD1P, SKTO/SKE accounts, OBXU/OBXV | `PaymentTerms` (wave 7) extended with discount tiers; settlement algorithm | full |
| FX on settlement | Exchange adjustment realized gain/loss (LedgerParameters, CurrencyExchangeAdjustment) | OB09 realized gain/loss accounts, KDF | realized gain/loss in settlement journal (wave 12 accounts) | full |
| Payment journal / proposal | LedgerJournalTable type Payment, LedgerJournalTrans, VendPaymProposal (by due date / cash discount / both), VendPaymModeTable (method of payment), payment specification, CustVendPaymJournal | F110 (FBZP config), REGUH/REGUP, payment methods T042Z, house banks FI12 | `PaymentProposal`, `PaymentMethod`, `Payment`, `PaymentRun` (Temporal) | full |
| Payment media | ER formats (ISO 20022 credit transfer, NACHA CCD/CTX, check layout), VendOutPaym classes | DMEE/PMW formats, F110 print program RFFOUS_T, check lot FCHI | `PaymentFile` generators: pain.001.001.09, NACHA (PPD/CCD/CTX), check PDF + MICR, positive pay CSV | full |
| Bank status ingestion | Return file import (ER pain.002), payment status | pain.002 via bank communication management (BCM) | `POST /payments/status:ingest` pain.002 parser | full |
| Positive pay | Positive pay format (BankPositivePayExport) | Not standard (custom/BCM) | positive pay CSV per bank profile | full |
| Prepayments | Prepayment invoice on PO (PurchPrepayTable), prepayment journal with posting profile | Down payment request F-47, down payment F-48 (special G/L A), clearing F-54 | `Prepayment` (PO-linked or free) with separate posting profile, applied at invoice posting or settlement | full |
| Credit notes and reversal | Negative invoice, reversal (TransactionReversal), void check | Credit memo MIRO/FB65, reverse FB08, void FCH8/FCH9 | wave 18 credit note; `Payment:reverse`, `Payment:void` | full |
| Vendor aging | Vendor aging report (VendAgingReport), aging period definitions | Aging S_ALR_87012078, due date analysis | `GET /aging` with bucket definitions; ties to control account | improved |
| Withholding tax at payment | Withholding tax (TaxWithhold*) calculated at payment | Extended withholding at payment (T059P) | wave 13 withholding engine invoked in settlement; posts withholding payable | full |
| Payment advice | Vendor payment advice (print management) | Payment advice RFFOAVIS | email/portal advice PDF with settled invoices | full |

### 3.19.3 Functional requirements

- **FR-19.1 Vendor open items** — Must. The system shall maintain a `VendorTransaction` for every posted invoice, credit note, payment and prepayment with `amount_txn`, `amount_acct`, `open_amount_txn`, `open_amount_acct`, `due_date`, `discount_date`, `discount_amount`, `status` (OPEN, PARTIALLY_SETTLED, SETTLED), and a `hold` flag with reason.
  - Open amount is updated only by settlement rows; `Σ open_amount_acct` per posting profile account equals the AP control balance (daily assertion).
  - Vendor balance query p95 < 50 ms for vendors with ≤ 100k transactions.

- **FR-19.2 Settlement algorithm** — Must. The system shall settle a set of credit-side items (payment, credit note, prepayment) against debit-side items (invoices) per vendor with the following algorithm, previewable before commit:
  1. Order target invoices by `settlement_priority` (user marks first, then due_date ASC, then amount DESC).
  2. For each invoice, `discount = round(invoice_amount_txn × discount_pct, minor)` if `settlement_date ≤ discount_date` (or terms are `ALWAYS`), else 0; for partial settlement, discount is pro-rated by `settled/invoice_amount` unless policy `discount_on_partial=false`.
  3. `applied_txn = min(available_credit, open_amount_txn − discount)`; invoice open amount reduces by `applied_txn + discount` (discount closes the item only when `applied_txn = open − discount`).
  4. Accounting amounts: `applied_acct_invoice = applied_txn × invoice_rate`, `applied_acct_credit = applied_txn × credit_rate`; `fx_diff = applied_acct_credit − applied_acct_invoice`; positive = realized loss for a vendor payable, negative = realized gain (wave 12 accounts).
  5. If a residual `|open_amount_txn| ≤ write_off_limit` (default 1.00 in transaction currency) and `auto_write_off=true`, post the residual to the write-off account and close the item.
  6. Cross-currency settlement (credit currency ≠ invoice currency) converts both to accounting currency at their own rates; the settlement unit is accounting currency; residuals follow step 5.
  7. Withholding (wave 13) is computed on `applied_txn` and reduces the payment, posting withholding payable.
  - Worked example: invoice 1,000.00 EUR posted at 1.1000 USD/EUR (1,100.00 USD), terms 2 % 10 net 30, paid on day 8 at rate 1.0800. Discount = 20.00 EUR. Applied = 980.00 EUR. Invoice-side acct = 980 × 1.10 = 1,078.00 USD; discount acct = 20 × 1.10 = 22.00; payment acct = 980 × 1.08 = 1,058.40. Journal: Dr AP control 1,100.00; Cr bank clearing 1,058.40; Cr discount taken 22.00; Cr realized FX gain 19.60. Balanced to the cent.

- **FR-19.3 Payment terms and discounts** — Must. The system shall compute `due_date` and up to two discount tiers (e.g., 2/10, 1/20, net 30) from the wave 7 payment terms, with `discount_basis` (gross, net of tax) and `use_cash_discount` (NORMAL, ALWAYS, NEVER) per vendor.

- **FR-19.4 Payment proposal** — Must. The system shall create a `PaymentProposal` from criteria (legal entity, vendor range/group, method of payment, due date to, discount date to, selection rule BY_DUE_DATE / BY_DISCOUNT / BOTH, minimum/maximum payment, currency, bank account), listing candidate open items with proposed amount and discount, excluding held vendors/items, and allowing line-level include/exclude, amount change and reselection with diff.
  - Discount optimization: when `BY_DISCOUNT`, items whose discount date is within `discount_lookahead_days` (default 7) are included; annualized discount value vs `cost_of_capital_pct` (default 8 %) decides early payment when `optimize=true`.
  - Proposal approval uses the wave 4 workflow with amount bands (default: > 100,000 USD two approvers).

- **FR-19.5 Payment run** — Must. The system shall execute an approved proposal as a Temporal `PaymentRun` workflow: for each vendor group create `Payment` records (one per vendor × currency × bank account × method), settle items (FR-19.2), post the payment journal (Dr AP control, Cr bank clearing per method), generate the `PaymentFile`, and mark payments SENT.
  - Each activity is idempotent on `(proposal_id, vendor_transaction_id)`; a resumed run skips already-settled items and already-generated files; the file is regenerated from posted payments if missing.
  - Throughput target: 10,000 open items settled and 2,000 payments generated in < 5 minutes.

- **FR-19.6 ISO 20022 pain.001** — Must. The system shall generate pain.001.001.09 files validated against the XSD with the following mapping:
  - `GrpHdr/MsgId` = `PaymentFile.file_number` (≤ 35 chars); `CreDtTm` = generation timestamp; `NbOfTxs` = count of CdtTrfTxInf; `CtrlSum` = Σ InstdAmt; `InitgPty/Nm` = legal entity name; `InitgPty/Id/OrgId/Othr/Id` = bank-assigned customer id from bank profile.
  - `PmtInf` per (debtor account × execution date × method): `PmtInfId` = `{file_number}-{seq}`; `PmtMtd` = `TRF`; `BtchBookg` = profile `batch_booking` (default true); `PmtTpInf/SvcLvl/Cd` = `SEPA` for SEPA profiles else `NURG`/`URGP`; `ReqdExctnDt/Dt` = payment date; `Dbtr/Nm`, `Dbtr/PstlAdr` = legal entity; `DbtrAcct/Id/IBAN` (or `Othr/Id` for non-IBAN) = bank account; `DbtrAgt/FinInstnId/BICFI` = bank BIC; `ChrgBr` = `SLEV` (SEPA) or profile default `SHAR`.
  - `CdtTrfTxInf` per Payment: `PmtId/InstrId` = payment number; `PmtId/EndToEndId` = payment number (≤ 35 chars, alphanumeric); `Amt/InstdAmt @Ccy` = amount and currency; `CdtrAgt/FinInstnId/BICFI` = vendor bank BIC (omitted when IBAN-only SEPA and profile allows); `Cdtr/Nm` = vendor name (≤ 140 chars, transliterated to the Latin character set required by the bank profile); `Cdtr/PstlAdr/Ctry` plus structured address when profile requires; `CdtrAcct/Id/IBAN` or `Othr/Id` + `CdtrAgt/.../ClrSysMmbId` for routing-number countries; `RmtInf/Ustrd` = concatenated invoice numbers (≤ 140 chars) or `RmtInf/Strd/RfrdDocInf/Nb` + `RfrdDocAmt` per settled invoice when `structured_remittance=true`; `Purp/Cd` = `SUPP`.
  - AX/SAP lineage: REGUH ZBNKN/ZBNKL → CdtrAcct, REGUH VBLNR → EndToEndId, REGUP BELNR → RfrdDocInf/Nb; AX LedgerJournalTrans PaymReference → EndToEndId.

- **FR-19.7 NACHA, checks, positive pay** — Must. The system shall generate NACHA files (file header 1, batch header 5 with SEC code PPD/CCD/CTX, entry detail 6, addenda 7 for CTX remittance, batch control 8, file control 9, blocking factor 10, 94-char records, hash of routing numbers), check PDFs with MICR line and check number from a check lot, and positive pay CSV (bank account, check number, amount, payee, issue date, void flag) per bank profile.

- **FR-19.8 Payment status and pain.002** — Must. The system shall ingest pain.002.001.10 status reports and map `GrpSts`/`TxSts` (ACCP, ACSC, ACSP, PDNG, RJCT) with `StsRsnInf/Rsn/Cd` (AC01, AC04, AM04, …) to payment statuses, moving RJCT payments to REJECTED, reversing settlement and re-opening items; NACHA returns (R01–R85) map likewise.

- **FR-19.9 Payment reversal and void** — Must. The system shall reverse a posted payment (new journal, settlement undone, items re-opened, file not regenerated) and void a check (check status VOID, positive pay void record), with reason codes and audit.

- **FR-19.10 Prepayments** — Must. The system shall post prepayment invoices (PO-linked, with prepayment posting profile and tax) and prepayment journals; at vendor invoice posting or manual settlement the prepayment applies against the invoice, reclassifying from the prepayment account to AP control.

- **FR-19.11 Vendor aging** — Must. The system shall produce aging by vendor/group with configurable buckets (default 0–30, 31–60, 61–90, 91–180, > 180 by due date or document date), as of any date, in transaction and accounting currency; totals tie to the AP control account per vendor group (daily job + report footer).

- **FR-19.12 Payment advice** — Should. The system shall email (or publish to the portal, wave 20) a payment advice PDF per payment listing settled invoices, discounts and withholding.

- **FR-19.13 Holds** — Should. The system shall support vendor-level holds (INVOICE, PAYMENT, ALL, from wave 7 status) and item-level holds with release date; held items are excluded from proposals and listed on the AP workspace.

- **FR-19.14 Automatic settlement** — Could. The system shall optionally settle credit notes against the oldest open invoices of the same vendor at posting (`auto_settle_credit_notes`).

### 3.19.4 Data model

**VendorTransaction**

| Field | Type | Required | Notes |
|---|---|---|---|
| id, voucher | uuid, text | yes | ledger entry voucher |
| vendor_id, legal_entity_id | uuid | yes | |
| type | enum | yes | INVOICE, CREDIT_NOTE, PAYMENT, PREPAYMENT, PREPAYMENT_INVOICE, WRITE_OFF, FX_ADJ |
| source_id | uuid | yes | invoice/payment id |
| document_date, posting_date, due_date, discount_date, discount_date_2 | date | yes/no | |
| currency, exchange_rate | text, numeric(18,8) | yes | |
| amount_txn, amount_acct | numeric(19,4) | yes | signed (invoice negative = credit to vendor) |
| open_amount_txn, open_amount_acct | numeric(19,4) | yes | |
| discount_amount, discount_amount_2 | numeric(19,4) | no | |
| status | enum | yes | OPEN, PARTIALLY_SETTLED, SETTLED |
| hold_reason, hold_release_date | text, date | no | |
| posting_profile_id, control_account_id | uuid | yes | |
| payment_method_id, bank_account_id (vendor) | uuid | no | default from vendor |

**Settlement / SettlementLine**

| Field | Type | Required | Notes |
|---|---|---|---|
| id, settlement_number | uuid, text | yes | |
| vendor_id, settlement_date | uuid, date | yes | |
| kind | enum | yes | MANUAL, PAYMENT_RUN, AUTO, REVERSAL |
| ledger_entry_id | uuid | yes | discount/FX/write-off/withholding postings |
| reversed_by_id | uuid | no | |
| line: credit_transaction_id, debit_transaction_id, applied_txn, applied_acct_debit, applied_acct_credit, discount_txn, discount_acct, fx_diff_acct, write_off_txn, withholding_txn, closes_debit (bool), closes_credit (bool) | | | |

**PaymentProposal / Payment / PaymentFile**

| Field | Type | Required | Notes |
|---|---|---|---|
| proposal: id, number, criteria (jsonb), status (DRAFT, SELECTED, IN_APPROVAL, APPROVED, RUNNING, COMPLETED, CANCELLED), payment_date, bank_account_id, method_id, total_by_currency (jsonb), approved_by[] | | | |
| proposal_line: proposal_id, vendor_transaction_id, proposed_txn, discount_txn, included (bool), hold (bool), exclusion_reason | | | |
| payment: id, payment_number, proposal_id, vendor_id, vendor_bank_account_id, method_id, currency, amount_txn, amount_acct, payment_date, status (CREATED, POSTED, SENT, ACCEPTED, SETTLED_AT_BANK, REJECTED, REVERSED, VOID), end_to_end_id, check_number, settlement_id, ledger_entry_id, file_id, status_reason_code | | | |
| file: id, file_number, format (PAIN001, NACHA, CHECK_PDF, POSITIVE_PAY), bank_profile_id, generated_at, payment_count, control_sum, sha256, storage_uri, status (GENERATED, DOWNLOADED, TRANSMITTED, ACKNOWLEDGED, REJECTED) | | | |

**PaymentMethod / BankProfile**: method (id, code, type TRANSFER/ACH/CHECK/CARD/WIRE, format, bank_account_id, clearing_account_id, check_lot_id, grace_days, cutoff_time); bank profile (id, bank, format variant SEPA/CH/UK/US, overrides jsonb ≤ 20 keys: charge bearer, batch booking, BIC required, structured remittance, char set, max Ustrd length).

Invariants: for every settlement line `applied_acct_credit − applied_acct_debit − fx_diff_acct = 0`; `Σ settlement lines applied on a transaction ≤ |amount|`; transaction SETTLED ⇔ open_amount = 0; `PaymentFile.control_sum = Σ payments in file`; one Payment per (proposal, vendor, currency, bank account, method). Indexes: VendorTransaction (tenant, vendor_id, status, due_date), (control_account_id, status); payments (proposal_id), (end_to_end_id) unique; settlements by transaction ids.

### 3.19.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| GET | /api/v1/payables/vendors/{id}/transactions?status=open | Open items | n/a | APInquire |
| POST | /api/v1/payables/settlements:preview | Compute settlement journal | n/a | APSettle |
| POST | /api/v1/payables/settlements | Commit manual settlement | client key | APSettle |
| POST | /api/v1/payables/settlements/{id}:reverse | Reverse | settlement id | APSettle |
| POST | /api/v1/payables/payment-proposals | Create and select | client key | PaymentProposalMaintain |
| POST | /api/v1/payables/payment-proposals/{id}:reselect | Re-run selection with diff | proposal id + criteria hash | PaymentProposalMaintain |
| PATCH | /api/v1/payables/payment-proposals/{id}/lines/{lineId} | Include/exclude/amount | ETag | PaymentProposalMaintain |
| POST | /api/v1/payables/payment-proposals/{id}:submit | Start approval | proposal id | PaymentProposalMaintain |
| POST | /api/v1/payables/payment-proposals/{id}:run | Execute PaymentRun | proposal id | PaymentRunExecute |
| GET | /api/v1/payables/payment-files/{id}/content | Download file | n/a | PaymentFileDownload |
| POST | /api/v1/payables/payments/status:ingest | pain.002 / NACHA return intake | message id | service account / PaymentStatusIngest |
| POST | /api/v1/payables/payments/{id}:reverse | Reverse payment | payment id | APSettle |
| POST | /api/v1/payables/payments/{id}:void | Void check | payment id | APSettle |
| POST | /api/v1/payables/prepayments | Prepayment invoice/journal | client key | VendorInvoicePost |
| GET | /api/v1/payables/aging?asOf=&by=due|document&buckets= | Aging | n/a | APInquire |

Commands: `CommitSettlement` (same vendor, no held items, rates available, AP period open, Σ applied ≤ available credit), `RunPayment` (proposal APPROVED, bank account active, cutoff time, vendor bank account validated — IBAN mod-97, ABA checksum), `IngestStatus` (file matches known MsgId/OrgnlEndToEndId; unknown ids → DLQ).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.payables.settlement.posted.v1` / `.reversed.v1` | settlement commit/reverse | settlement_id, vendor_id, lines (ids, applied, discount, fx_diff), ledger_entry_id | ledger reconciliation, analytics |
| `dev.passage.payables.proposal.approved.v1` | approval complete | proposal_id, totals by currency, payment_date | notifications |
| `dev.passage.payables.payment.posted.v1` | payment journal posted | payment_id, vendor, amount, method, bank account, settled items | cash (w36), analytics |
| `dev.passage.payables.payment.sent.v1` | file generated & marked sent | payment_id, file_id, format, end_to_end_id | bank connectivity (w36), supplier-portal (w20) |
| `dev.passage.payables.payment.status.changed.v1` | pain.002/NACHA return | payment_id, old_status, new_status, reason_code | notifications, supplier-portal |
| `dev.passage.payables.aging.reconciled.v1` | daily job | as_of, vendor_group, open_total, control_balance, diff | alerting |

### 3.19.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| AP workspace | Due this week, discounts expiring, held items, proposals awaiting approval, rejected payments | counts, amounts by currency | APInquire |
| Vendor transactions | Filter open/all, settle (opens preview), hold/release, view voucher | document, dates, amounts, open, discount available, status | APInquire / APSettle |
| Settlement dialog | Mark items, set date, preview journal, commit | credit/debit items, applied, discount, FX diff, write-off, resulting journal | APSettle |
| Payment proposal | Criteria form, selection grid, include/exclude, reselect diff, submit, run | vendor, item, due, discount, proposed amount, hold reason | PaymentProposalMaintain |
| Payment run monitor | Progress, failures, retry, download files, advices | payments by status, file list, control sums | PaymentRunExecute |
| Payment files / bank profiles | Download, mark transmitted, configure profile overrides, test generate | format, bank, count, control sum, status | PaymentFileDownload / BankProfileMaintain |
| Aging | Bucket view, drill to items, export | vendor group, buckets, total, control balance, diff | APInquire |

Interaction rules: settlement preview updates within 300 ms of marking an item; proposal grid supports multi-select include/exclude and keyboard navigation; amounts always shown with currency code; approvals from shell inbox; bank profile editor validates with a sample file generation and XSD check before save; personalization of buckets and columns.

### 3.19.7 Business rules and edge cases

- **BR-19.1** Payment state machine: `CREATED → POSTED → SENT → ACCEPTED → SETTLED_AT_BANK`; `SENT|ACCEPTED → REJECTED` (settlement reversed, items re-opened, payment stays for audit); `POSTED|SENT → REVERSED` (manual); `SENT (check) → VOID`. Proposal: `DRAFT → SELECTED → IN_APPROVAL → APPROVED → RUNNING → COMPLETED`; `IN_APPROVAL → SELECTED` on rejection; `DRAFT|SELECTED → CANCELLED`.
- **BR-19.2** Discount eligibility uses `settlement_date` (payment date), not run date; a proposal with payment_date ≤ discount_date takes the discount even if generated after it; `use_cash_discount=ALWAYS` takes it regardless of date, `NEVER` never.
- **BR-19.3** Partial payment discount: default `discount_on_partial=true` pro-rata; residual item keeps the remaining discount entitlement with the same discount date.
- **BR-19.4** Rounding: discount and FX amounts rounded half-even to the currency minor unit; the FX difference is computed as the balancing figure so the settlement journal always balances by construction; any rounding residual ≤ 0.01 in accounting currency posts to the rounding account (wave 12).
- **BR-19.5** Cross-currency: when credit and debit currencies differ, `applied` is expressed in accounting currency; the transaction-currency open amounts are reduced by `applied_acct / own_rate`; residual below `write_off_limit` closes the item.
- **BR-19.6** Settlement in a closed AP period is rejected; reversal posts in the current open period with a reversal voucher referencing the original.
- **BR-19.7** Concurrency: settlement locks VendorTransaction rows in ascending id order; a payment run and a manual settlement on the same item serialize; the loser sees `AP-SET-009 item no longer open` and the proposal line is marked excluded with reason.
- **BR-19.8** Idempotency: `PaymentRun` activities key on `(proposal_id, vendor_transaction_id)`; the file activity keys on `(proposal_id, bank_account_id, method_id, payment_date)` and is deterministic (same payments → same bytes except `CreDtTm`), so a regenerated file is byte-identical in content fields and carries the same `MsgId`.
- **BR-19.9** `EndToEndId` and `MsgId` are restricted to `[A-Za-z0-9/-?:().,'+ ]` ≤ 35 chars; vendor names are transliterated per bank profile (e.g., `ß → ss`); failure to transliterate rejects the payment line before file generation.
- **BR-19.10** Minimum payment amount (default 1.00) and negative vendor balance: a vendor whose included items net to ≤ 0 is excluded from the proposal with reason `NET_CREDIT`; credit notes are settled against invoices inside the proposal before amounts are computed.
- **BR-19.11** Prepayment applied at invoice posting cannot exceed the invoice total; the remainder stays open as a prepayment transaction.
- **BR-19.12** Rejected payment (pain.002 RJCT AC01 invalid account) re-opens items with a `hold_reason=BANK_DETAILS` on the vendor's bank account until corrected.

### 3.19.8 Acceptance criteria

- **AC-19.1** (FR-19.2) Given the 300-scenario settlement fixture, when each scenario is previewed and committed, then discount, FX gain/loss, write-off and withholding amounts and accounts equal expected values for all 300, and every settlement journal balances.
- **AC-19.2** (FR-19.2) Given the worked example in FR-19.2, when settled, then the journal lines are exactly Dr AP 1,100.00; Cr bank clearing 1,058.40; Cr discount 22.00; Cr FX gain 19.60.
- **AC-19.3** (FR-19.6) Given a proposal of 50 SEPA payments, when the run completes, then the pain.001.001.09 file validates against the XSD, `CtrlSum` equals Σ amounts, and the bank test harness returns ACCP for all 50.
- **AC-19.4** (FR-19.5) Given a run of 2,000 payments killed after 1,200 payments are posted, when the workflow resumes, then exactly 2,000 payments exist, no item is settled twice, and the file contains 2,000 transactions.
- **AC-19.5** (FR-19.11) Given open items across 3 vendor groups, when aging is run as of today, then Σ buckets per group equals the AP control account balance per posting profile and the daily job emits diff = 0.
- **AC-19.6** (FR-19.4) Given an invoice with 2 % discount expiring in 5 days and cost of capital 8 %, when a BY_DISCOUNT proposal with optimize=true runs, then the item is included with the discount (annualized 2 % × 365/20 ≈ 36.5 % > 8 %).
- **AC-19.7** (FR-19.8) Given a SENT payment, when a pain.002 with TxSts RJCT and Rsn AC01 is ingested, then the payment is REJECTED, its settlement reversed, the invoice re-opened with hold BANK_DETAILS, and `payment.status.changed` emitted.
- **AC-19.8** (FR-19.7) Given 10 US vendors paid by ACH CCD, when the NACHA file is generated, then record counts, block count (multiple of 10), entry hash and total debit/credit match the file control record and a NACHA validator passes.
- **AC-19.9** (FR-19.10) Given a PO prepayment of 300.00 USD and a later invoice of 1,000.00 USD, when the invoice posts, then 300.00 is applied, AP open = 700.00, and the prepayment account is reduced by 300.00.
- **AC-19.10** (FR-19.9) Given a posted check payment, when voided, then the check status is VOID, the positive pay file contains a void record, items are re-opened, and the payment journal is reversed.

### 3.19.9 Test plan and fixtures

Fixtures: `ap-settle-300` (300 scenarios authored with the accountant: full/partial, discount taken/lost/always, cross-currency, write-off, withholding, prepayment, credit note netting); `ap-pain001-bank-profiles` (SEPA, UK, CH, US-ISO) with expected XML; `ap-nacha-10`; `ap-aging-demo` (3 vendor groups, 5,000 items); `ap-pain002-samples` (ACCP, RJCT codes); `ap-crash-run` (2,000 payments with injected failure).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Settlement scenarios | unit (table-driven) | settlement engine | AC-19.1 |
| Settlement balances | property | engine | random items/rates/dates: journal always balances; Σ applied ≤ credit |
| pain.001 XSD | contract | file generator | valid against `pain.001.001.09.xsd` for all profiles |
| Bank harness | E2E | sandbox bank | ACCP for fixture file |
| NACHA validator | unit | NACHA generator | AC-19.8 |
| Run idempotency | E2E (Temporal test server with activity failure injection) | PaymentRun | AC-19.4 |
| Aging ties | E2E | aging job | AC-19.5 |
| Status ingest | unit | pain.002 parser | all codes map; unknown → DLQ |
| Proposal throughput | performance | 10k items | < 5 min run, p95 settle activity < 50 ms |

Reconciliation checks: Σ VendorTransaction.open_amount_acct per control account = ledger balance; Σ payments POSTED per bank clearing account = ledger clearing balance; Σ file control sums = Σ payments SENT; settlement discount Σ = discount account movement for the period.

### 3.19.10 Deployment and operations

Helm chart `passage-payables` extended: payment-run Temporal worker ×2, file generator worker ×2, object store bucket `passage-payment-files` (S3-compatible, server-side encryption, 7-year retention, write-once). Config: `AP_WRITE_OFF_LIMIT=1.00`, `AP_DISCOUNT_LOOKAHEAD_DAYS=7`, `AP_COST_OF_CAPITAL_PCT=8`, `AP_MIN_PAYMENT=1.00`, `AP_FILE_RETENTION_DAYS=2555`. Migrations `payables/v19.*`. Flags: `ap.nacha`, `ap.checks`, `ap.positive_pay`, `ap.auto_settle_credit_notes`, `ap.structured_remittance`. Jobs: aging reconciliation daily 03:00; discount-expiry digest daily 07:00; pain.002 poller (wave 36 replaces with bank connectivity). SLOs: settlement preview p95 < 300 ms; proposal selection of 10k items < 10 s; file generation < 30 s for 2,000 payments. Alerts: aging diff ≠ 0 (page); payment run failed activity > 3 retries (page); files generated but not downloaded/transmitted within 4 h (ticket); rejected payments > 1 % of a run (ticket). Runbook: resume stuck run (Temporal), regenerate file, reverse payment, correct vendor bank account and re-propose. Rollback: additive migrations; payment files already transmitted are not affected; disable `ap.*` format flags to stop generation.

### 3.19.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX VendTrans + VendTransOpen (open items) | VendorTransaction OPEN/PARTIALLY_SETTLED | AmountCur/AmountMST → amount_txn/acct; VendTransOpen.AmountCur → open; CashDiscDate, DueDate; vendor balance Σ must equal migrated AP control |
| AX VendSettlement history | not migrated | closed history in lakehouse |
| AX VendPaymModeTable, VendPaymModeSpec | PaymentMethod | ER format name → format enum |
| AX BankAccountTable (vendor bank accounts, VendBankAccount) | vendor bank accounts (wave 7 role data) | IBAN/BIC/ABA validated on load |
| AX CashDisc, PaymTerm | PaymentTerms discount tiers | |
| AX LedgerJournalTrans open payment journals | not migrated | post or delete before cutover |
| SAP BSIK (open vendor items) | VendorTransaction | DMBTR/WRBTR, ZFBDT+ZBD1T → due/discount dates, ZLSPR → hold; UMSKZ A → PREPAYMENT |
| SAP T042Z/T042E (payment methods), FBZP house banks | PaymentMethod, bank accounts | |
| SAP LFBK (vendor bank details) | vendor bank accounts | |
| SAP REGUH/REGUP of unexecuted F110 proposals | not migrated | complete or delete before cutover |
| SAP T052 payment terms | PaymentTerms | |

Validation: Σ open items per vendor and per control account equals the source trial balance at cutover (0.00 difference); count of open items equals source; 100 random vendors' bank details re-validated. Not migrated: cleared items (BSAK, settled VendTrans), check registers (void history), positive pay history, payment files.

### 3.19.12 Dependencies, risks, open questions

Depends on: wave 18 (VendorTransaction creation), wave 12 (rates, realized gain/loss accounts, rounding), wave 13 (withholding), wave 14 (AP period control), wave 7 (vendor bank accounts, terms, holds), wave 4 (Temporal, approvals), wave 36 for bank transport (forward dependency; this wave uses file download).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Bank-specific pain.001 variants exceed the 20-key profile model | Medium | Medium | Profile model reviewed against 4 banks' implementation guides; escape hatch: XSLT post-processor per profile |
| Accountant rejects discount-on-partial default | Low | Low | Policy flag; fixture covers both |
| Payment file leakage (contains bank details) | Low | High | Object store with per-tenant prefix, OPA-gated download, audit on every download, no email attachment of files |

Open questions:
1. pain.001 version: .09 (2019) as default or .03 for banks still on the older schema? Default: .09 with profile override to .03. Decide by wave 19 mid-point.
2. Should the proposal auto-net credit notes across currencies? Default: same currency only. Decide by wave 19 close.
3. Card payments (virtual cards) in v1? Default: method type exists, no provider adapter until v1.x. Decide by wave 35.

## 3.20 Wave 20: Inventory Costing & Supplier Collaboration

### 3.20.1 Scope and business context

Wave 20 makes inventory value correct. Waves 16–18 wrote `cost_amount_physical` and `cost_amount_posted` on every inventory transaction at the running estimate (receipt price, running average on issues). This wave adds the costing engine that settles issues against receipts under the item model group's method — FIFO, LIFO, weighted average (running or periodic) or standard cost — and the inventory close/recalculation job (AX InventClosing with InventSettlement and InventCostTrans; SAP material ledger period-end closing CKMLCP with CKMLPP/CKMLCR and MBEW price control S/V, MR21 price change, MR22 debit/credit) that posts `cost_amount_adjustment` rows and GL adjustments so that the inventory value report ties to the GL inventory accounts by site, warehouse, item group and financial dimension. Standard cost captures purchase price variance (PPV) on receipt and invoice. Costing versions (AX CostingVersion/InventItemPrice; SAP CK11N/CK24 marking and release for material-level prices) hold planned and standard costs by product × site with activation and revaluation of on-hand.

The supplier portal (D365 vendor collaboration; SAP Ariba Supplier Network / SNC) at `supplier.<env>.passage.dev` lets vendor users acknowledge POs (accept/reject/propose changes), send ASNs, reply to RFQs (wave 17 hook), submit invoices against POs (wave 18 `:capture` source PORTAL), view payment status and advices (wave 19), and exchange documents.

Out of scope: BOM cost roll-up, costing sheets and production variance decomposition (wave 39), production order WIP settlement (wave 21 uses this engine for its receipts/issues), landed cost (wave 34), cost accounting ledger (wave 38), portal e-invoicing via Peppol (wave 35), supplier onboarding/qualification workflow (v1.x).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX inventory close (InventClosing) takes 4–12 h for large companies, blocks posting in the period, fails on unsettled negative issues with cryptic "cannot settle" messages, and must be cancelled and re-run end to end | Single-threaded settlement per item in X++ with helper threads; whole-period scope; no incremental | Close runs as a Temporal workflow with per-product child workflows (parallelism 32), incremental from the last closed date, resumable, with a pre-check report listing items that cannot settle and why | Close duration on 1M transactions: 4–12 h → < 20 min; re-run after failure: full → incremental from checkpoint |
| Inventory value does not tie to the GL: AX "Inventory value report" (InventValueReport) requires report configuration (InventValueReportSetup) and still differs from the GL because of manual journals and timing; SAP MB5L vs FAGLB03 differences investigated monthly | Value report computed from InventTrans; GL has postings from other sources; no per-dimension open-item relationship | Value report is a projection from `CostTransaction` rows with the GL voucher on each; daily reconciliation per inventory account × dimension; every difference row drills to the causing voucher | Monthly inventory-to-GL reconciliation: 1–3 days → < 1 h; unexplained differences: non-zero → 0 |
| Weighted average in AX during the period uses running average per receipt, then close recalculates to periodic average, changing COGS retroactively with surprises at close; SAP MAP (VERPR) can go negative or spike on small stock with large invoice differences | Two-phase (running vs periodic) design; MAP recalculated on invoice with stock near zero | Method choice `WEIGHTED_AVG_RUNNING` (what you see during the period is final) or `WEIGHTED_AVG_PERIODIC` (explicit), with a preview of close adjustments by product before posting; guard that invoice differences on zero stock post to price difference, not to a residual unit | Unexpected COGS adjustments at close: typical 1–3 % of COGS → < 0.1 %; MAP spike incidents: tracked, target 0 |
| Standard cost activation in AX requires costing version setup, pending prices, "Activate pending prices", then a revaluation posting that is hard to reconcile; SAP CK24 marking/release with CKM3 analysis is a multi-transaction month-end routine | Multi-step manual procedure | `CostingVersion` with pending/active state; `:activate` posts the revaluation of on-hand in one voucher and shows the per-site delta before activation | Steps to activate standard costs for a plant: 6–9 transactions → 2 (review, activate); activation latency: hours → < 2 min for 10k products |
| Vendors call or email for PO status, ASN entry is EDI-only or re-keyed by the warehouse, invoices arrive by email PDF; D365 vendor collaboration needs Azure AD B2B guest setup per vendor user; Ariba requires a separate subscription | Portal is an add-on with separate identity | Portal built on the same APIs/events with its own Keycloak realm; vendor admin invites users self-service; ASN pre-fills receiving (wave 18) | Vendor onboarding to portal: days → < 15 min; PO acknowledgement latency: 2–5 days → < 1 day median; receipts pre-filled from ASN: 0 % → > 80 % of portal vendors' receipts |

### 3.20.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity level |
|---|---|---|---|---|
| Costing method per item | InventModelGroup.InventModel (FIFO, LIFO, LIFO date, Weighted avg, Weighted avg date, Standard cost, Moving average) | MBEW VPRSV (S standard / V moving average), material ledger actual costing for periodic | `ItemModelGroup.costing_method` (FIFO, LIFO, LIFO_DATE, WEIGHTED_AVG_RUNNING, WEIGHTED_AVG_PERIODIC, STANDARD, MOVING_AVG) | full |
| Inventory close | InventClosing, InventSettlement, InventCostTrans, InventClosingRecalculation; Close and adjustment form | CKMLCP (material ledger cockpit: selection, determine sequence, single/multi-level price determination, post closing), CKMLPP/CKMLCR | `InventoryClose` workflow, `CostSettlement`, `CostTransaction` | full |
| Recalculation | Recalculation (without closing), cancel recalculation | Material ledger price determination preview; CKM3 analysis | `:recalculate` (preview/post without closing period) | full |
| Adjustment | Adjustment of on-hand / transactions (InventAdjOnhand, InventAdjTrans) | MR22 debit/credit material, MR21 price change | `CostAdjustment` (on-hand or transaction level) | full |
| Standard cost / costing versions | CostingVersion, InventItemPrice (pending/active), activation, revaluation posting | Standard price STPRS, CK11N estimate, CK24 mark/release, price change document MR21, revaluation | `CostingVersion`, `ItemCost` (PENDING/ACTIVE), `:activate` with revaluation voucher | full (roll-up in w39) |
| Purchase price variance | Standard cost variance posting types PurchPriceVariance at receipt/invoice | PRD price difference account (OBYC PRD) on GR/IR | PPV posting at receipt and invoice (wave 18 hooks) with `CostTransaction` kind PPV | full |
| Inventory value report | InventValueReport, InventValueReportSetup, Inventory aging | MB5L (stock value), MC.9 / MB52, CKM3 | `GET /costing/value-report` with dimension grouping and GL tie-out column | improved |
| Marking | InventTrans marking (MarkingRefInventTransOrigin) | Not standard (batch-specific valuation partial) | `marked_transaction_id` on transactions (wave 16 field) honored by settlement | full |
| Moving average | Moving average (InventModel MovingAverage) with product cost adjustment at invoice | MAP VERPR update on GR/IR | `MOVING_AVG` with invoice-difference settlement to stock proportionally | full |
| Vendor collaboration | D365 Vendor collaboration workspace (PO confirmation, consignment, invoices, RFQ replies) | Ariba Supplier Network, SNC/Supplier Portal | Supplier portal (PO ack, ASN, invoice, RFQ reply, payments, documents) | full |
| ASN | D365 vendor collaboration ASN; AIF ASN inbound | ASN via EDI DESADV, SNC | `AdvanceShipNotice` → receiving expected lines | full |

### 3.20.3 Functional requirements

- **FR-20.1 Costing methods** — Must. The system shall value issues against receipts by the item model group's method:
  - **FIFO**: issues settle against the oldest unsettled receipts by `financial_date`, then `physical_date`, then id. Example: receipts 100 @ 10.00 (Jan 5), 100 @ 12.00 (Jan 20); issue 150 on Jan 25 → cost = 100 × 10.00 + 50 × 12.00 = 1,600.00; remaining on-hand 50 @ 12.00 = 600.00.
  - **LIFO**: issues settle against the newest receipts preceding the issue date (LIFO_DATE) or the newest in the period (LIFO). Same data, LIFO_DATE: 150 issue = 100 × 12.00 + 50 × 10.00 = 1,700.00; remaining 50 @ 10.00 = 500.00.
  - **WEIGHTED_AVG_PERIODIC**: all issues in the period settle at `(opening value + Σ receipts value) / (opening qty + Σ receipts qty)`. Same data: average = 2,200 / 200 = 11.00; issue cost 1,650.00; remaining 550.00.
  - **WEIGHTED_AVG_RUNNING**: each issue uses the average at its moment (in date order): issue on Jan 25 after both receipts = 11.00 → 1,650.00; an issue of 50 on Jan 10 would be at 10.00.
  - **MOVING_AVG**: like running average, but invoice-price differences on already-issued quantity post to price difference (not to stock); differences on remaining stock adjust its value.
  - **STANDARD**: all transactions at the active `ItemCost` for product × site; receipt at PO price 10.50 against standard 10.00 for 100 units posts PPV 50.00 Dr; invoice at 10.80 posts additional PPV 30.00; inventory stays 1,000.00.
  - Settlement rows (`CostSettlement`) link issue to receipt with qty and value; `cost_amount_adjustment` on the issue = settled value − previously posted value.

- **FR-20.2 Inventory close** — Must. The system shall run `InventoryClose` for a legal entity up to a close date as a Temporal workflow: pre-check (unsettled negative on-hand, missing invoices for RECEIVED-not-PURCHASED when `close_requires_invoice=true`, open count sheets), settlement per product (child workflows, parallelism configurable, default 32), adjustment postings to the ledger (one voucher per product group × account, idempotent on close id), and lock of transactions with `financial_date ≤ close_date` against cost changes.
  - Performance: 1M transactions, 2,000 products in < 20 min on the stage sizing.
  - Cancel close reverses adjustment vouchers and unlocks; a close cannot be cancelled if a later close exists.
  - Close requires the INVENTORY period for the close date to be OPEN; after close completes it may be set CLOSED (wave 14).

- **FR-20.3 Recalculation** — Must. The system shall run the same settlement as close without locking, posting adjustments with `kind=RECALC` reversible by the next recalculation or close; preview mode returns per-product adjustment totals without posting.

- **FR-20.4 Cost transactions and value report** — Must. The system shall write a `CostTransaction` for every value-affecting event (physical posting, financial posting, invoice difference, settlement adjustment, PPV, revaluation, manual adjustment) with the ledger voucher, and serve the inventory value report (quantity and value by product, site, warehouse, item group, dimension; posted vs physical value; opening, receipts, issues, adjustments, closing) whose totals equal the ledger inventory account balances per dimension on the report date.
  - Daily reconciliation job `costing.reconcile` emits `reconciliation.diff` on any difference.
  - Value report for 10k products p95 < 5 s.

- **FR-20.5 Costing versions and standard cost activation** — Must. The system shall maintain `CostingVersion` (type STANDARD, PLANNED; blocking flags; fallback version) with `ItemCost` rows (product, site, unit cost, cost breakdown placeholder for wave 39, effective_from, status PENDING/ACTIVE/SUPERSEDED). Activation sets ACTIVE, supersedes the previous, posts revaluation of on-hand `(new − old) × physical qty` per product × site to inventory / revaluation account, and emits `cost.changed`.
  - Pending prices can be imported (CSV/API) and copied from the previous version with a percent change.
  - Activation is atomic per product and idempotent per version id.

- **FR-20.6 Cost adjustments** — Must. The system shall allow manual adjustments of on-hand value (per product × dimension: new unit cost or amount) and of specific receipt transactions (amount), posting to inventory / adjustment account and flowing to issues on next close.

- **FR-20.7 Purchase price variance** — Must. The system shall, for STANDARD items, post PPV at receipt `(PO price − standard) × qty` and at invoice `(invoice price − PO price) × qty` (wave 18 hook), to the PPV account from the posting profile; for actual-cost methods the invoice difference adjusts the receipt's cost (`cost_amount_posted`) and flows to issues through settlement.

- **FR-20.8 Marking** — Should. The system shall honor explicit marking between an issue and a receipt (set by users or by wave 21/27 processes) so settlement pairs marked transactions first regardless of method.

- **FR-20.9 Supplier portal: PO acknowledgement** — Must. The portal shall show confirmed POs and versions to the vendor's users; they shall accept, reject (reason) or propose changes (date, qty, price per line) which create a `PurchaseOrderAcknowledgement`; proposed changes become a pending change on the PO for the buyer to accept (creating a new version via wave 17) or decline.
  - SLA tracking: acknowledgement due within `ack_due_days` (default 2); overdue listed on the buyer workspace.

- **FR-20.10 Supplier portal: ASN** — Must. The portal shall create an `AdvanceShipNotice` (carrier, tracking, ship date, expected arrival, packages with SSCC optional, lines with PO line, qty, batch, serials, expiry) that pre-creates ARRIVED expected lines in receiving; receipt registration from an ASN pre-fills quantities and tracking dimensions.

- **FR-20.11 Supplier portal: invoices and payments** — Must. The portal shall let vendors submit invoices against received PO lines (source PORTAL into wave 18 `:capture`, structured, no OCR), view invoice match/approval status (without internal variance amounts), and see payment status, advices and remittance details from wave 19.

- **FR-20.12 Supplier portal: identity and authorization** — Must. The portal shall use a separate Keycloak realm `suppliers` with vendor organizations as groups, vendor-admin self-service invitation of users (email verification, MFA optional per vendor), OPA policies scoping every query to the vendor ids the user belongs to, and rate limits (60 requests/min per user, 1,000/min per vendor).

- **FR-20.13 Supplier portal: RFQ replies and documents** — Should. The portal shall accept RFQ replies (wave 17 FR-17.4) and exchange documents (certificates, contracts) with virus scanning and 25 MB size limit.

- **FR-20.14 Phase-exit walkthrough** — Must. The system shall support the recorded end-to-end demo: requisition → PO → portal acknowledgement → ASN → receipt → invoice (portal) → match → payment run → close → value report tie-out, on the stage environment with the demo plant.

### 3.20.4 Data model

**CostTransaction**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | |
| inventory_transaction_id | uuid | yes | FK |
| product_id, site_id, dimension_set_id | uuid/bytea | yes | denormalized for the report |
| kind | enum | yes | PHYSICAL, FINANCIAL, INVOICE_DIFF, SETTLEMENT_ADJ, PPV, REVALUATION, MANUAL_ADJ, RECALC |
| qty | numeric(28,6) | yes | 0 for value-only |
| amount_acct | numeric(19,4) | yes | signed |
| close_id / recalc_id | uuid | no | |
| ledger_entry_id | uuid | yes | voucher |
| financial_date | date | yes | |
| created_at | timestamptz | yes | append-only |

**CostSettlement**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | |
| close_id | uuid | yes | or recalc id |
| issue_transaction_id, receipt_transaction_id | uuid | yes | |
| qty_settled | numeric(28,6) | yes | > 0 |
| value_settled_acct | numeric(19,4) | yes | |
| method | enum | yes | method used |
| sequence | int | yes | order of settlement for determinism |
| cancelled (bool), cancelled_by_close_id | | no | |

**InventoryClose**

| Field | Type | Required | Notes |
|---|---|---|---|
| id, close_number | uuid, text | yes | |
| legal_entity_id, close_date | uuid, date | yes | |
| kind | enum | yes | CLOSE, RECALC, PREVIEW |
| status | enum | yes | PRECHECK, PRECHECK_FAILED, SETTLING, POSTING, COMPLETED, CANCELLING, CANCELLED, FAILED |
| parallelism | int | yes | default 32 |
| products_total, products_done, settlements_count, adjustment_total_acct | int/numeric | yes | progress |
| started_at, completed_at, duration_s | timestamptz/int | | |
| temporal_workflow_id | text | yes | |

**CostingVersion / ItemCost**: version (id, code, type STANDARD/PLANNED, status DRAFT/ACTIVE/ARCHIVED, fallback_version_id, block_activation (bool), site_scope[]); item cost (version_id, product_id, site_id, unit, unit_cost_acct, effective_from, status PENDING/ACTIVE/SUPERSEDED, activated_at, revaluation_ledger_entry_id, source MANUAL/IMPORT/COPY/ROLLUP (w39)).

**PurchaseOrderAcknowledgement / AdvanceShipNotice / SupplierUser**: ack (id, po_id, version_no, vendor_user_id, response ACCEPT/REJECT/PROPOSE, reason, proposed_lines jsonb, status PENDING_BUYER/ACCEPTED/DECLINED, created_at); asn (id, asn_number, vendor_id, po_id, carrier, tracking_number, ship_date, expected_arrival, packages jsonb (sscc, weight), lines (po_line_id, qty, batch, serials[], expiry), status SUBMITTED/ARRIVED/RECEIVED/CANCELLED); supplier user (keycloak_subject, vendor_ids[], role VENDOR_ADMIN/VENDOR_USER, invited_by, mfa_enabled, last_login).

Invariants: for each issue transaction Σ qty_settled over non-cancelled settlements ≤ |qty|; after a COMPLETED close every issue with `financial_date ≤ close_date` is fully settled or listed in the close exception report; Σ CostTransaction.amount_acct per inventory account = ledger balance; exactly one ACTIVE ItemCost per (STANDARD version chain, product, site); a vendor user can read only rows where vendor_id ∈ their vendor_ids (OPA + RLS policy on the `supplier` database). Indexes: CostTransaction (tenant, product_id, financial_date), (ledger_entry_id); CostSettlement (issue_transaction_id), (receipt_transaction_id); ItemCost (product_id, site_id, status).

### 3.20.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/costing/closes | Start close (kind CLOSE/RECALC/PREVIEW) | entity+close_date+kind | InventoryCloseRun |
| GET | /api/v1/costing/closes/{id} | Progress and exceptions | n/a | CostingInquire |
| POST | /api/v1/costing/closes/{id}:cancel | Cancel/reverse | close id | InventoryCloseRun |
| GET | /api/v1/costing/value-report?asOf=&groupBy=&site= | Inventory value with GL tie-out | n/a | CostingInquire |
| POST | /api/v1/costing/adjustments | On-hand or transaction adjustment | client key | CostAdjust |
| POST | /api/v1/costing/versions | Create costing version | client key | CostingVersionMaintain |
| POST | /api/v1/costing/versions/{id}/item-costs:import | Import pending costs (CSV/JSON) | file hash | CostingVersionMaintain |
| POST | /api/v1/costing/versions/{id}:activate | Activate pending costs with revaluation | version id | CostingVersionActivate |
| GET | /api/v1/costing/products/{id}/cost?site=&date= | Current cost (used by waves 21, 27) | n/a | CostingInquire |
| POST | /internal/v1/costing/ppv | Hook from receiving/payables for STANDARD items | source doc id | service account |
| GET | /portal/v1/purchase-orders | Vendor's POs | n/a | portal: VendorUser |
| POST | /portal/v1/purchase-orders/{id}:acknowledge | Accept/reject/propose | po+version | portal: VendorUser |
| POST | /portal/v1/asns | Submit ASN | client key | portal: VendorUser |
| POST | /portal/v1/invoices | Submit invoice (→ payables capture) | vendor invoice number | portal: VendorUser |
| GET | /portal/v1/payments | Payment status and advices | n/a | portal: VendorUser |
| POST | /portal/v1/users:invite | Vendor admin invites user | email | portal: VendorAdmin |

Commands: `StartClose` (period open, no running close for entity, close_date ≥ last close), `ActivateVersion` (no block flag, all pending costs > 0 unless `allow_zero_cost`, site in scope), `Acknowledge` (latest version only; proposals require at least one changed line), `SubmitASN` (PO confirmed, qty ≤ remaining × (1 + over_delivery_pct), serial count = qty for serial items).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.costing.close.completed.v1` / `.cancelled.v1` / `.failed.v1` | close lifecycle | close_id, entity, close_date, kind, products, settlements, adjustment_total, duration_s, exceptions_count | ledger reconciliation, notifications, analytics |
| `dev.passage.costing.cost.changed.v1` | activation, manual adjustment affecting unit cost | product_id, site_id, version, old_cost, new_cost, effective_from, revaluation_amount | inventory (cost price cache), planning (w22), product costing (w39) |
| `dev.passage.costing.reconciliation.diff.v1` | daily tie-out diff ≠ 0 | account, dimension, inventory_value, ledger_balance, diff, sample vouchers | alerting |
| `dev.passage.supplier.po.acknowledged.v1` | portal ack | po_id, version, response, proposed changes | procurement (pending change), notifications |
| `dev.passage.supplier.asn.submitted.v1` | ASN submit | asn_id, po_id, lines, expected_arrival, tracking | receiving (expected lines), warehouse (w31), notifications |
| `dev.passage.supplier.invoice.submitted.v1` | portal invoice | invoice_id (payables), vendor_id, po_id, total | payables (already captured), notifications |

### 3.20.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Costing workspace | Last close status, reconciliation diffs, pending costing versions, products with negative value or unsettled issues | counts, amounts, durations | CostingInquire |
| Inventory close | Pre-check, start, monitor (progress bar per product), exceptions list, cancel | close date, kind, parallelism, exceptions (product, reason, qty) | InventoryCloseRun |
| Inventory value report | Group by, as-of date, GL tie-out column, drill to cost transactions and vouchers, export | product, site, qty, physical value, financial value, GL balance, diff | CostingInquire |
| Costing version / item costs | Create, import, copy with %, compare to active, activate with revaluation preview | product, site, pending cost, active cost, delta, on-hand qty, revaluation amount | CostingVersionMaintain |
| Cost adjustment | Adjust on-hand or receipt, preview voucher, post | product, dims, qty, current value, new unit cost, delta | CostAdjust |
| Supplier portal: home | Open POs needing ack, ASNs, invoices in progress, payments | counts, due dates | portal VendorUser |
| Supplier portal: PO | View versions, accept/reject/propose per line, print | lines, dates, prices, version diff | portal VendorUser |
| Supplier portal: ASN | Create from PO lines, add packages, batches/serials, submit | PO lines, qty, batch, expiry, serials, tracking | portal VendorUser |
| Supplier portal: invoices / payments | Submit invoice from receipts, status timeline, payment advices | invoice no, amount, status, payment date, reference | portal VendorUser |
| Supplier portal: users | Invite, deactivate, MFA | email, role, status | portal VendorAdmin |

Interaction rules: the close screen refreshes progress via server-sent events every 2 s; value report defaults to the last close date; activation shows a mandatory revaluation preview with total and top-20 deltas; portal is WCAG 2.1 AA, mobile-friendly, English + German + French + Spanish strings in v1, no internal data (variance amounts, standard costs, approvals) exposed; portal file uploads scanned before storage.

### 3.20.7 Business rules and edge cases

- **BR-20.1** Close state machine: `PRECHECK → SETTLING → POSTING → COMPLETED`; `PRECHECK → PRECHECK_FAILED`; `SETTLING|POSTING → FAILED` (resumable); `COMPLETED → CANCELLING → CANCELLED` only when it is the latest close for the entity.
- **BR-20.2** Settlement determinism: receipts are ordered by (financial_date, physical_date, id) and issues by (financial_date, physical_date, id); the same inputs always produce the same `CostSettlement` sequence (property-tested), so re-runs after crashes are idempotent.
- **BR-20.3** Unsettled issues (negative on-hand at settlement time) are valued at the running average of the product × site (fallback: active standard, then last receipt cost) and listed as exceptions; a later receipt settles them at the next close with an adjustment.
- **BR-20.4** Rounding: settlement values are computed in full precision and rounded half-even to the minor unit per settlement row; the last settlement row of a receipt absorbs the rounding residual so Σ settled value = receipt value exactly.
- **BR-20.5** Periodic weighted average with transfers between sites: the average is per product × site (`costing_level=SITE`, default) or per product (`costing_level=ENTITY`) as configured on the item model group; transfers at site level settle like issues/receipts.
- **BR-20.6** Invoice difference after close: an invoice posted after the receipt's period was closed posts the difference to the invoice's period as `INVOICE_DIFF` and flows to issues in the next close (never reopens a closed period).
- **BR-20.7** Standard cost revaluation uses the physical on-hand quantity at activation time; transactions already financially posted are not restated; activation during an in-flight close is rejected (`CST-ACT-004`).
- **BR-20.8** Close date must be ≤ today and ≥ the last completed close; transactions with `financial_date ≤ close_date` are locked against new cost adjustments except through `:cancel`.
- **BR-20.9** Portal acknowledgement applies to the latest PO version only; a new version invalidates pending acknowledgements (status SUPERSEDED) and re-requests one.
- **BR-20.10** ASN quantity may exceed PO remaining only within the over-delivery tolerance (wave 18); duplicate tracking number per vendor is rejected; an ASN can be cancelled until any of its lines is RECEIVED.
- **BR-20.11** Portal invoice submission is limited to lines with received, uninvoiced quantity; the vendor cannot submit a price different from the PO price unless the buyer enabled `portal_price_override` for the vendor (creating a price-variance exception in wave 18).
- **BR-20.12** Concurrency: close child workflows take an advisory lock per product; wave 16 journal posting for a product whose close child is settling waits up to 5 s then fails with `CST-LOCK-001` (retry); costs read by waves 21/27 are the last committed values.

### 3.20.8 Acceptance criteria

- **AC-20.1** (FR-20.1) Given the 100 receipt/issue sequences per method fixture, when settled by close, then every issue cost, remaining on-hand value and adjustment equals the reference workbook to the cent for FIFO, LIFO, LIFO_DATE, WEIGHTED_AVG_PERIODIC, WEIGHTED_AVG_RUNNING, MOVING_AVG and STANDARD.
- **AC-20.2** (FR-20.1) Given the FR-20.1 example data, when closed under FIFO, LIFO_DATE and WEIGHTED_AVG_PERIODIC, then issue cost is 1,600.00, 1,700.00 and 1,650.00 and remaining value 600.00, 500.00 and 550.00.
- **AC-20.3** (FR-20.2) Given the 1M-transaction fixture, when a close runs on stage, then it completes in < 20 min, and the value report at close date equals the GL inventory balances per site and item group with diff 0.00.
- **AC-20.4** (FR-20.7) Given a STANDARD item at 10.00 with a PO at 10.50 and invoice at 10.80 for 100 units, when received and invoiced, then PPV postings are 50.00 and 30.00 Dr, inventory value is 1,000.00, GR/IR nets to zero.
- **AC-20.5** (FR-20.5) Given 10,000 pending item costs, when the version is activated, then all are ACTIVE within 2 min, one revaluation voucher per site balances to `Σ (new − old) × on-hand`, and `cost.changed` is emitted per product.
- **AC-20.6** (FR-20.2) Given a close killed during SETTLING at 40 %, when resumed, then it completes with the same settlement rows as an uninterrupted run (byte-equal export).
- **AC-20.7** (FR-20.9) Given a confirmed PO visible in the portal, when the vendor proposes a delivery date change, then a pending change appears on the buyer's PO and accepting it creates version 2 and emits `po.version.created`.
- **AC-20.8** (FR-20.10) Given an ASN for 2 batch-tracked lines, when the goods arrive and the receiver opens the receipt from the ASN, then quantities, batches and expiry dates are pre-filled and registration takes one confirmation.
- **AC-20.9** (FR-20.12) Given a user of vendor A, when they request vendor B's PO by id, then the portal returns 404 and the OPA decision log records the denial.
- **AC-20.10** (FR-20.14) Given the demo plant, when the phase-exit walkthrough is executed, then every step completes on stage and the final value report, GR/IR report and AP aging each reconcile to the GL with 0.00 difference.

### 3.20.9 Test plan and fixtures

Fixtures: `cost-methods-700` (100 sequences × 7 methods with reference workbook `cost_reference.xlsx` maintained by the accountant; includes negative on-hand, transfers, invoice differences, returns); `cost-1m` (reuses `inv-1m-txn` with receipts/invoices from waves 17–18 generators); `cost-std-activation-10k`; `portal-demo-vendor` (vendor with 3 users, 20 POs, 5 ASNs); `cost-ax-extract` (InventClosing, InventSettlement, InventItemPrice from USMF); `cost-sap-extract` (MBEW, CKMLHD/CKMLPP/CKMLCR samples).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Method correctness | unit (table-driven) | settlement engine | AC-20.1 |
| Determinism/idempotency | property | engine | identical inputs → identical settlements; resume = uninterrupted |
| Close performance | performance (stage) | InventoryClose | < 20 min on cost-1m |
| Value report tie-out | E2E | value-report vs ledger | diff 0.00 after close |
| PPV | contract | receiving/payables → costing/ppv | AC-20.4 |
| Activation | E2E | versions:activate | AC-20.5 |
| Portal authz | security (OPA unit + E2E) | portal APIs | AC-20.9; no cross-vendor leakage across 1,000 random requests |
| Portal accessibility | E2E (axe) | portal screens | 0 serious/critical violations |
| Phase-exit walkthrough | E2E (Playwright, recorded) | full flow | AC-20.10 |

Reconciliation checks: Σ CostTransaction per inventory account = ledger balance; Σ settlement value per receipt = receipt value; Σ PPV cost transactions = PPV account movement; revaluation voucher = Σ item deltas × on-hand; portal invoice totals = payables captured totals.

### 3.20.10 Deployment and operations

Helm charts: `passage-costing` (API ×2, Temporal close workers ×4 with CPU 2 each, schema `costing` in `inventory-db`), `passage-supplier-portal` (React app served by the shell at `supplier.<env>.passage.dev`, BFF ×2 in TypeScript, CloudNativePG `supplier-db`, Keycloak realm `suppliers` provisioned by Helm hook, Envoy Gateway route with rate limiting and WAF rules). Config: `CST_CLOSE_PARALLELISM=32`, `CST_LOCK_WAIT_S=5`, `CST_LEVEL_DEFAULT=SITE`, `CST_ALLOW_ZERO_COST=false`, `PORTAL_RATE_LIMIT_USER=60`, `PORTAL_UPLOAD_MAX_MB=25`, `PORTAL_ACK_DUE_DAYS=2`. Migrations `inventory/v20.*` (costing schema), `supplier/v20.*`. Flags: `cst.moving_avg`, `cst.marking`, `portal.rfq_replies`, `portal.invoice_submit`, `portal.mfa_required`. Jobs: reconciliation daily 02:15; ack-overdue scan daily 08:00; portal inactive-user cleanup weekly. SLOs: value report p95 < 5 s; cost lookup p95 < 20 ms; close < 20 min per 1M txns; portal page load p95 < 1.5 s; portal availability 99.9 %. Alerts: reconciliation diff ≠ 0 (page); close FAILED (page); close running > 60 min (ticket); portal auth failures > 100/min from one IP (security ticket, auto-block). Runbook: resume close, cancel close, re-run reconciliation, rotate portal realm client secret, revoke vendor user. Rollback: additive migrations; costing schema can be disabled (flag `cst.enabled=false` reverts to wave 16 running estimate without data loss); portal can be taken down independently (DNS) without affecting internal flows.

### 3.20.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX InventModelGroup.InventModel | ItemModelGroup.costing_method | FIFO→FIFO, LIFO→LIFO, LIFODate→LIFO_DATE, WeightedAvg→WEIGHTED_AVG_PERIODIC, WeightedAvgDate→WEIGHTED_AVG_RUNNING, StdCost→STANDARD, MovingAverage→MOVING_AVG |
| AX InventItemPrice (active, PriceType Cost) per CostingVersion | ItemCost ACTIVE in version `MIG-STD` | per site; pending prices migrated as PENDING |
| AX InventSum PostedValue / wave 16 opening | opening cost layers: one receipt layer per product × dimension at average cost | FIFO/LIFO layer detail from last InventClosing unsettled receipts (InventTrans with open qty) when available, else collapsed to one layer |
| AX InventClosing history | not migrated | reporting only |
| SAP MBEW (VPRSV, STPRS, VERPR, LBKUM, SALK3, PEINH) | costing_method (S→STANDARD, V→WEIGHTED_AVG_RUNNING or MOVING_AVG per decision), ItemCost (STPRS/PEINH), opening layer value SALK3 | per plant (BWKEY) → site |
| SAP CKMLPP/CKMLCR (material ledger period values) | opening layer value when ML active | actual price of last closed period |
| SAP MLCD / CKMLHD history | not migrated | |
| D365 vendor collaboration users / Ariba supplier accounts | SupplierUser invitations (not credentials) | vendors re-register via invitation email |

Validation: Σ opening layer value per product × site equals source value (0.01 tolerance per product, 0.00 total per account); standard costs for 200 random products equal source; costing method distribution equals source counts. Not migrated: settled cost history, SAP ML price analysis, AX recalculation logs, portal documents.

### 3.20.12 Dependencies, risks, open questions

Depends on: wave 16 (transactions, cost fields, marking field), wave 17 (PO prices, versions, pending changes), wave 18 (receipts, invoice differences, capture), wave 19 (payment status), wave 11–14 (posting, period control, vouchers), wave 4 (Temporal), wave 2–3 (Keycloak realms, OPA, metadata), wave 7 (vendor contacts).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Opening cost layers from AX/SAP are collapsed, so first FIFO/LIFO close differs from legacy | High | Medium | Document in cutover; parallel-run compare in wave 49; collapsed layer flagged in value report |
| Close parallelism contends with daytime posting | Medium | Low | Run off-hours by default; lock wait 5 s with retry; advisory locks per product only |
| Portal security incident | Low | High | Separate realm/DB, OPA deny-by-default, pen-test before phase exit, WAF, rate limits, audit |
| Accountant requires LIFO by period layers (US tax) beyond LIFO_DATE | Medium | Low | Periodic LIFO pools deferred; documented as v1.x |

Open questions:
1. SAP MAP (V) maps to WEIGHTED_AVG_RUNNING or MOVING_AVG by default? Default: MOVING_AVG (closest to VERPR behavior on invoice differences). Decide by wave 20 mid-point.
2. Should the portal expose expected payment dates (from proposals) or only executed payments? Default: executed only. Decide by wave 20 close.
3. Costing level ENTITY vs SITE default for weighted average? Default: SITE. Decide by wave 20 mid-point with the accountant.
4. Dollar-value LIFO pools for US tax reporting: v1.x or wave 39? Default: v1.x. Decide by wave 39 planning.
