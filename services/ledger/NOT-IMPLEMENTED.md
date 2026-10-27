# NOT-IMPLEMENTED — ledger (PSG-11, wave 11, after slice 3)

Every PSG-11 deliverable and acceptance criterion not yet fully built. Slice 2
added shared reason codes (D6, FR-11.15), reversal reason rules, negative
(storno) postings in the balance projection (BR-11.16), the reason-code API and
the reason-code and reverse screens. Slice 3 added account structures with
constraint rows, versions and activation (D1, FR-11.3), structure validation in
posting and `/validate`, the structure version in the combination hash (FR-11.4),
the per-ledger dimension value register with effective dates and suspension
(FR-11.2, `LEDGER_DIMENSION_INACTIVE`), their API, and the structure and
dimension value screens. Status is either `later slice of PSG-11`
(owned by this service, next slices) or `handoff` (needs another owner or a
path outside `services/ledger/` and `apps/web/src/modules/ledger/`).

## Deliverables (ticket items 1–10)

| # | Item | Status | Reason |
|---|---|---|---|
| D1 | Accounts, account structures with dimension validation rules | partial | Built: accounts, ledgers, atomic immutable idempotent posting; account structures (`account_structure`, migration 0007, RLS) with ordered segments, main-account filter, constraint rows (`under` conditions on `MAIN_ACCOUNT` or earlier segments; exact, range, `*`, `blank` tokens), `effectiveFrom` versions, draft → active → retired with active/retired immutable, `GET/PUT /ledgers/{ledger}/account-structures[/{id}]`, validation in posting and `/validate` (`LEDGER_STRUCTURE_NOT_FOUND`, `LEDGER_DIMENSION_REQUIRED`, `LEDGER_DIMENSION_INVALID`), and the web list and editor. Not built: advanced rules (FR-11.16, D7); re-validating the 1,000 most recent combinations on activation (FR-11.16); the "validate sample" action of the structure screen (users can call `POST /validate`); structure definition export (JSON/XLSX, PRD §2.11 exports); `If-Match`/`ETag` on structure `PUT` (see the API table). A ledger with no active structure stays open (format checks only) so ledgers keep posting while structures are designed. |
| D2 | Financial dimensions (up to 20 per ledger), default dimensions on master data, dimension validation | partial | Built: the ledger holds the attribute names (max 20); a per-ledger value register (`dimension_value`, migration 0007, RLS) with name, `activeFrom`/`activeTo` and `suspended`; `GET/PUT /ledgers/{ledger}/dimension-values[/{attribute}/{value}]`; posting refuses a registered value that is suspended or out of dates (`LEDGER_DIMENSION_INACTIVE`); web list and form. Not built: a `DimensionAttribute` entity with its backing entity binding (department, cost center, site, project, customer, vendor, item) and value sync from those entities' events (wave 7/8 producers, handoff); a closed custom value list (unregistered values are format-checked only, not refused); default dimensions on party/product/site/account and the precedence merge (FR-11.2, later slice of PSG-11). |
| D3 | Posting API with source linkage and reversal | partial | `POST /journal-entries` (source object, idempotency, 422 violations) and reversal (optional later date, never before the original; reversing a reversal re-instates) with reason codes (FR-11.15) are built. The reinstatement flag (`allow_reinstate`) is not. |
| D4 | Balance projection rebuilt from the journal | partial | Projection is upserted in the posting transaction; rebuild recomputes and reports `rowsDiffering`. BR-11.7 shadow table, chunking per period, catch-up loop and 2-second advisory swap are not built; rebuild writes in place. |
| D5 | Journal workspace: manual and recurring journals, templates, approval workflow, attachments | later slice of PSG-11 | Batches, names, status machine (BR-11.10), Temporal approval (wave 4) and attachments (FR-11.9–11.11) are not built. The web module contains no batch screens. |
| D6 | Reason codes with required reversal reasons and negative-posting reversal | partial | Built: tenant `reason_code` catalogue (migration 0006, RLS), `GET/PUT /api/v1/ledger/reason-codes[/{code}]`, ledger parameters `reversalReasonRequired`/`negativePostingEnabled`, reason rules on reversal, reason fields on the reversing entry and `journal.reversed`, storno lines (BR-11.16) in the projection and its rebuild, and the web list/edit and reverse screens. Not built: see the FR-11.15 row. |
| D7 | Advanced rule structures, default description templates, voucher attachments with document types and retention | later slice of PSG-11 | FR-11.16, FR-11.17 (`%n` templates, `LEDGER_DESCRIPTION_TRUNCATED`), FR-11.18 (document types, retention, `LEDGER_ATTACHMENT_RETAINED`) not built. Attachment storage waits on the wave 5 document framework (handoff). |
| D8 | Open-item managed G/L accounts, clearing, residuals, activation runs, auto-clearing rules, tolerance groups | later slice of PSG-11 | FR-11.19 and FR-11.20 are not built. `open_item_managed` is not on the account yet. The `dev.passage.ledger.open-item.cleared.v1` event is not emitted. Wave 12 CurrencyPolicy is needed for clearing FX (handoff). |
| D9 | User-defined validation rules (CEL), derivation/substitution strategy engine, functional area `FUNC_AREA` | later slice of PSG-11 | FR-11.21 and FR-11.22 are not built. The posting pipeline runs only the stages listed in the README; default descriptions, derivation, splitting and validation rules are absent. CEL evaluation needs a library decision (not in the allowed dependency list). |
| D10 | Document splitting and audit-logged dimension realignment | later slice of PSG-11 | FR-11.23 (`LEDGER_SPLIT_NO_SOURCE`) and FR-11.24 (`dev.passage.ledger.realignment.completed.v1`) are not built. |

