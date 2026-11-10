ALTER TABLE rma_credit ADD COLUMN IF NOT EXISTS post_lease_until timestamptz;
ALTER TABLE rma_credit ADD COLUMN IF NOT EXISTS needs_attention text;
