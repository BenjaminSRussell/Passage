-- Process manufacturing, wave 24 slice 11: the batch quality record (FR-24.5,
-- AC-24.10). Until the wave-16 inventory batch store exists, this service keeps
-- the batches it records quality data for (batch_record, one row per item and
-- batch number), the attribute definitions of an item (batch_attribute_def),
-- every value ever recorded (batch_attribute_history, append only; the current
-- value of an attribute is its latest row) and every disposition change
-- (batch_disposition_history, append only).

CREATE TABLE IF NOT EXISTS batch_attribute_def (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  item_id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 40),
  kind text NOT NULL CHECK (kind IN ('numeric', 'text', 'enum', 'date')),
  -- PRD 4.24.4 BatchAttribute.role: the potency attribute of an item is numeric
  -- and there is at most one per item.
  role text NOT NULL DEFAULT 'generic' CHECK (role IN ('generic', 'potency') AND (role = 'generic' OR kind = 'numeric')),
  target numeric(18,6),
  min_value numeric(18,6),
  max_value numeric(18,6),
  options jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, item_id, name)
);
CREATE UNIQUE INDEX IF NOT EXISTS batch_attribute_def_potency_idx ON batch_attribute_def (tenant_id, item_id) WHERE role = 'potency';

CREATE TABLE IF NOT EXISTS batch_record (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  item_id uuid NOT NULL,
  batch_number text NOT NULL CHECK (char_length(batch_number) BETWEEN 1 AND 40),
  batch_order_id uuid,
  disposition text NOT NULL DEFAULT 'available' CHECK (disposition IN ('available', 'on-hold', 'rejected', 'quarantine')),
  -- Set from the "expiry" date attribute when it is recorded (FR-24.6).
  expiry_date date,
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, item_id, batch_number),
  FOREIGN KEY (tenant_id, batch_order_id) REFERENCES batch_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS batch_record_item_idx ON batch_record (tenant_id, item_id, created_at DESC);

CREATE TABLE IF NOT EXISTS batch_attribute_history (
  tenant_id uuid NOT NULL,
  batch_id uuid NOT NULL,
  seq bigint GENERATED ALWAYS AS IDENTITY,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('numeric', 'text', 'enum', 'date')),
  num_value numeric(18,6),
  text_value text,
  date_value date,
  pass boolean NOT NULL,
  detail text NOT NULL DEFAULT '',
  recorded_by text NOT NULL,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, batch_id, seq),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES batch_record (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS batch_disposition_history (
  tenant_id uuid NOT NULL,
  batch_id uuid NOT NULL,
  seq bigint GENERATED ALWAYS AS IDENTITY,
  legal_entity_id uuid NOT NULL,
  from_disposition text NOT NULL,
  to_disposition text NOT NULL,
  reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 500),
  -- PRD 4.24.17: the user who changes the disposition also recorded attribute
  -- values on this batch (Maintain batch attributes with Maintain batch
  -- disposition on the same batch is flagged, not refused).
  segregation_conflict boolean NOT NULL DEFAULT false,
  changed_by text NOT NULL,
  changed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, batch_id, seq),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES batch_record (tenant_id, id)
);

ALTER TABLE batch_attribute_def ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_attribute_def FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_attribute_def_tenant_access ON batch_attribute_def AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE batch_record ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_record FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_record_tenant_access ON batch_record AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY batch_record_legal_entity_scope ON batch_record AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE batch_attribute_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_attribute_history FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_attribute_history_tenant_access ON batch_attribute_history AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY batch_attribute_history_legal_entity_scope ON batch_attribute_history AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE batch_disposition_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_disposition_history FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_disposition_history_tenant_access ON batch_disposition_history AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY batch_disposition_history_legal_entity_scope ON batch_disposition_history AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Definitions and both histories are only read and inserted; batch_record is
-- also updated (disposition, expiry, version) under a row lock.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON batch_attribute_def, batch_attribute_history, batch_disposition_history TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON batch_record TO passage_app';
  END IF;
END
$$;
