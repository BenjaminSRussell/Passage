-- Wave 23 slice 7: each scheduled changeover carries the cost and cleaning
-- standard operation set of the setup matrix entry that priced it (FR-23.15),
-- so the changeover analysis report (PRD 4.23.14) and campaign settlement
-- (FR-23.16) read them from ScheduledJob rather than repricing.
--
-- Money is numeric(20,6) with an ISO currency (the matrix currency). Both are
-- null when no matrix entry priced the job: no change of setup group, or the
-- route setup was used (SCHED-0012). Like the changeover minutes, they sit on
-- the job's first segment only. The table already has row-level security; new
-- columns inherit it.

ALTER TABLE scheduled_job
  ADD COLUMN changeover_cost numeric(20,6) CHECK (changeover_cost >= 0),
  ADD COLUMN changeover_currency char(3) CHECK (changeover_currency ~ '^[A-Z]{3}$'),
  ADD COLUMN cleaning_operation_set_id text CHECK (char_length(cleaning_operation_set_id) BETWEEN 1 AND 64),
  ADD CONSTRAINT scheduled_job_changeover_cost_currency CHECK ((changeover_cost IS NULL) = (changeover_currency IS NULL)),
  ADD CONSTRAINT scheduled_job_changeover_cost_on_first_segment
    CHECK (segment_no = 1 OR (changeover_cost IS NULL AND cleaning_operation_set_id IS NULL));
