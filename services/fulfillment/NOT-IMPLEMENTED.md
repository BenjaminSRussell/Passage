# NOT-IMPLEMENTED: fulfillment (PSG-28, wave 28)

Slice 1 builds the release, pick, pack and packing-slip core, the SSCC and GS1
rules, the posting matrix for stocked lines (physical posting and COGS at
shipment), cancellation, the shipment and packing-slip read APIs, the OpenAPI
and AsyncAPI contracts, the chart, and the web screens: fulfillment workspace,
release board, shipments list, shipment detail (confirm pick, pack container,
post packing slip with a journal preview), packing slip journal and detail
(cancel), and the GS1-128 scan page.

Slice 2 adds packing configuration and auto-pack (container types with tare,
returnable flag and packaging material; packing rules per product and optional
customer; `:auto-pack` with FUL-0012 warnings and FUL-0013), packaging
materials per product unit, country fee rates, the statistical packaging fee
lines on the packing slip (posted with a ship-to `countryCode`, mirrored on
cancel, carried in the posted and cancelled events), the packaging report per
country and month, returnable transport packaging balances per customer and
container type (raised on post, lowered on cancel and on a standalone return
receipt, never negative), the matching OpenAPI and AsyncAPI, and the web
screens: container types, packing rules, packaging materials, packaging fees
with the report, returnable packaging, auto-pack on the shipment detail, the
ship-to country on the post form, and the fee lines on the packing slip.

Slice 3 adds partial shipments (a slip posts while a planned remainder is
left, the shipment is `PARTIALLY_SHIPPED`, the remainder is re-picked, packed
and posted on another slip; each container remembers the slip it shipped with
so cancelling one slip reverts only its own containers and returnable
balances), shipment cancellation before anything ships (BR-28.6), the picking
workbench (filter released shipments by customer, carrier, pick zone and ship
date; group by customer, carrier, zone or product into waves of at most
`maxLines` lines and `maxOrders` orders without splitting a shipment; preview;
create every wave in one action with `WV-` numbers, moving the shipments to
PICKING), the waves list and detail, carrier tracking events (recorded once
per provider event key, published as `tracking.updated`, DELIVERED moves a
SHIPPED shipment to DELIVERED), the ASN event on post (`asnEnabled`), the
GS1-128 SSCC container label as ZPL, carrier and pick zone on release, the
matching OpenAPI and AsyncAPI, and the web screens: picking workbench, pick
waves and wave detail, and on the shipment detail the remainder pick, cancel,
tracking with manual entry, the ZPL label and the ASN option.

Slice 4 adds the delivered-not-invoiced report (FR-28.12, section 5.28.14):
`GET /delivered-not-invoiced` gives, for one legal entity and optionally one
customer as of a date, the open balance on account 1405 per currency and the
packing slips that make it up with their days outstanding. The balance is read
from the slip journals, so a slip posted with `cogsAtShipment` (Dr 5000) never
appears, a slip cancelled after the report date still counts on that date, and
an invoiced slip drops out. Store tests against Postgres cover AC-28.8 (scaled
down, with the Σ 1405 reconciliation), the as-of date around a cancellation, the
customer filter, tenant isolation and the legal-entity scope. The web screen
`/fulfillment/delivered-not-invoiced` shows the totals and the slips and opens a
slip on its packing slip page.

Slice 5 adds the SSCC register (section 5.28.14, BR-28.5):
`GET /sscc-register` (`WarehouseView`) lists every issued SSCC in SSCC order
with its container type and status, shipment, order, customer, the packing slip
it shipped on, the issue time and the no-reuse date (issue date + 12 months),
filtered by GS1 company prefix, customer and issue-date range and paged with a
keyset cursor. Containers are read through their shipment, so the tenant and
legal-entity row-level security applies. A store test against Postgres covers
the order, the filters, a back-dated SSCC, the cursor, tenant isolation and the
legal-entity scope. The web screen `/fulfillment/sscc-register` filters, pages
and opens the shipment of a row.

Slice 6 adds the short picks and backorders report, returnable packaging aging
and the `warehouse-sim-1k` run. A pick confirmation below plan stamps the
line's first short-pick time (migration 0005); `GET /backorders`
(`WarehouseView`, FR-28.2, section 5.28.14) lists those lines newest first with
planned, picked, shipped and the remainder, open until a re-pick reaches plan
or the shipment is cancelled, filtered by warehouse and short-pick date
(`status=all` adds closed ones) and paged with a keyset cursor.
`GET /returnable-packaging/aging` (`WarehouseView`, FR-28.16) ages every
balance above zero first in, first out from the posting dates of the slips
that shipped its returnable containers and flags it `chargeDue` once older
than `chargeDays` (default 90). The store test `TestWarehouseSim1k_ZeroInventoryMismatch`
runs AC-28.7 against Postgres: 1,000 seeded orders released, picked (one in ten
short, the remainder re-picked), packed and posted, every order SHIPPED, and per
product the `packingslip.posted` quantities equal the slip lines and the
released quantities. The web screens `/fulfillment/backorders` and
`/fulfillment/returnable-aging` filter both reports, and the workspace links
them.

Slice 7 closes the open remainder of a partially shipped shipment (FR-28.8,
`short_pick_action` CANCEL_REMAINDER): `POST /shipments/{id}:close-remainder`
(`WarehouseRelease`, `If-Match` and `Idempotency-Key`) is allowed once a slip has
shipped part of the quantity, while the shipment is PARTIALLY_SHIPPED or its
remainder is being picked, and nothing is packed waiting to ship. Every line
shrinks to what shipped (migration 0008 adds `qty_closed` and `closed_at`), the
backorder closes, the shipment is SHIPPED and `shipment.closed` carries per
order line the closed quantity (`qty`) and the part of it that was picked but
never packed (`picked_unpacked`). The short picks and backorders report shows
the closed quantity (`qtyClosed`) of a closed line. Store (including tenant and
legal-entity isolation), domain and API tests cover it; the shipment detail
gets a "Close remainder" button with confirmation and a Closed column, and the
backorders screen a "Qty closed" column. The AsyncAPI `ShipmentReleased` message gained its title and summary, so
the fulfillment OpenAPI and AsyncAPI specs have no FAIL lines on the doc gates.

Everything listed below is missing. Each row gives a status and a reason.
Status is `later slice of PSG-28` (same service, next slice) or `handoff`
(needs another wave, the kernel, or a path outside this wave's ownership).

Slice 8 adds unpacking (BR-28.6): `POST /shipments/{id}/containers/{cid}:unpack`
(`WarehousePack`, `If-Match` and `Idempotency-Key`) takes a CLOSED container apart
while the shipment is PICKING, PICKED or PACKED. The container's packed quantities
return to the unpacked pool, a PACKED shipment goes back to PICKED (repack, or
cancel once it holds no container), the container row stays with status
`UNPACKED` (migration 0009) so its SSCC is never issued again and still shows in
the SSCC register, and `dev.passage.fulfillment.container.unpacked.v1` is
written through the outbox. A container on a posted slip (SHIPPED) is refused
with FUL-0021, including the shipped container of a partially shipped shipment
whose remainder was repacked. The shipment version is bumped before the container
and lines change, so two concurrent unpacks of one container give one success and
one 412. Store tests against Postgres cover repack with a fresh SSCC, cancel after
unpacking everything, the partial-shipment case, the concurrent unpack, the
replay, tenant and legal-entity scope. The shipment detail drops unpacked
containers and has an Unpack button per closed container with a confirmation; the
SSCC register shows them as "Unpacked".

Slice 9 adds wave-level pick confirmation and short pick reasons (FR-28.13,
section 5.28.14). `POST /waves/{id}:confirm-pick` (`WarehousePick`,
`Idempotency-Key`) confirms the listed picks across all shipments of a wave in
one transaction and, with `pickRest`, picks every other unconfirmed line in
full; it writes one `pick.confirmed` per shipment carrying `wave_id`, refuses a
line outside the wave (FUL-0041) and a listed line on a shipment that can no
longer pick (FUL-0001), and answers FUL-0050 when nothing is left to pick.
`pickRest` leaves a cancelled or already picked shipment of the wave alone and
decides what to add only after locking each shipment, so it never overwrites a
pick another command confirmed meanwhile. A pick below plan
may carry a `reason` (OUT_OF_STOCK, DAMAGED, NOT_FOUND, QUALITY_HOLD, OTHER;
migration 0010 adds `shipment_line.short_pick_reason`), also on the single
shipment `:confirm-pick`; it is stored on the line, carried in `pick.confirmed`
as `short_reason`, and shown in the short picks report (`reason`) and the
shipment line. `GET /waves/{id}` now returns the wave's lines with their pick
state. Store tests against Postgres cover the all-shipments command, the
atomic refusals, the replay, a cancelled shipment in the wave, a line already
confirmed by a single-shipment pick, `wave_id` and `short_reason` in the
event, and tenant and legal-entity isolation. The wave detail page has a
"Confirm picks for the wave" card (planned quantity by default, reason when
short, only lines of shipments that still pick), the shipment pick form offers
the reason on a short line, the shipment lines show it, and the backorders
screen has a Reason column. The reason is optional and the latest one given
for a line is kept.
Slice 10 adds batch, serial and location capture at pick (BR-28.10, FUL-0006).
A release line may carry `trackingRequired` (the caller knows the product's tracking
dimension group); it is stored on the shipment line (migration 0011). `POST
/shipments/{id}/lines/{lid}:capture-tracking` (`WarehousePick`, `If-Match`,
`Idempotency-Key`) replaces the unshipped batch, serial and location entries of a
picked line in the new `pick_tracking` table (tenant and legal-entity policies,
granted in the same migration). A serial is one unit and unique per product while
its shipment is live (FUL-0036; cancelling the shipment frees it), entries together
with what already shipped cannot exceed the picked quantity (FUL-0035), and an
entry without batch and serial is refused (FUL-0050). The packing-slip post refuses
a tracking-required line whose entries do not cover exactly the quantity the slip
ships (FUL-0006), stamps the entries with the slip, and `packingslip.posted` carries
`product_id` and the `tracking` entries per line so inventory can issue the right
batches and serials; cancelling the slip puts the entries back on the pick, so a
partial shipment keeps the entries of the first slip and tracks the remainder
separately. A re-pick cannot drop the picked quantity below the line's entries
(FUL-0035), and closing the remainder drops the entries that never shipped, so their
serials are free again. A shipped serial stays taken for its product (it cannot be
picked again) until a return frees it, which is not built (see the handoff below). `dev.passage.fulfillment.pick.tracked.v1` is written through the outbox
on every capture. Store tests against Postgres cover refusal at post with none and
short entries, the replace and the version check, the event payload, the stamp and
the cancel, serial uniqueness across shipments and its release on shipment cancel,
the partial shipment, the re-pick guard and the close of the remainder, and tenant and
legal-entity isolation. The shipment detail has
a "Batch and serial tracking" card (line chooser, entry rows with batch, serial,
location and quantity, the covered quantity) and a "Batch / serial" column on the
lines; the release form has a "Batch or serial tracked" checkbox per line. No new duty is introduced (`WarehousePick`), so there is nothing new for #313.
The OpenAPI and AsyncAPI have zero FAIL lines on the doc gates.

Slice 11 adds wave assignment and close (FR-28.13, section 5.28.6). Migration 0012
gives `wave` a `picker`, `assigned_at`, `closed_at`, a `version` and the status
CLOSED, and grants `passage_app` UPDATE on it (no new table).
`POST /waves/{id}:assign` (`WarehouseRelease`, `If-Match` with the wave version,
`Idempotency-Key`, body `picker`) gives an OPEN wave to a picker or hands it to
another one; a CLOSED wave, or the picker it already has, is refused.
`POST /waves/{id}:close` (same headers) closes an OPEN wave once every line of a
live shipment is pick-confirmed, else FUL-0055 with the count left; lines on
cancelled shipments do not count. `dev.passage.fulfillment.wave.assigned.v1`
(with `previous_picker`) and `dev.passage.fulfillment.wave.closed.v1` (with
`short_lines`) are written through the outbox in the same transaction. The waves
list filters by `status`, `picker` and `unassigned`, and `GET /waves` and
`/waves/{id}` return `version`, `picker`, `assignedAt` and `closedAt`. Store tests
against Postgres cover assign, replay, stale version, same picker, tenant and
legal-entity scope, the events, the filters, close refusals with open lines and a
cancelled shipment, close with a short line, replay, and that a CLOSED wave is final
(no assign, close or wave pick);
API tests cover the validation, headers and duty. The wave detail has an "Assign the
wave" card, a "Close wave" button with confirmation and a printable "Pick list" in
zone order, and the waves list a status and picker filter. No new duty is introduced
(`WarehouseRelease`), so there is nothing new for #313. The OpenAPI and AsyncAPI have
zero FAIL lines on the doc gates.

Slice 16 adds partial vendor confirmations and closing the remainder of a drop-ship (FR-28.7). Migration 0018
adds `drop_ship.qty_confirmed`, `qty_closed` and `closed_at`, the status PARTIALLY_CONFIRMED, and the table
`drop_ship_confirmation` (one row per vendor shipment with its quantity, `vendorShipmentRef`, ship and booking
dates and journal; tenant and restrictive legal-entity policies; granted SELECT, INSERT to `passage_app`; a vendor
shipment reference is booked once per drop-ship; the backfill of `qty_confirmed` on drop-ships confirmed before it lifts
FORCE ROW LEVEL SECURITY for the owner inside the migration transaction and forces it again, because a non-superuser owner
would otherwise update no row and the new split check would refuse the migration). `POST /drop-ships/{id}:confirm` takes an optional `qty`
(everything still open when left out, so existing callers are unchanged): it books Dr 5000 / Cr 2110 for that
quantity at the PO cost, the drop-ship is PARTIALLY_CONFIRMED while quantity is open and CONFIRMED when none is.
A quantity above the open quantity is refused with FUL-0063, a reused vendor shipment reference with FUL-0064.
`POST /drop-ships/{id}:close-remainder` (`WarehouseRelease`, `If-Match`, `Idempotency-Key`) closes the open
quantity of a PARTIALLY_CONFIRMED drop-ship without booking it (the drop-ship is CONFIRMED, `qtyClosed` is set);
cancel is still OPEN only and says to close the remainder instead. `dropship.confirmed` is written once per vendor
shipment with `confirmation_seq`, `confirmed_qty`, `qty_confirmed` and the journal of that confirmation only (the
ledger posts it with the drop-ship id plus the sequence); `dropship.closed` is new. `GET /drop-ships/{id}` returns
`qtyConfirmed`, `qtyClosed`, `qtyOpen` and the `confirmations` with their journals; `journal` lists the lines of all
of them in order. Store tests (Postgres, as the application role) cover the three-part confirmation that books exactly
10 x 12.50, the refusals, replay, the concurrent 412, close-remainder with its event, and tenant and legal-entity
isolation; API, domain and web tests cover the rest. Built in review: the parts of a split confirmation add up to the
whole-line amount (cumulative rounding, see the rounding row), the 0018 backfill works for a non-superuser owner, and
the concurrency test holds the drop-ship row so the two confirmations really overlap. The drop-ship detail takes a quantity (default the open
quantity), lists the vendor shipments and has a "Close remainder" button with confirmation. No new duty is
introduced, so there is nothing new for #313. The
OpenAPI and AsyncAPI have zero FAIL lines on the doc gates.

Slice 17 adds the release and wave parameters per warehouse (section 5.28.13: `release_horizon_days`,
`release_requires_full_atp`, `wave_max_lines`, `wave_max_orders`; FR-28.1, FR-28.13). Migration 0020 adds
`warehouse_release_rule` (tenant and restrictive legal-entity policies; granted SELECT, INSERT, UPDATE to
`passage_app`; range checks 0-30, 1-2000, 1-500). `GET /release-rules`, `GET` and `PUT /release-rules/{warehouseId}`
(`WarehouseView`, `WarehouseRelease`; `If-Match` with the version, 0 for none; `Idempotency-Key`) read and save it.
A warehouse without a saved rule reads, at version 0, what a release run and the workbench apply to it: the
service configuration for the horizon and the ATP rule (`RELEASE_HORIZON_DAYS`, `RELEASE_REQUIRES_FULL_ATP`; 2 days
and full ATP unless configured) and 200 lines, 50 orders, so callers that name their own values, and warehouses
without a rule, behave exactly as before. A save under another legal entity than the warehouse's (its saved rule,
its short pick setting or its shipments) or outside the caller's scope is FUL-0057; two first saves at the same
moment write one row and the other is stale (412). `dev.passage.fulfillment.warehousereleaserule.changed.v1` (with
the previous values) goes through the outbox. `POST /releases:run` takes the saved rule when the request names no
`horizonDays` or `requireFullAtp` (a request value wins), and `waves:preview` and `POST /waves` take `waveMaxLines`
and `waveMaxOrders` for a `maxLines` or `maxOrders` left out. Store tests (Postgres, as the application role:
default, versioned save, replay, event, concurrent first saves, tenant and legal-entity isolation, agreement with
the short pick setting, configured defaults, range checks), API and web tests cover it; `/fulfillment/release-rules`
lists the rules (`WarehouseView`) and saves them (form shown to `WarehouseRelease` only). Built in review: the
legal entity of a warehouse is now shared by its short pick setting and its release rule (neither can be saved
under another one than the other), the unsaved rule reads the service configuration instead of fixed values, the
first-save race is tested, and the web form keeps the saved version of a typed warehouse id. No new duty. The
release-rule routes are seeded in `policies/authz/data.json`. Slice 18 built release priority ordering and
partial-quantity release by ATP; not built: `posting_date_source`, `auto_pack_enabled` and
`workbench_group_key_default` (the other warehouse parameters of section 5.28.13). The OpenAPI and AsyncAPI have
zero FAIL lines on the doc gates.

