# NOT-IMPLEMENTED — inventory (PSG-16, wave 16)

Slice 1 builds the immutable ledger (append-only by trigger), the dimension hash, the
status model, the on-hand projection, MOVEMENT / ADJUSTMENT / OPENING_BALANCE journals
with synchronous posting, running-average issue valuation, the negative-stock check,
mandatory site, serial uniqueness, the journal list and detail, the on-hand inquiry,
OpenAPI, AsyncAPI, chart, Dockerfile, and web screens (on-hand inquiry, journal list,
new journal, journal detail with add-line and post).

Slice 2 adds reservations (MANUAL, FIFO, FEFO, LIFO; create, list, release) with the
batch-expiry input FEFO reads, blocking and quarantine (block, list, release) as valued
status-transfer pairs, cycle counting (count plans, sheet generation, count entry, blind
count, tolerance approval, posting as a COUNT journal), the transaction inquiry, the
`reservation.*` and `blocking.*` events, and web screens for each (transactions,
reservations, new reservation, blocking, block stock, count plans, new count plan, count
sheets, count sheet).

Slice 3 adds transfer orders with an in-transit warehouse (create, ship, receive with
under/over-delivery and close-short, cancel), the stock-in-transit inquiry, periodic ABC
classification by on-hand value (run, list, report), the `transfer.shipped` /
`transfer.received` events, and web screens for each (transfer orders, new transfer order,
transfer order with ship / receive / cancel, stock in transit, ABC runs, run classification,
ABC report).

Slice 4 adds journal reversal (BR-16.12: `:reverse` creates a DRAFT journal of negated
lines linked to the lines they reverse; posting values each line at the negated value of
the transaction it corrects and sets `corrects_transaction_id`, so the GL entry mirrors
the original), tag counting (FR-16.18: generate a `TAG-` range per plan, enter or void
tags, transfer the summed used tags to a count sheet, open tags blocking unless a
supervisor marks them MISSING), the on-hand rebuild-and-compare with optional repair
(`POST /internal/v1/inventory/onhand:rebuild`, InventoryAdmin), and web screens (count
tags per plan, reverse on the journal detail).

Slice 5 adds the journal approval workflow (BR-16.11: `:submit`, `:approve`, `:reject`,
`:reopen`, `:cancel`, each guarded by `If-Match`) and the `:validate` dry run (the post run
in a transaction that is always rolled back), vendor consignment (FR-16.17, BR-16.14:
replenishment orders received as vendor-owned RECEIVED stock with no value, no financial
date and no GL entry; OWNERSHIP_CHANGE journals posting a DEDUCTED vendor-owned issue at
zero and a PURCHASED legal-entity receipt at the consignment price, Dr inventory / Cr
consignment accrual, with `ownership.changed`; INV-OWN-001 / INV-OWN-002; the consignment
stock report), the `segment` dimension with segment-aware reservations (FR-16.20,
BR-16.17, INV-SEG-001), owner and segment filters on the on-hand inquiry, and web screens
(workflow actions on the journal detail, ownership-change lines, consignment orders, new
consignment order, consignment order with receive, consignment stock, segment fields on
the reservation dialog).

Slice 6 adds the status model as commands (FR-16.2, BR-16.1): the service-to-service
`POST /internal/v1/inventory/transactions:apply` that registers a transaction for an
external origin (purchase, receipt, sales or production line) at an expected or physical
status and steps it forward (skips allowed, never backward, `fromStatus` guard, financial
step setting the financial date and posted cost), the named backward moves `:unpick`,
`:unregister` (AC-16.8) and `:cancel-reservation`, the append-only
`inventory_transaction_status_change` audit with idempotent replay, the transaction detail
and the status-change inquiry, `transaction.posted` / `onhand.changed` on every status
change, and web screens (transaction detail with status history and the backward moves,
status-change inquiry, transaction rows linked to their detail). It also grants the
application role the transfer-order and ABC tables that migrations 0004 / 0005 left
ungranted.

Slice 7 adds the inventory-to-ledger reconciliation (FR-16.12, AC-16.9): a run per legal
entity, inventory account, currency and day comparing the on-hand posted value with the
inventory account's net over the posted journals (or a ledger balance supplied by the
caller), flagging negative on-hand rows (FR-16.4), stored in `reconciliation_run` and
raising `reconciliation.diff` on a non-zero difference; the run list and run read; and web
screens (reconciliation runs, run reconciliation, run detail).

