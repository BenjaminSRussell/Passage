# Phase 5: Order-to-Cash (waves 26-30)

Pricing, sales orders with ATP, fulfillment, billing and revenue recognition, accounts receivable and collections. At the end of wave 30 a manufacturer runs the full buy-make-sell-collect loop on Passage — the first go-live milestone.

## Wave 26: Pricing, Discounts & Trade Agreements

**Phase 5 — Order-to-Cash**

**Goal.** A deterministic pricing engine resolves prices and discounts from layered agreements, promotions and rebates for any customer/product/date/quantity.

**Replaces**

- *AX / D365:* Trade agreements (price/line/multiline/total discounts), price groups, smart rounding, commissions, rebate management
- *SAP:* SD pricing procedures, condition technique (VK11), access sequences, condition exclusion, rebates (VBO1)

**Deliverable**

- Pricing engine: condition tables, access sequences (specific → general), price/line/multiline/total discounts, surcharges, rounding rules
- Promotions and campaigns with validity, customer/product groups, quantity breaks, currency-specific prices
- Rebate agreements and accrual calculation (settlement in wave 30)
- Price simulation and explain (which conditions applied), mass price updates
- Commission calculation hooks

**Proof (testable acceptance)**

- [ ] Pricing fixtures: 500 scenarios including overlapping agreements resolve to the expected price and explanation
- [ ] Engine is pure and deterministic: same inputs → same output across 10k random requests (property test)
- [ ] p95 price resolution < 20 ms for a 10-line order
- [ ] Mass update of 100k prices is atomic and reversible

**Deploy**

Pricing workspace; demo customers and price lists configured.

**References**

