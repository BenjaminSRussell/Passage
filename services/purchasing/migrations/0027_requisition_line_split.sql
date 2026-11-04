-- Wave 17 purchasing slice 22: splitting a requisition line (FR-17.3). A split cuts new OPEN lines out of an
-- OPEN line of an APPROVED requisition; each new line remembers the line it was cut from. No new table and no
-- grant: purchase_requisition_line is already granted to passage_app (SELECT, INSERT, UPDATE) in 0009.
ALTER TABLE purchase_requisition_line ADD COLUMN IF NOT EXISTS split_from_line_id uuid;
ALTER TABLE purchase_requisition_line ADD CONSTRAINT purchase_requisition_line_split_from
  FOREIGN KEY (tenant_id, split_from_line_id) REFERENCES purchase_requisition_line (tenant_id, id) NOT VALID;
CREATE INDEX IF NOT EXISTS purchase_requisition_line_split_from ON purchase_requisition_line (tenant_id, split_from_line_id)
  WHERE split_from_line_id IS NOT NULL;
