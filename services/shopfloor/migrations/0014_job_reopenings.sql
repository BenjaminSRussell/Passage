-- PSG-25 slice 15: supervisor reopen of a finished job (FR-25.3 note). A finished job takes no
-- feedback (SF-0122) and is hidden from the dispatch list; a supervisor reopens it with a reason
-- when more must be reported (a late count, a rework pass, a finish keyed by mistake). Every
-- reopening is kept: who, why, when, the finish it undid and the totals at that moment. The job
-- row itself goes back to `started` with finished_at cleared and its version bumped, so the
-- "(status = 'finished') = (finished_at IS NOT NULL)" check from 0007 still holds.
-- The table is tenant scoped with forced row-level security and append only for the application.

CREATE TABLE IF NOT EXISTS job_reopening (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  job_id uuid NOT NULL,
  reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 500),
  reopened_by text NOT NULL CHECK (char_length(reopened_by) BETWEEN 1 AND 200),
  reopened_at timestamptz NOT NULL DEFAULT now(),
  previous_finished_at timestamptz NOT NULL,
  good_qty numeric(18,4) NOT NULL CHECK (good_qty >= 0),
  scrap_qty numeric(18,4) NOT NULL CHECK (scrap_qty >= 0),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, job_id) REFERENCES shop_job (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS job_reopening_job_ix ON job_reopening (tenant_id, job_id, reopened_at DESC, id);
ALTER TABLE job_reopening ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_reopening FORCE ROW LEVEL SECURITY;
CREATE POLICY job_reopening_tenant_access ON job_reopening AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON job_reopening TO passage_app';
  END IF;
END
$$;
