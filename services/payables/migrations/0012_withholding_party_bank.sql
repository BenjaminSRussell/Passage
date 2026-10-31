ALTER TABLE ap_payment_proposal_group ADD COLUMN IF NOT EXISTS withheld_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (withheld_amount >= 0);
ALTER TABLE ap_payment_proposal_line ADD COLUMN IF NOT EXISTS withheld_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (withheld_amount >= 0);
ALTER TABLE ap_payment_proposal_line ADD COLUMN IF NOT EXISTS withholding_code text CHECK (withholding_code IS NULL OR withholding_code <> '');
ALTER TABLE ap_payment_proposal_line DROP CONSTRAINT IF EXISTS ap_payment_proposal_line_withheld_within_pay;
ALTER TABLE ap_payment_proposal_line ADD CONSTRAINT ap_payment_proposal_line_withheld_within_pay CHECK (withheld_amount <= pay_amount);
ALTER TABLE ap_payment_run ADD COLUMN IF NOT EXISTS withholding_account text;

DROP TABLE IF EXISTS ap_payment_proposal_payee;
DROP FUNCTION IF EXISTS ap_payment_proposal_payee_guard();