## Functional requirements not built in slice 1

| Requirement | Status | Reason |
|---|---|---|
| FR-11.1 ledgers per legal entity, shared charts, `MainAccountLedgerSetting` (SKB1), 50,000 accounts / 500 charts caps | partial | Shared chart and account attributes are built. The company-specific setting, caps and the legal-entity-to-ledger model wait on wave 6 legal entities (handoff). |
| FR-11.3 account structure validation and version effective dates | partial | Built (see D1). Advanced rules are FR-11.16. |
| FR-11.4 combination id over `(structure_version_id, account, dims…)` | partial | Built: a line validated under a structure is hashed over `(structure version id, chart, account, attribute=value…)` and `dimension_combination.account_structure_id` records the version. Lines on a ledger without structures keep the structure-less hash. Values are hashed by text, not by a `dimension_value_id`, because values are not yet entities with ids (D2). |
| FR-11.5 `Idempotency-Key` contract | partial | Trimmed keys, replays (200, original `traceId`), conflicts (409) and 422 without caching are built. Concurrent duplicates on the same ledger serialize on the ledger row lock and replay; the same key racing on two ledgers of one legal entity returns 409 `LEDGER_IDEMPOTENCY_IN_PROGRESS` with `Retry-After: 1`. Expired records (400 days) are treated as new on the next use, but the `idempotency-purge` job that deletes them in bulk is not built. |
| FR-11.5 posting pipeline stages | partial | Built: schema, ledger resolution, account existence/postability/suspension/recon, currency, scale, empty lines, dimension format, balance, combination interning, sequence, insert, balance upsert, outbox. Not built: period status (wave 14; `LEDGER_PERIOD_CLOSED`, `LEDGER_PERIOD_ON_HOLD`, `LEDGER_PERIOD_NOT_FOUND`) and fiscal period resolution (wave 6; `fiscal_period_id` is a `YYYY-MM` `period_key` for now), rounding and FX (wave 12), and advanced rules. |
| FR-11.6 gap-free `posting_sequence` from the wave 5 number sequence service | partial | Sequence and voucher are allocated gap-free from `ledger.next_sequence` inside the posting transaction (BR-11.5). The kernel `sequence` package is not available yet, so the allocation is local. Handoff to platform owner for the shared service; `check_voucher_used` is not built. |
| FR-11.6 append-only enforcement | built | Triggers reject UPDATE, DELETE and TRUNCATE on `journal_entry`/`journal_line` for every role (0003); `passage_app` holds SELECT and INSERT only (0005, applied when the role exists). Store test `TestPostgres_JournalIsAppendOnly` asserts both. |
| FR-11.7 balance fields and period-to-date | partial | `debit`, `credit`, `net`, `line_count`, `last_posting_sequence` are built. Opening balances in period 0 (wave 14) are not. |
| FR-11.8 reversal: once-only, line links, reinstatement | partial | Once-only (`LEDGER_REVERSAL_ALREADY_REVERSED`), line links, reversal date ≥ original (`LEDGER_DATE_INVALID`), reason codes (FR-11.15) and reinstatement by reversing the reversal are built. `allow_reinstate` (reversing a reversal is allowed by default here) is not a flag yet. |
| BR-11.1 balance re-checked inside the database | built | Deferred constraint trigger `journal_entry_balanced` re-sums each entry's lines at commit. |
| BR-11.2 statistical zero-amount lines (`quantity` ≠ 0) | later slice of PSG-11 | `quantity`/`unit` are not on `journal_line` yet; a zero line is accepted only with `text`. |
| BR-11.8 `allow_recon_accounts` journal-name override | later slice of PSG-11 | Manual journals (source `ledger`/`manual_journal`) are refused on reconciliation accounts; the per-journal-name override waits on journal names (D5). |
| `amount_rpt` and `Balance.currency_type` txn/rpt rows | later slice of PSG-11 | Wave 11 is single-currency: lines store `amount_txn` = `amount_acct`, and the projection keeps `acct` rows only. Reporting currency arrives with wave 12. |
| FR-11.15 shared reason codes | partial | Built: catalogue, CRUD API, ledger-level `reversal_reason_required` and `negative_posting_enabled`, active / `applies_to` ledger / `requires_comment` / `alternative_posting_date_allowed` checks, storno lines, reason fields on the entry and on `journal.reversed`. Not built: the journal-name level `reversal_reason_required` (waits on journal names, D5); the parameters live on the ledger rather than the legal entity (PRD §2.11 parameter table) until legal-entity parameters exist; translatable `name` (single-language until the wave 5 translation service, handoff); the "Reason code usage" report; reason codes on clearing reset and realignment (D8, D10). Other modules (bank, customer, vendor, fixed asset, counting) read the shared table in their own waves. |
| FR-11.12 source object and `source_line_ref` | built | — |
| FR-11.13 opening balances via the migration framework | handoff | Needs wave 10 migration framework and the conversion-suspense account; the demo chart and opening balances (deploy) are also not loaded. |
| FR-11.14 query API: line query (keyset), balance filters by dimension, `group_by` | partial | Trial balance over account and period range is built. Line query with keyset cursor, `dim.{attr}` filters, `group_by`, and the account × dimension inquiries are not built. |
| FR-11.16 – FR-11.24 | later slice of PSG-11 | See D7–D10. |

