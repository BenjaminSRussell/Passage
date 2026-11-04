-- Wave 24 AC4: catch-weight dual quantity on receipt lines (FR-24.7, BR-24.5).
-- qty is the actual weight; cw_qty is the catch-weight count (cases); the spec
-- (nominal/min/max per unit) is stored so a later quantity correction can
-- re-check the band. Nullable so non-catch-weight lines are unchanged.
-- receiving_receipt_line is a live table, so this file only changes the
-- catalog: nullable columns and a NOT VALID check. 0020 validates the check.
SET LOCAL lock_timeout = '5s';

ALTER TABLE receiving_receipt_line ADD COLUMN IF NOT EXISTS cw_qty int;
ALTER TABLE receiving_receipt_line ADD COLUMN IF NOT EXISTS cw_nominal numeric(20,6);
ALTER TABLE receiving_receipt_line ADD COLUMN IF NOT EXISTS cw_min numeric(20,6);
ALTER TABLE receiving_receipt_line ADD COLUMN IF NOT EXISTS cw_max numeric(20,6);
ALTER TABLE receiving_receipt_line ADD CONSTRAINT receiving_receipt_line_catch_weight_check
  CHECK (
    (cw_qty IS NULL OR cw_qty >= 1)
    AND (cw_qty IS NULL) = (cw_nominal IS NULL)
    AND (cw_nominal IS NULL) = (cw_min IS NULL)
    AND (cw_min IS NULL) = (cw_max IS NULL)
    AND (cw_nominal IS NULL OR (cw_min > 0 AND cw_min <= cw_nominal AND cw_nominal <= cw_max))
  ) NOT VALID;
