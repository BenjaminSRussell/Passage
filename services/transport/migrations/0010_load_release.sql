-- Load release for tendering (FR-34.5, #426): one row per release, written with
-- its dev.passage.tms.load.released.v1 outbox event; workflow-ts starts the
-- tender cascade from the event. released_by and legal_entity_scope are the
-- verified caller of the release, never request input. legal_entity_id is the
-- load's, copied so the release follows the load's legal-entity scope directly.
CREATE TABLE IF NOT EXISTS load_release (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  load_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  carriers jsonb NOT NULL CHECK (jsonb_typeof(carriers) = 'array' AND jsonb_array_length(carriers) BETWEEN 1 AND 20),
  currency text CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),
  timeout_min integer NOT NULL CHECK (timeout_min BETWEEN 5 AND 1440),
  released_by text NOT NULL CHECK (released_by <> ''),
  legal_entity_scope uuid[] NOT NULL CHECK (legal_entity_id = ANY (legal_entity_scope)),
  released_at timestamptz NOT NULL,
  idempotency_key text,
  request_hash text,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, load_id) REFERENCES load (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS load_release_idempotency ON load_release (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS load_release_load ON load_release (tenant_id, load_id, released_at DESC);
ALTER TABLE load_release ENABLE ROW LEVEL SECURITY;
ALTER TABLE load_release FORCE ROW LEVEL SECURITY;
CREATE POLICY load_release_tenant_access ON load_release AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY load_release_legal_entity_scope ON load_release AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
  WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Releases are written once.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON load_release TO passage_app';
  END IF;
END
$$;
