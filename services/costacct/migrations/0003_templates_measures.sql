-- Statistical measures (FR-38.17 inputs, PRD 7.38.5 /statistical-measures/{id}/values):
-- one value per receiver cost object and period. Loads are upserts keyed by
-- (measure, object, period). Formulas reference a measure as m.<code>.
CREATE TABLE IF NOT EXISTS statistical_measure (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Za-z_][A-Za-z0-9_]{0,39}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
  unit text NOT NULL CHECK (char_length(unit) BETWEEN 1 AND 20),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, code)
);

CREATE TABLE IF NOT EXISTS statistical_measure_value (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  measure_id uuid NOT NULL,
  cost_object_id uuid NOT NULL,
  period_key text NOT NULL CHECK (period_key ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  value numeric(19,4) NOT NULL CHECK (value >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, measure_id, cost_object_id, period_key),
  FOREIGN KEY (tenant_id, measure_id) REFERENCES statistical_measure (tenant_id, id),
  FOREIGN KEY (tenant_id, cost_object_id) REFERENCES cost_object (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS statistical_measure_value_period
  ON statistical_measure_value (tenant_id, legal_entity_id, period_key);

-- Allocation templates (FR-38.17): a sender cost object is credited, and each
-- line's receiver debited, with the line formula's value on the line's secondary
-- element. Formulas are validated at save; every save of a code is a new version
-- (optimistic: the caller names the version it read) and keeps its lines in
-- allocation_template_version.
CREATE TABLE IF NOT EXISTS allocation_template (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{2,40}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  sender_cost_object_id uuid NOT NULL,
  allow_negative boolean NOT NULL DEFAULT false,
  lines jsonb NOT NULL,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, code),
  FOREIGN KEY (tenant_id, sender_cost_object_id) REFERENCES cost_object (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS allocation_template_version (
  tenant_id uuid NOT NULL,
  template_id uuid NOT NULL,
  version integer NOT NULL,
  legal_entity_id uuid NOT NULL,
  sender_cost_object_id uuid NOT NULL,
  allow_negative boolean NOT NULL,
  lines jsonb NOT NULL,
  saved_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, template_id, version),
  FOREIGN KEY (tenant_id, template_id) REFERENCES allocation_template (tenant_id, id)
);

-- One completed run per template and period; dry runs are never stored.
CREATE TABLE IF NOT EXISTS template_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  template_id uuid NOT NULL,
  template_version integer NOT NULL,
  period_key text NOT NULL CHECK (period_key ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  currency char(3) NOT NULL,
  sender_cost_object_id uuid NOT NULL,
  total numeric(19,4) NOT NULL,
  lines jsonb NOT NULL,
  warnings jsonb NOT NULL,
  status text NOT NULL CHECK (status = 'Completed'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, template_id) REFERENCES allocation_template (tenant_id, id),
  FOREIGN KEY (tenant_id, template_id, template_version) REFERENCES allocation_template_version (tenant_id, template_id, version)
);
CREATE UNIQUE INDEX IF NOT EXISTS template_run_one_completed
  ON template_run (tenant_id, template_id, period_key) WHERE status = 'Completed';
CREATE INDEX IF NOT EXISTS template_run_entity ON template_run (tenant_id, legal_entity_id, created_at);

-- Entries posted by a template run point back to it (origin Allocation); the
-- run's entries net to zero.
ALTER TABLE cost_entry ADD COLUMN IF NOT EXISTS template_run_id uuid;
DO $$ BEGIN
  ALTER TABLE cost_entry ADD CONSTRAINT cost_entry_template_run_fk
    FOREIGN KEY (tenant_id, template_run_id) REFERENCES template_run (tenant_id, id);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

ALTER TABLE statistical_measure ENABLE ROW LEVEL SECURITY;
ALTER TABLE statistical_measure FORCE ROW LEVEL SECURITY;
ALTER TABLE statistical_measure_value ENABLE ROW LEVEL SECURITY;
ALTER TABLE statistical_measure_value FORCE ROW LEVEL SECURITY;
ALTER TABLE allocation_template ENABLE ROW LEVEL SECURITY;
ALTER TABLE allocation_template FORCE ROW LEVEL SECURITY;
ALTER TABLE allocation_template_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE allocation_template_version FORCE ROW LEVEL SECURITY;
ALTER TABLE template_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE template_run FORCE ROW LEVEL SECURITY;

CREATE POLICY statistical_measure_tenant_access ON statistical_measure AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY statistical_measure_legal_entity_scope ON statistical_measure AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE POLICY statistical_measure_value_tenant_access ON statistical_measure_value AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY statistical_measure_value_legal_entity_scope ON statistical_measure_value AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE POLICY allocation_template_tenant_access ON allocation_template AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY allocation_template_legal_entity_scope ON allocation_template AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE POLICY allocation_template_version_tenant_access ON allocation_template_version AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY allocation_template_version_legal_entity_scope ON allocation_template_version AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE POLICY template_run_tenant_access ON template_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY template_run_legal_entity_scope ON template_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
