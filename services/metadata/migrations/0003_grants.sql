-- Application role privileges. passage_app (no BYPASSRLS) reads and writes the
-- registry tables and the outbox under their RLS policies. Skipped when the role
-- does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON metadata_entity_version, metadata_extension, metadata_record, metadata_decision_table, metadata_usage_daily, outbox TO passage_app';
  END IF;
END
$$;
