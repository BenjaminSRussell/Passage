ALTER TABLE receiving_receipt_line
  ADD COLUMN IF NOT EXISTS po_line_no int CHECK (po_line_no IS NULL OR po_line_no > 0);
