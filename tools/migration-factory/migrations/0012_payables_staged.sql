-- Migration factory, wave 49 slice 11 (H-S8-3, CUT-1608): an open_items run whose payables lines
-- (ledger AP) the payables service cannot take yet is staged for them in the go/no-go. The count is
-- kept on the run, which is scoped by tenant only: the go/no-go reads it as a cutover manager of any
-- legal-entity scope, and target_row is held to the caller's legal-entity scopes, so counting there
-- would let a narrow scope read "nothing staged" and a GO pass. No new table: the grants of 0001 and
-- 0002 cover the new column (the load sets it with the UPDATE of 0002).
--
-- Runs stored before this migration keep NULL ("not counted"): a 0 would claim they hold no payables
-- lines, and an earlier open_items run with AP rows would then read as posted. The go/no-go treats a
-- NULL open_items run as staged until 0014 backfills it from target_row or the pack is re-run. New
-- runs take the default 0.
--
-- pack_run is live. A nullable column without a default, a default for new rows only and a NOT VALID
-- check are catalog-only changes (no rewrite, no scan; 0014 validates the check after the backfill),
-- but each ALTER takes ACCESS EXCLUSIVE for an instant: the lock_timeout makes the migration fail
-- (and the rollout retry) instead of queueing behind a long transaction while every later query on
-- the table queues behind it.
SET LOCAL lock_timeout = '5s';

alter table pack_run add column payables_staged int;
alter table pack_run alter column payables_staged set default 0;
alter table pack_run add constraint pack_run_payables_staged_check check (payables_staged >= 0) not valid;
