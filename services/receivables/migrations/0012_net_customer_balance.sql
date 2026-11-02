-- Credits net against debits (FR-30.1): since the opening-item import loads
-- credit-side open items, the balance snapshot and customerbalance.changed.v1
-- report the net open balance, and a customer can be net in credit. The slice-5
-- constraints (total_open >= 0, 0 <= overdue <= total_open) become: overdue is
-- never negative and never more than the balance owed. Credits clear the oldest
-- debits first, so a customer net in credit has nothing overdue.
ALTER TABLE customer_balance_snapshot DROP CONSTRAINT IF EXISTS customer_balance_snapshot_total_open_check;
ALTER TABLE customer_balance_snapshot DROP CONSTRAINT IF EXISTS customer_balance_snapshot_check;
ALTER TABLE customer_balance_snapshot ADD CONSTRAINT customer_balance_snapshot_overdue_range
  CHECK (overdue >= 0 AND overdue <= greatest(total_open, 0));
