# purchasing

Passage purchasing service. Wave 17 (PSG-17), slice 1 of the procurement
context: purchase requisitions with amount/category approval routing, purchase
orders with persisted totals, trade agreement price lookup with explain, and the
web workspace module at `apps/web/src/modules/purchasing`.

Full ticket scope and the list of what is not yet built are in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## Layout

```
cmd/purchasing/        entrypoint: dbx.Connect -> dbx.Migrate -> api.Register -> httpx.Serve
internal/domain/       pure rules: approval bands, trade agreement lookup, PO totals, reapproval
internal/store/        Postgres repositories (pgx), every transaction via dbx.WithTenantTx
internal/api/          HTTP handlers under /v1/procurement, duties via authz.RequireDuty
internal/events/       event types and payloads (dev.passage.procurement.*)
migrations/            0001_init.sql (tables, sequences, RLS, outbox, idempotency, total trigger)
api/openapi.yaml       OpenAPI 3.1 for every route
events/asyncapi.yaml   AsyncAPI 3 for every event emitted
charts/purchasing/     Helm chart (probes on /healthz and /readyz, DATABASE_URL from a secret)
testdata/              approval matrix fixture (30 requisitions: 6 amounts across the 4 bands x 5 categories)
Dockerfile             distroless static image (build from the repo root)
```

