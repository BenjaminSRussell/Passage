-- PHI read-access log (#606, HIPAA audit controls). One row per read of a record that returns
-- protected health information: who read it, which record and fields, when, and why (purpose).
-- Like audit_entry it is append-only and hash-chained per tenant, so a missing or edited row breaks
-- the chain and an auditor can prove the export is complete. user_id is the verified caller, never
-- a request value; source_service is the service that recorded the read on the caller's behalf.
CREATE TABLE IF NOT EXISTS phi_read (
  tenant_id uuid NOT NULL,
  seq bigserial NOT NULL,
  id uuid NOT NULL,
  entity text NOT NULL CHECK (length(entity) BETWEEN 1 AND 128),
  record_id text NOT NULL CHECK (length(record_id) BETWEEN 1 AND 128),
  fields text[] NOT NULL CHECK (cardinality(fields) BETWEEN 1 AND 64),
  purpose text NOT NULL CHECK (length(btrim(purpose)) BETWEEN 1 AND 500),
  user_id text NOT NULL CHECK (length(user_id) BETWEEN 1 AND 256),
  source_service text NOT NULL DEFAULT '',
  trace_id text NOT NULL CHECK (length(trace_id) BETWEEN 1 AND 128),
  occurred_at timestamptz NOT NULL,
  prev_hash char(64) NOT NULL,
  hash char(64) NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, seq),
  UNIQUE (tenant_id, hash)
);
ALTER TABLE phi_read ENABLE ROW LEVEL SECURITY;
ALTER TABLE phi_read FORCE ROW LEVEL SECURITY;
CREATE POLICY phi_read_tenant_access ON phi_read AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE INDEX IF NOT EXISTS phi_read_record ON phi_read (tenant_id, entity, record_id, seq);
CREATE INDEX IF NOT EXISTS phi_read_user ON phi_read (tenant_id, user_id, seq);
CREATE INDEX IF NOT EXISTS phi_read_time ON phi_read (tenant_id, occurred_at);

CREATE FUNCTION phi_read_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'phi_read is append-only';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER phi_read_no_mutation
  BEFORE UPDATE OR DELETE ON phi_read
  FOR EACH ROW EXECUTE PROCEDURE phi_read_immutable();
CREATE TRIGGER phi_read_no_truncate
  BEFORE TRUNCATE ON phi_read
  FOR EACH STATEMENT EXECUTE PROCEDURE phi_read_immutable();

-- passage_app appends and reads; it never updates, deletes or truncates. Skipped when the role does
-- not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON phi_read TO passage_app';
    EXECUTE 'GRANT USAGE ON SEQUENCE phi_read_seq_seq TO passage_app';
  END IF;
END
$$;
