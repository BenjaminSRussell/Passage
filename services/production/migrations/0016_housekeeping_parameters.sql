-- Wave 21 slice 17: order housekeeping and proposal defaults per site (PRD 4.21.13, 4.21.16).
-- Additive (expand): new columns with defaults on production_parameters, so rows written
-- before this migration keep the behaviour they had. No new table, so no new grant: the
-- table keeps the SELECT, INSERT and UPDATE grant of 0013.

-- Picking-list proposal default and route-card proposal basis (the journal proposal when a
-- create request names none); the stale-order age in days for cancel-stale-created-orders
-- (0 never cancels); the opt-in for auto-end-fully-reported.
ALTER TABLE production_parameters
  ADD COLUMN IF NOT EXISTS picking_list_proposal text NOT NULL DEFAULT 'remaining'
    CHECK (picking_list_proposal IN ('remaining', 'proportional', 'none')),
  ADD COLUMN IF NOT EXISTS route_card_proposal text NOT NULL DEFAULT 'remaining'
    CHECK (route_card_proposal IN ('remaining', 'proportional')),
  ADD COLUMN IF NOT EXISTS stale_order_days integer NOT NULL DEFAULT 90
    CHECK (stale_order_days BETWEEN 0 AND 3650),
  ADD COLUMN IF NOT EXISTS auto_end_enabled boolean NOT NULL DEFAULT false;

-- A housekeeping run under Idempotency-Key K logs each order it moves as K:<orderId>; a retry
-- finds them by that prefix (a byte-wise range on this index) and replays them instead of
-- scanning again.
CREATE INDEX IF NOT EXISTS production_status_log_key_prefix
  ON production_status_log (tenant_id, idempotency_key text_pattern_ops);
