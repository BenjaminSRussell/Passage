-- Fulfillment slice 25: returnable transport packaging parameters per legal
-- entity (section 5.28.13). rtp_tracking_enabled says whether a packing slip of
-- the entity raises the customer's returnable balances (BR-28.13); it defaults
-- to true because every slip raised them before this migration (the PRD's
-- default is false, see NOT-IMPLEMENTED). rtp_charge_days (30 to 365, default
-- 90) is the age after which an outstanding balance is chargeable (FR-28.16).
-- The slip remembers whether it tracked, so a cancel mirrors what the post did
-- even if the setting changed in between. No new table: legal_entity_setting and
-- packing_slip keep their grants.
ALTER TABLE legal_entity_setting ADD COLUMN IF NOT EXISTS rtp_tracking_enabled boolean NOT NULL DEFAULT true;
ALTER TABLE legal_entity_setting ADD COLUMN IF NOT EXISTS rtp_charge_days integer NOT NULL DEFAULT 90
  CHECK (rtp_charge_days BETWEEN 30 AND 365);
ALTER TABLE packing_slip ADD COLUMN IF NOT EXISTS rtp_tracked boolean NOT NULL DEFAULT true;
