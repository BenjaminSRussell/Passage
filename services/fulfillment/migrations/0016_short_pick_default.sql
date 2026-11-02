-- Fulfillment: the short_pick_action default becomes the PRD's
-- REMAINDER_BACKORDER (section 5.28.13) instead of KEEP_OPEN. A saved setting
-- keeps its value; only rows inserted without an action take the new default.
ALTER TABLE warehouse_setting ALTER COLUMN short_pick_action SET DEFAULT 'REMAINDER_BACKORDER';
