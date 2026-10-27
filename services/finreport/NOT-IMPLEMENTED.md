# NOT-IMPLEMENTED: PSG-15 (wave 15), slice 1

Every deliverable bullet of PSG-15, and every acceptance criterion that slice 1
does not fully meet, is listed here. Status is either **later slice of PSG-15**
(work this service still owes) or **handoff** (needs another owner or a
platform change outside the wave's owned paths).

## Deliverables (PSG-15 "Deliverables" 1-7)

| # | Deliverable | Built in slice 1 | Status | Reason |
| --- | --- | --- | --- | --- |
| 1 | Report designer: row/column/tree over account structures and dimensions | Rows (header, account_range, dimension_filter, total, calculation), columns (P, P-n, YTD, PY, PYTD, Q, FY, CALC), validation via `:validate`, JSON import/export, definition list. Web designer form (`apps/web/src/modules/finreport`): rows and columns with add, remove and move up/down, validate, save draft with If-Match, publish, export, JSON import. | Partial. Reporting trees, row-set/column-set/tree endpoints, split-pane layout, live preview, drag reorder, multi-select indent and "insert total of selection" are **later slice of PSG-15**. | Slice 1 is the evaluation core, the API and a form-based designer; the tree model and the richer editor follow. |
| 2 | Standard statements: TB, BS, P&L, CF indirect, comparative, consolidated-by-entity | Evaluator and CF derivation in the domain; no shipped standard definitions or entity-column view | **later slice of PSG-15** | Needs seeded `TB-STD`, `BS-STD`, `PL-STD`, `CF-STD`, `TB-DIM` definitions and the entity-column run mode. CF derivation (`cashflow.go`) is not yet called from the run endpoint. |
| 3 | Drill-down: statement, balance, journal lines, source document, trace id | Drill level 1 (balance keys of a cell, sum checked, REPORT-0099 on mismatch; computed cells answer `computed: true`), with the statement grid and drill panel in the web module | Level 2 (lines, keyset pages, `sum_of_lines`, `filtered_amount`), `source_uri`, trace id links to Tempo are **later slice of PSG-15**. Line data is a **handoff** to the ledger line API (wave 11). | The journal line API is not available to this service yet. |
| 4 | Scheduled distribution (PDF/XLSX/email), row-level security on dimensions | Nothing | **later slice of PSG-15** for schedules, library, share links and retention. **Handoff** for SMTP (wave 4), the Chromium renderer sidecar (deploy/ + platform), and OPA dimension-value decisions (wave 2 `authz` Policy). | Requires the outputs pipeline, the email transport and the OPA decision for dimension values. |
| 5 | Audit trail report: who posted, approvals, reversals, period overrides | Nothing | **later slice of PSG-15**; **handoff** to wave 5 (audit store) and waves 11, 13, 14 (`journal.posted`, approval, period events). | Source events and audit rows are owned by other waves. |
| 6 | Derived financial hierarchies (main account categories, dimension values) | Nothing | **later slice of PSG-15** (tables, membership, publish checks REPORT-0013/0014, `hierarchy.unassigned`). **Handoff** to wave 11 for `MainAccountCategory` and `MainAccount.category_id`. | The category model is owned by wave 11. |
| 7 | P&L by function of expense (PL-FUNC) | Nothing | **later slice of PSG-15**; **handoff** to wave 11 for the `FUNC_AREA` dimension (FR-11.22). | Depends on the dimension. |

## Functional requirements

| FR | Status | Reason |
| --- | --- | --- |
| FR-15.1 read model fed by `journal.posted/reversed/balance.rebuilt/year-end.closed` | **later slice of PSG-15** for the consumer, HWM upkeep and backfill; **handoff** to wave 11 for the events and to the kernel for consumer inbox helpers. Slice 1 reads `reporting_balance` and `reporting_hwm` as they are, and tests seed them directly. | No consumer exists yet. |
| FR-15.1 lag SLO and nightly zero-diff verify against `ledger.Balance` | **later slice of PSG-15** | Needs the consumer above and the ledger balance API. |
| FR-15.2 max 2,000 rows, expression validation on save | Done (`Validate`: limits, references, cycles, syntax; division by zero renders blank and is tested). Row formats bold, indent, print zero and page break are stored; underline is not modelled, and page break has no effect until PDF rendering exists (**later slice of PSG-15**). | Rendering outputs are not built. |
| BR-15.3 sign presentation and `raw:` calculations | Done (credit rows show `-net`; `raw:` uses stored net). | n/a |
| FR-15.3 columns: currency type, ledger override, variance/percent, comparative templates | Period expressions, CALC (variance and percent as expressions such as `C2-C1` or `(C2-C1)/C1*100`) and per-column currency type (empty = the run's) done. Currency filter within a currency type, column dimension filter, column format, `ledger_code_override` (BR-15.10, REPORT-0005), comparative templates and variance presets are **later slice of PSG-15**. | Multi-ledger columns need the chart-of-accounts compatibility check; the read model has no transaction currency code yet. |
| FR-15.4 reporting trees and per-unit runs with roll-up | **later slice of PSG-15**; **handoff** to wave 6 for dimension hierarchies | Depends on wave 6 hierarchies. |
| FR-15.5 standard definitions and `is_closing_entry` / `is_opening_entry` handling | Closing entries are excluded from income statements (`closing_net`) and balance sheets start at the fiscal year's opening period 0 (brought-forward entries), per BR-15.2. Seeded standard definitions, TB-STD opening/debit/credit/closing columns (the read model holds net only) and the consolidated-by-entity view are **later slice of PSG-15**. | Requires seeded definitions (see deliverable 2) and debit/credit in the read model. |
| FR-15.6 run outputs: HTML, PDF, XLSX, CSV; `outputs[]` is empty | Runs and cells stored with SHA-256 result hash and HWM (done); the interactive HTML statement is the web module's run page. File outputs and renderers are **later slice of PSG-15**; PDF sidecar is a **handoff** to deploy/. | Rendering is out of slice 1. |
| FR-15.6 performance: 2,000 x 12 over 200k balances < 20 s | Not measured | Needs a benchmark fixture under `testdata/` (later). |
| FR-15.6 queued / running / failed states and 202 + `statusUrl` for long work | Runs are synchronous; `status` is always `completed`. **later slice of PSG-15** | Async execution needs the job runner (platform). |
| FR-15.7 drill level 1 | Done (`GET .../balances`, exact sum check) | n/a |
| FR-15.7 drill level 2, source link, trace id | **later slice of PSG-15** (see deliverable 3) | Journal line API is a handoff. |
| FR-15.8 scheduling and distribution | **later slice of PSG-15**; SMTP is a **handoff** to wave 4 | See deliverable 4. |
| FR-15.9 row-level security on dimensions | **later slice of PSG-15**; OPA dimension decision is a **handoff** to wave 2 | `authz.RequireDuty` checks duties only. |
| FR-15.10 audit trail report and hashed export | **later slice of PSG-15**; **handoff** to wave 5 | See deliverable 5. |
| FR-15.11 definition import/export as JSON | Done: `GET .../export`, `POST .../import` (schema v1, REPORT-0011 when the code has a draft or a published version and `overwrite` is not set, REPORT-0012 schema, optional Idempotency-Key stored in `idempotency_keys`). Store test exports, re-imports under a new code and compares cells. | n/a |
| FR-15.12 MR and FSV converters | **later slice of PSG-15**; the migration packs belong to the migration-framework owner (**handoff**). | Converters need the MR and FSV extracts. |
| FR-15.13 derived hierarchies | **later slice of PSG-15** (see deliverable 6). | n/a |
| FR-15.14 PL-FUNC | **later slice of PSG-15** (see deliverable 7). | n/a |

## Data, events, jobs

| Item | Status | Reason |
| --- | --- | --- |
| `reporting.max_lag_seconds` gate, `allow_stale`, REPORT-0004 | **later slice of PSG-15** | Needs the ledger HWM to compare against. `GET /lag` reports the reporting HWM only. |
| Fiscal calendar used for period placement | Slice 1 takes the calendar in the run request body; the web run form generates whole fiscal years (opening period 0 plus N periods, quarters spread evenly). **Handoff** to wave 14 (period calendar API). | No calendar service exists yet. |
| BR-15.11 large cells (> 50,000 keys become a query spec) | **later slice of PSG-15** | Keys are stored in full for now. |
| Events: `statement.failed`, `distribution.sent`, `lag.exceeded`, `hierarchy.unassigned` | **later slice of PSG-15** | Their producers are not built yet. |
| `statement.generated.v1` and `definition.published.v1` | Done: written to the outbox in the same transaction. | n/a |
| Jobs: `reporting-balance-consumer`, `reporting-balance-verify`, `statement-schedules`, `library-retention-sweep`, `audit-completeness-check`, `hierarchy-membership-refresh` | **later slice of PSG-15** | Depend on the consumer and the schedule model. |
| `POST` publish and validate: `Idempotency-Key` | Not taken. Validate is stateless. Publish is safe to repeat: a second call finds no draft and answers 404, so it never creates a second version. Import and run creation persist the key (`idempotency_keys` table and `statement_run.idempotency_key`). | PRD §2.15.5 asks for a key on import and runs only. |
| Retire endpoint, row-set / column-set / tree endpoints | **later slice of PSG-15** | Not needed by the first evaluation path. The definition list (`GET /definitions`) and run list (`GET /runs`, newest first, no cursor) are built. |
| Run list keyset pagination and report library filters (entity, period) | **later slice of PSG-15** | `GET /runs` returns at most 200 runs, filterable by definition code. |
| Ad-hoc balances query `GET /reporting/balances` | **later slice of PSG-15** | Needs the dimension security filter. |
| Audit view `AuditTrailEntry` | **later slice of PSG-15** | See deliverable 5. |
| Parameters `reporting.*` (tenant, legal-entity and definition scopes) | **later slice of PSG-15** | Parameter store is part of the platform (wave 1/2). |

## Platform and deployment

| Item | Status | Reason |
| --- | --- | --- |
| Kernel endpoints: `/metrics` on the admin port, `/v1/reporting/openapi.yaml`, `/asyncapi.yaml`, `/error-codes`, `X-Passage-Api-Build`, `RateLimit-*` headers | **handoff** to the platform kernel (wave 0) | Not in `platform/go`. The service serves `/healthz` and `/readyz` from `httpx.NewRouter`. |
| JWT validation, OPA decision point | **handoff** to the platform (wave 2). Slice 1 trusts the gateway headers through `tenant.Middleware` and checks duties locally. | Wave 2 owns OPA and the JWT path. |
| OTel, structured logs with `correlationId` | **handoff** to the platform kernel | Not wired in slice 1. The service uses the default JSON `slog` handler. |
| Helm: ServiceMonitor template (value toggle exists, no template) | **later slice of PSG-15** | The toggle is in `values.yaml`. |
| Helm: `REPORTING_*` env vars are set in the deployment but not read yet | **later slice of PSG-15** | Config is read in later slices. |
| `reporting-db` CloudNativePG cluster, read replica, Kafka consumer group `reporting-balances` | **handoff** to deploy/ (deployment-cicd) | Cluster manifests are not in this wave's paths. |
| Renderer sidecar (Chromium) | **handoff** to deploy/ and **later slice of PSG-15** | See FR-15.6. |

## Acceptance criteria

| AC | Status | Reason |
| --- | --- | --- |
| AC1 statements tie to the trial balance to the cent (demo dataset) | **later slice of PSG-15** | Needs the `demo-fy2026-closed` fixture from waves 11-14 and the standard definitions. The store test seeds three balances only. |
| AC2 designer round-trips a 200-row definition through JSON export/import | Done. `internal/domain/designer_roundtrip_test.go` covers 200 x 12 `Marshal`, `Parse` and identical cell values; `TestExportImportRoundTripAndReproducibleHash` (store) exports, imports under a new code, publishes and runs both with identical cells. | n/a |
| AC3 drill sum property over random cells | Done for the evaluator (rapid, 20 rows x 12 columns, random balances). Line-level sum (level 2) is **later slice of PSG-15**. | Level 2 is not built. |
| AC4 phase exit sign-off by finance stakeholders | **handoff** to the finance stakeholders and the wave 15 issue | Process, not code. |
| AC5 every deliverable has code or a row here | Met by this file. | n/a |
| AC6 `go vet` and `go test` in every Go module; `vitest` and `tsc` in `apps/web`; OpenAPI and AsyncAPI match; chart lints | Go vet and tests pass, chart lints, web tests and tsc pass. Contract tests check OpenAPI against the registered routes and AsyncAPI against the published events. The page tests run the in-repo axe AA checks; Playwright end-to-end tests are **later slice of PSG-15**. | Keyboard cell-by-cell navigation (arrow keys) and breadcrumb drill panels need drill level 2. |
| AC-15.4 AP-sourced line opens its document and trace | **later slice of PSG-15** | Drill level 2 is not built. |
| AC-15.5 export then re-import under a new code gives equal cells | Done (store test, see AC2). | n/a |
| AC-15.6 OPA dimension security | **later slice of PSG-15** | See FR-15.9. |
| AC-15.7 `result_hash` reproducible at the same HWM | Done for stored runs: two runs at the same HWM hash equally and a later posting changes hash and HWM (store test). Postings arriving during a run cannot skew it because balances and HWM are read in one statement. The consumer that moves the HWM is **later slice of PSG-15**. | n/a |
| AC-15.8 nightly schedule with PDF/XLSX email | **later slice of PSG-15** | See deliverable 4. |
| AC-15.9 reopen and re-close appear in the audit trail | **later slice of PSG-15** | See deliverable 5. |
| AC-15.10 2,000 x 12 over 200k balances < 20 s | Not measured | See FR-15.6 performance. |
| AC-15.11 phase exit comparison to AX and SAP expectations | **handoff** to finance stakeholders | Process. |
| AC-15.12 derived hierarchy strict publish and `REPORT_HIERARCHY_UNASSIGNED` | **later slice of PSG-15** | See deliverable 6. |
| AC-15.13 PL-FUNC equals PL-STD net | **later slice of PSG-15** | See deliverable 7. |

## Notes

- `REPORT-0099` (drill invariant) is a slice-local code. It is not yet in the
  message catalog (PRD §2.15.18) and should be added there with the owner.
- Codes `COM-0001`, `COM-0002`, `COM-0003`, `COM-0404` and `AUTH-0001` are used
  for shape, body, header and auth errors. They match the shared catalog
  pattern. Confirm them against the platform error catalog.
