-- Wave 17 purchasing slice 31: requisition editing, withdrawal and single-line cancellation (BR-17.1).
-- The audit trail (0030) gains three actions and the line a cancelled row concerns; a DRAFT requisition's lines
-- are replaced as a set when the requester edits it, so the store deletes them (the only DELETE on this table:
-- a DRAFT line is referenced by no RFQ, PO or split).
ALTER TABLE purchase_requisition_history DROP CONSTRAINT IF EXISTS purchase_requisition_history_action_check;
ALTER TABLE purchase_requisition_history ADD CONSTRAINT purchase_requisition_history_action_check
  CHECK (action IN ('SUBMITTED', 'APPROVED', 'REJECTED', 'REOPENED', 'CANCELLED', 'EDITED', 'WITHDRAWN', 'LINE_CANCELLED'));
ALTER TABLE purchase_requisition_history ADD COLUMN IF NOT EXISTS line_id uuid;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT DELETE ON purchase_requisition_line TO passage_app';
  END IF;
END
$$;
