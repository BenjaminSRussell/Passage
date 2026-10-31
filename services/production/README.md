# production

Production orders for Passage (wave 21, ticket PSG-21, PRD section 4.21). Slice 1 covers the
order itself; slice 2 adds journals, WIP postings and settlement; slice 3 adds rework orders and
as-built genealogy; slice 4 adds the bulk status update, and split and merge (slice 11: split of a Started order with the WIP transfer); slice 5 adds
order-specific BOM and route edits and the WIP balance and variance reports; slice 6 adds trigger
points on operation feedback; slice 7 adds subcontract purchase requests; slice 9 adds the item's
order quantity rules, estimate on create and the operation finish and reopen commands; slice 10 adds
order change management (OCM profiles, ECO detection, change preview and cost, apply and dismiss); slice 16 adds the per-site production parameters.

Slice 1:

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
- Report as finished (FR-21.7 quantities): good and error quantities within quantity × (1 +
  `PRODUCTION_RAF_TOLERANCE_PCT`); further reports on a ReportedAsFinished order add to them.
  Reset status to any earlier status (FR-21.9); a reset to Created drops the estimate. End checks
  the zero-output duty (BR-21.1).
- Header edits (PATCH): quantity while Created or Estimated (re-explodes, re-plans and
  re-estimates in one transaction), delivery date until Released.
- The order list (keyset paging, filters), order read, planned-cost read and status-log read.
- The settlement decomposition of BR-21.6 (`domain.Settle`: lot-size, production quantity,
  production price, production substitution), property-tested, with the BR-21.7 worked example
  and the 50-order `prod-orders-50` fixture.
- Events `order.created`, `order.updated` and `order.statuschanged`, written to the outbox in the
  same transaction as the change.

Slice 2:

- Posting profiles (FR-21.13): ledger accounts per legal entity × item group × site, with `All`
  as the wildcard; resolution item group × site, item group × All, All × site, All × All. Read
  and maintained (`PUT`, If-Match to replace) through `/v1/production/posting-profiles`. The
  order carries an optional `itemGroup`.
- Production journals (FR-21.5 to FR-21.7): `picking-list`, `route-card` and
  `report-as-finished`, created as drafts with proposed lines (`remaining`, `proportional`,
  `none`) or explicit lines, then posted with `:post` (Idempotency-Key) or deleted. Posting runs
  under the order row lock (BR-21.4), checks the posting date against the order's creation date
  (BR-21.3), the over-consumption tolerance (`PRODUCTION_OVERCONSUMPTION_PCT`, duty
  `production.journal.over-consume`) and the RAF tolerance, and writes a balanced set of WIP
  subledger rows: issues Dr WIP / Cr inventory at the line's unit cost, route-card hours Dr WIP /
  Cr labor and overhead absorption at the operation's rates, RAF Dr finished goods / Cr WIP at the
  planned unit cost (cumulative rounding, so receipts never drift). Amounts round to the
  currency's minor unit with `platform/go/money`.
- Co-products and by-products (slice 12, FR-21.14): `coProducts` on order create declares items the
  order yields besides its product (planned quantity = quantity per unit × order quantity). A report
  as finished may carry `coProducts` receipt lines (the `:report-finished` command reports them in
  proportion); planned cost is spread over product and co-products by quantity only, and End measures
  earned output over the same base so WIP still clears to 0. A line typed `by-product` carries no cost
  (BR-9.12) and is received at zero. Split and merge refuse such orders.
- Flushing principles on BOM lines: `start` lines are issued for their remaining quantity by a
  picking list that `:start` posts; `finish` lines are consumed in proportion to each report as
  finished (AC-21.4: the full quantity consumes exactly qty per × quantity × (1 + scrap%));
  `manual` and `available-at-location` lines are picked. A route card on a Released order starts
  it (`PRODUCTION_AUTOSTART_ON_FEEDBACK`); `finished` closes an operation to further feedback.
- `:report-finished` posts a RAF journal; a RAF journal posted through `:post` moves a Started
  order to ReportedAsFinished the same way.
