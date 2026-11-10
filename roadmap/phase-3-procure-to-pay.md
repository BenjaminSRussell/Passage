# Phase 3: Inventory & Procure-to-Pay (waves 16-20)

Inventory ledger and costing, purchasing, receiving with three-way match, accounts payable and payments, supplier collaboration. Physical goods start moving and valuing correctly into the GL.

## Wave 16: Inventory Ledger, Dimensions & On-Hand

**Phase 3 — Inventory & Procure-to-Pay**

**Goal.** Every quantity movement is an immutable inventory transaction with physical/financial status, storage and tracking dimensions, and real-time on-hand at any dimension level.

**Replaces**

- *AX / D365:* InventTrans, InventSum, inventory dimensions, inventory journals (movement, adjustment, transfer, counting), item model groups
- *SAP:* Material documents (MSEG/MKPF), stock overview (MMBE), movement types, storage locations, batch/serial, physical inventory (MI01)

**Deliverable**

- Inventory transaction service: quantities and status (ordered/reserved/picked/deducted/received/posted) per dimension; on-hand projection by site/warehouse/location/batch/serial
- Inventory journals: movement, adjustment, transfer, bill of materials, counting with approval and posting to the GL
- Reservations (manual/auto, FEFO/FIFO batch picking policies), blocking, quarantine
- Physical inventory: cycle counting plans, count sheets, variance posting
- Inventory status and unit conversions on movements
- Transfer orders with in-transit warehouse: ship/receive, under/over-delivery, picking release event, shipping and receiving documents, stock-in-transit inquiry
- Ownership dimension with vendor consignment (replenishment orders, ownership change journal and batch, consignment reports) and customer consignment warehouses
- Tag counting, periodic ABC classification, and sales-order/project segregated stock (segment dimension)

**Proof (testable acceptance)**

- [ ] Concurrency test: 10k simultaneous issues against one SKU never drive on-hand negative when policy forbids it
- [ ] On-hand projection equals SUM(transactions) for 1M transactions (rebuild and compare)
- [ ] Every posted journal produces a balanced GL entry via the universal journal (checked per journal type)
- [ ] FEFO picking test: reservation selects earliest-expiry batch across 100 batches

**Deploy**

Inventory management workspace; demo plant stocked via opening-balance journal; on-hand inquiry live.

**References**