Slice 8 adds cycle-count control (FR-16.9): plan selection by location range (byte-wise, C
collation), ABC class (the class of the row's product x site in the latest ABC run) and a
sample percent drawn per sheet (a hash seeded with the sheet id, so successive sheets rotate
through the stock and each sheet's draw can be recomputed); the stock freeze (INV-CNT-002) that
refuses every movement of a row held by an unfinished sheet of a `freeze_stock` plan (journal
postings including other COUNT journals and count reversals, transfer ship / receive, blocking
and release, and status steps that change physical stock; only the sheet's own COUNT journal
passes); the COUNTED / APPROVED / CANCELLED sheet statuses with `:submit`, `:approve` and
`:cancel`; supervisor recount requests per line (INV-CNT-006) that reopen the sheet; the
four-eyes rule (INV-CNT-007, INV-CNT-008); blind sheets that stay blind to counters until
POSTED or CANCELLED; the `countsheet.changed` event (SUBMITTED, APPROVED, RECOUNT_REQUESTED,
CANCELLED, POSTED); and web screens (selection, freeze and four-eyes options on the plan, review
actions, confirmed cancel and recount on the sheet). It also gives every AsyncAPI message a title and
summary, so the OpenAPI and AsyncAPI doc gates report no FAIL lines.

Slice 9 adds the TRANSFER journal type (a move between two dimension sets of one legal entity, such
as bin to bin, warehouse to warehouse or segment to segment, without a document in transit): each line
names the source (`dimensions`), the destination (`toDimensions`, stored on the line with its own
dimension-set hash) and a positive quantity. Posting locks the rows in key order, values the issue at
the source row's running average, and gives the paired receipt the same value, so quantity and value
follow the stock, the inventory account does not move and the GL entry is empty (no offset account, no
posting-profile account). The destination needs a site and must differ from the source; batch, serial and the
product dimensions (config, size, color, style) cannot change; BLOCKED / QUARANTINE are moved by blockings (`INV-JNL-006`); a product x dimension row
cannot be both a source and a destination in one journal; the negative policy, count freeze, currency
and vendor-owned rules apply to both legs. The receipt leg is an origin of type `TRANSFER_IN`
(migration 0012; no new table, so no new grant). Web: the transfer journal type on the new-journal form,
destination fields on the add-line form and a destination column on the line list.

Slice 10 adds the cross-order transfer order line inquiry with a late overview (FR-16.15, PRD 3.16.14):
`GET /v1/inventory/transfer-order-lines` lists the lines of every order in the caller's legal entities with
order status, route, ship date, ordered, shipped, received, remaining and in-transit quantities and days in
transit, filtered by legal entity, status, product, shipping or receiving warehouse and a ship-date range.
It flags LATE_SHIPMENT (order still CREATED after its planned ship date) and LATE_RECEIPT (open shipment, not
closed short, in transit more than `lateReceiptDays`, default 7) as of `asOf`, with `daysLate` counted past the
due date; `late=true` returns only late lines. The list is capped at 1,000 lines and says when it was cut
(`truncated`). It is read only: no migration, table, grant, duty or event. Web: the transfer order lines page
with every filter, a late summary and a notice when the list is capped. The OpenAPI and AsyncAPI documents have
no FAIL lines on the documentation gates.
Slice 11 adds four read-only inquiries of PRD 3.16.14, all under `inventory.read`, none with a migration, table,
grant, duty or event: `GET /v1/inventory/reports/count-variance` (counted lines of count sheets filtered by
legal entity, plan, sheet, warehouse, counter and counted-day range; variance quantity is the posted variance
once the sheet is POSTED, otherwise counted minus expected; variance value is quantity times the cost of the
posted COUNT journal line, `valueBasis` JOURNAL, or times today's running average for a sheet not yet posted,
`valueBasis` ESTIMATE; CANCELLED sheets are left out; gain, loss and net value totalled per counter and currency),
`GET /v1/inventory/reports/negative-onhand` (rows with physical stock below zero, largest shortfall first, with
the first day of the current negative run, movements netted per day), `GET /v1/inventory/reports/tracking-trace`
(every transaction of a batch or serial, oldest first, FORWARD = issues, BACKWARD = receipts, BOTH with a running
physical balance per legal entity and product; `INV-TRC-001`, a service code outside the 3.16.18 catalog, when
neither is given; the trace follows one batch or serial and does not cross into the batches a production order
made from it, which needs the wave 21 consumption links) and `GET /v1/inventory/reports/dimension-combinations` (each
product x dimension set with first and last use and transaction count). Each is capped at 1,000 rows and says
when it was cut. Web: count variance (filters, estimate notice, totals per counter), negative on-hand, batch
and serial trace, and dimension combinations pages with their nav items. The OpenAPI and AsyncAPI documents have
no FAIL lines on the documentation gates.

Slice 12 adds journal names (PRD 3.16.13): table `inventory_journal_name` (migration 0013, RLS by tenant and
legal entity, granted to `passage_app` with SELECT, INSERT, UPDATE) and `inventory_journal.journal_name_id`.
`POST` / `GET /v1/inventory/journal-names`, `GET` / `PATCH /v1/inventory/journal-names/{id}` (create with
Idempotency-Key, change with If-Match; the code is upper-cased and unique per legal entity, `INV-JNLN-001`; the
code, legal entity and journal type are fixed). A journal is created under a name with `journalNameId`: the name
must be active and of the journal's legal entity and type (`INV-JNL-007`); a MOVEMENT journal takes the name's
default offset account when the request has none; a replay of the create answers even after the name was
deactivated, and concurrent creates under one key replay instead of failing on the unique key. The journal's
foreign key carries the legal entity, so the database refuses a journal pointing at another legal entity's name.
A name with `requiresApproval` makes `:post` refuse a DRAFT journal (`INV-JNL-008`) until it is submitted and
approved (BR-16.11). Events `journalname.created` and `journalname.updated` go through the outbox in
the write's transaction. Web: the journal names list (filters, require / drop approval and activate / deactivate
per row), the new journal name form, and a journal name choice with its approval notice on the new journal form,
all with loading, empty and error states. The OpenAPI and AsyncAPI documents have no FAIL lines on the
documentation gates.

Slice 13 adds the inventory management workspace summary (PRD Deploy line, wave 16): `GET
/v1/inventory/workspace/summary` (`inventory.read`, optional `legalEntityId`) counts draft journals and journals
awaiting approval (SUBMITTED), stock rows with negative physical quantity, open count sheets and counted sheets,
legal-entity / account / currency pairs whose latest reconciliation run is a DIFFERENCE with the time of the last
run (the PRD tile's "last run"; null before any run), active reservations and transfer orders in every status (zero
for a status without orders), all read under row-level security; a `legalEntityId` outside the caller's scope
counts nothing. No new table, grant, duty or event. The web workspace page (`/inventory/workspace`, first nav item)
shows the tiles with links to the pages that clear them, the last reconciliation run, a "nothing needs attention"
note when the attention tiles are all zero, and the loading and error-with-retry states. The overview's
orders-by-status counts (FR-16.15) are now served by this endpoint.

Slice 15 adds holds (wave 33 AC2; the base for the wave 24 catch-weight and wave 30 RMA holds): `POST /v1/inventory/holds`
places a hold with a reason (QUALITY, DAMAGE, RECALL, LEGAL, OTHER), a source (`sourceType` code, `sourceRef`) and a
quantity or the whole lot (`wholeLot`, the batch named, one hold per row of the lot); held quantity stays in physical stock
and is left out of `availPhysical`, `availOrdered`, every issue check, reservations and FEFO (`on_hand.held_physical`, migration
0015, `inventory_hold` with RESTRICTIVE legal-entity RLS). `allowPartial` holds what is available and answers the shortfall.
`POST /holds/{id}:release` (If-Match, optional quantity) and `POST /holds:release-source` (states the level to keep, so a retry
releases nothing more) end holds; `GET /holds` is the inquiry. `hold.placed` / `hold.released` and `onhand.changed`
(`HELD_PHYSICAL`) go through the outbox; the rebuild-and-compare counts active holds. The duty `inventory.hold.write` is for
people; the service-only `inventory.hold.apply` (granted to `quality`) reaches place and release-source only. The property
test `TestHoldProperty_AvailablePlusHeldIsOnHandAndNeverNegative` proves available + held + reserved = on-hand and no negative
availability under random place, whole-lot, release, release-by-source and issue sequences. Not built: a hold on a whole lot
does not follow later receipts into the lot, holds are not shown on the web on-hand page, and holds do not freeze count sheets.

PSG-1615 (wave 16 slice 15) adds partial shipments and a planned receipt date on transfer orders (FR-16.15, BR-16.13). `POST
/v1/inventory/transfer-orders/{id}:ship` takes an optional `lines` array (`lineId`, `qty`): a shipment moves only
the quantity named on each line, at most what is left to ship (`INV-TRF-007`) with at most 6 decimals (`INV-UOM-002`), each line once, an unknown line
`INV-TRF-004`; without `lines` it ships what is left on every open line, so existing callers behave as before.
A CREATED order ships first; a SHIPPED or PARTIALLY_RECEIVED order ships again while a line has quantity left,
and an order with nothing left is `INV-TRF-007`. Each shipment is its own valued SOLD / PURCHASED pair set and
its own `transfer.shipped` event (the event lines carry the quantity of that shipment and the line totals). The
shipped quantity accumulates on the line; a line now closes on receipt only once its whole ordered quantity has
shipped and been received (or closed short), and on a one-step transfer once fully shipped, so the order is
RECEIVED only when every line is. Closing a line short measures the shortfall against the ordered quantity (the
shipped quantity when larger), so a close-short on a partly shipped line gives up its unshipped remainder only
within the underdelivery tolerance (`INV-TRF-005` otherwise). The order's `shipDate` is its first (earliest)
shipment, so a later partial shipment does not restart the late-receipt clock of quantity already in transit;
each shipment's own date is on its transactions and its `transfer.shipped` event. `unshippedQty` is on every transfer line. `plannedReceiptDate` (migration
0016, one nullable column on `transfer_order`; no new table, so no new grant) is set on create (not before the
ship date, part of the idempotency body check), is returned on the order and on the line inquiry, and replaces
the `lateReceiptDays` allowance in the late-receipt test when present (late once the date has passed). No new
duty or event type. Web: the transfer order page's Ship card is a per-line quantity form (default all that is
left; less than that sends the lines and keeps the order open, with a validation of quantity above what is
left), offered again while quantity is left; a "Left to ship" column and the planned receipt date on the order;
a planned receipt date field on the new order form (checked against the ship date); a planned receipt column on
the transfer line inquiry; the Receive card lists only lines with shipped quantity and says when closing short
gives up quantity not shipped. The OpenAPI document has no FAIL lines on the documentation gates. Not built: the
late-shipment test for the unshipped remainder of a partly shipped order (an order is only late-shipped while
CREATED), cancelling or closing the unshipped remainder without a receipt, and the planned receipt date as a
legal-entity default.

PSG-1616 (wave 16 slice 16) adds the reservation time to live (FR-16.7) and the web screens for holds. A reservation request
takes `ttlHours` (0 to 8760; 0 or absent means no expiry, as the PRD's `reservation_ttl_hours` default 0, never "expire now"): every allocation then carries `expiresAt` (migration 0017, one nullable column on
`inventory_reservation` and a partial index; no new table, so no new grant). `POST /v1/inventory/reservations:expire`
(`inventory.reservation.write`, `limit` 1 to 500, default 200) releases the ACTIVE reservations whose `expiresAt` has passed,
oldest first, each in its own transaction by the same path as DELETE (the reserved bucket goes back to availability, the on-hand
projection event is written); the `reservation.released` event carries `reason` EXPIRED. It answers the number released, `more`
when others are still due, and the items; repeating it releases only what is still due. Each release re-checks status and
`expires_at` under the row lock with `SKIP LOCKED`, so two runs at once (or a run racing a DELETE) release a reservation once
(store test `TestConcurrentExpiryRunsReleaseEachReservationOnce`). The run is scoped by row-level security to the caller's legal
entities (`TestExpiryRunStaysInTheCallersLegalEntities`); a scheduler calls it with a tenant-wide principal. `ttlHours` is part
of the idempotent request: replaying a key with other hours is `idempotency_key_reuse`. Web: a time-to-live field on the new
reservation form, an Expires column and a Release expired button on the reservation list; the holds list (state and source type
filters, release of all or part of a hold at the version shown, If-Match), the place-hold form (quantity hold or whole lot,
reason, source, shortfall result, Idempotency-Key) and a Held column on the on-hand inquiry, each with loading, empty and error
states. No new duty or event type. The OpenAPI and AsyncAPI documents have no FAIL lines on the documentation gates. Not built:
a scheduler for the expiry run, extending a reservation's expiry, the consumers of fulfillment's `shipment.closed` /
`shipment.released` events (inventory would release or consume the shipment's reservations and register the DEDUCTED step
from them; they need the Kafka consumer wiring and the wave 24 event contract settled, handoff to waves 24 / 27), and the
hold items of slice 15 (a whole-lot hold following later receipts, holds freezing count sheets).

