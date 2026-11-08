-- Fulfillment slice 24: the remaining packing parameters of section 5.28.13 that
-- belong to a legal entity. gs1_company_prefix and sscc_extension_digit decide
-- which SSCCs the entity's containers carry (BR-28.5; null = the process
-- default GS1_COMPANY_PREFIX and extension, which is what every container used
-- before this migration). packaging_fee_billable (BR-28.12) tells billing that
-- the statistical packaging fee lines of a slip are to be charged; the slip
-- remembers the value it posted under so a cancel mirrors it. No new table:
-- legal_entity_setting and packing_slip keep their grants.
ALTER TABLE legal_entity_setting ADD COLUMN IF NOT EXISTS gs1_company_prefix text
  CHECK (gs1_company_prefix IS NULL OR gs1_company_prefix ~ '^[0-9]{6,12}$');
ALTER TABLE legal_entity_setting ADD COLUMN IF NOT EXISTS sscc_extension_digit smallint
  CHECK (sscc_extension_digit IS NULL OR sscc_extension_digit BETWEEN 0 AND 9);
ALTER TABLE legal_entity_setting ADD COLUMN IF NOT EXISTS packaging_fee_billable boolean NOT NULL DEFAULT false;
ALTER TABLE packing_slip ADD COLUMN IF NOT EXISTS packaging_fee_billable boolean NOT NULL DEFAULT false;