Slice 18 keeps journal orders out of the warehouse and adds release priority and partial release by ATP (FR-28.1,
BR-27.7). Sales now carries `order_type` on `order.confirmed.v1` and a JOURNAL order skips credit check and ATP, so it
must never ship. This service reads no sales events (the caller offers the confirmed lines), so the release line takes
`orderType` (JOURNAL, ORDER, SUBSCRIPTION or RETURNED, ORDER when left out; one type per order): release skips a
JOURNAL line as `journal_order`, and migration 0021 stores the type on the shipment (`orderType`, also carried to a
backorder shipment). A shipment that carries JOURNAL anyway refuses `:confirm-pick`, the wave pick, `containers`
(pack), `:auto-pack`, `:capture-tracking` and the packing slip post with FUL-0065 (422), stays out of the picking
workbench and cannot be put in a wave (cancel stays allowed), and `POST /drop-ships` refuses `orderType` JOURNAL with
FUL-0065. A release line also takes `priority` (1 first to 9, 5 when left out, one per order): a run creates the
shipments of higher priority orders first (then the earlier ship date), so they get the lower shipment numbers, and the
shipment stores and returns `priority` (migration 0021). Partial release: a line takes `atpQty` (0 or more). With
full ATP not required (request `requireFullAtp` false or the warehouse rule) a line above its `atpQty` is released for
`atpQty` and its shipment line is flagged `partial` (migration 0021); an `atpQty` of 0 is skipped as `atp_short`; the
next run offering the same order line releases what is still open (the order line quantity less what live shipments
plan, counting a closed remainder only when no backorder shipment carries it) for the new `atpQty`, and a line that is
fully dealt with stays `already_released`. With full ATP required a line above its `atpQty` is skipped as `atp_short`;
a line with no `atpQty` behaves as before (`atpOk` decides). Store tests (Postgres, as the application role), domain,
API and web tests cover it; the release board has the order type, priority and ATP quantity fields and the shipment
detail shows the priority and which lines were released in part. No new duty and no new route. Handoff to sales (wave
27): its release call must send `orderType` (from `order.confirmed`), `priority` and `atpQty`; until it does every line
reads as an ORDER of priority 5 and `atpOk`, as before. Priority does not yet order the picking workbench waves or the
release board list, and a line's ATP is not read from inventory (wave 16). The OpenAPI and AsyncAPI have zero FAIL
lines on the doc gates (`FUL-0065` is documented on each refusing operation).
Built in review: a line whose partial shipment is cancelled after its remainder was released is released again
for the cancelled quantity (the "released in part" test now looks at cancelled shipments too, while the open
quantity counts only live ones; before, the line read as `already_released` and the cancelled quantity was lost),
with a store test that also covers a short-picked remainder moved to a backorder shipment (still dealt with, so
nothing is released twice); migration 0021 now only changes the catalog (fast defaults, `NOT VALID` checks,
`lock_timeout` 5s) and 0022 validates the two checks under SHARE UPDATE EXCLUSIVE, so no scan of `shipment` runs
under ACCESS EXCLUSIVE. Concurrency: runs for one warehouse hold the per-warehouse advisory lock before they read
what is released, so two runs never release the same remainder; a cancel or short pick that commits during a run
can only leave quantity for the next run, never release it twice. Known limit: the full-ATP check compares
`atpQty` with the order line quantity, so with full ATP required a remainder whose ATP covers it but not the whole
line is skipped as `atp_short` until full ATP is turned off or the ATP covers the line.

