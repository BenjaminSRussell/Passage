-- Application role privileges. passage_app (no BYPASSRLS) works through the RLS
-- policies; it never owns the tables. recognition_run is append-only, the outbox is
-- insert-only (it is excluded from RLS and only the relay reads it), and nothing is
-- deleted. Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON revenue_contract, performance_obligation, revenue_schedule TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON recognition_run TO passage_app';
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
  END IF;
END
$$;
