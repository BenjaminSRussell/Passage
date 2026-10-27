# intercompany (wave 40, PSG-40, slices 1-2)

Intercompany trade, transfers and charges between legal entities of one
tenant: trading-partner rules, the due-to / due-from journal derivation, the
mirror-document saga state logic, the reconciliation classifier and
intercompany charge pricing and settlement, centralized payments and the
planning group sequence (PRD 7.40).

## What slice 1 delivers

Pure domain logic plus two stateless HTTP previews and the web workspace.
Nothing is persisted and no event is published yet.

| Area | Code | Status |
|---|---|---|
| Due-to / due-from derivation, BR-40.4, AC-40.4, counterparty at the document rate (FR-40.5) | `internal/domain/journal.go` | built, unit and property tests |
| Journal preview `POST /v1/interco/journals:preview` (duty `IntercompanyPost`) | `internal/api` | built, tests |
| Reconciliation classifier, FR-40.6 / BR-40.5, 50 planted mismatches flagged and 50 clean pairs matched (AC-40.5 shape) | `internal/domain/recon.go` | built, unit tests (`TestClassify_PlantedMismatches`) |
| Reconciliation classify `POST /v1/interco/reconciliations:classify` (duty `IntercompanyReconcile`) | `internal/api` | built, tests |
| Mirror saga state logic, FR-40.2 / BR-40.1 | `internal/domain/mirror.go` | built, property test (AC-40.1 / AC-40.2 shape, 1,000 random orders) |
| Partner policy validation, FR-40.1; tolerance BR-40.3 | `internal/domain/pairing.go` | built, unit tests |
| IC charge cost-plus pricing, FR-40.13 (AC-40.11 arithmetic) | `internal/domain/charge.go` | built, unit tests |
| Counters on `/metrics` (admin port) | `internal/api` | built, tests |
| Web workspace: `/intercompany`, `/intercompany/journal`, `/intercompany/reconciliation` | `apps/web/src/modules/intercompany` | built, vitest |

Browser calls go to `/api/v1/interco/...`; the web BFF rewrites them to the
`/v1/interco/...` paths above and sets `X-Passage-Tenant` and
`X-Passage-Duties`.

## What slice 2 adds

Pure rules for intercompany charges, centralized payments and planning
groups, each behind a stateless preview and a web page. Still nothing is
stored, posted or published.

| Area | Code | Status |
|---|---|---|
| Transfer price resolution by source transaction date, BR-40.12 (IC-0821; two rows effective on one date are rejected) | `internal/domain/charge_settlement.go` | built, unit tests |
| Transfer-priced charge pricing (Cost, CostPlus, Table) and Journal-mode legs in lender and borrower, FR-40.13, AC-40.11 (528.00 / 480.00 / 48.00); a table price below cost posts the IC loss as a debit | `internal/domain/charge_settlement.go` | built, unit tests |
| Charge preview `POST /v1/interco/charges:preview` (duty `IntercompanyPost`) | `internal/api/settlement.go` | built, tests |
| Centralized payments: pay and receive on behalf legs with due-to / due-from from the matrix, amount rounded to the minor unit, FR-40.12, AC-40.10 | `internal/domain/centralized.go` | built, unit tests |
| Centralized payment preview `POST /v1/interco/centralized-payments:preview` (duty `IntercompanyConfigure`) | `internal/api/settlement.go` | built, tests |
| Planning group run order: buyers before suppliers, each entity once; cycles (strongly connected entities) need an explicit sequence for every member and run in sequence order while edges around them are kept, else IC-0816, FR-40.15 / BR-40.13 | `internal/domain/planning.go` | built, unit tests and rapid properties over acyclic and cyclic partner graphs |
| IC demand at requested date minus transport lead time (AC-40.16) and stale suppliers of a replanned buyer (IC-0815) | `internal/domain/planning.go` | built, unit tests |
| Planning sequence `POST /v1/interco/planning-groups:sequence` (duty `IntercompanyPlan`) | `internal/api/planning.go` | built, tests |
| OpenAPI matches the mounted routes and duties | `internal/api/contract_test.go` | built, test |
| Web pages: `/intercompany/charges`, `/intercompany/centralized-payments`, `/intercompany/planning` | `apps/web/src/modules/intercompany` | built, vitest |

Remaining deliverables and acceptance criteria are listed in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## Run

```
cd services/intercompany
go run ./cmd/intercompany          # HTTP_ADDR defaults to :8080
go test ./...                      # no database needed
go vet ./...
```

The service reads only `HTTP_ADDR` and `ADMIN_ADDR` (default `:9090`,
serving `/metrics`) in these slices. `DATABASE_URL`,
`DATABASE_MIGRATOR_URL` and the OPA sidecar are wired when the store slice
lands.

## Layout

```
cmd/intercompany/        entrypoint (JSON slog, router, serve)
internal/domain/         pure rules, no I/O
internal/api/            HTTP handlers and DTOs
api/openapi.yaml         OpenAPI 3.1 for every exposed endpoint
events/asyncapi.yaml     AsyncAPI 3 (no events in slices 1 and 2)
charts/intercompany/     Helm chart
```

## Error codes

Problem details use `type: https://passage.dev/problems/<code>`.

| Code | Meaning |
|---|---|
| COM-0001 | Invalid input (currency, amount, side, date, category, method, or a missing field) |
| IC-0801 | No active IC partner policy between the two entities |
| IC-0805 | No IC accounting matrix entry for the entity pair (due-to / due-from missing) |
| IC-0815 | Supplier plan is stale after its buyer was replanned (warning in the planning sequence response) |
| IC-0816 | Planning group has a cycle without an explicit sequence |
| IC-0821 | No transfer price for the category and role on the date |
| IC-0830 | Derived journal legs do not balance (defect guard; not in the PRD catalog, listed in NOT-IMPLEMENTED.md) |
| AUTH-0001 / AUTH-0003 | Missing tenant / missing duty |

## Dependencies

Only the platform module and what it already pulls in (chi, shopspring/decimal,
pgregory.net/rapid for tests).

## Wave reference

PSG-40, PRD section 7.40 (Wave 40: Intercompany & Financial Consolidation).
