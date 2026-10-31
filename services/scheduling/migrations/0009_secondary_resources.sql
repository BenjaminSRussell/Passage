-- Wave 23 slice 12: secondary resources booked with the primary (FR-23.4,
-- BR-23.4, SCHED-0005).
--
-- An operation names the secondary resources (tools, crews, a transfer line)
-- it needs for the whole of its job. The scheduler books each of them for the
-- same segments as the primary, so a finite secondary is never held by two
-- jobs at once. The secondary bookings live in their own table so every reader
-- of scheduled_job keeps meaning "the job on its primary resource".

ALTER TABLE scheduling_operation
  ADD COLUMN secondary_resource_ids uuid[] NOT NULL DEFAULT '{}' CHECK (cardinality(secondary_resource_ids) <= 5);

CREATE TABLE IF NOT EXISTS scheduled_job_secondary (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  job_id uuid NOT NULL,
  resource_id uuid NOT NULL,
  segment_no integer NOT NULL CHECK (segment_no >= 1),
  start_at timestamptz NOT NULL,
  end_at timestamptz NOT NULL,
  run_id uuid NOT NULL,
  finite boolean NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, job_id, resource_id, segment_no),
  CHECK (end_at > start_at),
  FOREIGN KEY (tenant_id, job_id) REFERENCES scheduling_operation (tenant_id, id),
  FOREIGN KEY (tenant_id, resource_id) REFERENCES scheduling_resource (tenant_id, id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES schedule_run (tenant_id, id),
  EXCLUDE USING gist (tenant_id WITH =, resource_id WITH =, tstzrange(start_at, end_at) WITH &&) WHERE (finite)
);
CREATE INDEX IF NOT EXISTS scheduled_job_secondary_resource_start ON scheduled_job_secondary (tenant_id, resource_id, start_at);
CREATE INDEX IF NOT EXISTS scheduled_job_secondary_job ON scheduled_job_secondary (tenant_id, job_id);
ALTER TABLE scheduled_job_secondary ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduled_job_secondary FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduled_job_secondary_tenant_access ON scheduled_job_secondary AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON scheduled_job_secondary TO passage_app';
  END IF;
END
$$;
