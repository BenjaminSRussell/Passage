-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies. The outbox is insert-only (Debezium relays it).
-- metric_reconciliation is upserted (ON CONFLICT DO UPDATE needs UPDATE) and never
-- deleted. Skipped when the role does not exist (developer databases without the
-- role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON metric_reconciliation TO passage_app';
  END IF;
END
$$;
