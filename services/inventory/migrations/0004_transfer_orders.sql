-- Wave 16 slice 3: transfer orders with in-transit warehouse (FR-16.15, FR-16.16).
-- Additive: two tables. Each movement of a line is a valued SOLD / PURCHASED pair of
-- inventory transactions (origin TRANSFER_LINE, reference = the transfer order id).
-- ship_date is the planned date until the order ships, then the actual one.

CREATE TABLE IF NOT EXISTS transfer_order (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  idempotency_key text NOT NULL,
  status text NOT NULL CHECK (status IN ('CREATED', 'SHIPPED', 'PARTIALLY_RECEIVED', 'RECEIVED', 'CANCELLED')),
  from_warehouse text NOT NULL,
  to_warehouse text NOT NULL,
  to_site text,
  transit_warehouse text,
  use_transit boolean NOT NULL,
  ship_date date NOT NULL,
  receipt_date date,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, number),
  CHECK (NOT use_transit OR transit_warehouse IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS transfer_order_from_idx ON transfer_order (tenant_id, from_warehouse, status);
CREATE INDEX IF NOT EXISTS transfer_order_to_idx ON transfer_order (tenant_id, to_warehouse, status);
ALTER TABLE transfer_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE transfer_order FORCE ROW LEVEL SECURITY;
CREATE POLICY transfer_order_tenant_access ON transfer_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY transfer_order_legal_entity_scope ON transfer_order AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS transfer_order_line (
  tenant_id uuid NOT NULL,
  transfer_order_id uuid NOT NULL,
  id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  product_id uuid NOT NULL,
  dimension_set_hash bytea NOT NULL,
  unit text NOT NULL,
  qty numeric(28,6) NOT NULL CHECK (qty > 0),
  shipped_qty numeric(28,6) NOT NULL DEFAULT 0 CHECK (shipped_qty >= 0),
  received_qty numeric(28,6) NOT NULL DEFAULT 0 CHECK (received_qty >= 0),
  under_pct numeric(9,6) NOT NULL DEFAULT 0 CHECK (under_pct BETWEEN 0 AND 100),
  over_pct numeric(9,6) NOT NULL DEFAULT 0 CHECK (over_pct BETWEEN 0 AND 100),
  closed boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, transfer_order_id, line_no),
  FOREIGN KEY (tenant_id, transfer_order_id) REFERENCES transfer_order (tenant_id, id),
  FOREIGN KEY (tenant_id, dimension_set_hash) REFERENCES dimension_set (tenant_id, hash)
);
ALTER TABLE transfer_order_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE transfer_order_line FORCE ROW LEVEL SECURITY;
CREATE POLICY transfer_order_line_tenant_access ON transfer_order_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
