# production

Production orders for Passage (wave 21, ticket PSG-21, PRD section 4.21). Slice 1 covers the
order itself, up to the point where journals would post:

- Order creation with BOM explosion (FR-21.1): phantom lines flattened in place with `30.1`
  numbering, BR-21.2 quantity rounding (6 decimals, or a whole unit with `roundUp`), BR-21.8
  scrap compounding backward along the route, order numbers `PO-000001` per legal entity.
- The status state machine of BR-21.1 with the eight `:command` custom methods, If-Match
  optimistic concurrency (412), Idempotency-Key replay with a request fingerprint (a reused key
  with another body is 422), and a status log row per command with actor and trace id (FR-21.2).
- Estimation (FR-21.3): planned material (Σ qty × (1 + scrap%) × unit cost, one row per item),
  labor and overhead (setup + run × process quantity at the resource rates, setup kept as the
  fixed part) and subcontract (vendor unit price × process quantity), 4 decimals internally.
  Re-estimate is allowed until Started and keeps the status.
- Report as finished at header level (FR-21.7 quantities): good and error quantities within
  quantity × (1 + `PRODUCTION_RAF_TOLERANCE_PCT`); further reports on a ReportedAsFinished order
  add to them. Reset status to any earlier status (FR-21.9) reverses the reported quantities, and
  a reset to Created drops the estimate. End checks the zero-output duty (BR-21.1).
- Header edits (PATCH): quantity while Created or Estimated (re-explodes, re-plans and
  re-estimates in one transaction), delivery date until Released.
- The order list (keyset paging, filters), order read, planned-cost read and status-log read.
- The settlement decomposition of BR-21.6 (`domain.Settle`: lot-size, production quantity,
  production price, production substitution), property-tested, with the BR-21.7 worked example
  and the 50-order `prod-orders-50` fixture.
- Events `order.created`, `order.updated` and `order.statuschanged`, written to the outbox in the
  same transaction as the change.

Inventory and GL postings (picking lists, route and job cards, WIP, RAF receipts, settlement
journals) need the wave 11 posting API and wave 16 inventory, and belong to later slices of
PSG-21. Everything not built is listed in `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/production/        entrypoint: config, dbx.Connect, optional dbx.Migrate, httpx router, admin /metrics
internal/domain/       pure rules: status.go (BR-21.1 state machine, reset, RAF and edit guards),
                       quantity.go (BR-21.2, BR-21.8), explode.go (FR-21.1), cost.go (FR-21.3, BR-21.6)
internal/domain/testdata/  prod-orders-50.json and the exact-fraction generator that computed it
internal/store/        Postgres repositories (pgx), tenant-scoped transactions, row locks, outbox writes
internal/api/          HTTP handlers, request validation, platform authz duties
internal/events/       event types and payloads
migrations/            0001_init.sql (tables, indexes, RLS, app-role grants), migrations.go (embed)
api/openapi.yaml       OpenAPI 3.1 for every route (a test checks it against the router)
events/asyncapi.yaml   AsyncAPI 3 for every event (a test checks it against the payloads)
charts/production/     Helm chart (deployment, service, HPA on CPU 70%, optional ServiceMonitor)
```

## Run

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/production
```

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | required | Postgres connection string |
| `MIGRATE_ON_START` | `false` | apply `migrations/` at start |
| `HTTP_ADDR` / `ADMIN_ADDR` | `:8080` / `:9090` | API and admin (`/metrics`) listeners |
| `PRODUCTION_RAF_TOLERANCE_PCT` | `0` | over-delivery allowed on report as finished, 0 to 100 (PRD 4.21.13) |

The gateway supplies the principal through the `X-Passage-Tenant`, `X-Passage-Legal-Entity`,
`X-Passage-User` and `X-Passage-Duties` headers until wave 2 replaces them with JWT validation.
The routes are `/v1/production/...`; the gateway serves them under `/api`.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...                     # store and HTTP tests on a database
PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app:...@.../... (also)    # the RLS test under the app role
python3 internal/domain/testdata/gen_prod_orders_50.py                     # regenerate the AC2 fixture
```

Database tests skip when `PASSAGE_TEST_DATABASE_URL` is unset; the RLS test also needs a
non-superuser `passage_app` role (`PASSAGE_TEST_APP_DATABASE_URL`). The domain tests are pure and
include `pgregory.net/rapid` property tests for the state machine, report-as-finished limits,
phantom explosion, backflush rounding and the settlement identity.

## Duties

| Duty (PRD 4.21.17) | Key | Used by |
|---|---|---|
| View production orders | `production.order.read` | list, read, status log |
| View production costs | `production.cost.read` | `GET …/{id}/costs` |
| Maintain production orders | `production.order.write` | create, PATCH, `:estimate`, `:schedule` |
| Release production orders | `production.order.release` | `:release`, `:start`, `:cancel`, `:reset-status` |
| Post production journals | `production.journal.post` | `:report-finished` |
| End production orders | `production.order.end` | `:end` |
| End production order with zero output | `production.order.zero-output` | `:end` from Started with no output |

Routes enforce duties with `platform/go/authz`. OPA entries for these duties are a handoff to
wave 2 (`policies/`).

## Error codes

Problems are RFC 9457 with `type` `https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning (PRD message) |
|---|---|---|
| `PRODUCTION_ILLEGAL_TRANSITION` | 409 | The command is not allowed from the current status (PROD-0001) |
| `PRODUCTION_FIELD_NOT_EDITABLE` | 409 | The PATCHed field cannot change in the current status |
| `PRODUCTION_RAF_EXCEEDS_REMAINING` | 422 | Reported quantity exceeds remaining plus tolerance (PROD-0008) |
| `PRODUCTION_END_REQUIRES_RAF` | 422 | Ending a started order that has output without reporting it as finished |
| `PRODUCTION_ZERO_OUTPUT_NOT_PERMITTED` | 422 | Ending with zero output without the zero-output duty (PROD-0013) |
| `PRODUCTION_INVALID_INPUT` | 422 | Explosion or planning rule failed (quantities, scrap, series, operation order) |
| `PRODUCTION_ORDER_NOT_FOUND` | 404 | No order with that id in the caller's tenant and legal-entity scope |
| `PRODUCTION_UNKNOWN_ACTION` | 404 | The `:command` is not one of the eight |
| `idempotency_key_reuse` | 422 | The Idempotency-Key was used for a different body, command or order |
| `conflict_version` | 412 | If-Match does not match the order version (PROD-0003) |
| `precondition_required` | 428 | If-Match is missing on a command or PATCH |
| `COM-0001` | 400 | Request failed validation; `errors` lists the fields |
| `AUTH-0001` / `AUTH-0003` | 401 / 403 | No tenant / missing duty |

The stable PROD-00nn service codes are not yet carried in the problem body, because the kernel
`httpx.Problem` has no `serviceCode` field (handoff to wave 0).

## Wave reference

PSG-21 (wave 21). Depends on PSG-20 (costing). Later slices: picking lists, route and job cards
and RAF journals with inventory and GL postings, WIP and settlement postings, subcontracting,
split and merge, order change management, trigger points, rework orders and as-built genealogy.
See `NOT-IMPLEMENTED.md`.
