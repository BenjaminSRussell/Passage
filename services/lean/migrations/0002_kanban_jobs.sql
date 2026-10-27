-- Lean manufacturing, wave 24 slice 4 (PRD 4.24.4): kanban jobs, one per
-- activity of the rule's production flow, and the handling units a card
-- reports when its last job completes (FR-24.10, BR-24.6).

ALTER TABLE kanban ADD COLUMN IF NOT EXISTS handling_units integer CHECK (handling_units IS NULL OR handling_units >= 1);

-- A card's work at one flow activity. Before any job starts, the jobs follow
-- the card through Not planned, Planned and Prepared; the first started job
-- moves the card to In progress and the last completed job completes it.
CREATE TABLE IF NOT EXISTS kanban_job (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  kanban_id uuid NOT NULL,
  activity_id uuid NOT NULL,
  sequence integer NOT NULL CHECK (sequence >= 1),
  state text NOT NULL CHECK (state IN ('not-planned', 'planned', 'prepared', 'in-progress', 'completed')),
  started_at timestamptz,
  completed_at timestamptz,
  reported_qty numeric(18,6) CHECK (reported_qty IS NULL OR reported_qty >= 0),
  scrap_qty numeric(18,6) CHECK (scrap_qty IS NULL OR scrap_qty >= 0),
  handling_units integer CHECK (handling_units IS NULL OR handling_units >= 1),
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (state <> 'completed' OR (completed_at IS NOT NULL AND reported_qty IS NOT NULL AND scrap_qty IS NOT NULL AND handling_units IS NOT NULL)),
  CHECK (state NOT IN ('in-progress', 'completed') OR started_at IS NOT NULL),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, kanban_id, sequence),
  FOREIGN KEY (tenant_id, kanban_id) REFERENCES kanban (tenant_id, id),
  FOREIGN KEY (tenant_id, activity_id) REFERENCES flow_activity (tenant_id, id)
);
-- PRD 4.24.4 index for the board: KanbanJob (activity_id, state).
CREATE INDEX IF NOT EXISTS kanban_job_board_ix ON kanban_job (tenant_id, activity_id, state);
-- The work-cell board reads activities by work cell.
CREATE INDEX IF NOT EXISTS flow_activity_work_cell_ix ON flow_activity (tenant_id, work_cell_resource_id) WHERE work_cell_resource_id IS NOT NULL;

ALTER TABLE kanban_job ENABLE ROW LEVEL SECURITY;
ALTER TABLE kanban_job FORCE ROW LEVEL SECURITY;
CREATE POLICY kanban_job_tenant_access ON kanban_job AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON kanban_job TO passage_app';
  END IF;
END
$$;
