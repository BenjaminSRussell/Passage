-- FR-22.2: coverage group setup with the defaults of the PRD (coverage time
-- fence 100 days, negative days 1, positive days 0, firming and freeze fences
-- 0, auto-firm off). version is the ETag every update bumps; idempotency_key
-- and input_hash let a retried create replay instead of answering 409.
CREATE TABLE IF NOT EXISTS coverage_group (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  coverage_code text NOT NULL CHECK (coverage_code IN ('requirement', 'period', 'minmax', 'manual')),
  coverage_time_fence_days integer NOT NULL DEFAULT 100 CHECK (coverage_time_fence_days BETWEEN 1 AND 3650),
  negative_days integer NOT NULL DEFAULT 1 CHECK (negative_days BETWEEN 0 AND 365),
  positive_days integer NOT NULL DEFAULT 0 CHECK (positive_days BETWEEN 0 AND 365),
  period_days integer NOT NULL DEFAULT 0 CHECK (period_days BETWEEN 0 AND 365),
  freeze_fence_days integer NOT NULL DEFAULT 0 CHECK (freeze_fence_days BETWEEN 0 AND 3650),
  action_fence_days integer NOT NULL DEFAULT 0 CHECK (action_fence_days BETWEEN 0 AND 3650),
  futures_fence_days integer NOT NULL DEFAULT 0 CHECK (futures_fence_days BETWEEN 0 AND 3650),
  firming_fence_days integer NOT NULL DEFAULT 0 CHECK (firming_fence_days BETWEEN 0 AND 3650),
  auto_firm boolean NOT NULL DEFAULT false,
  generate_actions boolean NOT NULL DEFAULT true,
  generate_futures boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  input_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT coverage_group_code_key UNIQUE (tenant_id, code),
  CONSTRAINT coverage_group_idempotency_key UNIQUE (tenant_id, idempotency_key),
  CHECK (NOT auto_firm OR firming_fence_days > 0),
  CHECK (coverage_code <> 'period' OR period_days > 0),
  CHECK ((idempotency_key IS NULL) = (input_hash IS NULL))
);
ALTER TABLE coverage_group ENABLE ROW LEVEL SECURITY;
ALTER TABLE coverage_group FORCE ROW LEVEL SECURITY;
CREATE POLICY coverage_group_tenant_access ON coverage_group AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
