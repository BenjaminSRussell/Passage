-- Wave 24 AC4: catch-weight spec and actual weight on sales order lines
-- (FR-24.7). quantity stays the catch-weight count (cases); LineNet is priced
-- per weight (nominal × cases until an actual weight is recorded). sales_order_line
-- is a live table, so this file only changes the catalog: nullable columns and
-- a NOT VALID check. 0031 validates the check.
SET LOCAL lock_timeout = '5s';

ALTER TABLE sales_order_line ADD COLUMN IF NOT EXISTS cw_nominal numeric(20,6);
ALTER TABLE sales_order_line ADD COLUMN IF NOT EXISTS cw_min numeric(20,6);
ALTER TABLE sales_order_line ADD COLUMN IF NOT EXISTS cw_max numeric(20,6);
ALTER TABLE sales_order_line ADD COLUMN IF NOT EXISTS actual_weight numeric(20,6);
ALTER TABLE sales_order_line ADD CONSTRAINT sales_order_line_catch_weight_check
  CHECK (
    ((cw_nominal IS NULL) = (cw_min IS NULL) AND (cw_min IS NULL) = (cw_max IS NULL))
    AND (actual_weight IS NULL OR actual_weight > 0)
  ) NOT VALID;
