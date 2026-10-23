# Inventory

## Purpose & Boundaries
Context `inventory` (Go only, database `passage_inventory`; waves 16, 20, 33, 39). Owns inventory transactions, on-hand projection, reservations, journals, counting, cost layers, costing versions/sheets, close, quality (orders, NCR/CAPA). Is the VALUATION OWNER: posts through `ledger.Post` for receipt valuation true-up, shipment COGS and RAF finished-goods valuation, triggered by events consumed as commands. Writes only its own tables (never receipt/shipment/production order rows).

## Interfaces
- Reads: `GET /v1/inventory/inventory-transactions` (read-only; created only by posted journals/consumers), `on-hand` (lock-free), `cost-layers`, `cost-closes/{id}`, `cost-variances`, `valuation-report?asOf=`, `supplier-scorecards`.
- Writes: `POST /reservations`, `DELETE /reservations/{id}`, `inventory-journals/{id}:post` (enforces negative-inventory policy -> `inventory_insufficient`), `counting-plans/{id}:generate` (202), `counting-journals/{id}:post`, `costing-versions/{id}:activate` (202), `cost-closes` (202), `cost-rolls` (202), `quality-orders/{id}/results`, `quality-orders/{id}:decide` (emits `qualityorder.decided.v1`), `nonconformances/{id}:escalate`, `corrective-actions/{id}:close`, `certificates-of-analysis:generate`.
- Valuation reservations: `POST /valuation-reservations/{id}:retry` (duty `Maintain inventory valuation`; optional `postingDate` -> key gets `:<revision>` suffix), `:cancel` (allowed only when source reversed, or `reverseSource=true` which is contested), `:override-negative` (duty `Approve negative inventory`; SoD with `Post inventory journals`) — R4-2 proposes DELETING this endpoint.
- `GET /v1/inventory/reconciliation/{check}` (value, quantity; lists open valuation reservations with age).

## Data Ownership
EN-037..045, 077: `inventory_transaction` (IL, monthly partition on transaction_date; PK (tenant_id, transaction_date, id); movement_id/step_no supersede model — status progresses by inserting new rows; direction receipt|issue; txn_type purchase|sales|production_input|production_output|transfer_out|transfer_in|adjustment|count|return_in|return_out|scrap|quality_move|co_product|by_product|opening|asset_maintenance|project; physical_status on_order..quotation; financial_status none|purchased|received_fin|sold_fin|deducted_fin|received_fin_posted|sold_posted; journal_entry_id + gl_seq set AT INSERT; idempotency_key), `transaction_request` (registry), `on_hand` (sync projection; PK (tenant_id, product_id, dimension_hash); CHECK allow_negative OR physical_on_hand - reserved_physical - blocked_qty >= 0; fillfactor 70), `reservation`, batch, serial, stock_block, inventory_journal(+line), count_task/line, `cost_layer` (immutable) + `cost_layer_balance` (only mutated field quantity_remaining), `cost_settlement`, `inventory_close`, costing_version, standard_cost, costing_sheet(+node), quality_order/result, nonconformance, `valuation_reservation` (EN-077; UQ (tenant_id, document_type, document_id, purpose); status reserved|finalized|rejected|cancelled|expired; gl_posting_status pending|posted|rejected|not_required; expires_at = created_at + 30 min; awaiting_reversal). Indexes IX-IN-01..36.

## Events Produced
`inventory.transaction.posted.v1` (48 partitions; includes journalEntryId, glSeq, reservationId; referenceType productreceipt|shipment|productionorder|journal|transfer|count|return), `inventory.transaction.rejected.v1` (reasonCode period_closed|inventory_insufficient|product_not_released|site_unknown|blocked|other, retryable, reservationId), `inventory.valuationreservation.cancelled.v1`, `.expired.v1`, `inventory.reservation.changed.v1`, `inventory.costclose.completed.v1`, `inventory.qualityorder.decided.v1`.

## Events Consumed
`inventory-receipts` (productreceipt.posted, reversed; G; posts valuation true-up key `post:inventory:productreceipt:<id>:valuation`), `inventory-shipments` (shipment.posted; COGS key `post:inventory:shipment:<id>:cogs`), `inventory-raf` (reportedasfinished, rafreversed; key `post:inventory:productionorder:<rafId>:fg-receipt`), `inventory-prodcost` (productionorder.ended), `inventory-prodorder` (released -> material reservations), `inventory-reservations` (salesorder.confirmed), `inventory-returns`, `inventory-quality-triggers` (productreceipt.posted, reportedasfinished, returnorder.received), `inventory-warehousework`, `inventory-costrollup` (bom.versionactivated), `inventory-reversal`, `inventory-partymerge`, `inventory-product-ref`, `inventory-periodcache`, `inventory-numbersequence`.

## Dependencies
internal: ledger (sync ledger.Post), product (ref), platform, procurement/sales/manufacturing (events), warehouse (location refs). external: none beyond kernel.

## Gotchas & Constraints
- Reserve-post-finalize (backend 6.4 rule 5): tx1 locks cost layers + on-hand, writes reservation `reserved`, adds issue qty to `on_hand.reserved_physical`; call `ledger.Post` OUTSIDE any tx; tx2 inserts inventory_transaction rows with journal_entry_id/gl_seq at insert, consumes layers, releases held qty, marks `finalized`, commits inbox row. On ledger rejection: reservation `rejected`, layers released, emit `transaction.rejected`; platform creates `valuation_rejection` task.
- Expiry sweeper `valuation-reservation-sweeper` runs every minute: if `ledger.posting_request` shows post exists -> finalize, never expire; else `expired`, release, emit expired event. Re-drive via `:retry` or after inbox cleared by replay-ticket procedure; row reused (never duplicated).
- Negative stock enforced at write (NFR-D4) by locking the single on_hand row `FOR UPDATE`; hot row ~2-5k updates/s (R-03).
- Hot write path index budget: inventory_transaction PK + IX-IN-01..04.
- FIFO consumption `FOR UPDATE SKIP LOCKED` via IX-IN-22. Costing: running costing; close is verification (S2). NFR-P8: 1M transactions close < 20 min; NFR-P9 cost roll 50k items < 15 min.
- Lines with `stocked=false` (drop-ship, services) produce no inventory transaction.
- `inventory_transaction` insert-only; carries `journal_entry_id` at insert; no `gl-link` endpoint.
- Unresolved: `shipment.reversed` event missing (R4-3); `:override-negative` contradicts NFR-D4 (R4-2); sweeper/finalize race (R4-4).

## Source Sections
- Backend 3.4 (329-344), 5.9 (682-710), 6.4 rule 5 (1097); Data 9.1 EN-037..045, EN-077 (2685-2857), 12.4 (3402-3404); Events 3.5 (4019-4029), 9.1-9.3 (4446-4584), consumer registry (4283-4300)
