-- Least-privilege grants for the application role (passage_app, NOBYPASSRLS).
-- sod_mitigation and outbox are append-only; idempotency_keys is claimed then completed.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON sod_mitigation TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON sod_conflict, idempotency_keys TO passage_app';
  END IF;
END
$$;
