ALTER TABLE ap_vendor_block DROP CONSTRAINT ap_vendor_block_pkey;
ALTER TABLE ap_vendor_block ADD COLUMN legal_entity_id uuid;
ALTER TABLE ap_vendor_block ADD COLUMN blocked_by text;
INSERT INTO ap_vendor_block (tenant_id, vendor_id, legal_entity_id, reason, created_at, blocked_by)
  SELECT DISTINCT b.tenant_id, b.vendor_id, i.legal_entity_id, b.reason, b.created_at, 'migration:0010'
  FROM ap_vendor_block b
  JOIN ap_open_item i ON i.tenant_id = b.tenant_id AND i.vendor_id = b.vendor_id
  WHERE b.legal_entity_id IS NULL;
DELETE FROM ap_vendor_block WHERE legal_entity_id IS NULL;
ALTER TABLE ap_vendor_block ALTER COLUMN legal_entity_id SET NOT NULL;
ALTER TABLE ap_vendor_block ADD PRIMARY KEY (tenant_id, legal_entity_id, vendor_id);
CREATE POLICY ap_vendor_block_legal_entity_scope ON ap_vendor_block AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE ap_open_item ADD COLUMN IF NOT EXISTS payment_hold_reason text;
ALTER TABLE ap_open_item ADD COLUMN IF NOT EXISTS payment_held_by text;