- End and settlement (FR-21.8, BR-21.6): `:end` refuses unposted journals and an end date before
  the last posting, computes actual cost from the posted journals, decomposes it against the
  estimate into lot-size, quantity, price and substitution variances that clear the WIP balance
  exactly, posts the clearing to the variance accounts and stores the variances. Ending with zero
  output sends all cost to variance. The BR-21.7 worked example ends with four variances of
  −49.00 and WIP 0.00 (`TestJournals_WorkedSettlementExample`).
- Reset reversals (FR-21.9): leaving ReportedAsFinished reverses the RAF journals; going back
  before Started reverses every journal. Each reversal is a posted journal with negated lines
  and swapped postings, linked to its original.
- Reads: the journal list and journal (lines and postings), the cost view (planned and actual by
  group, WIP balance, variances) and the actual cost on the order list (FR-21.11).
- Events `journal.posted` and `order.settled` carry the ledger lines for the general ledger
  (wave 11) to book.

Slice 3:

- Rework orders (FR-21.20): `originType` `Rework` with an optional `reworkReference` to the
  original order (by id), batch or serial (by number), and the duty `production.order.rework`.
  An order reference must name an order of the same product that reported at least the rework
  quantity as finished (`PRODUCTION_REWORK_REFERENCE_INVALID`). A rework order has no BOM
  explosion by default: the request lists the components, and the product itself as a line to
  issue the reworked units into WIP at their existing cost. Its reports as finished receive the
  units back at that cost (the planned cost of the order's line for its own product, zero without
  one), so the rework (replacement parts, labor, overhead) stays in WIP; End posts the WIP
  balance as variances of type `rework`, one per cost group, to the profile's
  `reworkVarianceAccount` (AC-21.15: 2.00 h at 30.00 settles as a 60.00 rework variance). The
  order list filters by `originType` and `reworkOf`, so an original order lists its rework orders.
- As-built genealogy (FR-21.19, BR-21.14): `POST …/orders/{id}/as-built` assigns a component
  serial (quantity 1) or batch to a finished serial or batch while the order is Started or
  ReportedAsFinished, with the component on the order's BOM, the assigned quantity of the
  component within what the order consumed of it, the distinct finished serials within the order
  quantity, and a component serial attached to one finished serial only across the tenant
  (`SERIAL_DUPLICATE`, enforced by a partial unique index too). `:detach` corrects a wrong
  assignment and keeps the record for the audit trail; a reset that reverses the reports as
  finished detaches the order's records. `GET /v1/production/as-built` answers as-built of a
  finished serial and where-used of a component serial (AC-21.14). Events `asbuilt.assigned` and
  `asbuilt.detached`.

Slice 4:

- Bulk status update (FR-21.12): `POST /v1/production/orders:bulk-status` applies `estimate`,
  `schedule`, `release`, `start`, `end` or `cancel` to 1 to 500 distinct orders. Each order runs
  in its own transaction at its current version with the same rules, postings and events as the
  single command (continue on error), and the answer lists a result per order with its code;
  `PRODUCTION_BULK_PARTIAL` (PROD-0022) marks a batch with failures. Order X runs under the key
  `<Idempotency-Key>:X`, so a retried batch replays what it moved and retries the rest. The route
  needs the release duty and the command its own duty too. It runs in the request, not as the
  Temporal job of 4.21.12 (wave 4).
- Split (FR-21.15): `POST /v1/production/orders/{id}:split` with `splitQty` (and the item's
  `minimumQty` and `multiple` until wave 8 supplies them, PROD-0015) on a Created to Released
  order. The new order takes the header, BOM and route snapshot and status, origin `Split` with
  `originId` and `splitFromOrderId` set to the original (a rework order's split stays a rework
  order of the same reference), and the next number. Both orders are re-exploded for their
  quantity (BR-21.2 and BR-21.8, each with its own setup) and re-estimated once estimated; the
  two quantities add up to the original's (PRD 4.21.4 invariant). Events `order.created` for the
  new order and `order.updated` for the original; a `split` status-log row on both.