- [AX 2012 trade agreements](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-trade-agreements)
- [SAP SD condition technique](https://help.sap.com/docs/SAP_ERP/ac23efa7a8c1432ab4fa0c6ba7e7a84f/2c1d6e5287ca4e1b93b7f3a1a3e0e1f0.html)

**Depends on:** wave 25

---

## Wave 27: Sales Orders, Quotations, ATP & Credit

**Phase 5 — Order-to-Cash**

**Goal.** Sales reps quote and take orders with real-time available-to-promise, credit checks, and configured products, with order holds and change control.

**Replaces**

- *AX / D365:* Sales quotations, sales orders, ATP/CTP, delivery date control, credit limits/holds, sales agreements, order holds
- *SAP:* Inquiries/quotations (VA21), sales orders (VA01), availability check (ATP), credit management (FD32/UKM), scheduling agreements

**Deliverable**

- Quotation → order conversion, sales agreements, order types (journal/order/subscription/returned)
- ATP/CTP: cumulative ATP from on-hand + supply − demand, delivery date control, backorder handling, alternative items
- Credit management: limits, aging-based rules, credit holds, release workflow
- Order holds (credit, fraud, manual), change management, line-level status
- Customer portal order entry and status (completed in wave 30); multi-channel order intake API

**Proof (testable acceptance)**

- [ ] ATP fixtures: 100 scenarios with incoming supply give expected promise dates; ATP never over-promises on-hand (property test)
- [ ] Credit check blocks an order exceeding limit and the release workflow unblocks it
- [ ] Order with a configured product creates the correct production/planned order linkage
- [ ] Order API handles 500 orders/min with p99 < 500 ms

**Deploy**

Sales workspace live; demo customers placing orders against the demo plant's stock.

**References**

- [AX 2012 sales and marketing](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/sales-and-marketing)
- [SAP SD sales order processing](https://help.sap.com/docs/SAP_ERP/ac23efa7a8c1432ab4fa0c6ba7e7a84f/1a1d6e5287ca4e1b93b7f3a1a3e0e1f0.html)
- [SAP advanced ATP (aATP)](https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE/2a6a1d6ef9f84bb5b5c5f0d5e0aa8c7e/6e0f6e5287ca4e1b93b7f3a1a3e0e1f0.html)

**Depends on:** wave 26

---

## Wave 28: Fulfillment: Picking, Packing & Shipping

**Phase 5 — Order-to-Cash**

**Goal.** Orders release to the warehouse, pick/pack/ship with packing slips and carrier labels, and inventory deducts with COGS posting on shipment.

**Replaces**

- *AX / D365:* Release to warehouse, picking lists/registration, packing slips, shipments, carrier interface, delivery schedules, COGS posting
- *SAP:* Outbound deliveries (VL01N), picking (LT03), packing (HU), post goods issue, shipment documents

**Deliverable**

- Order release rules, wave/batch picking (basic; advanced WMS in wave 31), pick confirmation, packing with containers/HU and SSCC labels
- Packing slip / delivery note posting: inventory deduction, COGS to the universal journal
- Carrier integration abstraction (rate, label, tracking) with reference adapters (EasyPost/Shippo-style) and ASN output
- Drop-ship and direct delivery flows, partial shipments, delivery schedules
- Shipment tracking visible to customer portal

**Proof (testable acceptance)**

- [ ] Packing slip posting is balanced (inventory → COGS) and reverses correctly on cancellation (contract tests)
- [ ] Pick-pack-ship E2E for 1,000 orders in a simulated warehouse with zero inventory mismatch
- [ ] SSCC label check-digit and GS1-128 barcode validation
- [ ] Carrier adapter contract tests against recorded fixtures

**Deploy**

Fulfillment workspace and mobile pick UI (basic) in the demo warehouse.

**References**

- [AX 2012 shipping and picking](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-picking-and-shipping)
- [SAP LE shipping](https://help.sap.com/docs/SAP_ERP/ac23efa7a8c1432ab4fa0c6ba7e7a84f/8c1d6e5287ca4e1b93b7f3a1a3e0e1f0.html)
- [GS1-128 / SSCC](https://www.gs1.org/standards/id-keys/sscc)

**Depends on:** wave 27

---

## Wave 29: Billing, Invoicing & Revenue Recognition

**Phase 5 — Order-to-Cash**

**Goal.** Invoices (one-off, consolidated, subscription, milestone) post revenue, tax and AR, with ASC 606 / IFRS 15 recognition schedules for bundled and deferred revenue.

**Replaces**

- *AX / D365:* Sales invoices, free-text invoices, summary updates, revenue recognition (D365), subscription billing (D365)
- *SAP:* Billing documents (VF01), billing plans, SD-FI integration, revenue accounting (RAR, IFRS 15)

**Deliverable**

- Invoice posting from shipments/orders; free-text invoices; consolidated/summary invoices; credit notes; invoice layouts (PDF, e-invoice hook)
- Revenue recognition: performance obligations, standalone selling price allocation, schedules (ratable, milestone, usage), deferred revenue postings
- Subscription billing: recurring schedules, proration, usage-based lines
- Intercompany invoicing hook (completed in wave 40)
- Billing audit: invoice → shipment → order → price explanation trace

**Proof (testable acceptance)**

- [ ] Invoice totals equal Σ lines + charges + tax with correct rounding across currencies (property test)
- [ ] ASC 606 fixtures: 40 bundle scenarios allocate and recognize revenue matching an accountant-reviewed workbook
- [ ] Deferred revenue roll-forward reconciles to the GL deferred revenue account
- [ ] Subscription proration edge cases (mid-period start/cancel, leap day) match expected amounts

**Deploy**

Billing workspace; demo entity invoices its shipped orders; revenue schedules visible.

**References**

- [D365 revenue recognition](https://learn.microsoft.com/en-us/dynamics365/finance/accounts-receivable/revenue-recognition-overview)
- [ASC 606 / IFRS 15](https://www.ifrs.org/issued-standards/list-of-standards/ifrs-15-revenue-from-contracts-with-customers/)
- [SAP Revenue Accounting and Reporting](https://help.sap.com/docs/SAP_REVENUE_ACCOUNTING_AND_REPORTING)

**Depends on:** wave 28

---

## Wave 30: Accounts Receivable, Collections, Returns & Customer Portal

**Phase 5 — Order-to-Cash**

**Goal.** Cash application settles customer items (lockbox, remittance, bank), collections manages dunning and disputes, returns flow through RMA — and the manufacturer go-live loop closes.

**Replaces**

- *AX / D365:* Customer payments/settlement, lockbox, collections workspace, collection letters, interest notes, return orders/RMA, rebate settlement
- *SAP:* Incoming payments (F-28), lockbox (FLB2), dunning (F150), dispute management (FSCM), returns (RE orders), credit memo requests

**Deliverable**

- Cash application: manual, lockbox (BAI2), remittance advice matching, bank statement (camt.053) auto-match with ML-assisted suggestions
- Collections: aging, collection letters/dunning levels, interest notes, promises to pay, disputes workflow, customer risk scoring
- Return orders (RMA): disposition codes, return to inventory/scrap/repair, credit note, restocking charges
- Rebate settlement from wave 26 accruals; customer statements
- Customer portal: orders, invoices, payments (Stripe/Adyen adapter), returns, documents

**Proof (testable acceptance)**

- [ ] Cash application fixtures: 500 payments auto-match ≥ 90% with zero false matches on the fixture set
- [ ] Dunning run respects levels, grace days and blocks; letters generated for the expected customers only
- [ ] RMA E2E: return → inspection → credit note posts correctly to inventory and AR
- [ ] Phase 5 exit — GO-LIVE CANDIDATE: the demo manufacturer runs buy → make → sell → collect for one simulated month; all subledgers reconcile to the GL; recorded and signed off

**Deploy**

AR/collections workspace, customer portal at customer.<env>.passage.dev, payment adapter in sandbox.

**References**

- [D365 collections](https://learn.microsoft.com/en-us/dynamics365/finance/accounts-receivable/collections-overview)
- [SAP FSCM collections & dispute management](https://help.sap.com/docs/SAP_ERP/f8f1ee2f3b9e4b1ba4bbb8ac2a6b5a2e/5d0f6e5287ca4e1b93b7f3a1a3e0e1f0.html)
- [ISO 20022 camt.053](https://www.iso20022.org/iso-20022-message-definitions)
- [BAI2 lockbox format](https://www.bai.org/)

**Depends on:** wave 29

---
