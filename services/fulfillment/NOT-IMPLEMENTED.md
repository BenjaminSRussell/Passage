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

## Important gaps (read first)

| Item | Status | Reason |
|---|---|---|
| Packing-slip post does **not** create inventory transactions. On-hand is not decremented. AC-28.1 (on-hand −10) and AC-28.2 (on-hand +10 on cancel) are not met. | handoff to inventory (wave 16) | Inventory issue is owned by wave 16. This slice emits `dev.passage.fulfillment.packingslip.posted.v1` with lines and quantities, and inventory must consume it. The slice does not write inventory tables. |
| The journal (Dr 1405 / Cr 1400) is stored on the packing slip and in the event. It is **not** posted to the universal journal. | handoff to ledger (wave 11) and kernel `ledgerclient` | The ledger.Post client is a kernel item (`ledgerclient`) not yet built. The journal lines are ready to send with Idempotency-Key = packing_slip_id. |
| Posting is a single local transaction, not a Temporal saga. There is no PENDING state, no compensation at step 3, and no 10-minute watchdog. | later slice of PSG-28 | The saga needs the Temporal worker, which is in the eventing-backbone scope. The store is written so that the ledger step can be added as a saga step. |
| The close-remainder action is a manual command on the shipment. The `short_pick_action` warehouse setting (automatic close or backorder shipment at pick time) and moving the remainder to a new backorder shipment are not built; sales (wave 27) does not yet consume `shipment.closed` to cancel the order line remainder. | later slice of PSG-28 and handoff to sales (wave 27) | The warehouse setting needs a settings store this service does not have. Slice 7 delivers the close itself (see above). |
| A re-pick of a partially shipped shipment confirms the remainder in one call: the shipment goes to PICKED at once (its lines stay pick-confirmed), so a second confirm-pick before packing is refused (FUL-0001). Lines left out of the call keep their remainder for a later round. | later slice of PSG-28 | Per-line pick status for remainder rounds belongs with pick work per wave (wave 31 brings directed pick work). |
| Post does not check for an ACTIVE order hold (§5.28.5 validation, FUL-0001 "no active hold"). Holds are only honoured at release (`held` on the release line). | handoff to sales (wave 27) for the hold read; later slice of PSG-28 for the check | The order hold API of wave 27 is not consumed yet. |
| The cost on each line comes from the caller (`costs[]`), not from wave 20 cost snapshots. | handoff to inventory costing (wave 20) | Cost snapshot ownership is outside this wave. The caller supplies `costSnapshotId` and `unitCost`. |
| AC-28.4: a short pick leaves the remainder on the shipment (re-picked from `PARTIALLY_SHIPPED`), `pick.confirmed` carries the `backordered` quantity, and the short picks and backorders report shows the open remainder (slice 6). Releasing the unpicked reservation is not done here. | handoff to inventory reservations (wave 16) | Reservations are owned by wave 16; inventory consumes `pick.confirmed` (`backordered`), `shipment.cancelled` and `shipment.closed` (`qty` to unreserve, `picked_unpacked` to put back). No inventory consumer exists yet. |
| The packaging fee is statistical only. The `packaging_fee_billable` SURCHARGE / FREIGHT line with its own tax code (BR-28.12) is not created here. | handoff to billing (wave 29) | Billing owns invoice lines. The fee lines are on the slip and in `packingslip.posted.v1` (`packaging_fees`), so wave 29 can bill them when `packaging_fee_billable` is set. The setting itself is not modelled here. |
| Auto-pack takes unit weights per shipment line from the caller (`unitWeights`); a line without one is limited by quantity only. | handoff to product (wave 8) for unit weights | Product weights and packaging units belong to the product master (wave 8). |
| The ship-to country of the packaging fee comes from the caller (`countryCode` on the post), not from the order's ship-to address. | handoff to sales (wave 27) | The order ship-to is not read yet. Without a country no fee is posted. |

## Deliverables (PRD section 5.28 and ticket PSG-28)

