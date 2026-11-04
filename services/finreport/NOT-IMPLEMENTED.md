# NOT-IMPLEMENTED: PSG-15 (wave 15), after slice 14

Every deliverable bullet of PSG-15, and every acceptance criterion that slices 1
to 7 do not fully meet, is listed here. Status is either **later slice of PSG-15**
(work this service still owes) or **handoff** (needs another owner or a
platform change outside the wave's owned paths).

## Deliverables (PSG-15 "Deliverables" 1-7)

| # | Deliverable | Built so far | Status | Reason |
| --- | --- | --- | --- | --- |
| 1 | Report designer: row/column/tree over account structures and dimensions | Rows (header, account_range, dimension_filter, total, calculation), columns (P, P-n, YTD, PY, PYTD, Q, FY, CALC), validation via `:validate`, JSON import/export, definition list. Web designer form (`apps/web/src/modules/finreport`): rows and columns with add, remove and move up/down, validate, save draft with If-Match, publish, export, JSON import. Slice 12: row sets and column sets (`GET/PUT /row-sets[/{code}]`, `/column-sets[/{code}]`, table `report_row_set` and `report_column_set`, migration 0014: stored with the definition rules applied to the part on its own, If-Match on replace, `updatedBy`), four shipped comparative templates (`GET /column-set-templates`, see FR-15.3), `POST /definitions:compose` (a draft body from a row set and a column set or template; stores nothing). Web: row set and column set list and form pages, the templates with a Copy link, and a "Start from saved parts" card in the designer that copies a row set's rows or a column set's or template's columns into the definition (copied, not linked). | Slice 13: reporting trees (`GET /trees`, `GET/PUT /trees/{code}`, table `report_tree`, migration 0015: units over the values of one dimension attribute with parents for roll-up, If-Match on replace) and a Reporting trees list and form in the web module. Partial. Split-pane layout, live preview, drag reorder, multi-select indent and "insert total of selection" are **later slice of PSG-15**. | Slice 1 is the evaluation core, the API and a form-based designer; the tree model and the richer editor follow. |
| 2 | Standard statements: TB, BS, P&L, CF indirect, comparative, consolidated-by-entity | Slice 2: `TB-STD` (opening, closing, movement per account class), `BS-STD` (current, prior year, variance, check row), `PL-STD` and `PL-FUNC` (period, YTD, prior year) ship in `domain/standard.go`; `GET /standard-definitions` lists them and `POST /standard-definitions:install` publishes version 1 of each code the tenant does not use, in one transaction. Web: install card on the definitions list. Slice 3: `CF-STD` (indirect: net income, non-cash adjustments, working capital, investing, financing, change in cash, check row; period and YTD), and every `cash_flow` run is reconciled per column (`domain.CheckCashFlow`, REPORT-0008 / REPORT-0009 fail the run). | Partial. `TB-DIM`, per-account rows and the entity-column (consolidated-by-entity) run mode are **later slice of PSG-15**; comparative column templates ship since slice 12 (see FR-15.3). CF-STD has no "not classified" row: an account range cannot express "every balance-sheet account outside the tagged ranges", so the run-level REPORT-0009 check (naming the accounts) stands in for it until hierarchies can be row sets. | The standard rows group accounts by the first digit of the account number (1 assets, 2 liabilities, 3 equity, 4 revenue, 5-6 expenses). That is an assumption standing in for `MainAccountCategory` (**handoff** to wave 11); a tenant with another chart edits the installed drafts. |
| 3 | Drill-down: statement, balance, journal lines, source document, trace id | Drill level 1 (balance keys of a cell, sum checked, REPORT-0099 on mismatch; computed cells answer `computed: true`). Slice 5: drill level 2 over the `reporting_journal_line` read model (migration 0005): lines of a cell or of one balance key at the run's HWM, closing entries excluded per statement kind, keyset pages in BR-15.4 order (default `REPORTING_PAGE_SIZE`, max 1,000), `sumOfLines`, `cumulativeSum` and the reconciliation of all lines against the keys on every page (`reconciled`, `difference`, `unreconciledKeys`; a gap is reported, never hidden), debit and credit, `sourceUri` (Passage route or http(s) URL, validated on write and again in the browser) and `traceUrl` from `REPORTING_TRACE_URL`. Web: journal lines panel from the drill panel, per balance or for the whole cell, with load more, breadcrumb, source and trace links. | Partial. Feeding `reporting_journal_line` from `journal.posted` / `journal.reversed` (the ledger consumer, FR-15.1) is **later slice of PSG-15**; the events are a **handoff** to wave 11. `filtered_amount` and the REPORT-0007 warning need dimension security (FR-15.9, **later slice of PSG-15**; OPA decision **handoff** to wave 2): no line is filtered today, so the response has no `filteredAmount`. Statement keyboard navigation (arrow keys, Enter to drill) is **later slice of PSG-15**. Setting `REPORTING_TRACE_URL` to the Tempo (Grafana) address of each environment is a **handoff** to deploy/. | The line feed and dimension security are not built; the trace viewer address is per environment. |
| 4 | Scheduled distribution (PDF/XLSX/email), row-level security on dimensions | Slice 3: schedules (`GET /schedules`, `GET/PUT /schedules/{code}`, table `report_schedule` with RLS): published definition, cron in an IANA zone, ledgers, currency type, formats, recipients, attachment limit, enabled, If-Match on replace, `nextRunAt`. CSV output of a run (`GET /runs/{id}/outputs/csv`) with the "as of" header and footer. Web: schedule list and form, Download CSV on the statement page. Slice 4: HTML and XLSX outputs (`GET /runs/{id}/outputs/html` and `/xlsx`) built from the same layout as CSV, REPORT-0016 on a renderer error; Download HTML and XLSX on the statement page. | Partial. The `statement-schedules` job that fires schedules (run per schedule x ledger x fire time, retry once, notify owner), the PDF renderer, stored outputs and `outputs[]` on runs and events, the report library with retention and legal hold, share links with expiry, `distribution.sent`, REPORT-0016 and REPORT-0017, the close-checklist trigger and per-recipient rendering are **later slice of PSG-15**. Which period a firing runs for (for example the last closed period) needs the period calendar (**handoff** to wave 14); the schedule stores no period yet. **Handoff** for SMTP (wave 4), the Chromium renderer sidecar (deploy/ + platform), the job runner (platform) and OPA dimension-value decisions (wave 2 `authz` Policy). | Requires the job runner, the email transport, the calendar API and the OPA decision for dimension values. |
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
| FR-15.5 standard definitions and `is_closing_entry` / `is_opening_entry` handling | Closing entries are excluded from income statements and cash flows (`closing_net`); balance sheets and trial balances start at the fiscal year's opening period 0 (brought-forward entries), per BR-15.2; cash flows leave the opening period out. `TB-STD`, `BS-STD`, `PL-STD` ship (slice 2), `CF-STD` (slice 3). `TB-STD` debit and credit columns (the read model holds net only), `TB-DIM` and the consolidated-by-entity view are **later slice of PSG-15** (the tree parameter is `POST /runs` `tree`, slice 13; with slice 14's column dimension filter a designer can already build a trial balance with one column per dimension value, only the standard `TB-DIM` code is not shipped, because its columns would have to name the tenant's dimension values). | Requires debit/credit in the read model and the tree model. |
| FR-15.6 run outputs: HTML, PDF, XLSX, CSV; `outputs[]` is empty | Runs and cells stored with SHA-256 result hash and HWM (done); the interactive HTML statement is the web module's run page. CSV (slice 3), a self-contained HTML file and an XLSX workbook (slice 4) are rendered on request from the stored cells through one shared layout, deterministic, with the same header and "as of" footer. PDF, page breaks, stored outputs and `outputs[]` are **later slice of PSG-15**; the PDF sidecar is a **handoff** to deploy/. The XLSX sheet has no styles part: bold rows, column widths and a number format (amounts show in the General format) are **later slice of PSG-15**. A spreadsheet reads XLSX amounts as binary floating point, so a value with more than 15 significant digits is shown rounded there; the file holds the exact six-place decimal, and CSV and HTML show it exactly. | PDF needs the renderer sidecar; XLSX styling is presentation only. |
| FR-15.6 performance: 2,000 x 12 over 200k balances < 20 s | Evaluation measured: `TestEvaluate_Scale2000x12Under20s` evaluates a 2,000 x 12 balance sheet over 192,000 balances (well under a second on a laptop) and checks every cell's drill sum. The end-to-end run against Postgres (balance load and 24,000 cell inserts) is not measured. | Needs a seeded `reporting-db` benchmark (later). |
| FR-15.6 queued / running / failed states and 202 + `statusUrl` for long work | Runs are synchronous; `status` is always `completed`. **later slice of PSG-15** | Async execution needs the job runner (platform). |
| FR-15.7 drill level 1 | Done (`GET .../balances`, exact sum check) | n/a |
| FR-15.7 drill level 2, source link, trace id | Done over the line read model (slice 5, see deliverable 3): `GET .../lines`, source document link, trace link. The consumer that fills the read model and the dimension-security `filtered_amount` are **later slice of PSG-15**. Runs stored before slice 5 have balance keys without their sign; drill level 2 takes the sign from the row, which is right for every row except a total mixing credit and debit rows (those show as unreconciled, never wrongly reconciled). | See deliverable 3. |
| FR-15.8 scheduling and distribution | Partial (slice 3): schedule CRUD and validation. Firing, distribution, library and share links are **later slice of PSG-15**; SMTP is a **handoff** to wave 4 | See deliverable 4. |
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
| Events: `statement.failed`, `distribution.sent`, `lag.exceeded` | `lag.exceeded.v1` built in slice 11: `finreport lag-check TENANT_ID...` (chart `lagCheck` CronJob, every minute; `store.CheckLag`, serialised per tenant by an advisory lock) opens an episode per ledger in `reporting_lag_alert` (migration 0013) when its lag first exceeds the gate, writes one event to the outbox once the lag has stayed above for `REPORTING_LAG_ALERT_S` (default 60; a ledger first seen already that far past the gate alerts at once), and closes the episode on recovery; payload `ledger_id`, `legal_entity_id` (also the `legalentityid` extension), `lag_seconds`, `hwm_ledger` (highest sequence seen, applied or waiting), `hwm_reporting`. `statement.failed` and `distribution.sent` are **later slice of PSG-15**. A cash flow run refused with REPORT-0008 / REPORT-0009 answers 422 and writes no `statement.failed`: runs are synchronous and nothing is stored. | The producers of `statement.failed` (async runs) and `distribution.sent` (distribution) are not built yet. A CronJob runs at most once a minute, so an alert can come up to a minute after the window ends. The alert rule "lag > 60 s (P2)" on the event is deploy/'s (**handoff**). |
| `statement.generated.v1`, `definition.published.v1`, `hierarchy.unassigned.v1` | Done: written to the outbox in the same transaction. `hierarchy.unassigned.v1` is raised at publish of a non-strict hierarchy; raising it when a new account or dimension value arrives needs the master-data events (see deliverable 6). | n/a |
| Retire event | Retire writes no event; PRD §2.15.5 names none. From slice 6 a retire writes a `definition_retired` audit entry in the same transaction. | n/a |
| Jobs: `reporting-balance-consumer` (built: slice 8 handler, slice 9 `consume` command; waits on the authz seed), `reporting-balance-verify` (the check exists as `POST /balance-verifications` since slice 10; the nightly runner does not), `statement-schedules`, `library-retention-sweep`, `audit-completeness-check`, `hierarchy-membership-refresh` | **later slice of PSG-15**. The schedule model exists (slice 3) and `domain.Schedule.NextRun` gives the next firing in the schedule's zone; the job that fires them does not. | Depend on the consumer and the platform job runner. |
| Schedules: delete, owner and "last run" | No delete (PRD §2.15.5 lists GET and PUT only; `enabled: false` pauses). `updatedBy` records the last editor; "last run" on the Schedules screen needs the firing job (**later slice of PSG-15**). | n/a |
| `POST` publish, validate, retire, install and hierarchy publish: `Idempotency-Key` | Not taken. Validate is stateless. Publish (definitions and hierarchies) is safe to repeat: a second call finds no draft and answers 404, so it never creates a second version. Retire succeeds again on a retired code. Install skips codes already in use. Import and run creation persist the key (`idempotency_keys` table and `statement_run.idempotency_key`). | PRD §2.15.5 asks for a key on import and runs only. |
| Tree endpoints (`/trees[/{id}]`) | Built in slice 13 as `GET /trees` (maintainers and statement runners; the list carries every tree's units, which is what the run form needs, like `GET /hierarchies`), `GET /trees/{code}` and `PUT /trees/{code}` (maintainers; If-Match, 428 or 412 as for the other parts); slice 14 adds the revision routes above. Hierarchy-backed units are **later slice of PSG-15**. | The definition list (`GET /definitions`), retire (slice 2) and run list (`GET /runs`, newest first, no cursor) are built. |
| `GET/PUT /v1/ledger/main-account-categories` | **handoff** to wave 11 | Path and entity belong to the ledger service. |
| Run list keyset pagination and report library filters (entity, period) | **later slice of PSG-15** | `GET /runs` returns at most 200 runs, filterable by definition code. |
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
| `REPORTING_*` configuration | Slice 4: every variable is read and validated at startup (`internal/config`, PRD §2.15.13 defaults and ranges) and set from the chart values; the effective values are logged. Slice 5: drill level 2 uses `REPORTING_PAGE_SIZE` as its default page, and `REPORTING_TRACE_URL` (new, not in §2.15.13: the trace viewer link) builds `traceUrl`. The lag gate, large cells, retention, attachment limit, share links, the PDF queue and the dimension-security cache are **later slice of PSG-15** (see their rows). | Their consumers are not built. |
| `reporting-db` CloudNativePG cluster, read replica, Kafka topic `passage.ledger.journal` access and its dead-letter topic `passage.ledger.journal.dlq` for consumer group `reporting-balances` (the PRD job `reporting-balance-consumer`; the group is `KAFKA_GROUP`, the inbox and principal name stay `finreport-ledger`) | **handoff** to deploy/ (deployment-cicd) | Cluster manifests are not in this wave's paths. |
| Renderer sidecar (Chromium) | **handoff** to deploy/ and **later slice of PSG-15** | See FR-15.6. |

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
| AC-15.8 nightly schedule with PDF/XLSX email | **later slice of PSG-15**; the schedule can be defined (slice 3) but nothing fires it | See deliverable 4. |
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

Left in the wave after slice 14 (not built; no open issue or PR covers them): the
consolidated-by-entity run mode (FR-15.5, needs the legal-entity columns and a
per-entity run parameter and multi-ledger loading), the `TB-STD` debit and credit
columns (the read model holds net only), a shipped `TB-DIM`, column currency filter
and `ledger_code_override` (need the transaction currency code in the read model and
the chart compatibility check), tree units backed by wave 6 hierarchy nodes and
dimension security per viewer (FR-15.4, FR-15.9), and the rest of the rows above
(scheduling jobs, PDF, converters).
