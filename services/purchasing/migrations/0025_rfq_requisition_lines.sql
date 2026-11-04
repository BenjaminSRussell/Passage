-- Wave 17 purchasing slice 20: an RFQ can be started from approved requisition lines that need
-- sourcing (FR-17.3, FR-17.4). An RFQ line remembers the requisition line it was raised for; the
-- requisition line stays OPEN while the RFQ runs but cannot be consolidated into a PO, and an
-- award claims it through the PO line like any other order. Additive only (expand/contract); no new
-- table, so the grants of 0009 already cover the changed tables. The foreign key is NOT VALID like the
-- other links of 0002 (the new column is empty, so there is nothing to validate under the lock) and has no
-- ON DELETE action: requisition lines are never deleted, and a delete would be refused while an RFQ names one.

SET LOCAL lock_timeout = '5s';

ALTER TABLE purchase_rfq_line ADD COLUMN IF NOT EXISTS requisition_line_id uuid;
ALTER TABLE purchase_rfq_line ADD CONSTRAINT purchase_rfq_line_requisition_line
  FOREIGN KEY (tenant_id, requisition_line_id) REFERENCES purchase_requisition_line (tenant_id, id) NOT VALID;
CREATE INDEX IF NOT EXISTS purchase_rfq_line_requisition_line ON purchase_rfq_line (tenant_id, requisition_line_id)
  WHERE requisition_line_id IS NOT NULL;
