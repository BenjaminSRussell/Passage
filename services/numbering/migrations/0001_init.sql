CREATE TABLE IF NOT EXISTS number_sequence (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  format text NOT NULL,
  legal_entity_id uuid NOT NULL,
  mode text NOT NULL CHECK (mode IN ('gap_free', 'continuous', 'non_continuous')),
  start_from bigint NOT NULL CHECK (start_from >= 1),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, code),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE number_sequence ENABLE ROW LEVEL SECURITY;
ALTER TABLE number_sequence FORCE ROW LEVEL SECURITY;
CREATE POLICY number_sequence_tenant_access ON number_sequence AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY number_sequence_legal_entity_scope ON number_sequence AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS number_sequence_state (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  sequence_code text NOT NULL,
  next_value bigint NOT NULL,
  PRIMARY KEY (tenant_id, legal_entity_id, sequence_code)
);
ALTER TABLE number_sequence_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE number_sequence_state FORCE ROW LEVEL SECURITY;
CREATE POLICY number_sequence_state_tenant_access ON number_sequence_state AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY number_sequence_state_legal_entity_scope ON number_sequence_state AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS number_allocation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  sequence_code text NOT NULL,
  value bigint NOT NULL,
  formatted text NOT NULL,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, sequence_code, value),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE number_allocation ENABLE ROW LEVEL SECURITY;
ALTER TABLE number_allocation FORCE ROW LEVEL SECURITY;
CREATE POLICY number_allocation_tenant_access ON number_allocation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY number_allocation_legal_entity_scope ON number_allocation AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
