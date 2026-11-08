-- Wave 27 slice 36: sales agreements committed by amount (FR-27.10, BR-27.9).
-- commitment_type: QUANTITY (the commitment, released, returned and drawn
-- figures are quantities of the agreement unit, as before) or AMOUNT (the same
-- columns hold currency amounts: a release draws the net value of its line).
-- agreement_release.units: what the release drew from the commitment, in the
-- commitment unit (the quantity for QUANTITY, the line net for AMOUNT);
-- unit_price: the price the release line was created at, used to value a
-- remainder cut on an AMOUNT agreement. Existing releases are QUANTITY ones.

ALTER TABLE sales_agreement ADD COLUMN IF NOT EXISTS commitment_type text NOT NULL DEFAULT 'QUANTITY'
  CHECK (commitment_type IN ('QUANTITY', 'AMOUNT'));

ALTER TABLE agreement_release ADD COLUMN IF NOT EXISTS units numeric(20,6);
ALTER TABLE agreement_release ADD COLUMN IF NOT EXISTS unit_price numeric(19,4);
UPDATE agreement_release r SET units = r.quantity, unit_price = a.unit_price
  FROM sales_agreement a
  WHERE a.tenant_id = r.tenant_id AND a.id = r.agreement_id AND r.units IS NULL;
ALTER TABLE agreement_release ALTER COLUMN units SET NOT NULL;
ALTER TABLE agreement_release ALTER COLUMN unit_price SET NOT NULL;
ALTER TABLE agreement_release DROP CONSTRAINT IF EXISTS agreement_release_units_positive;
ALTER TABLE agreement_release ADD CONSTRAINT agreement_release_units_positive CHECK (units > 0);
