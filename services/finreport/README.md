# finreport: financial reporting (wave 15, PSG-15)

Report definitions (row, column and tree designer model), statement runs over
the reporting read model, and drill-down from a statement cell to its balance
keys and on to the journal lines, their source documents and traces. Phase 2
exit wave. Reads from its own `reporting` database, fed by ledger
events.

## Slice 11: lag gate and lag alert

`GET /v1/reporting/lag` returns per ledger `lagSeconds` (age of the oldest
journal entry that arrived ahead of the mark and waits for an earlier one),
`hwmLedger` (highest sequence seen, applied or waiting) and the gate
`maxLagSeconds`. `POST /runs` refuses a ledger whose lag is above
`REPORTING_MAX_LAG_S` with 409 REPORT-0004 unless the body sets `allowStale`
(default false), which completes the run with REPORT-0004 stored as a warning.
`finreport lag-check TENANT_ID...` (the chart's `lagCheck` CronJob, every
minute, in process as `system:finreport-lag-check`, no route and no authz
seed) keeps one `reporting_lag_alert` row (migration 0013) per ledger above
the gate and writes one `lag.exceeded.v1` per episode once the lag has stayed
above it for `REPORTING_LAG_ALERT_S`; checks of one tenant are serialised by an
advisory lock. The measure is a lower bound: a consumer that is stopped or
behind on its partitions, or an event lost with nothing after it, shows no lag
(NOT-IMPLEMENTED.md).

## Slice 10: ledger verification

`POST /v1/reporting/balance-verifications` (duty `reporting.balance.verify`,
plus the ledger duty `ledger.balance.read`, `Idempotency-Key` required)
compares the net per account of the read model (accounting currency, every
period) with the ledger's trial balance and stores the result in
`balance_verification` (migration 0012). `matched`, `differs`, or
`inconclusive` when the read model's high-water mark moved while the ledger
was read or entries waited ahead of it. `GET /balance-verifications[/{id}]`
(duty `reporting.audit.view`) lists and shows the checks; the ledger amounts
of a check are withheld from a caller without `ledger.balance.read`. `balance.verified.v1` goes to the outbox in the same
transaction. The API reads the ledger with `LEDGER_URL` and the service signing
key (the chart sets both when `serviceIdentity.signingKeySecret` is set);
without them the routes answer 503. See NOT-IMPLEMENTED.md for the authz seed.

## Status: slice 6

Slice 6 adds the audit trail report (FR-15.10; on top of slices 1 to 5, below):

- `audit_trail_entry` (migration 0006, RLS): `AuditTrailEntry` with occurred
  at, ledger, period, kind, actor, object reference, details (JSON) and trace
  id, indexed on (ledger, occurred at). Kinds are the PRD's ledger kinds
  (posted, replayed, reversed, batch submitted/approved/rejected, period
  changed, reopen approved, rate override, tax fallback, allocation, year-end
  and rebuild runs) and this service's own: `statement_run`,
  `definition_published` (also on standard install), `definition_retired`,
  `hierarchy_published`, `schedule_saved` and `audit_exported`. The reporting
  kinds are written in the same transaction as the change (a refused or
  replayed change writes none), with the request id as trace.
  `PutAuditEntries` takes the ledger kinds from other services' events, one
  entry per source event id (a redelivery is a replay); the consumers that will
  call it are a later slice, and tests load entries through it.
- `GET /audit-trail?ledger=&period=&kind=&actor=&from=&to=&cursor=&limit=`:
  entries oldest first, keyset paged (default 100, at most 1,000), each with
  `traceUrl` from `REPORTING_TRACE_URL`. Every query is narrowed (PRD
  §2.15.19): a period, or from and to at most 366 days apart, else 400.
- `GET /audit-trail:export?…`: every matching entry as CSV (details as JSON
  with sorted keys, formula text neutralized), with `X-Passage-Export-Sha256`
  of the body and `X-Passage-Export-Id`; the export is itself recorded
  (`audit_exported` with the filter, entry count and digest). More than 50,000
  entries is refused with REPORT-0030, never cut short.
