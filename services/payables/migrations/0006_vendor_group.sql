ALTER TABLE ap_open_item ADD COLUMN IF NOT EXISTS vendor_group_id uuid;
CREATE INDEX IF NOT EXISTS ap_open_item_entity_vendor_group ON ap_open_item (tenant_id, legal_entity_id, vendor_group_id);
