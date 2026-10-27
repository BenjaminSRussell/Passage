# costing

Passage inventory costing service: wave 20 (ticket PSG-20, "Inventory Costing & Supplier Collaboration"), **slices 1 and 2**.

The service owns its tables and applies its own migrations (`migrations/0001_costing.sql`) through
`dbx.Migrate`. The PRD places them in schema `costing` of `inventory-db` (`inventory/v20.*`); that
placement is a deploy decision recorded as a handoff in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## What slice 2 adds

- **Marking** (FR-20.8): an issue names the receipt it settles against first (`markedReceiptId`).
  The marked quantity is reserved on the receipt when it enters the engine, so no unmarked issue or
  earlier shortage consumes it, and the marked part settles at that receipt's own cost under every
  method except STANDARD (where every unit costs the standard). Under the average methods the
  reserved part leaves the pool. A marked issue sequenced before its same-day receipt waits for it.
  A mark on a receipt, an unknown receipt or a receipt dated after the issue is refused with `CST-TXN-001`.
- **Split valuation** (FR-20.16, `internal/domain/valuation.go`): stock is kept per valuation type
  (`valuationType` on each transaction, optional `defaultValuationType` on the request). Each type
  is its own cost pool. In a split product every receipt needs a type (its own or the default). An
  untyped issue takes the type of the receipt it marks, else the default, else the only type holding
  stock; it is refused with `CST-VAL-001` when several hold stock. `CanChangeCategory` refuses a category change while stock
  exists (`CST-VAL-002`). The settlement preview returns `remainingByValuationType` when types are used.
- **Parallel valuation ledgers** (FR-20.15, BR-20.13, `internal/domain/ledger.go`):
  `POST /v1/costing/valuation-ledgers:preview` settles one transaction sequence under several
  ledgers, each with its own method, currency and rate. Exactly one ledger is primary with rate 1.
  Receipt values convert at the ledger rate and round to its scale. Nothing is posted.
- **Inventory aging** (FR-20.17, BR-20.15, `internal/domain/aging.go`):
  `POST /v1/costing/aging:preview` buckets remaining cost by calendar days since receipt. Default
  buckets are 0–30, 31–90, 91–180, 181–365 and > 365; `bucketBounds` overrides them. For the average
  methods the ages come from FIFO layers, repriced to the average, so `tiesToRemaining` is true.
  A split product is aged per valuation type (each at its own average) and the response adds
  `valuationTypes`. Transactions dated after `asOf` are refused.

- **Web screens** (`apps/web/src/modules/costing`): the settlement preview now takes a default
  valuation type and shows remaining value by type; new pages at `/costing/valuation-ledgers`
  (ledger editor, per-ledger results and issue costs side by side) and `/costing/aging` (buckets,
  tie-out to the remaining value, and aging per valuation type).

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
- Web module `apps/web/src/modules/costing`: settlement preview page (slice 2 adds the valuation ledger and aging screens).

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
| `CST-VAL-001` | 422 | An untyped issue must name a valuation type because several types hold stock |
| `CST-VAL-002` | 422 | A valuation category cannot change while the product holds stock (domain guard; no endpoint changes a category yet) |
| `CST-VLG-001` | 422 | Valuation ledger set invalid (no or two primaries, duplicate id, non-positive rate, primary rate not 1) |
| `CST-VER-CODE-DUPLICATE` | 409 | A costing version with this code already exists for the legal entity |
| `CST-VER-NOT-FOUND` | 404 | Costing version not found in this tenant |
| `CST-VER-NOT-DRAFT` | 409 | Item costs can only be imported into a DRAFT version |
| `idempotency_key_reuse` | 422 | `Idempotency-Key` reused with a different body |
| `AUTH-0003` | 403 | Missing duty (kernel `authz.RequireDuty`) |
| `COM-0500` | 500 | Unexpected error; detail hidden, logged with the request id |

## Duties

| Duty | Routes | PRD duty (§3.20.17) |
|---|---|---|
| `costing.read` | `POST /v1/costing/settlements:preview`, `POST /v1/costing/valuation-ledgers:preview`, `POST /v1/costing/aging:preview` | CostingInquire |
| `costing.version.write` | `POST /v1/costing/versions`, `POST /v1/costing/versions/{id}/item-costs:import` | CostingVersionMaintain |
| `costing.ppv.post` | `POST /internal/v1/costing/ppv` | service account of receiving and payables |

## Dependencies

Only the platform module and what it already pulls in (chi, pgx/v5, shopspring/decimal, google/uuid,
pgregory.net/rapid for tests). No new dependencies in slice 1.

## Wave reference

PSG-20 · wave 20 · PRD `docs/prd/phase-3-procure-to-pay.md` §3.20 · depends on PSG-19 (waves 16–19 for
transactions, receipts, invoices and payments).
