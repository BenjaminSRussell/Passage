# fulfillment

Passage fulfillment service, wave 28 (PSG-28), slice 1. It releases confirmed
order lines to a warehouse shipment, records pick and pack quantities,
closes containers with GS1 SSCC-18 numbers, and posts a packing slip that
journals delivered-not-invoiced (Dr 1405 / Cr 1400 at cost, or Dr 5000 / Cr 1400
when COGS posts at shipment), with a mirror reversal on cancellation. The
inventory issue and the ledger posting are made by their owning services from
the `packingslip.posted` event; this service does not write their tables (see
NOT-IMPLEMENTED.md).

Engineering rules: `docs/engineering/CONVENTIONS.md`. Requirements:
`docs/prd/phase-5-order-to-cash.md` section 5.28. Gaps in this slice are listed in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## Layout

```
cmd/fulfillment/        entrypoint: dbx.Connect, optional Migrate, httpx router, Serve
internal/domain/        pure rules: SSCC/GS1 check digits, release grouping, pick/pack,
                        shipment state machine, posting matrix (no I/O, property tests)
internal/store/         Postgres repositories, tenant-scoped transactions, outbox, idempotency,
                        release de-duplication, PS- numbering per legal entity
internal/events/        event types dev.passage.fulfillment.* and payloads
internal/api/           HTTP handlers, DTO validation, duties, If-Match, Idempotency-Key
migrations/             0001_init.sql (RLS, outbox, idempotency, number counters)
api/openapi.yaml        OpenAPI 3.1 for every endpoint in this slice
events/asyncapi.yaml    AsyncAPI 3 for every event in this slice
charts/fulfillment/     Helm chart (helm lint passes)
```

## Run

Configuration is read from the environment and validated at startup; the process
exits non-zero on an invalid value.

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `DATABASE_URL` | yes | | Postgres URL for the `passage_app` role (no BYPASSRLS) |
| `GS1_COMPANY_PREFIX` | yes | | 6 to 12 digits (BR-28.5) |
| `SSCC_EXTENSION_DIGIT` | no | `0` | 0 to 9 |
| `RELEASE_HORIZON_DAYS` | no | `2` | 0 to 30 (FR-28.1) |
| `RELEASE_REQUIRES_FULL_ATP` | no | `true` | `false` allows partial release |
| `MIGRATE_ON_START` | no | `false` | dev only; stage and prod use the Argo PreSync migration Job |
| `HTTP_ADDR` | no | `:8080` | listen address |

```
cd services/fulfillment
go run ./cmd/fulfillment
```

## Test

```
go vet ./... && go test ./...
```

Domain tests are table-driven and use `pgregory.net/rapid` for the SSCC property
(generation, check digit, single-digit change detection), plus the GS1 General
Specifications example `106141411234567897`. The store tests run release, pick,
short pick, pack, post (both BR-28.1 debit accounts), cancel, re-post, idempotent
replay, key reuse, release de-duplication and tenant and legal-entity isolation
against Postgres through `platform/go/testkit`. They run as the application role
so row-level security applies, which needs both URLs:

```
PASSAGE_TEST_DATABASE_URL=postgres://admin@localhost/db \
PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app@localhost/db \
go test ./...
```

`passage_app` is NOSUPERUSER NOBYPASSRLS and needs the privileges of the schema
owner. Without the URLs the store tests are skipped, so `go test ./...` passes
without a database. The API tests use an in-package fake store; they also check
that `api/openapi.yaml` documents exactly the mounted routes with a duty each,
and the events test checks `events/asyncapi.yaml` against the published types.

The web module (`apps/web/src/modules/fulfillment`) has the fulfillment workspace,
release board, shipments list, shipment detail (confirm pick, pack container,
post packing slip with a journal preview), packing slip journal and detail
(cancel), and the GS1-128 scan page. Run `npx vitest --run src/modules/fulfillment`
and `npx tsc --noEmit` in `apps/web`.

