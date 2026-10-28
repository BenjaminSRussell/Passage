DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON revenue_contract, performance_obligation, revenue_schedule, recognition_run, outbox TO passage_app';
  END IF;
END
$$;
