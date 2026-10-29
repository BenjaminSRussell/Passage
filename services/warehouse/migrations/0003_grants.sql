DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON dock_appointment, location_directive TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON dock_door, location, receipt, receipt_line, work, work_line TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON location_stock, idempotency_keys TO passage_app';
  END IF;
END
$$;