## Endpoints (slice 1)

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/api/v1/fulfillment/releases:run` | WarehouseRelease | Idempotency-Key. Creates shipments per (order, warehouse, ship date); a line already on an open shipment is skipped as `already_released`; runs per warehouse are serialised |
| GET | `/api/v1/fulfillment/shipments` | WarehouseView | Delivery monitor; filters `status`, `warehouseId`, `orderId`, `limit` (1 to 500, default 100) |
| GET | `/api/v1/fulfillment/shipments/{id}` | WarehouseView | ETag carries the version |
| POST | `/api/v1/fulfillment/shipments/{id}:confirm-pick` | WarehousePick | Idempotency-Key, If-Match |
| POST | `/api/v1/fulfillment/shipments/{id}/containers` | WarehousePack | Packs and closes a container with an SSCC; Idempotency-Key, If-Match |
| POST | `/api/v1/fulfillment/shipments/{id}:post-packing-slip` | PackingSlipPost | Slip, journal, containers SHIPPED and outbox in one transaction; Idempotency-Key, If-Match |
| GET | `/api/v1/fulfillment/packing-slips` | WarehouseView | Packing slip journal; filters `shipmentId`, `orderId`, `status`, `limit` |
| GET | `/api/v1/fulfillment/packing-slips/{id}` | WarehouseView | Slip with lines and journal |
| POST | `/api/v1/fulfillment/packing-slips/{id}:cancel` | PackingSlipPost | Reversing slip with mirror lines and journal; Idempotency-Key |

Idempotency: a key is stored per tenant with a fingerprint of the method, path
and body. A replay returns the stored response; the same key on another path or
with another body is `idempotency_key_reuse`. Concurrent requests with one key
are serialised by an advisory lock, so the second replays the first.

Responses are JSON. Errors are RFC 9457 problem details (`application/problem+json`)
with a stable `code`.

## Error codes

Codes raised in this slice:

| Code | Meaning |
|---|---|
| `FUL-0001` | Shipment status does not allow the command (for example, not PACKED) |
| `FUL-0004` | Over-pick: picked exceeds planned |
| `FUL-0010` | Cannot cancel: the packing slip is invoiced (409) |
| `FUL-0017` | GS1-128 scan could not be parsed (web parser, `gs1.ts`) |
| `FUL-0020` | Shipment not found |
| `FUL-0021` | Invalid move between shipment states |
| `FUL-0023` | Over-pack: packed exceeds picked |
| `FUL-0030` | Negative quantity |
| `FUL-0040` | Packing slip not found |
| `FUL-0041` | Shipment line not found on this shipment |
| `FUL-0042` | Cost snapshot missing for a stocked line |
| `FUL-0043` | Packing slip cannot be cancelled: already cancelled, or itself a reversal (409) |
| `FUL-0044` | Nothing to ship: no packed quantity is waiting on the shipment |
| `FUL-0050` | Request failed validation; `errors` names the field (also a repeated line, lines of one order with different customers or legal entities, or more than 6 decimals) |
| `FUL-0051` | `Idempotency-Key` header missing or longer than 255 characters |
| `COM-0001` | Malformed JSON body (kernel code) |
| `precondition_required` | `If-Match` (shipment version) missing (428) |
| `conflict_version` | `If-Match` does not match the current version (412) |
| `idempotency_key_reuse` | Same Idempotency-Key with a different body (422) |

Catalog codes `FUL-0002`, `FUL-0003`, `FUL-0005` to `FUL-0009` and `FUL-0011` to
`FUL-0016` belong to later slices (period control, negative stock, tolerance,
tracking dimensions, carrier, saga, consolidation, packaging, RTP, workbench).

## Dependencies

Allowed by convention: platform kernel and what it pulls in (chi, pgx/v5,
shopspring/decimal, google/uuid), plus `pgregory.net/rapid` for tests. No new
libraries were added.

## Wave reference

PSG-28 / wave 28. Depends on PSG-27 (orders and holds), wave 16 (inventory
reservations), wave 20 (cost snapshots), wave 14 (period control).
