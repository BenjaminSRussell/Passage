-- Wave 21 slice 32: a production order keeps the released product whose site default supplied its order
-- quantity rules (FR-21.1), so a later quantity change, split or merge is checked against the same rules
-- without the caller sending the id again. Additive (expand). No new table, so no new grant:
-- production_order is granted in 0001. Orders made before this file, or without a releasedProductId,
-- have none and keep checking only the rules a request sends.
--
-- Catalog-only change: a nullable column without a default takes the ACCESS EXCLUSIVE lock for an
-- instant and never scans or rewrites. The lock_timeout makes the migration fail (and the rollout
-- retry) instead of queueing behind a long transaction.
SET LOCAL lock_timeout = '5s';

ALTER TABLE production_order ADD COLUMN IF NOT EXISTS released_product_id uuid;
