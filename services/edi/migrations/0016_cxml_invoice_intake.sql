-- #35 (35-4) round 3: an inbound cXML InvoiceDetailRequest of one purchase order lands in payables' vendor-invoice intake; the
-- message records the intake row it became.
ALTER TABLE message DROP CONSTRAINT IF EXISTS message_downstream_kind_check;
ALTER TABLE message ADD CONSTRAINT message_downstream_kind_check CHECK (downstream_kind IN ('PURCHASE_ORDER', 'ASN', 'VENDOR_INVOICE'));
