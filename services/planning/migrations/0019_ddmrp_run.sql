-- DDMRP buffers in the plan run (FR-22.16, BR-22.11, AC-22.12). A planned
-- order that replenishes a buffer records the buffer's planning priority (net-
-- flow position / top of green, 4 places as the buffer computes it) and zone;
-- both are null on every other order. A run raises the PLAN-0010 red-zone
-- message for every planned order that replenishes a buffer in its red zone,
-- so the set of message actions grows. No table is added: planned_order and
-- action_message are already under RLS and granted to passage_app.
ALTER TABLE planned_order
  ADD COLUMN IF NOT EXISTS planning_priority numeric(12,4),
  ADD COLUMN IF NOT EXISTS buffer_zone text CHECK (buffer_zone IN ('red', 'yellow', 'green'));
ALTER TABLE action_message DROP CONSTRAINT IF EXISTS action_message_action_check;
ALTER TABLE action_message ADD CONSTRAINT action_message_action_check
  CHECK (action IN ('delay', 'postpone', 'advance', 'shortage', 'cancel', 'decrease', 'increase', 'red-zone'));
