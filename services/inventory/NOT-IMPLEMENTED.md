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
tags per plan, reverse on the journal detail). Every other item in the ticket and PRD
3.16 is listed here.

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Transaction service: statuses per dimension (ordered/reserved/picked/deducted/received/posted) | partial — later slice of PSG-16 | Status model and all buckets are in `internal/domain`; posting writes `issue_status` / `receipt_status`. The status-transition command `POST /internal/v1/inventory/transactions:apply`, splitting (BR-16.2), `Unpick` / `Unregister` / `CancelReservation`, and the `inventory_transaction_status_change` audit table are not built. |
| 1 | On-hand projection by site/warehouse/location/batch/serial | built | `GET /v1/inventory/onhand` groups by product, legal entity and any dimension (site, warehouse, location, batch, serial, inventory_status, config, size, color, style). As-of-date replay is not built (later slice). |
| 2 | Journals: TRANSFER and BOM types | later slice of PSG-16 | Warehouse-to-warehouse moves are built as transfer orders (item 6); the TRANSFER journal type (paired lines with `to_dimension_set_id`, e.g. between locations or segments) is not. BOM needs the wave 9 BOM explosion. COUNT journals are built (slice 2) and are created only by count-sheet posting. |
| 2 | Journal approval workflow (SUBMIT / APPROVE / REJECT / CANCEL), `:validate`, Temporal `PostInventoryJournal` | later slice of PSG-16 | Slice 1 posts a DRAFT (or APPROVED) journal synchronously, skipping SUBMITTED / APPROVED; the BR-16.11 rules exist in `CanMoveJournal` but there are no transition endpoints. The Temporal workflow owns POSTING and is not wired. |
| 2 | Posting calls the wave 11 `Post(JournalEntry)` ledger API | handoff — wave 11 (ledger) | Ledger Post does not exist yet. The balanced GL entry is built in-process, stored on the journal (`gl_entry`) and carried by `journal.posted`; `ledger_entry_id` stays null. |
| 2 | Posting profiles per legal entity / item group | handoff — wave 11 (posting profiles) | Slice 1 reads one inventory, adjustment and opening-clearing account from configuration (`INV_*_ACCOUNT`). |
| 2 | Period control for module INVENTORY (AC-16.7, INV-PER-001) | handoff — wave 14 (period control) | Period service not available; posting does not check the financial date against periods. |
| 2 | Reversal of a posted journal (BR-16.12) | built (slice 4) | `POST /journals/{id}:reverse` (journal write duty) creates the DRAFT reversal: same type, legal entity, offset account and currency, one negated line per original line (`reversesLineId`), no lines can be added. A journal has one reversal (a repeat returns it, 200 + `Idempotent-Replayed`). It is posted with the normal `:post` (post duty, `If-Match`, negative policy): each line takes the negated value of the transaction it corrects (not the running average), its transaction carries `corrects_transaction_id`, and `journal.posted` carries `reversesJournalId`. The original journal and its transactions are never changed. Store tests: `TestReversalMirrorsTheOriginalAtItsValues`, `TestReversalRespectsTheNegativePolicy`. Cancelling an unposted reversal draft is not built (it stays as the original's one reversal). |
| 2 | Journal number sequence `INVJ-` | handoff — wave 5 (numbering) | Journals are identified by id; the number column and sequence call arrive with the numbering client. |
| 2 | CSV import, paste from spreadsheet, copy journal | later slice of PSG-16 | Web grid features; the slice 1 screen adds one line at a time. |
| 3 | Reservations: MANUAL, FIFO, FEFO, LIFO against on-hand (RESERVED_PHYSICAL), over-reservation rejected, partial with shortfall, `POST/GET/DELETE /reservations` | built (slice 2) | Every on-hand row of the product in the legal entity is locked before allocation, so reservations never over-reserve (`TestConcurrentReservationsNeverOverReserve`). |
| 3 | Reservations against expected receipts (RESERVED_ORDERED) | later slice of PSG-16 | Needs the ordered / arrived buckets, which only receiving and purchase orders (waves 17–18) fill through `transactions:apply`. |
| 3 | Reservations as RESERVED_PHYSICAL issue transactions, CONSUMED status | handoff — waves 21 / 27 (production, sales) | A reservation is a record that moves the `reserved_physical` bucket; the projection's bucket equals the sum of ACTIVE reservations (reconciliation test). The demand documents that would own an issue transaction, and consume the reservation on issue, belong to the sales and production waves. |
| 3 | Reservation TTL (`reservation_ttl_hours`, `expires_at`, 5-minute expiry job) | later slice of PSG-16 | No scheduler is wired (see the Temporal jobs row). Reservations hold until released. |
| 3 | `reservation_policy_default` per legal entity, `shelf_life_margin_days` per product tracking group | handoff — wave 6 / wave 8 parameters | The request carries the policy (default FIFO) and the margin (default 0). |
| 3 | Batch expiry master data | handoff — wave 8 (batches) | `PUT /batch-expiries/{productId}/{batchId}` keeps a local copy that FEFO reads; it is replaced by the wave 8 batch event feed. No screen: the batch master screens are wave 8's. |
| 3 | Blocking and quarantine: block a quantity, batch or serial with a reason, release with RELEASE | built (slice 2) | `POST /blockings`, `GET /blockings`, `POST /blockings/{id}:release`. A blocking is a valued SOLD / PURCHASED pair from the unrestricted dimension set to the same set with `inventory_status` BLOCKED or QUARANTINE (no GL effect: both legs are on the inventory account). |
| 3 | Dispositions SCRAP and RETURN, QUARANTINE → BLOCKED transition, `inventory_status_transitions` matrix, expected release date | later slice of PSG-16 | SCRAP needs a loss posting and RETURN the vendor return (wave 18); both answer `INV-BLK-005`. Until then: release, then post an ADJUSTMENT. |
| 3 | Blocking of reserved stock, blocking in another warehouse than the source | not planned | Reserved stock must be released first (INV-BLK-003 counts only available stock). |
| 4 | Cycle counting: count plan, `:generate`, count entry, tolerance approval (AC-16.6), blind count, `:post` as a COUNT journal | built (slice 2) | Variances are measured against the book quantity at posting (BR-16.8). Approval is the `inventory.count.approve` duty of the poster (403 `INV-CNT-001` without it). |
| 4 | Count freeze (`count_lock`, `INV-CNT-002`) and the recount flag | later slice of PSG-16 | Movements are not blocked while a sheet is open; BR-16.8's posting-time variance keeps the result right but no recount is flagged. |
| 4 | Plan selection by ABC class, location range, last-count age, sample percent; plan frequency (cron) and the generation job | later slice of PSG-16 | A plan selects one legal entity and optionally one warehouse; sheets are generated on demand. ABC needs item 8. |
| 4 | Sheet statuses COUNTED and APPROVED, separate approval step, four-eyes (`CycleCountApprove` vs `CycleCountMaintain`) | later slice of PSG-16 / platform OPA policy | A sheet is OPEN until posted; approval happens at posting. Segregation of duties belongs to the OPA bundle. |
| 4 | Count sheet number from the wave 5 `CNT-` sequence | handoff — wave 5 (numbering) | `CNT-` plus 8 random hex digits from the sheet id, unique per tenant. |
| 4 | PWA count screen with offline queue | later slice of PSG-16 | The web count sheet works online only. |
| 5 | Unit conversions on movements (UoM table lookup, product unit decimals) | partial — handoff to wave 6/8 UoM service | The API accepts `qty` in the inventory unit plus `enteredQty` / `enteredUnit` (same sign required), stores both, and rejects more than 6 decimals (`INV-UOM-002`). Conversion lookup and the product's own unit decimals need the wave 6/8 service. |
| 5 | Inventory status values and transitions | partial (slice 2) | BLOCKED and QUARANTINE are restricted: left out of available stock in the on-hand inquiry (`blocked` column), of reservations and of FEFO candidates. Other values remain free dimension values; the status transition matrix is not built. |
| 6 | Transfer orders, in-transit warehouse, ship / receive, under/over-delivery, close-short | built (slice 3) | `POST` / `GET /transfer-orders`, `GET /transfer-orders/{id}`, `:ship`, `:receive`, `:cancel`. Two-step through a transit warehouse (`useTransit`, default) or one-step to the destination; optional receiving site (`toSiteId`). Each movement is a valued SOLD / PURCHASED pair (origin `TRANSFER_LINE`, reference = order id): the receipt carries the issue's running-average cost (BR-16.3), both legs are on the inventory account, so there is no GL entry (AC-16.11). Rows are locked in ascending order across all lines (BR-16.4); the shipping warehouse follows the negative policy, the transit warehouse never goes negative. Close-short within `underdeliveryPct` leaves the shortfall in transit (flagged in the inquiry); receipts beyond `overdeliveryPct` are `INV-TRF-002`; an allowed over-delivery is issued from the shipping warehouse. Cancel only before shipment (`INV-TRF-003`). Store tests: `TestTransferThroughTransitWithShortClose` (AC-16.11), `TestOneStepTransferAndOverDelivery`, `TestTransferShipRespectsAvailability`, `TestTransferIdempotencyCancelAndScope`. |
| 6 | Transfer legs at DEDUCTED / RECEIVED (physical) status | by design until the status command exists | PRD FR-16.15 names a DEDUCTED issue and a RECEIVED receipt. Without `transactions:apply` and a financial update, physical-only legs would leave the value at the shipping warehouse (its running average and posted value wrong, the receiving warehouse valued at zero). Legs post at SOLD / PURCHASED like AX's financially updated transfer shipment; the cost-equality and no-GL rules hold. Revisit with wave 20 costing. |
| 6 | Transfer order DRAFT status, header `delivery_terms` / `mode_of_delivery` / planned `receipt_date`, partial shipments, adding lines after creation, delete of CREATED orders | later slice of PSG-16 | Orders are created CREATED and ship in full; `ship_date` holds the planned date until shipped, then the actual one; `receipt_date` is set when the order is fully received. |
| 6 | ON_ORDER / ORDERED supply and demand pair at creation, and its cancellation | later slice of PSG-16 | Needs the status-transition command (item 1); planning (wave 22) reads it. Creation posts nothing yet. |
| 6 | Transit warehouse from warehouse master data (`transit_warehouse_id` per shipping warehouse, type TRANSIT), legal-entity `transfer_use_transit`, product tolerance defaults | handoff — wave 6 / wave 8 master data | The request carries `useTransit`, the transit warehouse and the per-line tolerances (default 0). |
| 6 | "The transit dimension never carries reservations" (BR-16.13) | handoff — wave 6 (warehouse type TRANSIT) | Policy reservations do not know which warehouses are transit warehouses until the warehouse type is readable; do not reserve from transit warehouse codes. |
| 6 | `transfer.release.v1` picking release event, batch ship / receive job, transfer number from the wave 5 sequence, shipping (`TRS-`) and receiving (`TRR-`) documents, four-eyes (`transfer_four_eyes`) | later slice of PSG-16 / handoff — waves 5, 15, 31 | Numbers are `TO-` plus 12 hex digits of the order id. Documents follow the wave 15 templates; segregation of duties belongs to the OPA bundle. |
| 6 | Cross-company transfers (pricing, intercompany invoice) | handoff — wave 40 | One legal entity per order. |
| 6 | In-transit stock inquiry (MB5T) | built (slice 3) | `GET /transfers/in-transit`: shipped, not yet received quantity by order line, oldest shipment first, days in transit, lines closed short flagged (`closedShort`). Value at from-cost is not shown (the transit on-hand row carries it in the on-hand inquiry). The printed list is a wave 15 document. |
| 7 | Ownership dimension, vendor consignment, `ConsignmentReplenishmentOrder`, OWNERSHIP_CHANGE journal, ownership batch, consignment reports | later slice of PSG-16 | `owner` is part of the hash contract but lines that set it are rejected until vendor-owned stock can be kept free of value (BR-16.14). Consignment price and accrual account need waves 17 and 11. |
| 7 | Customer consignment warehouse (`CUSTOMER_CONSIGNMENT`) | later slice of PSG-16 (documents in wave 27) | Warehouse type belongs to wave 6 master data. |
| 8 | Tag counting (`CountTag`, TAG- sequence, transfer to COUNT) | built (slice 4) | `POST /count-plans/{id}:generate-tags` (1 to 1000 tags, Idempotency-Key), `GET /count-plans/{id}/tags` (status filter: the missing / void list before transfer), `POST /count-tags/{id}:use` and `:void` (`If-Match`), `POST /count-plans/{id}:transfer-tags`. A tag is counted once (`INV-TAG-001`); zero is a count; the plan's warehouse is filled in and another warehouse refused. The transfer sums the untransferred USED tags per product x dimension set into a new OPEN count sheet (expected = book now), which is posted like any count sheet (COUNT journal, tolerance approval). Open tags block it (`INV-TAG-002`) unless `markOpenMissing`, which needs `inventory.count.approve` (`INV-TAG-006`). Store test: `TestTagCountingTransfersSummedTagsToACountSheet` (AC-16.13 at reduced scale). |
| 8 | Tag numbers from the wave 5 `TAG-` sequence, tag ranges per warehouse without a plan, a TAG_COUNT journal type, printed tag strips with barcodes (Code 128 / ZPL) | handoff — wave 5 (numbering), wave 15 (documents) | Numbers are `TAG-` plus six digits from a local per-legal-entity counter allocated under an advisory lock (unique per legal entity). A range always belongs to a count plan (the plan may name one warehouse). The tag entries themselves stand in for the TAG_COUNT journal; their transfer produces the COUNT sheet and journal. |
| 8 | ABC classification by VALUE (run, report) | built (slice 3) | `POST /abc-runs`, `GET /abc-runs`, `GET /abc-runs/{id}`: product x site ranked by posted on-hand value, thresholds 80 / 15 / 5 by default (must sum to 100, `INV-ABC-001`), an item whose cumulative share before it is at or below a boundary takes the higher class (BR-16.16), zero-value items in class C or N. Runs are immutable and replayable by Idempotency-Key. A product x site valued in two currencies is refused (`INV-CUR-001`). |
| 8 | ABC criteria REVENUE, MARGIN, CARRYING_COST and the lookback; the monthly job; the class stored on the product; the `abc_class[]` count-plan filter; `abc_*` legal-entity parameters | later slice of PSG-16 / handoff — waves 8, 20, 27 | Revenue and margin need sales (wave 27) and costing (wave 20); the product field belongs to wave 8. No scheduler is wired (see the Temporal jobs row). The run takes its thresholds and zero class in the request. |
| 8 | Segment dimension (SALES_ORDER / PROJECT) and segment-aware reservation | later slice of PSG-16 | `segment` is not yet a dimension name. Adding it later is safe for existing keys: absent dimensions do not contribute to the canonical form, so current hashes do not change. |

## PRD items (section 3.16) not built in slice 1

| Item | Status | Reason |
|---|---|---|
| FR-16.1 status-change audit rows | later slice of PSG-16 | No transaction is changed after insert; the trigger already refuses changes to quantity and identity and any delete. Corrections via `corrects_transaction_id` are built for journal reversals (slice 4). |
| FR-16.2 backward-step commands (Unpick, Unregister, CancelReservation) | later slice of PSG-16 | Depends on the status transition command. |
| FR-16.4 negative policy per item model group (`negative_physical_allowed`, `negative_financial_allowed`) | handoff — wave 8 (item model groups) | Slice 1 applies one tenant-wide policy from `INV_NEGATIVE_DEFAULT` (default forbid). |
| FR-16.4 `INV-LCK-001` lock timeout with 200 ms backoff | later slice of PSG-16 | Lock waits follow the request context (30 s router timeout); there is no bounded `lock_timeout` yet. |
| FR-16.4 negative rows on the workspace tile and in the daily reconciliation | later slice of PSG-16 | The on-hand inquiry flags any group containing a negative row (`negative`); the tile and reconciliation are not built. |
| FR-16.4 negative policy on blocking legs | by design | Blocking and release always forbid negative stock at their source, whatever the item policy: they move existing stock only. |
| FR-16.5 warehouse mandatory per storage dimension group; active dimensions per product dimension groups | handoff — wave 8 (dimension groups) | Slice 1 enforces site on every line (`INV-DIM-001`) and accepts any known dimension; the per-product groups come from wave 8. |
| FR-16.6 saga compensation for ledger failures | later slice of PSG-16 | No ledger call yet (see item 2). |
| FR-16.7 batch / serial selection in the reservation dialog (pick specific batches from a candidate list) | later slice of PSG-16 | MANUAL reserves one named dimension set (batch and serial included); the dialog does not list candidates. |
| FR-16.9 count freeze and recount flag | later slice of PSG-16 | See item 4. |
| FR-16.11 physical vs financial date split and BR-16.6 backdating | partial | `physical_date` and `financial_date` are stored (both the posting date for journals). Backdating rule and period check need wave 14. |
| FR-16.12 daily reconciliation job and `reconciliation.diff.v1` | later slice of PSG-16 | The reconciliation itself is a test (`TestProjectionReconcilesWithTransactionsAndLedger`: projection = transactions, inventory value = GL inventory net); the scheduled job, the ledger balance read and the event are not built. |
| FR-16.13 transaction inquiry `GET /transactions` | built (slice 2) | Filters product, status, origin type and physical date range; newest first with cursor paging; drill-down to the journal of a journal line. |
| FR-16.13 export and GL voucher drill-down | later slice of PSG-16 / handoff — wave 11 | Export is a web grid feature (wave 5 shell); `ledger_entry_id` stays null until the ledger Post exists. |
| FR-16.14 opening balance load from AX / SAP extracts via wave 10 migration framework | handoff — wave 10 (migration framework) | Mapping rules belong to the migration pack; the OPENING_BALANCE journal it targets is built. |
| FR-16.16 in-transit inquiry | built (slice 3) | `GET /transfers/in-transit` (see item 6). |
| FR-16.15 transfer order lines inquiry and transfer order overview report (3.16.14) | partial (slice 3) | The transfer order list shows status and received progress, the order shows ordered / shipped / received / in transit per line. A cross-order line inquiry with filters and the late-shipment overview are a later slice. |
| FR-16.18 to FR-16.20 tag counting, ABC, segments | partial (slices 3, 4) | ABC classification by value (slice 3) and tag counting (slice 4) are built (item 8); segments are a later slice of PSG-16. |
| 3.16.1 valuation by product x site per item model group (running average across dimension sets) | handoff — wave 20 (costing) | Slice 1 values issues at the running average of the on-hand row being issued (one dimension set), the finest consistent level under the row lock. Wave 20 replaces it with the costing engine. |
| 3.16.5 `POST /internal/v1/inventory/transactions:apply`, `POST /journals/{id}:validate` | later slice of PSG-16 | Listed in the endpoint table; the reservation, blocking, count and transaction endpoints are built (slice 2). `POST /internal/v1/inventory/onhand:rebuild` is built (slice 4). |
| 3.16.5 `onhand:rebuild` returns a run id | by design | The rebuild runs synchronously in one request and returns its report (rows checked, rows differing, the first 100 differences by field, whether they were repaired). An asynchronous run with an id belongs to the scheduled job (see the 3.16.10 rebuild row). |
| 3.16.5 events `transfer.release`, `ownership.changed`, `reconciliation.diff` | later slice of PSG-16 | Not emitted yet. `transfer.shipped/received` (slice 3), `reservation.created/released` and `blocking.created/released` (added by this service, not in the PRD event table) are built. |
| 3.16.4 monthly range partitions on InventoryTransaction; partial index on open statuses | later slice of PSG-16 | Partitioning is triggered above 20M rows; open statuses do not occur until the status command exists. |
| 3.16.4 `license_plate` dimension | handoff — wave 31 (warehouse) | Part of the hash contract; lines that set it are rejected until wave 31 activates it. |
| 3.16.10 weekly rebuild-and-compare job (`inv-1m-txn`, < 10 min) | later slice of PSG-16 | The `onhand:rebuild` endpoint is built (slice 4): it recomputes every visible row from the transaction status sums (through the posting arithmetic) and the active reservations in one snapshot, and with `?repair=true` relocks, recomputes and overwrites each differing row in posting lock order (`TestRebuildComparesAndRepairsTheProjection`). The scheduled job and the 1M-transaction fixture are not built. |
| 3.16.10 Debezium connector `inventory-outbox`, CNPG cluster `inventory-db`, Argo CD app | handoff — deployment (`deploy/`) | Owned outside this wave's paths. The chart builds the Deployment, Service, HPA and an optional ServiceMonitor. |
| 3.16.10 Temporal cron jobs (reconciliation, count generation, TTL expiry, ABC, ownership batch, partition maintenance, draft cleanup) | later slice of PSG-16 | No scheduler wired. The chart passes `INV_RECON_CRON` and `INV_RESERVATION_TTL_HOURS`, which the service does not read yet. |
| 3.16.10 feature flags `inv.fefo_policy`, `inv.count_pwa`, `inv.transfer_cost_diff` | handoff — platform flag service | No flag client in the kernel yet. |
| 3.16.11 AX / SAP migration of opening balances and history | handoff — wave 10 | See FR-16.14. |
| 3.16.13 parameters (`journal_names`, posting restriction, etc.) | later slice of PSG-16 | Slice 1 reads the line cap from `INV_JOURNAL_MAX_LINES`; journal names and the other setup tables are not built. |
| 3.16.14 count variance report (variance value, by counter), negative list, transit, consignment, segment, batch / serial trace, dimension combinations | later slice of PSG-16 | The reservation, blocking and transaction inquiries are built (slice 2); the count sheet shows posted variances per line; the tag status / missing tags list is built (slice 4, `GET /count-plans/{id}/tags`). |
| 3.16.15 printed documents (posting list, count sheets, transfer shipping / receiving, labels, blocking notice) | later slice of PSG-16 | Document layouts follow the wave 5/15 templates and are not built. |
| 3.16.17 duties ConsignmentMaintain, InventoryAdmin, InventorySetupMaintain | later slice of PSG-16 (per feature) | Built: `inventory.read`, `inventory.journal.write`, `inventory.journal.post`, `inventory.reservation.write` (InventoryReserve), `inventory.blocking.write` (InventoryBlock), `inventory.count.write` / `.post` / `.approve` (CycleCountMaintain / Post / Approve), `inventory.transfer.write` / `.post` (TransferOrderMaintain / TransferOrderPost, slice 3), `inventory.abc.write` (the ABC part of InventorySetupMaintain, slice 3), `inventory.admin` (the rebuild part of InventoryAdmin, slice 4), and `inventory.batch.write` for the interim batch-expiry input. Tag counting uses the cycle-count duties (marking open tags MISSING is `inventory.count.approve`); reversal uses the journal write / post duties. Segregation-of-duties pairs belong to the OPA policy bundle (platform). |
| 3.16.19 performance budgets (k6 at 10k concurrent issues, p95 SLOs) | later slice of PSG-16 | The store test runs 300 concurrent issues against 100 units (AC1 at reduced scale). The 10k k6 run needs a load environment. |

## Ticket acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 concurrency: 10k issues never drive on-hand negative | partial | `TestConcurrentIssuesNeverGoNegative` runs 300 concurrent issues against 100 units under RLS (exactly 100 succeed). The 10k k6 run is a later slice. |
| AC2 projection equals SUM(transactions) for 1M transactions | partial | `TestRebuildComparesAndRepairsTheProjection` (slice 4: the rebuild finds no difference after journals and a reservation in two legal entities, then reports and repairs a drifted, a deleted and an orphan row, and a legal-entity scope sees only its rows), `TestProjectionReconcilesWithTransactionsAndLedger`, `TestProjectionReconcilesAfterSlice2Operations` and the slice 3 transfer tests (`reconcile`) (journals, blockings, reservations and a count; reserved_physical = active reservations) check every row of small datasets; `TestRebuildEqualsLiveProperty` checks rebuild = live in the domain. The 1M fixture and rebuild job are a later slice. |
| AC3 every posted journal produces a balanced GL entry, checked per journal type | built for MOVEMENT, ADJUSTMENT, OPENING_BALANCE, COUNT; transfers have none by design | `TestBuildEntry_BalancedPerJournalType` (rapid) plus the store tests (`TestCountSheetPostsVarianceWithApproval` checks the COUNT entry). TRANSFER / BOM are a later slice; the contract test against wave 11 is a handoff. |
| AC4 FEFO picking across 100 batches | built | `TestFEFO100Batches` selects across 100 batches in the domain; `TestFEFOReservationSpansBatchesInExpiryOrder` reserves through the store and API path (expiry order, blocked and margin-expiring batches skipped, shortfall returned). |
| AC5 every deliverable has code or a row here | built | This file. |
| AC6 `go vet` / `go test`, web `vitest` and `tsc`, OpenAPI / AsyncAPI match code, Helm lints | built | `go vet ./... && go test ./...`, `gofmt -l .`, `helm lint charts/inventory`, and in `apps/web` `npx vitest --run src/modules/inventory` and `npx tsc --noEmit`. Spec parity is tested (`TestOpenAPIDocumentsEveryOperation`, `TestPayloadsMatchAsyncAPISchemas`). |

## Test-plan items not built

| Item | Status | Reason |
|---|---|---|
| Status transition monotonicity (rapid) over `ApplyTransactionStep` | later slice of PSG-16 | The domain `Step` / `CanStep` rules are tested; the command does not exist yet. |
| Outbox event schema compatibility (BACKWARD) against the registry | handoff — platform schema registry | Registry service is owned by the platform / eventing wave. Payload-to-AsyncAPI parity is tested locally. |
| Period control contract test | handoff — wave 14 | See above. |
| PWA offline count E2E (Playwright) | later slice of PSG-16 | Count PWA is not built. |
| Fixtures `inv-demo-plant`, `inv-1m-txn`, `inv-fefo-100`, `inv-ax-extract`, `inv-sap-extract` under `testdata/` | later slice of PSG-16 | Generators (`scripts/fixtures/inv_txn_gen.py`) and extracts are not in this slice; the FEFO test builds its 100 batches in code. |

## Web module (`apps/web/src/modules/inventory`)

| Item | Status | Reason |
|---|---|---|
| On-hand inquiry, journal list, new journal, journal detail (add line, post) | built | Shell primitives, CSS Modules, loading / empty / error states, vitest tests. |
| Transactions, reservations (list, release, new reservation), blocking (list, release, block stock), count plans (list, generate sheet, new plan), count sheets (list, sheet with count entry and post) | built (slice 2) | Same primitives and states; vitest tests in `slice2.test.tsx`. |
| Transfer orders (list by status, new order with several lines, order with ship / receive / close-short / cancel), stock in transit, ABC runs (list, run, report) | built (slice 3) | Same primitives and states; vitest tests in `slice3.test.tsx`. Transfer transactions link to their order from the transaction inquiry. |
| Count tags per plan (list with status filter, generate a range, count or void a tag, transfer to a count sheet with the supervisor's mark-missing choice); reverse on the journal detail (posting date, opens the reversal draft, links between a journal and its reversal) | built (slice 4) | Same primitives and states; vitest tests in `slice4.test.tsx`. The rebuild is an internal endpoint (InventoryAdmin) the BFF does not expose, so it has no screen. |
| Printing tag strips | handoff — wave 15 (documents) | The tag list shows the numbers to print. |
| Warehouse pickers on the transfer form, transfer documents (print), batch / serial selection at receipt | later slice of PSG-16 / handoff — wave 6 | The form takes warehouse and site codes; the receipt keeps the shipped dimensions. |
| Inventory management workspace tiles (journals awaiting approval, negative on-hand, open count sheets, reconciliation diffs) | later slice of PSG-16 | Depend on journal approval and reconciliation. |
| Quarantine disposition choice in the release action | later slice of PSG-16 | The screen releases with RELEASE, the only disposition the service supports yet. |
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
  The shared `idempotency_keys` table is not used.
- Money: cost amounts, cost prices and `posted_value` are `numeric(20,6)` with a currency
  column, values rounded to the currency minor unit with `platform/money`.
