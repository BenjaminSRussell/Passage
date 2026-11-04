-- Wave 17 purchasing slice 21: order price history (PRD 3.17.14, GET /inquiries/price-history) reads
-- every line of one product whatever its status; the product indexes of 0021 cover OPEN lines only,
-- so without this one the inquiry scans purchase_order_line. No new table and no grant. dbx.Migrate
-- runs every file in a transaction, so CREATE INDEX CONCURRENTLY is not available (as in 0021):
-- the build takes SHARE on purchase_order_line (reads go on; writes to PO lines wait for it).
SET LOCAL lock_timeout = '5s';

CREATE INDEX IF NOT EXISTS purchase_order_line_product ON purchase_order_line (tenant_id, product_id)
  WHERE product_id IS NOT NULL;
