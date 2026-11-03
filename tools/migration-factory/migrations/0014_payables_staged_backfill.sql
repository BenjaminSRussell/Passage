-- Migration factory, wave 49 slice 11 (H-S8-3): backfill of pack_run.payables_staged (0012) for the
-- runs stored before it, from what the stored load proves: the target_row rows of the run with ledger
-- AP that the payables service could not take (NOT_AVAILABLE). A run with no such row (no load, or
-- receivables only) gets 0.
--
-- The statement reads every tenant and legal entity, so it needs the row-level security bypass of the
-- migration role (target_row is also held to the caller's legal-entity scopes). A role without it
-- would see no rows: the backfill is then skipped with a notice and the runs stay NULL, which the
-- go/no-go reads as staged for open_items until the pack is re-run (a conservative default: never a
-- GO on payables lines nobody counted). The UPDATE takes ROW EXCLUSIVE on pack_run and the row locks
-- of the runs it sets, so pack runs and reads go on meanwhile; the lock_timeout bounds the wait for
-- that lock and for the SHARE UPDATE EXCLUSIVE of the VALIDATE, which scans pack_run without blocking
-- reads or writes.
SET LOCAL lock_timeout = '5s';

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) THEN
    RAISE NOTICE 'pack_run.payables_staged backfill skipped: role % does not bypass row-level security', current_user;
    RETURN;
  END IF;
  UPDATE pack_run p SET payables_staged = (
    SELECT count(*) FROM target_row r
    WHERE r.tenant_id = p.tenant_id AND r.run_id = p.run_id AND r.ledger = 'AP' AND r.status = 'NOT_AVAILABLE')
  WHERE p.payables_staged IS NULL;
END
$$;

alter table pack_run validate constraint pack_run_payables_staged_check;
