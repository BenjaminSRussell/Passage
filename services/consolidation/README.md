# consolidation (wave 40, PSG-40, slices 1-2)

Financial consolidation of a legal-entity group: IAS 21 translation with CTA,
the elimination entries E1–E5 with the non-controlling interest (NCI) share,
ownership validation, unrealized profit per IC receipt layer, the equity
method and divestitures, two-sided IC elimination and the unit validation
rules of the monitor (PRD 7.40).

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

Remaining deliverables and acceptance criteria are listed in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## Run

```
cd services/consolidation
go run ./cmd/consolidation         # HTTP_ADDR defaults to :8080
go test ./...                      # no database needed
go vet ./...
```

The service reads only `HTTP_ADDR` and `ADMIN_ADDR` (default `:9090`,
serving `/metrics`) in these slices. `DATABASE_URL` and the
migrator URL are wired when the store slice lands.

## Layout

```
cmd/consolidation/       entrypoint (JSON slog, router, serve)
internal/domain/         pure rules, no I/O
internal/api/            HTTP handlers and DTOs
api/openapi.yaml         OpenAPI 3.1 for every exposed endpoint
events/asyncapi.yaml     AsyncAPI 3 (no events in slices 1 and 2)
charts/consolidation/    Helm chart
```

## Error codes

Problem details use `type: https://passage.dev/problems/<code>`.

| Code | Meaning |
|---|---|
| COM-0001 | Invalid input: currency, ownership outside 0 to 100 (0 to 50 for the equity method), rate missing, unsold share outside 0 to 1, negative quantity, cost or threshold, dividends above the carrying amount |
| CNS-0903 | An elimination rule is unbalanced (defect; the run fails) |
| CNS-0904 | Ownership for a unit totals more than 100.00 % on a date (raised by `ownership.go`; the dated group model is a later slice) |
| CNS-0905 | Two-sided IC difference above the threshold; returned per pair by the IC difference preview and the task ends in Error (BR-40.15) |
| CNS-0906 | A validation rule failed for a unit; returned per rule by the validation preview (FR-40.20) |
| CNS-0908 | Group cost unknown for some layers; margin fallback used (warning in the profit-in-inventory preview) |
| CNS-0910 | Move from an existing equity interest to control is a step acquisition, not supported (domain guard) |
| AUTH-0001 / AUTH-0003 | Missing tenant / missing duty |
| CNS-0920 | Bargain purchase: investment cost below the subsidiary's net assets at acquisition. Not in the PRD catalog; proposed for the catalog owner |

## Dependencies

Only the platform module and what it already pulls in (chi, shopspring/decimal,
pgregory.net/rapid for tests).

## Wave reference

PSG-40, PRD section 7.40 (Wave 40: Intercompany & Financial Consolidation).
