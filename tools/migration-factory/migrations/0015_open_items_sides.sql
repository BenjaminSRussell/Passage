-- Migration factory, wave 49 slice 12 (decided items S11-a and S11-b): the catalog half of two changes.
-- The data half is 0016.
--
-- (a) open_items keys its legacy_xref rows by side. An AX CustTransOpen and a VendTransOpen RecId (SAP
-- BSID and BSIK keys) with the same value are two different open items, but until now both were held
-- under source table open_items and collided. A receivable is now held under open_items.receivable and
-- a payable under open_items.payable, as slice 11 keyed open_orders.sales and open_orders.purchase.
-- Nothing in the schema changes for that: source_table is free text. Two things have to:
--   * legacy_xref is append-only, and its trigger refuses every update but the one-time confirmation of
--     the target id. 0016 lets it move a row from the bare open_items to one of the two side tables for
--     the length of its own transaction and puts the 0006 trigger back before it commits, so the rows
--     stay write-once. (The application role holds UPDATE on passage_id and target_confirmed only, 0006.)
--   * target_row held one row per (run, legacy id); a run that carries a receivable and a payable with
--     the same legacy id needs one per (run, ledger, legacy id).
--
-- (b) pack_run gets receivables_load, the status of the run's load into receivables (target_load.status),
-- NULL when the run has no load. The go/no-go reads it as a cutover manager of any legal-entity scope;
-- target_load is held to the caller's scopes, so a narrow scope reading target_load would see "no load"
-- and let a failed one pass (the reason payables_staged lives on pack_run too, 0012). The new column is
-- nullable without a default and the check is NOT VALID: catalog-only changes, validated by 0016 after
-- the backfill.
--
-- The ALTERs take ACCESS EXCLUSIVE (or SHARE for the unique index, which scans target_row) for an
-- instant: the lock_timeout makes the migration fail and the rollout retry rather than queue behind a
-- long transaction while every later query on the table queues behind it.
SET LOCAL lock_timeout = '5s';

alter table pack_run add column receivables_load text;
alter table pack_run add constraint pack_run_receivables_load_check
  check (receivables_load in ('STAGED', 'LOADING', 'PARTIAL', 'UNRECONCILED', 'MISMATCH', 'RECONCILED', 'FAILED')) not valid;

create unique index target_row_run_ledger_legacy on target_row (tenant_id, run_id, ledger, legacy_id);
alter table target_row drop constraint target_row_tenant_id_run_id_legacy_id_key;
