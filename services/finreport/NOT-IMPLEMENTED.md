# NOT-IMPLEMENTED: PSG-15 (wave 15), after slice 20

Every deliverable bullet of PSG-15, and every acceptance criterion that slices 1
to 7 do not fully meet, is listed here. Status is either **later slice of PSG-15**
(work this service still owes) or **handoff** (needs another owner or a
platform change outside the wave's owned paths).

## Deliverables (PSG-15 "Deliverables" 1-7)

| # | Deliverable | Built so far | Status | Reason |
| --- | --- | --- | --- | --- |
| 1 | Report designer: row/column/tree over account structures and dimensions | Rows (header, account_range, dimension_filter, total, calculation), columns (P, P-n, YTD, PY, PYTD, Q, FY, CALC), validation via `:validate`, JSON import/export, definition list. Web designer form (`apps/web/src/modules/finreport`): rows and columns with add, remove and move up/down, validate, save draft with If-Match, publish, export, JSON import. Slice 12: row sets and column sets (`GET/PUT /row-sets[/{code}]`, `/column-sets[/{code}]`, table `report_row_set` and `report_column_set`, migration 0014: stored with the definition rules applied to the part on its own, If-Match on replace, `updatedBy`), four shipped comparative templates (`GET /column-set-templates`, see FR-15.3), `POST /definitions:compose` (a draft body from a row set and a column set or template; stores nothing). Web: row set and column set list and form pages, the templates with a Copy link, and a "Start from saved parts" card in the designer that copies a row set's rows or a column set's or template's columns into the definition (copied, not linked). | Slice 13: reporting trees (`GET /trees`, `GET/PUT /trees/{code}`, table `report_tree`, migration 0015: units over the values of one dimension attribute with parents for roll-up, If-Match on replace) and a Reporting trees list and form in the web module. Partial. Split-pane layout, live preview, drag reorder, multi-select indent and "insert total of selection" are **later slice of PSG-15**. | Slice 1 is the evaluation core, the API and a form-based designer; the tree model and the richer editor follow. |
| 2 | Standard statements: TB, BS, P&L, CF indirect, comparative, consolidated-by-entity | Slice 2: `TB-STD` (opening, closing, movement per account class), `BS-STD` (current, prior year, variance, check row), `PL-STD` and `PL-FUNC` (period, YTD, prior year) ship in `domain/standard.go`; `GET /standard-definitions` lists them and `POST /standard-definitions:install` publishes version 1 of each code the tenant does not use, in one transaction. Web: install card on the definitions list. Slice 3: `CF-STD` (indirect: net income, non-cash adjustments, working capital, investing, financing, change in cash, check row; period and YTD), and every `cash_flow` run is reconciled per column (`domain.CheckCashFlow`, REPORT-0008 / REPORT-0009 fail the run). | Partial. `TB-DIM` and per-account rows are **later slice of PSG-15**; the entity-column (consolidated-by-entity) run mode ships in slice 15 (section below); comparative column templates ship since slice 12 (see FR-15.3). CF-STD has no "not classified" row: an account range cannot express "every balance-sheet account outside the tagged ranges", so the run-level REPORT-0009 check (naming the accounts) stands in for it until hierarchies can be row sets. | The standard rows group accounts by the first digit of the account number (1 assets, 2 liabilities, 3 equity, 4 revenue, 5-6 expenses). That is an assumption standing in for `MainAccountCategory` (**handoff** to wave 11); a tenant with another chart edits the installed drafts. |
| 3 | Drill-down: statement, balance, journal lines, source document, trace id | Drill level 1 (balance keys of a cell, sum checked, REPORT-0099 on mismatch; computed cells answer `computed: true`). Slice 5: drill level 2 over the `reporting_journal_line` read model (migration 0005): lines of a cell or of one balance key at the run's HWM, closing entries excluded per statement kind, keyset pages in BR-15.4 order (default `REPORTING_PAGE_SIZE`, max 1,000), `sumOfLines`, `cumulativeSum` and the reconciliation of all lines against the keys on every page (`reconciled`, `difference`, `unreconciledKeys`; a gap is reported, never hidden), debit and credit, `sourceUri` (Passage route or http(s) URL, validated on write and again in the browser) and `traceUrl` from `REPORTING_TRACE_URL`. Web: journal lines panel from the drill panel, per balance or for the whole cell, with load more, breadcrumb, source and trace links. | Partial. Feeding `reporting_journal_line` from `journal.posted` / `journal.reversed` (the ledger consumer, FR-15.1) is **later slice of PSG-15**; the events are a **handoff** to wave 11. `filtered_amount` and the REPORT-0007 warning need dimension security (FR-15.9, **later slice of PSG-15**; OPA decision **handoff** to wave 2): no line is filtered today, so the response has no `filteredAmount`. Statement keyboard navigation (arrow keys, Enter to drill) is **later slice of PSG-15**. Setting `REPORTING_TRACE_URL` to the Tempo (Grafana) address of each environment is a **handoff** to deploy/. | The line feed and dimension security are not built; the trace viewer address is per environment. |
| 4 | Scheduled distribution (PDF/XLSX/email), row-level security on dimensions | Slice 3: schedules (`GET /schedules`, `GET/PUT /schedules/{code}`, table `report_schedule` with RLS): published definition, cron in an IANA zone, ledgers, currency type, formats, recipients, attachment limit, enabled, If-Match on replace, `nextRunAt`. CSV output of a run (`GET /runs/{id}/outputs/csv`) with the "as of" header and footer. Web: schedule list and form, Download CSV on the statement page. Slice 4: HTML and XLSX outputs (`GET /runs/{id}/outputs/html` and `/xlsx`) built from the same layout as CSV, REPORT-0016 on a renderer error; Download HTML and XLSX on the statement page. Slice 16: the `finreport fire-schedules TENANT_ID...` job (chart CronJob `statementSchedules`, every five minutes) fires the schedules that came due (section "Slice 16" below): one firing per schedule x ledger x cron time, a run of the definition for the month before the cron time, CSV/HTML/XLSX rendered and stored in the report library (`report_library_entry`, SHA-256, retention date), `distribution.sent.v1` per file in the outbox, retry once, `statement.failed.v1` on a second failure; `GET /library[/{id}[/content]]`, `GET /schedule-firings`; web Report library list and file pages, "Last run" on the Schedules page. | Partial. Slice 17 built legal hold and the retention sweep (see below), slice 19 share links with expiry. The PDF renderer, `outputs[]` on runs and on `statement.generated`, REPORT-0017, the close-checklist trigger, per-recipient rendering, and consolidated or tree/hierarchy schedules are **later slice of PSG-15**. The period a firing runs for is the calendar month before the cron time, placed in a fiscal year by the schedule's `fiscalYearStartMonth`; the ledger's own calendar and "last closed period" (**handoff** to wave 14) replace that. **Handoff** for SMTP (wave 4), the Chromium renderer sidecar (deploy/ + platform), the job runner (platform) and OPA dimension-value decisions (wave 2 `authz` Policy). | Requires the email transport (the notification service sends the email from `distribution.sent.v1`), the calendar API and the OPA decision for dimension values. |
| 5 | Audit trail report: who posted, approvals, reversals, period overrides | Slice 6: `audit_trail_entry` (migration 0006, RLS) with every PRD kind (posted, replayed, reversed, batch submitted/approved/rejected, period changed, reopen approved, rate override, tax fallback, allocation, year-end and rebuild runs) plus this service's own (`statement_run`, `definition_published`, `definition_retired`, `hierarchy_published`, `schedule_saved`, `audit_exported`), the latter written in the same transaction as the change. `PutAuditEntries` ingests ledger kinds, one entry per source event id. `GET /audit-trail` (ledger, period, kind, actor, from/to; narrowed range enforced; keyset pages; `traceUrl`) and `GET /audit-trail:export` (CSV, SHA-256 header, export recorded in the trail with its digest, REPORT-0030 over 50,000 entries). Web: audit trail page with filter, entry details, load more and export showing the digest. | Partial. The consumers that turn `ledger.journal.posted` / `reversed`, batch approval, period status and reopen approval, rate override, tax fallback, allocation, year-end and rebuild events into entries are **later slice of PSG-15**; the events are a **handoff** to waves 11, 12, 13 and 14, and the CDC audit store to wave 5. PDF export (renderer sidecar, **handoff** to deploy/), keeping export files 10 years in the report library, actor display names (identity, **handoff** to wave 2) and masking names on data-subject requests are **later slice of PSG-15**. | The ledger-side events and the audit store are owned by other waves. |
| 6 | Derived financial hierarchies (main account categories, dimension values) | Slice 2: `report_hierarchy` table (RLS), domain rules (ranges, values, prefix wildcards, attribute tests; depth 12, 20,000 nodes, cycles), list, revision-checked drafts, publish with REPORT-0013 (strict) and REPORT-0014 (overlap), materialized membership per version, `hierarchy.unassigned.v1` per unassigned member of a non-strict publish, owner recorded. Web: hierarchy list and editor with publish and membership view. Slice 7: a run can take its rows from a published main account or dimension value hierarchy (`hierarchy` in `POST /runs`, see FR-15.4): one row per node with members in the ledger, parents total their children, an Unassigned row last (REPORT-0034 warning, footnoted on the statement page), a Total row, the hierarchy version recorded on the run, `GET /runs/{id}/definition` serving the expanded rows. Web: "Rows from hierarchy" on the run form, hierarchy label on the statement page. | Partial. Effective-date resolution of the hierarchy version at run time (BR-15.13: the version effective on the run period's end date; a run takes the version asked for, else the latest published, because the run's calendar periods carry no dates until the ledger calendar of wave 14 exists, **handoff**), expanding a leaf into its accounts on demand on the statement page, main account category hierarchies as run rows (need the category master data of wave 11, **handoff**), `follow_master_data`, retiring a hierarchy and a member-level membership table (membership is one JSON document per version) are **later slice of PSG-15**. The members to classify are passed in the publish request: the account master, `MainAccountCategory` / `MainAccount.category_id` and dimension values are a **handoff** to wave 11 and wave 6. Re-evaluation on `ledger.account.changed` and dimension events within 60 s (`hierarchy-membership-refresh`) and the owner notification are **later slice of PSG-15** after those events exist. | Master data is owned by waves 6 and 11. |
| 7 | P&L by function of expense (PL-FUNC) | Slice 2: `PL-FUNC` ships with revenue, cost of sales, distribution, administrative, other operating and financial rows filtered on `FUNC_AREA`, and a check row of expense lines without `FUNC_AREA`. `domain.PLFuncCheck` returns REPORT-0015. | Partial. Slice 3 stores REPORT-0015 in the run's `checks[]` as a warning (the run completes) and the statement page shows it. Expense lines whose `FUNC_AREA` is a value other than COS, DIST, ADMIN, OTHER or FIN fall out of the functional rows without showing in the check row (**later slice of PSG-15**, needs a "not in" filter). **Handoff** to wave 11 for the `FUNC_AREA` dimension (FR-11.22). | Depends on the dimension. |

## Functional requirements

| FR | Status | Reason |
| --- | --- | --- |
| FR-15.1 read model fed by `journal.posted/reversed/balance.rebuilt/year-end.closed` | Slice 8 (consumer core): `consumer.HandleJournalPosted` applies a `dev.passage.ledger.journal.posted.v1` event (source `/passage/ledger/ledger`) through `store.ApplyEntry`. It runs as the service principal `service:finreport-ledger` that the platform eventing consumer puts in the context, narrowed to the entry's legal entity (never all-scope); it fetches the entry's lines through `LineSource` (lines whose count differs from `lineCount` are retried), and the store, in one transaction, writes them to `reporting_journal_line`, upserts their accounting-currency amounts into `reporting_balance` (`currency_type` `acct`) and advances `reporting_hwm`. The read model holds exactly the entries up to the mark (BR-15.6): the ledger keys `journal.posted` by entry id, so one ledger's entries arrive through several partitions and out of order; an entry ahead of the mark waits in `reporting_entry_pending` (migration 0010, lines included) and is applied, with every waiting successor, as soon as the gap closes. A replay changes nothing. Nothing is dead-lettered for arriving early, so a slow partition cannot stop a ledger; an entry that never arrives shows as `entriesPending` on `GET /lag` and in the lag panel. Reversals arrive as `journal.posted` (lines carry `reversalOfEntryId`) and need no handler; negative postings (storno, BR-11.16) are stored as a negative amount on the side they reduce. A ledger `passage://` source link is stored as its Passage route, any other unsafe link is dropped (a link never refuses an entry). `reporting_entry_applied` records each entry and whether it was a closing entry. Malformed events and read-model refusals are `eventing.Permanent`. **Blocking handoff** to wave 11: `journal.posted` must carry `legalEntityId` (`JournalPostedData` in `services/ledger/internal/events`; `events.New` copies it to the `legalentityid` extension). Without it the consumer refuses every event: a service principal holds no all-scope duty, so with no entity it can neither mint the ledger token nor pass row-level security. **Slice 9 (transport)**: the `LineSource` HTTP client (`internal/ledgerlines`: `GET /v1/ledger/journal-lines?ledger=&documentId=&limit=1000`, signed with the service token for audience `ledger`, duty `ledger.entry.lookup`, scoped to the entry's legal entity; keeps the lines of `entryId` only because a reversal shares its original's documentId; follows `nextCursor`, at most 1,000 pages; maps `debitCredit`, `amountAcct`, `negativePosting`, `reversalOfEntryId`, `dimensions`, `sourceUri`, `traceId`; a ledger 400 or 404, such as a ledger outside the event's legal entity, is dead-lettered at once, while 401, 403, 429 and 5xx are retried) and the `consume` command of `cmd/finreport` (same image, `args: [consume]`): `eventing.NewConsumer` named `finreport-ledger` (inbox and service principal), Kafka group `reporting-balances` (override with `KAFKA_GROUP`) on `passage.ledger.journal`, dead-letter `passage.ledger.journal.dlq`, `Idempotent: true` over the platform inbox (`consumer_inbox`, migration 0011, tenant RLS, SELECT and INSERT for `passage_app`), admin port 9090 with `/healthz`, `/readyz` (Kafka ping) and `/metrics`; helm `consumer-deployment.yaml` (`consumer.enabled`, `serviceIdentity`, `serviceUrls.ledger`). The consumer cannot start until the **authz seed** below lands: like the production and purchasing consumers it stops at start with "service principal service:finreport-ledger with duty ledger.entry.lookup is not seeded" (and `eventing.NewConsumer` refuses a principal that is not in `tenant.ServicePrincipalTable()`), and the signer refuses a missing service grant (the HTTP client's tests replace the signer for that reason). Not yet exercised against a live Kafka and ledger. **Later slice of PSG-15**: backfill of a ledger whose entries predate the consumer (until then its first entry waits for entry 1), and the `balance.rebuilt` and `year-end.closed` handlers. **Handoff** to wave 11 for a reporting-currency (`rpt`) and transaction-currency amount on `LedgerLine` (only `amount` and `amountAcct` exist, so `rpt` and `txn` balances are not written) and for the posting service on the event: a year-end closing counts as closing only with source system `close` and type `year_end_closing`, both caller-supplied, while the ledger itself trusts `posted_by_service`. The fiscal period is the ledger's `periodKey` year and month until wave 14's calendar API (no opening period 0 is written). | Consumer core, ledger client and Kafka wiring are built; they run once the authz seed and the wave 11 `legalEntityId` land. |
| FR-15.1 lag SLO and nightly zero-diff verify against `ledger.Balance` | Partial. Slice 11: lag gate, `GET /lag` lag seconds and the `lag.exceeded` alert (see the Data, events, jobs rows); the lag p95 < 10 s SLO needs a metrics histogram on the platform kernel (**handoff**). Slice 10: the verify itself. `POST /balance-verifications` (new duty `reporting.balance.verify`, `Idempotency-Key`; the caller must also hold the ledger duty `ledger.balance.read`, because the ledger only sees finreport's service duty and would otherwise show its balances to a user it would refuse) reads, in one statement, the read model's mark, the entries waiting ahead of it (`reporting_entry_pending`) and the net per account (accounting currency, every period, closing entries included); then the ledger's trial balance (`GET /v1/ledger/balances?ledger=` with no filter: accounting currency, every period, year-end closings included, posted entries only, nets debit minus credit with negative postings on the reversed side as in the read model; signed as the service for the legal entity that owns the read-model rows, `internal/ledgerbalances`); then the mark again. `domain.Verify` compares them (difference = read model minus ledger, an account one side lacks counts as zero) and the check is stored in `balance_verification` (migration 0012, RLS, legal-entity scope, append-only) with `balance.verified.v1` (no amounts; `legal_entity_id` sets the `legalentityid` extension) in the same transaction. Status `matched`, `differs`, or `inconclusive` when the mark moved during the ledger read or entries waited ahead of it before or after (never reported as a mismatch). At most 500 differing accounts are stored; `diffCount` is the full number. `GET /balance-verifications` (summaries, no amounts) and `/{id}` (duty `reporting.audit.view`; the ledger amounts are withheld, `diffsWithheld: true`, from a caller without `ledger.balance.read`). Web: Ledger verification page (check a ledger, past checks) and a check page with the differing accounts. The API pods get `LEDGER_URL` and the signing key from the chart when `serviceIdentity.signingKeySecret` is set; without them the routes answer 503 (REPORT-0036), as does a request finreport cannot sign (grant not seeded, or a legacy ledger with no owning entity). A ledger that does not answer, refuses or answers something that is not this ledger's unfiltered trial balance is 502 REPORT-0036 (the cause is logged, not returned); a ledger 404 is 422. **Known limit**: with a steady mark and nothing waiting, an entry the consumer has not received at all still shows as `differs`, because the ledger's trial balance does not say which posting sequence it reflects (**handoff** to wave 11 below). **Later slice of PSG-15**: the nightly run over every ledger (needs the platform job runner and a service principal that runs without an end user), and comparing per period or per dimension combination (the check compares account totals only; the ledger's `group_by` and period filters are the way to extend it). | Until the authz seed below lands, `POST` answers 403 for every user (no role can hold the unseeded duty) and, past it, 503 REPORT-0036 (the signer has no `finreport` to `ledger` grant for `ledger.balance.read`). |
| FR-15.2 max 2,000 rows, expression validation on save | Done (`Validate`: limits, references, cycles, syntax; division by zero renders blank and is tested). Row formats bold, indent, print zero and page break are stored; underline is not modelled, and page break has no effect until PDF rendering exists (**later slice of PSG-15**). | Rendering outputs are not built. |
| BR-15.3 sign presentation and `raw:` calculations | Done (credit rows show `-net`; `raw:` uses stored net). | n/a |
| FR-15.3 columns: currency type, ledger override, variance/percent, comparative templates | Period expressions, CALC (variance and percent as expressions such as `C2-C1` or `(C2-C1)/C1*100`) and per-column currency type (empty = the run's) done. Slice 12: comparative templates (FR-15.3 "comparative column sets are templates"): `CMP-PY` (period, prior year, variance, variance %), `CMP-YTD-PY` (YTD against PYTD), `CMP-PRIOR-PERIOD` (P against P-1) and `CMP-FULL` (period and YTD against prior year, 8 columns), built from CALC columns (`C1-C2` and `(C1-C2)/C2*100`; a zero comparative renders blank, a missing prior-year period is the existing BR-15.9 behaviour) and tested through the evaluator. Variance and percent work on displayed values (BR-15.3): a growing expense row shows a positive variance (higher, not favourable), and the percent divides by the comparative as shown, so a comparative below zero reverses its sign; the expression language has no ABS (a favourable/unfavourable presentation or an ABS denominator is a product decision, not built). `CMP-PRIOR-PERIOD` is `P-1`, which crosses into the prior fiscal year and skips the adjustment period unless the definition counts adjustment periods (BR-15.1; tested through the template). Slice 14: column dimension filter (`dimensionFilter` on a column, the shape of a row's filter: `{attribute: [values or "*"]}`; a row reads a balance only when its own filter and the column's both match, so one definition shows a column per cost center or profit center; refused on CALC columns and on cash flow definitions, which reconcile over the whole ledger; validated with the definition, also in column sets, up to 200 values per attribute; the designer's column cards take `attribute=value,value`; every cell still drills to exactly the balances it summed). `*` keeps the row filter's meaning: it matches every balance, also one without a value for the attribute (so `{attr: ["*"]}` alone filters nothing), and `""` matches only balances without a value; there is no "has any value" operator (**decision for Ben**). Saved column sets carry the filter (they store whole columns) and `definitions:compose` copies it; the shipped `CMP-*` templates have none. A run limited to a tree unit, a hierarchy run and the column filter all apply (AND; tested together in the store). When large cells store a query spec instead of keys (BR-15.11, not built), the spec must carry the column filter. Currency filter within a currency type, column format and `ledger_code_override` (BR-15.10, REPORT-0005) are **later slice of PSG-15**; a budget comparative waits for wave 38. | Multi-ledger columns need the chart-of-accounts compatibility check; the read model has no transaction currency code yet. |
| FR-15.4 reporting trees and per-unit runs with roll-up | Partial (slice 7): a run over a published derived hierarchy (`hierarchy: {code, version?, normalBalance?, accountFrom?, accountTo?}`) replaces the definition's rows by the hierarchy tree and keeps its columns, kind and flags; cash flow definitions are refused (REPORT-0033). Members are the accounts, or the values of the dimension attribute, that have balances in the ledger; a member the published membership knows by the same id and key is placed as published, any other by the rules on its key (a member published under an id different from its key, or a member first seen after the publish that only an attribute rule would place, therefore falls into Unassigned at run time). Balances without a value for the dimension attribute are shown in Unassigned. Empty nodes are left out. A run's rows are bounded by the definition limit of 2,000 rows (FR-15.2): a hierarchy that yields more non-empty nodes than that is refused with REPORT-0033 (raising the limit for hierarchy runs is **later slice of PSG-15**). Statement runners list the published hierarchies on the run form (`GET /hierarchies` also takes `reporting.statement.run` and then answers published codes only). Slice 13 adds the reporting tree object and the per-unit run: `POST /runs` takes `tree: {code, unit?}` and keeps only the balances whose value of the tree's dimension attribute belongs to the unit or a unit below it, so a parent unit's run is the roll-up of its children and one run per unit gives one report per unit; without `unit` the run covers the whole tree and carries REPORT-0041 when the ledger has balances for values the tree does not list or without a value; an unknown tree or unit and a cash flow definition are refused with REPORT-0040; the run stores the tree, unit and tree revision (`statement_run.tree_*`), the `statement.generated` parameters carry `tree` and `treeUnit`, and the run form picks a tree and unit. Units are values of one dimension attribute listed by hand (a value belongs to one unit); a tree that takes its units from a wave 6 hierarchy node, OPA dimension security per viewer (REPORT-0006, FR-15.9), one request that produces every unit's report at once, tree runs together with a hierarchy-driven row set that name the same dimension, and roll-up across legal entities with the entity-column mode are **later slice of PSG-15**; **handoff** to wave 6 for dimension hierarchies. Tree changes do not write an outbox event or audit entry yet (the parts of slice 12 do not either). Review notes: the tree limits the balances before the definition is evaluated, so calculated rows and CALC columns of a parent unit are recomputed over the rolled-up balances (a parent's ratio is never the sum of its children's ratios; tested). A unit run leaves out, without a warning, the balances of values outside the unit and the balances that carry no value for the attribute (leaving other units out is the point); only a whole-tree run warns (REPORT-0041). The run records the tree revision number. Slice 14 keeps every revision body (`report_tree_revision`, migration 0016, written with the tree in one transaction, append-only; `GET /trees/{code}/revisions`, `GET /trees/{code}/revisions/{revision}`, and `GET /runs/{id}/tree` which returns the tree as the run used it, the run's unit and the current revision). The web tree editor lists the revisions and shows the units of one; the run page shows the units the run covered, and says when the tree changed since. A tree saved before migration 0016 has only its then-current revision: the migration backfills it when the migration role bypasses row-level security, and otherwise skips with a notice, in which case the store reads that revision from `report_tree` and stores it under the row lock of the next replace, so it is never lost (review fix). A run made on a revision replaced before migration 0016 answers REPORT-0037 and the run page says the revision was not kept; `GET /runs/{id}/tree` is under the view duty, like `GET /runs/{id}/definition`, and returns only the run's unit and the units below it (the whole tree for a whole-tree run), because whole trees stay maintainer-only (review decision, PRD §2.15.17 puts the run and what it was made of under "View financial statements"). Revisions are kept for as long as the tree exists (no pruning): a run in the report library (up to 7 years, FR-15.8) must still show its units; one body is at most 500 units of up to 200 values; there is no restore-a-revision action; drill-down reads the balance keys stored with the run's cells, so a later tree edit never changes an existing run or its drill. A tree unit is not an access boundary: any statement runner may pick any unit, and a plain run covers the whole ledger anyway, until dimension security (FR-15.9) filters balances per viewer. The lag gate (FR-15.1) runs before the tree filter and applies unchanged; balance verification (slice 10) checks the read model against the ledger and does not involve runs. Distribution schedules do not take a tree or unit yet (**later slice of PSG-15**). | Depends on wave 6 hierarchies and the entity model. |
| FR-15.5 standard definitions and `is_closing_entry` / `is_opening_entry` handling | Closing entries are excluded from income statements and cash flows (`closing_net`); balance sheets and trial balances start at the fiscal year's opening period 0 (brought-forward entries), per BR-15.2; cash flows leave the opening period out. `TB-STD`, `BS-STD`, `PL-STD` ship (slice 2), `CF-STD` (slice 3). `TB-STD` closing debit and credit balance columns ship in slice 20 (see Slice 20 below); `TB-DIM` and period debit and credit activity totals (the read model holds net only) are **later slice of PSG-15**; the consolidated-by-entity view is built in slice 15 (see below; without translation or elimination) (the tree parameter is `POST /runs` `tree`, slice 13; with slice 14's column dimension filter a designer can already build a trial balance with one column per dimension value, only the standard `TB-DIM` code is not shipped, because its columns would have to name the tenant's dimension values). | Requires debit/credit in the read model and the tree model. |
| FR-15.6 run outputs: HTML, PDF, XLSX, CSV; `outputs[]` is empty | Runs and cells stored with SHA-256 result hash and HWM (done); the interactive HTML statement is the web module's run page. CSV (slice 3), a self-contained HTML file and an XLSX workbook (slice 4) are rendered on request from the stored cells through one shared layout, deterministic, with the same header and "as of" footer. Slice 16 stores the files of scheduled runs in the report library; PDF, page breaks, stored outputs for ad-hoc runs and `outputs[]` on runs are **later slice of PSG-15**; the PDF sidecar is a **handoff** to deploy/. The XLSX sheet has no styles part: bold rows, column widths and a number format (amounts show in the General format) are **later slice of PSG-15**. A spreadsheet reads XLSX amounts as binary floating point, so a value with more than 15 significant digits is shown rounded there; the file holds the exact six-place decimal, and CSV and HTML show it exactly. | PDF needs the renderer sidecar; XLSX styling is presentation only. |
| FR-15.6 performance: 2,000 x 12 over 200k balances < 20 s | Evaluation measured: `TestEvaluate_Scale2000x12Under20s` evaluates a 2,000 x 12 balance sheet over 192,000 balances (well under a second on a laptop) and checks every cell's drill sum. The end-to-end run against Postgres (balance load and 24,000 cell inserts) is not measured. | Needs a seeded `reporting-db` benchmark (later). |
| FR-15.6 queued / running / failed states and 202 + `statusUrl` for long work | Runs are synchronous; `status` is always `completed`. **later slice of PSG-15** | Async execution needs the job runner (platform). |
| FR-15.7 drill level 1 | Done (`GET .../balances`, exact sum check) | n/a |
| FR-15.7 drill level 2, source link, trace id | Done over the line read model (slice 5, see deliverable 3): `GET .../lines`, source document link, trace link. The consumer that fills the read model and the dimension-security `filtered_amount` are **later slice of PSG-15**. Runs stored before slice 5 have balance keys without their sign; drill level 2 takes the sign from the row, which is right for every row except a total mixing credit and debit rows (those show as unreconciled, never wrongly reconciled). | See deliverable 3. |
| FR-15.8 scheduling and distribution | Partial. Slice 3: schedule CRUD and validation. Slice 16: firing, report library with retention date and SHA-256, `distribution.sent.v1` (attachment or link by the schedule's attachment limit), failure events. Slice 17: legal hold per legal entity and the weekly retention sweep (see Slice 17 below). Slice 18: ad-hoc run retention (see Slice 18 below). Slice 19: share links with expiry (see Slice 19 below; the gateway route that lets the download through without a JWT is a **handoff**). The close-checklist trigger and the email itself are **later slice of PSG-15**; SMTP is a **handoff** to wave 4 (the notification service consumes `distribution.sent.v1`) | See deliverable 4. |
| FR-15.9 row-level security on dimensions | **later slice of PSG-15**; OPA dimension decision is a **handoff** to wave 2 | `authz.RequireDuty` checks duties only. |
| FR-15.10 audit trail report and hashed export | Partial (slice 6): report, filters, CSV export with SHA-256, export recorded. Ledger-kind consumers and PDF export are **later slice of PSG-15**; source events are a **handoff** (waves 5, 11-14). | See deliverable 5. |
| FR-15.11 definition import/export as JSON | Done: `GET .../export`, `POST .../import` (schema v1, REPORT-0011 when the code has a draft or a published version and `overwrite` is not set, REPORT-0012 schema, optional Idempotency-Key stored in `idempotency_keys`). Store test exports, re-imports under a new code and compares cells. | n/a |
| FR-15.12 MR and FSV converters | **later slice of PSG-15**; the migration packs belong to the migration-framework owner (**handoff**). | Converters need the MR and FSV extracts. |
| FR-15.13 derived hierarchies | Partial (slices 2 and 7), see deliverable 6. JSON export and import use `GET` and `PUT /hierarchies/{code}` (the published body is accepted as a draft). | Runs over hierarchies and master-data refresh are later. |
| FR-15.14 PL-FUNC | Partial (slices 2 and 3), see deliverable 7. REPORT-0015 is a run warning. | `FUNC_AREA` is wave 11. |

## Data, events, jobs

| Item | Status | Reason |
| --- | --- | --- |
| `reporting.max_lag_seconds` gate, `allow_stale`, REPORT-0004 | Slice 11: `POST /runs` takes `allowStale`; a ledger whose read model is more than `REPORTING_MAX_LAG_S` (default 30) behind refuses the run with 409 `REPORT-0004`, and with `allowStale` the run completes with `REPORT-0004` stored as a warning in `checks[]` (statement page shows it). `GET /lag` returns `lagSeconds`, `hwmLedger` and `maxLagSeconds`; the lag panel flags a stale ledger and the run form offers "Run anyway". Lag is the age of the oldest journal entry that arrived ahead of the mark and waits for an earlier one (`reporting_entry_pending.received_at`). | The gate is the process-wide `REPORTING_MAX_LAG_S`; the tenant, legal-entity and definition overrides need the platform parameter store (**handoff**). A ledger posting the consumer has not received (event lost before the consumer, consumer stopped, or consumer behind on its partitions with nothing waiting) is invisible to this measure, so `lagSeconds` 0 means "nothing out of order", not "caught up": comparing against the ledger's own high-water mark needs a ledger HWM read (wave 11, **handoff**) and the nightly verify (**later slice of PSG-15**). |
| Fiscal calendar used for period placement | Slice 1 takes the calendar in the run request body; the web run form generates whole fiscal years (opening period 0 plus N periods, quarters spread evenly). **Handoff** to wave 14 (period calendar API). | No calendar service exists yet. |
| BR-15.11 large cells (> 50,000 keys become a query spec) | **later slice of PSG-15** | Keys are stored in full for now. |
| Events: `statement.failed`, `distribution.sent`, `lag.exceeded` | `lag.exceeded.v1` built in slice 11: `finreport lag-check TENANT_ID...` (chart `lagCheck` CronJob, every minute; `store.CheckLag`, serialised per tenant by an advisory lock) opens an episode per ledger in `reporting_lag_alert` (migration 0013) when its lag first exceeds the gate, writes one event to the outbox once the lag has stayed above for `REPORTING_LAG_ALERT_S` (default 60; a ledger first seen already that far past the gate alerts at once), and closes the episode on recovery; payload `ledger_id`, `legal_entity_id` (also the `legalentityid` extension), `lag_seconds`, `hwm_ledger` (highest sequence seen, applied or waiting), `hwm_reporting`. `statement.failed.v1` and `distribution.sent.v1` are built in slice 16 for scheduled firings (section "Slice 16"); an ad-hoc `POST /runs` that fails still writes no `statement.failed`. A cash flow run refused with REPORT-0008 / REPORT-0009 answers 422 and writes no `statement.failed`: runs are synchronous and nothing is stored. | The producers of `statement.failed` (async runs) and `distribution.sent` (distribution) are not built yet. A CronJob runs at most once a minute, so an alert can come up to a minute after the window ends. The alert rule "lag > 60 s (P2)" on the event is deploy/'s (**handoff**). |
| `statement.generated.v1`, `definition.published.v1`, `hierarchy.unassigned.v1` | Done: written to the outbox in the same transaction. `hierarchy.unassigned.v1` is raised at publish of a non-strict hierarchy; raising it when a new account or dimension value arrives needs the master-data events (see deliverable 6). | n/a |
| Retire event | Retire writes no event; PRD §2.15.5 names none. From slice 6 a retire writes a `definition_retired` audit entry in the same transaction. | n/a |
| Jobs: `reporting-balance-consumer` (built: slice 8 handler, slice 9 `consume` command; waits on the authz seed), `reporting-balance-verify` (the check exists as `POST /balance-verifications` since slice 10; the nightly runner does not), `audit-completeness-check`, `hierarchy-membership-refresh` (and `library-retention-sweep`, built in slice 17) | **later slice of PSG-15**. `statement-schedules` is built in slice 16 as `finreport fire-schedules` and `library-retention-sweep` in slice 17 as `finreport sweep-library` (neither needs a platform job runner: they are chart CronJobs like `lag-check`). The others, and the ones that depend on the consumer, are later. | Depend on the consumer and the platform job runner. |
| Schedules: delete, owner and "last run" | No delete (PRD §2.15.5 lists GET and PUT only; `enabled: false` pauses). `updatedBy` records the last editor; "last run" is on the Schedules screen from slice 16 (`GET /schedule-firings`). | n/a |
| `POST` publish, validate, retire, install and hierarchy publish: `Idempotency-Key` | Not taken. Validate is stateless. Publish (definitions and hierarchies) is safe to repeat: a second call finds no draft and answers 404, so it never creates a second version. Retire succeeds again on a retired code. Install skips codes already in use. Import and run creation persist the key (`idempotency_keys` table and `statement_run.idempotency_key`). | PRD §2.15.5 asks for a key on import and runs only. |
| Tree endpoints (`/trees[/{id}]`) | Built in slice 13 as `GET /trees` (maintainers and statement runners; the list carries every tree's units, which is what the run form needs, like `GET /hierarchies`), `GET /trees/{code}` and `PUT /trees/{code}` (maintainers; If-Match, 428 or 412 as for the other parts); slice 14 adds the revision routes above. Hierarchy-backed units are **later slice of PSG-15**. | The definition list (`GET /definitions`), retire (slice 2) and run list (`GET /runs`, newest first, no cursor) are built. |
| `GET/PUT /v1/ledger/main-account-categories` | **handoff** to wave 11 | Path and entity belong to the ledger service. |
| Run list keyset pagination | **later slice of PSG-15** | `GET /runs` returns at most 200 runs, filterable by definition code. The report library (`GET /library`, slice 16) is keyset paged and filters by definition, ledger (entity), period, schedule and format. |
| Ad-hoc balances query `GET /reporting/balances` | **later slice of PSG-15** | Needs the dimension security filter. |
| Audit view `AuditTrailEntry` | Table built (slice 6, see deliverable 5), indexed on (tenant, ledger, occurred at, id) and (tenant, occurred at, id). It is a table fed in transaction and by `PutAuditEntries`, not a materialized view over the wave 5 audit store: that store is not available to this service (**handoff** to wave 5). The BR-15.12 `audit-completeness-check` job (events vs rows per day, P1 alert) is **later slice of PSG-15**, after the consumer exists. | Depends on the consumer and the job runner. |
| Parameters `reporting.*` (tenant, legal-entity and definition scopes) | The process-wide defaults come from `REPORTING_*` (slice 4). Tenant, legal-entity and definition overrides are **later slice of PSG-15**; the parameter store is a **handoff** to the platform (wave 1/2). | Parameter store is part of the platform. |

## Platform and deployment

| Item | Status | Reason |
| --- | --- | --- |
| Kernel endpoints: `/metrics` on the admin port, `/v1/reporting/openapi.yaml`, `/asyncapi.yaml`, `/error-codes`, `X-Passage-Api-Build`, `RateLimit-*` headers | **handoff** to the platform kernel (wave 0) | Not in `platform/go`. The service serves `/healthz` and `/readyz` from `httpx.NewRouter`. |
| JWT validation, OPA decision point | **handoff** to the platform (wave 2). Slice 1 trusts the gateway headers through `tenant.Middleware` and checks duties locally. | Wave 2 owns OPA and the JWT path. |
| OTel, structured logs with `correlationId` | **handoff** to the platform kernel | Not wired in slice 1. The service uses the default JSON `slog` handler. |
| Helm: ServiceMonitor | Template built (slice 4) behind `serviceMonitor.enabled`, scraping `/metrics` on the admin port. It has nothing to scrape until the kernel serves `/metrics` (kernel endpoints row above, **handoff**). | n/a |
| `REPORTING_*` configuration | Slice 4: every variable is read and validated at startup (`internal/config`, PRD §2.15.13 defaults and ranges) and set from the chart values; the effective values are logged. Slice 5: drill level 2 uses `REPORTING_PAGE_SIZE` as its default page, and `REPORTING_TRACE_URL` (new, not in §2.15.13: the trace viewer link) builds `traceUrl`. The lag gate, large cells, retention, attachment limit, the PDF queue and the dimension-security cache are **later slice of PSG-15** (see their rows). | Their consumers are not built. |
| `reporting-db` CloudNativePG cluster, read replica, Kafka topic `passage.ledger.journal` access and its dead-letter topic `passage.ledger.journal.dlq` for consumer group `reporting-balances` (the PRD job `reporting-balance-consumer`; the group is `KAFKA_GROUP`, the inbox and principal name stay `finreport-ledger`) | **handoff** to deploy/ (deployment-cicd) | Cluster manifests are not in this wave's paths. |
| Renderer sidecar (Chromium) | **handoff** to deploy/ and **later slice of PSG-15** | See FR-15.6. |

- `REPORT-0043` (report library entry not found, or outside the caller's
  legal-entity scope) and `REPORT-0044` (a scheduled firing failed for a reason
  that is not a problem of the run, such as a database error) are slice-local
  codes from slice 16, not yet in the PRD catalog. A firing otherwise records
  the problem code of its last attempt (`REPORT-0004` stale read model,
  `REPORT-0031` unknown ledger, `REPORT-0016` render failure, and so on).

## Acceptance criteria

| AC | Status | Reason |
| --- | --- | --- |
| AC1 statements tie to the trial balance to the cent (demo dataset); cash flow reconciles to the change in cash | Partial. `TestStatements_TieOutToTrialBalance` (domain) ties BS-STD (check row 0.00), PL-STD and TB-STD on a balanced ledger; `TestInstallStandard` (store) runs the installed TB-STD with total 0.00 and the installed CF-STD with the check row 0.00; `TestCashFlow_PropertyReconcilesEveryBalancedLedger` reconciles CF-STD over random balanced ledgers with closing entries. `TestDemoDataset_StatementsTieToTrialBalanceAndCashFlowReconciles` runs TB, BS, PL and CF-STD over a deterministic 12-period closed year (cent amounts, closing entries) and ties each to the journals in every period. Running against the waves 11-14 `demo-fy2026-closed` export is **later slice of PSG-15** (fixture does not exist yet). | In-repo fixture stands in until waves 11-14 produce the export. |
| AC2 designer round-trips a 200-row definition through JSON export/import | Done. `internal/domain/designer_roundtrip_test.go` covers 200 x 12 `Marshal`, `Parse` and identical cell values; `TestExportImportRoundTripAndReproducibleHash` (store) exports, imports under a new code, publishes and runs both with identical cells. | n/a |
| AC3 drill sum property over random cells | Done. Level 1: rapid over random balances. Level 2 (slice 5): `TestDrill_LinesSumToEveryCell` (domain, 100 random ledgers of lines with closing entries and postings after the HWM, four statement kinds, every balance-backed cell) and `TestDrillLines_PropertyPagesSumToCell` (Postgres, 100 random ledgers, every cell walked page by page at a random page size; the final cumulative sum equals the cell, each line listed once per key, none after the HWM). Together over 1,000 random cells. | n/a |
| AC4 phase exit sign-off by finance stakeholders | **handoff** to the finance stakeholders and the wave 15 issue | Process, not code. |
| AC5 every deliverable has code or a row here | Met by this file. | n/a |
| AC6 `go vet` and `go test` in every Go module; `vitest` and `tsc` in `apps/web`; OpenAPI and AsyncAPI match; chart lints | Go vet and tests pass, chart lints, web tests and tsc pass. Contract tests check OpenAPI against the registered routes and AsyncAPI against the published events. The page tests run the in-repo axe AA checks; Playwright end-to-end tests are **later slice of PSG-15**. | Keyboard cell-by-cell navigation (arrow keys, Enter to drill) is **later slice of PSG-15**; the drill panels show their breadcrumb from slice 5. |
| AC-15.4 AP-sourced line opens its document and trace | Partial. A line's `sourceUri` route opens in the app and `traceUrl` opens the configured trace viewer (web test with an AP-style `/payables/invoices/...` route; store test returns source and trace). AP lines reaching the read model need the ledger consumer (**later slice of PSG-15**) and AP postings (**handoff** to wave 16); the Tempo address is a **handoff** to deploy/. | No AP posting reaches this service yet. |
| AC-15.5 export then re-import under a new code gives equal cells | Done (store test, see AC2). | n/a |
| AC-15.6 OPA dimension security | **later slice of PSG-15** | See FR-15.9. |
| AC-15.7 `result_hash` reproducible at the same HWM | Done for stored runs: two runs at the same HWM hash equally and a later posting changes hash and HWM (store test). Postings arriving during a run cannot skew it because balances and HWM are read in one statement. The consumer that moves the HWM is **later slice of PSG-15**. | n/a |
| AC-15.2 CF-STD net cash flow equals the change in cash, "not classified" 0.00 | Done at domain and store level (see AC1); an unreconciled ledger fails the run with REPORT-0008, an untagged moved account with REPORT-0009. The "not classified" row is the run check (see deliverable 2). On the demo dataset: **later slice of PSG-15**. | Needs the demo fixture. |
| AC-15.8 nightly schedule with PDF/XLSX email | Partial. A schedule fires (slice 16) and its CSV, HTML and XLSX files are in the library with `distribution.sent.v1` per file (`TestRunner_EndToEndAgainstPostgres`); for 3 entities the schedule lists 3 ledgers and fires 3 times. PDF and the email itself are **later slice of PSG-15** / **handoff** (renderer sidecar, notification service). | See deliverable 4. |
| AC-15.9 reopen and re-close appear in the audit trail | Partial. `TestAuditTrail_ReopenPostingAndReCloseInOrder` (store) loads a reopen request, two reopen approvals, the reopen, a posting and the re-close out of order and lists them in time order with both approvers and every trace id. Producing those entries from the period and ledger events is **later slice of PSG-15** (events: **handoff** to waves 11 and 14). | See deliverable 5. |
| AC-15.10 2,000 x 12 over 200k balances < 20 s | Evaluation measured in a domain test; end to end against Postgres not measured | See FR-15.6 performance. |
| AC-15.11 phase exit comparison to AX and SAP expectations | **handoff** to finance stakeholders | Process. |
| AC-15.12 derived hierarchy strict publish and `REPORT_HIERARCHY_UNASSIGNED` | Partial. A strict publish with an unassigned member is refused with REPORT-0013 naming it (store test); a non-strict publish emits `hierarchy.unassigned`. Emitting it when the account is created, and the BS-STD run with the hierarchy as row set, are **later slice of PSG-15**; categories are a **handoff** to wave 11. | See deliverable 6. |
| AC-15.13 PL-FUNC equals PL-STD net | Done at domain level (`TestPLFunc_EqualsPLStdWhenEveryLineHasFunctionalArea`, check row 0.00; a line without `FUNC_AREA` shows in the check row). On the demo dataset: **later slice of PSG-15**. | Needs the demo fixture and `FUNC_AREA` (wave 11). |

## Notes

- `REPORT-0016` (`REPORT_RENDER_FAILED`) is in the PRD catalog and is used
  from slice 4 when an output renderer fails.
- `REPORT-0099` (drill invariant), `REPORT-0027` (hierarchy not found),
  `REPORT-0018` (schedule invalid) and `REPORT-0019` (schedule not found) are
  slice-local codes. They are not yet in the message catalog (PRD §2.15.18)
  and should be added there with the owner.
- Codes `COM-0001`, `COM-0002`, `COM-0003`, `COM-0404` and `AUTH-0001` are used
  for shape, body, header and auth errors. They match the shared catalog
  pattern. Confirm them against the platform error catalog.
- `REPORT-0028` (journal line refused by the read model) is a slice-local
  code from slice 5, not yet in the PRD catalog.
- Drill level 2 is guarded by the ledger's `ledger.journal.read` ("View
  ledger transactions", wave 11) as PRD §2.15.5 and §2.15.17 say, not by
  `reporting.statement.view`; the role catalog must grant it to statement
  viewers who drill to lines (**handoff** to wave 2).
- `REPORT-0033` (a run cannot use the hierarchy as its rows) and `REPORT-0034`
  (run warning: members without a node, shown in the Unassigned row) are
  slice-local codes from slice 7, not yet in the PRD catalog. A run naming an
  unknown or unpublished hierarchy version answers 422 `REPORT-0027`, and two
  matching leaves for a member first seen at run time 422 `REPORT-0014`.
- `REPORT-0029` (audit entry refused) and `REPORT-0030` (audit export over
  50,000 entries) are slice-local codes from slice 6, not yet in the PRD
  catalog. A malformed or unbounded audit filter answers 400 `COM-0002`, like
  the other query parameters.
- The duty `reporting.audit.view` ("View audit trail", PRD §2.15.17) is new
  in slice 6; the role catalog that grants it to internal and external
  auditors and the financial controller, the time-boxed grant for external
  auditors and the segregation-of-duties conflicts with "Post ledger
  journals" and "Maintain period status" are a **handoff** to wave 2.
- The audit trail adds kinds beyond PRD §2.15.4 for the reporting service's
  own changes (`statement_run`, `definition_published`, `definition_retired`,
  `hierarchy_published`, `schedule_saved`, `audit_exported`), so exports to
  auditors are themselves logged (PRD §2.15.20 privacy note).
- The duty `reporting.statement.schedule` ("Schedule financial statements",
  PRD §2.15.17) is new in slice 3; the role catalog that grants it to the
  financial reporting analyst is a **handoff** to wave 2.

## Legal-entity scope follow-ups (#241)

- Slice 8: the consumer writes `legal_entity_id` from the event's
  `legalEntityId` (payload, or the `legalentityid` extension copied from it):
  the first entry of a ledger records it on `reporting_hwm`, later entries must
  match (422 `REPORT-0032`, permanent). An event without one is refused; the
  consumer never writes a NULL entity and never runs all-scope. It writes the
  `reporting_hwm` row before the lines.
- **handoff**: existing rows keep a NULL `legal_entity_id`; assigning them is a
  data migration owned by the tenant administrator.

## Handoffs from slice 7 (authz seed)

No new duty. Two route rows the main session must seed in `policies/authz/data.json`
(without them the gateway refuses the calls the service already allows):

| Duty | Resource | Methods | Why |
| --- | --- | --- | --- |
| `reporting.statement.view` | `/v1/reporting/runs/*/definition` | `GET` | The statement page of a hierarchy run reads the expanded rows. |
| `reporting.statement.run` | `/v1/reporting/hierarchies` | `GET` | The run form lists published hierarchies for "Rows from hierarchy". The service answers a caller without `reporting.definition.maintain` with published codes only (no drafts, `hasDraft` false); hierarchy bodies, members, edits and publish stay under `reporting.definition.maintain`. |

Until the second row is seeded, a statement runner without `reporting.definition.maintain`
sees the picker disabled with a note and runs the definition's own rows.

## Handoffs from slice 8 (authz seed)

No new duty and no new route. The consumer is an internal handler, so nothing is
seeded for this slice; the seed below is needed for the Kafka wiring and the
ledger client (built in slice 9), together with the wave 11 change.

| Item | Owner | Seed |
| --- | --- | --- |
| Service principal `finreport-ledger` | Main session (`platform/go/tenant/serviceprincipal.go`, `policies/authz/data.json`) | `servicePrincipals["finreport-ledger"] = {"ledger.entry.lookup"}`; `servicePrincipalIssuers["finreport-ledger"] = {"finreport"}`; `data.json` `service_principals["finreport-ledger"] = ["ledger.entry.lookup"]` |
| Service grant finreport to ledger | Main session (`policies/authz/data.json`) | `service_grants["finreport"] = {"ledger": ["ledger.entry.lookup"]}` (service-only `GET /v1/ledger/journal-lines`) |
| Rego pin | Main session (`policies/authz/authz_test.rego`) | `data.service_principals["finreport-ledger"] == ["ledger.entry.lookup"]`, `data.service_grants.finreport == {"ledger": ["ledger.entry.lookup"]}`, `allowed("ledger.entry.lookup", "GET", "/v1/ledger/journal-lines")`, `not allowed("ledger.entry.lookup", "GET", "/v1/ledger/journal-entries/e1")` |
| `legalEntityId` on `dev.passage.ledger.journal.posted.v1` (and AsyncAPI) | Wave 11 (`services/ledger`) | Blocking: the consumer refuses events without it. |
| Kafka consumer group `reporting-balances` on `passage.ledger.journal` (chart value `consumer.kafka.group`), dead-letter topic `passage.ledger.journal.dlq`, Kafka secret `consumer.kafka.secret` (keys `password`, `ca.crt`), ledger accepting finreport's service public key (key id `serviceIdentity.keyId`), finreport's signing key secret `serviceIdentity.signingKeySecret` (key `signing-key`), `consumer.enabled=true` | deploy/ | Deployment is not in this wave's paths; the chart defaults to the consumer off. |

## Handoffs from slice 10 (authz seed, exact rows)

New duty `reporting.balance.verify` ("Verify the reporting balances against the ledger"), enforced in the service on `POST /v1/reporting/balance-verifications` and not yet in `policies/authz/data.json`. The web nav item is gated on the existing `reporting.audit.view`; the page's check action is enforced by the service (403 shows as an error on the page). `TestEveryServiceDutyIsSeeded` in identity fails until the duty row exists (`ledger.balance.read`, which the service also checks with `HasDuty`, is already seeded).

`policies/authz/data.json` `duties`: add the new duty, and replace `reporting.audit.view` with this (two GET rows added):

```json
"reporting.balance.verify": {"permissions": [{"resource": "/v1/reporting/balance-verifications", "methods": ["POST"]}]},
"reporting.audit.view": {"permissions": [
  {"resource": "/v1/reporting/audit-trail", "methods": ["GET"]},
  {"resource": "/v1/reporting/audit-trail:export", "methods": ["GET"]},
  {"resource": "/v1/reporting/balance-verifications", "methods": ["GET"]},
  {"resource": "/v1/reporting/balance-verifications/*", "methods": ["GET"]}
]},
```

`policies/authz/data.json` `service_grants` (replaces the slice 8 row; the existing `ledger.balance.read` duty already routes `GET /v1/ledger/balances`):

```json
"finreport": {"ledger": ["ledger.entry.lookup", "ledger.balance.read"]},
```

`platform/go/tenant/service.go` `serviceGrants` (the mirror the platform test keeps equal; `ledger.balance.read` is already in `readDuties`):

```go
"finreport": {"ledger": {"ledger.entry.lookup", "ledger.balance.read"}},
```

`policies/authz/authz_test.rego`: the slice 8 pin `test_finreport_ledger_consumer_reads_only_journal_lines` asserts the old grant and must change with it; the consumer principal `finreport-ledger` stays `["ledger.entry.lookup"]`:

```rego
test_finreport_ledger_consumer_reads_only_journal_lines if {
	data.service_principals["finreport-ledger"] == ["ledger.entry.lookup"]
	data.service_grants.finreport == {"ledger": ["ledger.entry.lookup", "ledger.balance.read"]}
	data.duties["ledger.entry.lookup"].service_only
	allowed("ledger.entry.lookup", "GET", "/v1/ledger/journal-lines")
	not allowed("ledger.entry.lookup", "GET", "/v1/ledger/journal-entries/e1")
	allowed("ledger.balance.read", "GET", "/v1/ledger/balances")
}

test_reporting_balance_verification_routes if {
	allowed("reporting.balance.verify", "POST", "/v1/reporting/balance-verifications")
	not allowed("reporting.balance.verify", "GET", "/v1/reporting/balance-verifications")
	allowed("reporting.audit.view", "GET", "/v1/reporting/balance-verifications")
	allowed("reporting.audit.view", "GET", "/v1/reporting/balance-verifications/v1")
	not allowed("reporting.audit.view", "POST", "/v1/reporting/balance-verifications")
}
```

Other handoffs:

| Owner | Handoff |
| --- | --- |
| Role catalog (wave 2) | grant `reporting.balance.verify` to the finance controller role (who also needs `ledger.balance.read` to run a check) |
| Wave 11 (`services/ledger`) | return the posting sequence the trial balance reflects on `GET /v1/ledger/balances` (the ledger's `next_sequence - 1`, read in the same transaction as the balances). With it a check can tell "read model behind" (inconclusive) from a real difference; today a steady mark with nothing waiting is reported as `differs` even when an entry has not reached the consumer yet. |
| Deploy (deploy/) | give the finreport API deployment the same `serviceIdentity.signingKeySecret` as the consumer, and have the ledger accept finreport's public key (the chart now mounts it into the API pods when set) |
| PRD catalog | `REPORT-0035` (balance verification not found) and `REPORT-0036` (the ledger could not be read, or verification is not configured), added by this slice |

## Handoffs from slice 9 (authz seed, exact rows)

No new duty and no new route; migration 0011 adds `consumer_inbox` (RLS, grants in the migration). The three rows of the slice 8 table above are what the consumer needs, in this exact form (repeated so the main session seeds from one place):

| File | Row |
| --- | --- |
| `platform/go/tenant/serviceprincipal.go` | `servicePrincipals["finreport-ledger"] = {"ledger.entry.lookup"}` and `servicePrincipalIssuers["finreport-ledger"] = {"finreport"}` |
| `policies/authz/data.json` | `service_principals["finreport-ledger"] = ["ledger.entry.lookup"]` |
| `policies/authz/data.json` | `service_grants["finreport"] = {"ledger": ["ledger.entry.lookup"]}` (merge with any existing `finreport` entry) |
| `platform/go/tenant/service.go` | `serviceGrants["finreport"]["ledger"] = {"ledger.entry.lookup"}` (the signer reads this table; `ledger.entry.lookup` is already in `readDuties`) |
| `policies/authz/authz_test.rego` | the pins listed in the slice 8 table |
| `services/ledger` (wave 11) | verify finreport's public key in the ledger's service key set |


## Handoffs from slice 11 (authz seed, exact rows)

None. No new duty, route, service grant or service principal: the review moved
the lag check off the `POST /v1/reporting/lag:check` route the builder added
(which needed a user duty for a caller that has no user) into the in-process
`finreport lag-check` command run by the chart's CronJob, like costacct
`cost-gl-reconcile` and the hr jobs. `GET /v1/reporting/lag` is already seeded
under `reporting.statement.view`; it now also returns `maxLagSeconds`.
`POST /runs` takes `allowStale` under its existing duty.

Other handoffs:

| Owner | Handoff |
| --- | --- |
| Wave 11 (ledger) | A read of the ledger's own high-water mark: for a ledger, the highest posting sequence and the posting time of a given sequence (for example `GET /v1/ledger/ledgers/{id}` returning `postingSequence` and `lastPostedAt`, or a `postingSequenceFrom` filter on `GET /journal-lines`), under a service duty finreport may sign. Until then the lag is the age of the oldest out-of-order entry only, and a stopped or slow consumer, or the last entry lost, shows 0. |
| Deploy (deploy/) | Enable `lagCheck` with the tenant ids; route `lag.exceeded.v1` to ops alerting (P2); alert on the consumer itself (pod not ready, Kafka consumer-group lag on `passage.ledger.journal` for group `reporting-balances`), which is what catches a stopped consumer today. |
| Platform | Tenant-level `reporting.max_lag_seconds` (parameter store); the CronJob lists tenants in values until a tenant registry job exists. |

## Handoffs from slice 12 (authz seed, exact rows)

No new duty and no new service grant. The new routes sit under the existing duty
`reporting.definition.maintain` (the web nav items for Row sets and Column sets
are gated on it, and the designer's "Start from saved parts" card is shown only
to holders of it, because statement viewers can open a definition but not read
the parts), so the routes need these rows added to that duty in
`policies/authz/data.json` (until they exist the gateway refuses the new pages'
calls):

```json
{"resource": "/v1/reporting/row-sets", "methods": ["GET"]},
{"resource": "/v1/reporting/row-sets/*", "methods": ["GET", "PUT"]},
{"resource": "/v1/reporting/column-sets", "methods": ["GET"]},
{"resource": "/v1/reporting/column-sets/*", "methods": ["GET", "PUT"]},
{"resource": "/v1/reporting/column-set-templates", "methods": ["GET"]},
{"resource": "/v1/reporting/definitions:compose", "methods": ["POST"]}
```

## Handoffs from slice 13 (authz seed, exact rows)

No new duty and no new service grant. The tree routes sit under the existing duties;
`:action` routes are not involved, so plain rows suffice. Add to `policies/authz/data.json`:

`reporting.definition.maintain`:

```json
{"resource": "/v1/reporting/trees", "methods": ["GET"]},
{"resource": "/v1/reporting/trees/*", "methods": ["GET", "PUT"]}
```

`reporting.statement.run` (the run form lists the trees with their units; the service
accepts either duty for the list only, like `GET /hierarchies`):

```json
{"resource": "/v1/reporting/trees", "methods": ["GET"]}
```

`POST /runs` with `tree` stays under `reporting.statement.run`. The web nav item
Reporting trees is gated on `reporting.definition.maintain`, which is already seeded.

## Handoffs from slice 14 (authz seed, exact rows)

No new duty and no new service grant. Three read routes; `*` does not cross `/`, so
each needs its own row in `policies/authz/data.json`.

`reporting.definition.maintain`:

```json
{"resource": "/v1/reporting/trees/*/revisions", "methods": ["GET"]},
{"resource": "/v1/reporting/trees/*/revisions/*", "methods": ["GET"]}
```

`reporting.statement.view` (the run page loads the tree of a tree run):

```json
{"resource": "/v1/reporting/runs/*/tree", "methods": ["GET"]}
```

Until these rows are seeded the gateway refuses the calls: the tree editor shows
"Revisions are unavailable" under the form and the run page shows its tree card with
the error; nothing else on those pages is affected.

## Slice 15: consolidated-by-entity runs (FR-15.5)

`POST /runs` takes `consolidation: {ledgers: [{ledgerId, label?}]}` (2 to 12 ledgers;
`ledgerId` may then be left out). `domain.Consolidate` expands every column of the
published definition into the sum of all ledgers (same code) plus one column per
ledger (`CODE_E1`, `CODE_E2`, ... in request order, labelled with the entity label);
a CALC column is expanded too, its consolidated column calculating over the
consolidated columns and each entity column over that entity's columns (`CUR-PY`
becomes `CUR_E1 - PY_E1`), so `TB-STD` (movement) and `BS-STD` (variance) consolidate.
Rows are unchanged, so each cell drills (level 1) to exactly its ledger's balance keys,
and drill level 2 reads each key's lines from that key's ledger at that ledger's HWM
in the run (the consolidated column's pages interleave the ledgers in BR-15.4 order;
its page `hwm` is 0, the entity columns show their ledger's mark). Migration 0017
adds `statement_run.consolidation` and `legal_entity_ids` (never empty: a CHECK
keeps an empty list, which every scope would contain, out), and rewrites the
legal-entity policies of `statement_run` and `statement_cell`: a consolidated run is
visible only to a caller whose scope covers every entity of its ledgers (a partial
view would show a sum the caller cannot reproduce); a ledger without an entity makes
the run visible to all-entity callers only. Each ledger is read under the caller's
scope; one the caller cannot see fails the run with 404 `REPORT-0031`, it is never
left out. One `statement_run` audit entry per ledger, each in its own entity. Outputs
(CSV, HTML, XLSX) render the expanded columns and name every ledger and its label in
the header. Web: "Consolidate by entity" field on the run form, the run page labels
the ledgers. No new duty, route or grant (`reporting.statement.run` on `POST /runs`;
nothing to seed).

What the mode does not do: there is no currency translation and no intercompany
elimination (wave 40), so the run needs the reporting currency type `rpt` in every
column (`REPORT-0042` otherwise) and adds the ledgers' `rpt` amounts as stored. It
does not check that the ledgers share one reporting currency or one chart of
accounts (BR-15.10's `REPORT_LEDGER_INCOMPATIBLE`): the read model holds neither the
ledger currency nor the chart (**handoff** to wave 11, with the `ledger_code_override`
check below). The read model has no `rpt` balances yet (the consumer writes `acct`
only, **handoff** to wave 11 above), so until the ledger carries reporting-currency
amounts a consolidated run of real postings shows zero. Cash flow definitions (net
income and cash reconcile per ledger) and a tree or hierarchy together with
consolidation are refused with `REPORT-0042` (slice-local code, not yet in the PRD
catalog) (**later slice of PSG-15**). The run's `ledger_id` is its first ledger; the
schedule model has no consolidation field (**later slice of PSG-15**).

Left in the wave after slice 15 (not built; no open issue or PR covers them):
consolidation with a tree or hierarchy and scheduled consolidated runs, period debit and credit activity totals for `TB-STD` (the read model holds net only), a shipped `TB-DIM`, column
currency filter and `ledger_code_override` (need the transaction currency code in the
read model and the chart compatibility check), tree units backed by wave 6 hierarchy
nodes and dimension security per viewer (FR-15.4, FR-15.9), and the rest of the rows above
(scheduling jobs, PDF, converters).

## Slice 16: scheduled statements and the report library (FR-15.8)

`finreport fire-schedules TENANT_ID...` (chart CronJob `statementSchedules`, off by
default, every five minutes, in-process system actor `system:finreport-statement-schedules`
with the all-scope duty like `lag-check`: it calls no other service and no route, so it
needs no service principal and no authz seed) runs, per tenant, every enabled
schedule that came due. For each of its ledgers the due cron time is the latest one
after the later of the schedule's last save and its last recorded firing, within a
31-day look-back (a cron time older than that is not made up; when several were
missed only the newest runs). A firing places the month before the cron time (in the
schedule's zone) in a fiscal calendar built from the schedule's new
`fiscalYearStartMonth` (default 1; fiscal years are 12 monthly periods plus opening
period 0, numbered by the calendar year in which they end, and the calendar holds the
fiscal year and the two before it for prior-year columns), runs the latest published
version of the definition as the system actor with the lag gate (a stale read model
fails the firing with `REPORT-0004`; scheduled runs do not use `allowStale`), renders
the schedule's formats from the stored cells (`internal/outputs`, shared with the
download route) and, in one transaction, records the firing (`report_schedule_firing`,
unique per schedule, ledger and cron time, so a second pass or a second pod records
nothing twice), stores each file (`report_library_entry`: bytes, SHA-256, size, the run,
period, legal entity, `retain_until` = `REPORTING_RETENTION_CLOSED_YEARS` after the
firing) and writes `dev.passage.reporting.distribution.sent.v1` per file (PRD
§2.15.5: run, format, size, recipients hashed; plus the schedule code the notification
service resolves the recipients from, the SHA-256, and `delivery` = `attachment`
when the file fits the schedule's attachment limit, else `link`). The run uses the
Idempotency-Key `schedule:CODE:LEDGER:CRONTIME`, so a retry or a repeat reuses the run.
An attempt that fails is retried once at once; a second failure records the firing as
failed with the problem code and detail and writes `statement.failed.v1`; a failed
firing is not repeated by later passes (the next cron time is). Formats other than
CSV, HTML and XLSX are skipped (PDF); a schedule with only PDF fails at render with
`REPORT-0016`. The legal-entity policy applies to firings and library entries (a
caller sees those of the entities in scope), and the application role has SELECT and
INSERT only on both tables: stored files cannot be rewritten or deleted through the
service (migration 0018).

API: `GET /library` (filters `definition`, `ledger`, `schedule`, `format`, `period`
YYYY-PP; keyset paged; no file bytes), `GET /library/{id}`, `GET /library/{id}/content`
(the stored bytes, `ETag` and `X-Passage-Content-Sha256` = the listed SHA-256), all
`reporting.statement.view`; `GET /schedule-firings[?schedule=&latest=true]`
(`reporting.statement.schedule`). Web: Report library list (filters, load more) and
file page (details, SHA-256, retention, download, link to the statement), "Last run"
column on the Schedules page, "Fiscal year starts in" on the schedule form.

Not built in this slice: PDF (renderer sidecar, **handoff** to deploy/), the email
(**handoff** to wave 4: the notification service consumes `distribution.sent.v1`,
looks the recipients up by `schedule_code` and sends the file or, when `delivery` is
`link`, a link to `GET /library/{id}/content`), share links with expiry (built in slice 19; slice 17
built legal hold and the retention sweep; `retain_until` is always the
closed-period retention, `REPORTING_RETENTION_CLOSED_YEARS` service-wide, because whether the
firing's period is closed and the per-legal-entity `reporting.retention_closed_years` need
the wave 14 calendar and the settings service), recipient-specific
copies under dimension security (FR-15.9), consolidated, tree or hierarchy schedules,
an "owner" notification of a failed firing beyond the event, and running a schedule on
demand.

### Handoffs from slice 16 (authz seed, exact rows)

No new duty and no new service grant. Four read routes; `*` does not cross `/`, so each
needs its own row in `policies/authz/data.json`.

`reporting.statement.view`:

```json
{"resource": "/v1/reporting/library", "methods": ["GET"]},
{"resource": "/v1/reporting/library/*", "methods": ["GET"]},
{"resource": "/v1/reporting/library/*/content", "methods": ["GET"]}
```

`reporting.statement.schedule`:

```json
{"resource": "/v1/reporting/schedule-firings", "methods": ["GET"]}
```

Until these rows are seeded the gateway refuses the calls: the Report library page and
file page show their error alert with "Try again", and the Schedules page shows
"Unavailable" in the Last run column; nothing else is affected.

Deploy handoffs (deploy/, not this wave's paths): set `statementSchedules.enabled` and
`statementSchedules.tenantIds` per environment; create the Kafka topic
`passage.reporting.distribution` (the `statement.failed.v1` event rides on the existing
`passage.reporting.statement` topic); the notification service (wave 4) subscribes to
`distribution.sent.v1` and `statement.failed.v1`.

## Slice 17: report library retention and legal hold (FR-15.8)

`finreport sweep-library TENANT_ID...` (chart CronJob `librarySweep`, off by default,
weekly Sunday 04:00 UTC, in-process system actor `system:finreport-library-sweep` with the
all-scope duty like `lag-check` and `fire-schedules`: no route, no service call, no authz
seed) is the PRD job `library-retention-sweep`. Per tenant and legal entity, in one
transaction, it deletes the `report_library_entry` files whose `retain_until` has passed,
records the pass in `report_library_sweep` (files and bytes deleted, files kept) and writes
a `library_swept` audit entry and `dev.passage.reporting.library.swept.v1`. A pass that
finds nothing past retention writes nothing. Files without a legal entity are never
deleted (no hold could cover them). Migration 0019 gives `passage_app` DELETE on the
library table for this, and a `BEFORE DELETE` trigger refuses any delete of a file before
its `retain_until`, without a legal entity, or while its legal entity is under an active
hold, whoever issues it, so the rule does not rest on the sweep code alone. Placing a hold
and deleting an entity's files take the same per-entity advisory lock
(`report_legal_hold_lock`), so a hold committed while a sweep is running is seen by the
delete and keeps the files. The application role still has no UPDATE or TRUNCATE on the
library, and only the release columns of a hold are updatable. The sweep cutoff is the
earlier of the job clock and the database clock.

Legal hold (PRD §2.15.15: "legal hold suspends `library-retention-sweep` per entity"):
`report_legal_hold` (migration 0019, RLS with the legal-entity policy). One hold can be in
force per legal entity (409 `REPORT-0045`); while it is, the sweep deletes none of the
entity's files and records how many it kept (`heldCount`, `holdId`). `POST /legal-holds`
(`Idempotency-Key`, body `legalEntityId` and `reason`; the entity must be in the caller's
scope, 403 `REPORT-0048`), `POST /legal-holds/{id}:release` (`If-Match` the revision: 428,
412 `conflict_version`; 409 `REPORT-0046` when released already; optional `note`),
`GET /legal-holds[?active=true]`, `GET /legal-holds/{id}` and `GET /library-sweeps`. Place and
release write `legal_hold_placed` and `legal_hold_released` audit entries and
`legalhold.placed.v1` and `legalhold.released.v1` in the same transaction. Library entries
carry `onHold`. Migration 0019 also widens the audit kind check. Web: Legal holds list with
the place form (shown to holders of the new duty), the hold page with the release form, the
retention sweep table, and the library file page says when a file is kept past its date.

Duty: `reporting.library.hold` (new) enforced in the service on both POSTs; reads use
`reporting.statement.view` (the web nav item is gated on it). `REPORT-0045` to
`REPORT-0049` (0049: reason or note empty or over 500 characters) are slice-local codes,
not yet in the PRD catalog.

Not built in this slice (slice 18 built the ad-hoc run retention, slice 19 the share links): per-legal-entity `reporting.retention_closed_years`
and the 90-day retention of ad-hoc runs (built in slice 18), the closed-period
test for `retain_until` (wave 14 calendar), a hold on one file or one definition (the hold
is per legal entity, as the PRD says), and a `library.swept` consumer.

### Handoffs from slice 17 (authz seed, exact rows)

New duty `reporting.library.hold` ("Hold the report library"), enforced in the service on
`POST /v1/reporting/legal-holds` and `POST /v1/reporting/legal-holds/*:release`. Not yet in
`policies/authz/data.json`; `TestEveryServiceDutyIsSeeded` in identity fails until the row
exists. `*` crosses neither `/` nor `:` (the policy globs on both), so each route needs its
own row and the release row must carry the `:release` suffix, as `definitions/*:publish` does.

`duties` (new):

```json
"reporting.library.hold": {"permissions": [
  {"resource": "/v1/reporting/legal-holds", "methods": ["POST"]},
  {"resource": "/v1/reporting/legal-holds/*:release", "methods": ["POST"]}
]},
```

`reporting.statement.view` (add):

```json
{"resource": "/v1/reporting/legal-holds", "methods": ["GET"]},
{"resource": "/v1/reporting/legal-holds/*", "methods": ["GET"]},
{"resource": "/v1/reporting/library-sweeps", "methods": ["GET"]}
```

`policies/authz/authz_test.rego` pin (suggested):

```rego
test_reporting_legal_hold_routes if {
	allowed("reporting.library.hold", "POST", "/v1/reporting/legal-holds")
	allowed("reporting.library.hold", "POST", "/v1/reporting/legal-holds/h1:release")
	not allowed("reporting.library.hold", "GET", "/v1/reporting/legal-holds")
	not allowed("reporting.library.hold", "POST", "/v1/reporting/legal-holds/h1")
	allowed("reporting.statement.view", "GET", "/v1/reporting/legal-holds/h1")
	allowed("reporting.statement.view", "GET", "/v1/reporting/library-sweeps")
	not allowed("reporting.statement.view", "POST", "/v1/reporting/legal-holds")
	not allowed("reporting.statement.view", "POST", "/v1/reporting/legal-holds/h1:release")
}
```

Until these rows are seeded the gateway refuses the calls: the Legal holds page shows its
error alert with "Try again" and the place and release actions answer 403.

| Owner | Handoff |
| --- | --- |
| Role catalog (wave 2) | grant `reporting.library.hold` to the legal or compliance role and the financial controller; keep it apart from `reporting.statement.schedule` if the segregation rules want the schedule owner unable to lift a hold |
| Deploy (deploy/) | set `librarySweep.enabled` and `librarySweep.tenantIds` per environment; the notification service or ops alerting may subscribe to `legalhold.placed.v1`, `legalhold.released.v1` and `library.swept.v1` (topics `passage.reporting.legalhold` and `passage.reporting.library`) |
| PRD catalog | add `REPORT-0045` to `REPORT-0049` |

## Slice 18: retention of ad-hoc statement runs (FR-15.8)

`REPORTING_RETENTION_ADHOC_DAYS` (PRD §2.15.13; default 90, 7 to 730) is now used. The weekly
`finreport sweep-library TENANT_ID...` job (chart `librarySweep`, which now passes the variable
from `reporting.retentionAdhocDays`) also deletes the ad-hoc runs older than the retention days,
after the library pass, in the same in-process system actor (no route, no service call, no authz
seed for the job). An ad-hoc run is a `statement_run` that no schedule firing
(`report_schedule_firing.run_id`) and no library file (`report_library_entry.run_id`) refers to;
scheduled runs stay as long as their firing record exists. The cutoff is the earlier of the job
clock and the database clock, minus the retention in 24-hour days. The run goes with its cells.

Legal hold applies as for the library: per legal entity, in one transaction that first takes the
same `report_legal_hold_lock` for every entity its candidate runs belong to, in uuid order (the
order the delete guard uses, so a concurrent hold on a second entity of a consolidated run is
waited for and seen, and overlapping passes cannot deadlock), a run any of whose entities (`legal_entity_id`, or `legal_entity_ids`
of a consolidated run) has an active hold is kept and counted (`heldRuns`, `holdId`). A
consolidated run belongs to the pass of its first entity. A run without a legal entity is never
deleted. The pass is recorded in `report_run_sweep` (migration 0020, RLS with the legal-entity
policy, insert-only), writes a `runs_swept` audit entry and
`dev.passage.reporting.runs.swept.v1` (topic `passage.reporting.runs`); a pass that finds
nothing writes nothing.

Migration 0020 gives `passage_app` DELETE on `statement_run` only: the cells follow through
`ON DELETE CASCADE` (run as the table owner), so the role has no DELETE on `statement_cell`. A
`BEFORE DELETE` trigger on `statement_run` refuses, whoever issues the delete, a run younger
than 7 days (168 hours, the lowest configurable retention), a scheduled run, a run without a
legal entity and a run under a legal hold. Because the guard reads `started_at` and the run's
entities, the role's table-wide UPDATE on `statement_run` (migration 0007) is narrowed to
`UPDATE (duration_ms)`, the only column the store updates. The foreign key of `statement_cell`
is re-added `NOT VALID` and then validated, so the change does not hold the stronger lock for
the scan of a populated table. The retention days themselves are applied by the sweep only.

`GET /v1/reporting/run-sweeps[?limit=]` lists the passes (reads use `reporting.statement.view`).
Web: an "Ad-hoc run sweeps" table on the Legal holds page, with loading, empty and error states.

Not built in this slice: a retention for scheduled runs whose library files were swept (the run
and its firing record stay), per-tenant or per-legal-entity `reporting.retention_adhoc_days`
overrides (the parameter store is a **handoff** to the platform; the process-wide variable is
used), a hold on one run, and a `runs.swept` consumer. A run's detail page does not yet show
when the run will expire. Open product question (PRD §2.15 open question 3): 90 days or 1 year
for ad-hoc runs; the PRD default of 90 days is used.

### Handoffs from slice 18 (authz seed, exact rows)

No new duty. One new route, read with the existing `reporting.statement.view` (not yet in
`policies/authz/data.json`, so the gateway refuses it until the row exists: the "Ad-hoc run
sweeps" table then shows its error alert with "Try again"):

```json
{"resource": "/v1/reporting/run-sweeps", "methods": ["GET"]}
```

`policies/authz/authz_test.rego` pin (suggested):

```rego
test_reporting_run_sweeps_route if {
	allowed("reporting.statement.view", "GET", "/v1/reporting/run-sweeps")
	not allowed("reporting.statement.view", "POST", "/v1/reporting/run-sweeps")
}
```

| Owner | Handoff |
| --- | --- |
| Deploy (deploy/) | the existing `librarySweep` CronJob now also deletes ad-hoc runs; set `reporting.retentionAdhocDays` per environment (7 to 730). The `passage.reporting.runs` topic carries `runs.swept.v1` |
| Authz seed (#313) | the route row above |

## Slice 19: share links with expiry (FR-15.8)

A report library file can be shared by a bearer link that expires (PRD §2.15.6 "share link",
FR-15.8 "shared link with expiry", `REPORTING_SHARE_LINK_EXPIRY_DAYS`, 14 by default, 1 to 90).

- `report_share_link` (migration 0021, RLS with the legal-entity policy, insert, revoke and download
  count only): the link's secret is shown once, when the link is made; only its SHA-256 is stored.
  The token is the tenant id as 32 hex digits, a dot and 256 random bits (`domain.NewShareToken`,
  `ParseShareToken`), so the download route can open the right tenant without a header and a
  malformed token never reaches the database. Status (`active`, `expired`, `revoked`) is derived from
  the expiry and the revocation. The link's expiry is the requested days or the file's `retain_until`,
  whichever is sooner; a file already past `retain_until` cannot be shared (409 REPORT-0053); the
  foreign key removes a link when the retention sweep deletes its file (`ON DELETE CASCADE`).
- Routes: `POST /library/{id}/share-links` (new duty `reporting.library.share`; `Idempotency-Key`
  required; body `{expiresInDays?}`; 201 with `token` and `path`, a repeat returns 200 without them),
  `GET /library/{id}/share-links` (same duty; never lists the secret),
  `POST /share-links/{id}:revoke` (same duty; `If-Match` revision, 428 or 412 as for legal holds;
  409 REPORT-0054 when revoked already) and the public `GET /shared/{token}` (no duty: the token is
  the authority). The download serves exactly the stored bytes with the ETag and
  `X-Passage-Content-Sha256` of the library download, counts the download on the link, and answers
  `Cache-Control: private, no-store`, `Referrer-Policy: no-referrer`, `nosniff`,
  `Content-Disposition: attachment`, `X-Frame-Options: DENY` and a `sandbox` CSP (a stored HTML
  statement is never run as a page). A token that names no link answers 404 REPORT-0050 (also for
  another tenant's secret); an expired or revoked link answers 410 REPORT-0051, and only after the
  whole secret matched, so 404 against 410 tells nothing to someone without the link. The store opens
  the file as the in-process principal `system:finreport-share-link` (all-scope, tenant from the
  token), which can do nothing else. The download is counted by the same `UPDATE` that checks the
  revocation and the expiry, in the transaction that reads the file, so a revocation that commits
  first always wins (store test).
- Security of the token. 256 bits from `crypto/rand`, unpadded base64url; only the SHA-256 of the
  secret is stored and the lookup is by that hash (an index lookup on a hash, no comparison of the
  secret, so no timing oracle). The tenant id in the token is not trusted: it only picks the tenant
  whose row-level security the lookup runs under (the app role is `NOBYPASSRLS` and the table is
  `FORCE`d), and a secret under any other tenant finds nothing. It is there because a lookup by hash
  alone would have to cross tenants (a `SECURITY DEFINER` function or a table outside RLS); the cost
  is that whoever holds a link sees the tenant's UUID, which grants nothing (the gateway sets the
  tenant header from the JWT and strips any a client sends). The token is in the URL path, so
  `cmd/finreport` sends every path under `/v1/reporting/shared/` to `api.PublicHandler`, which serves
  the download alone, with an access log that writes `/v1/reporting/shared/{token}` instead of the
  path and problems whose `instance` is the same redacted path; each served download is logged with
  the link id and client address. Every other path goes to the platform router with
  `tenant.Middleware` exactly as before slice 19 (test: every route of `Register` still answers 401
  without a tenant, and the public handler answers 404 for anything but the download). The create
  response is the only place the secret appears; a replay of the create returns the stored link with
  200 and no secret (no second secret is minted), the list never carries it, and the events and audit
  entries carry the link id only.
- Each download is one `UPDATE` of the link row. Someone holding a valid link can make the service
  read the file and write the row at the request rate; without a valid link a request costs one
  index lookup and writes nothing. Rate limiting the public route by client address is a gateway
  handoff (below); the per-tenant limit does not apply to it because it has no tenant header.
- Audit trail kinds `share_link_created` and `share_link_revoked`; events
  `dev.passage.reporting.sharelink.created.v1` and `.revoked.v1` (topic `passage.reporting.sharelink`,
  `legalentityid` extension = the file's entity; the secret is never in an event).
- Web: on the Library file page, for a caller holding `reporting.library.share`, a "Share this file"
  card: lifetime in days (1 to 90), Create link (idempotency key per request; the link is shown once
  in a read-only field), the list of links with status, expiry and download count, and Revoke with a
  confirmation. Loading, empty and error states; vitest tests.

Decisions for Ben (defaulted by the slice): (1) a share link is a public, unauthenticated bearer link
to a financial statement: anyone who holds it downloads the file without signing in, until it expires
or is revoked (the PRD says "shared link with expiry" and does not say whether the recipient signs in).
The alternative is a link that needs a Passage login, which would make it a library deep link, not a
share. (2) Sharing is its own duty, `reporting.library.share`, although PRD §2.15.6 lists "share link"
under "View financial statements"; everyone who can view statements would otherwise be able to hand
them to anyone outside Passage. (3) The token carries the tenant UUID (see above).

Not built in this slice: the email that carries a link instead of a file when `delivery` is `link`
(the notification service, wave 4; it can create the link through `POST /library/{id}/share-links`
with a service principal once a service grant exists), a download page with a password or
one-time-use link, a per-address rate limit on the public route (the secret has 256 bits, so it is
a load limit, not a guessing defence; gateway handoff below), per-tenant and per-legal-entity overrides of the default lifetime (parameter store,
**handoff** to the platform), a lifetime cap lower than 90 days per role, and recipient-specific copies
under dimension security (FR-15.9).

### Handoffs from slice 19 (authz seed, gateway, deploy)

New duty `reporting.library.share` ("Share report library files"). Not in `policies/authz/data.json`
yet, so it cannot be assigned and the service answers 403 on the three routes; the Library file page
shows the card only to callers who hold the duty, so nobody sees it until the rows exist (the rest of
the page is unaffected). `*` does not cross `/`, so each route needs its own row:

```json
"reporting.library.share": {"permissions": [
  {"resource": "/v1/reporting/library/*/share-links", "methods": ["GET", "POST"]},
  {"resource": "/v1/reporting/share-links/*:revoke", "methods": ["POST"]}
]}
```

`policies/authz/authz_test.rego` pin (suggested):

```rego
test_reporting_library_share_routes if {
	allowed("reporting.library.share", "POST", "/v1/reporting/library/abc/share-links")
	allowed("reporting.library.share", "GET", "/v1/reporting/library/abc/share-links")
	allowed("reporting.library.share", "POST", "/v1/reporting/share-links/abc:revoke")
	not allowed("reporting.statement.view", "POST", "/v1/reporting/library/abc/share-links")
}
```

| Owner | Handoff |
| --- | --- |
| Authz seed (#313) | the duty and rows above; assign it to the Financial controller and Financial reporting analyst roles (PRD §2.15.17 lists no sharing duty; the PRD table needs a "Share report library files" row) |
| Gateway (wave 2) | Today the download is unreachable from outside: `deploy/gateway/httproutes.yaml` has no finreport route at all, and `SecurityPolicy/passage-api-jwt` targets the whole `passage-api` Gateway, so any route added there needs a JWT. To publish share links: (1) an HTTPRoute for `GET` with path prefix `/v1/reporting/shared/` (and `/api/v1/reporting/shared/`, rewritten to `/v1/...`; the web app builds links as `<origin>/api/v1/reporting/shared/<token>`) to finreport, and nothing else of finreport on it; (2) no JWT on that route only: a route-targeted SecurityPolicy without `jwt` (it overrides the Gateway-level one for that route) or, like the AS2 edge, a separate Gateway that only this route attaches to, with `test_gateway.py` pinning that no other route is public; (3) the identity headers stripped as on every listener (the handler ignores them anyway); (4) a rate limit per client address (`sourceCIDR` Distinct, as on the AS2 edge), since the per-tenant limit keys on `X-Passage-Tenant`, which this route does not have; (5) the Envoy access log for that route must not write the path or must redact the segment after `/shared/` (the token is the credential); (6) the finreport NetworkPolicy must admit the Envoy proxies of whichever Gateway carries it. The ordinary authenticated finreport routes need their own HTTPRoute as well; they are not routed today either |
| Deploy (deploy/) | create the Kafka topic `passage.reporting.sharelink` (`sharelink.created.v1`, `sharelink.revoked.v1`) |
| Notification (wave 4) | optional: send the share link instead of a library link when a `distribution.sent.v1` has `delivery` = `link` |

## Slice 20: trial balance debit and credit balance columns (FR-15.5)

A column takes `balanceSide` (`debit` or `credit`, validated by `Definition.Validate`,
refused on CALC columns; no migration: the definition body is JSON). The sign of an
account's net over the column's periods (after the column's dimension and ledger
filters, summed over all combinations) decides its side: a debit column keeps the
accounts with a net debit and shows the net, a credit column keeps those with a net
credit and shows it positive. An account is on one side as a whole, all its balance
keys go with it, so a cell still drills to exactly its balances (BR-15.4) and drill
level 2 signs each key's lines by the key's `credit` flag as before. A row's
`normalBalance` does not change a side column. `TB-STD` gains `CLOSE_DR`, `CLOSE_CR`
and `DIFF` (debit less credit, 0.00 for a balanced ledger). The web column editor has a
"Balance side" select (period columns only) and keeps the field on save.

- `POST /standard-definitions:install` publishes a code only when the tenant has none,
  so a tenant that already installed `TB-STD` keeps version 1 without the new columns;
  it adds them in the designer (the web editor offers the side) or by publishing a new
  version. No automatic upgrade of installed standard definitions exists (later slice).
- Period activity totals (debit and credit posted in the period, as against the balance
  side) need `debit` and `credit` in `reporting_balance`, which holds net only: later slice.
- No authz seed, route or event is added by this slice.
- Review fixes: drill level 2 signs a balance-side cell by the column's side, not the
  row's normal balance (a debit side cell of a credit-normal row no longer shows as
  unreconciled); a `raw:` calculation turns a side cell back into the stored net (debit
  side as shown, credit side negated) whatever the row's normal balance; a cash flow
  definition refuses `balanceSide` (its reconciliation reads every account).
- The side is decided per cell over the balances that row and column read: an account
  that is a debit in one department row and a credit in another is on opposite sides in
  those rows, while an unfiltered row nets it (its debit and credit totals differ from
  the sum of the department rows, but each balances). A consolidated column nets each
  account over all ledgers; each entity column decides per ledger.
- Transaction-currency columns (`txn`) decide the side on the net summed across
  currencies, as every `txn` column already sums; a per-currency side needs the currency
  filter of FR-15.3 (not built).
