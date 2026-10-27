-- PSG-25 slice 7: the dispatch projection of scheduled jobs (FR-25.3), operator feedback
-- with good and scrap quantities and the finished flag (FR-25.4), and quality check results
-- with the finish gate and supervisor override (FR-25.5, AC-25.9, SF-0005). The projection
-- is written by the w23 schedule through POST /api/v1/shopfloor/jobs. Every table is tenant
-- scoped with forced RLS. Quantities use numeric(18,4) like time_registration.

CREATE TABLE IF NOT EXISTS shop_job (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  order_ref text NOT NULL CHECK (char_length(order_ref) BETWEEN 1 AND 64),
  operation_ref text NOT NULL CHECK (char_length(operation_ref) BETWEEN 1 AND 64),
  resource_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'released' CHECK (status IN ('released', 'started', 'finished')),
  planned_qty numeric(18,4) NOT NULL CHECK (planned_qty >= 0),
  good_qty numeric(18,4) NOT NULL DEFAULT 0 CHECK (good_qty >= 0),
  scrap_qty numeric(18,4) NOT NULL DEFAULT 0 CHECK (scrap_qty >= 0),
  sequence_start timestamptz NOT NULL,
  -- Check definitions attached to the operation: [{code, name, kind, min, max, mandatory}].
  checks jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(checks) = 'array'),
  finished_at timestamptz,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK ((status = 'finished') = (finished_at IS NOT NULL))
);
-- Dispatch list: per resource, jobs in start order.
CREATE INDEX IF NOT EXISTS shop_job_dispatch_ix ON shop_job (tenant_id, resource_id, sequence_start, id);
ALTER TABLE shop_job ENABLE ROW LEVEL SECURITY;
ALTER TABLE shop_job FORCE ROW LEVEL SECURITY;
CREATE POLICY shop_job_tenant_access ON shop_job AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- One row per client-generated feedback id, so an offline replay is a no-op (FR-25.12).
-- checks keeps the values of this posting with their evaluation, for audit; the latest value
-- per check is in job_check_result. A finish with failed mandatory checks carries the
-- supervisor's override reason and recorded_by is the supervisor.
CREATE TABLE IF NOT EXISTS job_feedback (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  job_id uuid NOT NULL,
  good_qty numeric(18,4) NOT NULL CHECK (good_qty >= 0),
  scrap_qty numeric(18,4) NOT NULL CHECK (scrap_qty >= 0),
  scrap_reason_id text CHECK (scrap_reason_id IS NULL OR char_length(scrap_reason_id) BETWEEN 1 AND 64),
  finished boolean NOT NULL,
  checks jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(checks) = 'array'),
  override_reason text CHECK (override_reason IS NULL OR char_length(override_reason) BETWEEN 1 AND 500),
  recorded_by text,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, job_id) REFERENCES shop_job (tenant_id, id),
  CHECK (scrap_qty = 0 OR scrap_reason_id IS NOT NULL),
  CHECK (override_reason IS NULL OR finished)
);
CREATE INDEX IF NOT EXISTS job_feedback_job_ix ON job_feedback (tenant_id, job_id, recorded_at);
ALTER TABLE job_feedback ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_feedback FORCE ROW LEVEL SECURITY;
CREATE POLICY job_feedback_tenant_access ON job_feedback AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Latest recorded value per check code on a job, and the feedback that recorded it.
CREATE TABLE IF NOT EXISTS job_check_result (
  tenant_id uuid NOT NULL,
  job_id uuid NOT NULL,
  check_code text NOT NULL CHECK (char_length(check_code) BETWEEN 1 AND 32),
  value text NOT NULL CHECK (char_length(value) BETWEEN 1 AND 64),
  pass boolean NOT NULL,
  feedback_id uuid NOT NULL,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, job_id, check_code),
  FOREIGN KEY (tenant_id, job_id) REFERENCES shop_job (tenant_id, id),
  FOREIGN KEY (tenant_id, feedback_id) REFERENCES job_feedback (tenant_id, id)
);
ALTER TABLE job_check_result ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_check_result FORCE ROW LEVEL SECURITY;
CREATE POLICY job_check_result_tenant_access ON job_check_result AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON shop_job, job_feedback, job_check_result TO passage_app';
  END IF;
END
$$;
