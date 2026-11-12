-- #35 (35-3): a requisition line taken from a supplier's punchout catalog says so (catalog_source PUNCHOUT) with the supplier's
-- part id, and carries the BR-35.13 approval flag: a price above the catalog price plus the supplier's tolerance. A requisition
-- with a flagged line is never approved automatically on submit.
ALTER TABLE purchase_requisition_line
  ADD COLUMN IF NOT EXISTS catalog_source text CHECK (catalog_source IS NULL OR catalog_source IN ('PUNCHOUT')),
  ADD COLUMN IF NOT EXISTS supplier_part_id text CHECK (supplier_part_id IS NULL OR char_length(supplier_part_id) BETWEEN 1 AND 100),
  ADD COLUMN IF NOT EXISTS needs_approval boolean NOT NULL DEFAULT false;
