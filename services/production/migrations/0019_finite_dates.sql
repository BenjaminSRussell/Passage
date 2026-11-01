-- Wave 21 slice 20: finite-capacity dates from scheduling (PRD 4.21 BR-21.1, FR-23.10).
-- Additive (expand): defaulted columns; orders written before keep the infinite-capacity baseline.

-- How the order's dates were found: `infinite` by :schedule or a re-date (one operation after
-- the other on a continuous clock), `finite` once scheduling's windows were applied. The run and
-- the time say which finite plan the dates come from. finite_compressed_opers are the operations
-- whose finite window is shorter than their jobs' planned hours (flagged, not refused).
ALTER TABLE production_order
  ADD COLUMN IF NOT EXISTS schedule_basis text NOT NULL DEFAULT 'infinite' CHECK (schedule_basis IN ('infinite', 'finite')),
  ADD COLUMN IF NOT EXISTS finite_run_id text,
  ADD COLUMN IF NOT EXISTS finite_scheduled_at timestamptz,
  ADD COLUMN IF NOT EXISTS finite_compressed_opers integer[] NOT NULL DEFAULT '{}';
-- production_order is already granted SELECT, INSERT and UPDATE to passage_app; production_job
-- UPDATE came with 0018 and the store deletes and inserts jobs as :schedule does.
