-- PLAN-0015 (FR-22.19, BR-22.13): an item planned under assemble-to-order-final
-- whose sales line found less forecast to consume than its quantity gets an
-- information message for the excess, so the set of message actions grows.
-- No table is added: action_message is already under RLS and granted to
-- passage_app.
ALTER TABLE action_message DROP CONSTRAINT IF EXISTS action_message_action_check;
ALTER TABLE action_message ADD CONSTRAINT action_message_action_check
  CHECK (action IN ('delay', 'postpone', 'advance', 'shortage', 'cancel', 'decrease', 'increase', 'red-zone', 'sales-on-top'));
