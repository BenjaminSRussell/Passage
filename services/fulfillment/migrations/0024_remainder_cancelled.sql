-- Fulfillment slice 20: fulfillment follows sales' order.remaindercancelled.v1.
-- order_line_kept holds, per sales order line, the quantity the line keeps after
-- the remainder was cancelled; a packing slip post that would ship more than that
-- is refused (FUL-0066). fulfillment_exception is the warehouse supervisor's list
-- of the cases the service cannot fix alone: a slip posted before the
-- cancellation arrived, and packed quantity beyond the kept quantity. New tables
-- only, so no lock on a live table.

CREATE TABLE IF NOT EXISTS order_line_kept (
  tenant_id uuid NOT NULL,
  order_line_id uuid NOT NULL,
  order_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  kept_qty numeric(20,6) NOT NULL CHECK (kept_qty >= 0),
  order_version integer NOT NULL CHECK (order_version >= 0),
  event_id text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, order_line_id)
);
ALTER TABLE order_line_kept ENABLE ROW LEVEL SECURITY;
ALTER TABLE order_line_kept FORCE ROW LEVEL SECURITY;
CREATE POLICY order_line_kept_tenant_access ON order_line_kept AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY order_line_kept_legal_entity_access ON order_line_kept AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS fulfillment_exception (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('SLIP_POSTED_BEYOND_KEPT', 'PACKED_BEYOND_KEPT')),
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'RESOLVED')),
  order_id uuid NOT NULL,
  order_line_id uuid NOT NULL,
  shipment_id uuid,
  packing_slip_id uuid,
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  kept_qty numeric(20,6) NOT NULL CHECK (kept_qty >= 0),
  detail text NOT NULL,
  source_event_id text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  resolved_by text,
  resolution_note text,
  PRIMARY KEY (tenant_id, id),
  CHECK ((status = 'RESOLVED') = (resolved_at IS NOT NULL))
);
-- One open exception per case: a second event about the same order line and shipment adds none.
CREATE UNIQUE INDEX IF NOT EXISTS fulfillment_exception_open_once ON fulfillment_exception
  (tenant_id, kind, order_line_id, coalesce(shipment_id, '00000000-0000-0000-0000-000000000000'::uuid)) WHERE status = 'OPEN';
CREATE INDEX IF NOT EXISTS fulfillment_exception_status_idx ON fulfillment_exception (tenant_id, status, created_at DESC);
ALTER TABLE fulfillment_exception ENABLE ROW LEVEL SECURITY;
ALTER TABLE fulfillment_exception FORCE ROW LEVEL SECURITY;
CREATE POLICY fulfillment_exception_tenant_access ON fulfillment_exception AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY fulfillment_exception_legal_entity_access ON fulfillment_exception AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON order_line_kept, fulfillment_exception TO passage_app';
  END IF;
END
$$;
