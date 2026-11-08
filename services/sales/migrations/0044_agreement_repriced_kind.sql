-- Slice 38: a history entry for the run that puts a revised agreement price on
-- the orders already released (BR-27.9). No new table, so no new grant.
ALTER TABLE sales_agreement_version DROP CONSTRAINT IF EXISTS sales_agreement_version_kind_check;
ALTER TABLE sales_agreement_version ADD CONSTRAINT sales_agreement_version_kind_check
  CHECK (kind IN ('REVISED', 'CLOSED', 'RETURNED', 'DRAWN', 'REPRICED'));
