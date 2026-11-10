-- Supplier portal (#511): an invoice document a vendor submits through the portal names the vendor that submitted it, so the vendor
-- reads back only its own submissions. Captures keyed by an AP clerk keep a null vendor. Additive only.
ALTER TABLE receiving_invoice_capture ADD COLUMN IF NOT EXISTS vendor_id uuid;
CREATE INDEX IF NOT EXISTS receiving_invoice_capture_vendor_ix ON receiving_invoice_capture (tenant_id, vendor_id, created_at DESC) WHERE vendor_id IS NOT NULL;
