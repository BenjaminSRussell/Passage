CREATE INDEX IF NOT EXISTS receiving_invoice_capture_keyset_ix ON receiving_invoice_capture (tenant_id, created_at, id);
