-- A count plan may include vendor-owned (consignment) stock, counted like any
-- other row but posted as a quantity-only COUNT variance (BR-16.14: no value,
-- no GL entry). Existing plans keep leaving it out.
ALTER TABLE count_plan ADD COLUMN IF NOT EXISTS include_vendor_owned boolean NOT NULL DEFAULT false;
