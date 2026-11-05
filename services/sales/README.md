# sales

The sales bounded context: sales orders, order holds, the credit check and
release, the stateless ATP promise check, credit profiles, order change and
cancel with versions and diffs, the scheduling-release reconciliation check and
the backorder run preview, holds applied by hand (MANUAL, FRAUD, PRICE_REVIEW),
product listings and exclusions checked at order entry, sales agreements whose
releases create orders, quotations converted whole or by line into orders, the
hold release queue, supplementary item rules with a stateless evaluation, and
MANDATORY supplementary lines added to orders at commit that follow their
parent line, and product proposal rules (cross-sell, up-sell, accessory) with
a stateless evaluation at line commit and recorded acceptance. Wave 27, slices
1 to 6 of PSG-27. Reference:
`docs/prd/phase-5-order-to-cash.md` §5.27.

Out of these slices (see `NOT-IMPLEMENTED.md`): amount-based agreements and
scheduling releases driven by w28 shipments and EDI, the ATP projection and reservations,
the committed backorder run,
allocation, consignment, OPTIONAL supplementary items prompted in an order-entry
grid, w8 related products as a proposal source, the hourly scheduler for quotation expiry, the quotation PDF, the intake dedup window beyond the
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
(default `:8080`), `ADMIN_ADDR` (default `:9090`, serves `/metrics`),
`FIELDCRYPT_KEYS_FILE` (or `FIELDCRYPT_KEYS`) and `FIELDCRYPT_CURRENT_KEY_ID`
(platform field encryption: `id=base64(32 bytes)` pairs; they seal the buyer of
a one-time customer order and key its idempotency fingerprint. Unset, one-time
orders are refused with 503 `SLS-0054`; in the chart, `fieldCrypt.keysSecret`
mounts the keys file and `fieldCrypt.currentKeyId` names the current key).

## Test

```sh
go vet ./...
go test ./...                                       # DB tests skip without the variable
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # run store and RLS tests
```

- `internal/domain`: table-driven and property tests (`pgregory.net/rapid`) for
  ATP cumulative propagation, the BR-27.1 worked example, the 100 fixtures of
  `testdata/atp-100.json` (AC-27.1, AC-27.2; regenerate with
  `python3 -I testdata/gen_atp100.py`, the independent reference a test compares
  the file with), the no-over-promise property over over-committed projections,
  the 40 credit fixtures of
  `testdata/credit-40.json` (CR-1..CR-6 with tolerance edges), line nets and
  quantity rules; the field-level diff, line-status and re-promise rules of
  change management; the scheduling-release reconciliation (AC-27.13); and the
  five backorder strategies with a property test (no strategy allocates more
  than supply or leaves supply idle, WIN/GAIN never deconfirm, LOSE deconfirms
  only at or above the threshold).
- `internal/api`: request validation, duty routing per hold type, list filters,
  PATCH/cancel/history contracts, the release check and backorder preview, wire
  formats, and that every route is in `api/openapi.yaml` and back; with the
  database, the intake load proof (500 orders/min open loop, p99 < 500 ms,
  exactly once; `-short` skips it, `PASSAGE_SALES_INTAKE_MINUTES` lengthens it).
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

