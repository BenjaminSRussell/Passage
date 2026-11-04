-- Sales slice 26: a credit manager can reject a CREDIT hold with a comment
-- (PRD §5.27.6 credit release queue: "release/reject with comment"). Rejection
-- ends the hold as REJECTED and cancels the unconfirmed order in the same
-- transaction. order_hold keeps its grants (0009: SELECT, INSERT, UPDATE).
-- order_hold holds a few rows per held order, so the checks are validated
-- here; the lock timeout keeps a busy table from stalling the rollout.
SET LOCAL lock_timeout = '5s';

ALTER TABLE order_hold DROP CONSTRAINT IF EXISTS order_hold_status_check;
ALTER TABLE order_hold ADD CONSTRAINT order_hold_status_check CHECK (status IN ('ACTIVE', 'RELEASED', 'CLEARED', 'REJECTED'));

ALTER TABLE order_hold ADD COLUMN IF NOT EXISTS rejected_by text;
ALTER TABLE order_hold ADD COLUMN IF NOT EXISTS rejected_at timestamptz;
ALTER TABLE order_hold ADD COLUMN IF NOT EXISTS rejection_comment text;

-- A REJECTED hold always says who, when and why; the comment is 1 to 500
-- characters, as the API takes it.
ALTER TABLE order_hold ADD CONSTRAINT order_hold_rejection_check CHECK (
  (rejection_comment IS NULL OR length(rejection_comment) BETWEEN 1 AND 500)
  AND (status <> 'REJECTED' OR (rejected_by IS NOT NULL AND rejected_at IS NOT NULL AND rejection_comment IS NOT NULL)));
