-- Lookup tables (PSG-10 outbound value lookups): tenant tables of keyed values (enum codes, unit-of-measure conversion
-- factors, price lists) that an outbound mapping reads while it renders a legacy request. Like outbound mappings, every
-- edit writes an immutable version row and the header carries the current version (the optimistic version of the API).
CREATE TABLE migration.lookup_table (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  name text NOT NULL CHECK (name ~ '^[A-Za-z][A-Za-z0-9_.-]{0,63}$'),
  -- Copy of the current version's kind, for listing and filtering.
  kind text NOT NULL CHECK (kind IN ('enum', 'uom_conversion', 'price_list')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  entry_count integer NOT NULL DEFAULT 0 CHECK (entry_count >= 0),
  created_by text NOT NULL DEFAULT '',
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, name),
  UNIQUE (tenant_id, idempotency_key)
);

CREATE TABLE migration.lookup_table_version (
  tenant_id uuid NOT NULL,
  table_id uuid NOT NULL,
  version integer NOT NULL CHECK (version >= 1),
  definition jsonb NOT NULL CHECK (jsonb_typeof(definition) = 'object'),
  checksum text NOT NULL,
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, table_id, version),
  FOREIGN KEY (tenant_id, table_id) REFERENCES migration.lookup_table (tenant_id, id)
);

CREATE INDEX lookup_table_list_idx ON migration.lookup_table (tenant_id, name, id);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['lookup_table', 'lookup_table_version'] LOOP
    EXECUTE format('ALTER TABLE migration.%I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE migration.%I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON migration.%I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON migration.%I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'')', t || '_legal_entity_scope', t);
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    GRANT SELECT, INSERT, UPDATE ON migration.lookup_table TO passage_app;
    GRANT SELECT, INSERT ON migration.lookup_table_version TO passage_app;
  END IF;
END
$$;
