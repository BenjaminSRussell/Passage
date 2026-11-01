# consolidation (wave 40, PSG-40, slices 1-3 and stored runs)

Financial consolidation of a legal-entity group: IAS 21 translation with CTA,
the elimination entries E1–E6 with the non-controlling interest (NCI) share,
ownership validation, unrealized profit per IC receipt layer, the equity
method and divestitures, two-sided IC elimination, the unit validation rules
of the monitor, reclassification and regrouping rules and top-side group
journals (PRD 7.40).

## What slice 1 delivers

Pure domain rules plus two stateless HTTP previews and the web workspace.
Nothing is persisted, no run is versioned and no event is published yet.

| Area | Code | Status |
|---|---|---|
| Elimination entries E1–E5 with NCI, FR-40.9 worked example (1,032,952.00 per side) | `internal/domain/eliminate.go` | built, exact-amount test (AC-40.7) |
| Balance check per rule, CNS-0903 | `internal/domain/eliminate.go` | built, tests |
| Ownership validation (never above 100.00 % on any date), CNS-0904 | `internal/domain/ownership.go` | built, tests |
| IAS 21 translation and CTA with NCI share, FR-40.8 / BR-40.6, AC-40.6 (38,000.00) | `internal/domain/translate.go` | built, unit tests |
| Elimination preview `POST /v1/consol/eliminations:preview` (duty `ConsolidationRun`) | `internal/api` | built, tests |
| Translation preview `POST /v1/consol/translations:preview` (duty `ConsolidationRun`) | `internal/api` | built, tests |
| Counters on `/metrics` (admin port) | `internal/api` | built, tests |
| Web workspace: `/consolidation`, `/consolidation/eliminations`, `/consolidation/translation` | `apps/web/src/modules/consolidation` | built, vitest |

Browser calls go to `/api/v1/consol/...`; the web BFF rewrites them to the
`/v1/consol/...` paths above and sets `X-Passage-Tenant` and
`X-Passage-Duties`.

## What slice 2 adds

Pure rules for profit in inventory, investments, two-sided IC elimination
and unit validation, each behind a stateless preview and a web page. Still
nothing is stored or published.

| Area | Code | Status |
|---|---|---|
| Unrealized profit per IC receipt layer (remaining quantity × (Legal − Group unit cost)), seller-margin fallback when the group cost is unknown with the CNS-0908 warning, each layer listed with its id and basis, FR-40.16 / BR-40.7, AC-40.12 (800.00) | `internal/domain/layers.go` | built, unit tests |
| Profit-in-inventory preview `POST /v1/consol/profit-in-inventory:preview` | `internal/api/slice2.go` | built, tests |
| Equity method (share of profit, dividends, closing carrying amount; a share of losses stops at a zero carrying amount, IAS 28.38), FR-40.18, AC-40.14 (60,000.00 / 340,000.00) | `internal/domain/equity.go` | built, unit tests |
| Equity-method preview `POST /v1/consol/equity-method:preview` | `internal/api/slice2.go` | built, tests |
| Divestiture gain = proceeds − (net assets + goodwill − NCI) + CTA recycled, FR-40.18 | `internal/domain/equity.go` | built, unit tests |
| Divestiture preview `POST /v1/consol/divestitures:preview` | `internal/api/slice2.go` | built, tests |
| Change-of-control guard against step acquisition, CNS-0910 | `internal/domain/equity.go` | built, unit tests (no endpoint until investments are recorded) |
| Two-sided IC elimination per partner pair, difference up to `consol.icThreshold` (default 1,000.00) to the IC difference account, above it CNS-0905 and the task in Error, BR-40.15 | `internal/domain/ic_difference.go` | built, unit tests |
| IC difference preview `POST /v1/consol/ic-differences:preview` | `internal/api/slice2.go` | built, tests |
| Seeded unit validation rules (balance sheet to 0.01, retained earnings roll within 1.00, IC partner present as a warning), CNS-0906 per failed rule, unit task status, group translation Blocked by an Error or Blocked unit, FR-40.20, AC-40.15 | `internal/domain/validation.go` | built, unit tests |
| Validation preview `POST /v1/consol/validations:preview` | `internal/api/slice2.go` | built, tests |
| OpenAPI matches the mounted routes and duties | `internal/api/contract_test.go` | built, test |
| Web pages: `/consolidation/profit-in-inventory`, `/consolidation/ic-differences`, `/consolidation/investments`, `/consolidation/validation` | `apps/web/src/modules/consolidation` | built, vitest |

