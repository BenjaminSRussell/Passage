DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON tax_transaction, tax_recommit_queue TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON tax_settlement_run, outbox TO passage_app';
  END IF;
END
$$;
