# inventory

Passage inventory service. Wave 16 (PSG-16). Slice 1: the immutable inventory
ledger, the dimension hash, the on-hand projection, inventory journals
(MOVEMENT, ADJUSTMENT, OPENING_BALANCE) with synchronous posting, the negative
stock policy, serial uniqueness, the journal list and the on-hand inquiry grouped
by any dimension subset. Slice 2: reservations (MANUAL, FIFO, FEFO, LIFO),
blocking and quarantine, cycle counting with tolerance approval and blind count
(posted as COUNT journals), and the transaction inquiry. Slice 3: transfer orders
through an in-transit warehouse (ship, receive with tolerances and close-short,
cancel), the stock-in-transit inquiry and ABC classification by on-hand value. Slice 4:
journal reversal (BR-16.12), tag counting with transfer to a count sheet (FR-16.18) and
the on-hand rebuild-and-compare (FR-16.3). Slice 5: the journal approval workflow
(BR-16.11) and the `:validate` dry run, vendor consignment (replenishment orders received
as vendor-owned, quantity-only stock; OWNERSHIP_CHANGE journals; the consignment stock
report; FR-16.17, BR-16.14) and the segment dimension with segment-aware reservations
(FR-16.20, BR-16.17). Slice 6: the transaction status model as commands (FR-16.2, BR-16.1):
the service-to-service `transactions:apply` (register a transaction for an external origin,
step it forward), the named backward moves Unpick, Unregister and CancelReservation, the
append-only status-change audit, the transaction detail and the status-change inquiry. The
web screens live in
`apps/web/src/modules/inventory`.

Requirements source: `docs/prd/phase-3-procure-to-pay.md` section 3.16.
What is not in this slice is listed in `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/inventory/            entrypoint: config, connect, migrate, router, admin port, serve
internal/domain/          journal rules, valuation, status model, dimensions, on-hand buckets, FEFO,
                          reservation, blocking, count, transfer and ABC rules (pure, no I/O)
internal/store/           pgx repositories: journals, posting, on-hand projection, transactions,
                          reservations, blockings, counts, transfer orders, ABC runs (RLS-aware)
internal/api/             HTTP handlers, DTOs, validation, Problem responses
internal/events/          event type constants and payloads
migrations/               0001 ledger, 0002 reservations and batch expiry, 0003 blocking and counts,
                          0004 transfer orders, 0005 ABC runs, 0006 reversal links and count tags,
                          0007 consignment orders, ownership-change origins, reservation segment,
                          0008 status-change audit, 0009 reconciliation runs (embedded, applied by
                          dbx.Migrate)
api/openapi.yaml          OpenAPI 3.1 for every endpoint (parity is tested)
events/asyncapi.yaml      AsyncAPI 3 for every event published (payload parity is tested)
charts/inventory/         Helm chart (Deployment, Service, HPA, ServiceMonitor toggle)
```

## Endpoints and duties

