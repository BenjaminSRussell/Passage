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
| `passage.treasury.statement.reconciled.v1` | treasury-svc | journal (wave 11), cashforecast-svc, analytics (46) | statementId, bankAccountId, matchedCount, unmatchedCount, closingBalance |
| `passage.treasury.cashposition.updated.v1` | cashforecast-svc | dashboards, interco-svc (netting) | legalEntityId, asOf, balancesByAccount[], forecastBuckets[] |
| `passage.assets.depreciation.posted.v1` | asset-svc | costacct-svc, analytics | runId, bookId, period, assetCount, totalAmount, journalEntryId |
| `passage.assets.asset.disposed.v1` | asset-svc | costacct-svc, tax (wave 13) | assetId, bookId, proceeds, nbv, gainLoss |
| `passage.budget.check.completed.v1` | budget-svc | procurement (17), AP (18), workflow | sourceDocumentId, result (pass/warn/fail), availableBefore, amount, availableAfter |
| `passage.controlling.allocation.completed.v1` | costacct-svc | budget-svc, analytics | runId, policyId, iterations, totalIn, totalOut, residual |
| `passage.costing.standardcost.activated.v1` | costing-svc | inventory costing (20), production (21), budget-svc | costingVersionId, itemCount, revaluationJournalId, totalRevaluation |
| `passage.costing.variance.posted.v1` | costing-svc | costacct-svc, analytics | productionOrderId, varianceByType{lotSize,quantity,price,substitution}, byComponent[] |
| `passage.interco.mirror.created.v1` | interco-svc | sales (27), procurement (17), consol-svc | sourceDocId, sourceEntity, mirrorDocId, mirrorEntity, type |
| `passage.consol.run.completed.v1` | consol-svc | reporting (15), analytics | runId, groupId, period, unitCount, eliminationCount, ctaAmount |

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

Commands: `ImportStatement` (validates schema, rejects duplicates), `RunMatching` (rule set must be Active), `ConfirmMatch` (amount tolerance ≤ rule tolerance), `CloseReconciliation` (difference = 0). Queries served from read models.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `passage.treasury.statement.imported.v1` | import committed | statementId, bankAccountId, lineCount, openingBalance, closingBalance | cashforecast-svc, treasury-svc matcher |
| `passage.treasury.statement.reconciled.v1` | reconciliation closed | statementId, matchedCount, createdCount, closingBalance | journal, analytics |
| `passage.treasury.check.voided.v1` | void | checkId, bankAccountId, number, reason | AP (19) positive pay |
| `passage.treasury.cashposition.updated.v1` | position refresh | legalEntityId, asOf, balances[] | dashboards, interco-svc |
| `passage.treasury.file.sent.v1` | outbound file acknowledged | connectionId, fileHash, channel, ackStatus | audit |

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

### 7.36.8 Acceptance criteria

- **AC-36.1** (FR-36.2) Given a camt.053 file with 1,000 entries, when imported, then 1,000 lines persist, opening + Σ lines = closing, and a re-upload returns the same statement id.
- **AC-36.2** (FR-36.3) Given the 1,000-line reconciliation fixture, when rules run, then ≥ 950 lines are auto-matched correctly and 0 are matched to the wrong transaction.
- **AC-36.3** (FR-36.5) Given a line for a USD 12.50 bank fee, when the user creates a transaction, then a journal entry Dr Bank fees 12.50 / Cr Bank 12.50 posts with the line as source document.
- **AC-36.4** (FR-36.5) Given a difference of 0.01, when closing, then the close is rejected with the difference displayed.
- **AC-36.5** (FR-36.6) Given check 1001 Printed, when voided and reprinted, then 1001 is Voided and 1002 is Printed with the same payee and amount.
- **AC-36.6** (FR-36.7) Given a facility of EUR 1,000,000 with EUR 950,000 utilized, when an LC of EUR 100,000 is created, then it is blocked without the override duty.
- **AC-36.7** (FR-36.9) Given 10,000 generated open items, when the forecast computes, then every bucket equals the sum of its contributions to the cent.
- **AC-36.8** (FR-36.12) Given an EBICS sandbox, when a pain.001 is sent, then the file hash, signature reference and bank acknowledgment are in the audit log and no private key bytes appear in any log or DB.

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

## 7.37 Wave 37: Fixed Assets & Leases

### 7.37.1 Scope and business context

