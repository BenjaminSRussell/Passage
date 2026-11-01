ALTER TABLE sales_order_line ADD COLUMN IF NOT EXISTS configuration_id uuid;

CREATE TABLE IF NOT EXISTS order_line_production (
  tenant_id uuid NOT NULL,
  line_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  configuration_id uuid NOT NULL,
  production_order_id uuid NOT NULL,
  requested_by text NOT NULL,
  requested_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, line_id),
  FOREIGN KEY (tenant_id, line_id) REFERENCES sales_order_line (tenant_id, id),
  FOREIGN KEY (tenant_id, order_id) REFERENCES sales_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS order_line_production_order ON order_line_production (tenant_id, order_id);

ALTER TABLE order_line_production ENABLE ROW LEVEL SECURITY;
ALTER TABLE order_line_production FORCE ROW LEVEL SECURITY;
CREATE POLICY order_line_production_tenant_access ON order_line_production AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY order_line_production_legal_entity_scope ON order_line_production AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON order_line_production TO passage_app';
  END IF;
END
$$;
