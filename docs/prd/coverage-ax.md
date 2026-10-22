# Part 12 — Coverage Inventory: Dynamics AX 2012 R3 / D365 F&SCM → Passage

| | |
|---|---|
| **Status** | Draft v0.1 — completeness oracle for the 50-wave roadmap |
| **Date** | 2026-10-21 |
| **Source of truth for waves** | [roadmap/waves.json](../../roadmap/waves.json) (wave numbers 1–50); [roadmap/phases.json](../../roadmap/phases.json) |
| **Scope rules** | [docs/PRD.md](../PRD.md) §2 goals and non-goals, §5 scope map |
| **Purpose** | The team's fear is "missing something". This part walks the AX 2012 R3 navigation pane module by module (plus the D365 F&SCM additions that matter to a manufacturer) and assigns every form, inquiry, report, periodic job and setup form to a Passage wave — or says, honestly, that nothing covers it yet. |

## 12.0 How to read this inventory

Each module section mirrors the AX 2012 R3 menu structure and has four tables:

1. **Common / Journals** — every list page, details form and journal the user opens from *Common* or *Journals*. Columns: Form name | AOT form or menu item (best effort from memory of the AX 2012 R3 AOT; treat as a search key, not gospel) | Purpose | Passage status | Wave | Notes.
2. **Inquiries & Reports** — every *Inquiries* entry and every SSRS report under *Reports*. Columns: Name | Purpose | Passage status | Wave | Notes.
3. **Periodic** — every periodic job or process. Same columns as 2.
4. **Setup** — every setup form, with the module's *parameters* form listed first and its tabs spelled out as separate rows where the tab changes behavior that a wave must implement. Columns: Name | Key parameters / options | Passage status | Wave | Notes.

**Status legend** (the *Passage status* column always starts with one of these tokens so the document can be counted mechanically):

- `COVERED-<wave>` — a deliverable in waves.json for that wave clearly implements the AX capability, at parity or better.
- `PARTIAL-<wave> (what's missing)` — the wave covers the core but a named piece of the AX feature is not in any wave deliverable. The missing piece is listed in the gap register (§12.99) with a proposed wave.
- `GAP` — no wave in waves.json clearly covers the item. The Notes column always begins with `Propose wave NN:` so the gap register can be generated from the rows. Wave column shows `—`.
- `OUT-OF-SCOPE (reason)` — excluded by a PRD non-goal (CRM/marketing automation, retail POS/e-commerce, country packs beyond US/UK/DE/FR/IN/MX/CA, X++/ABAP code migration) or because AX itself deprecated it before R3. Wave column shows `—`.
- `SUPERSEDED (how)` — Passage deliberately does it a different way; the replacing mechanism is named. Wave column shows the wave that ships the replacement.

**Conventions**

- Wave numbers refer to `n` in waves.json. Phase boundaries: 0 = 1–5, 1 = 6–10, 2 = 11–15, 3 = 16–20, 4 = 21–25, 5 = 26–30, 6 = 31–35, 7 = 36–40, 8 = 41–45, 9 = 46–50.
- "SSRS report" rows are assessed against two things: the *data* (does a Passage wave produce it?) and the *rendering* (wave 46 replaces every SSRS layout with the operational report designer and embedded dashboards). A report is `COVERED-<n>` when the data wave exists and wave 46 renders it; it is `PARTIAL` only when the data itself is missing.
- Forms that are shared across modules (Terms of payment, Methods of payment, Charges codes, Print management, Form setup, Number sequences) are listed in every module where AX shows them, so each module section is self-contained for its reviewer. They map to the same wave each time.
- Where AX 2012 R3 and D365 F&SCM differ (e.g. Collections, Credit management, Cost accounting ledger, Landed cost), the D365 name is given in Notes and the row is assessed against the D365 behavior because that is what a manufacturer migrating today expects.
- Country-specific forms are listed only where they belong to one of the seven v1 country packs; everything else is `OUT-OF-SCOPE (country pack beyond v1 set)`.

## 12.1 General ledger

The general ledger is the heart of AX. In Passage it is the universal journal (wave 11) plus currencies/parallel ledgers (12), tax (13), period close (14) and financial reporting (15). Consolidation moves to wave 40; budgeting to wave 38.

### 12.1.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Main accounts (list page) | MainAccountListPage | Browse chart of accounts with balances FactBox | COVERED-11 | 11 | Metadata-driven list page with balance projection FactBox |
| Main account details | MainAccount | Account type, category, DB/CR default, suspended, validation (currency/user/posting type), allocation terms, default dimensions, translations | COVERED-11 | 11 | Account structure validation rules at posting API |
| Chart of accounts | LedgerChartOfAccounts | Shared COA header; main accounts belong to a COA shared by ledgers | COVERED-11 | 11 | Shared COA across legal entities (tenant-level) |
| Ledger | Ledger | Per-legal-entity ledger: COA, fiscal calendar, accounting/reporting currency, exchange rate types, account structures, balance control | COVERED-11 | 11 | Ledger service entity; parallel ledgers in 12 |
| Financial dimensions | DimensionAttribute | Define dimensions backed by entities (dept, cost center, customer…) or custom lists | COVERED-11 | 11 | Up to 20 dims per ledger |
| Financial dimension values | DimensionAttributeValue (DimensionValueDetails) | Values with active dates, suspended, owner, group dimension | COVERED-11 | 11 | Effective-dated values |
| Financial dimension sets | DimensionFocus | Balance views by dimension set, rebuild balances | COVERED-11 | 11 | Balance projection by account × dimension × period (event-sourced) |
| General journal (list) | LedgerJournalTable (menu LedgerJournalTable) | List of journal batches by name with status/approval | COVERED-11 | 11 | Journal workspace |
| General journal lines | LedgerJournalTransDaily | Voucher lines, account/offset, dimensions, tax, invoice, due date, payment info | COVERED-11 | 11 | |
| Journal voucher (lines) — Fixed assets journal | LedgerJournalTransAsset | FA transactions from GL journal | COVERED-37 | 37 | FA transaction types posted through journal |
| Periodic journals | LedgerJournalPeriodic | Recurring journal templates with period unit, last date, end date | COVERED-14 | 14 | Recurring journal scheduling |
| Allocation journal | LedgerJournalTable (type Allocation) | Journal created by allocation rule processing, review before post | COVERED-14 | 14 | Allocation engine output |
| Elimination journal | LedgerJournalTable (type Elimination) | Journal created by elimination proposals in consolidation company | COVERED-40 | 40 | |
| Intercompany journal | LedgerJournalTable (intercompany lines) | Lines posting due-to/due-from across legal entities | COVERED-40 | 40 | Due-to/due-from entries wave 40; posting API wave 11 |
| Global general journal (D365) | LedgerJournalTable (global) | Enter journals for any legal entity without company switch | COVERED-11 | 11 | Shell re-scopes per legal entity; cross-entity journal entry in workspace |
| Journal approval (workflow) | LedgerJournalTable + workflow | Submit journal for approval | COVERED-11 | 11 | Temporal approval template (wave 4) applied to journals in 11 |
| Posted journals (list) | LedgerJournalTable (posted) | Review posted batches, reverse | COVERED-11 | 11 | Reversal creates linked negating entry |
| Voucher attachments / document handling | DocuView | Attach documents to vouchers | PARTIAL-11 (no document types, archiving policy or external store yet) | 11 | Attachments named in wave 11; general document management is a gap under Org admin §12.29 |
| Advanced ledger entries (public sector) | LedgerAdvancedEntry (PSA) | Multi-line entries with posting definitions | OUT-OF-SCOPE (public sector feature set is not in the PRD scope map) | — | Public sector is not a target vertical |
| Journal names list | LedgerJournalName | Also reachable from Common in some role centers | COVERED-11 | 11 | See Setup |
| Reverse transaction (from voucher) | LedgerTransReverse | Reverse one voucher / trace reversal | COVERED-11 | 11 | |
| Reverse entire journal | LedgerJournalTableReverse | Reverse all vouchers of a batch | COVERED-11 | 11 | |
| Fiscal calendars (Common link in R3) | FiscalCalendars | Shared calendars, years, periods | COVERED-6 | 6 | Owned by Org admin in Passage |

### 12.1.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Trial balance (inquiry, dimension set) | Opening/debit/credit/closing by account and dimension set, date range | COVERED-15 | 15 | Drill-down to lines |
| Ledger transactions (Voucher transactions) | Lines by voucher with origin trace | COVERED-11 | 11 | Journal lines inquiry with source-document link |
| Audit trail | Who posted which voucher when, journal/source | COVERED-15 | 15 | Audit trail report (wave 15) over CDC audit (wave 5) |
| Transaction origin | Drill from a GL line to originating subledger document | COVERED-15 | 15 | Statement → balance → lines → source document |
| Posted sales tax (Sales tax transactions inquiry) | Tax amounts by code/period | COVERED-13 | 13 | |
| Settled sales tax (sales tax payments inquiry) | Settlement history | COVERED-13 | 13 | |
| Ledger budget transactions inquiry | Budget register entries by account | COVERED-38 | 38 | |
| Currency exchange rate inquiry | Rates by type/pair/date | COVERED-12 | 12 | Rate audit |
| Dimension statement (Dimension balance inquiry) | Balances for a single dimension value | COVERED-11 | 11 | Balance projection query |
| Closing sheet (periodic, country-dependent) | Closing adjustments worksheet (used in ES/IT/EE) | OUT-OF-SCOPE (country pack beyond v1 set; closing adjustments in Passage are allocation/accrual journals) | — | |
| Ledger reconciliation inquiry (Inventory/ledger, AR/AP) | Compare subledger to GL | COVERED-20 | 20 | Daily automated subledger-to-GL reconciliation from wave 16; inventory value report wave 20 |
| SSRS: Trial balance | Printed trial balance | COVERED-15 | 15 | Rendered via wave 46 designer |
| SSRS: Trial balance summary | Summary by account | COVERED-15 | 15 | |
| SSRS: Ledger transaction list | Lines by date range | COVERED-11 | 11 | |
| SSRS: Ledger account balance | Balance per account/period | COVERED-15 | 15 | |
| SSRS: Dimension statement | Dimension-based P&L/balance | COVERED-15 | 15 | |
| SSRS: Financial statement (old LedgerBalanceSheet row/column definitions) | Legacy AX statement engine | SUPERSEDED (report definitions in wave 15 designer, Management Reporter model) | 15 | AX 2012 had both; Passage keeps only the MR-style model |
| SSRS: Chart of accounts | List of accounts with attributes | COVERED-11 | 11 | |
| SSRS: Journal (LedgerJournal report) | Unposted journal printout | COVERED-11 | 11 | |
| SSRS: Posted journals | Posted batch printout | COVERED-11 | 11 | |
| SSRS: Voucher (LedgerTransVoucher) | Single voucher with all lines | COVERED-11 | 11 | |
| SSRS: Audit trail | Printed audit trail | COVERED-15 | 15 | |
| SSRS: Ledger reconciliation (Ledger/Inventory, Ledger/AR, Ledger/AP, Ledger/Project, Ledger/Bank) | Reconciliation listings | COVERED-20 | 20 | Reconciliation checks per subledger, wave 20 report; bank in 36; project in 41 |
| SSRS: Sales tax payments | Settlement report | COVERED-13 | 13 | |
| SSRS: Sales tax specification by ledger transaction | Tax by voucher | COVERED-13 | 13 | |
| SSRS: Sales tax transactions — details | Tax detail listing | COVERED-13 | 13 | |
| SSRS: Sales tax list / Sales tax codes | Setup listing | COVERED-13 | 13 | |
| SSRS: EU sales list | Statutory EU sales listing | COVERED-13 | 13 | |
| SSRS: Exchange rate (Currency exchange rates) | Rate table printout | COVERED-12 | 12 | |
| SSRS: Fiscal year close report / Opening balances | Closing results | COVERED-14 | 14 | |
| SSRS: Consolidation (Consolidated balances) | Consolidated trial balance | COVERED-40 | 40 | |
| SSRS: Eliminations | Elimination detail | COVERED-40 | 40 | |
| SSRS: Allocation rule report | Allocation definitions | COVERED-14 | 14 | |
| SSRS: Journal names, Posting setup listings | Setup listings | COVERED-11 | 11 | Any entity can be exported/printed from the shell (wave 5) |
| SSRS: Cash flow forecast (GL cash flow) | Cash flow forecast from open items | COVERED-36 | 36 | |
| SSRS: Balance control (balance sheet with balance control accounts) | Control accounts | COVERED-15 | 15 | |
| SSRS: Fiscal LIFO reports (Italy) | Italian fiscal LIFO valuation | OUT-OF-SCOPE (country pack beyond v1 set) | — | |
| SSRS: Withholding tax reports | Withholding by vendor/period | COVERED-13 | 13 | 1099 in AP |
| Management Reporter / Financial reporting (D365) | Row/column/tree report designer and viewer | COVERED-15 | 15 | |

### 12.1.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Journals → Periodic journals (retrieve) | Generate journal lines from periodic journal template | COVERED-14 | 14 | |
| Journals → Allocation journal (Process allocation request) | Run allocation rules, create allocation journal | COVERED-14 | 14 | Fixed, basis, iterative |
| Journals → Accrual schemes (Ledger accruals) | Spread an expense/revenue across periods | COVERED-14 | 14 | |
| Journals → Ledger accrual (from journal line) | Apply scheme on a journal line | COVERED-14 | 14 | |
| Fiscal year close → Fiscal year close (year-end close) | Transfer P&L to retained earnings, opening balances | COVERED-14 | 14 | Per-dimension carry-forward |
| Fiscal year close → Opening transactions | Create opening transactions per account/dimension | COVERED-14 | 14 | |
| Fiscal year close → Transfer opening balances (D365 year-end close templates) | Templates for retained earnings by dimension | COVERED-14 | 14 | |
| Consolidate → Consolidate [Online] | Pull subsidiary balances into consolidation company | COVERED-40 | 40 | |
| Consolidate → Consolidate [Export] / [Import] | File-based consolidation | COVERED-40 | 40 | Import path for non-Passage subsidiaries |
| Consolidate → Elimination proposal | Generate elimination journal | COVERED-40 | 40 | |
| Currency revaluation (Foreign currency revaluation, GL) | Unrealized gain/loss on monetary accounts | COVERED-12 | 12 | Reversal next period |
| Currency revaluation → Reporting currency adjustment (D365) | Restate reporting currency balances | COVERED-12 | 12 | Currency translation |
| Transaction reversal (Reverse transaction / Trace reversal) | Reverse posted vouchers | COVERED-11 | 11 | |
| Sales tax payment (Settle and post sales tax) | Settle tax period to authority | COVERED-13 | 13 | |
| Sales tax → Sales tax report / EU sales list / VAT return | Statutory extracts | COVERED-13 | 13 | VAT return, EU sales list, Intrastat, SAF-T |
| Conditional sales tax settlement | Settle tax when payment made (cash basis) | COVERED-13 | 13 | Rules engine supports cash-basis trigger; verify fixture |
| Ledger balances rebuild (Financial dimension sets balance update) | Rebuild balance table | COVERED-11 | 11 | Balance projection rebuild from journal |
| Close period (Ledger calendar period status change) | Open/on hold/closed per module | COVERED-14 | 14 | Period control per ledger × module × user group |
| Financial period close workspace (D365) | Close checklist, tasks, dependencies | COVERED-14 | 14 | |
| Delete / Compress transactions (Ledger transaction compression) | Compress old GL transactions | SUPERSEDED (append-only journal; history archived to lakehouse wave 46, never compressed in place) | 46 | |
| Reason code assignment (reversal reasons) | Require reasons on reversal | PARTIAL-11 (reason codes as a shared framework are not named in any wave) | 11 | See Org admin §12.29 reason codes |
| Intercompany journal post (periodic) | Post pending intercompany lines | COVERED-40 | 40 | |
| Advanced ledger entries (public sector posting definitions) | | OUT-OF-SCOPE (public sector) | — | |
| Fiscal LIFO calculation (IT) | | OUT-OF-SCOPE (country pack beyond v1 set) | — | |
| Ledger budget: transfer budget to ledger (old budget model) | | SUPERSEDED (budget register entries, wave 38) | 38 | |
| Financial reports generation (MR schedule) | Scheduled report packs | COVERED-15 | 15 | Scheduled distribution PDF/XLSX/email |
| Export ledger (SAF-T, audit file) | Statutory audit file | COVERED-13 | 13 | SAF-T extract; other country audit files in wave 48 |

### 12.1.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| General ledger parameters — Ledger tab | Check for voucher used, correction (storno), journal-based accrual, transaction reversal, batch transfer rules, use document date for voucher, balance control | COVERED-11 | 11 | Storno (negative debit) handled as posting option |
| General ledger parameters — Sales tax tab | Apply sales tax taxation rules, cash discount on invoice incl. tax, tax on prepayment, rounding, conditional sales tax, sales tax settlement account, reverse charge | COVERED-13 | 13 | |
| General ledger parameters — Inventory dimensions tab | Dimensions shown on ledger lines | COVERED-16 | 16 | |
| General ledger parameters — Number sequences tab | Voucher sequences for journals, tax settlement | COVERED-5 | 5 | Number sequence service |
| General ledger parameters — Batch transfer rules (D365 summarize subledger) | Asynchronous subledger transfer, summarize by account | SUPERSEDED (subledgers post synchronously via the universal journal API; summarization is a projection) | 11 | |
| Ledger | COA, calendar, currencies, account structures, balance control | COVERED-11 | 11 | |
| Chart of accounts → Chart of accounts | Shared COA | COVERED-11 | 11 | |
| Chart of accounts → Main accounts | Accounts | COVERED-11 | 11 | |
| Chart of accounts → Main account categories | Categories for reporting/cubes | COVERED-15 | 15 | Used by statement designer and wave 46 semantic layer |
| Chart of accounts → Account structures (Configure account structures) | Segments, allowed values, constraints, activation | COVERED-11 | 11 | |
| Chart of accounts → Advanced rule structures / Advanced rules | Conditional additional segments | PARTIAL-11 (advanced rules (conditional extra segments) are not explicitly listed; account structures are) | 11 | Validation rules may need conditional branch |
| Chart of accounts → Account structure activation (periodic) | Activate draft structures | COVERED-11 | 11 | |
| Chart of accounts → Financial dimensions | | COVERED-11 | 11 | |
| Chart of accounts → Financial dimension sets | | COVERED-11 | 11 | |
| Chart of accounts → Derived financial hierarchies (R3) | Category hierarchies used to derive dimension groupings for reporting | PARTIAL-15 (derived hierarchies for reporting are not named) | 15 | Report tree designer likely covers; confirm |
| Chart of accounts → Default dimension templates (D365) | Templates for default dimensions on master data | COVERED-11 | 11 | Default dimensions on master data |
| Ledger calendar (Fiscal calendars + Ledger calendar) | Period statuses per module (open/on hold/closed), per user group access | COVERED-14 | 14 | Calendar definition in 6; period control in 14 |
| Fiscal calendars (Org admin shared) | Years, periods, closing periods | COVERED-6 | 6 | |
| Journals → Journal names | Type, voucher series, approval workflow, posting restrictions, lines limit, fixed offset account, default currency/exchange rate type, private for user group | COVERED-11 | 11 | |
| Journals → Journal texts | Reusable transaction text codes | COVERED-11 | 11 | Journal templates |
| Journals → Journal controls (posting restrictions by account type/segment) | Restrict accounts per journal name | COVERED-11 | 11 | |
| Journals → Default descriptions (Transaction text) | Auto text by transaction type with %1 placeholders | PARTIAL-11 (default description templates per posting type not named) | 11 | Small; put in wave 11 journal workspace |
| Journals → Accrual schemes | Ledger accrual schemes with debit/credit accounts, period key | COVERED-14 | 14 | |
| Journals → Periodic journal names | | COVERED-14 | 14 | |
| Posting → Accounts for automatic transactions (System accounts) | Penny difference, exchange rate gain/loss, year-end result, error account, cash discount etc. | COVERED-11 | 11 | |
| Posting → Posting definitions (public sector) | | OUT-OF-SCOPE (public sector) | — | |
| Posting → Transaction posting definitions (PSA) | | OUT-OF-SCOPE (public sector) | — | |
| Posting → Intercompany accounting | Due-to/due-from account pairs between legal entities, journal names | COVERED-40 | 40 | |
| Posting → Reason codes (financial reasons) | Reasons for reversals, FA, customer/vendor transactions | PARTIAL-11 (shared reason code framework not named) | 11 | Gap register |
| Posting → Ledger posting groups (sales tax) | Tax payable/receivable/settlement accounts | COVERED-13 | 13 | |
| Posting → Inventory posting (link) | Inventory posting profiles | COVERED-16 | 16 | |
| Allocation → Ledger allocation rules | Fixed/basis/spread, source filters, destination, offset, reversal | COVERED-14 | 14 | |
| Allocation → Allocation terms (on main account) | Fixed percentage allocation on account | COVERED-14 | 14 | |
| Currency → Currencies | ISO code, rounding (price/unit/sales/purchase/general rounding rules), symbol | COVERED-6 | 6 | ISO 4217 reference data |
| Currency → Currency exchange rates | Rates by type/pair/date, triangulation | COVERED-12 | 12 | |
| Currency → Exchange rate types | Rate types (default, budget, closing) | COVERED-12 | 12 | |
| Currency → Currency revaluation accounts | Realized/unrealized gain/loss by currency | COVERED-12 | 12 | |
| Currency → Import currency exchange rates (provider) | Configure providers (ECB, OANDA) and schedule | COVERED-12 | 12 | ECB/OpenExchangeRates adapters |
| Currency → Euro triangulation / Denomination currency | Legacy euro conversion | OUT-OF-SCOPE (obsolete; triangulation itself is in wave 12) | — | |
| Sales tax → Sales tax codes | Rate, limits, unit, calculation method (whole amount/interval), period, ledger posting group, use tax, exempt | COVERED-13 | 13 | |
| Sales tax → Sales tax groups | Grouping by customer/vendor with exempt/reverse charge flags | COVERED-13 | 13 | |
| Sales tax → Item sales tax groups | Grouping by item | COVERED-13 | 13 | |
| Sales tax → Sales tax authorities | Authority vendor account, rounding | COVERED-13 | 13 | |
| Sales tax → Sales tax settlement periods | Period intervals per authority, terms | COVERED-13 | 13 | |
| Sales tax → Ledger posting groups | | COVERED-13 | 13 | |
| Sales tax → Sales tax exempt codes | Reason codes for exemption | COVERED-13 | 13 | Exemption certificates |
| Sales tax → Sales tax jurisdictions (US) | Jurisdiction hierarchy, codes | COVERED-13 | 13 | |
| Sales tax → Sales tax reporting codes | Codes for statutory return boxes | COVERED-13 | 13 | VAT return box mapping |
| Sales tax → Sales tax (country-specific tab e.g. VAT registration numbers by country) | | COVERED-7 | 7 | Tax registration numbers on party |
| Sales tax → Withholding tax codes / groups | Withholding rates, item withholding groups (D365) | COVERED-13 | 13 | |
| Sales tax → Tax branch (IN/other) | | OUT-OF-SCOPE (country pack beyond v1 set) | — | India is in v1 pack set but tax branch is GST-era D365; handled in wave 48 India pack as needed |
| Sales tax → External (Sales tax, Avalara-type) | External provider connection | COVERED-13 | 13 | |
| Sales tax → Conditional sales tax | Tax on payment | COVERED-13 | 13 | |
| Sales tax → Sales tax on prepayment | | COVERED-13 | 13 | |
| Foreign trade → Intrastat parameters, commodity codes, transaction codes, transport methods, ports, statistics procedure | Intrastat master data | PARTIAL-13 (Intrastat extract is named; the setup forms (commodity codes, transaction codes, transport methods, ports, country properties) are not) | 13 | Gap register proposes wave 13 |
| Foreign trade → EU sales list parameters / reporting codes | | COVERED-13 | 13 | |
| Consolidation → Consolidation accounts, Elimination rules, Consolidation groups | Account mapping, elimination rules, legal entity groups | COVERED-40 | 40 | |
| Fiscal LIFO (IT) | | OUT-OF-SCOPE (country pack beyond v1 set) | — | |
| Financial reporting → Report definitions (MR) | Rows, columns, trees, report | COVERED-15 | 15 | |
| Financial reporting → Financial statements (legacy) | Row/column definitions | SUPERSEDED (wave 15 designer) | 15 | |
| Financial reporting → Financial reporting parameters (D365) | Integration settings | COVERED-15 | 15 | |
| Budgeting (links) | | COVERED-38 | 38 | See §12.4 |
| Ledger → Balance control accounts | Accounts for balance control on journal name | COVERED-11 | 11 | |
| Ledger → Posting layers | Current/operations/tax layers | COVERED-12 | 12 | Posting layers = ledger groups |
| Cash flow forecasts (GL setup: main accounts cash flow, dependent accounts) | Forecast settings per account | COVERED-36 | 36 | |
| Organization → Legal entities (link) | | COVERED-6 | 6 | |
| Number sequences (GL) | Voucher series | COVERED-5 | 5 | |
| Print management (GL) | | COVERED-46 | 46 | Document designer |
| Form setup (GL letters) | | COVERED-46 | 46 | |
| Workflow → General ledger workflows (journal approval, advanced ledger entry) | Workflow configuration per journal | COVERED-4 | 4 | Workflow designer |
| Ledger → Closing sheets setup (country) | | OUT-OF-SCOPE (country pack beyond v1 set) | — | |
| Tax (IN) → India-specific setup (tax components, TDS/TCS) | | PARTIAL-48 (India pack listed for wave 48 but TDS/TCS/GST depth unspecified) | 48 | |

## 12.2 Accounts payable

Accounts payable in AX spans vendor master (Passage wave 7), purchase orders (17), receiving and invoice matching (18), payments and settlement (19) and 1099/positive pay (19, 36). Vendor collaboration goes to wave 20.

### 12.2.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Vendors (list page) | VendTableListPage | All vendors with FactBoxes (balance, contacts, recent POs) | COVERED-7 | 7 | Party model with Vendor role |
| Vendor details | VendTable | General, addresses, contacts, misc details, vendor profile, purchasing demographics, invoice and delivery, purchase order defaults, payment, tax 1099, financial dimensions | COVERED-7 | 7 | Role-specific vendor data entity |
| Vendors on hold | VendTable (Blocked = Invoice/All/Payment/Requisition/Never) | Hold statuses | COVERED-7 | 7 | Hold types in vendor role |
| Vendor requests (R3) | VendRequest (VendRequestListPage) | New vendor / change request workflow (employee-initiated, unsolicited via EP) | PARTIAL-20 (vendor request workflow and prospective-vendor self-registration are not named; supplier portal is) | 20 | Gap register |
| Vendor bank accounts | VendBankAccount | Bank details per vendor, SEPA, prenote status | COVERED-19 | 19 | |
| Purchase orders (list page) | PurchTableListPage | All POs | COVERED-17 | 17 | |
| Purchase order details | PurchTable | Header/lines, charges, confirmation, change management | COVERED-17 | 17 | |
| Purchase orders → Received but not invoiced | PurchTableListPage (filter) | POs awaiting invoice | COVERED-18 | 18 | GR/IR reconciliation |
| Purchase orders → Open purchase order lines | PurchLine list | Open line view | COVERED-17 | 17 | |
| Purchase orders → Backordered purchase lines | PurchLineBackOrder | Overdue deliveries | COVERED-17 | 17 | |
| Purchase orders → Purchase order confirmations (list of journals) | PurchConfirmJournal (VendPurchOrderJour) | Confirmation history | COVERED-17 | 17 | |
| Purchase orders → Product receipts (Packing slip journal) | VendPackingSlipJour | Posted receipts | COVERED-18 | 18 | |
| Purchase orders → Prepayment invoices | PurchPrepayTable | Prepayment on PO | COVERED-19 | 19 | Prepayments |
| Vendor invoices → Open vendor invoices | VendInvoiceJournal (CustVendInvoiceJour) | Posted invoices not yet paid | COVERED-19 | 19 | Open-item AP |
| Vendor invoices → Pending vendor invoices | VendInvoiceInfoListPage (VendInvoiceInfoTable) | Unposted header/lines invoices with matching | COVERED-18 | 18 | |
| Vendor invoices → Vendor invoice register (journal) | LedgerJournalTable (VendInvoiceRegister) | Register invoice to arrival account before approval | COVERED-18 | 18 | Invoice capture (two-step register/approve) |
| Vendor invoices → Invoice approval journal | LedgerJournalTable (VendInvoiceApprove) | Approve registered invoices, assign ledger accounts | COVERED-18 | 18 | |
| Vendor invoices → Invoice journal | LedgerJournalTable (VendInvoiceRecord) | Direct expense invoices (no PO) | COVERED-18 | 18 | |
| Vendor invoices → Invoice pool excl. posting | VendInvoicePool (LedgerJournalPool) | Pending pool of invoices | COVERED-18 | 18 | |
| Vendor invoices → Vendor invoice workflow (D365) | VendInvoiceInfoTable + workflow | Header/line approval | COVERED-18 | 18 | Exception workflow |
| Vendor invoices → Invoice matching details | VendInvoiceMatchingDetails | Price/qty/total match status per line | COVERED-18 | 18 | |
| Vendor invoices → Vendor invoice automation (D365 OCR / "Invoice capture") | VendInvoiceCapture | Capture, classify, auto-apply prepayment, submit | COVERED-18 | 18 | OCR/AP-automation adapter; AI coding agent wave 47 |
| Payments → Payment journal | LedgerJournalTable (VendPaymentJournal) | Payment lines, proposals, generate payments, settle | COVERED-19 | 19 | |
| Payments → Promissory note journals (Draw / Redraw / Remittance / Settle) | LedgerJournalTable (VendPromissoryNote*) | Vendor bills of exchange | GAP | — | Propose wave 36: promissory notes / bills of exchange (vendor side) with remittance and settlement journals; not in any wave |
| Payments → Payment proposal | VendPaymProposal | Select by due date/discount date | COVERED-19 | 19 | |
| Payments → Vendor payment advice (print) | VendPaymAdvice | Advice document | COVERED-19 | 19 | |
| Payments → Positive pay (link) | BankPositivePayTable | Export issued checks | COVERED-19 | 19 | |
| Payments → Prenotes (ACH) | VendPaymPrenote | Prenote generation and confirmation | PARTIAL-19 (ACH prenote (zero-dollar validation) flow is not named) | 19 | Gap register |
| Payments → Centralized payments (cross-company) | VendPaymentJournal (centralized) | Pay on behalf of other legal entities | PARTIAL-40 (centralized payments across entities are not named in 19 or 40) | 40 | Gap register |
| Vendor transactions | VendTrans | Posted vendor ledger | COVERED-19 | 19 | |
| Vendor open transactions (Settle open transactions) | VendOpenTrans | Settlement, mark, cross-currency, cash discount | COVERED-19 | 19 | |
| Closed transaction editing | VendTransClosed? (VendSettledTrans) | Reverse settlements | COVERED-19 | 19 | Reversal handling |
| Vendor invoice (from PO) — Invoice posting form | PurchEditLines (VendInvoiceInfoTable) | Post PO invoice | COVERED-18 | 18 | |
| Purchase order → Receipts list (Arrival overview) | WMSArrivalOverview | Inbound arrival planning | COVERED-18 | 18 | Arrival/registration |
| Purchase order → Registration | InventTransRegister | Register receipt by dimension/serial | COVERED-18 | 18 | |
| Vendor → Consignment (D365 consignment replenishment orders) | VendConsignmentReplenishmentOrder | Vendor-owned stock, ownership change | GAP | — | Propose wave 16: consignment inventory (vendor-owned on-hand, ownership-change journal, consignment replenishment orders) |
| 1099 → Vendor 1099 transactions | Vend1099Trans (Tax1099Summary) | Tax 1099 amounts per vendor | COVERED-13 | 13 | 1099 reporting hooks |
| Vendor → Vendor collaboration invoices (D365) | VendCollabInvoice | Vendor-submitted invoices | COVERED-20 | 20 | Supplier portal invoice submission |
| Vendor → Vendor evaluation (R3 procurement) | VendEvaluation | Rate vendors on criteria | COVERED-33 | 33 | Supplier quality scorecards |
| Purchase agreements (link) | PurchAgreement | Blanket agreements | COVERED-17 | 17 | |
| Vendor → Charges transactions | MarkupTrans | Posted charges | COVERED-17 | 17 | |
| Vendor → Deliveries (Vendor delivery schedule) | PurchLineDeliverySchedule | Delivery schedules on PO lines | COVERED-17 | 17 | |

### 12.2.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Vendor transactions inquiry | Posted transactions by vendor | COVERED-19 | 19 | |
| Vendor balance (Vendor balance list) | Balances by vendor | COVERED-19 | 19 | |
| Vendor aging (Aged balances inquiry) | Aging by period definition | COVERED-19 | 19 | Vendor aging |
| Invoice history and matching details | Match status per invoice | COVERED-18 | 18 | |
| Invoice journal inquiry / Invoice (Vendor invoice journal) | Posted invoice headers | COVERED-18 | 18 | |
| Payment history / Payment inquiry | Payments by vendor | COVERED-19 | 19 | |
| Settlement inquiry (Settled transactions) | Settlement pairs, cash discounts | COVERED-19 | 19 | |
| Purchase order lines / Purchase order history | PO status history | COVERED-17 | 17 | Versioned change management |
| Product receipt journal inquiry (Packing slip journal) | Receipts | COVERED-18 | 18 | |
| Cash discount inquiry | Discounts taken/lost | COVERED-19 | 19 | |
| Vendor invoice transactions (line-level) | Invoice lines | COVERED-18 | 18 | |
| Journals → Confirmations / Receipts lists / Product receipts / Invoices (journal inquiries) | Posted document journals | COVERED-17 | 17 | |
| Accrued purchases (Received not invoiced) | GR/IR accrual balance | COVERED-18 | 18 | |
| Charges inquiry | Charges by PO | COVERED-17 | 17 | |
| Withholding tax inquiry | Withholding by vendor | COVERED-13 | 13 | |
| 1099 → Tax 1099 summary / detail / state | 1099 inquiries | COVERED-13 | 13 | US country pack wave 48 produces IRS file formats |
| Vendor hold history | | COVERED-7 | 7 | Audit log |
| Positive pay inquiry | Exported checks | COVERED-19 | 19 | |
| SSRS: Vendor aging (Vendor aging report) | Aged AP | COVERED-19 | 19 | |
| SSRS: Vendor balance list with credit limit | | COVERED-19 | 19 | |
| SSRS: Vendor invoice (printed invoice copy) | | COVERED-18 | 18 | |
| SSRS: Vendor transactions report | | COVERED-19 | 19 | |
| SSRS: Vendor payment advice / Payment summary | | COVERED-19 | 19 | |
| SSRS: Check (BankChequePrint) / Check register | Check layout printing | COVERED-19 | 19 | Check printing wave 19; check management wave 36 |
| SSRS: Vendor 1099 (1099-MISC/INT/DIV/S/G forms and electronic file) | IRS forms and e-file | COVERED-48 | 48 | US country pack; hooks in 13 |
| SSRS: Vendor invoice specification / Invoice register journal | | COVERED-18 | 18 | |
| SSRS: Purchase order (document), Purchase order confirmation, Purchase inquiry (price inquiry) | PO documents | COVERED-17 | 17 | Rendered wave 46 |
| SSRS: Product receipt (packing slip) document | | COVERED-18 | 18 | |
| SSRS: Receipts list | | COVERED-18 | 18 | |
| SSRS: Vendor statement (Vendor account statement) | | COVERED-19 | 19 | |
| SSRS: Vendor base data / Vendor list | | COVERED-7 | 7 | |
| SSRS: Purchase order status / Purchase order overview / Backorder lines | | COVERED-17 | 17 | |
| SSRS: Purchase analysis (Purchase by vendor/item/period) | Statistics | COVERED-46 | 46 | Semantic layer |
| SSRS: Vendor payment journal / Payment history | | COVERED-19 | 19 | |
| SSRS: Cash discount list / Terms of payment list | Setup listings | COVERED-7 | 7 | |
| SSRS: Expected cash flow (vendor) | | COVERED-36 | 36 | |
| SSRS: Vendor posting profile / Vendor groups listing | | COVERED-7 | 7 | |
| SSRS: Withholding tax (US/other) | | COVERED-13 | 13 | |
| SSRS: Purchase order lines — Vendor delivery performance (On-time delivery) | Vendor OTD | COVERED-33 | 33 | Supplier scorecards |
| SSRS: Promissory note (vendor) documents | | GAP | — | Propose wave 36: with promissory notes |
| SSRS: Vendor due date list (Open invoices by due date) | | COVERED-19 | 19 | |
| SSRS: Prepayment statement | | COVERED-19 | 19 | |
| SSRS: Vendor invoice totals (country) | | OUT-OF-SCOPE (country pack beyond v1 set) | — | |

### 12.2.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Payment proposal (batch) | Generate payment lines | COVERED-19 | 19 | |
| Generate payments (payment media) | Produce ACH/SEPA/check/wire files | COVERED-19 | 19 | pain.001, NACHA, check print |
| Payment file return import (pain.002) | Bank status ingestion | COVERED-19 | 19 | |
| Positive pay export | | COVERED-19 | 19 | |
| Foreign currency revaluation (AP) | Revalue open vendor items | COVERED-12 | 12 | |
| Settlement → Settle open transactions (batch) / Automatic settlement | Auto-settle by rules | COVERED-19 | 19 | |
| Reverse settlement | | COVERED-19 | 19 | |
| Cash discount → Recalculate cash discount dates | | COVERED-19 | 19 | |
| Invoice → Post pending vendor invoices (batch) | | COVERED-18 | 18 | |
| Invoice → Vendor invoice policy violations (R3 audit) | Policy check batch | PARTIAL-48 (audit policies are not named; SoD engine is) | 48 | Gap register |
| Purchase order → Confirm / Receive / Invoice (batch posting) | Mass posting | COVERED-17 | 17 | Batch job framework wave 4 |
| Purchase order → Update → Delivery date / Finalize PO (close) | | COVERED-17 | 17 | |
| Purchase order → Purchase order confirmation requests (vendor collaboration) | | COVERED-20 | 20 | |
| Purchase order → Evaluated receipt settlement (ERS) | Self-billing invoices from receipts | COVERED-18 | 18 | |
| Purchase order → Delete purchase orders / lines (clean up) | Housekeeping | COVERED-17 | 17 | |
| Purchase order → Vendor delivery schedule update | | COVERED-17 | 17 | |
| 1099 → Update 1099 (year-end) / Print 1099 / electronic filing | | COVERED-48 | 48 | US pack |
| Vendor → Vendor hold release (mass) | | COVERED-7 | 7 | |
| Vendor → Update vendor bank accounts status (prenote results) | | PARTIAL-19 (prenote) | 19 | |
| Promissory notes → Draw / Redraw / Remit / Settle (periodic batches) | | GAP | — | Propose wave 36: promissory notes |
| Clean up → Delete settled transactions / Compress vendor transactions | | SUPERSEDED (append-only ledger; archive to lakehouse wave 46) | 46 | |
| Consignment → Consignment replenishment order processing | | GAP | — | Propose wave 16: consignment |
| Invoice capture → Process OCR queue (D365) | | COVERED-18 | 18 | |
| Vendor collaboration → Provision users / invite (D365) | | COVERED-20 | 20 | |
| Purchase requisition → Release approved requisitions / Consolidate (procurement, link) | | COVERED-17 | 17 | |

### 12.2.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Accounts payable parameters — General tab | Posting profile default, settlement priority, cash discount administration, credit note as correction, delivery address, intercompany | COVERED-19 | 19 | |
| Accounts payable parameters — Updates tab | PO defaults (confirmation/receipt/invoice posting rules), safety levels on PO, enable change management, cancel/reverse rules, product receipt posting | COVERED-17 | 17 | |
| Accounts payable parameters — Invoice tab | Invoice matching (2-way/3-way/none), post product receipt in ledger, post invoice automatically, approval workflow, invoice totals matching, charges matching, allow price tolerance, use invoice register, posting of invoice: tax, prepayment | COVERED-18 | 18 | |
| Accounts payable parameters — Settlement tab | Automatic settlement, cash discount mark, exchange rate at settlement, reverse settlement options | COVERED-19 | 19 | |
| Accounts payable parameters — Ledger and sales tax tab | Sales tax, posting type, financial dimension defaults | COVERED-13 | 13 | |
| Accounts payable parameters — Credit rating / Blocking | Block types | COVERED-7 | 7 | |
| Accounts payable parameters — Number sequences | Invoice, payment, PO sequences | COVERED-5 | 5 | |
| Accounts payable parameters — Summary update | Consolidation of POs at invoicing by invoice account/order/automatic summary | COVERED-18 | 18 | Summary invoice posting across POs |
| Accounts payable parameters — Prices (Price tolerance) | Default price tolerance percentage, charges tolerance | COVERED-18 | 18 | |
| Accounts payable parameters — Vendor invoice workflow (D365 Invoice capture) | | COVERED-18 | 18 | |
| Vendors → Vendor groups | Group, default terms, default tax group | COVERED-7 | 7 | |
| Vendors → Vendor posting profiles | Summary account, settle account, sales tax prepayment, arrival account per group/vendor/table | COVERED-7 | 7 | Posting profile hooks in 7; accounts resolved in 11 |
| Vendors → Vendor holds reasons (D365) | | COVERED-7 | 7 | |
| Vendors → Vendor request types (R3) | Request workflow types | PARTIAL-20 (vendor request workflow) | 20 | |
| Vendors → Vendor evaluation criteria / criterion groups (R3) | Scoring criteria | COVERED-33 | 33 | |
| Vendors → Vendor profiles / Vendor user requests (EP) | | SUPERSEDED (supplier portal wave 20 with Keycloak identities) | 20 | |
| Payment → Terms of payment | Net days, cash on delivery, current month, COD, payment schedule, grace | COVERED-7 | 7 | |
| Payment → Cash discounts | Discount %, days, next discount code, ledger account | COVERED-7 | 7 | Also in 19 settlement |
| Payment → Methods of payment (vendor) | Period, account type, file format (export), bridging posting, payment specification, remittance, bank transaction type, check/EFT/SEPA, positive pay format, validation | COVERED-19 | 19 | |
| Payment → Payment specifications | Per method of payment code (e.g. SEPA purpose) | COVERED-19 | 19 | |
| Payment → Payment fees | Fee per bank/method with ledger account | COVERED-19 | 19 | |
| Payment → Payment days | Fixed days of week/month for payment date | COVERED-19 | 19 | |
| Payment → Payment schedules | Installments (fixed amount/quantity/percentage) | COVERED-7 | 7 | |
| Payment → Payment calendar (D365) | Bank holidays by country for due dates | PARTIAL-19 (payment calendar / bank holiday due-date shifting is not named) | 19 | Gap register |
| Payment → Promissory note setup (journal names, posting profiles, protest) | | GAP | — | Propose wave 36: promissory notes |
| Payment → Positive pay formats | XSLT-defined bank formats | COVERED-19 | 19 | Positive pay export |
| Payment → Payment purpose codes (country) | | COVERED-19 | 19 | SEPA purpose codes |
| Payment → Direct debit (vendor) | | OUT-OF-SCOPE (vendor-side direct debit is country-specific; SEPA creditor side covered via customer direct debit in AR) | — | |
| Charges → Charges codes (purchase) | Debit/credit type (item/ledger/customer/vendor), tax group, posting | COVERED-17 | 17 | |
| Charges → Auto charges | Automatic charges by account/item code, mode of delivery | COVERED-17 | 17 | |
| Charges → Vendor charges groups / Item charges groups / Delivery charges groups | | COVERED-17 | 17 | |
| Charges → Charges allocation (to lines) | Allocate header charges to lines by net amount/quantity | COVERED-17 | 17 | Landed cost in 34 |
| Invoice matching → Price tolerances (item/vendor/group) | Tolerance % per item/vendor combination | COVERED-18 | 18 | |
| Invoice matching → Charges tolerances | | COVERED-18 | 18 | |
| Invoice matching → Invoice totals tolerances (D365: Invoice totals matching) | | COVERED-18 | 18 | |
| Invoice matching → Matching policy by item/vendor | 2-way/3-way per item/vendor | COVERED-18 | 18 | |
| Invoice → Vendor invoice policies (R3 audit policy) | Policy rules for invoice audit (duplicates, missing receipts) | PARTIAL-48 (audit policies) | 48 | |
| Invoice → Invoice register setup (arrival accounts) | Via posting profile | COVERED-18 | 18 | |
| Invoice → Vendor invoice automation parameters (D365) | | COVERED-18 | 18 | |
| Forms → Form setup (PO, receipt, invoice print options) | Document notes, print settings, number of copies | COVERED-46 | 46 | |
| Forms → Form notes | Notes per document type | COVERED-46 | 46 | |
| Forms → Print management | Conditional print destinations per document/vendor | PARTIAL-46 (print management conditional routing per account is not named; document designer is) | 46 | Gap register |
| Tax 1099 → 1099 fields / 1099 boxes / State 1099 / 1099 software vendor info | IRS box mapping | COVERED-48 | 48 | US pack; hooks in 13 |
| Distribution → Financial dimension defaults on vendor | | COVERED-11 | 11 | |
| Vendor bank → Bank transaction types, Bank parameters (link) | | COVERED-36 | 36 | |
| Trade agreements → Price/discount agreement journal names | | COVERED-26 | 26 | Pricing engine applies to purchase prices too (wave 17 uses it) |
| Trade agreements → Activate price/discount (Trade agreement activation) | | COVERED-26 | 26 | |
| Trade agreements → Price/discount groups (vendor) | | COVERED-26 | 26 | |
| Trade agreements → Purchase price tolerance | | COVERED-18 | 18 | |
| Purchase → Purchase order defaults (order type, charges) | | COVERED-17 | 17 | |
| Purchase → Purchase order change management (activation per vendor) | | COVERED-17 | 17 | |
| Purchase → Prepayment setup (prepayment posting profile) | | COVERED-19 | 19 | |
| External codes (vendor) | Partner codes for EDI | COVERED-35 | 35 | Partner profiles |
| Vendor → Vendor item catalog (Vendor external item numbers) | Vendor item numbers, descriptions | COVERED-8 | 8 | External item numbers on released product |
| Withholding tax (vendor group assignment) | | COVERED-13 | 13 | |
| Vendor → Purchase order approval policies (link to Procurement) | | COVERED-17 | 17 | |
| Vendor → Vendor consignment setup (D365 consignment parameters, ownership change journal names) | | GAP | — | Propose wave 16: consignment |
| Workflow → Accounts payable workflows (vendor invoice, PO, vendor change) | | COVERED-4 | 4 | |
| Number sequences (AP) | | COVERED-5 | 5 | |
| Vendor → Vendor collaboration parameters (D365) | | COVERED-20 | 20 | |
| Payment → Electronic payment file formats (AIF-based/ER) | | COVERED-19 | 19 | ISO 20022 generator; ER designer in 48 for exotic formats |
| Payment → Check layout (bank account) | Check printing layout per bank account | COVERED-19 | 19 | Check layout in document designer; bank check management in 36 |

## 12.3 Accounts receivable

Accounts receivable covers customer master (wave 7), free-text and sales invoices (29), payments, settlement, collections, interest and lockbox (30), credit management (27) and commissions (26). AX 2012 R3 also places the sales order list here; it is assessed in §12.17.

### 12.3.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Customers (list page) | CustTableListPage | All customers with balance/aging FactBoxes | COVERED-7 | 7 | Party model with Customer role |
| Customer details | CustTable | General, addresses, contacts, misc details, sales demographics, credit and collections, invoice and delivery, sales order defaults, payment defaults, financial dimensions, retail | COVERED-7 | 7 | |
| Customer → Credit limit / credit rating / mandatory credit limit | CustTable (credit tab) | Limit, rating, mandatory check | COVERED-27 | 27 | Credit management |
| Customer bank accounts | CustBankAccount | Bank details for direct debit/refunds | COVERED-30 | 30 | |
| Customer → Direct debit mandates (SEPA, R3) | CustDirectDebitMandate | Mandate lifecycle, sequence type | PARTIAL-30 (SEPA direct debit mandates (pain.008) are not named; collections and cash application are) | 30 | Gap register |
| Sales orders (list page, also in AR) | SalesTableListPage | | COVERED-27 | 27 | See §12.17 |
| Free text invoices (list page) | CustFreeInvoiceListPage | Non-item invoices with ledger lines | COVERED-29 | 29 | |
| Free text invoice details | CustFreeInvoice | Lines with ledger account, tax, dimensions, charges | COVERED-29 | 29 | |
| Recurring free text invoices (R3 templates) | CustInvoiceTemplate / CustRecurrenceInvoice | Template-based recurring invoices | COVERED-29 | 29 | Subscription/recurring schedules |
| Customer invoices → Open customer invoices | CustInvoiceJournal | Posted invoices open | COVERED-30 | 30 | |
| Customer invoices → Invoice journal (customer) | LedgerJournalTable (CustInvoiceJournal) | Ledger-based customer invoices | COVERED-29 | 29 | |
| Customer invoices → Sales invoice journal (posted) | CustInvoiceJour | Posted sales invoices | COVERED-29 | 29 | |
| Payments → Payment journal | LedgerJournalTable (CustPaymentJournal) | Customer receipts, settlement, deposit slips | COVERED-30 | 30 | Cash application |
| Payments → Lockbox transactions (R3 US) | CustLockbox / CustPaymLockbox | Import BAI2 lockbox files, match, create payment journal | COVERED-30 | 30 | |
| Payments → Bill of exchange journals (Draw / Redraw / Protest / Remittance / Settle) | LedgerJournalTable (CustBillOfExchange*) | Customer bills of exchange | GAP | — | Propose wave 36: bills of exchange (customer) with remittance for collection/discount, protest, settle; not in any wave |
| Payments → Deposit slips | BankDeposit | Group receipts into bank deposit | COVERED-36 | 36 | |
| Payments → Payment advice (customer) | CustPaymAdvice | | COVERED-30 | 30 | |
| Payments → Credit card payments (Payment services) | CreditCardAuthTrans | Authorize/capture via payment service | COVERED-30 | 30 | Stripe/Adyen adapter |
| Payments → Postdated checks (R3 APAC) | CustPostDatedCheck | Register, mature, settle PDCs | GAP | — | Propose wave 36: postdated checks (customer and vendor) as a treasury instrument |
| Collections → Collections (list page, R3 collections workspace) | CustCollectionsListPage | Aging, activities, promises, cases per customer | COVERED-30 | 30 | Collections workspace |
| Collections → Collections (customer detail page) | CustCollections | Transactions, activities, promise to pay, dispute, write-off, NSF | COVERED-30 | 30 | |
| Collections → Collection letter notes (journal) | CustCollectionLetterJour | Generated collection letters | COVERED-30 | 30 | Dunning levels |
| Collections → Interest notes (journal) | CustInterestJour | Interest invoices | COVERED-30 | 30 | |
| Collections → Customer pools | CustCollectionsPool | Query-based customer groupings for collectors | COVERED-30 | 30 | |
| Collections → Collection agents | CustCollectionsAgent | Agent assignment | COVERED-30 | 30 | |
| Collections → Aging snapshot | CustAgingLegalEntity (CustAgingSnapshot) | Snapshot of aging for workspace | COVERED-30 | 30 | Aging computed live from open items; snapshot for KPI history in 46 |
| Collections → Cases (collection cases) | CaseDetail | Case per customer dispute | PARTIAL-30 (disputes workflow named; generic case management not) | 30 | See §12.29 case management gap |
| Collections → Promise to pay / NSF / Write-off actions | CustCollections (actions) | | COVERED-30 | 30 | |
| Customer transactions | CustTrans | Posted customer ledger | COVERED-30 | 30 | |
| Customer open transactions (Settle open transactions) | CustOpenTrans | Settlement with marking, cross-currency, discounts | COVERED-30 | 30 | |
| Closed transaction editing (customer) | CustSettledTrans | Reverse settlement | COVERED-30 | 30 | |
| Customer → Interest adjustments | CustInterestAdj | Adjust interest notes | COVERED-30 | 30 | |
| Customer → Trade agreements (price/discount agreement journals) | PriceDiscAdmTable | Customer pricing journals | COVERED-26 | 26 | |
| Customer → Charges transactions | MarkupTrans | | COVERED-26 | 26 | |
| Customer → Prepayments (sales order prepayment) | SalesPrepayment (via payment journal) | Customer prepayment and application | COVERED-30 | 30 | |
| Customer → Credit management (D365 add-in): Credit holds list, Credit limit adjustments journal, Blocking rules | CustCreditManagement* | Credit holds, temporary limits, rules | COVERED-27 | 27 | Credit holds, limits, release workflow |
| Customer → Customer self-service (D365 customer portal) | | | COVERED-30 | 30 | Customer portal |
| Returns → Return orders (link) | ReturnTable | RMA | COVERED-30 | 30 | |
| Commission transactions | CommissionTrans | Posted commission | PARTIAL-26 (commission calculation hooks are named, but commission posting to vendor/employee and transactions are not) | 26 | Gap register |
| Customer → Customer invoice (packing slip based) posting — Summary update | SalesEditLines | Consolidated invoicing | COVERED-29 | 29 | |
| Customer → Global customer (cross-company) | CustTable (global) | | COVERED-7 | 7 | Party shared across entities |
| Customer → Rebates (Customer rebate agreements — TAM link) | TAMRebateAgreement | | COVERED-26 | 26 | Rebate accrual 26, settlement 30 |

### 12.3.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Customer transactions inquiry | | COVERED-30 | 30 | |
| Customer balance list / Customer balance | | COVERED-30 | 30 | |
| Aged balances (customer aging inquiry) | | COVERED-30 | 30 | |
| Invoice journal / Invoice lines inquiry | Posted invoices | COVERED-29 | 29 | |
| Payment history / Payment inquiry | | COVERED-30 | 30 | |
| Settlement inquiry (Settled transactions) | | COVERED-30 | 30 | |
| Interest note inquiry | | COVERED-30 | 30 | |
| Collection letter inquiry | | COVERED-30 | 30 | |
| Credit card transactions inquiry | | COVERED-30 | 30 | |
| Lockbox import history | | COVERED-30 | 30 | |
| Sales order inquiries (Sales orders, Backorders, Open order lines, Delivered not invoiced) | | COVERED-27 | 27 | |
| Journals → Confirmations / Picking lists / Packing slips / Invoices | Posted document journals | COVERED-28 | 28 | Invoices in 29 |
| Commission inquiry | | PARTIAL-26 (commission posting) | 26 | |
| Credit management inquiries (D365: credit holds history, credit limit history) | | COVERED-27 | 27 | |
| Customer hold / on-hold history | | COVERED-7 | 7 | |
| Direct debit mandate inquiry | | PARTIAL-30 (direct debit) | 30 | |
| Bill of exchange inquiry | | GAP | — | Propose wave 36: bills of exchange |
| Postdated check inquiry | | GAP | — | Propose wave 36: postdated checks |
| Free text invoice inquiry | | COVERED-29 | 29 | |
| Customer self-service inquiries (portal) | | COVERED-30 | 30 | |
| SSRS: Customer aging (Aged balances report) | | COVERED-30 | 30 | |
| SSRS: Customer balance list / with credit limit | | COVERED-30 | 30 | |
| SSRS: Customer account statement (external / internal) | Statements | COVERED-30 | 30 | Customer statements |
| SSRS: Customer transactions | | COVERED-30 | 30 | |
| SSRS: Invoice (sales invoice document) / Free text invoice document / Invoice specification | Customer-facing documents | COVERED-29 | 29 | Rendered wave 46; e-invoice 35 |
| SSRS: Collection letter note document | | COVERED-30 | 30 | |
| SSRS: Interest note document | | COVERED-30 | 30 | |
| SSRS: Customer payment advice / Payment summary | | COVERED-30 | 30 | |
| SSRS: Deposit slip | | COVERED-36 | 36 | |
| SSRS: Customer due date list (Open invoices by due date) | | COVERED-30 | 30 | |
| SSRS: Expected cash flow (customer) | | COVERED-36 | 36 | |
| SSRS: Customer base data / Customer list | | COVERED-7 | 7 | |
| SSRS: Sales order confirmation / Picking list / Packing slip (documents) | | COVERED-28 | 28 | Confirmation in 27 |
| SSRS: Sales analysis (Sales by customer/item/period, Top customers) | | COVERED-46 | 46 | Semantic layer |
| SSRS: Commission report / Commission transactions | | PARTIAL-26 (commission posting) | 26 | |
| SSRS: Cash discount list / Terms of payment / Posting profiles listings | | COVERED-7 | 7 | |
| SSRS: Sales tax reporting (customer) | | COVERED-13 | 13 | |
| SSRS: Bill of exchange documents / Remittance list | | GAP | — | Propose wave 36: bills of exchange |
| SSRS: Credit card payment report | | COVERED-30 | 30 | |
| SSRS: Customer invoice totals (country-specific) | | OUT-OF-SCOPE (country pack beyond v1 set) | — | |
| SSRS: Lockbox report | | COVERED-30 | 30 | |
| SSRS: Collections (Collections activities, Promises) | | COVERED-30 | 30 | |
| SSRS: Customer/customer group statistics | | COVERED-46 | 46 | |
| SSRS: Prepayment statement (customer) | | COVERED-30 | 30 | |
| SSRS: External codes listing | | COVERED-35 | 35 | |

### 12.3.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Collections → Create collection letters | Generate letters by sequence/level | COVERED-30 | 30 | Dunning run |
| Collections → Print/post collection letters | | COVERED-30 | 30 | |
| Collections → Interest calculation (Create interest notes) | Calculate interest on overdue | COVERED-30 | 30 | |
| Collections → Print/post interest notes | | COVERED-30 | 30 | |
| Collections → Age customer balances (aging snapshot) | | COVERED-30 | 30 | |
| Collections → Customer pool refresh | | COVERED-30 | 30 | |
| Settlement → Settle open transactions (batch) / Automatic settlement | | COVERED-30 | 30 | |
| Foreign currency revaluation (AR) | | COVERED-12 | 12 | |
| Payments → Lockbox import (batch) | | COVERED-30 | 30 | |
| Payments → Payment file import (customer payments / camt.054) | | COVERED-30 | 30 | camt.053/054 auto-match |
| Payments → Generate direct debit (pain.008) | SEPA direct debit collection | PARTIAL-30 (direct debit) | 30 | |
| Payments → Bill of exchange remittance/protest batches | | GAP | — | Propose wave 36: bills of exchange |
| Payments → Credit card settlement/capture (payment services) | Capture authorized cards on invoice | COVERED-30 | 30 | |
| Sales update → Confirmation / Picking list / Packing slip / Invoice (batch) | Mass posting | COVERED-28 | 28 | Invoice 29 |
| Sales update → Summary update (consolidated invoicing) | | COVERED-29 | 29 | |
| Sales update → Recurring free text invoices (generate) | | COVERED-29 | 29 | |
| Sales update → Reservation (batch) / Explode BOM | | COVERED-16 | 16 | |
| Sales update → Delete orders (clean up) | | COVERED-27 | 27 | |
| Sales update → Update credit holds / Credit management periodic (D365) | | COVERED-27 | 27 | |
| Customer statements (periodic generate) | | COVERED-30 | 30 | |
| Clean up → Delete settled transactions / Compress customer transactions | | SUPERSEDED (append-only; archive to lakehouse) | 46 | |
| Commission → Calculate/post commissions (via invoice) | | PARTIAL-26 (commission posting) | 26 | |
| Credit card → Authorization expiry cleanup | | COVERED-30 | 30 | |
| Rebates → Process rebates / Cumulate / Approve / Pay (TAM link) | | COVERED-30 | 30 | Rebate settlement |
| Customer → Customer payment predictions (D365 Finance insights) | ML prediction of payment date | COVERED-47 | 47 | Collections outreach agent; prediction model under wave 47 |
| Customer → Update customer bank account status | | COVERED-30 | 30 | |
| Customer → Send e-invoices (ER) | | COVERED-35 | 35 | Peppol |
| Interest → Interest adjustment batch | | COVERED-30 | 30 | |

### 12.3.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Accounts receivable parameters — General tab | Posting profile, settlement priority, delivery address, credit rating, mandatory credit limit, credit limit type (none/balance/balance+packing slip/all), message when exceeding, intercompany | COVERED-27 | 27 | |
| Accounts receivable parameters — Updates tab | Posting defaults for confirmation/picking/packing slip/invoice, safety levels, cancel options, picking list posting, packing slip in ledger, delivery date control defaults | COVERED-28 | 28 | |
| Accounts receivable parameters — Settlement tab | Automatic settlement, cash discount rules, exchange rate at settlement, settlement of overpayments | COVERED-30 | 30 | |
| Accounts receivable parameters — Collections tab | Collection letter from (per customer/transaction), interest calculation basis, collection agents, aging period definition | COVERED-30 | 30 | |
| Accounts receivable parameters — Credit rating / Credit management (D365) | Credit management automation | COVERED-27 | 27 | |
| Accounts receivable parameters — Ledger and sales tax tab | | COVERED-13 | 13 | |
| Accounts receivable parameters — Prices tab | Price/discount search defaults, total discount calculation, smart rounding | COVERED-26 | 26 | |
| Accounts receivable parameters — Summary update tab | Summary update by invoice account/order/automatic/none, summary update parameters | COVERED-29 | 29 | |
| Accounts receivable parameters — Shipments tab (AR delivery) | Output order, shipment defaults | SUPERSEDED (wave 28 fulfillment / wave 31 WHS model replaces WMS-I shipments) | 28 | |
| Accounts receivable parameters — Number sequences | | COVERED-5 | 5 | |
| Accounts receivable parameters — Payment services (credit card) | Processor connection, authorization expiry | COVERED-30 | 30 | |
| Accounts receivable parameters — Sales order holds | Default hold behaviors | COVERED-27 | 27 | |
| Customers → Customer groups | | COVERED-7 | 7 | |
| Customers → Customer posting profiles | Summary account, settle account, collection letter sequence, interest code, write-off account | COVERED-7 | 7 | |
| Customers → Customer hold reasons / On-hold statuses (Invoice/All/Payment/Requisition) | | COVERED-7 | 7 | |
| Customers → Customer self-service parameters (EP) | | SUPERSEDED (customer portal wave 30) | 30 | |
| Payment → Terms of payment | | COVERED-7 | 7 | |
| Payment → Cash discounts | | COVERED-7 | 7 | |
| Payment → Methods of payment (customer) | Period, account type, file format (import), bridging, lockbox, credit card, direct debit, payment specification | COVERED-30 | 30 | |
| Payment → Payment specifications / Payment fees / Payment days / Payment schedules | | COVERED-30 | 30 | Schedules in 7 |
| Payment → Payment calendar (D365) | | PARTIAL-19 (payment calendar) | 19 | |
| Payment → Lockbox setup (lockbox, lockbox file format, customer MICR numbers) | BAI2 format mapping | COVERED-30 | 30 | |
| Payment → Direct debit mandate setup (SEPA creditor identifier) | | PARTIAL-30 (direct debit) | 30 | |
| Payment → Bill of exchange setup (journal names, posting profiles, protest reasons, remittance types) | | GAP | — | Propose wave 36: bills of exchange |
| Payment → Postdated checks setup (R3 APAC) | | GAP | — | Propose wave 36: postdated checks |
| Payment → Credit card → Payment services / Credit card types / Credit card processors | Authorize.net-style connector | COVERED-30 | 30 | |
| Payment → Prepayment posting profile | | COVERED-30 | 30 | |
| Collections → Collection letter sequences | Levels, days, fees, letter text, block/hold actions | COVERED-30 | 30 | |
| Collections → Interest codes | Rate by period, grace, min/max, ranges, calculation by date/days | COVERED-30 | 30 | |
| Collections → Aging period definitions | Buckets by day/month/period | COVERED-30 | 30 | |
| Collections → Customer pools | | COVERED-30 | 30 | |
| Collections → Collection agents / Collections team (R3 team) | | COVERED-30 | 30 | |
| Collections → Collections email templates (D365) | | PARTIAL-30 (email templates for dunning/collections communication not named; letters are) | 30 | Gap register (email framework, Org admin) |
| Collections → Write-off reason codes / Write-off account | | COVERED-30 | 30 | |
| Collections → NSF information (bank → NSF fees) | | COVERED-36 | 36 | |
| Credit management (D365) → Credit management parameters, Blocking rules, Scoring groups, Risk groups, Credit limit adjustment journal names, Guarantees/insurance, Credit management groups | Rule-based holds and scoring | COVERED-27 | 27 | Aging-based rules, risk scoring |
| Charges → Charges codes (sales) / Auto charges / Customer charges groups / Item charges groups / Delivery charges groups | | COVERED-26 | 26 | Charges are a pricing condition |
| Commission → Commission calculation / Commission sales groups / Commission customer groups / Commission item groups / Commission posting | | PARTIAL-26 (commission posting and sales group/rep assignment) | 26 | |
| Forms → Form setup (sales documents) / Form notes / Print management | | PARTIAL-46 (print management routing) | 46 | |
| Forms → Form sorting (sort lines on documents) | | COVERED-46 | 46 | Document designer |
| Trade agreements → Price/discount agreement journal names / Activate / Price/discount groups (customer, line, multiline, total) | | COVERED-26 | 26 | |
| Trade agreements → Smart rounding (Round-off) rules | Psychological price rounding | COVERED-26 | 26 | |
| Trade agreements → Supplementary items | Free/mandatory add-on items | PARTIAL-26 (supplementary items are not named in pricing or order waves) | 27 | Gap register |
| Sales → Order types / Order hold codes / Order event setup | | COVERED-27 | 27 | |
| Sales → Delivery (Modes of delivery, Terms of delivery, Delivery schedule defaults, Destination codes) | | COVERED-28 | 28 | Terms of delivery in 7 |
| Sales → Sales tax exemption numbers on customer | | COVERED-13 | 13 | |
| Sales → Customer item numbers (External item descriptions) | | COVERED-8 | 8 | |
| Sales → Sales reasons / Reason codes (customer) | | PARTIAL-11 (reason codes) | 11 | |
| Sales → Sales statistics / Statistics groups | | COVERED-46 | 46 | |
| Sales → Customer classification groups / Line of business / Segments / Subsegments | | COVERED-7 | 7 | Customer attributes |
| Sales → Sales districts / Sales groups (sales reps) | | PARTIAL-26 (sales groups/reps for commission) | 26 | |
| Sales → Campaign/prospect setup (link to Sales & marketing) | | OUT-OF-SCOPE (CRM non-goal) | — | |
| External codes (customer) | | COVERED-35 | 35 | |
| Workflow → Accounts receivable workflows (free text invoice, credit limit, customer change) | | COVERED-4 | 4 | |
| Statement → Customer statement setup (statement layout, statement intervals) | | COVERED-30 | 30 | |
| Interest → Interest adjustment reasons | | COVERED-30 | 30 | |
| Number sequences (AR) | | COVERED-5 | 5 | |
| Rebates → Rebate agreements (TAM) / Rebate program types / Rebate groups | | COVERED-26 | 26 | See §12.18 |
| Returns → Return reason codes / Return reason code groups / Disposition codes / Disposition actions (link to Sales & marketing) | | COVERED-30 | 30 | |
| E-invoice setup (Electronic invoice parameters, EDI) | | COVERED-35 | 35 | |
| Customer → Customer tax registration / VAT numbers | | COVERED-7 | 7 | |

## 12.4 Budgeting

AX 2012 R2/R3 budgeting = budget register entries, budget control, budget planning (R2+). All of it lands in wave 38.

### 12.4.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Budget register entries (list page) | BudgetTransactionListPage | All register entries by code/model | COVERED-38 | 38 | |
| Budget register entry details | BudgetTransaction | Header/lines, budget type (original, transfer, revision, encumbrance, pre-encumbrance, carry-forward), allocation, workflow | COVERED-38 | 38 | |
| Budget plans (list page, R2+) | BudgetPlanListPage | Budget planning documents by process/scenario/org | COVERED-38 | 38 | |
| Budget plan details | BudgetPlan | Lines per scenario, worksheets (Excel), justification, workflow stages | COVERED-38 | 38 | Top-down/bottom-up |
| Budget control statistics | BudgetControlStatistics | Budget vs actual vs encumbrance by dimension | COVERED-38 | 38 | |
| Budget plan → Budget plan templates / worksheets | BudgetPlanTemplate | Excel-based planning worksheets | PARTIAL-38 (Excel worksheet round-trip for budget plans is not named; planning workflow and scenarios are) | 38 | Gap register |
| Budget account entries (fixed assets budgets link) | AssetBudget | Capex budgets from FA | COVERED-37 | 37 | Asset budgeting |
| Budget → Reservations for encumbrances (inquiry) | BudgetSourceTracking | Encumbrance tracking per source document | COVERED-38 | 38 | Commitments |

### 12.4.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Budget control statistics inquiry | Budget funds available | COVERED-38 | 38 | |
| Actual vs budget inquiry | | COVERED-38 | 38 | |
| Budget register entry inquiry / Budget balances | | COVERED-38 | 38 | |
| Budget plan inquiries (Budget plan lines, Budget plan amounts by scenario) | | COVERED-38 | 38 | |
| Budget check results (Budget check errors/warnings) | Per source document | COVERED-38 | 38 | |
| Budget control activity (source document encumbrance) | | COVERED-38 | 38 | |
| SSRS: Budget funds available | | COVERED-38 | 38 | |
| SSRS: Actual versus budget | | COVERED-38 | 38 | |
| SSRS: Budget control activity | | COVERED-38 | 38 | |
| SSRS: Budget register entries / Budget details | | COVERED-38 | 38 | |
| SSRS: Budget plan (printed) | | COVERED-38 | 38 | |
| SSRS: Budget transactions by model | | COVERED-38 | 38 | |
| SSRS: Ledger budget (legacy LedgerBudget) | | SUPERSEDED (register entries) | 38 | |

### 12.4.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Budget transfers (process transfer register entries) | | COVERED-38 | 38 | |
| Budget → Recalculate budget control balances | Rebuild available balances | COVERED-38 | 38 | |
| Budget → Carry forward budget / Year-end budget processing | | COVERED-38 | 38 | |
| Budget planning → Generate budget plan from source (GL actuals, previous plan, FA, projects, demand forecast, HR positions) | | PARTIAL-38 (generating plans from position/HR forecasts and demand forecasts is not named) | 38 | Gap register |
| Budget planning → Budget plan workflow stage advance (batch) | | COVERED-38 | 38 | |
| Budget planning → Generate budget register entry from budget plan | | COVERED-38 | 38 | |
| Budget → Allocate budget (allocation terms / period allocation keys / dimension allocation) | | COVERED-38 | 38 | |
| Budget → Import/export budget register entries (Excel/DIXF) | | COVERED-5 | 5 | Excel import/export |
| Budget → Delete budget balances / purge | | COVERED-38 | 38 | |
| Budget control → Activate/deactivate configuration | | COVERED-38 | 38 | |
| Budget → Encumbrance year-end process (public sector) | | OUT-OF-SCOPE (public sector) | — | |

### 12.4.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Budgeting parameters | Default budget model, budget transfer journal, number sequences, budget balances update | COVERED-38 | 38 | |
| Basic budgeting → Budget models | Models with submodels, stop/blocked | COVERED-38 | 38 | Budget versions |
| Basic budgeting → Budget codes | Codes per budget type with workflow | COVERED-38 | 38 | |
| Basic budgeting → Budget cycles / Budget cycle time spans | Fiscal cycle mapping | COVERED-38 | 38 | |
| Basic budgeting → Budget transfer rules | Allowed transfers between dimensions | COVERED-38 | 38 | |
| Basic budgeting → Budget allocation terms | Allocation by dimension percent | COVERED-38 | 38 | |
| Basic budgeting → Period allocation keys | Spread across periods | COVERED-38 | 38 | |
| Basic budgeting → Dimensions for budgeting | Which financial dimensions are budgeted | COVERED-38 | 38 | |
| Basic budgeting → Budget register entry workflow | | COVERED-38 | 38 | |
| Budget control → Budget control configuration (Define parameters, Budget funds available calculation, Documents and journals, Assign budget models, Define budget control rules, Budget groups, Over budget permissions, Activate) | Full budget control configuration | COVERED-38 | 38 | Budget checks with tolerances and overrides |
| Budget control → Budget control rules / Budget groups | | COVERED-38 | 38 | |
| Budget control → User groups with override permissions | | COVERED-38 | 38 | |
| Budget planning → Budget planning configuration (Scenarios, Stages, Workflows, Stage rules, Budget plan priorities, Templates, Columns, Layouts, Parameters) | | COVERED-38 | 38 | |
| Budget planning → Budget planning processes | Process per cycle × ledger × org hierarchy | COVERED-38 | 38 | |
| Budget planning → Budget planning templates (Excel) | | PARTIAL-38 (Excel worksheets) | 38 | |
| Budget planning → Budget plan document layouts (D365) | | COVERED-38 | 38 | |
| Budget planning → Position forecasting (D365 HR-driven) | Forecast positions/compensation into plan | PARTIAL-38 (position forecasting) | 38 | Depends on HR wave 42; sequencing note in gap register |
| Budget planning → Budget planning allocation schedules | | COVERED-38 | 38 | |
| Fixed asset budgets (link) | | COVERED-37 | 37 | |
| Project budgets (link) | | COVERED-41 | 41 | |
| Workflow → Budgeting workflows | | COVERED-4 | 4 | |
| Number sequences (budgeting) | | COVERED-5 | 5 | |

## 12.5 Cash and bank management

Everything lands in wave 36 except payment generation (19) and customer receipts (30). Bills of exchange / promissory notes and postdated checks are gaps (proposed wave 36).

### 12.5.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Bank accounts (list page) | BankAccountTableListPage | All bank accounts | COVERED-36 | 36 | |
| Bank account details | BankAccountTable | Account, currency, main account, check setup (layout, numbering), positive pay, SEPA/IBAN, bank groups, reconciliation settings, signatories | COVERED-36 | 36 | |
| Bank transactions (per account) | BankAccountTrans | Posted bank transactions | COVERED-36 | 36 | |
| Bank statements (list page, Advanced bank reconciliation R2+) | BankStatementListPage | Imported statements (camt.053, BAI2, MT940) | COVERED-36 | 36 | |
| Bank statement details | BankStatement | Lines, status, validation | COVERED-36 | 36 | |
| Bank reconciliation worksheets (Advanced) | BankReconciliationWorksheet | Match statement to book, rules, reconcile | COVERED-36 | 36 | |
| Account reconciliation (classic bank reconciliation) | BankReconciliation | Manual tick-off reconciliation | SUPERSEDED (advanced reconciliation with rules and ML suggestions, wave 36) | 36 | Classic mode retained as manual matching within the worksheet |
| Checks (list) | BankChequeTable | Issued checks, void, reprint, status | COVERED-36 | 36 | Check management |
| Deposit slips | BankDeposit | Deposit of customer receipts | COVERED-36 | 36 | |
| Letters of credit (Import/Export letters of credit, R2+) | BankLC* (BankLCImport, BankLCExport) | LC lifecycle, shipments, margins, charges | COVERED-36 | 36 | |
| Letters of guarantee | BankLG* | Guarantee issue, extend, cancel, liquidate | COVERED-36 | 36 | |
| Bank facilities (Facility agreements) | BankFacilityAgreement | Facility limits per bank, types | COVERED-36 | 36 | |
| Bank → Positive pay transactions | BankPositivePayTable | Export status | COVERED-19 | 19 | |
| Bank → Cash flow forecast (D365 Cash flow forecasting workspace) | CashFlowForecast* | Forecast from AP/AR/budget/projects | COVERED-36 | 36 | |
| Cash management (D365 Cash positioning) | | Intraday positions | COVERED-36 | 36 | camt.052 |
| Bank → Bank document (LC/LG) transactions | BankDocument* | | COVERED-36 | 36 | |
| Bank → Bills of exchange / Promissory notes (links) | | | GAP | — | Propose wave 36: bills of exchange and promissory notes (see AR/AP) |
| Bank → Postdated checks (links) | | | GAP | — | Propose wave 36: postdated checks |
| Bank → Payment journal (bank-originated) | LedgerJournalTable (bank) | | COVERED-36 | 36 | |
| Bank → Bank account signatories (D365) | BankAccountSignatory | Signers and approvals | COVERED-36 | 36 | |
| Intercompany netting / cash pooling (in-house banking) | — (not in AX; SAP IHC) | | COVERED-36 | 36 | In-house banking basics |

### 12.5.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Bank transactions inquiry | | COVERED-36 | 36 | |
| Bank balance inquiry / Bank account balance by date | | COVERED-36 | 36 | |
| Check inquiry / Check register | | COVERED-36 | 36 | |
| Reconciliation inquiry / Reconciliation history | | COVERED-36 | 36 | |
| Bank statement inquiry (imported statements) | | COVERED-36 | 36 | |
| Deposit slip inquiry | | COVERED-36 | 36 | |
| Letter of credit / guarantee inquiries | | COVERED-36 | 36 | |
| Cash flow forecast inquiry (Cash flow forecasts by period) | | COVERED-36 | 36 | |
| Bank facility usage inquiry | | COVERED-36 | 36 | |
| Positive pay inquiry | | COVERED-19 | 19 | |
| SSRS: Bank transactions / Bank account statement (internal) | | COVERED-36 | 36 | |
| SSRS: Check register / Checks report / Void checks | | COVERED-36 | 36 | |
| SSRS: Reconciliation summary / Bank reconciliation report | | COVERED-36 | 36 | |
| SSRS: Deposit slip (document) | | COVERED-36 | 36 | |
| SSRS: Cash flow forecast report | | COVERED-36 | 36 | |
| SSRS: Bank account list / Bank groups | | COVERED-36 | 36 | |
| SSRS: Payment reconciliation (bank) | | COVERED-36 | 36 | |
| SSRS: Letter of credit / guarantee documents | | COVERED-36 | 36 | |
| SSRS: Unreconciled transactions | | COVERED-36 | 36 | |
| SSRS: Bank transaction types list | | COVERED-36 | 36 | |

### 12.5.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Import bank statements (batch) | camt.053/BAI2/MT940 import | COVERED-36 | 36 | |
| Reconcile bank statements (auto-match batch) | | COVERED-36 | 36 | |
| Cash flow forecast → Calculate / Update forecast | | COVERED-36 | 36 | |
| Positive pay → Generate positive pay file | | COVERED-19 | 19 | |
| Checks → Void / Reprint / Delete checks | | COVERED-36 | 36 | |
| Bank → Foreign currency revaluation (bank accounts) | | COVERED-12 | 12 | |
| Bank → Transfer bank accounts (bank transfer journal) | Account-to-account transfer | COVERED-36 | 36 | |
| Letters of credit → Expiry / margin update batches | | COVERED-36 | 36 | |
| Bank → Delete reconciliation history / Compress | | SUPERSEDED (append-only) | 46 | |
| Bank → Post bank fees / NSF | | COVERED-36 | 36 | |
| Bank connectivity → Send/receive files (SFTP/EBICS/host-to-host) | | COVERED-36 | 36 | Vault-managed keys |
| Cash positioning → Intraday statement import (camt.052) | | COVERED-36 | 36 | |

### 12.5.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Cash and bank management parameters — General | Allow checks for bank accounts, check number method, prenotes, bank statement reconciliation defaults, advanced reconciliation enabled, letter of credit/guarantee settings | COVERED-36 | 36 | |
| Cash and bank management parameters — Number sequences | Check, deposit, statement sequences | COVERED-5 | 5 | |
| Bank groups | Bank master (routing, SWIFT, address) | COVERED-36 | 36 | |
| Bank transaction types | Classify transactions (fee, interest, check) with GL accounts | COVERED-36 | 36 | |
| Bank transaction groups | Grouping for reconciliation | COVERED-36 | 36 | |
| Check layout (per bank account) | Layout, MICR, signature, copies, check form | COVERED-19 | 19 | Check printing; management 36 |
| Bank statement format (Advanced bank reconciliation: Bank statement formats, Transformation (XSLT), Reconciliation matching rules, Matching rule sets) | Import formats and rules | COVERED-36 | 36 | Standard ISO 20022/BAI2/MT940 parsers; custom via ER wave 48 |
| Bank reconciliation matching rules / rule sets | Auto-match criteria, tolerance, grouping | COVERED-36 | 36 | |
| Positive pay formats | | COVERED-19 | 19 | |
| Letters of credit → Bank documents facility types, LC types (import/export), LC charges, Document types | | COVERED-36 | 36 | |
| Letters of guarantee → Guarantee types, Letters of guarantee parameters | | COVERED-36 | 36 | |
| Bank facilities → Facility agreements, Facility groups, Facility types | | COVERED-36 | 36 | |
| Cash flow forecasting setup (Cash flow forecast setup D365: AP/AR/budget/ledger/inventory/project/sales/purchase/ external forecasts; dependent accounts; liquidity accounts) | | COVERED-36 | 36 | |
| Payment purpose codes / Bank codes (country) | | COVERED-36 | 36 | |
| Bank account → Prenote setup | | PARTIAL-19 (prenote) | 19 | |
| Bank → Bank holidays / Payment calendar | | PARTIAL-19 (payment calendar) | 19 | |
| Bills of exchange / promissory notes setup (bank side) | | GAP | — | Propose wave 36: bills of exchange |
| Postdated checks parameters (R3 APAC) | | GAP | — | Propose wave 36: postdated checks |
| Bank connectivity (D365 Bank connectivity, Electronic payment setup) | Host-to-host, SWIFT, EBICS | COVERED-36 | 36 | |
| Workflow → Bank workflows (bank account change approval, signatory) | | COVERED-36 | 36 | |
| Number sequences (bank) | | COVERED-5 | 5 | |
| Credit card → Payment services (link) | | COVERED-30 | 30 | |
| NSF information (bank fees for returned payments) | | COVERED-36 | 36 | |

## 12.6 Fixed assets

The AX fixed asset module (asset groups, books/value models, depreciation profiles, journals, mass update, physical inventory) maps to wave 37; leases (D365 Asset leasing) are also wave 37.

### 12.6.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Fixed assets (list page) | AssetTableListPage | All assets | COVERED-37 | 37 | |
| Fixed asset details | AssetTable | General, technical info (serial, make/model), location, responsible worker, insurance, lease, sale/scrap, value models/books, financial dimensions | COVERED-37 | 37 | |
| Value models (AX 2012) / Books (D365) per asset | AssetBook (AssetBookTable) | Depreciation profile, service life, posting layer, status, acquisition info | COVERED-37 | 37 | Multi-book |
| Depreciation books (tax, AX 2012) | AssetDepBookTable | Non-ledger tax books | COVERED-37 | 37 | Tax books merged into "books" per D365 |
| Fixed asset journal (GL journal, type fixed asset) | LedgerJournalTable (AssetJournal) | Acquisition, acquisition adjustment, depreciation, depreciation adjustment, revaluation, write-up/down, disposal sale/scrap, extraordinary depreciation, bonus depreciation | COVERED-37 | 37 | Full transaction set |
| Fixed asset budget journal | AssetBudgetJournal | Budget entries for assets | COVERED-37 | 37 | |
| Depreciation book journal (tax) | AssetDepBookJournal | Non-posting tax depreciation | COVERED-37 | 37 | |
| Fixed asset transactions | AssetTrans | Posted asset ledger | COVERED-37 | 37 | |
| Fixed asset → Physical inventory (D365 asset count) | AssetPhysicalInventory | Barcode count and reconciliation | COVERED-37 | 37 | |
| Fixed asset → Lease (D365 Asset leasing: Lease, Lease book, Payment schedule, Lease classification) | AssetLease* | Lease records with classification tests | COVERED-37 | 37 | IFRS 16 / ASC 842 |
| Fixed asset → Asset leasing journals (lease payment, interest, depreciation) | AssetLeaseJournal | | COVERED-37 | 37 | |
| Fixed asset → Asset acquisition from PO/AP invoice (Fixed asset on purchase line) | PurchLine (asset) | Create/acquire asset from purchase | COVERED-37 | 37 | Acquisition from AP invoice |
| Fixed asset → Acquisition from project (Estimate posting to FA) | ProjTable (asset) | CIP to asset | COVERED-37 | 37 | CIP from projects |
| Fixed asset → Acquisition from inventory (Inventory to fixed asset journal) | InventJournalTable (asset) | Issue inventory to asset | COVERED-37 | 37 | |
| Fixed asset → Split / Transfer (Mass transfer, Transfer fixed asset) | AssetTransfer | Transfer between dimensions/locations, split | COVERED-37 | 37 | |
| Fixed asset → Reclassify (change group) | AssetReclassify | | COVERED-37 | 37 | |
| Fixed asset → Fixed asset additions | AssetAddition | Sub-components/additions tracking | PARTIAL-37 (asset additions (non-financial component records) not named) | 37 | Gap register |
| Fixed asset → Maintenance / Service (asset maintenance records) | AssetMaintenance | | COVERED-44 | 44 | EAM |
| Fixed asset → Insurance records | AssetInsurance | | PARTIAL-37 (insurance records not named) | 37 | Gap register |
| Fixed asset → Asset statement (D365 Fixed asset roll forward) | AssetRollForward | | COVERED-37 | 37 | |

### 12.6.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Fixed asset transactions inquiry | | COVERED-37 | 37 | |
| Fixed asset balances inquiry (Fixed asset balances by book) | NBV per book/period | COVERED-37 | 37 | |
| Depreciation profile preview (Profile inquiry) | Projected depreciation | COVERED-37 | 37 | |
| Fixed asset budget inquiry | | COVERED-37 | 37 | |
| Value model / book inquiry | | COVERED-37 | 37 | |
| Lease inquiries (lease liability, ROU schedules) | | COVERED-37 | 37 | |
| SSRS: Fixed asset list / Fixed asset base data | | COVERED-37 | 37 | |
| SSRS: Fixed asset transactions report | | COVERED-37 | 37 | |
| SSRS: Fixed asset balances / Fixed asset note (statement) | | COVERED-37 | 37 | |
| SSRS: Fixed asset roll forward | | COVERED-37 | 37 | |
| SSRS: Fixed asset depreciation forecast | | COVERED-37 | 37 | |
| SSRS: Fixed assets due for replacement | | COVERED-37 | 37 | |
| SSRS: Fixed asset insurance report | | PARTIAL-37 (insurance) | 37 | |
| SSRS: Fixed asset acquisitions / disposals / Scrapped assets | | COVERED-37 | 37 | |
| SSRS: Fixed asset listing by location / responsible | | COVERED-37 | 37 | |
| SSRS: Lease reports (liability amortization, ROU, maturity analysis, disclosure) | | COVERED-37 | 37 | |
| SSRS: Bonus depreciation report (US) | | COVERED-37 | 37 | |
| SSRS: Fixed asset physical inventory report | | COVERED-37 | 37 | |
| SSRS: Fixed asset budget report | | COVERED-37 | 37 | |
| SSRS: Reconciliation Fixed assets/Ledger | | COVERED-37 | 37 | Subledger reconciles to GL |

### 12.6.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Depreciation proposal (create depreciation journal) | Depreciation run per book/period | COVERED-37 | 37 | |
| Depreciation book → Depreciation proposal (tax book) | | COVERED-37 | 37 | |
| Mass update (change depreciation profile/service life for many assets) | | COVERED-37 | 37 | |
| Mass transfer | | COVERED-37 | 37 | |
| Revaluation of fixed asset groups (Consumption/Revaluation batch) | | COVERED-37 | 37 | |
| Copy fixed asset / Copy value model | | COVERED-37 | 37 | |
| Fixed asset budget → Transfer to budget register | | COVERED-38 | 38 | |
| Fixed asset → Periodic depreciation (batch) | | COVERED-37 | 37 | |
| Consumption depreciation → Consumption proposal (units of production) | | COVERED-37 | 37 | |
| Asset leasing → Batch journal creation (payments, interest, amortization) / Lease impairment / Lease modification | | COVERED-37 | 37 | |
| Fixed asset → Year-end / Close value models | | COVERED-37 | 37 | |
| Fixed asset → Delete/compress transactions | | SUPERSEDED (append-only) | 46 | |
| Fixed asset → Physical inventory count import (barcode) | | COVERED-37 | 37 | |
| Fixed asset → Import assets (DIXF/Excel) | | COVERED-10 | 10 | |

### 12.6.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Fixed assets parameters — General | Automatically create depreciation adjustments, minimum depreciation, consider prior-year depreciation, bonus depreciation, consumption depreciation, capitalization threshold, delete asset on journal post, allow multiple acquisitions, low-value pool (AU), default value models | COVERED-37 | 37 | AU low-value pool is OOS country |
| Fixed assets parameters — Purchase orders | Allow asset acquisition from PO, create asset during receipt/invoice, check capitalization threshold | COVERED-37 | 37 | |
| Fixed assets parameters — Number sequences | | COVERED-5 | 5 | |
| Fixed asset groups | Group defaults: value models, number sequence, depreciation profiles, service life, posting profile defaults | COVERED-37 | 37 | |
| Fixed asset posting profiles | Ledger accounts per transaction type × group/asset × book | COVERED-37 | 37 | |
| Value models (AX) / Books (D365) | Posting layer, calendar, depreciation profile, alternative profile, round-off, derived value models/books (auto-post to multiple) | COVERED-37 | 37 | |
| Depreciation books (tax) | Non-posting books | COVERED-37 | 37 | |
| Depreciation profiles | Methods: straight line service life, straight line life remaining, reducing balance, manual, factor, consumption, 200%/175%/150%/125% reducing, manual, RB/SL (US), Lo-Value pool; conventions (half year, mid month, mid quarter, full month, half-year start of life); period frequency | COVERED-37 | 37 | MACRS tables |
| Depreciation profiles → Manual depreciation schedules (percentages per period) | | COVERED-37 | 37 | |
| Consumption factors / Consumption units | Units-of-production setup | COVERED-37 | 37 | |
| Depreciation conventions | | COVERED-37 | 37 | |
| Fixed asset → Special depreciation allowance (bonus depreciation, US) | | COVERED-37 | 37 | |
| Fixed asset → Journal names (fixed asset journal, budget journal, depreciation book journal) | | COVERED-37 | 37 | |
| Fixed asset → Location / Major types / Condition / Asset attributes | Reference lists | COVERED-37 | 37 | |
| Fixed asset → Insurance types / Insurance companies | | PARTIAL-37 (insurance) | 37 | |
| Fixed asset → Reason codes (fixed assets) | | PARTIAL-11 (reason codes) | 11 | |
| Fixed asset → Transaction reversal setup | | COVERED-37 | 37 | |
| Fixed asset → Fixed asset activity codes | | COVERED-37 | 37 | |
| Fixed asset → Sorting / Fixed asset groups → Value model defaults | | COVERED-37 | 37 | |
| Fixed asset → Derived value models / derived books | Post to multiple books from one journal | COVERED-37 | 37 | |
| Asset leasing → Asset leasing parameters, Lease books, Lease groups, Payment frequencies, Classification criteria (ownership transfer, purchase option, lease term %, PV %), Journal names, Posting accounts | | COVERED-37 | 37 | |
| Fixed asset → Physical inventory parameters (barcode, worker) | | COVERED-37 | 37 | |
| Workflow → Fixed asset workflows (journal approval) | | COVERED-4 | 4 | |
| Number sequences (fixed assets) | | COVERED-5 | 5 | |
| Fixed asset → Country-specific (RU/PL/CZ depreciation, DE "AfA tables") | | OUT-OF-SCOPE (country pack beyond v1 set; DE AfA handled in DE pack wave 48 if required) | — | |


## 12.7 Cost accounting

AX 2012 R3 ships the legacy *Cost accounting* module (COS: dimensions, cost categories, cost lines, service categories, allocations, budget cost). D365 replaced it with the *Cost accounting ledger* (cost objects, cost elements, allocation policies, statistical measures, cost control workspace). Passage builds the D365 model in wave 38; every legacy COS form is therefore `SUPERSEDED` by its wave-38 equivalent.

### 12.7.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Cost accounting dimensions (legacy) | COSDimension | Cost center/cost unit dimensions | SUPERSEDED (cost object dimensions and hierarchies, wave 38) | 38 | |
| Cost categories (legacy COS) | COSCostCategory | Cost types mapped from ledger accounts | SUPERSEDED (cost elements, wave 38) | 38 | |
| Cost lines (legacy, manual cost postings) | COSLine | Manual cost entries | SUPERSEDED (cost accounting ledger journals, wave 38) | 38 | |
| Cost journal (legacy COS journal) | COSJournalTable | Manual cost accounting journal | SUPERSEDED (cost entries journal, wave 38) | 38 | |
| Service categories / Service quantities (legacy) | COSServiceCategory | Activity quantities for allocation | SUPERSEDED (statistical measures, wave 38) | 38 | |
| Cost distribution (legacy) | COSAllocation (distribution) | Distribute costs by key | SUPERSEDED (allocation policies: distribution, wave 38) | 38 | |
| Cost allocation (legacy) | COSAllocation | Allocate costs via allocation key | SUPERSEDED (allocation policies: assessment, wave 38) | 38 | |
| Cost budget (legacy COS budget) | COSBudget | Budget by cost category/dimension | SUPERSEDED (budget data in cost accounting ledger from budget register entries, wave 38) | 38 | |
| Cost accounting ledger (D365) | CAMCostAccountingLedger | Ledger per fiscal calendar/currency with GL/budget source | COVERED-38 | 38 | |
| Cost objects / Cost object dimensions (D365) | CAMDimension | Cost center, product, project cost objects | COVERED-38 | 38 | |
| Cost elements / Cost element dimensions (D365) | CAMCostElement | Primary (from GL) and secondary cost elements | COVERED-38 | 38 | |
| Dimension hierarchies (D365 cost accounting) | CAMDimensionHierarchy | Hierarchies for allocation/reporting | COVERED-38 | 38 | |
| Cost entries (D365) | CAMCostEntry | Processed source entries | COVERED-38 | 38 | |
| Cost control workspace (D365) | CAMCostControlWorkspace | Manager cost reporting with variance and drill | COVERED-38 | 38 | |
| Statistical measures / Statistical entries (D365) | CAMStatisticalMeasure | Quantities (headcount, kWh) used as allocation bases | COVERED-38 | 38 | |
| Cost behavior / Cost classification (fixed/variable) (D365) | CAMCostBehavior | Classify fixed vs variable | PARTIAL-38 (cost behavior (fixed/variable split) policies not named) | 38 | Gap register |
| Internal orders (SAP concept; AX via projects) | — | | COVERED-38 | 38 | Internal orders named in wave 38 |
| Activity-based costing | — (not in AX) | | COVERED-38 | 38 | |

### 12.7.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Cost transactions inquiry (legacy) | | SUPERSEDED (cost entries, wave 38) | 38 | |
| Cost balances by dimension (legacy) | | SUPERSEDED (cost control workspace) | 38 | |
| Cost accounting ledger → Cost entries inquiry (D365) | | COVERED-38 | 38 | |
| Cost control → Cost by cost object/element with budget variance (D365) | | COVERED-38 | 38 | |
| Allocation results inquiry (D365: Allocation journal) | | COVERED-38 | 38 | |
| SSRS: Cost accounting (legacy) — Cost distribution sheet, Cost unit statement, Cost category list, Cost allocation list | | SUPERSEDED (cost control reports, wave 38/46) | 38 | |
| D365: Cost control workspace reports / Power BI cost accounting analysis | | COVERED-46 | 46 | Management cubes |
| SSRS: Service category quantities | | SUPERSEDED (statistical measures) | 38 | |

### 12.7.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Transfer ledger transactions to cost accounting (legacy) | | SUPERSEDED (source data processing from GL, wave 38) | 38 | |
| Cost distribution / Cost allocation processing (legacy) | | SUPERSEDED (overhead calculation, wave 38) | 38 | |
| Cost budget transfer (legacy) | | SUPERSEDED | 38 | |
| Source data processing (D365: GL, budget, statistical) | Pull entries into cost ledger | COVERED-38 | 38 | |
| Overhead calculation (D365: cost behavior, cost distribution, overhead rate, cost allocation) | Run allocation policies in sequence | COVERED-38 | 38 | Allocation cycles converge |
| Cost accounting ledger → Reverse overhead calculation | | COVERED-38 | 38 | |
| Statistical measure data import | | COVERED-38 | 38 | |
| Cost ledger period close | | COVERED-38 | 38 | |

### 12.7.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Cost accounting parameters (legacy) | Dimension mapping, journal names | SUPERSEDED (cost accounting ledger setup) | 38 | |
| Cost accounting dimensions / Cost category groups / Allocation keys / Calculation versions (legacy) | | SUPERSEDED | 38 | |
| D365: Cost accounting ledgers | Calendar, currency, exchange rate type | COVERED-38 | 38 | |
| D365: Dimensions (cost element, cost object, statistical) and Dimension member data providers (GL main accounts, financial dimensions, budget) | | COVERED-38 | 38 | |
| D365: Dimension hierarchies | | COVERED-38 | 38 | |
| D365: Data source providers / Source data (GL actual, budget, budget register) | | COVERED-38 | 38 | |
| D365: Policies — Cost behavior policies, Cost distribution policies, Overhead rate policies, Cost allocation policies, Cost rollup policies (D365 2020+) | | PARTIAL-38 (cost behavior and cost rollup policies not named; allocation policies are) | 38 | |
| D365: Policy assignments / Policy versions | | COVERED-38 | 38 | |
| D365: Cost control workspace configurations | | COVERED-38 | 38 | |
| D365: Cost accounting ledger security (dimension access) | | COVERED-2 | 2 | OPA policies |
| D365: Statistical measure providers | | COVERED-38 | 38 | |
| D365: Cost accounting parameters | | COVERED-38 | 38 | |
| Number sequences (cost accounting) | | COVERED-5 | 5 | |

## 12.8 Cost management (D365)

D365's *Cost management* module consolidates inventory accounting (AX Inventory management → Setup → Costing), costing versions, costing sheets and the inventory value reports. Passage splits it: inventory costing and inventory close in wave 20, product costing/cost roll-up/costing sheets/variances in wave 39, landed cost in wave 34.

### 12.8.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Costing versions (Costing version setup) | CostingVersion | Standard/planned cost versions, blocking, fallback, site | COVERED-20 | 20 | Costing versions in 20; BOM calc in 39 |
| Item prices (Costing version maintenance: pending/active prices) | InventItemPrice (CostingVersionMaintenance) | Pending cost, activation, cost breakdown by cost group | COVERED-39 | 39 | |
| Cost object (D365 inventory accounting cost objects) | CostObject | Item × dimension cost object with running average | COVERED-20 | 20 | |
| Inventory value / Cost explorer | CostExplorer (InventCostTransExplorer) | Trace cost from issue to receipts | COVERED-20 | 20 | Settlement trace |
| Closing and adjustment (Inventory close list) | InventClosing | Close runs, recalculation, adjustments (on-hand, transactions) | COVERED-20 | 20 | |
| Inventory posting (link) | InventPosting | | COVERED-16 | 16 | |
| Production → Standard cost transactions (variances) | InventCostTrans | Purchase/production variances | COVERED-39 | 39 | |
| Cost management → Inventory accounting journals (Moving average revaluation, Standard cost revaluation, Inventory cost revaluation) | InventJournalTable (revaluation) | Revalue on-hand | COVERED-39 | 39 | Standard cost revaluation in 39; moving average settlement in 20 |
| Landed cost (D365 Landed cost module: Voyages, Shipping containers, Folios, Cost estimates, Goods in transit) | ITMVoyage* | Multi-leg inbound costing with estimates and actuals | PARTIAL-34 (landed cost allocation is named; D365 voyage/container/folio tracking and goods-in-transit orders are not) | 34 | Gap register |
| Global inventory accounting (D365 add-in: multiple cost ledgers / accounting conventions) | GIA ledgers | Parallel inventory valuation (IFRS vs local) | GAP | — | Propose wave 20: parallel inventory valuation ledgers (one costing method per ledger group) aligned with wave 12 parallel ledgers |

### 12.8.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Inventory value report (configurable: Inventory value report by resource/resource group, by dimension) | On-hand value vs GL by date | COVERED-20 | 20 | |
| Inventory value report storage / configuration (D365) | Persisted report runs | COVERED-20 | 20 | |
| Inventory aging report (D365) | Aging of on-hand by receipt date | PARTIAL-20 (inventory aging buckets not named) | 20 | Gap register |
| Cost rollup inquiry / Calculated cost breakdown (BOM calculation results) | | COVERED-39 | 39 | |
| Costing version comparison / Compare item prices | | COVERED-39 | 39 | Cost estimate comparison |
| Inventory close inquiry / Settlements / Adjustment history | | COVERED-20 | 20 | |
| Standard cost variance inquiry (purchase price variance, production variances) | | COVERED-39 | 39 | |
| SSRS: Inventory value (Physical inventory by inventory dimension, Inventory value by dimension) | | COVERED-20 | 20 | |
| SSRS: Inventory cost/value reconciliation (Inventory/Ledger reconciliation) | | COVERED-20 | 20 | |
| SSRS: Cost estimates and costing (BOM calculation report) | | COVERED-39 | 39 | |
| SSRS: Indirect cost transactions / Costing sheet listing | | COVERED-39 | 39 | |
| SSRS: Variance analysis statement (production) | | COVERED-39 | 39 | |
| SSRS: Settled transactions / Inventory close report | | COVERED-20 | 20 | |
| SSRS: Items without cost | | COVERED-39 | 39 | |
| Landed cost reports (voyage cost, cost variance by voyage) | | PARTIAL-34 (landed cost voyages) | 34 | |

### 12.8.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Closing and adjustment → Close inventory | Settle issues vs receipts per model | COVERED-20 | 20 | |
| Closing and adjustment → Recalculation | Interim recalculation | COVERED-20 | 20 | |
| Closing and adjustment → Adjustment (on-hand / transactions) | Manual value adjustments | COVERED-20 | 20 | |
| Closing and adjustment → Cancel close / Reopen | | COVERED-20 | 20 | |
| Costing version → Calculation (BOM calculation, mass) | Roll up costs | COVERED-39 | 39 | Mass roll 50k items < 15 min |
| Costing version → Activate pending prices (mass activation) | Activate standard costs; revalue inventory | COVERED-39 | 39 | |
| Costing version → Copy item prices between versions | | COVERED-39 | 39 | |
| Inventory accounting → Post inventory to ledger (transfer) | | SUPERSEDED (synchronous posting via universal journal) | 16 | |
| Moving average → Revaluation for moving average | | COVERED-20 | 20 | |
| Standard cost → Revaluation on activation / Conversion to standard cost (Standard cost conversion checklist) | Convert items from actual to standard | PARTIAL-39 (standard cost conversion checklist (switching costing model mid-life) not named) | 39 | Gap register |
| Landed cost → Voyage cost allocation / Goods in transit receipts | | PARTIAL-34 (landed cost voyages) | 34 | |
| Cost management → Inventory value report generation (batch) | | COVERED-20 | 20 | |
| Costing → Delete pending prices / Clean up costing versions | | COVERED-39 | 39 | |
| Inventory cost (D365) → Update running average / Refresh on-hand cost | | COVERED-20 | 20 | |

### 12.8.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Inventory and warehouse management parameters — Costing/ Inventory accounting tab (D365 Cost management parameters) | Post physical inventory, post financial inventory, break-even cost, use latest cost price, variance by cost group | COVERED-20 | 20 | |
| Costing versions | Type (standard/planned), block activation/changes, fallback (active/none/version), site/date filters, explosion mode | COVERED-39 | 39 | |
| Cost groups | Direct material, direct manufacturing, indirect, undefined; cost group type and behavior (fixed/variable) | COVERED-39 | 39 | |
| Costing sheets (Costing sheet setup) | Nodes: totals, price nodes (cost groups), surcharge/rate indirect costs with absorption basis | COVERED-39 | 39 | |
| Indirect cost (surcharge / rate) definitions and versions | Rates per costing version/site | COVERED-39 | 39 | |
| Cost categories (routing costs; shared with Production) | Cost category price per version, cost group | COVERED-39 | 39 | |
| BOM calculation groups | Cost price model, sales price model, warnings, stop explosion | COVERED-39 | 39 | |
| Cost basis types (D365 commodity pricing) | | COVERED-26 | 26 | Commodity price based on index; see PI §12.23 |
| Inventory posting profiles (link) | | COVERED-16 | 16 | |
| Item model groups → Costing method (FIFO/LIFO/LIFO date/weighted avg/weighted avg date/standard/moving average), include physical value, fixed receipt price, latest cost price | | COVERED-20 | 20 | |
| Inventory value report configuration (D365: Inventory value reports setup — columns, dimensions, resource type) | | COVERED-20 | 20 | |
| Inventory aging report setup (D365) | | PARTIAL-20 (inventory aging) | 20 | |
| Landed cost setup (D365: Landed cost parameters, Cost types, Cost type codes, Auto cost, Apportionment, Shipping container types, Vessels, Voyage templates, Legs, Journeys, Tracking) | | PARTIAL-34 (landed cost voyages) | 34 | |
| Global inventory accounting setup (ledgers, accounting policies, conventions) | | GAP | — | Propose wave 20: parallel inventory valuation ledgers |
| Standard cost conversion setup | | PARTIAL-39 (standard cost conversion) | 39 | |
| Number sequences (cost management) | | COVERED-5 | 5 | |

## 12.9 Compliance and internal controls

AX 2012's *Compliance and internal controls* module holds internal control documentation (process library/files), the risk library, audit policies (R3 audit workbench) and the database log link. Environmental sustainability was removed before R3. Passage: SoD and SOC 2 evidence in wave 48; audit policies are a gap proposed for wave 48; database log is wave 5.

### 12.9.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Internal controls → Files (process documents) | SysComplianceFiles (EP) | Document library for control documentation | PARTIAL-48 (SOC 2 evidence automation is named; a controls/process document library is not) | 48 | Gap register |
| Internal controls → Process library / Process documents | SysComplianceProcess | Hierarchical process documentation with risk/control links | PARTIAL-48 (process library) | 48 | |
| Internal controls → Risk library | SysComplianceRisk | Risks with controls | PARTIAL-48 (risk library) | 48 | |
| Audit → Audit cases | AuditCase (CaseDetail audit) | Cases from audit policy violations | PARTIAL-48 (audit policies) | 48 | |
| Audit → Audit policy violations list | AuditPolicyViolation | Violations by policy rule | PARTIAL-48 (audit policies) | 48 | |
| Database log (link to Sys admin) | SysDatabaseLog | | COVERED-5 | 5 | Audit log from CDC |
| Environmental sustainability (substance flows, process maps) | ENV* | | OUT-OF-SCOPE (removed from AX 2012 R3; v2 ESG module candidate) | — | |
| Compliance → Segregation of duties conflicts (link to Sys admin) | SysSecSegregationOfDutiesConflict | | COVERED-48 | 48 | SoD engine |
| Compliance → Electronic signatures (link) | SysSignature | | GAP | — | Propose wave 48: electronic signatures (21 CFR Part 11 style) on configured tables/fields with reason codes and signature log |

### 12.9.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Audit policy violations inquiry | | PARTIAL-48 (audit policies) | 48 | |
| Audit case inquiry | | PARTIAL-48 (audit policies) | 48 | |
| Database log inquiry | | COVERED-5 | 5 | |
| Electronic signature log inquiry | | GAP | — | Propose wave 48: electronic signatures |
| Segregation of duties conflicts / unresolved conflicts | | COVERED-48 | 48 | |
| SSRS: Audit policy report / Audit case report | | PARTIAL-48 (audit policies) | 48 | |
| SSRS: Database log report | | COVERED-5 | 5 | |
| SSRS: Segregation of duties rules / conflicts | | COVERED-48 | 48 | |
| SSRS: Electronic signature report | | GAP | — | Propose wave 48: electronic signatures |
| SSRS: Internal control documents | | PARTIAL-48 (process library) | 48 | |
| SOC 2 control evidence reports (Passage) | | COVERED-48 | 48 | |

### 12.9.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Audit → Run audit policies (batch: expense reports, vendor invoices, purchase orders, GL journals) | Rule-based post-hoc audit (duplicates, keywords, sampling, list search) | PARTIAL-48 (audit policies) | 48 | |
| Audit → Close/archive audit cases | | PARTIAL-48 (audit policies) | 48 | |
| Database log → Clean up database log | | SUPERSEDED (immutable audit store with retention policy wave 48) | 48 | |
| SoD → Verify compliance of user-role assignments (batch) | | COVERED-48 | 48 | |
| Access certification campaigns (Passage) | | COVERED-48 | 48 | |
| Electronic signature → Reset/revoke signatures | | GAP | — | Propose wave 48: electronic signatures |

### 12.9.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Compliance parameters | Portal site, file locations | SUPERSEDED (no Enterprise Portal; compliance workspace wave 48) | 48 | |
| Audit → Audit policies (policy framework: policy rule types — expense/vendor invoice/PO/GL journal; rule types: duplicate, keyword, list search, sampling, premium/mileage) | | PARTIAL-48 (audit policies) | 48 | |
| Audit → Audit policy rule types | | PARTIAL-48 (audit policies) | 48 | |
| Audit → Audit case categories | | PARTIAL-48 (audit policies) | 48 | |
| Audit → Audit keyword lists / Audit sampling parameters | | PARTIAL-48 (audit policies) | 48 | |
| Database log setup (tables/fields to log) | | COVERED-5 | 5 | Field-level audit flag in metadata |
| Electronic signature → Electronic signature parameters, Signature requirements (table/field), Reason codes for signatures | | GAP | — | Propose wave 48: electronic signatures |
| Segregation of duties rules (link) | | COVERED-48 | 48 | |
| Risk/control types, Process document templates | | PARTIAL-48 (process library) | 48 | |
| Workflow → Audit case workflow | | COVERED-4 | 4 | |

## 12.10 Travel and expense

Expense management (expense reports, cash advances, travel requisitions, policies, per diems, credit card import, project integration) lands in wave 43; approval infrastructure in wave 4; VAT recovery relies on wave 13.

### 12.10.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Expense reports (list page) | TrvExpTableListPage | My/all expense reports by status | COVERED-43 | 43 | |
| Expense report details | TrvExpTable / TrvExpTrans | Lines by category, itemization, receipts, guests, project/dimension, policy violations | COVERED-43 | 43 | |
| Expense reports → Unattached credit card transactions (Credit card transactions pool) | TrvPBSMaindata (TrvUnreconciledExpense) | Imported card transactions awaiting assignment | COVERED-43 | 43 | Credit-card feed import |
| Cash advances (Cash advance requests) | TrvCashAdvance | Request, approve, pay via AP, settle against reports | PARTIAL-43 (cash advances are not named) | 43 | Gap register |
| Travel requisitions | TrvRequisitionTable | Pre-trip approval with estimates | PARTIAL-43 (travel requisitions / pre-approval are not named) | 43 | Gap register |
| Expense reports → Itemization / Guests / Per diem lines | TrvExpTransItemization | Hotel itemization, meals per diem | COVERED-43 | 43 | |
| Expense reports → Receipts (attach, OCR) | TrvDocuView | | COVERED-43 | 43 | OCR receipts |
| Expense reports → Intercompany expenses (R3) | TrvExpTrans (intercompany) | Charge another legal entity | PARTIAL-40 (intercompany expenses not named in 40 or 43) | 40 | Gap register |
| Expense reports → Expense report workflow / Approval | Workflow | | COVERED-43 | 43 | |
| Delegates (expense delegates) | TrvDelegates | Enter on behalf | COVERED-43 | 43 | |
| Expense reports → Posted expense reports / Expense transactions | TrvExpTransPosted | | COVERED-43 | 43 | |
| Expense → Mobile receipt capture (D365 mobile workspace) | | | COVERED-43 | 43 | PWA |

### 12.10.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Expense report inquiry / Expense transactions | | COVERED-43 | 43 | |
| Credit card transactions inquiry (imported / unreconciled) | | COVERED-43 | 43 | |
| Cash advance inquiry | | PARTIAL-43 (cash advances) | 43 | |
| Travel requisition inquiry | | PARTIAL-43 (travel requisitions) | 43 | |
| Policy violations inquiry | | COVERED-43 | 43 | |
| Expense reports pending approval (Work items) | | COVERED-4 | 4 | |
| SSRS: Expense report (printed) / Expense report details | | COVERED-43 | 43 | |
| SSRS: Expense transactions by category / employee / project | | COVERED-46 | 46 | |
| SSRS: Cash advance report | | PARTIAL-43 (cash advances) | 43 | |
| SSRS: Unreconciled credit card transactions | | COVERED-43 | 43 | |
| SSRS: Policy violations report | | COVERED-43 | 43 | |
| SSRS: Per diem rates list | | COVERED-43 | 43 | |
| SSRS: Expense audit (audit policy results) | | PARTIAL-48 (audit policies) | 48 | |
| SSRS: VAT recovery report (expense tax) | | PARTIAL-13 (VAT recovery on expenses (partial recoverability per country) not named) | 13 | Gap register |

### 12.10.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Import credit card transactions (card feed: AMEX GL1025/Visa VCF/MasterCard CDF) | | COVERED-43 | 43 | |
| Post expense reports (batch) | Posts to AP (reimbursement) and GL/project | COVERED-43 | 43 | Reimbursement via AP |
| Reconcile/settle cash advances | | PARTIAL-43 (cash advances) | 43 | |
| Expense → Mileage rate update / Per diem rate import | | COVERED-43 | 43 | |
| Expense → Run audit policies on expense reports | | PARTIAL-48 (audit policies) | 48 | |
| Expense → Delete unposted / Clean up card pool | | COVERED-43 | 43 | |
| Expense → Recover VAT (ER-based claim) | | PARTIAL-13 (VAT recovery) | 13 | |
| Expense → Intercompany expense posting (due-to/due-from) | | PARTIAL-40 (intercompany expenses) | 40 | |

### 12.10.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Expense management parameters — General | Default currency, posting to AP (vendor per worker), mileage/per diem defaults, require receipts threshold, allow itemization, project posting | COVERED-43 | 43 | |
| Expense management parameters — Policies / Workflow / Credit card | Policy violation behaviors, approval, card import settings | COVERED-43 | 43 | |
| Expense management parameters — Intercompany / Cash advance | | PARTIAL-43 (cash advances) | 43 | |
| Expense management parameters — Number sequences | | COVERED-5 | 5 | |
| Expense categories (shared categories flagged for expense; expense type, default payment method, tax group, ledger account) | | COVERED-43 | 43 | |
| Shared categories (link to Org admin category hierarchy) | | COVERED-8 | 8 | Category hierarchies |
| Payment methods (expense: company card, personal, cash advance) | Who pays / who is reimbursed | COVERED-43 | 43 | |
| Per diem locations / Per diem rates / Per diem rate tiers (meals, hotel, first/last day %) | | COVERED-43 | 43 | |
| Mileage rates / Mileage tiers | | COVERED-43 | 43 | |
| Expense policies (Policy framework: expense policy rules by category: amount limits, receipts, per diem, mileage, hotel; actions: warn/justify/block) | | COVERED-43 | 43 | |
| Expense policy violation types / justifications | | COVERED-43 | 43 | |
| Credit card → Credit card types, Credit card category codes (MCC mapping), Credit card transaction import formats, Card issuer connection | | COVERED-43 | 43 | |
| Delegates setup | | COVERED-43 | 43 | |
| Cash advance → Cash advance parameters, posting | | PARTIAL-43 (cash advances) | 43 | |
| Travel requisition → Requisition policies / types | | PARTIAL-43 (travel requisitions) | 43 | |
| Expense → Tax configuration (recoverable VAT per country, tax groups on expenses) | | PARTIAL-13 (VAT recovery) | 13 | |
| Expense → Project integration (billable categories, line properties) | | COVERED-41 | 41 | |
| Expense → Audit policies (link) | | PARTIAL-48 (audit policies) | 48 | |
| Expense → Display fields / Expense report fields configuration (optional fields) | | COVERED-3 | 3 | Metadata extensions |
| Expense → Ledger posting (expense posting profiles / posting by category) | | COVERED-43 | 43 | |
| Workflow → Expense workflows (report, line-level, cash advance, requisition) | | COVERED-4 | 4 | |
| Number sequences (expense) | | COVERED-5 | 5 | |
| Expense → Mobile app configuration (D365) | | COVERED-43 | 43 | |

## 12.11 Project management and accounting

The project module is wave 41 (WBS, resources, journals, billing, estimates, grants), with time registration shared with wave 25, expenses with wave 43 and fixed-asset capitalization with wave 37.

### 12.11.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Projects (list page) | ProjTableListPage | All projects, hierarchy | COVERED-41 | 41 | |
| Project details | ProjTable | Type (T&M, fixed-price, investment, cost, internal, time), stage, group, contract, funding, dimensions, WBS, invoice/billing, budget, resource | COVERED-41 | 41 | |
| Project contracts | ProjInvoiceTable | Contract with funding sources, funding rules, limits, billing rules | COVERED-41 | 41 | |
| Project quotations | SalesQuotationTable (project) | Quote with WBS/estimates, convert to project | PARTIAL-41 (project quotations with convert-to-project are not named; sales quotations are in 27) | 41 | Gap register |
| Work breakdown structure | ProjWBS (HierarchyTreeLookup) | Tasks, dependencies, effort, resources, estimates | COVERED-41 | 41 | |
| Project hierarchy / Project tree | ProjTable (parent) | Parent/sub-projects | COVERED-41 | 41 | |
| Project stage change (Create/In process/Finished…) | ProjTable (stage) | Stage gates with rules | COVERED-41 | 41 | |
| Project forecasts (Hour, Expense, Item, Fee, On-account forecasts) | ProjForecast* | Forecast models | COVERED-41 | 41 | |
| Project budgets / Budget revisions (Project budgeting) | ProjBudget | Original budget, revisions, budget control per project | COVERED-41 | 41 | |
| Timesheets (list page) | TSTimesheetTableListPage | Weekly timesheets with workflow | PARTIAL-41 (hour journals are named; weekly timesheet UI with favorites/workflow is not explicit) | 41 | Gap register (note wave 25 time registration covers shop floor, not project timesheets) |
| Timesheet details | TSTimesheetTable | Lines, comments, submit | PARTIAL-41 (timesheets) | 41 | |
| Hour journals | ProjJournalTable (hour) | Post hours | COVERED-41 | 41 | |
| Expense journals (project) | LedgerJournalTable (project expense) | Post expenses | COVERED-41 | 41 | |
| Item journals (project) | InventJournalTable (project) | Consume inventory on project | COVERED-41 | 41 | |
| Fee journals | ProjJournalTable (fee) | Post fees/revenue | COVERED-41 | 41 | |
| Beginning balance journals | ProjJournalTable (beginning balance) | Opening WIP/transactions on migration | PARTIAL-41 (beginning balance journals for project migration not named; wave 49 covers project WIP migration) | 49 | Gap register |
| Project purchase orders / Purchase requisitions (project) | PurchTable (project) | PO tied to project | COVERED-41 | 41 | |
| Project item requirements | SalesTable (item requirement) | Item requirements consumed from inventory | COVERED-41 | 41 | |
| Project sales orders | SalesTable (project) | | COVERED-41 | 41 | |
| Project invoice proposals | ProjProposalJour | Build invoice from transactions | COVERED-41 | 41 | |
| Project invoices (posted) | ProjInvoiceJour | | COVERED-41 | 41 | |
| On-account transactions (Milestones) | ProjOnAccTrans | Fixed-price billing milestones | COVERED-41 | 41 | |
| Estimates (Estimate projects, Completed contract / Percentage completion) | ProjControlPeriodTable (ProjEstimate) | Revenue recognition for fixed-price | COVERED-41 | 41 | |
| Grants (Grant management, R2 PSA) | ProjGrant | Grant records, grant types, funding | COVERED-41 | 41 | Customer funding/grants |
| Project → Resources (Resource list, Resource assignments, Resource availability, Resource requests) | ProjResource*, ResRoleAssignment | Role-based resource booking | COVERED-41 | 41 | |
| Project → Resource scheduling (Resource scheduling form, R3) | ResResourceScheduling | Book resources to WBS tasks | COVERED-41 | 41 | |
| Project → Activities (Activity list for projects) | smmActivities | Activities on projects | PARTIAL-45 (generic activities/case management framework) | 45 | |
| Project → Committed costs | ProjCommittedCost | Commitments from PO/requisition/timesheet | COVERED-41 | 41 | |
| Project → Funding limits / Funding rules | ProjFundingLimit | | COVERED-41 | 41 | |
| Project → Billing rules (Unit of delivery, Progress, Milestone, Fee, Time and material, Retainage) | ProjBillingRule | | COVERED-41 | 41 | |
| Project → Retention terms (customer/vendor retention) | ProjRetentionTerms | Hold back % until milestone | PARTIAL-41 (customer and vendor retention (retainage) terms are not named) | 41 | Gap register |
| Project → Project transactions (posted) | ProjTrans* | | COVERED-41 | 41 | |
| Project → Adjustments (Project transaction adjustments) | ProjAdjust | Reallocate posted transactions | COVERED-41 | 41 | |
| Project → Subcontracts / Vendor invoices for project | | | COVERED-41 | 41 | |
| Project → Project workspace (D365 Project management workspace) | | | COVERED-41 | 41 | |
| Microsoft Project integration (MPP sync) | ProjMSProject* | | SUPERSEDED (WBS scheduling native; MS Project import/export via wave 5 Excel/CSV and wave 35 APIs) | 41 | |

### 12.11.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Project transactions inquiry (posted transactions, by type) | | COVERED-41 | 41 | |
| Project statements (Project statement inquiry: cost, revenue, WIP, profit) | | COVERED-41 | 41 | |
| Cash flow (project) | | COVERED-36 | 36 | |
| Committed cost inquiry | | COVERED-41 | 41 | |
| Invoice proposals / Invoices inquiry | | COVERED-41 | 41 | |
| Estimate inquiry (Estimate details) | | COVERED-41 | 41 | |
| Budget vs actual (project budget balances) | | COVERED-41 | 41 | |
| Resource utilization / availability inquiry | | COVERED-41 | 41 | |
| Timesheet inquiries (missing timesheets, timesheet hours) | | PARTIAL-41 (timesheets) | 41 | |
| Hour utilization | | COVERED-41 | 41 | |
| Project ledger reconciliation (Project/Ledger) | | COVERED-41 | 41 | |
| Funding source / contract inquiries | | COVERED-41 | 41 | |
| Grant inquiries | | COVERED-41 | 41 | |
| SSRS: Project statement / Project statement by category | | COVERED-41 | 41 | |
| SSRS: Project invoice (document) / Invoice proposal / Credit note | | COVERED-41 | 41 | |
| SSRS: Hour utilization / Employee utilization | | COVERED-41 | 41 | |
| SSRS: Project transactions (hours, expenses, items, fees, on-account) | | COVERED-41 | 41 | |
| SSRS: WIP / Accrued revenue / Cost-to-complete / Percentage complete | | COVERED-41 | 41 | |
| SSRS: Project list / Project base data | | COVERED-41 | 41 | |
| SSRS: Cash flow forecast (project) | | COVERED-36 | 36 | |
| SSRS: Committed costs report | | COVERED-41 | 41 | |
| SSRS: Timesheet report / Missing timesheets | | PARTIAL-41 (timesheets) | 41 | |
| SSRS: Estimate report / Estimate journal | | COVERED-41 | 41 | |
| SSRS: Project budget / Budget balances | | COVERED-41 | 41 | |
| SSRS: Resource scheduling / Resource assignment | | COVERED-41 | 41 | |
| SSRS: Project invoice journal / Customer invoice register | | COVERED-41 | 41 | |
| SSRS: Earned value (CPI/SPI) | | COVERED-41 | 41 | EVM |
| SSRS: Project profitability | | COVERED-46 | 46 | |
| SSRS: Grant report | | COVERED-41 | 41 | |

### 12.11.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Journals → Post hour/expense/item/fee journals (batch) | | COVERED-41 | 41 | |
| Timesheets → Timesheet workflow / Post timesheets / Create periodic timesheets | | PARTIAL-41 (timesheets) | 41 | |
| Estimates → Create / Post / Reverse / Eliminate estimates | Fixed-price revenue recognition cycle | COVERED-41 | 41 | |
| Invoice proposals → Create invoice proposals (batch) / Post | | COVERED-41 | 41 | |
| Project → Transfer to fixed assets (investment project estimate elimination) | | COVERED-37 | 37 | |
| Project → Accrue revenue (T&M WIP accrual) | | COVERED-41 | 41 | |
| Project → Reverse accrued revenue | | COVERED-41 | 41 | |
| Project → Cost control / Budget control recalc | | COVERED-41 | 41 | |
| Project → Project stage update (mass) | | COVERED-41 | 41 | |
| Project → Line property update / Adjust transactions (mass) | | COVERED-41 | 41 | |
| Project → Intercompany project invoicing (R3 intercompany timesheets/expenses) | Cross-entity resource lending and invoicing | PARTIAL-40 (intercompany project resourcing/invoicing not named) | 40 | Gap register |
| Project → Project purchase requisition release | | COVERED-17 | 17 | |
| Project → Copy project / Copy WBS from template | | COVERED-41 | 41 | |
| Project → Delete / Close projects | | COVERED-41 | 41 | |
| Project → Project forecast → Copy/transfer forecast to budget | | COVERED-41 | 41 | |
| Project → Project sales tax / VAT on invoice proposals | | COVERED-13 | 13 | |
| Project → Retention release (vendor/customer) | | PARTIAL-41 (retention) | 41 | |
| Project → Resource availability recalculation | | COVERED-41 | 41 | |
| Project → Grant periodic (grant renewals, closeouts) | | COVERED-41 | 41 | |
| Project → Project invoice e-invoicing (ER) | | COVERED-35 | 35 | |

### 12.11.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Project management and accounting parameters — General | Default project types, stages, line property, WBS defaults, ledger posting (balance/P&L), hour/expense/item/fee posting, cost price defaults, sales price defaults, invoice proposal rules | COVERED-41 | 41 | |
| Project parameters — Financial (ledger posting rules by project type: cost, revenue, WIP, accrued) | | COVERED-41 | 41 | |
| Project parameters — Cost control / Budget / Estimate / Invoice / Timesheet / Scheduling / Resources / Journals / Number sequences | | COVERED-41 | 41 | Timesheet tab PARTIAL per 12.11.1 |
| Project groups | Posting rules per group (P&L/balance), WIP accounts, estimate method, cost template | COVERED-41 | 41 | |
| Project types / Project stages / Project stage rules | | COVERED-41 | 41 | |
| Categories → Project categories / Category groups / Shared categories | Hour, expense, item, fee categories with posting | COVERED-41 | 41 | |
| Line properties | Chargeable / capitalize / efficiency % | COVERED-41 | 41 | |
| Posting → Ledger posting setup (Cost accounts, Revenue accounts by category/project/customer) | | COVERED-41 | 41 | |
| Posting → Project posting profile (customer invoice) | | COVERED-41 | 41 | |
| Prices → Cost prices (hour, expense), Sales prices (hour, expense, fee, subscription), Transfer prices (intercompany) | Price lists by resource/project/category | COVERED-41 | 41 | Transfer prices PARTIAL-40 for intercompany |
| Estimates → Cost templates / Period types / Estimate periods | Completion methods | COVERED-41 | 41 | |
| Project contracts → Funding types / Funding rule templates | | COVERED-41 | 41 | |
| Billing → Billing rule templates / Retention terms | | PARTIAL-41 (retention) | 41 | |
| Timesheets → Timesheet periods / Period types / Timesheet parameters (favorites, delegates, workflow by project) | | PARTIAL-41 (timesheets) | 41 | |
| Resources → Resource roles, Competencies, Calendars, Resource groups, Availability parameters | | COVERED-41 | 41 | |
| Scheduling → Scheduling parameters (WBS: default calendar, dependencies) | | COVERED-41 | 41 | |
| WBS → WBS templates | | COVERED-41 | 41 | |
| Project → Project templates | | COVERED-41 | 41 | |
| Project → Validation (Project/category, Project/worker, Category/worker validation groups) | Who may post what to which project | COVERED-41 | 41 | |
| Budget → Project budget parameters / Budget control configuration (project) | | COVERED-41 | 41 | |
| Grants → Grant types, Grant statuses, CFDA numbers (US) | | COVERED-41 | 41 | |
| Indirect costs (Indirect cost component groups, Indirect cost components, Indirect cost rules — R3 PSA) | Overhead on project hours | PARTIAL-41 (project indirect cost components/rules not named; cost accounting allocations in 38 do not apply to project transactions) | 41 | Gap register |
| Journals → Journal names (project journals) | | COVERED-41 | 41 | |
| Forms → Form setup (project invoices) / Print management | | PARTIAL-46 (print management routing) | 46 | |
| Workflow → Project workflows (timesheet, expense, hour journal, project stage) | | COVERED-4 | 4 | |
| Number sequences (project) | | COVERED-5 | 5 | |
| Microsoft Project → MS Project integration parameters | | SUPERSEDED (native WBS) | 41 | |
| Intercompany → Intercompany project parameters (lending/borrowing entity, timesheet/expense/vendor invoice transfer) | | PARTIAL-40 (intercompany project) | 40 | |

## 12.12 Human resources

AX 2012 R3 Human resources (workers, positions, jobs, departments, benefits, leave, compensation, performance, skills/competencies, recruitment, courses, questionnaires, injury/illness, loaned items, worker compliance) maps to wave 42, with absence calendars shared with wave 25 and payroll in 43. Courses, questionnaires, injury/illness and loaned items are not in any wave and are listed as gaps.

### 12.12.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Workers (list page) | HcmWorkerListPage | All workers (employees + contractors) | COVERED-42 | 42 | Party with Employee role |
| Worker details | HcmWorker | Profile, employment, position assignments, compensation, leave, benefits, identification (SSN/passport), bank accounts, contacts, personal contacts, financial dimensions, competencies | COVERED-42 | 42 | PII vaulting |
| Employees / Contractors (list pages) | HcmEmployeeListPage / HcmContractorListPage | Filtered worker lists | COVERED-42 | 42 | |
| Employment (worker employment history, effective-dated) | HcmEmployment | Hire, terminate, rehire with dates | COVERED-42 | 42 | |
| Positions (list page) | HcmPositionListPage | Position hierarchy, vacancies | COVERED-42 | 42 | |
| Position details | HcmPosition | Job, department, reports-to, position type, duration, FTE, worker assignment, payroll, budget | COVERED-42 | 42 | |
| Jobs | HcmJob | Job templates: title, type, function, tasks, skills, education, compensation level | COVERED-42 | 42 | |
| Departments (Org admin link) | OMDepartment | | COVERED-6 | 6 | |
| Personnel actions (D365: hire, transfer, terminate, position change with workflow) | HcmPersonnelAction | Action-based changes with approval | COVERED-42 | 42 | Effective-dated with workflow |
| Leave and absence → Absence registrations / Absence journals (AX 2012) | HRMAbsenceTable | Register absences, approve, transfer | COVERED-42 | 42 | |
| Leave and absence → Leave requests / Leave balances (D365) | HcmLeaveRequest | ESS requests with balances | COVERED-42 | 42 | |
| Compensation → Fixed compensation / Variable compensation (plans, enrollments, events) | HcmFixedCompensationEvent / HcmVariableComp* | Salary structures, merit, bonus plans | COVERED-42 | 42 | Compensation plans |
| Benefits → Benefit enrollment (worker benefits) | HcmWorkerBenefit | Enroll in plans, dependents, beneficiaries | COVERED-42 | 42 | |
| Benefits → Open enrollment / Benefits management (D365 benefits management with life events) | BenefitsManagement* | Enrollment periods, eligibility processing | COVERED-42 | 42 | |
| Performance → Performance journals / Discussions / Goals / Reviews (D365 performance management) | HcmPerformance* | Goals, reviews, ratings | COVERED-42 | 42 | |
| Competencies → Skills / Certificates / Education / Courses / Tests / Positions of trust / Professional experience (worker competencies) | HcmPersonSkill, HcmPersonCertificate, … | Worker competency records | COVERED-42 | 42 | Skills, certifications |
| Competencies → Skill mapping / Skill gap analysis | HcmSkillMapping | Search workers by skills | PARTIAL-42 (skill mapping/gap analysis not named) | 42 | Gap register |
| Recruitment → Recruitment projects / Applicants / Applications / Application routing | HRMRecruitingTable, HRMApplicant, HRMApplication | Applicant tracking | COVERED-42 | 42 | Recruitment pipeline basics |
| Courses (training) → Courses, Course sessions, Course registrations, Instructors, Course locations | HRMCourseTable | Training administration | GAP | — | Propose wave 42: learning/training management (courses, sessions, registrations, instructors, completion to competencies) |
| Questionnaires → Questionnaires, Questionnaire schedules, Answer sessions, Results analysis | KMQuestionnaire, KMKnowledgeCollectorSchedule | Surveys/tests (360 feedback, course evaluations) | GAP | — | Propose wave 42: questionnaire engine (design, schedule, answer, analyze) reused by performance reviews and quality audits |
| Injury and illness → Injury/illness incidents, Treatments, Body parts, OSHA logs | HRMInjuryIncident | Workplace incident tracking | GAP | — | Propose wave 42: injury and illness incident register with OSHA 300 extracts (US pack) |
| Loaned items → Loaned items, Loan items | HRMLoanTable | Equipment lent to workers | GAP | — | Propose wave 42: loaned items (assets issued to workers) tied to EAM assets wave 44 |
| Worker compliance (US) → I-9, ADA requirements, Immigration/visa, Union agreements | HcmWorkerI9Document, HcmPersonVisa | Compliance records | PARTIAL-42 (country-specific compliance fields framework is named; I-9/visa/union specifics come with US pack) | 48 | |
| Worker → Worker tasks / reminders / Checklists (onboarding) | HcmWorkerTask | Onboarding/offboarding checklists | PARTIAL-42 (onboarding checklists not named) | 42 | Gap register |
| Employee self-service (Role center, EP) | EP ESS pages | View/edit personal info, requests | COVERED-42 | 42 | ESS/MSS portal |
| Manager self-service | EP MSS pages | Team view, approvals | COVERED-42 | 42 | |
| Worker → Worker action workflow (position changes) | HcmWorkerActionWorkflow | | COVERED-42 | 42 | |
| Teams (Org admin: Teams, Team types) | OMTeam | Cross-org teams for collaboration | PARTIAL-6 (teams (ad hoc cross-org groups) not named; hierarchies are) | 6 | Gap register |
| Workforce → Worker calendars / Working time (link to calendars) | WorkCalendarTable | | COVERED-9 | 9 | |
| Human resources → Cases (HR cases) | CaseDetail | | PARTIAL-45 (generic case management) | 45 | |

### 12.12.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Worker inquiries (Workers by department/position, Seniority, Birthdays, Anniversaries) | | COVERED-42 | 42 | |
| Position inquiries (Vacant positions, Position hierarchy, Headcount) | | COVERED-42 | 42 | |
| Absence inquiries (Absence statistics, Absence per worker) | | COVERED-42 | 42 | |
| Benefit inquiries (Enrollments, Eligibility) | | COVERED-42 | 42 | |
| Compensation inquiries (Pay history, Compensation events) | | COVERED-42 | 42 | |
| Competency inquiries (Skills by worker, Certificate expiry) | | COVERED-42 | 42 | |
| Recruitment inquiries (Applicants by project) | | COVERED-42 | 42 | |
| Course inquiries | | GAP | — | Propose wave 42: learning management |
| Questionnaire results analysis | | GAP | — | Propose wave 42: questionnaire engine |
| Injury/illness inquiries | | GAP | — | Propose wave 42: injury and illness |
| Loaned items inquiry | | GAP | — | Propose wave 42: loaned items |
| SSRS: Worker list / Worker base data / Worker contact list | | COVERED-42 | 42 | |
| SSRS: Position hierarchy / Position list / Vacancies | | COVERED-42 | 42 | |
| SSRS: Absence report / Absence statistics | | COVERED-42 | 42 | |
| SSRS: Benefit enrollment report / Benefit cost | | COVERED-42 | 42 | |
| SSRS: Compensation report / Pay rate history | | COVERED-42 | 42 | |
| SSRS: Skills / Certificates / Education reports | | COVERED-42 | 42 | |
| SSRS: Recruitment reports (Applicant list, Application status) | | COVERED-42 | 42 | |
| SSRS: Course reports (Attendance, Course confirmation letters) | | GAP | — | Propose wave 42: learning management |
| SSRS: Questionnaire reports | | GAP | — | Propose wave 42: questionnaire engine |
| SSRS: OSHA 300/300A/301 (US) | | GAP | — | Propose wave 42: injury and illness (US pack output wave 48) |
| SSRS: EEO-1 / VETS (US compliance) | | PARTIAL-42 (US compliance reporting with wave 48 US pack) | 48 | |
| SSRS: Headcount / Turnover analytics | | COVERED-46 | 46 | |
| SSRS: Organization chart | | COVERED-6 | 6 | Hierarchy designer |

### 12.12.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Absence → Transfer absence registrations (to payroll/projects) | | COVERED-42 | 42 | |
| Leave → Accrue leave balances (D365 accrual processing) | | COVERED-42 | 42 | Accrual fixtures |
| Leave → Carry-forward / expire balances | | COVERED-42 | 42 | |
| Benefits → Mass benefit enrollment / Update benefit rates / Expire benefits / Process life events | | COVERED-42 | 42 | |
| Compensation → Process compensation events (merit cycle) | | COVERED-42 | 42 | |
| Positions → Update position assignments (effective dating batch) / Position budget sync | | COVERED-42 | 42 | |
| Recruitment → Close recruitment projects / Mass applicant correspondence | | COVERED-42 | 42 | |
| Courses → Course confirmations / Transfer course completion to competencies | | GAP | — | Propose wave 42: learning management |
| Questionnaires → Distribute questionnaires (schedule) / Collect results | | GAP | — | Propose wave 42: questionnaire engine |
| Worker → Delete worker history / Anonymize (GDPR) | | COVERED-48 | 48 | Subject requests |
| Worker → Reminders (certificate expiry, probation end) | | PARTIAL-42 (reminder/alert engine; see alerts gap in §12.31) | 42 | |
| HR → Import workers (DIXF) | | COVERED-10 | 10 | |
| HR → Year-end (US ACA 1095-C) | | PARTIAL-42 (ACA reporting with US pack) | 48 | |

### 12.12.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Human resources parameters — General / Recruitment / Workers / Absence / Benefits / Compensation / Number sequences / Shared (HR shared parameters: identification, worker numbering) | | COVERED-42 | 42 | |
| Organization → Departments, Jobs, Positions setup (Position types, Position hierarchy types, Job functions, Job types, Job tasks, Areas of responsibility, Titles) | | COVERED-42 | 42 | |
| Workers → Employment types, Reasons for termination, Employment categories, Seniority, Identification types, Ethnic origins, Veteran statuses, Marital status, Honorifics | Reference lists | COVERED-42 | 42 | |
| Workers → Personal contact relationship types / Dependents / Beneficiaries | | COVERED-42 | 42 | |
| Absence → Absence codes / Absence code groups / Absence setups / Absence statuses / Leave types (D365: Leave and absence plans, Accrual schedules, Leave calendars, Holiday corrections) | | COVERED-42 | 42 | |
| Benefits → Benefit types, Benefit plans, Benefit options, Benefit eligibility policies, Benefit rates, Contribution calculation rates, Benefit periods, Life events (D365), Flex credits (D365) | | COVERED-42 | 42 | |
| Compensation → Compensation grids, Compensation levels, Reference point setups, Fixed compensation plans, Variable compensation plans, Compensation actions, Pay frequencies, Compensation regions, Surveys | | COVERED-42 | 42 | |
| Performance → Rating models, Review types, Goal types, Discussion types | | COVERED-42 | 42 | |
| Competencies → Skill types, Skills, Rating models, Certificate types, Education disciplines, Education levels, Institutions, Course types | | COVERED-42 | 42 | |
| Recruitment → Recruitment project types, Application statuses, Applicant sources, Media, Correspondence actions, Application routing templates | | COVERED-42 | 42 | |
| Courses → Course types, Course groups, Course locations, Classrooms, Instructors, Agenda templates | | GAP | — | Propose wave 42: learning management |
| Questionnaires → Questionnaire types, Question types, Answer groups, Result groups, Media types, Questionnaire parameters | | GAP | — | Propose wave 42: questionnaire engine |
| Injury and illness → Incident types, Body parts, Injury/illness treatments, Severity levels, Outcomes | | GAP | — | Propose wave 42: injury and illness |
| Loaned items → Loan types | | GAP | — | Propose wave 42: loaned items |
| Worker compliance (US) → I-9 document types, ADA requirements, Visa types, Union agreements | | PARTIAL-42 (US compliance) | 48 | |
| Worker → Worker tasks / Checklist templates (onboarding) | | PARTIAL-42 (onboarding checklists) | 42 | |
| Teams → Team types, Teams | | PARTIAL-6 (teams) | 6 | |
| Employee self-service parameters (EP) / Manager self-service parameters | | SUPERSEDED (ESS/MSS portal wave 42) | 42 | |
| HR → Workflow (worker actions, leave, compensation, benefits) | | COVERED-4 | 4 | |
| HR → Number sequences | | COVERED-5 | 5 | |
| HR → Privacy / PII masking policies (Passage) | | COVERED-42 | 42 | |
| HR → Calendars (working time) | | COVERED-9 | 9 | |
| HR → Country-specific fields (localization framework) | | COVERED-42 | 42 | |
| HR → Payroll information on worker (link) | | COVERED-43 | 43 | |

## 12.13 Payroll

AX 2012 R2/R3 US Payroll (earnings, benefits/deductions, taxes via Symmetry tax engine, pay statements, GL posting, W-2) maps to wave 43 (country-pluggable gross-to-net with US and one EU pack, plus provider adapter).

### 12.13.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Earnings statements (list page) | PayrollEarningStatementListPage | Generated earnings per worker per period | COVERED-43 | 43 | |
| Earnings statement details | PayrollEarningStatement | Earning lines (salary, hourly, overtime, retro), source (timesheet, schedule, manual) | COVERED-43 | 43 | |
| Pay statements (list page) | PayrollPayStatementListPage | Gross-to-net statements | COVERED-43 | 43 | |
| Pay statement details | PayrollPayStatement | Earnings, benefits, deductions, taxes, net pay, payment method | COVERED-43 | 43 | |
| Worker → Payroll information (Worker payroll setup: tax regions, tax codes, earnings, benefits, garnishments, pay cycle, payment method/direct deposit) | PayrollWorkerDetails (HcmWorker → Payroll tab) | Per-worker payroll setup | COVERED-43 | 43 | |
| Worker → Benefit (payroll) enrollment / Deduction enrollment | PayrollWorkerBenefit | Payroll deduction/contribution enrollment | COVERED-43 | 43 | |
| Worker → Garnishments and tax levies | PayrollWorkerGarnishment | Garnishment rules, limits, disposable income | COVERED-43 | 43 | |
| Worker → Tax codes / Tax regions (W-4 info) | PayrollWorkerTaxCode | Federal/state withholding settings | COVERED-43 | 43 | |
| Worker → Earning codes / Default earnings (recurring earnings) | PayrollWorkerEarningCode | Standing earnings | COVERED-43 | 43 | |
| Worker → Payroll calendar assignment / Pay cycles | | | COVERED-43 | 43 | |
| Worker → Direct deposit / Payment methods | PayrollWorkerBankAccountDisbursement | Split deposits | COVERED-43 | 43 | |
| Payroll → Payroll transactions / Posted payroll journal (GL journal from payroll) | LedgerJournalTable (payroll) | | COVERED-43 | 43 | |
| Payroll → Vendor invoices for payroll liabilities (taxes, benefit providers) | VendInvoice (payroll) | | COVERED-43 | 43 | Liabilities via AP |
| Payroll → Tax (Symmetry tax engine integration) | PayrollTaxEngine* | External tax calculation | COVERED-43 | 43 | Versioned rule sets; provider adapter alternative |
| Payroll → Retroactive earnings | PayrollEarningStatement (retro) | Retro pay lines | COVERED-43 | 43 | |
| Payroll → Pay statement reversal / Void | | | COVERED-43 | 43 | |
| Payroll → Time registration import (from T&A / timesheets) | | | COVERED-25 | 25 | Export to payroll from wave 25 |

### 12.13.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Earnings statement inquiry / Pay statement inquiry | | COVERED-43 | 43 | |
| Worker payroll history (earnings, taxes, deductions YTD) | | COVERED-43 | 43 | |
| Tax inquiries (Tax summary by region, Tax liabilities) | | COVERED-43 | 43 | |
| Benefit/deduction register inquiry | | COVERED-43 | 43 | |
| Garnishment inquiry | | COVERED-43 | 43 | |
| Payroll posting inquiry (journal) | | COVERED-43 | 43 | |
| SSRS: Pay statement (document) / Check stub | | COVERED-43 | 43 | |
| SSRS: Payroll register / Payroll summary | | COVERED-43 | 43 | |
| SSRS: Tax register / Tax liability / Form 941 / State unemployment (SUTA) | US statutory | COVERED-48 | 48 | US pack |
| SSRS: W-2 / W-3 / Electronic W-2 | | COVERED-43 | 43 | Year-end forms framework |
| SSRS: Benefit register / Deduction register / Garnishment register | | COVERED-43 | 43 | |
| SSRS: Earnings register | | COVERED-43 | 43 | |
| SSRS: Payroll journal (GL) | | COVERED-43 | 43 | |
| SSRS: Workers compensation report | | PARTIAL-43 (workers' compensation class codes/reporting not named) | 48 | Gap register |
| SSRS: Payroll audit / reconciliation reports | | COVERED-43 | 43 | |
| SSRS: Positive pay for payroll checks | | COVERED-19 | 19 | |
| SSRS: New hire reporting (US state) | | PARTIAL-43 (new hire reporting with US pack) | 48 | |

### 12.13.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Generate earnings statements | Create earnings per pay period from schedules/time | COVERED-43 | 43 | |
| Release earnings statements | | COVERED-43 | 43 | |
| Generate pay statements (calculate gross-to-net) | | COVERED-43 | 43 | |
| Submit pay statements for payment (payment journal, direct deposit file, checks) | | COVERED-43 | 43 | NACHA via wave 19 |
| Post payroll (GL journal) | | COVERED-43 | 43 | |
| Issue payments / Print payroll checks | | COVERED-19 | 19 | |
| Pay statement reversal | | COVERED-43 | 43 | |
| Retroactive pay processing | | COVERED-43 | 43 | |
| Generate vendor invoices for payroll liabilities | | COVERED-43 | 43 | |
| Tax updates (apply tax engine updates) | | COVERED-43 | 43 | Versioned rule sets |
| Year-end → Generate W-2 / Electronic filing / Archive | | COVERED-43 | 43 | |
| Benefit accrual (payroll-based leave accrual: PTO) | | COVERED-42 | 42 | |
| Payroll → Arrears recovery processing | | PARTIAL-43 (deduction arrears recovery not named) | 43 | Gap register |
| Payroll → Pay period roll / Close pay period | | COVERED-43 | 43 | |
| Payroll → Provider export (ADP/Gusto adapter) | | COVERED-43 | 43 | |
| Payroll → Import time from T&A | | COVERED-25 | 25 | |

### 12.13.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Payroll parameters — General / Earnings / Posting / Payment / Tax / Number sequences | Default earning codes, accrual, posting defaults, payment journal names, tax engine | COVERED-43 | 43 | |
| Pay cycles and pay periods | Weekly/biweekly/semimonthly/monthly, period generation | COVERED-43 | 43 | |
| Payroll calendars (D365: work cycles) | | COVERED-43 | 43 | |
| Earning codes | Type (hourly/salary/overtime/retro), rate basis, multiplier, tax treatment, productive, accounting | COVERED-43 | 43 | |
| Earning code groups / Earning rate groups | | COVERED-43 | 43 | |
| Benefits (payroll) → Benefit elements: Benefit types, Benefit plans, Benefit options; Benefit accrual plans (PTO accrual with tiers) | Deduction/contribution plans with tax rules | COVERED-43 | 43 | |
| Taxes → Tax regions, Tax codes (federal/state/local), Tax groups, Tax engine configuration (Symmetry), Tax update schedule, Workers' compensation codes | | COVERED-43 | 43 | Workers' comp PARTIAL |
| Garnishments → Garnishment types, Disposable income definitions, Garnishment rules, Administrative fees | | COVERED-43 | 43 | |
| Schedules → Work schedules (schedule templates for salaried) | | COVERED-43 | 43 | |
| Posting → Payroll posting definitions (earnings/benefits/taxes to accounts), Payroll posting rules by position/department | | COVERED-43 | 43 | |
| Payment → Payment methods (check, direct deposit), Pay statement layouts, Positive pay | | COVERED-43 | 43 | |
| Vendors for payroll liabilities (tax authorities, benefit providers) | | COVERED-43 | 43 | |
| Pay statement → Pay statement check layout / Direct deposit advice | | COVERED-46 | 46 | |
| Year-end → W-2 box mapping, Electronic filing setup | | COVERED-43 | 43 | |
| Country packs → US tables (federal/state) / EU reference country (DE or UK) | | COVERED-43 | 43 | Open question 3 in PRD |
| Third-party payroll provider adapter configuration | | COVERED-43 | 43 | |
| Workflow → Payroll workflows | | COVERED-4 | 4 | |
| Number sequences (payroll) | | COVERED-5 | 5 | |
| Payroll → Leave of absence / FMLA tracking (US) | | PARTIAL-42 (FMLA tracking not named) | 48 | Gap register |

## 12.14 Inventory management

AX 2012 R3 Inventory management (minus Quality, treated in §12.25, and minus WMS I/II which Passage supersedes with wave 31) maps to wave 16 (ledger, journals, counting, reservations, blocking, quarantine), wave 20 (costing, close), wave 28 (picking/shipping of orders) and wave 8 (item master). Transfer orders are only partially covered and are listed in the gap register.

### 12.14.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Released products (list page, link) | EcoResProductDetailsExtended (InventTable) | Item master per legal entity | COVERED-8 | 8 | See §12.15 |
| On-hand inventory (inquiry entry) | InventOnhandItem / InventDimOnHand | On-hand by dimension with physical/financial split | COVERED-16 | 16 | |
| Inventory transactions | InventTrans | All transactions with statuses | COVERED-16 | 16 | |
| Transfer orders (list page) | InventTransferOrderListPage | Ship/receive between warehouses with in-transit | PARTIAL-16 (transfer *journal* is named; transfer *orders* with shipment, in-transit warehouse, receive, underdelivery and transfer-order picking are not named in 16, 22 or 28) | 16 | Gap register |
| Transfer order details | InventTransferTable / InventTransferLine | Lines, shipment/receipt, dates, charges | PARTIAL-16 (transfer orders) | 16 | |
| Journals → Movement journal | InventJournalTable (Movement) | Qty in/out with offset account | COVERED-16 | 16 | |
| Journals → Inventory adjustment journal (Profit/loss) | InventJournalTable (ProfitLoss) | Adjustments to item posting accounts | COVERED-16 | 16 | |
| Journals → Transfer journal | InventJournalTable (Transfer) | Dimension-to-dimension transfer | COVERED-16 | 16 | |
| Journals → Bills of materials journal | InventJournalTable (BOM) | Report BOM as finished without production | COVERED-16 | 16 | |
| Journals → Counting journal | InventJournalTable (Count) | Count sheets, variances, approval | COVERED-16 | 16 | |
| Journals → Tag counting journal | InventJournalTable (TagCount) | Tag-based count | PARTIAL-16 (tag counting (numbered tags, two-step) not named; cycle counting is) | 16 | Gap register |
| Journals → Item arrival journal / Production input journal | WMSJournalTable (Arrival / ProdInput) | Register arrivals; production input | COVERED-18 | 18 | Arrival; production input in 21 |
| Journals → Inventory ownership change journal (D365 consignment) | InventJournalTable (OwnershipChange) | | GAP | — | Propose wave 16: consignment |
| Quarantine orders | InventQuarantineOrder | Quarantine warehouse, start/end/report as finished | COVERED-16 | 16 | Quarantine |
| Inventory blocking | InventBlocking | Block qty (manual/quality) | COVERED-16 | 16 | |
| Quality orders / Nonconformances (links) | InventQualityOrderTable | | COVERED-33 | 33 | §12.25 |
| Batches (Inventory batches) | InventBatch | Batch attributes, expiry, shelf-life dates, disposition, vendor batch, country of origin | COVERED-24 | 24 | Batch core in 16; attributes/shelf life in 24 |
| Serial numbers | InventSerial | Serial records | COVERED-16 | 16 | |
| Batch disposition codes (status on batch) | PdsDispositionCode | Available/unavailable with blocking | COVERED-24 | 24 | |
| Batch merge (PI) | PdsBatchMerge | Merge batches with attribute recalculation | PARTIAL-24 (batch merge/split with attribute recalculation not named) | 24 | Gap register |
| Pallets (WMS II) | WMSPallet | Pallet master | SUPERSEDED (license plates wave 31) | 31 | |
| Locations (WMS II) / Store areas / Store zones | WMSLocation, WMSStoreArea, WMSStoreZone | Legacy location model | SUPERSEDED (location model wave 31) | 31 | |
| Pallet transports / Output orders / Shipments (WMS I) | WMSTransport, WMSOrder, WMSShipment | Legacy warehouse execution | SUPERSEDED (work/waves/loads wave 31; fulfillment wave 28) | 31 | |
| Picking routes / Picking list registration (sales/inventory) | WMSPickingRoute, WMSOrderTrans | Legacy picking | SUPERSEDED (pick confirmation wave 28; work wave 31) | 28 | |
| Arrival overview | WMSArrivalOverview | Expected receipts planning | COVERED-18 | 18 | |
| Item arrival registration / Registration form | InventTransRegister | Register by dimension/serial/batch | COVERED-18 | 18 | |
| Picking → Pick (Picking form on orders) / Unpick | InventTransPick | Pick/unpick quantities | COVERED-28 | 28 | |
| Reservation form (manual reservation on order lines) | InventTransReserve | Reserve by dimension/batch | COVERED-16 | 16 | |
| Inventory → Bar codes (item bar codes) | InventItemBarcode | | COVERED-8 | 8 | |
| Inventory → Dimensions display / Dimension defaults | | | COVERED-16 | 16 | |
| Inventory → Counting → Cycle counting work (WHS) | WHSWorkTable (count) | | COVERED-31 | 31 | |
| Inventory → ABC classification (on item) | InventTable (ABC) | ABC codes by revenue/value/margin/carrying cost | PARTIAL-16 (ABC classification fields/calculation not named) | 16 | Gap register |
| Inventory → Forecast (Inventory forecast: sales/purchase forecasts, link) | ForecastSales / ForecastPurch | | COVERED-22 | 22 | |
| Inventory → Item coverage (link) | ReqItemTable | | COVERED-22 | 22 | |
| Inventory → Inventory statuses (WHS) | WHSInventoryStatus | Available/blocked statuses | COVERED-16 | 16 | Inventory status |
| Inventory → Catch weight quantities on transactions | PdsCatchWeight | Dual UoM | COVERED-24 | 24 | |
| Inventory → Commodity codes (Intrastat on item) | | | PARTIAL-13 (Intrastat setup) | 13 | |
| Inventory → Inventory visibility add-in (D365) | | Real-time cross-system on-hand | COVERED-16 | 16 | On-hand projection; API wave 35 |

### 12.14.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| On-hand inquiry (by dimension, with cost) | | COVERED-16 | 16 | |
| On-hand → Physical inventory by dimension / Available physical | | COVERED-16 | 16 | |
| Transactions inquiry (Inventory transactions by item/dimension/reference) | | COVERED-16 | 16 | |
| Transfer order inquiries (Transfer order lines, shipped/received) | | PARTIAL-16 (transfer orders) | 16 | |
| Journal inquiries (posted journals by type) | | COVERED-16 | 16 | |
| Batch inquiries (Batch attributes, Batch disposition, Expired batches, Shelf life advice) | | COVERED-24 | 24 | |
| Serial number inquiry / Serial trace (Item tracing R3) | Trace batch/serial forward/backward | COVERED-24 | 24 | Genealogy; also wave 33 quality |
| Quarantine order inquiry | | COVERED-16 | 16 | |
| Reservations inquiry (Reserved by/for) | | COVERED-16 | 16 | |
| Inventory blocking inquiry | | COVERED-16 | 16 | |
| Settlements / Cost explorer (link) | | COVERED-20 | 20 | |
| Inventory statistics (Item statistics, Inventory turnover) | | COVERED-46 | 46 | Inventory turns metric |
| Item arrival inquiry (Arrival overview) | | COVERED-18 | 18 | |
| Picking list inquiry (Picking list registration journal) | | COVERED-28 | 28 | |
| Shipments / Output orders inquiries (WMS I) | | SUPERSEDED (wave 28/31) | 31 | |
| Pallet / Location inquiries (WMS II) | | SUPERSEDED (wave 31) | 31 | |
| Counting history / Count variance inquiry | | COVERED-16 | 16 | |
| Inventory dimension inquiry (dimension combinations) | | COVERED-16 | 16 | |
| Inventory → Intercompany on-hand (R3) | On-hand across legal entities | PARTIAL-40 (intercompany on-hand visibility not named) | 40 | Gap register |
| SSRS: Physical inventory by inventory dimension / On-hand | | COVERED-16 | 16 | |
| SSRS: Inventory value (link) | | COVERED-20 | 20 | |
| SSRS: Inventory transactions report | | COVERED-16 | 16 | |
| SSRS: Journal reports (Movement, Counting list, Counting journal, Transfer, BOM, Tag counting list) | | COVERED-16 | 16 | Tag counting PARTIAL |
| SSRS: Transfer order (shipping document, receiving document, Transfer order overview) | | PARTIAL-16 (transfer orders) | 16 | |
| SSRS: Inventory aging (link) | | PARTIAL-20 (inventory aging) | 20 | |
| SSRS: ABC classification report | | PARTIAL-16 (ABC classification) | 16 | |
| SSRS: Inventory turnover / Slow-moving items / Items without transactions | | COVERED-46 | 46 | |
| SSRS: Item base data / Item list / Item price list | | COVERED-8 | 8 | |
| SSRS: Batch / Serial number listings (Batch attributes, Expired batches) | | COVERED-24 | 24 | |
| SSRS: Quarantine orders report | | COVERED-16 | 16 | |
| SSRS: Bar code label (item) | | COVERED-32 | 32 | ZPL labels |
| SSRS: Item arrival / Receipts list | | COVERED-18 | 18 | |
| SSRS: Picking list / Picking list registration | | COVERED-28 | 28 | |
| SSRS: Packing slip (inventory transfer) | | PARTIAL-16 (transfer orders) | 16 | |
| SSRS: Inventory reconciliation (Inventory/Ledger) | | COVERED-20 | 20 | |
| SSRS: Item model group / Dimension group listings | | COVERED-8 | 8 | |
| SSRS: Consignment inventory reports (D365) | | GAP | — | Propose wave 16: consignment |
| SSRS: Catch weight on-hand / transactions | | COVERED-24 | 24 | |
| SSRS: Country of origin / Shelf life reports (PI) | | COVERED-24 | 24 | |

### 12.14.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Closing and adjustment (link) | | COVERED-20 | 20 | |
| Counting → Create counting journal (by item/warehouse/location, counting groups, periodic) | | COVERED-16 | 16 | Cycle counting plans |
| Counting → Post counting / Approve variances | | COVERED-16 | 16 | |
| Tag counting → Create tag counting / Transfer to counting journal | | PARTIAL-16 (tag counting) | 16 | |
| Transfer orders → Ship / Receive (batch) / Transfer order picking release | | PARTIAL-16 (transfer orders) | 16 | |
| Transfer orders → Delete transfer orders | | PARTIAL-16 (transfer orders) | 16 | |
| Quarantine orders → Start / End / Report as finished (batch) | | COVERED-16 | 16 | |
| Reservation → Automatic reservation (batch) / Cancel reservations | | COVERED-16 | 16 | |
| Batches → Expire batches / Update shelf life dates / Batch disposition update | | COVERED-24 | 24 | |
| Batches → Shelf life advice (batch rotation) | | COVERED-24 | 24 | |
| ABC classification (periodic calculation) | | PARTIAL-16 (ABC classification) | 16 | |
| Forecast → Transfer forecast (link) | | COVERED-22 | 22 | |
| Bar codes → Generate bar codes (batch) | | COVERED-8 | 8 | |
| Inventory → Recalculate on-hand (InventSum rebuild) / Check inventory consistency | | COVERED-16 | 16 | Projection rebuild |
| Inventory → Clean up (Delete inventory journals, Delete InventSum zero records, Delete warehouse journals) | | COVERED-16 | 16 | Housekeeping job |
| Inventory → Inventory close (link) / Transfer inventory to ledger | | COVERED-20 | 20 | |
| Inventory → Warehouse management: Pallet transports / Shipments / Output orders processing (WMS I/II) | | SUPERSEDED (wave 31) | 31 | |
| Inventory → Update unit conversions (mass) | | COVERED-6 | 6 | |
| Inventory → Release products to legal entities (mass) | | COVERED-8 | 8 | |
| Inventory → Consignment: ownership change batch | | GAP | — | Propose wave 16: consignment |
| Inventory → Intercompany: on-hand sync | | PARTIAL-40 (intercompany on-hand) | 40 | |
| Inventory → Item tracing (run trace) | | COVERED-24 | 24 | |
| Inventory → Catch weight: update nominal weights | | COVERED-24 | 24 | |

### 12.14.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Inventory and warehouse management parameters — General | Default dimensions, pallet/location usage (WMS II), date-controlled reservations, allow negative physical on quarantine, bill of materials: explode, counting reason codes, reservation by batch/date | COVERED-16 | 16 | WMS II flags superseded |
| Inventory and warehouse management parameters — Journals | Journal names defaults, delete lines after posting, journal size | COVERED-16 | 16 | |
| Inventory and warehouse management parameters — Inventory dimensions / Inventory accounting (Costing) | | COVERED-20 | 20 | |
| Inventory and warehouse management parameters — Quality management | Hours per day, nonconformance defaults | COVERED-33 | 33 | |
| Inventory and warehouse management parameters — Transport (WMS I) / Locations (WMS II) / Bar codes / Pallets | | SUPERSEDED (wave 31) | 31 | |
| Inventory and warehouse management parameters — Transfer orders | Default in-transit warehouse, underdelivery, picking | PARTIAL-16 (transfer orders) | 16 | |
| Inventory and warehouse management parameters — Number sequences | | COVERED-5 | 5 | |
| Inventory and warehouse management parameters — Quarantine / Blocking | | COVERED-16 | 16 | |
| Inventory → Item model groups | Costing method, include physical value, post physical/financial inventory, negative physical/financial, quarantine management, registration/picking requirements, receiving/issue requirements, stocked product, FIFO date-controlled, backward from ship date | COVERED-8 | 8 | Costing behavior wave 20 |
| Inventory → Item groups | Posting defaults per group | COVERED-8 | 8 | |
| Inventory → Dimension groups: Product dimension groups, Storage dimension groups, Tracking dimension groups (active, primary stocking, physical/financial inventory, for purchase prices/sales prices, blank receipt/issue allowed, coverage plan by dimension) | | COVERED-8 | 8 | |
| Inventory → Inventory dimension display settings (Dimensions display) | | COVERED-5 | 5 | Saved views |
| Inventory → Reservation hierarchies (WHS) | | COVERED-31 | 31 | |
| Inventory → Unit sequence groups (WHS) | | COVERED-31 | 31 | |
| Inventory → Inventory statuses / Inventory status transitions (D365) | | COVERED-16 | 16 | |
| Inventory → Posting → Inventory posting profiles (Purchase order, Sales order, Inventory, Production, Standard cost variance tabs; by item code/account code) | Full inventory posting matrix | COVERED-16 | 16 | Standard cost variance accounts in 39 |
| Inventory → Posting → Item posting groups (link) | | COVERED-16 | 16 | |
| Inventory → Journals → Journal names, inventory (type, voucher, posting restrictions, reserve on post) | | COVERED-16 | 16 | |
| Inventory → Journals → Warehouse journal names (arrival, production input) | | COVERED-18 | 18 | |
| Inventory → Counting → Counting groups (periodic/zero/positive/negative/manual counting codes, interval) | | COVERED-16 | 16 | Cycle counting plans |
| Inventory → Counting → Counting reason code policies | | PARTIAL-11 (reason codes) | 11 | |
| Inventory → Batch → Batch attributes, Batch attribute groups, Product-specific batch attributes, Customer-specific batch attributes | | COVERED-24 | 24 | |
| Inventory → Batch → Batch disposition master | | COVERED-24 | 24 | |
| Inventory → Batch → Shelf life setup on item (shelf advice period, best before, expiry), Batch number groups (manual/auto, by date) | | COVERED-24 | 24 | |
| Inventory → Serial → Serial number groups (auto numbering rules) | | COVERED-16 | 16 | |
| Inventory → Warehouses (Warehouse details: type, site, quarantine warehouse, transit warehouse, WHS flags, default locations, location format, reservation) | | COVERED-6 | 6 | WHS flags wave 31 |
| Inventory → Sites | | COVERED-6 | 6 | |
| Inventory → Locations (WMS II) / Location names / Location types / Store areas / Zones / Aisles | Legacy | SUPERSEDED (location model wave 31) | 31 | |
| Inventory → Pallets → Pallet types / Pallet IDs / Pallet transports setup (WMS II) | | SUPERSEDED (license plates wave 31) | 31 | |
| Inventory → Bar codes → Bar code setup (types: EAN13, Code 39, Code 128, Interleaved 2 of 5, UPC-A…), Bar code mask characters, Bar code masks | | COVERED-8 | 8 | GS1 identifiers |
| Inventory → Quarantine → Quarantine zones (link to Quality) | | COVERED-33 | 33 | |
| Inventory → Inventory blocking reasons | | COVERED-16 | 16 | |
| Inventory → Distribution → Item allocation keys (link to planning) | | COVERED-22 | 22 | |
| Inventory → Forecast models / Forecast plans (link) | | COVERED-22 | 22 | |
| Inventory → ABC classification setup (ABC calculation parameters) | | PARTIAL-16 (ABC classification) | 16 | |
| Inventory → Catch weight → Catch weight item setup, CW units, nominal/min/max weights | | COVERED-24 | 24 | |
| Inventory → Transfer orders → Transfer order parameters, In-transit warehouses | | PARTIAL-16 (transfer orders) | 16 | |
| Inventory → Consignment → Consignment parameters (D365) | | GAP | — | Propose wave 16: consignment |
| Inventory → Country of origin / Product origin setup (PI) | | COVERED-24 | 24 | |
| Inventory → Dimension-based cost price (Cost by site/warehouse) | | COVERED-20 | 20 | |
| Inventory → Unit of measure (link) / Unit conversions (standard, inter-class, intra-class, product-specific) | | COVERED-6 | 6 | |
| Inventory → Charges on inventory (Misc. charges on journals) | | COVERED-16 | 16 | |
| Inventory → Inventory visibility add-in setup (D365) | | SUPERSEDED (native on-hand projection + public API wave 35) | 35 | |
| Workflow → Inventory workflows (inventory journal approval) | | COVERED-16 | 16 | |
| Number sequences (inventory) | | COVERED-5 | 5 | |
| Shipping carriers (Shipping carrier interface, AX 2012 carrier integration: UPS/FedEx via shipping software) | | COVERED-28 | 28 | Carrier integration abstraction |
| Delivery → Modes of delivery / Terms of delivery / Transport (link) | | COVERED-28 | 28 | |
| Inventory → Picking → Picking routes setup / Picking workbench (R3 sales picking workbench) | Batch create picking lists by filters | PARTIAL-28 (picking workbench (grouped picking list generation across sales orders, non-WHS) is not named; wave/batch picking is) | 28 | Gap register |

## 12.15 Product information management

Products, product masters, variants, released products, dimension groups, attributes, categories, BOMs/formulas (also §12.22/§12.23), configuration — wave 8 and wave 9 — with product compliance (PI) a gap.

### 12.15.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Products (all products and product masters, shared) | EcoResProductListPage / EcoResProductDetails | Tenant-level product definitions | COVERED-8 | 8 | |
| Product masters | EcoResProductMasterListPage | Masters with dimension groups and configuration technology (predefined variant / dimension-based / constraint-based) | COVERED-8 | 8 | |
| Product variants | EcoResProductVariantListPage (EcoResDistinctProductVariant) | Variants per master | COVERED-8 | 8 | 1,000 variants in < 5 s |
| Released products (list page) | EcoResProductDetailsExtended (InventTable) | Per legal entity: general, purchase, sell, manage inventory, engineer, plan, manage projects, manage costs, financial dimensions, retail, warehouse, transportation, product variants | COVERED-8 | 8 | Site/warehouse defaults |
| Released product variants | InventDimCombination | Variant per entity with price/cost | COVERED-8 | 8 | |
| Released product → Default order settings / Site specific order settings | InventItemSetup (InventItemPurchSetup/SalesSetup/InventSetup) | Default site/warehouse, order qty (min/max/multiple/standard), lead time, stopped | COVERED-8 | 8 | |
| Released product → Item coverage (link) | ReqItemTable | | COVERED-22 | 22 | |
| Released product → BOM versions / Formula versions / Route versions | BOMVersion, RouteVersion | Approved/active versions per item | COVERED-9 | 9 | |
| Released product → External item descriptions (customer/vendor item numbers) | CustVendExternalItem | | COVERED-8 | 8 | |
| Released product → Bar codes / GTIN | InventItemBarcode / InventItemGTIN | | COVERED-8 | 8 | |
| Released product → Unit conversions (product-specific) | UnitOfMeasureConversion | | COVERED-6 | 6 | |
| Released product → Translations (product name/description per language) | EcoResProductTranslation | | COVERED-8 | 8 | Translations via metadata |
| Released product → Product attributes / Attribute values | EcoResProductAttributeValue | Attribute values by category | COVERED-8 | 8 | |
| Released product → Product categories assignment | EcoResProductCategory | | COVERED-8 | 8 | |
| Released product → Related products | EcoResProductRelation | Up-sell/cross-sell/accessory | PARTIAL-8 (related products (cross/up-sell relations) not named) | 8 | Gap register |
| Released product → Product images / documents | DocuRef | | COVERED-8 | 8 | |
| Released product → Potency / Active ingredient (PI) | PdsIngredient | Base attribute, potency item | COVERED-24 | 24 | |
| Released product → Catch weight (PI) | PdsCatchWeightItem | | COVERED-24 | 24 | |
| Released product → Planning items / Co-products / By-products (PI) | PdsCoProduct | | COVERED-24 | 24 | |
| Released product → Commodity pricing (PI: Pricing calculation, Price margins, Commodity codes) | PdsCommodity* | Cost basis types, price calculation, margins | PARTIAL-26 (commodity price index-based pricing for process industries (PI commodity pricing) not named) | 26 | Gap register |
| Released product → Product compliance (PI: Regulated products, Restricted lists, Reporting details, Safety data sheets (MSDS/SDS) with validity and send on sales) | PdsProductComplianceTable, PdsMSDS | Hazardous material compliance | GAP | — | Propose wave 33: product compliance (regulated/restricted lists by country/state, SDS versions with shipment delivery rules, reporting thresholds) |
| Released product → Sequencing (PI sequence groups) | PdsSequence | Sequence attributes for scheduling | COVERED-23 | 23 | Setup optimization |
| Released product → Pricing (Item price, Price simulation link) | InventItemPrice | | COVERED-26 | 26 | Cost in 39 |
| Released product → Charges (Item charges groups) | | | COVERED-17 | 17 | |
| Released product → Product lifecycle state (D365) | EcoResProductLifecycleState | Active/obsolete states controlling transactions | PARTIAL-8 (product lifecycle states with per-process blocking not named; stopped flags are) | 8 | Gap register |
| Product configuration → Product configuration models (R2+: constraint-based) | PCProductConfigurationModel | Components, attributes, constraints, calculations, BOM/route lines, UI | COVERED-8 | 8 | Configurator foundation (OR-Tools/MiniZinc) |
| Product configuration → Configure line (configure from order) | PCRuntimeConfigurator | Interactive configuration | COVERED-27 | 27 | Order with configured product |
| Product configuration → Product models (legacy Product builder, deprecated R2) | PBATable | | OUT-OF-SCOPE (deprecated in AX 2012 R2; constraint-based configurator replaces) | — | |
| Product configuration → Configuration variants / Dimension-based configuration (configurations) | ConfigTable | Config dimension values | COVERED-8 | 8 | |
| Categories → Category hierarchies (Procurement, Sales, Commodity code, Retail, Project) | EcoResCategoryHierarchy | Hierarchies with attributes | COVERED-8 | 8 | |
| Categories → Category hierarchy roles | EcoResCategoryHierarchyRole | | COVERED-8 | 8 | |
| Bills of materials (list page, BOM designer) | BOMTable, BOMDesigner | | COVERED-9 | 9 | |
| Formulas (PI) | BOMTable (formula) | | COVERED-9 | 9 | Formula BOMs |
| Routes (link) | RouteTable | | COVERED-9 | 9 | |
| Product change cases (case management for products) | CaseDetail (product change) | Link cases to product/BOM changes | COVERED-9 | 9 | ECR/ECO |
| Engineering change (D365 ECM link) | | | COVERED-9 | 9 | §12.34 |
| Product → Product readiness checks (D365) | EcoResProductReadiness | Pre-release checklists per legal entity | PARTIAL-9 (product readiness policies/checks not named) | 9 | Gap register |
| Item sales tax groups / Tax on product | | | COVERED-13 | 13 | |
| Product → Packaging (D365 packaging materials, PI container packaging) | | Packaging material for fees | PARTIAL-28 (packaging materials and packaging fees (EU packaging duty) not named) | 28 | Gap register |

### 12.15.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Released products inquiry / Products by category | | COVERED-8 | 8 | |
| Product variants inquiry | | COVERED-8 | 8 | |
| Where-used (BOM where-used) | | COVERED-9 | 9 | |
| BOM lines / BOM versions inquiry | | COVERED-9 | 9 | |
| Configuration inquiries (Configured products, Configuration history) | | COVERED-8 | 8 | |
| External item descriptions inquiry | | COVERED-8 | 8 | |
| Product attributes inquiry / Attribute search | | COVERED-8 | 8 | |
| Product compliance inquiries (Regulated items, SDS due) | | GAP | — | Propose wave 33: product compliance |
| SSRS: Item base data / Released product list | | COVERED-8 | 8 | |
| SSRS: BOM list / BOM lines / BOM where-used / BOM versions | | COVERED-9 | 9 | |
| SSRS: Formula report (PI) | | COVERED-9 | 9 | |
| SSRS: Item price list / Price list by customer | | COVERED-26 | 26 | |
| SSRS: Bar code labels | | COVERED-32 | 32 | |
| SSRS: Product configuration model / Configuration report | | COVERED-8 | 8 | |
| SSRS: Category hierarchy report | | COVERED-8 | 8 | |
| SSRS: Safety data sheet (SDS document, PI) | | GAP | — | Propose wave 33: product compliance |
| SSRS: Product images catalog | | COVERED-46 | 46 | |
| SSRS: Unit conversions list | | COVERED-6 | 6 | |

### 12.15.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Release products (to legal entities, mass release) | | COVERED-8 | 8 | |
| Product → Change product dimension groups / Change item model group (periodic conversions) | | PARTIAL-8 (changing dimension/model groups on items with transactions (conversion jobs) not named) | 8 | Gap register |
| Product → Generate variants (from dimension values) | | COVERED-8 | 8 | |
| Product → Update default order settings (mass) | | COVERED-8 | 8 | |
| BOM → Copy BOM / Change BOM item (mass replace component) | | COVERED-9 | 9 | |
| BOM → Approve/activate versions (batch) | | COVERED-9 | 9 | |
| BOM → Calculate (BOM calculation, link) | | COVERED-39 | 39 | |
| Product configuration → Model versioning / Reuse configurations / Clean up configurations | | COVERED-8 | 8 | |
| Product → Import products / variants / attributes (DIXF) | | COVERED-10 | 10 | |
| Product → Update product translations (batch) | | COVERED-8 | 8 | |
| Product compliance → Expire SDS / Reporting (periodic) | | GAP | — | Propose wave 33: product compliance |
| Product → Delete released products / Mark obsolete | | PARTIAL-8 (lifecycle states) | 8 | |
| Categories → Update category attributes / Reassign products | | COVERED-8 | 8 | |
| Product → Price update (Item price copy / Trade agreement bulk update) | | COVERED-26 | 26 | |

### 12.15.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Product information management parameters | Product dimension defaults, variant number nomenclature (D365), released product defaults, number sequences | COVERED-8 | 8 | |
| Product → Product dimension groups / Storage dimension groups / Tracking dimension groups | | COVERED-8 | 8 | |
| Product → Product dimensions: Sizes, Colors, Styles, Configurations (shared value lists) | | COVERED-8 | 8 | |
| Product → Product nomenclature / Variant nomenclature (D365) | Naming rules for variants | COVERED-8 | 8 | |
| Product → Item model groups (link) / Item groups (link) | | COVERED-8 | 8 | |
| Product → Product lifecycle states (D365) | | PARTIAL-8 (lifecycle states) | 8 | |
| Product → Product types / Product subtypes (item vs service; product vs master) | | COVERED-8 | 8 | |
| Product → Units (Units, Unit classes, Unit conversions) | | COVERED-6 | 6 | |
| Product → Attributes: Attribute types, Attributes, Attribute groups (D365 product attributes), Category attributes | | COVERED-8 | 8 | |
| Product → Category hierarchies / Category hierarchy roles | | COVERED-8 | 8 | |
| Product → Bar code setup / Bar code masks / GTIN setup | | COVERED-8 | 8 | |
| Product → Product configuration models setup (Attribute types, Components, Subcomponents, Tables constraints (system-defined/user-defined), Template constraints, BOM/route line templates, Calculation templates, Pricing rules, UI setup, Model versions, Product configuration parameters) | | COVERED-8 | 8 | |
| Product → Configuration groups (dimension-based config) / Configuration rules (BOM config rules, legacy) | | COVERED-8 | 8 | |
| Product → Default order settings templates | | COVERED-8 | 8 | |
| Product → External item description setup | | COVERED-8 | 8 | |
| Product → Item sales tax groups default / Charges groups default | | COVERED-13 | 13 | |
| Product → Packaging units / Packing material fees setup (EU) | | PARTIAL-28 (packaging materials) | 28 | |
| Product → Product compliance setup (PI: Regulated regions, Restricted lists, Reporting lists, SDS parameters, Product safety data sheet validity, Item-specific: MSDS validity days) | | GAP | — | Propose wave 33: product compliance |
| Product → Country of origin setup / Product origin rules (PI) | | COVERED-24 | 24 | |
| Product → Commodity pricing setup (Cost basis types, Pricing calculations, Price margin templates, Commodity codes) | | PARTIAL-26 (commodity pricing) | 26 | |
| Product → Potency / Base attribute setup / Batch attributes (link) | | COVERED-24 | 24 | |
| Product → Catch weight setup (link) | | COVERED-24 | 24 | |
| Product → Sequencing setup (Sequences, Sequence groups) | | COVERED-23 | 23 | |
| BOM → BOM versions parameters (approval required, activation rules), BOM calculation groups (link), BOM designer parameters | | COVERED-9 | 9 | |
| BOM → Scrap / Rounding-up (BOM line types: item, phantom, pegged supply, vendor) | | COVERED-9 | 9 | Phantom, subcontract (vendor line type) in 21 |
| Engineering → Engineering change management parameters (D365, link) | | COVERED-9 | 9 | |
| Product → Product readiness policies (D365) | | PARTIAL-9 (product readiness) | 9 | |
| Product → Product master → Product search / Product image settings | | COVERED-8 | 8 | |
| Product → Released product templates (record templates) | | COVERED-5 | 5 | Record templates in shell |
| Product → Workflow (product release/change approval) | | COVERED-4 | 4 | |
| Number sequences (products, variants, BOMs) | | COVERED-5 | 5 | |
| Product → Retail product attributes / Retail categories | | OUT-OF-SCOPE (retail non-goal) | — | |

## 12.16 Procurement and sourcing

Requisitions, RFQs, agreements, POs, purchasing policies, categories and vendor evaluation map to wave 17; receiving to 18; catalogs are partially covered; external catalogs (punchout), vendor rebates and broker contracts are gaps.

### 12.16.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Purchase requisitions (list page: all, prepared by me, assigned to me) | PurchReqTableListPage | Requisition lifecycle (draft, in review, approved, closed) | COVERED-17 | 17 | |
| Purchase requisition details | PurchReqTable / PurchReqLine | Lines from catalog/non-catalog, requester, accounting distribution, approval | COVERED-17 | 17 | |
| Purchase requisitions → Release approved purchase requisitions (workbench) | PurchReqReleaseApproved | Consolidate and create POs | COVERED-17 | 17 | Consolidation into POs |
| Purchase requisitions → Consolidation opportunities | PurchReqConsolidation | Group lines for better pricing | COVERED-17 | 17 | |
| Requests for quotations (list page) | PurchRFQCaseTableListPage | RFQ cases | COVERED-17 | 17 | |
| RFQ details / RFQ replies (Vendor replies) / Compare replies | PurchRFQCaseTable, PurchRFQReplyTable, PurchRFQCompare | Send, receive, compare, accept/reject | COVERED-17 | 17 | Supplier reply portal |
| RFQ → Bidding (Vendor portal RFQ reply, sealed bids, scoring criteria R3) | PurchRFQVendorPortal | Scoring, sealed bidding, amendments | PARTIAL-17 (sealed bids, scoring criteria and amendments/addenda not named) | 17 | Gap register |
| Purchase agreements (list page) | PurchAgreementListPage | Blanket with commitment types (product qty/value, category value, total value), release lines | COVERED-17 | 17 | |
| Purchase agreement classifications | AgreementClassification | | COVERED-17 | 17 | |
| Purchase orders (list page: all, received not invoiced, etc.) | PurchTableListPage | | COVERED-17 | 17 | |
| Purchase order details | PurchTable | Header/lines, delivery schedule, pricing, charges, dimensions, change management, intercompany | COVERED-17 | 17 | Intercompany 40 |
| Purchase orders → Purchase order change requests / Versions (change management) | PurchTableVersion | Versioned, re-approval | COVERED-17 | 17 | |
| Purchase orders → Purchase order confirmation (vendor confirmation via collaboration) | PurchConfirmation | | COVERED-20 | 20 | |
| Purchase orders → Delivery schedule on line | PurchLineDeliverySchedule | | COVERED-17 | 17 | |
| Purchase orders → Direct delivery (drop ship from sales) | PurchTable (direct delivery) | | COVERED-28 | 28 | Drop-ship |
| Purchase orders → Subcontracting (purchase line from production) | PurchLine (subcontract) | | COVERED-21 | 21 | |
| Vendors (list page, link) | VendTableListPage | | COVERED-7 | 7 | |
| Procurement catalogs | CatCatalog (ProcurementCatalog) | Internal catalog of products per category | PARTIAL-17 (requisitions and category policies are named; an internal procurement catalog with product images, category navigation, catalog publication by org unit is not) | 17 | Gap register |
| External catalogs (punchout cXML/OCI) | CatExternalCatalog | Vendor punchout | GAP | — | Propose wave 35: external catalog punchout (cXML/OCI) via integration hub, requisition lines returned to wave 17 |
| Vendor catalogs (vendor catalog import, CMR files) | CatVendorCatalog (CatCMR) | Vendor-maintained product catalog import | PARTIAL-20 (vendor catalog upload via supplier portal not named) | 20 | Gap register |
| Procurement categories (category hierarchy for procurement) | EcoResCategory (procurement role) | | COVERED-8 | 8 | |
| Vendor requests (link) | VendRequest | | PARTIAL-20 (vendor request workflow) | 20 | |
| Vendor evaluation (R3 vendor rating / criteria) | VendEvaluation | Rate vendors per criteria at receipt/invoice | COVERED-33 | 33 | Supplier scorecards |
| Vendor rebates (PI vendor rebate agreements, R3) | PdsRebateAgreement (vendor) | Rebate agreements with vendors, claims, accruals | GAP | — | Propose wave 26: vendor rebates (agreements, accrual on purchase, claim/credit note settlement via AP) mirroring customer rebates |
| Broker contracts (PI brokers and royalties, R3) | PdsBrokerContract | Broker commissions on sales, claim via AP | GAP | — | Propose wave 26: broker contracts (commission to broker vendor on sales invoices, claims via AP) |
| Royalty agreements (PI, R3) | PdsRoyaltyAgreement | Royalties payable on sales to licensors | GAP | — | Propose wave 26: royalty agreements (accrual on sales, claim to vendor) |
| Consignment (link) | | | GAP | — | Propose wave 16: consignment |
| Purchase orders → Product receipt / Receipts list (links) | | | COVERED-18 | 18 | |
| Purchase orders → Invoice (link) | | | COVERED-18 | 18 | |
| Purchase orders → Charges / Charges allocation | MarkupTrans | | COVERED-17 | 17 | |
| Purchase orders → Prepayment (link) | | | COVERED-19 | 19 | |
| Purchase inquiries (Purchase inquiry documents, R3 "purchase inquiry" journal) | PurchInquiry (VendPurchOrderJour inquiry type) | Price inquiry documents to vendors | COVERED-17 | 17 | RFQ covers |
| Procurement → Purchasing policies (link) | PurchPolicy | | COVERED-17 | 17 | |
| Procurement → Signing limits (link to Org admin) | SigningLimit | | PARTIAL-17 (signing limits (self-service limit requests per job/compensation) not named; amount-band policies are) | 17 | Gap register |
| Procurement → Workflow work items (link) | | | COVERED-4 | 4 | |
| Procurement → Vendor collaboration (D365 PO confirmation, invoices, consignment) | | | COVERED-20 | 20 | |
| Procurement → Vendor onboarding (D365 prospective vendor registration) | VendProspectiveVendorRegistration | External registration → request → vendor | PARTIAL-20 (vendor onboarding) | 20 | |
| Procurement → Vendor certifications / Vendor diversity (D365) | VendCertification | Certifications with expiry, diversity classifications | PARTIAL-7 (vendor certifications/diversity classifications not named) | 7 | Gap register |
| Procurement → Vendor contacts / Vendor users (portal) | | | COVERED-20 | 20 | |

### 12.16.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Purchase order inquiries (POs, lines, history, versions, backorders, open lines) | | COVERED-17 | 17 | |
| Purchase requisition inquiries (by requester, status, consolidation) | | COVERED-17 | 17 | |
| RFQ inquiries (RFQ status, replies, award history) | | COVERED-17 | 17 | |
| Purchase agreement inquiries (Fulfillment / released quantities) | | COVERED-17 | 17 | |
| Vendor evaluation inquiry / Vendor performance | | COVERED-33 | 33 | |
| Journals → Confirmations / Receipts lists / Product receipts / Invoices | | COVERED-18 | 18 | |
| Charges inquiry | | COVERED-17 | 17 | |
| Procurement category spend inquiry (Spend analysis cube) | | COVERED-46 | 46 | |
| Vendor rebate inquiries | | GAP | — | Propose wave 26: vendor rebates |
| Broker/royalty claim inquiries | | GAP | — | Propose wave 26: broker contracts |
| SSRS: Purchase order (document), Purchase order confirmation, PO status, PO lines | | COVERED-17 | 17 | |
| SSRS: Purchase requisition (document) / Requisition status | | COVERED-17 | 17 | |
| SSRS: Request for quotation (document) / RFQ comparison / RFQ reply | | COVERED-17 | 17 | |
| SSRS: Purchase agreement (document) / Agreement fulfillment | | COVERED-17 | 17 | |
| SSRS: Vendor evaluation report / Vendor performance (on-time, quality) | | COVERED-33 | 33 | |
| SSRS: Purchase analysis (Spend by vendor/category/period, Purchase statistics) | | COVERED-46 | 46 | |
| SSRS: Backorder lines / Overdue deliveries | | COVERED-17 | 17 | |
| SSRS: Procurement catalog report | | PARTIAL-17 (procurement catalog) | 17 | |
| SSRS: Vendor rebate reports | | GAP | — | Propose wave 26: vendor rebates |
| SSRS: Purchase price variance (link) | | COVERED-39 | 39 | |
| SSRS: Policy violations (purchasing policy) | | COVERED-17 | 17 | |

### 12.16.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Purchase requisitions → Release approved requisitions (batch auto-create POs) | | COVERED-17 | 17 | |
| Purchase requisitions → Expire / Clean up requisitions | | COVERED-17 | 17 | |
| Purchase orders → Confirm / Receipts list / Product receipt / Invoice (batch posting) | | COVERED-17 | 17 | |
| Purchase orders → Update delivery dates / Finalize / Cancel remaining | | COVERED-17 | 17 | |
| Purchase orders → Firm planned purchase orders (link) | | COVERED-22 | 22 | |
| Purchase agreements → Update agreement release status / Expire | | COVERED-17 | 17 | |
| RFQ → Send RFQ (batch), Close bidding, Purchase order from accepted reply | | COVERED-17 | 17 | |
| Catalog → Import vendor catalogs (CMR) / Publish procurement catalog | | PARTIAL-17 (procurement catalog) | 17 | |
| Vendor evaluation → Calculate vendor ratings | | COVERED-33 | 33 | |
| Vendor rebates → Cumulate / Approve / Process claims | | GAP | — | Propose wave 26: vendor rebates |
| Broker → Process broker claims / Royalty → Process royalty claims | | GAP | — | Propose wave 26: broker contracts |
| Procurement → Vendor collaboration: provision users, send PO (batch) | | COVERED-20 | 20 | |
| Procurement → Policy evaluation (re-evaluate policies on open requisitions) | | COVERED-17 | 17 | |
| Procurement → Delete purchase orders / Clean up update history | | COVERED-17 | 17 | |
| Procurement → EDI 850 outbound (batch) | | COVERED-35 | 35 | |

### 12.16.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Procurement and sourcing parameters — General | Default order type, create direct delivery, safety levels, intercompany PO defaults, delivery date control | COVERED-17 | 17 | |
| Procurement and sourcing parameters — Purchase requisitions / Purchase agreements / RFQ / Catalog / Vendor collaboration / Number sequences | | COVERED-17 | 17 | Catalog tab PARTIAL |
| Policies → Purchasing policies (policy rules: catalog policy, category access policy, category policy, purchase agreement policy, purchase order creation and demand consolidation, purchase requisition control, RFQ policy, re-approval rule for POs) | Policy framework with org hierarchy scope | COVERED-17 | 17 | Amount band/category policies |
| Policies → Purchase requisition approval amount thresholds / Signing limit policies | | PARTIAL-17 (signing limits) | 17 | |
| Policies → Purchasing policy organization hierarchy purpose | | COVERED-6 | 6 | Hierarchy purposes |
| Categories → Procurement categories, Category policy rules, Category managers, Category vendor assignment, Category questionnaires | | PARTIAL-8 (category-level vendor assignment and category questionnaires not named) | 17 | Gap register |
| Catalogs → Procurement catalogs, Catalog policies, External catalogs (cXML config), Vendor catalog import parameters, Catalog images | | PARTIAL-17 (procurement catalog) | 17 | External catalogs GAP (wave 35) |
| Vendors → Vendor evaluation criterion groups / criteria | | COVERED-33 | 33 | |
| Vendors → Vendor request types / Vendor onboarding questionnaires (D365) | | PARTIAL-20 (vendor request workflow) | 20 | |
| Vendors → Vendor certification types / Diversity classifications | | PARTIAL-7 (vendor certifications) | 7 | |
| RFQ → RFQ types, RFQ scoring criteria/groups, Reply templates, Solicitation types (D365), Bidding parameters | | PARTIAL-17 (sealed bids/scoring) | 17 | |
| Purchase agreements → Agreement classifications, Commitment types | | COVERED-17 | 17 | |
| Purchase orders → Order types, Purchase order change management parameters per vendor, PO approval workflow | | COVERED-17 | 17 | |
| Charges → Charges codes / Auto charges / Charges groups (link) | | COVERED-17 | 17 | |
| Prices and discounts → Trade agreement journal names, Price/discount groups (vendor), Activate price/discount, Purchase price tolerance, Vendor price search parameters (vendor > group > all) | | COVERED-26 | 26 | Wave 17 uses pricing engine for vendor prices |
| Vendor rebates → Rebate agreements, Rebate program types, Rebate groups (vendor), Rebate claim journal names | | GAP | — | Propose wave 26: vendor rebates |
| Brokers and royalties → Broker contract setup, Royalty agreement setup, Claim journal names | | GAP | — | Propose wave 26: broker contracts |
| Delivery → Modes of delivery, Terms of delivery, Delivery schedule parameters | | COVERED-28 | 28 | |
| Forms → Form setup (purchase documents) / Form notes / Print management (purchase) | | PARTIAL-46 (print management routing) | 46 | |
| Vendor collaboration → Vendor collaboration parameters, Consignment parameters | | COVERED-20 | 20 | Consignment GAP wave 16 |
| Workflow → Procurement workflows (requisition line/header, PO, agreement, RFQ) | | COVERED-4 | 4 | |
| Number sequences (procurement) | | COVERED-5 | 5 | |
| Intercompany → Intercompany purchase order setup (link) | | COVERED-40 | 40 | |
| Procurement → Unit of measure defaults per category | | COVERED-6 | 6 | |
| Procurement → Receiving: Registration/receipt requirements on item model group (link) | | COVERED-18 | 18 | |
| Procurement → EDI partner setup (link) | | COVERED-35 | 35 | |

## 12.17 Sales and marketing

Sales quotations, orders, agreements, delivery and returns map to waves 27–30 (orders/ATP/credit 27, fulfillment 28, invoicing 29, returns 30) with pricing in 26. The CRM half (leads, opportunities, campaigns, telemarketing, mailings, activities, responsibilities) is out of scope per the PRD non-goal; Passage integrates with Salesforce/HubSpot via wave 35 connectors.

### 12.17.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Sales orders (list page: all, open, backorders, delivered not invoiced, recent) | SalesTableListPage | | COVERED-27 | 27 | |
| Sales order details | SalesTable / SalesLine | Header/lines, delivery, price/discount, charges, dimensions, holds, intercompany, project | COVERED-27 | 27 | |
| Sales order → Order types (journal, subscription, sales order, returned order, item requirements) | SalesTable.SalesType | | COVERED-27 | 27 | |
| Sales order → Delivery date control (ATP, CTP, sales lead time, none) | SalesLine (dlv date control) | | COVERED-27 | 27 | |
| Sales order → Credit limit check / Order holds | SalesTableHold | | COVERED-27 | 27 | |
| Sales order → Order events / Order history | SalesOrderEvent | | COVERED-27 | 27 | Audit |
| Sales order → Configure line / Product configuration | PCRuntimeConfigurator | | COVERED-27 | 27 | |
| Sales order → Direct delivery / Drop ship to vendor | SalesLine (direct delivery) → PurchTable | | COVERED-28 | 28 | |
| Sales order → Alternative items (substitutes) | InventItemAlternative | Alternate when out of stock | COVERED-27 | 27 | |
| Sales order → Supplementary items (prompt) | PriceDiscSuppItem | | PARTIAL-26 (supplementary items) | 27 | |
| Sales order → Charges / Charges allocation | MarkupTrans | | COVERED-26 | 26 | |
| Sales order → Prepayment | | | COVERED-30 | 30 | |
| Sales order → Confirmation / Picking list / Packing slip / Invoice posting | SalesEditLines | | COVERED-28 | 28 | Invoice 29; confirmation 27 |
| Sales order → Reservation | InventTransReserve | | COVERED-16 | 16 | |
| Sales order → Delivery schedule (split line into schedule) | SalesLineDeliverySchedule | | COVERED-28 | 28 | |
| Sales order → Blanket / Sales agreements | SalesAgreementListPage / SalesAgreement | Commitment types, release orders | COVERED-27 | 27 | |
| Sales order → Release orders from agreement | SalesAgreementRelease | | COVERED-27 | 27 | |
| Sales order → Intercompany chain (direct delivery chains) | InterCompany* | | COVERED-40 | 40 | |
| Sales order → Backorder lines / Backorder overview | SalesBackorderLines | | COVERED-27 | 27 | |
| Sales order → Item requirements (project) | SalesTable (item req) | | COVERED-41 | 41 | |
| Sales order → Packing (packing form, HU) | | | COVERED-28 | 28 | |
| Sales order → Enter shipping details / Carrier | | | COVERED-28 | 28 | |
| Sales quotations (list page) | SalesQuotationListPage | Quotes with prospects/customers, win/lose, convert | COVERED-27 | 27 | |
| Sales quotation details | SalesQuotationTable | Lines, expiry, follow-up, templates, alternative quotes, price simulation | COVERED-27 | 27 | Price simulation in 26 |
| Sales quotation → Quotation templates / Quotation journals (confirm, send, lose, cancel) | SalesQuotationTemplate, SalesQuotationJour | | COVERED-27 | 27 | |
| Sales quotation → Mass create quotations (campaign) | SalesQuotationMassCreate | | OUT-OF-SCOPE (CRM non-goal: campaign-driven mass quotes) | — | |
| Return orders (list page) | ReturnTableListPage | RMA with reason, disposition, replacement | COVERED-30 | 30 | |
| Return order details | ReturnTable (SalesTable returned) | Return lines, find sales order, disposition code, inspection, replacement order, credit | COVERED-30 | 30 | |
| Return order → Arrival / Registration / Quarantine / Disposition | WMSArrivalOverview (return) | | COVERED-30 | 30 | |
| Customers (link) / Prospects | CustTableListPage / smmBusRelTable | Customers and prospects | COVERED-7 | 7 | Prospect role in party model |
| Contacts | smmContactPersonListPage | Contact persons | COVERED-7 | 7 | |
| Leads (R2+) | smmLeadTableListPage | Lead management | OUT-OF-SCOPE (CRM non-goal; integrate Salesforce/HubSpot wave 35) | — | |
| Opportunities (R2+) | smmOpportunityTableListPage | Opportunity pipeline | OUT-OF-SCOPE (CRM non-goal) | — | |
| Campaigns | smmCampaignTable | Campaign targets, responses | OUT-OF-SCOPE (CRM non-goal) | — | |
| Campaign targets / Mailings / Direct marketing | smmCampaignSelection, smmMailings | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Telemarketing / Call lists | smmTMCallListTable | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Activities (sales activities: tasks, appointments, actions, events) | smmActivities | | OUT-OF-SCOPE (CRM non-goal; project/service activities handled by case framework) | — | |
| Responsibilities (sales responsibilities on customers/opportunities) | smmResponsibilities | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Sales management (sales targets, sales units, sales management statistics) | smmSalesUnit, smmSalesTarget | | OUT-OF-SCOPE (CRM non-goal; targets via wave 46 analytics) | — | |
| Business relations (legacy prospects) | smmBusRelTable | | COVERED-7 | 7 | Prospect role |
| Competitors (on opportunities) | smmCompetitorTable | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Outlook synchronization (contacts/appointments) | smmOutlookSync | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Customer self-service (D365 customer portal link) | | | COVERED-30 | 30 | |
| Trade allowance (link) | | | COVERED-26 | 26 | §12.18 |
| Sales → Price simulation (quotation / order) | PriceSimulation | What-if price and margin | COVERED-26 | 26 | Price simulation and explain |
| Sales → Commissions (link) | | | PARTIAL-26 (commission posting) | 26 | |
| Sales → Sales order → Shipping carrier interface (R3) | ShipCarrier* | Rate/label via carrier software | COVERED-28 | 28 | |
| Sales → Credit card authorization on order | CreditCardAuthTrans | | COVERED-30 | 30 | |
| Sales → Order intake API / Multi-channel (EDI 850 inbound, e-commerce) | | | COVERED-27 | 27 | EDI in 35 |
| Sales → Call center (link) | | | OUT-OF-SCOPE (call center) | — | §12.19 |

### 12.17.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Sales order inquiries (orders, lines, history, open lines, backorders) | | COVERED-27 | 27 | |
| Sales quotation inquiries (quotation lines, follow-up, win/loss) | | COVERED-27 | 27 | |
| Sales agreement fulfillment inquiry | | COVERED-27 | 27 | |
| Return order inquiries (return status, disposition history) | | COVERED-30 | 30 | |
| Journals → Confirmations / Picking lists / Packing slips / Invoices | | COVERED-28 | 28 | |
| Delivery → Backorders / Delivery performance (OTIF) | | COVERED-46 | 46 | OTIF metric |
| ATP inquiry (Available-to-promise per item/date) | | COVERED-27 | 27 | |
| CRM inquiries (Leads/Opportunities pipeline, Campaign results, Activities) | | OUT-OF-SCOPE (CRM non-goal) | — | |
| SSRS: Sales order confirmation / Picking list / Packing slip / Invoice (documents) | | COVERED-28 | 28 | |
| SSRS: Sales quotation (document) / Quotation confirmation | | COVERED-27 | 27 | |
| SSRS: Sales agreement (document) | | COVERED-27 | 27 | |
| SSRS: Return order / Return acknowledgment (RMA document) | | COVERED-30 | 30 | |
| SSRS: Sales order status / Open sales orders / Backorder lines by item/customer | | COVERED-27 | 27 | |
| SSRS: Sales analysis (Sales by customer/item/sales group, Top items, Gross margin) | | COVERED-46 | 46 | |
| SSRS: Price list (customer) / Price list by item | | COVERED-26 | 26 | |
| SSRS: Delivery performance / On-time delivery | | COVERED-46 | 46 | |
| SSRS: Campaign / Lead / Opportunity / Activity / Telemarketing reports | | OUT-OF-SCOPE (CRM non-goal) | — | |
| SSRS: Mailings / Labels (customer address labels) | | OUT-OF-SCOPE (CRM non-goal) | — | |
| SSRS: Sales statistics / Customer group statistics | | COVERED-46 | 46 | |
| SSRS: Sales tax on sales (link) | | COVERED-13 | 13 | |
| SSRS: Certificate of analysis on shipment (link) | | COVERED-33 | 33 | |
| SSRS: Commission report (link) | | PARTIAL-26 (commission posting) | 26 | |
| SSRS: Shipping labels / Bill of lading | | COVERED-28 | 28 | BOL in 34 |

### 12.17.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Sales update → Confirmation / Picking list / Packing slip / Invoice (batch) | | COVERED-28 | 28 | |
| Sales update → Release sales order picking (Release to warehouse) | | COVERED-28 | 28 | |
| Sales update → Summary update | | COVERED-29 | 29 | |
| Sales update → Delivery date update / Backorder processing | | COVERED-27 | 27 | |
| Sales update → Delete orders / Clean up update history / Delete quotations | | COVERED-27 | 27 | |
| Sales quotations → Expire quotations / Mass update | | COVERED-27 | 27 | |
| Sales agreements → Update status / Expire | | COVERED-27 | 27 | |
| Return orders → Clean up expired RMA | | COVERED-30 | 30 | |
| Credit management → Update holds / Auto release (D365) | | COVERED-27 | 27 | |
| Campaigns → Campaign processing / Mailings | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Activities → Synchronize with Outlook | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Sales → EDI 855/856/810 outbound (batch) | | COVERED-35 | 35 | |
| Sales → Intercompany sales order creation | | COVERED-40 | 40 | |
| Sales → Order holds → Clear holds (batch) | | COVERED-27 | 27 | |
| Sales → Shipping carrier → Export shipments / Import tracking | | COVERED-28 | 28 | |
| Sales → Trade allowance processing (link) | | COVERED-26 | 26 | |

### 12.17.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Sales and marketing parameters — General | Default quotation settings, prospect conversion, sales unit | COVERED-27 | 27 | |
| Sales and marketing parameters — Quotations (expiry, follow-up, templates, probability) | | COVERED-27 | 27 | |
| Sales and marketing parameters — Leads / Opportunities / Campaigns / Activities / Telemarketing / Mailings / Outlook | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Sales and marketing parameters — Number sequences | | COVERED-5 | 5 | |
| Sales orders → Order types / Order hold codes / Order event setup / Sales order pools | | COVERED-27 | 27 | |
| Sales orders → Delivery date control (Sales lead time, Order deadlines, Transport days, Coverage calendars, Delivery date control defaults) | | COVERED-27 | 27 | |
| Sales orders → Order deadlines (cut-off times) | | COVERED-27 | 27 | |
| Sales orders → Transport days (shipping calendar between sites/warehouses/customers) | | COVERED-27 | 27 | |
| Sales orders → Customer item numbers / External item descriptions | | COVERED-8 | 8 | |
| Sales orders → Alternative items setup | | COVERED-27 | 27 | |
| Sales orders → Supplementary items | | PARTIAL-26 (supplementary items) | 27 | |
| Sales agreements → Agreement classifications | | COVERED-27 | 27 | |
| Quotations → Quotation templates / Quotation types / Reasons (win/loss/cancel) / Quotation journal names / Probability | | COVERED-27 | 27 | |
| Returns → Return reason codes / Reason code groups / Disposition codes / Disposition actions (credit only, replace and credit, replace and scrap, return to customer, scrap) | | COVERED-30 | 30 | |
| Returns → RMA parameters (expiry, inspection warehouse) | | COVERED-30 | 30 | |
| Prices and discounts → Trade agreement journal names, Price/discount groups (customer, line discount, multiline discount, total discount, price group), Activate price/discount, Smart rounding, Discount combination rules (price/discount parameters), Price search (customer > group > all) | | COVERED-26 | 26 | |
| Prices and discounts → Price adjustment / Price list creation (customer price lists) | | COVERED-26 | 26 | Mass price updates |
| Charges → Charges codes (sales), Auto charges, Charges groups | | COVERED-26 | 26 | |
| Commissions → Commission calculation, Sales groups (sales reps), Commission customer groups, Commission item groups, Commission posting | | PARTIAL-26 (commission posting) | 26 | |
| Delivery → Modes of delivery, Terms of delivery, Delivery charges groups, Shipping carriers, Carrier services (R3 shipping carrier interface: Carrier company, Carrier interface config) | | COVERED-28 | 28 | |
| Delivery → Destination codes (R3 Transportation link) | | COVERED-34 | 34 | |
| Customers → Customer groups / Posting profiles / Classification groups / Line of business / Segments / Subsegments / Sales districts (link) | | COVERED-7 | 7 | |
| Customers → Prospects setup (Business relation types, statuses, Conversion to customer templates) | | COVERED-7 | 7 | Prospect role |
| Contacts → Contact titles, Loyalty, Decision maker, Character, Contact responsibilities | | COVERED-7 | 7 | Contact role attributes |
| Leads → Lead types, Lead sources, Lead ratings, Lead statuses, Lead qualification stages | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Opportunities → Opportunity types, Sales phases, Probabilities, Reasons, Processes | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Campaigns → Campaign types, Campaign groups, Campaign media, Campaign processes, Response types, Mailing types, Target groups | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Activities → Activity types, Activity categories, Activity phases, Activity plans | | OUT-OF-SCOPE (CRM non-goal; service activities in 45) | — | |
| Telemarketing → Call list types, Call statuses, Scripts | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Responsibilities → Responsibility types / Sales management: Sales units, Sales targets, Sales management periods | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Management statistics (sales statistics setup, Sales/management statistics cubes) | | COVERED-46 | 46 | |
| Document management → Mail merge / Document templates (sales letters) | | OUT-OF-SCOPE (CRM non-goal; document templates for business documents handled in wave 46) | — | |
| Credit management (D365) setup (link) | | COVERED-27 | 27 | |
| ATP → ATP parameters (time fence, backward/forward consumption, include planned orders), CTP via master planning | | COVERED-27 | 27 | |
| Forms → Form setup (sales documents), Form notes, Print management (sales), Form sorting | | PARTIAL-46 (print management routing) | 46 | |
| Workflow → Sales workflows (sales order approval D365, sales agreement) | | COVERED-4 | 4 | |
| Number sequences (sales) | | COVERED-5 | 5 | |
| Intercompany → Intercompany sales setup (link) | | COVERED-40 | 40 | |
| Sales → Customer self-service parameters (D365 portal) | | COVERED-30 | 30 | |
| Sales → E-commerce connector (Shopify/Medusa) | | COVERED-35 | 35 | Non-goal says integrate, not build |

## 12.18 Trade allowance management

AX 2012 R3 TAM: trade allowance agreements (bill-back, lump sum, off-invoice), funds, customer categories, merchandising events, deductions workbench, claims via AR. Passage: promotions and rebate accruals in wave 26, settlement and deductions in wave 30. The deduction workbench and funds are only partially covered.

### 12.18.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Trade allowance agreements (list page) | TAMTradeAllowanceAgreementListPage | Agreements with merchandising events: bill-back, lump sum, off-invoice | COVERED-26 | 26 | Promotions + rebate agreements |
| Trade allowance agreement details | TAMTradeAllowanceAgreement | Customers, items, period, funds, approval, lines (bill-back rebate lines, lump sum, off-invoice discounts) | COVERED-26 | 26 | |
| Merchandising events (Promotion planning) | TAMMerchandisingEvent | Events with analysis (lift, baseline) | PARTIAL-26 (promotion planning with baseline/lift analysis not named; promotions are) | 26 | Gap register |
| Funds | TAMFund | Budget funds per customer/period with usage tracking | PARTIAL-26 (trade promotion funds with budget/usage tracking not named) | 26 | Gap register |
| Bill-back workbench (Rebate workbench) | TAMBillBackWorkbench (PdsRebate*) | Cumulate, approve, process, pay rebates | COVERED-30 | 30 | Rebate settlement |
| Deduction workbench | TAMDeductionWorkbench | Match short-pays (deductions) to claims, write-off, deny, split | PARTIAL-30 (deductions workbench — matching customer short-pays to promotions, denial/write-off/split — not named; disputes workflow is) | 30 | Gap register |
| Lump sum payments (via AP claim / credit note) | TAMLumpSum | | COVERED-30 | 30 | |
| Customer rebate transactions (PdsRebateTable) | PdsRebateTable | Accrued rebate lines | COVERED-26 | 26 | |
| Customer categories (Customer category hierarchy for TAM) | TAMCustomerCategory | Customer groupings for agreements | COVERED-7 | 7 | Customer groups/attributes |
| Trade allowance → Sales order promotion application (promotion on order lines) | SalesLine (TAM) | Off-invoice applied at order | COVERED-26 | 26 | |

### 12.18.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Trade allowance agreement inquiry / Agreement status | | COVERED-26 | 26 | |
| Fund usage inquiry | | PARTIAL-26 (funds) | 26 | |
| Rebate accrual inquiry / Bill-back transactions | | COVERED-26 | 26 | |
| Deduction inquiry (open deductions, matched) | | PARTIAL-30 (deductions workbench) | 30 | |
| Promotion effectiveness analysis (lift) | | PARTIAL-26 (promotion planning) | 26 | |
| SSRS: Trade allowance agreement (document) | | COVERED-26 | 26 | |
| SSRS: Fund statement / Fund usage | | PARTIAL-26 (funds) | 26 | |
| SSRS: Bill-back / Rebate report | | COVERED-30 | 30 | |
| SSRS: Deductions report | | PARTIAL-30 (deductions workbench) | 30 | |
| SSRS: Customer promotions summary | | COVERED-46 | 46 | |

### 12.18.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Cumulate rebates / Approve rebates / Process rebates (create credit notes or AP claims) | | COVERED-30 | 30 | |
| Deductions → Match deductions to claims / Write off small balances | | PARTIAL-30 (deductions workbench) | 30 | |
| Funds → Fund budget recalculation / Fund close | | PARTIAL-26 (funds) | 26 | |
| Agreements → Approve / Expire / Mass copy agreements | | COVERED-26 | 26 | |
| Lump sum → Generate lump sum claims | | COVERED-30 | 30 | |
| Promotion → Update promotion planning actuals | | PARTIAL-26 (promotion planning) | 26 | |

### 12.18.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Trade allowance management parameters | Default fund, rebate journal names, deduction journal, approval workflow, claim via AR/AP | COVERED-26 | 26 | |
| Trade allowance → Customer categories / Customer category hierarchy | | COVERED-7 | 7 | |
| Trade allowance → Funds setup (fund types, budget periods) | | PARTIAL-26 (funds) | 26 | |
| Trade allowance → Merchandising event categories / Event types | | PARTIAL-26 (promotion planning) | 26 | |
| Trade allowance → Deduction types / Deduction reason codes / Denial reasons | | PARTIAL-30 (deductions workbench) | 30 | |
| Trade allowance → Rebate program types / Rebate groups / Rebate journal names / Rebate posting accounts | | COVERED-26 | 26 | |
| Trade allowance → Lump sum setup | | COVERED-30 | 30 | |
| Trade allowance → Bill-back calculation basis (invoice/payment) | | COVERED-26 | 26 | |
| Trade allowance → Workflow (agreement approval) | | COVERED-4 | 4 | |
| Trade allowance → Number sequences | | COVERED-5 | 5 | |

## 12.19 Call center

AX 2012 R3 Call center (part of Retail) is a catalog-and-script driven order-taking channel. Per the PRD non-goal (retail POS/e-commerce), it is out of scope except for the pieces that are generic order-desk capabilities already built in wave 27 (order entry, holds, payments).

### 12.19.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Call center sales orders (order entry with scripts, prices from catalog, payment capture) | SalesTable (call center) | Operator order entry | OUT-OF-SCOPE (retail/call-center channel non-goal; generic order desk in wave 27) | — | Wave 27 order entry covers manufacturer order desk |
| Customer service form (CustomerService) | MCRCustomerService | 360° customer view for operators | SUPERSEDED (customer 360 workspace in wave 7/30 shell with FactBoxes) | 30 | |
| Catalogs (call center catalogs, catalog products, source codes) | MCRCatalogTable | Mail-order catalog with source codes | OUT-OF-SCOPE (retail non-goal) | — | |
| Scripts | MCRScript | Operator scripts | OUT-OF-SCOPE (retail non-goal) | — | |
| Continuity programs / Continuity orders | MCRContinuity* | Subscription shipments (book-of-the-month) | OUT-OF-SCOPE (retail non-goal; subscription billing wave 29 covers recurring invoicing) | — | |
| Installment plans / Payment schedules (call center) | MCRInstallment | | OUT-OF-SCOPE (retail non-goal; payment schedules wave 7) | — | |
| Fraud / Order holds (fraud rules) | MCRFraud* | Fraud hold rules | SUPERSEDED (order holds (credit/fraud/manual) wave 27) | 27 | |
| Returns (call center RMA with return reason, disposition) | | | COVERED-30 | 30 | |
| Direct mail / Direct marketing | MCRDirectMail | | OUT-OF-SCOPE (CRM/retail non-goal) | — | |
| Price match / Price override journals | MCRPriceMatch | | OUT-OF-SCOPE (retail non-goal) | — | |
| Coupons (call center coupons) | MCRCoupon | | OUT-OF-SCOPE (retail non-goal; promotions wave 26 for B2B) | — | |
| Credit card payments / Tender on order | | | COVERED-30 | 30 | |
| Customer → Call center customer details (RFM, source) | MCRCustTable | | OUT-OF-SCOPE (retail non-goal) | — | |

### 12.19.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Call center sales statistics (orders per operator, catalog performance) | | OUT-OF-SCOPE (retail non-goal) | — | |
| Continuity / Installment inquiries | | OUT-OF-SCOPE (retail non-goal) | — | |
| Fraud hold inquiry | | SUPERSEDED (order holds inquiry wave 27) | 27 | |
| SSRS: Catalog / Source code analysis | | OUT-OF-SCOPE (retail non-goal) | — | |
| SSRS: Call center order status | | COVERED-27 | 27 | |

### 12.19.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Continuity → Process continuity orders | | OUT-OF-SCOPE (retail non-goal) | — | |
| Installments → Generate installment invoices | | OUT-OF-SCOPE (retail non-goal) | — | |
| Fraud → Run fraud checks | | SUPERSEDED (order holds wave 27) | 27 | |
| Direct mail → Generate mailing | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Call center → Clean up expired catalogs / coupons | | OUT-OF-SCOPE (retail non-goal) | — | |

### 12.19.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Call center parameters | Channel defaults, scripts, fraud, order holds, catalogs, payment | OUT-OF-SCOPE (retail non-goal) | — | |
| Call centers (channel setup, operators, payment methods) | | OUT-OF-SCOPE (retail non-goal) | — | |
| Scripts / Script categories | | OUT-OF-SCOPE (retail non-goal) | — | |
| Fraud → Fraud rules, Fraud scoring, Fraud statuses | | SUPERSEDED (order hold codes/rules wave 27) | 27 | |
| Catalogs → Catalog source codes, Catalog categories | | OUT-OF-SCOPE (retail non-goal) | — | |
| Continuity → Continuity programs, Schedules | | OUT-OF-SCOPE (retail non-goal) | — | |
| Installment plans setup | | OUT-OF-SCOPE (retail non-goal) | — | |
| Coupons setup | | OUT-OF-SCOPE (retail non-goal) | — | |
| Price match setup / Reason codes | | OUT-OF-SCOPE (retail non-goal) | — | |
| Payment → Call center payment methods / Credit card types | | COVERED-30 | 30 | |
| Direct marketing setup | | OUT-OF-SCOPE (CRM non-goal) | — | |

## 12.20 Retail

AX 2012 R3 Retail (Retail headquarters, POS, Commerce Data Exchange, channels, assortments, loyalty, retail scheduler) is out of scope per the PRD non-goal; Passage integrates with Shopify/Medusa via wave 35. Rows are listed so reviewers can see the exclusion is deliberate.

### 12.20.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Retail stores / Retail channels (store, online store, call center) | RetailChannelTable | Channel master | OUT-OF-SCOPE (retail non-goal) | — | |
| Registers / Devices / POS terminals | RetailTerminalTable | | OUT-OF-SCOPE (retail non-goal) | — | |
| Retail product catalogs / Assortments | RetailCatalog, RetailAssortment | | OUT-OF-SCOPE (retail non-goal) | — | |
| Retail statements (POS transaction posting) | RetailStatementTable | Post POS sales to AR/inventory | OUT-OF-SCOPE (retail non-goal; e-commerce orders arrive as sales orders via wave 35 connector) | — | |
| Retail transactions (POS transactions) | RetailTransactionTable | | OUT-OF-SCOPE (retail non-goal) | — | |
| Loyalty (programs, cards, schemes, points) | RetailLoyalty* | | OUT-OF-SCOPE (retail non-goal) | — | |
| Retail discounts / Price groups / Promotions (Retail pricing) | RetailPeriodicDiscount | | OUT-OF-SCOPE (retail non-goal; B2B promotions wave 26) | — | |
| Gift cards | RetailGiftCard | | OUT-OF-SCOPE (retail non-goal) | — | |
| Replenishment (Retail replenishment: buyer's push, cross docking, replenishment rules) | RetailReplenishment* | Store replenishment | OUT-OF-SCOPE (retail non-goal; warehouse replenishment wave 31) | — | |
| Retail workers / Commissions | | | OUT-OF-SCOPE (retail non-goal) | — | |
| Online store / Sharepoint storefront | | | OUT-OF-SCOPE (e-commerce storefront non-goal) | — | |

### 12.20.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Retail sales reports (sales by store/register/hour, Top items) | | OUT-OF-SCOPE (retail non-goal) | — | |
| Statement inquiries / Transaction inquiries | | OUT-OF-SCOPE (retail non-goal) | — | |
| Loyalty inquiries | | OUT-OF-SCOPE (retail non-goal) | — | |
| Commerce Data Exchange status (sync inquiry) | | OUT-OF-SCOPE (retail non-goal) | — | |
| Retail cubes (Retail BI) | | OUT-OF-SCOPE (retail non-goal) | — | |

### 12.20.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Retail scheduler (A/N jobs: distribution schedule, upload/download sessions) | Commerce Data Exchange sync jobs | OUT-OF-SCOPE (retail non-goal) | — | |
| Calculate / Post statements | | OUT-OF-SCOPE (retail non-goal) | — | |
| Replenishment → Buyer's push / Cross docking | | OUT-OF-SCOPE (retail non-goal) | — | |
| Loyalty → Process loyalty points / Expire | | OUT-OF-SCOPE (retail non-goal) | — | |
| Retail → Publish catalog / Online store sync | | OUT-OF-SCOPE (retail non-goal; Shopify connector wave 35) | — | |

### 12.20.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Retail parameters / Retail shared parameters | | OUT-OF-SCOPE (retail non-goal) | — | |
| Retail scheduler parameters / Channel database / Commerce Data Exchange | | OUT-OF-SCOPE (retail non-goal) | — | |
| POS → POS profiles (functionality, receipt, hardware, visual, offline), Screen layouts, Operations, Permission groups | | OUT-OF-SCOPE (retail non-goal) | — | |
| Store → Store setup, Registers, Hardware stations, Payment methods per store, Tender types, Info codes, Reason codes (retail) | | OUT-OF-SCOPE (retail non-goal) | — | |
| Pricing → Retail price groups, Discount setup, Affiliations | | OUT-OF-SCOPE (retail non-goal) | — | |
| Loyalty setup | | OUT-OF-SCOPE (retail non-goal) | — | |
| Sales tax → Retail tax setup (destination-based, tax override) | | COVERED-13 | 13 | Tax engine handles destination-based |
| Retail hierarchy / Organization hierarchy for retail | | COVERED-6 | 6 | Hierarchy purposes |
| Online store → Storefront / Sharepoint site setup | | OUT-OF-SCOPE (e-commerce storefront non-goal) | — | |
## 12.21 Master planning

Static/dynamic plans, coverage, planned orders, action/futures messages, forecasting, safety stock and intercompany planning map to wave 22 (MRP/MPS/forecasting) with capacity in 23 and intercompany planning in 40. D365 Planning Optimization and priority-based planning are superseded by the Passage planning engine.

### 12.21.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Planned orders (list page: all, planned purchase, planned production, planned transfer, planned kanban) | ReqPOListPage | Review/firm/change planned orders | COVERED-22 | 22 | |
| Planned order details | ReqPO | Qty, dates, pegging (requirement profile), action/futures messages, approve, firm, group | COVERED-22 | 22 | |
| Planned orders → Firm (to PO/production/transfer/kanban) | ReqTransFirm | Firming with grouping (by vendor, period, buyer group) | COVERED-22 | 22 | |
| Planned orders → Change planned orders (mass change) | ReqPOChange | Bulk date/qty | COVERED-22 | 22 | |
| Net requirements (per item) | ReqTransOverview | Requirements/supply with pegging per plan | COVERED-22 | 22 | Explain-this-plan pegging tree |
| Explosion (per order/line) | ReqTransExplosion | Multi-level explosion tree with pegging and futures | COVERED-22 | 22 | |
| Action messages (list) | ReqTransAction | Advance/postpone/decrease/increase/cancel/derived actions | COVERED-22 | 22 | |
| Futures messages / Delay messages (list) | ReqTransFutures | Delays and calculated dates | COVERED-22 | 22 | |
| Supply schedule (R3) | ReqSupplySchedule | Period-bucketed supply/demand view with planned order creation | PARTIAL-22 (period-bucketed supply schedule workbench not named; planning engine and planned orders are) | 22 | Gap register |
| Demand forecast (Forecast → Demand forecast / Sales forecast lines) | ForecastSales | Forecast lines by item/model/allocation key | COVERED-22 | 22 | |
| Supply forecast (Purchase forecast) | ForecastPurch | | COVERED-22 | 22 | |
| Inventory forecast | ForecastInvent? (InventForecast) | On-hand level forecast | PARTIAL-22 (inventory (stock level) forecast type not named; demand/supply forecasts are) | 22 | Gap register |
| Demand forecasting (R3: Generate statistical baseline forecast, Adjusted demand forecast workbook, Forecast accuracy) | ReqDemPlan* | Azure ML/SSAS baseline forecasts with Excel adjustment | COVERED-22 | 22 | ETS/Croston service with override and consensus |
| Master plans (list) | ReqPlanSched | Plans: static, dynamic, others | COVERED-22 | 22 | |
| Item coverage (per item/dimension) | ReqItemTable | Coverage group override, min/max, lead times, planned order type, vendor | COVERED-22 | 22 | |
| Safety stock journal | ReqSafetyStockJournal | Proposed min levels from history | COVERED-22 | 22 | Safety stock |
| Planned production orders → Scheduling (Gantt from planned orders) | | | COVERED-23 | 23 | |
| Intercompany planned orders (R3 intercompany master scheduling) | ReqPO (intercompany) | Planned orders across legal entities with intercompany planning groups | PARTIAL-40 (intercompany master planning across legal entities not named in 22 or 40) | 40 | Gap register |
| Planned orders → Sequencing (PI) | PdsSequence | Sequence planned batch orders | COVERED-23 | 23 | |
| Planned orders → Capacity reservations (link) | WrkCtrCapRes | | COVERED-23 | 23 | |
| Demand forecasting → Forecast models / Submodels | ForecastModel | | COVERED-22 | 22 | |
| Planning → Planning Optimization (D365 service), Priority-based planning (D365) | ReqPlanningOptimization | External planning service | SUPERSEDED (native Go planning engine wave 22 with priority as an attribute) | 22 | Priority-based planning (D365 2021+): PARTIAL — see gap register entry under setup |

### 12.21.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Net requirements inquiry / Requirement profile | | COVERED-22 | 22 | |
| Explosion inquiry / Trace (why was this planned) | | COVERED-22 | 22 | |
| Planned order inquiries (by item, vendor, period) | | COVERED-22 | 22 | |
| Action message inquiry / Futures inquiry | | COVERED-22 | 22 | |
| Capacity → Capacity load / Capacity reservations (link) | | COVERED-23 | 23 | |
| Master plan run history / Plan log (ReqLog) | Run statistics, errors | COVERED-22 | 22 | |
| Forecast inquiries (Forecast lines, Forecast consumption / reduction) | | COVERED-22 | 22 | |
| Demand forecast accuracy (MAPE) | | COVERED-22 | 22 | |
| Item coverage inquiry (Coverage settings by item) | | COVERED-22 | 22 | |
| Planned order pegging (bidirectional) | | COVERED-22 | 22 | |
| SSRS: Planned orders report / Planned purchase orders by vendor | | COVERED-22 | 22 | |
| SSRS: Net requirements report | | COVERED-22 | 22 | |
| SSRS: Action messages / Futures messages | | COVERED-22 | 22 | |
| SSRS: Capacity load (planned) | | COVERED-23 | 23 | |
| SSRS: Forecast (Sales forecast, Purchase forecast, Inventory forecast) | | COVERED-22 | 22 | Inventory forecast PARTIAL |
| SSRS: Coverage groups listing / Item coverage listing | | COVERED-22 | 22 | |
| SSRS: Safety stock journal report | | COVERED-22 | 22 | |
| SSRS: Master plan run log | | COVERED-22 | 22 | |
| SSRS: Supply schedule report | | PARTIAL-22 (supply schedule) | 22 | |

### 12.21.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Master scheduling (run plan: regeneration / net change / net change minimized) | | COVERED-22 | 22 | Incremental < 2 min target |
| Forecast scheduling (run forecast plan) | | COVERED-22 | 22 | |
| Planned order → Firm planned orders (batch by filter) | | COVERED-22 | 22 | |
| Planned order → Approve planned orders (batch) | | COVERED-22 | 22 | |
| Planned order → Delete planned orders | | COVERED-22 | 22 | |
| Planned order → Scheduling of planned orders (Gantt scheduling batch) | | COVERED-23 | 23 | |
| Plan → Copy plan / Delete plan | | COVERED-22 | 22 | |
| Safety stock → Calculate proposal (from history) / Update minimum keys | | COVERED-22 | 22 | |
| Forecast → Copy / Transfer forecast / Create forecast from history (Forecast allocation) | | COVERED-22 | 22 | |
| Forecast → Forecast reduction (consumption by actual orders) | Reduction keys and principles | COVERED-22 | 22 | Forecast with reduction |
| Demand forecasting → Generate statistical baseline forecast / Import adjusted forecast / Remove outliers | | COVERED-22 | 22 | |
| Intercompany master scheduling (run across entities) | | PARTIAL-40 (intercompany planning) | 40 | |
| Capacity → Delete capacity reservations (link) | | COVERED-23 | 23 | |
| Clean up → Delete futures/action messages / Clean plan history | | COVERED-22 | 22 | |
| Planning → Planning Optimization sync (D365) | | SUPERSEDED (native engine) | 22 | |
| Planning → Kanban quantity calculation (link to Lean) | | COVERED-24 | 24 | |
| Planning → Item allocation key refresh | | COVERED-22 | 22 | |
| Planning → Generate planned kanbans | | COVERED-24 | 24 | |
| Planning → MRP exception triage agent (Passage) | | COVERED-47 | 47 | |

### 12.21.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Master planning parameters — General | Current static/dynamic plan, automatic copy static→dynamic, use dynamic negative days, planned order defaults | COVERED-22 | 22 | |
| Master planning parameters — Planned orders | Group by vendor/buyer/period on firming, planned purchase order type, firm journal | COVERED-22 | 22 | |
| Master planning parameters — Standard update / Number sequences / Performance (threads, cache) | | COVERED-22 | 22 | |
| Master planning parameters — Planning Optimization (D365 connector, parameters) | | SUPERSEDED (native engine) | 22 | |
| Plans → Master plans (Plan: include on-hand/transactions/forecast/safety stock/inquiries/intercompany; time fences: coverage, freeze, firming, explosion, capacity, forecast plan, action, futures, BOM explosion; scheduling method: operations/job; finite capacity/material/property; action messages on/off; futures on/off) | Full plan parameters | COVERED-22 | 22 | Capacity flags 23 |
| Plans → Forecast plans (include forecast models, reduction) | | COVERED-22 | 22 | |
| Coverage → Coverage groups (requirement/period/min-max/manual, negative/positive days, coverage time fence, firming/freeze fences, action rules, futures, safety margins (receipt/issue/reorder), period lengths, action message tolerances, BOM/formula explosion fence, use dynamic negative days, coverage planned order type, planning time fence) | | COVERED-22 | 22 | |
| Coverage → Item coverage (link) / Minimum/maximum keys | Seasonal min/max profiles | COVERED-22 | 22 | |
| Coverage → Period templates (period coverage) | | COVERED-22 | 22 | |
| Coverage → Planned order defaults (Planned purchase/production/transfer defaults) | | COVERED-22 | 22 | |
| Forecast → Forecast models / Submodels | | COVERED-22 | 22 | |
| Forecast → Reduction keys / Reduction principles (none, percent-reduction key, transactions-dynamic period, transactions-reduction key) | | COVERED-22 | 22 | |
| Forecast → Item allocation keys / Period allocation keys (Period allocation categories) | Distribute forecast across items/periods | COVERED-22 | 22 | |
| Forecast → Customer/Vendor forecast groups (forecast at group level) | | COVERED-22 | 22 | |
| Demand forecasting → Demand forecasting parameters (algorithm, horizon, aggregation, history start, item allocation keys, Azure ML service) | | COVERED-22 | 22 | statsforecast service |
| Demand forecasting → Forecast algorithm parameters (seasonality, confidence, outlier removal) | | COVERED-22 | 22 | |
| Action messages → Action message settings (per coverage group) | | COVERED-22 | 22 | |
| Futures → Futures settings (per coverage group) | | COVERED-22 | 22 | |
| Safety stock → Safety stock journal names | | COVERED-22 | 22 | |
| Intercompany → Intercompany planning groups / Intercompany planning group members (sequence, plan) | | PARTIAL-40 (intercompany planning) | 40 | |
| Scheduling → Scheduling parameters (link to Production: operations/job scheduling defaults) | | COVERED-23 | 23 | |
| Sequencing → Sequencing setup (PI) | | COVERED-23 | 23 | |
| Planning → Planning item groups (process planning item, link) | | COVERED-24 | 24 | |
| Planning → Delivery date control / Transport days / Coverage calendars (shared with Sales) | | COVERED-27 | 27 | |
| Planning → Planned order grouping rules (vendor, buyer group, period) | | COVERED-22 | 22 | |
| Planning → Buyer groups | Grouping for planned purchase orders | COVERED-22 | 22 | |
| Planning → Planner / Planner groups (D365: planner on item) | | COVERED-22 | 22 | |
| Planning → Priority-based planning (D365: planning priority models, priority rules on coverage group) | Priority-driven reorder instead of dates (DDMRP-style) | PARTIAL-22 (priority-based / DDMRP-style buffer planning not named) | 22 | Gap register |
| Planning → Subcontracting planning (vendor line type / planned purchase for subcontract) | | COVERED-21 | 21 | |
| Planning → Capability-based scheduling (link) | | COVERED-23 | 23 | |
| Master planning → Number sequences | | COVERED-5 | 5 | |
| Master planning → Workflow (planned order approval D365) | | COVERED-4 | 4 | |

## 12.22 Production control

Production orders, journals, routes/operations, resources, scheduling, Gantt, manufacturing execution (job registration, time and attendance) and production posting map to waves 21 (orders/WIP/costing), 23 (capacity/scheduling), 25 (shop floor, T&A), 9 (routes/resources master data) and 39 (variances/costing sheets).

### 12.22.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Production orders (list page: all, created, estimated, scheduled, released, started, reported as finished, ended; "my" pools) | ProdTableListPage | Order lifecycle | COVERED-21 | 21 | |
| Production order details | ProdTable | Header: item, qty, dates, BOM/route, status, pool, group, reservation, site/warehouse, priority, properties, financial dimensions, costing | COVERED-21 | 21 | |
| Production order → BOM (production BOM lines) | ProdBOM | Order-specific BOM with line type (item/phantom/pegged supply/vendor), scrap, consumption, resource consumption | COVERED-21 | 21 | |
| Production order → Route (production route operations) | ProdRoute | Order-specific operations, times, resources, queue/setup/process/transport | COVERED-21 | 21 | |
| Production order → Jobs (route jobs) | ProdRouteJob | Job-level (setup, process, queue after/before, transport, overlap) | COVERED-23 | 23 | Job scheduling |
| Production order → Estimate / Schedule / Release / Start / Report as finished / End (update dialogs) | ProdUpdEstimation, ProdUpdScheduling, ProdUpdRelease, ProdUpdStartUp, ProdUpdReportFinished, ProdUpdCostCalc (End) | Status updates with options (auto-post journals, reference-level update, BOM/route consumption) | COVERED-21 | 21 | |
| Production order → Reset status | ProdUpdStatusReset | Back-step status | COVERED-21 | 21 | Lifecycle state machine |
| Production order → Split / Group (ProdSplit) | ProdSplit | Split order quantity | PARTIAL-21 (production order split/merge not named) | 21 | Gap register |
| Production order → Price calculation / Cost estimation view | ProdCalcTrans | Estimated cost by cost group | COVERED-21 | 21 | |
| Production order → Production transactions (view cost, consumption) | ProdTransView | | COVERED-21 | 21 | |
| Production order → Planned production orders (link) | ReqPOListPage | | COVERED-22 | 22 | |
| Production order → Production pools | ProdPool | Grouping/statuses for processing | COVERED-21 | 21 | |
| Production order → Reservation / Pick list release | | | COVERED-21 | 21 | |
| Production order → Subcontracting (vendor lines → PO) | ProdBOM (vendor) | | COVERED-21 | 21 | |
| Production order → Attachments / Instructions / Drawings | DocuRef | | COVERED-25 | 25 | Shop-floor instructions |
| Journals → Picking list | ProdJournalTable (Picking list) / ProdJournalBOM | Material consumption, backflush, negative consumption | COVERED-21 | 21 | |
| Journals → Route card | ProdJournalTable (Route card) / ProdJournalRoute | Hours/qty per operation (non-job level) | COVERED-21 | 21 | |
| Journals → Job card | ProdJournalTable (Job card) / ProdJournalRoute (job) | Per job registration | COVERED-21 | 21 | |
| Journals → Report as finished | ProdJournalTable (RAF) / ProdJournalProd | RAF qty, error qty, accept error, end job, by/co-products | COVERED-21 | 21 | |
| Journals → Production journals (all) / Posted journals | ProdJournalTable | | COVERED-21 | 21 | |
| Journals → Production input journal (WMS arrival for production) | WMSJournalTable (ProdInput) | | COVERED-21 | 21 | |
| Resources (list page) | WrkCtrResourceListPage (WrkCtrTable) | Resources: type (vendor/human/machine/tool/location), capacity, efficiency, calendar, capabilities, costs | COVERED-9 | 9 | |
| Resource groups | WrkCtrResourceGroup | Group with site, warehouse, input/output locations, calendar, ledger posting | COVERED-9 | 9 | |
| Resource capabilities (link) | WrkCtrCapability | | COVERED-9 | 9 | |
| Routes (list page, Route details, Route versions) | RouteTableListPage, RouteTable, RouteVersion | Routes with operations, relations | COVERED-9 | 9 | |
| Operations | RouteOprTable | Operation master with relations (per item/route/site) | COVERED-9 | 9 | |
| Operation relations (route operation details: setup/run time, resources requirements, cost categories, scrap) | RouteOpr | | COVERED-9 | 9 | |
| Gantt chart (Resource/Order views) | GanttControl (ProdGantt) | Interactive scheduling | COVERED-23 | 23 | |
| Capacity reservations | WrkCtrCapRes | Reserved capacity per job/resource | COVERED-23 | 23 | |
| Job list (all jobs, ready jobs, started) | ProdRouteJobListPage | Jobs across orders | COVERED-23 | 23 | |
| Manufacturing execution → Job registration (terminal: clock-in, start job, bundle jobs, report feedback, clock-out) | JmgRegistration (JmgRegistrationTouch) | Shop-floor terminal | COVERED-25 | 25 | Kiosk UI |
| Manufacturing execution → Job list (electronic job list / dispatch list) | JmgJobList | Prioritized job queue per resource | COVERED-25 | 25 | |
| Manufacturing execution → Registrations (raw registrations) | JmgTermReg | Clock/job registrations awaiting calculation | COVERED-25 | 25 | |
| Manufacturing execution → Current activities / Absences | JmgCurrentActivities | Who is on which job | COVERED-25 | 25 | |
| Manufacturing execution → Calculate / Approve (registrations per worker/day) | JmgCalculate, JmgApprove | Pay-time calculation and supervisor approval | COVERED-25 | 25 | |
| Manufacturing execution → Transfer (to payroll/projects/production) | JmgTransfer | | COVERED-25 | 25 | Export to payroll wave 43 |
| Manufacturing execution → Time and attendance (clock registrations, flex balance, absence registration) | JmgRegistration (T&A) | | COVERED-25 | 25 | |
| Manufacturing execution → Production floor execution (D365 PFE) | JmgProductionFloorExecution | Touch UI for jobs, materials, quality, maintenance requests | COVERED-25 | 25 | |
| Production control → Lean manufacturing (links) | | | COVERED-24 | 24 | §12.24 |
| Production control → Batch orders (PI) | ProdTable (batch) | | COVERED-24 | 24 | §12.23 |
| Production control → Production order cost analysis (Cost estimates and costing) | ProdCalc | Planned vs actual by cost group/type | COVERED-21 | 21 | Variance decomposition 39 |
| Production control → Delays (R3 Production orders with delays) | ProdTable (delays) | | COVERED-23 | 23 | |
| Production control → Scrap / Error cause (job card error) | ProdErrorCause | | COVERED-21 | 21 | |
| Production control → Production order properties (property for scheduling) | ProdProperty | | COVERED-23 | 23 | |
| Production control → Production groups | ProdGroup | Ledger posting grouping | COVERED-21 | 21 | |

### 12.22.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Production order inquiries (orders, BOM, route, jobs, transactions, status log) | | COVERED-21 | 21 | |
| Production transactions (Route transactions, BOM transactions) | | COVERED-21 | 21 | |
| Cost inquiry (Production cost: estimated vs realized, WIP) | | COVERED-21 | 21 | |
| Capacity → Capacity load (per resource/group/period, graphical) | | COVERED-23 | 23 | |
| Capacity → Capacity reservations | | COVERED-23 | 23 | |
| Capacity → Resource load (Gantt) | | COVERED-23 | 23 | |
| Jobs → Job list / Job status / Ready jobs | | COVERED-23 | 23 | |
| Journals → Picking list / Route card / Job card / RAF journal inquiries | | COVERED-21 | 21 | |
| Manufacturing execution → Registration inquiries (Raw registrations, Pay time, Flex balance, Absence, Job registrations, Overtime) | | COVERED-25 | 25 | |
| Manufacturing execution → Production feedback / Operator efficiency | | COVERED-25 | 25 | OEE |
| Lean → Kanban inquiries (link) | | COVERED-24 | 24 | |
| Scrap / Error cause inquiry | | COVERED-21 | 21 | |
| Production → Delayed orders / Production performance (OTD) | | COVERED-46 | 46 | |
| SSRS: Production orders (status list) / Production order overview | | COVERED-21 | 21 | |
| SSRS: Production papers (Job card, Route card, Picking list, Route card with jobs, Production order document bundle) | | COVERED-21 | 21 | Rendered 46 |
| SSRS: Production order — BOM lines / Route lines | | COVERED-21 | 21 | |
| SSRS: Capacity load / Capacity reservations / Resource load | | COVERED-23 | 23 | |
| SSRS: Production cost per period / Completed production order cost analysis / Production cost by cost group | | COVERED-39 | 39 | |
| SSRS: Route consumption / BOM consumption (planned vs actual) | | COVERED-21 | 21 | |
| SSRS: Job transactions / Route transactions | | COVERED-21 | 21 | |
| SSRS: Resources / Resource groups / Capabilities listings | | COVERED-9 | 9 | |
| SSRS: Calendars / Working time | | COVERED-9 | 9 | |
| SSRS: Routes / Operations listings | | COVERED-9 | 9 | |
| SSRS: Cost categories / Cost category prices | | COVERED-39 | 39 | |
| SSRS: Manufacturing execution reports (Registrations, Pay statistics, Absence, Flex, Job efficiency, Timecard) | | COVERED-25 | 25 | |
| SSRS: Production variance reports (link) | | COVERED-39 | 39 | |
| SSRS: Scrap report / Error cause statistics | | COVERED-21 | 21 | |
| SSRS: Work-in-progress report | | COVERED-21 | 21 | |
| SSRS: Production order label (barcode) | | COVERED-32 | 32 | |

### 12.22.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Production orders → Estimate / Schedule / Release / Start / Report as finished / End (batch) | | COVERED-21 | 21 | |
| Production orders → Update costing (Price calculation batch) | | COVERED-21 | 21 | |
| Production orders → Delete production orders / Clean up ended orders | | COVERED-21 | 21 | |
| Production orders → Reset status (batch) | | COVERED-21 | 21 | |
| Journals → Post journals (batch) / Delete posted journals | | COVERED-21 | 21 | |
| Scheduling → Operations scheduling / Job scheduling (batch, by filter) | | COVERED-23 | 23 | |
| Scheduling → Reschedule all / Delete capacity reservations | | COVERED-23 | 23 | |
| Scheduling → Compose working times (generate calendar days from templates) | | COVERED-9 | 9 | |
| Scheduling → Update calendars (calendar expiry extension) | | COVERED-9 | 9 | |
| Manufacturing execution → Calculate registrations (batch) / Approve (batch) / Transfer (batch) | | COVERED-25 | 25 | |
| Manufacturing execution → Generate flex balances / Reset flex / Reset statistics balances | | COVERED-25 | 25 | |
| Manufacturing execution → Update job list / Clean up registrations | | COVERED-25 | 25 | |
| Manufacturing execution → Auto-clock-out | | COVERED-25 | 25 | |
| Lean → Backflush costing (periodic) | | COVERED-24 | 24 | |
| Lean → Kanban periodic (update kanban quantities, process scheduled kanbans) | | COVERED-24 | 24 | |
| Production → Production order status refresh for WHS (release to warehouse) | | COVERED-31 | 31 | |
| Production → IoT: machine event ingestion / OEE calc | | COVERED-25 | 25 | |
| Production → Update route/BOM on open orders (mass re-explosion) | | COVERED-21 | 21 | |
| Production → Production order cost roll-up refresh (planned cost re-estimate) | | COVERED-21 | 21 | |

### 12.22.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Production control parameters — General | Production order defaults: BOM/route version selection, reservation (manual/estimation/scheduling/release/start), automatic BOM consumption (flushing principle: start/finish/manual), route consumption, posting of journals, scheduling method default, lock route operations, remove reservations, status defaults | COVERED-21 | 21 | |
| Production control parameters — Standard update tab | Default update parameters per status update (estimation, scheduling, release, start, RAF, end) | COVERED-21 | 21 | |
| Production control parameters — Start tab | Picking list posting at start (quantity: start qty/remaining), route card at start, end-mark, reference status update | COVERED-21 | 21 | |
| Production control parameters — Operations tab | Route card/job card defaults, hours/qty posting, scrap handling | COVERED-21 | 21 | |
| Production control parameters — Journals tab | Default journal names per type | COVERED-21 | 21 | |
| Production control parameters — Automatic update tab | Automatic report as finished, end-mark, auto-start, reference level update | COVERED-21 | 21 | |
| Production control parameters — General ledger tab | Ledger posting: item+category, item+resource, none; post costs during production (WIP); profit setting; posting of RAF by cost group | COVERED-21 | 21 | |
| Production control parameters — Number sequences tab | | COVERED-5 | 5 | |
| Production control parameters — Lean tab / Process tab (PI) | | COVERED-24 | 24 | |
| Production parameters by site (Site-specific production parameters) | Overrides per site | COVERED-21 | 21 | |
| Production → Production groups | Ledger posting profile per group (WIP accounts, issue/receipt accounts) | COVERED-21 | 21 | |
| Production → Production pools | | COVERED-21 | 21 | |
| Production → Production journal names (picking list, route card, job card, RAF) | | COVERED-21 | 21 | |
| Production → Production posting profiles (in Inventory posting: Production tab: estimated cost consumed, WIP issue, WIP account, cost of materials consumed, issue, receipt, RAF) | | COVERED-21 | 21 | |
| Production → Error causes (scrap reason codes) | | COVERED-21 | 21 | |
| Production → Properties (scheduling property per resource/period) | | COVERED-23 | 23 | |
| Production → Priority (production priority) | | COVERED-23 | 23 | |
| Routes → Route groups (setup time/run time/queue time estimation flags, job management, capacity, reporting, scheduling of jobs, working time, cost categories) | | COVERED-9 | 9 | |
| Routes → Operations (link) | | COVERED-9 | 9 | |
| Routes → Cost categories (hourly/piece rates, cost group, ledger accounts) | | COVERED-39 | 39 | |
| Routes → Shared categories (project/production category flags) | | COVERED-9 | 9 | |
| Routes → Cost category price per costing version | | COVERED-39 | 39 | |
| Routes → Route version parameters (approval, activation) | | COVERED-9 | 9 | |
| Routes → Route types (simple/network: sequential/parallel) | | COVERED-9 | 9 | |
| Routes → Route operation relations: Primary/secondary resources, Link types (hard/soft), Overlap quantity, Transfer batch | | COVERED-9 | 9 | |
| Resources → Resource groups | | COVERED-9 | 9 | |
| Resources → Resources (types: vendor, human resources, machine, tool, location) | | COVERED-9 | 9 | |
| Resources → Capabilities / Resource capabilities (level, priority, expiry) | | COVERED-9 | 9 | |
| Resources → Resource types / Competency-based (skills, courses, certificates for human resources) | | COVERED-9 | 9 | HR competency in 42 |
| Resources → Resource bottleneck / Finite capacity flag / Efficiency % / Capacity unit | | COVERED-23 | 23 | |
| Resources → Resource ledger posting (operations resource, WIP, issue accounts) | | COVERED-21 | 21 | |
| Resources → Vendor as resource (subcontracting) | | COVERED-21 | 21 | |
| Calendars → Calendars (working time calendars), Working time templates (shifts, efficiency %, properties), Compose working times | | COVERED-9 | 9 | |
| Calendars → Calendar properties / Working time intervals | | COVERED-9 | 9 | |
| Scheduling → Scheduling parameters (Scheduling direction, Scheduling method default, Capacity: finite/infinite, Finite material, Finite property, Capability-based scheduling, Schedule references, Sync references, Cancel capacity reservations, Primary resource selection (duration/first available/ etc.), Scheduling time fence) | | COVERED-23 | 23 | |
| Scheduling → Operations scheduling percentage / Scheduling of jobs: Job types, Job management | | COVERED-23 | 23 | |
| Scheduling → Load (capacity load calculation: setup/process/queue/transport included) | | COVERED-23 | 23 | |
| Sequencing → Sequence groups / Sequences (PI) | | COVERED-23 | 23 | |
| Manufacturing execution → Manufacturing execution parameters (General: registration validation, job bundling, feedback, Pay: pay agreement, flex, overtime; Production: picking list/RAF posting from registrations; Absence; Terminal) | | COVERED-25 | 25 | |
| Manufacturing execution → Registration configuration / Terminal configuration (touch screen layout, buttons, barcode) | | COVERED-25 | 25 | Kiosk configuration |
| Manufacturing execution → Workers (time registration workers: profile group, calculation group, approval group, pay agreement, flex allowed, terminal) | | COVERED-25 | 25 | |
| Manufacturing execution → Profiles (clock profiles with profile types: standard time, flex+, flex-, overtime, break, pay) | | COVERED-25 | 25 | Pay-type calculation |
| Manufacturing execution → Profile groups / Profile calendar / Profile relations | | COVERED-25 | 25 | |
| Manufacturing execution → Pay: Pay agreements, Pay agreement lines (pay types by time/overtime/shift), Pay types, Pay items, Statistics balances, Counters | | COVERED-25 | 25 | |
| Manufacturing execution → Indirect activities (categories, activities: break, meeting, maintenance) | | COVERED-25 | 25 | Downtime reason codes |
| Manufacturing execution → Absence (codes, groups, statistics) | | COVERED-42 | 42 | Shared with HR |
| Manufacturing execution → Groups (Calculation groups, Approval groups, Supervisors) | | COVERED-25 | 25 | |
| Manufacturing execution → Flex (flex profiles, flex groups, flex balance limits) | | COVERED-25 | 25 | |
| Manufacturing execution → Switch codes / Job bundling / Job priority | | COVERED-25 | 25 | |
| Manufacturing execution → Production feedback (feedback parameters, auto-report) | | COVERED-25 | 25 | |
| Manufacturing execution → Production floor execution: Configure PFE interface (tabs, buttons, kiosk), Device setup (D365) | | COVERED-25 | 25 | |
| Manufacturing execution → Time and attendance parameters (shifts, rounding, grace) | | COVERED-25 | 25 | |
| Lean manufacturing setup (link) | | COVERED-24 | 24 | §12.24 |
| Process manufacturing setup (link) | | COVERED-24 | 24 | §12.23 |
| Production → IoT (D365 Sensor Data Intelligence: scenarios, sensors, notifications) | | COVERED-25 | 25 | OPC-UA/MQTT |
| Production → Subcontracting setup (vendor resource, line type vendor, purchase on release) | | COVERED-21 | 21 | |
| Production → Costing sheet / Cost groups (link) | | COVERED-39 | 39 | |
| Production → BOM/Route version activation rules | | COVERED-9 | 9 | |
| Production → Quality associations for production (link) | | COVERED-33 | 33 | |
| Production → Warehouse: Production input/output locations, License plate RAF (WHS) | | COVERED-31 | 31 | |
| Production → Workflow (production order release approval D365) | | COVERED-4 | 4 | |
| Production → Number sequences | | COVERED-5 | 5 | |
| Production → Gantt chart configuration (colors, views, user settings) | | COVERED-23 | 23 | |
| Production → Andon / alerts (Passage) | | COVERED-25 | 25 | |

## 12.23 Process manufacturing (PI)

AX 2012 R3 folds process industries into PIM, Inventory, Production control, Master planning and Sales. Formulas, batch orders, co/by-products, potency, catch weight, batch attributes/shelf life, batch balancing, consolidated batch orders, rework and sequencing map to waves 24 (process manufacturing), 9 (formula versions) and 23 (sequencing). Commodity pricing, vendor rebates, brokers/royalties and product compliance are gaps (see §12.15, §12.16).

### 12.23.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Formulas (list page) / Formula designer | PdsFormulaListPage (BOMTable formula), BOMDesigner | Formula with co/by-products, scalable lines, percentage lines, step consumption | COVERED-9 | 9 | Formula BOMs |
| Formula versions (per item, with formula size, co-product/by-product lines, approval) | BOMVersion (formula) | | COVERED-9 | 9 | |
| Formula lines (line types: scalable, step consumption, percent, variable/fixed, catch weight) | BOMLine (formula) | | COVERED-9 | 9 | |
| Co-products / By-products on formula (cost allocation %, burden: by-product cost allocation: none, percent of cost, per quantity, per series, recycled) | PdsCoByProduct | | COVERED-24 | 24 | Co/by-product allocation methods |
| Batch orders (list page) | ProdTableListPage (batch) | Batch orders from formulas | COVERED-24 | 24 | |
| Batch order details | ProdTable (PdsBatch) | Formula explosion, co/by-products, batch attributes, potency, batch balancing, rework | COVERED-24 | 24 | |
| Batch order → Batch balancing | PdsBatchBalancing | Adjust ingredient quantities by actual potency of selected batches | COVERED-24 | 24 | Potency fixtures |
| Batch order → Consolidated batch orders (batch order consolidation / bulk item → packed items) | PdsConsolidatedOrder | Bulk and packed order relation | PARTIAL-24 (consolidated batch orders (bulk/pack relationship) not named) | 24 | Gap register |
| Batch order → Rework batch order | PdsRework | Rework a finished batch | PARTIAL-24 (rework batch orders not named) | 24 | Gap register |
| Batch order → Report as finished with co/by-products | ProdJournalProd (coby) | | COVERED-24 | 24 | |
| Planning items (formula for planning multiple co-products) | InventTable (planning item) | | COVERED-24 | 24 | |
| Batch attributes (Inventory batches, link) | PdsBatchAttrib | Attributes with min/max/target/tolerance | COVERED-24 | 24 | |
| Batch attribute inheritance (lot inheritance: product-to-product, co-product inheritance) | PdsInheritance | Inherit attributes/shelf life from ingredients | PARTIAL-24 (batch attribute and shelf-life inheritance rules not named) | 24 | Gap register |
| Catch weight (CW) transactions on orders (sales/purchase/production/inventory) | PdsCatchWeight | | COVERED-24 | 24 | |
| Potency (active ingredient) on batches / Potency pricing (price per unit of active ingredient) | PdsIngredient / PdsPotencyPricing | | COVERED-24 | 24 | Potency pricing PARTIAL-26 (commodity pricing) |
| Shelf life (best before, expiry, shelf advice) / Batch disposition codes | InventBatch (shelf life) | | COVERED-24 | 24 | |
| Sequencing → Sequenced planned orders / Sequencing of batch orders | PdsSequence | | COVERED-23 | 23 | |
| Commodity pricing (Pricing calculation, link) | PdsCommodity* | | PARTIAL-26 (commodity pricing) | 26 | |
| Vendor rebates / Brokers / Royalties (links) | | | GAP | — | Propose wave 26: vendor rebates (see §12.16) |
| Product compliance / Safety data sheets (link) | | | GAP | — | Propose wave 33: product compliance (see §12.15) |
| Container packaging (PI packaging / Container types) | PdsContainer | Packaging hierarchy | PARTIAL-28 (packaging materials) | 28 | |
| Country of origin (batch-level) | PdsCountryOfOrigin | | COVERED-24 | 24 | |
| Item tracing (batch/serial genealogy) | InventTrack | | COVERED-24 | 24 | |
| Batch merge | PdsBatchMerge | | PARTIAL-24 (batch merge) | 24 | |
| Same-batch reservation (batch reservation rules: consolidated/same batch on sales line) | PdsSameBatch | | PARTIAL-24 (same-batch reservation requirement on sales lines not named) | 24 | Gap register |
| Customer-specific batch attribute requirements (customer sellable days / batch attribute requirements on sales) | PdsCustSellableDays, PdsCustBatchAttrib | Shelf life minimum per customer, attribute ranges per customer | PARTIAL-24 (customer sellable days / customer batch attribute requirements on sales lines not named) | 24 | Gap register |

### 12.23.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Batch order inquiries (co-product cost, batch balancing results) | | COVERED-24 | 24 | |
| Batch attribute inquiries / Attribute search for batches | | COVERED-24 | 24 | |
| Item tracing / Genealogy inquiry | | COVERED-24 | 24 | |
| Catch weight on-hand / transactions | | COVERED-24 | 24 | |
| Shelf life inquiries (Expired, Expiring, Shelf advice) | | COVERED-24 | 24 | |
| Potency inquiries | | COVERED-24 | 24 | |
| Sequencing inquiry | | COVERED-23 | 23 | |
| Commodity price inquiries | | PARTIAL-26 (commodity pricing) | 26 | |
| SSRS: Formula report / Formula with co-products | | COVERED-9 | 9 | |
| SSRS: Batch order papers (Batch card, Picking list with potency) | | COVERED-24 | 24 | |
| SSRS: Batch attribute certificate (link to CoA) | | COVERED-33 | 33 | |
| SSRS: Lot/batch traceability report | | COVERED-24 | 24 | |
| SSRS: Catch weight reports | | COVERED-24 | 24 | |
| SSRS: Shelf life / Expiry reports | | COVERED-24 | 24 | |
| SSRS: Country of origin report | | COVERED-24 | 24 | |
| SSRS: Safety data sheet (link) | | GAP | — | Propose wave 33: product compliance |
| SSRS: Co-product cost allocation report | | COVERED-24 | 24 | |

### 12.23.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Batch orders → Estimate / Schedule / Release / Start / RAF / End (batch) | | COVERED-24 | 24 | |
| Batch orders → Consolidate batch orders (batch) | | PARTIAL-24 (consolidated batch orders) | 24 | |
| Batches → Update shelf life / Expire batches / Reset disposition | | COVERED-24 | 24 | |
| Batches → Attribute inheritance processing on RAF | | PARTIAL-24 (inheritance) | 24 | |
| Commodity pricing → Price calculation run (update trade agreements from commodity index) | | PARTIAL-26 (commodity pricing) | 26 | |
| Sequencing → Run sequencing on planned orders | | COVERED-23 | 23 | |
| Catch weight → Update nominal weights | | COVERED-24 | 24 | |
| Compliance → SDS expiry / regulated product reporting | | GAP | — | Propose wave 33: product compliance |
| Vendor rebates → Process | | GAP | — | Propose wave 26: vendor rebates |

### 12.23.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Production control parameters — Process tab (formula defaults, co-product posting, batch balancing, rework) | | COVERED-24 | 24 | |
| Formula → Formula parameters (scalable, formula size, yield %, step consumption tables) | | COVERED-9 | 9 | |
| Formula → Co-product cost allocation methods / By-product burden types | | COVERED-24 | 24 | |
| Batch → Batch attributes, Attribute groups, Product-specific attributes, Customer-specific attributes | | COVERED-24 | 24 | |
| Batch → Batch disposition master / Disposition codes | | COVERED-24 | 24 | |
| Batch → Shelf life setup, Batch number groups, Expiry calculation (from production date/receipt date) | | COVERED-24 | 24 | |
| Batch → Lot inheritance setup (inheritance of attributes/shelf life by product) | | PARTIAL-24 (inheritance) | 24 | |
| Batch → Same-batch reservation / Batch reservation parameters (FEFO date-controlled) | | COVERED-16 | 16 | Same-batch PARTIAL-24 |
| Potency → Base attribute, Potency item setup, Potency pricing (per active ingredient) | | COVERED-24 | 24 | |
| Catch weight → CW item setup, CW units, Nominal/min/max weight | | COVERED-24 | 24 | |
| Sequencing → Sequences, Sequence groups, Sequence priorities (link) | | COVERED-23 | 23 | |
| Commodity pricing → Cost basis types, Pricing calculations, Price margin templates | | PARTIAL-26 (commodity pricing) | 26 | |
| Product compliance → Regulated/restricted lists, Reporting lists, SDS setup | | GAP | — | Propose wave 33: product compliance |
| Vendor rebates / Brokers / Royalties setup | | GAP | — | Propose wave 26: vendor rebates |
| Container packaging → Container types, Packaging groups | | PARTIAL-28 (packaging materials) | 28 | |
| Country of origin setup | | COVERED-24 | 24 | |
| Planning item setup (planning formula) | | COVERED-24 | 24 | |
| Customer sellable days / Customer batch attribute requirements | | PARTIAL-24 (customer sellable days) | 24 | |
| Rework → Rework parameters | | PARTIAL-24 (rework) | 24 | |
| Consolidated batch orders → Bulk/pack item relations | | PARTIAL-24 (consolidated batch orders) | 24 | |
| Number sequences (PI) | | COVERED-5 | 5 | |

## 12.24 Lean manufacturing

Value streams, production flows, activities, kanban rules, kanban boards and backflush costing map to wave 24. Value streams as operating units are in wave 6.

### 12.24.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Production flows (list page) | LeanProductionFlowListPage | Flow with versions and activities | COVERED-24 | 24 | |
| Production flow details / Versions (activate, expire, takt, cycle time) | LeanProductionFlow / LeanProductionFlowVersion | | COVERED-24 | 24 | |
| Activities (process/transfer activities with resource, locations, run time, takt) | LeanProductionFlowActivity (PlanActivity) | | COVERED-24 | 24 | |
| Activity relations (successor/predecessor, cycle time ratio) | PlanActivityRelation | | COVERED-24 | 24 | |
| Kanban rules (list page) | KanbanRuleListPage | Manufacturing/withdrawal; fixed/scheduled/event; quantities, lead time, circulating cards | COVERED-24 | 24 | |
| Kanbans (list page: all kanbans, by rule) | KanbanListPage / Kanban | Kanban cards with jobs and status | COVERED-24 | 24 | |
| Kanban board for process jobs | KanbanJobSchedulingBoard | Drag jobs between planned/in-progress/completed; board per cell | COVERED-24 | 24 | |
| Kanban board for transfer jobs | KanbanJobTransferBoard | Transfer execution board | COVERED-24 | 24 | |
| Kanban schedule board (scheduling of kanban jobs) | KanbanJobSchedulingPeriod | Period/resource schedule | COVERED-24 | 24 | |
| Kanban quantity calculation (Kanban quantity calculation policies, calculation runs) | KanbanQuantityCalculation | Recalculate kanban quantities from demand | COVERED-24 | 24 | |
| Event kanbans (sales/production/kanban line/stock replenishment events) | Kanban (event) | | COVERED-24 | 24 | Event kanban simulation |
| Kanban cards (print, card circulation) | KanbanCard | | COVERED-24 | 24 | |
| Lean → Backflush costing (cost accounting for production flows: periodic conversion costs, variance) | LeanCostingPeriodicProcess | Period-based costing of production flows | COVERED-24 | 24 | |
| Lean → Production flow costing (cost group breakdown, WIP by flow) | LeanCostFlow | | COVERED-24 | 24 | |
| Lean → Subcontracting in lean (service activities with vendor, purchase agreements) | LeanSubcontracting | Service-based subcontracting on activities | PARTIAL-24 (lean subcontracting (activity-based service purchase from vendor, purchase agreement linkage) not named; discrete subcontracting is in 21) | 24 | Gap register |
| Lean → Picking list (kanban material pick) / Kanban job pick list | KanbanJobPickingList | | COVERED-24 | 24 | |
| Lean → Scheduled kanban jobs (from master planning planned kanbans) | | | COVERED-22 | 22 | Planned kanbans |
| Value streams (Org admin operating unit) | OMValueStream | | COVERED-6 | 6 | |
| Lean → Kanban supermarket / Kanban location setup (WHS) | | | COVERED-31 | 31 | |

### 12.24.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Kanban job inquiries (jobs by status, late jobs, cycle time) | | COVERED-24 | 24 | |
| Kanban rule inquiries / Kanban quantities history | | COVERED-24 | 24 | |
| Production flow inquiries (activity load, takt vs actual) | | COVERED-24 | 24 | |
| Lean costing inquiries (period costs, variances per flow) | | COVERED-24 | 24 | |
| Event kanban inquiry (source events) | | COVERED-24 | 24 | |
| SSRS: Kanban card (print) / Kanban card circulation report | | COVERED-24 | 24 | |
| SSRS: Kanban rule report / Kanban overview | | COVERED-24 | 24 | |
| SSRS: Production flow report / Activity report | | COVERED-24 | 24 | |
| SSRS: Kanban quantity calculation report | | COVERED-24 | 24 | |
| SSRS: Backflush costing report / Lean variances | | COVERED-24 | 24 | |
| SSRS: Kanban job pick list | | COVERED-24 | 24 | |

### 12.24.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Kanban → Process event kanbans (generate from events: sales, BOM line, min stock) | | COVERED-24 | 24 | |
| Kanban → Kanban quantity calculation run / Update kanban rules from calculation | | COVERED-24 | 24 | |
| Kanban → Print kanban cards (batch) / Reprint | | COVERED-24 | 24 | |
| Kanban → Schedule kanban jobs (automatic planning) | | COVERED-24 | 24 | |
| Kanban → Expire fixed kanbans / Clean up completed kanbans | | COVERED-24 | 24 | |
| Lean → Backflush costing period processing (calculate conversion costs, post, close period) | | COVERED-24 | 24 | |
| Lean → Production flow version activation/expiry (batch) | | COVERED-24 | 24 | |
| Lean → Transfer job auto-complete (on receive) | | COVERED-24 | 24 | |
| Lean → Lean subcontracting: generate purchase orders for service activities | | PARTIAL-24 (lean subcontracting) | 24 | |
| Lean → Kanban rule effectivity update from master planning | | COVERED-22 | 22 | |

### 12.24.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Production control parameters — Lean tab (kanban defaults, costing, picking list posting, transfer defaults) | | COVERED-24 | 24 | |
| Lean → Production flow models (costing method per flow: backflush costing) | | COVERED-24 | 24 | |
| Lean → Kanban rule policies (replenishment strategy: fixed, scheduled, event; kanban quantity, min/max, lead time, circulating/one-time; automatic planning) | | COVERED-24 | 24 | |
| Lean → Kanban quantity calculation policies (demand period, forecast/orders, safety stock, rounding) | | COVERED-24 | 24 | |
| Lean → Kanban card configuration (card layout, card number sequence, barcode) | | COVERED-24 | 24 | |
| Lean → Kanban board configuration (board per work cell, columns, colors, job filters) | | COVERED-24 | 24 | |
| Lean → Work cells (resource groups of type work cell, capacity units, calendars) | | COVERED-9 | 9 | |
| Lean → Lean schedule groups | | COVERED-24 | 24 | |
| Lean → Kanban handling units (bins/containers) | | COVERED-24 | 24 | |
| Lean → Activity resource/location defaults (input/output locations, supermarket) | | COVERED-24 | 24 | |
| Lean → Lean costing: Cost groups for lean, Cost category for activities, Conversion cost posting, WIP accounts per production flow | | COVERED-24 | 24 | |
| Lean → Subcontracting setup (service items, purchase agreements for activities) | | PARTIAL-24 (lean subcontracting) | 24 | |
| Lean → Kanban job registration (manufacturing execution integration) | | COVERED-25 | 25 | |
| Lean → Value streams (link) | | COVERED-6 | 6 | |
| Lean → Number sequences (kanban, production flow) | | COVERED-5 | 5 | |
| Lean → Mobile device kanban flows (WHS mobile: receive kanban, empty kanban) | | COVERED-32 | 32 | |

## 12.25 Quality management

In AX 2012 R3 quality lives under Inventory management (quality orders, nonconformances, corrections, test groups, quality associations, certificates of analysis). It maps to wave 33, with inventory blocking from wave 16 and calibration/SPC additions.

### 12.25.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Quality orders (list page) | InventQualityOrderTableListPage | Orders generated by associations or manually; reference (purchase, sales, production, quarantine, inventory, RMA) | COVERED-33 | 33 | |
| Quality order details | InventQualityOrderTable / InventQualityOrderLine | Tests, sampling, results (variable/attribute), pass/fail, validate | COVERED-33 | 33 | Results recording |
| Quality order → Results (test results per sample) | InventQualityOrderLineResults | | COVERED-33 | 33 | |
| Quality order → Validate / Close | InventQualityOrderValidate | Usage decision | COVERED-33 | 33 | Usage decision |
| Nonconformances (list page) | InventNonConformanceTableListPage | NCR with problem type, source (customer, service request, internal, vendor, production), quarantine zone | COVERED-33 | 33 | NCR/CAPA |
| Nonconformance details | InventNonConformanceTable | Approve/refuse, related operations, corrections, quarantine link | COVERED-33 | 33 | |
| Nonconformance → Operations (nonconformance operations with cost items, timesheets, charges) | InventNonConformanceOperation | Work to resolve NC with cost | COVERED-33 | 33 | Cost of quality |
| Corrections | InventNonConformanceCorrection | Corrective actions with diagnostic types, planned/actual dates | COVERED-33 | 33 | CAPA with 8D |
| Certificates of analysis (print from sales packing slip / quality order) | InventTestCertificateOfAnalysis | CoA document | COVERED-33 | 33 | |
| Quality → Test instruments / Device calibration (Passage addition) | | | COVERED-33 | 33 | Calibration tracking |
| Quality → Supplier quality scorecards (Passage) | | | COVERED-33 | 33 | |
| Quality → SPC charts (Passage) | | | COVERED-33 | 33 | |
| Quality → Inventory blocking (quality) | InventBlocking (quality) | Auto-block on quality order creation | COVERED-33 | 33 | Automatic blocking/release |
| Quality → Quarantine orders (link) | InventQuarantineOrder | | COVERED-16 | 16 | |
| Quality → Batch disposition on failed quality (link) | | | COVERED-24 | 24 | |
| Quality → D365 quality management mobile flows (quality order on mobile device) | WHSQuality* | Quality on handheld | COVERED-32 | 32 | |
| Quality → Vendor quality (quality by vendor, test by vendor) | InventTestAssociationTable (vendor) | | COVERED-33 | 33 | |

### 12.25.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Quality order inquiries (by status, item, vendor, test results) | | COVERED-33 | 33 | |
| Nonconformance inquiries (by problem type, source, status) | | COVERED-33 | 33 | |
| Correction inquiries | | COVERED-33 | 33 | |
| Test result statistics (pass rate by item/vendor) | | COVERED-33 | 33 | |
| SSRS: Certificate of analysis | | COVERED-33 | 33 | |
| SSRS: Quality order (document) / Quality order test results | | COVERED-33 | 33 | |
| SSRS: Nonconformance (document) / Nonconformance tag / Nonconformance report | | COVERED-33 | 33 | |
| SSRS: Correction report | | COVERED-33 | 33 | |
| SSRS: Nonconformance operations / Cost of quality | | COVERED-33 | 33 | |
| SSRS: Test groups / Tests listings | | COVERED-33 | 33 | |
| SSRS: Quality associations listing | | COVERED-33 | 33 | |
| SSRS: Vendor quality performance | | COVERED-33 | 33 | |
| SSRS: Item sampling listing | | COVERED-33 | 33 | |

### 12.25.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Quality → Create quality orders automatically (on events per association) | | COVERED-33 | 33 | Trigger fixtures |
| Quality → Validate quality orders (batch) | | COVERED-33 | 33 | |
| Quality → Delete / Close nonconformances | | COVERED-33 | 33 | |
| Quality → Calibration due processing (Passage) | | COVERED-33 | 33 | |
| Quality → Supplier scorecard recalculation | | COVERED-33 | 33 | |
| Quality → Nonconformance cost posting (operations charges) | | COVERED-33 | 33 | |

### 12.25.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Inventory and warehouse management parameters — Quality management tab | Use quality management, hours per day, report setup (CoA, NC tag), nonconformance defaults | COVERED-33 | 33 | |
| Quality management → Quality associations (reference type: purchase/sales/production/quarantine/inventory; item code; account code; event type: before/after; execution: before/after; test group; document type; dimensions) | | COVERED-33 | 33 | |
| Quality management → Test groups (tests, sequence, sampling, acceptable quality level, instrument, destructive, test variable/outcome) | | COVERED-33 | 33 | |
| Quality management → Tests (type: integer/fraction/option, unit, instrument) | | COVERED-33 | 33 | |
| Quality management → Test variables / Test variable outcomes (pass/fail outcomes) | | COVERED-33 | 33 | |
| Quality management → Test instruments | | COVERED-33 | 33 | |
| Quality management → Item sampling (fixed quantity / percent / license plate / full blocking) | | COVERED-33 | 33 | AQL sampling plans |
| Quality management → Quality groups / Item quality groups (D365) | | COVERED-33 | 33 | |
| Quality management → Nonconformance: Problem types, Quarantine zones, Diagnostic types, Operations (NC operation types), Charges on NC operations | | COVERED-33 | 33 | |
| Quality management → Nonconformance types (customer, service request, internal, vendor, production) | | COVERED-33 | 33 | |
| Quality management → Certificate of analysis setup (print per customer / item) | | COVERED-33 | 33 | |
| Quality management → Quality orders number sequences | | COVERED-5 | 5 | |
| Quality management → Quality order blocking parameters (block on create, release on validate) | | COVERED-33 | 33 | |
| Quality management → Calibration schedules (Passage) | | COVERED-33 | 33 | |
| Quality management → CAPA templates (8D) (Passage) | | COVERED-33 | 33 | |
| Quality management → Workflow (nonconformance approval) | | COVERED-4 | 4 | |
| Quality management → Mobile device quality menu items (D365) | | COVERED-32 | 32 | |
| Quality management → Product compliance (PI, link) | | GAP | — | Propose wave 33: product compliance |

## 12.26 Warehouse management (WHS)

The AX 2012 R3 advanced warehouse module (Warehouse management) — location directives, work templates, waves, loads, license plates, mobile device menu items, replenishment, cycle counting, packing, labels — maps to wave 31 (rules, work, waves, loads, replenishment, slotting) and wave 32 (mobile app, labels, RFID/voice). Dock scheduling is wave 31; TMS pieces (§12.27) wave 34.

### 12.26.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| All loads (list page) / Load details | WHSLoadTableListPage / WHSLoadTable | Loads with lines, shipments, status, carrier | COVERED-31 | 31 | Load planning |
| Load planning workbench | WHSLoadPlanningWorkbench | Build loads from order lines, templates, release | COVERED-31 | 31 | |
| Shipments (list page) / Shipment details | WHSShipmentTableListPage / WHSShipmentTable | Shipment per order/load with containers | COVERED-28 | 28 | WHS shipments in 31 |
| Waves (list page) / Wave details | WHSWaveTableListPage / WHSWaveTable | Wave lines, process, release, status | COVERED-31 | 31 | |
| All work (list page) / Work details | WHSWorkTableListPage / WHSWorkTable | Work headers/lines (pick/put), status, user, LP | COVERED-31 | 31 | |
| Work → Open work / Work by user / Work audit | WHSWorkListPage (filters) | | COVERED-31 | 31 | |
| Work → Cancel work / Reassign work / Unblock | WHSWorkCancel, WHSWorkReassign | Exceptions | COVERED-31 | 31 | |
| License plates (list) / License plate details | WHSLicensePlate | LP with nested parents, on-hand | COVERED-31 | 31 | |
| Locations (WHS) | WHSLocation (WMSLocation with profile) | Bins with profile, zone, stocking limits | COVERED-31 | 31 | |
| Containers (list) / Containerization | WHSContainerTable | Packing containers, SSCC | COVERED-28 | 28 | Containers/HU |
| Pack (packing station form) | WHSPackContainer (Pack) | Pack items into containers at packing station | COVERED-28 | 28 | |
| Replenishment → Replenishment work / Min-max | WHSReplenishment | | COVERED-31 | 31 | |
| Cycle counting → Cycle count work / Cycle count plans runs | WHSCycleCountPlan (run) | | COVERED-31 | 31 | |
| Cycle counting → Count approvals (variance review) | WHSCycleCountWorkApproval | | COVERED-31 | 31 | |
| Inventory status (on-hand by status) | WHSInventoryStatus | | COVERED-16 | 16 | |
| Dock appointment scheduling → Appointments / Dock management | TMSAppointment* (TMSDockMgmt) | Carrier appointments per dock | COVERED-31 | 31 | Dock scheduling |
| Inbound → ASN (Advanced shipping notice import) / Inbound loads | WHSUOMStructure / WHSASN | ASN with packing structure to LP | COVERED-31 | 31 | ASN-driven receiving |
| Inbound → Receiving (mobile) / Receipt lines | WHSRFReceive | | COVERED-32 | 32 | |
| Outbound → Release to warehouse (sales / transfer / production) | WHSReleaseToWarehouse | | COVERED-28 | 28 | |
| Outbound → Manual packing / Ship confirm loads / Bill of lading | WHSLoadShipConfirm, WHSBillOfLading | | COVERED-28 | 28 | BOL doc |
| Outbound → Cross docking (D365 planned cross docking, opportunistic) | WHSCrossDocking | Cross-dock inbound to outbound | PARTIAL-31 (cross docking (planned/opportunistic) not named) | 31 | Gap register |
| Outbound → Wave label printing / Document routing | WHSDocumentRouting | | COVERED-32 | 32 | |
| Mobile device → Work queue / Mobile device portal (AX 2012 R3 WHS mobile portal, IIS) | WHSWorkExecute (mobile) | Handheld execution | SUPERSEDED (PWA + native shell mobile app wave 32) | 32 | |
| Mobile device → System-directed / user-directed picking, cluster picking, work with LP, print | WHSWorkExecute (menu items) | | COVERED-32 | 32 | |
| Work users / Work user sessions | WHSWorkUser / WHSWorkUserSession | | COVERED-32 | 32 | Session/device management |
| Product → Warehouse tab on released product (unit sequence group, reservation hierarchy, filter codes, physical dimensions) | InventTable (WHS) | | COVERED-31 | 31 | |
| Inventory → Movement by template (ad hoc moves) | WHSWorkTemplate (movement) | | COVERED-31 | 31 | |
| Inventory → Inventory adjustments (WHS) | WHSRFAdjustment | | COVERED-32 | 32 | |
| Inventory → License plate build / Consolidate / Split | | | COVERED-31 | 31 | |
| Inventory → Inventory status change | WHSInventoryStatusChange | | COVERED-16 | 16 | |
| Production → Raw material picking / RAF with LP / Put-away from production (WHS) | WHSProdPickingList | | COVERED-31 | 31 | |
| Kanban → Kanban supermarket (WHS lean) | | | COVERED-31 | 31 | |
| Quality → Quality orders on mobile / Quality item sampling LP (link) | | | COVERED-32 | 32 | |
| WHS → Warehouse slotting (D365 slotting templates, slotting demand) | WHSSlotting | | COVERED-31 | 31 | Slotting analysis |
| WHS → Warehouse workload visualization (D365 workspace) | | | COVERED-31 | 31 | Warehouse KPIs |
| WHS → Catch weight on mobile (D365) | | | COVERED-24 | 24 | |
| WHS → Warehouse app task validator / Detours (D365) | WHSMobileDeviceDetour | | COVERED-32 | 32 | Flow designer |
| WHS → Sales order picking with packing structure (D365 packing structure) | | | COVERED-28 | 28 | |
| WHS → Transfer order WHS flow (ship/receive with LP) | | | PARTIAL-16 (transfer orders) | 31 | WHS execution of transfer orders depends on transfer orders gap |

### 12.26.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| On-hand by location / On-hand by license plate | | COVERED-31 | 31 | |
| Work inquiries (Work line history, Work audit, Work exceptions log) | | COVERED-31 | 31 | |
| Location load / Location stocking limits inquiry | | COVERED-31 | 31 | Capacity never exceeded |
| Wave inquiries (Wave history, Wave lines) | | COVERED-31 | 31 | |
| Load inquiries (Load lines, Load history, Shipments per load) | | COVERED-31 | 31 | |
| License plate inquiry / LP history | | COVERED-31 | 31 | |
| Cycle count inquiries (Count history, Variances) | | COVERED-31 | 31 | |
| Replenishment inquiry | | COVERED-31 | 31 | |
| Mobile device inquiries (User sessions, Work user activity) | | COVERED-32 | 32 | |
| Inventory status inquiry | | COVERED-16 | 16 | |
| Dock appointment inquiries | | COVERED-31 | 31 | |
| Warehouse KPIs (throughput, pick rates, accuracy) | | COVERED-46 | 46 | |
| SSRS: Pick list (WHS work document) / Work pick list | | COVERED-31 | 31 | |
| SSRS: Packing slip (WHS) / Bill of lading / Container label / Shipping label (SSCC) | | COVERED-28 | 28 | Labels 32 |
| SSRS: License plate label / Location label / Wave label | | COVERED-32 | 32 | ZPL |
| SSRS: Load report / Load manifest | | COVERED-31 | 31 | |
| SSRS: Cycle count report / Count sheets | | COVERED-31 | 31 | |
| SSRS: Location report / Zone listing / Location profile listing | | COVERED-31 | 31 | |
| SSRS: Work template / Location directive listings | | COVERED-31 | 31 | |
| SSRS: Replenishment report | | COVERED-31 | 31 | |
| SSRS: Inventory status report | | COVERED-16 | 16 | |
| SSRS: Dock schedule | | COVERED-31 | 31 | |
| SSRS: Work user productivity | | COVERED-46 | 46 | |
| SSRS: Cross docking report | | PARTIAL-31 (cross docking) | 31 | |

### 12.26.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Wave processing (batch wave release / scheduled waves) | | COVERED-31 | 31 | |
| Replenishment → Min-max replenishment / Demand replenishment / Wave demand replenishment (batch) | | COVERED-31 | 31 | |
| Replenishment → Load demand replenishment (D365) | | COVERED-31 | 31 | |
| Cycle counting → Cycle count plan processing (scheduled) / Threshold counting | | COVERED-31 | 31 | |
| Cycle counting → Approve/reject count variances (batch) | | COVERED-31 | 31 | |
| Work → Delete work / Cancel expired work / Clean up work history | | COVERED-31 | 31 | |
| Loads → Ship confirm (batch) / Load posting (packing slip from load) | | COVERED-28 | 28 | |
| Loads → Outbound load planning (batch build by template) | | COVERED-31 | 31 | |
| Inbound → ASN import (EDI 856) batch | | COVERED-35 | 35 | |
| Mobile → Label reprint batch / Document routing cleanup | | COVERED-32 | 32 | |
| Inventory status → Mass status change | | COVERED-16 | 16 | |
| Slotting → Run slotting / Generate slotting replenishment | | COVERED-31 | 31 | |
| License plates → Clean up empty license plates | | COVERED-31 | 31 | |
| Work users → Expire sessions | | COVERED-32 | 32 | |
| Cross docking → Planned cross docking processing | | PARTIAL-31 (cross docking) | 31 | |
| WHS → Release to warehouse (batch, automatic release by rule) | | COVERED-28 | 28 | Order release rules |
| WHS → Warehouse workload refresh | | COVERED-31 | 31 | |
| WHS → Delete expired mobile device user sessions / Clean up WHS history (work line history retention) | | COVERED-31 | 31 | |

### 12.26.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Warehouse management parameters — General | Default location profiles, LP numbering, user/system-directed defaults, work audit, pack defaults, cycle count defaults, replenishment defaults, dock management, number sequences | COVERED-31 | 31 | |
| Warehouse management parameters — Mobile device / Print / Barcode | Mobile device settings, display, printers | COVERED-32 | 32 | |
| Warehouse (Warehouse details with "Use warehouse management processes", default receipt/shipping/production locations, license plate receiving) | | COVERED-31 | 31 | |
| Location setup → Location profiles (allow mixed items/batches/status, LP tracking, cycle counting, stocking limits, location format) | | COVERED-31 | 31 | |
| Location setup → Location formats (segments, separators) | | COVERED-31 | 31 | |
| Location setup → Location types (quarantine, shipping, receiving, staging, final shipping) | | COVERED-31 | 31 | |
| Location setup → Zones / Zone groups | | COVERED-31 | 31 | |
| Location setup → Locations (bins) with coordinates, sort codes | | COVERED-31 | 31 | |
| Location setup → Fixed locations (item fixed locations) | | COVERED-31 | 31 | |
| Location setup → Location stocking limits (by item/UoM/container type/physical dimensions) | | COVERED-31 | 31 | |
| Location setup → Location directives (work order type, direction, work type; lines with query, strategy (consolidate, FEFO batch, LP empty, round up, lowest qty…), location directive actions with query, fixed location usage, allow negative, restrict by UoM) | | COVERED-31 | 31 | Directive fixtures |
| Location setup → Location directive failures (D365 troubleshooting) | | COVERED-31 | 31 | |
| Location setup → Directive codes | | COVERED-31 | 31 | |
| Work → Work templates (work order type, work pools, work classes, work template lines: pick/put/print/inventory status change/custom; work header breaks; max pick quantity; directive codes) | | COVERED-31 | 31 | |
| Work → Work pools | | COVERED-31 | 31 | |
| Work → Work classes (valid location types per class) | | COVERED-31 | 31 | |
| Work → Work users (user id, default warehouse, menu, allowed work classes, process cycle counting, max LP) | | COVERED-32 | 32 | |
| Work → Work exceptions (short pick exceptions, pick overrides) | | COVERED-31 | 31 | Short picks |
| Work → Work priorities / Work blocking reasons (D365) | | COVERED-31 | 31 | |
| Work → Work policies (D365: work creation rules for production/transfer) | | COVERED-31 | 31 | |
| Work → Work cancel reasons (D365) | | COVERED-31 | 31 | |
| Waves → Wave templates (shipping, manufacturing, kanban; wave methods: allocate wave, create loads, create work, containerize, replenish, release to warehouse; automatic processing, wave thresholds, attributes) | | COVERED-31 | 31 | |
| Waves → Wave process methods (register custom methods) | | COVERED-31 | 31 | |
| Waves → Wave attributes / Wave step codes (D365) | | COVERED-31 | 31 | |
| Waves → Wave label templates (D365) | | COVERED-32 | 32 | |
| Load → Load templates (capacity: weight/volume/floor space, load template group) | | COVERED-31 | 31 | |
| Load → Load building templates / Load building strategies (D365: volume-based) | | COVERED-31 | 31 | |
| Load → Load posting methods (packing slip, sales invoice, transfer shipment) | | COVERED-28 | 28 | |
| Load → Load line type / Load status (customizable) | | COVERED-31 | 31 | |
| Mobile device → Mobile device menu items (work: pick/put, user-directed, system-directed, cluster picking, cycle counting, inquiry, adjustments, movement, LP build, print, production RAF, kanban, quality; indirect activities; options per item: generate LP, override, validation) | | COVERED-32 | 32 | Flow designer |
| Mobile device → Mobile device menus (hierarchy of menu items) | | COVERED-32 | 32 | |
| Mobile device → Mobile device display settings / Work user display / Mobile device steps configuration (D365 step configuration, field names) | | COVERED-32 | 32 | |
| Mobile device → Mobile device overrides / Detours (D365) | | COVERED-32 | 32 | |
| Mobile device → Mobile device barcode setup / Scan data parsing (GS1) (D365) | | COVERED-32 | 32 | GS1 AI parser |
| Mobile device → Work user sessions / Device management (D365 Warehouse app) | | COVERED-32 | 32 | |
| Mobile device → Cluster profiles (cluster picking) | | COVERED-32 | 32 | |
| Mobile device → Work confirmation / Validation (location, LP, item confirmation per menu item) | | COVERED-32 | 32 | |
| Mobile device → Indirect activity codes (warehouse) | | COVERED-32 | 32 | |
| Mobile device → Mobile device user authentication (D365 device token) | | COVERED-2 | 2 | Keycloak device auth |
| Inventory → Inventory statuses / Inventory status transitions | | COVERED-16 | 16 | |
| Inventory → Disposition codes (WHS: receiving disposition → inventory status + work template) | | COVERED-31 | 31 | |
| Inventory → Unit sequence groups (pallet/case/each with LP grouping and packing) | | COVERED-31 | 31 | |
| Inventory → Reservation hierarchies | | COVERED-31 | 31 | |
| Inventory → Filters (Filter codes, Filter groups, Product filter assignment) | | COVERED-31 | 31 | |
| Inventory → Physical dimensions (per item/UoM) | | COVERED-31 | 31 | |
| Inventory → Warehouse-specific item setup (fixed locations, min/max, replenishment) | | COVERED-31 | 31 | |
| Packing → Container types / Container groups / Container build templates (containerization during wave) / Container packing policies / Packing profiles / Packing station setup | | COVERED-28 | 28 | |
| Packing → Packing policies (D365 manual packing: packing slip/label on close) | | COVERED-28 | 28 | |
| Replenishment → Replenishment templates (min/max, demand, wave demand, load demand, slotting; replenishment units, work template) | | COVERED-31 | 31 | |
| Replenishment → Min/max replenishment rules per item/location | | COVERED-31 | 31 | |
| Replenishment → Fixed location replenishment / Immediate replenishment (D365) | | COVERED-31 | 31 | |
| Cycle counting → Cycle count plans (query, days between counts, work pool, max locations) | | COVERED-31 | 31 | |
| Cycle counting → Cycle count thresholds (quantity/percent threshold triggers) | | COVERED-31 | 31 | |
| Cycle counting → Cycle count approval parameters / Counting reason codes (link) | | COVERED-31 | 31 | |
| Cycle counting → Spot cycle counting / Supervisor approval | | COVERED-31 | 31 | |
| Label → Document routing (printer per work/user/warehouse), Document routing layouts (ZPL templates), Printers (print service) | | COVERED-32 | 32 | ZPL print service |
| Label → License plate label layouts / Container label layouts (D365 label layouts) | | COVERED-32 | 32 | |
| Label → Wave label printing setup (D365) | | COVERED-32 | 32 | |
| Dock scheduling → Appointment rules, Dock profiles, Dock management profiles, Appointment schedules, Appointment durations | | COVERED-31 | 31 | |
| Inbound → ASN setup (packing structure import, EDI 856 mapping) | | COVERED-35 | 35 | |
| Inbound → Inbound load statuses / Receiving policies (LP receiving, mixed LP receiving) | | COVERED-31 | 31 | |
| Outbound → Release to warehouse rules (automatic release, fulfillment rate %, partial release) | | COVERED-28 | 28 | |
| Outbound → Ship confirm setup / Bill of lading setup | | COVERED-28 | 28 | |
| Outbound → Shipping carrier / Mode of delivery mapping to WHS | | COVERED-28 | 28 | |
| Outbound → Cross docking templates (D365) | | PARTIAL-31 (cross docking) | 31 | |
| Outbound → Outbound sorting (D365 outbound sorting positions) | | PARTIAL-31 (outbound sorting (sort pick-to-shipment at a sortation area) not named) | 31 | Gap register |
| Slotting → Slotting templates / Slotting specifications (D365) | | COVERED-31 | 31 | |
| Warehouse → Production: Production input/output location setup, Raw material picking policies, RAF put-away policies, Material staging | | COVERED-31 | 31 | |
| Warehouse → Kanban: Kanban board for WHS, Supermarket replenishment | | COVERED-31 | 31 | |
| Warehouse → Catch weight on WHS (D365 CW tags) | | COVERED-24 | 24 | |
| Warehouse → Warehouse management workload visualization setup | | COVERED-31 | 31 | |
| Warehouse → Warehouse app task validator / Process guide steps (D365) | | COVERED-32 | 32 | |
| Warehouse → RFID / Voice adapter configuration (Passage) | | COVERED-32 | 32 | |
| Warehouse → Transportation (link to TMS) | | COVERED-34 | 34 | |
| Warehouse → Number sequences (work, wave, load, LP, container) | | COVERED-5 | 5 | |
| Warehouse → Work audit templates | | COVERED-31 | 31 | |
| Warehouse → Warehouse management (WHS) workflows (D365 none) | | COVERED-4 | 4 | |
| Warehouse → Yard management (D365 2024: yard, trailers, yard moves) | | GAP | — | Propose wave 34: yard management (trailer/yard locations, check-in/out, yard moves, dock door assignment) alongside TMS driver check-in |

## 12.27 Transportation management (TMS)

AX 2012 R3 TMS: rating engines, rate masters, carriers, route plans/guides, load building, tendering, freight bills and reconciliation, driver check-in, transportation tenders, dock appointment scheduling. Passage wave 34 covers rating, routing, load building, tendering, tracking, freight reconciliation and landed cost; yard/driver check-in is a gap.

### 12.27.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Loads (planning workbench, TMS view) | WHSLoadPlanningWorkbench (TMS) | Rate loads, assign carriers | COVERED-34 | 34 | |
| Rate route workbench | TMSRateRouteWorkbench | Rate shop across carriers/routes for a load | COVERED-34 | 34 | Rate shopping |
| Transportation tenders / Carrier tendering | TMSLoadTender | Tender to carrier, accept/reject, timeouts | COVERED-34 | 34 | Tendering workflow |
| Freight bills | TMSFreightBill | Expected freight bill per load leg with charges | COVERED-34 | 34 | |
| Freight bill invoices / Freight invoice matching | TMSFreightBillInvoice, TMSFreightBillMatching | Carrier invoice vs freight bill, reconciliation | COVERED-34 | 34 | Freight reconciliation |
| Driver check-in / check-out | TMSDriverCheckIn | Driver/trailer arrival at dock, appointment link | GAP | — | Propose wave 34: driver check-in/out with appointment link, trailer/seal capture, gate log (with yard management) |
| Routes (transportation routes: multi-leg, hubs) | TMSRoute | Route with segments and hubs | COVERED-34 | 34 | Route planning |
| Route plans / Route guides | TMSRoutePlan, TMSRouteGuide | Planned legs and automatic carrier selection rules | COVERED-34 | 34 | |
| Shipments (TMS view) / Shipment tracking | WHSShipmentTable (TMS) | Tracking events, POD | COVERED-34 | 34 | EDI 214/API |
| Load → Rate loads / Rate shipments | TMSRating | Rating per load/shipment | COVERED-34 | 34 | |
| Load → Hub consolidation / Load building (manual) | TMSHub | Consolidate at hubs | COVERED-34 | 34 | |
| Load → Freight charge allocation to orders (customer charges from freight) | TMSFreightChargeAllocation | Pass freight to sales orders as charges | COVERED-34 | 34 | |
| Load → Transportation status / Carrier invoice reasons | TMSTransportationStatus | | COVERED-34 | 34 | |
| Landed cost (D365 module; voyages, link) | | | PARTIAL-34 (landed cost voyages) | 34 | |
| Carrier → Carrier rate cards (Rate masters, Rate base) | TMSRateMaster | | COVERED-34 | 34 | |
| Dock appointment scheduling (link) | | | COVERED-31 | 31 | |
| Inbound → Inbound load rating / Transportation on purchase orders | | | COVERED-34 | 34 | |
| Transportation → Transportation management workspace (D365 outbound/inbound) | | | COVERED-34 | 34 | |

### 12.27.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Freight bill inquiries / Freight invoice reconciliation status | | COVERED-34 | 34 | |
| Tender history / Tender status inquiry | | COVERED-34 | 34 | |
| Load tracking inquiry (tracking events) | | COVERED-34 | 34 | |
| Rating results inquiry (rate shopping log) | | COVERED-34 | 34 | |
| Carrier performance (on-time, cost per mile) | | COVERED-46 | 46 | |
| Driver check-in log | | GAP | — | Propose wave 34: driver check-in |
| SSRS: Bill of lading (master/underlying) | | COVERED-28 | 28 | |
| SSRS: Load manifest / Load tender document | | COVERED-34 | 34 | |
| SSRS: Freight bill / Freight invoice reconciliation report | | COVERED-34 | 34 | |
| SSRS: Carrier rate listing / Rate master listing | | COVERED-34 | 34 | |
| SSRS: Route guide listing / Route plan listing | | COVERED-34 | 34 | |
| SSRS: Transportation tender summary | | COVERED-34 | 34 | |
| SSRS: Freight accrual report | | COVERED-34 | 34 | |
| SSRS: Dock schedule (link) | | COVERED-31 | 31 | |
| SSRS: Landed cost reports (link) | | PARTIAL-34 (landed cost voyages) | 34 | |

### 12.27.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Rate loads (batch rating) / Auto-rate on release | | COVERED-34 | 34 | |
| Tendering → Send tenders (batch) / Tender timeout processing / Cascade to next carrier | | COVERED-34 | 34 | |
| Freight reconciliation → Match freight invoices (auto) / Post freight accruals / Generate AP invoice for matched freight | | COVERED-34 | 34 | |
| Tracking → Import tracking events (EDI 214 / carrier API polling) | | COVERED-34 | 34 | |
| Load building → Automatic load building (strategy) | | COVERED-34 | 34 | |
| Route → Route guide evaluation (batch assign carriers) | | COVERED-34 | 34 | |
| Fuel index → Update fuel surcharges from index | | COVERED-34 | 34 | |
| Landed cost → Voyage cost actualization (link) | | PARTIAL-34 (landed cost voyages) | 34 | |
| Freight charges → Allocate freight to sales/purchase orders (batch) | | COVERED-34 | 34 | |
| Transportation → Clean up tenders / rating logs | | COVERED-34 | 34 | |
| Driver check-in → Expire appointments / Gate log archive | | GAP | — | Propose wave 34: driver check-in |

### 12.27.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Transportation management parameters — General | Default rating profile, tender settings, freight bill types, dock management, number sequences, appointment settings | COVERED-34 | 34 | |
| Carriers → Shipping carriers (carrier vendor, services, rating profile, tender method, tracking) | | COVERED-34 | 34 | |
| Carriers → Carrier services / Carrier groups / Carrier rating profiles | | COVERED-34 | 34 | |
| Carriers → Transportation methods / Transportation modes | | COVERED-34 | 34 | |
| Carriers → Carrier invoice reasons / Reconciliation reasons | | COVERED-34 | 34 | |
| Rating → Rating profiles (rating metadata: rate base, rate master, engine) | | COVERED-34 | 34 | |
| Rating → Rate masters / Rate base / Rate base type / Rate base assignments (effective-dated rate tables: per mile, per weight break, per zone) | | COVERED-34 | 34 | |
| Rating → Rating metadata (rate structure definitions) | | COVERED-34 | 34 | |
| Rating → Accessorial masters / Accessorial assignments / Accessorial charges (fuel surcharge, liftgate, residential) | | COVERED-34 | 34 | |
| Rating → Fuel indexes / Fuel surcharge tables | | COVERED-34 | 34 | |
| Rating → Zone masters / Zones / Zone engines (postal code zones) | | COVERED-34 | 34 | |
| Rating → Engines: Rating engines, Generic engines, Transit time engines, Mileage engines (PC*MILER/Google Maps adapter), Apportionment engines, Rate engines (custom) | | COVERED-34 | 34 | Adapter interface; OR-Tools for VRP |
| Rating → Transit time masters | | COVERED-34 | 34 | |
| Routing → Route plans / Route guides / Route schedules / Hubs (hub masters) / Transportation constraints (carrier/item/customer constraints) | | COVERED-34 | 34 | |
| Routing → Route segments / Legs | | COVERED-34 | 34 | |
| Load building → Load building strategies / Load templates (link) | | COVERED-34 | 34 | |
| Freight billing → Freight bill types / Freight bill type assignments (which charges belong to which party) / Audit masters (tolerances for matching) | | COVERED-34 | 34 | |
| Freight billing → Freight accrual posting (ledger accounts) / Freight charge allocation rules (to orders) | | COVERED-34 | 34 | |
| Tendering → Tender types / Tender timeout rules / Tender broadcast (D365) | | COVERED-34 | 34 | |
| Tracking → Transportation status master / Status codes (EDI 214 mapping) / Proof of delivery setup | | COVERED-34 | 34 | |
| Dock appointment → Appointment rules / Dock profiles / Appointment scheduling calendar | | COVERED-31 | 31 | |
| Driver check-in → Check-in setup / Gate configuration | | GAP | — | Propose wave 34: driver check-in |
| Landed cost → Landed cost parameters / Cost types / Voyage templates (link) | | PARTIAL-34 (landed cost voyages) | 34 | |
| Transportation → Container / Trailer types | | COVERED-34 | 34 | |
| Transportation → Transportation document types / Bill of lading setup | | COVERED-28 | 28 | |
| Transportation → Destination codes / Postal code masters | | COVERED-34 | 34 | |
| Transportation → Number sequences (load tender, freight bill) | | COVERED-5 | 5 | |
| Transportation → Workflow (freight invoice approval) | | COVERED-4 | 4 | |
| Transportation → Carrier EDI/API connections (204/990/214 and carrier APIs) | | COVERED-35 | 35 | |
| Transportation → Yard management setup (D365 2024) | | GAP | — | Propose wave 34: yard management |

## 12.28 Service management

Service agreements, service orders, dispatch board, service objects/tasks, repair, subscriptions and service BOM in AX 2012 R3 map to wave 45 (installed base, agreements/SLA, cases, work orders, dispatch, mobile). Service subscriptions billing shares wave 29.

### 12.28.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Service agreements (list page) | SMAAgreementTableListPage | Agreements with lines (service objects/tasks, intervals, prices) | COVERED-45 | 45 | Service agreements with entitlements/SLAs |
| Service agreement details | SMAAgreementTable / SMAAgreementLine | Lines, service level agreement, project link, templates | COVERED-45 | 45 | |
| Service orders (list page) | SMAServiceOrderTableListPage | Service orders from agreements/manual; lines (hour, item, expense, fee) | COVERED-45 | 45 | Service work orders |
| Service order details | SMAServiceOrderTable / SMAServiceOrderLine | Technician, service object, task, repair, stage, SLA timer, posting to project | COVERED-45 | 45 | |
| Dispatch board | SMADispatchBoard | Gantt of technicians × service orders, drag/drop | COVERED-45 | 45 | Dispatch optimization (OR-Tools) |
| Service objects (installed base) | SMAServiceObjectTable | Customer equipment, serial, template BOM | COVERED-45 | 45 | Installed base from shipped serials |
| Service object relations (object ↔ agreement/customer) | SMAServiceObjectRelation | | COVERED-45 | 45 | |
| Service tasks / Service task relations | SMAServiceTaskTable | | COVERED-45 | 45 | |
| Repair → Repair lines (symptom, diagnosis, resolution, repair stage) | SMARepairLine | Repair tracking | COVERED-45 | 45 | Repair orders |
| Service subscriptions (subscription fee transactions, index) | SMASubscriptionTable | Recurring subscription fees via project | PARTIAL-45 (AX service subscriptions (fee indexation, accrual via project) not explicitly named; subscription billing is wave 29) | 29 | Gap register |
| Service → Service stages / Stage transitions | SMAServiceStage | | COVERED-45 | 45 | |
| Service → Service level agreements (time recording, SLA compliance) | SMAServiceLevelAgreement | | COVERED-45 | 45 | SLA attainment |
| Service → Service order sign-off / Customer signature (mobile) | | | COVERED-45 | 45 | Mobile app signatures |
| Service → Cases (customer service cases, link to Org admin case management) | CaseDetail (service) | | COVERED-45 | 45 | Cases → service work orders |
| Service → Technician mobile (D365 Field Service mobile) | | | COVERED-45 | 45 | |
| Service → Warranty tracking (D365 warranty / AX via service object) | | | COVERED-45 | 45 | Warranty terms |
| Service → Service BOM (service object BOM / template BOM) | SMAServiceObjectBOM | | COVERED-45 | 45 | |
| Service → Parts consumption from technician van stock | | | COVERED-45 | 45 | Mobile inventory locations |
| Service → Service order project posting (hours/items/expenses to project) | | | COVERED-41 | 41 | |
| Service → Service order invoicing (via project invoice proposal) | | | COVERED-41 | 41 | Billing billable vs warranty |

### 12.28.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Service order inquiries (by stage, technician, customer, SLA status) | | COVERED-45 | 45 | |
| Service agreement inquiries (expiring agreements, lines due) | | COVERED-45 | 45 | |
| Service object inquiries (history per object) | | COVERED-45 | 45 | |
| Repair statistics (symptom/diagnosis/resolution frequency) | | COVERED-45 | 45 | |
| SLA compliance inquiry | | COVERED-45 | 45 | |
| Subscription inquiries | | PARTIAL-45 (service subscriptions) | 29 | |
| Service KPIs (first-time fix, SLA attainment, technician utilization) | | COVERED-45 | 45 | |
| SSRS: Service order (document) / Service order lines / Service order status | | COVERED-45 | 45 | |
| SSRS: Service agreement (document) / Agreement lines | | COVERED-45 | 45 | |
| SSRS: Service object list / Service object history | | COVERED-45 | 45 | |
| SSRS: Repair report / Repair statistics | | COVERED-45 | 45 | |
| SSRS: Dispatch list / Technician schedule | | COVERED-45 | 45 | |
| SSRS: SLA report | | COVERED-45 | 45 | |
| SSRS: Subscription report / Subscription fee transactions | | PARTIAL-45 (service subscriptions) | 29 | |
| SSRS: Service invoice (via project) | | COVERED-41 | 41 | |

### 12.28.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Create service orders from agreements (periodic generation by interval) | | COVERED-45 | 45 | |
| Service agreements → Copy / Renew / Expire agreements | | COVERED-45 | 45 | |
| Subscriptions → Create subscription fee transactions / Accrue subscription revenue / Index subscriptions | | PARTIAL-45 (service subscriptions) | 29 | |
| Service orders → Post service orders (to project) / Invoice (via project) | | COVERED-41 | 41 | |
| Service orders → Update stages (batch) / Close | | COVERED-45 | 45 | |
| Dispatch → Automatic dispatch optimization run | | COVERED-45 | 45 | |
| Service → SLA breach notifications | | COVERED-45 | 45 | |
| Service → Synchronize installed base from shipments (serials) | | COVERED-45 | 45 | |
| Service → Mobile sync / Offline reconciliation | | COVERED-45 | 45 | |

### 12.28.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Service management parameters — General / Service orders / Agreements / Subscriptions / Dispatching / Number sequences | Default stages, project posting, time recording, SLA settings, subscription defaults | COVERED-45 | 45 | Subscriptions PARTIAL |
| Service → Service stages / Stage reasons / Stage transitions | | COVERED-45 | 45 | |
| Service → Service order types / Service priorities | | COVERED-45 | 45 | |
| Service → Service level agreements (SLA definitions: time to respond/resolve, calendars) | | COVERED-45 | 45 | |
| Service → Service objects: Service object groups, Service object templates | | COVERED-45 | 45 | |
| Service → Service tasks / Service task groups | | COVERED-45 | 45 | |
| Service → Service agreement groups / Agreement templates / Service intervals | | COVERED-45 | 45 | |
| Service → Repair: Conditions, Symptom areas, Symptom codes, Diagnosis areas, Diagnosis codes, Resolutions, Repair stages | | COVERED-45 | 45 | |
| Service → Dispatching: Dispatch teams, Technicians (workers), Skills, Territories, Dispatch board settings | | COVERED-45 | 45 | |
| Service → Subscription: Subscription groups, Subscription fee categories, Index rules, Subscription periods | | PARTIAL-45 (service subscriptions) | 29 | |
| Service → Project integration (project group for service, categories, line properties) | | COVERED-41 | 41 | |
| Service → Warranty types / Entitlement types (Passage) | | COVERED-45 | 45 | |
| Service → Mobile app configuration (offline, photos, signatures) | | COVERED-45 | 45 | |
| Service → Van stock / Technician warehouses | | COVERED-45 | 45 | |
| Service → Workflow (service order approval) | | COVERED-4 | 4 | |
| Service → Number sequences | | COVERED-5 | 5 | |
| Service → Case categories for service (link) | | COVERED-45 | 45 | |
| Service → Customer portal: case submission / service request (link) | | COVERED-30 | 30 | |

## 12.29 Organization administration

Legal entities, operating units, hierarchies, address book, number sequences, document management, case management, workflow configuration, calendars, units, electronic signatures, foreign trade, policies and signing limits. Passage: waves 6 (structure, calendars, UoM, address book), 7 (global address book), 5 (number sequences, audit), 4 (workflow). Case management, document management depth, reason codes, teams and electronic signatures are partial or gaps.

### 12.29.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Legal entities (list) / Legal entity details | OMLegalEntity (CompanyInfo) | Company, addresses, tax registration, bank, statutory reporting, foreign trade, ER tabs | COVERED-6 | 6 | |
| Operating units (departments, cost centers, business units, value streams, retail channels) | OMOperatingUnit | | COVERED-6 | 6 | |
| Organization hierarchies / Hierarchy designer (with purposes, publish with effective date, view hierarchy) | OMHierarchyDesigner, OMHierarchyPurpose | Multi-purpose effective-dated hierarchies | COVERED-6 | 6 | |
| Global address book (list) / Party details | DirPartyTableListPage / DirPartyTable | Parties with roles, addresses, contact info, relationships | COVERED-7 | 7 | |
| Global address book → Duplicate check / Merge parties | DirDuplicateCheck, DirPartyMerge | | COVERED-7 | 7 | |
| Address books (address book groupings with team access) | DirAddressBook | | COVERED-7 | 7 | |
| Teams / Team types | OMTeam | | PARTIAL-6 (teams) | 6 | |
| Cases (list page) / Case details | CaseDetailListPage / CaseDetail | Case with category, process, activities, log, knowledge articles, associations, resolution | PARTIAL-45 (generic case management (categories, processes, activities, knowledge articles, associations to any record) is not named in any wave; service cases are in 45, disputes in 30, product change in 9) | 45 | Gap register |
| Cases → Knowledge articles | CaseKnowledgeArticle | | PARTIAL-45 (generic case management) | 45 | |
| Document management → Document handling (attachments on any record), Document types, File types | DocuView, DocuType | | PARTIAL-5 (attachments exist in 5/11; document types, storage locations (SharePoint/S3), archive directories, retention are not named) | 5 | Gap register |
| Document management → Document templates (Word/Excel mail merge) | DocuTemplate | | OUT-OF-SCOPE (CRM mail merge non-goal; business documents via wave 46) | — | |
| Number sequences (list) / Number sequence details / Number sequence wizard | NumberSequenceTableListPage, NumberSequenceTable, NumberSeqWizard | Scopes (shared, company, legal entity, operating unit, fiscal calendar period), continuous, pre-allocation, format segments | COVERED-5 | 5 | |
| Number sequences → References (per data type/scope) | NumberSequenceReference | | COVERED-5 | 5 | |
| Workflow → Workflow configurations (per type: PO, requisition, journal, etc.) / Workflow editor | WorkflowTable, Workflow editor (WF4) | Graphical designer, versions, activation | COVERED-4 | 4 | Workflow designer |
| Workflow → Workflow history / Work items | WorkflowStatus, WorkflowWorkItem | | COVERED-4 | 4 | |
| Calendars (Org admin: working time calendars shared with production/HR) | WorkCalendarTable | | COVERED-9 | 9 | |
| Units / Unit conversions / Unit classes / Unit translations | UnitOfMeasure, UnitOfMeasureConversion | | COVERED-6 | 6 | |
| Foreign trade → Intrastat (journal) / EU sales list (link) | Intrastat, TaxEUSalesList | | PARTIAL-13 (Intrastat setup) | 13 | Intrastat extract named; journal maintenance/corrections not |
| Electronic signatures → Signature log / Reset signature | SysSignatureLog | | GAP | — | Propose wave 48: electronic signatures |
| Policies → Signing limits (Signing limit policies, Worker signing limit requests, Default signing limits by job/compensation level) | SigningLimit* | | PARTIAL-17 (signing limits) | 17 | |
| Policies → Policy framework forms (Purchasing policies, Expense policies, Audit policies, Vendor invoice policies, Signing limit policies) | SysPolicy* | Policy framework with org hierarchy scoping | PARTIAL-17 (a generic policy framework scoped by org hierarchy is implied by 17/43 but not named as reusable) | 17 | Gap register |
| Product safety data sheets (link, PI) | | | GAP | — | Propose wave 33: product compliance |
| Reason codes (shared financial reasons across modules) | ReasonTable | Fixed assets, customer, vendor, bank, ledger reasons | PARTIAL-11 (reason codes) | 11 | |
| Address → Address setup: Address formats, Address purposes, Name sequences, Name affixes, ZIP/postal codes, Counties, States/provinces, Countries/regions, Districts | LogisticsAddress* | | COVERED-6 | 6 | Address book with validated postal addresses |
| Address → Address books parameters / Global address book parameters (security by address book, name sequence defaults, duplicate check on) | DirParameters | | COVERED-7 | 7 | |
| Global address book → Party relationship types / Role security | DirRelationshipType | | COVERED-7 | 7 | |
| Organization → Translations (legal entity/operating unit name translations) | | | COVERED-6 | 6 | |
| Organization → Legal entity logos / Document branding | CompanyImage | | COVERED-46 | 46 | Document designer branding |
| Electronic signatures → Certificates (user signing certificates) | SysSignatureCertificate | | GAP | — | Propose wave 48: electronic signatures |
| Print management (org-wide) | PrintMgmtDocType | | PARTIAL-46 (print management routing) | 46 | |
| Alerts (org-level alert rules) | EventRule | | GAP | — | Propose wave 5: alerts and notification rules (change-based and due-date alerts on any entity/field, delivered in-app/email/webhook) built on the wave 4 event backbone |
| Questionnaires (link, Org admin > Questionnaires in R3) | KMQuestionnaire | | GAP | — | Propose wave 42: questionnaire engine |
| Case management → Case processes / Case categories (link) | CaseProcess, CaseCategory | | PARTIAL-45 (generic case management) | 45 | |
| Organization → Vendor collaboration user provisioning (link) | | | COVERED-20 | 20 | |

### 12.29.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Organization hierarchy inquiry (as-of date view) | | COVERED-6 | 6 | |
| Number sequence inquiries (status list, pre-allocation, gaps) | | COVERED-5 | 5 | |
| Workflow inquiries (Workflow history, Workflow performance, Pending work items) | | COVERED-4 | 4 | |
| Case inquiries (cases by category/status, SLA) | | PARTIAL-45 (generic case management) | 45 | |
| Global address book inquiries (parties by role, duplicate candidates) | | COVERED-7 | 7 | |
| Document management inquiries (documents by type/record) | | PARTIAL-5 (document management) | 5 | |
| Electronic signature inquiry | | GAP | — | Propose wave 48: electronic signatures |
| SSRS: Legal entities list / Operating units list / Organization hierarchy report | | COVERED-6 | 6 | |
| SSRS: Number sequence list / Number sequence references | | COVERED-5 | 5 | |
| SSRS: Workflow configurations / Workflow history / Workflow tracking | | COVERED-4 | 4 | |
| SSRS: Address book (parties, contacts, addresses) / Mailing labels | | COVERED-7 | 7 | Labels OOS (CRM) |
| SSRS: Case report / Case activities | | PARTIAL-45 (generic case management) | 45 | |
| SSRS: Units / Unit conversions list | | COVERED-6 | 6 | |
| SSRS: Calendars report | | COVERED-9 | 9 | |
| SSRS: Intrastat report / EU sales list (link) | | COVERED-13 | 13 | |
| SSRS: Signing limits report | | PARTIAL-17 (signing limits) | 17 | |
| SSRS: Policies report (policy rules per hierarchy) | | PARTIAL-17 (policy framework) | 17 | |

### 12.29.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Organization hierarchy → Publish hierarchy version (effective date) | | COVERED-6 | 6 | |
| Number sequences → Clean up pre-allocated / Status list cleanup / Generate pre-allocation | | COVERED-5 | 5 | |
| Workflow → Workflow message processing / Workflow due date processing / Workflow cleanup (batch jobs) | | COVERED-4 | 4 | Temporal timers |
| Workflow → Line-item workflow batch activation | | COVERED-4 | 4 | |
| Global address book → Run duplicate detection (batch) / Party merge batch | | COVERED-7 | 7 | |
| Global address book → Address validation / Geocoding refresh | | COVERED-6 | 6 | |
| Foreign trade → Intrastat transfer (from invoices) / Intrastat validation / Intrastat export (file) | | PARTIAL-13 (Intrastat setup) | 13 | |
| Foreign trade → EU sales list transfer / Export | | COVERED-13 | 13 | |
| Cases → Close cases / Case SLA breach notifications | | PARTIAL-45 (generic case management) | 45 | |
| Document management → Archive documents / Purge by retention | | PARTIAL-5 (document management) | 5 | |
| Electronic signatures → Signature verification / Reset | | GAP | — | Propose wave 48: electronic signatures |
| Calendars → Compose working times / Update calendar expiry | | COVERED-9 | 9 | |
| Signing limits → Signing limit request processing (annual recertification) | | PARTIAL-17 (signing limits) | 17 | |
| Translations → Import/export system translations | | COVERED-48 | 48 | Localization framework |
| Organization → Delete legal entity / Legal entity copy (template) | | COVERED-6 | 6 | Tenant provisioning automation in 50 |

### 12.29.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Organization → Legal entities | Name, addresses, registration numbers, tax, bank, statutory, foreign trade, ER | COVERED-6 | 6 | |
| Organization → Operating units / Departments / Cost centers / Business units / Value streams | | COVERED-6 | 6 | |
| Organization → Organization hierarchies / Organization hierarchy purposes (security, procurement, expenditure, budget planning, benefits, centralized payments, signing limits, audit, retail, etc.) | | COVERED-6 | 6 | |
| Organization → Teams / Team types | | PARTIAL-6 (teams) | 6 | |
| Global address book → Global address book parameters / Address books / Address parameters (formats, purposes, name sequences, ZIP, counties, states, countries, districts, address setup) | | COVERED-7 | 7 | |
| Global address book → Party relationship types / Name affixes / Contact information types / Electronic address purposes | | COVERED-7 | 7 | |
| Global address book → Address validation (external verification service connector) | | COVERED-6 | 6 | Geocoding hook |
| Number sequences → Number sequences / References / Number sequence parameters (continuous, cleanup, pre-allocation) / Segment configuration (scopes) | | COVERED-5 | 5 | |
| Document management → Document management parameters (archive directory, SharePoint, file types, max size), Document types (file, note, URL, image, template; group; location: archive/database/SharePoint), Active document tables, File types | | PARTIAL-5 (document management) | 5 | |
| Document management → Document management: OneDrive/SharePoint integration (D365), Attachment migration | | PARTIAL-5 (document management) | 5 | Object storage (S3-compatible) is the Passage equivalent |
| Case management → Case categories / Case category types (HR, customer service, collections, product change, audit, service, project, knowledge, etc.) / Case processes (stages) / Case category security | | PARTIAL-45 (generic case management) | 45 | |
| Workflow → Workflow parameters (notifications, email, Enterprise Portal links, due date escalation) / Workflow version management | | COVERED-4 | 4 | |
| Workflow → Workflow types (registered workflow types per module) / Workflow infrastructure configuration (message/due date/line-item batch) | | COVERED-4 | 4 | Temporal |
| Workflow → Workflow delegation (user options) / Workflow participants (hierarchy, user group, expenditure participants, queue) | | COVERED-4 | 4 | |
| Calendars → Calendars / Working time templates | | COVERED-9 | 9 | |
| Units → Units / Unit classes / Unit conversions / Unit translations / Unit of measure parameters (decimal precision) | | COVERED-6 | 6 | UCUM |
| Electronic signatures → Electronic signature parameters / Signature requirements / Reason codes / Certificates | | GAP | — | Propose wave 48: electronic signatures |
| Foreign trade → Foreign trade parameters, Commodity codes, Transaction codes, Transport methods, Ports, Statistics procedures, Country/region properties (EU), Intrastat compression, Intrastat check setup, EU sales list reporting | | PARTIAL-13 (Intrastat setup) | 13 | |
| Policies → Signing limit policies / Signing limit parameters / Default signing limits (per job/compensation) / Signing limit request workflow | | PARTIAL-17 (signing limits) | 17 | |
| Policies → Policy parameters (per policy type: organization hierarchy purpose) | | PARTIAL-17 (policy framework) | 17 | |
| Reason codes (Financial reasons: ledger, bank, customer, vendor, fixed asset) | | PARTIAL-11 (reason codes) | 11 | |
| Product safety data sheets setup (link, PI) | | GAP | — | Propose wave 33: product compliance |
| Print management → Print management setup (per module/document: original/copy destinations, conditional settings by query) | | PARTIAL-46 (print management routing) | 46 | |
| Alerts → Alert rules (Change-based, Due-date; event rule setup, alert email templates) | | GAP | — | Propose wave 5: alerts and notification rules |
| Questionnaires setup (link) | | GAP | — | Propose wave 42: questionnaire engine |
| Organization → Fiscal calendars (shared) | | COVERED-6 | 6 | |
| Organization → Vendor collaboration / Customer self-service user provisioning (link) | | COVERED-20 | 20 | |
| Organization → Translations (system labels, custom text translations) | | COVERED-48 | 48 | |
| Organization → Shared categories (category hierarchies, link) | | COVERED-8 | 8 | |
| Organization → Record templates (per table) | | COVERED-5 | 5 | |
| Organization → Document branding / Report headers per legal entity | | COVERED-46 | 46 | |
| Organization → Data partitions (link to Sys admin) | | SUPERSEDED (tenant model wave 2) | 2 | |
| Organization → Audit policies (link) | | PARTIAL-48 (audit policies) | 48 | |
| Organization → Workflow approval email templates | | PARTIAL-4 (email templates / SMTP outbound not explicitly named in 4 or 5) | 4 | Gap register |

## 12.30 System administration

Users, security roles/duties/privileges, SoD, licensing, batch jobs, AOS/cluster, AIF services, database log, data partitions, virtual companies, Enterprise Portal, reporting/analysis services, help, alerts/email, model store. Passage supersedes most of this with waves 1 (pipeline), 2 (identity/authz), 3 (metadata), 4 (batch/workflow/events), 5 (audit/observability) and 35 (integration hub).

### 12.30.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Users (list) / User details (relations, roles, user options, profiles) | SysUserInfoPage / UserInfo | User accounts, domain/claims, roles per organization | COVERED-2 | 2 | Keycloak + SCIM; admin UI |
| User groups | UserGroupInfo | Groups for period access/journal approval | COVERED-2 | 2 | |
| User relations (worker ↔ user) | DirPersonUser | | COVERED-7 | 7 | Employee role ↔ identity |
| Security → Security roles / Duties / Privileges / Entry points (Security configuration, Security development tool) | SecurityRole, SecurityDuty, SecurityPrivilege | Role-based security model | COVERED-2 | 2 | Compiled to OPA |
| Security → Assign users to roles (rules: automatic role assignment by query) | SysSecRoleAssignmentRule | | COVERED-2 | 2 | |
| Security → Segregation of duties rules / Verify compliance / Conflicts | SysSecSegregationOfDuties* | | COVERED-48 | 48 | |
| Security → Extensible data security policies (XDS) | SysDataSecurityPolicy | Row-level security via policy queries | SUPERSEDED (Postgres RLS + OPA data policies wave 2) | 2 | |
| Security → Table permissions framework / Record-level security (legacy) | SysRecordLevelSecurity | | SUPERSEDED (RLS/OPA) | 2 | |
| Security → Effective permissions viewer (Passage) | | | COVERED-2 | 2 | |
| Batch jobs (list) / Batch job details / Batch tasks | BatchJob, BatchTask | Jobs, recurrence, dependencies, alerts, history | COVERED-4 | 4 | Batch job framework + monitor UI |
| Batch groups / Server configuration (batch servers, threads) | BatchGroup, SysServerConfig | | SUPERSEDED (Kubernetes workers, Temporal task queues) | 4 | |
| AOS → Cluster configuration / Load balancing | SysClusterConfig | | SUPERSEDED (Kubernetes + Envoy, wave 1) | 1 | |
| Online users | SysUsersOnline | Sessions, kill session | COVERED-2 | 2 | Keycloak sessions |
| Database → Database information / SQL administration (indexes, synchronize) / Database log setup | SysSqlAdmin, SysDatabaseLogSetup | | SUPERSEDED (Atlas migrations from metadata, wave 3; audit from CDC, wave 5) | 3 | |
| Data partitions | SysDataPartition | Partition isolation | SUPERSEDED (tenant model with RLS wave 2) | 2 | |
| Virtual company accounts / Table collections | VirtualDataAreaList | Share tables across companies | SUPERSEDED (tenant-level shared master data (party, product, COA) by design, waves 7/8/11) | 7 | |
| Services and AIF → Inbound/Outbound ports, Adapters (file, MSMQ, HTTP, NetTcp), Document data policies, Exceptions, Queue manager, History, Document services | AifInboundPort, AifOutboundPort, AifDocumentLog | Integration framework | SUPERSEDED (integration hub: REST/GraphQL/webhooks/Kafka/EDI, wave 35; message monitor with replay) | 35 | |
| Licensing → License information / License configuration (configuration keys) | SysLicenseCodeReadFile, SysConfig | Feature keys | SUPERSEDED (feature flags/tenant entitlements in metadata registry wave 3; licensing model decided before wave 35) | 3 | |
| Setup checklists (Initialization checklist, Partition checklist, Upgrade checklist) | SysCheckList* | | SUPERSEDED (tenant provisioning automation wave 50; migration runbooks wave 49) | 50 | |
| Enterprise Portal → Sites / EP parameters / Web parts | EP* | | SUPERSEDED (React shell + portals, waves 5/20/30/42) | 5 | |
| Reporting Services / Analysis Services → Report servers, Deploy reports, Cubes (SQL Server Analysis Services projects), Configure analysis extensions | SRSServers, BIAnalysisServer | | SUPERSEDED (document designer and semantic layer/lakehouse, wave 46) | 46 | |
| Help system (Help server, Help content) | | | SUPERSEDED (documentation site, in-app help wave 50) | 50 | |
| Alerts → Alert parameters / Email templates / Email parameters (SMTP) / Outbound email status (E-mail sending status) | EventParameters, SysEmailTable, SysEmailParameters | | PARTIAL-4 (email templates/SMTP outbound) | 4 | Alerts GAP wave 5 |
| Email → Email distributor batch | SysEmailBatch | | PARTIAL-4 (email templates/SMTP outbound) | 4 | |
| Document handling (link) | | | PARTIAL-5 (document management) | 5 | |
| Global search configuration / Search configuration (Enterprise search, full-text) | SysSearchConfiguration | | COVERED-5 | 5 | Global search in shell |
| Client performance options / Usage data / Personalization reset | SysUserSetup | | COVERED-5 | 5 | Personalization |
| System parameters (System parameters form: language, timezone defaults, security, alerts, help, Unicode) | SystemParameters | | COVERED-2 | 2 | Tenant settings |
| Model store / Layers / Models (AOT) / Code upgrade / Compile / CIL | SysModelStore | | SUPERSEDED (metadata registry + GitOps, waves 1/3; no X++ per PRD non-goal) | 3 | |
| Trace parser / SQL statement trace log / Tracing cockpit | SysTraceParser | | SUPERSEDED (OpenTelemetry traces/metrics/logs wave 5) | 5 | |
| Sys admin → Periodic → Data import/export (link to DIXF) | | | COVERED-10 | 10 | |
| Sys admin → Office add-ins (Excel add-in configuration, Document data sources) | DocuDataSource | | COVERED-5 | 5 | Excel/CSV import-export |
| Sys admin → Workflow infrastructure configuration (link) | | | COVERED-4 | 4 | |
| Sys admin → Business events catalog (D365) | BusinessEventsCatalog | Event subscriptions to endpoints | COVERED-4 | 4 | Kafka/webhooks |
| Sys admin → Feature management (D365) | FeatureManagement | Feature flags | COVERED-3 | 3 | |
| Sys admin → Data entities / OData (D365 link) | | | COVERED-3 | 3 | Generated OpenAPI |
| Sys admin → Dual-write (D365, link) | | | SUPERSEDED (wave 35) | 35 | §12.40 |
| Sys admin → LCS environment monitoring / Telemetry (D365) | | | SUPERSEDED (Grafana/Tempo/Loki, wave 5) | 5 | |
| Sys admin → Mobile workspaces (D365 mobile app designer) | | | SUPERSEDED (PWA shell, metadata-driven forms) | 5 | |
| Sys admin → Inquiries → Database log / System log / Batch job history / SQL statements | | | COVERED-5 | 5 | |
| Sys admin → Users → User log (login history) | SysUserLog | | COVERED-2 | 2 | Keycloak events |
| Sys admin → Security diagnostics for task recordings (D365) | | | SUPERSEDED (effective-permission viewer wave 2) | 2 | |
| Sys admin → Task recorder / Business process modeler (LCS) | SysTaskRecorder | | SUPERSEDED (Playwright E2E suites; training curriculum wave 50) | 50 | |
| Sys admin → Electronic signature (link) | | | GAP | — | Propose wave 48: electronic signatures |
| Sys admin → Printers / Document routing agent (D365 network printers) | | | COVERED-32 | 32 | Print service |
| Sys admin → Personalization management (D365: import/export user personalizations, saved views publish) | | | COVERED-5 | 5 | Saved views |
| Sys admin → Data archiving / Long-term retention (D365 archive) | | | COVERED-46 | 46 | Lakehouse archive |
| Sys admin → Secrets (key vault parameters, D365) | | | COVERED-36 | 36 | Vault; secrets rotation 48 |
| Sys admin → Tenant provisioning / Environment management (Passage) | | | COVERED-50 | 50 | |

### 12.30.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Online users / User sessions | | COVERED-2 | 2 | |
| Batch job history / Batch job errors | | COVERED-4 | 4 | |
| Database log inquiry | | COVERED-5 | 5 | |
| System log / Event log | | COVERED-5 | 5 | Loki |
| SQL statement trace / Long-running queries | | SUPERSEDED (OpenTelemetry/Grafana) | 5 | |
| Security inquiries (Users by role, Role permissions, Security roles report, Duties by role, Privileges by duty, Users with SoD conflicts, Effective permissions) | | COVERED-2 | 2 | |
| AIF inquiries (Document history, Exceptions, Queue manager) | | SUPERSEDED (message monitor wave 35) | 35 | |
| License inquiries (Named user license counts, License usage by role) | | PARTIAL-3 (license usage reporting is not named; depends on licensing decision) | 3 | Gap register |
| Workflow infrastructure inquiries | | COVERED-4 | 4 | |
| User log / Login history | | COVERED-2 | 2 | |
| SSRS: Security roles / Users / User permissions (Security role report) | | COVERED-2 | 2 | |
| SSRS: Database log report | | COVERED-5 | 5 | |
| SSRS: Batch job report | | COVERED-4 | 4 | |
| SSRS: License report / Named user counts | | PARTIAL-3 (license usage reporting) | 3 | |
| SSRS: Number sequence status (link) | | COVERED-5 | 5 | |
| SSRS: Table information / AOT object listings | | SUPERSEDED (metadata registry diff/export wave 3) | 3 | |
| Observability dashboards (SLOs, traces) (Passage) | | COVERED-5 | 5 | |

### 12.30.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Batch → Batch job scheduling / Recurrence / Batch alerts / Clean up batch history | | COVERED-4 | 4 | |
| Database log → Clean up database log | | SUPERSEDED (retention policy) | 48 | |
| Database → Synchronize database / Rebuild indexes / Compile / Generate CIL / Deploy | | SUPERSEDED (CI/CD pipeline wave 1; Atlas migrations wave 3) | 1 | |
| Database → Backup / Restore / Refresh from production (LCS) | | COVERED-48 | 48 | Backup/restore drills; DR in 50 |
| AIF → Process inbound/outbound queues / Send e-mails / Clean up document log | | SUPERSEDED (integration hub wave 35) | 35 | |
| Email → Send email distributor batch / Retry failed | | PARTIAL-4 (email templates/SMTP outbound) | 4 | |
| Alerts → Change-based alerts / Due date alerts (batch processors) | | GAP | — | Propose wave 5: alerts and notification rules |
| Notifications → Clean up notifications | | COVERED-5 | 5 | |
| Security → Verify SoD compliance (batch) / Automatic role assignment batch | | COVERED-48 | 48 | |
| Security → User provisioning sync (SCIM/AD import wizard) | | COVERED-2 | 2 | |
| Licensing → Named user license count refresh | | PARTIAL-3 (license usage reporting) | 3 | |
| Data → Data import/export (DIXF batch, link) | | COVERED-10 | 10 | |
| Data → Delete company data / Copy company (SysDataExport/Import) | | COVERED-10 | 10 | Migration framework; tenant provisioning 50 |
| Data → Data partition management (create/initialize) | | SUPERSEDED (tenant provisioning) | 50 | |
| Enterprise Portal → Deploy EP / Refresh AOT elements | | SUPERSEDED (shell deploys via GitOps) | 1 | |
| Reporting → Deploy reports / Process cubes (SSAS) | | SUPERSEDED (wave 46 lakehouse/dbt) | 46 | |
| Help → Update help content | | SUPERSEDED (docs site wave 50) | 50 | |
| Upgrade → Code upgrade / Data upgrade (AX 2009 → 2012, 2012 → D365) | | SUPERSEDED (migration factory wave 49, data only per non-goal) | 49 | |
| Model store → Import/export model / Install hotfix (axutil) | | SUPERSEDED (GitOps, signed images, wave 1) | 1 | |
| Monitoring → Performance counters / Trace capture | | SUPERSEDED (OpenTelemetry wave 5) | 5 | |
| Personalization → Reset usage data (batch) | | COVERED-5 | 5 | |
| Business events → Process business events queue / Retry failed endpoints (D365) | | COVERED-4 | 4 | Outbox + Kafka |
| Data archiving → Archive/purge jobs (D365) | | COVERED-46 | 46 | |
| Secrets → Rotate secrets (Passage) | | COVERED-48 | 48 | |
| DR → Game days / Failover drills (Passage) | | COVERED-50 | 50 | |

### 12.30.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| System parameters | Default language, timezone, alerts, help, Unicode, security defaults | COVERED-2 | 2 | |
| Users → User options defaults (language, timezone, number formats, workflow notifications, delegation) | | COVERED-5 | 5 | My user options |
| Users → Active Directory import wizard / Claims users / Identity providers | | COVERED-2 | 2 | OIDC/SAML federation, SCIM |
| Users → User profiles (role center profiles) | | SUPERSEDED (workspaces per role, wave 5) | 5 | |
| Security → Security roles / Duties / Privileges / Process cycles (security configuration) | | COVERED-2 | 2 | |
| Security → Segregation of duties rules | | COVERED-48 | 48 | |
| Security → Automatic role assignment rules (query-based) | | COVERED-2 | 2 | |
| Security → Extensible data security (XDS) policies / Policy contexts | | SUPERSEDED (RLS/OPA data policies wave 2) | 2 | |
| Security → Security development tool / Entry point permissions | | SUPERSEDED (policy compiler from metadata wave 2/3) | 2 | |
| Security → Password/MFA policies (Passage via Keycloak) | | COVERED-2 | 2 | |
| Security → API keys / Service accounts / Rate limits (Passage) | | COVERED-35 | 35 | Developer portal keys and quotas |
| Licensing → License information / Configuration keys / Named user licensing types | | SUPERSEDED (feature flags/entitlements wave 3; licensing model open question) | 3 | License usage PARTIAL |
| Batch → Batch groups / Batch servers / Batch thread configuration / Batch job alerts | | SUPERSEDED (Temporal task queues, K8s worker pools; monitor UI wave 4) | 4 | |
| AOS → Server configuration / Cluster configuration / Load balancer / Services (AIF on AOS) | | SUPERSEDED (Kubernetes, Envoy, wave 1) | 1 | |
| Database → Database log setup / Database information / SQL administration | | SUPERSEDED (metadata audit flags wave 3/5) | 5 | |
| Data partitions / Partition checklist | | SUPERSEDED (tenant model wave 2) | 2 | |
| Virtual company accounts / Table collections | | SUPERSEDED (shared tenant-level master data) | 7 | |
| Services and AIF → Inbound ports / Outbound ports / Adapters / Pipelines (XSLT transforms) / Document data policies / Value mapping / Services configuration | | SUPERSEDED (integration hub wave 35: REST/GraphQL/webhooks/Kafka; EDI mapping engine) | 35 | |
| Enterprise Portal → Web sites / EP parameters / Web menus / Web parts / Role centers | | SUPERSEDED (React shell + portals) | 5 | |
| Reporting Services → Report servers / Reporting parameters / Report deployment / Print destinations (file, email, printer) | | SUPERSEDED (document designer wave 46; print service wave 32) | 46 | |
| Analysis Services → Analysis servers / Cubes / Perspectives / Analysis extensions | | SUPERSEDED (semantic layer wave 46) | 46 | |
| Alerts → Alert parameters (polling intervals, retention, drill-down links) / Email templates / Email parameters (SMTP, Exchange, mail sending mode: batch/client) / Email distributor | | PARTIAL-4 (email templates/SMTP outbound) | 4 | Alerts GAP wave 5 |
| Help → Help system parameters / Help server / Help content languages | | SUPERSEDED (docs site wave 50) | 50 | |
| Global search / Enterprise search parameters / Search configuration | | COVERED-5 | 5 | |
| Client performance → Client performance options / Usage data policies | | COVERED-5 | 5 | |
| Document management (link) | | PARTIAL-5 (document management) | 5 | |
| Office add-ins → Document data sources (Excel add-in entities), Office integration parameters (D365) | | COVERED-5 | 5 | |
| Workflow infrastructure configuration (message processing, due date, line-item batch jobs) | | COVERED-4 | 4 | |
| Model store → Models / Layers / Configuration (Development: AOT, X++ compile, hotfixes) | | SUPERSEDED (no X++; metadata + GitOps) | 3 | PRD non-goal |
| Setup checklists → Initialization checklist (steps: license, config keys, partitions, company, users) | | SUPERSEDED (tenant provisioning automation wave 50) | 50 | |
| System → Timezones / Languages / Date-time settings | | COVERED-6 | 6 | Reference data |
| Number sequences (system-level) | | COVERED-5 | 5 | |
| Sys admin → Feature management configuration (D365) | | COVERED-3 | 3 | |
| Sys admin → Business events configuration (endpoints: Azure Service Bus, Event Grid, HTTPS, Power Automate) (D365) | | COVERED-4 | 4 | Webhooks/Kafka; replay wave 35 |
| Sys admin → Data entities / OData / Entity store refresh (D365) | | COVERED-3 | 3 | Lakehouse 46 |
| Sys admin → Integration: Recurring integrations / Data management framework scheduling (D365) | | COVERED-35 | 35 | |
| Sys admin → Electronic signature parameters (link) | | GAP | — | Propose wave 48: electronic signatures |
| Sys admin → Personalization management / Saved views publish (D365) | | COVERED-5 | 5 | |
| Sys admin → Network printers / Document routing agent (D365) | | COVERED-32 | 32 | |
| Sys admin → Key vault parameters / Certificates (D365) | | COVERED-36 | 36 | Vault |
| Sys admin → Data residency / Backup policies / Retention (Passage) | | COVERED-48 | 48 | |
| Sys admin → Tenant provisioning templates / Environment catalog (Passage) | | COVERED-50 | 50 | |
| Sys admin → Observability: SLOs, alert routes, PagerDuty integration (Passage) | | COVERED-5 | 5 | |
| Sys admin → AI: model provider configuration, token budgets, kill switch (Passage) | | COVERED-47 | 47 | |

## 12.31 Home / role centers / workflow

AX 2012 R3 Home module: role centers (Enterprise Portal pages with cues, KPIs, work lists), work items (workflow), activities, cases, questionnaires, alerts and user options. Passage: workspaces and personalization in wave 5, workflow in wave 4, dashboards/KPIs in wave 46, copilot in wave 47.

### 12.31.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Role center (per profile: Accountant, Buyer, Production manager, Sales manager, CFO, … 30+ role centers) | EP role center pages (SysUserProfile) | Cues, KPIs (SSAS), work list, quick links, reports, Power BI parts | SUPERSEDED (role-based workspaces with tiles/lists wave 5 and embedded dashboards/KPIs wave 46) | 5 | |
| Work items assigned to me / Work items queue / Work item queue assignments | WorkflowWorkItemListPage, WorkflowWorkItemQueue | Approvals and tasks | COVERED-4 | 4 | Human tasks on Temporal |
| Work item → Approve/Reject/Delegate/Request change/Complete | WorkflowWorkItemActionDialog | | COVERED-4 | 4 | |
| My activities (Activities: tasks, appointments, events, actions) | smmActivities (Home) | Personal activity list | OUT-OF-SCOPE (CRM activity model non-goal; tasks for cases/projects handled by case framework and workflow) | — | |
| Cases (My cases) | CaseDetailListPage | | PARTIAL-45 (generic case management) | 45 | |
| Questionnaires → My questionnaires / Answer questionnaire / Questionnaire results | KMQuestionnaire (Home) | | GAP | — | Propose wave 42: questionnaire engine |
| Alerts (My alerts / Notification list) | EventInbox | Alert inbox | GAP | — | Propose wave 5: alerts and notification rules (notification center exists in shell; rule-based alerts do not) |
| Notification center (Passage shell) | | In-app notifications from workflow/batch | COVERED-5 | 5 | |
| Cues (saved queries with counts) | SysCue | | COVERED-5 | 5 | Saved views with counts in workspaces |
| My user options (General, Status bar, Preferences: language/timezone/number format/date format, Workflow: notifications/delegation, Usage data, Account) | SysUserSetup (UserOptions) | | COVERED-5 | 5 | |
| Workflow delegation (My delegations: all/by workflow/by module, date ranges) | WorkflowDelegation | | COVERED-4 | 4 | |
| Timesheets (Home → Timesheets, link) | TSTimesheetTable | | PARTIAL-41 (timesheets) | 41 | |
| Expense reports (Home → Expense reports, link) | TrvExpTable | | COVERED-43 | 43 | |
| Purchase requisitions (Home → My requisitions, link) | PurchReqTable | | COVERED-17 | 17 | |
| Employee self-service pages (My information, My team) | EP ESS | | COVERED-42 | 42 | |
| Reports (Home → Common reports / My reports) | | | COVERED-46 | 46 | |
| Global search / Record search | SysSearch | | COVERED-5 | 5 | |
| Favorites / Navigation personalization | SysFavorites | | COVERED-5 | 5 | Personalization |
| Document handling on any record | DocuView | | PARTIAL-5 (document management) | 5 | |
| Copilot / Ask-the-data (Passage) | | | COVERED-47 | 47 | |
| Job monitor / My batch jobs (Passage) | | | COVERED-4 | 4 | |

### 12.31.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Workflow history (per document) / Workflow tracking details | | COVERED-4 | 4 | |
| Work item inquiries (Work items by user/queue/status, overdue work items) | | COVERED-4 | 4 | |
| Delegation inquiry | | COVERED-4 | 4 | |
| Role center KPIs (SSAS cube KPIs per role) | | COVERED-46 | 46 | Governed metrics |
| Cue counts inquiry | | COVERED-5 | 5 | |
| Alert inquiry (alerts by rule/user) | | GAP | — | Propose wave 5: alerts and notification rules |
| Activity inquiries | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Questionnaire statistics / Results analysis | | GAP | — | Propose wave 42: questionnaire engine |
| SSRS: Workflow history report / Workflow performance | | COVERED-4 | 4 | |
| SSRS: Work items report | | COVERED-4 | 4 | |
| SSRS: My reports (user-specific report list) | | COVERED-46 | 46 | |
| SSRS: Questionnaire reports (link) | | GAP | — | Propose wave 42: questionnaire engine |

### 12.31.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Workflow → Due date escalation / Reminders (batch) | | COVERED-4 | 4 | SLA timers |
| Workflow → Work item queue reassignment (batch) | | COVERED-4 | 4 | |
| Alerts → Process change-based alerts / Due-date alerts | | GAP | — | Propose wave 5: alerts and notification rules |
| Role center → Refresh cues / Refresh KPIs (cube processing) | | COVERED-46 | 46 | |
| Questionnaires → Schedule questionnaires (batch) | | GAP | — | Propose wave 42: questionnaire engine |
| Notifications → Clean up / Expire notifications | | COVERED-5 | 5 | |
| Personalization → Publish saved views to roles (D365) | | COVERED-5 | 5 | |
| Copilot → Agent runs (suggest mode) (Passage) | | COVERED-47 | 47 | |

### 12.31.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Role center setup → User profiles / Profile defaults / Role center page assignment (EP) | | SUPERSEDED (workspaces per role, wave 5) | 5 | |
| Role center → Cues setup (create cue from filter, cue groups) / KPI setup (KPI parts, cube perspectives) | | COVERED-5 | 5 | KPIs 46 |
| Workflow → Workflow configurations (link) / Workflow parameters / Workflow participants / Delegation settings | | COVERED-4 | 4 | |
| Workflow → Work item queues / Queue groups / Queue assignment rules | | COVERED-4 | 4 | |
| Workflow → Notification settings (email, in-app, digest) / Workflow email templates | | PARTIAL-4 (email templates/SMTP outbound) | 4 | |
| Alerts → Alert rules (create alert rule on field/record: changes to, due date; subject/message; recipients; organization-wide) / Alert rule templates | | GAP | — | Propose wave 5: alerts and notification rules |
| My user options → Language, Timezone, Number/date formats, Default company, Status bar, Workflow notifications, Delegation, Email, Usage data, Accessibility (D365 enhanced tab sequence) | | COVERED-5 | 5 | WCAG 2.1 AA |
| Questionnaires → Questionnaire setup (link) | | GAP | — | Propose wave 42: questionnaire engine |
| Activities → Activity setup (link, CRM) | | OUT-OF-SCOPE (CRM non-goal) | — | |
| Cases → Case setup (link) | | PARTIAL-45 (generic case management) | 45 | |
| Favorites / Navigation → Navigation pane customization, Personalization (form personalization, saved views, workspace tiles) | | COVERED-5 | 5 | |
| Global search → Search configuration (searchable tables/fields) | | COVERED-5 | 5 | |
| Home → Keyboard shortcuts / Accessibility settings (Passage) | | COVERED-5 | 5 | Keyboard-first |
| Home → Copilot settings (opt-in, data scope) (Passage) | | COVERED-47 | 47 | |
| Home → Mobile workspace configuration (D365 mobile app) | | SUPERSEDED (PWA shell) | 5 | |
| Home → Dashboard/tile configuration per workspace (D365 workspace tiles, Power BI embed) | | COVERED-46 | 46 | |

## 12.32 Data import/export framework

DIXF (AX 2012 R3) / Data management framework (D365): source data formats, processing groups, entities (target entities, custom entity wizard), staging, execution history, compare, copy between companies, recurring integrations. Maps to wave 10 (migration framework) and wave 35 (recurring integrations), with Excel in wave 5.

### 12.32.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Processing groups (list) / Processing group details | DMFDefinitionGroup | Group of entities with source format, mapping, sequence | COVERED-10 | 10 | Migration pipeline: extract/stage/map/validate/load |
| Processing group → Entities (select entities, source format, sample file, generate mapping, modify mapping, validate) | DMFDefinitionGroupEntity | | COVERED-10 | 10 | Declarative mapping DSL |
| Processing group → Get staging data (source → staging) | DMFStagingWriter | | COVERED-10 | 10 | Postgres staging schema |
| Processing group → Copy data to target (staging → target) | DMFStagingTarget | | COVERED-10 | 10 | |
| Processing group → Preview source data / Validate | DMFPreview | | COVERED-10 | 10 | |
| Staging data (view/edit staging records, errors) | DMFStaging* (per entity) | Review and correct before load | COVERED-10 | 10 | Exception queue with re-run |
| Execution history (jobs, status, errors, logs) | DMFExecution | | COVERED-10 | 10 | Migration run ledger with lineage |
| Target entities (entity list: Customers, Vendors, Products, BOM, Routes, Open transactions, Fixed assets, GL balances, Budget, Projects, HR, etc. ~150 entities in R3) | DMFEntity | | COVERED-10 | 10 | AX connectors for master data in 10; transactional packs in 49 |
| Custom entity wizard (create entity from table) | DMFEntityWizard | | COVERED-3 | 3 | Any metadata entity is importable (generated) |
| Entity structure / Entity attributes (field metadata) | DMFEntityAttribute | | COVERED-3 | 3 | |
| Compare data (source vs target) | DMFCompareData | | COVERED-10 | 10 | Reconciliation: counts, hashes, sampled compare |
| Copy entity data between companies | DMFCopyEntity | | COVERED-10 | 10 | |
| Data import/export → Export (to file by processing group) | DMFExport | | COVERED-5 | 5 | Excel/CSV export of any entity |
| D365 Data management workspace (Import/Export projects, Data packages, Templates, Job history, Recurring data jobs, Entity list, Configure entity export to database (BYOD)) | DMFDataManagement | | COVERED-10 | 10 | BYOD superseded by lakehouse 46 |
| D365 → Recurring data jobs (enqueue/dequeue APIs) | DMFRecurringJob | | COVERED-35 | 35 | Integration hub; Kafka |
| D365 → Data package (zip with manifest) / LCS asset library | | | COVERED-10 | 10 | Migration packs |
| D365 → Entity dependencies / Sequencing / Data templates (default templates) | | | COVERED-10 | 10 | |
| D365 → Configuration data projects (copy config across environments) | | | COVERED-10 | 10 | Also GitOps config |
| DIXF → Source → Staging via SSIS (service on AOS) | DMFService | | SUPERSEDED (pipeline service in Go/Python, no SSIS) | 10 | |

### 12.32.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Execution history / Job status inquiry | | COVERED-10 | 10 | |
| Staging data inquiry (errors by entity) | | COVERED-10 | 10 | |
| Compare data results | | COVERED-10 | 10 | |
| Entity mapping inquiry (field mapping per entity) | | COVERED-10 | 10 | |
| Recurring job status / Message status (D365) | | COVERED-35 | 35 | Message monitor |
| SSRS: Processing group report / Entity list | | COVERED-10 | 10 | |
| SSRS: Execution log / Error log report | | COVERED-10 | 10 | |
| Reconciliation report (row counts, hash totals, sampled field compare) (Passage) | | COVERED-10 | 10 | |

### 12.32.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Run processing group (batch: get staging, copy to target) | | COVERED-10 | 10 | |
| Clean up staging tables / Execution history | | COVERED-10 | 10 | |
| Recurring import/export jobs (D365 scheduling) | | COVERED-35 | 35 | |
| Entity store refresh (D365 BYOD/entity store) | | SUPERSEDED (CDC → lakehouse wave 46) | 46 | |
| Dry-run / Idempotent re-run (Passage) | | COVERED-10 | 10 | |
| Parallel-run replay (Passage migration factory) | | COVERED-49 | 49 | |

### 12.32.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Data import/export framework parameters (shared working directory, SSIS service, entity settings, source formats defaults) | | COVERED-10 | 10 | |
| Source data formats (file: CSV/XML/Excel/fixed width; ODBC; AX) with code page, delimiters, headers | | COVERED-10 | 10 | Extract: ODBC from AX SQL, SAP RFC/OData, flat files |
| Target entities setup (entity structure, staging table, query, validation class) | | COVERED-3 | 3 | Metadata-generated |
| Entity mapping defaults / Default values / Conversion (enum mapping, number sequence generation) | | COVERED-10 | 10 | Mapping DSL: coercion, enum mapping, default fill |
| Processing group sequencing / Entity dependencies | | COVERED-10 | 10 | |
| DIXF service configuration (AOS service) | | SUPERSEDED (K8s pipeline service) | 10 | |
| D365 → Data management parameters (entity settings, performance: parallel threads, set-based processing, skip staging) | | COVERED-10 | 10 | |
| D365 → Data management: Entity export to database (BYOD) configuration | | SUPERSEDED (lakehouse 46) | 46 | |
| D365 → Recurring integration endpoints / Integration keys | | COVERED-35 | 35 | |
| D365 → Data templates / Default templates per module | | COVERED-10 | 10 | |
| Passage → Legacy connectors (AX 2012/D365 F&O, SAP ECC/S4) configuration | | COVERED-10 | 10 | |
| Passage → Great Expectations validation suites | | COVERED-10 | 10 | |
| Passage → Migration run ledger / Lineage | | COVERED-10 | 10 | |

## 12.33 Electronic reporting / Regulatory Configuration Service (D365)

ER (D365; backported to AX 2012 R3 CU13 partially): configurations (models, model mappings, formats), configuration providers, repositories (LCS, RCS, Dataverse, file system, global repo), destinations, ER parameters, business document management, electronic messages, Globalization Studio (tax calculation service, e-invoicing service). Passage: wave 48 (ER designer, localization framework), wave 35 (e-invoicing, Peppol), wave 13 (tax calculation), wave 46 (business documents).

### 12.33.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Electronic reporting workspace / Configurations (tree: data model, model mapping, format, metadata) | ERWorkspace, ERSolutionTable | Design and manage configurations | COVERED-48 | 48 | ER designer: configurable XML/JSON/CSV with versioning |
| Configuration → Model designer (data model) | ERModelDesigner | | COVERED-48 | 48 | |
| Configuration → Model mapping designer (data sources: tables, entities, calculated fields, parameters) | ERModelMappingDesigner | | COVERED-48 | 48 | Over metadata entities |
| Configuration → Format designer (XML/JSON/text/Excel/Word output, bindings, formulas) | ERFormatDesigner | | COVERED-48 | 48 | Excel/Word output via wave 46 designer |
| Configuration → Versions (draft/completed/shared/discontinued), Import/Export (XML), Rebase | ERSolutionVersionTable | | COVERED-48 | 48 | Versioning |
| Configuration providers / Active provider | ERVendorTable | | COVERED-48 | 48 | |
| Repositories (LCS asset library, RCS, Dataverse, File system, Global repository, Operations resources) | ERRepository* | Fetch Microsoft-published configurations | SUPERSEDED (Passage country packs ship with Helm/packs; registry of regulator mandates wave 48) | 48 | |
| ER jobs (Electronic reporting jobs: run history, output files) | ERFormatMappingRunJobTable | | COVERED-48 | 48 | |
| ER destinations (per format: file, email, print, archive (document management), screen, Power BI, SharePoint) | ERFormatDestinationTable | | COVERED-48 | 48 | Archive depends on document management PARTIAL-5 |
| Business document management (Word/Excel templates for business documents, editable by users; template versions) | ERBusinessDocumentManagement | | COVERED-46 | 46 | Operational report designer |
| Electronic messages (Electronic messaging framework: message types, message items, processing actions, additional fields; used for VAT returns, SAF-T, UK MTD, IT e-invoice SDI, etc.) | EMElectronicMessage*, EMMessageProcessing | Statutory message lifecycle (generate, send, receive response) | PARTIAL-48 (electronic messaging lifecycle (statutory submission with response tracking, e.g. UK MTD, DE/FR mandates) not named; e-invoicing in 35 covers Peppol/SDI/CFDI submissions) | 48 | Gap register |
| Globalization Studio → Tax calculation service (D365 Tax Calculation: tax configurations, tax feature setup, tax applicability rules, tax codes designer) | TaxCalculationService | Configurable tax determination | COVERED-13 | 13 | Internal rules engine default + provider adapters |
| Globalization Studio → Electronic invoicing service (Invoicing service: submissions, processing pipelines, customization) | EInvoiceService* | Cloud e-invoicing | COVERED-35 | 35 | Peppol access point, SDI/CFDI hooks |
| Globalization Studio → Globalization features / Feature setup (per country) | | | COVERED-48 | 48 | Country packs |
| Regulatory Configuration Service (RCS: standalone config editing, Globalization feature management) | RCS portal | | SUPERSEDED (ER designer in-product wave 48; configs in Git-backed packs) | 48 | |
| ER → Data model/format parameters (application-specific parameters: lookup tables maintained by users, e.g. VAT box mapping) | ERFormatMappingParameters | | COVERED-48 | 48 | |
| ER → Format mapping run (run report manually with parameters) | ERFormatMappingRun | | COVERED-48 | 48 | |
| ER → Configuration dependencies / Model-to-format links | | | COVERED-48 | 48 | |

### 12.33.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| ER jobs history / Output archive inquiry | | COVERED-48 | 48 | |
| Configuration version history / Dependency inquiry | | COVERED-48 | 48 | |
| Electronic messages inquiry (status, responses, errors) | | PARTIAL-48 (electronic messaging) | 48 | |
| E-invoicing submission log (D365 invoicing service) | | COVERED-35 | 35 | |
| Tax calculation service logs | | COVERED-13 | 13 | |
| Business document template usage inquiry | | COVERED-46 | 46 | |
| ER → Configuration comparison (diff versions) | | COVERED-48 | 48 | |
| ER → Format validation report (schema validation) | | COVERED-48 | 48 | Regulator schema fixtures |
| Statutory reports catalog per country (Passage) | | COVERED-48 | 48 | |

### 12.33.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Run ER format in batch (scheduled statutory reports) | | COVERED-48 | 48 | |
| Import configurations from repository (update to latest versions) | | SUPERSEDED (pack updates via Helm/GitOps wave 48) | 48 | |
| Electronic messages → Generate / Send / Receive response (batch processing actions) | | PARTIAL-48 (electronic messaging) | 48 | |
| E-invoicing → Submit documents / Poll status (batch) | | COVERED-35 | 35 | |
| ER → Clean up job history / Archived outputs | | COVERED-48 | 48 | |
| Business document management → Publish templates | | COVERED-46 | 46 | |
| Tax calculation service → Sync configurations | | COVERED-13 | 13 | |
| Regulatory mandates registry refresh (Passage) | | COVERED-48 | 48 | |

### 12.33.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Electronic reporting parameters (attachments document types for job archive/baseline/others, LCS/RCS connection, Dataverse) | | COVERED-48 | 48 | |
| Configuration providers (own provider for custom configs) | | COVERED-48 | 48 | |
| Repositories (LCS, RCS, Global repo, file system, Dataverse) | | SUPERSEDED (Git-backed packs) | 48 | |
| ER destinations per format component (file/email/print/archive/screen/Power BI) + Electronic reporting destination parameters | | COVERED-48 | 48 | |
| ER → Application-specific parameters setup (per format version) | | COVERED-48 | 48 | |
| ER → Format mapping assignment to business processes (e.g. payment formats on method of payment, invoice formats in print management, customer/vendor statements) | | COVERED-48 | 48 | Payment formats 19; print 46 |
| Business document management → Parameters (SharePoint/OneDrive for template editing), Template access by role | | COVERED-46 | 46 | |
| Electronic messages → Message types, Message item types, Message statuses, Processing actions (ER export/import, web service call, user input), Additional fields, Executable classes, Web service settings (endpoint, certificates), Message processing | | PARTIAL-48 (electronic messaging) | 48 | |
| Globalization Studio → Tax calculation parameters (connection, feature per LE), Tax features (versions), Tax applicability (customer/vendor/item tax groups by rules) | | COVERED-13 | 13 | |
| Globalization Studio → Electronic invoicing parameters (invoicing service connection, feature per LE, processing pipelines, submission channels), E-invoicing document types | | COVERED-35 | 35 | |
| Globalization Studio → Globalization feature setup per legal entity | | COVERED-48 | 48 | |
| RCS → Globalization feature management (standalone) | | SUPERSEDED (in-product) | 48 | |
| ER → Key vault certificates for signing (XAdES) | | COVERED-36 | 36 | Vault; signing in 35/48 |
| ER → Configuration localization (labels per language) | | COVERED-48 | 48 | |
| Passage → Country pack registry (US, UK, DE, FR, IN, MX, CA) with regulator schema fixtures | | COVERED-48 | 48 | |
| Passage → Regulator mandate registry (effective dates, versions) | | COVERED-48 | 48 | |

## 12.34 Engineering change management (D365)

D365 ECM: engineering products, engineering categories/attributes, engineering versions, product readiness, engineering change requests/orders with impact analysis, release to legal entities, product lifecycle states. Passage wave 9 covers ECR/ECO workflow, impact analysis (where-used), versions and release to production; engineering organization/release policies and readiness checks are partial.

### 12.34.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Engineering products (list) / Engineering product details | EngChgEngineeringProduct | Version-tracked products owned by engineering org | COVERED-9 | 9 | |
| Engineering versions (version per product with BOM/route/attributes, effective dates, active) | EngChgEcoResProductVersion | | COVERED-9 | 9 | Effective-dated versions |
| Engineering change requests (list) / details | EngChgEngineeringChangeRequest | Request with impacted products, severity, workflow | COVERED-9 | 9 | |
| Engineering change orders (list) / details | EngChgEngineeringChangeOrder | Order with product changes (BOM, route, attributes, new version), impact analysis, approval, processing | COVERED-9 | 9 | ECO workflow with impact analysis |
| Engineering change order → Impact analysis (where-used, open orders, on-hand, planned orders) | EngChgImpactAnalysis | | COVERED-9 | 9 | Where-used; open order impact should be verified against 21/22 |
| Engineering change order → Process changes (apply to products, create version) | EngChgProcessChange | | COVERED-9 | 9 | |
| Release product structure (release engineering product + BOM/route to legal entities) | EngChgReleaseProductStructure | Release to operational LEs with policies | PARTIAL-9 (engineering organization vs operational legal entity split, and release product structure to legal entities with per-LE release policies, not named) | 9 | Gap register |
| Product readiness checks (per LE checklist before product is usable) | EngChgProductReadiness | | PARTIAL-9 (product readiness) | 9 | |
| Product acceptance (accept released product in receiving LE) | EngChgProductAcceptance | | PARTIAL-9 (release product structure) | 9 | |
| Product lifecycle states (per version, controlling process enablement) | EcoResProductLifecycleState | | PARTIAL-8 (lifecycle states) | 8 | |
| Engineering attributes / Attribute search (engineering attribute values per version) | EngChgEngineeringAttribute | | COVERED-8 | 8 | Attributes & classification |
| Engineering product categories (category assigns version nomenclature, attributes, readiness policy) | EngChgEngineeringProductCategory | | PARTIAL-9 (engineering product categories with version nomenclature not named; category hierarchies are) | 9 | Gap register |
| Engineering change → Open order impact (sales/purchase/production orders referencing superseded version) | | | COVERED-9 | 9 | |
| Engineering → Document/drawing attachments per version | DocuRef (version) | | COVERED-9 | 9 | |
| Engineering → Product change cases (AX 2012 R3 case management for product changes) | CaseDetail (product change) | | COVERED-9 | 9 | |
| Engineering → PLM integration (D365 Teamcenter/Windchill connectors via Dataverse) | | | PARTIAL-35 (PLM connector not named among iPaaS reference connectors) | 35 | Gap register |

### 12.34.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Engineering version inquiry / Version comparison (BOM diff between versions) | | COVERED-9 | 9 | |
| Where-used inquiry (across versions) | | COVERED-9 | 9 | |
| Change request/order inquiries (by status, product, severity) | | COVERED-9 | 9 | |
| Impact analysis report | | COVERED-9 | 9 | |
| Release status inquiry (product × legal entity) | | PARTIAL-9 (release product structure) | 9 | |
| Readiness check status inquiry | | PARTIAL-9 (product readiness) | 9 | |
| SSRS: Engineering change order (document) / Change request | | COVERED-9 | 9 | |
| SSRS: Engineering version list / BOM comparison | | COVERED-9 | 9 | |
| SSRS: Product release report | | PARTIAL-9 (release product structure) | 9 | |

### 12.34.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Process engineering change orders (batch apply) | | COVERED-9 | 9 | |
| Release product structures (batch release to LEs) | | PARTIAL-9 (release product structure) | 9 | |
| Expire old versions / Activate versions by effective date | | COVERED-9 | 9 | |
| Readiness check evaluation (batch) | | PARTIAL-9 (product readiness) | 9 | |
| Engineering → Sync with PLM (batch) | | PARTIAL-35 (PLM connector) | 35 | |
| Engineering → Update open orders to new version (mass) | | COVERED-9 | 9 | |

### 12.34.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Engineering change management parameters (engineering organization, default severity, change order defaults, version activation, number sequences) | | COVERED-9 | 9 | |
| Engineering organizations (which LE owns engineering data) | | PARTIAL-9 (release product structure) | 9 | |
| Engineering product categories (version nomenclature, attributes, readiness policy, release policy) | | PARTIAL-9 (engineering product categories) | 9 | |
| Version nomenclature (number/letter/date segments) | | PARTIAL-9 (engineering product categories) | 9 | |
| Product readiness policies / Readiness checks (per-LE checklists: tax, prices, coverage, warehouse setup) | | PARTIAL-9 (product readiness) | 9 | |
| Product release policies (what to copy to receiving LE: BOM, route, prices, default order settings) | | PARTIAL-9 (release product structure) | 9 | |
| Engineering attributes / Attribute types / Attribute search setup | | COVERED-8 | 8 | |
| Product lifecycle states (enabled business processes per state, default states) | | PARTIAL-8 (lifecycle states) | 8 | |
| Change severity / Change priority / Change categories / Change reasons | | COVERED-9 | 9 | |
| Impact analysis configuration (which documents to scan) | | COVERED-9 | 9 | |
| ECM workflows (change request, change order approval) | | COVERED-4 | 4 | |
| Engineering → Number sequences (versions, change orders) | | COVERED-5 | 5 | |
| Engineering → PLM connector configuration | | PARTIAL-35 (PLM connector) | 35 | |

## 12.35 Asset management (D365)

D365 Asset management (EAM): functional locations, assets, asset types/lifecycle, maintenance plans/rounds/sequences, job types, checklists, maintenance requests, work orders with scheduling and journals, counters, fault management, condition assessment, warranties, costing and KPIs. Maps to wave 44.

### 12.35.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Assets (list) / Asset details (type, lifecycle state, functional location, model/manufacturer, attributes, BOM/spare parts, counters, warranties, documents) | EntAssetObjectTable | | COVERED-44 | 44 | |
| Functional locations (hierarchy, types, lifecycle) | EntAssetFunctionalLocation | | COVERED-44 | 44 | |
| Asset lifecycle state changes / Asset history (installation, transfers) | EntAssetObjectLifecycleState | | COVERED-44 | 44 | |
| Maintenance plans (time-based, counter-based, condition) / Maintenance plan lines | EntAssetMaintenancePlan | | COVERED-44 | 44 | |
| Maintenance rounds (route of assets inspected together) | EntAssetMaintenanceRound | | PARTIAL-44 (maintenance rounds (multi-asset inspection routes) not named) | 44 | Gap register |
| Maintenance sequences (ordered job types on an asset) | EntAssetMaintenanceSequence | | PARTIAL-44 (maintenance sequences not named) | 44 | Gap register |
| Maintenance requests (list) / details (request types, convert to work order) | EntAssetMaintenanceRequest | | COVERED-44 | 44 | IoT-triggered requests |
| Work orders (list) / details (lines per asset/job type, lifecycle state, scheduled, workers, checklist, tools, items, hours, expenses, project) | EntAssetWorkOrderTable | | COVERED-44 | 44 | |
| Work order → Schedule (scheduling of workers/capacity; work order pools; dispatch) | EntAssetWorkOrderSchedule | | COVERED-44 | 44 | Scheduling against resources wave 23 |
| Work order → Journals (hours, items, expenses) and posting to project/GL | EntAssetWorkOrderJournal | | COVERED-44 | 44 | Costs → asset and GL |
| Work order → Checklists / Checklist results | EntAssetWorkOrderChecklist | | COVERED-44 | 44 | |
| Work order → Fault registration (symptoms, causes, remedies) | EntAssetFault | | PARTIAL-44 (fault designer: symptom/area/type/cause/remedy taxonomy and fault analysis not named) | 44 | Gap register |
| Work order → Downtime registration (maintenance downtime affecting production resource) | EntAssetMaintenanceDowntime | | COVERED-44 | 44 | Downtime → scheduling capacity |
| Work order → Tools (resources booked) | EntAssetWorkOrderTool | | COVERED-44 | 44 | |
| Work order → Project / Cost control (work order cost, budget) | EntAssetWorkOrderProject | | COVERED-44 | 44 | |
| Counters (asset counters, counter registrations) / Measurements | EntAssetCounter | | COVERED-44 | 44 | Counters and measurement points |
| Condition assessment (records per asset) | EntAssetConditionAssessment | | COVERED-44 | 44 | Condition monitoring |
| Asset BOM / Spare parts (parts per asset with where-used) | EntAssetObjectBOM | | COVERED-44 | 44 | |
| Warranties (vendor warranty per asset, warranty check on work orders) | EntAssetWarranty | | PARTIAL-44 (asset warranties (vendor warranty terms, warranty check on work order) not named) | 44 | Gap register |
| Maintenance budgets (asset/functional location budget lines, budget calculation from plans) | EntAssetMaintenanceBudget | | PARTIAL-44 (maintenance budgeting from plans not named) | 44 | Gap register |
| Asset insurance / Asset attributes | | | COVERED-44 | 44 | Attributes via wave 8 framework |
| Maintenance workers / Worker groups / Worker skills (technicians) | EntAssetWorker | | COVERED-44 | 44 | |
| Maintenance planning workspace / Asset management workspace | | | COVERED-44 | 44 | |
| Mobile maintenance (D365 Asset management mobile workspace) | | | COVERED-44 | 44 | PWA |
| Asset → IoT sensors / Sensor Data Intelligence (asset condition from IoT) | | | COVERED-25 | 25 | IoT ingestion; thresholds in 44 |
| Asset → Link to fixed asset (financial asset) | EntAssetObjectFixedAsset | | COVERED-37 | 37 | |
| Asset → Link to production resource (machine) | EntAssetObjectResource | | COVERED-44 | 44 | |

### 12.35.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Work order inquiries (by state, asset, worker, overdue) | | COVERED-44 | 44 | |
| Maintenance request inquiries | | COVERED-44 | 44 | |
| Asset cost inquiry (cost per asset/functional location: hours, items, expenses, by period) | | COVERED-44 | 44 | |
| Asset KPIs (MTBF, MTTR, availability, cost per asset, downtime) | | COVERED-44 | 44 | |
| Fault analysis (fault statistics by symptom/cause) | | PARTIAL-44 (fault designer) | 44 | |
| Counter history / Condition trends | | COVERED-44 | 44 | |
| Maintenance plan forecast (upcoming work by plan) | | COVERED-44 | 44 | |
| Warranty inquiry | | PARTIAL-44 (warranties) | 44 | |
| Budget vs actual (maintenance) | | PARTIAL-44 (maintenance budgets) | 44 | |
| SSRS: Work order (document) / Work order report / Work order pick list / Checklist | | COVERED-44 | 44 | |
| SSRS: Maintenance plan report / Forecast | | COVERED-44 | 44 | |
| SSRS: Asset list / Functional location list / Asset hierarchy | | COVERED-44 | 44 | |
| SSRS: Asset cost report / Cost control | | COVERED-44 | 44 | |
| SSRS: Fault report | | PARTIAL-44 (fault designer) | 44 | |
| SSRS: Downtime report | | COVERED-44 | 44 | |
| SSRS: Asset KPI report | | COVERED-44 | 44 | |
| SSRS: Spare parts where-used | | COVERED-44 | 44 | |

### 12.35.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Schedule maintenance plans (generate work orders from time/counter/condition plans) | | COVERED-44 | 44 | Plan fixtures |
| Schedule maintenance rounds / Maintenance sequences | | PARTIAL-44 (maintenance rounds) | 44 | |
| Work order scheduling (auto-schedule, capacity) | | COVERED-44 | 44 | |
| Counter updates (import from IoT/production) | | COVERED-44 | 44 | |
| Condition threshold evaluation (IoT) | | COVERED-44 | 44 | |
| Post work order journals (batch) | | COVERED-44 | 44 | |
| Maintenance budget calculation | | PARTIAL-44 (maintenance budgets) | 44 | |
| Asset KPI calculation (batch) | | COVERED-44 | 44 | |
| Close work orders / Update lifecycle states (batch) | | COVERED-44 | 44 | |
| Mobile sync | | COVERED-44 | 44 | |

### 12.35.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Asset management parameters (work order defaults, journal names, project integration, scheduling, number sequences, mobile) | | COVERED-44 | 44 | |
| Assets → Asset types, Asset type defaults (job types, counters, attributes, checklists), Asset lifecycle models/states, Asset models, Manufacturers, Asset attribute types, Asset criticality, Asset service levels | | COVERED-44 | 44 | |
| Functional locations → Functional location types, Lifecycle models/states, Attributes | | COVERED-44 | 44 | |
| Maintenance jobs → Maintenance job types, Job type categories, Job type variants, Trades, Job type defaults (hours, items, checklists, tools), Checklist templates/variables | | COVERED-44 | 44 | |
| Maintenance plans → Plan types, Plan line defaults, Maintenance rounds setup, Maintenance sequences setup | | PARTIAL-44 (maintenance rounds) | 44 | |
| Work orders → Work order types, Work order lifecycle models/states, Work order pools, Work order priorities, Service levels, Work order projects (project groups/categories), Scheduling parameters (workers, capacity, preferred workers) | | COVERED-44 | 44 | |
| Maintenance requests → Request types, Request lifecycle states, Request-to-work-order defaults | | COVERED-44 | 44 | |
| Workers → Maintenance workers, Worker groups, Skills, Worker calendars | | COVERED-44 | 44 | |
| Counters → Counter types, Counter units | | COVERED-44 | 44 | |
| Fault → Fault symptoms, Fault areas, Fault types, Fault causes, Fault remedies, Fault designer (per asset type) | | PARTIAL-44 (fault designer) | 44 | |
| Condition assessment → Condition assessment templates | | COVERED-44 | 44 | |
| Warranties → Warranty types, Vendor warranty setup | | PARTIAL-44 (warranties) | 44 | |
| Budget → Maintenance budget parameters | | PARTIAL-44 (maintenance budgets) | 44 | |
| Cost control → Cost types, Asset cost posting (project categories/GL) | | COVERED-44 | 44 | |
| Downtime → Downtime reasons, Capacity impact on production resources | | COVERED-44 | 44 | |
| IoT → Sensor scenarios, Thresholds, Notification rules | | COVERED-25 | 25 | |
| Mobile → Mobile workspace configuration | | COVERED-44 | 44 | |
| Workflow → Asset management workflows (request approval, work order approval) | | COVERED-4 | 4 | |
| Number sequences (assets, work orders, requests) | | COVERED-5 | 5 | |

## 12.36 Rebate management (D365)

D365 Rebate management (2020+): deals (customer or vendor), deal lines with tiers, transaction accruals, deal statuses, provisions, rebate posting via journals, claims. Passage: customer rebates in waves 26 (agreements/accrual) and 30 (settlement); vendor-side deals are a gap (proposed wave 26).

### 12.36.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Rebate management deals (list) / Deal details (customer/vendor, products, tiers, dates, calculation basis, posting) | RebateManagementDeal | | COVERED-26 | 26 | Customer deals; vendor deals GAP |
| Deal → Vendor deals (purchase rebates) | RebateManagementDeal (vendor) | | GAP | — | Propose wave 26: vendor rebates |
| Deal lines / Tiers (amount/quantity break, percentage/amount/price) | RebateManagementDealLine | | COVERED-26 | 26 | |
| Rebate management transactions (accrued per invoice line) | RebateManagementTransaction | | COVERED-26 | 26 | |
| Provisions (accrual journal) / Reversal of provisions | RebateManagementProvision | | COVERED-26 | 26 | Accrual postings |
| Process rebates (create claims: credit note, AP invoice, customer payment journal line) | RebateManagementProcess | | COVERED-30 | 30 | Rebate settlement |
| Deal status (draft, active, closed, cancelled) / Deal approval workflow | | | COVERED-26 | 26 | |
| Deal → Price-based deals (royalty-type) | | | GAP | — | Propose wave 26: royalty agreements |
| Rebate management workspace | | | COVERED-26 | 26 | |

### 12.36.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Rebate transactions inquiry (by deal/customer/period) | | COVERED-26 | 26 | |
| Provision balance inquiry (accrued vs settled) | | COVERED-30 | 30 | |
| Deal performance (achieved tier, projected rebate) | | COVERED-26 | 26 | |
| Vendor deal inquiries | | GAP | — | Propose wave 26: vendor rebates |
| SSRS: Rebate deal (document) / Rebate statement to customer | | COVERED-30 | 30 | |
| SSRS: Rebate accrual report | | COVERED-26 | 26 | |
| SSRS: Vendor rebate claim | | GAP | — | Propose wave 26: vendor rebates |

### 12.36.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Generate rebate transactions (from posted invoices) | | COVERED-26 | 26 | |
| Post provisions (accruals) / Reverse provisions | | COVERED-26 | 26 | |
| Process rebates (claims) / Settle | | COVERED-30 | 30 | |
| Close/expire deals | | COVERED-26 | 26 | |
| Vendor rebate claims processing | | GAP | — | Propose wave 26: vendor rebates |

### 12.36.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Rebate management parameters (journal names, posting profiles, default calculation, workflow, number sequences) | | COVERED-26 | 26 | |
| Rebate management posting profiles (accrual/expense accounts) | | COVERED-26 | 26 | |
| Deal types / Rebate reduction principles / Calculation methods (per unit, percentage, fixed) | | COVERED-26 | 26 | |
| Rebate groups (customer rebate groups, item rebate groups) | | COVERED-26 | 26 | |
| Vendor deal setup (claim via AP) | | GAP | — | Propose wave 26: vendor rebates |
| Workflow → Deal approval | | COVERED-4 | 4 | |
| Number sequences (deals) | | COVERED-5 | 5 | |

## 12.37 Subscription billing (D365)

D365 Subscription billing (2021+): Recurring contract billing (billing schedules, groups, escalation, milestones, unbilled revenue), Revenue and expense deferrals (deferral schedules, templates, recognition), Multiple element revenue allocation (MEA templates, standalone selling prices). Maps to wave 29.

### 12.37.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Billing schedules (list) / Billing schedule details (lines, frequency, start/end, escalation, milestones, pricing, deferral) | RCB BillingSchedule | Recurring contract billing | COVERED-29 | 29 | Subscription billing: recurring schedules, proration, usage |
| Billing schedule → Generate invoices (free text / sales order) | RCB GenerateInvoice | | COVERED-29 | 29 | |
| Billing schedule → Escalation/de-escalation (price changes by period) | RCB Escalation | | COVERED-29 | 29 | |
| Billing schedule → Milestone billing (milestone templates) | RCB Milestone | | COVERED-29 | 29 | |
| Billing schedule → Usage-based billing (consumption import) | RCB Usage | | COVERED-29 | 29 | |
| Billing schedule → Renewal / Termination / Put on hold / Credit (prorated) | | | COVERED-29 | 29 | |
| Unbilled revenue (unbilled revenue schedules, invoiced-in-advance) | RCB UnbilledRevenue | | COVERED-29 | 29 | Deferred revenue postings |
| Deferral schedules (list) / details (revenue and expense deferrals, short-term/long-term split) | RED DeferralSchedule | | PARTIAL-29 (expense deferrals (prepaid expense amortization from AP invoices) not named; revenue deferrals are) | 29 | Gap register |
| Deferral → Recognition processing (recognize period amounts, generate journal) | RED Recognition | | COVERED-29 | 29 | |
| Deferral → Deferral templates / Straight-line vs event-based | RED Template | | COVERED-29 | 29 | |
| Multiple element revenue allocation (MEA) → MEA templates, SSP allocation, allocation on sales order/billing schedule | MEA* | | COVERED-29 | 29 | SSP allocation, ASC 606 bundles |
| Contract renewal workspace / Subscription billing workspace | | | COVERED-29 | 29 | |
| Billing schedule → Customer portal self-service (view subscriptions) | | | COVERED-30 | 30 | |

### 12.37.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Billing schedule inquiries (upcoming invoices, renewals due, MRR/ARR) | | COVERED-29 | 29 | |
| Unbilled revenue inquiry / Deferred revenue roll-forward | | COVERED-29 | 29 | Reconciles to GL |
| Deferral schedule inquiry (balance by period, short/long-term) | | COVERED-29 | 29 | Expense deferral PARTIAL |
| MEA allocation inquiry | | COVERED-29 | 29 | |
| SSRS: Billing schedule (document) / Invoice preview | | COVERED-29 | 29 | |
| SSRS: Deferred revenue report / Revenue waterfall | | COVERED-29 | 29 | |
| SSRS: Unbilled revenue report | | COVERED-29 | 29 | |
| SSRS: Subscription metrics (MRR, churn) | | COVERED-46 | 46 | |

### 12.37.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Generate invoices from billing schedules (batch by date) | | COVERED-29 | 29 | |
| Recognition processing (deferral recognition batch) | | COVERED-29 | 29 | |
| Short-term/long-term reclassification (batch) | | COVERED-29 | 29 | |
| Escalation processing (price updates) | | COVERED-29 | 29 | |
| Usage import (batch) | | COVERED-29 | 29 | |
| Renewal processing / Expire schedules | | COVERED-29 | 29 | |
| Unbilled revenue posting (batch) | | COVERED-29 | 29 | |
| Expense deferral recognition | | PARTIAL-29 (expense deferrals) | 29 | |

### 12.37.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Recurring contract billing parameters (invoice type, proration method, number sequences, posting) | | COVERED-29 | 29 | |
| Billing schedule groups / Billing frequencies / Item billing types / Escalation types / Milestone templates / Termination reasons / Renewal settings | | COVERED-29 | 29 | |
| Revenue and expense deferrals parameters (posting, short/long-term accounts, recognition method) | | COVERED-29 | 29 | Expense PARTIAL |
| Deferral templates / Deferral defaults (per item/customer/account) / Deferral item groups / Event-based deferral events | | COVERED-29 | 29 | |
| MEA parameters / MEA templates / Standalone selling prices (by item/customer group, date-effective) / Allocation methods (relative SSP, residual) | | COVERED-29 | 29 | |
| Workflow → Billing schedule approval | | COVERED-4 | 4 | |
| Number sequences | | COVERED-5 | 5 | |

## 12.38 Revenue recognition (D365)

D365 Revenue recognition (2019–2023, now deprecated in favor of Subscription billing): revenue schedules, revenue prices (SSP), bundles, revenue recognition journals, reallocation, expected revenue. Maps to wave 29.

### 12.38.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Revenue schedules (per sales order line: schedule lines by period, status, hold) | RevRecSchedule | | COVERED-29 | 29 | Schedules (ratable, milestone, usage) |
| Revenue recognition journal (recognize scheduled revenue, post) | RevRecJournal (LedgerJournalTable) | | COVERED-29 | 29 | |
| Revenue price allocation (on sales order: allocate per SSP across lines/bundles) | RevRecAllocation | | COVERED-29 | 29 | |
| Expected revenue / Expected revenue schedule (before invoicing) | RevRecExpectedRevenue | | COVERED-29 | 29 | |
| Bundles (bundle items with components for allocation) | RevRecBundle | | COVERED-29 | 29 | Bundles |
| Reallocation (after order change, re-run allocation) | RevRecReallocation | | COVERED-29 | 29 | |
| Revenue recognition → Contract (contract terms per order) | RevRecContract | | COVERED-29 | 29 | |
| Revenue recognition workspace | | | COVERED-29 | 29 | |

### 12.38.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Revenue schedule inquiry (deferred/recognized by period) | | COVERED-29 | 29 | |
| Allocation details inquiry | | COVERED-29 | 29 | |
| Deferred revenue roll-forward | | COVERED-29 | 29 | |
| SSRS: Revenue recognition report / Revenue schedule report | | COVERED-29 | 29 | |
| SSRS: Deferred revenue by customer | | COVERED-29 | 29 | |

### 12.38.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Create revenue recognition journal (batch) | | COVERED-29 | 29 | |
| Post revenue recognition journal | | COVERED-29 | 29 | |
| Reallocation processing | | COVERED-29 | 29 | |
| Update expected revenue | | COVERED-29 | 29 | |

### 12.38.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Revenue recognition parameters (journal names, deferred revenue accounts, recognition basis, bundle handling) | | COVERED-29 | 29 | |
| Revenue schedules (schedule templates: occurrences, frequency, recognition basis: contract start/invoice date, milestone) | | COVERED-29 | 29 | |
| Revenue prices (SSP by item/customer/currency, allocation %) | | COVERED-29 | 29 | |
| Item groups / Released product → Revenue type (post-contract support, essential, non-essential), Revenue schedule default | | COVERED-29 | 29 | |
| Bundle setup (bundle components) | | COVERED-29 | 29 | |
| Revenue recognition posting (ledger accounts for deferred revenue, contract liability) | | COVERED-29 | 29 | |
| Number sequences | | COVERED-5 | 5 | |

## 12.39 Vendor collaboration / customer self-service (D365)

D365 Vendor collaboration (PO confirmation, consignment, invoices, RFQ reply, onboarding/registration), vendor collaboration user provisioning, and customer self-service (Power Pages/portal). Passage: supplier portal (wave 20), customer portal (wave 30).

### 12.39.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Vendor collaboration workspace (vendor view: POs to confirm, confirmed, rejected) | VendVendorPortalPurchaseOrder | | COVERED-20 | 20 | Supplier portal: PO acknowledgement |
| Vendor → Confirm/reject PO with changes (substitute, split, dates) | VendVendorPortalPurchaseOrderConfirm | | COVERED-20 | 20 | |
| Vendor → Confirm with notes / attachments | | | COVERED-20 | 20 | |
| Vendor → Consignment inventory view (on-hand, replenishment orders) | VendVendorPortalConsignment | | GAP | — | Propose wave 16: consignment |
| Vendor → Invoices (submit invoice against PO/receipt; view invoice status) | VendVendorPortalInvoice | | COVERED-20 | 20 | |
| Vendor → RFQ replies (bid via portal) | VendVendorPortalRFQ | | COVERED-17 | 17 | |
| Vendor → ASN submission (D365 vendor shipping notice) | | | COVERED-20 | 20 | ASN |
| Vendor → Document exchange / Contacts / Company information maintenance (bank account change requests) | | | COVERED-20 | 20 | Document exchange; change approval workflow 7 |
| Vendor → Prospective vendor registration (onboarding wizard, questionnaires) | VendProspectiveVendorRegistration | | PARTIAL-20 (vendor onboarding) | 20 | |
| Vendor collaboration user provisioning (invite users, roles: vendor admin, vendor user; security per vendor) | VendVendorPortalUserRequest | | COVERED-20 | 20 | Keycloak external identities |
| Vendor → Vendor collaboration: Product receipt visibility / Payment status | | | COVERED-20 | 20 | Payment advice via portal (wave 19) |
| Customer self-service (D365 customer portal template: orders, invoices, cases, account) | Customer portal (Power Pages) | | COVERED-30 | 30 | Customer portal |
| Customer portal → Order entry / Order status / Shipment tracking | | | COVERED-27 | 27 | Portal completed in 30 |
| Customer portal → Invoices and payments (card/ACH) | | | COVERED-30 | 30 | |
| Customer portal → Returns request | | | COVERED-30 | 30 | |
| Customer portal → Cases / Service requests | | | COVERED-45 | 45 | |
| Customer portal → Documents (statements, CoA) | | | COVERED-30 | 30 | |

### 12.39.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Vendor collaboration inquiries (confirmations pending, response time) | | COVERED-20 | 20 | |
| Portal user activity / provisioning status | | COVERED-20 | 20 | |
| Customer portal usage inquiry | | COVERED-30 | 30 | |
| SSRS: Vendor confirmation performance | | COVERED-33 | 33 | Supplier scorecards |

### 12.39.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Send POs to portal / Notify vendors (batch) | | COVERED-20 | 20 | |
| Expire pending confirmations / Escalate | | COVERED-20 | 20 | |
| Provision portal users (batch invite) / Deactivate | | COVERED-20 | 20 | |
| Sync customer portal data | | SUPERSEDED (portal reads APIs directly; no sync) | 30 | |

### 12.39.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Vendor collaboration parameters (PO confirmation required, auto-confirm rules, invoice submission, consignment, onboarding questionnaires) | | COVERED-20 | 20 | |
| Vendor collaboration → Security roles for external users / Vendor collaboration user provisioning workflow | | COVERED-2 | 2 | |
| Vendor collaboration → Procurement: Purchase order change settings for vendor-proposed changes | | COVERED-17 | 17 | |
| Vendor collaboration → Onboarding: Registration wizard configuration, Questionnaires | | PARTIAL-20 (vendor onboarding) | 20 | |
| Customer self-service → Portal configuration (branding, features, payment adapter) | | COVERED-30 | 30 | |
| Customer self-service → External identity (customer users, B2B invitations) | | COVERED-2 | 2 | |
| Customer self-service → Dual-write/Dataverse dependency (D365 portal uses Dataverse) | | SUPERSEDED (portal on Passage APIs) | 30 | |

## 12.40 Dual-write / Dataverse integration (D365)

D365 Dual-write synchronizes F&SCM tables with Dataverse for Power Platform and Dynamics 365 CE apps (Sales, Field Service, Customer Service). Passage supersedes the entire mechanism with the integration hub: generated APIs, Kafka topics, webhooks, MCP and reference connectors (Salesforce, Shopify, Stripe). Every row is `SUPERSEDED` and points at the replacing wave.

### 12.40.1 Common / Journals

| Form | AOT form / menu item | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|---|
| Dual-write administration (Data integration → Dual-write: table maps, running/paused status) | DualWriteAdmin | Manage entity maps | SUPERSEDED (integration hub: Kafka topics per entity + webhooks, wave 35) | 35 | |
| Table maps (standard maps: customers, vendors, products, orders, invoices, etc.) / Map versions | DualWriteTableMap | | SUPERSEDED (AsyncAPI-described topics from entity metadata, wave 4) | 4 | |
| Initial sync (initial write) / Catch-up sync | DualWriteInitialSync | | SUPERSEDED (replay from Kafka/outbox; migration framework wave 10) | 35 | |
| Dual-write → Error log / Alerts | DualWriteErrorLog | | SUPERSEDED (message monitor with replay and DLQ, wave 35) | 35 | |
| Dual-write → Company (legal entity) mapping to Dataverse business units | | | SUPERSEDED (tenant/legal entity in API scope) | 2 | |
| Dataverse → Virtual entities (F&O virtual tables in Dataverse) | | | SUPERSEDED (GraphQL/REST APIs, wave 35) | 35 | |
| Power Platform integration (Power Apps embed, Power Automate flows, Power BI) | | | SUPERSEDED (embedded dashboards wave 46; Temporal workflows/agents wave 47; webhooks 35) | 46 | |
| Dynamics 365 Sales / Customer Service / Field Service integration via Dual-write | | | SUPERSEDED (Salesforce/HubSpot connectors wave 35; native service wave 45) | 35 | CRM non-goal |
| Business events to Power Automate / Logic Apps | | | SUPERSEDED (CloudEvents on Kafka + webhooks, wave 4/35) | 4 | |
| Data integrator (legacy CDS data integration projects) | | | SUPERSEDED (integration hub) | 35 | |
| Finance and Operations virtual entities / OData endpoints | | | SUPERSEDED (generated OpenAPI/GraphQL, wave 3/35) | 3 | |
| Export to Data Lake (D365 Export to Azure Data Lake / Synapse Link) | | | SUPERSEDED (CDC → Iceberg lakehouse, wave 46) | 46 | |

### 12.40.2 Inquiries & Reports

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Dual-write sync status inquiry | | SUPERSEDED (message monitor wave 35) | 35 | |
| Dual-write error inquiry | | SUPERSEDED (message monitor/DLQ wave 35) | 35 | |
| Dataverse → Entity sync health (Power Platform admin center) | | SUPERSEDED (observability wave 5; integration SLAs wave 35) | 35 | |
| Synapse Link status | | SUPERSEDED (lakehouse freshness metrics wave 46) | 46 | |

### 12.40.3 Periodic

| Name | Purpose | Passage status | Wave | Notes |
|---|---|---|---|---|
| Initial sync / Resync table maps | | SUPERSEDED (replay) | 35 | |
| Pause/resume maps (maintenance windows) | | SUPERSEDED (consumer pause/resume on Kafka) | 35 | |
| Error retry batch | | SUPERSEDED (retry with backoff + DLQ) | 35 | |
| Export to Data Lake incremental refresh | | SUPERSEDED (CDC streaming) | 46 | |

### 12.40.4 Setup

| Name | Key parameters / options | Passage status | Wave | Notes |
|---|---|---|---|---|
| Dual-write setup (link environment, Dataverse connection, solution install) | | SUPERSEDED (integration hub setup: API keys, webhook endpoints, Kafka consumer groups, wave 35) | 35 | |
| Table map configuration (field mapping, transforms, filters, direction) | | SUPERSEDED (event schemas from metadata; mapping in connectors) | 4 | |
| Dual-write → Integration key configuration (alternate keys) | | SUPERSEDED (entity natural keys in metadata) | 3 | |
| Power Platform environment linkage (LCS) | | SUPERSEDED (developer portal wave 35) | 35 | |
| Export to Data Lake configuration | | SUPERSEDED (lakehouse wave 46) | 46 | |
| Dataverse security roles for virtual entities | | SUPERSEDED (OPA policies on APIs wave 2) | 2 | |

## 12.99 Gap register

Every row in §12.1–§12.40 whose status is `GAP` or `PARTIAL` appears here with a proposed wave assignment. `GAP` rows carry their proposal in the Notes column of the source table ("Propose wave NN: …"); `PARTIAL` rows propose the wave shown in their Wave column unless the Notes say otherwise. The register is generated from the tables above by `scripts`-style tooling so it cannot drift from the inventory; re-run the assembler after editing any module section.

### 12.99.1 Gap themes and proposed wave assignments

The detailed registers below are generated from every `GAP` and `PARTIAL` row in §12.1–§12.40. This table groups them into the themes a wave owner would actually plan. Proposed waves respect the dependency chain in waves.json (a theme is never proposed before the wave whose data model it needs). Items marked **(wave-splitting candidate)** are large enough that the wave's estimate should be revisited at the next phase-exit re-estimation (PRD §11).

| Theme | AX / D365 feature(s) | Proposed wave | Why this wave | Size |
|---|---|---|---|---|
| Transfer orders | Transfer orders with in-transit warehouse, ship/receive, underdelivery, transfer picking, WHS transfer flows | 16 | Inventory ledger wave owns movements; MRP (22) and WHS (31) consume transfer orders as supply/work | **(wave-splitting candidate)** |
| Consignment inventory | Vendor-owned on-hand, ownership-change journal, consignment replenishment orders, vendor portal consignment view | 16 | Needs inventory ledger ownership dimension; surfaces in AP (19) and supplier portal (20) | Medium |
| Inventory extras | Tag counting, ABC classification, inventory aging, picking workbench | 16 / 20 / 28 | Natural extensions of counting, costing and fulfillment waves | Small each |
| Alerts and notification rules | Change-based and due-date alert rules on any entity/field, alert inbox, email delivery | 5 | Shell + event backbone exist by wave 5; every later module expects alerts | Medium |
| Email and templates | SMTP/provider outbound, templated email for workflow, collections, dunning, portals | 4 | Workflow notifications in 4 need it; collections (30) reuses | Small |
| Document management depth | Document types, storage backends (S3-compatible), archive/retention, document routing to records | 5 | Attachments exist in 5/11; policy layer missing | Medium |
| Reason codes framework | Shared financial reason codes (ledger, bank, customer, vendor, FA, counting) | 11 | First consumer is journal reversal in 11 | Small |
| Policy framework and signing limits | Reusable org-hierarchy-scoped policy engine (purchasing, expense, audit, signing limits) and signing-limit requests | 17 | First consumer is purchasing policy in 17; expense (43) and audit (48) reuse | Medium |
| Procurement catalogs and punchout | Internal procurement catalog, vendor catalog import (CMR), external catalogs via cXML/OCI punchout | 17 / 20 / 35 | Catalog in 17, vendor upload via portal 20, punchout through integration hub 35 | Medium |
| RFQ depth | Sealed bids, scoring criteria, amendments | 17 | Extends RFQ cycle | Small |
| Vendor master depth | Vendor requests/onboarding workflow, certifications and diversity classifications, vendor catalog | 7 / 20 | Party governance (7) and supplier portal (20) | Small |
| Payments depth | ACH prenotes, payment calendars (bank holidays), centralized cross-entity payments | 19 / 40 | Payment media in 19; centralized payments need intercompany due-to/due-from (40) | Small / Medium |
| Treasury instruments | Customer bills of exchange, vendor promissory notes, postdated checks (draw/remit/protest/settle journals) | 36 | Cash and bank wave owns instruments; AR/AP journals already exist | **(wave-splitting candidate)** |
| Direct debit mandates | SEPA mandates (pain.008), mandate lifecycle | 30 | Cash application wave; ISO 20022 generator from 19 | Small |
| Rebates, brokers, royalties, commissions | Vendor rebates, broker contracts, royalty agreements, commission posting, TAM funds, promotion planning, deductions workbench, supplementary items, commodity pricing | 26 / 30 | Pricing engine (26) owns agreements/accruals; settlement and deductions in 30 | **(wave-splitting candidate)** |
| Process manufacturing depth | Consolidated batch orders, rework batch orders, batch merge, attribute/shelf-life inheritance, same-batch reservation, customer sellable days, lean subcontracting | 24 | Process/lean wave | Medium |
| Planning depth | Supply schedule workbench, inventory forecast, priority-based (DDMRP-style) planning, intercompany master planning | 22 / 40 | Planning engine in 22; intercompany in 40 | Medium |
| Production order split/merge | Split a production order quantity, merge | 21 | Lifecycle state machine owner | Small |
| Product master depth | Product lifecycle states, related products, dimension/model-group conversion jobs, packaging materials/fees | 8 / 28 | Product master and fulfillment | Small |
| Engineering change depth | Engineering organization and release product structure to LEs, readiness checks/policies, engineering categories and version nomenclature, PLM connector | 9 / 35 | ECM wave; PLM connector via integration hub | Medium |
| Product compliance | Regulated/restricted lists, safety data sheets with delivery rules, reporting thresholds | 33 | Quality wave is the natural home (documents on shipment, blocking) | Medium |
| Intrastat and VAT recovery | Intrastat setup (commodity/transaction codes, transport methods, ports), Intrastat journal corrections, VAT recovery on expenses | 13 | Tax engine wave | Small |
| Financial reporting hierarchies | Derived financial hierarchies, advanced rule structures, default descriptions | 11 / 15 | Journal and reporting waves | Small |
| Cost accounting policies | Cost behavior (fixed/variable) and cost rollup policies | 38 | Cost accounting ledger wave | Small |
| Budget planning depth | Excel worksheet round-trip, position forecasting from HR, plan generation from demand forecast/FA | 38 (+42 dependency) | Position forecasting needs HR (42): deliver stub in 38, complete in 42 | Medium |
| Costing extras | Standard cost conversion checklist, parallel inventory valuation ledgers (Global inventory accounting) | 39 / 20 | Costing waves | Medium |
| Landed cost (D365 module) | Voyages, shipping containers, folios, goods-in-transit orders, cost estimates vs actuals | 34 | Transportation wave owns landed cost allocation | **(wave-splitting candidate)** |
| Yard and gate | Driver check-in/out, yard management (trailers, yard moves, dock assignment) | 34 | TMS wave with dock scheduling from 31 | Medium |
| WHS extras | Cross docking (planned/opportunistic), outbound sorting | 31 | Advanced warehouse wave | Small |
| Fixed asset extras | Asset additions (components), insurance records | 37 | Fixed assets wave | Small |
| Intercompany depth | Intercompany expenses, intercompany project resourcing/invoicing, intercompany on-hand visibility, centralized payments, intercompany planning | 40 | Intercompany wave | Medium |
| Project depth | Project quotations, weekly timesheets with workflow, retention (retainage) terms, project indirect cost components, beginning balance journals (49) | 41 / 49 | Project wave; migration pack for opening WIP | Medium |
| HR extras | Courses/learning, questionnaire engine, injury and illness (OSHA), loaned items, skill mapping, onboarding checklists, teams (6), US compliance specifics (48) | 42 (+6, +48) | HR wave; questionnaire engine reused by performance reviews and quality | **(wave-splitting candidate)** |
| Expense extras | Cash advances, travel requisitions, deduction arrears recovery (payroll), workers' compensation and new-hire reporting (48) | 43 (+48) | Payroll/expense wave; statutory outputs via US pack | Medium |
| EAM extras | Maintenance rounds and sequences, fault designer, asset warranties, maintenance budgets | 44 | Asset management wave | Medium |
| Service subscriptions | AX service subscription fees with indexation and accrual | 29 (+45) | Subscription billing engine in 29 is the implementation; service wave links agreements | Small |
| Generic case management | Case categories/processes/activities/knowledge articles on any record (HR, collections, product change, audit, service) | 45 | Service wave builds cases; earlier consumers (30, 9) use a minimal case entity until then — flag as risk | Medium |
| Compliance extras | Audit policies (rule-based post-hoc audit of expenses/invoices/POs/journals), process/risk library, electronic signatures (21 CFR Part 11), electronic messaging lifecycle, India TDS/TCS depth, license usage reporting (3) | 48 (+3) | Compliance wave; electronic signatures are a prerequisite for pharma/food manufacturers and should be pulled earlier if the first customer is regulated | **(wave-splitting candidate)** |
| Print management routing | Conditional print destinations per document/account/query | 46 | Document designer wave | Small |

### 12.99.2 Proposed load by wave

| Proposed wave | GAP rows | PARTIAL rows | Total |
|---|---|---|---|
| 3 | 0 | 3 | 3 |
| 4 | 0 | 6 | 6 |
| 5 | 7 | 8 | 15 |
| 6 | 0 | 4 | 4 |
| 7 | 0 | 2 | 2 |
| 8 | 0 | 7 | 7 |
| 9 | 0 | 16 | 16 |
| 11 | 0 | 10 | 10 |
| 13 | 0 | 8 | 8 |
| 15 | 0 | 1 | 1 |
| 16 | 9 | 15 | 24 |
| 17 | 0 | 16 | 16 |
| 19 | 0 | 6 | 6 |
| 20 | 2 | 11 | 13 |
| 21 | 0 | 1 | 1 |
| 22 | 0 | 4 | 4 |
| 24 | 0 | 16 | 16 |
| 26 | 19 | 24 | 43 |
| 27 | 0 | 3 | 3 |
| 28 | 0 | 5 | 5 |
| 29 | 0 | 7 | 7 |
| 30 | 0 | 11 | 11 |
| 31 | 0 | 6 | 6 |
| 33 | 12 | 0 | 12 |
| 34 | 6 | 8 | 14 |
| 35 | 1 | 3 | 4 |
| 36 | 16 | 0 | 16 |
| 37 | 0 | 4 | 4 |
| 38 | 0 | 6 | 6 |
| 39 | 0 | 2 | 2 |
| 40 | 0 | 10 | 10 |
| 41 | 0 | 12 | 12 |
| 42 | 24 | 4 | 28 |
| 43 | 0 | 10 | 10 |
| 44 | 0 | 15 | 15 |
| 45 | 0 | 11 | 11 |
| 46 | 0 | 7 | 7 |
| 48 | 12 | 34 | 46 |
| 49 | 0 | 1 | 1 |
| **Total** | **108** | **317** | **425** |

### 12.99.3 GAP rows (no wave covers the item)

| # | Module | Table | Item | Status | Missing / proposal | Proposed wave |
|---|---|---|---|---|---|---|
| 1 | 12.2 Accounts payable | 12.2.1 Common / Journals | Payments → Promissory note journals (Draw / Redraw / Remittance / Settle) | GAP | Propose wave 36: promissory notes / bills of exchange (vendor side) with remittance and settlement journals; not in any wave | 36 |
| 2 | 12.2 Accounts payable | 12.2.1 Common / Journals | Vendor → Consignment (D365 consignment replenishment orders) | GAP | Propose wave 16: consignment inventory (vendor-owned on-hand, ownership-change journal, consignment replenishment orders) | 16 |
| 3 | 12.2 Accounts payable | 12.2.2 Inquiries & Reports | SSRS: Promissory note (vendor) documents | GAP | Propose wave 36: with promissory notes | 36 |
| 4 | 12.2 Accounts payable | 12.2.3 Periodic | Promissory notes → Draw / Redraw / Remit / Settle (periodic batches) | GAP | Propose wave 36: promissory notes | 36 |
| 5 | 12.2 Accounts payable | 12.2.3 Periodic | Consignment → Consignment replenishment order processing | GAP | Propose wave 16: consignment | 16 |
| 6 | 12.2 Accounts payable | 12.2.4 Setup | Payment → Promissory note setup (journal names, posting profiles, protest) | GAP | Propose wave 36: promissory notes | 36 |
| 7 | 12.2 Accounts payable | 12.2.4 Setup | Vendor → Vendor consignment setup (D365 consignment parameters, ownership change journal names) | GAP | Propose wave 16: consignment | 16 |
| 8 | 12.3 Accounts receivable | 12.3.1 Common / Journals | Payments → Bill of exchange journals (Draw / Redraw / Protest / Remittance / Settle) | GAP | Propose wave 36: bills of exchange (customer) with remittance for collection/discount, protest, settle; not in any wave | 36 |
| 9 | 12.3 Accounts receivable | 12.3.1 Common / Journals | Payments → Postdated checks (R3 APAC) | GAP | Propose wave 36: postdated checks (customer and vendor) as a treasury instrument | 36 |
| 10 | 12.3 Accounts receivable | 12.3.2 Inquiries & Reports | Bill of exchange inquiry | GAP | Propose wave 36: bills of exchange | 36 |
| 11 | 12.3 Accounts receivable | 12.3.2 Inquiries & Reports | Postdated check inquiry | GAP | Propose wave 36: postdated checks | 36 |
| 12 | 12.3 Accounts receivable | 12.3.2 Inquiries & Reports | SSRS: Bill of exchange documents / Remittance list | GAP | Propose wave 36: bills of exchange | 36 |
| 13 | 12.3 Accounts receivable | 12.3.3 Periodic | Payments → Bill of exchange remittance/protest batches | GAP | Propose wave 36: bills of exchange | 36 |
| 14 | 12.3 Accounts receivable | 12.3.4 Setup | Payment → Bill of exchange setup (journal names, posting profiles, protest reasons, remittance types) | GAP | Propose wave 36: bills of exchange | 36 |
| 15 | 12.3 Accounts receivable | 12.3.4 Setup | Payment → Postdated checks setup (R3 APAC) | GAP | Propose wave 36: postdated checks | 36 |
| 16 | 12.5 Cash and bank management | 12.5.1 Common / Journals | Bank → Bills of exchange / Promissory notes (links) | GAP | Propose wave 36: bills of exchange and promissory notes (see AR/AP) | 36 |
| 17 | 12.5 Cash and bank management | 12.5.1 Common / Journals | Bank → Postdated checks (links) | GAP | Propose wave 36: postdated checks | 36 |
| 18 | 12.5 Cash and bank management | 12.5.4 Setup | Bills of exchange / promissory notes setup (bank side) | GAP | Propose wave 36: bills of exchange | 36 |
| 19 | 12.5 Cash and bank management | 12.5.4 Setup | Postdated checks parameters (R3 APAC) | GAP | Propose wave 36: postdated checks | 36 |
| 20 | 12.8 Cost management (D365) | 12.8.1 Common / Journals | Global inventory accounting (D365 add-in: multiple cost ledgers / accounting conventions) | GAP | Propose wave 20: parallel inventory valuation ledgers (one costing method per ledger group) aligned with wave 12 parallel ledgers | 20 |
| 21 | 12.8 Cost management (D365) | 12.8.4 Setup | Global inventory accounting setup (ledgers, accounting policies, conventions) | GAP | Propose wave 20: parallel inventory valuation ledgers | 20 |
| 22 | 12.9 Compliance and internal controls | 12.9.1 Common / Journals | Compliance → Electronic signatures (link) | GAP | Propose wave 48: electronic signatures (21 CFR Part 11 style) on configured tables/fields with reason codes and signature log | 48 |
| 23 | 12.9 Compliance and internal controls | 12.9.2 Inquiries & Reports | Electronic signature log inquiry | GAP | Propose wave 48: electronic signatures | 48 |
| 24 | 12.9 Compliance and internal controls | 12.9.2 Inquiries & Reports | SSRS: Electronic signature report | GAP | Propose wave 48: electronic signatures | 48 |
| 25 | 12.9 Compliance and internal controls | 12.9.3 Periodic | Electronic signature → Reset/revoke signatures | GAP | Propose wave 48: electronic signatures | 48 |
| 26 | 12.9 Compliance and internal controls | 12.9.4 Setup | Electronic signature → Electronic signature parameters, Signature requirements (table/field), Reason codes for signatures | GAP | Propose wave 48: electronic signatures | 48 |
| 27 | 12.12 Human resources | 12.12.1 Common / Journals | Courses (training) → Courses, Course sessions, Course registrations, Instructors, Course locations | GAP | Propose wave 42: learning/training management (courses, sessions, registrations, instructors, completion to competencies) | 42 |
| 28 | 12.12 Human resources | 12.12.1 Common / Journals | Questionnaires → Questionnaires, Questionnaire schedules, Answer sessions, Results analysis | GAP | Propose wave 42: questionnaire engine (design, schedule, answer, analyze) reused by performance reviews and quality audits | 42 |
| 29 | 12.12 Human resources | 12.12.1 Common / Journals | Injury and illness → Injury/illness incidents, Treatments, Body parts, OSHA logs | GAP | Propose wave 42: injury and illness incident register with OSHA 300 extracts (US pack) | 42 |
| 30 | 12.12 Human resources | 12.12.1 Common / Journals | Loaned items → Loaned items, Loan items | GAP | Propose wave 42: loaned items (assets issued to workers) tied to EAM assets wave 44 | 42 |
| 31 | 12.12 Human resources | 12.12.2 Inquiries & Reports | Course inquiries | GAP | Propose wave 42: learning management | 42 |
| 32 | 12.12 Human resources | 12.12.2 Inquiries & Reports | Questionnaire results analysis | GAP | Propose wave 42: questionnaire engine | 42 |
| 33 | 12.12 Human resources | 12.12.2 Inquiries & Reports | Injury/illness inquiries | GAP | Propose wave 42: injury and illness | 42 |
| 34 | 12.12 Human resources | 12.12.2 Inquiries & Reports | Loaned items inquiry | GAP | Propose wave 42: loaned items | 42 |
| 35 | 12.12 Human resources | 12.12.2 Inquiries & Reports | SSRS: Course reports (Attendance, Course confirmation letters) | GAP | Propose wave 42: learning management | 42 |
| 36 | 12.12 Human resources | 12.12.2 Inquiries & Reports | SSRS: Questionnaire reports | GAP | Propose wave 42: questionnaire engine | 42 |
| 37 | 12.12 Human resources | 12.12.2 Inquiries & Reports | SSRS: OSHA 300/300A/301 (US) | GAP | Propose wave 42: injury and illness (US pack output wave 48) | 42 |
| 38 | 12.12 Human resources | 12.12.3 Periodic | Courses → Course confirmations / Transfer course completion to competencies | GAP | Propose wave 42: learning management | 42 |
| 39 | 12.12 Human resources | 12.12.3 Periodic | Questionnaires → Distribute questionnaires (schedule) / Collect results | GAP | Propose wave 42: questionnaire engine | 42 |
| 40 | 12.12 Human resources | 12.12.4 Setup | Courses → Course types, Course groups, Course locations, Classrooms, Instructors, Agenda templates | GAP | Propose wave 42: learning management | 42 |
| 41 | 12.12 Human resources | 12.12.4 Setup | Questionnaires → Questionnaire types, Question types, Answer groups, Result groups, Media types, Questionnaire parameters | GAP | Propose wave 42: questionnaire engine | 42 |
| 42 | 12.12 Human resources | 12.12.4 Setup | Injury and illness → Incident types, Body parts, Injury/illness treatments, Severity levels, Outcomes | GAP | Propose wave 42: injury and illness | 42 |
| 43 | 12.12 Human resources | 12.12.4 Setup | Loaned items → Loan types | GAP | Propose wave 42: loaned items | 42 |
| 44 | 12.14 Inventory management | 12.14.1 Common / Journals | Journals → Inventory ownership change journal (D365 consignment) | GAP | Propose wave 16: consignment | 16 |
| 45 | 12.14 Inventory management | 12.14.2 Inquiries & Reports | SSRS: Consignment inventory reports (D365) | GAP | Propose wave 16: consignment | 16 |
| 46 | 12.14 Inventory management | 12.14.3 Periodic | Inventory → Consignment: ownership change batch | GAP | Propose wave 16: consignment | 16 |
| 47 | 12.14 Inventory management | 12.14.4 Setup | Inventory → Consignment → Consignment parameters (D365) | GAP | Propose wave 16: consignment | 16 |
| 48 | 12.15 Product information management | 12.15.1 Common / Journals | Released product → Product compliance (PI: Regulated products, Restricted lists, Reporting details, Safety data sheets (MSDS/SDS) with validity and send on sales) | GAP | Propose wave 33: product compliance (regulated/restricted lists by country/state, SDS versions with shipment delivery rules, reporting thresholds) | 33 |
| 49 | 12.15 Product information management | 12.15.2 Inquiries & Reports | Product compliance inquiries (Regulated items, SDS due) | GAP | Propose wave 33: product compliance | 33 |
| 50 | 12.15 Product information management | 12.15.2 Inquiries & Reports | SSRS: Safety data sheet (SDS document, PI) | GAP | Propose wave 33: product compliance | 33 |
| 51 | 12.15 Product information management | 12.15.3 Periodic | Product compliance → Expire SDS / Reporting (periodic) | GAP | Propose wave 33: product compliance | 33 |
| 52 | 12.15 Product information management | 12.15.4 Setup | Product → Product compliance setup (PI: Regulated regions, Restricted lists, Reporting lists, SDS parameters, Product safety data sheet validity, Item-specific: MSDS validity days) | GAP | Propose wave 33: product compliance | 33 |
| 53 | 12.16 Procurement and sourcing | 12.16.1 Common / Journals | External catalogs (punchout cXML/OCI) | GAP | Propose wave 35: external catalog punchout (cXML/OCI) via integration hub, requisition lines returned to wave 17 | 35 |
| 54 | 12.16 Procurement and sourcing | 12.16.1 Common / Journals | Vendor rebates (PI vendor rebate agreements, R3) | GAP | Propose wave 26: vendor rebates (agreements, accrual on purchase, claim/credit note settlement via AP) mirroring customer rebates | 26 |
| 55 | 12.16 Procurement and sourcing | 12.16.1 Common / Journals | Broker contracts (PI brokers and royalties, R3) | GAP | Propose wave 26: broker contracts (commission to broker vendor on sales invoices, claims via AP) | 26 |
| 56 | 12.16 Procurement and sourcing | 12.16.1 Common / Journals | Royalty agreements (PI, R3) | GAP | Propose wave 26: royalty agreements (accrual on sales, claim to vendor) | 26 |
| 57 | 12.16 Procurement and sourcing | 12.16.1 Common / Journals | Consignment (link) | GAP | Propose wave 16: consignment | 16 |
| 58 | 12.16 Procurement and sourcing | 12.16.2 Inquiries & Reports | Vendor rebate inquiries | GAP | Propose wave 26: vendor rebates | 26 |
| 59 | 12.16 Procurement and sourcing | 12.16.2 Inquiries & Reports | Broker/royalty claim inquiries | GAP | Propose wave 26: broker contracts | 26 |
| 60 | 12.16 Procurement and sourcing | 12.16.2 Inquiries & Reports | SSRS: Vendor rebate reports | GAP | Propose wave 26: vendor rebates | 26 |
| 61 | 12.16 Procurement and sourcing | 12.16.3 Periodic | Vendor rebates → Cumulate / Approve / Process claims | GAP | Propose wave 26: vendor rebates | 26 |
| 62 | 12.16 Procurement and sourcing | 12.16.3 Periodic | Broker → Process broker claims / Royalty → Process royalty claims | GAP | Propose wave 26: broker contracts | 26 |
| 63 | 12.16 Procurement and sourcing | 12.16.4 Setup | Vendor rebates → Rebate agreements, Rebate program types, Rebate groups (vendor), Rebate claim journal names | GAP | Propose wave 26: vendor rebates | 26 |
| 64 | 12.16 Procurement and sourcing | 12.16.4 Setup | Brokers and royalties → Broker contract setup, Royalty agreement setup, Claim journal names | GAP | Propose wave 26: broker contracts | 26 |
| 65 | 12.23 Process manufacturing (PI) | 12.23.1 Common / Journals | Vendor rebates / Brokers / Royalties (links) | GAP | Propose wave 26: vendor rebates (see §12.16) | 26 |
| 66 | 12.23 Process manufacturing (PI) | 12.23.1 Common / Journals | Product compliance / Safety data sheets (link) | GAP | Propose wave 33: product compliance (see §12.15) | 33 |
| 67 | 12.23 Process manufacturing (PI) | 12.23.2 Inquiries & Reports | SSRS: Safety data sheet (link) | GAP | Propose wave 33: product compliance | 33 |
| 68 | 12.23 Process manufacturing (PI) | 12.23.3 Periodic | Compliance → SDS expiry / regulated product reporting | GAP | Propose wave 33: product compliance | 33 |
| 69 | 12.23 Process manufacturing (PI) | 12.23.3 Periodic | Vendor rebates → Process | GAP | Propose wave 26: vendor rebates | 26 |
| 70 | 12.23 Process manufacturing (PI) | 12.23.4 Setup | Product compliance → Regulated/restricted lists, Reporting lists, SDS setup | GAP | Propose wave 33: product compliance | 33 |
| 71 | 12.23 Process manufacturing (PI) | 12.23.4 Setup | Vendor rebates / Brokers / Royalties setup | GAP | Propose wave 26: vendor rebates | 26 |
| 72 | 12.25 Quality management | 12.25.4 Setup | Quality management → Product compliance (PI, link) | GAP | Propose wave 33: product compliance | 33 |
| 73 | 12.26 Warehouse management (WHS) | 12.26.4 Setup | Warehouse → Yard management (D365 2024: yard, trailers, yard moves) | GAP | Propose wave 34: yard management (trailer/yard locations, check-in/out, yard moves, dock door assignment) alongside TMS driver check-in | 34 |
| 74 | 12.27 Transportation management (TMS) | 12.27.1 Common / Journals | Driver check-in / check-out | GAP | Propose wave 34: driver check-in/out with appointment link, trailer/seal capture, gate log (with yard management) | 34 |
| 75 | 12.27 Transportation management (TMS) | 12.27.2 Inquiries & Reports | Driver check-in log | GAP | Propose wave 34: driver check-in | 34 |
| 76 | 12.27 Transportation management (TMS) | 12.27.3 Periodic | Driver check-in → Expire appointments / Gate log archive | GAP | Propose wave 34: driver check-in | 34 |
| 77 | 12.27 Transportation management (TMS) | 12.27.4 Setup | Driver check-in → Check-in setup / Gate configuration | GAP | Propose wave 34: driver check-in | 34 |
| 78 | 12.27 Transportation management (TMS) | 12.27.4 Setup | Transportation → Yard management setup (D365 2024) | GAP | Propose wave 34: yard management | 34 |
| 79 | 12.29 Organization administration | 12.29.1 Common / Journals | Electronic signatures → Signature log / Reset signature | GAP | Propose wave 48: electronic signatures | 48 |
| 80 | 12.29 Organization administration | 12.29.1 Common / Journals | Product safety data sheets (link, PI) | GAP | Propose wave 33: product compliance | 33 |
| 81 | 12.29 Organization administration | 12.29.1 Common / Journals | Electronic signatures → Certificates (user signing certificates) | GAP | Propose wave 48: electronic signatures | 48 |
| 82 | 12.29 Organization administration | 12.29.1 Common / Journals | Alerts (org-level alert rules) | GAP | Propose wave 5: alerts and notification rules (change-based and due-date alerts on any entity/field, delivered in-app/email/webhook) built on the wave 4 event backbone | 5 |
| 83 | 12.29 Organization administration | 12.29.1 Common / Journals | Questionnaires (link, Org admin > Questionnaires in R3) | GAP | Propose wave 42: questionnaire engine | 42 |
| 84 | 12.29 Organization administration | 12.29.2 Inquiries & Reports | Electronic signature inquiry | GAP | Propose wave 48: electronic signatures | 48 |
| 85 | 12.29 Organization administration | 12.29.3 Periodic | Electronic signatures → Signature verification / Reset | GAP | Propose wave 48: electronic signatures | 48 |
| 86 | 12.29 Organization administration | 12.29.4 Setup | Electronic signatures → Electronic signature parameters / Signature requirements / Reason codes / Certificates | GAP | Propose wave 48: electronic signatures | 48 |
| 87 | 12.29 Organization administration | 12.29.4 Setup | Product safety data sheets setup (link, PI) | GAP | Propose wave 33: product compliance | 33 |
| 88 | 12.29 Organization administration | 12.29.4 Setup | Alerts → Alert rules (Change-based, Due-date; event rule setup, alert email templates) | GAP | Propose wave 5: alerts and notification rules | 5 |
| 89 | 12.29 Organization administration | 12.29.4 Setup | Questionnaires setup (link) | GAP | Propose wave 42: questionnaire engine | 42 |
| 90 | 12.30 System administration | 12.30.1 Common / Journals | Sys admin → Electronic signature (link) | GAP | Propose wave 48: electronic signatures | 48 |
| 91 | 12.30 System administration | 12.30.3 Periodic | Alerts → Change-based alerts / Due date alerts (batch processors) | GAP | Propose wave 5: alerts and notification rules | 5 |
| 92 | 12.30 System administration | 12.30.4 Setup | Sys admin → Electronic signature parameters (link) | GAP | Propose wave 48: electronic signatures | 48 |
| 93 | 12.31 Home / role centers / workflow | 12.31.1 Common / Journals | Questionnaires → My questionnaires / Answer questionnaire / Questionnaire results | GAP | Propose wave 42: questionnaire engine | 42 |
| 94 | 12.31 Home / role centers / workflow | 12.31.1 Common / Journals | Alerts (My alerts / Notification list) | GAP | Propose wave 5: alerts and notification rules (notification center exists in shell; rule-based alerts do not) | 5 |
| 95 | 12.31 Home / role centers / workflow | 12.31.2 Inquiries & Reports | Alert inquiry (alerts by rule/user) | GAP | Propose wave 5: alerts and notification rules | 5 |
| 96 | 12.31 Home / role centers / workflow | 12.31.2 Inquiries & Reports | Questionnaire statistics / Results analysis | GAP | Propose wave 42: questionnaire engine | 42 |
| 97 | 12.31 Home / role centers / workflow | 12.31.2 Inquiries & Reports | SSRS: Questionnaire reports (link) | GAP | Propose wave 42: questionnaire engine | 42 |
| 98 | 12.31 Home / role centers / workflow | 12.31.3 Periodic | Alerts → Process change-based alerts / Due-date alerts | GAP | Propose wave 5: alerts and notification rules | 5 |
| 99 | 12.31 Home / role centers / workflow | 12.31.3 Periodic | Questionnaires → Schedule questionnaires (batch) | GAP | Propose wave 42: questionnaire engine | 42 |
| 100 | 12.31 Home / role centers / workflow | 12.31.4 Setup | Alerts → Alert rules (create alert rule on field/record: changes to, due date; subject/message; recipients; organization-wide) / Alert rule templates | GAP | Propose wave 5: alerts and notification rules | 5 |
| 101 | 12.31 Home / role centers / workflow | 12.31.4 Setup | Questionnaires → Questionnaire setup (link) | GAP | Propose wave 42: questionnaire engine | 42 |
| 102 | 12.36 Rebate management (D365) | 12.36.1 Common / Journals | Deal → Vendor deals (purchase rebates) | GAP | Propose wave 26: vendor rebates | 26 |
| 103 | 12.36 Rebate management (D365) | 12.36.1 Common / Journals | Deal → Price-based deals (royalty-type) | GAP | Propose wave 26: royalty agreements | 26 |
| 104 | 12.36 Rebate management (D365) | 12.36.2 Inquiries & Reports | Vendor deal inquiries | GAP | Propose wave 26: vendor rebates | 26 |
| 105 | 12.36 Rebate management (D365) | 12.36.2 Inquiries & Reports | SSRS: Vendor rebate claim | GAP | Propose wave 26: vendor rebates | 26 |
| 106 | 12.36 Rebate management (D365) | 12.36.3 Periodic | Vendor rebate claims processing | GAP | Propose wave 26: vendor rebates | 26 |
| 107 | 12.36 Rebate management (D365) | 12.36.4 Setup | Vendor deal setup (claim via AP) | GAP | Propose wave 26: vendor rebates | 26 |
| 108 | 12.39 Vendor collaboration / customer self-service (D365) | 12.39.1 Common / Journals | Vendor → Consignment inventory view (on-hand, replenishment orders) | GAP | Propose wave 16: consignment | 16 |

### 12.99.4 PARTIAL rows (wave covers the core; named piece missing)

| # | Module | Table | Item | Status | Missing / proposal | Proposed wave |
|---|---|---|---|---|---|---|
| 1 | 12.1 General ledger | 12.1.1 Common / Journals | Voucher attachments / document handling | PARTIAL | no document types, archiving policy or external store yet | 11 |
| 2 | 12.1 General ledger | 12.1.3 Periodic | Reason code assignment (reversal reasons) | PARTIAL | reason codes as a shared framework are not named in any wave | 11 |
| 3 | 12.1 General ledger | 12.1.4 Setup | Chart of accounts → Advanced rule structures / Advanced rules | PARTIAL | advanced rules (conditional extra segments) are not explicitly listed; account structures are | 11 |
| 4 | 12.1 General ledger | 12.1.4 Setup | Chart of accounts → Derived financial hierarchies (R3) | PARTIAL | derived hierarchies for reporting are not named | 15 |
| 5 | 12.1 General ledger | 12.1.4 Setup | Journals → Default descriptions (Transaction text) | PARTIAL | default description templates per posting type not named | 11 |
| 6 | 12.1 General ledger | 12.1.4 Setup | Posting → Reason codes (financial reasons) | PARTIAL | shared reason code framework not named | 11 |
| 7 | 12.1 General ledger | 12.1.4 Setup | Foreign trade → Intrastat parameters, commodity codes, transaction codes, transport methods, ports, statistics procedure | PARTIAL | Intrastat extract is named; the setup forms (commodity codes, transaction codes, transport methods, ports, country properties) are not | 13 |
| 8 | 12.1 General ledger | 12.1.4 Setup | Tax (IN) → India-specific setup (tax components, TDS/TCS) | PARTIAL | India pack listed for wave 48 but TDS/TCS/GST depth unspecified | 48 |
| 9 | 12.2 Accounts payable | 12.2.1 Common / Journals | Vendor requests (R3) | PARTIAL | vendor request workflow and prospective-vendor self-registration are not named; supplier portal is | 20 |
| 10 | 12.2 Accounts payable | 12.2.1 Common / Journals | Payments → Prenotes (ACH) | PARTIAL | ACH prenote (zero-dollar validation) flow is not named | 19 |
| 11 | 12.2 Accounts payable | 12.2.1 Common / Journals | Payments → Centralized payments (cross-company) | PARTIAL | centralized payments across entities are not named in 19 or 40 | 40 |
| 12 | 12.2 Accounts payable | 12.2.3 Periodic | Invoice → Vendor invoice policy violations (R3 audit) | PARTIAL | audit policies are not named; SoD engine is | 48 |
| 13 | 12.2 Accounts payable | 12.2.3 Periodic | Vendor → Update vendor bank accounts status (prenote results) | PARTIAL | prenote | 19 |
| 14 | 12.2 Accounts payable | 12.2.4 Setup | Vendors → Vendor request types (R3) | PARTIAL | vendor request workflow | 20 |
| 15 | 12.2 Accounts payable | 12.2.4 Setup | Payment → Payment calendar (D365) | PARTIAL | payment calendar / bank holiday due-date shifting is not named | 19 |
| 16 | 12.2 Accounts payable | 12.2.4 Setup | Invoice → Vendor invoice policies (R3 audit policy) | PARTIAL | audit policies | 48 |
| 17 | 12.2 Accounts payable | 12.2.4 Setup | Forms → Print management | PARTIAL | print management conditional routing per account is not named; document designer is | 46 |
| 18 | 12.3 Accounts receivable | 12.3.1 Common / Journals | Customer → Direct debit mandates (SEPA, R3) | PARTIAL | SEPA direct debit mandates (pain.008) are not named; collections and cash application are | 30 |
| 19 | 12.3 Accounts receivable | 12.3.1 Common / Journals | Collections → Cases (collection cases) | PARTIAL | disputes workflow named; generic case management not | 30 |
| 20 | 12.3 Accounts receivable | 12.3.1 Common / Journals | Commission transactions | PARTIAL | commission calculation hooks are named, but commission posting to vendor/employee and transactions are not | 26 |
| 21 | 12.3 Accounts receivable | 12.3.2 Inquiries & Reports | Commission inquiry | PARTIAL | commission posting | 26 |
| 22 | 12.3 Accounts receivable | 12.3.2 Inquiries & Reports | Direct debit mandate inquiry | PARTIAL | direct debit | 30 |
| 23 | 12.3 Accounts receivable | 12.3.2 Inquiries & Reports | SSRS: Commission report / Commission transactions | PARTIAL | commission posting | 26 |
| 24 | 12.3 Accounts receivable | 12.3.3 Periodic | Payments → Generate direct debit (pain.008) | PARTIAL | direct debit | 30 |
| 25 | 12.3 Accounts receivable | 12.3.3 Periodic | Commission → Calculate/post commissions (via invoice) | PARTIAL | commission posting | 26 |
| 26 | 12.3 Accounts receivable | 12.3.4 Setup | Payment → Payment calendar (D365) | PARTIAL | payment calendar | 19 |
| 27 | 12.3 Accounts receivable | 12.3.4 Setup | Payment → Direct debit mandate setup (SEPA creditor identifier) | PARTIAL | direct debit | 30 |
| 28 | 12.3 Accounts receivable | 12.3.4 Setup | Collections → Collections email templates (D365) | PARTIAL | email templates for dunning/collections communication not named; letters are | 30 |
| 29 | 12.3 Accounts receivable | 12.3.4 Setup | Commission → Commission calculation / Commission sales groups / Commission customer groups / Commission item groups / Commission posting | PARTIAL | commission posting and sales group/rep assignment | 26 |
| 30 | 12.3 Accounts receivable | 12.3.4 Setup | Forms → Form setup (sales documents) / Form notes / Print management | PARTIAL | print management routing | 46 |
| 31 | 12.3 Accounts receivable | 12.3.4 Setup | Trade agreements → Supplementary items | PARTIAL | supplementary items are not named in pricing or order waves | 27 |
| 32 | 12.3 Accounts receivable | 12.3.4 Setup | Sales → Sales reasons / Reason codes (customer) | PARTIAL | reason codes | 11 |
| 33 | 12.3 Accounts receivable | 12.3.4 Setup | Sales → Sales districts / Sales groups (sales reps) | PARTIAL | sales groups/reps for commission | 26 |
| 34 | 12.4 Budgeting | 12.4.1 Common / Journals | Budget plan → Budget plan templates / worksheets | PARTIAL | Excel worksheet round-trip for budget plans is not named; planning workflow and scenarios are | 38 |
| 35 | 12.4 Budgeting | 12.4.3 Periodic | Budget planning → Generate budget plan from source (GL actuals, previous plan, FA, projects, demand forecast, HR positions) | PARTIAL | generating plans from position/HR forecasts and demand forecasts is not named | 38 |
| 36 | 12.4 Budgeting | 12.4.4 Setup | Budget planning → Budget planning templates (Excel) | PARTIAL | Excel worksheets | 38 |
| 37 | 12.4 Budgeting | 12.4.4 Setup | Budget planning → Position forecasting (D365 HR-driven) | PARTIAL | position forecasting | 38 |
| 38 | 12.5 Cash and bank management | 12.5.4 Setup | Bank account → Prenote setup | PARTIAL | prenote | 19 |
| 39 | 12.5 Cash and bank management | 12.5.4 Setup | Bank → Bank holidays / Payment calendar | PARTIAL | payment calendar | 19 |
| 40 | 12.6 Fixed assets | 12.6.1 Common / Journals | Fixed asset → Fixed asset additions | PARTIAL | asset additions (non-financial component records) not named | 37 |
| 41 | 12.6 Fixed assets | 12.6.1 Common / Journals | Fixed asset → Insurance records | PARTIAL | insurance records not named | 37 |
| 42 | 12.6 Fixed assets | 12.6.2 Inquiries & Reports | SSRS: Fixed asset insurance report | PARTIAL | insurance | 37 |
| 43 | 12.6 Fixed assets | 12.6.4 Setup | Fixed asset → Insurance types / Insurance companies | PARTIAL | insurance | 37 |
| 44 | 12.6 Fixed assets | 12.6.4 Setup | Fixed asset → Reason codes (fixed assets) | PARTIAL | reason codes | 11 |
| 45 | 12.7 Cost accounting | 12.7.1 Common / Journals | Cost behavior / Cost classification (fixed/variable) (D365) | PARTIAL | cost behavior (fixed/variable split) policies not named | 38 |
| 46 | 12.7 Cost accounting | 12.7.4 Setup | D365: Policies — Cost behavior policies, Cost distribution policies, Overhead rate policies, Cost allocation policies, Cost rollup policies (D365 2020+) | PARTIAL | cost behavior and cost rollup policies not named; allocation policies are | 38 |
| 47 | 12.8 Cost management (D365) | 12.8.1 Common / Journals | Landed cost (D365 Landed cost module: Voyages, Shipping containers, Folios, Cost estimates, Goods in transit) | PARTIAL | landed cost allocation is named; D365 voyage/container/folio tracking and goods-in-transit orders are not | 34 |
| 48 | 12.8 Cost management (D365) | 12.8.2 Inquiries & Reports | Inventory aging report (D365) | PARTIAL | inventory aging buckets not named | 20 |
| 49 | 12.8 Cost management (D365) | 12.8.2 Inquiries & Reports | Landed cost reports (voyage cost, cost variance by voyage) | PARTIAL | landed cost voyages | 34 |
| 50 | 12.8 Cost management (D365) | 12.8.3 Periodic | Standard cost → Revaluation on activation / Conversion to standard cost (Standard cost conversion checklist) | PARTIAL | standard cost conversion checklist (switching costing model mid-life) not named | 39 |
| 51 | 12.8 Cost management (D365) | 12.8.3 Periodic | Landed cost → Voyage cost allocation / Goods in transit receipts | PARTIAL | landed cost voyages | 34 |
| 52 | 12.8 Cost management (D365) | 12.8.4 Setup | Inventory aging report setup (D365) | PARTIAL | inventory aging | 20 |
| 53 | 12.8 Cost management (D365) | 12.8.4 Setup | Landed cost setup (D365: Landed cost parameters, Cost types, Cost type codes, Auto cost, Apportionment, Shipping container types, Vessels, Voyage templates, Legs, Journeys, Tracking) | PARTIAL | landed cost voyages | 34 |
| 54 | 12.8 Cost management (D365) | 12.8.4 Setup | Standard cost conversion setup | PARTIAL | standard cost conversion | 39 |
| 55 | 12.9 Compliance and internal controls | 12.9.1 Common / Journals | Internal controls → Files (process documents) | PARTIAL | SOC 2 evidence automation is named; a controls/process document library is not | 48 |
| 56 | 12.9 Compliance and internal controls | 12.9.1 Common / Journals | Internal controls → Process library / Process documents | PARTIAL | process library | 48 |
| 57 | 12.9 Compliance and internal controls | 12.9.1 Common / Journals | Internal controls → Risk library | PARTIAL | risk library | 48 |
| 58 | 12.9 Compliance and internal controls | 12.9.1 Common / Journals | Audit → Audit cases | PARTIAL | audit policies | 48 |
| 59 | 12.9 Compliance and internal controls | 12.9.1 Common / Journals | Audit → Audit policy violations list | PARTIAL | audit policies | 48 |
| 60 | 12.9 Compliance and internal controls | 12.9.2 Inquiries & Reports | Audit policy violations inquiry | PARTIAL | audit policies | 48 |
| 61 | 12.9 Compliance and internal controls | 12.9.2 Inquiries & Reports | Audit case inquiry | PARTIAL | audit policies | 48 |
| 62 | 12.9 Compliance and internal controls | 12.9.2 Inquiries & Reports | SSRS: Audit policy report / Audit case report | PARTIAL | audit policies | 48 |
| 63 | 12.9 Compliance and internal controls | 12.9.2 Inquiries & Reports | SSRS: Internal control documents | PARTIAL | process library | 48 |
| 64 | 12.9 Compliance and internal controls | 12.9.3 Periodic | Audit → Run audit policies (batch: expense reports, vendor invoices, purchase orders, GL journals) | PARTIAL | audit policies | 48 |
| 65 | 12.9 Compliance and internal controls | 12.9.3 Periodic | Audit → Close/archive audit cases | PARTIAL | audit policies | 48 |
| 66 | 12.9 Compliance and internal controls | 12.9.4 Setup | Audit → Audit policies (policy framework: policy rule types — expense/vendor invoice/PO/GL journal; rule types: duplicate, keyword, list search, sampling, premium/mileage) | PARTIAL | audit policies | 48 |
| 67 | 12.9 Compliance and internal controls | 12.9.4 Setup | Audit → Audit policy rule types | PARTIAL | audit policies | 48 |
| 68 | 12.9 Compliance and internal controls | 12.9.4 Setup | Audit → Audit case categories | PARTIAL | audit policies | 48 |
| 69 | 12.9 Compliance and internal controls | 12.9.4 Setup | Audit → Audit keyword lists / Audit sampling parameters | PARTIAL | audit policies | 48 |
| 70 | 12.9 Compliance and internal controls | 12.9.4 Setup | Risk/control types, Process document templates | PARTIAL | process library | 48 |
| 71 | 12.10 Travel and expense | 12.10.1 Common / Journals | Cash advances (Cash advance requests) | PARTIAL | cash advances are not named | 43 |
| 72 | 12.10 Travel and expense | 12.10.1 Common / Journals | Travel requisitions | PARTIAL | travel requisitions / pre-approval are not named | 43 |
| 73 | 12.10 Travel and expense | 12.10.1 Common / Journals | Expense reports → Intercompany expenses (R3) | PARTIAL | intercompany expenses not named in 40 or 43 | 40 |
| 74 | 12.10 Travel and expense | 12.10.2 Inquiries & Reports | Cash advance inquiry | PARTIAL | cash advances | 43 |
| 75 | 12.10 Travel and expense | 12.10.2 Inquiries & Reports | Travel requisition inquiry | PARTIAL | travel requisitions | 43 |
| 76 | 12.10 Travel and expense | 12.10.2 Inquiries & Reports | SSRS: Cash advance report | PARTIAL | cash advances | 43 |
| 77 | 12.10 Travel and expense | 12.10.2 Inquiries & Reports | SSRS: Expense audit (audit policy results) | PARTIAL | audit policies | 48 |
| 78 | 12.10 Travel and expense | 12.10.2 Inquiries & Reports | SSRS: VAT recovery report (expense tax) | PARTIAL | VAT recovery on expenses (partial recoverability per country) not named | 13 |
| 79 | 12.10 Travel and expense | 12.10.3 Periodic | Reconcile/settle cash advances | PARTIAL | cash advances | 43 |
| 80 | 12.10 Travel and expense | 12.10.3 Periodic | Expense → Run audit policies on expense reports | PARTIAL | audit policies | 48 |
| 81 | 12.10 Travel and expense | 12.10.3 Periodic | Expense → Recover VAT (ER-based claim) | PARTIAL | VAT recovery | 13 |
| 82 | 12.10 Travel and expense | 12.10.3 Periodic | Expense → Intercompany expense posting (due-to/due-from) | PARTIAL | intercompany expenses | 40 |
| 83 | 12.10 Travel and expense | 12.10.4 Setup | Expense management parameters — Intercompany / Cash advance | PARTIAL | cash advances | 43 |
| 84 | 12.10 Travel and expense | 12.10.4 Setup | Cash advance → Cash advance parameters, posting | PARTIAL | cash advances | 43 |
| 85 | 12.10 Travel and expense | 12.10.4 Setup | Travel requisition → Requisition policies / types | PARTIAL | travel requisitions | 43 |
| 86 | 12.10 Travel and expense | 12.10.4 Setup | Expense → Tax configuration (recoverable VAT per country, tax groups on expenses) | PARTIAL | VAT recovery | 13 |
| 87 | 12.10 Travel and expense | 12.10.4 Setup | Expense → Audit policies (link) | PARTIAL | audit policies | 48 |
| 88 | 12.11 Project management and accounting | 12.11.1 Common / Journals | Project quotations | PARTIAL | project quotations with convert-to-project are not named; sales quotations are in 27 | 41 |
| 89 | 12.11 Project management and accounting | 12.11.1 Common / Journals | Timesheets (list page) | PARTIAL | hour journals are named; weekly timesheet UI with favorites/workflow is not explicit | 41 |
| 90 | 12.11 Project management and accounting | 12.11.1 Common / Journals | Timesheet details | PARTIAL | timesheets | 41 |
| 91 | 12.11 Project management and accounting | 12.11.1 Common / Journals | Beginning balance journals | PARTIAL | beginning balance journals for project migration not named; wave 49 covers project WIP migration | 49 |
| 92 | 12.11 Project management and accounting | 12.11.1 Common / Journals | Project → Activities (Activity list for projects) | PARTIAL | generic activities/case management framework | 45 |
| 93 | 12.11 Project management and accounting | 12.11.1 Common / Journals | Project → Retention terms (customer/vendor retention) | PARTIAL | customer and vendor retention (retainage) terms are not named | 41 |
| 94 | 12.11 Project management and accounting | 12.11.2 Inquiries & Reports | Timesheet inquiries (missing timesheets, timesheet hours) | PARTIAL | timesheets | 41 |
| 95 | 12.11 Project management and accounting | 12.11.2 Inquiries & Reports | SSRS: Timesheet report / Missing timesheets | PARTIAL | timesheets | 41 |
| 96 | 12.11 Project management and accounting | 12.11.3 Periodic | Timesheets → Timesheet workflow / Post timesheets / Create periodic timesheets | PARTIAL | timesheets | 41 |
| 97 | 12.11 Project management and accounting | 12.11.3 Periodic | Project → Intercompany project invoicing (R3 intercompany timesheets/expenses) | PARTIAL | intercompany project resourcing/invoicing not named | 40 |
| 98 | 12.11 Project management and accounting | 12.11.3 Periodic | Project → Retention release (vendor/customer) | PARTIAL | retention | 41 |
| 99 | 12.11 Project management and accounting | 12.11.4 Setup | Billing → Billing rule templates / Retention terms | PARTIAL | retention | 41 |
| 100 | 12.11 Project management and accounting | 12.11.4 Setup | Timesheets → Timesheet periods / Period types / Timesheet parameters (favorites, delegates, workflow by project) | PARTIAL | timesheets | 41 |
| 101 | 12.11 Project management and accounting | 12.11.4 Setup | Indirect costs (Indirect cost component groups, Indirect cost components, Indirect cost rules — R3 PSA) | PARTIAL | project indirect cost components/rules not named; cost accounting allocations in 38 do not apply to project transactions | 41 |
| 102 | 12.11 Project management and accounting | 12.11.4 Setup | Forms → Form setup (project invoices) / Print management | PARTIAL | print management routing | 46 |
| 103 | 12.11 Project management and accounting | 12.11.4 Setup | Intercompany → Intercompany project parameters (lending/borrowing entity, timesheet/expense/vendor invoice transfer) | PARTIAL | intercompany project | 40 |
| 104 | 12.12 Human resources | 12.12.1 Common / Journals | Competencies → Skill mapping / Skill gap analysis | PARTIAL | skill mapping/gap analysis not named | 42 |
| 105 | 12.12 Human resources | 12.12.1 Common / Journals | Worker compliance (US) → I-9, ADA requirements, Immigration/visa, Union agreements | PARTIAL | country-specific compliance fields framework is named; I-9/visa/union specifics come with US pack | 48 |
| 106 | 12.12 Human resources | 12.12.1 Common / Journals | Worker → Worker tasks / reminders / Checklists (onboarding) | PARTIAL | onboarding checklists not named | 42 |
| 107 | 12.12 Human resources | 12.12.1 Common / Journals | Teams (Org admin: Teams, Team types) | PARTIAL | teams (ad hoc cross-org groups) not named; hierarchies are | 6 |
| 108 | 12.12 Human resources | 12.12.1 Common / Journals | Human resources → Cases (HR cases) | PARTIAL | generic case management | 45 |
| 109 | 12.12 Human resources | 12.12.2 Inquiries & Reports | SSRS: EEO-1 / VETS (US compliance) | PARTIAL | US compliance reporting with wave 48 US pack | 48 |
| 110 | 12.12 Human resources | 12.12.3 Periodic | Worker → Reminders (certificate expiry, probation end) | PARTIAL | reminder/alert engine; see alerts gap in §12.31 | 42 |
| 111 | 12.12 Human resources | 12.12.3 Periodic | HR → Year-end (US ACA 1095-C) | PARTIAL | ACA reporting with US pack | 48 |
| 112 | 12.12 Human resources | 12.12.4 Setup | Worker compliance (US) → I-9 document types, ADA requirements, Visa types, Union agreements | PARTIAL | US compliance | 48 |
| 113 | 12.12 Human resources | 12.12.4 Setup | Worker → Worker tasks / Checklist templates (onboarding) | PARTIAL | onboarding checklists | 42 |
| 114 | 12.12 Human resources | 12.12.4 Setup | Teams → Team types, Teams | PARTIAL | teams | 6 |
| 115 | 12.13 Payroll | 12.13.2 Inquiries & Reports | SSRS: Workers compensation report | PARTIAL | workers' compensation class codes/reporting not named | 48 |
| 116 | 12.13 Payroll | 12.13.2 Inquiries & Reports | SSRS: New hire reporting (US state) | PARTIAL | new hire reporting with US pack | 48 |
| 117 | 12.13 Payroll | 12.13.3 Periodic | Payroll → Arrears recovery processing | PARTIAL | deduction arrears recovery not named | 43 |
| 118 | 12.13 Payroll | 12.13.4 Setup | Payroll → Leave of absence / FMLA tracking (US) | PARTIAL | FMLA tracking not named | 48 |
| 119 | 12.14 Inventory management | 12.14.1 Common / Journals | Transfer orders (list page) | PARTIAL | transfer *journal* is named; transfer *orders* with shipment, in-transit warehouse, receive, underdelivery and transfer-order picking are not named in 16, 22 or 28 | 16 |
| 120 | 12.14 Inventory management | 12.14.1 Common / Journals | Transfer order details | PARTIAL | transfer orders | 16 |
| 121 | 12.14 Inventory management | 12.14.1 Common / Journals | Journals → Tag counting journal | PARTIAL | tag counting (numbered tags, two-step) not named; cycle counting is | 16 |
| 122 | 12.14 Inventory management | 12.14.1 Common / Journals | Batch merge (PI) | PARTIAL | batch merge/split with attribute recalculation not named | 24 |
| 123 | 12.14 Inventory management | 12.14.1 Common / Journals | Inventory → ABC classification (on item) | PARTIAL | ABC classification fields/calculation not named | 16 |
| 124 | 12.14 Inventory management | 12.14.1 Common / Journals | Inventory → Commodity codes (Intrastat on item) | PARTIAL | Intrastat setup | 13 |
| 125 | 12.14 Inventory management | 12.14.2 Inquiries & Reports | Transfer order inquiries (Transfer order lines, shipped/received) | PARTIAL | transfer orders | 16 |
| 126 | 12.14 Inventory management | 12.14.2 Inquiries & Reports | Inventory → Intercompany on-hand (R3) | PARTIAL | intercompany on-hand visibility not named | 40 |
| 127 | 12.14 Inventory management | 12.14.2 Inquiries & Reports | SSRS: Transfer order (shipping document, receiving document, Transfer order overview) | PARTIAL | transfer orders | 16 |
| 128 | 12.14 Inventory management | 12.14.2 Inquiries & Reports | SSRS: Inventory aging (link) | PARTIAL | inventory aging | 20 |
| 129 | 12.14 Inventory management | 12.14.2 Inquiries & Reports | SSRS: ABC classification report | PARTIAL | ABC classification | 16 |
| 130 | 12.14 Inventory management | 12.14.2 Inquiries & Reports | SSRS: Packing slip (inventory transfer) | PARTIAL | transfer orders | 16 |
| 131 | 12.14 Inventory management | 12.14.3 Periodic | Tag counting → Create tag counting / Transfer to counting journal | PARTIAL | tag counting | 16 |
| 132 | 12.14 Inventory management | 12.14.3 Periodic | Transfer orders → Ship / Receive (batch) / Transfer order picking release | PARTIAL | transfer orders | 16 |
| 133 | 12.14 Inventory management | 12.14.3 Periodic | Transfer orders → Delete transfer orders | PARTIAL | transfer orders | 16 |
| 134 | 12.14 Inventory management | 12.14.3 Periodic | ABC classification (periodic calculation) | PARTIAL | ABC classification | 16 |
| 135 | 12.14 Inventory management | 12.14.3 Periodic | Inventory → Intercompany: on-hand sync | PARTIAL | intercompany on-hand | 40 |
| 136 | 12.14 Inventory management | 12.14.4 Setup | Inventory and warehouse management parameters — Transfer orders | PARTIAL | transfer orders | 16 |
| 137 | 12.14 Inventory management | 12.14.4 Setup | Inventory → Counting → Counting reason code policies | PARTIAL | reason codes | 11 |
| 138 | 12.14 Inventory management | 12.14.4 Setup | Inventory → ABC classification setup (ABC calculation parameters) | PARTIAL | ABC classification | 16 |
| 139 | 12.14 Inventory management | 12.14.4 Setup | Inventory → Transfer orders → Transfer order parameters, In-transit warehouses | PARTIAL | transfer orders | 16 |
| 140 | 12.14 Inventory management | 12.14.4 Setup | Inventory → Picking → Picking routes setup / Picking workbench (R3 sales picking workbench) | PARTIAL | picking workbench (grouped picking list generation across sales orders, non-WHS) is not named; wave/batch picking is | 28 |
| 141 | 12.15 Product information management | 12.15.1 Common / Journals | Released product → Related products | PARTIAL | related products (cross/up-sell relations) not named | 8 |
| 142 | 12.15 Product information management | 12.15.1 Common / Journals | Released product → Commodity pricing (PI: Pricing calculation, Price margins, Commodity codes) | PARTIAL | commodity price index-based pricing for process industries (PI commodity pricing) not named | 26 |
| 143 | 12.15 Product information management | 12.15.1 Common / Journals | Released product → Product lifecycle state (D365) | PARTIAL | product lifecycle states with per-process blocking not named; stopped flags are | 8 |
| 144 | 12.15 Product information management | 12.15.1 Common / Journals | Product → Product readiness checks (D365) | PARTIAL | product readiness policies/checks not named | 9 |
| 145 | 12.15 Product information management | 12.15.1 Common / Journals | Product → Packaging (D365 packaging materials, PI container packaging) | PARTIAL | packaging materials and packaging fees (EU packaging duty) not named | 28 |
| 146 | 12.15 Product information management | 12.15.3 Periodic | Product → Change product dimension groups / Change item model group (periodic conversions) | PARTIAL | changing dimension/model groups on items with transactions (conversion jobs) not named | 8 |
| 147 | 12.15 Product information management | 12.15.3 Periodic | Product → Delete released products / Mark obsolete | PARTIAL | lifecycle states | 8 |
| 148 | 12.15 Product information management | 12.15.4 Setup | Product → Product lifecycle states (D365) | PARTIAL | lifecycle states | 8 |
| 149 | 12.15 Product information management | 12.15.4 Setup | Product → Packaging units / Packing material fees setup (EU) | PARTIAL | packaging materials | 28 |
| 150 | 12.15 Product information management | 12.15.4 Setup | Product → Commodity pricing setup (Cost basis types, Pricing calculations, Price margin templates, Commodity codes) | PARTIAL | commodity pricing | 26 |
| 151 | 12.15 Product information management | 12.15.4 Setup | Product → Product readiness policies (D365) | PARTIAL | product readiness | 9 |
| 152 | 12.16 Procurement and sourcing | 12.16.1 Common / Journals | RFQ → Bidding (Vendor portal RFQ reply, sealed bids, scoring criteria R3) | PARTIAL | sealed bids, scoring criteria and amendments/addenda not named | 17 |
| 153 | 12.16 Procurement and sourcing | 12.16.1 Common / Journals | Procurement catalogs | PARTIAL | requisitions and category policies are named; an internal procurement catalog with product images, category navigation, catalog publication by org unit is not | 17 |
| 154 | 12.16 Procurement and sourcing | 12.16.1 Common / Journals | Vendor catalogs (vendor catalog import, CMR files) | PARTIAL | vendor catalog upload via supplier portal not named | 20 |
| 155 | 12.16 Procurement and sourcing | 12.16.1 Common / Journals | Vendor requests (link) | PARTIAL | vendor request workflow | 20 |
| 156 | 12.16 Procurement and sourcing | 12.16.1 Common / Journals | Procurement → Signing limits (link to Org admin) | PARTIAL | signing limits (self-service limit requests per job/compensation) not named; amount-band policies are | 17 |
| 157 | 12.16 Procurement and sourcing | 12.16.1 Common / Journals | Procurement → Vendor onboarding (D365 prospective vendor registration) | PARTIAL | vendor onboarding | 20 |
| 158 | 12.16 Procurement and sourcing | 12.16.1 Common / Journals | Procurement → Vendor certifications / Vendor diversity (D365) | PARTIAL | vendor certifications/diversity classifications not named | 7 |
| 159 | 12.16 Procurement and sourcing | 12.16.2 Inquiries & Reports | SSRS: Procurement catalog report | PARTIAL | procurement catalog | 17 |
| 160 | 12.16 Procurement and sourcing | 12.16.3 Periodic | Catalog → Import vendor catalogs (CMR) / Publish procurement catalog | PARTIAL | procurement catalog | 17 |
| 161 | 12.16 Procurement and sourcing | 12.16.4 Setup | Policies → Purchase requisition approval amount thresholds / Signing limit policies | PARTIAL | signing limits | 17 |
| 162 | 12.16 Procurement and sourcing | 12.16.4 Setup | Categories → Procurement categories, Category policy rules, Category managers, Category vendor assignment, Category questionnaires | PARTIAL | category-level vendor assignment and category questionnaires not named | 17 |
| 163 | 12.16 Procurement and sourcing | 12.16.4 Setup | Catalogs → Procurement catalogs, Catalog policies, External catalogs (cXML config), Vendor catalog import parameters, Catalog images | PARTIAL | procurement catalog | 17 |
| 164 | 12.16 Procurement and sourcing | 12.16.4 Setup | Vendors → Vendor request types / Vendor onboarding questionnaires (D365) | PARTIAL | vendor request workflow | 20 |
| 165 | 12.16 Procurement and sourcing | 12.16.4 Setup | Vendors → Vendor certification types / Diversity classifications | PARTIAL | vendor certifications | 7 |
| 166 | 12.16 Procurement and sourcing | 12.16.4 Setup | RFQ → RFQ types, RFQ scoring criteria/groups, Reply templates, Solicitation types (D365), Bidding parameters | PARTIAL | sealed bids/scoring | 17 |
| 167 | 12.16 Procurement and sourcing | 12.16.4 Setup | Forms → Form setup (purchase documents) / Form notes / Print management (purchase) | PARTIAL | print management routing | 46 |
| 168 | 12.17 Sales and marketing | 12.17.1 Common / Journals | Sales order → Supplementary items (prompt) | PARTIAL | supplementary items | 27 |
| 169 | 12.17 Sales and marketing | 12.17.1 Common / Journals | Sales → Commissions (link) | PARTIAL | commission posting | 26 |
| 170 | 12.17 Sales and marketing | 12.17.2 Inquiries & Reports | SSRS: Commission report (link) | PARTIAL | commission posting | 26 |
| 171 | 12.17 Sales and marketing | 12.17.4 Setup | Sales orders → Supplementary items | PARTIAL | supplementary items | 27 |
| 172 | 12.17 Sales and marketing | 12.17.4 Setup | Commissions → Commission calculation, Sales groups (sales reps), Commission customer groups, Commission item groups, Commission posting | PARTIAL | commission posting | 26 |
| 173 | 12.17 Sales and marketing | 12.17.4 Setup | Forms → Form setup (sales documents), Form notes, Print management (sales), Form sorting | PARTIAL | print management routing | 46 |
| 174 | 12.18 Trade allowance management | 12.18.1 Common / Journals | Merchandising events (Promotion planning) | PARTIAL | promotion planning with baseline/lift analysis not named; promotions are | 26 |
| 175 | 12.18 Trade allowance management | 12.18.1 Common / Journals | Funds | PARTIAL | trade promotion funds with budget/usage tracking not named | 26 |
| 176 | 12.18 Trade allowance management | 12.18.1 Common / Journals | Deduction workbench | PARTIAL | deductions workbench — matching customer short-pays to promotions, denial/write-off/split — not named; disputes workflow is | 30 |
| 177 | 12.18 Trade allowance management | 12.18.2 Inquiries & Reports | Fund usage inquiry | PARTIAL | funds | 26 |
| 178 | 12.18 Trade allowance management | 12.18.2 Inquiries & Reports | Deduction inquiry (open deductions, matched) | PARTIAL | deductions workbench | 30 |
| 179 | 12.18 Trade allowance management | 12.18.2 Inquiries & Reports | Promotion effectiveness analysis (lift) | PARTIAL | promotion planning | 26 |
| 180 | 12.18 Trade allowance management | 12.18.2 Inquiries & Reports | SSRS: Fund statement / Fund usage | PARTIAL | funds | 26 |
| 181 | 12.18 Trade allowance management | 12.18.2 Inquiries & Reports | SSRS: Deductions report | PARTIAL | deductions workbench | 30 |
| 182 | 12.18 Trade allowance management | 12.18.3 Periodic | Deductions → Match deductions to claims / Write off small balances | PARTIAL | deductions workbench | 30 |
| 183 | 12.18 Trade allowance management | 12.18.3 Periodic | Funds → Fund budget recalculation / Fund close | PARTIAL | funds | 26 |
| 184 | 12.18 Trade allowance management | 12.18.3 Periodic | Promotion → Update promotion planning actuals | PARTIAL | promotion planning | 26 |
| 185 | 12.18 Trade allowance management | 12.18.4 Setup | Trade allowance → Funds setup (fund types, budget periods) | PARTIAL | funds | 26 |
| 186 | 12.18 Trade allowance management | 12.18.4 Setup | Trade allowance → Merchandising event categories / Event types | PARTIAL | promotion planning | 26 |
| 187 | 12.18 Trade allowance management | 12.18.4 Setup | Trade allowance → Deduction types / Deduction reason codes / Denial reasons | PARTIAL | deductions workbench | 30 |
| 188 | 12.21 Master planning | 12.21.1 Common / Journals | Supply schedule (R3) | PARTIAL | period-bucketed supply schedule workbench not named; planning engine and planned orders are | 22 |
| 189 | 12.21 Master planning | 12.21.1 Common / Journals | Inventory forecast | PARTIAL | inventory (stock level) forecast type not named; demand/supply forecasts are | 22 |
| 190 | 12.21 Master planning | 12.21.1 Common / Journals | Intercompany planned orders (R3 intercompany master scheduling) | PARTIAL | intercompany master planning across legal entities not named in 22 or 40 | 40 |
| 191 | 12.21 Master planning | 12.21.2 Inquiries & Reports | SSRS: Supply schedule report | PARTIAL | supply schedule | 22 |
| 192 | 12.21 Master planning | 12.21.3 Periodic | Intercompany master scheduling (run across entities) | PARTIAL | intercompany planning | 40 |
| 193 | 12.21 Master planning | 12.21.4 Setup | Intercompany → Intercompany planning groups / Intercompany planning group members (sequence, plan) | PARTIAL | intercompany planning | 40 |
| 194 | 12.21 Master planning | 12.21.4 Setup | Planning → Priority-based planning (D365: planning priority models, priority rules on coverage group) | PARTIAL | priority-based / DDMRP-style buffer planning not named | 22 |
| 195 | 12.22 Production control | 12.22.1 Common / Journals | Production order → Split / Group (ProdSplit) | PARTIAL | production order split/merge not named | 21 |
| 196 | 12.23 Process manufacturing (PI) | 12.23.1 Common / Journals | Batch order → Consolidated batch orders (batch order consolidation / bulk item → packed items) | PARTIAL | consolidated batch orders (bulk/pack relationship) not named | 24 |
| 197 | 12.23 Process manufacturing (PI) | 12.23.1 Common / Journals | Batch order → Rework batch order | PARTIAL | rework batch orders not named | 24 |
| 198 | 12.23 Process manufacturing (PI) | 12.23.1 Common / Journals | Batch attribute inheritance (lot inheritance: product-to-product, co-product inheritance) | PARTIAL | batch attribute and shelf-life inheritance rules not named | 24 |
| 199 | 12.23 Process manufacturing (PI) | 12.23.1 Common / Journals | Commodity pricing (Pricing calculation, link) | PARTIAL | commodity pricing | 26 |
| 200 | 12.23 Process manufacturing (PI) | 12.23.1 Common / Journals | Container packaging (PI packaging / Container types) | PARTIAL | packaging materials | 28 |
| 201 | 12.23 Process manufacturing (PI) | 12.23.1 Common / Journals | Batch merge | PARTIAL | batch merge | 24 |
| 202 | 12.23 Process manufacturing (PI) | 12.23.1 Common / Journals | Same-batch reservation (batch reservation rules: consolidated/same batch on sales line) | PARTIAL | same-batch reservation requirement on sales lines not named | 24 |
| 203 | 12.23 Process manufacturing (PI) | 12.23.1 Common / Journals | Customer-specific batch attribute requirements (customer sellable days / batch attribute requirements on sales) | PARTIAL | customer sellable days / customer batch attribute requirements on sales lines not named | 24 |
| 204 | 12.23 Process manufacturing (PI) | 12.23.2 Inquiries & Reports | Commodity price inquiries | PARTIAL | commodity pricing | 26 |
| 205 | 12.23 Process manufacturing (PI) | 12.23.3 Periodic | Batch orders → Consolidate batch orders (batch) | PARTIAL | consolidated batch orders | 24 |
| 206 | 12.23 Process manufacturing (PI) | 12.23.3 Periodic | Batches → Attribute inheritance processing on RAF | PARTIAL | inheritance | 24 |
| 207 | 12.23 Process manufacturing (PI) | 12.23.3 Periodic | Commodity pricing → Price calculation run (update trade agreements from commodity index) | PARTIAL | commodity pricing | 26 |
| 208 | 12.23 Process manufacturing (PI) | 12.23.4 Setup | Batch → Lot inheritance setup (inheritance of attributes/shelf life by product) | PARTIAL | inheritance | 24 |
| 209 | 12.23 Process manufacturing (PI) | 12.23.4 Setup | Commodity pricing → Cost basis types, Pricing calculations, Price margin templates | PARTIAL | commodity pricing | 26 |
| 210 | 12.23 Process manufacturing (PI) | 12.23.4 Setup | Container packaging → Container types, Packaging groups | PARTIAL | packaging materials | 28 |
| 211 | 12.23 Process manufacturing (PI) | 12.23.4 Setup | Customer sellable days / Customer batch attribute requirements | PARTIAL | customer sellable days | 24 |
| 212 | 12.23 Process manufacturing (PI) | 12.23.4 Setup | Rework → Rework parameters | PARTIAL | rework | 24 |
| 213 | 12.23 Process manufacturing (PI) | 12.23.4 Setup | Consolidated batch orders → Bulk/pack item relations | PARTIAL | consolidated batch orders | 24 |
| 214 | 12.24 Lean manufacturing | 12.24.1 Common / Journals | Lean → Subcontracting in lean (service activities with vendor, purchase agreements) | PARTIAL | lean subcontracting (activity-based service purchase from vendor, purchase agreement linkage) not named; discrete subcontracting is in 21 | 24 |
| 215 | 12.24 Lean manufacturing | 12.24.3 Periodic | Lean → Lean subcontracting: generate purchase orders for service activities | PARTIAL | lean subcontracting | 24 |
| 216 | 12.24 Lean manufacturing | 12.24.4 Setup | Lean → Subcontracting setup (service items, purchase agreements for activities) | PARTIAL | lean subcontracting | 24 |
| 217 | 12.26 Warehouse management (WHS) | 12.26.1 Common / Journals | Outbound → Cross docking (D365 planned cross docking, opportunistic) | PARTIAL | cross docking (planned/opportunistic) not named | 31 |
| 218 | 12.26 Warehouse management (WHS) | 12.26.1 Common / Journals | WHS → Transfer order WHS flow (ship/receive with LP) | PARTIAL | transfer orders | 31 |
| 219 | 12.26 Warehouse management (WHS) | 12.26.2 Inquiries & Reports | SSRS: Cross docking report | PARTIAL | cross docking | 31 |
| 220 | 12.26 Warehouse management (WHS) | 12.26.3 Periodic | Cross docking → Planned cross docking processing | PARTIAL | cross docking | 31 |
| 221 | 12.26 Warehouse management (WHS) | 12.26.4 Setup | Outbound → Cross docking templates (D365) | PARTIAL | cross docking | 31 |
| 222 | 12.26 Warehouse management (WHS) | 12.26.4 Setup | Outbound → Outbound sorting (D365 outbound sorting positions) | PARTIAL | outbound sorting (sort pick-to-shipment at a sortation area) not named | 31 |
| 223 | 12.27 Transportation management (TMS) | 12.27.1 Common / Journals | Landed cost (D365 module; voyages, link) | PARTIAL | landed cost voyages | 34 |
| 224 | 12.27 Transportation management (TMS) | 12.27.2 Inquiries & Reports | SSRS: Landed cost reports (link) | PARTIAL | landed cost voyages | 34 |
| 225 | 12.27 Transportation management (TMS) | 12.27.3 Periodic | Landed cost → Voyage cost actualization (link) | PARTIAL | landed cost voyages | 34 |
| 226 | 12.27 Transportation management (TMS) | 12.27.4 Setup | Landed cost → Landed cost parameters / Cost types / Voyage templates (link) | PARTIAL | landed cost voyages | 34 |
| 227 | 12.28 Service management | 12.28.1 Common / Journals | Service subscriptions (subscription fee transactions, index) | PARTIAL | AX service subscriptions (fee indexation, accrual via project) not explicitly named; subscription billing is wave 29 | 29 |
| 228 | 12.28 Service management | 12.28.2 Inquiries & Reports | Subscription inquiries | PARTIAL | service subscriptions | 29 |
| 229 | 12.28 Service management | 12.28.2 Inquiries & Reports | SSRS: Subscription report / Subscription fee transactions | PARTIAL | service subscriptions | 29 |
| 230 | 12.28 Service management | 12.28.3 Periodic | Subscriptions → Create subscription fee transactions / Accrue subscription revenue / Index subscriptions | PARTIAL | service subscriptions | 29 |
| 231 | 12.28 Service management | 12.28.4 Setup | Service → Subscription: Subscription groups, Subscription fee categories, Index rules, Subscription periods | PARTIAL | service subscriptions | 29 |
| 232 | 12.29 Organization administration | 12.29.1 Common / Journals | Teams / Team types | PARTIAL | teams | 6 |
| 233 | 12.29 Organization administration | 12.29.1 Common / Journals | Cases (list page) / Case details | PARTIAL | generic case management (categories, processes, activities, knowledge articles, associations to any record) is not named in any wave; service cases are in 45, disputes in 30, product change in 9 | 45 |
| 234 | 12.29 Organization administration | 12.29.1 Common / Journals | Cases → Knowledge articles | PARTIAL | generic case management | 45 |
| 235 | 12.29 Organization administration | 12.29.1 Common / Journals | Document management → Document handling (attachments on any record), Document types, File types | PARTIAL | attachments exist in 5/11; document types, storage locations (SharePoint/S3), archive directories, retention are not named | 5 |
| 236 | 12.29 Organization administration | 12.29.1 Common / Journals | Foreign trade → Intrastat (journal) / EU sales list (link) | PARTIAL | Intrastat setup | 13 |
| 237 | 12.29 Organization administration | 12.29.1 Common / Journals | Policies → Signing limits (Signing limit policies, Worker signing limit requests, Default signing limits by job/compensation level) | PARTIAL | signing limits | 17 |
| 238 | 12.29 Organization administration | 12.29.1 Common / Journals | Policies → Policy framework forms (Purchasing policies, Expense policies, Audit policies, Vendor invoice policies, Signing limit policies) | PARTIAL | a generic policy framework scoped by org hierarchy is implied by 17/43 but not named as reusable | 17 |
| 239 | 12.29 Organization administration | 12.29.1 Common / Journals | Reason codes (shared financial reasons across modules) | PARTIAL | reason codes | 11 |
| 240 | 12.29 Organization administration | 12.29.1 Common / Journals | Print management (org-wide) | PARTIAL | print management routing | 46 |
| 241 | 12.29 Organization administration | 12.29.1 Common / Journals | Case management → Case processes / Case categories (link) | PARTIAL | generic case management | 45 |
| 242 | 12.29 Organization administration | 12.29.2 Inquiries & Reports | Case inquiries (cases by category/status, SLA) | PARTIAL | generic case management | 45 |
| 243 | 12.29 Organization administration | 12.29.2 Inquiries & Reports | Document management inquiries (documents by type/record) | PARTIAL | document management | 5 |
| 244 | 12.29 Organization administration | 12.29.2 Inquiries & Reports | SSRS: Case report / Case activities | PARTIAL | generic case management | 45 |
| 245 | 12.29 Organization administration | 12.29.2 Inquiries & Reports | SSRS: Signing limits report | PARTIAL | signing limits | 17 |
| 246 | 12.29 Organization administration | 12.29.2 Inquiries & Reports | SSRS: Policies report (policy rules per hierarchy) | PARTIAL | policy framework | 17 |
| 247 | 12.29 Organization administration | 12.29.3 Periodic | Foreign trade → Intrastat transfer (from invoices) / Intrastat validation / Intrastat export (file) | PARTIAL | Intrastat setup | 13 |
| 248 | 12.29 Organization administration | 12.29.3 Periodic | Cases → Close cases / Case SLA breach notifications | PARTIAL | generic case management | 45 |
| 249 | 12.29 Organization administration | 12.29.3 Periodic | Document management → Archive documents / Purge by retention | PARTIAL | document management | 5 |
| 250 | 12.29 Organization administration | 12.29.3 Periodic | Signing limits → Signing limit request processing (annual recertification) | PARTIAL | signing limits | 17 |
| 251 | 12.29 Organization administration | 12.29.4 Setup | Organization → Teams / Team types | PARTIAL | teams | 6 |
| 252 | 12.29 Organization administration | 12.29.4 Setup | Document management → Document management parameters (archive directory, SharePoint, file types, max size), Document types (file, note, URL, image, template; group; location: archive/database/SharePoint), Active document tables, File types | PARTIAL | document management | 5 |
| 253 | 12.29 Organization administration | 12.29.4 Setup | Document management → Document management: OneDrive/SharePoint integration (D365), Attachment migration | PARTIAL | document management | 5 |
| 254 | 12.29 Organization administration | 12.29.4 Setup | Case management → Case categories / Case category types (HR, customer service, collections, product change, audit, service, project, knowledge, etc.) / Case processes (stages) / Case category security | PARTIAL | generic case management | 45 |
| 255 | 12.29 Organization administration | 12.29.4 Setup | Foreign trade → Foreign trade parameters, Commodity codes, Transaction codes, Transport methods, Ports, Statistics procedures, Country/region properties (EU), Intrastat compression, Intrastat check setup, EU sales list reporting | PARTIAL | Intrastat setup | 13 |
| 256 | 12.29 Organization administration | 12.29.4 Setup | Policies → Signing limit policies / Signing limit parameters / Default signing limits (per job/compensation) / Signing limit request workflow | PARTIAL | signing limits | 17 |
| 257 | 12.29 Organization administration | 12.29.4 Setup | Policies → Policy parameters (per policy type: organization hierarchy purpose) | PARTIAL | policy framework | 17 |
| 258 | 12.29 Organization administration | 12.29.4 Setup | Reason codes (Financial reasons: ledger, bank, customer, vendor, fixed asset) | PARTIAL | reason codes | 11 |
| 259 | 12.29 Organization administration | 12.29.4 Setup | Print management → Print management setup (per module/document: original/copy destinations, conditional settings by query) | PARTIAL | print management routing | 46 |
| 260 | 12.29 Organization administration | 12.29.4 Setup | Organization → Audit policies (link) | PARTIAL | audit policies | 48 |
| 261 | 12.29 Organization administration | 12.29.4 Setup | Organization → Workflow approval email templates | PARTIAL | email templates / SMTP outbound not explicitly named in 4 or 5 | 4 |
| 262 | 12.30 System administration | 12.30.1 Common / Journals | Alerts → Alert parameters / Email templates / Email parameters (SMTP) / Outbound email status (E-mail sending status) | PARTIAL | email templates/SMTP outbound | 4 |
| 263 | 12.30 System administration | 12.30.1 Common / Journals | Email → Email distributor batch | PARTIAL | email templates/SMTP outbound | 4 |
| 264 | 12.30 System administration | 12.30.1 Common / Journals | Document handling (link) | PARTIAL | document management | 5 |
| 265 | 12.30 System administration | 12.30.2 Inquiries & Reports | License inquiries (Named user license counts, License usage by role) | PARTIAL | license usage reporting is not named; depends on licensing decision | 3 |
| 266 | 12.30 System administration | 12.30.2 Inquiries & Reports | SSRS: License report / Named user counts | PARTIAL | license usage reporting | 3 |
| 267 | 12.30 System administration | 12.30.3 Periodic | Email → Send email distributor batch / Retry failed | PARTIAL | email templates/SMTP outbound | 4 |
| 268 | 12.30 System administration | 12.30.3 Periodic | Licensing → Named user license count refresh | PARTIAL | license usage reporting | 3 |
| 269 | 12.30 System administration | 12.30.4 Setup | Alerts → Alert parameters (polling intervals, retention, drill-down links) / Email templates / Email parameters (SMTP, Exchange, mail sending mode: batch/client) / Email distributor | PARTIAL | email templates/SMTP outbound | 4 |
| 270 | 12.30 System administration | 12.30.4 Setup | Document management (link) | PARTIAL | document management | 5 |
| 271 | 12.31 Home / role centers / workflow | 12.31.1 Common / Journals | Cases (My cases) | PARTIAL | generic case management | 45 |
| 272 | 12.31 Home / role centers / workflow | 12.31.1 Common / Journals | Timesheets (Home → Timesheets, link) | PARTIAL | timesheets | 41 |
| 273 | 12.31 Home / role centers / workflow | 12.31.1 Common / Journals | Document handling on any record | PARTIAL | document management | 5 |
| 274 | 12.31 Home / role centers / workflow | 12.31.4 Setup | Workflow → Notification settings (email, in-app, digest) / Workflow email templates | PARTIAL | email templates/SMTP outbound | 4 |
| 275 | 12.31 Home / role centers / workflow | 12.31.4 Setup | Cases → Case setup (link) | PARTIAL | generic case management | 45 |
| 276 | 12.33 Electronic reporting / Regulatory Configuration Service (D365) | 12.33.1 Common / Journals | Electronic messages (Electronic messaging framework: message types, message items, processing actions, additional fields; used for VAT returns, SAF-T, UK MTD, IT e-invoice SDI, etc.) | PARTIAL | electronic messaging lifecycle (statutory submission with response tracking, e.g. UK MTD, DE/FR mandates) not named; e-invoicing in 35 covers Peppol/SDI/CFDI submissions | 48 |
| 277 | 12.33 Electronic reporting / Regulatory Configuration Service (D365) | 12.33.2 Inquiries & Reports | Electronic messages inquiry (status, responses, errors) | PARTIAL | electronic messaging | 48 |
| 278 | 12.33 Electronic reporting / Regulatory Configuration Service (D365) | 12.33.3 Periodic | Electronic messages → Generate / Send / Receive response (batch processing actions) | PARTIAL | electronic messaging | 48 |
| 279 | 12.33 Electronic reporting / Regulatory Configuration Service (D365) | 12.33.4 Setup | Electronic messages → Message types, Message item types, Message statuses, Processing actions (ER export/import, web service call, user input), Additional fields, Executable classes, Web service settings (endpoint, certificates), Message processing | PARTIAL | electronic messaging | 48 |
| 280 | 12.34 Engineering change management (D365) | 12.34.1 Common / Journals | Release product structure (release engineering product + BOM/route to legal entities) | PARTIAL | engineering organization vs operational legal entity split, and release product structure to legal entities with per-LE release policies, not named | 9 |
| 281 | 12.34 Engineering change management (D365) | 12.34.1 Common / Journals | Product readiness checks (per LE checklist before product is usable) | PARTIAL | product readiness | 9 |
| 282 | 12.34 Engineering change management (D365) | 12.34.1 Common / Journals | Product acceptance (accept released product in receiving LE) | PARTIAL | release product structure | 9 |
| 283 | 12.34 Engineering change management (D365) | 12.34.1 Common / Journals | Product lifecycle states (per version, controlling process enablement) | PARTIAL | lifecycle states | 8 |
| 284 | 12.34 Engineering change management (D365) | 12.34.1 Common / Journals | Engineering product categories (category assigns version nomenclature, attributes, readiness policy) | PARTIAL | engineering product categories with version nomenclature not named; category hierarchies are | 9 |
| 285 | 12.34 Engineering change management (D365) | 12.34.1 Common / Journals | Engineering → PLM integration (D365 Teamcenter/Windchill connectors via Dataverse) | PARTIAL | PLM connector not named among iPaaS reference connectors | 35 |
| 286 | 12.34 Engineering change management (D365) | 12.34.2 Inquiries & Reports | Release status inquiry (product × legal entity) | PARTIAL | release product structure | 9 |
| 287 | 12.34 Engineering change management (D365) | 12.34.2 Inquiries & Reports | Readiness check status inquiry | PARTIAL | product readiness | 9 |
| 288 | 12.34 Engineering change management (D365) | 12.34.2 Inquiries & Reports | SSRS: Product release report | PARTIAL | release product structure | 9 |
| 289 | 12.34 Engineering change management (D365) | 12.34.3 Periodic | Release product structures (batch release to LEs) | PARTIAL | release product structure | 9 |
| 290 | 12.34 Engineering change management (D365) | 12.34.3 Periodic | Readiness check evaluation (batch) | PARTIAL | product readiness | 9 |
| 291 | 12.34 Engineering change management (D365) | 12.34.3 Periodic | Engineering → Sync with PLM (batch) | PARTIAL | PLM connector | 35 |
| 292 | 12.34 Engineering change management (D365) | 12.34.4 Setup | Engineering organizations (which LE owns engineering data) | PARTIAL | release product structure | 9 |
| 293 | 12.34 Engineering change management (D365) | 12.34.4 Setup | Engineering product categories (version nomenclature, attributes, readiness policy, release policy) | PARTIAL | engineering product categories | 9 |
| 294 | 12.34 Engineering change management (D365) | 12.34.4 Setup | Version nomenclature (number/letter/date segments) | PARTIAL | engineering product categories | 9 |
| 295 | 12.34 Engineering change management (D365) | 12.34.4 Setup | Product readiness policies / Readiness checks (per-LE checklists: tax, prices, coverage, warehouse setup) | PARTIAL | product readiness | 9 |
| 296 | 12.34 Engineering change management (D365) | 12.34.4 Setup | Product release policies (what to copy to receiving LE: BOM, route, prices, default order settings) | PARTIAL | release product structure | 9 |
| 297 | 12.34 Engineering change management (D365) | 12.34.4 Setup | Product lifecycle states (enabled business processes per state, default states) | PARTIAL | lifecycle states | 8 |
| 298 | 12.34 Engineering change management (D365) | 12.34.4 Setup | Engineering → PLM connector configuration | PARTIAL | PLM connector | 35 |
| 299 | 12.35 Asset management (D365) | 12.35.1 Common / Journals | Maintenance rounds (route of assets inspected together) | PARTIAL | maintenance rounds (multi-asset inspection routes) not named | 44 |
| 300 | 12.35 Asset management (D365) | 12.35.1 Common / Journals | Maintenance sequences (ordered job types on an asset) | PARTIAL | maintenance sequences not named | 44 |
| 301 | 12.35 Asset management (D365) | 12.35.1 Common / Journals | Work order → Fault registration (symptoms, causes, remedies) | PARTIAL | fault designer: symptom/area/type/cause/remedy taxonomy and fault analysis not named | 44 |
| 302 | 12.35 Asset management (D365) | 12.35.1 Common / Journals | Warranties (vendor warranty per asset, warranty check on work orders) | PARTIAL | asset warranties (vendor warranty terms, warranty check on work order) not named | 44 |
| 303 | 12.35 Asset management (D365) | 12.35.1 Common / Journals | Maintenance budgets (asset/functional location budget lines, budget calculation from plans) | PARTIAL | maintenance budgeting from plans not named | 44 |
| 304 | 12.35 Asset management (D365) | 12.35.2 Inquiries & Reports | Fault analysis (fault statistics by symptom/cause) | PARTIAL | fault designer | 44 |
| 305 | 12.35 Asset management (D365) | 12.35.2 Inquiries & Reports | Warranty inquiry | PARTIAL | warranties | 44 |
| 306 | 12.35 Asset management (D365) | 12.35.2 Inquiries & Reports | Budget vs actual (maintenance) | PARTIAL | maintenance budgets | 44 |
| 307 | 12.35 Asset management (D365) | 12.35.2 Inquiries & Reports | SSRS: Fault report | PARTIAL | fault designer | 44 |
| 308 | 12.35 Asset management (D365) | 12.35.3 Periodic | Schedule maintenance rounds / Maintenance sequences | PARTIAL | maintenance rounds | 44 |
| 309 | 12.35 Asset management (D365) | 12.35.3 Periodic | Maintenance budget calculation | PARTIAL | maintenance budgets | 44 |
| 310 | 12.35 Asset management (D365) | 12.35.4 Setup | Maintenance plans → Plan types, Plan line defaults, Maintenance rounds setup, Maintenance sequences setup | PARTIAL | maintenance rounds | 44 |
| 311 | 12.35 Asset management (D365) | 12.35.4 Setup | Fault → Fault symptoms, Fault areas, Fault types, Fault causes, Fault remedies, Fault designer (per asset type) | PARTIAL | fault designer | 44 |
| 312 | 12.35 Asset management (D365) | 12.35.4 Setup | Warranties → Warranty types, Vendor warranty setup | PARTIAL | warranties | 44 |
| 313 | 12.35 Asset management (D365) | 12.35.4 Setup | Budget → Maintenance budget parameters | PARTIAL | maintenance budgets | 44 |
| 314 | 12.37 Subscription billing (D365) | 12.37.1 Common / Journals | Deferral schedules (list) / details (revenue and expense deferrals, short-term/long-term split) | PARTIAL | expense deferrals (prepaid expense amortization from AP invoices) not named; revenue deferrals are | 29 |
| 315 | 12.37 Subscription billing (D365) | 12.37.3 Periodic | Expense deferral recognition | PARTIAL | expense deferrals | 29 |
| 316 | 12.39 Vendor collaboration / customer self-service (D365) | 12.39.1 Common / Journals | Vendor → Prospective vendor registration (onboarding wizard, questionnaires) | PARTIAL | vendor onboarding | 20 |
| 317 | 12.39 Vendor collaboration / customer self-service (D365) | 12.39.4 Setup | Vendor collaboration → Onboarding: Registration wizard configuration, Questionnaires | PARTIAL | vendor onboarding | 20 |


### 12.99.5 Inventory totals

| Status | Rows |
|---|---|
| COVERED | 2472 |
| PARTIAL | 317 |
| GAP | 108 |
| OUT-OF-SCOPE | 102 |
| SUPERSEDED | 147 |
| **Total assessed rows** | **3146** |
