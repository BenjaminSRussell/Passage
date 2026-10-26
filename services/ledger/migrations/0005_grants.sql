-- Application role privileges (FR-11.6). passage_app (no BYPASSRLS) gets INSERT and
-- SELECT only on the journal tables, so posted entries cannot be changed even before
-- the append-only triggers fire. Dimension combinations are immutable too. Skipped
-- when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON outbox, idempotency_keys, main_account, ledger, balance TO passage_app';
    EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE ON journal_entry, journal_line, dimension_combination FROM passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON journal_entry, journal_line, dimension_combination TO passage_app';
  END IF;
END
$$;
