# sales

The sales bounded context: sales orders, order holds, the credit check and
release, the stateless ATP promise check, credit profiles, order change and
cancel with versions and diffs, the scheduling-release reconciliation check and
the backorder run preview, holds applied by hand (MANUAL, FRAUD, PRICE_REVIEW),
product listings and exclusions checked at order entry, sales agreements whose
releases create orders, quotations converted whole or by line into orders, the
hold release queue, supplementary item rules with a stateless evaluation, and
MANDATORY supplementary lines added to orders at commit that follow their
parent line. Wave 27, slices 1 to 5 of PSG-27. Reference:
`docs/prd/phase-5-order-to-cash.md` §5.27.

Out of these slices (see `NOT-IMPLEMENTED.md`): amount-based agreements and
scheduling-agreement release storage, the ATP projection and reservations,
change approval and `:cancel-remainder`, the committed backorder run,
allocation, consignment, OPTIONAL supplementary items prompted in an order-entry
grid, cross-selling, quotation expiry job and PDF, the intake dedup window beyond the
order's own key, and the Temporal release workflow.

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
  quantity rules; the field-level diff, line-status and re-promise rules of
  change management; the scheduling-release reconciliation (AC-27.13); and the
  five backorder strategies with a property test (no strategy allocates more
  than supply or leaves supply idle, WIN/GAIN never deconfirm, LOSE deconfirms
  only at or above the threshold).
- `internal/api`: request validation, duty routing per hold type, list filters,
  PATCH/cancel/history contracts, the release check and backorder preview, wire
  formats, and that every route is in `api/openapi.yaml` and back.
- `internal/events`: every event type is in `events/asyncapi.yaml` and back.
- `internal/store`: create with idempotent replay and key reuse, the CREDIT hold
  and the release that confirms the order (AC-27.3) with the outbox rows each
  step writes, release scoped to one order, list filters and keyset paging,
  per-legal-entity numbering, stale `If-Match` (412), credit profile
  concurrency, channel duplicate rules (BR-27.10), change of a confirmed order
  (AC-27.7: version, stored diff, only the changed line re-promised,
  `order.changed`), the credit re-check when a change raises the total, cancel
  (lines cancelled, holds cleared, out of the release queue, `order.cancelled`,
  refused with a picked line), and FORCE row-level security per tenant and
  legal entity, including the version history; product listing denial and
  override (AC-27.11), group precedence and validity on the order date;
  agreement releases (AC-27.9: order at the agreement price, exhaustion, replay,
  key reuse, `agreement.released`), validity and concurrent releases that never
  overdraw; holds applied by hand (FRAUD blocks confirm, one per type, SoD on
  release, none on a cancelled order). Uses `platform/go/testkit`, skipped without
  `PASSAGE_TEST_DATABASE_URL`.

Web module: `apps/web/src/modules/sales` — sales orders list, new order, order
detail (confirm, release holds, credit rules, change history), order change
and cancel, credit release queue, credit profiles list and form, the ATP check,
the scheduling-release check, the backorder run preview, apply hold and listing
overrides on the order detail, product listings (list, new entry, remove) and
sales agreements (list, new, detail with release-order and history) (`npx vitest --run src/modules/sales`
and `npx tsc --noEmit` in `apps/web`).

## Endpoints