- [AX 2012 inventory transactions and statuses](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-inventory-transactions)
- [SAP MM inventory management](https://help.sap.com/docs/SAP_ERP)
- [Apache OFBiz inventory data model](https://cwiki.apache.org/confluence/display/OFBIZ/Data+Model+Diagrams)

**Depends on:** wave 15

---

## Wave 17: Purchasing: Requisitions, RFQs, Agreements & Purchase Orders

**Phase 3 — Inventory & Procure-to-Pay**

**Goal.** Buyers run requisition → sourcing → PO with approval policies, agreements and change management, and suppliers receive orders electronically.

**Replaces**

- *AX / D365:* Purchase requisitions, RFQs, purchase agreements, purchase orders, purchasing policies, charges, change management
- *SAP:* Purchase requisitions (ME51N), RFQs (ME41), contracts/scheduling agreements (ME31K), POs (ME21N), release strategy, pricing conditions

**Deliverable**

- Purchase requisition with policy-driven approval (amount bands, category), consolidation into POs
- RFQ cycle: issue, supplier reply portal, compare, award
- Purchase agreements (blanket) with release tracking; trade agreements/price lists for vendor pricing
- Purchase order lifecycle with versioned change management, confirmation, charges, delivery schedules
- Supplier output: PDF/email, EDI 850/ORDERS adapter hook (completed in wave 35)
- Scheduling agreements with FRC/JIT releases, firm/trade-off zones and cumulative quantities
- Reusable policy framework scoped by org hierarchy, plus worker signing limits with request workflow and annual recertification
- Procurement catalogs with images, publication by org unit and vendor catalog import; category managers, vendor assignment and questionnaires
- RFQ types with sealed bids, weighted scoring and amendments
- Service PO lines with service outlines, value/contract limits and model service specifications; invoicing plans; subsequent price change (reprice) of open POs; consignment price agreements

**Proof (testable acceptance)**

- [ ] Approval policy test matrix: 30 requisitions across amount/category bands route to the expected approver chain
- [ ] PO change after confirmation creates a new version and requires re-approval when policy says so
- [ ] Price lookup picks the most specific trade agreement (vendor > group > all) with effective dates (property test)
- [ ] PO total = Σ lines + charges + tax to the cent for 1,000 random POs

**Deploy**

Procurement workspace live; demo vendors with price lists; purchasing policy configured for the demo plant.

**References**

- [AX 2012 procurement and sourcing](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/procurement-and-sourcing)
- [SAP MM purchasing](https://help.sap.com/docs/SAP_ERP)
- [Odoo purchase module (reference flows)](https://www.odoo.com/documentation/18.0/applications/inventory_and_mrp/purchase.html)

**Partly verified end to end by**

- `tools/e2e/flow_test.go`

**Depends on:** wave 16

---

## Wave 18: Receiving, Product Receipts & Three-Way Match

**Phase 3 — Inventory & Procure-to-Pay**

**Goal.** Goods receipt updates inventory and accrues liability; vendor invoices match to PO and receipt within tolerances or route to resolution.

**Replaces**

- *AX / D365:* Product receipts, arrival journal, invoice matching (2/3-way, tolerances), accrued purchases, pending vendor invoices, invoice register/approval
- *SAP:* Goods receipt (MIGO), GR/IR clearing, logistics invoice verification (MIRO), tolerance keys, evaluated receipt settlement

**Deliverable**

- Receiving: arrival, registration, product receipt posting (inventory + GR/IR accrual to the GL)
- Vendor invoice capture: manual, OCR/AP-automation adapter, e-invoice (UBL/Peppol, completed in wave 35)
- Matching engine: 2-way/3-way, price/quantity/total tolerances, match status, exception workflow
- Accrued purchases and GR/IR reconciliation report
- Evaluated receipt settlement (self-billing)
- Service entry sheets with acceptance workflow, unplanned services within limits, collective acceptance, SES-based invoice match and ERS
- Stochastic (random-sample) invoice audit block with auditor release
- Consignment settlement run, invoicing plan settlement run, subsequent debit/credit invoices, and one-time payee invoices

**Proof (testable acceptance)**

- [ ] Matching fixture set: 200 invoice scenarios (over/under-delivery, price variance, partial) yield expected statuses and postings
- [ ] GR/IR account nets to zero after invoice for 1,000 random PO lines
- [ ] Receipt of a serialized/batched item records tracking dimensions and blocks duplicate serials
- [ ] Invoice posting is rejected in a closed AP period (period control contract test)

**Deploy**

Receiving and AP invoice workspaces; demo plant receives and matches invoices for its opening POs.

**References**

- [AX 2012 invoice matching](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-accounts-payable-invoice-matching)
- [SAP logistics invoice verification](https://help.sap.com/docs/SAP_ERP)
- [Peppol BIS Billing 3.0](https://docs.peppol.eu/poacc/billing/3.0/)

**Partly verified end to end by**

- `tools/e2e/flow_test.go`

**Depends on:** wave 17

---

## Wave 19: Accounts Payable, Settlement & Payments

**Phase 3 — Inventory & Procure-to-Pay**

**Goal.** Open vendor items settle against payments with discounts and withholding; payment runs produce bank files in ISO 20022 and post to cash.

**Replaces**

- *AX / D365:* Vendor transactions/settlement, payment journals, payment proposals, methods of payment, cash discounts, positive pay, vendor aging
- *SAP:* Vendor open items (FBL1N), automatic payment program (F110), payment methods/mediums (DMEE), cash discount terms

**Deliverable**

- Open-item accounting for AP: settlement (partial/full, cross-currency, discounts, write-off), vendor aging
- Payment proposal: due-date selection, discount optimization, holds, approval workflow
- Payment methods: ACH/SEPA (ISO 20022 pain.001), check printing, card, wire; positive pay export
- Vendor payment advice (email/portal), payment status from bank (pain.002) ingestion
- Prepayments, vendor credit notes, reversal handling
- ACH prenotes with bank-account status lifecycle, and payment calendars with bank-holiday due-date and execution-date shifting
- Vendor balance confirmations and AP correspondence types (statement, payment notice, open items)
- Installment open items, noted items and guarantees, vendor-initiated direct debit mandates, and one-time payee payments
- Vendor bank account change control: dual approval, out-of-band notification and payment cooling-off

**Proof (testable acceptance)**

- [ ] Settlement fixtures: 300 scenarios (partial, multi-currency, discount taken/lost) produce expected FX gain/loss and discount postings
- [ ] pain.001 output validates against the ISO 20022 XSD and a bank test harness
- [ ] Aging report buckets reconcile to the AP control account per vendor group
- [ ] Payment run is idempotent: re-running after a crash does not pay twice (Temporal + idempotency test)

**Deploy**

AP workspace with payment runs; demo entity pays open invoices to a sandbox bank file.

**References**

- [AX 2012 accounts payable](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/accounts-payable)
- [SAP automatic payment program](https://help.sap.com/docs/SAP_ERP)
- [ISO 20022 pain.001](https://www.iso20022.org/iso-20022-message-definitions)
- [NACHA ACH file format](https://achdevguide.nacha.org/ach-file-overview)

**Depends on:** wave 18

---

## Wave 20: Inventory Costing & Supplier Collaboration

**Phase 3 — Inventory & Procure-to-Pay**

**Goal.** Inventory is valued correctly under FIFO/LIFO/weighted-average/standard with closing/adjustment runs that reconcile to the GL; suppliers self-serve via a portal.

**Replaces**

- *AX / D365:* Inventory close, recalculation, costing versions, inventory value report, vendor collaboration (D365)
- *SAP:* Material valuation (MAP/standard price), material ledger (CKMLCP), price change (MR21), Ariba supplier portal

**Deliverable**

- Costing engine: FIFO, LIFO, weighted average (running/periodic), standard cost with variance capture; moving-average settlement
- Inventory close/recalculation job with settlement of issues against receipts and GL adjustments
- Inventory value report reconciling on-hand value to the GL inventory accounts by dimension
- Costing versions, cost changes, revaluation
- Supplier portal: PO acknowledgement, ASN, invoice submission, document exchange
- Parallel inventory valuation ledgers (one costing method and currency per ledger group) with per-ledger close and value report
- Split valuation and batch valuation (valuation type dimension with per-type accounts)
- Inventory aging report with configurable buckets tied to the value report, and month-end snapshots
- Supplier portal: vendor requests and prospective-vendor onboarding with questionnaires, catalog upload, service entry/timesheets, consignment view, controlled document distribution with acknowledgement

**Proof (testable acceptance)**

- [ ] Costing fixtures: 100 receipt/issue sequences per method match a reference workbook to the cent
- [ ] Inventory close on 1M transactions completes in < 20 min and the value report ties to the GL
- [ ] Standard cost variance (purchase price variance) posts correctly on receipt and invoice
- [ ] Phase 3 exit: demo plant buys, receives, pays and values inventory end-to-end in a recorded walkthrough

**Deploy**

Costing workspace; supplier portal at supplier.<env>.passage.dev with a demo vendor login.

**References**

- [AX 2012 inventory close and costing](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-inventory-close)
- [SAP material ledger / actual costing](https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE)
- [IAS 2 inventories](https://www.ifrs.org/issued-standards/list-of-standards/ias-2-inventories/)

**Depends on:** wave 19

---
