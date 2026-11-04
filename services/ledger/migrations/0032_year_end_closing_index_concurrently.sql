-- dbx:no-transaction
-- Built CONCURRENTLY (moved out of 0028) so a large ledger keeps taking journal_entry writes during the build.
-- A database that ran the old 0028 already has a valid index and this is a no-op. A concurrent build that failed
-- leaves an INVALID index that IF NOT EXISTS would keep; drop only such a leftover first.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
             WHERE c.relname = 'journal_entry_year_end_closing_ix' AND NOT i.indisvalid) THEN
    EXECUTE 'DROP INDEX journal_entry_year_end_closing_ix';
  END IF;
END
$$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS journal_entry_year_end_closing_ix ON journal_entry (tenant_id, ledger_id)
  WHERE posted_by_service = 'close' AND source_type = 'year_end_closing';
