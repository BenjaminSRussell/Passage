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
| Transfer orders | InventTransferTable/InventTransferLine, InventTransferJour (shipment, receipt), in-transit warehouse (InventLocation type Transit), Transfer order parameters, Transfer order picking release | Stock transport order (ME21N item category U, document type UB intra-company / NB cross-company), replenishment delivery NL/NLCC via VL10B, movement 351/641 to stock in transit, GR 101, MB5T | `TransferOrder`, `TransferOrderLine`, `TransferShipment`, `TransferReceipt`; in-transit warehouse dimension (FR-16.15, FR-16.16) | full (cross-company STO pricing and intercompany invoice in wave 40; picking work in wave 31) |
| Vendor consignment | D365 consignment replenishment orders (ConsignmentReplenishmentOrder), inventory ownership change journal (InventOwnershipChangeJournal), owner inventory dimension (InventDim.InventOwnerId), Consignment parameters | Item category K, special stock K (MKOL), MB54, PI for consignment MIK1, settlement MRKO, stock determination OSPX/OSPW, consignment info record price | `owner` dimension, `ConsignmentReplenishmentOrder`, journal type OWNERSHIP_CHANGE (FR-16.17); settlement invoice in wave 18 FR-18.16; portal view wave 20 FR-20.22 | full |
| Customer consignment | Not standard in AX 2012 (warehouse per customer convention) | Special stock W (MSKU), MB58, fill-up 631 / issue 633 | Warehouse type `CUSTOMER_CONSIGNMENT` with `customer_id` (FR-16.17); fill-up and consumption documents in wave 27 | partial (documents in wave 27) |
| Sales-order / project stock | Not in AX 2012 (marking only) | Special stock E (MSKA) / Q (MSPR), valuated/non-valuated, MBBS | `segment` dimension (SALES_ORDER / PROJECT + reference) (FR-16.20) | full (value report by segment in wave 20) |
| Tag counting | InventJournalTable type TagCounting (InventJournalTrans with TagId), Create tag counting, Transfer to counting journal | No direct equivalent (MI09 count without reference, count tags printed from MI21) | Journal type TAG_COUNT with `CountTag` rows → COUNT journal (FR-16.18) | full |
| ABC classification | InventTable ABCRevenue/ABCValue/ABCMargin/ABCContribution, periodic ABC classification (InventABCUpdate), ABC classification report | MIBC ABC analysis for cycle counting (MARC-ABCIN), cycle counting indicators OMCO | `AbcClassification` per product × site with periodic job (FR-16.19); feeds CountPlan selection | full |
| Statistical inventory sampling | n/a | MIS1 / MIS2 inventory sampling | Not provided; CountPlan `sample_pct` counts a random sample without extrapolation | dropped-with-reason (see 3.16.21) |

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
  - Dimensions: site, warehouse, location, batch, serial, inventory_status, config, size, color, style, license_plate (reserved), owner (activated by FR-16.17 consignment).
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

- **FR-16.15 Transfer orders with in-transit stock** — Must. The system shall provide a `TransferOrder` document (AX InventTransferTable; SAP stock transport order UB) with header `from_warehouse_id`, `to_warehouse_id`, `transit_warehouse_id`, `ship_date`, `receipt_date`, `delivery_terms`, `mode_of_delivery`, status `DRAFT, CREATED, SHIPPED, PARTIALLY_RECEIVED, RECEIVED, CANCELLED`, and lines with `qty`, `shipped_qty`, `received_qty`, `remaining_qty`, `underdelivery_pct`, `overdelivery_pct`, `close_short` and tracking dimensions.
  - Creation writes an ON_ORDER issue at the from-warehouse and an ORDERED receipt at the to-warehouse (supply and demand for wave 22 planning).
  - `:ship` (AX "Ship transfer order", SAP VL10B + PGI 641) posts in one database transaction a DEDUCTED issue at the from-dimension and a RECEIVED receipt at the transit dimension (`warehouse = transit_warehouse_id`, status `IN_TRANSIT`); cost of the transit receipt equals the cost of the issue (BR-16.3).
  - `:receive` (AX "Receive", SAP MIGO 101 against STO) posts a DEDUCTED issue at the transit dimension and a RECEIVED receipt at the to-dimension with the registered batch/serial/location; under-delivery within `underdelivery_pct` with `close_short=true` closes the line; over-receipt beyond `overdelivery_pct` is rejected with `INV-TRF-002`.
  - `transit_warehouse_id` is mandatory when the legal-entity parameter `transfer_use_transit=true`; otherwise `:ship` performs ship-and-receive in one step (SAP one-step 301).
  - Transfer order picking release emits `dev.passage.inventory.transfer.release.v1` for wave 31 warehouse work (and wave 28 outbound picking); until those waves exist the shipment is posted from the transfer order screen or PWA.
  - Cancellation of unshipped remaining quantity cancels the ON_ORDER/ORDERED pair; delete of CREATED orders with no postings is allowed (AX "Delete transfer orders").
  - Documents: transfer shipping document and transfer receiving document (3.16.15).

- **FR-16.16 In-transit stock inquiry** — Should. The system shall list stock in transit by transfer order, product, from/to warehouse, ship date and days in transit (SAP MB5T), sourced from OnHand rows at transit warehouses, with drill-down to the transfer order.

- **FR-16.17 Ownership dimension, vendor consignment and customer consignment** — Must. The system shall activate the `owner` dimension (D365 InventOwnerId; SAP special stock indicator K) through the tracking dimension group, with `owner = legal entity` by default or a vendor id for vendor-owned stock.
  - `ConsignmentReplenishmentOrder` (vendor, lines product/qty/date/warehouse) receives vendor-owned quantity as RECEIVED transactions with `owner = vendor`, `posted_value = 0`, no ledger entry and no GR/IR (quantity-only stock).
  - Journal type OWNERSHIP_CHANGE moves quantity from `owner = vendor` to `owner = legal entity` as a paired issue/receipt at the consignment price (wave 17 trade agreement with `consignment=true`, else the replenishment order price), posting Dr inventory / Cr consignment accrual (posting profile account `consignment_accrual`; SAP OBYC KON) and emitting `dev.passage.inventory.ownership.changed.v1`; wave 18 FR-18.16 consumes it to create the consignment settlement invoice (SAP MRKO).
  - When the item model group sets `auto_ownership_change_on_issue=true`, an issue that would consume vendor-owned stock creates the ownership change automatically before the issue; stock determination order (`OWN_FIRST` / `CONSIGNMENT_FIRST`, SAP OSPX) is set per warehouse.
  - A periodic ownership-change batch (3.16.16) converts consumption-driven and age-driven (`max_consignment_days`) quantities.
  - Vendor-owned stock is counted in cycle counts (SAP MIK1) and is excluded from the inventory value report; consignment inventory reports (3.16.14) list vendor-owned quantity by vendor, warehouse and age.
  - Customer consignment: a warehouse of type `CUSTOMER_CONSIGNMENT` carries `customer_id`; stock there remains `owner = legal entity`, is reported by customer (SAP MB58), and is consumed by wave 27 consignment fill-up/issue documents.

- **FR-16.18 Tag counting** — Should. The system shall support two-step tag counting (AX tag counting journal): a `CountTag` range is generated per plan or warehouse with sequence `TAG-`, each tag printed with a barcode; a tag entry records product, dimensions and counted quantity per tag with status `USED`, `VOID`, `MISSING`; "transfer to counting journal" aggregates USED tags by product × dimension set into a COUNT journal where expected quantity is compared; missing and void tags are listed before transfer and each tag number is used at most once (`INV-TAG-001`).

- **FR-16.19 ABC classification** — Should. The system shall compute `AbcClassification` per product × site for criteria `VALUE` (on-hand value), `REVENUE`, `MARGIN`, `CARRYING_COST` over a lookback (default 365 days) with cumulative-percentage thresholds (default A 80 %, B 15 %, C 5 %, must sum to 100 — `INV-ABC-001`), store the class on the product (AX InventTable ABC fields; SAP MARC-ABCIN), run it as a monthly job and expose a report; CountPlan selection by `abc_class[]` uses the latest run.

- **FR-16.20 Segregated stock (sales-order and project stock)** — Should. The system shall support a `segment` dimension with `segment_type` (`NONE`, `SALES_ORDER`, `PROJECT`) and `segment_ref_id` (SAP special stock E / Q), activated per tracking dimension group, with an item model group flag `segment_valuated` (valuated vs non-valuated special stock); policy-based reservation for a demand carrying a segment considers only matching-segment stock first, then unsegmented stock when `segment_fallback=true`; the wave 20 value report groups by segment (SAP MBBS).

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
| `dev.passage.inventory.transfer.shipped.v1` / `.received.v1` / `.release.v1` | transfer order ship / receive / picking release | transfer_order_id, lines (product, dims, qty), from/to/transit warehouse, ship_date, receipt_date | planning (w22), warehouse (w31), fulfillment (w28), analytics |
| `dev.passage.inventory.ownership.changed.v1` | OWNERSHIP_CHANGE journal posted | journal_id, vendor_id, lines (product, dims, qty, consignment_price, amount_acct), ledger_entry_id | payables (w18 consignment settlement), supplier-portal (w20), analytics |

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
- **BR-16.13** Transfer order state machine: `DRAFT → CREATED → SHIPPED → PARTIALLY_RECEIVED → RECEIVED`; `CREATED|SHIPPED → CANCELLED` only for unshipped remaining quantity (shipped quantity in transit must be received or returned by a reverse transfer); `received_qty ≤ shipped_qty` per line; the transit dimension never carries reservations.
- **BR-16.14** Vendor-owned stock (`owner ≠ legal entity`) never carries `posted_value` and never reaches a financial status; an OWNERSHIP_CHANGE line cannot exceed the vendor-owned physical quantity at the dimension set (`INV-OWN-002`); the ownership change is a paired issue/receipt whose only difference is the owner dimension, so `physical_invent` summed across owners is unchanged.
- **BR-16.15** Tag counting: a tag number is consumed once per count tag range; a USED tag with zero quantity is a valid count of zero; the transfer to the COUNT journal rejects when any tag is still OPEN unless the supervisor marks remaining tags MISSING.
- **BR-16.16** ABC thresholds are cumulative percentages of the ranked criterion; ties at a boundary go to the higher class; products with zero activity in the lookback are class `C` unless `abc_zero_class` is set to `N` (not classified).
- **BR-16.17** Segmented stock: an issue for demand with segment S may consume stock with segment S, or unsegmented stock when `segment_fallback=true`; it never consumes stock of a different segment; transfers between segments are TRANSFER journal lines with `to_dimension_set_id` differing only in segment.

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
- **AC-16.11** (FR-16.15) Given a transfer order of 100 EA from warehouse W1 to W2 with transit warehouse T1 and `transfer_use_transit=true`, when shipped and then 95 EA received with `close_short=true` and `underdelivery_pct=10`, then on-hand is W1 −100, T1 +5 in transit (flagged for investigation), W2 +95, the line is closed, and both postings have `ledger_entry_id` null when the sites share a cost (no GL effect).
- **AC-16.12** (FR-16.17) Given 500 EA vendor-owned stock (owner = V1) and an OWNERSHIP_CHANGE journal of 120 EA at consignment price 4.00 USD, when posted, then on-hand shows 380 EA owner V1 (value 0) and 120 EA owner legal entity (value 480.00), the ledger entry is Dr inventory 480.00 / Cr consignment accrual 480.00, and `ownership.changed` is emitted with amount 480.00.
- **AC-16.13** (FR-16.18) Given 50 tags generated for warehouse W1, when 48 are entered as USED, 1 VOID and 1 left open, then transfer to the COUNT journal is rejected until the open tag is marked MISSING, after which the COUNT journal contains one line per product × dimension set with the summed tag quantities.
- **AC-16.14** (FR-16.19) Given 1,000 products with on-hand value following a Zipf distribution, when ABC classification by VALUE runs with 80/15/5, then the cumulative value of class A products is ≥ 80 % and the class is visible on the product and usable as a CountPlan filter.

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

Transformation notes: dimension hash computed after mapping; quantities converted to Passage inventory unit via MARM/UnitOfMeasureConversion; opening journal posts with financial_date = cutover date − 1 and offset account = migration clearing account. Validation: Σ migrated qty per product/site equals source extract; Σ value equals source within 0.01 × product count; no serial duplicates. Not migrated: InventSum reservation buckets (recreated by open documents), AX quarantine orders (re-entered manually; typically < 50), SAP special stock O (subcontracting; wave 21); special stock K migrates as `owner = vendor` stock per FR-16.17, InventClosing history (wave 20 decides).

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

### 3.16.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `negative_physical_allowed` | item model group | bool | false | true/false | FR-16.4 rejects issues below zero `avail_physical` when false | InventModelGroup.PhysNegative ("Physical negative inventory") | OMJ1 negative stocks per valuation area / storage location (T001K, T001L) |
| `negative_financial_allowed` | item model group | bool | false | true/false | FR-16.4 on `avail_ordered` | InventModelGroup.FinancialNegative | n/a (MBEW value cannot go negative) |
| `post_physical_inventory` | item model group | bool | true | true/false | Physical statuses post `cost_amount_physical` to the ledger (accrual accounts) | InventModelGroup.PostPhysicalInvent ("Post physical inventory") | OMJJ movement type value-update flag (T156) |
| `backdated_physical_posting` | item model group | bool | false | true/false | BR-16.6 physical date in closed inventory period | n/a | MMRV allow posting to previous period |
| `reservation_policy_default` | legal entity | enum | FIFO | MANUAL, FIFO, FEFO, LIFO | FR-16.7 default when the caller passes none | Inventory parameters → Date-controlled reservation; Batch reservation parameters (FEFO date-controlled) | Batch determination strategy MBC1 / search procedure; OMCP availability check per movement type |
| `reservation_ttl_hours` | tenant | int | 0 | 0–720 | FR-16.7 auto-release of stale reservations | n/a | n/a |
| `shelf_life_margin_days` | product tracking group | int | 0 | 0–365 | FEFO excludes batches expiring inside margin | PdsShelfAdvice / best-before margin | MARA-MHDRZ remaining shelf life |
| `journal_names` | legal entity | table | 6 seeded (MOV, ADJ, TRF, BOM, CNT, OPN) | journal type, voucher sequence, posting restriction (USER/GROUP), approval workflow id, delete lines after posting | FR-16.6 header defaults and approval routing | Inventory → Journals → Journal names | Document types per movement type (OMBA), number ranges OMBT |
| `journal_max_lines` | tenant | int | 10,000 | 1,000–50,000 | FR-16.6 line cap (`INV-JNL-001`) | Inventory parameters → Journals → Journal size | n/a |
| `inventory_posting_profile` | legal entity | table | seeded from wave 11 chart | item code (table/group/all) × account code → inventory, adjustment, loss/profit, consignment accrual, transit accounts | FR-16.6 account resolution | Inventory → Posting → Inventory posting profiles (Inventory tab) | OBYC BSX / GBB (VBR, INV, ZOB) / PRD with valuation class and modifier |
| `count_tolerance_qty_pct` / `count_tolerance_value` | count plan | numeric | 5 / 100.00 acct ccy | 0–100 / ≥ 0 | FR-16.9 supervisor approval threshold | Counting groups (InventCountGroup) / counting reason codes | OMJN tolerances per user group (maximum posting difference) |
| `count_freeze` | count plan | bool | true | true/false | BR-16.8 count lock vs recount flag | n/a (uses inventory blocking) | MI01 "freeze book inventory" / posting block |
| `blind_count` | count plan | bool | false | true/false | FR-16.9 hides expected quantity | n/a | MI04 count without quantity display (variant) |
| `blocking_reason_codes` | legal entity | list | QUALITY, DAMAGE, RECALL, LEGAL, OTHER | user-defined | FR-16.8 mandatory reason | Inventory blocking reasons | OMBS reason for movement (T157D) |
| `inventory_status_transitions` | legal entity | matrix | AVAILABLE↔BLOCKED, AVAILABLE→QUARANTINE, QUARANTINE→AVAILABLE/BLOCKED (disposition) | allowed pairs | FR-16.8 status change validation | Inventory statuses / inventory status transitions (D365) | Stock types unrestricted / quality / blocked; transfer postings 321, 343, 349 |
| `serial_number_groups` | product | table | none | prefix, counter, length, auto-on-receipt | FR-16.5 auto serial assignment | Serial number groups | OIS2 serial number profiles |
| `recon_cron` | tenant | cron | `0 2 * * *` (tenant local) | valid cron | FR-16.12 schedule | n/a (Check inventory consistency, manual) | MB5L batch variant |
| `transfer_use_transit` | legal entity | bool | true | true/false | FR-16.15 two-step vs one-step transfer | Transfer order parameters → In-transit warehouse required | OMGN STO setup: delivery type NL (two-step) vs 301 one-step |
| `transit_warehouse_id` | warehouse (from) | reference | none | warehouse of type TRANSIT | FR-16.15 transit dimension per shipping warehouse | Warehouse → "Transit" warehouse assignment (InventLocation.InventLocationIdTransit) | Stock in transit per supplying plant (OMGN checking rule) |
| `transfer_underdelivery_pct` / `transfer_overdelivery_pct` | product, overridable per line | numeric | 0 / 0 | 0–100 | FR-16.15 close-short and over-receipt check | Released product → Deliver → Underdelivery/Overdelivery | OMC0 tolerance keys B1/B2 |
| `owner_dimension_active` | tracking dimension group | bool | false | true/false | FR-16.17 enables `owner` on the hash | Tracking dimension group → Owner (D365) | Item category K / special stock K |
| `auto_ownership_change_on_issue` / `stock_determination` | item model group / warehouse | bool / enum | false / OWN_FIRST | OWN_FIRST, CONSIGNMENT_FIRST | FR-16.17 consumption-driven ownership change | Consignment parameters (D365) | OSPX / OSPW stock determination strategy |
| `max_consignment_days` | vendor × product (trade agreement) | int | 0 (off) | 0–365 | Ownership change batch converts stock older than N days | n/a | Consignment info record (no age rule; custom) |
| `abc_thresholds` / `abc_criterion` / `abc_lookback_days` | legal entity | numeric triple / enum / int | 80-15-5 / VALUE / 365 | sum = 100; VALUE, REVENUE, MARGIN, CARRYING_COST | FR-16.19 | Inventory → ABC classification setup | MIBC percentages and period |
| `segment_dimension_active` / `segment_valuated` / `segment_fallback` | tracking dimension group / item model group / legal entity | bool | false / true / true | true/false | FR-16.20 | n/a | Special stock E/Q valuated vs non-valuated (account assignment category M/E) |
| `tag_number_sequence` | legal entity | number sequence | `TAG-` | wave 5 sequence | FR-16.18 | Tag counting journal name | n/a |

### 3.16.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| On-hand inquiry | list inquiry | product, site, warehouse, location, batch, serial, status, owner, segment, `groupBy`, as-of date | physical_invent, avail_physical, avail_ordered, ordered, on_order, reserved_*, posted_value | `inventory.OnHand` (as-of replays transactions) | On-hand inventory (InventOnhand), Physical inventory by inventory dimension | MMBE, MB52, MB53 plant availability, MB5B stocks on posting date |
| Inventory transactions | drill-down | product, dims, status, origin type, physical/financial date range, voucher | reference, direction, qty, statuses, dates, cost amounts, ledger voucher | `InventoryTransaction` | Inventory transactions (InventTrans), Transaction origin | MB51 material document list, MB03 / MIGO display, MBGR by reason |
| Posted journals by type | list inquiry | journal type, status, date, posted by | journal number, lines, total qty, voucher | `InventoryJournal` | Journal inquiries (posted journals by type) | MB51 by movement type / document type |
| Count variance report | operational report | plan, sheet, warehouse, date, counter | expected, counted, variance qty, variance value, approval status | `CountSheet` | Counting history / Count variance inquiry | MI20 / MI24 difference list |
| Reservation inquiry | list inquiry | product, demand type/id, policy, status | reserved qty, batch/serial, expires_at | `Reservation` | Reservation form (InventTransReservation) | MB24 / MB25 reservations list |
| Blocking and quarantine inquiry | list inquiry | product, reason, status, expected release | blocked qty, reason, created by, release date, disposition | `Blocking` + OnHand status buckets | Inventory blocking inquiry, Quarantine order inquiry | MMBE blocked / quality stock columns |
| Negative on-hand list | KPI / list | site, warehouse | product, dims, shortfall qty, first negative date | `OnHand` where `physical_invent < 0` | Inventory management workspace tile | MB52 with negative filter |
| Inventory-to-ledger reconciliation | operational report (daily) | legal entity, account, dimension, date | inventory value, ledger balance, difference, sample vouchers | FR-16.12 job output | Ledger reconciliation inquiry (Inventory/ledger) | MB5L stock values vs G/L |
| Dimension combinations inquiry | list inquiry | product | dimension set hash, active dimensions, first/last use | `DimensionSet` | Inventory dimension inquiry | n/a |
| Batch / serial trace | drill-down | batch or serial, direction (forward/backward) | transactions, documents, locations through time | `InventoryTransaction` by tracking dimension | Batch/serial trace (Item tracing) | MB56 batch where-used list |
| Transfer order lines (shipped / received) | list inquiry | from/to warehouse, status, dates, product | ordered, shipped, received, remaining, days in transit | `TransferOrderLine` | Transfer order lines inquiry (InventTransferLine) | ME2N (document type UB), VL06O STO deliveries |
| Stock in transit | list inquiry | product, from/to warehouse, days in transit > N | qty, value at from-cost, transfer order, ship date | `OnHand` at TRANSIT warehouses | On-hand at transit warehouse | MB5T stock in transit |
| Consignment stock by vendor | operational report | vendor, warehouse, product, age | vendor-owned qty, consignment price, extended value (statistical), age buckets | `OnHand` where `owner ≠ legal entity` | Consignment inventory reports (D365) | MB54 consignment stocks |
| Customer consignment stock | operational report | customer, warehouse | qty, value, last movement | `OnHand` at CUSTOMER_CONSIGNMENT warehouses | n/a | MB58 customer consignment |
| Sales-order / project stock | list inquiry | segment type, segment reference | qty, value (if valuated), reservations | `OnHand` by `segment` | n/a | MBBS valuated special stock, MMBE special stock view |
| ABC classification report | operational report | site, criterion, run date | product, criterion value, cumulative %, class, previous class | `AbcClassification` | ABC classification report (SSRS) | MIBC result list |
| Tag status / missing tags | list inquiry | tag range, plan | tag number, status, product, qty, entered by | `CountTag` | Tag counting journal | n/a |
| Transfer order overview | operational report | legal entity, date range, status | orders by status, late shipments, late receipts | `TransferOrder` | SSRS Transfer order overview | ME2N + VL06I inbound delivery monitor |

### 3.16.15 Printed and electronic documents

| Document | Trigger | Layout elements (header / line / footer) | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Inventory journal posting list | journal POSTED (on demand) | header: journal number, type, posting date, posted by, voucher; lines: product, dimensions, qty, unit, cost; footer: totals, offset account | PDF (A4/Letter), CSV | journal number `INVJ-` | Accounting record; retain with ledger vouchers (10 years DE/FR, 7 years US) |
| Count sheet / count list | count sheet generated | header: plan, sheet number, warehouse, counter, generated at; lines: location, product, batch/serial, expected (blank when blind), counted; footer: signature line | PDF, PWA screen | sheet number `CNT-` | Physical inventory evidence (HGB §240/§257, French CGI annual inventory); retain 10 years |
| Count tags | tag range generated | tag number barcode (Code 128), warehouse, range id, product/qty entry boxes | PDF (tag strips), ZPL | `TAG-` | Same as count sheet |
| Transfer shipping document (packing slip) | transfer order `:ship` | header: transfer number, from/to/transit warehouse, ship date, carrier, delivery terms; lines: product, qty, batch/serial, weight; footer: package count, signatures | PDF, email; EDI DESADV via wave 35 hook | shipment number `TRS-` | Transport document (CMR-like data for cross-border; Intrastat source, see 3.16.20) |
| Transfer receiving document | transfer order `:receive` | header: transfer number, receipt date, receiver; lines: shipped qty, received qty, variance, location | PDF | receipt number `TRR-` | Goods receipt evidence; retain 10 years |
| Consignment replenishment order | order confirmed | header: vendor, warehouse, requested date; lines: product, qty, consignment price | PDF, email, portal (wave 20) | `CRO-` | Commercial document; retain 10 years |
| Batch / serial / location label | registration or on demand | product, batch, expiry, serial, GS1-128 barcode (AI 01/10/17/21) | ZPL (4×6 in, 100×150 mm), PDF | none | GS1 compliance where required (FMD serial for pharma, wave 33) |
| Blocking / quarantine notice | block created | product, dims, qty, reason, expected release, approver | email (wave 4 notifications) | none | none |

### 3.16.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Inventory-to-ledger reconciliation (FR-16.12) | daily 02:00 tenant local | legal entity × inventory account | run id = (entity, date); recomputes fully, upserts result rows | < 5 min per 1M transactions | retry 3× with 10 min backoff; then alert | diff ≠ 0 pages inventory accountant; missed run ticket |
| On-hand rebuild-and-compare | weekly Sunday 03:00 | tenant × product range (1,000 products per child workflow) | checkpoint per child; compare table keyed by run id | < 10 min per 1M transactions | child retry; whole run resumable from last child | any row diff pages engineering (indicates bug) |
| Count sheet generation per plan | plan cron (e.g., `0 6 * * 1-5`) | count plan | key = (plan id, run date); re-run returns existing sheets | < 30 s per 10k lines | alert plan owner | sheets generated = 0 for an active plan → warning |
| Reservation TTL expiry | every 5 min | tenant | selects `expires_at < now()`; release is idempotent | < 10 s | retry next tick | expired-but-active reservations > 100 → ticket |
| ABC classification recalculation (FR-16.19) | monthly, 1st 04:00 | legal entity × site | key = (entity, site, period); overwrites class with previous retained | < 2 min per 100k products | retry 3× | class churn > 30 % → warning to inventory manager |
| Ownership change batch (FR-16.17) | daily 23:00 | vendor × warehouse | key = (vendor, warehouse, date); converts consumption-driven and aged quantities once | < 5 min | failed vendor skipped and listed | lines failed > 0 → ticket |
| Transfer order batch ship / receive (from confirmed shipments or ASNs) | hourly | legal entity | key = (transfer line, step) | < 1 min per 1k lines | line-level errors reported, rest posted | error lines > 0 → ticket |
| Journal and projection cleanup | weekly Sunday 05:00 | tenant | deletes journal line drafts older than `journal_draft_retention_days` (90) and OnHand rows with all-zero buckets older than 30 days | < 2 min | skip and retry | none |
| Partition maintenance | monthly 1st 01:00 | tenant | creates next-month partition on InventoryTransaction when over the row threshold; idempotent DDL | < 1 min | page (future inserts would fail) | partition missing for next month → page |

