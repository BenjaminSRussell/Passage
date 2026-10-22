# Phase 2: Financial Core (waves 11-15)

Stand up a single append-only universal journal (one source of truth, like SAP ACDOCA / AX GeneralJournalAccountEntry) with dimensions, multi-currency, parallel ledgers, tax, period close and financial reporting. Every later module posts through this engine.

## Wave 11: Universal Journal & Chart of Accounts

**Phase 2 — Financial Core**

**Goal.** One append-only, double-entry journal with financial dimensions is the single source of truth for every posting; it balances by construction.

**Replaces**

- *AX / D365:* General ledger, chart of accounts, financial dimensions, account structures, ledger journals, GeneralJournalAccountEntry
- *SAP:* ACDOCA universal journal, chart of accounts (OB13), GL accounts (FS00), document posting (FB50)

**Deliverable**

- Ledger service (Go + Postgres): accounts, account structures with dimension validation rules, journal entries with lines; posting is atomic, immutable, idempotent (idempotency keys)
- Financial dimensions (up to 20 per ledger), default dimensions on master data, dimension validation
- Posting API used by all subledgers: `Post(JournalEntry)` with source-document linkage and reversal
- Balance projection: pre-aggregated balances by account × dimension × period, rebuilt from the journal (event-sourced)
- Journal workspace: manual journals, recurring journals, templates, approval workflow, attachments
- Shared financial reason codes (ledger, bank, customer, vendor, fixed asset, counting) with required reversal reasons and negative-posting (storno) reversal
- Advanced rule structures, default description templates, and voucher attachments with document types and retention classes
- Open-item managed G/L accounts: manual clearing, post-with-clearing, reset, activation run, automatic clearing rules and tolerance groups (F-03/F-04/F.13/OBA0/OB74 parity)
- User-defined validation rules and a dimension derivation/substitution strategy engine (OB28/OBBH/KEDR/FMDERIVE parity), including functional area for cost-of-sales accounting
- Document splitting (dimension inheritance to balance-sheet and tax lines with zero-balance clearing) and audit-logged dimension realignment runs

**Proof (testable acceptance)**

- [ ] Property test: 1M random balanced entries posted concurrently; trial balance sums to zero; balance projection equals journal re-aggregation
- [ ] Posting rejects unbalanced, closed-period, or invalid-dimension entries (table-driven negative tests)
- [ ] Idempotency: duplicate Post with the same key is a no-op
- [ ] Throughput: 5,000 journal lines/sec sustained on the dev cluster with p99 < 200 ms
- [ ] Reversal creates an exact negating entry linked to the original

**Deploy**

GL service and journal workspace live; the demo manufacturer's chart of accounts and opening balances loaded.

**References**

