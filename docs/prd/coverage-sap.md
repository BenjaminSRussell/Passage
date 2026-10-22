# Part 13 — Coverage Inventory: SAP ECC 6.0 / S/4HANA → Passage

This part is the completeness oracle for the SAP side of the parity goal (PRD §2 goal 1, §5 scope map). It inventories SAP ECC 6.0 and S/4HANA functionality module by module — by transaction code and by IMG (SPRO) node — and maps every row to one of the 50 Passage waves in [roadmap/waves.json](../../roadmap/waves.json). Its purpose is to make "we missed something" a question that can be answered by grep rather than by fear. The companion inventory for Dynamics AX / D365 F&SCM is Part 12.

## 13.0 How to read this inventory

Each SAP application component gets one section with four tables:

1. **Master data & documents** — the create/change/display transactions and the documents they produce (`T-code | Name | Purpose | Passage status | Wave | Notes`).
2. **Reports & inquiries** — standard list, line-item and analysis reports (`T-code | Name | Passage status | Wave | Notes`).
3. **Periodic processing** — closing, batch and mass-processing transactions (`T-code | Name | Passage status | Wave | Notes`).
4. **Customizing (SPRO)** — the IMG nodes, their customizing tables and the key options Passage must offer an equivalent for (`IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes`).

**Status legend** (exactly one token per row, always in the "Passage status" column):

- `COVERED-<wave>` — the wave's `deliverable`/`proof` in waves.json clearly includes an equivalent. The wave column repeats the number.
- `PARTIAL-<wave>` — the wave covers the core but the Notes column names what is missing. Every PARTIAL row is repeated in the gap register (§13.99).
- `GAP` — no wave clearly covers the item. The wave column holds a *proposed* assignment (`propose <n>`); the gap register collects them for a roadmap decision.
- `OUT-OF-SCOPE` — excluded by a PRD non-goal (§2) or by the manufacturer focus; the reason is in Notes.
- `SUPERSEDED-<wave>` — Passage deliberately does it a different way; the Notes column names the Passage mechanism. A SUPERSEDED row is **not** a gap: the SAP transaction has no one-to-one twin because the underlying mechanism (e.g. the condition technique's separate rebate module, SAP GUI, ABAP enhancement spots) is replaced by something structurally different.

Conventions:

- T-codes are given in their ECC 6.0 form; where S/4HANA replaced a transaction with a Fiori app or a new transaction, both are named (e.g. `FD32 → UKM_BP`, `VBO1 → WCOCO`). Fiori apps are cited by name, not by app number.
- "Wave" refers to the `n` in waves.json. Phase-exit waves (5, 10, 15, 20, 25, 30, 35, 40, 45, 50) are the sign-off points at which a module's rows must be demonstrably closed.
- IMG rows name the customizing table where it is well known (T001, T001B, T030, T156, T161, T685, TVAK…). Passage stores all customizing as metadata-registry configuration (wave 3) scoped per tenant / legal entity (wave 6); the "Key options" column lists the switches Passage must expose, not every SAP field.
- Rows are intentionally granular (a create/change/display triple is one row; a report family such as `S_ALR_87012*` is one row per distinct report, not one row per variant).
- Where SAP offers both an ECC transaction and a New GL / S/4 successor, the successor is inventoried and the predecessor noted, so classic GL (`GLT0`-based) transactions appear once under the FI-GL section.

A per-section roll-up of the same counts is in §13.98 and the gap register (every GAP and PARTIAL row with its current or proposed wave) is in §13.99. Status totals across all four tables of every section (computed from the rows, excluding §13.98–13.99):

| Status | Rows | Share |
|---|---|---|
| COVERED | 2049 | 74.6% |
| PARTIAL | 310 | 11.3% |
| GAP | 91 | 3.3% |
| OUT-OF-SCOPE | 22 | 0.8% |
| SUPERSEDED | 274 | 10.0% |
| **Total rows** | **2746** | 100% |

---

## 13.1 FI-GL — General Ledger (classic GL, New GL, ledgers, ACDOCA)

SAP context: FI-GL in ECC 6.0 runs either classic GL (`GLT0` totals, special-purpose ledgers) or New GL (`FAGLFLEXT`, document splitting, parallel ledgers). S/4HANA collapses FI and CO totals into the universal journal `ACDOCA` (one line item table; CO objects and ledgers as columns). Passage's wave 11 universal journal is built on the S/4 model: one append-only line-item store, balances as projections, dimensions as columns, parallel ledgers via ledger groups (wave 12).

### 13.1.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| FS00 | G/L account master (centrally) | Create/change/display chart-of-accounts and company-code segment of a G/L account | COVERED-11 | 11 | Account entity with per-legal-entity attributes (currency, open-item flag, posting block, tax category, field status) |
| FSP0 | G/L account in chart of accounts | Chart segment only | COVERED-11 | 11 | Same entity; chart-level vs entity-level attributes split in metadata |
| FSS0 | G/L account in company code | Company-code segment only | COVERED-11 | 11 | Per-legal-entity release of an account |
| FS15 / FS16 | Copy G/L accounts (send/receive) | Copy company-code segments between company codes | COVERED-11 | 11 | "Release account to legal entity" bulk action |
| FSM1 / FSM2 / FSM3 | Sample accounts | Templates for company-code segments | SUPERSEDED-11 | 11 | Account templates in the journal workspace; no separate sample-account object |
| KDH1 / KDH2 / KDH3 | Account groups (G/L account groups hierarchy) | Hierarchies for FSV/reporting | COVERED-15 | 15 | Account structures and report trees |
| FB50 | Enter G/L account document | Single-screen G/L posting (Enjoy) | COVERED-11 | 11 | Manual journal; idempotent Post API |
| FB50L | Enter G/L account document for ledger group | Ledger-specific manual posting | COVERED-12 | 12 | Journal with ledger-group scope |
| F-02 | Enter G/L account posting (classic) | Posting-key based general posting | SUPERSEDED-11 | 11 | Posting keys replaced by debit/credit amount sign on lines |
| FB01 | Post document (general) | Any document type, any account type | COVERED-11 | 11 | Single Post API for all subledger-aware lines |
| FV50 / FBV0 / FBV2 / FBV3 | Park / post / change / display parked G/L document | Draft documents for later posting or approval | COVERED-11 | 11 | Draft status + approval workflow (Temporal) |
| FBV4 / FBV5 / FBV6 | Change parked header / display changes / reject | Parked document administration | COVERED-11 | 11 | Draft audit from CDC (wave 5) |
| FB02 / FB03 / FB04 | Change / display / display changes to document | Document maintenance (header text, reference, assignment only) | COVERED-11 | 11 | Post-posting edit limited to non-financial fields; audit log |
| FB08 / F.80 | Reverse document / mass reversal | Negating entry linked to the original | COVERED-11 | 11 | Reversal API creates linked negating entry; mass reversal via batch job (wave 4) |
| FBRA | Reset cleared items | Undo clearing before reversal | COVERED-19 | 19 | Settlement reversal in AP/AR; GL open-item accounts see §13.1 note on F-03 |
| FBD1 / FBD2 / FBD3 / FBD4 | Recurring entry document create/change/display/changes | Recurring journals with run schedule | COVERED-11 | 11 | Recurring journals with schedule; execution via wave 4 batch |
| F.14 / F.15 | Execute recurring entries / list recurring entries | Batch creation from recurring templates | COVERED-11 | 11 | Scheduled recurring-journal job |
| FBS1 | Enter accrual/deferral document | Reversing document with reversal date | COVERED-14 | 14 | Accrual schemes with automatic reversal |
| F.81 | Reverse accrual/deferral documents | Mass reversal of FBS1 documents | COVERED-14 | 14 | Automatic reversal on schedule |
| FBM1 / FBM2 / FBM3 | Sample documents | Non-posting template documents | SUPERSEDED-11 | 11 | Journal templates |
| F-03 / F-04 | Clear G/L account / post with clearing | Open-item clearing on OI-managed G/L accounts | PARTIAL-11 | 11 | Open-item management specified only for AP/AR (waves 19/30) and GR/IR (wave 18); generic OI-managed G/L accounts (clearing accounts, bank sub-accounts) need the same clearing engine exposed on GL |
| F-05 | Post foreign currency valuation (manual) | Manual FX adjustment posting | COVERED-12 | 12 | Manual journal in reporting currency |
| F-65 | Preliminary posting (park) | Classic parking | COVERED-11 | 11 | Draft journals |
| FB41 | Post tax payable | Transfer of tax balances | COVERED-13 | 13 | Tax settlement job posts to tax payable |
| OB08 | Enter exchange rates (also master data) | Rates by rate type, date, pair | COVERED-12 | 12 | Exchange rate service with provider adapters and audit |
| FBCJ | Cash journal | Petty cash with receipts/expenses and GL posting | GAP | propose 36 | No wave mentions a petty-cash/cash-journal document; small but common on plant floors — add to wave 36 (cash & bank) as "cash journal with receipts, business transactions and daily closing" |
| FBCJC0 / FBCJC1 / FBCJC2 / FBCJC3 | Cash journal setup, number ranges, business transactions, print | Cash journal customizing | GAP | propose 36 | Follows FBCJ |
| FAGLL03 (document) | Ledger-specific line item entry context | See reports | COVERED-12 | 12 | — |
| GB01 / GB02 | Enter/post G/L documents with document splitting characteristics | Classic FI-SL documents | SUPERSEDED-12 | 12 | Special-purpose ledger replaced by parallel ledgers + dimensions |
| OB_GLACC11 / OB_GLACC12 / OB_GLACC13 | Mass maintenance of G/L accounts (chart / company code / names) | Mass change | COVERED-5 | 5 | Grid mass edit + Excel import/export on any entity |
| FAGL_ACTIVATE_OP | Activate open-item management on G/L accounts | Conversion utility | PARTIAL-11 | 11 | Depends on the GL OI gap above |
| FAGL_SWITCH_TO_OPEN_ITEM | Switch to OI management (S/4) | Same | PARTIAL-11 | 11 | Same |
| GCAC | Ledger comparison | Compare two ledgers | COVERED-12 | 12 | Per-ledger trial balances with diff |
| FAGLB03 (document view) | Ledger balance display → line items | See reports | COVERED-15 | 15 | — |
| FINS_ACDOC_CUST | Universal journal customizing overview (S/4) | ACDOCA extensibility | SUPERSEDED-3 | 3 | Journal-line extensions via the metadata kernel (customer fields on JournalLine) |
| FAGL_MIG_* | New GL migration cockpit | Classic → New GL migration | SUPERSEDED-49 | 49 | Migration factory loads GL balances by dimension directly |
| ACC_DOCUMENT (BAPI_ACC_DOCUMENT_POST) | Accounting document BAPI | Programmatic posting | COVERED-11 | 11 | Post API (REST/gRPC) with idempotency keys |

### 13.1.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| FBL3N | G/L account line items (classic) | COVERED-11 | 11 | Journal line inquiry with saved views, grid, export |
| FAGLL03 | G/L account line items (New GL, ledger-aware) | COVERED-12 | 12 | Ledger filter on the same inquiry |
| FAGLL03H | G/L line item browser (HANA) | COVERED-46 | 46 | Lakehouse-backed line-item browser for history; OLTP inquiry for recent |
| FS10N / FAGLB03 | G/L account balance display (classic / New GL) | COVERED-11 | 11 | Balance projection by account × dimension × period with drill to lines |
| FS10NA | Balance display with drill-down | COVERED-15 | 15 | Statement → balance → lines drill |
| F.01 | Financial statements (RFBILA00) | COVERED-15 | 15 | Balance sheet / P&L from financial statement version equivalent |
| S_ALR_87012284 | Financial statements (FSV, with comparison) | COVERED-15 | 15 | Comparative columns in report designer |
| S_ALR_87012277 | G/L account balances | COVERED-15 | 15 | Trial balance |
| S_ALR_87012301 | Totals and balances | COVERED-15 | 15 | Trial balance by period |
| S_ALR_87012282 | G/L line items, list for printing | COVERED-11 | 11 | Printable line-item list |
| S_ALR_87012287 | Document journal | COVERED-15 | 15 | Audit trail / document journal |
| S_ALR_87012289 | Compact document journal | COVERED-15 | 15 | Same, compact layout |
| S_ALR_87012291 | Line item journal | COVERED-15 | 15 | Same |
| S_ALR_87012293 | Display of changed documents | COVERED-5 | 5 | Audit log viewer |
| S_ALR_87012308 | Display changes to G/L accounts | COVERED-5 | 5 | Audit log on Account entity |
| S_ALR_87012326 | Chart of accounts list | COVERED-11 | 11 | Account list export |
| S_ALR_87012328 | G/L account list | COVERED-11 | 11 | Same |
| S_ALR_87012332 | G/L account statements | COVERED-15 | 15 | Account statement report |
| S_ALR_87012341 / 87012342 | Invoice numbers allocated twice / gaps in document number assignment | COVERED-5 | 5 | Number-sequence gap report (gap-free mode audit) |
| S_ALR_87012357 / 87012356 | Advance return for tax on sales/purchases / sales tax report | COVERED-13 | 13 | VAT return extracts |
| S_ALR_87012382 | Cash flow (direct method, from FSV) | COVERED-15 | 15 | Indirect cash flow standard; direct via report designer |
| S_PL0_86000028 / S_PL0_86000029 | Financial statement: actual/actual comparison, plan/actual | COVERED-15 | 15 | Comparative statements; plan column after wave 38 |
| S_PL0_86000030 | G/L account balances (New GL) | COVERED-12 | 12 | Ledger-aware trial balance |
| S_PL0_86000031 | Transaction figures: account balance | COVERED-11 | 11 | — |
| S_E38_98000088 | Profit center group: plan/actual/variance | COVERED-38 | 38 | Dimension = profit center; plan from budgeting |
| S_AC0_52000887 / 52000888 | Receivables/payables by profit center (New GL) | COVERED-15 | 15 | Dimension-level subledger report |
| FAGLF03 | Reconciliation ledger vs. documents (New GL) | COVERED-16 | 16 | Daily automated subledger-to-journal reconciliation check |
| F.03 | Financial accounting comparative analysis (SAPF190) | COVERED-16 | 16 | Same reconciliation job |
| GR55 / GRR1 / GRR2 / GRR3 | Report Painter / Report Writer report groups and reports | SUPERSEDED-15 | 15 | Row/column report designer (Management-Reporter model) replaces Report Painter; wave 46 semantic layer for ad-hoc |
| FGI0 / FGI1 / FGI3 | Drill-down reporting (New GL): execute / create report / display | SUPERSEDED-15 | 15 | Report designer + drill-down |
| FGI4 / FGI5 / FGI6 | Drill-down report forms | SUPERSEDED-15 | 15 | Same |
| FAGL_SCHEDULE_LOG (FAGLCOFITRACEADMIN) | Trace for CO-FI real-time integration | SUPERSEDED-11 | 11 | No CO→FI reconciliation needed: single journal |
| S_ALR_87012309 | Cash book / cash journal report | GAP | propose 36 | Follows FBCJ gap |
| FAGLGVTR (log) | Balance carry-forward log | COVERED-14 | 14 | Year-end job log |
| Fiori: Trial Balance | Trial balance (ledger, dimensions, comparison) | COVERED-15 | 15 | — |
| Fiori: Display Line Items in General Ledger | S/4 line-item app | COVERED-11 | 11 | — |
| Fiori: Display Financial Statement | S/4 statements app | COVERED-15 | 15 | — |
| Fiori: Balance Sheet/Income Statement — Multidimensional | Dimension-pivot statements | COVERED-46 | 46 | Semantic layer pivot |
| Fiori: Manage Journal Entries | Search/display/reverse journal entries | COVERED-11 | 11 | — |
| Fiori: Verify General Journal Entries (approval) | Workflow-based journal approval | COVERED-11 | 11 | Approval workflow on journals |
| Fiori: Audit Journal | Chronological audit journal | COVERED-15 | 15 | Audit trail report |
| Fiori: Journal Entry Analyzer | Analyze journal entries by dimension | COVERED-46 | 46 | — |

### 13.1.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| OB52 (operational use) | Open/close posting periods | COVERED-14 | 14 | Period control per ledger × module × user group |
| FAGL_FC_VAL / F.05 (classic) | Foreign currency valuation (GL, AP, AR open items) | COVERED-12 | 12 | Revaluation job with next-period reversal |
| FAGL_FC_TRANS | Currency translation (balances into group currency) | COVERED-12 | 12 | Reporting-currency translation |
| FAGLGVTR / F.16 (classic) | Balance carry-forward (New GL / classic) | COVERED-14 | 14 | Year-end close: P&L to retained earnings, BS carry-forward per dimension |
| F.07 | Balance carry-forward AP/AR | COVERED-14 | 14 | Subledger carry-forward in the same job |
| F.13 / F13E | Automatic clearing (with/without specification of clearing currency) | PARTIAL-11 | 11 | Automatic clearing specified for GR/IR (wave 18) and AP/AR settlement; generic rule-based GL auto-clearing is part of the GL OI gap |
| FAGLF101 / F101 | Regroup receivables/payables (sorted list, reclassification by remaining term) | GAP | propose 14 | Balance-sheet reclassification (short/long-term, debit-balance vendors, credit-balance customers) is a standard close step not named in wave 14's checklist/allocations — add as a close task type |
| F.19 | GR/IR analysis / regrouping | COVERED-18 | 18 | GR/IR reconciliation report and reclass |
| F.50 / F.52 | G/L interest calculation (balance / account interest) | GAP | propose 30 | Interest on GL balances (e.g. intercompany loans) is not named; wave 30 covers AR interest notes only — extend the interest engine to GL/intercompany accounts |
| ACACACT / ACACTREE01 / ACACPSITEMS | Accrual engine: periodic run / accrual objects / display postings | COVERED-14 | 14 | Accrual schemes; recurring accrual objects |
| ACACADJ | Accrual engine: adjust accrual | COVERED-14 | 14 | — |
| FAGL_CL_REGROUP / FAGL_CL_MIGRATION | Closing: regrouping (S/4) | GAP | propose 14 | Same as FAGLF101 |
| F.97 / F.98 / F.99 | Balance confirmations for G/L / AP / AR | GAP | propose 19 | Balance confirmation letters (vendor/customer/GL) not named in waves 19 or 30 — add to wave 30 customer statements + wave 19 vendor confirmations |
| FB1S / FB1D / FB1K | Clear G/L / customer / vendor (classic clearing) | COVERED-19 | 19 | Settlement; GL part in GL OI gap |
| GCAC | Ledger comparison (closing check) | COVERED-12 | 12 | — |
| FINS_CO_RECON (FINS_RECON_RC0..RC3) | S/4 reconciliation reports (ACDOCA vs. BSEG/COEP) | SUPERSEDED-11 | 11 | No dual tables to reconcile; balance projection vs journal check is the equivalent |
| FINSC_LEDGER (admin run) | Ledger settings activation | COVERED-12 | 12 | Ledger group configuration |
| CLOCO / CLOCOC / CLOCOS | Financial Closing Cockpit: task list / templates / monitor | COVERED-14 | 14 | Close checklist with templates, dependencies, assignees, status board |
| SCMA | Schedule Manager (classic) | SUPERSEDED-14 | 14 | Close checklist + wave 4 batch scheduler |
| Fiori: Manage Closing Tasks / Advanced Financial Closing | S/4 close orchestration | COVERED-14 | 14 | Close checklist across entities |
| SAFT / Fiori: Run Statutory Reports (ACR) | Advanced Compliance Reporting (SAF-T, VAT returns) | COVERED-13 | 13 | SAF-T extract validated to OECD XSD; VAT returns; statutory packs in wave 48 |
| FAGL_SCHEDULE (FAGL_PLAN_*) | Plan data entry in New GL (GP12N etc.) | COVERED-38 | 38 | Budget register against ledger dimensions |
| GP12N / GP12NA | Plan data entry / display (New GL) | COVERED-38 | 38 | — |
| FAGL_CO_PLAN | Transfer CO plan data to New GL | SUPERSEDED-38 | 38 | One planning store |
| S_ALR_87099918 (RFUMSV00 run) | Advance tax return run | COVERED-13 | 13 | Tax settlement job |

### 13.1.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OX02 — Company code | T001 | Country, currency, language, fiscal year variant, chart of accounts | COVERED-6 | 6 | LegalEntity entity |
| OBY6 — Company code global parameters | T001 | FYV, posting period variant, field status variant, negative postings, business area FS, tax base net/gross, cash management active | COVERED-11 | 11 | LegalEntity accounting settings |
| OB13 / OB62 — Chart of accounts / assign to company code | T004, T001 | Operational / group / country chart; length; integration with CO | COVERED-11 | 11 | Account structure per ledger; alternative account numbers via dimension mapping |
| OBD4 — Account groups | T077S | Number ranges, field status per group | COVERED-11 | 11 | Account groups with field visibility rules (metadata field attributes) |
| OB53 — Retained earnings account | T030 (BIL) | P&L statement account type | COVERED-14 | 14 | Year-end close settings |
| OB29 — Fiscal year variants | T009, T009B | Calendar/non-calendar, special periods, shortened FY | COVERED-6 | 6 | Fiscal calendars: 4-4-5, 13-period, calendar; special periods needed — covered by period types |
| OB37 — Assign fiscal year variant to company code | T001 | — | COVERED-6 | 6 | Calendar per legal entity / ledger |
| OB52 — Posting period variants: open/close periods | T001B | Account type ranges, from/to period, authorization group (2nd interval) | COVERED-14 | 14 | Period control per ledger × module × user group |
| OBBO / OBBP — Posting period variant define / assign | T010O, T001 | — | COVERED-14 | 14 | — |
| OBA7 — Document types | T003 | Number range, account types allowed, reversal doc type, net posting, authorization group, reference/doc header text required | COVERED-11 | 11 | Journal/document types with number sequence and allowed line types |
| FBN1 / OBH2 — Number ranges for accounting documents / copy | NRIV | Year-dependent, external, interval | COVERED-5 | 5 | Number sequence service, gap-free mode for fiscal docs |
| OB41 — Posting keys | TBSL | D/C, account type, field status, sales-related, special G/L | SUPERSEDED-11 | 11 | No posting keys: line = account + signed amount + line type |
| OBC4 — Field status variants / groups | T004F, T004V | Required/optional/suppressed per field group | COVERED-3 | 3 | Metadata field attributes per account group / line type |
| OBC5 — Assign company code to field status variant | T001 | — | COVERED-11 | 11 | — |
| OB28 / GGB0 — Validations (FI) | GB90*, T001Q | Prerequisite/check/message at header, line, document level | PARTIAL-11 | 11 | Wave 11 has dimension validation rules; a general user-defined validation rule language (Boolean rules over header/line fields with message classes) is only implied by wave 3 "validation" — scope it explicitly |
| OBBH / GGB1 — Substitutions (FI) | GB91*, T001R | Field substitution with exits at line/document | PARTIAL-11 | 11 | Default dimensions on master data exist; rule-based substitution (derive dimension from another field) needs an explicit rule engine on the Post API |
| OBBZ / OBBW — Substitution/validation for cost of sales accounting | — | Functional area derivation | PARTIAL-11 | 11 | Functional area as a dimension with derivation rules (see above) |
| OKKP (FI side) — Controlling area assignment | TKA01, TKA02 | Company code ↔ controlling area, currency type | SUPERSEDED-11 | 11 | No separate controlling area: dimensions on the journal; wave 38 management reporting |
| OB22 / FINSC_LEDGER — Parallel currencies per company code / ledger settings (S/4) | T001A, FINSC_LEDGER | Currency types 10/30/40, valuation view, ledger type (standard/extension), accounting principle, FYV per ledger | COVERED-12 | 12 | Three-currency lines; ledger groups; extension ledgers (adjustment-only) need confirming — treat as covered by ledger groups |
| FAGL_ACTIVATION / FAGL_ACTIVEC — Activate New GL | FAGL_ACTIVEC | — | SUPERSEDED-11 | 11 | Single journal always on |
| Define ledgers for GL accounting (FAGL_LEDGER) | T881, T882G | Leading ledger, representative ledger | COVERED-12 | 12 | — |
| Define ledger groups (FAGL_LEDGER_GRP) | T8G*, FAGL_TLDGRP | Group of ledgers for postings | COVERED-12 | 12 | — |
| Define accounting principles / assign to ledger groups | TACC_PRINCIPLE, TACC_TRGT_LDGR | IFRS/local GAAP/tax | COVERED-12 | 12 | — |
| Document splitting: classify G/L accounts, document types, splitting method, zero-balance, constants (FAGL_SPLIT*) | T8G01…T8G40 | Active/passive split, characteristics (PC, segment, business area), zero-balance clearing account, inheritance | PARTIAL-11 | 11 | Wave 11 requires every line to carry dimensions, so dimension-complete documents are native; but *inheritance to balance-sheet lines* (AP/AR/tax lines split by the expense lines' profit centers) and zero-balance clearing per dimension are not named — needed for balance sheet by profit center/segment |
| Define segment / derive segment from profit center | FAGL_SEGM, CEPC | — | COVERED-11 | 11 | Segment as a dimension; derivation default from profit center dimension |
| OB08 / OBBS / OBBT — Exchange rate types, ratios, rate maintenance | TCURR, TCURV, TCURF, TCURX | Rate types (M, EURX, P), inversion, triangulation via reference currency, decimals per currency | COVERED-12 | 12 | Rate types, triangulation, decimals from ISO 4217 |
| OB07 — Exchange rate types | TCURV | — | COVERED-12 | 12 | — |
| OBA1 — Accounts for exchange rate differences / FX valuation | T030H | Realized/unrealized gain-loss, valuation and translation accounts per G/L account | COVERED-12 | 12 | Posting profile for realized/unrealized |
| OB59 / OBBD — Valuation methods and valuation areas (FC valuation) | T044A, T044G, FAGL_VAL_AREA | Lowest value principle, always valuate, reset, exchange rate type per method; valuation area ↔ accounting principle | COVERED-12 | 12 | Revaluation method per ledger / accounting principle |
| OBXR / OBYR — Special G/L indicators (customers / vendors) | T074 | Down payments, bills of exchange, guarantees, noted items | PARTIAL-19 | 19 | Prepayments/down payments covered (waves 19/30); bills of exchange and noted items not named — see FI-AP/AR sections |
| OBY7 / OBY8 / OBY9 — Copy chart of accounts / company code / delete | — | — | COVERED-6 | 6 | Entity templates ("copy legal entity") in enterprise structure |
| OBYA — Clearing accounts for cross-company-code transactions | T001U | Due-to/due-from per company-code pair | COVERED-40 | 40 | Intercompany accounting due-to/due-from |
| OBYC (FI-side view) — Automatic postings (MM/SD integration) | T030 | Transaction keys BSX, GBB, WRX, PRD, KDM, UMB, FRL, BSV, AUM, VAX/VAY | COVERED-16 | 16 | Inventory posting profiles (item model group × site × account) |
| OB40 — Tax accounts | T030K | Per tax transaction key (MWS, VST, NAV…) | COVERED-13 | 13 | Tax posting profile |
| OBB8 / OBB9 — Terms of payment / installment terms | T052, T052R | Day limits, baseline date, discount days/percent, installment split | PARTIAL-7 | 7 | Payment terms covered; installment terms (split one invoice into several due dates) not named — add to wave 7 terms model |
| OBA3 / OBA4 — Tolerance groups (customers/vendors) / (employees, posting amount limits) | T043G, T043T | Max amount per document, per line, cash discount, payment difference tolerances | COVERED-19 | 19 | Amount limits via OPA policy + settlement tolerances (wave 19) |
| OBBE / OBXL — Reason codes for payment differences / accounts | T053R, T030 | Charge off, residual, disputes | COVERED-30 | 30 | Reason codes on cash application |
| OBD2 — Customer account groups / OBD3 — Vendor account groups | T077D, T077K | Number range, field status, one-time account | COVERED-7 | 7 | Party role groups, field attributes; one-time vendors/customers — see §13.2 |
| OBXU / OBXI — Cash discount received/granted accounts | T030 | Net/gross method | COVERED-19 | 19 | — |
| OBXK / OBXB — Bank charges / bank sub-accounts | T030 | — | COVERED-36 | 36 | Bank posting profiles |
| OBYP — GR/IR clearing accounts | T030 | WRX | COVERED-18 | 18 | — |
| OBXM / OBXO / OBXW — Retained earnings-related: acquisition/retirement, rounding, over/under payments | T030 | — | COVERED-19 | 19 | Rounding and payment difference accounts |
| OBA0 — Tolerance groups for G/L accounts (clearing) | T043S | — | PARTIAL-11 | 11 | GL OI gap |
| OB32 — Document change rules | TBAER | Which fields editable after posting | COVERED-11 | 11 | Field-level edit rules post-posting |
| OB46 — Interest indicators / OB82 / OBAA | T056* | Balance interest, item interest, calendar type | GAP | propose 30 | Interest engine (see F.52) |
| OBD5 / Define sort keys (allocation field) | TZUN | Assignment field fill rule | SUPERSEDED-11 | 11 | Sort/assignment via configurable reference fields and saved views |
| OBR1 / OBR2 / OBR3 — Reset transaction data / delete master data / delete customer-vendor | — | Test-client tooling | SUPERSEDED-1 | 1 | Environment reset through pipeline/tenant provisioning |
| FINSC_FIELDSTATUS / Define field status for ledger (S/4) | — | — | COVERED-3 | 3 | Metadata |
| OB74 — Additional rules for automatic clearing | TF123 | Criteria fields per account type/range | PARTIAL-11 | 11 | GL OI gap (AP/AR criteria covered by settlement rules) |
| OBB1 / OBB2 — Define accounts for bank interest / tax on cash discount | T030 | — | COVERED-13 | 13 | — |
| SNUM / SNRO (FI number range objects) | NRIV | RF_BELEG, etc. | COVERED-5 | 5 | Number sequences |
| OBBG — Assign country to tax calculation procedure | T005 | TAXUSJ, TAXUSX, TAXEUR… | COVERED-13 | 13 | Jurisdiction hierarchy per country |
| OBH1 — Copy number range intervals to fiscal year | NRIV | — | COVERED-5 | 5 | Year-dependent sequences |
| Define Reasons for Reversal (OB_REVERSE / T041C) | T041C | Negative posting allowed, alternative posting date | COVERED-11 | 11 | Reversal reason codes |
| FAGL_CL_RGRP / Define sort method & adjustment accounts for regrouping | T043/ T030 | Remaining term intervals | GAP | propose 14 | FAGLF101 gap |
| Define Posting Keys for Clearing (OBXH etc.) | T030 | — | SUPERSEDED-11 | 11 | No posting keys |
| OBD4 → "Define Screen Layout per Company Code" | T001 | — | COVERED-3 | 3 | — |
| Cost of sales accounting: functional areas (OKBD), activate (FAGL_FUNC_AREA) | TFKB, T001 | — | PARTIAL-11 | 11 | Functional area dimension + derivation rules (substitution gap) |
| Define Business Areas (OX03) / Consolidation business areas | TGSB, TGSBK | — | COVERED-6 | 6 | Dimension values |
| Profit center: standard hierarchy, dummy PC (KCH1/KE59 — see CO-PA) | CEPC, SETHEADER | — | COVERED-6 | 6 | Profit center as dimension with hierarchy purposes |

---

## 13.2 FI-AP — Accounts Payable

SAP context: vendor master (LFA1/LFB1/LFM1, replaced by Business Partner with FLVN00/FLVN01 roles in S/4), invoices and credit memos (FB60/FB65 or MIRO for PO-based), open items (BSIK/BSAK), payment program F110, dunning (rare for AP), down payments, withholding tax. Passage maps vendor master to wave 7 (party model), non-PO invoices to wave 18 (capture) and open items/payments to wave 19.

### 13.2.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| XK01 / XK02 / XK03 (→ BP) | Vendor master create/change/display centrally | General, company-code and purchasing data | COVERED-7 | 7 | Party + Vendor role + per-legal-entity and per-purchasing-org data |
| FK01 / FK02 / FK03 | Vendor master (accounting) | Company-code segment only | COVERED-7 | 7 | Vendor role, legal-entity scope |
| MK01 / MK02 / MK03 | Vendor master (purchasing) | Purchasing-org segment | COVERED-7 | 7 | Vendor role, purchasing scope; see MM-PUR |
| XK05 / XK06 / FK05 / FK06 | Block / mark for deletion | Posting/payment/purchasing blocks | COVERED-7 | 7 | Role status and block flags with approval |
| XK07 | Change account group | Reassign vendor group | COVERED-7 | 7 | Group change under governance workflow |
| XK99 / MASS (LFA1) | Mass maintenance vendor | Mass change | COVERED-5 | 5 | Grid mass edit |
| BP (role FLVN00/FLVN01) | Business partner — vendor roles (S/4) | Unified BP | COVERED-7 | 7 | Native model |
| CVI_FS_CHECK_CUSTOMIZING / MDS_LOAD_COCKPIT | Customer-vendor integration synchronization | ECC → S/4 BP sync | SUPERSEDED-7 | 7 | One party model, no sync |
| One-time vendor (account group CPD) | One-time accounts with address on document | Ad-hoc payee | PARTIAL-7 | 7 | Not named in wave 7; add "one-time party" role with document-level address capture |
| FK08 / FK09 | Confirm vendor master changes (dual control) | Sensitive-field dual control | COVERED-7 | 7 | Change approval workflow on master data (bank details, payment terms) |
| FB60 | Enter vendor invoice (non-PO) | Single-screen AP invoice | COVERED-18 | 18 | Manual invoice capture without PO |
| FB65 | Enter vendor credit memo | — | COVERED-19 | 19 | Vendor credit notes |
| F-43 / F-41 | Enter vendor invoice / credit memo (classic) | Posting-key based | SUPERSEDED-18 | 18 | Single invoice form |
| FV60 / FV65 | Park vendor invoice / credit memo | Draft for approval | COVERED-18 | 18 | Invoice draft + approval workflow |
| FBV0 | Post parked document | — | COVERED-18 | 18 | — |
| F-48 | Vendor down payment | Special G/L down payment posting | COVERED-19 | 19 | Prepayments |
| F-47 | Down payment request | Noted item for F110 | COVERED-19 | 19 | Prepayment request on PO/invoice workflow |
| F-54 | Clear vendor down payment | Transfer down payment against invoice | COVERED-19 | 19 | Prepayment application on settlement |
| F-53 / F-58 | Post outgoing payment / with printed form | Manual payment with clearing | COVERED-19 | 19 | Manual payment + settlement; check print |
| F-44 | Clear vendor | Account clearing without payment | COVERED-19 | 19 | Settlement without payment (credit vs invoice) |
| F-51 | Post with clearing (transfer) | Transfer with clearing | COVERED-19 | 19 | — |
| F-40 / F-36 (AP side: FBW6) | Bill of exchange payment (AP) | Bills of exchange payable | GAP | propose 36 | Bills of exchange (AP and AR) are not in any wave; relevant in IT/ES/FR/JP/BR; propose as a wave 36 (bank) option or defer to a localization v1.x pack under wave 48 — needs a decision |
| FB10 | Invoice/credit memo fast entry | Multi-document fast entry | COVERED-18 | 18 | Grid entry / import |
| FBL1N (as document finder) | — | — | COVERED-19 | 19 | — |
| FCHN / FCH5 / FCH6 | Check register / create manual check / change check info | Check management | COVERED-36 | 36 | Check management (void/reprint), check register |
| FCH7 / FCH8 / FCH9 | Reprint / cancel payment + check / void check | — | COVERED-36 | 36 | — |
| FCHR / FCHT | Online cashed checks / assign checks to payments | — | COVERED-36 | 36 | Bank statement cashed-check matching |
| FCHU / FCHX | Create reference for check / check extract (positive pay) | Positive pay file | COVERED-19 | 19 | Positive pay export |
| F110 (document side: payment documents) | — | — | COVERED-19 | 19 | — |
| FBZ5 | Print check for payment document | — | COVERED-19 | 19 | Check printing |
| FB03 (vendor doc) | Display document | — | COVERED-19 | 19 | — |
| FBD1 (vendor recurring) | Recurring vendor invoice | — | COVERED-11 | 11 | Recurring journal with vendor lines |
| J1INCHLN / J1INBANK / J1INCERT (IN WHT) | Withholding tax challan / bank challan / certificate (India) | Country-specific WHT remittance | PARTIAL-13 | 13 | Withholding engine + 26AS hooks named; Indian TDS challan/certificate flows belong to the IN country pack (wave 48) — partial until pack ships |
| S_P00_07000134 / RFIDYYWT | Generic withholding tax reporting (1099, DME) | Statutory WHT reporting | COVERED-13 | 13 | 1099 reporting hooks (year-end forms in wave 43 share structures) |
| FIBLFFP / F111 | Payment request / payment program for payment requests | Treasury payment requests outside AP | PARTIAL-36 | 36 | Payment requests from treasury (not vendor items) not named; add to wave 36 in-house banking |
| VIM (OpenText) / Fiori: Manage Supplier Invoices / Upload Supplier Invoices | AP automation | OCR capture, approval | COVERED-18 | 18 | OCR/AP automation adapter |
| Ariba Network invoice | Supplier-submitted invoice | — | COVERED-20 | 20 | Supplier portal invoice submission |

### 13.2.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| FBL1N / FBL1H | Vendor line items / line item browser | COVERED-19 | 19 | Vendor open/cleared item inquiry |
| FK10N / FK10NA | Vendor balance display | COVERED-19 | 19 | Vendor balance with drill |
| S_ALR_87012077 | Vendor information system | COVERED-46 | 46 | Vendor analytics |
| S_ALR_87012078 | Due date analysis for open items | COVERED-19 | 19 | Aging |
| S_ALR_87012082 | Vendor balances in local currency | COVERED-19 | 19 | — |
| S_ALR_87012083 | List of vendor open items for printing | COVERED-19 | 19 | — |
| S_ALR_87012084 | Open items — vendor due date forecast | COVERED-36 | 36 | Cash forecast input |
| S_ALR_87012085 | Vendor payment history | COVERED-19 | 19 | — |
| S_ALR_87012086 | Vendor list | COVERED-7 | 7 | — |
| S_ALR_87012087 | Address list | COVERED-7 | 7 | — |
| S_ALR_87012089 | Display changes to vendors | COVERED-5 | 5 | Audit viewer |
| S_ALR_87012090 | Display/confirm critical vendor changes | COVERED-7 | 7 | Dual-control queue |
| S_ALR_87012093 | Vendor business (purchasing volume) | COVERED-46 | 46 | — |
| S_ALR_87012103 | List of vendor line items | COVERED-19 | 19 | — |
| S_ALR_87012104 | Vendor open items (OI list) | COVERED-19 | 19 | — |
| S_ALR_87012105 | List of down payments open at key date | COVERED-19 | 19 | Prepayment report |
| S_P99_41000099 | Payment list (F110) | COVERED-19 | 19 | Payment run output |
| S_ALR_87012119 | Cashed checks per bank account | COVERED-36 | 36 | — |
| S_P99_41000101 | Check register | COVERED-36 | 36 | — |
| S_ALR_87012176 (AP aging via RFKOPR00) | Vendor open item aging | COVERED-19 | 19 | — |
| S_ALR_87012116 | Bill of exchange list | GAP | propose 36 | Follows bills gap |
| FBL1N variant: withholding tax | Withholding tax line items | COVERED-13 | 13 | — |
| S_ALR_87012111 / S_P00_07000134 | Withholding tax report / 1099-MISC | COVERED-13 | 13 | — |
| Fiori: Supplier Balances / Display Supplier List / Manage Supplier Line Items | S/4 apps | COVERED-19 | 19 | — |
| Fiori: Accounts Payable Overview (analytical) | AP KPI page | COVERED-46 | 46 | Embedded dashboards |
| Fiori: Cash Discount Utilization / Days Payable Outstanding | AP KPIs | COVERED-46 | 46 | Governed metrics |

### 13.2.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| F110 | Automatic payment program (proposal, edit, schedule, print) | COVERED-19 | 19 | Payment proposal → approval → run → media; idempotent |
| F110S | Payment program scheduled (batch variant) | COVERED-19 | 19 | Scheduled via wave 4 jobs |
| F111 | Payment program for payment requests | PARTIAL-36 | 36 | See payment requests |
| FBPM / FBPM1 | Payment medium workbench (PMW) run / DME engine | COVERED-19 | 19 | ISO 20022 pain.001, NACHA, check files |
| DMEE / DMEE1 | DME engine format tree maintenance | SUPERSEDED-19 | 19 | Format adapters (ISO 20022, NACHA) + wave 48 electronic reporting designer for new formats |
| FDTA | DME administration (download files) | COVERED-19 | 19 | Payment file vault with audit (keys never leave Vault — wave 36) |
| SAPF110V (F110 proposal log) | Payment proposal log | COVERED-19 | 19 | — |
| FBZ0 | Edit payment proposal (display/change) | COVERED-19 | 19 | — |
| F150 (AP dunning) | Dunning vendors (debit balances) | PARTIAL-30 | 30 | Dunning covers customers; dunning vendors with debit balances is an edge case — reuse engine |
| FAGL_FC_VAL (AP) | FX valuation of vendor open items | COVERED-12 | 12 | — |
| FAGLF101 (AP) | Regroup vendors with debit balances | GAP | propose 14 | Reclassification gap |
| F.98 | Vendor balance confirmation | GAP | propose 19 | Balance confirmations gap |
| MRRL (ERS — see MM-IV) | Evaluated receipt settlement | COVERED-18 | 18 | — |
| J1INCHLN run / withholding tax remittance | WHT payment to authority | COVERED-13 | 13 | Tax settlement for withholding |
| S_P00_07000134 (year-end 1099 run) | 1099 generation | COVERED-13 | 13 | — |
| F.07 (AP) | Carry forward vendor balances | COVERED-14 | 14 | — |
| FBWE (AP bill of exchange presentation) | Bill of exchange presentation | GAP | propose 36 | Bills gap |
| Fiori: Schedule Accounts Payable Jobs | Job templates (payment proposal, correspondence) | COVERED-4 | 4 | Job framework |
| Fiori: Manage Automatic Payments | S/4 payment run app | COVERED-19 | 19 | — |
| FPAYM / FBPM payment advice | Payment advice generation (REMADV) | COVERED-19 | 19 | Vendor payment advice email/portal |
| F.64 (AP correspondence) | Correspondence: maintain / print | PARTIAL-19 | 19 | Correspondence types (account statement, payment notice) only partially named (payment advice) — define correspondence framework (templates + triggers) on wave 46 document designer |

### 13.2.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OBD3 — Vendor account groups / field status | T077K | Number range, one-time, field status | COVERED-7 | 7 | — |
| XKN1 / OBAS — Number ranges for vendor accounts / assign | NRIV, T077K | Internal/external | COVERED-5 | 5 | — |
| OBB8 — Terms of payment (vendors) | T052 | — | COVERED-7 | 7 | — |
| FBZP — Payment program configuration: all company codes, paying company codes, payment methods per country, per company code, bank determination (ranking, bank accounts, available amounts, value date), house banks | T042, T042A, T042B, T042D, T042E, T042I, T042Z, T042V | Payment method classification (check/transfer/bill), min/max amounts, foreign payments allowed, form/DME program, optimization by bank group/postal code, bank selection ranking | COVERED-19 | 19 | Payment methods, bank ranking and limits per legal entity; house banks in wave 36 |
| FI12 / FI13 — House banks and bank accounts | T012, T012K | Bank key, account id, G/L account, currency | COVERED-36 | 36 | Bank account master (BAM) |
| OBPM1 / OBPM2 / OBPM3 / OBPM4 — Payment medium formats / note-to-payee / payment method / selection variants | TFPM*, T042Z* | — | COVERED-19 | 19 | Format adapters |
| OBYR — Special G/L indicators vendors (down payments, guarantees, BoE) | T074 | Reconciliation accounts per SGL | PARTIAL-19 | 19 | Down payments yes; BoE no |
| OBXT / OBA3 — Tolerances for vendors / payment differences | T043G | Residual, charge-off limits | COVERED-19 | 19 | Settlement tolerances |
| OBA4 — Tolerance groups for employees (posting limits) | T043T | Max amount per doc/line/discount | COVERED-2 | 2 | OPA policy on amounts |
| Define withholding tax types / codes / assign to company code (extended WHT) | T059P, T059Z, T001WT | Invoice/payment time, base, rates, exemptions, certificates, accumulation | COVERED-13 | 13 | Withholding engine |
| Classic withholding tax (OBCS / T059Q) | T059Q | — | SUPERSEDED-13 | 13 | Extended model only |
| OBZT — Tax codes allowed in FB60/FB70 | T007V | Per transaction | COVERED-13 | 13 | Allowed tax codes per document type |
| OBCL — Default tax code for non-taxable transactions | T001 | — | COVERED-13 | 13 | — |
| OB71 — Define reason codes for payment block / OB27 | T008 | Block for payment, manual/automatic | COVERED-19 | 19 | Payment holds |
| OBB9 — Installment terms | T052R | — | PARTIAL-7 | 7 | Installment gap |
| Define alternative payee / permitted payee (BP role) | LFA1-LNRZA, LFZA | — | COVERED-7 | 7 | Alternate payee on vendor role |
| Define correspondence types / forms (OB77, OB78, OB79) | T048, T048B | — | PARTIAL-19 | 19 | Correspondence framework gap |
| Define payment advice forms (SAPscript/PDF) | — | — | COVERED-19 | 19 | Payment advice templates; wave 46 designer |
| Check lots (FCHI) / check number ranges | PCEC | Lot per house bank/account | COVERED-36 | 36 | Check lots |
| OBYP / OBA7 (vendor doc types KR, KG, KZ, KA) | T003 | — | COVERED-11 | 11 | Document types |
| Define sensitive fields for dual control (S_ALR_87003378 / T055F) | T055F | Fields requiring confirmation | COVERED-7 | 7 | Change approval on sensitive fields |
| OBXS — Down payment request accounts | T030 | — | COVERED-19 | 19 | — |
| Define vendor activity groups (no) — Define payment block reasons | T008 | — | COVERED-19 | 19 | — |
| Evaluated receipt settlement defaults (vendor master ERS flag, OMRR) | LFM1 | — | COVERED-18 | 18 | — |
| OBXF — Posting keys for AP (OBXK group) | TBSL | — | SUPERSEDED-11 | 11 | — |
| OBD3 "one-time account" | T077K | — | PARTIAL-7 | 7 | One-time party gap |
| Define intercompany vendor/customer clearing (OBYA) | T001U | — | COVERED-40 | 40 | — |
| Bank-to-bank transfers (payment request types, FIBL) | — | — | PARTIAL-36 | 36 | Payment requests gap |

---

## 13.3 FI-AR — Accounts Receivable

SAP context: customer master (KNA1/KNB1/KNVV → BP roles FLCU00/FLCU01), customer invoices via SD billing (VF01 → RV document) or FB70, incoming payments (F-28), lockbox (FLB2), dunning (F150), correspondence, interest calculation, credit management (classic FD32 or FSCM-CR). Passage: customer master wave 7, billing wave 29, AR/cash application/collections wave 30, credit wave 27.

### 13.3.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| XD01 / XD02 / XD03 (→ BP) | Customer master centrally | General, company-code, sales-area data | COVERED-7 | 7 | Party + Customer role + sales-scope data |
| FD01 / FD02 / FD03 | Customer master (accounting) | Company-code segment | COVERED-7 | 7 | — |
| VD01 / VD02 / VD03 | Customer master (sales) | Sales-area segment | COVERED-7 | 7 | Sales-scope role data (see SD) |
| XD05 / XD06 / FD05 / FD06 / VD05 / VD06 | Block / flag for deletion | — | COVERED-7 | 7 | — |
| XD07 | Change customer account group | — | COVERED-7 | 7 | — |
| XD99 / MASS (KNA1) | Mass maintenance customers | — | COVERED-5 | 5 | — |
| FD08 / FD09 | Confirm critical customer changes | Dual control | COVERED-7 | 7 | — |
| FD32 → UKM_BP / UKM_MY_DCDS | Customer credit management master | Credit limit, risk category, blocks | COVERED-27 | 27 | Credit limits, aging-based rules, risk scoring (wave 30) |
| FD24 / FD33 | Credit management changes / display | — | COVERED-27 | 27 | — |
| FB70 | Enter customer invoice (FI, non-SD) | Direct AR invoice | COVERED-29 | 29 | Free-text invoice |
| FB75 | Enter customer credit memo | — | COVERED-29 | 29 | Credit notes |
| F-22 / F-27 | Customer invoice / credit memo (classic) | — | SUPERSEDED-29 | 29 | Single invoice form |
| FV70 / FV75 | Park customer invoice / credit memo | — | COVERED-29 | 29 | Draft + approval |
| F-28 | Post incoming payment | Manual cash application with clearing | COVERED-30 | 30 | Manual cash application |
| F-26 | Incoming payment fast entry | — | COVERED-30 | 30 | — |
| F-29 | Customer down payment | — | COVERED-30 | 30 | Customer prepayments (deposit on order) |
| F-37 | Customer down payment request | — | COVERED-27 | 27 | Deposit request on order |
| F-39 | Clear customer down payment | — | COVERED-30 | 30 | — |
| F-30 / F-32 | Post with clearing / clear customer | — | COVERED-30 | 30 | Settlement without payment |
| F-34 / F-36 / F-33 | Bill of exchange payment / discount (AR) | — | GAP | propose 36 | Bills of exchange gap |
| FBE1 / FBE2 / FBE3 | Payment advice create/change/display | Remittance advices for matching | COVERED-30 | 30 | Remittance advice ingestion and matching |
| FBE6 | Delete payment advices | — | COVERED-30 | 30 | — |
| FLB1 / FLB2 | Postprocess lockbox / import lockbox file (BAI2) | Lockbox | COVERED-30 | 30 | Lockbox BAI2 |
| FEBA_LOCKBOX | Postprocess lockbox (New) | — | COVERED-30 | 30 | — |
| F-21 / F-30 | Transfer posting (customer) | — | COVERED-30 | 30 | — |
| FDM1 / UDM_DISPUTE | Create dispute case (FSCM-DM) | — | COVERED-30 | 30 | Disputes workflow |
| F.27 (document side) | Account statement (periodic correspondence) | — | COVERED-30 | 30 | Customer statements |
| FBL5N (document finder) | — | — | COVERED-30 | 30 | — |
| FB12 / F.61 / F.64 | Request / print correspondence | Correspondence requests | PARTIAL-30 | 30 | Statements and dunning named; general correspondence framework (payment notices, individual letters, open item lists) only partial |
| F.2A / F.2B | Interest calculation (customer items) run | — | COVERED-30 | 30 | Interest notes |
| VKM1 / VKM4 / VKM3 | Release blocked SD documents (credit) | Credit release | COVERED-27 | 27 | Credit hold release workflow |
| BP (FLCU00/FLCU01) | Customer roles in BP (S/4) | — | COVERED-7 | 7 | — |
| Fiori: Manage Customer Line Items / Post Incoming Payments / Reprocess Bank Statement Items | S/4 AR apps | COVERED-30 | 30 | — |
| Biller Direct (FSCM-BD) | Customer self-service for invoices/payments | — | SUPERSEDED-30 | 30 | Customer portal with payment adapter |

### 13.3.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| FBL5N / FBL5H | Customer line items / browser | COVERED-30 | 30 | — |
| FD10N / FD10NA | Customer balance display | COVERED-30 | 30 | — |
| FD11 | Customer account analysis | COVERED-30 | 30 | — |
| S_ALR_87012167 | Accounts receivable information system | COVERED-46 | 46 | AR analytics |
| S_ALR_87012168 | Due date analysis for open items | COVERED-30 | 30 | Aging |
| S_ALR_87012169 / 87012170 | Transaction figures: account balance / special sales | COVERED-30 | 30 | — |
| S_ALR_87012172 | Customer balances in local currency | COVERED-30 | 30 | — |
| S_ALR_87012173 | List of customer open items for printing | COVERED-30 | 30 | — |
| S_ALR_87012174 | List of customer open items | COVERED-30 | 30 | — |
| S_ALR_87012175 | Open items — customer due date forecast | COVERED-36 | 36 | Cash forecast |
| S_ALR_87012176 | Customer evaluation with OI sorted list (aging) | COVERED-30 | 30 | — |
| S_ALR_87012177 | Customer payment history | COVERED-30 | 30 | Payment behaviour feeds risk scoring |
| S_ALR_87012178 | Customer open item analysis by balance of overdue items | COVERED-30 | 30 | — |
| S_ALR_87012179 | Customer list | COVERED-7 | 7 | — |
| S_ALR_87012180 | Address list | COVERED-7 | 7 | — |
| S_ALR_87012182 | Display changes to customers | COVERED-5 | 5 | — |
| S_ALR_87012183 | Display/confirm critical customer changes | COVERED-7 | 7 | — |
| S_ALR_87012186 | Customer sales | COVERED-46 | 46 | — |
| S_ALR_87012197 | List of customer line items | COVERED-30 | 30 | — |
| S_ALR_87012198 | List of down payments open on key date | COVERED-30 | 30 | — |
| S_ALR_87012199 | Customer: bill of exchange list | GAP | propose 36 | Bills gap |
| S_ALR_87012215 | Customer sales per period (RFDUML00) | COVERED-46 | 46 | — |
| F.21 / S_ALR_87012218 | Interest scale / interest calculation list | COVERED-30 | 30 | — |
| F.31 | Credit management overview | COVERED-27 | 27 | Credit exposure |
| F.32 / FDK43 | Credit management: missing data / master data list | COVERED-27 | 27 | — |
| FCV3 / S_ALR_87012215 (credit) | Credit overview (SD) | COVERED-27 | 27 | — |
| UKM_COMMITMENTS / UKM_LOGS_DISPLAY | FSCM credit exposure / logs | COVERED-27 | 27 | — |
| UDM_SUPERVISOR / UDM_SPECIALIST | Collections worklists (FSCM-COL) | COVERED-30 | 30 | Collections workspace |
| UDM_DISPUTE (list) | Dispute case list | COVERED-30 | 30 | — |
| S_ALR_87012085 (customer) / F.23 | Dunning history | COVERED-30 | 30 | — |
| FBMP (display) | Dunning procedure list | COVERED-30 | 30 | — |
| Fiori: Days Sales Outstanding / Overdue Receivables / Future Receivables / Cash Collection Tracker | AR KPIs | COVERED-46 | 46 | — |
| Fiori: Process Receivables | Collections work center | COVERED-30 | 30 | — |

### 13.3.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| F150 | Dunning run (parameters, schedule, print) | COVERED-30 | 30 | Dunning levels, grace days, blocks; letters only for expected customers |
| F.24 / F.26 / F.2A / F.2B | Interest calculation (items / balances) | COVERED-30 | 30 | Interest notes |
| F.27 | Periodic account statements | COVERED-30 | 30 | Customer statements |
| FLB2 / FEBA_LOCKBOX | Lockbox import and postprocessing | COVERED-30 | 30 | — |
| FF_5 / FEBAN (AR side) | Bank statement → customer clearing | COVERED-30 | 30 | camt.053 auto-match for AR (wave 36 for the bank side) |
| F.13 (AR) | Automatic clearing customers | COVERED-30 | 30 | Rule-based auto-match |
| FAGL_FC_VAL (AR) | FX valuation of customer open items | COVERED-12 | 12 | — |
| FAGLF101 (AR) | Regroup customers with credit balances | GAP | propose 14 | Reclassification gap |
| F.99 | Customer balance confirmation | GAP | propose 30 | Balance confirmation gap |
| F.07 (AR) | Carry forward | COVERED-14 | 14 | — |
| UKM_MALUS_DSP / UKM_MASS / UKM_CREDIT_LIMIT_MASS | Mass credit limit recalculation / scoring run | COVERED-30 | 30 | Risk scoring batch |
| F.28 / FD24 mass | Reset credit limit data / recalculate | COVERED-27 | 27 | — |
| UDM_BP_PROF / UDM_GENWL | Generate collections worklist (FSCM-COL) | COVERED-30 | 30 | Worklist generation |
| F110 (customer direct debits) | Direct debit run (SEPA SDD) | COVERED-30 | 30 | Customer direct debit via payment adapter / pain.008 — confirm pain.008 is in wave 19 formats (wave 19 lists pain.001 only) |
| FDTA (DD) | DME admin for direct debits | COVERED-19 | 19 | — |
| SEPA mandate management (FSEPA_M1..M4) | Mandates | PARTIAL-30 | 30 | SEPA direct-debit mandates not named; required for EU customers paying by direct debit — add mandate entity to wave 30 |
| VF44 / VF45 (see SD) | Revenue recognition run (classic) | COVERED-29 | 29 | — |
| FBWD / FBWE (AR BoE) | Bill of exchange presentation | GAP | propose 36 | Bills gap |
| Fiori: Schedule Accounts Receivable Jobs | Job templates (dunning, statements, interest) | COVERED-4 | 4 | — |

### 13.3.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OBD2 — Customer account groups / field status | T077D | — | COVERED-7 | 7 | — |
| XDN1 / OBAR — Number ranges customers / assign | NRIV, T077D | — | COVERED-5 | 5 | — |
| OBB8 — Terms of payment (customers) | T052 | — | COVERED-7 | 7 | — |
| OBXR — Special G/L customers (down payments, security deposits, BoE) | T074 | — | PARTIAL-30 | 30 | BoE gap |
| OBA3 — Customer tolerances | T043G | — | COVERED-30 | 30 | — |
| FBMP — Dunning procedures (levels, intervals, charges, min amounts, grace days, forms) | T047*, T047A..T047M | Dunning levels 1–9, dunning charges by amount, interest indicator, print parameters, dunning by dunning area | COVERED-30 | 30 | Dunning levels + letters |
| OBL6 / OB61 — Dunning areas / block reasons | T047M, T040S | — | COVERED-30 | 30 | — |
| OB46 / OB82 / OB83 — Interest indicators, calculation types, reference rates | T056*, T056P, T056R | Item/balance interest, calendar, rounding | COVERED-30 | 30 | AR interest; GL interest is a gap |
| OB45 — Credit control areas (classic) | T014 | Credit limit currency, update group, risk categories | SUPERSEDED-27 | 27 | FSCM-style credit segments modeled as credit profiles per legal entity/group |
| OVA8 — Automatic credit control (SD) | T691F | Static/dynamic check, horizon, open orders/deliveries, reactions | COVERED-27 | 27 | Credit rules (limit, aging, dynamic horizon) |
| OB01 — Risk categories / OVFL credit groups | T691A, T691B | — | COVERED-27 | 27 | — |
| UKM_* FSCM credit customizing (credit segments, rules, scoring formulas, BRF+ rules) | UKMCRED_SGM*, UKM_* | Score formulas, limit rules, check rules per segment | COVERED-27 | 27 | Credit profiles + scoring (wave 30) |
| UDM_* collections customizing (collection strategies, segments, profiles, priorities, worklist rules) | UDM_* | Strategy rules (BRF), priorities, groups | COVERED-30 | 30 | Collection strategies |
| UDM_* dispute case customizing (case types, attributes, status, reasons, RMS) | SCMG_* | — | COVERED-30 | 30 | Disputes |
| OBB9 — Installment terms (customers) | T052R | — | PARTIAL-7 | 7 | Installment gap |
| OB10 / Lockbox control (OBAY, OBAX) | T049A, T049B, T049L | Lockbox destination, BAI format, posting rules | COVERED-30 | 30 | — |
| OBXL / OBBE — Payment difference reason codes and accounts | T053R, T030 | — | COVERED-30 | 30 | — |
| Define payment advice types / notes to payee (OBBE) | T053 | — | COVERED-30 | 30 | — |
| Define correspondence types (OB77) / periodic account statements | T048 | — | PARTIAL-30 | 30 | Correspondence framework gap |
| SEPA mandate customizing | SEPA_* | — | PARTIAL-30 | 30 | Mandate gap |
| Define alternate payer / head office–branch | KNA1-KNRZE, KNB1 | Head office/branch accounting | PARTIAL-7 | 7 | Head-office/branch (invoice to branch, pay from HQ) not named in wave 7 party roles — add "bill-to/pay-from parent" relationship |
| OBYR (customer side) sales/FI account determination — see VKOA | T030, C001.. | — | COVERED-29 | 29 | Revenue account determination |
| Define reason codes for credit holds (OVA8/ OVAK) | T691* | — | COVERED-27 | 27 | — |
| OB38 — Assign credit control area to company code | T001 | — | SUPERSEDED-27 | 27 | — |
| Define cash discount base, tax on discount (OBY6) | T001 | — | COVERED-13 | 13 | — |
| Define dunning forms (SAPscript F150_DUNN_01) | — | — | COVERED-30 | 30 | Letter templates; wave 46 designer |

---


## 13.4 FI-AA — Asset Accounting

SAP context: asset classes, charts of depreciation with depreciation areas (book, tax, group, IFRS), asset master (ANLA/ANLB/ANLC), transactions (acquisition ABZON/F-90, retirement ABAVN/F-92, transfer ABUMN, post-capitalization ABNAN, write-up ABZU, manual depreciation ABMA, unplanned ABAA), depreciation run AFAB (S/4: FAA_DEPRECIATION_POST), year-end AJAB/AJRW (S/4: FAA_CMP), AuC settlement AIAB/AIBU, asset history sheet. Passage: wave 37 with books ≈ depreciation areas and posting to the universal journal per ledger group.

### 13.4.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| AS01 / AS02 / AS03 | Asset master create/change/display | Asset with depreciation areas, time-dependent data, origin | COVERED-37 | 37 | Asset master with books |
| AS11 / AS12 / AS13 | Asset sub-number create/change/display | Component sub-assets | COVERED-37 | 37 | Sub-assets / components |
| AS21 / AS22 / AS23 | Group asset create/change/display | Group depreciation (US tax) | PARTIAL-37 | 37 | Group assets (depreciate at group level, e.g. ADR) not named in wave 37 — add to tax book features |
| AS05 / AS06 / AS25 / AS26 | Block / delete asset / block group asset | — | COVERED-37 | 37 | — |
| AS91 / AS92 / AS93 / AS94 | Legacy asset data transfer (create/change/display old asset, sub-number) | Opening NBVs | COVERED-49 | 49 | Fixed-asset migration pack; wave 37 demo migration |
| AS100 | Legacy data transfer via Excel | — | COVERED-10 | 10 | Migration framework + Excel import |
| ABZON / F-90 | Acquisition with automatic offsetting entry / from vendor (AP) | Acquisition | COVERED-37 | 37 | Acquisition from AP invoice |
| ABZP / ABZE | Acquisition from affiliated company / in-house production | — | COVERED-37 | 37 | From intercompany (wave 40) and from production/project |
| ABSO | Miscellaneous transactions (transaction type driven) | Generic asset posting | COVERED-37 | 37 | Transaction type model |
| ABUMN / ABUML | Transfer within company code / to another asset class | Intra-company transfer | COVERED-37 | 37 | Transfer |
| ABT1N | Intercompany asset transfer | Cross-entity transfer with NBV/gross methods | COVERED-40 | 40 | Intercompany asset transfer via wave 40 IC + wave 37 |
| ABAVN / F-92 / ABAON | Retirement by scrapping / with customer (sale) / sale without customer | Disposals | COVERED-37 | 37 | Disposal (sale/scrap) with gain/loss |
| ABNAN | Post-capitalization | Capitalize in a later year | COVERED-37 | 37 | Adjustment transactions |
| ABZU | Write-up | Reverse excess depreciation | COVERED-37 | 37 | — |
| ABAA | Unplanned depreciation | Impairment-like manual posting | COVERED-37 | 37 | Impairment |
| ABMA | Manual depreciation | — | COVERED-37 | 37 | — |
| ABMR | Manual transfer of reserves | Special reserves (German HGB) | OUT-OF-SCOPE | — | Special-reserve accounting (Sonderposten) is a DE localization topic; revisit with the DE country pack |
| ABAW | Balance sheet revaluation | Revaluation (IAS 16) | COVERED-37 | 37 | Revaluation transaction |
| ABGF / ABGL | Credit memo in invoice year / in following year | Acquisition credit | COVERED-37 | 37 | — |
| AB08 | Reverse asset document | — | COVERED-37 | 37 | Reversal |
| AIAB / AIBU / AIST | AuC settlement rule / settlement / reverse | Assets under construction | COVERED-37 | 37 | CIP / under-construction from projects (wave 41 feeds) |
| OASV | Post balances to asset accounts (data transfer) | Data transfer reconciliation | COVERED-49 | 49 | Migration reconciliation |
| AW01N | Asset Explorer | Values, transactions, parameters per area | COVERED-37 | 37 | Asset inquiry with per-book values |
| ASKB / ASKBN | Periodic APC posting (non-leading areas) | Post values of areas that don't post real-time | COVERED-37 | 37 | Multi-book posting to ledger groups |
| ABST / ABST2 | Reconciliation FI-AA vs. G/L | — | COVERED-37 | 37 | Asset subledger reconciles to GL automatically |
| OAMK | Change reconciliation accounts (asset) | Setup | COVERED-37 | 37 | — |
| AS08 | Asset number ranges | — | COVERED-5 | 5 | — |
| Fiori: Manage Fixed Assets / Post Acquisition / Post Retirement / Transfer Asset | S/4 apps | COVERED-37 | 37 | — |
| RE-FX: RECN / RECECN (lease contracts, IFRS 16 valuation) | Contract and lease management | Lease accounting | COVERED-37 | 37 | IFRS 16 / ASC 842 schedules, ROU asset & liability |
| RE-FX: REISCN / RERAPP / RERAPL | Lease contract lists / periodic posting / planned-record list | — | COVERED-37 | 37 | Lease schedules and postings |
| RE-FX real estate objects (business entity, building, rental object, rental contracts) | Real estate management | Property management | OUT-OF-SCOPE | — | Real-estate management (tenancy, service charge settlement) is beyond the manufacturer scope; only the lease-accounting part is in wave 37 |
| Asset barcode inventory (AIS / third-party) | Physical asset inventory | — | COVERED-37 | 37 | Barcode physical inventory with reconciliation |
| Insurance values (asset master tab, S_ALR_87011951) | Insurance values / indexing | — | PARTIAL-37 | 37 | Insurance values and index series not named — add to asset master |

### 13.4.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| S_ALR_87011990 | Asset history sheet | COVERED-37 | 37 | Asset roll-forward by class/book |
| S_ALR_87011963 / 87011964 / 87011965 / 87011966 | Asset balances by asset number / class / business area / cost center | COVERED-37 | 37 | — |
| S_ALR_87011967 / 87011968 | Asset balances by plant / location | COVERED-37 | 37 | — |
| S_ALR_87011979 / 87011981 | Physical inventory list / by location | COVERED-37 | 37 | — |
| S_ALR_87011994 | Asset balances (summary) | COVERED-37 | 37 | — |
| S_ALR_87012004 / 87012005 | Total depreciation / depreciation by class | COVERED-37 | 37 | — |
| S_ALR_87012006 | Ordinary depreciation | COVERED-37 | 37 | — |
| S_ALR_87012007 / 87012008 | Special / unplanned depreciation | COVERED-37 | 37 | — |
| S_ALR_87012013 / 87012018 | Depreciation comparison / depreciation simulation | COVERED-37 | 37 | Simulation on planned assets |
| S_ALR_87012026 | Depreciation current year | COVERED-37 | 37 | — |
| S_ALR_87012028 | Asset transactions | COVERED-37 | 37 | — |
| S_ALR_87012033 | Asset acquisitions | COVERED-37 | 37 | — |
| S_ALR_87012037 | Changes to asset master records | COVERED-5 | 5 | — |
| S_ALR_87012039 | Asset transactions (intracompany transfers) | COVERED-37 | 37 | — |
| S_ALR_87012041 / 87012043 | Asset portfolio / asset list | COVERED-37 | 37 | — |
| S_ALR_87012048 | Asset retirements | COVERED-37 | 37 | — |
| S_ALR_87012050 / 87012052 | Asset acquisitions / retirements (gain-loss) | COVERED-37 | 37 | — |
| S_ALR_87012054 | Intracompany transfers | COVERED-37 | 37 | — |
| S_ALR_87012056 | Directory of unposted assets | COVERED-37 | 37 | — |
| S_ALR_87012058 | List of origins of asset debits | COVERED-37 | 37 | Acquisition source trace (AP, project, inventory) |
| S_ALR_87012060 | List of origins by cost elements | COVERED-37 | 37 | — |
| S_ALR_87012063 | Asset list (inventory) | COVERED-37 | 37 | — |
| S_ALR_87012075 | Leased assets (classic leasing) | COVERED-37 | 37 | Lease schedules |
| S_ALR_87012936 / 87012937 | Depreciation on capitalized assets (simulation) / planned depreciation | COVERED-37 | 37 | — |
| S_P99_41000192 | Posted depreciation by cost center | COVERED-37 | 37 | Dimension-level depreciation |
| S_ALR_87010127 / 87010129 (US: form 4562 / mid-quarter) | Tax depreciation reports | PARTIAL-37 | 37 | MACRS tables yes; IRS Form 4562 layout belongs to the US pack (wave 48) |
| AR01 / AR02 / AR03 / AR11 | Asset balances / history sheet / depreciation list / mid-year listing (ad hoc) | COVERED-37 | 37 | — |
| AR31 / AR32 | Mass change of assets (worklist execute/display) | COVERED-5 | 5 | Mass edit |
| Fiori: Asset Balances / Asset History Sheet / Depreciation Lists / Asset Transactions | S/4 apps | COVERED-37 | 37 | — |

### 13.4.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| AFAB / AFABN / FAA_DEPRECIATION_POST (S/4) | Depreciation posting run (planned, repeat, restart, unplanned) | COVERED-37 | 37 | Depreciation run per book with restart safety (Temporal) |
| AFAR | Recalculate depreciation | COVERED-37 | 37 | Recalc after parameter change |
| AFBP | Display depreciation posting log | COVERED-37 | 37 | — |
| AJRW | Fiscal year change (open new year) | SUPERSEDED-37 | 37 | No explicit year open; values projected per period; year-end close via wave 14 checklist |
| AJAB / FAA_CMP (S/4) | Year-end closing asset accounting / close fiscal year | COVERED-14 | 14 | Close checklist task with asset-period lock |
| OAAQ / OAAR | Reverse year-end closing / per area | COVERED-14 | 14 | Controlled reopen with audit |
| ASKB / ASKBN | Periodic posting of non-leading depreciation areas | COVERED-37 | 37 | — |
| AIAB / AIBU (periodic) | AuC settlement run | COVERED-37 | 37 | — |
| ABST2 | FI-AA / G/L reconciliation | COVERED-37 | 37 | Automated daily |
| AR29 / AR29N | Revaluation / index-based revaluation and new valuation | COVERED-37 | 37 | Revaluation |
| J1AI / country inflation accounting (AJ) | Inflation accounting | OUT-OF-SCOPE | — | Hyperinflation (IAS 29) country functions deferred beyond first country packs |
| RASIMU02 (depreciation simulation) | Simulation / forecast | COVERED-37 | 37 | — |
| RERAPP (RE-FX) | Periodic lease posting run | COVERED-37 | 37 | — |
| Fiori: Schedule Asset Accounting Jobs | Job templates | COVERED-4 | 4 | — |

### 13.4.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| EC08 / OAOB — Copy reference chart of depreciation / assign company code | T093C, T093 | — | COVERED-37 | 37 | Books template per country |
| OADB / OABC / OABD — Depreciation areas: define, area type, transfer of APC/depreciation terms | T093, T093A | Real/derived area, posting to G/L (0/1/2/3/4), ledger group, currency, value takeover rules | COVERED-37 | 37 | Books (tax/IFRS/local) posting to ledger groups; derived books (delta) — treat as covered |
| OAYZ / OAYR — Asset class default depreciation / posting rules (periodic/monthly) | T093B | Depreciation key, useful life per class and area; posting cycle | COVERED-37 | 37 | Asset class defaults, posting cycle |
| OAOA — Asset classes | ANKA, ANKB | Account determination, number range, screen layout, AuC flag, low-value asset flag, history | COVERED-37 | 37 | Asset groups/classes |
| AO90 — Account determination (APC, acc. depreciation, expense, gain/loss, revaluation, AuC) | T095, T095B | Per area and account determination key | COVERED-37 | 37 | Asset posting profiles |
| AO21 / AO25 / AO71 — Depreciation keys, methods (base, declining, multi-level, period control), maximum amount, cut-off value | T090*, T090NA, T090NAZ, T090NP, T090NR | Base methods (SL, DB, units of production), multi-level, period control (fiscal year start, mid-month, half-year), changeover methods, shutdown, scrap value | COVERED-37 | 37 | Depreciation profiles + conventions (half-year, mid-month, mid-quarter) incl. MACRS tables |
| OAVH / OAVS — Period control / calendar assignment | T090NP, T090NV | — | COVERED-37 | 37 | Conventions |
| AO73 / AO74 / AO76 — Transaction types, transaction type groups | TABW, TABWG | Debit/credit, retirement w/ revenue, capitalization, history sheet group | COVERED-37 | 37 | Transaction type model |
| OAYK — Low-value asset limits per area | T093B | Maximum LVA amount, quantity LVA | COVERED-37 | 37 | LVA threshold rules |
| OAYE / OAYF — Legacy data transfer date / last period posted | T093C | — | COVERED-49 | 49 | Migration parameters |
| OA79 / OA80 — Asset history sheet versions / groups | T082A, T082B, T082C | — | COVERED-37 | 37 | Roll-forward report definition |
| AO11 — Number range assets | NRIV | — | COVERED-5 | 5 | — |
| AO21 / OA77 — Screen layout rules for asset master / depreciation areas | T082G, T082H | Field status, maintenance level (class/main/sub) | COVERED-3 | 3 | Metadata field attributes |
| OAYR intervals — Specify intervals and posting rules (AFAB period) | T093D | Smoothing vs catch-up | COVERED-37 | 37 | Catch-up / smoothing option |
| OABN / OABW — Area for revaluation / unplanned depreciation allowed | T093A | Which transaction types allowed per area | COVERED-37 | 37 | — |
| OAV8 / OAVA — Asset evaluation groups / environmental protection indicators / asset locations | T087, T499S | Reporting attributes | COVERED-37 | 37 | Classification attributes on asset; site/location dimensions (wave 6) |
| OAYM / OAAX — Group assets per area | T093A | Group asset areas | PARTIAL-37 | 37 | Group asset gap |
| OAVI / OAVF — Insurance types, companies, index series | T086, T094 | — | PARTIAL-37 | 37 | Insurance gap |
| OAOA AuC settings — Asset under construction: settlement profile, line-item settlement | T093 | — | COVERED-37 | 37 | — |
| RE-FX IMG: contract types, valuation rules (IFRS 16/ASC 842), accounting principles, condition types, posting rules | TIVRA*, VICN* | Lease classification, discount rate, IBR | COVERED-37 | 37 | Lease parameters |
| Parallel valuation: ledger approach vs accounts approach (FAA_CMP settings) | T093, FINSC_LEDGER | — | COVERED-37 | 37 | Ledger-group approach only |
| Depreciation keys for tax (MACRS, ADS) tables | T090* | — | COVERED-37 | 37 | MACRS tables |

---

## 13.5 FI-BL / FSCM-CM / BAM — Bank Accounting, Cash Management & Bank Account Management

SAP context: house banks (FI12 → Bank Account Management in S/4 with workflow), electronic bank statement (FF_5/FEBA/FEBAN; S/4: Manage Bank Statements), manual statement (FF67), check deposit (FF68), cash position/liquidity forecast (FF7A/FF7B; S/4 Cash Management "One Exposure" FQM), memo records (FF63), bank communication management (BCM), in-house cash (IHC), cash journal. Passage: wave 36, with bank-side AR/AP matching in waves 19/30.

### 13.5.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| FI01 / FI02 / FI03 / FI06 | Bank master (bank key) create/change/display/flag | Bank directory | COVERED-7 | 7 | Bank as party role; bank directory import (SWIFT/ABA) |
| BAUP | Bank directory upload (file) | Mass bank key import | COVERED-7 | 7 | Import adapter |
| FI12 / FI12_HBANK / Fiori: Manage Bank Accounts (BAM) | House banks and bank accounts | Bank account master with workflow, signatories | COVERED-36 | 36 | Bank account master with signatories and approval |
| BAM: bank account review, signatory, overdraft limit | Bank account governance | — | COVERED-36 | 36 | — |
| FF67 | Manual bank statement entry | — | COVERED-36 | 36 | Manual statement entry |
| FF_5 / FF.5 | Import electronic bank statement (MT940, BAI2, camt.053) | EBS import | COVERED-36 | 36 | camt.053/BAI2/MT940 import |
| FEBA / FEBAN / FEBA_BANK_STATEMENT | Postprocess bank statement | Fix unmatched items | COVERED-36 | 36 | Reconciliation exception queue |
| FEBP | Post bank statement (batch) | — | COVERED-36 | 36 | — |
| Fiori: Reprocess Bank Statement Items | S/4 | — | COVERED-36 | 36 | — |
| FF68 | Check deposit (manual entry) | Deposit list | COVERED-36 | 36 | Check deposit |
| FF63 / FF65 | Memo records create / list (planned items) | Manual cash plan items | COVERED-36 | 36 | Forecast manual items |
| FF7A / FF7B → Fiori: Cash Position / Liquidity Forecast | Cash position / liquidity forecast | Treasury daily view | COVERED-36 | 36 | Cash positioning (intraday camt.052) and forecast from AP/AR/payroll/projects |
| FF/1 / FF/4 | Payment advice / planning type management | — | COVERED-36 | 36 | — |
| FCH* (check management — see FI-AP) | — | — | COVERED-36 | 36 | — |
| FLB1 / FLB2 (see FI-AR) | Lockbox | — | COVERED-30 | 30 | — |
| FBCJ | Cash journal | Petty cash | GAP | propose 36 | Cash journal gap (see FI-GL) |
| FRFT / FRFT_B | Repetitive payment codes / bank-to-bank transfer | Treasury transfers | PARTIAL-36 | 36 | Bank-to-bank transfer / payment request gap |
| BNK_MONI / BNK_APP / BNK_MONIP (BCM) | Bank Communication Management: batch monitor, approve payment batches, payment status | Payment batch approval and status (pain.002) | COVERED-36 | 36 | Bank file audit and approval; pain.002 ingestion (wave 19) |
| IHC0 / IHC1IP (In-House Cash) | In-house cash center, IHC accounts, internal payment orders | Intercompany banking | PARTIAL-36 | 36 | "In-house banking basics: netting, cash pooling" named; full IHC (internal accounts, interest, statements for subsidiaries) is partial |
| Fiori: Bank Statement Monitor | Bank statement monitor (S/4) | — | COVERED-36 | 36 | — |
| Fiori: Manage Bank Statements / Cash Flow Analyzer | S/4 Cash | — | COVERED-36 | 36 | — |
| Fiori: Approve Bank Payments / Monitor Payments (Advanced Payment Management) | APM | Payment hub routing | PARTIAL-36 | 36 | Payment approval covered; APM-style central payment hub (route, batch, reformat across entities) only partially (adapters) |
| Letters of credit / bank guarantees (TRM FTR_CREATE or BL) | Guarantees | — | COVERED-36 | 36 | LC and guarantees named |
| FBWE / FBWD (bills) | Bill of exchange presentation | — | GAP | propose 36 | Bills gap |

### 13.5.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| FF7A | Cash position | COVERED-36 | 36 | — |
| FF7B | Liquidity forecast | COVERED-36 | 36 | — |
| FF_6 | Display bank statement (electronic) | COVERED-36 | 36 | — |
| FF.6 | Display statement overview | COVERED-36 | 36 | — |
| FEBA (list mode) | Statement items overview | COVERED-36 | 36 | — |
| S_ALR_87012309 (cash journal) | Cash journal report | GAP | propose 36 | — |
| S_P99_41000101 / FCHN | Check register | COVERED-36 | 36 | — |
| FF.8 | Payment advice journal | COVERED-36 | 36 | — |
| FLQAB / FLQAD (liquidity planner) | Liquidity analysis / actual assignment | COVERED-36 | 36 | Cash flow forecast buckets |
| FQM_* (One Exposure) / Fiori: Cash Flow Analyzer | Cash flow analysis by liquidity item | COVERED-36 | 36 | — |
| Fiori: Bank Relationship Overview / Bank Fee Analysis | Bank fees (camt.086) | GAP | propose 36 | Bank fee analysis (camt.086) not named; propose as wave 36 report |
| Fiori: Cash Position Details / Check Cash Flow Items | — | COVERED-36 | 36 | — |
| BNK_MONI (report mode) | Payment batch status | COVERED-36 | 36 | — |

### 13.5.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| FF_5 (scheduled) | Nightly statement import | COVERED-36 | 36 | Scheduled job |
| FEBC / FEBP | Batch posting of statements | COVERED-36 | 36 | — |
| FF67 → FEBA (manual cycle) | Manual statement close | COVERED-36 | 36 | — |
| FF.D | Delete / archive memo records | COVERED-36 | 36 | — |
| FLQAC / FLQAD | Liquidity planner assignment run | COVERED-36 | 36 | — |
| FQM_FLOW_BUILDER (One Exposure rebuild) | Rebuild cash flows | SUPERSEDED-36 | 36 | Forecast projected from events; rebuild = projection rebuild |
| BNK_BATCH_* (BCM batching job) | Payment batching | COVERED-36 | 36 | — |
| Bank reconciliation statement (S/4 bank reconciliation) | — | COVERED-36 | 36 | Auto-reconciliation with matching rules + ML |
| Cash pooling / sweeping run (IHC) | — | PARTIAL-36 | 36 | Basics named |
| Intercompany netting run (IHC netting) | — | COVERED-36 | 36 | Netting named |
| F111 (treasury payment requests) | — | PARTIAL-36 | 36 | — |
| Bank fee statement import (camt.086) | — | GAP | propose 36 | — |

### 13.5.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OBXK / FI12 — House banks, accounts, G/L accounts, bank sub-accounts (clearing per payment method) | T012, T012K, T012D | Sub-accounts per payment method (outgoing checks, transfers) | COVERED-36 | 36 | Bank sub-account posting profile |
| OT83 / OT51 — Electronic bank statement: transaction types, external transaction codes, posting rules, account symbols, assign bank to transaction type | T028B, T028D, T028G, T033* | Posting rule (bank side / subledger side), interpretation algorithms (document number, reference, note-to-payee), BAI/MT940/camt format | COVERED-36 | 36 | Matching rules engine + posting rules |
| OTPM — Search strings for EBS | T028SS | Regex-like note-to-payee parsing | COVERED-36 | 36 | Matching rules with pattern extraction (ML suggestions) |
| OT41 / FF/1 — Planning levels / planning groups / cash management account names | T036, T035, T037 | Level per G/L and subledger group; forecast groups | COVERED-36 | 36 | Forecast categories/buckets |
| Liquidity items and derivation rules (FQM / FLQ) | FLQITEM*, FQM_* | Query sequences, derivation from G/L, BP, doc type | COVERED-36 | 36 | Forecast source rules |
| Define check lots / void reason codes (FCHI / FCHV) | PCEC, TVOID | — | COVERED-36 | 36 | — |
| Bank chains (FIBC) | TBCHAIN* | Intermediary banks per currency/country | PARTIAL-36 | 36 | Correspondent/intermediary bank chains not named — add to bank connectivity |
| BCM: rules for batching, approval (release) rules, payment status mapping | BNK_* | Approval patterns (n-eyes), batch rules | COVERED-36 | 36 | Approval workflow for bank files |
| IHC customizing: IHC centers, accounts, conditions, payment orders | IHC_* | — | PARTIAL-36 | 36 | IHC partial |
| BAM: account types, review process, signatory groups, workflow | FCLM_* | — | COVERED-36 | 36 | — |
| Cash journal customizing (FBCJC0..3) | TCJ_* | — | GAP | propose 36 | — |
| Bank connectivity (EBICS / SWIFT / Multi-Bank Connectivity settings) | — | — | COVERED-36 | 36 | SFTP/EBICS/Open-Banking adapters; keys in Vault |
| Lockbox customizing (OBAY/OBAX) — see FI-AR | — | — | COVERED-30 | 30 | — |

---

## 13.6 FSCM — Credit, Collections, Dispute Management & Treasury (TRM)

SAP context: FIN-FSCM-CR (credit management with scoring/BRF+), FIN-FSCM-COL (collections worklists, strategies), FIN-FSCM-DM (dispute cases on SAP Case Management), FIN-FSCM-BD (Biller Direct), FIN-FSCM-TRM (Treasury & Risk: transaction manager for money market/FX/derivatives/securities, market risk analyzer, hedge management, exposure management). Passage: credit wave 27, collections/disputes wave 30, cash/bank wave 36; TRM largely not covered.

### 13.6.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| UKM_BP | Credit management business partner (credit profile, segments, limits) | Credit master | COVERED-27 | 27 | Credit profile per customer/credit group |
| UKM_CASE | Credit limit request / case | Workflow for limit increase | COVERED-7 | 7 | Credit limit change approval (wave 7 governance + wave 27) |
| UKM_MY_DCDS | Documented credit decisions | Credit decision log | COVERED-27 | 27 | Credit hold release workflow with reasons |
| UKM_COMMITMENTS | Credit exposure (liability) display | — | COVERED-27 | 27 | — |
| UDM_DISPUTE | Create/process dispute case | Dispute | COVERED-30 | 30 | Disputes workflow |
| UDM_SUPERVISOR / UDM_SPECIALIST | Collections supervisor / specialist worklist | Collections | COVERED-30 | 30 | Collections workspace |
| Promise to pay / customer contacts / notes (collections) | — | — | COVERED-30 | 30 | Promises to pay |
| Resubmissions (collections) | Follow-ups | — | COVERED-30 | 30 | Follow-up tasks |
| Create dispute from FBL5N line item | Dispute from line item | — | COVERED-30 | 30 | — |
| Biller Direct web app | Customer invoice portal | — | SUPERSEDED-30 | 30 | Customer portal |
| FTR_CREATE / FTR_EDIT / FTR_00 | Treasury transaction manager: create financial transaction (money market, FX forward/spot/swap, derivatives, securities, loans) | Deal capture | GAP | propose 36 | TRM transaction manager is not in any wave. Manufacturers with FX exposure need at least FX spot/forward deal capture, settlement and valuation; propose a wave 36 "treasury deals (FX, loans, deposits)" extension, with derivatives/securities deferred to v2 |
| TBB1 / TPM10 | Post flows / treasury posting | Deal accounting | GAP | propose 36 | Follows TRM gap |
| TPM1 / TPM60 | Valuation (key date), amortization, position management | Mark-to-market | GAP | propose 36 | — |
| THMEX (Hedge management & accounting, IFRS 9) | Hedge relationships, effectiveness, hedge accounting postings | Hedge accounting | GAP | propose 36 | Hedge accounting per IFRS 9/ASC 815 — v2 candidate; register as gap |
| Exposure management (raw exposures, TEM) | Exposure capture from AP/AR/forecast | — | GAP | propose 36 | FX exposure from open items is derivable from waves 12/36 data; deal side missing |
| Market Risk Analyzer / Credit Risk Analyzer | VaR, limits | — | OUT-OF-SCOPE | — | Market/credit risk analyzers are bank-grade treasury; beyond manufacturer v1 |
| TBDM market data import (rates, prices) | Market data management | — | PARTIAL-12 | 12 | Exchange-rate providers covered; interest curves/security prices not |
| Loans management (FN*, CML) | Loan contracts | — | GAP | propose 36 | Intercompany and bank loans: schedules + interest; propose minimal loan contract in wave 36 |
| Commodity management (CPE, commodity pricing engine) | Commodity pricing in MM/SD | — | OUT-OF-SCOPE | — | Commodity derivatives/pricing engine not in v1; price-index-based condition could be done via pricing engine (wave 26) later |

### 13.6.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| UKM_LOGS_DISPLAY | Credit check logs | COVERED-27 | 27 | — |
| UKM_MALUS_DSP | Score/limit history | COVERED-30 | 30 | — |
| F.31 | Credit overview | COVERED-27 | 27 | — |
| UDM_SUPERVISOR (reports) | Collections KPI (promises kept, contacts) | COVERED-46 | 46 | — |
| UDM_DISPUTE (list/analytics) | Dispute analytics | COVERED-46 | 46 | — |
| Fiori: Credit Limit Utilization / Manage Credit Accounts / Manage Collection Worklist / Manage Dispute Cases | S/4 apps | COVERED-30 | 30 | — |
| TPM12 / TPM13 | Position list / flow list (TRM) | GAP | propose 36 | TRM gap |
| TPM20 | Posting journal (TRM) | GAP | propose 36 | TRM gap |
| Hedge plan reports | — | GAP | propose 36 | — |
| Treasury information system (TRM) | — | GAP | propose 36 | — |

### 13.6.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| UKM mass scoring / limit recalculation | Mass credit scoring | COVERED-30 | 30 | — |
| UKM_TRANSFER_ITEMS | Transfer FI/SD liabilities to FSCM credit | SUPERSEDED-27 | 27 | Exposure computed from the journal/orders directly |
| UDM_GENWL | Generate collections worklist | COVERED-30 | 30 | — |
| F150 (triggered strategies) | Dunning | COVERED-30 | 30 | — |
| Auto-create disputes from residuals / close promises | — | COVERED-30 | 30 | — |
| TBB1 (periodic) / TPM1 valuation run / accruals | TRM period-end | GAP | propose 36 | TRM gap |
| Hedge effectiveness test run | — | GAP | propose 36 | — |
| TBDM import run | Market data import | PARTIAL-12 | 12 | — |

### 13.6.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| UKM: credit segments, risk classes, rules, formulas (BRF+), check rules, score ranges, blocking reasons | UKMCRED_SGM*, UKM_* | Static/dynamic checks, oldest open item, max dunning level, scoring formula, follow-on process | COVERED-27 | 27 | Credit rules + scoring |
| UKM: integration (SD credit group, FI-AR liability update) | T691*, UKM_* | — | COVERED-27 | 27 | — |
| UDM collections: segments, groups, strategies (rules, priorities), promise-to-pay, resubmission reasons, customer contact types | UDM_* | — | COVERED-30 | 30 | — |
| Dispute case management: RMS, case types, attributes profile, status profile, reason codes, escalation, action profile | SCMG* | — | COVERED-30 | 30 | — |
| Biller Direct configuration | — | — | SUPERSEDED-30 | 30 | — |
| TRM: product types, transaction types, flow types, update types, valuation areas/classes, accounting codes, position management procedures | TRL*, TRD*, TPM* | — | GAP | propose 36 | TRM gap |
| Hedge management: hedging profiles, exposure categories | THM* | — | GAP | propose 36 | — |
| Market data: rate types, yield curves, volatility | VTB*, TCURR | — | PARTIAL-12 | 12 | — |

---

## 13.7 CO-OM — Overhead Cost Controlling (cost centers, internal orders, activity types, allocations)

SAP context: cost center accounting (CO-OM-CCA), internal orders (CO-OM-OPA), activity-based costing (CO-OM-ABC), cost element accounting (CO-OM-CEL; S/4 merges cost elements into G/L accounts FS00 type P/S). Planning (KP06/KP26), actual postings (KB11N/KB21N/KB31N), period-end allocations (KSV5/KSU5/KSC6), settlement (KO88), commitments. Passage: wave 38 cost accounting ledger with allocation policies; primary postings come straight from the universal journal (wave 11 dimension = cost center).

### 13.7.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| KS01 / KS02 / KS03 | Cost center create/change/display | Cost center master (validity, hierarchy, category, lock flags) | COVERED-6 | 6 | OperatingUnit (cost center) with effective dating; CO attributes in wave 38 |
| KS04 / KS05 / KS13 | Delete / display changes / list | — | COVERED-6 | 6 | — |
| KS12 / KS14 | Collective change / cost center list | Mass | COVERED-5 | 5 | — |
| KSH1 / KSH2 / KSH3 | Cost center group create/change/display | Groups (sets) | COVERED-6 | 6 | Hierarchy purposes; groups = hierarchy nodes |
| OKEON / OKENN | Standard hierarchy change/display | — | COVERED-6 | 6 | Hierarchy designer |
| KA01 / KA02 / KA03 / KA06 (→ FS00 in S/4) | Cost element primary / secondary create/change/display | Cost elements | SUPERSEDED-11 | 11 | S/4 model: accounts of type primary/secondary cost in the single chart |
| KA04 / KA05 / KA23 | Delete / changes / list cost elements | — | COVERED-11 | 11 | — |
| KAH1 / KAH2 / KAH3 | Cost element group | — | COVERED-11 | 11 | Account structures/groups |
| KL01 / KL02 / KL03 / KL04 / KL05 | Activity type create/change/display/delete/changes | Activity types (labor hour, machine hour) | COVERED-38 | 38 | Activity types with rates; used by routing (wave 9) and production costing (wave 21/39) |
| KLH1 / KLH2 / KLH3 | Activity type group | — | COVERED-38 | 38 | — |
| KK01 / KK02 / KK03 | Statistical key figure create/change/display | SKFs for allocation bases | COVERED-38 | 38 | Statistical measures |
| KBH1 / KBH2 / KBH3 | SKF groups | — | COVERED-38 | 38 | — |
| KO01 / KO02 / KO03 | Internal order create/change/display | Orders (overhead, investment, accrual, revenue) | COVERED-38 | 38 | Internal orders (cost objects) |
| KO04 / KOK2 / KOK3 / KOK4 | Order manager / collective processing / display / automatic collective | Mass order processing | COVERED-38 | 38 | — |
| KOH1 / KOH2 / KOH3 | Order group | — | COVERED-38 | 38 | — |
| KO22 / KO23 / KO24 / KO25 / KO26 / KO27 | Order budget: change/display, supplement, return, release | Budget on internal orders with availability control | COVERED-38 | 38 | Budget control on cost objects |
| KO30 / KO32 | Activate / deactivate availability control | — | COVERED-38 | 38 | — |
| KOB1 / KOB2 / KOB3 / KOB4 / KOB5 | Order line items: actual / commitment / variance / budget / settlement | Inquiry | COVERED-38 | 38 | — |
| KB11N / KB14N / KB13N | Manual reposting of costs (enter/reverse/display) | Reposting primary costs | COVERED-38 | 38 | Cost reposting journal (dimension-only move) |
| KB15N / KB16N / KB17N | Manual cost allocation (enter/display/reverse) | — | COVERED-38 | 38 | Manual allocation |
| KB21N / KB23N / KB24N | Activity allocation (enter/display/reverse) | Direct activity allocation (secondary) | COVERED-38 | 38 | Activity allocation entries; from shop floor (wave 25) |
| KB31N / KB33N / KB34N | Statistical key figures: enter/display/reverse | — | COVERED-38 | 38 | — |
| KB41N / KB43N / KB44N | Reposting revenues | — | COVERED-38 | 38 | — |
| KB51N / KB53N / KB54N | Activity posting (sender activities) | — | COVERED-38 | 38 | — |
| KB61 | Reposting of line items (document-based) | Repost with reference | COVERED-38 | 38 | — |
| KP06 / KP07 | Cost center plan: primary costs (change/display) | Planning | COVERED-38 | 38 | Budget planning with versions/scenarios |
| KP26 / KP27 | Activity type plan: quantities and prices | — | COVERED-38 | 38 | Activity rate planning |
| KP46 / KP47 | SKF planning | — | COVERED-38 | 38 | — |
| KPF6 / KPF7 | Order plan: primary costs | — | COVERED-38 | 38 | — |
| KP04 / KP65 / KP66 / KP67 | Planner profiles / planning layouts | Planning UX | SUPERSEDED-38 | 38 | Budget planning forms via metadata grids |
| KP90 / KP91 / KP98 | Delete plan data / copy plan versions | — | COVERED-38 | 38 | Scenario copy |
| KPSI | Plan reconciliation / iterative plan cost calculation | — | COVERED-38 | 38 | — |
| CP01 / CP02 / CP03 | Business process (ABC) create/change/display | ABC processes | COVERED-38 | 38 | Activity-based costing |
| CPH1 / CPH2 / CPH3 | Business process group | — | COVERED-38 | 38 | — |
| KSAZ (also customizing) | Overhead structure (costing sheet) | — | COVERED-39 | 39 | Costing sheets |
| Commitments from ME21N/ME51N with account assignment K/F | Commitments | — | COVERED-38 | 38 | Encumbrances/commitments from PR/PO |
| Fiori: Manage Cost Centers / Manage Activity Types / Manage Internal Orders | S/4 apps | — | COVERED-38 | 38 | — |
| Fiori: Reassign Costs and Revenues / Enter Activity Allocation / Enter Statistical Key Figures | S/4 apps | — | COVERED-38 | 38 | — |
| Fiori: Import Financial Plan Data (SAC/Excel) | Plan upload | — | COVERED-38 | 38 | Excel import / SAC-like planning via wave 46 |

### 13.7.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| S_ALR_87013611 | Cost centers: actual/plan/variance | COVERED-38 | 38 | Cost control workspace |
| S_ALR_87013612 | Cost centers: range: actual/plan/variance | COVERED-38 | 38 | — |
| S_ALR_87013613 / 87013614 | Actual/plan/variance by cost element / cost element groups | COVERED-38 | 38 | — |
| S_ALR_87013617 / 87013618 / 87013619 | Actual/target/variance; range cost centers; cost center groups | COVERED-38 | 38 | Target costs need activity-dependent plan — covered in wave 38/39 |
| S_ALR_87013620 | Actual comparison across fiscal years / periods | COVERED-38 | 38 | — |
| S_ALR_87013621 | Range: actual/plan/commitments | COVERED-38 | 38 | — |
| S_ALR_87013623 / 87013624 / 87013625 | Quarterly / year comparison / plan-plan versions | COVERED-38 | 38 | — |
| S_ALR_87013626 / 87013627 / 87013628 | Activity types: plan/actual; prices; comparison | COVERED-38 | 38 | — |
| S_ALR_87013631 / 87013632 | SKF actual/plan; activity quantities | COVERED-38 | 38 | — |
| S_ALR_87013633 / 87013635 / 87013636 | Cost component split; statistical key figures; area comparison | COVERED-38 | 38 | — |
| S_ALR_87013640 | Cost centers: planning overview | COVERED-38 | 38 | — |
| S_ALR_87013645 / 87013646 / 87013647 | Cost center: variance by cost element groups | COVERED-38 | 38 | — |
| KSB1 / KSB2 / KSB5 / KSBP | Cost center actual line items / commitments / CO documents / plan line items | COVERED-38 | 38 | — |
| KSBT | Activity prices report | COVERED-38 | 38 | — |
| KSBL | Cost center planning overview | COVERED-38 | 38 | — |
| KOB1 … KOB8 | Order line items (actual, commitment, variance, budget, settlement, results analysis) | COVERED-38 | 38 | — |
| S_ALR_87012993 / 87012994 / 87012995 | Orders: actual/plan/variance; actual/plan/commitment; list | COVERED-38 | 38 | — |
| S_ALR_87012996 / 87012997 / 87012998 | Orders: budget/actual/commitment; actual/target; cumulative | COVERED-38 | 38 | — |
| S_ALR_87012999 / 87013000 / 87013001 | Orders: current period / year comparison | COVERED-38 | 38 | — |
| S_ALR_87013002 … 87013010 | Order group reports | COVERED-38 | 38 | — |
| S_ALR_87013019 | Orders: list of orders with budget | COVERED-38 | 38 | — |
| KOC4 | Order selection (order manager) | COVERED-38 | 38 | — |
| ABC business process reports (S_ALR_87013 range) | Business process reports | COVERED-38 | 38 | — |
| KS13 | Cost center master data report | COVERED-6 | 6 | — |
| KSB9 | Plan/actual comparison | COVERED-38 | 38 | — |
| Fiori: Cost Centers – Plan/Actual / Internal Orders – Plan/Actual / Cost Center Line Items | S/4 analytical apps | COVERED-46 | 46 | — |
| Report Painter CO reports (GR55 library 1VK, 6O1) | — | SUPERSEDED-15 | 15 | Report designer + semantic layer |

### 13.7.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| KSV1 / KSV2 / KSV3 / KSV5 / KSV6 | Distribution cycle create/change/display/execute/overview (actual) | COVERED-38 | 38 | Allocation policies (distribution keeps primary cost element) |
| KSU1 / KSU2 / KSU3 / KSU5 / KSU6 | Assessment cycle (actual) | COVERED-38 | 38 | Assessment (secondary cost element) |
| KSW1 … KSW5 | Periodic reposting cycles | COVERED-38 | 38 | — |
| KSC1 … KSC6 | Indirect activity allocation cycles | COVERED-38 | 38 | — |
| KSVB / KSUB / KSCB | Plan distribution / plan assessment / plan indirect activity allocation | COVERED-38 | 38 | Plan allocations |
| KSII / KSPI | Actual price calculation / plan price calculation | COVERED-38 | 38 | Activity rate calculation (plan and actual) |
| KSS1 / KSS2 | Actual cost splitting / variance calculation for cost centers | COVERED-38 | 38 | Variance analysis for cost centers (target vs actual) |
| KSA3 | Accrual calculation (percentage method) | COVERED-14 | 14 | Accrual schemes |
| KO88 / KO8G | Settlement of orders (individual / collective) | COVERED-38 | 38 | Settlement rules to cost objects/assets/GL |
| KO8B | Display settlement documents | COVERED-38 | 38 | — |
| KGI2 / KGI4 | Overhead calculation on orders (individual / collective) | COVERED-39 | 39 | Costing-sheet overhead application |
| KSI4 | Overhead calculation on cost centers | COVERED-39 | 39 | — |
| CPTA / CPTB | ABC: template allocation (actual/plan) | PARTIAL-38 | 38 | ABC named; template (driver-formula) allocation is partial — specify driver formulas |
| KO9E | Order planning: plan settlement | COVERED-38 | 38 | — |
| OKP1 / OKP2 | Period lock change/display (CO) | COVERED-14 | 14 | Period control per module (CO) |
| KALA / KALB | Reconciliation ledger (classic CO-FI) | SUPERSEDED-11 | 11 | Single journal |
| CO-FI real-time integration (FAGLCOFIC) | — | SUPERSEDED-11 | 11 | — |
| KEU5 (CO-PA assessment — see CO-PA) | — | COVERED-38 | 38 | — |
| KSV5/KSU5 run via Schedule Manager (SCMA) | — | COVERED-14 | 14 | Close checklist |
| Fiori: Run Allocations / Manage Allocations (S/4 universal allocation) | S/4 allocation apps | COVERED-38 | 38 | — |
| Commitment carry-forward (KOCF / CJCF) | Carry forward open commitments | COVERED-38 | 38 | Encumbrance year-end handling |
| KO02 status: TECO/close orders (mass via KOK4) | Order status management | COVERED-38 | 38 | — |

### 13.7.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OKKP — Maintain controlling area (activation of components, currency, fiscal year variant, CoA) | TKA00, TKA01 | Cost centers, order management, commitments, ProfitAnalysis, PCA active; variance calculation | SUPERSEDED-6 | 6 | No controlling area; features switched per tenant/legal entity |
| OX19 — Assign company codes to controlling area | TKA02 | 1:1 or cross-company | SUPERSEDED-6 | 6 | — |
| KANK — Number ranges for CO documents | NRIV | Business transactions grouped | COVERED-5 | 5 | — |
| OKEQ — Maintain versions (plan versions, valuation settings) | TKA07, TKA09 | Plan/actual, exchange rate type, price calculation settings | COVERED-38 | 38 | Budget versions / scenarios |
| OKA2 — Cost center categories | TKT05 | Activity/actual revenue allowed, quantity | COVERED-6 | 6 | Cost center type attributes |
| OKEN / OKEON — Standard hierarchy | SETHEADER, SETNODE | — | COVERED-6 | 6 | — |
| OKB2 / OKB3 / OKB9 — Cost element default (batch input), automatic account assignment | TKA3A | Default cost center per account/company code | COVERED-11 | 11 | Default dimensions per account |
| Sender/receiver rules for cycles, allocation structures (OKO6), PA transfer structures (KEI1) | T811*, T811C, T811K | Segments, tracing factors (fixed %, portions, SKF, actual costs), iterative, cumulative | COVERED-38 | 38 | Allocation policies: fixed/basis/iterative |
| OKO7 — Settlement profiles, allocation structures, source structures, PA transfer structure | TKB1, T811* | Receivers allowed (CTR, ORD, FXA, G/L, WBS, PSG), settlement cost elements, amount/percent/equivalence | COVERED-38 | 38 | Settlement rules |
| KOT2_OPA / KOT2 — Order types (internal orders) | T003O | Order category, number range, settlement profile, planning profile, status profile, release immediately | COVERED-38 | 38 | Cost object types |
| BS02 — Status profiles (user statuses, allowed transactions) | TJ30 | — | COVERED-4 | 4 | Workflow state machines per entity |
| OKOB — Budget profiles, availability control tolerances | TBP* | Tolerance limits, actions (warning, mail, error), usage % | COVERED-38 | 38 | Budget check tolerances/overrides |
| KP65 / KP75 / KP85 — Planner profiles, planning layouts | TKA50, T240 | — | SUPERSEDED-38 | 38 | Metadata grids |
| KL01 customizing — Activity type categories (manual, indirect, as needed) | TKA* | Price indicator (plan/actual/manual), allocation cost element | COVERED-38 | 38 | — |
| Define periodic reposting / assessment cost elements | — | — | COVERED-38 | 38 | — |
| OKV1 / OKVW — Target cost versions / variance variants for cost centers | TKV01 | Fixed/variable variance categories | COVERED-38 | 38 | — |
| OKTZ (cost component structure) — see CO-PC | — | — | COVERED-39 | 39 | — |
| Commitments: activate commitment management (OKKP), commitment item mapping | TKA00 | — | COVERED-38 | 38 | — |
| CO versions for parallel valuation (group/profit-center valuation, transfer prices) | TKA07, TKA09, TKA01 | Legal/group/PC valuation | PARTIAL-40 | 40 | Transfer-pricing documentation hooks named; parallel group/profit-center valuation views (three valuation views in CO/ML) are not — register as partial for consolidation of profit in inventory |
| ABC: template allocation environments, functions (CPT1) | — | — | PARTIAL-38 | 38 | — |
| Statistical key figure categories (fixed/totals) | — | — | COVERED-38 | 38 | — |
| Activity types: allocation structure assignment | — | — | COVERED-38 | 38 | — |

---

## 13.8 CO-PC — Product Cost Controlling (product cost planning, cost object controlling, material ledger)

SAP context: CO-PC-PCP (cost estimates CK11N, costing runs CK40N, marking/releasing CK24, cost component split OKTZ, costing variants OKKN, costing sheets KZS2), CO-PC-OBJ (order-related: WIP KKAX, variances KKS1/KKS2, settlement CO88; product cost collectors KKF6N for REM; sales-order costing; results analysis KKA1-3), CO-PC-ACT (material ledger/actual costing CKMLCP, price change MR21, CKM3). Passage: wave 20 (valuation, material ledger, MR21), wave 21 (order costing, WIP, settlement), wave 39 (standard cost planning, roll-up, costing sheets, variances), wave 41 (results analysis for projects).

### 13.8.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| CK11N / CK13N | Create / display material cost estimate with quantity structure | Standard cost estimate from BOM + routing | COVERED-39 | 39 | BOM calculation by costing version/site/qty |
| CKUC | Multilevel unit costing (costing without quantity structure) | Manual cost estimate | COVERED-39 | 39 | Manual cost estimate lines (unit costing) |
| CKECP / CKECP1 | Easy cost planning / ad-hoc cost estimate | Templates for cost planning | PARTIAL-39 | 39 | What-if simulations named; template-based "easy" cost planning for non-material objects (projects/orders) — partial, map to wave 41 project estimates |
| CK24 | Price update: mark and release standard cost | Activate standard cost → MBEW | COVERED-39 | 39 | Standard cost activation with inventory revaluation posting |
| CK40N | Costing run (selection, structure explosion, costing, analysis, marking, release) | Mass costing | COVERED-39 | 39 | Mass roll of 50k items < 15 min |
| CK51N / CK53N | Sales order cost estimate create / display | Costing per sales order item (MTO) | PARTIAL-27 | 27 | Order-specific costing for configured/MTO products not explicitly named in waves 27/39 — add "sales order cost estimate" to wave 39 simulations |
| CK74N / CK75N | Additive costs create / change | Manual add-ons to cost estimate | COVERED-39 | 39 | Additive cost lines |
| CK91N / CK92 | Procurement alternatives / mixing ratios (mixed costing) | Multiple sources blended | PARTIAL-39 | 39 | Mixed costing (weighted sources: make vs buy, multiple vendors/production versions) not named — add to wave 39 fallback rules |
| KKF6N / KKF6M / KKF1 | Product cost collector create/change (REM) / collective | Cost collector per material/production version | COVERED-24 | 24 | Backflush costing for lean (periodic cost collector) |
| KKF2 / KKF3 | Cost object (general) create/change | Cost object nodes | COVERED-38 | 38 | Cost objects |
| KKPHIE / KKP2 | Cost object hierarchy | — | PARTIAL-38 | 38 | Cost object hierarchies for process cost distribution — partial |
| KZS2 (master) | Costing sheet maintain | Overhead rates | COVERED-39 | 39 | Costing sheets |
| KZZ2 / KZB2 / KZM2 | Overhead rates (percentage / quantity) / credits | — | COVERED-39 | 39 | Surcharge/rate nodes |
| KEKO / KEPH (cost estimate tables) | — | Data | COVERED-39 | 39 | Cost estimate entity with component split |
| MR21 | Price change (material) | Standard/MAP price change with revaluation | COVERED-20 | 20 | Cost changes, revaluation |
| MR22 | Debit/credit material (value only) | Inventory value adjustment | COVERED-20 | 20 | Value adjustment journal |
| CKMPRPUN | Price determination / periodic unit price | — | COVERED-20 | 20 | Periodic weighted average |
| CKM3 / CKM3N | Material price analysis (ML) | Per-period price history, categories (receipts, consumption, price diffs) | COVERED-20 | 20 | Costing inquiry per item/period |
| CKMLCP (document view) | Costing cockpit | — | COVERED-20 | 20 | — |
| CKMM | Change price determination (2 → 3) | ML activation per material | SUPERSEDED-20 | 20 | Costing method is an item model group attribute |
| CKMLPC / CKMLDM | ML price change / debit-credit | — | COVERED-20 | 20 | — |
| CKMSTART | ML startup | — | SUPERSEDED-20 | 20 | Always-on inventory ledger |
| KKAX / KKAO (document) | WIP calculation individual / collective | WIP per order | COVERED-21 | 21 | WIP postings |
| KKA1 / KKA2 / KKA3 | Results analysis (sales order / WBS / order) | POC / revenue-based RA | COVERED-41 | 41 | POC estimates for projects; sales-order RA via wave 29 rev rec |
| KKS1 / KKS2 (document) | Variance calculation (collective / individual) | Variance categories | COVERED-39 | 39 | Variance decomposition |
| CKR1 | Reorganization of cost estimates | — | SUPERSEDED-39 | 39 | Versioned estimates retained |
| Fiori: Analyze Costs by Work Center / Production Cost Analysis / Material Price Analysis | S/4 | — | COVERED-39 | 39 | — |

### 13.8.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| CK84_99 | Itemization of cost estimates | COVERED-39 | 39 | Itemization view |
| CK80_99 / CK86_99 / CK88_99 | Cost components / costed multilevel BOM / partner cost component split | COVERED-39 | 39 | Cost component split by level |
| CK85_99 | Cost estimate comparison | COVERED-39 | 39 | Cost estimate comparison |
| S_P99_41000111 | Analyze/compare material cost estimates | COVERED-39 | 39 | — |
| S_ALR_87099930 / S_P99_41000112 | Material cost estimate list / price analysis | COVERED-39 | 39 | — |
| KKBC_ORD / KKBC_HOE / KKBC_PKO | Cost analysis: order / hierarchy / product cost collector | COVERED-21 | 21 | Production order cost inquiry (plan vs actual, by cost component) |
| KKBC_KTR / KKBC_KUN | Cost object / sales order cost analysis | COVERED-29 | 29 | Sales-order profitability |
| KOB1 (production orders) | Order actual line items | COVERED-21 | 21 | — |
| S_ALR_87013127 / 87013128 / 87013129 | Order selection: target/actual/variance; WIP; variance categories | COVERED-39 | 39 | — |
| S_ALR_87013139 / 87013140 | Cumulative / period-based analysis | COVERED-39 | 39 | — |
| S_ALR_87013146 | Order summarization hierarchy | COVERED-46 | 46 | — |
| KKR0 / KKRC | Summarization hierarchy (define / run) | SUPERSEDED-46 | 46 | Semantic layer aggregates |
| CKM3N / CKMVFM | Material price analysis / value flow monitor | COVERED-20 | 20 | — |
| S_P99_41000062 | ML reports: price history, consumption | COVERED-20 | 20 | — |
| MB5L / MC.9 (value) | Inventory value list (see MM-IM) | COVERED-20 | 20 | — |
| KKAQ / KKAV | Results analysis reports / WIP reports | COVERED-21 | 21 | — |
| Variance category reports (lot size / price / quantity / resource usage) | — | COVERED-39 | 39 | — |
| Fiori: Production Cost Analysis / Analyze Work in Process / Analyze Production Variances | S/4 | COVERED-39 | 39 | — |

### 13.8.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| CK40N (periodic costing run) | Monthly/annual standard costing run | COVERED-39 | 39 | — |
| CK24 (mass release) | Release standard prices | COVERED-39 | 39 | — |
| KKAX / KKAO | WIP calculation (order / collective) | COVERED-21 | 21 | — |
| KKS1 / KKS2 / KKS5 / KKS6 | Variance calculation (collective / individual; cost collectors) | COVERED-39 | 39 | — |
| CO88 / KO88 / KK87 | Settlement production orders (collective / individual / cost collectors) | COVERED-21 | 21 | Order settlement to inventory/COGS/variance |
| KGI2 / CO43 | Overhead calculation on production orders (individual / collective) | COVERED-39 | 39 | — |
| KKA1/KKA2/KKA3 (runs), KKAJ / KKAK | Results analysis (collective) | COVERED-41 | 41 | — |
| CKMLCP | Material ledger costing cockpit (single-level, multi-level, revaluation of consumption, WIP revaluation, post closing) | COVERED-20 | 20 | Periodic actual costing with settlement of issues against receipts |
| CKMLCPAVR (Alternative valuation run) | Alternative valuation run (cumulative periods) | PARTIAL-20 | 20 | Multi-period cumulative actual costing (AVR) not named — partial |
| CKMDUVACT / CKMDUVMAT | Distribution of usage variances | PARTIAL-20 | 20 | Usage-variance distribution to consumers — partial |
| MMPV / MMRV | Close period / allow posting to previous period | COVERED-14 | 14 | — |
| WIP at actual cost (ML) | — | PARTIAL-20 | 20 | WIP revaluation at actual cost — partial |
| CKMLCP "post closing" → FI postings | ML closing entries | COVERED-20 | 20 | GL adjustments |
| KKAO/KKAX scheduling via SCMA | — | COVERED-14 | 14 | Close checklist |
| Fiori: Schedule Production Accounting Jobs | S/4 jobs | COVERED-4 | 4 | — |
| Product cost collector periodic settlement (KK87 / CO88 with PKO) | — | COVERED-24 | 24 | — |

### 13.8.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OKKN — Costing variants (costing type, valuation variant, date control, quantity structure control, transfer control) | TCK03, TCK05, TCK06, TCK07 | Valuation strategy for materials (plan price 1/2/3, std, MAP, PO price), activities (plan/actual rate), subcontracting, external processing; BOM/routing selection id; transfer existing estimates | COVERED-39 | 39 | Costing versions with fallback rules and explosion modes |
| OKK4 — Valuation variants | TCK05 | Strategy sequences | COVERED-39 | 39 | — |
| OKKM — Costing types | TCK03 | Price update allowed (std/tax/commercial) | COVERED-39 | 39 | — |
| OKTZ — Cost component structure (components, assignment of cost elements, primary cost split, rollup) | TCKH1..TCKH8 | Fixed/variable split, roll-up flag, COGS/inventory relevant | COVERED-39 | 39 | Cost component split / cost groups |
| OKYD — Cost component views / groups | TCKH* | — | COVERED-39 | 39 | — |
| KZS2 / KZM2 / KZZ2 / KZB2 / KZE2 — Costing sheets: calculation bases, percentage overheads, quantity overheads, credits, dependencies | TKZS*, T685* | Base cost element groups, overhead keys/groups, dependency (plant, order type), credit cost element and cost center | COVERED-39 | 39 | Costing sheet nodes: surcharge/rate with dependency keys |
| OKKI / OKOZ — Overhead keys / overhead groups | TKZU* | Per material | COVERED-39 | 39 | — |
| OPJI / OPJF — Selection ID for BOM/routing in costing | TCK19 | Alternatives selection | COVERED-39 | 39 | — |
| OKK3 — Date control, quantity structure control | TCK06 | — | COVERED-39 | 39 | — |
| Period lock for ML (OKP1) | — | — | COVERED-14 | 14 | — |
| OMX1 / OMX2 / OMX3 — Activate material ledger per valuation area; ML types; assign currency types | T001K, TCKM* | Price determination 2/3; currencies (10/30) | SUPERSEDED-20 | 20 | Costing method per item model group; multi-currency valuation covered by wave 12/20 |
| CKMLCP settings: activate actual costing, revaluation of consumption, WIP at actual | TCKM*, MLAUF | — | COVERED-20 | 20 | — |
| OKG1..OKG9 — Results analysis: RA keys, versions, methods (POC, revenue-based, cost-based), valuation methods, line IDs, assignment, update, posting rules | TKKA* | 15 RA methods, WIP/reserves categories | COVERED-41 | 41 | POC methods (project) + rev rec (wave 29) |
| OKGC / OKGD — WIP calculation: RA keys for production orders | TKKA* | WIP at target vs actual | COVERED-21 | 21 | — |
| OKV1 / OKVG / OKVW — Variance keys, variance variants, target cost versions | TKV01, TKV02, TKV03 | Variance categories (input price, input quantity, resource usage, remaining input, mixed price, output price, lot size, remaining); target cost version basis (current std, plan, alternative) | COVERED-39 | 39 | Variance decomposition categories (purchase price, production lot size/quantity/price/substitution) |
| OKO7 — Settlement profiles / allocation structures for production orders | TKB1, T811* | Settlement to material (MAT), variance to PA (PSG) | COVERED-21 | 21 | — |
| OPL8 (order-type parameters: costing variants plan/actual, RA key) — see PP | T399X | — | COVERED-21 | 21 | — |
| KKF6N settings: REM profile cost collector | — | — | COVERED-24 | 24 | — |
| Mixed costing: quantity structure types, procurement alternatives (OMXL) | CKMLMV* | — | PARTIAL-39 | 39 | Mixed costing gap |
| Easy cost planning: costing models, characteristics | CKF* | — | PARTIAL-39 | 39 | — |
| Sales order costing: requirements class settings (OVZG) valuation/costing | T459K | Valuated/non-valuated sales order stock, costing id, RA key | PARTIAL-27 | 27 | Sales-order stock valuation (valuated MTO stock) belongs to wave 16/21 — not named; partial |
| Transfer prices / multiple valuation views (OKKP, OKEQ group) | TKA01 | — | PARTIAL-40 | 40 | See CO-OM |
| Cost of goods sold split by cost component (S/4 COGS splitting) | FINSC_COGS* | Split COGS posting into components | PARTIAL-39 | 39 | COGS posting by cost component (material/labor/overhead accounts) not named in wave 28/39 — add posting profile option |
| Price-difference splitting (S/4) | — | Split PPV by cost component | PARTIAL-39 | 39 | Same |

---

## 13.9 CO-PA — Profitability Analysis (costing-based, account-based) and EC-PCA Profit Center Accounting

SAP context: CO-PA with operating concern (KEA0), characteristics/value fields, derivation (KEDR), valuation (KE4I), actual postings from SD billing/FI, assessments (KEU5), top-down distribution (KE28), planning (KEPM), reports (KE30). S/4HANA favours account-based ("margin analysis") in ACDOCA with attributed profitability segments. EC-PCA: profit centers (KE51), PC hierarchy, transfer prices, PCA reports (S_ALR_87013326). Passage: profitability = dimensions on the universal journal (wave 11) + semantic layer (wave 46); PCA = profit center dimension + hierarchy (wave 6); allocations wave 38.

### 13.9.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| KEA0 / KEA5 / KEA6 | Maintain operating concern / characteristics / value fields | Define profitability dimensions | SUPERSEDED-11 | 11 | Profitability characteristics are financial dimensions (up to 20) on every journal line; value fields = accounts (account-based model) |
| KEDR / KEDE | Derivation strategy / rules (derive characteristics) | Characteristic derivation | PARTIAL-11 | 11 | Default dimensions on master data exist; rule-based derivation tables (lookup A×B→C with validity) = substitution gap (see FI-GL) |
| KE4I / KE4J / KE4IM | Assign SD condition types / MM conditions to value fields | Valuation mapping | SUPERSEDED-29 | 29 | Pricing-condition amounts post to revenue/discount accounts with dimensions; no separate value field mapping |
| KE4U / KE40 | Valuation strategies / costing key (COGS split from cost estimate) | Valuation with cost component split | COVERED-39 | 39 | COGS by cost component (see CO-PC partial for posting) |
| KE21N / KE23N / KE24 | Create line item / display line item / line item list | Manual PA postings & inquiry | COVERED-11 | 11 | Journal with profitability dimensions |
| KE51 / KE52 / KE53 / KE54 | Profit center create/change/display/delete | PC master | COVERED-6 | 6 | Profit center as OperatingUnit type / dimension |
| KE55 / KE56 | Profit center collective / mass assignment to company codes | — | COVERED-5 | 5 | — |
| KCH1 / KCH2 / KCH3 / KCH5N / KCH6N | Profit center groups / standard hierarchy | — | COVERED-6 | 6 | Hierarchy designer |
| KE59 | Dummy profit center | Fallback PC | SUPERSEDED-11 | 11 | Dimension required-ness rules; no dummy |
| 1KE4 / 1KEK | PCA: transfer postings of receivables/payables | PCA periodic transfer | SUPERSEDED-11 | 11 | Real-time: dimensions on every line (document splitting partial noted) |
| 1KE1 / 1KEE | PC assignment monitor | Check master data assignment to PC | COVERED-6 | 6 | Unassigned-dimension report |
| 9KE0 | PCA: manual (statistical) postings | Statistical PC postings | COVERED-11 | 11 | Statistical/memo dimension journals (non-balancing) — treat covered via statistical ledger of wave 38 |
| KEPM | CO-PA planning framework | Sales/profit planning | COVERED-38 | 38 | Planning on dimensions (budget register) |
| 7KE1 / 7KE3 | PC planning: costs/revenues, balance sheet items | — | COVERED-38 | 38 | — |
| KES1 / KES3 | Characteristic values maintenance / hierarchies | — | COVERED-6 | 6 | Dimension values & hierarchies |
| KEQ3 / KEN1 | Segment-level characteristics / number ranges (PA documents) | — | SUPERSEDED-11 | 11 | — |
| KEDB / KEDU | Delete test data / rebuild summarization levels | — | SUPERSEDED-46 | 46 | Projections/lakehouse |
| Fiori: Market Segments – Actuals / Product Profitability / Customer Profitability | S/4 margin analysis apps | — | COVERED-46 | 46 | Governed metrics (revenue, margin) by dimension |
| Fiori: Manage Profit Centers / Manage Profit Center Groups | S/4 | — | COVERED-6 | 6 | — |

### 13.9.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| KE30 / KE31 / KE32 / KE33 | Execute / create / change / display profitability report (drill-down) | SUPERSEDED-46 | 46 | Semantic-layer explorer and report designer replace KE3x drill-down reports |
| KE24 / KE25 | Line item display actual / plan | COVERED-11 | 11 | — |
| KE3I / KE35 | Report transport / forms | SUPERSEDED-46 | 46 | — |
| KE4L / KE41 (pricing reports) | — | COVERED-26 | 26 | — |
| S_ALR_87013326 | PCA: plan/actual comparison | COVERED-38 | 38 | — |
| S_ALR_87013340 / 87013336 / 87013337 | PC group: plan/actual; balance sheet by PC; receivables/payables by PC | COVERED-15 | 15 | Dimension statements (document splitting partial noted) |
| KE5Z / KE5Y | PCA actual / plan line items | COVERED-11 | 11 | — |
| S_ALR_87013342 / S_ALR_87009712 | PC: return on investment / key figures | COVERED-46 | 46 | — |
| 2KEE / 2KET | PC totals records / balance carry-forward display | COVERED-14 | 14 | — |
| KE3L | Report trees | SUPERSEDED-5 | 5 | Workspace navigation |
| Fiori: Profit Center – Plan/Actual / Margin Analysis apps | — | COVERED-46 | 46 | — |

### 13.9.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| KEU1 / KEU2 / KEU3 / KEU5 | CO-PA assessment cycles (cost centers → segments) | COVERED-38 | 38 | Allocation to profitability dimensions |
| KE28 | Top-down distribution (actual) | PARTIAL-38 | 38 | Top-down distribution of aggregated values (e.g. freight by revenue share) to detailed segments not named — add as allocation type "top-down" |
| KE27 | Periodic valuation (revaluate with actual costs from ML) | PARTIAL-20 | 20 | Revaluation of COGS in profitability with actual cost — tied to ML revaluation of consumption partial |
| KE2B / KE2C | Subsequent posting of billing documents / FI docs to PA | SUPERSEDED-11 | 11 | Real-time single journal |
| KE4ST / KE4SMM | Simulate/transfer order settlements to PA | SUPERSEDED-21 | 21 | Settlement posts with dimensions |
| KEND / KEDR realignment run | Realignment (re-derive characteristics on history) | PARTIAL-11 | 11 | Mass re-derivation of dimensions on posted lines is a controlled restatement; propose explicit "dimension realignment" tool (audit-logged) in wave 11/15 |
| 1KEK / 1KE8 / F.5D / F.5E | PCA periodic transfers (payables/receivables, assets, materials) | SUPERSEDED-11 | 11 | — |
| 2KES | PCA balance carry-forward | COVERED-14 | 14 | — |
| 4KE5 / 4KE1-4KE3 | PCA distribution/assessment cycles | COVERED-38 | 38 | — |
| KEPM batch / plan copy (KE1Z) | Plan copy/versions | COVERED-38 | 38 | — |
| KEDV / KEDB | Summarization levels build (CO-PA) | SUPERSEDED-46 | 46 | — |

### 13.9.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| KEA0 — Operating concern: attributes (currency, fiscal year), data structure, environment | TKEB, CE1xxxx..CE4xxxx | Costing-based vs account-based; company-code currency | SUPERSEDED-11 | 11 | Account-based margin analysis = universal journal dimensions |
| KEKK — Assign controlling area to operating concern | TKA01 | — | SUPERSEDED-6 | 6 | — |
| KEDR — Derivation rules (table lookup, derivation rule, move, clear, enhancement) | TKEDR*, K9R* | — | PARTIAL-11 | 11 | Derivation gap |
| KE4I / KE4J / KE4IM / KE4W — Value field assignment, reset of value fields, quantity fields | TKEZU, TKEVA | — | SUPERSEDED-29 | 29 | — |
| KE4U / KE40 / KE4H — Valuation strategy & costing keys, assignment | TKEVA*, TKEPS | Costing key by material type/plant/point of valuation | COVERED-39 | 39 | COGS split |
| KEI1 / KEI2 — PA transfer structures (settlement/assessment to PA) | T811*, TKEPC | Cost element groups → value fields | SUPERSEDED-38 | 38 | Allocations target dimensions directly |
| KEPM planning: planning levels, packages, methods, layouts | TKEPL* | — | SUPERSEDED-38 | 38 | Budget planning |
| KE3 report customizing: forms, key figure schemes (KER1) | — | — | SUPERSEDED-46 | 46 | — |
| 0KE5 — PCA: controlling area settings (PCA active, dummy PC, elimination of internal business volume) | TKA01, TKA00 | — | SUPERSEDED-6 | 6 | — |
| 0KEM / 0KEP — PCA substitution rules / derivation of PC from SD (sales org, material), MM | T8A* | — | PARTIAL-11 | 11 | Derivation gap |
| 3KEH / 3KEI — Additional balance sheet/P&L accounts in PCA (classic) | T8A3* | — | SUPERSEDED-11 | 11 | — |
| 8KEM / 8KEN — Transfer prices: pricing procedures, conditions, transfer price variants | T8PT* | PC valuation view | PARTIAL-40 | 40 | Transfer pricing documentation hooks only; PC valuation view not covered |
| GB02 — Number ranges for PCA documents | NRIV | — | COVERED-5 | 5 | — |
| S/4: Define attributed profitability segment settings | FINSC_* | Attribution for cost center/order postings | SUPERSEDED-11 | 11 | — |
| KES3 — Characteristic hierarchies | — | — | COVERED-6 | 6 | Dimension hierarchies |

---

## 13.10 EC-CS / SEM-BCS / S/4HANA Group Reporting — Consolidation

SAP context: EC-CS (ECC), SEM-BCS (BW-based), S/4HANA Finance for Group Reporting (consolidation units, data monitor, consolidation monitor, reclassifications, IC elimination, consolidation of investments, currency translation, group reports via Fiori/SAC). Passage: wave 40 (group structures, ownership %, eliminations, translation, minority interest, consolidated statements via wave 15).

### 13.10.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| CX1N (EC-CS) / Fiori: Manage Consolidation Units | Consolidation units, groups, versions | Group structure | COVERED-40 | 40 | Group structures, ownership % |
| CX1X / Fiori: Manage Group Structure | Consolidation group hierarchy, periods of inclusion/divestiture | — | COVERED-40 | 40 | Ownership with effective dates |
| CX5S / Fiori: Define FS Items / FS item hierarchies | Financial statement items, breakdown categories, sub-items | Group chart of accounts | COVERED-40 | 40 | Group account structure (wave 15 account structures) |
| CXCD / Fiori: Data Monitor | Data collection: flexible upload, read from universal journal, manual journal | Data collection | COVERED-40 | 40 | Entity data from the journal; upload for non-Passage entities (wave 10 import) |
| CX20 / Fiori: Post Group Journal Entries | Manual consolidation entries (document types, posting levels 00/01/10/20/30) | Adjustments | COVERED-40 | 40 | Group adjustment journals at posting levels (entity, group) |
| CXCE / Fiori: Consolidation Monitor | Run tasks: reclass, IC elim, CoI, translation, validation | Consolidation run | COVERED-40 | 40 | — |
| Fiori: Manage Reclassifications / Define Reclassification Rules | Rule-based reclassifications | — | PARTIAL-40 | 40 | Elimination rules named; general reclassification rules (e.g. move FS item based on sign) partial |
| Fiori: Consolidation of Investments (activity-based: first consolidation, subsequent, step acquisition, divestiture; equity method) | CoI | Goodwill, minority interest | PARTIAL-40 | 40 | Minority interest named; purchase-method CoI with goodwill, step acquisitions, divestitures and equity-method accounting are not — partial |
| Fiori: Manage Investments / Equity data | Investment and equity holdings | — | PARTIAL-40 | 40 | — |
| Fiori: Define Validation Rules / Run Validations | Data validation before consolidation | — | COVERED-40 | 40 | Validation rules (wave 3 validation + reconciliation) |
| Fiori: Define Currency Translation Methods | Translation methods, rates per FS item type (closing, average, historical), CTA | — | COVERED-40 | 40 | IAS 21 translation |
| Fiori: Intercompany Matching & Reconciliation (ICMR) | IC matching at document level, auto-match rules, communication | IC reconciliation | COVERED-40 | 40 | IC matching and reconciliation workspace |
| Fiori: Manage Substitution/Validation Rules (Group Reporting) | — | — | COVERED-40 | 40 | — |
| Fiori: Group Data Analysis / Consolidated Balance Sheet / P&L / Cash flow | Group reports with drill to entity | — | COVERED-40 | 40 | Consolidated statements via wave 15 with drill |
| SEM-BCS: UCWB (workbench), UCMON (monitor) | BW-based consolidation | — | SUPERSEDED-40 | 40 | Native consolidation on the journal/lakehouse |
| EC-CS: data entry layouts, report writer reports | — | — | SUPERSEDED-40 | 40 | — |
| Disclosure management / XBRL (SAP DM, SAC) | Statutory filing packages | — | GAP | propose 48 | XBRL/ESEF filing output not named; propose electronic reporting designer (wave 48) output type |
| Fiori: Manage Ledgers for Group Reporting (Y1/Y2), versions, extension versions | Consolidation ledgers/versions | — | COVERED-40 | 40 | Consolidation versions (actual, budget, forecast) |

### 13.10.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| Fiori: Group Data Analysis (with reporting rules, hierarchies) | Consolidated analysis | COVERED-40 | 40 | — |
| Fiori: Consolidated Balance Sheet / Income Statement / Cash Flow / Changes in Equity | Group statements | COVERED-40 | 40 | Changes in equity statement — add to wave 15 standard statements (minor) |
| Fiori: Data Analysis – Journal Entries (group) | Group line items | COVERED-40 | 40 | — |
| Fiori: Group Journal Entry display / task logs | Audit of consolidation run | COVERED-40 | 40 | — |
| Fiori: Intercompany Reconciliation reports / ICMR dashboards | — | COVERED-40 | 40 | — |
| EC-CS report writer (database listing) | — | SUPERSEDED-15 | 15 | Report designer |
| S_ALR_87012 consolidation reports (EC-CS) | — | SUPERSEDED-15 | 15 | — |

### 13.10.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| Data monitor tasks: release universal journal, data validation, currency translation, balance carry-forward | Entity-level close for consolidation | COVERED-40 | 40 | — |
| Consolidation monitor tasks: IC elimination (AR/AP, revenue/expense, dividends), elimination of IC profit in inventory, CoI, group validation | Group-level tasks | PARTIAL-40 | 40 | IC balances and inventory profit named; dividend elimination and CoI partial |
| Period lock / task lock (period control for consolidation) | — | COVERED-14 | 14 | Period control per ledger incl. consolidation ledger |
| Balance carry-forward (group) | — | COVERED-14 | 14 | — |
| Reclassification runs | — | PARTIAL-40 | 40 | — |
| Matching run (ICMR) scheduled | — | COVERED-40 | 40 | — |

### 13.10.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Global settings: consolidation chart of accounts, versions, ledgers, fiscal year variant | TF*, FINCS_* | — | COVERED-40 | 40 | — |
| FS item attributes, breakdown categories, selections | FINCS_FSI*, TF* | Sub-assignments (partner unit, movement type/transaction type, functional area) | COVERED-40 | 40 | Group dimensions incl. partner and movement type (movement types needed for cash flow/equity — confirm wave 40 includes "transaction/movement type") |
| Document types (posting levels, auto-reversal, deferred tax) | FINCS_DOCTYPE | — | COVERED-40 | 40 | — |
| Reclassification methods / elimination methods (IC elimination of AR/AP, revenue/expense, dividends) | TF* | Two-sided elimination, differences to FS item, thresholds | PARTIAL-40 | 40 | — |
| CoI methods (purchase, equity, proportional), activities | TF* | Goodwill treatment, minority | PARTIAL-40 | 40 | — |
| Currency translation methods, exchange rate indicators, differences | TF* | — | COVERED-40 | 40 | — |
| Validation rules / reporting rules | FINCS_* | — | COVERED-40 | 40 | — |
| Data release/collection settings: mapping of operational CoA to group CoA, substitution | FINCS_MAP* | — | COVERED-40 | 40 | Account mapping entity ↔ group |
| ICMR: matching methods, reconciliation cases, auto-adjustment rules, communication templates | ICA_* | — | COVERED-40 | 40 | — |
| Group reporting data mapping for external (non-SAP) units (flexible upload formats) | — | — | COVERED-10 | 10 | Import DSL |

---

## 13.11 FM / PSM — Funds Management & Public Sector Management (budgeting)

SAP context: FM (budget control system BCS: FMBB budgeting workbench, FMAVCR availability control, funds centers FMSA/FMSB, commitment items FMCIA, funds FM5I, FMDERIVE account assignment derivation, earmarked funds FMX1/FMY1, FM reports FMRP), PSM (grants management GM, funded programs, PSCD). Manufacturers rarely activate FM; D365 F&O's budget control is the model Passage follows (wave 38). FM rows map to wave 38 where they are budget control; genuinely public-sector functions are OUT-OF-SCOPE.

### 13.11.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| FMSA / FMSB / FMSC | Funds center create/change/display | Budget responsibility unit | COVERED-38 | 38 | Budget dimensions (cost center/department) |
| FMCIA / FMCIC | Commitment item create/display | Budget line category | COVERED-38 | 38 | Budget account categories (main accounts/groups) |
| FM5I / FM5S | Fund create/display | Funding source | PARTIAL-38 | 38 | "Fund" dimension (restricted/unrestricted funding) not named; grants/projects usually carry it — partial; usable via a financial dimension |
| FMMEASURE (funded program) | Funded program | — | PARTIAL-38 | 38 | Same |
| FMBB | Budgeting workbench (BCS): enter, supplement, return, transfer, release, carry-over | Budget documents | COVERED-38 | 38 | Budget register entries with types (original, transfer, supplement) |
| FMBBC / FMMPDA | Budget mass copy / plan to budget | — | COVERED-38 | 38 | — |
| FMX1 / FMX2 / FMX3 | Earmarked funds: funds reservation create/change/display | Pre-commitment reservation | PARTIAL-38 | 38 | Pre-encumbrance from requisition is named; manual "funds reservation" without a requisition is not — add budget reservation document |
| FMY1 / FMZ1 / FMW1 / FMV1 | Funds precommitment / commitment / forecast of revenue / funds blocking | — | PARTIAL-38 | 38 | Same |
| FMAVCR01 / FMAVCR02 | Availability control: display annual values / overall | Budget consumption | COVERED-38 | 38 | Budget check and consumption inquiry |
| FMDERIVE | Derivation of FM account assignment | — | PARTIAL-11 | 11 | Derivation gap |
| FMRP_RFFMEP1AX / FMRP_RFFMEP1GX | FM line items (actuals/commitments) | — | COVERED-38 | 38 | — |
| GM: GMGRANT, grants budgeting | Grant master, grant budgeting | Grants management | OUT-OF-SCOPE | — | Public-sector/university grants; PRD scope is manufacturers (wave 41 has "customer funding/grants" for projects which covers project funding sources) |
| PSCD / FI-CA (public sector collection) | Contract accounts | — | OUT-OF-SCOPE | — | Utilities/public-sector contract accounting |
| Fiori: Budget Control Overview / Manage Budget Documents | S/4 FM apps | — | COVERED-38 | 38 | — |

### 13.11.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| FMRP_RW_BUDGET / FMRP_RW_BUDCON | Budget overview / budget consumption | COVERED-38 | 38 | — |
| FMRP_RFFMAV01X / FMAVCR01 | Availability control overview | COVERED-38 | 38 | — |
| FMEDDW | Budget entry documents, totals | COVERED-38 | 38 | — |
| FM commitments/actuals reports (FMRP_*) | — | COVERED-38 | 38 | — |
| GM reports (S_ALN_*) | Grants reports | OUT-OF-SCOPE | — | — |

### 13.11.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| FMJ2 / FMJ_SPLIT | Commitment carry-forward (open PO/PR to next year) | COVERED-38 | 38 | Encumbrance year-end handling |
| FMMC / FMMA | Budget carry-forward (residual) | COVERED-38 | 38 | — |
| FMAVCREINIT | Reinitialize AVC ledger | SUPERSEDED-38 | 38 | Projection rebuild |
| FMCG / FMCJ | Close/open budget period (BCS period control) | COVERED-14 | 14 | Period control (budget module) |
| FMBB release run / mass release | — | COVERED-38 | 38 | Budget workflow |
| FM reconstruction (FMN3N / FMN4N / FMN5N) | Reconstruct commitments/actuals | SUPERSEDED-38 | 38 | — |

### 13.11.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| FM area definition, assignment to company codes, fiscal year variant | FM01, T001 | — | SUPERSEDED-6 | 6 | Budget scope = legal entity + dimensions |
| Activate FM / BCS | FMAVC* | — | SUPERSEDED-38 | 38 | Budget control activation per entity |
| Budget types, budget categories, versions (FMBB types) | FMBCS* | Payment/commitment budget | COVERED-38 | 38 | Budget types |
| Availability control ledgers, tolerance profiles, activation per budget address, filter settings | FMAVC*, FMAVCT | Warning %, error %, derivation of control objects | COVERED-38 | 38 | Budget check tolerances and overrides |
| Commitment item categories (expense/revenue/balance), financial transactions | FMCI* | — | COVERED-38 | 38 | — |
| FMDERIVE strategy | FMDERIVE* | — | PARTIAL-11 | 11 | — |
| Update profile (payment budget vs commitment budget, update dates) | — | — | SUPERSEDED-38 | 38 | — |
| Earmarked funds document types, number ranges | FMEF* | — | PARTIAL-38 | 38 | — |
| Grants management customizing | GM* | — | OUT-OF-SCOPE | — | — |

---

## 13.12 MM-PUR — Purchasing

SAP context: purchase requisitions (ME51N), RFQ/quotations (ME41–ME49), purchase orders (ME21N), outline agreements — contracts (ME31K) and scheduling agreements (ME31L/ME38), info records (ME11), source lists (ME01), quota arrangements (MEQ1), release strategies (classification-based), purchasing conditions (MEK1), vendor evaluation (ME61), output (ME9F), stock transport orders, subcontracting, consignment, third-party. S/4: Fiori Manage Purchase Orders/Requisitions, central procurement, Ariba. Passage: wave 17 (requisitions, RFQ, agreements, POs, pricing, approvals), wave 7 (vendor), wave 20 (supplier portal), wave 33 (vendor evaluation), wave 16 (transfers), wave 40 (intercompany).

### 13.12.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| ME51N / ME52N / ME53N | Purchase requisition create/change/display | Internal demand | COVERED-17 | 17 | Requisition with policy approvals |
| ME54N / ME55 | Release PR individual / collective | Approval | COVERED-17 | 17 | Approval workflow (Temporal) |
| ME56 / ME57 | Assign source of supply / assign and process (convert to PO) | Sourcing | COVERED-17 | 17 | Consolidation into POs; source determination from agreements/price lists |
| ME58 / ME59N | Create PO from assigned PRs / automatic PO creation | — | COVERED-17 | 17 | Auto-PO from approved requisitions |
| ME5A / ME5J / ME5K | PR list display / by project / by account assignment | — | COVERED-17 | 17 | Saved views |
| ME5F | Release reminder (PR) | — | COVERED-4 | 4 | SLA timers/escalation |
| ME41 / ME42 / ME43 | RFQ create/change/display | Sourcing event | COVERED-17 | 17 | RFQ cycle |
| ME44 / ME45 | Maintain RFQ supplement / release RFQ | — | COVERED-17 | 17 | — |
| ME47 / ME48 | Maintain / display quotation | Supplier reply | COVERED-17 | 17 | Supplier reply portal (wave 20) + manual entry |
| ME49 | Price comparison list | Compare quotes | COVERED-17 | 17 | Compare & award |
| ME4L / ME4M / ME4N / ME4S | RFQ lists by vendor / material / number / collective number | — | COVERED-17 | 17 | — |
| ME21N / ME22N / ME23N | Purchase order create/change/display | PO | COVERED-17 | 17 | PO lifecycle with versions |
| ME21 / ME22 / ME23 (classic) | — | — | SUPERSEDED-17 | 17 | Single PO form |
| ME24 / ME25 / ME27 | Maintain PO supplement / create PO with vendor unknown / stock transport order (classic) | — | COVERED-17 | 17 | Vendor-unknown PO = requisition |
| ME28 / ME29N | Release PO collective / individual | Approval | COVERED-17 | 17 | — |
| ME2O | Subcontracting stock per vendor (SC stock monitor) | Components at subcontractor | COVERED-21 | 21 | Subcontracting with vendor PO linkage; vendor-held inventory location |
| ME2ON | SC stock monitoring (S/4) | — | COVERED-21 | 21 | — |
| ME31K / ME32K / ME33K | Contract (quantity/value) create/change/display | Blanket agreement | COVERED-17 | 17 | Purchase agreements with release tracking |
| ME34K / ME35K | Maintain contract supplement / release contract | — | COVERED-17 | 17 | — |
| ME31L / ME32L / ME33L | Scheduling agreement create/change/display | SA with delivery schedules | COVERED-17 | 17 | PO delivery schedules + agreements; JIT/forecast schedule lines (ME84/ME38) — see ME84 row |
| ME34L / ME35L | SA supplement / release | — | COVERED-17 | 17 | — |
| ME38 / ME39 | Maintain / display SA delivery schedule | Schedule lines | COVERED-17 | 17 | Delivery schedules on agreement lines |
| ME84 / ME85 / ME86 | Create SA releases (FRC/JIT) / renumber / display release documentation | Forecast / JIT delivery schedules to supplier (EDI DELFOR/DELJIT) | PARTIAL-17 | 17 | Vendor-facing forecast/JIT release generation (cumulative quantities, release documentation) is a supplier-schedule capability not explicitly named in wave 17; EDI DELFOR/DELJIT in wave 35 lists 850/855/856/810/820/214 only — partial |
| ME9F / ME9K / ME9L / ME9E | Message output PO / contracts / SA / SA releases | Print/EDI | COVERED-17 | 17 | Supplier output (PDF/email), EDI in wave 35 |
| ME11 / ME12 / ME13 / ME14 / ME15 | Purchasing info record create/change/display/changes/flag delete | Vendor–material price & lead time | COVERED-17 | 17 | Trade agreements / vendor price lists per vendor×product×site with lead time; info-record "last PO" updates |
| ME1L / ME1M / ME1W / ME1P | Info records by vendor / material / material group / order price history | — | COVERED-17 | 17 | — |
| ME01 / ME03 / ME04 / ME05 / ME06 / ME07 | Source list maintain/display/changes/generate/analyze/reorganize | Allowed/fixed sources, MRP-relevant | COVERED-17 | 17 | Approved vendor list per item/site with MRP relevance flag (wave 22 uses it — confirm "fixed source for planned purchase orders") |
| MEQ1 / MEQ3 / MEQ4 / MEQ6 / MEQ8 | Quota arrangement maintain/display/changes/analyze | Split demand across sources | PARTIAL-22 | 22 | Quota arrangements (percentage split across vendors/own production for MRP) not named in wave 17/22 — partial |
| MEK1 / MEK2 / MEK3 / MEK4 | Create/change/display conditions (purchasing) / create with reference | Condition records (PB00, RA01, FRA1) | COVERED-17 | 17 | Vendor pricing conditions (price, discounts, freight) via pricing engine shared with wave 26 — confirm purchasing uses the wave 26 engine (wave 17 says "trade agreements/price lists") |
| MEKA / MEKB / MEKC / MEKD / MEKE | Condition lists by vendor/material group/material/plant | — | COVERED-17 | 17 | — |
| MEKP / MEKR / MEKH | Price change conditions mass / by vendor | — | COVERED-26 | 26 | Mass price updates |
| ME61 / ME62 / ME63 / ME64 / ME65 | Vendor evaluation maintain/display/calculate scores/evaluation comparison/ranking list | Vendor scorecards | COVERED-33 | 33 | Supplier quality scorecards (quality, delivery, price, service criteria) |
| ME6A / ME6B / ME6C / ME6D / ME6E / ME6F / ME6G / ME6H | Vendor evaluation lists, changes, without weighting, log | — | COVERED-33 | 33 | — |
| MK01 / XK01 (purchasing views) | Vendor purchasing data | — | COVERED-7 | 7 | — |
| MKVZ / MKVG | Vendor list purchasing / vendor-material | — | COVERED-7 | 7 | — |
| ME21N item category L (subcontracting) | Subcontract PO with components (BOM explosion) | — | COVERED-21 | 21 | — |
| ME21N item category K (consignment) | Vendor consignment PO | — | PARTIAL-16 | 16 | Consignment stock (vendor-owned at our site, settle on consumption MRKO) not named in waves 16/18 — partial; needs ownership attribute on inventory + settlement run |
| ME21N item category S (third-party) | Third-party / drop-ship PO from sales order | — | COVERED-28 | 28 | Drop-ship / direct delivery |
| ME21N item category U (stock transfer) / UB STO / NB cross-company STO | Stock transport orders (intra/intercompany) with delivery | — | PARTIAL-16 | 16 | Wave 16 "transfer journals" and wave 40 intercompany trade cover the postings; an explicit *transfer order document* with delivery, in-transit stock and receiving (plant-to-plant with shipping) is not named — partial; propose "transfer orders with in-transit status" in wave 16/28 |
| ME21N pipeline (P) | Pipeline materials (utilities) | — | OUT-OF-SCOPE | — | Pipeline handling (no PO, settlement on withdrawal) — niche; can be modeled as consignment later |
| ME21N service item (D) | Services PO | — | PARTIAL-18 | 18 | See MM-SRV |
| ME21N limit items / blanket PO (FO) | Framework orders with value limit | — | COVERED-17 | 17 | Blanket agreements; limit PO as order type — confirm |
| ME2V / ME2W | Goods receipt forecast / vendor delivery forecast | — | COVERED-18 | 18 | Expected receipts |
| MEMASSPO / MEMASSRQ / MEMASSIN / MEMASSSA | Mass change PO / PR / info record / SA | — | COVERED-5 | 5 | Mass edit |
| ME80FN / ME80RN / ME80AN | General analyses: PO / RFQ / agreements | — | COVERED-17 | 17 | — |
| ME91F / ME91A / ME91E | Reminders/expediters (urging) for PO / RFQ / SA | Expediting letters | COVERED-17 | 17 | Expediting output and dunning of overdue confirmations — confirm in wave 17 ("confirmation" named) |
| ME92F / ME92K / ME92L | Monitor order acknowledgment | Confirmation tracking | COVERED-17 | 17 | PO confirmation; supplier portal acknowledgement (wave 20) |
| ME97 / ME98 | Archive PRs / POs | — | COVERED-48 | 48 | Retention policies |
| ME2A | Monitor confirmations | — | COVERED-17 | 17 | — |
| ME23N → "Purchase order history" | PO history (GR, IR, DP) | — | COVERED-18 | 18 | PO line document flow |
| Fiori: Manage Purchase Requisitions / Orders / Contracts / Scheduling Agreements / Supplier Quotations / Purchasing Info Records / Source Lists | S/4 apps | — | COVERED-17 | 17 | — |
| Fiori: Create Purchase Requisition (self-service, catalog, free text) | Self-service requisitioning with catalogs (OCI punch-out) | — | PARTIAL-17 | 17 | Requisition yes; catalog/punch-out (OCI/cXML) item selection not named — partial; propose wave 20 (supplier collaboration) or wave 35 (integration) |
| Fiori: Monitor Purchase Order Items / Purchase Order Items by Account Assignment | — | — | COVERED-17 | 17 | — |
| Central procurement (hub-and-spoke, central PR/PO/contract) | Multi-system procurement | — | SUPERSEDED-17 | 17 | Single multi-entity tenant; cross-entity contracts via wave 40 |
| SAP Ariba Buying / Sourcing / Contracts / SLP (supplier lifecycle) | Cloud procurement suite | — | PARTIAL-20 | 20 | Supplier portal (ack, ASN, invoice, docs) covers Ariba Network basics; strategic sourcing events (auctions, weighted RFP scoring), supplier onboarding/qualification questionnaires and contract authoring are not — partial; propose v1.x |

### 13.12.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| ME2L / ME2M / ME2N / ME2K / ME2C / ME2J / ME2B / ME2W | Purchasing documents by vendor / material / number / account assignment / material group / project / requirement tracking / supplying plant | COVERED-17 | 17 | List pages with saved views |
| ME3L / ME3M / ME3N / ME3K / ME3C / ME3B / ME3J | Outline agreements by vendor / material / number / account assignment / material group / tracking / project | COVERED-17 | 17 | — |
| ME5A / ME5K / ME5J | Requisitions lists | COVERED-17 | 17 | — |
| ME80FN / ME80RN / ME80AN / ME81N | General analyses; analysis of order values | COVERED-46 | 46 | — |
| ME1P | Order price history | COVERED-17 | 17 | — |
| ME2V | Goods receipt forecast (expected deliveries) | COVERED-18 | 18 | — |
| ME6H | Vendor evaluation: standard analysis | COVERED-33 | 33 | — |
| MC$G / MC$I / MC$4 / MC$6 / MC$O / MC$< | PURCHIS: vendor analysis (purchasing values, quantities), material, material group, delivery reliability, PO value | COVERED-46 | 46 | Purchasing analytics in semantic layer |
| MCE1 / MCE3 / MCE5 / MCE7 / MCE8 | PURCHIS: purchasing group / vendor / material group / material / long-term planning analyses | COVERED-46 | 46 | — |
| MELB | Purchasing transactions per requirement tracking number | COVERED-17 | 17 | — |
| MSRV1 / MSRV3 (services) | — | PARTIAL-18 | 18 | See MM-SRV |
| Open POs by vendor / open PO value reports | — | COVERED-17 | 17 | — |
| ME2L with selection parameter WE101/RECHNUNG | Open GR / open invoices by PO | COVERED-18 | 18 | — |
| Fiori: Purchase Order Items by Supplier (analytical), Supplier Evaluation by Time/Quantity/Price/Quality, Spend Analysis apps | S/4 analytical apps | COVERED-46 | 46 | — |
| Fiori: Monitor Supplier Confirmations / Monitor Scheduling Agreement Releases | — | PARTIAL-17 | 17 | SA release documentation partial |
| RFQ analytics / quotation comparison | — | COVERED-17 | 17 | — |

### 13.12.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| ME59N (batch) | Automatic PO generation from PRs | COVERED-17 | 17 | Scheduled auto-PO |
| ME84 (batch) | SA release creation run | PARTIAL-17 | 17 | SA release partial |
| ME9F (batch) / RSNAST00 | Output processing (print/EDI) | COVERED-17 | 17 | Output jobs; wave 35 EDI |
| ME91F (batch) | Expediting run | COVERED-17 | 17 | — |
| ME63 / ME6G (batch) | Vendor evaluation scoring run | COVERED-33 | 33 | — |
| MEKP / MEKPE (batch) | Price change mass run | COVERED-26 | 26 | — |
| ME07 | Source list reorganization | SUPERSEDED-17 | 17 | Effective dating |
| ME97 / SARA (MM_EKKO) | Archiving POs | COVERED-48 | 48 | Retention |
| ME2K (commitments) | Commitment update | COVERED-38 | 38 | Encumbrance from PO |
| MRKO (consignment settlement) | — | PARTIAL-16 | 16 | Consignment gap |
| Contract release value update / expiry monitoring (ME3M with validity) | Contract expiry alerts | COVERED-17 | 17 | Agreement validity alerts |
| MEI1 / MEI2 / MEI4 | Subsequent price change (automatic document adjustment after condition change) | PARTIAL-17 | 17 | Retroactive re-pricing of open POs after condition change — not named; partial |
| Fiori: Schedule Purchasing Jobs | Job templates | COVERED-4 | 4 | — |

### 13.12.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OX08 / OX01 / OX17 — Purchasing organizations; assign to company code / plant; reference purch. org | T024E, T024W, T024Z | Plant-specific, cross-plant, cross-company, reference purch org | COVERED-6 | 6 | Procurement hierarchy purpose in enterprise structure |
| OME4 — Purchasing groups | T024 | — | COVERED-6 | 6 | Buyer groups |
| OMEC / OMEB / OMEE / OMED — Document types PO / RFQ / contract / SA; number ranges; allowed item categories; link PR↔PO | T161, T161A, T161P | Field selection, control indicators (GR/IR required), time-dependent conditions, release groups | COVERED-17 | 17 | Order types with number sequences and allowed line types |
| OMH7 / OMH6 — Number ranges for purchasing documents | NRIV | — | COVERED-5 | 5 | — |
| OMF4 — Screen layout at document level / field selection keys | T162, T162K | Required/optional per field selection key (e.g. ME21N, NBF, AKTH) | COVERED-3 | 3 | Metadata field attributes by order type |
| OMET — Function authorizations for buyers (value limits, order types) | T160B | — | COVERED-2 | 2 | OPA policy on amounts/order types |
| Item categories / account assignment categories (OME9) | T163, T163K, T163B | Account assignment (K, F, P, A, C, Q), GR/IR indicators, consumption posting | COVERED-17 | 17 | Account assignment line types; project (wave 41), asset (wave 37) |
| OMGQ / OMGS / OMGSCK — Release procedure for POs (with classification) / PRs (with/without classification): release groups, codes, indicators, strategies, prerequisites, workflow | T16FG, T16FC, T16FS, T16FK, T16FW, T16FV | Classification characteristics (value, plant, group), release simulation, workflow assignment | SUPERSEDED-17 | 17 | Policy-driven approval chains (amount bands, category, site) configured in the wave 4 workflow designer; no classification system needed |
| OMKK — Price variance tolerance limits for PO vs. PR / info record (PE, SE keys) | T169G | Percentage/absolute, warning/error | COVERED-17 | 17 | Price tolerance on PO vs agreement |
| OMFI / OMFJ — Version management for PR / PO (reasons, fields) | T160VS, T160VSK | Version-relevant fields, reasons | COVERED-17 | 17 | Versioned change management |
| M/03 … M/08 — Purchasing condition tables / access sequences / condition types / calculation schema (RM0000, RM1000), schema groups for vendor/purchasing org, schema determination | T681*, T682*, T685, T683 | Condition types PB00/PBXX/RA00/FRA1/SKTO, header/item, group conditions, time-dependent | SUPERSEDED-26 | 26 | Shared pricing engine (condition tables/access sequences) — wave 26 engine used by purchasing; wave 17 price lists |
| Price determination process / condition control at plant level | T001W | — | COVERED-17 | 17 | — |
| Default values for buyers (user parameters EVO/EFB) | T160, TMGU | — | COVERED-5 | 5 | User defaults / personalization |
| Texts for purchasing documents, text types, copying rules | T165*, TTXID | — | COVERED-17 | 17 | Document text blocks (header/line notes) |
| Partner roles in purchasing (OA ordering address, PI invoicing party, GS goods supplier), partner schemas | TPAR, TPAER | — | COVERED-7 | 7 | Party roles & document partners |
| Reasons for ordering, reasons for rejection (RFQ) | T161* | — | COVERED-17 | 17 | Reason codes |
| Confirmation control (confirmation control keys, categories AB/LA) | T163D, T163G | Confirmation sequences, GR-relevant, MRP-relevant | COVERED-17 | 17 | Confirmation + ASN (wave 20/28) |
| OMGN — Set up stock transport order (delivery type, checking rule per supplying plant) | T161V, T161W | STO with/without delivery, one-step/two-step | PARTIAL-16 | 16 | Transfer order gap |
| Subcontracting settings (SC vendor stock types) | T156* | — | COVERED-21 | 21 | — |
| Vendor evaluation — weighting keys, criteria, scoring methods, points scores | T147*, T147A..T147M | Main/sub criteria, automatic (price, GR deviation, quality, QM) vs manual | COVERED-33 | 33 | Scorecard criteria |
| Source determination — source list requirement per plant, quota arrangement rules | T001W, TMQ1 | — | PARTIAL-22 | 22 | Quota partial |
| Message determination for purchasing (NACE EF/EV/EL/EA), condition tables, output types NEU, print programs, EDI partner profile | NAST, TNAPR, T685B | Medium (print, fax, EDI, email), timing | COVERED-17 | 17 | Output rules + wave 35 EDI; see cross-application |
| Default document types in requisition creation from MRP (OPPI / OMD) | — | — | COVERED-22 | 22 | Planned purchase order → requisition/PO conversion settings |
| Account assignment — automatic account determination (OBYC KBS) | T030 | — | COVERED-16 | 16 | — |
| OMEE — Contract document types, target value, release order documentation | T161 | — | COVERED-17 | 17 | — |
| Scheduling agreement — release creation profile, firm zone/trade-off zone, JIT indicator | T163M | — | PARTIAL-17 | 17 | SA release partial |
| Taxes in purchasing (tax code default per vendor/plant, MEK1 NAVS) | T007A | — | COVERED-13 | 13 | Tax determination on PO |
| Incoterms / terms of delivery (OVIM) | TINC | — | COVERED-7 | 7 | Delivery terms |
| Purchasing value keys (reminder days, tolerances, order acknowledgment) | T405 | Under/over-delivery tolerances, reminders 1/2/3 | COVERED-17 | 17 | Item defaults for expediting and delivery tolerances |
| Material groups (OMSF) — see MM master data | T023 | — | COVERED-8 | 8 | Product categories |
| Authorization groups / field selection per activity (classic) | — | — | COVERED-2 | 2 | — |
| Flexible workflow for PR/PO/contract approval (S/4 SWDD_SCENARIO / Manage Workflows app) | SWF_* | Step conditions, agent rules | COVERED-4 | 4 | Workflow designer |
| Ariba Network integration (cXML, Cloud Integration Gateway) | — | — | PARTIAL-35 | 35 | cXML not listed among EDI formats (X12/EDIFACT/Peppol); partial |

---

## 13.13 MM-IM — Inventory Management & Physical Inventory

SAP context: material documents (MKPF/MSEG; S/4 MATDOC), goods movements via MIGO (receipts, issues, transfers, reversals, 100+ movement types), reservations (MB21), stock overview (MMBE), stock types (unrestricted, quality, blocked, in transit, consignment, subcontractor, sales order stock, project stock), physical inventory (MI01–MI20, cycle counting MICN), period close (MMPV). Passage: wave 16 inventory ledger, wave 18 receiving, wave 21 production movements, wave 28 shipping movements, wave 31 warehouse detail.

### 13.13.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| MM01 / MM02 / MM03 (IM views: general plant, storage, accounting) | Material master — plant/storage/valuation views | Item per site/warehouse | COVERED-8 | 8 | Released product per legal entity with site/warehouse defaults; item model group (costing, negative stock) |
| MIGO | Goods movement (GR for PO/order/other, GI, transfer posting, place in storage, remove from storage, return delivery, subsequent delivery/adjustment, cancel, display) | Universal movement UI | COVERED-16 | 16 | Inventory journals (movement/transfer/adjustment) + receiving (wave 18) + production issues/receipts (wave 21) |
| MB1A / MB1B / MB1C (classic) | Goods issue / transfer posting / other receipt | — | SUPERSEDED-16 | 16 | Journals by type |
| MB01 / MB0A / MB31 (classic) | GR for PO / GR for PO unknown / GR for production order | — | SUPERSEDED-18 | 18 | Receiving (wave 18) / RAF (wave 21) |
| MB11 (classic) | Goods movement (general) | — | SUPERSEDED-16 | 16 | — |
| MB02 / MB03 | Change (texts) / display material document | — | COVERED-16 | 16 | Inventory transaction inquiry |
| MBST / MBSU | Cancel material document / place in storage for mat doc | Reversal | COVERED-16 | 16 | Reversal of inventory transaction with linked negating movement |
| MB21 / MB22 / MB23 / MB24 / MB25 | Reservation create/change/display/list by material/by account assignment | Manual reservations | COVERED-16 | 16 | Manual/auto reservations |
| MBVR | Manage reservations (delete/close) | — | COVERED-16 | 16 | — |
| MB90 | Output of material documents (GR slip, labels) | — | COVERED-32 | 32 | Label printing (ZPL) via print service; GR slips via document designer (wave 46) |
| MMBE | Stock overview (by plant/SLoc/batch/special stock) | On-hand inquiry | COVERED-16 | 16 | On-hand by dimension |
| MB52 | List warehouse stocks on hand | — | COVERED-16 | 16 | — |
| MB53 / MB54 | Plant availability / consignment stock | — | PARTIAL-16 | 16 | Vendor consignment gap |
| MB58 | Consignment and returnable packaging stock at customer | Customer consignment | PARTIAL-27 | 27 | Customer consignment (fill-up/issue/pick-up/return orders) not named in SD/inventory waves — partial; needs "stock at customer" ownership dimension |
| MBLB | Stocks at subcontractor | — | COVERED-21 | 21 | — |
| MMSC | Collective entry of storage locations (extend material) | — | COVERED-8 | 8 | Release to warehouse mass |
| MI01 / MI02 / MI03 | Physical inventory document create/change/display | Count documents | COVERED-16 | 16 | Count sheets |
| MI04 / MI05 / MI06 | Enter count / change count / display count | — | COVERED-16 | 16 | Count entry (mobile in wave 32) |
| MI07 / MI08 / MI10 | Post differences / post with count / post document without reference | Variance posting | COVERED-16 | 16 | Variance posting to GL |
| MI09 | Enter count without reference document | — | COVERED-16 | 16 | — |
| MI11 | Recount | — | COVERED-16 | 16 | — |
| MI20 | Difference list | — | COVERED-16 | 16 | — |
| MI21 / MI22 / MI23 / MI24 | Print PI documents / display PI docs for material / display PI data for material / difference list | — | COVERED-16 | 16 | — |
| MI31 / MI32 / MI33 / MI34 / MI35 / MI37 / MI38 / MI39 / MI40 | Batch create PI documents (all stocks / special stocks / enter count via batch input / post differences via batch) | Mass PI | COVERED-16 | 16 | Cycle counting plans + mass count generation |
| MICN | Create PI documents for cycle counting | ABC-based cycle counting | COVERED-16 | 16 | Cycle counting plans (ABC classification MIBC) |
| MIBC | ABC analysis for cycle counting (set CC indicator) | — | COVERED-16 | 16 | — |
| MIK1 / MIQ1 / MIM1 | PI for vendor consignment / project stock / returnable transport packaging | Special stock PI | PARTIAL-16 | 16 | Consignment/RTP gaps |
| MIE1 / MIW1 / MIV1 | PI for sales order stock / customer consignment / returnable packaging at customer | — | PARTIAL-27 | 27 | Sales-order stock & customer consignment partial |
| MIS1 / MIS2 (inventory sampling) | Inventory sampling (statistical) | — | GAP | propose 16 | Statistical inventory sampling (count a sample, extrapolate) — rarely used but a recognized GAAP-compatible method; low priority |
| OMJJ (movement types) — see customizing | — | — | COVERED-16 | 16 | — |
| Special stock: sales order stock (E), project stock (Q) | Stock segregated by SO/WBS | — | PARTIAL-16 | 16 | Sales-order/project-specific stock (valuated/non-valuated) not explicitly named as an inventory dimension — partial; needed for MTO/ETO manufacturers (waves 21/27/41) |
| Stock in transit (MB5T; S/4 stock in transit for STO) | In-transit stock | — | PARTIAL-16 | 16 | Transfer order gap |
| Returnable transport packaging (M, V) | RTP at vendor/customer | — | GAP | propose 28 | Returnable packaging (pallets, crates) tracking at customers/vendors not named; propose wave 28 (packaging/HU) option |
| Goods receipt blocked stock (103/105) | GR to blocked, release | — | COVERED-18 | 18 | Arrival/registration vs product receipt posting (two-step receiving) |
| Fiori: Post Goods Movement / Post Goods Receipt for PO / Transfer Stock – In Plant / Cross-Plant / Stock – Single Material / Multiple Materials / Manage Stock | S/4 apps | — | COVERED-16 | 16 | — |
| Fiori: Manage Physical Inventory Documents / Process Physical Inventory Count / Overview of Count Results | — | — | COVERED-16 | 16 | — |
| Fiori: Manage Material Documents / Material Documents Overview | — | — | COVERED-16 | 16 | — |

### 13.13.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| MB51 | Material document list (by material/plant/movement type/date) | COVERED-16 | 16 | Inventory transactions inquiry |
| MB52 | Warehouse stocks on hand (with values) | COVERED-16 | 16 | — |
| MB5B | Stocks on posting date (period opening/closing, movements) | COVERED-16 | 16 | On-hand as-of date from immutable transactions |
| MB5L | List of stock values: balances (vs GL) | COVERED-20 | 20 | Inventory value report reconciling to GL |
| MB5T | Stock in transit (cross-company/plant) | PARTIAL-16 | 16 | Transfer order gap |
| MB5M | Expiration date list (shelf life) | COVERED-24 | 24 | Shelf life / expiry (batch attributes) |
| MB5W | List of stock values (period) | COVERED-20 | 20 | — |
| MB5S | GR/IR balances per PO | COVERED-18 | 18 | — |
| MB5C | Pick-up list for batch where-used | COVERED-24 | 24 | Batch traceability |
| MB54 / MB58 / MBLB | Consignment / customer consignment / subcontractor stock | PARTIAL-16 | 16 | Consignment gaps |
| MBBS | Valuated special stock (sales order/project) | PARTIAL-16 | 16 | Special stock gap |
| MBSM | Cancelled material documents | COVERED-16 | 16 | — |
| MMBE | Stock overview | COVERED-16 | 16 | — |
| MMBE_OLD | Legacy stock overview | SUPERSEDED-16 | 16 | — |
| MC.1 / MC.2 / MC.3 / MC.4 / MC.5 / MC.6 / MC.7 / MC.8 / MC.9 / MC.A / MC.B / MC.C / MC.D / MC.E | INVCO stock analyses: plant / storage location / material / MRP controller / business area / by receipts/issues/stock value/ranges of coverage/inventory turnover/slow-moving | COVERED-46 | 46 | Inventory analytics (turns, coverage, slow-moving) in semantic layer; MC.9 current stock/values also in wave 20 value report |
| MC50 / MC46 / MC44 / MC45 | Dead stock / slow-moving items / inventory turnover / usage values | COVERED-46 | 46 | — |
| MC48 / MC49 | Stock value: current / mean stock value | COVERED-46 | 46 | — |
| MMPV (log) / MMRV | Period close status | COVERED-14 | 14 | — |
| MI20 / MI24 | PI difference list | COVERED-16 | 16 | — |
| S_P00_07000139 / S_P00_07000140 | Stock card and inventory list (country-specific) | COVERED-16 | 16 | — |
| MB5OA / MB5U | Stock in open PO / consumption | COVERED-17 | 17 | — |
| MBGR | Material documents by reason for movement | COVERED-16 | 16 | Reason codes |
| Fiori: Stock – Multiple Materials / Inventory Turnover Analysis / Slow or Non-Moving Materials / Dead Stock Analysis / Goods Movement Analysis / Inventory Aging | Analytical apps | COVERED-46 | 46 | — |
| Fiori: Material Documents Overview / Reservations overview | — | COVERED-16 | 16 | — |

### 13.13.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| MMPV | Close period for material master records | COVERED-14 | 14 | Period control (inventory) |
| MMRV | Allow posting to previous period | COVERED-14 | 14 | Period status on-hold/open per module |
| MI31 → MI21 → MI04 → MI07 (PI cycle) | Annual physical inventory | COVERED-16 | 16 | — |
| MICN (scheduled) | Cycle count document generation | COVERED-16 | 16 | — |
| MIBC (periodic) | ABC re-classification | COVERED-16 | 16 | — |
| MB5K | Stock consistency check (MARD vs MSEG) | SUPERSEDED-16 | 16 | On-hand projection rebuild-and-compare proof |
| MBVR | Reservation cleanup | COVERED-16 | 16 | — |
| MRKO | Consignment & pipeline settlement | PARTIAL-16 | 16 | Consignment gap |
| MB5L (month-end) | Inventory to GL reconciliation | COVERED-20 | 20 | Daily automated |
| CKMLCP / MR21 (see CO-PC) | — | COVERED-20 | 20 | — |
| SARA MM_MATBEL | Archiving material documents | COVERED-48 | 48 | Retention + lakehouse history (wave 46) |
| Subcontracting component provision (541) via ME2O | — | COVERED-21 | 21 | — |
| Fiori: Schedule Physical Inventory Document creation | — | COVERED-4 | 4 | — |

### 13.13.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OX10 / OX09 — Plants / storage locations | T001W, T001L | Address, factory calendar, purchasing org assignment | COVERED-6 | 6 | Sites, warehouses |
| OX18 / OX14 — Assign plant to company code / valuation area setting (plant vs company code) | T001K | Valuation level | COVERED-6 | 6 | Valuation at site level (default) — confirm valuation level choice |
| OMJJ — Movement types: copy, define, allowed fields, update control (quantity/value), reversal movement, reason codes, account grouping (GBB modifiers), screen layout, allowed transactions | T156, T156B, T156S, T156X, T156M, T156W, T158B | 100+ movement types; auto-created vs custom | SUPERSEDED-16 | 16 | Inventory transaction types are a fixed typed set (receipt, issue, transfer, adjustment, count, RAF, consumption …) with posting profiles per item model group; custom "movement types" become journal types with configurable posting profiles |
| OMBS — Reason for movement | T157D | Reason codes per movement type | COVERED-16 | 16 | Reason codes on journals |
| OMBT — Number ranges for material documents / PI documents / reservations | NRIV | — | COVERED-5 | 5 | — |
| OMJ1 — Allow negative stocks (per valuation area / storage location) | T001K, T001L | — | COVERED-16 | 16 | Negative-inventory policy per item model group/warehouse |
| OMJX / OMJR — Field selection for MIGO / print control | T158, T159P | — | COVERED-3 | 3 | Metadata |
| OMJ5 — Shelf life expiration date check (per plant / movement type) | T159L, T156 | Minimum remaining shelf life check at GR | COVERED-24 | 24 | Shelf life checks on receipt |
| OMC0 — Tolerance limits for GR (under/over delivery, B1/B2 keys) | T169G | B1/B2 keys | COVERED-18 | 18 | Receipt tolerances |
| OMBW — Screen layout per movement type (field selection) | T156B | — | COVERED-3 | 3 | — |
| OMCJ / OMCH — Default values for goods receipt (stock type default: quality/blocked), GR slip print | T158, T159 | — | COVERED-18 | 18 | Receive-to-quarantine by item/vendor (wave 33 QM triggers) |
| OMJD — Automatic storage location creation on movements | T001L | — | COVERED-16 | 16 | — |
| Stock types / GR blocked stock settings | — | — | COVERED-16 | 16 | Inventory status (available, quarantine, blocked, in-transit) |
| Batch / serial checks in IM (see batch section) | — | — | COVERED-16 | 16 | — |
| OMWB / OBYC — Account determination (valuation grouping code, valuation class, account modifier, transaction key) | T030, T025, T001K, T030W | BSX inventory, GBB offsets (VBR consumption, AUF order, VAX/VAY COGS, ZOB other, INV PI differences), PRD price differences, WRX GR/IR, UMB revaluation, FRL external processing, BSV subcontracting, KDM FX | COVERED-16 | 16 | Inventory posting profiles by item model group × site × transaction type |
| OMWD / OMWC / OMSK — Valuation grouping code / split valuation (valuation categories/types) / valuation classes & account category reference | T001K, T025, T025K, T149* | Split valuation by origin/batch (valuation type) | PARTIAL-20 | 20 | Split valuation (different values per valuation type, e.g. new vs refurbished, domestic vs import) not named — partial; batch-level valuation needed for process industries |
| OMW0 / OMW1 — Price control (standard/moving average) per material type / valuation area | T134, T001K | V/S, price control mandatory | COVERED-20 | 20 | Costing method on item model group |
| OMS2 — Material types (quantity/value update per valuation area) | T134, T134M | Quantity/value updating, allowed views, number range | COVERED-8 | 8 | Product types (stocked/non-stocked/service) + item model group flags (physical/financial posting) |
| OMSL / OMSF — Material number format / material groups | TMCNM, T023 | Length, leading zeros, lexicographic | COVERED-8 | 8 | Product number sequences; categories |
| OMSR / OMS9 — Field selection material master (field references, screen sequences) | T130F, T133* | — | COVERED-3 | 3 | Metadata |
| OMT3B / OMT3E — Material master screen sequence / data screens | T133K | — | SUPERSEDED-5 | 5 | Metadata-driven forms |
| OMSY — Fiscal year/period per company code (material periods) | MARV | Current period, allow previous | COVERED-14 | 14 | — |
| OMBG / OMJN — Physical inventory settings: default values, tolerances per user group (posting differences max), cycle counting indicators (OMCO) and intervals, inventory sampling | T159*, T043I, T159C | ABC thresholds, count intervals, float days | COVERED-16 | 16 | Cycle counting plans; variance posting limits via OPA |
| OMJ2 / OMJ3 — Print settings for GR/GI slips, labels | T159*, T159P | — | COVERED-46 | 46 | Document designer / label printing (wave 32) |
| OMCQ — System messages (warning/error) for inventory management | T100C | Make messages errors/warnings | SUPERSEDED-3 | 3 | Validation rules with severity in metadata |
| OMJA — Movement type statistics relevance (LIS) | T156S | — | SUPERSEDED-46 | 46 | Lakehouse |
| Special stock settings (consignment price determination, pipeline) | — | — | PARTIAL-16 | 16 | Consignment gap |
| OMCT — batch level (see batch) | — | — | COVERED-8 | 8 | — |
| OMCP — Dynamic availability check per movement type | T156, T441V | Check rule per movement | COVERED-16 | 16 | ATP/availability policy on issues (no negative unless allowed) |
| Stock determination (OSPX / OSPW) strategies for issues (consignment vs own, SLoc priority) | — | — | PARTIAL-16 | 16 | Picking policies FEFO/FIFO named; ownership-based stock determination partial (consignment) |

---

## 13.14 MM-IV — Logistics Invoice Verification

SAP context: MIRO (PO-based, delivery-note based, GR-based IV, planned/unplanned delivery costs, subsequent debit/credit), parking (MIR7), blocked invoices (MRBR; price/quantity/date/stochastic blocks), tolerance keys (OMR6), GR/IR clearing maintenance (MR11), ERS (MRRL), consignment settlement (MRKO), invoicing plans (MRIS), revaluation (MRNB), duplicate invoice check, OBYC WRX/PRD/KDM. Passage: wave 18 (capture, 2/3-way match, tolerances, exceptions, GR/IR, ERS), wave 19 (AP open items), wave 34 (freight invoices/landed cost).

### 13.14.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| MIRO | Enter incoming invoice (invoice, credit memo, subsequent debit/credit; PO reference, delivery note, bill of lading, service entry sheet, vendor) | PO-based invoice | COVERED-18 | 18 | Vendor invoice capture with PO/receipt reference; subsequent debit/credit = price adjustment invoice |
| MIR7 / MIR4 | Park invoice / display invoice document | Draft | COVERED-18 | 18 | Draft + approval |
| MIR6 | Invoice overview (parked, held, blocked, posted) | Worklist | COVERED-18 | 18 | AP invoice workspace |
| MR8M | Cancel invoice document | Reversal | COVERED-18 | 18 | Reversal with inventory/GR-IR re-open |
| MRBR | Release blocked invoices (price/qty/date/manual block; automatic release) | Exception resolution | COVERED-18 | 18 | Match exception workflow and release |
| MR11 / MR11SHOW | GR/IR clearing account maintenance (write off small balances) / display | GR/IR cleanup | COVERED-18 | 18 | GR/IR reconciliation report + write-off |
| MRRL | Evaluated receipt settlement | Self-billing | COVERED-18 | 18 | ERS |
| MRKO | Consignment and pipeline settlement | — | PARTIAL-16 | 16 | Consignment gap |
| MRIS | Invoicing plan settlement (periodic/partial invoicing plans on PO) | Recurring PO invoicing (rent, leases) | PARTIAL-17 | 17 | Invoicing plans on POs (periodic settlement without GR) not named — partial; relevant to leases/services; propose wave 17 PO line "invoicing plan" |
| MRNB | Revaluation (retroactive price change on invoiced items) | Retro-billing by vendor | PARTIAL-17 | 17 | Retro re-pricing partial (MEI1 row) |
| MRDC | Automatic delivery cost settlement (freight, customs via planned delivery costs) | Planned delivery costs settlement | COVERED-34 | 34 | Freight reconciliation + landed cost |
| MIRA | Enter invoice for background verification | Batch invoice entry | COVERED-18 | 18 | Bulk capture/OCR queue |
| MR01 / MR02 / MR03 / MR41 (classic) | Classic invoice verification | — | SUPERSEDED-18 | 18 | — |
| MR90 | Output messages for invoices (ERS printouts) | — | COVERED-18 | 18 | ERS self-billing document output |
| MIR5 | Display list of invoice documents | — | COVERED-18 | 18 | — |
| Invoice reduction (complaint document) | Invoice reduction with vendor complaint letter | — | COVERED-18 | 18 | Short-pay / invoice reduction with dispute to vendor — treat covered under exception workflow |
| GR-based IV flag (PO item) | Match per GR document | — | COVERED-18 | 18 | 3-way at receipt-document level |
| Unplanned delivery costs | Distribute across items or post to separate account | — | COVERED-34 | 34 | Landed cost allocation |
| Duplicate invoice check (vendor flag + OMRDC) | Prevent duplicates (ref no, date, amount) | — | COVERED-18 | 18 | Duplicate detection rules |
| Withholding tax on MIRO | — | — | COVERED-13 | 13 | — |
| Fiori: Create Supplier Invoice / Manage Supplier Invoices / Release Blocked Invoices / Clear GR/IR Clearing Account / Supplier Invoices List | S/4 apps | — | COVERED-18 | 18 | — |
| Fiori: Upload Supplier Invoices / Supplier Invoice from OCR (Central Invoice Management, Business Network) | — | — | COVERED-18 | 18 | OCR adapter; e-invoice (wave 35) |

### 13.14.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| MIR5 / MIR6 | Invoice document list / overview | COVERED-18 | 18 | — |
| MB5S | GR/IR balances per PO | COVERED-18 | 18 | — |
| MR11SHOW | GR/IR maintenance documents | COVERED-18 | 18 | — |
| MRBR (list) | Blocked invoices list | COVERED-18 | 18 | — |
| Invoice register (AP) | Invoice register | COVERED-18 | 18 | — |
| ME2L (RECHNUNG) | Open invoice quantities by PO | COVERED-18 | 18 | — |
| F.19 | GR/IR analysis | COVERED-18 | 18 | — |
| PURCHIS invoice values | Invoice value analysis | COVERED-46 | 46 | — |
| Fiori: Invoice Cycle Time / Touchless invoice rate KPIs | — | COVERED-46 | 46 | — |

### 13.14.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| MRBR (automatic release in batch) | Release blocked invoices automatically when block reasons no longer apply | COVERED-18 | 18 | Re-evaluate match status on new receipts |
| MRRL (scheduled) | ERS run | COVERED-18 | 18 | — |
| MRKO (scheduled) | Consignment settlement | PARTIAL-16 | 16 | — |
| MRIS (scheduled) | Invoicing plan run | PARTIAL-17 | 17 | — |
| MRNB (scheduled) | Revaluation run | PARTIAL-17 | 17 | — |
| MRDC (scheduled) | Delivery cost settlement | COVERED-34 | 34 | — |
| MR11 (periodic) | GR/IR clearing | COVERED-18 | 18 | — |
| F.19 (periodic regroup) | GR/IR balance sheet regrouping | COVERED-18 | 18 | — |
| SARA MM_REBEL | Archive invoice documents | COVERED-48 | 48 | — |
| Fiori: Schedule Supplier Invoice Jobs | — | COVERED-4 | 4 | — |

### 13.14.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OMR6 — Tolerance limits (keys AN, AP, BD, BR, BW, DQ, DW, KW, LA, LD, PP, PS, ST, VP) per company code | T169G | Absolute/percent upper/lower, check/no check; price variance (PP), quantity (DQ), date (ST), blanket PO (LA/LD), order price unit (PS), moving average (VP) | COVERED-18 | 18 | Price/quantity/total/date tolerances per legal entity and vendor group |
| OMR4 / OMRJ — Document types for invoice verification (RE, KG) / number ranges | T003, T169F, NRIV | — | COVERED-11 | 11 | — |
| OMRDC — Duplicate invoice check settings | T169P | Check company code, reference, date | COVERED-18 | 18 | — |
| OMRM — Activate direct posting to G/L and material accounts in MIRO | T169P | — | COVERED-18 | 18 | Non-PO lines on PO invoices |
| Item amount check (manual block by amount) | T169P | — | COVERED-18 | 18 | Approval threshold |
| OMRX — Stochastic block (random sampling thresholds) | T169P | — | GAP | propose 18 | Random audit sampling of invoices — simple to add to wave 18 exception rules; register as gap |
| OMRN — Maintain default values for tax codes (MIRO) | T169V | — | COVERED-13 | 13 | — |
| Vendor-specific tolerances (tolerance groups for vendors: small differences, automatic acceptance) | T169*, T043* | — | COVERED-18 | 18 | Vendor-group tolerances |
| OBYC — WRX, PRD, KDM, DIF for IV | T030 | — | COVERED-18 | 18 | — |
| ERS settings: vendor flag, document type, tax code default, message type ERS | LFM1, T169* | — | COVERED-18 | 18 | — |
| Invoice reduction: document types, reason codes | T169* | — | COVERED-18 | 18 | — |
| Define payment block reasons via IV | T008 | — | COVERED-19 | 19 | — |
| Invoicing plan types (periodic/partial) | T163* | — | PARTIAL-17 | 17 | Invoicing plan gap |
| Delivery costs: condition types FRA1/FRB1/ZOTH, provision accounts (FR1/FR3 keys) | T030, T685 | — | COVERED-34 | 34 | Landed cost charge codes with accrual accounts |
| Flexible workflow for supplier invoice approval (S/4) | SWF_* | — | COVERED-4 | 4 | — |
| Default values for IV (unplanned delivery costs distribution) | T169P | — | COVERED-34 | 34 | — |
| GR/IR regrouping accounts | T030 | — | COVERED-18 | 18 | — |

---

## 13.15 MM-SRV — External Services Management (incl. S/4 Lean Services)

SAP context: service master (AC03), model service specifications (ML10), standard service catalog (SSC), service POs (item category D with service lines and value limits), service entry sheets (ML81N) with acceptance → GR, service conditions (ML33/ML39), invoice via MIRO referencing SES. S/4 "lean services" simplifies: service items as product type Service, no SES (Fiori Manage Service Entry Sheets – Lean). Passage: non-stock service products (wave 8), PO (wave 17), service confirmation/receipt (wave 18) — **no explicit service entry sheet**, so mostly PARTIAL.

### 13.15.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| AC01 / AC02 / AC03 / AC04 / AC06 | Service master create/change/display/list/changes | Service master (activity numbers, UoM, valuation class) | PARTIAL-8 | 8 | Product of type "service" is covered; a dedicated service master with service categories, purchasing text, standard service catalog is partial — add "service product type" attributes to wave 8 |
| ML10 / ML11 / ML12 | Model service specifications create/change/display | Reusable service spec templates | GAP | propose 17 | Service specification templates (hierarchical outlines of services with quantities) not named; propose "PO line templates" in wave 17 |
| Standard service catalog (SSC) | Standard texts for services | — | GAP | propose 17 | Same |
| ME21N (item cat D) with service lines, value limits, contract limits | Service PO with hierarchical service outline and limits | — | PARTIAL-17 | 17 | Limit/blanket POs named; service outlines (hierarchy of service lines under one PO item) and contract-limit references are partial |
| ML81N / ML82 / ML83 / ML84 / ML85 | Service entry sheet create-maintain / display / list / accept-revoke / collective release | Service performance confirmation → GR | PARTIAL-18 | 18 | Receiving covers "product receipt"; a *service entry sheet* (lines of performed services with quantities/prices, acceptance workflow, partial entries, unplanned services within limits) is not named — partial; add "service receipt/entry sheet with acceptance" to wave 18 |
| ML33 / ML34 / ML35 / ML36 / ML39 / ML40 / ML41 / ML45 / ML46 / ML47 / ML48 | Service conditions: vendor-level / service-level / vendor+plant | Service pricing | PARTIAL-17 | 17 | Vendor price lists for service products via pricing engine — partial until service products confirmed |
| MSRV1 … MSRV6 | Service lists: by service / by PO / by SES / by vendor / by contract / entry sheets | — | PARTIAL-18 | 18 | — |
| MIRO with SES reference | Invoice for services (SES-based) | — | PARTIAL-18 | 18 | Match to service receipt |
| ML91 / ML92 / ML93 | Service entry sheet archiving / lists | — | PARTIAL-18 | 18 | — |
| Fiori: Manage Service Entry Sheets – Lean Services / Create Service Entry Sheet | S/4 lean services | — | PARTIAL-18 | 18 | Same |
| Fiori: Manage Purchase Orders (service items), Manage Supplier Quotations for services | — | — | PARTIAL-17 | 17 | — |
| Supplier self-service SES (SAP Business Network / Fieldglass) | Supplier enters SES / timesheets | — | PARTIAL-20 | 20 | Supplier portal: add service confirmation/timesheet entry (contingent labor) — partial |
| Fieldglass (contingent workforce, SOW) | External workforce management | — | OUT-OF-SCOPE | — | Contingent workforce VMS beyond v1; contractor records in HR (wave 42) + service POs suffice |

### 13.15.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| MSRV1 … MSRV6 | Service lists | PARTIAL-18 | 18 | — |
| ME2S | Services per purchase order | PARTIAL-18 | 18 | — |
| MSRV3 (entry sheets) | SES list | PARTIAL-18 | 18 | — |
| ML83 / ML84 | SES acceptance list | PARTIAL-18 | 18 | — |
| PURCHIS services analysis | Service purchasing analysis | COVERED-46 | 46 | — |

### 13.15.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| ML85 (collective release of SES) | Mass acceptance | PARTIAL-18 | 18 | — |
| MRRL for services | ERS for SES | PARTIAL-18 | 18 | — |
| SES archiving (SARA MM_SRVMAS) | — | COVERED-48 | 48 | — |

### 13.15.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Service master: number ranges, service categories, organizational status, field selection | ASMD | — | PARTIAL-8 | 8 | — |
| Service specifications: screen layout, number ranges for SES, release procedure for SES (classification) | ESSR | — | PARTIAL-18 | 18 | SES approval via workflow designer |
| Service conditions: calculation schema MS0000, condition types PRS | T685 | — | PARTIAL-17 | 17 | — |
| Default values for SES acceptance (immediate GR, account assignment) | — | — | PARTIAL-18 | 18 | — |
| Source determination for services (contracts) | — | — | PARTIAL-17 | 17 | — |
| Lean services: activate, product type service, service entry sheet (lean) settings | — | — | PARTIAL-18 | 18 | — |

---
## 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management)

SAP context: classic WM — warehouse number, storage types/sections/bins (LS01N), transfer requirements (LB01), transfer orders (LT01/LT03/LT06/LT10), confirmation (LT12), put-away/picking strategies, replenishment (LP21/LX16?), WM physical inventory (LI01N–LI21), RF (LM00–LM77), HU management (HU02), storage unit management. EWM (decentral or embedded in S/4) — warehouse product, storage types/bins, warehouse tasks/orders, waves, POSC/LOSC process steps, packing specifications, labor management, yard, slotting, MFS, RF framework (/SCWM/RFUI), warehouse monitor (/SCWM/MON). Passage: wave 31 (locations, LPs/HUs, directives, work templates, waves, replenishment, load planning, dock scheduling, slotting, KPIs), wave 32 (mobile/RF, voice, RFID, labels), wave 16 (counting), wave 28 (basic pick/pack/ship).

### 13.16.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| LS01N / LS02N / LS03N | Storage bin create/change/display | Bins with type, section, capacity, coordinates | COVERED-31 | 31 | Location model: zones/aisles/racks/bins with capacity/profile |
| LS05 / LS10 | Generate storage bins automatically / define bin structure | Mass bin creation | COVERED-31 | 31 | Bin generator |
| LS06 / LS07 / LS08 | Block/unblock bins (by bin / by storage type / list of blocked bins) | Bin blocking | COVERED-31 | 31 | Location status |
| LS11 / LS12 | Change several bins / block storage type | — | COVERED-31 | 31 | — |
| LS24 / LS25 / LS26 | Stock per material (bin stock) / per bin / stock overview (warehouse) | Bin-level inquiry | COVERED-31 | 31 | On-hand by location/LP |
| LS22 / LS23 / LS27 / LS28 | Change / display quants; quants by storage unit | Quant maintenance | COVERED-31 | 31 | Inventory by location/LP/batch |
| LS32 / LS33 | Change / display storage unit | SU (handling unit) | COVERED-31 | 31 | License plates |
| LB01 / LB02 / LB03 / LB10 / LB11 / LB12 / LB13 | Transfer requirement create/change/display/lists (by storage type, material, movement) | TR = open work demand | SUPERSEDED-31 | 31 | Work demand generated directly from source documents (ASN, order release, replenishment) |
| LT01 / LT02 / LT03 / LT04 / LT05 / LT06 / LT07 / LT08 / LT09 / LT10 | Transfer order: create (manual) / from TR / for delivery / from TR / for posting change / for material document / for mixed SU / by storage type / stock transfer | Work (tasks) creation | COVERED-31 | 31 | Work templates generate tasks (put-away, pick, move, replenish) |
| LT11 / LT12 / LT13 / LT14 / LT15 / LT16 | Confirm TO item / TO / SU / by picking list / cancel TO / cancel TO item | Work confirmation | COVERED-31 | 31 | Work execution (confirm, exceptions, short picks) |
| LT21 / LT22 / LT23 / LT24 / LT25 / LT26 / LT27 / LT28 | Display TO / list by storage type / by number / by material / by SU / by bin / by delivery / archive | Work inquiry | COVERED-31 | 31 | Work list pages |
| LT31 / LT32 | Print TO / TO group | Pick lists | COVERED-32 | 32 | Printing + mobile |
| LT41 / LT42 / LT43 / LT44 / LT45 | Create / display / confirm TO group (wave) for delivery groups | Group picking (waves, 2-step picking) | COVERED-31 | 31 | Wave templates; cluster/zone picking |
| LT0A / LT0B / LT0C / LT0D / LT0E / LT0F / LT0G / LT0H / LT0I / LT0J / LT0R / LT0S | TOs for: inbound delivery / outbound delivery / multiple deliveries / return to stock / stock pickup / 2-step picking | — | COVERED-31 | 31 | — |
| LP10 / LP11 / LP12 / LP21 / LP22 | Replenishment: fixed bins (planned) / for storage type / for production / replenishment of fixed bins / replenishment for storage types | Replenishment | COVERED-31 | 31 | Replenishment (min/max, demand, wave) |
| LX LD10 / LD11 / LD12 / LD13 | Picking for delivery monitor / collective TO creation for deliveries | — | COVERED-31 | 31 | — |
| LQ01 / LQ02 | Transfer posting in WM (stock category change: Q→unrestricted, blocked) | Status change | COVERED-31 | 31 | Inventory status changes per location |
| LU01 / LU02 / LU03 / LU04 | Posting change notices | — | SUPERSEDED-31 | 31 | Status changes are direct |
| LI01N / LI02N / LI03N | WM physical inventory document create/change/display | Bin counts | COVERED-16 | 16 | Cycle counting at location level (wave 31 adds bins) |
| LI04 / LI05 / LI06 | Print / list PI documents | — | COVERED-16 | 16 | — |
| LI11N / LI12N / LI13N / LI14 | Enter count / change count / display / start recount | — | COVERED-32 | 32 | Mobile count |
| LI20 / LI21 | Clear differences WM / clear differences in IM | Variance posting (two-step: WM then IM) | SUPERSEDED-16 | 16 | Single inventory ledger: one variance posting |
| LX15 / LX16 / LX17 / LX18 | Continuous inventory / zero-stock check / annual inventory evaluation / stock placement counting | PI variants | COVERED-16 | 16 | Cycle counting policies incl. zero-count trigger |
| LICC / LX26 | Cycle counting per quant / continuous inventory | — | COVERED-16 | 16 | — |
| HU02 / HU03 / HUMO | Create/change HU / display HU / HU monitor | Handling units (nested) | COVERED-28 | 28 | Containers/HU with SSCC; nested LPs (wave 31) |
| HUPAST / HU04 / HUIBD | Pack in HU monitor; HU for inbound delivery | — | COVERED-31 | 31 | — |
| VL01N/VL02N packing (see LE-SHP) | Packing in delivery | — | COVERED-28 | 28 | — |
| LM00 → LM01 … LM77 (RF menus: putaway, picking, inquiry, counting, load/unload, pack) | RF transactions (ITSmobile) | RF execution | COVERED-32 | 32 | Mobile app flows (receive, put-away, pick, pack, move, count, inquiry) |
| LRF1 / LRF2 | RF monitor / RF queue assignment | RF supervision | COVERED-32 | 32 | Device/session management; task queues (wave 31 priorities) |
| LRFMD | RF device master | — | COVERED-32 | 32 | Device registry |
| LX01 … LX47 (lists — see reports) | — | — | COVERED-31 | 31 | — |
| LXDCA / cross-docking (LXDCK) | Planned cross-docking | — | GAP | propose 31 | Cross-docking (inbound → outbound without put-away, planned/opportunistic) not named in wave 31 — propose directive type "cross-dock" |
| Dynamic cycle counting / task interleaving | — | — | COVERED-31 | 31 | Work assignment by priority; interleaving — confirm |
| EWM: /SCWM/MON | Warehouse management monitor (documents, stock, resources, exceptions) | Supervisor cockpit | COVERED-31 | 31 | Warehouse workspace & KPIs |
| EWM: /SCWM/PRDI / /SCWM/PRDO | Maintain inbound / outbound delivery (warehouse request) | Delivery documents in EWM | SUPERSEDED-31 | 31 | Single delivery/receipt documents (no ERP↔EWM replication) |
| EWM: /SCWM/TODLV_I / /SCWM/TODLV_O / /SCWM/TO_CONF | Create warehouse tasks for inbound/outbound delivery / confirm | — | COVERED-31 | 31 | — |
| EWM: /SCWM/ADPROD / /SCWM/ADGI | Goods issue/receipt for production (staging) | Production staging | COVERED-31 | 31 | Staging for production orders (wave 21 picking lists + wave 31 directives) |
| EWM: /SCWM/WAVE / /SCWM/WAVETMP | Wave management: create/release waves, wave templates | Wave picking | COVERED-31 | 31 | Wave templates |
| EWM: /SCWM/PACK / /SCWM/PACKSPEC / /SCWM/PSSTAGE | Packing work center / packaging specifications | Packing specs (how to pack product into HU) | PARTIAL-31 | 31 | HU/packing covered; *packaging specifications* (rules: product → packaging material, quantities per level) only implied — partial; propose wave 28/31 "packaging rules" |
| EWM: /SCWM/VASEXEC / VAS orders | Value-added services (kitting, labeling) | — | GAP | propose 31 | VAS/kit-to-order in warehouse not named; propose wave 31 work type "VAS" and wave 24 kitting via lean BOM |
| EWM: /SCWM/RSRC / /SCWM/USER / /SCWM/QUEUE | Resources, users, queues | Resource management | COVERED-31 | 31 | Users/resources/queues |
| EWM: /SCWM/LM (labor management: engineered labor standards, planned vs actual, performance docs) | Labor management | — | PARTIAL-31 | 31 | Warehouse KPIs named; engineered labor standards, operator performance/incentive documents — partial |
| EWM: /SCWM/SLOT slotting & rearrangement | Slotting | — | COVERED-31 | 31 | Slotting analysis |
| EWM: /SCWM/YM_* yard management (yard bins, vehicles, TUs, checkpoints) | Yard management | — | PARTIAL-31 | 31 | Dock scheduling named; yard (trailer positions, moves, check-in/out) partial |
| EWM: /SCWM/TU, /SCWM/VEH | Transportation units / vehicles | — | PARTIAL-34 | 34 | Load/TU in wave 34 — partial for yard link |
| EWM: /SCWM/PI_PROCESS / /SCWM/PI_CREATE / /SCWM/PI_COUNT / /SCWM/DIFF_ANALYZER | Physical inventory in EWM + difference analyzer | — | COVERED-16 | 16 | — |
| EWM: /SCWM/QINSP Quality inspection engine (QIE) | Inspection in warehouse | — | COVERED-33 | 33 | Quality orders at receipt |
| EWM: /SCWM/RFUI | RF framework (menu, screens, logical transactions) | — | COVERED-32 | 32 | Configurable flow designer |
| EWM: /SCWM/MFS* (Material Flow System: PLC interface, conveyors, ASRS) | Automation integration | — | GAP | propose 32 | MFS/PLC integration (telegrams to conveyor/AS-RS) not named; wave 25 OPC-UA/MQTT edge is the natural base — propose wave 32 "automation adapter (ASRS/conveyor)" |
| EWM: /SCWM/DLVPPF / /SCWM/PPF | Post processing framework actions (print, msg) | — | COVERED-4 | 4 | Event-triggered actions |
| EWM: Catch weight, serial numbers in WM, batch in WM | — | — | COVERED-24 | 24 | Catch weight (wave 24), serial (wave 8) |
| EWM: Kit-to-stock / kit-to-order | — | — | GAP | propose 31 | See VAS |
| EWM: Dangerous goods / HazMat checks in warehouse (EHS) | — | — | GAP | propose 34 | Hazmat classification, storage compatibility, DG documents — not named; propose wave 34 (transport) + wave 8 attributes |
| Fiori: Warehouse KPIs, Warehouse Monitor apps, Outbound Delivery Order Overview, Process Warehouse Tasks | — | — | COVERED-31 | 31 | — |

### 13.16.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| LX01 | List of empty storage bins | COVERED-31 | 31 | — |
| LX02 | Stock list (bin stock by material) | COVERED-31 | 31 | — |
| LX03 | Bin status report | COVERED-31 | 31 | — |
| LX04 | Capacity used (storage type) | COVERED-31 | 31 | Capacity never exceeded (proof) |
| LX05 | Block bins in bulk storage | COVERED-31 | 31 | — |
| LX06 / LX07 | Fire-containment sections / check storage (inconsistencies) | PARTIAL-31 | 31 | Fire/hazard sections tie to hazmat gap |
| LX08 / LX09 | Accident regulations / overview of all TRs | COVERED-31 | 31 | — |
| LX10 / LX11 / LX12 / LX13 | Stock movements per storage type / document overview / document overview (SU) / analysis of differences | COVERED-31 | 31 | — |
| LX14 / LX15 / LX16 / LX17 | Material situation (ABC/movement frequency) / selection of PI by date / zero-stock check / list of inventory differences | COVERED-31 | 31 | ABC/velocity analysis for slotting |
| LX18 / LX19 / LX20 / LX21 | Statistics of inventory differences / inventory data takeover / generate IM inventory from WM / pick list for several TOs | COVERED-31 | 31 | — |
| LX22 / LX23 / LX24 / LX25 / LX26 / LX27 / LX29 | Inventory status / stock comparison IM–WM / hazardous material / inventory status / cycle counting / stock below minimum / fixed bin supervision | COVERED-31 | 31 | IM–WM comparison superseded (single ledger) |
| LX31 / LX32 / LX33 / LX34 | Analysis of print control / archived TOs / archived TRs / posting change notices | COVERED-31 | 31 | — |
| LX35 / LX36 / LX37 / LX38 / LX39 | Archived PI docs / archived PI history / linked objects / check report Customizing strategy K / evaluation of 2-step picking | COVERED-31 | 31 | — |
| LX40 / LX41 / LX42 / LX43 / LX44 / LX45 / LX46 / LX47 | Material master data in WM / bin status (SU) / stock in storage units / check consistency / HU inquiry / RF monitor-related / TO analysis / analysis of delays in delivery | COVERED-31 | 31 | — |
| LL01 | Warehouse activity monitor (critical TOs, TRs, deliveries, negative stock, posting changes) | COVERED-31 | 31 | Exceptions dashboard |
| LT22 … LT28 | TO lists | COVERED-31 | 31 | — |
| LS24 … LS26 | Bin stock | COVERED-31 | 31 | — |
| HUMO | HU monitor | COVERED-28 | 28 | — |
| MCL1 / MCL5 / MCL9 / MCLD (WM LIS) | Warehouse controlling: stock placement/removal, flow of quantities, material movements | COVERED-46 | 46 | — |
| EWM: /SCWM/MON node reports (stock, documents, resources, PI, labor) | — | COVERED-31 | 31 | — |
| EWM: /SCWM/LM_ labor reports (planned vs actual) | — | PARTIAL-31 | 31 | — |
| Fiori: Warehouse KPIs (dock-to-stock, order cycle time, pick accuracy) | — | COVERED-31 | 31 | — |

### 13.16.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| LP21 / LP22 (scheduled) | Replenishment runs | COVERED-31 | 31 | — |
| LX16 (scheduled) | Zero-stock check | COVERED-16 | 16 | — |
| LICC / LX26 (scheduled) | Cycle counting generation | COVERED-16 | 16 | — |
| LT0S / LT0R | 2-step picking: removal / allocation | COVERED-31 | 31 | — |
| LT41/LT42 (TO groups, scheduled waves) | Wave release | COVERED-31 | 31 | — |
| LX / LT15 cleanup / TO archiving (SARA LE_TRANSORD / RL_TA) | Archiving TOs | COVERED-48 | 48 | — |
| LX14 (periodic ABC) | ABC analysis | COVERED-31 | 31 | — |
| EWM: /SCWM/WAVE scheduled release, replenishment (/SCWM/REPL), slotting (/SCWM/SLOT), rearrangement (/SCWM/REAR), PI generation, archiving | — | COVERED-31 | 31 | — |
| EWM: /SCWM/ERP_* delivery replication, queue monitor (SMQ1/SMQ2, qRFC) | ERP↔EWM integration | SUPERSEDED-31 | 31 | Embedded single system; Kafka events instead of qRFC |
| EWM: Delta/initial stock load (/SCWM/ISU, /SCWM/ERP_STOCKCHECK) | Stock comparison ERP↔EWM | SUPERSEDED-16 | 16 | Single ledger |
| Labor management period close (/SCWM/LM) | — | PARTIAL-31 | 31 | — |

### 13.16.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OMLW / Define warehouse number, assign to plant/storage location (OMLY) | T300, T320 | Warehouse number ↔ plant/SLoc | COVERED-6 | 6 | Warehouse entity (wave 6) with WMS-enabled flag (wave 31) |
| Define storage types (OMLX / T301), storage sections (T302), storage bin types (T303), storage unit types (T307), bin structure | T301, T302, T303, T307, T305 | Put-away strategy (A/B/C/F/I/K/L/P/Q/R), removal strategy (A/F/L/M/P/S/H), mixed storage, capacity check method, SU management, addition to stock, bin blocking on movement, return/negative stock | SUPERSEDED-31 | 31 | Storage strategies replaced by location directives (rules engine) + location profiles; capacity check methods covered |
| Storage section search (OMLH), storage type search (OMLY/T334T), bin type search (T334B) | T334T, T334B, T334E | Search sequences per movement type/stock category/special stock | SUPERSEDED-31 | 31 | Directive rules |
| WM movement types (OMLR), reference movement types IM→WM (OMLM LE_WM_MVT) | T333, T321 | TO creation automatic/immediate, confirmation required, source/dest bins fixed | SUPERSEDED-31 | 31 | Work templates per source document type |
| Define control for TO creation / confirmation (OMLV print control, OMLN number ranges) | T329*, T340 | Print immediately, pick list sorting, confirmation per item/HU | COVERED-31 | 31 | Work template options |
| Storage unit management activation, SU number ranges | T300, NRIV | — | COVERED-31 | 31 | LP sequences |
| Replenishment control for fixed bins / storage types (OMLH / T331B) | T331, T331B, T* | Min/max per bin, replenishment movement type | COVERED-31 | 31 | — |
| Physical inventory in WM: activate, PI types per storage type, difference posting tolerances | T301, T* | — | COVERED-16 | 16 | — |
| Hazardous material: regions, water pollution classes, storage classes, fire-containment sections | T* (MGEF) | — | GAP | propose 34 | Hazmat gap |
| Interface to QM / PP (control cycles for WM staging PK05 / LPK1), kanban in WM | PKHD, T* | — | COVERED-31 | 31 | Staging for production (control cycles) — confirm WM staging strategies (pick parts, crate parts, release order parts) in wave 31 |
| RF: menu management (LRFMD LRF1), screen management, verification profiles, bar code types (OMLC) | T* , LRF_* | — | COVERED-32 | 32 | Flow designer |
| Decentralized WM / EWM integration (SLG, qRFC, distribution model) | — | — | SUPERSEDED-31 | 31 | — |
| EWM: warehouse number, storage types/sections/bins/activity areas, storage bin sorting, warehouse process types, POSC/LOSC (process-oriented/layout-oriented storage control), put-away/stock removal strategies & search sequences, HU types & packaging specs, work centers, queues, resource types, wave templates, labor management, exceptions codes, PPF | /SCWM/T3* tables | — | COVERED-31 | 31 | Rules engine + work templates + location profiles; packaging specs partial (noted) |
| EWM: delivery document types / item types, mapping from ERP | /SCDL/* | — | SUPERSEDED-28 | 28 | Single delivery model |
| EWM: QIE, catch weight, batch management, serial number profiles in EWM | — | — | COVERED-33 | 33 | — |
| EWM: Yard management customizing | — | — | PARTIAL-31 | 31 | — |
| EWM: MFS customizing (PLC communication channels, telegrams) | — | — | GAP | propose 32 | — |
| EWM: Labor management: formulas, ELS, incentive | — | — | PARTIAL-31 | 31 | — |

---

## 13.17 LE-SHP / LE-TRA — Shipping & (classic) Transportation

SAP context: outbound deliveries (VL01N, VL10*), picking (LT03 / WM, or picking without WM), packing with HUs, post goods issue (VL02N), inbound deliveries (VL31N) and ASN, delivery monitor (VL06O/VL06I), shipping points, route determination, delivery scheduling, POD (VLPOD), output (VL71/VL74), shipment documents (VT01N) and shipment cost (VI01) in LE-TRA (deprecated in S/4 in favour of TM). Passage: wave 28 (release, pick/pack/ship, HU/SSCC, carrier adapters, ASN, drop-ship, partial shipments), wave 18 (inbound ASN receiving), wave 34 (shipment/freight).

### 13.17.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| VL01N / VL02N / VL03N | Outbound delivery create (w/ reference to order) / change / display | Delivery document | COVERED-28 | 28 | Shipment/packing-slip document from order release |
| VL01NO | Delivery without order reference | — | COVERED-28 | 28 | Ad-hoc shipment (e.g. samples) — confirm |
| VL10A / VL10B / VL10C / VL10D / VL10E / VL10F / VL10G / VL10H / VL10I | Delivery due list: sales orders / POs (STO) / items / items (STO) / …, collective delivery creation | Release to warehouse in batch | COVERED-28 | 28 | Order release rules (batch/wave) |
| VL04 | Process delivery due list (classic) | — | SUPERSEDED-28 | 28 | — |
| VL06O / VL06G / VL06P / VL06U / VL06C / VL06D / VL06L / VL06T / VL06F | Outbound delivery monitor: for GI / for picking / unchecked / confirmation / for distribution / loading / transportation planning / general | Delivery worklist by status | COVERED-28 | 28 | Fulfillment workspace with status views |
| VL06I / VL06IG / VL06IP / VL06IC | Inbound delivery monitor | — | COVERED-18 | 18 | Inbound ASN/receiving worklist |
| VL31N / VL32N / VL33N | Inbound delivery (ASN) create/change/display | ASN-based receiving | COVERED-18 | 18 | ASN-driven receiving with LPs (wave 31) |
| VL34 | Worklist inbound deliveries (collective creation from POs) | — | COVERED-18 | 18 | — |
| VL09 | Reverse goods movement (cancel PGI/PGR) | Reversal | COVERED-28 | 28 | Packing slip cancellation reverses COGS |
| VL02N → Post goods issue | PGI | COGS/inventory posting | COVERED-28 | 28 | — |
| VL22 | Display delivery change documents | — | COVERED-5 | 5 | — |
| VL23 / VL21 | Post goods issue in background / collective PGI | — | COVERED-28 | 28 | Batch PGI |
| VL35 / VL35_ST / VL36 / VL37 | Create picking waves (by delivery time / shipment) / change wave / wave monitor | Wave picking (basic) | COVERED-28 | 28 | Basic wave picking (wave 28), advanced (wave 31) |
| LT03 / LT0A (picking via WM) | Picking TO for delivery | — | COVERED-28 | 28 | Pick confirmation |
| VL02N → Picking without WM (pick quantity) / "Lean WM" | Picking without WM | — | COVERED-28 | 28 | Basic pick |
| VL02N → Pack (HU01 HU02 in delivery), VL74 | Packing in delivery; HU creation; packing proposals | Packing | COVERED-28 | 28 | Packing with containers/HU, SSCC |
| HU02 / HU03 / HUMO / HUPAST | HU management | — | COVERED-28 | 28 | — |
| VLPOD / VLPODL / VLPODQ | Proof of delivery: change / list / quantity differences | POD-based billing | COVERED-34 | 34 | POD events from carrier/EDI 214; billing on POD — confirm wave 29 billing trigger "on POD" option |
| VL71 / VL74 / VL70 | Output from deliveries / HU output / message overview | Packing slip, labels, ASN (DESADV/856) | COVERED-28 | 28 | ASN output, labels |
| VLSP | Subsequent outbound delivery split | Split delivery | COVERED-28 | 28 | Partial shipments/split |
| VL_ VLUNIV | Delivery mass change (VL_MASS) | — | COVERED-5 | 5 | — |
| VT01N / VT02N / VT03N | Shipment document create/change/display (LE-TRA) | Shipment (consolidated deliveries, legs, stages, carrier) | COVERED-34 | 34 | Load/shipment (wave 34); basic shipment in wave 28 ("shipments") |
| VT04 / VT05 / VT06 / VT70 | Shipment creation in collective processing / shipment list / shipment output | — | COVERED-34 | 34 | Load building |
| VT11 / VT12 / VT14 / VT16 / VT22 | Shipment lists: selection / status / change documents | — | COVERED-34 | 34 | — |
| VI01 / VI02 / VI03 / VI04 / VI05 / VI11 / VI12 | Shipment cost document create/change/display/list; settlement; transfer to FI | Freight cost calc & accrual | COVERED-34 | 34 | Rating + freight accrual |
| VI07 / VI08 | Shipment cost settlement with service agent (self-billing) | — | COVERED-34 | 34 | Freight reconciliation / self-billing |
| VTFL / VFSC | Shipment cost → billing of freight to customer | — | COVERED-29 | 29 | Freight charges on invoice |
| VL10 → STO deliveries (replenishment delivery NL/NLCC) | Deliveries for stock transport orders | — | PARTIAL-16 | 16 | Transfer order gap |
| VRRE / VL0 (returns delivery) | Returns delivery (inbound) for RE orders | — | COVERED-30 | 30 | RMA receipt |
| VL02N → batch split, serial numbers in delivery | Batch/serial assignment at shipment | — | COVERED-28 | 28 | Tracking dimensions on packing slip |
| Delivery scheduling (route, transit, pick/pack times, loading) | Backward/forward delivery scheduling | — | COVERED-27 | 27 | Delivery date control (transport days, pick/pack lead) |
| Fiori: Manage Outbound Deliveries / Create Outbound Deliveries / Pick Outbound Deliveries / Pack Outbound Deliveries / Post Goods Issue / Manage Inbound Deliveries / Change Inbound Deliveries | S/4 apps | — | COVERED-28 | 28 | — |
| Fiori: Outbound Delivery Overview / Delivery Performance (analytical) | — | — | COVERED-46 | 46 | OTIF metric |
| Dangerous goods check in delivery (EHS-DG), DG documents | — | — | GAP | propose 34 | Hazmat gap |
| Foreign trade / export documents in delivery (customs, commercial invoice, EUR.1, Intrastat from delivery; GTS) | Export compliance | — | PARTIAL-13 | 13 | Intrastat extract named (wave 13); export documentation (commercial invoice, packing list for customs, certificate of origin), export control screening (denied parties, licenses) and customs declarations (GTS) are not — partial; propose wave 48 "foreign trade/customs" pack |
| Preference determination (SAP GTS / foreign trade) | Origin & preference (USMCA/EU) | — | GAP | propose 48 | Rules-of-origin/preference calculation from BOM — gap |

### 13.17.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| VL06O / VL06I family | Delivery monitors | COVERED-28 | 28 | — |
| VL06F | General delivery list | COVERED-28 | 28 | — |
| VL_ VLSI | Delivery lists | COVERED-28 | 28 | — |
| V_SA / V_UC / V_V2 | Backorders by delivery / incomplete deliveries / rescheduling | COVERED-27 | 27 | Backorder handling |
| VA14L / V.15 | Blocked deliveries / backorders | COVERED-27 | 27 | — |
| VT11 / VT16 / VT20 | Shipment lists | COVERED-34 | 34 | — |
| VI11 / VI12 | Shipment cost lists | COVERED-34 | 34 | — |
| MC(/ MCTA (SIS deliveries) / MC-O | Shipping analyses (on-time, lead time) | COVERED-46 | 46 | OTIF/delivery performance |
| HUMO | HU monitor | COVERED-28 | 28 | — |
| VLPODL | POD list | COVERED-34 | 34 | — |
| Fiori: Outbound Delivery Log/ Delivery Performance | — | COVERED-46 | 46 | — |

### 13.17.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| VL10 family (batch) | Scheduled delivery creation | COVERED-28 | 28 | Release rules scheduled |
| VL23 / VL21 (batch PGI) | Batch goods issue | COVERED-28 | 28 | — |
| VL71 / RSNAST00 | Output run (packing slips, ASN EDI) | COVERED-28 | 28 | — |
| VT04 (batch) | Shipment creation run | COVERED-34 | 34 | — |
| VI04 (batch) | Shipment cost creation | COVERED-34 | 34 | — |
| VI05 / Shipment cost settlement run | Freight accrual settlement | COVERED-34 | 34 | — |
| VLPODQ (batch) | POD quantity processing | COVERED-34 | 34 | — |
| V_V2 (scheduled) | Rescheduling sales documents (backorder re-allocation) | COVERED-27 | 27 | ATP re-allocation job |
| SARA RV_LIKP / SD_VTTK | Archiving deliveries/shipments | COVERED-48 | 48 | — |
| Intrastat (VE01 / MEIS / VI99) declaration run | Intrastat from deliveries/receipts | COVERED-13 | 13 | — |
| Fiori: Schedule Delivery Creation / Schedule Goods Issue | — | COVERED-4 | 4 | — |

### 13.17.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OVXD / OVX6 — Shipping points define / assign to plant; shipping point determination (OVL2) | TVST, TVSWZ, TVSTZ | Loading group × shipping condition × plant → shipping point | COVERED-28 | 28 | Warehouse/shipping site determination rules |
| 0VLK — Delivery types (LF, LR, NL, NLCC, EL) | TVLK | Order required, item requirement, route determination, output, text determination, number range | COVERED-28 | 28 | Shipment document types |
| 0VLP — Delivery item categories; 0184 determination | TVLP, T184L | Picking relevance, packing control, availability check, batch split allowed, zero-quantity allowed | COVERED-28 | 28 | Line types |
| OVLK / 0VLG — Delivery split criteria, combination rules (per ship-to, route, shipping point) | TVLK | — | COVERED-28 | 28 | Consolidation rules |
| OVLY / OVZ9 (delivery scheduling): shipping point/route times (pick/pack, loading, transit, transportation lead time), factory calendars | TVST, TVRO, TROLZ | Backward/forward scheduling, working times | COVERED-27 | 27 | Delivery date control |
| 0VTC / OVRF — Routes, route stages, route determination (departure zone × destination zone × shipping condition × transportation group) | TVRO, TVRSZ, TVTW, TROLZ | Transit time, carrier default, route schedule (VL51) | COVERED-34 | 34 | Routing/rating in wave 34; basic route/transit in wave 27/28 |
| VL51 / VL52 / VL53 — Route schedules | TVSR | — | COVERED-34 | 34 | — |
| OVLT — Picking: lean WM, picking location determination (MALA/RETA/MARE rules), storage location rule | TVLK, TVKOL | — | COVERED-28 | 28 | Picking location rules |
| OVLS / OVLV — Packing: packaging material types (VHAR), packing instructions (POP1/POP2/POP3/POF1), allowed packaging materials, HU number ranges (SSCC), packing control per item category | TVTY, TVEGR, PACKKP | Automatic packing via packing instructions | PARTIAL-28 | 28 | SSCC/containers yes; packing instructions (auto-pack rules) partial (same as EWM packaging specs) |
| OVLP — POD: activate POD per item category / ship-to, reasons for quantity differences | TVLP, TVPODG | — | COVERED-34 | 34 | — |
| V/30 / VV21 / VV22 / VV23 — Output determination for deliveries (LD00 delivery note, LALE ASN, HU labels), output types, access sequences, condition records | T685B, NAST, TNAPR | Medium, timing, partner | COVERED-28 | 28 | Output rules (see cross-app) |
| VTLA — Copy control order → delivery; VTFL delivery → billing | TVCPL, TVCPF | Copy requirements, data transfer routines | SUPERSEDED-28 | 28 | Document flow in domain model with explicit mapping rules; no copy-control routines |
| OVZ9 / OVZ2 / OVZ3 — Availability check in deliveries (checking groups, scope) | TVLP, T441V | — | COVERED-27 | 27 | ATP |
| OVLK: Partner determination in delivery (VOPAN LF), text determination (VOTXN), incompletion (OVA2/OVA0 delivery incompletion) | TVLK, TPAER, TVIMF | — | COVERED-28 | 28 | Partners/notes/completeness validations via metadata |
| 0VLK: Serial numbers / batch split in delivery; batch determination (VCH1 for delivery) | — | — | COVERED-28 | 28 | — |
| OVTZ / Transportation: shipment types (VTTK), shipment stages, legs, shipment cost types (T_55), pricing procedure for freight (TVFT), service agent condition records (TK11), shipment cost settlement settings | TVTK, TVFT, T_56, TVFTS | Shipment cost item categories, calculation basis (weight/volume/distance), settlement to vendor | SUPERSEDED-34 | 34 | TM-style rating engine (rate cards, accessorials) replaces LE-TRA condition-technique freight pricing |
| Foreign trade: export control, preference, Intrastat settings (VEB, VE0) | T604, T605 | — | PARTIAL-13 | 13 | Foreign trade gap |
| Dangerous goods: DG profiles, DG check methods in SD/LE | DGT* | — | GAP | propose 34 | Hazmat gap |
| Loading groups, transportation groups (material master) | TLGR, TTGR | — | COVERED-8 | 8 | Product attributes |
| Define reasons for cancellation of PGI, goods movement types for delivery (601/602/641/643/651…) via schedule line | T156, TVEP | — | COVERED-28 | 28 | Transaction types |
| OVLN Define number ranges deliveries / HUs | NRIV | — | COVERED-5 | 5 | — |

---

## 13.18 TM — Transportation Management (SAP TM / embedded TM in S/4)

SAP context: freight units (FU), freight orders (FO) and bookings (FB), transportation cockpit (/SCMTMS/PLAN? or NWBC/Fiori), carrier selection and tendering, charge management (agreements, rate tables, calculation sheets), freight settlement documents (FSD) → MM-IV, forwarding orders/settlement (LSP scenario), tracking events (EM), strategic freight procurement. Passage: wave 34 (load/route planning with VRP, rating, tendering, tracking, freight reconciliation, landed cost).

### 13.18.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| /SCMTMS/TRQ (forwarding order) / Fiori: Create Forwarding Order | Forwarding order (LSP scenario: customer orders transport) | LSP business | OUT-OF-SCOPE | — | Logistics service provider (3PL/forwarder) scenario is beyond manufacturer scope; shipper scenario only |
| Freight units (FU) built from deliveries/orders (FUBR rules) | Freight unit building | — | COVERED-34 | 34 | Load building from shipments |
| /SCMTMS/FRE Freight order (road), freight booking (air/ocean) | FO/FB documents | — | COVERED-34 | 34 | Loads with carrier; ocean/air bookings — confirm multi-modal (wave 34 says loads/routes/carriers; treat covered with ocean/air as modes) |
| Transportation cockpit (/SCMTMS/PLAN NWBC) / Fiori: Transportation Cockpit | Manual + optimizer planning (VSR) | Planning UI | COVERED-34 | 34 | Load/route planning with OR-Tools VRP |
| Carrier selection (manual/automatic, cost/priority/allocation) | — | — | COVERED-34 | 34 | Rate shopping |
| Tendering (peer-to-peer, broadcast, carrier portal/collaboration portal, EDI 990) | Tendering workflow | — | COVERED-34 | 34 | Tendering workflow with timeouts cascade |
| Freight agreements, rate tables, calculation sheets, charge types, scales | Charge management master data | — | COVERED-34 | 34 | Carrier rate cards, accessorials, fuel surcharge |
| Freight settlement document (FSD) / Fiori: Manage Freight Settlement Documents → MM-IV (service PO) | Carrier cost settlement | — | COVERED-34 | 34 | Freight reconciliation + accrual posting |
| Cost distribution (to deliveries/items → CO-PA/landed cost) | Freight cost distribution | — | COVERED-34 | 34 | Landed cost allocation |
| Tracking & tracing (Event Management / Fiori: Track Freight Orders), event handlers | Visibility | — | COVERED-34 | 34 | Tracking events (EDI 214/API), POD |
| Transportation units / resources (vehicles, trailers, drivers), calendars | Fleet resources | — | PARTIAL-34 | 34 | Own fleet (vehicles/drivers/driver hours) not named — partial; carriers assumed external |
| Transportation network: locations, zones, lanes, transshipment locations, schedules | Network master | — | COVERED-34 | 34 | Routes/lanes/zones |
| Dangerous goods & DG checks in TM | — | — | GAP | propose 34 | Hazmat gap |
| Customs declarations via GTS from TM | — | — | PARTIAL-13 | 13 | Foreign trade gap |
| Fiori: Monitor Freight Orders / Freight Order Worklist | — | — | COVERED-34 | 34 | — |
| Strategic freight procurement (RFQ for lanes, bid analysis) | — | — | GAP | propose 34 | Lane-based freight RFQ/bid analysis — gap; could reuse wave 17 RFQ |
| Load consolidation / container packing optimization (load plan 3D) | — | — | PARTIAL-34 | 34 | Load building with capacity constraints named; 3D load plan/visual stacking partial |
| Parcel manifesting (multi-carrier, label) | — | — | COVERED-28 | 28 | Carrier adapters (EasyPost/Shippo-style) |

### 13.18.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| Fiori: Freight Order Overview / Transportation Cockpit lists | — | COVERED-34 | 34 | — |
| Fiori: Analyze Freight Costs / Freight cost per lane / carrier performance | — | COVERED-46 | 46 | — |
| Fiori: Track Shipments / event monitor | — | COVERED-34 | 34 | — |
| Fiori: Carrier On-time performance | — | COVERED-46 | 46 | — |
| TM BW content (freight cost analytics) | — | SUPERSEDED-46 | 46 | — |

### 13.18.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| Background planning (VSR optimizer runs) | Scheduled planning | COVERED-34 | 34 | — |
| Freight unit building run | — | COVERED-34 | 34 | — |
| Automatic carrier selection run / tendering jobs | — | COVERED-34 | 34 | — |
| FSD creation & transfer to ERP (batch) | Freight settlement run | COVERED-34 | 34 | — |
| Freight accrual posting / reversal | — | COVERED-34 | 34 | — |
| Event message processing (EM) | — | COVERED-34 | 34 | — |
| Archiving freight documents | — | COVERED-48 | 48 | — |

### 13.18.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Freight unit building rules, freight order types, freight booking types | /SCMTMS/C_* | Split rules, stages | COVERED-34 | 34 | — |
| Planning profiles, optimizer settings, selection profiles, capacity selection, incompatibilities, constraints | /SCMTMS/C_* | Cost functions, time windows | COVERED-34 | 34 | VRP constraints |
| Carrier selection settings, tendering profiles, tendering process types | — | — | COVERED-34 | 34 | — |
| Charge management: charge types, calculation profiles, rate table templates, scale templates, agreement types | — | — | COVERED-34 | 34 | — |
| Settlement: FSD types, cost distribution profiles, invoice integration (service PO types), accruals | — | — | COVERED-34 | 34 | — |
| Event management configuration (expected events, event types) | — | — | COVERED-34 | 34 | — |
| Transportation zones, lanes, means of transport, equipment types | — | — | COVERED-34 | 34 | — |
| Output management (PPF) for TM documents (BOL, load plan) | — | — | COVERED-46 | 46 | Document designer |
| Integration: ERP order/delivery-based transportation requirements, TM–EWM integration | — | — | SUPERSEDED-34 | 34 | Single system events |
| Dangerous goods in TM | — | — | GAP | propose 34 | — |

---

## 13.19 SD — Sales & Distribution (pricing, orders, deliveries, billing, credit)

SAP context: SD-MD (customer sales data, customer-material info, conditions), SD-SLS (inquiry, quotation, order, contract, scheduling agreement, returns, credit/debit memo requests, consignment, free-of-charge, rush/cash sales, third-party, make-to-order with configuration), SD-BF (pricing condition technique, availability check, partner/text/output determination, incompletion, credit management), SD-SHP (see LE-SHP), SD-BIL (billing documents, billing plans, invoice lists, credit/debit memos, pro-forma, intercompany billing, rebates, revenue account determination, SD-FI interface). Passage: wave 26 (pricing), wave 27 (quotes/orders/ATP/credit/holds), wave 28 (fulfillment), wave 29 (billing/rev rec), wave 30 (AR/returns/portal), wave 40 (intercompany).

### 13.19.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| VD01 / VD02 / VD03 / XD01 (sales area data) | Customer master sales area view | Shipping, billing, partner functions, pricing groups per sales org/channel/division | COVERED-7 | 7 | Customer role with sales-scope data (sales org/channel/division ≈ hierarchy purpose) |
| VD51 / VD52 / VD53 / VD59 | Customer-material info record create/change/display/list | Customer part numbers, delivery agreements | COVERED-27 | 27 | Customer item numbers/external item references — confirm in wave 27 (named in AX "external item descriptions"); treat covered |
| VK11 / VK12 / VK13 / VK14 | Condition record create/change/display/create with reference (prices PR00, discounts K004/K005/K007, freight, taxes MWST) | Pricing conditions | COVERED-26 | 26 | Pricing engine condition tables/access sequences |
| VK31 / VK32 / VK33 / VK34 | Condition maintenance via area menu (mass) | — | COVERED-26 | 26 | Mass price updates |
| V-41 … V-48 / V_I7 | Create material/customer/price list conditions, scales | — | COVERED-26 | 26 | Quantity breaks (scales) |
| VK30 / VKP | Condition maintenance / pricing report | — | COVERED-26 | 26 | — |
| VB11 / VB12 / VB13 / VB14 | Material determination (substitution) create/change/display/reference | Product substitution rules | COVERED-27 | 27 | Alternative items |
| VB01 / VB02 / VB03 | Listing/exclusion | Allowed/forbidden products per customer | PARTIAL-27 | 27 | Product listing/exclusion per customer not named in wave 27 — partial; simple rule on order validation |
| VBN1 / VBN2 / VBN3 | Free goods condition records (inclusive/exclusive) | Free goods | PARTIAL-26 | 26 | Free goods (buy 10 get 1) not named; promotions with quantity breaks are — partial |
| VB21 / VB22 / VB23 / VB31 / VB32 / VB33 | Sales deals / promotions | Promotions | COVERED-26 | 26 | Promotions & campaigns |
| VA51 / VA52 / VA53 | Item proposal create/change/display | Order templates | COVERED-27 | 27 | Order templates / product proposals — confirm (AX "item proposal") |
| VA11 / VA12 / VA13 / VA15 | Inquiry create/change/display/list | Pre-sales inquiry | COVERED-27 | 27 | Quotation stage "inquiry" or CRM; wave 27 quotations cover |
| VA21 / VA22 / VA23 / VA25 / VA26 | Quotation create/change/display/list/selection | Quotation | COVERED-27 | 27 | Quotation → order conversion |
| VA01 / VA02 / VA03 / VA05 / VA05N | Sales order create/change/display/list | Standard order (OR), rush (SO), cash sales (BV), free-of-charge (FD/KL), returns (RE), credit/debit memo request (CR/DR), consignment (KB/KE/KA/KR), third-party, MTO/configurable | COVERED-27 | 27 | Order types: journal/order/subscription/returned; CR/DR requests → credit note workflow (wave 29/30) |
| VA01 order type RE (returns) + VRRE | Returns order with returns delivery | RMA | COVERED-30 | 30 | Return orders (RMA) with disposition |
| VA01 CR / DR (credit / debit memo request) with billing block | Credit/debit memo requests | — | COVERED-29 | 29 | Credit notes with approval |
| VA01 KB / KE / KA / KR (consignment fill-up/issue/pick-up/returns) | Customer consignment processes | — | PARTIAL-27 | 27 | Customer consignment gap (see MM-IM) |
| VA01 BV / SO (cash sales / rush order) | Immediate delivery and invoice | — | COVERED-27 | 27 | Order type with immediate release/invoice — confirm |
| VA01 FD / KL / SD (free-of-charge delivery / subsequent free-of-charge) | Free-of-charge | — | COVERED-27 | 27 | Zero-price order lines with COGS |
| VA01 third-party (item cat TAS) → PR/PO → MIRO → VF01 | Third-party order processing | — | COVERED-28 | 28 | Drop-ship |
| VA01 individual purchase order (TAB) | Purchase-to-order (stock via PO) | — | COVERED-27 | 27 | Direct delivery / back-to-back PO — covered by drop-ship & "order with configured product creates production/planned order linkage" (wave 27); back-to-back purchase covered via wave 22 MRP; treat covered |
| VA01 with variant configuration (TAC) | Configure-to-order | — | COVERED-27 | 27 | Configurator (wave 8) |
| VA01 MTO (TAK) → production order | Make-to-order linkage | — | COVERED-27 | 27 | Order-linked production order |
| VA31 / VA32 / VA33 / VA35 | Customer scheduling agreement create/change/display/list (SA with JIT/forecast schedules VA32 → delivery schedule) | Customer SA (automotive) | PARTIAL-27 | 27 | "Scheduling agreements" named in wave 27 replaces list; inbound EDI DELFOR/DELJIT (830/862) customer schedules with cumulative quantities and schedule reconciliation are not named — partial (automotive) |
| VA41 / VA42 / VA43 / VA45 | Contract create/change/display/list (quantity contract, value contract, service contract, master contract) | Sales agreements | COVERED-27 | 27 | Sales agreements (blanket); service contracts in wave 45 |
| VA44 / VA46 | Contract: actual overview / value contract release orders | — | COVERED-27 | 27 | Release tracking on agreements |
| VA88 / VA08 | Sales document mass processing (MASS VBAK) | Mass change | COVERED-5 | 5 | — |
| VA14L | Sales documents blocked for delivery | Holds | COVERED-27 | 27 | Order holds |
| VKM1 / VKM2 / VKM3 / VKM4 / VKM5 | Credit: blocked SD documents / released / all / release / delivery | Credit release | COVERED-27 | 27 | Credit hold workflow |
| V.01 / V.02 / V.03 / V.04 / V.05 | Incomplete sales documents lists (orders, deliveries, contracts…) | Incompleteness | COVERED-27 | 27 | Validation/incomplete status |
| V_R1 / V_R2 / V_RA | Rescheduling / backorder processing | ATP rebalancing | COVERED-27 | 27 | Backorder handling |
| CO06 / CO09 / CO24 | Backorder processing (ATP manual allocation) / availability overview / missing parts | ATP | COVERED-27 | 27 | ATP/CTP with cumulative ATP view |
| V_V2 | Rescheduling sales documents (batch) | — | COVERED-27 | 27 | — |
| VKA / VSB1 (self-billing) | Self-billing procedure (customer self-billing, automotive) | — | GAP | propose 29 | Customer self-billing (EDI 820/GSVERF reconciliation against invoices) not named; automotive; propose wave 29/35 option |
| VF01 / VF02 / VF03 | Billing document create/change/display (invoice F2, pro-forma F5/F8, credit memo G2, debit memo L2, intercompany IV, returns credit RE, cancellation S1) | Billing | COVERED-29 | 29 | Invoice posting; credit notes; pro-forma — confirm pro-forma (no posting) document type; treat covered |
| VF04 | Maintain billing due list (order-related, delivery-related, collective) | Billing run | COVERED-29 | 29 | Invoice generation from shipments/orders; consolidated invoices |
| VF05 / VF05N | List billing documents | — | COVERED-29 | 29 | — |
| VF06 | Batch billing | — | COVERED-29 | 29 | — |
| VF11 | Cancel billing document | Reversal | COVERED-29 | 29 | Credit note / cancellation |
| VF21 / VF22 / VF23 / VF24 / VF25 | Invoice list create/change/display/worklist/list | Invoice lists (periodic collective invoice with factoring discount) | COVERED-29 | 29 | Consolidated/summary invoices |
| VF31 | Output from billing documents | Print/EDI invoice (810/INVOIC), e-invoice | COVERED-29 | 29 | Invoice layouts; e-invoice via wave 35 |
| VFX3 | Release billing documents to accounting (blocked) | — | COVERED-29 | 29 | Posting errors queue |
| VF44 / VF45 / VF46 / VF47 | Revenue recognition (classic SD rev rec): worklist / display / cancel / inconsistency check | Classic rev rec | SUPERSEDED-29 | 29 | ASC 606/IFRS 15 rev rec engine |
| RAR: FARR_* (contracts, POBs, schedules), Fiori: Manage Revenue Contracts | Revenue Accounting and Reporting (IFRS 15) | — | COVERED-29 | 29 | Performance obligations, SSP allocation, schedules |
| Billing plans (periodic/milestone) on order items (VA02 → billing plan) | Periodic / milestone billing | — | COVERED-29 | 29 | Subscription (recurring) + milestone billing |
| Down payment processing in SD (billing plan FAZ, F-29) | Down payment request/invoice | — | COVERED-27 | 27 | Deposit requests; wave 30 customer prepayments |
| VF01 IV (intercompany billing) | Intercompany invoice from cross-company delivery | — | COVERED-40 | 40 | IC mirror documents |
| Resource-related billing DP90/DP91 (see CS/PS) | — | — | COVERED-41 | 41 | T&M billing |
| Customer returns (S/4 Advanced Returns Management: MSR_*) | Returns with inspection, refund determination, logistics follow-up (return to vendor) | — | COVERED-30 | 30 | RMA: inspection, disposition, credit/refund; return-to-vendor flow — confirm return-to-vendor; treat covered |
| VKP0 / Pricing report (V/LD) | Pricing reports (condition lists) | — | COVERED-26 | 26 | Price simulation and explain |
| VOFM (routines) — see customizing | — | — | SUPERSEDED-26 | 26 | — |
| Fiori: Manage Sales Orders / Sales Quotations / Sales Contracts / Sales Scheduling Agreements / Credit Memo Requests / Debit Memo Requests / Customer Returns / Billing Documents / Create Billing Documents | S/4 apps | — | COVERED-27 | 27 | — |
| Fiori: Sales Order Fulfillment Issues (monitor), Track Sales Orders | — | — | COVERED-27 | 27 | Order holds/exceptions workspace |
| Fiori: Manage Prices – Sales / Pricing Elements | — | — | COVERED-26 | 26 | — |
| Fiori: Manage Solution Orders / Solution Business (bundles) | Bundled sales (products, services, subscriptions) | — | PARTIAL-29 | 29 | Bundles with mixed revenue types partially via rev rec bundles; solution-order document type not named — partial |
| SAP CPQ / Sales Cloud | Configure-price-quote (cloud) | — | OUT-OF-SCOPE | — | CRM/CPQ suites: PRD non-goal (integrate with Salesforce/HubSpot); internal quoting with configurator (waves 8/27) covers ERP-side CPQ |
| Sales activities / CAS (VC01N) | Sales support (contacts, activities, mailings) | — | OUT-OF-SCOPE | — | CRM non-goal |
| Cross-selling (VB41), product proposal/dynamic product proposal | Order-entry suggestions | — | PARTIAL-27 | 27 | Product proposals partially (item proposal); cross-sell rules not named |
| Payment cards in SD (authorization, settlement — CCARD) | Card payments on orders | — | COVERED-30 | 30 | Stripe/Adyen adapter on portal; card on order/invoice — confirm card authorization on order (wave 30 says portal payments); treat covered |

### 13.19.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| VA05 / VA05N | List of sales orders (by customer, material, status) | COVERED-27 | 27 | — |
| VA15 / VA25 / VA35 / VA45 | Inquiries / quotations / scheduling agreements / contracts lists | COVERED-27 | 27 | — |
| VA26 / VA46 (expiring) | Quotation / contract expiry lists | COVERED-27 | 27 | — |
| V.01 … V.05 | Incomplete documents | COVERED-27 | 27 | — |
| V.14 / V.15 | Sales orders by object status / backorders | COVERED-27 | 27 | — |
| V.21 / V.23 | Log of collective run / release orders for billing | COVERED-29 | 29 | — |
| VA14L / VKM1 | Blocked documents (delivery / credit) | COVERED-27 | 27 | — |
| VF05 / VF05N | Billing documents list | COVERED-29 | 29 | — |
| VF24 / VFX3 | Invoice list worklist / blocked for accounting | COVERED-29 | 29 | — |
| V/LD | Pricing report (condition lists) | COVERED-26 | 26 | — |
| VK13 lists / V_NL | Condition overview | COVERED-26 | 26 | — |
| CO09 / CO06 | Availability overview / backorder processing | COVERED-27 | 27 | — |
| MC+E / MC+A / MC+Q / MC+2 / MC(A / MC(I / MC-A / MC-E | SIS: customer analysis (incoming orders, sales, returns, credit memos), material analysis, sales org, shipping point, sales office/employee | COVERED-46 | 46 | Sales analytics in semantic layer |
| MCTA / MCTC / MCTE / MCTG / MCTI / MCTK | SIS standard analyses: customer / material / sales org / sales office / sales employee / shipping point | COVERED-46 | 46 | — |
| MCSI | SIS flexible analyses | SUPERSEDED-46 | 46 | Self-service explorer |
| S_ALR_87012 (SD-FI: revenue by account) / VKOA simulation | Revenue account determination analysis | COVERED-29 | 29 | — |
| F.31 / FDK43 (credit) | Credit overview | COVERED-27 | 27 | — |
| VA88 / SDO1 | Document flow | COVERED-27 | 27 | Document flow: order → shipment → invoice → payment |
| Fiori: Sales Order Fulfillment Monitor / Sales Volume / Incoming Sales Orders / Sales Order Backlog / Quotation Conversion Rates / Customer Returns analytics | S/4 analytical apps | COVERED-46 | 46 | — |
| Fiori: Sales Management Overview | KPI page | COVERED-46 | 46 | — |

### 13.19.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| VF04 / VF06 (batch) | Billing due list run (daily/periodic, collective invoices) | COVERED-29 | 29 | — |
| VF21 (batch) / RV60SBAT | Invoice list creation | COVERED-29 | 29 | — |
| VF31 (batch) / RSNAST00 | Invoice output run | COVERED-29 | 29 | — |
| VFX3 (batch) | Release to accounting | COVERED-29 | 29 | — |
| V_V2 (batch) | Rescheduling / backorder re-allocation | COVERED-27 | 27 | — |
| VKM / credit re-check batch (RVKRED06/08) | Credit re-check of blocked docs, credit exposure rebuild | COVERED-27 | 27 | — |
| F.28 (reset credit limit data) | Credit data rebuild | SUPERSEDED-27 | 27 | Exposure computed |
| VA05 → mass reject / mass close | Mass order status changes | COVERED-5 | 5 | — |
| VF44 (batch) | Classic rev rec run | SUPERSEDED-29 | 29 | — |
| FARR_* rev rec period-end (contract liability/asset posting, revenue posting run) | RAR period close | COVERED-29 | 29 | Deferred revenue roll-forward postings |
| Billing plan date update (periodic billing dates) | — | COVERED-29 | 29 | Subscription schedules |
| SARA SD_VBAK / SD_VBRK | Archiving sales/billing | COVERED-48 | 48 | — |
| MCVCHECK / S014 SIS update (OMO1) | LIS statistics update | SUPERSEDED-46 | 46 | CDC to lakehouse |
| Fiori: Schedule Billing Creation / Schedule Billing Output / Schedule Billing Release | S/4 job templates | COVERED-4 | 4 | — |

### 13.19.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OVX5 / OVXA / OVXB / OVXG / OVX3 / OVXK / OVXM — Sales organizations, distribution channels, divisions, sales areas, sales offices/groups, assignments to company code/plant | TVKO, TVTW, TSPA, TVKOV, TVKOS, TVBUR, TVKGR, TVKWZ | — | COVERED-6 | 6 | Sales hierarchy purpose; sales scope dimensions |
| VOV8 — Sales document types (OR, RE, CR, DR, KB, BV, SO, QT, KA, IN, WK1, LZ…) | TVAK | Number range, screen sequence, delivery/billing type defaults, credit check, incompletion procedure, pricing procedure determination (doc pricing proc), immediate delivery, lead time, transaction group | COVERED-27 | 27 | Order types with defaults |
| VOV7 — Item categories (TAN, TAS, TAB, TAK, TAC, TANN, TAQ, TAD, REN, G2N, L2N…) | TVAP | Billing relevance (A/B/F/I), pricing relevant, schedule lines allowed, returns, BOM explosion, credit active, completion rule | COVERED-27 | 27 | Line types |
| VOV4 — Item category determination (doc type × item category group × usage × higher-level) | T184 | — | SUPERSEDED-27 | 27 | Line type derived from product type/order type via metadata rules |
| VOV6 / VOV5 — Schedule line categories and determination (CP, CN, CV, DN…) | TVEP, T184 | Movement type, requirement transfer, availability check, purchase requisition creation (third-party) | SUPERSEDED-27 | 27 | Delivery schedule lines are native; movement/requirement behaviour by line type |
| OVKK — Pricing procedure determination (sales area × doc pricing procedure × customer pricing procedure) | T683V | — | COVERED-26 | 26 | Pricing procedure selection rules |
| V/08 — Pricing procedures (RVAA01 etc.): steps, counters, condition types, from/to, manual, required, statistical, subtotals, requirements, calculation/base formulas, account keys | T683S | Requirements/formulas (VOFM), subtotals, account keys ERL/ERS/ERF/MWS | COVERED-26 | 26 | Pricing procedure = ordered condition steps with rounding; VOFM routines replaced by expression rules |
| V/06 — Condition types (PR00, K004, K005, K007, KF00, HD00, RA01, MWST, VPRS, SKTO, BO01…) | T685, T685A | Condition class (price/discount/tax/rebate), calculation type (qty/percent/fixed/scale), scale basis, group condition, header/item, manual, plus/minus, text, reference condition | COVERED-26 | 26 | Condition type attributes |
| V/07 — Access sequences | T682, T682I, T682Z | Accesses with condition tables, exclusive, requirements | COVERED-26 | 26 | Access sequences specific → general |
| V/03 / V/04 / V/05 — Condition tables create/change/display | T681, A001..A999 | Field catalog, header/with validity | COVERED-26 | 26 | Condition tables generated from metadata |
| OV31 / V/N — Condition exclusion groups (VB(1 / OV32) | T684, T684G | Best price among groups | COVERED-26 | 26 | Exclusion/best-of rules |
| OVKI / OVKJ — Document pricing procedure / customer pricing procedure indicators | TVKV, TVKD | — | COVERED-26 | 26 | — |
| V/76 / V/77 — Material pricing group / customer pricing group / price lists / price groups | T178, T151, T189, T188 | — | COVERED-26 | 26 | Price groups |
| V/LD — Pricing reports definition | — | — | COVERED-26 | 26 | — |
| VOFM — Requirements, formulas (pricing, copy, output, data transfer) | TFRM | ABAP routines | SUPERSEDED-26 | 26 | Expression/rule language in metadata; no ABAP (PRD non-goal) |
| OVA8 / OVAK / OVAD / OVA7 — Automatic credit control, credit check per sales doc type, delivery type, item category credit active | T691F, TVAK, TVLK, TVAP | Static/dynamic, horizon, max doc value, oldest open item, reaction, release via VKM | COVERED-27 | 27 | Credit rules |
| OB45 / OB38 — Credit control areas / assignment (classic) | T014, T001 | — | SUPERSEDED-27 | 27 | — |
| OVZ9 / OVZ2 / OVZ3 / OVZ1 / OVZG / OVZH / OVZ0 — Availability check: checking groups, checking rules, scope of check (include POs, reservations, production orders, safety stock), requirement classes/types, strategy groups (planning), delivery date control | TMVF, T441V, T459K, T459A, TVEP | ATP scope, replenishment lead time, backorders, product allocation | COVERED-27 | 27 | ATP configuration: supply/demand elements included, delivery date control; planning strategy in wave 22 |
| OVZJ / OVZI — Product allocation (quota per customer/period) | T190, KOTA | — | GAP | propose 27 | Product allocation (ATP quotas per customer/region in scarcity) not named — propose wave 27 ATP option |
| OVZK / OVZ — Availability check against product allocation; aATP (S/4): backorder processing strategies (BOP), alternative-based confirmation (ABC), release for delivery | — | — | PARTIAL-27 | 27 | ATP/CTP covered; aATP backorder processing with strategies (win/gain/redistribute/fill/lose) partially named ("backorder handling") |
| VOPAN — Partner determination procedures (sales doc header/item, delivery, billing, customer master) | TPAR, TPAER, TVPG | Partner functions SP/SH/BP/PY, mandatory, unique | COVERED-7 | 7 | Document partner roles (sold-to, ship-to, bill-to, payer) |
| VOTXN — Text determination (text types, procedures, access sequences) | TTXID, TTXERN | Copy from customer/material/preceding doc | COVERED-27 | 27 | Notes/text blocks with inheritance — confirm inheritance rules |
| OVA2 / OVA0 / OVA — Incompletion procedures (order/delivery/billing), status groups | TVUVG, TVUV, TVUVF | Fields required for next step | COVERED-27 | 27 | Validation/completeness rules via metadata |
| VTAA / VTLA / VTFA / VTFL / VTAF / VTFF — Copy control order→order, order→delivery, order→billing, delivery→billing, quotation→order, billing→billing | TVCPA, TVCPL, TVCPF | Copy requirements, data transfer routines, item category proposal, quantity/value update (+/-) | SUPERSEDED-27 | 27 | Document conversion rules (quote→order→shipment→invoice) in the domain model with explicit mapping and quantity tracking; no VOFM copy routines |
| VOFA — Billing types (F2, F5, F8, G2, L2, RE, IV, S1, S2, FAZ, FAS) | TVFK | Number range, posting block, account determination procedure, cancellation type, invoice list type, rebate relevant, statistics | COVERED-29 | 29 | Invoice document types |
| OVBI / OVBJ / OVBK / OVBL / OVBM — Billing plan types (periodic, milestone), date descriptions, date categories, date proposals, assignment to item categories | T6B0, TFPLA, TFPLB, TFPLT | Horizon, in advance/arrears, billing block until milestone, milestone from PS | COVERED-29 | 29 | Billing schedules (ratable/milestone/usage) |
| VKOA — Revenue account determination (condition technique: application V, procedure KOFI/KOFK, tables 001-005: sales org/account assignment group customer/material/account key) | C001..C005, T030, TVKV | Account keys ERL, ERS, ERF, MWS, EVV, DIF | COVERED-29 | 29 | Revenue/discount/freight posting profiles by customer group × item group × sales scope |
| OVK1 / OVK3 / OVK4 / OVK6 — Tax determination rules: tax categories per country (MWST, UTXJ), customer tax classifications, material tax classifications, plant/region tax codes | TSTL, TSKD, TSKM, T005, TVKWZ | Tax classification combinations → tax code via condition records (VK11 MWST) | COVERED-13 | 13 | Tax groups × item tax groups → tax codes (tax engine); jurisdiction codes (TAXUSJ) |
| OVFL / OVF — Define plant/region tax jurisdiction defaults | — | — | COVERED-13 | 13 | — |
| OVXD / OVX — Shipping points etc. — see LE-SHP | — | — | COVERED-28 | 28 | — |
| OVLK — Delivery scheduling and transportation scheduling activation per sales doc type | TVAK | — | COVERED-27 | 27 | — |
| OVKP / OVRC — Order reasons, reasons for rejection, delivery blocks, billing blocks, order blocks | TVAU, TVAG, TVLS, TVFS, TVAST | — | COVERED-27 | 27 | Reason codes; holds (credit, manual) |
| OVB — Define sales document status groups, user statuses (BS02) | TJ30 | — | COVERED-4 | 4 | — |
| OVK — Customer groups, customer account assignment groups, material account assignment groups, item category groups, material groups 1-5 | T151, TVKT, TVKM, TPTM, TVM1..TVM5 | — | COVERED-7 | 7 | Customer groups (wave 7), item groups (wave 8) |
| OVSH — Sales districts, sales offices, sales groups, customer hierarchy types (OVH1/OVH2/OVH3 hierarchy assignments) | T171, TVBUR, TVKGR, T179, KNVH | Hierarchy nodes for pricing/rebates | COVERED-7 | 7 | Customer hierarchies (party hierarchy purposes) |
| OVZ "Define requirements classes/types, assignment to strategy group" (OVZG/OVZH/OVZI) | T459K, T459A, T459U | MTO/MTS/CTO behaviour, costing, special stock E | PARTIAL-27 | 27 | Planning strategies (wave 22) + sales-order stock partial |
| Output determination SD (NACE V1/V2/V3, V/30 V/40 V/42 V/48 V/57): output types BA00/LD00/RD00, access sequences, condition records VV11/VV21/VV31, print programs/forms | NAST, TNAPR, T685B, B001.. | Medium (print/EDI/email/ALE), partner, timing, repetition | COVERED-27 | 27 | Output rules per document type; EDI 855/856/810 wave 35 |
| OV "Define material determination types/procedures" (OV11 OV12) | T475, T475T | Substitution reasons, automatic/manual | COVERED-27 | 27 | Alternative items rules |
| OV "Listing/exclusion procedures" (OV04) | T684 | — | PARTIAL-27 | 27 | Listing gap |
| OVKF — Free goods: condition type NA00, procedure NA0001, determination | T685, KOTN | Inclusive/exclusive, calculation rule | PARTIAL-26 | 26 | Free goods gap |
| OVSD / OVSE / OVSG — Sales activities (CAS) customizing | — | — | OUT-OF-SCOPE | — | CRM non-goal |
| OMO1 / MC18 / MC21 / MC24 — SIS info structures, update rules, update groups | TMC*, S0xx | — | SUPERSEDED-46 | 46 | Lakehouse + semantic layer |
| OVB6 / V/86 — Define rounding (unit of measure rounding, rounding profiles OVR) | T006R, TVRPL | Rounding profiles for order qty | COVERED-27 | 27 | Order quantity rounding (UoM); smart rounding for prices (wave 26) |
| OVRQ / Define units of measure groups, delivery units, minimum order qty (material master) | T006, MVKE | — | COVERED-8 | 8 | Product sales attributes |
| Advanced Returns Management settings: return reasons, refund types, inspection codes, follow-up activities, logistical follow-up | MSR_* | — | COVERED-30 | 30 | Disposition codes |
| RAR customizing: accounting principles, POB types, SSP determination, allocation methods, contract categories, inflight checks | FARR_C_* | — | COVERED-29 | 29 | Rev rec configuration |
| Payment card customizing (CCARD): card types, authorization horizon, merchant IDs, clearing house interface | TCCAA, TCCT | — | COVERED-30 | 30 | Payment adapter |
| Define sales deal / promotion types (OVB / VB(2) | T6B1, T6B2 | — | COVERED-26 | 26 | — |
| Intercompany billing settings (IV billing type, internal customer per sales org, OVV9 / OVV8) | TVKO, T001, TVVF | — | COVERED-40 | 40 | — |
| Define number ranges SD documents (VN01, VN02, VN03, VN04) | NRIV | — | COVERED-5 | 5 | — |
| Define sales document field selection / screen sequence (SHD0 transaction variants) | — | — | SUPERSEDED-5 | 5 | Metadata-driven forms + personalization |
| Mass maintenance of SD conditions (VK12 scales), condition index (V/I / V/I7), condition table index | — | — | COVERED-26 | 26 | — |

---

## 13.20 SD Rebates / Settlement Management (condition contracts)

SAP context: ECC SD rebate processing — rebate agreements (VBO1/VBO2/VBO3), accruals via condition types BO01–BO06 on billing, settlement (VB(7), partial settlement, retroactive rebates (VBOF), rebate index rebuild (VBOF/OVB3). S/4HANA replaces this with Settlement Management (condition contracts WCOCO, business volume determination, settlement runs WB2R_SC, settlement documents, accruals; also supplier rebates / chargebacks / commissions). Passage: wave 26 (rebate agreements + accrual), wave 30 (rebate settlement), wave 19 (vendor side via AP).

### 13.20.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| VBO1 / VBO2 / VBO3 | Rebate agreement create/change/display (types 0001 group, 0002 material, 0003 customer, 0004 hierarchy, 0005 independent of sales volume) | Customer rebate agreements | COVERED-26 | 26 | Rebate agreements and accrual calculation |
| VB(D | Extend rebate agreement (validity) | — | COVERED-26 | 26 | — |
| VB(7 | Rebate settlement (partial/final; creates credit memo request B1/B3) | Settlement | COVERED-30 | 30 | Rebate settlement from wave 26 accruals |
| VBOF | Update billing documents (retroactive rebate accrual) | Retroactive agreements | PARTIAL-26 | 26 | Retroactive rebate (apply agreement to already-invoiced volume) not named — partial |
| VB(8 / VBOE | Rebate index: list / rebuild | — | SUPERSEDED-26 | 26 | Accruals computed from journal/billing events |
| WCOCO (S/4) / Fiori: Manage Condition Contracts | Condition contract create/change (customer, supplier, commission; contract types) | Settlement management master | COVERED-26 | 26 | Agreement model covers customer rebates; supplier rebates (vendor-side accrual + AP credit) — confirm wave 19/26 cover supplier rebates; see below |
| Supplier condition contracts (purchasing rebates, chargebacks) | Vendor rebates / ship-and-debit chargebacks | — | PARTIAL-19 | 19 | Supplier rebate accrual and settlement (credit from vendor) and chargeback/ship-and-debit claims are not named in waves 17/19 — partial; propose wave 26 engine reused for purchasing with settlement via AP in wave 19 |
| Commission contracts (sales rep commissions via settlement management) | Commissions | — | PARTIAL-26 | 26 | "Commission calculation hooks" only — partial; settlement to payroll/AP not named |
| WB2R_BUSVOL / Fiori: Business Volume | Business volume determination (condition contract business volume selection) | — | COVERED-26 | 26 | Accrual base from billed/shipped volume |
| WB2R_SC / WB2R_SV | Settlement run (single / mass) → settlement documents (WB2R_SD display) | — | COVERED-30 | 30 | — |
| Settlement documents (credit memo / invoice) WB2R_* / Fiori: Manage Settlement Documents | Settlement documents with accounting | — | COVERED-30 | 30 | Credit note / vendor invoice from settlement |
| Fiori: Condition Contract Overview / Settlement Dates / Accruals monitor | — | — | COVERED-26 | 26 | — |
| Promotion pricing / trade promotion management (TPM, CRM) | Trade promotions with funds | — | OUT-OF-SCOPE | — | CRM TPM non-goal; wave 26 promotions cover pricing side |
| Chargeback / ship-and-debit claims from distributors | Claim validation against agreements | — | GAP | propose 30 | Distributor chargeback claims (EDI 844/849) validation and settlement — gap for manufacturers selling via distribution; propose wave 30 + wave 35 |
| Royalty / licensing payments (settlement management use) | Royalty accrual/settlement | — | GAP | propose 26 | Royalty accrual by sales volume (license fees to third parties) — gap; reuse rebate engine with vendor-side settlement |

### 13.20.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| VB(8 / VBO3 → sales volume / verification level | Rebate agreement sales volume & accrual verification | COVERED-26 | 26 | Explain accrual |
| VB(5 | Rebate agreement list | COVERED-26 | 26 | — |
| S_ALR_87012 (rebate accruals in FI) | Accrual balances | COVERED-26 | 26 | Accrual account reconciliation |
| Fiori: Condition Contract Overview / Business Volume analytics / Settlement document list | — | COVERED-26 | 26 | — |
| WB2R_* reports: settlement document list, accrual list | — | COVERED-30 | 30 | — |

### 13.20.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| VB(7 (periodic settlement run) | Partial settlement per period | COVERED-30 | 30 | — |
| VBOF (batch) | Retroactive accrual update | PARTIAL-26 | 26 | — |
| WB2R_SC (batch) / settlement calendar | Settlement run by settlement date type | COVERED-30 | 30 | — |
| Accrual run / accrual reversal (settlement management) | Periodic accrual posting | COVERED-26 | 26 | Rebate accrual job |
| WB2R_BUSVOL (batch) | Business volume update | COVERED-26 | 26 | — |
| Delta accruals / delta settlement | Delta settlement after retro changes | PARTIAL-26 | 26 | Retroactive gap |

### 13.20.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OVB3 / VB(2 — Rebate agreement types, condition type groups, rebate condition types (BO01-BO06), accrual account keys (ERB/ERU) | T6B1, T6B2, T685, T687 | Manual accruals, settlement periods, verification levels | COVERED-26 | 26 | Rebate agreement types |
| OVB1 / OVB2 — Activate rebate processing (sales org, billing type, payer) | TVKO, TVFK, KNVV | — | COVERED-26 | 26 | — |
| V/08 rebate steps in pricing procedure (BO01 subtotal, statistical accrual) | T683S | — | COVERED-26 | 26 | — |
| Settlement management: condition contract types, settlement process types, settlement document types, business volume selection criteria, settlement calendars, accrual settings, pricing procedures for settlement (WB2R) | WCB_*, WB2_* | Customer/supplier/commission contract types, settlement date types (partial/final/delta), accrual condition types | COVERED-26 | 26 | Agreement types + settlement calendars (wave 30 settlement) |
| Settlement management: account determination for settlement docs, tax determination | T030, WB2_* | — | COVERED-30 | 30 | — |
| Chargeback / claim types | — | — | GAP | propose 30 | — |
| Commission settlement integration (HR/AP) | — | — | PARTIAL-26 | 26 | — |

---

## 13.21 PP — Production Planning (master data, MRP, SOP/demand management, production orders, capacity, KANBAN, REM)

SAP context: PP-BD (material MRP views, BOMs CS01, work centers CR01, routings CA01, production versions C223, ECM CC01), PP-MRP (MD01/MD02/MD03, MD04 stock/requirements, MD05/MD06 MRP lists, planned orders MD11–MD16, planning file MD20/MD21, MRP areas, MRP Live MD01N), PP-MP (SOP MC87/MC88, flexible planning MC93/MC94, demand management MD61–MD63, PIR transfer MC74/MC75, long-term planning MS01–MS70), PP-SFC (production orders CO01–CO15, COOIS, COHV, CO40/CO41 conversion, confirmations CO11N, COGI, availability check, TECO/close), PP-CRP (capacity CM01–CM07, leveling CM21–CM29, CM25 planning table), PP-KAB (control cycles PK01, kanban board PK13N), PP-REM (REM profile, planning table MF50, backflush MFBF, cost collectors), PP-PDC. Passage: wave 9 (BOM/routing/work centers/ECM), wave 21 (production orders, confirmations, WIP), wave 22 (MRP/MPS/forecasting), wave 23 (capacity/scheduling), wave 24 (lean/kanban/REM), wave 25 (shop floor/PDC), wave 39 (costing).

### 13.21.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| MM01 / MM02 / MM03 (MRP 1-4, work scheduling views) | Material master planning data: MRP type, lot size, procurement type, special procurement, safety stock, planning strategy, scheduling margin key, availability check, production version, backflush, batch entry | Planning parameters per item/site | COVERED-22 | 22 | Coverage groups (requirement/period/min-max), lead times, safety stock, planning strategy on released product; wave 8 for master |
| MM02 → MRP areas (MD61 per MRP area) | MRP area assignment (storage location / subcontractor level planning) | — | PARTIAL-22 | 22 | Planning at warehouse (sub-site) level and at subcontractor level not explicitly named — partial; wave 22 plans by site; add "planning scope = site or warehouse" |
| CS01 / CS02 / CS03 | Material BOM create/change/display (usage 1 production, 2 engineering, 3 universal; alternatives; item categories L/N/R/D/T/K/M/I) | BOMs | COVERED-9 | 9 | BOM header/version/lines with effective dates, phantoms, scrap, by-/co-products |
| CS05 / CS06 / CS07 / CS08 | Change/display BOM group / plant assignment BOM (allocate BOM to plant) | — | COVERED-9 | 9 | BOM versions per site |
| CS11 / CS12 / CS13 | BOM explosion: level by level / multilevel / summarized | — | COVERED-9 | 9 | Explosion API |
| CS14 | BOM comparison | — | COVERED-9 | 9 | Version diff — confirm; treat covered |
| CS15 | Where-used list (single-level/multilevel) | — | COVERED-9 | 9 | Where-used |
| CS20 | Mass change BOM | — | COVERED-9 | 9 | Mass change with ECO |
| CS40 / CS41 / CS42 | Configurable BOM (super BOM) assignment / create / change | Super BOM for VC | COVERED-8 | 8 | Configurator BOM with constraints |
| CS51 / CS52 / CS53 / CS61 / CS62 / CS63 / CS71 … | Standard BOM / document BOM / equipment BOM / functional location BOM / sales order BOM / WBS BOM | Other BOM categories | PARTIAL-9 | 9 | Material BOM yes; equipment/functional location BOMs in wave 44; sales-order BOMs (order-specific) and WBS BOMs not named — partial (ETO) |
| CS80 / CS81 / CS82 | BOM change documents | — | COVERED-5 | 5 | Audit |
| CR01 / CR02 / CR03 | Work center create/change/display (capacities, scheduling formulas, costing activity types, default values, hierarchy) | Resources | COVERED-9 | 9 | Resources (machine/tool/labor/vendor/location) with capabilities, calendars, cost categories |
| CR05 / CR06 / CR07 / CR08 | Work center list / assignment to cost center / capacity list / hierarchy | — | COVERED-9 | 9 | — |
| CR11 / CR12 / CR13 | Capacity create/change/display (pooled capacities) | Capacity master | COVERED-9 | 9 | Resource capacity with shifts |
| CR21 / CR22 / CR23 | Work center hierarchy create/change/display | Resource groups | COVERED-9 | 9 | Resource groups |
| CRC1 / CRC2 / CRC3 | Resource (PP-PI) create/change/display | PI resources | COVERED-9 | 9 | Same resource model |
| CA01 / CA02 / CA03 | Routing create/change/display (operations, sequences — standard/alternative/parallel, component allocation, PRTs, inspection characteristics, user fields) | Routings | COVERED-9 | 9 | Routing versions, operations, operation relations (sequential/parallel), setup/run times, component allocation — confirm component-to-operation allocation; treat covered |
| CA11 / CA12 / CA13 | Reference operation set create/change/display | Reusable operation sequences | COVERED-9 | 9 | Operation templates — confirm; treat covered |
| CA21 / CA22 / CA23 | Rate routing (REM) create/change/display | — | COVERED-24 | 24 | Production flow / rate routing |
| CA31 / CA32 / CA33 | Reference rate routing | — | COVERED-24 | 24 | — |
| CA80 / CA81 / CA82 / CA83 / CA85 / CA85N / CA90 | Where-used (work center / PRT / ref op set), mass change routings, routing change documents | — | COVERED-9 | 9 | — |
| CA97 / CA97N | Update material master (lot size data from routing) | — | COVERED-9 | 9 | Lead time roll-up from routing (CA97) — covered by routing capacity calc proof |
| CA98 / CA99 | Delete routings / display task list change docs | — | COVERED-9 | 9 | — |
| C223 | Production versions (BOM alternative + routing + lot size range + validity) | Production versions | COVERED-9 | 9 | BOM/route version pairing per site/quantity — confirm production-version concept (AX has BOM/route versions separately); treat covered |
| CF01 / CF02 / CF03 / CF10 … | Production resources/tools (PRT: material/equipment/document/misc) | Tools | COVERED-9 | 9 | Resource type "tool" |
| CC01 / CC02 / CC03 / CC04 / CC05 / CC06 / CC07 | Engineering change master (ECR/ECO) create/change/display/info system/alternative dates/leading change master | ECM | COVERED-9 | 9 | ECR/ECO workflow with effective dates, impact analysis |
| CC11 / CC12 / CC13 | Change request (ECR) create/change/display | — | COVERED-9 | 9 | — |
| CC31 / CC32 / CC33 | Change package / change number release | — | COVERED-9 | 9 | — |
| CEWB | Engineering workbench (mass BOM/routing maintenance) | — | COVERED-9 | 9 | Engineering workspace grid |
| MD61 / MD62 / MD63 | Planned independent requirements (PIR) create/change/display | Forecast/demand input per strategy | COVERED-22 | 22 | Forecasts with reduction/consumption modes |
| MD64 / MD65 / MD66 / MD67 | PIR: create/change planning table / display / split / copy | — | COVERED-22 | 22 | — |
| MD70 / MD71 / MD72 / MD73 / MD74 / MD75 / MD76 | Copy PIR from forecast / planning / delete old PIRs / reorganize | — | COVERED-22 | 22 | — |
| MD81 / MD82 / MD83 | Customer independent requirements | — | COVERED-22 | 22 | Manual demand lines — confirm; treat covered |
| MP30 / MP31 / MP32 / MP33 / MP38 / MP39 | Material forecast (execute / change / display parameters / total forecast) | Statistical forecast | COVERED-22 | 22 | ETS/Croston forecast service with manual override |
| MC87 / MC88 / MC89 | SOP standard: create/change/display plan (product group / material) | Sales & operations planning | COVERED-22 | 22 | Forecast consensus workflow (S&OP) — rough-cut capacity planning (MC88 RCCP) partially via wave 23 capacity load; treat covered |
| MC84 / MC85 / MC86 | Product group create/display/change (SOP aggregation) | Planning hierarchies | COVERED-22 | 22 | Forecast at product family level with disaggregation — confirm; treat covered |
| MC93 / MC94 / MC95 / MC96 | Flexible planning: create/change/display/copy plan versions (info structures) | Flexible planning | SUPERSEDED-22 | 22 | Planning on lakehouse/semantic layer + forecast service (wave 46/22) |
| MC74 / MC75 / MC76 | Transfer SOP to demand management (material / product group / disaggregate) | — | COVERED-22 | 22 | Consensus → PIR |
| MC8B / MC8E / MC8V | Event, mass processing SOP | — | COVERED-22 | 22 | — |
| MS01 / MS02 / MS03 / MS04 / MS05 / MS06 / MS07 / MS08 | Long-term planning (LTP): planning scenario, simulative MRP, stock/requirements (simulation), evaluations | Simulation planning | PARTIAL-22 | 22 | What-if simulation plans (separate planning versions with simulated demand, evaluating capacity/purchasing) not explicitly named — partial; propose "static plans" (AX-style) in wave 22 |
| MS31 / MS32 / MS33 / MS44 / MS47 / MS64 / MS65 / MS70 | LTP: planning scenario create/change/display, planned orders in scenario, evaluation (capacity, PURCHIS transfer) | — | PARTIAL-22 | 22 | Same |
| MD01 / MD02 / MD03 | MRP total planning / single-item multilevel / single-item single-level | MRP run | COVERED-22 | 22 | Planning engine with regenerative/net-change modes |
| MD01N (MRP Live, S/4) | MRP Live (HANA-based total run) | — | COVERED-22 | 22 | 50k items < 10 min target |
| MD40 / MD41 / MD42 / MD43 | MPS total / single-item multilevel / single-level / interactive | MPS | COVERED-22 | 22 | MPS with firming (planning time fence) |
| MD50 / MD51 | MRP for sales order / project | Order-specific MRP (MTO) | COVERED-22 | 22 | Pegged planning for MTO — covered via pegging; sales-order stock partial noted in MM-IM |
| MD04 | Stock/requirements list (current) | Net requirements view | COVERED-22 | 22 | Net requirements / pegging inquiry |
| MD05 / MD06 | MRP list (individual / collective) | Planning results at run time | COVERED-22 | 22 | Plan run results snapshot |
| MD07 | Stock/requirements list collective (by MRP controller, exception groups, traffic lights) | Planner worklist | COVERED-22 | 22 | Planner workspace with action messages |
| MD09 | Pegged requirements | Pegging | COVERED-22 | 22 | Pegging tree |
| MD4C | Multilevel order report | Order network | COVERED-22 | 22 | Explain-this-plan |
| MD11 / MD12 / MD13 / MD16 / MD19 | Planned order create/change/display/collective display/firm | Planned orders | COVERED-22 | 22 | Planned orders with firming, grouping |
| MD14 / MD15 / MD17 | Convert planned order → PR (individual / collective) / conversion collective PR | Conversion | COVERED-22 | 22 | Firm → requisition/PO |
| CO40 / CO41 / CO48 | Convert planned order → production order (individual / collective / partial) | Conversion | COVERED-21 | 21 | Firm → production order |
| MD20 / MD21 | Create / display planning file entry | Net-change triggers | SUPERSEDED-22 | 22 | Event-driven net change |
| MD25 / MD26 / MD27 | Planning calendar create/change/display | Period lot-sizing calendars | COVERED-22 | 22 | Period coverage calendars — confirm custom period calendars; treat covered |
| MDAB / MDRE | Planning file setup / consistency check | — | SUPERSEDED-22 | 22 | — |
| MDBT | MRP run in background (variants) | — | COVERED-22 | 22 | Scheduled nightly plan |
| MDVP | Collective availability check (planned orders) | — | COVERED-22 | 22 | — |
| MDSA / MDSB | Planned orders with/without components (BOM explosion numbers) | — | COVERED-22 | 22 | — |
| MF50 | REM planning table (rate-based line loading) | Lean planning board | COVERED-24 | 24 | Production flow / kanban board; rate-based planning — confirm line-loading planning table; treat covered |
| MFBF / MF41 / MF42N / MF70 | REM backflush (assembly/component/activity) / reverse / collective / aggregate collective confirmation | REM execution | COVERED-24 | 24 | Backflush costing for lean (periodic) |
| MF60 / MF63 / MF65 / MF68 | Pull list (material staging) / stock transfer / situation / printing | Material staging | COVERED-24 | 24 | Kanban/withdrawal + wave 31 staging |
| MF12 / MF26 / MF27 | Document log / display postprocessing errors | — | COVERED-24 | 24 | — |
| MF30 / KKF6N | Create product cost collector (REM) | — | COVERED-24 | 24 | — |
| MFPR | Production version check (REM) | — | COVERED-24 | 24 | — |
| PK01 / PK02 / PK03 / PKMC | Control cycle create/change/display / maintenance (S/4) | Kanban rules (supply area, container qty, replenishment strategy: in-house/external/stock transfer, status sequence) | COVERED-24 | 24 | Kanban rules (manufacturing/withdrawal/fixed/event) |
| PK05 / PK05S | Supply area create / display | Supply areas (PSA) | COVERED-24 | 24 | Supply areas / kanban locations |
| PK13N / PK12N / PK11 | Kanban board (demand source / supply source / status change) | Kanban board | COVERED-24 | 24 | Kanban board, cards |
| PK21 / PK22 / PK23 / PK31 / PK41 / PK51 | Kanban status change (barcode), kanban correction, event-driven kanban (PK23), kanban calculation (PK41), card print | — | COVERED-24 | 24 | Event kanbans; card printing (wave 32 labels); kanban quantity calculation (PK41/PK51) — confirm kanban quantity calculation; treat covered |
| PK17 / PK18 | Kanban plan/actual, kanban evaluation | — | COVERED-24 | 24 | — |
| CO01 / CO02 / CO03 | Production order create/change/display (with material) | Production order | COVERED-21 | 21 | Production order lifecycle |
| CO07 / CO08 / CO10 | Production order without material / for sales order / for project | Variants | COVERED-21 | 21 | Rework orders (CO07), MTO orders (CO08), project orders (wave 41) |
| CO05N / CO05 | Collective release of orders | — | COVERED-21 | 21 | Mass release |
| CO11N / CO11 / CO12 / CO14 / CO15 / CO16N / CO13 | Confirmations: single operation (time ticket) / fast entry / collective / display / order confirmation / postprocessing errors / cancel | Confirmations | COVERED-21 | 21 | Report as finished + route card/job card confirmations; shop-floor kiosk (wave 25) |
| CO1F / CO1L / CO1P / CO1V | Progress confirmation / confirmation list / predefined confirmation processes / fast entry | — | COVERED-21 | 21 | — |
| COGI | Postprocess failed goods movements (backflush errors) | Error queue | COVERED-21 | 21 | Backflush error queue with retry — confirm; essential; treat covered |
| CO27 / CO28 | Picking list / collective picking | Component picking | COVERED-21 | 21 | Picking lists (backflush/manual) |
| MB1A 261 / MIGO GI for order, MB31 / MIGO GR for order (101), 531 by-product, 545 | Goods movements for production orders | — | COVERED-21 | 21 | Material issues, RAF with by-/co-products |
| CO02 → TECO / CLSD / mass via COHV | Technically complete / close orders | Status transitions | COVERED-21 | 21 | Lifecycle state machine (end) |
| COHV / COHVPI / COMAC | Mass processing of production orders (release, print, schedule, costing, TECO, close, availability check) | Mass processing | COVERED-21 | 21 | Mass operations with job framework |
| COOIS / COOISPI | Order information system (orders, operations, components, confirmations, documented goods movements, capacities, PRTs) | Inquiry | COVERED-21 | 21 | Production order inquiry: cost roll, status log, transactions |
| CO24 | Missing parts information system | Shortages | COVERED-21 | 21 | Component shortage list (ATP on components) |
| CO46 / CO50 | Order progress report / collective | — | COVERED-21 | 21 | — |
| CO04 / CO04N | Print order papers (shop papers: job ticket, pick list, time ticket, confirmation slip) | Shop papers | COVERED-21 | 21 | Route cards, job cards; document designer wave 46 |
| CORS / CO88 (see CO-PC) | — | — | COVERED-21 | 21 | — |
| CM01 / CM02 / CM03 / CM04 / CM05 | Capacity planning: load (standard overview) / orders / pool / backlog / overload | Capacity evaluation | COVERED-23 | 23 | Capacity load views by resource/group/period |
| CM07 / CM50 / CM51 / CM52 | Variable capacity evaluation / work center view / order view / extended | — | COVERED-23 | 23 | — |
| CM21 / CM22 / CM23 / CM25 / CM26 / CM27 / CM28 / CM29 / CM31 | Capacity leveling: planning table (graphical/tabular), dispatching, work center view, individual capacity | Finite scheduling / dispatching | COVERED-23 | 23 | Interactive Gantt with drag-reschedule; finite scheduling |
| CM40 / CM41 | Capacity leveling in background / evaluation of background leveling | — | COVERED-23 | 23 | Scheduler service batch |
| CM99 / CM38 | Capacity leveling profiles | — | COVERED-23 | 23 | — |
| CR15 / CR16 | Where-used work center / capacity | — | COVERED-9 | 9 | — |
| Production scheduling board (Fiori Manage Production Operations / Schedule Production), PP/DS board | — | — | COVERED-23 | 23 | Gantt |
| Fiori: Manage Production Orders / Confirm Production Operation / Monitor Production Orders / Manage Material Coverage / Monitor Material Coverage / Display MRP Master Data Issues / Manage Change Requests (MRP) / Schedule MRP Runs / Manage Planned Orders / Manage Supply Demand / Monitor External Requirements / Monitor Internal Requirements | S/4 PP Fiori apps | — | COVERED-22 | 22 | Planner workspace |
| Fiori: Manage Work Center Capacity / Schedule Production / Monitor Work Center Schedules / Monitor Capacity Utilization | — | — | COVERED-23 | 23 | — |
| Fiori: Kanban Board / Manage Control Cycles | — | — | COVERED-24 | 24 | — |
| Fiori: Manage Production Versions / Manage Bills of Material / Manage Routings / Manage Work Centers / Manage Change Records (ECM) | — | — | COVERED-9 | 9 | — |
| Demand-driven replenishment (DDMRP: buffer positioning, buffer levels, replenishment planning, Fiori apps) | DDMRP (S/4) | — | GAP | propose 22 | DDMRP (decoupling points, buffer zones red/yellow/green, net flow equation) not named; propose as wave 22 coverage-group type "DDMRP buffer" |
| Predictive MRP (pMRP, S/4) | Rough simulation of future demand vs capacity | — | PARTIAL-22 | 22 | Overlaps LTP partial |
| Production planning with PP/DS — see §13.23 | — | — | COVERED-23 | 23 | — |
| Subcontracting with MRP (special procurement 30), components provided, subcontract orders from planned orders | — | — | COVERED-21 | 21 | Subcontracting operations with vendor PO linkage (wave 21) + MRP (wave 22) |
| Co-products / by-products in BOM & order settlement (apportionment structure) | — | — | COVERED-24 | 24 | Co/by-product costing allocation |
| Phantom assemblies, alternative items (BOM item alternative groups), discontinuation (MM02 follow-up material) | — | — | PARTIAL-9 | 9 | Phantoms yes; alternative item groups (priority/strategy) and discontinuation/follow-up (run-out) are not named — partial; propose wave 9 BOM line attributes + wave 22 MRP logic |
| Assembly processing / assemble-to-order (planning strategies 81/82) | ATO: production order created from sales order with dynamic ATP | — | COVERED-27 | 27 | Order with configured product creates production/planned order linkage |
| Make-to-order with sales order stock (strategy 20), planning with final assembly (40), planning without final assembly (50), subassembly planning (70) | Planning strategies | — | PARTIAL-22 | 22 | Coverage groups + forecast reduction named; the full strategy-group semantics (consumption modes, MTO stock segmentation, planning at assembly level) are partially covered — partial; sales-order stock noted in MM-IM |
| Scrap: assembly scrap, component scrap, operation scrap (routing), net ID | Scrap planning | — | COVERED-9 | 9 | BOM scrap and route scrap — confirm operation scrap on routing; treat covered |
| Rework (CO07 / trigger points CO31 / CO33) | Rework orders / trigger points inserting rework operations | — | PARTIAL-21 | 21 | Rework via order w/o material covered; trigger points (automatic rework operation insertion on confirmation) not named — partial |
| Serial numbers in production orders (header/components), batch determination in production (COB1) | — | — | COVERED-21 | 21 | Tracking dimensions |
| Order split (CO02 → split), order change management (OCM: COCM / COCM1) | Order split / order change management after ECO | — | PARTIAL-21 | 21 | Order change management (propagating BOM/routing changes to open orders with change costs) not named — partial; propose wave 9/21 |

### 13.21.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| MD04 / MD05 / MD06 / MD07 | Stock/requirements and MRP lists | COVERED-22 | 22 | — |
| MD09 / MD4C | Pegging / order report | COVERED-22 | 22 | — |
| MD45 / MD46 / MD47 / MD48 | Evaluation of planning result (individual/collective) / product group planning / cross-plant evaluation | COVERED-22 | 22 | — |
| MDLD | Print MRP list | COVERED-22 | 22 | — |
| MD44 / MD4x | Planning situation (material/product group) | COVERED-22 | 22 | — |
| COOIS / COOISPI | Production order info system (all list variants) | COVERED-21 | 21 | — |
| CO24 | Missing parts | COVERED-21 | 21 | — |
| CO26 / CO28 | Order information system: planned/production orders | COVERED-21 | 21 | — |
| COID | Object overview (order info system) | COVERED-21 | 21 | — |
| CM01 … CM07 | Capacity evaluations | COVERED-23 | 23 | — |
| CS11 / CS12 / CS13 / CS15 | BOM explosion / where-used | COVERED-9 | 9 | — |
| CA80 / CA81 / CA82 | Routing where-used | COVERED-9 | 9 | — |
| CC04 | Change master information system | COVERED-9 | 9 | — |
| MCP1 / MCP3 / MCP5 / MCP7 / MCPB / MCPD / MCPF (PP-IS, Shop Floor Information System) | Work center analysis / operation analysis / material analysis / production order analysis (lead times, quantities, scrap) | COVERED-46 | 46 | Production analytics (OEE in wave 25) |
| MCRE / MCRI / MCRM (REM analysis) / MCRP / MCRK (kanban analysis) | REM/kanban LIS | COVERED-46 | 46 | — |
| MCP6 / MCPK | Work center capacity analysis (LIS) | COVERED-46 | 46 | — |
| MD4 / S_ALR "Planning file entries" | — | SUPERSEDED-22 | 22 | — |
| PK17 / PK18 / PK19 | Kanban evaluations (plan vs actual, status history) | COVERED-24 | 24 | — |
| MF26 / MF12 | REM document log / postprocessing list | COVERED-24 | 24 | — |
| Fiori: Production Order Overview / Production Execution analytics / Production Cost Analysis / OEE dashboards (SAP Digital Manufacturing) | — | COVERED-25 | 25 | OEE |
| Fiori: MRP Cockpit (Monitor Material Coverage / Shortages), Display MRP Key Figures | — | COVERED-22 | 22 | — |

### 13.21.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| MD01 / MDBT / MD01N (scheduled) | Nightly MRP / MRP Live run | COVERED-22 | 22 | — |
| MD40 (scheduled) | MPS run | COVERED-22 | 22 | — |
| MP38 / MPBT | Mass forecast run | COVERED-22 | 22 | Forecast batch |
| MC8E / MC8D / MC8G | SOP mass processing (copy, transfer) | COVERED-22 | 22 | — |
| MD74 / MD75 / MD76 | Reorganize PIRs (delete history, adjust) | COVERED-22 | 22 | — |
| MD15 / CO41 (batch) | Mass conversion of planned orders | COVERED-22 | 22 | — |
| CO05N / COHV (batch release, print, costing) | Mass release / scheduling / costing | COVERED-21 | 21 | — |
| COGI (periodic) | Backflush error reprocessing | COVERED-21 | 21 | — |
| CO1P | Predefined confirmation processes (background) | COVERED-21 | 21 | — |
| CM40 (scheduled) | Background capacity leveling | COVERED-23 | 23 | — |
| CO88 / KKAX / KKS1 (see CO-PC) | Settlement / WIP / variances | COVERED-21 | 21 | — |
| MFBF / MF70 (periodic) / KK87 (REM settlement) | REM period-end | COVERED-24 | 24 | — |
| PK41 / PK51 (kanban calculation run) | Kanban quantity recalculation | COVERED-24 | 24 | — |
| CORUPROC / CORUAFWP | Confirmation background process (actual costs, goods movements) | SUPERSEDED-21 | 21 | Synchronous posting via journal/inventory APIs; job framework for retries |
| SARA PP_ORDER / PR_ORDER | Archiving production orders | COVERED-48 | 48 | — |
| CA97 (periodic) | Update lot-size dependent lead times | COVERED-9 | 9 | — |
| Fiori: Schedule MRP Runs / Schedule Production Order Jobs | — | COVERED-4 | 4 | — |

### 13.21.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OS20 / OS21 / OS22 / OS23 / OS24 / OS25 / OS27 / OS28 / OS30 — BOM: usages, status, item categories, default values, modification parameters, BOM explosion/applications, alternative determination | T415S, T416, T418, TCS | Item category control (stock/non-stock/variable size/document/text), usage-specific item status | COVERED-9 | 9 | BOM line types and usages |
| OPJM / OPJI / OPJF — Selection ID for alternative BOM/routing (application, priorities) | TCS, T407 | — | COVERED-9 | 9 | Version selection rules |
| OPJL — Define checking groups / checking rules for order availability (OPJJ scope, OPJK checking control per plant/order type/business event: component, PRT, capacity check; release behaviour) | T441V, T441R, T399X | Availability check at creation/release; collective conversion rules | COVERED-21 | 21 | Component availability on release |
| OPJH — Production order types (PP01, PP02…) | T003O | Number range, settlement profile, residence time, status profile, CO partner update, classification | COVERED-21 | 21 | Order types |
| OPL8 — Order type dependent parameters (planning: production scheduling profile, routing selection, BOM application; implementation: documentation of goods movements; cost accounting: costing variants, RA key) | T399X | — | COVERED-21 | 21 | Order type parameters |
| OPKP — Production scheduling profiles (automatic release/GR/print at creation, batch management, capacity leveling flags, overall profile) | TCO43 | — | COVERED-21 | 21 | Production scheduling profile → order defaults |
| OPJ / OPU3 / OPU5 — Scheduling parameters production orders / planned orders (scheduling type: forward/backward/today, reduction strategies, capacity requirements generation, scheduling horizon) | T399D, TCX | Lead time scheduling vs duration, reduction levels, float before/after (OPJ scheduling margin key OMI8) | COVERED-23 | 23 | Scheduler settings (forward/backward, finite/infinite) |
| OPK4 — Confirmation parameters (per plant/order type): goods movements (backflush), actual costs posting, error handling, propose dates/quantities, decoupled/synchronous, HR integration (time tickets), termination on error | TCORU, T399 | — | COVERED-21 | 21 | Confirmation behaviour |
| OPK0 / OPK1 / OPK2 / OPK3 — Confirmation screens, single-screen entry layout, reasons for variances | TCORV, T* | — | COVERED-21 | 21 | Reason codes for scrap/variance |
| OPKF / OPK8 — Print control (shop papers): lists, forms, print programs | T496 | — | COVERED-21 | 21 | Document designer |
| OPJB / BS02 — Status profiles (user statuses for orders) | TJ30 | — | COVERED-4 | 4 | — |
| OPJ2 / OPJ — Define number ranges production orders / confirmations / reservations | NRIV | — | COVERED-5 | 5 | — |
| OPJ6 / OPJ7 / OPJ8 — Production order field selection, component/operation defaults | T* | — | COVERED-3 | 3 | — |
| OPPQ — Plant parameters for MRP (number ranges, planning run defaults, floats, scheduling margin keys, direct procurement, BOM explosion, planning file, external procurement defaults, rescheduling horizon, exception messages) | T399D, T436A (margin keys) | — | COVERED-22 | 22 | Planning parameters per site |
| OPPR — MRP groups (strategy group, consumption mode, planning horizon, creation indicators) | T438M | — | COVERED-22 | 22 | Coverage group / planning group |
| OMDQ / OMDR — MRP types (PD, VB, VM, VV, ND, M0-M4, P1-P4 with firming types, planning time fence, roll-forward) | T438A | Reorder point, forecast-based, MPS, time fence, firming types 0-4 | COVERED-22 | 22 | Coverage groups: requirement/period/min-max; firming/time fences |
| OMI4 — Lot-sizing procedures (EX, FX, HB, WB, MB, TB, PK, optimum: dynamic/least unit cost/part period balancing, rounding, min/max, takt) | T439A | Static/period/optimum, scheduling, rounding profiles | COVERED-22 | 22 | Lot-sizing: requirement/period/min-max/fixed/multiples/EOQ — confirm optimum procedures (least-unit-cost, part-period) — treat covered with note |
| OMD9 — Special procurement types (subcontracting 30, phantom 50, stock transfer 40, withdrawal from alternative plant, direct production 52, production in other plant 70/80) | T460A | — | COVERED-22 | 22 | Procurement type per item/site incl. transfer from other site (transfer order gap noted) |
| OMD0 — MRP controllers | T024D | — | COVERED-22 | 22 | Planner assignments |
| OMDX / OMDU — Activate MRP / planning file per plant | T399D, MDLV | — | SUPERSEDED-22 | 22 | — |
| OMDY / OMD3 — Exception messages, exception groups | T458A, T458B | — | COVERED-22 | 22 | Action/futures messages |
| OPPS / OPPT — Planning strategies / strategy groups (10, 11, 20, 25, 30, 40, 50, 52, 59, 60, 63, 70, 74, 81, 82…) | T461S, T461P | Requirements type, consumption, assembly processing | PARTIAL-22 | 22 | Planning strategy semantics partial (noted) |
| OMPO / OMI — Consumption mode, consumption periods (backward/forward) | T438M | — | COVERED-22 | 22 | Forecast reduction |
| OMIK / OMI8 — Scheduling margin key, floats | T436A | — | COVERED-22 | 22 | Safety margins |
| OMIZ / MRP areas (OMIZ / S/4 MRP areas per storage location, subcontractor) | MDLV, MDLG, MDLW | — | PARTIAL-22 | 22 | MRP area partial |
| OMIX / OMD — Define planning horizon, rescheduling check (tolerances), firm receipts handling | T399D | — | COVERED-22 | 22 | Action message tolerances |
| OMDT / Safety stock availability %, dynamic safety stock (range of coverage profiles OMIA) | T438R | — | COVERED-22 | 22 | Safety stock + period coverage — confirm range-of-coverage profile (dynamic safety stock) — treat covered |
| MC7F / OMPB / OMPC — SOP: info structure S076 planning parameters, planning types (MC8A/MC8B), macros, period split, aggregation/disaggregation (proportional factors MC9) | S076, T445, TMC | — | COVERED-22 | 22 | Forecast hierarchy disaggregation |
| OMPN / OMPK — Demand management: requirement types, PIR versions, consumption, requirements class | T459K, T459A, T459U | — | COVERED-22 | 22 | — |
| MP80 / OMPH / OMPG — Forecast profiles, forecast parameters (model selection, alpha/beta/gamma, periods) | T* , MPOP | Constant, trend, seasonal, Croston | COVERED-22 | 22 | Forecast models |
| OPPB / OPPP — Planning calendars | T439 | — | COVERED-22 | 22 | — |
| OP40 / OP41 / OP42 / OP43 / OP44 / OP45 … — Work center: categories, field selection, default values, capacity categories, formulas, standard value keys, performance efficiency rate, shift definitions, shift sequences, move time matrix, location groups | TC26, TC27, TC30, TC31, TC37, TC24, T430, T435, T436 | Formulas for setup/processing/teardown/capacity; standard value keys (SAP1, SAP2); shifts | COVERED-9 | 9 | Resources: capabilities, calendars with shifts, time formulas — confirm formula-based durations (quantity-dependent) — treat covered |
| OP00 / OP01 / OP4 — Capacity planning profiles (overall profiles, selection, time, evaluation, strategy, period profiles for CM21/CM25), dispatching strategy (finite, insert, midpoint, date entry, sequence dependent setup) | TCY* | — | COVERED-23 | 23 | Scheduler settings; setup optimization |
| OP4A / OP4B — Capacity reduction, available capacity versions, intervals of available capacity | TC26, KAKO, KAZY | — | COVERED-23 | 23 | Capacity versions |
| OP5A / OP8B / OP8 — Routing: task list types, usages, status, control keys (OPJ8 OP67 / OP00 — control key: scheduling, capacity, costing, confirmation required, external processing, print, milestone), operation default values, number ranges, user fields, PRT control keys | TCA*, T430 (control keys), T430T | Control key flags | COVERED-9 | 9 | Operation control attributes (schedule/cost/confirm/external) |
| OP67 / OPJ — Control keys (PP01, PP02, PP03 external, PP99 milestone) | T430 | — | COVERED-9 | 9 | — |
| OPMP / OPM — Trigger points (standard trigger points, functions) | TTRIG | — | PARTIAL-21 | 21 | Trigger point gap |
| OS40 / OS41 / OS44 — ECM: change types, object types, status, ECR/ECO profiles, release keys, effectivity types (date, serial, parameter) | TCC*, TECH* | Date effectivity vs parameter/serial effectivity | PARTIAL-9 | 9 | Date effectivity covered; serial-number/parameter effectivity not named — partial |
| OPZ / OSPT / OSP2 — REM profiles (reporting point backflush, cost collector, GI/GR posting, aggregate backflush, RP confirmation) | T437S | Reporting points (milestones), pull-list, cost collector | COVERED-24 | 24 | Lean profiles |
| OSP / OPKA — Kanban: control cycle categories, replenishment strategies (in-house: 0001 production order, 0002 run schedule, 0003 manual KANBAN; external: PO, SA, source list; stock transfer: reservation, TO), status sequences, kanban calculation profiles, print control | TPKM, TPK* | — | COVERED-24 | 24 | Kanban rules types |
| OPKW / OPKX — PDC (plant data collection) interfaces: CC2/CC4 communication parameters, upload/download, time events | TCORU, T* | — | COVERED-25 | 25 | Shop-floor kiosk + IoT replaces PDC; HR time events in wave 25 |
| OPJG / OPK — Batch determination in production (search procedure, strategy types), serial number profiles per order type | TLSP, T* | — | COVERED-21 | 21 | — |
| OPJW / OPJ — Define default values for conversion (planned → production), order types per MRP group | T399X, T438M | — | COVERED-22 | 22 | — |
| OPPE / OPPI — Default document types for PRs from MRP, purchasing group, account assignment | T399D | — | COVERED-22 | 22 | — |
| OPJJ — Availability check scope (production) | T441V | — | COVERED-21 | 21 | — |
| OPJK — Checking control (order type × plant × business transaction) | T399X / T441R | — | COVERED-21 | 21 | — |
| OMK / OMI — Define MRP areas, MRP list layout, MD04 navigation profiles, period totals | T* | — | COVERED-22 | 22 | — |
| OPPS "Assembly processing" settings (order type for ATO) | T461S | — | COVERED-27 | 27 | — |
| DDMRP buffer profiles, classification runs (S/4 DD) | PPH_DD_* | — | GAP | propose 22 | DDMRP gap |
| OCM profiles (order change management: change profiles, change cost) | COCM* | — | PARTIAL-21 | 21 | OCM partial |

---

## 13.22 PP-PI — Production Planning for Process Industries

SAP context: master recipes (C201–C203; recipe header, operations/phases, resources, materials, secondary resources, process instructions/XSteps), process orders (COR1–COR3, COR5 collective, COR6N confirmation, CORR? mass), process management (control recipes CO53, process messages CO54/CO57, PI sheets CO60), electronic batch record (COEBR), batch management and batch determination, resource networks (CMX? / RN?), campaign planning (PP-PI campaigns), process manufacturing cockpit (CO50?), material quantity calculation. Passage: wave 24 (formulas/batch orders, potency, co/by-products, catch weight, recipe version control), wave 21 (order lifecycle), wave 25 (shop floor/PI sheets), wave 33 (QM in process).

### 13.22.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| C201 / C202 / C203 | Master recipe create/change/display (recipe group, operations, phases, material list, resources, process instructions, QM characteristics, relationships) | Recipes | COVERED-24 | 24 | Formula + route (recipe) with version control integrated with ECM |
| C223 (PI) | Production versions (recipe + formula) | — | COVERED-24 | 24 | — |
| C251 / C252 / C253 | Print recipes / recipe lists / where-used | — | COVERED-24 | 24 | — |
| C260 / C261 | Change recipe with ECM / recipe change documents | — | COVERED-9 | 9 | — |
| C298 | Mass deletion of recipes | — | COVERED-24 | 24 | — |
| CRC1 / CRC2 / CRC3 | Resource (PI) create/change/display (resource category: processing unit, labor, storage, vessel) | Resources | COVERED-9 | 9 | Resource model |
| CRC5 / CRC | Resource network (RN) | Resource networks for scheduling | PARTIAL-23 | 23 | Resource networks (allowed sequences of resources, e.g. vessel → filler) not named — partial; scheduling with CP-SAT could model as constraints |
| CS01 (BOM for PI = formula) / material quantity calculation (C201 → MQC formulas) | Formula with material quantity calculation (active ingredient scaling, yield) | — | COVERED-24 | 24 | Potency-based consumption, scalable quantities |
| COR1 / COR2 / COR3 | Process order create/change/display | Batch/process orders | COVERED-24 | 24 | Batch orders from formulas |
| COR5 / CORS / COR7 / COR7_PI / COR8 | Collective release / cancel / create from planned order (individual / collective) | — | COVERED-24 | 24 | — |
| COR6 / COR6N / CORK / CORR / CORT / CORZ | Confirmation: phase / order / collective / display / cancel | Confirmations | COVERED-24 | 24 | Batch order reporting (RAF with co/by-products) |
| CORA / CORM / CORP | Process order scheduling / mass processing (COHVPI) | — | COVERED-24 | 24 | — |
| CO53 / CO53XT | Control recipe monitor (send control recipes to process control / PI sheet) | Process management | PARTIAL-25 | 25 | Shop-floor instructions/drawings on kiosk; structured *control recipes* with process instruction categories sent to a destination (PCS/PI sheet) are partial |
| CO54 / CO54XT / CO57 / CO58 | Process message monitor / create process message manually / send | Process messages (from shop floor/PCS → ERP: consumption, yield, quality results) | PARTIAL-25 | 25 | IoT/MQTT machine events + kiosk reporting cover inbound; configurable message categories with destinations (e.g. to QM results, to batch record) partial |
| CO60 / CO60XT / CO60_PI / CO64 / CO67 | PI sheet: find/maintain / worklist / print / archive | Electronic work instructions (XSteps/PI sheets) | PARTIAL-25 | 25 | Kiosk instructions; a designer for interactive PI sheets (XSteps with input fields, calculations, signatures, process data requests) is not named — partial |
| COEBR | Electronic batch record (EBR) creation/approval/archive (21 CFR Part 11) | Batch record | GAP | propose 24 | EBR with digital signatures, deviation review and archiving (GxP) not named — propose wave 24 (+ wave 48 compliance) |
| CMX / Standard XSteps (CMXSV) / SXS repository | Standard XSteps repository | — | PARTIAL-25 | 25 | Same as PI sheet |
| CO50 (Process manufacturing cockpit) / COPOC | Process operator cockpit | — | COVERED-25 | 25 | Shop-floor kiosk |
| MSC1N / MSC2N / MSC3N / MSC4N (batch master — see §13.33) | Batch create/change/display/changes | — | COVERED-24 | 24 | — |
| Campaign planning (PP-PI campaign, process order campaigns: COCP / CMP) | Group process orders into campaigns (setup/cleanout) | — | PARTIAL-23 | 23 | Campaign/sequence planning (group orders to minimize changeover, cleanout orders) not named — partial; CP-SAT setup optimization partly covers |
| Cleanout / changeover orders, recipe-dependent cleaning | — | — | PARTIAL-23 | 23 | Same |
| Digital signature in PI sheets / batch records (DSIG) | 21 CFR Part 11 signatures | — | GAP | propose 24 | E-signature framework (meaning of signature, re-authentication) not named; propose cross-cutting in wave 24/48 |
| Shelf life / batch expiry, batch-specific UoM (potency UoM, proportion/product units) | — | — | COVERED-24 | 24 | Batch attributes, shelf life, catch weight dual UoM, potency |
| Batch derivation (DVMO / DVMAN) — see §13.33 | — | — | PARTIAL-24 | 24 | Batch derivation (inherit attributes from component batches to product batch) not named — partial |
| Fiori: Manage Process Orders / Confirm Process Order Phase / Monitor Process Orders | S/4 apps | — | COVERED-24 | 24 | — |

### 13.22.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| COOISPI | Process order information system | COVERED-24 | 24 | — |
| CO54 (monitor) | Process message log | PARTIAL-25 | 25 | — |
| COEBR (list) | Batch record list | GAP | propose 24 | — |
| MB56 / BMBC | Batch where-used / batch information cockpit | COVERED-24 | 24 | Batch traceability (top-down/bottom-up) |
| MCP (PP-PI LIS: resource analysis, phase analysis) | — | COVERED-46 | 46 | — |
| C251 recipe lists | — | COVERED-24 | 24 | — |
| CMX/CO60 worklists | PI sheet worklist | PARTIAL-25 | 25 | — |

### 13.22.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| COR5 / COHVPI (batch) | Mass release / scheduling | COVERED-24 | 24 | — |
| CO53 (send control recipes, batch) | — | PARTIAL-25 | 25 | — |
| CO54 (process message processing, batch) | — | PARTIAL-25 | 25 | — |
| CO64 / CO67 (PI sheet archive) | — | PARTIAL-25 | 25 | — |
| COEBR (batch record generation/archive) | — | GAP | propose 24 | — |
| CO88 / KKAX / KKS1 (process orders) | Settlement / WIP / variances | COVERED-21 | 21 | — |
| Campaign settlement / cleanout cost allocation | — | PARTIAL-23 | 23 | — |
| SARA PR_ORDER | Archive process orders | COVERED-48 | 48 | — |

### 13.22.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Master recipe: profiles, task list types (2), status, control keys (PI01, PI02…), phase/operation categories, relationship types | TCA*, T430 | — | COVERED-24 | 24 | — |
| Process order types (PI01…), order type dependent parameters (COR4), production scheduling profile (CORY), confirmation parameters (CORO / OPK4 PI), availability check (OPJJ/OPJK PI) | T003O, T399X, TCO43 | — | COVERED-24 | 24 | — |
| Material quantity calculation: formulas, functions | — | — | COVERED-24 | 24 | Potency scaling |
| Process management: process instruction categories/characteristics (O10C O12C / O13C), process message categories & destinations (O05C / O06C / O07C), control recipe destinations (O10C), PI sheet layouts, XStep settings (SXS), signature strategies (O2 / DSIG) | TPM*, CPM* | Destinations: PI sheet, process control system (PCS via PI-PCS interface), ERP functions (goods movement, confirmation, QM results) | PARTIAL-25 | 25 | PI sheet / message framework partial |
| Resource networks | — | — | PARTIAL-23 | 23 | — |
| Campaign planning settings | — | — | PARTIAL-23 | 23 | — |
| EBR: batch record profiles, archiving, approval workflow | — | — | GAP | propose 24 | — |
| Batch management in PP-PI: batch determination procedures for process orders (COB1), classification (023), batch derivation | TLSP, T* | — | PARTIAL-24 | 24 | Derivation partial |
| Co-product apportionment structures (CK / OPJ) | — | — | COVERED-24 | 24 | Allocation methods |

---

## 13.23 PP/DS — Advanced Planning & Detailed Scheduling (APO PP/DS and S/4 embedded PP/DS)

SAP context: APO PP/DS (CIF integration CFM1/CFM2, product view /SAPAPO/RRP3, detailed scheduling planning board /SAPAPO/CDPS0, heuristics, optimizer, pegging, alert monitor /SAPAPO/AMON1, product planning table /SAPAPO/PPT1, PP/DS runs /SAPAPO/CDPSB0, order views /SAPAPO/RRP4, block planning, setup matrices, characteristic-dependent planning (CDP), MRP heuristics, shelf-life planning). S/4HANA embedded PP/DS: advanced planning flag on material, PP/DS planning via MD04/Fiori, same planning board. Passage: wave 23 (scheduler service with finite/infinite, OR-Tools CP-SAT optimization, Gantt, bottlenecks) + wave 22 (pegging). PP/DS-specific supply-chain-wide planning (SNP, GATP) outside this component.

### 13.23.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| CFM1 / CFM2 / CFM3 / CFM4 / CFM5 / CFM6 | CIF integration models: generate / activate / display / deactivate / delete / change transfer | ERP ↔ APO master & transactional data | SUPERSEDED-23 | 23 | Embedded single system, Kafka events; no CIF |
| CFC9 / CFG1 / CFP1 / CFP3 | CIF settings / application log / queue display | — | SUPERSEDED-23 | 23 | — |
| /SAPAPO/MAT1 / /SAPAPO/LOC3 / /SAPAPO/RES01 / /SAPAPO/PDS_DISP / /SAPAPO/SCC03 (PPM) | APO product / location / resource / PDS (production data structure) / PPM master | Planning master data | SUPERSEDED-23 | 23 | Native product/site/resource/BOM-route data (waves 6/8/9) |
| /SAPAPO/RRP3 | Product view (receipts/requirements, pegging, order creation) | Planner view | COVERED-22 | 22 | Net requirements view with pegging |
| /SAPAPO/RRP4 / /SAPAPO/RRP7 | Receipts view / product overview | — | COVERED-22 | 22 | — |
| /SAPAPO/CDPS0 / /SAPAPO/CDPS1 / CDPSC | Detailed scheduling planning board (variable/interactive), DS strategies | Gantt scheduling | COVERED-23 | 23 | Interactive Gantt with drag-reschedule |
| /SAPAPO/PPT1 | Product planning table (REM/line loading) | — | COVERED-24 | 24 | Lean/rate-based board |
| /SAPAPO/CDPSB0 / /SAPAPO/CDPSB1 | Production planning run (heuristics: SAP_PP_002 planning standard lots, SAP_PP_003, SAP_MRP_001, SAP_PP_C001 bottom-up, SAP_PP_SL001 shelf life…) in background / display | Planning run | COVERED-22 | 22 | Planning engine runs with pluggable heuristics (lot sizing); bottleneck/multi-level heuristics via CP-SAT |
| /SAPAPO/OPT (PP/DS optimizer profiles & runs), /SAPAPO/CDPSOPT | Optimizer (genetic algorithm / constraint propagation; objectives: makespan, lateness, setup time/cost, mode cost) | Optimization | COVERED-23 | 23 | CP-SAT objectives: minimize lateness/makespan; setup optimization |
| Setup matrices / setup groups (/SAPAPO/CDPSC4 / CDPSC5) | Sequence-dependent setup | — | COVERED-23 | 23 | Setup optimization (sequence-dependent setup matrix) — confirm matrix data model; treat covered |
| Block planning (resource blocks per characteristic, /SAPAPO/CDPSC12) | Reserve capacity blocks by product characteristics | — | GAP | propose 23 | Block planning (time blocks on resources restricting which characteristic values can be produced) not named — propose wave 23 constraint type |
| Characteristic-dependent planning (CDP) / CDP configuration | Planning by batch characteristics (e.g., color, grade) | — | PARTIAL-24 | 24 | Batch attributes exist; planning that matches demand characteristics to supply batches (CDP) — partial; propose wave 22/24 |
| Shelf-life planning (SAP_PP_SL001) | Pegging respecting expiry (FEFO planning) | — | PARTIAL-22 | 22 | FEFO picking (wave 16) yes; planning-time shelf-life pegging (min remaining shelf life at demand date) not named — partial |
| Pegging (dynamic/fixed), /SAPAPO/PEG fixed pegging | Pegging | — | COVERED-22 | 22 | Pegging tree; fixed pegging (manual lock) — confirm "firming"; treat covered |
| /SAPAPO/AMON1 / /SAPAPO/AMON3 | Alert monitor (alert profiles, overall profiles) | Exceptions | COVERED-22 | 22 | Action/delay messages + wave 5 alerts |
| /SAPAPO/RRPCUST1 / /SAPAPO/CDPSC (strategy profiles, planning board profiles) | Planning strategies (finite/infinite, insert/append/squeeze-in, planning direction, scheduling mode) | — | COVERED-23 | 23 | Scheduler strategies |
| /SAPAPO/MC01 (models/versions), /SAPAPO/MVM | Planning versions (simulation versions) | — | PARTIAL-22 | 22 | Simulation versions = LTP partial |
| /SAPAPO/SDP94 (DP), /SAPAPO/SNP94 (SNP), GATP (/SAPAPO/AC03) | Demand planning / supply network planning / global ATP | — | PARTIAL-22 | 22 | DP ≈ wave 22 forecasting (statistical + consensus); SNP (multi-site network optimization: sourcing across plants/DCs with transport lanes and capacity) and GATP (rules-based ATP, multi-site substitution) are not named — partial; register multi-site network planning as a v1.x candidate under wave 22/27 |
| S/4 embedded PP/DS: "Advanced planning" flag (MM02), PP/DS planning via MD04 extension, Fiori: Monitor Capacity Utilization / Schedule Production / Production Scheduling Board | — | — | COVERED-23 | 23 | — |
| Multi-resource planning (modes, alternative resources, secondary resources) | — | — | COVERED-23 | 23 | Primary/secondary resources, alternative resources via capabilities |
| Finite scheduling with time-continuous capacity, calendar, breaks, downtime (from PM) | — | — | COVERED-23 | 23 | Downtime from wave 44 affects capacity |

### 13.23.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| /SAPAPO/RRP3 / RRP4 / RRP7 | Product/receipt views | COVERED-22 | 22 | — |
| /SAPAPO/CDPS0 (views: resource, order, operation) | DS board views | COVERED-23 | 23 | — |
| /SAPAPO/AMON1 | Alert monitor | COVERED-22 | 22 | — |
| /SAPAPO/PEG1 | Pegging overview | COVERED-22 | 22 | — |
| /SAPAPO/OPT11 (optimizer logs) | Optimizer explanation | COVERED-23 | 23 | Explain schedule (constraint warnings) |
| /SAPAPO/CDPSC11 (resource load / capacity utilization) | Capacity utilization | COVERED-23 | 23 | — |
| Fiori: Monitor Capacity Utilization / Monitor Resource Schedule | — | COVERED-23 | 23 | — |
| /SAPAPO/CCR (CIF comparison/reconciliation) | ERP vs APO consistency | SUPERSEDED-23 | 23 | Single system |

### 13.23.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| /SAPAPO/CDPSB0 (scheduled planning runs with heuristic steps) | Nightly PP/DS run | COVERED-22 | 22 | — |
| Optimizer background runs | — | COVERED-23 | 23 | — |
| /SAPAPO/RLCDEL (delete old orders), /SAPAPO/OM17 (consistency) | Housekeeping | SUPERSEDED-23 | 23 | — |
| CIF delta/initial transfers, queue monitoring (SMQ1/SMQ2, /SAPAPO/CQ) | — | SUPERSEDED-23 | 23 | — |
| Conversion of planned orders (PP/DS → ERP), /SAPAPO/RRP_ | — | COVERED-22 | 22 | — |
| Alert determination runs | — | COVERED-22 | 22 | — |

### 13.23.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Global settings for PP/DS (planning version, pegging, order types), planning procedures (per product), heuristics (algorithms, settings), heuristic profiles | /SAPAPO/* | Automatic planning immediately / in planning run; planning procedure 1-5 | COVERED-22 | 22 | Planning policies per coverage group |
| Strategy profiles (scheduling modes, planning direction, finiteness level, validity periods, propagation ranges), planning board profiles, time profiles, work areas | /SAPAPO/* | — | COVERED-23 | 23 | — |
| Optimizer profiles (objectives weights, runtime, modes), setup matrices | /SAPAPO/* | — | COVERED-23 | 23 | — |
| Alert profiles / alert types | /SAPAPO/* | — | COVERED-22 | 22 | — |
| CIF: integration model settings, publication types, filter objects, error handling (CFC) | CIF* | — | SUPERSEDED-23 | 23 | — |
| Block planning settings, CDP settings | — | — | GAP | propose 23 | Block planning gap |
| Embedded PP/DS activation (S/4): advanced planning settings, PP/DS material flag defaults | — | — | SUPERSEDED-23 | 23 | — |
| Resource categories (single/multi/mixed), capacity variants, breaks, shifts, downtimes | /SAPAPO/RES* | — | COVERED-9 | 9 | — |

---

## 13.24 QM — Quality Management

SAP context: QM-PT (inspection plans QP01, master inspection characteristics QS21, inspection methods QS31, catalogs QS41, sampling procedures QDV1, sampling schemes QDP1, dynamic modification rules QDB1, quality info records QI01/QV51), QM-IM (inspection lots QA01/QA32, results recording QE01/QE51N, defects recording, usage decisions QA11, stock postings), QM-QC (control charts QGC1, quality level QDL1), QM-QN (quality notifications QM01, tasks, 8D), QM-CA (certificates QC01/QC21, CoA on delivery), QM-IT (test equipment calibration via PM), vendor evaluation integration, stability studies (QST01), audit management (PLMD_AUDIT), FMEA, control plan (S/4 QM). Passage: wave 33 (tests, sampling, associations, quality orders, UD, blocking, NCR/CAPA, CoA, scorecards, SPC, calibration), wave 16 (quarantine status), wave 18 (receive to QI).

### 13.24.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| QS21 / QS22 / QS23 / QS24 / QS25 / QS26 / QS27 / QS28 | Master inspection characteristic create/change/display (quantitative/qualitative, tolerances, catalogs, sampling) / versions / where-used / list / replace | Test definitions | COVERED-33 | 33 | Tests (variable/attribute) with tolerances, catalogs |
| QS31 / QS32 / QS33 / QS34 / QS37 / QS38 / QS39 | Inspection method create/change/display/versions/where-used/list | Test methods | COVERED-33 | 33 | Test instructions/methods |
| QS41 / QS42 / QS43 / QS44 / QS45 / QS46 / QS47 / QS48 / QS49 | Catalogs: code groups/codes (characteristic attributes, defect types, defect locations, causes, tasks, activities, usage decisions) create/change/display/list; selected sets (QS51 / QS52 / QS53) | Code catalogs | COVERED-33 | 33 | Catalogs: attribute values, defect codes, cause codes, disposition codes |
| QP01 / QP02 / QP03 / QP05 / QP06 / QP07 / QP08 | Inspection plan create/change/display/print/list/mass change/where-used (operations, characteristics, inspection points, test equipment) | Inspection plans | COVERED-33 | 33 | Test groups + quality associations |
| QP11 / QP12 / QP13 | Reference operation set (QM) | — | COVERED-33 | 33 | Test group templates |
| QDV1 / QDV2 / QDV3 / QDV6 / QDV7 | Sampling procedure create/change/display/uses/replace (sample size: fixed, %, sampling scheme, 100%; valuation mode: attributive, variable, manual) | Sampling | COVERED-33 | 33 | Sampling plans (AQL per ANSI/ASQ Z1.4) |
| QDP1 / QDP2 / QDP3 | Sampling scheme create/change/display (AQL tables, inspection severity) | — | COVERED-33 | 33 | — |
| QDB1 / QDB2 / QDB3 | Dynamic modification rule create/change/display (skip lots, tightened/reduced/normal) | Skip-lot / dynamic modification | COVERED-33 | 33 | Inspection skip rules based on quality level — confirm skip-lot (switching rules) in wave 33 sampling; treat covered |
| QDL1 / QDL2 / QDL3 | Quality level create/change/display | — | COVERED-33 | 33 | — |
| QI01 / QI02 / QI03 / QI05 / QI06 / QI07 / QI08 | Quality info record procurement (vendor release, QM control key, certificate requirement, inspection skip) / mass change / list / release status | Vendor-material quality agreement | COVERED-33 | 33 | Supplier/item quality status (approved, blocked, probation) |
| QV51 / QV52 / QV53 | Quality info record sales (CoA profile per customer) | — | COVERED-33 | 33 | CoA per customer profile |
| QC01 / QC02 / QC03 | Certificate profile create/change/display | CoA layout | COVERED-33 | 33 | CoA templates |
| QC11 / QC12 / QC13 / QC15 | Certificate profile assignment (material/customer) | — | COVERED-33 | 33 | — |
| QC21 / QC22 | Quality certificate for batch / for inspection lot (print) | CoA output | COVERED-33 | 33 | CoA generation on shipments |
| QC51 / QC52 / QC53 / QC55 | Certificate receipt (incoming certificates from vendor) | Incoming CoA tracking | PARTIAL-33 | 33 | Incoming vendor certificate receipt/tracking (required certificate at GR, block until received) not named — partial |
| QA01 / QA02 / QA03 | Inspection lot create/change/display (manual; automatic at GR 01, production 03/04, delivery 10, stock transfer 08, recurring 09, calibration 14) | Quality orders | COVERED-33 | 33 | Quality orders generated by associations (receipt/production/shipment); recurring inspection (09) — confirm recurring/periodic re-inspection of batches; treat covered |
| QA32 / QA33 | Inspection lot worklist: change / display (results, UD, stock posting) | Worklist | COVERED-33 | 33 | Quality workspace |
| QA11 / QA12 / QA13 / QA14 | Usage decision record/change/display/change with history | Disposition | COVERED-33 | 33 | Usage decisions with inventory blocking/release |
| QA16 / QA17 | Collective usage decision / job planning for UD | — | COVERED-33 | 33 | Mass UD |
| QE01 / QE02 / QE03 / QE11 / QE12 / QE13 / QE17 | Results recording (per lot / operation / inspection point / for test equipment) | Results | COVERED-33 | 33 | Results recording (variable/attribute) |
| QE51N / QE52 / QE72 | Results recording worklist / defects worklist | — | COVERED-33 | 33 | — |
| QF01 / QF02 / QF03 / QF11 | Defects recording (lot / operation / characteristic) | Defects | COVERED-33 | 33 | Nonconformance capture |
| QA07 / QA08 | Deadline monitoring (recurring inspection, shelf life) / mass change inspection setup in material master | — | COVERED-33 | 33 | Periodic inspection triggers |
| QA10 / QA05 / QA06 | Trigger recurring inspection / job planning | — | COVERED-33 | 33 | — |
| QE71 / QE72 / QE73 | Tabular results recording (inspection points / characteristics / lots) | Mass results | COVERED-33 | 33 | Grid results entry |
| QM01 / QM02 / QM03 | Quality notification create/change/display (types Q1 customer complaint, Q2 vendor complaint, Q3 internal problem; items, causes, tasks, activities, 8D) | NCR / complaints | COVERED-33 | 33 | Nonconformance → correction/CAPA (8D) workflow; customer complaint and supplier complaint types — confirm customer complaint type links to RMA (wave 30); treat covered |
| QM10 / QM11 / QM12 / QM13 / QM14 / QM19 | Notification lists (worklist, tasks, items, activities) | — | COVERED-33 | 33 | — |
| QM50 / QM (notification cost / order) | QM order for notification costs (appraisal cost) | — | COVERED-33 | 33 | Cost of quality |
| QK01 / QK02 / QK04 | Assign QM order to material / settlement (appraisal costs) | — | COVERED-33 | 33 | Cost of quality |
| QGC1 / QGC2 / QGC3 | Control charts (SPC) for inspection lots / characteristics / tabular | SPC | COVERED-33 | 33 | SPC charts from results |
| QGA3 / QGA2 / QGP1 / QGP2 | Results history / quality score / inspection results analysis | — | COVERED-33 | 33 | — |
| QST01 / QST02 / QST03 / QST04 / QST05 / QST06 / QST07 | Stability study (initial sample, storage conditions, testing schedule) | Stability | GAP | propose 33 | Stability studies (pharma/food shelf-life testing schedules) not named; propose wave 33 extension for regulated process manufacturers |
| QE17 / IE01 (test equipment as equipment, calibration inspection type 14, IP10 calibration plans) | Calibration inspection | — | COVERED-33 | 33 | Device/instrument calibration tracking |
| PLMD_AUDIT (Audit management: audits, questions, corrective actions) | Audit management | — | PARTIAL-33 | 33 | CAPA yes; audit planning/questionnaires/audit findings (ISO 9001 internal audits, supplier audits) not named — partial |
| FMEA (QM-FMEA, S/4), control plan (S/4 QM) | FMEA / control plan | — | GAP | propose 33 | FMEA and control plans (APQP/PPAP) — gap for automotive/aerospace suppliers; propose wave 33 extension |
| Quality inspection in EWM (QIE) / inspection at warehouse | — | — | COVERED-33 | 33 | — |
| Source inspection (QI01 source inspection flag, QI07) | Inspection at vendor before GR | — | PARTIAL-33 | 33 | Source inspection lots (before delivery) not named — partial |
| Sample management (QPR1 / QPR2 / QPR3 / QPR5 / QPR6 / QPR7) | Physical samples, sample drawing procedure, sample labels | — | PARTIAL-33 | 33 | Sampling sizes yes; physical sample records (primary/pooled samples with labels, retention samples) not named — partial |
| Multiple specifications (customer/country-specific specs, S/4 "Multiple Specifications") | — | — | PARTIAL-33 | 33 | Customer-specific test specs/limits — partial |
| Fiori: Manage Inspection Lots / Record Inspection Results / Record Defects / Manage Quality Tasks / Manage Quality Notifications / Process Inspection Lots / Manage Usage Decisions | S/4 apps | — | COVERED-33 | 33 | — |
| Fiori: Manage Quality Levels / Manage Inspection Plans / Manage Master Inspection Characteristics / Manage Sampling Procedures / Manage Sampling Schemes | — | — | COVERED-33 | 33 | — |

### 13.24.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| QA32 / QA33 | Inspection lot lists | COVERED-33 | 33 | — |
| QM10 / QM11 | Notification lists | COVERED-33 | 33 | — |
| QGA3 / QGA | Results history / quality score | COVERED-33 | 33 | — |
| QGP1 | Results for inspection lot / characteristic (graphics) | COVERED-33 | 33 | — |
| QGC1 … QGC3 | Control charts | COVERED-33 | 33 | — |
| MCXA / MCXB / MCXC / MCXD / MCXE / MCXF / MCXG / MCXI / MCXK / MCXM / MCXP / MCXV / MCXX (QMIS) | QMIS: inspection lots (material/vendor/customer), notifications, results, defects, quality scores, problems (vendor/customer/material) | COVERED-46 | 46 | Quality analytics; supplier scorecards (wave 33) |
| MCVA / MCVB / MCVC / MCVD | QMIS problem analyses (notifications) | COVERED-46 | 46 | — |
| QS28 / QS38 / QS49 | Master data lists (MICs, methods, catalogs) | COVERED-33 | 33 | — |
| QP06 / QP07 | Inspection plan lists / where-used | COVERED-33 | 33 | — |
| QI06 | Quality info records list | COVERED-33 | 33 | — |
| QA07 (list) | Deadline monitoring list | COVERED-33 | 33 | — |
| QC55 | Certificate receipt overview | PARTIAL-33 | 33 | — |
| ME6H / ME64 (vendor evaluation with QM score) | Supplier quality score | COVERED-33 | 33 | — |
| Fiori: Quality Technician Overview / Quality Engineer Overview / Inspection Lot Analytics / Defect Analysis / Supplier Quality | — | COVERED-46 | 46 | — |

### 13.24.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| QA07 (scheduled) | Deadline monitoring: recurring inspection lots, shelf-life expiry, batch status change | COVERED-33 | 33 | — |
| QA16 (batch UD) | Automatic usage decisions (auto UD for lots without defects) | COVERED-33 | 33 | Auto-UD rules — confirm; treat covered |
| QDL (quality level update) / dynamic modification update | Quality level update after UD | COVERED-33 | 33 | — |
| QEH / QM notification follow-up (SWU3 workflow tasks) | Task monitoring / escalation | COVERED-4 | 4 | SLA timers |
| QK04 / KO88 (QM orders settlement) | Settle appraisal costs | COVERED-33 | 33 | — |
| QST (stability schedule generation) | Stability testing schedule | GAP | propose 33 | — |
| Calibration plan scheduling (IP10/IP30 for test equipment) | — | COVERED-33 | 33 | — |
| ME63 (vendor evaluation with QM) | — | COVERED-33 | 33 | — |
| SARA QM_QMEL / QM_CONTROL | Archiving notifications/lots | COVERED-48 | 48 | — |
| Quality score calculation / quality level reorganization | — | COVERED-33 | 33 | — |

### 13.24.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| QCC0 — QM customizing entry (inspection types 01-14; inspection lot origins; assignment of inspection types to lot origins; default values per inspection type: plan usage, auto UD, skip, inspection with task list/material spec, sample calculation, QM order) | TQ30, TQ32, TQ34 | Inspection type per origin (GR, production, delivery, recurring, calibration, stock transfer, audit, customer return) | COVERED-33 | 33 | Quality associations: when/what triggers a quality order (receipt, production, shipment, return) |
| Material master QM view defaults (QM control key, inspection setup QM02) | QMAT, TQ08 | — | COVERED-33 | 33 | Item quality settings |
| QM control keys in procurement (QM in procurement active, certificate required, vendor release, technical delivery terms) | TQ08, TQ | Blocks PO/GR if vendor not released / certificate missing | COVERED-33 | 33 | Supplier quality status enforced at PO/GR — confirm enforcement at PO; treat covered |
| Define catalog types (1 characteristic attributes, 2 tasks, 3 , 8 activities, 9 defect types, E defect locations, 5 causes…), code groups | TQ15, QPGR | — | COVERED-33 | 33 | — |
| Define sampling procedures/schemes defaults; valuation modes; inspection severities; dynamic modification (lot-based/characteristic-based) | TQ | — | COVERED-33 | 33 | — |
| Define inspection points (time/quantity/freely defined), partial lots, inspection point identification | TQ79 | — | PARTIAL-33 | 33 | In-process inspection points (per time/quantity during production) partially covered by "production" trigger — partial |
| Define results recording settings: processing modes, valuation rules, characteristic weighting, defect classes (OK/critical/major/minor) | TQ | — | COVERED-33 | 33 | — |
| Define usage decision catalogs, follow-up actions (e.g. auto email, batch status change, stock posting to blocked) , UD selected sets per plant | TQ , TQ07 | Follow-up actions as function modules | COVERED-33 | 33 | Disposition actions |
| Quality notification types, screen areas, partner determination, priorities, catalogs profile, action box (follow-up functions), tasks workflow | TQ80, TQ8*, T* | Q1/Q2/Q3/F1/F2/F3/G types | COVERED-33 | 33 | NCR types; 8D template |
| Quality certificate: certificate types, profile characteristics, output determination (NACE QC) | TQ , NAST | — | COVERED-33 | 33 | — |
| QM orders: order types (QN01), settlement, costing | T003O | — | COVERED-33 | 33 | — |
| QM-IT: calibration inspection type 14, PM integration (IP10 plans with inspection), equipment categories for test equipment | TQ30, T370T | — | COVERED-33 | 33 | — |
| Vendor evaluation QM criteria (quality score, audit score, complaints) | T147* | — | COVERED-33 | 33 | — |
| Stability study customizing | — | — | GAP | propose 33 | — |
| FMEA / control plan customizing (S/4) | — | — | GAP | propose 33 | — |
| Audit management customizing (audit types, question lists) | — | — | PARTIAL-33 | 33 | — |
| Multiple specifications customizing | — | — | PARTIAL-33 | 33 | — |
| Sample management customizing (sample types, sample-drawing procedures) | — | — | PARTIAL-33 | 33 | — |
| Define number ranges for lots/notifications/MICs | NRIV | — | COVERED-5 | 5 | — |
| Define status profiles for lots/notifications (BS02) | TJ30 | — | COVERED-4 | 4 | — |

---

## 13.25 PM — Plant Maintenance (Enterprise Asset Management)

SAP context: technical objects (functional locations IL01, equipment IE01, equipment BOMs IB01, serial numbers IQ01, measuring points IK01, measurement documents IK11, object links, classification), maintenance planning (task lists IA01/IA05, maintenance plans IP01/IP41/IP42 single-cycle/strategy/multiple counter, scheduling IP10/IP30, strategies IP11), maintenance processing (notifications IW21 M1/M2/M3, orders IW31 PM01-PM06, permits, refurbishment orders, confirmations IW41, completion IW32 TECO, settlement KO88), PMIS (MCI1-MCI8), work clearance management (WCM), Linear asset management, MRS integration, mobile asset management (SAP Service and Asset Manager). Passage: wave 44 (functional locations, assets/equipment, BOMs/spares, counters/measurement, plans, requests, work orders with parts/labor/costs, IoT condition monitoring, KPIs) + wave 23 scheduling + wave 25 IoT + wave 37 fixed asset link.

### 13.25.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| IL01 / IL02 / IL03 / IL04 / IL05 | Functional location create/change/display/list/changes | Location hierarchy (structure indicator) | COVERED-44 | 44 | Functional locations |
| IL07 / IL08 / IL09 / IL10 | FL structure list / installation / dismantle / mass change | — | COVERED-44 | 44 | — |
| IL11 / IL12 / IL13 / IL14 | Reference functional location | — | COVERED-44 | 44 | FL templates — confirm; treat covered |
| IE01 / IE02 / IE03 / IE05 / IE06 / IE4N | Equipment create/change/display/list/changes/install-dismantle with goods movement | Equipment master (categories M/P/Q/S/F; serial-number link to material) | COVERED-44 | 44 | Assets/equipment hierarchy; serial-tracked equipment from shipments (wave 45 installed base) |
| IE07 / IE08 / IE10 | Equipment list (multilevel) / create equipment (standard) / mass change | — | COVERED-44 | 44 | — |
| IE31 / IE36 / IE37 | Fleet object create / list / change | Fleet management (vehicles) | PARTIAL-44 | 44 | Fleet-specific attributes (fuel, odometer, driver) not named — partial; counters cover odometer |
| IH01 / IH02 / IH03 / IH04 / IH05 / IH06 / IH08 / IH09 | Structural display: FL structure / equipment structure / where-used / list | — | COVERED-44 | 44 | Asset hierarchy inquiry |
| IB01 / IB02 / IB03 / IB11 / IB12 / IB13 / IB05 | Equipment BOM / functional location BOM create/change/display | Spares BOM | COVERED-44 | 44 | BOMs/spares per asset |
| IQ01 / IQ02 / IQ03 / IQ04 / IQ08 / IQ09 | Serial number (material serial) create/change/display/list | Serialized material = equipment | COVERED-8 | 8 | Serial tracking dimension; equipment link (wave 44/45) |
| IK01 / IK02 / IK03 / IK07 / IK08 | Measuring point create/change/display/list/mass change (counters/measuring points) | Counters & measurement points | COVERED-44 | 44 | Counters and measurement points |
| IK11 / IK12 / IK13 / IK16 / IK17 / IK18 / IK21 / IK22 / IK31 / IK32 / IK33 / IK34 | Measurement document create/change/display/list/collective entry/reverse / transfer | Readings | COVERED-44 | 44 | Readings (manual, IoT via wave 25) |
| IA01 / IA02 / IA03 / IA05 / IA06 / IA07 / IA11 / IA12 / IA13 / IA08 / IA09 / IA10 | Task lists: equipment / general maintenance / functional location (operations, components, PRTs, maintenance packages) | Maintenance task lists | COVERED-44 | 44 | Checklists & job templates (operations, parts) — confirm multi-operation task lists with parts; treat covered |
| IP01 / IP02 / IP03 / IP41 / IP42 / IP43 / IP11 / IP12 / IP13 / IP14 / IP15 / IP16 / IP17 / IP18 | Maintenance plans: create (strategy / single-cycle / multiple counter) change / display; strategies create/change/display (packages, hierarchy, cycle sets) / mass change / lists | Preventive maintenance plans | COVERED-44 | 44 | Time/counter/condition-based plans; strategy packages (e.g. 1M/3M/1Y with hierarchy) — confirm package hierarchy; treat covered |
| IP10 / IP30 / IP30H / IP19 / IP24 / IP31 | Schedule maintenance plan (manual) / deadline monitoring (batch) / scheduling overview (graphical) / scheduling list / cost overview | Plan scheduling | COVERED-44 | 44 | Plan generation of work orders/requests |
| IW21 / IW22 / IW23 / IW24 / IW25 / IW26 | Maintenance notification create (M1 request, M2 malfunction, M3 activity)/change/display, breakdown report | Maintenance requests | COVERED-44 | 44 | Maintenance requests; downtime/breakdown reporting (affects scheduling capacity) |
| IW28 / IW29 / IW30 / IW64 / IW65 / IW66 / IW67 / IW68 / IW69 | Notification lists / activities / tasks / items / causes | — | COVERED-44 | 44 | — |
| IW31 / IW32 / IW33 / IW34 / IW36 | Maintenance order create (PM01 maintenance, PM02 preventive, PM03 refurbishment, PM04 calibration, PM05 investment, PM06)/change/display/from notification/sub-order | Work orders | COVERED-44 | 44 | Work orders with planning, parts reservation, labor, costs to asset/GL; refurbishment orders (repair spares to stock) — confirm refurbishment with valuation types; treat PARTIAL via note below |
| IW81 / IW8W | Refurbishment order create / goods receipt for refurbishment | Refurbish spares (valuation type change) | PARTIAL-44 | 44 | Refurbishment of rotables (condition-based valuation: damaged → refurbished) not named — partial; split valuation gap in MM |
| IW37 / IW37N / IW38 / IW39 / IW40 / IW49 / IW49N / IW47 / IW48 | Order lists / operations lists / multilevel list / confirmations list | — | COVERED-44 | 44 | — |
| IW41 / IW42 / IW43 / IW44 / IW45 / IW46 / IW47 / IW48 | Confirmations: individual / overall completion / display / collective / cancel / postprocessing errors | Labor confirmation | COVERED-44 | 44 | Labor time to work orders (mobile technician app wave 45; kiosk wave 25) |
| IW3D / IW3K / IW3L / IW3M | Print order / change components / list components / material where-used in orders | — | COVERED-44 | 44 | — |
| IW32 → Permits (IPMD / WCM) | Permits, work clearance management (lockout/tagout) | Safety permits | PARTIAL-44 | 44 | Permits/LOTO work clearance (WCM) not named — partial; propose wave 44 "permit/LOTO checklist" |
| IW51 / IW52 / IW53 (CS notifications) — see §13.26 | — | — | COVERED-45 | 45 | — |
| MB11 / MIGO 261/262 for PM order, MB21 reservations from orders | Parts issues/reservations | — | COVERED-44 | 44 | Parts reservation & consumption |
| ME51N from PM order (non-stock components, external services) | External services on work orders | — | COVERED-44 | 44 | Contractor services via PO — partial due to service entry sheet gap; treat covered via PO |
| CL20N (class assignment to equipment/FL), characteristics | Technical object classification | — | COVERED-8 | 8 | Classification |
| IE4N | Install/dismantle equipment with goods movement | — | COVERED-44 | 44 | — |
| Linear asset management (LAM: linear data on FL/equipment, network) | Pipelines, roads, rails | — | OUT-OF-SCOPE | — | Linear assets (utilities/rail) outside manufacturer scope |
| Asset Intelligence Network / Predictive Asset Insights / Asset Strategy & Performance Management | Cloud EAM extensions | — | PARTIAL-44 | 44 | Condition monitoring + predictive hooks named; RCM/criticality analysis (ASPM) partial |
| Work orders → fixed asset capitalization (investment orders PM05 / settlement to AuC) | Capital maintenance | — | COVERED-37 | 37 | Settlement to asset (CIP) |
| Fiori: Manage Maintenance Notifications / Orders / Create Maintenance Request / Perform Maintenance Jobs / Report Malfunction / Find Technical Objects / Manage Maintenance Plans / Schedule Maintenance Plans / Manage Work Orders (Phase model) | S/4 apps | — | COVERED-44 | 44 | — |
| Fiori: Maintenance Planner Overview / Maintenance Scheduling Board / Resource Scheduling (RSH) | Scheduling technicians | — | COVERED-23 | 23 | Scheduling against resources (wave 23) |
| SAP Service and Asset Manager (mobile) | Mobile EAM app | — | COVERED-45 | 45 | Technician mobile app (wave 45) used for maintenance too — confirm maintenance mobile in wave 44/45; treat covered |

### 13.25.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| IW28 / IW29 / IW38 / IW39 / IW37N / IW49N | Notification / order / operation lists | COVERED-44 | 44 | — |
| IH01 … IH09 | Structure displays | COVERED-44 | 44 | — |
| IK17 / IK18 | Measurement documents list | COVERED-44 | 44 | — |
| IP19 / IP24 | Scheduling overview (graphical) / list | COVERED-44 | 44 | — |
| IW3M / IW13 | Material where-used in orders / material where-used (equipment BOM) | COVERED-44 | 44 | — |
| MCI1 / MCI2 / MCI3 / MCI4 / MCI5 / MCI6 / MCI7 / MCI8 | PMIS: object class / manufacturer / location / planner group / damage analysis / object statistics / breakdown analysis (MTBF/MTTR) / cost analysis | COVERED-44 | 44 | Asset lifecycle KPIs: MTBF/MTTR, cost per asset, downtime |
| MCJB / MCJC | PMIS: MTTR/MTBF analyses | COVERED-44 | 44 | — |
| S_ALR_87013 (PM cost reports: order cost analysis, planned/actual) | — | COVERED-44 | 44 | — |
| IW37N with costs / KOB1 (PM orders) | Order cost line items | COVERED-44 | 44 | — |
| IP16 / IP17 / IP18 | Maintenance plan lists / items / cost | COVERED-44 | 44 | — |
| Fiori: Maintenance Backlog Overview / Asset Health KPIs / Technical Object Breakdowns / Maintenance Order Costs | — | COVERED-46 | 46 | — |

### 13.25.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| IP30 / IP30H (scheduled) | Deadline monitoring: generate maintenance calls (orders/notifications) | COVERED-44 | 44 | — |
| IK34 / IoT measurement document upload (MDoc interface) | Counter reading upload | COVERED-25 | 25 | IoT → readings |
| IW42 / IW44 (collective confirmation) | Confirmation batch | COVERED-44 | 44 | — |
| KO88 / KO8G (PM order settlement) | Settlement to cost center/asset | COVERED-44 | 44 | Costs → asset and GL |
| IW32 TECO/CLSD mass (IW38 → mass TECO) | Order completion | COVERED-44 | 44 | — |
| MCI LIS update / PMIS rebuild (OLI) | — | SUPERSEDED-46 | 46 | — |
| SARA PM_ORDER / PM_QMEL | Archiving | COVERED-48 | 48 | — |
| Condition-based triggers (IoT thresholds → maintenance request) | — | COVERED-44 | 44 | IoT threshold → request < 5 s |
| Capacity requirements transfer to scheduling (work center load from PM orders) | — | COVERED-23 | 23 | Downtime affects production capacity |

### 13.25.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Technical objects: structure indicators for FL (edit masks, hierarchy levels), equipment categories (M machine, P production resource, S customer equipment, Q test equipment, F fleet), equipment types, object types, planner groups, plant sections, ABC indicators, authorization groups | T370F, T370T, T370U, T024I, T357, T370C | — | COVERED-44 | 44 | FL numbering masks; equipment categories |
| Define maintenance planning plants / maintenance work centers / planner groups | T001W, T024I | — | COVERED-6 | 6 | Sites + maintenance resources |
| Notification types (M1/M2/M3), catalogs profile, priorities (OIO), screen structures, partner determination, action box, number ranges | TQ80, T352B, TQ8 | Breakdown flag, malfunction start/end, damage/cause/activity catalogs | COVERED-44 | 44 | Request types; downtime capture |
| Order types (PM01–PM06), order type ↔ planning plant, number ranges, default values (control key, cost center), settlement profiles, costing variants, budget profiles | T003O, T350, T399X, TKB1 | — | COVERED-44 | 44 | Work order types with costing |
| Control keys (PM01 internal, PM02 external, PM03 external with service), activity types | T430 | — | COVERED-44 | 44 | — |
| Maintenance plan categories (order/notification/service entry sheet), strategies, packages, scheduling parameters (shift factors, tolerance, call horizon, cycle modification) | T351*, T351X, T399W | Call horizon %, scheduling period, completion requirement | COVERED-44 | 44 | Plan scheduling parameters |
| Measurement/counter settings: characteristics (CT04), counter overflow, reversal | T* | — | COVERED-44 | 44 | — |
| Task list types (A general, E equipment, T functional location), usages, status, condition codes | TCA* | — | COVERED-44 | 44 | — |
| Refurbishment: valuation types for condition (OMWC split valuation), order type PM04 settings | T025, T003O | — | PARTIAL-44 | 44 | Refurbishment gap |
| Permits (define permit types, categories), WCM (work clearance objects, applications, approvals) | T357G, WCA* | — | PARTIAL-44 | 44 | Permit/LOTO gap |
| PMIS info structures (S061/S062/S065/S070/S115/S116), update groups | — | — | SUPERSEDED-46 | 46 | — |
| Integration: PM ↔ FI-AA (asset sync from equipment, settlement), PM ↔ MM (reservations, PRs), PM ↔ PP (capacity), PM ↔ QM (calibration), PM ↔ HR (work center persons) | — | — | COVERED-44 | 44 | — |
| Phase model for maintenance orders (S/4 "Reactive Maintenance"/"Proactive" processes, phases/subphases, approvals) | — | — | COVERED-44 | 44 | Work order state machine (wave 4) |
| Mobile EAM configuration (Service and Asset Manager) | — | — | COVERED-45 | 45 | — |

---

## 13.26 CS / Customer Service / SAP Field Service Management

SAP context: CS (SM): service notifications (IW51–IW59, S1 problem/S2 service request/S3 activity), service orders (IW31 SM01/SM02/SM03 with revenue; billing via DP90 resource-related billing, DIP profile ODP1), service contracts (VA41 WV with billing plans, service products, SLA via contract), warranties (BGM1/BGM2 master warranties, warranty check on notification/order), installed base (IB51/IB52/IB53; IBase components), repairs processing (RAS order with repair procedure, returns/loaner/repair/delivery), in-house repair (S/4 Fiori), solution database, call center (CIC), SAP Field Service Management (cloud dispatch/mobile), S/4 Service (service orders, contracts, confirmations in S/4 Service). Passage: wave 45 (installed base, warranties, agreements/SLAs, cases → work orders, repair orders, dispatch optimization, technician mobile app, parts from trucks, billing to customer/warranty, KPIs) + wave 41 (resource-related billing) + wave 30 (RMA).

### 13.26.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| IB51 / IB52 / IB53 / IB55 / IB56 / IB61 / IB62 | Installed base create/change/display/list; IBase components (equipment, materials, documents, text); IBase by sales order | Installed base | COVERED-45 | 45 | Installed base/service objects with serial tracking from shipments |
| IE01 (equipment category S customer equipment), IE4N | Customer equipment | — | COVERED-45 | 45 | — |
| BGM1 / BGM2 / BGM3 | Master warranty create/change/display (counters, services covered, validity) | Warranty terms | COVERED-45 | 45 | Warranty terms on installed base |
| IW51 / IW52 / IW53 / IW54 / IW55 / IW56 / IW57 | Service notification create (S1/S2/S3)/change/display/create from call center/complete | Cases | COVERED-45 | 45 | Cases |
| IW58 / IW59 | Service notification lists | — | COVERED-45 | 45 | — |
| IW31 (SM01/SM02/SM03) / IW32 / IW33 | Service order create/change/display (revenue-bearing, resource-related billing, DIP profile) | Service work orders | COVERED-45 | 45 | Service work orders with parts, labor, billing |
| IW34 / IW36 | Service order from notification / sub-order | — | COVERED-45 | 45 | — |
| IW41 / IW42 (service confirmations) | Technician time/parts confirmation | — | COVERED-45 | 45 | Mobile confirmations |
| DP90 / DP91 / DP93 / DP95 / DP96 / DP97 / DP98 | Resource-related billing: billing request from service order / sales order / contract; collective; display | T&M billing | COVERED-41 | 41 | T&M invoice proposals (wave 41 billing rules; wave 45 billable vs warranty lines) |
| ODP1 / ODP2 (DIP profile) — customizing | — | — | COVERED-41 | 41 | Billing rules |
| VA41 / VA42 / VA43 (WV service contract, SC, QP quantity contract with service items) | Service contracts with billing plans (periodic), SLA/response profiles, contract items (service products), price agreements | Service agreements | COVERED-45 | 45 | Service agreements (entitlements, SLAs, pricing) |
| VA01 RAS (repair order: repair procedure — return, loaner, repair, scrap, deliver) + IW32 repair orders | Repairs processing (in-house repair) | — | COVERED-45 | 45 | Repair orders; RMA (wave 30) |
| Fiori: Manage In-House Repairs (S/4) | — | — | COVERED-45 | 45 | — |
| Warranty claim processing (WTY: WTY1 warranty claims from dealers/to vendors) | Warranty claims (supplier recovery) | — | PARTIAL-45 | 45 | Customer warranty handling yes; supplier warranty claim recovery (claims to OEM/vendor, reimbursement) not named — partial |
| Service products (material type DIEN), service packages (KMAT/configurable services) | Service catalog | — | COVERED-8 | 8 | Service product type (noted partial in MM-SRV for purchasing side) |
| Solution database (IS01 / IS02 symptom/solution) | Knowledge base | — | GAP | propose 45 | Knowledge base/solution search for cases not named; propose wave 45 (or wave 47 copilot answers) |
| Customer Interaction Center (CIC0) / SAP CRM Service / S/4 Service (Service orders, service contracts, service confirmations, service quotation) | Service front office | — | PARTIAL-45 | 45 | Case management yes; omnichannel contact center (telephony/CTI, email routing) not named — partial (CRM non-goal boundary) |
| SAP FSM: Planning & dispatching board, technician mobile app, smartforms/checklists, crowd service, customer self-service portal | Field service cloud | — | COVERED-45 | 45 | Dispatch board, OR-Tools scheduling, mobile app (offline, photos, signatures, parts); checklists via wave 44 |
| Service parts planning (SPP / advanced service parts) | Spare parts planning for service networks | — | PARTIAL-22 | 22 | MRP covers; multi-echelon service parts planning partial |
| Technician van stock (storage locations per technician, mobile inventory) | Truck stock | — | COVERED-45 | 45 | Mobile inventory locations |
| Subscription billing for service contracts (SAP BRIM / Subscription Billing) | Usage-based/subscription | — | COVERED-29 | 29 | Subscription billing (recurring, proration, usage) |
| Entitlement management (S/4 Entitlement Management) | Entitlements | — | COVERED-45 | 45 | Entitlements on agreements |
| Fiori: Manage Service Orders / Service Confirmations / Service Contracts / Service Quotations / Solution Quotations / Customer Service Overview / Manage Service Requests | S/4 Service apps | — | COVERED-45 | 45 | — |

### 13.26.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| IW58 / IW59 / IW38 / IW39 (service) | Notification / order lists | COVERED-45 | 45 | — |
| IB56 / IH (IBase structure) | Installed base displays | COVERED-45 | 45 | — |
| MCI (service CS-IS: notifications, orders by customer) | Service information system | COVERED-46 | 46 | — |
| DP96 / VF04 (service billing due) | Billing requests list | COVERED-41 | 41 | — |
| Contract lists (VA45 WV), expiring contracts | — | COVERED-45 | 45 | — |
| Warranty check log (notification/order) | — | COVERED-45 | 45 | — |
| Fiori: Service KPIs (SLA attainment, first-time fix, utilization, backlog) | — | COVERED-45 | 45 | Service KPIs |
| FSM analytics / reporting | — | COVERED-46 | 46 | — |

### 13.26.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| DP90 (batch) / DP95 | Collective billing request creation | COVERED-41 | 41 | — |
| VF04 (service contracts periodic billing) | Contract billing run | COVERED-29 | 29 | — |
| IP30 for service maintenance plans (contract-based preventive visits) | Contract-based plans generate service orders | COVERED-45 | 45 | Preventive visits from agreements — confirm via wave 44 plans linked to agreements; treat covered |
| KO88 (service order settlement to profitability) | Settlement | COVERED-45 | 45 | — |
| Contract renewal/expiry run, SLA breach monitoring | — | COVERED-45 | 45 | SLA tracking |
| SARA PM_ORDER / SM notifications | Archiving | COVERED-48 | 48 | — |
| Dispatch optimization run (FSM auto-scheduling) | — | COVERED-45 | 45 | OR-Tools dispatch |

### 13.26.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Service notification types (S1/S2/S3), catalogs, priorities, response/service profiles (SLA: response time, service window) | TQ80, T* (response profiles OIS) | — | COVERED-45 | 45 | SLA profiles |
| Service order types (SM01/SM02/SM03), revenues on orders, settlement to PA/sales order, DIP profiles (ODP1: usage, characteristics, sources, material determination) | T003O, DIP* | — | COVERED-41 | 41 | Billing rules |
| Contract types (WV), contract profiles, cancellation rules, renewal, billing plan types for contracts, SLA assignment, price agreements | TVAK, TVKV, T* | — | COVERED-45 | 45 | Agreements |
| Repairs processing: repair procedures, actions (101 returns, 102 loaner, 103 repair, 104 scrap, 105 deliver), item categories IRRE/IRRP/IRRS/IRLA | T* (VRPP) | — | COVERED-45 | 45 | Repair order flows |
| Warranty: warranty types, counters, warranty check settings (notification/order) | BGMK, T* | — | COVERED-45 | 45 | — |
| IBase categories, component types | IBIN, T* | — | COVERED-45 | 45 | — |
| Warranty claim processing (WTY) customizing | — | — | PARTIAL-45 | 45 | — |
| FSM configuration (skills, regions, business rules, checklists, mobile settings) | — | — | COVERED-45 | 45 | — |
| S/4 Service: service order types, item categories, service confirmation types, service quotation | — | — | COVERED-45 | 45 | — |
| Solution database / knowledge base settings | — | — | GAP | propose 45 | — |
| CIC / contact center framework | — | — | PARTIAL-45 | 45 | — |

---

## 13.27 PS — Project System

SAP context: project definitions (CJ01/CJ06/CJ20N project builder), WBS elements (CJ11–CJ13), networks and activities (CN21–CN23, CN01 standard networks), milestones (CN11?), standard WBS (CJ91), planning (CJ40 cost planning, CJ42 revenue, CJR2 cost element planning, CJ9? capacity), budgeting (CJ30 original, CJ36 supplement, CJ37 return, CJ32 release, availability control), dates/scheduling (CJ21? CJ2B? CN24?), execution (CN25 confirmations, CAT2 timesheet), procurement (PRs from networks, PS material components), billing (billing plans on WBS/sales order, DP90 resource-related, milestone billing), period-end (CJ44 overhead, CJ8G settlement, KKA2 results analysis, CJB1 settlement rules, interest CJZ2?), reporting (CN41/CN42/CN43/CN45/CN46/CN47/CN48 structure/info system, S_ALR_87013532-87013575 cost reports, CJI3 line items, CJE0 hierarchy reports), project versions (CN72), simulation (CJV1), claims (CLM1), progress analysis (CNE1/CNE5 EVM), investment management (IM: IM01 program, IM22? IM52 budget distribution, appropriation requests IMA11). S/4: Project Control (Fiori Project Builder, Commercial Project Management CPM), Project Financials Control. Passage: wave 41 (WBS, scheduling, resources, forecasts/budgets, hour/expense/item/fee journals, project POs, billing T&M/FP/milestones/retainers, WIP/POC/RA, EVM, grants) + wave 38 budget control + wave 37 CIP.

### 13.27.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| CJ20N | Project Builder (project definition, WBS, networks, activities, milestones, documents, PS texts, materials) | Central project maintenance | COVERED-41 | 41 | Project hierarchy + WBS with scheduling |
| CJ01 / CJ02 / CJ03 / CJ06 / CJ07 / CJ08 | Project definition create/change/display (with/without template) | — | COVERED-41 | 41 | Project types & templates |
| CJ11 / CJ12 / CJ13 / CJ14 | WBS element create/change/display | WBS | COVERED-41 | 41 | — |
| CJ91 / CJ92 / CJ93 | Standard WBS create/change/display | Templates | COVERED-41 | 41 | Project templates — confirm; treat covered |
| CN01 / CN02 / CN03 | Standard network create/change/display | Network templates | COVERED-41 | 41 | — |
| CN21 / CN22 / CN23 / CN24 / CN25 / CN26 / CN27 / CN28 / CN29 | Network create/change/display / overall scheduling / confirm activity / collective confirmation / cancel confirmation / display confirmation | Networks & activities (internal, external, cost, service), relationships (FS/SS/FF/SF), scheduling | COVERED-41 | 41 | WBS scheduling respecting dependencies & resource calendars; activity confirmations (hours) |
| CN11 / CN12 / CN13 | Standard milestones | Milestones | COVERED-41 | 41 | Milestones (billing & progress) |
| CJ2B / CJ2C / CJ2D / CJ21 / CJ22 / CJ23 / CJ24 / CJ29 | Project planning board (Gantt) change/display; WBS scheduling; project scheduling | Gantt | COVERED-41 | 41 | Scheduling Gantt (reuse wave 23 Gantt) |
| CJ40 / CJ41 | WBS cost planning change/display (structure/detailed planning, cost element) | Cost planning | COVERED-41 | 41 | Forecasts/budgets |
| CJ42 / CJ43 | Revenue planning | — | COVERED-41 | 41 | — |
| CJR2 / CJR3 / CJR4 / CJR5 / CJR6 / CJR7 | Cost element / activity input planning (WBS) | — | COVERED-41 | 41 | — |
| CJ9B / CJ9BS / CJ9C / CJ9D / CJ9E / CJ9F / CJ9FS / CJ9G / CJ9K / CJ9L / CJ9M | Copy plan versions, plan → budget, copy actual to plan, delete | Planning utilities | COVERED-41 | 41 | Forecast versions |
| CJ30 / CJ31 / CJ32 / CJ33 / CJ34 / CJ35 / CJ36 / CJ37 / CJ38 / CJ3A / CJ3B | Budget: original/display/release/display release/transfer/return/supplement/carry forward/display documents | Project budget & availability control | COVERED-38 | 38 | Budget control on projects (wave 38) + wave 41 project budgets |
| CJBV / CJBW / CJBN / CJEN | Activate/deactivate availability control; reconstruct | — | COVERED-38 | 38 | — |
| CJB1 / CJB2 | Generate settlement rules (collective / individual) | — | COVERED-41 | 41 | Ledger posting profiles / settlement to assets (CIP) or COGS |
| CJ8G / CJ88 / CJ8A / CJ9 | Settlement (collective / individual / AuC settlement) | Settlement | COVERED-41 | 41 | Project settlement; CIP to fixed assets (wave 37) |
| CJ44 / CJ45 / CJ46 / CJ47 | Overhead calculation (individual/collective) actual/plan | Overhead | COVERED-39 | 39 | Costing sheet overheads on projects |
| CJZ1 / CJZ2 / CJZ3 / CJZ5 | Interest calculation on projects | Capitalized interest | GAP | propose 41 | Project interest calculation (interest on WIP/capitalized interest IAS 23) not named — propose wave 41 option |
| KKA2 / KKAJ (RA for WBS) | Results analysis (POC, revenue-based, cost-based) | WIP / accrued revenue | COVERED-41 | 41 | POC estimates, WIP/accrued revenue |
| CJI3 / CJI4 / CJI5 / CJI8 / CJI9 / CJIA / CJIB / CJIC / CJID / CJIE / CJIF / CJIG | Line items: actual / plan / commitments / budget / hierarchy / document display | Inquiry | COVERED-41 | 41 | Project transactions inquiry |
| CN41 / CN41N / CN42 / CN42N / CN43 / CN43N / CN44 / CN45 / CN46 / CN47 / CN48 / CN49 / CN50 / CN51 / CN52 / CN53 / CN54 / CN55 | Structure overview / project definitions / WBS / networks / activities / confirmations / components / milestones / PRTs / capacities / PS texts / documents / relationships | Project info system (structures) | COVERED-41 | 41 | — |
| CN72 / CN71 | Project version create (snapshots) | Versions/baselines | COVERED-41 | 41 | Baselines — confirm; treat covered |
| CJV1 / CJV2 / CJV3 / CJV4 / CJV5 / CJV6 | Simulation version create/change/display/transfer | Simulation | PARTIAL-41 | 41 | Project what-if simulation versions not named — partial |
| CLM1 / CLM2 / CLM3 / CLM10 / CLM11 | Claims (variation/change requests with cost impact) | Claim management | PARTIAL-41 | 41 | Change orders/claims on projects (scope change with cost/revenue impact and customer approval) not named — partial; propose wave 41 "project change order" |
| CNE1 / CNE2 / CNE5 | Progress analysis (EVM: POC methods, BCWS/BCWP/ACWP) individual/collective/display | EVM | COVERED-41 | 41 | EVM (CPI/SPI) |
| CNS0 / CN (shipping from projects — delivery from project) | Project deliveries (CNS0) | — | PARTIAL-41 | 41 | Shipping items from project stock/WBS not named — partial (ETO) |
| Project stock (Q), PS material components, MD51 project MRP, CN33 (BOM transfer PS-BOM) | Project procurement/stock | — | PARTIAL-41 | 41 | Project items/POs named; project stock segregation partial (MM-IM note) |
| CAT2 / CATS / CATSXT / CAPS / CAT6 / CAT7 / CAT9 / CATA | Timesheet entry / approval / transfer to PS/CO/PM/HR | Time capture | COVERED-41 | 41 | Hour journals; timesheet with approval (shared with wave 25/42) |
| PR05 (expenses to WBS) | Expense to project | — | COVERED-43 | 43 | Expense journals (wave 41 & 43) |
| VA01 with WBS account assignment / milestone billing plan; DP90 | Project sales & billing | — | COVERED-41 | 41 | T&M/fixed-price/milestone billing, retainers |
| IM01 / IM02 / IM03 / IM22 / IM23 / IM24 / IM25 / IM27 / IM28 / IM30 / IM31 / IM32 / IM33 / IM34 / IM35 / IM36 / IM52 / IMA11 / IMA2 / IMA3 / IMCCP1 | Investment Management: investment programs (positions, hierarchy), budget distribution/supplement/return, program planning, appropriation requests (IMA11 create, variants, approval), rollup/carry-forward | Capital investment programs | PARTIAL-37 | 37 | Asset budgeting named in wave 37; investment program hierarchy with appropriation requests (approval of capex before projects/orders) and program budget distribution — partial; propose wave 38 budgeting + wave 41 |
| Commercial Project Management (CPM: project workspace, financial planning, change/risk/issue management) | Professional-services project cockpit | — | PARTIAL-41 | 41 | Risks/issues register, cost/revenue forecasting workbook — partial |
| Multi-project / portfolio (SAP PPM: portfolio items, resource planning, project management) | PPM | — | OUT-OF-SCOPE | — | Portfolio & project management suite beyond ERP-project v1 (resource bookings & WBS in wave 41 suffice) |
| Fiori: Project Builder / Project Control – Enterprise Projects / Project Financial Controller Overview / Monitor Projects / Manage Project Procurement / Plan Project Costs / Project Briefs | S/4 apps | — | COVERED-41 | 41 | — |
| Fiori: Manage Project Billing / Release Billing Proposals (S/4 Project Billing for professional services) | — | — | COVERED-41 | 41 | Invoice proposals |

### 13.27.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| S_ALR_87013532 / 87013533 / 87013534 / 87013535 / 87013536 | Plan/actual/variance; plan/actual/commitment/remaining plan; budget/actual/commitment; budget updates; actual in CO area currency | COVERED-41 | 41 | Project profitability, committed costs |
| S_ALR_87013537 … 87013575 (cost element, revenue, hierarchy, period breakdown, forecast, payments) | PS cost/revenue/payment reports | COVERED-41 | 41 | — |
| S_ALR_87013558 / 87013559 / 87013560 | Budget/actual/variance; budget/distributed/plan; commitment | COVERED-38 | 38 | — |
| S_ALR_87013562 / 87013563 / 87013564 | Actual/plan/variance by cost element; period; revenue | COVERED-41 | 41 | — |
| S_ALR_87013566 / 87013567 / 87013568 / 87013569 / 87013570 / 87013571 / 87013572 / 87013573 / 87013574 / 87013575 | Results analysis, WIP, payments, receipts/payments, cash flow | COVERED-41 | 41 | — |
| S_ALR_87100185 / 87100186 / 87100187 / 87100188 / 87100189 / 87100190 / 87100191 | Progress reports (EVM), milestone trend analysis | COVERED-41 | 41 | — |
| CJE0 / CJE1 / CJE2 / CJE3 / CJE4 / CJE5 / CJE6 / CJEB | Hierarchy reports (Report Painter) execute/create/change/display/forms | SUPERSEDED-15 | 15 | Report designer |
| CJI3 / CJI5 / CJI8 | Line items | COVERED-41 | 41 | — |
| CN41N / CN42N / CN43N / CN45N / CN46N / CN47N / CN48N | Project info system structure reports | COVERED-41 | 41 | — |
| CN41 → progress, dates comparisons (CN / CNMT milestone trend analysis) | Milestone trend analysis | COVERED-41 | 41 | — |
| CNE5 | Progress analysis report | COVERED-41 | 41 | — |
| CJ31 / CJ33 | Budget display | COVERED-38 | 38 | — |
| IM (IM reports: S_ALR_87012) | Investment program reports | PARTIAL-37 | 37 | — |
| MCI / project capacity (CM / CMP2 / CMP3 / CMP9 workforce planning) | Capacity/workforce planning reports | COVERED-41 | 41 | Resource bookings |
| Fiori: Project Cost Report / Project Profitability / Budget Consumption / Project Status Overview / Utilization Analysis | — | COVERED-46 | 46 | — |

### 13.27.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| CJ44 / CJ45 (overhead) | Overhead allocation | COVERED-39 | 39 | — |
| KKA2 / KKAJ (results analysis) | RA run | COVERED-41 | 41 | — |
| CJ8G / CJ88 | Settlement run (to CO/PA, assets, cost centers) | COVERED-41 | 41 | — |
| CJZ2 (interest) | Interest calculation run | GAP | propose 41 | — |
| CJB1 (periodic rule generation) | — | COVERED-41 | 41 | — |
| CJCF / CJCO | Carry forward commitments / budget | COVERED-38 | 38 | — |
| CJEN / CJBN | Reconstruct project info database / availability control | SUPERSEDED-41 | 41 | Projections |
| CNE2 (progress determination collective) | EVM progress run | COVERED-41 | 41 | — |
| CN72 (periodic versions) | Snapshot | COVERED-41 | 41 | — |
| DP90/DP95 (billing requests) | Billing | COVERED-41 | 41 | — |
| CAT6 / CAT7 / CAT9 (transfer CATS → PS/CO/PM/HR) | Timesheet transfer | SUPERSEDED-41 | 41 | Direct posting of approved time |
| IM27 / IM32 (IM budget rollup / carry-forward) | — | PARTIAL-37 | 37 | — |
| SARA PS_PROJECT | Archiving | COVERED-48 | 48 | — |
| CJ02 mass status (TECO/CLSD) / CN mass change (CNMASS) | — | COVERED-5 | 5 | — |

### 13.27.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OPSA — Project profile (WBS defaults, status profile, planning profile, budget profile, version profile, settlement, planning board, object class) | TCJ41 | — | COVERED-41 | 41 | Project types/templates |
| OPSJ — Budget profiles (availability control activation, tolerance limits, actions) | TBP*, TBPFE | — | COVERED-38 | 38 | — |
| OPSB — Planning profiles (cost/revenue planning, versions, automatic revenue planning) | TCJ4 | — | COVERED-41 | 41 | — |
| OPSC / OPSD / OPSE — Project coding mask (CJ / OPSK special characters, edit mask), project types, priorities, applicant/responsible persons | TCJE, TCJS, TCJP | — | COVERED-41 | 41 | Project numbering masks |
| OPSV / OPUV / OPUU / OPSU — Network types, network profiles, control keys (PS01/PS02/PS05), network parameters (CO integration, scheduling, number ranges) | TCN41, T003O, TCN | — | COVERED-41 | 41 | — |
| OPTP / OPU6 / OPU — Scheduling parameters for WBS/networks (planning method, reduction, scheduling type, adjust basic dates) | TCX | — | COVERED-41 | 41 | — |
| OPSQ / OPS — Status profiles (BS02), user status for projects | TJ30 | — | COVERED-4 | 4 | — |
| OKG (RA keys) / OPS — Results analysis keys per project profile, valuation methods | TKKA* | — | COVERED-41 | 41 | — |
| OPSJ / OKOA / Settlement profiles & strategies (CJB1 strategy: settlement to AuC/cost center/PA) | TKB1, T811 | — | COVERED-41 | 41 | — |
| ODP1 — DIP profiles (resource-related billing) | DIP* | Usage (billing/quotation), characteristics, sources (actual costs, SD) | COVERED-41 | 41 | Billing rules |
| OPSX / Milestone usage, milestone billing, progress analysis settings (progress version, measurement methods OPS) | T* | — | COVERED-41 | 41 | EVM measurement methods |
| OPS — Project stock settings (valuated/non-valuated), PS material components (OPUV), MRP for project | TCJ41, T* | — | PARTIAL-41 | 41 | Project stock partial |
| CATS: data entry profiles (CAC1), CATS fields, approval workflow, cost assignment | TCATS*, TCAT | — | COVERED-41 | 41 | Timesheet profiles |
| Investment management: program types, appropriation request types, status, budget categories, plan/budget profiles | TAIF, TAPRT | — | PARTIAL-37 | 37 | IM partial |
| Claim management customizing (notification type CL, claim types) | TQ80 | — | PARTIAL-41 | 41 | — |
| Simulation versions / project versions profiles (OPTS / OPT) | TCJ | — | PARTIAL-41 | 41 | — |
| PS texts, documents (DMS link), PS info system profiles (OPSM / OPTZ / CJ) | — | — | COVERED-41 | 41 | — |
| Project billing (S/4): billing element profiles, billing rules, invoice proposal settings | — | — | COVERED-41 | 41 | — |
| Interest profiles (OPIB / OPIC / OPID) | T* | — | GAP | propose 41 | — |

---

## 13.28 HCM — PA / OM / PT / PY + SuccessFactors Employee Central

SAP context: HCM-PA (personnel administration: infotypes 0000–0999, PA30/PA40 actions, PA20, PA70 fast entry, master data), PA-OS/OM (org management: org units, positions, jobs, PPOME/PP01, evaluation paths, relationships), PT (time management: work schedules, absences/attendances IT2001/2002, quotas IT2006, time evaluation PT60/RPTIME00, CATS, time events PDC, PT50 quota overview, shift planning PP61), PY (payroll: PC00_Mxx_CALC, schemas PE01, rules PE02, wage types, payroll results PC_PAYRESULT, posting PC00_M99_CIPE, off-cycle, retro, year-end PU19 tax reporter US, garnishments IT0194/0195, remuneration statement), PA-BN (benefits: plans, enrollment HRBEN0001, HRBEN0071 eligibility), PA-RC (recruitment PB10–PB80, e-Recruiting), PA-PD (personnel development: qualifications PPPM, appraisals PHAP_*, career/succession), PE (training & event management, LSO), PA-CM (compensation management), HR ESS/MSS, SuccessFactors Employee Central (EC), EC Payroll, SF Recruiting/Onboarding/Learning/Performance & Goals/Compensation/Succession. Passage: wave 42 (workers/positions/jobs/compensation, absence/leave, benefits, ESS/MSS, talent basics, recruitment basics, PII controls), wave 43 (payroll engine, country packs US + 1 EU, provider adapter, year-end forms, expense management), wave 25 (time & attendance core).

### 13.28.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| PA30 / PA20 | Maintain / display HR master data (infotypes: 0000 actions, 0001 org assignment, 0002 personal data, 0006 address, 0007 planned working time, 0008 basic pay, 0009 bank details, 0014 recurring payments, 0015 additional payments, 0016 contract, 0021 family, 0041 date specs, 0105 communication, 0167/0168/0169 benefits, 0207-0210 US tax, 0194/0195 garnishments, 2001 absences, 2002 attendances, 2006 quotas, 2010 remuneration info) | Employee master | COVERED-42 | 42 | Worker/employee/contractor records with effective-dated employment, org assignments, compensation plans; bank details (wave 43 payroll); country-specific fields framework |
| PA40 | Personnel actions (hire, org reassignment, pay change, leave of absence, termination, rehire) | Lifecycle actions | COVERED-42 | 42 | Effective-dated employment actions with workflow |
| PA41 / PA42 | Change entry/leaving date / fast entry for actions | — | COVERED-42 | 42 | — |
| PA70 / PA71 | Fast entry (infotype for many employees) / fast entry of time data | Mass maintenance | COVERED-5 | 5 | Grid mass edit |
| PA10 | Personnel file (overview of all infotypes) | — | COVERED-42 | 42 | Worker 360 view |
| PRMD / PRMM / PRMS | Maintain/display HR master data (travel privileges) | — | COVERED-43 | 43 | — |
| PPOME / PPOMA / PPOSE / PPOM_OLD | Organization and staffing: org units, positions, jobs, holders, cost center assignment (change/display) | Org management | COVERED-42 | 42 | Positions, jobs, departments (wave 6 operating units) with effective dating |
| PO03 / PO10 / PO13 / PO01 / PP01 / PP02 / PP03 | Maintain job / org unit / position / work center / general object maintenance (infotypes 1000-1999: relationships 1001, description 1002, planned compensation 1005, vacancy 1007, account assignment 1008, cost distribution 1018, obsolete 1014, work schedule 1011) | OM objects | COVERED-42 | 42 | Position/job attributes, cost distribution, vacancy |
| PQ01 … PQ / PPO | OM actions (create org unit with position etc.) | — | COVERED-42 | 42 | — |
| PPCI / PPSC / PPSM | Create structure / evaluation paths | — | COVERED-6 | 6 | Hierarchy purposes |
| PP61 / PP62 / PP63 / PP64 / PP6I / PP6A | Shift planning (target plan, requirements, assignments) | Shift planning / rostering | PARTIAL-25 | 25 | Time registration & shifts in wave 25; shift rostering (planning employees to shifts against requirements, swaps) not named — partial; propose wave 42 |
| PT01 / PT02 / PT03 | Work schedule generation / change / display (period work schedules, daily WS, WS rules) | Work schedules | COVERED-42 | 42 | Work calendars/shifts (wave 9 resource calendars + wave 42 employee schedules) |
| PA61 / PA62 / PA63 / PA64 | Maintain time data / list entry / display / calendar | Absences/attendances | COVERED-42 | 42 | Absence/leave requests; attendance (wave 25) |
| PT50 / PT_QTA00 / PT_QTA10 | Quota overview / generate absence quotas | Leave balances | COVERED-42 | 42 | Accruals, carry-over |
| PT60 / PT61 / PT62 / PT63 / PT64 / PT65 / PT66 / PT67 / PT68 / PT69 | Time evaluation (RPTIME00) / time statement / attendance list / absence list / time leveling / time balances | Time evaluation | COVERED-25 | 25 | Pay-type calculation (overtime, shifts); time statement |
| PT40 / PT41 / PT42 / PT43 / PT44 / PT45 / PT46 / PT47 / PT80 / PT90 | Time events / PDC (upload, postprocessing), time pool | Clock in/out | COVERED-25 | 25 | Kiosk registrations |
| CAT2 / CATS (see PS) | Timesheet | — | COVERED-41 | 41 | — |
| PC00_Mxx_CALC (e.g. PC00_M10_CALC US, PC00_M01_CALC DE, PC00_M08_CALC GB) / PC00_M99_CALC_SIMU | Payroll run (country) / simulation | Gross-to-net | COVERED-43 | 43 | Payroll engine (US + one EU reference; provider adapter) |
| PA03 | Payroll control record (release/exit/corrections, period) | Payroll area status | COVERED-43 | 43 | Pay cycle status with locking |
| PU03 | Change payroll status (infotype 0003: retro accounting dates, locks) | — | COVERED-43 | 43 | Retro pay triggers |
| PC00_M99_CIPE / PCP0 / PC00_M99_CIPC | Posting to accounting (create, release, post documents) / display posting runs | Payroll GL posting | COVERED-43 | 43 | Payroll posting to GL (reconciled) |
| PC00_M99_CWTR | Wage type reporter | — | COVERED-43 | 43 | Payroll reconciliation reports |
| PC_PAYRESULT / PC00_M99_DISPLAY | Display payroll results (clusters) | — | COVERED-43 | 43 | Pay statements / results |
| PC00_Mxx_CEDT / PC00_M99_CEDT | Remuneration statement (pay slip) | — | COVERED-43 | 43 | Pay statements |
| PC00_M99_CMLI0 / PC00_M99_FPAYM / PC00_M99_CDTA | Bank transfers (DME), pre-DME | Payroll payments | COVERED-43 | 43 | Payment via wave 19 formats (ACH/SEPA) |
| PC00_M99_URMP / PC00_M99_CLJN / PC00_M99_CLGA | Payroll journal / account / cumulated | — | COVERED-43 | 43 | — |
| PC00_M10_OSUP / PC00_Mxx_OC (off-cycle workbench PUOC_10) | Off-cycle payroll (bonus, correction, final pay) | — | PARTIAL-43 | 43 | Off-cycle/ad-hoc pay runs (bonus, termination final pay, check replacement) not explicitly named — partial |
| PU19 | Tax reporter (US: 941, W-2, state) | US statutory | COVERED-43 | 43 | Year-end forms framework (W-2/1099); quarterly filings — partial via "framework"; treat covered |
| PC00_M10_CLAIMS / PC00_M10_UTXX | US claims processing / tax authorities | — | PARTIAL-43 | 43 | Claims (overpayment recovery) not named — partial |
| PA30 IT0194/0195 garnishments; PC00_M10_CGTD | Garnishments (orders, documents, disbursement) | — | COVERED-43 | 43 | Garnishments named |
| HRBEN0001 / HRBEN0003 / HRBEN0005 / HRBEN0006 / HRBEN0014 / HRBEN0071 / HRBEN0075 / HRBEN0079 | Benefits: enrollment / participation monitor / termination / eligibility change / open enrollment / plan overview / cost summary / default plans | Benefits administration | COVERED-42 | 42 | Benefits: plans, eligibility, open enrollment, life events; ESS enrollment |
| HRBEN00ICOBRA / HRBEN00COBRA | COBRA (US) | — | PARTIAL-42 | 42 | COBRA notices/continuation (US) belongs to US pack — partial |
| PB10 / PB20 / PB30 / PB40 / PB50 / PB60 / PB80 / PBAA / PBAB / PBAE / PBAK | Recruitment (classic): initial entry of applicant, display, maintain, applicant actions, vacancy assignment, applicant activities, mass processing | Recruiting | COVERED-42 | 42 | Recruitment pipeline basics |
| E-Recruiting (HRRCF_*) / SF Recruiting & Onboarding | Modern recruiting | — | PARTIAL-42 | 42 | Pipeline basics; career site, requisition approval, offer letters, onboarding tasks — partial |
| PPPM / PPPE / PPQD / OOQA | Qualifications catalog, profile matchup, development plans | Skills | COVERED-42 | 42 | Skills, certifications |
| PHAP_CATALOG / PHAP_CREATE / PHAP_CHANGE / PHAP_ADMIN / PHAP_SEARCH / PHAP_PREPARE | Appraisal templates / create / change / administration (performance management, objective setting) | Performance | COVERED-42 | 42 | Performance reviews, goals |
| PPCP / PPSS (career/succession planning) / SF Succession | Succession planning | — | PARTIAL-42 | 42 | Talent basics; succession planning/talent pools not named — partial |
| PECM_* (enterprise compensation management: planning, budgets, long-term incentives, pay scale OH) / SF Compensation | Compensation planning cycles | — | PARTIAL-42 | 42 | Compensation plans yes; merit/bonus planning cycles with budgets and manager worksheets — partial |
| PSV1 / PSV2 / PSV3 / PSVP / LSO_PSV (training & event management / LSO) / SF Learning | Training catalog, bookings, resources, follow-up | Learning | PARTIAL-42 | 42 | Certifications tracked; course catalog/LMS (sessions, bookings, completion) not named — partial |
| PP01 IT1050/1051 (job pricing, pay grades) / OH11 (pay scale) | Pay structures (pay scale groups/levels) | — | COVERED-42 | 42 | Compensation plans / pay grades |
| HRUS_D2 / HRPAD (personnel data: concurrent employment, global employment) | Concurrent/global employment | — | PARTIAL-42 | 42 | Multi-assignment and global assignments not named — partial |
| ESS/MSS (Fiori My Profile, My Leave Requests, My Timesheet, My Paystubs, Team Calendar, Approve Leave Requests, Approve Timesheets) | Self-service | — | COVERED-42 | 42 | ESS/MSS |
| SuccessFactors Employee Central (person/employment/job info, foundation objects, position management, time off, EC time sheet, global benefits, document generation, MDF custom objects, Intelligent Services, EC Payroll, People Analytics) | Cloud HRIS | — | COVERED-42 | 42 | HR core (worker, positions, absence, benefits, ESS); MDF-style custom objects via metadata kernel (wave 3) |
| SF Onboarding / Offboarding | Onboarding tasks | — | PARTIAL-42 | 42 | Onboarding checklists not named — partial |
| Employee Central integration with ERP (BIB, EC-ERP replication, cost center replication) | — | — | SUPERSEDED-42 | 42 | Single system |
| HR PII: HR authorizations (structural P_ORGIN/P_PERNR/structural authorizations OOSB), data privacy (ILM blocking of employee data) | — | — | COVERED-42 | 42 | PII vaulting, consent, retention, role-based masking (contract tests) |
| Fiori: Manage Employee Master Data / Employee Lookup / Org Chart | — | — | COVERED-42 | 42 | — |

### 13.28.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| S_AHR_61016369 / 61016362 / 61016360 / 61016380 / 61016373 … (PA standard reports: employee list, flexible employee data, headcount, entries/leavings, birthday list, service anniversaries, nationality, family members, education) | HR master data reports | COVERED-42 | 42 | HR reporting via saved views + semantic layer |
| S_AHR_61016532 / 61016529 / 61016528 … (OM reports: org structure, staff assignments, position descriptions, vacancies, existing jobs/positions/org units) | OM reports | COVERED-42 | 42 | Org chart |
| S_AHR_61018 (time: absences/attendances overview, quota overview, time statement, cumulated time balances, attendance check) / PT64 / PT65 / PT90 | Time reports | COVERED-42 | 42 | — |
| S_AHR_61016150 / PC00_M99_CWTR / PC00_M99_CLJN / PC00_M99_CLGA / PC00_M99_URME (payroll: wage type reporter, payroll journal, payroll account, reconciliation, posting documents) | Payroll reports | COVERED-43 | 43 | Payroll audit and reconciliation reports |
| PC00_M10_* US reports (941, 940, W-2, state unemployment, new hire, EEO-1, workers' comp) | US statutory | PARTIAL-43 | 43 | Year-end W-2/1099 named; quarterly 941/state UI and EEO/VETS reporting belong to US pack — partial |
| HRBEN reports (participation, costs, benefit statements, HRBEN0075 / 0079) | Benefits reports | COVERED-42 | 42 | — |
| Ad-hoc query (S_PH0_48000510 / PQAH / SQ01 HR InfoSet query) | HR ad hoc query | SUPERSEDED-46 | 46 | Self-service explorer with PII RLS |
| HIS (PPIS / S_AHR_61016 HIS reporting via org structure) | HR information system | SUPERSEDED-46 | 46 | — |
| SF People Analytics / Workforce Analytics / Report Center | — | COVERED-46 | 46 | — |
| Fiori: Headcount Overview / Workforce KPIs | — | COVERED-46 | 46 | — |

### 13.28.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| PA03 release → PC00_Mxx_CALC → corrections → exit | Payroll cycle | COVERED-43 | 43 | Pay cycles |
| PC00_M99_CIPE (posting) → PCP0 release | Payroll GL posting run | COVERED-43 | 43 | — |
| PC00_M99_CMLI / FPAYM / PC00_M99_FPAYM | Payroll payment run (DME) | COVERED-43 | 43 | — |
| PT60 (RPTIME00) scheduled | Time evaluation nightly | COVERED-25 | 25 | — |
| PT_QTA00 / RPTQTA00 | Quota generation/accrual run | COVERED-42 | 42 | Leave accruals |
| RPUAUD00 / PT_BAL00 | Time balance / audit | COVERED-42 | 42 | — |
| PU19 (quarterly/annual tax reporter runs) | Statutory filings | PARTIAL-43 | 43 | — |
| PC00_M99_CRET (retro) / PC00_M99_PA03_RELEA | Retro payroll processing | COVERED-43 | 43 | Retro pay |
| HRBEN0001 open enrollment period / HRBEN0071 eligibility run | Open enrollment cycle | COVERED-42 | 42 | — |
| RPUDELPP / PU01 (delete payroll results) | Payroll result corrections | COVERED-43 | 43 | — |
| PA40 mass actions (RPUPAA / RPMACT batch input) | Mass actions | COVERED-5 | 5 | — |
| HR data archiving / ILM destruction of employee data (SARA PA_*, ILM) | — | COVERED-48 | 48 | Retention policies, erasure |
| SF: scheduled jobs (EC → payroll point-to-point replication, Intelligent Services events) | — | SUPERSEDED-42 | 42 | Events |
| Third-party payroll provider file exchange (ADP/Gusto adapters) | Outsourced payroll | COVERED-43 | 43 | Provider adapter |

### 13.28.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Enterprise structure (HR): personnel areas/subareas (T500P, T001P), employee groups/subgroups (T501, T503), payroll areas (T549A), assignment to company code | T500P, T001P, T501, T503, T549A | — | COVERED-42 | 42 | HR org dimensions per legal entity |
| Infotype customizing: infotype attributes (T582A), screen control (T588M), infotype menus (T588B), dynamic actions (T588Z), features (PE03: NUMKR, ABKRS, LGMST, TARIF…) | T582A, T588M, T588B, T588Z, T549 | Time constraints, retro relevance, subtypes | SUPERSEDED-3 | 3 | Metadata-driven entities/fields with effective dating; dynamic actions → workflow/event hooks (wave 4) |
| PA40 personnel actions / action types / info groups / reasons (T529A, T529U, T588C) | T529A, T529U, T588D | — | COVERED-42 | 42 | Action types with reason codes |
| OM: object types, relationships, evaluation paths, plan versions, number ranges (OONR), infotype 1000-series settings | T778O, T778V, T778A, T778P, T77S0 | — | COVERED-42 | 42 | — |
| OM–PA integration switches (T77S0 PLOGI ORGA), position vacancy, cost distribution | T77S0 | — | SUPERSEDED-42 | 42 | Single model |
| Time management: work schedule rules (T508A), daily work schedules (T550A), period work schedules (T551A), holiday calendars (SCAL), absence/attendance types (T554S), quota types (T556A), counting rules (T556C), quota generation rules (T559L), time types (T555A), time evaluation schemas (TM00/TM04) and PCRs (PE02), time pairs | T508A, T550A, T551A, T554S, T556A, T556C, T559L, T555A | — | COVERED-42 | 42 | Absence plans, accrual rules, work calendars; pay-type calculation rules (wave 25) as versioned rule sets replace schemas/PCRs |
| Payroll: wage types (T512W, T511, wage type catalog OH11), processing/cumulation/evaluation classes, schemas (PE01: U000 US, D000 DE), personnel calculation rules (PE02), payroll periods (T549Q/T549S), pay scale structure (T510), tax models (T5UTM/T5UTE US), BSI TaxFactory integration (US), posting characteristics (T52EK symbolic accounts, T52EZ, OBYE account assignment), garnishment customizing, off-cycle reasons | T512W, T511, T510, T549Q, T549S, T52E*, T5U* | — | COVERED-43 | 43 | Earnings/deductions/taxes as versioned rule sets; US federal/state tables (BSI-like provider adapter option); posting profiles |
| Benefits: benefit areas, plan types, plans (health/insurance/savings/stock/credit/misc), cost variants, eligibility rules, termination rules, enrollment events, open enrollment periods, EDI to providers (T5UB*) | T5UB*, T74* | — | COVERED-42 | 42 | Benefit plans/eligibility/events |
| Recruitment: applicant groups/ranges, activities, vacancies, applicant status | T750*, T751* | — | COVERED-42 | 42 | — |
| PD: qualification scales, appraisal templates (PHAP_CATALOG), development plans, career/succession settings | T77* | — | COVERED-42 | 42 | — |
| Compensation management: compensation areas, plans, guidelines, budgets, eligibility | T71* | — | PARTIAL-42 | 42 | Planning cycles partial |
| Training & event management / LSO: business event types, resources, booking, follow-up, LSO settings | T77* | — | PARTIAL-42 | 42 | LMS partial |
| HR authorizations: P_ORGIN, P_PERNR, structural authorizations (OOSP/OOSB), test procedures (T584A), context-sensitive | T77PR, T77UA | — | COVERED-42 | 42 | OPA policies with structural scope (org hierarchy) + PII masking |
| ESS/MSS configuration (launchpad, Fiori apps, HR Renewal) | — | — | COVERED-42 | 42 | — |
| SuccessFactors EC: foundation objects, MDF objects, business rules (rules engine), workflows, role-based permissions, data models (XML), picklists, event reasons | — | — | COVERED-42 | 42 | Metadata kernel + workflow + OPA |
| EC Payroll / point-to-point replication configuration | — | — | SUPERSEDED-42 | 42 | — |
| Country-specific HR infotypes and legal changes (country packs: US, DE, GB, FR, IN, MX, CA) | — | — | PARTIAL-48 | 48 | Country-specific fields framework (wave 42) + packs (wave 48) cover tax/statutory; HR-legal (works council, contract types, statutory leave) per country partially |
| Concurrent employment / global employment configuration | — | — | PARTIAL-42 | 42 | — |

---

## 13.29 FI-TV — Travel Management / SAP Concur

SAP context: FI-TV travel requests (TRIP: TP01? request, PR01? plan), travel planning (flight/hotel/car booking via Amadeus/Sabre, PR03? ), travel expenses (PR05 create/change expense report, TRIP, PR04? , receipts, per diems/flat rates, mileage, credit card clearing PRCC, approval workflow, settlement PREC, posting to FI PRFI/PRRW, payment via payroll or AP/DME PRDX/PRDH?), travel privileges IT0017, Concur Expense/Travel/Invoice/Request with SAP ICS integration. Passage: wave 43 (expense reports, receipts OCR, policies/per diems, approvals, credit-card feed, reimbursement via AP, project expense) — travel booking is not covered.

### 13.29.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| PA30 IT0017 travel privileges / PRMD | Employee travel privileges (reimbursement group, trip rules) | — | COVERED-43 | 43 | Expense policy groups per employee |
| TRIP / PR05 / PR04 | Travel manager / create-change travel expense report (receipts, per diems, mileage, advances, deductions) | Expense report | COVERED-43 | 43 | Expense reports with receipts (OCR), per diems, mileage |
| TP01 / TP02 / TP03 / PR01 (travel request) | Travel request (pre-trip approval with estimated cost) | Pre-approval | PARTIAL-43 | 43 | Pre-trip travel request/approval with estimated cost and cash advance not named — partial; propose wave 43 "pre-approval request" |
| TP04 / PR03 (travel plan: flight, hotel, car via GDS) | Travel booking | Booking | OUT-OF-SCOPE | — | Travel booking/GDS integration is not in scope; integrate with Concur Travel/Navan via wave 35 integration hub |
| PR02 / PR05 advances (PR cash advance) | Cash advances | — | PARTIAL-43 | 43 | Advances and offset against expenses not named — partial |
| PRCC / PRCCD / PRCCE / PRCCF | Credit card clearing: import card transactions, buffer, assign to trips | Card feed | COVERED-43 | 43 | Credit-card feed import |
| PR05 → receipts attachments (ArchiveLink) | Receipt images | — | COVERED-43 | 43 | Receipt capture |
| PR10 / PR11 (per diem / flat rate maintenance — customizing) | — | — | COVERED-43 | 43 | — |
| PRAP / PR05 approval (workflow WS20000040) | Expense approval | — | COVERED-43 | 43 | Approvals |
| Concur Expense (reports, receipts, ExpenseIt, audit rules, allocations to cost objects, card feeds), Concur Request, Concur Invoice, Concur Travel | Cloud T&E | — | COVERED-43 | 43 | Expense management covers Concur Expense; Request partial; Travel OOS; Invoice = AP capture (wave 18) |
| Concur–SAP ICS integration (financial posting, employee sync) | — | — | SUPERSEDED-43 | 43 | Native |
| Fiori: My Travel Requests / My Travel and Expenses / Approve Travel Expenses | — | — | COVERED-43 | 43 | — |

### 13.29.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| PRTS / S_AHR_61016401 … 61016411 (trip statistics, receipts, cost assignment, general trip data, trips by period/cost center, travel expense statement) | Travel reports | COVERED-43 | 43 | Expense reporting |
| PR05 overview / PRTE (trip overview) | — | COVERED-43 | 43 | — |
| PRCC statistics | Card transactions not assigned | COVERED-43 | 43 | — |
| S_AHR_61016 (taxable amounts per employee) | Taxable benefits | COVERED-43 | 43 | Taxable expense items to payroll |
| Concur reporting (Cognos/Intelligence) | — | COVERED-46 | 46 | — |

### 13.29.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| PREC | Settlement of trips (calculate reimbursement, tax) | COVERED-43 | 43 | Expense settlement |
| PRFI / PRRW | Create posting run / manage posting runs (post to FI/CO) | COVERED-43 | 43 | Posting to GL/projects |
| PRDX / PRDH / PRDE | DME payment of trips (direct payment) / via payroll (PRPY) / data medium | COVERED-43 | 43 | Reimbursement via AP payment run (wave 19) |
| PRCC (scheduled import) | Card feed import | COVERED-43 | 43 | — |
| PRPY / transfer to payroll (IT0017/ wage types) | Reimbursement via payroll | COVERED-43 | 43 | Payroll reimbursement option — confirm; treat covered |
| PRAA (archiving) / SARA FI_TV | Archiving | COVERED-48 | 48 | — |
| Concur SAE (standard accounting extract) import | Concur → GL | SUPERSEDED-43 | 43 | — |

### 13.29.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Travel expense control (trip provision variants, trip schemas, trip types statutory/enterprise, reimbursement groups) | T702N, T702O, T706* | — | COVERED-43 | 43 | Policy groups |
| Per diems/flat rates: meals, accommodation, mileage (country/region/time), deductions (T706H T706F T706U) | T706* | Statutory vs enterprise rates, 3-month rule (DE), IRS rates (US) | COVERED-43 | 43 | Per-diem tables per country pack (wave 48) |
| Expense types (T706B1, individual receipts), max amounts, receipt requirement, VAT/tax treatment (input tax recovery), wage types for payroll | T706B*, T706K | — | COVERED-43 | 43 | Expense categories with policy limits and tax |
| Travel request / travel plan settings, approval workflows, substitutes | T702, SWF | — | PARTIAL-43 | 43 | Pre-approval partial |
| Credit card clearing: card companies, transaction keys mapping to expense types | T706_CCC | — | COVERED-43 | 43 | — |
| Posting: symbolic accounts, account determination (T706K), cost assignment rules (cost center/WBS/order), posting runs | T706K, T030 | — | COVERED-43 | 43 | Posting profiles; project expense |
| Travel planning (GDS, policies, travel agency) | — | — | OUT-OF-SCOPE | — | — |
| Concur configuration (audit rules, policies, workflows, allocation forms) | — | — | COVERED-43 | 43 | Policy rule engine |

---

## 13.30 Basis & Security (users, roles, GRC Access Control, change documents, transports, system administration)

SAP context: user administration (SU01, SU10, SU3, SU53, SU56, SUIM, SUGR, SU0?), CUA (SCUA/SCUM), role maintenance (PFCG, SU24, SU25, SU22, SU21 auth objects, SU20 fields), security audit (SM19/SM20/RSAU_CONFIG, SM18), password/security policies (RZ10, SECPOL), SSO/SNC/SAML (STRUST, SAML2, SNC), GRC Access Control (ARA risk analysis, ARM access requests, EAM emergency access/firefighter, BRM role management, UAR user access review), transports (SE09/SE10/STMS/SE01/SE03, CTS+, ChaRM), system admin (SM36/SM37 jobs, SM50/SM51/SM66 processes, SM12 locks, SM13 updates, SM21 syslog, ST22 dumps, ST02/ST03/ST04/ST05/ST06/ST11/ST12 performance, SMLG, RZ04, RZ20 CCMS, RZ70/SLD, SM59 RFC, SICF, SOAMANAGER, SPAD/SP01/SP02 spool, SCOT/SOST mail, SM30/SM31 table maintenance, SE16/SE16N/SE16H table display, SE11/SE80/SE38/SE37/SE24/SE18/SE19/CMOD development, SNOTE/SPAM/SAINT/SUM patching, SCC4/SCC1/SCCL client admin, SCDO/SCU3 change docs & table logging, SNRO number ranges, SE93 t-codes, SM35 batch input, SLG1 app log, SARA archiving, DB13/DB02 database, SAP Solution Manager, EarlyWatch, SAP Router, Web Dispatcher, HANA cockpit, SAP Cloud ALM). Passage: waves 1 (pipeline/transports), 2 (identity/authz), 3 (metadata/DDIC), 4 (jobs/workflow), 5 (shell, number sequences, audit, observability), 48 (SoD, SOC 2, privacy), 50 (operations/DR).

### 13.30.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| SU01 / SU01D / SU10 | User maintenance (address, logon data, roles, profiles, parameters, defaults, groups) / display / mass changes | Users | COVERED-2 | 2 | Keycloak users with SCIM provisioning; admin UI (users, roles, duties) |
| SU3 / SU0 / SU50 / SU51 / SU52 | User own data (defaults, parameters, address) | — | COVERED-5 | 5 | Personalization/preferences |
| SU01 → SNC / SAML2 / STRUST / SSO2 | SSO configuration | — | COVERED-2 | 2 | OIDC/SAML federation; MFA |
| SCUA / SCUM / SCUG / SCUL | Central User Administration | Multi-system users | SUPERSEDED-2 | 2 | Single IdP (Keycloak) |
| SUGR | User groups | — | COVERED-2 | 2 | Groups |
| PFCG | Role maintenance (menu, authorizations, profiles, users; single/composite/derived roles, org levels) | Roles | COVERED-2 | 2 | Role/duty/privilege model compiled to OPA/Rego; derived roles by org level = role scoping per legal entity/site |
| SU24 / SU25 / SU22 | Authorization default values per transaction (check indicators) / upgrade/profile generator / SAP defaults | Auth proposals | SUPERSEDED-2 | 2 | Privileges attached to API endpoints/entities in metadata |
| SU21 / SU20 | Authorization objects / fields | — | SUPERSEDED-2 | 2 | Privilege = (resource, action, scope) in policy |
| SU53 / SU56 / ST01 / STAUTHTRACE | Authorization check analysis / user buffer / system trace / auth trace | Troubleshooting | COVERED-2 | 2 | Effective-permission viewer + OPA decision logs |
| SUIM | User information system (users by role/auth/transaction, change docs for users/roles, comparisons) | Reporting | COVERED-2 | 2 | Effective-permission report; access reports |
| SM19 / SM20 / RSAU_CONFIG / RSAU_READ_LOG / SM18 | Security audit log configuration / evaluation / reorganization | Security audit | COVERED-5 | 5 | Audit log + OTel logs; wave 48 SOC 2 evidence |
| SECPOL / RZ10 login parameters | Password/security policies | — | COVERED-2 | 2 | Keycloak policies |
| GRC AC: NWBC Access Request (ARM) / Access Risk Analysis (ARA) / Business Role Management (BRM) / Emergency Access Management (EAM firefighter IDs, logs) / User Access Review (UAR) / SoD Review | Access governance | — | COVERED-48 | 48 | SoD rules engine, conflict detection, mitigation workflow, access certification campaigns; emergency (break-glass) access — confirm firefighter/break-glass with session log; treat PARTIAL via next row |
| GRC EAM firefighter (superuser privilege management with reason codes and log review) | Emergency access | — | PARTIAL-48 | 48 | Break-glass elevated access with time-boxed sessions, reason codes and reviewer sign-off not explicitly named — partial |
| SE09 / SE10 / SE01 / SE03 / STMS / STMS_IMPORT / SE06 / SE07 | Transport organizer (workbench/customizing requests), transport management, import queues, system change option | Transports | COVERED-1 | 1 | GitOps: config/metadata changes as versioned artifacts promoted through environments by digest; customizing-as-code |
| SCC4 / SCC1 / SCCL / SCC3 / SCC5 / SCC7 / SCC8 / SCC9 | Client maintenance, client copy (local/remote), client transport, delete | Clients | SUPERSEDED-2 | 2 | Tenants + environments; tenant provisioning automation (wave 50) |
| SE11 / SE12 / SE13 / SE14 / SE15 / SE16 / SE16N / SE16H / SE17 | ABAP Dictionary: tables/views/data elements/domains/search helps/lock objects, technical settings, DB utility, repository info system, table browser | DDIC | COVERED-3 | 3 | Metadata registry (entities, fields, enums); generated schema; table browser = entity explorer |
| SE80 / SE38 / SE37 / SE24 / SE41 / SE51 / SE71 / SE91 / SE93 / SE63 / SE54 / SM30 / SM31 / SM34 | Development workbench, programs, function modules, classes, menus, screens, SAPscript, messages, transaction codes, translation, table maintenance generator, table maintenance | ABAP development | OUT-OF-SCOPE | — | PRD non-goal: no proprietary extension language; metadata extensions (wave 3) and public APIs (wave 35) instead |
| SE18 / SE19 / SE20 / CMOD / SMOD / SPAU / SPDD / SE95 | BAdIs, enhancement spots, customer exits, modification adjustment | Enhancement framework | SUPERSEDED-3 | 3 | Lifecycle hooks/extension points in metadata; webhooks and event subscribers (waves 4/35) |
| SNOTE / SPAM / SAINT / SUM / SPAU / SPDD | Note assistant, support packages, add-ons, upgrade | Patching | SUPERSEDED-1 | 1 | Continuous delivery; zero-downtime migrations (wave 50) |
| SM36 / SM37 / SM62 / SM64 / SM61 / SM65 / SMX | Background jobs: define, monitor, events, trigger event, job servers, check | Batch | COVERED-4 | 4 | Batch job framework with job monitor UI |
| SM50 / SM51 / SM66 / SM04 / AL08 / SM12 / SM13 / SM14 / SM21 / ST22 / SM58 / SMQ1 / SMQ2 / SMQR / SMQS | Work processes, servers, global process overview, user sessions, locks, update requests, system log, dumps, tRFC, qRFC queues | System monitoring | SUPERSEDED-5 | 5 | Kubernetes + OpenTelemetry (Grafana/Tempo/Loki/Prometheus), SLO dashboards, alerting; Kafka lag monitoring; Temporal UI |
| ST02 / ST03 / ST03N / ST04 / ST05 / ST06 / ST07 / ST10 / ST11 / ST12 / STAD / SQLM / SWLT / ST14 | Buffers, workload analysis, DB performance, SQL trace, OS monitor, app monitor, table call stats, developer traces, single transaction analysis, statistics records, SQL monitor | Performance | SUPERSEDED-5 | 5 | OTel traces/metrics; pgBadger/pg_stat_statements; k6 load campaigns (wave 50) |
| RZ20 / RZ21 / RZ03 / RZ04 / RZ10 / RZ11 / RZ12 / SMLG / SM63 | CCMS monitoring, operation modes, instance profiles, parameters, RFC server groups, logon groups | — | SUPERSEDED-1 | 1 | Helm values / K8s config; HPA; Envoy routing |
| SM59 / SICF / SOAMANAGER / SRT_* / WE21 / SMICM / SMMS | RFC destinations, HTTP services, web service config, ICM | Connectivity | SUPERSEDED-35 | 35 | Integration hub (REST/GraphQL/webhooks/Kafka), API gateway, keys & quotas |
| STRUST / SSFA / SSO2 / SAML2 / SNCWIZARD | Trust manager (certificates), SSF, SAML | PKI | COVERED-1 | 1 | cert-manager, Vault, Keycloak |
| SPAD / SP01 / SP02 / SPAD printers / SP12 | Spool administration, output requests, device types | Printing | COVERED-32 | 32 | Print service (ZPL, PDF) + document designer; cloud print queues — confirm generic print queue management for office printers (not just ZPL); treat PARTIAL via note in cross-app |
| SCOT / SOST / SOSG / SOSV / SBWP / SO10 / SO16 | SAPconnect (email/fax), send status, business workplace, standard texts | Messaging | COVERED-5 | 5 | Notification service (email/SMS/in-app) — confirm; treat covered |
| SM35 / SHDB | Batch input sessions / recorder | Data entry automation | SUPERSEDED-10 | 10 | Import/export (wave 5) and migration pipeline (wave 10); APIs |
| SLG0 / SLG1 / SLG2 | Application log objects / display / delete | App log | COVERED-5 | 5 | Structured logs (Loki) with trace ids |
| SCDO / SCU3 / SE16 CDHDR-CDPOS / RSSCD100 | Change document objects / table logging evaluation / change documents display | Change documents | COVERED-5 | 5 | Field-level immutable audit from CDC with viewer |
| SNRO / SNUM / NRIV | Number range objects/intervals/buffering | Number ranges | COVERED-5 | 5 | Number sequence service (gap-free option) |
| SARA / SARI / SARJ / DB15 / AOBJ / ILM (IRM_CUST, ILMARA, ILM_DESTRUCTION) — see cross-app | Archiving | — | COVERED-48 | 48 | — |
| DB02 / DB12 / DB13 / DB14 / DB20 / DBACOCKPIT / HANA cockpit / HDBSQL | Database administration: space, backups, planning calendar, statistics | DBA | SUPERSEDED-50 | 50 | CloudNativePG backups/PITR, backup/restore drills (wave 48), partitioning (wave 50) |
| SM02 / SM13 / SE38 RSPARAM / SU10 lock | System messages, update admin | — | SUPERSEDED-5 | 5 | Shell banner/notifications — confirm system-wide announcement banner; treat covered |
| SM30 → table maintenance views (customizing data) | Customizing table maintenance | — | SUPERSEDED-3 | 3 | Configuration entities generated from metadata with UI |
| SE93 transaction codes / SMEN favorites / SSM2 area menus | Navigation | — | SUPERSEDED-5 | 5 | Workspaces, global search, saved views |
| SPRO / SPRO_ADMIN / S_IMG (IMG projects, project views, activities) | Implementation guide, IMG projects, customizing activities, documentation | Customizing navigator | SUPERSEDED-3 | 3 | Configuration workspace organized per module; implementer guide (wave 50); configuration-as-code with diff/versioning (wave 3 registry) |
| SOLMAN (Solution Manager: ChaRM, test suite, process documentation, monitoring, EarlyWatch, Focused Build) / SAP Cloud ALM / Signavio | ALM | — | SUPERSEDED-1 | 1 | GitHub + Argo CD + observability; process documentation = functional guides (wave 50) |
| SAP Router, Web Dispatcher, SAProuter, Gateway (SMGW), Cloud Connector | Network components | — | SUPERSEDED-1 | 1 | Envoy Gateway, ingress, cert-manager |
| SLD / LMDB / RZ70 | System landscape directory | — | SUPERSEDED-1 | 1 | Backstage catalog (wave 35) |
| SU01 → user types (dialog, system, communication, service, reference) | Technical users | — | COVERED-2 | 2 | Service accounts/API keys (wave 35) |
| SAP Identity Management (IdM) / Identity Authentication Service (IAS) / Identity Provisioning (IPS) | Cloud identity | — | SUPERSEDED-2 | 2 | Keycloak + SCIM |
| UI5/Fiori launchpad admin (/UI2/FLPD_CUST, /UI2/FLPCM_CUST, /UI2/SEMOBJ, /UI2/FLP, spaces & pages /UI2/FLPD_SPACE) — see cross-app | — | — | COVERED-5 | 5 | — |
| SAP GUI (Windows/Java/HTML), SAP Business Client (NWBC), WebGUI, Personas | Clients | — | SUPERSEDED-5 | 5 | React shell; Personas-style personalization = shell personalization |
| HANA: HDB user/role admin, SQL privileges, calculation views | DB-level security | — | SUPERSEDED-2 | 2 | Postgres roles + RLS; no end-user DB access |

### 13.30.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| SUIM (all reports) | Users by roles/authorizations/t-codes; roles by authorization; change documents (users/roles/profiles); comparison; critical authorizations | COVERED-2 | 2 | Access reports; SoD (wave 48) |
| RSUSR002 / RSUSR003 / RSUSR008_009_NEW / RSUSR100 / RSUSR200 | Users by complex criteria / standard users password check / critical combinations / change docs / logon dates & password changes | COVERED-2 | 2 | — |
| SM20 / RSAU_READ_LOG | Security audit log evaluation | COVERED-5 | 5 | — |
| ST03N / STAD / SM21 / ST22 | Usage statistics, statistics records, syslog, dumps | SUPERSEDED-5 | 5 | Grafana dashboards |
| SM37 job overview / SM36 | Job logs | COVERED-4 | 4 | Job monitor |
| SLG1 | Application log | COVERED-5 | 5 | — |
| SCU3 / RSVTPROT | Table change logs | COVERED-5 | 5 | Audit viewer for configuration entities |
| SE16/SE16N/SE16H | Table display | SUPERSEDED-3 | 3 | Entity explorer + lakehouse SQL (wave 46) with RLS |
| GRC AC reports: risk analysis (user/role/profile level), mitigation reports, firefighter logs, access request status, UAR status | Access governance reports | COVERED-48 | 48 | — |
| STMS import history / SE10 request logs | Transport history | COVERED-1 | 1 | Argo CD history, git log |
| AIS (Audit Information System: SECR / S_ALR_87100 system audit, business audit) | Audit information system | COVERED-48 | 48 | SOC 2 control evidence; wave 15 audit trail |
| EarlyWatch Alert / Solution Manager dashboards | System health | SUPERSEDED-5 | 5 | SLO dashboards |
| License administration (SLAW / USMM / LAW2) | License measurement | SUPERSEDED-50 | 50 | Tenant usage/cost model (wave 50) |

### 13.30.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| SM36 standard jobs (SAP_REORG_JOBS, SAP_REORG_SPOOL, SAP_COLLECTOR_FOR_PERFMONITOR, SAP_REORG_ABAPDUMPS, SAP_CHECK_ACTIVE_JOBS…) | Housekeeping jobs | SUPERSEDED-4 | 4 | Platform housekeeping (log retention, vacuum) via K8s CronJobs / job framework |
| SM18 / RSAU_FILE_ADMIN | Security audit log reorganization | COVERED-5 | 5 | Log retention policies |
| SUIM/PFCG mass user comparison (PFUD / PFCG_TIME_DEPENDENCY) | Role assignment validity cleanup | COVERED-2 | 2 | Effective-dated role assignments |
| SU10 mass lock / unlock, inactive users (RSUSR200) | User lifecycle | COVERED-2 | 2 | SCIM deprovisioning; wave 48 access reviews |
| GRC UAR campaigns, periodic risk analysis batch, firefighter log sync | Access certification | COVERED-48 | 48 | Access certification campaigns |
| STMS import jobs / transport of copies / ChaRM cycles | Release management | COVERED-1 | 1 | GitOps promotion |
| DB13 backups / statistics / reorg | DBA calendar | SUPERSEDED-50 | 50 | CloudNativePG scheduled backups; wave 48 restore drills |
| SPAM/SUM/SNOTE patch cycles, kernel upgrades | Patching | SUPERSEDED-1 | 1 | CI/CD with SBOM/vuln scans (Grype/Trivy), image signing |
| Client copy refresh (SCCL) for test systems | Environment refresh | SUPERSEDED-2 | 2 | Tenant clone/anonymized refresh — confirm anonymized test-data refresh tooling (wave 48 privacy + wave 50 ops); treat PARTIAL via row below |
| Anonymized production-to-test data refresh (TDMS / client copy with scrambling) | Test data management | PARTIAL-48 | 48 | Tenant clone with PII scrambling not explicitly named — partial; propose wave 48/50 |
| SLG2 / SARA application log deletion | Log cleanup | COVERED-5 | 5 | — |
| License measurement (USMM) | — | SUPERSEDED-50 | 50 | — |
| SM13 update error reprocessing / SM58 tRFC retry / SMQ2 queue restart | Error reprocessing | SUPERSEDED-4 | 4 | Outbox + Temporal retries, DLQ replay (wave 35) |

### 13.30.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Profile parameters (RZ10/RZ11: login/*, auth/*, rdisp/*, icm/*, rsau/*) | TPFET, PAHI | Password rules, lock on failed logons, SSO settings, session timeout, audit | COVERED-2 | 2 | Keycloak realm policies; gateway settings |
| PFCG role concept: org levels (PFCG_ORGFIELD_CREATE), derived roles, composite roles, menu, SU24 check indicators | AGR_*, USOBT_C, USOBX_C | — | COVERED-2 | 2 | Role/duty/privilege metadata; org-scoped assignment |
| SU25 steps / SU24 maintenance (check indicators) | USOBT_C | — | SUPERSEDED-2 | 2 | — |
| Security audit log filters (SM19/RSAU_CONFIG), audit classes | RSAU* | — | COVERED-5 | 5 | — |
| GRC AC: rule sets (functions, risks, SoD matrix), mitigating controls, risk owners, workflow MSMP, firefighter configuration, connectors | GRAC* | — | COVERED-48 | 48 | SoD rules + mitigation workflow |
| GRC AC: access request types, approval stages (MSMP), provisioning settings, UAR settings | GRAC* | — | COVERED-48 | 48 | Access request workflow via wave 4 + Keycloak provisioning |
| STMS configuration: transport domain, routes, layers, QA approval procedure, import strategies; CTS+ for non-ABAP | TMS* | — | SUPERSEDED-1 | 1 | Argo CD environments, promotion by digest, approval gates |
| SCC4 client settings (customizing changes allowed, cross-client, protection) | T000 | — | SUPERSEDED-2 | 2 | Environment-level write protection via GitOps |
| SE06 system change option, SE03 namespaces, software components | — | — | SUPERSEDED-1 | 1 | — |
| Operation modes (RZ04), logon groups (SMLG), RFC server groups (RZ12) | — | — | SUPERSEDED-1 | 1 | K8s scheduling/HPA |
| Background processing: job classes, server groups (SM61), event definitions (SM62) | TBTCO, BTCSEV | — | COVERED-4 | 4 | Job priorities/partitioning; event triggers (Kafka) |
| Printing: device types, output devices, access methods (SPAD), front-end printing, print queues | TSP03* | — | PARTIAL-32 | 32 | Print service for labels/documents; generic office print queue/printer management partial |
| SAPconnect nodes (SCOT), SMTP, fax, domains, send jobs | SX* | — | COVERED-5 | 5 | Email provider adapter |
| Table logging activation (rec/client, SE13 log data changes), change document objects (SCDO) | — | — | SUPERSEDED-5 | 5 | CDC audit for everything flagged audit=true in metadata |
| Number range buffering (SNRO: no buffering, main memory, parallel) | TNRO | — | COVERED-5 | 5 | Gap-free vs buffered sequences |
| Archiving customizing: archiving objects, residence times, archive infostructures (SARI/SARJ), content repository (OAC0), ILM policies | AOBJ, ARCH_*, ILM* | — | COVERED-48 | 48 | Retention policies, legal holds |
| ArchiveLink / KPro: content repositories (OAC0/OACT/OAC2/OAC3), document types, links to business objects (GOS) | TOA*, TOAOM | — | COVERED-5 | 5 | Attachments object store (S3-compatible) — confirm object storage for attachments; treat covered |
| SAML/OIDC/SNC configuration, trusted IdPs, user mapping | SAML2* | — | COVERED-2 | 2 | — |
| HTTP whitelist, CSRF, UCON (unified connectivity) RFC allowlist | UCON* | — | COVERED-2 | 2 | API gateway policies |
| Read Access Logging (SRAL) configuration | SRAL* | — | PARTIAL-48 | 48 | PII access logged (wave 42) for HR; configurable read-access logging for arbitrary sensitive fields partial — propose wave 48 |
| UI data protection masking (UI masking/logging add-on) | — | — | COVERED-42 | 42 | PII masking per role |
| Data destruction / blocking (ILM, BP blocking BUPA_PRE_EOP, end of purpose) | ILM* | — | COVERED-48 | 48 | Retention + erasure; end-of-purpose checks per party — confirm EoP checks across modules; treat covered |
| SAP Cloud ALM / Solution Manager process management, test management (ECATT SECATT, CBTA) | — | — | SUPERSEDED-1 | 1 | CI tests, Playwright, fixtures |

---

## 13.31 Cross-Application (workflow, IDoc/ALE, PI/PO, DRC e-documents, output management NAST/BRF+, Fiori launchpad, archiving/ILM, GDPR/IRF, attachments, texts, enterprise search)

### 13.31.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| SWDD / SWDD_SCENARIO / SWDM | Workflow builder (classic) / flexible workflow scenario editor / business workflow explorer | Workflow design | COVERED-4 | 4 | Workflow designer (configuration-driven approval chains) + Temporal |
| PFTC / PFTC_INS / PFTC_CHG / PFTC_DIS | Task maintenance (standard tasks, workflow templates) | — | SUPERSEDED-4 | 4 | Workflow templates in designer |
| PFAC / PFAC_INS / PFAC_CHG | Rules (agent determination: responsibilities, org, function module) | Agent rules | COVERED-4 | 4 | Approver resolution (role, hierarchy, amount band, delegation) |
| SWO1 / SWO2 / SWO3 / SWO6 / SE24 (ABAP OO) | Business object builder / repository / delegation | BOR | SUPERSEDED-3 | 3 | Entities + events from metadata |
| SWE2 / SWETYPV / SWEC / SWEL / SWELS / SWE3 / SWEINST | Event type linkages, change-document events, event trace, instance linkages | Event linkage | COVERED-4 | 4 | Entity-changed CloudEvents trigger workflows; AsyncAPI |
| SBWP / Fiori My Inbox / SWNCONFIG / SWNADMIN / SOST | Business workplace inbox, My Inbox app, extended notifications (email) | Task inbox | COVERED-4 | 4 | Human tasks UI in shell + notifications; delegation/substitution (HRUS_D2 / SBWP substitutes) — confirm substitution rules; treat covered |
| SWIA / SWI1 / SWI2_FREQ / SWI2_DEAD / SWI2_DURA / SWI2_ADM1 / SWI5 / SWI6 / SWI14 / SWPR / SWPC / SWU_OBUF / SWWL / SWWH | Work item administration (execute as admin, selection, frequency, deadlines, durations, without agents), workload, workflows for object, restart after error, continue after system crash, buffer sync, delete work items | Workflow admin | COVERED-4 | 4 | Temporal UI + workflow admin (retry, restart, reassign, SLA/deadline monitor) |
| SWU3 / SWU0 / SWUS / SWUD / SWU2 / SWU_EWBTE | Automatic workflow customizing / event simulation / test workflow / diagnosis / RFC monitor | — | COVERED-4 | 4 | Workflow test harness (Temporal test server in CI) |
| SWF_ADM_ / SWFVISU / SWF_PROCESS_ | Flexible workflow admin, task visualization (My Inbox UI), process monitor | — | COVERED-4 | 4 | — |
| WE02 / WE05 / WE09 / WE19 / WE07 / WE10 | IDoc display / list / search by content / test tool / statistics | IDoc monitoring | COVERED-35 | 35 | Message monitor with replay |
| WE20 / WE21 / WE30 / WE31 / WE60 / WE81 / WE82 / WE41 / WE42 / WE57 / WE47 / WE46 / WE40 / WE44 / BD51 / BD55 / BD56 / BD62 / BD64 / BD87 / BD20 / BD21 / BD50 / BD52 / BD53 / BD61 / BD10 / BD11 / BD12 / BD13 / BD14 / BD15 / BD16 / BD17 / BD18 | Partner profiles, ports, IDoc types, segments, documentation, message types, assignment, process codes (outbound/inbound), status processing, distribution model, reprocess, inbound/outbound processing, change pointers (activate, per message type, reduce), send material/customer/vendor/cost center… | ALE/IDoc configuration & distribution | SUPERSEDED-35 | 35 | EDI engine (X12/EDIFACT) + Kafka topic subscriptions + webhooks + REST/GraphQL; partner profiles, acknowledgements, message monitor; master-data distribution = events (no change pointers) |
| SALE / BDLS / BD54 / BD97 / SM59 | ALE customizing, logical system conversion, define logical systems, assign RFC | — | SUPERSEDED-35 | 35 | — |
| PI/PO: ESR (Enterprise Services Repository), Integration Directory (ID), SXMB_MONI / SXI_MONITOR / SXMB_ADM / SXMB_IFR / SRT_MONI / SRT_UTIL / IDX1 / IDX2 / IDX5 / SLDCHECK / RWB / PIMON | Process integration design/configuration/monitoring, IDoc adapter metadata, web service monitoring | Middleware | SUPERSEDED-35 | 35 | Integration hub (API catalog via Backstage, developer portal, webhooks, Kafka, iPaaS connectors Salesforce/Shopify/Stripe); message monitor with replay and partner SLAs |
| SAP Integration Suite (Cloud Integration/CPI iFlows, API Management, Open Connectors, Trading Partner Management, Event Mesh, Integration Advisor) | Cloud middleware | — | SUPERSEDED-35 | 35 | Same; Event Mesh = Kafka topics; TPM = EDI partner profiles |
| EDOC_COCKPIT / EDOC_RESUBMIT / EDOC_BACKGROUND / Fiori: eDocument Cockpit (DRC) / Manage Electronic Documents | Document and Reporting Compliance: e-invoices/e-documents per country (IT SDI, MX CFDI, ES SII, HU RTIR, IN e-invoice/e-way bill, PL KSeF, Peppol, FR, BR NF-e…) | e-documents | COVERED-35 | 35 | Peppol UBL 2.1, Factur-X/ZUGFeRD, country mandates framework (IT SDI, MX CFDI hooks); further mandates per country pack (wave 48) — note: only IT/MX hooks named; first packs US/UK/DE/FR/IN/MX/CA — IN e-invoice & FR 2026 mandate expected in packs |
| DRC Statutory Reporting (Run Statutory Reports / Manage Compliance Reports: VAT returns, EC sales list, SAF-T, withholding, Intrastat, e-filing) | Statutory reporting framework | — | COVERED-48 | 48 | Electronic reporting designer + regulator mandates registry; wave 13 extracts |
| NACE / NACZ / NACO / VV11-VV13 / VV21-23 / VV31-33 / MN01-MN27 / VOK2 / V/30 / V/40 / V/57 / V/59 / V/G6 / VN | Output determination (condition technique per application: V1 sales, V2 shipping, V3 billing, EF purchasing, EA RFQ, EV contracts, EL SA, ME inventory, Q QM, PM , MR invoice verif, KM) — output types, access sequences, procedures, condition records, print programs/forms, partners, media, dispatch time | Output management (NAST) | SUPERSEDED-46 | 46 | Output rules: per document type/event → channel (print/email/EDI/portal) with templates from document designer; events (wave 4) for timing; wave 35 EDI; no condition technique for output |
| S/4 Output Management (BRF+ output determination: OPD, output parameter determination, Manage Output Items , Adobe forms via ADS, email templates, printer queues) | S/4 output control | — | SUPERSEDED-46 | 46 | Same; BRF+ decision tables ↔ rules engine |
| BRF+ (BRFPLUS / FDT_WORKBENCH) / Decision Service Management (DSM) | Business rules framework (decision tables, formulas, rulesets) | Rules engine | PARTIAL-3 | 3 | Validation rules and OPA policies exist; a general-purpose, user-maintainable decision-table/rules engine (used by output, tax, credit, dispute, GRC, substitution) is implied across waves but not named as a shared service — partial; propose explicit "rules engine (decision tables)" in wave 3/4 |
| SE71 (SAPscript) / SMARTFORMS / SFP (Adobe forms) / ADS / Fiori form templates (Maintain Form Templates) | Form design | Forms | SUPERSEDED-46 | 46 | Operational report/document designer (invoices, POs, packing slips, checks) |
| SO10 / SO72 / SE75 (text objects/IDs) | Standard texts, text management | Text blocks | PARTIAL-5 | 5 | Document/line notes exist; a reusable standard-text library with placeholders and language versions for documents is not named — partial; propose wave 46 designer text blocks |
| GOS (Generic Object Services): attachments, notes, links, relationships, workflow overview, ArchiveLink (OAAD / OAWD / OAOR) | Attachments & notes on any object | — | COVERED-5 | 5 | Attachments & notes on any entity (journal attachments wave 11; DMS §13.35) |
| Document Relationship Browser (DRB) / document flow | Cross-document flow | — | COVERED-27 | 27 | Document flow (order→shipment→invoice→payment), trace ids |
| /UI2/FLP / /UI2/FLPD_CUST / /UI2/FLPCM_CUST / /UI2/FLPD_CONF / /UI2/SEMOBJ / /UI2/NWBC_CFG_CUST / /UI2/FLPCM_ (catalogs, groups, tiles, target mappings, spaces & pages, launchpad content manager) / /UI2/FLP_SYS_CONF / /IWFND/MAINT_SERVICE (OData services) / /IWFND/ERROR_LOG / /IWFND/GW_CLIENT / /UI5/THEME_DESIGNER / /UI5/UI5_REPOSITORY_LOAD | Fiori launchpad configuration, OData gateway, UI theme designer | Launchpad & gateway | COVERED-5 | 5 | React shell: workspaces by role, navigation, global search, personalization, theming; API gateway |
| SAP Screen Personas / GuiXT | Screen simplification | — | SUPERSEDED-5 | 5 | Metadata-driven forms + personalization |
| SAP Enterprise Search (ESH_COCKPIT / ESH_ADMIN) / Fiori search | Enterprise search | — | COVERED-5 | 5 | Global search |
| SARA / SARI / SARJ / SARE / AOBJ / SAR / DB15 / ILM: IRM_CUST / ILMARA / ILM_DESTRUCTION / ILM_STOR / ILM_SNAPSHOT / LHM (legal hold) / ILM retention warehouse | Data archiving (write/delete/store/retrieve), archive info system, ILM retention rules, legal holds, data destruction, retention warehouse (system decommissioning) | Archiving & ILM | COVERED-48 | 48 | Retention policies, legal holds, erasure; history in lakehouse (wave 46) — archive retrieval = lakehouse query; system decommissioning (retention warehouse) partially covered by migration-factory history load (wave 49) |
| GDPR: IRF (Information Retrieval Framework: data model, purposes, retrieval runs), blocking/deletion (BUPA_PRE_EOP, EoP checks), consent management (ECM), read access logging (SRAL), UI masking, data privacy workbench (DPW) | Data protection | — | COVERED-48 | 48 | GDPR/CCPA subject requests (export/erasure), retention; consent in HR (wave 42); SRAL partial (noted) |
| SAP Information Steward / MDG (Master Data Governance: change requests, data models, validation rules, replication) | Master data governance | — | COVERED-7 | 7 | Data governance: ownership, stewardship queue, change approval workflow (party); product/BOM governance via ECO (wave 9) — note MDG-style central governance for all master domains confirm; treat covered |
| MDG consolidation & mass processing (duplicate check, best record) | — | — | COVERED-7 | 7 | Dedup/merge with survivorship |
| SAP Business Partner Screening / Watch List Screening (GTS) | Sanctions/denied-party screening | — | GAP | propose 48 | Denied-party/sanctions screening of parties and documents not named; propose wave 48 (compliance) with provider adapter |
| Legal Content Management / contract management (SAP CLM, Ariba Contracts) | Legal contracts | — | PARTIAL-17 | 17 | Purchase/sales agreements exist; legal contract authoring, clause library, obligations — partial (noted in Ariba row) |
| SAP Jam / SAP Build Work Zone / Collaboration | Collaboration | — | OUT-OF-SCOPE | — | Integrate with Slack/Teams via webhooks (wave 35); no native collaboration suite |
| SAP Mobile Platform / SAP Mobile Services / Mobile Start / Fiori Client | Mobile platform | — | COVERED-5 | 5 | PWA shell; mobile apps (waves 25/32/45) |
| Business Application Studio / Web IDE / Eclipse ADT (SE80 successors) / ABAP Cloud / Steampunk / BTP extensions (side-by-side) | Extension development | — | SUPERSEDED-3 | 3 | Metadata extensions + public APIs/webhooks/MCP (waves 3/35/47) |
| Key User Extensibility (Custom Fields and Logic, Custom Business Objects, Custom CDS Views, Custom Analytical Queries, Adapt UI) | In-app extensibility (S/4) | — | COVERED-3 | 3 | Metadata kernel: entity extensions (fields, enums, hooks), custom entities; custom views (wave 46); adapt UI (wave 5) |
| SAP Business Technology Platform (BTP) services: Workflow Management, Business Rules, Process Automation, Event Mesh, API Management, Integration Suite, Document Management, Launchpad service | Platform services | — | SUPERSEDED-4 | 4 | Temporal, rules (partial), Kafka, hub, object storage, shell |
| SAP Cloud Appliance Library / S/4 activation (Best Practices content, SAP Activate) | Pre-configured content | — | SUPERSEDED-6 | 6 | Seed data / demo manufacturer; tenant templates (wave 50 provisioning) |
| Data Quality Management (DQM: address validation, duplicate check via SAP DQM microservices) | Address validation | — | COVERED-6 | 6 | Validated postal addresses, geocoding hook |
| SAP Geographical Enablement Framework (GEF) / maps | Geo | — | COVERED-6 | 6 | Geocoding; maps in TM/FSM (waves 34/45) |
| Business Event Handling (BTE: FIBF/BF / open FI events) | Event hooks in FI | — | SUPERSEDED-4 | 4 | Lifecycle hooks + events |
| SAP Business Rules (BRF+ in GRC, tax, dispute, output) — see above | — | — | PARTIAL-3 | 3 | — |
| Application Interface Framework (AIF: /AIF/CUST, /AIF/ERR, interface monitor) | Interface monitoring & error handling | — | COVERED-35 | 35 | Message monitor with replay, dead-letter, partner SLAs |
| SAP Process Mining (Signavio) / Process Observer (POC) | Process mining | — | PARTIAL-46 | 46 | Event stream + lakehouse enables mining; no native process-mining UI — partial |

### 13.31.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| SWI1 / SWI2_* / SWI5 / SWI6 | Work item selection, frequency, deadlines, durations, workload | COVERED-4 | 4 | Workflow dashboards (SLA, throughput) |
| SWEL | Event trace | COVERED-4 | 4 | Kafka topic browser / event log |
| WE02 / WE05 / WE07 / WE09 | IDoc lists & statistics | COVERED-35 | 35 | Message monitor |
| BD87 (status overview) | IDoc status overview | COVERED-35 | 35 | — |
| SXMB_MONI / SRT_MONI / /AIF/ERR | Integration message monitors | COVERED-35 | 35 | — |
| EDOC_COCKPIT (status by country) | e-document status | COVERED-35 | 35 | — |
| NAST analysis (VV13 / WE output status) / Fiori: Manage Output Items | Output status lists | COVERED-46 | 46 | Output log per document |
| SOST | Sent emails/fax status | COVERED-5 | 5 | Notification log |
| SARI / ILM reports | Archive info system queries | COVERED-46 | 46 | Lakehouse history queries |
| IRF retrieval run results | Subject access report | COVERED-48 | 48 | — |
| /IWFND/ERROR_LOG / /IWFND/APPS_LOG | OData gateway logs | SUPERSEDED-5 | 5 | OTel traces |
| /UI2/FLP usage analytics (Fiori usage) | Usage analytics | COVERED-5 | 5 | Product analytics on shell (OTel) |
| ESH search analytics | — | COVERED-5 | 5 | — |

### 13.31.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| SWWDHEX / SWWERRE / SWWCOND / SWWCLEAR (deadline monitoring, error monitoring, condition evaluation, clear work items) | Workflow background jobs | SUPERSEDED-4 | 4 | Temporal timers/retries |
| RSWWWIDE / SWWL (delete work items) / SWW_SARA archiving | Workflow housekeeping | COVERED-48 | 48 | Workflow history retention |
| RSEOUT00 / RBDAPP01 / RBDMANI2 / RBDMIDOC (change pointers → IDocs) / RSEIDOC2 / RBDAGAIN / RBDAGAIE | IDoc outbound processing, inbound posting, reprocess, change pointer processing | SUPERSEDED-35 | 35 | Event-driven outbox; replay |
| RSNAST00 / RSNASTED | Output processing (print/EDI) batch | COVERED-46 | 46 | Output jobs |
| EDOC_BACKGROUND / DRC submission jobs | e-document submission/polling | COVERED-35 | 35 | — |
| SARA write/delete/store jobs; ILM destruction runs | Archiving runs | COVERED-48 | 48 | Retention jobs |
| IRF retrieval runs; blocking runs (BUPA_PRE_EOP/EoP) | GDPR runs | COVERED-48 | 48 | — |
| /IWFND/ cache cleanup, /UI2/ cache invalidation | Gateway maintenance | SUPERSEDED-5 | 5 | — |
| ESH index updates (ESH_COCKPIT) | Search indexing | COVERED-5 | 5 | Search index from CDC |
| SXMS_ / integration archiving & retention | Message retention | COVERED-35 | 35 | — |
| MDG replication / data load jobs (DRFOUT) | Master data replication | SUPERSEDED-7 | 7 | Events |

### 13.31.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| SWU3 basic workflow settings (RFC destination WORKFLOW_LOCAL, WF-BATCH user, prefix numbers SWU9 , monitoring jobs) | SWF_* | — | SUPERSEDED-4 | 4 | Temporal cluster config |
| Flexible workflow scenarios (per document type: PR, PO, contract, journal entry, supplier invoice, credit memo…), step conditions, agent determination rules, deadlines, email notifications | SWF_FLEX* | — | COVERED-4 | 4 | Workflow designer with templates per entity |
| Substitution/delegation rules (SBWP, HRUS_D2), vacation handling | HRUS_D2 | — | COVERED-4 | 4 | Delegation/escalation with SLA timers |
| ALE: logical systems, distribution model, partner profiles, ports, serialization groups, filters, change pointers activation per message type | TBDLS, TBD*, EDP*, EDIPO* | — | SUPERSEDED-35 | 35 | Partner profiles for EDI; topic subscriptions |
| EDI: converter/subsystem settings, status codes, message control | EDI* | — | COVERED-35 | 35 | EDI engine (AS2/SFTP, acknowledgments 997/CONTRL) |
| PI/PO / Integration Suite configuration (communication channels, adapters, mappings, SLAs) | — | — | SUPERSEDED-35 | 35 | Hub |
| DRC: e-document types per country, activation, interface connectors (PEPPOL access point, SDI, CFDI PAC), source document types, status mapping | EDOC* | — | COVERED-35 | 35 | Country mandates framework; packs wave 48 |
| DRC statutory reporting: report definitions, categories, parameters, document and data sources, correspondence | — | — | COVERED-48 | 48 | ER designer |
| Output determination customizing (NACE per application: output types, procedures, access sequences, condition tables, print programs, forms, partner functions, media, dispatch times, storage mode) | T685B, TNAPR, NAST, T681* | — | SUPERSEDED-46 | 46 | Output rules/channels/templates |
| S/4 output control: output types, determination (BRF+), form templates, email templates, printer settings, channels | APOC_* | — | SUPERSEDED-46 | 46 | — |
| BRF+ applications, functions, rulesets, decision tables, expressions | FDT_* | — | PARTIAL-3 | 3 | Rules engine gap |
| Text determination (text objects, IDs, procedures) across SD/MM/FI | TTXID, TTXERN | — | COVERED-27 | 27 | Notes types per entity |
| GOS/ArchiveLink: document types, object types, links (OAC3), content repositories (OAC0), bar code entry (OAM1) , storage scenarios | TOA* | — | COVERED-5 | 5 | Attachment types, object storage |
| Fiori launchpad: catalogs, groups, spaces, pages, target mappings, roles (PFCG menu), theming, user defaults (default values per parameter) | /UI2/* | — | COVERED-5 | 5 | Workspaces, navigation by role, personalization |
| OData/Gateway activation (/IWFND/MAINT_SERVICE), service groups, system aliases | /IWFND/* | — | SUPERSEDED-35 | 35 | Generated OpenAPI/GraphQL; API catalog |
| Archiving objects: residence/retention, variants, archive routing, info structures, ILM rules (audit areas, policies: retention/destruction), legal hold management | AOBJ, ILM* | — | COVERED-48 | 48 | Retention policies per entity |
| Data protection: purposes, EoP (end-of-purpose) checks per application, blocking, IRF data model/purposes, SRAL, consent | ILM*, IRF*, SRAL* | — | COVERED-48 | 48 | Privacy requests; SRAL partial |
| Enterprise search: connectors, models, authorization | ESH* | — | COVERED-5 | 5 | — |
| Key user extensibility: custom fields (business contexts), custom logic (BAdI implementations in-app), custom business objects, custom CDS views, UI adaptation | — | — | COVERED-3 | 3 | Metadata extensions |
| Application log objects (SLG0), log retention | BALOBJ | — | COVERED-5 | 5 | — |
| AIF interfaces, error handling recipients, alerts | /AIF/* | — | COVERED-35 | 35 | — |
| MDG data models, change request types, workflows, validation (BRF+), replication (DRF) | MDG* | — | COVERED-7 | 7 | Governance workflow |
| Watch list screening / GTS compliance configuration | — | — | GAP | propose 48 | Screening gap |

---

## 13.32 LO-VC — Variant Configuration & AVC (Advanced Variant Configuration)

SAP context: classification-based configuration: characteristics (CT04), classes type 300 (CL02), configurable material KMAT (MM01), configuration profile (CU41), object dependencies (CU01: preconditions, selection conditions, procedures, actions, constraints/constraint nets), variant tables (CU60/CU61), variant functions (CU65), super BOM (CS40) and super routing with dependencies, configuration simulation (CU50), material variants (MM01 with configured values), order BOM (CU51), variant pricing (VA00/VA01 condition types with $SET_PRICING_FACTOR), VC modeling environment (PMEVC), AVC (S/4 Advanced Variant Configuration: new engine, Fiori Manage/Simulate Configuration Models, Manage Classes/Characteristics, product modeling environment), Product Structure Management (PSM/iPPE). Passage: wave 8 (constraint-based configurator on MiniZinc/OR-Tools; attributes & classification), wave 27 (configured order → production), wave 26 (variant pricing via engine), wave 9 (BOM/routing).

### 13.32.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| CT04 / CT06 | Characteristics maintain / where-used (values, intervals, multi-value, required, restrictable, reference to table field) | Configuration attributes | COVERED-8 | 8 | Attributes with value domains; reference attributes — confirm "reference characteristics" (bind to document field e.g. order qty); treat covered |
| CL02 / CL01 / CL03 / CL6B / CL6C | Class (type 300 variant) create/change/display, class hierarchy | Configuration classes | COVERED-8 | 8 | Classes |
| MM01 (KMAT, configurable flag) | Configurable material | — | COVERED-8 | 8 | Product master (configurable) |
| CU41 / CU42 / CU43 | Configuration profile create/change/display (class assignment, BOM explosion, interface design, process: planned/production order BOM, order BOM, sales order) | Config profile | COVERED-8 | 8 | Configurator model binding to product + explosion mode |
| CU01 / CU02 / CU03 / CU04 / CU05 | Object dependencies (global) create/change/display/list/where-used (preconditions, selection conditions, procedures, actions, constraints) | Dependencies | COVERED-8 | 8 | Constraint model (MiniZinc/OR-Tools): constraints, selection rules, defaults; procedure-style value inference — confirm procedural "procedures" semantics (set defaults, $SELF.x = ...) map to constraint model; treat covered |
| CU21 / CU22 / CU23 | Dependency nets (constraint nets) create/change/display | Constraint nets | COVERED-8 | 8 | — |
| CU60 / CU61 / CU62 / CU65 / CU66 / CU67 | Variant tables: structure / content / display; variant functions (ABAP) | Lookup tables / functions | PARTIAL-8 | 8 | Variant tables (decision tables) map to constraint tables — covered; *variant functions* (ABAP-coded) → SUPERSEDED by expression functions; table maintenance UI not named — partial |
| CU50 | Configuration simulation (for material with profile) | Test configuration | COVERED-8 | 8 | Configurator simulation/test (fixture set proof) |
| CU51 / CU52 / CU55 / CU56 | Order BOM maintain (sales order BOM — result-oriented/knowledge-based) / display / instantiation | Order BOM (ETO adjustments) | PARTIAL-21 | 21 | Order-specific BOM edits after configuration (ETO) not named — partial; propose wave 21/27 "order BOM" |
| CU70 / CU71 / CU72 | Configurable material: variant matching / material variant matching | Material variants | COVERED-8 | 8 | Variants: pre-defined variants (dimension combinations) and configured variants — matching an entered configuration to an existing variant (type matching) — confirm; treat covered |
| MM01 material variants (configured values in MRP 3 / Basic data) | Material variant (stockable configured product) | — | COVERED-8 | 8 | Released variant per configuration |
| CS40 / CS41 / CS42 | Super BOM allocation to configurable material / dependencies on BOM items (CS02 → dependencies) | Super BOM | COVERED-8 | 8 | Configurator BOM selection rules |
| CA01 super routing with dependencies on operations | Super routing | — | COVERED-9 | 9 | Operation selection rules — confirm; treat covered |
| CU80 / CUMODEL / PMEVC | Product modeling environment for VC (model tree, dependencies, variant tables) | Modeling IDE | COVERED-8 | 8 | Configurator model editor |
| VA01 → configuration (CUCFG screen, characteristic value assignment, consistency check) | Configure in sales order / quote | — | COVERED-27 | 27 | Configured products on quotes/orders; portal configurator (wave 30) — confirm customer-portal configurator; treat PARTIAL via row below |
| Customer-facing configurator (web, Fiori / SAP CPQ) | Guided selling | — | PARTIAL-30 | 30 | Customer portal order entry named; guided configurator UI for portal/e-commerce not explicitly named — partial |
| VK11 VA00 / VA01 (variant conditions, $SET_PRICING_FACTOR) | Variant pricing (surcharges by characteristic values) | — | COVERED-26 | 26 | Pricing engine with attribute-based conditions — confirm condition on configuration attribute; treat covered |
| CU44 / CU45 (configuration overview, config display in documents) | Display configuration (orders, deliveries, production orders) | — | COVERED-27 | 27 | Configuration on documents |
| CU7 / CUCPT (configuration copy/ templates) | Configuration templates | — | PARTIAL-8 | 8 | Saved configurations/templates for reuse — partial |
| AVC: Fiori Simulate Configuration Models / Manage Configuration Models / Manage Characteristics / Manage Classes / Configuration Model Overview / Object Dependencies (AVC syntax: constraints only, no procedures), AVC engine (variant configuration engine with HANA), "Classic vs Advanced" | S/4 AVC | — | COVERED-8 | 8 | Constraint-only engine matches AVC direction |
| PSM / iPPE (Product Structure Management: product variant structures, CMP nodes, PVS) | Product structure for complex configurable products (automotive) | — | PARTIAL-9 | 9 | Multi-level configurable structures covered by super BOM; iPPE-style product variant structures with alternatives/variants at node level — partial; propose wave 9 v1.x |
| Configuration in production (planned/production order configuration, CO02 → configuration), configured BOM explosion at order | — | — | COVERED-21 | 21 | Configured order explosion (phase 4 exit: configured product manufactured) |
| Configuration in purchasing (configurable PO items for configurable vendor products), configuration in service (IBase configuration) | — | — | PARTIAL-17 | 17 | Configured PO items not named — partial |
| VC with batch/serial effectivity, parameter effectivity (ECM) | Effectivity | — | PARTIAL-9 | 9 | Noted in PP (serial/parameter effectivity partial) |
| Fiori: Configuration Reporting / Where-used of characteristics in configurations | — | — | COVERED-8 | 8 | — |

### 13.32.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| CU05 (where-used dependencies) / CT06 (char where-used) / CL6C | Where-used lists | COVERED-8 | 8 | — |
| CU50 (simulation trace / explanation) | Configuration explanation (why value set/excluded) | COVERED-8 | 8 | Explain configuration (constraint explanation) — confirm explanation output; treat covered |
| CU44 / CUTRACE (configuration trace) | Trace dependencies | COVERED-8 | 8 | — |
| Configured documents lists (VA05 with config, COOIS with config) | — | COVERED-27 | 27 | — |
| AVC: Configuration Model Overview / usage analytics (most used values) | — | COVERED-46 | 46 | — |

### 13.32.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| CU mass release of dependencies / knowledge base (CU31 / CU32 / CU33 / CU34 / CU36 runtime versions for SCE/IPC) | Knowledge-base objects & runtime versions (for external configurators, IPC) | SUPERSEDED-8 | 8 | Single configurator service with versioned models |
| Model versioning / release (AVC model release) | Model release | COVERED-8 | 8 | Versioned configurator models with ECO (wave 9) |
| Re-explosion of configured orders after model change (OCM) | — | PARTIAL-21 | 21 | OCM partial (noted) |
| Material variant generation (mass create variants) | — | COVERED-8 | 8 | Variant explosion (1,000 variants < 5 s) |

### 13.32.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Class types (300 variant, 200 material, 001…), class status, characteristics groups, organizational areas | TCLA, TCLAT, TCLO | — | COVERED-8 | 8 | — |
| Configuration profile settings: BOM explosion (none/single/multi-level), configuration process (planned/production order, sales order, order BOM), result-oriented vs knowledge-based order BOM, interface design (UI groups) | CUCO | — | COVERED-8 | 8 | Explosion mode per model; UI groups → configurator form layout |
| Dependency editor settings, variant table DB-tables, variant functions (function modules) | — | — | SUPERSEDED-8 | 8 | Expression language |
| Pricing: variant condition types (VA00), $SET_PRICING_FACTOR, pricing procedure steps | T685 | — | COVERED-26 | 26 | — |
| Material variant settings (MRP 3: configured material, variant/configuration type matching activation) | — | — | COVERED-8 | 8 | — |
| AVC activation (per configuration profile: Advanced), AVC engine settings, Fiori apps | — | — | SUPERSEDED-8 | 8 | — |
| Configurable BOM/routing settings (item/operation dependencies allowed), ECM for VC objects | — | — | COVERED-9 | 9 | — |
| Order BOM settings (CU51 processing) | — | — | PARTIAL-21 | 21 | — |
| Integration with CPQ / IPC / SCE knowledge-base runtime | — | — | SUPERSEDED-8 | 8 | Configurator API for portals |

---

## 13.33 Batch Management & Serial Numbers

SAP context: batch master (MSC1N/MSC2N/MSC3N/MSC4N; batch level plant/material/client OMCT; batch status management OMCS?; batch classification class type 022/023 with characteristics e.g. expiry, potency, grade), batch determination (search strategies by application: MM MBC1, SD VCH1, PP COB1, WM LS?; condition technique with selection classes and sort rules CU70?), batch where-used (MB56, BMBC batch information cockpit), batch derivation (DVMO monitor, DVSP? setup, sender/receiver), shelf life (SLED, min remaining shelf life, OMJ5 checks), batch-specific units of measure (proportion/product units), electronic batch record, batch input/output in all movements, Batch Management in EWM/QM (quality inspection per batch, batch status), serial numbers (serial number profiles OIS2, serial number master IQ01-IQ09 = equipment, serial procedures MMSL/SDRE/SDLS/PPAU/PPRL/QMSL/PPSF/MMSL, serial in deliveries/orders/inventory, Fiori Manage Serial Numbers?), S/4 "Batch Management: Manage Batches, Batch Genealogy? Fiori Batch Information Cockpit". Passage: wave 8 (batch/serial tracking dimensions, profiles), wave 16 (batch/serial on movements, FEFO), wave 18 (serial capture at receipt, duplicate block), wave 24 (batch attributes, shelf life, disposition, potency, catch weight), wave 28 (batch/serial on shipments), wave 33 (QM per batch), wave 45 (serial → installed base).

### 13.33.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| MSC1N / MSC2N / MSC3N / MSC4N | Batch create/change/display/changes (basic data: production/expiry dates, status, vendor batch, classification values) | Batch master | COVERED-24 | 24 | Batch entity with attributes, shelf life, status; wave 8 tracking dimension |
| MSC5N / MSC (mass change batches) | Mass batch change | — | COVERED-5 | 5 | — |
| MCH1 / MCHA / MCHB (tables) | Batch tables (client/plant/stock level) | Data | COVERED-16 | 16 | Batch on-hand per warehouse/location |
| MBC1 / MBC2 / MBC3 | Batch search strategy (MM) create/change/display | Batch determination rules (MM) | COVERED-16 | 16 | Picking policies FEFO/FIFO + attribute-based selection — confirm attribute-based batch selection (e.g. grade ≥ X, customer-specific restrictions); treat covered |
| VCH1 / VCH2 / VCH3 | Batch search strategy (SD) | — | COVERED-28 | 28 | Customer-specific batch rules (e.g. min remaining shelf life per customer) — confirm; treat covered |
| COB1 / COB2 / COB3 | Batch search strategy (PP/PI) | — | COVERED-21 | 21 | Batch determination in production issues |
| LS (WM batch strategy) / /SCWM/ batch determination | — | — | COVERED-31 | 31 | — |
| MB56 | Batch where-used list (top-down / bottom-up) | Genealogy | COVERED-24 | 24 | Batch traceability; batch where-used (recall) — confirm "batch genealogy" explicitly for recall reporting; treat covered |
| BMBC | Batch information cockpit (search by attributes, stock, where-used, documents, QM results) | Batch cockpit | COVERED-24 | 24 | Batch workspace |
| DVMO / DVSP / DVMAN | Batch derivation: monitor / set up derivation (sender/receiver, rules) / manual derivation | Derivation | PARTIAL-24 | 24 | Batch derivation (inherit attribute values from component batches) partial (noted in PP-PI) |
| BMSM / batch status change (restricted/unrestricted) via MSC2N / QA11 UD | Batch status management | — | COVERED-24 | 24 | Batch disposition (QM UD, manual) |
| Batch split in deliveries / orders; batch entry in confirmations | — | — | COVERED-28 | 28 | — |
| Shelf life: SLED/BBD, remaining shelf life checks (OMJ5), batch expiry re-inspection (QA07), expired batch stock reports (MB5M) | Shelf life | — | COVERED-24 | 24 | Best-before/expiry, FEFO |
| Batch-specific UoM (CUNI with batch-specific conversion, potency conversion, active-ingredient management) | Proportion/product units | — | COVERED-24 | 24 | Potency-based consumption & catch weight dual UoM |
| Batch classification (CL20N class 023, characteristics CT04: e.g. expiry date, potency, country of origin, vendor batch) | Batch attributes | — | COVERED-24 | 24 | Batch attributes |
| Batch valuation (split valuation per batch, OMWC valuation type = batch) | Batch-level valuation | — | PARTIAL-20 | 20 | Split valuation gap |
| Batch numbering (internal/external, OMAD number ranges, batch number templates, customer-specific numbering via exit) | Batch numbering | — | COVERED-5 | 5 | Number sequences (per item/site templates) — confirm template-based batch numbers (e.g. YYMMDD-xxx); treat covered |
| Documentary batches (S/4 "documentary batch" for non-batch-managed materials) | Documentary batches | — | PARTIAL-24 | 24 | Recording batch numbers for info only (no stock segregation) not named — partial |
| Batch in EWM (batch-managed stock in bins), QIE | — | — | COVERED-31 | 31 | — |
| OIS2 — Serial number profiles (customizing; see below) | — | — | COVERED-8 | 8 | — |
| IQ01 / IQ02 / IQ03 / IQ04 / IQ08 / IQ09 | Serial number master (material serial = equipment category) create/change/display/list/display list | Serial master | COVERED-8 | 8 | Serial entity (tracking dimension) with status and history; equipment link (wave 44/45) |
| Serial numbers in: GR/GI (MIGO), deliveries (VL02N), sales orders, production orders (CO02 header/components), inspection lots, PM orders, physical inventory | Serial tracking | — | COVERED-16 | 16 | Serial on all movements; duplicate block (wave 18 proof) |
| Serial number history (IQ03 → history: stock, location, customer) | Serial genealogy | — | COVERED-45 | 45 | Installed base serial tracking from shipments; serial history — confirm full movement history per serial; treat covered |
| Fiori: Manage Batches / Batch Information Cockpit / Display Batch Genealogy / Manage Serial Numbers | — | — | COVERED-24 | 24 | — |
| Serial numbers at component level in finished-goods serial (as-built genealogy) | As-built records | — | PARTIAL-21 | 21 | Serial-to-serial component genealogy (as-built) not explicitly named — partial; propose wave 21 RAF capture |

### 13.33.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| MB56 / BMBC | Batch where-used / cockpit | COVERED-24 | 24 | — |
| MB5M | Expiration date list | COVERED-24 | 24 | — |
| MMBE / MB52 (batch level) | Stock by batch | COVERED-16 | 16 | — |
| MB5C | Batch pick-up list | COVERED-24 | 24 | — |
| CL30N / CL6BN (find objects/batches by characteristic values) | Batch search by attributes | COVERED-24 | 24 | — |
| IQ08 / IQ09 | Serial number lists | COVERED-8 | 8 | — |
| QA33 (per batch) / QGA3 | Batch quality results | COVERED-33 | 33 | — |
| DVMO (derivation monitor) | — | PARTIAL-24 | 24 | — |
| Fiori: Batch Genealogy / Expiring Batches / Serial Number History | — | COVERED-24 | 24 | — |

### 13.33.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| QA07 (recurring inspection / expiry monitoring, batch status change) | Expiry handling | COVERED-33 | 33 | — |
| MB5M-based expiry alerts | — | COVERED-24 | 24 | — |
| Batch archiving (SARA MM_SPSTOCK / batch deletion flag MSC2N, MM71) | — | COVERED-48 | 48 | — |
| Batch derivation runs (DVMO) | — | PARTIAL-24 | 24 | — |
| Serial number archiving / equipment | — | COVERED-48 | 48 | — |
| Batch level conversion (plant → material level, RVBLEVEL) | — | SUPERSEDED-8 | 8 | Batch uniqueness scope is a product attribute |

### 13.33.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| OMCT — Batch level (plant / material / client) | TCUCH | Uniqueness scope | COVERED-8 | 8 | Batch uniqueness scope per product |
| OMCS — Batch status management activation (restricted/unrestricted), initial status per material type/plant (OMAB) | TCUCH, T134, T001W | — | COVERED-24 | 24 | Batch status |
| OMAD / OMAE — Internal batch number assignment, number ranges, automatic creation at GR/GI | TCUCH, NRIV | — | COVERED-5 | 5 | — |
| OMCV / OMCZ — Batch classification activation per movement type/transaction, class type 023 settings, characteristics for batch class (standard characteristics LOBM_*) | T156, TCLA | — | COVERED-24 | 24 | — |
| OMJ5 — Shelf life expiration check per plant/movement type, SLED calculation (production date + shelf life) | T159L | — | COVERED-24 | 24 | — |
| Batch determination: condition tables, access sequences, strategy types (per application MM/SD/PP/WM), search procedures, assignment to movement types/order types/sales doc types, selection classes, sort rules (CU70) , quantity proposal, batch split settings | T6 , TLSP | — | COVERED-16 | 16 | Picking policies & batch selection rules |
| Batch derivation: activation, sender/receiver fields, derivation events, rules | DVMO* | — | PARTIAL-24 | 24 | — |
| Batch-specific units of measure: activation, proportion/product units, characteristics (LOBM_*) | T006, TCUBSUOM | — | COVERED-24 | 24 | — |
| Batch valuation (valuation type = batch) activation | T001K, T149 | — | PARTIAL-20 | 20 | — |
| Documentary batch activation (S/4) | — | — | PARTIAL-24 | 24 | — |
| OIS2 — Serial number profiles (serialization level, serializing procedures per business transaction: MMSL goods movement, SDRE returns, SDLS deliveries, PPAU production order header, PPRL production release, PPSF serial in production, QMSL inspection, HUSL HU; equipment requirement, stock check) | T377, T377P, T377X | Optional/mandatory serial per transaction; stock validation | COVERED-8 | 8 | Serial tracking profiles (track at receipt/issue/shipment/production) |
| OIS / OIEQ — Serial numbers: equipment category for serial numbers, number assignment (serial = equipment), equipment creation automatic | T377, T370T | — | COVERED-8 | 8 | — |
| Serial number status management, serial number history activation | — | — | COVERED-8 | 8 | — |
| EWM/WM batch & serial settings | — | — | COVERED-31 | 31 | — |

---

## 13.34 CA-CL — Classification System

SAP context: characteristics (CT04: formats CHAR/NUM/DATE/TIME/CURR, values, intervals, multi-value, required, additional values, reference to table fields, documents, long texts), classes (CL01/CL02/CL03: class types 001 material, 002 equipment, 003 functional location, 017 document, 022 batch (plant), 023 batch (material), 032 release strategy, 300 variant, 010 vendor, 011 customer, 012 BP…), class hierarchies (CL24N? CL6? ), object assignment (CL20N single object/multiple classes, CL24N class/multiple objects, CL22N? ), find objects (CL30N/CL6BN), where-used (CL6AN? ), mass change (CLMM), class type customizing (O1CL), characteristic groups, organizational areas, status, keywords, classification data transfer (BAPI_OBJCL_*), standard characteristics (LOBM_, etc.). Passage: wave 8 ("Attributes & classification (SAP CL-style classes) and category hierarchies") used by products, batches, equipment, documents; metadata kernel typed fields (wave 3).

### 13.34.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| CT04 / CT06 / CT10 / CT12 | Characteristics maintain / where-used / list / dependencies | Attribute definitions (typed, value sets, intervals, multi-value, units, templates) | COVERED-8 | 8 | Attribute definitions with typed value domains and UoM |
| CL01 / CL02 / CL03 / CL04 | Class create/change/display/delete (class type, characteristics, keywords, standards, documents, status, organizational areas, hierarchy) | Classes | COVERED-8 | 8 | Attribute groups/classes per entity type |
| CL24N / CL20N / CL22N / CL25 | Assign objects to class / assign class(es) to object / display assignments / class hierarchy maintenance | Object classification | COVERED-8 | 8 | Attribute assignment to products/batches/equipment/documents/parties — confirm classification applies to parties (010/011) and documents (017); treat covered |
| CL6AN / CL6BN / CL30N / CL31 | Where-used (class), find objects in classes (search by characteristic values), class list | Search | COVERED-8 | 8 | Attribute search (global search + filters) |
| CLMM | Mass change classification data | — | COVERED-5 | 5 | — |
| CL2B / CLHP | Class hierarchy / hierarchy display | — | COVERED-8 | 8 | Category hierarchies |
| CLGT / CLWA / CL6 (class statistics, characteristics overview) | — | — | COVERED-8 | 8 | — |
| CTBW (characteristic ↔ BW extraction) | — | — | SUPERSEDED-46 | 46 | Attributes stream to lakehouse |
| CL02 class type 032 (release strategy) | Classification used for release strategies | — | SUPERSEDED-17 | 17 | Policy-driven approvals |
| CL02 class type 300 (variant) — see §13.32 | — | — | COVERED-8 | 8 | — |
| CL02 class types 022/023 (batch) — see §13.33 | — | — | COVERED-24 | 24 | — |
| CL02 class type 017 (document) — see §13.35 | — | — | COVERED-8 | 8 | — |
| CL02 class types 002/003 (equipment / functional location) | Technical object classification | — | COVERED-44 | 44 | — |
| Engineering/technical attributes with UoM & tolerances (e.g. for parametric search of parts) | Parametric search | — | COVERED-8 | 8 | Attribute search |
| Fiori: Manage Characteristics / Manage Classes / Classify Products | — | — | COVERED-8 | 8 | — |

### 13.34.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| CL30N / CL6BN | Find objects in class (by values) | COVERED-8 | 8 | — |
| CL6AN / CT06 | Where-used | COVERED-8 | 8 | — |
| CL6C / CLGT | Class/characteristic lists, statistics | COVERED-8 | 8 | — |
| Fiori: Classification analytics (products by attribute) | — | COVERED-46 | 46 | — |

### 13.34.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| CLMM (mass update) | — | COVERED-5 | 5 | — |
| Classification reorganization / redundancy cleanup (RCCLUKA / CL) | — | SUPERSEDED-8 | 8 | Metadata registry |
| Classification data transfer (BAPI_OBJCL_CREATE, LSMW object CLASSIFICATION) | Migration | COVERED-10 | 10 | Migration DSL for attributes |

### 13.34.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| O1CL — Class types per object (table, screen, key fields, multiple classification, standard class, hierarchy allowed, variant class) | TCLA, TCLAO, TCLAT | — | SUPERSEDED-3 | 3 | Attribute groups attachable per entity in metadata |
| Characteristics groups (CT04), characteristic status, organizational areas, keywords | TCMG, TCLST | — | COVERED-8 | 8 | — |
| Class status (released/locked), class groups | TCLST | — | COVERED-8 | 8 | — |
| Characteristic templates / formats, default values, currency/UoM settings | — | — | COVERED-8 | 8 | — |
| Object key settings per class type (e.g. material+plant for 022) | TCLAO | — | COVERED-8 | 8 | — |
| Classification in BW/extraction settings (CTBW) | — | — | SUPERSEDED-46 | 46 | — |
| Attribute inheritance in class hierarchies | — | — | COVERED-8 | 8 | Category hierarchies with inherited attributes — confirm inheritance; treat covered |

---

## 13.35 DMS — Document Management System (CA-DMS), Easy DMS, ECTR, CAD integration

SAP context: document info records (CV01N/CV02N/CV03N/CV04N; document type, part, version, status network, originals stored in KPro/content server, object links to material/BOM/equipment/orders/customers/WBS, classification 017, document hierarchies/structures CV11/CV12/CV13?, document BOM), document distribution (CVI? ), status management with release workflow, change management via ECM, Easy DMS (Windows Explorer integration), SAP Engineering Control Center (ECTR) for CAD (AutoCAD/SolidWorks/NX) with BOM sync, conversion (PDF/viewer), ArchiveLink (OAAD) for scanned docs, Fiori: Manage Documents, SAP Document Management service (BTP), Product Lifecycle Management (PLM: engineering record, PLM web UI, Enterprise Product Development EPD). Passage: wave 5 (attachments on any entity), wave 8 (product images/documents), wave 9 (ECO workflow, "PLM integration" named), wave 48 (retention) — a true DMS (versioned, status-controlled, CAD-linked documents) is only PARTIAL.

### 13.35.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| CV01N / CV02N / CV03N | Document info record create/change/display (document type, number, part, version; status; originals; object links; classification; texts) | Document master | PARTIAL-8 | 8 | Attachments (wave 5) and product documents (wave 8) exist; a *document info record* with type, versioning, part/version numbering, status network and release is not named — partial; propose wave 9 "engineering documents" (drawings/specs under ECO control) |
| CV04N | Find document (by attributes, classification, object links, full text) | Search | PARTIAL-8 | 8 | Global search covers attachments metadata; attribute/classification search on documents partial |
| CV11 / CV12 / CV13 / CV15 | Document structure (document BOM) create/change/display / where-used | Document hierarchies | GAP | propose 9 | Document structures (assemblies of documents, e.g. drawing sets) not named — propose wave 9 |
| CV01N → originals (check-in/check-out, KPro storage categories, viewer, conversion) | Original files (CAD, PDF, Office) with check-out locking | Files | PARTIAL-5 | 5 | Attachments stored; check-out/check-in locking, multiple originals per version, viewer/conversion (2D/3D) not named — partial |
| CV01N → object links (MARA, STKO, EQUI, IFLOT, AUFK, KNA1, LFA1, PRPS, VBAK, EKPO…) | Link documents to business objects | — | COVERED-5 | 5 | Attachments on any entity; cross-entity links — confirm one document linked to many entities; treat covered |
| CV01N → status network (in work → released → obsolete), status with release/approval workflow, digital signature | Document status & release | — | PARTIAL-9 | 9 | ECO workflow covers BOM/route release; document release state machine with signature — partial |
| CV01N → version / part / change number (ECM) | Document versioning under ECM | — | PARTIAL-9 | 9 | ECO-controlled document versions partial |
| CV01N → classification (class type 017) | Document attributes | — | COVERED-8 | 8 | Attribute classes on documents |
| CV01N → document distribution (recipients, distribution orders CVI1 / CVI2 / CVI8) | Distribution of documents (e.g. to suppliers) | — | PARTIAL-20 | 20 | Supplier portal document exchange named; controlled distribution with acknowledgement partial |
| Easy DMS / SAP DMS Connector for Windows | Explorer-integrated document management | — | SUPERSEDED-5 | 5 | Web shell drag-drop + Excel/CSV import; desktop sync not planned |
| ECTR (Engineering Control Center): CAD integration (SolidWorks, Inventor, NX, CATIA, AutoCAD, ECAD), BOM derivation from CAD, 3D viewer, DMS for CAD | CAD–ERP integration | — | GAP | propose 9 | CAD integration (CAD file management, CAD BOM → engineering BOM sync) not named; wave 9 says "PLM integration" only as reference — propose wave 9 (v1.x) PLM/CAD connector (e.g. via a PLM system) rather than native CAD management |
| OAAD / OAWD / OAOR / OAC0 (ArchiveLink scanned documents, business document navigator) | Scanned document linking (incoming invoices, delivery notes) | — | COVERED-5 | 5 | Attachments; OCR capture (wave 18) |
| Document BOM items (CS01 item category D) | Documents as BOM components (e.g., drawings, instructions) | — | PARTIAL-9 | 9 | BOM line type "document" not named — partial |
| PLM: Engineering Record (change management cockpit), Product Structure Management, Visual Enterprise (3D visualization), Recipe Development (PLM-RDP), Specification management (EHS specs), Labeling (GLM) | PLM suite | — | PARTIAL-9 | 9 | ECR/ECO covered; recipe development with formula trials/specs (process industries), specification management, global label management — partial; propose wave 24 (recipe dev) & wave 48 (labeling compliance) |
| SAP Document Management service (BTP) / Document Information Extraction | Cloud DMS | — | COVERED-5 | 5 | Object storage; OCR (wave 18) |
| Fiori: Manage Documents / Manage Document Info Records | — | — | PARTIAL-8 | 8 | — |

### 13.35.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| CV04N | Document search | PARTIAL-8 | 8 | — |
| CV13 / CV15 | Document structure display / where-used | GAP | propose 9 | — |
| CVI8 (distribution log) | Distribution status | PARTIAL-20 | 20 | — |
| Document lists per object (object links) | — | COVERED-5 | 5 | Attachment lists |
| ECTR reports (CAD BOM comparison) | — | GAP | propose 9 | — |

### 13.35.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| CV conversion runs (originals → PDF/neutral formats) | Conversion server runs | PARTIAL-5 | 5 | Preview generation for attachments — partial |
| Document archiving / retention (SARA CV_DVS / KPro cleanup) | — | COVERED-48 | 48 | — |
| Document distribution runs (CVI) | — | PARTIAL-20 | 20 | — |
| ECTR sync jobs | — | GAP | propose 9 | — |

### 13.35.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| DC10 — Document types (number assignment, version/part numbering, status network, object links, screen layout, classification class, originals count, versioning) | TDWA, TDWS, TDWO, TDWP | — | PARTIAL-8 | 8 | Attachment types exist; full document type model partial |
| DC20 — Status (document status definitions, release flag, content version) | TDWS | — | PARTIAL-9 | 9 | — |
| DC30 — Workstation applications (file types, viewer/editor, conversion) | TDWP | — | PARTIAL-5 | 5 | Preview/viewer mapping partial |
| OACT / OAC0 / KPro storage categories, content repositories, cache servers | SDOKSTCA, TOAAR | — | COVERED-5 | 5 | Object storage buckets |
| Object link customizing (DC screen numbers, object types allowed per document type) | TDWO | — | COVERED-5 | 5 | — |
| Document distribution customizing (recipient lists, distribution types) | — | — | PARTIAL-20 | 20 | — |
| ECTR configuration (CAD applications, templates, BOM sync rules) | — | — | GAP | propose 9 | — |
| Digital signature for documents (signature strategies) | — | — | GAP | propose 24 | E-signature framework gap (noted in PP-PI) |
| Full-text search activation (TREX/HANA) for originals | — | — | PARTIAL-5 | 5 | Attachment content indexing not named — partial |

---

## 13.36 Analytics — BW/4HANA, CDS views & embedded analytics, SAP Analytics Cloud, LIS, Report Painter, SAP Query

SAP context: BW (RSA1 workbench, DataSources RSA5/RSA6/RSA3, process chains RSPC, InfoProviders, transformations/DTPs, queries BEx RSRT/BEx Query Designer/Analyzer, Analysis for Office, BW/4HANA, Open ODS, HANA calculation views, BW Bridge), embedded analytics (CDS views with @Analytics annotations, analytical queries, Fiori analytical list pages/overview pages, KPI modeler & Smart Business, Query Browser, View Browser, Custom Analytical Queries, Multidimensional reports), SAP Analytics Cloud (stories, planning, predictive, Digital Boardroom, live connection to S/4), Datasphere, SAP Query (SQ01/SQ02/SQ03, SQVI QuickViewer, InfoSets), Report Painter/Writer (GRR1/GR51/GR55), LIS (SIS/PURCHIS/INVCO/PP-IS/QMIS/PMIS; MC* transactions), Drill-down reporting (FGI0, KE30, FM/PS), Fiori Overview pages. Passage: wave 46 (CDC → Iceberg lakehouse, Trino/ClickHouse, dbt, Cube semantic layer with governed metrics and RLS, embedded dashboards, self-service explorer, scheduled reports, export to Excel/Power BI/Tableau, operational document designer, OpenMetadata catalog/lineage), wave 15 (financial report designer), wave 5 (saved views, export).

### 13.36.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| RSA1 | BW Data Warehousing Workbench (InfoObjects, DataSources, InfoProviders, transformations, DTPs, process chains) | Warehouse modeling | SUPERSEDED-46 | 46 | CDC → Iceberg lakehouse; dbt models replace transformations/InfoProviders |
| RSA5 / RSA6 / RSA3 / RSA7 / LBWE / LBWQ / LBWG / SBIW | Install/maintain DataSources, extractor checker, delta queue, LO cockpit (logistics extraction), setup tables | Extraction | SUPERSEDED-46 | 46 | Debezium CDC from every domain; no extractors/setup tables |
| RSPC / RSPCM / RSPC1 | Process chains (maintenance / monitor) | ETL orchestration | SUPERSEDED-46 | 46 | dbt + job framework (wave 4) with lineage |
| RSRT / RSRT2 / RRMX / BEx Query Designer / BEx Analyzer / Analysis for Office (AO) / BEx Web | Query monitor / BEx reporting / Excel-based analysis | Query & reporting | SUPERSEDED-46 | 46 | Semantic layer (Cube) + self-service explorer; Excel export & Power BI/Tableau connectivity |
| RSD1 / RSD5 / RSDS / RSDCUBE / RSDODS / RSDMPRO | InfoObjects, DataSources, InfoCubes, DSOs, MultiProviders, CompositeProviders (BW/4: aDSO, CompositeProvider, Open ODS) | Modeling objects | SUPERSEDED-46 | 46 | dbt models + semantic cubes |
| RSECADMIN | BW analysis authorizations | RLS in BW | SUPERSEDED-46 | 46 | RLS mirrored from OPA |
| RSMO / RSRV / RSDDV / RSKC / RSBATCH | Monitor, analysis & repair, aggregates/BWA/HANA, special chars, batch manager | BW admin | SUPERSEDED-46 | 46 | Lakehouse ops, OpenMetadata |
| HANA calculation views (HANA Studio / Web IDE), HANA Live, Smart Data Integration | HANA native modeling | — | SUPERSEDED-46 | 46 | Trino/ClickHouse views; dbt |
| CDS views (ADT: @Analytics.query, @Analytics.dataCategory CUBE/DIMENSION, @ObjectModel), Custom CDS Views (Fiori), View Browser, Query Browser | Embedded analytics virtual data model | — | COVERED-46 | 46 | Semantic layer models over lakehouse (and OLTP for operational views); custom views via self-service explorer with governed metrics |
| Custom Analytical Queries (Fiori), Manage KPIs and Reports (KPI modeler, Smart Business tiles), Analytical List Page, Overview Pages | Embedded analytics apps | — | COVERED-46 | 46 | Embedded dashboards in every workspace; KPI tiles (OTIF, OEE, DSO…) |
| Multidimensional reports (Design Studio/Lumira/WebDynpro grid), Fiori "Multidimensional" apps | Pivot analysis | — | COVERED-46 | 46 | Self-service explorer pivot |
| SAP Analytics Cloud (stories, data models, live/import connections, planning models, predictive scenarios (Smart Predict), Digital Boardroom, Analytics Designer, SAC for Group Reporting) | Cloud BI & planning | — | COVERED-46 | 46 | Embedded dashboards + explorer; planning in wave 38; predictive via wave 22 forecasting & wave 47 agents |
| SAP Datasphere (spaces, data builder, semantic layer, data marketplace), BW Bridge | Cloud data warehouse | — | COVERED-46 | 46 | Lakehouse + semantic layer + catalog |
| SQ01 / SQ02 / SQ03 / SQVI | SAP Query (queries, InfoSets, user groups), QuickViewer | Ad hoc query | SUPERSEDED-46 | 46 | Self-service explorer with RLS |
| GRR1 / GRR2 / GRR3 / GR51 / GR52 / GR53 / GR55 / GR32 / GR37 / GRCT / GRS | Report Painter / Report Writer: reports, libraries, sets, variables, report groups | Financial/CO reporting | SUPERSEDED-15 | 15 | Row/column report designer (Management-Reporter model) |
| GS01 / GS02 / GS03 / GS11 | Sets (basic, single, multi) for Report Writer / allocation | Sets | SUPERSEDED-6 | 6 | Hierarchies & dimension groups |
| FGI0 / FGI1 / KE30 / CJE0 / FMEN / S_ALR drill-down reports | Drill-down reporting tools | — | SUPERSEDED-46 | 46 | Explorer drill-down |
| MC* (LIS: MCSI, MC21 info structures, MC18 field catalogs, MC24 update rules, MC3 planning, OMO1 update control, standard analyses MC.x, MCE*, MCT*, MCP*, MCI*, MCX*, MCL*) | Logistics Information System | — | SUPERSEDED-46 | 46 | Lakehouse + semantic metrics; see per-module rows |
| S_ALR_* report trees (SAP Easy Access: Information Systems) / SARP / SERP (report trees) / SE43 area menus | Report navigation | — | SUPERSEDED-5 | 5 | Workspace navigation + saved reports |
| Report variants (SE38 variants, SU3 defaults), ALV layouts (/N ALV grid, layout management), ALV export to Excel | Report personalization | — | COVERED-5 | 5 | Saved views, grid personalization, Excel/CSV export |
| Scheduled report distribution (SM36 + SP01 + SOST; BW broadcasting RSRD_ADMIN; SAC schedule publications) | Report broadcasting | — | COVERED-46 | 46 | Scheduled reports (PDF/XLSX/email) |
| SAP BusinessObjects BI (Web Intelligence, Crystal Reports, Lumira, Dashboards) | Enterprise BI | — | SUPERSEDED-46 | 46 | Embedded BI + export to Power BI/Tableau |
| Crystal Reports / Adobe forms for operational documents (invoice, PO layouts) | Operational documents | — | COVERED-46 | 46 | Operational report/document designer (replaces SSRS/Adobe layouts) |
| Data catalog / metadata (SAP Information Steward, Datasphere catalog, BW metadata repository RSOR) | Catalog & lineage | — | COVERED-46 | 46 | OpenMetadata catalog & lineage |
| Data quality dashboards (Information Steward) | DQ monitoring | — | PARTIAL-46 | 46 | Metric reconciliation checks exist; generic data-quality rule dashboards (completeness, validity per entity) partial — propose wave 46 DQ rules on catalog |
| SAP Predictive Analytics / Predictive Analytics Integrator (PAI) / ISLM (Intelligent Scenario Lifecycle Management) / HANA PAL/APL | Embedded ML scenarios | — | SUPERSEDED-47 | 47 | Agent runtime + Python forecasting/ML services; eval harness (wave 47) |

### 13.36.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| BEx queries / AO workbooks / SAC stories (all standard BW business content: FI, CO, SD, MM, PP, QM, PM, HR InfoCubes/queries) | Standard BI content | SUPERSEDED-46 | 46 | Governed metric library (revenue, margin, DSO, DPO, OTIF, OEE, inventory turns, etc.) + standard dashboards per workspace |
| Fiori Overview Pages (Procurement, Sales, Finance, Inventory, Production, Quality, Maintenance, HR) | Role-based KPI pages | COVERED-46 | 46 | Embedded dashboards in every workspace |
| Fiori analytical apps (hundreds: e.g. Sales Volume, Overdue Receivables, Inventory Turnover, Supplier Evaluation, Production Order Cycle Time) | — | COVERED-46 | 46 | Covered per-module rows |
| Query Browser / View Browser | Discover CDS views/queries | COVERED-46 | 46 | Catalog |
| S_ALR_* standard reports (all modules) | Classic reports | COVERED-15 | 15 | Report designer + explorer; see per-module tables |
| SQVI / SQ01 outputs | Ad hoc lists | SUPERSEDED-46 | 46 | — |
| LIS standard analyses (MC.x etc.) | — | SUPERSEDED-46 | 46 | — |
| Usage/monitoring: RSRT statistics, BW statistics, SAC usage | BI usage analytics | COVERED-46 | 46 | — |

### 13.36.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| RSPC process chains (daily loads, delta, rollup, compression, aggregates) | ETL schedule | SUPERSEDED-46 | 46 | Streaming CDC (p95 lag < 60 s) + dbt scheduled models |
| LBWE / LBWQ / setup table fills (OLI*BW: OLI7BW, OLI3BW…) | LO extraction setup | SUPERSEDED-46 | 46 | — |
| OMO1 / LIS update runs, OLIX (LIS rebuild) | LIS statistics | SUPERSEDED-46 | 46 | — |
| RSRD_ADMIN / broadcasting / SAC publication schedules | Report distribution | COVERED-46 | 46 | Scheduled reports |
| Data archiving of BW (near-line storage NLS) | — | SUPERSEDED-46 | 46 | Iceberg retention/partitioning |
| Metric reconciliation (revenue in semantic layer = GL daily) | — | COVERED-46 | 46 | Automated check (proof) |
| ISLM model retraining schedules | — | SUPERSEDED-47 | 47 | Agent/ML eval & retraining pipelines |

### 13.36.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| BW source system connection (RSA1: source systems, ODP, SLT/RFC), BW settings (RSCUSTV*, RSADMIN) | RSADMIN, RSBASIDOC | — | SUPERSEDED-46 | 46 | Debezium connectors per domain DB |
| LIS customizing (MC21 info structures, MC24 update rules, OMO1 update control per application, period units) | TMC* | — | SUPERSEDED-46 | 46 | — |
| Report Painter libraries, standard layouts, report groups; Report Writer sets | T803*, T801* | — | SUPERSEDED-15 | 15 | — |
| SAP Query: InfoSets (SQ02), user groups (SQ03), query areas (global/standard) | AQ* | — | SUPERSEDED-46 | 46 | — |
| Embedded analytics: analytical authorizations (CDS DCL), KPI/Smart Business configuration (KPI catalog, evaluations, tiles), analytical app configuration | — | — | COVERED-46 | 46 | RLS from OPA; KPI tiles |
| SAC: connections, models, teams/roles, data access control | — | — | COVERED-46 | 46 | — |
| Datasphere spaces, connections, semantic layer | — | — | COVERED-46 | 46 | — |
| Data catalog/lineage configuration | — | — | COVERED-46 | 46 | OpenMetadata |
| Report scheduling & distribution (broadcast settings, email templates) | — | — | COVERED-46 | 46 | — |
| ALV layout administration (global vs user layouts, default layouts) | LTDX | — | COVERED-5 | 5 | Saved views (shared/personal) |
| Export governance (Excel export restrictions, download audit) | — | — | PARTIAL-48 | 48 | Export of PII/financial data audit & restriction policies not named — partial; propose wave 48 |

---

## 13.37 AI — SAP Joule, SAP Build Process Automation, Business AI (SUPERSEDED by wave 47)

SAP context: Joule (generative AI copilot in S/4HANA Cloud, SuccessFactors, Ariba: navigational, transactional, informational, Joule agents, Joule Studio), SAP Build Process Automation (BPA: workflow + RPA bots, decisions/business rules, process visibility), SAP CoPilot (legacy), SAP Business AI scenarios (cash application ML, AP invoice matching proposals, MRP exception triage, predictive delivery, intelligent product proposals, GR/IR account reconciliation ML, situation handling (Situation Framework: situation types, situation pages), Intelligent Scenario Lifecycle Management (ISLM), Document Information Extraction, Business Document Processing, SAP AI Core/AI Launchpad, generative AI hub), SAP Signavio process AI. Passage: wave 47 (MCP server over every API/entity/report with OPA, copilot UI, Temporal-backed agent runtime with approval gates — AP coding, cash application, MRP exception triage, collections outreach, journal anomaly detection — eval harness in CI, prompt/version registry, cost budgets, responsible-AI controls, model-agnostic provider layer).

### 13.37.1 Master data & documents

| T-code | Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Joule (navigational/transactional/informational capabilities; natural-language navigation, create documents, Q&A over data/documentation) | Copilot in S/4 Fiori | Copilot | SUPERSEDED-47 | 47 | Copilot UI in the shell: ask-the-data (semantic layer), explain-this-record, draft documents, summarize workflows; MCP server exposes every API/entity/report |
| Joule Agents (e.g. dispute resolution agent, cash collection agent, maintenance planner agent, sourcing agent, financial accruals agent) / Joule Studio (build custom agents/skills) | Autonomous agents | Agents | SUPERSEDED-47 | 47 | Agent runtime (Temporal) with approval gates: AP invoice coding, cash application suggestions, MRP exception triage, collections outreach, journal anomaly detection; agent builder = MCP tools + workflow designer |
| SAP Build Process Automation: processes (workflow), automations (RPA bots), decisions (business rules), forms, process visibility dashboards, pre-built content (SAP Store) | Low-code process automation + RPA | — | SUPERSEDED-4 | 4 | Workflow designer (wave 4) + rules (partial, §13.31) + agents (wave 47) for unstructured steps; RPA screen-scraping bots replaced by APIs/MCP tools |
| SAP CoPilot (legacy digital assistant) | — | — | SUPERSEDED-47 | 47 | — |
| Situation Handling (Situation Framework: standard/extended situation types, situation pages, My Situations) | Proactive alerts with context and actions | — | SUPERSEDED-47 | 47 | Wave 5 alerting + wave 22 exception messages + wave 47 agents triage; "situations" = proactive tasks from events |
| Business AI scenarios: Cash Application (ML matching), Intelligent Invoice Matching / AP, GR/IR reconciliation ML, Predicted delivery delay (sales), Intelligent product proposal, Slow-moving inventory prediction, Demand forecasting ML (IBP/S/4), Maintenance predictive (PAI), Anomaly detection in journals (Financial Statement Insights) | Embedded ML | — | SUPERSEDED-47 | 47 | Agents in suggest-mode (AP coding ≥ 92%, cash application ≥ 90% precision); ML forecasting (wave 22); predictive maintenance hooks (wave 44); anomaly detection on journals |
| Document Information Extraction / Business Document Processing (invoice OCR, PO extraction) | Document AI | — | COVERED-18 | 18 | OCR/AP-automation adapter (wave 18); receipts OCR (wave 43) |
| ISLM (Intelligent Scenario Lifecycle Management: scenarios, models, training, activation) / SAP AI Core / AI Launchpad / generative AI hub (model access, prompt management, orchestration, grounding) | ML/AI lifecycle & model hub | — | SUPERSEDED-47 | 47 | Model-agnostic provider layer (Claude default, Bedrock/Azure/open-weights), prompt/version registry, eval harness in CI, cost/token budgets |
| SAP Signavio Process AI / Process Insights (process mining recommendations) | Process intelligence | — | PARTIAL-46 | 46 | Process mining partial (noted §13.31) |
| Joule in SuccessFactors / Ariba / Concur (HR Q&A, procurement assistant, expense assistant) | Domain copilots | — | SUPERSEDED-47 | 47 | Copilot enabled in all workspaces |
| SAP Conversational AI (chatbots, legacy) | Chatbots | — | SUPERSEDED-47 | 47 | Copilot UI |
| Responsible AI: SAP AI ethics policy, Joule data privacy (no training on customer data), AI governance | AI governance | — | COVERED-47 | 47 | PII redaction, action logging, kill switch, permission inheritance (agent never exceeds user), NIST AI RMF |

### 13.37.2 Reports & inquiries

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| Joule informational queries (Q&A over business data and documentation) | Ask-the-data | SUPERSEDED-47 | 47 | Ask-the-data over semantic layer |
| ISLM model monitoring (accuracy, drift) / AI Launchpad metrics | Model monitoring | SUPERSEDED-47 | 47 | Eval dashboards; cost/token budget alerts |
| BPA process visibility dashboards | Process KPIs | SUPERSEDED-46 | 46 | Workflow dashboards (wave 4/46) |
| Agent action audit (Joule agent logs) | — | COVERED-47 | 47 | Every agent action audited with inputs, tool calls, outputs, approver |
| Situation analytics (situations by type, resolution time) | — | SUPERSEDED-47 | 47 | — |

### 13.37.3 Periodic processing

| T-code | Name | Passage status | Wave | Notes |
|---|---|---|---|---|
| ISLM training/retraining jobs, model activation | — | SUPERSEDED-47 | 47 | Eval runs in CI gate deploys; retraining pipelines (Python services) |
| Cash application ML proposal runs (nightly) | — | SUPERSEDED-47 | 47 | Cash application agent (suggest-mode) |
| Situation determination batch runs | — | SUPERSEDED-47 | 47 | Event-driven |
| RPA bot schedules (BPA) | — | SUPERSEDED-4 | 4 | Job framework + APIs |
| Token/cost budget resets & reports per tenant | — | COVERED-47 | 47 | Cost guard |

### 13.37.4 Customizing (SPRO)

| IMG node / T-code | Table(s) | Key options | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Joule activation per scope item, capability assignment to roles, Joule Studio skill/agent configuration, grounding sources | — | — | SUPERSEDED-47 | 47 | Copilot/agent enablement per workspace & role (OPA); MCP tool catalog |
| Situation types (standard/extended), triggers, conditions, responsibility management (teams & responsibilities), notification channels | — | — | SUPERSEDED-47 | 47 | Alert rules + responsibility assignment via org model (wave 6) |
| ISLM scenario settings, AI Core resource groups, model deployment configs, generative AI hub orchestration (prompt templates, content filtering, data masking) | — | — | SUPERSEDED-47 | 47 | Provider layer config, prompt registry, PII redaction, kill switch |
| BPA: process templates, decision tables (business rules), RPA agent settings, environment/destinations | — | — | SUPERSEDED-4 | 4 | Workflow designer; rules engine (partial) |
| AI governance policies (usage logging, consent, data residency for AI processing) | — | — | COVERED-47 | 47 | Responsible-AI controls; data residency per tenant (wave 48) |
| Business AI scenario activation (per company code / app), confidence thresholds, auto-apply vs propose | — | — | COVERED-47 | 47 | Suggest-mode vs auto with approval gates; precision thresholds gating deploy |

---

## 13.98 Coverage summary by module

Row counts per section (all four tables). Modules with a high PARTIAL/GAP share are where the roadmap needs decisions.

| Section | COVERED | PARTIAL | GAP | OUT-OF-SCOPE | SUPERSEDED | Total |
|---|---|---|---|---|---|---|
| 13.1 FI-GL — General Ledger (classic GL, New GL, ledgers, ACDOCA) | 123 | 13 | 9 | 0 | 19 | 164 |
| 13.2 FI-AP — Accounts Payable | 91 | 11 | 5 | 0 | 5 | 112 |
| 13.3 FI-AR — Accounts Receivable | 96 | 7 | 5 | 0 | 4 | 112 |
| 13.4 FI-AA — Asset Accounting | 89 | 5 | 0 | 3 | 1 | 98 |
| 13.5 FI-BL / FSCM-CM / BAM — Bank Accounting, Cash Management & Bank Account Management | 48 | 7 | 6 | 0 | 1 | 62 |
| 13.6 FSCM — Credit, Collections, Dispute Management & Treasury (TRM) | 23 | 3 | 14 | 2 | 3 | 45 |
| 13.7 CO-OM — Overhead Cost Controlling (cost centers, internal orders, activity types, allocations) | 99 | 3 | 0 | 0 | 8 | 110 |
| 13.8 CO-PC — Product Cost Controlling (product cost planning, cost object controlling, material ledger) | 67 | 13 | 0 | 0 | 5 | 85 |
| 13.9 CO-PA — Profitability Analysis (costing-based, account-based) and EC-PCA Profit Center Accounting | 27 | 7 | 0 | 0 | 22 | 56 |
| 13.10 EC-CS / SEM-BCS / S/4HANA Group Reporting — Consolidation | 29 | 7 | 1 | 0 | 4 | 41 |
| 13.11 FM / PSM — Funds Management & Public Sector Management (budgeting) | 18 | 7 | 0 | 4 | 5 | 34 |
| 13.12 MM-PUR — Purchasing | 95 | 16 | 0 | 1 | 5 | 117 |
| 13.13 MM-IM — Inventory Management & Physical Inventory | 80 | 13 | 2 | 0 | 9 | 104 |
| 13.14 MM-IV — Logistics Invoice Verification | 49 | 7 | 1 | 0 | 1 | 58 |
| 13.15 MM-SRV — External Services Management (incl. S/4 Lean Services) | 2 | 22 | 2 | 1 | 0 | 27 |
| 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | 78 | 9 | 7 | 0 | 11 | 105 |
| 13.17 LE-SHP / LE-TRA — Shipping & (classic) Transportation | 69 | 4 | 3 | 0 | 3 | 79 |
| 13.18 TM — Transportation Management (SAP TM / embedded TM in S/4) | 31 | 3 | 3 | 1 | 2 | 40 |
| 13.19 SD — Sales & Distribution (pricing, orders, deliveries, billing, credit) | 117 | 10 | 2 | 3 | 13 | 145 |
| 13.20 SD Rebates / Settlement Management (condition contracts) | 22 | 6 | 3 | 1 | 1 | 33 |
| 13.21 PP — Production Planning (master data, MRP, SOP/demand management, production orders, capacity, KANBAN, REM) | 174 | 14 | 2 | 0 | 6 | 196 |
| 13.22 PP-PI — Production Planning for Process Industries | 26 | 18 | 5 | 0 | 0 | 49 |
| 13.23 PP/DS — Advanced Planning & Detailed Scheduling (APO PP/DS and S/4 embedded PP/DS) | 29 | 4 | 2 | 0 | 8 | 43 |
| 13.24 QM — Quality Management | 70 | 10 | 5 | 0 | 0 | 85 |
| 13.25 PM — Plant Maintenance (Enterprise Asset Management) | 58 | 6 | 0 | 1 | 2 | 67 |
| 13.26 CS / Customer Service / SAP Field Service Management | 42 | 5 | 2 | 0 | 0 | 49 |
| 13.27 PS — Project System | 64 | 12 | 3 | 1 | 3 | 83 |
| 13.28 HCM — PA / OM / PT / PY + SuccessFactors Employee Central | 66 | 16 | 0 | 0 | 7 | 89 |
| 13.29 FI-TV — Travel Management / SAP Concur | 25 | 3 | 0 | 2 | 2 | 32 |
| 13.30 Basis & Security (users, roles, GRC Access Control, change documents, transports, system administration) | 51 | 4 | 0 | 1 | 39 | 95 |
| 13.31 Cross-Application (workflow, IDoc/ALE, PI/PO, DRC e-documents, output management NAST/BRF+, Fiori launchpad, archiving/ILM, GDPR/IRF, attachments, texts, enterprise search) | 57 | 6 | 2 | 1 | 25 | 91 |
| 13.32 LO-VC — Variant Configuration & AVC (Advanced Variant Configuration) | 30 | 9 | 0 | 0 | 4 | 43 |
| 13.33 Batch Management & Serial Numbers | 44 | 9 | 0 | 0 | 1 | 54 |
| 13.34 CA-CL — Classification System | 24 | 0 | 0 | 0 | 5 | 29 |
| 13.35 DMS — Document Management System (CA-DMS), Easy DMS, ECTR, CAD integration | 8 | 18 | 7 | 0 | 1 | 34 |
| 13.36 Analytics — BW/4HANA, CDS views & embedded analytics, SAP Analytics Cloud, LIS, Report Painter, SAP Query | 22 | 2 | 0 | 0 | 28 | 52 |
| 13.37 AI — SAP Joule, SAP Build Process Automation, Business AI (SUPERSEDED by wave 47) | 6 | 1 | 0 | 0 | 21 | 28 |
| **All** | **2049** | **310** | **91** | **22** | **274** | **2746** |

## 13.99 Gap register

Every row above whose status is `GAP` or `PARTIAL-<wave>`, in document order, with the wave it is currently mapped to (PARTIAL) or the *proposed* wave (GAP). The "What is missing / proposal" column repeats the row's Notes so this register can be read on its own and pasted into the wave issues. Proposed waves are suggestions for the roadmap owners; a GAP stays a GAP until a wave's `deliverable` in waves.json names it.

### 13.99.1 Rollup by (current or proposed) wave

| Wave | PARTIAL rows mapped | GAP rows proposed | Total |
|---|---|---|---|
| 3 | 3 | 0 | 3 |
| 5 | 5 | 0 | 5 |
| 7 | 6 | 0 | 6 |
| 8 | 9 | 0 | 9 |
| 9 | 10 | 6 | 16 |
| 11 | 17 | 0 | 17 |
| 12 | 3 | 0 | 3 |
| 13 | 4 | 0 | 4 |
| 14 | 0 | 5 | 5 |
| 16 | 17 | 1 | 18 |
| 17 | 18 | 2 | 20 |
| 18 | 16 | 1 | 17 |
| 19 | 5 | 2 | 7 |
| 20 | 13 | 0 | 13 |
| 21 | 8 | 0 | 8 |
| 22 | 13 | 2 | 15 |
| 23 | 6 | 2 | 8 |
| 24 | 9 | 6 | 15 |
| 25 | 11 | 0 | 11 |
| 26 | 7 | 1 | 8 |
| 27 | 11 | 1 | 12 |
| 28 | 1 | 1 | 2 |
| 29 | 1 | 1 | 2 |
| 30 | 7 | 5 | 12 |
| 31 | 8 | 3 | 11 |
| 32 | 1 | 2 | 3 |
| 33 | 10 | 5 | 15 |
| 34 | 3 | 7 | 10 |
| 35 | 1 | 0 | 1 |
| 36 | 10 | 29 | 39 |
| 37 | 9 | 0 | 9 |
| 38 | 9 | 0 | 9 |
| 39 | 6 | 0 | 6 |
| 40 | 10 | 0 | 10 |
| 41 | 8 | 3 | 11 |
| 42 | 10 | 0 | 10 |
| 43 | 7 | 0 | 7 |
| 44 | 6 | 0 | 6 |
| 45 | 4 | 2 | 6 |
| 46 | 3 | 0 | 3 |
| 48 | 5 | 4 | 9 |
| **All** | **310** | **91** | **401** |

### 13.99.2 Register (document order)

| # | Section | Table | T-code / IMG node | Name | Status | Wave (current / proposed) | What is missing / proposal |
|---|---|---|---|---|---|---|---|
| 1 | 13.1 FI-GL | Master data & documents | F-03 / F-04 | Clear G/L account / post with clearing | `PARTIAL-11` | 11 | Open-item management specified only for AP/AR (waves 19/30) and GR/IR (wave 18); generic OI-managed G/L accounts (clearing accounts, bank sub-accounts) need the same clearing engine exposed on GL |
| 2 | 13.1 FI-GL | Master data & documents | FBCJ | Cash journal | `GAP` | propose 36 | No wave mentions a petty-cash/cash-journal document; small but common on plant floors — add to wave 36 (cash & bank) as "cash journal with receipts, business transactions and daily closing" |
| 3 | 13.1 FI-GL | Master data & documents | FBCJC0 / FBCJC1 / FBCJC2 / FBCJC3 | Cash journal setup, number ranges, business transactions, print | `GAP` | propose 36 | Follows FBCJ |
| 4 | 13.1 FI-GL | Master data & documents | FAGL_ACTIVATE_OP | Activate open-item management on G/L accounts | `PARTIAL-11` | 11 | Depends on the GL OI gap above |
| 5 | 13.1 FI-GL | Master data & documents | FAGL_SWITCH_TO_OPEN_ITEM | Switch to OI management (S/4) | `PARTIAL-11` | 11 | Same |
| 6 | 13.1 FI-GL | Reports & inquiries | S_ALR_87012309 | Cash book / cash journal report | `GAP` | propose 36 | Follows FBCJ gap |
| 7 | 13.1 FI-GL | Periodic processing | F.13 / F13E | Automatic clearing (with/without specification of clearing currency) | `PARTIAL-11` | 11 | Automatic clearing specified for GR/IR (wave 18) and AP/AR settlement; generic rule-based GL auto-clearing is part of the GL OI gap |
| 8 | 13.1 FI-GL | Periodic processing | FAGLF101 / F101 | Regroup receivables/payables (sorted list, reclassification by remaining term) | `GAP` | propose 14 | Balance-sheet reclassification (short/long-term, debit-balance vendors, credit-balance customers) is a standard close step not named in wave 14's checklist/allocations — add as a close task type |
| 9 | 13.1 FI-GL | Periodic processing | F.50 / F.52 | G/L interest calculation (balance / account interest) | `GAP` | propose 30 | Interest on GL balances (e.g. intercompany loans) is not named; wave 30 covers AR interest notes only — extend the interest engine to GL/intercompany accounts |
| 10 | 13.1 FI-GL | Periodic processing | FAGL_CL_REGROUP / FAGL_CL_MIGRATION | Closing: regrouping (S/4) | `GAP` | propose 14 | Same as FAGLF101 |
| 11 | 13.1 FI-GL | Periodic processing | F.97 / F.98 / F.99 | Balance confirmations for G/L / AP / AR | `GAP` | propose 19 | Balance confirmation letters (vendor/customer/GL) not named in waves 19 or 30 — add to wave 30 customer statements + wave 19 vendor confirmations |
| 12 | 13.1 FI-GL | Customizing (SPRO) | OB28 / GGB0 — Validations (FI) | GB90*, T001Q | `PARTIAL-11` | 11 | Wave 11 has dimension validation rules; a general user-defined validation rule language (Boolean rules over header/line fields with message classes) is only implied by wave 3 "validation" — scope it explicitly |
| 13 | 13.1 FI-GL | Customizing (SPRO) | OBBH / GGB1 — Substitutions (FI) | GB91*, T001R | `PARTIAL-11` | 11 | Default dimensions on master data exist; rule-based substitution (derive dimension from another field) needs an explicit rule engine on the Post API |
| 14 | 13.1 FI-GL | Customizing (SPRO) | OBBZ / OBBW — Substitution/validation for cost of sales accounting | — | `PARTIAL-11` | 11 | Functional area as a dimension with derivation rules (see above) |
| 15 | 13.1 FI-GL | Customizing (SPRO) | Document splitting: classify G/L accounts, document types, splitting method, zero-balance, constants (FAGL_SPLIT*) | T8G01…T8G40 | `PARTIAL-11` | 11 | Wave 11 requires every line to carry dimensions, so dimension-complete documents are native; but *inheritance to balance-sheet lines* (AP/AR/tax lines split by the expense lines' profit centers) and zero-balance clearing per dimension are not named — needed for balance sheet by profit center/segment |
| 16 | 13.1 FI-GL | Customizing (SPRO) | OBXR / OBYR — Special G/L indicators (customers / vendors) | T074 | `PARTIAL-19` | 19 | Prepayments/down payments covered (waves 19/30); bills of exchange and noted items not named — see FI-AP/AR sections |
| 17 | 13.1 FI-GL | Customizing (SPRO) | OBB8 / OBB9 — Terms of payment / installment terms | T052, T052R | `PARTIAL-7` | 7 | Payment terms covered; installment terms (split one invoice into several due dates) not named — add to wave 7 terms model |
| 18 | 13.1 FI-GL | Customizing (SPRO) | OBA0 — Tolerance groups for G/L accounts (clearing) | T043S | `PARTIAL-11` | 11 | GL OI gap |
| 19 | 13.1 FI-GL | Customizing (SPRO) | OB46 — Interest indicators / OB82 / OBAA | T056* | `GAP` | propose 30 | Interest engine (see F.52) |
| 20 | 13.1 FI-GL | Customizing (SPRO) | OB74 — Additional rules for automatic clearing | TF123 | `PARTIAL-11` | 11 | GL OI gap (AP/AR criteria covered by settlement rules) |
| 21 | 13.1 FI-GL | Customizing (SPRO) | FAGL_CL_RGRP / Define sort method & adjustment accounts for regrouping | T043/ T030 | `GAP` | propose 14 | FAGLF101 gap |
| 22 | 13.1 FI-GL | Customizing (SPRO) | Cost of sales accounting: functional areas (OKBD), activate (FAGL_FUNC_AREA) | TFKB, T001 | `PARTIAL-11` | 11 | Functional area dimension + derivation rules (substitution gap) |
| 23 | 13.2 FI-AP | Master data & documents | One-time vendor (account group CPD) | One-time accounts with address on document | `PARTIAL-7` | 7 | Not named in wave 7; add "one-time party" role with document-level address capture |
| 24 | 13.2 FI-AP | Master data & documents | F-40 / F-36 (AP side: FBW6) | Bill of exchange payment (AP) | `GAP` | propose 36 | Bills of exchange (AP and AR) are not in any wave; relevant in IT/ES/FR/JP/BR; propose as a wave 36 (bank) option or defer to a localization v1.x pack under wave 48 — needs a decision |
| 25 | 13.2 FI-AP | Master data & documents | J1INCHLN / J1INBANK / J1INCERT (IN WHT) | Withholding tax challan / bank challan / certificate (India) | `PARTIAL-13` | 13 | Withholding engine + 26AS hooks named; Indian TDS challan/certificate flows belong to the IN country pack (wave 48) — partial until pack ships |
| 26 | 13.2 FI-AP | Master data & documents | FIBLFFP / F111 | Payment request / payment program for payment requests | `PARTIAL-36` | 36 | Payment requests from treasury (not vendor items) not named; add to wave 36 in-house banking |
| 27 | 13.2 FI-AP | Reports & inquiries | S_ALR_87012116 | Bill of exchange list | `GAP` | propose 36 | Follows bills gap |
| 28 | 13.2 FI-AP | Periodic processing | F111 | Payment program for payment requests | `PARTIAL-36` | 36 | See payment requests |
| 29 | 13.2 FI-AP | Periodic processing | F150 (AP dunning) | Dunning vendors (debit balances) | `PARTIAL-30` | 30 | Dunning covers customers; dunning vendors with debit balances is an edge case — reuse engine |
| 30 | 13.2 FI-AP | Periodic processing | FAGLF101 (AP) | Regroup vendors with debit balances | `GAP` | propose 14 | Reclassification gap |
| 31 | 13.2 FI-AP | Periodic processing | F.98 | Vendor balance confirmation | `GAP` | propose 19 | Balance confirmations gap |
| 32 | 13.2 FI-AP | Periodic processing | FBWE (AP bill of exchange presentation) | Bill of exchange presentation | `GAP` | propose 36 | Bills gap |
| 33 | 13.2 FI-AP | Periodic processing | F.64 (AP correspondence) | Correspondence: maintain / print | `PARTIAL-19` | 19 | Correspondence types (account statement, payment notice) only partially named (payment advice) — define correspondence framework (templates + triggers) on wave 46 document designer |
| 34 | 13.2 FI-AP | Customizing (SPRO) | OBYR — Special G/L indicators vendors (down payments, guarantees, BoE) | T074 | `PARTIAL-19` | 19 | Down payments yes; BoE no |
| 35 | 13.2 FI-AP | Customizing (SPRO) | OBB9 — Installment terms | T052R | `PARTIAL-7` | 7 | Installment gap |
| 36 | 13.2 FI-AP | Customizing (SPRO) | Define correspondence types / forms (OB77, OB78, OB79) | T048, T048B | `PARTIAL-19` | 19 | Correspondence framework gap |
| 37 | 13.2 FI-AP | Customizing (SPRO) | OBD3 "one-time account" | T077K | `PARTIAL-7` | 7 | One-time party gap |
| 38 | 13.2 FI-AP | Customizing (SPRO) | Bank-to-bank transfers (payment request types, FIBL) | — | `PARTIAL-36` | 36 | Payment requests gap |
| 39 | 13.3 FI-AR | Master data & documents | F-34 / F-36 / F-33 | Bill of exchange payment / discount (AR) | `GAP` | propose 36 | Bills of exchange gap |
| 40 | 13.3 FI-AR | Master data & documents | FB12 / F.61 / F.64 | Request / print correspondence | `PARTIAL-30` | 30 | Statements and dunning named; general correspondence framework (payment notices, individual letters, open item lists) only partial |
| 41 | 13.3 FI-AR | Reports & inquiries | S_ALR_87012199 | Customer: bill of exchange list | `GAP` | propose 36 | Bills gap |
| 42 | 13.3 FI-AR | Periodic processing | FAGLF101 (AR) | Regroup customers with credit balances | `GAP` | propose 14 | Reclassification gap |
| 43 | 13.3 FI-AR | Periodic processing | F.99 | Customer balance confirmation | `GAP` | propose 30 | Balance confirmation gap |
| 44 | 13.3 FI-AR | Periodic processing | SEPA mandate management (FSEPA_M1..M4) | Mandates | `PARTIAL-30` | 30 | SEPA direct-debit mandates not named; required for EU customers paying by direct debit — add mandate entity to wave 30 |
| 45 | 13.3 FI-AR | Periodic processing | FBWD / FBWE (AR BoE) | Bill of exchange presentation | `GAP` | propose 36 | Bills gap |
| 46 | 13.3 FI-AR | Customizing (SPRO) | OBXR — Special G/L customers (down payments, security deposits, BoE) | T074 | `PARTIAL-30` | 30 | BoE gap |
| 47 | 13.3 FI-AR | Customizing (SPRO) | OBB9 — Installment terms (customers) | T052R | `PARTIAL-7` | 7 | Installment gap |
| 48 | 13.3 FI-AR | Customizing (SPRO) | Define correspondence types (OB77) / periodic account statements | T048 | `PARTIAL-30` | 30 | Correspondence framework gap |
| 49 | 13.3 FI-AR | Customizing (SPRO) | SEPA mandate customizing | SEPA_* | `PARTIAL-30` | 30 | Mandate gap |
| 50 | 13.3 FI-AR | Customizing (SPRO) | Define alternate payer / head office–branch | KNA1-KNRZE, KNB1 | `PARTIAL-7` | 7 | Head-office/branch (invoice to branch, pay from HQ) not named in wave 7 party roles — add "bill-to/pay-from parent" relationship |
| 51 | 13.4 FI-AA | Master data & documents | AS21 / AS22 / AS23 | Group asset create/change/display | `PARTIAL-37` | 37 | Group assets (depreciate at group level, e.g. ADR) not named in wave 37 — add to tax book features |
| 52 | 13.4 FI-AA | Master data & documents | Insurance values (asset master tab, S_ALR_87011951) | Insurance values / indexing | `PARTIAL-37` | 37 | Insurance values and index series not named — add to asset master |
| 53 | 13.4 FI-AA | Reports & inquiries | S_ALR_87010127 / 87010129 (US: form 4562 / mid-quarter) | Tax depreciation reports | `PARTIAL-37` | 37 | MACRS tables yes; IRS Form 4562 layout belongs to the US pack (wave 48) |
| 54 | 13.4 FI-AA | Customizing (SPRO) | OAYM / OAAX — Group assets per area | T093A | `PARTIAL-37` | 37 | Group asset gap |
| 55 | 13.4 FI-AA | Customizing (SPRO) | OAVI / OAVF — Insurance types, companies, index series | T086, T094 | `PARTIAL-37` | 37 | Insurance gap |
| 56 | 13.5 FI-BL / FSCM-CM / BAM | Master data & documents | FBCJ | Cash journal | `GAP` | propose 36 | Cash journal gap (see FI-GL) |
| 57 | 13.5 FI-BL / FSCM-CM / BAM | Master data & documents | FRFT / FRFT_B | Repetitive payment codes / bank-to-bank transfer | `PARTIAL-36` | 36 | Bank-to-bank transfer / payment request gap |
| 58 | 13.5 FI-BL / FSCM-CM / BAM | Master data & documents | IHC0 / IHC1IP (In-House Cash) | In-house cash center, IHC accounts, internal payment orders | `PARTIAL-36` | 36 | "In-house banking basics: netting, cash pooling" named; full IHC (internal accounts, interest, statements for subsidiaries) is partial |
| 59 | 13.5 FI-BL / FSCM-CM / BAM | Master data & documents | Fiori: Approve Bank Payments / Monitor Payments (Advanced Payment Management) | APM | `PARTIAL-36` | 36 | Payment approval covered; APM-style central payment hub (route, batch, reformat across entities) only partially (adapters) |
| 60 | 13.5 FI-BL / FSCM-CM / BAM | Master data & documents | FBWE / FBWD (bills) | Bill of exchange presentation | `GAP` | propose 36 | Bills gap |
| 61 | 13.5 FI-BL / FSCM-CM / BAM | Reports & inquiries | S_ALR_87012309 (cash journal) | Cash journal report | `GAP` | propose 36 | — |
| 62 | 13.5 FI-BL / FSCM-CM / BAM | Reports & inquiries | Fiori: Bank Relationship Overview / Bank Fee Analysis | Bank fees (camt.086) | `GAP` | propose 36 | Bank fee analysis (camt.086) not named; propose as wave 36 report |
| 63 | 13.5 FI-BL / FSCM-CM / BAM | Periodic processing | Cash pooling / sweeping run (IHC) | — | `PARTIAL-36` | 36 | Basics named |
| 64 | 13.5 FI-BL / FSCM-CM / BAM | Periodic processing | F111 (treasury payment requests) | — | `PARTIAL-36` | 36 | — |
| 65 | 13.5 FI-BL / FSCM-CM / BAM | Periodic processing | Bank fee statement import (camt.086) | — | `GAP` | propose 36 | — |
| 66 | 13.5 FI-BL / FSCM-CM / BAM | Customizing (SPRO) | Bank chains (FIBC) | TBCHAIN* | `PARTIAL-36` | 36 | Correspondent/intermediary bank chains not named — add to bank connectivity |
| 67 | 13.5 FI-BL / FSCM-CM / BAM | Customizing (SPRO) | IHC customizing: IHC centers, accounts, conditions, payment orders | IHC_* | `PARTIAL-36` | 36 | IHC partial |
| 68 | 13.5 FI-BL / FSCM-CM / BAM | Customizing (SPRO) | Cash journal customizing (FBCJC0..3) | TCJ_* | `GAP` | propose 36 | — |
| 69 | 13.6 FSCM | Master data & documents | FTR_CREATE / FTR_EDIT / FTR_00 | Treasury transaction manager: create financial transaction (money market, FX forward/spot/swap, derivatives, securities, loans) | `GAP` | propose 36 | TRM transaction manager is not in any wave. Manufacturers with FX exposure need at least FX spot/forward deal capture, settlement and valuation; propose a wave 36 "treasury deals (FX, loans, deposits)" extension, with derivatives/securities deferred to v2 |
| 70 | 13.6 FSCM | Master data & documents | TBB1 / TPM10 | Post flows / treasury posting | `GAP` | propose 36 | Follows TRM gap |
| 71 | 13.6 FSCM | Master data & documents | TPM1 / TPM60 | Valuation (key date), amortization, position management | `GAP` | propose 36 | — |
| 72 | 13.6 FSCM | Master data & documents | THMEX (Hedge management & accounting, IFRS 9) | Hedge relationships, effectiveness, hedge accounting postings | `GAP` | propose 36 | Hedge accounting per IFRS 9/ASC 815 — v2 candidate; register as gap |
| 73 | 13.6 FSCM | Master data & documents | Exposure management (raw exposures, TEM) | Exposure capture from AP/AR/forecast | `GAP` | propose 36 | FX exposure from open items is derivable from waves 12/36 data; deal side missing |
| 74 | 13.6 FSCM | Master data & documents | TBDM market data import (rates, prices) | Market data management | `PARTIAL-12` | 12 | Exchange-rate providers covered; interest curves/security prices not |
| 75 | 13.6 FSCM | Master data & documents | Loans management (FN*, CML) | Loan contracts | `GAP` | propose 36 | Intercompany and bank loans: schedules + interest; propose minimal loan contract in wave 36 |
| 76 | 13.6 FSCM | Reports & inquiries | TPM12 / TPM13 | Position list / flow list (TRM) | `GAP` | propose 36 | TRM gap |
| 77 | 13.6 FSCM | Reports & inquiries | TPM20 | Posting journal (TRM) | `GAP` | propose 36 | TRM gap |
| 78 | 13.6 FSCM | Reports & inquiries | Hedge plan reports | — | `GAP` | propose 36 | — |
| 79 | 13.6 FSCM | Reports & inquiries | Treasury information system (TRM) | — | `GAP` | propose 36 | — |
| 80 | 13.6 FSCM | Periodic processing | TBB1 (periodic) / TPM1 valuation run / accruals | TRM period-end | `GAP` | propose 36 | TRM gap |
| 81 | 13.6 FSCM | Periodic processing | Hedge effectiveness test run | — | `GAP` | propose 36 | — |
| 82 | 13.6 FSCM | Periodic processing | TBDM import run | Market data import | `PARTIAL-12` | 12 | — |
| 83 | 13.6 FSCM | Customizing (SPRO) | TRM: product types, transaction types, flow types, update types, valuation areas/classes, accounting codes, position management procedures | TRL*, TRD*, TPM* | `GAP` | propose 36 | TRM gap |
| 84 | 13.6 FSCM | Customizing (SPRO) | Hedge management: hedging profiles, exposure categories | THM* | `GAP` | propose 36 | — |
| 85 | 13.6 FSCM | Customizing (SPRO) | Market data: rate types, yield curves, volatility | VTB*, TCURR | `PARTIAL-12` | 12 | — |
| 86 | 13.7 CO-OM | Periodic processing | CPTA / CPTB | ABC: template allocation (actual/plan) | `PARTIAL-38` | 38 | ABC named; template (driver-formula) allocation is partial — specify driver formulas |
| 87 | 13.7 CO-OM | Customizing (SPRO) | CO versions for parallel valuation (group/profit-center valuation, transfer prices) | TKA07, TKA09, TKA01 | `PARTIAL-40` | 40 | Transfer-pricing documentation hooks named; parallel group/profit-center valuation views (three valuation views in CO/ML) are not — register as partial for consolidation of profit in inventory |
| 88 | 13.7 CO-OM | Customizing (SPRO) | ABC: template allocation environments, functions (CPT1) | — | `PARTIAL-38` | 38 | — |
| 89 | 13.8 CO-PC | Master data & documents | CKECP / CKECP1 | Easy cost planning / ad-hoc cost estimate | `PARTIAL-39` | 39 | What-if simulations named; template-based "easy" cost planning for non-material objects (projects/orders) — partial, map to wave 41 project estimates |
| 90 | 13.8 CO-PC | Master data & documents | CK51N / CK53N | Sales order cost estimate create / display | `PARTIAL-27` | 27 | Order-specific costing for configured/MTO products not explicitly named in waves 27/39 — add "sales order cost estimate" to wave 39 simulations |
| 91 | 13.8 CO-PC | Master data & documents | CK91N / CK92 | Procurement alternatives / mixing ratios (mixed costing) | `PARTIAL-39` | 39 | Mixed costing (weighted sources: make vs buy, multiple vendors/production versions) not named — add to wave 39 fallback rules |
| 92 | 13.8 CO-PC | Master data & documents | KKPHIE / KKP2 | Cost object hierarchy | `PARTIAL-38` | 38 | Cost object hierarchies for process cost distribution — partial |
| 93 | 13.8 CO-PC | Periodic processing | CKMLCPAVR (Alternative valuation run) | Alternative valuation run (cumulative periods) | `PARTIAL-20` | 20 | Multi-period cumulative actual costing (AVR) not named — partial |
| 94 | 13.8 CO-PC | Periodic processing | CKMDUVACT / CKMDUVMAT | Distribution of usage variances | `PARTIAL-20` | 20 | Usage-variance distribution to consumers — partial |
| 95 | 13.8 CO-PC | Periodic processing | WIP at actual cost (ML) | — | `PARTIAL-20` | 20 | WIP revaluation at actual cost — partial |
| 96 | 13.8 CO-PC | Customizing (SPRO) | Mixed costing: quantity structure types, procurement alternatives (OMXL) | CKMLMV* | `PARTIAL-39` | 39 | Mixed costing gap |
| 97 | 13.8 CO-PC | Customizing (SPRO) | Easy cost planning: costing models, characteristics | CKF* | `PARTIAL-39` | 39 | — |
| 98 | 13.8 CO-PC | Customizing (SPRO) | Sales order costing: requirements class settings (OVZG) valuation/costing | T459K | `PARTIAL-27` | 27 | Sales-order stock valuation (valuated MTO stock) belongs to wave 16/21 — not named; partial |
| 99 | 13.8 CO-PC | Customizing (SPRO) | Transfer prices / multiple valuation views (OKKP, OKEQ group) | TKA01 | `PARTIAL-40` | 40 | See CO-OM |
| 100 | 13.8 CO-PC | Customizing (SPRO) | Cost of goods sold split by cost component (S/4 COGS splitting) | FINSC_COGS* | `PARTIAL-39` | 39 | COGS posting by cost component (material/labor/overhead accounts) not named in wave 28/39 — add posting profile option |
| 101 | 13.8 CO-PC | Customizing (SPRO) | Price-difference splitting (S/4) | — | `PARTIAL-39` | 39 | Same |
| 102 | 13.9 CO-PA | Master data & documents | KEDR / KEDE | Derivation strategy / rules (derive characteristics) | `PARTIAL-11` | 11 | Default dimensions on master data exist; rule-based derivation tables (lookup A×B→C with validity) = substitution gap (see FI-GL) |
| 103 | 13.9 CO-PA | Periodic processing | KE28 | Top-down distribution (actual) | `PARTIAL-38` | 38 | Top-down distribution of aggregated values (e.g. freight by revenue share) to detailed segments not named — add as allocation type "top-down" |
| 104 | 13.9 CO-PA | Periodic processing | KE27 | Periodic valuation (revaluate with actual costs from ML) | `PARTIAL-20` | 20 | Revaluation of COGS in profitability with actual cost — tied to ML revaluation of consumption partial |
| 105 | 13.9 CO-PA | Periodic processing | KEND / KEDR realignment run | Realignment (re-derive characteristics on history) | `PARTIAL-11` | 11 | Mass re-derivation of dimensions on posted lines is a controlled restatement; propose explicit "dimension realignment" tool (audit-logged) in wave 11/15 |
| 106 | 13.9 CO-PA | Customizing (SPRO) | KEDR — Derivation rules (table lookup, derivation rule, move, clear, enhancement) | TKEDR*, K9R* | `PARTIAL-11` | 11 | Derivation gap |
| 107 | 13.9 CO-PA | Customizing (SPRO) | 0KEM / 0KEP — PCA substitution rules / derivation of PC from SD (sales org, material), MM | T8A* | `PARTIAL-11` | 11 | Derivation gap |
| 108 | 13.9 CO-PA | Customizing (SPRO) | 8KEM / 8KEN — Transfer prices: pricing procedures, conditions, transfer price variants | T8PT* | `PARTIAL-40` | 40 | Transfer pricing documentation hooks only; PC valuation view not covered |
| 109 | 13.10 EC-CS / SEM-BCS / S/4HANA Group Reporting | Master data & documents | Fiori: Manage Reclassifications / Define Reclassification Rules | Rule-based reclassifications | `PARTIAL-40` | 40 | Elimination rules named; general reclassification rules (e.g. move FS item based on sign) partial |
| 110 | 13.10 EC-CS / SEM-BCS / S/4HANA Group Reporting | Master data & documents | Fiori: Consolidation of Investments (activity-based: first consolidation, subsequent, step acquisition, divestiture; equity method) | CoI | `PARTIAL-40` | 40 | Minority interest named; purchase-method CoI with goodwill, step acquisitions, divestitures and equity-method accounting are not — partial |
| 111 | 13.10 EC-CS / SEM-BCS / S/4HANA Group Reporting | Master data & documents | Fiori: Manage Investments / Equity data | Investment and equity holdings | `PARTIAL-40` | 40 | — |
| 112 | 13.10 EC-CS / SEM-BCS / S/4HANA Group Reporting | Master data & documents | Disclosure management / XBRL (SAP DM, SAC) | Statutory filing packages | `GAP` | propose 48 | XBRL/ESEF filing output not named; propose electronic reporting designer (wave 48) output type |
| 113 | 13.10 EC-CS / SEM-BCS / S/4HANA Group Reporting | Periodic processing | Consolidation monitor tasks: IC elimination (AR/AP, revenue/expense, dividends), elimination of IC profit in inventory, CoI, group validation | Group-level tasks | `PARTIAL-40` | 40 | IC balances and inventory profit named; dividend elimination and CoI partial |
| 114 | 13.10 EC-CS / SEM-BCS / S/4HANA Group Reporting | Periodic processing | Reclassification runs | — | `PARTIAL-40` | 40 | — |
| 115 | 13.10 EC-CS / SEM-BCS / S/4HANA Group Reporting | Customizing (SPRO) | Reclassification methods / elimination methods (IC elimination of AR/AP, revenue/expense, dividends) | TF* | `PARTIAL-40` | 40 | — |
| 116 | 13.10 EC-CS / SEM-BCS / S/4HANA Group Reporting | Customizing (SPRO) | CoI methods (purchase, equity, proportional), activities | TF* | `PARTIAL-40` | 40 | — |
| 117 | 13.11 FM / PSM | Master data & documents | FM5I / FM5S | Fund create/display | `PARTIAL-38` | 38 | "Fund" dimension (restricted/unrestricted funding) not named; grants/projects usually carry it — partial; usable via a financial dimension |
| 118 | 13.11 FM / PSM | Master data & documents | FMMEASURE (funded program) | Funded program | `PARTIAL-38` | 38 | Same |
| 119 | 13.11 FM / PSM | Master data & documents | FMX1 / FMX2 / FMX3 | Earmarked funds: funds reservation create/change/display | `PARTIAL-38` | 38 | Pre-encumbrance from requisition is named; manual "funds reservation" without a requisition is not — add budget reservation document |
| 120 | 13.11 FM / PSM | Master data & documents | FMY1 / FMZ1 / FMW1 / FMV1 | Funds precommitment / commitment / forecast of revenue / funds blocking | `PARTIAL-38` | 38 | Same |
| 121 | 13.11 FM / PSM | Master data & documents | FMDERIVE | Derivation of FM account assignment | `PARTIAL-11` | 11 | Derivation gap |
| 122 | 13.11 FM / PSM | Customizing (SPRO) | FMDERIVE strategy | FMDERIVE* | `PARTIAL-11` | 11 | — |
| 123 | 13.11 FM / PSM | Customizing (SPRO) | Earmarked funds document types, number ranges | FMEF* | `PARTIAL-38` | 38 | — |
| 124 | 13.12 MM-PUR | Master data & documents | ME84 / ME85 / ME86 | Create SA releases (FRC/JIT) / renumber / display release documentation | `PARTIAL-17` | 17 | Vendor-facing forecast/JIT release generation (cumulative quantities, release documentation) is a supplier-schedule capability not explicitly named in wave 17; EDI DELFOR/DELJIT in wave 35 lists 850/855/856/810/820/214 only — partial |
| 125 | 13.12 MM-PUR | Master data & documents | MEQ1 / MEQ3 / MEQ4 / MEQ6 / MEQ8 | Quota arrangement maintain/display/changes/analyze | `PARTIAL-22` | 22 | Quota arrangements (percentage split across vendors/own production for MRP) not named in wave 17/22 — partial |
| 126 | 13.12 MM-PUR | Master data & documents | ME21N item category K (consignment) | Vendor consignment PO | `PARTIAL-16` | 16 | Consignment stock (vendor-owned at our site, settle on consumption MRKO) not named in waves 16/18 — partial; needs ownership attribute on inventory + settlement run |
| 127 | 13.12 MM-PUR | Master data & documents | ME21N item category U (stock transfer) / UB STO / NB cross-company STO | Stock transport orders (intra/intercompany) with delivery | `PARTIAL-16` | 16 | Wave 16 "transfer journals" and wave 40 intercompany trade cover the postings; an explicit *transfer order document* with delivery, in-transit stock and receiving (plant-to-plant with shipping) is not named — partial; propose "transfer orders with in-transit status" in wave 16/28 |
| 128 | 13.12 MM-PUR | Master data & documents | ME21N service item (D) | Services PO | `PARTIAL-18` | 18 | See MM-SRV |
| 129 | 13.12 MM-PUR | Master data & documents | Fiori: Create Purchase Requisition (self-service, catalog, free text) | Self-service requisitioning with catalogs (OCI punch-out) | `PARTIAL-17` | 17 | Requisition yes; catalog/punch-out (OCI/cXML) item selection not named — partial; propose wave 20 (supplier collaboration) or wave 35 (integration) |
| 130 | 13.12 MM-PUR | Master data & documents | SAP Ariba Buying / Sourcing / Contracts / SLP (supplier lifecycle) | Cloud procurement suite | `PARTIAL-20` | 20 | Supplier portal (ack, ASN, invoice, docs) covers Ariba Network basics; strategic sourcing events (auctions, weighted RFP scoring), supplier onboarding/qualification questionnaires and contract authoring are not — partial; propose v1.x |
| 131 | 13.12 MM-PUR | Reports & inquiries | MSRV1 / MSRV3 (services) | — | `PARTIAL-18` | 18 | See MM-SRV |
| 132 | 13.12 MM-PUR | Reports & inquiries | Fiori: Monitor Supplier Confirmations / Monitor Scheduling Agreement Releases | — | `PARTIAL-17` | 17 | SA release documentation partial |
| 133 | 13.12 MM-PUR | Periodic processing | ME84 (batch) | SA release creation run | `PARTIAL-17` | 17 | SA release partial |
| 134 | 13.12 MM-PUR | Periodic processing | MRKO (consignment settlement) | — | `PARTIAL-16` | 16 | Consignment gap |
| 135 | 13.12 MM-PUR | Periodic processing | MEI1 / MEI2 / MEI4 | Subsequent price change (automatic document adjustment after condition change) | `PARTIAL-17` | 17 | Retroactive re-pricing of open POs after condition change — not named; partial |
| 136 | 13.12 MM-PUR | Customizing (SPRO) | OMGN — Set up stock transport order (delivery type, checking rule per supplying plant) | T161V, T161W | `PARTIAL-16` | 16 | Transfer order gap |
| 137 | 13.12 MM-PUR | Customizing (SPRO) | Source determination — source list requirement per plant, quota arrangement rules | T001W, TMQ1 | `PARTIAL-22` | 22 | Quota partial |
| 138 | 13.12 MM-PUR | Customizing (SPRO) | Scheduling agreement — release creation profile, firm zone/trade-off zone, JIT indicator | T163M | `PARTIAL-17` | 17 | SA release partial |
| 139 | 13.12 MM-PUR | Customizing (SPRO) | Ariba Network integration (cXML, Cloud Integration Gateway) | — | `PARTIAL-35` | 35 | cXML not listed among EDI formats (X12/EDIFACT/Peppol); partial |
| 140 | 13.13 MM-IM | Master data & documents | MB53 / MB54 | Plant availability / consignment stock | `PARTIAL-16` | 16 | Vendor consignment gap |
| 141 | 13.13 MM-IM | Master data & documents | MB58 | Consignment and returnable packaging stock at customer | `PARTIAL-27` | 27 | Customer consignment (fill-up/issue/pick-up/return orders) not named in SD/inventory waves — partial; needs "stock at customer" ownership dimension |
| 142 | 13.13 MM-IM | Master data & documents | MIK1 / MIQ1 / MIM1 | PI for vendor consignment / project stock / returnable transport packaging | `PARTIAL-16` | 16 | Consignment/RTP gaps |
| 143 | 13.13 MM-IM | Master data & documents | MIE1 / MIW1 / MIV1 | PI for sales order stock / customer consignment / returnable packaging at customer | `PARTIAL-27` | 27 | Sales-order stock & customer consignment partial |
| 144 | 13.13 MM-IM | Master data & documents | MIS1 / MIS2 (inventory sampling) | Inventory sampling (statistical) | `GAP` | propose 16 | Statistical inventory sampling (count a sample, extrapolate) — rarely used but a recognized GAAP-compatible method; low priority |
| 145 | 13.13 MM-IM | Master data & documents | Special stock: sales order stock (E), project stock (Q) | Stock segregated by SO/WBS | `PARTIAL-16` | 16 | Sales-order/project-specific stock (valuated/non-valuated) not explicitly named as an inventory dimension — partial; needed for MTO/ETO manufacturers (waves 21/27/41) |
| 146 | 13.13 MM-IM | Master data & documents | Stock in transit (MB5T; S/4 stock in transit for STO) | In-transit stock | `PARTIAL-16` | 16 | Transfer order gap |
| 147 | 13.13 MM-IM | Master data & documents | Returnable transport packaging (M, V) | RTP at vendor/customer | `GAP` | propose 28 | Returnable packaging (pallets, crates) tracking at customers/vendors not named; propose wave 28 (packaging/HU) option |
| 148 | 13.13 MM-IM | Reports & inquiries | MB5T | Stock in transit (cross-company/plant) | `PARTIAL-16` | 16 | Transfer order gap |
| 149 | 13.13 MM-IM | Reports & inquiries | MB54 / MB58 / MBLB | Consignment / customer consignment / subcontractor stock | `PARTIAL-16` | 16 | Consignment gaps |
| 150 | 13.13 MM-IM | Reports & inquiries | MBBS | Valuated special stock (sales order/project) | `PARTIAL-16` | 16 | Special stock gap |
| 151 | 13.13 MM-IM | Periodic processing | MRKO | Consignment & pipeline settlement | `PARTIAL-16` | 16 | Consignment gap |
| 152 | 13.13 MM-IM | Customizing (SPRO) | OMWD / OMWC / OMSK — Valuation grouping code / split valuation (valuation categories/types) / valuation classes & account category reference | T001K, T025, T025K, T149* | `PARTIAL-20` | 20 | Split valuation (different values per valuation type, e.g. new vs refurbished, domestic vs import) not named — partial; batch-level valuation needed for process industries |
| 153 | 13.13 MM-IM | Customizing (SPRO) | Special stock settings (consignment price determination, pipeline) | — | `PARTIAL-16` | 16 | Consignment gap |
| 154 | 13.13 MM-IM | Customizing (SPRO) | Stock determination (OSPX / OSPW) strategies for issues (consignment vs own, SLoc priority) | — | `PARTIAL-16` | 16 | Picking policies FEFO/FIFO named; ownership-based stock determination partial (consignment) |
| 155 | 13.14 MM-IV | Master data & documents | MRKO | Consignment and pipeline settlement | `PARTIAL-16` | 16 | Consignment gap |
| 156 | 13.14 MM-IV | Master data & documents | MRIS | Invoicing plan settlement (periodic/partial invoicing plans on PO) | `PARTIAL-17` | 17 | Invoicing plans on POs (periodic settlement without GR) not named — partial; relevant to leases/services; propose wave 17 PO line "invoicing plan" |
| 157 | 13.14 MM-IV | Master data & documents | MRNB | Revaluation (retroactive price change on invoiced items) | `PARTIAL-17` | 17 | Retro re-pricing partial (MEI1 row) |
| 158 | 13.14 MM-IV | Periodic processing | MRKO (scheduled) | Consignment settlement | `PARTIAL-16` | 16 | — |
| 159 | 13.14 MM-IV | Periodic processing | MRIS (scheduled) | Invoicing plan run | `PARTIAL-17` | 17 | — |
| 160 | 13.14 MM-IV | Periodic processing | MRNB (scheduled) | Revaluation run | `PARTIAL-17` | 17 | — |
| 161 | 13.14 MM-IV | Customizing (SPRO) | OMRX — Stochastic block (random sampling thresholds) | T169P | `GAP` | propose 18 | Random audit sampling of invoices — simple to add to wave 18 exception rules; register as gap |
| 162 | 13.14 MM-IV | Customizing (SPRO) | Invoicing plan types (periodic/partial) | T163* | `PARTIAL-17` | 17 | Invoicing plan gap |
| 163 | 13.15 MM-SRV | Master data & documents | AC01 / AC02 / AC03 / AC04 / AC06 | Service master create/change/display/list/changes | `PARTIAL-8` | 8 | Product of type "service" is covered; a dedicated service master with service categories, purchasing text, standard service catalog is partial — add "service product type" attributes to wave 8 |
| 164 | 13.15 MM-SRV | Master data & documents | ML10 / ML11 / ML12 | Model service specifications create/change/display | `GAP` | propose 17 | Service specification templates (hierarchical outlines of services with quantities) not named; propose "PO line templates" in wave 17 |
| 165 | 13.15 MM-SRV | Master data & documents | Standard service catalog (SSC) | Standard texts for services | `GAP` | propose 17 | Same |
| 166 | 13.15 MM-SRV | Master data & documents | ME21N (item cat D) with service lines, value limits, contract limits | Service PO with hierarchical service outline and limits | `PARTIAL-17` | 17 | Limit/blanket POs named; service outlines (hierarchy of service lines under one PO item) and contract-limit references are partial |
| 167 | 13.15 MM-SRV | Master data & documents | ML81N / ML82 / ML83 / ML84 / ML85 | Service entry sheet create-maintain / display / list / accept-revoke / collective release | `PARTIAL-18` | 18 | Receiving covers "product receipt"; a *service entry sheet* (lines of performed services with quantities/prices, acceptance workflow, partial entries, unplanned services within limits) is not named — partial; add "service receipt/entry sheet with acceptance" to wave 18 |
| 168 | 13.15 MM-SRV | Master data & documents | ML33 / ML34 / ML35 / ML36 / ML39 / ML40 / ML41 / ML45 / ML46 / ML47 / ML48 | Service conditions: vendor-level / service-level / vendor+plant | `PARTIAL-17` | 17 | Vendor price lists for service products via pricing engine — partial until service products confirmed |
| 169 | 13.15 MM-SRV | Master data & documents | MSRV1 … MSRV6 | Service lists: by service / by PO / by SES / by vendor / by contract / entry sheets | `PARTIAL-18` | 18 | — |
| 170 | 13.15 MM-SRV | Master data & documents | MIRO with SES reference | Invoice for services (SES-based) | `PARTIAL-18` | 18 | Match to service receipt |
| 171 | 13.15 MM-SRV | Master data & documents | ML91 / ML92 / ML93 | Service entry sheet archiving / lists | `PARTIAL-18` | 18 | — |
| 172 | 13.15 MM-SRV | Master data & documents | Fiori: Manage Service Entry Sheets – Lean Services / Create Service Entry Sheet | S/4 lean services | `PARTIAL-18` | 18 | Same |
| 173 | 13.15 MM-SRV | Master data & documents | Fiori: Manage Purchase Orders (service items), Manage Supplier Quotations for services | — | `PARTIAL-17` | 17 | — |
| 174 | 13.15 MM-SRV | Master data & documents | Supplier self-service SES (SAP Business Network / Fieldglass) | Supplier enters SES / timesheets | `PARTIAL-20` | 20 | Supplier portal: add service confirmation/timesheet entry (contingent labor) — partial |
| 175 | 13.15 MM-SRV | Reports & inquiries | MSRV1 … MSRV6 | Service lists | `PARTIAL-18` | 18 | — |
| 176 | 13.15 MM-SRV | Reports & inquiries | ME2S | Services per purchase order | `PARTIAL-18` | 18 | — |
| 177 | 13.15 MM-SRV | Reports & inquiries | MSRV3 (entry sheets) | SES list | `PARTIAL-18` | 18 | — |
| 178 | 13.15 MM-SRV | Reports & inquiries | ML83 / ML84 | SES acceptance list | `PARTIAL-18` | 18 | — |
| 179 | 13.15 MM-SRV | Periodic processing | ML85 (collective release of SES) | Mass acceptance | `PARTIAL-18` | 18 | — |
| 180 | 13.15 MM-SRV | Periodic processing | MRRL for services | ERS for SES | `PARTIAL-18` | 18 | — |
| 181 | 13.15 MM-SRV | Customizing (SPRO) | Service master: number ranges, service categories, organizational status, field selection | ASMD | `PARTIAL-8` | 8 | — |
| 182 | 13.15 MM-SRV | Customizing (SPRO) | Service specifications: screen layout, number ranges for SES, release procedure for SES (classification) | ESSR | `PARTIAL-18` | 18 | SES approval via workflow designer |
| 183 | 13.15 MM-SRV | Customizing (SPRO) | Service conditions: calculation schema MS0000, condition types PRS | T685 | `PARTIAL-17` | 17 | — |
| 184 | 13.15 MM-SRV | Customizing (SPRO) | Default values for SES acceptance (immediate GR, account assignment) | — | `PARTIAL-18` | 18 | — |
| 185 | 13.15 MM-SRV | Customizing (SPRO) | Source determination for services (contracts) | — | `PARTIAL-17` | 17 | — |
| 186 | 13.15 MM-SRV | Customizing (SPRO) | Lean services: activate, product type service, service entry sheet (lean) settings | — | `PARTIAL-18` | 18 | — |
| 187 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Master data & documents | LXDCA / cross-docking (LXDCK) | Planned cross-docking | `GAP` | propose 31 | Cross-docking (inbound → outbound without put-away, planned/opportunistic) not named in wave 31 — propose directive type "cross-dock" |
| 188 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Master data & documents | EWM: /SCWM/PACK / /SCWM/PACKSPEC / /SCWM/PSSTAGE | Packing work center / packaging specifications | `PARTIAL-31` | 31 | HU/packing covered; *packaging specifications* (rules: product → packaging material, quantities per level) only implied — partial; propose wave 28/31 "packaging rules" |
| 189 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Master data & documents | EWM: /SCWM/VASEXEC / VAS orders | Value-added services (kitting, labeling) | `GAP` | propose 31 | VAS/kit-to-order in warehouse not named; propose wave 31 work type "VAS" and wave 24 kitting via lean BOM |
| 190 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Master data & documents | EWM: /SCWM/LM (labor management: engineered labor standards, planned vs actual, performance docs) | Labor management | `PARTIAL-31` | 31 | Warehouse KPIs named; engineered labor standards, operator performance/incentive documents — partial |
| 191 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Master data & documents | EWM: /SCWM/YM_* yard management (yard bins, vehicles, TUs, checkpoints) | Yard management | `PARTIAL-31` | 31 | Dock scheduling named; yard (trailer positions, moves, check-in/out) partial |
| 192 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Master data & documents | EWM: /SCWM/TU, /SCWM/VEH | Transportation units / vehicles | `PARTIAL-34` | 34 | Load/TU in wave 34 — partial for yard link |
| 193 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Master data & documents | EWM: /SCWM/MFS* (Material Flow System: PLC interface, conveyors, ASRS) | Automation integration | `GAP` | propose 32 | MFS/PLC integration (telegrams to conveyor/AS-RS) not named; wave 25 OPC-UA/MQTT edge is the natural base — propose wave 32 "automation adapter (ASRS/conveyor)" |
| 194 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Master data & documents | EWM: Kit-to-stock / kit-to-order | — | `GAP` | propose 31 | See VAS |
| 195 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Master data & documents | EWM: Dangerous goods / HazMat checks in warehouse (EHS) | — | `GAP` | propose 34 | Hazmat classification, storage compatibility, DG documents — not named; propose wave 34 (transport) + wave 8 attributes |
| 196 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Reports & inquiries | LX06 / LX07 | Fire-containment sections / check storage (inconsistencies) | `PARTIAL-31` | 31 | Fire/hazard sections tie to hazmat gap |
| 197 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Reports & inquiries | EWM: /SCWM/LM_ labor reports (planned vs actual) | — | `PARTIAL-31` | 31 | — |
| 198 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Periodic processing | Labor management period close (/SCWM/LM) | — | `PARTIAL-31` | 31 | — |
| 199 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Customizing (SPRO) | Hazardous material: regions, water pollution classes, storage classes, fire-containment sections | T* (MGEF) | `GAP` | propose 34 | Hazmat gap |
| 200 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Customizing (SPRO) | EWM: Yard management customizing | — | `PARTIAL-31` | 31 | — |
| 201 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Customizing (SPRO) | EWM: MFS customizing (PLC communication channels, telegrams) | — | `GAP` | propose 32 | — |
| 202 | 13.16 LE-WM (classic Warehouse Management) and EWM (Extended Warehouse Management) | Customizing (SPRO) | EWM: Labor management: formulas, ELS, incentive | — | `PARTIAL-31` | 31 | — |
| 203 | 13.17 LE-SHP / LE-TRA | Master data & documents | VL10 → STO deliveries (replenishment delivery NL/NLCC) | Deliveries for stock transport orders | `PARTIAL-16` | 16 | Transfer order gap |
| 204 | 13.17 LE-SHP / LE-TRA | Master data & documents | Dangerous goods check in delivery (EHS-DG), DG documents | — | `GAP` | propose 34 | Hazmat gap |
| 205 | 13.17 LE-SHP / LE-TRA | Master data & documents | Foreign trade / export documents in delivery (customs, commercial invoice, EUR.1, Intrastat from delivery; GTS) | Export compliance | `PARTIAL-13` | 13 | Intrastat extract named (wave 13); export documentation (commercial invoice, packing list for customs, certificate of origin), export control screening (denied parties, licenses) and customs declarations (GTS) are not — partial; propose wave 48 "foreign trade/customs" pack |
| 206 | 13.17 LE-SHP / LE-TRA | Master data & documents | Preference determination (SAP GTS / foreign trade) | Origin & preference (USMCA/EU) | `GAP` | propose 48 | Rules-of-origin/preference calculation from BOM — gap |
| 207 | 13.17 LE-SHP / LE-TRA | Customizing (SPRO) | OVLS / OVLV — Packing: packaging material types (VHAR), packing instructions (POP1/POP2/POP3/POF1), allowed packaging materials, HU number ranges (SSCC), packing control per item category | TVTY, TVEGR, PACKKP | `PARTIAL-28` | 28 | SSCC/containers yes; packing instructions (auto-pack rules) partial (same as EWM packaging specs) |
| 208 | 13.17 LE-SHP / LE-TRA | Customizing (SPRO) | Foreign trade: export control, preference, Intrastat settings (VEB, VE0) | T604, T605 | `PARTIAL-13` | 13 | Foreign trade gap |
| 209 | 13.17 LE-SHP / LE-TRA | Customizing (SPRO) | Dangerous goods: DG profiles, DG check methods in SD/LE | DGT* | `GAP` | propose 34 | Hazmat gap |
| 210 | 13.18 TM | Master data & documents | Transportation units / resources (vehicles, trailers, drivers), calendars | Fleet resources | `PARTIAL-34` | 34 | Own fleet (vehicles/drivers/driver hours) not named — partial; carriers assumed external |
| 211 | 13.18 TM | Master data & documents | Dangerous goods & DG checks in TM | — | `GAP` | propose 34 | Hazmat gap |
| 212 | 13.18 TM | Master data & documents | Customs declarations via GTS from TM | — | `PARTIAL-13` | 13 | Foreign trade gap |
| 213 | 13.18 TM | Master data & documents | Strategic freight procurement (RFQ for lanes, bid analysis) | — | `GAP` | propose 34 | Lane-based freight RFQ/bid analysis — gap; could reuse wave 17 RFQ |
| 214 | 13.18 TM | Master data & documents | Load consolidation / container packing optimization (load plan 3D) | — | `PARTIAL-34` | 34 | Load building with capacity constraints named; 3D load plan/visual stacking partial |
| 215 | 13.18 TM | Customizing (SPRO) | Dangerous goods in TM | — | `GAP` | propose 34 | — |
| 216 | 13.19 SD | Master data & documents | VB01 / VB02 / VB03 | Listing/exclusion | `PARTIAL-27` | 27 | Product listing/exclusion per customer not named in wave 27 — partial; simple rule on order validation |
| 217 | 13.19 SD | Master data & documents | VBN1 / VBN2 / VBN3 | Free goods condition records (inclusive/exclusive) | `PARTIAL-26` | 26 | Free goods (buy 10 get 1) not named; promotions with quantity breaks are — partial |
| 218 | 13.19 SD | Master data & documents | VA01 KB / KE / KA / KR (consignment fill-up/issue/pick-up/returns) | Customer consignment processes | `PARTIAL-27` | 27 | Customer consignment gap (see MM-IM) |
| 219 | 13.19 SD | Master data & documents | VA31 / VA32 / VA33 / VA35 | Customer scheduling agreement create/change/display/list (SA with JIT/forecast schedules VA32 → delivery schedule) | `PARTIAL-27` | 27 | "Scheduling agreements" named in wave 27 replaces list; inbound EDI DELFOR/DELJIT (830/862) customer schedules with cumulative quantities and schedule reconciliation are not named — partial (automotive) |
| 220 | 13.19 SD | Master data & documents | VKA / VSB1 (self-billing) | Self-billing procedure (customer self-billing, automotive) | `GAP` | propose 29 | Customer self-billing (EDI 820/GSVERF reconciliation against invoices) not named; automotive; propose wave 29/35 option |
| 221 | 13.19 SD | Master data & documents | Fiori: Manage Solution Orders / Solution Business (bundles) | Bundled sales (products, services, subscriptions) | `PARTIAL-29` | 29 | Bundles with mixed revenue types partially via rev rec bundles; solution-order document type not named — partial |
| 222 | 13.19 SD | Master data & documents | Cross-selling (VB41), product proposal/dynamic product proposal | Order-entry suggestions | `PARTIAL-27` | 27 | Product proposals partially (item proposal); cross-sell rules not named |
| 223 | 13.19 SD | Customizing (SPRO) | OVZJ / OVZI — Product allocation (quota per customer/period) | T190, KOTA | `GAP` | propose 27 | Product allocation (ATP quotas per customer/region in scarcity) not named — propose wave 27 ATP option |
| 224 | 13.19 SD | Customizing (SPRO) | OVZK / OVZ — Availability check against product allocation; aATP (S/4): backorder processing strategies (BOP), alternative-based confirmation (ABC), release for delivery | — | `PARTIAL-27` | 27 | ATP/CTP covered; aATP backorder processing with strategies (win/gain/redistribute/fill/lose) partially named ("backorder handling") |
| 225 | 13.19 SD | Customizing (SPRO) | OVZ "Define requirements classes/types, assignment to strategy group" (OVZG/OVZH/OVZI) | T459K, T459A, T459U | `PARTIAL-27` | 27 | Planning strategies (wave 22) + sales-order stock partial |
| 226 | 13.19 SD | Customizing (SPRO) | OV "Listing/exclusion procedures" (OV04) | T684 | `PARTIAL-27` | 27 | Listing gap |
| 227 | 13.19 SD | Customizing (SPRO) | OVKF — Free goods: condition type NA00, procedure NA0001, determination | T685, KOTN | `PARTIAL-26` | 26 | Free goods gap |
| 228 | 13.20 SD Rebates / Settlement Management (condition contracts) | Master data & documents | VBOF | Update billing documents (retroactive rebate accrual) | `PARTIAL-26` | 26 | Retroactive rebate (apply agreement to already-invoiced volume) not named — partial |
| 229 | 13.20 SD Rebates / Settlement Management (condition contracts) | Master data & documents | Supplier condition contracts (purchasing rebates, chargebacks) | Vendor rebates / ship-and-debit chargebacks | `PARTIAL-19` | 19 | Supplier rebate accrual and settlement (credit from vendor) and chargeback/ship-and-debit claims are not named in waves 17/19 — partial; propose wave 26 engine reused for purchasing with settlement via AP in wave 19 |
| 230 | 13.20 SD Rebates / Settlement Management (condition contracts) | Master data & documents | Commission contracts (sales rep commissions via settlement management) | Commissions | `PARTIAL-26` | 26 | "Commission calculation hooks" only — partial; settlement to payroll/AP not named |
| 231 | 13.20 SD Rebates / Settlement Management (condition contracts) | Master data & documents | Chargeback / ship-and-debit claims from distributors | Claim validation against agreements | `GAP` | propose 30 | Distributor chargeback claims (EDI 844/849) validation and settlement — gap for manufacturers selling via distribution; propose wave 30 + wave 35 |
| 232 | 13.20 SD Rebates / Settlement Management (condition contracts) | Master data & documents | Royalty / licensing payments (settlement management use) | Royalty accrual/settlement | `GAP` | propose 26 | Royalty accrual by sales volume (license fees to third parties) — gap; reuse rebate engine with vendor-side settlement |
| 233 | 13.20 SD Rebates / Settlement Management (condition contracts) | Periodic processing | VBOF (batch) | Retroactive accrual update | `PARTIAL-26` | 26 | — |
| 234 | 13.20 SD Rebates / Settlement Management (condition contracts) | Periodic processing | Delta accruals / delta settlement | Delta settlement after retro changes | `PARTIAL-26` | 26 | Retroactive gap |
| 235 | 13.20 SD Rebates / Settlement Management (condition contracts) | Customizing (SPRO) | Chargeback / claim types | — | `GAP` | propose 30 | — |
| 236 | 13.20 SD Rebates / Settlement Management (condition contracts) | Customizing (SPRO) | Commission settlement integration (HR/AP) | — | `PARTIAL-26` | 26 | — |
| 237 | 13.21 PP | Master data & documents | MM02 → MRP areas (MD61 per MRP area) | MRP area assignment (storage location / subcontractor level planning) | `PARTIAL-22` | 22 | Planning at warehouse (sub-site) level and at subcontractor level not explicitly named — partial; wave 22 plans by site; add "planning scope = site or warehouse" |
| 238 | 13.21 PP | Master data & documents | CS51 / CS52 / CS53 / CS61 / CS62 / CS63 / CS71 … | Standard BOM / document BOM / equipment BOM / functional location BOM / sales order BOM / WBS BOM | `PARTIAL-9` | 9 | Material BOM yes; equipment/functional location BOMs in wave 44; sales-order BOMs (order-specific) and WBS BOMs not named — partial (ETO) |
| 239 | 13.21 PP | Master data & documents | MS01 / MS02 / MS03 / MS04 / MS05 / MS06 / MS07 / MS08 | Long-term planning (LTP): planning scenario, simulative MRP, stock/requirements (simulation), evaluations | `PARTIAL-22` | 22 | What-if simulation plans (separate planning versions with simulated demand, evaluating capacity/purchasing) not explicitly named — partial; propose "static plans" (AX-style) in wave 22 |
| 240 | 13.21 PP | Master data & documents | MS31 / MS32 / MS33 / MS44 / MS47 / MS64 / MS65 / MS70 | LTP: planning scenario create/change/display, planned orders in scenario, evaluation (capacity, PURCHIS transfer) | `PARTIAL-22` | 22 | Same |
| 241 | 13.21 PP | Master data & documents | Demand-driven replenishment (DDMRP: buffer positioning, buffer levels, replenishment planning, Fiori apps) | DDMRP (S/4) | `GAP` | propose 22 | DDMRP (decoupling points, buffer zones red/yellow/green, net flow equation) not named; propose as wave 22 coverage-group type "DDMRP buffer" |
| 242 | 13.21 PP | Master data & documents | Predictive MRP (pMRP, S/4) | Rough simulation of future demand vs capacity | `PARTIAL-22` | 22 | Overlaps LTP partial |
| 243 | 13.21 PP | Master data & documents | Phantom assemblies, alternative items (BOM item alternative groups), discontinuation (MM02 follow-up material) | — | `PARTIAL-9` | 9 | Phantoms yes; alternative item groups (priority/strategy) and discontinuation/follow-up (run-out) are not named — partial; propose wave 9 BOM line attributes + wave 22 MRP logic |
| 244 | 13.21 PP | Master data & documents | Make-to-order with sales order stock (strategy 20), planning with final assembly (40), planning without final assembly (50), subassembly planning (70) | Planning strategies | `PARTIAL-22` | 22 | Coverage groups + forecast reduction named; the full strategy-group semantics (consumption modes, MTO stock segmentation, planning at assembly level) are partially covered — partial; sales-order stock noted in MM-IM |
| 245 | 13.21 PP | Master data & documents | Rework (CO07 / trigger points CO31 / CO33) | Rework orders / trigger points inserting rework operations | `PARTIAL-21` | 21 | Rework via order w/o material covered; trigger points (automatic rework operation insertion on confirmation) not named — partial |
| 246 | 13.21 PP | Master data & documents | Order split (CO02 → split), order change management (OCM: COCM / COCM1) | Order split / order change management after ECO | `PARTIAL-21` | 21 | Order change management (propagating BOM/routing changes to open orders with change costs) not named — partial; propose wave 9/21 |
| 247 | 13.21 PP | Customizing (SPRO) | OPPS / OPPT — Planning strategies / strategy groups (10, 11, 20, 25, 30, 40, 50, 52, 59, 60, 63, 70, 74, 81, 82…) | T461S, T461P | `PARTIAL-22` | 22 | Planning strategy semantics partial (noted) |
| 248 | 13.21 PP | Customizing (SPRO) | OMIZ / MRP areas (OMIZ / S/4 MRP areas per storage location, subcontractor) | MDLV, MDLG, MDLW | `PARTIAL-22` | 22 | MRP area partial |
| 249 | 13.21 PP | Customizing (SPRO) | OPMP / OPM — Trigger points (standard trigger points, functions) | TTRIG | `PARTIAL-21` | 21 | Trigger point gap |
| 250 | 13.21 PP | Customizing (SPRO) | OS40 / OS41 / OS44 — ECM: change types, object types, status, ECR/ECO profiles, release keys, effectivity types (date, serial, parameter) | TCC*, TECH* | `PARTIAL-9` | 9 | Date effectivity covered; serial-number/parameter effectivity not named — partial |
| 251 | 13.21 PP | Customizing (SPRO) | DDMRP buffer profiles, classification runs (S/4 DD) | PPH_DD_* | `GAP` | propose 22 | DDMRP gap |
| 252 | 13.21 PP | Customizing (SPRO) | OCM profiles (order change management: change profiles, change cost) | COCM* | `PARTIAL-21` | 21 | OCM partial |
| 253 | 13.22 PP-PI | Master data & documents | CRC5 / CRC | Resource network (RN) | `PARTIAL-23` | 23 | Resource networks (allowed sequences of resources, e.g. vessel → filler) not named — partial; scheduling with CP-SAT could model as constraints |
| 254 | 13.22 PP-PI | Master data & documents | CO53 / CO53XT | Control recipe monitor (send control recipes to process control / PI sheet) | `PARTIAL-25` | 25 | Shop-floor instructions/drawings on kiosk; structured *control recipes* with process instruction categories sent to a destination (PCS/PI sheet) are partial |
| 255 | 13.22 PP-PI | Master data & documents | CO54 / CO54XT / CO57 / CO58 | Process message monitor / create process message manually / send | `PARTIAL-25` | 25 | IoT/MQTT machine events + kiosk reporting cover inbound; configurable message categories with destinations (e.g. to QM results, to batch record) partial |
| 256 | 13.22 PP-PI | Master data & documents | CO60 / CO60XT / CO60_PI / CO64 / CO67 | PI sheet: find/maintain / worklist / print / archive | `PARTIAL-25` | 25 | Kiosk instructions; a designer for interactive PI sheets (XSteps with input fields, calculations, signatures, process data requests) is not named — partial |
| 257 | 13.22 PP-PI | Master data & documents | COEBR | Electronic batch record (EBR) creation/approval/archive (21 CFR Part 11) | `GAP` | propose 24 | EBR with digital signatures, deviation review and archiving (GxP) not named — propose wave 24 (+ wave 48 compliance) |
| 258 | 13.22 PP-PI | Master data & documents | CMX / Standard XSteps (CMXSV) / SXS repository | Standard XSteps repository | `PARTIAL-25` | 25 | Same as PI sheet |
| 259 | 13.22 PP-PI | Master data & documents | Campaign planning (PP-PI campaign, process order campaigns: COCP / CMP) | Group process orders into campaigns (setup/cleanout) | `PARTIAL-23` | 23 | Campaign/sequence planning (group orders to minimize changeover, cleanout orders) not named — partial; CP-SAT setup optimization partly covers |
| 260 | 13.22 PP-PI | Master data & documents | Cleanout / changeover orders, recipe-dependent cleaning | — | `PARTIAL-23` | 23 | Same |
| 261 | 13.22 PP-PI | Master data & documents | Digital signature in PI sheets / batch records (DSIG) | 21 CFR Part 11 signatures | `GAP` | propose 24 | E-signature framework (meaning of signature, re-authentication) not named; propose cross-cutting in wave 24/48 |
| 262 | 13.22 PP-PI | Master data & documents | Batch derivation (DVMO / DVMAN) — see §13.33 | — | `PARTIAL-24` | 24 | Batch derivation (inherit attributes from component batches to product batch) not named — partial |
| 263 | 13.22 PP-PI | Reports & inquiries | CO54 (monitor) | Process message log | `PARTIAL-25` | 25 | — |
| 264 | 13.22 PP-PI | Reports & inquiries | COEBR (list) | Batch record list | `GAP` | propose 24 | — |
| 265 | 13.22 PP-PI | Reports & inquiries | CMX/CO60 worklists | PI sheet worklist | `PARTIAL-25` | 25 | — |
| 266 | 13.22 PP-PI | Periodic processing | CO53 (send control recipes, batch) | — | `PARTIAL-25` | 25 | — |
| 267 | 13.22 PP-PI | Periodic processing | CO54 (process message processing, batch) | — | `PARTIAL-25` | 25 | — |
| 268 | 13.22 PP-PI | Periodic processing | CO64 / CO67 (PI sheet archive) | — | `PARTIAL-25` | 25 | — |
| 269 | 13.22 PP-PI | Periodic processing | COEBR (batch record generation/archive) | — | `GAP` | propose 24 | — |
| 270 | 13.22 PP-PI | Periodic processing | Campaign settlement / cleanout cost allocation | — | `PARTIAL-23` | 23 | — |
| 271 | 13.22 PP-PI | Customizing (SPRO) | Process management: process instruction categories/characteristics (O10C O12C / O13C), process message categories & destinations (O05C / O06C / O07C), control recipe destinations (O10C), PI sheet layouts, XStep settings (SXS), signature strategies (O2 / DSIG) | TPM*, CPM* | `PARTIAL-25` | 25 | PI sheet / message framework partial |
| 272 | 13.22 PP-PI | Customizing (SPRO) | Resource networks | — | `PARTIAL-23` | 23 | — |
| 273 | 13.22 PP-PI | Customizing (SPRO) | Campaign planning settings | — | `PARTIAL-23` | 23 | — |
| 274 | 13.22 PP-PI | Customizing (SPRO) | EBR: batch record profiles, archiving, approval workflow | — | `GAP` | propose 24 | — |
| 275 | 13.22 PP-PI | Customizing (SPRO) | Batch management in PP-PI: batch determination procedures for process orders (COB1), classification (023), batch derivation | TLSP, T* | `PARTIAL-24` | 24 | Derivation partial |
| 276 | 13.23 PP/DS | Master data & documents | Block planning (resource blocks per characteristic, /SAPAPO/CDPSC12) | Reserve capacity blocks by product characteristics | `GAP` | propose 23 | Block planning (time blocks on resources restricting which characteristic values can be produced) not named — propose wave 23 constraint type |
| 277 | 13.23 PP/DS | Master data & documents | Characteristic-dependent planning (CDP) / CDP configuration | Planning by batch characteristics (e.g., color, grade) | `PARTIAL-24` | 24 | Batch attributes exist; planning that matches demand characteristics to supply batches (CDP) — partial; propose wave 22/24 |
| 278 | 13.23 PP/DS | Master data & documents | Shelf-life planning (SAP_PP_SL001) | Pegging respecting expiry (FEFO planning) | `PARTIAL-22` | 22 | FEFO picking (wave 16) yes; planning-time shelf-life pegging (min remaining shelf life at demand date) not named — partial |
| 279 | 13.23 PP/DS | Master data & documents | /SAPAPO/MC01 (models/versions), /SAPAPO/MVM | Planning versions (simulation versions) | `PARTIAL-22` | 22 | Simulation versions = LTP partial |
| 280 | 13.23 PP/DS | Master data & documents | /SAPAPO/SDP94 (DP), /SAPAPO/SNP94 (SNP), GATP (/SAPAPO/AC03) | Demand planning / supply network planning / global ATP | `PARTIAL-22` | 22 | DP ≈ wave 22 forecasting (statistical + consensus); SNP (multi-site network optimization: sourcing across plants/DCs with transport lanes and capacity) and GATP (rules-based ATP, multi-site substitution) are not named — partial; register multi-site network planning as a v1.x candidate under wave 22/27 |
| 281 | 13.23 PP/DS | Customizing (SPRO) | Block planning settings, CDP settings | — | `GAP` | propose 23 | Block planning gap |
| 282 | 13.24 QM | Master data & documents | QC51 / QC52 / QC53 / QC55 | Certificate receipt (incoming certificates from vendor) | `PARTIAL-33` | 33 | Incoming vendor certificate receipt/tracking (required certificate at GR, block until received) not named — partial |
| 283 | 13.24 QM | Master data & documents | QST01 / QST02 / QST03 / QST04 / QST05 / QST06 / QST07 | Stability study (initial sample, storage conditions, testing schedule) | `GAP` | propose 33 | Stability studies (pharma/food shelf-life testing schedules) not named; propose wave 33 extension for regulated process manufacturers |
| 284 | 13.24 QM | Master data & documents | PLMD_AUDIT (Audit management: audits, questions, corrective actions) | Audit management | `PARTIAL-33` | 33 | CAPA yes; audit planning/questionnaires/audit findings (ISO 9001 internal audits, supplier audits) not named — partial |
| 285 | 13.24 QM | Master data & documents | FMEA (QM-FMEA, S/4), control plan (S/4 QM) | FMEA / control plan | `GAP` | propose 33 | FMEA and control plans (APQP/PPAP) — gap for automotive/aerospace suppliers; propose wave 33 extension |
| 286 | 13.24 QM | Master data & documents | Source inspection (QI01 source inspection flag, QI07) | Inspection at vendor before GR | `PARTIAL-33` | 33 | Source inspection lots (before delivery) not named — partial |
| 287 | 13.24 QM | Master data & documents | Sample management (QPR1 / QPR2 / QPR3 / QPR5 / QPR6 / QPR7) | Physical samples, sample drawing procedure, sample labels | `PARTIAL-33` | 33 | Sampling sizes yes; physical sample records (primary/pooled samples with labels, retention samples) not named — partial |
| 288 | 13.24 QM | Master data & documents | Multiple specifications (customer/country-specific specs, S/4 "Multiple Specifications") | — | `PARTIAL-33` | 33 | Customer-specific test specs/limits — partial |
| 289 | 13.24 QM | Reports & inquiries | QC55 | Certificate receipt overview | `PARTIAL-33` | 33 | — |
| 290 | 13.24 QM | Periodic processing | QST (stability schedule generation) | Stability testing schedule | `GAP` | propose 33 | — |
| 291 | 13.24 QM | Customizing (SPRO) | Define inspection points (time/quantity/freely defined), partial lots, inspection point identification | TQ79 | `PARTIAL-33` | 33 | In-process inspection points (per time/quantity during production) partially covered by "production" trigger — partial |
| 292 | 13.24 QM | Customizing (SPRO) | Stability study customizing | — | `GAP` | propose 33 | — |
| 293 | 13.24 QM | Customizing (SPRO) | FMEA / control plan customizing (S/4) | — | `GAP` | propose 33 | — |
| 294 | 13.24 QM | Customizing (SPRO) | Audit management customizing (audit types, question lists) | — | `PARTIAL-33` | 33 | — |
| 295 | 13.24 QM | Customizing (SPRO) | Multiple specifications customizing | — | `PARTIAL-33` | 33 | — |
| 296 | 13.24 QM | Customizing (SPRO) | Sample management customizing (sample types, sample-drawing procedures) | — | `PARTIAL-33` | 33 | — |
| 297 | 13.25 PM | Master data & documents | IE31 / IE36 / IE37 | Fleet object create / list / change | `PARTIAL-44` | 44 | Fleet-specific attributes (fuel, odometer, driver) not named — partial; counters cover odometer |
| 298 | 13.25 PM | Master data & documents | IW81 / IW8W | Refurbishment order create / goods receipt for refurbishment | `PARTIAL-44` | 44 | Refurbishment of rotables (condition-based valuation: damaged → refurbished) not named — partial; split valuation gap in MM |
| 299 | 13.25 PM | Master data & documents | IW32 → Permits (IPMD / WCM) | Permits, work clearance management (lockout/tagout) | `PARTIAL-44` | 44 | Permits/LOTO work clearance (WCM) not named — partial; propose wave 44 "permit/LOTO checklist" |
| 300 | 13.25 PM | Master data & documents | Asset Intelligence Network / Predictive Asset Insights / Asset Strategy & Performance Management | Cloud EAM extensions | `PARTIAL-44` | 44 | Condition monitoring + predictive hooks named; RCM/criticality analysis (ASPM) partial |
| 301 | 13.25 PM | Customizing (SPRO) | Refurbishment: valuation types for condition (OMWC split valuation), order type PM04 settings | T025, T003O | `PARTIAL-44` | 44 | Refurbishment gap |
| 302 | 13.25 PM | Customizing (SPRO) | Permits (define permit types, categories), WCM (work clearance objects, applications, approvals) | T357G, WCA* | `PARTIAL-44` | 44 | Permit/LOTO gap |
| 303 | 13.26 CS / Customer Service / SAP Field Service Management | Master data & documents | Warranty claim processing (WTY: WTY1 warranty claims from dealers/to vendors) | Warranty claims (supplier recovery) | `PARTIAL-45` | 45 | Customer warranty handling yes; supplier warranty claim recovery (claims to OEM/vendor, reimbursement) not named — partial |
| 304 | 13.26 CS / Customer Service / SAP Field Service Management | Master data & documents | Solution database (IS01 / IS02 symptom/solution) | Knowledge base | `GAP` | propose 45 | Knowledge base/solution search for cases not named; propose wave 45 (or wave 47 copilot answers) |
| 305 | 13.26 CS / Customer Service / SAP Field Service Management | Master data & documents | Customer Interaction Center (CIC0) / SAP CRM Service / S/4 Service (Service orders, service contracts, service confirmations, service quotation) | Service front office | `PARTIAL-45` | 45 | Case management yes; omnichannel contact center (telephony/CTI, email routing) not named — partial (CRM non-goal boundary) |
| 306 | 13.26 CS / Customer Service / SAP Field Service Management | Master data & documents | Service parts planning (SPP / advanced service parts) | Spare parts planning for service networks | `PARTIAL-22` | 22 | MRP covers; multi-echelon service parts planning partial |
| 307 | 13.26 CS / Customer Service / SAP Field Service Management | Customizing (SPRO) | Warranty claim processing (WTY) customizing | — | `PARTIAL-45` | 45 | — |
| 308 | 13.26 CS / Customer Service / SAP Field Service Management | Customizing (SPRO) | Solution database / knowledge base settings | — | `GAP` | propose 45 | — |
| 309 | 13.26 CS / Customer Service / SAP Field Service Management | Customizing (SPRO) | CIC / contact center framework | — | `PARTIAL-45` | 45 | — |
| 310 | 13.27 PS | Master data & documents | CJZ1 / CJZ2 / CJZ3 / CJZ5 | Interest calculation on projects | `GAP` | propose 41 | Project interest calculation (interest on WIP/capitalized interest IAS 23) not named — propose wave 41 option |
| 311 | 13.27 PS | Master data & documents | CJV1 / CJV2 / CJV3 / CJV4 / CJV5 / CJV6 | Simulation version create/change/display/transfer | `PARTIAL-41` | 41 | Project what-if simulation versions not named — partial |
| 312 | 13.27 PS | Master data & documents | CLM1 / CLM2 / CLM3 / CLM10 / CLM11 | Claims (variation/change requests with cost impact) | `PARTIAL-41` | 41 | Change orders/claims on projects (scope change with cost/revenue impact and customer approval) not named — partial; propose wave 41 "project change order" |
| 313 | 13.27 PS | Master data & documents | CNS0 / CN (shipping from projects — delivery from project) | Project deliveries (CNS0) | `PARTIAL-41` | 41 | Shipping items from project stock/WBS not named — partial (ETO) |
| 314 | 13.27 PS | Master data & documents | Project stock (Q), PS material components, MD51 project MRP, CN33 (BOM transfer PS-BOM) | Project procurement/stock | `PARTIAL-41` | 41 | Project items/POs named; project stock segregation partial (MM-IM note) |
| 315 | 13.27 PS | Master data & documents | IM01 / IM02 / IM03 / IM22 / IM23 / IM24 / IM25 / IM27 / IM28 / IM30 / IM31 / IM32 / IM33 / IM34 / IM35 / IM36 / IM52 / IMA11 / IMA2 / IMA3 / IMCCP1 | Investment Management: investment programs (positions, hierarchy), budget distribution/supplement/return, program planning, appropriation requests (IMA11 create, variants, approval), rollup/carry-forward | `PARTIAL-37` | 37 | Asset budgeting named in wave 37; investment program hierarchy with appropriation requests (approval of capex before projects/orders) and program budget distribution — partial; propose wave 38 budgeting + wave 41 |
| 316 | 13.27 PS | Master data & documents | Commercial Project Management (CPM: project workspace, financial planning, change/risk/issue management) | Professional-services project cockpit | `PARTIAL-41` | 41 | Risks/issues register, cost/revenue forecasting workbook — partial |
| 317 | 13.27 PS | Reports & inquiries | IM (IM reports: S_ALR_87012) | Investment program reports | `PARTIAL-37` | 37 | — |
| 318 | 13.27 PS | Periodic processing | CJZ2 (interest) | Interest calculation run | `GAP` | propose 41 | — |
| 319 | 13.27 PS | Periodic processing | IM27 / IM32 (IM budget rollup / carry-forward) | — | `PARTIAL-37` | 37 | — |
| 320 | 13.27 PS | Customizing (SPRO) | OPS — Project stock settings (valuated/non-valuated), PS material components (OPUV), MRP for project | TCJ41, T* | `PARTIAL-41` | 41 | Project stock partial |
| 321 | 13.27 PS | Customizing (SPRO) | Investment management: program types, appropriation request types, status, budget categories, plan/budget profiles | TAIF, TAPRT | `PARTIAL-37` | 37 | IM partial |
| 322 | 13.27 PS | Customizing (SPRO) | Claim management customizing (notification type CL, claim types) | TQ80 | `PARTIAL-41` | 41 | — |
| 323 | 13.27 PS | Customizing (SPRO) | Simulation versions / project versions profiles (OPTS / OPT) | TCJ | `PARTIAL-41` | 41 | — |
| 324 | 13.27 PS | Customizing (SPRO) | Interest profiles (OPIB / OPIC / OPID) | T* | `GAP` | propose 41 | — |
| 325 | 13.28 HCM | Master data & documents | PP61 / PP62 / PP63 / PP64 / PP6I / PP6A | Shift planning (target plan, requirements, assignments) | `PARTIAL-25` | 25 | Time registration & shifts in wave 25; shift rostering (planning employees to shifts against requirements, swaps) not named — partial; propose wave 42 |
| 326 | 13.28 HCM | Master data & documents | PC00_M10_OSUP / PC00_Mxx_OC (off-cycle workbench PUOC_10) | Off-cycle payroll (bonus, correction, final pay) | `PARTIAL-43` | 43 | Off-cycle/ad-hoc pay runs (bonus, termination final pay, check replacement) not explicitly named — partial |
| 327 | 13.28 HCM | Master data & documents | PC00_M10_CLAIMS / PC00_M10_UTXX | US claims processing / tax authorities | `PARTIAL-43` | 43 | Claims (overpayment recovery) not named — partial |
| 328 | 13.28 HCM | Master data & documents | HRBEN00ICOBRA / HRBEN00COBRA | COBRA (US) | `PARTIAL-42` | 42 | COBRA notices/continuation (US) belongs to US pack — partial |
| 329 | 13.28 HCM | Master data & documents | E-Recruiting (HRRCF_*) / SF Recruiting & Onboarding | Modern recruiting | `PARTIAL-42` | 42 | Pipeline basics; career site, requisition approval, offer letters, onboarding tasks — partial |
| 330 | 13.28 HCM | Master data & documents | PPCP / PPSS (career/succession planning) / SF Succession | Succession planning | `PARTIAL-42` | 42 | Talent basics; succession planning/talent pools not named — partial |
| 331 | 13.28 HCM | Master data & documents | PECM_* (enterprise compensation management: planning, budgets, long-term incentives, pay scale OH) / SF Compensation | Compensation planning cycles | `PARTIAL-42` | 42 | Compensation plans yes; merit/bonus planning cycles with budgets and manager worksheets — partial |
| 332 | 13.28 HCM | Master data & documents | PSV1 / PSV2 / PSV3 / PSVP / LSO_PSV (training & event management / LSO) / SF Learning | Training catalog, bookings, resources, follow-up | `PARTIAL-42` | 42 | Certifications tracked; course catalog/LMS (sessions, bookings, completion) not named — partial |
| 333 | 13.28 HCM | Master data & documents | HRUS_D2 / HRPAD (personnel data: concurrent employment, global employment) | Concurrent/global employment | `PARTIAL-42` | 42 | Multi-assignment and global assignments not named — partial |
| 334 | 13.28 HCM | Master data & documents | SF Onboarding / Offboarding | Onboarding tasks | `PARTIAL-42` | 42 | Onboarding checklists not named — partial |
| 335 | 13.28 HCM | Reports & inquiries | PC00_M10_* US reports (941, 940, W-2, state unemployment, new hire, EEO-1, workers' comp) | US statutory | `PARTIAL-43` | 43 | Year-end W-2/1099 named; quarterly 941/state UI and EEO/VETS reporting belong to US pack — partial |
| 336 | 13.28 HCM | Periodic processing | PU19 (quarterly/annual tax reporter runs) | Statutory filings | `PARTIAL-43` | 43 | — |
| 337 | 13.28 HCM | Customizing (SPRO) | Compensation management: compensation areas, plans, guidelines, budgets, eligibility | T71* | `PARTIAL-42` | 42 | Planning cycles partial |
| 338 | 13.28 HCM | Customizing (SPRO) | Training & event management / LSO: business event types, resources, booking, follow-up, LSO settings | T77* | `PARTIAL-42` | 42 | LMS partial |
| 339 | 13.28 HCM | Customizing (SPRO) | Country-specific HR infotypes and legal changes (country packs: US, DE, GB, FR, IN, MX, CA) | — | `PARTIAL-48` | 48 | Country-specific fields framework (wave 42) + packs (wave 48) cover tax/statutory; HR-legal (works council, contract types, statutory leave) per country partially |
| 340 | 13.28 HCM | Customizing (SPRO) | Concurrent employment / global employment configuration | — | `PARTIAL-42` | 42 | — |
| 341 | 13.29 FI-TV | Master data & documents | TP01 / TP02 / TP03 / PR01 (travel request) | Travel request (pre-trip approval with estimated cost) | `PARTIAL-43` | 43 | Pre-trip travel request/approval with estimated cost and cash advance not named — partial; propose wave 43 "pre-approval request" |
| 342 | 13.29 FI-TV | Master data & documents | PR02 / PR05 advances (PR cash advance) | Cash advances | `PARTIAL-43` | 43 | Advances and offset against expenses not named — partial |
| 343 | 13.29 FI-TV | Customizing (SPRO) | Travel request / travel plan settings, approval workflows, substitutes | T702, SWF | `PARTIAL-43` | 43 | Pre-approval partial |
| 344 | 13.30 Basis & Security (users, roles, GRC Access Control, change documents, transports, system administration) | Master data & documents | GRC EAM firefighter (superuser privilege management with reason codes and log review) | Emergency access | `PARTIAL-48` | 48 | Break-glass elevated access with time-boxed sessions, reason codes and reviewer sign-off not explicitly named — partial |
| 345 | 13.30 Basis & Security (users, roles, GRC Access Control, change documents, transports, system administration) | Periodic processing | Anonymized production-to-test data refresh (TDMS / client copy with scrambling) | Test data management | `PARTIAL-48` | 48 | Tenant clone with PII scrambling not explicitly named — partial; propose wave 48/50 |
| 346 | 13.30 Basis & Security (users, roles, GRC Access Control, change documents, transports, system administration) | Customizing (SPRO) | Printing: device types, output devices, access methods (SPAD), front-end printing, print queues | TSP03* | `PARTIAL-32` | 32 | Print service for labels/documents; generic office print queue/printer management partial |
| 347 | 13.30 Basis & Security (users, roles, GRC Access Control, change documents, transports, system administration) | Customizing (SPRO) | Read Access Logging (SRAL) configuration | SRAL* | `PARTIAL-48` | 48 | PII access logged (wave 42) for HR; configurable read-access logging for arbitrary sensitive fields partial — propose wave 48 |
| 348 | 13.31 Cross-Application (workflow, IDoc/ALE, PI/PO, DRC e-documents, output management NAST/BRF+, Fiori launchpad, archiving/ILM, GDPR/IRF, attachments, texts, enterprise search) | Master data & documents | BRF+ (BRFPLUS / FDT_WORKBENCH) / Decision Service Management (DSM) | Business rules framework (decision tables, formulas, rulesets) | `PARTIAL-3` | 3 | Validation rules and OPA policies exist; a general-purpose, user-maintainable decision-table/rules engine (used by output, tax, credit, dispute, GRC, substitution) is implied across waves but not named as a shared service — partial; propose explicit "rules engine (decision tables)" in wave 3/4 |
| 349 | 13.31 Cross-Application (workflow, IDoc/ALE, PI/PO, DRC e-documents, output management NAST/BRF+, Fiori launchpad, archiving/ILM, GDPR/IRF, attachments, texts, enterprise search) | Master data & documents | SO10 / SO72 / SE75 (text objects/IDs) | Standard texts, text management | `PARTIAL-5` | 5 | Document/line notes exist; a reusable standard-text library with placeholders and language versions for documents is not named — partial; propose wave 46 designer text blocks |
| 350 | 13.31 Cross-Application (workflow, IDoc/ALE, PI/PO, DRC e-documents, output management NAST/BRF+, Fiori launchpad, archiving/ILM, GDPR/IRF, attachments, texts, enterprise search) | Master data & documents | SAP Business Partner Screening / Watch List Screening (GTS) | Sanctions/denied-party screening | `GAP` | propose 48 | Denied-party/sanctions screening of parties and documents not named; propose wave 48 (compliance) with provider adapter |
| 351 | 13.31 Cross-Application (workflow, IDoc/ALE, PI/PO, DRC e-documents, output management NAST/BRF+, Fiori launchpad, archiving/ILM, GDPR/IRF, attachments, texts, enterprise search) | Master data & documents | Legal Content Management / contract management (SAP CLM, Ariba Contracts) | Legal contracts | `PARTIAL-17` | 17 | Purchase/sales agreements exist; legal contract authoring, clause library, obligations — partial (noted in Ariba row) |
| 352 | 13.31 Cross-Application (workflow, IDoc/ALE, PI/PO, DRC e-documents, output management NAST/BRF+, Fiori launchpad, archiving/ILM, GDPR/IRF, attachments, texts, enterprise search) | Master data & documents | SAP Business Rules (BRF+ in GRC, tax, dispute, output) — see above | — | `PARTIAL-3` | 3 | — |
| 353 | 13.31 Cross-Application (workflow, IDoc/ALE, PI/PO, DRC e-documents, output management NAST/BRF+, Fiori launchpad, archiving/ILM, GDPR/IRF, attachments, texts, enterprise search) | Master data & documents | SAP Process Mining (Signavio) / Process Observer (POC) | Process mining | `PARTIAL-46` | 46 | Event stream + lakehouse enables mining; no native process-mining UI — partial |
| 354 | 13.31 Cross-Application (workflow, IDoc/ALE, PI/PO, DRC e-documents, output management NAST/BRF+, Fiori launchpad, archiving/ILM, GDPR/IRF, attachments, texts, enterprise search) | Customizing (SPRO) | BRF+ applications, functions, rulesets, decision tables, expressions | FDT_* | `PARTIAL-3` | 3 | Rules engine gap |
| 355 | 13.31 Cross-Application (workflow, IDoc/ALE, PI/PO, DRC e-documents, output management NAST/BRF+, Fiori launchpad, archiving/ILM, GDPR/IRF, attachments, texts, enterprise search) | Customizing (SPRO) | Watch list screening / GTS compliance configuration | — | `GAP` | propose 48 | Screening gap |
| 356 | 13.32 LO-VC | Master data & documents | CU60 / CU61 / CU62 / CU65 / CU66 / CU67 | Variant tables: structure / content / display; variant functions (ABAP) | `PARTIAL-8` | 8 | Variant tables (decision tables) map to constraint tables — covered; *variant functions* (ABAP-coded) → SUPERSEDED by expression functions; table maintenance UI not named — partial |
| 357 | 13.32 LO-VC | Master data & documents | CU51 / CU52 / CU55 / CU56 | Order BOM maintain (sales order BOM — result-oriented/knowledge-based) / display / instantiation | `PARTIAL-21` | 21 | Order-specific BOM edits after configuration (ETO) not named — partial; propose wave 21/27 "order BOM" |
| 358 | 13.32 LO-VC | Master data & documents | Customer-facing configurator (web, Fiori / SAP CPQ) | Guided selling | `PARTIAL-30` | 30 | Customer portal order entry named; guided configurator UI for portal/e-commerce not explicitly named — partial |
| 359 | 13.32 LO-VC | Master data & documents | CU7 / CUCPT (configuration copy/ templates) | Configuration templates | `PARTIAL-8` | 8 | Saved configurations/templates for reuse — partial |
| 360 | 13.32 LO-VC | Master data & documents | PSM / iPPE (Product Structure Management: product variant structures, CMP nodes, PVS) | Product structure for complex configurable products (automotive) | `PARTIAL-9` | 9 | Multi-level configurable structures covered by super BOM; iPPE-style product variant structures with alternatives/variants at node level — partial; propose wave 9 v1.x |
| 361 | 13.32 LO-VC | Master data & documents | Configuration in purchasing (configurable PO items for configurable vendor products), configuration in service (IBase configuration) | — | `PARTIAL-17` | 17 | Configured PO items not named — partial |
| 362 | 13.32 LO-VC | Master data & documents | VC with batch/serial effectivity, parameter effectivity (ECM) | Effectivity | `PARTIAL-9` | 9 | Noted in PP (serial/parameter effectivity partial) |
| 363 | 13.32 LO-VC | Periodic processing | Re-explosion of configured orders after model change (OCM) | — | `PARTIAL-21` | 21 | OCM partial (noted) |
| 364 | 13.32 LO-VC | Customizing (SPRO) | Order BOM settings (CU51 processing) | — | `PARTIAL-21` | 21 | — |
| 365 | 13.33 Batch Management & Serial Numbers | Master data & documents | DVMO / DVSP / DVMAN | Batch derivation: monitor / set up derivation (sender/receiver, rules) / manual derivation | `PARTIAL-24` | 24 | Batch derivation (inherit attribute values from component batches) partial (noted in PP-PI) |
| 366 | 13.33 Batch Management & Serial Numbers | Master data & documents | Batch valuation (split valuation per batch, OMWC valuation type = batch) | Batch-level valuation | `PARTIAL-20` | 20 | Split valuation gap |
| 367 | 13.33 Batch Management & Serial Numbers | Master data & documents | Documentary batches (S/4 "documentary batch" for non-batch-managed materials) | Documentary batches | `PARTIAL-24` | 24 | Recording batch numbers for info only (no stock segregation) not named — partial |
| 368 | 13.33 Batch Management & Serial Numbers | Master data & documents | Serial numbers at component level in finished-goods serial (as-built genealogy) | As-built records | `PARTIAL-21` | 21 | Serial-to-serial component genealogy (as-built) not explicitly named — partial; propose wave 21 RAF capture |
| 369 | 13.33 Batch Management & Serial Numbers | Reports & inquiries | DVMO (derivation monitor) | — | `PARTIAL-24` | 24 | — |
| 370 | 13.33 Batch Management & Serial Numbers | Periodic processing | Batch derivation runs (DVMO) | — | `PARTIAL-24` | 24 | — |
| 371 | 13.33 Batch Management & Serial Numbers | Customizing (SPRO) | Batch derivation: activation, sender/receiver fields, derivation events, rules | DVMO* | `PARTIAL-24` | 24 | — |
| 372 | 13.33 Batch Management & Serial Numbers | Customizing (SPRO) | Batch valuation (valuation type = batch) activation | T001K, T149 | `PARTIAL-20` | 20 | — |
| 373 | 13.33 Batch Management & Serial Numbers | Customizing (SPRO) | Documentary batch activation (S/4) | — | `PARTIAL-24` | 24 | — |
| 374 | 13.35 DMS | Master data & documents | CV01N / CV02N / CV03N | Document info record create/change/display (document type, number, part, version; status; originals; object links; classification; texts) | `PARTIAL-8` | 8 | Attachments (wave 5) and product documents (wave 8) exist; a *document info record* with type, versioning, part/version numbering, status network and release is not named — partial; propose wave 9 "engineering documents" (drawings/specs under ECO control) |
| 375 | 13.35 DMS | Master data & documents | CV04N | Find document (by attributes, classification, object links, full text) | `PARTIAL-8` | 8 | Global search covers attachments metadata; attribute/classification search on documents partial |
| 376 | 13.35 DMS | Master data & documents | CV11 / CV12 / CV13 / CV15 | Document structure (document BOM) create/change/display / where-used | `GAP` | propose 9 | Document structures (assemblies of documents, e.g. drawing sets) not named — propose wave 9 |
| 377 | 13.35 DMS | Master data & documents | CV01N → originals (check-in/check-out, KPro storage categories, viewer, conversion) | Original files (CAD, PDF, Office) with check-out locking | `PARTIAL-5` | 5 | Attachments stored; check-out/check-in locking, multiple originals per version, viewer/conversion (2D/3D) not named — partial |
| 378 | 13.35 DMS | Master data & documents | CV01N → status network (in work → released → obsolete), status with release/approval workflow, digital signature | Document status & release | `PARTIAL-9` | 9 | ECO workflow covers BOM/route release; document release state machine with signature — partial |
| 379 | 13.35 DMS | Master data & documents | CV01N → version / part / change number (ECM) | Document versioning under ECM | `PARTIAL-9` | 9 | ECO-controlled document versions partial |
| 380 | 13.35 DMS | Master data & documents | CV01N → document distribution (recipients, distribution orders CVI1 / CVI2 / CVI8) | Distribution of documents (e.g. to suppliers) | `PARTIAL-20` | 20 | Supplier portal document exchange named; controlled distribution with acknowledgement partial |
| 381 | 13.35 DMS | Master data & documents | ECTR (Engineering Control Center): CAD integration (SolidWorks, Inventor, NX, CATIA, AutoCAD, ECAD), BOM derivation from CAD, 3D viewer, DMS for CAD | CAD–ERP integration | `GAP` | propose 9 | CAD integration (CAD file management, CAD BOM → engineering BOM sync) not named; wave 9 says "PLM integration" only as reference — propose wave 9 (v1.x) PLM/CAD connector (e.g. via a PLM system) rather than native CAD management |
| 382 | 13.35 DMS | Master data & documents | Document BOM items (CS01 item category D) | Documents as BOM components (e.g., drawings, instructions) | `PARTIAL-9` | 9 | BOM line type "document" not named — partial |
| 383 | 13.35 DMS | Master data & documents | PLM: Engineering Record (change management cockpit), Product Structure Management, Visual Enterprise (3D visualization), Recipe Development (PLM-RDP), Specification management (EHS specs), Labeling (GLM) | PLM suite | `PARTIAL-9` | 9 | ECR/ECO covered; recipe development with formula trials/specs (process industries), specification management, global label management — partial; propose wave 24 (recipe dev) & wave 48 (labeling compliance) |
| 384 | 13.35 DMS | Master data & documents | Fiori: Manage Documents / Manage Document Info Records | — | `PARTIAL-8` | 8 | — |
| 385 | 13.35 DMS | Reports & inquiries | CV04N | Document search | `PARTIAL-8` | 8 | — |
| 386 | 13.35 DMS | Reports & inquiries | CV13 / CV15 | Document structure display / where-used | `GAP` | propose 9 | — |
| 387 | 13.35 DMS | Reports & inquiries | CVI8 (distribution log) | Distribution status | `PARTIAL-20` | 20 | — |
| 388 | 13.35 DMS | Reports & inquiries | ECTR reports (CAD BOM comparison) | — | `GAP` | propose 9 | — |
| 389 | 13.35 DMS | Periodic processing | CV conversion runs (originals → PDF/neutral formats) | Conversion server runs | `PARTIAL-5` | 5 | Preview generation for attachments — partial |
| 390 | 13.35 DMS | Periodic processing | Document distribution runs (CVI) | — | `PARTIAL-20` | 20 | — |
| 391 | 13.35 DMS | Periodic processing | ECTR sync jobs | — | `GAP` | propose 9 | — |
| 392 | 13.35 DMS | Customizing (SPRO) | DC10 — Document types (number assignment, version/part numbering, status network, object links, screen layout, classification class, originals count, versioning) | TDWA, TDWS, TDWO, TDWP | `PARTIAL-8` | 8 | Attachment types exist; full document type model partial |
| 393 | 13.35 DMS | Customizing (SPRO) | DC20 — Status (document status definitions, release flag, content version) | TDWS | `PARTIAL-9` | 9 | — |
| 394 | 13.35 DMS | Customizing (SPRO) | DC30 — Workstation applications (file types, viewer/editor, conversion) | TDWP | `PARTIAL-5` | 5 | Preview/viewer mapping partial |
| 395 | 13.35 DMS | Customizing (SPRO) | Document distribution customizing (recipient lists, distribution types) | — | `PARTIAL-20` | 20 | — |
| 396 | 13.35 DMS | Customizing (SPRO) | ECTR configuration (CAD applications, templates, BOM sync rules) | — | `GAP` | propose 9 | — |
| 397 | 13.35 DMS | Customizing (SPRO) | Digital signature for documents (signature strategies) | — | `GAP` | propose 24 | E-signature framework gap (noted in PP-PI) |
| 398 | 13.35 DMS | Customizing (SPRO) | Full-text search activation (TREX/HANA) for originals | — | `PARTIAL-5` | 5 | Attachment content indexing not named — partial |
| 399 | 13.36 Analytics | Master data & documents | Data quality dashboards (Information Steward) | DQ monitoring | `PARTIAL-46` | 46 | Metric reconciliation checks exist; generic data-quality rule dashboards (completeness, validity per entity) partial — propose wave 46 DQ rules on catalog |
| 400 | 13.36 Analytics | Customizing (SPRO) | Export governance (Excel export restrictions, download audit) | — | `PARTIAL-48` | 48 | Export of PII/financial data audit & restriction policies not named — partial; propose wave 48 |
| 401 | 13.37 AI | Master data & documents | SAP Signavio Process AI / Process Insights (process mining recommendations) | Process intelligence | `PARTIAL-46` | 46 | Process mining partial (noted §13.31) |
