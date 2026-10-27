CREATE TABLE IF NOT EXISTS project (
  tenant_id uuid NOT NULL,
  id text NOT NULL CHECK (id ~ '^[A-Z0-9][A-Z0-9-]{0,19}$'),
  legal_entity_id uuid NOT NULL,
  parent_project_id text,
  project_group_id text NOT NULL CHECK (char_length(project_group_id) BETWEEN 1 AND 10),
  project_type text NOT NULL CHECK (project_type IN ('TIME_MATERIAL', 'FIXED_PRICE', 'INTERNAL', 'COST', 'INVESTMENT', 'TIME')),
  customer_party_id uuid,
  contract_id uuid,
  currency char(3) NOT NULL,
  stage text NOT NULL DEFAULT 'CREATED' CHECK (stage IN ('CREATED', 'IN_PROCESS', 'ON_HOLD', 'FINISHED')),
  start_date date NOT NULL,
  end_date date,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, parent_project_id) REFERENCES project (tenant_id, id),
  CHECK (end_date IS NULL OR end_date >= start_date),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE project ENABLE ROW LEVEL SECURITY;
ALTER TABLE project FORCE ROW LEVEL SECURITY;
CREATE POLICY project_tenant_access ON project AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY project_legal_entity_scope ON project AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
CREATE INDEX IF NOT EXISTS project_stage_idx ON project (tenant_id, stage, id);
