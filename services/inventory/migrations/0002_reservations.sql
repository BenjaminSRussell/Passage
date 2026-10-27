-- Wave 16 slice 2: batch expiry (FEFO input) and reservations (FR-16.7).
-- Additive only.

-- Local copy of the wave 8 batch expiry, keyed by product and batch. Read by FEFO
-- selection. A batch without a row (or with a null date) never expires and sorts
-- last (BR-16.7).
CREATE TABLE IF NOT EXISTS batch_expiry (
  tenant_id uuid NOT NULL,
  product_id uuid NOT NULL,
  batch_id text NOT NULL CHECK (char_length(batch_id) BETWEEN 1 AND 60),
  expiry_date date,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, product_id, batch_id)
);
ALTER TABLE batch_expiry ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_expiry FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_expiry_tenant_access ON batch_expiry AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- One row per allocation of a reservation request. A policy request that spans
-- several dimension sets writes one row per allocation, numbered by allocation_no
-- under the same idempotency key. ACTIVE rows are the reserved_physical bucket of
-- the on-hand row they point at.
CREATE TABLE IF NOT EXISTS inventory_reservation (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
  allocation_no integer NOT NULL CHECK (allocation_no > 0),
  product_id uuid NOT NULL,
  dimension_set_hash bytea NOT NULL,
  qty numeric(28,6) NOT NULL CHECK (qty > 0),
  requested_qty numeric(28,6) NOT NULL CHECK (requested_qty > 0),
  demand_type text NOT NULL CHECK (demand_type IN ('MANUAL', 'SALES_ORDER', 'PRODUCTION_ORDER', 'TRANSFER_ORDER')),
  demand_id text NOT NULL CHECK (char_length(demand_id) BETWEEN 1 AND 60),
  policy text NOT NULL CHECK (policy IN ('MANUAL', 'FIFO', 'FEFO', 'LIFO')),
  status text NOT NULL CHECK (status IN ('ACTIVE', 'RELEASED', 'CONSUMED')),
  created_by text,
  created_at timestamptz NOT NULL DEFAULT now(),
  released_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key, allocation_no),
  FOREIGN KEY (tenant_id, legal_entity_id, product_id, dimension_set_hash)
    REFERENCES on_hand (tenant_id, legal_entity_id, product_id, dimension_set_hash),
  CHECK ((status = 'RELEASED') = (released_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS inventory_reservation_product_idx
  ON inventory_reservation (tenant_id, legal_entity_id, product_id, dimension_set_hash, status);
ALTER TABLE inventory_reservation ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_reservation FORCE ROW LEVEL SECURITY;
CREATE POLICY inventory_reservation_tenant_access ON inventory_reservation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY inventory_reservation_legal_entity_scope ON inventory_reservation AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON batch_expiry TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON inventory_reservation TO passage_app';
  END IF;
END
$$;
