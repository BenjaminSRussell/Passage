DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON idempotency_keys TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON consolidation_run, consolidation_balance, consolidation_entry, consolidation_statement_line TO passage_app';
  END IF;
END
$$;