PSG-1617 (wave 16 slice 17) adds the blocking dispositions of FR-16.8. Migration 0018 adds three columns to
`inventory_blocking` (`expected_release_date`, `scrap_journal_id` with a composite foreign key to `inventory_journal` and a
check that SCRAP and the journal go together, `escalated_at` / `escalated_by`); no new table, so no new grant. `POST
/v1/inventory/blockings/{id}:release` with disposition SCRAP writes the stock off: in the release transaction it creates and
posts an ADJUSTMENT journal (`blocking-scrap:<id>`, one issue line at the blocked dimensions) through the same posting path as
any journal, so the issue is valued at the running average of the blocked row, the GL entry debits the adjustment account and
credits the inventory account by that value, the on-hand projection events are written, and the daily reconciliation sees the
GL entry. It needs the new duty `inventory.blocking.scrap` (checked in the handler, 403 `INV-BLK-008`) and
`INV_ADJUSTMENT_ACCOUNT` (422 `INV-JNL-005` without it). The scrap always posts under the forbid negative policy, whatever
`INV_NEGATIVE_DEFAULT` says, so it never writes off more than the blocked row still holds (422 `INV-NEG-001` when other
postings took the blocked stock); a failure rolls the whole release back, journal included (store test
`TestScrapBlockingLeavesNothingBehindWhenItFails`, which also shows another legal entity of the tenant gets 404), and a repeat
returns the released blocking and posts nothing more (`TestScrapBlockingWritesTheStockOff`). A scrap writes off the whole
blocking; there is no partial disposition (block the quantity to scrap as its own blocking). Inventory holds (#409) sit on
unrestricted stock and a blocking only takes available stock (held quantity is excluded), so a scrap never touches held stock
and neither places nor releases a hold. Valuation is the wave 16 running average of the blocked row, as for every issue in
this service; the wave 20 costing engine settles it under the item's costing method like any other issue. `POST /v1/inventory/blockings/{id}:block` (If-Match) moves an active quarantine to
BLOCKED as a valued pair (no GL effect), keeps the blocking ACTIVE at a higher version and emits
`dev.passage.inventory.blocking.escalated.v1` (`INV-BLK-007` when already blocked, `INV-BLK-006` when no longer active;
`TestEscalateQuarantineToBlocked`). A blocking request may carry `expectedReleaseDate` (not before the physical date;
information only, part of the idempotent request); `BlockingData` events carry it and `scrapJournalId` as nullable fields.
Web: the blocking list shows the expected release date and, per active blocking, Release, Move to blocked (quarantine only) and
Scrap (only for users holding `inventory.blocking.scrap`; confirmation dialog, then a link to the scrap journal); the block
form takes the expected release date; each with loading, empty and error states. The OpenAPI and AsyncAPI documents have no FAIL lines on the documentation gates. Not built:
RETURN (wave 18 vendor return), a configurable status transition matrix, a posting profile account for scrap separate from
the adjustment account (wave 11 posting profiles), and a link back from a reversal of the scrap journal: reversing it through
the journal reversal puts the stock back at the restricted status while the blocking stays RELEASED (SCRAP); block it again
to track it.

PSG-1618 (wave 16 slice 18) adds closing and cancelling vendor replenishment orders (FR-16.17). Migration 0019 adds
`closed_at`, `closed_by` and `close_reason` to `consignment_order`, widens the status check to CLOSED and CANCELLED and checks
that a terminal status and `closed_at` go together; no new table, so no new grant. `POST
/v1/inventory/consignment-orders/{id}:cancel` (`inventory.consignment.write`, If-Match, optional `reason` of at most 200
characters) ends a CREATED order: nothing was received, so nothing moves (`INV-CNS-004` when it has receipts).
`POST .../{id}:close` ends a PARTIALLY_RECEIVED order: the vendor-owned stock already received stays, the open quantity of each
line is given up and the order takes no more receipt (`INV-CNS-005` when nothing was received, `INV-CNS-003` for a RECEIVED,
CLOSED or CANCELLED order, which also refuses `:receive`). Both lock the order, check the version (412 `conflict_version`) and
write `dev.passage.inventory.consignment.closed.v1` (disposition CLOSED or CANCELLED, reason, per line ordered, received and
given-up quantity) through the outbox in the same transaction. The order row lock and the version make a receipt and a close
at the same version mutually exclusive: the loser gets 412, and a receipt reread after the close gets `INV-CNS-003`, so nothing is
booked against an ended order (`TestConcurrentReceiptAndCloseNeverBookAgainstAClosedOrder`). Like the other `If-Match`
transitions of the service there is no Idempotency-Key: a retried close at the old version is 412 and emits nothing, and the reread
shows CLOSED. Creating a replenishment order posts no ordered or expected-receipt quantity and no planning or ATP read uses its open
quantity, so ending it has no supply to release; `consignment.closed` is there for a future consumer. The order and its lines now carry `closedAt`, `closedBy`,
`closeReason` and `givenUpQty`; `openQty` is 0 on an ended order. Store tests: `TestCloseConsignmentKeepsReceivedStockAndGivesUpTheRest`
(stock stays, receipt and second close refused, event, another legal entity gets 404) and
`TestCancelConsignmentEndsAnUntouchedOrder`. The reason is also bounded in the table (1 to 200 characters or null). Web: the consignment order page shows a Close order or Cancel order card (reason,
confirmation dialog) to holders of `inventory.consignment.write`, hides the receipt form once the order has ended, and shows who
ended it and why; the list filters by Closed and Cancelled. No new duty. The OpenAPI and AsyncAPI documents have no FAIL lines on
the documentation gates. Policy bundle (#313): done, `inventory.consignment.write` in `policies/authz/data.json` carries
`POST /v1/inventory/consignment-orders/*:close` and `:cancel` next to `*:receive` (no new duty). Not built: reopening an ended order,
reversing a close, a planning (wave 22) consumer of `consignment.closed`, and closing by age.

Every other item in the ticket and PRD 3.16 is listed here.

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Transaction service: statuses per dimension (ordered/reserved/picked/deducted/received/posted) | built (slice 6) | `POST /internal/v1/inventory/transactions:apply` (`inventory.transaction.apply`, service account) registers a transaction for a `PURCH_LINE`, `RECEIPT_LINE`, `SALES_LINE` or `PROD_LINE` origin at any non-financial status of its direction (one per origin, `INV-STP-002`) and steps it forward (`INV-STP-001` backward; 412 on a stale `fromStatus`; SOLD / PURCHASED set the financial date and `cost_amount_posted`; vendor-owned never financial, `INV-OWN-001`). `:unpick`, `:unregister` and `:cancel-reservation` (`inventory.transaction.step`) are the only backward moves. Each change moves the on-hand buckets under the row lock with the negative policy, writes an `inventory_transaction_status_change` row and emits `transaction.posted` / `onhand.changed`. Store tests: `TestUnregisterReturnsAReceiptToArrived` (AC-16.8), `TestReceiptStepsForwardToPurchased`, `TestIssueUnpickAndSellAtRunningAverage`, `TestStatusStepGuards`, `TestStatusStepsAreTenantIsolated`; each ends with a clean rebuild-and-compare. |
| 1 | Splitting a transaction on a partial step (BR-16.2), "quantity ≤ remaining at previous status" | later slice of PSG-16 | A step moves the whole transaction quantity. Splitting needs a schema decision: the append-only trigger makes `qty` immutable, so the AX split (reduce the original row, insert the remainder with the same origin) cannot be done in place; it needs either a relaxed trigger for split-reductions recorded in the audit, or split rows linked to a closed parent. Callers register one transaction per document line until then. |
| 1 | GL effect of a financial step through `transactions:apply`, posting the physical value of physical statuses (AX physical posting) | handoff — waves 17 / 18 / 20 (purchasing, receiving, costing) and wave 11 (ledger) | The step sets `cost_amount_posted` and moves `posted_value`; the calling document posts its own ledger entry (PRD 3.17: receipt posting is one `transactions:apply` plus one ledger `Post`). Issues reaching SOLD are valued at the running average at the step; revaluation at closing is wave 20. |
| 1 | `transactions:apply` throughput guard (429 with `Retry-After` beyond 2,500 rps, PRD 3.16.19) | handoff — platform gateway rate limiting | Not enforced in the service. |
| 1 | Registration of service-owned origins (`COUNT_LINE`, `OPENING`) through `transactions:apply`; reservation records tied to RESERVED_PHYSICAL issue transactions | later slice of PSG-16 / handoff — waves 21 / 27 | Counts and opening balances post as journals. Reservation records (item 3) still move the `reserved_physical` bucket on their own; `:cancel-reservation` acts on an issue transaction registered at a reserved status, not on a reservation record (released with `DELETE /reservations/{id}`). |
| 1 | On-hand projection by site/warehouse/location/batch/serial | built | `GET /v1/inventory/onhand` groups by product, legal entity and any dimension (site, warehouse, location, batch, serial, inventory_status, config, size, color, style). |
| 1 | As-of-date on-hand (stock and value on a past date) | built (slice 14) | `GET /v1/inventory/onhand?asOf=YYYY-MM-DD` (inclusive) replays `inventory_transaction` up to the date with the same filters and groupings: each transaction counts at the status it held on the date. A transaction registered or moved through `transactions:apply` takes its status from `inventory_transaction_status_change` (the target of the last change dated on or before the date; none means registered later), so a pick later undone, a receipt registered after the date or an issue sold after it count as they stood then; a transaction without status history (journals, blockings, transfers, consignment) counts at its current status from its physical date. Answers physical quantity, posted value (transactions at a financial status with a financial date on or before the date), blocked quantity and the per-row negative flag, in the projection row's currency (tests `TestOnHandAsOfReplaysTheLedgerByDate`, `TestOnHandAsOfReplaysStatusHistory`, `TestOnHandAsOfReplaysBlockedAndNegative`, `TestOnHandAsOfIsTenantAndEntityScoped`). Limits: available physical / ordered and reserved quantities are not replayed (reservation records move reserved stock without a transaction and keep no per-date history), so `availPhysical`, `availOrdered` and `reservedPhysical` are omitted from a replay rather than reported as 0; ordered / on-order quantities are not returned by the inquiry at all (live or replay); value is the posted amount as it stands now, so a value restated by a later costing run (wave 20) shows restated; the replay reads the ledger on every call (no daily snapshot), which PRD 3.16.19 does not budget for. |
| 2 | Journals: TRANSFER type | built (slice 9) | `journalType` TRANSFER with `toDimensions` per line (see the slice 9 paragraph); tests `TestTransferJournalMovesQuantityAndValue`, `TestTransferJournalRules`, `TestTransferJournalValidateDoesNotPost`. A posted transfer cannot be reversed (post one back from the destination); a transfer between segments needs no demand-document check until waves 21 / 27. The inventory status may change between free values (AVAILABLE to another unrestricted value) because the status transition matrix is not built (see item 5); BLOCKED / QUARANTINE are refused on either side. Transfer journals do not know which warehouses are transit warehouses (wave 6 warehouse type TRANSIT), so a transfer journal can move stock out of or into a transit warehouse code without touching the transfer order's in-transit quantity; do not use transit warehouse codes on transfer journals. Store tests also cover a serial moving with its unit (`TestTransferJournalMovesASerial`) and line replay comparing the destination (`TestTransferJournalLineReplayComparesTheDestination`). |
| 2 | Journals: BOM type | handoff — wave 9 (BOM explosion) | Needs the BOM explosion. COUNT journals are built (slice 2) and are created only by count-sheet posting. |
| 2 | Journal approval workflow (SUBMIT / APPROVE / REJECT / REOPEN / CANCEL) and `:validate` | built (slice 5) | `POST /journals/{id}:submit` (journal write; re-runs the line rules, so an empty or invalid journal cannot be submitted), `:approve` / `:reject` (the approval task of InventoryJournalPost, `inventory.journal.post`), `:reopen` (REJECTED to DRAFT) and `:cancel` (DRAFT or REJECTED), each with `If-Match`; POSTING / POSTED are reached only by `:post`. A SUBMITTED or REJECTED journal takes no lines and cannot be posted. `:validate` runs the whole post (line rules, offset account, row locks, negative and vendor-owned checks, serial uniqueness, currency) in a transaction that is always rolled back and answers `valid` or the post's problem. Store test: `TestJournalApprovalWorkflowAndValidate`. |
| 2 | Approval routing per journal name: built (slice 12) for the approval requirement; workflow-template id, posting restriction, Temporal `PostInventoryJournal` owning POSTING | built (slice 12) / handoff — wave 4 (workflow templates) | A journal name with `requiresApproval` refuses `:post` from DRAFT (`INV-JNL-008`); the journal must be submitted and approved first. A journal without a name still posts from DRAFT or APPROVED. Reversal drafts and COUNT journals carry no name, so they post under their own rules. The name is not linked to a wave 4 workflow template, there is no per-name posting restriction duty, and segregation of the submitter and the approver belongs to the OPA policy bundle. |
| 2 | Posting calls the wave 11 `Post(JournalEntry)` ledger API | handoff — wave 11 (ledger) | Ledger Post does not exist yet. The balanced GL entry is built in-process, stored on the journal (`gl_entry`) and carried by `journal.posted`; `ledger_entry_id` stays null. |
| 2 | Posting profiles per legal entity / item group | handoff — wave 11 (posting profiles) | Slice 1 reads one inventory, adjustment and opening-clearing account from configuration (`INV_*_ACCOUNT`). |
| 2 | Period control for module INVENTORY (AC-16.7, INV-PER-001) | handoff — wave 14 (period control) | Period service not available; posting does not check the financial date against periods. |
| 2 | Reversal of a posted journal (BR-16.12) | built (slice 4) | `POST /journals/{id}:reverse` (journal write duty) creates the DRAFT reversal: same type, legal entity, offset account and currency, one negated line per original line (`reversesLineId`), no lines can be added. A journal has one reversal (a repeat returns it, 200 + `Idempotent-Replayed`). It is posted with the normal `:post` (post duty, `If-Match`, negative policy): each line takes the negated value of the transaction it corrects (not the running average), its transaction carries `corrects_transaction_id`, and `journal.posted` carries `reversesJournalId`. The original journal and its transactions are never changed. Store tests: `TestReversalMirrorsTheOriginalAtItsValues`, `TestReversalRespectsTheNegativePolicy`. Cancelling an unposted reversal draft is not built (it stays as the original's one reversal). |
| 2 | Journal number sequence `INVJ-` | handoff — wave 5 (numbering) | Journals are identified by id; the number column and sequence call arrive with the numbering client. |
| 2 | CSV import, paste from spreadsheet, copy journal | later slice of PSG-16 | Web grid features; the slice 1 screen adds one line at a time. |
| 3 | Reservations: MANUAL, FIFO, FEFO, LIFO against on-hand (RESERVED_PHYSICAL), over-reservation rejected, partial with shortfall, `POST/GET/DELETE /reservations` | built (slice 2) | Every on-hand row of the product in the legal entity is locked before allocation, so reservations never over-reserve (`TestConcurrentReservationsNeverOverReserve`). |
| 3 | Reservations against expected receipts (RESERVED_ORDERED) | later slice of PSG-16 | Needs the ordered / arrived buckets, which only receiving and purchase orders (waves 17–18) fill through `transactions:apply`. |
| 3 | Reservations as RESERVED_PHYSICAL issue transactions, CONSUMED status | handoff — waves 21 / 27 (production, sales) | A reservation is a record that moves the `reserved_physical` bucket; the projection's bucket equals the sum of ACTIVE reservations (reconciliation test). The demand documents that would own an issue transaction, and consume the reservation on issue, belong to the sales and production waves. |
| 3 | Segment-aware reservations (FR-16.20, BR-16.17) | built (slice 5) | The request names the demand's segment (`segmentType` SALES_ORDER / PROJECT, `segmentRef`, `segmentFallback`, default true). A policy reservation takes that segment's stock first, then unsegmented stock with fallback, never another segment; an unsegmented demand never takes segmented stock. A MANUAL reservation of stock whose segment the demand may not take is `INV-SEG-001`. The demand segment is stored (replay check of the key) and shown in the inquiry and the reservation events. Vendor-owned stock is never reserved (`INV-OWN-001` for MANUAL, skipped by the policies). Store test: `TestSegmentAwareReservations`. |
| 3 | Reservation TTL (`reservation_ttl_hours`, `expires_at`, 5-minute expiry job) | built (slice 16) for the request TTL and the expiry run; later slice of PSG-16 for the scheduler and the legal-entity default | A reservation request may carry `ttlHours` (0 to 8760); `expires_at` is set on every allocation and `POST /v1/inventory/reservations:expire` (`inventory.reservation.write`, optional `limit`) releases the due ones. No scheduler is wired (see the Temporal jobs row): the run is called by an operator, the "Release expired" button or a job, and is safe to repeat. `reservation_ttl_hours` as a legal-entity default is wave 6 / wave 8 parameter data (the request carries it). |
| 3 | `reservation_policy_default` per legal entity, `shelf_life_margin_days` per product tracking group | handoff — wave 6 / wave 8 parameters | The request carries the policy (default FIFO) and the margin (default 0). |
| 3 | Batch expiry master data | handoff — wave 8 (batches) | `PUT /batch-expiries/{productId}/{batchId}` keeps a local copy that FEFO reads; it is replaced by the wave 8 batch event feed. No screen: the batch master screens are wave 8's. |
| 3 | Blocking and quarantine: block a quantity, batch or serial with a reason, release with RELEASE | built (slice 2) | `POST /blockings`, `GET /blockings`, `POST /blockings/{id}:release`. A blocking is a valued SOLD / PURCHASED pair from the unrestricted dimension set to the same set with `inventory_status` BLOCKED or QUARANTINE (no GL effect: both legs are on the inventory account). |
| 3 | Disposition SCRAP, QUARANTINE → BLOCKED transition, expected release date | built (slice 17) | See the slice 17 paragraph. |
| 3 | Disposition RETURN (return to vendor), `inventory_status_transitions` matrix per legal entity | later slice of PSG-16 / handoff — wave 18 | RETURN needs the vendor return of wave 18 and answers `INV-BLK-005`; until then: release, then post an ADJUSTMENT. The only transitions are the built ones (AVAILABLE to BLOCKED or QUARANTINE, QUARANTINE to BLOCKED, any restricted status back to AVAILABLE, SCRAP); a configurable matrix is not built. |
| 3 | Blocking of reserved stock, blocking in another warehouse than the source | not planned | Reserved stock must be released first (INV-BLK-003 counts only available stock). |
| 4 | Cycle counting: count plan, `:generate`, count entry, tolerance approval (AC-16.6), blind count, `:post` as a COUNT journal | built (slice 2) | Variances are measured against the book quantity at posting (BR-16.8). Approval is the `inventory.count.approve` duty of the poster (403 `INV-CNT-001` without it). |
| 4 | Vendor-owned stock in cycle counts (SAP MIK1) | later slice of PSG-16 | Sheet generation leaves vendor-owned rows out: a count variance posts a valued COUNT journal, which vendor-owned stock never carries (BR-16.14). Counting it needs a quantity-only variance posting. Tags refuse the owner dimension for the same reason. |
| 4 | Count freeze (`INV-CNT-002`) for reservations; plan option "allow movement with recount" (BR-16.8 recount flag on movement) | later slice of PSG-16 | Slice 8 freezes every path that moves physical stock (journals, transfers, blocking, physical status steps) on a row held by an unfinished sheet of a `freeze_stock` plan. Reservations do not move physical stock and are not refused. A plan without `freeze_stock` lets movements through and the posting-time variance absorbs them (BR-16.8); it does not yet raise a recount flag on the lines a movement touched. |
| 4 | Plan selection by last-count age; plan frequency (cron) and the generation job | later slice of PSG-16 | Slice 8 selects by location range, ABC class (latest ABC run, matched per site) and a per-sheet sample percent. Last-count age and scheduled generation are not built; sheets are generated on demand. |
| 4 | Platform OPA policy for count separation of duties | partial — route split done in `policies/authz/data.json`; role-level SoD is platform authz (#313) | Slice 8 builds the statuses (OPEN, COUNTED, APPROVED, POSTED, CANCELLED), `:submit` / `:approve` / `:cancel`, per-line recount requests and the service-side four-eyes rule (the approver may not be a counter, INV-CNT-007). Done: `inventory.count.approve` is a route duty (no longer a flag) carrying POST `/v1/inventory/count-sheets/*:approve` and `/v1/inventory/count-sheets/*/lines/*:recount`; `policies/authz/authz_test.rego` checks that `inventory.count.write` reaches neither and `inventory.count.approve` cannot `:post`. Not done: a check that refuses a role granting both `inventory.count.write` and `inventory.count.approve`. |
| 4 | Count sheet number from the wave 5 `CNT-` sequence | handoff — wave 5 (numbering) | `CNT-` plus 8 random hex digits from the sheet id, unique per tenant. |
| 4 | PWA count screen with offline queue | later slice of PSG-16 | The web count sheet works online only. |
| 5 | Unit conversions on movements (UoM table lookup, product unit decimals) | partial — handoff to wave 6/8 UoM service | The API accepts `qty` in the inventory unit plus `enteredQty` / `enteredUnit` (same sign required), stores both, and rejects more than 6 decimals (`INV-UOM-002`). Conversion lookup and the product's own unit decimals need the wave 6/8 service. |
| 5 | Inventory status values and transitions | partial (slice 2) | BLOCKED and QUARANTINE are restricted: left out of available stock in the on-hand inquiry (`blocked` column), of reservations and of FEFO candidates. Other values remain free dimension values; the status transition matrix is not built. |
| 6 | Transfer orders, in-transit warehouse, ship / receive, under/over-delivery, close-short | built (slice 3) | `POST` / `GET /transfer-orders`, `GET /transfer-orders/{id}`, `:ship`, `:receive`, `:cancel`. Two-step through a transit warehouse (`useTransit`, default) or one-step to the destination; optional receiving site (`toSiteId`). Each movement is a valued SOLD / PURCHASED pair (origin `TRANSFER_LINE`, reference = order id): the receipt carries the issue's running-average cost (BR-16.3), both legs are on the inventory account, so there is no GL entry (AC-16.11). Rows are locked in ascending order across all lines (BR-16.4); the shipping warehouse follows the negative policy, the transit warehouse never goes negative. Close-short within `underdeliveryPct` leaves the shortfall in transit (flagged in the inquiry); receipts beyond `overdeliveryPct` are `INV-TRF-002`; an allowed over-delivery is issued from the shipping warehouse. Cancel only before shipment (`INV-TRF-003`). Store tests: `TestTransferThroughTransitWithShortClose` (AC-16.11), `TestOneStepTransferAndOverDelivery`, `TestTransferShipRespectsAvailability`, `TestTransferIdempotencyCancelAndScope`. |
| 6 | Transfer legs at DEDUCTED / RECEIVED (physical) status | by design until the status command exists | PRD FR-16.15 names a DEDUCTED issue and a RECEIVED receipt. Without `transactions:apply` and a financial update, physical-only legs would leave the value at the shipping warehouse (its running average and posted value wrong, the receiving warehouse valued at zero). Legs post at SOLD / PURCHASED like AX's financially updated transfer shipment; the cost-equality and no-GL rules hold. Revisit with wave 20 costing. |
| 6 | Transfer order DRAFT status, header `delivery_terms` / `mode_of_delivery`, adding lines after creation, delete of CREATED orders | later slice of PSG-16 | Orders are created CREATED; `ship_date` holds the planned date until shipped, then the latest actual one. Partial shipments and the planned `receipt_date` are built (slice 15); `receipt_date` (actual) is set when the order is fully received. |
| 6 | ON_ORDER / ORDERED supply and demand pair at creation, and its cancellation | later slice of PSG-16 | The status commands exist (slice 6), but transfer orders do not register an ON_ORDER / ORDERED pair at creation yet; planning (wave 22) reads it. Creation posts nothing yet. |
| 6 | Transit warehouse from warehouse master data (`transit_warehouse_id` per shipping warehouse, type TRANSIT), legal-entity `transfer_use_transit`, product tolerance defaults | handoff — wave 6 / wave 8 master data | The request carries `useTransit`, the transit warehouse and the per-line tolerances (default 0). |
| 6 | "The transit dimension never carries reservations" (BR-16.13) | handoff — wave 6 (warehouse type TRANSIT) | Policy reservations do not know which warehouses are transit warehouses until the warehouse type is readable; do not reserve from transit warehouse codes. |
| 6 | `transfer.release.v1` picking release event, batch ship / receive job, transfer number from the wave 5 sequence, shipping (`TRS-`) and receiving (`TRR-`) documents, four-eyes (`transfer_four_eyes`) | later slice of PSG-16 / handoff — waves 5, 15, 31 | Numbers are `TO-` plus 12 hex digits of the order id. Documents follow the wave 15 templates; segregation of duties belongs to the OPA bundle. |
| 6 | Cross-company transfers (pricing, intercompany invoice) | handoff — wave 40 | One legal entity per order. |
| 6 | In-transit stock inquiry (MB5T) | built (slice 3) | `GET /transfers/in-transit`: shipped, not yet received quantity by order line, oldest shipment first, days in transit, lines closed short flagged (`closedShort`). Value at from-cost is not shown (the transit on-hand row carries it in the on-hand inquiry). The printed list is a wave 15 document. |
| 7 | Ownership dimension, vendor consignment replenishment order, OWNERSHIP_CHANGE journal, consignment stock report | built (slice 5) | `POST` / `GET /consignment-orders`, `GET /consignment-orders/{id}`, `:receive` (`If-Match`), `GET /consignment/stock`. A replenishment order (vendor, site, warehouse, requested date, currency; lines with product, quantity, consignment price and location / batch / serial) is received as RECEIVED transactions (origin `CONSIGNMENT_LINE`) at its dimensions with `owner = vendor`: value 0, no financial date, no GL entry, no GR/IR; over-receipt `INV-CNS-002`. An OWNERSHIP_CHANGE journal (also needs `inventory.consignment.write` to create; one vendor per journal; lines name the vendor-owned source, a positive quantity and the consignment price) posts per line a DEDUCTED issue of the vendor-owned stock at 0 and a PURCHASED receipt (origin `OWNERSHIP_CHANGE`) at the same dimensions without owner at quantity x price; the GL entry is Dr inventory / Cr consignment accrual (`INV_CONSIGNMENT_ACCRUAL_ACCOUNT`; unset is `INV-JNL-005`), and `ownership.changed` carries the lines and amounts. More than the available vendor-owned quantity is `INV-OWN-002` whatever the negative policy. Any other document that names an owner is `INV-OWN-001` (journals, transfers, reservations; blockings and tags refuse the dimension). The stock report lists vendor-owned quantity by vendor and dimension set with the first receipt, its age and a statistical value at the vendor's latest consignment price. Store tests: `TestConsignmentReceiptAndOwnershipChange` (AC-16.12), `TestVendorOwnedStockStaysOutOfValuedOperations`, `TestConsignmentIdempotencyReceiptsAndScope`. |
| 7 | Consignment price from the wave 17 trade agreement (`consignment=true`), accrual account from the posting profile per legal entity | handoff — wave 17 (trade agreements), wave 11 (posting profiles) | The ownership-change line carries the price (the replenishment price is shown on the order and the stock report); one accrual account comes from configuration. |
| 7 | `auto_ownership_change_on_issue`, stock determination (`OWN_FIRST` / `CONSIGNMENT_FIRST`), the ownership-change batch (consumption- and age-driven, `max_consignment_days`) | later slice of PSG-16 / handoff — wave 8 (item model groups) | Needs the item model group and warehouse parameters and a scheduler (see the Temporal jobs row). Issues of vendor-owned stock are refused until an ownership change is posted. |
| 7 | Cancelling or closing a replenishment order | built (slice 18) | See the slice 18 paragraph. |
| 7 | Reversing an ownership change (return to the vendor), the `CRO-` number from the wave 5 sequence, the printed replenishment order, age buckets and the inventory value report exclusion | later slice of PSG-16 / handoff — waves 5, 15, 18 | A posted ownership change cannot be reversed (`INV-JNL-003`); returns to the vendor need the wave 18 vendor return. Numbers are `CRO-` plus 12 hex digits of the order id. The stock report gives the age in days rather than buckets; vendor-owned rows carry no value, so they add nothing to value totals. |
| 7 | `owner_dimension_active` per tracking dimension group | handoff — wave 8 (dimension groups) | The owner dimension is accepted for every product. |
| 7 | Customer consignment warehouse (`CUSTOMER_CONSIGNMENT`) | later slice of PSG-16 (documents in wave 27) | Warehouse type belongs to wave 6 master data. |
| 8 | Tag counting (`CountTag`, TAG- sequence, transfer to COUNT) | built (slice 4) | `POST /count-plans/{id}:generate-tags` (1 to 1000 tags, Idempotency-Key), `GET /count-plans/{id}/tags` (status filter: the missing / void list before transfer), `POST /count-tags/{id}:use` and `:void` (`If-Match`), `POST /count-plans/{id}:transfer-tags`. A tag is counted once (`INV-TAG-001`); zero is a count; the plan's warehouse is filled in and another warehouse refused. The transfer sums the untransferred USED tags per product x dimension set into a new OPEN count sheet (expected = book now), which is posted like any count sheet (COUNT journal, tolerance approval). Open tags block it (`INV-TAG-002`) unless `markOpenMissing`, which needs `inventory.count.approve` (`INV-TAG-006`). Store test: `TestTagCountingTransfersSummedTagsToACountSheet` (AC-16.13 at reduced scale). |
| 8 | Tag numbers from the wave 5 `TAG-` sequence, tag ranges per warehouse without a plan, a TAG_COUNT journal type, printed tag strips with barcodes (Code 128 / ZPL) | handoff — wave 5 (numbering), wave 15 (documents) | Numbers are `TAG-` plus six digits from a local per-legal-entity counter allocated under an advisory lock (unique per legal entity). A range always belongs to a count plan (the plan may name one warehouse). The tag entries themselves stand in for the TAG_COUNT journal; their transfer produces the COUNT sheet and journal. |
| 8 | ABC classification by VALUE (run, report) | built (slice 3) | `POST /abc-runs`, `GET /abc-runs`, `GET /abc-runs/{id}`: product x site ranked by posted on-hand value, thresholds 80 / 15 / 5 by default (must sum to 100, `INV-ABC-001`), an item whose cumulative share before it is at or below a boundary takes the higher class (BR-16.16), zero-value items in class C or N. Runs are immutable and replayable by Idempotency-Key. A product x site valued in two currencies is refused (`INV-CUR-001`). |
| 8 | ABC criteria REVENUE, MARGIN, CARRYING_COST and the lookback; the monthly job; the class stored on the product; the `abc_class[]` count-plan filter; `abc_*` legal-entity parameters | later slice of PSG-16 / handoff — waves 8, 20, 27 | Revenue and margin need sales (wave 27) and costing (wave 20); the product field belongs to wave 8. No scheduler is wired (see the Temporal jobs row). The run takes its thresholds and zero class in the request. |
| 8 | Segment dimension (SALES_ORDER / PROJECT) and segment-aware reservation | built (slice 5) | `segment` is the last dimension name (existing hashes are unchanged: absent dimensions do not contribute to the canonical form; `TestSegmentDoesNotChangeExistingHashes`). Its value is `SALES_ORDER:<ref>` or `PROJECT:<ref>` (one value rather than separate `segment_type` / `segment_ref_id` columns), checked wherever dimensions are normalized. Journals receive and issue segmented stock; reservations are segment-aware (item 3); the on-hand inquiry filters and groups by segment (the sales-order / project stock inquiry). |
| 8 | `segment_valuated` (non-valuated special stock), `segment_dimension_active`, the legal-entity `segment_fallback` default, INV-SEG-001 on issues | handoff — wave 8 (item model / tracking groups), waves 21 / 27 (demand documents) | Segmented stock is valued like any stock. The request carries the fallback (default true). Issues are journal lines that name their dimension set; the segment check on issues needs the demand documents. The transfer between segments (BR-16.17) is a TRANSFER journal line whose `toDimensions` change the segment (slice 9, item 2). |

## PRD items (section 3.16) not built in slice 1

| Item | Status | Reason |
|---|---|---|
| FR-16.1 status-change audit rows | built (slice 6) | `inventory_transaction_status_change` (RLS by tenant and legal entity, append-only trigger) holds one row per registration and status move; the trigger on `inventory_transaction` still refuses changes to quantity and identity and any delete. Corrections via `corrects_transaction_id` are built for journal reversals (slice 4). Document postings (journals, blocking, transfers, consignment) write their transactions at the final status and have no audit row. |
| FR-16.2 backward-step commands (Unpick, Unregister, CancelReservation) | built (slice 6) | See item 1. |
| FR-16.4 negative policy per item model group (`negative_physical_allowed`, `negative_financial_allowed`) | handoff — wave 8 (item model groups) | Slice 1 applies one tenant-wide policy from `INV_NEGATIVE_DEFAULT` (default forbid). |
| FR-16.4 `INV-LCK-001` lock timeout with 200 ms backoff | later slice of PSG-16 (the slice 5 draft's 2 s `lock_timeout` on journal posting was not kept: it applied to the whole transaction, advisory locks included, and was untested) | Lock waits follow the request context (30 s router timeout); there is no bounded `lock_timeout` yet. |
| FR-16.4 negative rows in the daily reconciliation | built (slice 7) | Each reconciliation run counts the on-hand rows of the legal entity with negative physical stock (`negativeRows`, shown on the run screen and carried by `reconciliation.diff`); the on-hand inquiry flags any group containing a negative row (`negative`). The workspace tile is listed under the web module. |
| FR-16.4 negative policy on blocking legs | by design | Blocking and release always forbid negative stock at their source, whatever the item policy: they move existing stock only. |
| FR-16.5 warehouse mandatory per storage dimension group; active dimensions per product dimension groups | handoff — wave 8 (dimension groups) | Slice 1 enforces site on every line (`INV-DIM-001`) and accepts any known dimension; the per-product groups come from wave 8. |
| FR-16.6 saga compensation for ledger failures | later slice of PSG-16 | No ledger call yet (see item 2). |
| FR-16.7 batch / serial selection in the reservation dialog (pick specific batches from a candidate list) | later slice of PSG-16 | MANUAL reserves one named dimension set (batch and serial included); the dialog does not list candidates. |
| FR-16.9 count freeze and recount flag | later slice of PSG-16 | See item 4. |
| FR-16.11 physical vs financial date split and BR-16.6 backdating | partial | `physical_date` and `financial_date` are stored (both the posting date for journals). A transaction at a physical status (vendor-owned RECEIVED or DEDUCTED, slice 5) has no financial date and no posted amount; `transaction.posted` carries both as null. Backdating rule and period check need wave 14. |
| FR-16.12 inventory-to-ledger reconciliation and `reconciliation.diff.v1` | built (slice 7) | `POST /v1/inventory/reconciliations` (InventoryAdmin) compares the sum of on-hand posted value of a legal entity in a currency with the net of the inventory account, both rounded to the minor unit, upserts one `reconciliation_run` per legal entity, account, currency and run date (RLS by tenant and legal entity) and enqueues `reconciliation.diff` in the same transaction when the difference is not zero; `GET /reconciliations` and `/reconciliations/{id}` are the report. Store tests: `TestReconcileMatchesThePostedJournals`, `TestReconcileDifferenceEmitsTheDiffEvent` (AC-16.9), `TestReconcileCountsNegativeRows`, `TestReconciliationIsScoped`; the projection = transactions check stays `TestProjectionReconcilesWithTransactionsAndLedger`. |
| FR-16.12 ledger balance read from the wave 11 ledger (balance projection of the inventory account, manual journals included) | handoff — wave 11 (ledger) | No ledger balance API or event feed exists yet. Without `ledgerBalance` the ledger side is the GL of the posted inventory journals (`GL_JOURNALS`), which cannot see manual ledger journals; AC-16.9 is met by passing the ledger's balance as `ledgerBalance` (`LEDGER_OVERRIDE`). A financial step through `transactions:apply` moves posted value without a GL entry in this service, so it shows as a difference on a `GL_JOURNALS` run. |
| FR-16.12 comparison per inventory account and dimension, one account per item group, sample vouchers in the report | handoff — wave 11 (posting profiles, financial dimensions) | One inventory account comes from configuration (`INV_INVENTORY_ACCOUNT`) and the GL entry carries no financial dimensions, so the run compares per legal entity, account and currency. The run date is today in UTC, not tenant-local. |
| FR-16.13 transaction inquiry `GET /transactions` | built (slice 2) | Filters product, status, origin type and physical date range; newest first with cursor paging; drill-down to the journal of a journal line. |
| FR-16.13 export and GL voucher drill-down | later slice of PSG-16 / handoff — wave 11 | Export is a web grid feature (wave 5 shell); `ledger_entry_id` stays null until the ledger Post exists. |
| FR-16.14 opening balance load from AX / SAP extracts via wave 10 migration framework | handoff — wave 10 (migration framework) | Mapping rules belong to the migration pack; the OPENING_BALANCE journal it targets is built. |
| FR-16.16 in-transit inquiry | built (slice 3) | `GET /transfers/in-transit` (see item 6). |
| FR-16.15 transfer order overview report (3.16.14): orders-by-status counts, export and a scheduled late-shipment notification | later slice of PSG-16 / handoff — wave 15 (documents, export) | Slice 10 builds the cross-order line inquiry and the late overview (`GET /v1/inventory/transfer-order-lines`), with the overview's legal entity, date range and status filters. Lateness is derived: an unshipped order after its planned ship date, or a shipment in transit more than `lateReceiptDays` (default 7, not yet a legal-entity parameter); a late receipt is measured from the order's planned receipt date when it has one (slice 15), else from the ship date. The late summary on the page counts the lines listed. The orders-by-status counts are served by the workspace summary (slice 13); there is no export and no notification. |
| FR-16.17 customer consignment warehouse (`CUSTOMER_CONSIGNMENT`, `customer_id`, SAP MB58 report) | later slice of PSG-16 (documents in wave 27) | Warehouse type belongs to wave 6 master data; stock there stays legal-entity owned. |
| FR-16.18 to FR-16.20 tag counting, ABC, segments | built (slices 3, 4, 5) | ABC classification by value (slice 3), tag counting (slice 4) and segments (slice 5) are built (item 8), with the gaps listed there. |
| 3.16.1 valuation by product x site per item model group (running average across dimension sets) | handoff — wave 20 (costing) | Slice 1 values issues at the running average of the on-hand row being issued (one dimension set), the finest consistent level under the row lock. Wave 20 replaces it with the costing engine. |
| 3.16.5 `POST /internal/v1/inventory/transactions:apply` | later slice of PSG-16 | Listed in the endpoint table; the reservation, blocking, count and transaction endpoints are built (slice 2), `onhand:rebuild` (slice 4) and `:validate` (slice 5). |
| 3.16.5 `onhand:rebuild` returns a run id | by design | The rebuild runs synchronously in one request and returns its report (rows checked, rows differing, the first 100 differences by field, whether they were repaired). An asynchronous run with an id belongs to the scheduled job (see the 3.16.10 rebuild row). |
| 3.16.5 event `transfer.release` | later slice of PSG-16 | Not emitted yet. `reconciliation.diff` (slice 7), `transfer.shipped/received` (slice 3), `ownership.changed` (slice 5), `reservation.created/released` and `blocking.created/released` (added by this service, not in the PRD event table) are built. |
| 3.16.4 monthly range partitions on InventoryTransaction; partial index on open statuses | later slice of PSG-16 | Partitioning is triggered above 20M rows. Open statuses occur since slice 6 (`transactions:apply`); the partial index on them is not built yet. |
| 3.16.4 `license_plate` dimension | handoff — wave 31 (warehouse) | Part of the hash contract; lines that set it are rejected until wave 31 activates it. |
| 3.16.10 weekly rebuild-and-compare job (`inv-1m-txn`, < 10 min) | later slice of PSG-16 | The `onhand:rebuild` endpoint is built (slice 4): it recomputes every visible row from the transaction status sums (through the posting arithmetic) and the active reservations in one snapshot, and with `?repair=true` relocks, recomputes and overwrites each differing row in posting lock order (`TestRebuildComparesAndRepairsTheProjection`). The scheduled job is not built; the 1M-transaction rebuild-and-compare is proven by `TestAC2ProjectionEqualsSumOfTransactions` (2.3 s for 1M transactions locally, well inside the 10 min budget). |
| 3.16.10 Debezium connector `inventory-outbox`, CNPG cluster `inventory-db`, Argo CD app | handoff — deployment (`deploy/`) | Owned outside this wave's paths. The chart builds the Deployment, Service, HPA and an optional ServiceMonitor. |
| 3.16.10 Temporal cron jobs (daily reconciliation at 02:00 tenant local with 3 retries and the missed-run ticket, count generation, TTL expiry, ABC, ownership batch, partition maintenance, draft cleanup) | later slice of PSG-16 | No scheduler wired: the reconciliation runs when InventoryAdmin calls `POST /reconciliations` (slice 7), which is idempotent per day so a scheduler can call it as is. The chart passes `INV_RECON_CRON` and `INV_RESERVATION_TTL_HOURS`, which the service does not read yet. |
| 3.16.10 feature flags `inv.fefo_policy`, `inv.count_pwa`, `inv.transfer_cost_diff` | handoff — platform flag service | No flag client in the kernel yet. |
| 3.16.11 AX / SAP migration of opening balances and history | handoff — wave 10 | See FR-16.14. |
| 3.16.13 parameters (`journal_names`, posting restriction, etc.) | partial (slice 12) | Journal names are built (code, journal type, approval requirement, default offset account, active flag). Not built: the six seeded names per legal entity (MOV, ADJ, TRF, BOM, CNT, OPN; there is no legal-entity provisioning hook in this service, and BOM / COUNT journals are not keyed), a journal name being mandatory on a keyed journal (it is optional, so existing clients keep working), the voucher number sequence per name (wave 5 number sequences), the posting restriction (USER / GROUP), the approval workflow template id (wave 4), "delete lines after posting" and per-name line caps (the cap still comes from `INV_JOURNAL_MAX_LINES`), and the other setup tables. A journal name cannot be deleted; deactivate it instead. |
| 3.16.13 journal name duty `inventory.journal-name.write` (InventorySetupMaintain, journal names) | done (seeded in `policies/authz/data.json`) | `inventory.journal-name.write` carries POST `/v1/inventory/journal-names` and PATCH `/v1/inventory/journal-names/*`; `inventory.read` carries GET `/v1/inventory/journal-names` and `/v1/inventory/journal-names/*`. |
| 3.16.14 count variance report, negative list, batch / serial trace, dimension combinations | built (slice 11) | See the slice 11 paragraph. The variance report lists counted lines (a sheet line that was never counted has no variance); the value of an unposted sheet is an estimate at today's running average. Tests: `TestCountVarianceReportValuesPostedAndOpenSheets`, `TestNegativeOnHandListShowsShortfallAndFirstNegativeDate`, `TestTrackingTraceFollowsABatchAndASerial`, `TestDimensionCombinationsReportFirstAndLastUse`. |
| 3.16.14 customer consignment stock, trace by document type and GS1 key data elements, count variance by counter across plans over time, export | later slice of PSG-16 / handoff — waves 8, 15, 27 | Customer consignment needs the warehouse type (wave 6) and customer documents (wave 27); batch key data elements live on the wave 8 batch; export is a wave 15 / shell grid feature. |
| 3.16.15 printed documents (posting list, count sheets, transfer shipping / receiving, labels, blocking notice) | later slice of PSG-16 | Document layouts follow the wave 5/15 templates and are not built. |
| 3 reservation expiry run resource in the policy bundle | done (seeded in `policies/authz/data.json`) | `inventory.reservation.write` carries POST `/v1/inventory/reservations:expire`. |
| 3 blocking scrap duty `inventory.blocking.scrap` | done (seeded in `policies/authz/data.json`) | Seeded as a flag duty (no routes); the service checks it inside `POST /v1/inventory/blockings/{id}:release` when the disposition is SCRAP (403 `INV-BLK-008` without it), and the web shows the Scrap action only to holders. |
| 3 escalation route `POST /v1/inventory/blockings/{id}:block` in the policy bundle | done (seeded in `policies/authz/data.json`) | `inventory.blocking.write` carries POST `/v1/inventory/blockings/*:block` next to POST `/v1/inventory/blockings` and `/v1/inventory/blockings/*:release`. No new duty. |
| 3.16.17 duties InventoryAdmin, InventorySetupMaintain | later slice of PSG-16 (per feature) | Built: `inventory.consignment.write` (ConsignmentMaintain, slice 5: replenishment orders, receipts and creating OWNERSHIP_CHANGE journals; approving and rejecting journals is the approval task of `inventory.journal.post`), `inventory.read`, `inventory.journal.write`, `inventory.journal.post`, `inventory.reservation.write` (InventoryReserve), `inventory.blocking.write` (InventoryBlock), `inventory.count.write` / `.post` / `.approve` (CycleCountMaintain / Post / Approve), `inventory.transfer.write` / `.post` (TransferOrderMaintain / TransferOrderPost, slice 3), `inventory.abc.write` (the ABC part of InventorySetupMaintain, slice 3), `inventory.admin` (the rebuild and reconciliation re-run parts of InventoryAdmin, slices 4 and 7), and `inventory.batch.write` for the interim batch-expiry input. Tag counting uses the cycle-count duties (marking open tags MISSING is `inventory.count.approve`); reversal uses the journal write / post duties. Segregation-of-duties pairs belong to the OPA policy bundle (platform). |
| 3.16.19 performance budgets (k6 at 10k concurrent issues, p95 SLOs) | later slice of PSG-16 | AC1's correctness at 10,000 concurrent issues is proven at the store (`TestAC1TenThousandConcurrentIssuesOfOneSKU`); the k6 run through the gateway and the p95 SLOs need a load environment. |
| Shared dimension-set inserts in hash order on the other multi-row writes (transfer order create / ship / receive, consignment order create, tag transfer) | later slice of PSG-16 | The Wave 16 proof found that two requests inserting the same new dimension sets in different orders deadlock (40P01); journal line adds and journal posting now insert all of a request's dimension sets first, in hash order (`upsertDimensionSets`, regression `TestConcurrentJournalsWithSharedNewDimensionSetsDoNotDeadlock`). The other paths that insert several new sets in line or row order have the same exposure and no regression test yet. |

## Ticket acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 concurrency: 10k issues never drive on-hand negative | built (Wave 16 proof) at the store; the k6 run over HTTP stays with 3.16.19 | `TestAC1TenThousandConcurrentIssuesOfOneSKU` is PRD AC-16.1 at full scale: 1,000 units, 10,000 one-unit issue journals prepared as drafts and released through one barrier against one SKU and dimension set over a 64-connection pool under RLS: exactly 1,000 post, 9,000 are `INV-NEG-001`, nothing else fails, on-hand ends 0 / 0, only 1,000 issue transactions exist and the rebuild is clean (about 3 s locally). `TestAC1MixedConcurrentIssuesAndReservationsNeverGoNegative` is the adversarial form: one SKU in four dimension sets, 3,000 issue journals of one to three lines in random order (lock ordering) and 300 FIFO reservations racing for more than the stock; no deadlock or other error, no row below zero, per-row books equal the stored quantities and reservations, every refusal justified by the final availability (seeded; rates logged, e.g. 2,214 / 3,000 journals posted). Database concurrency is the pool's 64 connections; the 10k-user k6 run through the gateway is the 3.16.19 performance row. `TestConcurrentIssuesNeverGoNegative` (300 issues) stays. |
| AC2 projection equals SUM(transactions) for 1M transactions | built (Wave 16 proof); the weekly rebuild job stays with 3.16.10 | `TestAC2ProjectionEqualsSumOfTransactions`: 16 workers on disjoint products write exactly N transactions through the store (opening, MOVEMENT, ADJUSTMENT and TRANSFER journals, blockings, `transactions:apply` registrations half unregistered, MANUAL reservations a third released, refused over-issues; two legal entities, Zipf over products, 12 months), each keeping its own books. The ledger must hold exactly N transactions, `RebuildOnHand` must find 0 differences over every row, an independent SQL re-aggregation of the transactions and active reservations must equal every bucket of every row, every book must equal its row, and per legal entity the posted value must equal the inventory net of the stored GL entries. CI runs N = 40,000; `PASSAGE_INVENTORY_AC2_TXNS=1000000` runs the issue's scale: locally 1,000,000 transactions over 2,000 products (43,484 rows) loaded in 3 min 23 s, rebuild-and-compare 2.3 s, 0 differences. Earlier evidence: `TestRebuildComparesAndRepairsTheProjection` (slice 4: the rebuild finds no difference after journals and a reservation in two legal entities, then reports and repairs a drifted, a deleted and an orphan row, and a legal-entity scope sees only its rows), `TestProjectionReconcilesWithTransactionsAndLedger`, `TestProjectionReconcilesAfterSlice2Operations` and the slice 3 transfer tests (`reconcile`) (journals, blockings, reservations and a count; reserved_physical = active reservations) check every row of small datasets; `TestRebuildEqualsLiveProperty` checks rebuild = live in the domain. |
| AC3 every posted journal produces a balanced GL entry, checked per journal type | partial: built for every journal type the service has (MOVEMENT, ADJUSTMENT, OPENING_BALANCE, COUNT, OWNERSHIP_CHANGE; TRANSFER has an empty entry by design) and their reversals; BOM and the universal-journal post are open | `TestAC3EveryJournalTypePostsABalancedEntry` (Wave 16 proof) posts each type through the store (COUNT through a count sheet, OWNERSHIP_CHANGE through consignment) plus the reversals of MOVEMENT, ADJUSTMENT and OPENING_BALANCE, and compares every stored `gl_entry` and its `journal.posted` glLines with hand-derived lines, checks the inventory net equals the change of posted on-hand value after each post, and a SQL sum over every posted journal's stored entry balances. `TestBuildEntry_BalancedPerJournalType` (rapid) covers random line mixes. Open: BOM journals (wave 9) and posting through the wave 11 universal journal `Post(JournalEntry)` (`ledger_entry_id` stays null; see item 2). |
| AC4 FEFO picking across 100 batches | built | `TestAC4FEFOAcross100BatchesMatchesTheReference` (Wave 16 proof) loads `testdata/inv-fefo-100.json`, whose expected allocations come from `testdata/gen_fefo_100.py` (an independent reference of FR-16.7 / BR-16.7 / FR-16.20, run with `python3 -I`): 100 batches with expiry ties broken by receipt date and lexical id, a batch on the shelf-life cutoff and one a day before it, never-expiring and null-expiry batches, an unbatched row, 10 batches blocked or quarantined in full and 5 in part, 12 pre-reserved, 8 received twice, decoys at another site and in a sales-order segment; four FEFO reservations in sequence (AC-16.4's 50 units, a 60-day margin, a drain with shortfall, and `INV-RES-002`) must match the reference pick by pick. `TestFEFO100Batches` (domain) and `TestFEFOReservationSpansBatchesInExpiryOrder` (store) stay. |
| AC5 every deliverable has code or a row here | built | This file. |
| AC6 `go vet` / `go test`, web `vitest` and `tsc`, OpenAPI / AsyncAPI match code, Helm lints | built | `go vet ./... && go test ./...`, `gofmt -l .`, `helm lint charts/inventory`, and in `apps/web` `npx vitest --run src/modules/inventory` and `npx tsc --noEmit`. Spec parity is tested (`TestOpenAPIDocumentsEveryOperation`, `TestPayloadsMatchAsyncAPISchemas`). |

## Test-plan items not built

| Item | Status | Reason |
|---|---|---|
| Status transition monotonicity (rapid) over `ApplyTransactionStep` | built (slice 6) | `TestStatusMovesAreMonotonicUnlessNamed` drives random command sequences through `ValidateStep` (no backward move except the named one, no direction change) and replays them on a projection row that must equal a rebuild from the final status. |
| Outbox event schema compatibility (BACKWARD) against the registry | handoff — platform schema registry | Registry service is owned by the platform / eventing wave. Payload-to-AsyncAPI parity is tested locally. |
| Period control contract test | handoff — wave 14 | See above. |
| PWA offline count E2E (Playwright) | later slice of PSG-16 | Count PWA is not built. |
| Fixtures `inv-fefo-100`, `inv-1m-txn` | built (Wave 16 proof) | `testdata/inv-fefo-100.json` with its generator `testdata/gen_fefo_100.py` (AC4). `inv-1m-txn` is generated in the AC2 test (`wave16_ac2_test.go`, seeded, Zipf over up to 2,000 products, 12 months) rather than by `scripts/fixtures/inv_txn_gen.py`, so the transactions go through the posting path instead of being bulk-loaded. |
| Fixtures `inv-demo-plant`, `inv-ax-extract`, `inv-sap-extract` under `testdata/` | later slice of PSG-16 / handoff — wave 10 | The demo plant belongs to the Deploy line; the AX / SAP extracts to the wave 10 migration packs. |

## Web module (`apps/web/src/modules/inventory`)

| Item | Status | Reason |
|---|---|---|
| On-hand inquiry, journal list, new journal, journal detail (add line, post) | built | Shell primitives, CSS Modules, loading / empty / error states, vitest tests. |
| Transactions, reservations (list, release, new reservation), blocking (list, release, block stock), count plans (list, generate sheet, new plan), count sheets (list, sheet with count entry and post) | built (slice 2) | Same primitives and states; vitest tests in `slice2.test.tsx`. |
| Transfer orders (list by status, new order with several lines, order with ship / receive / close-short / cancel), stock in transit, ABC runs (list, run, report) | built (slice 3) | Same primitives and states; vitest tests in `slice3.test.tsx`. Transfer transactions link to their order from the transaction inquiry. |
| Count tags per plan (list with status filter, generate a range, count or void a tag, transfer to a count sheet with the supervisor's mark-missing choice); reverse on the journal detail (posting date, opens the reversal draft, links between a journal and its reversal) | built (slice 4) | Same primitives and states; vitest tests in `slice4.test.tsx`. The rebuild is an internal endpoint (InventoryAdmin) the BFF does not expose, so it has no screen. |
| Journal workflow actions (Validate, Submit, Approve / Reject, Reopen, Cancel) on the journal detail; ownership-change lines (vendor, consignment price); segment on journal lines; consignment orders (list by status, new order, order with receive), consignment stock by vendor with a New ownership change link; segment fields on the reservation dialog; owner and segment filters and groupings on the on-hand inquiry | built (slice 5) | Same primitives and states; vitest tests in `slice5.test.tsx`. |
| Transaction detail (status history, Unpick / Unregister / Cancel reservation with the date of the move, retried with the same Idempotency-Key), status-change inquiry (filter by transaction and command), transaction rows linked to their detail | built (slice 6) | Same primitives and states; vitest tests in `slice6.test.tsx`. `transactions:apply` is a service-to-service endpoint with no screen. |
| Inventory-to-ledger reconciliation: runs (status filter), run reconciliation (legal entity, currency, optional ledger balance), run detail (difference alert, negative-stock warning, both sides and the ledger source) | built (slice 7) | Same primitives and states; vitest tests in `slice7.test.tsx`. The Run action is shown to every reader; the service answers 403 without `inventory.admin`, shown on the form (the shell has no duty hook for page actions). |
| Count variance, negative on-hand, batch and serial trace and dimension combination reports (filters, totals per counter, estimate notice, empty / error states) | built (slice 11) | Same primitives and states; vitest tests in `slice11.test.tsx`. Nav items use `inventory.read`. |
| Journal names (list with type / legal entity / state filters, require or drop approval, activate or deactivate, new journal name), journal name choice on the new journal form | built (slice 12) | Same primitives and states; vitest tests in `slice12.test.tsx`. The nav item uses `inventory.read` (see the `inventory.journal-name.write` handoff row). |
| Journals-awaiting-approval tile | built (slice 13) | Workspace page tile with a link to the journal list. |
| Approver inbox (list of submitted journals for the approver, filter by status in the list URL) | later slice of PSG-16 | The journal list filters by status SUBMITTED in its form; the tile only shows the count. |
| Printing tag strips | handoff — wave 15 (documents) | The tag list shows the numbers to print. |
| Warehouse pickers on the transfer form, transfer documents (print), batch / serial selection at receipt | later slice of PSG-16 / handoff — wave 6 | The form takes warehouse and site codes; the receipt keeps the shipped dimensions. |
| Inventory management workspace (tiles for journals awaiting approval, negative on-hand rows, reconciliation differences, counted sheets, draft journals, open count sheets, active reservations, transfer orders by status; legal-entity scope) | built (slice 13) | Same primitives and states; vitest tests in `slice13.test.tsx`. Nav item gated on `inventory.read`. The reconciliation tile shows the last run time. Not built: user-personalised tile layout and the wave 5 workspace tile framework, tile links that carry a status filter into the target list. |
| Holds (list with state and source filters, release all or part, place hold for a quantity or a whole lot, Held column on the on-hand inquiry); reservation time to live (field on the new reservation form, Expires column, Release expired) | built (slice 16) | Same primitives and states; vitest tests in `slice16.test.tsx`. Holds are not shown per dimension set on a detail page. |
| Quarantine disposition choice in the release action | later slice of PSG-16 | The screen releases with RELEASE, the only disposition the service supports yet. |
| On-hand inquiry As of date field (replay, availability columns hidden, date validated) | built (slice 14) | Same primitives and states; an info alert names the replay date; the date is checked as a real calendar date before the call (the service answers 400 otherwise). A text field, not a date picker (the shell has no date picker primitive yet). Vitest tests in `pages.test.tsx` and `api/client.test.ts`. |
| Drill from on-hand to transactions, export, keyboard grid entry, column personalization | later slice of PSG-16 | Need the transaction inquiry and the wave 5 shell personalization. |
| Product and legal-entity pickers | handoff — waves 6 / 8 screens | The forms take product and legal-entity ids; lookups come from the org and product modules. |

## Technical notes

- Idempotency: journal create stores the `Idempotency-Key` on the journal (unique per
  tenant, like costing) and replays with `Idempotent-Replayed: true`; lines:bulk takes an
  optional per-line `clientKey` (unique per journal); posting is idempotent by journal id
  and guarded by the journal version (`If-Match`). Reservations, blockings, count plans and
  sheet generation store their key on the row the same way (a different body under a
  stored key is `idempotency_key_reuse`); blocking release, count entry and count posting
  are guarded by the row version (`If-Match`); reservation release (DELETE) is idempotent.
  Tag generation stores its key on the tag range and tag transfer on the count sheet it
  creates; tag entry and void are guarded by the tag version (`If-Match`); a journal has
  one reversal, so `:reverse` replays it without a key.
  Replenishment orders store their key on the order (a different body is
  `idempotency_key_reuse`); receipts and the journal workflow steps are guarded by the
  order or journal version (`If-Match`); a reservation's demand segment is part of its
  replay check.
  Status changes store their key on the audit row (unique per tenant, serialized with an
  advisory lock, replayed with `Idempotent-Replayed`; a key reused for another request is
  `idempotency_key_reuse`); a STEP is also guarded by `fromStatus`.
  The shared `idempotency_keys` table is not used.
- Money: cost amounts, cost prices and `posted_value` are `numeric(20,6)` with a currency
  column, values rounded to the currency minor unit with `platform/money`.
