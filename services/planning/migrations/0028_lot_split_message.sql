-- PLAN-0005 (FR-22.4): a requirement above the item's maximum order quantity that
-- is split into several planned orders raises a warning, so the set of message
-- actions grows. No table is added: action_message is already under RLS and
-- granted to passage_app.
ALTER TABLE action_message DROP CONSTRAINT IF EXISTS action_message_action_check;
ALTER TABLE action_message ADD CONSTRAINT action_message_action_check
  CHECK (action IN ('delay', 'postpone', 'advance', 'shortage', 'cancel', 'decrease', 'increase', 'red-zone', 'sales-on-top', 'no-vendor', 'lot-split'));
