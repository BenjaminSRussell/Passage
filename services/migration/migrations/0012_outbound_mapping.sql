-- Outbound mappings (PSG-10 integration track S5, decision B4: orders and invoices flow Passage to the legacy ERP).
-- A mapping turns one Passage CloudEvent type into the request body the integration hub pushes to the customer's
-- legacy ERP. Every edit writes an immutable version row; the header carries the current version (the optimistic
-- version of the API) and the facts that do not change between versions.
CREATE TABLE migration.outbound_mapping (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  -- The legacy system whose keys the mapping reads and records in migration.xref (fixed at creation).
  source_system text NOT NULL CHECK (source_system IN ('ax2012', 'd365fo', 'sap_ecc', 'sap_s4') OR source_system ~ '^custom_[a-z0-9_]{1,40}$'),
  -- Copy of the current version's event type pattern, for listing and filtering.
  event_type text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_by text NOT NULL DEFAULT '',
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, name),
  UNIQUE (tenant_id, idempotency_key)
);

CREATE TABLE migration.outbound_mapping_version (
  tenant_id uuid NOT NULL,
  mapping_id uuid NOT NULL,
  version integer NOT NULL CHECK (version >= 1),
  definition jsonb NOT NULL CHECK (jsonb_typeof(definition) = 'object'),
  checksum text NOT NULL,
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, mapping_id, version),
  FOREIGN KEY (tenant_id, mapping_id) REFERENCES migration.outbound_mapping (tenant_id, id)
);

-- One row per (mapping, event) whose legacy key was recorded: the hub retries xref:record, and the retry must be
-- a no-op that answers like the first call (and refuses a different legacy key for the same event).
CREATE TABLE migration.outbound_xref_event (
  tenant_id uuid NOT NULL,
  mapping_id uuid NOT NULL,
  event_id text NOT NULL CHECK (length(event_id) BETWEEN 1 AND 256),
  legacy_key text NOT NULL CHECK (length(legacy_key) BETWEEN 1 AND 512),
  refs jsonb NOT NULL,
  actor text NOT NULL DEFAULT '',
  recorded_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, mapping_id, event_id),
  FOREIGN KEY (tenant_id, mapping_id) REFERENCES migration.outbound_mapping (tenant_id, id)
);

CREATE INDEX outbound_mapping_list_idx ON migration.outbound_mapping (tenant_id, name, id);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['outbound_mapping', 'outbound_mapping_version', 'outbound_xref_event'] LOOP
    EXECUTE format('ALTER TABLE migration.%I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE migration.%I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON migration.%I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON migration.%I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'')', t || '_legal_entity_scope', t);
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    GRANT SELECT, INSERT, UPDATE ON migration.outbound_mapping TO passage_app;
    GRANT SELECT, INSERT ON migration.outbound_mapping_version, migration.outbound_xref_event TO passage_app;
  END IF;
END
$$;