### 3.16.17 Security duties and privileges

| Duty | Privileges (entry points: API paths / screens / jobs) | Segregation-of-duties conflicts | Default roles that include it | AX duty / SAP authorization reference |
|---|---|---|---|---|
| InventoryInquire | GET /inventory/onhand, /transactions, /reservations; all inquiries in 3.16.14; workspace tiles | none | Inventory clerk, Warehouse supervisor, Inventory accountant, Buyer, Planner | AX "Inquire into inventory status" (InventOnhandInquire); SAP M_MSEG_LGO display, S_ALR stock reports |
| InventoryJournalMaintain | POST /inventory/journals, lines:bulk, :validate; Inventory journal screen (create/edit/submit) | with InventoryJournalPost when `journal_names.posting_restriction=SEGREGATED` | Inventory clerk | AX "Maintain inventory journals" (InventJournalMaintain); SAP M_MSEG_BWA activity 01 |
| InventoryJournalPost | POST /inventory/journals/{id}:post; approval task | with InventoryJournalMaintain (same journal), with InventorySetupMaintain | Warehouse supervisor, Inventory accountant | AX "Post inventory journals"; SAP M_MSEG_BWA activity 02 per movement type |
| InventoryReserve | POST/DELETE /inventory/reservations; reservation dialog | none | Inventory clerk, Order processor (w27), Production planner (w21) | AX "Maintain inventory reservations"; SAP M_MRES_BWA |
| InventoryBlock | POST /inventory/blockings, status change; blocking screen; quarantine disposition | with CycleCountPost for the same dimension set (hides shrinkage) | Quality inspector (w33), Warehouse supervisor | AX "Maintain inventory blocking"; SAP M_MSEG_BWA for 343/344, QM stock |
| CycleCountMaintain | count-plans CRUD, :generate; count sheet entry (desktop/PWA); tag entry | with CycleCountApprove | Inventory clerk, Counter | AX "Maintain counting journals"; SAP M_ISEG_WIB (PI count) |
| CycleCountApprove | approve sheet lines above tolerance | with CycleCountMaintain, CycleCountPost (when policy `four_eyes_count=true`) | Warehouse supervisor | AX "Approve counting variances"; SAP M_ISEG_WDB (post differences) |
| CycleCountPost | POST /count-sheets/{id}:post; tag transfer to journal | with CycleCountMaintain under four-eyes | Warehouse supervisor, Inventory accountant | AX "Post counting journals"; SAP MI07 M_ISEG_WDB |
| TransferOrderMaintain | transfer orders CRUD, :cancel, delete; transfer order screen | none | Inventory clerk, Planner | AX "Maintain transfer orders" (InventTransferOrderMaintain); SAP M_BEST_BSA document type UB |
| TransferOrderPost | :ship, :receive; batch ship/receive job | with TransferOrderMaintain when `transfer_four_eyes=true` | Warehouse supervisor | AX "Ship and receive transfer orders"; SAP M_MSEG_BWA 641/101, V_VLIK |
| ConsignmentMaintain | consignment replenishment orders, OWNERSHIP_CHANGE journals, ownership change batch | with VendorInvoicePost (w18) for the same vendor | Inventory clerk, Buyer | D365 "Maintain consignment inventory"; SAP M_BEST_WRK item cat K, MRKO |
| InventoryAdmin | /internal/onhand:rebuild, partition maintenance, reconciliation re-run | with InventoryJournalPost | Inventory system administrator | AX "Maintain inventory parameters" + batch; SAP S_BTCH_ADM, MB5L |
| InventorySetupMaintain | parameters in 3.16.13, journal names, posting profiles, status transitions, ABC setup | with InventoryJournalPost, TransferOrderPost | Inventory system administrator, Inventory accountant (posting profiles only) | AX "Maintain inventory setup" (InventParameters); SAP SPRO MM-IM customizing (S_TABU_DIS) |

### 3.16.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| INV-NEG-001 | error | Issue of {qty} {unit} would make available physical stock negative for {product} at {dims} (shortfall {shortfall}) | Item model group forbids physical negative | Receive stock, reduce quantity or change item model group policy | FR-16.4, BR-16.4 |
| INV-NEG-002 | error | Issue would make financial (ordered) stock negative for {product} at {dims} | Item model group forbids financial negative | Confirm the expected receipt first or allow financial negative | FR-16.4 |
| INV-SER-001 | error | Serial {serial} of {product} is already in physical stock at {warehouse} | Duplicate serial receipt | Correct the serial or issue the existing one first | FR-16.5, BR-16.9 |
| INV-SER-002 | error | Serial-tracked product {product} requires quantity ±1 per transaction | Quantity not ±1 | Split the line into one row per serial | BR-16.9 |
| INV-UOM-002 | error | Quantity {entered_qty} {entered_unit} converts to {qty} with residual beyond {decimals} decimals | Conversion residual | Enter a quantity that converts exactly or adjust unit decimals | FR-16.10 |
| INV-DIM-001 | error | Dimension {dimension} is mandatory for {product} | Storage/tracking dimension group marks dimension mandatory | Fill the dimension | FR-16.5 |
| INV-PER-001 | error | Physical date {date} is in a closed inventory period and backdated posting is not allowed | BR-16.6 | Change the physical date or allow backdated posting on the item model group | BR-16.6 |
| INV-RES-001 | error | Only {available} of {requested} {unit} available to reserve for {product} | Over-reservation | Reserve the partial quantity returned or wait for receipts | FR-16.7 |
| INV-RES-002 | warning | No eligible batch found for FEFO reservation (expiry margin {days} days) | All batches blocked, quarantined or expiring | Review blocked batches or reduce the shelf-life margin | FR-16.7 |
| INV-BLK-001 | error | Quarantined quantity requires a disposition (release, scrap, return) before exit | Missing disposition | Choose a disposition | FR-16.8 |
| INV-JNL-001 | error | Journal exceeds {max} lines | `journal_max_lines` | Split the journal | FR-16.6 |
| INV-JNL-002 | error | Movement journal line {line} requires an offset account | Missing offset | Enter an offset account valid for the account structure | FR-16.6 |
| INV-JNL-003 | error | Journal {journal} is already posting (workflow {id}) | Concurrent post | Wait for the workflow to finish | BR-16.11 |
| INV-CNT-001 | warning | Variance {pct} % exceeds tolerance {tol} %; supervisor approval required | FR-16.9 | Request approval from a user with CycleCountApprove | FR-16.9 |
| INV-CNT-002 | error | Dimension set {dims} is locked by open count sheet {sheet} | Count freeze | Post or cancel the sheet, or allow movement with recount on the plan | FR-16.9 |
| INV-TRF-001 | error | Shipping warehouse {warehouse} has no transit warehouse configured | `transit_warehouse_id` missing | Assign a transit warehouse or disable `transfer_use_transit` | FR-16.15 |
| INV-TRF-002 | error | Received quantity {qty} exceeds shipped quantity {shipped} plus tolerance {pct} % | Over-receipt on transfer | Correct the quantity or raise the tolerance on the line | FR-16.15 |
| INV-TRF-003 | error | Transfer order {number} cannot be cancelled: {qty} in transit | BR-16.13 | Receive the in-transit quantity or create a reverse transfer | BR-16.13 |
| INV-OWN-001 | error | Vendor-owned stock cannot be financially posted or valued | Attempt to post value on `owner ≠ legal entity` | Post an ownership change journal first | BR-16.14 |
| INV-OWN-002 | error | Ownership change of {qty} exceeds vendor-owned quantity {available} at {dims} | BR-16.14 | Reduce the quantity | FR-16.17 |
| INV-TAG-001 | error | Tag {tag} has already been used | Duplicate tag entry | Void the earlier entry or use a different tag | FR-16.18 |
| INV-TAG-002 | error | {n} tags are still open; mark them MISSING or enter them before transferring | BR-16.15 | Resolve open tags | FR-16.18 |
| INV-ABC-001 | error | ABC thresholds must sum to 100 (got {sum}) | Setup error | Correct the thresholds | FR-16.19 |
| INV-SEG-001 | error | Stock with segment {segment} cannot be issued to demand with segment {demand_segment} | BR-16.17 | Transfer between segments or enable fallback | FR-16.20 |
| INV-LCK-001 | error | Could not lock on-hand row for {product} at {dims} within {ms} ms | Lock contention | Retry; the client should back off 200 ms × attempt | BR-16.4 |

### 3.16.19 Performance budgets and capacity

| Operation | Volume assumption (per tenant per day / peak per minute) | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Journal post (1,000 lines) | 500 journals / 20 per min | 0.8 s / 2 s / 4 s | 2,000 lines/s per pod | InventoryTransaction ≈ 420 B/row + 280 B indexes; 50M rows ≈ 35 GB | (tenant, product, dimension_set, physical_date) B-tree; partial index on open statuses; monthly range partitions over 20M rows | Temporal task queue absorbs backlog; HPA 3→12 pods; posting latency rises to ≤ 10 s p95, no rejections |
| Internal `transactions:apply` (waves 17–20) | 200k steps / 1,500 per min | 15 ms / 40 ms / 80 ms | 1,500 rps sustained | counted above | OnHand PK (tenant, product, dimension_set); `FOR UPDATE` per row | 429 with `Retry-After` beyond 2,500 rps; callers (receiving, PO confirm) retry with jitter |
| On-hand query (≤ 10k rows aggregated) | 50k queries / 600 per min | 12 ms / 50 ms / 120 ms | 1,000 rps | OnHand ≈ 300 B/row; ≤ 2M rows per tenant | clustered by (tenant, product); covering index on (tenant, site, warehouse, product) | Reads shift to the Postgres read replica (`inventory-db-ro`); stale-by ≤ 2 s shown in UI |
| Reservation (policy FEFO over 100 batches) | 30k / 300 per min | 25 ms / 80 ms / 150 ms | 500 rps | Reservation ≈ 200 B/row | index (product, dimension_set, status) | Serializes per product; p95 ≤ 300 ms |
| Count sheet generation (10k lines) | 20 sheets | 5 s / 30 s / 60 s | n/a | CountSheet lines ≈ 250 B/row | (plan_id, status) | Queued per plan; no user-facing path |
| Rebuild-and-compare (1M transactions) | weekly | 6 min / 10 min / 15 min | 2,000 rows/s | temp compare table dropped after run | parallel by product range, 32 children | Runs on replica snapshot; skipped if lag > 60 s and retried next hour |
| Reconciliation (per entity) | daily | 1 min / 5 min / 8 min | n/a | result rows ≈ 1 KB × accounts × days | aggregate on (ledger_entry_id) index | Off-peak only; alert if > 15 min |
| Transfer order ship / receive (100 lines) | 2,000 orders / 50 per min | 0.4 s / 1.2 s / 2.5 s | 200 orders/min | TransferOrder ≈ 1.5 KB + lines 300 B | (from_warehouse, status), (to_warehouse, status) | Same as journal post (paired transactions) |
| Outbox event publish | 300k events / 3,000 per min | lag 1 s / 5 s / 10 s | 5,000 events/s per Debezium connector | Kafka retention 7 days | partition by tenant_id + product_id | Lag alert at 60 s; consumers idempotent on transaction id |

### 3.16.20 Localization and compliance hooks

| Concern | Varies by | Country packs touching it (wave 48) | Extension point used |
|---|---|---|---|
| Statutory stock ledger / inventory book (Italy libro giornale di magazzino, Portugal/Spain stock register, India stock register, Russia stock card) | Country reporting rules | IT, ES, PT, IN, RU, TR | Report projection `inventory.transaction_ledger` (wave 15 report definitions) with country layout; SAP S_P00_07000139 / AX country-specific inventory reports |
| Annual physical inventory obligation and evidence retention | HGB §240/§257 (10 years), French CGI, US GAAP evidence | DE, FR, US | `CountSheet` retention policy from wave 5 audit retention; immutable PDF archive in object store |
| Electronic signature on count approval, blocking release and quarantine disposition | FDA 21 CFR Part 11, EU GMP Annex 11 | US (pharma), EU (pharma) | Wave 4 approval step with `signature_required=true`; wave 33 extends to quality orders |
| Batch and serial regulatory tracing (EU FMD, DSCSA, food lot tracing FSMA 204) | Industry regulation | EU, US | GS1 label template (3.16.15) and batch trace inquiry (3.16.14); key data elements stored on batch (wave 8) |
| Intrastat and cross-border transfers (intra-EU transfer orders between warehouses in different member states, STO cross-border) | EU Intrastat thresholds | All EU packs | `transfer.received` event carries country of dispatch/arrival, commodity code (wave 8), net mass; wave 48 Intrastat consumer |
| Consignment stock VAT and call-off stock simplification (EU "quick fixes" 2020) | EU VAT directive Art. 17a | EU packs | `ownership.changed` event feeds wave 13 tax engine for supply point; call-off register report hook |
| Units of measure, decimals and quantity rounding | Metric vs imperial; product decimals | n/a (product-level) | Wave 6 UoM service; BR-16.5 rounding |
| Currency rounding of `posted_value` | Accounting currency minor unit (JPY 0, KWD 3) | n/a | Wave 12 rounding rules |
| Calendar and time zone for cron jobs and physical date default | Tenant/site local time, local holidays | All | Wave 6 site calendar; jobs scheduled in site time zone |
| Language of journal types, statuses, reason codes, PWA count screen | User language | n/a | Wave 3 metadata labels (en, de, fr, es in v1) |
| Privacy of counter identity (`counted_by`) and PWA device identifiers | GDPR, state privacy laws | EU, US-CA | Wave 2 pseudonymization hook on worker references after retention |