- [SAP ACDOCA universal journal](https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE)
- [AX 2012 financial dimensions & account structures](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-financial-dimensions-and-account-structures)
- [TigerBeetle debit/credit ledger design](https://docs.tigerbeetle.com/concepts/debit-credit/)
- [Modern Treasury: double-entry ledgers](https://www.moderntreasury.com/journal/accounting-for-developers-part-i)
- [Martin Fowler: Accounting patterns](https://martinfowler.com/apsupp/accounting.pdf)

**Depends on:** wave 10

---

## Wave 12: Multi-Currency, Parallel Ledgers & Revaluation

**Phase 2 — Financial Core**

**Goal.** Every posting carries transaction, accounting and reporting currency amounts; parallel ledgers (IFRS/local GAAP/tax) post simultaneously; FX revaluation is automated.

**Replaces**

- *AX / D365:* Exchange rate types, accounting/reporting currency, foreign currency revaluation, posting layers (current/operations/tax)
- *SAP:* Currency types (10/30/40…), ledger groups, parallel ledgers (FI-GL New GL), FAGL_FC_VAL revaluation

**Deliverable**

- Exchange rate service: rate types, provider adapters (ECB, OpenExchangeRates), triangulation, historical rates, rate audit
- Three-currency amounts on every journal line; rounding rules per currency; realized/unrealized gain-loss accounts
- Ledger groups / posting layers: a source document posts to N ledgers with per-ledger accounting principle differences
- Foreign currency revaluation job (GL, AP, AR open items) with reversal next period
- Currency translation for reporting currency restatement
- Market data series (interest rates, indices, prices) with provider import, audit and date lookup in the fx service

**Proof (testable acceptance)**

- [ ] Rounding: 10k random amounts across JPY/KWD/USD/EUR reconcile to the cent with ISO 4217 minor units
- [ ] Parallel-ledger test: one document posts differently to IFRS vs local-GAAP ledger per rules; trial balances per ledger each sum to zero
- [ ] Revaluation on a fixture set matches a hand-computed workbook (SAP FAGL_FC_VAL semantics)
- [ ] Rate provider outage falls back to last-known rate and raises an alert

**Deploy**

Exchange rates workspace; revaluation batch scheduled in dev; demo entity with EUR reporting currency.

**References**

- [IAS 21 effects of changes in FX rates](https://www.ifrs.org/issued-standards/list-of-standards/ias-21-the-effects-of-changes-in-foreign-exchange-rates/)
- [SAP parallel ledgers in New GL](https://help.sap.com/docs/SAP_ERP)
- [AX 2012 foreign currency revaluation](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/foreign-currency-revaluation-general-ledger)
- [ECB exchange rates API](https://data.ecb.europa.eu/help/api/overview)

**Depends on:** wave 11

---

## Wave 13: Tax Engine (Sales/Use, VAT/GST, Withholding)

**Phase 2 — Financial Core**

**Goal.** Any document line gets the right tax determination, calculation and posting across jurisdictions, with pluggable external providers and audit-ready reporting.

**Replaces**

- *AX / D365:* Sales tax codes/groups/item sales tax groups, settlement periods, withholding tax, EU sales list, Intrastat
- *SAP:* Tax procedures/condition types (FTXP), tax codes, jurisdiction codes, withholding tax, VAT return reporting (ACR)

**Deliverable**

- Tax determination model: tax groups × item tax groups → tax codes; jurisdiction hierarchy; effective-dated rates; exemption certificates
- Calculation: inclusive/exclusive, compound, rounding, reverse charge, use tax, partial recoverability
- Withholding tax (vendor and customer), 1099 / Form 26AS style reporting hooks
- Provider adapters: Avalara/Vertex-compatible interface + an internal rules engine as default
- Tax settlement job, VAT return, EU sales list, Intrastat and SAF-T extracts
- Intrastat master data (CN8 commodity codes, transaction codes, transport modes, ports, country properties) and an Intrastat journal with transfer, validation, compression, correction and export
- VAT recovery policies on expenses (recoverable % per category and country) and a foreign VAT reclaim extract
- Withholding tax remittance runs with challan/bank references and gap-free certificate numbering hooks for country forms

**Proof (testable acceptance)**

- [ ] Tax matrix fixture: 500 scenarios across US/EU/IN/CA matched to expected results (community-sourced + accountant-reviewed)
- [ ] Settlement run reconciles to the GL tax accounts to the cent
- [ ] Provider failover: external provider timeout falls back to the internal engine and flags the document
- [ ] SAF-T export validates against the OECD XSD

**Deploy**

Tax configuration workspace; demo entities configured for US sales tax and EU VAT.

**References**

- [AX 2012 sales tax overview](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-sales-tax)
- [SAP tax determination (FI)](https://help.sap.com/docs/SAP_ERP)
- [OECD SAF-T](https://www.oecd.org/tax/forum-on-tax-administration/publications-and-products/guidance-for-the-standard-audit-file-tax-version-2-0.htm)
- [Avalara AvaTax API](https://developer.avalara.com/api-reference/avatax/rest/v2/)

**Depends on:** wave 12

---

## Wave 14: Period Close, Year-End & Allocations

**Phase 2 — Financial Core**

**Goal.** Controllers run a checklist-driven close across entities with period controls per module, automated allocations and accruals, and a closed period that cannot be reopened silently.

**Replaces**

- *AX / D365:* Ledger calendar period status per module, financial period close workspace, allocation rules, year-end close, accruals schemes
- *SAP:* Posting periods (OB52), closing cockpit, allocations (KSV5/KSU5), balance carry-forward (FAGLGVTR), accrual engine

**Deliverable**

- Period control per ledger × module × user group (open/on-hold/closed) enforced at the posting API
- Close checklist: templates, task dependencies, assignees, due dates, status board across legal entities
- Allocation engine: fixed, basis (statistical measure), and iterative allocations with reversal
- Accrual schemes and automatic reversals; recurring journal scheduling
- Year-end close: P&L to retained earnings, balance carry-forward per dimension, opening transactions
- Balance-sheet regrouping: reclassification by remaining term, vendors with debit balances and customers with credit balances, auto-reversed (FAGLF101 parity)

**Proof (testable acceptance)**

- [ ] Posting into a closed period is rejected for every subledger posting path (contract tests)
- [ ] Allocation fixtures: results match hand-computed totals; iterative allocation converges
- [ ] Year-end close of a 12-period dataset produces opening balances equal to closing balances per dimension
- [ ] Close checklist with 40 tasks across 3 entities completes with blocking dependencies honored

**Deploy**

Financial period close workspace live; demo entities close a simulated period in a recorded walkthrough.

**References**

- [D365 financial period close workspace](https://learn.microsoft.com/en-us/dynamics365/finance/general-ledger/financial-period-close-workspace)
- [AX 2012 allocation rules](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-ledger-allocation-rules)
- [SAP Financial Closing Cockpit](https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE)

**Depends on:** wave 13

---

## Wave 15: Financial Reporting, Statements & Audit Trail

**Phase 2 — Financial Core**

**Goal.** Balance sheet, income statement and cash flow are generated from the journal with drill-down to the source document, plus a report designer for custom statements.

**Replaces**

- *AX / D365:* Management Reporter / Financial reporting, trial balance, ledger transaction inquiries, audit trail
- *SAP:* Financial statement versions (OB58), Fiori financial statements, SAP Analytics Cloud for finance, document flow drill-down

**Deliverable**

- Report definitions: row/column/tree designer (Management Reporter model) over account structures and dimensions
- Standard statements: trial balance, balance sheet, P&L, cash flow (indirect), comparative and consolidated-by-entity views
- Drill-down: statement → balance → journal lines → source document, with trace id
- Scheduled distribution (PDF/XLSX/email), row-level security on dimensions
- Audit trail report: who posted what, approvals, reversals, period-control overrides
- Derived financial hierarchies over main account categories and dimension values, usable as statement rows and reporting trees
- Standard P&L by function of expense (PL-FUNC) over the functional area dimension

**Proof (testable acceptance)**

- [ ] Statements for the demo dataset tie to the trial balance to the cent; cash flow reconciles to the change in cash
- [ ] Report designer round-trips a 200-row definition through JSON export/import
- [ ] Drill-down from a balance to its lines returns exactly the lines that sum to the balance (property test)
- [ ] Phase 2 exit: finance stakeholders sign off a closed period with full statements produced from Passage

**Deploy**

Financial reporting workspace; nightly statement pack scheduled for the demo entities.

**References**

- [D365 financial reporting (Management Reporter)](https://learn.microsoft.com/en-us/dynamics365/finance/general-ledger/financial-reporting-getting-started)
- [SAP financial statement versions](https://help.sap.com/docs/SAP_ERP)
- [IAS 7 statement of cash flows](https://www.ifrs.org/issued-standards/list-of-standards/ias-7-statement-of-cash-flows/)

**Depends on:** wave 14

---
