# Phase 7: Treasury, Assets & Controlling (waves 36-40)

Cash and bank, fixed assets, cost accounting and budgeting, standard-cost variance analysis, intercompany and consolidation. The finance function reaches full AX/SAP parity.

## Wave 36: Cash & Bank Management, Reconciliation & Treasury

**Phase 7 — Treasury, Assets & Controlling**

**Goal.** Bank accounts, statements and reconciliation are automated via ISO 20022; cash positioning and forecasting give treasury a daily view.

**Replaces**

- *AX / D365:* Cash and bank management, bank reconciliation (advanced), checks, letters of credit/guarantees, cash flow forecasting
- *SAP:* Bank accounting, electronic bank statement (FF_5), bank account management (BAM), cash management & liquidity forecast

**Deliverable**

- Bank account master with signatories and approval; check management (void/reprint); letters of credit and guarantees
- Statement import (camt.053/BAI2/MT940) and auto-reconciliation with matching rules and ML suggestions
- Cash positioning (intraday camt.052) and cash flow forecasting from AP/AR/payroll/projects
- In-house banking basics: intercompany netting, cash pooling
- Bank connectivity adapters (SFTP/EBICS/Open-Banking API) with secure key management (Vault)
- Treasury instruments: customer bills of exchange and vendor promissory notes (draw/remit/protest/settle) and postdated checks (customer and vendor)
- Cash journal / petty cash desks with business transactions, daily count and close, cash book
- Bank fee analysis from camt.086 against bank fee agreements
- Treasury deals (FX spot/forward/swap, fixed-term deposits) with confirmation, settlement flows and key-date valuation; FX exposure view
- Loan contracts (bank and intercompany) with schedules and interest accrual; GL balance interest on intercompany current accounts
- Treasury payment requests and bank-to-bank transfers with four-eyes approval; bank chains; payment hub routing and batch monitor
- In-house cash center basics: participant current accounts, internal payment orders, interest conditions, generated camt.053 statements (flag-gated)

**Proof (testable acceptance)**

- [ ] Reconciliation fixtures: 1,000 statement lines auto-match ≥ 95% correctly
- [ ] Cash forecast equals sum of sources per bucket (property test)
- [ ] EBICS/SFTP adapter contract tests against sandbox
- [ ] Keys never leave Vault; audit of every bank file sent

**Deploy**

Treasury workspace; sandbox bank connection for the demo entity.

**References**