All slice-2 previews need duty `ConsolidationRun`: they are steps of a
consolidation run (`equity-method-run` runs inside it, PRD 7.40.16).

## What slice 3 adds

E6 dividends with the per-rule rounding line, the reclassification dry run
and the top-side group journal check, each behind a stateless preview and a
web page. Still nothing is stored, posted or published.

| Area | Code | Status |
|---|---|---|
| E6 dividends within the group: parent share from dividend income, NCI share from NCI equity, the whole dividend from dividends declared, at the dividend-date rate, FR-40.9 | `internal/domain/dividends.go` | built, unit tests |
| Per-rule rounding line (`ROUNDING`) that keeps a rule balanced after per-line rounding; a residual above half a minor unit per line is a defect and fails with CNS-0903, BR-40.9 | `internal/domain/dividends.go` (`BalanceRule`) | built, unit tests; used by E6 (E1–E5 balance by construction) |
| Dividend preview `POST /v1/consol/dividends:preview` (duty `ConsolidationRun`) | `internal/api/slice3.go` | built, tests |
| Reclassification rules run in order: entity-level DebitBalanceVendors, CreditBalanceCustomers and RemainingTerm (default 12 months, per open item due date) per party, group-level SignBased; thresholds, unique codes, versions, level checked against type, each rule balanced; entity-level lines reverse on the first day of the next period, FR-40.17, BR-40.14, AC-40.13 | `internal/domain/reclass.go` | built, unit tests |
| Reclassification dry run `POST /v1/consol/reclassification-runs:preview` (duty `ConsolidationRun`) | `internal/api/slice3.go` | built, tests |
| Top-side group journal invariants (document type, reason and attachment, two or more lines, one side per line at the minor unit, balanced or CNS-0903), approval at or above the threshold (default 100,000.00, CNS-0909), periods a carried-forward journal posts in to fiscal year end and Cumulative past it, FR-40.19, BR-40.18 | `internal/domain/groupjournal.go` | built, unit tests |
| Group journal check `POST /v1/consol/group-journals:preview` (duty `ConsolidationJournal`; CNS-0909 is a warning because nothing is posted) | `internal/api/slice3.go` | built, tests |
| Web pages: `/consolidation/dividends`, `/consolidation/reclassification`, `/consolidation/group-journals` | `apps/web/src/modules/consolidation` | built, vitest |

## Stored runs (wave 40 AC4)

A run computes the IAS 21 translation and the E1–E5 eliminations of each
subsidiary unit of a group with the rules above, derives the group statements
from them and stores everything. Rules and rates are not changed by storing:
the IAS 21 treatment of the eliminations is an open accountant question
(issue 349) and the run keeps what the engine produces.

| Area | Code | Status |
|---|---|---|
| `POST /v1/consol/runs` (duty `ConsolidationRun`, `Idempotency-Key` required): run header (group, period, currency, version, status `completed`, inputs hash, `created_by`), unit balances, eliminations and statement lines inserted in one transaction | `internal/store`, `internal/api/runs.go` | built, tests |
| `GET /v1/consol/runs` (filter `groupId`, `period`, newest first, cursor), `GET /v1/consol/runs/{id}` (balances and eliminations), `GET /v1/consol/runs/{id}/statements` (duty `ConsolidationView`) | `internal/api/runs.go` | built, tests |
| Rerun of a group, period and currency is the next version; stored rows are never updated or deleted (triggers raise 55000; `passage_app` has INSERT and SELECT only) | `migrations/0001_runs.sql`, `0002_grants.sql` | built, tests |
| Event `dev.passage.consol.run.completed.v1` through the outbox in the run's transaction | `internal/events` | built, test |
| Row-level security on the tenant GUC `passage.tenant_id` (the same scope the service has today); the group is a column and a filter, not a legal-entity policy, because a group spans legal entities | `migrations/0001_runs.sql` | built, tests as `passage_app` |

