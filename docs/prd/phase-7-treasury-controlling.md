# Part 7 — Phase 7: Treasury, Assets & Controlling (waves 36–40)

## 7.0 Phase overview

**Business outcome at phase exit.** At the end of wave 40 the finance function runs entirely on Passage: treasury sees every bank balance intraday and a 13-week cash forecast built from the subledgers; fixed assets and leases depreciate under multiple books and post to the universal journal; controllers plan budgets, enforce budget control on commitments, and run a cost accounting ledger with allocations; product costs roll up from BOMs and routings with a cost component split, and production variances are decomposed by cause; intercompany trade posts mirror documents across legal entities and the group consolidates with eliminations and IAS 21 translation. This is the "finance parity" milestone in PRD §10 (wave 40): a practicing controller signs off that nothing they did in AX/SAP finance is missing.

**Phase-exit sign-off criteria**
- 1,000-line bank statement fixture auto-matches ≥ 95% of lines correctly with zero false positive matches (wave 36 proof).
- Cash forecast total per bucket equals the sum of source contributions to the cent, verified by a property test over 10,000 generated open items (wave 36).
- 100 asset × method × convention fixtures match IRS Publication 946 tables and the straight-line edge cases to the cent; the asset subledger ties to GL asset cost and accumulated depreciation accounts with zero difference (wave 37).
- IFRS 16 lease schedules match the accountant-reviewed workbook to the cent for 25 fixture leases (wave 37).
- Budget check blocks or warns every over-budget PO per policy; encumbrance liquidation on invoice is exact to the cent (wave 38).
- Every allocation cycle converges and `total cost out = total cost in` to the cent; cost accounting ledger primary costs reconcile to GL (wave 38).
- 50 multi-level cost roll-ups match the reference workbook across all cost components; variance decomposition sums to total variance for 200 ended production orders; mass roll of 50,000 items completes in < 15 minutes (wave 39).
- Intercompany mirror documents balance across entities for 1,000 random IC orders; IC reconciliation flags 100% of planted mismatches; the 3-entity, 2-currency demo group consolidates to accountant-reviewed statements (wave 40).
- Daily subledger-to-GL reconciliation jobs for bank, assets, cost accounting, costing and intercompany run green for 10 consecutive days in `stage`.

**Cross-wave data model summary**

| Entity | Owning service (domain DB) | Key relationships |
|---|---|---|
| BankAccount, BankStatement, StatementLine, ReconciliationMatch, MatchingRule | `treasury-svc` (Postgres `treasury`) | BankAccount → LegalEntity, MainAccount (wave 11); StatementLine → payment (wave 19), receipt (wave 30) |
| CashForecastEntry, CashPosition | `cashforecast-svc` (Python, Postgres `treasury`) | Reads AP/AR open items, bank balances, recurring entries |
| BankConnection, OutboundFile, InboundFile | `bank-connect-svc` (TS, Postgres `treasury`) | Keys referenced by Vault path only |
| Asset, AssetBook, DepreciationProfile, AssetTransaction, Lease, LeaseSchedule | `asset-svc` (Postgres `assets`) | Asset → Project (wave 41 later), PurchaseLine (wave 17), InventTrans (wave 16); posts via journal API (wave 11) |
| BudgetRegisterEntry, BudgetPlan, BudgetControlRule, Encumbrance, BudgetCheckResult | `budget-svc` (Postgres `controlling`) | Encumbrance → Requisition/PO (wave 17), VendorInvoice (wave 18) |
| CostObject, CostElement, CostEntry, AllocationPolicy, AllocationRun, StatisticalMeasure | `costacct-svc` (Postgres `controlling`) | CostEntry mirrors journal lines by dimension (wave 11) |
| CostingVersion, CostingSheet, CostGroup, CostCalculation, CostComponentSplit, ItemCostRecord, VarianceEntry | `costing-svc` (Postgres `costing`, extends wave 20) | Reads BOM/Route versions (wave 9), production orders (wave 21), inventory cost trans (wave 20) |
| IntercompanyPartner, IcDocumentLink, IcJournal, IcReconciliationItem | `interco-svc` (TS, Postgres `group`) | Links SalesOrder (wave 27) ↔ PurchaseOrder (wave 17), invoices, payments |
| ConsolidationGroup, ConsolidationUnit, Ownership, EliminationRule, ConsolidationRun, ConsolidationEntry | `consol-svc` (Postgres `group`) | Reads balances per ledger (wave 11), rates (wave 12), statements (wave 15) |

**Cross-wave event catalog**

| Event (CloudEvents type) | Producer | Consumers | Payload summary |
|---|---|---|---|
| `dev.passage.treasury.statement.reconciled.v1` | treasury-svc | journal (wave 11), cashforecast-svc, analytics (46) | statementId, bankAccountId, matchedCount, unmatchedCount, closingBalance |
| `dev.passage.treasury.cashposition.updated.v1` | cashforecast-svc | dashboards, interco-svc (netting) | legalEntityId, asOf, balancesByAccount[], forecastBuckets[] |
| `dev.passage.assets.depreciation.posted.v1` | asset-svc | costacct-svc, analytics | runId, bookId, period, assetCount, totalAmount, journalEntryId |
| `dev.passage.assets.asset.disposed.v1` | asset-svc | costacct-svc, tax (wave 13) | assetId, bookId, proceeds, nbv, gainLoss |
| `dev.passage.budget.check.completed.v1` | budget-svc | procurement (17), AP (18), workflow | sourceDocumentId, result (pass/warn/fail), availableBefore, amount, availableAfter |
| `dev.passage.controlling.allocation.completed.v1` | costacct-svc | budget-svc, analytics | runId, policyId, iterations, totalIn, totalOut, residual |
| `dev.passage.costing.standardcost.activated.v1` | costing-svc | inventory costing (20), production (21), budget-svc | costingVersionId, itemCount, revaluationJournalId, totalRevaluation |
| `dev.passage.costing.variance.posted.v1` | costing-svc | costacct-svc, analytics | productionOrderId, varianceByType{lotSize,quantity,price,substitution}, byComponent[] |
| `dev.passage.interco.mirror.created.v1` | interco-svc | sales (27), procurement (17), consol-svc | sourceDocId, sourceEntity, mirrorDocId, mirrorEntity, type |
| `dev.passage.consol.run.completed.v1` | consol-svc | reporting (15), analytics | runId, groupId, period, unitCount, eliminationCount, ctaAmount |

**Phase-level risks**

| Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|
| Bank connectivity (EBICS/Open Banking) certification differs per bank and takes weeks | High | Medium | Ship SFTP first; EBICS and PSD2 against public sandboxes; keep adapters behind one `BankChannel` interface | Engineering |
| Depreciation or lease math diverges from auditor expectation on edge cases (short years, mid-quarter, modifications) | Medium | High | Fixture library reviewed by the accountant; property tests that total depreciation always equals depreciable basis | Product |
| Budget control latency slows PO entry | Medium | Medium | p95 budget check < 150 ms SLO; per-dimension-combination balance cache rebuilt from events | Engineering |
| Reciprocal allocations fail to converge with circular self-references | Low | Medium | Exact linear solve for ≤ 2,000 senders; capped iteration with residual sweep rule (BR-38.6) | Engineering |
| Cost roll-up on cyclic or 20+ level BOMs | Medium | Medium | Cycle detection at BOM approval (wave 9) and at roll time; hard depth limit 20 | Engineering |
| Two-person team across five accounting-heavy waves | High | High | Accountant review gate per wave; AI-assisted generation per PRD §11 | Owners |

## 7.36 Wave 36: Cash & Bank Management, Reconciliation & Treasury

### 7.36.1 Scope and business context

Wave 36 gives treasury a system of record for bank accounts and the daily mechanics around them. Before this wave, AP payments (wave 19) and AR receipts (wave 30) post to GL bank accounts, but nobody can import a bank statement, reconcile it, know the intraday position, or forecast cash. After it, a treasury analyst imports camt.053/BAI2/MT940 statements (or receives them automatically through SFTP/EBICS/Open Banking), the matching rule engine reconciles ≥ 95% of lines without touching them, exceptions are worked in a reconciliation workspace, and a cash position and 13-week forecast refresh from the subledgers every hour. Checks can be issued, voided and reprinted; letters of credit and bank guarantees are tracked with facility limits; intercompany netting and notional cash pooling basics are available for groups.

Out of scope: hedge accounting and derivative valuation (not in v1), in-house bank virtual accounts with interest calculation, lockbox OCR, treasury trading (money market, FX deals). Payroll (wave 43) and project (wave 41) forecast sources are consumed through the forecast source interface once those waves exist; until then recurring forecast entries stand in.

**Scope amendment (gap closure).** The gap registers (coverage-ax §12.99 rows 1–19, coverage-sap §13.99 rows 2–84 proposed or mapped to wave 36) assign the following to this wave, and they are added below: customer bills of exchange and vendor promissory notes (FR-36.13, FR-36.14), postdated checks (FR-36.15), cash journal / petty cash (FR-36.16), bank fee analysis from camt.086 (FR-36.17), treasury deals limited to FX spot, forward and swap plus fixed-term deposits (FR-36.18), loan contracts with interest schedules including intercompany loans (FR-36.19), FX exposure (FR-36.20), treasury payment requests and bank-to-bank transfers (FR-36.21), bank chains (FR-36.22), payment hub routing (FR-36.23) and in-house cash current accounts (FR-36.24, Could, flag-gated; this supersedes the out-of-scope note above for virtual accounts with interest). Hedge accounting (IFRS 9 / ASC 815), derivatives other than FX forwards and swaps, securities and money-market trading beyond fixed-term deposits remain out of scope for v1; their disposition is recorded in 7.36.21.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| D365 advanced bank reconciliation: statement import via DMF (BankStatementFormat + XSLT transform) fails silently on bank camt dialects; the user re-imports after editing XSLT | Transform-first design; no schema validation surfaced to the user | Native camt/BAI2/MT940 parsers with per-bank profile overrides and line-level validation errors | Failed imports needing IT help: 1 in 5 → < 1 in 200 |
| SAP FEBAN post-processing: each unmatched line is opened, the clearing document found in FBL5N, keyed and posted (≈ 14 clicks per line) | Posting rules (OT83) only test search strings; no candidate ranking | Scored candidates shown inline; `m` to match; N:1 batch matching | Clicks per exception 14 → 2; auto-match ≥ 95% |
| BankReconciliationMatchingRule in D365 is pass/fail; a 1-day value-date difference leaves the line unmatched | Boolean rules, no partial credit | Weighted scoring with tolerance and date-window decay | Unmatched lines after rules: 30% → < 5% |
| Cash position in AX = BankAccountTable.Balance updated at posting; SAP FF7A refreshes in a batch run; both ignore intraday camt.052 | Batch-oriented, no intraday feed | camt.052 ingestion on arrival; position recomputed per event | Position latency 4 h batch → < 60 s after file arrival |
| EBICS keys held on a Windows share for a bank client tool; audit cannot prove who sent what | Connectivity outside the ERP | `bank-connect-svc` signs in Vault; every file hashed and acknowledged | 100% of files with signer, hash and bank ack in audit |

### 7.36.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Bank account master, signatories, approval | BankAccountTable, BankAccountTrans, BankGroup, form BankAccountTable; D365 bank account change workflow | FI12 house banks T012/T012K, BAM (FCLM_BAM) with signatories and workflow | `treasury-svc` BankAccount entity, workspace Bank accounts | full |
| Electronic statement import | BankStmtISOAccountStatement, BankStmtISOReportEntry, BankStatementFormat, DIXF ISO 20022 transform | FF_5 (FEBKO/FEBEP), FF67 manual statement, OT83 posting rules, external transaction codes | Statement import API, formats camt.053/BAI2/MT940/CSV | improved (native XSD validation, duplicate detection) |
| Advanced bank reconciliation | BankReconciliationHeader, BankReconciliationMatchingRule, BankReconciliationMatchingRuleSet, BankReconciliationWorkspace | FEBAN post-processing, FEB_BSPROC, search strings (OTPM), interpretation algorithms | Matching rule engine with scoring, reconciliation workspace | improved |
| Check management | BankChequeTable, BankChequeReprint, check void/reverse, positive pay BankPositivePayFormat | PAYR check register, FCH5/FCH8/FCH9 (void/reissue), FCHN, FCHX | Check entity with void/reprint/stop, positive pay (wave 19 export) | full |
| Letters of credit / guarantees | BankLCImport, BankLCExport, BankLGTable (letters of guarantee), BankFacility, BankFacilityAgreement | No native EBS equivalent; TRM/bank guarantee via FI-TRM or custom; S/4 Trade Finance | Facility, LetterOfCredit, Guarantee entities with limit tracking | full |
| Cash position (intraday) | Bank balance per account only (BankAccountTable.Balance), no intraday | FF7A cash position, FQM_FLOW (One Exposure), camt.052 intraday | camt.052 ingestion, CashPosition view by entity/currency/account | improved |
| Cash flow forecast | LedgerCov, LedgerCovDocument, Cash flow forecasting setup, workspace Cash overview | FF7B liquidity forecast, FQM planning levels/groups, Cash Management liquidity planner | `cashforecast-svc` buckets daily/weekly/monthly from AP/AR/recurring sources | full |
| Intercompany netting, cash pooling | Not native (centralized payments only) | IHC In-House Cash, cash pooling (concentration) in S/4 Cash Management | Netting run creating IC settlement journals; notional pool structures | partial (no IHC virtual accounts) |
| Bank connectivity | None native (file-based via DMF) | Bank Communication Management (BCM), Multi-Bank Connectivity (MBC) | `bank-connect-svc` adapters SFTP/EBICS 3.0/PSD2 XS2A, Vault keys | improved |
| Bills of exchange (customer) | CustBillOfExchangeJour, CustBillOfExchangeTrans, LedgerJournalTable types Customer bill of exchange draw / redraw / remittance / settle / protest, CustPaymModeTable (bill of exchange), remittance types Collection / Discount, Cash and bank management parameters → Bills of exchange | F-36 (BoE payment), F-33 (discount), F-34 (collection), FBWD / FBWE presentation, special G/L indicator W (BSID/BSED), S_ALR_87012199 BoE list | BillOfExchange entity with draw / remit / protest / settle lifecycle (FR-36.13) | full |
| Promissory notes (vendor) | VendPromissoryNoteJour, VendPromissoryNoteTrans, journal types Vendor promissory note draw / redraw / remittance / settle, vendor posting profile per note status | F-40 (AP bill of exchange payment), FBW6 reversal, FBWE presentation, special G/L W (BSIK), S_ALR_87012116 | PromissoryNote entity (FR-36.14) | full |
| Postdated checks | CustPostdatedChecks / VendPostdatedChecks (R3 APAC), Cash and bank management parameters → Postdated checks tab (clearing accounts, withholding tax at settlement) | No standard object; noted item (special G/L S) or BoE used in practice | PostdatedCheck entity with maturity, deposit, bounce (FR-36.15) | improved |
| Cash journal / petty cash | Not native (LedgerJournal against a cash main account) | FBCJ, TCJ_DOCUMENTS / TCJ_POSITIONS / TCJ_C_JOURNALS, FBCJC0–FBCJC3 setup, S_ALR_87012309 cash book | CashDesk and CashJournalDocument with daily close (FR-36.16) | full |
| Bank fee analysis | None | Fiori Bank Fee Analysis / Bank Relationship Overview, camt.086 import (BAM) | BankFeeStatement import and variance to fee agreement (FR-36.17) | full |
| Treasury deals (FX, deposits) | None | FTR_CREATE / FTR_EDIT / FTR_00 (TRM transaction manager), VTBFHA / VTBFHAPO, TBB1 flow posting, TPM1 key-date valuation, TPM10, TPM12 / TPM13 position and flow lists, TPM20 posting journal | TreasuryDeal with flows, settlement and key-date valuation (FR-36.18) | partial (FX spot / forward / swap and fixed-term deposits only; no derivatives beyond these, no securities) |
| Loans management | None (loans kept as journals) | CML / FN* loan contracts, FNM1 flows, F.52 balance interest used for IC loans | LoanContract with schedule and interest accrual (FR-36.19) | partial (annuity / linear / bullet, fixed or index-linked rate; no structured loans) |
| FX exposure | None | Treasury Exposure Management (raw exposures, exposure positions) | FxExposure derived from AP / AR / orders / forecast / loans, offset by deals (FR-36.20) | partial |
| Payment requests and bank-to-bank transfers | Bank account transfer via LedgerJournal (account type Bank → Bank) | FIBLFFP payment request, F111 payment program for payment requests, FRFT / FRFT_B repetitive codes | TreasuryPaymentRequest with repetitive templates and four-eyes approval (FR-36.21) | full |
| Bank chains | Intermediary bank fields on VendBankAccount only | FIBC bank chains (TBCHAIN11 / TBCHAIN21 / TBCHAIN31) | BankChain per currency / receiving country / receiving BIC used in pain.001 IntrmyAgt (FR-36.22) | full |
| Payment hub routing | Centralized payments (wave 40, FR-40.12) | Advanced Payment Management (APM) routing, batching, reformatting; BCM batch approval | PaymentRoutingRule and central payment monitor in `bank-connect-svc` (FR-36.23) | partial (no cross-tenant hub) |
| In-house cash | None | IHC0 / IHC1IP in-house cash center, IHC accounts, internal payment orders, interest conditions, subsidiary account statements | IhcAccount with internal payment orders, interest and generated camt.053 (FR-36.24) | partial (no external payments routed through IHC accounts in v1) |
| Hedge accounting | None | THMEX hedge management, hedge effectiveness test run, hedge plan reports, THM* customizing | none | dropped-with-reason: IFRS 9 / ASC 815 hedge accounting is deferred to v2; FX deals are valued through P&L (7.36.21) |

### 7.36.3 Functional requirements

- **FR-36.1 Bank account master** — Must. The system shall maintain bank accounts per legal entity with IBAN/BIC or routing/account numbers, currency, GL main account, bank transaction types, signatories and a change-approval workflow.
  - Notes: IBAN mod-97 and ABA routing checksum validated on save; account numbers stored encrypted at rest (pgcrypto, key in Vault) and shown masked except to the `TreasuryBankAccountMaintain` duty; status lifecycle Draft → Active → Inactive → Closed; changes to number/currency/GL account require 2-step approval (Temporal workflow); maximum 500 bank accounts per legal entity.
- **FR-36.2 Statement import** — Must. The system shall import bank statements in camt.053.001.02/08, BAI2, MT940 and configurable CSV, validate structure, detect duplicates and persist header, balances (OPBD/CLBD/CLAV) and entries.
  - Notes: files up to 50 MB and 200,000 entries; duplicate detection on (bankAccount, statementId or sequence, closingBalance); gap detection on statement sequence numbers raises a warning; each entry retains the raw source fragment for audit; import of 10,000 lines completes in < 30 s.
- **FR-36.3 Matching rule engine** — Must. The system shall match statement lines to Passage bank transactions using ordered rule sets, producing a 0–100 score per candidate and auto-matching above a configurable threshold.
  - Notes: criteria: amount (exact / tolerance absolute or %), value date window (±N days), reference regex capture (end-to-end id, check number, invoice number), counterparty name trigram similarity, bank transaction code map (BTC/domain-family-subfamily); weights sum to 100; default thresholds auto-match ≥ 90, suggest 60–89, unmatched < 60; 1:1, 1:N and N:1 matches with up to 50 lines per side; rules tested against historical statements with a precision/recall report before activation.
- **FR-36.4 ML match suggestions** — Should. The system shall rank unmatched lines' candidates with a model trained on the tenant's confirmed matches and present the top 3 suggestions with reasons.
  - Notes: Python gradient-boosted model in `cashforecast-svc`; retrained nightly if ≥ 200 new confirmed matches; suggestions never auto-post; model inputs and version logged per suggestion.
- **FR-36.5 Reconciliation workspace** — Must. The system shall let users confirm, split, create new transactions (fees, interest) from unmatched lines, mark lines as in-transit, and close the reconciliation when book and bank balances agree.
  - Notes: closing requires `|bank closing − (book balance + outstanding items)| = 0.00`; new transactions created from lines post through the wave-11 journal API with the statement line as source document; reversed matches keep history; reconciliation of 10,000 lines closes in < 60 s.
- **FR-36.6 Check management** — Must. The system shall issue, void, reprint and stop checks with gap-free check numbers per bank account.
  - Notes: void keeps the number consumed; reprint issues a new number and voids the old; status Created → Printed → Sent → Cleared / Voided / Stopped; positive pay file (wave 19 format) regenerated on void.
- **FR-36.7 Letters of credit and guarantees** — Must. The system shall record import/export letters of credit and bank guarantees against bank facilities with limits, expiry and amendments, and link them to purchase or sales orders.
  - Notes: facility utilization = Σ open instruments; new instrument blocked when utilization + amount > limit unless duty `TreasuryFacilityOverride`; expiry alerts 30/7/1 days; margin deposits post via the journal.
- **FR-36.8 Cash position** — Must. The system shall present intraday position per legal entity, currency and bank account from the latest camt.052 or statement balance plus same-day unposted payments and receipts.
  - Notes: refreshed on every inbound file and at least hourly; converted to entity reporting currency at the wave-12 spot rate; age of each balance displayed; stale (> 24 h) balances flagged.
- **FR-36.9 Cash flow forecast** — Must. The system shall compute forecast buckets (daily 30 days, weekly 13 weeks, monthly 12 months) from registered sources.
  - Notes: sources: AP open items (due date adjusted by vendor average days-late), AR open items (due date + customer DSO delta), open POs/SOs not yet invoiced (expected invoice date + terms), recurring entries, manual adjustments, later payroll (43) and projects (41); each source implements `ForecastSource.Contribute(asOf) []Contribution`; every bucket total equals the sum of its contributions (property test); 1M open items forecast in < 5 min.
- **FR-36.10 Intercompany netting** — Should. The system shall run a netting cycle across participating entities, net IC receivables/payables per pair and currency, and create settlement journals.
  - Notes: cycle statuses Proposed → Approved → Settled; netting rate = wave-12 rate on settlement date; residual below 1.00 in currency written off to IC rounding account.
- **FR-36.11 Cash pooling** — Could. The system shall model notional and physical (zero-balancing) pool structures and generate sweep transfers.
  - Notes: target balance per sub-account; sweep creates payment (wave 19) with pool header as counterparty.
- **FR-36.12 Bank connectivity** — Must. The system shall send payment files and receive statements through SFTP, EBICS 3.0 (H005) and PSD2/Berlin Group XS2A adapters with keys and certificates in Vault.
  - Notes: signing via Vault transit engine; private keys never leave Vault; every outbound file hashed (SHA-256) and logged with user, time, channel and bank acknowledgment; retries with exponential backoff up to 24 h; sandbox profiles per adapter.
- **FR-36.13 Bills of exchange (customer)** — Should. The system shall draw bills of exchange against customer open items, redraw unpaid bills, remit bills to a bank for collection or discount, record protest, and settle bills at maturity, posting each status through a customer posting profile per status.
  - Notes: lifecycle Drawn → Remitted (Collection or Discount) → Honored | Protested; a Redraw creates a new bill that settles the protested one; posting: Draw Dr Bills receivable / Cr Customer (open items settled, wave 30 settlement engine); Remit for collection Dr Bills remitted for collection / Cr Bills receivable; Remit for discount Dr Bank (net), Dr Discount expense / Cr Bills remitted; Honored on bank statement match (FR-36.3 rule criterion `billNumber`) Dr Bank / Cr Bills remitted; Protest reverses to the customer open item plus protest fee line; remittance files (CFONB 160, RiBa CBI, AEB 58 / 19) come from the wave-48 country packs through the wave-19 format engine; feature flag `treasury.bills-of-exchange`; bill numbers from a per-entity number sequence; maximum 5,000 bills per remittance.
- **FR-36.14 Promissory notes (vendor)** — Should. The system shall draw promissory notes against vendor open items, redraw, remit notes to the bank, and settle them at maturity through vendor posting profiles per status.
  - Notes: lifecycle Drawn → Remitted → Settled, with Redraw replacing an unpaid note; Draw Dr Vendor (open items settled, wave 19 settlement) / Cr Notes payable; Settle on bank statement match Dr Notes payable / Cr Bank; notes due within the forecast horizon contribute to FR-36.9 on maturity date with confidence 1.0; the note document prints with the vendor remittance (7.36.15); same flag as FR-36.13.
- **FR-36.15 Postdated checks** — Should. The system shall register customer postdated checks received and vendor postdated checks issued with a check date later than the registration date, hold them in PDC clearing accounts, deposit or present them at maturity, and handle bounced checks.
  - Notes: customer PDC lifecycle Registered → Deposited (maturity deposit journal Dr Bank / Cr PDC clearing) → Cleared | Bounced (reverse, restore customer open item, bounce fee line, customer credit hold event to wave 30); vendor PDC Issued → Presented → Cleared, with the vendor open item settled at registration (parameter `settle-vendor-on-registration`) or at clearing; withholding tax (wave 13) computed at check date when `withholding-at-pdc-settlement` is on (AX R3 India / Thailand behavior); check date drives the FR-36.9 forecast bucket; the `maturity-deposit` job (7.36.16) proposes deposits per bank account daily.
- **FR-36.16 Cash journal (petty cash)** — Must. The system shall maintain cash desks per legal entity and site with a currency, GL cash account and number sequence; record receipts and payments by business transaction type; and close each day with a counted balance.
  - Notes: business transaction types Receipt, Payment, CustomerReceipt (settles wave-30 open items), VendorPayment (settles wave-19 open items), BankDeposit, BankWithdrawal, each with default main account, tax code and dimension defaults; documents are Saved (editable) then Posted (journal API, source document = cash journal document); daily close requires `counted = computed closing` where computed = opening + Σ receipts − Σ payments, otherwise a difference line posts to the cash difference account up to a parameterized limit (default USD 10.00 / EUR 10.00) and above it the close is blocked; negative desk balance is blocked at posting; receipt printout per document; maximum 50 desks per legal entity.
- **FR-36.17 Bank fee analysis (camt.086)** — Should. The system shall import camt.086 bank services billing statements, store fee lines by AFP service code with volume, unit price and tax, compare them to the bank fee agreement, and report variances.
  - Notes: BankFeeAgreement holds expected unit price per (bank, service code, currency) with effective dates; variance = (billed unit price − agreed) × volume plus unexpected service codes; tolerance default 1% per line; monthly fee accrual posts optionally (Dr Bank fees / Cr Accrued bank fees) and is cleared by the statement charge line (BTC CHRG) through FR-36.3; 12 months of statements retained online, 7 years archived.
- **FR-36.18 Treasury deals (FX spot / forward / swap, fixed-term deposits)** — Should. The system shall capture deals with counterparty bank, trade date, value dates, buy and sell amounts, rate and forward points, confirm them against the bank confirmation, generate settlement flows, value open deals at key dates and post the results.
  - Notes: deal types FxSpot, FxForward, FxSwap (near and far legs), FixedTermDeposit; status Draft → Confirmed (manual or MT300 / camt.054 match) → Settled → Closed, with Canceled from Draft or Confirmed; settlement flows become treasury payment requests (FR-36.21) on value date; key-date valuation of a forward: mark-to-market = (market forward rate for the remaining tenor − contract rate) × notional, converted at the spot rate, discounted with the wave-12 yield curve when `discount-mtm` is on; unrealized gain / loss posts to P&L (no hedge accounting) and reverses on the first day of the next period; deposits accrue interest daily with the deal's day-count convention (ACT/360 default, ACT/365, 30/360); accounting codes map (deal type, flow type) to accounts; worked example: forward sell EUR 1,000,000 against USD at 1.1000; at the key date the market forward for the remaining tenor is 1.0850, so the deal value to the EUR seller = (1.1000 − 1.0850) × 1,000,000 = USD 15,000.00 unrealized gain (undiscounted); maximum 20,000 open deals per tenant.
- **FR-36.19 Loan contracts (bank and intercompany)** — Should. The system shall record loan contracts as lender or borrower with principal, schedule type, interest terms and day-count convention, generate the repayment and interest schedule, accrue interest per period and create the settlement flows.
  - Notes: schedule types Annuity, Linear (equal principal), Bullet; rate fixed or floating (reference index + spread with fixings imported from the wave-12 rate service); day-count ACT/360, ACT/365, 30/360; accrual posts Dr Interest expense / Cr Accrued interest (borrower) or Dr Accrued interest / Cr Interest income (lender); intercompany loans create both entities' contracts from one record, post interest as an IC journal (wave 40 due-to / due-from matrix) each period, which covers F.52-style balance interest for IC loans; worked example: EUR 1,000,000 bullet, 3.00% ACT/360, 30-day period → interest = 1,000,000 × 0.03 × 30 / 360 = 2,500.00; early repayment recomputes the remaining schedule prospectively; schedules contribute to FR-36.9 and FR-36.20.
- **FR-36.20 FX exposure** — Could. The system shall derive FX exposures per currency pair and bucket from AP and AR open items, open purchase and sales orders, forecast entries, loan flows and deals, and present the net open exposure after deals.
  - Notes: exposure = Σ inflows − Σ outflows per currency and bucket (same buckets as FR-36.9) − Σ deal legs maturing in the bucket; hedge ratio = hedged / gross exposure; export CSV for the treasury committee; no automatic deal proposals.
- **FR-36.21 Treasury payment requests and bank-to-bank transfers** — Must. The system shall create payment requests that are not vendor open items (transfers between own bank accounts, third-party treasury payments, deal and loan settlements), approve them, and hand them to the wave-19 payment engine with category `TreasuryTransfer`.
  - Notes: repetitive templates (SAP FRFT equivalent) store counterparty, accounts, amount and purpose for one-click requests; four-eyes approval required above a per-entity threshold (default USD 50,000 / EUR 50,000), with the approver different from the creator (SoD check in OPA); intercompany transfers post due-to / due-from through the wave-40 matrix; a request for a Draft or Inactive bank account is rejected; cutoff times per bank from FR-36.23 determine the execution date.
- **FR-36.22 Bank chains** — Should. The system shall maintain correspondent and intermediary bank chains per currency, receiving bank country and receiving BIC, and apply them when generating payment files.
  - Notes: up to 3 hops per chain; written to pain.001 `IntrmyAgt1–3` and MT103 fields 56 / 57 by the wave-19 format engine; BIC validated (ISO 9362); chains effective-dated; a payment to a currency / country without a chain is not blocked.
- **FR-36.23 Payment hub routing and monitor** — Should. The system shall route approved payment batches from every legal entity to a channel and format profile by rule, batch them per bank cutoff, and show one monitor of all batches with bank status.
  - Notes: rule key (legal entity, bank account, payment method, currency, amount band, urgency) → (connection, format profile); batching windows per connection (for example every 30 minutes and a final run 15 minutes before the bank cutoff); status from pain.002 and camt.054 updates the batch and the underlying payments (wave 19 / wave 30); central batch approval optional per entity (BCM-style); rules are versioned and tested against the last 30 days of payments before activation (dry-run report of routing changes).
- **FR-36.24 In-house cash current accounts** — Could. The system shall run an in-house cash center at one legal entity with current accounts per participating entity, internal payment orders between them, interest conditions, and monthly statements that the participants import like bank statements.
  - Notes: flag `treasury.ihc`; IhcAccount per (participant entity, currency) with credit and debit interest conditions (fixed or index + spread), overdraft limit; internal payment order debits the payer account and credits the payee account same day and posts due-to / due-from through the wave-40 matrix in both entities; monthly interest capitalizes to the account; a camt.053 is generated per account at month end and on demand and imported by the participant through FR-36.2 so that FR-36.3 reconciles it; external payments on behalf of participants go through FR-36.23 routing in v1 without passing through the IHC account.
- **FR-36.25 Balance interest on GL and intercompany accounts** — Could. The system shall calculate interest on the daily balances of selected main accounts (typically intercompany current accounts and shareholder accounts) using interest indicators with debit and credit rates, and post the interest with an interest advice.
  - Notes: interest indicator = (rate table or reference index + spread, day-count, calculation period, minimum amount 1.00); interest = Σ_days balance_d × rate_d / basis, separately for debit and credit days; balance per value date from the wave-11 balance projection; postings Dr Interest receivable / Cr Interest income (or the reverse) per account and dimension; for intercompany accounts the counterpart posts in the partner entity through the wave-40 IC journal (FR-40.5); reuses the day-count library of FR-36.19; run is per legal entity and period and is rerunnable after reversal only.

### 7.36.4 Data model

**BankAccount**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| legal_entity_id | uuid | yes | FK org (wave 6) |
| account_code | text(20) | yes | unique per entity |
| iban / account_number | text, encrypted | yes | one of the two; masked in API |
| bic / routing_number | text | no | validated |
| currency | char(3) | yes | ISO 4217 |
| main_account_id | uuid | yes | FK ledger account (wave 11) |
| status | enum | yes | Draft/Active/Inactive/Closed |
| next_check_number | bigint | no | gap-free sequence |
| signatories | jsonb | no | [{partyId, limit, currency}] |

**BankStatementLine**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| statement_id | uuid | yes | FK BankStatement |
| line_no | int | yes | unique in statement |
| booking_date / value_date | date | yes | |
| amount | numeric(19,4) | yes | signed, statement currency |
| btc_code | text | no | ISO 20022 domain/family/subfamily |
| references | jsonb | no | EndToEndId, InstrId, Ustrd, chequeNumber |
| counterparty_name | text | no | normalized for trigram index |
| status | enum | yes | Unmatched/Suggested/Matched/Created/InTransit |
| raw_fragment | text | yes | source XML/text slice |

**ReconciliationMatch**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| reconciliation_id | uuid | yes | FK ReconciliationHeader |
| statement_line_ids | uuid[] | yes | 1..50 |
| bank_transaction_ids | uuid[] | yes | 1..50 |
| score | smallint | yes | 0–100 |
| rule_id | uuid | no | null when manual |
| matched_by | text | yes | user or `rule-engine` |
| matched_at | timestamptz | yes | |
| reversed_at | timestamptz | no | |

**CashForecastEntry**: id, legal_entity_id, source_type (AP/AR/PO/SO/Recurring/Manual/Payroll/Project), source_id, expected_date, amount, currency, confidence (0–1), bucket_key, computed_at.

**Entities added by FR-36.13–FR-36.25** (summaries; tables follow the same conventions as above):
- **TreasuryInstrument**: id, legal_entity_id, kind (BillOfExchange/PromissoryNote/PostdatedCheck), direction (Receivable/Payable), number (per-entity sequence), party_id, bank_account_id, amount numeric(19,4), currency, issue_date, maturity_date (check date for PDC), status (Drawn/Registered/Remitted/Deposited/Presented/Honored/Settled/Cleared/Protested/Bounced/Redrawn), remittance_id, settled_open_item_ids uuid[], protest_reason_code, replaced_by_id.
- **CashDesk**: id, legal_entity_id, site_id, code, currency, main_account_id, difference_account_id, difference_limit numeric(19,4), number_sequence_id, status. **CashJournalDocument**: id, desk_id, document_no, business_date, transaction_type, amount, tax_code, party_id, open_item_ids, dimensions, status (Saved/Posted/Reversed), journal_entry_id. **CashDeskDay**: desk_id, business_date, opening, receipts, payments, computed_closing, counted, difference, status (Open/Closed/Reopened), closed_by.
- **BankFeeStatement / BankFeeLine**: statement_id, bank_party_id, billing_period, line: service_code (AFP), description, volume numeric(19,4), unit_price numeric(19,6), amount, tax, agreed_unit_price, variance, status (Within/Over/Under/Unexpected).
- **TreasuryDeal**: id, legal_entity_id, deal_no, type (FxSpot/FxForward/FxSwap/FixedTermDeposit), counterparty_bank_id, trade_date, near_value_date, far_value_date, buy_currency, buy_amount, sell_currency, sell_amount, contract_rate numeric(19,8), forward_points numeric(19,8), interest_rate numeric(9,6), day_count, status, confirmation_ref, flows jsonb [{flowType, valueDate, amount, currency, paymentRequestId, journalEntryId}].
- **LoanContract**: id, legal_entity_id, role (Borrower/Lender), counterparty_party_id, ic_partner_id, principal, currency, start_date, maturity_date, schedule_type (Annuity/Linear/Bullet), rate_type (Fixed/Floating), fixed_rate, index_code, spread_bp, day_count, status (Draft/Active/Repaid/Terminated); **LoanScheduleLine**: period_no, start, end, opening_principal, interest, principal_repayment, closing_principal, fixing_rate, accrual_journal_id, settlement_request_id.
- **TreasuryPaymentRequest**: id, legal_entity_id, category (TreasuryTransfer/DealSettlement/LoanSettlement/ThirdParty/IhcExternal), from_bank_account_id, to_bank_account_id or beneficiary jsonb, amount, currency, requested_date, execution_date, template_id, status (Draft/Submitted/Approved/Routed/Sent/Executed/Rejected), approver_id. **BankChain**: currency, receiving_country, receiving_bic, hops[1..3] (BIC, account), effective_from. **PaymentRoutingRule**: version, match keys, connection_id, format_profile_id, batch_window_min, cutoff_local_time.
- **IhcAccount**: id, center_entity_id, participant_entity_id, currency, credit_condition, debit_condition, overdraft_limit, balance (projection), status; **IhcPaymentOrder**: id, payer_account_id, payee_account_id, amount, value_date, status, journal_pair_id.
- **InterestIndicator / InterestRun**: indicator code, rate table, day_count, period; run id, legal_entity_id, period, accounts[], total_debit_interest, total_credit_interest, journal_entry_ids.

Invariants: Σ amounts of matched statement lines = Σ amounts of matched bank transactions ± configured tolerance; a bank transaction belongs to at most one non-reversed match; statement closing balance = opening balance + Σ lines; a closed reconciliation is immutable. Indexes: statement lines by (bank_account_id, value_date), GIN trigram on counterparty_name, partial index on status = Unmatched; CashForecastEntry partitioned monthly by expected_date, retention 24 months.

