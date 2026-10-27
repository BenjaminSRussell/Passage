-- Fulfillment slice 1: shipments, pick and pack quantities, containers with
-- SSCCs, packing slips with their journal, number counters, outbox and
-- idempotency keys. Every tenant table is row-level secured by tenant_id; tables
-- that carry legal_entity_id also get the restrictive legal-entity scope policy.

CREATE TABLE IF NOT EXISTS number_counter (
  tenant_id uuid NOT NULL,
  sequence_id text NOT NULL,
  next_value bigint NOT NULL CHECK (next_value >= 1),
  PRIMARY KEY (tenant_id, sequence_id)
);

CREATE TABLE IF NOT EXISTS shipment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  shipment_number text NOT NULL,
  legal_entity_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  order_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('OPEN', 'RELEASED', 'PICKING', 'PICKED', 'PACKED', 'SHIPPED', 'DELIVERED', 'CANCELLED')),
  planned_ship_date date NOT NULL,
  actual_ship_date timestamptz,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, shipment_number)
);
CREATE INDEX IF NOT EXISTS shipment_warehouse_status_idx ON shipment (tenant_id, warehouse_id, status, planned_ship_date);

CREATE TABLE IF NOT EXISTS shipment_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  shipment_id uuid NOT NULL,
  order_line_id uuid NOT NULL,
  product_id uuid NOT NULL,
  unit text NOT NULL,
  qty_planned numeric(20,6) NOT NULL CHECK (qty_planned > 0),
  qty_picked numeric(20,6) NOT NULL DEFAULT 0 CHECK (qty_picked >= 0),
  qty_packed numeric(20,6) NOT NULL DEFAULT 0 CHECK (qty_packed >= 0),
  qty_shipped numeric(20,6) NOT NULL DEFAULT 0 CHECK (qty_shipped >= 0),
  pick_confirmed boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, shipment_id) REFERENCES shipment (tenant_id, id),
  CHECK (qty_picked <= qty_planned),
  CHECK (qty_packed <= qty_picked),
  CHECK (qty_shipped <= qty_packed)
);
CREATE INDEX IF NOT EXISTS shipment_line_shipment_idx ON shipment_line (tenant_id, shipment_id);
CREATE INDEX IF NOT EXISTS shipment_line_order_line_idx ON shipment_line (tenant_id, order_line_id);

CREATE TABLE IF NOT EXISTS container (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  shipment_id uuid NOT NULL,
  container_type text NOT NULL,
  sscc char(18) NOT NULL CHECK (sscc ~ '^[0-9]{18}$'),
  status text NOT NULL DEFAULT 'CLOSED' CHECK (status IN ('OPEN', 'CLOSED', 'SHIPPED')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, sscc),
  FOREIGN KEY (tenant_id, shipment_id) REFERENCES shipment (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS container_line (
  tenant_id uuid NOT NULL,
  container_id uuid NOT NULL,
  shipment_line_id uuid NOT NULL,
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  PRIMARY KEY (tenant_id, container_id, shipment_line_id),
  FOREIGN KEY (tenant_id, container_id) REFERENCES container (tenant_id, id),
  FOREIGN KEY (tenant_id, shipment_line_id) REFERENCES shipment_line (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS packing_slip (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  shipment_id uuid NOT NULL,
  order_id uuid NOT NULL,
  packing_slip_number text NOT NULL,
  status text NOT NULL CHECK (status IN ('PENDING', 'POSTED', 'CANCELLED', 'INVOICED')),
  posting_date date NOT NULL,
  currency char(3) NOT NULL,
  total_cost numeric(20,6) NOT NULL,
  journal jsonb NOT NULL DEFAULT '[]'::jsonb,
  reversal_of_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (status IN ('POSTED', 'CANCELLED') OR reversal_of_id IS NULL),
  PRIMARY KEY (tenant_id, id),
  -- PS- numbers are gap-free per legal entity (section 5.28.15).
  UNIQUE (tenant_id, legal_entity_id, packing_slip_number),
  FOREIGN KEY (tenant_id, shipment_id) REFERENCES shipment (tenant_id, id),
  FOREIGN KEY (tenant_id, reversal_of_id) REFERENCES packing_slip (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS packing_slip_order_idx ON packing_slip (tenant_id, order_id);
CREATE INDEX IF NOT EXISTS packing_slip_posting_date_idx ON packing_slip (tenant_id, posting_date);
CREATE INDEX IF NOT EXISTS packing_slip_shipment_idx ON packing_slip (tenant_id, shipment_id);
-- A posted slip is reversed at most once.
CREATE UNIQUE INDEX IF NOT EXISTS packing_slip_reversal_once ON packing_slip (tenant_id, reversal_of_id) WHERE reversal_of_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS packing_slip_line (
  tenant_id uuid NOT NULL,
  packing_slip_id uuid NOT NULL,
  order_line_id uuid NOT NULL,
  shipment_line_id uuid NOT NULL,
  qty numeric(20,6) NOT NULL,
  cost_amount numeric(20,6) NOT NULL,
  cost_snapshot_id text NOT NULL,
  PRIMARY KEY (tenant_id, packing_slip_id, shipment_line_id),
  FOREIGN KEY (tenant_id, packing_slip_id) REFERENCES packing_slip (tenant_id, id),
  FOREIGN KEY (tenant_id, shipment_line_id) REFERENCES shipment_line (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int NOT NULL,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);

ALTER TABLE number_counter ENABLE ROW LEVEL SECURITY;
ALTER TABLE number_counter FORCE ROW LEVEL SECURITY;
CREATE POLICY number_counter_tenant_access ON number_counter AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE shipment ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipment FORCE ROW LEVEL SECURITY;
CREATE POLICY shipment_tenant_access ON shipment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY shipment_legal_entity_scope ON shipment AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE shipment_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipment_line FORCE ROW LEVEL SECURITY;
CREATE POLICY shipment_line_tenant_access ON shipment_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE container ENABLE ROW LEVEL SECURITY;
ALTER TABLE container FORCE ROW LEVEL SECURITY;
CREATE POLICY container_tenant_access ON container AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE container_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE container_line FORCE ROW LEVEL SECURITY;
CREATE POLICY container_line_tenant_access ON container_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE packing_slip ENABLE ROW LEVEL SECURITY;
ALTER TABLE packing_slip FORCE ROW LEVEL SECURITY;
CREATE POLICY packing_slip_tenant_access ON packing_slip AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY packing_slip_legal_entity_scope ON packing_slip AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE packing_slip_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE packing_slip_line FORCE ROW LEVEL SECURITY;
CREATE POLICY packing_slip_line_tenant_access ON packing_slip_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY idempotency_keys_tenant_access ON idempotency_keys AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
