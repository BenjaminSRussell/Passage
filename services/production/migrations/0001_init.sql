-- Production orders, their exploded BOM and route snapshot, planned cost,
-- status log and the transactional outbox. Every table is tenant-scoped with RLS.

CREATE TABLE IF NOT EXISTS production_order_sequence (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  next_value bigint NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id, legal_entity_id)
);

CREATE TABLE IF NOT EXISTS production_order (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_number text NOT NULL,
  product_id uuid NOT NULL,
  site_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  currency char(3) NOT NULL,
  quantity numeric(18,6) NOT NULL CHECK (quantity > 0),
  qty_raf numeric(18,6) NOT NULL DEFAULT 0 CHECK (qty_raf >= 0),
  qty_error numeric(18,6) NOT NULL DEFAULT 0 CHECK (qty_error >= 0),
  status text NOT NULL DEFAULT 'Created' CHECK (status IN ('Created','Estimated','Scheduled','Released','Started','ReportedAsFinished','Ended','Cancelled')),
  delivery_date date,
  origin_type text NOT NULL DEFAULT 'Manual' CHECK (origin_type IN ('PlannedOrder','SalesOrderLine','Manual','Kanban','Split','Rework')),
  origin_id uuid,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, order_number),
  UNIQUE (tenant_id, idempotency_key)
);
-- PRD 4.21.4 indexing: the order list by status and delivery date, and by product.
CREATE INDEX IF NOT EXISTS production_order_status ON production_order (tenant_id, legal_entity_id, status, delivery_date);
CREATE INDEX IF NOT EXISTS production_order_product ON production_order (tenant_id, product_id, status);

CREATE TABLE IF NOT EXISTS production_bom_line (
  tenant_id uuid NOT NULL,
  order_id uuid NOT NULL,
  line_number text NOT NULL,
  line_seq integer NOT NULL,
  item_id uuid NOT NULL,
  qty_per numeric(18,6) NOT NULL,
  series_size numeric(18,6) NOT NULL CHECK (series_size > 0),
  scrap_pct numeric(7,4) NOT NULL CHECK (scrap_pct >= 0 AND scrap_pct < 100),
  round_up boolean NOT NULL DEFAULT false,
  qty_required numeric(18,6) NOT NULL,
  unit_cost_est numeric(20,6) NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, order_id, line_number),
  FOREIGN KEY (tenant_id, order_id) REFERENCES production_order (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS production_operation (
  tenant_id uuid NOT NULL,
  order_id uuid NOT NULL,
  oper_number integer NOT NULL CHECK (oper_number > 0),
  resource_id uuid NOT NULL,
  setup_time_h numeric(10,4) NOT NULL DEFAULT 0,
  run_time_h_per numeric(10,4) NOT NULL DEFAULT 0,
  process_qty numeric(18,6) NOT NULL,
  scrap_pct numeric(7,4) NOT NULL DEFAULT 0 CHECK (scrap_pct >= 0 AND scrap_pct < 100),
  labor_rate numeric(20,6) NOT NULL DEFAULT 0,
  overhead_rate numeric(20,6) NOT NULL DEFAULT 0,
  subcontract boolean NOT NULL DEFAULT false,
  subcontract_price numeric(20,6) NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','started','finished')),
  PRIMARY KEY (tenant_id, order_id, oper_number),
  FOREIGN KEY (tenant_id, order_id) REFERENCES production_order (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS production_cost_estimate (
  tenant_id uuid NOT NULL,
  order_id uuid NOT NULL,
  cost_group text NOT NULL CHECK (cost_group IN ('material','labor','overhead','subcontract')),
  resource_key text NOT NULL,
  planned_qty numeric(18,6) NOT NULL,
  planned_amount numeric(20,6) NOT NULL,
  fixed_amount numeric(20,6) NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, order_id, cost_group, resource_key),
  FOREIGN KEY (tenant_id, order_id) REFERENCES production_order (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS production_status_log (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  command text NOT NULL,
  from_status text NOT NULL,
  to_status text NOT NULL,
  actor text NOT NULL,
  trace_id text NOT NULL DEFAULT '',
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, order_id) REFERENCES production_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS production_status_log_order ON production_status_log (tenant_id, order_id, occurred_at);

CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Tenant isolation on every table; legal-entity scope where the row carries legal_entity_id.
ALTER TABLE production_order_sequence ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_order_sequence FORCE ROW LEVEL SECURITY;
CREATE POLICY production_order_sequence_tenant_access ON production_order_sequence AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY production_order_sequence_legal_entity_scope ON production_order_sequence AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE production_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_order FORCE ROW LEVEL SECURITY;
CREATE POLICY production_order_tenant_access ON production_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY production_order_legal_entity_scope ON production_order AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE production_status_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_status_log FORCE ROW LEVEL SECURITY;
CREATE POLICY production_status_log_tenant_access ON production_status_log AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY production_status_log_legal_entity_scope ON production_status_log AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE production_bom_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_bom_line FORCE ROW LEVEL SECURITY;
CREATE POLICY production_bom_line_tenant_access ON production_bom_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE production_operation ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_operation FORCE ROW LEVEL SECURITY;
CREATE POLICY production_operation_tenant_access ON production_operation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE production_cost_estimate ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_cost_estimate FORCE ROW LEVEL SECURITY;
CREATE POLICY production_cost_estimate_tenant_access ON production_cost_estimate AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The application role (non-superuser, no BYPASSRLS) gets DML only; RLS applies to it.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON
      production_order_sequence, production_order, production_bom_line, production_operation,
      production_cost_estimate, production_status_log, outbox
    TO passage_app';
  END IF;
END
$$;
