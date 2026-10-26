# costing

Passage inventory costing service: wave 20 (ticket PSG-20, "Inventory Costing & Supplier Collaboration"), **slice 1**.

The service owns its tables and applies its own migrations (`migrations/0001_costing.sql`) through
`dbx.Migrate`. The PRD places them in schema `costing` of `inventory-db` (`inventory/v20.*`); that
placement is a deploy decision recorded as a handoff in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## What slice 1 does

- **Settlement engine** (`internal/domain`, pure, no I/O). Values issues against receipts under
  an item model group's costing method: `FIFO`, `LIFO`, `LIFO_DATE`, `WEIGHTED_AVG_RUNNING`,
  `WEIGHTED_AVG_PERIODIC`, `MOVING_AVG`, `STANDARD` (FR-20.1). Quantity that stock does not
  cover when an issue is costed waits on a backlog; a later receipt in the same run settles it
  first. What no receipt covers is valued at the BR-20.3 fallback (running average of the stock
  before the issue, then the standard cost, then the last receipt cost, or the configured
  `STANDARD` / `LAST_RECEIPT`) and listed as an exception. Rounding is half-even per settlement row, with the last
  row absorbing the residual (BR-20.4). Ordering is by (financial date, physical date, id), so
  the output is deterministic (BR-20.2, property-tested with `pgregory.net/rapid`).
- **Purchase price variance** (`internal/domain/ppv.go`): receipt PPV `(PO − standard) × qty` and
  invoice PPV `(invoice − PO) × qty` (FR-20.7, AC-20.4). A positive amount is a debit.
- **Settlement preview API** `POST /v1/costing/settlements:preview`. Stateless; nothing is posted.
- **Costing versions** `POST /v1/costing/versions` (DRAFT, idempotent per `Idempotency-Key`, code
  unique per legal entity) and item-cost import `POST /v1/costing/versions/{id}/item-costs:import`
  (one statement, idempotent per product and site, bumps the version's `version`). Persisted with
  tenant RLS and the legal-entity scope policy (`migrations/0001_costing.sql`).
- **PPV hook** `POST /internal/v1/costing/ppv` (calculation only; persisting `CostTransaction` is
  a later slice).
- Web module `apps/web/src/modules/costing`: settlement preview page.

Everything else in the ticket is listed in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## Run

```sh
# from the repo root
PASSAGE_ENV=dev SERVICE_NAME=costing DATABASE_URL=postgres://... MIGRATE_ON_START=true \
  go run ./services/costing/cmd/costing
```

Endpoints: `GET /healthz`, `GET /readyz`, the API above, metrics on the admin port 9090.

## Test

```sh
cd services/costing
go vet ./... && go test ./...
```

Store tests use `platform/go/testkit` and skip unless `PASSAGE_TEST_DATABASE_URL` is set. They run
the queries as a non-superuser role so RLS applies, which needs `PASSAGE_TEST_APP_DATABASE_URL` as
`passage_app`:

```sh
PASSAGE_TEST_DATABASE_URL=postgres://admin@... PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app@... go test ./...
```

## Contracts

- HTTP: [`api/openapi.yaml`](api/openapi.yaml)
- Events: [`events/asyncapi.yaml`](events/asyncapi.yaml). Slice 1 publishes no events; the planned
  catalog is listed under `x-planned-events`.
- Chart: [`charts/costing`](charts/costing) (`helm lint charts/costing`).

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `COM-0001` | 400 | Request failed validation; `errors` maps field to reason |
| `AUTH-0001` | 401 | Missing or malformed tenant |
| `AUTH-0003` | 403 | Legal entity is outside the caller's scope |
| `CST-TXN-001` | 422 | Invalid settlement input (a non-positive quantity, or `STANDARD` without a positive standard unit cost) |
| `CST-TXN-002` | 422 | Duplicate transaction id in one preview |
| `CST-PPV-001` | 422 | PPV inputs invalid (quantity not positive or a negative unit price) |
| `CST-VER-CODE-DUPLICATE` | 409 | A costing version with this code already exists for the legal entity |
| `CST-VER-NOT-FOUND` | 404 | Costing version not found in this tenant |
| `CST-VER-NOT-DRAFT` | 409 | Item costs can only be imported into a DRAFT version |
| `idempotency_key_reuse` | 422 | `Idempotency-Key` reused with a different body |
| `AUTH-0003` | 403 | Missing duty (kernel `authz.RequireDuty`) |
| `COM-0500` | 500 | Unexpected error; detail hidden, logged with the request id |

## Duties

| Duty | Routes | PRD duty (§3.20.17) |
|---|---|---|
| `costing.read` | `POST /v1/costing/settlements:preview` | CostingInquire |
| `costing.version.write` | `POST /v1/costing/versions`, `POST /v1/costing/versions/{id}/item-costs:import` | CostingVersionMaintain |
| `costing.ppv.post` | `POST /internal/v1/costing/ppv` | service account of receiving and payables |

## Dependencies

Only the platform module and what it already pulls in (chi, pgx/v5, shopspring/decimal, google/uuid,
pgregory.net/rapid for tests). No new dependencies in slice 1.

## Wave reference

PSG-20 · wave 20 · PRD `docs/prd/phase-3-procure-to-pay.md` §3.20 · depends on PSG-19 (waves 16–19 for
transactions, receipts, invoices and payments).