- Merge (FR-21.15): `POST /v1/production/orders:merge` with the orders and the versions read.
  The orders must be Created to Released, of one legal entity, product, site, warehouse,
  currency, item group and rework reference, with the same BOM and route snapshot. Refused when
  any of them has posted journals (PROD-0016) or an order to be cancelled has drafts. The first
  order survives with the summed quantity, re-exploded and re-estimated (`order.updated`); the
  others become Cancelled with `mergedIntoOrderId` (`order.statuschanged`, a `merge` log row).
  Rows are locked in id order. Migration `0004_split_merge.sql` adds the two links (self foreign
  keys, partial indexes) and the check that a merged order is Cancelled.

Slice 5:

- Order-specific BOM and route edits (FR-21.18): `PATCH /v1/production/orders/{id}/bom` under
  If-Match with `lines` and `operations` edits (`add`, `remove`, `change`; a change sends only the
  fields it sets), applied in order after explosion and before Started. Required quantities are
  re-exploded (BR-21.2), process quantities re-planned (BR-21.8), the planned cost re-estimated
  once the order is estimated, and the order flagged `orderBomModified`. A line or operation with
  consumption, route feedback or any journal (posted, reversed or draft) is never removed or moved
  to another item or resource, a requirement never drops below what was consumed, and the order
  keeps one operation. A Released order also needs `production.order.bom-after-release` (Maintain
  order BOM after release, 403 `AUTH-0003`); Started and later are 409
  `PRODUCTION_ORDER_EDIT_NOT_ALLOWED`. Each edit is audited with the line or operation before and
  after it (`GET …/{id}/bom-edits`), and `order.bomedited` carries the edits, the requirement lines
  and the planned cost. A split copies the flag; a merge survivor keeps it when any merged order
  had it. Migration `0005_order_edits.sql` adds the flag and the `production_order_edit` audit table.
- WIP balance by order (4.21.14): `GET /v1/production/wip-balances` with legal entity, site,
  status and as-of date: per order with WIP rows up to the date, the posted consumption, the value
  received by reports as finished, what End settled and the balance (Σ debit − Σ credit of the WIP
  rows, so it reconciles to the subledger), keyset-paged, with totals per currency.
- Variance report (4.21.14): `GET /v1/production/variances` with legal entity, site, item group,
  order, variance type and posting period: each variance with the planned cost of its cost group
  and its percentage of it, totals per currency and type, and the net.

Slice 6:

- Trigger points (FR-21.17, BR-21.13): `POST …/orders/{id}/trigger-points` defines a rule on an
  operation of an open order (operation, sequence unique per operation, condition, function,
  `repeatable`, and the operations it inserts or routes), with `production.setup.write` (and
  `production.order.rework` for a trigger that creates rework orders); `GET` lists them with
  their firings; `DELETE /v1/production/trigger-points/{id}` removes one that never fired. The
  condition is `<field> <op> <number>` over the route-card line of the operation (`qty_error`,
  `qty_good`, `hours`; `>`, `>=`, `<`, `<=`, `==`, `!=`).
- Posting a route card evaluates, per operation it reports on, the due trigger points in
  ascending sequence, in the post's transaction. `insert-operations` inserts its operations after
  the operation, numbered operation + 5, + 6, … or, when that would reach the next operation,
  right after the operation (operations on the route are never renumbered, since journals and
  costs refer to them); each processes the error quantity, or else the operation's process
  quantity (AC-21.13: 3 error units on 20 insert 25 and 26 before 30). `create-rework-order`
  creates a Created rework order (FR-21.20) of the product for the error quantity, with the
  trigger's operations as its route, referencing the order. `notify` only publishes the event.
  A trigger fires once per operation unless `repeatable`. Each function runs in a savepoint: a
  failure (no room for the operations, no error quantity to rework) changes nothing, is recorded
  with its reason (PROD-0019, `PRODUCTION_TRIGGER_FUNCTION_FAILED`) and never blocks the post;
  the trigger stays due. The post returns the firings in `triggerFirings` (also on replays and
  journal reads), and each emits `triggerpoint.fired`. Operations a trigger inserts are not on
  the journal, so nothing is evaluated on them in the same post (BR-21.13).
- Migration `0006_trigger_points.sql` adds `production_trigger_point` (its operation reference
  cascades when an order edit removes the operation) and the firing log
  `production_trigger_firing`, with RLS and grants.

