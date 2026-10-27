-- PSG-25 slice 8: job dispatch from registrations (FR-25.3, BR-25.7).
-- flags carries review flags on a registration: `job-finished-conflict` on a job-start
-- for a job the server had already finished (BR-25.7, SF-0004), `auto-stopped` and
-- `auto-stop-capped` on the job-stops the server writes when a worker starts another job
-- (FR-25.3, SF-0002). The table keeps its forced row-level security from 0001.

ALTER TABLE time_registration ADD COLUMN IF NOT EXISTS flags text[] NOT NULL DEFAULT '{}';

-- The open job-starts of a worker are read on every job-start (one open unit per worker).
CREATE INDEX IF NOT EXISTS time_registration_worker_jobs ON time_registration (tenant_id, worker_id, job_id, started_at)
  WHERE kind IN ('job-start', 'job-stop');
