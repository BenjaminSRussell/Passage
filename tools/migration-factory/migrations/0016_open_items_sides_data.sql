-- Migration factory, wave 49 slice 12 (S11-a, S11-b): the data half of 0015, on its own so the long
-- statements run apart from the catalog changes.
--
-- 1. Rekey the legacy_xref rows of the open_items pack by side. A row whose run staged a payables line
--    (target_row ledger AP) under that legacy id is a payable; every other row is a receivable (a row
--    that never went through a load has no ledger on record, and receivables is where the extract
--    defaulted it; a quarantined payable released before this migration is the one case it cannot tell
--    apart and keeps the receivable side). The old key was unique, so the new one is too.
-- 2. Backfill pack_run.receivables_load from target_load, then validate its check.
--
-- legacy_xref is append-only: its trigger (0006) refuses any change of source_table. For this transaction
-- only, the trigger also allows the one move of step 1 (from the bare open_items to a side, every other
-- column unchanged); the 0006 trigger is put back before the transaction commits, so no other session
-- ever sees the wider one and the rows stay write-once afterwards.
--
-- Both statements read every tenant and legal entity. A role that bypasses row-level security (the
-- deployed migration owner, and the superuser of the database tests) runs them as they are: the UPDATEs
-- take ROW EXCLUSIVE on the tables and row locks on the rows they set, so runs and reads go on meanwhile.
-- Any other role that got here owns the tables (0015's ALTERs need ownership), and an owner is held to the
-- policies only because the tables FORCE row-level security: for such a role the migration lifts FORCE on
-- the four tables, runs the statements and forces it again, all in this transaction. That takes ACCESS
-- EXCLUSIVE on the four tables until the commit (instant on an empty or small database), and it means the
-- rekey never runs half-blind: a role that saw no rows would leave open_items rows unkeyed, and the
-- factory would load them a second time. The lock_timeout bounds every lock wait, including the SHARE
-- UPDATE EXCLUSIVE of the VALIDATE.
SET LOCAL lock_timeout = '5s';

create or replace function legacy_xref_confirm_target() returns trigger language plpgsql as $$
begin
  -- 0016 only, replaced below before the commit: the move of an open_items row to its side.
  if old.source_table = 'open_items' and new.source_table in ('open_items.receivable', 'open_items.payable')
     and (new.tenant_id, new.xref_id, new.source_system, new.source_company, new.source_id,
          new.passage_entity, new.passage_id, new.run_id, new.amount, new.currency, new.created_at, new.target_confirmed)
         is not distinct from
         (old.tenant_id, old.xref_id, old.source_system, old.source_company, old.source_id,
          old.passage_entity, old.passage_id, old.run_id, old.amount, old.currency, old.created_at, old.target_confirmed) then
    return new;
  end if;
  if old.target_confirmed or not new.target_confirmed
     or (new.tenant_id, new.xref_id, new.source_system, new.source_company, new.source_table, new.source_id,
         new.passage_entity, new.run_id, new.amount, new.currency, new.created_at)
        is distinct from
        (old.tenant_id, old.xref_id, old.source_system, old.source_company, old.source_table, old.source_id,
         old.passage_entity, old.run_id, old.amount, old.currency, old.created_at) then
    raise exception 'legacy_xref is append-only: only the one-time confirmation of the target id may update a row';
  end if;
  return new;
end
$$;

DO $$
DECLARE
  bypass boolean;
  t text;
BEGIN
  SELECT rolsuper OR rolbypassrls INTO bypass FROM pg_roles WHERE rolname = current_user;
  bypass := coalesce(bypass, false);
  IF NOT bypass THEN
    FOREACH t IN ARRAY ARRAY['legacy_xref', 'target_row', 'pack_run', 'target_load'] LOOP
      EXECUTE format('alter table %I no force row level security', t);
    END LOOP;
  END IF;
  UPDATE legacy_xref x SET source_table = 'open_items.payable'
  WHERE x.source_table = 'open_items'
    AND EXISTS (SELECT 1 FROM target_row r
                WHERE r.tenant_id = x.tenant_id AND r.run_id = x.run_id AND r.legacy_id = x.source_id AND r.ledger = 'AP');
  UPDATE legacy_xref SET source_table = 'open_items.receivable' WHERE source_table = 'open_items';
  UPDATE pack_run p SET receivables_load = l.status
  FROM target_load l
  WHERE l.tenant_id = p.tenant_id AND l.run_id = p.run_id AND p.receivables_load IS NULL;
  IF NOT bypass THEN
    FOREACH t IN ARRAY ARRAY['legacy_xref', 'target_row', 'pack_run', 'target_load'] LOOP
      EXECUTE format('alter table %I force row level security', t);
    END LOOP;
  END IF;
END
$$;

-- The 0006 trigger again: only the one-time confirmation of the target id may update a row.
create or replace function legacy_xref_confirm_target() returns trigger language plpgsql as $$
begin
  if old.target_confirmed or not new.target_confirmed
     or (new.tenant_id, new.xref_id, new.source_system, new.source_company, new.source_table, new.source_id,
         new.passage_entity, new.run_id, new.amount, new.currency, new.created_at)
        is distinct from
        (old.tenant_id, old.xref_id, old.source_system, old.source_company, old.source_table, old.source_id,
         old.passage_entity, old.run_id, old.amount, old.currency, old.created_at) then
    raise exception 'legacy_xref is append-only: only the one-time confirmation of the target id may update a row';
  end if;
  return new;
end
$$;

alter table pack_run validate constraint pack_run_receivables_load_check;
