-- FR-22.7: a planner decides each action message: apply it (the owning context
-- changes the source order) or dismiss it. The decision is stored on the message
-- under optimistic versioning. A run that raises the same message again (same item,
-- scope, action, supply, demand, suggested date, quantity and delay) carries the
-- decision onto the new version's message; carried_from names the message the
-- planner decided. No table is added: action_message is already under RLS (tenant
-- and legal-entity policies) and granted SELECT, INSERT, UPDATE and DELETE to
-- passage_app.
ALTER TABLE action_message ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'open';
ALTER TABLE action_message ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1;
ALTER TABLE action_message ADD COLUMN IF NOT EXISTS decided_by text;
ALTER TABLE action_message ADD COLUMN IF NOT EXISTS decided_at timestamptz;
ALTER TABLE action_message ADD COLUMN IF NOT EXISTS decision_note text NOT NULL DEFAULT '';
ALTER TABLE action_message ADD COLUMN IF NOT EXISTS decision_key text;
ALTER TABLE action_message ADD COLUMN IF NOT EXISTS carried_from uuid;
ALTER TABLE action_message DROP CONSTRAINT IF EXISTS action_message_status_check;
ALTER TABLE action_message ADD CONSTRAINT action_message_status_check
  CHECK (status IN ('open', 'applied', 'dismissed'));
ALTER TABLE action_message DROP CONSTRAINT IF EXISTS action_message_decision_check;
ALTER TABLE action_message ADD CONSTRAINT action_message_decision_check
  CHECK ((status = 'open') = (decided_at IS NULL));
CREATE INDEX IF NOT EXISTS action_message_status ON action_message (tenant_id, plan_version_id, status);
