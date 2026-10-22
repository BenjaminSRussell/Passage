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

Status totals across all four tables of every section (computed from the rows, excluding §13.99):

{{STATUS_TOTALS}}

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
| FAGL_SCHEDULE_LOG? (FAGLCOFITRACEADMIN) | Trace for CO-FI real-time integration | SUPERSEDED-11 | 11 | No CO→FI reconciliation needed: single journal |
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
| FINS_CO_RECON? (FINS_RECON_RC0..RC3) | S/4 reconciliation reports (ACDOCA vs. BSEG/COEP) | SUPERSEDED-11 | 11 | No dual tables to reconcile; balance projection vs journal check is the equivalent |
| FINSC_LEDGER (admin run) | Ledger settings activation | COVERED-12 | 12 | Ledger group configuration |
| CLOCO / CLOCOC / CLOCOS | Financial Closing Cockpit: task list / templates / monitor | COVERED-14 | 14 | Close checklist with templates, dependencies, assignees, status board |
| SCMA | Schedule Manager (classic) | SUPERSEDED-14 | 14 | Close checklist + wave 4 batch scheduler |
| Fiori: Manage Closing Tasks / Advanced Financial Closing | S/4 close orchestration | COVERED-14 | 14 | Close checklist across entities |
| SAFT / Fiori: Run Statutory Reports (ACR) | Advanced Compliance Reporting (SAF-T, VAT returns) | COVERED-13 | 13 | SAF-T extract validated to OECD XSD; VAT returns; statutory packs in wave 48 |
| FAGL_SCHEDULE? (FAGL_PLAN_*) | Plan data entry in New GL (GP12N etc.) | COVERED-38 | 38 | Budget register against ledger dimensions |
| GP12N / GP12NA | Plan data entry / display (New GL) | COVERED-38 | 38 | — |
| FAGL_CO_PLAN | Transfer CO plan data to New GL | SUPERSEDED-38 | 38 | One planning store |
| S_ALR_87099918? (RFUMSV00 run) | Advance tax return run | COVERED-13 | 13 | Tax settlement job |

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
| OB59 / OBBD? — Valuation methods and valuation areas (FC valuation) | T044A, T044G, FAGL_VAL_AREA? | Lowest value principle, always valuate, reset, exchange rate type per method; valuation area ↔ accounting principle | COVERED-12 | 12 | Revaluation method per ledger / accounting principle |
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
| OBD5? / Define sort keys (allocation field) | TZUN | Assignment field fill rule | SUPERSEDED-11 | 11 | Sort/assignment via configurable reference fields and saved views |
| OBR1 / OBR2 / OBR3 — Reset transaction data / delete master data / delete customer-vendor | — | Test-client tooling | SUPERSEDED-1 | 1 | Environment reset through pipeline/tenant provisioning |
| FINSC_FIELDSTATUS? / Define field status for ledger (S/4) | — | — | COVERED-3 | 3 | Metadata |
| OB74 — Additional rules for automatic clearing | TF123 | Criteria fields per account type/range | PARTIAL-11 | 11 | GL OI gap (AP/AR criteria covered by settlement rules) |
| OBB1 / OBB2? — Define accounts for bank interest / tax on cash discount | T030 | — | COVERED-13 | 13 | — |
| SNUM / SNRO (FI number range objects) | NRIV | RF_BELEG, etc. | COVERED-5 | 5 | Number sequences |
| OBBG — Assign country to tax calculation procedure | T005 | TAXUSJ, TAXUSX, TAXEUR… | COVERED-13 | 13 | Jurisdiction hierarchy per country |
| OBH1 — Copy number range intervals to fiscal year | NRIV | — | COVERED-5 | 5 | Year-dependent sequences |
| Define Reasons for Reversal (OB_REVERSE? / T041C) | T041C | Negative posting allowed, alternative posting date | COVERED-11 | 11 | Reversal reason codes |
| FAGL_CL_RGRP? / Define sort method & adjustment accounts for regrouping | T043?/ T030 | Remaining term intervals | GAP | propose 14 | FAGLF101 gap |
| Define Posting Keys for Clearing (OBXH etc.) | T030 | — | SUPERSEDED-11 | 11 | No posting keys |
| OBD4 → "Define Screen Layout per Company Code" | T001 | — | COVERED-3 | 3 | — |
| Cost of sales accounting: functional areas (OKBD), activate (FAGL_FUNC_AREA?) | TFKB, T001 | — | PARTIAL-11 | 11 | Functional area dimension + derivation rules (substitution gap) |
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
| F-40 / F-36? (AP side: FBW6?) | Bill of exchange payment (AP) | Bills of exchange payable | GAP | propose 36 | Bills of exchange (AP and AR) are not in any wave; relevant in IT/ES/FR/JP/BR; propose as a wave 36 (bank) option or defer to a localization v1.x pack under wave 48 — needs a decision |
| FB10 | Invoice/credit memo fast entry | Multi-document fast entry | COVERED-18 | 18 | Grid entry / import |
| FBL1N (as document finder) | — | — | COVERED-19 | 19 | — |
| FCHN / FCH5 / FCH6 | Check register / create manual check / change check info | Check management | COVERED-36 | 36 | Check management (void/reprint), check register |
| FCH7 / FCH8 / FCH9 | Reprint / cancel payment + check / void check | — | COVERED-36 | 36 | — |
| FCHR / FCHT | Online cashed checks / assign checks to payments | — | COVERED-36 | 36 | Bank statement cashed-check matching |
| FCHU / FCHX | Create reference for check / check extract (positive pay) | Positive pay file | COVERED-19 | 19 | Positive pay export |
| F110 (document side: payment documents) | — | — | COVERED-19 | 19 | — |
| FBZ5 | Print check for payment document | — | COVERED-19 | 19 | Check printing |
| FBZ8? / FBZP | — | See customizing | — | — | — |
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
| S_ALR_87012176? (AP aging via RFKOPR00) | Vendor open item aging | COVERED-19 | 19 | — |
| S_ALR_87012116? | Bill of exchange list | GAP | propose 36 | Follows bills gap |
| FBL1N variant: withholding tax | Withholding tax line items | COVERED-13 | 13 | — |
| S_ALR_87012111? / S_P00_07000134 | Withholding tax report / 1099-MISC | COVERED-13 | 13 | — |
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
| SAPF110V? (F110 proposal log) | Payment proposal log | COVERED-19 | 19 | — |
| FBZ0 | Edit payment proposal (display/change) | COVERED-19 | 19 | — |
| F150 (AP dunning) | Dunning vendors (debit balances) | PARTIAL-30 | 30 | Dunning covers customers; dunning vendors with debit balances is an edge case — reuse engine |
| FAGL_FC_VAL (AP) | FX valuation of vendor open items | COVERED-12 | 12 | — |
| FAGLF101 (AP) | Regroup vendors with debit balances | GAP | propose 14 | Reclassification gap |
| F.98 | Vendor balance confirmation | GAP | propose 19 | Balance confirmations gap |
| MRRL (ERS — see MM-IV) | Evaluated receipt settlement | COVERED-18 | 18 | — |
| J1INCHLN run / withholding tax remittance | WHT payment to authority | COVERED-13 | 13 | Tax settlement for withholding |
| S_P00_07000134 (year-end 1099 run) | 1099 generation | COVERED-13 | 13 | — |
| F.07 (AP) | Carry forward vendor balances | COVERED-14 | 14 | — |
| FBWE? (AP bill of exchange presentation) | Bill of exchange presentation | GAP | propose 36 | Bills gap |
| Fiori: Schedule Accounts Payable Jobs | Job templates (payment proposal, correspondence) | COVERED-4 | 4 | Job framework |
| Fiori: Manage Automatic Payments | S/4 payment run app | COVERED-19 | 19 | — |
| FPAYM? / FBPM payment advice | Payment advice generation (REMADV) | COVERED-19 | 19 | Vendor payment advice email/portal |
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
| Classic withholding tax (OBCS? / T059Q) | T059Q | — | SUPERSEDED-13 | 13 | Extended model only |
| OBZT — Tax codes allowed in FB60/FB70 | T007V? | Per transaction | COVERED-13 | 13 | Allowed tax codes per document type |
| OBCL — Default tax code for non-taxable transactions | T001 | — | COVERED-13 | 13 | — |
| OB71 — Define reason codes for payment block / OB27 | T008 | Block for payment, manual/automatic | COVERED-19 | 19 | Payment holds |
| OBB9 — Installment terms | T052R | — | PARTIAL-7 | 7 | Installment gap |
| Define alternative payee / permitted payee (BP role) | LFA1-LNRZA, LFZA | — | COVERED-7 | 7 | Alternate payee on vendor role |
| Define correspondence types / forms (OB77, OB78, OB79) | T048, T048B | — | PARTIAL-19 | 19 | Correspondence framework gap |
| Define payment advice forms (SAPscript/PDF) | — | — | COVERED-19 | 19 | Payment advice templates; wave 46 designer |
| Check lots (FCHI) / check number ranges | PCEC | Lot per house bank/account | COVERED-36 | 36 | Check lots |
| OBYP? / OBA7 (vendor doc types KR, KG, KZ, KA) | T003 | — | COVERED-11 | 11 | Document types |
| Define sensitive fields for dual control (S_ALR_87003378 / T055F) | T055F | Fields requiring confirmation | COVERED-7 | 7 | Change approval on sensitive fields |
| OBXS? — Down payment request accounts | T030 | — | COVERED-19 | 19 | — |
| Define vendor activity groups? (no) — Define payment block reasons | T008 | — | COVERED-19 | 19 | — |
| Evaluated receipt settlement defaults (vendor master ERS flag, OMRR?) | LFM1 | — | COVERED-18 | 18 | — |
| OBXF? — Posting keys for AP (OBXK group) | TBSL | — | SUPERSEDED-11 | 11 | — |
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
| F-34? / F-36 / F-33 | Bill of exchange payment / discount (AR) | — | GAP | propose 36 | Bills of exchange gap |
| FBE1 / FBE2 / FBE3 | Payment advice create/change/display | Remittance advices for matching | COVERED-30 | 30 | Remittance advice ingestion and matching |
| FBE6? | Delete payment advices | — | COVERED-30 | 30 | — |
| FLB1 / FLB2 | Postprocess lockbox / import lockbox file (BAI2) | Lockbox | COVERED-30 | 30 | Lockbox BAI2 |
| FEBA_LOCKBOX | Postprocess lockbox (New) | — | COVERED-30 | 30 | — |
| F-21 / F-30 | Transfer posting (customer) | — | COVERED-30 | 30 | — |
| FDM1? / UDM_DISPUTE | Create dispute case (FSCM-DM) | — | COVERED-30 | 30 | Disputes workflow |
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
| S_ALR_87012215 | Customer sales per period? (RFDUML00) | COVERED-46 | 46 | — |
| F.21? / S_ALR_87012218? | Interest scale / interest calculation list | COVERED-30 | 30 | — |
| F.31 | Credit management overview | COVERED-27 | 27 | Credit exposure |
| F.32? / FDK43 | Credit management: missing data / master data list | COVERED-27 | 27 | — |
| FCV3? / S_ALR_87012215 (credit) | Credit overview (SD) | COVERED-27 | 27 | — |
| UKM_COMMITMENTS / UKM_LOGS_DISPLAY | FSCM credit exposure / logs | COVERED-27 | 27 | — |
| UDM_SUPERVISOR / UDM_SPECIALIST | Collections worklists (FSCM-COL) | COVERED-30 | 30 | Collections workspace |
| UDM_DISPUTE (list) | Dispute case list | COVERED-30 | 30 | — |
| S_ALR_87012085? (customer) / F.23? | Dunning history | COVERED-30 | 30 | — |
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
| UKM_MALUS_DSP / UKM_MASS? / UKM_CREDIT_LIMIT_MASS? | Mass credit limit recalculation / scoring run | COVERED-30 | 30 | Risk scoring batch |
| F.28? / FD24 mass | Reset credit limit data / recalculate | COVERED-27 | 27 | — |
| UDM_BP_PROF? / UDM_GENWL | Generate collections worklist (FSCM-COL) | COVERED-30 | 30 | Worklist generation |
| F110 (customer direct debits) | Direct debit run (SEPA SDD) | COVERED-30 | 30 | Customer direct debit via payment adapter / pain.008 — confirm pain.008 is in wave 19 formats (wave 19 lists pain.001 only) |
| FDTA (DD) | DME admin for direct debits | COVERED-19 | 19 | — |
| SEPA mandate management (FSEPA_M1..M4) | Mandates | PARTIAL-30 | 30 | SEPA direct-debit mandates not named; required for EU customers paying by direct debit — add mandate entity to wave 30 |
| VF44 / VF45? (see SD) | Revenue recognition run (classic) | COVERED-29 | 29 | — |
| FBWD? / FBWE (AR BoE) | Bill of exchange presentation | GAP | propose 36 | Bills gap |
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
| OBL6? / OB61 — Dunning areas / block reasons | T047M, T040S | — | COVERED-30 | 30 | — |
| OB46 / OB82 / OB83 — Interest indicators, calculation types, reference rates | T056*, T056P, T056R | Item/balance interest, calendar, rounding | COVERED-30 | 30 | AR interest; GL interest is a gap |
| OB45 — Credit control areas (classic) | T014 | Credit limit currency, update group, risk categories | SUPERSEDED-27 | 27 | FSCM-style credit segments modeled as credit profiles per legal entity/group |
| OVA8 — Automatic credit control (SD) | T691F | Static/dynamic check, horizon, open orders/deliveries, reactions | COVERED-27 | 27 | Credit rules (limit, aging, dynamic horizon) |
| OB01 — Risk categories / OVFL credit groups | T691A, T691B | — | COVERED-27 | 27 | — |
| UKM_* FSCM credit customizing (credit segments, rules, scoring formulas, BRF+ rules) | UKMCRED_SGM*, UKM_* | Score formulas, limit rules, check rules per segment | COVERED-27 | 27 | Credit profiles + scoring (wave 30) |
| UDM_* collections customizing (collection strategies, segments, profiles, priorities, worklist rules) | UDM_* | Strategy rules (BRF), priorities, groups | COVERED-30 | 30 | Collection strategies |
| UDM_* dispute case customizing (case types, attributes, status, reasons, RMS) | SCMG_* | — | COVERED-30 | 30 | Disputes |
| OBB9 — Installment terms (customers) | T052R | — | PARTIAL-7 | 7 | Installment gap |
| OB10? / Lockbox control (OBAY, OBAX) | T049A, T049B, T049L | Lockbox destination, BAI format, posting rules | COVERED-30 | 30 | — |
| OBXL / OBBE — Payment difference reason codes and accounts | T053R, T030 | — | COVERED-30 | 30 | — |
| Define payment advice types / notes to payee (OBBE?) | T053? | — | COVERED-30 | 30 | — |
| Define correspondence types (OB77) / periodic account statements | T048 | — | PARTIAL-30 | 30 | Correspondence framework gap |
| SEPA mandate customizing | SEPA_* | — | PARTIAL-30 | 30 | Mandate gap |
| Define alternate payer / head office–branch | KNA1-KNRZE, KNB1 | Head office/branch accounting | PARTIAL-7 | 7 | Head-office/branch (invoice to branch, pay from HQ) not named in wave 7 party roles — add "bill-to/pay-from parent" relationship |
| OBYR? (customer side) sales/FI account determination — see VKOA | T030, C001.. | — | COVERED-29 | 29 | Revenue account determination |
| Define reason codes for credit holds (OVA8/ OVAK) | T691* | — | COVERED-27 | 27 | — |
| OB38? — Assign credit control area to company code | T001 | — | SUPERSEDED-27 | 27 | — |
| Define cash discount base, tax on discount (OBY6) | T001 | — | COVERED-13 | 13 | — |
| Define dunning forms (SAPscript F150_DUNN_01) | — | — | COVERED-30 | 30 | Letter templates; wave 46 designer |

---
