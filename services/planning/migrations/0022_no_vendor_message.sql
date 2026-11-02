-- PLAN-0008 (FR-22.3, FR-22.5): a planned purchase order that has neither a quota
-- source nor an item-coverage default vendor raises a warning, so the set of
-- message actions grows. No table is added: action_message is already under RLS
-- and granted to passage_app.
ALTER TABLE action_message DROP CONSTRAINT IF EXISTS action_message_action_check;
ALTER TABLE action_message ADD CONSTRAINT action_message_action_check
  CHECK (action IN ('delay', 'postpone', 'advance', 'shortage', 'cancel', 'decrease', 'increase', 'red-zone', 'sales-on-top', 'no-vendor'));
