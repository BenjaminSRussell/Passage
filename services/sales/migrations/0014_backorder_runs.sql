-- Wave 27 slice 10: stored backorder processing runs (FR-27.17, BR-27.11, AC-27.12).
-- A run is created as a PREVIEW over the open lines of one product at one site
-- and warehouse, read from the confirmed orders and their schedule lines, and
-- keeps what each line has now (before_qty, the quantity with a promised date)
-- and what the strategy gives it (after_qty). Committing applies every changed
-- line in one transaction. order_version is the order's version the preview
-- was computed on: a commit refuses an order that changed since.
-- gain_date is the date a line that gains quantity is promised: never before
-- the line's requested date, the supply date or its quota period.

CREATE TABLE IF NOT EXISTS backorder_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  product_id uuid NOT NULL,
  site_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  strategy text NOT NULL CHECK (strategy IN ('WIN', 'GAIN', 'REDISTRIBUTE', 'FILL', 'LOSE')),
  supply numeric(20,6) NOT NULL CHECK (supply >= 0),
  supply_date date NOT NULL,
  lose_threshold int NOT NULL CHECK (lose_threshold BETWEEN 1 AND 9),
  default_priority int NOT NULL CHECK (default_priority BETWEEN 1 AND 9),
  status text NOT NULL CHECK (status IN ('PREVIEW', 'COMMITTED')),
  allocated numeric(20,6) NOT NULL,
  deconfirmed_count int NOT NULL,
  changed_count int NOT NULL,
  version int NOT NULL DEFAULT 1,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  committed_by text,
  committed_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((status = 'COMMITTED') = (committed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS backorder_run_product ON backorder_run (tenant_id, legal_entity_id, product_id, created_at DESC);

CREATE TABLE IF NOT EXISTS backorder_run_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  run_id uuid NOT NULL,
  seq int NOT NULL CHECK (seq >= 1),
  order_id uuid NOT NULL,
  order_number text NOT NULL,
  order_version int NOT NULL,
  line_id uuid NOT NULL,
  line_no int NOT NULL,
  customer_id uuid NOT NULL,
  priority_class int NOT NULL CHECK (priority_class BETWEEN 1 AND 9),
  requested_date date NOT NULL,
  order_value numeric(20,6) NOT NULL CHECK (order_value >= 0),
  demand numeric(20,6) NOT NULL CHECK (demand > 0),
  before_qty numeric(20,6) NOT NULL CHECK (before_qty >= 0),
  after_qty numeric(20,6) NOT NULL CHECK (after_qty >= 0),
  changed boolean NOT NULL,
  deconfirmed boolean NOT NULL,
  gain_date date,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, run_id, seq),
  UNIQUE (tenant_id, run_id, line_id)
);
CREATE INDEX IF NOT EXISTS backorder_run_line_order ON backorder_run_line (tenant_id, order_id);

ALTER TABLE backorder_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE backorder_run FORCE ROW LEVEL SECURITY;
CREATE POLICY backorder_run_tenant_access ON backorder_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY backorder_run_legal_entity_scope ON backorder_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE backorder_run_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE backorder_run_line FORCE ROW LEVEL SECURITY;
CREATE POLICY backorder_run_line_tenant_access ON backorder_run_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY backorder_run_line_legal_entity_scope ON backorder_run_line AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The store inserts runs and lines and updates a run's status when it commits;
-- run lines are never changed and nothing is deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON backorder_run TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON backorder_run_line TO passage_app';
  END IF;
END
$$;
