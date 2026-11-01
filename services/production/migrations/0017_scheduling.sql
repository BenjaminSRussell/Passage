-- Wave 21 slice 18: infinite-capacity scheduling of an order's route (PRD 4.21, FR-21.1,
-- BR-21.1 "Estimated to Scheduled: operation and job dates (infinite capacity in w21)").
-- Additive (expand): new nullable or defaulted columns, one new table; rows written before
-- this migration keep the behaviour they had (an order without dates is unscheduled).

-- Queue and transit hours of an operation, carried by the route snapshot like its setup and
-- run times, and the window the schedule gave it.
ALTER TABLE production_operation
  ADD COLUMN IF NOT EXISTS queue_before_h numeric(10,4) NOT NULL DEFAULT 0 CHECK (queue_before_h >= 0),
  ADD COLUMN IF NOT EXISTS queue_after_h numeric(10,4) NOT NULL DEFAULT 0 CHECK (queue_after_h >= 0),
  ADD COLUMN IF NOT EXISTS transit_h numeric(10,4) NOT NULL DEFAULT 0 CHECK (transit_h >= 0),
  ADD COLUMN IF NOT EXISTS planned_start timestamptz,
  ADD COLUMN IF NOT EXISTS planned_end timestamptz;

-- The order's dates, set by :schedule and cleared when a reset or a cancel takes the
-- schedule back; the method says whether each operation got one job or one job per time.
-- schedule_start is the instant the plan starts at: a route change on a dated order (order
-- edit, applied order change, split, merge, trigger-inserted operation) re-dates it forward
-- from there.
ALTER TABLE production_order
  ADD COLUMN IF NOT EXISTS start_date date,
  ADD COLUMN IF NOT EXISTS end_date date,
  ADD COLUMN IF NOT EXISTS schedule_start timestamptz,
  ADD COLUMN IF NOT EXISTS scheduling_method text NOT NULL DEFAULT 'operations'
    CHECK (scheduling_method IN ('operations', 'jobs'));

-- ProductionJob: a dated slice of an operation (PRD 4.21 entities). seq orders the jobs of
-- an operation. An operation's jobs go with the operation (an order edit removes only an
-- operation without feedback) and are replaced as a set by the next :schedule.
CREATE TABLE IF NOT EXISTS production_job (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  oper_number integer NOT NULL CHECK (oper_number > 0),
  seq integer NOT NULL CHECK (seq > 0),
  job_type text NOT NULL CHECK (job_type IN ('queue-before', 'setup', 'process', 'queue-after', 'transport')),
  resource_id uuid NOT NULL,
  planned_start timestamptz NOT NULL,
  planned_end timestamptz NOT NULL CHECK (planned_end >= planned_start),
  planned_hours numeric(18,6) NOT NULL CHECK (planned_hours >= 0),
  status text NOT NULL DEFAULT 'planned' CHECK (status IN ('planned', 'started', 'finished')),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, order_id, oper_number, seq),
  FOREIGN KEY (tenant_id, order_id, oper_number) REFERENCES production_operation (tenant_id, order_id, oper_number) ON DELETE CASCADE
);
-- The jobs of one resource over a time window (the load a planner reads), and of one order.
CREATE INDEX IF NOT EXISTS production_job_resource ON production_job (tenant_id, resource_id, planned_start);
CREATE INDEX IF NOT EXISTS production_job_start ON production_job (tenant_id, legal_entity_id, planned_start, id);

ALTER TABLE production_job ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_job FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS production_job_tenant_access ON production_job;
CREATE POLICY production_job_tenant_access ON production_job AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS production_job_legal_entity_scope ON production_job;
CREATE POLICY production_job_legal_entity_scope ON production_job AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The store inserts a schedule and deletes the previous one (a re-date, a reset, a cancel or a
-- merge); it never updates a job in this slice.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON production_job TO passage_app';
  END IF;
END
$$;