Wave 37 adds the asset subledger. Capital spend currently lands on expense or clearing accounts; after this wave an accountant acquires assets from AP invoices, projects (CIP) or inventory, depreciates them monthly under several books (local GAAP, IFRS, tax) with different methods and conventions, transfers, splits, revalues, impairs and disposes of them with correct gain/loss, and accounts for leases under IFRS 16 / ASC 842 with right-of-use assets and lease liabilities. A physical inventory with barcode scanning reconciles the register to what exists.

Out of scope: enterprise asset maintenance (wave 44), tax depreciation reports beyond MACRS/ADS tables for the US and straight-line for other v1 countries (country packs in wave 48), component accounting for infrastructure-grade asset hierarchies deeper than 3 levels.

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

Validation: book must be Open and period open per ledger × module (wave 14); disposal percentage in (0,100]; lease IBR in (0, 50%].

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `passage.assets.asset.acquired.v1` | acquisition posted | assetId, bookIds[], cost, sourceType, sourceId | budget-svc, analytics |
| `passage.assets.depreciation.posted.v1` | run posted | runId, bookDefinitionId, period, assetCount, totalAmount, journalEntryId | costacct-svc, analytics |
| `passage.assets.asset.disposed.v1` | disposal posted | assetId, bookId, percentage, proceeds, nbv, gainLoss | tax, analytics |
| `passage.assets.lease.recognized.v1` | lease activated | leaseId, liability, rouAsset, termMonths, rate | analytics |

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

### 7.37.8 Acceptance criteria

- **AC-37.1** (FR-37.3) Given the USD 12,000 mid-month example, when the schedule is generated, then FY2026 = 1,900.00, FY2027–2030 = 2,400.00 each, FY2031 = 500.00.
- **AC-37.2** (FR-37.4) Given the USD 25,000 MACRS 5-year example, when computed, then the yearly amounts are 5,000.00 / 8,000.00 / 4,800.00 / 2,880.00 / 2,880.00 / 1,440.00.
- **AC-37.3** (FR-37.4) Given 100 fixture assets across methods and conventions, when depreciated for full lives, then every amount matches the reference table to the cent.
- **AC-37.4** (FR-37.5) Given 10,000 assets, when the monthly run posts, then one journal entry per book exists, Σ lines = Σ proposal, and a second run for the same period is rejected.
- **AC-37.5** (FR-37.8) Given cost 10,000, accumulated 4,000, proceeds 7,000, 50% disposal, when disposed, then cost −5,000, accumulated +2,000, gain 4,000 posts and the remaining NBV is 3,000.
- **AC-37.6** (FR-37.10) Given the 5-year USD 10,000 lease at 5%, when activated, then liability = 43,294.77, Y5 interest = 476.18 and closing = 0.00.
- **AC-37.7** (FR-37.10) Given 25 fixture leases, when schedules compute, then every line matches the accountant workbook to the cent.
- **AC-37.8** (FR-37.13) Given the demo entity after a month, when reconciliation runs, then Σ asset cost and Σ accumulated depreciation equal the GL accounts with 0.00 difference.

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

## 7.38 Wave 38: Cost Accounting, Budgeting & Budget Control

### 7.38.1 Scope and business context

Wave 38 gives controllers a planning and control layer separate from statutory accounting. Before it, the universal journal records actuals by dimension but there is no budget to compare against, no way to stop a purchase order that exceeds a department's funds, and no management view that reclassifies and allocates costs the way the business is run. After it, budget register entries and planning scenarios flow through workflow into budget models; budget control checks requisitions, POs and invoices against available funds, recording encumbrances and liquidating them exactly; a cost accounting ledger mirrors primary costs from the journal into cost objects and cost elements, runs assessment/distribution allocations that converge and reconcile, supports activity-based costing and internal orders, and feeds the management reporting cubes consumed by wave 46.

Out of scope: public-sector fund accounting (appropriations, apportionments, year-end encumbrance carry-forward beyond the simple option), profitability analysis segment reporting (CO-PA style, wave 46 semantic layer), project budgets (wave 41).

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

Validation: check requests must carry accounting date, ledger dimension and amount per line; allocation run rejects when period is closed in the cost ledger or a prior step's run for the period is missing.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `passage.budget.register.posted.v1` | entry posted | entryId, modelId, type, lines summary | balance projector, analytics |
| `passage.budget.check.completed.v1` | check | sourceDocumentId, lineResults[{result, available, amount}] | procurement, AP, workflow |
| `passage.budget.encumbrance.relieved.v1` | relief | encumbranceId, amount, invoiceId, remaining | analytics |
| `passage.controlling.allocation.completed.v1` | run complete | runId, policyId, iterations, totalIn, totalOut, residual | budget-svc, analytics |
| `passage.controlling.costentry.created.v1` | mirror or allocation | entryId, costObjectId, elementId, amount, origin | lakehouse |

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

