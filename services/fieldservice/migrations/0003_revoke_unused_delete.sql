-- The store never deletes from any fieldservice table, so passage_app loses DELETE on
-- service_number_counter and service_work_order. No table keeps DELETE. Skipped when
-- the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'REVOKE DELETE ON service_number_counter, service_work_order FROM passage_app';
  END IF;
END
$$;