- Duty `reporting.audit.view` ("View audit trail", PRD §2.15.17).
- AC-15.9 at store level: a reopen with two approvals, a posting and the
  re-close, loaded out of order, list in time order with their trace ids.
- Web: Audit trail page (filter, entries, entry details, load more, export
  with the digest shown).

Slice 5 added drill level 2 (FR-15.7, BR-15.4):

- `reporting_journal_line` (migration 0005, RLS): one row per ledger line with
  posting sequence, line number, posting date, period, account, combination,
  currency type, voucher, debit, credit (net generated as debit - credit),
  closing flag, source document link and trace id. `PutJournalLines` writes it
  (validated: a source link must be a Passage route or an http(s) URL, never
  `javascript:` or `//host`; a stored line is a replay and is kept). The ledger
  consumer that will feed it is a later slice; tests load lines through it.
- `GET /runs/{id}/cells/{row}/{column}/lines?key=&cursor=&limit=`: the lines of
  the cell's balance keys (or of one key, `key` = its position in the level 1
  list) at the run's HWM, closing entries left out for income statements and
  cash flows, ordered by (posting date, posting sequence, line number, key) and
  keyset paged (default `REPORTING_PAGE_SIZE`, at most 1,000). Each line shows
  debit, credit and the amount as the cell shows it: every balance key now
  records its row's sign (`credit`), so a total mixing credit and debit rows
  signs each line correctly. Every page carries `sumOfLines`, `cumulativeSum`
  (the final page's equals the cell value when the lines reconcile) and the
  reconciliation of all the lines against the keys (`reconciled`,
  `linesTotal`, `difference`, `unreconciledKeys`): a gap in the read model is
  reported, never hidden. Needs the ledger's "View ledger transactions" duty
  (`ledger.journal.read`, PRD §2.15.5).
- `traceUrl` on each line from `REPORTING_TRACE_URL` (an http(s) template with
  `{traceId}`, validated at startup; empty shows the id without a link).
- AC-15.3: a domain property test (random lines aggregated into balances,
  every cell's lines sum to it) and a Postgres property test that walks every
  cell's pages at random page sizes and ends on the cell value.
- Web: "Show all journal lines" and per-balance "Lines" in the drill panel open
  a journal lines panel (breadcrumb Statement › cell › Balances › Lines) with
  load more, the reconciliation note or warning, source document links (in-app
  route or new tab) and trace links.

Slice 4 added:

- Run outputs as HTML and XLSX (FR-15.6, PRD §2.15.15):
  `GET /runs/{id}/outputs/html` is one self-contained HTML document (inline
  styles, no scripts, every label escaped by `html/template`);
  `GET /runs/{id}/outputs/xlsx` is a workbook with one sheet, `Statement`,
  amounts as numeric cells and text as inline strings (never formulas). CSV,
  HTML and XLSX are built from one layout (`domain.buildLayout`), so the three
  files of a run carry the same header, grid (`n/a` for a missing prior-year
  period) and "as of" footer, and the same run always yields the same bytes.
  A renderer error answers 500 REPORT-0016. pdf still answers 404 (renderer
  sidecar).
- Startup configuration (`internal/config`): every `REPORTING_*` variable
  below has a default and an allowed range from PRD §2.15.13. An out-of-range
  or non-integer value stops the process, naming every bad variable, before it
  connects to the database. The effective values are logged at startup.
- Helm: a ServiceMonitor template behind `serviceMonitor.enabled` (scrapes
  `/metrics` on the admin port), and every `REPORTING_*` variable set from the
  `reporting` values.
- Web module: Download CSV, HTML and XLSX on the statement page.

Slice 3 built:

- `CF-STD`, the indirect cash flow (FR-15.5, BR-15.5): net income, non-cash
  adjustments, working capital, operating, investing and financing sections,
  the change in cash and a check row, for the period and the year to date. It
  installs with the other standard definitions. Cash flow statements show
  movements: closing entries and opening periods (period 0) are left out. Every
  run of a `cash_flow` definition with tagged rows is reconciled per column:
  an untagged balance-sheet account that moved fails the run with REPORT-0009
  (naming the accounts), a net cash flow that differs from the change in cash
  fails it with REPORT-0008; nothing is stored. The domain property test
  reconciles CF-STD over random balanced ledgers (AC-15.2).
- Run checks: warnings stored with a completed run and returned as
  `checks[]` on every run read. PL-FUNC with expense lines without
  `FUNC_AREA` carries REPORT-0015 (FR-15.14).
- `GET /runs/{id}/outputs/csv` (FR-15.6, PRD §2.15.15): the run as CSV with a
  header (statement, definition version, ledger, period, currency type) and the
  "as of" footer (posting sequence per ledger, result hash, generated at and
  by, run id), rendered from the stored cells and the run's definition version,
  so it is byte-for-byte reproducible. Labels that a spreadsheet would read as
  formulas are prefixed with an apostrophe.
- Distribution schedules (FR-15.8): `GET /schedules`, `GET/PUT
  /schedules/{code}` with the duty `reporting.statement.schedule`. A schedule
  names a published definition (REPORT-0001 otherwise, checked in the save
  transaction), a five-field cron evaluated in an IANA time zone (expressions
  that can never fire, such as 30 February, are refused), 1 to 50 ledgers, the
  currency type, formats, 1 to 50 recipients and the attachment limit (1 to 25
  MB, default 20). Field errors are REPORT-0018. Replacing needs If-Match like
  drafts (428, 412). Reads return `nextRunAt` in the schedule's zone. Firing
  the schedules and sending email are not built (NOT-IMPLEMENTED.md).
- Row indent is validated (0 to 10).
- Web module: Download CSV and the run's warnings on the statement page, the
  schedule list (next run, paused) and the schedule form.

Slice 2 built:

- Derived hierarchies (FR-15.13, BR-15.13): `GET /hierarchies` list,
  revision-checked drafts (`GET/PUT /hierarchies/{code}`, ETag / If-Match),
  `POST /hierarchies/{code}:publish` with the members to classify, and
  `GET /hierarchies/{code}/members` for the materialized membership of a
  version. Leaves carry rules (key or attribute ranges, values, prefix
  wildcards such as `61*`). Publish refuses overlaps (REPORT-0014) and, for
  strict hierarchies, unassigned members (REPORT-0013); a non-strict publish
  writes one `hierarchy.unassigned.v1` per unassigned member to the outbox in
  the same transaction, addressed to the hierarchy owner. `strict` defaults to
  true for account and category hierarchies. Up to 12 levels and 20,000 nodes.
- `POST /definitions/{code}:retire` (BR-15.8): new runs are refused
  (REPORT-0001), retired versions and the runs that used them stay readable, an
  open draft is kept, and a later publish takes the next version number.
  Repeating the call succeeds. The definition list carries a `status`
  (published, retired or draft).
- Standard definitions `TB-STD`, `BS-STD`, `PL-STD` and `PL-FUNC` (FR-15.5,
  FR-15.14): `GET /standard-definitions` and `POST /standard-definitions:install`,
  which publishes version 1 of every code the tenant does not use yet, in one
  transaction, and skips the rest. Standard columns take the run's currency
  type. Trial balances are now cumulative like balance sheets (balances, not
  movements). The domain tests tie BS-STD, PL-STD and TB-STD out on a balanced
  ledger and show PL-FUNC equal to PL-STD when every expense line has a
  `FUNC_AREA` (AC-15.1 and AC-15.13 at domain level).
- Web module `finreport`: status column and the standard-definition install
  card on the definitions list, retire (with confirmation) in the designer, and
  the derived hierarchy list and editor (nodes, rules, publish with members,
  membership per leaf and unassigned members).

Slice 1 built:

- Definition model: rows (header, account_range, dimension_filter, total,
  calculation), columns (period expressions P, P-1, YTD, PY, PYTD, Q, FY, CALC),
  validation with expression references and cycle detection (FR-15.2, FR-15.3).
- Period expression evaluation and calendar handling (BR-15.1, BR-15.9).
- Statement evaluation over `reporting_balance` rows with a SHA-256 result hash
  that is reproducible for the same balances and definition (FR-15.6, BR-15.6).
  Balance sheets are cumulative from the opening period (period 0, which holds
  the brought-forward entries) of the column's fiscal year; income statements
  cover the column's periods and leave out closing entries (BR-15.2). Credit
  rows show `-net`; a calculation prefixed `raw:` uses stored net (BR-15.3).
  A column without a currency type uses the run's currency type.
- Calculation rows and CALC columns are stored as computed cells with no
  balance keys; every other cell's keys sum exactly to its value (BR-15.4).
- The run reads the balances and the ledger's HWM in one statement, so the
  recorded HWM is exactly the snapshot the cells reflect (BR-15.6).
- Cash-flow derivation with `CF_UNRECONCILED` and unclassified checks (BR-15.5).
- Definition list, drafts with revision-checked edits (ETag / If-Match),
  immutable published versions, publish, validate, JSON export and import with
  schema version, duplicate check and Idempotency-Key replay (FR-15.11, BR-15.8).
- Statement runs with Idempotency-Key replay, stored cells and balance keys,
  `statement.generated.v1` and `definition.published.v1` in the outbox.
- Run list (report library without outputs), drill level 1 (balance keys of a
  cell, sum checked) and the read-model lag view.
- Web module `apps/web/src/modules/finreport`: statement runs list, run form,
  statement grid with drill panel, definition list with JSON import, and the
  definition designer (rows, columns, validate, save draft, publish, export).

See `NOT-IMPLEMENTED.md` for what is deferred.

## Layout

```
cmd/finreport/          entrypoint (dbx.Connect, optional Migrate, httpx.Serve)
internal/domain/        definitions, expressions, periods, evaluation, cash flow, standard definitions, derived hierarchies (pure, no I/O)
internal/store/         Postgres store (pgx), tenant transactions, outbox writes
internal/api/           HTTP handlers under /v1/reporting
internal/events/        event types and payloads
internal/config/        REPORTING_* tunables, validated at startup
migrations/             SQL applied in order (0003 run checks, 0004 schedules, 0005 journal lines, 0006 audit trail); FS embedded in migrations.go
api/openapi.yaml        OpenAPI 3.1 for every endpoint
events/asyncapi.yaml    AsyncAPI 3 for every event
charts/finreport/       Helm chart
```

## Run

```
export DATABASE_URL=postgres://passage_migrator@localhost:5432/passage_reporting?sslmode=disable
export MIGRATE_ON_START=true   # dev only
go run ./cmd/finreport
```

Requests need the gateway headers `X-Passage-Tenant`, `X-Passage-User` and
`X-Passage-Duties` (see `platform/go/tenant`).

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./internal/store/...   # DB tests
```

Store tests are skipped without `PASSAGE_TEST_DATABASE_URL`. The row-level
security test also needs `PASSAGE_TEST_APP_DATABASE_URL` (a non-owner role) and
is skipped without it. The domain tests include a 200-row by 12-column
export/import round trip and a rapid property test (drill keys sum to the cell
value); the store tests cover export, re-import under a new code and identical
cells (AC-15.5) and hash reproducibility at the same HWM (AC-15.7). Contract
tests check that `api/openapi.yaml` lists exactly the registered routes and that
`events/asyncapi.yaml` lists every published event.

## Configuration

| Variable | Default | Allowed | Meaning (PRD §2.15.13) |
| --- | --- | --- | --- |
| `REPORTING_MAX_LAG_S` | 30 | 0-600 | Lag above which a run is blocked unless `allow_stale` |
| `REPORTING_LAG_ALERT_S` | 60 | 0-3600 | Seconds the lag stays above the gate before `lag.exceeded` is raised |
| `REPORTING_PAGE_SIZE` | 500 | 100-1000 | Drill level 2 page size |
| `REPORTING_CELL_KEY_LIMIT` | 50000 | 1000-200000 | Keys stored before a cell becomes a query spec |
| `REPORTING_RETENTION_CLOSED_YEARS` | 7 | 1-30 | Library retention for closed-period runs |
| `REPORTING_RETENTION_ADHOC_DAYS` | 90 | 7-730 | Ad-hoc run retention |
| `REPORTING_EMAIL_ATTACHMENT_LIMIT_MB` | 20 | 1-25 | Attach versus link |
| `REPORTING_SHARE_LINK_EXPIRY_DAYS` | 14 | 1-90 | Shared link lifetime |
| `REPORTING_PDF_CONCURRENCY_PER_POD` | 4 | 1-16 | Renderer queue width |
| `REPORTING_OPA_CACHE_SECONDS` | 60 | 5-600 | Dimension security cache |
| `REPORTING_TRACE_URL` | empty | http(s) URL containing `{traceId}` | Trace viewer link of a drill-down line (Tempo through Grafana) |

The values are validated and logged. Drill level 2 reads `REPORTING_PAGE_SIZE`
and `REPORTING_TRACE_URL`; the lag gate reads `REPORTING_MAX_LAG_S` and the lag alert
`REPORTING_LAG_ALERT_S`; large cells, the report library,
distribution, the PDF renderer and dimension security that use the others are
later slices (NOT-IMPLEMENTED.md).

## Duties

| Duty | Routes |
| --- | --- |
| `reporting.definition.maintain` | definition PUT, publish, retire, validate, export, import; standard definition list and install; hierarchy list, GET, PUT, publish and members |
| `reporting.statement.run` | `POST /runs` |
| `reporting.statement.view` | definition list and GET, run list and reads, cells, drill level 1, CSV, HTML and XLSX output, lag |
| `reporting.statement.schedule` | schedule list, GET and PUT |
| `ledger.journal.read` (wave 11, "View ledger transactions") | drill level 2 (`.../lines`) |
| `reporting.audit.view` ("View audit trail") | audit trail list and export |

## Error codes

Problem details use `type` `https://passage.dev/problems/<code>`.

| Code | Status | Meaning |
| --- | --- | --- |
| REPORT-0001 | 422 | Definition has no published version (`REPORT_DEFINITION_NOT_PUBLISHED`) |
| REPORT-0002 | 422 | Expression or reference invalid; body code mismatches the path |
| REPORT-0003 | 422 | Period does not exist in the supplied calendar (`REPORT_PERIOD_NOT_FOUND`) |
| REPORT-0008 | 422 | Cash flow run does not reconcile (`CF_UNRECONCILED`); the run is not stored |
| REPORT-0009 | 422 | Cash flow run has moved balance-sheet accounts without a cash-flow tag (`CF_UNCLASSIFIED_ACCOUNTS`); the run is not stored |
| REPORT-0016 | 500 | Rendering a run output failed for one format (`REPORT_RENDER_FAILED`); other formats are unaffected |
| REPORT-0015 | warning | Stored in a run's `checks[]`: PL-FUNC expense lines without a functional area |
| REPORT-0018 | 422 | Schedule field invalid (code, name, cron, time zone, ledgers, currency type, formats, recipients, attachment limit); body code mismatches the path |
| REPORT-0019 | 404 | Schedule not found |
| REPORT-0011 | 409 | Import of an existing code without `overwrite=true` (`REPORT_IMPORT_DUPLICATE`) |
| REPORT-0013 | 422 | Strict hierarchy has unassigned members at publish (`REPORT_HIERARCHY_UNASSIGNED`) |
| REPORT-0014 | 422 | A member matches two leaves at publish (`REPORT_HIERARCHY_OVERLAP`) |
| REPORT-0012 | 422 | Unsupported schema version on import (`REPORT_IMPORT_SCHEMA`) |
| REPORT-0020 | 404 | Definition or version not found; retire of a code that was never published |
| REPORT-0024 | 404 | Statement run not found (also for a run id that is not a UUID) |
| REPORT-0025 | 404 | Cell not found in the run; balance key position past the cell's keys, or a key of a computed cell (drill level 2) |
| REPORT-0028 | 422 | Journal line refused by the read model (date, period, amounts beyond six places, currency type, unsafe source link) |
| REPORT-0029 | 422 | Audit entry refused by the trail (unknown or reporting-only kind on ingest, missing actor, object or source event id, bad ledger or period) |
| REPORT-0031 | 404 | Ledger has no reporting data visible to the caller |
| REPORT-0032 | 422 | Ledger spans several legal entities, or legalEntityId differs from the ledger's owner |
| REPORT-0030 | 422 | Audit trail export matches more than 50,000 entries; narrow the filter |
| REPORT-0099 | 500 | Drill invariant failed: balance keys do not sum to the cell (never served) |
| conflict_version | 412 | Draft or schedule revision in `If-Match` is stale (409 when two creates race) |
| precondition_required | 428 | Draft or schedule exists and `If-Match` was not sent |
| idempotency_key_reuse | 422 | Idempotency-Key reused for a different run or import request |
| idempotency_key_in_flight | 409 | A concurrent request with the same Idempotency-Key won; retry to get the replay |
| COM-0001 | 422 | Shape violation in a definition or hierarchy (codes, names, labels, rules, members without id or key); hierarchy body code mismatches the path |
| COM-0002 | 400/422 | Malformed body, bad If-Match, bad list or line page limit, key or cursor, or run parameters (ledgerId not a UUID, calendar quarter not 1-4); audit trail filter malformed or not narrowed to a period or a window of at most 366 days |
| COM-0003 | 400 | `Idempotency-Key` header missing on `POST /runs` |
| REPORT-0026 | 422 | Definition has no rows or columns, or more than 2,000 rows or 60 columns; hierarchy has no nodes, more than 20,000 nodes, more than 12 levels or a cycle |
| REPORT-0027 | 404 | Hierarchy, its draft or a published version not found |
| COM-0404 | 404 | Unknown definition or hierarchy action; output format not rendered (pdf) |
| AUTH-0001 | 401 | Missing tenant |
| AUTH-0003 | 403 | Missing duty (authz middleware) |

## Dependencies

Go modules only: the platform module and what it already pulls in (chi,
pgx/v5, shopspring/decimal, google/uuid) plus `pgregory.net/rapid` for tests.

## Legal-entity scope (#241)

`statement_run`, `reporting_balance`, `reporting_hwm`, `reporting_journal_line` and
`audit_trail_entry` carry a nullable `legal_entity_id` (migration 0008) with a
RESTRICTIVE legal-entity policy; `statement_cell` follows its run. A row with no
entity is visible only to all-entity callers. A ledger's owner is read from its
`reporting_hwm` and `reporting_balance` rows under the caller's scope: runs,
journal lines and ledger audit entries take that entity, never the caller's.
`legalEntityId` on `POST /v1/reporting/runs` is only an assertion (422
REPORT-0032 when it differs). A ledger with no visible rows is 404 REPORT-0031
(a hidden ledger looks the same as an empty one); a ledger whose rows span
several entities is 422 REPORT-0032. The ledger consumer must write the
ledger's `reporting_hwm` row before its journal lines. Audit entries without a
ledger (definitions, hierarchies, schedules) stay tenant-level. Report
definitions, hierarchies and schedules are tenant-level configuration and are
not entity-scoped.

## Slice 16: scheduled statements and the report library

`finreport fire-schedules TENANT_ID...` (the chart's `statementSchedules`
CronJob, every five minutes, in process as
`system:finreport-statement-schedules`, no route and no authz seed) runs the
distribution schedules that came due: per schedule and ledger it runs the
definition for the month before the cron time, renders CSV, HTML and XLSX,
stores the files in `report_library_entry` (migration 0018, SHA-256 and
retention date) and writes `distribution.sent.v1` per file; a firing that fails
twice is recorded in `report_schedule_firing` and writes `statement.failed.v1`.
`GET /v1/reporting/library` lists the stored files, `.../library/{id}/content`
serves exactly the stored bytes, `GET /v1/reporting/schedule-firings` is the
firing history. Details and what is left: NOT-IMPLEMENTED.md, "Slice 16".
