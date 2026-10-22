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
| `passage.inventory.transaction.posted.v1` | inventory | costing, planning (w22), analytics | txn id, origin, product, dims, qty, issue/receipt status, physical/financial dates |
| `passage.inventory.onhand.changed.v1` | inventory | procurement (ATP for PO), sales (w27), warehouse (w31) | product, dim hash, avail_physical, avail_ordered, delta |
| `passage.inventory.journal.posted.v1` | inventory | ledger (reconciliation), costing | journal id, type, line count, ledger entry id |
| `passage.procurement.requisition.approved.v1` | procurement | procurement consolidation, budget control (w38) | req id, lines, approver chain |
| `passage.procurement.po.confirmed.v1` | procurement | inventory (OnOrder txns), supplier-portal, EDI (w35) | po id, version, lines, vendor |
| `passage.procurement.po.version.created.v1` | procurement | supplier-portal, receiving | po id, version, diff summary |
| `passage.receiving.receipt.posted.v1` | receiving | inventory, payables (match candidates), costing | receipt id, lines, po lines, qty, GR/IR amount |
| `passage.payables.invoice.posted.v1` | payables | ledger, costing (PPV/invoice-cost settle), tax (w13) | invoice id, vendor, lines, match status, amounts |
| `passage.payables.invoice.match.exception.v1` | payables | workflow (Temporal), notifications | invoice id, line, variance type, amounts |
| `passage.payables.settlement.posted.v1` | payables | ledger, analytics | settlement id, items, discount, fx gain/loss |
| `passage.payables.payment.sent.v1` | payables | bank connectivity (w36), supplier-portal, cash (w36) | payment id, file id, amount, method, vendor |
| `passage.payables.payment.status.changed.v1` | payables | notifications, analytics | payment id, old/new status, pain.002 reason code |
| `passage.costing.close.completed.v1` | costing | ledger, analytics | close id, period, adjustments, duration |
| `passage.costing.cost.changed.v1` | costing | inventory, planning, product costing (w39) | product, site, version, old/new cost, effective date |
| `passage.supplier.asn.submitted.v1` | supplier-portal | receiving, warehouse (w31) | asn id, po lines, expected qty, carrier |

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

- **FR-16.12 Daily reconciliation** — Must. The system shall run a job that compares SUM(OnHand.posted_value) by inventory account and dimension to the ledger balance projection and raises `passage.inventory.reconciliation.diff.v1` on any non-zero difference.

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
| `passage.inventory.transaction.posted.v1` | any transaction insert or status change | transaction_id, origin, product_id, dimension_set, qty, issue_status, receipt_status, physical_date, financial_date, cost_amount_* | costing, analytics, planning |
| `passage.inventory.onhand.changed.v1` | OnHand row update | product_id, dimension_set, avail_physical, avail_ordered, delta_bucket, delta_qty | procurement, sales, warehouse |
| `passage.inventory.journal.posted.v1` | journal reaches POSTED | journal_id, type, lines, ledger_entry_id, posting_date | ledger reconciliation, costing |
| `passage.inventory.reservation.created.v1` / `.released.v1` | reservation lifecycle | reservation_id, demand, product, dims, qty, policy | sales, production |
| `passage.inventory.reconciliation.diff.v1` | daily job finds diff ≠ 0 | account, dimension, inventory_value, ledger_balance, diff | alerting |

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
| `passage.procurement.requisition.submitted.v1` / `.approved.v1` / `.rejected.v1` | workflow transitions | requisition_id, lines (product, qty, amount), total, approver chain, policy_version | notifications, budget (w38), analytics |
| `passage.procurement.rfq.sent.v1` / `.awarded.v1` | RFQ send/award | rfq_id, vendors, lines, awarded vendor per line | supplier-portal (w20), notifications |
| `passage.procurement.po.confirmed.v1` | confirm | po_id, version_no, vendor_id, lines (product, qty, unit, price, delivery dates, site/warehouse), totals, output channel | inventory, receiving (w18), supplier-portal, EDI (w35), analytics |
| `passage.procurement.po.version.created.v1` | any confirmed version > 1 | po_id, version_no, patch, reapproval_required | supplier-portal, receiving |
| `passage.procurement.po.cancelled.v1` | cancel | po_id, lines cancelled, remaining qty | inventory (cancel ORDERED), supplier-portal |
| `passage.procurement.agreement.threshold.v1` | released ≥ 90 % or expiry < 30 d | agreement_id, line, pct, expiry | notifications |

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

