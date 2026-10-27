-- Billing slice 3: credit notes (FR-29.5) and installment open items (FR-29.18).
--
-- A credit note is an invoice row of type CREDIT_NOTE that references the
-- posted invoice it credits; its lines reference the original lines. Amounts
-- are stored as positive magnitudes (the type carries the sign), so the totals
-- and line checks of 0001 hold for credit notes as well.

ALTER TABLE invoice ADD COLUMN IF NOT EXISTS original_invoice_id uuid;
ALTER TABLE invoice ADD COLUMN IF NOT EXISTS credit_reason text;
ALTER TABLE invoice ADD CONSTRAINT invoice_original_fk
  FOREIGN KEY (tenant_id, original_invoice_id) REFERENCES invoice (tenant_id, id);
ALTER TABLE invoice ADD CONSTRAINT invoice_credit_note_has_original
  CHECK ((invoice_type = 'CREDIT_NOTE') = (original_invoice_id IS NOT NULL));
ALTER TABLE invoice ADD CONSTRAINT invoice_credit_note_has_reason
  CHECK ((invoice_type = 'CREDIT_NOTE') = (credit_reason IS NOT NULL AND length(credit_reason) BETWEEN 1 AND 200));
-- Remaining creditable amount: the posted credit notes of one original.
CREATE INDEX IF NOT EXISTS invoice_original_idx ON invoice (tenant_id, original_invoice_id, status)
  WHERE original_invoice_id IS NOT NULL;

ALTER TABLE invoice_line ADD COLUMN IF NOT EXISTS original_line_no integer CHECK (original_line_no > 0);

-- Installment split at post (BR-29.11): one row per open item the receivables
-- context opens from invoice.posted. Σ amount = invoice gross exactly.
CREATE TABLE IF NOT EXISTS invoice_installment (
  tenant_id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  installment_no integer NOT NULL CHECK (installment_no > 0),
  percent numeric(9,6) NOT NULL CHECK (percent > 0 AND percent <= 100),
  offset_days integer NOT NULL CHECK (offset_days BETWEEN 0 AND 365),
  amount numeric(20,6) NOT NULL CHECK (amount >= 0),
  due_date date NOT NULL,
  PRIMARY KEY (tenant_id, invoice_id, installment_no),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoice (tenant_id, id)
);
ALTER TABLE invoice_installment ENABLE ROW LEVEL SECURITY;
ALTER TABLE invoice_installment FORCE ROW LEVEL SECURITY;
CREATE POLICY invoice_installment_tenant_access ON invoice_installment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
