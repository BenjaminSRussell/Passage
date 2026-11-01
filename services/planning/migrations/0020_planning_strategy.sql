-- FR-22.19 / BR-22.13: planning strategy setup. One row per strategy code and
-- tenant carries how the strategy treats forecast against sales demand: the
-- consumption mode with its backward and forward windows (assemble-to-order),
-- whether pegging is marked and whether order-specific stock is segregated. A
-- strategy without a row takes the PRD defaults (backward 30 days, forward 0).
-- Shared tenant-level setup like coverage_group, so no legal_entity_id.
CREATE TABLE IF NOT EXISTS planning_strategy (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code IN ('mts-net', 'mts-gross', 'mto', 'assemble-to-order-final', 'plan-without-final-assembly', 'subassembly')),
  consumption_mode text NOT NULL DEFAULT 'backward' CHECK (consumption_mode IN ('backward', 'forward', 'backward-forward')),
  backward_days integer NOT NULL DEFAULT 30 CHECK (backward_days BETWEEN 0 AND 365),
  forward_days integer NOT NULL DEFAULT 0 CHECK (forward_days BETWEEN 0 AND 365),
  uses_marking boolean NOT NULL DEFAULT false,
  segregated_stock boolean NOT NULL DEFAULT false,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  input_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT planning_strategy_code_key UNIQUE (tenant_id, code),
  CONSTRAINT planning_strategy_idempotency_key UNIQUE (tenant_id, idempotency_key),
  CHECK ((idempotency_key IS NULL) = (input_hash IS NULL))
);
ALTER TABLE planning_strategy ENABLE ROW LEVEL SECURITY;
ALTER TABLE planning_strategy FORCE ROW LEVEL SECURITY;
CREATE POLICY planning_strategy_tenant_access ON planning_strategy AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- passage_app: the store inserts and updates (If-Match), it never deletes.
-- Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON planning_strategy TO passage_app';
  END IF;
END
$$;
