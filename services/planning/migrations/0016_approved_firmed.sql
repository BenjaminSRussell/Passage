-- FR-22.11: planned orders a planner approved become firmed by the next plan
-- run. The run records how many it firmed on the version it wrote.
ALTER TABLE plan_version
  ADD COLUMN IF NOT EXISTS approved_firmed_count integer NOT NULL DEFAULT 0
  CHECK (approved_firmed_count >= 0);