### 7.38.8 Acceptance criteria

- **AC-38.1** (FR-38.4) Given budget 100,000.00 with actuals 60,000.00 and encumbrances 30,000.00, when a PO line of 12,000.00 is checked with warn 0% and block 5%, then the result is `fail` (available 10,000.00 < 12,000.00, over by 20%).
- **AC-38.2** (FR-38.4) Given the same balance, when a PO of 10,400.00 is checked, then the result is `warn` (over by 4%) and 10,400.00 is reserved.
- **AC-38.3** (FR-38.5) Given a PO line of 10 × 50.00 encumbered, when an invoice for 10 × 52.00 posts, then relief = 500.00 and a new check runs for 20.00.
- **AC-38.4** (FR-38.4) Given two concurrent POs of 8,000.00 against 10,000.00 available, when checked simultaneously, then exactly one passes.
- **AC-38.5** (FR-38.7) Given a reciprocal step with 3 service centers serving each other, when run, then Σ out = Σ in to the cent and the solution matches the analytic solve.
- **AC-38.6** (FR-38.11) Given a month of journal postings, when reconciliation runs, then Σ primary cost entries per element equal the journal balances with 0.00 difference.
- **AC-38.7** (FR-38.2) Given a plan with 3 scenarios and 2 stages, when approvers act, then the workflow completes and a register entry posts from the selected scenario.
- **AC-38.8** (FR-38.6) Given a journal entry posted, when 5 s elapse, then matching cost entries exist for every line with a primary element mapping.

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

## 7.39 Wave 39: Product Costing, Cost Roll-Up & Variance Analysis

### 7.39.1 Scope and business context

Wave 20 delivered the inventory costing engine with a standard cost method but no way to derive the standard: standards were keyed. Wave 39 computes them. Cost accountants define cost groups, costing sheets with indirect cost nodes, and costing versions; roll up costs from BOMs and routings per site and quantity with explosion modes and fallback rules; compare estimates; activate pending costs, which revalues on-hand inventory and posts the revaluation; and analyze purchase price and production variances decomposed into lot size, quantity, price and substitution by cost component. At the end of the wave the demo plant runs on standard cost with a monthly variance report.

Out of scope: actual costing / material ledger periodic unit price (wave 20 covers FIFO/weighted average), mixed costing across procurement alternatives, co-product cost allocation for process manufacturing beyond fixed percentage (wave 24 rules reused), sales-order costing for configured items.

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

Invariants: Σ CostComponentSplit.amount per calculation = unit_cost × calc_quantity (6 decimals); exactly one Active calculation per (version, item, site); Σ VarianceEntry per production order = actual − standard of RAF to the cent. Indexes: CostCalculation unique partial (version, item, site) where status = Active; VarianceEntry by (site_id, period, type).

### 7.39.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/costing/versions | Create version | Idempotency-Key | CostingMaintain |
| POST | /api/v1/costing/sheets | Create/version sheet | Idempotency-Key | CostingMaintain |
| POST | /api/v1/costing/calculations | Roll one item (explosion mode, qty) | (version, item, site, bom, route, qty) | CostingCalculate |
| POST | /api/v1/costing/versions/{id}/roll | Mass roll selection | runId | CostingCalculate |
| GET | /api/v1/costing/calculations/{id}/explain | Explanation tree with split | n/a | CostingView |
| POST | /api/v1/costing/versions/{id}/activate | Preview (dryRun=true) or activate | (version, date) | CostingActivate |
| GET | /api/v1/costing/variances?site=&period=&type= | Variances | n/a | CostingView |
| POST | /api/v1/costing/variances/compute | Compute for ended order (system) | productionOrderId+version | CostingSystem |
| GET | /api/v1/costing/compare?a=&b= | Version comparison | n/a | CostingView |

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `passage.costing.calculation.completed.v1` | item rolled | calculationId, itemId, siteId, unitCost, split[] | analytics |
| `passage.costing.rollrun.completed.v1` | mass roll finished | runId, itemCount, errorCount, durationMs | workspace |
| `passage.costing.standardcost.activated.v1` | activation | costingVersionId, itemCount, revaluationJournalId, totalRevaluation | inventory costing, production, budget |
| `passage.costing.variance.posted.v1` | variance computed | sourceId, type totals, byComponent[], journalEntryId | costacct-svc, analytics |

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

## 7.40 Wave 40: Intercompany & Financial Consolidation

