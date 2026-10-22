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

Validation: account count match; every migrated combination validates under the migrated structure (exceptions queued); opening TB per entity equals source to the cent. Not migrated: AX `LedgerJournalTable` unposted batches (users re-key), SAP parked documents, posting definitions (re-implemented per subledger wave).

### 2.11.12 Dependencies, risks, open questions

Depends on: wave 3 (metadata entity generation), wave 4 (outbox, Temporal approval template), wave 5 (gap-free number sequences, audit), wave 6 (legal entities, fiscal calendars, currencies), wave 7 (party dimension backing), wave 10 (opening balance load).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Gap-free sequence becomes a throughput bottleneck | Medium | Medium | Sequence lock held < 1 ms; batch allocation for multi-entry documents; measured in the 5k lines/s test |
| Dimension combination table growth (millions of tuples) | Medium | Low | 32-byte hash key; combinations are reused; monitored |
| Structure change invalidates existing combinations | Medium | Medium | Combinations are tied to structure version; old versions stay queryable |

Open questions: (1) Should `amount_txn` be stored as integer minor units instead of numeric(23,6)? Default: numeric with scale check; decide by wave 12 start. (2) Idempotency scope per legal entity vs per tenant? Default: per legal entity; decide by wave 11 mid-point. (3) Maximum dimensions 20 vs AX's practical 10? Default 20; decide by wave 11 exit.

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

Posting request extensions (wave 11 contract, additive): `"ledger_group": "ALL"`, per line `"amount": {"currency":"EUR","value":"1000.00"}`, optional `"rate_override": {"rate_type":"M","rate":"1.0850000000"}`, optional `"ledger_lines": {"HGB": [ … ]}` for ledger-specific substitutions. Response contains `entries[]` one per ledger with `amounts: {txn, acct, rpt}` and `rates_applied[]`.

Commands: `PublishRate` (rate > 0, currencies enabled, 10% deviation warning), `RunRevaluation` (period open for module GL, accounts flagged, closing rate exists for every currency with balance else `LEDGER_RATE_MISSING`), `RunTranslation`.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.fx.rate.published.v1 | rate committed | rate_type, from, to, valid_from, rate, previous_rate, source, deviation_pct | ledger cache, reporting, AP/AR |
| dev.passage.fx.provider.degraded.v1 | import failure after retries | provider, last_success_at, rate_type, error | ops alerting |
| dev.passage.ledger.revaluation.completed.v1 | run posted | run_id, ledger_id, period, rate_type, gain_total, loss_total, posting_entry_id, reversal_entry_id | reporting, close |
| dev.passage.ledger.translation.completed.v1 | translation posted | run_id, ledger_id, period, cta_amount, entry_id | reporting, close |

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

Validation: rate counts per type; sampled 1,000 conversions compared with source values to the cent; opening balances in all three currencies tie per ledger. Not migrated: provider credentials (re-entered in Vault), valuation run history.

### 2.12.12 Dependencies, risks, open questions

Depends on: wave 11 (journal, posting API), wave 6 (currencies), wave 4 (Temporal schedules), wave 5 (audit).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| SAP `TCURX` non-ISO decimals conflict with ISO 4217 | Medium | Medium | Migration report flags conflicts; ISO wins; amounts rescaled with sign-off |
| Residual allocation changes a user-visible line amount | Medium | Low | Adjustment recorded on the line and shown in UI; alternative rounding-difference account configurable |
| Provider licensing (OXR) | Low | Low | ECB and manual are license-free defaults |

Open questions: (1) Should reporting currency be per ledger or per legal entity? Default per ledger; decide by wave 12 mid-point. (2) Historical rate for non-monetary items in translation: store rate on line at posting (default) or look up by date? Default store; decide by wave 12 exit. (3) Support SAP-style "second local currency" (currency type 40) as a fourth amount? Default no (use a parallel ledger); revisit at wave 40.

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

Calculation request/response (abridged): request `{legal_entity, document_type, document_date, tax_date, currency, party:{id, role, tax_id}, ship_from, ship_to, lines:[{line_ref, amount, quantity, product_id, tax_group, item_tax_group, price_includes_tax, account, dimensions, override:{tax_code?, exempt_reason?}}]}`; response `{engine, fallback, lines:[{line_ref, base, taxes:[{code, jurisdiction, rate_pct, tax_amount, recoverable, nonrecoverable, direction}]}], document_taxes_by_code:[…], posting_lines:[… wave 11 line objects …], trace_id, determination_trace[]}`.

Validation: every line has resolvable groups or an override; dates within code validity; currency enabled; party tax id format valid for reverse charge; totals of `posting_lines` balance against the caller's lines.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.tax.calculated.v1 | calculate with commit intent | document ref, engine, fallback, totals per code, trace_id | document services, audit |
| dev.passage.tax.transaction.recorded.v1 | TaxTransaction linked to journal line | transaction id, code, period, base, tax, direction, journal_line_id | reporting, lakehouse |
| dev.passage.tax.settled.v1 | settlement posted | run id, authority, period, output, input, net, entry_id, version | reporting, ledger |
| dev.passage.tax.provider.fallback.v1 | provider failure | provider, document ref, error, retry_after | ops, AP/AR recommit job |
| dev.passage.tax.rate.changed.v1 | rate row created | code, valid_from, rate_pct, previous | document services cache |

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

