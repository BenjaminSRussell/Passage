-- FR-22.7: Increase messages on an existing receipt for a shortage inside the
-- freeze fence.
ALTER TABLE action_message DROP CONSTRAINT IF EXISTS action_message_action_check;
ALTER TABLE action_message ADD CONSTRAINT action_message_action_check
  CHECK (action IN ('delay', 'postpone', 'advance', 'shortage', 'cancel', 'decrease', 'increase'));

-- FR-22.9 consensus workflow: a model is a draft (cells writable), submitted
-- (locked, waiting for approval) or published (locked; forecast.published.v1
-- was written). submitted_by and the cell writers (forecast_line.updated_by)
-- let the approval refuse anyone who maintained the version (4.22.17).
ALTER TABLE forecast_model ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'draft'
  CHECK (status IN ('draft', 'submitted', 'published'));
ALTER TABLE forecast_model ADD COLUMN IF NOT EXISTS submitted_by text;
ALTER TABLE forecast_model ADD COLUMN IF NOT EXISTS submitted_at timestamptz;
ALTER TABLE forecast_model ADD COLUMN IF NOT EXISTS decided_by text;
ALTER TABLE forecast_model ADD COLUMN IF NOT EXISTS published_at timestamptz;
ALTER TABLE forecast_model ADD CONSTRAINT forecast_model_submitted_check
  CHECK (status = 'draft' OR (submitted_by IS NOT NULL AND submitted_at IS NOT NULL));
ALTER TABLE forecast_model ADD CONSTRAINT forecast_model_published_check
  CHECK (status <> 'published' OR (decided_by IS NOT NULL AND published_at IS NOT NULL));
ALTER TABLE forecast_line ADD COLUMN IF NOT EXISTS updated_by text;