## API and contract items

| Item | Status | Reason |
|---|---|---|
| Ledger `dimension-combinations/resolve`, `journal-batches` lifecycle, `reason-codes`, `advanced-rules`, `description-templates`, `open-items`, `clearings`, `accounts:activate-open-items`, `auto-clearing*`, `validation-rules`, `derivation-strategies`, `lookup-tables`, `splitting-rules`, `realignments` | later slice of PSG-11 | Not built. Only the routes in `api/openapi.yaml` exist. |
| `GET/PUT` account and ledger configuration with `If-Match` (428/412 `conflict_version`) and `ETag` on reads | later slice of PSG-11 | The current `PUT` account, account-structure and dimension-value routes and their GETs do not use `If-Match`/`ETag`. Optimistic concurrency needs a row version column that is not in the migrations yet. Structure versions are protected differently: an active or retired version cannot be changed (409 `LEDGER_STRUCTURE_LOCKED`), so concurrent edits can only race on drafts. |
| `GET /v1/ledger/openapi.yaml`, `/asyncapi.yaml`, `/error-codes` (kernel-standard endpoints) | handoff | Served by the kernel for every service; not in this service's code. |
| `RateLimit-*`, `Retry-After`, `X-Passage-Api-Build`, `Deprecation`/`Sunset` headers | handoff | Gateway (edge-identity) and kernel responsibilities. |
| Problem `serviceCode` (`LED-xxxx`) and `errors[]` catalogue | later slice of PSG-11 | Ledger problems carry the `LEDGER_*` code; the `serviceCode` mapping is not yet in `api/openapi.yaml` or the README. |
| `dev.passage.ledger.batch.status-changed.v1`, `account.changed.v1`, `rules.changed.v1`, `open-item.cleared.v1`, `realignment.completed.v1` | later slice of PSG-11 | Their producing features are not built, except that `account.changed.v1` (PRD trigger: account or structure change) is not yet emitted by the account `PUT` nor by structure activation or retirement. `journal.posted` carries a single-currency `totals` object instead of `txn[]/acct/rpt` (multi-currency is wave 12). |
| Authorization against OPA (`allow`, `legalEntityScopes`, obligations) | handoff | The service uses the kernel's local `authz.RequireDuty` (DutyPolicy). Wave 2 platform owns the OPA decision point. Legal-entity scope via `passage.legal_entity_scopes` is set by the kernel's `dbx.WithTenantTx`. |

## Readiness, operations and deployment

