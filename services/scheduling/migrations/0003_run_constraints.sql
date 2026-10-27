-- Wave 23 slice 3: schedule runs apply resource networks (FR-23.13), setup
-- matrix changeovers (FR-23.15) and capacity blocks (FR-23.17).
--
-- An operation carries its setup group, the route's static setup minutes
-- (used when the matrix has no entry for a pair, SCHED-0012), the attributes
-- block conditions are matched against, and the network that governs the step
-- from the order's previous operation into it. A scheduled job records the
-- changeover laid in front of its process minutes on its first segment, and a
-- run records its warnings (SCHED-0010, SCHED-0012, SCHED-0014). The tables
-- already have row-level security; new columns inherit it.

ALTER TABLE scheduling_operation
  ADD COLUMN setup_group text CHECK (setup_group IS NULL OR char_length(setup_group) BETWEEN 1 AND 32),
  ADD COLUMN setup_minutes integer NOT NULL DEFAULT 0 CHECK (setup_minutes BETWEEN 0 AND 10080),
  ADD COLUMN attributes jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(attributes) = 'object'),
  ADD COLUMN network_id uuid,
  ADD CONSTRAINT scheduling_operation_network_fk FOREIGN KEY (tenant_id, network_id) REFERENCES scheduling_resource_network (tenant_id, id);

ALTER TABLE scheduled_job
  ADD COLUMN changeover_minutes integer NOT NULL DEFAULT 0 CHECK (changeover_minutes >= 0),
  ADD CONSTRAINT scheduled_job_changeover_on_first_segment CHECK (changeover_minutes = 0 OR segment_no = 1);

ALTER TABLE schedule_run
  ADD COLUMN warnings jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(warnings) = 'array');

-- The setup group a resource is in at a run's start is that of its last job
-- before the start; this index serves that lookup.
CREATE INDEX IF NOT EXISTS scheduled_job_resource_end ON scheduled_job (tenant_id, resource_id, end_at DESC);