Validation: code counts; a 200-document sample recalculated in Passage matches the source tax to the cent; unsettled totals per code match. Not migrated: settled history (kept in the legacy archive), provider transaction logs.

### 2.13.12 Dependencies, risks, open questions

Depends on: wave 11 (posting), wave 12 (currency conversion for tax amounts), wave 7 (party tax ids, VIES check), wave 8 (product categories), wave 6 (addresses).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Jurisdiction tables for US are vast and change monthly | High | Medium | Providers are the production default for US; internal engine ships sample states only |
| Country-specific VAT box mappings | High | Medium | Mapping is configuration (wave 48 packs); wave 13 ships DE/FR/UK/IN reference mappings |
| Rounding disputes with legacy figures | Medium | Low | Rounding mode configurable per code; migration validation sample |

Open questions: (1) Settlement counterpart as tax authority Party (vendor role) vs settlement account? Default Party; decide by wave 13 mid-point. (2) Store provider raw payloads for 13 months or 7 years? Default 13 months payload + 7 years hash; decide by wave 13 exit. (3) Should `tax/calculate` be callable from the posting API automatically for manual journals with a tax group? Default yes behind flag `ledger.auto_tax`; decide by wave 14.

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

Commands: `TransitionPeriodStatus` (validates FR-14.2 ordering; closing with unposted approved batches dated in the period → `CLOSE_PENDING_BATCHES` unless `force` with duty); `RequestReopen` (reason ≥ 10 chars, two approvers distinct from requester); `RunAllocation` (period open for GL, basis non-zero or `allocate_from_zero`); `RunYearEndClose` (all periods closed per FR-14.10, retained earnings account exists and is equity).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.close.period.status-changed.v1 | PeriodStatus transition | ledger_id, fiscal_period_id, module, from, to, actor, approvals[], reason | all posting services (cache), close, reporting, audit |
| dev.passage.close.run.created.v1 | run instantiated | run_id, template_id, period, legal_entity_ids, task_count | notifications |
| dev.passage.close.task.completed.v1 | task completed/skipped | run_id, task_id, status, actor, evidence_uri, late (bool) | board, notifications, reporting |
| dev.passage.ledger.allocation.completed.v1 | run posted/reversed | run_id, rule_ids, period, iterations, entry_ids, reversal | reporting, close |
| dev.passage.ledger.accrual.scheduled.v1 | scheme applied | source_entry_id, scheme, occurrences[] (dates, amounts) | close board |
| dev.passage.ledger.year-end.closed.v1 | year-end posted | ledger_id, fiscal_year_id, closing_entry_ids, opening_entry_ids, retained_earnings_total | reporting, close |

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

Validation: period statuses per module equal the source at cutover; allocation rules re-executed on the last closed period in simulate mode and compared to source postings (tolerance 0.01 per destination). Not migrated: completed close task history, prior allocation run history, SAP accrual engine objects (re-keyed as schemes).

### 2.14.12 Dependencies, risks, open questions

Depends on: wave 11 (posting API hook, reversal), wave 12 (revaluation hook on checklist), wave 13 (settlement hook), wave 4 (Temporal human tasks and timers), wave 6 (fiscal calendars with adjustment periods), wave 2 (user groups).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Subledger services (future waves) bypass module mapping | Medium | High | Source mapping is mandatory; unmapped `source.system` → GL and rejected when GL on hold; contract test suite grows per wave |
| Iterative allocation performance with many dimensions | Low | Medium | Solver limited to 50 rules per group; measured in fixtures |
| Year-end with millions of combinations | Medium | Medium | Chunked generation (50k lines per entry, multiple entries per run); run measured on 1M-combination synthetic set |

Open questions: (1) Should on_hold allow service accounts of the owning module by default? Default no; decide by wave 14 mid-point. (2) Retained earnings per dimension as default? Default yes for the demo; decide with accountant review by wave 14 exit. (3) Do adjustment periods 13–16 get their own close checklist? Default: same run, tasks flagged `adjustment_only`.

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

Queries and validation: `RunStatement` requires a published definition, period exists for every selected ledger, viewer has at least one permitted dimension value when a tree is used, and read-model lag ≤ `max_lag_seconds` (default 30) unless `allow_stale = true` (result flagged). `ImportDefinition` validates schema version and rejects duplicate codes without `overwrite`.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.reporting.statement.generated.v1 | run completed | run_id, definition code/version, parameters, hwm_by_ledger, result_hash, outputs[] | distribution, close (automation hook), audit |
| dev.passage.reporting.statement.failed.v1 | run failed | run_id, error, stage | ops, requester notification |
| dev.passage.reporting.definition.published.v1 | publish | code, version, actor, diff_summary | audit, cache |
| dev.passage.reporting.lag.exceeded.v1 | lag > threshold for 60 s | ledger_id, lag_seconds, hwm_ledger, hwm_reporting | ops alerting, close board |
| dev.passage.reporting.distribution.sent.v1 | email/link delivered | run_id, recipients (hashed), format, size | audit |

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
