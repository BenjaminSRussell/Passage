CREATE INDEX IF NOT EXISTS receiving_vendor_invoice_keyset_ix ON receiving_vendor_invoice (tenant_id, created_at, id);
CREATE INDEX IF NOT EXISTS receiving_vendor_invoice_line_receipt_ix ON receiving_vendor_invoice_line (tenant_id, receipt_id);
CREATE INDEX IF NOT EXISTS receiving_receipt_purchase_order_ix ON receiving_receipt (tenant_id, purchase_order_id);
