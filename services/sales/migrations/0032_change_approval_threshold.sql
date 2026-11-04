-- Wave 27 slice 27: change approval by amount threshold and by a named approver
-- group (FR-27.9). customer_change_policy keeps its tenant and legal-entity
-- policies and its grants (0016: SELECT, INSERT, UPDATE); sales_order keeps
-- its grants (0009: SELECT, INSERT, UPDATE); no table is added.
-- approval_threshold: with approval_required on, a change to a confirmed order
-- applies without approval only while the value it moves, added to what
-- earlier unapproved changes of the order already moved, stays below this
-- amount, in threshold_currency (a cancellation moves the whole total); NULL
-- means every change needs approval, the slice 13 behaviour.
-- approver_user_ids: when not empty, only these users (identity user ids) may
-- approve or reject a request for the customer, or change its confirmed orders
-- directly; the approve duty is still required.
SET LOCAL lock_timeout = '5s';

ALTER TABLE customer_change_policy ADD COLUMN IF NOT EXISTS approval_threshold numeric(19,4);
ALTER TABLE customer_change_policy ADD COLUMN IF NOT EXISTS threshold_currency text;
ALTER TABLE customer_change_policy ADD COLUMN IF NOT EXISTS approver_user_ids uuid[] NOT NULL DEFAULT '{}';

-- A threshold is positive and always has its currency; the group has at most 20 users.
-- Existing rows have no threshold and an empty group, so the checks hold for
-- them and are added validated (the table has one row per customer that set it).
ALTER TABLE customer_change_policy DROP CONSTRAINT IF EXISTS customer_change_policy_threshold_check;
ALTER TABLE customer_change_policy ADD CONSTRAINT customer_change_policy_threshold_check CHECK (
  (approval_threshold IS NULL AND threshold_currency IS NULL)
  OR (approval_threshold > 0 AND threshold_currency ~ '^[A-Z]{3}$'));
ALTER TABLE customer_change_policy DROP CONSTRAINT IF EXISTS customer_change_policy_approvers_check;
ALTER TABLE customer_change_policy ADD CONSTRAINT customer_change_policy_approvers_check CHECK (cardinality(approver_user_ids) <= 20);

-- unapproved_change_value: the value the changes applied to a confirmed order
-- without approval have moved so far (the sum of each line's absolute net
-- movement). It stops a large change from being split into several small ones
-- that each stay under the threshold. A constant default is a catalog-only
-- change, so existing orders are not rewritten.
ALTER TABLE sales_order ADD COLUMN IF NOT EXISTS unapproved_change_value numeric(19,4) NOT NULL DEFAULT 0;
ALTER TABLE sales_order DROP CONSTRAINT IF EXISTS sales_order_unapproved_change_value_check;
ALTER TABLE sales_order ADD CONSTRAINT sales_order_unapproved_change_value_check CHECK (unapproved_change_value >= 0) NOT VALID;