Statement lines (group currency): `net_assets` has the opening and closing net
assets, unit net income, CTA and its NCI and group shares; `income` has unit
net income, the E3 unrealized-profit adjustment, consolidated net income, NCI
profit (E5 less the E3 NCI share) and the profit attributable to owners. These
are a first derivation from the engine entries, not an accountant-reviewed
statement layout; the wave-15 designer renders printed statements.

Remaining deliverables and acceptance criteria are listed in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## Run

```
cd services/consolidation
go run ./cmd/consolidation         # HTTP_ADDR defaults to :8080
go test ./...                      # database tests skip without PASSAGE_TEST_DATABASE_URL
                                   # (RLS and grant tests also need PASSAGE_TEST_APP_DATABASE_URL)
go vet ./...
```

The service needs `DATABASE_URL` and reads `HTTP_ADDR`, `ADMIN_ADDR`
(default `:9090`, serving `/metrics`) and `MIGRATE_ON_START=true` (dev only;
stage and prod apply `migrations/` before the rollout).

## Layout

```
cmd/consolidation/       entrypoint (JSON slog, database, router, serve)
internal/domain/         pure rules, no I/O
internal/api/            HTTP handlers and DTOs
internal/store/          Postgres runs, idempotency keys and outbox
internal/events/         event types and payloads
migrations/              SQL migrations (runs, grants)
api/openapi.yaml         OpenAPI 3.1 for every exposed endpoint
events/asyncapi.yaml     AsyncAPI 3 (consol.run.completed)
charts/consolidation/    Helm chart
```

## Error codes

Problem details use `type: https://passage.dev/problems/<code>`.

| Code | Meaning |
|---|---|
| COM-0001 | Invalid input: currency, ownership outside 0 to 100 (0 to 50 for the equity method), rate missing, unsold share outside 0 to 1, negative quantity, cost or threshold, dividends above the carrying amount |
| CNS-0903 | An elimination or reclassification rule, or a top-side journal, is unbalanced (defect; the run fails) |
| CNS-0904 | Ownership for a unit totals more than 100.00 % on a date (raised by `ownership.go`; the dated group model is a later slice) |
| CNS-0905 | Two-sided IC difference above the threshold; returned per pair by the IC difference preview and the task ends in Error (BR-40.15) |
| CNS-0906 | A validation rule failed for a unit; returned per rule by the validation preview (FR-40.20) |
| CNS-0908 | Group cost unknown for some layers; margin fallback used (warning in the profit-in-inventory preview) |
| CNS-0909 | Top-side journal at or above the approval threshold needs approval (warning in the group journal check) |
| CNS-0910 | Move from an existing equity interest to control is a step acquisition, not supported (domain guard) |
| CNS-0921 / CNS-0922 | No run with that id for the tenant (404) / Idempotency-Key reused with different inputs (409). Not in the PRD catalog; proposed for the catalog owner |
| AUTH-0001 / AUTH-0003 | Missing tenant / missing duty |
| CNS-0920 | Bargain purchase: investment cost below the subsidiary's net assets at acquisition. Not in the PRD catalog; proposed for the catalog owner |

## Dependencies

The platform module, chi, shopspring/decimal and pgx (already used by the
other stored services).

## Wave reference

PSG-40, PRD section 7.40 (Wave 40: Intercompany & Financial Consolidation).
