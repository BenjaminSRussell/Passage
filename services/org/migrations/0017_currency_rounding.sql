CREATE TABLE IF NOT EXISTS currency_rounding (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  currency_code char(3) NOT NULL CHECK (currency_code ~ '^[A-Z]{3}$'),
  purpose text NOT NULL CHECK (purpose IN ('general', 'price', 'purchase', 'sales', 'tax', 'cash')),
  legal_entity_id uuid,
  unit numeric(16,6) NOT NULL CHECK (unit > 0),
  method text NOT NULL CHECK (method IN ('half_even', 'half_up', 'down', 'up')),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entity (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS currency_rounding_key ON currency_rounding
  (tenant_id, currency_code, purpose, coalesce(legal_entity_id, '00000000-0000-0000-0000-000000000000'::uuid));
ALTER TABLE currency_rounding ENABLE ROW LEVEL SECURITY;
ALTER TABLE currency_rounding FORCE ROW LEVEL SECURITY;
CREATE POLICY currency_rounding_tenant_access ON currency_rounding AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY currency_rounding_legal_entity_scope ON currency_rounding AS RESTRICTIVE
  USING (legal_entity_id IS NULL
         OR current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
