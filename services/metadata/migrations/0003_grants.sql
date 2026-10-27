-- Application role privileges. passage_app (no BYPASSRLS) reads and writes the
-- registry tables and the outbox under their RLS policies. Skipped when the role
-- does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    -- Published versions, extensions, decision tables and outbox rows are append-only.
    EXECUTE 'GRANT SELECT, INSERT ON metadata_entity_version, metadata_extension, metadata_decision_table, outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON metadata_record, metadata_usage_daily TO passage_app';
  END IF;
END
$$;
