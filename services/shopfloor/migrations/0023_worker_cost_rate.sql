-- PSG-25 slice 23: effective-dated worker cost rates (D2a'). A row says "from this date the worker
-- costs this much an hour"; a day is priced at the latest row effective on or before its date, and
-- at worker.cost_rate for days before the first row, so a raise or a correction no longer reprices
-- the days before it when a supervisor recalculates them. Rows are append-only (a rate for a date
-- is set once; a later change gets a later date), so the table is granted SELECT and INSERT only.
-- The currency stays on the worker (one currency per worker, locked once pay is approved).
-- New table, no lock on a live one.

CREATE TABLE IF NOT EXISTS worker_cost_rate (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  worker_id uuid NOT NULL,
  effective_from date NOT NULL,
  cost_rate numeric(20, 6) NOT NULL CHECK (cost_rate >= 0),
  note text CHECK (char_length(note) BETWEEN 1 AND 500),
  created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, worker_id, effective_from),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id)
);
ALTER TABLE worker_cost_rate ENABLE ROW LEVEL SECURITY;
ALTER TABLE worker_cost_rate FORCE ROW LEVEL SECURITY;
CREATE POLICY worker_cost_rate_tenant_access ON worker_cost_rate AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON worker_cost_rate TO passage_app';
  END IF;
END
$$;
