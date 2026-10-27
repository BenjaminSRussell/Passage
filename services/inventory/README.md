# inventory

Passage inventory service. Wave 16 (PSG-16). Slice 1: the immutable inventory
ledger, the dimension hash, the on-hand projection, inventory journals
(MOVEMENT, ADJUSTMENT, OPENING_BALANCE) with synchronous posting, the negative
stock policy, serial uniqueness, the journal list and the on-hand inquiry grouped
by any dimension subset. Slice 2: reservations (MANUAL, FIFO, FEFO, LIFO),
blocking and quarantine, cycle counting with tolerance approval and blind count
(posted as COUNT journals), and the transaction inquiry. Slice 3: transfer orders
through an in-transit warehouse (ship, receive with tolerances and close-short,
cancel), the stock-in-transit inquiry and ABC classification by on-hand value. The
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
                          0004 transfer orders, 0005 ABC runs
                          (embedded, applied by dbx.Migrate)
api/openapi.yaml          OpenAPI 3.1 for every endpoint (parity is tested)
events/asyncapi.yaml      AsyncAPI 3 for every event published (payload parity is tested)
charts/inventory/         Helm chart (Deployment, Service, HPA, ServiceMonitor toggle)
```

## Endpoints and duties

| Method | Path | Duty | PRD duty |
|---|---|---|---|
| POST | `/v1/inventory/journals` | `inventory.journal.write` | InventoryJournalMaintain |
| GET | `/v1/inventory/journals` | `inventory.read` | InventoryInquire |
| GET | `/v1/inventory/journals/{id}` | `inventory.read` | InventoryInquire |
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
| POST | `/v1/inventory/blockings/{id}:release` | `inventory.blocking.write` | InventoryBlock |
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
stock back the same way; a quarantine needs an explicit disposition (`INV-BLK-001`),
and SCRAP / RETURN are not built yet (`INV-BLK-005`).

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
| `INV-JNL-004` | 409 | Journal is not in a state that allows the change (lines on a non-DRAFT journal, posting from another status) |
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
| `INV-BLK-005` | 422 | Disposition SCRAP or RETURN (not supported yet) |
| `INV-BLK-006` | 422 | Blocking is no longer active |
| `INV-CNT-001` | 403 | Count variance above tolerance and the caller lacks `inventory.count.approve` |
| `INV-CNT-003` | 422 | Count sheet is not OPEN |
| `INV-CNT-004` | 404 | Count plan, sheet or line not found |
| `INV-CNT-005` | 422 | Count sheet posted with no counted line |
| `conflict_version` | 412 | `If-Match` does not match the journal, blocking or count sheet version |
| `idempotency_key_reuse` | 422 | Idempotency-Key (or a line `clientKey`) reused with a different body |

Problems are RFC 9457 `application/problem+json` with `type` =
`https://passage.dev/problems/<code>`.

## Wave reference

- Wave 16 / PSG-16 slices 1 and 2. Dependencies: wave 6 (sites, warehouses, UoM), wave 8
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