### 7.36.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/treasury/bank-accounts | Create account | Idempotency-Key | TreasuryBankAccountMaintain |
| POST | /api/v1/treasury/statements/import | Upload statement file (multipart) | file SHA-256 | TreasuryStatementImport |
| POST | /api/v1/treasury/reconciliations/{id}/run-rules | Execute rule set | runId | TreasuryReconcile |
| POST | /api/v1/treasury/reconciliations/{id}/matches | Manual match | Idempotency-Key | TreasuryReconcile |
| POST | /api/v1/treasury/reconciliations/{id}/close | Close | state check | TreasuryReconcileApprove |
| POST | /api/v1/treasury/checks/{id}/void | Void check | state check | TreasuryCheckMaintain |
| GET | /api/v1/treasury/cash-position?entity=&asOf= | Position | n/a | TreasuryView |
| GET | /api/v1/treasury/cash-forecast?entity=&horizon= | Forecast buckets | n/a | TreasuryView |
| POST | /api/v1/treasury/netting-cycles | Propose netting | Idempotency-Key | TreasuryNetting |
| POST | /api/v1/treasury/connections/{id}/send | Send file via channel | file hash | TreasuryBankSend |
| POST | /api/v1/treasury/instruments | Draw bill / note, register PDC | Idempotency-Key | TreasuryInstrumentMaintain |
| POST | /api/v1/treasury/instruments/{id}/remit | Remit (collection/discount) or deposit | state check | TreasuryInstrumentMaintain |
| POST | /api/v1/treasury/instruments/{id}/protest | Protest bill / bounce PDC | state check | TreasuryInstrumentSettle |
| POST | /api/v1/treasury/cash-desks/{id}/documents | Post cash journal document | Idempotency-Key | TreasuryCashDeskPost |
| POST | /api/v1/treasury/cash-desks/{id}/days/{date}/close | Close desk day with count | state check | TreasuryCashDeskPost |
| POST | /api/v1/treasury/fee-statements/import | Import camt.086 | file SHA-256 | TreasuryStatementImport |
| POST | /api/v1/treasury/deals | Capture deal | Idempotency-Key | TreasuryDealCapture |
| POST | /api/v1/treasury/deals/{id}/confirm | Confirm against bank confirmation | state check | TreasuryDealConfirm |
| POST | /api/v1/treasury/valuation-runs | Key-date valuation (dryRun option) | (entity, keyDate) | TreasuryDealValuate |
| POST | /api/v1/treasury/loans | Create loan and schedule | Idempotency-Key | TreasuryLoanMaintain |
| POST | /api/v1/treasury/payment-requests | Create request or from template | Idempotency-Key | TreasuryPaymentRequestCreate |
| POST | /api/v1/treasury/payment-requests/{id}/approve | Approve (four-eyes) | state check | TreasuryPaymentRequestApprove |
| PUT | /api/v1/treasury/routing-rules/{version} | Publish routing rules after dry run | version | TreasuryRoutingConfigure |
| POST | /api/v1/treasury/ihc/payment-orders | Internal payment order | Idempotency-Key | TreasuryIhcOperate |
| POST | /api/v1/treasury/interest-runs | GL balance interest run | (entity, period) | TreasuryInterestRun |

