-- Wave 17 purchasing slice 25: the backordered lines inquiry (PRD 3.17.14, GET
-- /inquiries/backordered-lines) reads OPEN lines that were received in part. They are a small
-- subset of the OPEN lines the 0021 indexes cover, so a partial index on exactly that predicate
-- keeps the inquiry from walking every open line of the tenant. No new table and no grant.
-- dbx.Migrate runs every file in a transaction, so CREATE INDEX CONCURRENTLY is not available (as in
-- 0021 and 0026): the build takes SHARE on purchase_order_line (reads go on; writes wait for it).
SET LOCAL lock_timeout = '5s';

CREATE INDEX IF NOT EXISTS purchase_order_line_backordered ON purchase_order_line (tenant_id, purchase_order_id, line_no)
  WHERE status = 'OPEN' AND received_qty > 0 AND received_qty < qty;
