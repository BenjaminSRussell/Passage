# consolidation (wave 40, PSG-40, slice 1)

Financial consolidation of a legal-entity group: IAS 21 translation with CTA,
the elimination entries E1–E5 with the non-controlling interest (NCI) share,
and ownership validation (PRD 7.40).

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
serving `/metrics`) in this slice. `DATABASE_URL` and the
migrator URL are wired when the store slice lands.

## Layout

```
cmd/consolidation/       entrypoint (JSON slog, router, serve)
internal/domain/         pure rules, no I/O
internal/api/            HTTP handlers and DTOs
api/openapi.yaml         OpenAPI 3.1 for every exposed endpoint
events/asyncapi.yaml     AsyncAPI 3 (no events in slice 1)
charts/consolidation/    Helm chart
```

## Error codes

Problem details use `type: https://passage.dev/problems/<code>`.

| Code | Meaning |
|---|---|
| COM-0001 | Invalid input: currency, ownership outside 0 to 100, rate missing, unsold share outside 0 to 1 |
| CNS-0903 | An elimination rule is unbalanced (defect; the run fails) |
| CNS-0904 | Ownership for a unit totals more than 100.00 % on a date (raised by `ownership.go`; the dated group model is a later slice) |
| AUTH-0001 / AUTH-0003 | Missing tenant / missing duty |
| CNS-0920 | Bargain purchase: investment cost below the subsidiary's net assets at acquisition. Not in the PRD catalog; proposed for the catalog owner |

## Dependencies

Only the platform module and what it already pulls in (chi, shopspring/decimal,
pgregory.net/rapid for tests).

## Wave reference

PSG-40, PRD section 7.40 (Wave 40: Intercompany & Financial Consolidation).