Events are written to the outbox in the transaction that makes the change. The `outbox-sales`
Debezium connector (`deploy/kafka/connect.yaml`) streams the outbox through the
`passage_outbox_sales` publication (migration 0039) as the `debezium_cdc_sales` role (grants in
migration 0040) and routes each event by its type to `passage.sales.<entity>`, the AsyncAPI
channel (`events.Topic`), not by `aggregate_type`: most order-side events share aggregate_type
`order`, but `orderprogress.recorded` lands on `passage.sales.orderprogress` (read by inventory's
postings consumer) and `orderhold.*` on `passage.sales.orderhold` (read by fulfillment). A new
entity needs a literal ACL for the `debezium` Kafka user in `connect.yaml`.

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/sales/orders` | `sales.order.write` or `sales.channel.intake` | `Idempotency-Key` required; `(channel, externalRef)` dedup; optional `oneTime` {name, line1, city, postalCode, country, taxId} makes it a one-time customer order held to `one_time_order_limit` (`SLS-0016`); the buyer is sealed at rest and needs field encryption keys (`SLS-0054`) |
| GET | `/v1/sales/orders` | `sales.order.read` | Newest first; `status`, `customerId`, `hold` (orders with an ACTIVE hold of the type), `cursor`, `limit` |
| GET | `/v1/sales/orders/{id}` | `sales.order.read` | ETag = version |
| POST | `/v1/sales/orders/{id}:confirm` | `sales.order.confirm` | `If-Match`; failing credit check places a CREDIT hold; draws allocation quota per line (`SLS-0013` warning when a line moves to a later period, 422 when none can cover it) |
| POST | `/v1/sales/orders/{id}/holds/{holdId}:release` | `sales.credit.release` (CREDIT) or `sales.order.hold` (other types) | `If-Match`; releasing the CREDIT hold confirms the order when nothing else blocks it (AC-27.3); when allocation quota cannot cover that confirmation the hold is still released and the order stays unconfirmed with an `SLS-0013` warning |
| POST | `/v1/sales/orders/{id}/holds/{holdId}:reject` | `sales.credit.release` | `If-Match`; body `{comment}` (1 to 500 characters, required); rejects an ACTIVE CREDIT hold of a DRAFT or OPEN order and cancels the order in the same transaction (other ACTIVE holds are CLEARED, `orderhold.rejected` and `order.cancelled` are emitted); the applier may not reject their own hold |
| PATCH | `/v1/sales/orders/{id}` | `sales.order.write` | `If-Match`; header and OPEN/RESERVED line edits, `addLines` and `removeLines` (an added line is listed-checked, gets its MANDATORY supplementary lines and, when confirmed, is promised; a removed line is cancelled and keeps its number, and on a CONFIRMED order also goes out on `order.remaindercancelled` with nothing kept; a lowered, raised or reopened line of a CONFIRMED order goes out on it with its new quantity kept); new version with a field-level diff; re-promises changed quantities/dates of a CONFIRMED order and re-runs credit when its total rises; emits `order.changed` |
| POST | `/v1/sales/orders/{id}:plan-production` | `sales.order.plan-production` | creates, for each configured line of a CONFIRMED order, the production order built to the line's saved configuration (origin `SalesOrderLine`) through the production service and links it to the line in `order_line_production`; idempotent per line; a line's `configurationId` is checked against the product service on order create |
| POST | `/v1/sales/orders/{id}:cancel` | `sales.order.confirm` | `If-Match`; optional `reason`; cancels OPEN/RESERVED lines, clears ACTIVE holds; emits `order.cancelled` (and, for a CONFIRMED order, `order.remaindercancelled` with status CANCELLED and every line kept at 0). For a customer whose changes need approval, a caller without `sales.order.approve` gets 202 and a stored request of kind `CANCEL` instead (`SLS-0043` if one already waits) |
| POST | `/v1/sales/orders/{id}:cancel-remainder` | `sales.order.write` | `If-Match`; optional `reason`; on a PARTIALLY_SHIPPED order cancels undelivered lines and closes part-delivered lines at the delivered quantity, lowers the net total, returns quota and agreement quantity, leaves holds as they are; the order becomes SHIPPED (INVOICED when all delivered was invoiced); emits `order.remaindercancelled` (`SLS-0025` on any other status) |
| POST | `/v1/sales/orders/{id}/lines/{lineNo}:cancel-remainder` | `sales.order.write` | `If-Match`; optional `reason`; the same close for one line of a PARTIALLY_SHIPPED order, other lines untouched, the order status follows its lines; emits `order.remaindercancelled` for that line (`SLS-0025` when the order is not partly shipped or the line has nothing left, `SLS-0109` unknown line) |
| GET | `/v1/sales/orders/{id}/versions` | `sales.order.read` | Change history, newest first, with each version's diff |
| POST | `/v1/sales/orders/{id}/holds` | `sales.order.hold` | `If-Match`; MANUAL, FRAUD or PRICE_REVIEW with a reason code; emits `orderhold.applied` |
| GET / POST | `/v1/sales/product-listings` | `sales.order.read` / `sales.listing.maintain` | Listing inquiry (`legalEntityId`, `productId`, `scopeKey`, `limit`); add an ALLOW or DENY entry per customer or group |
| DELETE | `/v1/sales/product-listings/{id}` | `sales.listing.maintain` | Remove an entry |
| GET / POST | `/v1/sales/agreements` | `sales.order.read` / `sales.agreement.maintain` | List (`legalEntityId`, `customerId`, `status`, `limit`); create with `Idempotency-Key`, numbered `SA-` |
| GET | `/v1/sales/agreements/{id}` | `sales.order.read` | With remaining quantity and release history |
| PATCH | `/v1/sales/agreements/{id}` | `sales.agreement.maintain` | `If-Match`; revise `commitmentQty` (not below the released quantity, `SLS-0053`), `unitPrice` (later releases only), `cumTolerance`, `validFrom`/`validTo` (empty clears) with an optional `reason`; a closed agreement is `SLS-0052`; a version with the field diff; emits `agreement.changed` |
| POST | `/v1/sales/agreements/{id}:close` | `sales.agreement.maintain` | `If-Match`; `reason` required; CLOSED takes no more releases, schedule releases or revisions (`SLS-0052`); emits `agreement.closed` |
| GET | `/v1/sales/agreements/{id}/versions` | `sales.order.read` | Revisions, the close and quantity returned by cancelled orders, newest first, with each entry's diff |
| POST | `/v1/sales/agreements/{id}:release-order` | `sales.agreement.maintain` | `Idempotency-Key`; creates a DRAFT order at the agreement price and decrements the commitment atomically; emits `agreement.released` |
| GET / POST | `/v1/sales/agreements/{id}/schedule-releases` | `sales.order.read` / `sales.agreement.maintain` | Releases newest first; store a FORECAST or JIT release (`Idempotency-Key`, `releaseNo`, `cumReceived`, `buckets`): ACCEPTED replaces the previous release's open lines, a cumulative mismatch beyond the agreement tolerance is stored HELD (`CUM_QTY_MISMATCH`, `SLS-0014`) and changes nothing; refused with `SLS-0029` outside the agreement's validity; emits `schedulerelease.received` |
| GET | `/v1/sales/agreements/{id}/schedule-releases/{releaseId}` | `sales.order.read` | The release with its schedule lines |
| POST | `/v1/sales/agreements/{id}/schedule-releases/{releaseId}:resolve` | `sales.agreement.maintain` | `If-Match`; `ACCEPT` (override) or `REJECT` a HELD release with a reason; emits `schedulerelease.resolved` |
| POST | `/v1/sales/agreements/{id}:record-shipped` | `sales.agreement.maintain` | `If-Match`; sets the cumulative shipped quantity (and optionally a later reset date, which carries the closed period's shipped quantity into the releases' `shippedOffset`) and recalculates the open lines of the release in effect; emits `schedulerelease.recalculated` |
| POST | `/v1/sales/orders/{id}/lines/{lineNo}:promise` | `sales.order.write` | `If-Match`; places one line of a CONFIRMED order against the supplied ATP buckets (`periods`, `completeOnly`) and stores its schedule lines: CONFIRMED up to the requested date, BACKORDER after it, UNPROMISED for quantity no bucket covers (FR-27.6, AC-27.8). New order version, `order.changed` and `atp.promised` |
| GET | `/v1/sales/orders/{id}/schedule-lines` | `sales.order.read` | The order's active schedule lines |
| GET | `/v1/sales/backorders` | `sales.order.read` | Backorder list by promised date, undated last (`legalEntityId`, `productId`, `customerId`, `promisedTo`, `limit`) |
| POST | `/v1/sales/atp:check` | `sales.order.read` | Stateless BR-27.1 check over supplied buckets |
| GET / POST | `/v1/sales/backorder-runs` | `sales.order.read` / `sales.backorder.run` | Stored backorder runs (`legalEntityId`, `productId`, `status`, `limit`); plan a run (`Idempotency-Key`, `legalEntityId`, `productId`, `siteId`, `warehouseId`, `strategy`, `supply`, `supplyDate`, optional `loseThreshold`, `defaultPriorityClass`, `priorityClasses`) over the open lines of the CONFIRMED orders and store it as a PREVIEW that changes nothing (FR-27.17, BR-27.11); `SLS-0027` when no line is open or a protected confirmation cannot be kept |
| GET | `/v1/sales/backorder-runs/{id}` | `sales.order.read` | The run with its lines in processing order (promised quantity before and after, gain date, order version the preview used) |
| POST | `/v1/sales/backorder-runs/{id}:commit` | `sales.backorder.run` | `If-Match`; applies a PREVIEW in one transaction serialised per product, site and warehouse: rewrites the schedule lines of each changed line, moves confirmed dates, versions each order, emits `order.changed`, `atp.promised` per changed line and `backorderrun.committed`; an order changed since the preview, or another line of the product promised differently since, is `SLS-0042` and nothing is applied; a committed run is `SLS-0041` |
| POST | `/v1/sales/schedule-releases:check` | `sales.agreement.maintain` | Stateless BR-27.12 reconciliation; a mismatch returns `held: true`, `code: CUM_QTY_MISMATCH` and no schedule lines |
| POST | `/v1/sales/backorder-runs:preview` | `sales.backorder.run` | Stateless FR-27.17 preview over supplied lines; nothing is committed |
| GET | `/v1/sales/change-requests` | `sales.order.read` | Optional `legalEntityId`, `status`, `orderId`, `limit`; PENDING oldest first (the approval queue), otherwise newest first |
| GET | `/v1/sales/change-requests/{id}` | `sales.order.read` | The stored change, its field-level diff and the order version it was made against; the ETag is the request version |
| POST | `/v1/sales/change-requests/{id}:withdraw` | `sales.order.write` | `If-Match`; the requester takes a PENDING request back (WITHDRAWN, comment optional); anyone else is `SLS-0114`, a decided request `SLS-0044` |
| POST | `/v1/sales/change-requests/{id}:approve` | `sales.order.approve` | `If-Match`; applies the stored change to the order and records the approval in one transaction (`order.changed`, then `orderchange.approved`; for a `CANCEL` request the order is cancelled as by `:cancel`, `order.cancelled` then `orderchange.approved`, and a line no longer OPEN/RESERVED is `SLS-0025`); the requester cannot approve (`SLS-0045`), a decided request is `SLS-0044`, an order that moved since is `SLS-0046` |
| POST | `/v1/sales/change-requests/{id}:reject` | `sales.order.approve` | `If-Match`; a `comment` is required; the order stays as it was |
| GET / PUT | `/v1/sales/customers/{customerId}/change-policy` | `sales.order.read` / `sales.order.approve` | `legalEntityId` required; `approvalRequired` switch, off at version 0, with an optional `approvalThreshold` + `thresholdCurrency` (changes of a confirmed order apply at once while the value they move, summed per line and over every change applied without approval, stays below it, so splitting a change does not avoid approval; a cancellation moves the whole total; another currency always needs approval) and `approverIds` (identity user ids: only these users approve or reject, `SLS-0150`, or change and cancel the customer's confirmed orders without a request); PUT takes `If-Match` (`"0"` to create), replaces `approvalRequired` and each member it names (`null` / `[]` clears) and keeps the members it leaves out; `changepolicy.changed` |
| GET | `/v1/sales/credit-profiles` | `sales.order.read` | Optional `legalEntityId`, `limit` |
| GET / PUT | `/v1/sales/credit-profiles/{customerId}` | `sales.order.read` / `sales.credit.maintain` | `legalEntityId` query or the caller's `Passage-Legal-Entity`; PUT takes `If-Match` (`"0"` to create) |
| GET / POST | `/v1/sales/quotations` | `sales.order.read` / `sales.quotation.maintain` | List (`legalEntityId`, `customerId`, `status`, `limit`) without lines; create a DRAFT with `Idempotency-Key`, numbered `SQ-`, validity default +30 days, probability 50 |
| GET | `/v1/sales/quotations/{id}` | `sales.order.read` | With lines; converted lines name their order |
| POST | `/v1/sales/quotations/{id}:send` | `sales.quotation.maintain` | `If-Match`; DRAFT to SENT while valid |
| POST | `/v1/sales/quotations/{id}:lose` | `sales.quotation.maintain` | `If-Match`; `{reason}`; DRAFT or SENT to LOST |
| POST | `/v1/sales/quotations/{id}:convert` | `sales.quotation.maintain` | `If-Match`; optional `{lineNos, listingOverrideReason}` (empty converts every open line); 201 with the CONVERTED quotation and one DRAFT order; emits `quotation.converted` |
| GET | `/v1/sales/holds` | `sales.credit.release` or `sales.order.hold` | Release queue: ACTIVE holds oldest first (`legalEntityId`, `holdType`, `limit`) with the failed credit rule, the customer's credit limit and the order version for the release call |
| POST | `/v1/sales/quotations:expire` | `sales.quotation.maintain` | Expiry run (`legalEntityId` optional): DRAFT and SENT quotations past their validity become EXPIRED, oldest first, up to 500 per run (`more` says to call again), each with a version bump and `quotation.expired` in the same transaction; state-based, so a repeat expires nothing twice |
| GET | `/v1/sales/summary` | `sales.order.read` | Workspace tiles (ACTIVE holds, backorder schedule lines, quotations ending within 7 days, lapsed open quotations, OPEN intake errors), the open pipeline per currency (count, net, probability-weighted net) narrowed by `status`, `minProbabilityPct` and `expiringWithinDays`, and won/lost/expired with the conversion rate over `windowDays` (default 90) |
| GET / POST | `/v1/sales/supplementary-item-rules` | `sales.order.read` / `sales.listing.maintain` | List (`legalEntityId`, `parentProductId`, `limit`); create a rule (one per parent, customer or all customers, and item) |
| DELETE | `/v1/sales/supplementary-item-rules/{id}` | `sales.listing.maintain` | Remove a rule |
| POST | `/v1/sales/supplementary-items:evaluate` | `sales.order.read` | Stateless: the MANDATORY and OPTIONAL supplementary lines a parent product and quantity trigger for a customer |
| GET / POST | `/v1/sales/product-proposal-rules` | `sales.order.read` / `sales.listing.maintain` | List (`legalEntityId`, repeated `orderedProductId`, `limit`) with each rule's accepted and declined counts; create a rule (one per ordered product, customer or all customers, and proposed product) |
| DELETE | `/v1/sales/product-proposal-rules/{id}` | `sales.listing.maintain` | Remove a rule; recorded responses stay |
| GET / POST | `/v1/sales/allocation-quotas` | `sales.order.read` / `sales.allocation.maintain` | Quota inquiry per product and period (`legalEntityId`, `productId`, `customerId`, `onDate`, `limit`) with quota, consumed and remaining; set a quota for a product, period and customer or every customer (`Idempotency-Key`; overlapping periods are `SLS-0035`) |
| GET / PATCH | `/v1/sales/allocation-quotas/{id}` | `sales.order.read` / `sales.allocation.maintain` | One quota with its draw history; change the quantity under `If-Match` (below the consumed quantity is `SLS-0036`) |
| POST | `/v1/sales/product-proposals:evaluate` | `sales.order.read` | Stateless: the products to propose for the products being committed, excluding those already on the order |
| POST | `/v1/sales/product-proposals:respond` | `sales.order.write` or `sales.channel.intake` | `Idempotency-Key`; records an accept or decline of one proposal |
| GET / POST | `/v1/sales/alternative-item-rules` | `sales.order.read` / `sales.listing.maintain` | List (`legalEntityId`, repeated `orderedProductId`, `limit`) by ordered product and priority; create a rule (ordered product, alternative, optional customer, `priority` 1 to 99, optional `validFrom` / `validTo`; one per ordered product, customer or all customers, and alternative, else `SLS-0055`) |
| DELETE | `/v1/sales/alternative-item-rules/{id}` | `sales.listing.maintain` | Remove a rule (`SLS-0113` when absent) |
| GET | `/v1/sales/intake-errors`, `/v1/sales/intake-errors/{id}` | `sales.order.read` | Channel orders refused at intake (`legalEntityId`, `channel`, `status`, `errorCode`, `limit`), most recently seen first; a retry of the same payload raises `attempts` and reopens a resolved row. The payload is not kept, only its hash |
| POST | `/v1/sales/intake-errors/{id}:resolve` | `sales.order.write` | Close an OPEN intake error with a `note` (`If-Match`; `SLS-0116` when already resolved) |
| POST | `/v1/sales/alternative-items:evaluate` | `sales.order.read` | Stateless: when the ordered product cannot ship the whole quantity by the requested date, the applicable alternatives that can, each with its available quantity and promise, from ATP buckets in the request |
| GET / POST | `/v1/sales/return-orders` | `sales.order.read` / `sales.order.write` | List (`legalEntityId`, `customerId`, `status`, `limit`); create a REQUESTED RMA (`Idempotency-Key`, numbered `RMA-`) from a billing invoice: customer, currency and net unit prices come from `originalInvoiceId`, quantity is capped at invoiced less what billing credit notes credited and other live RMAs return (`SLS-0123`) |
| GET | `/v1/sales/return-orders/{id}` | `sales.order.read` | The RMA with lines; ETag = version |
| POST | `/v1/sales/return-orders/{id}:authorize` | `sales.order.write` | `If-Match`; REQUESTED → AUTHORIZED with 30-day validity; the creator may not authorize (`AUTH-0003`) |
| POST | `/v1/sales/return-orders/{id}:receive` | `sales.order.write` | `If-Match`; AUTHORIZED → RECEIVED into quarantine, lines left out receive 0 (`SLS-0120` if expired, stored as EXPIRED) |
| POST | `/v1/sales/return-orders/{id}:inspect` | `sales.order.write` | `If-Match`; body `lines[]` of `lineNo`, `quantityAccepted`, `disposition` for every received line (no lines or no body when nothing was received); RECEIVED → INSPECTED. RETURN_TO_STOCK on a line without site, warehouse or return cost is `SLS-0119`; a CURRENT_COST line's cost is read here (`SLS-0122` when the product has no on-hand average) |
| POST | `/v1/sales/return-orders/{id}:disposition` | `sales.order.write` | `If-Match`; INSPECTED → DISPOSITIONED, then the accepted RETURN_TO_STOCK quantity is restocked through inventory's service-only `POST /internal/v1/inventory/return-receipts` (duty `inventory.return-receipt.post`) under key `rma-receive:<lineId>` at the return cost method; the answer is trusted only as that receipt (POSTED MOVEMENT of the legal entity, one line with the key, product and quantity) (repeat on DISPOSITIONED to retry; `SLS-0124` 503) |
| POST | `/v1/sales/return-orders/{id}:credit` | `sales.order.write` | `If-Match`; credit from accepted quantities is stored (CREDITED), receivables posts the note, then CLOSED (repeat on CREDITED to retry; `SLS-0121` when receivables is not configured, `SLS-0124` on an outage). The restocking floor applies once per note; a return with nothing to credit closes with net 0 and no credit note |
| POST | `/v1/sales/return-orders/{id}:reject` | `sales.order.write` | `If-Match`; REQUESTED or AUTHORIZED → REJECTED before any goods are received (`SLS-0119` otherwise); the quantity is returnable again |
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
  so unapplied cash is a credit); for BALANCE_ALL it adds the computed open
  orders (CONFIRMED net totals plus the undelivered part of PARTIALLY_SHIPPED
  orders). Until the wave 12 spot rate is wired, an order
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
  Slice 14: a PATCH revises the commitment (never below the released
  quantity), price, validity and tolerance under the row lock; `:close` ends
  it early with a reason (CLOSED, no releases or schedule releases). Cancelling
  an order a release created gives the release's quantity back to the
  commitment in the cancellation transaction, once per release
  (`agreement_return`); EXHAUSTED becomes ACTIVE again, CLOSED stays CLOSED.
  A revised price applies to later releases only.
- **FR-27.9 change approval** (slice 13): a customer can be switched to
  `approvalRequired` (`customer_change_policy`, per legal entity and customer;
  off when never saved). A PATCH of a CONFIRMED order of such a customer by a
  caller without `sales.order.approve` is validated, then stored as a PENDING
  `order_change_request` (the PATCH body, its diff, the order version it was
  made against) and answered 202 with the order unchanged and `pendingChange`;
  `orderchange.requested` is written. At most one request per order is PENDING
  (`SLS-0043`). `:approve` applies it through the same path as a direct PATCH,
  as the requester's change, and records the decision in one transaction
  (`order.changed`, then `orderchange.approved`); `:reject` needs a comment. A
  caller with `sales.order.approve` changes the order directly.
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
- **FR-27.1 / BR-27.5** quotations: DRAFT → SENT → CONVERTED or LOST (`POST /quotations:expire` sets EXPIRED
  for quotations past validity; CANCELLED exists as a state nothing sets yet). A quotation is valid
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
  drops below the rule's base quantity (and reopened when the open parent
  reaches it again), and is cancelled with the order.
- **FR-27.21** product proposals: a rule proposes a product (CROSS_SELL,
  UP_SELL or ACCESSORY) when another is ordered, for one customer or every
  customer of the legal entity (`sales.listing.maintain`; a second rule for
  the same ordered product, customer and proposed product is `SLS-0034`).
  `POST /product-proposals:evaluate` is stateless: it returns the proposals for
  the products being committed, never one already on the order or being
  committed, with a customer's own rule winning over the rule for every
  customer; nothing is added to the order. `POST /product-proposals:respond`
  (`Idempotency-Key`) records the user's accept or decline, and the rule list
  returns each rule's accepted and declined counts, the acceptance rate.
- **FR-27.13** alternative items: a rule names a product to offer in place of
  an ordered product, for one customer or every customer of the legal entity,
  with a priority (1 first) and optional validity days judged on the requested
  date (`sales.listing.maintain`). `POST /alternative-items:evaluate` is
  stateless: it places the quantity against the ordered product's buckets and,
  when less than the whole quantity ships by the requested date, against the
  buckets of each applicable alternative (a customer's own rule wins over the
  rule for every customer). Offers that cover the whole quantity come first,
  then by priority, then by quantity; an alternative with only later supply is
  no offer, and one sent without buckets is listed as `unchecked`. Nothing is
  stored and no line changes.
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
| `SLS-0013` | 422 / warning | Allocation for a line is exhausted (FR-27.16, BR-27.15): 422 on a confirm or change when no period from the requested one on has enough left; a warning in `warnings[]` when the line moved to a later period, and when a CREDIT release could not confirm the order (the hold is still released) |
| `SLS-0014` | 200 (held) | Cumulative quantity mismatch; the scheduling release is held (`code` = `CUM_QTY_MISMATCH`) |
| `SLS-0015` | 200 (warning) | Backorder run preview would deconfirm lines (`warningCode`) |
| `SLS-0016` | 422 | One-time customer order over the one-time order limit (FR-27.22), on create, on a change that raises the total and on confirm; a CREDIT release that would confirm such an order releases the hold and returns it as a warning |
| `SLS-0019` | 422 | Order not confirmable in its current state |
| `SLS-0021` | 422 | Order has an active hold that blocks this action |
| `SLS-0022` | 422 | Hold is not ACTIVE |
| `SLS-0023` | 422 | Order is shipped, closed or cancelled and cannot be changed |
| `SLS-0024` | 422 | Change refused: unknown line, a line that is not OPEN or RESERVED, a supplementary line's own quantity or date or its removal, a line with a production order planned removed, a change that would leave no line, an invalid added line, invalid quantity, bad price or date, or reason too long |
| `SLS-0025` | 422 | Order cannot be cancelled: shipped, closed or cancelled, or a line is picked, delivered or invoiced |
| `SLS-0026` | 422 | Scheduling release refused: negative quantities, unordered or decreasing buckets |
| `SLS-0027` | 422 | Backorder run refused: unknown strategy, supply below zero, a WIN, GAIN or LOSE run that cannot keep a protected confirmation, or (stored runs) no open line for the product, site and warehouse or more than 10,000 |
| `SLS-0028` | 422 | Hold cannot be applied: CREDIT by hand, the order is shipped, closed or cancelled, or an ACTIVE hold of the type exists |
| `SLS-0029` | 422 | Agreement is not valid on the release date |
| `SLS-0030` | 422 | Quotation status does not allow the action (send a non-DRAFT, convert a DRAFT or LOST, lose a CONVERTED) |
| `SLS-0031` | 422 | Quotation validity has passed |
| `SLS-0032` | 422 | Selected quotation lines are not open (already converted, unknown or repeated) or none remain |
| `SLS-0033` | 409 | A supplementary item rule for the parent, customer (or all customers) and item exists |
| `SLS-0034` | 409 | A product proposal rule for the ordered product, customer (or all customers) and proposed product exists |
| `SLS-0055` | 409 | An alternative item rule for the ordered product, customer (or all customers) and alternative product exists |
| `SLS-0035` | 409 | An allocation quota for the product and customer (or all customers) already covers part of the period |
| `SLS-0036` | 409 | Allocation quota set below the consumed quantity |
| `SLS-0037` | 422 | The order is not CONFIRMED or is a JOURNAL order (BR-27.7), the line is past RESERVED, or the line is a supplementary line, so it cannot be promised |
| `SLS-0038` | 409 | A release with this number was already received for the agreement |
| `SLS-0039` | 422 | The release is not HELD, or a newer release is already in effect |
| `SLS-0040` | 422 | Cumulative shipped quantity would fall without a later reset date, or the reset date moves back |
| `SLS-0041` | 409 | The backorder run was already committed |
| `SLS-0042` | 409 | The run is out of date, so nothing was applied: an order of a changed line moved since the preview (version, status or line state), or another open line of the product, site and warehouse now holds another promised quantity than the preview saw |
| `SLS-0043` | 409 | The order already has a change waiting for approval |
| `SLS-0044` | 409 | The change request was already decided |
| `SLS-0045` | 403 | A change cannot be approved by the user who requested it |
| `SLS-0150` | 403 | The customer names its change approvers and the caller is not one |
| `SLS-0046` | 409 | The order changed since the change request was made; reject it and ask for a new change |
| `SLS-0047` | 422 | A line's `configurationId` does not exist, is not valid or does not belong to the line's product |
| `SLS-0048` | 503 | The product service could not confirm a line's configuration (or is not configured); nothing is stored, retry |
| `SLS-0049` | 422 | `:plan-production` on an order that is not CONFIRMED |
| `SLS-0050` | 502/503 | The production service could not plan a line (or is not configured); the lines planned before it are kept, retry |
| `SLS-0051` | 409 | Production already has an order for the line made for another product or configuration |
| `SLS-0052` | 422 | The agreement is closed: no release, schedule release, revision or second close |
| `SLS-0053` | 422 | Agreement commitment revised below the quantity already released |
| `SLS-0054` | 503 | One-time customer order while the service has no field encryption keys (`FIELDCRYPT_KEYS_FILE` / `FIELDCRYPT_CURRENT_KEY_ID`): the buyer is never stored or returned in clear |
| `SLS-0061` | 422 | BR-24.5: the actual catch weight of a DELIVERED report is outside min × cases to max × cases (FR-24.7) |
| `SLS-0062` | 422 | A hold cannot be rejected: it is not a CREDIT hold, or its order is no longer awaiting confirmation (DRAFT or OPEN) |
| `SLS-0063` | 422 | A catch-weight report is malformed: a report on a catch-weight line that is not a whole number of cases, or `actualWeight` on a line that is not catch-weight or on a report that is not DELIVERED |
| `SLS-0100` | 404 | Order not found |
| `SLS-0101` | 404 | Hold not found |
| `SLS-0102` | 404 | Credit profile not found |
| `SLS-0103` | 404 | Sales agreement not found |
| `SLS-0104` | 404 | Product listing not found |
| `SLS-0105` | 404 | Quotation not found |
| `SLS-0106` | 404 | Supplementary item rule not found |
| `SLS-0107` | 404 | Product proposal rule not found (also on `:respond` for a rule outside the legal entity) |
| `SLS-0108` | 404 | Allocation quota not found |
| `SLS-0109` | 404 | Order line not found |
| `SLS-0110` | 404 | Scheduling release not found |
| `SLS-0111` | 404 | Backorder run not found |
| `SLS-0112` | 404 | Change request not found |
| `SLS-0113` | 404 | Alternative item rule not found |
| `SLS-0114` | 403 | Only the user who requested a change can withdraw it |
| `SLS-0115` | 404 | Intake error not found |
| `SLS-0116` | 409 | Intake error is already resolved |
| `SLS-0119` | 409 | Return order is not in the status the command requires |
| `SLS-0120` | 409 | Return order has expired |
| `SLS-0121` | 503 | Receivables is not configured; the credit note cannot be posted |
| `SLS-0122` | 422 | Original invoice unknown, not posted, or not of this legal entity, customer or product |
| `SLS-0123` | 422 | Return quantity exceeds what the invoice still allows |
| `SLS-0124` | 503 / 422 | A peer (billing, fulfillment, inventory, receivables) is unavailable or refused; the return order keeps its state |
| `SLS-0125` | 404 | Return order not found |
| `idempotency_key_reuse` | 422 | Same `Idempotency-Key` with a different body (kernel) |
| `COM-0001` | 400 | Request failed validation; field errors in `errors` |
| `AUTH-0001` | 401 | Missing or malformed tenant |
| `AUTH-0003` | 403 | Duty missing; order outside the caller's legal entity; or the user who applied a hold releasing it (SoD) |

## Dependencies

Go module only. Allowed dependencies: the platform module and what it pulls in
(chi, pgx/v5, shopspring/decimal, google/uuid). No new dependency.
