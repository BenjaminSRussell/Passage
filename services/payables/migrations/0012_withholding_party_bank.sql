ALTER TABLE ap_payment_proposal_group ADD COLUMN IF NOT EXISTS withheld_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (withheld_amount >= 0);
ALTER TABLE ap_payment_proposal_line ADD COLUMN IF NOT EXISTS withheld_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (withheld_amount >= 0);
ALTER TABLE ap_payment_proposal_line ADD COLUMN IF NOT EXISTS withholding_code text CHECK (withholding_code IS NULL OR withholding_code <> '');
ALTER TABLE ap_payment_proposal_line DROP CONSTRAINT IF EXISTS ap_payment_proposal_line_withheld_within_pay;
ALTER TABLE ap_payment_proposal_line ADD CONSTRAINT ap_payment_proposal_line_withheld_within_pay CHECK (withheld_amount <= pay_amount);
ALTER TABLE ap_payment_run ADD COLUMN IF NOT EXISTS withholding_account text;

DROP TABLE IF EXISTS ap_payment_proposal_payee;
DROP FUNCTION IF EXISTS ap_payment_proposal_payee_guard();

ALTER TABLE ap_payment_proposal_group ADD COLUMN IF NOT EXISTS bank_account_id text CHECK (bank_account_id IS NULL OR bank_account_id <> '');
ALTER TABLE ap_payment_proposal_group ADD COLUMN IF NOT EXISTS bank_fingerprint text;
ALTER TABLE ap_payment_proposal_group ADD COLUMN IF NOT EXISTS bank_last4 text;
ALTER TABLE ap_payment_proposal_group ADD COLUMN IF NOT EXISTS bank_key_id text;
ALTER TABLE ap_payment_proposal_group DROP CONSTRAINT IF EXISTS ap_payment_proposal_group_bank_bound_together;
ALTER TABLE ap_payment_proposal_group ADD CONSTRAINT ap_payment_proposal_group_bank_bound_together CHECK ((bank_account_id IS NULL) = (bank_fingerprint IS NULL) AND (bank_account_id IS NULL) = (bank_key_id IS NULL));
CREATE OR REPLACE FUNCTION ap_payment_proposal_group_bank_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.bank_account_id IS NOT NULL
     OR NOT EXISTS (SELECT 1 FROM ap_payment_proposal p WHERE p.tenant_id = NEW.tenant_id AND p.id = NEW.proposal_id AND p.status = 'DRAFT') THEN
    RAISE EXCEPTION 'the bank account binding of group % is set once, at approval' , OLD.id USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS ap_payment_proposal_group_bank_guard ON ap_payment_proposal_group;
CREATE TRIGGER ap_payment_proposal_group_bank_guard BEFORE UPDATE ON ap_payment_proposal_group FOR EACH ROW EXECUTE FUNCTION ap_payment_proposal_group_bank_guard();
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (bank_account_id, bank_fingerprint, bank_key_id, bank_last4) ON ap_payment_proposal_group TO passage_app';
  END IF;
END
$$;

ALTER TABLE ap_payment_proposal_group ADD COLUMN IF NOT EXISTS paid_base numeric(20,6) CHECK (paid_base IS NULL OR paid_base >= 0);
ALTER TABLE ap_payment_proposal_group DROP CONSTRAINT IF EXISTS ap_payment_proposal_group_withheld_has_base;
ALTER TABLE ap_payment_proposal_group ADD CONSTRAINT ap_payment_proposal_group_withheld_has_base CHECK (withheld_amount = 0 OR paid_base IS NOT NULL);
