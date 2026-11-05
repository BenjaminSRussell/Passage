-- dbx:no-transaction
-- Built CONCURRENTLY so inventory keeps registering transactions during the
-- build. A concurrent build that failed leaves an INVALID index that IF NOT
-- EXISTS would keep; drop only such a leftover first.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
             JOIN pg_namespace n ON n.oid = c.relnamespace
             WHERE c.relname = 'inventory_transaction_origin_line_ix' AND n.nspname = current_schema() AND NOT i.indisvalid) THEN
    EXECUTE 'DROP INDEX inventory_transaction_origin_line_ix';
  END IF;
END
$$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS inventory_transaction_origin_line_ix
  ON inventory_transaction_origin (tenant_id, source_type, line_source_id) WHERE line_source_id IS NOT NULL;