| Item | Status | Reason |
|---|---|---|
| Main-port `GET /readyz` with database check | handoff | The kernel's router registers a static `/readyz` on the main port, which the ledger cannot override without a panic. The database-backed readiness check is served on the admin port (`:9090/readyz`), and the chart's readiness probe points there. Kernel owner to allow service-supplied readiness. |
| Helm: HPA 3–12 on CPU 60%, PodDisruptionBudget 2 | handoff | These belong to the umbrella chart under `deploy/` (not owned by this wave). |
| CloudNativePG cluster `ledger-db` (3 instances), `DATABASE_URL` secret | handoff | `deploy/` owner. The chart references the secret `ledger-db-app` by name. |
| Kafka topics `ledger.journal`, `ledger.batch`; Debezium outbox connector | handoff | Eventing backbone (`deploy/`) owner. Topic names in `events/asyncapi.yaml` use the `passage.ledger.*` convention, which must be reconciled with the topic list in the wave spec. |
| Atlas expand/contract migrations and Argo PreSync migration Job | handoff | Migrations are SQL files applied by `dbx.Migrate`. Stage/prod migration Job lives in `deploy/`. |
| Monthly `create-partitions` job; `journal_line` partitioned by fiscal year | later slice of PSG-11 | Tables are not partitioned yet; partitioning needs the fiscal calendar (wave 6). |
| Scheduled jobs: `balance-verify` nightly, `recurring-journal-generate`, `gl-auto-clear`, `idempotency-purge`, `dimension-value-reconcile`, `attachment-retention-sweep`, `open-item-aging-snapshot` | later slice of PSG-11 | Temporal workflows; the balance rebuild exists only as an API call. |
| Feature flags `ledger.recurring_journals`, `ledger.advanced_rules` | later slice of PSG-11 | Depend on features not yet built. |
| Image build pipeline and cosign signing | handoff | CI owner (`deploy/`, `.github`). The service's own `Dockerfile` is built (same layout as `services/org`). |
| Pact contract tests and k6 throughput test | later slice of PSG-11 | Performance gate runs on the dev cluster; not in slice 1. |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 1M random balanced entries, 64 workers, TB sums to zero, projection equals re-aggregation | partial | Balanced-by-construction property tests (domain, `rapid`) and a scaled-down concurrent posting test with trial-balance check exist. The 1M-entry run is a performance gate (later slice). |
| AC2 negative tests: unbalanced, closed period, invalid dimension | partial | Unbalanced, account rules, scale, empty lines, dimension attribute/format, structure not found, dimension required/invalid/inactive and closed period (before `firstOpenDate`) are covered in domain table tests and store tests (`TestPostgres_PostingValidatesAgainstStructures`, `TestPostgres_DimensionValueRegister`). Fiscal-period status is not built (see above). |
| AC3 duplicate `Post` with the same key is a no-op | built | Store test covers replay returning the same entry. |
| AC4 throughput 5,000 lines/s, p99 < 200 ms | later slice of PSG-11 | Performance test on the dev cluster; needs the deployed stack. |
| AC5 reversal is an exact negating entry linked to the original | built | Store test asserts swapped D/C, reversal line links and single reversal. |
| PRD AC-11.4 6010 requires DEPT under the active structure → 422 `LEDGER_DIMENSION_REQUIRED` with `line_index` | built | `TestStructure_AC11_4` (domain) and `TestPostgres_PostingValidatesAgainstStructures` (store). |
| PRD AC-11.11 reason required on reversal; storno restores turnover | partial | Built at ledger level: `TestPostgres_ReversalRequiresReasonWhenConfigured` (422 `LEDGER_REASON_REQUIRED`) and `TestPostgres_NegativeReversalRestoresTurnover` (trial balance debit and credit back to their pre-posting values, rebuild agrees, re-instatement restores them). The journal-name trigger waits on D5. |
| AC6 every deliverable has code or a reason | built | This file. |
| AC7 `go vet ./... && go test ./...`; web `vitest` and `tsc --noEmit`; OpenAPI/AsyncAPI match code; chart lints | built | All pass. Store tests skip without `PASSAGE_TEST_DATABASE_URL`; at review they were run against Postgres 16 with `PASSAGE_TEST_APP_DATABASE_URL` as `passage_app`. `internal/api/openapi_test.go` fails on route or duty drift from `api/openapi.yaml`; `internal/events` tests that every event type is in `events/asyncapi.yaml`. |

## Test plan items

| Item | Status | Reason |
|---|---|---|
| Fixtures `coa-demo`, `dims-demo`, `structures-demo`, `entries-random-1m`, `entries-negative-120`, `opening-balances-demo` under `testdata/` | later slice of PSG-11 | Fixtures depend on structures, dimensions and the AX extract. Not present in slice 1. |
| Error-code table (120 cases) | later slice of PSG-11 | Only the codes raised today are covered. |
| Idempotency contract tests (OpenAPI + Pact) | later slice of PSG-11 | Pact not set up; behaviour is covered by store tests. |
| Rebuild equivalence after concurrent load | later slice of PSG-11 | Needs the shadow-table rebuild (D4). |
| Journal workspace E2E (Playwright + axe) | later slice of PSG-11 | No journal workspace yet; the web module has the trial balance, the entry screen with its reverse form, the reason-code screens and the account structure and dimension value screens, covered by vitest with an axe AA check, not by Playwright. |
| Append-only SQL test for app role and owner | built | `TestPostgres_JournalIsAppendOnly` (needs the test database). |
| Reconciliation checks: subledger ⇄ journal, projection ⇄ events | later slice of PSG-11 | Subledgers arrive in waves 16+; projection ⇄ events needs the event consumer. |