- [D365 cash and bank management](https://learn.microsoft.com/en-us/dynamics365/finance/cash-bank-management/cash-bank-management-overview)
- [SAP Cash Management](https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE)
- [EBICS](https://www.ebics.org/en/home)
- [HashiCorp Vault](https://developer.hashicorp.com/vault/docs)

**Depends on:** wave 35

---

## Wave 37: Fixed Assets & Leases

**Phase 7 — Treasury, Assets & Controlling**

**Goal.** Assets are acquired from AP/projects/inventory, depreciated under multiple books and methods, transferred, split, revalued and disposed with full GL integration; leases follow IFRS 16.

**Replaces**

- *AX / D365:* Fixed assets: books, depreciation profiles, value models, acquisition/depreciation/disposal journals, asset leasing (D365)
- *SAP:* Asset accounting (FI-AA): asset classes, depreciation areas, AFAB, transfers, retirements; RE-FX / contract and lease management

**Deliverable**

- Asset master with books (tax/IFRS/local), depreciation profiles (straight-line, declining balance, units of production, MACRS tables), conventions
- Transactions: acquisition (from AP invoice, project, inventory), depreciation run, adjustment, transfer, split, revaluation, disposal (sale/scrap), impairment
- Asset budgeting and CIP/under-construction from projects
- Lease accounting (IFRS 16/ASC 842): lease schedules, ROU asset and liability postings
- Physical asset inventory with barcode and reconciliation
- Asset additions/component records and insurance policies with insured-value index series
- Group assets for pooled tax depreciation (US GAA, DE Sammelposten, UK pools)
- Capital appropriation requests with variants and value-based approval feeding depreciation forecast and budgeting

**Proof (testable acceptance)**

- [ ] Depreciation fixtures: 100 assets × methods × conventions match published tables (IRS MACRS, straight-line edge cases) to the cent
- [ ] Disposal gain/loss posting correct across partial disposals
- [ ] Lease fixtures: schedules match accountant-reviewed workbook under IFRS 16
- [ ] Asset subledger reconciles to GL asset and accumulated depreciation accounts

**Deploy**

Fixed assets workspace; demo entity's assets migrated with opening net book values.

**References**

- [D365 fixed assets](https://learn.microsoft.com/en-us/dynamics365/finance/fixed-assets/fixed-assets)
- [SAP asset accounting](https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE)
- [IFRS 16 leases](https://www.ifrs.org/issued-standards/list-of-standards/ifrs-16-leases/)
- [IRS Publication 946 (MACRS)](https://www.irs.gov/publications/p946)

**Depends on:** wave 36

---

## Wave 38: Cost Accounting, Budgeting & Budget Control

**Phase 7 — Treasury, Assets & Controlling**

**Goal.** Controllers plan and control costs by cost center/object with budgets, budget control on commitments, and management reporting separate from statutory.

**Replaces**

- *AX / D365:* Budgeting, budget planning, budget control, cost accounting (cost objects/elements/allocations, cost control workspace), commitments
- *SAP:* CO: cost centers (KS01), cost elements, internal orders, activity types, assessments/distributions, budgeting (funds management), commitment management

**Deliverable**

- Budget register entries, budget planning with workflow and scenarios, top-down/bottom-up, budget versions
- Budget control: encumbrances/commitments from requisitions/POs, budget checks with tolerances and overrides
- Cost accounting ledger: cost elements, cost objects, dimension hierarchies, allocation policies (assessment/distribution), statistical measures
- Activity-based costing and internal orders; cost control workspace with variance analysis
- Management reporting cubes feeding wave 46
- Budget plan Excel worksheet templates with validated round-trip; plan generation from GL actuals, plans, FA forecast, demand forecast and position forecasts
- Cost behavior, overhead rate and cost rollup policies; template (driver-formula) allocation; top-down distribution; cost object hierarchies
- Budget reservation documents (earmarked funds: reservation, precommitment, blocking, revenue forecast)
- Investment programs with position hierarchy, budget distribution, rollup and carry-forward

**Proof (testable acceptance)**

- [ ] Budget check fixtures: POs over budget are blocked/warned per policy; encumbrance liquidation on invoice is exact
- [ ] Allocation cycles converge and reconcile (total cost in = total cost out)
- [ ] Cost accounting ledger reconciles to GL for primary costs
- [ ] Planning workflow with 3 scenarios and approvals completes

**Deploy**

Budgeting and cost accounting workspaces; demo entity budget loaded.

**References**

- [D365 cost accounting](https://learn.microsoft.com/en-us/dynamics365/finance/cost-accounting/cost-accounting-home-page)
- [D365 budget control](https://learn.microsoft.com/en-us/dynamics365/finance/budgeting/budget-control-overview-configuration)
- [SAP CO overhead cost controlling](https://help.sap.com/docs/SAP_ERP)

**Depends on:** wave 37

---

## Wave 39: Product Costing, Cost Roll-Up & Variance Analysis

**Phase 7 — Treasury, Assets & Controlling**

**Goal.** Standard costs roll up from BOMs/routings with cost groups and overhead sheets; production and purchase variances are analyzed by cause.

**Replaces**

- *AX / D365:* Costing versions, BOM calculation, cost groups, costing sheets (indirect costs), standard cost activation, production variances (lot size, quantity, price, substitution)
- *SAP:* Product cost planning (CK11N), costing runs (CK40N), cost component split, overhead costing sheets, variance calculation (KKS1), target/actual

**Deliverable**

- Costing sheet: cost groups, indirect cost nodes (surcharge/rate), cost component split
- BOM calculation (cost roll-up) by costing version, site, quantity with explosion mode and fallback rules; mass roll and activation
- Variance analysis: purchase price, production (lot size, quantity, price, substitution), planned vs actual by cost component
- Standard cost revaluation of on-hand inventory on activation
- Cost estimate comparison and what-if simulations
- Standard cost conversion checklist (actual to standard) with revaluation preview
- Ad-hoc cost estimates with costing models; mixed costing over procurement alternatives; sales order cost estimates for configured/MTO lines
- COGS and price-difference posting by cost component; parallel valuation views (legal/group/profit center)
- Parallel inventory valuation ledgers (global inventory accounting), flag-gated

**Proof (testable acceptance)**

- [ ] Roll-up fixtures: 50 multi-level products match a reference workbook across cost components
- [ ] Activation revalues inventory exactly and posts the revaluation to the GL
- [ ] Variance decomposition sums to total variance for 200 ended production orders
- [ ] Mass roll of 50k items in < 15 min

**Deploy**

Costing workspace completes; demo plant on standard cost with monthly variance report.

**References**

- [AX 2012 BOM calculations and costing sheets](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-bom-calculations)
- [AX 2012 production variances](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-production-variances)
- [SAP product cost planning](https://help.sap.com/docs/SAP_ERP)

**Depends on:** wave 38

---

## Wave 40: Intercompany & Financial Consolidation

**Phase 7 — Treasury, Assets & Controlling**

**Goal.** Intercompany trade, transfers and expenses post mirror documents automatically across legal entities; group consolidation eliminates and translates for the consolidated statements.

**Replaces**

- *AX / D365:* Intercompany trade (direct delivery, chains), intercompany accounting (due-to/due-from), consolidation (online/import), eliminations
- *SAP:* Cross-company postings, intercompany billing, Group Reporting / consolidation (SEM-BCS/ECCS), elimination entries, currency translation

**Deliverable**

- Intercompany trade: customer/vendor pairing, auto-created mirror sales/purchase orders, direct delivery chains, intercompany pricing
- Intercompany accounting: due-to/due-from entries, intercompany journals, matching and reconciliation workspace
- Consolidation: group structures, ownership %, elimination rules (IC balances, profit in inventory), currency translation (IAS 21), minority interest
- Consolidated statements via financial reporting (wave 15) with drill to entity
- Transfer-pricing documentation hooks
- Centralized payments (pay/receive on behalf of) and intercompany charges for expenses, timesheets and vendor invoice lines with transfer prices
- Intercompany on-hand visibility and intercompany planning groups with sequenced MRP
- Reclassification and balance-sheet regrouping rules (entity and group level) and two-sided IC elimination with thresholds
- Consolidation of investments (first/subsequent, ownership change, divestiture, equity method), top-side group journals, consolidation monitor with validation rules

**Proof (testable acceptance)**

- [ ] IC mirror documents always balance across entities (property test over 1,000 random IC orders)
- [ ] Consolidation fixtures: 3-entity group with IC sales and inventory matches accountant-reviewed eliminations
- [ ] IC reconciliation flags every planted mismatch
- [ ] Phase 7 exit: group consolidated statements produced for the demo group with full finance parity sign-off

**Deploy**

Intercompany and consolidation workspaces live; demo group (3 entities, 2 currencies) consolidated.

**References**

- [D365 intercompany trade](https://learn.microsoft.com/en-us/dynamics365/supply-chain/sales-marketing/intercompany-trade)
- [D365 consolidation](https://learn.microsoft.com/en-us/dynamics365/finance/budgeting/consolidation-elimination-overview)
- [SAP Group Reporting](https://help.sap.com/docs/SAP_S4HANA_FINANCE_FOR_GROUP_REPORTING)
- [IFRS 10 consolidated financial statements](https://www.ifrs.org/issued-standards/list-of-standards/ifrs-10-consolidated-financial-statements/)

**Depends on:** wave 39

---