| Method | Path | Duty | PRD duty |
|---|---|---|---|
| POST | `/v1/inventory/journals` | `inventory.journal.write` | InventoryJournalMaintain |
| GET | `/v1/inventory/journals` | `inventory.read`, or the service-only `inventory.ownership-change.read` (receiving's signed token, #305; sees only OWNERSHIP_CHANGE journals: other types answer 403 on list, 404 on get) | InventoryInquire |
| GET | `/v1/inventory/journals/{id}` | `inventory.read`, or the service-only `inventory.ownership-change.read` (OWNERSHIP_CHANGE journals only) | InventoryInquire |
| POST | `/v1/inventory/journals/{id}/lines:bulk` | `inventory.journal.write` | InventoryJournalMaintain |
| POST | `/v1/inventory/journals/{id}:post` | `inventory.journal.post` | InventoryJournalPost |
| GET | `/v1/inventory/onhand` | `inventory.read` | InventoryInquire |
| GET | `/v1/inventory/transactions` | `inventory.read` | InventoryInquire |
| POST | `/v1/inventory/reservations` | `inventory.reservation.write` | InventoryReserve |
| GET | `/v1/inventory/reservations` | `inventory.read` | InventoryInquire |
| DELETE | `/v1/inventory/reservations/{id}` | `inventory.reservation.write` | InventoryReserve |
| PUT | `/v1/inventory/batch-expiries/{productId}/{batchId}` | `inventory.batch.write` | (interim FEFO input until wave 8 batches) |
| POST | `/v1/inventory/blockings` | `inventory.blocking.write` | InventoryBlock |
| GET | `/v1/inventory/blockings` | `inventory.read` | InventoryInquire |
| POST | `/v1/inventory/blockings/{id}:release` | `inventory.blocking.write` (SCRAP also `inventory.blocking.scrap`) | InventoryBlock |
| POST | `/v1/inventory/blockings/{id}:block` | `inventory.blocking.write` | InventoryBlock |
| POST | `/v1/inventory/count-plans` | `inventory.count.write` | CycleCountMaintain |
| GET | `/v1/inventory/count-plans` | `inventory.read` | InventoryInquire |
| POST | `/v1/inventory/count-plans/{id}:generate` | `inventory.count.write` | CycleCountMaintain |
| GET | `/v1/inventory/count-sheets` | `inventory.read` | InventoryInquire |
| GET | `/v1/inventory/count-sheets/{id}` | `inventory.read` | InventoryInquire |
| PUT | `/v1/inventory/count-sheets/{id}/lines/{lineId}` | `inventory.count.write` | CycleCountMaintain |
| POST | `/v1/inventory/count-sheets/{id}:post` | `inventory.count.post` (+ `inventory.count.approve` above tolerance) | CycleCountPost / CycleCountApprove |
| POST | `/v1/inventory/transfer-orders` | `inventory.transfer.write` | TransferOrderMaintain |
| GET | `/v1/inventory/transfer-orders` | `inventory.read` | InventoryInquire |
| GET | `/v1/inventory/transfer-orders/{id}` | `inventory.read` | InventoryInquire |
| POST | `/v1/inventory/transfer-orders/{id}:ship` | `inventory.transfer.post` | TransferOrderPost |
| POST | `/v1/inventory/transfer-orders/{id}:receive` | `inventory.transfer.post` | TransferOrderPost |
| POST | `/v1/inventory/transfer-orders/{id}:cancel` | `inventory.transfer.write` | TransferOrderMaintain |
| GET | `/v1/inventory/transfers/in-transit` | `inventory.read` | InventoryInquire |
| POST | `/v1/inventory/abc-runs` | `inventory.abc.write` | InventorySetupMaintain (ABC setup) |
| GET | `/v1/inventory/abc-runs` | `inventory.read` | InventoryInquire |
| GET | `/v1/inventory/abc-runs/{id}` | `inventory.read` | InventoryInquire |
| POST | `/v1/inventory/journals/{id}:reverse` | `inventory.journal.write` | InventoryJournalMaintain (the reversal is posted with `:post`) |
| POST | `/v1/inventory/count-plans/{id}:generate-tags` | `inventory.count.write` | CycleCountMaintain |
| GET | `/v1/inventory/count-plans/{id}/tags` | `inventory.read` | InventoryInquire |
| POST | `/v1/inventory/count-plans/{id}:transfer-tags` | `inventory.count.write` (+ `inventory.count.approve` to mark open tags MISSING) | CycleCountMaintain / CycleCountApprove |
| POST | `/v1/inventory/count-tags/{id}:use` | `inventory.count.write` | CycleCountMaintain |
| POST | `/v1/inventory/count-tags/{id}:void` | `inventory.count.write` | CycleCountMaintain |
| POST | `/internal/v1/inventory/onhand:rebuild` | `inventory.admin` | InventoryAdmin |
| POST | `/v1/inventory/journals/{id}:validate` | `inventory.journal.write` | InventoryJournalMaintain |
| POST | `/v1/inventory/journals/{id}:submit` | `inventory.journal.write` | InventoryJournalMaintain |
| POST | `/v1/inventory/journals/{id}:approve` | `inventory.journal.post` | InventoryJournalPost (approval task) |
| POST | `/v1/inventory/journals/{id}:reject` | `inventory.journal.post` | InventoryJournalPost (approval task) |
| POST | `/v1/inventory/journals/{id}:reopen` | `inventory.journal.write` | InventoryJournalMaintain |
| POST | `/v1/inventory/journals/{id}:cancel` | `inventory.journal.write` | InventoryJournalMaintain |
| POST | `/v1/inventory/journal-names` | `inventory.journal-name.write` | InventorySetupMaintain (journal names) |
| GET | `/v1/inventory/journal-names` | `inventory.read` | InventoryInquire |
| GET | `/v1/inventory/journal-names/{id}` | `inventory.read` | InventoryInquire |
| PATCH | `/v1/inventory/journal-names/{id}` | `inventory.journal-name.write` | InventorySetupMaintain (journal names) |
| POST | `/v1/inventory/consignment-orders` | `inventory.consignment.write` | ConsignmentMaintain |
| GET | `/v1/inventory/consignment-orders` | `inventory.read` | InventoryInquire |
| GET | `/v1/inventory/consignment-orders/{id}` | `inventory.read` | InventoryInquire |
| POST | `/v1/inventory/consignment-orders/{id}:receive` | `inventory.consignment.write` | ConsignmentMaintain |
| GET | `/v1/inventory/consignment/stock` | `inventory.read` | InventoryInquire |
| POST | `/internal/v1/inventory/transactions:apply` | `inventory.transaction.apply` | service account (waves 17–20) |
| POST | `/v1/inventory/transactions/{id}:unpick` | `inventory.transaction.step` | (no PRD duty; the warehouse user undoing a pick) |
| POST | `/v1/inventory/transactions/{id}:unregister` | `inventory.transaction.step` | (no PRD duty; the receiving user undoing a registration) |
| POST | `/v1/inventory/transactions/{id}:cancel-reservation` | `inventory.transaction.step` | (no PRD duty) |
| GET | `/v1/inventory/transactions/{id}` | `inventory.read` | InventoryInquire |
| GET | `/v1/inventory/status-changes` | `inventory.read` | InventoryInquire |
| POST | `/v1/inventory/reconciliations` | `inventory.admin` | InventoryAdmin (reconciliation re-run) |
| GET | `/v1/inventory/reconciliations` | `inventory.read` | InventoryInquire |
| GET | `/v1/inventory/reconciliations/{id}` | `inventory.read` | InventoryInquire |

Creating an OWNERSHIP_CHANGE journal also needs `inventory.consignment.write`
(ConsignmentMaintain); its lines and posting use the journal duties.

Duty names follow the repository's `<context>.<entity>.<action>` convention; the
right-hand column is the PRD 3.16.17 name each one stands for.

## Posting model

`POST /v1/inventory/journals/{id}:post` runs in one Postgres transaction:

1. Load the journal `FOR UPDATE`; a POSTED journal is returned unchanged (the journal
   id is the idempotency key); otherwise the version must match `If-Match`.
2. Validate the header and every line (site mandatory, serial ±1, precision).
3. Plan each line: receipts (qty > 0) post at `PURCHASED`, issues (qty < 0) at `SOLD`.
4. Take an advisory lock per received serial, then lock the on-hand rows in ascending
   (product, dimension set hash) order (`SELECT ... FOR UPDATE`, BR-16.4).
5. Value each line under the lock: receipts at qty x cost price, issues at the row's
   running average (posted value / posted quantity); the issue that empties a row takes
   its whole value, so no rounding residue stays. Every value is rounded to the
   currency's minor unit (BR-16.5) and the same amount goes to the transaction, the
   projection and the GL entry.
6. Apply the deltas and check the negative policy against `avail_physical` /
   `avail_ordered`: a posting that leaves either negative and lower than before is
   rejected with `INV-NEG-001` / `INV-NEG-002` (a receipt into an already-negative row is
   accepted). A row valued in another currency rejects the posting (`INV-CUR-001`).
7. Reject a received serial that is now in physical stock more than once in the legal
   entity (`INV-SER-001`, BR-16.9).
8. Write `inventory_transaction` and `inventory_transaction_origin` rows (append-only,
   enforced by trigger), build the balanced GL entry (inventory account against the
   MOVEMENT offset account, or the adjustment / opening clearing account) and store it
   on the journal.
9. Enqueue `transaction.posted` (per line), `onhand.changed` (per row) and
   `journal.posted` into the outbox in the same transaction.

The on-hand primary key is (tenant, legal entity, product, dimension set), so legal
entities that reuse dimension values never share stock.

## Reservations (FR-16.7)

`POST /reservations` locks every on-hand row of the product in the legal entity, in
ascending dimension-set order (the order postings use), then allocates:

- MANUAL reserves one named dimension set (site required) and fails with `INV-RES-001`
  beyond its available physical quantity;
- FIFO, FEFO and LIFO spread the quantity over the unrestricted rows that match the
  dimension filter: FEFO by batch expiry (`batch_expiry`), then earliest receipt, then
  batch id (BR-16.7), skipping batches that expire before `requiredDate +
  shelfLifeMarginDays`; FIFO / LIFO by earliest receipt date of the dimension set.

Each allocation is one `inventory_reservation` row and adds to the row's
`reserved_physical`, which lowers `avail_physical`: postings, blockings and other
reservations then cannot take that stock (they fail the negative check or
`INV-RES-001`). Without `allowPartial` a shortfall rejects the request; with it the
shortfall is returned. `DELETE /reservations/{id}` releases (idempotent). Events:
`reservation.created` / `.released` per allocation and `onhand.changed` with
`deltaBucket = RESERVED_PHYSICAL`.

## Blocking and quarantine (FR-16.8, BR-16.10)

`POST /blockings` moves a quantity from unrestricted stock (source dimensions with no
`inventory_status`) to the same dimensions with `inventory_status` BLOCKED or
QUARANTINE. The move is a SOLD issue at the source valued at its running average and a
PURCHASED receipt at the blocked set for the same value (origin `BLOCKING`,
`reference_number` = blocking id), written in one transaction under both row locks.
Physical stock and value summed over the product do not change; both legs are on the
inventory account, so there is no GL entry. Restricted rows are left out of
`availPhysical` / `availOrdered` in the on-hand inquiry (reported as `blocked`), out of
policy reservations and out of manual reservations (`INV-BLK-002`). Release moves the
stock back the same way; a quarantine needs an explicit disposition (`INV-BLK-001`).
SCRAP writes the stock off: the release creates and posts, in the same transaction, an
ADJUSTMENT journal (`blocking-scrap:<blocking id>`) that issues the quantity from the blocked
dimensions at their running average, so the GL entry debits the adjustment account and credits
the inventory account by the value the blocking carried, and the inventory-to-ledger
reconciliation sees it like any posted journal. It needs the `inventory.blocking.scrap` duty
(`INV-BLK-008`) and `INV_ADJUSTMENT_ACCOUNT`, writes off the whole blocking, and always posts
under the forbid negative policy (`INV-NEG-001` when other postings took the blocked stock); the
blocking answers `scrapJournalId`. RETURN
needs the wave 18 vendor return and answers `INV-BLK-005`. `POST /blockings/{id}:block` moves
an active quarantine to BLOCKED (the QUARANTINE to BLOCKED transition) as a valued pair with
no GL effect, emits `blocking.escalated` and keeps the blocking active. A blocking may carry an
`expectedReleaseDate` (information only, not before the blocking date).

## Cycle counting (FR-16.9, BR-16.8)

A count plan names a legal entity, optionally a warehouse, the counting unit and
currency, the tolerances (default 5 % and 100.00) and whether counts are blind.
`:generate` snapshots one sheet line per on-hand row with physical stock. Counters enter
quantities line by line (`If-Match` on the sheet version); on a blind sheet the expected
quantity is withheld from callers without `inventory.count.approve` until it is posted.
`:post` locks the counted rows in lock order, takes the variance against the book
quantity at that moment, and refuses with 403 `INV-CNT-001` when any variance exceeds
the percent or value tolerance and the caller cannot approve. The variances are then
written as a COUNT journal (gains valued at the running average) and posted through the
journal path, so the negative policy, GL entry (inventory against the adjustment account)
and events are those of any journal.

## Journal reversal (BR-16.12)

`:reverse` on a POSTED journal creates a DRAFT journal of the same type, legal entity,
offset account and currency, with one negated line per original line (`reversesLineId`).
The original is never changed and has at most one reversal (a repeat returns it). A
reversal takes no extra lines. Posting it goes through the normal `:post`, except that
each line is valued at the negated value of the transaction it corrects (not the running
average), its transaction carries `corrects_transaction_id`, and `journal.posted` carries
`reversesJournalId`: the GL entry is the exact mirror of the original. The negative
policy still applies, so stock already issued cannot be reversed out.

## Tag counting (FR-16.18, BR-16.15)

`:generate-tags` creates a range of OPEN tags for a count plan, numbered from the legal
entity's `TAG-` sequence. Each tag is counted once with `:use` (product, dimensions,
counted quantity; zero is a count; the plan's warehouse is filled in) or voided before it
is transferred. `:transfer-tags` sums the untransferred USED tags per product x dimension
set into a new OPEN count sheet (counted = tag sum, expected = book now); the sheet is
then posted like any count sheet. Open tags block the transfer (`INV-TAG-002`) unless the
caller marks them MISSING, which needs `inventory.count.approve`.

## Rebuild and compare (FR-16.3)

`POST /internal/v1/inventory/onhand:rebuild` recomputes every visible on-hand row from the
transactions (status sums through the posting arithmetic) and the ACTIVE reservations,
compares it with the stored row in one snapshot and reports the differing rows and
fields. `?repair=true` locks each differing row in posting lock order, rebuilds it again
under the lock and overwrites it (a missing row is recreated, an orphan zeroed).

## Inventory-to-ledger reconciliation (FR-16.12)

`POST /v1/inventory/reconciliations` (`legalEntityId`, `currency`, optional `ledgerBalance`)
compares, for the legal entity and currency:

- the inventory value: the sum of `on_hand.posted_value` of the rows valued in the currency;
- the ledger balance of the inventory account (`INV_INVENTORY_ACCOUNT`; unset is `INV-JNL-005`):
  without `ledgerBalance`, debit minus credit of that account over the GL entries of the POSTED
  inventory journals in the currency (`GL_JOURNALS`); with it, the balance the caller read from
  the wave 11 ledger, which also carries manual journals (`LEDGER_OVERRIDE`, AC-16.9).

Both sides are rounded to the currency's minor unit and the difference is inventory minus
ledger; `MATCHED` means zero. The run also counts on-hand rows of the legal entity with
negative physical stock (`negativeRows`, FR-16.4), which does not change the status. Blocking,
transfer and ownership-change legs move value between rows of the legal entity (or post their
own GL entry), so the GL-derived run matches whenever the projection and the journals agree;
a status step to SOLD / PURCHASED through `transactions:apply` moves posted value without a GL
entry here (the calling document posts it), so it shows as a difference until the ledger
balance is supplied.

A run is keyed by (legal entity, account, currency, run date = today UTC): a re-run on the same
day recomputes and overwrites that day's row (same id). A non-zero difference enqueues
`dev.passage.inventory.reconciliation.diff.v1` in the same transaction, once per run that
differs. `GET /v1/inventory/reconciliations` (filters `legalEntityId`, `status`) and
`GET /v1/inventory/reconciliations/{id}` are the report. The 02:00 tenant-local Temporal cron
that would call it daily is not wired (see NOT-IMPLEMENTED.md).

## Status model commands (FR-16.2, BR-16.1)

Journals, blockings, transfers and consignment write each transaction at its final status.
Other services drive transactions through statuses with
`POST /internal/v1/inventory/transactions:apply` (Idempotency-Key required):

- **Register** (`origin` set): one transaction for an external origin (`PURCH_LINE`,
  `RECEIPT_LINE`, `SALES_LINE`, `PROD_LINE`; one transaction per origin, `INV-STP-002`) at an
  expected or physical status matching the sign of `qty` (receipts positive, issues
  negative; never SOLD or PURCHASED). Receipts carry `qty × costPrice` as physical cost,
  issues the row's running average; vendor-owned stock is never valued.
- **Step** (`transactionId` set): forward to `toStatus` (skips allowed; backward or across
  directions is `INV-STP-001`). `fromStatus` is the status the caller read; a mismatch is
  412 `conflict_version`. Moving into SOLD / PURCHASED sets the financial date and
  `cost_amount_posted` (issues at the running average, receipts at `costAmount` or their
  physical cost); vendor-owned stock never reaches a financial status (`INV-OWN-001`).
  Moving from an expected status into a physical or financial one sets the physical date.

The named backward moves are user commands on one transaction:
`:unpick` (PICKED → RESERVED_PHYSICAL), `:unregister` (REGISTERED → ARRIVED, AC-16.8) and
`:cancel-reservation` (RESERVED_PHYSICAL or RESERVED_ORDERED → ON_ORDER). The move follows from
the transaction's current status; a command that does not apply is `INV-STP-001`.

Every registration or move runs in one database transaction: the transaction row is locked,
the on-hand row is locked and moved by the whole quantity (old bucket out, new bucket in,
the same arithmetic the rebuild uses), the negative policy is checked under the lock, the
row's status and dates are updated (quantity and identity stay immutable), an
`inventory_transaction_status_change` row is written (append-only, unique per
Idempotency-Key; a replay answers 200 with `Idempotent-Replayed`, another request under the
same key is `idempotency_key_reuse`), and `transaction.posted` (with `statusChangeId` and
`previousStatus`) and `onhand.changed` (the bucket of the status reached) go to the outbox.
No GL entry is produced: the calling document posts its own ledger entry.
`GET /transactions/{id}` returns a transaction with its status history and
`GET /status-changes` lists the audit newest first.

## Run

```
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_inventory
export MIGRATE_ON_START=true              # dev only
export INV_INVENTORY_ACCOUNT=1300 INV_ADJUSTMENT_ACCOUNT=5100 INV_OPENING_CLEARING_ACCOUNT=3900
go run ./cmd/inventory
```

Optional: `INV_NEGATIVE_DEFAULT` (`forbid`, default, or `allow`), `INV_JOURNAL_MAX_LINES`
(1000 to 50000, default 10000), `HTTP_ADDR` (`:8080`), `ADMIN_ADDR` (`:9090`).
Configuration is validated at startup; the process exits non-zero when a required
variable is missing. The HTTP port serves the API and `/healthz`; the admin port serves
`/readyz` (checks Postgres) and `/metrics`.

## Test

```
go vet ./...
go test ./...                              # domain, API and contract tests run without a database
PASSAGE_TEST_DATABASE_URL=postgres://owner@.../db \
PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app@.../db go test ./...   # store tests under RLS
```

Store tests use `platform/go/testkit` (`DBWithApp`): migrations run as the owner and every
query runs as `passage_app` (NOSUPERUSER, NOBYPASSRLS), so row-level security is exercised.
They skip without `PASSAGE_TEST_DATABASE_URL`. Domain property tests use `pgregory.net/rapid`.

The Wave 16 proof tests (`internal/store/wave16_ac*_test.go`) cover the issue's acceptance
criteria: 10,000 concurrent issues (AC1), rebuild-and-compare after N transactions (AC2;
40,000 by default, `PASSAGE_INVENTORY_AC2_TXNS=1000000` for the issue's 1M), the GL entry of
every journal type (AC3) and the `testdata/inv-fefo-100.json` FEFO fixture (AC4), whose
expected picks come from `testdata/gen_fefo_100.py` (`python3 -I gen_fefo_100.py > inv-fefo-100.json`).

## Error codes

| Code | Status | Meaning |
|---|---|---|
| `COM-0001` | 400 | Validation failure (field errors in `errors[]`), malformed body or cursor, or an invalid line rule |
| `COM-0428` | 428 | `If-Match` (resource version) missing on `:post`, `:release` or a count entry |
| `COM-0500` | 500 | Unexpected error (detail hidden) |
| `AUTH-0001` | 401 | Missing or malformed tenant on the request |
| `AUTH-0003` | 403 | Missing duty, or a legal entity outside the caller's scope |
| `INV-JNL-NOT-FOUND` | 404 | Journal id not found in the tenant / legal-entity scope |
| `INV-JNL-001` | 422 | Journal exceeds the maximum number of lines |
| `INV-JNL-002` | 422 | Movement journal requires an offset account (store and posting check; the create endpoint reports it earlier as a `COM-0001` field error on `offsetAccountId`) |
| `INV-JNL-003` | 409 | Journal is already posting (reserved for the Temporal workflow) |
| `INV-JNL-004` | 409 | Journal is not in a state that allows the change (lines on a non-DRAFT or reversal journal, posting from another status, reversing a journal that is not POSTED) |
| `INV-NEG-001` | 422 | Issue would make available physical stock negative (policy forbids it); detail names product, dimensions and shortfall |
| `INV-NEG-002` | 422 | Issue would make financial (ordered) stock negative |
| `INV-SER-001` | 422 | Serial already in physical stock in the legal entity |
| `INV-SER-002` | 422 | Serial-tracked line quantity is not ±1 |
| `INV-DIM-001` | 422 | Mandatory dimension (site) missing |
| `INV-UOM-002` | 422 | Quantity has more decimals than the inventory unit allows (6) |
| `INV-CUR-001` | 422 | Posting currency differs from the currency the on-hand row is valued in |
| `INV-RES-001` | 422 | Over-reservation: less available than requested (no `allowPartial`) |
| `INV-RES-002` | 422 | FEFO reservation found no eligible batch |
| `INV-RES-003` | 404 | Reservation id not found |
| `INV-BLK-001` | 422 | Quarantine release without a disposition |
| `INV-BLK-002` | 422 | Reservation of BLOCKED or QUARANTINE stock |
| `INV-BLK-003` | 422 | Blocked quantity exceeds the available quantity at the source |
| `INV-BLK-004` | 404 | Blocking id not found |
| `INV-BLK-005` | 422 | Disposition RETURN (not supported yet) |
| `INV-BLK-006` | 422 | Blocking is no longer active |
| `INV-BLK-007` | 422 | Only quarantined stock moves to blocked |
| `INV-BLK-008` | 403 | SCRAP without `inventory.blocking.scrap` |
| `INV-CNT-001` | 403 | Count variance above tolerance and the caller lacks `inventory.count.approve` |
| `INV-CNT-003` | 422 | Count sheet is not OPEN |
| `INV-CNT-004` | 404 | Count plan, sheet or line not found |
| `INV-CNT-005` | 422 | Count sheet posted with no counted line |
| `INV-TAG-001` | 422 | Count tag already used |
| `INV-TAG-002` | 422 | Open tags block the transfer to a count sheet |
| `INV-TAG-003` | 422 | Tag is VOID, MISSING or already on a count sheet |
| `INV-TAG-004` | 422 | No used tag to transfer |
| `INV-TAG-005` | 404 | Count tag not found |
| `INV-TAG-006` | 403 | Marking open tags MISSING without `inventory.count.approve` |
| `INV-OWN-001` | 422 | Vendor-owned stock valued, financially posted, or stepped to SOLD / PURCHASED |
| `INV-STP-001` | 422 | Status move not allowed: backward or across directions under STEP, a named command that does not apply at the current status, or a registration at a financial status or one that does not match the quantity's sign |
| `INV-STP-002` | 409 | The origin already has a transaction (one transaction per origin) |
| `INV-TXN-001` | 404 | Transaction id not found in the tenant / legal-entity scope |
| `INV-REC-001` | 404 | Reconciliation run not found in the tenant / legal-entity scope |
| `idempotency_key_conflict` | 409 | The status-change Idempotency-Key is held by a change in a legal entity outside the caller's scope |
| `conflict_version` | 412 | `If-Match` does not match the journal, blocking or count sheet version, or `fromStatus` of a STEP is not the transaction's current status |
| `idempotency_key_reuse` | 422 | Idempotency-Key (or a line `clientKey`) reused with a different body |

Problems are RFC 9457 `application/problem+json` with `type` =
`https://passage.dev/problems/<code>`.

## Wave reference

- Wave 16 / PSG-16 slices 1 to 6. Dependencies: wave 6 (sites, warehouses, UoM), wave 8
  (products, dimension groups, item model groups), wave 11 (`Post(JournalEntry)`),
  wave 14 (period control), wave 4 (Temporal, outbox), wave 10 (migration framework).
- Slice 1 posts synchronously in the request. The Temporal `PostInventoryJournal`
  workflow, the wave 11 ledger call, and period control are later slices (see
  `NOT-IMPLEMENTED.md`).

## Dependencies

No dependency beyond the platform module and what it already pulls in (chi, pgx/v5,
shopspring/decimal, rapid for tests).

## Transfer model

A transfer order ships in full and receives in one or more receipts. Every movement of a
line (shipping warehouse to transit, transit to destination, or shipping warehouse to
destination for a one-step order or an allowed over-delivery) is a valued SOLD / PURCHASED
transaction pair with origin `TRANSFER_LINE` and the order id as reference, written in the
same transaction as the order update and the `transfer.shipped` / `transfer.received` event.
All on-hand rows a step touches are locked first in ascending (product, dimension set) order;
each issue is valued at its row's running average and the receipt carries that value, so
physical stock and value summed over the product never change and there is no GL entry.
