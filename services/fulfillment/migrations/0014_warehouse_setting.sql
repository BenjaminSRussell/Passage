-- Fulfillment slice 13: the short_pick_action warehouse setting (FR-28.8) and
-- backorder shipments. A warehouse chooses what happens to the open remainder
-- once a packing slip has shipped part of a shipment: KEEP_OPEN (the shipment
-- stays PARTIALLY_SHIPPED and its remainder is re-picked, the default),
-- CANCEL_REMAINDER (the remainder is closed) or REMAINDER_BACKORDER (the
-- remainder moves to a new shipment of the same order and warehouse). A
-- warehouse belongs to one legal entity (org), so the setting carries it and is
-- scoped by it. A backorder shipment points back at the shipment it split from.
CREATE TABLE IF NOT EXISTS warehouse_setting (
  tenant_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  short_pick_action text NOT NULL DEFAULT 'KEEP_OPEN' CHECK (short_pick_action IN ('KEEP_OPEN', 'CANCEL_REMAINDER', 'REMAINDER_BACKORDER')),
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, warehouse_id)
);

ALTER TABLE warehouse_setting ENABLE ROW LEVEL SECURITY;
ALTER TABLE warehouse_setting FORCE ROW LEVEL SECURITY;
CREATE POLICY warehouse_setting_tenant_access ON warehouse_setting AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY warehouse_setting_legal_entity_scope ON warehouse_setting AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE shipment ADD COLUMN IF NOT EXISTS backorder_of_id uuid;
ALTER TABLE shipment DROP CONSTRAINT IF EXISTS shipment_backorder_of_fk;
ALTER TABLE shipment ADD CONSTRAINT shipment_backorder_of_fk
  FOREIGN KEY (tenant_id, backorder_of_id) REFERENCES shipment (tenant_id, id);
ALTER TABLE shipment DROP CONSTRAINT IF EXISTS shipment_backorder_not_self;
ALTER TABLE shipment ADD CONSTRAINT shipment_backorder_not_self CHECK (backorder_of_id IS NULL OR backorder_of_id <> id);
CREATE UNIQUE INDEX IF NOT EXISTS shipment_backorder_once ON shipment (tenant_id, backorder_of_id) WHERE backorder_of_id IS NOT NULL;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON warehouse_setting TO passage_app';
  END IF;
END
$$;
