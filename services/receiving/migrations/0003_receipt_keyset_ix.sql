CREATE INDEX IF NOT EXISTS receiving_receipt_keyset_ix ON receiving_receipt (tenant_id, created_at, id);
