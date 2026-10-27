-- Wave 27 slice 5: MANDATORY supplementary lines added at order commit
-- (FR-27.14, BR-27.14, AC-27.10). A supplementary line references its parent
-- line in the same order (supplementary_of), names the rule that added it and
-- keeps the rule's base quantity and quantity per base, so a later change of
-- the parent's quantity recomputes it, or removes it below one base quantity,
-- even after the rule itself is removed.
ALTER TABLE sales_order_line ADD COLUMN IF NOT EXISTS supplementary_of uuid;
ALTER TABLE sales_order_line ADD COLUMN IF NOT EXISTS supplementary_rule_id uuid;
ALTER TABLE sales_order_line ADD COLUMN IF NOT EXISTS supplementary_base_qty numeric(20,6);
ALTER TABLE sales_order_line ADD COLUMN IF NOT EXISTS supplementary_qty_per_base numeric(20,6);

ALTER TABLE sales_order_line DROP CONSTRAINT IF EXISTS sales_order_line_supplementary_of_fk;
ALTER TABLE sales_order_line ADD CONSTRAINT sales_order_line_supplementary_of_fk
  FOREIGN KEY (tenant_id, supplementary_of) REFERENCES sales_order_line (tenant_id, id);

ALTER TABLE sales_order_line DROP CONSTRAINT IF EXISTS sales_order_line_supplementary_check;
ALTER TABLE sales_order_line ADD CONSTRAINT sales_order_line_supplementary_check CHECK (
  (supplementary_of IS NULL AND supplementary_rule_id IS NULL AND supplementary_base_qty IS NULL AND supplementary_qty_per_base IS NULL)
  OR (supplementary_of IS NOT NULL AND supplementary_rule_id IS NOT NULL
      AND supplementary_base_qty > 0 AND supplementary_qty_per_base > 0));

CREATE INDEX IF NOT EXISTS sales_order_line_supplementary_of ON sales_order_line (tenant_id, supplementary_of)
  WHERE supplementary_of IS NOT NULL;
