-- Where each run's model call was served (#605 EU Data Boundary): provider, host and hosting region, so a
-- residency review can show that no tenant's prompt left its region. NULL for a run that made no model call.
-- agent_run_guard (0001) lets a RUNNING row change, so finishRun writes these with the final status.
ALTER TABLE agent_run ADD COLUMN IF NOT EXISTS model_provider text;
ALTER TABLE agent_run ADD COLUMN IF NOT EXISTS model_host text;
ALTER TABLE agent_run ADD COLUMN IF NOT EXISTS model_region text;