Slice 19 follows sales' events so fulfillment no longer depends on the release caller to know a JOURNAL order or an
order on hold (BR-27.7, section 5.28.5). The service gets a consumer mode (`fulfillment consume`, the same image;
`consumer.enabled` in the chart, off by default) built on the platform eventing consumer: consumer
`fulfillment-order` (Kafka group of the same name) follows `dev.passage.sales.order.confirmed.v1` on
`passage.sales.order` and `dev.passage.sales.orderhold.applied.v1` / `released.v1` on `passage.sales.orderhold`,
runs as the service principal `service:fulfillment-order` with the flag duty `fulfillment.order.sync`, reads through the
`consumer_inbox` (migration 0023, tenant RLS, SELECT and INSERT for `passage_app`), checks the CloudEvents `source` is
`sales` and dead-letters anything else (and any event that is malformed) to `passage.sales.order.dlq`, and retries
store errors. It stores `sales_order_type` (order id to type, ordered by the sales order `version`, so an older event
never turns a newer type back) and `sales_order_hold` (one row per hold, active after applied and inactive after
released, same ordering; SELECT, INSERT, UPDATE). Both have tenant RLS, enabled and forced. Release now replaces the
caller's `orderType` of every line with the stored one, except that JOURNAL from either side wins (a stored JOURNAL
order is skipped as `journal_order` whatever the caller sent, and a caller's JOURNAL is kept over a stored ORDER, so
the override only ever moves toward not shipping; the stored type may lag a newer sales version), marks the lines of
an order with an active hold `held`, and `POST /drop-ships` refuses a stored JOURNAL order (or a caller's JOURNAL)
with FUL-0065. The packing slip post is refused with FUL-0001 while the order has an active hold, and with FUL-0065
when sales confirmed the order as JOURNAL even if its shipment was released as ORDER before the event arrived.
Holds are ordered per hold by the sales order `version` the event carries (sales writes the order version after the
apply or release, so a release is always newer than its apply); a release delivered before its apply leaves the hold
inactive. An `order.confirmed` without `order_type` is dead-lettered, never read as ORDER. Neither sales event
carries `legal_entity_id`, so `sales_order_type` and `sales_order_hold` have no legal-entity column or policy: they
are tenant-scoped and read only by order id inside release, drop-ship creation and post, never listed. An order fulfillment has not heard of keeps today's
behaviour. The consumer exits at start until its principal is seeded. No new route and no new entity with a screen, so
no web change; the OpenAPI describes the new FUL-0001 case on the post and the AsyncAPI lists the three consumed
events, both with zero FAIL lines on the doc gates. Not built: storing or using the per-line `atp_qty`, a hold check on
pick, pack and auto-pack (built in slice 20), a hold check on drop-ship creation and vendor confirmation (a confirmation records a
shipment the vendor already made), and a hold or order type screen.

Handoffs from slice 19:

| Item | Status | Reason |
|---|---|---|
| Authz seed: service principal `fulfillment-order` with duty `fulfillment.order.sync`, issuer-bound to the `fulfillment` service (`platform/go/tenant/serviceprincipal.go` `servicePrincipals` and `servicePrincipalIssuers`, and `policies/authz/data.json` `service_principals`, as for `shopfloor-production`). `fulfillment.order.sync` is a flag duty held only by that principal: no user role, no nav item, no route. | handoff for #313 (authz seed) | The consumer refuses to start until the table has it (`orderSeeded`), and the handlers refuse a principal without the duty. |
| Deploy: Kafka topics `passage.sales.order`, `passage.sales.orderhold` (already produced by sales' outbox relay) and the dead-letter topic `passage.sales.order.dlq` (create it); ACLs for the Kafka user `fulfillment-consumer` (secret named by `consumer.kafka.secret`): read on the two sales topics, consumer group `fulfillment-order`, write on the dead-letter topic. Then set `consumer.enabled`, `consumer.kafka.brokers` and `consumer.kafka.secret` in the environment values. | handoff to platform / deploy | Nothing is provisioned from this service. The migration is run by the PreSync job as for the API. |
| Existing orders: only events consumed after the consumer starts are known, so orders confirmed or held earlier stay unknown (caller's type, no hold) until sales republishes them or a replay from the topic start is run (the inbox makes a replay safe). | deviation; handoff to deploy | Start the consumer group from the earliest offset when it is first enabled. |

Slice 20 follows sales' `dev.passage.sales.order.remaindercancelled.v1` (sales slice 23) on the same consumer and topic
(`passage.sales.order`, consumer `fulfillment-order`; no new topic, principal or duty), and adds hold checks on pick, pack
and auto-pack. The consumer reads `order_id`, `legal_entity_id`, `version` and per line `line_id` (the order line) and
`kept_qty`. Per line it stores the quantity the line keeps in `order_line_kept` (migration 0024, tenant and legal-entity
RLS, SELECT, INSERT, UPDATE; an event with a lower sales order `version` than the stored one changes nothing for that
line) and then plans the cut from the live shipments of the order line: allowed unshipped quantity is `kept - shipped`,
and the unshipped planned quantity above it is cut, newest shipment first, but never below what is packed or shipped.
A shipment line shrinks (`qty_planned` and `qty_picked` to the new plan, `qty_closed` grows, unposted batch and serial
entries are dropped only when they cover more than stays picked, a line cut to nothing counts as pick-confirmed), the
shipment moves on to PICKED or PACKED when the cut leaves every line confirmed or every picked unit packed, `shipment.closed` is published with the new `reason`
`order_remainder_cancelled` (the closed quantity and `picked_unpacked` so inventory releases the reservation and puts
picked stock back; sales must not cancel again), and a shipment left with nothing planned, nothing shipped and no
container is cancelled with `shipment.cancelled` (the shipments of an order are locked in id order first, so a post or
pick on one waits and then sees the cut). A partly shipped shipment whose remainder is gone becomes SHIPPED. The consumer
principal has no legal entity, so the store sets the transaction's legal-entity scope to the event's `legal_entity_id`
for the call (as production's intake does). A post that would ship more of an order line than the kept quantity is
refused with FUL-0066 (`order_line_kept` plus the line's shipped quantity across shipments), so packed quantity that
could not be cut cannot ship. The cases the service cannot fix alone go to a new supervisor list,
`fulfillment_exception` (migration 0024, tenant and legal-entity RLS, SELECT, INSERT, UPDATE): `SLIP_POSTED_BEYOND_KEPT`
(the race: a slip was posted for more than the kept quantity before the event arrived; nothing is retried or reversed, the
exception names the latest posted slip and the surplus, and the remedy is a return through wave 30 or a new order) and
`PACKED_BEYOND_KEPT` (packed, unposted quantity above the kept quantity; unpack and put the goods back: unpacking a
container re-applies the kept quantity, so the unpacked quantity above it is cut with `shipment.closed` and the kept
quantity can be repacked and posted). One open
exception exists per kind, order line and shipment. `GET /exceptions` and `GET /exceptions/{id}` are under `WarehouseView`,
`POST /exceptions/{id}:resolve` (If-Match version, Idempotency-Key, a note of 1 to 500 characters) under `WarehouseRelease`;
`exception.raised.v1` and `exception.resolved.v1` are published through the outbox. Pick, pack, auto-pack and wave pick
now refuse a held order with FUL-0001 (a wave pick of the rest skips a held order's shipment; a line of it named in the
call fails the call); post already did. The web module gets the Exceptions list (open by default, status filter) and the
exception detail with the advice per kind, links to the shipment and slip, and the resolve form (shown to `WarehouseRelease`
only). The OpenAPI (60 operations) and the AsyncAPI (28 events) have zero FAIL lines on the doc gates.

Handoffs and gaps from slice 20:

| Item | Status | Reason |
|---|---|---|
| Authz seed: route rows in `policies/authz/data.json` for `/v1/fulfillment/exceptions` and `/v1/fulfillment/exceptions/*` (GET, `WarehouseView`) and `/v1/fulfillment/exceptions/*:resolve` (POST, `WarehouseRelease`). No new duty name and no new principal. | handoff for #313 (authz seed) | Both duties are already seeded; the web nav item is gated on `WarehouseView`. |
| Inventory must consume `shipment.closed` with `reason` `order_remainder_cancelled` and `shipment.cancelled` as for a manual close or cancel (release the reservation of `qty`, put back `picked_unpacked`); sales must not cancel the order line again on that reason, and should show `exception.raised.v1` where it tracks order fulfilment problems. | handoff to inventory (wave 16) and sales (wave 27) | No consumer exists yet. |
| Packed quantity beyond the kept quantity is not unpacked by the service | deviation | A container is a physical object with an SSCC; the supervisor unpacks it (`:unpack`), which cuts the unpacked quantity above the kept quantity. Until then the post is refused (FUL-0066). The exception is resolved by hand; it is not closed by the unpack. |
| Unposted batch and serial entries of a cut line that cover more than stays picked are dropped as a whole | deviation | Capture is repeated for what stays picked; trimming entries by quantity would guess which serials stay. Entries that still fit are kept. |
| Pick and pack of a line sales cut back are not blocked by the kept quantity; only the post is (FUL-0066) | deviation | The cut already shrinks the plan to what is packed or shipped, so a pick above the plan is refused as an over-pick (FUL-0004). |
| A slip posted beyond the kept quantity is not reversed or returned by the service | handoff to receivables / returns (wave 30) | The exception tells the supervisor; return authorisation and credit are wave 30. |
| A hold check on capture tracking and on drop-ship creation and vendor confirmation | deviation | Capture tracking is a recording step, and a vendor confirmation records a shipment the vendor already made. |
| `tools/rlscheck` reports `fulfillment.service_token_replay` (migration 0019, from main) as `rls-not-enabled-and-forced`: the table is missing from `tools/rlscheck/allowlist.go`, where the other services' replay tables carry the permanent SECURITY DEFINER purge reason. The slice 20 tables pass. | handoff (repo tooling, outside this wave's paths) | Add `"fulfillment.service_token_replay:" + problemNotForced` with the same reason as the other services. |
| `order_line_kept` is never pruned | deviation | One small row per cancelled order line; a retention job belongs with the data lifecycle work. |

Slice 21 closes the two gaps slice 20 left. Drop-ship: on `order.remaindercancelled.v1` the same consumer now also reads the
active drop-ship of each order line (`followDropShip`, in the consumer's transaction, after the shipment lines were cut). The
vendor quantity not yet shipped above what the line keeps is closed with no booking, as `:close-remainder` does
(`qty_closed` grows, an OPEN drop-ship stays OPEN with less open, a PARTIALLY_CONFIRMED one becomes CONFIRMED when none is
left), and `dropship.closed.v1` is published; an OPEN drop-ship that keeps nothing is CANCELLED and `dropship.cancelled.v1` is
published. Both events carry the new optional `reason` `order_remainder_cancelled` (additive, kept at v1) so sales does not
cancel again. A vendor confirmation already booked above the kept quantity raises the new exception kind
`DROPSHIP_CONFIRMED_BEYOND_KEPT` (migration 0025 widens the kind check on `fulfillment_exception`; no new table, so the grants
of 0024 stand); nothing is reversed and the supervisor arranges a vendor return or a new order. Creating a drop-ship above the
quantity sales kept, and a vendor confirmation that would take the confirmed total above it, are refused with FUL-0066.
Release: a run reads `order_line_kept`: a line sales cut back is offered only for the kept quantity minus the planned quantity of
its live shipments and the quantity of its active drop-ship (capped, and not flagged partial: nothing is left for a later run, so
a repeat run skips it as `already_released`), and is skipped with the new reason `order_remainder_cancelled` when it has no live
shipment and nothing is left to offer. The release OpenAPI skip reason enum, the exception kind enums, the
AsyncAPI drop-ship `reason` and exception kinds, and the web labels (skip reason, exception kind, advice) are updated. No new
route, duty, principal, topic or entity; the exception list and detail pages already show the new kind.

Handoffs and gaps from slice 21:

| Item | Status | Reason |
|---|---|---|
| Sales must treat `dropship.closed.v1` and `dropship.cancelled.v1` with `reason` `order_remainder_cancelled` as already known (no second cancel), and inventory has nothing to do (no stock moves for a drop-ship). | handoff to sales (wave 27) | No consumer exists yet. |
| A drop-ship that keeps part of its quantity and has nothing confirmed stays OPEN with `qty_closed` above zero; the vendor's purchase order line is not reduced by fulfillment. | handoff to purchasing (wave 17) | Purchasing must lower the PO line quantity from `dropship.closed.v1` (`qty_closed`); fulfillment does not call purchasing. |
| A vendor confirmation booked above the kept quantity is not reversed (the Dr 5000 / Cr 2110 stays) | deviation; handoff to purchasing (wave 17) and the ledger | The exception tells the supervisor; a vendor return and its reversal are not built (see "A confirmed drop-ship cannot be reversed"). |
| A drop-ship created while the consumer applies the cancellation (the consumer does not see the uncommitted drop-ship, and the create does not see the uncommitted kept row) keeps its full quantity. | deviation | The vendor confirmation is held to the kept quantity (FUL-0066, `TestDropShip_ConfirmAboveKeptIsRefused`); the supervisor cancels the OPEN drop-ship and creates one for the kept quantity, or closes the remainder after a partial confirmation. Closing the window would need a lock on an order line that has no row yet. |
| A vendor confirmation above the kept quantity is refused (FUL-0066) even when the vendor has already shipped (the purchase order line is not lowered by fulfillment, see above), so that shipment cannot be booked here. | deviation; decision for Ben | The ticket asks for the refusal; the alternative is to book it and raise `DROPSHIP_CONFIRMED_BEYOND_KEPT`, as the consumer does for a confirmation booked before the cancellation arrived. |
| A second drop-ship of a line that already has an active one is refused with FUL-0066 rather than FUL-0061 when its quantity is also above the kept quantity. | deviation | The kept check runs before the insert that detects the active drop-ship; both are refusals. |
| Authz seed: none. The new exception kind and skip reason use existing duties and routes. | none | |

Slice 22 keeps the history behind the returnable transport packaging balances and lets a logistics admin correct one
(FR-28.16, BR-28.13). Migration 0026 adds `returnable_packaging_movement` (tenant RLS enabled and forced, SELECT and INSERT for
`passage_app`, append-only): every change of a balance writes one signed row with `balance_after`, so the rows of one customer
and container type replay to the balance. `SHIPPED` and `SHIPMENT_CANCELLED` are written by the packing slip post and cancel
in the same transaction and name the slip, `RETURNED` by the return receipt, `ADJUSTED` by the new
`POST /v1/fulfillment/returnable-packaging:adjust` (`ReturnablePackagingMaintain`; section 5.28.17 lists `WarehouseReceive` as
its segregation-of-duties conflict, so receiving returns does not let a caller correct a balance). The adjustment is a signed non-zero whole number with a mandatory
3 to 200 character reason and an optional reference, creates the balance row when the customer has none (an opening balance),
is refused with FUL-0015 below zero and with the new FUL-0069 for a container type that is unknown or not returnable, takes an
Idempotency-Key and keeps who made it. `GET /v1/fulfillment/returnable-packaging/movements` (`WarehouseView`) lists the history
newest first, filtered by customer, container type and kind. The return receipt now publishes
`dev.passage.fulfillment.returnablepackaging.returned.v1` (qty negative) and the correction
`dev.passage.fulfillment.returnablepackaging.adjusted.v1`, both through the outbox in the same transaction. Store tests against
Postgres (as a NOBYPASSRLS role) cover the replay of the history to the balance through post, cancel, repost, return and
two corrections, the filters, tenant isolation, the refusals writing nothing, the events, the opening balance with an
idempotent replay, and overlapping corrections and returns (including racing first corrections that create the balance row)
whose history still replays step by step. Every writer locks the balance row before it appends the movement, and a movement
is stamped with `clock_timestamp()` at the insert, so `created_at` order is the order the balance changed in. The web screen `/fulfillment/returnable-movements` lists the movements with filters, links a slip, and
shows the "Correct a balance" form only to a caller holding `ReturnablePackagingMaintain` (the service enforces it); the returnable packaging
page links to it. Not built: the `rtp_tracking_enabled` and `rtp_charge_days` legal-entity settings (see the returnable row
below), a correction that ages (a positive correction has no shipment, so it never becomes `chargeDue`), and history before
this migration.

Handoffs and gaps from slice 22:

| Item | Status | Reason |
|---|---|---|
| Authz seed: new duty `"ReturnablePackagingMaintain": {"permissions": [{"resource": "/v1/fulfillment/returnable-packaging:adjust", "methods": ["POST"]}]}` (section 5.28.17, "RTP balances adjustments", default role Logistics admin), and `{"resource": "/v1/fulfillment/returnable-packaging/movements", "methods": ["GET"]}` added to `WarehouseView`. | handoff for #313 (authz seed) | `policies/authz/data.json` is outside this wave's paths. Until it is seeded the correction and the movements list answer 403 at the gateway. The web nav item is gated on the seeded `WarehouseView`; the correction form needs the new duty in the principal's token. |
| Segregation of duties: `ReturnablePackagingMaintain` conflicts with `WarehouseReceive` (section 5.28.17), so one person cannot both receive returned packaging and correct the balance it lowered. | handoff to compliance (wave 48, `sod_rule` seed, FR-48.4) | A role-level conflict is detected by the compliance rule set when roles are assigned; the service checks only that the caller holds `ReturnablePackagingMaintain`. |
| Wave 30 (receivables) can read `returnablepackaging.returned.v1` and `.adjusted.v1` to credit packaging a customer sent back, and the movements to charge unreturned items. | handoff to receivables (wave 30) | Fulfillment does not call receivables. |
| The `rtp_tracking_enabled` and `rtp_charge_days` settings stay unbuilt: balances are kept per tenant, not per legal entity, so a per-legal-entity setting has nothing to attach to; the caller still passes `chargeDays` (default 90) to the aging. | deviation | A legal-entity column on `returnable_packaging_balance` would change the key every post and return uses. |
| Balances that existed before migration 0026 have no earlier movements; the first movement of such a balance shows `balance_after` after a change from a balance the history never saw. | deviation | There is no data to backfill from: only the balance was stored. |
| Movements are listed newest first up to `limit` (default 100, max 500) with no cursor. | deviation | As the other report lists of this service. |

Slice 23 builds the over-delivery tolerance (BR-28.7, FUL-0005, `overdelivery_pct` of section 5.27). Migration 0027 adds
`delivery_tolerance` (tenant RLS enabled and forced; SELECT, INSERT and UPDATE for `passage_app`; one row per product and customer
scope, a missing product or customer meaning every one, unique per scope, with a version) and, on `shipment_line`,
`over_tolerance_pct` with the check `qty_picked <= qty_planned * (1 + over_tolerance_pct / 100)` in place of
`qty_picked <= qty_planned`. `GET /v1/fulfillment/delivery-tolerances` (`WarehouseView`, filters `productId`, `customerId`, `limit`) and
`PUT /v1/fulfillment/delivery-tolerances` (`PackingRuleMaintain`; `If-Match` with the version read, 0 for a new scope;
`Idempotency-Key`; body `productId`, `customerId`, `overdeliveryPct`, 0 to 100 with at most 4 decimals) keep them and write
`dev.passage.fulfillment.deliverytolerance.changed.v1` with the value replaced. Confirm-pick (single shipment and wave) looks the
tolerance up only for a quantity above plan: the most specific scope wins (product and customer, product, customer, general),
and none means 0. A pick above plan is FUL-0004 with no tolerance, FUL-0005 above planned plus the tolerance (the ceiling is cut to
6 decimals), and accepted within it; the line keeps the percentage in force, `shipment.line.overTolerancePct`, and
`pick.confirmed` carries `over_picked`. The over-picked quantity packs and posts as picked (the slip, journal, ASN and
`packingslip.posted` carry the whole quantity), a shipment whose lines all reached plan is SHIPPED, and an over-shipped line has
nothing to close (`PostTarget`, `CloseRemainder`, the remainder-cancel planner and the backorder report treat a negative
remainder as 0; a remainder cut that leaves every line shipped completes the shipment even with an over-shipped line). When
sales cancelled the remainder of the order line (`order_line_kept`, slice 20), a pick above plan is also refused with FUL-0066
once the line would carry more than the order line keeps (its other live shipment lines count with the larger of planned and
picked, an active drop-ship with its open quantity): the post would refuse it anyway (`checkKeptQty`). Web: `/fulfillment/delivery-tolerances` (list, edit, save with version; the form only for `PackingRuleMaintain`),
the pick form on the shipment detail accepts a quantity within the ceiling of the saved tolerances and explains FUL-0004 and
FUL-0005 before calling, and the lines table shows the excess over plan. Tests: domain, API, Postgres store (resolution order,
version, replay, event, tenant isolation, pick to post to cancel of an over-shipped line, the database check) and web.

Handoffs and gaps from slice 23:

| Item | Status | Reason |
|---|---|---|
| Authz seed: `PackingRuleMaintain` gains `{"resource": "/v1/fulfillment/delivery-tolerances", "methods": ["PUT"]}` and `WarehouseView` gains `{"resource": "/v1/fulfillment/delivery-tolerances", "methods": ["GET"]}` in `policies/authz/data.json`. No new duty: section 5.28.17 lists none for the tolerance, so it follows the other packing and delivery master data (Warehouse admin). | handoff for #313 (authz seed) | Policies are outside this wave's paths. Until seeded the routes answer 403 in a real deployment. |
| Wave 27 (sales) holds its own `overdelivery_pct` for agreements (BR-27.9) and the order's shipped quantity; it must accept `qty` above the ordered quantity on `packingslip.posted.v1` (the line `qty` can now exceed the shipment line's planned quantity) and may read `deliverytolerance.changed.v1`. Billing (wave 29) invoices the shipped quantity. | handoff to sales (wave 27) and billing (wave 29) | Neither is read from here. Fulfillment does not know the order line's ordered quantity, only what each shipment line plans: the tolerance applies per shipment line, so an order line split over several shipments may exceed its ordered quantity by at most the tolerance overall. The order's quantity limits the pick only once sales cancelled the remainder (`order_line_kept`, FUL-0066). Billing proposes from the slip lines and does not compare them with the order (checked: `services/billing/internal/domain/shipment.go`). |
| An over-pick confirmed before sales' `order.remaindercancelled.v1` arrives is not trimmed by the cut (the planner cuts the planned quantity, never the picked excess above it); once packed, the post is refused with FUL-0066 and the picker re-picks down to the kept quantity. | deviation | Trimming picked stock without a pick confirmation would leave inventory unaware of where the excess went. |
| Inventory (wave 16) issues the `packingslip.posted.v1` quantity, so the unreserved excess is taken from on-hand; reservations only cover the planned quantity. | handoff to inventory (wave 16) | Reservations are owned by wave 16. |
| The tolerance is looked up per product and customer of the shipment; the PRD's "product group" scope and a per-line override are not modelled. | deviation | Product groups belong to the product master (wave 8). |
| The wave detail pick form (`WaveDetailPage`) still refuses a quantity above plan before calling (FUL-0004); the service accepts it within the tolerance. Over-picking in a wave goes through the shipment detail pick form. | later slice of PSG-28 | The wave line payload carries no customer to resolve the tolerance with. |

## Important gaps (read first)

| Item | Status | Reason |
|---|---|---|
| Packing-slip post does **not** create inventory transactions. On-hand is not decremented. AC-28.1 (on-hand −10) and AC-28.2 (on-hand +10 on cancel) are not met. | handoff to inventory (wave 16) | Inventory issue is owned by wave 16. This slice emits `dev.passage.fulfillment.packingslip.posted.v1` with lines and quantities, and inventory must consume it. The slice does not write inventory tables. |
| Wave 33 AC2: packing-slip post of held quantity refused with `QUALITY_HOLD` | built | `PostPackingSlip` claims the packed-not-shipped quantity per product at inventory (`POST /v1/inventory/issue-claims`, duty `inventory.issue.claim`, kind SHIP, key per shipment version, product, quantity and attempt) before it posts. Inventory takes the claim under the product's on-hand row locks and refuses when it would reach held stock, counting the SHIP claims already taken, so repeated or concurrent posts cannot add up to held stock and a hold placed meanwhile is seen. Lines of one product are summed first. A refusal is 422 `QUALITY_HOLD`; an inventory error or outage is 503 `COM-0503`; a failed post withdraws its claims. Product ids that are not UUIDs cannot be checked and are refused. Without `INVENTORY_URL` the check is not wired. Tests: `TestPostPackingSlip_RefusesHeldQuantity`, `TestPostPackingSlip_RepeatedShipmentsCannotEatHeldStock`, `TestPostPackingSlip_SumsLinesOfOneProductBeforeChecking`, `TestPostPackingSlip_FailedDoubleSubmitKeepsTheCommittedClaim`, `TestHoldProperty_ShipNeverMovesHeldUntilUsageDecision`. Drop-ship confirmation still posts no inventory transaction (D4). Distinct from the short-pick reason of the same name. |
| The journal (Dr 1405 / Cr 1400) is stored on the packing slip and in the event. It is **not** posted to the universal journal. | handoff to ledger (wave 11); wiring `platform/go/ledgerclient` | The shared ledger posting client (`ledgerclient`, PR #411) is on main and used by production, EAM and assets; fulfillment is not wired to it yet (that needs `cmd/fulfillment/main.go` and the chart, which PR #415 is changing). The journal lines are ready to send with Idempotency-Key = packing_slip_id, and a drop-ship's with drop_ship_id. |
| Posting is a single local transaction, not a Temporal saga. There is no PENDING state, no compensation at step 3, and no 10-minute watchdog. | later slice of PSG-28 | The saga needs the Temporal worker, which is in the eventing-backbone scope. The store is written so that the ledger step can be added as a saga step. |
| The `short_pick_action` setting is applied when a packing slip posts and leaves a remainder open (slice 13), not at pick time as the PRD parameter table places it (FR-28.2), and a pick below plan does not close or move anything by itself. Sales (wave 27) does not yet consume `shipment.closed` to cancel the order line remainder, nor read its `backorder_shipment_id` (and `shipment.released`'s `backorder_of`) to keep the order lines open instead of counting them as released twice. | deviation; handoff to sales (wave 27) | A post needs every picked unit packed, so the remainder at post is exactly what the picks fell short of and the outcome matches a pick-time action; closing needs something shipped (FUL-0021 otherwise), and the line keeps its pick progress until then. The only difference is timing: the reservation of the remainder is released by `pick.confirmed` (`backordered`) at pick time either way. Slice 7 delivers the manual close, slice 13 the setting and the backorder shipment (see above). |
| A re-pick of a partially shipped shipment confirms the remainder in one call: the shipment goes to PICKED at once (its lines stay pick-confirmed), so a second confirm-pick before packing is refused (FUL-0001). Lines left out of the call keep their remainder for a later round. | later slice of PSG-28 | Per-line pick status for remainder rounds belongs with pick work per wave (wave 31 brings directed pick work). |
| Post, pick, pack, auto-pack, wave pick and release honour sales' order holds only after the consumer runs: until `service:fulfillment-order` is seeded and the consumer deployment is enabled, no hold is known and none is checked (slices 19 and 20). | handoff (authz seed and deploy, see slice 19 below) | The check itself is built: post, pick, pack and auto-pack are refused with FUL-0001 while the order has an active hold, a wave pick of the rest leaves a held order's shipment out, and release skips its lines as `held`.
| The cost on each line comes from the caller (`costs[]`), not from wave 20 cost snapshots. | handoff to inventory costing (wave 20) | Cost snapshot ownership is outside this wave. The caller supplies `costSnapshotId` and `unitCost`. |
| AC-28.4: a short pick leaves the remainder on the shipment (re-picked from `PARTIALLY_SHIPPED`), `pick.confirmed` carries the `backordered` quantity, and the short picks and backorders report shows the open remainder (slice 6). Releasing the unpicked reservation is not done here. | handoff to inventory reservations (wave 16) | Reservations are owned by wave 16; inventory consumes `pick.confirmed` (`backordered`), `shipment.cancelled` and `shipment.closed` (`qty` to unreserve, `picked_unpacked` to put back). No inventory consumer exists yet. |
| The packaging fee is statistical only. The `packaging_fee_billable` SURCHARGE / FREIGHT line with its own tax code (BR-28.12) is not created here. | handoff to billing (wave 29) | Billing owns invoice lines. The fee lines are on the slip and in `packingslip.posted.v1` (`packaging_fees`), so wave 29 can bill them when `packaging_fee_billable` is set. The setting itself is not modelled here. |
| Auto-pack takes unit weights per shipment line from the caller (`unitWeights`); a line without one is limited by quantity only. | handoff to product (wave 8) for unit weights | Product weights and packaging units belong to the product master (wave 8). |
| The ship-to country of the packaging fee comes from the caller (`countryCode` on the post), not from the order's ship-to address. | handoff to sales (wave 27) | The order ship-to is not read yet. Without a country no fee is posted. |
| Whether an order is a JOURNAL order is now taken from the stored `order.confirmed.v1` type (slice 19); a release or drop-ship for an order fulfillment has not heard of (the event not yet consumed, or the consumer not yet enabled) still takes the caller's `orderType`, and a JOURNAL order whose lines are then offered without it **is released and shipped**. | handoff to sales (wave 27) to keep sending `orderType`, `priority` and `atpQty` on its release call until the consumer is enabled everywhere; authz seed and deploy handoffs below | Sales must also read `shipment.released` line quantities as what was released (a partial release releases less than the order line and a later run the rest), not mark the line fully released. The per-line `atp_qty` of `order.confirmed.v1` (sales slice 22) is optional and not stored or used yet; release still takes `atpQty` from the caller.

Slice 12 adds nested containers, pallet to carton (FR-28.3; `container.parent_container_id` of section 5.28.4).
Migration 0013 gives `container` a `parent_container_id` (no new table, the
existing grants cover it). `POST /shipments/{id}:nest-containers` (`WarehousePack`,
`If-Match`, `Idempotency-Key`, body `containerType` and `containerIds`) puts
CLOSED cartons of a PICKED or PACKED shipment on a new pallet: the pallet is a
container of its own with a fresh SSCC, takes tare, returnable flag and material
from a known container type, weighs its tare plus its cartons and holds no lines,
so packed quantities do not change. A carton that is not CLOSED, is already on a
pallet or is a pallet itself is refused with FUL-0056 (one level only).
`POST /shipments/{id}/containers/{cid}:unnest` dissolves a CLOSED pallet; the
pallet row stays UNPACKED so its SSCC is never reissued. A carton on a pallet and
a pallet cannot be unpacked (FUL-0056, dissolve first), and a shipment holding a
pallet cannot cancel until it is dissolved and unpacked. Posting the slip ships the
pallet with its cartons, a returnable pallet raises the customer's balance and a
slip cancel reverts both. `container.nested` and `container.unnested` go through
the outbox, the ASN lists `parent_sscc` on each carton, and `Container` returns
`parentContainerId` and `childContainerIds`. Domain, API and store tests (Postgres,
including post, ASN, balance and slip cancel) and web tests cover it; the shipment
detail has the "Put cartons on a pallet" form, a Pallet column and "Dissolve pallet".

Slice 13 adds the `short_pick_action` warehouse setting and backorder shipments (FR-28.8).
Migration 0014 adds `warehouse_setting` (it carries `legal_entity_id`: a warehouse belongs to one
legal entity in org; tenant and restrictive legal-entity policies; granted SELECT, INSERT, UPDATE to
`passage_app`) and `shipment.backorder_of_id` (a shipment holds at most one backorder).
`GET /warehouse-settings` lists every warehouse the caller sees shipments or a saved setting for,
with its `legalEntityId` (REMAINDER_BACKORDER, the default, at version 0 when none is saved); `GET` and
`PUT /warehouse-settings/{warehouseId}` (`WarehouseView`, `WarehouseRelease`; `If-Match` with the
version read, 0 for none; `Idempotency-Key`; body `shortPickAction` and `legalEntityId`) read and
save it. A save under a legal entity other than the saved one or the one of the warehouse's
shipments, or over a setting outside the caller's legal-entity scope, is refused with FUL-0057.
`dev.passage.fulfillment.warehousesetting.changed.v1` (with `legal_entity_id` and
`previous_short_pick_action`) goes through the outbox. The codes are KEEP_OPEN,
CANCEL_REMAINDER and REMAINDER_BACKORDER (default since migration 0016, the PRD's; KEEP_OPEN is kept for the re-pick flow of
slice 6). `POST /shipments/{id}:backorder-remainder` (`WarehouseRelease`, `If-Match`,
`Idempotency-Key`, optional `plannedShipDate`) follows the rules of `:close-remainder` and moves the
closed quantity to a new RELEASED shipment of the same order, customer, legal entity, warehouse and
carrier (lines keep zone and tracking requirement); `Shipment` returns `backorderOfId` and
`backorderId`, `shipment.closed` carries `backorder_shipment_id`, and the new shipment publishes
`shipment.released` with `backorder_of`. When a packing slip posts and the shipment stays
PARTIALLY_SHIPPED, the warehouse's action runs in the same transaction: CANCEL_REMAINDER closes the
remainder, REMAINDER_BACKORDER moves it, KEEP_OPEN leaves it for a re-pick; a shipment that cannot
close is left alone, a setting saved for another legal entity than the shipment's is ignored, and
the post response names the action (`remainderAction`, `backorderShipmentId`), which the packing
slip page shows right after the post with a link to the backorder shipment. Domain, API and store
tests (Postgres, as the application role: default and versioned save, replay, tenant and
legal-entity isolation, the legal entity check, backorder lines, links and events, refusals, the
three actions at post and a full post that triggers nothing) and web tests cover it. The shipment
detail has a "Backorder remainder" button with a planned date and links between the two shipments,
and `/fulfillment/warehouse-settings` lists and saves the action with the legal entity. No new duty
is introduced (`WarehouseRelease`, `WarehouseView`), so there is nothing new for #313. The OpenAPI
and AsyncAPI have zero FAIL lines on the doc gates.

Slice 14 adds the posting settings per legal entity (section 5.28.13: `cogs_at_shipment`, `asn_enabled`).
Migration 0015 adds `legal_entity_setting` (tenant and restrictive legal-entity policies; granted
SELECT, INSERT, UPDATE to `passage_app`), the legal-entity sibling of slice 13's per-warehouse
`warehouse_setting` (same shape: one row per scope key, `version`, `updated_at`, default read at
version 0, `<entity>setting.changed` with the previous values); the other legal-entity parameters of
section 5.28.13 (`consolidation_enabled`, `gs1_company_prefix`, `packaging_fee_billable`, `rtp_*`,
`dni_check_time`) belong on this table when they are built. `GET /legal-entity-settings` lists every
legal entity the caller sees shipments or a saved setting for (both flags false at version 0 when none
is saved), and `GET` and `PUT /legal-entity-settings/{legalEntityId}` (`WarehouseView`,
`WarehouseRelease`; `If-Match` with the version read, 0 for none; `Idempotency-Key`; body
`cogsAtShipment` and `asnEnabled`, both required) read and save it. A save for a legal entity outside
the caller's scope is refused with FUL-0058. `dev.passage.fulfillment.legalentitysetting.changed.v1`
(with the previous values) goes through the outbox.

Slice 15 changes the `short_pick_action` default and adds drop-ship (FR-28.7, AC-28.5). Product decision
2026-11-01: a warehouse with no saved setting now takes the PRD's REMAINDER_BACKORDER, not KEEP_OPEN
(`domain.DefaultShortPickAction`; the store default of an unsaved warehouse; migration 0016 for the
column default, which rewrites no row, so a warehouse that saved a setting keeps it; KEEP_OPEN and
CANCEL_REMAINDER stay allowed). An unsaved warehouse takes the default for every shipment whatever
legal entity its other shipments belong to (review fix: it read the legal entity of one arbitrary
shipment and silently kept the remainder open for the others); a setting saved for one legal entity
still applies to that legal entity's shipments only. A post that leaves a remainder in a
warehouse nobody configured now moves it to a backorder shipment, and a warehouse that wants the
re-pick flow saves KEEP_OPEN. Store tests show an unsaved warehouse backordering, and the existing
re-pick tests save KEEP_OPEN explicitly. Drop-ship: migration 0017 adds `drop_ship` (tenant and
restrictive legal-entity policies; granted SELECT, INSERT, UPDATE to `passage_app`; one active
drop-ship per order line; a positive PO cost; gap-free `DS-` numbers per legal entity). `POST /drop-ships` (`WarehouseRelease`)
links an order line to its purchase order line at the PO cost (a line on a warehouse shipment is refused
with FUL-0062, and release skips a drop-shipped line as `drop_ship`); `POST /drop-ships/{id}:confirm`
(`PurchaseOrderConfirm`, `If-Match`, `Idempotency-Key`; body `shippedOn`, optional `vendorShipmentRef` and
`postingDate`) books Dr 5000 / Cr 2110 and stores the journal; `:cancel` (`WarehouseRelease`) cancels an
OPEN one; `GET` list (status, order, vendor) and read (`WarehouseView`). Events `dropship.created`,
`dropship.confirmed` (carries the journal) and `dropship.cancelled` go through the outbox. Error codes
FUL-0060 (not found), FUL-0061 (the order line already has an active drop-ship) and FUL-0062 (the
order line is on a warehouse shipment). Web: `/fulfillment/drop-ships` (list by status with the
create form for `WarehouseRelease`) and `/fulfillment/drop-ships/:id` (details, vendor confirmation
for `PurchaseOrderConfirm`, journal, cancel for `WarehouseRelease`). No new duty name is introduced;
the routes still need their `data.json` resource entries (handoff row below).

`cogs_at_shipment` is an accounting policy (BR-28.1: physical posting to 1405 or COGS at shipment), not
a per-slip choice. Every `:post-packing-slip` takes the policy of the shipment's legal entity inside the
post transaction (false, Dr 1405, when none is saved, which is what every post did before); a request
that names the other value is refused with FUL-0059 (a request naming the same value posts, so existing
callers that send `false` are unchanged). Changing the policy needs the ledger setup duty
`ledger.setup.write` (already seeded, held by finance) on top of `WarehouseRelease`, checked in the
store against the saved value (403 AUTH-0003); a save that only changes `asn_enabled` needs
`WarehouseRelease` alone. `asn_enabled` is the default of the ASN output: a post that omits `asnEnabled`
takes it, one that names it keeps its own value (an operational choice, not a posting one).

Store tests (Postgres, as the application role: default, versioned save, replay, event, tenant and
legal-entity isolation, the duty for a COGS change, the post taking the settings, the ASN override and
the FUL-0059 refusal), API tests and web tests cover it. The post form on the shipment detail shows the
COGS policy as text (it is not editable and not sent) and previews the journal with it, and presets the
ASN checkbox from the setting and sends the poster's choice; `/fulfillment/legal-entity-settings` lists
and saves the settings, with the COGS checkbox disabled for a user without `ledger.setup.write`. No new
duty is introduced (`WarehouseView`, `WarehouseRelease`, `ledger.setup.write` are all seeded), so there
is nothing new for #313. The OpenAPI and AsyncAPI have zero FAIL lines on the doc gates.

## Deliverables (PRD section 5.28 and ticket PSG-28)

| # | Deliverable | Status | Reason |
|---|---|---|---|
| D1 | Order release rules (horizon, ATP, hold, one shipment per order × warehouse × date) | built (domain + `POST /releases:run` + release board screen) | A line already on a shipment that is not cancelled is skipped as `already_released`, and runs per warehouse are serialised. Release priority ordering and partial release by ATP are built (slice 18); consolidation (BR-28.8, `fulfillment.consolidation`) is not built. The caller supplies the confirmed lines; reading them from sales (wave 27) is a later slice. |
| D1 | Wave and batch picking, wave creation, wave pick-list print | partial | Built: waves from the picking workbench (see D6), the waves list and detail, shipments moved to PICKING, `wave.created`, pick confirmation per wave (slice 9), picker assignment and reassignment, wave close and the printable pick list (slice 11). Not built: batch picking (one picker walking several orders' lines as one task), the pick list as a stored document (`WV-` PDF/ZPL, wave 5 docgen), bin-sequenced paths (the list orders by zone and product, because lines carry no bin location yet, wave 31), and automatic close of a wave when its last line is picked (close is a manual command). |
| D1 | Pick confirmation with batch/serial/location, `short_pick_action` | partial | Quantity confirmation is built; batch, serial and location capture per line with the FUL-0006 check at post is built (slice 10). The `short_pick_action` warehouse setting is built (slice 13). Not built: capture inside the pick call itself and per wave (capture is its own command after the pick), batch and serial existence or on-hand checks against inventory (wave 16), and expiry or batch-status rules (handoff to inventory, wave 16). |
| D1 | `trackingRequired` source | handoff to product (wave 8) and sales (wave 27) | The release caller supplies `trackingRequired` per line (the web release form has a checkbox for it); this service does not read the product's tracking dimension group, so a caller that omits it releases the line as untracked. |
| D1 | Serial freed by a customer return | handoff to sales returns (wave 30) and inventory (wave 16) | A serial that shipped on a posted slip stays unique for its product in `pick_tracking`, so the same unit cannot be picked again. A return receipt must free it (or inventory must own serial status); until then a returned and resold serial is refused with FUL-0036. |
| D1 | Inventory issue by batch and serial | handoff to inventory (wave 16) | `packingslip.posted` lines carry `product_id` and `tracking` (batch, serial, location, qty); `pick.tracked` carries the live set before shipping. Inventory must consume them. |
| D1 | Packing with containers, nested pallet → carton, SSCC with mod-10 | partial | SSCC generation, container creation (manual and auto-pack) and nesting of cartons on a pallet (slice 12) are built; a known container type gives the tare, returnable flag and material. Not built: deeper nesting than pallet to carton, adding a carton to an existing pallet (dissolve and nest again), auto-nesting by a pallet rule, a separate `:close` action (containers are created CLOSED), and container dimensions. |
| D1 | GS1-128 label rendering (PDF, ZPL) | partial | Built: `GET /shipments/{id}/containers/{cid}/label` renders ZPL with AI (00) and the SSCC after FNC1, after checking the check digit (FUL-0007), and the web shows it. Not built: the PDF label, the GTIN (01), batch (10), quantity (37), ship-to postal (420) and order reference fields of section 5.28.15, and label storage (`label_ref`); document rendering is wave 5 (docgen). |
| D2 | Packing slip posting: inventory deduction and COGS journal | partial | Journal (both BR-28.1 stocked rows), `PS-` numbering gap-free per legal entity, containers marked SHIPPED, and the event are built. Inventory deduction is a handoff (see above). Ledger post is a handoff. |
| D2 | Cancellation with mirror journal and reversal_of | built | A reversing slip carries negative quantities, costs and journal amounts; the shipment returns to PACKED and its containers to CLOSED; an INVOICED slip is refused with FUL-0010. Inventory mirror is a handoff. Billing (wave 29) sets INVOICED. |
| D3 | Carrier abstraction (rate, label, manifest, track) with EasyPost/Shippo/file adapters | partial (#482) | Built in `services/carrier-gateway-ts`: the adapter interface, the deterministic sandbox carrier, rate shopping, labels and tracking. Built here: `internal/carrier`, the client that signs each call with only its duty (`carrier.rate.quote`, `carrier.label.create`, `carrier.tracking.read`) for the shipment's legal entity. Also built: `POST /shipments/{id}:carrier-rates`, `:carrier-label` and `:carrier-track` (WarehouseShip; `CARRIER_GATEWAY_URL`, chart `carrierGatewayUrl`; unset answers 503 FUL-0070). The label is recorded as a LABELLED tracking event, `label:<tracking number>`. Not built: the EasyPost/Shippo/file adapters, manifest, and void. The shipment stores no ship-from or ship-to address or parcel dimensions, so the caller sends them on each call. See `services/carrier-gateway-ts/NOT-IMPLEMENTED.md`. |
| D3 | ASN output (`dev.passage.fulfillment.asn.issued.v1`) | partial | Built: the post publishes `asn.issued` with the slip, the carrier and the containers of that slip with their SSCCs and order-line quantities when the request sets `asnEnabled` or, if it omits it, the legal entity's `asn_enabled` setting is on (slice 14; a request naming `asnEnabled` overrides the setting). Not built: the `fulfillment.asn` feature flag, the tracking number (no carrier label yet), the SSCC hierarchy (no nesting). EDI 856/DESADV mapping is wave 35. |
| D4 | Drop-ship linked to PO, vendor confirm, COGS at PO cost (Dr 5000 / Cr 2110) | partial (slices 15, 16) | Built: `drop_ship` with its PO links, `POST /drop-ships`, list and read, `:confirm` (the vendor's ship confirmation, whole or in parts, books Dr 5000 / Cr 2110 at the PO cost, no inventory transaction), `:close-remainder` (slice 16) and `:cancel`, `dropship.created`, `.confirmed` (once per vendor shipment, with its journal), `.closed` and `.cancelled`, the web list, create form and detail with the confirmation. Built in review: an order line ships from stock or from the vendor, never both (FUL-0062 on create, `drop_ship` skip on release). Not built: reading the PO from purchasing (wave 17) so the cost and quantity come from it, the vendor-confirm webhook or EDI 856, a PS- packing slip for the confirmation (AC-28.5 says "a slip posts"), and the `dropship-30` fixture. See the slice 15 rows below. |
| D4 | Partial shipments and delivery schedule lines | partial | Built: `PARTIALLY_SHIPPED` until every line has shipped its planned quantity, re-pick of the remainder, several slips per shipment with per-slip containers and returnables. Built in slice 7: closing the remainder by hand (`:close-remainder`, see above). Built in slice 13: moving the remainder to a new backorder shipment, by hand or by the warehouse setting. Not built: schedule lines (a shipment line is one order line; FR-28.8 schedule lines need wave 27 VBEP-style lines), and the order-level status (sales, wave 27, reads `packingslip.posted`). |
| D5 | Tracking visible to the customer portal; `tracking-updated` event; tracking poll and webhook | partial | Built: `POST /shipments/{id}/tracking` (carrier-gateway after its HMAC check, or a manual entry, BR-28.9) stores one event per provider event key and publishes `tracking.updated`; `GET /shipments/{id}/tracking`; DELIVERED moves a SHIPPED shipment to DELIVERED; the tracking card on the shipment detail. `POST /shipments/{id}:carrier-track` pulls tracking from carrier-gateway on demand and records each new event (#482). Not built: the 30-minute scheduled poll and the `/webhooks/carriers/{provider}` HMAC webhook (handoff to carrier-gateway), the portal screen and the `Portal` duty on the GET (handoff to the customer portal app). |
| D6 | Picking workbench (grouped waves with preview, saved filters, `WAVE_MAX_LINES`) | partial | Built (AC-28.10): filters by customer, carrier, pick zone and planned ship date range; group by customer, carrier, zone or product; waves of at most `maxLines` (default 200) lines and `maxOrders` (default 50) orders; a shipment is never split and a wave never mixes legal entities (BR-28.14); a read-only preview; one-action create; a line belongs to at most one wave (database key); FUL-0016 in the create response; FUL-0053 when a shipment left RELEASED after the preview; the screens. Not built: product group and order priority filters (need wave 8 groups and the wave 27 priority), saved filters per user, the `workbench_group_key_default`, `wave_max_lines` and `wave_max_orders` warehouse settings (defaults are constants, the request may override), and the weight column of the preview. The zone and product of a shipment are those of its first line, because a shipment is not split. Only RELEASED shipments are waved: the remainder of a partially shipped shipment is re-picked on the shipment, not through a wave. |
| D6 | Packing instructions and auto-pack (`packing_rule`, BR-28.11) | partial | Built: container types, packing rules per product with an optional customer (the customer rule wins), `POST /shipments/{id}:auto-pack` filling containers most specific rule first and closing on quantity or max weight, FUL-0012 warnings for lines without a rule, FUL-0013 when one unit exceeds the max weight, manual repack, and the screens. Not built: rules per product group (needs wave 8 groups), nesting levels, the `auto_pack_enabled` warehouse setting, serial-tracked lines (the domain never splits a serial unit, but the store does not know which lines are serial-tracked until BR-28.10 tracking dimensions are built), and a one-click accept of a proposed plan (auto-pack packs directly; the result lists what it did). |
| D7 | Packaging materials, packaging fee per kg and country, statistical packaging line | partial | Built: packaging material weight per product unit, container types with a material, country fee rates valid from a date, the fee per material (product packaging × shipped units, plus the tare of non-returnable containers of a material) rounded half-even per material, FUL-0014 when a shipped material has no rate, the mirror lines on cancel, the packaging report per country and month (optionally per legal entity) and the screens. Not built: material attributes (type, recyclable flag, unit) beyond the code; the billable SURCHARGE (handoff above); the authoritative DE/FR/ES rate tables, LUCID/CITEO exports and the period statement document (handoff to the country packs, wave 48); the monthly accrual job. |
| D7 | Returnable transport packaging balances (`RTP_NEGATIVE`) | partial | Built: balance per (customer, container type) raised by returnable containers on post, lowered on cancel and by `POST /returnable-packaging:return`, refused below zero with FUL-0015, the balance list and the screen. Built in slice 6: the aging inquiry (`GET /returnable-packaging/aging`, days outstanding first in, first out, `chargeDue` after `chargeDays`) and its screen. Built in slice 22: the movement history, manual balance adjustments (`ReturnablePackagingMaintain`) and the `returned` and `adjusted` events. Not built: the nightly 03:00 aging job, the `rtp_charge_days` legal-entity setting (the caller passes `chargeDays`, default 90), the charge hook after `rtp_charge_days` (handoff to wave 30: it can read `chargeDue=true`), return receipts from wave 30 RMA lines (handoff to wave 30), and the `rtp_tracking_enabled` setting. |
| Deploy | Fulfillment workspace (tiles, release board, packing station, shipment detail) | partial | Built: workspace tiles (released, picking, packed not shipped, partially shipped, shipped), release board with carrier and pick zone, picking workbench, pick waves and wave detail, shipments list, shipment detail with pick and remainder pick, auto-pack, pack, post (with ship-to country and ASN), cancel, tracking and the ZPL label, packing slip journal and detail with packaging fees, delivered-not-invoiced report, SSCC register, short picks and backorders, scan page, container types, packing rules, packaging materials, packaging fees and report, returnable packaging and its aging. Not built: the carrier exceptions tile, a DNI balance tile on the workspace (the balance is per legal entity, so the workspace links to the report instead), pick list print, packing-station scan-to-pack, bulk release on multi-select, per-warehouse layouts. They need the carrier, DNI and wave endpoints. |
| Deploy | Mobile pick UI (basic PWA) in the demo warehouse | handoff to pwa-apps (owned elsewhere) | The PWA is outside this wave's owned paths. The GS1 parser (`apps/web/src/modules/fulfillment/gs1.ts`) can be reused. |
| Deploy | Demo warehouse seed data | handoff to deployment (`deploy/`) | `deploy/` is not owned by this wave. |
| Deploy | Feature flags `fulfillment.consolidation`, `fulfillment.asn`, `fulfillment.mobile_pick` | later slice of PSG-28 | No flag system wired in this slice. |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 | built (producer side) | Fixture `cogs-matrix-14` (`testdata/cogs-matrix-14.json`, expected journals from the independent reference `testdata/gen_cogs_matrix14.py`, run with `python3 -I`): seven BR-28.1 slip scenarios (stocked at a running-average and at a six-decimal FIFO snapshot, standard cost, drop-ship, non-stocked, catch-weight in KWD, a mixed JPY slip with zero-decimal rounding) under both posting policies. `TestCogsMatrix14_PostBalancedAndCancelMirrors` (14/14: exact journal, valid balanced ledger `PostEntryRequest` lines, exact mirror, every account nets to zero) and `TestPackingSlipLedgerContract_CogsMatrix` (Postgres: the 10 cells with stocked lines through post, cancel and re-post; the `packingslip.posted` and `.cancelled` journals and line costs against the reference). Not built: drop-ship and non-stocked lines at the store (the store posts stocked lines only), a run against the ledger service itself (the ledger client is a kernel handoff; the test encodes the ledger API's PostLine rules), the accountant review of the cells, and the wave 29 invoice column of BR-28.1. A slip with an empty journal (drop-ship, non-stocked or zero cost only) must not be sent to the ledger, which refuses an entry without lines. |
| AC2 | built (simulated warehouse) | `TestWarehouseSim1k_ZeroInventoryMismatch` (slice 6) and `TestWarehouseSim1k_SimulatedWarehouseOnHand`: 1,000 orders from another seed with 2 in 10 products batch tracked and 1 in 10 serial tracked (pickers scan oldest batch and lowest serial first), stock seeded at demand plus 0 to 20 units, one in ten shipments short-picked and re-picked, one in seven with its first slip cancelled and posted again. The floor ledger (what pickers took and got back) and an inventory projection rebuilt only from `packingslip.posted` and `.cancelled` agree on every warehouse, product, batch and serial, nothing goes negative during the replay and each warehouse and product ends at seeded stock minus demand: 0 mismatches over 7,755 stock keys. Not built: inventory's own consumer (wave 16) and the "within 60 minutes" timing on production hardware (the run takes about 2 s locally). The projection takes the warehouse from the order line, because `packingslip.posted` carries no warehouse, and reverses a cancellation from the issue of the slip it names, because reversal lines carry neither product nor tracking. |
| AC3 | built | Fixture `sscc-200` (`testdata/sscc-200.json` from the independent reference `testdata/gen_sscc200.py`): `TestSSCC200_CheckDigitAndLabel` (200/200: 104 valid, 96 invalid by substitution, transposition, length and foreign characters; 3 of 30 adjacent transpositions keep a valid mod-10 check digit and are recorded as valid; every valid SSCC prints a ZPL label whose Code 128 data is code set C + FNC1 + `00` + SSCC and scans back) and `TestGS1ElementStrings_Fixture` (95 AI combinations in bracketed and raw FNC1 form round-trip; 27 invalid element strings refused with the class of their GS1 rule). The parser now refuses an AI 17 date with month 00 or 13 or a day past the month's end, a batch or serial outside GS1 character set 82, and one AI repeated with two values. The web parser `gs1.ts` does not have these three checks yet. **Spec discrepancy:** the AC example `0614141` + serial `0123456789` gives a 19-digit string, which is not a valid SSCC-18 (with a 7-digit prefix the serial reference has 9 digits). The tests use the GS1 General Specifications example instead: extension 1, prefix 0614141, serial 123456789 give `106141411234567897`, which scans back from `(00)`. The PRD example needs correction. |
| AC4 | partial (carrier-gateway) | `services/carrier-gateway-ts/test/contract.ts` checks the adapter contract and replays it against `testdata/sandbox-recorded.json`. Fixtures recorded from real carriers wait for the reference adapters. |
| AC5 | built | This document lists each deliverable not built, with a reason. |
| AC6 | built | `go vet`, `go test`, gofmt, `helm lint`, `vitest` and `tsc` pass. The migration and the store tests were run against Postgres 16 as a NOBYPASSRLS application role. OpenAPI and AsyncAPI are checked against the routes and event types by tests. |

AC-28.11 (auto-pack 12, 12, 6 with valid SSCCs and gross weights), AC-28.12
(10.00 EUR statistical fee and 20 kg cardboard in the DE report) and AC-28.13
(balance 6 after 10 out and 4 back; a return of 7 refused) are covered by store
tests against Postgres (`internal/store/packing_test.go`), domain tests and web
tests. The rejection code is `FUL-0015` from the message catalog (section
5.28.18), not the `RTP_NEGATIVE` string in BR-28.13. AC-28.5 (drop-ship) is covered by the slice 15 store tests (Dr 5000 / Cr 2110 at PO cost, no
inventory transaction, no shipment or slip) and AC-28.6 (carrier
adapters) maps to the deliverables above and is not built yet; AC-28.7 (1,000-order
E2E) is built against a simulated warehouse (see AC2). AC-28.9 is met by the scan page parser (`module.test.tsx`); the
mobile pick PWA around it is a handoff (see Deploy).
AC-28.10 (60 lines for UPS across 12 customers, waves of at most 20 lines, no
shipment split, one action) is covered by domain, store and web tests
(`wave_test.go`, `store/workbench_test.go`, `waves.test.tsx`). AC-28.4 is
partial: 90 of 100 picked ships 90, the shipment stays PARTIALLY_SHIPPED with 10
to go and `pick.confirmed` carries `backordered` 10; the reservation release is
inventory's (handoff above). AC-28.8 is partial: the DNI report equals the cost of
the uninvoiced slips and Σ 1405 of their journals (store test, scaled from 50
slips to 5); the comparison with the ledger's GL 1405 is a handoff to ledger.

## Test plan items (section 5.28.9)

| Item | Status | Reason |
|---|---|---|
| Posting matrix, 14 cells, cancel mirrors | built | `cogs-matrix-14`, 14/14 in `domain`, 10 stocked cells through the store (see AC1). |
| Saga compensation (Temporal) | later slice of PSG-28 | No saga yet (see table above). |
| SSCC and GS1-128 property tests | built | `domain/sscc_test.go`, `domain/sscc200_test.go` (200/200, all AI combinations) and `web gs1.test.ts`. |
| Pick-pack-ship 1k E2E | built against a simulated warehouse | Against Postgres through the store with a floor ledger and an event-fed on-hand projection (see AC2). Inventory's own consumer is a handoff to wave 16. |
| Carrier adapters (recordings) | partial (carrier-gateway) | Only the sandbox recording exists. |
| DNI equals GL 1405 (daily job) | partial | Built: the DNI report and a store test that it equals Σ 1405 over the uninvoiced slip journals (AC-28.8 scaled to 5 slips, 2 invoiced, plus one `cogsAtShipment` slip). Not built: the 02:00 job and the comparison with the ledger's GL 1405 balance, which needs the ledger client (handoff to ledger, wave 11). |
| Fixtures `carrier-recorded`, `dropship-30` under `testdata/` | later slice of PSG-28 | Not built. `sscc-200` and `cogs-matrix-14` are built from independent Python references (`testdata/gen_*.py`, checked byte for byte by `TestSSCC200_FixtureMatchesReference` and `TestCogsMatrix14_FixtureMatchesReference`). `warehouse-sim-1k` is built: `testdata/warehouse-sim-1k/README.md` describes it; the data is generated from fixed seeds by `internal/store/sim_test.go` and, with the batch/serial mix, `internal/store/siminventory_test.go`. |

## Operations and API surface

| Item | Status | Reason |
|---|---|---|
| Endpoints `:rate`, `:label` (carrier label), `:manifest`, carrier webhook | partial (#482) | `:carrier-rates`, `:carrier-label` and `:carrier-track` are built. `:manifest` and the carrier webhook are not. |
| Pick confirmation per wave (`/waves/{id}:confirm-pick`) | built (slice 9) | Confirms all shipments of a wave in one command with optional `pickRest`; directed pick work per line and pick-by-scan remain wave 31. |
| `PackingSlipPost` cancel requires `If-Match` (convention) | deviation | A posted slip is immutable and has no version. Only Idempotency-Key is required. |
| Idempotency keys are scoped per tenant and key, not per principal | deviation | The stored fingerprint covers the method, path and body, so a key reused on another resource is refused, not replayed. The table matches `dbx.IdempotencyDDL`. Records are not expired after 24 hours yet (no cleanup job). |
| Duties on handlers (`x-passage-duty`) | built as route-level checks | OPA decision point is handled by the kernel `authz` package. Slice 2 uses `PackingRuleMaintain` (container types, packing rules, packaging materials), `PackagingFeeMaintain` (rates and report) and `WarehouseReceive` (return receipts); the config lists are `WarehouseView`. Slice 3 uses `WarehouseRelease` (workbench preview and create, shipment cancel), `WarehouseView` (waves, tracking list, container label) and `WarehouseShip` (recording a tracking event). Slice 4 uses `WarehouseView` for the DNI report (section 5.28.17: "GET shipments, tracking, DNI"); slices 5 and 6 use it for the SSCC register, the backorders report and returnable aging. The `CarrierAdmin` duty is not used yet (no carrier configuration endpoints); slice 22 uses `ReturnablePackagingMaintain` for balance corrections. Segregation of duties (WarehousePick with PackingSlipPost under `pick_post_separation`; PackingSlipPost with SalesOrderMaintain; ReturnablePackagingMaintain with WarehouseReceive; PackagingFeeMaintain with InvoiceMaintain) is not enforced. The web nav carries the duty per entry. |
| Returnable balances are kept per (customer, container type), not per (customer, packaging material) as FR-28.16 states | deviation | A returnable container type is the item the customer holds and returns (for example EUR-PALLET), and a return receipt names it. Two returnable types of one material keep separate balances. |
| Cancelling a slip writes the mirror packaging fee lines in the month of the reversal, not the month of the original | deviation | It matches the reversing journal, which is dated on the cancel day, and leaves a reported month unchanged once filed. Within one month the report nets to zero. |
| Cancelling a slip whose returnable containers the customer has already returned is refused with FUL-0015 | deviation | The balance would go negative (BR-28.13). Reverse the return first, or post a manual adjustment once adjustments exist. |
| Container types, packing rules, product packaging and fee rates are saved without a version (`If-Match`) | deviation | They are set-up data saved by key (code; product and customer; product and material; country, material and date): a save replaces the row, last writer wins, and the Idempotency-Key makes a retry safe. Delete endpoints are not built; a weight of 0 stops product packaging counting. |
| `/metrics`, `/v1/fulfillment/openapi.yaml`, `/error-codes` | later slice of PSG-28 | Kernel-standard endpoints, not wired in slice 1. |
| Health `/healthz`, `/readyz` | built | Kernel `httpx.NewRouter` default. |
| Rate-limit and `X-Passage-Api-Build` headers | handoff to kernel `httpx` | |
| Event type naming | deviation | Events use `dev.passage.fulfillment.packingslip.*` (the kernel accepts one segment per name part). The PRD uses `passage.fulfillment.packing-slip.*`, which is not a valid kernel type. |
| Config `COGS_AT_SHIPMENT` per legal entity | built (slice 14) | `cogs_at_shipment` is a legal entity setting every post follows; a post naming the other value is FUL-0059. Changing it needs `ledger.setup.write` on top of `WarehouseRelease`. The PRD names no duty for section 5.28.13 parameters; `ledger.setup.write` is the existing ledger posting setup duty. The process-wide `COGS_AT_SHIPMENT=false` of section 5.28.10 is the default of an unsaved legal entity and is not an environment variable. The open question of section 5.28.12 (default `true` for migrated SAP entities) is for the migration factory (wave 49) to set per legal entity through this API. |
| Route permissions for `/v1/fulfillment/legal-entity-settings` (GET under `WarehouseView`, `legal-entity-settings/*` GET under `WarehouseView` and PUT under `WarehouseRelease`) and slice 13's `/v1/fulfillment/warehouse-settings` (same pattern) in `policies/authz/data.json` | done (seeded in `policies/authz/data.json`) | `WarehouseView` carries GET `/v1/fulfillment/legal-entity-settings[/*]` and `/warehouse-settings[/*]`; `WarehouseRelease` carries PUT `/legal-entity-settings/*` and `/warehouse-settings/*` (and POST `/shipments/*:backorder-remainder`). |
| Billing (wave 29) reading the COGS timing of each slip | handoff to billing (wave 29) | A legal entity may change its policy while slips are uninvoiced. Billing must decide what to reverse at invoice from each slip's own journal (1405 or 5000 lines, in `packingslip.posted`), never from the current setting. |
| `GS1_COMPANY_PREFIX` per legal entity (BR-28.5) | deviation | Slice 1 has one prefix per process. It is not per legal entity. The serial counter is per tenant and prefix, gap-free, and a serial that no longer fits the prefix is refused. |
| SSCC no reuse for 12 months (`sscc_no_reuse_months`) | deviation | `UNIQUE (tenant_id, sscc)` means an SSCC is never issued twice in a tenant, which is stricter than 12 months. The tenant setting `sscc_no_reuse_months` (12 to 60) is not stored: this service has no tenant settings yet, so the register's `noReuseUntil` always uses the default 12. |
| SSCC register columns `printed_at` and "reused flag" (section 5.28.14) | deviation | Labels are rendered on request and not stored (`label_ref` is not built, see D1), so there is no print time; the register returns `issuedAt` instead. An SSCC cannot be reused (unique per tenant), so a reused flag would always be false; the register returns `noReuseUntil` instead. |
| SSCC sequence check (nightly 00:30, alert on duplicates or check-digit failures) | later slice of PSG-28 | No scheduler is wired in this service. The database refuses duplicates and the label endpoint checks the check digit (FUL-0007). |
| Period check (FUL-0002), negative on-hand (FUL-0003) on post (the tracking dims check, FUL-0006, is built in slice 10; the tolerance, FUL-0005, in slice 23) | later slice of PSG-28 | Period control is wave 14. Negative on-hand is wave 16. |
| Consolidation (BR-28.8, FUL-0011, `consolidation_enabled`, flag `fulfillment.consolidation`) | later slice of PSG-28 | A shipment holds one order (`order_id`), has no delivery address, and the order's `ship_separately` flag is not read from sales (wave 27). Consolidating needs multi-order shipments, the address on the shipment and the flag, so the FUL-0011 check is not built until release can use it. |
| Shipment cancel of a packed shipment | built (unpack first, slice 8) | `POST /shipments/{id}:cancel` (slice 3) cancels from OPEN to PACKED while nothing has shipped and the shipment holds no container. A shipment with containers cannot cancel (FUL-0021) until they are unpacked one by one (slice 8, `:unpack`). |
| DNI report (FR-28.12) | built | `GET /delivered-not-invoiced` (`WarehouseView`) and the web screen. It is computed from the slip journals on read, not from a stored projection. |
| DNI daily reconciliation job (02:00, `dni_check_time`), the "GL 1405 diff" column and the DNI diff alert | handoff to ledger (wave 11) and later slice of PSG-28 | The diff needs the ledger's GL 1405 balance per legal entity; the ledger client is a kernel item not yet built. No scheduler is wired in this service. |
| DNI report is per packing slip, not per slip line (section 5.28.14 lists "slip, line, qty, cost") | deviation | The 1405 balance is posted per slip; lines with quantities are on the packing slip detail the row opens. |
| An INVOICED slip leaves the DNI report for every as-of date | deviation | Billing (wave 29) sets INVOICED and relieves 1405; this service does not record the invoice date, so a past as-of date cannot tell whether the slip was still open then. Cancellations are dated (the reversal's posting date) and are honoured exactly. |
| DNI report returns at most `limit` slips (default 100, max 500) | deviation | `totals` and `lineCount` always cover every open slip; the web screen asks for 500 and says when the list is cut. Cursor paging is not built. |
| Release job every 15 min per warehouse | later slice of PSG-28 | Release is on-demand through the API only. |
| Saga watchdog, carrier retries (BR-28.9), manifest cutoff | later slice of PSG-28 | |
| Outbox events `shipment.released`, `pick.confirmed`, `packingslip.posted`, `packingslip.cancelled`, `shipment.cancelled`, `wave.created`, `asn.issued`, `tracking.updated` | built | Written in the same transaction as the state change. Consumers are not in this slice. `wave.created` uses the aggregate type `wave`. |
| Tracking event type name | deviation | The PRD names `passage.fulfillment.shipment.tracking-updated.v1`; the kernel allows one lowercase segment per part, so the type is `dev.passage.fulfillment.tracking.updated.v1`. |
| `POST /shipments/{id}/tracking` takes no Idempotency-Key | deviation | The provider event key in the body is the idempotency key (section 5.28.5: "provider event id"): a redelivery returns the stored event with `created: false` (200) and publishes nothing. |
| `POST /waves:preview` is a read-only POST without Idempotency-Key | deviation | The filter is a JSON body; nothing is written. `POST /waves` (create) requires the key. |
| Container `:close` endpoint and label reference | later slice of PSG-28 | See D1. |
| Migration from AX/SAP | later slice of PSG-28 | |
| Reporting: carrier exceptions and cost | later slice of PSG-28 | Needs carrier-gateway. Shipments list (with the PARTIALLY_SHIPPED filter), release board, pick waves, packing slip journal, delivered not invoiced, packaging weights per country and month, RTP balances and aging, the SSCC register and short picks and backorders are built. |
| Short picks report: the "reason" column (section 5.28.14) | built (slice 9) | Reason codes (OUT_OF_STOCK, DAMAGED, NOT_FOUND, QUALITY_HOLD, OTHER) on pick confirmation, single shipment and wave, in the API and the web forms, and in the report. The code list is fixed in the service (a check constraint); a tenant-maintained reason list is not built. |
| Wave screen: picker, reassign and print (section 5.28.6) | built (slice 11) | The wave detail assigns and reassigns the picker (`:assign`, version checked, `wave.assigned` carries the previous picker), closes the wave (`:close`, FUL-0055 while a line is left) and prints the pick list; the waves list shows status and picker and filters by both (`-` lists waves nobody has). |
| Picker is free text (a user name or badge up to 64 characters), not a reference to a user or worker | deviation | The user directory is outside this service. Nothing checks that the picker exists or holds `WarehousePick`; the "my waves" view is the picker filter. A picker has no capacity limit and a wave can be reassigned any number of times while OPEN. |
| The short pick action is per warehouse (scoped by the warehouse's legal entity), and a warehouse is listed once it has a shipment or a saved setting | deviation; handoff to org (wave 6) | There is no warehouse master in this service (warehouses live in org). The caller names the legal entity on save and the service checks it only against the saved setting, the warehouse's release rule (slice 17) and its shipments, so a setting saved for a warehouse with none of them yet trusts the caller; reading the warehouse's legal entity from org (or its `warehouse` events) would close that. The settings screen also accepts a warehouse id typed in. |
| The release rules are per warehouse (scoped by the warehouse's legal entity), and a warehouse is listed once it has a shipment, a short pick setting or a saved rule | deviation; handoff to org (wave 6) | As for the short pick action: there is no warehouse master here, so the caller names the legal entity on save and the service checks it against the warehouse's saved rule, short pick setting and shipments that the caller can see. A first save for a warehouse with none of them, or whose records are all in a legal entity outside the caller's scope, trusts the caller; reading the warehouse's legal entity from org would close that. |
| A release rule that is saved replaces the service configuration (`RELEASE_HORIZON_DAYS`, `RELEASE_REQUIRES_FULL_ATP`) for that warehouse | deviation | The configuration stays the default of a warehouse without a rule, so nothing changes until a rule is saved; a request's `horizonDays` or `requireFullAtp` still wins over both. |
| A backorder shipment is planned for the date given, else today (a post-triggered backorder keeps the original planned date unless it has passed), and its lines carry no pick progress. It is RELEASED at once, without a fresh ATP check, and can be waved like any released shipment | deviation; handoff to inventory (wave 16) | The remainder is released as a fresh shipment, so inventory sees `pick.confirmed` (`backordered`: release the short-picked reservation), `shipment.closed` (release only what is still reserved for the closed quantity, so the backordered part is not released twice) and `shipment.released` with `backorder_of` (reserve the backorder lines again, which may find no stock). Inventory must consume all three; no consumer exists yet. |
| `short_pick_action` has a third value, KEEP_OPEN, beside the PRD's two (section 5.28.13) | deviation | REMAINDER_BACKORDER is the default (product decision 2026-11-01, migration 0016, `domain.DefaultShortPickAction`); KEEP_OPEN stays allowed for the re-pick of the remainder on the same shipment that slices 6 and 7 built. |
| A confirmed drop-ship does not create a packing slip: it has its own `DS-` number and journal, the `PS-` sequence and the delivered-not-invoiced report are untouched | deviation; handoff to billing (wave 29) and the ledger (wave 11) | `packing_slip` is tied to a shipment of warehouse stock. The drop-ship carries its own journal (Dr 5000 / Cr 2110) and `dropship.confirmed`; the ledger posts it with Idempotency-Key = `drop_ship_id` once fulfillment is wired to `ledgerclient` (see the packing slip journal row), and billing invoices the order line from the event. AC-28.5's "a slip posts" is met by the confirmed drop-ship and its journal, not by a PS- slip. |
| Sales does not learn from a packing slip that a drop-ship line shipped | handoff to sales (wave 27) | No `packingslip.posted` is published for a drop-ship line. Sales must consume `dev.passage.fulfillment.dropship.confirmed.v1` (`order_line_id`, `qty`) to mark the order line shipped, and `dropship.created` / `.cancelled` if it shows the line as drop-shipped; no consumer exists yet. |
| The vendor confirmation is `POST /drop-ships/{id}:confirm`, not `/drop-ship/{orderLineId}:vendor-confirm` (section 5.28.5) | deviation | The drop-ship is a record with its own id, version and `DS-` number, created before the vendor ships; the command addresses it like every other fulfillment command (`If-Match` on its version). An order line has at most one active drop-ship, so `GET /drop-ships?orderId=` finds it. |
| A drop-ship create and a release run of the same order line at the same moment can both succeed | deviation | Create refuses a line on a shipment and release skips a line with a drop-ship, but the two are not serialised on the order line (release locks per warehouse, a drop-ship has none). The window is the length of one transaction; a per-line lock on every release line was not worth the lock-table cost. |
| The drop-ship routes in `policies/authz/data.json` | done (seeded in `policies/authz/data.json`) | `/v1/fulfillment/drop-ships` and `/v1/fulfillment/drop-ships/*` (GET) are under `WarehouseView`, `/v1/fulfillment/drop-ships` and `/v1/fulfillment/drop-ships/*:cancel` (POST) under `WarehouseRelease`, and `/v1/fulfillment/drop-ships/*:confirm` (POST) under `PurchaseOrderConfirm`. No new duty name. |
| The purchase order link, quantity and cost are given by the caller and not checked against purchasing | handoff to purchasing (wave 17) | The service stores `purchaseOrderId`, `purchaseOrderLineId`, `vendorId` and `unitCost` as given (UUIDs, a positive quantity and cost of at most 6 decimals, a 3-letter currency). Reading the PO line, checking the vendor and quantity, and taking the cost from it belong with wave 17; a PO receipt in GR/IR is for purchasing to clear against the 2110 credit. |
| A confirmed drop-ship cannot be reversed | deviation | A vendor return and the mirror posting of a booked confirmation are not built. Each vendor shipment is booked once; a vendor that ships in parts is built (slice 16), and a remainder the vendor will not ship is closed with `:close-remainder`. |
| Rounding of partial confirmations | built in review | A confirmation books the cost of everything confirmed so far rounded once, less what the earlier confirmations booked, so the parts always add up to the confirmed total rounded once and, when all of it ships, to the whole-line amount (3 x 0.125 in single units books 0.13, 0.12, 0.13 = 0.38, not 0.39). The confirmation form previews the same amount. A whole confirmation books exactly what slice 15 booked. |
| `dropship.confirmed.v1` is now published once per vendor shipment, not once per drop-ship | deviation (kept at v1) | Additive change: no service in the repository consumes any fulfillment drop-ship event yet (searched all services and apps), and a whole confirmation still sends one event with the slice 15 fields and values plus `confirmation_seq` 1 and `confirmed_qty` = `qty`, so no `.v2` was cut. Partial events happen only when a caller sends `qty`. The AsyncAPI tells consumers to act on `confirmed_qty` and the event's journal, never on `qty` (the ordered quantity), and to de-duplicate on `drop_ship_id` plus `confirmation_seq`; the ledger, sales and billing consumers must be written that way. |
| Sales (wave 27) does not yet read `dropship.confirmed` per shipment (`confirmed_qty`) or `dropship.closed` | handoff to sales (wave 27) | Sales must mark `confirmed_qty` of the order line shipped on each event and cancel the order line remainder on `dropship.closed` (`qty_closed`). |
| Confirming a drop-ship needs `PurchaseOrderConfirm`, not the `PurchasingConfirm` of section 5.28.5; creating and cancelling need `WarehouseRelease` | deviation | `PurchasingConfirm` is not a seeded duty; the purchasing service's duty for confirming purchase orders is `PurchaseOrderConfirm`, so the vendor confirmation stays a purchasing act under the seeded name (duty naming is #210). Creating and cancelling a drop-ship are order-to-warehouse routing, like release. |
| Cancelling a packing slip after the short pick action ran does not undo the action | deviation | The shipment returns to PACKED at the quantity that had shipped; the closed remainder stays closed and a backorder shipment stays (cancel it on its own if it should not ship). |
| A shipment re-picked in rounds under KEEP_OPEN (lines left out of a re-pick keep their remainder) is closed or backordered at its next post once the warehouse switches to CANCEL_REMAINDER or REMAINDER_BACKORDER | deviation | The action reads the setting at post, so lines left out on purpose are treated as short. |
| Closing a wave leaves its shipments alone | deviation | The wave is a pick-work container; shipments keep their own lifecycle (PICKED, PACKED). A short-picked line stays a backorder on its shipment and is re-picked through the shipment's own pick; a CLOSED wave refuses wave picks (FUL-0001), and a wave pick takes the wave row FOR SHARE so it cannot slip in beside a close. Nothing reopens a CLOSED wave. |
| Assign and close need `WarehouseRelease`, not `WarehousePick` | deviation | Section 5.28.6 lists "reassign" on the wave / pick list screen, whose duty is `WarehousePick`. Handing work to a picker and closing the wave are supervisor actions here, so a picker cannot reassign or close their own wave; picks stay on `WarehousePick`. Both duties were already enforced by this service and are already on the #313 seeding list, so nothing is new for #313. |
| Short picks report: "period" filters the line's first short pick | deviation | A line keeps the time of its first short pick, so a later short re-pick of the same line does not move it to another period. Lines short-picked before migration 0005 take their shipment's last change time. |
| Returnable aging is as of today (UTC) only | deviation | The balance is the current one, so aging it as of a past date would mix in containers shipped later. The nightly job (not built) would run it daily. |
| Returnable aging is computed on read and returns at most `limit` balances (default 100, max 500) | deviation | `count` and `chargeDueCount` cover every outstanding balance; the screen asks for 500 and says when the list is cut. Paging is not built. |
| Returnable aging for a caller scoped to some legal entities | deviation | Balances are per tenant (no legal entity), while the shipped containers are read through their slip and shipment under the legal-entity scope; a scoped caller ages a balance only from the shipments it can see, falling back to the oldest visible one. |
| Documents: packing slip PDF, delivery note, GS1 label, carrier label, manifest, BOL, packaging fee statement | later slice of PSG-28 | Document rendering is wave 5 (docgen). |

## Known risks

- The store's `PostPackingSlip` and `CancelPackingSlip` run inside one `dbx.WithTenantTx` transaction. This is correct for slice 1. A future saga must not add a second transaction that breaks the atomicity assumed here.
- Release de-duplication reads shipments through row-level security, so a line already released under a legal entity outside the caller's scope is not seen. Release runs are expected to use an all-entities principal (the scheduled job) until sales hands lines over by event.
- `GS1_COMPANY_PREFIX` defaults to empty in the chart, so the pod exits at start until a prefix is configured; this is deliberate (no made-up prefix).
- Migration 0003 links every SHIPPED container to its packing slip. It back-fills existing containers with the shipment's live posted slip, which is exact because before slice 3 a shipment had at most one live slip.
- The workbench reads RELEASED shipments through row-level security, so a user scoped to some legal entities plans waves only from theirs; a wave never mixes legal entities.
- A DELIVERED tracking event on a PARTIALLY_SHIPPED shipment is stored and published but does not change the shipment; the shipment becomes SHIPPED only when its remainder ships.
- Returnable aging reads, for every customer with a balance, all its shipped returnable containers grouped by posting date. That grows with history; if it gets slow, a stored movement ledger per (customer, container type) fed by post, cancel and return should replace it.
- The DNI report is computed on read from the slip journals (partial index `packing_slip_dni_idx` skips invoiced slips). Cancelled slips and their reversals stay in that index for good; if a legal entity's open plus cancelled slips grow past what the 5-minute budget of section 5.28.19 allows, a stored DNI projection fed by `packingslip.posted` / `cancelled` and the billing invoice event should replace it.
