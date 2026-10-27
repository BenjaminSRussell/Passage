-- Lean manufacturing, wave 24 slice 5 (PRD 4.24.4 FlowCostingPeriod, FR-24.12,
-- BR-24.9): periodic costing periods of a production flow. A period is opened
-- for a date range and settled once, after its end, when every card with jobs
-- in it is Completed or Emptied. Amounts are money (numeric(20,6) with the
-- period's currency). The ledger journal is a handoff, so journal_entry_id
-- stays empty until the ledger integration posts it.

CREATE TABLE IF NOT EXISTS flow_costing_period (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  flow_id uuid NOT NULL,
  period_start date NOT NULL,
  period_end date NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'settled')),
  material_cost numeric(20,6) CHECK (material_cost IS NULL OR material_cost >= 0),
  conversion_cost numeric(20,6) CHECK (conversion_cost IS NULL OR conversion_cost >= 0),
  output_value numeric(20,6) CHECK (output_value IS NULL OR output_value >= 0),
  variance numeric(20,6),
  -- Cards with jobs in the period when it was settled (BR-24.9).
  settled_kanbans integer CHECK (settled_kanbans IS NULL OR settled_kanbans >= 0),
  journal_entry_id uuid,
  settled_at timestamptz,
  settled_by text,
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (period_end >= period_start),
  CHECK (status = 'open' OR (settled_at IS NOT NULL AND material_cost IS NOT NULL AND conversion_cost IS NOT NULL
    AND output_value IS NOT NULL AND variance IS NOT NULL AND settled_kanbans IS NOT NULL)),
  -- BR-24.9: the stored variance always balances the stored costs.
  CHECK (variance IS NULL OR variance = material_cost + conversion_cost - output_value),
  PRIMARY KEY (tenant_id, id),
  -- One period per flow and start date; overlaps are refused by the store
  -- under a lock on the flow.
  UNIQUE (tenant_id, flow_id, period_start),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, flow_id) REFERENCES production_flow (tenant_id, id)
);

-- The settlement gate reads a flow's jobs by start time.
CREATE INDEX IF NOT EXISTS kanban_job_started_ix ON kanban_job (tenant_id, activity_id, started_at) WHERE started_at IS NOT NULL;

ALTER TABLE flow_costing_period ENABLE ROW LEVEL SECURITY;
ALTER TABLE flow_costing_period FORCE ROW LEVEL SECURITY;
CREATE POLICY flow_costing_period_tenant_access ON flow_costing_period AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON flow_costing_period TO passage_app';
  END IF;
END
$$;
