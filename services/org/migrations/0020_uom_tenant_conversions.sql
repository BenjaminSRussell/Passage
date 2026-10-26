ALTER TABLE uom_conversion ADD COLUMN IF NOT EXISTS idempotency_key text;
CREATE UNIQUE INDEX IF NOT EXISTS uom_conversion_idempotency ON uom_conversion (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
