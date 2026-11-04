-- Wave 23 slice 29: per-run job snapshots, run comparison and rollback to a
-- previous run (PRD 4.23.14 Run comparison, rollback).
--
-- A run replaces the jobs it schedules, so scheduled_job only ever holds the
-- latest plan. schedule_run_job keeps what each run placed, one row per
-- segment, written in the run's own transaction and never changed afterwards.
-- Comparison reads two snapshots; rollback writes a snapshot back as the live
-- plan under a new run of scope 'rollback'. There is no foreign key to the
-- operation or the resource: an operation deleted or an order withdrawn after
-- the run must not be blocked by history, and rollback skips what no longer
-- exists. Runs written before this migration have snapshot = false and cannot
-- be compared or rolled back to. The primary key leads with (tenant_id,
-- run_id), which is how every read finds a run's rows.
SET LOCAL lock_timeout = '5s';

ALTER TABLE schedule_run
  ADD COLUMN IF NOT EXISTS snapshot boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS rollback_of uuid;
ALTER TABLE schedule_run DROP CONSTRAINT IF EXISTS schedule_run_scope_check;
ALTER TABLE schedule_run ADD CONSTRAINT schedule_run_scope_check
  CHECK (scope IN ('order', 'selection', 'site', 'move', 'rollback'));
ALTER TABLE schedule_run DROP CONSTRAINT IF EXISTS schedule_run_rollback_of_fk;
ALTER TABLE schedule_run ADD CONSTRAINT schedule_run_rollback_of_fk
  FOREIGN KEY (tenant_id, rollback_of) REFERENCES schedule_run (tenant_id, id);

CREATE TABLE IF NOT EXISTS schedule_run_job (
  tenant_id uuid NOT NULL,
  run_id uuid NOT NULL,
  job_id uuid NOT NULL,
  segment_no integer NOT NULL CHECK (segment_no >= 1),
  order_id uuid NOT NULL,
  oper_number integer NOT NULL,
  resource_id uuid NOT NULL,
  start_at timestamptz NOT NULL,
  end_at timestamptz NOT NULL,
  late_minutes integer NOT NULL DEFAULT 0 CHECK (late_minutes >= 0),
  changeover_minutes integer NOT NULL DEFAULT 0 CHECK (changeover_minutes >= 0),
  changeover_cost numeric(20, 6) CHECK (changeover_cost >= 0),
  changeover_currency char(3) CHECK (changeover_currency ~ '^[A-Z]{3}$'),
  cleaning_operation_set_id text CHECK (char_length(cleaning_operation_set_id) BETWEEN 1 AND 64),
  secondary_resource_ids uuid[] NOT NULL DEFAULT '{}' CHECK (cardinality(secondary_resource_ids) <= 5),
  -- The operation's version when the run placed it: rollback skips an
  -- operation changed or re-imported since, whose placement is out of date.
  operation_version integer NOT NULL CHECK (operation_version >= 1),
  PRIMARY KEY (tenant_id, run_id, job_id, segment_no),
  CHECK (end_at > start_at),
  FOREIGN KEY (tenant_id, run_id) REFERENCES schedule_run (tenant_id, id)
);
ALTER TABLE schedule_run_job ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedule_run_job FORCE ROW LEVEL SECURITY;
CREATE POLICY schedule_run_job_tenant_access ON schedule_run_job AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON schedule_run_job TO passage_app';
  END IF;
END
$$;
