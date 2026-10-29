-- Migration factory, wave 49 slice 4 (FR-49.6, 9.49.6): starting a runbook step. cutover_step
-- gains actual_start (9.49.4) as started_at, with the user who started it, so the runbook shows
-- elapsed against planned while the step runs and a completion without a duration is measured
-- from the start. A step is started at most once and never after it is done.

alter table cutover_step add column started_at timestamptz;
alter table cutover_step add column started_by text not null default '';
alter table cutover_step add constraint cutover_step_started_by_needs_start
  check (started_at is not null or started_by = '');

-- Application role privileges. No table is created here: starting a step writes cutover_step and
-- the plan version on cutover_plan through the SELECT, INSERT, UPDATE grant of 0001_init.sql.
-- Restated so this migration alone states what the store needs. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, UPDATE ON cutover_plan, cutover_step TO passage_app';
  END IF;
END
$$;