Commands: `ImportStatement` (validates schema, rejects duplicates), `RunMatching` (rule set must be Active), `ConfirmMatch` (amount tolerance ≤ rule tolerance), `CloseReconciliation` (difference = 0). Queries served from read models.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.treasury.statement.imported.v1` | import committed | statementId, bankAccountId, lineCount, openingBalance, closingBalance | cashforecast-svc, treasury-svc matcher |
| `dev.passage.treasury.statement.reconciled.v1` | reconciliation closed | statementId, matchedCount, createdCount, closingBalance | journal, analytics |
| `dev.passage.treasury.check.voided.v1` | void | checkId, bankAccountId, number, reason | AP (19) positive pay |
| `dev.passage.treasury.cashposition.updated.v1` | position refresh | legalEntityId, asOf, balances[] | dashboards, interco-svc |
| `dev.passage.treasury.file.sent.v1` | outbound file acknowledged | connectionId, fileHash, channel, ackStatus | audit |
| `dev.passage.treasury.instrument.statuschanged.v1` | bill / note / PDC status change | instrumentId, kind, fromStatus, toStatus, amount, maturityDate | AR (30), AP (19), cashforecast-svc |
| `dev.passage.treasury.pdc.bounced.v1` | PDC bounced | instrumentId, customerId, amount, fee, reasonCode | AR (30) credit hold |
| `dev.passage.treasury.cashdesk.closed.v1` | desk day closed | deskId, businessDate, closing, difference | journal audit, analytics |
| `dev.passage.treasury.deal.valued.v1` | valuation posted | runId, keyDate, dealCount, unrealizedTotal, journalEntryId | analytics, consol-svc |
| `dev.passage.treasury.paymentrequest.approved.v1` | request approved | requestId, category, amount, currency, executionDate | bank-connect-svc routing |
| `dev.passage.treasury.ihc.statement.generated.v1` | IHC month-end statement | ihcAccountId, participantEntityId, period, closingBalance, fileRef | treasury-svc import (participant) |

### 7.36.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Bank accounts | Create, edit (workflow), close, view balances | code, IBAN (masked), currency, GL account, status, last statement | TreasuryBankAccountMaintain |
| Reconciliation workspace | Run rules, confirm, split, create transaction, close | line date/amount/reference, candidate score, difference, unmatched count | TreasuryReconcile |
| Checks | Print, void, reprint, stop | number, payee, amount, status | TreasuryCheckMaintain |
| Facilities and instruments | Create LC/guarantee, amend, close | facility, limit, utilization, expiry | TreasuryFacilityMaintain |
| Cash position | Filter entity/currency, drill to statement | account, balance, age, same-day pending | TreasuryView |
| Cash forecast | Change horizon, add manual entry, drill to source | bucket, inflow, outflow, net, cumulative | TreasuryView |
| Bank connections | Test, send, view log | channel, status, last file, ack | TreasuryBankSend |

Interaction rules: the reconciliation grid supports keyboard `m` (match selected), `u` (unmatch), `n` (new transaction); multi-select up to 50 lines each side with running difference shown; rule changes require re-run before close; column layouts and filters persist per user via the wave-5 shell personalization.

### 7.36.7 Business rules and edge cases

- **BR-36.1** Score = Σ (criterion weight × criterion result ∈ [0,1]); amount tolerance criterion result = 1 − |Δ|/tolerance clipped at 0; date window result = 1 − |days|/window.
- **BR-36.2** Ties above the auto threshold with more than one candidate are downgraded to Suggested; auto-match requires a unique top candidate at least 5 points above the next.
- **BR-36.3** Reconciliation state machine: Open → InProgress → Closed; Closed → Reopened only by `TreasuryReconcileApprove` within the open fiscal period; reopening reverses nothing, it only allows new matches.
- **BR-36.4** Statement currency ≠ bank account currency is rejected; FX differences between book and bank on foreign receipts post to realized gain/loss (wave 12) with the statement line as source.
- **BR-36.5** A void after Sent and before Cleared triggers a positive pay void record; void after Cleared is rejected.
- **BR-36.6** Forecast bucket boundaries use the legal entity's calendar; a contribution whose expected date falls on a non-banking day moves to the next banking day.
- **BR-36.7** Concurrency: two users confirming matches on the same line receive 409 on the second write (optimistic version on StatementLine).
- **BR-36.8** Duplicate statement file with identical hash returns 200 with the existing statement id; same statement id with different closing balance returns 409.
- **BR-36.9** Facility utilization uses the instrument's original currency converted at the daily rate; limit breaches recomputed nightly and on rate change > 2%.
- **BR-36.10** Bill of exchange state machine: Drawn → Remitted → Honored | Protested; Protested → Redrawn (new bill, old bill closed) or → ReturnedToCustomer (open item restored with protest fee); a bill cannot be remitted twice; remittance for discount books the discount expense on remittance date, not at maturity.
- **BR-36.11** Promissory note state machine: Drawn → Remitted → Settled; Drawn → Redrawn; settlement requires a matched bank statement line or a manual settlement with duty `TreasuryInstrumentSettle`; the vendor open item is settled at draw and never reopened except through a redraw reversal.
- **BR-36.12** Postdated check: a deposit before the check date is rejected unless the parameter `allow-early-deposit` is on; a bounce after Cleared is a new customer transaction (fee + reversal) with reason code; bounced customer checks raise `dev.passage.treasury.pdc.bounced.v1` consumed by wave-30 credit hold.
- **BR-36.13** Cash desk day close: closing balance = opening + Σ receipts − Σ payments in desk currency; |counted − closing| ≤ cash difference limit posts a difference line, above the limit the close is blocked; no document can post to a closed day; reopening a closed day requires `TreasuryCashDeskApprove` and the day's reopen is audited.
- **BR-36.14** Deal valuation entries are per deal and key date, reversed on the first calendar day of the next period before the new valuation; a deal settled between two key dates realizes gain / loss = settlement rate difference × notional and the last unrealized entry is reversed in the same journal.
- **BR-36.15** Interest accrual uses the contract day-count; ACT/360 for a leap-year February period counts 29 days; accrual rounding half-up to the minor unit per period, and the final period of a schedule absorbs the rounding residual so Σ interest = Σ scheduled interest.
- **BR-36.16** Treasury payment requests above the four-eyes threshold require an approver different from the creator and from the last editor; OPA denies self-approval; an edit after approval returns the request to Draft.
- **BR-36.17** In-house cash internal payment orders value the same day; an order exceeding the payer's overdraft limit is rejected unless the `TreasuryIhcOverride` duty approves; interest capitalizes on the last calendar day of the month at the conditions in force per day (daily balance method).
- **BR-36.18** Bank fee variance: a fee line is Within tolerance when |billed − agreed| / agreed ≤ 1%; a service code with no agreement line is Unexpected; a Within line never raises a message, an Over line raises TRS-0418 and an Unexpected line TRS-0419.

### 7.36.8 Acceptance criteria

- **AC-36.1** (FR-36.2) Given a camt.053 file with 1,000 entries, when imported, then 1,000 lines persist, opening + Σ lines = closing, and a re-upload returns the same statement id.
- **AC-36.2** (FR-36.3) Given the 1,000-line reconciliation fixture, when rules run, then ≥ 950 lines are auto-matched correctly and 0 are matched to the wrong transaction.
- **AC-36.3** (FR-36.5) Given a line for a USD 12.50 bank fee, when the user creates a transaction, then a journal entry Dr Bank fees 12.50 / Cr Bank 12.50 posts with the line as source document.
- **AC-36.4** (FR-36.5) Given a difference of 0.01, when closing, then the close is rejected with the difference displayed.
- **AC-36.5** (FR-36.6) Given check 1001 Printed, when voided and reprinted, then 1001 is Voided and 1002 is Printed with the same payee and amount.
- **AC-36.6** (FR-36.7) Given a facility of EUR 1,000,000 with EUR 950,000 utilized, when an LC of EUR 100,000 is created, then it is blocked without the override duty.
- **AC-36.7** (FR-36.9) Given 10,000 generated open items, when the forecast computes, then every bucket equals the sum of its contributions to the cent.
- **AC-36.8** (FR-36.12) Given an EBICS sandbox, when a pain.001 is sent, then the file hash, signature reference and bank acknowledgment are in the audit log and no private key bytes appear in any log or DB.
- **AC-36.9** (FR-36.13) Given a customer bill of EUR 10,000.00 drawn against two invoices and remitted for discount at 2% fee, when remitted, then the journal shows Dr Bank 9,800.00, Dr Discount expense 200.00 / Cr Bills remitted 10,000.00, and the two invoices are settled.
- **AC-36.10** (FR-36.13) Given a remitted bill, when the bank reports non-payment and the user protests it with a EUR 25.00 fee, then the customer open item of 10,025.00 is restored and the bill status is Protested.
- **AC-36.11** (FR-36.15) Given a customer PDC of USD 5,000.00 dated 2026-11-15 registered on 2026-10-20, when the forecast computes, then 5,000.00 sits in the 2026-11-15 bucket; when deposited on 2026-11-15 and bounced on 2026-11-18, then the open item of 5,000.00 plus a 15.00 fee is restored.
- **AC-36.12** (FR-36.16) Given a desk opening at EUR 500.00 with receipts 120.00 and payments 80.00, when the cashier counts 539.50 and closes, then a difference line of −0.50 posts and the day is Closed; when the cashier counts 525.00, then the close is rejected with TRS-0431.
- **AC-36.13** (FR-36.17) Given a camt.086 with service code `010100` billed at 0.26 for 1,000 items and an agreement of 0.25, when imported, then the variance line shows 10.00 Over (4%) and raises TRS-0418.
- **AC-36.14** (FR-36.18) Given the forward sell EUR 1,000,000 at 1.1000 example and a market forward of 1.0850 at the key date, when valued, then an unrealized gain of USD 15,000.00 posts and reverses on the first day of the next period.
- **AC-36.15** (FR-36.19) Given the EUR 1,000,000 bullet IC loan at 3.00% ACT/360, when the 30-day period accrues, then the lender posts Dr Due from borrower 2,500.00 / Cr Interest income 2,500.00 and the borrower the mirror entry with one ic_pair_id.
- **AC-36.16** (FR-36.21) Given a transfer request of USD 75,000.00 created by user A, when A attempts to approve, then OPA denies it; when user B approves, then a pain.001 with category `TreasuryTransfer` is routed by FR-36.23 to the bank's connection.
- **AC-36.17** (FR-36.25) Given an IC current account with a debit balance of EUR 200,000.00 for 31 days at 4.00% ACT/360, when the interest run executes for the month, then interest = 200,000 × 0.04 × 31 / 360 = 688.89 posts in the lender and the mirror entry posts in the borrower.

### 7.36.9 Test plan and fixtures

Fixtures: `recon-1000` (1,000 statement lines across camt.053/BAI2/MT940 with 60% exact, 25% reference-only, 10% N:1 batch receipts, 5% fees, generated from wave 19/30 payment fixtures); `forecast-10k` (generated open items with due dates across 12 months); `ebics-sandbox` (vendor sandbox recordings for contract tests); `facility-20` (LCs and guarantees with amendments).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Scoring function | Unit | rule engine | deterministic scores per criterion table |
| Statement parsers | Contract | camt.053 v02/v08, BAI2, MT940 | 100% of fixture files parse; schema errors reported with line |
| Match uniqueness | Property | reconciliation | no transaction in two active matches |
| Forecast additivity | Property | cashforecast-svc | bucket = Σ contributions |
| Reconcile-and-close | E2E | workspace | recon-1000 closes with difference 0 |
| Import throughput | Performance | import API | 10,000 lines < 30 s; 200,000 lines < 10 min |
| Adapter contracts | Contract | SFTP/EBICS/XS2A | sandbox round-trip with acknowledgment |

Reconciliation checks: Σ BankAccount book balance = GL bank main account balance per entity nightly; Σ outstanding checks = positive pay open items; cash position = latest statement closing + pending.

### 7.36.10 Deployment and operations

Helm charts `treasury-svc`, `bank-connect-svc`, `cashforecast-svc`; config: `TREASURY_AUTO_MATCH_THRESHOLD=90`, `TREASURY_MAX_FILE_MB=50`, Vault role `bank-connect`. Atlas migrations generate tables from entity metadata. Feature flags: `treasury.ml-suggestions`, `treasury.cash-pooling`. Scheduled jobs (Temporal): `statement-poll` every 15 min per connection, `cash-position-refresh` hourly, `forecast-rebuild` hourly and on demand, `facility-expiry-check` daily 06:00. SLOs: import p95 < 30 s per 10k lines, rule run p95 < 20 s per 10k lines, forecast API p95 < 300 ms. Alerts: unreconciled lines > 7 days, connection failure > 3 retries, key rotation due. Runbook: resend file, rebuild forecast, rotate EBICS keys. Rollback: Helm rollback; matches are append-only with reversals, so no data rollback.

### 7.36.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| BankAccountTable, BankAccountTrans (open items) | BankAccount, BankTransaction | opening balance as of cutover; unreconciled items migrated as InTransit |
| BankChequeTable | Check | status map; next number continues |
| BankLCImport/Export, BankLGTable, BankFacility | LetterOfCredit, Guarantee, Facility | open instruments only |
| T012/T012K, BAM accounts | BankAccount | house bank + account id → account code |
| FEBKO/FEBEP (last 90 days) | BankStatement, StatementLine | for matching history and ML training |
| PAYR | Check | |
| LedgerCov / FQM_FLOW | not migrated | forecast is recomputed from sources |

Validation: Σ migrated bank balances = GL bank accounts; IBAN checksums pass. Not migrated: closed reconciliations older than 90 days, matching rule configurations (re-authored, since scoring semantics differ).

### 7.36.12 Dependencies, risks, open questions

Depends on: wave 11 journal posting, wave 12 rates, wave 19 payment files and positive pay, wave 30 cash application, wave 35 integration hub for SFTP transport, wave 5 Vault integration.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Bank-specific camt dialects | High | Medium | Per-bank profile overrides; fixture per bank |
| ML suggestions mislead users | Medium | Low | Suggestions only, reasons shown, precision tracked |

Open questions: (1) Default auto-match threshold 90 or 95? Proposed 90; decide by wave 36 build start. (2) EBICS versions to support (2.5 and 3.0)? Proposed 3.0 only; decide by wave 36. (3) Cash pooling in v1? Proposed Could; decide at phase 7 exit.

### 7.36.13 Parameters and setup

Parameters live in the `Treasury parameters` page (one tab per area, mirroring AX `BankParameters` tabs) and are versioned with effective dates; changes are audited and take effect for documents created after the change.

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `recon.autoMatchThreshold` | legal entity | int | 90 | 60–100 | score at or above which the engine auto-matches (FR-36.3) | BankReconciliationMatchingRuleSet, Cash and bank management parameters → Bank reconciliation | FEBAN interpretation algorithm + OT83 posting rule assignment |
| `recon.suggestThreshold` | legal entity | int | 60 | 1–(auto − 1) | lower bound of Suggested status | matching rule "Mark as matched" vs manual | OTPM search string relevance (no score) |
| `recon.tieMargin` | legal entity | int | 5 | 0–20 | points the top candidate must lead by (BR-36.2) | none | none |
| `statement.maxFileMb` | tenant | int | 50 | 1–200 | rejects larger uploads (FR-36.2) | DMF file size limit | FF_5 file size (no limit setting) |
| `statement.sequenceGapPolicy` | bank account | enum | Warn | Ignore / Warn / Block | handling of missing statement numbers | none | FF_5 "check statement number" |
| `statement.btcMap` | bank account | table | ISO 20022 defaults | domain/family/subfamily → transaction type | maps bank codes to bank transaction types and default offset accounts | BankTransType, Bank statement transaction code mapping | OT83 external transaction types → posting rules (T028G) |
| `position.staleHours` | legal entity | int | 24 | 1–168 | flags old balances in cash position (FR-36.8) | none | FF7A display (no flag) |
| `forecast.horizon` | legal entity | struct | daily 30 / weekly 13 / monthly 12 | daily 1–90, weekly 1–52, monthly 1–36 | bucket layout (FR-36.9) | Cash flow forecasting setup (LedgerCovParameters) | OT14 planning levels, OT13 planning groups, FF7B horizon |
| `forecast.arDelayMode` | legal entity | enum | CustomerDsoDelta | None / CustomerDsoDelta / CustomerAverageDaysLate | adjusts AR expected dates | Cash flow forecasting setup → receivables payment history | FF7B planning level "customer payment history" |
| `check.voidReasonRequired` | bank account | bool | true | true / false | void without reason rejected | Bank account → Checks → "Require void reason" | FCH8 reason for void (T012V void reason codes) |
| `check.regeneratePositivePay` | bank account | bool | true | true / false | positive pay record on void (BR-36.5) | BankPositivePayFormat | FCHX positive pay extract |
| `facility.expiryAlertDays` | legal entity | int[] | 30, 7, 1 | 1–365 each | LC / guarantee expiry alerts (FR-36.7) | Letters of credit → expiry notification | none native |
| `netting.residualWriteOff` | legal entity | money | 1.00 | 0.00–100.00 | residual to IC rounding account (FR-36.10) | none | IHC netting tolerance |
| `paymentRequest.fourEyesThreshold` | legal entity | money | USD 50,000 / EUR 50,000 | ≥ 0 | second approver required above (FR-36.21) | Bank → Bank transfer workflow | FIBLFFP payment request approval (F8BT / BCM release strategy) |
| `instrument.billPostingProfile` | legal entity | table | none | status (Drawn/Remitted-Collection/Remitted-Discount/Protested) → account | GL accounts per bill status (FR-36.13) | Accounts receivable parameters → Bill of exchange tab, customer posting profile per bill status | OBXR special G/L indicator W accounts, OBYN bill of exchange charges |
| `instrument.notePostingProfile` | legal entity | table | none | status (Drawn/Remitted/Settled) → account | GL accounts per promissory-note status (FR-36.14) | Accounts payable parameters → Promissory note tab, vendor posting profile | OBYR special G/L W (vendor) |
| `instrument.remittanceTypes` | bank account | enum[] | Collection | Collection / Discount | which remittance modes the bank accepts | Bill of exchange remittance types | F-33 / F-34 discount vs collection |
| `pdc.clearingAccounts` | legal entity | struct | none | receivable PDC account, payable PDC account | holding accounts until clearing (FR-36.15) | Cash and bank management parameters → Postdated checks tab | special G/L indicator S (noted items) |
| `pdc.settleVendorOnRegistration` | legal entity | bool | true | true / false | vendor open item settled at issue or at clearing | Postdated checks → "Settle postdated check" | none |
| `pdc.allowEarlyDeposit` | legal entity | bool | false | true / false | BR-36.12 deposit before check date | none | none |
| `pdc.withholdingAtSettlement` | legal entity | bool | false | true / false | withholding tax at check date (wave 13) | R3 India/Thailand "Calculate withholding tax at settlement" | extended withholding tax at payment (OBWW) |
| `cashDesk.differenceLimit` | cash desk | money | 10.00 desk currency | 0.00–1,000.00 | close blocked above limit (BR-36.13) | none | FBCJC0 cash journal definition (no limit) |
| `cashDesk.businessTransactions` | legal entity | table | 6 seeded types | type, main account, tax code, dimensions | defaults per cash journal line (FR-36.16) | none | FBCJC2 business transactions (TCJ_TRANSACTIONS) |
| `cashDesk.numberSequence` | cash desk | sequence | per desk, gap-free | number sequence id | document numbering | none | FBCJC1 number ranges |
| `fees.tolerancePct` | legal entity | decimal | 1.00% | 0.00–10.00% | Within vs Over (BR-36.18) | none | BAM bank fee analysis tolerance |
| `deal.dayCountDefault` | legal entity | enum | ACT/360 | ACT/360 / ACT/365 / 30/360 / ACT/ACT | interest on deposits and loans | none | TRM product type interest calculation method (FTR customizing JBD*) |
| `deal.accountingCodes` | legal entity | table | seeded per deal type | (deal type, flow type, update type) → accounts | posting of flows and valuations (FR-36.18) | none | TRM accounting codes, update types, valuation areas (TPM customizing) |
| `deal.discountMtm` | legal entity | bool | false | true / false | discount MTM with the wave-12 curve | none | TPM valuation class / NPV method |
| `loan.defaultSchedule` | legal entity | enum | Annuity | Annuity / Linear / Bullet | schedule template (FR-36.19) | none | CML product types / condition types |
| `interest.indicators` | main account | table | none | indicator code, rates, day count, period, min amount | GL balance interest (FR-36.25) | none | OB46 interest indicators, OB82 interest terms, OB83 reference rates |
| `bankChain.searchMode` | legal entity | enum | ByCurrencyAndCountry | ByCurrency / ByCurrencyAndCountry / ByReceivingBic | chain lookup order (FR-36.22) | none | FIBC bank chain scenario (TBCHAIN11/21/31) |
| `routing.batchWindowMin` | connection | int | 30 | 5–1,440 | batching window (FR-36.23) | none | BCM batch rules (Bank Communication Management → merge criteria) |
| `routing.cutoffLocalTime` | connection | time | 15:00 bank local | 00:00–23:59 | requests after cutoff move to next banking day | Bank → Bank holidays / payment calendar (wave 19) | BCM payment medium cutoff, FBZP value days |
| `ihc.conditions` | IHC account | struct | none | credit / debit rate, index + spread, overdraft limit | IHC interest and limits (FR-36.24) | none | IHC0 conditions, IHC account limits |

### 7.36.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Reconciliation status | list inquiry | entity, bank account, period, status | statement, lines, matched, suggested, unmatched, difference, age | `recon_status_rm` | Bank reconciliation list (BankReconciliationHeader) | FEBAN, FEB_BSPROC |
| Unmatched lines aging | operational report | entity, account, age bands 0–7 / 8–30 / > 30 days | count, amount per band, top counterparties | `statement_line` partial index Unmatched | Bank statement lines (unreconciled) | FEBAN worklist, FF_6 |
| Cash position | KPI / drill-down | entity, currency, as-of | balance, age, same-day pending in/out, converted total | `cash_position_rm` | Bank account balance FactBox | FF7A |
| Cash flow forecast | drill-down | entity, horizon, source types, currency | inflow, outflow, net, cumulative, by source | `cash_forecast_entry` | Cash flow forecast workspace (LedgerCov) | FF7B, FQM liquidity forecast |
| Check register / outstanding checks | list inquiry | bank account, status, date range | number, payee, amount, status, cleared date | `check` | Check (BankChequeTable), Outstanding checks report | FCHN, FCH6 (PAYR) |
| Facility utilization | KPI | facility, bank, currency | limit, utilization, headroom, expiring in 30 days | `facility_rm` | Bank facilities, LC inquiry | none native (TRM / Trade Finance) |
| Bill of exchange list (customer) | list inquiry | status, maturity range, remittance, customer | number, customer, amount, maturity, status, bank | `treasury_instrument` kind = Bill | Bill of exchange inquiry (CustBillOfExchangeJour) | S_ALR_87012199 |
| Promissory note list (vendor) | list inquiry | status, maturity range, vendor | number, vendor, amount, maturity, status | `treasury_instrument` kind = Note | Promissory note inquiry (VendPromissoryNoteJour) | S_ALR_87012116 |
| Remittance list | operational report | remittance id, bank, type | bills, totals, discount fee, net proceeds | `instrument_remittance` | SSRS Bill of exchange remittance list | FBWE output list |
| Postdated check inquiry | list inquiry | direction, check date range, status, party | check no, bank, check date, amount, status | `treasury_instrument` kind = PDC | Postdated checks (CustPostdatedChecks / VendPostdatedChecks) | none |
| Cash book | statutory report | cash desk, date range | opening, documents (no, type, text, receipt, payment), running balance, closing, difference | `cash_journal_document`, `cash_desk_day` | none | S_ALR_87012309, FBCJ print |
| Bank fee analysis | operational report | bank, period, service code | volume, billed, agreed, variance, status, trend 12 months | `bank_fee_line` | none | Fiori Bank Fee Analysis / Bank Relationship Overview |
| Treasury position list | list inquiry | entity, deal type, counterparty, key date | deal, nominal, rate, MTM, accrued interest, maturity | `deal_position_rm` | none | TPM12 |
| Treasury flow list | list inquiry | entity, value date range, flow type | deal / loan, flow type, amount, currency, posted, payment request | `deal_flow`, `loan_schedule_line` | none | TPM13 |
| Treasury posting journal | operational report | entity, period, deal type | flow / valuation postings with accounts and journal ids | `deal_posting_log` | none | TPM20 |
| FX exposure | KPI / drill-down | entity, currency pair, horizon | gross exposure, hedged, net open, hedge ratio | `fx_exposure_rm` | none | Exposure management 2.0 reports |
| Loan schedule and interest | drill-down | loan, role, period | opening, interest, repayment, closing, fixing | `loan_schedule_line` | none | FNO5 / CML flow overview |
| Payment batch monitor | list inquiry | entity, connection, status, date | batch, payments, amount, channel, ack, pain.002 status | `payment_batch_rm` (bank-connect-svc) | none | BNK_MONI, Fiori Monitor Payments |
| IHC account balances | list inquiry | center, participant, currency | balance, overdraft limit, accrued interest, last statement | `ihc_account` | none | IHC account balance (IHC1IP / F9K* reports) |
| Interest run result | operational report | entity, period, indicator | account, debit days interest, credit days interest, posted amount | `interest_run` | none | F.52 log |

### 7.36.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Check | payment with method Check (wave 19) or reprint | header: payer entity, bank, check no, date; body: payee, amount in words and figures; footer: MICR E-13B line (routing, account, check no), signature image per signatory limit | PDF to check stock (laser), print queue | gap-free per bank account (`next_check_number`) | voided checks retained as records 7 years (US), 10 years (DE) |
| Positive pay file | check print / void | header: account, file date; lines: check no, amount, issue date, payee, void flag | bank CSV / BAI-like per profile | per bank account per day | 7 years |
| Bill of exchange | draw (FR-36.13) | drawer, drawee, amount, maturity, place of payment, domiciliation bank, acceptance box | PDF; LCR (CFONB 160), RiBa (CBI), AEB 19 / 58 files via wave-48 pack | per-entity bill sequence | statutory form per country (FR Code de commerce L511-1); 10 years |
| Remittance list and file | remittance (FR-36.13) | remittance id, bank, type, bills (no, drawee, maturity, amount), totals, discount fee | PDF + bank file | per remittance | 10 years |
| Promissory note | draw (FR-36.14) | issuer, beneficiary, amount, maturity, domiciliation | PDF; pagaré layout (ES) via pack | per-entity note sequence | 10 years |
| Cash journal receipt | cash document post (FR-36.16) | desk, document no, date, type, party, amount, tax, cashier, signature line | PDF (A6 / thermal 80 mm) | per desk gap-free | GoBD (DE) single-record immutability; 10 years |
| Cash book (daily / monthly) | day close, on demand | desk, period, opening, document lines, closing, count, difference, closer | PDF, CSV | n/a | DE §146 AO daily recording; 10 years |
| Deal confirmation (outbound) | deal capture (FR-36.18) | deal no, trade date, value dates, amounts, rate, settlement instructions | PDF, MT300 via SWIFT service bureau (wave 35 hub) | deal sequence | EMIR record keeping 5 years after termination |
| Loan interest advice / statement | accrual or settlement (FR-36.19), interest run (FR-36.25) | contract, period, principal, rate, days, interest, withholding | PDF, email to counterparty; IC copy to partner entity | per contract period | transfer-pricing file 10 years |
| IHC account statement | month end / on demand (FR-36.24) | account, opening, internal orders, interest, closing | camt.053.001.08, PDF | per account per period | 10 years |
| Treasury payment order | approval (FR-36.21) | debtor account, creditor, amount, purpose, approvers | pain.001.001.09 | payment request number | 10 years |
| Bank fee variance report | camt.086 import | bank, period, service lines, variance | PDF, CSV | none | none (management report) |

### 7.36.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `statement-poll` | every 15 min | bank connection | file SHA-256 dedupe | < 30 s per connection | retry 5× exponential; channel marked degraded | connection failure > 3 retries |
| `cash-position-refresh` | hourly + on file arrival | legal entity | as-of timestamp | < 20 s per entity | keep last good position, flag stale | position age > 2 h |
| `forecast-rebuild` | hourly + on demand | legal entity | (entity, computedAt) snapshot swap | < 5 min for 1M items | previous snapshot stays live | rebuild failure |
| `facility-expiry-check` | daily 06:00 | legal entity | (instrument, alert day) sent flag | < 1 min | retry next run | none |
| `ml-match-retrain` | nightly 01:00 if ≥ 200 new matches | tenant | model version | < 20 min | keep prior model | precision drop > 5 pts |
| `bank-gl-reconcile` | nightly 02:30 | bank account | (account, date) | < 5 min | report differences | difference ≠ 0 |
| `pdc-maturity-deposit` | daily 07:00 | bank account | (instrument, check date) | < 2 min | proposals only; no posting | proposals older than 2 days |
| `instrument-maturity` | daily 07:00 | legal entity | (instrument, maturity) | < 2 min | remittance / settle proposals | bills past maturity unhonored > 5 days |
| `deal-valuation` | period end (close checklist task, wave 14) and on demand | legal entity | (entity, key date) run id; reversal first | < 5 min for 20,000 deals | run fails whole; nothing posts | missing market rates |
| `valuation-reversal` | first calendar day of period 00:30 | legal entity | (run id) reversal flag | < 2 min | retry; blocks next valuation | reversal missing |
| `loan-interest-accrual` | period end | legal entity | (contract, period) | < 3 min for 5,000 loans | per-contract isolation; failures listed | missing fixings |
| `gl-interest-run` | period end | legal entity | (entity, period) | < 5 min for 10,000 accounts | rerun only after reversal | difference vs prior period > 50% |
| `payment-batch-window` | every `routing.batchWindowMin` | connection | batch id | < 60 s per 10,000 payments | batch held; payments stay Approved | batch past cutoff unsent |
| `fee-statement-poll` | monthly day 5 | bank connection | file hash | < 1 min | retry daily for 5 days | statement not received |
| `ihc-interest-and-statements` | month end 23:00 | IHC center | (account, period) | < 5 min for 500 accounts | per-account checkpoint | statement generation failure |
| `cash-desk-open-day-check` | daily 23:00 site local | cash desk | (desk, date) | < 30 s | alert only | days left open > 1 |

### 7.36.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| TreasuryBankAccountMaintain | POST/PUT /treasury/bank-accounts; Bank accounts screen | TreasuryBankAccountApprove; AccountsPayablePaymentPost (wave 19) | Treasury analyst |
| TreasuryBankAccountApprove | bank-account change workflow approve | TreasuryBankAccountMaintain | Treasurer |
| TreasuryStatementImport | POST /statements/import, /fee-statements/import | none | Treasury analyst, integration service account |
| TreasuryReconcile | /reconciliations/*/run-rules, /matches; Reconciliation workspace | AccountsPayablePaymentPost, AccountsReceivableCashPost on the same bank account | Treasury analyst |
| TreasuryReconcileApprove | /reconciliations/*/close, reopen | TreasuryReconcile (same reconciliation) | Treasurer, Controller |
| TreasuryCheckMaintain | /checks/*/void, reprint, stop | AccountsPayablePaymentApprove | AP payment clerk |
| TreasuryFacilityMaintain / TreasuryFacilityOverride | Facilities and instruments screen; override limit | override vs maintain | Treasury analyst / Treasurer |
| TreasuryInstrumentMaintain | /instruments (draw, register, remit, deposit) | TreasuryInstrumentSettle | AR clerk, AP clerk |
| TreasuryInstrumentSettle | /instruments/*/protest, settle | TreasuryInstrumentMaintain | Treasury analyst |
| TreasuryCashDeskPost | /cash-desks/*/documents, day close | TreasuryCashDeskApprove; AccountsReceivableCashPost on same desk | Cashier |
| TreasuryCashDeskApprove | reopen closed day, raise difference limit | TreasuryCashDeskPost | Site controller |
| TreasuryDealCapture | POST /deals | TreasuryDealConfirm (front office vs back office) | Treasury dealer |
| TreasuryDealConfirm | /deals/*/confirm, settlement release | TreasuryDealCapture | Treasury back office |
| TreasuryDealValuate | /valuation-runs | TreasuryDealCapture | Treasury accountant |
| TreasuryLoanMaintain | /loans | TreasuryPaymentRequestApprove on the same loan | Treasury analyst |
| TreasuryPaymentRequestCreate | /payment-requests, templates | TreasuryPaymentRequestApprove (BR-36.16) | Treasury analyst |
| TreasuryPaymentRequestApprove | /payment-requests/*/approve | TreasuryPaymentRequestCreate, TreasuryBankAccountMaintain | Treasurer |
| TreasuryRoutingConfigure | /routing-rules, bank chains, connections | TreasuryBankSend | Treasury system admin |
| TreasuryBankSend | /connections/*/send | TreasuryRoutingConfigure | Treasury back office, payment service account |
| TreasuryIhcOperate / TreasuryIhcOverride | /ihc/payment-orders; overdraft override | operate vs override | IHC operator / Treasurer |
| TreasuryInterestRun | /interest-runs | GeneralLedgerJournalApprove (wave 11) | Treasury accountant |
| TreasuryView | read-only GETs, cash position, forecast, all treasury inquiries | none | Treasurer, CFO, Controller |

SoD conflicts are enforced as OPA rules (wave 2) at role assignment (warning, requires mitigation record) and at execution for the same object (deny).

### 7.36.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| TRS-0401 | error | IBAN `{iban}` fails the mod-97 check | checksum invalid | correct the IBAN | FR-36.1 |
| TRS-0402 | info | Change to bank account `{code}` awaits approval | number / currency / GL change | approver acts in workflow | FR-36.1 |
| TRS-0405 | info | Statement already imported as `{statementId}` | identical file hash | none; existing statement returned | BR-36.8 |
| TRS-0406 | error | Statement `{id}` exists with a different closing balance | same statement id, different content | contact bank; import corrected file with new id | BR-36.8 |
| TRS-0407 | warning | Statement sequence gap: expected `{n}`, received `{m}` | missing statement | request missing statement | FR-36.2 |
| TRS-0408 | error | Statement currency `{c1}` differs from account currency `{c2}` | wrong account mapping | fix bank profile account mapping | BR-36.4 |
| TRS-0410 | error | Reconciliation cannot close: difference `{amount}` | book ≠ bank + outstanding | match or create remaining items | FR-36.5 |
| TRS-0411 | error | Line `{line}` was matched by `{user}` | concurrent match | refresh grid | BR-36.7 |
| TRS-0414 | error | Check `{no}` is cleared and cannot be voided | void after Cleared | post a reversal through AP instead | BR-36.5 |
| TRS-0415 | error | Facility `{facility}` limit exceeded by `{amount}` | utilization + amount > limit | request override or reduce amount | FR-36.7 |
| TRS-0416 | warning | Balance for `{account}` is `{hours}` h old | no statement within stale window | check connection / poll | FR-36.8 |
| TRS-0418 | warning | Bank fee `{service}` billed `{billed}` above agreed `{agreed}` | variance beyond tolerance | dispute with bank or update agreement | BR-36.18 |
| TRS-0419 | warning | Unexpected bank service code `{service}` | no agreement line | add service to agreement | BR-36.18 |
| TRS-0421 | error | Bill `{no}` is already remitted on `{remittanceId}` | second remittance | none | BR-36.10 |
| TRS-0422 | error | Protest reason code required | protest without reason | choose a reason code | BR-36.10 |
| TRS-0424 | error | Postdated check `{no}` cannot be deposited before `{checkDate}` | early deposit | wait or enable `pdc.allowEarlyDeposit` | BR-36.12 |
| TRS-0425 | info | Postdated check `{no}` bounced; open item restored | bank return | follow up with customer | FR-36.15 |
| TRS-0431 | error | Cash count difference `{diff}` exceeds limit `{limit}` | count vs computed closing | recount or request approver | BR-36.13 |
| TRS-0432 | error | Cash desk `{desk}` day `{date}` is closed | posting to closed day | post on the open day or reopen with approval | BR-36.13 |
| TRS-0433 | error | Cash desk balance would become negative | payment > balance | record a bank withdrawal first | FR-36.16 |
| TRS-0441 | error | Deal `{no}` confirmation differs: `{field}` | MT300 / manual confirmation mismatch | correct deal or contact counterparty | FR-36.18 |
| TRS-0442 | error | No market rate for `{pair}` tenor `{tenor}` on `{date}` | missing wave-12 rate / points | load rates and rerun | FR-36.18 |
| TRS-0445 | error | Missing fixing for index `{index}` on `{date}` | floating loan period without fixing | import fixing | FR-36.19 |
| TRS-0451 | error | You cannot approve a request you created or edited | self-approval | another approver acts | BR-36.16 |
| TRS-0452 | error | No routing rule matches payment batch `{batch}` | rule gap | add rule or default route | FR-36.23 |
| TRS-0453 | warning | Bank cutoff passed; execution date moved to `{date}` | request after cutoff | none or mark urgent channel | FR-36.21 |
| TRS-0461 | error | IHC account `{account}` overdraft limit exceeded | internal order > limit | override duty or reduce | BR-36.17 |
| TRS-0471 | error | EBICS signature failed: key `{keyId}` expired | key rotation due | rotate keys in Vault, re-send | FR-36.12 |

### 7.36.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Statement import | 30 files/day, 50,000 lines/day; peak 20 files/min at 06:00 | 8 s / 30 s / 60 s per 10k lines | 200,000 lines in < 10 min | 18M lines × 1.2 KB ≈ 22 GB | statement_line partitioned monthly by booking_date; GIN trigram on counterparty | queue by connection; imports serialize per bank account; p95 doubles, no rejections |
| Rule run | 50,000 lines/day | 5 s / 20 s / 40 s per 10k lines | 4 parallel accounts | n/a | partial index status = Unmatched | runs queue in Temporal; UI shows queued |
| Manual match API | 2,000/day; peak 60/min | 40 / 150 / 300 ms | 50 req/s | 1M matches ≈ 0.5 GB | match by (reconciliation_id) | p95 < 400 ms |
| Cash position query | 5,000/day; peak 100/min | 50 / 300 / 600 ms | 30 req/s | n/a (read model) | read model keyed (entity, currency) | serve cached snapshot with age |
| Forecast rebuild | 1M open items, hourly | n/a | 1M items < 5 min | 24 months × 12M rows ≈ 6 GB | monthly partitions on expected_date | rebuild cadence drops to every 3 h; snapshot age shown |
| Check print batch | 2,000 checks/run | n/a | 2,000 checks < 2 min | 50k PDFs ≈ 5 GB (object store) | check by (bank_account_id, number) unique | batch split by 500 |
| Cash journal posting | 500 desks × 40 docs/day | 60 / 300 / 600 ms | 20 docs/s | 7M docs ≈ 4 GB | by (desk_id, business_date) | posting queue; receipts print after commit |
| Instrument status changes | 5,000 bills/day | 80 / 300 / 600 ms | 5,000 per remittance in < 60 s | 2M instruments ≈ 2 GB | by (legal_entity_id, kind, status, maturity_date) | remittance batches queue |
| Deal valuation run | 20,000 open deals | n/a | < 5 min | 240k valuation rows ≈ 0.3 GB | deal by (entity, status); valuation partitioned by key date | run takes < 15 min; close checklist task stays InProgress |
| Loan accrual | 5,000 contracts | n/a | < 3 min | 60k schedule lines ≈ 0.1 GB | schedule by (loan_id, period_no) | runtime scales linearly |
| Payment routing | 100,000 payments/day; peak 10,000 per window | 20 ms per payment | 10,000 per batch < 60 s | 36M routing log rows ≈ 10 GB | log partitioned monthly | batches fill next window; cutoff warnings TRS-0453 |
| GL interest run | 10,000 accounts × 31 days | n/a | < 5 min | small | balance projection by (account, value_date) | < 15 min |

### 7.36.20 Localization and compliance hooks

- **Bills of exchange and promissory notes** vary by country: FR LCR / BOR (CFONB 160 remittance), IT RiBa (CBI), ES letra de cambio / pagaré (AEB 19 / 58, Norma 32), PT letras, JP tegata and densai electronic records, BR duplicata / boleto (CNAB 240 / 400). Country packs (wave 48) provide the `InstrumentRemittanceFormat` implementation, the document layout, stamp-duty lines (IT, JP stamp tax by amount band) and protest deadlines; the core supplies the lifecycle and postings.
- **Postdated checks** are common in IN, AE, SA, PH, MY, TH; the IN pack sets withholding at settlement, the AE pack records bounce reason codes required for legal action. Extension point `PdcPolicy` (deposit window, bounce fee, credit hold).
- **Cash journal**: DE GoBD / §146 AO require daily recording, immutability after post, and a cash book export (DSFinV-K is out of scope because desks are not retail POS); PL requires KP / KW cash receipt documents; FR and IT require a sequential cash book. Extension point `CashJournalPolicy` (numbering, mandatory fields, max cash payment amounts such as IT EUR 5,000 / FR EUR 1,000 cash payment limits, warning TRS-0433 extended by pack rules).
- **Bank statement and payment formats**: BAI2 (US), CODA (BE), Norma 43 (ES), CFONB 120 (FR), MT940 / camt.053 (DE, NL), CAMT for SEPA; positive pay (US), CPA-005 (CA); bank chain usage for non-SEPA cross-border payments. Extension point `BankFormatProfile` in the wave-19 format engine.
- **FX deal regulatory reporting**: EU EMIR (FX forwards and swaps are reportable; spot is not), UK EMIR, US Dodd-Frank for swap counterparties; Passage records the LEI on every counterparty and the UTI on deals and exports the trade report through the wave-48 `RegulatoryTradeReport` hook; delegated reporting by the bank is the default.
- **Intercompany loans and GL interest**: thin-capitalization and interest-limitation data (DE §4h EStG, US §163(j), EU ATAD art. 4) and transfer-pricing safe-harbor rates feed the wave-40 transfer-pricing evidence export; withholding tax on cross-border interest uses wave-13 tax codes.
- **Retention**: statements, checks, cash books and deal records 10 years (DE HGB §257, FR), 7 years (US), 5 years after termination for EMIR records; enforced through the wave-5 retention policies.
- **Calendars and rounding**: banking-day calendars per country and currency (TARGET2 for EUR); day-count conventions per currency market (GBP ACT/365, EUR / USD money market ACT/360).

### 7.36.21 Coverage closure

Register rows assigned to wave 36 in coverage-ax.md §12.99 (16 rows) and coverage-sap.md §13.99 (39 rows), plus two SAP rows proposed for wave 30 that this wave closes (GL balance interest).

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.2.1 Payments → Promissory note journals (Draw / Redraw / Remittance / Settle) | GAP | ADDED as FR-36.14 | lifecycle BR-36.11 |
| AX 12.2.2 SSRS Promissory note (vendor) documents | GAP | ADDED as FR-36.14 | document in 7.36.15 |
| AX 12.2.3 Promissory notes → Draw / Redraw / Remit / Settle periodic batches | GAP | ADDED as FR-36.14 | `instrument-maturity` job 7.36.16 |
| AX 12.2.4 Payment → Promissory note setup (journal names, posting profiles, protest) | GAP | ADDED as FR-36.14 | `instrument.notePostingProfile` 7.36.13 |
| AX 12.3.1 Payments → Bill of exchange journals (Draw / Redraw / Protest / Remittance / Settle) | GAP | ADDED as FR-36.13 | BR-36.10 |
| AX 12.3.1 Payments → Postdated checks (R3 APAC) | GAP | ADDED as FR-36.15 | customer and vendor |
| AX 12.3.2 Bill of exchange inquiry | GAP | ADDED as FR-36.13 | 7.36.14 Bill of exchange list |
| AX 12.3.2 Postdated check inquiry | GAP | ADDED as FR-36.15 | 7.36.14 |
| AX 12.3.2 SSRS Bill of exchange documents / Remittance list | GAP | ADDED as FR-36.13 | 7.36.15 |
| AX 12.3.3 Payments → Bill of exchange remittance / protest batches | GAP | ADDED as FR-36.13 | `instrument-maturity` job |
| AX 12.3.4 Payment → Bill of exchange setup (journal names, posting profiles, protest reasons, remittance types) | GAP | ADDED as FR-36.13 | `instrument.billPostingProfile`, `instrument.remittanceTypes` |
| AX 12.3.4 Payment → Postdated checks setup (R3 APAC) | GAP | ADDED as FR-36.15 | `pdc.*` parameters |
| AX 12.5.1 Bank → Bills of exchange / Promissory notes (links) | GAP | ADDED as FR-36.13 / FR-36.14 | bank-side remittance and honor via FR-36.3 `billNumber` criterion |
| AX 12.5.1 Bank → Postdated checks (links) | GAP | ADDED as FR-36.15 | deposit proposals per bank account |
| AX 12.5.4 Bills of exchange / promissory notes setup (bank side) | GAP | ADDED as FR-36.13 / FR-36.14 | remittance types per bank account |
| AX 12.5.4 Postdated checks parameters (R3 APAC) | GAP | ADDED as FR-36.15 | 7.36.13 |
| SAP 13.1 FBCJ Cash journal | GAP | ADDED as FR-36.16 | |
| SAP 13.1 FBCJC0 / FBCJC1 / FBCJC2 / FBCJC3 cash journal setup, number ranges, business transactions, print | GAP | ADDED as FR-36.16 | `cashDesk.*` parameters, receipt and cash book documents |
| SAP 13.1 S_ALR_87012309 Cash book | GAP | ADDED as FR-36.16 | 7.36.14 Cash book |
| SAP 13.1 F.50 / F.52 G/L interest calculation | GAP (proposed 30) | ADDED as FR-36.25 | treasury owns IC current-account interest; wave 30 interest notes unchanged |
| SAP 13.1 OB46 / OB82 / OBAA interest indicators | GAP (proposed 30) | ADDED as FR-36.25 | `interest.indicators` |
| SAP 13.2 F-40 / F-36 (FBW6) Bill of exchange payment (AP) | GAP | ADDED as FR-36.14 | AP bills modeled as promissory notes payable |
| SAP 13.2 FIBLFFP / F111 Payment request | PARTIAL-36 | ADDED as FR-36.21 | |
| SAP 13.2 S_ALR_87012116 Bill of exchange list (AP) | GAP | ADDED as FR-36.14 | 7.36.14 |
| SAP 13.2 F111 Payment program for payment requests | PARTIAL-36 | ADDED as FR-36.21 | executed by wave-19 payment engine, routed by FR-36.23 |
| SAP 13.2 FBWE AP bill of exchange presentation | GAP | ADDED as FR-36.14 | remit / settle |
| SAP 13.2 Bank-to-bank transfers (payment request types, FIBL) | PARTIAL-36 | ADDED as FR-36.21 | category `TreasuryTransfer` |
| SAP 13.3 F-34 / F-36 / F-33 Bill of exchange payment / discount (AR) | GAP | ADDED as FR-36.13 | collection and discount |
| SAP 13.3 S_ALR_87012199 Customer bill of exchange list | GAP | ADDED as FR-36.13 | |
| SAP 13.3 FBWD / FBWE AR bill presentation | GAP | ADDED as FR-36.13 | |
| SAP 13.5 FBCJ Cash journal | GAP | ADDED as FR-36.16 | duplicate of FI-GL row |
| SAP 13.5 FRFT / FRFT_B Repetitive payment codes / bank-to-bank transfer | PARTIAL-36 | ADDED as FR-36.21 | repetitive templates |
| SAP 13.5 IHC0 / IHC1IP In-House Cash | PARTIAL-36 | ADDED as FR-36.24 | external payments via IHC accounts out of v1 |
| SAP 13.5 Advanced Payment Management (Approve / Monitor Payments) | PARTIAL-36 | ADDED as FR-36.23 | central monitor, routing, batching |
| SAP 13.5 FBWE / FBWD (bills) presentation | GAP | ADDED as FR-36.13 | |
| SAP 13.5 S_ALR_87012309 Cash journal report | GAP | ADDED as FR-36.16 | |
| SAP 13.5 Bank Fee Analysis (camt.086) | GAP | ADDED as FR-36.17 | |
| SAP 13.5 Cash pooling / sweeping run (IHC) | PARTIAL-36 | ADDED as FR-36.24 | FR-36.11 sweeps + IHC accounts and interest |
| SAP 13.5 F111 (treasury payment requests) periodic | PARTIAL-36 | ADDED as FR-36.21 | |
| SAP 13.5 Bank fee statement import (camt.086) | GAP | ADDED as FR-36.17 | `fee-statement-poll` job |
| SAP 13.5 Bank chains (FIBC) | PARTIAL-36 | ADDED as FR-36.22 | |
| SAP 13.5 IHC customizing (centers, accounts, conditions, payment orders) | PARTIAL-36 | ADDED as FR-36.24 | `ihc.conditions` |
| SAP 13.5 Cash journal customizing (FBCJC0..3) | GAP | ADDED as FR-36.16 | |
| SAP 13.6 FTR_CREATE / FTR_EDIT / FTR_00 Treasury transaction manager | GAP | ADDED as FR-36.18 | FX spot / forward / swap, fixed-term deposits; securities, money-market paper and options OUT-OF-SCOPE for v1 (no customer demand in the manufacturing segment; v2 backlog) |
| SAP 13.6 TBB1 / TPM10 Post flows | GAP | ADDED as FR-36.18 | flows post on value date |
| SAP 13.6 TPM1 / TPM60 Valuation, amortization, position management | GAP | ADDED as FR-36.18 | key-date MTM; amortization of deposits by accrual |
| SAP 13.6 THMEX Hedge management and accounting (IFRS 9) | GAP | OUT-OF-SCOPE hedge accounting (IFRS 9 / ASC 815 designation, OCI cash-flow hedge reserve) requires effectiveness documentation and auditor-reviewed models; v1 values FX deals through P&L | revisit at v2 planning |
| SAP 13.6 Exposure management (TEM) | GAP | ADDED as FR-36.20 | |
| SAP 13.6 Loans management (FN*, CML) | GAP | ADDED as FR-36.19 | bank and IC loans |
| SAP 13.6 TPM12 / TPM13 Position / flow list | GAP | ADDED as FR-36.18 | 7.36.14 |
| SAP 13.6 TPM20 Posting journal | GAP | ADDED as FR-36.18 | 7.36.14 |
| SAP 13.6 Hedge plan reports | GAP | OUT-OF-SCOPE hedge accounting deferred to v2 | FX exposure hedge ratio in FR-36.20 covers the planning view |
| SAP 13.6 Treasury information system | GAP | ADDED as FR-36.18 / FR-36.20 | position, flow, exposure inquiries |
| SAP 13.6 TBB1 periodic / TPM1 valuation run / accruals | GAP | ADDED as FR-36.18 / FR-36.19 | `deal-valuation`, `loan-interest-accrual` jobs |
| SAP 13.6 Hedge effectiveness test run | GAP | OUT-OF-SCOPE hedge accounting deferred to v2 | |
| SAP 13.6 TRM customizing: product, transaction, flow, update types, valuation areas, accounting codes | GAP | ADDED as FR-36.18 | `deal.accountingCodes`, `deal.dayCountDefault` |
| SAP 13.6 Hedge management customizing (THM*) | GAP | OUT-OF-SCOPE hedge accounting deferred to v2 | |

## 7.37 Wave 37: Fixed Assets & Leases

### 7.37.1 Scope and business context

Wave 37 adds the asset subledger. Capital spend currently lands on expense or clearing accounts; after this wave an accountant acquires assets from AP invoices, projects (CIP) or inventory, depreciates them monthly under several books (local GAAP, IFRS, tax) with different methods and conventions, transfers, splits, revalues, impairs and disposes of them with correct gain/loss, and accounts for leases under IFRS 16 / ASC 842 with right-of-use assets and lease liabilities. A physical inventory with barcode scanning reconciles the register to what exists.

Out of scope: enterprise asset maintenance (wave 44), tax depreciation reports beyond MACRS/ADS tables for the US and straight-line for other v1 countries (country packs in wave 48), component accounting for infrastructure-grade asset hierarchies deeper than 3 levels.

**Scope amendment (gap closure).** The registers (coverage-ax §12.99 rows 40–43, coverage-sap §13.99 rows 51–55 and 315–321) add asset additions / component records (FR-37.14), insurance records with insured-value indexing (FR-37.15), group assets for pooled tax depreciation (FR-37.16) and capital appropriation requests (FR-37.17) to this wave. Investment program hierarchies with budget distribution and carry-forward land in wave 38 (FR-38.21) because they need budget control; the IRS Form 4562 layout ships with the US country pack (wave 48).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX depreciation proposal → LedgerJournalTrans → post: three forms, and one bad asset aborts the whole journal with an infolog | Journal-centric batch without per-asset isolation | Temporal run with per-asset checkpoints; failing assets reported, others post | Failed runs per month 2 → 0; run restart time 1 h → < 1 min |
| SAP AFAB runs in background only, errors in SM37 spool; test run then real run, 2–4 h for 100k assets | Batch job architecture | Online proposal preview with formula inputs; 1M asset-periods/h | Run time for 100k assets 2 h → < 6 min |
| AssetBook convention changes in D365 silently recalculate history; auditors cannot see why a prior charge changed | No immutable transaction log per book | Append-only AssetTransaction; prospective changes only, with explanation | Unexplained changes in audit sample 100% traceable |
| Mid-quarter test performed in Excel because neither AX nor SAP computes the 40% Q4 test | Tax rules outside the engine | Automatic test per tax book per year with delta posting | Manual tax workbooks: 1 per entity per year → 0 |
| Acquisition from AP needs the invoice line fixed-asset fields, then an asset journal; SAP needs F-90 with asset account assignment keyed again | Two-step with re-keying | AP invoice line with asset flag posts directly to the book | Clicks 18 → 3; re-keyed acquisitions 0 |

### 7.37.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Asset master and groups | AssetTable, AssetGroup, AssetGroupBookSetup, form AssetTable | ANLA, ANLZ (time-dependent), asset classes ANKA/ANKT (OAOA), AS01/AS02/AS03 | Asset, AssetClass entities | full |
| Books / depreciation areas | AssetBook (D365 books; AX 2012 value models + AssetDepBook), posting layer per book | ANLB per depreciation area (OADB), T093 chart of depreciation, ledger assignment | AssetBook per asset × book definition, posts to ledger layers (wave 12) | full |
| Depreciation profiles | AssetDepreciationProfile (StraightLine, ReducingBalance, Manual, Factor, Consumption, StraightLineLifeRemaining, 200%/175%/150%/125% DB, RB/SL), conventions | Depreciation keys T090NA/T090NAZ (AFAMA), multilevel methods T090NP, period controls T090NR | DepreciationProfile with method, convention, factor, tables | full |
| Depreciation run | AssetDepreciationProposal → LedgerJournalTrans (Asset journal), AssetTrans | AFAB / FAA_DEPRECIATION_POST, ANLP, ANLC | Depreciation run (Temporal) → journal entry per book/period | improved (restartable, per-asset diagnostics) |
| Acquisition from AP / project / inventory | PurchLine fixed asset fields, ProjTable capitalize, InventJournal to fixed asset | F-90, ABZON, MIGO with asset account assignment (A), AIAB/AIBU AuC settlement | AcquisitionSource links from wave 18 invoice, wave 41 project, wave 16 inventory journal | full |
| Transfer / split | AssetTransfer, "Split fixed asset" (D365) | ABUMN (intra/inter-company), partial transfer | Transfer and Split transactions preserving history | full |
| Disposal, revaluation, impairment | AssetTrans types DisposalSale, DisposalScrap, WriteUpAdj, WriteDownAdj, Revaluation | ABAON, ABAVN, F-92, ABAW revaluation, impairment via write-down | Disposal (sale/scrap, partial), Revaluation, Impairment | full |
| Leases | D365 Asset leasing (AssetLeaseLease, AssetLeaseBook, AssetLeasePaymentSchedule, AssetLeaseLiabilityAmortization) | RE-FX Contract and Lease Management (RECN, RECEEP valuation), IFRS 16 valuation rules | Lease, LeaseSchedule, ROU asset auto-created | full |
| Physical inventory | Not native (AX 2012 lacked); D365 none; partner add-ons | ANLA-INVNR only; third-party scanning | Count sessions with PWA barcode scanning | improved |
| Asset additions / components | AssetAddition (Fixed asset → Additions), AssetMajorType, AssetTable.MajorType | Sub-numbers (AS11 asset sub-number), ANLA-ANLN2 | AssetAddition records under an asset (FR-37.14) | full |
| Insurance | AssetInsurance, AssetInsuranceType, insurance companies on AssetTable (Insurance tab), SSRS Fixed asset insurance report | ANLA insurance fields, OAVI insurance types, OAVF / T094 index series, S_ALR_87011951 | AssetInsurancePolicy with index series (FR-37.15) | full |
| Group assets | none (AX uses consolidated books per group manually) | AS21 / AS22 / AS23 group asset, OAYM / OAAX group asset areas (T093A) | GroupAsset per tax book (FR-37.16) | full |
| Appropriation requests | none native (Budget planning + project quotations) | IMA11 / IMA2 / IMA3 appropriation requests, variants, approval status, IMCCP1 | CapexRequest with variants and Temporal approval (FR-37.17) | full |

### 7.37.3 Functional requirements

- **FR-37.1 Asset master** — Must. The system shall maintain assets with class, location, responsible worker, serial/barcode, parent/child (≤ 3 levels), and default dimensions.
  - Notes: asset number from wave-5 number sequences; status NotYetAcquired → Open → Sold / Scrapped / Transferred; barcode unique per legal entity.
- **FR-37.2 Books and profiles** — Must. The system shall attach N books per asset (default from class), each with a depreciation profile, service life, salvage value, convention, posting layer and ledger.
  - Notes: methods: straight-line (service life and life-remaining variants), declining balance (factor 125/150/175/200%, with or without switch to SL), units of production, manual, MACRS tables (GDS 3/5/7/10/15/20-year, ADS); conventions: none, half-year, mid-month (1st or 15th), mid-quarter, full-month, half-year next year; up to 10 books per asset.
- **FR-37.3 Straight-line formula** — Must. The system shall compute period depreciation as `(cost − salvage − accumulated adjustments) / remaining periods` with the convention applied to the first and last periods.
  - Notes: worked mid-month example: cost USD 12,000, salvage 0, 5-year life, in service 2026-03-10, mid-month (15th) convention. Monthly charge = 12,000 / 60 = 200.00. March 2026 takes half a month: 100.00. FY2026 = 100 + 9 × 200 = 1,900.00; FY2027–FY2030 = 2,400.00 each (9,600.00); FY2031 = Jan 200 + Feb 200 + Mar 100 = 500.00. Total 12,000.00. The last period absorbs any rounding residual so Σ depreciation = depreciable basis exactly.
- **FR-37.4 MACRS** — Must. The system shall compute MACRS using the declining-balance-with-switch formula and verify it against the IRS Publication 946 percentage tables.
  - Notes: worked 5-year GDS, half-year convention, cost USD 25,000: rate = 200%/5 = 40%. Y1 = 25,000 × 40% × ½ = 5,000.00 (20.00%). Y2 = 20,000 × 40% = 8,000.00 (32.00%). Y3 = 12,000 × 40% = 4,800.00 (19.20%). Y4: DB = 7,200 × 40% = 2,880.00; SL over remaining 2.5 years = 7,200 / 2.5 = 2,880.00 → switch (11.52%). Y5 = 2,880.00 (11.52%). Y6 = 1,440.00 (5.76%). Σ = 25,000.00. Mid-quarter applies when > 40% of the year's basis is placed in Q4; the system computes the test per tax book per year and alerts. Bonus depreciation percentage and Section 179 amounts recorded as first-year adjustments.
- **FR-37.5 Depreciation run** — Must. The system shall propose and post depreciation per book, period and legal entity as one journal entry per book with lines per asset (or summarized per account/dimension by option).
  - Notes: Temporal workflow with per-asset checkpoints; rerun for the same book/period is rejected unless the prior run is reversed; 1,000,000 asset-book-periods per hour; proposal preview shows per-asset amount and formula inputs.
- **FR-37.6 Acquisition sources** — Must. The system shall create acquisitions from AP invoice lines flagged as asset, project CIP capitalization, inventory journal issue to asset, and manual journal.
  - Notes: AP invoice with asset flag posts Dr Asset cost / Cr AP (instead of expense); CIP collects to the asset's under-construction book value and capitalizes on a date with transfer from CIP account; inventory issue uses the item's current cost (wave 20).
- **FR-37.7 Transfer and split** — Must. The system shall transfer assets between dimensions, locations or legal entities and split by percentage or amount into new assets with proportional cost and accumulated depreciation.
  - Notes: intercompany transfer posts disposal in the source and acquisition in the target at NBV or agreed price (wave 40 IC due-to/due-from); split percentages must sum to 100.00%.
- **FR-37.8 Disposal** — Must. The system shall dispose assets by sale (to a customer invoice, wave 29, or free text) or scrap, including partial disposal, posting cost reversal, accumulated depreciation reversal and gain/loss.
  - Notes: gain/loss = proceeds − NBV of the disposed portion; partial disposal uses the disposed share of cost and accumulated depreciation; depreciation up to the disposal date is posted first when `depreciate-on-disposal` is set per book.
- **FR-37.9 Revaluation and impairment** — Must. The system shall post revaluation (IAS 16 revaluation model) to OCI reserve or P&L per policy and impairment (IAS 36) as write-down with reversal limits.
  - Notes: impairment reversal cannot exceed the carrying amount that would have existed without impairment.
- **FR-37.10 Leases (IFRS 16 / ASC 842)** — Must. The system shall record leases with payment schedules, incremental borrowing rate, options, and generate the liability amortization and ROU depreciation schedules, posting initial recognition, monthly interest, payments and depreciation.
  - Notes: worked example, annual payments in arrears USD 10,000 for 5 years, IBR 5%: liability = 10,000 × (1 − 1.05⁻⁵)/0.05 = 43,294.77. Schedule: Y1 interest 2,164.74, closing 35,459.51; Y2 1,772.98 → 27,232.49; Y3 1,361.62 → 18,594.11; Y4 929.71 → 9,523.82; Y5 476.18 (plugged from 476.19 to clear the 0.01 residual) → 0.00. Total interest 6,705.23 = 50,000 − 43,294.77. ROU asset 43,294.77 depreciated straight-line 8,658.95 for Y1–Y4 and 8,658.97 in Y5. Monthly schedules use periodic rate = nominal/12 by default (option: effective (1+r)^(1/12) − 1). Modifications remeasure the liability with the ROU adjusted by the same amount; short-term (≤ 12 months) and low-value (< USD 5,000 / EUR 5,000) leases are expensed under election.
- **FR-37.11 Physical inventory** — Should. The system shall run count sessions with the PWA scanner, record found/missing/moved assets and create transfer or scrap proposals.
- **FR-37.12 Asset budgeting** — Should. The system shall forecast depreciation for existing assets and planned acquisitions and publish it as a budget source to wave 38.
- **FR-37.13 Subledger reconciliation** — Must. The system shall reconcile Σ asset cost and Σ accumulated depreciation per book to the GL accounts nightly and list differences by asset.
- **FR-37.14 Asset additions and component records** — Should. The system shall record additions and components under an asset (improvement, major part, accessory) with description, quantity, serial number, date, cost reference and optional link to an acquisition adjustment, without creating a separate depreciable asset unless the user promotes the record to a sub-asset.
  - Notes: an addition with `financial = true` creates an AcquisitionAdj transaction on the selected books in the same request; non-financial additions are descriptive only; promote-to-sub-asset creates a child asset (≤ 3 levels, FR-37.1) and moves the referenced cost by split (FR-37.7); maximum 500 additions per asset.
- **FR-37.15 Insurance records and insured-value indexing** — Should. The system shall record insurance policies per asset (insurer, policy number, insurance type, coverage start / end, premium, insured value basis) and compute the indexed insured value from replacement value and an index series.
  - Notes: insurance types (property, machinery breakdown, transit, fleet) and insurance companies are master data; insured value_t = base replacement value × index_t ÷ index_base; index series per asset class and country, yearly values loaded by the `insurance-index-update` job; policies expiring within 30 days raise alerts; the insurance report (7.37.14) lists insured vs NBV vs replacement value and under-insured assets (insured < 90% of indexed replacement value).
- **FR-37.16 Group assets** — Should. The system shall group assets of the same class and tax year into a group asset for a designated tax book and compute depreciation on the group's aggregate basis, while other books keep per-asset depreciation.
  - Notes: covers US general asset accounts (IRC §168(i)(4)), DE Sammelposten (EUR 250.01–1,000.00 pool depreciated over 5 years), UK capital allowance pools and IN block of assets via profiles from the wave-48 packs; member assets carry the book with `depreciatedAtGroup = true` and no own charge; disposal of a member under the default rule credits proceeds to the group (no per-asset gain / loss) and under `terminate-on-disposal` computes gain / loss per member; the group's depreciation run line references every member for drill.
- **FR-37.17 Capital appropriation requests** — Should. The system shall let requesters create capital expenditure requests with one or more variants (scope, amount per year, expected in-service date, depreciation profile assumption), route them through a value-based approval workflow, and on approval create planned acquisitions and, when the request is executed, the asset under construction or project.
  - Notes: status Draft → Submitted → InApproval → Approved → Released → Closed, with Rejected and Postponed; approval levels by amount (defaults ≤ USD 50,000 plant manager, ≤ 500,000 CFO, above board) through the wave-4 workflow; the approved variant's yearly amounts publish as planned acquisitions to FR-37.12 depreciation forecasting and as a budget source to wave 38; assignment to an investment program position (FR-38.21) is optional; actual spend from linked CIP / project (wave 41) is tracked against the approved amount with an overrun warning at 10%.

### 7.37.4 Data model

**Asset**: id, legal_entity_id, asset_number (unique per entity), class_id (FK AssetClass), name, status, location_id, responsible_worker_id, barcode, parent_asset_id, default_dimensions jsonb, acquired_date, quantity numeric(19,4).

**AssetBook**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| asset_id | uuid | yes | FK Asset |
| book_definition_id | uuid | yes | FK BookDefinition (ledger, posting layer, currency) |
| profile_id | uuid | yes | FK DepreciationProfile |
| service_life_periods | int | yes | 1..1200 |
| salvage_value | numeric(19,4) | yes | default 0 |
| convention | enum | yes | None/HalfYear/MidMonth1/MidMonth15/MidQuarter/FullMonth/HalfYearNextYear |
| in_service_date | date | yes | |
| cost, accumulated_depreciation, nbv | numeric(19,4) | yes | projections from AssetTransaction |
| last_depreciated_period | text | no | fiscal period key |
| status | enum | yes | NotYetAcquired/Open/Closed/Sold/Scrapped |

**AssetTransaction**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK, append-only |
| asset_book_id | uuid | yes | FK |
| type | enum | yes | Acquisition/AcquisitionAdj/Depreciation/DepreciationAdj/Transfer/Split/Revaluation/Impairment/DisposalSale/DisposalScrap/CIPCapitalization |
| transaction_date | date | yes | |
| amount | numeric(19,4) | yes | signed |
| journal_entry_id | uuid | yes | FK wave-11 journal |
| source_type, source_id | text, uuid | no | AP invoice, project, inventory journal, lease |
| reversal_of_id | uuid | no | |
| run_id | uuid | no | depreciation run |

**Lease**: id, legal_entity_id, lease_number, lessor_party_id, commencement_date, term_months, payment_frequency, payment_amount, payment_timing (arrears/advance), ibr_rate numeric(9,6), classification (Finance/Operating for ASC 842), rou_asset_id, status (Draft/Active/Modified/Terminated), election flags.

**LeaseScheduleLine**: lease_id, period_no, opening_liability, interest, payment, closing_liability, rou_depreciation, posted_journal_id.

**Entities added by FR-37.14–FR-37.17**: **AssetAddition** (id, asset_id, type Improvement/Component/Accessory, description, quantity, serial_number, added_date, financial bool, asset_transaction_id, promoted_asset_id); **AssetInsurancePolicy** (id, asset_id, insurer_party_id, policy_number, insurance_type_id, coverage_from, coverage_to, premium, basis Replacement/NBV/Agreed, base_value, index_series_id, index_base_year); **IndexSeries / IndexValue** (series code, country, class, year, value numeric(9,4)); **GroupAsset** (id, legal_entity_id, book_definition_id, class_id, tax_year, profile_id, basis, accumulated_depreciation, member_count, status); **CapexRequest** (id, legal_entity_id, number, title, requester_id, status, selected_variant_id, program_position_id, approved_amount, currency, linked_asset_ids[], linked_project_id) and **CapexRequestVariant** (id, request_id, description, amounts_by_year jsonb, in_service_date, profile_assumption_id, npv, irr).

Invariants: Σ AssetTransaction amounts by type = projected cost / accumulated depreciation; NBV ≥ 0 unless revaluation policy allows; Σ depreciation over life = cost − salvage; LeaseScheduleLine closing of period n = opening of n+1 and final closing = 0.00. Indexes: AssetTransaction by (asset_book_id, transaction_date), by run_id; AssetBook by (book_definition_id, status).

### 7.37.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/assets | Create asset | Idempotency-Key | AssetMaintain |
| POST | /api/v1/assets/{id}/books | Add book | Idempotency-Key | AssetMaintain |
| POST | /api/v1/assets/depreciation-runs | Propose run (book, period) | (book, period) unique | AssetDepreciate |
| POST | /api/v1/assets/depreciation-runs/{id}/post | Post proposal | state check | AssetPostJournal |
| POST | /api/v1/assets/{id}/transactions | Acquisition/adjustment/revaluation/impairment | Idempotency-Key | AssetPostJournal |
| POST | /api/v1/assets/{id}/dispose | Sale/scrap, partial % | Idempotency-Key | AssetDispose |
| POST | /api/v1/assets/{id}/transfer | Transfer / split | Idempotency-Key | AssetMaintain |
| GET | /api/v1/assets/{id}/schedule?book= | Projected depreciation | n/a | AssetView |
| POST | /api/v1/leases | Create lease and compute schedule | Idempotency-Key | LeaseMaintain |
| POST | /api/v1/leases/{id}/modify | Remeasure | Idempotency-Key | LeaseMaintain |
| POST | /api/v1/assets/count-sessions | Physical inventory | Idempotency-Key | AssetCount |
| POST | /api/v1/assets/{id}/additions | Record addition / component | Idempotency-Key | AssetMaintain |
| POST | /api/v1/assets/{id}/insurance-policies | Add insurance policy | Idempotency-Key | AssetInsuranceMaintain |
| POST | /api/v1/assets/group-assets | Create group asset and assign members | Idempotency-Key | AssetConfigure |
| POST | /api/v1/assets/capex-requests | Create appropriation request with variants | Idempotency-Key | CapexRequestCreate |
| POST | /api/v1/assets/capex-requests/{id}/submit | Start approval workflow | state check | CapexRequestCreate |

Validation: book must be Open and period open per ledger × module (wave 14); disposal percentage in (0,100]; lease IBR in (0, 50%].

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.assets.asset.acquired.v1` | acquisition posted | assetId, bookIds[], cost, sourceType, sourceId | budget-svc, analytics |
| `dev.passage.assets.depreciation.posted.v1` | run posted | runId, bookDefinitionId, period, assetCount, totalAmount, journalEntryId | costacct-svc, analytics |
| `dev.passage.assets.asset.disposed.v1` | disposal posted | assetId, bookId, percentage, proceeds, nbv, gainLoss | tax, analytics |
| `dev.passage.assets.lease.recognized.v1` | lease activated | leaseId, liability, rouAsset, termMonths, rate | analytics |
| `dev.passage.assets.capexrequest.approved.v1` | request approved | requestId, variantId, amountsByYear, programPositionId | budget-svc (38), asset forecasting |
| `dev.passage.assets.insurance.expiring.v1` | policy within 30 days of expiry | policyId, assetId, coverageTo, insuredValue | workspace, notifications |

### 7.37.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Fixed assets workspace | New asset, run depreciation, exceptions, reconciliation tile | assets by status, pending acquisitions, unreconciled difference | AssetView |
| Asset detail | Books tab, transactions tab, schedule, dispose, transfer | number, class, NBV per book, location | AssetMaintain |
| Depreciation run | Propose, review, post, reverse | book, period, asset count, total, exceptions | AssetDepreciate |
| Lease detail | Schedule, modify, terminate | liability, ROU, interest, next payment | LeaseMaintain |
| Count session (PWA) | Scan, mark found/missing, submit | barcode, location, found at | AssetCount |

Interaction rules: schedule preview recalculates on any book field change before save; bulk class change via grid multi-select with validation at submit; depreciation run proposals paginate 500 assets per page with per-asset formula tooltip.

### 7.37.7 Business rules and edge cases

