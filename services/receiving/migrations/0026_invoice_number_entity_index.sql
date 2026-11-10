CREATE INDEX IF NOT EXISTS receiving_vendor_invoice_number_le_ix ON receiving_vendor_invoice (tenant_id, legal_entity_id, lower(invoice_number));