Slice 7:

- Subcontract purchase requests (FR-21.10): releasing an order (one at a time or by bulk status)
  emits `subcontract.porequested` in the release transaction, after `order.statuschanged`, for
  each subcontract operation: the vendor resource, the process quantity, the vendor unit price,
  the planned amount (unit price × quantity at 4 places, equal to the estimate's subcontract row),
  currency, site, warehouse and the order's delivery date as the due date. The operation's
  `poRequestedAt` is stamped under the order row lock, so each operation is requested once: a
  re-estimate, a reset and second release or a replay emit nothing more. A subcontract operation
  that an edit adds to a Released order, and the operations of the new order of a Released split,
  are requested in that transaction. Purchasing (wave 17) creates the PO line from the event.
- Subcontract operations inquiry (4.21.14): `GET /v1/production/subcontract-operations` with
  legal entity, site, vendor resource, operation status, order status and due-date range: each
  subcontract operation with its quantity, good quantity, unit price, amount and request time,
  keyset-paged by order and operation, with totals per currency (`production.order.read`).
- Migration `0007_subcontract.sql` adds `production_operation.po_requested_at` and a partial index
  on the subcontract operations.

Slice 9:

- Order quantity rules (FR-21.1): create takes the item's `minimumQty`, `maximumQty` and
  `multiple` (absent or 0 is no rule) until released-product settings (wave 8) are read, and
  refuses a quantity outside them with 422 `PRODUCTION_ORDER_QUANTITY`; a PATCH of the quantity
  takes and checks them the same way.
- Estimate on create (4.21.13): with `PRODUCTION_ESTIMATE_ON_CREATE=true` a new order is estimated
  in its create transaction: it is returned Estimated at version 2 with its planned cost, an
  `estimate` status-log row and `order.statuschanged` after `order.created`. A replay emits nothing.
- Operation commands (FR-21.6): `POST …/orders/{id}/operations/{operNumber}:finish` closes an
  operation to further route cards without posting one; `:reopen` opens a finished operation
  again, as `started` when it has feedback and `open` otherwise. Both apply while the order is
  Started or ReportedAsFinished and not to a subcontracted operation (409
  `PRODUCTION_OPERATION_STATE`), need `production.journal.post`, If-Match on the order version and
  an Idempotency-Key. A change bumps the order version, writes a `production_operation_log` row and
  emits `operation.statuschanged`; a command already in effect changes nothing. Migration
  `0008_operation_log.sql` adds the log with RLS and grants.

Slice 10:

- OCM profiles (FR-21.16, 4.21.13): `PUT /v1/production/ocm-profiles` (If-Match to replace) and `GET`
  per legal entity × item group × site (either may be `All`), resolved item group × site, item group ×
  All, All × site, All × All; with none stored the default is manual and no surcharge. Mode
  `automatic`, `manual` or `none`; surcharge = a fixed amount plus a percentage of a positive delta
  (BR-21.12). Needs `production.setup.write` / `.read`.
- Detection: `POST /v1/production/order-changes:detect` takes an ECO notice (`ecoId`, `productId`,
  optional `siteId`, and the new `bom` and/or `operations`, as on create) and records every open order
  (Created to Released) of the product that differs from it and whose profile is not `none`, once per
  (order, ECO), with the delta counts and the change cost; a hand-edited order is flagged
  `needsReview` (an edit after detection flags its pending changes too). `orderchange.detected` per
  record. Older pending changes of the order that the ECO covers are dismissed as superseded; one it
  covers only in part is blocked (409 `PRODUCTION_ORDER_CHANGE_SUPERSEDED`). Needs
  `production.order.write`.
- Automatic profile: every status command (except `:cancel`) on an order before Started first
  applies its pending automatic changes, oldest first, unless the order was edited by hand or the
  change is blocked; the record notes `applied automatically at :<command>`.
- Worklist and record: `GET /v1/production/order-changes` (status, site, order, ECO, needsReview,
  keyset-paged) and `/{id}` (ETag is the record version).
- `POST …/order-changes/{id}:preview` shows the delta, the planned cost before and after, the
  surcharge and change cost, and why applying is blocked now; `:apply` (If-Match on the record,
  Idempotency-Key, `production.order.apply-change`) re-explodes the order through the order-edit
  rules, re-estimates, stores the before and after snapshot hashes and emits `order.bomedited` and
  `orderchange.applied`; lines with consumption or journals are never removed or re-pointed (409
  `PRODUCTION_ORDER_EDIT_TARGET_IN_USE`); `:dismiss` sets it aside with a note.
- Migration `0009_order_change.sql` adds `production_ocm_profile` and `production_order_change`
  with RLS, a legal-entity policy and grants (select, insert, update).

Inventory transactions (wave 16), GL booking (wave 11) and period control (wave 14) are
handoffs. Everything not built is listed in `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/production/        entrypoint: config, dbx.Connect, optional dbx.Migrate, httpx router, admin /metrics
internal/domain/       pure rules: status.go (BR-21.1 state machine, reset, RAF and edit guards),
                       quantity.go (BR-21.2, BR-21.8), explode.go (FR-21.1), cost.go (FR-21.3, BR-21.6),
                       journal.go (proposals, tolerances, posting profiles, postings, settlement),
                       rework.go (FR-21.20), asbuilt.go (FR-21.19, BR-21.14),
                       bulk.go (FR-21.12), split.go (FR-21.15 split and merge),
                       order_edit.go (FR-21.18 BOM and route edits), inquiry.go (variance % of planned),
                       trigger.go (FR-21.17 conditions, due triggers, inserted numbering, rework route),
                       subcontract.go (FR-21.10 purchase requests),
                       operation.go (FR-21.1 order quantity rules, FR-21.6 finish and reopen),
                       ocm.go (FR-21.16 profiles, snapshot diff, BR-21.12 change cost)
internal/domain/testdata/  prod-orders-50.json and the exact-fraction generator that computed it
internal/store/        Postgres repositories (pgx), tenant-scoped transactions, row locks, outbox writes:
                       store.go (orders, commands), journal.go (journals, postings, settlement, reversals),
                       profile.go (posting profiles), asbuilt.go (as-built genealogy),
                       split.go (split, merge, bulk status), order_edit.go (BOM and route edits, audit),
                       inquiry.go (WIP balance and variance reports), trigger.go (trigger points, firing on route cards), ocm.go (OCM profiles, detection, preview, apply, dismiss),
                       subcontract.go (purchase requests at release, subcontract operations inquiry),
                       operation.go (finish and reopen)
internal/api/          HTTP handlers, request validation, platform authz duties
internal/events/       event types and payloads
migrations/            0001_init.sql (orders), 0002_journals.sql (profiles, journals, WIP postings,
                       variances), 0003_rework_asbuilt.sql (rework reference, rework variance account,
                       as-built records), 0004_split_merge.sql (split and merge links),
                       0005_order_edits.sql (orderBomModified, edit audit, report indexes),
                       0006_trigger_points.sql (trigger points and firings), with RLS and app-role grants
                       on every table, 0007_subcontract.sql (purchase request stamp),
                       0008_operation_log.sql (operation finish and reopen log); migrations.go (embed)
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
| `PRODUCTION_OVERCONSUMPTION_PCT` | `10` | picking beyond the required quantity allowed without the over-consumption duty, 0 to 1000 |
| `PRODUCTION_AUTOSTART_ON_FEEDBACK` | `true` | a route card on a Released order starts it |
| `PRODUCTION_ESTIMATE_ON_CREATE` | `false` | a new order is estimated in its create transaction (4.21.13 "Estimate on create") |

The gateway supplies the principal through the `X-Passage-Tenant`, `X-Passage-Legal-Entity`,
`X-Passage-User` and `X-Passage-Duties` headers until wave 2 replaces them with JWT validation.
The routes are `/v1/production/...`; the gateway serves them under `/api`.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...                     # store and HTTP tests on a database
PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app:...@.../... (also)    # the RLS test under the app role
python3 -I internal/domain/testdata/gen_prod_orders_50.py                  # regenerate the AC2 domain fixture
python3 -I internal/store/testdata/gen_costing_50.py                       # regenerate the AC2 database fixture
```

Database tests skip when `PASSAGE_TEST_DATABASE_URL` is unset; the RLS test also needs a
non-superuser `passage_app` role (`PASSAGE_TEST_APP_DATABASE_URL`). The domain tests are pure and
include `pgregory.net/rapid` property tests for the state machine, report-as-finished limits,
phantom explosion, backflush rounding, the settlement identity, proportional consumption,
drift-free RAF receipts and the variances clearing the posted WIP in every currency precision.

## Duties

| Duty (PRD 4.21.17) | Key | Used by |
|---|---|---|
| View production orders | `production.order.read` | list, read, status log, BOM edit history, journal list and read, the operations overview and journal inquiry across orders, as-built and where-used, trigger points |
| View production costs | `production.cost.read` | `GET …/{id}/costs`, `GET /v1/production/wip-balances`, `GET /v1/production/variances` |
| Maintain production orders | `production.order.write` | create, PATCH, `:estimate`, `:schedule`, `PATCH …/{id}/bom` |
| Maintain order BOM after release | `production.order.bom-after-release` | `PATCH …/{id}/bom` on a Released order (with `production.order.write`) |
| Create rework orders | `production.order.rework` | create with `originType` `Rework` (with `production.order.write`); defining a `create-rework-order` trigger point (with `production.setup.write`) |
| Release production orders | `production.order.release` | `:release`, `:start`, `:cancel`, `:reset-status`, `:split`, `orders:merge`, `orders:bulk-status` (with the command's duty) |
| Post production journals | `production.journal.post` | `:report-finished`, create, `:post` and delete journals, assign and `:detach` as-built records, operation `:finish` and `:reopen` |
| Post production over-consumption | `production.journal.over-consume` | posting beyond the requirement plus `PRODUCTION_OVERCONSUMPTION_PCT` |
| View production setup | `production.setup.read` | `GET /v1/production/posting-profiles` |
| Maintain production setup | `production.setup.write` | `PUT /v1/production/posting-profiles`, define and delete trigger points |
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
| `PRODUCTION_UNKNOWN_ACTION` | 404 | The `:command` is not one of the eight or `:split`, a journal action is not `:post`, an as-built action is not `:detach`, or an operation command is not `{operNumber}:finish` or `:reopen` |
| `PRODUCTION_JOURNAL_NOT_FOUND` | 404 | No journal with that id in the caller's scope |
| `PRODUCTION_JOURNAL_NOT_ALLOWED` | 409 | The journal type cannot post on the order's status |
| `PRODUCTION_JOURNAL_ALREADY_POSTED` | 409 | Posting or deleting a posted journal |
| `PRODUCTION_OPERATION_FINISHED` | 409 | Feedback on a finished operation |
| `PRODUCTION_OPERATION_STATE` | 409 | `:finish` or `:reopen` on an order that is not Started or ReportedAsFinished, or on a subcontracted operation |
| `PRODUCTION_OPERATION_NOT_FOUND` | 404 | The operation number is not on the order's route |
| `PRODUCTION_ORDER_QUANTITY` | 422 | The order quantity is below the item's minimum, above its maximum or not a multiple of its multiple (FR-21.1) |
| `PRODUCTION_UNPOSTED_JOURNALS` | 409 | End with draft journals (PROD-0012) |
| `PRODUCTION_JOURNAL_EMPTY` | 422 | The proposal found nothing to post |
| `PRODUCTION_INVALID_JOURNAL_LINE` | 422 | A line names an unknown BOM line or operation, or a subcontracted operation |
| `PRODUCTION_OVER_CONSUMPTION` | 422 | Consumption beyond the requirement plus tolerance without the duty (PROD-0007) |
| `PRODUCTION_POSTING_DATE_BEFORE_ORDER` | 422 | Posting date before the order was created (BR-21.3) |
| `PRODUCTION_POSTING_PROFILE_MISSING` | 422 | No posting profile resolves for the order's item group and site, or ending a rework order whose profile has no rework variance account |
| `PRODUCTION_END_BEFORE_LAST_JOURNAL` | 422 | The end date is before the last journal posting date (FR-21.8) |
| `PRODUCTION_POSTING_NOT_BALANCED` | 422 | The settlement variances do not clear the WIP balance (an internal invariant) |
| `PRODUCTION_INVALID_HOURS` | 422 | Hours below 0.01 or with more than 2 decimals |
| `PRODUCTION_REWORK_REFERENCE_INVALID` | 422 | The rework order's order reference is unknown, another product, or more than it reported as finished |
| `PRODUCTION_AS_BUILT_ORDER_STATE` | 409 | As-built change on an order that is not Started or ReportedAsFinished |
| `PRODUCTION_AS_BUILT_COMPONENT_NOT_ON_BOM` | 422 | The component is not on the order's BOM |
| `PRODUCTION_AS_BUILT_EXCEEDS_CONSUMED` | 422 | More of the component assigned than the order consumed (BR-21.14) |
| `PRODUCTION_AS_BUILT_TOO_MANY_FINISHED` | 422 | More finished serials or batches than the order quantity |
| `PRODUCTION_AS_BUILT_NOT_FOUND` | 404 | No as-built record with that id in the caller's scope |
| `SERIAL_DUPLICATE` | 422 | The component serial is already attached to a finished serial (PROD-0009, BR-21.14) |
| `PRODUCTION_SPLIT_QUANTITY` | 422 | The split quantity is not above zero and below the order quantity, is below the minimum order quantity or is not a multiple (PROD-0015) |
| `PRODUCTION_MERGE_REFUSED` | 422 | An order has posted journals (PROD-0016), an order to be cancelled has drafts, an order is not Created to Released, or the orders differ in product, site, warehouse, currency, item group, rework reference or BOM and route |
| `PRODUCTION_ORDER_EDIT_NOT_ALLOWED` | 409 | BOM or route edit on a Started or later order (FR-21.18) |
| `PRODUCTION_ORDER_EDIT_TARGET_EXISTS` | 409 | The line or operation to add is already on the order |
| `PRODUCTION_ORDER_EDIT_TARGET_IN_USE` | 409 | The line or operation has consumption, feedback or journals |
| `PRODUCTION_ORDER_EDIT_TARGET_NOT_FOUND` | 422 | The line or operation to change or remove is not on the order |
| `PRODUCTION_ORDER_CHANGE_NOT_FOUND` | 404 | No order change with that id in the caller's scope |
| `PRODUCTION_ORDER_CHANGE_RESOLVED` | 409 | Applying or dismissing a change already applied (or dismissing one applied) |
| `PRODUCTION_ORDER_CHANGE_NOT_ELIGIBLE` | 409 | Applying a change to an order that is Started or later (FR-21.16) |
| `PRODUCTION_ORDER_CHANGE_SUPERSEDED` | 409 | A later ECO on the order replaces the same BOM or route; applying would undo it |
| `PRODUCTION_ORDER_CHANGE_NO_DELTA` | 422 | The order already matches the change's BOM and route; dismiss it |
| `PRODUCTION_TRIGGER_CONDITION_INVALID` | 422 | The trigger condition is not `<field> <op> <number>` |
| `PRODUCTION_TRIGGER_FUNCTION_UNSUPPORTED` | 422 | `release-next` or `start-workflow` (waves 23 and 4) |
| `PRODUCTION_TRIGGER_OPERATION_NOT_FOUND` | 422 | The trigger's operation is not on the order's route |
| `PRODUCTION_TRIGGER_POINT_EXISTS` | 409 | The operation already has a trigger point with that sequence |
| `PRODUCTION_TRIGGER_ORDER_CLOSED` | 409 | Trigger point on an Ended or Cancelled order |
| `PRODUCTION_TRIGGER_POINT_NOT_FOUND` | 404 | No trigger point with that id in the caller's scope |
| `PRODUCTION_TRIGGER_POINT_FIRED` | 409 | Deleting a trigger point that fired |
| `PRODUCTION_TRIGGER_FUNCTION_FAILED` | firing | Not a response: the reason a due trigger did nothing, on the firing and its event (PROD-0019) |
| `PRODUCTION_BULK_PARTIAL` | 200 body | Some orders of a bulk status update failed; see `results` (PROD-0022) |
| `idempotency_key_reuse` | 422 | The Idempotency-Key was used for a different body, command or order |
| `conflict_version` | 412 | If-Match does not match the order or profile version (PROD-0003) |
| `precondition_required` | 428 | If-Match is missing on a command, PATCH or profile replacement |
| `COM-0001` | 400 | Request failed validation; `errors` lists the fields |
| `AUTH-0001` / `AUTH-0003` | 401 / 403 | No tenant / missing duty |

The stable PROD-00nn service codes are not yet carried in the problem body, because the kernel
`httpx.Problem` has no `serviceCode` field (handoff to wave 0).

## Wave reference

PSG-21 (wave 21), slices PSG-2101 to PSG-2106. Depends on PSG-20 (costing). Later slices: job
cards, subcontracting, the split of Started orders with the WIP transfer of BR-21.11 and order
change management. See `NOT-IMPLEMENTED.md`.

Slice 13 (read-only inquiries of 4.21.14, no migration):

- Operations overview: `GET /v1/production/operations` with legal entity, site, resource, order,
  operation status, order status and delivery-date range: each operation with its process
  quantity, planned hours (setup + run × process quantity), hours reported by route cards, good and
  error quantities and status, keyset-paged by order and operation, with totals (counts by status,
  hours, quantities) over the whole filter (`production.order.read`).
- Production journal inquiry: `GET /v1/production/journals` with legal entity, order (id or exact number), type
  (including `split-transfer`), posted state and posting-date range: journal number, type, order,
  posting date, posted, line count, the value consumed (issue and hours lines) and received
  (receipt and co-product lines) in the order currency once posted, journal entry id, keyset-paged by id, with
  the count over the whole filter (`production.order.read`).
- Order KPIs: `GET /v1/production/kpi` with legal entity, site, product, the UTC date range the orders
  ended in, an as-of date and `groupBy` site or product: orders ended, on-time % (finished, the last
  `ReportedAsFinished` entry of the status log or the `Ended` one without it, on or before the
  delivery date, over the ended orders that have one), mean cycle days (first `Started` entry to
  the finish), scrap % (error ÷ finished + error) and the open orders (current status) and those
  overdue as of the date, per group and in total; rates are null with nothing to measure
  (`production.order.read`). Read-only; no table or event of its own.

Slice 15:

- Subcontract purchase request changes (FR-21.10): purchasing hears about every change to an
  operation it was asked to buy. `subcontract.pochanged` carries the previous and the current
  quantity, unit price, amount and due date after a quantity or delivery date PATCH, an order BOM or
  route edit (also from an engineering change), a split (the original's PO shrinks) or a merge (the
  survivor's grows); `subcontract.pocancelled` withdraws the
  request when the order is cancelled or merged away, or an edit removes the operation, turns
  subcontracting off or changes the vendor resource. A cancel clears `poRequestedAt` and stamps
  `poCancelledAt` (migration `0012_subcontract_changes.sql`), so a later release or a switch back on
  requests afresh. The comparison is `domain.PlanSubcontractChanges`, run in the transaction of the
  change. The subcontract inquiry and the order's route tab show a withdrawn request.

Slice 16:

- Production parameters (PRD 4.21.13): `GET`/`PUT /v1/production/parameters` store, per legal entity ×
  site (a site id, or `All`; a site's row wins), the RAF tolerance, over-consumption tolerance,
  auto-start on feedback, estimate on create, split allocation default, trigger points enabled and
  as-built required (migration `0013_parameters.sql`, `domain.ResolveParameters`). `PUT` replaces
  under If-Match, needs every setting and enqueues `parameters.saved` in the same transaction. The
  store resolves the order's legal entity and site inside each transaction: a stored row overrides
  the service environment (`PRODUCTION_RAF_TOLERANCE_PCT` and the others, which stay the defaults
  for a site with no row) on creating and posting journals, report as finished, create, split and
  trigger evaluation. With as-built required, `:end` of an order that reported output without an
  attached as-built record is refused with 422 `PRODUCTION_AS_BUILT_REQUIRED`.
