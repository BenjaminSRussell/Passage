-- Versioned change history for sales orders (FR-27.9). One row per version
-- bump: the diff is the field-level change set that produced that version.
CREATE TABLE IF NOT EXISTS sales_order_version (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  version integer NOT NULL CHECK (version > 0),
  actor text NOT NULL,
  reason text NOT NULL DEFAULT '',
  diff jsonb NOT NULL DEFAULT '[]'::jsonb,
  changed_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, order_id, version),
  FOREIGN KEY (tenant_id, order_id) REFERENCES sales_order (tenant_id, id)
);

ALTER TABLE sales_order_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_order_version FORCE ROW LEVEL SECURITY;
CREATE POLICY sales_order_version_tenant_access ON sales_order_version AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY sales_order_version_legal_entity_scope ON sales_order_version AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Cancelling an order clears its ACTIVE holds (PRD §5.27.4 order_hold status
-- ACTIVE | RELEASED | CLEARED) so a cancelled order leaves the release queue.
ALTER TABLE order_hold DROP CONSTRAINT IF EXISTS order_hold_status_check;
ALTER TABLE order_hold ADD CONSTRAINT order_hold_status_check CHECK (status IN ('ACTIVE', 'RELEASED', 'CLEARED'));
