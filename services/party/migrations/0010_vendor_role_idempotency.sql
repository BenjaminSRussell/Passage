-- Idempotency-Key on vendor role create (PSG-704): the migration loader retries
-- vendor creates, and a retry must return the original role. The key is stored
-- prefixed with the legal entity and party; vendor_role is already granted
-- SELECT, INSERT, UPDATE to passage_app (0008), so no grant changes.
ALTER TABLE vendor_role ADD COLUMN IF NOT EXISTS idempotency_key text;
CREATE UNIQUE INDEX IF NOT EXISTS vendor_role_idempotency_key ON vendor_role (tenant_id, idempotency_key);
