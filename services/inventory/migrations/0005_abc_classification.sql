-- Wave 16 slice 3: periodic ABC classification (FR-16.19). Runs are immutable;
-- each run stores one row per product x site.

CREATE TABLE IF NOT EXISTS abc_run (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  idempotency_key text NOT NULL,
  criterion text NOT NULL CHECK (criterion IN ('VALUE')),
  threshold_a numeric(9,6) NOT NULL CHECK (threshold_a BETWEEN 0 AND 100),
  threshold_b numeric(9,6) NOT NULL CHECK (threshold_b BETWEEN 0 AND 100),
  threshold_c numeric(9,6) NOT NULL CHECK (threshold_c BETWEEN 0 AND 100),
  zero_class text NOT NULL CHECK (zero_class IN ('C', 'N')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (threshold_a + threshold_b + threshold_c = 100)
);
ALTER TABLE abc_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE abc_run FORCE ROW LEVEL SECURITY;
CREATE POLICY abc_run_tenant_access ON abc_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY abc_run_legal_entity_scope ON abc_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS abc_classification (
  tenant_id uuid NOT NULL,
  run_id uuid NOT NULL,
  product_id uuid NOT NULL,
  site text NOT NULL,
  criterion_value numeric(28,6) NOT NULL,
  cum_pct numeric(9,6) NOT NULL,
  class text NOT NULL CHECK (class IN ('A', 'B', 'C', 'N')),
  PRIMARY KEY (tenant_id, run_id, product_id, site),
  FOREIGN KEY (tenant_id, run_id) REFERENCES abc_run (tenant_id, id)
);
ALTER TABLE abc_classification ENABLE ROW LEVEL SECURITY;
ALTER TABLE abc_classification FORCE ROW LEVEL SECURITY;
CREATE POLICY abc_classification_tenant_access ON abc_classification AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
