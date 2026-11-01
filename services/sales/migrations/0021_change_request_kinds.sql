-- Wave 27 slice 17: change approval for cancellations, and withdrawal by the
-- requester (FR-27.9). A request is either a CHANGE (the PATCH body in
-- "change") or a CANCEL (the order is cancelled when approved; the reason is
-- in "reason" and "change" is empty). The requester can take a PENDING
-- request back, which ends it as WITHDRAWN. No new table: the existing
-- grants on order_change_request already cover the update.
ALTER TABLE order_change_request
  ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'CHANGE' CHECK (kind IN ('CHANGE', 'CANCEL'));
ALTER TABLE order_change_request DROP CONSTRAINT IF EXISTS order_change_request_status_check;
ALTER TABLE order_change_request
  ADD CONSTRAINT order_change_request_status_check CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED', 'WITHDRAWN'));
