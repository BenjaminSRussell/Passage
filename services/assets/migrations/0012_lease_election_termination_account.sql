-- Wave 37, D4c: short-term and low-value lease elections (IFRS 16.5-8) and a dedicated termination
-- gain / loss account in the lease posting profile.
--
-- A lease under an election is not recognized: liability and rou_asset are 0, its schedule lines carry only
-- the payment (expensed per period against the payment clearing account). A low-value election states the
-- value of the underlying asset when new and the currency that value is stated in (USD or EUR, the currencies
-- the threshold is set in; the lease itself may be in any currency). purchase_option records whether the lease
-- contains a purchase option, which rules out the short-term election (IFRS 16 Appendix A). No new table, so no
-- new grant.
ALTER TABLE lease ADD COLUMN IF NOT EXISTS election text NOT NULL DEFAULT 'None'
  CHECK (election IN ('None', 'ShortTerm', 'LowValue'));
ALTER TABLE lease ADD COLUMN IF NOT EXISTS purchase_option boolean NOT NULL DEFAULT false;
ALTER TABLE lease ADD COLUMN IF NOT EXISTS underlying_asset_value numeric(20,6)
  CHECK (underlying_asset_value IS NULL OR underlying_asset_value > 0);
ALTER TABLE lease ADD COLUMN IF NOT EXISTS underlying_asset_currency char(3);
ALTER TABLE lease DROP CONSTRAINT IF EXISTS lease_election_shape;
ALTER TABLE lease ADD CONSTRAINT lease_election_shape CHECK (
  (election = 'None' OR (liability = 0 AND rou_asset = 0))
  AND ((election = 'LowValue') = (underlying_asset_value IS NOT NULL))
  AND ((underlying_asset_value IS NULL) = (underlying_asset_currency IS NULL))
  AND (election <> 'ShortTerm' OR (term_months <= 12 AND NOT purchase_option)));

-- The profile gains the termination gain / loss account (the termination journal used the remeasurement
-- gain account until now, so existing rows keep that account) and the account that takes the payments of a
-- lease under an election (existing rows start with their depreciation expense account, which the accountant
-- replaces with a lease expense account).
ALTER TABLE lease_posting_profile ADD COLUMN IF NOT EXISTS termination_gain_loss_account text;
ALTER TABLE lease_posting_profile ADD COLUMN IF NOT EXISTS exempt_lease_expense_account text;
UPDATE lease_posting_profile SET termination_gain_loss_account = remeasurement_gain_account WHERE termination_gain_loss_account IS NULL;
UPDATE lease_posting_profile SET exempt_lease_expense_account = depreciation_expense_account WHERE exempt_lease_expense_account IS NULL;
ALTER TABLE lease_posting_profile ALTER COLUMN termination_gain_loss_account SET NOT NULL;
ALTER TABLE lease_posting_profile ALTER COLUMN exempt_lease_expense_account SET NOT NULL;