| # | Deliverable | Status | Reason |
|---|---|---|---|
| D1 | Order release rules (horizon, ATP, hold, one shipment per order × warehouse × date) | built (domain + `POST /releases:run` + release board screen) | A line already on a shipment that is not cancelled is skipped as `already_released`, and runs per warehouse are serialised. Release priority ordering and consolidation (BR-28.8, `fulfillment.consolidation`) are not built. The caller supplies the confirmed lines; reading them from sales (wave 27) is a later slice. |
| D1 | Wave and batch picking, wave creation, wave pick-list print | partial | Built: waves from the picking workbench (see D6), the waves list and detail, shipments moved to PICKING, `wave.created`, pick confirmation per wave (slice 9), picker assignment and reassignment, wave close and the printable pick list (slice 11). Not built: batch picking (one picker walking several orders' lines as one task), the pick list as a stored document (`WV-` PDF/ZPL, wave 5 docgen), bin-sequenced paths (the list orders by zone and product, because lines carry no bin location yet, wave 31), and automatic close of a wave when its last line is picked (close is a manual command). |
| D1 | Pick confirmation with batch/serial/location, `short_pick_action` | partial | Quantity confirmation is built; batch, serial and location capture per line with the FUL-0006 check at post is built (slice 10). Not built: the `short_pick_action` setting, capture inside the pick call itself and per wave (capture is its own command after the pick), batch and serial existence or on-hand checks against inventory (wave 16), and expiry or batch-status rules (handoff to inventory, wave 16). |
| D1 | `trackingRequired` source | handoff to product (wave 8) and sales (wave 27) | The release caller supplies `trackingRequired` per line (the web release form has a checkbox for it); this service does not read the product's tracking dimension group, so a caller that omits it releases the line as untracked. |
| D1 | Serial freed by a customer return | handoff to sales returns (wave 30) and inventory (wave 16) | A serial that shipped on a posted slip stays unique for its product in `pick_tracking`, so the same unit cannot be picked again. A return receipt must free it (or inventory must own serial status); until then a returned and resold serial is refused with FUL-0036. |
| D1 | Inventory issue by batch and serial | handoff to inventory (wave 16) | `packingslip.posted` lines carry `product_id` and `tracking` (batch, serial, location, qty); `pick.tracked` carries the live set before shipping. Inventory must consume them. |
| D1 | Packing with containers, nested pallet → carton, SSCC with mod-10 | partial | SSCC generation and container creation (manual and auto-pack) are built; a known container type gives the tare, returnable flag and material. Nesting (`parent_container_id`) is not. Containers are created CLOSED in the pack call; a separate `:close` action is not exposed. Container dimensions are not recorded. |
| D1 | GS1-128 label rendering (PDF, ZPL) | partial | Built: `GET /shipments/{id}/containers/{cid}/label` renders ZPL with AI (00) and the SSCC after FNC1, after checking the check digit (FUL-0007), and the web shows it. Not built: the PDF label, the GTIN (01), batch (10), quantity (37), ship-to postal (420) and order reference fields of section 5.28.15, and label storage (`label_ref`); document rendering is wave 5 (docgen). |
| D2 | Packing slip posting: inventory deduction and COGS journal | partial | Journal (both BR-28.1 stocked rows), `PS-` numbering gap-free per legal entity, containers marked SHIPPED, and the event are built. Inventory deduction is a handoff (see above). Ledger post is a handoff. |
| D2 | Cancellation with mirror journal and reversal_of | built | A reversing slip carries negative quantities, costs and journal amounts; the shipment returns to PACKED and its containers to CLOSED; an INVOICED slip is refused with FUL-0010. Inventory mirror is a handoff. Billing (wave 29) sets INVOICED. |
| D3 | Carrier abstraction (rate, label, manifest, track) with EasyPost/Shippo/file adapters | handoff to `carrier-gateway` (TS service) and later slice of PSG-28 | carrier-gateway is a separate deployable outside `services/fulfillment/`. Its owner must build it. |
| D3 | ASN output (`dev.passage.fulfillment.asn.issued.v1`) | partial | Built: the post publishes `asn.issued` with the slip, the carrier and the containers of that slip with their SSCCs and order-line quantities when the request sets `asnEnabled`. Not built: the `asn_enabled` legal-entity setting and the `fulfillment.asn` flag (the caller decides per post), the tracking number (no carrier label yet), the SSCC hierarchy (no nesting). EDI 856/DESADV mapping is wave 35. |
| D4 | Drop-ship linked to PO, vendor confirm, COGS at PO cost (Dr 5000 / Cr 2110) | later slice of PSG-28 | Needs PO links (wave 17). The `drop_ship` event and vendor-confirm endpoint are not built. |
| D4 | Partial shipments and delivery schedule lines | partial | Built: `PARTIALLY_SHIPPED` until every line has shipped its planned quantity, re-pick of the remainder, several slips per shipment with per-slip containers and returnables. Built in slice 7: closing the remainder by hand (`:close-remainder`, see above). Not built: schedule lines (a shipment line is one order line; FR-28.8 schedule lines need wave 27 VBEP-style lines), moving the remainder to a new backorder shipment, and the order-level status (sales, wave 27, reads `packingslip.posted`). |
| D5 | Tracking visible to the customer portal; `tracking-updated` event; tracking poll and webhook | partial | Built: `POST /shipments/{id}/tracking` (carrier-gateway after its HMAC check, or a manual entry, BR-28.9) stores one event per provider event key and publishes `tracking.updated`; `GET /shipments/{id}/tracking`; DELIVERED moves a SHIPPED shipment to DELIVERED; the tracking card on the shipment detail. Not built: the 30-minute poll and the `/webhooks/carriers/{provider}` HMAC webhook (handoff to carrier-gateway), the portal screen and the `Portal` duty on the GET (handoff to the customer portal app). |
| D6 | Picking workbench (grouped waves with preview, saved filters, `WAVE_MAX_LINES`) | partial | Built (AC-28.10): filters by customer, carrier, pick zone and planned ship date range; group by customer, carrier, zone or product; waves of at most `maxLines` (default 200) lines and `maxOrders` (default 50) orders; a shipment is never split and a wave never mixes legal entities (BR-28.14); a read-only preview; one-action create; a line belongs to at most one wave (database key); FUL-0016 in the create response; FUL-0053 when a shipment left RELEASED after the preview; the screens. Not built: product group and order priority filters (need wave 8 groups and the wave 27 priority), saved filters per user, the `workbench_group_key_default`, `wave_max_lines` and `wave_max_orders` warehouse settings (defaults are constants, the request may override), and the weight column of the preview. The zone and product of a shipment are those of its first line, because a shipment is not split. Only RELEASED shipments are waved: the remainder of a partially shipped shipment is re-picked on the shipment, not through a wave. |
| D6 | Packing instructions and auto-pack (`packing_rule`, BR-28.11) | partial | Built: container types, packing rules per product with an optional customer (the customer rule wins), `POST /shipments/{id}:auto-pack` filling containers most specific rule first and closing on quantity or max weight, FUL-0012 warnings for lines without a rule, FUL-0013 when one unit exceeds the max weight, manual repack, and the screens. Not built: rules per product group (needs wave 8 groups), nesting levels, the `auto_pack_enabled` warehouse setting, serial-tracked lines (the domain never splits a serial unit, but the store does not know which lines are serial-tracked until BR-28.10 tracking dimensions are built), and a one-click accept of a proposed plan (auto-pack packs directly; the result lists what it did). |
| D7 | Packaging materials, packaging fee per kg and country, statistical packaging line | partial | Built: packaging material weight per product unit, container types with a material, country fee rates valid from a date, the fee per material (product packaging × shipped units, plus the tare of non-returnable containers of a material) rounded half-even per material, FUL-0014 when a shipped material has no rate, the mirror lines on cancel, the packaging report per country and month (optionally per legal entity) and the screens. Not built: material attributes (type, recyclable flag, unit) beyond the code; the billable SURCHARGE (handoff above); the authoritative DE/FR/ES rate tables, LUCID/CITEO exports and the period statement document (handoff to the country packs, wave 48); the monthly accrual job. |
| D7 | Returnable transport packaging balances (`RTP_NEGATIVE`) | partial | Built: balance per (customer, container type) raised by returnable containers on post, lowered on cancel and by `POST /returnable-packaging:return`, refused below zero with FUL-0015, the balance list and the screen. Built in slice 6: the aging inquiry (`GET /returnable-packaging/aging`, days outstanding first in, first out, `chargeDue` after `chargeDays`) and its screen. Not built: the nightly 03:00 aging job, the `rtp_charge_days` legal-entity setting (the caller passes `chargeDays`, default 90), the charge hook after `rtp_charge_days` (handoff to wave 30: it can read `chargeDue=true`), return receipts from wave 30 RMA lines (handoff to wave 30), manual balance adjustments (`ReturnablePackagingMaintain`), the `rtp_tracking_enabled` setting, and an event for return receipts. |
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
| AC4 | handoff | Carrier adapter contract tests and recorded fixtures belong to carrier-gateway. |
| AC5 | built | This document lists each deliverable not built, with a reason. |
| AC6 | built | `go vet`, `go test`, gofmt, `helm lint`, `vitest` and `tsc` pass. The migration and the store tests were run against Postgres 16 as a NOBYPASSRLS application role. OpenAPI and AsyncAPI are checked against the routes and event types by tests. |

AC-28.11 (auto-pack 12, 12, 6 with valid SSCCs and gross weights), AC-28.12
(10.00 EUR statistical fee and 20 kg cardboard in the DE report) and AC-28.13
(balance 6 after 10 out and 4 back; a return of 7 refused) are covered by store
tests against Postgres (`internal/store/packing_test.go`), domain tests and web
tests. The rejection code is `FUL-0015` from the message catalog (section
5.28.18), not the `RTP_NEGATIVE` string in BR-28.13. AC-28.5 (drop-ship) and AC-28.6 (carrier
adapters) map to deliverables above and are not built yet; AC-28.7 (1,000-order
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
| Carrier adapters (recordings) | handoff | carrier-gateway. |
| DNI equals GL 1405 (daily job) | partial | Built: the DNI report and a store test that it equals Σ 1405 over the uninvoiced slip journals (AC-28.8 scaled to 5 slips, 2 invoiced, plus one `cogsAtShipment` slip). Not built: the 02:00 job and the comparison with the ledger's GL 1405 balance, which needs the ledger client (handoff to ledger, wave 11). |
| Fixtures `carrier-recorded`, `dropship-30` under `testdata/` | later slice of PSG-28 | Not built. `sscc-200` and `cogs-matrix-14` are built from independent Python references (`testdata/gen_*.py`, checked byte for byte by `TestSSCC200_FixtureMatchesReference` and `TestCogsMatrix14_FixtureMatchesReference`). `warehouse-sim-1k` is built: `testdata/warehouse-sim-1k/README.md` describes it; the data is generated from fixed seeds by `internal/store/sim_test.go` and, with the batch/serial mix, `internal/store/siminventory_test.go`. |

## Operations and API surface

| Item | Status | Reason |
|---|---|---|
| Endpoints `:rate`, `:label` (carrier label), `:manifest`, drop-ship vendor-confirm, carrier webhook | later slice of PSG-28 and handoff (carrier-gateway) | Carrier work is not in this slice. Tracking GET and POST are built (slice 3). |
| Pick confirmation per wave (`/waves/{id}:confirm-pick`) | built (slice 9) | Confirms all shipments of a wave in one command with optional `pickRest`; directed pick work per line and pick-by-scan remain wave 31. |
| `PackingSlipPost` cancel requires `If-Match` (convention) | deviation | A posted slip is immutable and has no version. Only Idempotency-Key is required. |
| Idempotency keys are scoped per tenant and key, not per principal | deviation | The stored fingerprint covers the method, path and body, so a key reused on another resource is refused, not replayed. The table matches `dbx.IdempotencyDDL`. Records are not expired after 24 hours yet (no cleanup job). |
| Duties on handlers (`x-passage-duty`) | built as route-level checks | OPA decision point is handled by the kernel `authz` package. Slice 2 uses `PackingRuleMaintain` (container types, packing rules, packaging materials), `PackagingFeeMaintain` (rates and report) and `WarehouseReceive` (return receipts); the config lists are `WarehouseView`. Slice 3 uses `WarehouseRelease` (workbench preview and create, shipment cancel), `WarehouseView` (waves, tracking list, container label) and `WarehouseShip` (recording a tracking event). Slice 4 uses `WarehouseView` for the DNI report (section 5.28.17: "GET shipments, tracking, DNI"); slices 5 and 6 use it for the SSCC register, the backorders report and returnable aging. The `CarrierAdmin` and `ReturnablePackagingMaintain` duties are not used yet (no carrier configuration or balance-adjustment endpoints). Segregation of duties (WarehousePick with PackingSlipPost under `pick_post_separation`; PackingSlipPost with SalesOrderMaintain; ReturnablePackagingMaintain with WarehouseReceive; PackagingFeeMaintain with InvoiceMaintain) is not enforced. The web nav carries the duty per entry. |
| Returnable balances are kept per (customer, container type), not per (customer, packaging material) as FR-28.16 states | deviation | A returnable container type is the item the customer holds and returns (for example EUR-PALLET), and a return receipt names it. Two returnable types of one material keep separate balances. |
| Cancelling a slip writes the mirror packaging fee lines in the month of the reversal, not the month of the original | deviation | It matches the reversing journal, which is dated on the cancel day, and leaves a reported month unchanged once filed. Within one month the report nets to zero. |
| Cancelling a slip whose returnable containers the customer has already returned is refused with FUL-0015 | deviation | The balance would go negative (BR-28.13). Reverse the return first, or post a manual adjustment once adjustments exist. |
| Container types, packing rules, product packaging and fee rates are saved without a version (`If-Match`) | deviation | They are set-up data saved by key (code; product and customer; product and material; country, material and date): a save replaces the row, last writer wins, and the Idempotency-Key makes a retry safe. Delete endpoints are not built; a weight of 0 stops product packaging counting. |
| `/metrics`, `/v1/fulfillment/openapi.yaml`, `/error-codes` | later slice of PSG-28 | Kernel-standard endpoints, not wired in slice 1. |
| Health `/healthz`, `/readyz` | built | Kernel `httpx.NewRouter` default. |
| Rate-limit and `X-Passage-Api-Build` headers | handoff to kernel `httpx` | |
| Event type naming | deviation | Events use `dev.passage.fulfillment.packingslip.*` (the kernel accepts one segment per name part). The PRD uses `passage.fulfillment.packing-slip.*`, which is not a valid kernel type. |
| Config `COGS_AT_SHIPMENT` per legal entity | later slice of PSG-28 | Slice 1 takes `cogsAtShipment` per request. |
| `GS1_COMPANY_PREFIX` per legal entity (BR-28.5) | deviation | Slice 1 has one prefix per process. It is not per legal entity. The serial counter is per tenant and prefix, gap-free, and a serial that no longer fits the prefix is refused. |
| SSCC no reuse for 12 months (`sscc_no_reuse_months`) | deviation | `UNIQUE (tenant_id, sscc)` means an SSCC is never issued twice in a tenant, which is stricter than 12 months. The tenant setting `sscc_no_reuse_months` (12 to 60) is not stored: this service has no tenant settings yet, so the register's `noReuseUntil` always uses the default 12. |
| SSCC register columns `printed_at` and "reused flag" (section 5.28.14) | deviation | Labels are rendered on request and not stored (`label_ref` is not built, see D1), so there is no print time; the register returns `issuedAt` instead. An SSCC cannot be reused (unique per tenant), so a reused flag would always be false; the register returns `noReuseUntil` instead. |
| SSCC sequence check (nightly 00:30, alert on duplicates or check-digit failures) | later slice of PSG-28 | No scheduler is wired in this service. The database refuses duplicates and the label endpoint checks the check digit (FUL-0007). |
| Period check (FUL-0002), negative on-hand (FUL-0003), tolerance (FUL-0005) on post (the tracking dims check, FUL-0006, is built in slice 10) | later slice of PSG-28 | Period control is wave 14. Negative on-hand is wave 16. |
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
