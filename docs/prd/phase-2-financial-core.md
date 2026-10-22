# Part 2 — Phase 2: Financial Core (waves 11–15)

## 2.0 Phase overview

**Business outcome at phase exit.** At the end of wave 15 a controller closes a fiscal period for the demo manufacturer's three legal entities entirely inside Passage: every financial fact lives in one append-only universal journal (`ledger` service, Go + Postgres) that balances by construction; each line carries transaction, accounting and reporting currency amounts and is posted simultaneously to the leading, local-GAAP and tax ledgers; tax is determined and settled to the cent; periods are controlled per ledger × module; allocations, accruals and year-end carry-forward are automated; and the balance sheet, income statement and cash flow statement are generated from the journal with drill-down to the originating document. Every later module (inventory, AP, AR, assets, projects) posts only through the wave 11 posting API — there is no second path to the general ledger.

**Phase-exit sign-off criteria**
- Trial balance for each demo entity and each ledger sums to 0.00 in all three currencies after 1M synthetic lines; balance projection equals a full re-aggregation of the journal (zero rows differ).
- Posting API sustains 5,000 lines/s for 30 min on the dev cluster with p99 < 200 ms; duplicate `Idempotency-Key` yields a byte-identical 200 response.
- Parallel-ledger fixture (20 documents) posts per the matrix in §2.12.7 and each ledger's trial balance is zero.
- 500-scenario tax matrix (US/EU/IN/CA) matches expected results; settlement run ties to GL tax accounts to the cent.
- One simulated period closed across 3 entities with a 40-task checklist, dependencies honored; year-end carry-forward produces opening balances equal to closing balances per dimension combination.
- Statement pack (TB, BS, P&L, CF) ties to the trial balance to the cent; cash flow reconciles to the change in cash; finance stakeholders record sign-off in the wave 15 issue.

**Cross-wave data model summary**

| Entity | Owning service | Key relationships |
|---|---|---|
| Ledger, LedgerGroup | ledger | LegalEntity (wave 6) 1:N Ledger; Ledger → FiscalCalendar, AccountingCurrency, ReportingCurrency |
| MainAccount, AccountStructure, DimensionAttribute, DimensionValue, DimensionCombination | ledger | ChartOfAccounts 1:N MainAccount; AccountStructure → ordered DimensionAttributes; DimensionCombination = hashed tuple of DimensionValues |
| JournalEntry, JournalLine, JournalBatch, JournalBatchLine | ledger | JournalEntry 1:N JournalLine; JournalEntry N:1 Ledger; JournalBatch (unposted) → JournalEntry (posted); SourceDocument reference (URI) |
| Balance (projection) | ledger | Keyed by ledger × account × DimensionCombination × fiscal period × currency type |
| ExchangeRateType, ExchangeRate, CurrencyPolicy | fx | ExchangeRate → (type, from, to, valid_from); Ledger → ExchangeRateType default |
| RevaluationRun, RevaluationLine | ledger | Produces JournalEntry pairs (posting + reversal) |
| TaxCode, TaxRate, TaxGroup, ItemTaxGroup, TaxJurisdiction, ExemptionCertificate, WithholdingTaxCode | tax | TaxGroup N:M TaxCode; ItemTaxGroup N:M TaxCode; TaxRate effective-dated per TaxCode × Jurisdiction |
| TaxTransaction, TaxSettlementRun | tax | TaxTransaction → JournalLine (posted tax line); SettlementRun → JournalEntry |
| PeriodStatus, CloseTemplate, CloseTask, CloseRun | close (TS/Temporal) + ledger (PeriodStatus) | PeriodStatus keyed ledger × period × module; CloseRun → CloseTasks → assignees (Party/Employee role) |
| AllocationRule, AllocationRun, AccrualScheme, RecurringJournal | ledger | Runs produce JournalEntry; rules reference AccountStructure filters |
| ReportDefinition, ReportRow, ReportColumn, ReportTree, StatementRun, StatementCell | reporting | ReportDefinition → MainAccount ranges + DimensionValue filters; StatementCell → Balance keys → JournalLines |

**Cross-wave event catalog**

| Event (CloudEvents type) | Producer | Consumers | Payload summary |
|---|---|---|---|
| dev.passage.ledger.journal.posted.v1 | ledger | reporting, tax, audit, lakehouse, all subledgers (waves 16+) | entry id, ledger id, document id, posting date, period, line count, totals per currency type, source ref, trace id |
| dev.passage.ledger.journal.reversed.v1 | ledger | reporting, tax, subledgers | original entry id, reversing entry id, reason code |
| dev.passage.ledger.balance.rebuilt.v1 | ledger | reporting, ops alerting | ledger id, high-water sequence, rows, diffs |
| dev.passage.fx.rate.published.v1 | fx | ledger, reporting, AP/AR (later) | rate type, pair, valid_from, rate, source, previous rate |
| dev.passage.fx.provider.degraded.v1 | fx | ops alerting | provider, last success, fallback mode |
| dev.passage.ledger.revaluation.completed.v1 | ledger | reporting, close | run id, ledger, period, unrealized gain/loss totals, entry ids |
| dev.passage.tax.calculated.v1 | tax | document services (waves 17+, 26+) | document ref, lines, tax amounts, provider used, fallback flag |
| dev.passage.tax.settled.v1 | tax | reporting, ledger | settlement period, payable/receivable totals, entry id |
| dev.passage.close.period.status-changed.v1 | ledger | all posting services, close, reporting | ledger, period, module, old → new status, actor, approvals |
| dev.passage.close.task.completed.v1 | close | close board, notifications | run id, task id, assignee, evidence link |
| dev.passage.ledger.allocation.completed.v1 | ledger | reporting, close | run id, rule ids, iterations, entry ids |
| dev.passage.ledger.year-end.closed.v1 | ledger | reporting, close | ledger, fiscal year, opening entry ids, retained earnings per dimension |
| dev.passage.reporting.statement.generated.v1 | reporting | distribution, audit | run id, definition id, period, hash, output URIs |

**Phase-level risks**

| Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|
| Balance projection drifts from journal under concurrent posting | Medium | High | Projection updated in the same Postgres transaction as the entry; nightly rebuild-and-compare job; drift alert at 1 row | ledger lead |
| Rounding differences across three currencies and N ledgers accumulate | Medium | High | ISO 4217 minor-unit rules in one shared Go package `money`; property tests over JPY/KWD/USD/EUR; rounding-difference account with tolerance | ledger lead |
| Tax engine correctness outside fixture coverage | High | High | Provider adapters (Avalara/Vertex) as production default for US; internal engine for VAT with accountant-reviewed fixtures | tax lead |
| Period reopened silently | Low | High | Reopen requires two distinct approvers (Temporal human tasks) and emits an audit event; no API bypass | close lead |
| Reporting projection lags and statements are stale at close | Medium | Medium | Statement run records the journal high-water sequence it covered; close task blocks until lag = 0 | reporting lead |
| Accountant review capacity (two-person team) | High | Medium | Fixtures reviewed per wave by an external practicing accountant; sign-off recorded in the wave issue | product |

## 2.11 Wave 11: Universal Journal & Chart of Accounts

### 2.11.1 Scope and business context

AX stores financial truth across `GeneralJournalEntry`/`GeneralJournalAccountEntry` with `DimensionAttributeValueCombination` as the account key, and SAP consolidated FI/CO into `ACDOCA`. Both rely on a posting layer that every module calls. Wave 11 builds that layer first: one append-only, double-entry journal per ledger, financial dimensions validated by account structures, a posting API with idempotency keys, a balance projection, and a journal workspace for manual, recurring and template journals with approval and attachments.

At the end of the wave a controller can define a chart of accounts and dimensions, post a manual journal (with approval), reverse it, and see the trial balance update in real time. Subledgers (waves 16+) will call `POST /api/v1/ledger/journal-entries` and nothing else.

Out of scope: multiple currencies beyond a single accounting currency (wave 12), tax lines (wave 13), period status enforcement beyond open/closed defaults (wave 14), statements (wave 15), intercompany (wave 40), budget control (wave 38).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX `LedgerJournalTable` form: posting a 2,000-line journal runs `LedgerJournalCheckPost` synchronously; validation errors surface one at a time in the Infolog after minutes of processing, forcing repeated post attempts | Validation and posting interleaved in X++ with per-line dimension lookups against `DimensionAttributeValueCombination` | `POST /ledger/validate` returns every violation with `line_index` in one pass; posting is a single atomic transaction | Validation of 2,000 lines < 1 s (vs 2–5 min); all errors in one response (vs 1 per attempt) |
| SAP FB50/F-02: a document that fails mid-posting (dump, lock on `NRIV` number range) can consume a document number or leave a parked `VBKPF` that is re-keyed | Number assignment and insert into `BKPF`/`BSEG` are not one atomic unit with the application-level checks | Gap-free `posting_sequence` assigned inside the same Postgres transaction; idempotency key makes retries safe | 0 lost numbers; 0 duplicate postings under crash test; retry needs 0 user action |
| Integrations double-post when a timeout hides a success (AIF/IDoc retries, Data management framework re-runs) | No idempotency contract on `LedgerJournalTrans` import or BAPI_ACC_DOCUMENT_POST | `Idempotency-Key` header with 400-day retention; same key + same payload → replay, different payload → 409 | Duplicate-post incidents 1–3/month typical → 0 |
| AX trial balance list page / SAP FAGLB03 are read from totals tables (`FAGLFLEXT`) that drift from line items after failed batches; reconciliation jobs (RFINDEX) run for hours | Totals maintained by separate update paths from line inserts | `Balance` upserted in the posting transaction; rebuild-and-compare job emits a diff count | Balance vs journal drift = 0 rows; rebuild of 10M lines < 15 min (vs multi-hour RFINDEX) |
| Reaching the source document from a GL line takes 6–10 clicks (AX voucher transactions → origin → subledger; SAP FB03 → environment → document flow) | Linkage stored in `SubledgerVoucherGeneralJournalEntry` / `BKPF-AWKEY` with per-module resolution logic | Every entry carries `source.uri`; UI resolves it as one link; `trace_id` on every entry | Clicks to source 8 → 1; any posting traceable through Tempo end-to-end |
| External systems (MES, BI, PLM) need custom X++/ABAP or middleware to learn that a posting happened | No event stream from the ledger; polling on `GeneralJournalEntry`/`ACDOCA` timestamps | `dev.passage.ledger.journal.posted.v1` on Kafka via outbox, AsyncAPI-described, plus REST/MCP | Integration lead time weeks → same day; event lag p95 < 2 s (vs 15-min+ polling) |

### 2.11.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Chart of accounts and main accounts | `MainAccount`, `LedgerChartOfAccounts`, form MainAccountListPage | `SKA1` (chart), `SKB1` (company-code view), FS00, OB13 | `ledger.MainAccount`, `ledger.ChartOfAccounts`; screen Chart of accounts | full |
| Financial dimensions | `DimensionAttribute`, `DimensionAttributeValue`, `DimensionFinancialTag` | CO objects (cost center `CSKS`, profit center `CEPC`), ACDOCA fields `RCNTR`, `PRCTR`, `RBUSA` | `ledger.DimensionAttribute`, `ledger.DimensionValue` (up to 20 per ledger) | full |
| Account structures / dimension validation | `DimensionHierarchy` (account structure), advanced rules | Field status groups (OBC4), validation/substitution (OB28/GGB0) | `ledger.AccountStructure` with ordered constraint rows | full |
| Ledger account combination | `DimensionAttributeValueCombination` (`RecId` as LedgerDimension) | ACDOCA field combination (no single key) | `ledger.DimensionCombination` (SHA-256 of ordered tuple) | improved: content-addressed, immutable |
| Journal entry / lines | `GeneralJournalEntry`, `GeneralJournalAccountEntry`, `SubledgerVoucherGeneralJournalEntry` | `BKPF`/`BSEG`, `ACDOCA` | `ledger.JournalEntry`, `ledger.JournalLine` | full |
| Manual journals (unposted) | `LedgerJournalTable`/`LedgerJournalTrans`, LedgerJournalTable form, journal names `LedgerJournalName` | FB50/F-02 park (`VBKPF`/`VBSEG`), FV50 | `ledger.JournalBatch`, `ledger.JournalBatchLine`; screen Journals | full |
| Recurring / periodic journals | Periodic journals (`LedgerJournalTable.JournalType = Periodic`) | FBD1 recurring entries (`BKDF`) | `ledger.RecurringJournal` (template + schedule) | full |
| Journal approval | Ledger journal workflow `LedgerJournalApproval` | Parked document workflow (WS10000051) | Temporal approval workflow template (wave 4) bound to JournalBatch | full |
| Reversal | `LedgerJournalTrans` reverse transaction, `TransactionReversalTrans` | FB08, FBRA (reset and reverse) | `POST /journal-entries/{id}/reverse` creating negating entry | full |
| Voucher numbering | `NumberSequenceTable` per journal name | Document number ranges per document type (FBN1, `NRIV`) | Number sequence service (wave 5), gap-free per ledger × document type | full |
| Trial balance / balances | `GeneralJournalAccountEntry` aggregation, Trial balance list page, `LedgerBalance...` | `FAGLFLEXT` (totals), `GLT0`, FAGLB03 | `ledger.Balance` projection; Trial balance screen | full |
| Posting definitions / posting profiles | Posting definitions, `LedgerPostingSetup` | Account determination (OBYC, T030) | Deferred to the owning subledger waves; wave 11 exposes only account + dimension resolution hooks | partial (by design) |
| Financial reason codes | `ReasonTable`, `ReasonTableRef`, Reason codes form (Organization administration > Setup > Financial reasons); reversal reason prompt in `LedgerTransReverse` | `T041C` reasons for reversal (OB_REVERSE), negative-posting flag, alternative posting date | `ledger.ReasonCode` with `applies_to[]` modules; required on reversal when the journal name demands it (FR-11.15) | full |
| Advanced rule structures | `DimensionHierarchy` (type Advanced rule structure), `DimensionAdvancedRule`, `DimensionRuleCriteria`, form "Advanced rules" | Field status groups (OBC4) and validations only; no conditional segment model | `ledger.AdvancedRule` (filter → appended structure segments) (FR-11.16) | improved: versioned with the structure |
| Default descriptions (transaction text) | Default descriptions form (`TransactionTxt`), placeholders `%1`–`%3` per transaction type and language | Header text `BKPF-BKTXT` / line text `BSEG-SGTXT` set by substitution exits | `ledger.DescriptionTemplate` per source_type × language (FR-11.17) | full |
| Open-item managed G/L accounts and clearing | Not available for ledger accounts (only customer/vendor settlement and bank reconciliation) | `SKB1-XOPVW` open-item management, F-03/F-04 manual clearing, F.13/F13E automatic clearing, FBRA reset, FAGL_ACTIVATE_OP / FAGL_SWITCH_TO_OPEN_ITEM | `ledger.OpenItem`, `ledger.ClearingDocument`, clearing rules, tolerance groups (FR-11.19, FR-11.20); reused by waves 18/19/30 | full |
| User-defined validation rules | Journal controls (`LedgerJournalControl`), main account validation (currency, user, posting type) | OB28 / GGB0 validations (`GB90*`, `T001Q`) with message classes at header/line/document | `ledger.ValidationRule` (prerequisite → check → message, CEL) (FR-11.21) | full |
| Dimension derivation / substitution | Default dimensions on master data; D365 default dimension templates; no rule engine | OBBH / GGB1 substitutions (`GB91*`, `T001R`), KEDR derivation strategy (`TKEDR*`, `K9R*`), 0KEM/0KEP PCA substitution (`T8A*`), FMDERIVE strategy | `ledger.DerivationStrategy` with ordered steps (table lookup, move, clear, expression, constant) applied before structure validation (FR-11.22) | full |
| Functional area / cost-of-sales accounting | Custom dimension attribute | OKBD functional areas (`TFKB`), FAGL_FUNC_AREA activation, OBBZ/OBBW derivation, `ACDOCA-RFAREA` | Standard dimension attribute `FUNC_AREA` with a shipped lookup-table derivation step (FR-11.22) | full |
| Document splitting | Not available (account structures require dimensions per line; no inheritance to AP/AR/tax lines) | FAGL_SPLIT*: item categories, splitting method 0000000012, zero-balance clearing account, constants (`T8G01`–`T8G40`) | `ledger.SplittingRule` per source_type: proportional inheritance of splitting dimensions to balance-sheet and tax lines plus zero-balance clearing lines (FR-11.23) | full |
| Dimension realignment | Not available (manual reclassification journals) | KEND / KEDR realignment run (CO-PA), re-derivation of characteristics on history | `ledger.RealignmentRun` posting reclassification entries, append-only (FR-11.24) | improved: no in-place update of history |
| Attachments with document types | `DocuRef`, `DocuType`, `DocuValue`, Document handling form | GOS attachments (`SRGBTBREL`), ArchiveLink (OAC0/OAC3) document types | Wave 5 document framework: `document_type`, retention class, store reference on every attachment (FR-11.18) | full |

### 2.11.3 Functional requirements

- **FR-11.1 Ledger and chart of accounts** — Must. The system shall maintain one or more ledgers per legal entity, each bound to a chart of accounts, a fiscal calendar (wave 6), and an accounting currency.
  - A chart of accounts may be shared across legal entities (SAP `SKA1` semantics); company-specific attributes (`SKB1` semantics: suspended, posting allowed, default dimensions) live on `MainAccountLedgerSetting`.
  - Main account id is 1–20 characters, alphanumeric, unique per chart; type ∈ {asset, liability, equity, revenue, expense, profit_and_loss_total, balance_sheet_total, reporting}; totals and reporting types are non-postable.
  - Maximum 50,000 main accounts per chart; 500 charts per tenant.
- **FR-11.2 Financial dimensions** — Must. The system shall support up to 20 dimension attributes per ledger, each backed either by a Passage entity (department, cost center, site, project, customer, vendor, item) or by a custom value list.
  - Dimension values are effective-dated (`active_from`, `active_to`) and may be suspended; values from backing entities are synchronized by consuming that entity's CloudEvents.
  - Default dimensions on master data (party, product, site) are stored as a `DimensionCombination` id and merged at posting time by precedence: explicit line > source document header > master data default > account default.
- **FR-11.3 Account structures and validation** — Must. The system shall validate every line's (main account, dimensions) tuple against the ledger's active account structures before posting.
  - An account structure is an ordered list of segments (main account first) with per-segment constraint rows: value ranges, lists, wildcards (`*`), and "blank allowed". Advanced rules add extra segments when a filter matches (AX advanced rule semantics).
  - Validation is deterministic: the first structure whose main-account filter matches is applied; no match → `LEDGER_STRUCTURE_NOT_FOUND`.
  - Structures are versioned with `effective_from`; a posting uses the version effective on the posting date.
- **FR-11.4 Dimension combinations** — Must. The system shall intern each validated tuple as an immutable `DimensionCombination` whose id is the SHA-256 of the canonical tuple `(structure_version_id, account_id, dim1_value_id, …, dim20_value_id)`.
  - Combinations are never deleted; display name is derived (`1010-DEPT01-CC200`).
  - Lookup by id is a primary-key read; lookup by tuple is a hash computation plus one read.
- **FR-11.5 Posting API** — Must. The system shall expose `POST /api/v1/ledger/journal-entries` that posts a balanced entry atomically or rejects it entirely, with an idempotency key.
  - Request: `Idempotency-Key` header (1–128 chars, scoped to tenant × legal entity), body per §2.11.5. Maximum 10,000 lines per entry; larger documents must be split by the caller with a shared `document_id`.
  - Response 201 with the posted entry; 200 on replay of the identical payload; 409 `LEDGER_IDEMPOTENCY_CONFLICT` if the same key is reused with a different payload hash; 409 `LEDGER_IDEMPOTENCY_IN_PROGRESS` with `Retry-After: 1` while the first request is still in flight.
  - 422 with a `violations[]` array, each `{code, line_index, field, message}`; codes in BR-11.9. Posting never partially succeeds.
  - Idempotency records are retained 400 days; replays after expiry are treated as new requests (documented contract).
- **FR-11.6 Append-only journal** — Must. The system shall never update or delete a posted `JournalEntry` or `JournalLine`; corrections are new entries.
  - Enforced by Postgres: the application role has INSERT and SELECT only on `journal_entry`/`journal_line`; a trigger raises on UPDATE/DELETE for all roles.
  - Every entry receives a gap-free `posting_sequence` (bigint) per ledger from the number sequence service in gap-free mode, assigned inside the posting transaction.
- **FR-11.7 Balance projection** — Must. The system shall maintain `ledger.Balance` rows keyed by (ledger, account, dimension combination, fiscal period, currency type) and update them in the same database transaction as the posting.
  - Fields: `debit`, `credit`, `net`, `line_count`, `last_posting_sequence`; period-to-date only; year-to-date and opening balances are computed at query time by summing periods (opening balances live in period 0, see wave 14).
  - A rebuild job (§2.11.7 BR-11.7) reconstructs the table from the journal and reports differences.
- **FR-11.8 Reversal** — Must. The system shall create an exact negating entry for any posted entry via `POST /journal-entries/{id}/reverse`.
  - Reversal date defaults to the original posting date; may be any open date ≥ original. Each reversing line has `debit/credit` swapped and `reversal_of_line_id` set.
  - An entry can be reversed once; reversing a reversal is permitted (re-instating), each linked to its predecessor.
- **FR-11.9 Journal workspace (manual journals)** — Must. The system shall provide journal batches (AX `LedgerJournalTable` semantics) with lines, totals, validation on save, submit-for-approval, and post.
  - Journal names carry: voucher number sequence, default offset account, approval workflow id, `posting_restriction` (own journals only / by user group), and `lines_limit` (default 5,000).
  - Batch statuses: draft → validated → in_approval → approved → posting → posted | rejected | cancelled (state machine in §2.11.7).
- **FR-11.10 Recurring journals and templates** — Should. The system shall allow a posted or draft batch to be saved as a template (amounts or percentages) and a template to be scheduled (cron expression, fiscal-period-relative dates) to generate batches.
  - Generation runs on Temporal at the scheduled time; generated batches enter the approval workflow unless the template is flagged `auto_post` and the user who scheduled it holds duty `Post ledger journals without approval`.
- **FR-11.11 Attachments and notes** — Should. The system shall attach files (max 25 MB each, 50 per batch) and notes to batches and carry references to the posted entry.
- **FR-11.12 Source document linkage** — Must. The system shall record on each entry a `source` object `{system, type, id, uri}` and on each line an optional `source_line_ref`, enabling drill-down from statements (wave 15) to the originating document.
- **FR-11.13 Opening balances load** — Must. The system shall accept opening balances through the migration framework (wave 10) as a journal entry in period 0 of the first fiscal year, balanced against a conversion-suspense account that must be zero after load.
- **FR-11.14 Query API** — Must. The system shall expose line and balance queries with filters on ledger, account ranges, dimension values, posting date and period, paginated by keyset cursor (max 1,000 rows per page).
- **FR-11.15 Financial reason codes** — Must. The system shall maintain one shared `ReasonCode` list per tenant (code ≤ 10 characters, name, `applies_to[]` ⊆ {ledger, bank, customer, vendor, fixed_asset, inventory_counting, tax, project}, `requires_comment`, `negative_posting_allowed`, `alternative_posting_date_allowed`, `active`) that later modules reuse, and shall require a reason code on journal reversal when the journal name or the ledger parameter `reversal_reason_required` is set.
  - The reverse request carries `reason_code` and optional `reason_comment` (≤ 255 characters); both are stored on the reversing entry and emitted in `journal.reversed.reason_code`.
  - `negative_posting_allowed` (SAP `T041C` semantics, AX "Correction" parameter): the reversing lines are flagged `negative_posting = true` and the `Balance` projection reduces the original side (debit −100) instead of adding to the opposite side (credit +100); trial balance totals of debits and credits therefore return to their pre-posting values.
  - `alternative_posting_date_allowed = false` forces the reversal date to equal the original posting date when that period is open.
  - Maximum 500 codes per tenant; a referenced code cannot be deleted, only deactivated.
- **FR-11.16 Advanced rule structures** — Must. Beyond FR-11.3, the system shall maintain advanced rules: a filter over (main account ranges, dimension values, wildcards) that, when it matches a line, appends an advanced rule structure (additional segments with their own constraint rows) to the account structure applied to that line.
  - Several advanced rules may match; their structures are appended in `priority` order; the resulting segment list is capped at 20 attributes (`LEDGER_ADVANCED_RULE_SEGMENT_LIMIT`).
  - Advanced rules and advanced rule structures share the account structure's version and activation step (AX "Activate" on the structure); activation re-validates the 1,000 most recently used combinations under the new version and lists those that would become invalid before the user confirms.
- **FR-11.17 Default descriptions** — Should. The system shall maintain description templates keyed by (source_type, legal entity or tenant, language) with placeholders `%1` … `%9` bound to named fields (`{voucher}`, `{party_name}`, `{document_date}`, `{amount_txn}`, `{currency_txn}`, `{source_id}`, `{period}`), applied when a posting request omits `description` or a line omits `text`.
  - Substituted text is truncated to 255 characters with a trailing ellipsis; language resolves from the legal entity default language (wave 6), or from the user's UI language for manual journals when the journal name sets `description_language = user`.
  - Legal-entity templates override tenant templates (AX Default descriptions per company semantics).
- **FR-11.18 Attachment document types and retention** — Should. Attachments (FR-11.11) shall carry a `document_type` from the wave 5 document framework (shipped types `invoice_image`, `approval_evidence`, `supporting_calculation`, `correspondence`, `other`), a retention class (default `financial_record_10y`), and a store reference (object-storage key or external DMS URI), and shall remain readable from the posted entry after the batch is posted.
  - Attachments become immutable when the batch reaches `posted`; deleting an attachment before its retention class expires is rejected with `LEDGER_ATTACHMENT_RETAINED`.
  - Maximum 25 MB per file, 50 per batch, as in FR-11.11; virus scanning and content-type allow-list are inherited from wave 5.
- **FR-11.19 Open-item managed G/L accounts and clearing** — Must. The system shall allow `MainAccountLedgerSetting.open_item_managed = true` on balance-sheet accounts (clearing accounts, bank sub-accounts, GR/IR, suspense, payroll clearing); every posted line on such an account creates an `OpenItem`; open items are cleared by a `ClearingDocument` grouping items whose Σ(debit − credit) in the clearing currency is 0 within the applicable tolerance.
  - Manual clearing (SAP F-03 semantics): the user selects items (filters: assignment, `source_line_ref`, document id, date range, amount, dimension values); the system shows the residual; a residual within the user's and account's tolerance group posts a difference line to the tolerance group's difference account; a residual outside tolerance either creates a residual item (new open item for the remainder, `residual_of_item_id`) or is rejected with `LEDGER_CLEARING_UNBALANCED`.
  - Post with clearing (F-04 semantics): a manual journal line on an open-item account may list `clear_item_ids[]`; clearing happens in the same posting transaction and fails the whole post on mismatch.
  - Reset clearing (FBRA semantics): reopens the items, marks the clearing document `reset` with a reason code (FR-11.15) and actor; if the clearing posted difference or FX lines, a negating entry is posted. Posted entries are never modified.
  - Activation on an account with history (FAGL_ACTIVATE_OP / FAGL_SWITCH_TO_OPEN_ITEM semantics): permitted only through an activation run that creates one opening open item per non-zero (dimension combination, transaction currency) balance dated the activation date and records the run id on the account setting; deactivation requires zero open items.
  - Clearing currency per account: `accounting` (default) or `transaction` (foreign-currency sub-accounts); FX differences on clearing post to the realized gain/loss accounts of the wave 12 `CurrencyPolicy` and are excluded from wave 12 revaluation (`revalue_level = open_item`).
  - AP and AR settlement (waves 19/30) and GR/IR clearing (wave 18) reuse this engine through the open-item contract in §2.11.5 with their own item sources; this wave ships the GL account source only.
- **FR-11.20 Automatic clearing rules and tolerance groups** — Must. The system shall define automatic clearing rules per (ledger, account range) as an ordered list of up to 5 criteria fields (`assignment`, `source_line_ref`, `document_id`, `dim.<attr>`, `amount_txn`, `posting_date` bucket) that group open items (SAP OB74 semantics), and tolerance groups (SAP OBA0 / AX "Maximum penny difference" semantics) with absolute and percentage limits per currency, assigned to accounts and to users.
  - The scheduled job `gl-auto-clear` (§2.11.16) evaluates each rule, clears every group whose Σ = 0 (or within the account tolerance when the rule sets `allow_tolerance`) as one `ClearingDocument` per group, and never creates residual items; a simulation mode returns the proposed groups.
  - The effective tolerance is the minimum of the user's and the account's group; service-account runs use the account group only.
- **FR-11.21 User-defined validation rules** — Must. The system shall evaluate ordered validation rules per ledger at header, line and document level, each with `prerequisite` (CEL boolean over header fields, line fields, dimension values, main account attributes, source, user groups), `check` (CEL boolean) and `message` (message-catalog code, severity error | warning, parameter bindings).
  - A failed error rule produces a 422 violation `{code, rule_id, line_index}`; a failed warning rule produces a `warnings[]` entry and, for manual batches, a confirmation step before submit.
  - Rules are effective-dated and versioned; evaluation adds ≤ 2 ms per 100 lines; the rule editor runs a rule against saved sample entries and shows the result per line.
  - AX journal controls and main-account validations (currency, user, posting type) and SAP OB28/GGB0 validations are shipped as rule templates.
- **FR-11.22 Dimension derivation and substitution rules** — Must. The system shall apply a per-ledger `DerivationStrategy` before account-structure validation: ordered steps of type `table_lookup` (source fields → target dimension through a validity-dated lookup table, e.g., (account category, cost center) → functional area), `move` (copy a field or dimension to another), `clear`, `expression` (CEL) and `constant`, each with an optional condition and an `overwrite` flag.
  - Precedence extends FR-11.2: explicit line value > step with `overwrite` > source document header > master data default > account default > step without `overwrite`.
  - Every derived value is recorded per line as `derived_by_step_ids[]`, shown in the journal line derivation trace and returned by `POST /ledger/validate`.
  - Functional area for cost-of-sales accounting is a standard dimension attribute `FUNC_AREA` with a shipped lookup-table step; the P&L-by-function statement in wave 15 reads it. Segment derivation from profit center (SAP FAGL_SEGM) is a shipped `table_lookup` step.
  - Lookup tables are maintained in the UI and by CSV import (≤ 100,000 rows per table); SAP KEDR / 0KEM / FMDERIVE strategies and OBBH substitutions migrate to steps (§2.11.11).
- **FR-11.23 Document splitting: dimension inheritance and zero balance** — Must. The system shall, per source_type, inherit a configured set of splitting dimensions (for example profit center, segment, business unit) from the lines that carry them to the lines that do not (payable, receivable, tax, cash discount, bank, rounding) in proportion to the carrying lines' amounts, and post zero-balance clearing lines so that every entry balances per splitting dimension value.
  - Item categories per account range (expense_revenue, payable, receivable, tax, cash, balance_sheet_other) and the method (`active` = proportional inheritance; `passive` = inherit from the open items being cleared, FR-11.19) are configuration; each ledger names a `zero_balance_clearing_account` and a `default_splitting_value` constant used when no carrying line exists.
  - Splitting runs after derivation (FR-11.22) and before structure validation; produced lines carry `split_of_line_id` and `inherited = true`; proportional rounding uses the BR-12.4 residual rule; a line that cannot be split and has no default raises `LEDGER_SPLIT_NO_SOURCE`.
  - A balance sheet by profit center or segment (wave 15 `BS-STD` with a tree over the splitting dimension) therefore balances per unit.
- **FR-11.24 Dimension realignment** — Should. The system shall provide a controlled restatement that, for a filter (ledger, account ranges, period range, dimension values), re-derives selected dimensions with the current derivation strategy or an explicit old → new value mapping and posts reclassification entries per (account, period, old combination, new combination) flagged `is_realignment = true`, without modifying any posted line.
  - Simulation lists affected combinations, line counts and amounts; posting requires duty `Run dimension realignment`, a reason code and an open or on-hold GL period with the runner in the allowed group; a run is reversible as a unit.
  - Realignment entries are ordinary entries for balances and statements and appear in the wave 15 audit trail as `kind = realignment_run`.

### 2.11.4 Data model

**MainAccount**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| chart_of_accounts_id | uuid | yes | FK ChartOfAccounts |
| account_number | text(20) | yes | unique per chart; `^[A-Za-z0-9._-]{1,20}$` |
| name | text(120) | yes | translatable via wave 3 metadata |
| account_type | enum | yes | asset, liability, equity, revenue, expense, profit_and_loss_total, balance_sheet_total, reporting |
| debit_credit_default | enum | no | debit/credit; UI default only |
| is_postable | bool | yes | false for totals/reporting |
| suspended_from | date | no | posting rejected on/after this date |
| default_dimension_combination_id | bytea(32) | no | FK DimensionCombination |
| reconciliation_account_type | enum | no | none, customer, vendor, bank, inventory, fixed_asset — subledger-only posting (SAP recon account semantics) |
| created_at / created_by | timestamptz / uuid | yes | audit from CDC |

**AccountStructure** (header) and **AccountStructureConstraint** (rows)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| ledger_id | uuid | yes | FK Ledger |
| version | int | yes | unique (ledger_id, name, version) |
| name | text(60) | yes | |
| segments | jsonb | yes | ordered array of dimension_attribute_id; first is MAIN_ACCOUNT |
| effective_from | date | yes | |
| status | enum | yes | draft, active, retired |
| constraint rows: segment_index | int | yes | 0 = main account |
| constraint rows: parent_path | jsonb | yes | the matched values of previous segments this row applies under |
| constraint rows: allowed | jsonb | yes | list of `{from,to}` ranges, exact values, `*`, `blank` |

**JournalEntry** and **JournalLine**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| ledger_id | uuid | yes | FK Ledger |
| legal_entity_id | uuid | yes | denormalized for RLS |
| document_id | uuid | yes | groups entries across ledgers and split batches |
| voucher | text(30) | yes | from number sequence; unique (ledger_id, voucher) |
| posting_sequence | bigint | yes | gap-free per ledger; unique (ledger_id, posting_sequence) |
| posting_date | date | yes | |
| fiscal_period_id | uuid | yes | FK FiscalPeriod (wave 6) resolved from posting_date |
| document_date | date | no | |
| description | text(255) | no | |
| source_system / source_type / source_id / source_uri | text | yes | e.g. `ledger`, `manual_journal`, batch id, `passage://ledger/journal-batches/{id}` |
| idempotency_key | text(128) | yes | unique (tenant_id, legal_entity_id, idempotency_key) |
| payload_hash | bytea(32) | yes | SHA-256 of canonical request |
| reversal_of_entry_id | uuid | no | FK JournalEntry |
| posted_at / posted_by | timestamptz / uuid | yes | |
| trace_id | text(32) | yes | OpenTelemetry trace |
| line: id | uuid | yes | PK |
| line: entry_id | uuid | yes | FK |
| line: line_number | int | yes | unique per entry, 1..10000 |
| line: dimension_combination_id | bytea(32) | yes | FK DimensionCombination (includes account) |
| line: main_account_id | uuid | yes | denormalized for indexing |
| line: debit_credit | enum | yes | D or C; amounts always positive |
| line: amount_txn / currency_txn | numeric(23,6) / char(3) | yes | wave 11: equals accounting currency |
| line: amount_acct / amount_rpt | numeric(23,6) | yes | rounded to minor units (BR-11.3) |
| line: quantity / unit | numeric(18,6) / text | no | statistical quantity |
| line: text | text(255) | no | |
| line: source_line_ref | text(200) | no | |
| line: reversal_of_line_id | uuid | no | |

**Balance** (projection): `ledger_id`, `main_account_id`, `dimension_combination_id`, `fiscal_period_id`, `currency_type` (txn/acct/rpt), `currency` char(3), `debit`, `credit`, `net`, `line_count`, `last_posting_sequence`; PK over the first five.

**ReasonCode** (FR-11.15)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| code | text(10) | yes | unique per tenant; `^[A-Z0-9_]{1,10}$` |
| name | text(60) | yes | translatable |
| applies_to | enum[] | yes | ledger, bank, customer, vendor, fixed_asset, inventory_counting, tax, project |
| requires_comment | bool | yes | default false |
| negative_posting_allowed | bool | yes | default false; SAP `T041C-XNEGP` |
| alternative_posting_date_allowed | bool | yes | default true |
| active | bool | yes | |

**OpenItem** and **ClearingDocument** (FR-11.19, FR-11.20)

| Field | Type | Required | Notes |
|---|---|---|---|
| item: id | uuid | yes | PK |
| item: ledger_id / main_account_id / dimension_combination_id | uuid / uuid / bytea(32) | yes | account must be `open_item_managed` |
| item: journal_line_id | uuid | yes | unique; the line that created the item (or activation run) |
| item: source_type | enum | yes | gl_line, activation, residual; waves 18/19/30 add gr_ir, vendor, customer |
| item: amount_clearing / currency_clearing | numeric(23,6) / char(3) | yes | signed (debit positive) in the account's clearing currency |
| item: amount_acct | numeric(23,6) | yes | |
| item: assignment | text(50) | no | free matching key (SAP `ZUONR` semantics) |
| item: posting_date / due_date | date | yes / no | |
| item: status | enum | yes | open, cleared, reset_open |
| item: clearing_document_id | uuid | no | set when cleared |
| item: residual_of_item_id | uuid | no | FK OpenItem |
| doc: id | uuid | yes | PK |
| doc: ledger_id / main_account_id | uuid | yes | |
| doc: clearing_date | date | yes | posting date of difference/FX lines |
| doc: mode | enum | yes | manual, post_with_clearing, automatic, activation |
| doc: rule_id | uuid | no | FK AutoClearingRule when automatic |
| doc: item_ids | uuid[] | yes | 2..10,000 |
| doc: difference_amount / difference_entry_id | numeric(23,6) / uuid | no | tolerance posting |
| doc: fx_difference_entry_id | uuid | no | wave 12 realized gain/loss |
| doc: status | enum | yes | cleared, reset |
| doc: reset_reason_code / reset_by / reset_at | text / uuid / timestamptz | conditional | |

**AutoClearingRule**: `id`, `ledger_id`, `account_from`, `account_to`, `criteria` (jsonb ordered list ≤ 5 of field names), `allow_tolerance` bool, `active`. **ToleranceGroup**: `id`, `legal_entity_id`, `code`, `scope` (account | user), `limits` (jsonb per currency: `{abs_debit, abs_credit, pct}`), `difference_account_id`; assignment tables `tolerance_group_account` (account range) and `tolerance_group_user`.

**ValidationRule** and **DerivationStrategy** (FR-11.21, FR-11.22)

| Field | Type | Required | Notes |
|---|---|---|---|
| rule: id / ledger_id / sequence | uuid / uuid / int | yes | evaluated in sequence |
| rule: level | enum | yes | header, line, document |
| rule: prerequisite | text (CEL) | no | empty = always |
| rule: check | text (CEL) | yes | must evaluate true, else message |
| rule: message_code / severity / params | text / enum / jsonb | yes | catalog code (§2.11.18), error or warning |
| rule: effective_from / effective_to / version / status | date / date / int / enum | yes | draft, active, retired |
| strategy: id / ledger_id / version / status | | yes | one active version per ledger |
| step: id / strategy_id / sequence | | yes | ≤ 50 steps |
| step: kind | enum | yes | table_lookup, move, clear, expression, constant |
| step: condition | text (CEL) | no | |
| step: source_fields / target | jsonb / text | yes | target is a dimension attribute or line field |
| step: lookup_table_id | uuid | conditional | FK LookupTable (rows: source tuple, target value, valid_from, valid_to) |
| step: overwrite | bool | yes | default false |

**SplittingRule** (FR-11.23): `id`, `ledger_id`, `source_type`, `splitting_dimensions` (attribute ids), `method` (active | passive), `item_categories` (jsonb account range → category), `zero_balance_clearing_account_id`, `default_splitting_value` (jsonb attribute → value), `active`. **RealignmentRun** (FR-11.24): `id`, `ledger_id`, `filter` (jsonb), `mapping` (jsonb old → new per attribute) or `strategy_version`, `mode` (simulate | post), `status` (proposed, posted, reversed, cancelled), `reason_code`, `entry_ids[]`, `reversal_entry_ids[]`, `combinations_affected`, `created_by`. **DescriptionTemplate** (FR-11.17): `id`, `scope` (tenant | legal_entity), `legal_entity_id`, `source_type`, `language`, `template` text(255), `field_bindings` (jsonb `%n` → field path).

**Invariants**
- For every JournalEntry: Σ debit lines = Σ credit lines in each of amount_txn (per currency), amount_acct and amount_rpt, exactly, after rounding.
- Every JournalLine's `dimension_combination_id` was valid under the account structure version effective on `posting_date`.
- `posting_sequence` is dense (no gaps) per ledger; `Balance.last_posting_sequence` ≤ max posted sequence.
- Σ Balance.net over all accounts for a (ledger, period, currency_type) = 0.

**Indexing / partitioning**: `journal_line` is range-partitioned by `fiscal_year` (one partition per year per tenant shard); indexes on (ledger_id, main_account_id, posting_date), (dimension_combination_id, posting_date), (entry_id); `journal_entry` indexed on (ledger_id, posting_sequence), (document_id), (source_system, source_type, source_id). `balance` is unpartitioned, PK is the clustered access path. All tables carry `tenant_id` with RLS.

### 2.11.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/ledger/journal-entries | Post a balanced entry | `Idempotency-Key` required | Post journal entries (service accounts) / Post ledger journals |
| POST | /api/v1/ledger/journal-entries/{id}/reverse | Create negating entry | `Idempotency-Key` required | Reverse journal entries |
| GET | /api/v1/ledger/journal-entries/{id} | Entry with lines | n/a | View ledger transactions |
| GET | /api/v1/ledger/journal-lines?ledger=&account_from=&account_to=&dim.{attr}=&date_from=&date_to=&cursor= | Line query | n/a | View ledger transactions |
| GET | /api/v1/ledger/balances?ledger=&period_from=&period_to=&account_from=&account_to=&dim.{attr}=&group_by= | Balance query | n/a | View ledger balances |
| POST | /api/v1/ledger/validate | Dry-run validation of an entry (no post) | none (stateless) | Post ledger journals |
| GET/PUT | /api/v1/ledger/charts/{chart}/accounts[/{number}] | Main account CRUD | PUT is naturally idempotent | Maintain chart of accounts |
| GET/PUT | /api/v1/ledger/ledgers/{ledger}/account-structures[/{id}] | Structure CRUD and activation | PUT idempotent | Maintain account structures |
| POST | /api/v1/ledger/dimension-combinations/resolve | Tuple → combination id | stateless | View ledger transactions |
| POST/GET/PATCH | /api/v1/ledger/journal-batches[/{id}] and /{id}:submit, :approve, :post | Manual journal lifecycle | `Idempotency-Key` on :post | Maintain / Approve / Post ledger journals |
| POST | /api/v1/ledger/balances:rebuild | Trigger rebuild (admin) | run id returned | Administer ledger |
| GET/PUT | /api/v1/ledger/reason-codes[/{code}] | Reason code CRUD (shared) | PUT idempotent | Maintain reason codes |
| GET/PUT | /api/v1/ledger/ledgers/{ledger}/advanced-rules[/{id}] | Advanced rules and advanced rule structures | PUT idempotent | Maintain account structures |
| GET/PUT | /api/v1/ledger/description-templates[/{id}] | Default description templates | PUT idempotent | Maintain ledger setup |
| GET | /api/v1/ledger/open-items?ledger=&account=&status=&assignment=&dim.{attr}=&cursor= | Open items on OI-managed accounts | n/a | View ledger transactions |
| POST | /api/v1/ledger/clearings | Manual clearing of selected items (with tolerance / residual options) | `Idempotency-Key` required | Clear open items |
| POST | /api/v1/ledger/clearings:simulate | Residual and tolerance preview | stateless | Clear open items |
| POST | /api/v1/ledger/clearings/{id}:reset | Reset clearing (reason code required) | `Idempotency-Key` required | Reset clearings |
| POST | /api/v1/ledger/accounts/{account}:activate-open-items | Activation run for an account with history | `Idempotency-Key` required | Administer ledger |
| GET/PUT | /api/v1/ledger/ledgers/{ledger}/auto-clearing-rules[/{id}] and /tolerance-groups[/{code}] | Automatic clearing rules, tolerance groups and assignments | PUT idempotent | Maintain clearing rules |
| POST | /api/v1/ledger/auto-clearing:run | Run automatic clearing now (simulate or clear) | run id | Run automatic clearing |
| GET/PUT | /api/v1/ledger/ledgers/{ledger}/validation-rules[/{id}] and POST /validation-rules/{id}:test | Validation rules CRUD and test against samples | PUT idempotent | Maintain validation and derivation rules |
| GET/PUT | /api/v1/ledger/ledgers/{ledger}/derivation-strategies[/{version}] and /lookup-tables[/{id}] (CSV import) | Derivation strategy, steps, lookup tables | PUT idempotent; import `Idempotency-Key` | Maintain validation and derivation rules |
| GET/PUT | /api/v1/ledger/ledgers/{ledger}/splitting-rules[/{source_type}] | Document splitting configuration | PUT idempotent | Maintain ledger setup |
| POST | /api/v1/ledger/realignments and /{id}:post, :reverse | Dimension realignment simulate / post / reverse | `Idempotency-Key` required | Run dimension realignment |

**Posting request (canonical JSON)**

```
POST /api/v1/ledger/journal-entries
Idempotency-Key: ap-invoice-7F3A-post
{ "ledger_group": "ALL" | "ledger_id": "...",
  "document_id": "uuid", "posting_date": "2026-03-31", "document_date": "2026-03-28",
  "description": "Vendor invoice 7F3A",
  "source": {"system":"ap","type":"vendor_invoice","id":"7F3A","uri":"passage://ap/invoices/7F3A"},
  "lines": [
    {"line_number":1,"account":"6010","dimensions":{"DEPT":"D100","CC":"C200"},
     "debit_credit":"D","amount":{"currency":"USD","value":"1250.00"},"text":"Freight"},
    {"line_number":2,"account":"2100","dimensions":{},"debit_credit":"C",
     "amount":{"currency":"USD","value":"1250.00"}} ] }
```
Response 201: `{ "entries":[{"id","ledger_id","voucher","posting_sequence","fiscal_period_id","totals":{"txn":{...},"acct":"1250.00","rpt":"1150.75"},"lines":[...]}], "idempotency":{"key","replayed":false}, "trace_id" }`. Amounts are strings with exactly the currency's minor-unit scale.

**Commands / validation**: `PostJournalEntry` (validation order: schema → ledger/period resolution → account existence and postability → structure validation → combination interning → currency/rounding (wave 12) → period status (wave 14) → balance check → sequence assignment → insert → balance upsert → outbox). `ReverseJournalEntry` (original exists, not already reversed unless `allow_reinstate`, target date open). `SubmitJournalBatch` (every line valid, totals balanced, user owns batch or holds duty). `ApproveJournalBatch` (approver ≠ submitter unless journal name allows self-approval).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.ledger.journal.posted.v1 | entry committed | entry_id, ledger_id, document_id, voucher, posting_sequence, posting_date, fiscal_period_id, line_count, totals{txn[],acct,rpt}, source, trace_id | reporting, tax, audit, lakehouse, subledgers |
| dev.passage.ledger.journal.reversed.v1 | reversal committed | original_entry_id, reversing_entry_id, reason_code, posting_date | reporting, subledgers |
| dev.passage.ledger.batch.status-changed.v1 | batch transition | batch_id, journal_name, from, to, actor, workflow_run_id | notifications, audit |
| dev.passage.ledger.balance.rebuilt.v1 | rebuild finished | ledger_id, high_water_sequence, rows_written, rows_differing, duration_ms | ops alerting |
| dev.passage.ledger.account.changed.v1 | account/structure change | chart_id, account_number, change_type, effective_from | reporting, dimension sync |
| dev.passage.ledger.open-item.cleared.v1 | clearing document committed or reset | clearing_document_id, ledger_id, account, mode, item_ids, difference_amount, fx_difference_entry_id, status (cleared/reset), reason_code | bank reconciliation (wave 36), AP/AR (waves 19/30), reporting, audit |
| dev.passage.ledger.realignment.completed.v1 | realignment posted or reversed | run_id, ledger_id, period_range, combinations_affected, entry_ids, reversal | reporting, audit |
| dev.passage.ledger.rules.changed.v1 | validation rule, derivation strategy or splitting rule activated | ledger_id, kind, id, version, actor | posting cache, audit |

Open-item contract for subledgers (used by waves 18/19/30): a service registers an item source `{source_type, resolver_uri}`; the clearing engine calls `resolver_uri` to fetch candidate items and posts the clearing through the same `ClearingDocument` path; the source service consumes `open-item.cleared` to update its own status. The posting request accepts per line `"clear_item_ids": [...]` (FR-11.19) and the derivation/splitting/validation stages run in the order shown in the command pipeline below.

Posting pipeline (revised with FR-11.21–FR-11.23): schema → ledger/period resolution → default descriptions (FR-11.17) → derivation strategy (FR-11.22) → document splitting (FR-11.23) → account existence and postability → structure and advanced rule validation (FR-11.3, FR-11.16) → validation rules (FR-11.21) → combination interning → currency/rounding (wave 12) → period status (wave 14) → balance check → open-item clearing (FR-11.19) → sequence assignment → insert → balance upsert → outbox. `POST /ledger/validate` runs the same stages up to the balance check and returns the derivation trace and splitting result.

### 2.11.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Chart of accounts | Create, edit, suspend, import CSV, view balances | number, name, type, postable, suspended_from, default dimensions | Maintain chart of accounts |
| Financial dimensions | Add attribute, bind to entity, maintain custom values | attribute, backing entity, active_from/to, suspended | Maintain financial dimensions |
| Account structures | Design segments, constraints grid, validate sample, activate version | segment, allowed ranges, effective_from, status | Maintain account structures |
| Journals (list) | New batch, filter by status/name/owner, bulk submit | voucher, journal name, status, total debit/credit, owner, posting date | Maintain ledger journals |
| Journal batch (detail) | Add/edit lines inline, validate, submit, approve, post, attach, save as template | line grid: account, dimensions (segmented entry), D/C, amount, text; totals bar | Maintain / Approve / Post ledger journals |
| Trial balance | Filter ledger/period/dimension, drill to lines, export | account, opening, debit, credit, closing, net | View ledger balances |
| Ledger transactions | Filter, drill to source document, trace id link | voucher, date, account, dims, D/C, amount, source | View ledger transactions |
| Reason codes | Create, deactivate, set modules and flags | code, name, applies_to, requires_comment, negative_posting_allowed | Maintain reason codes |
| Open items and clearing | Filter items, select, see residual, clear, create residual item, reset clearing | account, assignment, date, amount (clearing / acct), status, clearing document | Clear open items / Reset clearings |
| Clearing rules and tolerances | Define auto-clearing criteria, tolerance groups, assign to accounts and users, simulate run | account range, criteria order, allow_tolerance, limits per currency, difference account | Maintain clearing rules |
| Validation and derivation rules | Rule editor with CEL assist, lookup table grid and CSV import, test against sample entries, activate version | sequence, level, prerequisite, check, message; step kind, source, target, overwrite | Maintain validation and derivation rules |
| Document splitting setup | Item categories by account range, splitting dimensions, method, zero-balance account, test with a sample document | source_type, dimensions, method, categories, clearing account | Maintain ledger setup |
| Dimension realignment | Define filter and mapping, simulate, review affected combinations, post, reverse | account range, periods, old value, new value, lines, amount | Run dimension realignment |

Interaction rules: segmented account entry accepts `1010-D100-C200` typed as one string with per-segment lookup (Tab moves segment); validation on blur per line and on save for the batch; grid supports paste from Excel (up to 5,000 rows) and bulk fill-down of dimensions; keyboard: Ctrl+Enter = save line, Alt+P = post (confirm dialog), Esc = cancel edit; saved views and column personalization persist per user (wave 5 shell).

### 2.11.7 Business rules and edge cases

- **BR-11.1 Balance by construction.** An entry is accepted only if Σ debits = Σ credits in every amount column; the check is performed on rounded amounts and repeated inside the database transaction (CHECK via deferred constraint trigger).
- **BR-11.2 Zero-amount lines** are allowed only when `quantity` ≠ 0 (statistical) or the line carries `text`; otherwise rejected with `LEDGER_LINE_EMPTY`.
- **BR-11.3 Rounding.** Amounts are stored with scale equal to the currency's ISO 4217 minor units (USD/EUR 2, JPY 0, KWD/BHD 3); requests with excess precision are rejected with `LEDGER_AMOUNT_SCALE` (no silent rounding of transaction amounts). Derived amounts (wave 12) are rounded half-away-from-zero.
- **BR-11.4 Posting date.** Must fall in a fiscal period of the ledger's calendar; date before the ledger's `first_open_date` or in a closed period → `LEDGER_PERIOD_CLOSED`. Future dates are allowed up to 366 days.
- **BR-11.5 Concurrency.** Posting takes a row lock on `ledger` only for sequence assignment (sub-millisecond); balance upserts use `INSERT … ON CONFLICT DO UPDATE` ordered by PK to prevent deadlocks; the service retries serialization failures up to 3 times with jitter.
- **BR-11.6 Idempotency key semantics.** Key is compared after trimming; scope is (tenant, legal entity). A replay returns the original response including the original `trace_id` and `replayed: true`. Keys are persisted before the entry insert in the same transaction, so a crash leaves either both or neither.
- **BR-11.7 Balance rebuild algorithm.** (1) Record `hwm = max(posting_sequence)` per ledger. (2) In a shadow table, `INSERT … SELECT ledger_id, main_account_id, dimension_combination_id, fiscal_period_id, currency_type, SUM(debit), SUM(credit), COUNT(*), MAX(posting_sequence) FROM journal_line JOIN journal_entry WHERE posting_sequence ≤ hwm GROUP BY …`, chunked by fiscal period (each chunk ≤ 5M lines, committed separately). (3) Catch-up loop: apply lines with sequence in (hwm, new_hwm] as deltas; repeat until the delta is < 1,000 lines. (4) Take a 2-second advisory lock on posting for the ledger, apply the final delta, compare shadow to live row by row (FULL OUTER JOIN on PK, any non-zero difference recorded), swap tables atomically, release. (5) Emit `balance.rebuilt` with `rows_differing`; > 0 raises a P1 alert and the live table is kept as the rebuilt copy (the journal is truth).
- **BR-11.8 Reconciliation account protection.** Accounts with `reconciliation_account_type ≠ none` reject manual batch lines (`LEDGER_RECON_ACCOUNT_MANUAL`) unless the journal name is flagged `allow_recon_accounts` (migration and correction use only).
- **BR-11.9 Error codes (422 unless noted).** `LEDGER_UNBALANCED`, `LEDGER_LINE_LIMIT`, `LEDGER_LINE_EMPTY`, `LEDGER_AMOUNT_SCALE`, `LEDGER_ACCOUNT_NOT_FOUND`, `LEDGER_ACCOUNT_NOT_POSTABLE`, `LEDGER_ACCOUNT_SUSPENDED`, `LEDGER_RECON_ACCOUNT_MANUAL`, `LEDGER_STRUCTURE_NOT_FOUND`, `LEDGER_DIMENSION_REQUIRED`, `LEDGER_DIMENSION_INVALID`, `LEDGER_DIMENSION_INACTIVE`, `LEDGER_COMBINATION_INVALID`, `LEDGER_PERIOD_NOT_FOUND`, `LEDGER_PERIOD_CLOSED`, `LEDGER_PERIOD_ON_HOLD` (403), `LEDGER_DATE_INVALID`, `LEDGER_CURRENCY_MISMATCH`, `LEDGER_RATE_MISSING`, `LEDGER_ROUNDING_EXCEEDED`, `LEDGER_REVERSAL_ALREADY_REVERSED`, `LEDGER_IDEMPOTENCY_CONFLICT` (409), `LEDGER_IDEMPOTENCY_IN_PROGRESS` (409), `LEDGER_FORBIDDEN` (403).
- **BR-11.10 Journal batch state machine.** `draft → validated` (all lines valid) → `in_approval` (submit; skipped if journal name has no workflow) → `approved | rejected` → `posting` (lock) → `posted` (entry ids recorded) or back to `approved` with error on failure; `draft|validated|rejected → cancelled`; any line edit in `validated|approved` returns the batch to `draft`. Posted and cancelled are terminal.
- **BR-11.11 Document split.** Entries sharing a `document_id` are independent for balancing; callers that split a document must balance each part (use a clearing account) — the API does not defer balancing across requests.
- **BR-11.12 Clearing tolerance.** For a selected item set, `residual = Σ amount_clearing`. Let `T_user` and `T_account` be the applicable tolerance groups (absent = 0). The posting limit is `min(T_user, T_account)` per side (`abs_debit` for a debit residual, `abs_credit` for a credit residual) and additionally `pct × Σ|amount|` when `pct` is set. `|residual| = 0` → clear; `0 < |residual| ≤ limit` → clear and post the residual to the tolerance group's `difference_account_id` with the dimensions of the largest item; `|residual| > limit` → `residual_item = true` creates a residual item for `residual` on the same account and combination, else `LEDGER_CLEARING_UNBALANCED`.
- **BR-11.13 Open-item lifecycle.** `open → cleared` (exactly once per clearing document) → `reset_open` (reset) → `cleared` (again, by a new document); an item in `cleared` cannot be in a second active clearing document (`LEDGER_OPEN_ITEM_ALREADY_CLEARED`); a residual item's `residual_of_item_id` chain is at most 20 deep; clearing dates may not precede the latest posting date of the items cleared; a clearing document is reset as a whole, never partially.
- **BR-11.14 Derivation evaluation.** Steps run strictly in `sequence`; a step whose target was set by an earlier step in the same run is skipped unless `overwrite = true`; a step may not read a field it also writes; lookup tables are matched on the full source tuple with `*` wildcards and the most specific row wins (fewest wildcards, then latest `valid_from`); a strategy with > 50 steps or a step with a CEL parse error cannot be activated (`LEDGER_DERIVATION_INVALID`).
- **BR-11.15 Splitting proportions.** For an entry with carrying lines (amounts `a_i`, splitting values `v_i`) and a non-carrying line amount `b`: produce lines `b × a_i / Σa` per distinct `v`, rounded per BR-12.4 with the residual on the largest share; when Σa = 0 or no carrying line exists use `default_splitting_value` or raise `LEDGER_SPLIT_NO_SOURCE`; after inheritance, for each splitting value with Σ(debit − credit) ≠ 0, add a pair of zero-balance lines (clearing account) so each value balances; zero-balance lines are excluded from P&L statements and flagged `is_zero_balance = true`.
- **BR-11.16 Negative posting.** When a reversal uses a reason code with `negative_posting_allowed`, the reversing lines keep the original side with the opposite sign in the `Balance` projection (`debit −x`), while `JournalLine.debit_credit` stays D/C with positive amounts and `negative_posting = true`; reports that sum debits and credits (trial balance, document journal) apply the flag so turnover totals equal the pre-posting values.
- **BR-11.17 Validation rule evaluation.** All rules are evaluated (no short-circuit) so the response lists every violation; a rule referencing a dimension attribute not in the ledger is skipped and logged; warnings do not block API callers (service accounts) but are returned in `warnings[]`; the same rule set applies to reversals, allocations, accruals and year-end entries unless the rule's prerequisite excludes the `source_type`.
- **BR-11.18 Open-item account constraints.** `open_item_managed` is allowed only for balance-sheet accounts that are not reconciliation accounts of type customer/vendor (those are managed by waves 19/30) and not `is_postable = false`; the flag change is effective-dated and requires an activation run when the account has non-zero balances; open items are revalued (wave 12) at item level, so the account is excluded from balance-level revaluation.

### 2.11.8 Acceptance criteria

- **AC-11.1** (FR-11.5, FR-11.6) Given a balanced two-line USD entry in an open period, when posted with a new key, then 201 is returned within 200 ms p99, a `posting_sequence` is assigned, and `journal.posted` is observed on Kafka within 2 s.
- **AC-11.2** (FR-11.5) Given the same request repeated with the same key, when posted, then 200 is returned with `replayed: true` and no new entry exists.
- **AC-11.3** (FR-11.5) Given the same key with a changed amount, when posted, then 409 `LEDGER_IDEMPOTENCY_CONFLICT`.
- **AC-11.4** (FR-11.3) Given account 6010 requires DEPT under the active structure, when a line omits DEPT, then 422 `LEDGER_DIMENSION_REQUIRED` with `line_index`.
- **AC-11.5** (BR-11.1) Given lines summing to debit 100.00 / credit 99.99, when posted, then 422 `LEDGER_UNBALANCED` and no rows are written.
- **AC-11.6** (FR-11.7) Given 1M random balanced entries posted by 64 concurrent workers, when complete, then Σ Balance.net = 0 for every period and a rebuild reports `rows_differing = 0`.
- **AC-11.7** (FR-11.8) Given a posted entry, when reversed, then a new entry exists whose lines equal the original with D/C swapped and `reversal_of_line_id` set, and a second reversal returns `LEDGER_REVERSAL_ALREADY_REVERSED`.
- **AC-11.8** (FR-11.9, BR-11.10) Given a journal name with a 2-level approval, when a clerk submits a batch, then it is `in_approval`, the approver sees a Temporal task, and after approval the clerk can post.
- **AC-11.9** (FR-11.13) Given the demo opening balance file, when loaded through the migration framework, then the conversion-suspense account balance is 0.00 and the trial balance equals the AX extract totals.
- **AC-11.10** (FR-11.14) Given 10M lines, when querying lines for one account and month, then the first page returns in < 300 ms p95 using the keyset cursor.
- **AC-11.11** (FR-11.15) Given a journal name with `reversal_reason_required = true`, when a reversal is requested without `reason_code`, then 422 `LEDGER_REASON_REQUIRED`; when requested with a code flagged `negative_posting_allowed`, then the trial balance debit and credit turnover equal their values before the original posting.
- **AC-11.12** (FR-11.19, BR-11.12) Given open items of USD 100.00 (D) and USD 99.98 (C) on a clearing account and a user tolerance of USD 0.05, when cleared manually, then one clearing document exists, a USD 0.02 difference line is posted to the tolerance account, and both items are `cleared`; with tolerance USD 0.01 and `residual_item = true`, then a residual item of USD 0.02 is created instead.
- **AC-11.13** (FR-11.20) Given 1,000 open items on GR/IR-style accounts with matching `assignment` values summing to zero in 400 groups and 50 unmatched items, when `gl-auto-clear` runs, then 400 clearing documents exist, 50 items remain open, and no residual items were created.
- **AC-11.14** (FR-11.21) Given an active rule "lines on accounts 6000–6999 require DEPT when amount_acct > 1,000", when a 1,500.00 line without DEPT is validated, then 422 with the rule id and `line_index`; a 500.00 line without DEPT passes.
- **AC-11.15** (FR-11.22) Given a lookup step (account category = COGS, cost center = C200) → FUNC_AREA = PROD, when a line on a COGS account with C200 and no FUNC_AREA is posted, then the stored combination contains FUNC_AREA = PROD and `derived_by_step_ids` names the step.
- **AC-11.16** (FR-11.23, BR-11.15) Given a vendor invoice with expense lines 600.00 (PC1) and 400.00 (PC2), a tax line 190.00 and a payable line 1,190.00, when posted with an active splitting rule on PC, then the payable is split 714.00 / 476.00 and the tax 114.00 / 76.00, and Σ(debit − credit) per profit center is 0.00.
- **AC-11.17** (FR-11.24) Given 2,000 lines on cost center C100 across 3 periods, when a realignment C100 → C150 is posted and then reversed, then balances per combination equal the pre-run values and both runs appear in the audit trail.
- **AC-11.18** (FR-11.16) Given an advanced rule "account 5000–5999 and DEPT = D100 adds segment PROJECT (required)", when a line on 5100/D100 omits PROJECT, then 422 `LEDGER_DIMENSION_REQUIRED` for PROJECT; a line on 5100/D200 posts without PROJECT.
- **AC-11.19** (FR-11.17) Given a tenant template for `manual_journal` "Journal %1 of %2" bound to `{voucher}` and `{document_date}`, when a batch without description is posted, then the entry description is "Journal GJ-000123 of 2026-03-31".

### 2.11.9 Test plan and fixtures

- Fixtures: `coa-demo` (1,200 accounts, 3 charts, from the demo manufacturer's AX `MainAccount` extract); `dims-demo` (8 attributes, 2,400 values); `structures-demo` (4 structures, 2 advanced rules); `entries-random-1m` (generator: seeded, 1M entries, 2–50 lines each, balanced by construction); `entries-negative-120` (120 hand-written rejection cases, one per error code × variant); `opening-balances-demo` (3 entities, CSV from AX `GeneralJournalAccountEntry` aggregation).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Balanced-entry property test | property (Go rapid) | posting service | any generated balanced entry posts; any mutated unbalanced entry is rejected |
| Error-code table | unit | validator | 120 cases map to expected code and line_index |
| Idempotency contract | contract (OpenAPI + Pact) | posting API | replay/conflict/in-progress semantics as specified |
| Concurrent posting | integration | Postgres + service | 64 workers × 1M entries: zero sequence gaps/duplicates, TB = 0 |
| Rebuild equivalence | integration | rebuild job | rows_differing = 0 after concurrent load |
| Throughput | performance (k6) | dev cluster 3 nodes | 5,000 lines/s for 30 min, p99 < 200 ms, error rate 0 |
| Journal workspace E2E | Playwright + axe | React shell | create → submit → approve → post; WCAG 2.1 AA pass |
| Append-only enforcement | unit (SQL) | triggers/grants | UPDATE/DELETE on journal tables raise for app role and superuser |

Reconciliation checks: Σ Balance per (ledger, period) = 0; Σ Balance per account = Σ journal lines per account; count of `journal.posted` events = count of entries (outbox audit); opening-balance conversion suspense = 0.

### 2.11.10 Deployment and operations

Helm chart `passage-ledger` (deployment, HPA 3–12 pods on CPU 60%, PodDisruptionBudget 2, CloudNativePG cluster `ledger-db` 3 instances, Kafka topics `ledger.journal`, `ledger.batch`, Debezium outbox connector). Config: `LEDGER_MAX_LINES=10000`, `LEDGER_IDEMPOTENCY_TTL_DAYS=400`, `LEDGER_REBUILD_CHUNK_LINES=5000000`, `LEDGER_FUTURE_DATE_DAYS=366`. Migrations via Atlas generated from entity metadata; partitions for the next fiscal year created by a monthly job. Feature flags: `ledger.recurring_journals`, `ledger.advanced_rules`. Scheduled jobs (Temporal): nightly `balance-verify` (rebuild into shadow, compare, no swap unless diff), monthly `create-partitions`, recurring journal generator. SLOs: posting availability 99.9%; posting p99 < 200 ms; `journal.posted` publish lag p95 < 2 s. Alerts: balance verify diff > 0 (P1), sequence gap detected (P1), outbox lag > 60 s (P2), posting error rate > 1% over 5 min (P2). Runbook: rebuild procedure, sequence gap investigation, idempotency table growth. Rollback: Argo CD previous digest; schema migrations are additive-only in this wave; no data rollback required because the journal is append-only.

### 2.11.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX `MainAccount`, `LedgerChartOfAccounts`, `MainAccountLegalEntity` | MainAccount, ChartOfAccounts, MainAccountLedgerSetting | type mapping from `MainAccountType`; suspended → `suspended_from` |
| AX `DimensionAttribute`, `DimensionAttributeValue`, `DimensionFinancialTag` | DimensionAttribute, DimensionValue | entity-backed attributes re-bound to Passage parties/sites; custom lists copied |
| AX `DimensionHierarchy` (account structures), `DimensionConstraintTree/Node` | AccountStructure(+Constraint) | advanced rules flattened into constraint rows; activated version 1 |
| AX `DimensionAttributeValueCombination` | DimensionCombination | recomputed from tuples; `RecId` kept in `legacy_ref` for cross-reference |
| AX `GeneralJournalEntry`/`GeneralJournalAccountEntry` (closing balances only) | JournalEntry (period 0) | opening balances per combination; detail history not migrated in Phase 2 (wave 49 transactional packs) |
| SAP `SKA1`/`SKAT`/`SKB1` | ChartOfAccounts, MainAccount, MainAccountLedgerSetting | `XSPEB` (blocked) → suspended; `MITKZ` → reconciliation_account_type |
| SAP `CSKS`, `CEPC`, `TGSB` | DimensionValue (CC, PC, BA attributes) | validity dates preserved |
| SAP `FAGLFLEXT`/`ACDOCA` closing balances by period | JournalEntry (period 0) | per ledger (`RLDNR`) → Passage ledger |
| AX `ReasonTable`, `ReasonTableRef` | ReasonCode | `applies_to` from the Ledger/Bank/Cust/Vend/Asset flags |
| AX `DimensionHierarchy` (advanced rule structures), `DimensionAdvancedRule`, `DimensionRuleCriteria` | AdvancedRule (+ structure) | criteria → filter; structure segments → appended segments |
| AX `TransactionTxt` (default descriptions) | DescriptionTemplate | `%1`–`%3` mapped to field bindings per transaction type |
| AX `DocuRef`/`DocuValue` on `LedgerJournalTable` | Attachment (wave 5 framework) | `DocuType` → document_type; files copied to object storage |
| SAP `T041C` | ReasonCode | `XNEGP` → negative_posting_allowed; `XABWD` → alternative_posting_date_allowed |
| SAP `SKB1-XOPVW`, open items on OI-managed G/L accounts (`BSIS` with `AUGBL` blank), `T043S`, `TF123` | MainAccountLedgerSetting.open_item_managed, OpenItem (activation run), ToleranceGroup, AutoClearingRule | one open item per uncleared `BSIS` row dated cutover; cleared history (`BSAS`) not migrated |
| SAP `GB90*`/`T001Q` validations, `GB91*`/`T001R` substitutions, `TKEDR*`/`K9R*` derivation, `T8A*` PCA substitution, `FMDERIVE*`, `TFKB` functional areas | ValidationRule, DerivationStrategy steps, LookupTable, dimension values for FUNC_AREA | Boolean rules converted to CEL where the exit is table-based; user exits (ZGGB*) listed in a conversion report for re-authoring |
| SAP `T8G01`–`T8G40` document splitting | SplittingRule | item categories → categories; splitting method → active; zero-balance account → clearing account |

Validation: account count match; every migrated combination validates under the migrated structure (exceptions queued); opening TB per entity equals source to the cent. Not migrated: AX `LedgerJournalTable` unposted batches (users re-key), SAP parked documents, posting definitions (re-implemented per subledger wave).

### 2.11.12 Dependencies, risks, open questions

Depends on: wave 3 (metadata entity generation), wave 4 (outbox, Temporal approval template), wave 5 (gap-free number sequences, audit), wave 6 (legal entities, fiscal calendars, currencies), wave 7 (party dimension backing), wave 10 (opening balance load).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Gap-free sequence becomes a throughput bottleneck | Medium | Medium | Sequence lock held < 1 ms; batch allocation for multi-entry documents; measured in the 5k lines/s test |
| Dimension combination table growth (millions of tuples) | Medium | Low | 32-byte hash key; combinations are reused; monitored |
| Structure change invalidates existing combinations | Medium | Medium | Combinations are tied to structure version; old versions stay queryable |

Open questions: (1) Should `amount_txn` be stored as integer minor units instead of numeric(23,6)? Default: numeric with scale check; decide by wave 12 start. (2) Idempotency scope per legal entity vs per tenant? Default: per legal entity; decide by wave 11 mid-point. (3) Maximum dimensions 20 vs AX's practical 10? Default 20; decide by wave 11 exit.

### 2.11.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `max_lines_per_entry` | tenant | int | 10,000 | 100–50,000 | Posting request line limit (FR-11.5) | — (journal `LinesLimit` on journal name) | 999 line limit per `BSEG` document (hard) |
| `idempotency_ttl_days` | tenant | int | 400 | 30–3,650 | Retention of idempotency records (FR-11.5) | — | — |
| `future_posting_days` | legal entity | int | 366 | 0–730 | Maximum days ahead for `posting_date` (BR-11.4) | General ledger parameters > Ledger > "Allow posting to future periods" (implicit) | OB52 open-period range |
| `reversal_reason_required` | legal entity | bool | true | true / false | Reason code mandatory on reversal (FR-11.15) | General ledger parameters > Ledger > Reason code requirements (reversal) | T041C reason required on FB08 |
| `negative_posting_enabled` | ledger | bool | false | true / false | Permits reason codes with `negative_posting_allowed` (BR-11.16) | General ledger parameters > Ledger > Correction (storno) | OBY6 "Negative postings permitted" |
| `check_voucher_used` | ledger | enum | reject_duplicates | reject_duplicates / warn / accept | Duplicate voucher detection across journal names | General ledger parameters > Ledger > Check for voucher used | FBN1 number range object `RF_BELEG` (unique by design) |
| `use_document_date_for_voucher` | ledger | bool | false | true / false | Voucher sequence drawn from document date year | General ledger parameters > Ledger > Use document date for vouchers | OBA7 document type "Doc. date = posting date" |
| `journal_name.posting_restriction` | journal name | enum | by_user_group | none / own_journals_only / by_user_group | Who may post batches of this name (FR-11.9) | Journal names > Posting restrictions | OBA7 authorization group |
| `journal_name.lines_limit` | journal name | int | 5,000 | 1–50,000 | Lines per batch (FR-11.9) | Journal names > Lines limit | — |
| `journal_name.allow_recon_accounts` | journal name | bool | false | true / false | Manual lines on reconciliation accounts (BR-11.8) | Journal names > Journal control (account types allowed) | OBA7 "Account types allowed" (D/K) |
| `journal_name.self_approval_allowed` | journal name | bool | false | true / false | Approver may equal submitter | Workflow configuration > automatic actions | WS10000051 agent rule |
| `journal_name.voucher_sequence_id` | journal name | ref | required | number sequence (wave 5), gap-free | Voucher numbering per journal name | Journal names > Voucher series | OBA7 number range per document type (FBN1) |
| `journal_name.description_language` | journal name | enum | legal_entity | legal_entity / user | Language for default descriptions (FR-11.17) | Default descriptions > Language | — |
| `max_dimension_attributes` | ledger | int | 20 | 1–20 | Attributes per ledger (FR-11.2) | Financial dimensions (practical limit ~10) | ACDOCA fixed fields + coding block extension (OXK3) |
| `account.open_item_managed` | main account × ledger | bool | false | true / false | Creates open items per line (FR-11.19) | — | FS00 company-code data "Open item management" (`SKB1-XOPVW`) |
| `account.clearing_currency` | main account × ledger | enum | accounting | accounting / transaction | Currency in which items must balance to clear | — | FS00 "Only balances in local currency" |
| `tolerance_group.limits` | legal entity (per group) | jsonb | `{USD:{abs_debit:0.05, abs_credit:0.05, pct:0}}` | per currency | Clearing difference posting limits (BR-11.12) | General ledger parameters > Ledger > Maximum penny difference | OBA0 tolerance groups for G/L accounts; OB57 user assignment |
| `auto_clearing.schedule` | ledger | cron | `0 2 * * *` | cron | Schedule of `gl-auto-clear` (FR-11.20) | — | F.13 batch variant (SM36) |
| `validation_rules.active_version` | ledger | int | latest active | activated version | Rule set in force (FR-11.21) | Journal controls | OB28 activation level 1/2 per company code |
| `derivation_strategy.active_version` | ledger | int | latest active | activated version | Strategy in force (FR-11.22) | — | KEDR strategy active; OBBH activation |
| `splitting.method` per source_type | ledger | enum | off | off / active / passive | Document splitting (FR-11.23) | — | FAGL_SPLIT activation per company code; splitting method |
| `splitting.zero_balance_clearing_account_id` | ledger | ref | none | balance-sheet account | Zero-balance lines (BR-11.15) | — | Zero-balance clearing account (T8G30) |
| `realignment.enabled` | ledger | bool | false | true / false | Enables FR-11.24 runs | — | KEND authorization |
| `balance_rebuild_chunk_lines` | tenant | int | 5,000,000 | 1M–20M | Chunk size in BR-11.7 | Financial dimension sets > Rebuild balances (no tuning) | RFINDEX variant |
| `attachment.default_retention_class` | legal entity | ref | `financial_record_10y` | wave 5 retention classes | Retention applied to voucher attachments (FR-11.18) | Document management parameters > Document types | OAC3 ArchiveLink document type / retention |

### 2.11.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Trial balance | list inquiry | ledger, period range, dimension set, currency type, account range | account, opening, debit, credit, closing, net, line count | `Balance` | Trial balance list page (`LedgerTrialBalanceListPage`); SSRS Trial balance | FAGLB03, S_ALR_87012277, F.08 |
| Ledger transactions (voucher transactions) | drill-down | ledger, voucher, date range, account, dimension values, source system | voucher, posting date, account, dimensions, D/C, txn/acct/rpt amounts, text, source link, trace id | `JournalLine` ⋈ `JournalEntry` | Voucher transactions / Ledger transactions inquiry; SSRS Ledger transaction list | FBL3N, FAGLL03, S_ALR_87012282, Fiori Display Line Items in General Ledger |
| Dimension statement | list inquiry | ledger, dimension attribute + value, period range | account, net per period, YTD | `Balance` | Dimension balance inquiry (`DimensionFocus` balances) | KE5Z (profit center), S_ALR_87013611 (cost center) |
| Chart of accounts listing | operational report | chart, account type, postable, suspended | number, name, type, category, default dimensions, OI flag, recon type | `MainAccount` ⋈ `MainAccountLedgerSetting` | SSRS Chart of accounts | S_ALR_87012326, S_ALR_87012328 |
| Journal batches (unposted) | list inquiry | journal name, status, owner, date, amount | voucher, name, status, Σ debit, Σ credit, owner, approver, posting date | `JournalBatch` | General journal list page; SSRS Journal (LedgerJournal) | FBV0 / FBV3 parked documents list |
| Posted journals | operational report | journal name, posted date range, posted by | batch, vouchers, entry ids, totals, posted by/at | `JournalBatch` (posted) ⋈ `JournalEntry` | Posted journals list; SSRS Posted journals | S_ALR_87012287 document journal |
| Voucher (document) view | drill-down | voucher or entry id | header, all lines with dimensions and three currencies, reversal links, attachments | `JournalEntry` | SSRS Voucher (LedgerTransVoucher); Voucher transactions | FB03, S_ALR_87012289 compact document journal |
| Open items by account | list inquiry | ledger, account range, status, assignment, date, dimension | item, posting date, assignment, amount clearing/acct, days open, clearing document | `OpenItem` | — (customer/vendor open transactions only) | FBL3N open items, FAGLL03 (OI status) |
| Clearing documents | drill-down | account, clearing date range, mode, status | document, mode, items, difference, FX difference, reset reason/by | `ClearingDocument` | — | FB03 clearing document, FBRA history |
| Automatic clearing proposal / log | operational report | run id, ledger, rule | groups proposed, cleared, skipped (reason), items per group | `gl-auto-clear` run log | — | F.13 log (SE38 `SAPF124` output) |
| Validation rule test result | drill-down | rule id, sample entry | per line: prerequisite result, check result, message | `ValidationRule` test harness | — | GGB0 simulation |
| Derivation trace | drill-down | batch line or `validate` response | step sequence, kind, matched lookup row, value before/after | posting pipeline | — | KEDR "Analyze derivation" (KE4ST) |
| Document splitting simulation | drill-down | sample document | lines before/after split, inherited flags, zero-balance lines | posting pipeline | — | FB50 "Simulate general ledger" (split view) |
| Reason code usage | operational report | reason code, module, period | count of reversals, clearings resets, amounts by code | `JournalEntry` (reversal) ⋈ `ReasonCode` | Reason codes form (no report) | T041C usage via FB03 reversal reason |
| Balance projection drift KPI | KPI | ledger | rows_differing from last `balance-verify`, duration, HWM | `balance.rebuilt` events | Financial dimension sets rebuild log | RFINDEX / SAPF190 comparison report |
| Idempotency replays | operational report | legal entity, date range, source system | key, first seen, replays, payload hash, conflicts (409) | idempotency table | — | — |

### 2.11.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Journal batch printout (unposted) | user action on Journal batch; optional on submit for approval | header: journal name, batch id, owner, status, totals; lines: line no., account, dimensions, D/C, currency, amount, text; footer: validation messages, approval chain | PDF, XLSX | batch id (not a legal number) | none; internal working paper, retained with batch 90 days after posting |
| Posted voucher | user action on entry; attached automatically to approval evidence when the journal name sets `print_on_post` | header: ledger, voucher, posting date, document date, description, source, trace id; lines: account, dimensions, D/C, txn/acct/rpt amounts, text, derived/inherited flags; footer: reversal links, attachments list, posted by/at, result hash | PDF, XLSX, CSV | voucher (gap-free, FR-11.6) | financial record; retention per `attachment.default_retention_class` (default 10 years) |
| Clearing statement | on clearing document creation or reset | header: account, clearing date, mode, document id; lines: cleared items with assignment, dates, amounts, residual/difference lines; footer: tolerance applied, actor | PDF | clearing document id | financial record; 10 years |
| Chart of accounts export | user action | account attributes incl. default dimensions, OI flag, recon type, suspended_from | XLSX, CSV, JSON | — | none |
| Account structure / advanced rule definition export | user action or version activation | segments, constraint rows, advanced rules and structures, version, effective_from | JSON, XLSX | structure version | configuration record; kept with the version indefinitely |
| Approval evidence pack | batch posted after approval | batch printout + workflow history (submitter, approvers, timestamps, comments) | PDF | batch id | audit evidence; 10 years |

No e-invoice or EDI documents originate in this wave.

### 2.11.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `balance-verify` (rebuild into shadow, compare, swap only on diff) | daily 01:00 tenant time zone | ledger | run id per (ledger, date); chunk checkpoints per fiscal period (BR-11.7) | 10M lines < 15 min | retry 1× after 10 min; partial chunks rolled back per chunk | `balance.rebuilt.rows_differing > 0` → P1; duration > 60 min → P3 |
| `create-partitions` | monthly, 1st 00:30 | tenant shard | idempotent `CREATE TABLE IF NOT EXISTS` for next fiscal year | < 1 min | alert, retry daily | missing partition for next year within 60 days → P2 |
| `recurring-journal-generate` | per template cron; default last business day of period 06:00 | template | one batch per (template, scheduled date) key; replay returns existing batch | 1,000 templates < 5 min | failed template marked; others continue | failed templates > 0 → P3; batch not generated by due date → P3 |
| `gl-auto-clear` | daily 02:00 | ledger × rule | run id per (ledger, rule, date); groups cleared individually with idempotency key `auto:{rule}:{group_hash}` | 1M open items < 10 min | group-level failures logged, run continues | cleared count = 0 for 3 consecutive runs on a rule with open items → P3 |
| `idempotency-purge` | daily 03:00 | tenant | deletes records older than `idempotency_ttl_days`; checkpoint by created_at | < 5 min per 10M keys | retry next day | table growth > 2× expected → P3 |
| `dimension-value-reconcile` (compare backing entities with `DimensionValue`) | daily 04:00 | dimension attribute | idempotent upsert; diff report stored | < 2 min | alert | diff count > 0 → P3 |
| `attachment-retention-sweep` (wave 5 framework, ledger hook) | weekly Sunday 05:00 | legal entity | retention class evaluation; deletes only past expiry | < 10 min | skip and retry | failures → P3 |
| `open-item-aging-snapshot` | daily 05:30 | ledger | one row per (account, date) | < 2 min | retry | — |

### 2.11.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| Maintain chart of accounts | PUT /ledger/charts/*/accounts; screen Chart of accounts; import CSV | Post ledger journals; Maintain ledger journals | Financial controller, Accounting manager |
| Maintain financial dimensions | PUT dimension attributes/values; screen Financial dimensions | Post ledger journals | Accounting manager |
| Maintain account structures | PUT account-structures, advanced-rules; activation; screen Account structures | Post ledger journals | Accounting manager |
| Maintain ledger setup | PUT description-templates, splitting-rules, ledgers; screen Ledger setup, Document splitting setup | Post ledger journals; Maintain ledger journals | Accounting manager, System administrator (finance) |
| Maintain reason codes | PUT /ledger/reason-codes; screen Reason codes | — | Accounting manager |
| Maintain ledger journals | POST/PATCH journal-batches, :submit; screen Journals, Journal batch; attachments | Approve ledger journals (same batch); Maintain chart of accounts | Accountant, Accounting clerk |
| Approve ledger journals | :approve, :reject; Temporal task UI | Maintain ledger journals (same batch); Post ledger journals without approval | Accounting supervisor, Financial controller |
| Post ledger journals | :post on batches; POST /ledger/validate | Maintain chart of accounts; Maintain validation and derivation rules | Accountant (senior), Accounting supervisor |
| Post ledger journals without approval | `auto_post` templates, direct post when journal name has no workflow | Maintain ledger journals; Approve ledger journals | Financial controller (limited) |
| Post journal entries (service accounts) | POST /ledger/journal-entries with `source.system ≠ ledger` | not assignable to human users | Subledger service accounts (waves 16+) |
| Reverse journal entries | POST /journal-entries/{id}/reverse; screen Ledger transactions > Reverse | Approve ledger journals (same entry) | Accounting supervisor, Financial controller |
| View ledger transactions | GET journal-entries, journal-lines, open-items; drill-downs | — | All finance roles, Internal auditor |
| View ledger balances | GET balances; Trial balance; Dimension statement | — | All finance roles, Internal auditor, Department manager (own dimensions via OPA) |
| Clear open items | POST /ledger/clearings, :simulate; screen Open items and clearing | Reset clearings; Maintain clearing rules; Maintain bank reconciliation (wave 36) | Accountant (senior), Treasury accountant |
| Reset clearings | POST /clearings/{id}:reset | Clear open items | Accounting supervisor |
| Maintain clearing rules | PUT auto-clearing-rules, tolerance-groups and assignments | Clear open items; Run automatic clearing | Accounting manager |
| Run automatic clearing | POST /ledger/auto-clearing:run; job `gl-auto-clear` | Maintain clearing rules | Accounting supervisor, Finance batch operator |
| Maintain validation and derivation rules | PUT validation-rules, derivation-strategies, lookup-tables; :test | Post ledger journals; Run dimension realignment | Accounting manager, Finance systems analyst |
| Run dimension realignment | POST /ledger/realignments, :post, :reverse | Maintain validation and derivation rules; Approve ledger journals | Financial controller |
| Administer ledger | balances:rebuild, accounts:activate-open-items, config, partitions | Post ledger journals | Finance platform administrator |

### 2.11.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| LEDGER-0001 `LEDGER_UNBALANCED` | error | Entry does not balance in {currency}: debits {debits}, credits {credits}. | Σ D ≠ Σ C in a currency column | Correct the line amounts or add the clearing line | BR-11.1, BR-12.5 |
| LEDGER-0002 `LEDGER_LINE_LIMIT` | error | Entry has {count} lines; the limit is {max}. | More than `max_lines_per_entry` | Split the document by `document_id` | FR-11.5 |
| LEDGER-0003 `LEDGER_LINE_EMPTY` | error | Line {line} has no amount, quantity or text. | Zero line without statistical content | Remove the line or add quantity/text | BR-11.2 |
| LEDGER-0004 `LEDGER_AMOUNT_SCALE` | error | Amount {amount} has more decimals than {currency} allows ({scale}). | Excess precision | Round in the caller | BR-11.3 |
| LEDGER-0005 `LEDGER_ACCOUNT_NOT_FOUND` | error | Account {account} does not exist in chart {chart}. | Unknown account | Create the account or correct the number | FR-11.1 |
| LEDGER-0006 `LEDGER_ACCOUNT_NOT_POSTABLE` | error | Account {account} is a total or reporting account. | `is_postable = false` | Use a postable account | FR-11.1 |
| LEDGER-0007 `LEDGER_ACCOUNT_SUSPENDED` | error | Account {account} is suspended from {date}. | `suspended_from ≤ posting_date` | Use another account or lift the suspension | FR-11.1 |
| LEDGER-0008 `LEDGER_RECON_ACCOUNT_MANUAL` | error | Account {account} accepts subledger postings only. | Manual line on a reconciliation account | Use a journal name with `allow_recon_accounts` or post through the subledger | BR-11.8 |
| LEDGER-0009 `LEDGER_STRUCTURE_NOT_FOUND` | error | No active account structure covers account {account} on {date}. | Structure filter gap | Extend a structure's main-account filter | FR-11.3 |
| LEDGER-0010 `LEDGER_DIMENSION_REQUIRED` | error | Dimension {attribute} is required for account {account}. | Blank not allowed by the structure or advanced rule | Supply the dimension value | FR-11.3, FR-11.16 |
| LEDGER-0011 `LEDGER_DIMENSION_INVALID` | error | Value {value} is not allowed for {attribute} under structure {structure}. | Constraint row mismatch | Choose an allowed value | FR-11.3 |
| LEDGER-0012 `LEDGER_DIMENSION_INACTIVE` | error | Value {value} of {attribute} is not active on {date}. | Effective dates / suspended | Use an active value or adjust dates | FR-11.2 |
| LEDGER-0013 `LEDGER_PERIOD_CLOSED` | error | Period {period} is closed for module {module}. | Period status | Post in an open period or request reopen (wave 14) | BR-11.4, BR-14.1 |
| LEDGER-0014 `LEDGER_PERIOD_ON_HOLD` | error (403) | Period {period} is on hold; your user group may not post to it. | On-hold without membership | Ask the controller group to post or reopen | BR-14.1 |
| LEDGER-0015 `LEDGER_IDEMPOTENCY_CONFLICT` | error (409) | Idempotency key {key} was already used with a different payload. | Key reuse | Use a new key for a new request | BR-11.6 |
| LEDGER-0016 `LEDGER_IDEMPOTENCY_IN_PROGRESS` | error (409) | A request with key {key} is still being processed. | Concurrent duplicate | Retry after 1 s | FR-11.5 |
| LEDGER-0017 `LEDGER_REVERSAL_ALREADY_REVERSED` | error | Entry {voucher} was already reversed by {reversing_voucher}. | Second reversal | Reverse the reversing entry to reinstate | FR-11.8 |
| LEDGER-0018 `LEDGER_REASON_REQUIRED` | error | A reason code is required for this reversal. | `reversal_reason_required` | Select a reason code (and comment if required) | FR-11.15 |
| LEDGER-0019 `LEDGER_REASON_INVALID_MODULE` | error | Reason code {code} does not apply to {module}. | `applies_to` mismatch | Choose a code valid for the module | FR-11.15 |
| LEDGER-0020 `LEDGER_CLEARING_UNBALANCED` | error | Selected items leave a residual of {residual} {currency}, above the tolerance {limit}. | BR-11.12 | Add items, create a residual item, or adjust the selection | FR-11.19 |
| LEDGER-0021 `LEDGER_OPEN_ITEM_ALREADY_CLEARED` | error | Item {item} is already cleared by document {document}. | BR-11.13 | Reset the earlier clearing first | FR-11.19 |
| LEDGER-0022 `LEDGER_OPEN_ITEM_ACCOUNT_REQUIRED` | error | Account {account} is not open-item managed. | Clearing on a non-OI account | Activate open-item management | BR-11.18 |
| LEDGER-0023 `LEDGER_OI_ACTIVATION_BALANCE` | error | Open-item activation needs an activation run because account {account} has a balance. | Flag changed on an account with history | Run accounts:activate-open-items | FR-11.19 |
| LEDGER-0024 `LEDGER_VALIDATION_RULE_FAILED` | error / warning | {rule_message} (rule {rule_id}, line {line}). | User-defined rule | Follow the rule's remediation text | FR-11.21 |
| LEDGER-0025 `LEDGER_DERIVATION_INVALID` | error | Derivation strategy version {version} cannot be activated: {detail}. | Parse error, > 50 steps, cyclic read/write | Fix the step | BR-11.14 |
| LEDGER-0026 `LEDGER_SPLIT_NO_SOURCE` | error | Line {line} cannot inherit {attribute}: no carrying line and no default value. | FR-11.23 | Add a value on the line or set `default_splitting_value` | BR-11.15 |
| LEDGER-0027 `LEDGER_SPLIT_CLEARING_ACCOUNT_MISSING` | error | Document splitting is active but no zero-balance clearing account is set for ledger {ledger}. | Configuration | Set the account in Document splitting setup | FR-11.23 |
| LEDGER-0028 `LEDGER_ADVANCED_RULE_SEGMENT_LIMIT` | error | Advanced rules add more than 20 segments for account {account}. | FR-11.16 | Reduce the matching rules' segments | FR-11.16 |
| LEDGER-0029 `LEDGER_ATTACHMENT_RETAINED` | error | Attachment {name} is retained until {date} and cannot be deleted. | Retention class | Wait for expiry or request a legal-hold exception (wave 5) | FR-11.18 |
| LEDGER-0030 `LEDGER_REALIGNMENT_PERIOD_CLOSED` | error | Realignment touches period {period}, which is closed for GL. | Period control | Exclude the period or reopen it | FR-11.24 |
| LEDGER-0031 `LEDGER_DESCRIPTION_TRUNCATED` | info | Description was truncated to 255 characters. | Template expansion | Shorten the template | FR-11.17 |
| LEDGER-0032 `LEDGER_NEGATIVE_POSTING_DISABLED` | error | Reason code {code} allows negative posting but the ledger does not. | `negative_posting_enabled = false` | Use another reason code or enable the parameter | BR-11.16 |

### 2.11.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Post journal entry (2–50 lines, incl. derivation, splitting, validation rules) | 200k lines/day per tenant; peak 5,000 lines/s at month-end | 40 / 120 / 200 ms | 5,000 lines/s sustained 30 min | 400 B/line × 50M lines = 20 GB + 60% indexes ≈ 32 GB | `journal_line` range-partitioned by fiscal year; indexes §2.11.4 | HPA to 12 pods; sequence lock serializes per ledger; p99 may rise to 600 ms; no rejection below 3× |
| Validate (dry run) | 50k calls/day | 20 / 60 / 100 ms | 1,000 req/s | none | stateless | same as posting |
| Balance query (one account, 12 periods, group by dimension) | 100k/day | 30 / 150 / 300 ms | 500 req/s | — | PK clustered on `balance` | read replica; cached dimension sets 60 s |
| Line query page (keyset, 1,000 rows) | 50k/day | 50 / 200 / 300 ms | 300 req/s | — | (ledger, account, posting_date) partition pruning | replica; page size reduced to 500 under pressure |
| Balance rebuild | nightly, 10M lines | — | 10M lines < 15 min | shadow table transient | chunk per fiscal period ≤ 5M lines | runs on replica snapshot; swap skipped if lock not acquired in 2 s |
| Manual clearing (≤ 10,000 items) | 5k clearings/day | 100 / 400 / 800 ms | 50/s | 150 B/open item; 2M items/year ≈ 300 MB | `open_item` index (ledger, account, status, assignment) | item selection capped at 10,000; larger via auto-clear |
| `gl-auto-clear` | 1M open items nightly | — | 1M items < 10 min | — | grouping in SQL per rule with hash on criteria | per-rule timeout 20 min; remaining rules next run |
| Derivation strategy evaluation | every posted line | ≤ 0.2 ms per line (50 steps) | — | lookup tables ≤ 100k rows each, memory-resident per pod | lookup tables cached, invalidated by `rules.changed` | strategy cache miss adds one query per pod |
| Document splitting | 30% of entries | ≤ 1 ms per entry of 50 lines | — | +25% lines on split entries | — | — |
| Realignment run | 10/month; ≤ 5M lines | — | 1M lines < 5 min (simulate), < 10 min (post) | reclassification entries ≈ 2 lines per combination × period | chunked by period | run queued; one active run per ledger |
| Idempotency table | 200k keys/day × 400 days ≈ 80M rows | lookup ≤ 1 ms | — | 120 B/key ≈ 10 GB | PK (tenant, legal_entity, key); purged daily | — |

### 2.11.20 Localization and compliance hooks

- Chart of accounts templates by country (German SKR03/SKR04, French Plan Comptable Général with mandatory classes 1–7, Indian Schedule III grouping, Spanish PGC, US/UK free-form): wave 48 country packs ship `ChartTemplate` JSON imported through PUT /ledger/charts; account number validation regex per template.
- Account name translations: `name` is translatable through wave 3 metadata; statutory languages per legal entity come from wave 6.
- Reversal practice: negative posting (storno) is the default expectation in DE/AT/CH/IT; country packs set `negative_posting_enabled = true` and ship reason codes flagged `negative_posting_allowed`; US/UK packs keep opposite-side reversal.
- Gap-free voucher numbering (FR-11.6) satisfies IT, FR, PT, ES and PL sequential-numbering rules; country packs may require a separate sequence per document class (e.g., IT sectional numbering) through the wave 5 number sequence scopes.
- Posting date vs document date: FR-11.5 accepts both; country packs add validation rules (FR-11.21) such as "document date ≤ posting date" (DE GoBD) or "posting within 30 days of document date".
- Retention: `attachment.default_retention_class` and journal retention map to statutory minimums (DE HGB §257 / AO §147: 10 years; US IRS: 7 years; UK: 6 years; FR: 10 years); the journal itself is never purged in Phase 2; archival export hooks are provided to wave 48 audit files (SAF-T via wave 13, GDPdU/GoBD export, FEC for France) through the line query API with stable ordering.
- Audit-file fields: `JournalLine` carries every field FEC (FR) and GoBD (DE) require (voucher, journal code via journal name, posting date, account, text, debit, credit, document date, validation date = posted_at); SAF-T `GeneralLedgerEntries` is produced by wave 13 from the same API.
- Privacy: `text`, `description` and attachment contents may hold personal data; they are tagged `pii_possible` for wave 7 data-subject request searches; no deletion from the journal — masking is applied on export only when legally required.
- Calendar and currency: fiscal calendars (wave 6) and ISO 4217 minor units (wave 12) are the only currency/calendar dependencies; rounding mode per ledger (BR-12.2) is set by country packs where commercial rounding differs.
- Extension point: `ledger.country_pack` hook set = chart template import, validation rule pack, description template language pack, reason code pack, and audit-file exporter registration; packs are declarative (JSON) and versioned with the wave 48 pack registry.

### 2.11.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.1.1 Common / Journals — Voucher attachments / document handling (`DocuView`) (AX register row 1) | PARTIAL-11 | ADDED as FR-11.18 | document types, retention class, store reference from wave 5 framework; attachment immutability after posting |
| AX §12.1.3 Periodic — Reason code assignment (reversal reasons) (AX register row 2) | PARTIAL-11 | ADDED as FR-11.15 | reason code required on reversal; negative posting semantics BR-11.16 |
| AX §12.1.4 Setup — Chart of accounts → Advanced rule structures / Advanced rules (AX register row 3) | PARTIAL-11 | ADDED as FR-11.16 | advanced rules versioned with account structures; AC-11.18 |
| AX §12.1.4 Setup — Journals → Default descriptions (Transaction text) (AX register row 5) | PARTIAL-11 | ADDED as FR-11.17 | templates per source_type × language; AC-11.19 |
| AX §12.1.4 Setup — Posting → Reason codes (financial reasons) (AX register row 6) | PARTIAL-11 | ADDED as FR-11.15 | shared `ReasonCode` with `applies_to[]` |
| AX §12.3.4 Setup — Sales → Sales reasons / Reason codes (customer) (AX register row 32) | PARTIAL-11 | ADDED as FR-11.15 | `applies_to` includes customer; AR (wave 30) consumes |
| AX §12.6.4 Setup — Fixed asset → Reason codes (fixed assets) (AX register row 44) | PARTIAL-11 | ADDED as FR-11.15 | `applies_to` includes fixed_asset; wave 37 consumes |
| AX §12.14.4 Setup — Inventory → Counting → Counting reason code policies (AX register row 137) | PARTIAL-11 | ADDED as FR-11.15 (codes); policies DEFERRED to wave 16 | code list here; the counting policy (which codes are mandatory per warehouse) belongs to the inventory ledger wave |
| AX §12.29.1 Common / Journals — Reason codes (shared financial reasons across modules) (`ReasonTable`) (AX register row 239) | PARTIAL-11 | ADDED as FR-11.15 | |
| AX §12.29.4 Setup — Reason codes (Financial reasons: ledger, bank, customer, vendor, fixed asset) (AX register row 258) | PARTIAL-11 | ADDED as FR-11.15 | screen Reason codes; API /ledger/reason-codes |
| SAP §13.1 FI-GL Master data & documents — F-03 / F-04 Clear G/L account / post with clearing (SAP register row 1) | PARTIAL-11 | ADDED as FR-11.19 | manual clearing, post-with-clearing, reset (FBRA); AC-11.12 |
| SAP §13.1 FI-GL Master data & documents — FAGL_ACTIVATE_OP (SAP register row 4) | PARTIAL-11 | ADDED as FR-11.19 | activation run creating opening open items |
| SAP §13.1 FI-GL Master data & documents — FAGL_SWITCH_TO_OPEN_ITEM (SAP register row 5) | PARTIAL-11 | ADDED as FR-11.19 | same activation run; deactivation requires zero open items |
| SAP §13.1 FI-GL Periodic processing — F.13 / F13E Automatic clearing (SAP register row 7) | PARTIAL-11 | ADDED as FR-11.20 | job `gl-auto-clear`; AC-11.13 |
| SAP §13.1 FI-GL Customizing — OB28 / GGB0 Validations (SAP register row 12) | PARTIAL-11 | ADDED as FR-11.21 | CEL prerequisite/check/message; AC-11.14 |
| SAP §13.1 FI-GL Customizing — OBBH / GGB1 Substitutions (SAP register row 13) | PARTIAL-11 | ADDED as FR-11.22 | derivation strategy steps |
| SAP §13.1 FI-GL Customizing — OBBZ / OBBW Substitution/validation for cost of sales accounting (SAP register row 14) | PARTIAL-11 | ADDED as FR-11.22 | FUNC_AREA attribute + shipped lookup step; AC-11.15 |
| SAP §13.1 FI-GL Customizing — Document splitting (FAGL_SPLIT*) (SAP register row 15) | PARTIAL-11 | ADDED as FR-11.23 | active/passive inheritance, zero-balance lines; AC-11.16 |
| SAP §13.1 FI-GL Customizing — OBA0 Tolerance groups for G/L accounts (SAP register row 18) | PARTIAL-11 | ADDED as FR-11.20 | `ToleranceGroup` per account and user; BR-11.12 |
| SAP §13.1 FI-GL Customizing — OB74 Additional rules for automatic clearing (SAP register row 20) | PARTIAL-11 | ADDED as FR-11.20 | `AutoClearingRule.criteria` |
| SAP §13.1 FI-GL Customizing — Cost of sales accounting: functional areas (OKBD), FAGL_FUNC_AREA (SAP register row 22) | PARTIAL-11 | ADDED as FR-11.22 | functional area is a standard dimension attribute; P&L by function in wave 15 |
| SAP §13.9 CO-PA Master data & documents — KEDR / KEDE derivation strategy (SAP register row 102) | PARTIAL-11 | ADDED as FR-11.22 | table_lookup steps with validity dates |
| SAP §13.9 CO-PA Periodic processing — KEND / KEDR realignment run (SAP register row 105) | PARTIAL-11 | ADDED as FR-11.24 | append-only reclassification; AC-11.17 |
| SAP §13.9 CO-PA Customizing — KEDR derivation rules (table lookup, rule, move, clear, enhancement) (SAP register row 106) | PARTIAL-11 | ADDED as FR-11.22 | enhancement exits → `expression` (CEL) steps |
| SAP §13.9 CO-PA Customizing — 0KEM / 0KEP PCA substitution / derivation of PC (SAP register row 107) | PARTIAL-11 | ADDED as FR-11.22 | profit center derived from (sales org, material group) lookup tables once waves 8/27 supply the fields |
| SAP §13.11 FM / PSM Master data & documents — FMDERIVE (SAP register row 121) | PARTIAL-11 | ADDED as FR-11.22 | FM account assignment (fund, funds center, commitment item) modeled as dimension attributes; budget control in wave 38 |
| SAP §13.11 FM / PSM Customizing — FMDERIVE strategy (SAP register row 122) | PARTIAL-11 | ADDED as FR-11.22 | strategy versions per ledger |

## 2.12 Wave 12: Multi-Currency, Parallel Ledgers & Revaluation

### 2.12.1 Scope and business context

AX carries transaction, accounting and reporting currency on `GeneralJournalAccountEntry` and uses posting layers (current/operations/tax); SAP New GL carries currency types (00 document, 10 company code, 30 group) on `ACDOCA` and posts to leading and non-leading ledgers through ledger groups. Wave 12 adds to the journal: an exchange rate service (`fx`, Go) with rate types, provider adapters, triangulation and audit; three-currency amounts with ISO 4217 rounding; ledger groups so one document posts to N ledgers with per-ledger accounting differences; FX revaluation of GL balances (open-item revaluation for AP/AR uses the same engine when those waves arrive); and currency translation for reporting-currency restatement.

After this wave a user can post a EUR invoice into a USD-accounting entity with EUR reporting currency, see the amounts in all three currencies, run the month-end revaluation and see it auto-reverse. Out of scope: realized gain/loss on settlement (waves 19 and 30), group-currency consolidation translation (wave 40), hedge accounting.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| SAP OB08 / `TCURR`: rates entered per 100 units with `TCURF` factors; a wrong factor silently converts amounts 100x; no deviation check | Factor table separate from rate table; no validation against the previous rate | Rates stored as plain `numeric(20,10)` "1 from = rate to"; > 10% deviation requires confirmation and emits a warning event | Factor-related misposting incidents → 0; deviation flagged in < 100 ms at entry |
| AX `ExchangeRate` import batch (`ExchangeRateProviderImport`) runs on a batch group; a failed run leaves no rate and postings fail hours later with "no exchange rate" | Batch framework with no fallback or alerting | Temporal schedule with retries; last-known rate stays valid; `fx.provider.degraded` alerts immediately | Posting failures from missing rates → 0; outage detected < 1 min (vs discovered at posting) |
| Rounding differences between `AccountingCurrencyAmount` and `ReportingCurrencyAmount` land in penny-difference accounts with no trace to the line (AX `LedgerParameters`); SAP rounding to OB00 account per document | Each line rounded independently; residual posted to a separate account without lineage | Residual allocated to the largest line and recorded as `rounding_adjustment` with the tolerance rule in BR-12.4 | Unexplained rounding postings → 0; every residual traceable to its line |
| Parallel valuation needs SAP ledger groups + FB50L and separate runs per ledger, or AX posting layers that most reports ignore | Ledger-specific posting bolted onto a single-ledger document model | One request to `ledger_group: ALL` yields N balanced entries sharing `document_id`, atomically | Per-ledger reconciliation effort 2 days/close → automated check; ledger-specific adjustments 1 call |
| FAGL_FC_VAL / AX revaluation runs take hours on large `BSEG` sets and users rerun after rate corrections, producing duplicate adjustments | Open-item scan over unindexed valuation sets; no run idempotency | Revaluation selects from `Balance` keyed by currency; runs are idempotent per (ledger, period, rate type) and auto-reverse | Revaluation of 100k combinations < 5 min (vs 1–4 h); duplicate adjustment runs → 0 |

### 2.12.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Exchange rate types | `ExchangeRateType`, Currency exchange rate types form | `TCURV` (rate types M, EURX, P), OB07 | `fx.ExchangeRateType` | full |
| Exchange rates | `ExchangeRate`, `ExchangeRateCurrencyPair`, OB08-style form | `TCURR`, OB08, `TCURF` (factors) | `fx.ExchangeRate` with `from`, `to`, `valid_from`, `rate` numeric(20,10); factor handled by rate precision | full |
| Rate providers | Exchange rate providers (OANDA, Central Bank of Europe) | Market data interface (datafeed) | `fx` provider adapters: ECB, OpenExchangeRates, manual, CSV | full |
| Triangulation | Euro triangulation | `TCURR` via reference currency (`TCURV-BWAER`) | `fx` triangulation through ledger reference currency | full |
| Three currencies per line | `TransactionCurrencyAmount`, `AccountingCurrencyAmount`, `ReportingCurrencyAmount` | `WSL` (document), `HSL` (local), `KSL` (group) in ACDOCA | `amount_txn`, `amount_acct`, `amount_rpt` on JournalLine | full |
| Parallel ledgers / posting layers | Posting layers Current/Operations/Tax (`LedgerPostingLayer`) | Leading ledger 0L, non-leading ledgers, ledger groups, `RLDNR` | `ledger.Ledger` × `LedgerGroup`; entries per ledger share `document_id` | improved: unlimited ledgers, per-ledger calendar and currency |
| FX revaluation (GL) | Foreign currency revaluation (General ledger), `LedgerExchAdj` | FAGL_FC_VAL (balances), valuation methods OB59, valuation areas | `ledger.RevaluationRun` with method per ledger | full |
| Unrealized gain/loss accounts | Currency revaluation accounts per currency / ledger | OBA1 (KDB/KDF), OB09 per recon account | `CurrencyPolicy` per ledger × currency: unrealized_gain, unrealized_loss, adjustment accounts | full |
| Currency translation (reporting currency) | Reporting currency on ledger, revaluation of reporting currency | Currency translation (FAGL_FCV / consolidation) | `ledger.TranslationRun` (monthly, average/closing/historical by account type) | partial: group consolidation in wave 40 |
| Rounding differences | Penny difference accounts (`LedgerParameters`) | Rounding difference account (OB00), tolerance | `CurrencyPolicy.rounding_difference_account`, tolerance per BR-12.4 | full |
| Rate audit / history | Database log on `ExchangeRate` | `TCURR` change documents | `fx.ExchangeRate` is append-only with `superseded_by` | improved |
| Market data beyond FX (interest rates, indices, prices) | Not available (exchange rates only) | TBDM market data import, market data classes (`VTB_MARKET`, `JBD*`), rate types / yield curves / volatility customizing | `fx.MarketDataSeries` (kind ∈ {fx_rate, interest_rate, index, price}) with the same providers, audit and lookup (FR-12.13); curves and security valuation in wave 36 | partial: series storage and import here, valuation in wave 36 |

### 2.12.3 Functional requirements

- **FR-12.1 Currency reference data and policy** — Must. The system shall use ISO 4217 currencies from wave 6 (code, numeric code, minor units) and maintain a `CurrencyPolicy` per ledger × currency with gain/loss/adjustment/rounding-difference accounts.
  - Minor units default from ISO 4217 (JPY 0, USD/EUR/GBP/INR 2, KWD/BHD/OMR 3, CLF 4); a tenant may not override them.
  - Non-ISO currencies (e.g., XTS test, loyalty points) can be defined with explicit minor units and flagged `is_statistical`.
- **FR-12.2 Exchange rate types and rates** — Must. The system shall maintain rate types (default, average, closing, budget, custom) and rates by (type, from, to, valid_from) with precision numeric(20,10).
  - Rate direction is `from → to` as "1 from = rate to"; the reciprocal is computed only if no explicit inverse rate exists and `allow_inverse` is set on the type.
  - Rates are append-only; a corrected rate for the same key supersedes the previous row and both are kept with `superseded_by`.
  - Maximum 10,000 pairs per type; history retained indefinitely.
- **FR-12.3 Rate lookup and triangulation** — Must. The system shall resolve a rate for (type, from, to, date) by: exact pair valid on date → inverse pair (if allowed) → triangulation through the ledger's reference currency (two hops, both must resolve) → `LEDGER_RATE_MISSING`.
  - Lookup is served from an in-memory cache per pod refreshed from `fx.rate.published` events; cold lookup p99 < 5 ms.
  - Triangulated amounts are computed in one step `amount × r1 × r2` at full precision, then rounded once.
- **FR-12.4 Provider adapters** — Must. The system shall import rates from ECB (daily reference rates), OpenExchangeRates and CSV through a Temporal schedule per rate type.
  - Provider timeout 10 s, 3 retries with exponential back-off; on failure the last-known rate remains valid and `fx.provider.degraded` is emitted; rates older than `max_rate_age_days` (default 5 business days) cause posting warnings, not rejections, unless the ledger sets `reject_stale_rates`.
  - Imported rates are stored with `source`, `provider_timestamp` and raw payload hash for audit.
- **FR-12.5 Three-currency amounts on every line** — Must. The posting API shall accept `amount` in any currency, convert to accounting and reporting currency using the ledger's default rate type (or an explicit `rate_override` with rate type and value), and store all three amounts rounded per currency.
  - If a line's currency equals the accounting currency, `amount_acct = amount_txn` exactly (no rate applied); likewise for reporting.
  - Explicit `rate_override` requires duty `Override exchange rates` and is recorded on the line.
  - An entry may contain lines in multiple transaction currencies; balancing is checked per transaction currency as well as in accounting and reporting currency.
- **FR-12.6 Rounding and residual allocation** — Must. The system shall apply ISO 4217 minor-unit rounding (half away from zero) per derived amount, then allocate any residual so that each derived column balances.
  - Algorithm in BR-12.4; the residual goes to the line with the largest absolute amount on the side that needs it; if the residual exceeds tolerance the post is rejected with `LEDGER_ROUNDING_EXCEEDED`.
- **FR-12.7 Ledger groups and parallel posting** — Must. The system shall define ledger groups (e.g., `ALL`, `LEADING`, `LOCAL`, `TAX`) per legal entity; a posting request addressed to a group produces one `JournalEntry` per member ledger, all committed in one transaction and sharing `document_id`.
  - Each ledger has its own accounting principle code (IFRS, US-GAAP, HGB, TAX), fiscal calendar, accounting currency, reporting currency and default rate type; conversions are performed per ledger.
  - Per-ledger differences are expressed by the caller (subledgers send ledger-specific lines, e.g., different depreciation) or by `LedgerAccountMapping` rows that remap an account per ledger (e.g., IFRS 16 lease accounts to operating lease expense in local GAAP).
  - A request addressed to a single non-leading ledger (ledger-specific adjustment, SAP FB50L semantics) is allowed when the user holds duty `Post ledger-specific adjustments`.
- **FR-12.8 FX revaluation of GL balances** — Must. The system shall run a revaluation per ledger × period × rate type over balance-sheet accounts flagged `revalue = true`, computing unrealized gain/loss per account × dimension combination × transaction currency, posting it on the period-end date and auto-reversing it on the first day of the next period.
  - Method per ledger: `closing_rate` (default, FAGL_FC_VAL semantics with reversal) or `cumulative_no_reversal` (delta against prior revaluation, AX semantics); both are supported, the ledger chooses.
  - Simulation mode produces the proposal without posting; the proposal is retained 90 days.
- **FR-12.9 Currency translation for reporting currency** — Should. The system shall restate reporting-currency balances monthly: monetary balance-sheet accounts at closing rate, non-monetary at historical (the posted `amount_rpt`), P&L at average rate, with the difference posted to a cumulative translation adjustment (CTA) equity account.
- **FR-12.10 Exchange rates workspace** — Must. The system shall provide screens for rate types, rate entry/import, provider schedules, lookup tester and rate history.
- **FR-12.11 Rate audit** — Must. The system shall record who/what created each rate, the provider payload hash, and expose a history view by pair; a rate change that moves more than 10% from the previous value requires confirmation in the UI and emits a warning event.
- **FR-12.12 Posting-time currency validation** — Must. The system shall reject a transaction currency that is suspended or not enabled for the legal entity with `LEDGER_CURRENCY_MISMATCH`, and a missing rate with `LEDGER_RATE_MISSING` including the (type, pair, date) in the violation.
- **FR-12.13 Market data series** — Should. The `fx` service shall store, besides exchange rates, generic market data series of kind `interest_rate` (reference rates such as SOFR, €STR, EURIBOR 3M, SONIA by tenor), `index` (CPI, commodity indices) and `price` (security or commodity prices by identifier), each a series of (series_code, valid_from, value numeric(20,10), source, provider_timestamp, payload_hash), append-only with `superseded_by`, imported by the same provider schedule framework as FR-12.4 (ECB SDW, CSV, manual) and queryable by `GET /fx/series/{code}?date=`.
  - Series carry `unit` (percent per annum, index points, currency per unit), `day_count` for rates (ACT/360, ACT/365, 30/360) and `tenor` (ON, 1W, 1M, 3M, 6M, 12M) as metadata; no interpolation or curve bootstrapping in this wave.
  - Deviation warning (FR-12.11) applies with a series-specific threshold (default 25% for rates, 10% for prices).
  - Consumers: wave 36 (interest calculation, yield curves, security valuation), wave 38 (budget rate assumptions), wave 29 (index-linked subscription pricing).

### 2.12.4 Data model

**ExchangeRate** (fx)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| rate_type_id | uuid | yes | FK ExchangeRateType |
| from_currency / to_currency | char(3) | yes | ISO 4217; unique (type, from, to, valid_from, version) |
| valid_from | date | yes | effective until next valid_from for the same key |
| rate | numeric(20,10) | yes | > 0 |
| source | enum | yes | manual, ecb, openexchangerates, csv, migration |
| provider_timestamp | timestamptz | no | |
| payload_hash | bytea(32) | no | SHA-256 of provider record |
| superseded_by | uuid | no | FK ExchangeRate; non-null means not current |
| created_at / created_by | timestamptz / uuid | yes | |

**Ledger** (extended) and **LedgerGroup**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| legal_entity_id | uuid | yes | FK |
| code | text(10) | yes | e.g. `0L`, `IFRS`, `HGB`, `TAX`; unique per legal entity |
| accounting_principle | enum | yes | IFRS, US_GAAP, LOCAL_GAAP, TAX, MANAGEMENT |
| is_leading | bool | yes | exactly one leading ledger per legal entity |
| accounting_currency / reporting_currency | char(3) | yes | |
| reference_currency | char(3) | no | triangulation pivot (default accounting currency) |
| default_rate_type_id / closing_rate_type_id / average_rate_type_id | uuid | yes | FK ExchangeRateType |
| fiscal_calendar_id | uuid | yes | FK (wave 6) |
| rounding_mode | enum | yes | half_away_from_zero (default), half_even |
| revaluation_method | enum | yes | closing_rate, cumulative_no_reversal |
| group: id / legal_entity_id / code / member_ledger_ids | uuid / uuid / text / uuid[] | yes | `ALL` auto-maintained |

**RevaluationRun** and **RevaluationLine**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| ledger_id | uuid | yes | |
| fiscal_period_id | uuid | yes | |
| rate_type_id | uuid | yes | typically closing |
| valuation_date | date | yes | period end |
| mode | enum | yes | simulate, post |
| status | enum | yes | proposed, posted, reversed, cancelled |
| posting_entry_id / reversal_entry_id | uuid | no | FK JournalEntry |
| totals_gain / totals_loss | numeric(23,6) | yes | accounting currency |
| line: account_id, dimension_combination_id, currency_txn | | yes | key |
| line: balance_txn, balance_acct_posted, rate_used, balance_acct_revalued, adjustment | numeric | yes | adjustment = revalued − posted |

**MarketDataSeries** (fx, FR-12.13): `id`, `code` text(30) unique per tenant (e.g., `EURIBOR_3M`, `SOFR_ON`, `CPI_US`), `kind` enum (interest_rate, index, price), `unit` text, `currency` char(3) optional, `tenor` enum optional, `day_count` enum optional, `provider_id` optional, `active` bool; **MarketDataPoint**: `series_id`, `valid_from` date, `value` numeric(20,10), `source` enum, `provider_timestamp`, `payload_hash` bytea(32), `superseded_by` uuid, `created_at/by`; unique (series_id, valid_from, version); index (series_id, valid_from DESC) WHERE superseded_by IS NULL.

**Invariants**: `amount_acct = round(amount_txn × rate, minor_units(acct))` per line before residual allocation; after allocation Σ D = Σ C in every column; exactly one leading ledger per legal entity; every member of a ledger group belongs to the same legal entity; a revaluation run that is `posted` with method `closing_rate` has a reversal entry dated period_end + 1 day.

**Indexing**: `exchange_rate` index on (rate_type_id, from_currency, to_currency, valid_from DESC) WHERE superseded_by IS NULL; `journal_line` adds index on (ledger_id, currency_txn, posting_date) for revaluation selection; `revaluation_line` PK (run_id, account_id, dimension_combination_id, currency_txn).

### 2.12.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| GET/PUT | /api/v1/fx/rate-types[/{code}] | Rate type CRUD | PUT idempotent | Maintain exchange rates |
| POST | /api/v1/fx/rates | Create rate(s), bulk up to 5,000 | `Idempotency-Key` | Maintain exchange rates |
| GET | /api/v1/fx/rates?type=&from=&to=&date= | Resolve a rate (with triangulation path) | n/a | View exchange rates |
| GET | /api/v1/fx/rates/history?type=&from=&to= | Rate history incl. superseded | n/a | View exchange rates |
| POST | /api/v1/fx/providers/{provider}:import | Trigger import now | run id | Administer exchange rates |
| POST | /api/v1/fx/convert | Convert amount(s) with rounding (stateless) | none | View exchange rates |
| GET/PUT | /api/v1/ledger/ledgers/{id}/currency-policies/{currency} | Gain/loss/rounding accounts | PUT idempotent | Maintain ledger setup |
| GET/PUT | /api/v1/ledger/legal-entities/{le}/ledger-groups[/{code}] | Ledger group membership | PUT idempotent | Maintain ledger setup |
| POST | /api/v1/ledger/revaluations | Create run (simulate or post) | `Idempotency-Key` | Run foreign currency revaluation |
| GET | /api/v1/ledger/revaluations/{id}[/lines] | Inspect proposal/results | n/a | View ledger balances |
| POST | /api/v1/ledger/revaluations/{id}:post | Post a simulated proposal | `Idempotency-Key` | Run foreign currency revaluation |
| POST | /api/v1/ledger/translations | Reporting-currency translation run | `Idempotency-Key` | Run currency translation |
| GET/PUT | /api/v1/fx/series[/{code}] | Market data series definitions (FR-12.13) | PUT idempotent | Maintain exchange rates |
| POST | /api/v1/fx/series/{code}/points | Append points, bulk up to 5,000 | `Idempotency-Key` | Maintain exchange rates |
| GET | /api/v1/fx/series/{code}?date= and /history | Resolve a value on a date; full history | n/a | View exchange rates |

Posting request extensions (wave 11 contract, additive): `"ledger_group": "ALL"`, per line `"amount": {"currency":"EUR","value":"1000.00"}`, optional `"rate_override": {"rate_type":"M","rate":"1.0850000000"}`, optional `"ledger_lines": {"HGB": [ … ]}` for ledger-specific substitutions. Response contains `entries[]` one per ledger with `amounts: {txn, acct, rpt}` and `rates_applied[]`.

Commands: `PublishRate` (rate > 0, currencies enabled, 10% deviation warning), `RunRevaluation` (period open for module GL, accounts flagged, closing rate exists for every currency with balance else `LEDGER_RATE_MISSING`), `RunTranslation`.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.fx.rate.published.v1 | rate committed | rate_type, from, to, valid_from, rate, previous_rate, source, deviation_pct | ledger cache, reporting, AP/AR |
| dev.passage.fx.provider.degraded.v1 | import failure after retries | provider, last_success_at, rate_type, error | ops alerting |
| dev.passage.ledger.revaluation.completed.v1 | run posted | run_id, ledger_id, period, rate_type, gain_total, loss_total, posting_entry_id, reversal_entry_id | reporting, close |
| dev.passage.ledger.translation.completed.v1 | translation posted | run_id, ledger_id, period, cta_amount, entry_id | reporting, close |
| dev.passage.fx.series.published.v1 | market data point committed | series_code, kind, valid_from, value, previous_value, source, deviation_pct | treasury (wave 36), budgeting (wave 38), subscription pricing (wave 29) |

### 2.12.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Exchange rate types | Create, set defaults per ledger | code, name, allow_inverse, provider | Maintain exchange rates |
| Exchange rates | Enter, import CSV, view history, compare to provider | type, from, to, valid_from, rate, source, deviation vs previous | Maintain exchange rates |
| Provider schedules | Configure provider, cron, test connection, run now | provider, rate type, pairs, last run, status | Administer exchange rates |
| Rate lookup tester | Enter type/pair/date, see resolution path | path (direct/inverse/triangulated), rate, valid_from | View exchange rates |
| Ledger setup | Ledgers, groups, currency policies, account mapping | code, principle, currencies, rate types, revaluation method, accounts | Maintain ledger setup |
| Foreign currency revaluation | Simulate, review proposal grid, post, view reversal | account, dims, currency, balance txn, posted acct, rate, revalued, adjustment | Run foreign currency revaluation |
| Journal line (extended) | Enter amount in any currency; see acct/rpt amounts live | currency, amount, rate (editable with duty), acct amount, rpt amount | Maintain ledger journals |

Interaction rules: the line grid shows derived amounts as read-only cells that recompute on currency/amount/date change within 100 ms; a rate override cell is editable only with the duty and is highlighted; the revaluation proposal grid supports excluding rows (recorded as exclusions on the run) and exporting to XLSX; a rate deviation > 10% opens a confirmation dialog with the previous rate.

### 2.12.7 Business rules and edge cases

- **BR-12.1 Minor units.** `scale(currency)` is the ISO 4217 minor unit; a stored amount must satisfy `amount = round(amount, scale)`; CHECK constraint `scale(amount) ≤ scale(currency)` is validated in the service and by a trigger.
- **BR-12.2 Rounding mode.** Default half-away-from-zero (matches AX `round()` and SAP commercial rounding); `half_even` is selectable per ledger for statistical ledgers only. Rounding occurs once per derived amount, after full-precision multiplication (numeric(38,16) intermediate).
- **BR-12.3 Rate date.** The rate for conversion is the one valid on `posting_date` unless the request supplies `rate_date` (allowed for AP/AR documents using document date); rate_date must be ≤ posting_date + 0 days (no future rates).
- **BR-12.4 Residual allocation.** For each derived column (acct, rpt): compute `rounded_i` per line; `residual = Σ rounded_debits − Σ rounded_credits`. If `|residual| ≤ tolerance = 0.5 × unit × line_count` (unit = 10^−scale), add `−residual` to the rounded amount of the line with the largest `|amount|` on the side that reduces the residual (ties: lowest line_number); the adjusted line records `rounding_adjustment`. If `|residual| > tolerance` reject with `LEDGER_ROUNDING_EXCEEDED`. When the entry has only one line per side and the residual is non-zero, post the residual to `rounding_difference_account` instead of adjusting the line, provided `|residual| ≤ 1 unit`.
- **BR-12.5 Multi-currency balancing.** Balancing is checked per transaction currency; an entry with EUR debit and USD credit must include explicit cross-currency lines (clearing) or is rejected with `LEDGER_UNBALANCED` listing the unbalanced currency.
- **BR-12.6 Parallel-ledger posting matrix.** For a request to ledger group `ALL` with leading `0L` (IFRS), `HGB` (local GAAP) and `TAX`:

| Source document type | 0L (IFRS) | HGB (local) | TAX | Rule |
|---|---|---|---|---|
| Manual journal, ledger_group ALL | post | post | post | identical lines, converted per ledger |
| Ledger-specific adjustment (single ledger) | post only if addressed | post only if addressed | post only if addressed | duty-gated |
| FX revaluation | post | post | skip if ledger.revalue = false | run per ledger |
| Account-mapped lines (`LedgerAccountMapping`) | original account | mapped account | mapped account | mapping keyed (ledger, account, effective_from) |
| Subledger with principle-specific lines (`ledger_lines`) | common lines + 0L lines | common lines + HGB lines | common lines + TAX lines | each ledger must balance independently |
| Year-end close (wave 14) | per ledger calendar | per ledger calendar | per ledger calendar | independent runs |

- **BR-12.7 Stale rates.** A rate older than `max_rate_age_days` yields a `warnings[]` entry `FX_RATE_STALE` in the response; with `reject_stale_rates` it is a 422 `LEDGER_RATE_MISSING`.
- **BR-12.8 Revaluation state machine.** `proposed → posted → reversed` (automatic at period+1 for closing_rate method) | `proposed → cancelled`; a second run for the same (ledger, period, rate type) while one is `posted` is rejected unless the first is reversed; cumulative method runs post the delta versus the last posted run.
- **BR-12.9 Revaluation selection.** Only balance-sheet accounts with `revalue = true` and non-zero `balance_txn` in a currency ≠ accounting currency; reconciliation accounts for AP/AR are revalued at open-item level in waves 19/30 and are excluded here (`revalue_level = open_item`).
- **BR-12.10 Provider precedence.** If two providers deliver the same (type, pair, date), the one with higher `priority` wins and the other is stored as `superseded`.
- **BR-12.11 Rate precision in display.** Rates display 6 decimals by default with full 10 on hover/export; amounts never display more than the currency scale.
- **BR-12.12 Market data series resolution.** A value for (series, date) is the point with the greatest `valid_from ≤ date` that is not superseded; no point → `FX_SERIES_VALUE_MISSING` (422); points older than `max_series_age_days` (default 10 business days for rates, 45 for indices) return `FX_SERIES_STALE` in `warnings[]`; the `fx` service never interpolates between points.

### 2.12.8 Acceptance criteria

- **AC-12.1** (FR-12.5, FR-12.6) Given a 3-line EUR entry posted to a USD ledger with rate 1.0850000000, when posted, then each `amount_acct` equals the rounded product, Σ D = Σ C in USD, and at most one line carries a `rounding_adjustment` of ≤ 0.01.
- **AC-12.2** (BR-12.1) Given 10,000 random amounts in JPY, KWD, USD and EUR converted in all directions, when summed per entry, then every entry balances to the minor unit and no stored amount has scale beyond the currency's.
- **AC-12.3** (FR-12.3) Given no GBP→CHF rate but GBP→EUR and EUR→CHF, when converting, then the response path is `triangulated` and the amount equals one-step rounding of `a × r1 × r2`.
- **AC-12.4** (FR-12.7, BR-12.6) Given the 20-document parallel-ledger fixture, when posted to `ALL`, then 0L, HGB and TAX each receive the expected lines (mapped accounts and ledger-specific lines) and each ledger's TB is 0.
- **AC-12.5** (FR-12.8) Given the revaluation fixture (12 accounts × 4 currencies), when run with closing rates, then unrealized gain/loss per line equals the workbook to the cent and a reversal entry exists dated period end + 1.
- **AC-12.6** (FR-12.4) Given the ECB adapter times out 3 times, when the schedule runs, then no rates change, `fx.provider.degraded` is emitted and a P2 alert fires.
- **AC-12.7** (FR-12.11) Given a manual rate 12% above the previous, when saved, then the UI requires confirmation and the event carries `deviation_pct = 12.0`.
- **AC-12.8** (FR-12.12) Given a line in a currency not enabled for the entity, when posted, then 422 `LEDGER_CURRENCY_MISMATCH`.
- **AC-12.9** (FR-12.9) Given the translation fixture, when run, then CTA equals total assets − liabilities − equity restated at the specified rates, to the cent.
- **AC-12.10** (FR-12.13, BR-12.12) Given series `EURIBOR_3M` with points on 2026-03-02 (2.4100) and 2026-03-03 (2.4300), when `GET /fx/series/EURIBOR_3M?date=2026-03-02` is called, then value 2.4100000000 with `valid_from = 2026-03-02`; when a point of 3.1000 is appended for 2026-03-04, then `fx.series.published` carries `deviation_pct = 27.57` and the UI requires confirmation.

### 2.12.9 Test plan and fixtures

- Fixtures: `iso4217-2026` (all active currencies with minor units); `rates-ecb-2025` (recorded ECB daily reference rates, 12 months); `amounts-random-10k` (seeded amounts across JPY/KWD/USD/EUR incl. extremes 0.001 and 9,999,999,999.999); `parallel-ledger-20` (20 documents with expected per-ledger lines, accountant-reviewed); `reval-workbook` (12 accounts × 4 currencies with XLSX expected values, FAGL_FC_VAL semantics); `translation-workbook`.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Rounding property test | property | `money` package | balance after allocation for all generated entries; residual ≤ tolerance |
| Rate resolution unit tests | unit | fx resolver | direct/inverse/triangulated/missing per table |
| Provider adapter contract | contract (recorded HTTP) | ECB, OXR adapters | parse recorded payloads; failure modes |
| Parallel-ledger fixture | integration | posting API | per-ledger lines and TB = 0 |
| Revaluation workbook | integration | revaluation job | adjustment per line equals workbook |
| Reversal timing | integration (Temporal time-skip) | revaluation workflow | reversal posted on period+1 |
| Rate cache consistency | integration | ledger pods | a published rate is used by all pods within 2 s |
| Conversion latency | performance | fx + posting | posting p99 stays < 200 ms with 3-currency conversion |

Reconciliation: per ledger per currency type TB = 0; Σ revaluation adjustments = posted gain − loss; `fx.rate.published` count = rate rows.

### 2.12.10 Deployment and operations

Helm chart `passage-fx` (deployment 2–6 pods, CloudNativePG `fx-db`, topics `fx.rates`, `fx.providers`); `passage-ledger` upgraded with currency policy tables. Config: `FX_MAX_RATE_AGE_DAYS=5`, `FX_PROVIDER_TIMEOUT_S=10`, `FX_DEVIATION_WARN_PCT=10`, `LEDGER_ROUNDING_MODE=half_away_from_zero`. Migrations: add `amount_rpt`/`currency_txn` backfill for wave 11 entries (= accounting amounts), add `ledger_group`, `currency_policy`, `revaluation_*`. Feature flags: `fx.triangulation`, `ledger.parallel_ledgers`, `ledger.translation`. Scheduled jobs: ECB import 16:30 CET business days; OXR hourly (if licensed); month-end revaluation on the close checklist (wave 14). SLOs: rate lookup p99 < 5 ms; import success ≥ 99% of business days. Alerts: provider degraded (P2), stale rate used in posting > 100/h (P3), revaluation run failure (P2). Runbook: manual rate entry during provider outage; re-run revaluation after rate correction (reverse then re-run). Rollback: digest rollback; feature flags off disable parallel posting (entries then go only to the leading ledger — documented as a degraded mode, not for production).

### 2.12.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX `ExchangeRateType`, `ExchangeRateCurrencyPair`, `ExchangeRate` | ExchangeRateType, ExchangeRate | AX stores rate per 100 units by default → divided; `ValidFrom` preserved |
| AX `Ledger` (accounting/reporting currency, posting layers) | Ledger, LedgerGroup | Current → leading; Operations/Tax layers → additional ledgers only if the customer used them |
| AX `LedgerExchAdj` history | not migrated | prior revaluations are embedded in migrated closing balances |
| SAP `TCURV`, `TCURR`, `TCURF`, `TCURX` | ExchangeRateType, ExchangeRate | rate = `UKURS × TFACT/FFACT`; `TCURX` decimals cross-checked against ISO 4217, exceptions reported |
| SAP `T882` (ledger assignment), `T881` (ledgers), ledger groups | Ledger, LedgerGroup | leading `0L` → is_leading; currency types 10/30 → accounting/reporting |
| SAP OBA1/OB09 gain/loss accounts | CurrencyPolicy | per ledger × currency |
| SAP TBDM market data (`VTB_MARKET`, reference interest rates `T056R`/`T056P`, index tables) | MarketDataSeries, MarketDataPoint | reference rate ids → series codes; yield curve definitions not migrated (wave 36 re-defines curves over migrated series) |

Validation: rate counts per type; sampled 1,000 conversions compared with source values to the cent; opening balances in all three currencies tie per ledger. Not migrated: provider credentials (re-entered in Vault), valuation run history.

### 2.12.12 Dependencies, risks, open questions

Depends on: wave 11 (journal, posting API), wave 6 (currencies), wave 4 (Temporal schedules), wave 5 (audit).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| SAP `TCURX` non-ISO decimals conflict with ISO 4217 | Medium | Medium | Migration report flags conflicts; ISO wins; amounts rescaled with sign-off |
| Residual allocation changes a user-visible line amount | Medium | Low | Adjustment recorded on the line and shown in UI; alternative rounding-difference account configurable |
| Provider licensing (OXR) | Low | Low | ECB and manual are license-free defaults |

Open questions: (1) Should reporting currency be per ledger or per legal entity? Default per ledger; decide by wave 12 mid-point. (2) Historical rate for non-monetary items in translation: store rate on line at posting (default) or look up by date? Default store; decide by wave 12 exit. (3) Support SAP-style "second local currency" (currency type 40) as a fourth amount? Default no (use a parallel ledger); revisit at wave 40.

### 2.12.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `ledger.accounting_currency` | ledger | char(3) | required | enabled ISO 4217 | Currency of `amount_acct` | Ledger form > Accounting currency | OB22 currency type 10 (company code currency) |
| `ledger.reporting_currency` | ledger | char(3) | = accounting | enabled ISO 4217 | Currency of `amount_rpt` | Ledger form > Reporting currency | OB22 currency type 30 (group currency) / FINSC_LEDGER |
| `ledger.reference_currency` | ledger | char(3) | accounting currency | ISO 4217 | Triangulation pivot (FR-12.3) | Currency exchange rate types > Euro triangulation | `TCURV-BWAER` reference currency |
| `ledger.default_rate_type_id` / `closing_rate_type_id` / `average_rate_type_id` | ledger | ref | `DEFAULT` / `CLOSING` / `AVERAGE` | rate types | Conversion at posting, revaluation, translation | Ledger form > Exchange rate type, budget exchange rate type | OB22 exchange rate type per currency type; OB59 valuation method rate type |
| `rate_type.allow_inverse` | rate type | bool | true | true / false | Reciprocal lookup when no explicit inverse (FR-12.2) | — (AX stores both directions) | OB08 inversion flag ("Inv." in `TCURR`) |
| `rate_type.provider_id` and schedule | rate type | ref + cron | ECB, `30 16 * * 1-5` CET | providers: ecb, openexchangerates, csv, manual | Import schedule (FR-12.4) | Currency exchange rate providers; Import currency exchange rates batch | Market data datafeed (TBDM) scheduling |
| `fx.max_rate_age_days` | tenant | int | 5 | 1–60 business days | Stale rate warning threshold (BR-12.7) | — | — |
| `ledger.reject_stale_rates` | ledger | bool | false | true / false | Stale rate becomes a 422 | — | — |
| `fx.deviation_warn_pct` | tenant | numeric | 10 | 1–100 | Confirmation threshold on rate entry (FR-12.11) | — | — |
| `fx.provider_timeout_s` / `fx.provider_retries` | tenant | int | 10 / 3 | 1–60 / 0–5 | Provider call behavior (FR-12.4) | Batch retry on `ExchangeRateProviderImport` | — |
| `ledger.rounding_mode` | ledger | enum | half_away_from_zero | half_away_from_zero / half_even | BR-12.2 | — (fixed) | — (commercial rounding fixed) |
| `currency_policy.unrealized_gain_account_id` / `unrealized_loss_account_id` / `realized_gain_account_id` / `realized_loss_account_id` | ledger × currency | ref | required when currency enabled | P&L accounts | Revaluation and clearing FX postings | Currency revaluation accounts (General ledger > Setup > Currency) | OBA1 KDB/KDF (`T030H`) |
| `currency_policy.rounding_difference_account_id` and `rounding_tolerance_units` | ledger × currency | ref + int | required; 1 | balance-sheet or P&L account; 0–5 units | BR-12.4 single-line residual | General ledger parameters > Ledger > Penny difference accounts (`LedgerParameters`) | OB00 rounding differences account |
| `ledger.revaluation_method` | ledger | enum | closing_rate | closing_rate / cumulative_no_reversal | FR-12.8 | Foreign currency revaluation > "Financial dimensions / Reversal" behavior | OB59 valuation method: "Reset" vs "Always valuate" |
| `account.revalue` and `account.revalue_level` | main account × ledger | bool + enum | false; balance | balance / open_item | Revaluation selection (BR-12.9) | Main account > Foreign currency revaluation checkbox | OBA1 valuation per account; OI-managed accounts valued at item level |
| `ledger_group.member_ledger_ids` | legal entity | uuid[] | `ALL` auto | ledgers of the entity | Parallel posting target (FR-12.7) | Posting layers (Current/Operations/Tax) | FAGL_LEDGER_GRP ledger groups (`T8G*`) |
| `ledger.accounting_principle` | ledger | enum | IFRS | IFRS / US_GAAP / LOCAL_GAAP / TAX / MANAGEMENT | Drives `LedgerAccountMapping` and subledger ledger-specific lines | — | Accounting principles (`TACC_PRINCIPLE`), assign to ledger group |
| `translation.rate_by_account_type` | ledger | jsonb | monetary: closing; non_monetary: historical; pnl: average; equity: historical | rate kinds | FR-12.9 | Reporting currency adjustment settings (D365) | FAGL_FC_TRANS translation method (OB59-like) |
| `translation.cta_account_id` | ledger | ref | required for translation | equity account | CTA posting (FR-12.9) | — | Translation differences account (OBA1 "Translation") |
| `fx.provider_priority` | tenant | jsonb | ecb 10, openexchangerates 20, csv 30, manual 40 | ordered | BR-12.10 precedence | — | — |
| `series.max_series_age_days` | series kind | int | rates 10, indices 45, prices 5 | 1–120 | BR-12.12 staleness warning | — | TBDM "last import" tolerance |
| `series.deviation_warn_pct` | series kind | numeric | rates 25, prices 10, indices 10 | 1–100 | FR-12.13 confirmation | — | — |

### 2.12.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Exchange rates by pair | list inquiry | rate type, from, to, date range, source | valid_from, rate, inverse, source, provider timestamp, superseded, deviation vs previous | `ExchangeRate` | Currency exchange rates form; SSRS Exchange rate (Currency exchange rates) | OB08 display, S_ALR_87012XXX via `TCURR` table view, Fiori Manage Exchange Rates |
| Rate lookup tester | drill-down | type, pair, date | resolution path (direct / inverse / triangulated), hops, rates used, result | fx resolver | — | — |
| Rate history / audit | drill-down | pair, type | version chain with `superseded_by`, created by, payload hash, provider | `ExchangeRate` | Database log on `ExchangeRate` | `TCURR` change documents (SCU3) |
| Provider import log | operational report | provider, rate type, date range | run, status, pairs imported, pairs unchanged, errors, duration | import run log | Batch job history for `ExchangeRateProviderImport` | TBDM import log |
| Ledger trial balance per ledger and currency type | list inquiry | ledger, period range, currency type (txn/acct/rpt), currency filter | account, opening, debit, credit, closing in selected currency type | `Balance` | Trial balance with posting layer filter | FAGLB03 per ledger (`RLDNR`), S_PL0_86000030 |
| Ledger comparison | operational report | two ledgers, period, account range | balance ledger A, ledger B, difference, mapped account | `Balance` × `LedgerAccountMapping` | — | GCAC ledger comparison |
| Multi-currency line inquiry | drill-down | ledger, currency_txn, date range, account | line, txn amount/currency, rate applied, rate type, override flag, acct, rpt, rounding adjustment | `JournalLine` | Voucher transactions (three currency columns) | FAGLL03 with document/local/group currency columns |
| Foreign currency revaluation proposal / results | drill-down | run id | account, dimensions, currency, balance txn, posted acct, rate used, revalued, adjustment, exclusion flag | `RevaluationLine` | Foreign currency revaluation form (simulation/results); SSRS Exchange adjustment | FAGL_FC_VAL log / posting list, S_ALR_87012XXX valuation list |
| Revaluation history | list inquiry | ledger, period range, rate type | run, mode, status, gain, loss, posting entry, reversal entry | `RevaluationRun` | Foreign currency revaluation history (`LedgerExchAdj`) | FAGL_FC_VAL run history (`FAGL_BSBW_HISTRY`) |
| Translation (CTA) report | operational report | ledger, period | account group, historical/closing/average rate, restated amount, CTA contribution | translation run | Reporting currency adjustment report (D365) | FAGL_FC_TRANS log |
| Stale rate usage | KPI | ledger, period | postings with `FX_RATE_STALE` warnings per day, pairs affected | posting warnings log | — | — |
| Market data series inquiry | list inquiry | series code, kind, date range | valid_from, value, unit, tenor, source, deviation | `MarketDataPoint` | — | TBDM market data display, `T056P` reference rates |
| Rate overrides audit | operational report | ledger, period, user | entry, line, rate type, override rate, market rate, deviation, user | `JournalLine.rate_override` | — | Document header exchange rate ≠ `TCURR` (FB03 rate field) |

### 2.12.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Exchange rate table printout | user action; scheduled monthly | header: rate type, date range, ledger reference currency; lines: pair, valid_from, rate, source; footer: generated at, provider payload hashes | PDF, XLSX, CSV | — | supporting record for audit of conversions; 10 years when attached to a close run |
| Foreign currency revaluation posting list | run posted | header: ledger, period, rate type, valuation date, method; lines: account, dimensions, currency, balances, rate, adjustment; footer: totals gain/loss, posting and reversal vouchers, result hash | PDF, XLSX | revaluation run id; vouchers from the ledger sequence | financial record; 10 years |
| Currency translation statement | translation run posted | header: ledger, period, rates applied per class; lines: account group, historical/closing/average amounts; footer: CTA movement, voucher | PDF, XLSX | run id | financial record; 10 years |
| Rate deviation confirmation record | rate saved above `fx.deviation_warn_pct` | pair, previous rate, new rate, deviation, user, timestamp, confirmation text | PDF (audit evidence), JSON | — | audit evidence; 10 years |

No external e-documents; rate files received from providers are stored as raw payloads (hash in `ExchangeRate.payload_hash`) for 13 months.

### 2.12.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `fx-import-ecb` | business days 16:30 CET | rate type | run id per (provider, rate type, date); points upserted by key, unchanged values skipped | < 30 s | 3 retries, exponential back-off; `fx.provider.degraded` after final failure; last-known rates stay valid | provider degraded → P2; no import in 2 business days → P2 |
| `fx-import-openexchangerates` (optional) | hourly | rate type | same | < 30 s | same | same |
| `fx-import-csv-inbox` | every 15 min | file | file hash as idempotency key | < 1 min | file quarantined with error report | parse errors → P3 |
| `fx-series-import` (interest/index/price series) | business days 18:00 | series | run id per (series, date) | < 1 min | same as rates | stale series beyond `max_series_age_days` → P3 |
| `ledger-fx-revaluation` (automation hook on close checklist, wave 14) | period end + 1 business day 06:00, per ledger | ledger × rate type | idempotent per (ledger, period, rate type); second run rejected while one is posted (BR-12.8) | 100k combinations < 5 min | run marked failed; close task blocked | failure → P2 |
| `ledger-fx-revaluation-reverse` | first day of next period 00:10 | run | reversal idempotent (`reversal_entry_id` set once) | < 1 min | retry hourly up to 24 h | reversal missing by day 1 12:00 → P2 |
| `ledger-translation` (close hook) | period end + 2 business days | ledger | idempotent per (ledger, period) | 200k balances < 3 min | failed → close task blocked | failure → P2 |
| `fx-rate-cache-refresh-audit` | every 5 min | pod | compares pod cache version with latest published rate | < 1 s | pod restarts cache | pod lag > 2 s → P3 |
| `fx-stale-rate-report` | daily 07:00 | ledger | daily snapshot | < 1 min | — | stale postings > 100/h → P3 |

### 2.12.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| View exchange rates | GET /fx/rates, /rates/history, /convert, /series; screens Exchange rates (read), Rate lookup tester | — | All finance roles |
| Maintain exchange rates | PUT /fx/rate-types, POST /fx/rates, POST /fx/series/*/points; screens Exchange rate types, Exchange rates, Market data series | Override exchange rates; Run foreign currency revaluation | Treasury accountant, Accounting manager |
| Administer exchange rates | POST /fx/providers/{p}:import; provider schedules and credentials (Vault); screen Provider schedules | Maintain exchange rates | Finance platform administrator |
| Override exchange rates | `rate_override` on posting lines; journal line rate cell | Maintain exchange rates; Approve ledger journals | Accounting supervisor (limited), Treasury accountant |
| Maintain ledger setup | PUT currency-policies, ledger-groups, ledgers, account mapping; screen Ledger setup | Post ledger journals; Run foreign currency revaluation | Accounting manager |
| Post ledger-specific adjustments | POST /ledger/journal-entries addressed to a single non-leading ledger | Approve ledger journals (same entry) | Financial controller, IFRS/local GAAP specialist |
| Run foreign currency revaluation | POST /ledger/revaluations, :post; screen Foreign currency revaluation | Maintain exchange rates; Maintain ledger setup | Accounting supervisor, Financial controller |
| Run currency translation | POST /ledger/translations | Maintain ledger setup | Financial controller, Group accountant |
| View ledger balances (per ledger) | GET balances with `ledger` filter; Ledger comparison | — | All finance roles |

### 2.12.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| FX-0001 `LEDGER_RATE_MISSING` | error | No {rate_type} rate from {from} to {to} valid on {date} (direct, inverse or triangulated). | FR-12.3 lookup exhausted | Enter the rate or import; check `allow_inverse` and reference currency | FR-12.3, FR-12.12 |
| FX-0002 `LEDGER_CURRENCY_MISMATCH` | error | Currency {currency} is not enabled or is suspended for {legal_entity}. | Currency not enabled | Enable the currency (wave 6) | FR-12.12 |
| FX-0003 `LEDGER_ROUNDING_EXCEEDED` | error | Rounding residual {residual} {currency} exceeds the tolerance {tolerance}. | BR-12.4 | Review amounts; a line split may be needed | FR-12.6 |
| FX-0004 `FX_RATE_STALE` | warning | Rate {rate_type} {from}/{to} is {age} days old (limit {max}). | BR-12.7 | Import or enter a current rate | FR-12.4 |
| FX-0005 `FX_RATE_DEVIATION` | warning | New rate {rate} deviates {pct}% from the previous {previous}. | FR-12.11 | Confirm or correct the rate | FR-12.11 |
| FX-0006 `FX_RATE_INVALID` | error | Rate must be greater than 0 with at most 10 decimals. | Validation | Correct the value | FR-12.2 |
| FX-0007 `FX_PROVIDER_DEGRADED` | warning (ops) | Provider {provider} failed after {retries} retries; last success {last_success}. | FR-12.4 | Check provider status; enter rates manually | FR-12.4 |
| FX-0008 `FX_RATE_OVERRIDE_FORBIDDEN` | error (403) | You do not hold the duty to override exchange rates. | FR-12.5 | Remove the override or request the duty | FR-12.5 |
| FX-0009 `LEDGER_GROUP_UNKNOWN` | error | Ledger group {code} does not exist for {legal_entity}. | FR-12.7 | Create the group or use a ledger id | FR-12.7 |
| FX-0010 `LEDGER_SPECIFIC_FORBIDDEN` | error (403) | Posting to a single non-leading ledger requires the duty "Post ledger-specific adjustments". | FR-12.7 | Address the ledger group or request the duty | FR-12.7 |
| FX-0011 `LEDGER_LEDGER_LINES_UNBALANCED` | error | Ledger-specific lines for {ledger} do not balance ({debits} / {credits}). | BR-12.6 | Balance each ledger's lines | FR-12.7 |
| FX-0012 `REVAL_RUN_EXISTS` | error | A posted revaluation already exists for {ledger} {period} {rate_type}. | BR-12.8 | Reverse it before re-running | FR-12.8 |
| FX-0013 `REVAL_RATE_MISSING` | error | No closing rate for {currency} on {valuation_date}; {count} balances cannot be revalued. | FR-12.8 | Enter the closing rate | FR-12.8 |
| FX-0014 `REVAL_PERIOD_CLOSED` | error | GL is closed for {period}; revaluation cannot post. | BR-14.1 | Reopen or choose another period | FR-12.8 |
| FX-0015 `TRANSLATION_CTA_ACCOUNT_MISSING` | error | No CTA account configured for ledger {ledger}. | FR-12.9 | Set `translation.cta_account_id` | FR-12.9 |
| FX-0016 `FX_SERIES_VALUE_MISSING` | error | Series {code} has no value on or before {date}. | BR-12.12 | Import or enter a point | FR-12.13 |
| FX-0017 `FX_SERIES_STALE` | warning | Series {code} value is {age} days old. | BR-12.12 | Import a current point | FR-12.13 |
| FX-0018 `FX_RATE_DATE_FUTURE` | error | Rate date {rate_date} is after the posting date. | BR-12.3 | Use a rate date ≤ posting date | FR-12.5 |

### 2.12.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Rate lookup (cached) | every posted line in foreign currency: 60k/day; peak 5,000/s | 0.05 / 1 / 5 ms | 20,000/s per pod | — | in-memory map (type, from, to) → sorted valid_from | cache miss → one indexed query; posting p99 budget unchanged |
| Rate publish (manual or import) | 2,000 points/day | 20 / 80 / 150 ms | 500/s bulk | 200 B × 500k rows ≈ 100 MB | index §2.12.4 | import queued |
| `POST /fx/convert` (stateless) | 100k/day | 5 / 20 / 40 ms | 2,000/s | — | — | — |
| Parallel posting to 3 ledgers | 30% of entries | +15 / +40 / +60 ms over single ledger | 5,000 lines/s aggregate | 3× journal rows for parallel entries | shared `document_id` index | HPA; entries stay atomic |
| Revaluation run | monthly per ledger; 100k combinations × 4 currencies | — | 100k combinations < 5 min | revaluation lines 120 B × 1.2M/year ≈ 150 MB | `journal_line` (ledger, currency_txn, posting_date); `revaluation_line` PK | run time scales linearly; proposal retained 90 days |
| Translation run | monthly; 200k balances | — | < 3 min | — | balance PK | — |
| Rate history query | 5k/day | 20 / 100 / 200 ms | — | — | (type, from, to, valid_from DESC) | — |
| Market data series storage | 50 series × 260 points/year | lookup 1 ms | — | < 5 MB | (series_id, valid_from DESC) | — |

### 2.12.20 Localization and compliance hooks

- Statutory rate sources: country packs (wave 48) register the mandatory provider per legal entity — ECB reference rates (EU), Bundesbank/ÖNB monthly VAT rates (DE/AT, `Umsatzsteuer-Umrechnungskurse`), HMRC monthly rates (UK), Banco de México FIX (MX), RBI reference rates (IN), Bank of Canada (CA) — as additional `fx` provider adapters with their own rate types (e.g., `TAX_MONTHLY`); the tax engine (wave 13) selects the statutory rate type per country for VAT reporting.
- Rate direction and quotation conventions (direct vs indirect quotation, per-100 units as in AX and `TCURF` factors) are normalized at import; display conventions per locale (decimal separator, 6 decimals) come from wave 5 formatting.
- Rounding: ISO 4217 minor units are not overridable; Swiss 0.05 cash rounding and similar are invoice-level concerns handled by waves 29/30 pricing, not by the ledger.
- Parallel ledgers for local GAAP: country packs ship ledger templates (`HGB`, `PCG`, `Ind AS`, `JGAAP`) with accounting principle, calendar (e.g., Japan April–March), and `LedgerAccountMapping` starter sets.
- Hyperinflation (IAS 29) restatement and functional-currency changes are out of scope for Phase 2; the `MarketDataSeries` of kind `index` (CPI) is the hook wave 40 uses.
- Privacy: none (no personal data in rates); provider credentials are secrets in Vault.
- Extension point: `fx.provider` adapter interface (`Fetch(rateType, date) → []RatePoint`), registered by country packs; `ledger.account_mapping_pack` for principle-specific account remaps.

### 2.12.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| SAP §13.6 FSCM Master data & documents — TBDM market data import (rates, prices) (SAP register row 74) | PARTIAL-12 | ADDED as FR-12.13 (series storage and import); valuation DEFERRED to wave 36 | exchange-rate import was already FR-12.4; interest, index and price series now stored with the same provider and audit model |
| SAP §13.6 FSCM Periodic processing — TBDM import run (SAP register row 82) | PARTIAL-12 | ADDED as FR-12.13 | job `fx-series-import` (§2.12.16) |
| SAP §13.6 FSCM Customizing — Market data: rate types, yield curves, volatility (`VTB*`, `TCURR`) (SAP register row 85) | PARTIAL-12 | ADDED as FR-12.13 for rate types and series metadata; yield curves and volatility DEFERRED to wave 36 | curve bootstrapping, interpolation and volatility surfaces belong to treasury (wave 36), which consumes `fx.series.published` |

## 2.13 Wave 13: Tax Engine (Sales/Use, VAT/GST, Withholding)

### 2.13.1 Scope and business context

AX determines tax by intersecting a sales tax group (party side, `TaxGroupHeading`/`TaxGroupData`) with an item sales tax group (`TaxItemGroupHeading`/`TaxItemGroupData`) to find tax codes (`TaxTable`) with rates (`TaxData`), posting results to `TaxTrans`. SAP uses tax procedures with condition types, tax codes per country (`T007A`, FTXP), jurisdiction codes for US, and withholding tax types/codes. Wave 13 builds the `tax` service (Go) that any document line calls for determination, calculation and posting lines, with an internal rules engine as default and an Avalara/Vertex-compatible provider adapter interface, plus settlement to the GL and statutory extracts (VAT return, EU sales list, Intrastat, SAF-T).

After this wave a manual journal or any later document gets correct tax lines for US sales/use tax, EU VAT (standard, reduced, reverse charge, intra-community), Indian GST (CGST/SGST/IGST) and Canadian GST/HST/PST, and the tax settlement run posts the net payable and reconciles to the tax accounts. Out of scope: document-level invoicing (waves 18, 29), 1099/26AS statutory filing formats (hooks only; forms in wave 48 country packs), customs duty.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| SAP FTXP: a tax code is a single rate per procedure step with no validity dates; a rate change (e.g., DE 19% → 16% in 2020) forces new codes and mass master-data updates in `KNVI`/`MLAN` | `T007A` has no effective dating | `TaxRate` rows are effective-dated under a stable `TaxCode`; groups and parties keep their assignments | Rate change rollout 2–5 days of config → 1 rate row; zero master-data touches |
| AX `TaxTrans` and SAP `BSET` show the result but not why: users cannot see which group intersection, exemption or jurisdiction produced the amount; support tickets replay the document in a test client | Determination logic in X++ `Tax` class / condition technique with no trace output | `POST /tax/calculate:explain` returns `determination_trace[]` step by step; tester screen renders it | "Why this tax?" investigation 30–60 min → < 1 min |
| US multi-jurisdiction via SAP external tax interface or AX ISV connectors: provider outage blocks invoicing (RFC timeout, document cannot save) | Synchronous dependency with no fallback | Provider adapter with 2 s timeout, internal-engine fallback, `tax_fallback` flag and recommit queue | Invoicing downtime during provider outage → 0; fallback decision < 2.5 s |
| Sales tax settlement (AX "Settle and post sales tax", SAP F.12 / S_ALR_87012357) posts a total that does not match the GL tax accounts; controllers reconcile by hand in Excel | Tax transactions and GL postings written by different code paths with separate rounding | `TaxTransaction` linked to `JournalLine` by id; settlement blocks on mismatch (`TAX_SETTLEMENT_MISMATCH`) | Settlement-to-GL reconciliation 4 h/period → automated, 0.00 variance enforced |
| Price-inclusive rounding differs across SAP condition steps and AX "per line / per document" settings, producing 1-cent mismatches with customer invoices and credit notes | Multiple rounding points without a distribution rule | One algorithm (BR-13.3) with largest-remainder distribution; credit notes use the original bracket | 1-cent invoice disputes → 0 in the 500-scenario matrix |

### 2.13.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Tax codes and rates | `TaxTable`, `TaxData` (effective-dated values, limits) | `T007A` (tax codes), FTXP, condition records `KONP`/`A003` | `tax.TaxCode`, `tax.TaxRate` (effective-dated, bracketed) | full |
| Party-side tax group | `TaxGroupHeading`, `TaxGroupData`, Sales tax groups form | Customer/vendor tax classification (`KNVI`/`LFA1-STKZU`), tax category on BP | `tax.TaxGroup` (N:M TaxCode) | full |
| Item-side tax group | `TaxItemGroupHeading`, `TaxItemGroupData` | Material tax classification (`MLAN`) | `tax.ItemTaxGroup` (N:M TaxCode) | full |
| Determination | Intersection of group and item group | Tax procedure (TAXUSJ/TAXEUR), access sequences, condition technique | Intersection + jurisdiction resolution + rule overrides (§2.13.7 BR-13.1) | improved: explainable trace |
| Jurisdictions | `TaxJurisdiction` (US), sales tax authorities `TaxAuthorityAddress` | Jurisdiction codes (`TTXJ`), external tax interface (Vertex/Sabrix) | `tax.TaxJurisdiction` hierarchy (country > state > county > city > district) | full |
| Exemptions | Tax exempt numbers, `TaxExempt` codes | Exemption licenses (withholding), tax code 0% | `tax.ExemptionCertificate` (party × jurisdiction × validity × reason) | full |
| Calculation modes | Price includes tax, calculation method (whole amount/interval), tax on tax | Condition calculation types, `KSCHL` steps, from/to step references | Inclusive/exclusive, compound sequence, bracketed rates | full |
| Reverse charge / use tax | Reverse charge item groups, Use tax checkbox on tax code | Acquisition tax codes (EU), use tax (US) via tax procedure | `TaxCode.mechanism` ∈ {standard, reverse_charge, use_tax, withholding} | full |
| Partial recoverability | Non-deductible percentage on tax code | Non-deductible tax (NAV/NVV condition types) | `TaxCode.recoverable_pct` with expense distribution | full |
| Withholding tax | Withholding tax codes/groups (`TaxWithholdTable`), 1099 fields | Extended withholding tax types/codes (`T059P`, `T059Z`), `WITH_ITEM` | `tax.WithholdingTaxCode`, `tax.WithholdingTransaction`; trigger at invoice or payment | full |
| Posted tax | `TaxTrans` | `BSET`, ACDOCA tax fields | `tax.TaxTransaction` linked to JournalLine | full |
| Settlement and reporting | Settlement periods, "Settle and post sales tax", EU sales list, Intrastat | Tax payable posting (F.12/S_ALR_87012357), ACR (Advanced Compliance Reporting), Intrastat (VE01) | `tax.TaxSettlementRun`, VAT return, ESL, Intrastat, SAF-T extracts | full (formats in wave 48) |
| External provider | Avalara/Vertex ISV connectors | External tax interface (RFC_CALCULATE_TAXES_DOC) | Provider adapter interface (`TaxProvider`) with failover | full |
| Intrastat master data | Foreign trade parameters, Commodity codes (`IntrastatCommodity`), Transaction codes (`IntrastatTransactionCode`), Transport methods, Ports, Statistics procedures, Country/region properties (EU), Intrastat compression / check setup | Foreign trade customizing (VEB/VE0: `T604`, `T605`, `T606`, `T616` modes of transport, `T618` customs offices), commodity codes (`T604`), Intrastat settings per country | `tax.CommodityCode`, `tax.IntrastatTransactionCode`, `tax.TransportMode`, `tax.Port`, `tax.StatisticsProcedure`, `tax.CountryTradeProperty` (FR-13.15) | full |
| Intrastat journal and declaration | Intrastat journal (`IntrastatArchiveGeneral`, `Intrastat` form): transfer from invoices, validate, compress, export file | VE01 / MEIS / VI99 declaration runs (`VEIAV`), Intrastat checks, file export per country | `tax.IntrastatLine` with transfer, validation, compression, correction and export (FR-13.16); country formats via wave 48 | full |
| Commodity code on products | Released product > Foreign trade > Commodity code | Material master Foreign trade export view (`MARC-STAWN`) | `product.commodity_code_id` (wave 8 field, FK to `tax.CommodityCode`), default per category | full |
| VAT recovery on expenses | Expense management > Setup > Tax configuration (recoverable VAT per country), Recover VAT (ER-based claim), SSRS VAT recovery report | Non-deductible tax (NAV/NVV), ESC claim via DRC / country-specific reports; no native EU refund directive flow | `tax.VatRecoveryPolicy` per (country, expense category), foreign VAT reclaim extract (FR-13.17) | full |
| Withholding remittance and certificates | Withholding tax payment (settle), 1099 summary; India TDS challan via GL/Indian localization | J1INCHLN (TDS challan), J1INBANK (bank challan update), J1INCERT (Form 16A), generic WHT reporting RFIDYYWT | `tax.WithholdingRemittanceRun`, certificate numbering and hooks (FR-13.18); country forms in wave 48 | partial: forms in wave 48 |

### 2.13.3 Functional requirements

- **FR-13.1 Tax codes and effective-dated rates** — Must. The system shall define tax codes per legal entity with mechanism (standard, reverse_charge, use_tax, withholding), rate rows effective-dated with optional amount brackets (`from_amount`, `to_amount`), recoverable percentage, jurisdiction, and posting accounts (payable, receivable, non-recoverable expense, use-tax expense).
  - Rate precision numeric(9,6) in percent (e.g., 19.000000); brackets evaluated on the line base or document base per code setting (AX "calculation method": whole amount vs interval).
  - Maximum 2,000 codes per legal entity; rate history retained indefinitely.
- **FR-13.2 Tax groups and item tax groups** — Must. The system shall maintain party-side tax groups and item-side item tax groups, each a set of tax codes; defaults come from party roles (customer/vendor), delivery address jurisdiction, and product/category.
- **FR-13.3 Jurisdiction hierarchy** — Must. The system shall model jurisdictions as a tree (country > state/province > county > city > special district) with address-to-jurisdiction resolution by postal code tables or provider lookup, and compute stacked rates (e.g., CA state 7.25% + county + district).
- **FR-13.4 Determination and calculation API** — Must. The system shall expose `POST /api/v1/tax/calculate` taking a document (header: legal entity, document type, dates, party, addresses, currency; lines: amount, quantity, product, tax group, item tax group, overrides) and returning per-line and per-document tax lines with an explain trace.
  - Stateless, p99 < 50 ms for 100-line documents with the internal engine; provider calls add their latency (timeout 2 s).
  - Order of operations in BR-13.1. Results carry `engine: internal | avalara | vertex`, `fallback: bool`, and `determination_trace[]`.
- **FR-13.5 Inclusive and exclusive prices** — Must. The system shall support price-inclusive lines by backing out tax: `base = gross / (1 + Σ non-compound rates)`, iteratively resolving compound codes; rounding per BR-13.3.
- **FR-13.6 Compound tax, reverse charge, use tax, partial recoverability** — Must. The system shall calculate tax-on-tax by code sequence, generate equal input and output lines for reverse charge, accrue use tax as a liability with the expense to the line's account, and split recoverable vs non-recoverable amounts.
- **FR-13.7 Exemption certificates** — Must. The system shall apply certificates by (party, jurisdiction, validity, reason code, optional product category), zeroing the applicable codes and recording the certificate id on the tax transaction; expired certificates produce `TAX_EXEMPTION_EXPIRED` warnings and full tax.
- **FR-13.8 Withholding tax** — Must. The system shall calculate withholding (vendor and customer) with code, base (gross, net, tax-inclusive), rate, threshold and annual accumulation, triggered at invoice or payment per code; results are stored as `WithholdingTransaction` and exposed to AP/AR (waves 19/30) and to 1099/26AS reporting hooks.
- **FR-13.9 Tax posting** — Must. The system shall convert a calculated document into journal lines (posting API, wave 11) with the tax account per code, dimension inheritance from the taxed line, and create `TaxTransaction` rows linked to the posted `JournalLine` ids, within the caller's document transaction via a prepared-lines contract (`POST /tax/calculate` → caller includes returned lines in its posting request → tax consumes `journal.posted` to link).
- **FR-13.10 Provider adapters and failover** — Must. The system shall implement a `TaxProvider` interface (`Calculate`, `Commit`, `Void`, `ValidateAddress`) with Avalara AvaTax REST v2 and Vertex O-Series adapters, selectable per legal entity × document type; on timeout or 5xx the internal engine is used and the document is flagged `tax_fallback = true` for later recommit.
  - Provider credentials in Vault; requests and responses hashed and stored 7 years for audit (payload itself retained 13 months).
- **FR-13.11 Settlement periods and settlement run** — Must. The system shall define settlement periods per tax authority (monthly, quarterly, annual) and run a settlement that sums tax transactions by code and period, posts the net payable/receivable to the authority's vendor/customer (party role) or a settlement account, and marks transactions settled.
  - Re-runs for the same period post only the delta (late-posted transactions) as a new settlement with `version` incremented.
  - Settlement totals must equal the GL tax account movements for the period; a difference > 0.00 blocks posting with `TAX_SETTLEMENT_MISMATCH`.
- **FR-13.12 Statutory extracts** — Should. The system shall produce VAT return boxes (configurable box mapping per country), EU sales list (ESL, by customer VAT id and transaction type), Intrastat lines (commodity code, weight, value, partner country) and SAF-T (OECD v2.0 XSD-valid XML) for a period.
- **FR-13.13 Tax configuration workspace** — Must. Screens for codes, rates, groups, jurisdictions, certificates, withholding, settlement periods, provider settings and a calculation tester.
- **FR-13.14 Tax matrix test harness** — Must. The system shall ship a fixture runner that executes the 500-scenario matrix against the engine in CI and reports diffs per scenario.
- **FR-13.15 Intrastat master data** — Must. The system shall maintain, per tenant with legal-entity overrides: commodity codes (Combined Nomenclature CN8, with validity year, supplementary unit, description, hierarchy by HS6), nature-of-transaction codes (two-digit EU list plus national second digit), transport modes (1–9 per EU regulation 2019/2152), ports and airports (UN/LOCODE), statistics procedures, delivery terms (Incoterms 2020) mapping, country/region trade properties (EU member, Intrastat threshold arrival/dispatch, reporting currency, arrival/dispatch statistical value rules), and the legal entity's Intrastat registration (VAT id, third-party declarant, agent).
  - CN8 code lists are imported yearly from the EU RAMON/Eurostat CSV through a CSV importer (≤ 20,000 rows); expired codes stay for historic lines.
  - Commodity code defaults on products via category (wave 8 exposes `commodity_code_id` and `country_of_origin`); a line without a commodity code at Intrastat transfer time is reported as an exception, not dropped.
- **FR-13.16 Intrastat journal, validation, compression and export** — Must. The system shall build `IntrastatLine` rows from posted documents (wave 13: manual journals flagged with trade data; waves 18/27/28/29 register vendor invoices, sales invoices, shipments and receipts as sources through the `IntrastatSource` contract) with direction (arrival/dispatch), commodity code, partner country and partner VAT id (dispatches, EU 2022 requirement), country of origin, nature of transaction, transport mode, delivery terms, net mass, supplementary units, invoice value, statistical value, and period.
  - Validation rules per country (mandatory fields by direction, mass > 0 unless supplementary unit, partner VAT id format via wave 7) produce `TAX_INTRASTAT_INVALID` exceptions on a worklist; lines can be corrected in the journal with an audit trail (field, old, new, user).
  - Compression merges lines with identical (direction, commodity, partner country, origin, transaction nature, transport mode, delivery terms, partner VAT id) per period into one declaration line (summed mass, units, values) per the country's compression setup.
  - Export produces the declaration dataset for the period (JSON canonical) and country formats registered by wave 48 (DE XML (Destatis), FR DEB/EMEBI, NL CBS IDEP, IT Elenchi; wave 13 ships the canonical format and a generic CSV); thresholds per country decide whether a declaration is required and are reported on the period summary.
  - Corrections after export create delta lines flagged `correction_of_line_id` and a correction declaration for the original period.
- **FR-13.17 VAT recovery on expenses** — Should. The system shall maintain `VatRecoveryPolicy` rows per (expense category, country of expense, legal entity country) with `recoverable_pct` (domestic input VAT per category, e.g., DE entertainment 70%, FR passenger cars 0%, BE 50%), `foreign_reclaim_eligible` (EU Directive 2008/9/EC or 13th Directive) and the minimum claim thresholds; the calculation API applies the policy when a document line carries an `expense_category` (expense reports in wave 43, vendor invoices in wave 19) by setting the code's recoverable percentage per line.
  - A foreign VAT reclaim extract per (legal entity, refund country, calendar quarter or year) lists eligible transactions with invoice reference, vendor VAT id, amounts, and expense codes per Annex of the directive (codes 1–10), and a VAT recovery report shows recoverable, non-recoverable and reclaimable amounts per period and country.
  - Non-recoverable amounts post to the line's expense account (FR-13.6) and reclaimable foreign VAT posts to a `foreign_vat_receivable_account_id` per policy when `book_foreign_vat_as_receivable = true`, otherwise to expense.
- **FR-13.18 Withholding remittance and certificate hooks** — Should. The system shall group posted `WithholdingTransaction` rows by (legal entity, authority, withholding code section, period) into a `WithholdingRemittanceRun` that posts the liability transfer to the authority party and records the remittance reference (bank challan number, payment date, bank branch code) and, per payee and period, assigns certificate numbers from a number sequence and exposes the data set (payee, tax id, section, base, rate, tax, remittance references) that country packs render as Form 16A / 26Q (IN), 1099-NEC/MISC and 1042-S (US), CIS statements (UK) and `Lohnsteuerbescheinigung`-style certificates (wave 48).
  - A remittance run may be `proposed → posted → cancelled` and its transactions are flagged `remitted`; quarterly returns reference remittance runs; a certificate is immutable once issued and can be reissued with a new number and `supersedes_certificate_id`.

### 2.13.4 Data model

**TaxCode** and **TaxRate**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| legal_entity_id | uuid | yes | |
| code | text(20) | yes | unique per legal entity |
| name | text(120) | yes | |
| mechanism | enum | yes | standard, reverse_charge, use_tax, withholding |
| jurisdiction_id | uuid | no | FK TaxJurisdiction |
| authority_party_id | uuid | yes | FK Party (role tax authority) |
| settlement_period_id | uuid | yes | FK TaxSettlementPeriod |
| calc_basis | enum | yes | line, document (bracket evaluation) |
| compound_sequence | int | no | null = not compound; higher sequences apply on base + earlier tax |
| recoverable_pct | numeric(9,6) | yes | default 100 |
| payable_account_id / receivable_account_id / nonrecoverable_account_id / use_tax_expense_account_id | uuid | conditional | FK MainAccount |
| rounding | enum | yes | per_line, per_document (default per_document for VAT, per_line for US) |
| rate: tax_code_id, valid_from, valid_to | | yes | non-overlapping intervals |
| rate: from_amount / to_amount | numeric(23,6) | no | bracket |
| rate: rate_pct | numeric(9,6) | yes | |
| rate: min_tax / max_tax | numeric(23,6) | no | caps |

**TaxTransaction**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| legal_entity_id / ledger_id | uuid | yes | leading ledger |
| tax_code_id | uuid | yes | |
| jurisdiction_id | uuid | no | |
| source (system, type, id, line_ref) | text | yes | document line |
| journal_line_id | uuid | no | set when `journal.posted` consumed |
| tax_direction | enum | yes | output (sales), input (purchase), use, withholding |
| base_amount_txn / tax_amount_txn / currency_txn | numeric / char(3) | yes | |
| base_amount_acct / tax_amount_acct | numeric(23,6) | yes | |
| recoverable_amount_acct / nonrecoverable_amount_acct | numeric(23,6) | yes | sum = tax_amount_acct |
| rate_pct_applied | numeric(9,6) | yes | |
| exemption_certificate_id | uuid | no | |
| counterparty_party_id / counterparty_tax_id | uuid / text | no | for ESL |
| engine / fallback / provider_ref | enum / bool / text | yes | |
| settlement_run_id | uuid | no | null = unsettled |
| posting_date / fiscal_period_id | date / uuid | yes | |

**TaxSettlementRun**: `id`, `legal_entity_id`, `authority_party_id`, `settlement_period_id`, `period_from`, `period_to`, `version`, `status` (proposed, posted, cancelled), `output_tax_total`, `input_tax_total`, `net_amount`, `entry_id`, `transaction_count`, `created_by`, `posted_at`.

**ExemptionCertificate**: `id`, `party_id`, `jurisdiction_id`, `certificate_number`, `reason_code`, `valid_from`, `valid_to`, `product_category_id` (optional), `document_uri`, `status` (active, expired, revoked).

**IntrastatLine** (FR-13.16) and Intrastat master data (FR-13.15)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| legal_entity_id / reporting_period | uuid / text(7) | yes | `YYYY-MM` |
| direction | enum | yes | arrival, dispatch |
| source (system, type, id, line_ref) | text | yes | originating document line |
| commodity_code_id | uuid | conditional | FK CommodityCode (CN8, `validity_year`); missing → exception |
| partner_country / origin_country | char(2) | yes / conditional | ISO 3166-1; origin mandatory for dispatches since 2022 |
| partner_vat_id | text(20) | conditional | dispatches; format-checked (wave 7) |
| transaction_nature_code | text(2) | yes | FK IntrastatTransactionCode |
| transport_mode | text(1) | yes | FK TransportMode (1–9) |
| delivery_terms | text(3) | conditional | Incoterms 2020 |
| port_id | uuid | no | FK Port (UN/LOCODE) |
| statistics_procedure_id | uuid | no | FK StatisticsProcedure |
| net_mass_kg / supplementary_units | numeric(18,3) / numeric(18,3) | conditional | mass required unless CN8 has supplementary unit only |
| invoice_value / statistical_value / currency | numeric(23,6) / numeric(23,6) / char(3) | yes | statistical value per country rule (FR-13.15) |
| status | enum | yes | transferred, validated, exception, compressed, exported, corrected |
| compressed_into_id / correction_of_line_id / export_run_id | uuid | no | |
| corrections | jsonb | no | [{field, old, new, user, at}] |

`CommodityCode`: `id`, `code` text(8), `validity_year`, `description`, `supplementary_unit`, `parent_hs6`; `IntrastatTransactionCode`: `code` text(2), `description`, `country` (null = EU list); `TransportMode`: `code` text(1), `description`; `Port`: `locode` text(5), `name`, `country`; `StatisticsProcedure`: `code`, `description`, `country`; `CountryTradeProperty`: `country`, `is_eu_member`, `threshold_arrival`, `threshold_dispatch`, `reporting_currency`, `statistical_value_rule` (invoice_value | invoice_plus_freight_to_border | custom), `compression_fields` jsonb, `export_format_id` (wave 48). `IntrastatExportRun`: `id`, `legal_entity_id`, `period`, `direction`, `line_count`, `total_value`, `format`, `output_uri`, `hash`, `status`, `declared_at`.

**VatRecoveryPolicy** (FR-13.17): `id`, `legal_entity_id`, `expense_category_id` (wave 43/19 category list; `*` allowed), `expense_country` char(2), `recoverable_pct` numeric(9,6), `foreign_reclaim_eligible` bool, `directive` enum (2008_9, 13th, none), `expense_code_annex` text(2), `book_foreign_vat_as_receivable` bool, `foreign_vat_receivable_account_id`, `min_claim_amount` numeric(23,6), `valid_from`, `valid_to`. `VatReclaimExtract`: `id`, `legal_entity_id`, `refund_country`, `period_from`, `period_to`, `transaction_ids[]`, `total_reclaimable`, `output_uri`, `status`.

**WithholdingRemittanceRun** (FR-13.18): `id`, `legal_entity_id`, `authority_party_id`, `section_code`, `period_from`, `period_to`, `status` (proposed, posted, cancelled), `transaction_ids[]`, `total_withheld`, `entry_id`, `remittance_reference` jsonb (`challan_no`, `bank_code`, `paid_on`, `bsr_code`), `posted_at`. `WithholdingCertificate`: `id`, `payee_party_id`, `period`, `certificate_number` (sequence per legal entity × section), `transaction_ids[]`, `issued_at`, `supersedes_certificate_id`, `output_uri`.

**Invariants**: Σ TaxTransaction.tax_amount_acct per (code, period) = movement on the code's payable/receivable account for that period (checked by settlement); recoverable + nonrecoverable = tax; rate intervals per code do not overlap; a settled transaction cannot change `settlement_run_id` except to null via a cancelled run. Indexing: `tax_transaction` on (legal_entity_id, tax_code_id, fiscal_period_id, settlement_run_id), (source_system, source_type, source_id), (journal_line_id); partitioned by fiscal year.

### 2.13.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/tax/calculate | Determine and calculate for a document | stateless (optional `Idempotency-Key` for provider commit) | Calculate tax (service accounts) |
| POST | /api/v1/tax/calculate:explain | Same with full determination trace | stateless | View tax configuration |
| POST | /api/v1/tax/commit | Commit provider transaction after document posting | `Idempotency-Key` required | Calculate tax |
| GET/PUT | /api/v1/tax/legal-entities/{le}/codes[/{code}] and /codes/{code}/rates | Codes and rates | PUT idempotent | Maintain tax codes |
| GET/PUT | /api/v1/tax/legal-entities/{le}/groups[/{id}] and /item-groups[/{id}] | Groups | PUT idempotent | Maintain tax groups |
| GET/PUT | /api/v1/tax/jurisdictions[/{id}] and POST /jurisdictions:resolve | Jurisdiction tree and address resolution | PUT idempotent | Maintain tax jurisdictions |
| GET/POST/PATCH | /api/v1/tax/exemption-certificates[/{id}] | Certificates | `Idempotency-Key` on POST | Maintain tax exemptions |
| GET/PUT | /api/v1/tax/legal-entities/{le}/withholding-codes[/{code}] | Withholding | PUT idempotent | Maintain withholding tax |
| GET | /api/v1/tax/transactions?le=&code=&period=&settled= | Posted tax transactions | n/a | View tax transactions |
| POST | /api/v1/tax/settlements | Propose or post a settlement run | `Idempotency-Key` | Settle tax |
| POST | /api/v1/tax/settlements/{id}:post | Post a proposal | `Idempotency-Key` | Settle tax |
| POST | /api/v1/tax/extracts/{vat-return|esl|intrastat|saf-t} | Generate extract for a period | run id | Produce tax reports |
| GET/PUT | /api/v1/tax/intrastat/commodity-codes[/{code}], /transaction-codes, /transport-modes, /ports, /statistics-procedures, /country-properties[/{country}] and POST /commodity-codes:import | Intrastat master data (FR-13.15) | PUT idempotent; import `Idempotency-Key` | Maintain foreign trade setup |
| POST | /api/v1/tax/intrastat/lines:transfer | Transfer lines from registered sources for a period | `Idempotency-Key` (period) | Maintain Intrastat journal |
| GET/PATCH | /api/v1/tax/intrastat/lines[/{id}]?le=&period=&direction=&status= | Intrastat journal and corrections | PATCH audited | Maintain Intrastat journal |
| POST | /api/v1/tax/intrastat/lines:validate, :compress and /exports | Validate, compress, export declaration | run id | Produce tax reports |
| GET/PUT | /api/v1/tax/legal-entities/{le}/vat-recovery-policies[/{id}] | VAT recovery policies (FR-13.17) | PUT idempotent | Maintain tax codes |
| POST | /api/v1/tax/vat-reclaims | Foreign VAT reclaim extract for (refund country, period) | run id | Produce tax reports |
| POST | /api/v1/tax/withholding/remittances and /{id}:post, :cancel | Withholding remittance runs (FR-13.18) | `Idempotency-Key` | Settle tax |
| POST | /api/v1/tax/withholding/certificates:issue | Issue certificates per payee and period | `Idempotency-Key` | Produce tax reports |

Calculation request/response (abridged): request `{legal_entity, document_type, document_date, tax_date, currency, party:{id, role, tax_id}, ship_from, ship_to, lines:[{line_ref, amount, quantity, product_id, tax_group, item_tax_group, price_includes_tax, account, dimensions, override:{tax_code?, exempt_reason?}}]}`; response `{engine, fallback, lines:[{line_ref, base, taxes:[{code, jurisdiction, rate_pct, tax_amount, recoverable, nonrecoverable, direction}]}], document_taxes_by_code:[…], posting_lines:[… wave 11 line objects …], trace_id, determination_trace[]}`.

Validation: every line has resolvable groups or an override; dates within code validity; currency enabled; party tax id format valid for reverse charge; totals of `posting_lines` balance against the caller's lines.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.tax.calculated.v1 | calculate with commit intent | document ref, engine, fallback, totals per code, trace_id | document services, audit |
| dev.passage.tax.transaction.recorded.v1 | TaxTransaction linked to journal line | transaction id, code, period, base, tax, direction, journal_line_id | reporting, lakehouse |
| dev.passage.tax.settled.v1 | settlement posted | run id, authority, period, output, input, net, entry_id, version | reporting, ledger |
| dev.passage.tax.provider.fallback.v1 | provider failure | provider, document ref, error, retry_after | ops, AP/AR recommit job |
| dev.passage.tax.rate.changed.v1 | rate row created | code, valid_from, rate_pct, previous | document services cache |
| dev.passage.tax.intrastat.exported.v1 | declaration export completed | export_run_id, legal_entity, period, direction, line_count, total_value, format, hash | audit, wave 48 submission connectors |
| dev.passage.tax.withholding.remitted.v1 | remittance run posted | run_id, authority, section, period, total_withheld, entry_id, remittance_reference | AP (wave 19), reporting, wave 48 returns |

`IntrastatSource` contract (used by waves 18/27/28/29): a document service registers `{source_type, resolver_uri}`; at transfer the tax service calls the resolver for posted documents in the period and receives lines with trade fields; documents reversed after transfer produce negative lines on the next transfer.

### 2.13.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Tax codes | Create, add rate rows, set accounts, mechanism | code, mechanism, jurisdiction, current rate, authority, settlement period | Maintain tax codes |
| Tax groups / item tax groups | Assign codes, set defaults for party/product | group, codes, exempt flags | Maintain tax groups |
| Jurisdictions | Tree view, import postal tables, resolve address tester | level, code, parent, rates stacked | Maintain tax jurisdictions |
| Exemption certificates | Register, upload document, expire | party, jurisdiction, number, validity, reason | Maintain tax exemptions |
| Withholding codes | Rates, thresholds, trigger point, reporting category | code, rate, base, threshold, 1099 box | Maintain withholding tax |
| Calculation tester | Enter a sample document, see tax lines and trace | line, code, rate, base, tax, explanation | View tax configuration |
| Tax transactions | Filter by code/period/document, drill to journal line and source | document, code, base, tax, direction, settled | View tax transactions |
| Settlement | Propose, review by code, post, view history | authority, period, output, input, net, status, version | Settle tax |
| Provider settings | Select provider per entity/document type, test connection | provider, environment, company code, last success | Administer tax |
| Foreign trade setup | Maintain commodity codes (import yearly CN8), transaction codes, transport modes, ports, statistics procedures, country properties and thresholds | code, description, validity year, supplementary unit; country, threshold arrival/dispatch, compression fields | Maintain foreign trade setup |
| Intrastat journal | Transfer period, work exceptions, correct fields, validate, compress, export, view declaration summary vs threshold | direction, commodity, partner country, VAT id, nature, transport, mass, units, invoice/statistical value, status | Maintain Intrastat journal / Produce tax reports |
| VAT recovery policies | Define recoverable % per category and country, reclaim eligibility, accounts; run reclaim extract; view recovery report | category, country, recoverable %, directive, annex code, receivable account | Maintain tax codes / Produce tax reports |
| Withholding remittance | Propose run per authority/section/period, enter remittance reference, post, issue certificates | payee, section, base, rate, withheld, challan no., certificate no. | Settle tax / Produce tax reports |

Interaction rules: the tester renders the determination trace as a step list (groups → intersection → jurisdiction → exemptions → brackets → compounding → rounding); rate grids validate non-overlapping intervals on save; settlement proposals show a reconciliation panel (transactions vs GL) and block the post button on mismatch.

### 2.13.7 Business rules and edge cases

- **BR-13.1 Order of operations.** (1) Resolve `tax_date` (document date unless the document type specifies delivery date). (2) Resolve party tax group (override > party address jurisdiction default > party role default) and item tax group (override > product > category > legal entity default). (3) Candidate codes = TaxGroup.codes ∩ ItemTaxGroup.codes; if the ship-to jurisdiction requires stacked rates, expand jurisdiction codes. (4) Apply exemption certificates: remove exempted codes, record certificate. (5) Apply explicit line override (duty-gated). (6) Compute base per line: exclusive → amount; inclusive → back out (FR-13.5). (7) Evaluate brackets on line or document basis per code. (8) Calculate non-compound codes, then compound codes in `compound_sequence` order on `base + Σ earlier taxes`. (9) Apply min/max caps. (10) Reverse charge → emit input and output lines of equal amount; use tax → liability line with offset to the line's expense account. (11) Split recoverable/non-recoverable. (12) Round per BR-13.3. (13) Withholding is computed separately on the configured base and never alters the document tax. Every step writes to `determination_trace`.
- **BR-13.2 Provider precedence.** If a provider is configured for (legal entity, document type), steps 3–11 are delegated; the provider's results are normalized into the same structures; the internal engine still validates rounding and balancing of the returned lines.
- **BR-13.3 Rounding.** `per_document` (VAT default): sum unrounded tax per code across lines, round once to currency minor units, then distribute to lines proportionally with largest-remainder allocation so line taxes sum to the document tax. `per_line` (US default): round each line's tax per code. Rounding mode is the ledger's (wave 12).
- **BR-13.4 Inclusive back-out with compound taxes.** Solve `gross = base × Π(1 + r_i)` for compound stacks and `gross = base × (1 + Σ r_i)` for parallel codes; base rounded last; the sum of base + taxes must equal gross to the minor unit (residual to the largest tax line).
- **BR-13.5 Brackets.** Evaluated on the absolute base; credit notes (negative amounts) use the same bracket as the positive amount; `calc_basis = document` sums all lines of the same code before bracket lookup.
- **BR-13.6 Effective dates.** A code with no rate valid on `tax_date` → `TAX_RATE_NOT_FOUND` (422); a rate change mid-period is applied by date, never pro-rated.
- **BR-13.7 Reverse charge eligibility.** Requires a counterparty tax id passing the country format check (wave 7) and both parties in different EU member states (or domestic reverse-charge categories configured); otherwise falls back to standard rate with warning `TAX_REVERSE_CHARGE_INELIGIBLE`.
- **BR-13.8 Withholding thresholds.** Annual accumulation per (vendor, code, fiscal year); withholding applies to the amount exceeding the threshold in the document that crosses it; accumulation is updated only on posting, not calculation.
- **BR-13.9 Settlement state machine.** `proposed → posted | cancelled`; `posted` is terminal; a cancelled run releases its transactions; settlement for a period may not post while the period is `closed` for module TAX (wave 14); a posted run cannot include transactions dated after `period_to`.
- **BR-13.10 Provider commit idempotency.** `commit` with the same key is replayed; a provider `Void` is issued when a document is reversed (`journal.reversed` consumer) and failures queue for retry with alerting after 24 h.
- **BR-13.11 Negative and zero rates.** Rate 0 is valid (zero-rated vs exempt distinguished by `reason_code` on the code); negative rates are rejected (`TAX_RATE_INVALID`).
- **BR-13.12 Multi-currency.** Tax is calculated in transaction currency and converted with the document's rates (wave 12); settlement is in accounting currency; the ESL reports in EUR using the ECB rate on tax_date when the accounting currency is not EUR.
- **BR-13.13 Intrastat line lifecycle.** `transferred → validated | exception`; `exception → validated` after correction; `validated → compressed` (lines merged into one compressed line referencing members) → `exported`; an exported line is immutable; a later correction creates a new line with `correction_of_line_id` dated in the original period and marks the original `corrected`; a period can be exported more than once (each export run is a version; the latest is the declaration of record). Statistical value = invoice value × (1 + `freight_pct`) when the country rule is `invoice_plus_freight_to_border` and no explicit freight is available, with `freight_pct` on the country property (default 0).
- **BR-13.14 Intrastat thresholds.** The period summary compares cumulative calendar-year value per direction with the country threshold; below threshold the export is produced but flagged `declaration_not_required`; crossing the threshold mid-year sets `declaration_required_from` on the legal entity's country property and raises `TAX_INTRASTAT_THRESHOLD_CROSSED` (info).
- **BR-13.15 VAT recovery precedence.** Policy resolution: (expense category, expense country, legal entity) > (category, country, `*`) > (`*`, country, legal entity) > (`*`, country, `*`); the resolved `recoverable_pct` overrides the tax code's default for that line only; foreign reclaim applies only when expense country ≠ legal entity country, the vendor VAT id is valid for the expense country, and the invoice meets the country's minimum (EU: simplified invoice ≤ EUR 1,000 accepted; fuel ≤ EUR 250); amounts below `min_claim_amount` per quarter (EUR 400) or year (EUR 50) are carried to the annual claim.
- **BR-13.16 Withholding remittance.** A remittance run may include only `WithholdingTransaction` rows with `posting_date ≤ period_to`, not yet remitted, for one (authority, section); posting transfers the withholding payable to the authority party's open item (wave 19) or settlement account and sets `remitted = true`; cancelling a posted run is allowed only before the certificate issue and posts a negating entry; certificates are numbered per (legal entity, section, fiscal year) gap-free.

### 2.13.8 Acceptance criteria

- **AC-13.1** (FR-13.4, FR-13.14) Given the 500-scenario matrix, when run in CI, then 100% of scenarios match expected tax per line and per document to the minor unit.
- **AC-13.2** (FR-13.5, BR-13.4) Given a EUR 119.00 inclusive line at 19% VAT, when calculated, then base 100.00 and tax 19.00; given 3 inclusive lines of 33.33 each at 19%, then the document tax equals round(Σ unrounded) and line taxes sum to it.
- **AC-13.3** (FR-13.6) Given a Canadian QC line of CAD 100.00 with GST 5% and QST 9.975% (non-compound since 2013), when calculated, then taxes are 5.00 and 9.98; given a configured compound stack, then the second tax applies on base + first tax.
- **AC-13.4** (FR-13.6) Given an intra-EU purchase from a DE vendor into FR with a valid VAT id, when calculated, then an input and an output line of equal amount are produced and the net tax is 0.00.
- **AC-13.5** (FR-13.7) Given an expired exemption certificate, when calculated, then full tax applies and `TAX_EXEMPTION_EXPIRED` is in warnings.
- **AC-13.6** (FR-13.10) Given AvaTax times out, when calculating, then the internal engine responds within 2.5 s total, `fallback = true`, and `tax.provider.fallback` is emitted.
- **AC-13.7** (FR-13.11) Given the settlement fixture for March 2026, when the run is posted, then net payable equals output − input, the posting balances, and Σ settled transactions equals the GL tax account movement to the cent.
- **AC-13.8** (FR-13.12) Given the demo EU entity, when SAF-T is generated, then the XML validates against the OECD v2.0 XSD.
- **AC-13.9** (FR-13.8, BR-13.8) Given a vendor with USD 550 cumulative and a USD 600 threshold, when a USD 100 invoice is posted, then withholding applies to USD 50.
- **AC-13.10** (BR-13.9) Given the TAX module is closed for the period, when a settlement post is attempted, then 422 `LEDGER_PERIOD_CLOSED`.
- **AC-13.11** (FR-13.15) Given the Eurostat CN8 file for 2026 (≈ 9,500 codes), when imported, then every code has `validity_year = 2026`, the 2025 codes remain for historic lines, and a product category default resolves `commodity_code_id` on a sample product.
- **AC-13.12** (FR-13.16, BR-13.13) Given 1,200 dispatch lines for DE in March 2026 of which 30 lack net mass, when validated and compressed, then 30 exceptions are listed, the remaining lines compress to the expected 180 declaration lines (fixture), Σ invoice value equals the source documents, and the canonical export carries the partner VAT ids.
- **AC-13.13** (FR-13.16) Given an exported line whose commodity code is later corrected, when the journal is corrected, then a correction line with `correction_of_line_id` exists, the original is `corrected`, and a correction export run for the same period is produced.
- **AC-13.14** (FR-13.17, BR-13.15) Given a DE entity with an entertainment policy of 70% and an expense line of EUR 119.00 incl. 19% VAT, when calculated, then recoverable 13.30, non-recoverable 5.70 posted to the expense account; given a FR hotel invoice for the same DE entity, then the line is marked reclaimable under Directive 2008/9 with annex code 6 and appears in the Q1 reclaim extract for FR.
- **AC-13.15** (FR-13.18, BR-13.16) Given 40 Indian TDS transactions under section 194J for April 2026, when a remittance run is posted with challan reference, then the liability transfer balances, all 40 are `remitted`, and certificates issued for 12 payees are numbered gap-free and carry the challan reference.

### 2.13.9 Test plan and fixtures

- Fixtures: `tax-matrix-500` (US 200: multi-jurisdiction, exemptions, use tax; EU 180: standard/reduced/reverse charge/triangulation; IN 70: CGST/SGST/IGST, TDS; CA 50: GST/HST/PST/QST; each with expected results reviewed by an accountant); `jurisdictions-us-sample` (5 states' postal tables); `settlement-2026-03` (1,200 transactions with GL expectations); `saf-t-demo`; recorded AvaTax/Vertex HTTP fixtures.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Matrix runner | fixture (CI) | internal engine | 500/500 match |
| Rounding distribution property test | property | per_document rounding | line taxes sum to document tax; each line within 1 unit of proportional |
| Inclusive back-out property | property | FR-13.5 | base + taxes = gross exactly |
| Provider contract | contract (recorded) | AvaTax, Vertex adapters | request mapping and response normalization |
| Failover | integration (fault injection) | provider client | fallback within 2.5 s, event emitted |
| Settlement reconciliation | integration | settlement job | transactions = GL movement |
| SAF-T schema | unit | extract generator | XSD valid |
| Calculation latency | performance | tax API | 100-line document p99 < 50 ms internal |

Reconciliation: Σ TaxTransaction per code and period = GL tax account movement; settlement net = VAT return net box; ESL totals = Σ reverse-charge/intra-community output transactions.

### 2.13.10 Deployment and operations

Helm chart `passage-tax` (deployment 3–10 pods, CloudNativePG `tax-db`, topics `tax.calculations`, `tax.transactions`, `tax.settlements`; Kafka consumer of `ledger.journal`). Config: `TAX_PROVIDER_TIMEOUT_MS=2000`, `TAX_PROVIDER_RETRIES=1`, `TAX_PAYLOAD_RETENTION_DAYS=395`, `TAX_DEFAULT_ROUNDING=per_document`. Secrets: provider credentials in Vault paths `tax/providers/{le}`. Migrations: tax schema; partitions per fiscal year. Feature flags: `tax.provider.avalara`, `tax.provider.vertex`, `tax.extracts.saft`. Scheduled jobs: provider recommit retry (hourly), certificate expiry notifications (daily), settlement reminders per period. SLOs: calculate p99 < 50 ms (internal), availability 99.9%, provider fallback rate < 0.5%/day. Alerts: fallback rate > 2% over 1 h (P2), commit retry queue > 100 (P2), settlement mismatch (P2). Runbook: provider outage, re-commit queue drain, rate correction after posting (reverse documents, never edit transactions). Rollback: digest rollback; flags disable providers (internal engine only).

### 2.13.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX `TaxTable`, `TaxData`, `TaxAuthorityAddress`, `TaxPeriodHead` | TaxCode, TaxRate, authority Party, TaxSettlementPeriod | `TaxData` intervals → brackets; ledger posting group `TaxLedgerAccountGroup` → accounts |
| AX `TaxGroupHeading`/`TaxGroupData`, `TaxItemGroupHeading`/`TaxItemGroupData` | TaxGroup, ItemTaxGroup | exempt flags preserved |
| AX `TaxExemptCodeTable`, customer `TaxExemptNumber` | ExemptionCertificate | validity defaulted to migration date → open; steward review queue |
| AX `TaxWithholdTable`, `TaxWithholdGroupHeading`, 1099 setup | WithholdingTaxCode | |
| AX `TaxTrans` (unsettled only) | TaxTransaction | linked to migrated opening balance lines by code |
| SAP `T007A`/`T007S`, `KONP`/`A003`, `T030K` | TaxCode, TaxRate, accounts | tax procedure steps → compound sequences |
| SAP `TTXJ`/`TTXD` jurisdictions | TaxJurisdiction | |
| SAP `T059P`/`T059Z`, `LFBW` | WithholdingTaxCode, party assignments | |
| SAP `BSET` (unsettled) | TaxTransaction | |
| AX `IntrastatCommodity`, `IntrastatTransactionCode`, `IntrastatTransportMode`, `IntrastatPort`, `IntrastatStatProc`, `IntrastatParameters`, `IntrastatCountryRegionParameters` | CommodityCode, IntrastatTransactionCode, TransportMode, Port, StatisticsProcedure, CountryTradeProperty | commodity codes re-keyed to CN8 validity year; `InventTable.IntrastatCommodity` → product `commodity_code_id` (wave 8) |
| AX `Intrastat` (journal, current open period only), `IntrastatArchiveGeneral` | IntrastatLine | open period lines migrated as `transferred`; archived declarations kept in the legacy archive |
| AX `TrvTaxConfiguration` / expense tax setup (recoverable VAT per country) | VatRecoveryPolicy | category mapping to wave 43 expense categories |
| SAP `T604`, `T605`, `T616`, `T618`, `MARC-STAWN`, `VEIAV` (open period) | CommodityCode, IntrastatTransactionCode, TransportMode, Port, product commodity code, IntrastatLine | `VEIAV` rows for the current period only |
| SAP `T059P`/`T059Z` sections, J1INCHLN challan history (open quarter) | WithholdingRemittanceRun (posted, historical reference only) | prior certificates not re-issued |

Validation: code counts; a 200-document sample recalculated in Passage matches the source tax to the cent; unsettled totals per code match. Not migrated: settled history (kept in the legacy archive), provider transaction logs.

### 2.13.12 Dependencies, risks, open questions

Depends on: wave 11 (posting), wave 12 (currency conversion for tax amounts), wave 7 (party tax ids, VIES check), wave 8 (product categories), wave 6 (addresses).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Jurisdiction tables for US are vast and change monthly | High | Medium | Providers are the production default for US; internal engine ships sample states only |
| Country-specific VAT box mappings | High | Medium | Mapping is configuration (wave 48 packs); wave 13 ships DE/FR/UK/IN reference mappings |
| Rounding disputes with legacy figures | Medium | Low | Rounding mode configurable per code; migration validation sample |

Open questions: (1) Settlement counterpart as tax authority Party (vendor role) vs settlement account? Default Party; decide by wave 13 mid-point. (2) Store provider raw payloads for 13 months or 7 years? Default 13 months payload + 7 years hash; decide by wave 13 exit. (3) Should `tax/calculate` be callable from the posting API automatically for manual journals with a tax group? Default yes behind flag `ledger.auto_tax`; decide by wave 14.

### 2.13.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `tax.provider` per document type | legal entity × document type | enum | internal | internal / avalara / vertex | Engine selection (FR-13.10) | Sales tax parameters > External tax (ISV) / D365 Tax calculation parameters | External tax interface activation (`TTXD`, RFC destination) per tax procedure |
| `tax.provider_timeout_ms` / `tax.provider_retries` | tenant | int | 2,000 / 1 | 500–10,000 / 0–3 | Failover timing (FR-13.10) | — | RFC timeout |
| `tax.default_rounding` | legal entity | enum | per_document (VAT), per_line (US) | per_line / per_document | BR-13.3 | General ledger parameters > Sales tax > Rounding (per line / per document) "Calculation" | OBY6 "Tax rounding" / `T001-TXKRS`; condition rounding rule |
| `tax.price_includes_tax_default` | legal entity × document type | bool | false (US), true (B2C retail) | true / false | Default for `price_includes_tax` (FR-13.5) | Accounts receivable parameters > Prices include sales tax | Pricing procedure gross/net condition (`KZWI`) |
| `tax.tax_date_basis` | legal entity × document type | enum | document_date | document_date / delivery_date / posting_date | BR-13.1 step 1 | General ledger parameters > Sales tax > "Date of sales tax calculation" | OBCK/`T001-XTXDT` tax reporting date |
| `tax.conditional_settlement` (cash basis) | legal entity | bool | false | true / false | Tax becomes due on payment (AX conditional sales tax) | General ledger parameters > Sales tax > Conditional sales tax | `T007A` "deferred tax" (target tax code, RFUMSV50) |
| `tax.tax_on_prepayment` | legal entity | bool | false | true / false | Tax lines on prepayment invoices (waves 19/30) | General ledger parameters > Sales tax > Sales tax on prepayment | Down payment tax (F-29 with tax code) |
| `tax.cash_discount_base` | legal entity | enum | net_of_tax | net_of_tax / incl_tax | Cash discount tax adjustment base (waves 19/30) | General ledger parameters > Sales tax > Cash discount on invoice incl. sales tax | OBY6 "Discount base is net value" |
| `tax_code.settlement_period_id` and `authority_party_id` | tax code | ref | required | settlement periods, authority parties | Settlement grouping (FR-13.11) | Sales tax codes > Settlement period; Sales tax authorities | Tax payable posting F.12 by tax code; tax office vendor in `T001` |
| `tax_code.rounding` | tax code | enum | inherits entity | per_line / per_document | BR-13.3 per code | Sales tax authorities > Rounding | — |
| `tax.reporting_box_mapping` | legal entity (country) | jsonb | DE/FR/UK/IN reference mappings | box id → (code, direction, base/tax) | VAT return boxes (FR-13.12) | Sales tax reporting codes | ACR report definitions / `T007K` tax report box |
| `tax.saft_version` | legal entity | enum | OECD_2_0 | OECD_2_0 / country variants (wave 48) | SAF-T schema | Electronic reporting SAF-T format configuration | ACR SAF-T report |
| `withholding.trigger` | withholding code | enum | payment | invoice / payment | FR-13.8 | Withholding tax codes > "Withholding tax on invoice/payment" | `T059P` "accumulation / posting at payment" |
| `withholding.threshold` / `withholding.annual_accumulation` | withholding code | numeric / bool | 0 / false | ≥ 0 | BR-13.8 | Withholding tax codes > Limits | `T059Z` minimum amount, accumulation type |
| `intrastat.registration` (VAT id, declarant, agent) | legal entity | jsonb | required for EU entities | — | Export header (FR-13.15) | Foreign trade parameters > Intrastat | Foreign trade customizing: company code Intrastat data (VE0) |
| `intrastat.country_property.threshold_arrival` / `threshold_dispatch` | country | numeric | per country (DE EUR 3,000,000 arrival / 1,000,000 dispatch) | ≥ 0 | BR-13.14 | Country/region properties (EU) | Country-specific threshold in VEB settings |
| `intrastat.compression_fields` | country | jsonb | EU default set (FR-13.16) | field list | Compression keys | Intrastat compression setup | VE01 aggregation settings |
| `intrastat.statistical_value_rule` / `freight_pct` | country | enum / numeric | invoice_value / 0 | invoice_value / invoice_plus_freight_to_border / custom | BR-13.13 | Foreign trade parameters > Statistical value | VEB statistical value calculation |
| `vat_recovery.min_claim_quarter` / `min_claim_year` | legal entity | numeric | EUR 400 / EUR 50 | ≥ 0 | BR-13.15 carry-forward | — | — |
| `tax.payload_retention_days` | tenant | int | 395 | 30–2,555 | Provider payload retention (FR-13.10) | — | — |
| `tax.recommit_retry_interval` | tenant | duration | 1 h | 5 min–24 h | Provider recommit queue | — | — |

### 2.13.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Posted tax transactions | list inquiry | legal entity, code, period, direction, settled, document | document, code, jurisdiction, base, tax, recoverable, non-recoverable, direction, settled run | `TaxTransaction` | Posted sales tax inquiry; SSRS Sales tax transactions — details | S_ALR_87012357 (line items), FBL3N on tax accounts, `BSET` view |
| Tax specification by voucher | drill-down | voucher / document | lines with codes, base, tax, determination trace link | `TaxTransaction` ⋈ `JournalLine` | SSRS Sales tax specification by ledger transaction | FB03 → Tax data |
| Sales tax settlement report | statutory report | authority, period, version | output, input, use, net, by code; GL reconciliation panel | `TaxSettlementRun` | SSRS Sales tax payments; Settled sales tax inquiry | S_ALR_87012357 advance return, F.12 (RFUMSV10) |
| VAT return (box report) | statutory report | legal entity, period | box id, base, tax per box mapping | `TaxTransaction` grouped by `reporting_box_mapping` | Sales tax report (by reporting codes) | RFUMSV00 / S_ALR_87099918, ACR VAT return |
| EU sales list | statutory report | legal entity, period | customer VAT id, country, value goods, services, triangulation | `TaxTransaction` (reverse charge / intra-community output) | SSRS EU sales list | RFASLM00 / S_ALR_87012400 |
| Intrastat declaration summary | statutory report | legal entity, period, direction | lines, Σ invoice value, Σ statistical value, Σ mass, exceptions, threshold status | `IntrastatLine` | Intrastat report / Intrastat list | VE02 / VE03, MEIS output |
| Intrastat exceptions worklist | list inquiry | period, direction, rule | line, missing/invalid field, source document | `IntrastatLine` (status exception) | Intrastat validation result | VE01 error log |
| Withholding tax report | statutory report | legal entity, code / section, period, payee | payee, tax id, base, rate, withheld, remitted, certificate | `WithholdingTransaction`, `WithholdingRemittanceRun` | SSRS Withholding tax; Tax 1099 summary / detail | S_ALR_87012111, RFIDYYWT, S_P00_07000134 |
| VAT recovery report | operational report | legal entity, period, country, category | recoverable, non-recoverable, reclaimable, carried forward | `TaxTransaction` ⋈ `VatRecoveryPolicy` | SSRS VAT recovery report (expense) | — (custom report over `BSET` NAV/NVV) |
| Foreign VAT reclaim extract | statutory report | legal entity, refund country, period | invoice ref, vendor VAT id, date, taxable amount, VAT, annex code | `VatReclaimExtract` | Recover VAT (ER-based claim) | — |
| SAF-T audit file | statutory report | legal entity, period | GL entries, master files, source documents (OECD 2.0) | ledger + tax + party APIs | Electronic reporting SAF-T | ACR SAF-T (FIUAMSAFT) |
| Calculation tester / explain | drill-down | sample document | determination trace steps | tax engine | Sales tax calculation (tax information dialog) | Tax procedure analysis (FB60 > tax simulation) |
| Provider fallback KPI | KPI | legal entity, day | fallback rate, recommit queue size, mean provider latency | `tax.provider.fallback` events | — | — |
| Exemption certificate expiry | operational report | party, jurisdiction, days to expiry | certificate, valid_to, status, document link | `ExemptionCertificate` | Tax exempt numbers listing | Exemption licenses list (J_1BLFZ-type / `LFBW` exemption dates) |
| Tax configuration listing | operational report | legal entity | codes, rates, groups, item groups, accounts | tax setup | SSRS Sales tax list / Sales tax codes | `T007A` / FTXP listing |

### 2.13.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| VAT return | settlement period close / user | header: entity, VAT id, period, version; lines: boxes with base and tax; footer: net payable/refundable, preparer, settlement voucher | PDF, canonical JSON; country e-formats (ELSTER XML, UK MTD JSON, FR EDI-TVA) via wave 48 | settlement run id + version | statutory; 10 years (DE/FR), 6 (UK), 7 (US) |
| EU sales list | monthly / quarterly | header: entity VAT id, period; lines: customer VAT id, country, goods, services, triangulation | PDF, CSV, canonical XML; country e-formats in wave 48 | extract run id | statutory; 10 years |
| Intrastat declaration | monthly per direction | header: PSI VAT id, declarant, period, direction; lines: CN8, partner country, partner VAT id, origin, nature, transport, delivery terms, mass, units, values | canonical JSON, CSV; DE XML (Destatis IDEV), FR EMEBI, NL CBS via wave 48 | export run id (versioned) | statutory; 10 years |
| SAF-T file | on request / monthly (PT, NO, PL mandates) | OECD 2.0 header, master files, GL entries, source documents | XML (XSD-validated) | extract run id | statutory; equals ledger retention |
| Tax settlement voucher | settlement posted | entity, authority, period, codes, output/input/net, voucher | PDF | ledger voucher | financial record; 10 years |
| Withholding certificate | certificate issue | payer, payee, tax ids, section, period, base, rate, withheld, remittance references, certificate no. | PDF; country forms (Form 16A, 1099-NEC/MISC, 1042-S) rendered by wave 48 | per (legal entity, section, fiscal year), gap-free | statutory; 7–10 years per country |
| Withholding remittance statement | remittance run posted | authority, section, period, transactions, challan/bank reference | PDF, CSV | remittance run id | statutory; 10 years |
| Foreign VAT reclaim file | reclaim extract | applicant, refund country, period, invoices with annex codes | XML per refund-country portal (wave 48), CSV | extract run id | statutory; 10 years |
| Exemption certificate image | certificate registered | uploaded document | PDF / image | certificate number (external) | statutory; certificate validity + 4 years (US streamlined) |

### 2.13.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `tax-provider-recommit` | hourly | legal entity × provider | commit with stored `Idempotency-Key`; queue item deleted on success | 1,000 items < 2 min | exponential back-off; alert after 24 h | queue > 100 → P2 |
| `tax-journal-link` (consume `journal.posted`, link TaxTransaction) | continuous | legal entity | upsert by (source, line_ref) | lag p95 < 5 s | dead-letter after 5 attempts | DLQ > 0 → P2 |
| `tax-settlement-propose` | per settlement period end + 1 day | authority | run per (authority, period, version) | 1M transactions < 3 min | proposal marked failed; close task blocked | mismatch → P2 |
| `tax-certificate-expiry` | daily 06:00 | legal entity | daily snapshot | < 1 min | — | certificates expiring ≤ 30 days → notification |
| `intrastat-transfer` | monthly, 3rd business day 05:00 | legal entity × direction | transfer keyed by (source line, period); re-run adds only new and reversed lines | 100k lines < 5 min | source resolver errors listed; run continues | exceptions > 0 → close task "Intrastat exceptions" |
| `intrastat-cn8-import` | yearly, 1 December | tenant | file hash | < 1 min | quarantine file | import missing by 31 December → P3 |
| `withholding-accumulation-rebuild` | daily 02:30 | vendor | recomputes annual accumulation from posted transactions; compare | 100k vendors < 5 min | — | diff > 0 → P2 |
| `vat-reclaim-quarterly` | quarter end + 10 days | legal entity × refund country | run per (country, quarter) | < 2 min | — | — |
| `saft-monthly` (where mandated) | 5th day of month | legal entity | run per period | 1M lines < 10 min | retry once | XSD failure → P2 |

### 2.13.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| Calculate tax (service accounts) | POST /tax/calculate, /tax/commit | not assignable to humans | Document service accounts |
| View tax configuration | /tax/calculate:explain; read screens | — | Tax accountant, Accountant, Internal auditor |
| Maintain tax codes | PUT codes, rates, VAT recovery policies; screen Tax codes, VAT recovery policies | Settle tax; Produce tax reports (same period sign-off) | Tax manager |
| Maintain tax groups | PUT groups / item-groups | Maintain tax codes (optional) | Tax manager, Tax accountant |
| Maintain tax jurisdictions | PUT jurisdictions, postal table import | — | Tax manager |
| Maintain tax exemptions | certificates CRUD | Maintain customer master (wave 7) | Tax accountant, AR credit specialist |
| Maintain withholding tax | withholding codes | Settle tax | Tax manager |
| View tax transactions | GET transactions, Intrastat lines (read) | — | Tax accountant, Accountant, Internal auditor |
| Settle tax | settlements, :post; withholding remittances | Maintain tax codes; Maintain withholding tax | Tax accountant (senior), Financial controller |
| Produce tax reports | extracts, Intrastat exports, VAT reclaims, certificates | Maintain tax codes | Tax accountant |
| Maintain foreign trade setup | Intrastat master data CRUD and CN8 import | Maintain Intrastat journal | Tax manager, Trade compliance officer |
| Maintain Intrastat journal | transfer, PATCH lines (corrections), validate, compress | Maintain foreign trade setup | Trade compliance clerk, Tax accountant |
| Administer tax | provider settings, credentials (Vault), recommit queue | Settle tax | Finance platform administrator |

### 2.13.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| TAX-0001 `TAX_RATE_NOT_FOUND` | error | Tax code {code} has no rate valid on {tax_date}. | BR-13.6 | Add a rate row | FR-13.1 |
| TAX-0002 `TAX_RATE_INVALID` | error | Rate {rate} is not valid; rates must be ≥ 0 with intervals that do not overlap. | BR-13.11 | Correct the rate rows | FR-13.1 |
| TAX-0003 `TAX_GROUP_UNRESOLVED` | error | No tax group or item tax group could be resolved for line {line}. | BR-13.1 step 2 | Set party/product defaults or override | FR-13.2 |
| TAX-0004 `TAX_NO_INTERSECTION` | warning | Groups {group} and {item_group} share no tax code; line {line} is untaxed. | BR-13.1 step 3 | Review group setup | FR-13.4 |
| TAX-0005 `TAX_EXEMPTION_EXPIRED` | warning | Exemption certificate {number} expired on {valid_to}; full tax applied. | FR-13.7 | Renew the certificate | FR-13.7 |
| TAX-0006 `TAX_REVERSE_CHARGE_INELIGIBLE` | warning | Reverse charge not applicable: {reason}; standard rate applied. | BR-13.7 | Check VAT id and countries | FR-13.6 |
| TAX-0007 `TAX_PROVIDER_FALLBACK` | warning | Provider {provider} unavailable; internal engine used. Document flagged for recommit. | FR-13.10 | None; recommit runs automatically | FR-13.10 |
| TAX-0008 `TAX_SETTLEMENT_MISMATCH` | error | Settlement {total} differs from GL tax account movement {gl} by {diff}. | FR-13.11 | Investigate unlinked tax lines or manual postings on tax accounts | FR-13.11 |
| TAX-0009 `TAX_SETTLEMENT_PERIOD_CLOSED` | error | Module TAX is closed for {period}. | BR-13.9 | Reopen TAX or settle next period | BR-13.9 |
| TAX-0010 `TAX_OVERRIDE_FORBIDDEN` | error (403) | You may not override the tax code on this line. | BR-13.1 step 5 | Remove the override or request the duty | FR-13.4 |
| TAX-0011 `TAX_INCLUSIVE_RESIDUAL` | info | Inclusive back-out residual {residual} applied to the largest tax line. | BR-13.4 | None | FR-13.5 |
| TAX-0012 `TAX_INTRASTAT_INVALID` | error | Intrastat line {line}: {field} {reason}. | FR-13.16 validation | Correct the line or the source master data | FR-13.16 |
| TAX-0013 `TAX_INTRASTAT_COMMODITY_MISSING` | error | No commodity code for product {product} on line {line}. | Product without CN8 | Set the product or category commodity code | FR-13.15 |
| TAX-0014 `TAX_INTRASTAT_LINE_EXPORTED` | error | Line {line} was exported in run {run} and cannot be edited; create a correction. | BR-13.13 | Use the correction action | FR-13.16 |
| TAX-0015 `TAX_INTRASTAT_THRESHOLD_CROSSED` | info | Cumulative {direction} value {value} exceeds the {country} threshold {threshold}; declarations required from {period}. | BR-13.14 | Submit declarations from that period | FR-13.16 |
| TAX-0016 `TAX_COMMODITY_CODE_EXPIRED` | warning | Commodity code {code} is not valid for {year}. | CN8 yearly change | Map to the successor code | FR-13.15 |
| TAX-0017 `TAX_VAT_RECOVERY_POLICY_MISSING` | warning | No VAT recovery policy for category {category} in {country}; tax code default {pct}% used. | BR-13.15 | Add a policy | FR-13.17 |
| TAX-0018 `TAX_RECLAIM_BELOW_MINIMUM` | info | Reclaimable VAT {amount} for {country} is below the minimum; carried to the annual claim. | BR-13.15 | None | FR-13.17 |
| TAX-0019 `TAX_WHT_ALREADY_REMITTED` | error | Transaction {id} is already included in remittance {run}. | BR-13.16 | Exclude it | FR-13.18 |
| TAX-0020 `TAX_WHT_CERTIFICATE_ISSUED` | error | Remittance {run} has issued certificates and cannot be cancelled. | BR-13.16 | Reissue certificates with corrections | FR-13.18 |
| TAX-0021 `TAX_SAFT_SCHEMA_INVALID` | error | SAF-T file failed XSD validation: {detail}. | FR-13.12 | Correct source master data (e.g., missing tax registration) | FR-13.12 |

### 2.13.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| `POST /tax/calculate` (internal, 100 lines) | 300k documents/day; peak 200/s | 8 / 25 / 50 ms | 1,000 documents/s per 10 pods | none (stateless) | rate/group caches per pod | HPA to 10 pods; p99 ≤ 120 ms |
| `POST /tax/calculate` (provider) | 30% of US documents | provider + 10 ms | per provider quota | payload 13 months ≈ 50 GB | — | timeout 2 s → internal fallback |
| TaxTransaction linking | 1M tax lines/month | lag p95 < 5 s | 2,000/s | 350 B/row × 12M = 4 GB | partition by fiscal year; indexes §2.13.4 | lag grows; settlement waits for lag = 0 |
| Settlement proposal | monthly; 1M transactions | — | < 3 min | settlement rows small | (le, code, period, settlement_run_id) | runs on replica |
| Intrastat transfer + validate + compress | 100k lines/month per entity | — | < 5 min | 250 B × 1.2M/year = 300 MB | (le, period, direction, status) | chunked per source type |
| VAT return / ESL extract | monthly | — | < 1 min | outputs ≈ 1 MB | — | — |
| SAF-T export | monthly / on request; 1M lines | — | < 10 min | 1 GB XML per month (object storage, compressed 10:1) | streamed generation | queued, one per entity |
| Calculation explain | 5k/day | 15 / 60 / 120 ms | 100/s | — | — | — |

### 2.13.20 Localization and compliance hooks

- Tax regimes vary by country in rates, mechanisms (VAT vs GST vs sales/use), reverse-charge categories (domestic reverse charge for construction, scrap, mobile phones in DE/UK), cash-basis VAT (FR services, PT), split payment (IT, PL), and tax point rules; the internal engine models them as configuration; wave 48 packs ship tax code sets, box mappings and settlement periods per country.
- Statutory filings: VAT return e-formats (ELSTER DE, MTD UK, EDI-TVA FR, SII ES real-time ledger, Fattura/LIPE IT), ESL formats, Intrastat national formats, SAF-T variants (PT, NO, PL JPK, RO D406), US 1099 IRIS/FIRE files, India GSTR-1/3B and TDS 26Q, Canada GST34 are produced by pack exporters registered on the `tax.extract_format` extension point over the canonical datasets of FR-13.12, FR-13.16, FR-13.18.
- E-invoicing clearance (SDI, CFDI, Peppol) is wave 35/48; the tax engine supplies tax breakdowns in the canonical form these require.
- Currency: VAT returns in national currency; ESL in EUR (BR-13.12); statutory rate type per country (wave 12 §2.12.20).
- Rounding: per-document vs per-line default per country (DE/FR per document, US per line, IN per line per GST rules).
- Retention: VAT records 10 years (DE, FR), 6 (UK), 7 (US), 8 (IN); tax transactions are part of the append-only ledger domain and never purged in Phase 2.
- Privacy: party tax ids and addresses are personal data for sole traders; Intrastat partner VAT id and withholding payee data are tagged `pii` for wave 7 data-subject requests; exports are access-logged.
- Foreign trade beyond Intrastat (export documents, customs declarations, export control screening) is a wave 48 foreign trade pack; wave 13 provides commodity codes, country of origin and trade fields reused by that pack.
- Extension points: `tax.provider` adapter, `tax.extract_format`, `tax.box_mapping_pack`, `tax.intrastat_validation_pack`, `tax.withholding_form` renderer.

### 2.13.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.1.4 Setup — Foreign trade → Intrastat parameters, commodity codes, transaction codes, transport methods, ports, statistics procedure (AX register row 7) | PARTIAL-13 | ADDED as FR-13.15 | master data entities and screen Foreign trade setup |
| AX §12.10.2 Inquiries & Reports — SSRS VAT recovery report (expense tax) (AX register row 78) | PARTIAL-13 | ADDED as FR-13.17 | VAT recovery report in §2.13.14 |
| AX §12.10.3 Periodic — Expense → Recover VAT (ER-based claim) (AX register row 81) | PARTIAL-13 | ADDED as FR-13.17 | foreign VAT reclaim extract; portal file formats DEFERRED to wave 48 |
| AX §12.10.4 Setup — Expense → Tax configuration (recoverable VAT per country, tax groups on expenses) (AX register row 86) | PARTIAL-13 | ADDED as FR-13.17 | `VatRecoveryPolicy`; expense categories come from wave 43 |
| AX §12.14.1 Common — Inventory → Commodity codes (Intrastat on item) (AX register row 124) | PARTIAL-13 | ADDED as FR-13.15 | product `commodity_code_id` (wave 8 field) referencing `tax.CommodityCode` |
| AX §12.29.1 Common — Foreign trade → Intrastat (journal) / EU sales list (link) (AX register row 236) | PARTIAL-13 | ADDED as FR-13.16 | Intrastat journal with corrections and audit |
| AX §12.29.3 Periodic — Foreign trade → Intrastat transfer / validation / export (file) (AX register row 247) | PARTIAL-13 | ADDED as FR-13.16 | transfer, validate, compress, export; national file formats via wave 48 |
| AX §12.29.4 Setup — Foreign trade parameters, Commodity codes, Transaction codes, Transport methods, Ports, Statistics procedures, Country/region properties, Intrastat compression, check setup, EU sales list reporting (AX register row 255) | PARTIAL-13 | ADDED as FR-13.15 (setup), FR-13.16 (compression, checks) | ESL reporting was already FR-13.12 |
| SAP §13.2 FI-AP — J1INCHLN / J1INBANK / J1INCERT (IN WHT) (SAP register row 25) | PARTIAL-13 | ADDED as FR-13.18 (remittance, challan reference, certificate numbering); Form 16A / 26Q rendering DEFERRED to wave 48 | IN country pack renders forms over the canonical dataset |
| SAP §13.17 LE-SHP — Foreign trade / export documents in delivery (customs, commercial invoice, EUR.1, Intrastat from delivery; GTS) (SAP register row 205) | PARTIAL-13 | ADDED as FR-13.16 for Intrastat from deliveries (via `IntrastatSource`); export documents and customs DEFERRED to wave 48 | register proposes a wave 48 foreign trade/customs pack; wave 28 shipments register as an Intrastat source |
| SAP §13.17 LE-SHP Customizing — Foreign trade: export control, preference, Intrastat settings (VEB, VE0; `T604`, `T605`) (SAP register row 208) | PARTIAL-13 | ADDED as FR-13.15 for Intrastat settings; export control and preference DEFERRED to wave 48 | denied-party screening and preference calculation are trade compliance, not tax |
| SAP §13.18 TM — Customs declarations via GTS from TM (SAP register row 212) | PARTIAL-13 | DEFERRED to wave 48 | customs declarations belong to the foreign trade/customs pack; wave 13 supplies commodity codes and origin data |

## 2.14 Wave 14: Period Close, Year-End & Allocations

### 2.14.1 Scope and business context

AX controls posting through `LedgerPeriod` statuses (open, on hold, closed) per module (`LedgerPeriodModuleAccessControl`) and offers the financial period close workspace, allocation rules (`LedgerAllocation`), accrual schemes and year-end close; SAP uses OB52 posting period intervals per account type with authorization groups, the Financial Closing Cockpit, assessment/distribution cycles (KSV5/KSU5), the accrual engine and balance carry-forward (FAGLGVTR). Wave 14 adds to Passage: a period-control matrix enforced inside the posting API; a close checklist run on Temporal with templates, dependencies and a cross-entity board; an allocation engine (fixed, basis, iterative) with reversal; accrual schemes and recurring journal scheduling (completing FR-11.10); and year-end close with carry-forward per dimension.

After this wave a controller can close March 2026 for three entities from one board, with every subledger path rejected after close, allocations and accruals posted and reversed on schedule, and year-end producing opening balances. Out of scope: subledger-specific close tasks (inventory close wave 20, AP/AR aging waves 19/30), consolidation (wave 40), budget vs actual (wave 38).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| SAP OB52 (`T001B`): closing a period is an edit of interval rows per account type and company code, done by Basis/FI power users across many company codes; a typo opens the wrong range and postings land in a closed month | Period control is a maintenance view, not a controlled transition with validation | Period-control grid with state machine (BR-14.3), GL-closes-last rule, bulk close across ledgers with confirmation | Close of 3 entities × 10 modules: 30 OB52 edits → 1 bulk action; wrong-period postings → 0 |
| Reopening a closed period in AX (Ledger calendar form) or SAP is a single-user action with no approval; auditors find silent reopenings in the database log weeks later | No workflow on period status | Reopen requires two approvers, a reason, an expiry timer and emits `close.period.status-changed` | Unapproved reopenings → 0; audit evidence available in < 1 s rather than at audit time |
| Close tracking lives in Excel or the AX financial period close workspace that does not block dependent tasks; SAP Closing Cockpit setup requires consultants | Task status is informational; automation hooks are limited to batch jobs | Temporal-driven CloseRun with DAG dependencies, automation hooks that complete on completion events, cross-entity board | Close coordination emails/period 50+ → 0; task status latency hours → seconds |
| AX allocation rules (`LedgerAllocation`) and SAP KSV5/KSU5 cycles post rounding residues to odd destinations and have no reciprocal (iterative) option without CO-OM workarounds; reruns double-post | Residual handling undefined; cycles not idempotent | Largest-share residual rule, iterative solver with convergence check, runs reversible as a unit and idempotent by key | Allocation rerun duplicates → 0; reciprocal allocations supported natively (vs manual spreadsheets) |
| FAGLGVTR / AX year-end close must be rerun after late postings; operators delete and regenerate opening entries, breaking the audit trail | Carry-forward implemented as mutable opening balances | Year-end run reverses its predecessor and regenerates (append-only), verifies opening = closing per combination before commit | Opening/closing mismatch after reruns → 0; audit trail unbroken |

### 2.14.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Period status | `LedgerPeriod` (Open/On hold/Closed), Ledger calendar form | `T001B` intervals via OB52 (periods 1–12, 13–16 special) | `ledger.PeriodStatus` per ledger × period × module | full |
| Module access control | `LedgerPeriodModuleAccessControl`, user groups per module | OB52 authorization group (`BRGRU`), account type (+, A, D, K, M, S) | PeriodStatus.module × `allowed_group_id` | full |
| Special periods | Closing period type on fiscal calendar | Periods 13–16 | Fiscal period `kind = adjustment` (wave 6) with own PeriodStatus | full |
| Close workspace | Financial period close workspace (templates, tasks, dependencies, calendar) | Financial Closing Cockpit / Advanced Financial Closing | `close` service: CloseTemplate, CloseRun, CloseTask (Temporal) | full |
| Allocations | `LedgerAllocation`, allocation rules (basis/fixed/equally/spread), process allocation request | KSV5 (distribution), KSU5 (assessment), cycles/segments, FAGLGA35 | `ledger.AllocationRule`, `AllocationRun` with fixed, basis, equal, iterative methods | full |
| Accruals | Accrual schemes (`LedgerAccrualScheme`), ledger accruals | Accrual engine (ACE), FBS1 with reversal | `ledger.AccrualScheme`; reversal via wave 11 reversal | full |
| Recurring entries | Periodic journals | FBD1/F.14 | `ledger.RecurringJournal` (wave 11) with calendar-relative schedules | full |
| Year-end close | General ledger > Year-end close (`LedgerFiscalYearClose...`), opening transactions, retained earnings per dimension | FAGLGVTR balance carry-forward, retained earnings account (OB53) | `ledger.YearEndCloseRun` producing period-0 opening entries | full |
| Reopen period | Change status back to Open | OB52 edit | Dual-approval reopen workflow | improved: four-eyes, audited |
| Close calendar / status board | Workspace tiles per entity, task list by due date | Cockpit task list, status overview | Close board across legal entities | full |
| Balance-sheet regrouping / reclassification | Not standard (manual reclassification journals; vendor debit balances via "Customer/vendor balance list" and manual journal) | FAGLF101 / F101 sort and reclassify by remaining term, FAGL_CL_REGROUP (S/4 closing regrouping), FAGL_CL_RGRP sort method and adjustment accounts (`T030`, `T043`), regroup vendors with debit balances and customers with credit balances | `ledger.RegroupingRule`, `ledger.RegroupingRun` with auto-reversal (FR-14.13); AP/AR item sources from waves 19/30 | full |

### 2.14.3 Functional requirements

- **FR-14.1 Period-control matrix** — Must. The system shall maintain a status per (ledger, fiscal period, module) where module ∈ {GL, AP, AR, INV, FA, PROJ, TAX, BANK, PAYROLL, ALL} and status ∈ {never_opened, open, on_hold, closed}.
  - `ALL` is the default when no module row exists; a module row overrides `ALL`. `on_hold` carries `allowed_group_id` (user group that may still post).
  - The posting API resolves module from `source.system` (mapping table, default GL for manual journals) and rejects per BR-14.1. The check is inside the posting transaction (status row read with `FOR SHARE`), so a close committing concurrently wins or loses atomically.
- **FR-14.2 Period status transitions** — Must. The system shall allow `never_opened → open`, `open ↔ on_hold`, `open|on_hold → closed`, and `closed → open` only through the reopen workflow (FR-14.3).
  - Closing a period for `ALL` requires every module row to be closed or absent; closing GL requires all other modules closed (GL closes last).
  - Period 13–16 adjustment periods may be open while 12 is closed.
- **FR-14.3 Reopen with dual approval** — Must. The system shall reopen a closed period only via a Temporal workflow requiring two distinct approvers holding duty `Approve period reopen`, a reason, and an expiry (reopened period auto-closes after N days, default 3).
  - Each reopen increments `reopen_count` and emits `close.period.status-changed` with both approvers.
- **FR-14.4 Close templates and runs** — Must. The system shall define close templates (tasks with module, description, assignee role, due offset in business days from period end, dependencies, evidence requirement, automation hook) and instantiate a `CloseRun` per (template, period, set of legal entities).
  - Maximum 500 tasks per template; dependencies form a DAG (cycle rejected on save).
  - Automation hooks call Passage jobs (revaluation, allocation, accrual, settlement, year-end, statement pack) and mark the task complete when the job emits its completion event.
- **FR-14.5 Close board** — Must. The system shall show tasks across entities by status (not started, in progress, blocked, completed, skipped) with due dates, owners, overdue counts, and allow bulk reassignment and evidence attachment.
- **FR-14.6 Allocation rules** — Must. The system shall define allocation rules with source (account/dimension filter, period, ledger), destination (account/dimension targets), method ∈ {fixed_percentage, fixed_weight, equally, basis}, basis source (balances of statistical or financial accounts by dimension, e.g., headcount), offset account, and `allocate_from_zero` behavior.
  - Percentages must sum to 100.000000 (tolerance 0); weights normalized; basis computed from the Balance projection for the period.
  - Rounding per BR-14.4; destination lines inherit dimensions from the basis.
- **FR-14.7 Iterative (reciprocal) allocations** — Should. The system shall solve mutually dependent rules (service departments allocating to each other) iteratively until the residual per source < 1 minor unit or 100 iterations, then post one entry per rule.
- **FR-14.8 Allocation runs and reversal** — Must. Runs support simulate and post modes, record the basis snapshot, and are reversible as a unit (reversing every produced entry); a rule may be flagged `auto_reverse_next_period`.
- **FR-14.9 Accrual schemes** — Must. The system shall define schemes (debit/credit accounts, frequency, number of occurrences, spread method ∈ {equal, by_days, custom}) that split a source line into future-dated entries, each auto-reversed per scheme (e.g., monthly accrual reversed on day 1).
  - Generated entries are scheduled on Temporal and posted on their dates if the period is open; otherwise the task appears as blocked on the close board.
- **FR-14.10 Year-end close** — Must. The system shall close a fiscal year per ledger: (a) for each P&L account × dimension combination, post the net to retained earnings (per dimension when `retain_dimensions = true`, else to the ledger's retained earnings combination) in the closing period, and (b) create opening entries in period 0 of the next year for each balance-sheet account × combination equal to its closing balance.
  - Re-runnable: a subsequent run reverses the prior run's entries and regenerates (append-only).
  - Requires all periods of the year closed for all modules except `GL` in `on_hold` for the closing user group; year-end entries are flagged `is_closing_entry`/`is_opening_entry` and excluded from P&L statements.
- **FR-14.11 Recurring journal scheduling** — Must. Completes FR-11.10: schedules by fiscal period offset (e.g., "last business day of period"), with pause/resume, next-run preview and a generated-batch history.
- **FR-14.12 Close reporting** — Should. The system shall report close duration per period and entity (period end to GL closed), tasks completed late, and reopen history.
- **FR-14.13 Balance-sheet regrouping (reclassification)** — Must. The system shall run, per ledger and key date, regrouping rules that reclassify balance-sheet positions for presentation and post adjustment entries to adjustment accounts, auto-reversed on the first day of the next period:
  - Sort method `remaining_term` (FAGLF101 semantics): open items of a source (GL open items from FR-11.19 in this wave; vendor and customer open items and receivable/payable reconciliation accounts when waves 19/30 register their `open_item_source`) are bucketed by remaining term from the key date to `due_date` (default buckets ≤ 1 year, 1–5 years, > 5 years) and each bucket's amount is reclassified from the source account to the bucket's adjustment account with the configured target (e.g., long-term portion of a loan or receivable).
  - Sort method `debit_credit_balance` per party: vendors whose net balance is a debit are reclassified from payables to "receivables – vendors with debit balance"; customers with a credit net balance from receivables to "payables – customers with credit balance"; the balance is computed per party × reconciliation account × splitting dimensions (FR-11.23) and currency.
  - Sort method `account_balance_sign` for GL accounts (bank accounts with overdraft reclassified to short-term borrowings; VAT receivable/payable offset positions): the whole account × combination balance is reclassified when its sign is opposite the account's `normal_balance`.
  - Each run supports simulate and post, records the snapshot it used, is idempotent per (ledger, key date, rule set), and is reversible as a unit; posted entries carry `is_regrouping = true` and keep the source line's dimensions; the close checklist (FR-14.4) offers a "Regrouping" automation hook.

### 2.14.4 Data model

**PeriodStatus**

| Field | Type | Required | Notes |
|---|---|---|---|
| ledger_id | uuid | yes | PK part |
| fiscal_period_id | uuid | yes | PK part |
| module | enum | yes | PK part; ALL, GL, AP, AR, INV, FA, PROJ, TAX, BANK, PAYROLL |
| status | enum | yes | never_opened, open, on_hold, closed |
| allowed_group_id | uuid | no | required when on_hold |
| reopen_count | int | yes | default 0 |
| reopen_expires_at | timestamptz | no | |
| changed_at / changed_by / approvals | timestamptz / uuid / jsonb | yes | approvals = [{user_id, at}] for reopen |

**CloseTask** (within CloseRun)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| run_id | uuid | yes | FK CloseRun (template_id, fiscal_period_id, legal_entity_ids[], status) |
| legal_entity_id | uuid | yes | |
| template_task_id | uuid | yes | |
| module | enum | yes | |
| name / description | text | yes | |
| assignee_user_id / assignee_role | uuid / text | conditional | |
| due_at | timestamptz | yes | period_end + offset business days |
| depends_on | uuid[] | no | task ids |
| status | enum | yes | not_started, in_progress, blocked, completed, skipped |
| evidence_uri / evidence_required | text / bool | conditional | |
| automation | jsonb | no | {job, params, completion_event} |
| completed_at / completed_by | timestamptz / uuid | no | |
| temporal_workflow_id | text | yes | |

**AllocationRule** and **AllocationRun**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| ledger_id | uuid | yes | |
| name | text(60) | yes | unique per ledger |
| source_filter | jsonb | yes | account ranges + dimension filters + `include_opening` |
| method | enum | yes | fixed_percentage, fixed_weight, equally, basis |
| destinations | jsonb | yes | [{account, dimensions, pct|weight}] or basis spec {account ranges, dimension_to_spread_by, period_offset} |
| offset_account_id / offset_dimension_mode | uuid / enum | yes | mode: source_dimensions, fixed |
| iterative_group | text | no | rules in the same group are solved together |
| auto_reverse_next_period | bool | yes | |
| run: id, rule_ids[], fiscal_period_id, mode (simulate/post), status (proposed, posted, reversed, cancelled), iterations, basis_snapshot (jsonb), entry_ids[], reversal_entry_ids[] | | | |

**AccrualScheme**: `id`, `ledger_id`, `code`, `debit_account_id`, `credit_account_id`, `frequency` (monthly, quarterly, custom), `occurrences`, `spread` (equal, by_days, custom[]), `start_rule` (document_date, next_period_start), `auto_reverse` bool, `reverse_day` int. **YearEndCloseRun**: `id`, `ledger_id`, `fiscal_year_id`, `retained_earnings_combination_id`, `retain_dimensions`, `status`, `closing_entry_ids[]`, `opening_entry_ids[]`, `previous_run_id`.

**RegroupingRule** and **RegroupingRun** (FR-14.13)

| Field | Type | Required | Notes |
|---|---|---|---|
| rule: id / ledger_id / code | uuid / uuid / text(20) | yes | unique (ledger_id, code) |
| rule: sort_method | enum | yes | remaining_term, debit_credit_balance, account_balance_sign |
| rule: source | jsonb | yes | {source_type: gl_open_item / vendor / customer / account_balance, account ranges, dimension filter} |
| rule: buckets | jsonb | conditional | remaining_term: [{from_days, to_days, adjustment_account_id, target_account_id}] |
| rule: adjustment_account_id / target_account_id | uuid | conditional | debit/credit balance and sign methods |
| rule: group_by | text[] | yes | party, reconciliation account, splitting dimensions, currency |
| rule: active / sequence | bool / int | yes | |
| run: id / ledger_id / key_date / rule_ids | uuid / uuid / date / uuid[] | yes | |
| run: mode / status | enum / enum | yes | simulate, post; proposed, posted, reversed, cancelled |
| run: snapshot | jsonb | yes | per rule: groups with balances, buckets, items count (compressed) |
| run: entry_ids / reversal_entry_ids | uuid[] | no | reversal dated key_date + 1 day |
| run: totals | jsonb | yes | per adjustment account |

**Invariants**: at most one PeriodStatus per (ledger, period, module); `closed` GL implies every other module closed; an AllocationRun that is `posted` has Σ destination amounts = source amount per rule (after rounding); YearEndCloseRun opening balances per combination = closing balances (verified by the run before commit). Indexing: `period_status` PK; `close_task` on (run_id, status), (assignee_user_id, due_at); `allocation_run` on (ledger_id, fiscal_period_id).

### 2.14.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| GET | /api/v1/ledger/ledgers/{id}/periods?year= | Period-control matrix | n/a | View period status |
| PATCH | /api/v1/ledger/ledgers/{id}/periods/{period}/modules/{module} | Transition status (open/on_hold/closed) | naturally idempotent on target status | Maintain period status |
| POST | /api/v1/ledger/ledgers/{id}/periods/{period}:request-reopen | Start reopen workflow | `Idempotency-Key` | Request period reopen |
| POST | /api/v1/close/templates and GET/PUT /{id} | Templates | PUT idempotent | Maintain close templates |
| POST | /api/v1/close/runs | Instantiate run for period × entities | `Idempotency-Key` | Run period close |
| GET | /api/v1/close/runs/{id}/tasks?entity=&status= | Board data | n/a | View close board |
| POST | /api/v1/close/tasks/{id}:start, :complete, :skip, :reassign | Task actions | `Idempotency-Key` | Work close tasks |
| GET/PUT | /api/v1/ledger/ledgers/{id}/allocation-rules[/{rule}] | Rules | PUT idempotent | Maintain allocation rules |
| POST | /api/v1/ledger/allocations | Run (simulate/post) | `Idempotency-Key` | Run allocations |
| POST | /api/v1/ledger/allocations/{id}:post, :reverse | Post proposal / reverse run | `Idempotency-Key` | Run allocations |
| GET/PUT | /api/v1/ledger/ledgers/{id}/accrual-schemes[/{code}] | Schemes | PUT idempotent | Maintain accrual schemes |
| POST | /api/v1/ledger/year-end-closes | Run year-end close (simulate/post) | `Idempotency-Key` | Run year-end close |
| GET/PUT | /api/v1/ledger/recurring-journals[/{id}] and :pause, :resume | Schedules | PUT idempotent | Maintain ledger journals |
| GET/PUT | /api/v1/ledger/ledgers/{id}/regrouping-rules[/{code}] | Regrouping rules (FR-14.13) | PUT idempotent | Maintain regrouping rules |
| POST | /api/v1/ledger/regroupings and /{id}:post, :reverse | Regrouping run simulate / post / reverse | `Idempotency-Key` | Run regrouping |

Commands: `TransitionPeriodStatus` (validates FR-14.2 ordering; closing with unposted approved batches dated in the period → `CLOSE_PENDING_BATCHES` unless `force` with duty); `RequestReopen` (reason ≥ 10 chars, two approvers distinct from requester); `RunAllocation` (period open for GL, basis non-zero or `allocate_from_zero`); `RunYearEndClose` (all periods closed per FR-14.10, retained earnings account exists and is equity).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.close.period.status-changed.v1 | PeriodStatus transition | ledger_id, fiscal_period_id, module, from, to, actor, approvals[], reason | all posting services (cache), close, reporting, audit |
| dev.passage.close.run.created.v1 | run instantiated | run_id, template_id, period, legal_entity_ids, task_count | notifications |
| dev.passage.close.task.completed.v1 | task completed/skipped | run_id, task_id, status, actor, evidence_uri, late (bool) | board, notifications, reporting |
| dev.passage.ledger.allocation.completed.v1 | run posted/reversed | run_id, rule_ids, period, iterations, entry_ids, reversal | reporting, close |
| dev.passage.ledger.accrual.scheduled.v1 | scheme applied | source_entry_id, scheme, occurrences[] (dates, amounts) | close board |
| dev.passage.ledger.year-end.closed.v1 | year-end posted | ledger_id, fiscal_year_id, closing_entry_ids, opening_entry_ids, retained_earnings_total | reporting, close |
| dev.passage.ledger.regrouping.completed.v1 | regrouping posted or reversed | run_id, ledger_id, key_date, rule_ids, totals per adjustment account, entry_ids, reversal | reporting, close |

### 2.14.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Ledger calendar / period control | Grid ledger × period × module; set status; bulk close; request reopen | period, module, status, allowed group, changed by, reopen count | Maintain period status |
| Close templates | Build task list, dependencies (DAG view), due offsets, automation | task, module, role, due offset, depends on, evidence, hook | Maintain close templates |
| Close board | Instantiate run, kanban/list across entities, filter overdue, reassign, attach evidence, complete | entity, task, owner, due, status, blocked by, evidence | View / Work close tasks |
| Allocation rules | Define source, method, destinations, basis; simulate | name, method, source filter, destinations, offset, iterative group | Maintain allocation rules |
| Allocation runs | Simulate, review proposal (source → destinations), post, reverse | rule, source amount, destination, basis value, pct, amount | Run allocations |
| Accrual schemes | Define, preview schedule on a sample amount | code, accounts, frequency, occurrences, spread, reverse day | Maintain accrual schemes |
| Year-end close | Select year/ledger, simulate, review retained earnings by dimension, post | account, combination, closing balance, opening balance, retained earnings | Run year-end close |
| Regrouping | Define rules (method, source, buckets, accounts), simulate for a key date, review groups (party / bucket / amount), post, reverse | rule, source account, party, bucket, balance, adjustment account, target account, amount | Maintain regrouping rules / Run regrouping |

Interaction rules: period grid supports keyboard range selection and bulk status change with a confirmation summarizing affected modules; close board tasks open in a side panel without losing filters; blocked tasks show the blocking task inline; allocation proposals support "what-if" edits to percentages that recompute live but require saving the rule to post.

### 2.14.7 Business rules and edge cases

- **BR-14.1 Period-control decision.** For a line with posting_date → period P, module M (from source mapping), user U: status = row(P, M) if present else row(P, ALL) if present else `never_opened`. `open` → allow; `on_hold` → allow iff U ∈ allowed_group else 403 `LEDGER_PERIOD_ON_HOLD`; `closed` or `never_opened` → 422 `LEDGER_PERIOD_CLOSED`. Service accounts for subledgers are members of no on-hold group by default.
- **BR-14.2 Period-control matrix (reference configuration for March close).**

| Module | Day 1–3 after period end | Day 4–5 | After GL close |
|---|---|---|---|
| INV, PROJ, PAYROLL | on_hold (ops group) | closed | closed |
| AP, AR, BANK | open | on_hold (AP/AR supervisors) | closed |
| TAX | open | open | closed after settlement |
| FA | open | closed | closed |
| GL | open | on_hold (controllers) | closed |
| ALL (default for unmapped) | open | on_hold (controllers) | closed |

- **BR-14.3 Period status state machine.** `never_opened → open`; `open → on_hold`; `on_hold → open`; `open|on_hold → closed`; `closed → open` only via reopen workflow (two approvals) which sets `reopen_expires_at`; on expiry a Temporal timer sets `closed` again and emits the event. Transitions are rejected with `CLOSE_INVALID_TRANSITION`.
- **BR-14.4 Allocation rounding.** Destination amounts = round(source × share, scale); residual = source − Σ destinations; residual applied to the destination with the largest share (ties: first by definition order). Basis values of zero across all destinations → skip rule with `ALLOC_ZERO_BASIS` unless `allocate_from_zero` = equally.
- **BR-14.5 Iterative convergence.** Reciprocal groups solved by Gauss–Seidel iteration on the unrounded amounts; stop when max |Δ| < 10^−scale / 2 or at 100 iterations (then fail `ALLOC_NO_CONVERGENCE`); rounding applied once after convergence per rule.
- **BR-14.6 Allocation period lock.** A run posts only if GL is open or on_hold with the runner in the allowed group; the basis period may differ from the posting period (e.g., prior-period headcount) and is snapshotted on the run.
- **BR-14.7 Accrual spread by days.** Amount_i = round(total × days_i / total_days); residual to the last occurrence; occurrences dated the first day of each period (or scheme `post_day`) and reversed the first day of the following period.
- **BR-14.8 Year-end retained earnings per dimension.** When `retain_dimensions`, the retained earnings line keeps the P&L line's dimension combination (replacing only the main account); dimension values inactive on the opening date are remapped to the ledger's `default_closing_combination` and listed in the run report.
- **BR-14.9 Year-end re-run.** A new run for the same (ledger, year) first reverses all entries of `previous_run_id` (dated the same dates), then regenerates; opening period 0 is not subject to period control of the new year's period 1.
- **BR-14.10 Close task dependencies.** A task becomes `in_progress`-eligible only when all `depends_on` tasks are `completed` or `skipped`; skipping requires a reason and duty `Skip close tasks`; automation tasks cannot be completed manually unless marked `manual_override_allowed`.
- **BR-14.11 Concurrency on close.** Closing GL takes `FOR UPDATE` on the PeriodStatus row and waits for in-flight postings (which hold `FOR SHARE`) up to 5 s before failing with `CLOSE_BUSY`; retried by the UI.
- **BR-14.12 Calendar mismatch across ledgers.** Period-control is per ledger; a ledger group posting where any member ledger's period is closed is rejected as a whole with the offending ledger named.
- **BR-14.13 Regrouping computation.** Remaining term = `due_date − key_date` in days; items without `due_date` fall into the shortest bucket and are listed as `no_due_date`; overdue items (negative term) fall into the shortest bucket. For `debit_credit_balance`, the group balance is Σ open items per (party, reconciliation account, splitting dimension values, currency) at the key date; only groups whose sign is opposite the account's normal balance are reclassified, for their full balance. Entries: debit/credit the adjustment account and the target account in accounting and reporting currency using the posted amounts (revalued amounts when a wave 12 revaluation for the key date is posted, so reclassification follows valuation); one entry per rule per run; auto-reversal on key_date + 1 day. A second posted run for the same (ledger, key date, rule) is rejected (`CLOSE_REGROUPING_EXISTS`) until the first is reversed. Rules run in `sequence`; an item is used by at most one rule per run.

### 2.14.8 Acceptance criteria

- **AC-14.1** (FR-14.1, BR-14.1) Given AP is closed for March and GL open, when a vendor-invoice posting (source.system = ap) dated 2026-03-31 is attempted, then 422 `LEDGER_PERIOD_CLOSED`; a manual GL journal on the same date posts.
- **AC-14.2** (FR-14.1) Given every subledger posting path in the contract suite (manual, ap, ar, inv, fa, proj, tax, bank stubs), when the period is closed for ALL, then every path is rejected (table-driven test, 0 exceptions).
- **AC-14.3** (FR-14.2) Given AP is still open, when GL close is requested, then `CLOSE_INVALID_TRANSITION` naming AP.
- **AC-14.4** (FR-14.3) Given a closed period, when a reopen is requested and approved by two distinct users, then status is `open`, `reopen_count = 1`, and after the expiry timer the period is `closed` again (Temporal time-skip).
- **AC-14.5** (FR-14.6, BR-14.4) Given the allocation fixture (rent USD 10,000.01 by headcount 3/5/2), when run, then destinations are 3,000.00, 5,000.01, 2,000.00 and Σ = source.
- **AC-14.6** (FR-14.7) Given two service departments allocating 10% to each other, when run iteratively, then the result converges within 100 iterations and matches the reciprocal workbook to the cent.
- **AC-14.7** (FR-14.9) Given a USD 1,200.00 annual insurance invoice with a 12-occurrence equal scheme, when applied, then 12 entries of 100.00 are scheduled on period starts with reversals on the next period start.
- **AC-14.8** (FR-14.10) Given a 12-period dataset, when the year-end close is posted, then every balance-sheet combination's opening balance equals its closing balance, P&L accounts open at 0.00, and retained earnings change equals net income.
- **AC-14.9** (FR-14.4, FR-14.5) Given a 40-task template across 3 entities with dependencies, when the run completes, then no task was completed before its dependencies and the board shows 120 tasks completed.
- **AC-14.10** (BR-14.11) Given 50 concurrent postings in flight, when GL close is requested, then it either waits ≤ 5 s and succeeds with no posting committed after the close timestamp, or returns `CLOSE_BUSY`.
- **AC-14.11** (FR-14.13, BR-14.13) Given a loan clearing account with open items due 2026-09-30 (USD 40,000.00), 2028-03-31 (USD 120,000.00) and 2032-03-31 (USD 200,000.00) and key date 2026-03-31, when the `remaining_term` rule runs, then USD 40,000.00 stays short-term, USD 320,000.00 is reclassified to long-term in one entry, and the reversal is dated 2026-04-01.
- **AC-14.12** (FR-14.13) Given stub vendor open items where vendor V7 nets to a debit of USD 1,250.00 and customer C3 nets to a credit of USD 800.00, when the `debit_credit_balance` rules run, then USD 1,250.00 moves from payables to "receivables – vendors with debit balance" and USD 800.00 from receivables to "payables – customers with credit balance"; a second post for the same key date returns `CLOSE_REGROUPING_EXISTS`.

### 2.14.9 Test plan and fixtures

- Fixtures: `period-matrix-demo` (3 ledgers × 16 periods × 10 modules); `close-template-40` (40 tasks, 3 entities, 12 dependencies, 6 automation hooks); `alloc-fixtures-25` (fixed, weight, equal, basis, 3 reciprocal groups; XLSX expected); `accrual-fixtures-10`; `fy2025-12-periods` (12 posted periods, 400 combinations, expected opening balances).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Period decision table | unit | BR-14.1 | 60 cases (status × module × group) |
| Subledger path contract | contract | all posting clients' stubs | closed period rejected on every path |
| Close race | integration | ledger + close | no post commits after close timestamp |
| Reopen workflow | Temporal test | close service | two approvals, expiry auto-close |
| Allocation workbook | integration | allocation engine | totals equal, residual rule honored |
| Reciprocal convergence property | property | iterative solver | converges for random 2–6 department matrices with shares < 50% |
| Year-end equivalence | integration | year-end job | opening = closing per combination |
| Close board E2E | Playwright + axe | React | 40-task run completes; WCAG AA |

Reconciliation: Σ allocation destinations = Σ sources per run; Σ accrual occurrences = source amount; opening balances per combination = closing; retained earnings Δ = net income per ledger.

### 2.14.10 Deployment and operations

Helm charts: `passage-close` (TypeScript service, 2–4 pods, CloudNativePG `close-db`, Temporal namespace `close`), `passage-ledger` upgraded (period status, allocation, accrual, year-end). Config: `CLOSE_REOPEN_DEFAULT_DAYS=3`, `CLOSE_LOCK_WAIT_MS=5000`, `ALLOC_MAX_ITERATIONS=100`, `ALLOC_CONVERGENCE_FRACTION=0.5`. Migrations: `period_status` seeded from fiscal calendars (`never_opened`), existing demo periods set `open`. Feature flags: `close.reopen_workflow` (always on in prod), `ledger.iterative_allocations`. Scheduled jobs: accrual and recurring journal generators (Temporal schedules), reopen expiry timers, close reminder notifications (daily 08:00 entity time zone). SLOs: period decision adds < 2 ms to posting p99; close board p95 < 500 ms for 1,000 tasks. Alerts: reopen without expiry (P2), posting rejected rate for closed periods > 50/h (P3, indicates a subledger misconfiguration), allocation non-convergence (P3). Runbook: emergency reopen, stuck close lock, re-running year-end. Rollback: digest rollback; period status rows are retained; disabling the flag never bypasses period control.

### 2.14.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX `LedgerPeriod`, `LedgerPeriodModuleAccessControl`, `LedgerFiscalCalendarPeriod` | PeriodStatus | statuses copied; modules mapped (Ledger→GL, VendInvoice→AP, CustInvoice→AR, Inventory→INV, Asset→FA, Project→PROJ, Sales tax→TAX, Bank→BANK) |
| AX `LedgerAllocation`, `LedgerAllocationRule*` (basis/destination/source) | AllocationRule | basis "statistical" → basis spec on statistical accounts |
| AX `LedgerAccrualScheme` | AccrualScheme | |
| AX financial period close templates/tasks (`FinancialPeriodCloseTemplate*`) | CloseTemplate, template tasks | dependencies and due offsets preserved |
| SAP `T001B` (OB52) | PeriodStatus | account type + → ALL, D → AR, K → AP, A → FA, M → INV, S → GL; authorization group → allowed_group |
| SAP KSV5/KSU5 cycles (`T811C`, `T811S`, `T811K`) | AllocationRule | segments → rules; tracing factor → basis spec |
| SAP `T030` retained earnings (OB53) | Ledger.retained_earnings_combination_id | per ledger |
| SAP closing cockpit templates | CloseTemplate | |
| SAP FAGLF101 sort methods and adjustment accounts (`T030` transactions PYO, PYU, PYT; `T043`), FAGL_CL_RGRP settings | RegroupingRule | sort method buckets → buckets; adjustment/target accounts per reconciliation account; prior regrouping runs not migrated (they auto-reverse) |

Validation: period statuses per module equal the source at cutover; allocation rules re-executed on the last closed period in simulate mode and compared to source postings (tolerance 0.01 per destination). Not migrated: completed close task history, prior allocation run history, SAP accrual engine objects (re-keyed as schemes).

### 2.14.12 Dependencies, risks, open questions

Depends on: wave 11 (posting API hook, reversal), wave 12 (revaluation hook on checklist), wave 13 (settlement hook), wave 4 (Temporal human tasks and timers), wave 6 (fiscal calendars with adjustment periods), wave 2 (user groups).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Subledger services (future waves) bypass module mapping | Medium | High | Source mapping is mandatory; unmapped `source.system` → GL and rejected when GL on hold; contract test suite grows per wave |
| Iterative allocation performance with many dimensions | Low | Medium | Solver limited to 50 rules per group; measured in fixtures |
| Year-end with millions of combinations | Medium | Medium | Chunked generation (50k lines per entry, multiple entries per run); run measured on 1M-combination synthetic set |

Open questions: (1) Should on_hold allow service accounts of the owning module by default? Default no; decide by wave 14 mid-point. (2) Retained earnings per dimension as default? Default yes for the demo; decide with accountant review by wave 14 exit. (3) Do adjustment periods 13–16 get their own close checklist? Default: same run, tasks flagged `adjustment_only`.

### 2.14.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `period_control.module_mapping` | tenant | jsonb | ledger→GL, ap→AP, ar→AR, inventory→INV, fa→FA, project→PROJ, tax→TAX, bank→BANK, payroll→PAYROLL | source.system → module | BR-14.1 module resolution | Ledger calendar > Module access (`LedgerPeriodModuleAccessControl`) | OB52 account types (+, A, D, K, M, S, V) |
| `period_control.on_hold_group_id` | ledger × period × module | ref | none | user groups (wave 2) | Who may post while on hold | Ledger calendar > Access level / user group | OB52 authorization group (`BRGRU`) second interval |
| `close.reopen_default_days` | tenant | int | 3 | 1–30 | Reopen expiry (FR-14.3) | — | — |
| `close.reopen_approvers` | tenant | int | 2 | 2–3 | Distinct approvers for reopen | — | — |
| `close.lock_wait_ms` | tenant | int | 5,000 | 1,000–30,000 | BR-14.11 wait | — | — |
| `close.gl_closes_last` | ledger | bool | true | true (fixed in prod) | FR-14.2 ordering | — | — |
| `close.block_on_pending_batches` | ledger | bool | true | true / false | `CLOSE_PENDING_BATCHES` on close | — | — |
| `close.template.due_offset` / `business_calendar_id` | template task | int / ref | 0 / legal entity calendar | −10 to +30 business days | Due date computation | Financial period close > Templates > Due day | Closing cockpit task offset (CLOCOC) |
| `allocation.max_iterations` / `convergence_fraction` | tenant | int / numeric | 100 / 0.5 | 10–1,000 / 0.1–1 | BR-14.5 | Allocation rules (no iterative) | KSV5/KSU5 iterative cycle flag |
| `allocation.allocate_from_zero` | rule | enum | skip | skip / equally | BR-14.4 zero basis | Allocation rule > "Allocate from zero" | Cycle segment "zero tracing factors" |
| `allocation.auto_reverse_next_period` | rule | bool | false | true / false | FR-14.8 | — | Cycle "reverse" via KSV6/KSU6 |
| `accrual.reverse_day` / `post_day` | scheme | int | 1 / 1 | 1–28 | BR-14.7 | Accrual scheme > Reversal / Period key | FBS1 reversal date; ACE reversal settings |
| `year_end.retained_earnings_combination_id` | ledger | ref | required | equity account combination | FR-14.10 | Ledger > Year-end close > Retained earnings account | OB53 retained earnings account (`T030` BIL) |
| `year_end.retain_dimensions` | ledger | bool | true | true / false | BR-14.8 | Year-end close template > "Transfer dimensions" (D365) | FAGLGVTR carry-forward of characteristics (`T881` fields) |
| `year_end.default_closing_combination_id` | ledger | ref | required when retain_dimensions | combination | Remap of inactive values | Year-end close template > Financial dimension defaults | — |
| `year_end.chunk_lines` | tenant | int | 50,000 | 10,000–100,000 | Entry size in year-end runs | — | — |
| `regrouping.buckets` default | ledger | jsonb | ≤ 365 d, 366–1,825 d, > 1,825 d | ordered day ranges | BR-14.13 | — | FAGLF101 sort method (`T030` PYO/PYU) |
| `regrouping.auto_reverse` | ledger | bool | true | true (fixed) | Reversal on key_date + 1 | — | FAGLF101 reversal posting date |
| `close.reminder_time` | legal entity | time | 08:00 | local time | Close reminders | — | — |

### 2.14.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Period-control matrix | list inquiry | ledger, fiscal year, module | period, module, status, on-hold group, changed by/at, reopen count | `PeriodStatus` | Ledger calendar form | OB52 view (`T001B`), OKP1 (CO period lock), MMPV log |
| Reopen history | operational report | ledger, year, requester, approver | period, module, reason, requester, approvers, opened/closed at, expiry | `PeriodStatus` audit | Database log on `LedgerPeriod` | `T001B` change documents (SCU3) |
| Close board / task list | list inquiry | run, entity, status, owner, overdue | task, entity, owner, due, status, blocked by, evidence, late | `CloseTask` | Financial period close workspace (Tasks, Calendar) | CLOCOS monitor, Fiori Manage Closing Tasks |
| Close duration KPI | KPI | entity, period range | business days period end → GL closed, late tasks %, reopen count | `CloseRun`, `PeriodStatus` | Financial period close workspace tiles | Advanced Financial Closing analytics |
| Allocation run results | drill-down | run, rule | source amount, destination, basis value, share, amount, iteration count | `AllocationRun` | Allocation journal (review before post); SSRS Allocation rule report | KSV5/KSU5 detail list, KSB5 |
| Allocation rule listing | operational report | ledger, method | rule, source filter, destinations, basis, offset, iterative group | `AllocationRule` | SSRS Allocation rule report | KSV3/KSU3 cycle display |
| Accrual schedule | list inquiry | scheme, source entry, date range | occurrence, date, amount, posted, reversal | `AccrualScheme` schedules | Ledger accruals inquiry | ACACPSITEMS, F.81 list |
| Recurring journal next runs | list inquiry | template, owner | next run, last run, generated batches, paused | `RecurringJournal` | Periodic journals list | F.15 list recurring entries |
| Year-end close results | operational report | ledger, fiscal year | retained earnings by dimension, opening per combination, remapped combinations | `YearEndCloseRun` | SSRS Fiscal year close report / Opening balances | FAGLGVTR log, 2KET, F.07 log |
| Regrouping proposal / posting list | drill-down | run, rule, key date | party / item, bucket, balance, adjustment account, target account, amount | `RegroupingRun.snapshot` | — | FAGLF101 sorted list and posting log |
| Rejected postings in closed periods | KPI | ledger, period, source system | count, amount, first/last attempt | posting rejection log | — | — |

### 2.14.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Close pack (checklist evidence) | CloseRun completed | header: entities, period, template; lines: task, owner, completed at, evidence links, skipped reasons; footer: GL close time, approvers | PDF, XLSX | close run id | audit evidence; 10 years |
| Period reopen approval record | reopen approved | ledger, period, module, reason, requester, approvers, timestamps, expiry | PDF, JSON | — | audit evidence; 10 years |
| Allocation posting list | allocation run posted | rules, basis snapshot, destination amounts, vouchers | PDF, XLSX | run id; ledger vouchers | financial record; 10 years |
| Year-end closing report | year-end run posted | retained earnings per dimension, opening entries summary, remapped combinations, vouchers | PDF, XLSX | run id | financial record; permanent for the fiscal year file |
| Regrouping posting list | regrouping run posted | rule, buckets, groups, adjustment amounts, vouchers, reversal date | PDF, XLSX | run id | supporting record for balance sheet presentation; 10 years |

No external electronic documents in this wave.

### 2.14.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `accrual-generator` | daily 00:30 entity time zone | scheme × occurrence | occurrence keyed (scheme, source entry, n) | 10k occurrences < 2 min | occurrence blocked if period closed → close task | blocked > 0 at day 3 → P3 |
| `recurring-journal-generate` (scheduling per FR-14.11) | per template | template | (template, scheduled date) | < 5 min for 1,000 templates | per template | — |
| `reopen-expiry` (Temporal timer) | at `reopen_expires_at` | period status row | transition idempotent | < 1 s | retry; alert | timer missing → P2 |
| `close-reminders` | daily `close.reminder_time` | legal entity | one notification per (task, day) | < 1 min | — | — |
| `allocation-run` (close hook) | per checklist | ledger × rule group | (ledger, period, rule ids) key | 1,000 rules < 5 min | run failed; task blocked | non-convergence → P3 |
| `year-end-close` | on demand / checklist | ledger | (ledger, year, run id); chunks of 50k lines | 1M combinations < 20 min | failed chunk rolls back run | failure → P2 |
| `regrouping-run` (close hook) | key date = period end, day +2 | ledger × rule | (ledger, key date, rule set) | 1M open items < 10 min | run failed; task blocked | failure → P2 |
| `regrouping-reverse` | key date + 1 day 00:15 | run | reversal set once | < 1 min | retry hourly | missing by 12:00 → P2 |
| `period-status-cache-audit` | every 5 min | posting service | compares cached status vs table | < 1 s | cache refresh | mismatch → P2 |

### 2.14.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| View period status | GET periods matrix | — | All finance roles |
| Maintain period status | PATCH periods/{p}/modules/{m}; bulk close | Approve period reopen; Post ledger journals in on-hold group | Financial controller |
| Request period reopen | POST :request-reopen | Approve period reopen (same request) | Accounting supervisor, Financial controller |
| Approve period reopen | Temporal approval task | Request period reopen (same request); Maintain period status | CFO, Group controller |
| Maintain close templates | close templates CRUD | — | Financial controller, Close manager |
| Run period close | POST /close/runs | — | Close manager |
| Work close tasks / View close board | task actions; board | Skip close tasks (own tasks) | All finance roles |
| Skip close tasks | :skip | Work close tasks (same task) | Close manager |
| Maintain allocation rules | allocation rules CRUD | Run allocations | Cost accountant (senior) |
| Run allocations | POST /ledger/allocations, :post, :reverse | Maintain allocation rules | Cost accountant |
| Maintain accrual schemes | accrual schemes CRUD | Approve ledger journals | Accounting manager |
| Run year-end close | POST /ledger/year-end-closes | Maintain period status | Financial controller |
| Maintain regrouping rules | regrouping rules CRUD | Run regrouping | Accounting manager |
| Run regrouping | POST /ledger/regroupings, :post, :reverse | Maintain regrouping rules | Financial controller, Accounting supervisor |

### 2.14.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| CLOSE-0001 `CLOSE_INVALID_TRANSITION` | error | Cannot change {module} {period} from {from} to {to}: {reason}. | BR-14.3, FR-14.2 | Close dependent modules first or use the reopen workflow | FR-14.2 |
| CLOSE-0002 `CLOSE_PENDING_BATCHES` | error | {count} approved journal batches dated in {period} are not posted. | Close with unposted batches | Post or cancel them, or force with duty | FR-14.2 |
| CLOSE-0003 `CLOSE_BUSY` | error | Postings are in progress for {ledger}; retry the close. | BR-14.11 lock wait | Retry | BR-14.11 |
| CLOSE-0004 `CLOSE_REOPEN_REASON_REQUIRED` | error | A reason of at least 10 characters is required. | FR-14.3 | Enter a reason | FR-14.3 |
| CLOSE-0005 `CLOSE_REOPEN_APPROVER_INVALID` | error | Approver {user} is the requester or already approved. | FR-14.3 | Choose a distinct approver | FR-14.3 |
| CLOSE-0006 `CLOSE_REOPEN_EXPIRED` | info | Period {period} was reclosed automatically after the reopen window. | Expiry timer | Request a new reopen if needed | BR-14.3 |
| CLOSE-0007 `CLOSE_TEMPLATE_CYCLE` | error | Task dependencies contain a cycle: {path}. | FR-14.4 | Remove a dependency | FR-14.4 |
| CLOSE-0008 `CLOSE_TASK_BLOCKED` | error | Task {task} is blocked by {blocking}. | BR-14.10 | Complete or skip the blocking tasks | BR-14.10 |
| CLOSE-0009 `CLOSE_TASK_AUTOMATED` | error | Task {task} completes automatically when {job} finishes. | BR-14.10 | Run the job, or allow manual override | BR-14.10 |
| CLOSE-0010 `ALLOC_PCT_NOT_100` | error | Destination percentages total {sum}; they must total 100.000000. | FR-14.6 | Adjust percentages | FR-14.6 |
| CLOSE-0011 `ALLOC_ZERO_BASIS` | warning | Rule {rule} skipped: basis is zero for all destinations. | BR-14.4 | Check basis data or set allocate_from_zero | BR-14.4 |
| CLOSE-0012 `ALLOC_NO_CONVERGENCE` | error | Iterative group {group} did not converge in {n} iterations. | BR-14.5 | Review reciprocal shares (≥ 50% loops) | FR-14.7 |
| CLOSE-0013 `YEAR_END_PERIODS_OPEN` | error | Year-end close needs all periods of {year} closed; open: {list}. | FR-14.10 | Close the listed periods | FR-14.10 |
| CLOSE-0014 `YEAR_END_RE_ACCOUNT_INVALID` | error | Retained earnings account {account} must be an equity account. | FR-14.10 | Fix the ledger setting | FR-14.10 |
| CLOSE-0015 `YEAR_END_DIMENSION_REMAPPED` | warning | {count} combinations had inactive values and were remapped to {default}. | BR-14.8 | Review the run report | BR-14.8 |
| CLOSE-0016 `CLOSE_REGROUPING_EXISTS` | error | A posted regrouping exists for {ledger} {key_date} rule {rule}. | BR-14.13 | Reverse it first | FR-14.13 |
| CLOSE-0017 `CLOSE_REGROUPING_SOURCE_UNAVAILABLE` | error | Open-item source {source} is not registered for {ledger}. | AP/AR wave not live | Use GL sources only or wait for the source | FR-14.13 |
| CLOSE-0018 `CLOSE_REGROUPING_NO_DUE_DATE` | warning | {count} items without due date were placed in the shortest bucket. | BR-14.13 | Maintain due dates on the source documents | BR-14.13 |

### 2.14.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Period decision in posting | every posting line batch | +0.3 / +1 / +2 ms | at 5,000 lines/s | — | `period_status` PK cached per pod, invalidated by event | cache miss → PK read |
| Period transition (close GL) | 3 entities × 10 modules × 16 periods per year | 50 / 500 / 5,000 ms (lock wait) | — | 480 rows/year/ledger | PK | `CLOSE_BUSY` returned after 5 s |
| Close board load | 1,000 tasks | 80 / 300 / 500 ms | 50 req/s | 1,000 tasks × 12 runs × 1 KB = 12 MB | (run_id, status), (assignee, due_at) | page size reduced |
| Allocation run | 1,000 rules, 50k destinations | — | < 5 min | basis snapshots 10 MB/run | balance PK queries per rule | runs queued per ledger |
| Iterative allocation group | ≤ 50 rules | — | < 60 s | — | in-memory solver | fail at 100 iterations |
| Accrual generation | 10k occurrences/day | — | < 2 min | — | (scheme, date) | — |
| Year-end close | 1M combinations | — | < 20 min | 2M lines/year/ledger | chunked entries of 50k lines | chunk size reduced, run longer |
| Regrouping | 1M open items | — | < 10 min | 2 entries per rule per period | `open_item` (account, status, due_date) | per-rule timeout 20 min |

### 2.14.20 Localization and compliance hooks

- Fiscal calendars vary (JP/IN April–March, AU July–June, 4-4-5 retail calendars, 13-period calendars) — handled by wave 6 calendars; period control is per ledger so local-GAAP ledgers can follow a different year end.
- Year-end: some jurisdictions require closing to the profit-and-loss summary account then to retained earnings (FR, ES PGC class 129 "Resultado del ejercicio"; IT "Utile d'esercizio") and separate opening/closing balance sheets (DE "Eröffnungsbilanzkonto"/"Schlussbilanzkonto" 9000/9008); country packs configure an intermediate result account and closing/opening balance sheet accounts as year-end template steps.
- Regrouping: remaining-term disclosure buckets vary — HGB §268(4)/(5) "Restlaufzeit bis 1 Jahr / mehr als 5 Jahre", IFRS current/non-current (IAS 1.66–1.76: 12 months), US GAAP current maturities; country packs ship bucket sets and adjustment account sets; debit-balance vendor reclassification is mandatory under HGB and common under IFRS.
- Statutory locking: DE GoBD requires "Festschreibung" (posted entries immutable within the period of the VAT return) — satisfied by append-only journal plus period close; FR FEC closes the year with a "clôture" that is irreversible — country pack sets `reopen_approvers = 3` and disallows reopen after the statutory deadline.
- Retention of close evidence: 10 years (DE/FR), 7 (US SOX workpapers per PCAOB AS 1215).
- SOX: reopen dual approval, close pack evidence and SoD duties above are the SOX 404 control hooks consumed by wave 48 controls monitoring.
- Extension points: `close.template_pack` (country/industry close templates), `ledger.year_end_step` (intermediate result account, closing/opening BS accounts), `ledger.regrouping_bucket_pack`.

### 2.14.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| SAP §13.1 FI-GL Periodic processing — FAGLF101 / F101 Regroup receivables/payables (sorted list, reclassification by remaining term) (SAP register row 8) | GAP (propose 14) | ADDED as FR-14.13 | `remaining_term` and `debit_credit_balance` methods; AC-14.11 |
| SAP §13.1 FI-GL Periodic processing — FAGL_CL_REGROUP / FAGL_CL_MIGRATION Closing: regrouping (S/4) (SAP register row 10) | GAP (propose 14) | ADDED as FR-14.13 | same engine; migration of FAGL_CL settings in §2.14.11 |
| SAP §13.1 FI-GL Customizing — FAGL_CL_RGRP / Define sort method & adjustment accounts for regrouping (`T043`, `T030`) (SAP register row 21) | GAP (propose 14) | ADDED as FR-14.13 | `RegroupingRule` buckets and adjustment/target accounts; parameters in §2.14.13 |
| SAP §13.2 FI-AP Periodic processing — FAGLF101 (AP) Regroup vendors with debit balances (SAP register row 30) | GAP (propose 14) | ADDED as FR-14.13 | rule ships in this wave; vendor item source activates when wave 19 registers `open_item_source = vendor` (AC-14.12 uses stub items) |
| SAP §13.3 FI-AR Periodic processing — FAGLF101 (AR) Regroup customers with credit balances (SAP register row 42) | GAP (propose 14) | ADDED as FR-14.13 | customer item source activates with wave 30 |

## 2.15 Wave 15: Financial Reporting, Statements & Audit Trail

### 2.15.1 Scope and business context

AX ships Management Reporter (row, column, reporting tree definitions over dimensions) plus trial balance and ledger transaction inquiries; SAP uses financial statement versions (OB58, `T011`/`T011T`, `FAGL_011*`), Fiori financial statement apps and document-flow drill-down. Wave 15 builds the `reporting` service (Go, with its own Postgres read model fed by ledger events) with a row/column/tree report designer, standard statements (trial balance, balance sheet, P&L, cash flow indirect, comparatives, by-entity consolidated view), drill-down to the journal line and source document, scheduled distribution with row-level security on dimensions, and the audit trail report. This is the Phase 2 exit wave: finance stakeholders sign off a closed period produced entirely from Passage.

Out of scope: group consolidation with eliminations and translation (wave 40), budgets (wave 38), lakehouse analytics and dashboards (wave 46), XBRL/iXBRL (wave 48).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Management Reporter reads from its own data mart refreshed by an integration service; statements lag the GL by 15–60 min and the mart silently breaks after AX upgrades or dimension changes | Separate ETL (MR integration) with no "as of" stamping | Read model fed by `journal.posted` events with per-ledger HWM; every statement shows the sequence it reflects; nightly zero-diff verify | Statement lag p95 < 10 s (vs 15–60 min); "mart out of sync" incidents → 0 |
| SAP F.01/RFBILA00 and FSV (OB58) drill to FAGLL03 but lose the FSV context; reaching the source document from a balance-sheet line is 7+ screens and often impossible for migrated items | Hierarchy, totals and line items are separate programs without a shared key | Drill-down contract (BR-15.4): cell → balance keys → lines → `source_uri` → trace id, with sums that must equal the cell | Clicks balance → document 7 → 3; drill sum mismatches → 0 (property-tested) |
| Report designer in MR requires a Windows client, separate security (tree security) and definitions that cannot be versioned or promoted between environments except by manual export | Desktop tooling, no API, no versioning | Web designer, JSON export/import with schema version, immutable published versions, OPA dimension security shared with the rest of Passage | Definition promotion dev → prod 1 h manual → < 1 min API call; security rule sets 2 → 1 |
| Cash flow statement is hand-built in Excel from the trial balance because MR/FSV cash flow setups are fragile and do not reconcile | No tagging model linking balance movements to cash flow categories with an enforced check | `cash_flow_tag` on rows, `CF-STD` indirect derivation with `CF_UNRECONCILED` failure and a "not classified" check row | Cash flow preparation 1–2 days/period → generated in < 20 s with 0.00 reconciliation enforced |
| Audit requests ("who posted, who approved, when was the period reopened") require pulling `SysDatabaseLog` / `CDHDR` extracts and workflow history separately, usually by IT | Audit data spread across logs with no finance-facing report | Audit trail report over CDC audit + ledger events with hashed export | Audit evidence turnaround days → minutes; controller self-service, 0 IT tickets |

### 2.15.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Report definitions | Management Reporter row/column/tree definitions, report definitions | Financial statement version (OB58, `T011`, `FAGL_011PC/ZC`), Report Painter/Writer | `reporting.ReportDefinition` (rows, columns, tree) | full |
| Row definitions | MR row: description, link to financial dimensions, format codes (TOT, CAL), normal balance | FSV nodes with account intervals, debit/credit split, net result | `ReportRow` with kind ∈ {account_range, dimension_filter, total, calculation, header} | full |
| Column definitions | MR column: FD (period), CALC, comparative, YTD, budget | Period/comparison columns in F.01 / Fiori | `ReportColumn` with period expressions (`P`, `P-1`, `YTD`, `PY`, `PYTD`, `CALC`) | full (budget columns in wave 38) |
| Reporting tree | MR tree of reporting units (dimension values) | Profit center hierarchy (KE5X), FSV per company | `ReportTree` over dimension hierarchies (wave 6) | full |
| Trial balance | Trial balance list page, `LedgerTrialBalanceListPage` | FAGLB03, S_ALR_87012277, F.08 | Trial balance statement (standard definition) | full |
| Balance sheet / P&L | MR default reports | F.01 (RFBILA00), Fiori Balance Sheet / P&L | Standard definitions `BS-STD`, `PL-STD` | full |
| Cash flow | MR cash flow (manual definitions) | Cash flow via FSV + S/4 Cash Flow app | `CF-STD` indirect with cash-account tagging and non-cash adjustments | full |
| Drill-down | MR drill to account/transactions; ledger transaction inquiry; voucher transactions | Document flow (FB03), FAGLL03 line items, drill from Fiori statements | Statement cell → balance keys → lines → source document URI → trace id | improved: trace id |
| Distribution | MR report library, scheduled generation, email | Report scheduling (SM36), SAP Analytics Cloud publication | `StatementRun` schedules with PDF/XLSX/CSV, email, link | full |
| Security | MR tree security, AX dimension security | Authorization objects (F_BKPF_BUK, K_CSKS), FSV authorization | OPA dimension-value policies applied to every balance query | full |
| Audit trail | Database log, voucher audit, workflow history | Change documents (CDHDR/CDPOS), FB03 header changes, SE16 on BKPF | Audit trail report over CDC audit (wave 5) + ledger events | full |
| Main account categories and derived hierarchies | Main account categories (`MainAccountCategory`), D365 derived financial hierarchies (`DimensionHierarchy` type Derived), Financial dimension sets | Account groups / G/L account hierarchies (KDH1–KDH3, `SETHEADER`/`SETNODE`), FSV nodes, profit center groups (KCH1) | `reporting.DerivedHierarchy` over main account categories and dimension values, effective-dated versions, usable as row sets and trees (FR-15.13) | full |
| P&L by function of expense (cost of sales accounting) | Custom MR rows by dimension | FSV with functional area (`RFAREA`), S_ALR_87012284 with FSV for cost of sales | Standard definition `PL-FUNC` over `FUNC_AREA` (FR-15.14) | full |

### 2.15.3 Functional requirements

- **FR-15.1 Reporting read model** — Must. The system shall maintain in `reporting-db` a `ReportingBalance` fact (ledger × account × dimension combination × period × currency type) built from `ledger.journal.posted/reversed/balance.rebuilt/year-end.closed` events, with the covered `posting_sequence` high-water mark per ledger.
  - Lag SLO: p95 < 10 s after posting; every statement records the HWM it reflects and shows "as of sequence N / time T".
  - A nightly comparison against `ledger.Balance` must show zero differences.
- **FR-15.2 Report designer: rows** — Must. Row definitions contain ordered rows of kind header, account_range (account from/to, optional dimension filter), dimension_filter (any dimension values with wildcards), total (sum of row references), calculation (expression over row references with + − × ÷ and constants), with format (bold, indent, sign flip for normal credit balance, print zero, page break).
  - Maximum 2,000 rows per definition; expressions validated on save (unknown references, division by zero handled → blank).
- **FR-15.3 Report designer: columns** — Must. Column definitions specify period expressions relative to the run period (`P`, `P-n`, `YTD`, `PY`, `PYTD`, `Q`, `FY`), currency type (acct/rpt/txn with currency filter), ledger, dimension filter, variance/percent calculations between columns, and formatting.
  - Maximum 60 columns; comparative column sets (actual vs prior year) are templates.
- **FR-15.4 Reporting trees** — Must. Trees list reporting units (dimension value or hierarchy node from wave 6 hierarchies) with roll-up; a statement run produces one report per unit plus the roll-up, honoring OPA dimension security per viewer.
- **FR-15.5 Standard statements** — Must. The system shall ship definitions `TB-STD` (trial balance: opening, debit, credit, closing, net), `BS-STD`, `PL-STD`, `CF-STD` (indirect method: net income + non-cash adjustments + working-capital changes derived from balance movements of tagged accounts + investing + financing = change in cash), `TB-DIM` (trial balance by dimension), each parameterized by ledger, period, currency type, and tree.
  - Statements mark `is_closing_entry` / `is_opening_entry` handling: P&L excludes closing entries; BS includes opening entries.
  - Consolidated-by-entity view: side-by-side columns per legal entity and a simple sum column in reporting currency (no eliminations; wave 40).
- **FR-15.6 Statement runs** — Must. A run evaluates a definition for parameters and stores `StatementCell` values with the balance keys used, the HWM, duration, and a SHA-256 of the result; outputs rendered as HTML (interactive), PDF, XLSX and CSV.
  - 2,000-row × 12-column statement over 200k balances completes in < 20 s.
- **FR-15.7 Drill-down** — Must. From any cell: (1) balances view listing the (account, combination, period) keys with amounts summing to the cell; (2) journal lines for a selected key or the whole cell, paginated, with `voucher`, date, D/C, amounts, source; (3) source document link (`source_uri` resolved to a Passage route or external URL) and `trace_id` link to Tempo.
  - Drill honors the viewer's dimension security; the line sum shown equals the cell value exactly or the UI displays a reconciliation warning with the filtered-out amount (never silently).
- **FR-15.8 Scheduling and distribution** — Must. Definitions can be scheduled (cron or close-checklist hook) to run per entity set and distribute via email (PDF/XLSX attachments ≤ 20 MB, else link), shared link with expiry, and a report library with retention (default 7 years for statements run against closed periods, 90 days for ad-hoc).
- **FR-15.9 Row-level security on dimensions** — Must. OPA policies grant dimension values (e.g., cost centers) per user; balance queries, statements, drill-down and exports are filtered server-side; a report run by a scheduler uses the recipient's permissions per rendered copy.
- **FR-15.10 Audit trail report** — Must. The system shall report for a period and ledger: entries posted (who, when, source, idempotency replay flag), batch approvals (submitter, approvers, timestamps), reversals, period status changes including reopen approvals, rate overrides, tax fallbacks, allocation and year-end runs; exportable to CSV/PDF with a hash.
- **FR-15.11 Definition import/export** — Must. Definitions round-trip as JSON (schema-versioned) for backup, environment promotion and Management Reporter conversion.
- **FR-15.12 Management Reporter and FSV converters** — Should. The migration framework shall convert MR row/column/tree definitions (from the MR database `ControlRowDetail`, `ControlColumnDetail`, `ControlTreeDetail`) and SAP FSV (`T011`, `FAGL_011PC`, `FAGL_011ZC`) into Passage definitions with a conversion report of unsupported constructs.
- **FR-15.13 Derived financial hierarchies** — Must. The system shall maintain derived hierarchies: trees whose leaf assignment is derived by rules rather than listed, over (a) main account categories (each main account carries `category_id`; categories nest under hierarchy nodes) and (b) dimension values (nodes with membership rules: value ranges, wildcards, attribute filters such as `cost_center.region = EMEA`), with effective-dated versions.
  - A derived hierarchy can be used as a row set (one row per node, totals rolled up, leaves expanded on demand) and as a reporting tree (FR-15.4); statements record the hierarchy version used.
  - Validation on publish: every postable account (or every active dimension value) maps to exactly one leaf node, or is listed in an "unassigned" check node that must be empty for `strict = true` hierarchies (`REPORT_HIERARCHY_UNASSIGNED`); overlaps are rejected (`REPORT_HIERARCHY_OVERLAP`).
  - Membership is re-evaluated when accounts or dimension values change (`ledger.account.changed`, dimension events); a new value that matches no rule appears in the unassigned node within 60 s and raises a notification to the hierarchy owner.
  - Up to 12 levels and 20,000 nodes per hierarchy; JSON import/export like FR-15.11.
- **FR-15.14 P&L by function of expense** — Should. The system shall ship `PL-FUNC` (revenue, cost of sales, distribution costs, administrative expenses, other operating income/expense, financial result) whose expense rows filter on the `FUNC_AREA` dimension (FR-11.22) instead of account ranges, with a check row that lists P&L lines without `FUNC_AREA` and must be 0.00.

### 2.15.4 Data model

**ReportDefinition**, **ReportRow**, **ReportColumn**

| Field | Type | Required | Notes |
|---|---|---|---|
| definition: id | uuid | yes | PK |
| definition: code / name / version | text(30) / text(120) / int | yes | unique (tenant, code, version) |
| definition: kind | enum | yes | trial_balance, balance_sheet, income_statement, cash_flow, custom |
| definition: row_set_id / column_set_id / tree_id | uuid | yes/yes/no | |
| definition: default_ledger_id / currency_type | uuid / enum | yes | |
| definition: status | enum | yes | draft, published, retired |
| definition: schema_version | text | yes | for JSON import/export |
| row: id, row_set_id, sequence | uuid, uuid, int | yes | |
| row: kind | enum | yes | header, account_range, dimension_filter, total, calculation |
| row: label | text(200) | yes | |
| row: account_from / account_to | text(20) | conditional | |
| row: dimension_filter | jsonb | no | {attr: [values or ranges or *]} |
| row: references / expression | uuid[] / text | conditional | for total / calculation |
| row: normal_balance | enum | yes | debit, credit (sign presentation) |
| row: format | jsonb | no | bold, indent, print_zero, page_break, underline |
| row: cash_flow_tag | enum | no | operating_adjustment, working_capital, investing, financing, cash |
| column: id, column_set_id, sequence, label | | yes | |
| column: period_expr | text | yes | `P`, `P-1`, `YTD`, `PY`, `PYTD`, `Q`, `FY`, `CALC` |
| column: currency_type / currency | enum / char(3) | yes/no | |
| column: ledger_code_override | text | no | compare ledgers side by side |
| column: dimension_filter | jsonb | no | |
| column: calc_expression | text | no | `C2-C1`, `(C2-C1)/C1` |
| column: format | jsonb | no | decimals, percent, width |

**StatementRun** and **StatementCell**

| Field | Type | Required | Notes |
|---|---|---|---|
| run: id | uuid | yes | PK |
| run: definition_id / version | uuid / int | yes | |
| run: parameters | jsonb | yes | ledger(s), period, tree unit, currency type, entities |
| run: hwm_by_ledger | jsonb | yes | {ledger_id: posting_sequence} |
| run: status | enum | yes | queued, running, completed, failed |
| run: started_at / completed_at / duration_ms | | yes | |
| run: result_hash | bytea(32) | yes | SHA-256 over cells |
| run: outputs | jsonb | yes | [{format, uri, size}] |
| run: requested_by / security_principal | uuid | yes | whose dimension security applied |
| cell: run_id, row_id, column_id | | yes | PK |
| cell: value | numeric(23,6) | yes | |
| cell: balance_keys | jsonb | yes | [{ledger, account, combination, period, currency_type}] (compressed; up to 50k keys, overflow stored as a query spec) |
| cell: line_count | int | yes | |

**DerivedHierarchy** and **HierarchyNode** (FR-15.13)

| Field | Type | Required | Notes |
|---|---|---|---|
| hierarchy: id / code / name | uuid / text(30) / text(120) | yes | unique (tenant, code, version) |
| hierarchy: subject | enum | yes | main_account_category, main_account, dimension_value |
| hierarchy: dimension_attribute_id | uuid | conditional | when subject = dimension_value |
| hierarchy: version / effective_from / status | int / date / enum | yes | draft, published, retired |
| hierarchy: strict | bool | yes | unassigned must be empty to publish |
| hierarchy: owner_user_id | uuid | yes | notified on unassigned members |
| node: id / hierarchy_id / parent_id / sequence | uuid | yes | depth ≤ 12 |
| node: label / code | text(120) / text(30) | yes | |
| node: membership_rules | jsonb | no | leaves only: ranges, lists, wildcards, attribute filters, category ids |
| node: normal_balance / cash_flow_tag | enum | no | inherited by generated rows |
| membership (materialized): hierarchy_version, node_id, member_id | | yes | rebuilt on change; PK (hierarchy_version, member_id) |

**MainAccountCategory**: `id`, `code` text(20), `name`, `account_type` (must match member accounts' type), `sequence`; `MainAccount.category_id` FK (wave 11 entity extended).

**AuditTrailEntry** (materialized view over audit store + ledger events): `occurred_at`, `ledger_id`, `kind` (posted, replayed, reversed, batch_submitted, batch_approved, batch_rejected, period_changed, reopen_approved, rate_override, tax_fallback, allocation_run, year_end_run, rebuild), `actor_id`, `object_ref`, `details` jsonb, `trace_id`.

**Invariants**: for every cell, Σ ReportingBalance over `balance_keys` = `value` (property-tested); total rows equal Σ referenced rows; `TB-STD` closing per account = opening + debit − credit; `CF-STD` change in cash = Σ cash-tagged account movements; `result_hash` is reproducible for the same HWM and definition version. Indexing: `reporting_balance` PK (ledger, period, account, combination, currency_type) plus (ledger, account, period); `statement_cell` PK (run_id, row_id, column_id); audit view indexed on (ledger_id, occurred_at).

### 2.15.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| GET/POST/PUT | /api/v1/reporting/definitions[/{code}] and /{code}:publish | Definitions CRUD and publish | PUT idempotent | Maintain report definitions |
| GET/PUT | /api/v1/reporting/row-sets[/{id}], /column-sets[/{id}], /trees[/{id}] | Designer parts | PUT idempotent | Maintain report definitions |
| POST | /api/v1/reporting/definitions/{code}:validate | Validate expressions, references | stateless | Maintain report definitions |
| GET/POST | /api/v1/reporting/definitions/{code}/export, /import | JSON round-trip | import `Idempotency-Key` | Maintain report definitions |
| POST | /api/v1/reporting/runs | Run a statement | `Idempotency-Key` | Run financial statements |
| GET | /api/v1/reporting/runs/{id}[/cells] and /outputs/{format} | Results and files | n/a | View financial statements |
| GET | /api/v1/reporting/runs/{id}/cells/{row}/{col}/balances | Drill level 1 | n/a | View financial statements |
| GET | /api/v1/reporting/runs/{id}/cells/{row}/{col}/lines?cursor= | Drill level 2 (lines) | n/a | View ledger transactions |
| GET | /api/v1/reporting/balances?ledger=&period=&account_from=&…&group_by= | Ad-hoc balance query (secured) | n/a | View ledger balances |
| GET/PUT | /api/v1/reporting/schedules[/{id}] | Distribution schedules | PUT idempotent | Schedule financial statements |
| GET | /api/v1/reporting/audit-trail?ledger=&period=&kind= and /audit-trail:export | Audit trail report | export run id | View audit trail |
| GET | /api/v1/reporting/lag | Read-model lag per ledger | n/a | View financial statements |
| GET/PUT | /api/v1/reporting/hierarchies[/{code}] and /{code}:publish, /{code}/members?version= | Derived hierarchies, publish, materialized membership (FR-15.13) | PUT idempotent | Maintain report definitions |
| GET/PUT | /api/v1/ledger/main-account-categories[/{code}] | Main account categories | PUT idempotent | Maintain chart of accounts |

Queries and validation: `RunStatement` requires a published definition, period exists for every selected ledger, viewer has at least one permitted dimension value when a tree is used, and read-model lag ≤ `max_lag_seconds` (default 30) unless `allow_stale = true` (result flagged). `ImportDefinition` validates schema version and rejects duplicate codes without `overwrite`.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.reporting.statement.generated.v1 | run completed | run_id, definition code/version, parameters, hwm_by_ledger, result_hash, outputs[] | distribution, close (automation hook), audit |
| dev.passage.reporting.statement.failed.v1 | run failed | run_id, error, stage | ops, requester notification |
| dev.passage.reporting.definition.published.v1 | publish | code, version, actor, diff_summary | audit, cache |
| dev.passage.reporting.lag.exceeded.v1 | lag > threshold for 60 s | ledger_id, lag_seconds, hwm_ledger, hwm_reporting | ops alerting, close board |
| dev.passage.reporting.distribution.sent.v1 | email/link delivered | run_id, recipients (hashed), format, size | audit |
| dev.passage.reporting.hierarchy.unassigned.v1 | new account or dimension value matches no leaf | hierarchy code, version, member_id, member kind | hierarchy owner notification, close board |

### 2.15.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Report designer | Create definition, edit rows/columns/tree in split panes, preview with live data, validate, publish, export/import | row label, kind, accounts, dimension filter, expression, format; column label, period expr, currency type | Maintain report definitions |
| Financial statements | Run (ledger, period, tree, currency), view interactive statement, compare runs, export | row, columns, as-of HWM, run status | Run / View financial statements |
| Drill-down panel | Cell → balances → lines → source document, open trace | account, combination, period, amount; voucher, date, D/C, amounts, source link | View ledger transactions |
| Trial balance (standard) | Period range, dimension filters, drill, export | account, opening, debit, credit, closing | View ledger balances |
| Report library | Browse outputs by entity/period/definition, share link, retention | definition, period, entity, generated at, hash | View financial statements |
| Schedules | Define cron/close hook, recipients, formats | definition, parameters, cron, recipients, last run | Schedule financial statements |
| Audit trail | Filter period/ledger/kind/actor, export with hash | occurred at, kind, actor, object, details, trace id | View audit trail |
| Derived hierarchies | Build tree, define membership rules per leaf, view unassigned/overlaps, publish version, use as row set or tree | node, level, rules, members count, unassigned count, version | Maintain report definitions |

Interaction rules: designer previews render within 3 s against the demo dataset and update on each save; row grid supports drag-reorder, multi-select indent and "insert total of selection"; statements are keyboard navigable cell-by-cell (arrow keys) with Enter to drill; drill panels stack as breadcrumbs (Statement › Cell › Balances › Lines › Document); exports preserve number formatting and include an "as of" footer with HWM, definition version and result hash; all screens pass axe WCAG 2.1 AA.

### 2.15.7 Business rules and edge cases

- **BR-15.1 Period expression evaluation.** `P` = run period; `P-n` = n periods earlier in the ledger calendar (crossing fiscal years, adjustment periods skipped unless `include_adjustment_periods`); `YTD` = periods from fiscal year start through P, including adjustment periods ≤ P's year when flagged; `PY` = same period prior year; `Q` = fiscal quarter containing P.
- **BR-15.2 Balance-sheet amounts** are cumulative: Σ ReportingBalance.net from period 0 (opening) through the column's period; P&L amounts are the sum over the column's period range only and exclude `is_closing_entry` lines.
- **BR-15.3 Sign presentation.** Stored net = debit − credit; rows with `normal_balance = credit` display `−net`; calculation rows use displayed values unless the expression is prefixed `raw:`.
- **BR-15.4 Statement drill-down contract.** Level 1 returns balance keys whose Σ equals the cell value exactly (including rounding of the cell to the currency scale — none is applied since balances are already at scale). Level 2 returns lines for one key or the whole cell with keyset pagination (page size 500, max 1,000) ordered by (posting_date, posting_sequence, line_number); a `sum_of_lines` field accompanies every page and the final page's cumulative sum must equal the cell value; when dimension security filters lines, the response includes `filtered_amount` and the UI shows a warning.
- **BR-15.5 Cash flow (indirect) derivation.** Net income = P&L total for the range; operating adjustments = movements of rows tagged `operating_adjustment` (depreciation, provisions); working capital = −Δ of asset rows and +Δ of liability rows tagged `working_capital`; investing and financing = movements of tagged rows; cash = Δ of rows tagged `cash`. The statement fails validation (`CF_UNRECONCILED`) if net cash flow ≠ Δ cash by more than 0.00; untagged balance-sheet accounts appear in a "not classified" check row that must be zero.
- **BR-15.6 Read-model consistency.** A run captures HWM per ledger at start and evaluates only lines with `posting_sequence ≤ HWM`; two runs with equal HWM and definition version produce equal `result_hash`.
- **BR-15.7 Security.** Dimension security is applied as a filter on `ReportingBalance` queries (OPA returns allowed value sets per attribute, cached 60 s); a scheduled run renders per recipient; totals never include values the viewer cannot see; an "unrestricted" copy is produced only for recipients holding `View all dimensions`.
- **BR-15.8 Definition versioning.** Publishing creates an immutable version; runs reference the version; editing a published definition creates a draft of version n+1; retiring prevents new runs but keeps history.
- **BR-15.9 Comparative columns across calendars.** If `PY` period does not exist (calendar changed), the column shows blank with a footnote, not zero.
- **BR-15.10 Multi-ledger columns.** Columns with `ledger_code_override` are allowed only when the ledgers share the chart of accounts; otherwise `REPORT_LEDGER_INCOMPATIBLE`.
- **BR-15.11 Large cells.** Cells with > 50k balance keys store a query spec instead of the key list; drill level 1 re-executes the query at the run's HWM.
- **BR-15.12 Audit trail completeness.** Every `journal.posted` event must have an audit row within 60 s; a daily job counts events vs rows and alerts on any difference.
- **BR-15.13 Derived hierarchy resolution.** A statement run uses the hierarchy version effective on the run period's end date (or an explicit version); membership is materialized per version so re-running at the same HWM and version reproduces `result_hash`; a member matching several leaves blocks publish; for non-strict hierarchies unassigned members roll into an "Unassigned" node shown last with a footnote; a member's category change after publish takes effect only in the next version (or immediately when the hierarchy sets `follow_master_data = true`, which then records the change time in the run).

### 2.15.8 Acceptance criteria

- **AC-15.1** (FR-15.5) Given the demo entities' closed March 2026, when `BS-STD` and `PL-STD` run, then total assets = liabilities + equity (including current-period net income) to the cent and the P&L net equals `TB-STD` net of P&L accounts.
- **AC-15.2** (FR-15.5, BR-15.5) Given `CF-STD` for the same period, when run, then net cash flow equals the change in cash-tagged accounts to the cent and the "not classified" row is 0.00.
- **AC-15.3** (FR-15.7, BR-15.4) Given any cell of a run, when drilled to lines across all pages, then the cumulative sum equals the cell value exactly (property test over 1,000 random cells).
- **AC-15.4** (FR-15.7) Given a line from an AP-sourced entry (stub), when the source link is opened, then it resolves to the document route and the trace id opens in Tempo.
- **AC-15.5** (FR-15.11) Given a 200-row, 12-column definition, when exported to JSON and re-imported under a new code, then the two definitions produce identical cell values for the same parameters.
- **AC-15.6** (FR-15.9) Given a user permitted only cost center C200, when they run `TB-DIM`, then only C200 rows appear and drill-down lines are limited to C200 with `filtered_amount` shown on mixed cells.
- **AC-15.7** (FR-15.1, BR-15.6) Given postings continue during a run, when the run completes, then its `result_hash` is reproducible by re-running at the same HWM.
- **AC-15.8** (FR-15.8) Given a nightly schedule for 3 entities, when it fires, then PDF and XLSX outputs are in the library and the email is sent with attachments < 20 MB.
- **AC-15.9** (FR-15.10) Given a reopened period with a posting and re-close, when the audit trail runs, then it lists the reopen with both approvers, the posting, and the re-close in order with trace ids.
- **AC-15.10** (FR-15.6) Given 200k balances, when a 2,000 × 12 statement runs, then it completes in < 20 s.
- **AC-15.11** (Phase exit) Given the finance stakeholders' review, when they compare the Passage statement pack to the AX/SAP-derived expectations for the demo period, then every line ties and sign-off is recorded in the wave 15 issue.
- **AC-15.12** (FR-15.13, BR-15.13) Given a derived hierarchy over main account categories with `strict = true`, when a new postable account without a category is created, then publish of the next version is rejected with `REPORT_HIERARCHY_UNASSIGNED` naming the account and `hierarchy.unassigned` is emitted; after assigning the category, `BS-STD` run with the hierarchy as row set ties to `TB-STD` to the cent.
- **AC-15.13** (FR-15.14) Given the demo period with every P&L line carrying `FUNC_AREA`, when `PL-FUNC` runs, then its net result equals `PL-STD` net result to the cent and the check row is 0.00.

### 2.15.9 Test plan and fixtures

- Fixtures: `demo-fy2026-closed` (the dataset produced by waves 11–14 for the demo entities, three ledgers, EUR reporting currency); `definitions-std` (TB-STD, BS-STD, PL-STD, CF-STD, TB-DIM); `definition-200x12` (designer round-trip fixture); `mr-sample-definitions` (Management Reporter export of 10 definitions); `fsv-sample` (SAP `T011`/`FAGL_011PC` extract for 2 FSVs); `security-cases` (8 users with distinct dimension grants).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Statement tie-out | integration | standard definitions | BS balances; P&L = TB; CF reconciles |
| Drill-down sum property | property | drill API | Σ lines = cell for 1,000 random cells |
| Period expression unit tests | unit | evaluator | 40 cases incl. adjustment periods and calendar change |
| Designer round-trip | contract | export/import | equal cell values |
| MR/FSV converters | unit | migration converters | 10 MR + 2 FSV definitions convert; unsupported constructs listed |
| Security filtering | integration | OPA + reporting | 8 users see only permitted values; totals exclude |
| Read-model lag | integration | event consumer | p95 < 10 s under 5k lines/s |
| Statement performance | performance | run engine | 2,000 × 12 over 200k balances < 20 s |
| Reporting UI | Playwright + axe | designer, statements, drill | WCAG AA; keyboard drill works |

Reconciliation: `ReportingBalance` = `ledger.Balance` nightly (0 diffs); TB closing = opening + movements; BS = L + E; CF net = Δ cash; audit rows = posted events.

### 2.15.10 Deployment and operations

Helm chart `passage-reporting` (Go service 3–8 pods, CloudNativePG `reporting-db` 3 instances with 1 read replica for statement runs, Kafka consumer group `reporting-balances`, renderer sidecar for PDF (Chromium headless) with 2 CPU limit). Config: `REPORTING_MAX_LAG_S=30`, `REPORTING_PAGE_SIZE=500`, `REPORTING_CELL_KEY_LIMIT=50000`, `REPORTING_RETENTION_CLOSED_YEARS=7`, `REPORTING_RETENTION_ADHOC_DAYS=90`. Migrations: reporting schema; backfill of `ReportingBalance` from the ledger via a one-time replay of `ledger.journal` topic (compacted snapshot + replay). Feature flags: `reporting.cash_flow`, `reporting.multi_ledger_columns`, `reporting.pdf`. Scheduled jobs: nightly `reporting-balance-verify`, nightly statement pack for demo entities (23:00 UTC), library retention sweep (weekly), audit completeness check (daily). SLOs: read-model lag p95 < 10 s; statement run p95 < 20 s (reference size); drill level 2 first page p95 < 300 ms; availability 99.9%. Alerts: lag > 60 s (P2), verify diff > 0 (P1), audit completeness diff (P1), PDF renderer failures > 5% (P3). Runbook: rebuild read model from ledger replay (≈ 15 min per 10M lines), stuck run cancellation, retention restore from object storage. Rollback: digest rollback; read model can be rebuilt from the journal at any time, so no data rollback is needed.

### 2.15.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| Management Reporter `ControlRowDetail`, `ControlColumnDetail`, `ControlTreeDetail`, `ControlReport` | ReportRow, ReportColumn, ReportTree, ReportDefinition | row link types (FD, dimension sets) → account_range/dimension_filter; CALC with +−×÷ converted; unsupported: XBRL tags, cross-report links (listed) |
| AX trial balance / inquiry saved queries | not migrated | standard definitions replace them |
| SAP `T011`, `T011T`, `FAGL_011PC`, `FAGL_011ZC`, `FAGL_011QT` (FSV) | ReportDefinition (BS/P&L), ReportRow | account intervals → account_range; debit/credit split nodes → two rows with sign rules; net result node → calculation row |
| SAP Report Painter libraries | not migrated | re-authored in the designer; conversion report lists them |
| AX `MainAccountCategory`, `DimensionHierarchy` (derived hierarchies), MR reporting trees | MainAccountCategory, DerivedHierarchy, HierarchyNode | category → `MainAccount.category_id`; derived hierarchy rules copied as membership rules |
| SAP account groups / G/L hierarchies (`SETHEADER`/`SETNODE`/`SETLEAF` class 0109), profit center groups (KCH1) | DerivedHierarchy (subject main_account or dimension_value) | set intervals → range rules |
| AX database log / SAP CDHDR history | not migrated | legacy audit remains in the archive; Passage audit begins at cutover |

Validation: converted definitions run against migrated opening balances and are compared to the legacy final statements for the cutover period (tolerance 0.00 after sign normalization); conversion report reviewed by the controller.

### 2.15.12 Dependencies, risks, open questions

Depends on: wave 11 (journal events, balances), wave 12 (reporting currency amounts), wave 13 (tax audit events), wave 14 (period events, closing/opening flags, close-checklist hooks), wave 6 (dimension hierarchies for trees), wave 5 (audit store, Tempo trace links), wave 2 (OPA).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Read model lag at month-end peak invalidates "as of" statements | Medium | Medium | HWM stamping; close task waits for lag = 0; replica for runs |
| MR definitions use constructs with no equivalent | High | Low | Conversion report; manual re-authoring budgeted in migration plan |
| PDF rendering resource spikes | Medium | Low | Renderer sidecar limits; queue with concurrency 4 per pod |
| Stakeholder sign-off depends on accountant availability | Medium | High | Sign-off session scheduled at wave start; fixtures reviewed incrementally |

Open questions: (1) Should the reporting read model live in `reporting-db` (default) or query `ledger.Balance` directly with replicas? Default separate read model; decide by wave 15 start. (2) Cash flow direct method in v1? Default no (indirect only); revisit at wave 36 (cash and bank). (3) Retention of ad-hoc runs 90 days vs 1 year? Default 90 days; decide by wave 15 exit.

### 2.15.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `reporting.max_lag_seconds` | tenant | int | 30 | 0–600 | Run blocked above lag unless `allow_stale` (§2.15.5) | Financial reporting parameters > data mart refresh | — |
| `reporting.page_size` | tenant | int | 500 | 100–1,000 | Drill level 2 page size (BR-15.4) | — | ALV page size |
| `reporting.cell_key_limit` | tenant | int | 50,000 | 1,000–200,000 | BR-15.11 key list vs query spec | — | — |
| `reporting.retention_closed_years` | legal entity | int | 7 | 1–30 | Library retention for closed-period runs (FR-15.8) | Report library retention | Spool retention (SP01) |
| `reporting.retention_adhoc_days` | tenant | int | 90 | 7–730 | Ad-hoc run retention | — | Spool retention |
| `reporting.email_attachment_limit_mb` | tenant | int | 20 | 1–25 | Attach vs link (FR-15.8) | Report distribution email settings | SCOT / SOST size limits |
| `reporting.share_link_expiry_days` | tenant | int | 14 | 1–90 | Shared link lifetime | — | — |
| `reporting.include_adjustment_periods` default | definition | bool | false | true / false | BR-15.1 | MR column "Include closing/adjustment periods" | F.01 "special periods" selection |
| `reporting.opa_cache_seconds` | tenant | int | 60 | 5–600 | BR-15.7 security cache | MR tree security sync | Authorization buffer |
| `reporting.pdf_concurrency_per_pod` | tenant | int | 4 | 1–16 | Renderer queue | — | — |
| `definition.currency_type` default | definition | enum | acct | txn / acct / rpt | Column currency | MR report > Currency | F.01 currency type selection |
| `definition.rounding_display` | definition | enum | units | units / thousands / millions | Display rounding (cells keep exact values; rounding difference row shown) | MR "Rounding" (thousands) | F.01 "Display in thousands" / RFBILA00 scale |
| `definition.zero_rows` | definition | enum | hide | hide / show | Print-zero default | MR row format "Print zero" | FSV "Display zero balances" |
| `hierarchy.strict` | hierarchy | bool | true for account hierarchies | true / false | FR-15.13 publish check | Derived hierarchies (D365) validation | FSV "Not assigned" node check (OB58) |
| `hierarchy.follow_master_data` | hierarchy | bool | false | true / false | BR-15.13 | — | Set maintenance dynamic sets |
| `audit_trail.export_hash_algorithm` | tenant | enum | sha256 | sha256 | Export integrity (FR-15.10) | — | — |
| `schedule.timezone` | schedule | tz | entity time zone | IANA | Cron evaluation | Batch job time zone | SM36 server time |

### 2.15.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Trial balance `TB-STD` / `TB-DIM` | statutory report | ledger, period range, currency type, dimension filter, tree | opening, debit, credit, closing, net per account (per dimension) | `ReportingBalance` | Trial balance list page; SSRS Trial balance / Trial balance summary | FAGLB03, S_ALR_87012277, S_ALR_87012301, F.08, Fiori Trial Balance |
| Balance sheet `BS-STD` | statutory report | ledger, period, comparison (PY), tree, currency type | rows per FSV-like definition, comparison, variance | `ReportingBalance` | MR Balance sheet; SSRS Balance control | F.01 (RFBILA00), S_ALR_87012284, Fiori Display Financial Statement |
| Income statement `PL-STD` / `PL-FUNC` | statutory report | ledger, period / YTD, comparison, tree | rows, current, PY, variance, % | `ReportingBalance` | MR Income statement; SSRS Dimension statement | S_ALR_87012284, S_PL0_86000028/29 |
| Cash flow `CF-STD` | statutory report | ledger, period range | operating, investing, financing, net change, check rows | `ReportingBalance` | MR cash flow definitions | S_ALR_87012382 (FSV-based), S/4 Cash Flow app |
| Consolidated-by-entity view | operational report | legal entities, period, reporting currency | per-entity columns, sum column | `ReportingBalance` | MR multi-company report | S_ALR_87012284 multiple company codes |
| G/L account statement | operational report | account range, period, dimension | opening, lines, running balance, closing | `ReportingBalance` + journal lines | SSRS Ledger account balance | S_ALR_87012332 |
| Document journal / line item journal | operational report | ledger, posting date range, voucher range | voucher, date, lines with accounts and amounts, totals | journal line API | SSRS Posted journals / Voucher | S_ALR_87012287, S_ALR_87012289, S_ALR_87012291 |
| Statement drill-down (cell → balances → lines → document) | drill-down | run, row, column | keys, amounts, lines, source link, trace id | `StatementCell` + ledger API | MR drill-down; Transaction origin | FS10NA, FAGLB03 → FAGLL03 → FB03 |
| Audit trail | operational report | ledger, period, kind, actor | occurred at, kind, actor, object, details, trace id | `AuditTrailEntry` | SSRS Audit trail; Database log | Fiori Audit Journal, RSSCD100 (change documents), S_ALR_87012287 |
| Receivables/payables by profit center | operational report | ledger, period, profit center tree | AR/AP reconciliation balances per PC (requires FR-11.23 splitting) | `ReportingBalance` | — | S_AC0_52000887 / 52000888 |
| Balance sheet by profit center / segment | statutory report | ledger, period, segment tree | BS per segment (IFRS 8) | `ReportingBalance` with splitting dimensions | MR tree by dimension | S_ALR_87013336, FSV by segment |
| Derived hierarchy membership | list inquiry | hierarchy, version | node, members, unassigned, overlaps | `HierarchyNode` membership | Derived financial hierarchies form | KDH3 / GS03 set display |
| Read-model lag KPI | KPI | ledger | lag seconds, HWM ledger vs reporting | `/reporting/lag` | MR data mart integration status | — |
| Report library | list inquiry | definition, entity, period | run, generated at, formats, hash, retention until | `StatementRun` | MR report library | Spool / SAP Analytics Cloud stories |

### 2.15.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Financial statement pack (TB, BS, P&L, CF) | close checklist hook, schedule or user | header: entity, ledger, period, currency, definition code/version, as-of HWM; rows/columns; footer: result hash, generated at/by, "as of sequence N" | PDF, XLSX, CSV, interactive HTML | statement run id | annual statements: statutory (DE HGB §257 10 years, US 7 years); monthly packs: internal, 7 years |
| Trial balance printout | user / schedule | accounts with opening, debit, credit, closing; totals; hash | PDF, XLSX, CSV | run id | supporting record; 10 years for year-end TB |
| G/L account statement | user | account header, lines with running balance, closing | PDF, XLSX | run id | supporting record |
| Audit trail export | user / auditor request | filters used, entries, hash of the file | CSV, PDF | export run id | audit evidence; 10 years |
| Statement distribution email | schedule | subject with entity/period, body with as-of and link; attachments ≤ 20 MB | email (SMTP via wave 4) | — | distribution log retained 7 years |
| Definition export | user | JSON definition with schema version | JSON | definition version | configuration record |

XBRL / iXBRL filings are wave 48.

### 2.15.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `reporting-balance-consumer` | continuous | ledger (Kafka partition) | upsert by event id with HWM per ledger | lag p95 < 10 s | DLQ after 5 attempts; replay | lag > 60 s → P2 |
| `reporting-balance-verify` | daily 01:30 | ledger | compare with `ledger.Balance` at HWM | 10M balances < 10 min | rebuild ledger from replay | diff > 0 → P1 |
| `statement-schedules` | per schedule cron (demo pack 23:00 UTC) | schedule × entity | run per (schedule, fire time) | 2,000 × 12 < 20 s each | retry once; notify owner | failures > 5% → P3 |
| `library-retention-sweep` | weekly Sunday 04:00 | legal entity | deletes outputs past retention; legal hold respected | < 10 min | skip and retry | — |
| `audit-completeness-check` | daily 02:00 | ledger | count events vs audit rows per day | < 2 min | — | diff > 0 → P1 |
| `hierarchy-membership-refresh` | on account/dimension events; full nightly 03:00 | hierarchy | rebuild per version | 20k nodes < 1 min | — | unassigned > 0 on strict → notification |

### 2.15.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| Maintain report definitions | definitions, row/column sets, trees, hierarchies CRUD, publish, import/export | Post ledger journals (for published statutory definitions) | Financial reporting analyst, Financial controller |
| Run financial statements | POST /reporting/runs | — | Accountant, Financial controller, FP&A analyst |
| View financial statements | GET runs, cells, outputs, library, lag | — | Finance roles, Executives (via OPA dimension grants) |
| Schedule financial statements | schedules CRUD | — | Financial reporting analyst |
| View all dimensions | bypass of dimension filtering for unrestricted copies (BR-15.7) | Maintain dimension security policies | CFO, Group controller, External auditor (time-boxed) |
| View audit trail | audit-trail query and export | Post ledger journals; Maintain period status | Internal auditor, External auditor, Financial controller |
| View ledger balances / transactions (drill) | balances, drill level 2 | — | as wave 11 |

### 2.15.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| REPORT-0001 `REPORT_DEFINITION_NOT_PUBLISHED` | error | Definition {code} has no published version. | RunStatement | Publish the definition | FR-15.6 |
| REPORT-0002 `REPORT_EXPRESSION_INVALID` | error | Row {row}: {detail} in expression {expr}. | FR-15.2 validation | Fix references | FR-15.2 |
| REPORT-0003 `REPORT_PERIOD_NOT_FOUND` | error | Period {period} does not exist in ledger {ledger}. | Parameters | Choose an existing period | FR-15.6 |
| REPORT-0004 `REPORT_STALE` | warning | Read model is {lag} s behind ledger {ledger}; results flagged as stale. | lag > max | Wait or rerun | FR-15.1 |
| REPORT-0005 `REPORT_LEDGER_INCOMPATIBLE` | error | Ledgers {a} and {b} do not share a chart of accounts. | BR-15.10 | Remove the column override | BR-15.10 |
| REPORT-0006 `REPORT_NO_PERMITTED_VALUES` | error (403) | You have no permitted values for {attribute} in tree {tree}. | BR-15.7 | Request dimension access | FR-15.9 |
| REPORT-0007 `REPORT_DRILL_FILTERED` | warning | {amount} of this cell is outside your dimension permissions. | BR-15.4 | None | FR-15.7 |
| REPORT-0008 `CF_UNRECONCILED` | error | Net cash flow {cf} differs from change in cash {delta}. | BR-15.5 | Tag accounts; fix "not classified" row | FR-15.5 |
| REPORT-0009 `CF_UNCLASSIFIED_ACCOUNTS` | error | {count} balance-sheet accounts have no cash flow tag. | BR-15.5 | Tag the listed accounts | FR-15.5 |
| REPORT-0010 `REPORT_PY_PERIOD_MISSING` | info | Prior-year period not found; column left blank. | BR-15.9 | None | BR-15.9 |
| REPORT-0011 `REPORT_IMPORT_DUPLICATE` | error | Definition code {code} exists; use overwrite to replace. | FR-15.11 | Rename or overwrite | FR-15.11 |
| REPORT-0012 `REPORT_IMPORT_SCHEMA` | error | Schema version {v} is not supported (supported: {list}). | FR-15.11 | Export from a compatible version | FR-15.11 |
| REPORT-0013 `REPORT_HIERARCHY_UNASSIGNED` | error | {count} members are not assigned to any leaf of {hierarchy}: {sample}. | FR-15.13 strict | Assign members or relax `strict` | FR-15.13 |
| REPORT-0014 `REPORT_HIERARCHY_OVERLAP` | error | Member {member} matches leaves {a} and {b}. | BR-15.13 | Narrow membership rules | FR-15.13 |
| REPORT-0015 `REPORT_PLFUNC_MISSING_FUNC_AREA` | warning | P&L lines totaling {amount} have no functional area. | FR-15.14 | Add derivation steps (FR-11.22) | FR-15.14 |
| REPORT-0016 `REPORT_RENDER_FAILED` | error | {format} rendering failed for run {run}. | Renderer | Retry; other formats unaffected | FR-15.6 |
| REPORT-0017 `REPORT_ATTACHMENT_TOO_LARGE` | info | Output exceeds {limit} MB; a link was sent instead. | FR-15.8 | None | FR-15.8 |

### 2.15.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Read-model update | 5,000 lines/s peak | lag 2 / 10 / 30 s | 5,000 lines/s | `reporting_balance` ≈ ledger Balance size (≈ 5 GB per 10M combinations × periods) | PK (ledger, period, account, combination, currency_type) | lag grows; runs flagged stale or wait |
| Statement run 2,000 × 12 | 500 runs/day; 50 concurrent at close | 6 / 20 / 35 s | 50 concurrent on replica | cells 60 B × 24k × 500 × 365 ≈ 260 GB raw → kept per retention, compressed | replica; per-run query plans by row kind | queued with concurrency limit; p95 40 s |
| Drill level 1 / 2 | 20k/day | 80 / 300 / 500 ms | 200 req/s | — | cell keys / query spec; journal line indexes | page size 250 |
| PDF render | 500/day | 2 / 6 / 12 s | 4 per pod | outputs 1 MB avg × 180k/year = 180 GB object storage | — | queue |
| Audit trail query (one month) | 2k/day | 200 ms / 1 s / 2 s | — | audit view 1 KB/event × 50M = 50 GB | (ledger_id, occurred_at) | narrowed date range enforced |
| Hierarchy membership rebuild | 1,000 events/day | < 5 s per event; nightly < 1 min | — | small | (hierarchy_version, member_id) | — |

### 2.15.20 Localization and compliance hooks

- Statutory statement layouts by country: HGB §266/§275 (DE balance sheet and P&L by nature/function), French PCG "bilan / compte de résultat" formats, UK FRS 102 formats, Indian Schedule III, Spanish PGC models, US GAAP/SEC Regulation S-X ordering; wave 48 packs ship `ReportDefinition` JSON per format and language.
- Presentation: number formats, negative-number display (parentheses vs minus), thousands scaling and statement language come from wave 5 locale settings; definitions carry translatable labels.
- Segment reporting (IFRS 8 / ASC 280) relies on document splitting (FR-11.23) and segment trees.
- XBRL/iXBRL tagging (ESEF in the EU, SEC EDGAR, HMRC CT600 iXBRL, MCA India) is wave 48; `ReportRow` reserves `taxonomy_concept` for tagging.
- Audit-file exports (FEC FR, GoBD DE, SAF-T) use the ledger line API; wave 15's audit trail adds the who/when/approval layer auditors request (ISA 315/330 journal-entry testing).
- Retention: statements and audit exports per §2.15.15; legal hold suspends `library-retention-sweep` per entity.
- Privacy: audit trail contains user ids and names; exports to external auditors are scoped by time-boxed duty and logged; data-subject requests mask user display names in exports older than retention when required (actor ids remain).
- Extension points: `reporting.definition_pack`, `reporting.renderer` (statutory PDF templates), `reporting.taxonomy_mapping`.

### 2.15.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.1.4 Setup — Chart of accounts → Derived financial hierarchies (R3) (AX register row 4) | PARTIAL-15 | ADDED as FR-15.13 | derived hierarchies over main account categories and dimension values, versioned, usable as row sets and trees; AC-15.12 |