- **BR-37.1** Period depreciation rounds half-up to currency minor unit; the final period of the life is a plug so Σ = depreciable basis.
- **BR-37.2** Asset state machine: NotYetAcquired → Open (first acquisition) → Sold | Scrapped | Transferred (full IC transfer); Open → Open on partial disposal; no transaction on Sold/Scrapped except reversal of the disposal.
- **BR-37.3** Depreciation for a period cannot post if a prior period for the same book is unposted (sequential), except when the book's `catch-up` option is set, which posts the arrears into the current period.
- **BR-37.4** Change of service life or salvage after acquisition applies prospectively: new charge = (NBV − salvage) / remaining periods.
- **BR-37.5** Mid-quarter test: if Q4 in-service basis / full-year basis > 40% for a tax book, all assets of that year switch to mid-quarter; assets already depreciated are recomputed and the delta posts in the current period.
- **BR-37.6** Disposal date before last depreciated period requires reversal of later depreciation first; the system offers the reversal set.
- **BR-37.7** Partial disposal percentage p: disposed cost = cost × p, disposed accumulated depreciation = acc × p, rounded; residual cents stay on the remaining asset.
- **BR-37.8** Lease liability remeasurement on modification: new PV at revised rate (if scope change) or original rate (if only payment change under ASC 842 for operating leases); ROU adjusts by the same amount; if ROU would go below zero the excess hits P&L.
- **BR-37.9** Concurrency: the depreciation run holds a per-(book, period) Postgres advisory lock; a second run request returns 409.
- **BR-37.10** Indexed insured value rounds half-up to the currency minor unit; a missing index value for the current year uses the latest available year and raises AST-0531.
- **BR-37.11** A member asset can belong to one group asset per book; adding a member after the group's first depreciation period posts a catch-up in the current period on the group; removing a member is allowed only through disposal or transfer.
- **BR-37.12** Capex request state machine: Draft → Submitted → InApproval → Approved → Released → Closed; InApproval → Rejected | Postponed → Draft; an amount change after Approved returns the request to Submitted when the increase exceeds 10% or USD 10,000 / EUR 10,000, whichever is lower.
- **BR-37.13** Promoting a financial addition to a sub-asset moves cost and accumulated depreciation of the referenced transaction pro rata; the parent's remaining life is unchanged.

### 7.37.8 Acceptance criteria

- **AC-37.1** (FR-37.3) Given the USD 12,000 mid-month example, when the schedule is generated, then FY2026 = 1,900.00, FY2027–2030 = 2,400.00 each, FY2031 = 500.00.
- **AC-37.2** (FR-37.4) Given the USD 25,000 MACRS 5-year example, when computed, then the yearly amounts are 5,000.00 / 8,000.00 / 4,800.00 / 2,880.00 / 2,880.00 / 1,440.00.
- **AC-37.3** (FR-37.4) Given 100 fixture assets across methods and conventions, when depreciated for full lives, then every amount matches the reference table to the cent.
- **AC-37.4** (FR-37.5) Given 10,000 assets, when the monthly run posts, then one journal entry per book exists, Σ lines = Σ proposal, and a second run for the same period is rejected.
- **AC-37.5** (FR-37.8) Given cost 10,000, accumulated 4,000, proceeds 7,000, 50% disposal, when disposed, then cost −5,000, accumulated +2,000, gain 4,000 posts and the remaining NBV is 3,000.
- **AC-37.6** (FR-37.10) Given the 5-year USD 10,000 lease at 5%, when activated, then liability = 43,294.77, Y5 interest = 476.18 and closing = 0.00.
- **AC-37.7** (FR-37.10) Given 25 fixture leases, when schedules compute, then every line matches the accountant workbook to the cent.
- **AC-37.8** (FR-37.13) Given the demo entity after a month, when reconciliation runs, then Σ asset cost and Σ accumulated depreciation equal the GL accounts with 0.00 difference.
- **AC-37.9** (FR-37.15) Given a replacement value of USD 100,000.00 at index 112.0 (base year 2024) and an index of 118.5 for 2026, when the index update runs, then the insured value is 105,803.57 and assets insured below 95,223.21 (90%) are listed as under-insured.
- **AC-37.10** (FR-37.16) Given 40 DE assets of EUR 600.00 each acquired in 2026 in a Sammelposten group, when the tax book depreciates FY2026, then one group line of 24,000.00 / 5 = 4,800.00 posts and each member shows 0.00 own depreciation with a drill to the group.
- **AC-37.11** (FR-37.17) Given a capex request of USD 320,000.00, when submitted, then the workflow routes to the CFO level; when approved, then planned acquisitions of the selected variant appear in the depreciation forecast and `capexrequest.approved` reaches budget-svc.
- **AC-37.12** (FR-37.14) Given an asset with a financial addition of USD 2,000.00, when saved, then an AcquisitionAdj of 2,000.00 posts on every Open book and the addition links to the transaction.

### 7.37.9 Test plan and fixtures

Fixtures: `dep-100` (100 assets × SL, DB 150/200%, UoP, MACRS 3/5/7-year, conventions, short first years, salvage > 0), `lease-25` (accountant workbook: arrears/advance, monthly/annual, modifications, terminations), `disposal-30` (full/partial, before and after period end), `migration-nbv` (opening NBVs for the demo entity).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Method formulas | Unit | depreciation engine | match tables to the cent |
| Life totals | Property | engine | Σ depreciation = cost − salvage for random inputs |
| Lease PV and schedule | Unit/Property | lease engine | closing = 0.00; Σ interest = Σ payments − PV |
| Run posting | Contract | journal API | one entry per book; idempotent |
| Disposal flows | E2E | workspace | gain/loss per fixture |
| Run throughput | Performance | run | 1M asset-book-periods/hour |

Reconciliation checks: asset subledger to GL per book nightly; lease liability schedule to GL lease liability; ROU asset to GL ROU accounts.

### 7.37.10 Deployment and operations

Helm chart `asset-svc`; env `ASSET_RUN_BATCH_SIZE=500`, `ASSET_MAX_BOOKS=10`; Atlas migrations; flags `assets.physical-inventory`, `assets.asc842-operating`. Jobs: `asset-gl-reconcile` nightly 02:00, `lease-schedule-post` monthly on period open, `mid-quarter-test` on tax year end. SLOs: schedule API p95 < 200 ms; run throughput as above; reconciliation job < 10 min for 1M assets. Alerts: reconciliation difference ≠ 0, run failure, lease schedule unposted after period close. Rollback: Helm rollback; posted runs reversed through the reversal API, never deleted.

### 7.37.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AssetTable, AssetGroup | Asset, AssetClass | |
| AssetBook / AssetDepBook, AssetDepreciationProfile | AssetBook, DepreciationProfile | method/convention mapped; opening NBV as acquisition + accumulated depreciation transactions dated cutover |
| AssetTrans | AssetTransaction (summarized) | history summarized to opening balances; detail retained in archive |
| AssetLease* | Lease, LeaseScheduleLine | remaining schedule recomputed; variance to legacy shown |
| ANLA, ANLZ, ANKA | Asset, AssetClass | |
| ANLB, ANLC, T090NA | AssetBook, DepreciationProfile | depreciation key → profile map table; ANLC year values → opening balances |
| ANLP, ANEP | AssetTransaction (summarized) | |
| RE-FX contracts | Lease | |

Validation: Σ opening NBV per book = GL; count of active assets matches; recomputed next-period depreciation within 0.5% of legacy (differences listed). Not migrated: fully depreciated and retired assets older than 7 years (archive), AX value model posting profiles (re-authored).

### 7.37.12 Dependencies, risks, open questions

Depends on: wave 11 journal and dimensions, wave 12 posting layers, wave 14 period control, wave 18 AP invoice, wave 16 inventory journals, wave 36 for IC transfer settlement; forward-references wave 41 projects (CIP) and wave 44 EAM.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Legacy NBV differs from recomputed schedules | High | Medium | Load NBV as balances, not recomputed; variance report |
| ASC 842 operating lease single-cost rule complexity | Medium | Medium | Flag-gated; IFRS 16 first |

Open questions: (1) Posting summarization default per account or per asset? Proposed per asset; decide by wave 37 start. (2) Support 175%/125% DB in v1? Proposed yes (cheap); decide by wave 37. (3) Bonus depreciation handled as profile or adjustment? Proposed adjustment; decide by wave 37.

### 7.37.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `assets.numberSequence` | legal entity | sequence | FA-###### | number sequence id | asset numbers (FR-37.1) | Fixed assets parameters → Number sequences | AS08 number ranges |
| `assets.capitalizationThreshold` | legal entity | money | USD 2,500 / EUR 800 | ≥ 0 | AP lines below threshold do not propose asset creation | Fixed assets parameters → "Capitalization threshold" (D365) | OAYK low-value asset amount |
| `assets.createFromPurchasing` | legal entity | enum | ProductReceipt | Off / ProductReceipt / VendorInvoice | when AP-flagged lines create assets (FR-37.6) | Fixed assets parameters → "Create asset during product receipt or vendor invoice posting" | asset account assignment category A (OME9), ANLA creation in ME21N |
| `assets.defaultBooksByClass` | asset class | table | none | book definitions + profiles | books attached on create (FR-37.2) | Fixed asset groups → Books setup (AssetGroupBookSetup) | OAYZ depreciation areas per asset class |
| `assets.bookDefinition` | legal entity | table | Local GAAP, IFRS, Tax | ledger, posting layer, currency, posting allowed | posting target per book | Books (AssetBookTable), posting layer | OADB depreciation areas, OAOB chart of depreciation |
| `assets.postingProfile` | legal entity | table | none | (class, transaction type, book) → accounts | account determination | Fixed asset posting profiles (AssetLedgerAccounts) | AO90 account determination |
| `assets.depreciationSummarization` | book | enum | PerAsset | PerAsset / PerAccountDimension | journal line granularity (FR-37.5) | Fixed assets parameters → "Summarize depreciation" | OAYR posting rules (period and method per area) |
| `assets.depreciationPeriodicity` | book | enum | Monthly | Monthly / Quarterly / Yearly | run frequency | Depreciation profile → Depreciation year / frequency | OAYR period interval |
| `assets.catchUp` | book | bool | false | true / false | BR-37.3 arrears into current period | "Catch up depreciation" option | AFAB "unplanned/repeat run" |
| `assets.depreciateOnDisposal` | book | bool | true | true / false | depreciation to disposal date first (FR-37.8) | Fixed assets parameters → "Depreciate asset when disposing" | depreciation key period control for retirement (T090NR) |
| `assets.roundingMinorUnit` | book | enum | currency minor unit | minor unit / whole unit | BR-37.1 rounding | Depreciation profile → Rounding | OAYO rounding specifications |
| `assets.maxBooks` | tenant | int | 10 | 1–20 | books per asset (env `ASSET_MAX_BOOKS`) | none | max 99 depreciation areas |
| `assets.disposalApproval` | legal entity | enum | AboveNbv10k | None / Always / AboveNbv10k | workflow on disposal | Fixed assets parameters → "Approval required for disposals" (workflow) | none native (ABAVN no workflow) |
| `assets.midQuarterTest` | tax book | bool | true for US tax books | true / false | BR-37.5 | none | US-specific depreciation keys with mid-quarter (T090NA) |
| `assets.bonusPercent` | tax book × year | decimal | per wave-48 US pack | 0–100% | first-year bonus adjustment (FR-37.4) | Special depreciation allowance profile | special depreciation area (bonus) |
| `lease.periodicRateMethod` | legal entity | enum | Nominal/12 | Nominal/12 / Effective | monthly rate (FR-37.10) | Asset leasing parameters → interest calculation | RE-FX valuation rule (RECEEP) |
| `lease.lowValueThreshold` | legal entity | money | USD 5,000 / EUR 5,000 | ≥ 0 | low-value election | Asset leasing parameters → "Low value threshold" | RE-FX contract type exemption |
| `lease.shortTermMonths` | legal entity | int | 12 | 1–12 | short-term election | Asset leasing parameters | RE-FX valuation rule term |
| `insurance.types` / `insurance.companies` | tenant | table | seeded 4 types | code, description | FR-37.15 master data | Insurance types / Insurance companies | OAVI insurance types, insurance companies (T086) |
| `insurance.indexSeries` | tenant | table | none | series code, country, class, yearly values | FR-37.15 indexing | none | OAVF index series (T094) |
| `assets.groupAssetBooks` | tax book | bool | false | true / false | book allows group assets (FR-37.16) | none | OAYM / OAAX group asset areas (T093A) |
| `capex.approvalLevels` | legal entity | table | 50,000 / 500,000 / above | amount bands → approver role | FR-37.17 routing | Budget planning workflow (stand-in) | IMA approval status profile, appropriation request types (TAPRT) |
| `capex.overrunWarnPct` | legal entity | decimal | 10% | 0–100% | warning on spend vs approved | none | IM budget availability control tolerance |
| `count.sessionTolerance` | site | int | 0 | 0–10 | missing items allowed before escalation (FR-37.11) | none | none |

### 7.37.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Asset register | list inquiry | entity, class, book, status, location | number, name, in-service, cost, accumulated, NBV, life remaining | `asset_book` | Fixed assets list page, Fixed asset balances | AR01 asset balances |
| Asset history sheet (roll-forward) | statutory report | entity, book, fiscal year, class | opening cost, acquisitions, retirements, transfers, revaluation, closing cost; opening acc., depreciation, impairment, closing acc.; NBV | `asset_transaction` aggregated | SSRS Fixed asset statement / Fixed asset note | S_ALR_87011990 asset history sheet |
| Depreciation forecast | drill-down | entity, book, horizon months, include planned (FR-37.17) | period, existing assets, planned acquisitions, total | forecast projection (FR-37.12) | SSRS Fixed asset projection / Depreciation expense projection | S_ALR_87012936 depreciation simulation |
| Acquisitions / retirements list | operational report | entity, book, date range, source type | asset, date, amount, source document, gain/loss | `asset_transaction` | SSRS Fixed asset acquisitions / disposals | S_ALR_87012050 acquisitions, S_ALR_87012052 retirements |
| Asset transactions | list inquiry | asset, book, type, date | type, date, amount, journal, run | `asset_transaction` | Fixed asset transactions (AssetTrans) | AW01N asset explorer, S_ALR_87012048 |
| Tax depreciation (US 4562 data) | statutory report | entity, tax book, year | Part I §179, Part II bonus, Part III MACRS by recovery class and convention, mid-quarter flag | tax book projection | none | S_ALR_87010127 / 87010129 |
| Lease roll-forward and maturity analysis | statutory report | entity, period, classification | opening liability, additions, interest, payments, remeasurements, closing; undiscounted maturities < 1 / 1–5 / > 5 years (IFRS 16.58) | `lease_schedule_line` | Asset leasing → Lease summary / liability amortization report | RE-FX RECEEP evaluation, IFRS 16 disclosure reports |
| Insurance report | operational report | entity, insurer, type, expiry window | asset, policy, insured value, indexed replacement value, NBV, under-insured flag | `asset_insurance_policy` | SSRS Fixed asset insurance report | S_ALR_87011951 |
| Physical inventory list | operational report | site, location, session | expected, found, missing, moved | `count_session` | none | S_ALR_87011979 asset inventory list |
| Subledger to GL reconciliation | KPI / drill-down | entity, book, period | subledger cost / acc., GL balance, difference per account | reconciliation job output (FR-37.13) | Fixed asset reconciliation (D365 report) | ABST2 reconciliation analysis |
| Group asset members | drill-down | group asset, year | members, basis, group depreciation share | `group_asset` | none | AS23 group asset display |
| Capex request pipeline | KPI | entity, status, program, year | count, requested, approved, spent, overrun % | `capex_request` | none | IM appropriation request information system, IMCCV1 |

### 7.37.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Asset tag label | asset create / reprint | asset number, barcode (Code 128 or QR), entity, location | PDF label (50 × 25 mm), ZPL for thermal printers | asset number | none |
| Asset transfer slip | transfer / IC transfer (FR-37.7) | asset, from / to dimension, location, entity, NBV, approver | PDF | transfer document number | 10 years (IC transfer evidence) |
| Disposal / scrap certificate | scrap disposal (FR-37.8) | asset, date, method, witness, NBV written off | PDF | disposal number | 10 years; environmental proof for regulated waste where required by pack |
| Asset history sheet | period / year end | as 7.37.14 | PDF, XLSX, XBRL tags via wave 15 | none | statutory notes to accounts; 10 years |
| Lease schedule | lease activation / modification | lease, payments, interest, liability, ROU | PDF, XLSX | lease number | IFRS 16 / ASC 842 audit file; 10 years |
| IRS Form 4562 | tax year end | US pack layout | PDF (wave 48) | none | 7 years (US) |
| Count sheet | count session start | location, expected assets, barcode, found box | PDF, PWA list | session number | none |
| Capex request approval record | approval | request, variant, amounts, NPV/IRR, approvers with timestamps | PDF | request number | 10 years (capex governance) |

### 7.37.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `depreciation-run` | monthly on period open + on demand | (book, period, legal entity) | per-asset checkpoint; (book, period) advisory lock | < 6 min for 100k assets | failing assets listed; others post | run failure, exceptions > 0 |
| `lease-schedule-post` | monthly on period open | legal entity | (lease, period) posted flag | < 2 min for 5,000 leases | per-lease isolation | unposted after close |
| `asset-gl-reconcile` | nightly 02:00 | (entity, book) | (entity, book, date) | < 10 min for 1M assets | report only | difference ≠ 0 |
| `mid-quarter-test` | tax year end + on demand | (entity, tax book, year) | (entity, book, year) result | < 2 min | alert; no auto-post without approval | Q4 share > 40% |
| `depreciation-forecast-publish` | nightly 03:00 | legal entity | snapshot version | < 5 min | previous snapshot kept | budget source stale > 2 days |
| `insurance-index-update` | yearly 01-15 + on load | index series | (series, year) | < 1 min | missing year → AST-0531 | under-insured count change |
| `insurance-expiry-check` | daily 06:00 | legal entity | (policy, alert day) | < 1 min | retry | expiring policies |
| `capex-overrun-check` | daily 05:00 | legal entity | (request, date) | < 1 min | warning only | overrun > `capex.overrunWarnPct` |

