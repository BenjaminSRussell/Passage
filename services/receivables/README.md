# passage-receivables

Accounts receivable for Passage, wave 30 (PSG-30), slice 1. This increment covers
the cash-application core: open items, aging, manual payments, the scored
matcher (BR-30.1) and settlement validation (BR-30.4). Everything else in the
wave is listed in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## What it does now

- **Open items** (`GET /api/v1/receivables/open-items`): customer and status filters, page limit up to 500.
- **Aging** (`GET /api/v1/receivables/aging?asOf=`): totals per currency and bucket (`CURRENT`, `1-30`, `31-60`, `61-90`, `91-180`, `OVER-180`), with due date as the basis.
- **Payments**: record a manual payment (`POST /payments`, idempotent), list them (`GET /payments?status=&customer=&limit=`), read one (`GET /payments/{id}`, ETag = version), score candidate settlements (`POST /payments/{id}:match`, changes nothing) and apply (`POST /payments/{id}:apply`, requires `If-Match`; an optional `Idempotency-Key` replays the first response).
- **Apply rules**: settlements plus on-account residual equal the unapplied amount; every item is in the payment's legal entity and currency (no cross-currency settlement yet) and belongs to the payment's customer; an unidentified payment takes the customer of the items it settles; a cash discount needs the value date on or before the discount date and may not exceed the item's discount; write-off tolerance is the larger of 5.00 and 0.5% of the original item amount.
- **Events** via the transactional outbox: `dev.passage.receivables.payment.received.v1` and `dev.passage.receivables.payment.applied.v1` (see `events/asyncapi.yaml`).

The API contract is `api/openapi.yaml`. Every operation names its duty in
`x-passage-duty`.

## Layout

```
cmd/receivables/       entrypoint: dbx.Connect -> (dbx.Migrate) -> httpx router -> Serve
internal/domain/       pure rules: match.go (BR-30.1), settle.go (BR-30.4), aging.go (FR-30.6)
internal/store/        pgx repositories, RLS via dbx.WithTenantTx, outbox writes
internal/api/          HTTP handlers, DTOs, validation
internal/events/       event types and payloads
migrations/            0001 tables and RLS, 0002 outbox, 0003 idempotency_keys
api/openapi.yaml       REST contract
events/asyncapi.yaml   event contract
charts/receivables/    Helm chart (passage-receivables)
```

## Run

```sh
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_receivables
export MIGRATE_ON_START=true   # dev only; stage and prod run migrations as a PreSync Job
export HTTP_ADDR=:8080
go run ./cmd/receivables
```

Required: `DATABASE_URL`. Optional: `HTTP_ADDR` (default `:8080`), `ADMIN_ADDR`
(default `:9090`, serves `/metrics`), `MIGRATE_ON_START`.

## Test

```sh
go vet ./... && go test ./...
```

Domain tests are table-driven plus `pgregory.net/rapid` properties. Store and API
integration tests use `platform/go/testkit` and skip unless
`PASSAGE_TEST_DATABASE_URL` is set. Set it to run them against Postgres 17.

## Matching rules implemented

- R1 document number in the remittance as a whole token, so `INV-1001` does not match `INV-10012` (+60), R2 single item equal to the amount (+30) or a subset of up to 5 items (+25), R3 amount equals open minus available cash discount (+28), R6 value date within 7 days of due (+5).
- Auto-apply when the top set scores ≥ 85 and the runner-up is ≤ winner − 20; 60–84 is suggested; below 60 goes on account.
- R4, R5 and R7 need bank-account hashes, name similarity and short-pay reasons that slice 1 does not carry yet.

## Error codes

Errors are RFC 9457 problem details (`application/problem+json`) with a stable `code`.
The `type` is `https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| `AUTH-0001` | 401 | No principal, or tenant missing or malformed |
| `AUTH-0003` | 403 | Missing duty, or legal entity outside the caller's scope |
| `COM-0001` | 400 | Request failed validation (`errors` lists fields) |
| `COM-0428` | 428 | `If-Match` required on apply |
| `conflict_version` | 412 | `If-Match` does not match the payment version |
| `idempotency_key_reuse` | 422 | Same `Idempotency-Key` on create or apply with a different body |
| `idempotency_in_progress` | 409 | Two creates with the same `Idempotency-Key` raced; retry to replay |
| `AR-PAYMENT-NOT-FOUND` | 404 | Payment id unknown or unknown action |
| `AR-PAYMENT-DUPLICATE` | 409 | Payment or open item reference already recorded (unique violation) |
| `AR-PAYMENT-STATE` | 409 | Payment state does not allow apply (only RECEIVED, SUGGESTED, ON_ACCOUNT) |
| `AR-OPEN-ITEM-NOT-FOUND` | 404 | Open item in a settlement is unknown |
| `AR-0001` | 400 | Settlements plus residual must equal the payment amount, or the plan is invalid (other legal entity, currency or customer; discount not available or above terms) |
| `AR-0002` | 409 | Open item is locked by another settlement (BR-30.7) |
| `AR-0006` | 422 | Write-off exceeds tolerance |

`Idempotency-Key` is enforced on create by storing the key on the payment row
(unique per tenant). On apply the key and the response are stored in
`idempotency_keys` in the same transaction, after the payment row lock, so a
retry replays the first response instead of failing with 412.

## Web

`apps/web/src/modules/receivables/`: the AR workspace (aging by currency and
bucket, payments waiting), open items, payments, record payment, and the payment
page with match candidates and the apply plan.

## Dependencies

Go only: the platform kernel, chi, pgx/v5, shopspring/decimal. No new libraries.
