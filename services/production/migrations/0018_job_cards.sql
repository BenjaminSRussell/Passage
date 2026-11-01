-- Wave 21 slice 19: job cards (PRD 4.21 FR-21.6, journal type `job-card`): time and quantities
-- reported per ProductionJob. Additive (expand): new defaulted columns and a wider check; rows
-- written before this migration keep the behaviour they had.

-- A job card is a production journal of its own type.
ALTER TABLE production_journal DROP CONSTRAINT IF EXISTS production_journal_journal_type_check;
ALTER TABLE production_journal ADD CONSTRAINT production_journal_journal_type_check
  CHECK (journal_type IN ('picking-list', 'route-card', 'report-as-finished', 'split-transfer', 'job-card'));

-- A job-card hours line names the job of its operation by type (an operation has at most one job
-- of each type, and a re-date gives jobs new ids, so the type is the stable name).
ALTER TABLE production_journal_line
  ADD COLUMN IF NOT EXISTS job_type text CHECK (job_type IN ('setup', 'process'));

-- What the job cards reported on a job; the job is `started` by its first card and `finished`
-- by a card that finishes it.
ALTER TABLE production_job
  ADD COLUMN IF NOT EXISTS hours_reported numeric(18,6) NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS qty_good numeric(18,6) NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS qty_error numeric(18,6) NOT NULL DEFAULT 0;

-- Posting a job card and reversing it update the job (0017 granted SELECT, INSERT, DELETE).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE ON production_job TO passage_app';
  END IF;
END
$$;
