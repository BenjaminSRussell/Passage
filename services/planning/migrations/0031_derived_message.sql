-- FR-22.7 Derived: an existing receipt of a component follows the cancel, decrease,
-- postpone or advance message of the parent supply it serves, so the set of message
-- actions grows, and a derived message names the parent's action in parent_action
-- (what to do with the component receipt); every other message keeps it empty. No
-- table is added: action_message is already under RLS and granted to passage_app.
-- Both constraints only widen what existing rows already satisfy (no row is derived
-- yet and the new column defaults to ''), and every statement can be run again.
ALTER TABLE action_message DROP CONSTRAINT IF EXISTS action_message_action_check;
ALTER TABLE action_message ADD CONSTRAINT action_message_action_check
  CHECK (action IN ('delay', 'postpone', 'advance', 'shortage', 'cancel', 'decrease', 'increase', 'derived', 'red-zone', 'sales-on-top', 'no-vendor', 'lot-split', 'bom-cycle', 'no-quota-source'));
ALTER TABLE action_message ADD COLUMN IF NOT EXISTS parent_action text NOT NULL DEFAULT '';
ALTER TABLE action_message DROP CONSTRAINT IF EXISTS action_message_parent_action_check;
ALTER TABLE action_message ADD CONSTRAINT action_message_parent_action_check
  CHECK (parent_action IN ('', 'cancel', 'decrease', 'postpone', 'advance') AND (action = 'derived') = (parent_action <> ''));
