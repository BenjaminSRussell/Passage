CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS quality_instrument (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,31}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  calibration_interval_days int NOT NULL CHECK (calibration_interval_days BETWEEN 1 AND 3650),
  last_calibrated_at timestamptz NOT NULL,
  due_at timestamptz NOT NULL,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE quality_instrument ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_instrument FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_instrument_tenant_access ON quality_instrument AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Test group versions are immutable rows; a new version is inserted (FR-33.1).
CREATE TABLE IF NOT EXISTS quality_test_group (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,31}$'),
  version int NOT NULL CHECK (version >= 1),
  tests jsonb NOT NULL CHECK (jsonb_typeof(tests) = 'array'),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code, version),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE quality_test_group ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_test_group FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_test_group_tenant_access ON quality_test_group AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS quality_association (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  trigger text NOT NULL CHECK (trigger IN ('PURCHASE_RECEIPT','PRODUCTION_RAF','SALES_PICK','TRANSFER_RECEIPT','QUARANTINE','SOURCE_INSPECTION')),
  item_scope text NOT NULL CHECK (item_scope IN ('ITEM','ITEM_GROUP','ALL')),
  item_ref uuid,
  party_scope text NOT NULL CHECK (party_scope IN ('PARTY','ALL')),
  party_ref uuid,
  site_scope text NOT NULL CHECK (site_scope IN ('SITE','ALL')),
  site_ref uuid,
  sequence int NOT NULL CHECK (sequence >= 0),
  test_group_id uuid NOT NULL,
  sampling_method text NOT NULL CHECK (sampling_method IN ('FIXED','PERCENT','AQL')),
  fixed_qty numeric(18,6) NOT NULL DEFAULT 0,
  percent numeric(9,6) NOT NULL DEFAULT 0,
  aql numeric(4,2) NOT NULL DEFAULT 0,
  severity text NOT NULL DEFAULT 'NORMAL' CHECK (severity IN ('NORMAL','TIGHTENED','REDUCED')),
  inspection_level text NOT NULL DEFAULT 'II' CHECK (inspection_level = 'II'),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, test_group_id) REFERENCES quality_test_group (tenant_id, id)
);
ALTER TABLE quality_association ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_association FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_association_tenant_access ON quality_association AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS quality_order (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  trigger text NOT NULL,
  source_ref jsonb NOT NULL,
  item_id uuid NOT NULL,
  site_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  dims jsonb NOT NULL DEFAULT '{}'::jsonb,
  lot_qty numeric(18,6) NOT NULL CHECK (lot_qty > 0),
  sample_qty numeric(18,6) NOT NULL CHECK (sample_qty > 0 AND sample_qty <= lot_qty),
  uom text NOT NULL CHECK (char_length(uom) BETWEEN 1 AND 10),
  test_group_id uuid NOT NULL,
  test_group_version int NOT NULL,
  sampling_method text NOT NULL,
  inspection_severity text NOT NULL CHECK (inspection_severity IN ('NORMAL','TIGHTENED','REDUCED')),
  code_letter text NOT NULL,
  accept_no int NOT NULL,
  reject_no int NOT NULL,
  status text NOT NULL CHECK (status IN ('CREATED','OPEN','RESULTS_IN_PROGRESS','PENDING_DECISION','CLOSED','CANCELLED')),
  version int NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  closed_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, test_group_id) REFERENCES quality_test_group (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS quality_order_status_site ON quality_order (tenant_id, status, site_id);
CREATE INDEX IF NOT EXISTS quality_order_created ON quality_order (tenant_id, created_at DESC, id);
-- One order per source line per trigger event (BR-33.3); manual orders are exempt.
CREATE UNIQUE INDEX IF NOT EXISTS quality_order_source_line ON quality_order
  (tenant_id, trigger, (source_ref->>'type'), (source_ref->>'id'), (coalesce(source_ref->>'line', '')))
  WHERE trigger <> 'MANUAL';
ALTER TABLE quality_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_order FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_order_tenant_access ON quality_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS quality_result (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  quality_order_id uuid NOT NULL,
  test_id uuid NOT NULL,
  sample_no int NOT NULL CHECK (sample_no >= 1),
  retest_no int NOT NULL DEFAULT 0 CHECK (retest_no >= 0),
  value_numeric numeric(18,6),
  outcome_id uuid,
  passed boolean NOT NULL,
  instrument_id uuid,
  recorded_by text NOT NULL,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, quality_order_id, test_id, sample_no, retest_no),
  FOREIGN KEY (tenant_id, quality_order_id) REFERENCES quality_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS quality_result_batch ON quality_result (tenant_id, quality_order_id, idempotency_key);
ALTER TABLE quality_result ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_result FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_result_tenant_access ON quality_result AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- One usage decision per order: CLOSED is final (BR-33.4).
CREATE TABLE IF NOT EXISTS quality_usage_decision (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  quality_order_id uuid NOT NULL,
  lines jsonb NOT NULL,
  accept_qty numeric(18,6) NOT NULL CHECK (accept_qty >= 0),
  reject_qty numeric(18,6) NOT NULL CHECK (reject_qty >= 0),
  deviation_qty numeric(18,6) NOT NULL CHECK (deviation_qty >= 0),
  decided_by text NOT NULL,
  decided_at timestamptz NOT NULL DEFAULT now(),
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, quality_order_id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, quality_order_id) REFERENCES quality_order (tenant_id, id)
);
ALTER TABLE quality_usage_decision ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_usage_decision FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_usage_decision_tenant_access ON quality_usage_decision AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