### 7.37.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| AssetView | GET /assets/*, workspace, all 7.37.14 inquiries | none | Fixed asset accountant, Controller, Auditor |
| AssetMaintain | POST /assets, /books, /additions, /transfer | AssetDispose on same asset when disposal approval is on | Fixed asset accountant |
| AssetConfigure | classes, book definitions, profiles, posting profiles, group assets | AssetPostJournal | Finance system admin |
| AssetDepreciate | /depreciation-runs (propose) | none | Fixed asset accountant |
| AssetPostJournal | /depreciation-runs/*/post, /transactions | AssetConfigure; AccountsPayableInvoiceApprove for AP-sourced acquisitions | Fixed asset accountant |
| AssetDispose | /dispose | AccountsReceivableInvoicePost for the sale invoice | Fixed asset accountant |
| AssetDisposeApprove | disposal workflow approve | AssetDispose | Controller |
| LeaseMaintain | /leases, /modify | LeasePostApprove | Lease accountant |
| AssetInsuranceMaintain | /insurance-policies, index series | none | Risk manager |
| AssetCount | /count-sessions, PWA scanner | AssetMaintain (counter cannot adjust register) | Site asset custodian |
| CapexRequestCreate | /capex-requests, submit | CapexRequestApprove on own request | Plant manager, Engineering lead |
| CapexRequestApprove | workflow approve | CapexRequestCreate (same request) | CFO, Controller |

### 7.37.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| AST-0501 | error | Book `{book}` period `{period}` is closed for fixed assets | period control (wave 14) | open period or post in current | FR-37.5 |
| AST-0502 | error | Depreciation for `{book}` `{period}` is already posted | rerun without reversal | reverse the prior run first | FR-37.5 |
| AST-0503 | error | Previous period `{period}` is not depreciated | sequential rule | run prior period or enable catch-up | BR-37.3 |
| AST-0504 | error | Another depreciation run holds `{book}` `{period}` | advisory lock | wait for completion | BR-37.9 |
| AST-0507 | error | Split percentages total `{pct}`; they must total 100.00% | split input | correct percentages | FR-37.7 |
| AST-0508 | error | Disposal date precedes last depreciated period `{period}` | backdated disposal | accept proposed reversal set | BR-37.6 |
| AST-0509 | error | Asset `{asset}` is `{status}`; only the disposal can be reversed | posting on Sold / Scrapped | reverse disposal | BR-37.2 |
| AST-0511 | warning | Q4 placed-in-service share `{pct}` exceeds 40%; mid-quarter applies | mid-quarter test | review and approve delta posting | BR-37.5 |
| AST-0512 | warning | Impairment reversal limited to `{amount}` | carrying amount cap | none | FR-37.9 |
| AST-0515 | error | Lease IBR `{rate}` outside (0, 50%] | invalid input | correct rate | FR-37.10 |
| AST-0516 | info | Lease remeasured; ROU adjusted by `{amount}` | modification | none | BR-37.8 |
| AST-0520 | error | Subledger differs from GL by `{amount}` on `{account}` | reconciliation | investigate listed assets | FR-37.13 |
| AST-0525 | warning | Barcode `{code}` not in register | count scan | create asset or mark found-unknown | FR-37.11 |
| AST-0531 | warning | Index `{series}` has no value for `{year}`; using `{lastYear}` | missing index | load index value | BR-37.10 |
| AST-0532 | warning | Asset `{asset}` is under-insured: `{insured}` vs `{indexed}` | insured < 90% indexed | review policy | FR-37.15 |
| AST-0535 | error | Asset `{asset}` already belongs to group `{group}` for book `{book}` | duplicate membership | remove via disposal / transfer | BR-37.11 |
| AST-0541 | warning | Capex request `{no}` spend `{spent}` exceeds approval by `{pct}` | overrun | request supplement | FR-37.17 |
| AST-0542 | info | Capex request `{no}` returned to Submitted after amount increase | BR-37.12 | approvers re-approve | BR-37.12 |

### 7.37.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Depreciation run | 100k assets × 3 books monthly | n/a | 1M asset-book-periods/h | 3.6M transactions ≈ 2 GB | AssetTransaction partitioned yearly by transaction_date; index (asset_book_id, transaction_date) | workers scale to 8; run < 20 min |
| Schedule API | 3,000/day; peak 60/min | 40 / 200 / 400 ms | 30 req/s | n/a | computed on read, cached per (book, version) | cache hit rate rises; p95 < 500 ms |
| Asset create from AP | 2,000/day; peak 200/min at invoice batch | 80 / 250 / 500 ms | 20/s | 0.7M assets ≈ 1 GB | Asset unique (entity, number), barcode unique | queued through outbox |
| Lease schedule compute | 50/day | 100 / 400 / 800 ms for 360 periods | 10/s | 5,000 leases × 120 lines ≈ 0.1 GB | by (lease_id, period_no) | none needed |
| GL reconciliation | 1M assets | n/a | < 10 min | small | aggregates by (book, account) | < 30 min; alerts delayed |
| Count scan (PWA) | 20 scanners × 1 scan/5 s | 50 / 150 / 300 ms | 10 scans/s | 0.5M scans ≈ 0.1 GB | by (session_id, barcode) | offline queue on device, sync later |
| Asset history sheet | 50 runs/month | 2 / 8 / 15 s for 100k assets | n/a | n/a | materialized yearly roll-forward | served from previous night snapshot with age |

### 7.37.20 Localization and compliance hooks

- **Tax depreciation methods and tables** vary by country: US MACRS / ADS, §179, bonus, Form 4562 (US pack); DE AfA tables, GWG (≤ EUR 800 immediate expense), Sammelposten pool (FR-37.16); FR dégressif coefficients 1.25 / 1.75 / 2.25 and amortissement dérogatoire (excess tax over book posted to a regulated provision); UK capital allowances (AIA, WDA 18% / 6% pools, full expensing) as group assets; IN Companies Act Schedule II lives and income-tax block of assets WDV; MX Ley del ISR maximum rates; CA CCA classes with half-year rule. Packs provide DepreciationProfile templates, conventions and statutory layouts through the `DepreciationMethodProvider` and `StatutoryAssetReport` extension points.
- **Statutory asset registers**: FR tableau des immobilisations (liasse 2054 / 2055), DE Anlagenspiegel (HGB §284), IT libro dei cespiti; layouts via the wave-48 electronic reporting designer.
- **Leases**: IFRS 16 (default), ASC 842 operating lease single-cost (flag), local GAAP packs where leases remain off-balance (DE HGB, FR PCG) use a book with `leaseCapitalization = false`.
- **Retention**: asset records for the life of the asset plus 10 years (DE, FR), 7 years after disposal (US).
- **Currency and rounding**: books in non-functional currencies (e.g., hyperinflationary IAS 29 restatement) are out of v1; rounding per book currency minor unit.
- **Privacy**: responsible-worker field is personal data; masked for users without HR visibility (wave 42 policy).

### 7.37.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.6.1 Fixed asset → Fixed asset additions (AssetAddition) | PARTIAL-37 | ADDED as FR-37.14 | |
| AX 12.6.1 Fixed asset → Insurance records (AssetInsurance) | PARTIAL-37 | ADDED as FR-37.15 | |
| AX 12.6.2 SSRS Fixed asset insurance report | PARTIAL-37 | ADDED as FR-37.15 | Insurance report in 7.37.14 |
| AX 12.6.4 Fixed asset → Insurance types / Insurance companies | PARTIAL-37 | ADDED as FR-37.15 | `insurance.types` / `insurance.companies` |
| SAP 13.4 AS21 / AS22 / AS23 Group asset | PARTIAL-37 | ADDED as FR-37.16 | |
| SAP 13.4 Insurance values / indexing (S_ALR_87011951) | PARTIAL-37 | ADDED as FR-37.15 | index series formula BR-37.10 |
| SAP 13.4 S_ALR_87010127 / 87010129 Tax depreciation reports (Form 4562 / mid-quarter) | PARTIAL-37 | DEFERRED to wave 48 | data (Part I–III amounts, mid-quarter flag) is in 7.37.14; the IRS form layout ships with the US pack |
| SAP 13.4 OAYM / OAAX Group assets per area (T093A) | PARTIAL-37 | ADDED as FR-37.16 | `assets.groupAssetBooks` |
| SAP 13.4 OAVI / OAVF Insurance types, companies, index series (T086, T094) | PARTIAL-37 | ADDED as FR-37.15 | `insurance.indexSeries` |
| SAP 13.27 IM01–IM36 / IMA11 / IMA2 / IMA3 / IMCCP1 Investment programs and appropriation requests | PARTIAL-37 | ADDED as FR-37.17 (appropriation requests) and FR-38.21 (programs) | program budget distribution needs wave-38 budget control |
| SAP 13.27 IM reports (S_ALR_87012…) Investment program reports | PARTIAL-37 | ADDED as FR-37.17 / FR-38.21 | Capex request pipeline (7.37.14), Investment program report (7.38.14) |
| SAP 13.27 IM27 / IM32 IM budget rollup / carry-forward | PARTIAL-37 | DEFERRED to wave 38 | ADDED there as FR-38.21 |
| SAP 13.27 IM customizing: program types, appropriation request types, status, budget categories, plan / budget profiles (TAIF, TAPRT) | PARTIAL-37 | ADDED as FR-37.17 / FR-38.21 | `capex.approvalLevels` here; program types in 7.38.13 |

## 7.38 Wave 38: Cost Accounting, Budgeting & Budget Control

### 7.38.1 Scope and business context

Wave 38 gives controllers a planning and control layer separate from statutory accounting. Before it, the universal journal records actuals by dimension but there is no budget to compare against, no way to stop a purchase order that exceeds a department's funds, and no management view that reclassifies and allocates costs the way the business is run. After it, budget register entries and planning scenarios flow through workflow into budget models; budget control checks requisitions, POs and invoices against available funds, recording encumbrances and liquidating them exactly; a cost accounting ledger mirrors primary costs from the journal into cost objects and cost elements, runs assessment/distribution allocations that converge and reconcile, supports activity-based costing and internal orders, and feeds the management reporting cubes consumed by wave 46.

Out of scope: public-sector fund accounting (appropriations, apportionments, year-end encumbrance carry-forward beyond the simple option), profitability analysis segment reporting (CO-PA style, wave 46 semantic layer), project budgets (wave 41).

**Scope amendment (gap closure).** The registers (coverage-ax §12.99 rows 34–37, 45–46; coverage-sap §13.99 rows 86–123 mapped to wave 38, plus the investment-program rows handed over from wave 37) add Excel worksheet templates (FR-38.13), plan generation from sources (FR-38.14), position forecasting (FR-38.15), cost behavior / overhead rate / cost rollup policies (FR-38.16), template (driver-formula) allocation (FR-38.17), cost object hierarchies (FR-38.18), top-down distribution (FR-38.19), budget reservation documents (earmarked funds, FR-38.20) and investment programs (FR-38.21). Fund and funded-program accounting uses financial dimensions (7.38.21); appropriations and apportionments remain out of scope.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| D365 budget check on PO happens in a batch-like synchronous call that takes 3–8 s per line on large BudgetSourceTracking tables; buyers disable it | Available-funds query aggregates BudgetSourceTrackingDetail at check time | Per dimension-combination balance cache rebuilt from events; check is one indexed read | Check latency 5 s → p95 < 150 ms |
| SAP FM availability control (FMAVCR01) rebuilds the AVC ledger (FMAVCT) when budget structure changes; FMAVCREINIT takes hours | Derived ledger with offline rebuild | Balance cache with incremental event replay | Rebuild for 1M documents 3 h → < 10 min |
| Encumbrance liquidation on invoice in AX leaves orphan amounts when the PO was partially received at a different price (BudgetSourceTracking residual) | Relief computed from invoice amount, not from PO reservation | Relief = min(invoice-matched share of PO reservation, remaining reservation); property-tested to the cent | Orphan encumbrances per month 20–50 → 0 |
| KSU5/KSV5 cycles in SAP: a cycle with a circular segment fails with "iteration not converging" and the controller must restructure segments | Fixed 1,000-iteration loop with no exact solve | Exact linear solve ≤ 2,000 senders; residual sweep rule | Failed cycles per close 2–3 → 0 |
| CAM cost accounting ledger in D365 requires "Process data" batch and loads GL into CAMCostEntry hours after posting | Separate ledger filled by batch import | Cost entries mirrored from journal events within seconds | Lag 4–24 h → < 5 s |

### 7.38.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Budget register entries | BudgetTransactionHeader, BudgetTransactionLine, BudgetTransactionCode, BudgetModel, BudgetCycle | FMBB budget entry documents, FMBH; KP06/KP26 plan versions | BudgetRegisterEntry, BudgetModel | full |
| Budget planning | BudgetPlanHeader/Line, BudgetPlanningProcess, BudgetPlanStage, BudgetPlanScenario, BudgetPlanWorkflow, BudgetPlanTemplate (Excel) | KP06 plan versions, BPC/SAC planning | BudgetPlan with scenarios, stages, Temporal workflow | full |
| Budget control configuration | BudgetControlConfiguration, BudgetControlRule, BudgetGroup, BudgetControlUserGroup, budget threshold, funds available calculation | FM: FMDERIVE, funds centers FMFCTR, commitment items FMCI, tolerance profiles (OFUP/OF11), AVC ledger FMAVCT | BudgetControlRule with dimension subset, time span, tolerances, overrides | full |
| Encumbrances / commitments | BudgetSourceTracking, BudgetSourceTrackingDetail, BudgetCheckResult, LedgerSourceDocument encumbrance posting (purchase order encumbrance posting type) | COOI commitments, FMIOI open items, KSB2 | Encumbrance entity with reservation/relief lines | full |
| Cost accounting ledger | CAMCostAccountingLedger, CAMCostObject, CAMCostElement, CAMCostElementDimension, CAMDimensionHierarchy, CAMCostEntry | CO area, KS01/KS02 CSKS, KSH1 hierarchy, CSKA/CSKB cost elements (KA01/KA06), COEP/COBK line items, COSP/COSS totals | CostObject, CostElement, CostEntry, DimensionHierarchy | full |
| Allocations | CAMCostAllocationPolicy, CAMCostDistributionPolicy, CAMAllocationBase, CAMCostBehaviorPolicy | KSV5 distribution, KSU5 assessment, cycles T811C/T811S/T811K, KSC6 indirect activity allocation | AllocationPolicy (assessment/distribution/reciprocal), AllocationBase | improved (exact solve) |
| Statistical measures / activity types | CAMStatisticalDimension, statistical measure provider templates | KL01 CSLA activity types, KP26 prices, KB21N | StatisticalMeasure, ActivityType with rates | full |
| Internal orders | Not native (projects used) | KO01 AUFK internal orders, KO88 settlement | InternalOrder cost object type with settlement rules | full |
| Cost control workspace | CAMCostControlWorkspace, overhead calculation, variance by cost object | S_ALR_87013611 (cost centers actual/plan/variance), KSB1 | Cost control workspace with plan/actual/variance drill | full |
| Budget plan worksheets | BudgetPlanTemplate, Excel budget plan worksheet (Office add-in), BudgetPlanLayout (D365 layouts) | KP06 with planner profiles (KP34), SAC / BPC input forms | WorksheetTemplate with xlsx round-trip (FR-38.13) | full |
| Generate plan from source | Budget plan generation: from GL actuals, from budget plan, from fixed assets, from project forecasts, from demand forecast, from position forecasts | KP97 copy planning, KP98 copy actual to plan | PlanGenerationRun with sources (FR-38.14) | full |
| Position forecasting | HcmPositionForecast, HcmPositionForecastBudgetAcctLine, budget cost elements | HCM personnel cost planning (PCP, HRHCP00*) | PositionForecast with cost elements (FR-38.15) | partial (HR-driven source when wave 42 ships) |
| Cost behavior / overhead / rollup policies | CAMCostBehaviorPolicy, CAMOverheadRatePolicy, CAMCostRollupPolicy | Cost center categories (OKA2), overhead costing sheets on cost centers (KZS2 / KGI2), cost center group rollups (KSH1) | CostBehaviorPolicy, OverheadRatePolicy, CostRollupPolicy (FR-38.16) | full |
| Template allocation | none | CPT1 templates, CPTA / CPTB template allocation (actual / plan) | AllocationTemplate with driver formulas (FR-38.17) | full |
| Cost object hierarchy | CAMDimensionHierarchy (reporting only) | KKPHIE / KKP2 cost object hierarchy, KKP4 collective entry, KKPX distribution | CostObjectHierarchy with node collection and distribution (FR-38.18) | full |
| Top-down distribution | none | KE28 top-down distribution (CO-PA) | Allocation type TopDown (FR-38.19) | full |
| Earmarked funds | Budget reservations (D365 public sector, BudgetReservation_PSN) | FMX1 / FMX2 / FMX3 funds reservation, FMY1 precommitment, FMZ1 commitment, FMW1 revenue forecast, FMV1 funds blocking, FMEF document types | BudgetReservation with types (FR-38.20) | full |
| Investment programs | none native | IM01–IM36 programs and positions, IM52 budget distribution, IM32 budget carry-forward, IM27 rollup | InvestmentProgram, ProgramPosition (FR-38.21) | full |

### 7.38.3 Functional requirements

- **FR-38.1 Budget models and register entries** — Must. The system shall record budget register entries (Original, Revision, Transfer, Carry-forward, Pre-encumbrance, Encumbrance reservation) per budget model, cycle, dimension combination and period, with workflow approval.
  - Notes: a transfer must net to zero across its lines; entries post to a budget ledger separate from the universal journal; budget model hierarchies (sub-models) roll up; 2M register lines per model supported.
- **FR-38.2 Budget planning** — Must. The system shall run planning processes with stages, scenarios (up to 10 per plan), top-down and bottom-up aggregation, distribution keys, and Excel import/export, ending in a register entry.
  - Notes: scenarios compared side-by-side; comments per cell; stage transitions by Temporal workflow; budget plan line count up to 500,000 per plan.
- **FR-38.3 Budget control configuration** — Must. The system shall define control rules per legal entity: budget control dimensions (subset of ledger dimensions), budget cycle time span (year / quarter / period / custom), funds-available formula, documents under control (requisition, PO, vendor invoice, GL journal, expense, project), thresholds (warn %, block %), and user-group overrides.
  - Notes: rule activation versioned with effective date; disabling control does not delete history.
- **FR-38.4 Budget check order of operations** — Must. The system shall evaluate a budget check in this fixed order: (1) determine whether the document type and main account range are under control; (2) derive the budget control dimension combination from the line's ledger dimension; (3) resolve the budget cycle and time span containing the line's accounting date; (4) relieve the document's own prior reservation (revision of a PO, invoice matching a PO) so only the net change is checked; (5) compute available = original + revisions + transfers in − transfers out + carry-forward − actuals − encumbrances − pre-encumbrances (− unposted documents if enabled); (6) compare the net change against available with the rule's thresholds: ≤ warn% → pass, between warn% and block% → warn, > block% → fail; (7) apply user-group override (fail → warn for groups with `BudgetOverride`); (8) record BudgetCheckResult and, on pass/warn, reserve the amount atomically.
  - Notes: evaluation uses a per-combination balance row with `SELECT … FOR UPDATE` so two concurrent POs cannot both pass against the last dollar; p95 < 150 ms; results retained 7 years.
- **FR-38.5 Encumbrance lifecycle** — Must. The system shall create pre-encumbrances for approved requisitions, convert to encumbrances on PO confirmation, adjust on PO change, relieve on invoice posting or PO cancellation, and optionally carry forward at year end.
  - Notes: relief = min(invoice quantity × PO unit price, remaining reservation) per PO line; price differences on invoice create a new check for the excess; closed POs release residuals.
- **FR-38.6 Cost accounting ledger** — Must. The system shall maintain cost objects (cost centers, internal orders, products, projects as references), cost elements (primary mapped from main accounts, secondary for allocations), dimension hierarchies, and cost entries mirrored from journal events within 5 s.
  - Notes: cost behavior (fixed/variable) classified by policy; one ledger per legal entity and currency; versioned hierarchies effective-dated.
- **FR-38.7 Allocation policies** — Must. The system shall run distribution (primary elements retained) and assessment (secondary element) allocations by fixed percentage, statistical measure, or actual cost basis, in ordered steps with reciprocal support.
  - Notes: iteration convergence rule: for reciprocal steps with n senders, when n ≤ 2,000 solve `(I − A) x = b` exactly (LU with partial pivoting); otherwise iterate Gauss–Seidel until max absolute unallocated residual on any sender ≤ 0.01 currency units or 100 iterations; after loop, any residual is swept to that sender's largest receiver; invariant `Σ cost in = Σ cost out` to the cent; rounding residual per step posted to the largest receiver.
- **FR-38.8 Activity-based costing** — Should. The system shall define activity types with planned rates, record activity quantities from production (wave 21) and manual entries, and allocate at rate × quantity with rate variances.
- **FR-38.9 Internal orders** — Should. The system shall create internal orders with budgets, collect costs, and settle to cost centers, assets (wave 37) or P&L by settlement rule.
- **FR-38.10 Cost control workspace** — Must. The system shall show plan vs actual vs committed per cost object and element, with variance %, drill to cost entries and to journal lines, and period comparison.
- **FR-38.11 Reconciliation** — Must. The system shall reconcile Σ primary cost entries per element and period to the journal balances and list differences.
- **FR-38.12 Management cubes** — Should. The system shall publish cost ledger facts and hierarchies to the lakehouse via CDC with a documented star schema for wave 46.
- **FR-38.13 Budget plan worksheet templates** — Should. The system shall define worksheet templates per planning process (row dimensions, column sets per scenario / period / prior-year actual, editable and protected ranges, formulas) and round-trip them as xlsx files whose upload validates and writes back only editable cells.
  - Notes: each exported workbook carries the plan id, stage, template version and a SHA-256 per protected range in a hidden sheet; upload is rejected when the stage changed, the template version differs or a protected range checksum fails; cell-level changes are applied as a diff with an audit row per cell; maximum 200,000 editable cells per upload; formulas evaluate in Passage (not trusted from the file) so totals always recompute server-side.
- **FR-38.14 Generate budget plan from sources** — Must. The system shall create or refresh plan lines from: GL actuals of a period range (with % uplift), another plan or scenario, depreciation forecast (FR-37.12) and approved capex requests (FR-37.17), demand forecast (wave 22) × standard cost (wave 39) or price, project forecasts (wave 41, when available), and position forecasts (FR-38.15).
  - Notes: generation modes Replace, Add, Update-if-unchanged (keeps manually edited cells); every generated line keeps its source reference; the run reports lines created / updated / skipped; 500,000 lines generate in < 3 min.
- **FR-38.15 Position forecasting** — Could. The system shall maintain forecast positions with start / end dates, FTE, compensation group and budget cost elements (salary, benefits %, payroll tax %), compute monthly cost per position and publish it as a plan source.
  - Notes: positions are entered manually or imported from CSV in wave 38; when wave 42 ships, the `HrPositionSource` populates them from effective-dated positions and compensation plans; cost element formulas: salary / 12 × FTE, benefits = salary component × %, tax capped by an annual ceiling per cost element; vacancy factor % per department.
- **FR-38.16 Cost behavior, overhead rate and cost rollup policies** — Should. The system shall classify cost entries as fixed or variable by policy rules (cost element range × cost object type, or regression result with a manual override), apply overhead rates per cost object and element to produce overhead cost entries, and define rollup policies that aggregate cost objects into hierarchy nodes for reporting.
  - Notes: policy evaluation order: explicit rule → element default → Unclassified; regression option fits cost against a statistical measure over ≥ 12 periods and classifies the intercept as fixed; overhead rate entries carry origin `Overhead` and a secondary element; rollup policies do not post, they define reporting aggregation; policies effective-dated and versioned.
- **FR-38.17 Template (driver-formula) allocation** — Should. The system shall allocate costs from a sender to receivers using templates whose lines evaluate formulas over receiver attributes and statistical measures (for example 25.00 × inspection lots + 0.5% × material cost) for actual and plan versions.
  - Notes: formula language is a restricted expression grammar (numbers, + − × ÷, min / max, if, references to measures, attributes and cost element balances); evaluation is per receiver per period; a template run produces one allocation run (FR-38.7 conservation invariant applies); formulas are validated at save and dry-run against the last closed period.
- **FR-38.18 Cost object hierarchies** — Should. The system shall define cost object hierarchies (for example product group → product → production order), collect costs at a node that cannot be assigned to a lower object, and distribute them to the lower objects by a basis (actual output, standard cost of output, fixed %) at period end.
  - Notes: a node may have collected costs only when its hierarchy type allows it; distribution posts origin `HierarchyDistribution` with the receiving order and feeds wave-39 variance (the distributed cost counts as actual input of category `Distributed`); undistributable residual remains on the node with a warning.
- **FR-38.19 Top-down distribution** — Should. The system shall distribute an amount booked at an aggregated level (for example freight per legal entity) to detailed segments (customer, product, region) in proportion to a reference basis from a reference period.
  - Notes: reference basis: revenue, quantity, COGS or any measure in the cost ledger or the wave-46 semantic layer; zero-basis segments are excluded; result posts as statistical (reporting) or actual cost entries per option; worked example: freight USD 12,000.00 with revenue shares 300,000 / 150,000 / 50,000 → 7,200.00 / 3,600.00 / 1,200.00.
- **FR-38.20 Budget reservation documents (earmarked funds)** — Should. The system shall create budget reservation documents of types FundsReservation, Precommitment, FundsBlocking and RevenueForecast that reserve or block available funds without a requisition, and let requisitions, POs, invoices and journals consume them by reference.
  - Notes: a reservation runs the FR-38.4 check on creation; consumption by a follow-on document relieves the reservation and creates the follow-on encumbrance in one transaction; FundsBlocking cannot be consumed; RevenueForecast increases available funds only when the rule sets `revenue-increases-budget`; residuals release on close or on expiry date; per-type number sequences.
- **FR-38.21 Investment programs** — Should. The system shall maintain investment programs per approval year with a hierarchy of program positions, distribute program budget top-down to positions, assign capex requests (FR-37.17), assets under construction and projects to positions, roll up plan / budget / actual, and carry forward unused budget to the next approval year.
  - Notes: a child position's budget cannot exceed the parent's undistributed budget; budget control (FR-38.4) for capex documents uses the program position as a control dimension; carry-forward moves (budget − actual − commitments) per position into the new program year as a CarryForward register entry; program types define allowed position levels (≤ 6) and budget categories (Capex, Opex-related, Contingency).

### 7.38.4 Data model

**BudgetRegisterEntry**: id, legal_entity_id, budget_model_id, entry_type (Original/Revision/Transfer/CarryForward/PreEncumbrance/Encumbrance), budget_cycle_id, status (Draft/Submitted/Approved/Posted/Rejected), lines[] (dimension_combination_id, period_key, amount numeric(19,4), currency), workflow_id, posted_at.

**BudgetControlBalance**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| rule_version_id | uuid | yes | FK BudgetControlRule version |
| control_dimension_key | text | yes | hashed dimension subset |
| time_span_key | text | yes | e.g. FY2026, 2026-Q3, 2026-09 |
| budget_total | numeric(19,4) | yes | original + revisions + transfers + carry-forward |
| actuals | numeric(19,4) | yes | from journal events |
| encumbrances | numeric(19,4) | yes | |
| pre_encumbrances | numeric(19,4) | yes | |
| version | bigint | yes | optimistic + row lock on check |

**Encumbrance**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| source_type | enum | yes | Requisition/PurchaseOrder/Invoice/Journal |
| source_line_id | uuid | yes | |
| balance_id | uuid | yes | FK BudgetControlBalance |
| reserved | numeric(19,4) | yes | |
| relieved | numeric(19,4) | yes | Σ relief lines |
| status | enum | yes | Open/PartiallyRelieved/Relieved/Released |
| lines | jsonb | yes | [{type: Reserve/Adjust/Relieve/Release, amount, documentId, at}] |

**CostEntry**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK, append-only |
| ledger_id | uuid | yes | cost accounting ledger |
| cost_object_id | uuid | yes | FK CostObject |
| cost_element_id | uuid | yes | FK CostElement (primary/secondary) |
| period_key | text | yes | |
| amount | numeric(19,4) | yes | |
| behavior | enum | yes | Fixed/Variable/Unclassified |
| origin | enum | yes | Journal/Allocation/Activity/Statistical/Manual |
| source_id | uuid | yes | journal line, allocation run, etc. |
| allocation_run_id | uuid | no | |

**AllocationPolicy**: id, ledger_id, name, type (Distribution/Assessment/Reciprocal), steps[] (order, sender filter, receiver filter, basis {Fixed%/StatisticalMeasure/ActualCost}, secondary_element_id), schedule, version.

**Entities added by FR-38.13–FR-38.21**: **WorksheetTemplate** (id, planning_process_id, version, row_dimensions[], column_sets jsonb, protected_ranges jsonb, formulas jsonb); **PlanGenerationRun** (id, plan_id, scenario_id, source_type, parameters jsonb, mode, created, updated, skipped); **PositionForecast** (id, legal_entity_id, position_code, department_dim, start_date, end_date, fte numeric(5,2), compensation_group_id, cost_elements jsonb, source Manual/HR); **CostBehaviorPolicy / OverheadRatePolicy / CostRollupPolicy** (id, ledger_id, version, effective_from, rules jsonb); **AllocationTemplate** (id, ledger_id, sender_filter, lines [{receiverFilter, formula, secondaryElementId}], version); **CostObjectHierarchy / HierarchyNode** (id, type, parent_id, cost_object_id, collects bool, distribution_basis); **BudgetReservation** (id, legal_entity_id, type FundsReservation/Precommitment/FundsBlocking/RevenueForecast, number, lines [{dimension_combination_id, period_key, amount, consumed}], expiry_date, status Open/PartiallyConsumed/Consumed/Closed/Expired); **InvestmentProgram / ProgramPosition** (program: id, code, approval_year, program_type_id, currency, status; position: id, program_id, parent_id, code, level, budget, distributed, plan, actual, commitment, assigned_object_refs[]).

Invariants: a BudgetControlBalance's available = budget_total − actuals − encumbrances − pre_encumbrances; Σ Encumbrance.reserved − relieved for a balance = balance.encumbrances; Σ CostEntry(origin=Allocation) per run = 0 (senders negative, receivers positive). Indexes: BudgetControlBalance unique (rule_version_id, control_dimension_key, time_span_key); CostEntry partitioned by period_key, index (cost_object_id, cost_element_id, period_key).

### 7.38.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/budget/register-entries | Create entry | Idempotency-Key | BudgetMaintain |
| POST | /api/v1/budget/register-entries/{id}/submit | Start workflow | state check | BudgetMaintain |
| POST | /api/v1/budget/plans/{id}/scenarios | Add scenario | Idempotency-Key | BudgetPlan |
| POST | /api/v1/budget/checks | Perform check (document lines) | documentId+version | BudgetCheck (system) |
| GET | /api/v1/budget/available?rule=&dims=&span= | Available funds | n/a | BudgetView |
| POST | /api/v1/budget/encumbrances/{id}/relieve | Relief from invoice | documentId | BudgetCheck (system) |
| POST | /api/v1/controlling/allocation-runs | Run policy for period | (policy, period, version) | CostAccountingRun |
| GET | /api/v1/controlling/cost-objects/{id}/entries?period= | Entries | n/a | CostAccountingView |
| POST | /api/v1/controlling/statistical-measures/{id}/values | Load measure values | Idempotency-Key | CostAccountingMaintain |
| POST | /api/v1/controlling/internal-orders/{id}/settle | Settle | (order, period) | CostAccountingRun |
| POST | /api/v1/budget/plans/{id}/worksheets/export | Export xlsx for template and stage | n/a | BudgetPlan |
| POST | /api/v1/budget/plans/{id}/worksheets/import | Upload xlsx diff | file SHA-256 | BudgetPlan |
| POST | /api/v1/budget/plans/{id}/generate | Generate from source | (plan, scenario, source, version) | BudgetPlan |
| POST | /api/v1/budget/reservations | Create earmarked funds document | Idempotency-Key | BudgetReserve |
| POST | /api/v1/budget/investment-programs/{id}/distribute | Distribute budget to positions | Idempotency-Key | InvestmentProgramMaintain |
| POST | /api/v1/controlling/template-allocations/runs | Run template allocation | (template, period, version) | CostAccountingRun |
| POST | /api/v1/controlling/top-down-runs | Run top-down distribution | (policy, period, version) | CostAccountingRun |

Validation: check requests must carry accounting date, ledger dimension and amount per line; allocation run rejects when period is closed in the cost ledger or a prior step's run for the period is missing.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.budget.register.posted.v1` | entry posted | entryId, modelId, type, lines summary | balance projector, analytics |
| `dev.passage.budget.check.completed.v1` | check | sourceDocumentId, lineResults[{result, available, amount}] | procurement, AP, workflow |
| `dev.passage.budget.encumbrance.relieved.v1` | relief | encumbranceId, amount, invoiceId, remaining | analytics |
| `dev.passage.controlling.allocation.completed.v1` | run complete | runId, policyId, iterations, totalIn, totalOut, residual | budget-svc, analytics |
| `dev.passage.controlling.costentry.created.v1` | mirror or allocation | entryId, costObjectId, elementId, amount, origin | lakehouse |
| `dev.passage.budget.reservation.consumed.v1` | follow-on document consumes reservation | reservationId, documentId, amount, remaining | analytics |
| `dev.passage.budget.program.carriedforward.v1` | year-end carry-forward | programId, fromYear, toYear, positions[], amount | budget-svc, asset-svc |
| `dev.passage.controlling.hierarchy.distributed.v1` | node distribution | runId, nodeId, period, distributed, residual | costing-svc (39), analytics |

### 7.38.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Budgeting workspace | New entry, approvals, available funds by rule | model, cycle, approved/pending, over-budget alerts | BudgetView |
| Budget plan grid | Edit cells, distribute, compare scenarios, submit stage | dimension rows × period columns, scenario columns, comments | BudgetPlan |
| Budget control rules | Configure dimensions, thresholds, user groups, activate | rule version, effective date, documents in control | BudgetConfigure |
| Budget check result (inline in PO) | View reason, request override | available, amount, result, threshold | Buyer |
| Cost accounting workspace | Run allocations, process status, reconciliation | ledger, period, steps status, in/out totals | CostAccountingRun |
| Cost control | Filter object/hierarchy node, drill | plan, actual, committed, variance, variance % | CostAccountingView |

Interaction rules: plan grid supports copy/paste from Excel, keyboard fill-down, and distribute-by-key; budget check result renders inside the PO line without leaving the form; allocation runs show step progress and residual live via server-sent events.

### 7.38.7 Business rules and edge cases

- **BR-38.1** Budget check amounts convert to the ledger's accounting currency at the document's rate; a later rate change does not re-check.
- **BR-38.2** Register entry state machine: Draft → Submitted → Approved → Posted; Submitted → Rejected → Draft; Posted entries are immutable and corrected by a Revision.
- **BR-38.3** A PO revision that lowers the amount never fails a check; it releases the difference.
- **BR-38.4** Invoice exceeding the PO reservation: the matched part relieves; the excess is checked as a new amount under the invoice's document rule.
- **BR-38.5** Time span resolution for a date on a period boundary uses the legal entity's fiscal calendar; dates outside any open cycle fail the check with reason `NoBudgetCycle`.
- **BR-38.6** Allocation convergence: iteration stops at residual ≤ 0.01 or 100 iterations; residual sweep goes to the sender's largest receiver by basis share; step totals must satisfy `Σ out = Σ in` to the cent or the run fails and rolls back.
- **BR-38.7** Allocation runs for a period are versioned; rerun reverses the prior run's entries (origin Allocation) and creates new ones; actuals-based runs after new journal postings require rerun, flagged by the reconciliation job.
- **BR-38.8** A sender with zero basis in all receivers keeps its cost and is reported as unallocated; the run does not fail.
- **BR-38.9** Internal order settlement cannot exceed collected cost; over-settlement is rejected.
- **BR-38.10** Year-end carry-forward of open encumbrances creates a CarryForward register entry in the new cycle and releases the old reservation atomically.
- **BR-38.11** Worksheet import applies only to cells whose server value equals the value at export time or that were not changed on the server since; conflicting cells are rejected and listed (CO-0627); the rest apply.
- **BR-38.12** Template allocation formulas returning a negative receiver amount fail the run unless the template sets `allow-negative`; division by a zero measure evaluates to 0 for that receiver and raises a warning.
- **BR-38.13** Top-down distribution: residual cents from rounding go to the segment with the largest basis; Σ distributed = source amount to the cent.
- **BR-38.14** Budget reservation state machine: Open → PartiallyConsumed → Consumed → Closed; Open | PartiallyConsumed → Expired at expiry date (residual released); FundsBlocking goes Open → Closed only.
- **BR-38.15** Investment program distribution: Σ child budgets ≤ parent budget at all times; reducing a parent below Σ children is rejected; carry-forward is allowed once per position and year.
- **BR-38.16** Cost behavior reclassification after period close applies from the next open period only; closed-period entries keep their classification.

### 7.38.8 Acceptance criteria

- **AC-38.1** (FR-38.4) Given budget 100,000.00 with actuals 60,000.00 and encumbrances 30,000.00, when a PO line of 12,000.00 is checked with warn 0% and block 5%, then the result is `fail` (available 10,000.00 < 12,000.00, over by 20%).
- **AC-38.2** (FR-38.4) Given the same balance, when a PO of 10,400.00 is checked, then the result is `warn` (over by 4%) and 10,400.00 is reserved.
- **AC-38.3** (FR-38.5) Given a PO line of 10 × 50.00 encumbered, when an invoice for 10 × 52.00 posts, then relief = 500.00 and a new check runs for 20.00.
- **AC-38.4** (FR-38.4) Given two concurrent POs of 8,000.00 against 10,000.00 available, when checked simultaneously, then exactly one passes.
- **AC-38.5** (FR-38.7) Given a reciprocal step with 3 service centers serving each other, when run, then Σ out = Σ in to the cent and the solution matches the analytic solve.
- **AC-38.6** (FR-38.11) Given a month of journal postings, when reconciliation runs, then Σ primary cost entries per element equal the journal balances with 0.00 difference.
- **AC-38.7** (FR-38.2) Given a plan with 3 scenarios and 2 stages, when approvers act, then the workflow completes and a register entry posts from the selected scenario.
- **AC-38.8** (FR-38.6) Given a journal entry posted, when 5 s elapse, then matching cost entries exist for every line with a primary element mapping.
- **AC-38.9** (FR-38.13) Given an exported worksheet whose protected range was edited offline, when uploaded, then the upload is rejected with CO-0626 and no cell is written.
- **AC-38.10** (FR-38.14) Given GL actuals of FY2025 of USD 1,200,000.00 for a department and a 3% uplift, when the plan generates for FY2026 in Replace mode, then plan lines total 1,236,000.00 with source references to the actuals.
- **AC-38.11** (FR-38.19) Given freight of USD 12,000.00 and revenue shares 300,000 / 150,000 / 50,000, when top-down distribution runs, then the segments receive 7,200.00 / 3,600.00 / 1,200.00 and Σ = 12,000.00.
- **AC-38.12** (FR-38.20) Given a funds reservation of EUR 20,000.00, when a PO of EUR 8,000.00 references it, then the reservation shows 12,000.00 remaining and an encumbrance of 8,000.00 exists, with available funds unchanged by the conversion.
- **AC-38.13** (FR-38.21) Given a program with budget USD 5,000,000.00 and children distributed 4,800,000.00, when a user distributes 300,000.00 more, then the distribution is rejected with CO-0641.
- **AC-38.14** (FR-38.17) Given a template line `25.00 × inspectionLots + 0.005 × materialCost` and an order with 4 lots and material cost 20,000.00, when run, then the receiver gets 200.00 and the sender is credited 200.00.

### 7.38.9 Test plan and fixtures

Fixtures: `budget-check-200` (200 PO/invoice scenarios across thresholds, overrides, revisions, cycles); `alloc-reciprocal-10` (service centers with circular references and a 2,500-sender case for the iterative path); `plan-3-scenarios` (demo entity planning process); `costledger-month` (one month of journal postings mapped to elements).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Check order of operations | Unit | budget-svc | each step observable in result trace |
| Relief exactness | Property | encumbrance | Σ reserved − relieved ≥ 0, never negative; relief ≤ matched share |
| Concurrent checks | E2E | API | no double pass |
| Allocation conservation | Property | costacct-svc | Σ in = Σ out for random matrices |
| Convergence | Unit | iterative solver | residual ≤ 0.01 within 100 iterations for diagonally dominant cases |
| Check latency | Performance | API | p95 < 150 ms at 50 checks/s |
| Allocation throughput | Performance | run | 1M cost entries, 5 steps < 10 min |

Reconciliation checks: cost ledger primary costs = journal by element/period; Σ encumbrances = Σ open PO lines under control; budget balances = Σ posted register entries.

### 7.38.10 Deployment and operations

Helm charts `budget-svc`, `costacct-svc` (shared Postgres `controlling`); env `BUDGET_CHECK_TIMEOUT_MS=500`, `ALLOC_MAX_ITER=100`, `ALLOC_EXACT_SOLVE_MAX_N=2000`; flags `budget.unposted-in-available`, `controlling.abc`; jobs: `cost-mirror-lag-check` every 5 min, `cost-gl-reconcile` nightly, `allocation-schedule` per policy, `budget-balance-rebuild` on demand. SLOs: check p95 < 150 ms, mirror lag p95 < 5 s, allocation 1M entries < 10 min. Alerts: mirror lag > 60 s, reconciliation difference ≠ 0, check timeouts > 0.1%. Rollback: Helm; balance cache rebuilt from events if corrupted.

### 7.38.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| BudgetTransactionHeader/Line (current and next cycle) | BudgetRegisterEntry | posted entries summarized per combination/period |
| BudgetControlConfiguration, BudgetControlRule, BudgetGroup | BudgetControlRule | re-validated; user groups mapped to Keycloak groups |
| BudgetSourceTracking (open) | Encumbrance | open POs only, tied to migrated POs (wave 17) |
| CAMCostObject, CAMCostElement, CAMDimensionHierarchy | CostObject, CostElement, DimensionHierarchy | |
| CSKS, CSKT, SETHEADER/SETNODE (KSH1) | CostObject, DimensionHierarchy | |
| CSKA/CSKB | CostElement | primary mapped by main account |
| T811C/T811S/T811K cycles | AllocationPolicy | segment → step; tracing factors → basis |
| FMAVCT, COOI, FMIOI | Encumbrance | open commitments only |
| COSP/COSS plan versions, KP06 | BudgetRegisterEntry (Original) | current year plan |
| COEP history | not migrated | cost entries mirrored from migrated journal balances |

Validation: Σ migrated budget = legacy budget report per combination; open commitments count and total match. Not migrated: closed-cycle budget history older than 2 years (lakehouse archive), AX budget plan Excel templates.

### 7.38.12 Dependencies, risks, open questions

Depends on: wave 11 dimensions and balances, wave 14 fiscal calendars and allocation engine (reused for GL), wave 17 requisitions/POs, wave 18 invoices, wave 21 activity quantities, wave 37 depreciation forecasts.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Balance cache drift vs journal | Medium | High | Nightly reconcile; rebuild job; alert |
| Procurement UX slowed by checks | Medium | Medium | SLO; async pre-check on requisition |

Open questions: (1) Include unposted documents in available funds by default? Proposed off; decide by wave 38 start. (2) Reciprocal exact solve threshold 2,000? Proposed yes; revisit at phase exit. (3) Internal orders as cost object type or separate module? Proposed cost object type; decide by wave 38.

### 7.38.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `budget.registerNumberSequence` | legal entity | sequence | BR-###### | sequence id | register entry numbers | Budgeting parameters → Number sequences | FMBB document number ranges (FM area) |
| `budget.transferRules` | legal entity | table | net zero only | dimension constraints for transfers | FR-38.1 transfer validation | Budgeting parameters → "Use budget transfer rules", Budget transfer rules | FMBB transfer document types, budget structure (FMMD) |
| `budget.cycleTimeSpan` | legal entity | enum | FiscalYear | FiscalYear / Quarter / Period / Custom | time span for control (FR-38.3) | Budget cycle time spans | FM budget profile (annual / overall) |
| `budget.controlDimensions` | rule version | dimension[] | MainAccount + Department | subset of ledger dimensions | control combination (FR-38.4) | Budget control configuration → Define parameters → budget control dimensions | FMDERIVE derivation, FM account assignment elements (fund, funds center, commitment item) |
| `budget.fundsAvailableFormula` | rule version | flags | budget − actuals − encumbrances − pre-encumbrances | include unposted / include revenue | available formula | Budget funds available calculation | AVC control ledger FMAVCT, AVC profiles |
| `budget.thresholds` | rule version | struct | warn 0% / block 0% | 0–100% each | FR-38.4 step 6 | Budget control rules → thresholds | FM availability control tolerance profiles |
| `budget.overrideGroups` | rule version | group[] | none | Keycloak groups | fail → warn (FR-38.4 step 7) | BudgetControlUserGroup | FM tolerance profile per user (activity group) |
| `budget.documentsUnderControl` | rule version | enum[] | Requisition, PO, VendorInvoice | Requisition / PO / VendorInvoice / Journal / Expense / Project / Reservation | FR-38.3 | Budget control configuration → Select source documents | FM update profile, commitment-relevant documents |
| `budget.carryForward` | legal entity | enum | Off | Off / OpenEncumbrances / OpenEncumbrancesAndUnusedBudget | year-end FR-38.5 / BR-38.10 | Budgeting → Year-end encumbrance process (D365) | FMJ2 / FMJ1 commitment carry-forward, FMCB budget carry-forward |
| `plan.worksheetMaxCells` | tenant | int | 200,000 | 1,000–1,000,000 | FR-38.13 upload limit | Excel budget plan layout limits | KP06 planner profile (KP34) layout |
| `plan.generationMode` | planning process | enum | UpdateIfUnchanged | Replace / Add / UpdateIfUnchanged | FR-38.14 | Generate budget plan from … → "Replace / Add" | KP97 copy planning (overwrite / reset) |
| `position.costElements` | legal entity | table | Salary, Benefits 20%, Payroll tax 7.65% capped | element, formula, ceiling | FR-38.15 | Budget cost elements (HcmBudgetPurposeType) | PCP cost items / cost planning variants |
| `reservation.documentTypes` | legal entity | table | 4 types seeded | type, number sequence, consumable | FR-38.20 | Budget reservation types (PSN) | FMEF earmarked funds document types, number ranges |
| `program.types` | tenant | table | Capex (6 levels) | levels, budget categories | FR-38.21 | none | IM program types (TAIF), budget categories, plan / budget profiles |
| `ledger.costAccountingLedger` | legal entity | struct | one per entity | currency, fiscal calendar, chart of cost elements | FR-38.6 | Cost accounting ledger (CAMCostAccountingLedger) | OKKP controlling area |
| `ledger.versions` | ledger | table | Actual (0), Plan (1) | version code, type, locked periods | plan / actual separation | Cost accounting versions (CAMVersion) | OKEQ CO versions |
| `ledger.periodLock` | ledger | table | none | period × activity (allocation, entry) | blocks runs in closed periods | Cost accounting → Ledger period status | OKP1 period lock |
| `ledger.elementMapping` | ledger | rules | main-account range → primary element | ranges | journal mirroring (FR-38.6) | Cost element dimension members from main accounts (CAMCostElementDimension) | KA01 primary / automatic creation of cost elements (S/4 GL account type "primary costs") |
| `ledger.defaultCostObject` | ledger | rules | none | main account → cost object | default assignment for journal lines without object | Cost accounting → default cost objects | OKB9 default account assignment |
| `alloc.exactSolveMaxN` | ledger | int | 2,000 | 100–10,000 | FR-38.7 solver choice | none | KSU5 iteration (no exact solve) |
| `alloc.maxIterations` | ledger | int | 100 | 10–1,000 | FR-38.7 | none | cycle iteration flag (KSU1 / KSV1) |
| `alloc.cycles` | ledger | policy | none | steps, sender / receiver filters, basis | FR-38.7 | Cost allocation / distribution policies (CAMCostAllocationPolicy) | KSU1 assessment cycles, KSV1 distribution cycles (T811C) |
| `alloc.templateEnvironment` | ledger | enum | Controlling | Controlling / CostObject / Segment | which attributes formulas may reference (FR-38.17) | none | CPT1 template environments |
| `policy.costBehavior` | ledger | policy | element default | rules / regression | FR-38.16 | Cost behavior policies | cost center categories (OKA2), fixed / variable plan split (KP06 layouts) |
| `policy.overheadRate` | ledger | policy | none | rate tables | FR-38.16 | Overhead rate policies | KZS2 costing sheet on cost centers / orders, KZZ2 overhead rates |
| `abc.activityTypes` | ledger | table | none | activity type, unit, planned rate | FR-38.8 | Statistical dimension / cost behavior templates | KL01 activity types, KP26 activity prices |
| `fund.dimensions` | legal entity | bool | false | true / false | seeds `Fund` and `FundedProgram` dimensions and adds them to control dimensions | Financial dimensions (Fund, PSN) | FM5I funds, FMMEASURE funded programs |

### 7.38.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Budget control statistics | drill-down | rule, combination, time span | budget, actuals, encumbrances, pre-encumbrances, reservations, available, % used | `budget_control_balance` | Budget control statistics (BudgetControlStatistics) | FMAVCR01 / FMAVCR02 |
| Budget vs actual vs committed | operational report | entity, hierarchy node, period range, version | plan, actual, committed, variance, variance % | cost ledger + budget balance | Budget control statistics by period, Actual vs budget (D365 report) | S_ALR_87013611, FMRP_RFFMEP1AX |
| Budget register entries | list inquiry | model, type, status, date | entry, type, lines total, workflow status | `budget_register_entry` | Budget register entries (BudgetTransactionHeader) | FMBB document display, FMEDDW |
| Open encumbrances | list inquiry | entity, source type, age | document, reserved, relieved, remaining, status | `encumbrance` | Encumbrance inquiry / BudgetSourceTracking | KSB2 commitments, FMRP_RFFMEP1OX |
| Earmarked funds list | list inquiry | type, status, expiry | document, amount, consumed, remaining, expiry | `budget_reservation` | Budget reservation list (PSN) | S_P99_41000099 (earmarked funds journal) |
| Cost object line items | drill-down | cost object, element, period, origin | entry, amount, origin, source document | `cost_entry` | Cost object statement (CAMCostObjectStatement) | KSB1, KOB1 |
| Allocation run log | operational report | policy / template, period, version | step, senders, receivers, in, out, residual, iterations | `allocation_run` | Overhead calculation results | KSU5 / KSV5 / CPTA logs |
| Plan scenario comparison | drill-down | plan, scenarios (≤ 3), dimension rows | scenario values, deltas, % | `budget_plan_line` | Budget plan comparison | KP06 version comparison, S_ALR_87013612 |
| Activity price and quantity | operational report | activity type, period | planned rate, actual rate, quantity, rate variance | `activity_type` | none | KSBT activity prices, KSII actual price calculation |
| Investment program overview | KPI / drill-down | program, year, position level | budget, distributed, plan, actual, commitments, available | `program_position` | none | IM information system program reports |
| Position forecast cost | operational report | department, scenario, year | positions, FTE, salary, benefits, tax, total by month | `position_forecast` | Position forecasting reports | PCP cost plan reports |
| Cost behavior classification | list inquiry | ledger, element, object type | fixed, variable, unclassified amounts, policy rule applied | `cost_entry` | Cost behavior inquiry | KSB1 by cost element category |

### 7.38.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Budget plan worksheet | export (FR-38.13) | header sheet (plan, stage, template version), data sheet, hidden checksum sheet | xlsx | n/a | none |
| Budget register entry approval record | workflow completion | entry, lines, approvers, timestamps | PDF | register entry number | 7 years (internal control evidence, SOX 404 where applicable) |
| Budget transfer advice | transfer posted | from / to combinations, amount, reason | PDF, email to budget owners | register entry number | 7 years |
| Earmarked funds document | create / change | type, lines, expiry, approver | PDF | per-type sequence | 7 years (public-sector packs 10 years) |
| Cost center report | period close | plan / actual / variance per element | PDF, XLSX | none | none (management) |
| Allocation run statement | run completion | sender → receiver matrix, totals, residual | PDF, CSV | run id | 7 years (cost allocation audit trail; government contract cost accounting standards where applicable) |
| Investment program budget letter | distribution | position, budget, conditions | PDF | program number | 7 years |

### 7.38.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `cost-mirror-lag-check` | every 5 min | ledger | offsets per Kafka partition | < 10 s | alert only | lag > 60 s |
| `cost-gl-reconcile` | nightly 02:15 | (ledger, period) | (ledger, period, date) | < 5 min | report differences | difference ≠ 0 |
| `allocation-schedule` | per policy (default period close day 2) | (policy, period) | (policy, period, version); rerun reverses | < 10 min for 1M entries | rollback run; retry | non-convergence / residual > 0.01 |
| `template-allocation` | per template | (template, period) | as above | < 5 min | as above | formula errors |
| `hierarchy-distribution` | period end after allocations | (hierarchy, period) | (node, period) | < 5 min | residual left on node | residual > 0 |
| `budget-balance-rebuild` | on demand | rule version | event replay offset | < 10 min for 1M documents | old cache serves until swap | drift detected |
| `encumbrance-carry-forward` | fiscal year end (close checklist) | legal entity | (entity, year) | < 10 min for 100k encumbrances | atomic per combination | items failed |
| `reservation-expiry` | daily 01:00 | legal entity | (reservation, date) | < 1 min | retry | expiring in 7 days |
| `program-carry-forward` | approval year end | program | (position, year) | < 2 min | per-position isolation | skipped positions |
| `position-forecast-refresh` | nightly 03:30 (when wave 42 active) | legal entity | snapshot version | < 5 min | keep previous | source failure |

### 7.38.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| BudgetView | GET /budget/*, budgeting workspace, 7.38.14 budget reports | none | Budget owner, Controller |
| BudgetMaintain | /register-entries, submit | BudgetApprove (same entry) | Budget analyst |
| BudgetApprove | register entry / transfer workflow approve | BudgetMaintain | Budget manager, CFO |
| BudgetPlan | /plans/*, worksheets, generate | BudgetApprove at the same stage | Budget preparer |
| BudgetConfigure | control rules, thresholds, override groups, worksheet templates | BudgetOverride, BudgetMaintain | Finance system admin |
| BudgetOverride | fail → warn on check | BudgetConfigure; ProcurementPurchaseOrderApprove for own POs | Budget manager |
| BudgetReserve | /reservations | BudgetApprove of the same reservation | Budget analyst |
| InvestmentProgramMaintain | /investment-programs, distribute, carry-forward | CapexRequestApprove (37) on assigned requests | Capex controller |
| CostAccountingView | GET /controlling/*, cost control workspace | none | Controller, Department head |
| CostAccountingMaintain | cost objects, elements, hierarchies, measures, policies | CostAccountingRun | Cost accountant |
| CostAccountingRun | allocation, template, top-down, hierarchy, settlement runs | CostAccountingMaintain (policy author cannot run the period) | Cost accountant lead |

### 7.38.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| CO-0601 | error | Budget check failed: available `{available}`, requested `{amount}` | over block threshold | reduce amount or request override | FR-38.4 |
| CO-0602 | warning | Budget exceeded by `{pct}` within tolerance | between warn and block | none or request override | FR-38.4 |
| CO-0603 | error | No budget cycle contains `{date}` | NoBudgetCycle | open cycle / change date | BR-38.5 |
| CO-0604 | error | Transfer lines do not net to zero (difference `{amount}`) | unbalanced transfer | correct lines | FR-38.1 |
| CO-0605 | info | Budget check overridden by `{user}` | override group | none | FR-38.4 |
| CO-0608 | error | Budget check timed out | check > 500 ms | retry; SRE alert | FR-38.4 |
| CO-0611 | error | Allocation did not converge: residual `{residual}` after `{n}` iterations | iterative solve | review reciprocal structure | BR-38.6 |
| CO-0612 | error | Allocation totals differ: in `{in}`, out `{out}` | conservation failure | run rolled back; report defect | BR-38.6 |
| CO-0613 | warning | Sender `{sender}` has zero basis; cost not allocated | no basis | load statistical measures | BR-38.8 |
| CO-0614 | error | Period `{period}` is locked for allocations | ledger period lock | unlock or run next period | 7.38.13 `ledger.periodLock` |
| CO-0615 | warning | Journal postings after the last allocation run for `{period}` | late postings | rerun allocation | BR-38.7 |
| CO-0618 | error | Settlement `{amount}` exceeds collected cost `{collected}` | over-settlement | reduce settlement | BR-38.9 |
| CO-0621 | error | Formula error in template `{template}` line `{n}`: `{detail}` | invalid expression | fix formula | FR-38.17 |
| CO-0622 | warning | Measure `{measure}` is zero for receiver `{receiver}` | division by zero | load measure or accept | BR-38.12 |
| CO-0626 | error | Worksheet protected range `{range}` was modified | checksum mismatch | export again | FR-38.13 |
| CO-0627 | warning | `{n}` cells conflicted with server changes and were not applied | concurrent edit | review conflict list | BR-38.11 |
| CO-0631 | error | Funds blocking `{doc}` cannot be consumed | FundsBlocking consumption | use a reservation type | BR-38.14 |
| CO-0632 | info | Reservation `{doc}` expired; `{amount}` released | expiry | none | BR-38.14 |
| CO-0641 | error | Distribution exceeds parent position budget by `{amount}` | BR-38.15 | reduce distribution or raise parent | BR-38.15 |
| CO-0645 | warning | Hierarchy node `{node}` keeps `{amount}` undistributed | no basis on children | review basis | FR-38.18 |

### 7.38.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Budget check | 20,000 checks/day; peak 50/s at month-end PO runs | 30 / 150 / 300 ms | 50 checks/s sustained | 7M check results ≈ 4 GB (7-year retention ≈ 28 GB) | BudgetControlBalance unique (rule_version, key, span); results partitioned monthly | row locks serialize hot combinations; p95 < 400 ms; timeouts fail open to `warn` only if rule sets `fail-open` (default fail closed) |
| Available funds query | 10,000/day | 20 / 80 / 150 ms | 100 req/s | n/a | same | cached 5 s |
| Cost entry mirroring | 2M journal lines/day | lag 1 / 5 / 15 s | 500 lines/s | 730M entries ≈ 180 GB | CostEntry partitioned by period_key; index (cost_object_id, cost_element_id, period_key) | lag up to 60 s; alert |
| Allocation run | 1M entries, 5 steps | n/a | < 10 min | allocation entries ≈ 20% of mirrored volume | per-run batch insert, COPY | < 30 min |
| Template / top-down run | 200k receivers | n/a | < 5 min | small | receivers streamed by partition | < 15 min |
| Plan grid load | 500,000 lines per plan; 50 concurrent planners | 300 ms / 1.2 s / 2.5 s per 10k-row page | 50 concurrent users | 5M plan lines ≈ 3 GB | plan lines by (plan_id, scenario_id, combination) | page size drops to 2k rows |
| Worksheet import | 200 uploads/day near deadline | 5 / 20 / 40 s for 200k cells | 10 concurrent | xlsx stored 1 year ≈ 20 GB | n/a | uploads queue, status polled |
| Budget balance rebuild | 1M documents | n/a | < 10 min | n/a | replay by rule version | < 30 min, old cache serves |

### 7.38.20 Localization and compliance hooks

- **Public-sector budgeting**: US federal (USSGL, appropriation / apportionment / allotment, Treasury Account Symbol) and state / local GASB 34 fund accounting are out of v1; the `fund.dimensions` parameter and earmarked funds (FR-38.20) cover nonprofit restricted funds (ASC 958) and EU grant tracking. Public-sector packs (post-v1) extend through `BudgetStructureProvider`.
- **Government contract cost accounting**: US CAS (FAR Part 30) and DCAA require documented, consistently applied allocation bases with an audit trail; allocation run statements (7.38.15) and versioned policies satisfy the trail; indirect rate (fringe, overhead, G&A) pools are configured as assessment steps.
- **Statutory cost accounting**: FR comptabilité analytique is not statutory; DE Betriebsabrechnungsbogen (BAB) is a report layout over the cost ledger (wave-48 DE pack template).
- **Personnel cost planning**: payroll tax and social security ceilings in FR-38.15 cost elements are country data from wave-43 payroll packs once available; privacy: position forecasts with named workers are personal data and use wave-42 HR visibility rules.
- **Retention**: budget check results and approval records 7 years; plan worksheets 1 year online then archive.
- **Fiscal calendars**: 4-4-5 and 13-period calendars from wave 14 drive time spans and allocation periods.

### 7.38.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.4.1 Budget plan → Budget plan templates / worksheets (BudgetPlanTemplate) | PARTIAL-38 | ADDED as FR-38.13 | |
| AX 12.4.3 Budget planning → Generate budget plan from source (GL actuals, previous plan, FA, projects, demand forecast, HR positions) | PARTIAL-38 | ADDED as FR-38.14 | project source activates with wave 41 |
| AX 12.4.4 Budget planning → Budget planning templates (Excel) | PARTIAL-38 | ADDED as FR-38.13 | |
| AX 12.4.4 Budget planning → Position forecasting (D365 HR-driven) | PARTIAL-38 | ADDED as FR-38.15 | manual / CSV positions in wave 38; HR-driven source when wave 42 ships |
| AX 12.7.1 Cost behavior / Cost classification (CAMCostBehavior) | PARTIAL-38 | ADDED as FR-38.16 | |
| AX 12.7.4 Policies — cost behavior, distribution, overhead rate, allocation, rollup | PARTIAL-38 | ADDED as FR-38.16 | distribution and allocation policies already FR-38.7 |
| SAP 13.7 CPTA / CPTB ABC template allocation | PARTIAL-38 | ADDED as FR-38.17 | |
| SAP 13.7 ABC template allocation environments, functions (CPT1) | PARTIAL-38 | ADDED as FR-38.17 | `alloc.templateEnvironment` |
| SAP 13.8 KKPHIE / KKP2 Cost object hierarchy | PARTIAL-38 | ADDED as FR-38.18 | |
| SAP 13.9 KE28 Top-down distribution (actual) | PARTIAL-38 | ADDED as FR-38.19 | |
| SAP 13.11 FM5I / FM5S Fund | PARTIAL-38 | SUPERSEDED by wave-11 financial dimension `Fund` seeded by `fund.dimensions` and usable as a budget control dimension (FR-38.3) | appropriation-based fund accounting out of v1 |
| SAP 13.11 FMMEASURE Funded program | PARTIAL-38 | SUPERSEDED by financial dimension `FundedProgram` (same mechanism) | |
| SAP 13.11 FMX1 / FMX2 / FMX3 Earmarked funds: funds reservation | PARTIAL-38 | ADDED as FR-38.20 | |
| SAP 13.11 FMY1 / FMZ1 / FMW1 / FMV1 Precommitment / commitment / revenue forecast / funds blocking | PARTIAL-38 | ADDED as FR-38.20 | FMZ1 funds commitment maps to FundsReservation with consumption |
| SAP 13.11 Earmarked funds document types, number ranges (FMEF*) | PARTIAL-38 | ADDED as FR-38.20 | `reservation.documentTypes` |
| SAP 13.27 IM27 / IM32 / IM52 Investment program budget distribution, rollup, carry-forward (handed over from wave 37) | PARTIAL-37 | ADDED as FR-38.21 | see 7.37.21 |

## 7.39 Wave 39: Product Costing, Cost Roll-Up & Variance Analysis

### 7.39.1 Scope and business context

Wave 20 delivered the inventory costing engine with a standard cost method but no way to derive the standard: standards were keyed. Wave 39 computes them. Cost accountants define cost groups, costing sheets with indirect cost nodes, and costing versions; roll up costs from BOMs and routings per site and quantity with explosion modes and fallback rules; compare estimates; activate pending costs, which revalues on-hand inventory and posts the revaluation; and analyze purchase price and production variances decomposed into lot size, quantity, price and substitution by cost component. At the end of the wave the demo plant runs on standard cost with a monthly variance report.

Out of scope: actual costing / material ledger periodic unit price (wave 20 covers FIFO/weighted average), mixed costing across procurement alternatives, co-product cost allocation for process manufacturing beyond fixed percentage (wave 24 rules reused), sales-order costing for configured items.

**Scope amendment (gap closure).** The registers (coverage-ax §12.99 rows 50 and 54; coverage-sap §13.99 rows 89–101 mapped to wave 39; plus SAP row 90 mapped to wave 27 and AX rows 20–21 proposed for wave 20, whose notes point here) add standard cost conversion (FR-39.12), ad-hoc cost estimates with costing models (FR-39.13), mixed costing over procurement alternatives (FR-39.14), COGS and price-difference posting by cost component (FR-39.15), sales order cost estimates for configured and make-to-order lines (FR-39.16), parallel valuation views (FR-39.17) and parallel inventory valuation ledgers (FR-39.18, flag-gated). These supersede the out-of-scope notes above for mixed costing and sales-order costing. Material-ledger actual costing remains with wave 20.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX BOMCalcTable "Calculate items" for a costing version runs single-threaded; 50k items take 6–10 h and lock InventItemPriceSim | Sequential explosion per item re-exploding shared subassemblies | Topological roll-up computes each node once; parallel by level | 50k items 8 h → < 15 min |
| CK40N costing run in SAP: selection, structure explosion, costing, analysis, marking, release as separate steps with logs in CKAPP; errors found at release | Multi-step batch with late validation | One run with upfront validation (missing prices, cycles, inactive routes) and per-item diagnostics | Rework runs per cycle 3 → 1 |
| Cost component split lost in AX when a subassembly rolls into its parent (only cost group breakdown if "cost breakdown" enabled, which doubles InventCostTrans volume) | Breakdown as optional duplicate storage | Split is the primary representation; parent carries child split natively | Items with full split 40% → 100% |
| Production variance report in AX shows lot size/quantity/price/substitution totals that do not sum to the posted variance when rounding occurs across InventCostTrans | Independent rounding per variance type | Decomposition with residual assignment rule; property-tested | Unexplained variance per order 0.00 |
| Standard cost activation (InventItemPrice activate / CK24 release) revalues inventory in a batch without preview; controllers discover the P&L hit after posting | No simulation of revaluation | Activation preview shows revaluation by item/site before commit | Unplanned revaluation surprises 0; preview < 60 s |

### 7.39.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Costing versions | CostingVersion (type Standard cost / Planned cost), blocking, fallback principle, InventItemPrice, InventItemPriceSim | Costing variant OKKN, valuation variant, costing version (OKKN), KEKO header | CostingVersion with status, fallback, site scope | full |
| Cost groups and categories | CostGroup (direct material / direct manufacturing / indirect / undefined; fixed/variable), CostCategory with cost category prices, BOMCalcGroup | Cost component structure OKTZ (TCKH1–3), cost elements per component, activity types/prices KP26 | CostGroup, CostComponent, CostCategory rate tables | full |
| Costing sheet | CostSheetNodeTable, CostSheetCalculationBasis, CostSheetCalculationFactor (surcharge/rate nodes, absorption basis) | Costing sheet (KZS2): base rows, overhead rows (percentage/quantity), credit rows; T683 | CostingSheet with Basis/Surcharge/Rate/Total nodes | full |
| BOM calculation | BOMCalcTable, BOMCalcTrans, BOMCalcItem, explosion mode (multilevel / single level / make-to-order / according to BOM line type), profit settings | CK11N single item, CK40N run, CKIS items, KEPH component split, CK13N display | CostCalculation with explosion mode, split retained | full |
| Mass roll and activation | Costing version "Calculate items", "Activate pending prices", InventSettlement cost change variance | CK40N marking (CK24 mark/release), MBEW STPRS update, MR21 | Mass roll (Temporal), activation with revaluation preview | improved |
| Purchase price variance | InventCostTrans variance type PurchPrice posted on receipt for standard cost items | Price difference account PRD on GR/IR (OBYC PRD) | VarianceEntry type PurchasePrice from wave 18 receipts/invoices | full |
| Production variances | ProdCalcTrans estimate, InventCostTransVariance (ProdLotSize, ProdQuantity, ProdPrice, ProdSubstitution) at ending | KKS1/KKS2 variance calculation categories (input price, input quantity, resource usage, remaining input, lot size, output price, remaining), CO88 settlement | Variance decomposition on production order end | full |
| Inventory cost revaluation | Cost change variance (InventSettlement), inventory value report | Price change document (MR21/CKMPRPUNMARK), ML revaluation | Revaluation journal posted on activation | full |
| Standard cost conversion | Standard cost conversion checklist (D365 InventStdCostConv, InventStdCostConvItem), "Convert to standard cost" | Price control change S → V / V → S on MBEW-VPRSV (MM02) with MR21 price change | ConversionRun with checklist (FR-39.12) | improved (preview, validation) |
| Ad-hoc cost estimate | none (Excel) | CKECP / CKECP1 easy cost planning, costing models (CKF*), CKUC unit costing | CostingModel and AdHocEstimate (FR-39.13) | full |
| Mixed costing | none (single source per costing version) | CK91N procurement alternatives, CK94 mixing ratios, OMXL quantity structure types | ProcurementAlternative with mixing ratios (FR-39.14) | full |
| COGS and PPV by component | Cost breakdown posting (InventCostTrans cost group detail), limited | S/4 COGS splitting (FINSC_COGS*), price-difference splitting | Component-split posting option (FR-39.15) | full |
| Sales order cost estimate | Item price for configured items (PCConfiguration → BOMCalcTable for order) | CK51N / CK53N sales order cost estimate, KKE1 base planning | CostCalculation type SalesOrder (FR-39.16) | full |
| Parallel valuation views | none | Legal / group / profit-center valuation (OKKP, TKA01 valuation views, transfer price variants) | Costing version valuation view (FR-39.17) | partial (no ML actual costing per view) |
| Global inventory accounting | Global inventory accounting add-in (D365 10.0.3x: inventory accounting ledgers, accounting conventions, costing policies) | Material ledger multiple currencies / valuations (CKMLCP per valuation view) | InventoryAccountingLedger per convention (FR-39.18) | partial (flag-gated; periodic settlement per ledger only) |

### 7.39.3 Functional requirements

- **FR-39.1 Cost groups and components** — Must. The system shall classify item costs, cost categories (labor/machine) and indirect nodes into cost groups with type and behavior, and map cost groups to cost components for the split.
  - Notes: ≤ 50 cost groups, ≤ 20 components per tenant; an item has exactly one cost group per site override.
- **FR-39.2 Costing sheet** — Must. The system shall define a costing sheet tree with Basis nodes (cost groups selected), Surcharge nodes (% on a basis), Rate nodes (amount per routing hour or per unit), and Total nodes, per site and costing version.
  - Notes: surcharge basis evaluates only the current level's direct lines unless the node is flagged `include-rolled-subassemblies`; absorption posts to indirect accounts via posting profiles at production end.
- **FR-39.3 Costing versions** — Must. The system shall maintain costing versions (Standard, Planned, Simulation) with blocking of pending prices, fallback principle (None / Active version / Specified version), site scope, and effective date.
- **FR-39.4 Cost roll-up algorithm** — Must. The system shall compute item costs by: (1) building the BOM graph for the selection and detecting cycles (reject with path); (2) ordering nodes topologically, leaves first, ≤ 20 levels; (3) for each node computing direct material = Σ child quantity × (1 + scrap%) × child cost (purchased: price from version, else fallback; manufactured: rolled value with its split); direct manufacturing = Σ operations (setup time / costing lot size + run time per unit) × cost category rate; indirect = costing sheet nodes evaluated on this level's basis; (4) retaining the cost component split of every child inside the parent (not collapsed); (5) storing the result as a pending price in the version with explanation lines.
  - Notes: worked 2-level example (USD). Subassembly SA-200: 3 × RM-301 at 2.50 = 7.50; 1 × RM-302 at 1.00 with 10% scrap = 1.10; machining 0.25 h × 60.00 = 15.00; sheet: material overhead 10% on direct material = 0.86; labor overhead 50% on direct labor = 7.50. SA-200 = 31.96 (DM 8.60, DL 15.00, IND-MAT 0.86, IND-LAB 7.50). Finished item FG-100: 2 × SA-200 = 63.92 carried as split (DM 17.20, DL 30.00, IND-MAT 1.72, IND-LAB 15.00); 1 × RM-300 at 4.00 → DM 4.00; assembly 0.5 h × 40.00 = 20.00 plus setup 1 h × 40.00 / costing lot size 10 = 4.00 → DL 24.00; material overhead 10% on own direct material purchased lines = 0.40; labor overhead 50% × 24.00 = 12.00. FG-100 = 104.32 with split DM 21.20, DL 54.00, IND-MAT 2.12, IND-LAB 27.00 (sums to 104.32). Explosion modes: multilevel (as above), single level (children at their active cost), make-to-order (explode only phantom and MTO lines), by BOM line type.
- **FR-39.5 Mass roll** — Must. The system shall roll all items in a version/site selection in parallel by topological level, with per-item diagnostics (missing price, inactive BOM/route, cycle), completing 50,000 items in < 15 minutes.
- **FR-39.6 Activation and revaluation** — Must. The system shall activate pending prices as of a date, compute on-hand revaluation = Σ on-hand quantity × (new − old standard) per item/site/dimension, show a preview, and post the revaluation journal (Dr/Cr Inventory vs cost change variance account).
  - Notes: activation is atomic per version/site; wave-20 standard cost engine switches at the activation timestamp; open production orders estimated at the old standard keep their estimate but variance at end uses the new standard for output (configurable).
- **FR-39.7 Purchase price variance** — Must. The system shall post PPV = (receipt unit cost − standard) × quantity at product receipt and adjust at invoice for standard-cost items, by cost component DM.
- **FR-39.8 Production variance decomposition** — Must. The system shall, when a production order ends, compute total variance = actual cost − standard cost of reported-as-finished (RAF) output, decomposed per cost component as: lot size variance LSV = Σ over fixed-per-lot elements (setup, constant scrap) of (standard per unit × Q_raf − estimated per lot ÷ Q_order × Q_raf); quantity variance QV = Σ_i (actual qty_i − standard qty_i for Q_raf) × standard unit cost_i for planned inputs i (inputs planned but not consumed give negative QV); price variance PV = Σ_i actual qty_i × (actual unit cost_i − standard unit cost_i) for inputs whose cost differs from standard (labor rate differences, components not on standard cost at that site, subcontract invoices); substitution variance SV = Σ_j actual qty_j × actual unit cost_j for inputs j not in the estimate. Identity: LSV + QV + PV + SV = total variance to the cent; residual from rounding assigned to QV of the largest planned line.
  - Notes: unfavorable is positive; output quantity variance for co-products handled by wave 24 percentages; variances post to GL by posting profile per type and component.
- **FR-39.9 Variance analysis workspace** — Must. The system shall present variances by order, item, site, period, type and component with drill to consumption and route transactions, and the monthly variance report.
- **FR-39.10 Cost estimate comparison and what-if** — Should. The system shall compare two versions or dates item-by-item by component, and allow a simulation version with overridden prices, rates and routes.
- **FR-39.11 Standard cost reconciliation** — Must. The system shall reconcile Σ on-hand quantity × active standard to GL inventory accounts per site nightly (extends the wave-20 inventory value report).
- **FR-39.12 Standard cost conversion** — Should. The system shall convert items from an actual costing method (FIFO, LIFO, weighted average) to standard cost at a conversion date through a checklist: select items and sites, roll pending standards into a conversion version, validate, preview the conversion revaluation, run inventory close up to the conversion date, and activate.
  - Notes: validation blocks conversion for items with negative physical on-hand, open in-transit transfers, or production orders started but not ended (option: allow, with variance at the new standard); conversion revaluation = Σ on-hand × standard − on-hand value at actual cost after the close, posted Dr / Cr Inventory vs cost change variance; after conversion the wave-20 engine stops settling issues for converted items; the checklist records each step's user and timestamp; reverse conversion (standard → actual) uses the same checklist with value carried at the last standard.
- **FR-39.13 Ad-hoc cost estimates (easy cost planning)** — Should. The system shall define costing models with characteristics (for example cavities, machine hours, material weight) whose formulas produce item, activity, subcontract and overhead lines, and compute estimates for non-material objects (quotes, internal orders, project tasks) with the costing sheet applied.
  - Notes: formula grammar shared with FR-38.17; prices resolved from the selected costing version with fallback; an estimate can be copied as plan to an internal order (wave 38), as a project estimate (wave 41), or as quote cost on a sales quotation (wave 27); estimates are versioned and comparable through FR-39.10.
- **FR-39.14 Mixed costing** — Should. The system shall maintain procurement alternatives per item and site (purchase from vendor, production version or BOM / route alternative, transfer from another site) with mixing ratios per costing version and compute the item's cost as the ratio-weighted cost of the alternatives with a weighted component split.
  - Notes: ratios must total 100.00% per item, site and version; each alternative is rolled with its own quantity structure; worked example: purchase from vendor A at 10.00 (60%) and in-house production at 12.50 (40%) → mixed cost = 0.6 × 10.00 + 0.4 × 12.50 = 11.00, and the split is weighted the same way (DM = 0.6 × 10.00 + 0.4 × production DM, other components 0.4 × production amounts); alternatives without a valid cost fail the item unless fallback applies; ratios can be derived from the last 12 months of receipts with `derive-ratios`.
- **FR-39.15 COGS and price-difference posting by cost component** — Should. The system shall, per site option, post cost of goods sold on sales issues (wave 28) to separate accounts per cost component from the item's active split, and split purchase price variances proportionally to the purchased item's own components.
  - Notes: worked example: 10 × FG-100 at 104.32 issued → Dr COGS-material 212.00, Dr COGS-labor 540.00, Dr COGS-material overhead 21.20, Dr COGS-labor overhead 270.00 / Cr Inventory 1,043.20; PPV split uses the ratio of each component in the standard of the purchased item (for example material 92%, freight surcharge 5%, duty 3%); rounding residual to the largest component; option is off by default so wave-28 postings are unchanged unless enabled.
- **FR-39.16 Sales order cost estimates** — Should. The system shall cost a configured or make-to-order sales order line using its order-specific BOM and route (wave 27 configurator output, wave 9 versions) at the order quantity, store the result as a calculation of type SalesOrder linked to the line, and show the margin.
  - Notes: margin = net line amount − estimated cost; the estimate recomputes on configuration change before confirmation and freezes at confirmation; option `use-order-estimate-as-standard` makes the MTO production order's variance compare against the order estimate instead of the item standard; estimates for lines with ≤ 6 levels return in < 2 s.
- **FR-39.17 Parallel valuation views** — Should. The system shall maintain costing versions per valuation view (Legal, Group, ProfitCenter): the Group view rolls intercompany-purchased components at the supplying entity's Legal-view cost (excluding intercompany markup), and the ProfitCenter view uses internal transfer prices between profit centers within an entity.
  - Notes: the Group view standard is stored alongside the Legal view per item and site; inventory receipts from IC partners (wave 40) store the group cost per receipt layer so that unrealized profit = Legal-view value − Group-view value of remaining stock; ProfitCenter view transfer prices come from a transfer-price table or cost-plus % per profit-center pair; only the Legal view posts to the statutory ledger, other views post to dedicated ledger layers (wave 12) when the flag `costing.valuation-views-post` is on.
- **FR-39.18 Parallel inventory valuation ledgers (global inventory accounting)** — Could. The system shall maintain inventory accounting ledgers per legal entity, each with an accounting convention (costing method per item group, currency, target ledger layer), and run the wave-20 costing engine per ledger on the same inventory transactions to produce per-ledger inventory values, COGS and settlements.
  - Notes: flag `costing.global-inventory-accounting`; example: IFRS layer FIFO, local GAAP layer weighted average, group layer standard (FR-39.17 Group view); each ledger has its own inventory close, value report and GL reconciliation; the primary ledger is the one wave 20 already posts, additional ledgers post adjustments into their layer only; capacity budget: 3 ledgers per entity in v1.

### 7.39.4 Data model

**CostingVersion**: id, legal_entity_id, code, type (Standard/Planned/Simulation), status (Open/Blocked/Active/Closed), fallback_principle, fallback_version_id, site_ids[], effective_from, activated_at.

**CostCalculation**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| costing_version_id | uuid | yes | FK |
| item_id, site_id | uuid | yes | |
| calc_quantity | numeric(19,4) | yes | costing lot size |
| explosion_mode | enum | yes | Multilevel/SingleLevel/MakeToOrder/ByLineType |
| bom_version_id, route_version_id | uuid | yes | wave 9 |
| unit_cost | numeric(19,6) | yes | 6-decimal unit cost |
| status | enum | yes | Pending/Active/Superseded/Error |
| diagnostics | jsonb | no | [{code, message, ref}] |
| run_id | uuid | no | mass roll |

**CostComponentSplit**

| Field | Type | Required | Notes |
|---|---|---|---|
| calculation_id | uuid | yes | FK, part of PK |
| component_id | uuid | yes | FK CostComponent, part of PK |
| level | enum | yes | Own/Rolled (child-carried) |
| amount | numeric(19,6) | yes | per calc_quantity |
| fixed_amount, variable_amount | numeric(19,6) | yes | behavior split |

**CostingSheetNode**: id, sheet_id, parent_id, type (Basis/Surcharge/Rate/Total), cost_group_ids[], percentage numeric(9,4), rate numeric(19,6), rate_unit (Hour/Unit), include_rolled bool, absorption_account_id, order.

**VarianceEntry**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| source_type, source_id | enum, uuid | yes | ProductionOrder/Receipt/Invoice/Activation |
| type | enum | yes | LotSize/Quantity/Price/Substitution/PurchasePrice/CostChange |
| component_id | uuid | yes | |
| item_id, site_id | uuid | yes | |
| amount | numeric(19,4) | yes | unfavorable positive |
| journal_entry_id | uuid | yes | |
| detail | jsonb | no | per input line actual/standard qty and cost |

**Entities added by FR-39.12–FR-39.18**: **ConversionRun** (id, legal_entity_id, site_ids[], item_ids[], conversion_date, costing_version_id, checklist jsonb [{step, status, user, at}], revaluation_journal_id, status Draft/Validated/Closed/Activated/Reversed); **CostingModel** (id, code, characteristics [{name, unit, default}], lines [{type Item/Activity/Subcontract/Overhead, ref, quantityFormula, priceFormula}], version) and **AdHocEstimate** (id, model_id, object_type Quote/InternalOrder/ProjectTask, object_id, characteristic_values jsonb, costing_version_id, total, split jsonb); **ProcurementAlternative** (id, item_id, site_id, costing_version_id, type Purchase/Production/Transfer, vendor_id or production_version_id or source_site_id, ratio numeric(7,4)); **CostCalculation** gains `calc_type` (Item/SalesOrder/AdHoc/Mixed) and `valuation_view` (Legal/Group/ProfitCenter), with the unique-Active partial index extended to (version, item, site, valuation_view); **InventoryAccountingLedger** (id, legal_entity_id, code, currency, ledger_layer, costing_policies [{itemGroupId, method}], primary bool).

Invariants: Σ CostComponentSplit.amount per calculation = unit_cost × calc_quantity (6 decimals); exactly one Active calculation per (version, item, site); Σ VarianceEntry per production order = actual − standard of RAF to the cent. Indexes: CostCalculation unique partial (version, item, site) where status = Active; VarianceEntry by (site_id, period, type).

### 7.39.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/product-costing/versions | Create version | Idempotency-Key | CostingMaintain |
| POST | /api/v1/product-costing/sheets | Create/version sheet | Idempotency-Key | CostingMaintain |
| POST | /api/v1/product-costing/calculations | Roll one item (explosion mode, qty) | (version, item, site, bom, route, qty) | CostingCalculate |
| POST | /api/v1/product-costing/versions/{id}/roll | Mass roll selection | runId | CostingCalculate |
| GET | /api/v1/product-costing/calculations/{id}/explain | Explanation tree with split | n/a | CostingView |
| POST | /api/v1/product-costing/versions/{id}/activate | Preview (dryRun=true) or activate | (version, date) | CostingActivate |
| GET | /api/v1/product-costing/variances?site=&period=&type= | Variances | n/a | CostingView |
| POST | /api/v1/product-costing/variances/compute | Compute for ended order (system) | productionOrderId+version | CostingSystem |
| GET | /api/v1/product-costing/compare?a=&b= | Version comparison | n/a | CostingView |
| POST | /api/v1/product-costing/conversions | Start standard cost conversion checklist | Idempotency-Key | CostingActivate |
| POST | /api/v1/product-costing/conversions/{id}/steps/{step} | Execute checklist step | (conversion, step) | CostingActivate |
| POST | /api/v1/product-costing/estimates | Ad-hoc estimate from costing model | Idempotency-Key | CostingCalculate |
| PUT | /api/v1/product-costing/procurement-alternatives/{itemId} | Set alternatives and ratios | version | CostingMaintain |
| POST | /api/v1/product-costing/sales-order-estimates | Cost a sales order line | (lineId, configVersion) | CostingCalculate (system for sales) |

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.costing.calculation.completed.v1` | item rolled | calculationId, itemId, siteId, unitCost, split[] | analytics |
| `dev.passage.costing.rollrun.completed.v1` | mass roll finished | runId, itemCount, errorCount, durationMs | workspace |
| `dev.passage.costing.standardcost.activated.v1` | activation | costingVersionId, itemCount, revaluationJournalId, totalRevaluation | inventory costing, production, budget |
| `dev.passage.costing.variance.posted.v1` | variance computed | sourceId, type totals, byComponent[], journalEntryId | costacct-svc, analytics |
| `dev.passage.costing.conversion.activated.v1` | conversion activated | conversionId, itemCount, revaluationJournalId, conversionDate | inventory costing (20), production (21) |
| `dev.passage.costing.salesorderestimate.completed.v1` | order line costed | lineId, calculationId, cost, margin, marginPct | sales (27), analytics |

### 7.39.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Costing workspace | Roll, activate, compare, diagnostics tile | version, pending count, errors, last activation | CostingView |
| Costing sheet designer | Add node, set basis/%/rate, validate | tree, cost groups, percentage, rate, absorption account | CostingMaintain |
| Calculation explain | Expand levels, view split, export | level, line, qty, unit cost, component amounts | CostingView |
| Activation preview | Filter item/site, approve | item, old, new, on-hand, revaluation | CostingActivate |
| Variance analysis | Pivot by type/component/order, drill | order, item, standard, actual, LSV, QV, PV, SV | CostingView |

Interaction rules: the explain tree lazy-loads levels; keyboard `e` expands all; activation preview requires an explicit confirmation of the total revaluation amount; variance pivot persists per user.

### 7.39.7 Business rules and edge cases

- **BR-39.1** Unit costs are stored with 6 decimals; postings round to the currency minor unit at the transaction level; split rounding residual goes to the DM component.
- **BR-39.2** Version state machine: Open → Blocked (no edits of pending) → Active (per site activation) → Closed; Simulation versions cannot activate.
- **BR-39.3** A cyclic BOM (item appears in its own explosion path) fails the calculation with the path listed; phantoms are exploded in place.
- **BR-39.4** Fallback: missing price with fallback Active uses the active standard; with None, the calculation errors; fallback use is recorded in diagnostics.
- **BR-39.5** Setup cost per unit = setup time × rate ÷ costing lot size; costing lot size defaults from the item's default order settings (wave 8) and can be overridden per calculation.
- **BR-39.6** Scrap: BOM line scrap % multiplies quantity; route operation scrap % compounds backward through preceding operations.
- **BR-39.7** Activation while a mass roll is running for the same version/site is rejected (advisory lock).
- **BR-39.8** Revaluation uses on-hand at the activation timestamp from the wave-16 projection; inventory transactions timestamped after activation use the new standard.
- **BR-39.9** Variance identity: if |LSV + QV + PV + SV − total| > 0.00 after residual assignment, the computation fails and the order end is blocked with an error.
- **BR-39.10** Ended orders reopened (wave 21) reverse their variance entries; re-ending recomputes.
- **BR-39.11** Standard cost conversion requires a completed wave-20 inventory close with end date = conversion date − 1 day; activation without it is rejected (CST-0731).
- **BR-39.12** Mixing ratios are validated per (item, site, version) to total 100.00%; a ratio of 0.00% keeps the alternative for reporting but excludes it from the roll.
- **BR-39.13** COGS component posting: Σ component lines = total COGS of the issue to the cent; residual from rounding goes to the material component.
- **BR-39.14** A sales order estimate freezes at line confirmation; later configuration changes create a new estimate version and mark the old one Superseded; production order variance uses the frozen version when `use-order-estimate-as-standard` is on.
- **BR-39.15** Group-view unit cost of an IC-purchased component equals the supplier entity's Legal-view cost at the receipt date converted at the receipt's rate; when the supplier has no Legal-view standard, the IC purchase price applies and the receipt is flagged `groupCostUnknown` for wave 40 elimination review.

### 7.39.8 Acceptance criteria

- **AC-39.1** (FR-39.4) Given the worked SA-200/FG-100 fixture, when rolled multilevel, then FG-100 = 104.32 with split DM 21.20 / DL 54.00 / IND-MAT 2.12 / IND-LAB 27.00.
- **AC-39.2** (FR-39.4) Given 50 multi-level fixture products, when rolled, then every component amount matches the reference workbook to the cent.
- **AC-39.3** (FR-39.5) Given 50,000 items across 8 levels, when mass rolled, then the run completes in < 15 min with diagnostics for every error.
- **AC-39.4** (FR-39.6) Given 1,000 units on hand at 10.00 and a new standard 10.50, when activated, then a journal Dr Inventory 500.00 / Cr Cost change variance 500.00 posts and the inventory value report ties.
- **AC-39.5** (FR-39.8) Given an order for 100 planned, 90 RAF, consuming 290 of a component standard 3/unit at 2.00 plus 5 units of an unplanned part at 1.50, with setup 40.00 per lot and costing lot size 100, then QV = (290 − 270) × 2.00 = 40.00, SV = 7.50, LSV = 90 × (0.40 − 0.40) = 0.00 and total = 47.50 (plus any labor PV), and the identity holds.
- **AC-39.6** (FR-39.8) Given 200 ended fixture orders, when variances compute, then Σ types = total for every order.
- **AC-39.7** (FR-39.7) Given a receipt of 10 at 5.20 for an item standard 5.00, when posted, then PPV = 2.00 posts to DM.
- **AC-39.8** (FR-39.3) Given a cyclic BOM, when rolled, then the calculation errors with the cycle path and the run continues for other items.
- **AC-39.9** (FR-39.11) Given month end, when reconciliation runs, then Σ on-hand × standard equals GL inventory per site.
- **AC-39.10** (FR-39.12) Given 500 units of an item at FIFO value 4,925.00 and a standard of 10.00, when conversion activates on 2026-11-01 after the October close, then a journal Dr Inventory 75.00 / Cr Cost change variance 75.00 posts and later issues use 10.00.
- **AC-39.11** (FR-39.14) Given alternatives vendor A 10.00 at 60% and production 12.50 at 40%, when rolled, then the mixed cost is 11.00 and the split totals 11.00.
- **AC-39.12** (FR-39.15) Given the component posting option and an issue of 10 × FG-100, when posted, then four COGS lines 212.00 / 540.00 / 21.20 / 270.00 post and total 1,043.20.
- **AC-39.13** (FR-39.16) Given a configured line priced at 2,400.00 whose configuration rolls to 1,860.00, when costed, then the estimate shows margin 540.00 (22.50%) and freezes on confirmation.
- **AC-39.14** (FR-39.17) Given a component bought from IC partner B at 120.00 whose Legal-view cost in B is 100.00, when the Group view rolls, then the parent's Group-view material component uses 100.00 and the Legal view uses 120.00.

### 7.39.9 Test plan and fixtures

Fixtures: `rollup-50` (50 products, up to 6 levels, phantoms, scrap, setup, reference workbook); `rollup-50k` (generated 50,000 items, 8 levels, for performance); `variance-200` (ended orders with each variance type, reference workbook); `activation-demo` (demo plant on-hand with old/new standards).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Node cost formulas | Unit | roll-up | worked example exact |
| Split conservation | Property | roll-up | Σ split = unit cost × qty for random BOMs |
| Cycle detection | Unit | graph | every planted cycle reported |
| Mass roll time | Performance | run | 50k items < 15 min on 4 workers |
| Activation revaluation | E2E | activation + journal | journal = preview; value report ties |
| Variance identity | Property | decomposition | LSV + QV + PV + SV = total for random orders |

Reconciliation checks: inventory at standard = GL inventory per site; Σ variance journals per period = Σ VarianceEntry; activation revaluation journal = Σ preview rows.

### 7.39.10 Deployment and operations

Helm chart `costing-svc` (extends wave 20 chart, Postgres `costing`); env `COSTING_MAX_LEVELS=20`, `COSTING_ROLL_WORKERS=4`; flags `costing.whatif`, `costing.variance-new-standard-for-open-orders`; jobs: `variance-compute` on production order end events, `standard-cost-reconcile` nightly, `monthly-variance-report` on period close. SLOs: single calculation p95 < 500 ms for ≤ 6 levels; explain API p95 < 300 ms; mass roll as above. Alerts: roll error rate > 2%, variance identity failures > 0, reconciliation difference ≠ 0. Rollback: Helm; activation reversal is a new activation of the prior version (reversing journal), never a delete.

### 7.39.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| CostingVersion, InventItemPrice (active standard) | CostingVersion, CostCalculation (Active, no explanation) | active standards loaded as calculations with status Active and diagnostics `Migrated` |
| CostGroup, CostCategory, CostSheetNodeTable/CalculationBasis/Factor | CostGroup, CostCategory, CostingSheet | |
| BOMCalcTable/BOMCalcTrans | not migrated | re-rolled from migrated BOMs; comparison report vs legacy |
| InventCostTransVariance history | lakehouse archive | |
| MBEW (STPRS, PEINH), KEKO/KEPH released estimates | CostCalculation, CostComponentSplit | KEPH components mapped to CostComponent |
| OKTZ cost component structure, KZS2 sheets, KP26 rates | CostComponent, CostingSheet, CostCategory rates | |
| COSS/CO88 settled variance history | lakehouse archive | |

Validation: re-rolled standard within 1% of legacy active standard for 95% of items (exceptions listed with explanation); Σ on-hand × migrated standard = migrated GL inventory. Not migrated: simulation versions, legacy calculation logs.

### 7.39.12 Dependencies, risks, open questions

Depends on: wave 9 BOM/route versions, wave 16 on-hand, wave 20 standard cost engine and inventory value report, wave 21 production order end and consumption, wave 24 co-product rules, wave 38 cost elements for absorption.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Legacy standards unreproducible | High | Medium | Load active standards as-is; re-roll comparison |
| Deep BOMs exceed memory in parallel roll | Low | Medium | Level batching; worker memory limit 2 GiB |

Open questions: (1) Surcharge basis includes rolled subassemblies by default? Proposed no (AX behavior); decide by wave 39 start. (2) Variance for open orders at new standard? Proposed flag off; decide by wave 39. (3) Store split at 6 or 4 decimals? Proposed 6; decide by wave 39.

### 7.39.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `costing.versionType` | costing version | enum | Standard | Standard / Planned / Simulation / Conversion | allowed operations (FR-39.3) | Costing versions → Costing type (Standard cost / Planned cost) | OKKN costing variant + OKKI costing type |
| `costing.valuationVariant` | costing version | struct | price strategy: version → active → purchase price | ordered price sources per item type | price lookup order for purchased items | Costing versions → Fallback principle, "Use purchase price" | OKK4 valuation variant (material price strategy) |
| `costing.fallback` | costing version | enum | Active | None / Active / SpecifiedVersion | BR-39.4 | Fallback principle | valuation variant strategy sequence |
| `costing.blockPending` | costing version | bool | false | true / false | blocks edits of pending prices (BR-39.2) | Costing version → Block pending prices | costing run marking allowance (CK24 "allow marking") |
| `costing.componentStructure` | tenant | table | DM, DL, IND-MAT, IND-LAB, SUB | ≤ 20 components, element / cost group assignment | split layout (FR-39.1) | Cost groups (CostGroup) and cost group types | OKTZ cost component structure (TCKH1–3) |
| `costing.sheet` | site × version | tree | none | Basis / Surcharge / Rate / Total nodes | FR-39.2 | Costing sheets (CostSheetNodeTable) | KZS2 costing sheet |
| `costing.overheadRates` | site × version × period | table | none | % or rate per node | surcharge / rate values | Costing sheet → Calculation factors | KZZ2 overhead rates |
| `costing.includeRolledSubassemblies` | site | bool | false | true / false | surcharge basis scope (FR-39.2) | Costing sheet node "Include in calculation basis" | costing sheet base row definition |
| `costing.maxLevels` | tenant | int | 20 | 1–40 | BR / env `COSTING_MAX_LEVELS` | BOM calculation "Maximum level" | costing variant explosion limit |
| `costing.lotSizeSource` | item | enum | DefaultOrderSettings | DefaultOrderSettings / Override | BR-39.5 costing lot size | Item → Default order settings → Standard order quantity | MARC-LOSGR costing lot size |
| `costing.postingProfile` | site | table | per variance type × component | accounts | variance and revaluation accounts | Posting profiles (Inventory posting → Production variances) | OBYC PRD / UMB / GBB account determination |
| `costing.varianceCategories` | tenant | enum[] | LotSize, Quantity, Price, Substitution | as listed + Distributed (FR-38.18) | categories computed | Production variances (production control parameters) | OKV1 variance keys, variance variants (input price / quantity / resource-usage / remaining / lot size / output price) |
| `costing.newStandardForOpenOrders` | site | bool | false | true / false | FR-39.6 open-order behavior | none | KKS1 target cost version 0 vs 1 |
| `costing.conversionBlockers` | legal entity | enum[] | NegativeOnHand, InTransit, StartedOrders | as listed | FR-39.12 validation | Standard cost conversion setup / checklist validations | MM02 price control change checks (open orders) |
| `costing.mixedCosting` | site | bool | false | true / false | FR-39.14 enabled | none | OMXL quantity structure types, procurement alternatives |
| `costing.mixingRatioSource` | site | enum | Manual | Manual / DeriveLast12Months | FR-39.14 ratio source | none | CK94 mixing ratios |
| `costing.cogsComponentPosting` | site | bool | false | true / false | FR-39.15 | Cost breakdown posting on inventory model group | FINSC_COGS splitting profile |
| `costing.ppvSplit` | site | bool | false | true / false | FR-39.15 PPV | none | price-difference splitting (S/4) |
| `costing.orderEstimateAsStandard` | site | bool | false | true / false | BR-39.14 | Item price for configured items | KKE1 / CK51N with sales-order controlling (strategy 20) |
| `costing.valuationViews` | legal entity | enum[] | Legal | Legal / Group / ProfitCenter | FR-39.17 | none | OKKP valuation views, transfer price variants (8KEM) |
| `costing.globalInventoryAccounting` | legal entity | struct | off | ledgers ≤ 3 with conventions | FR-39.18 | Global inventory accounting → Ledgers, Accounting conventions, Costing policies | material ledger valuation types / currency types (OMX2, OMX3) |
| `costing.rollWorkers` | tenant | int | 4 | 1–32 | parallelism (env `COSTING_ROLL_WORKERS`) | batch task count | CK40N parallel processing (server group) |

### 7.39.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Cost calculation explain | drill-down | version, item, site, view | level, line, qty, unit cost, component amounts, sheet nodes | `cost_calculation`, `cost_component_split` | BOM calculation results (BOMCalcTrans) | CK13N, KEKO / KEPH display |
| Mass roll diagnostics | operational report | run, error code | item, site, code, message, reference | `cost_calculation.diagnostics` | Calculation infolog | CK40N log, CKAPP |
| Pending vs active standard | list inquiry | version, site, threshold % | item, active, pending, delta, delta % | `cost_calculation` | Item price (InventItemPrice) pending vs active | S_P99_41000111 cost estimate comparison, CK40N analysis |
| Activation revaluation preview | operational report | version, site, date | item, on-hand, old, new, revaluation | activation preview | Activate pending prices (no preview) | CK24 / MR21 (no preview) |
| Production variance by order | drill-down | site, period, item, type | order, standard, actual, LSV, QV, PV, SV, by component | `variance_entry` | Production variances (InventCostTransVariance) | KKS1 / KKBC_ORD |
| Purchase price variance | list inquiry | site, vendor, item, period | receipt, standard, actual, PPV, split | `variance_entry` type PurchasePrice | PPV inquiry (InventCostTrans) | MR11 GR/IR clearing log, FBL3N on the PRD price-difference account |
| Monthly variance report | KPI | site, period | variance by type / component, % of standard production cost | `variance_entry` aggregated | Production variance report (SSRS) | KKS1 summary log, CO-PC information system summarized order analysis |
| Inventory value at standard | statutory report | site, date, view, ledger | item, qty, standard, value, GL balance, difference | wave-20 value report + `cost_calculation` | Inventory value report | MB5L, MB52 with value |
| Standard cost conversion status | operational report | conversion run | step, status, user, timestamp, revaluation | `conversion_run` | Standard cost conversion checklist | MM02 change documents |
| Mixed cost composition | drill-down | item, site, version | alternative, ratio, cost, contribution, split | `procurement_alternative` | none | CK91N / CK94 display |
| Sales order margin | list inquiry | order, customer, margin % band | line, price, estimate, margin, frozen | `cost_calculation` type SalesOrder | Configured item price | CK53N, KKE3 |
| Valuation view comparison | drill-down | item, site, views | Legal, Group, ProfitCenter cost and component deltas | `cost_calculation` by view | none | CKM3 price analysis per valuation view |

### 7.39.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Cost estimate sheet | explain export | item, version, quantity, levels, split, sheet nodes | PDF, XLSX | calculation id | supporting evidence for inventory valuation; 10 years |
| Activation revaluation report | activation | item, site, old / new standard, on-hand, revaluation, journal id | PDF, CSV | activation id | 10 years (inventory valuation audit) |
| Standard cost conversion record | conversion activated | checklist steps with users and timestamps, revaluation | PDF | conversion run id | 10 years |
| Monthly variance report | period close | variance by type / component / order | PDF, XLSX | none | management; 7 years |
| Sales order cost estimate | line confirmation | configuration summary, cost by component, margin | PDF (internal) | calculation id | 7 years (pricing evidence) |
| Transfer-price evidence (cost basis) | period end | Group vs Legal view cost per IC item | CSV feeding FR-40.4 export | none | transfer-pricing documentation 10 years |

### 7.39.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `mass-roll` | on demand; typical yearly + quarterly | (version, site, level) | per-level checkpoint; item results upserted | < 15 min for 50k items | per-item error, run continues | error rate > 2% |
| `variance-compute` | on production order end event | production order | (order, version) | < 2 s per order | retries 3×; order end blocked on identity failure | identity failures > 0 |
| `standard-cost-reconcile` | nightly 02:45 | site | (site, date) | < 5 min | report | difference ≠ 0 |
| `monthly-variance-report` | period close | site | (site, period) | < 2 min | rerun | not generated by close day 3 |
| `mixing-ratio-derive` | quarterly | site | (site, quarter) | < 3 min | keep previous ratios | ratio change > 20 pts |
| `valuation-view-roll` | after each Legal mass roll | (version, site, view) | per-level checkpoint | < 15 min | view marked stale | stale views at activation |
| `gia-ledger-settlement` | per inventory close | (entity, ledger) | (ledger, close date) | < 30 min per ledger for 1M transactions | per-ledger isolation | ledger close failed |
| `sales-order-estimate-refresh` | on configuration change event | sales line | (line, config version) | < 2 s | warning on line | estimate missing at confirmation |

### 7.39.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| CostingView | GET /costing/*, explain, variances, compare | none | Cost accountant, Controller, Plant manager |
| CostingMaintain | versions, sheets, cost groups, components, alternatives, models | CostingActivate | Cost accountant |
| CostingCalculate | /calculations, /roll, /estimates, /sales-order-estimates | none | Cost accountant, sales service account |
| CostingActivate | /activate, /conversions | CostingMaintain (author of the pending prices cannot activate them) | Controller |
| CostingSystem | /variances/compute | n/a (service account only) | costing-svc worker |
| CostingConfigureLedgers | valuation views, inventory accounting ledgers, posting profiles | CostingActivate | Finance system admin |

### 7.39.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| CST-0701 | error | Cycle detected: `{path}` | cyclic BOM | fix BOM | BR-39.3 |
| CST-0702 | error | No price for `{item}` in version `{version}` and fallback None | missing price | add price or set fallback | BR-39.4 |
| CST-0703 | warning | Fallback price used for `{item}` from `{source}` | fallback | review | BR-39.4 |
| CST-0704 | error | BOM depth `{n}` exceeds limit `{max}` | > 20 levels | restructure or raise limit | FR-39.4 |
| CST-0705 | error | Route `{route}` inactive for `{site}` at `{date}` | invalid route | approve route version | FR-39.5 |
| CST-0710 | error | Version `{version}` is being rolled; activation blocked | advisory lock | wait | BR-39.7 |
| CST-0711 | error | Simulation versions cannot be activated | BR-39.2 | copy to Standard | BR-39.2 |
| CST-0712 | info | Activation will revalue inventory by `{amount}` | preview | confirm amount | FR-39.6 |
| CST-0720 | error | Variance identity failed for order `{order}`: residual `{r}` | rounding / data defect | order end blocked; contact support | BR-39.9 |
| CST-0721 | warning | Order `{order}` reopened; variances reversed | BR-39.10 | re-end order | BR-39.10 |
| CST-0731 | error | Inventory close through `{date}` required before conversion | BR-39.11 | run inventory close | BR-39.11 |
| CST-0732 | error | `{n}` items have negative on-hand; conversion blocked | conversion blocker | correct on-hand | FR-39.12 |
| CST-0741 | error | Mixing ratios for `{item}` total `{pct}` | ≠ 100.00% | correct ratios | BR-39.12 |
| CST-0751 | warning | Sales order estimate superseded by configuration change | BR-39.14 | none | BR-39.14 |
| CST-0761 | warning | Group cost unknown for IC receipt `{receipt}`; IC price used | BR-39.15 | roll supplier Legal view | BR-39.15 |
| CST-0771 | error | Inventory accounting ledger `{ledger}` has no costing policy for item group `{group}` | GIA setup gap | add policy | FR-39.18 |

### 7.39.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Single calculation | 2,000/day interactive | 150 / 500 / 1,000 ms (≤ 6 levels) | 20/s | ≈ 1 KB per calculation + split | unique partial (version, item, site, view) where Active | p95 < 1.5 s |
| Mass roll | 50k items × 2 views, 4×/year | n/a | 50k items < 15 min per view | 400k calculations × 3 KB ≈ 1.2 GB | level batches; worker memory 2 GiB | workers scale to 8; < 30 min |
| Explain API | 500/day | 80 / 300 / 600 ms | 10/s | n/a | lazy load per level | page levels |
| Variance compute | 3,000 orders ended/day; peak 300/h | 400 ms / 2 s / 4 s per order | 5 orders/s | 1M variance entries ≈ 0.6 GB | VarianceEntry by (site_id, period, type) | queue; order end confirmed async |
| Sales order estimate | 5,000 configured lines/day | 400 ms / 2 s / 4 s | 10/s | 5,000 × 365 × 3 KB ≈ 5.5 GB | by (sales_line_id) | estimates queue; confirmation waits ≤ 10 s |
| COGS component posting | 200k issues/day | adds ≤ 10 ms per issue | 300 issues/s | 4× COGS lines in journal ≈ +40 GB | journal partitions (wave 11) | posting batched |
| GIA ledger settlement | 1M inventory transactions/month × 3 ledgers | n/a | < 30 min per ledger | 3× settlement rows ≈ 10 GB | per-ledger partitions | ledgers run sequentially |

### 7.39.20 Localization and compliance hooks

- **Inventory valuation rules** differ: IFRS (IAS 2) prohibits LIFO; US GAAP allows LIFO (LIFO conformity rule ties tax and book); DE HGB allows Festwert and group valuation; FR PCG allows CUMP / FIFO. Global inventory accounting ledgers (FR-39.18) let one entity keep an IFRS FIFO ledger and a US LIFO tax ledger; country packs (wave 48) seed accounting conventions.
- **Standard cost capitalization**: IAS 2.13 requires fixed production overhead absorption at normal capacity; the costing sheet rate nodes carry `normalCapacityHours` and the variance report shows under-absorption that must be expensed; US §263A (UNICAP) additional costs are a tax-ledger costing sheet variant supplied by the US pack.
- **Transfer pricing**: Group and ProfitCenter views feed OECD Guidelines evidence (cost-plus margins) exported in FR-40.4; local file requirements (DE §90 AO, MX, IN Form 3CEB) consume it through wave 48.
- **Retention**: cost estimates and activation revaluation evidence 10 years (DE HGB §257, FR), 7 years (US).
- **Rounding**: 6-decimal unit costs, posting in currency minor unit; JPY and other zero-decimal currencies round to whole units at posting.

### 7.39.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.8.3 Standard cost → Revaluation on activation / Conversion to standard cost (Standard cost conversion checklist) | PARTIAL-39 | ADDED as FR-39.12 | revaluation on activation already FR-39.6 |
| AX 12.8.4 Standard cost conversion setup | PARTIAL-39 | ADDED as FR-39.12 | `costing.conversionBlockers` |
| AX 12.8.1 Global inventory accounting (multiple cost ledgers / accounting conventions) | GAP (proposed 20) | ADDED as FR-39.18 | costing-svc extends the wave-20 engine; flag-gated |
| AX 12.8.4 Global inventory accounting setup (ledgers, accounting policies, conventions) | GAP (proposed 20) | ADDED as FR-39.18 | `costing.globalInventoryAccounting` |
| SAP 13.8 CKECP / CKECP1 Easy cost planning / ad-hoc cost estimate | PARTIAL-39 | ADDED as FR-39.13 | project use in wave 41 consumes the estimate |
| SAP 13.8 CK51N / CK53N Sales order cost estimate | PARTIAL-27 (note proposes 39) | ADDED as FR-39.16 | |
| SAP 13.8 CK91N / CK92 Procurement alternatives / mixing ratios | PARTIAL-39 | ADDED as FR-39.14 | |
| SAP 13.8 Mixed costing customizing: quantity structure types, procurement alternatives (OMXL) | PARTIAL-39 | ADDED as FR-39.14 | `costing.mixedCosting`, `costing.mixingRatioSource` |
| SAP 13.8 Easy cost planning customizing: costing models, characteristics (CKF*) | PARTIAL-39 | ADDED as FR-39.13 | CostingModel entity |
| SAP 13.8 COGS split by cost component (FINSC_COGS*) | PARTIAL-39 | ADDED as FR-39.15 | |
| SAP 13.8 Price-difference splitting (S/4) | PARTIAL-39 | ADDED as FR-39.15 | |

## 7.40 Wave 40: Intercompany & Financial Consolidation

### 7.40.1 Scope and business context

Wave 40 closes phase 7 and delivers finance parity. Groups with several legal entities trade with each other, share costs and must report as one. Before this wave each entity is an island: an intercompany sale is keyed twice, due-to/due-from balances drift, and consolidated statements are assembled in spreadsheets. After it, pairing a customer in entity A with a vendor in entity B makes every sales order, packing slip, invoice and payment create its mirror in the partner automatically (including direct-delivery chains); intercompany journal lines post due-to/due-from entries in both entities; a reconciliation workspace matches both sides and flags mismatches; and the consolidation service builds group structures with ownership percentages, collects balances per unit, translates under IAS 21, eliminates intercompany balances, transactions, unrealized profit in inventory and investments, computes non-controlling interest, and produces consolidated statements through the wave-15 reporting designer with drill-down to the entity journal.

Out of scope: equity-method accounting for associates (v1.x), complex multi-tier ownership changes mid-year (step acquisitions), statutory group tax, transfer-pricing calculation itself (hooks only: documented IC prices and margin evidence export).

**Scope amendment (gap closure).** The registers (coverage-ax §12.99 rows 11, 73, 82, 97, 103, 126, 135, 190, 192, 193; coverage-sap §13.99 rows 87–116 mapped to wave 40; plus the regrouping rows proposed for wave 14) add centralized payments (FR-40.12), intercompany charges for expenses, time and vendor-invoice lines (FR-40.13), intercompany on-hand visibility (FR-40.14), intercompany planning groups (FR-40.15), group valuation for unrealized profit (FR-40.16), reclassification and regrouping rules (FR-40.17), consolidation of investments including the equity method and divestitures (FR-40.18), top-side group journals (FR-40.19) and the consolidation monitor with validation rules (FR-40.20). This supersedes the out-of-scope note above for equity-method accounting; step acquisitions (remeasuring a previously held interest when control is obtained) and proportional consolidation remain out of scope, with dispositions in 7.40.21.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| D365 intercompany: the mirror PO/SO is created by a synchronous X++ call; if the partner entity's number sequence or dimension validation fails, the user sees an infolog and the pair is left half-created (SalesTable without InterCompanyPurchId) | No saga; no compensation | Temporal saga with compensating cancel; both documents or neither | Orphan IC documents per month 5–20 → 0 |
| SAP cross-company FB01 posts via OBYA clearing accounts; FBICR3 (ICR) reconciles weeks later by downloading both ledgers | Reconciliation is a separate batch extract | Every IC posting carries a pair id; reconciliation is continuous per posting | Mismatch detection latency 30 days → < 1 min |
| Intercompany billing in SAP SD (RD04 output → INVOIC IDoc → FB01 vendor invoice) fails silently in WE02 when a tax code is missing; AP sees nothing | Message-based integration inside one system | Mirror vendor invoice created in the same saga with validation before commit | IDoc-style failures to triage per month 10–30 → 0 |
| AX consolidation (LedgerConsolidate online) copies balances into a consolidation company; eliminations (LedgerEliminationRule) are posted journals with no drill to source entity; reruns duplicate unless reversed manually | Copy-based consolidation into a ledger | Consolidation run is a derived, versioned dataset with elimination entries linked to source balances; rerun supersedes | Rerun effort 2 h → < 2 min; drill to entity 100% |
| Currency translation differences computed in Excel because AX "Consolidate online" translates all accounts at one rate type unless per-account overrides are maintained | Rate type per account instead of per statement category | IAS 21 methods by account category with CTA derived automatically | Manual CTA journals per period 1–3 → 0 |

### 7.40.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Trading partner setup | InterCompanyTradingPartner, InterCompanyEndpoint, InterCompanyEndpointActionPolicy, InterCompanyTradingValueMap, CustTable/VendTable intercompany flags | Customer/vendor with company code assignment, VBAK-VKORG IC sales org, OBYA clearing | IntercompanyPartner (customer in A ↔ vendor in B) with action policies and value maps | full |
| Mirror sales/purchase orders | SalesTable.InterCompanyOriginalPurchId, PurchTable.InterCompanyOriginalSalesId, InterCompanySalesOrder/PurchaseOrder classes, chains | Cross-company stock transport order (ME21N NB) with delivery and IC billing (VF01 IV) | Mirror document saga | full |
| Direct delivery chains | InterCompany direct delivery, SalesLine.InterCompanyInventDim, chain of orders | Third-party order processing, cross-company drop-ship | DirectDeliveryChain entity (3 orders linked) | full |
| Intercompany invoices | InterCompany invoice posting (sales invoice → vendor invoice), InterCompanyPurchInvoice | SD IV intercompany billing, RD04 → INVOIC → FB01 | Mirror invoice creation in same saga | improved |
| Intercompany accounting (journals) | LedgerJournalTrans.OffsetCompany, LedgerIntercompany (due-to/due-from accounts), intercompany journal | FB01 cross-company (BVORG), OBYA, FAGL cross-company postings | IcJournal with due-to/due-from derivation | full |
| Intercompany reconciliation | Intercompany reconciliation report (D365 10.0.x) | ICR FBICR3 / FBIC010 (GL open items), FBICS3 | IcReconciliationItem continuous matching | improved |
| Group structure and ownership | Consolidation legal entity, LedgerConsolidate, LedgerConsolidateLegalEntity (share %) | Consolidation units/groups (CXCUNIT), ownership in COI, ECCS CX5x | ConsolidationGroup, ConsolidationUnit, Ownership | full |
| Eliminations | LedgerEliminationRule, LedgerEliminationRuleLine, elimination journal (LedgerJournalType Elimination), ConsolidationAccount | Group Reporting reclassification/elimination tasks (ACDOCU document types 2x), IC profit in inventory, COI | EliminationRule engine, ConsolidationEntry | full |
| Currency translation | Consolidate online rate types per account, LedgerConsolidateExchangeRate | Translation methods (closing, average, historical), CTA | IAS 21 per account category, CTA derived | full |
| Consolidated statements | Management Reporter consolidated reporting tree | Group Reporting reports, ACDOCU-based analytics | Wave 15 designer over consolidated dataset with drill | full |
| Centralized payments | Centralized payments (Organization administration → Centralized payments, VendPaymentJournal paying for other legal entities), LedgerInterCompany | FBZP paying company code / sending company code, F110 cross-company payment | CentralizedPaymentSetup with due-to / due-from settlement (FR-40.12) | full |
| Intercompany charges (expenses, timesheets) | TrvExpTrans intercompany legal entity, ProjIntercompany parameters (lending / borrowing), intercompany timesheets and vendor invoices (R3) | CO cross-company allocations, PS intercompany billing (DP90 with cross-company resource-related billing) | IcChargeRequest contract (FR-40.13) | full |
| Intercompany on-hand | Intercompany on-hand (R3 Inventory → On-hand across legal entities) | MMBE across plants of different company codes | IC on-hand read model (FR-40.14) | full |
| Intercompany master planning | Intercompany planning groups (ReqIntercompanyPlanningGroup), intercompany master scheduling | MRP with special procurement key (cross-company stock transfer), MD01 sequence by plant | IcPlanningGroup run sequence (FR-40.15) | full |
| Group valuation of IC profit | none (profit in inventory computed manually) | Group / profit-center valuation views (OKKP, TKA01), transfer prices 8KEM / 8KEN | Group view cost per receipt layer (FR-40.16, with FR-39.17) | partial (no ML actual group costing) |
| Reclassification and regrouping | Financial reports reclass rows; no posting rule | FAGLF101 / FAGL_CL_REGROUP regrouping, Group Reporting reclassification methods and runs | ReclassificationRule engine (FR-40.17) | full |
| Consolidation of investments | LedgerConsolidate share % only | CoI activities (first consolidation, subsequent, increase / decrease, divestiture), equity method (ECCS CoI, Group Reporting "Manage Investments / Equity data") | ConsolidationOfInvestments activities (FR-40.18) | partial (no step acquisition) |
| Top-side group journals | Consolidation company journals (LedgerJournal in consolidation company) | Group Reporting "Post Group Journal Entries" (document types 0C / 1x / 2x), ECCS manual postings | GroupJournal per run with document types (FR-40.19) | full |
| Consolidation monitor and validation | Consolidate online process log | CXCD data monitor, consolidation monitor tasks, validation rules (Group Reporting "Define Validation Rules") | ConsolidationMonitor tasks and ValidationRule (FR-40.20) | full |

### 7.40.3 Functional requirements

- **FR-40.1 Trading partners** — Must. The system shall pair a customer in entity A with a vendor in entity B (and vice versa) with action policies (auto-create, auto-confirm, auto-post packing slip, auto-post invoice), value maps (item numbers, units, dimensions, charges) and an IC pricing policy (agreed price list, cost plus %, transfer price table).
  - Notes: one active pairing per entity pair and direction; policies versioned; mirror creation requires both entities on the same tenant.
- **FR-40.2 Mirror-document matrix** — Must. The system shall create mirror documents per the matrix below in a Temporal saga with compensation.
  - Notes: matrix (source in selling/holding entity → mirror in buying/receiving entity): sales order → purchase order; sales order change (qty/date/price) → PO change with re-approval if beyond policy tolerance; packing slip → product receipt; sales invoice → vendor invoice (matched 3-way to the mirror PO and receipt); customer payment received → vendor payment settled (optional, when netting is not used); sales return (RMA) → purchase return; free-text invoice → vendor invoice journal; IC journal line with offset entity → due-to/due-from entries both sides; fixed asset IC transfer (wave 37) → disposal + acquisition; reverse direction (PO created in buyer first) → sales order in seller. Saga steps: validate partner and policy → reserve number sequences in both entities → create source → create mirror → link (`IcDocumentLink`) → publish; on any failure compensate by canceling created documents.
- **FR-40.3 Direct delivery chains** — Must. The system shall support customer order in A, IC PO from A to B, IC SO in B shipping directly to A's customer, with B's packing slip posting A's product receipt and A's customer packing slip in one saga.
- **FR-40.4 Intercompany pricing and transfer-pricing hooks** — Should. The system shall price IC lines from the policy, record the pricing basis on the link, and export a transfer-pricing evidence file (item, qty, price, basis, cost, margin) per period.
- **FR-40.5 Intercompany accounting** — Must. The system shall derive due-to/due-from accounts per entity pair from the IC accounting setup and post balanced entries in both entities for IC journal lines, allocations (wave 14/38) and expense re-charges, each carrying a shared `ic_pair_id`.
  - Notes: both entries post atomically through the journal API per entity; if the second post fails the first is reversed; amounts in the counterparty entity convert at the document rate with FX difference booked to IC FX accounts.
- **FR-40.6 Intercompany reconciliation** — Must. The system shall match IC balances and transactions per entity pair, currency and period, classifying each as Matched (difference ≤ 0.01 or FX tolerance), Timing (counterpart exists in the next period), or Mismatch, with a workspace to resolve.
  - Notes: FX tolerance default 0.5% of amount; period-end summary required to be free of Mismatch before consolidation unless overridden by `ConsolidationApprove`.
- **FR-40.7 Group structure** — Must. The system shall define consolidation groups and units (legal entities or sub-groups) with effective-dated ownership %, consolidation method (full / proportional — v1 full only), group currency and chart mapping (entity main account → consolidation account).
- **FR-40.8 Data collection and translation** — Must. The system shall collect trial balances per unit and period from the wave-11 balance projection, translate under IAS 21: assets/liabilities at closing rate, income/expense at average rate (or transaction-date rates by option), equity at historical rates, and derive CTA = translated closing net assets − (translated opening net assets + translated net income); CTA posts to the OCI consolidation account.
- **FR-40.9 Eliminations** — Must. The system shall apply elimination rules in order: (E1) IC revenue/COGS and other P&L; (E2) IC receivables/payables and loans, with the exchange difference to profit or loss (IAS 21.45) unless the balance is flagged as part of a net investment in a foreign operation, then to CTA (IAS 21.32); (E3) unrealized profit in inventory (upstream shared with NCI, downstream parent only); (E4) investment in subsidiary against equity at acquisition with goodwill, retranslated at the closing rate with the difference to CTA (IAS 21.47); (E5) NCI share of current-year profit and equity; (E6) dividends within group. Each produces ConsolidationEntry lines linked to the source balances.
  - Notes: worked 2-entity example (group currency USD; P owns 80% of S, S in EUR; closing rate 1.10, average 1.08, historical at acquisition 1.05). S sold goods to P for EUR 100,000 (cost EUR 70,000); 40% remain in P's inventory; S's receivable EUR 100,000 open; P's payable recorded at USD 108,500. E1: Dr Revenue 108,000.00 / Cr COGS 108,000.00 (100,000 × 1.08). E2: Dr AP 108,500.00, Dr CTA 1,500.00 / Cr AR 110,000.00 (100,000 × 1.10). E3: unrealized profit = 30,000 × 40% = EUR 12,000 × 1.08 = 12,960.00: Dr COGS 12,960.00 / Cr Inventory 12,960.00; upstream so NCI shares 20%: Dr NCI (equity) 2,592.00 / Cr NCI share of profit 2,592.00. E4: S share capital EUR 500,000 and pre-acquisition retained earnings EUR 200,000 at 1.05 = 735,000.00; P investment USD 620,000.00; Dr Share capital 525,000.00, Dr Pre-acquisition RE 210,000.00, Dr Goodwill 32,000.00 / Cr Investment in S 620,000.00, Cr NCI 147,000.00 (20% × 735,000). E5: S net income EUR 150,000 × 1.08 = 162,000.00; Dr Profit attributable to NCI 32,400.00 / Cr NCI (equity) 32,400.00. Each entry balances; the run reports Σ debits = Σ credits = 1,032,952.00 across eliminations E1–E5.
- **FR-40.10 Consolidation run** — Must. The system shall execute collection → translation → eliminations → NCI → validation as one versioned run per group/period with partial recompute for changed units, producing a consolidated trial balance and statements.
  - Notes: run of 50 units × 10,000 accounts in < 5 min; validation: consolidated balance sheet balances; Σ eliminations per rule balanced; no unresolved Mismatch items.
- **FR-40.11 Consolidated reporting and drill** — Must. The system shall expose the consolidated dataset to the wave-15 designer with drill from a consolidated line to unit balances, elimination entries and entity journal lines.
- **FR-40.12 Centralized payments** — Must. The system shall let a paying entity settle vendor invoices of other entities (pay on behalf of) and receive customer payments for other entities, posting due-to / due-from in both entities and settling the open item in the owning entity.
  - Notes: setup per (paying entity, owning entity, payment method) with the due-to / due-from accounts from the BR-40.4 matrix; worked example: P pays S's vendor invoice EUR 10,000.00 → P: Dr Due from S 10,000.00 / Cr Bank 10,000.00; S: Dr Vendor 10,000.00 / Cr Due to P 10,000.00, open item settled in S; cash discount belongs to the owning entity; FX difference in the owning entity when currencies differ; customer receipts work in reverse; a payment proposal in the paying entity (wave 19) selects open items across the owning entities it is set up for; both postings carry one `ic_pair_id` and run in the mirror saga (FR-40.2 compensation rules).
- **FR-40.13 Intercompany charges** — Must. The system shall accept intercompany charge requests from expense lines (wave 43), timesheet and project transactions (wave 41) and vendor invoice lines (wave 18) whose cost belongs to another entity, price them by the IC charge pricing policy, and post the cost in the borrowing entity with a due-to and the recovery in the lending entity with a due-from or an IC invoice.
  - Notes: `IcChargeRequest` fields: source document, lending entity, borrowing entity, category (Expense / Hour / Item / Fee), quantity, cost amount, dimensions in the borrowing entity; pricing: cost, cost-plus % per category, or transfer price table per resource role and category (AX intercompany transfer prices); settlement mode Journal (due-to / due-from) or Invoice (IC customer invoice in lender → vendor invoice in borrower through FR-40.2); tax on cross-border recharges from wave 13; the consuming waves (41, 43) call the same API, so no IC logic lives in those services.
- **FR-40.14 Intercompany on-hand** — Should. The system shall show on-hand and available quantities of trading partners' sites for items mapped by the partner value map when entering IC orders, without exposing cost.
  - Notes: read model built from wave-16 on-hand events of partner entities in the same tenant, filtered by the IC partner's allowed sites; refresh lag p95 < 60 s; visibility requires the partner policy `share-on-hand = true` in both directions.
- **FR-40.15 Intercompany planning groups** — Should. The system shall define planning groups of entities with a run sequence so that planned IC purchase orders created by MRP in a buying entity become IC demand in the supplying entity before that entity is planned, and firming creates the mirror documents through FR-40.2.
  - Notes: sequence derived from the IC partner graph (buyers before suppliers) with cycle detection; each entity is planned by the wave-22 engine in sequence inside one Temporal workflow; IC demand carries the requested date minus the IC transport lead time; replanning a buyer after its supplier marks the supplier plan stale (IC-0815).
- **FR-40.16 Group valuation for unrealized profit** — Must. The system shall compute unrealized profit in inventory (E3) per IC receipt layer as remaining quantity × (Legal-view unit cost in the holder − Group-view unit cost from the supplier, FR-39.17), and fall back to the seller's average margin (BR-40.7) when a layer carries `groupCostUnknown`.
  - Notes: layers follow the holder's costing method (FIFO for remaining layers by default); supplier group cost converts at the receipt-date rate and the elimination translates at the average rate of the period; the elimination detail lists every layer for drill.
- **FR-40.17 Reclassification and regrouping rules** — Must. The system shall run rule-based reclassifications at entity level (posting through the journal with automatic reversal on the first day of the next period) and at group level (consolidation entries), covering sign-based moves, remaining-term splits and two-sided intercompany elimination methods with difference accounts and thresholds.
  - Notes: entity-level rule types: DebitBalanceVendors → other receivables, CreditBalanceCustomers → other payables, RemainingTerm (receivables / payables / loans due > 12 months → non-current, per open item due date), invoked as a wave-14 close checklist task (SAP FAGLF101 / FAGL_CL_REGROUP equivalent); group-level types: SignBased (for example negative consolidated cash → bank overdrafts), FS item remap, and two-sided IC elimination with difference posted to an IC difference account when |difference| ≤ threshold (default 1,000.00 group currency) and blocking the run above it; rules are ordered, versioned and dry-runnable.
- **FR-40.18 Consolidation of investments and equity method** — Should. The system shall record investments and equity holdings per investor and investee with effective-dated ownership and cost, and run CoI activities: first consolidation (goodwill per E4), subsequent consolidation, ownership increase or decrease without change of control (equity transaction, no goodwill change), full divestiture with deconsolidation gain or loss, and the equity method for associates and joint ventures.
  - Notes: divestiture gain = proceeds − (group carrying amount of net assets + goodwill − NCI) + CTA recycled from OCI; equity method: share of profit = ownership % × associate net income, dividends received reduce the investment, worked example: 30% associate, investment USD 300,000.00, associate net income 200,000.00, dividends paid to the investor 20,000.00 → Dr Investment in associate 60,000.00 / Cr Share of profit of associates 60,000.00; Dr Dividend income 20,000.00 / Cr Investment in associate 20,000.00; closing carrying amount 340,000.00; associates are not line-consolidated and need only net income, equity and dividends, collected through FR-40.8 or imported (`/consol/imports`).
- **FR-40.19 Top-side group journals** — Must. The system shall let consolidation accountants post manual group journals per run and document type (Adjustment, Reclassification, Elimination, CoI) at group or unit level, with optional carry-forward into later periods and approval.
  - Notes: document types decide whether entries are period-only or cumulative (carry forward in the same fiscal year) and whether they need approval (default: amount ≥ 100,000.00 group currency); top-side entries survive reruns (they attach to the group and period, not to a run version) and are re-applied to each new version; every entry needs a reason text and attachment.
- **FR-40.20 Consolidation monitor and validation rules** — Must. The system shall present a monitor per group and period with tasks per unit (collect, release, validate, translate) and per group (IC eliminations, profit in inventory, CoI, reclassification, top-side, group validation), enforce task dependencies, lock released unit data, and evaluate validation rules at unit and group level.
  - Notes: task status NotStarted / Running / Completed / CompletedWithWarnings / Error / Blocked; unit data release locks the unit's collected balances for the period (re-collection needs `ConsolidationApprove`); validation rules are expressions over FS items (for example total assets = total liabilities + equity; IC partner present on IC FS items; change in retained earnings = net income − dividends ± tolerance 1.00) with severity Error (blocks) or Warning; the monitor feeds the wave-14 close checklist.

### 7.40.4 Data model

**IcDocumentLink**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| ic_pair_id | uuid | yes | shared by all documents in a chain |
| partner_id | uuid | yes | FK IntercompanyPartner |
| source_entity_id, source_doc_type, source_doc_id | uuid, enum, uuid | yes | |
| mirror_entity_id, mirror_doc_type, mirror_doc_id | uuid, enum, uuid | yes | |
| chain_id | uuid | no | direct delivery chain |
| saga_id | text | yes | Temporal workflow id |
| status | enum | yes | Creating/Linked/Compensated/Failed |
| pricing_basis | jsonb | no | policy, price list, markup |

**IcReconciliationItem**: id, entity_a_id, entity_b_id, period_key, currency, ic_pair_id, amount_a, amount_b, difference, fx_tolerance_applied, status (Matched/Timing/Mismatch/Resolved), resolution_note, resolved_by.

**ConsolidationUnit**: id, group_id, legal_entity_id (or child_group_id), functional_currency, ownership[] ({from, to, percent numeric(7,4)}), method (Full), acquisition_date, historical_rate numeric(19,8), chart_mapping_id.

**ConsolidationRun**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| group_id | uuid | yes | |
| period_key | text | yes | |
| version | int | yes | increments per rerun |
| status | enum | yes | Collecting/Translating/Eliminating/Validating/Completed/Failed/Superseded |
| rates | jsonb | yes | closing, average, historical by currency |
| cta_amount | numeric(19,4) | no | |
| validation | jsonb | no | checks and results |
| started_at, completed_at | timestamptz | yes/no | |

**ConsolidationEntry**: id, run_id, rule_code (E1–E6/Translation/Collection), unit_id, consolidation_account_id, debit, credit, group_currency, source_refs jsonb ({unitId, accountId, amount, rate}), nci_share numeric(19,4).

**Entities added by FR-40.12–FR-40.20**: **CentralizedPaymentSetup** (paying_entity_id, owning_entity_id, payment_method, direction Pay/Receive, due_to_account, due_from_account, effective_from); **IcChargeRequest** (id, source_type Expense/Timesheet/ProjectTransaction/VendorInvoiceLine, source_id, lending_entity_id, borrowing_entity_id, category, quantity, cost_amount, price_amount, pricing_basis jsonb, settlement_mode Journal/Invoice, ic_pair_id, status Received/Priced/Posted/Failed); **IcTransferPrice** (lending_entity_id, borrowing_entity_id, category, resource_role, method Cost/CostPlus/Table, markup_pct, price, currency, effective_from); **IcPlanningGroup** (id, members [{entityId, sequence}], lead_time_days per pair); **ReclassificationRule** (id, level Entity/Group, type, selection jsonb, target_account_or_fs_item, threshold, order, version, auto_reverse bool); **Investment** (investor_unit_id, investee_unit_id, method Full/Equity, ownership[] {from, percent}, cost, acquisition_date, goodwill, fair_value_adjustments jsonb); **GroupJournal** (id, group_id, period_key, document_type, unit_id, lines [{fsItem, debit, credit, partnerUnit}], carry_forward bool, approval_status, reason); **ConsolidationTask** (group_id, period_key, unit_id, task_type, status, started_at, completed_at, messages jsonb); **ValidationRule** (id, level, expression, severity, tolerance, version).

Invariants: every IcDocumentLink with status Linked has both documents existing and amounts equal at document rate; Σ ConsolidationEntry debit = Σ credit per run and per rule_code; ownership percentages per unit never exceed 100.00% at any date; a Completed run is immutable (rerun creates a new version and marks the old Superseded). Indexes: IcDocumentLink by ic_pair_id, by (mirror_entity_id, mirror_doc_id); ConsolidationEntry by (run_id, consolidation_account_id); IcReconciliationItem by (entity_a_id, entity_b_id, period_key, status).

### 7.40.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/interco/partners | Create pairing and policy | Idempotency-Key | IntercompanyConfigure |
| POST | /api/v1/interco/mirrors | Start mirror saga for a source document (system) | sourceDocId+version | IntercompanySystem |
| GET | /api/v1/interco/links?pairId= | Chain view | n/a | IntercompanyView |
| POST | /api/v1/interco/journals | IC journal with offset entity | Idempotency-Key | IntercompanyPost |
| POST | /api/v1/interco/reconciliations/run?period= | Run matching | (period, version) | IntercompanyReconcile |
| POST | /api/v1/interco/reconciliations/items/{id}/resolve | Resolve | state check | IntercompanyReconcile |
| POST | /api/v1/consol/groups | Create group/units/ownership | Idempotency-Key | ConsolidationConfigure |
| POST | /api/v1/consol/runs | Start run (group, period) | (group, period) new version | ConsolidationRun |
| GET | /api/v1/consol/runs/{id}/trial-balance | Consolidated TB | n/a | ConsolidationView |
| GET | /api/v1/consol/runs/{id}/entries?rule= | Elimination entries with source refs | n/a | ConsolidationView |
| POST | /api/v1/consol/imports | External unit TB import | file hash | ConsolidationConfigure |
| POST | /api/v1/interco/centralized-payments/setup | Configure pay / receive on behalf | Idempotency-Key | IntercompanyConfigure |
| POST | /api/v1/interco/charges | Submit IC charge request (system) | sourceId+version | IntercompanySystem |
| GET | /api/v1/interco/onhand?partner=&item= | Partner on-hand | n/a | IntercompanyView |
| POST | /api/v1/interco/planning-groups/{id}/run | Sequenced IC planning run | runId | IntercompanyPlan |
| POST | /api/v1/consol/reclassification-runs | Entity- or group-level reclass (dryRun option) | (scope, period, version) | ConsolidationRun |
| POST | /api/v1/consol/group-journals | Post top-side journal | Idempotency-Key | ConsolidationJournal |
| POST | /api/v1/consol/investments | Record investment / ownership change | Idempotency-Key | ConsolidationConfigure |
| GET | /api/v1/consol/monitor?group=&period= | Monitor tasks and statuses | n/a | ConsolidationView |
| POST | /api/v1/consol/units/{id}/release?period= | Release and lock unit data | state check | ConsolidationUnitRelease |

Validation: mirror saga rejects when the partner policy is inactive or the mirror entity's period is closed; consolidation run rejects unresolved Mismatch items unless override; rates must exist for every unit currency.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.interco.mirror.created.v1` | saga linked | icPairId, sourceDocId, sourceEntity, mirrorDocId, mirrorEntity, type | sales, procurement, analytics |
| `dev.passage.interco.mirror.compensated.v1` | saga failed | icPairId, failedStep, reason | workflow, alerts |
| `dev.passage.interco.journal.posted.v1` | IC journal both sides posted | icPairId, entityA, entityB, amount, currency, journalIds[] | reconciliation, consol-svc |
| `dev.passage.interco.reconciliation.flagged.v1` | mismatch | itemId, entities, difference | workspace, workflow |
| `dev.passage.consol.run.completed.v1` | run completed | runId, groupId, period, version, unitCount, eliminationCount, ctaAmount | reporting, analytics |
| `dev.passage.interco.charge.posted.v1` | IC charge posted in both entities | chargeId, icPairId, lendingEntity, borrowingEntity, amount, settlementMode | wave 41 projects, wave 43 expenses, reconciliation |
| `dev.passage.interco.planning.completed.v1` | planning group run done | runId, groupId, entitiesPlanned[], icDemandCount | master planning (22), workspace |
| `dev.passage.consol.unit.released.v1` | unit data released | groupId, unitId, period, balancesHash | consol-svc monitor |
| `dev.passage.consol.validation.failed.v1` | validation rule error | runId, ruleId, unitId, difference | workspace, close checklist (14) |

### 7.40.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Intercompany workspace | Pending sagas, failed mirrors, mismatches, netting link | pair, source, mirror, status, age | IntercompanyView |
| Partner setup | Pair, policies, value maps, pricing | entity A/B, customer/vendor, actions, tolerances | IntercompanyConfigure |
| Chain view | Open any document in chain, retry/compensate | SO, PO, receipts, invoices, payments with status | IntercompanyView |
| IC reconciliation | Run, filter status, resolve, drill both sides | pair, currency, amount A, amount B, difference, status | IntercompanyReconcile |
| Consolidation workspace | Define group, start run, view validation, open statements | group, period, version, status, CTA, checks | ConsolidationRun |
| Elimination entries | Filter by rule/unit, drill to source | rule, unit, account, debit, credit, NCI share | ConsolidationView |

Interaction rules: chain view renders as a graph with keyboard navigation between nodes; reconciliation grid pairs rows side by side with difference highlighted; consolidation run progress streams per step; statements open in the wave-15 viewer with a "consolidated" badge and drill menu.

### 7.40.7 Business rules and edge cases

- **BR-40.1** Mirror saga state machine: Creating → Linked; Creating → Compensated (all created documents canceled) → Failed when compensation itself fails (alert, manual); Linked documents cannot be deleted, only canceled through the chain.
- **BR-40.2** Source and mirror amounts are equal in document currency; each entity's accounting currency conversion uses its own rate at the document date; differences surface in reconciliation as FX-tolerated.
- **BR-40.3** A change to a linked sales order beyond policy tolerance (default qty ±0, price ±0.5%, date ±2 days) puts the mirror PO into re-approval (wave 17 workflow); within tolerance it updates silently.
- **BR-40.4** Due-to/due-from derivation: entity A line with offset entity B posts A: Dr/Cr account, Cr/Dr "Due from B"; B: Dr/Cr "Due to A", Cr/Dr the offset account; accounts resolved from the IC accounting matrix (A,B); missing matrix entry fails the post.
- **BR-40.5** Reconciliation Timing status requires the counterpart within the next period; after that it becomes Mismatch.
- **BR-40.6** Translation: historical rate per equity movement date; CTA computed per unit, then NCI share of CTA (ownership %) reclassified to NCI.
- **BR-40.7** Unrealized profit in inventory uses the seller's margin on the remaining quantity (FIFO assumption on IC receipts) and the average rate of the sale period.
- **BR-40.8** Ownership changes mid-period apply from the effective date; income is split pro rata by days unless a measured sub-period balance is provided.
- **BR-40.9** Consolidation rounding: entries computed at 6 decimals, rounded to the group currency minor unit per line; a per-rule rounding line keeps each rule balanced.
- **BR-40.10** Rerun with no changed units and same rates returns the existing version (idempotent); any change creates version +1 and supersedes.
- **BR-40.11** Centralized payment: the owning entity's open item is settled only when the paying entity's payment posts; a failed bank payment (wave 19 return) reverses both entities' postings in one saga.
- **BR-40.12** IC charge pricing resolves the transfer price effective on the source transaction date; a missing price fails the charge with IC-0821 and the source document stays posted in the lending entity on a suspense IC account until repriced.
- **BR-40.13** IC planning sequence: an entity appears once per run; cycles in the partner graph are broken by the group's explicit sequence, else the run fails with IC-0816.
- **BR-40.14** Entity-level reclassification entries reverse on the first calendar day of the next period with the same journal source; a rerun in the same period reverses the previous run first.
- **BR-40.15** Two-sided IC elimination: difference = Σ receivable side − Σ payable side per partner pair and FS item group after translation; |difference| ≤ threshold posts to the IC difference item; above threshold the task ends in Error.
- **BR-40.16** Ownership change without loss of control posts the difference between consideration and the NCI carrying amount to group equity; goodwill is unchanged.
- **BR-40.17** Unit data released for a period cannot be re-collected without `ConsolidationApprove`; re-collection invalidates every downstream task of that unit and period.
- **BR-40.18** Carry-forward top-side journals are re-applied to every later period of the same fiscal year and stop at year end unless the document type is `Cumulative`.

### 7.40.8 Acceptance criteria

- **AC-40.1** (FR-40.2) Given a sales order of 100 × 25.00 EUR in A to the IC customer, when confirmed, then a PO 100 × 25.00 EUR exists in B linked by ic_pair_id, and both documents balance.
- **AC-40.2** (FR-40.2) Given 1,000 random IC orders including planted number-sequence failures, when sagas run, then every pair is either Linked with equal amounts or Compensated with no orphan document.
- **AC-40.3** (FR-40.3) Given a direct-delivery chain, when B posts the packing slip, then A's product receipt and A's customer packing slip post in the same saga with matching quantities.
- **AC-40.4** (FR-40.5) Given an IC journal line of USD 1,000.00 expense in A offset to B, when posted, then A shows Dr Expense 1,000.00 / Cr Due from B 1,000.00 and B shows Dr Due to A 1,000.00 / Cr Cash 1,000.00 with one ic_pair_id.
- **AC-40.5** (FR-40.6) Given 50 planted mismatches (missing mirror, wrong amount, wrong period), when reconciliation runs, then all 50 are flagged and no matched pair is flagged.
- **AC-40.6** (FR-40.8) Given unit S with opening net assets EUR 700,000 at 1.05, net income EUR 150,000 at 1.08, closing net assets EUR 850,000 at 1.10, when translated, then CTA = 935,000 − (735,000 + 162,000) = 38,000.00 USD.
- **AC-40.7** (FR-40.9) Given the worked P/S example, when the run executes, then entries E1–E5 post with the stated amounts and each rule balances.
- **AC-40.8** (FR-40.9) Given the 3-entity, 2-currency demo group fixture, when consolidated, then the consolidated balance sheet and P&L match the accountant-reviewed eliminations to the cent.
- **AC-40.9** (FR-40.11) Given a consolidated revenue line, when drilled, then unit balances, E1 entries and entity journal lines are reachable within 3 clicks.
- **AC-40.10** (FR-40.12) Given P pays S's vendor invoice of EUR 10,000.00, when the payment posts, then P shows Dr Due from S / Cr Bank 10,000.00, S shows Dr Vendor / Cr Due to P 10,000.00 with the invoice settled, and both lines share one ic_pair_id.
- **AC-40.11** (FR-40.13) Given 8 hours by a lending-entity consultant at cost 60.00 with cost-plus 10%, when the charge posts in Journal mode, then the borrower books 528.00 project cost with Due to lender, and the lender books Due from borrower 528.00 against cost recovery 480.00 and IC margin 48.00.
- **AC-40.12** (FR-40.16) Given 40 units remaining from an IC receipt at Legal cost 120.00 and Group cost 100.00, when E3 runs, then unrealized profit of 800.00 in the receipt currency is eliminated and the detail lists the layer.
- **AC-40.13** (FR-40.17) Given a vendor with a debit balance of USD 5,000.00 at period end, when the regrouping rule runs, then Dr Other receivables 5,000.00 / Cr Vendor reclass 5,000.00 posts and reverses on the first day of the next period.
- **AC-40.14** (FR-40.18) Given the 30% associate example, when the equity method runs, then the investment carrying amount is 340,000.00 and the share of profit is 60,000.00.
- **AC-40.15** (FR-40.20) Given a unit whose balance sheet does not balance by 12.00, when validation runs, then the unit task ends in Error and the group translation task is Blocked.
- **AC-40.16** (FR-40.15) Given buyer A with a planned IC PO of 100 units for 2026-12-10 and transport lead time 3 days, when the planning group runs, then supplier B is planned after A with IC demand of 100 units on 2026-12-07.

### 7.40.9 Test plan and fixtures

Fixtures: `ic-orders-1000` (generated across 3 entities, 2 currencies, with planted failures and direct-delivery chains); `ic-recon-planted-50` (mismatch types); `consol-group-3` (P in USD, S1 in EUR 80%, S2 in USD 100%, IC sales, inventory, loans, dividends; accountant workbook); `translation-cases` (rate scenarios, mid-year acquisition).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Saga compensation | Property | interco-svc | no orphan documents under random failures |
| Mirror amount equality | Property | links | source = mirror in document currency |
| Due-to/due-from derivation | Unit | IC accounting | matrix cases |
| Reconciliation classifier | Unit | matcher | Matched/Timing/Mismatch per table |
| Translation and CTA | Unit | consol-svc | worked example exact |
| Elimination rules | Unit/E2E | run | P/S example; demo group workbook |
| Run performance | Performance | run | 50 units × 10k accounts < 5 min |
| Drill-down | E2E | reporting | 3 clicks to journal |

Reconciliation checks: Σ due-from in A = Σ due-to in B per pair and currency (after FX tolerance); consolidated TB balances; Σ eliminations per rule = 0; consolidated equity = parent equity + NCI + CTA.

### 7.40.10 Deployment and operations

Helm charts `interco-svc` (TS, Temporal workers), `consol-svc` (Go), Postgres `group`; env `IC_SAGA_TIMEOUT_S=120`, `IC_FX_TOLERANCE_PCT=0.5`, `CONSOL_MAX_UNITS=500`; flags `interco.payment-mirroring`, `consol.external-import`; jobs: `ic-reconcile` hourly and at period close, `ic-saga-sweeper` every 5 min for stuck sagas, `consol-run-scheduled` on close checklist task (wave 14). SLOs: mirror saga p95 < 5 s, reconciliation run < 2 min for 100k items, consolidation as above. Alerts: compensated sagas > 0 per hour, stuck sagas > 10 min, run failures. Runbook: retry saga, manual compensation, rerun consolidation version. Rollback: Helm; runs are versioned datasets, links are append-only.

### 7.40.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| InterCompanyTradingPartner, InterCompanyEndpoint*, value maps | IntercompanyPartner, policies | |
| SalesTable/PurchTable IC links (open orders) | IcDocumentLink | open chains only, status Linked |
| LedgerIntercompany (due-to/due-from setup) | IC accounting matrix | |
| LedgerConsolidate*, LedgerEliminationRule/Line, ConsolidationAccount mapping | ConsolidationGroup, Unit, EliminationRule, chart mapping | |
| OBYA clearing accounts, customer/vendor IC assignments | IC accounting matrix, IntercompanyPartner | |
| Open cross-company STOs and IC billing documents | IcDocumentLink | |
| Group Reporting units/groups/FS item mapping, ECCS CX hierarchy | ConsolidationGroup, Unit, chart mapping | |
| ACDOCU / ECMCT history | lakehouse archive | prior consolidated results kept for comparison, not re-run |

Validation: IC balances per pair equal legacy after migration; first Passage consolidation for the last closed legacy period matches legacy consolidated statements within 0.01% per line (differences explained). Not migrated: closed IC chains, legacy elimination journals (replaced by derived runs).

### 7.40.12 Dependencies, risks, open questions

Depends on: waves 11–15 (journal, rates, period control, reporting), wave 17 PO and approval, wave 18 vendor invoice matching, wave 27–29 sales orders, shipping and invoicing, wave 30 payments, wave 36 netting, wave 37 IC asset transfers, wave 39 cost for unrealized profit.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Cross-domain saga partial failure leaves stuck state | Medium | High | Sweeper job, compensation tests, manual runbook |
| Accountant disagreement on NCI/CTA presentation | Medium | Medium | Workbook-driven fixtures; sign-off gate |
| Group chart mapping effort per customer | High | Medium | Mapping import template; validation report |

Open questions: (1) Mirror customer payments by default or rely on netting? Proposed netting; decide by wave 40 start. (2) Proportional consolidation in v1? Proposed no; decide at phase exit. (3) Transfer-pricing evidence format (CSV vs OECD XML)? Proposed CSV; decide by wave 40.

### 7.40.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `ic.accountingMatrix` | entity pair | table | none | (A, B) → due-to, due-from, IC FX accounts | BR-40.4 derivation | Intercompany accounting setup (LedgerInterCompany) | OBYA cross-company clearing accounts |
| `ic.partnerPolicy` | entity pair × direction | struct | auto-create on, auto-post off | create / confirm / post packing slip / post invoice flags | FR-40.1 action policies | InterCompanyEndpointActionPolicy | IC billing output (VF01 IV, RD04 output type), customer / vendor per company code |
| `ic.valueMaps` | entity pair | table | none | item, unit, dimension, charge maps | FR-40.1 mapping | InterCompanyTradingValueMap | customer-material info records (VD51), plant assignment |
| `ic.pricingPolicy` | entity pair | enum | PriceList | PriceList / CostPlus / TransferPriceTable | IC line price (FR-40.4) | Intercompany trade → Price / InterCompanyTradingRelation pricing | pricing procedure with PI01 / IV01 conditions, 8KEM / 8KEN transfer price variants |
| `ic.changeTolerance` | entity pair | struct | qty ±0, price ±0.5%, date ±2 days | ranges | BR-40.3 | none | none |
| `ic.fxTolerancePct` | tenant | decimal | 0.5% | 0–5% | FR-40.6 Matched | none | FBICR3 tolerance settings (ICR) |
| `ic.sagaTimeoutS` | tenant | int | 120 | 30–600 | saga timeout (env `IC_SAGA_TIMEOUT_S`) | none | none |
| `ic.centralizedPayments` | entity pair | struct | none | paying entity, payment methods, direction | FR-40.12 | Centralized payments setup | FBZP paying company code / sending company code |
| `ic.chargeSettlementMode` | entity pair | enum | Journal | Journal / Invoice | FR-40.13 settlement | Intercompany project parameters (lending / borrowing, invoice vs journal) | PS intercompany billing (DP90 cross-company) |
| `ic.transferPrices` | entity pair × category | table | cost | Cost / CostPlus % / Table | FR-40.13 pricing | Project → Transfer prices (intercompany) | CO transfer price variants (TKA01 valuation) |
| `ic.shareOnHand` | entity pair | bool | false | true / false | FR-40.14 visibility | Intercompany on-hand (R3) visibility | plant assignment / ATP across company codes |
| `ic.planningGroups` | tenant | table | none | members, sequence, lead times | FR-40.15 | Intercompany planning groups / members | MRP special procurement key (cross-company), plant sequencing |
| `consol.groupCurrency` | consolidation group | char(3) | USD | ISO 4217 | translation target | Consolidation legal entity currency | consolidation group currency (Group Reporting) |
| `consol.chartMapping` | unit | table | none | main account → consolidation account / FS item | FR-40.7 | Consolidation accounts (MainAccount.ConsolidationMainAccount), consolidation account groups | FS item mapping (Group Reporting), ECCS FS item hierarchy |
| `consol.translationMethods` | group | table | closing / average / historical by account category | per category method | FR-40.8 | Consolidate online → exchange rate type per account | translation methods (closing, average, historical) |
| `consol.eliminationRules` | group | ordered rules | E1–E6 | rule sets | FR-40.9 | Elimination rules (LedgerEliminationRule) | elimination / reclassification methods (Group Reporting), ECCS elimination of IC payables / receivables |
| `consol.icThreshold` | group | money | 1,000.00 group currency | ≥ 0 | BR-40.15 | none | reclassification method difference threshold |
| `consol.reclassRules` | entity / group | ordered rules | DebitBalanceVendors, CreditBalanceCustomers, RemainingTerm > 12 months | rule types (FR-40.17) | FR-40.17 | none (financial report rows only) | FAGL_CL_RGRP sort methods and adjustment accounts, FAGLF101 |
| `consol.coiMethods` | investment | enum | Full | Full / Equity | FR-40.18 | LedgerConsolidateLegalEntity share % | CoI methods (purchase, equity) |
| `consol.documentTypes` | group | table | Adjustment, Reclassification, Elimination, CoI | period / cumulative, approval threshold | FR-40.19 | Consolidation journal names | Group Reporting document types (0C, 1x, 2x, 3x) |
| `consol.validationRules` | unit / group | expressions | 3 seeded | expression, severity, tolerance | FR-40.20 | none | validation rules (Define Validation Rules) |
| `consol.maxUnits` | tenant | int | 500 | 1–2,000 | env `CONSOL_MAX_UNITS` | none | none |

### 7.40.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| IC chain view | drill-down | pair id, document, status | documents in chain, status, amounts | `ic_document_link` | Intercompany sales order / purchase order references | VF03 / ME23N document flow |
| Failed / compensated sagas | operational report | date, partner, failed step | saga, source, step, reason, age | `ic_document_link` status Compensated / Failed | none | WE02 IDoc errors (INVOIC) |
| IC reconciliation | list inquiry | pair, period, currency, status | amount A, amount B, difference, status, resolution | `ic_reconciliation_item` | Intercompany reconciliation report (D365) | FBICR3, FBIC010, FBICS3 |
| Due-to / due-from balances | statutory report | entity pair, period | due-to, due-from, difference, ageing | journal balances by IC accounts | Intercompany accounting balances | FAGLB03 on clearing accounts, ICR reports |
| Centralized payments log | list inquiry | paying entity, owning entity, date | payment, invoices settled, amounts, ic pair | `ic_document_link` type CentralPayment | Centralized payments journal lines | F110 payment list by paying company code |
| IC charges | list inquiry | lender, borrower, category, period | charge, source, cost, price, margin, mode | `ic_charge_request` | Intercompany expense / timesheet lines | KSB1 cross-company allocations |
| Transfer-pricing evidence | statutory report | entity pair, period | item / category, qty, price, basis, cost, margin % | `ic_document_link.pricing_basis` + FR-39.17 | none | TP documentation from 8KEM conditions |
| Consolidated trial balance | statutory report | group, period, version | FS item, unit amounts, eliminations, consolidated | `consolidation_entry` | SSRS Consolidation (consolidated balances) | ECCS CX5x reports, Group Reporting "Group Data Analysis" |
| Elimination detail | drill-down | run, rule, unit | entry, FS item, debit, credit, NCI, source refs | `consolidation_entry` | Elimination journal lines | CoI / elimination document display |
| Consolidation monitor | KPI | group, period | tasks by unit / group, status, duration | `consolidation_task` | none | Consolidation monitor (Group Reporting), CXCD data monitor |
| CTA and NCI roll-forward | statutory report | group, period | opening, translation movement, NCI share, closing | `consolidation_entry` (Translation, E5) | none | Group Reporting statement of changes in equity |
| Investment / equity holdings | list inquiry | group, investor, method | investee, ownership %, cost, goodwill, carrying amount | `investment` | Consolidation legal entity share % | Manage Investments / Equity data |

### 7.40.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| IC invoice (sales) and mirror vendor invoice | FR-40.2 invoice step, FR-40.13 Invoice mode | standard invoice layout (wave 29) plus IC partner, pair id, transfer-pricing basis | PDF, e-invoice per country pack (wave 48) where IC invoices are in mandate scope | seller's invoice sequence | VAT invoice rules apply to cross-border IC supplies; 10 years |
| IC netting / settlement statement | centralized payment or netting (FR-36.10) | pair, documents settled, amounts, FX | PDF, email to both entity controllers | settlement number | 10 years |
| IC balance confirmation | period end | pair, balances by account and currency, difference, confirm box | PDF, portal acknowledgment | per pair per period | audit evidence 10 years |
| Transfer-pricing evidence file | period / year end | per FR-40.4 and FR-39.17 | CSV (OECD XML optional per open question 3) | none | local file / master file support 10 years |
| Consolidated financial statements | run completed | balance sheet, P&L, OCI, equity changes, cash flow (wave 15 designer) | PDF, XLSX, iXBRL / ESEF via wave 15 | run id + version | statutory group accounts; 10 years |
| Elimination and top-side journal listing | run completed | rule, entries, reasons, approvers | PDF, CSV | run id | 10 years |

### 7.40.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `ic-reconcile` | hourly + period close | (pair, period) | (pair, period, version) | < 2 min for 100k items | rerun | mismatches > 0 at close |
| `ic-saga-sweeper` | every 5 min | saga id | Temporal workflow id | < 30 s | compensate or alert | stuck > 10 min |
| `ic-onhand-projection` | continuous (events) | partner pair | event offset | lag < 60 s | replay from offset | lag > 5 min |
| `ic-planning-run` | nightly 01:00 before entity MRP | planning group | (group, run date) | < 60 min for 5 entities | entity-level retry; plan marked stale | stale supplier plan |
| `regrouping-run` | period end (close checklist task) | legal entity | (entity, period, version) with reversal | < 5 min for 200k open items | rollback and retry | not run by close day 2 |
| `regrouping-reversal` | first calendar day of period | legal entity | (run id) | < 2 min | retry | reversal missing |
| `consol-run-scheduled` | on close checklist task | (group, period) | (group, period, version) | < 5 min for 50 units × 10k accounts | task-level error, run Failed | run failure |
| `equity-method-run` | inside consolidation run | investment | (investment, period) | < 1 min | task Error | missing associate data |
| `ic-balance-confirmation` | period close day 3 | pair | (pair, period) | < 2 min | resend | unconfirmed after 5 days |

### 7.40.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| IntercompanyView | GET /interco/*, chain view, IC on-hand, reconciliation read | none | IC accountant, Controllers of both entities |
| IntercompanyConfigure | /partners, matrix, value maps, pricing, centralized payment setup, planning groups | IntercompanyPost, IntercompanyReconcile | Group finance system admin |
| IntercompanyPost | /interco/journals | IntercompanyReconcile (same pair and period) | IC accountant |
| IntercompanyReconcile | /reconciliations/*, resolve | IntercompanyPost | Group accountant |
| IntercompanySystem | /mirrors, /charges (service accounts only) | n/a | interco-svc, project-svc, expense-svc |
| IntercompanyPlan | /planning-groups/*/run | none | Supply chain planner (group) |
| ConsolidationConfigure | /consol/groups, investments, chart mapping, rules, validation rules | ConsolidationRun, ConsolidationApprove | Consolidation lead |
| ConsolidationUnitRelease | /units/*/release | ConsolidationApprove for the same unit | Entity controller |
| ConsolidationRun | /consol/runs, reclassification runs | ConsolidationConfigure | Consolidation accountant |
| ConsolidationJournal | /group-journals | ConsolidationApprove for own journals | Consolidation accountant |
| ConsolidationApprove | override Mismatch gate, approve top-side ≥ threshold, re-collect released unit | ConsolidationJournal, ConsolidationRun | Group CFO |
| ConsolidationView | GET /consol/*, monitor, statements | none | Group controller, Auditor |

### 7.40.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| IC-0801 | error | No active IC partner policy between `{a}` and `{b}` | missing / inactive pairing | create or activate partner | FR-40.1 |
| IC-0802 | error | Mirror creation failed at `{step}`; source document canceled | saga compensation | fix cause (number sequence, dimension) and recreate | BR-40.1 |
| IC-0803 | error | Compensation failed for pair `{pairId}`; manual action required | Failed state | runbook manual compensation | BR-40.1 |
| IC-0804 | warning | Change exceeds partner tolerance; mirror PO sent for re-approval | BR-40.3 | approver acts | BR-40.3 |
| IC-0805 | error | No IC accounting matrix entry for `{a}` → `{b}` | missing due-to / due-from | add matrix row | BR-40.4 |
| IC-0806 | error | Mirror entity `{b}` period `{period}` is closed | period control | open period or date | FR-40.2 |
| IC-0810 | warning | Reconciliation item `{id}` moved from Timing to Mismatch | counterpart missing after next period | resolve | BR-40.5 |
| IC-0811 | info | Centralized payment settled `{n}` invoices of `{entity}` | FR-40.12 | none | FR-40.12 |
| IC-0815 | warning | Supplier plan for `{entity}` is stale after buyer replanning | FR-40.15 | rerun group | FR-40.15 |
| IC-0816 | error | Planning group has a cycle without explicit sequence | BR-40.13 | define sequence | BR-40.13 |
| IC-0821 | error | No transfer price for `{category}` / `{role}` on `{date}` | BR-40.12 | add price | BR-40.12 |
| IC-0825 | warning | Partner on-hand for `{entity}` is `{s}` s old | projection lag | none | FR-40.14 |
| CNS-0901 | error | Consolidation blocked: `{n}` unresolved IC mismatches | FR-40.6 gate | resolve or override | FR-40.6 |
| CNS-0902 | error | Missing `{rateType}` rate for `{currency}` on `{date}` | rates missing | load rates (wave 12) | FR-40.8 |
| CNS-0903 | error | Rule `{rule}` unbalanced by `{amount}` | elimination defect | run fails; report defect | BR-40.9 |
| CNS-0904 | error | Ownership for `{unit}` exceeds 100.00% on `{date}` | invalid ownership | correct ownership | FR-40.7 |
| CNS-0905 | error | IC difference `{diff}` exceeds threshold `{t}` for `{pair}` | BR-40.15 | resolve in IC reconciliation | BR-40.15 |
| CNS-0906 | error | Validation `{rule}` failed for `{unit}`: `{difference}` | FR-40.20 | correct unit data and re-release | FR-40.20 |
| CNS-0907 | error | Unit `{unit}` data is released; re-collection needs approval | BR-40.17 | request approval | BR-40.17 |
| CNS-0908 | warning | Group cost unknown for `{n}` layers; margin fallback used | FR-40.16 | roll Group view | FR-40.16 |
| CNS-0909 | error | Top-side journal ≥ `{threshold}` requires approval | FR-40.19 | approver acts | FR-40.19 |
| CNS-0910 | error | Change of control requires step-acquisition accounting, not supported | ownership crosses 50% from an existing interest | post manual top-side entries (FR-40.19) | FR-40.18 |

### 7.40.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Mirror saga | 5,000 IC documents/day; peak 300/min at invoicing | 1.5 / 5 / 10 s | 10 sagas/s | 1.8M links ≈ 1 GB | IcDocumentLink by ic_pair_id and (mirror_entity_id, mirror_doc_id) | Temporal task queue backlog; p95 < 15 s; no lost sagas |
| IC charge posting | 50,000 charges/day (time + expense) | 200 ms / 800 ms / 2 s | 50/s | 18M charges ≈ 9 GB | partitioned monthly by created_at | batched by pair per minute |
| IC reconciliation run | 100k items per pair-period | n/a | < 2 min | 1.2M items ≈ 0.6 GB | (entity_a, entity_b, period, status) | < 6 min |
| IC on-hand query | 3,000/day | 30 / 120 / 250 ms | 50 req/s | read model ≈ size of partner on-hand | keyed (partner, item, site) | stale data with age shown |
| Consolidation run | 50 units × 10k accounts, 5 versions per close | n/a | < 5 min | 50 × 10k × 12 × 5 ≈ 30M entries ≈ 12 GB | ConsolidationEntry partitioned by period; index (run_id, account) | partial recompute for changed units; < 15 min |
| Drill-down consolidated → journal | 2,000/day during close | 200 ms / 800 ms / 1.5 s | 20 req/s | n/a | source_refs GIN index | paginated drill |
| Regrouping run | 200k open items per entity | n/a | < 5 min | 2.4M reclass lines ≈ 1 GB | open items by due date (wave 19 / 30 indexes) | < 15 min |

### 7.40.20 Localization and compliance hooks

- **Consolidation frameworks**: IFRS 10 / IFRS 3 / IAS 21 / IAS 28 (equity method) default; US GAAP ASC 810 / 830 / 323 differences (for example, NCI measured at fair value vs proportionate share, a choice under IFRS 3) are set per group; local GAAP group accounts (DE HGB §§290–315, FR règlement ANC 2020-01) through wave-48 pack chart mappings and statement layouts; ESEF iXBRL tagging for EU listed groups through the wave-15 designer.
- **Proportional consolidation** is dropped because IFRS 11 removed it for joint ventures (equity method applies); HGB §310 still allows it, so a DE pack may request it post-v1.
- **Transfer pricing**: OECD Guidelines master / local file and country-by-country reporting (CbCR, OECD XML schema) for groups with revenue ≥ EUR 750 million; Passage supplies the data export (FR-40.4, FR-40.13, FR-39.17); filing formats are wave-48 pack outputs (`TransferPricingReport` extension point).
- **IC invoicing tax**: cross-border IC supplies follow wave-13 VAT rules (EU intra-community supply, reverse charge); IC invoices fall under e-invoicing mandates where applicable (MX CFDI, IN IRP, FR / DE B2B schedules) through wave-48 packs.
- **Regrouping**: balance-sheet classification of current / non-current (IAS 1.66–1.76) and HGB §268 (residual terms > 1 year disclosed); the RemainingTerm rule threshold is a pack parameter (12 months default).
- **Retention**: consolidation runs, top-side journals and IC confirmations 10 years.
- **Currency**: translation of hyperinflationary units (IAS 29) out of v1; rounding per group currency minor unit (BR-40.9).

### 7.40.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX 12.2.1 Payments → Centralized payments (cross-company) | PARTIAL-40 | ADDED as FR-40.12 | |
| AX 12.10.1 Expense reports → Intercompany expenses (R3) | PARTIAL-40 | ADDED as FR-40.13 | expense UI arrives with wave 43 and calls the IC charge API |
| AX 12.10.3 Expense → Intercompany expense posting (due-to / due-from) | PARTIAL-40 | ADDED as FR-40.13 | Journal settlement mode |
| AX 12.11.3 Project → Intercompany project invoicing (intercompany timesheets / expenses) | PARTIAL-40 | ADDED as FR-40.13 | posting and invoicing contract here; project resource lending screens DEFERRED to wave 41 |
| AX 12.11.4 Intercompany → Intercompany project parameters (lending / borrowing, timesheet / expense / vendor invoice transfer) | PARTIAL-40 | ADDED as FR-40.13 | `ic.chargeSettlementMode`, `ic.transferPrices` |
| AX 12.11 Prices → Transfer prices (intercompany) (noted PARTIAL-40 in the COVERED-41 row) | PARTIAL-40 | ADDED as FR-40.13 | IcTransferPrice |
| AX 12.14.2 Inventory → Intercompany on-hand (R3) | PARTIAL-40 | ADDED as FR-40.14 | |
| AX 12.14.3 Inventory → Intercompany: on-hand sync | PARTIAL-40 | ADDED as FR-40.14 | event-driven projection, no batch sync |
| AX 12.21.1 Intercompany planned orders (R3 intercompany master scheduling) | PARTIAL-40 | ADDED as FR-40.15 | |
| AX 12.21.3 Intercompany master scheduling (run across entities) | PARTIAL-40 | ADDED as FR-40.15 | `ic-planning-run` |
| AX 12.21.4 Intercompany planning groups / members (sequence, plan) | PARTIAL-40 | ADDED as FR-40.15 | `ic.planningGroups` |
| SAP 13.7 CO versions for parallel valuation (TKA07, TKA09, TKA01) | PARTIAL-40 | ADDED as FR-40.16 (with FR-39.17) | |
| SAP 13.8 Transfer prices / multiple valuation views (OKKP, OKEQ group) | PARTIAL-40 | ADDED as FR-40.16 (with FR-39.17) | |
| SAP 13.9 8KEM / 8KEN Transfer prices: pricing procedures, conditions, variants | PARTIAL-40 | ADDED as FR-40.13 / FR-39.17 | profit-center valuation view in FR-39.17; IC pricing in FR-40.4 / FR-40.13 |
| SAP 13.10 Manage Reclassifications / Define Reclassification Rules | PARTIAL-40 | ADDED as FR-40.17 | |
| SAP 13.10 Consolidation of Investments (first, subsequent, step acquisition, divestiture, equity method) | PARTIAL-40 | ADDED as FR-40.18 | step acquisition OUT-OF-SCOPE for v1 (IFRS 3.42 remeasurement of the previously held interest is rare in the target segment); CNS-0910 directs users to top-side journals (FR-40.19) |
| SAP 13.10 Manage Investments / Equity data | PARTIAL-40 | ADDED as FR-40.18 | Investment entity |
| SAP 13.10 Consolidation monitor tasks (IC elimination incl. dividends, profit in inventory, CoI, group validation) | PARTIAL-40 | ADDED as FR-40.20 | dividends are E6 (FR-40.9); CoI FR-40.18 |
| SAP 13.10 Reclassification runs | PARTIAL-40 | ADDED as FR-40.17 | |
| SAP 13.10 Reclassification / elimination methods (two-sided, differences to FS item, thresholds, dividends) | PARTIAL-40 | ADDED as FR-40.17 | BR-40.15 |
| SAP 13.10 CoI methods (purchase, equity, proportional), activities | PARTIAL-40 | ADDED as FR-40.18 | proportional OUT-OF-SCOPE: IFRS 11 removed proportional consolidation for joint ventures (7.40.20) |
| SAP 13.1 FAGLF101 / F101 Regroup receivables / payables | GAP (proposed 14) | ADDED as FR-40.17 | runs as a wave-14 close checklist task; engine lives in consol-svc rules |
| SAP 13.1 FAGL_CL_REGROUP / FAGL_CL_MIGRATION Closing regrouping (S/4) | GAP (proposed 14) | ADDED as FR-40.17 | |
| SAP 13.1 FAGL_CL_RGRP sort method and adjustment accounts for regrouping | GAP (proposed 14) | ADDED as FR-40.17 | `consol.reclassRules` |
| SAP 13.2 FAGLF101 (AP) Regroup vendors with debit balances | GAP (proposed 14) | ADDED as FR-40.17 | DebitBalanceVendors rule |
| SAP 13.3 FAGLF101 (AR) Regroup customers with credit balances | GAP (proposed 14) | ADDED as FR-40.17 | CreditBalanceCustomers rule |
