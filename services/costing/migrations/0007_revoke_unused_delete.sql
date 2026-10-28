-- The store never deletes from any costing table, so passage_app loses DELETE on
-- costing_version and item_cost. No table keeps DELETE. Skipped when the role does
-- not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'REVOKE DELETE ON costing_version, item_cost FROM passage_app';
  END IF;
END
$$;
