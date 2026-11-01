-- Campaign completion and cancellation (FR-23.14). The scheduling_campaign
-- table, its RLS policy and its passage_app grant (SELECT, INSERT, UPDATE,
-- DELETE) come from 0002; no new table here.
ALTER TABLE scheduling_campaign ADD COLUMN IF NOT EXISTS completed_at timestamptz;
ALTER TABLE scheduling_campaign ADD COLUMN IF NOT EXISTS cancelled_at timestamptz;
ALTER TABLE scheduling_campaign ADD COLUMN IF NOT EXISTS cancel_reason text CHECK (char_length(cancel_reason) BETWEEN 1 AND 500);
-- A row loaded as completed or cancelled before these columns existed gets a
-- time, so the consistency check below holds.
UPDATE scheduling_campaign SET completed_at = coalesce(started_at, created_at) WHERE status = 'completed' AND completed_at IS NULL;
UPDATE scheduling_campaign SET cancelled_at = created_at WHERE status = 'cancelled' AND cancelled_at IS NULL;
ALTER TABLE scheduling_campaign DROP CONSTRAINT IF EXISTS scheduling_campaign_close_consistent;
ALTER TABLE scheduling_campaign ADD CONSTRAINT scheduling_campaign_close_consistent CHECK (
  (status = 'completed') = (completed_at IS NOT NULL)
  AND (status = 'cancelled') = (cancelled_at IS NOT NULL)
  AND (cancel_reason IS NULL OR status = 'cancelled'));
