-- #30 round 3: reversal of applied payments, cross-currency settlement of credit items and a per-entity FX tolerance.
--
-- A payment's application is reversed by a reversal settlement row per original (reversal_of_id, migration 0001) that carries
-- the original's amounts negated, so every as-of sum over settlement (statements, balance confirmations) shows the item settled
-- up to the reversal date and open again after it. Original rows keep their non-negative amounts.
ALTER TABLE settlement DROP CONSTRAINT IF EXISTS settlement_settled_amount_check;
ALTER TABLE settlement DROP CONSTRAINT IF EXISTS settlement_cash_discount_taken_check;
ALTER TABLE settlement DROP CONSTRAINT IF EXISTS settlement_write_off_amount_check;
ALTER TABLE settlement DROP CONSTRAINT IF EXISTS settlement_item_amount_check;
ALTER TABLE settlement DROP CONSTRAINT IF EXISTS settlement_amount_sign;
ALTER TABLE settlement ADD CONSTRAINT settlement_amount_sign CHECK (
  (reversal_of_id IS NULL AND settled_amount >= 0 AND cash_discount_taken >= 0 AND write_off_amount >= 0 AND (item_amount IS NULL OR item_amount > 0))
  OR (reversal_of_id IS NOT NULL AND settled_amount <= 0 AND cash_discount_taken <= 0 AND write_off_amount <= 0 AND (item_amount IS NULL OR item_amount < 0)));
CREATE UNIQUE INDEX IF NOT EXISTS settlement_reversal_once ON settlement (tenant_id, reversal_of_id) WHERE reversal_of_id IS NOT NULL;

-- A credit item settled against a debit in another currency: amount stays what comes off the credit (its currency); debit_amount is what
-- comes off the debit in its currency, with the two rates into the functional currency and the realized difference (positive a gain).
ALTER TABLE credit_settlement ADD COLUMN IF NOT EXISTS debit_currency char(3) CHECK (debit_currency IS NULL OR debit_currency ~ '^[A-Z]{3}$');
ALTER TABLE credit_settlement ADD COLUMN IF NOT EXISTS debit_amount numeric(20,6) CHECK (debit_amount IS NULL OR debit_amount > 0);
ALTER TABLE credit_settlement ADD COLUMN IF NOT EXISTS credit_rate numeric(20,10) CHECK (credit_rate IS NULL OR credit_rate > 0);
ALTER TABLE credit_settlement ADD COLUMN IF NOT EXISTS debit_rate numeric(20,10) CHECK (debit_rate IS NULL OR debit_rate > 0);
ALTER TABLE credit_settlement ADD COLUMN IF NOT EXISTS fx_gain_loss numeric(20,6) NOT NULL DEFAULT 0;

-- The FX tolerance of an entity: a cross-currency item amount may differ from what the cash or credit is worth by the larger of
-- tolerance_amount (in the item's currency) and tolerance_percent of the item amount. NULL keeps BR-30.4's 5.00 and 0.5%.
ALTER TABLE ar_fx_setting ADD COLUMN IF NOT EXISTS tolerance_amount numeric(20,6) CHECK (tolerance_amount IS NULL OR tolerance_amount >= 0);
ALTER TABLE ar_fx_setting ADD COLUMN IF NOT EXISTS tolerance_percent numeric(9,4) CHECK (tolerance_percent IS NULL OR (tolerance_percent >= 0 AND tolerance_percent <= 100));
