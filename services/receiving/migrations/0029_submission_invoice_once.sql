-- An invoice closes at most one invoice submission (#511 review). Resolving a
-- submission names the vendor invoice accounts payable posted from it; two
-- submissions naming the same invoice would both read as invoiced while only
-- one invoice was posted. The store refuses it with SUP-INV-006 first; this
-- index makes a race between two resolves fail as well.
CREATE UNIQUE INDEX IF NOT EXISTS receiving_invoice_submission_invoice_once
  ON receiving_invoice_submission (tenant_id, invoice_id) WHERE invoice_id IS NOT NULL;
