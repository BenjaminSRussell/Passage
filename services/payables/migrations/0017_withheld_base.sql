-- #13: the amount a payment line's tax was withheld on, kept with the withheld tax so the payment run can record the withholding with the
-- tax service (POST /v1/tax/withholding/entries needs the taxable base for the remittance and the certificate). Null on lines proposed before
-- this column existed, and on lines nothing was withheld from; the pay amount stands in for it.
ALTER TABLE ap_payment_proposal_line ADD COLUMN IF NOT EXISTS withheld_base numeric(20,6) CHECK (withheld_base IS NULL OR withheld_base >= 0);

-- A payment run whose withholding the tax service refused for good (a 4xx that retrying cannot fix) is parked here with the reason, so an
-- operator sees it on the run instead of an endless retry. Cleared when the recording later succeeds.
ALTER TABLE ap_payment_run ADD COLUMN IF NOT EXISTS withholding_record_error text;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (withholding_record_error) ON ap_payment_run TO passage_app';
  END IF;
END
$$;
