# assets (Wave 37: Fixed Assets & Leases, slice 1)

The fixed-asset subledger and IFRS 16 leases. Slice 1 delivers the asset
register, depreciation books with the straight-line and declining-balance
(with switch to straight line, MACRS-style) engines, the projected schedule,
acquisitions at book creation, the transaction log, disposals (sale or scrap,
partial percentage with gain/loss on every open book) and lease recognition
with liability and ROU schedules. The web module is
`apps/web/src/modules/assets`.

Reference: PSG-37, `docs/prd/phase-7-treasury-controlling.md` section 7.37.
The Helm chart is `charts/assets` (the PRD calls it `asset-svc`; the chart is
named after the service directory, per the repository convention).

## Layout

```
cmd/assets/            entrypoint: dbx.Connect -> (optional) dbx.Migrate -> httpx.NewRouter -> register -> httpx.Serve
internal/domain/       pure rules: Schedule (SL, DB/MACRS), Dispose, ComputeLease, insured value,
                       capex state machine (no I/O)
internal/store/        pgx repositories, tenant transactions, idempotency_keys, outbox writes
internal/api/          chi handlers, DTOs, validation, duties
internal/events/       event types and payloads (dev.passage.assets.*.v1)
migrations/            0001_assets.sql (RLS on every table, append-only asset_transaction)
testdata/              worked-examples.json: PRD fixtures (AC-37.1, AC-37.2)
api/openapi.yaml       HTTP contract for every route (checked by TestRoutesMatchOpenAPI)
events/asyncapi.yaml   events published through the outbox (checked by TestEventsMatchAsyncAPI)
charts/assets/         Helm chart (liveness /healthz, readiness /readyz, ServiceMonitor toggle)
```

## Run

```
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_assets
export MIGRATE_ON_START=true        # dev only; stage/prod run migrations as a PreSync Job
go run ./cmd/assets
```

Ports: `HTTP_ADDR` (default `:8080`), `ADMIN_ADDR` (default `:9090`, `/metrics`).
`ASSET_MAX_BOOKS` (default 10, 1 to 20) caps books per asset.
Tenant and duties come from the gateway headers handled by `platform/go/tenant`
and `authz` until OPA is wired (wave 2 kernel).

## API rules

- Commands (`POST`) need `Idempotency-Key`. The key, a hash of the request and
  the response are stored in `idempotency_keys` in the same transaction as the
  change: a retry replays the first response, the same key with a different
  request is 422 `idempotency_key_reuse`.
- `GET /v1/assets/{id}` and the create return `ETag: "<version>"`;
  `POST /v1/assets/{id}/dispose` requires `If-Match` (428 without, 412 when stale).
- A disposal applies to every Open book of the asset (as SAP retirements post to
  all depreciation areas): each book records its own transaction with the
  disposed share of cost and accumulated depreciation, rounded once with the
  residual cents left on the asset (BR-37.7), and its own gain or loss against
  the same proceeds. 100% closes the asset and its books as Sold or Scrapped
  (BR-37.2). Scrap takes no proceeds.
- Straight-line life is in months; declining-balance life is in years.
  Conventions: None, FullMonth, MidMonth15 (half of the first and last month,
  the PRD worked example) for straight line; None and HalfYear for declining
  balance.
- A legal-entity header narrows every read through the restrictive RLS policy;
  creating in another entity is 403.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # store tests
PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app@... # also runs the RLS test as a non-bypass role
```

Store tests use `testkit.DB(t)` and skip without `PASSAGE_TEST_DATABASE_URL`.
They cover idempotent replay and key reuse, AC-37.5 on two books, the
subledger reconciliation (Σ transactions per book = the book's cost and
accumulated depreciation), the book limit, pagination and RLS.
Domain tests are pure: table-driven, `pgregory.net/rapid` properties (life totals,
lease closing), and the worked-example fixtures.

## Dependencies

Only the platform module and what it already pulls in (chi, pgx/v5,
shopspring/decimal, google/uuid).

## Money and rounding

Amounts are `numeric(20,6)` with a currency. Period depreciation rounds half-up
to the currency minor unit and the last period is a plug, so the sum equals the
depreciable basis exactly (BR-37.1). Schedules render in the currency's minor
unit (JPY has none). Rates are decimal strings; no floats.

## Error codes

Problem details are `application/problem+json` with `code` and `type`
`https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| COM-0001 | 400 | Request validation failed (field errors in `errors`), `Idempotency-Key` missing, malformed `If-Match` or legal-entity header |
| AUTH-0001 | 401 | Missing or malformed tenant |
| AUTH-0003 | 403 | Missing duty, or legal entity outside the caller's scope |
| AST-0001 | 404 | Asset not found |
| AST-0002 | 409 | Asset number or barcode already exists in the legal entity |
| AST-0003 | 404 | Book not found on the asset |
| AST-0004 | 409 | Asset already has a book with this code |
| AST-0005 | 404 | Lease not found |
| AST-0006 | 412 / 428 | Asset version conflict (stale or missing `If-Match`) |
| AST-0007 | 409 | Lease number already exists in the legal entity |
| AST-0010 | 422 | Depreciation profile invalid (life, method, convention, factor, salvage) |
| AST-0011 | 422 | Disposal invalid (percentage outside (0, 100], date before in-service) |
| AST-0012 | 422 | Lease invalid (term, frequency, payment) |
| AST-0013 | 422 | Too many books on the asset (`ASSET_MAX_BOOKS`) |
| AST-0509 | 422 | Asset state does not allow the posting (BR-37.2) |
| AST-0515 | 422 | Lease IBR outside (0, 50%] |
| idempotency_key_reuse | 422 | Idempotency-Key reused with a different request |

Other PRD message codes (AST-0501..0542) belong to later slices; see NOT-IMPLEMENTED.md.

## Events

Written to the outbox in the same transaction as the change:
`dev.passage.assets.asset.acquired.v1` (one per book added),
`dev.passage.assets.asset.disposed.v1` (one per book disposed),
`dev.passage.assets.lease.recognized.v1`. See `events/asyncapi.yaml`.

## Known gaps

See NOT-IMPLEMENTED.md for everything in the ticket that is not in this slice,
with status and reason.
