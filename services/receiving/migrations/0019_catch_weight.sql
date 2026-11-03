-- Wave 24 AC4: catch-weight dual quantity on receipt lines (FR-24.7, BR-24.5).
-- qty is the actual weight; cw_qty is the catch-weight count (cases); the spec
-- (nominal/min/max per unit) is stored so a later quantity correction can
-- re-check the band. Nullable so non-catch-weight lines are unchanged.
ALTER TABLE receiving_receipt_line
  ADD COLUMN IF NOT EXISTS cw_qty int CHECK (cw_qty IS NULL OR cw_qty >= 1),
  ADD COLUMN IF NOT EXISTS cw_nominal numeric(28,6),
  ADD COLUMN IF NOT EXISTS cw_min numeric(28,6),
  ADD COLUMN IF NOT EXISTS cw_max numeric(28,6);
