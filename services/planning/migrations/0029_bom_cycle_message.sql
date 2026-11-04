-- PLAN-0004 (BR-22.1 step 2): a cyclic BOM no longer refuses the run; the component
-- line that closes a cycle is skipped and raises a bom-cycle warning. PLAN-0009
-- (FR-22.17): a planned order no source of its quota arrangement can take is stored
-- as a no-quota-source message instead of only a log line. So the set of message
-- actions grows. No table is added: action_message is already under RLS and granted
-- to passage_app.
ALTER TABLE action_message DROP CONSTRAINT IF EXISTS action_message_action_check;
ALTER TABLE action_message ADD CONSTRAINT action_message_action_check
  CHECK (action IN ('delay', 'postpone', 'advance', 'shortage', 'cancel', 'decrease', 'increase', 'red-zone', 'sales-on-top', 'no-vendor', 'lot-split', 'bom-cycle', 'no-quota-source'));
