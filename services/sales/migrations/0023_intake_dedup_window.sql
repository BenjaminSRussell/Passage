-- Wave 27 slice 19: intake_dedup_window_days (BR-27.10, PRD 5.27.13), 1 to 90
-- days, default 30. A repeat of (channel, external_ref) for one legal entity
-- within the window returns the stored order (or is refused when the payload
-- differs); after the window the same reference may open a new order. The
-- permanent UNIQUE (tenant_id, channel, external_ref) of 0001 made dedup last
-- forever, so it is replaced by a plain index; the store serialises concurrent
-- intakes of one reference with a transaction advisory lock. Like every
-- migration here the index is built in the migration's own transaction (no
-- CONCURRENTLY).
-- No new table, so no new grant: sales_parameter and sales_order are already
-- granted to passage_app.
ALTER TABLE sales_parameter
  ADD COLUMN IF NOT EXISTS intake_dedup_window_days int NOT NULL DEFAULT 30
  CHECK (intake_dedup_window_days BETWEEN 1 AND 90);

ALTER TABLE sales_order DROP CONSTRAINT IF EXISTS sales_order_tenant_id_channel_external_ref_key;

CREATE INDEX IF NOT EXISTS sales_order_intake_ref
  ON sales_order (tenant_id, legal_entity_id, channel, external_ref, created_at DESC)
  WHERE external_ref IS NOT NULL;

-- The credit review-due list reads profiles by review date.
CREATE INDEX IF NOT EXISTS credit_profile_review_date
  ON credit_profile (tenant_id, review_date)
  WHERE review_date IS NOT NULL;
