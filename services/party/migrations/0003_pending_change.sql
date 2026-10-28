CREATE TABLE IF NOT EXISTS pending_change (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  field text NOT NULL CHECK (field = 'credit_limit'),
  current_value numeric(18,2),
  requested_value numeric(18,2) NOT NULL CHECK (requested_value >= 0),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
  requested_by text NOT NULL,
  decided_by text,
  requested_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customer_role (tenant_id, id),
  CHECK ((status = 'pending') = (decided_at IS NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS pending_change_one_open
  ON pending_change (tenant_id, customer_id, field) WHERE status = 'pending';

ALTER TABLE pending_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE pending_change FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS pending_change_tenant_access ON pending_change;
CREATE POLICY pending_change_tenant_access ON pending_change AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS pending_change_legal_entity_scope ON pending_change;
CREATE POLICY pending_change_legal_entity_scope ON pending_change AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
