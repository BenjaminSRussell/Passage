# fulfillment

Passage fulfillment service, wave 28 (PSG-28), slices 1 to 6. It releases confirmed
order lines to a warehouse shipment, records pick and pack quantities,
closes containers with GS1 SSCC-18 numbers (by hand or by packing rule with
auto-pack), and posts a packing slip that
journals delivered-not-invoiced (Dr 1405 / Cr 1400 at cost, or Dr 5000 / Cr 1400
when COGS posts at shipment), with a mirror reversal on cancellation. The
inventory issue and the ledger posting are made by their owning services from
the `packingslip.posted` event; this service does not write their tables (see
NOT-IMPLEMENTED.md). The slip also carries statistical packaging fee lines
(packaging weight per material × the ship-to country's rate, never journaled)
and raises the customer's returnable packaging balance for each returnable
container it ships. A slip can post part of a shipment: the shipment stays
PARTIALLY_SHIPPED and its remainder is re-picked and posted on another slip.
The picking workbench groups released shipments into pick waves; carrier
tracking events are stored and published; containers print a GS1-128 ZPL label.
The delivered-not-invoiced report gives the open 1405 balance per legal entity
as of a date, read from the slip journals. The SSCC register lists every SSCC
issued, with its shipment and no-reuse date. The short picks and backorders
report lists the lines a pick confirmed below plan with the remainder still
owed, and the returnable packaging aging gives the days each customer has held
its returnable containers (first in, first out) and whether a charge is due.

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
migrations/             0001_init.sql (RLS, outbox, idempotency, number counters),
                        0002_packing_packaging.sql (container types, packing rules,
                        product packaging, fee rates and lines, returnable balances),
                        0003_partial_waves_tracking.sql (PARTIALLY_SHIPPED, carrier and
                        zone, container slip link, waves, tracking events)
                        0004_dni.sql (partial index for the DNI report)
                        0005_short_picks.sql (first short-pick time per shipment line)
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
(generation, check digit, single-digit change detection) and the auto-pack
invariants (every unit packed once, no container over its quantity or weight),
plus the GS1 General Specifications example `106141411234567897`. The store
tests run release, pick, short pick, pack, post (both BR-28.1 debit accounts),
cancel, re-post, idempotent replay, key reuse, release de-duplication, tenant
and legal-entity isolation, auto-pack (AC-28.11), the packaging fee and report
(AC-28.12), returnable balances (AC-28.13), partial shipments over two slips
with a per-slip cancel (AC-28.4, FR-28.8), shipment cancel, the workbench
(AC-28.10), tracking, the ASN, the delivered-not-invoiced report (AC-28.8,
as-of dates around a cancel), the SSCC register (filters, cursor, scope), the
short picks and backorders report (FR-28.2: open, shipped part, re-pick, cancel,
filters, cursor, scope), returnable aging (FIFO buckets from the slips, a
cancelled slip left out) and the `warehouse-sim-1k` run (AC-28.7: 1,000 orders
released, picked with short picks, packed and posted, with zero mismatch between
released quantities, slip lines and `packingslip.posted` events; see
`testdata/warehouse-sim-1k/README.md`; skipped with `-short`) against Postgres
through `platform/go/testkit`. They run as the application role
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
release board, shipments list, shipment detail (confirm pick, auto-pack, pack
container, post packing slip with a journal preview and ship-to country),
packing slip journal and detail (cancel, packaging fees), the GS1-128 scan
page, and the container types, packing rules, packaging materials, packaging
fees (rates and report) and returnable packaging pages, and from slice 3 the
picking workbench, pick waves and wave detail, and on the shipment detail the
remainder pick, cancel, tracking with manual entry, the ZPL label and the ASN
option, from slice 4 the delivered-not-invoiced report, from slice 5 the
SSCC register, and from slice 6 the short picks and backorders report and the
returnable packaging aging. Run `npx vitest --run src/modules/fulfillment`
and `npx tsc --noEmit` in `apps/web`.

## Endpoints (slice 1)

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/fulfillment/releases:run` | fulfillment.warehouse.release | Idempotency-Key. Creates shipments per (order, warehouse, ship date); a line already on an open shipment is skipped as `already_released`, a line with an active drop-ship as `drop_ship`; a line whose remainder sales cancelled is released only up to the quantity it keeps (`order_line_kept`) and skipped as `order_remainder_cancelled` when nothing is left; runs per warehouse are serialised |
| GET | `/v1/fulfillment/shipments` | fulfillment.warehouse.view | Delivery monitor; filters `status`, `warehouseId`, `orderId`, `limit` (1 to 500, default 100) |
| GET | `/v1/fulfillment/shipments/{id}` | fulfillment.warehouse.view | ETag carries the version |
| POST | `/v1/fulfillment/shipments/{id}:confirm-pick` | fulfillment.warehouse.pick | Idempotency-Key, If-Match; above plan only within the over-delivery tolerance (FUL-0004 with none, FUL-0005 above it) and never beyond what the order line keeps after sales cancelled the remainder (FUL-0066) |
| POST | `/v1/fulfillment/shipments/{id}/containers` | fulfillment.warehouse.pack | Packs and closes a container with an SSCC; Idempotency-Key, If-Match |
| POST | `/v1/fulfillment/shipments/{id}:post-packing-slip` | fulfillment.packing-slip.post | Slip, journal, containers SHIPPED and outbox in one transaction; Idempotency-Key, If-Match |
| GET | `/v1/fulfillment/packing-slips` | fulfillment.warehouse.view | Packing slip journal; filters `shipmentId`, `orderId`, `status`, `limit` |
| GET | `/v1/fulfillment/packing-slips/{id}` | fulfillment.warehouse.view | Slip with lines and journal |
| POST | `/v1/fulfillment/packing-slips/{id}:cancel` | fulfillment.packing-slip.post | Reversing slip with mirror lines, journal and packaging fee lines; takes returnable containers back off the customer's balance; Idempotency-Key |

## Endpoints (slice 2)

| Method | Path | Duty | Notes |
|---|---|---|---|
| GET / POST | `/v1/fulfillment/container-types` | fulfillment.warehouse.view / fulfillment.packing-rule.maintain | Code, tare, returnable, packaging material; POST replaces by code |
| GET / POST | `/v1/fulfillment/packing-rules` | fulfillment.warehouse.view / fulfillment.packing-rule.maintain | Per product and optional customer; POST replaces the rule of that scope and keeps its id; unknown container type is FUL-0050 (422) |
| GET / POST | `/v1/fulfillment/product-packaging` | fulfillment.warehouse.view / fulfillment.packing-rule.maintain | kg of a material per product unit |
| POST | `/v1/fulfillment/shipments/{id}:auto-pack` | fulfillment.warehouse.pack | Idempotency-Key, If-Match; optional `unitWeights` per line; FUL-0012 warnings, FUL-0013 |
| GET / POST | `/v1/fulfillment/packaging-fee-rates` | fulfillment.packaging-fee.maintain | Rate per kg per country and material, valid from a date |
| GET | `/v1/fulfillment/packaging-report` | fulfillment.packaging-fee.maintain | `countryCode`, `period` (YYYY-MM), optional `legalEntityId`; net of reversals |
| GET | `/v1/fulfillment/returnable-packaging` | fulfillment.warehouse.view | Balances, optional `customerId` |
| POST | `/v1/fulfillment/returnable-packaging:return` | fulfillment.warehouse.receive | Standalone return receipt, whole containers; FUL-0015 below zero |

The post takes an optional `countryCode` (ship-to); with it, the slip carries
the packaging fee lines and a shipped material without a rate is FUL-0014.

## Endpoints (slice 3)

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/fulfillment/shipments/{id}:cancel` | fulfillment.warehouse.release | Idempotency-Key, If-Match; nothing shipped and no container (BR-28.6) |
| POST | `/v1/fulfillment/waves:preview` | fulfillment.warehouse.release | Read-only workbench preview; filter, `groupBy`, `maxLines` (200), `maxOrders` (50) |
| POST | `/v1/fulfillment/waves` | fulfillment.warehouse.release | Idempotency-Key; creates the waves, shipments to PICKING; FUL-0016 message; FUL-0053 when stale |
| GET | `/v1/fulfillment/waves`, `/waves/{id}` | fulfillment.warehouse.view | Waves newest first (`warehouseId`, `status`, `picker`, `unassigned`, `limit`); one wave with its shipments, lines and picker |
| POST | `/v1/fulfillment/waves/{id}:assign` | fulfillment.warehouse.release | Idempotency-Key, If-Match (wave version); body `picker`; assigns or reassigns an OPEN wave; `wave.assigned` |
| POST | `/v1/fulfillment/waves/{id}:close` | fulfillment.warehouse.release | Idempotency-Key, If-Match (wave version); closes when every line is picked or on a cancelled shipment, else FUL-0055; `wave.closed`; a CLOSED wave refuses `:confirm-pick` (FUL-0001) |
| GET | `/v1/fulfillment/shipments/{id}/tracking` | fulfillment.warehouse.view | Tracking events, newest first |
| POST | `/v1/fulfillment/shipments/{id}/tracking` | fulfillment.warehouse.ship | One event per provider event key (201, or 200 `created: false`); DELIVERED moves SHIPPED to DELIVERED |
| GET | `/v1/fulfillment/shipments/{id}/containers/{cid}/label` | fulfillment.warehouse.view | GS1-128 SSCC label as ZPL (`application/zpl`) |

Release lines take an optional `carrierCode` (one per order) and `zone`. The
post takes `asnEnabled` to publish `asn.issued`. Confirm-pick also works on a
PARTIALLY_SHIPPED shipment: the picked quantity is the line's new total and may
not be below what is packed (FUL-0031).

## Endpoints (slice 4)

| Method | Path | Duty | Notes |
|---|---|---|---|
| GET | `/v1/fulfillment/delivered-not-invoiced` | fulfillment.warehouse.view | `legalEntityId` (required), `customerId`, `asOf` (default today), `limit`; open 1405 per currency (`totals`, `lineCount`) and the slips with days outstanding |
| GET | `/v1/fulfillment/sscc-register` | fulfillment.warehouse.view | `companyPrefix`, `customerId`, `from`, `to` (issue dates, UTC), `limit`, `cursor`; issued SSCCs in SSCC order with container, shipment, slip, `issuedAt` and `noReuseUntil` (BR-28.5), `nextCursor` when more exist |

## Endpoints (slice 6)

| Method | Path | Duty | Notes |
|---|---|---|---|
| GET | `/v1/fulfillment/backorders` | fulfillment.warehouse.view | Short picks and backorders (FR-28.2): `warehouseId`, `status` (`open` default, `all`), `from`, `to` (first short-pick date, UTC), `limit`, `cursor`; each line with `qtyRemainder` (planned - picked) and `open`, newest short pick first |
| GET | `/v1/fulfillment/returnable-packaging/aging` | fulfillment.warehouse.view | Returnable aging (FR-28.16): `customerId`, `chargeDays` (30 to 365, default 90), `chargeDue=true`, `limit`; every balance above zero with `oldestOutstandingDate`, `daysOutstanding` (FIFO from the slip posting dates, as of today UTC) and `chargeDue` (older than `chargeDays`); `count` and `chargeDueCount` cover all, `items` the oldest `limit` |

A pick confirmation that leaves a line below plan stamps the line's first
short-pick time (migration 0005); the remainder is the backorder until a re-pick
reaches plan or the shipment is cancelled.

## Endpoints (slice 12)

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/fulfillment/shipments/{id}:nest-containers` | fulfillment.warehouse.pack | Idempotency-Key, If-Match; body `containerType`, `containerIds` (1 to 200); puts CLOSED free cartons of a PICKED or PACKED shipment on a new pallet with its own SSCC (FR-28.3); 201 with `container` and `shipment`; FUL-0056 when a carton is not CLOSED, is on a pallet or is a pallet; `container.nested` |
| POST | `/v1/fulfillment/shipments/{id}/containers/{cid}:unnest` | fulfillment.warehouse.pack | Idempotency-Key, If-Match; dissolves a CLOSED pallet, its cartons stay packed and the pallet SSCC is never reissued; FUL-0056 when the container is not a pallet; `container.unnested` |

A container returns `parentContainerId` (its pallet) and `childContainerIds`
(the cartons on a pallet). A carton on a pallet, or a pallet, cannot be unpacked
until the pallet is dissolved (FUL-0056). The pallet ships with its cartons and
the ASN gives each carton its `parent_sscc`.

## Endpoints (slice 13)

| Method | Path | Duty | Notes |
|---|---|---|---|
| GET | `/v1/fulfillment/warehouse-settings` | fulfillment.warehouse.view | The short pick action (FR-28.8) of every warehouse the caller sees shipments or a saved setting for, with its `legalEntityId`; REMAINDER_BACKORDER (the default) at version 0 when none is saved |
| GET | `/v1/fulfillment/warehouse-settings/{warehouseId}` | fulfillment.warehouse.view | One warehouse; ETag carries the version (0 when none is saved) |
| PUT | `/v1/fulfillment/warehouse-settings/{warehouseId}` | fulfillment.warehouse.release | Idempotency-Key, If-Match (0 when none is saved); body `shortPickAction` (KEEP_OPEN, CANCEL_REMAINDER, REMAINDER_BACKORDER) and `legalEntityId`; FUL-0057 when the warehouse belongs to another legal entity; `warehousesetting.changed` |
| GET | `/v1/fulfillment/delivery-tolerances` | fulfillment.warehouse.view | Over-delivery tolerances (BR-28.7), most specific scope first; filters `productId`, `customerId`, `limit` |
| PUT | `/v1/fulfillment/delivery-tolerances` | fulfillment.packing-rule.maintain | Idempotency-Key, If-Match (0 for a new scope); body `productId`, `customerId` (either may be null), `overdeliveryPct` (0 to 100, 4 decimals); `deliverytolerance.changed` |
| POST | `/v1/fulfillment/shipments/{id}:backorder-remainder` | fulfillment.warehouse.release | Idempotency-Key, If-Match; optional `plannedShipDate`; the rules of `:close-remainder`, and the closed quantity moves to a new RELEASED shipment (`backorderOfId`); `shipment.closed` with `backorder_shipment_id`, then `shipment.released` with `backorder_of` |

The setting is per warehouse and scoped by its legal entity. When a packing slip
posts and leaves the shipment PARTIALLY_SHIPPED, the action runs in the same
transaction: KEEP_OPEN leaves the remainder for a re-pick,
CANCEL_REMAINDER closes it, REMAINDER_BACKORDER (default, the PRD's) moves it to a backorder shipment;
the post response names it (`remainderAction`, `backorderShipmentId`).

## Endpoints (slice 14)

| Method | Path | Duty | Notes |
|---|---|---|---|
| GET | `/v1/fulfillment/legal-entity-settings` | fulfillment.warehouse.view | The posting defaults of every legal entity the caller sees shipments or a saved setting for; both flags false at version 0 when none is saved |
| GET | `/v1/fulfillment/legal-entity-settings/{legalEntityId}` | fulfillment.warehouse.view | One legal entity; ETag carries the version (0 when none is saved) |
| PUT | `/v1/fulfillment/legal-entity-settings/{legalEntityId}` | fulfillment.warehouse.release (+ `ledger.setup.write` to change `cogsAtShipment`) | Idempotency-Key, If-Match (0 when none is saved); body `cogsAtShipment` and `asnEnabled`, both required; FUL-0058 outside the caller's legal-entity scope; 403 AUTH-0003 for a `cogsAtShipment` change without `ledger.setup.write`; `legalentitysetting.changed` |

`cogs_at_shipment` and `asn_enabled` (section 5.28.13) are per legal entity.
`cogs_at_shipment` is the COGS posting policy (BR-28.1): every
`:post-packing-slip` of the legal entity takes it (false, Dr 1405, when none is
saved), and a request that names the other value is refused with FUL-0059, so a
poster cannot book one slip on another COGS timing. Changing it is an accounting
policy change and needs the ledger setup duty `ledger.setup.write` on top of
`fulfillment.warehouse.release`. `asn_enabled` is only the default of the ASN output: a
request that omits `asnEnabled` takes it, one that names it keeps its own value.

Idempotency: a key is stored per tenant with a fingerprint of the method, path
and body. A replay returns the stored response; the same key on another path or
with another body is `idempotency_key_reuse`. Concurrent requests with one key
are serialised by an advisory lock, so the second replays the first.

Responses are JSON. Errors are RFC 9457 problem details (`application/problem+json`)
with a stable `code`.

## Endpoints (slice 15)

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/fulfillment/drop-ships` | fulfillment.warehouse.release | Idempotency-Key; links an order line to its purchase order line at the PO cost (`unitCost` positive, `currency`); one active drop-ship per order line (FUL-0061); a line on a warehouse shipment that is not cancelled is refused (FUL-0062), and a qty above what the line keeps after sales cancelled the remainder (FUL-0066); `dropship.created` |
| GET | `/v1/fulfillment/drop-ships` | fulfillment.warehouse.view | Newest first; filters `status` (OPEN, CONFIRMED, CANCELLED), `orderId`, `vendorId`, `limit` |
| GET | `/v1/fulfillment/drop-ships/{id}` | fulfillment.warehouse.view | One drop-ship with its journal; ETag carries the version |
| POST | `/v1/fulfillment/drop-ships/{id}:confirm` | procurement.purchase-order.confirm | Idempotency-Key, If-Match; body `shippedOn`, optional `vendorShipmentRef`, `postingDate`; books Dr 5000 / Cr 2110 at the PO cost with no inventory transaction; a confirmed total above what the line keeps after sales cancelled the remainder is refused (FUL-0066); `dropship.confirmed` carries the journal; final |
| POST | `/v1/fulfillment/drop-ships/{id}:cancel` | fulfillment.warehouse.release | Idempotency-Key, If-Match; only an OPEN drop-ship (a CONFIRMED one is refused with FUL-0001); `dropship.cancelled` |

## Endpoints (carrier-gateway, #482)

These call `services/carrier-gateway-ts` with a service token for the shipment's legal entity
(`CARRIER_GATEWAY_URL` and the service signing key; unset answers 503 FUL-0070). The body carries
`shipFrom`, `shipTo` and `parcels` (`weightKg`, `lengthCm`, `widthCm`, `heightCm` as decimal strings),
because a shipment stores no addresses or dimensions.

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/fulfillment/shipments/{id}:carrier-rates` | fulfillment.warehouse.ship | Optional `carriers`, `services`; rates cheapest first and per-carrier `failures` |
| POST | `/v1/fulfillment/shipments/{id}:carrier-label` | fulfillment.warehouse.ship | Idempotency-Key (1 to 64 visible ASCII); `service`, optional `carrier` (default: the shipment's) and `reference` (default: the shipment number). A retry returns the same label (200). The label is recorded as a LABELLED tracking event |
| POST | `/v1/fulfillment/shipments/{id}:carrier-track` | fulfillment.warehouse.ship | Optional `carrier`, `trackingNumber` (default: the newest tracked one); records each new carrier event once (`recorded`) |

Carrier errors: FUL-0071 means carrier-gateway refused (its code is in the detail; 404 or 422, and a 400 passes its
field errors through). FUL-0072 means it refused the call (502) or is unavailable (503, retry). FUL-0073 means the
shipment is cancelled, or there is no tracking number yet (409).

The `short_pick_action` default is REMAINDER_BACKORDER (migration 0016); KEEP_OPEN and
CANCEL_REMAINDER stay allowed.

## Error codes

Codes raised in this slice:

| Code | Meaning |
|---|---|
| `FUL-0001` | Shipment status does not allow the command (for example, not PACKED) |
| `FUL-0004` | Over-pick: picked exceeds planned and no over-delivery tolerance applies |
| `FUL-0005` | Over-ship: picked exceeds planned plus the customer's over-delivery tolerance (422) |
| `FUL-0007` | The container's stored SSCC failed its check digit; no label is printed (422) |
| `FUL-0010` | Cannot cancel: the packing slip is invoiced (409) |
| `FUL-0012` | Warning in the auto-pack response: no packing rule for the product; the line stays unpacked |
| `FUL-0013` | Auto-pack: one unit is heavier than the rule's max weight (422) |
| `FUL-0014` | Packaging fee rate missing (or in another currency) for a shipped material in the ship-to country (422) |
| `FUL-0015` | Returnable packaging balance would become negative: a return above the balance, or a cancel after the customer returned the containers (422) |
| `FUL-0016` | Info in the wave create response: "Workbench created {n} waves from {lines} lines" |
| `FUL-0017` | GS1-128 scan could not be parsed (web parser, `gs1.ts`) |
| `FUL-0020` | Shipment not found |
| `FUL-0021` | Invalid move between shipment states |
| `FUL-0023` | Over-pack: packed exceeds picked |
| `FUL-0030` | Negative quantity |
| `FUL-0031` | Re-pick below the quantity already packed or shipped (422) |
| `FUL-0032` | Wave limits must be positive (400) |
| `FUL-0033` | Unknown workbench group key (400) |
| `FUL-0034` | One shipment has more lines than a wave may hold (422) |
| `FUL-0040` | Packing slip not found |
| `FUL-0041` | Shipment line not found on this shipment |
| `FUL-0042` | Cost snapshot missing for a stocked line |
| `FUL-0043` | Packing slip cannot be cancelled: already cancelled, or itself a reversal (409) |
| `FUL-0044` | Nothing to ship: no packed quantity is waiting on the shipment |
| `FUL-0050` | Request failed validation; `errors` names the field (also a repeated line, lines of one order with different customers or legal entities, or more than 6 decimals) |
| `FUL-0051` | `Idempotency-Key` header missing or longer than 255 characters |
| `FUL-0052` | Wave not found (404) |
| `FUL-0053` | A shipment left RELEASED, or a line is already in a wave, since the preview (409) |
| `FUL-0055` | A wave cannot close: a line of a live shipment is still to pick (422) |
| `FUL-0054` | Container not found on the shipment (404) |
| `FUL-0056` | Container nesting refused: a carton is not CLOSED, already on a pallet or a pallet itself; the container is not a pallet; or a nested container is unpacked (422) |
| `FUL-0057` | Warehouse setting saved under a legal entity the warehouse does not belong to (its saved setting or its shipments name another one, or it is outside the caller's scope) (422) |
| `FUL-0058` | Legal entity setting saved for a legal entity outside the caller's legal-entity scope (422) |
| `FUL-0059` | Packing slip post names a `cogsAtShipment` that contradicts the legal entity's `cogs_at_shipment` policy (422) |
| `FUL-0060` | Drop-ship not found (404) |
| `FUL-0061` | The order line already has an active drop-ship (409) |
| `FUL-0062` | The order line is on a warehouse shipment that is not cancelled, so it cannot be drop-shipped (409) |
| `COM-0001` | Malformed JSON body (kernel code) |
| `precondition_required` | `If-Match` (shipment version) missing (428) |
| `conflict_version` | `If-Match` does not match the current version (412) |
| `idempotency_key_reuse` | Same Idempotency-Key with a different body (422) |

Catalog codes `FUL-0002`, `FUL-0003`, `FUL-0006`, `FUL-0008`,
`FUL-0009` and `FUL-0011` belong to later slices (period control, negative
stock, tracking dimensions, carrier, saga, consolidation).

## Dependencies

Allowed by convention: platform kernel and what it pulls in (chi, pgx/v5,
shopspring/decimal, google/uuid), plus `pgregory.net/rapid` for tests. No new
libraries were added.

## Wave reference

PSG-28 / wave 28. Depends on PSG-27 (orders and holds), wave 16 (inventory
reservations), wave 20 (cost snapshots), wave 14 (period control).
