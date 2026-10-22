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
| Interest/collection letters (vendor side) | Not in AX AP | — | — | n/a (listed for completeness; no AX feature) |
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