## Run

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/purchasing
```

Environment: `DATABASE_URL` (required), `MIGRATE_ON_START` (dev only), `HTTP_ADDR`
(default `:8080`), `ADMIN_ADDR` (default `:9090`, serves `/metrics`). The gateway
supplies `X-Passage-Tenant`, legal-entity scope, user and duties (the kernel tenant
middleware until JWT validation lands).

## Test

```
go vet ./... && go test ./...
```

Domain tests are pure (table-driven plus `rapid` property tests). `internal/api`
tests drive the real router over an in-memory store. `internal/store` tests use
`platform/go/testkit` and skip unless `PASSAGE_TEST_DATABASE_URL` is set; the
row-level-security test also needs `PASSAGE_TEST_APP_DATABASE_URL` (a non-owner
`passage_app` role) and skips without it.

## Routes (`/v1/procurement`; browser: `/api/v1/procurement` via the BFF)

| Method and path | Duty | Notes |
|---|---|---|
| POST `/requisitions` | RequisitionCreate | Idempotency-Key required; max 500 lines; blank estimates default from the preferred vendor's trade agreement, else 0 with warning PRC-REQ-003 |
| GET `/requisitions` | RequisitionCreate | Newest first; `status`, `mine=true`, `limit` (≤ 200), `cursor` |
| GET `/requisitions/{id}` | RequisitionCreate | ETag = version |
| POST `/requisitions/{id}:submit` | RequisitionCreate | Idempotency-Key and If-Match (version) required; requester only; routes approvers |
| POST `/purchase-orders` | PurchaseOrderMaintain | Idempotency-Key required; missing unit prices come from trade agreements |
| GET `/purchase-orders` | PurchaseOrderInquire | Newest first; `approvalStatus`, `vendorId`, `limit`, `cursor` |
| GET `/purchase-orders/{id}` | PurchaseOrderInquire | ETag = row version |
| GET `/pricing/lookup` | PurchaseOrderMaintain | Winner plus candidate explain list; `vendorGroup`/`productGroup` add memberships; `tie` = LATEST_FROM_DATE (default) or LOWEST_PRICE |
| POST `/trade-agreements` | TradeAgreementMaintain | PRICE relation only in this slice; PRC-TA-002 on an overlapping row |
| GET `/trade-agreements` | TradeAgreementMaintain | Newest first; `accountId`, `itemId`, `status`, `limit`, `cursor` |
| GET `/trade-agreements/{id}` | TradeAgreementMaintain | |

Writes for a legal entity outside the caller's `X-Passage-Legal-Entity` scope are
refused with AUTH-0003 before they reach the database (RLS would refuse them too).

Health: `GET /healthz` (kernel) and `GET /readyz` (pings Postgres, 503 when it is
down). Metrics: `GET :9090/metrics` (Prometheus text counters).

## Business rules implemented

- **Approval routing (FR-17.2).** Default bands: up to 1,000 auto-approve; up to
  10,000 manager; up to 100,000 manager and category owner; above that also CFO.
  Each distinct line category is routed on the requisition total and the most
  senior chain wins. The chain is stored with the policy version `builtin-1`. An
  empty chain approves the requisition on submit and writes `requisition.approved`.
- **Trade agreement lookup (FR-17.6).** The store loads only rows for the vendor,
  its groups or all vendors and the product, its groups or all products, valid on
  the date, in the currency and unit, active and not consignment. The domain drops
  rows outside the quantity break (`from_qty ≤ qty < to_qty`) and ranks the rest:
  specificity = account score (vendor 4, group 2, all 0) plus item score (product
  4, group 2, all 0); then the latest `valid_from`; then the lowest unit price
  (`tie=LOWEST_PRICE` swaps the last two). Every row considered is in `candidates`.
- **Overlap at activation (BR-17.3).** A new ACTIVE row with the same account, item,
  currency, unit, consignment flag and `valid_from` as an ACTIVE row whose quantity
  break overlaps is refused with PRC-TA-002.
- **Totals (FR-17.11, BR-17.5).** Line amounts are rounded half-even once from the
  exact qty × price ÷ price unit. Header totals are sums of rounded lines.
  `total = subtotal − discount_total + charges_total + tax_total` is checked by a
  deferred constraint trigger in the database.
- **Price source (FR-17.7).** Each PO line stores `MANUAL` or `TRADE_AGREEMENT:<id>`;
  the price date is the order date (BR-17.3 default).
- **Idempotency.** Creates and `:submit` store the key with a hash of the operation
  and body; a replay returns the original resource, a different body (or endpoint)
  with the same key is `idempotency_key_reuse`.

## Error codes

Problems are RFC 9457 with `code` set to the stable value below. Domain and
store codes are returned as the `code` field.

| Code | HTTP | Meaning |
|---|---|---|
| COM-0001 | 400/422 | Malformed body or invalid field (`errors` names the field) |
| AUTH-0001 | 401 | Missing tenant or user in the caller context |
| PRC-IDEM-001 | 400 | Idempotency-Key header missing |
| PRC-IFMATCH-001 | 428 | If-Match header missing on a custom method |
| PRC-IFMATCH-002 | 400 | If-Match is not a version number |
| AUTH-0003 | 403 | Caller lacks the route's duty, or the legal entity is outside the caller's scope |
| PRC-REQ-002 | 422 | Requisition has more than 500 lines |
| PRC-REQ-003 | warning | Returned in `warnings` on create: no price found, estimate set to 0 |
| PRC-REQ-004 | 403 | Only the requester can submit the requisition |
| PRC-REQ-NOT-FOUND | 404 | Requisition not found |
| PRC-REQ-STATE | 409 | Requisition is not in a state that allows the action |
| PRC-PO-NOT-FOUND | 404 | Purchase order not found |
| PRC-TA-NOT-FOUND | 404 | Trade agreement not found |
| PRC-TA-002 | 409 | Overlapping ACTIVE trade agreement row with equal specificity and valid_from |
| PRC-VERSION-CONFLICT | 409 | Requisition is at another version than If-Match |
| PRC-PRICE-001 | 422 | No trade agreement price for the line; enter a unit price |
| PRC-PRICE-002 | 422 | Unknown tie rule |
| PRC-PO-TOTAL | 500 (COM-0500) | Database total identity violated; the trigger refuses the write and the client sees COM-0500. Should not occur because totals come from `domain.Price` |
| PRC-ACTION-001 | 404 | Unknown custom action on a requisition |
| idempotency_key_reuse | 422 | Idempotency key reused with a different body or on another endpoint |

## Dependencies

Allowed set only: platform kernel, chi (through the kernel), pgx/v5,
shopspring/decimal, and `pgregory.net/rapid` for property tests (already in the
platform go.mod).

## Web module

`apps/web/src/modules/purchasing` gives the procurement workspace: requisition,
purchase order and trade agreement lists with create forms and detail pages, and
the price lookup with explain. See its README.
