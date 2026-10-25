ALTER TABLE legal_entity ADD COLUMN IF NOT EXISTS primary_address_id uuid;
ALTER TABLE site ADD COLUMN IF NOT EXISTS address_id uuid;
ALTER TABLE warehouse ADD COLUMN IF NOT EXISTS address_id uuid;
ALTER TABLE location ADD COLUMN IF NOT EXISTS address_id uuid;
