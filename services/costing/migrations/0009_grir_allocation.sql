-- Issue #470: an invoice PPV row clears GR/IR from the receipt it names, allocated by remainder,
-- so invoices that split a receipt clear exactly its GR/IR. Additive only.
--
-- grir_receipt_key is the source key of the receipt PPV row the invoice clears; grir_qty is the
-- quantity it invoiced (the row's qty stays 0, so the value report is unchanged). Rows recorded
-- before this migration carry neither and are not allocated against.
ALTER TABLE cost_transaction ADD COLUMN IF NOT EXISTS grir_receipt_key text;
ALTER TABLE cost_transaction ADD COLUMN IF NOT EXISTS grir_qty numeric(28,6);

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'cost_transaction_grir_allocation'
                 AND conrelid = 'cost_transaction'::regclass) THEN
    ALTER TABLE cost_transaction ADD CONSTRAINT cost_transaction_grir_allocation CHECK (
      (grir_receipt_key IS NULL AND grir_qty IS NULL)
      OR (kind = 'PPV' AND gr_ir_amount IS NOT NULL AND grir_qty > 0 AND char_length(grir_receipt_key) BETWEEN 1 AND 200));
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS cost_transaction_grir_receipt ON cost_transaction (tenant_id, grir_receipt_key)
  WHERE grir_receipt_key IS NOT NULL;