### 3.16.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.2.1 — Vendor → Consignment (D365 consignment replenishment orders) [#2] | GAP | ADDED as FR-16.17 | `ConsignmentReplenishmentOrder`; settlement invoice in wave 18 FR-18.16 |
| AX 12.2.3 — Consignment → Consignment replenishment order processing [#5] | GAP | ADDED as FR-16.17 | Replenishment order receive; ownership change batch (3.16.16) |
| AX 12.2.4 — Vendor → Vendor consignment setup (consignment parameters, ownership change journal names) [#7] | GAP | ADDED as FR-16.17 | Parameters `owner_dimension_active`, `auto_ownership_change_on_issue`, journal name OWNERSHIP_CHANGE (3.16.13) |
| AX 12.14.1 — Journals → Inventory ownership change journal [#44] | GAP | ADDED as FR-16.17 | Journal type OWNERSHIP_CHANGE |
| AX 12.14.2 — SSRS: Consignment inventory reports [#45] | GAP | ADDED as FR-16.17 | "Consignment stock by vendor" report (3.16.14) |
| AX 12.14.3 — Inventory → Consignment: ownership change batch [#46] | GAP | ADDED as FR-16.17 | Ownership change batch job (3.16.16) |
| AX 12.14.4 — Inventory → Consignment → Consignment parameters [#47] | GAP | ADDED as FR-16.17 | 3.16.13 rows |
| AX 12.16.1 — Consignment (link) [#57] | GAP | ADDED as FR-16.17 | Procurement entry point to replenishment orders |
| AX 12.39.1 — Vendor → Consignment inventory view (on-hand, replenishment orders) [#108] | GAP | DEFERRED to wave 20 | Portal consignment view is FR-20.22 (data from FR-16.17) |
| AX 12.14.1 — Transfer orders (list page) [#119] | PARTIAL | ADDED as FR-16.15 | `TransferOrder` with in-transit warehouse |
| AX 12.14.1 — Transfer order details [#120] | PARTIAL | ADDED as FR-16.15 | Header/lines, ship, receive, underdelivery |
| AX 12.14.1 — Journals → Tag counting journal [#121] | PARTIAL | ADDED as FR-16.18 | Two-step tag counting |
| AX 12.14.1 — Inventory → ABC classification (on item) [#123] | PARTIAL | ADDED as FR-16.19 | Class stored per product × site |
| AX 12.14.2 — Transfer order inquiries (lines, shipped/received) [#125] | PARTIAL | ADDED as FR-16.15 | 3.16.14 "Transfer order lines" |
| AX 12.14.2 — SSRS: Transfer order (shipping document, receiving document, overview) [#127] | PARTIAL | ADDED as FR-16.15 | 3.16.15 shipping/receiving documents; 3.16.14 overview |
| AX 12.14.2 — SSRS: ABC classification report [#129] | PARTIAL | ADDED as FR-16.19 | 3.16.14 |
| AX 12.14.2 — SSRS: Packing slip (inventory transfer) [#130] | PARTIAL | ADDED as FR-16.15 | Transfer shipping document (3.16.15) |
| AX 12.14.3 — Tag counting → Create tag counting / Transfer to counting journal [#131] | PARTIAL | ADDED as FR-16.18 | Tag range generation and transfer |
| AX 12.14.3 — Transfer orders → Ship / Receive (batch) / Transfer order picking release [#132] | PARTIAL | ADDED as FR-16.15 | Batch ship/receive job (3.16.16); picking release event for wave 31 |
| AX 12.14.3 — Transfer orders → Delete transfer orders [#133] | PARTIAL | ADDED as FR-16.15 | Delete of CREATED orders without postings |
| AX 12.14.3 — ABC classification (periodic calculation) [#134] | PARTIAL | ADDED as FR-16.19 | Monthly job (3.16.16) |
| AX 12.14.4 — Inventory and warehouse management parameters — Transfer orders [#136] | PARTIAL | ADDED as FR-16.15 | `transfer_use_transit`, tolerances (3.16.13) |
| AX 12.14.4 — Inventory → ABC classification setup [#138] | PARTIAL | ADDED as FR-16.19 | `abc_thresholds`, criterion, lookback (3.16.13) |
| AX 12.14.4 — Inventory → Transfer orders → Transfer order parameters, In-transit warehouses [#139] | PARTIAL | ADDED as FR-16.15 | `transit_warehouse_id` per shipping warehouse |
| SAP 13.12 — ME21N item category K (consignment) [#126] | PARTIAL-16 | ADDED as FR-16.17 | Replenishment order replaces consignment PO; price from trade agreement `consignment=true` (wave 17) |
| SAP 13.12 — ME21N item category U / UB STO / NB cross-company STO [#127] | PARTIAL-16 | ADDED as FR-16.15 | Intra-company STO full; cross-company pricing and intercompany invoice in wave 40 |
| SAP 13.12 — MRKO consignment settlement [#134] | PARTIAL-16 | ADDED as FR-16.17 | Ownership change emits event; invoice creation in wave 18 FR-18.16 |
| SAP 13.12 — OMGN set up stock transport order [#136] | PARTIAL-16 | ADDED as FR-16.15 | `transfer_use_transit`, transit warehouse, tolerances |
| SAP 13.13 — MB53 / MB54 plant availability / consignment stock [#140] | PARTIAL-16 | ADDED as FR-16.17 | On-hand inquiry with `owner`; consignment report (3.16.14) |
| SAP 13.13 — MIK1 / MIQ1 / MIM1 PI for consignment / project stock / RTP [#142] | PARTIAL-16 | ADDED as FR-16.17, FR-16.20 | Count plans filter by owner and segment; returnable transport packaging OUT-OF-SCOPE (handled as ordinary product with owner dimension in v1) |
| SAP 13.13 — MIS1 / MIS2 inventory sampling [#144] | GAP (propose 16) | OUT-OF-SCOPE statistical sampling with extrapolation is not provided in v1; random-sample counting without extrapolation exists via CountPlan `sample_pct` | Revisit if a customer requires sampling-based book adjustment |
| SAP 13.13 — Special stock: sales order stock (E), project stock (Q) [#145] | PARTIAL-16 | ADDED as FR-16.20 | `segment` dimension, valuated flag |
| SAP 13.13 — Stock in transit (MB5T; S/4 stock in transit for STO) [#146] | PARTIAL-16 | ADDED as FR-16.15, FR-16.16 | Transit warehouse dimension and inquiry |
| SAP 13.13 — MB5T stock in transit report [#148] | PARTIAL-16 | ADDED as FR-16.16 | 3.16.14 |
| SAP 13.13 — MB54 / MB58 / MBLB consignment / customer consignment / subcontractor stock [#149] | PARTIAL-16 | ADDED as FR-16.17 | MB54 and MB58 reports added; subcontractor stock (MBLB) DEFERRED to wave 21 subcontracting |
| SAP 13.13 — MBBS valuated special stock [#150] | PARTIAL-16 | ADDED as FR-16.20 | Inquiry in 3.16.14; valued grouping in wave 20 value report |
| SAP 13.13 — MRKO consignment and pipeline settlement [#151] | PARTIAL-16 | ADDED as FR-16.17 | Pipeline material (utilities without stock) OUT-OF-SCOPE; settlement in wave 18 |
| SAP 13.13 — Special stock settings (consignment price determination, pipeline) [#153] | PARTIAL-16 | ADDED as FR-16.17 | Consignment price via trade agreement flag; pipeline OUT-OF-SCOPE |
| SAP 13.13 — OSPX / OSPW stock determination strategies [#154] | PARTIAL-16 | ADDED as FR-16.17 | `stock_determination` OWN_FIRST / CONSIGNMENT_FIRST per warehouse |
| SAP 13.14 — MRKO consignment settlement (MM-IV) [#155] | PARTIAL-16 | ADDED as FR-16.17 | Invoice side in wave 18 FR-18.16 |
| SAP 13.14 — MRKO scheduled [#158] | PARTIAL-16 | ADDED as FR-16.17 | Ownership change batch (3.16.16) + wave 18 settlement job |
| SAP 13.17 — VL10 → STO deliveries (NL/NLCC) [#203] | PARTIAL-16 | ADDED as FR-16.15 | `TransferShipment` with shipping document; carrier/freight in wave 34 |

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
| Scheduling agreements and releases | Purchase agreement with delivery schedule lines (partial); D365 no FRC/JIT releases | Scheduling agreement ME31L/ME38 delivery schedule, release creation profile (T163M), firm/trade-off zones, FRC/JIT releases ME84, release documentation ME85/ME86, Monitor SA Releases | `SchedulingAgreement` + `ReleaseSchedule` + `Release` (FRC/JIT) with cumulative quantities (FR-17.15) | full (EDI DELFOR/DELJIT transport in wave 35) |
| Policy framework | Policy framework (SysPolicy): Purchasing policies, Expense policies, Audit policies, Vendor invoice policies, Signing limit policies scoped by organization hierarchy purpose | Release strategy classification; no generic policy framework | `Policy` / `PolicyRule` engine scoped by org hierarchy purpose (FR-17.16); reused by waves 43, 48 | improved |
| Signing limits | Signing limit policies, default signing limits by job/compensation level, worker signing limit requests, recertification | Release codes per user (CL24N classification by amount) | `SigningLimit` per worker with request workflow and recertification (FR-17.17) | full |
| Procurement catalog | Procurement catalogs (CatProcurementCatalog), catalog policy rule, catalog images, vendor catalog import (CMR XML), external catalogs (cXML/OCI punch-out) | Fiori self-service requisition with catalogs; OCI/cXML punch-out; SRM-MDM catalog | `ProcurementCatalog` with publication by org unit, images, vendor catalog import (FR-17.18); vendor upload via portal (wave 20 FR-20.19); punch-out in wave 35 | partial (punch-out in wave 35) |
| RFQ bidding (sealed bids, scoring) | RFQ types, scoring criteria/groups, reply templates, sealed bids, amendments, solicitation types (D365 public sector) | ME47 quotation, ME49 price comparison; weighted scoring only in Ariba Sourcing | `RfqType`, `ScoringCriterion`, sealed reply storage, amendments (FR-17.19) | full (reverse auctions OUT-OF-SCOPE) |
| Category setup depth | Procurement categories, category policy rules, category managers, category vendor assignment, category questionnaires | Material groups T023, source list ME01, purchasing group | `CategoryVendorAssignment`, `CategoryManager`, `CategoryQuestionnaire` (FR-17.20) | full |
| Invoicing plans | n/a (prepayments only) | Invoicing plan on PO item (periodic/partial), MRIS settlement run, invoicing plan types T163* | `InvoicingPlan` on PO line (FR-17.21); run in wave 18 FR-18.17 | full |
| Subsequent price change | n/a | MEI1/MEI2/MEI4 document index and automatic document adjustment; MRNB revaluation of invoiced items | `:reprice` run over open PO lines (FR-17.22); invoiced items via wave 18 FR-18.18 subsequent debit/credit | full |
| Service purchase orders and service specifications | Procurement category lines (no hierarchy) | ME21N item category D with service outline, value limits, contract limits; model service specifications ML10–ML12; standard service catalog; service conditions ML33–ML48, calculation schema MS0000 | Service lines with `ServiceOutline` hierarchy, limits, `ModelServiceSpecification` templates, service prices via TradeAgreement (FR-17.23) | full |
| Consignment price agreements | Trade agreement with consignment flag (D365) | Consignment info record (ME11 info category K), price determination for consignment | TradeAgreement `consignment=true` consumed by wave 16 ownership change (FR-17.24) | full |
| Legal contract authoring | n/a | SAP CLM / Ariba Contracts clause library, obligations | Not provided; agreements carry attachments, validity and expiry alerts | dropped-with-reason (3.17.21) |
| Configurable PO items | n/a | Configuration in purchasing (LO-VC configured items on PO) | Deferred to the wave that introduces the configurator (wave 24/27); PO line stores `configuration_id` placeholder | dropped-with-reason (3.17.21) |

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

- **FR-17.15 Scheduling agreements and releases** — Must. The system shall support a `SchedulingAgreement` (SAP ME31L type LP/LPA; AX agreement with delivery schedule) per vendor × product × site with target quantity, validity, price terms, `release_profile` (FRC forecast horizon days, JIT horizon days, firm zone days, trade-off zone days, release frequency), and a `ReleaseSchedule` of dated quantities maintained manually in this wave and by wave 22 MRP later.
  - A release run (manual `:release` or the job in 3.17.16; SAP ME84) snapshots the schedule into a numbered `Release` (kind FRC or JIT) carrying per-line cumulative released quantity, cumulative received quantity (from wave 18 receipts) and the last receipt reference; the release document is rendered (3.17.15) and emitted as `dev.passage.procurement.sa.release.created.v1` for wave 35 EDI DELFOR/DELJIT.
  - Schedule lines inside the firm zone cannot be reduced without a reason code; lines in the trade-off zone warn; beyond it they are forecast only.
  - Each schedule line writes an ORDERED inventory transaction (wave 16) like a PO delivery schedule; receipts reference the schedule line.
  - Inquiry "Monitor scheduling agreement releases" lists agreements with open cumulative quantity, last release date, next release due, and release documentation per release number (SAP ME85/ME86).

- **FR-17.16 Policy framework** — Must. The system shall implement a reusable `Policy` engine (AX SysPolicy framework): policy types `PURCHASING`, `SIGNING_LIMIT`, `VENDOR_INVOICE` (consumed by wave 18), `EXPENSE` (wave 43), `AUDIT` (wave 48); each policy has an organization hierarchy purpose (wave 6), effective dates, and ordered `PolicyRule` rows of a rule type (e.g., purchasing: catalog policy, category access, purchase agreement policy, PO creation and demand consolidation, requisition control, RFQ control) with a JSON body validated by a per-rule-type schema.
  - Resolution: the most specific organization node in the hierarchy wins; rule evaluation is exposed as `POST /policies:evaluate` (pure, used by FR-17.2 and the policy tester).
  - Policies are versioned; the version at document submit time governs the instance; a "re-evaluate policies on open requisitions" job exists (AX Procurement → Policy evaluation).
  - Reports: policy rules per hierarchy node (AX SSRS Policies report) in 3.17.14.

- **FR-17.17 Signing limits** — Must. The system shall maintain `SigningLimit` per worker (wave 7/42 worker) with document types REQUISITION, PURCHASE_ORDER, INVOICE (wave 18), EXPENSE (wave 43), amount per currency, `spending_limit` and `approval_limit`, derived from `DefaultSigningLimit` rows by job and compensation level (policy type SIGNING_LIMIT) with worker-level overrides approved through a `SigningLimitRequest` workflow (wave 4), annual recertification job, and a signing limits report; FR-17.2 approval routing uses `approval_limit` so an approver whose limit is below the document amount is skipped to the next level.

- **FR-17.18 Procurement catalog** — Must. The system shall provide `ProcurementCatalog` objects (name, org units published to, validity, category navigation tree from the wave 8 procurement hierarchy, product entries with images, vendor, price source, unit, lead time, `preferred` flag) with publication status DRAFT/PUBLISHED/RETIRED; the requisition line picker searches published catalogs for the requester's org unit (catalog policy rule in FR-17.16), shows images and prices, and marks non-catalog lines.
  - Vendor catalog import: `POST /catalogs/{id}/vendor-import` accepts CSV/JSON (columns vendor product number, name, category, unit, price, currency, validity, image URL, lead time) in a staging table with buyer review (accept/reject per row) before publication; the same endpoint is used by the wave 20 supplier portal upload (FR-20.19); AX CMR XML is accepted through a mapping in the wave 10 migration framework.
  - External catalog punch-out (cXML/OCI) is a wave 35 integration hub capability; the requisition line carries `external_catalog_ref` reserved for it.

- **FR-17.19 RFQ types, sealed bids and scoring** — Should. The system shall add `RfqType` (sealed yes/no, amendment allowed, default scoring group, reply template), sealed bidding (replies encrypted at rest with a per-RFQ key and unreadable to buyers until `reply_deadline`, opening logged with user and time), `ScoringCriterion` groups (criterion, weight, scale 1–10) with buyer scoring per reply and a weighted total used by the compare view's `WEIGHTED_SCORE` ranking rule, amendments/addenda (new RFQ version re-sent to invited vendors with acknowledgement), and reply templates (questions the vendor must answer, rendered in the wave 20 portal).

- **FR-17.20 Category vendor assignment, category managers and questionnaires** — Should. The system shall allow per procurement category: `CategoryManager` (worker), `CategoryVendorAssignment` (vendor, status APPROVED/PROVISIONAL/BLOCKED, validity) enforced by the category access rule (`PRC-CAT-002` when a requisition or PO line uses a non-assigned vendor and the rule is HARD), and `CategoryQuestionnaire` (questions attached to a category, answered on the requisition line and shown to approvers).

- **FR-17.21 Invoicing plans on purchase order lines** — Should. The system shall allow a PO line (service or category line) to carry an `InvoicingPlan` of type PERIODIC (start, end, frequency MONTHLY/QUARTERLY, amount per period, billing day) or PARTIAL (milestone rows with date, percent or amount), with `settle_without_receipt=true` so that wave 18 FR-18.17 generates the invoice (SAP MRIS) on each due date without a product receipt; Σ partial percents = 100 and the plan total equals the line amount.

- **FR-17.22 Subsequent price change on open purchase orders** — Should. The system shall provide `POST /purchase-orders:reprice` (SAP MEI1/MEI2 document index; AX has no equivalent) selecting open, un-invoiced PO lines whose `price_source` is a trade agreement or agreement line changed since confirmation, previewing old/new price and amount deltas, and applying the change as a PO version (FR-17.8) with reapproval per policy; lines already received or invoiced at the old price are passed to wave 18 FR-18.18 for a subsequent debit/credit when `reprice_invoiced=true`.

- **FR-17.23 Service purchase orders, service outlines and model service specifications** — Must. The system shall support PO lines of kind SERVICE (SAP item category D; AX procurement category line) with an optional `ServiceOutline` hierarchy (outline levels with description, and service items with service product or free text, quantity, unit, unit price), `value_limit` and `expected_value` for unplanned services (SAP limit items), an optional reference to an agreement line whose remaining value constrains the limit (`contract_limit`), and reusable `ModelServiceSpecification` templates (SAP ML10–ML12, standard service catalog) copied into a PO or RFQ line.
  - Service prices resolve through FR-17.6 with `item_code=PRODUCT` on service products (SAP service conditions ML33–ML48, calculation schema MS0000); source determination picks an agreement for the service product and site when one exists.
  - Wave 18 FR-18.14 receives against service lines through service entry sheets.

- **FR-17.24 Consignment price agreements** — Should. The system shall allow a TradeAgreement row with `consignment=true` (SAP consignment info record; D365 consignment trade agreement) used by wave 16 FR-16.17 ownership changes and ignored by PO pricing unless the PO line is a consignment replenishment order line.

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
| `dev.passage.procurement.sa.release.created.v1` | scheduling agreement release run | agreement_id, release_number, kind (FRC/JIT), vendor_id, lines (product, schedule dates, qty, cumulative released, cumulative received) | EDI (w35 DELFOR/DELJIT), supplier-portal (w20), planning (w22) |
| `dev.passage.procurement.policy.changed.v1` / `signinglimit.changed.v1` | policy version activated / signing limit approved | policy_id, type, version, hierarchy purpose; worker_id, document type, limits | procurement (re-evaluation), expense (w43), audit (w48) |
| `dev.passage.procurement.po.repriced.v1` | `:reprice` applied | po_id, version_no, lines (old price, new price, delta), invoiced lines passed to payables | payables (w18 subsequent debit/credit), analytics |

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
- **BR-17.13** Scheduling agreement zones: a schedule line with date ≤ today + `firm_zone_days` is FIRM (reduction requires reason code and emits a change release), ≤ today + `tradeoff_zone_days` is TRADEOFF (reduction warns), otherwise FORECAST; cumulative released quantity on a release never decreases unless a cumulative reset (new agreement year) is recorded explicitly.
- **BR-17.14** Policy resolution walks the organization hierarchy of the policy type's purpose from the document's operating unit upward; the first node with an active policy version wins; if no node has one, the legal-entity default policy applies; absence of a rule type in the winning policy means "no restriction".
- **BR-17.15** Signing limits are evaluated in the document currency converted to the worker's limit currency at the document date rate; an approver whose `approval_limit < amount` is skipped and the chain continues upward; a requester whose `spending_limit < amount` triggers an additional approval level rather than a rejection.
- **BR-17.16** Sealed RFQ replies cannot be read, scored or compared before `reply_deadline`; opening early requires two users with RFQAward and is logged as a sealed-bid exception; a late reply to a sealed RFQ is rejected (not merely flagged).
- **BR-17.17** Invoicing plan totals: PERIODIC plan total = periods × amount and must equal the line amount; PARTIAL plan percents sum to 100; changing the line amount after plan generation reproportions ungenerated plan rows only.
- **BR-17.18** Reprice selects only lines with `invoiced_qty = 0` by default; lines with receipts but no invoice reprice the GR/IR expectation (wave 18 GrirOpenItem keeps the receipt-side amount; difference flows at invoice); the run never changes manually priced lines (`price_source = MANUAL`).
- **BR-17.19** Service outline items sum to the outline parent; `value_limit` applies to the sum of unplanned service entry sheet amounts (wave 18) and `contract_limit` to the referenced agreement line's remaining value; exceeding either rejects the entry sheet with `PRC-SRV-003`.

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
- **AC-17.11** (FR-17.15) Given a scheduling agreement with schedule lines of 100 EA weekly for 12 weeks, firm zone 14 days, when a JIT release is created on day 0 and 100 EA are received against week 1, then release 1 shows cumulative released 1,200 and cumulative received 0, release 2 (run on day 8) shows cumulative received 100, and reducing week-2 quantity to 50 requires a reason code.
- **AC-17.12** (FR-17.16, FR-17.17) Given a purchasing policy on operating unit "Plant A" with category access rule blocking category "IT hardware" for requesters outside IT, and a requester with approval limit 5,000 USD, when a 7,000 USD requisition for IT hardware is submitted from Plant A by a non-IT requester, then it is rejected with `PRC-CAT-002`; submitted by an IT requester, it routes past the requester's manager (limit 5,000) to the next approver with limit ≥ 7,000.
- **AC-17.13** (FR-17.18) Given a published catalog for org unit "Finance" with 200 items and a vendor import file of 50 rows of which 3 lack a unit, when imported, then 47 rows stage as ACCEPTABLE and 3 as ERROR with reason, and after buyer acceptance the 47 appear in the catalog and in the requisition picker for Finance requesters only.
- **AC-17.14** (FR-17.19) Given a sealed RFQ with deadline 2026-11-01T17:00Z and 3 replies, when a buyer opens the compare view at 2026-11-01T16:59Z, then no reply prices are returned (403 `PRC-RFQ-004`); at 17:00:01Z all three are visible and the opening is logged.
- **AC-17.15** (FR-17.21) Given a service PO line of 12,000.00 EUR with a PERIODIC plan monthly 1,000.00 from 2027-01-01, when generated, then 12 plan rows exist with `due_date` on the billing day and Σ amount = 12,000.00; changing the line to 13,200.00 before any invoice reproportions all 12 rows to 1,100.00.
- **AC-17.16** (FR-17.22) Given 100 open PO lines priced from trade agreement T1 and T1's price changed from 10.00 to 9.50, when `:reprice` previews and applies, then 100 PO versions are created with patch `/lines/n/unit_price` 10.00 → 9.50, lines with invoices are excluded from the version and listed for wave 18 subsequent credit, and manually priced lines are untouched.
- **AC-17.17** (FR-17.23) Given a service PO line with outline "Maintenance" containing 3 service items and `value_limit` 2,000.00 for unplanned work, when wave 18 service entry sheets total 1,900.00 unplanned, then a further 200.00 unplanned entry is rejected with `PRC-SRV-003`.

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

### 3.17.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `change_management_enabled` / `change_management_vendor_override` | legal entity / vendor | bool | true / inherit | true/false/inherit | FR-17.8 versioning and re-approval | Procurement and sourcing parameters → Purchase order change management; Vendor → "Activate change management" | OMFI/OMFJ version management per document type (T160VS) |
| `reapproval_rules` | legal entity (policy) | rule set | any price or qty increase > 5 %, new line | DSL (field, operator, threshold) | FR-17.8 | Purchase order approval workflow conditions | Release strategy re-triggering (CEKKO changes) |
| `requisition_required_categories` | legal entity | category list | none | procurement categories | FR-17.13 PO creation requires requisition | Purchasing policy rule "Purchase order creation and demand consolidation" | n/a (PR-to-PO link enforced by T161 "PR required") |
| `po_required_before_invoice` | legal entity | bool | false | true/false | consumed by wave 18 expense invoices | Accounts payable parameters → "Require purchase order" | n/a |
| `consolidation_window_days` | legal entity | int | 7 | 1–60 | FR-17.3 grouping | Purchasing policy → Demand consolidation | ME59N grouping by delivery date (variant) |
| `approval_bands` | policy (PURCHASING) | table | 0–1k auto; 1k–10k manager; 10k–100k + category owner; >100k + CFO | amount band × category × org unit × role | FR-17.2 | Purchase requisition workflow conditions; purchasing policy | Release strategy CL24N characteristics (amount, material group) |
| `price_resolution_date` | legal entity | enum | ORDER_DATE | ORDER_DATE, DELIVERY_DATE | BR-17.3 | Procurement parameters → Prices → "Price date" | Pricing date control on vendor master (LFM1-MEPRF) |
| `tie_rule` | legal entity | enum | LOWEST_PRICE | LOWEST_PRICE, LATEST_FROM_DATE | FR-17.6 | Trade agreement "Find next" / search precedence | Access sequence exclusion flags |
| `rfq_late_policy` / `rfq_default_ranking` | legal entity | enum | FLAG / LOWEST_PRICE | FLAG, REJECT; LOWEST_PRICE, LEAD_TIME, WEIGHTED_SCORE | FR-17.4, BR-17.11 | RFQ parameters, bidding parameters | n/a (ME49 ranking manual) |
| `rfq_types` | legal entity | table | STANDARD, SEALED | sealed flag, amendments allowed, scoring group, reply template | FR-17.19 | RFQ types, solicitation types, scoring criteria/groups, reply templates | Document types for RFQ (OMEB) |
| `agreement_threshold_pct` / `agreement_expiry_days` | legal entity | numeric / int | 90 / 30 | 50–100 / 1–180 | FR-17.14 alerts | Purchase agreements → Agreement classifications (expiry) | ME3M validity monitoring (custom) |
| `charge_codes` / `auto_charges` | legal entity | table | seeded FREIGHT, HANDLING, CUSTOMS | code, calc, debit/credit types, accounts; auto charge by vendor/item group | FR-17.9 | Charges codes, Auto charges, Charges groups | Condition types FRA1/ZOA1, calculation schema RM0000 |
| `pdf_template_id` / `po_email_from` | legal entity | reference / email | default template / entity address | wave 15 templates | FR-17.12 | Form setup (purchase documents), Print management | NACE EF output type NEU, MEDRUCK form |
| `po_number_gap_free` | legal entity | bool | false | true/false | number sequence continuity (open question 2) | Number sequence "Continuous" | Number range internal, no gaps (T161 ext. numbering) |
| `sa_release_profile` | scheduling agreement (default per legal entity) | record | FRC 90 d / JIT 14 d / firm 14 d / trade-off 28 d / weekly | horizons in days; frequency | FR-17.15 | n/a | T163M release creation profile; firm zone / trade-off zone on SA |
| `policy_types` / `hierarchy_purpose_per_type` | tenant / legal entity | table | PURCHASING → "Procurement internal control"; SIGNING_LIMIT → "Signing limit" | wave 6 hierarchy purposes | FR-17.16 | Policy parameters (organization hierarchy purpose per policy type) | n/a |
| `default_signing_limits` | policy (SIGNING_LIMIT) | table | none | job, compensation level, document type, currency, spending/approval limit | FR-17.17 | Default signing limits (per job / compensation level), Signing limit parameters | Release codes per user |
| `signing_limit_recertification_months` | legal entity | int | 12 | 3–24 | FR-17.17 job | Signing limit request workflow (recertification) | n/a |
| `catalog_policy_rule` | policy (PURCHASING) | rule | catalog required for categories X | categories, org units, allow non-catalog | FR-17.18 | Catalog policy rule | n/a |
| `vendor_catalog_import_params` | legal entity | record | max 50,000 rows; image max 2 MB; currencies allowed | | FR-17.18 | Vendor catalog import parameters, Catalog images | n/a |
| `category_access_mode` | policy (PURCHASING) | enum | SOFT | HARD, SOFT, OFF | FR-17.20 `PRC-CAT-002` | Category access policy rule, Category vendor assignment | Source list "fixed/blocked" (ME01) |
| `invoicing_plan_types` | legal entity | table | MONTHLY, QUARTERLY, MILESTONE | type, frequency, billing day, settle without receipt | FR-17.21 | n/a | Invoicing plan types (T163*), OMEC |
| `reprice_invoiced` | legal entity | bool | false | true/false | FR-17.22 passes invoiced lines to wave 18 | n/a | MRNB revaluation activation |
| `service_limit_defaults` | legal entity | record | expected value = 0, limit = 0 (must be set), overall limit check HARD | | FR-17.23 | n/a | Limit item defaults, MM-SRV screen layout, value limits |
| `model_service_specifications` | legal entity | table | none | outline template rows | FR-17.23 | n/a | ML10–ML12 model service specs, standard service catalog |

### 3.17.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Purchase orders (all / open / received not invoiced / backordered) | list inquiry | vendor, status, document status, site, buyer, date range | PO number, vendor, approval status, document status, total, version | `PurchaseOrder` | Purchase orders list page; Backordered purchase lines; Received but not invoiced | ME2L / ME2M / ME2N with selection parameters WE101 / RECHNUNG; ME80FN |
| Purchase order lines and history | drill-down | product, vendor, delivery date, status | line qty, received, invoiced, remaining, price, price source, versions | `PurchaseOrderLine`, `PurchaseOrderVersion` | Purchase order lines / Purchase order history / Versions | ME23N purchase order history (EKBE), ME2N line view |
| Requisitions by requester / status / consolidation | list inquiry | requester, status, org unit, category, date | requisition, lines, estimated total, approver chain position, PO link | `PurchaseRequisition` | Purchase requisition inquiries; My requisitions | ME5A / ME5K / ME5J, Monitor Purchase Requisitions |
| Approval status and SLA | drill-down | document, approver, overdue | step, approver, assigned at, due, decision | Temporal workflow projection | Workflow history | SWI1 / release log |
| RFQ status, replies and award history | list inquiry | RFQ, vendor, status, deadline | invitations, replies, ranking, scores, awarded | `RFQ`, `RFQReply` | RFQ inquiries; Compare replies | ME4L / ME4M / ME4N, ME49 price comparison, ME47 |
| Purchase agreement fulfillment | list inquiry | agreement, vendor, product, validity | committed, released, remaining %, expiry | `PurchaseAgreement` | Purchase agreement inquiries (fulfillment / released quantities) | ME3L / ME3M / ME3N release documentation, ME80RN |
| Pricing explain | drill-down | vendor, product, qty, date, currency | candidate rows, specificity, winner | FR-17.6 explain | Trade agreement "Find prices" | ME23N → Conditions → Analysis (condition analysis) |
| Trade agreement list by vendor / product | list inquiry | relation, account/item codes, validity | price, breaks, currency, validity | `TradeAgreement` | Trade agreements (PriceDiscTable) inquiry | MEKA / MEKB / MEKC / MEKD condition lists, ME1L / ME1M info records |
| Order price history | operational report | product, vendor, date range | PO date, vendor, price, qty, agreement | `PurchaseOrderLine` | n/a (PurchLine query) | ME1P order price history |
| Charges inquiry | list inquiry | PO, charge code, vendor | charge, calc, amount, debit/credit sides | `Charge` | Charges inquiry | Condition records on PO (KONV) |
| Vendor delivery performance (confirmed vs actual) | KPI | vendor, period | on-time %, average days late, lines | `DeliverySchedule` + receipts (w18) | Vendor delivery performance (D365) | ME2V goods receipt forecast; vendor evaluation ME6H |
| Buyer workspace queues | KPI | buyer | lines to source, RFQs open, POs to confirm, agreements expiring, change orders pending | FR-17.14 | Procurement workspace | Fiori Procurement Overview |
| Scheduling agreement releases monitor | list inquiry | agreement, vendor, release kind, date | release number, cumulative released, cumulative received, next release due | `Release` | n/a | Monitor Scheduling Agreement Releases; ME85 / ME86 release documentation |
| Policy rules per hierarchy node | operational report | policy type, hierarchy, date | node, policy version, rule type, rule body | `Policy` | SSRS Policies report | n/a |
| Signing limits report | operational report | org unit, document type, as-of | worker, job, spending limit, approval limit, source (default/override), recertified at | `SigningLimit` | SSRS Signing limits report | Release codes assignment list |
| Procurement catalog report | operational report | catalog, category, org unit | items, vendor, price, publication status, missing images | `ProcurementCatalog` | SSRS Procurement catalog report | n/a |
| Services per purchase order | list inquiry | PO, vendor, service product | outline, planned value, entered (w18), invoiced, limit remaining | `ServiceOutline` | n/a | ME2S services per PO, MSRV1–MSRV6 service lists |
| Invoicing plan schedule | list inquiry | PO, vendor, due date range | plan row, due date, amount, status (OPEN/INVOICED) | `InvoicingPlan` | n/a | MRIS selection list |

### 3.17.15 Printed and electronic documents

| Document | Trigger | Layout elements (header / line / footer) | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Purchase order / change order | `:confirm` (every version) | header: PO number, version, vendor name/address, ship-to, bill-to, buyer, currency, payment and delivery terms, dates; lines: line no, product/vendor item no, description, qty, unit, price, discount, amount, delivery date, agreement ref; footer: subtotal, charges, tax, total, terms text, signature | PDF/A-2b, email (wave 4), EDI 850/ORDERS via `po.confirmed` (wave 35), portal (wave 20) | PO number `PO-` + `v{n}` | Commercial document; retain 10 years (DE/FR), 7 years (US); e-signature optional |
| Request for quotation | `:send` | header: RFQ number, deadline, terms, contact; lines: product/description, qty, unit, required date; questions from reply template | PDF, email, portal | RFQ number `RFQ-` | Public-sector solicitation rules where applicable (wave 48) |
| RFQ amendment / addendum | amendment issued | as RFQ with change summary | PDF, email, portal | `RFQ-{n}-A{k}` | Same |
| Purchase agreement document | agreement EFFECTIVE | header: agreement number, vendor, validity, commitment type; lines: product/category, committed qty/amount, price terms | PDF, email | `PA-` | Contract; retain 10 years after expiry |
| Scheduling agreement release (FRC / JIT) | release run | header: agreement, release number, kind, vendor, cumulative received, last receipt; lines: schedule date, qty, cumulative qty | PDF, email; EDI DELFOR (FRC) / DELJIT (JIT) via wave 35 | `REL-{agreement}-{n}` | Commercial; retain with agreement |
| Consolidated requisition approval summary | approval task | requisition header, lines, policy band, approver chain | HTML email with deep link | n/a | none |
| Requisition printout | on demand | header, lines, dimensions, approval history | PDF | `PR-` | none (internal) |
| Vendor catalog import result | import finished | accepted/rejected rows with reasons | CSV | n/a | none |

### 3.17.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Agreement threshold and expiry scan | daily 06:00 | legal entity | key = (agreement line, date); emits each threshold once | < 1 min per 10k lines | retry 3× | missed run ticket |
| RFQ deadline closer | hourly | legal entity | sets REPLIES_OPEN → CLOSED where deadline passed; idempotent | < 10 s | retry next hour | RFQs past deadline still open > 2 h → ticket |
| Approval SLA escalation | Temporal timers per instance | workflow id | timer fires once per step | n/a | workflow retry | task backlog > 100 → ticket |
| Release approved requisitions (auto-create POs) | daily 07:00 (opt-in) | legal entity × vendor | key = line id set hash; lines already consolidated skipped | < 2 min per 5k lines | lines with errors left in queue | errors > 0 → buyer notification |
| Scheduling agreement release run (FR-17.15) | per profile frequency (default weekly Monday 05:00; JIT daily) | agreement | key = (agreement, kind, run date); unchanged schedule produces no new release | < 1 min per 1k agreements | failed agreement listed, rest released | releases failed > 0 → buyer ticket |
| Policy re-evaluation on open requisitions (FR-17.16) | on policy version activation + nightly 01:00 | legal entity | key = (policy version, requisition); re-routes only when chain differs | < 2 min per 10k requisitions | retry 3× | re-routed count reported |
| Signing limit recertification (FR-17.17) | yearly per `signing_limit_recertification_months` + daily sweep | legal entity | key = (worker, cycle); creates one request per worker per cycle | < 1 min | retry | unrecertified limits past due → manager notification |
| Catalog publication / vendor import processing (FR-17.18) | on demand + nightly 02:00 | catalog | key = (import file hash); rows staged once | < 5 min per 50k rows | row-level errors | errors > 5 % → import marked FAILED |
| Subsequent price change run (FR-17.22) | on demand; optional weekly Sunday 06:00 | legal entity × trade agreement | key = (agreement version, run); lines already repriced skipped | < 5 min per 10k lines | per-PO failure isolated | versions created / failed reported |
| Delete and clean-up (cancelled POs older than N days, expired requisitions, update history) | monthly 1st 03:00 | legal entity | idempotent delete by cut-off | < 5 min | skip | none |

### 3.17.17 Security duties and privileges

| Duty | Privileges (entry points: API paths / screens / jobs) | Segregation-of-duties conflicts | Default roles that include it | AX duty / SAP authorization reference |
|---|---|---|---|---|
| RequisitionCreate | POST /requisitions, :submit; requisition screen; catalog picker | none | Employee (all), Requester | AX "Maintain purchase requisitions" (PurchReqMaintain); SAP M_BANF_BSA, M_BANF_WRK |
| RequisitionApprove | approval tasks for requisitions; delegate | with RequisitionCreate on own requisitions (self-approval blocked by workflow) | Manager, Category owner, CFO | AX "Approve purchase requisitions"; SAP M_BANF_FRG release codes |
| PurchaseOrderInquire | GET purchase orders, versions, diff, document.pdf; buyer workspace; inquiries 3.17.14 | none | Buyer, Receiving clerk, AP clerk, Controller | AX "Inquire into purchase orders"; SAP M_BEST_BSA activity 03 |
| PurchaseOrderMaintain | POST/PATCH purchase orders, :cancel, requisitions:consolidate, pricing/lookup, charges | with PurchaseOrderConfirm when `po_four_eyes=true`; with ReceivingPost (w18) and VendorInvoicePost (w18) | Buyer | AX "Maintain purchase orders" (PurchTableMaintain); SAP M_BEST_BSA 01/02, M_BEST_EKG purchasing group |
| PurchaseOrderConfirm | :confirm; approval tasks for POs; :reprice apply | with PurchaseOrderMaintain under four-eyes; with VendorMaintain (w7) | Senior buyer, Purchasing manager | AX "Confirm purchase orders"; SAP M_EINK_FRG release PO (ME28/ME29N) |
| RFQMaintain | POST rfqs, :send, replies; RFQ screen; amendments | with RFQAward on same RFQ when sealed | Buyer | AX "Maintain requests for quotation"; SAP M_ANFR_BSA, M_ANGB_BSA |
| RFQAward | :award; sealed-bid opening; scoring | with RFQMaintain (sealed) | Purchasing manager | AX "Award RFQ"; SAP M_ANGB_FRG |
| AgreementMaintain | POST agreements, lines; scheduling agreements, release schedules | with PurchaseOrderConfirm when policy requires | Buyer, Contract manager | AX "Maintain purchase agreements"; SAP M_RAHM_BSA, M_RAHM_FRG |
| TradeAgreementMaintain | trade agreement journal create/post; consignment price rows | with PurchaseOrderConfirm (price manipulation) | Pricing analyst, Purchasing manager | AX "Maintain trade agreements" (PriceDiscAdm); SAP M_EINF_EKO conditions, MEK1 |
| ProcurementPolicyMaintain | policies, policy rules, approval bands, category access, catalog policy, signing limit defaults | with RequisitionApprove, PurchaseOrderConfirm | Procurement administrator | AX "Maintain purchasing policies" (PurchPolicy); SAP release strategy customizing (S_TABU_DIS, CL24N) |
| SigningLimitApprove | signing limit requests approval; recertification | with own SigningLimitRequest | HR manager, Procurement administrator | AX "Approve signing limit requests"; n/a |
| CatalogMaintain | catalogs CRUD, vendor import review, publication | none | Catalog manager, Buyer | AX "Maintain procurement catalogs" (CatProcurementCatalogMaintain); SAP SRM catalog admin |
| CategoryMaintain | category managers, vendor assignments, questionnaires | with VendorMaintain (w7) when assignment approval required | Category manager, Procurement administrator | AX "Maintain procurement categories"; SAP source list ME01 (M_ORDR_WRK) |
| ProcurementSetupMaintain | parameters in 3.17.13, charge codes, number sequences, templates | with PurchaseOrderConfirm, TradeAgreementMaintain | Procurement administrator | AX "Maintain procurement parameters" (PurchParameters); SAP SPRO MM-PUR (S_TABU_DIS) |

### 3.17.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| PRC-REQ-001 | error | Requisition line {line}: account structure invalid for {dimensions} | Wave 11 account structure | Correct the financial dimensions | FR-17.1 |
| PRC-REQ-002 | error | Requisition has more than {max} lines | Line cap 500 | Split the requisition | FR-17.1 |
| PRC-REQ-003 | warning | No price found for {product}; estimated price set to 0 | No trade agreement or last price | Enter an estimated price | FR-17.1 |
| PRC-POL-001 | error | No approver with approval limit ≥ {amount} {currency} found above {requester} | Signing limits exhausted | Add a default signing limit for the top level or route to CFO | FR-17.2, FR-17.17 |
| PRC-CAT-002 | error | Vendor {vendor} is not assigned to category {category} (policy {policy}) | Category access HARD | Choose an assigned vendor or request a provisional assignment | FR-17.20 |
| PRC-CAT-003 | error | Catalog item required for category {category} under policy {policy} | Catalog policy rule | Pick a catalog item or request a non-catalog exception | FR-17.18 |
| PRC-AGR-003 | error | Agreement line {line} would be exceeded: released {released} + {qty} > committed {committed} (HARD) | FR-17.5 | Reduce quantity, extend the agreement or switch to SOFT | FR-17.5 |
| PRC-AGR-004 | warning | Agreement line {line} is at {pct} % of commitment | Threshold ≥ 90 % | Plan a renewal | FR-17.14 |
| PRC-AGR-005 | error | PO delivery date {date} is outside agreement validity {from}–{to} | BR-17.10 | Change the date or the agreement reference | BR-17.10 |
| PRC-TA-002 | error | Overlapping trade agreement rows with equal specificity for {account}/{item} from {date} | Activation check | Adjust validity or quantity breaks | BR-17.3 |
| PRC-PO-007 | error | Line {line} quantity {qty} cannot be below received {received} / invoiced {invoiced} | FR-17.8, BR-17.7 | Receive a correction or credit first | FR-17.8 |
| PRC-PO-008 | error | PO {po} must be APPROVED and the vendor active before confirmation (vendor status {status}) | Confirm precondition | Complete approval or release the vendor hold | FR-17.7 |
| PRC-PO-009 | error | Concurrent update: PO {po} changed by {user} at {time} | ETag mismatch | Reload and reapply the change | BR-17.8 |
| PRC-FX-001 | error | No exchange rate {from}→{to} of type {type} on {date} | Wave 12 rates | Enter the rate or change the PO currency | BR-17.12 |
| PRC-RFQ-003 | warning | Reply from {vendor} received after deadline {deadline}; excluded from ranking | BR-17.11 | Override with reason to include | FR-17.4 |
| PRC-RFQ-004 | error | Sealed replies for RFQ {rfq} cannot be viewed before {deadline} | BR-17.16 | Wait for the deadline or perform a logged two-person opening | FR-17.19 |
| PRC-SA-001 | error | Schedule line {date} is in the firm zone; reduction requires a reason code | BR-17.13 | Provide a reason code | FR-17.15 |
| PRC-SA-002 | error | Cumulative received {received} exceeds cumulative released {released} for {product} | Receipt beyond release | Create a release or check receipts | FR-17.15 |
| PRC-IP-001 | error | Invoicing plan total {plan_total} ≠ line amount {line_amount} | BR-17.17 | Adjust plan rows | FR-17.21 |
| PRC-RP-001 | info | Reprice preview: {n} lines, total delta {delta} {currency}; {m} invoiced lines excluded | FR-17.22 | Apply or cancel | FR-17.22 |
| PRC-SRV-003 | error | Unplanned services {amount} exceed limit {limit} on PO line {line} | BR-17.19 | Raise the limit via a PO change or reject the entry | FR-17.23 |
| PRC-SRV-004 | error | Service outline items ({sum}) do not sum to outline total ({total}) | Outline consistency | Correct item amounts | FR-17.23 |
| PRC-CAT-005 | error | Vendor catalog row {row}: {field} missing or invalid ({value}) | Import validation | Fix the file and re-import | FR-17.18 |
| PRC-SL-001 | warning | Signing limit for {worker} expired on {date}; approvals routed to manager until recertified | Recertification overdue | Complete recertification | FR-17.17 |
| PRC-OUT-001 | warning | PO {po} confirmed but document rendering failed {n} times; vendor not yet notified | Render worker failure | Re-render from the PO screen | FR-17.12 |

### 3.17.19 Performance budgets and capacity

| Operation | Volume assumption (per tenant per day / peak per minute) | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| PO create / line edit with totals and tax | 5,000 POs, 40k line edits / 200 per min | 120 ms / 500 ms / 900 ms | 100 rps | PO ≈ 3 KB + lines 600 B + versions 2–20 KB (JSON patch); 5k POs/day ≈ 6 GB/year | PO (tenant, vendor, approval_status); line (po_id, line_no); versions by (po_id, version_no) | Totals recalculation batched per request; tax engine timeout 2 s → totals marked STALE and recomputed on confirm |
| PO confirm (incl. inventory apply + render enqueue) | 4,000 / 100 per min | 200 ms / 500 ms / 1.5 s (render async < 5 s) | 60 rps | documents ≈ 150 KB PDF each in object store ≈ 220 GB/year | render worker queue | Render queue grows; confirmation unaffected; PDF SLO relaxed to 60 s with user notice |
| Pricing lookup / explain | 300k / 2,000 per min | 5 ms / 20 ms / 40 ms | 500 rps | TradeAgreement ≤ 1M rows ≈ 400 MB | composite (tenant, relation, account_code, account_id, item_code, item_id, valid_from) + currency | Served from in-pod cache (5 s TTL) of agreement rows per vendor; p95 ≤ 50 ms |
| Requisition submit → workflow start | 2,000 / 60 per min | 300 ms / 2 s / 4 s | 30 rps | requisition ≈ 2 KB + lines | by requester, status | Temporal queue; submit returns 202 with workflow id |
| Consolidation board (5k open lines) | 50 runs | 1 s / 4 s / 8 s | n/a | n/a | lines (status, vendor, site, delivery_date) | Paginated grouping; hard cap 20k lines per run |
| RFQ compare (50 replies × 200 lines) | 100 | 300 ms / 1 s / 2 s | n/a | replies ≈ 1 KB/line | reply (rfq_id, line_id) | Server-side ranking cached per RFQ until a reply changes |
| Scheduling agreement release run (1k agreements) | weekly | 20 s / 60 s / 120 s | n/a | release ≈ 5 KB | (agreement_id, release_number) | Child workflows per 100 agreements |
| Policy evaluate | 10k / 300 per min | 3 ms / 10 ms / 25 ms | 1,000 rps | policies ≈ 50 KB each | hierarchy path materialized (wave 6) | Cached compiled policy per version in pod memory |
| Catalog search (requisition picker) | 100k / 1,000 per min | 30 ms / 100 ms / 200 ms | 300 rps | catalog entries ≈ 1 KB + images in object store | wave 8 search index with catalog filter | Falls back to product search without images |
| Reprice run (10k lines) | monthly | 1 min / 5 min / 10 min | n/a | versions as above | (price_source_id, status) | Chunked 500 lines per activity |

### 3.17.20 Localization and compliance hooks

| Concern | Varies by | Country packs touching it (wave 48) | Extension point used |
|---|---|---|---|
| PO document legal content (company registration numbers, VAT ids, mandatory clauses, language) | Country commercial law; vendor language | All packs | Wave 15 template per legal entity with country block; vendor language from wave 7 party preferences |
| Tax group defaulting and reverse charge on PO lines | VAT/GST regimes, US sales/use tax | EU, UK, CA, AU, IN, BR | Wave 13 tax engine via tax group / item tax group on line; `tax_total` in FR-17.11 |
| Public procurement rules (sealed bids, solicitation types, bid protests, thresholds) | Public sector regulations (US FAR, EU directives 2014/24) | US-PS, EU-PS | FR-17.19 `RfqType` with pack-supplied solicitation types; audit log of sealed-bid opening |
| Gap-free document numbering for POs/RFQs | IT, ES, PT, TR, some LATAM | IT, ES, PT, TR, BR | `po_number_gap_free` per legal entity (wave 5 sequences) |
| Currency and rounding of unit price (4 decimals) and line amounts | Currency minor units | n/a | BR-17.5 using wave 12 rounding rules |
| Calendar: delivery dates and SLA timers on business days | Site calendar and holidays | All | Wave 6 calendars; approval SLA timers skip non-working days |
| Signing limits and approval evidence (SOX 404, internal control) | Audit regimes | US (SOX), JP (J-SOX) | FR-17.17 recertification report; wave 48 audit policy consumes `signinglimit.changed` |
| Vendor sanctions / denied-party screening before PO confirmation | OFAC, EU sanctions lists | US, EU | Wave 7 party screening hook invoked in `ConfirmPO` (vendor status) |
| EDI/e-order formats (EDIFACT ORDERS, X12 850, Peppol Order 3.0) | Trading-partner region | n/a (wave 35) | `po.confirmed` event payload complete enough for each format (wave 35 mapping) |
| Data residency of PO documents and attachments | Tenant region | n/a | Object store bucket per region (wave 1) |
| Privacy of requester and approver identities in printed documents | GDPR | EU | Template fields default to role names, not personal names, in EU packs |

### 3.17.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.16.1 — RFQ → Bidding (vendor portal RFQ reply, sealed bids, scoring criteria) [#152] | PARTIAL | ADDED as FR-17.19 | Sealed bids, scoring groups, amendments; portal reply in wave 20 FR-20.13 |
| AX 12.16.1 — Procurement catalogs [#153] | PARTIAL | ADDED as FR-17.18 | Catalog with images, category navigation, publication by org unit |
| AX 12.16.1 — Procurement → Signing limits (link to Org admin) [#156] | PARTIAL | ADDED as FR-17.17 | Signing limits with requests and recertification |
| AX 12.16.2 — SSRS: Procurement catalog report [#159] | PARTIAL | ADDED as FR-17.18 | 3.17.14 |
| AX 12.16.3 — Catalog → Import vendor catalogs (CMR) / Publish procurement catalog [#160] | PARTIAL | ADDED as FR-17.18 | CSV/JSON import with staging; CMR XML mapped by wave 10 framework; job in 3.17.16 |
| AX 12.16.4 — Policies → Purchase requisition approval amount thresholds / Signing limit policies [#161] | PARTIAL | ADDED as FR-17.16, FR-17.17 | Approval bands as policy rules; default signing limits |
| AX 12.16.4 — Categories → Procurement categories, Category policy rules, Category managers, Category vendor assignment, Category questionnaires [#162] | PARTIAL | ADDED as FR-17.20 | Managers, vendor assignment with HARD/SOFT mode, questionnaires |
| AX 12.16.4 — Catalogs → Procurement catalogs, Catalog policies, External catalogs (cXML config), Vendor catalog import parameters, Catalog images [#163] | PARTIAL | ADDED as FR-17.18 | Catalog policy rule and import parameters in 3.17.13; external catalog (cXML/OCI) configuration DEFERRED to wave 35 |
| AX 12.16.4 — RFQ → RFQ types, RFQ scoring criteria/groups, Reply templates, Solicitation types, Bidding parameters [#166] | PARTIAL | ADDED as FR-17.19 | `rfq_types` setup in 3.17.13 |
| AX 12.29.1 — Policies → Signing limits (policies, worker requests, defaults by job/compensation) [#237] | PARTIAL | ADDED as FR-17.17 | |
| AX 12.29.1 — Policies → Policy framework forms (Purchasing, Expense, Audit, Vendor invoice, Signing limit) [#238] | PARTIAL | ADDED as FR-17.16 | Policy types PURCHASING and SIGNING_LIMIT implemented here; VENDOR_INVOICE used by wave 18; EXPENSE (wave 43) and AUDIT (wave 48) reuse the engine |
| AX 12.29.2 — SSRS: Signing limits report [#245] | PARTIAL | ADDED as FR-17.17 | 3.17.14 |
| AX 12.29.2 — SSRS: Policies report (policy rules per hierarchy) [#246] | PARTIAL | ADDED as FR-17.16 | 3.17.14 |
| AX 12.29.3 — Signing limits → Signing limit request processing (annual recertification) [#250] | PARTIAL | ADDED as FR-17.17 | Recertification job (3.17.16) |
| AX 12.29.4 — Policies → Signing limit policies / parameters / default signing limits / request workflow [#256] | PARTIAL | ADDED as FR-17.17 | 3.17.13 `default_signing_limits`, `signing_limit_recertification_months` |
| AX 12.29.4 — Policies → Policy parameters (organization hierarchy purpose per policy type) [#257] | PARTIAL | ADDED as FR-17.16 | 3.17.13 `hierarchy_purpose_per_type` |
| SAP 13.12 — ME84 / ME85 / ME86 SA releases (FRC/JIT), renumber, release documentation [#124] | PARTIAL-17 | ADDED as FR-17.15 | Releases with cumulative quantities; DELFOR/DELJIT transport in wave 35 |
| SAP 13.12 — Fiori: Create Purchase Requisition (self-service, catalog, free text) with OCI punch-out [#129] | PARTIAL-17 | ADDED as FR-17.18 | Catalog item selection added; OCI/cXML punch-out DEFERRED to wave 35 |
| SAP 13.12 — Fiori: Monitor Supplier Confirmations / Monitor Scheduling Agreement Releases [#132] | PARTIAL-17 | ADDED as FR-17.15 | Releases monitor in 3.17.14; supplier confirmations monitor from FR-17.7 confirmed dates and wave 20 acknowledgements |
| SAP 13.12 — ME84 batch SA release creation run [#133] | PARTIAL-17 | ADDED as FR-17.15 | Job in 3.17.16 |
| SAP 13.12 — MEI1 / MEI2 / MEI4 subsequent price change [#135] | PARTIAL-17 | ADDED as FR-17.22 | Open lines repriced via PO version; invoiced lines via wave 18 FR-18.18 |
| SAP 13.12 — Scheduling agreement release creation profile, firm/trade-off zone, JIT indicator (T163M) [#138] | PARTIAL-17 | ADDED as FR-17.15 | `sa_release_profile` in 3.17.13 |
| SAP 13.14 — MRIS invoicing plan settlement [#156] | PARTIAL-17 | ADDED as FR-17.21 | Plan on PO line; settlement run in wave 18 FR-18.17 |
| SAP 13.14 — MRNB revaluation (retroactive price change on invoiced items) [#157] | PARTIAL-17 | ADDED as FR-17.22 | `reprice_invoiced` hands invoiced lines to wave 18 FR-18.18 |
| SAP 13.14 — MRIS scheduled invoicing plan run [#159] | PARTIAL-17 | ADDED as FR-17.21 | Run defined in wave 18 (3.18.16) |
| SAP 13.14 — MRNB scheduled revaluation run [#160] | PARTIAL-17 | ADDED as FR-17.22 | Job in 3.17.16 (weekly, opt-in) |
| SAP 13.14 — Invoicing plan types (periodic/partial) [#162] | PARTIAL-17 | ADDED as FR-17.21 | `invoicing_plan_types` in 3.17.13 |
| SAP 13.15 — ML10 / ML11 / ML12 model service specifications [#164] | GAP (propose 17) | ADDED as FR-17.23 | `ModelServiceSpecification` templates |
| SAP 13.15 — Standard service catalog (SSC) [#165] | GAP (propose 17) | ADDED as FR-17.23 | Standard texts as service products (wave 8) referenced from templates |
| SAP 13.15 — ME21N item category D with service lines, value limits, contract limits [#166] | PARTIAL-17 | ADDED as FR-17.23 | Service outline, `value_limit`, `contract_limit` |
| SAP 13.15 — ML33 … ML48 service conditions (vendor / service / vendor+plant) [#168] | PARTIAL-17 | ADDED as FR-17.23 | Service prices via FR-17.6 trade agreements on service products with site |
| SAP 13.15 — Fiori: Manage Purchase Orders (service items), Manage Supplier Quotations for services [#173] | PARTIAL-17 | ADDED as FR-17.23 | PO and RFQ lines of kind SERVICE with outline |
| SAP 13.15 — Service conditions calculation schema MS0000, condition types PRS [#183] | PARTIAL-17 | ADDED as FR-17.23 | Trade agreement relation PRICE on service products; schema concept SUPERSEDED by wave 26 condition engine |
| SAP 13.15 — Source determination for services (contracts) [#185] | PARTIAL-17 | ADDED as FR-17.23 | Agreement lookup for service product × site |
| SAP 13.31 — Legal Content Management / contract management (CLM, Ariba Contracts) [#351] | PARTIAL-17 | OUT-OF-SCOPE clause library, obligation tracking and contract authoring are not an ERP v1 capability; agreements keep attachments, validity and expiry alerts (FR-17.5, FR-17.14) | Candidate for v1.x or external CLM integration via wave 35 |
| SAP 13.32 — Configuration in purchasing (configurable PO items) [#361] | PARTIAL-17 | DEFERRED to wave 24 | Depends on the product configurator (wave 24 process/variant configuration); PO line reserves `configuration_id` |

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
| Service entry sheets | n/a (service lines invoiced directly) | ML81N service entry sheet, ML82/ML83/ML84 display/list/accept, ML85 collective release, ESSR, lean services (S/4 Manage Service Entry Sheets), MSRV lists, ME2S, MRRL for services, MIRO with SES reference | `ServiceEntrySheet` with acceptance workflow, partial and unplanned entries within limits, collective acceptance, ERS for services, invoice match to SES (FR-18.14) | full |
| Stochastic invoice block | n/a | OMRX stochastic block (threshold amount, percentage), RBKP ZLSPR = S, MRBR release | `stochastic_block` policy (FR-18.15) producing status `AUDIT_SAMPLE` | full |
| Consignment settlement | D365 consignment: vendor invoice from ownership change | MRKO consignment and pipeline settlement | Consignment settlement run creating `VendorInvoice` kind CONSIGNMENT from wave 16 ownership changes (FR-18.16) | full (pipeline OUT-OF-SCOPE) |
| Invoicing plan settlement | n/a | MRIS invoicing plan settlement (periodic/partial) | Invoicing plan run creating invoices without receipt (FR-18.17) | full |
| Subsequent debit / credit | Invoice with reference to invoiced PO line (price adjustment) | MIRO subsequent debit/credit; MRNB revaluation | `VendorInvoice` kind SUBSEQUENT_DEBIT / SUBSEQUENT_CREDIT (FR-18.18) | full |
| One-time vendor invoice | VendTable one-time supplier (OneTimeVendor flag, VendParameters one-time vendor account) | Account group CPD, one-time account with address/bank on document (OBD3) | `VendorInvoice.one_time_payee` with document-level name, address, tax id and bank account (FR-18.19); payment in wave 19 FR-19.23 | full |

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

- **FR-18.14 Service entry sheets (service receipts)** — Must. The system shall provide a `ServiceEntrySheet` (SAP ML81N / ESSR; S/4 lean service entry sheet) against a SERVICE PO line (wave 17 FR-17.23) with header (vendor, PO, period from/to, external reference, entered by), lines referencing outline service items (planned) or free-text unplanned services with quantity, unit, unit price and amount, status `DRAFT, SUBMITTED, ACCEPTED, REJECTED, REVOKED`, and an acceptance workflow (wave 4 template; approver = PO requester or cost owner from the policy type VENDOR_INVOICE of FR-17.16).
  - Acceptance posts the service receipt: Dr expense / cost object per line account assignment, Cr GR/IR accrual, creating a `GrirOpenItem` per sheet line (SAP "GR for SES"); the PO line's `received_qty`/`received_amount` and `document_status` update; for lines with `settle_without_receipt=false` the invoice (FR-18.4) proposes lines from accepted sheets and matching (FR-18.5) compares to the sheet amount (quantity tolerance applies to amount for services).
  - Unplanned services are limited by the PO line `value_limit` and `contract_limit` (BR-17.19, `PRC-SRV-003`); partial entries accumulate against planned quantities.
  - Collective acceptance (`POST /service-entry-sheets:accept-bulk`, SAP ML85) and revoke (reverses the posting if not invoiced).
  - ERS vendors (FR-18.10) get an invoice per accepted sheet (SAP MRRL for services) with number `ERS-{sheet_number}`.
  - Lists: by service, by PO, by sheet, by vendor, by agreement (SAP MSRV1–MSRV6), acceptance list (ML83/ML84); the wave 20 portal lets vendors enter sheets (FR-20.20).

- **FR-18.15 Stochastic (random-sample) invoice block** — Must. The system shall evaluate on every invoice reaching MATCHED a `stochastic_block` rule from the matching policy scope (SAP OMRX T169P): `threshold_amount` (acct ccy) and `percentage`; invoices with total ≥ threshold are blocked with probability `percentage` % (linear decrease below threshold proportional to total/threshold), using a seeded CSPRNG with the invoice id so the decision is reproducible; a blocked invoice gets match status `AUDIT_SAMPLE`, is routed to the AP auditor queue (duty InvoiceAuditRelease), and is released or rejected with reason; the sample rate actually achieved is reported monthly.

- **FR-18.16 Consignment settlement** — Must. The system shall consume `dev.passage.inventory.ownership.changed.v1` (wave 16 FR-16.17) and, per vendor and settlement frequency (`consignment_settlement_frequency` DAILY/WEEKLY/MONTHLY, SAP MRKO), create and post a `VendorInvoice` of kind CONSIGNMENT with one line per ownership change line (product, qty, consignment price, tax group from the trade agreement), clearing the consignment accrual (Dr consignment accrual / Cr vendor control), skipping matching (status MATCHED by construction), and sending a consignment settlement statement (3.18.15) to the vendor; when `consignment_self_billing=false` the vendor's own invoice is matched against the ownership change lines instead (2-way to accrual).

- **FR-18.17 Invoicing plan settlement run** — Should. The system shall run daily (3.18.16; SAP MRIS) over wave 17 invoicing plan rows with `due_date ≤ run date` and status OPEN, creating per vendor a `VendorInvoice` kind PLAN with lines for each due row (amount, tax group of the PO line, account assignment), posting it when `settle_without_receipt=true` and the plan is marked ERS-like (`auto_post=true`), otherwise leaving it in DRAFT for the vendor's invoice to be matched 2-way to the plan row; plan rows move to INVOICED with the invoice reference.

- **FR-18.18 Subsequent debit and credit** — Should. The system shall support `VendorInvoice` kinds SUBSEQUENT_DEBIT and SUBSEQUENT_CREDIT (SAP MIRO subsequent debit/credit; AX price-adjustment invoice) referencing invoiced PO lines with a value-only change (quantity 0): posting Dr/Cr inventory (actual-cost items, via `cost_amount_adjustment` for wave 20) or PPV (standard cost) against the vendor control account; wave 17 FR-17.22 `:reprice` with `reprice_invoiced=true` creates SUBSEQUENT_CREDIT drafts for repriced invoiced lines (SAP MRNB).

- **FR-18.19 One-time vendor invoices** — Should. The system shall allow an invoice (kind EXPENSE or PO with the legal entity's one-time vendor account; AX VendParameters one-time vendor; SAP CPD account group) to carry a document-level `one_time_payee` block (name, address, country, tax id, email, bank account IBAN/BIC or routing/account) validated like vendor bank accounts (BR-19 IBAN mod-97 / ABA), stored encrypted, printed on the payment advice and used by wave 19 FR-19.23 for payment; duplicate check uses payee name + invoice number; one-time payees exceeding `one_time_payee_max_amount` (default 10,000 USD) or appearing more than `one_time_payee_max_count` times (default 3 per 12 months) are rejected with `AP-OTV-001` and must be set up as vendors (wave 7).

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
| `dev.passage.receiving.service-entry-sheet.accepted.v1` / `.revoked.v1` | sheet accepted / revoked | sheet_id, po_id, po_line_id, vendor_id, lines (service item, qty, amount, account assignment), grir amounts, ledger_entry_id | payables (match, ERS), costing (expense), project accounting (w41), supplier-portal (w20) |
| `dev.passage.payables.invoice.audit-sampled.v1` | stochastic block applied | invoice_id, vendor_id, total, rule (threshold, percentage), assignee | workflow, notifications, audit (w48) |
| `dev.passage.payables.consignment.settled.v1` | consignment settlement invoice posted | invoice_id, vendor_id, ownership change journal ids, lines, total | supplier-portal (w20), analytics |

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
- **BR-18.13** Service entry sheet state machine: `DRAFT → SUBMITTED → ACCEPTED`; `SUBMITTED → REJECTED → DRAFT`; `ACCEPTED → REVOKED` only while `invoiced_amount = 0` on all its lines; acceptance is the posting event (SAP "GR for SES"); a sheet period may not overlap another ACCEPTED sheet of the same PO line for the same planned service item unless the item is quantity-based.
- **BR-18.14** Stochastic block decision = `hash(invoice_id ‖ rule_version) mod 10,000 < percentage × 100 × min(1, total / threshold)`; the decision is recorded with rule version so re-matching does not re-draw; an invoice already in a variance exception is not additionally sampled.
- **BR-18.15** Consignment settlement invoices are always posted in the vendor's currency at the trade-agreement price captured on the ownership change line (not re-priced at settlement); price changes after the ownership change are handled as SUBSEQUENT_DEBIT/CREDIT.
- **BR-18.16** Invoicing plan rows generate at most one invoice line each; a plan row whose PO line is cancelled is cancelled with it; plan invoices for lines with `settle_without_receipt=false` require an accepted service entry sheet or product receipt covering the period before posting.
- **BR-18.17** Subsequent debit/credit lines have quantity 0 and reference an invoiced PO line; the amount is capped at the invoiced amount for credits (`AP-SUB-001`); GR/IR is not touched; for actual-cost items the adjustment is written to the receipt transaction's `cost_amount_adjustment` and flows to issues at the next wave 20 close (BR-20.6).
- **BR-18.18** One-time payee data is stored encrypted (wave 2 field-level encryption) and never copied to the party model; the payee block is immutable after posting; payment to a one-time payee requires the bank account to pass the same validation as vendor bank accounts and the invoice to have passed a four-eyes approval regardless of amount band.

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
- **AC-18.11** (FR-18.14) Given a service PO line with planned items A (10 h × 100.00 EUR) and B (1 × 500.00 EUR) and unplanned limit 300.00, when a sheet with A 6 h, B 1 and unplanned 250.00 is submitted and accepted, then a ledger entry Dr expense 1,350.00 / Cr GR/IR 1,350.00 posts, three GrirOpenItem rows exist, and an invoice proposed from the sheet matches with status MATCHED; a second sheet with unplanned 100.00 is rejected with `PRC-SRV-003`.
- **AC-18.12** (FR-18.15) Given a stochastic rule threshold 10,000.00 USD and percentage 10, when 10,000 matched invoices of 20,000.00 USD are evaluated, then between 900 and 1,100 receive `AUDIT_SAMPLE`, re-matching any invoice yields the same decision, and released invoices post normally.
- **AC-18.13** (FR-18.16) Given 3 ownership changes for vendor V1 in a week (120 EA × 4.00, 50 EA × 4.00, 30 EA × 4.20 USD) and weekly settlement, when the settlement run executes, then one CONSIGNMENT invoice of 806.00 USD plus tax posts, the consignment accrual account nets to 0.00 for those lines, and the settlement statement is emailed.
- **AC-18.14** (FR-18.17) Given a PERIODIC plan of 1,000.00 EUR monthly with `auto_post=true`, when the run executes on the due date twice (rerun), then exactly one invoice per due row exists and the plan row is INVOICED.
- **AC-18.15** (FR-18.18) Given an invoiced line of 100 EA at 10.00 USD for a FIFO item with 60 EA still on hand, when a SUBSEQUENT_CREDIT of 50.00 USD posts, then AP is debited 50.00, inventory credited 50.00, the receipt transaction carries `cost_amount_adjustment` −50.00, and GR/IR is unchanged.
- **AC-18.16** (FR-18.19) Given a one-time payee with an invalid IBAN checksum, when the invoice is saved, then it is rejected with `AP-BNK-001`; with a valid IBAN and amount 12,000.00 USD (> 10,000.00 limit), it is rejected with `AP-OTV-001`.

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

### 3.18.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `match_level` | matching policy scope | enum | THREE_WAY | NONE, TWO_WAY, THREE_WAY | FR-18.5 | Accounts payable parameters → Invoice validation → "Enable invoice matching validation", line matching policy | GR-based IV flag on PO item (EKPO-WEBRE) |
| `price_tol_pct` / `price_tol_abs` / `price_tol_mode` | matching policy scope | numeric / numeric / enum | 2.0 / 25.00 / UPPER_ONLY | ≥ 0; UPPER_ONLY, BOTH | FR-18.5 price check | Price tolerances (VendInvoicePriceTolerance) per item/vendor/group | OMR6 keys PP (price), PS (price variance estimated), VP (moving-average price variance) |
| `qty_tol_pct` | matching policy scope | numeric | 5.0 | 0–100 | FR-18.5 quantity check | Released product → quantity tolerance | OMR6 DQ, DW (quantity variance when GR quantity = 0) |
| `total_tol_pct` / `total_tol_abs` | matching policy scope | numeric | 1.0 / 50.00 | ≥ 0 | FR-18.5 total check | Invoice totals tolerances (D365 invoice totals matching) | OMR6 AN / AP (amount for item with/without order reference) |
| `small_diff_abs` | matching policy scope | numeric | 0.50 | ≥ 0 | BR-18.5 auto-posting to small-differences account | Accounts payable parameters → "Maximum penny difference" | OMR6 BD (form small differences automatically) |
| `date_tol_days` | matching policy scope | int | 0 (off) | 0–90 | invoice before planned delivery date | n/a | OMR6 ST (date variance, value × days) |
| `charges_tol_pct` | matching policy scope | numeric | 5.0 | ≥ 0 | FR-18.11 charge variance | Invoice matching → Charges tolerances | OMR6 KW (variance from condition value) |
| `stochastic_block` | matching policy scope | record | off (threshold 0, percentage 0) | threshold_amount ≥ 0, percentage 0–100 | FR-18.15 | n/a | OMRX stochastic block (T169P) |
| `over_delivery_pct` / `under_delivery_pct` / `close_short` | product, vendor × product | numeric / numeric / bool | 0 / 0 / false | 0–100 | FR-18.1 receiving tolerance | Released product → Over/underdelivery | OMC0 tolerance keys B1/B2, PO item tolerances (EKPO-UEBTO/UNTTO) |
| `receipt_requirements` | item model group | enum | RECEIPT | NONE, REGISTRATION, RECEIPT | FR-18.1 registration mandatory before receipt | Item model group → Registration requirements / Receiving requirements | Default stock type on GR (OMCJ) |
| `default_receipt_status` | warehouse | enum | AVAILABLE | AVAILABLE, QUARANTINE, BLOCKED | inventory status on registration | Default inventory status on warehouse | OMCJ default stock type (quality/blocked), movement 103/105 |
| `duplicate_check_window_months` / `duplicate_check_fields` | legal entity | int / set | 24 / vendor + invoice number | + invoice date, + amount | FR-18.4 `AP-DUP-001` | Accounts payable parameters → "Check for invoice numbers used" | OMRDC duplicate invoice check (T169P) |
| `capture_confidence_threshold` / `auto_post_confidence` | legal entity | numeric | 0.85 / off (0.95 when enabled) | 0–1 | UX highlight and auto-post gate | Invoice capture parameters (D365) | VIM/DIE confidence thresholds |
| `ers_enabled` / `ers_invoice_number_format` | vendor | bool / pattern | false / `ERS-{receipt_number}` | | FR-18.10 | Vendor → "Self-billing"; Generate invoices from product receipts | LFM1-XERSY, OMRR ERS document type and tax code |
| `exception_sla_days` / `exception_assignee_rule` | legal entity | int / enum | 3 / BUYER_FOR_PRICE_RECEIVER_FOR_QTY | 1–30 | FR-18.7 | Vendor invoice workflow escalation | MRBR worklist assignment (custom) |
| `grir_writeoff_limit` / `grir_writeoff_age_days` | legal entity | numeric / int | 5.00 / 180 | ≥ 0 | GR/IR maintenance proposal (3.18.16) | Accrued purchases write-off (manual) | MR11 tolerance and key date |
| `posting_profile.grir_account` / `ppv_account` / `small_diff_account` / `consignment_accrual` | posting profile | account | seeded | ledger accounts | FR-18.2, FR-18.6, FR-18.16 | Posting profiles: Purchase expenditure un-invoiced, Price variance, Penny difference | OBYC WRX, PRD, DIF, KON |
| `ses_acceptance_workflow` / `ses_approver_rule` | policy (VENDOR_INVOICE) | workflow id / enum | wave 4 default / PO_REQUESTER | PO_REQUESTER, COST_CENTER_OWNER, PROJECT_MANAGER | FR-18.14 | n/a | ESSR release procedure (classification), SES approval workflow |
| `ses_number_sequence` | legal entity | sequence | `SES-` | wave 5 sequences | FR-18.14 | n/a | Number ranges for SES |
| `consignment_settlement_frequency` / `consignment_self_billing` | vendor | enum / bool | WEEKLY / true | DAILY, WEEKLY, MONTHLY | FR-18.16 | Consignment parameters (D365) | MRKO run variant per vendor |
| `invoicing_plan_run_time` | legal entity | time | 04:00 | | FR-18.17 | n/a | MRIS variant |
| `one_time_vendor_account` / `one_time_payee_max_amount` / `one_time_payee_max_count` | legal entity | reference / numeric / int | none / 10,000.00 USD / 3 per 12 months | | FR-18.19 | Accounts payable parameters → One-time supplier account | Account group CPD (OBD3, T077K) |
| `non_po_invoice_approval` | policy (VENDOR_INVOICE) | enum | AMOUNT_BANDS | AMOUNT_BANDS, GL_ACCOUNT_OWNER | open question 3 | Invoice register approval journal | FB60 parking + workflow |

### 3.18.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Expected receipts (arrival overview) | list inquiry | site, warehouse, vendor, date range, ASN | PO, line, expected qty, arrived, registered, ASN | PO schedules + `ProductReceipt` | Arrival overview (WMSArrivalOverview) | ME2V goods receipt forecast, VL06I inbound delivery monitor |
| Product receipt journal | list inquiry | vendor, PO, packing slip, date | receipt number, lines, qty, amount, voucher | `ProductReceipt` | Product receipt journal inquiry (VendPackingSlipJour) | MB51 movement 101, ME23N PO history |
| Vendor invoice list / status | list inquiry | vendor, status, match status, source, date | invoice, vendor invoice number, total, match status, assignee | `VendorInvoice` | Pending vendor invoices; Invoice journal inquiry | MIR5 / MIR6 invoice overview (parked, held, blocked, posted) |
| Match details | drill-down | invoice | per line PO price, invoice price, qty received/invoiced, variances, policy applied | `MatchResult` | Invoice history and matching details | MIRO tolerance log, MRBR block reasons |
| Exception queue | operational report | assignee, variance type, age | invoice, line, variance, SLA due | Temporal workflow projection | Vendor invoice workflow history | MRBR blocked invoices list |
| GR/IR open items (accrued purchases) | operational report | vendor, PO, age, as-of | received not invoiced, invoiced not received, totals vs ledger | `GrirOpenItem` | Accrued purchases report (Received not invoiced) | MB5S GR/IR balances, F.19 GR/IR analysis |
| Received not invoiced by PO | list inquiry | vendor, buyer | PO line, received qty/amount, invoiced, days open | `GrirOpenItem` + PO | Purchase orders → Received but not invoiced | ME2L with RECHNUNG, ME80FN |
| Duplicate invoice suspects | KPI / list | vendor, window | pairs with same normalized number or same amount/date | `VendorInvoice` | n/a | OMRDC report (custom) |
| Invoice capture quality | KPI | adapter, vendor, period | invoices captured, fields corrected %, avg confidence, touchless % | `VendorInvoice.source`, audit | Invoice capture dashboard (D365) | VIM analytics |
| Service entry sheets (by service / PO / sheet / vendor / contract) | list inquiry | vendor, PO, status, period | sheet, lines planned/unplanned, amount, accepted by, invoiced | `ServiceEntrySheet` | n/a | MSRV1–MSRV6, MSRV3 entry sheets, ME2S |
| SES acceptance list | list inquiry | approver, status | sheet, amount, submitted at, SLA | `ServiceEntrySheet` | n/a | ML83 / ML84 |
| Audit-sampled invoices | operational report | period, vendor, result | invoice, total, sampled at, released/rejected, reason | `MatchResult` status AUDIT_SAMPLE | n/a | MRBR filtered on stochastic block (ZLSPR S) |
| Consignment settlement history | list inquiry | vendor, period | invoice, ownership change journals, qty, amount | `VendorInvoice` kind CONSIGNMENT | Consignment vendor invoices (D365) | MRKO settlement list |
| ERS / self-billing log | list inquiry | vendor, date | receipt or sheet, ERS invoice, status | `VendorInvoice` kind ERS | Self-billing invoices | MR90 ERS output, MRRL log |

### 3.18.15 Printed and electronic documents

| Document | Trigger | Layout elements (header / line / footer) | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Goods receipt slip / product receipt | receipt POSTED | header: receipt number, PO, vendor, packing slip, receipt date, receiver; lines: product, qty, unit, batch/serial, location, status; footer: signature | PDF, PWA print | `RCV-` | Goods receipt evidence; retain 10 years |
| Receipt label | registration | product, batch, expiry, serial, PO, GS1-128 | ZPL, PDF | none | GS1 where required |
| Self-billing (ERS) invoice | ERS invoice POSTED | header: "Self-billed invoice" legend, vendor and buyer VAT ids, invoice number, date, receipt reference; lines: product, qty, price, tax; footer: totals per tax rate, self-billing agreement reference | PDF/A-3, email; Peppol self-billing (wave 35) | `ERS-{receipt}` / `ERS-{sheet}` | EU VAT Directive Art. 224 (prior self-billing agreement, acceptance procedure); retain 10 years |
| Consignment settlement statement / self-billing invoice | consignment invoice POSTED | header: vendor, period, settlement number; lines: ownership change date, product, qty, price, amount; footer: tax, total | PDF/A-3, email, portal (wave 20) | `CSS-` | Same as ERS (self-billing rules) |
| Service entry sheet | sheet SUBMITTED / ACCEPTED | header: sheet number, PO line, period, vendor, accepted by/at; lines: service item, qty, unit, price, amount, planned/unplanned; footer: totals, limit remaining | PDF, email, portal | `SES-` | Service acceptance evidence; retain 10 years |
| Invoice reduction / debit note to vendor | exception resolved with "request credit note" | header: invoice reference, reason; lines: disputed lines, expected vs invoiced | PDF, email | `DN-` | Commercial correspondence |
| Invoice approval cover (non-PO) | approval task | invoice image link, coding lines, approver chain | HTML email | n/a | none |
| GR/IR reconciliation report | month end (on demand) | open items with totals vs ledger | PDF, XLSX | n/a | Audit workpaper; retain with period close |

### 3.18.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| GR/IR reconciliation (FR-18.9) | daily 02:30 | legal entity × GR/IR account | run id (entity, date); full recompute | < 3 min per 1M open items | retry 3× | diff ≠ 0 pages AP lead |
| Post pending matched invoices (batch, opt-in) | every 15 min | legal entity | key = invoice id; POSTED skipped | < 1 min per 1k | invoice-level errors left in queue | errors > 20 → ticket |
| Automatic release of blocked invoices | hourly | legal entity | re-evaluates EXCEPTION invoices whose cause cleared (receipt arrived, PO price changed); idempotent | < 2 min | retry | none |
| ERS run (receipts and sheets missed by the consumer) | daily 05:00 | vendor | key = receipt line / sheet line; existing ERS invoice skipped | < 5 min | vendor skipped | ERS invoices created by batch > 0 indicates consumer lag → ticket |
| Consignment settlement run (FR-18.16) | per vendor frequency (default Monday 04:30) | vendor | key = (vendor, period); ownership change lines settled once | < 2 min per 1k lines | vendor skipped and listed | failures > 0 → ticket |
| Invoicing plan run (FR-18.17) | daily 04:00 | legal entity | key = plan row id | < 1 min per 5k rows | row-level error | errors > 0 → ticket |
| GR/IR maintenance proposal (write-off of aged residuals) | monthly, day 1 06:00 | legal entity | key = (open item, period); proposal reviewed before posting | < 2 min | n/a | proposal ready notification |
| Capture DLQ reprocessing | every 30 min | adapter | key = adapter document id | < 1 min | after 5 attempts park | DLQ depth > 50 → ticket |
| Stochastic sample rate report | monthly day 2 | legal entity | key = period | < 30 s | retry | achieved rate deviates > 2 points from configured → warning |
| SES acceptance reminders and escalation | Temporal timers | sheet | timer per step | n/a | workflow retry | sheets pending > SLA → manager notification |

### 3.18.17 Security duties and privileges

| Duty | Privileges (entry points: API paths / screens / jobs) | Segregation-of-duties conflicts | Default roles that include it | AX duty / SAP authorization reference |
|---|---|---|---|---|
| ReceivingMaintain | arrivals, receipts create/register; receiving workspace; PWA | none | Receiving clerk | AX "Maintain product receipts" (VendPackingSlipMaintain); SAP M_MSEG_BWE 101 activity 01 |
| ReceivingPost | receipts :post, :correct, :cancel | with PurchaseOrderMaintain (w17), VendorInvoicePost | Receiving supervisor | AX "Post product receipts"; SAP M_MSEG_BWE post |
| ServiceEntryMaintain | service entry sheets create/submit; portal entry (w20) | with ServiceEntryAccept on same sheet | Requester, Vendor user (portal), Project manager | n/a; SAP ML81N (M_SRV_LS activity 01) |
| ServiceEntryAccept | accept, revoke, accept-bulk | with ServiceEntryMaintain (same sheet), PurchaseOrderMaintain (same PO) | Cost center owner, Project manager | SAP ML85 / M_SRV_LS release (activity 43) |
| VendorInvoiceMaintain | invoices create, :propose-lines, :match; invoice screen | with VendorMaintain (w7), PaymentProposalMaintain (w19) | AP clerk | AX "Maintain vendor invoices" (VendInvoiceInfoTableMaintain); SAP M_RECH_WRK, MIRO/MIR7 |
| VendorInvoiceCapture | invoices:capture (adapter/service), capture review | none | AP clerk, service account | AX "Process vendor invoice capture" (D365); SAP VIM roles |
| VendorInvoicePost | invoices :post; credit notes; subsequent debit/credit; ERS, consignment, plan runs | with VendorInvoiceMaintain when `ap_four_eyes=true`, ReceivingPost, PaymentRunExecute (w19) | AP supervisor | AX "Post vendor invoices"; SAP M_RECH_WRK post, FB60 |
| InvoiceExceptionResolve | exception tasks :accept, request credit, adjust PO, reject | with VendorInvoicePost under four-eyes; amount ≤ approver limit (FR-17.17) | Buyer, Receiving supervisor, AP manager | AX "Approve vendor invoice variances"; SAP MRBR (M_RECH_SPG release blocked invoices) |
| InvoiceAuditRelease | audit-sampled invoices release/reject | with VendorInvoicePost (same invoice) | Internal auditor, AP manager | SAP MRBR stochastic block release (M_RECH_SPG) |
| MatchingPolicyMaintain | matching policies, stochastic rule, matching:evaluate tester | with InvoiceExceptionResolve | AP administrator | AX "Maintain invoice matching setup"; SAP OMR6/OMRX (S_TABU_DIS) |
| APInquire | GR/IR report, invoice lists, match details, SES lists | none | AP clerk, Controller, Auditor | AX "Inquire into vendor invoices"; SAP F_BKPF_BUK display, MB5S |
| GrirWriteOff | GR/IR maintenance proposal post | with VendorInvoicePost | AP manager, Controller | SAP MR11 (M_RECH_WRK + F_BKPF_BUK) |

### 3.18.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| RCV-OVR-001 | error | Receipt of {qty} exceeds ordered {ordered} plus over-delivery tolerance {pct} % | FR-18.1 | Reduce quantity or raise tolerance on the PO line | FR-18.1 |
| RCV-UND-001 | warning | Line {line} received {qty} below under-delivery tolerance; line stays open | FR-18.1 | Set close short or await remaining delivery | FR-18.1 |
| RCV-DUP-001 | error | Packing slip {number} already used for vendor {vendor} on receipt {receipt} | FR-18.2 | Correct the packing slip number | FR-18.2 |
| RCV-PER-001 | error | Receipt date {date} is in a closed INVENTORY period | Period control | Use the first open date | FR-18.2 |
| RCV-COR-001 | error | Correction {delta} would reduce received quantity below invoiced {invoiced} | FR-18.3 | Post a credit note first | FR-18.3, BR-18.12 |
| RCV-REG-001 | error | Registration is required before receipt for {product} | `receipt_requirements` | Register the line first | FR-18.1 |
| AP-DUP-001 | error | Invoice {number} from {vendor} already exists ({existing}, {date}) | Duplicate check | Verify with vendor; cancel the duplicate | FR-18.4 |
| AP-CAP-001 | warning | {n} captured fields below confidence {threshold}; confirm before posting | OCR confidence | Review highlighted fields | FR-18.4 |
| AP-TOT-001 | error | Entered total {entered} differs from computed total {computed} by more than {small_diff} | Keying error | Correct the total or lines | FR-18.4, BR-18.5 |
| AP-MAT-001 | warning | Line {line}: price variance {amount} ({pct} %) exceeds tolerance | FR-18.5 | Accept, adjust PO or request credit | FR-18.5, FR-18.7 |
| AP-MAT-002 | warning | Line {line}: invoiced quantity {qty} exceeds received uninvoiced {received} | FR-18.5 | Wait for receipt or request credit | FR-18.5 |
| AP-MAT-003 | warning | Invoice total variance {amount} exceeds tolerance | FR-18.5 | Review charges and tax | FR-18.5 |
| AP-MAT-004 | warning | Invoice currency {inv_ccy} differs from PO currency {po_ccy} | BR-18.3 | Confirm or request corrected invoice | BR-18.3 |
| AP-MAT-005 | info | Invoice selected for audit sample (rule {rule}); posting held for release | FR-18.15 | Internal auditor releases or rejects | FR-18.15 |
| AP-ACC-001 | error | Variance {amount} exceeds your approval limit {limit} | AcceptVariance limit | Escalate to an approver with a higher limit | FR-18.7 |
| PER-AP-001 | error | Posting date {date} is in a closed AP period | BR-18.10 | Change posting date to first open day | BR-18.10 |
| AP-ERS-001 | error | ERS invoice blocked: PO price changed since receipt ({old} → {new}) | BR-18.9 | Buyer confirms price | BR-18.9 |
| AP-SES-001 | error | Service entry sheet {sheet} overlaps accepted sheet {other} for item {item} | BR-18.13 | Adjust period or item | BR-18.13 |
| AP-SES-002 | error | Sheet {sheet} cannot be revoked: {amount} already invoiced | BR-18.13 | Credit the invoice first | FR-18.14 |
| AP-CON-001 | error | Consignment settlement for {vendor}: no trade agreement price for {product} on {date} | Missing consignment price | Create consignment trade agreement row | FR-18.16 |
| AP-IP-001 | error | Invoicing plan row {row} requires an accepted receipt or service entry sheet for the period | BR-18.16 | Accept the sheet or set settle without receipt | FR-18.17 |
| AP-SUB-001 | error | Subsequent credit {amount} exceeds invoiced amount {invoiced} on line {line} | BR-18.17 | Reduce the amount | FR-18.18 |
| AP-OTV-001 | error | One-time payee {name} exceeds limits ({amount} / {count} uses); create a vendor | FR-18.19 | Create a vendor in the party model | FR-18.19 |
| AP-BNK-001 | error | Bank account {account} fails validation ({rule}) | IBAN mod-97 / ABA checksum | Correct the bank account | FR-18.19 |
| AP-GRIR-001 | error | GR/IR report total {report} differs from ledger balance {ledger} by {diff} | Manual journal or defect | Investigate vouchers listed in the diff event | FR-18.9 |

### 3.18.19 Performance budgets and capacity

| Operation | Volume assumption (per tenant per day / peak per minute) | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Receipt post | 8,000 receipts / 200 per min | 400 ms / 1.5 s / 3 s | 200 receipts/min | receipt ≈ 1.5 KB + lines 500 B; GrirOpenItem 300 B | (tenant, vendor, packing_slip_number) unique; GrirOpenItem (po_line_id, status) | Ledger `Post` queued in Temporal; receipt returns POSTING then POSTED ≤ 10 s |
| Invoice create + match | 10,000 invoices / 150 per min | 80 ms / 300 ms / 700 ms | 150 rps | invoice ≈ 3 KB + lines 600 B + page images 300 KB in object store (≈ 1 TB/year at 10k/day) | invoices (tenant, vendor_id, lower(vendor_invoice_number)) partial unique | Matching moved to async worker; UI shows MATCHING spinner ≤ 5 s |
| Matching evaluate (pure) | 50k / 1,000 per min | 10 ms / 100 ms / 200 ms | 500 rps | none | in-memory policy cache by scope | Policy cache warm; p95 ≤ 250 ms |
| Invoice post | 10,000 / 150 per min | 500 ms / 2 s / 4 s | 100 rps | VendorTransaction (w19) 400 B | n/a | Ledger post backpressure via Temporal |
| ERS consumer | 3,000 receipts / 100 per min | lag 2 s / 10 s / 30 s | 50 invoices/s | as invoices | consumer group `payables-ers` partitions = 12 | lag alert at 100 messages; batch job catches up |
| Service entry sheet accept | 2,000 / 50 per min | 300 ms / 1 s / 2 s | 50 rps | sheet ≈ 2 KB | (po_line_id, status) | same as receipt post |
| GR/IR report (1M open items) | 50 runs | 1 s / 4 s / 8 s | n/a | n/a | aggregate on (vendor_id, status) | Served from materialized daily snapshot + intraday delta |
| Capture intake (OCR adapter) | 5,000 / 100 per min | 200 ms / 800 ms / 2 s | 100 rps | payload JSON 20 KB + images | DLQ topic `payables.capture.dlq` | 429 to adapter with `Retry-After` |

### 3.18.20 Localization and compliance hooks

| Concern | Varies by | Country packs touching it (wave 48) | Extension point used |
|---|---|---|---|
| E-invoice intake formats (Peppol BIS 3.0, XRechnung, FatturaPA/SDI, Factur-X, CFDI, NF-e) and mandatory clearance | Country e-invoicing mandates | DE, IT, FR, ES, PL, MX, BR, IN | `:capture` source UBL/structured (wave 35 adapters); invoice keeps original XML as legal archive |
| Self-billing rules (prior agreement, "self-billing" legend, vendor acceptance) | EU VAT Art. 224, UK HMRC self-billing | EU, UK | ERS/consignment templates (3.18.15) with country legend; vendor `self_billing_agreement_ref` |
| Invoice archiving (original format, integrity, 8–10 year retention; GoBD, French PAF) | Country retention law | DE, FR, IT, ES | Object store write-once bucket with retention lock; hash on VendorInvoice |
| Tax determination on receipt and invoice (reverse charge, use tax accrual, import VAT) | VAT/GST/sales tax regimes | all | Wave 13 tax engine call in `PostInvoice`; use-tax accrual on 2-way invoices |
| GR/IR and accrual presentation (balance sheet regrouping for GR/IR debit balances) | Local GAAP | DE (HGB), FR (PCG) | F.19-style regrouping job hook in wave 14 close; GrirOpenItem sign split |
| Withholding on services (e.g., IN TDS, BR ISS/IRRF, US backup withholding) at invoice | Country withholding | IN, BR, MX, US | Wave 13 withholding engine invoked on invoice lines with service products; payment-time withholding in wave 19 |
| Invoice numbering of self-billed documents (gap-free) | IT, ES, PT | IT, ES, PT | Number sequence per legal entity with `gap_free=true` |
| Language of vendor-facing documents | Vendor language | all | Wave 15 templates per language |
| Date and amount formats in capture parsing | Locale | all | Capture adapter locale hint per vendor |
| Privacy of one-time payee data | GDPR, CCPA | EU, US-CA | Field-level encryption, retention purge after legal retention (wave 2) |

### 3.18.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| SAP 13.12 — ME21N service item (D) services PO [#128] | PARTIAL-18 | ADDED as FR-17.23 (order) and FR-18.14 (receipt) | Service PO defined in wave 17; acceptance and posting here |
| SAP 13.12 — MSRV1 / MSRV3 service reports [#131] | PARTIAL-18 | ADDED as FR-18.14 | 3.18.14 service entry sheet lists |
| SAP 13.14 — OMRX stochastic block (T169P) [#161] | GAP (propose 18) | ADDED as FR-18.15 | Deterministic seeded sampling, auditor release |
| SAP 13.15 — ML81N / ML82 / ML83 / ML84 / ML85 service entry sheet create, display, list, accept/revoke, collective release [#167] | PARTIAL-18 | ADDED as FR-18.14 | Acceptance workflow, revoke, accept-bulk |
| SAP 13.15 — MSRV1 … MSRV6 service lists (master data) [#169] | PARTIAL-18 | ADDED as FR-18.14 | 3.18.14 |
| SAP 13.15 — MIRO with SES reference [#170] | PARTIAL-18 | ADDED as FR-18.14 | Invoice proposes lines from accepted sheets; match to sheet amount |
| SAP 13.15 — ML91 / ML92 / ML93 SES archiving / lists [#171] | PARTIAL-18 | SUPERSEDED by wave 46 archiving policy | Lists covered in 3.18.14; archiving uses the platform retention/archival service rather than a per-object archive run |
| SAP 13.15 — Fiori: Manage Service Entry Sheets – Lean Services / Create Service Entry Sheet [#172] | PARTIAL-18 | ADDED as FR-18.14 | Desktop and portal (FR-20.20) entry |
| SAP 13.15 — MSRV1 … MSRV6 service lists (reports) [#175] | PARTIAL-18 | ADDED as FR-18.14 | 3.18.14 |
| SAP 13.15 — ME2S services per purchase order [#176] | PARTIAL-18 | ADDED as FR-18.14 | 3.17.14 "Services per purchase order" and 3.18.14 |
| SAP 13.15 — MSRV3 entry sheets list [#177] | PARTIAL-18 | ADDED as FR-18.14 | 3.18.14 |
| SAP 13.15 — ML83 / ML84 SES acceptance list [#178] | PARTIAL-18 | ADDED as FR-18.14 | 3.18.14 "SES acceptance list" |
| SAP 13.15 — ML85 collective release of SES [#179] | PARTIAL-18 | ADDED as FR-18.14 | `:accept-bulk` |
| SAP 13.15 — MRRL for services (ERS for SES) [#180] | PARTIAL-18 | ADDED as FR-18.14 | ERS invoice per accepted sheet |
| SAP 13.15 — Service specification screen layout, SES number ranges, SES release procedure (ESSR) [#182] | PARTIAL-18 | ADDED as FR-18.14 | `ses_number_sequence`, `ses_acceptance_workflow` (3.18.13); screen layout SUPERSEDED by wave 3 metadata form layouts |
| SAP 13.15 — Default values for SES acceptance (immediate GR, account assignment) [#184] | PARTIAL-18 | ADDED as FR-18.14 | Acceptance is the posting; account assignment defaults from the PO line |
| SAP 13.15 — Lean services activation, product type service, lean SES settings [#186] | PARTIAL-18 | ADDED as FR-18.14 | Service products (wave 8 product type SERVICE) and SES settings in 3.18.13 |
| SAP 13.2 — One-time vendor (account group CPD) [#23, register wave 7] | PARTIAL-7 | ADDED as FR-18.19 (document-level payee on invoice) | Register assigns the party role to wave 7; the document-level capture and payment use are closed here and in FR-19.23 |
| SAP 13.2 — OBD3 one-time account (T077K) [#37, register wave 7] | PARTIAL-7 | ADDED as FR-18.19 | `one_time_vendor_account` parameter (3.18.13) |

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
| ACH prenotes | Payments → Prenotes (ACH), vendor bank account prenote status, "Update vendor bank accounts status", Bank account → Prenote setup | Not standard (DMEE custom zero-dollar entry) | `Prenote` (NACHA transaction code 23/33, zero amount) with bank account status lifecycle (FR-19.15) | full |
| Payment calendar and bank holidays | Payment calendar (D365), Payment days, bank holidays / calendar per bank | Factory calendar on house bank / payment method (FBZP "bank calendar"), T052 payment terms "next business day" | `PaymentCalendar` rules shifting due dates and execution dates by bank calendar (FR-19.16) | full |
| Balance confirmations | n/a (vendor statement only) | F.18 / F.98 balance confirmation (vendor), reply processing F.19-style | `BalanceConfirmation` runs with reply capture (FR-19.17) | full |
| AP correspondence | Vendor account statement (VendAccountStatement), payment advice | Correspondence F.64, types OB77/OB78/OB79 (account statement, payment notice, open items list) | `CorrespondenceType` (statement, payment notice, open items, balance confirmation) with template and trigger (FR-19.18) | full (conditional print routing in wave 46) |
| Special G/L transactions (vendors) | Prepayment posting profiles; no guarantees / noted items | OBYR special G/L indicators A (down payment), F (down payment request), G (guarantee), noted items; bill of exchange W | `VendorTransaction` types NOTED_ITEM (statistical) and GUARANTEE with separate reconciliation accounts (FR-19.19); bills of exchange in wave 36 | partial (bills of exchange in wave 36) |
| Installment payment terms | Payment schedules (PaymSched, multiple due dates per invoice) | OBB9 installment terms (T052R) | Installment split of vendor open items (FR-19.20) | full |
| Vendor-initiated direct debits (SEPA B2B) | Method of payment with "vendor collects"; no mandate register | Payment method for direct debit at vendor; mandate reference stored as text | `VendorDebitMandate` and payment method `DEBIT_BY_VENDOR` (FR-19.21) | full |
| Bank account change control | D365 vendor bank account change workflow / approval | Dual control on sensitive fields (FK08 confirm, S_ALR_87010039 list, FI12) | Dual approval and payment cooling-off on vendor bank account changes (FR-19.22) | improved |
| One-time payee payment | One-time supplier payment via payment journal | CPD account paid via F110 with document-level bank details | Payment to `one_time_payee` (FR-19.23) | full |

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

- **FR-19.15 ACH prenotes** — Should. The system shall generate prenote entries (NACHA entry detail with transaction code 23 checking / 33 savings, amount 0.00, SEC code per method) for new or changed vendor bank accounts whose payment method requires a prenote, set the bank account `prenote_status` to `PENDING` with `prenote_sent_at`, and move it to `CONFIRMED` automatically after `prenote_wait_days` (default 6 banking days, per NACHA) unless a return (R03, R04, C01–C13 NOC) is ingested via FR-19.8, which sets `REJECTED` or applies the notification of change; payment proposals exclude vendors whose required prenote is not CONFIRMED (`AP-PRN-001`) and a periodic job updates statuses (AX "Update vendor bank accounts status").

- **FR-19.16 Payment calendar and bank-holiday shifting** — Must. The system shall maintain `PaymentCalendar` rules per legal entity, payment method and bank account (AX payment calendar / payment days; SAP bank calendar on house bank) referencing wave 6 calendars with bank holidays (TARGET2, US Federal Reserve, UK BACS etc.) and apply them: (a) invoice due dates computed from terms that fall on a non-banking day shift per rule `NEXT`, `PREVIOUS` or `NEXT_UNLESS_MONTH_CHANGE`; (b) proposal `payment_date` and pain.001 `ReqdExctnDt` are validated to be banking days of the debtor bank (`AP-CAL-001`), with `cutoff_time` per method; (c) payment days (e.g., pay only on Tuesdays) restrict proposal payment dates. The same rule set is used by AR (wave 30) for customer due dates.

- **FR-19.17 Vendor balance confirmations** — Should. The system shall run a balance confirmation (SAP F.18 / F.98) for selected vendors as of a key date, rendering a confirmation letter per vendor (open items and balance, reply slip), sending it by email or portal (wave 20), capturing replies (AGREED, DISAGREED with vendor balance and comment, NO_REPLY) with a reconciliation list of differences, and storing the run as audit evidence; the same framework serves AR in wave 30.

- **FR-19.18 AP correspondence types** — Should. The system shall define `CorrespondenceType` records (VENDOR_STATEMENT, PAYMENT_NOTICE, OPEN_ITEMS_LIST, BALANCE_CONFIRMATION, PAYMENT_ADVICE) each with a wave 15 template, channel (email, portal, print), trigger (on demand, on event such as `payment.posted`, or scheduled), and a correspondence log per vendor (SAP F.64 maintain/print, OB77–OB79); conditional routing per recipient is extended in wave 46.

- **FR-19.19 Noted items and guarantees** — Could. The system shall support vendor transaction types NOTED_ITEM (statistical, no ledger posting, visible in open items and proposals as reminders, e.g., expected down payment requests; SAP special G/L F) and GUARANTEE (guarantee received from vendor, posted to a statistical guarantee account pair with expiry date and alert; SAP special G/L G), both excluded from AP control reconciliation and shown separately in aging; bills of exchange and promissory notes (SAP W, AX promissory note journals) are wave 36.

- **FR-19.20 Installment payment terms** — Should. The system shall split a posted invoice's vendor transaction into installment open items when its payment terms (wave 7) define installments (AX payment schedule; SAP OBB9 T052R: installment n, percent, own terms per installment), each installment carrying its own due date, discount date and amount, with rounding residual on the last installment; settlement, proposals and aging operate on installments; the invoice header shows total and installment status.

- **FR-19.21 Vendor-initiated direct debits** — Could. The system shall record `VendorDebitMandate` (vendor, creditor identifier, mandate reference, scheme SEPA CORE/B2B or US ACH debit authorization, max amount, valid from/to, status) for vendors that collect by direct debit, a payment method of type `DEBIT_BY_VENDOR` whose proposals create Payments without a file (status SENT on the pre-notified collection date), and expected-debit lines for bank reconciliation in wave 36; a debit without an active mandate or above `max_amount` is flagged on the AP workspace.

- **FR-19.22 Vendor bank account change control** — Must. The system shall require dual approval (approver ≠ requester) for any create/change of a vendor bank account (wave 7 data) used by payment, log old/new values, notify the vendor's primary contact through a channel other than the one used for the change request, and block payment to the changed account for `bank_change_cooling_days` (default 3) after approval unless an AP manager overrides with reason (`AP-BNK-003`); the sensitive-field change report lists changes per period (SAP S_ALR_87010039 / FK08).

- **FR-19.23 Payment to one-time payees** — Should. The system shall pay invoices carrying a `one_time_payee` (wave 18 FR-18.19) as one Payment per invoice using the document-level bank account, include them in proposals only after four-eyes approval of the invoice, emit them in pain.001/NACHA with the payee name and address from the document, and print the payee on the advice and positive-pay record.

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
| `dev.passage.payables.prenote.status.changed.v1` | prenote sent / confirmed / rejected / NOC applied | vendor_id, bank_account_id, old_status, new_status, return_code | notifications, vendor master (w7) |
| `dev.passage.payables.vendor-bank-account.changed.v1` | bank account change approved | vendor_id, bank_account_id, changed fields (masked), approved_by, cooling_until | notifications (vendor contact), audit (w48), security monitoring |
| `dev.passage.payables.balance-confirmation.replied.v1` | vendor reply captured | run_id, vendor_id, our_balance, vendor_balance, status | notifications, audit (w48) |

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
- **BR-19.13** Prenote status machine: `NONE → PENDING → CONFIRMED`; `PENDING → REJECTED` on return R02/R03/R04; NOC (C01–C13) updates the bank account fields and restarts at PENDING when the account number or routing changed; any change to account or routing number of a CONFIRMED account resets to NONE and requires a new prenote.
- **BR-19.14** Due-date shifting applies to the due date and to discount dates only when `shift_discount_dates=true` (default false, matching SAP behavior of not shifting discount days); a shifted due date never crosses the next due-date of another installment of the same invoice.
- **BR-19.15** Installments: amount of installment k = round(total × pct_k, minor) for k < n; the last installment absorbs the residual so Σ installments = invoice total exactly; cash discount applies per installment from its own terms.
- **BR-19.16** Bank account change cooling-off is measured in banking days of the paying bank calendar; payments already in a SENT file are unaffected; an override records approver, reason and the out-of-band confirmation reference.
- **BR-19.17** Noted items and guarantees never affect `open_amount_acct` totals reconciled to the AP control account; they cannot be settled against invoices and are closed by explicit release or expiry.

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
- **AC-19.11** (FR-19.15) Given a new vendor bank account with ACH method requiring a prenote, when the prenote file is generated and no return arrives for 6 banking days, then status becomes CONFIRMED and the vendor is included in the next proposal; when an R03 return is ingested on day 2, status is REJECTED and the proposal excludes the vendor with `AP-PRN-001`.
- **AC-19.12** (FR-19.16) Given terms net 30 producing due date Saturday 2026-12-26 and rule NEXT with the TARGET2 calendar (2026-12-28 is a banking day), when the invoice posts, then the due date is 2026-12-28; a proposal with payment date 2026-12-25 is rejected with `AP-CAL-001`.
- **AC-19.13** (FR-19.20) Given an invoice of 1,000.00 USD with installment terms 3 × 33.33 %, when posted, then three open items of 333.33, 333.33 and 333.34 exist with due dates 30/60/90 days, and aging shows each in its bucket.
- **AC-19.14** (FR-19.22) Given a vendor bank account change requested by user A, when user A tries to approve it, then it is rejected; approved by user B, a payment proposal on the next banking day excludes the vendor with `AP-BNK-003` until 3 banking days pass, and the change report lists the change with old/new masked values.
- **AC-19.15** (FR-19.17) Given a balance confirmation run for 20 vendors, when 15 reply AGREED, 3 DISAGREED with balances and 2 do not reply, then the reconciliation list shows 3 differences with amounts and 2 NO_REPLY after the reply deadline.

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

### 3.19.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `write_off_limit` / `auto_write_off` | legal entity (per currency) | numeric / bool | 1.00 / true | ≥ 0 | FR-19.2 step 5 | Accounts payable parameters → Settlement → "Maximum penny difference" / over-under payment | OBA3 / OBXT tolerance groups for vendors and payment differences (T043G) |
| `discount_on_partial` | legal entity | bool | true | true/false | BR-19.3 | Settlement → "Calculate cash discount for partial payments" | OBA3 cash discount adjustment for partial payments |
| `use_cash_discount` | vendor | enum | NORMAL | NORMAL, ALWAYS, NEVER | FR-19.3 | Vendor → "Use cash discount" | n/a (always per terms; ZTERM override) |
| `discount_lookahead_days` / `cost_of_capital_pct` / `optimize` | legal entity | int / numeric / bool | 7 / 8 / false | | FR-19.4 | Payment proposal → "Cash discount" selection | F110 parameter "next payment run date" logic |
| `min_payment` / `max_payment` | payment method | numeric | 1.00 / none | ≥ 0 | BR-19.10 | Method of payment → minimum/maximum | FBZP payment method per company code: minimum/maximum amount |
| `payment_methods` | legal entity | table | TRANSFER (pain.001), ACH (NACHA), CHECK, WIRE | type, format, bank account, clearing account, check lot, grace days, cutoff time | FR-19.5 | Methods of payment (VendPaymModeTable), payment specifications, payment fees | T042Z payment methods per country, T042E per company code, FBZP |
| `bank_profiles` | bank account | record | SEPA / US / UK / CH variants | ≤ 20 override keys | FR-19.6 | ER format configurations per bank | DMEE tree / PMW format (OBPM1–OBPM4) |
| `pain001_version` | bank profile | enum | 001.001.09 | .03, .09 | open question 1 | ER format version | DMEE format version |
| `check_lots` / `check_layout` | bank account | table / template | none / default MICR layout | lot ranges, layout fields | FR-19.7 | Check layout (per bank account), check number sequences | FCHI check lots, FBZ5 print program RFFOUS_C |
| `positive_pay_format` | bank account | enum | CSV | CSV, BAI-like fixed, bank XML | FR-19.7 | Positive pay formats | FCHX check extract |
| `proposal_approval_bands` | legal entity | table | > 100,000 USD two approvers | bands | FR-19.4 | Payment journal approval workflow | F110 release via BCM approval (FBPM rules) |
| `aging_buckets` / `aging_basis` | legal entity, user override | list / enum | 0–30, 31–60, 61–90, 91–180, >180 / DUE_DATE | DUE_DATE, DOCUMENT_DATE, POSTING_DATE | FR-19.11 | Aging period definitions | S_ALR_87012078 variant (days in arrears) |
| `prenote_required` / `prenote_wait_days` | payment method / legal entity | bool / int | true for ACH / 6 | 3–10 banking days | FR-19.15 | Bank account → Prenote setup; Method of payment "Prenote required" | n/a |
| `payment_calendar_rules` | legal entity × method × bank account | table | NEXT, bank calendar from wave 6 | NEXT, PREVIOUS, NEXT_UNLESS_MONTH_CHANGE; payment days | FR-19.16 | Payment calendar (D365), Payment days, Bank holidays | House bank calendar, FBZP "bank calendar", T052 "next business day" |
| `shift_discount_dates` | legal entity | bool | false | true/false | BR-19.14 | Payment calendar "Cash discount date" shift option | n/a |
| `correspondence_types` | legal entity | table | VENDOR_STATEMENT, PAYMENT_NOTICE, OPEN_ITEMS_LIST, BALANCE_CONFIRMATION, PAYMENT_ADVICE | template, channel, trigger | FR-19.18 | Print management for vendor documents | OB77 / OB78 / OB79 correspondence types and print programs (T048, T048B) |
| `balance_confirmation_reply_days` | legal entity | int | 21 | 7–90 | FR-19.17 | n/a | F.18 reply deadline (letter text) |
| `special_gl_types` | legal entity | table | NOTED_ITEM, GUARANTEE with accounts | | FR-19.19 | Prepayment posting profiles | OBYR special G/L indicators (T074) |
| `bank_change_cooling_days` / `bank_change_dual_approval` | legal entity | int / bool | 3 / true | 0–15 | FR-19.22 | Vendor bank account change workflow (D365) | Sensitive fields for dual control (V_T055F), FK08 |
| `auto_settle_credit_notes` | legal entity | bool | false | true/false | FR-19.14 | Settlement → "Automatic settlement" | F.13 automatic clearing |
| `withholding_at_payment` | legal entity | bool | per wave 13 country pack | | FR-19.2 step 7 | Withholding tax parameters | Extended withholding at payment (T059P) |

### 3.19.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Vendor transactions (open / all) | list inquiry | vendor, status, type, date range, currency | voucher, document, dates, amount, open, discount available, hold | `VendorTransaction` | Vendor transactions (VendTrans), Open transactions | FBL1N / FBL1H vendor line items |
| Vendor balance | list inquiry | vendor group, currency, as-of | balance txn/acct, last payment | `VendorTransaction` aggregate | Vendor balance list | FK10N / S_ALR_87012082 vendor balances |
| Vendor aging | operational report | as-of, basis, buckets, group | buckets, total, control balance, diff | FR-19.11 projection | Vendor aging report (VendAgingReport) | S_ALR_87012078 due date analysis, RFKOPR00 aging |
| Cash requirements / due forecast | operational report | due date range, method, bank account | amount due by day and currency, discounts available | `VendorTransaction` open | Cash flow forecast (AP part) | S_ALR_87012083 open items for printing; FF7A cash position |
| Settlement history | drill-down | vendor, date, kind | settlement, items, discount, FX, write-off | `Settlement` | Closed transaction editing / Settlement history | FBL1N cleared items, FB03 clearing document |
| Cash discount inquiry | list inquiry | date range, taken/lost | invoice, discount available, taken, lost | `SettlementLine` | Cash discount inquiry | S_ALR_87012085 payment history with discount |
| Payment proposal log | drill-down | proposal | included/excluded items with reasons | `PaymentProposal` | Payment proposal (VendPaymProposal) | SAPF110V proposal log, FBZ0 |
| Payment history / payment list | list inquiry | method, status, date, bank account | payment number, vendor, amount, file, status, reason code | `Payment` | Payment history | S_P99_41000099 payment list, FBL1N payments |
| Payment files | list inquiry | format, bank, status | file number, count, control sum, sha256, downloaded by | `PaymentFile` | Generated payment files | FDTA DME administration |
| Positive pay inquiry | list inquiry | bank account, date | check number, amount, payee, void | `Payment` CHECK | Positive pay inquiry | FCHN check register |
| Check register | operational report | check lot, date | check number, payee, amount, status (issued/void/cleared) | `Payment` CHECK | Check journal (BankChequeTable) | FCHN, FCH6 |
| Prenote status | list inquiry | status, vendor | bank account (masked), status, sent at, return code | vendor bank accounts | Prenote inquiry | n/a |
| Down payments / prepayments open at key date | list inquiry | as-of, vendor | prepayment, applied, open | `VendorTransaction` PREPAYMENT | Prepayment inquiry | S_ALR_87012105 down payments at key date |
| Balance confirmation reconciliation | operational report | run | vendor, our balance, vendor balance, status, difference | `BalanceConfirmation` | n/a | F.18/F.98 reply list |
| Vendor bank account changes (sensitive fields) | operational report (audit) | period, vendor | field, old/new (masked), requested by, approved by, cooling until | audit log | Vendor bank account change tracking (D365) | S_ALR_87010039 sensitive field changes, FK08 |
| AP control reconciliation | operational report | entity, account, date | open items total, ledger balance, diff | aging job output | Ledger reconciliation (AP) | FAGLF03 / F.03 comparative analysis |

### 3.19.15 Printed and electronic documents

| Document | Trigger | Layout elements (header / line / footer) | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| ISO 20022 credit transfer | payment run | per FR-19.6 mapping | XML pain.001.001.09 (or .03) | file number `PF-` | Bank instruction; retain 7–10 years (write-once bucket, 2,555 days default) |
| NACHA file | payment run (ACH) / prenote run | records 1/5/6/7/8/9 per FR-19.7 | fixed-width 94-char ASCII | file id modifier A–Z/0–9 per day | NACHA Operating Rules record retention (2 years min); retained 7 years |
| Check with remittance stub | payment run (CHECK) | check face: date, payee, amount in words and digits, MICR (E-13B) line with check number, routing, account; stub: invoices settled, discounts | PDF on check stock (MICR toner) | check lot number | Escheat/unclaimed property follow-up records; 7 years |
| Positive pay file | after check run / void | bank account, check number, amount, payee, issue date, void flag | CSV / bank format | per file | Bank agreement |
| Payment advice / remittance advice | payment posted / sent | header: payer, payee, payment number, date, method, amount; lines: invoice number, date, gross, discount, withholding, paid | PDF, email, portal (wave 20), EDI 820/REMADV (wave 35) | payment number | none beyond accounting retention |
| Vendor account statement | on demand / scheduled | header: vendor, period; lines: opening, documents, payments, closing balance | PDF, email, portal | n/a | none |
| Balance confirmation letter | confirmation run | header: key date, vendor, our balance; lines: open items; reply slip | PDF, email, portal | run number `BC-` | Audit evidence (ISA 505 external confirmations); retain with audit file 10 years |
| Withholding certificate | payment with withholding | payer, payee tax ids, gross, withheld, rate, period | PDF (country form in wave 48) | per country | Country tax law (e.g., IN Form 16A, MX constancia) |
| Prenote notification | prenote sent | vendor, account (masked), expected confirmation date | email | n/a | none |

### 3.19.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Aging reconciliation to AP control | daily 03:00 | legal entity × control account | run id (entity, date) | < 2 min per 1M open items | retry 3× | diff ≠ 0 pages AP lead |
| Discount-expiry digest | daily 07:00 | legal entity | key = (entity, date) | < 30 s | retry | none |
| Scheduled payment proposal (opt-in, SAP F110S) | per schedule (e.g., Tue/Thu 06:00) | legal entity × method | key = (schedule, run date); creates proposal in SELECTED only | < 10 s per 10k items | alert treasurer | proposal not created → ticket |
| pain.002 / NACHA return poller | every 15 min (until wave 36) | bank account | key = message id / return trace number | < 30 s | DLQ unknown ids | DLQ depth > 0 → ticket |
| Prenote status update (FR-19.15) | daily 05:00 | legal entity | key = (bank account, date) | < 1 min | retry | PENDING > 10 banking days → ticket |
| Automatic settlement of credit notes (FR-19.14) | every 30 min | vendor | key = credit note id | < 1 min | item-level error | errors > 0 → ticket |
| Recalculate cash discount and due dates after calendar change (FR-19.16) | on calendar change + nightly 01:30 | legal entity | key = (calendar version, item) | < 2 min per 100k items | retry | changed items reported |
| Balance confirmation run (FR-19.17) | annually / on demand | vendor range | key = run id; letters generated once | < 5 min per 5k vendors | vendor-level errors | NO_REPLY after deadline → list to auditor |
| Correspondence scheduled sends (statements) | monthly day 3 | vendor | key = (vendor, period, type) | < 10 min per 10k vendors | retry 3× | bounce rate > 5 % → ticket |
| Guarantee expiry alert (FR-19.19) | daily 06:00 | legal entity | key = (guarantee, date) | < 10 s | retry | guarantees expiring < 30 days → notification |
| Payment file retention sweep | monthly | bucket prefix | object lock ensures no early delete | < 5 min | none | n/a |

### 3.19.17 Security duties and privileges

| Duty | Privileges (entry points: API paths / screens / jobs) | Segregation-of-duties conflicts | Default roles that include it | AX duty / SAP authorization reference |
|---|---|---|---|---|
| APInquire | vendor transactions, balance, aging, settlement history, payment history | none | AP clerk, Controller, Auditor | AX "Inquire into vendor transactions"; SAP F_LFA1_BUK display, FBL1N |
| APSettle | settlements preview/commit/reverse; payments :reverse, :void; holds | with PaymentRunExecute when four-eyes; with VendorMaintain (w7) | AP supervisor | AX "Settle vendor transactions" (VendSettle); SAP F_BKPF_KOA K clearing F-44 |
| PaymentProposalMaintain | payment proposals create/reselect/edit lines/submit | with PaymentProposalApprove; with VendorMaintain and VendorBankAccountMaintain | AP clerk, Treasury analyst | AX "Generate payment proposals" (VendPaymProposal); SAP F_REGU_BUK activity 02/11 (F110 parameters/proposal) |
| PaymentProposalApprove | proposal approval tasks | with PaymentProposalMaintain, PaymentRunExecute (same proposal) | Treasurer, Controller | AX payment journal approval workflow; SAP BCM release (F_PAYRQ, FBPM approval) |
| PaymentRunExecute | :run; run monitor; retry | with PaymentProposalMaintain, VendorBankAccountMaintain, VendorInvoicePost (w18) | Treasury operator | AX "Generate payments"; SAP F_REGU_BUK activity 21/25 (F110 payment run, print) |
| PaymentFileDownload | payment-files content download, mark transmitted | with VendorBankAccountMaintain | Treasury operator | AX "Export payment files"; SAP FDTA (F_REGU_BUK 25) |
| PaymentStatusIngest | payments/status:ingest (service account), manual return entry | none | service account, Treasury operator | AX "Import payment return files"; SAP FEBAN/BCM status |
| BankProfileMaintain | bank profiles, payment methods, check lots, payment calendars | with PaymentRunExecute | Treasury administrator | AX "Maintain methods of payment"; SAP FBZP (S_TABU_DIS T042*) |
| VendorBankAccountMaintain | request vendor bank account create/change | with VendorBankAccountApprove (same change), PaymentRunExecute, PaymentProposalMaintain | Vendor master clerk | AX "Maintain vendor bank accounts"; SAP F_LFA1_AEN sensitive fields |
| VendorBankAccountApprove | approve bank account changes; cooling-off override | with VendorBankAccountMaintain (same change) | AP manager, Treasurer | SAP FK08 confirm change (F_LFA1_AEN activity 08) |
| CorrespondenceSend | statements, balance confirmations, payment notices; reply capture | none | AP clerk | SAP F.64 / F.18 (F_BKPF_BUK display + correspondence) |
| PrepaymentMaintain | prepayments, noted items, guarantees | with PaymentRunExecute | AP supervisor | AX prepayment journal; SAP F-47/F-48 (F_BKPF_BUK, special G/L) |

### 3.19.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| AP-SET-001 | error | Items belong to different vendors ({v1}, {v2}) | CommitSettlement | Settle per vendor | FR-19.2 |
| AP-SET-002 | error | Applied amount {applied} exceeds available credit {available} | CommitSettlement | Reduce applied amounts | FR-19.2 |
| AP-SET-003 | error | No exchange rate {ccy}→{acct_ccy} on {date} | Missing rate | Enter the rate (wave 12) | FR-19.2 |
| AP-SET-004 | error | Settlement date {date} is in a closed AP period | BR-19.6 | Use an open date | BR-19.6 |
| AP-SET-009 | warning | Item {item} is no longer open; excluded from proposal | BR-19.7 race | None (informational) | BR-19.7 |
| AP-PRP-001 | warning | Vendor {vendor} excluded: net credit balance ({amount}) | BR-19.10 | Review credit notes | BR-19.10 |
| AP-PRP-002 | warning | Item {item} on hold ({reason}) excluded | FR-19.13 | Release hold | FR-19.13 |
| AP-PRP-003 | error | Proposal total {amount} requires approval by {n} approvers | approval bands | Submit for approval | FR-19.4 |
| AP-RUN-001 | error | Bank account {account} inactive or method cutoff {time} passed for {date} | RunPayment | Change payment date or bank account | FR-19.5 |
| AP-RUN-002 | error | Vendor bank account {account} failed {rule} validation | IBAN mod-97 / ABA | Correct bank details | FR-19.5 |
| AP-FIL-001 | error | Payment {payment}: creditor name cannot be transliterated to {charset} | BR-19.9 | Edit vendor name or profile charset | BR-19.9 |
| AP-FIL-002 | error | Generated file failed XSD validation: {xpath} {reason} | Generator / profile defect | Fix bank profile override; regenerate | FR-19.6 |
| AP-STS-001 | warning | Payment {payment} rejected by bank ({code}: {text}); items re-opened | pain.002 RJCT / NACHA return | Correct bank details and re-propose | FR-19.8, BR-19.12 |
| AP-STS-002 | error | Status message references unknown MsgId/EndToEndId {id} | IngestStatus | Investigate DLQ entry | FR-19.8 |
| AP-VOI-001 | error | Check {check} cannot be voided: status {status} | FR-19.9 | Reverse instead | FR-19.9 |
| AP-PRE-001 | error | Prepayment application {amount} exceeds invoice total {total} | BR-19.11 | Reduce application | BR-19.11 |
| AP-PRN-001 | warning | Vendor {vendor} excluded: prenote for account {account} is {status} | FR-19.15 | Wait for confirmation or change method | FR-19.15 |
| AP-CAL-001 | error | Payment date {date} is not a banking day for {bank} (calendar {calendar}) | FR-19.16 | Choose the next banking day {next} | FR-19.16 |
| AP-INS-001 | error | Installment percentages sum to {sum} %, expected 100 % | Terms setup (wave 7) | Correct payment terms | FR-19.20 |
| AP-BNK-002 | error | Bank account change must be approved by a different user | FR-19.22 | Ask another approver | FR-19.22 |
| AP-BNK-003 | warning | Vendor {vendor} excluded: bank account changed on {date}, cooling-off until {until} | FR-19.22, BR-19.16 | Wait or obtain manager override with confirmation reference | FR-19.22 |
| AP-MND-001 | warning | Direct debit by {vendor} of {amount} has no active mandate or exceeds max {max} | FR-19.21 | Review with vendor; dispute debit with bank (wave 36) | FR-19.21 |
| AP-OTV-002 | error | One-time payee invoice {invoice} requires four-eyes approval before payment | FR-19.23 | Complete approval | FR-19.23 |
| AP-AGE-001 | error | Aging total {aging} differs from AP control balance {ledger} by {diff} | Daily reconciliation | Investigate listed vouchers | FR-19.11 |

### 3.19.19 Performance budgets and capacity

| Operation | Volume assumption (per tenant per day / peak per minute) | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Settlement preview | 5,000 / 100 per min | 60 ms / 300 ms / 600 ms | 100 rps | none | VendorTransaction (tenant, vendor_id, status, due_date) | p95 ≤ 1 s; preview limited to 500 items per request |
| Settlement commit | 20,000 (incl. runs) / 2,000 per min in runs | 30 ms / 50 ms / 120 ms per item activity | 200 items/s per worker | Settlement ≈ 500 B + lines 300 B | lock order by id (BR-19.7) | Run extends; deadline-based alert if ETA past cutoff |
| Proposal selection (10k open items) | 20 runs | 3 s / 10 s / 20 s | n/a | proposal lines ≈ 200 B | partial index on status = OPEN | Selection paginated by vendor range |
| Payment run (2,000 payments, 10k items) | 5 runs / day | 2 min / 5 min / 8 min | 40 items/s end-to-end | Payment ≈ 600 B; files 1–5 MB | payments (proposal_id), (end_to_end_id) unique | Worker HPA 2→6; cutoff-time ETA alert |
| File generation (2,000 payments) | 5 / day | 8 s / 30 s / 60 s | n/a | object store 7-year retention ≈ 20 GB/year | per-tenant prefix | Streaming writer; memory bounded at 256 MiB |
| Status ingest (pain.002) | 50 files / day | 1 s / 5 s / 10 s per file | 10k transactions per file | raw messages retained 7 years | (end_to_end_id) lookup | Queue; status lag ≤ 15 min |
| Aging (5k vendors, 1M items) | 200 queries | 300 ms / 2 s / 4 s | n/a | daily snapshot ≈ 50 MB | snapshot table by (as_of, vendor_group) | Served from snapshot + intraday delta; exact recompute async |
| Vendor balance | 50k / 500 per min | 10 ms / 50 ms / 100 ms | 500 rps | n/a | running balance table per vendor × currency | Read replica |

### 3.19.20 Localization and compliance hooks

| Concern | Varies by | Country packs touching it (wave 48) | Extension point used |
|---|---|---|---|
| Payment formats beyond the canonical pain.001/NACHA (UK BACS Standard 18, CH pain.001 SPS, DE DTAZV, JP Zengin, MX SPEI, BR CNAB 240, AU ABA, CA CPA 005) | Local clearing systems | UK, CH, DE, JP, MX, BR, AU, CA | `PaymentFile` generator plug-in interface (format enum + bank profile); XSLT post-processor escape hatch |
| Payment purpose codes and regulatory reporting codes (e.g., CN, IN, AE purpose codes, CBR reporting) | Central bank rules | CN, IN, AE, RU | `Purp/Cd` and `RgltryRptg` from vendor/payment purpose code table (AX "Payment purpose codes") |
| Bills of exchange, promissory notes, postdated checks | IT, ES, FR, TR, JP, BR, APAC practice | IT, ES, FR, TR, JP, BR, MY, SG | Wave 36 instrument model on top of `VendorTransaction` type extension; DEFERRED (see 3.19.21) |
| Withholding at payment and certificates (US 1099/backup withholding, IN TDS, MX ISR, BR IRRF) | Country tax law | US, IN, MX, BR, PH | Wave 13 withholding engine called in settlement step 7; certificate templates per pack; 1099 reporting in wave 48 |
| Cash discount VAT adjustment (discount reduces taxable base in some countries) | VAT regimes | DE, AT, NL, BE | Settlement journal hook `tax.adjust_on_discount` invoking wave 13 |
| Payment calendars and bank holidays | Clearing system calendars (TARGET2, Fedwire, BACS) | all | Wave 6 calendars referenced by FR-19.16 |
| Unclaimed property / escheatment of uncashed checks | US state law | US | Check register aging report + `escheat_due_date` on CHECK payments (wave 48 report) |
| Strong customer authentication / payment approval evidence | PSD2 (bank side), SOX controls | EU, US | Proposal approval workflow evidence, file hash, download audit |
| Vendor bank detail change fraud controls | Internal control frameworks | all | FR-19.22 dual approval and cooling-off |
| Data protection of bank data in files and advices | GDPR, PCI (card) | EU | Masked display, OPA-gated download, encryption at rest |
| Language and format of advices, statements and confirmation letters | Vendor language | all | Wave 15 templates |

### 3.19.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.2.1 — Payments → Prenotes (ACH) [#10] | PARTIAL | ADDED as FR-19.15 | Zero-dollar NACHA entries with status lifecycle |
| AX 12.2.3 — Vendor → Update vendor bank accounts status (prenote results) [#13] | PARTIAL | ADDED as FR-19.15 | Job in 3.19.16 |
| AX 12.2.4 — Payment → Payment calendar (D365) [#15] | PARTIAL | ADDED as FR-19.16 | Due-date and execution-date shifting |
| AX 12.3.4 — Payment → Payment calendar (AR) [#26] | PARTIAL | ADDED as FR-19.16 | Same rule set reused by wave 30 for customer due dates |
| AX 12.5.4 — Bank account → Prenote setup [#38] | PARTIAL | ADDED as FR-19.15 | `prenote_required`, `prenote_wait_days` (3.19.13) |
| AX 12.5.4 — Bank → Bank holidays / Payment calendar [#39] | PARTIAL | ADDED as FR-19.16 | Bank calendars from wave 6 |
| SAP 13.1 — F.97 / F.98 / F.99 balance confirmations (G/L, AP, AR) [#11] | GAP (propose 19) | ADDED as FR-19.17 | Vendor side here; customer side reuses the framework in wave 30; G/L confirmations (F.97) SUPERSEDED by wave 15 audit trail exports |
| SAP 13.1 — OBXR / OBYR special G/L indicators (customers / vendors) [#16] | PARTIAL-19 | ADDED as FR-19.19 | Noted items and guarantees added; bills of exchange DEFERRED to wave 36; customer side wave 30 |
| SAP 13.2 — F.98 vendor balance confirmation [#31] | GAP (propose 19) | ADDED as FR-19.17 | |
| SAP 13.2 — F.64 AP correspondence maintain / print [#33] | PARTIAL-19 | ADDED as FR-19.18 | Correspondence types with log; conditional routing in wave 46 |
| SAP 13.2 — OBYR special G/L indicators vendors (down payments, guarantees, BoE) [#34] | PARTIAL-19 | ADDED as FR-19.19 | Down payments already FR-19.10; guarantees added; BoE DEFERRED to wave 36 |
| SAP 13.2 — Define correspondence types / forms OB77, OB78, OB79 [#36] | PARTIAL-19 | ADDED as FR-19.18 | `correspondence_types` (3.19.13) |
| SAP 13.20 — Supplier condition contracts (purchasing rebates, chargebacks) [#229] | PARTIAL-19 | DEFERRED to wave 26 | Rebate accrual and claim engine belongs with the wave 26 condition engine; AP settlement of the resulting vendor credit notes already uses FR-18.12 and FR-19.2 |
| AX 12.2.1 / 12.2.2 / 12.2.3 / 12.2.4 — Promissory note journals, documents, draw/remit/settle batches, setup [#1, #3, #4, #6; register wave 36] | GAP | DEFERRED to wave 36 | Register assigns treasury instruments to wave 36; noted here because the instrument settles vendor open items; `VendorTransaction.type` is extensible for it |
| AX 12.5.1 / 12.5.4 — Bills of exchange / promissory notes and postdated checks (bank side) [#16, #17, #18, #19; register wave 36] | GAP | DEFERRED to wave 36 | Same |
| SAP 13.2 — F-40 / F-36 / FBW6 bill of exchange payment (AP) [#24; register wave 36] | GAP | DEFERRED to wave 36 | Same |
| SAP 13.1 / 13.2 — OBB8 / OBB9 installment terms [#17, #35; register wave 7] | PARTIAL-7 | ADDED as FR-19.20 (open-item split) | Terms definition stays in wave 7; splitting vendor open items into installments is closed here |
| AX 12.3.1 — Direct debit mandates (SEPA) [#18; register wave 30] / SAP 13.3 SEPA mandate management [#44; register wave 30] | PARTIAL-30 | ADDED as FR-19.21 (vendor-initiated debits only) | Customer-side creditor mandates (pain.008) remain wave 30 |
| SAP 13.2 — One-time vendor (CPD) payment [#23; register wave 7] | PARTIAL-7 | ADDED as FR-19.23 | Invoice capture in FR-18.19 |

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
| Parallel inventory valuation | D365 Global inventory accounting add-in (multiple cost ledgers, accounting conventions, accounting policies per ledger) | Material ledger with multiple currencies/valuations (group valuation, profit-center valuation, parallel currencies in CKMLCR), transfer prices | `ValuationLedger` per wave 12 ledger group with its own costing method and currency (FR-20.15) | full |
| Split valuation / batch valuation | Inventory by dimension cost price (site/warehouse/batch "Financial inventory" flag on dimension group) | Split valuation OMWC/OMWD (valuation categories and types, e.g., domestic/import, new/refurbished), batch valuation (valuation type = batch), T149 | `valuation_type` dimension with `ValuationCategory` (FR-20.16) | full |
| Inventory aging | Inventory aging report (D365, InventAgingReport) with aging period definitions, inventory aging report storage | MC.A / MC50 dead stock / MC46 slow-moving, inventory turnover MC.9 | Inventory aging report by receipt-date layers (FR-20.17) | full |
| Vendor requests and onboarding | Vendor requests (R3: VendRequest*), vendor request types, D365 prospective vendor registration wizard and questionnaires | Ariba SLP registration and qualification; MDG-S supplier requests | `VendorRequest` workflow and portal self-registration with questionnaires (FR-20.18) | full |
| Vendor catalog upload | Vendor collaboration → vendor catalogs (CMR upload) | Ariba catalog (CIF) upload | Portal catalog upload into wave 17 staging (FR-20.19) | full |
| Supplier service entry | n/a | SAP Business Network / Fieldglass supplier SES and timesheets | Portal service entry sheet and timesheet entry (FR-20.20) | full |
| Document distribution to suppliers | Document handling + vendor collaboration documents | DMS document distribution CVI1/CVI2/CVI8, recipient lists, distribution log | `DocumentDistribution` with recipient lists, acknowledgement and log (FR-20.21) | full |
| Portal consignment view | Vendor collaboration consignment inventory (on-hand, replenishment orders, product receipts) | SNC consignment monitor | Portal consignment pages (FR-20.22) | full |
| Alternative valuation / usage variance distribution / WIP at actual / CO-PA revaluation | n/a | CKMLCPAVR, CKMDUVACT/CKMDUVMAT, WIP at actual, KE27 | Not in this wave; production actual costing in waves 21/39 | dropped-with-reason (3.20.21) |

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

- **FR-20.15 Parallel inventory valuation ledgers** — Should. The system shall allow a legal entity to define additional `ValuationLedger` rows bound to wave 12 ledger groups (e.g., IFRS primary in EUR with WEIGHTED_AVG_PERIODIC, US GAAP group ledger in USD with FIFO, tax ledger with LIFO_DATE), each with `costing_method_override` per item model group, `currency`, and `exchange_rate_type`; every value-bearing inventory event writes one `CostTransaction` per valuation ledger (column `valuation_ledger_id`, default ledger = existing behavior), close and recalculation run per valuation ledger (child workflows keyed by ledger × product), and adjustments post only to the ledgers of that ledger group via `Post(JournalEntry)` with ledger-group scope; the value report takes a `valuationLedger` parameter. Cost lookup for waves 21/27 returns the primary ledger unless asked otherwise.

- **FR-20.16 Split valuation and batch valuation** — Should. The system shall support a `valuation_type` storage/tracking dimension governed by a `ValuationCategory` on the product (e.g., ORIGIN with types DOMESTIC/IMPORT; CONDITION with NEW/REFURBISHED; BATCH where each batch is its own valuation type), so that cost layers, running/periodic averages, standard costs and the value report are kept per product × site × valuation type (SAP OMWC/OMWD split valuation, valuation type = batch); the item posting profile can resolve accounts per valuation type (valuation class equivalent); a transfer between valuation types is a TRANSFER journal posting the value difference to revaluation; the category is set at product creation and cannot change while stock exists (`CST-VAL-002`).

- **FR-20.17 Inventory aging report** — Must. The system shall produce an inventory aging report as of a date by product, site, warehouse, item group and valuation type with configurable buckets (default 0–30, 31–90, 91–180, 181–365, > 365 days) based on the receipt date of the remaining cost layers (FIFO layers after settlement; for average methods, oldest-unconsumed approximation using FIFO layer order), showing quantity and value per bucket and totals that tie to the value report; report setups (bucket sets, grouping) are saved per user or legal entity (AX Inventory aging report setup) and the report can be stored as a snapshot for month-end evidence.

- **FR-20.18 Vendor requests and prospective vendor onboarding** — Should. The system shall support `VendorRequest` (types ADD_VENDOR, ADD_TO_CATEGORY, CHANGE_STATUS, per AX vendor request types) raised internally or via a public portal registration page (D365 prospective vendor registration): the prospect enters company data, contacts, bank details (not activated), tax ids and answers to the `OnboardingQuestionnaire` assigned by category or request type (file uploads for certificates); the request runs a wave 4 approval workflow (category manager, compliance, AP master data) with duplicate check against wave 7 parties (tax id, name similarity ≥ 0.9), sanctions screening hook, and on approval creates the wave 7 vendor role (status ON_HOLD until bank account approval per FR-19.22) and invites the contact as VENDOR_ADMIN; rejected requests keep data 90 days then purge.

- **FR-20.19 Vendor catalog upload via the portal** — Should. The portal shall let vendor users upload catalog files (CSV/JSON per FR-17.18, max 50,000 rows, images ≤ 2 MB each, 25 MB total) into the wave 17 staging area for a catalog the buyer has opened to that vendor, show row-level validation results, and notify the buyer for review; vendors see only their own staged and published rows.

- **FR-20.20 Supplier service entry and timesheets** — Should. The portal shall let vendor users create service entry sheets (wave 18 FR-18.14) against their open SERVICE PO lines, including timesheet-style entry (worker name or contractor id, date, hours, service item) aggregated into sheet lines, submit them for acceptance, and view acceptance status and rejection reasons; internal limits and expected values are not displayed, only remaining limit percentage when `portal_show_limit=true`.

- **FR-20.21 Controlled document distribution to suppliers** — Could. The system shall provide `DocumentDistribution` (SAP CVI1/CVI2): a document (drawing, specification, quality agreement, policy) from the wave 5 attachment store is distributed to a `RecipientList` (vendors, vendor users, categories) via the portal, each recipient must acknowledge (read and accept) the specific version by a due date, reminders follow wave 4 timers, and the distribution log (SAP CVI8) shows sent/viewed/acknowledged per recipient; a new document version supersedes open distributions and re-requests acknowledgement.

- **FR-20.22 Portal consignment view** — Should. The portal shall show vendor users their vendor-owned stock (wave 16 FR-16.17) by warehouse and product with age, open consignment replenishment orders, receipts against them, ownership changes (consumption) and consignment settlement invoices (wave 18 FR-18.16), with export.

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
| `dev.passage.supplier.vendor-request.submitted.v1` / `.approved.v1` / `.rejected.v1` | vendor request lifecycle | request_id, type, prospect (name, country, tax id masked), category, questionnaire score | party master (w7 vendor creation), notifications, compliance screening |
| `dev.passage.supplier.document.acknowledged.v1` | recipient acknowledges distributed document | distribution_id, document_id, version, vendor_id, user_id, acknowledged_at | quality (w33), notifications |
| `dev.passage.costing.aging.snapshot.created.v1` | aging snapshot stored | snapshot_id, entity, as_of, valuation_ledger, totals per bucket | analytics, close checklist (w14) |

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
- **BR-20.13** Valuation ledgers share quantities and settlement order inputs but not values: each ledger's settlement is computed independently from the same transaction sequence using its own method and currency; the primary ledger's results drive `cost_amount_adjustment` on the inventory transaction, the others are stored only in `CostTransaction`; a close is COMPLETED for the entity only when all valuation ledgers completed.
- **BR-20.14** Split valuation: issues must name a valuation type when more than one has stock (`CST-VAL-001`) unless the product's default valuation type is set; batch valuation creates the valuation type automatically at batch receipt with the receipt cost.
- **BR-20.15** Aging buckets use calendar days between layer receipt `financial_date` (physical date when not yet invoiced) and the as-of date; layers from migration opening balances carry the source receipt date when available, else the cutover date (flagged).
- **BR-20.16** A vendor request cannot create a vendor when a duplicate party match ≥ 0.9 exists unless a master data steward links or overrides with reason; bank details captured in onboarding are always subject to FR-19.22 dual approval.
- **BR-20.17** Portal-entered service entry sheets enter status SUBMITTED directly; vendors may withdraw only while not ACCEPTED; timesheet hours per worker per day are capped at 24 and per sheet at the period length.

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
- **AC-20.11** (FR-20.15) Given the FR-20.1 example data, a primary ledger (EUR, WEIGHTED_AVG_PERIODIC) and a group ledger (USD at 1.10, FIFO), when the close runs, then the primary issue cost is 1,650.00 EUR and the group ledger issue cost is 1,760.00 USD (1,600.00 × 1.10), each ledger's value report ties to its own ledger balances, and the group ledger adjustment voucher posts only to the group ledger.
- **AC-20.12** (FR-20.16) Given a product with valuation category ORIGIN and receipts 100 DOMESTIC @ 10.00 and 100 IMPORT @ 14.00 under WEIGHTED_AVG_RUNNING, when 50 IMPORT are issued, then issue cost is 700.00 (not 600.00) and the value report shows DOMESTIC 1,000.00 and IMPORT 700.00.
- **AC-20.13** (FR-20.17) Given FIFO layers received 400, 100 and 20 days before the as-of date with remaining 10, 50 and 100 units at 5.00, when the aging report runs with default buckets, then buckets 0–30 = 500.00, 91–180 = 250.00, 181–365 = 50.00 and the total equals the value report for the product.
- **AC-20.14** (FR-20.18) Given a prospect registering with a tax id equal to an existing vendor's, when the request is submitted, then it is flagged as duplicate and cannot create a vendor until a steward links it; a non-duplicate approved request creates a vendor ON_HOLD and emails a VENDOR_ADMIN invitation.
- **AC-20.15** (FR-20.21) Given a specification distributed to 10 vendors with due date 5 days, when 7 acknowledge and a new version is released on day 3, then the 7 acknowledgements are marked SUPERSEDED, all 10 receive the new version, and the log shows per-recipient status for both versions.

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

### 3.20.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `costing_method` | item model group | enum | WEIGHTED_AVG_RUNNING | FIFO, LIFO, LIFO_DATE, WEIGHTED_AVG_RUNNING, WEIGHTED_AVG_PERIODIC, STANDARD, MOVING_AVG | FR-20.1 | InventModelGroup.InventModel | MBEW-VPRSV price control S/V (OMW0/OMW1) |
| `include_physical_value` | item model group | bool | true | true/false | physically posted (un-invoiced) receipts participate in running average | Item model group → "Include physical value" | n/a (MAP updated at GR) |
| `costing_level` | item model group | enum | SITE | SITE, ENTITY | BR-20.5 | Dimension-based cost price (financial inventory flag on site/warehouse) | Valuation level plant vs company code (OX14) |
| `close_parallelism` | legal entity | int | 32 | 1–128 | FR-20.2 | Inventory close helper threads | CKMLCP parallel processing settings |
| `close_requires_invoice` | legal entity | bool | false | true/false | FR-20.2 pre-check | n/a | CKMLCP "materials not invoiced" check |
| `lock_wait_s` | tenant | int | 5 | 1–30 | BR-20.12 | n/a | n/a |
| `unsettled_issue_fallback` | legal entity | enum | RUNNING_AVG | RUNNING_AVG, STANDARD, LAST_RECEIPT | BR-20.3 | Inventory parameters → "Use latest cost price" / break-even | ML price determination fallback (CKMLCP single-level) |
| `costing_versions` | legal entity | table | `STD-{year}` | type STANDARD/PLANNED, block activation, fallback version, site scope | FR-20.5 | Costing versions | CK11N costing variant, CK24 marking allowance (CK24 "allow marking") |
| `allow_zero_cost` | legal entity | bool | false | true/false | FR-20.5 activation check | n/a | n/a |
| `posting_profile.revaluation_account` / `ppv_account` / `cost_adjustment_account` | posting profile | account | seeded | ledger accounts | FR-20.5–20.7 | Posting profiles: Standard cost revaluation, Price variance, Inventory adjustment | OBYC UMB (revaluation), PRD (price difference), BSX |
| `valuation_ledgers` | legal entity | table | primary only | ledger group (w12), currency, rate type, method override per item model group | FR-20.15 | Global inventory accounting: ledgers, accounting policies, conventions | ML currency types (OMX2/OMX3), group/profit-center valuation (8KEM) |
| `valuation_categories` | tenant | table | none | category code, types, account mapping per type, BATCH flag | FR-20.16 | Financial inventory dimension (batch/warehouse) | OMWC split valuation (T149C/T149D), OMWD valuation grouping code |
| `aging_bucket_sets` | legal entity, user | table | 0–30, 31–90, 91–180, 181–365, > 365 | bucket boundaries | FR-20.17 | Inventory aging report setup | MC50 / MC46 day thresholds |
| `value_report_setup` | legal entity | table | default columns | columns, dimensions, resource type, GL tie-out | FR-20.4 | Inventory value report configuration | MB5L variant |
| `portal_rate_limit_user` / `portal_rate_limit_vendor` | tenant | int | 60 / 1,000 per min | | FR-20.12 | n/a | n/a |
| `portal_mfa_required` | vendor | bool | false | true/false | FR-20.12 | Vendor collaboration security (Azure AD B2B) | Ariba MFA setting |
| `ack_due_days` | legal entity, vendor | int | 2 | 0–30 | FR-20.9 | Vendor collaboration → confirmation response time | Confirmation control keys (T163D) |
| `portal_price_override` | vendor | bool | false | true/false | BR-20.11 | Vendor collaboration "Allow price change on invoice" | n/a |
| `portal_upload_max_mb` / `portal_allowed_file_types` | tenant | int / list | 25 / pdf, png, jpg, csv, xlsx, xml | | FR-20.13 | Document management file types | DMS document type file extensions |
| `vendor_request_types` / `onboarding_questionnaires` | legal entity | table | ADD_VENDOR with default questionnaire | type, workflow id, questionnaire per category | FR-20.18 | Vendor request types; Vendor onboarding questionnaires; Registration wizard configuration | Ariba SLP registration questionnaires |
| `prospect_data_retention_days` | tenant | int | 90 | 30–365 | FR-20.18 purge | n/a | n/a |
| `portal_show_limit` | vendor | bool | false | true/false | FR-20.20 | n/a | n/a |
| `document_recipient_lists` / `distribution_types` | legal entity | table | none | recipients, acknowledgement required, due days | FR-20.21 | n/a | DMS distribution types and recipient lists (CVI customizing) |

### 3.20.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Inventory value report | statutory / operational report | as-of, group by, site, valuation ledger, valuation type, segment | qty, physical value, financial value, GL balance, diff | `CostTransaction` | Inventory value report (InventValueReport) | MB5L, MB5W, MC.9 |
| Inventory close list and exceptions | list inquiry | entity, close date, kind | status, duration, products, settlements, exceptions | `InventoryClose` | Closing and adjustment list | CKMLCP cockpit status, CKMLPP log |
| Settlement trace (cost explorer) | drill-down | issue or receipt transaction | settled pairs, qty, value, method, sequence | `CostSettlement` | Inventory value / Cost explorer, Settlements | CKM3N material price analysis |
| Adjustment history | list inquiry | product, period | adjustment kind, amount, voucher | `CostTransaction` kinds SETTLEMENT_ADJ/RECALC/MANUAL_ADJ | Adjustment history | MR22 documents, ML price change documents |
| Costing version comparison | operational report | version A vs B, site | product, cost A, cost B, delta, on-hand, revaluation | `ItemCost` | Costing version comparison | CK13N / CK40N comparison, MR21 preview |
| Price change history | list inquiry | product, site | effective date, old/new cost, source, voucher | `cost.changed` projection | Item price history (InventItemPrice) | MR21 price change documents, S_P99_41000062 price history |
| PPV analysis | operational report | vendor, product, period | PO price, standard, invoice price, PPV at receipt, PPV at invoice | `CostTransaction` PPV | Standard cost variance report | PRD account analysis, CKM3 |
| Inventory-to-GL reconciliation | operational report (daily) | account, dimension | inventory value, ledger, diff, sample vouchers | `costing.reconcile` | Ledger reconciliation inquiry | MB5L vs FAGLB03 |
| Inventory aging | operational report | as-of, bucket set, grouping, valuation ledger | qty and value per bucket, total, % > 180 days | cost layers after settlement | Inventory aging report (D365) | MC50 dead stock, MC46 slow-moving, MC.A |
| Inventory turnover / days on hand | KPI | period, item group, site | COGS, average inventory, turns, days on hand | `CostTransaction` | Inventory turnover (D365 PBI) | MC.9 / MC44 inventory turnover |
| Portal acknowledgement monitor | list inquiry | vendor, due, status | PO, version, sent, due, response | `PurchaseOrderAcknowledgement` | Vendor collaboration → PO confirmation requests | ME92F/ME92K monitor order acknowledgment, ME2A |
| ASN list | list inquiry | vendor, expected arrival, status | ASN, PO, lines, tracking | `AdvanceShipNotice` | Vendor collaboration ASN | VL06I inbound delivery monitor |
| Portal user activity and provisioning | operational report | vendor, last login, status | user, role, MFA, invited by, last login | `SupplierUser` | Portal user activity / provisioning status | Ariba user report |
| Vendor requests pipeline | list inquiry | type, status, category, age | request, prospect, step, assignee, duplicate flag | `VendorRequest` | Vendor requests list | MDG-S change requests (USMD) |
| Document distribution log | list inquiry | document, recipient list, status | recipient, version, sent, viewed, acknowledged | `DocumentDistribution` | n/a | CVI8 distribution log |
| Consignment portal activity | list inquiry | vendor | replenishment orders, receipts, ownership changes, settlements | waves 16/18 projections | Vendor collaboration consignment | SNC consignment monitor |

### 3.20.15 Printed and electronic documents

| Document | Trigger | Layout elements (header / line / footer) | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Inventory value report (period-end) | close COMPLETED / on demand | header: entity, as-of, valuation ledger, method; lines: grouping, qty, value, GL balance, diff; footer: totals per account | PDF/A, XLSX | close number `CLS-` | Year-end inventory valuation evidence (IAS 2, HGB §253, US ASC 330); retain 10 years |
| Inventory aging snapshot | month end | buckets per grouping, totals | PDF, XLSX | snapshot id | Audit workpaper (obsolescence reserve support) |
| Standard cost revaluation voucher report | version activation | per site: products, old/new cost, on-hand, revaluation amount | PDF, XLSX | version code | Audit evidence for cost changes |
| Cost adjustment voucher report | manual adjustment | product, dims, old/new value, reason, approver | PDF | adjustment number `CAD-` | Audit evidence |
| PO acknowledgement confirmation | vendor acknowledges | PO, version, response, proposed changes | email to buyer and vendor | n/a | Commercial correspondence |
| ASN / delivery note (vendor-generated) | ASN submitted | ASN number, PO, packages with SSCC, lines, batches | PDF (vendor print), SSCC label ZPL | vendor ASN number | GS1 logistics label where required |
| Vendor onboarding confirmation | request approved / rejected | prospect, decision, next steps | email | request number `VR-` | Privacy notice reference (GDPR Art. 13) |
| Distributed document acknowledgement receipt | acknowledgement | document, version, user, timestamp | PDF in log, email | n/a | Quality agreement evidence (ISO 9001 §7.5, IATF 16949) |

### 3.20.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Inventory close / recalculation (FR-20.2, FR-20.3) | monthly after period end (manual start or schedule day 2 22:00) | legal entity × valuation ledger × product | close id; settlement deterministic; child checkpoints | < 20 min per 1M transactions per ledger | FAILED resumable; cancel reverses | close FAILED page; > 60 min ticket |
| Inventory-to-GL reconciliation (`costing.reconcile`) | daily 02:15 | legal entity × account | run id | < 5 min | retry 3× | diff ≠ 0 page |
| Running average / on-hand cost refresh | every 10 min | product | idempotent recompute from cost layers | < 1 min | retry | lag > 30 min ticket |
| Pending cost activation (scheduled versions) | daily 00:30 | version × site | version id idempotent | < 2 min per 10k products | per-product failure listed | failures > 0 ticket |
| Moving-average revaluation (invoice differences on remaining stock) | hourly | product | key = invoice diff cost transaction | < 1 min | retry | none |
| Inventory value report and aging snapshots | month end day 1 03:00 | legal entity × valuation ledger | key = (entity, ledger, as-of) | < 5 min per 10k products | retry | missing snapshot → close checklist blocker |
| PO acknowledgement overdue scan | daily 08:00 | vendor | key = (ack, date) | < 30 s | retry | overdue list on buyer workspace |
| Portal inactive-user cleanup | weekly Sunday 04:00 | vendor | deactivates users with no login > 180 days | < 1 min | retry | deactivations reported to vendor admin |
| Vendor request SLA and prospect data purge | daily 01:00 | legal entity | purge key = request id after retention | < 1 min | retry | none |
| Document distribution reminders | daily 07:30 | distribution | timer per recipient | < 1 min | retry | overdue acknowledgements → owner notification |

### 3.20.17 Security duties and privileges

| Duty | Privileges (entry points: API paths / screens / jobs) | Segregation-of-duties conflicts | Default roles that include it | AX duty / SAP authorization reference |
|---|---|---|---|---|
| CostingInquire | closes GET, value report, aging, cost lookup, settlement trace | none | Cost accountant, Controller, Auditor | AX "Inquire into inventory value"; SAP CKM3 (K_ML_VA display) |
| InventoryCloseRun | closes POST (CLOSE/RECALC/PREVIEW), :cancel | with CostAdjust, CostingVersionActivate | Cost accountant | AX "Close inventory" (InventClosing); SAP CKMLCP (K_ML_VA activity 'post closing') |
| CostAdjust | adjustments POST | with InventoryCloseRun, InventoryJournalPost (w16) | Senior cost accountant | AX "Adjust inventory" (InventAdjustment); SAP MR22 (M_MSEG_WMB / K_ML_VA) |
| CostingVersionMaintain | versions create, item-costs:import, copy | with CostingVersionActivate | Cost accountant | AX "Maintain costing versions"; SAP CK11N / CK24 mark (K_KEKO) |
| CostingVersionActivate | versions :activate (revaluation) | with CostingVersionMaintain, CostAdjust | Controller | AX "Activate pending prices"; SAP CK24 release, MR21 (M_MATE_WRK + F_BKPF_BUK) |
| ValuationSetupMaintain | valuation ledgers, valuation categories, item model group costing fields, posting profile accounts | with InventoryCloseRun | Finance system administrator | AX "Maintain cost management setup"; SAP OMWC/OMX2 (S_TABU_DIS) |
| SupplierPortalAdmin (internal) | invite vendor admins, deactivate users, vendor request approval queue, distribution lists | with VendorBankAccountApprove (w19) | Supplier relationship manager | D365 "Provision vendor collaboration users"; Ariba administrator |
| VendorRequestApprove | approve/reject vendor requests, duplicate override | with VendorMaintain (w7) when the same person entered prospect data | Category manager, Compliance officer, Vendor master steward | AX "Approve vendor requests"; SAP MDG-S approval |
| DocumentDistributionMaintain | distributions create, recipient lists | none | Quality engineer, Buyer | SAP CVI1/CVI2 (C_DRAD_OBJ) |
| portal: VendorUser | /portal/v1 purchase orders, ASNs, invoices, payments, SES/timesheets, catalog upload (when granted), consignment view, documents | scoped by OPA to own vendor ids | external vendor staff | D365 "Vendor collaboration user" role; Ariba supplier user |
| portal: VendorAdmin | users:invite, deactivate, MFA settings, contact data | none (external) | external vendor administrator | D365 "Vendor admin (external)"; Ariba supplier administrator |
| portal: ProspectiveVendor | public registration endpoint, own request status | rate limited, CAPTCHA | anonymous prospect | D365 "Prospective vendor" role |

### 3.20.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| CST-CLS-001 | error | A close is already running for {entity} ({close}) | StartClose | Wait for completion | FR-20.2 |
| CST-CLS-002 | error | Close date {date} is before the last completed close {last} | BR-20.8 | Cancel later closes first | BR-20.8 |
| CST-CLS-003 | error | INVENTORY period for {date} is not open | FR-20.2 | Open the period (wave 14) | FR-20.2 |
| CST-CLS-004 | warning | Pre-check: {n} products with unsettled negative on-hand; valued at fallback | BR-20.3 | Post missing receipts or accept fallback | FR-20.2 |
| CST-CLS-005 | warning | Pre-check: {n} receipts not invoiced and `close_requires_invoice=true` | FR-20.2 | Post invoices or disable the check | FR-20.2 |
| CST-CLS-006 | error | Close {close} cannot be cancelled: later close {later} exists | BR-20.1 | Cancel the later close first | BR-20.1 |
| CST-LOCK-001 | error | Product {product} is being settled by close {close}; retry | BR-20.12 | Retry after a few seconds | BR-20.12 |
| CST-ACT-001 | error | Version {version} is blocked for activation | ActivateVersion | Remove the block flag | FR-20.5 |
| CST-ACT-002 | error | {n} pending costs are zero and zero cost is not allowed | ActivateVersion | Enter costs or allow zero cost | FR-20.5 |
| CST-ACT-004 | error | Cannot activate during an in-flight close | BR-20.7 | Wait for the close | BR-20.7 |
| CST-ADJ-001 | error | Transaction {txn} is in a closed inventory period (close {close}) | BR-20.8 | Adjust in an open period | FR-20.6 |
| CST-REC-001 | error | Inventory value {inv} differs from ledger {gl} for {account}/{dimension} by {diff} | Daily reconciliation | Drill to sample vouchers | FR-20.4 |
| CST-VLG-001 | error | Valuation ledger {ledger} has no exchange rate {from}→{to} on {date} | FR-20.15 | Enter the rate | FR-20.15 |
| CST-VAL-001 | error | Valuation type required: {product} has stock in {types} | BR-20.14 | Specify the valuation type | FR-20.16 |
| CST-VAL-002 | error | Valuation category of {product} cannot change while stock exists | FR-20.16 | Issue or transfer all stock first | FR-20.16 |
| CST-AGE-001 | info | {pct} % of layers originate from migration opening balances (dated at cutover) | BR-20.15 | None; flagged in report | FR-20.17 |
| SUP-ACK-001 | error | Only the latest PO version {version} can be acknowledged | BR-20.9 | Open the latest version | FR-20.9 |
| SUP-ASN-001 | error | ASN quantity {qty} exceeds remaining {remaining} plus tolerance | BR-20.10 | Correct quantity | FR-20.10 |
| SUP-ASN-002 | error | Tracking number {tracking} already used on ASN {asn} | BR-20.10 | Use the correct tracking number | FR-20.10 |
| SUP-INV-001 | error | Invoice price {price} differs from PO price {po_price}; price override not enabled | BR-20.11 | Contact the buyer | FR-20.11 |
| SUP-AUTH-001 | error | Not found | Cross-vendor access attempt (404 by design) | None | FR-20.12 |
| SUP-RATE-001 | error | Too many requests; retry after {seconds} s | Rate limit | Retry later | FR-20.12 |
| SUP-UPL-001 | error | File {name} rejected: {reason} (max {mb} MB, types {types}, malware scan) | Upload validation | Upload an allowed clean file | FR-20.13 |
| SUP-REQ-001 | warning | A party with matching tax id or name already exists; steward review required | BR-20.16 | Steward links or overrides | FR-20.18 |
| SUP-SES-001 | error | Hours for {worker} on {date} exceed 24 | BR-20.17 | Correct timesheet | FR-20.20 |
| SUP-DOC-001 | warning | Acknowledgement of {document} v{version} overdue since {date} | FR-20.21 | Vendor acknowledges | FR-20.21 |

### 3.20.19 Performance budgets and capacity

| Operation | Volume assumption (per tenant per day / peak per minute) | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Inventory close (1M transactions, 2,000 products, per valuation ledger) | monthly | 12 min / 20 min / 30 min | 1,000 settlements/s across 32 children | CostSettlement ≈ 200 B/row (~1.2M rows per close ≈ 240 MB) ; CostTransaction ≈ 250 B/row | CostTransaction (tenant, product_id, financial_date), (ledger_entry_id), (valuation_ledger_id); monthly partitions | Parallelism capped by DB CPU; additional ledgers run sequentially if CPU > 80 % |
| Value report (10k products) | 200 runs | 1 s / 5 s / 10 s | n/a | n/a | materialized balance per (product, site, account, valuation ledger) refreshed on each cost transaction | Served from last snapshot with "as of HH:MM" banner |
| Cost lookup (waves 21/27) | 500k / 5,000 per min | 3 ms / 20 ms / 40 ms | 2,000 rps | n/a | in-pod cache keyed (product, site, valuation type) invalidated by `cost.changed` | Cache-only reads, stale ≤ 60 s |
| Standard cost activation (10k products) | 12 / year | 40 s / 2 min / 4 min | n/a | ItemCost ≈ 200 B/row | (product_id, site_id, status) | Chunked by 500 products |
| Aging report (10k products, 200k layers) | 50 runs | 2 s / 8 s / 15 s | n/a | snapshots ≈ 20 MB/month | layers (product, site, receipt_date) | Snapshot fallback |
| Portal page load / API | 2,000 vendor users, 50k requests / 500 per min | 300 ms / 1.5 s / 3 s (page) ; 50 ms / 200 ms / 400 ms (API) | 200 rps | supplier-db ≈ 5 GB/year + documents 50 GB/year | RLS by vendor_id; indexes (vendor_id, status) | Rate limits per user/vendor; WAF; static assets cached at edge |
| ASN submit / receipt pre-fill | 1,500 / 50 per min | 150 ms / 500 ms / 1 s | 50 rps | ASN ≈ 2 KB + lines | (vendor_id, tracking_number) unique | Queue to receiving consumer |
| Vendor registration (public) | 50 / 5 per min | 300 ms / 1 s / 2 s | 10 rps | prospect ≈ 5 KB + uploads | n/a | CAPTCHA + 5 req/min/IP limit |

### 3.20.20 Localization and compliance hooks

| Concern | Varies by | Country packs touching it (wave 48) | Extension point used |
|---|---|---|---|
| Permitted costing methods (LIFO prohibited under IFRS/IAS 2; allowed under US GAAP and US tax with LIFO conformity) | Accounting framework, tax | US (LIFO, dollar-value LIFO in v1.x), IFRS countries | Item model group validation per ledger framework; FR-20.15 lets a tax/US GAAP ledger use LIFO while IFRS uses FIFO/average |
| Lower of cost and net realizable value / write-downs | IAS 2, ASC 330, HGB §253 | all | Manual cost adjustment (FR-20.6) + aging report (FR-20.17) as evidence; NRV calculation hook for wave 39 |
| Statutory inventory valuation reports (e.g., FR état des stocks, IT LIFO a scatti, BR Bloco K / SPED, MX inventory reports) | Country | FR, IT, BR, MX, IN | Value report projection exposed to wave 15 report definitions; SPED Bloco K mapping in wave 48 |
| Currency of valuation ledgers and hyperinflation (IAS 29 restatement) | Functional currency | AR, TR, VE | Valuation ledger with index-adjusted revaluation hook (wave 12 revaluation) |
| Transfer pricing for intercompany stock (group valuation) | OECD guidelines | all multi-entity | Group valuation ledger in FR-20.15; intercompany profit elimination in wave 40 |
| Supplier portal accessibility and language | WCAG 2.1 AA, EU EAA 2025 | EU | Shell i18n (en, de, fr, es); axe tests |
| Supplier data privacy (prospect and user personal data, retention, consent) | GDPR, CCPA, LGPD | EU, US-CA, BR | Privacy notice at registration; purge job; DSAR export of SupplierUser data |
| Supplier due diligence and sanctions screening (EU CSDDD, German LkSG supply-chain act, UFLPA) | Country law | DE, EU, US | FR-20.18 questionnaire + screening hook; questionnaire answers stored as evidence |
| E-invoice submission from portal must respect clearance mandates | IT SDI, MX CFDI, IN IRN | IT, MX, IN, PL (KSeF) | Portal invoice converted via wave 35 country adapter before posting |
| Document distribution evidence for quality agreements | ISO 9001, IATF 16949, GMP | all (regulated industries) | FR-20.21 acknowledgement log retained 10 years |

### 3.20.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.8.1 — Global inventory accounting (multiple cost ledgers / accounting conventions) [#20] | GAP | ADDED as FR-20.15 | Valuation ledgers bound to wave 12 ledger groups |
| AX 12.8.4 — Global inventory accounting setup (ledgers, accounting policies, conventions) [#21] | GAP | ADDED as FR-20.15 | `valuation_ledgers` in 3.20.13 |
| AX 12.2.1 — Vendor requests (R3) [#9] | PARTIAL | ADDED as FR-20.18 | Internal and prospect-initiated requests |
| AX 12.2.4 — Vendors → Vendor request types [#14] | PARTIAL | ADDED as FR-20.18 | `vendor_request_types` |
| AX 12.8.2 — Inventory aging report (D365) [#48] | PARTIAL | ADDED as FR-20.17 | Layer-based buckets tying to value report |
| AX 12.8.4 — Inventory aging report setup [#52] | PARTIAL | ADDED as FR-20.17 | `aging_bucket_sets` |
| AX 12.14.2 — SSRS: Inventory aging (link) [#128] | PARTIAL | ADDED as FR-20.17 | 3.20.14 and snapshot document (3.20.15) |
| AX 12.16.1 — Vendor catalogs (vendor catalog import, CMR files) [#154] | PARTIAL | ADDED as FR-20.19 | Portal upload into FR-17.18 staging |
| AX 12.16.1 — Vendor requests (link) [#155] | PARTIAL | ADDED as FR-20.18 | |
| AX 12.16.1 — Procurement → Vendor onboarding (prospective vendor registration) [#157] | PARTIAL | ADDED as FR-20.18 | Public registration with questionnaires |
| AX 12.16.4 — Vendors → Vendor request types / Vendor onboarding questionnaires [#164] | PARTIAL | ADDED as FR-20.18 | `onboarding_questionnaires` |
| AX 12.39.1 — Vendor → Prospective vendor registration (wizard, questionnaires) [#316] | PARTIAL | ADDED as FR-20.18 | |
| AX 12.39.4 — Vendor collaboration → Onboarding: registration wizard configuration, questionnaires [#317] | PARTIAL | ADDED as FR-20.18 | |
| AX 12.39.1 — Vendor → Consignment inventory view [#108, deferred from wave 16] | GAP | ADDED as FR-20.22 | Data from FR-16.17 and FR-18.16 |
| SAP 13.8 — CKMLCPAVR alternative valuation run (cumulative periods) [#93] | PARTIAL-20 | DEFERRED to wave 39 | Multi-period cumulative actual costing belongs with product costing and multi-level actual cost roll-up; the wave 20 engine provides per-period settlement only |
| SAP 13.8 — CKMDUVACT / CKMDUVMAT distribution of usage variances [#94] | PARTIAL-20 | DEFERRED to wave 39 | Requires production consumption variances (waves 21/39) |
| SAP 13.8 — WIP at actual cost (ML) [#95] | PARTIAL-20 | DEFERRED to wave 39 | WIP valuation from wave 21 production orders; revaluation at actual in wave 39 |
| SAP 13.9 — KE27 periodic valuation (revaluate profitability with actual costs) [#104] | PARTIAL-20 | DEFERRED to wave 39 | COGS revaluation to profitability segments uses `close.completed` adjustments; profitability analysis lands with wave 38/39 cost accounting |
| SAP 13.12 — SAP Ariba Buying / Sourcing / Contracts / SLP [#130] | PARTIAL-20 | ADDED as FR-20.18 (supplier onboarding/qualification), FR-17.19 (weighted RFP scoring) | Reverse auctions and contract authoring OUT-OF-SCOPE for v1 |
| SAP 13.13 — OMWD / OMWC / OMSK split valuation, valuation types, valuation classes [#152] | PARTIAL-20 | ADDED as FR-20.16 | `valuation_type` dimension and per-type account mapping |
| SAP 13.15 — Supplier self-service SES (Business Network / Fieldglass) [#174] | PARTIAL-20 | ADDED as FR-20.20 | Portal sheets and timesheets; contingent-labor management (Fieldglass worker onboarding) OUT-OF-SCOPE |
| SAP 13.33 — Batch valuation (split valuation per batch) [#366] | PARTIAL-20 | ADDED as FR-20.16 | Valuation category BATCH |
| SAP 13.33 — Batch valuation activation (T001K, T149) [#372] | PARTIAL-20 | ADDED as FR-20.16 | `valuation_categories` with BATCH flag |
| SAP 13.35 — CV01N document distribution (recipients, CVI1/CVI2/CVI8) [#380] | PARTIAL-20 | ADDED as FR-20.21 | |
| SAP 13.35 — CVI8 distribution log [#387] | PARTIAL-20 | ADDED as FR-20.21 | 3.20.14 |
| SAP 13.35 — Document distribution runs (CVI) [#390] | PARTIAL-20 | ADDED as FR-20.21 | Reminder job (3.20.16) |
| SAP 13.35 — Document distribution customizing (recipient lists, distribution types) [#395] | PARTIAL-20 | ADDED as FR-20.21 | `document_recipient_lists` |
