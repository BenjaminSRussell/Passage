-- Customer portal (#30): a bill-to contact may act for its ship-to accounts, but a ship-to contact may not act for its bill-to
-- account unless this flag is set on the ship-to customer. Off by default; additive only.
ALTER TABLE customer_role ADD COLUMN IF NOT EXISTS portal_bill_to_access boolean NOT NULL DEFAULT false;
