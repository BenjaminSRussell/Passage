-- Wave 33 slice 3: nonconformances (FR-33.7) and their 8D correction (FR-33.8, BR-33.9).

-- A nonconformance report. version is the optimistic version of the NCR and its
-- correction together. A reject usage decision raises one (FR-33.6); an NCR that
-- references a closed order amends its decision (BR-33.4).
CREATE TABLE IF NOT EXISTS quality_nonconformance (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  source text NOT NULL CHECK (source IN ('CUSTOMER','VENDOR','INTERNAL','PRODUCTION','SERVICE')),
  item_id uuid NOT NULL,
  dims jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(dims) = 'object'),
  qty numeric(18,6) NOT NULL CHECK (qty > 0),
  uom text NOT NULL CHECK (char_length(uom) BETWEEN 1 AND 10),
  problem_type text NOT NULL CHECK (problem_type ~ '^[A-Z0-9][A-Z0-9_-]{1,31}$'),
  severity text NOT NULL CHECK (severity IN ('MINOR','MAJOR','CRITICAL')),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
  quality_order_id uuid,
  vendor_id uuid,
  customer_id uuid,
  production_order_id uuid,
  quarantine_order_id uuid,
  status text NOT NULL CHECK (status IN ('NEW','APPROVED','REFUSED','IN_CORRECTION','CLOSED')),
  status_reason text NOT NULL DEFAULT '' CHECK (char_length(status_reason) <= 500),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version int NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, quality_order_id) REFERENCES quality_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS quality_nonconformance_vendor ON quality_nonconformance (tenant_id, vendor_id, created_at);
CREATE INDEX IF NOT EXISTS quality_nonconformance_status ON quality_nonconformance (tenant_id, status, created_at DESC, id);
CREATE INDEX IF NOT EXISTS quality_nonconformance_order ON quality_nonconformance (tenant_id, quality_order_id) WHERE quality_order_id IS NOT NULL;
ALTER TABLE quality_nonconformance ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_nonconformance FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_nonconformance_tenant_access ON quality_nonconformance AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The 8D correction of one nonconformance (FR-33.8). steps is the array of the
-- eight disciplines with their due dates, notes and completion.
CREATE TABLE IF NOT EXISTS quality_correction (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  nonconformance_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('OPEN','EFFECTIVENESS_PENDING','CLOSED')),
  current_step int NOT NULL CHECK (current_step BETWEEN 1 AND 8),
  steps jsonb NOT NULL CHECK (jsonb_typeof(steps) = 'array' AND jsonb_array_length(steps) = 8),
  started_by text NOT NULL,
  started_at timestamptz NOT NULL,
  effectiveness_days int NOT NULL CHECK (effectiveness_days BETWEEN 30 AND 365),
  effectiveness_due_at timestamptz,
  effectiveness_result text CHECK (effectiveness_result IN ('EFFECTIVE','NOT_EFFECTIVE')),
  effectiveness_note text NOT NULL DEFAULT '' CHECK (char_length(effectiveness_note) <= 2000),
  effectiveness_by text NOT NULL DEFAULT '',
  effectiveness_at timestamptz,
  reopened int NOT NULL DEFAULT 0 CHECK (reopened >= 0),
  closed_at timestamptz,
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, nonconformance_id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, nonconformance_id) REFERENCES quality_nonconformance (tenant_id, id)
);
ALTER TABLE quality_correction ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_correction FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_correction_tenant_access ON quality_correction AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The NCR a reject decision raised (FR-33.6), so a replayed decision returns it.
ALTER TABLE quality_usage_decision ADD COLUMN IF NOT EXISTS nonconformance_id uuid;
