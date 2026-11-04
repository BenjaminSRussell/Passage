-- Wave 21 slice 28: co-products and by-products edited on the order (FR-21.14, FR-21.18).
-- Additive (expand): the audit trail of the order's edits may name a co-product line, and the
-- store deletes a co-product line that no receipt or journal references.
ALTER TABLE production_order_edit DROP CONSTRAINT IF EXISTS production_order_edit_target_check;
ALTER TABLE production_order_edit ADD CONSTRAINT production_order_edit_target_check
  CHECK (target IN ('line', 'operation', 'coproduct'));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT DELETE ON production_coproduct TO passage_app';
  END IF;
END
$$;