Contract: `api/openapi.yaml`. Events: `events/asyncapi.yaml`.

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/sales/orders` | `sales.order.write` or `sales.channel.intake` | `Idempotency-Key` required; `(channel, externalRef)` dedup |
| GET | `/v1/sales/orders` | `sales.order.read` | Newest first; `status`, `customerId`, `hold` (orders with an ACTIVE hold of the type), `cursor`, `limit` |
| GET | `/v1/sales/orders/{id}` | `sales.order.read` | ETag = version |
| POST | `/v1/sales/orders/{id}:confirm` | `sales.order.confirm` | `If-Match`; failing credit check places a CREDIT hold |
| POST | `/v1/sales/orders/{id}/holds/{holdId}:release` | `sales.credit.release` (CREDIT) or `sales.order.hold` (other types) | `If-Match`; releasing the CREDIT hold confirms the order when nothing else blocks it (AC-27.3) |
| PATCH | `/v1/sales/orders/{id}` | `sales.order.write` | `If-Match`; header and OPEN/RESERVED line edits; new version with a field-level diff; re-promises changed quantities/dates of a CONFIRMED order and re-runs credit when its total rises; emits `order.changed` |
| POST | `/v1/sales/orders/{id}:cancel` | `sales.order.confirm` | `If-Match`; optional `reason`; cancels OPEN/RESERVED lines, clears ACTIVE holds; emits `order.cancelled` |
| GET | `/v1/sales/orders/{id}/versions` | `sales.order.read` | Change history, newest first, with each version's diff |
| POST | `/v1/sales/orders/{id}/holds` | `sales.order.hold` | `If-Match`; MANUAL, FRAUD or PRICE_REVIEW with a reason code; emits `orderhold.applied` |
| GET / POST | `/v1/sales/product-listings` | `sales.order.read` / `sales.listing.maintain` | Listing inquiry (`legalEntityId`, `productId`, `scopeKey`, `limit`); add an ALLOW or DENY entry per customer or group |
| DELETE | `/v1/sales/product-listings/{id}` | `sales.listing.maintain` | Remove an entry |
| GET / POST | `/v1/sales/agreements` | `sales.order.read` / `sales.agreement.maintain` | List (`legalEntityId`, `customerId`, `status`, `limit`); create with `Idempotency-Key`, numbered `SA-` |
| GET | `/v1/sales/agreements/{id}` | `sales.order.read` | With remaining quantity and release history |
| POST | `/v1/sales/agreements/{id}:release-order` | `sales.agreement.maintain` | `Idempotency-Key`; creates a DRAFT order at the agreement price and decrements the commitment atomically; emits `agreement.released` |
| POST | `/v1/sales/atp:check` | `sales.order.read` | Stateless BR-27.1 check over supplied buckets |
| POST | `/v1/sales/schedule-releases:check` | `sales.agreement.maintain` | Stateless BR-27.12 reconciliation; a mismatch returns `held: true`, `code: CUM_QTY_MISMATCH` and no schedule lines |
| POST | `/v1/sales/backorder-runs:preview` | `sales.backorder.run` | Stateless FR-27.17 preview over supplied lines; nothing is committed |
| GET | `/v1/sales/credit-profiles` | `sales.order.read` | Optional `legalEntityId`, `limit` |
| GET / PUT | `/v1/sales/credit-profiles/{customerId}` | `sales.order.read` / `sales.credit.maintain` | `legalEntityId` query or the caller's `Passage-Legal-Entity`; PUT takes `If-Match` (`"0"` to create) |
| GET / POST | `/v1/sales/quotations` | `sales.order.read` / `sales.quotation.maintain` | List (`legalEntityId`, `customerId`, `status`, `limit`) without lines; create a DRAFT with `Idempotency-Key`, numbered `SQ-`, validity default +30 days, probability 50 |
| GET | `/v1/sales/quotations/{id}` | `sales.order.read` | With lines; converted lines name their order |
| POST | `/v1/sales/quotations/{id}:send` | `sales.quotation.maintain` | `If-Match`; DRAFT to SENT while valid |
| POST | `/v1/sales/quotations/{id}:lose` | `sales.quotation.maintain` | `If-Match`; `{reason}`; DRAFT or SENT to LOST |
| POST | `/v1/sales/quotations/{id}:convert` | `sales.quotation.maintain` | `If-Match`; optional `{lineNos, listingOverrideReason}` (empty converts every open line); 201 with the CONVERTED quotation and one DRAFT order; emits `quotation.converted` |
| GET | `/v1/sales/holds` | `sales.credit.release` or `sales.order.hold` | Release queue: ACTIVE holds oldest first (`legalEntityId`, `holdType`, `limit`) with the failed credit rule, the customer's credit limit and the order version for the release call |
| GET / POST | `/v1/sales/supplementary-item-rules` | `sales.order.read` / `sales.listing.maintain` | List (`legalEntityId`, `parentProductId`, `limit`); create a rule (one per parent, customer or all customers, and item) |
| DELETE | `/v1/sales/supplementary-item-rules/{id}` | `sales.listing.maintain` | Remove a rule |
| POST | `/v1/sales/supplementary-items:evaluate` | `sales.order.read` | Stateless: the MANDATORY and OPTIONAL supplementary lines a parent product and quantity trigger for a customer |
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
  MANUAL, FRAUD and PRICE_REVIEW holds are applied by hand with
  `sales.order.hold` on a DRAFT, OPEN or CONFIRMED order (one ACTIVE hold per
  type); CREDIT holds only come from the credit check. (Blocking by MANUAL and
  PRICE_REVIEW on warehouse release belongs with w28; see NOT-IMPLEMENTED.)
- **BR-27.13 / FR-27.15** listings: every line of a new order (including an
  agreement release) is checked on the order date. A customer DENY beats
  everything, then customer ALLOW, group DENY, group ALLOW; a product with no
  ALLOW entry is unrestricted, otherwise a customer not on the list is
  NOT_LISTED. A denied line is 422 `SLS-0011` unless the caller has
  `sales.order.approve` and gives `listingOverrideReason`; each accepted line is
  logged on the order (`listingOverrides`). Group entries apply when the order
  names its `customerGroup` (until the w7 party lookup supplies it).
- **BR-27.9 / FR-27.10** agreements: a quantity commitment at an agreed price.
  `:release-order` locks the agreement row, refuses a release beyond the
  remaining quantity (`SLS-0008` AGREEMENT_EXHAUSTED, AC-27.9) or outside the
  validity period (`SLS-0029`), creates the DRAFT order (channel `AGREEMENT`)
  and stores the release in the same transaction. At the commitment the
  agreement is EXHAUSTED. Overdelivery is 0 until the parameter table exists.
- **FR-27.9 / BR-27.5** change management: PATCH changes the header date and
  OPEN/RESERVED lines of a DRAFT, OPEN or CONFIRMED order. Each change that
  alters something bumps the version, stores the field-level diff in
  `sales_order_version` and emits `order.changed`; a no-op keeps the version.
  On a CONFIRMED order a new quantity or requested date re-promises that line
  only (its confirmed date follows the requested date until the ATP projection
  exists), and a higher total re-runs the credit check: a FAIL that no valid
  CREDIT release covers (BR-27.4, amount + 5%) places a CREDIT hold while the
  order stays CONFIRMED. Cancel is allowed from DRAFT, OPEN or CONFIRMED when no
  line is past RESERVED; it cancels the lines, clears ACTIVE holds (`CLEARED`)
  and emits `order.cancelled`.
- **BR-27.12** scheduling-release check: the customer's cumulative received
  quantity is compared with the cumulative shipped; a difference beyond the
  tolerance holds the release (`CUM_QTY_MISMATCH`) with no schedule lines;
  otherwise the open lines are the increments of cumulative required − shipped.
- **BR-27.11 / FR-27.17** backorder preview: lines are processed by priority
  class, requested date, then order value (descending). WIN and GAIN keep every
  confirmed quantity (GAIN then serves the smallest open demand first);
  REDISTRIBUTE may move confirmed quantity to higher priority; FILL serves by
  requested date; LOSE may deconfirm only lines at or above `loseThreshold`
  (default 3). A run that cannot keep a protected confirmation is refused.
- **FR-27.1 / BR-27.5** quotations: DRAFT → SENT → CONVERTED or LOST (EXPIRED
  and CANCELLED exist as states; nothing sets them yet). A quotation is valid
  through its `validUntil` day (default +30 days, at most 365); an expired
  quotation can be neither sent nor converted (`SLS-0031`). `:convert` takes
  the quotation's row lock and, in one transaction, creates one DRAFT order
  (channel `QUOTE`) from the selected OPEN lines at the quoted price and
  discount, marks those lines CONVERTED with the order id, moves the quotation
  to CONVERTED and writes `quotation.converted`; the remaining OPEN lines can
  convert later into another order. The order passes the product listing check
  like any order, so a denied line refuses the whole conversion (`SLS-0011`)
  unless the caller has `sales.order.approve` and gives a reason (AC-27.6).
- **FR-27.14** supplementary items: a rule gives `quantityPerBase` of an item
  for every whole `baseQuantity` of the ordered product (AC-27.10: 1 per 10, so
  25 gives 2), for one customer or every customer of the legal entity; a
  customer's own rule replaces the all-customers rule for the same item. FREE
  items carry unit price 0, PRICED items a positive price. The evaluation is
  stateless. Every new order (UI or channel create, quotation conversion,
  agreement release) gets the lines of the MANDATORY rules at commit, after its
  own lines, each naming its parent line (`supplementaryOf`, `parentLineNo`)
  and returned as an `SLS-0012` warning; OPTIONAL rules are only offered by the
  evaluation. **BR-27.14**: a supplementary line inherits its parent's site,
  warehouse and dates, cannot be changed on its own (`SLS-0024`), is
  recomputed when the parent's quantity changes, is cancelled when the parent
  drops below the rule's base quantity, and is cancelled with the order.
- **BR-27.10** intake: a repeat of `(channel, externalRef)` with the same body
  returns the original order with 200; a different body is a conflict.
- **BR-27.6** optimistic concurrency with `If-Match` and the version column.
- **Money** is decimal text on the wire (`numeric(19,4)` for nets and totals, `numeric(20,6)` for prices and quantities); never float.

## Error codes

Problem details are `application/problem+json` with `type` =
`https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| `SLS-0005` | 422 | Credit hold applied (rule failed) |
| `SLS-0006` | 412 / 428 | `If-Match` stale (412), or missing (428) |
| `SLS-0007` | 409 | Duplicate `(channel, externalRef)` with a different payload; or a concurrent create of the same agreement, release or quotation |
| `SLS-0008` | 422 | Agreement commitment exhausted (`AGREEMENT_EXHAUSTED`) |
| `SLS-0011` | 422 | Product not listed for the customer (`PRODUCT_NOT_LISTED`) |
| `SLS-0012` | 201 (warning) | Mandatory supplementary item added at commit (`warnings[]` on the order) |
| `SLS-0014` | 200 (held) | Cumulative quantity mismatch; the scheduling release is held (`code` = `CUM_QTY_MISMATCH`) |
| `SLS-0015` | 200 (warning) | Backorder run preview would deconfirm lines (`warningCode`) |
| `SLS-0019` | 422 | Order not confirmable in its current state |
| `SLS-0021` | 422 | Order has an active hold that blocks this action |
| `SLS-0022` | 422 | Hold is not ACTIVE |
| `SLS-0023` | 422 | Order is shipped, closed or cancelled and cannot be changed |
| `SLS-0024` | 422 | Change refused: unknown line, a line that is not OPEN or RESERVED, a supplementary line's own quantity or date, invalid quantity, bad price or date, or reason too long |
| `SLS-0025` | 422 | Order cannot be cancelled: shipped, closed or cancelled, or a line is picked, delivered or invoiced |
| `SLS-0026` | 422 | Scheduling release refused: negative quantities, unordered or decreasing buckets |
| `SLS-0027` | 422 | Backorder run refused: unknown strategy, supply below zero, or a WIN, GAIN or LOSE run that cannot keep a protected confirmation |
| `SLS-0028` | 422 | Hold cannot be applied: CREDIT by hand, the order is shipped, closed or cancelled, or an ACTIVE hold of the type exists |
| `SLS-0029` | 422 | Agreement is not valid on the release date |
| `SLS-0030` | 422 | Quotation status does not allow the action (send a non-DRAFT, convert a DRAFT or LOST, lose a CONVERTED) |
| `SLS-0031` | 422 | Quotation validity has passed |
| `SLS-0032` | 422 | Selected quotation lines are not open (already converted, unknown or repeated) or none remain |
| `SLS-0033` | 409 | A supplementary item rule for the parent, customer (or all customers) and item exists |
| `SLS-0100` | 404 | Order not found |
| `SLS-0101` | 404 | Hold not found |
| `SLS-0102` | 404 | Credit profile not found |
| `SLS-0103` | 404 | Sales agreement not found |
| `SLS-0104` | 404 | Product listing not found |
| `SLS-0105` | 404 | Quotation not found |
| `SLS-0106` | 404 | Supplementary item rule not found |
| `idempotency_key_reuse` | 422 | Same `Idempotency-Key` with a different body (kernel) |
| `COM-0001` | 400 | Request failed validation; field errors in `errors` |
| `AUTH-0001` | 401 | Missing or malformed tenant |
| `AUTH-0003` | 403 | Duty missing; order outside the caller's legal entity; or the user who applied a hold releasing it (SoD) |

## Dependencies

Go module only. Allowed dependencies: the platform module and what it pulls in
(chi, pgx/v5, shopspring/decimal, google/uuid). No new dependency.
