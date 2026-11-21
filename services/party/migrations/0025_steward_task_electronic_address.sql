-- B-43a: electronic addresses on a party (a duplicate blocking key) and the stewardship queue of overridden duplicate checks.
CREATE TABLE IF NOT EXISTS party_electronic_address (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  party_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('email', 'phone', 'url')),
  value text NOT NULL CHECK (char_length(value) BETWEEN 1 AND 2048),
  normalized text NOT NULL CHECK (char_length(normalized) BETWEEN 1 AND 2048),
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, party_id, legal_entity_id, kind, normalized),
  FOREIGN KEY (tenant_id, party_id) REFERENCES party (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS party_electronic_address_normalized ON party_electronic_address (tenant_id, normalized);

CREATE TABLE IF NOT EXISTS steward_task (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  party_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  role text NOT NULL CHECK (role IN ('vendor', 'customer')),
  candidates jsonb NOT NULL DEFAULT '[]'::jsonb,
  override_reason text NOT NULL CHECK (char_length(override_reason) BETWEEN 1 AND 500),
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
  resolution text CHECK (resolution IN ('distinct', 'duplicate')),
  resolution_reason text CHECK (char_length(resolution_reason) BETWEEN 1 AND 500),
  requested_by text NOT NULL DEFAULT '',
  resolved_by text CHECK (resolved_by IS NULL OR resolved_by <> ''),
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, party_id) REFERENCES party (tenant_id, id),
  CHECK ((status = 'open') = (resolution IS NULL)),
  CHECK ((status = 'open') = (resolved_by IS NULL)),
  CHECK ((status = 'open') = (resolved_at IS NULL))
);

CREATE INDEX IF NOT EXISTS steward_task_status ON steward_task (tenant_id, status, created_at, id);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['party_electronic_address', 'steward_task'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_access', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS PERMISSIVE
      USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid)$p$, t || '_tenant_access', t);
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_legal_entity_scope', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS RESTRICTIVE
      USING (current_setting('depot.legal_entity_scopes', true) = '*'
             OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')))$p$, t || '_legal_entity_scope', t);
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON party_electronic_address TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON steward_task TO depot_app';
  END IF;
END
$$;