### 7.40.1 Scope and business context

Wave 40 closes phase 7 and delivers finance parity. Groups with several legal entities trade with each other, share costs and must report as one. Before this wave each entity is an island: an intercompany sale is keyed twice, due-to/due-from balances drift, and consolidated statements are assembled in spreadsheets. After it, pairing a customer in entity A with a vendor in entity B makes every sales order, packing slip, invoice and payment create its mirror in the partner automatically (including direct-delivery chains); intercompany journal lines post due-to/due-from entries in both entities; a reconciliation workspace matches both sides and flags mismatches; and the consolidation service builds group structures with ownership percentages, collects balances per unit, translates under IAS 21, eliminates intercompany balances, transactions, unrealized profit in inventory and investments, computes non-controlling interest, and produces consolidated statements through the wave-15 reporting designer with drill-down to the entity journal.

Out of scope: equity-method accounting for associates (v1.x), complex multi-tier ownership changes mid-year (step acquisitions), statutory group tax, transfer-pricing calculation itself (hooks only: documented IC prices and margin evidence export).

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
- **FR-40.9 Eliminations** — Must. The system shall apply elimination rules in order: (E1) IC revenue/COGS and other P&L; (E2) IC receivables/payables and loans with translation differences to CTA; (E3) unrealized profit in inventory (upstream shared with NCI, downstream parent only); (E4) investment in subsidiary against equity at acquisition with goodwill; (E5) NCI share of current-year profit and equity; (E6) dividends within group. Each produces ConsolidationEntry lines linked to the source balances.
  - Notes: worked 2-entity example (group currency USD; P owns 80% of S, S in EUR; closing rate 1.10, average 1.08, historical at acquisition 1.05). S sold goods to P for EUR 100,000 (cost EUR 70,000); 40% remain in P's inventory; S's receivable EUR 100,000 open; P's payable recorded at USD 108,500. E1: Dr Revenue 108,000.00 / Cr COGS 108,000.00 (100,000 × 1.08). E2: Dr AP 108,500.00, Dr CTA 1,500.00 / Cr AR 110,000.00 (100,000 × 1.10). E3: unrealized profit = 30,000 × 40% = EUR 12,000 × 1.08 = 12,960.00: Dr COGS 12,960.00 / Cr Inventory 12,960.00; upstream so NCI shares 20%: Dr NCI (equity) 2,592.00 / Cr NCI share of profit 2,592.00. E4: S share capital EUR 500,000 and pre-acquisition retained earnings EUR 200,000 at 1.05 = 735,000.00; P investment USD 620,000.00; Dr Share capital 525,000.00, Dr Pre-acquisition RE 210,000.00, Dr Goodwill 32,000.00 / Cr Investment in S 620,000.00, Cr NCI 147,000.00 (20% × 735,000). E5: S net income EUR 150,000 × 1.08 = 162,000.00; Dr Profit attributable to NCI 32,400.00 / Cr NCI (equity) 32,400.00. Each entry balances; the run reports Σ debits = Σ credits = 1,032,952.00 across eliminations E1–E5.
- **FR-40.10 Consolidation run** — Must. The system shall execute collection → translation → eliminations → NCI → validation as one versioned run per group/period with partial recompute for changed units, producing a consolidated trial balance and statements.
  - Notes: run of 50 units × 10,000 accounts in < 5 min; validation: consolidated balance sheet balances; Σ eliminations per rule balanced; no unresolved Mismatch items.
- **FR-40.11 Consolidated reporting and drill** — Must. The system shall expose the consolidated dataset to the wave-15 designer with drill from a consolidated line to unit balances, elimination entries and entity journal lines.

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

Validation: mirror saga rejects when the partner policy is inactive or the mirror entity's period is closed; consolidation run rejects unresolved Mismatch items unless override; rates must exist for every unit currency.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `passage.interco.mirror.created.v1` | saga linked | icPairId, sourceDocId, sourceEntity, mirrorDocId, mirrorEntity, type | sales, procurement, analytics |
| `passage.interco.mirror.compensated.v1` | saga failed | icPairId, failedStep, reason | workflow, alerts |
| `passage.interco.journal.posted.v1` | IC journal both sides posted | icPairId, entityA, entityB, amount, currency, journalIds[] | reconciliation, consol-svc |
| `passage.interco.reconciliation.flagged.v1` | mismatch | itemId, entities, difference | workspace, workflow |
| `passage.consol.run.completed.v1` | run completed | runId, groupId, period, version, unitCount, eliminationCount, ctaAmount | reporting, analytics |

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
