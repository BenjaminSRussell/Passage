ALTER TABLE postal_address DROP CONSTRAINT IF EXISTS postal_address_check;
ALTER TABLE postal_address DROP CONSTRAINT IF EXISTS postal_address_valid_range;
ALTER TABLE postal_address ADD CONSTRAINT postal_address_valid_range CHECK (valid_to IS NULL OR valid_to >= valid_from);
CREATE INDEX IF NOT EXISTS postal_address_versions ON postal_address (tenant_id, address_id, valid_from);
