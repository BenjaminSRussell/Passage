# sales

The sales bounded context: sales orders, order holds, the credit check and
release, the stateless ATP promise check and credit profiles. Wave 27, slice 1
of PSG-27. Reference: `docs/prd/phase-5-order-to-cash.md` §5.27.

Out of this slice (see `NOT-IMPLEMENTED.md`): quotations, sales agreements,
the ATP projection and reservations, change versions, the backorder run,
allocation, consignment, supplementary items, the intake dedup window beyond
the order's own key, and the Temporal release workflow.

## Run

```sh
cd services/sales
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_sales
export DATABASE_MIGRATOR_URL=postgres://passage_owner:...@localhost:5432/passage_sales
export MIGRATE_ON_START=true   # local development only; stage/prod run migrations as an Argo PreSync Job
go run ./cmd/sales
```

Environment: `DATABASE_URL` (required), `MIGRATE_ON_START`, `HTTP_ADDR`
(default `:8080`), `ADMIN_ADDR` (default `:9090`, serves `/metrics`).

## Test

```sh
go vet ./...
go test ./...                                       # DB tests skip without the variable
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # run store and RLS tests
```

- `internal/domain`: table-driven and property tests (`pgregory.net/rapid`) for
  ATP cumulative propagation, the BR-27.1 worked example, the 100 fixtures of
  `testdata/atp-100.json` (AC-27.1, AC-27.2), the 40 credit fixtures of
  `testdata/credit-40.json` (CR-1..CR-6 with tolerance edges), line nets and
  quantity rules.
- `internal/api`: request validation, duty routing per hold type, list filters,
  wire formats, and that every route is in `api/openapi.yaml` and back.
- `internal/events`: every event type is in `events/asyncapi.yaml` and back.
- `internal/store`: create with idempotent replay and key reuse, the CREDIT hold
  and the release that confirms the order (AC-27.3) with the outbox rows each
  step writes, release scoped to one order, list filters and keyset paging,
  per-legal-entity numbering, stale `If-Match` (412), credit profile
  concurrency, channel duplicate rules (BR-27.10), and FORCE row-level security
  per tenant and legal entity. Uses `platform/go/testkit`, skipped without
  `PASSAGE_TEST_DATABASE_URL`.

Web module: `apps/web/src/modules/sales` — sales orders list, new order, order
detail (confirm, release holds, credit rules), credit release queue, credit
profiles list and form, and the ATP check (`npx vitest --run src/modules/sales`
and `npx tsc --noEmit` in `apps/web`).

## Endpoints

Contract: `api/openapi.yaml`. Events: `events/asyncapi.yaml`.

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/sales/orders` | `sales.order.write` or `sales.channel.intake` | `Idempotency-Key` required; `(channel, externalRef)` dedup |
| GET | `/v1/sales/orders` | `sales.order.read` | Newest first; `status`, `customerId`, `hold` (`hold=CREDIT` is the release queue), `cursor`, `limit` |
| GET | `/v1/sales/orders/{id}` | `sales.order.read` | ETag = version |
| POST | `/v1/sales/orders/{id}:confirm` | `sales.order.confirm` | `If-Match`; failing credit check places a CREDIT hold |
| POST | `/v1/sales/orders/{id}/holds/{holdId}:release` | `sales.credit.release` (CREDIT) or `sales.order.hold` (other types) | `If-Match`; releasing the CREDIT hold confirms the order when nothing else blocks it (AC-27.3) |
| POST | `/v1/sales/atp:check` | `sales.order.read` | Stateless BR-27.1 check over supplied buckets |
| GET | `/v1/sales/credit-profiles` | `sales.order.read` | Optional `legalEntityId`, `limit` |
| GET / PUT | `/v1/sales/credit-profiles/{customerId}` | `sales.order.read` / `sales.credit.maintain` | `legalEntityId` query or the caller's `Passage-Legal-Entity`; PUT takes `If-Match` (`"0"` to create) |
| GET | `/readyz` | none | Postgres `SELECT` within 500 ms |

## Rules implemented

- **BR-27.1 / FR-27.4** cumulative ATP: `cumulative[n] = min over k >= n of the prefix sum`;
  the search starts at the bucket that contains the requested date and never
  promises earlier than requested. A complete promise is the first bucket whose
  cumulative covers the quantity, otherwise the quantity is backordered in full
  (`completeOnly`); partial delivery splits over buckets.
- **BR-27.3 / FR-27.7** credit rules: CR-1 exposure against limit with
  percentage and amount tolerance, CR-2 oldest item, CR-3 dunning level, CR-4
  mandatory check, CR-5 overdue share (WARN), CR-6 young customer order cap.
  Any FAIL places a CREDIT hold; every rule is stored with value and threshold.
  Exposure is open AR less unapplied cash (both stored as non-negative amounts,
  so unapplied cash is a credit). Until the wave 12 spot rate is wired, an order
  in another currency than the profile fails CR-1 with a note rather than being
  compared across currencies.
- **BR-27.4 / FR-27.8** holds: CREDIT and FRAUD block confirmation; a CREDIT
  release confirms the order and stays valid for 7 days (`released_until`), so a
  re-confirm of the same order within that window and within 5% of the held
  amount does not re-hold. The user who applied a hold
  cannot release it. Releases and confirms are versioned by `If-Match`.
  (Blocking by MANUAL and PRICE_REVIEW on release is not yet enforced; see NOT-IMPLEMENTED.)
- **BR-27.10** intake: a repeat of `(channel, externalRef)` with the same body
  returns the original order with 200; a different body is a conflict.
- **BR-27.6** optimistic concurrency with `If-Match` and the version column.
- **Money** is `numeric(20,6)` decimal text on the wire; never float.

## Error codes

Problem details are `application/problem+json` with `type` =
`https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| `SLS-0005` | 422 | Credit hold applied (rule failed) |
| `SLS-0006` | 412 / 428 | `If-Match` stale (412), or missing (428) |
| `SLS-0007` | 409 | Duplicate `(channel, externalRef)` with a different payload |
| `SLS-0019` | 422 | Order not confirmable in its current state |
| `SLS-0021` | 422 | Order has an active hold that blocks this action |
| `SLS-0022` | 422 | Hold is not ACTIVE |
| `SLS-0100` | 404 | Order not found |
| `SLS-0101` | 404 | Hold not found |
| `SLS-0102` | 404 | Credit profile not found |
| `idempotency_key_reuse` | 422 | Same `Idempotency-Key` with a different body (kernel) |
| `COM-0001` | 400 | Request failed validation; field errors in `errors` |
| `AUTH-0001` | 401 | Missing or malformed tenant |
| `AUTH-0003` | 403 | Duty missing; order outside the caller's legal entity; or the user who applied a hold releasing it (SoD) |

## Dependencies

Go module only. Allowed dependencies: the platform module and what it pulls in
(chi, pgx/v5, shopspring/decimal, google/uuid). No new dependency.
