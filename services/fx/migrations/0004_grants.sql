DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON exchange_rate_type, provider_import_run TO passage_app';
    EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE ON exchange_rate, idempotency_keys FROM passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON exchange_rate, idempotency_keys TO passage_app';
    EXECUTE 'GRANT UPDATE (superseded_by) ON exchange_rate TO passage_app';
  END IF;
END
$$;
