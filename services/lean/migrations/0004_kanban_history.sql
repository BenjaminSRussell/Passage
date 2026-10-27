-- Lean manufacturing, wave 24 slice 6 (PRD 4.24.14 kanban rule analysis,
-- FR-24.12 backflush of conversion cost). A reused fixed-rule card clears its
-- jobs (BR-24.6), so the card and job rows hold only the current cycle. These
-- append-only tables keep every completed cycle and every completed job, so the
-- analysis counts all cycles of a period and the backflush all jobs of it.

-- When the card was last planned: its rule's lead time runs from here, and
-- the analysis measures lead time from here to completion.
ALTER TABLE kanban ADD COLUMN IF NOT EXISTS planned_at timestamptz;

-- One row per completion of a card (one replenishment cycle).
CREATE TABLE IF NOT EXISTS kanban_cycle (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  kanban_id uuid NOT NULL,
  rule_id uuid NOT NULL,
  qty numeric(18,6) NOT NULL CHECK (qty > 0),
  planned_at timestamptz,
  due timestamptz,
  completed_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, kanban_id) REFERENCES kanban (tenant_id, id),
  FOREIGN KEY (tenant_id, rule_id) REFERENCES kanban_rule (tenant_id, id)
);
-- The analysis reads a rule's cycles by completion time.
CREATE INDEX IF NOT EXISTS kanban_cycle_rule_ix ON kanban_cycle (tenant_id, rule_id, completed_at);

-- One row per completed kanban job, with what it reported.
CREATE TABLE IF NOT EXISTS kanban_job_completion (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  job_id uuid NOT NULL,
  kanban_id uuid NOT NULL,
  activity_id uuid NOT NULL,
  started_at timestamptz NOT NULL,
  completed_at timestamptz NOT NULL,
  reported_qty numeric(18,6) NOT NULL CHECK (reported_qty >= 0),
  scrap_qty numeric(18,6) NOT NULL CHECK (scrap_qty >= 0),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, job_id) REFERENCES kanban_job (tenant_id, id),
  FOREIGN KEY (tenant_id, activity_id) REFERENCES flow_activity (tenant_id, id)
);
-- The backflush reads a flow's completions by activity and completion time.
CREATE INDEX IF NOT EXISTS kanban_job_completion_activity_ix ON kanban_job_completion (tenant_id, activity_id, completed_at);

-- Backfill from the current cycle of each card: completed jobs, and the cards
-- whose jobs are all completed. Cycles of reused cards before this migration
-- were cleared with their jobs and cannot be recovered.
INSERT INTO kanban_job_completion (tenant_id, id, job_id, kanban_id, activity_id, started_at, completed_at, reported_qty, scrap_qty)
SELECT j.tenant_id, j.id, j.id, j.kanban_id, j.activity_id, j.started_at, j.completed_at, j.reported_qty, j.scrap_qty
FROM kanban_job j
WHERE j.state = 'completed'
  AND NOT EXISTS (SELECT 1 FROM kanban_job_completion c WHERE c.tenant_id = j.tenant_id AND c.job_id = j.id);

INSERT INTO kanban_cycle (tenant_id, id, kanban_id, rule_id, qty, planned_at, due, completed_at)
SELECT k.tenant_id, k.id, k.id, k.rule_id, k.qty, NULL, k.due, max(j.completed_at)
FROM kanban k JOIN kanban_job j ON j.tenant_id = k.tenant_id AND j.kanban_id = k.id
WHERE k.state IN ('completed', 'emptied')
  AND NOT EXISTS (SELECT 1 FROM kanban_cycle c WHERE c.tenant_id = k.tenant_id AND c.kanban_id = k.id)
GROUP BY k.tenant_id, k.id, k.rule_id, k.qty, k.due
HAVING bool_and(j.state = 'completed');

ALTER TABLE kanban_cycle ENABLE ROW LEVEL SECURITY;
ALTER TABLE kanban_cycle FORCE ROW LEVEL SECURITY;
CREATE POLICY kanban_cycle_tenant_access ON kanban_cycle AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE kanban_job_completion ENABLE ROW LEVEL SECURITY;
ALTER TABLE kanban_job_completion FORCE ROW LEVEL SECURITY;
CREATE POLICY kanban_job_completion_tenant_access ON kanban_job_completion AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON kanban_cycle TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON kanban_job_completion TO passage_app';
  END IF;
END
$$;
