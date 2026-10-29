DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON print_device, print_routing_rule TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON print_job TO passage_app';
  END IF;
END
$$;
