-- Fulfillment slice 3: partial shipments and shipment cancellation (FR-28.8,
-- BR-28.6), the picking workbench waves (FR-28.13, BR-28.14) and carrier
-- tracking events (FR-28.10). Every new table is row-level secured by
-- tenant_id; tables that carry legal_entity_id also get the restrictive
-- legal-entity scope policy.

-- The shipment state machine gains PARTIALLY_SHIPPED (FR-28.8).
ALTER TABLE shipment DROP CONSTRAINT IF EXISTS shipment_status_check;
ALTER TABLE shipment ADD CONSTRAINT shipment_status_check CHECK (status IN (
  'OPEN', 'RELEASED', 'PICKING', 'PICKED', 'PACKED', 'PARTIALLY_SHIPPED', 'SHIPPED', 'DELIVERED', 'CANCELLED'));

-- What the workbench filters and groups by (FR-28.13): the order's carrier on
-- the shipment and the pick zone on each line. Null groups as UNASSIGNED.
ALTER TABLE shipment ADD COLUMN IF NOT EXISTS carrier_code text CHECK (carrier_code ~ '^[A-Za-z0-9_.-]{1,32}$');
ALTER TABLE shipment_line ADD COLUMN IF NOT EXISTS zone_code text CHECK (zone_code ~ '^[A-Za-z0-9_.-]{1,32}$');

-- A shipment can now post several slips (FR-28.8), so each shipped container
-- remembers the slip it left with; cancelling one slip reverts only its own
-- containers and returnable balances.
ALTER TABLE container ADD COLUMN IF NOT EXISTS packing_slip_id uuid;
ALTER TABLE container DROP CONSTRAINT IF EXISTS container_packing_slip_fk;
ALTER TABLE container ADD CONSTRAINT container_packing_slip_fk
  FOREIGN KEY (tenant_id, packing_slip_id) REFERENCES packing_slip (tenant_id, id);
-- Before this migration a shipment had at most one live posted slip, so every
-- SHIPPED container belongs to it.
UPDATE container c SET packing_slip_id = (
  SELECT p.id FROM packing_slip p
  WHERE p.tenant_id = c.tenant_id AND p.shipment_id = c.shipment_id AND p.status = 'POSTED' AND p.reversal_of_id IS NULL
  ORDER BY p.created_at DESC LIMIT 1)
WHERE c.status = 'SHIPPED' AND c.packing_slip_id IS NULL;
CREATE INDEX IF NOT EXISTS container_packing_slip_idx ON container (tenant_id, packing_slip_id) WHERE packing_slip_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS wave (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  wave_number text NOT NULL,
  legal_entity_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  group_by text NOT NULL CHECK (group_by IN ('CUSTOMER', 'CARRIER', 'ZONE', 'PRODUCT')),
  group_key text NOT NULL,
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN')),
  line_count integer NOT NULL CHECK (line_count >= 1),
  order_count integer NOT NULL CHECK (order_count >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, wave_number)
);
CREATE INDEX IF NOT EXISTS wave_warehouse_idx ON wave (tenant_id, warehouse_id, created_at);

CREATE TABLE IF NOT EXISTS wave_shipment (
  tenant_id uuid NOT NULL,
  wave_id uuid NOT NULL,
  shipment_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, wave_id, shipment_id),
  FOREIGN KEY (tenant_id, wave_id) REFERENCES wave (tenant_id, id),
  FOREIGN KEY (tenant_id, shipment_id) REFERENCES shipment (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS wave_shipment_shipment_idx ON wave_shipment (tenant_id, shipment_id);

-- A shipment line belongs to at most one wave (BR-28.14).
CREATE TABLE IF NOT EXISTS wave_line (
  tenant_id uuid NOT NULL,
  shipment_line_id uuid NOT NULL,
  wave_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, shipment_line_id),
  FOREIGN KEY (tenant_id, wave_id) REFERENCES wave (tenant_id, id),
  FOREIGN KEY (tenant_id, shipment_line_id) REFERENCES shipment_line (tenant_id, id)
);

-- Carrier tracking events. event_key is the provider event id, so a redelivered
-- webhook or poll does not add a second event (FR-28.6).
CREATE TABLE IF NOT EXISTS shipment_tracking_event (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  shipment_id uuid NOT NULL,
  provider text NOT NULL,
  event_key text NOT NULL,
  tracking_number text NOT NULL,
  status text NOT NULL CHECK (status IN ('QUOTED', 'LABELLED', 'MANIFESTED', 'IN_TRANSIT', 'DELIVERED', 'EXCEPTION')),
  description text NOT NULL DEFAULT '',
  event_time timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, shipment_id, provider, event_key),
  FOREIGN KEY (tenant_id, shipment_id) REFERENCES shipment (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS shipment_tracking_event_time_idx ON shipment_tracking_event (tenant_id, shipment_id, event_time);

ALTER TABLE wave ENABLE ROW LEVEL SECURITY;
ALTER TABLE wave FORCE ROW LEVEL SECURITY;
CREATE POLICY wave_tenant_access ON wave AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY wave_legal_entity_scope ON wave AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE wave_shipment ENABLE ROW LEVEL SECURITY;
ALTER TABLE wave_shipment FORCE ROW LEVEL SECURITY;
CREATE POLICY wave_shipment_tenant_access ON wave_shipment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE wave_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE wave_line FORCE ROW LEVEL SECURITY;
CREATE POLICY wave_line_tenant_access ON wave_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE shipment_tracking_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipment_tracking_event FORCE ROW LEVEL SECURITY;
CREATE POLICY shipment_tracking_event_tenant_access ON shipment_tracking_event AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
