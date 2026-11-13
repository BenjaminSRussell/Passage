-- FR-40.6, FR-40.18: a run reads the IC pair amounts of a subsidiary from the collected ledgers, and retranslates a foreign
-- associate's carrying amount at the closing rate (IAS 21.44, IAS 28).
-- A unit's terms name the account in its own ledger that holds its receivable from the holder, and the account in the holder's
-- ledger that holds the payable to it. The investments register keeps a foreign associate's carrying amount in its own currency,
-- the base of the next retranslation, and the exchange difference the run booked to the translation reserve.

ALTER TABLE consolidation_unit_terms ADD COLUMN IF NOT EXISTS ic_receivable_account text CHECK (ic_receivable_account IS NULL OR ic_receivable_account ~ '^[A-Za-z0-9._:-]{1,64}$');
ALTER TABLE consolidation_unit_terms ADD COLUMN IF NOT EXISTS parent_payable_account text CHECK (parent_payable_account IS NULL OR parent_payable_account ~ '^[A-Za-z0-9._:-]{1,64}$');

ALTER TABLE consolidation_investment ADD COLUMN IF NOT EXISTS unit_currency char(3);
ALTER TABLE consolidation_investment ADD COLUMN IF NOT EXISTS closing_carrying_unit numeric(28,6);
ALTER TABLE consolidation_investment ADD COLUMN IF NOT EXISTS fx_difference numeric(28,6) NOT NULL DEFAULT 0;
