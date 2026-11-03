-- Second half of 0020, kept apart so no step here runs under ACCESS EXCLUSIVE: CREATE INDEX takes SHARE
-- on purchase_order_line (reads go on; writes to PO lines wait for the build). dbx.Migrate runs every
-- file in a transaction, so CREATE INDEX CONCURRENTLY is not available (as in inventory 0021).
SET LOCAL lock_timeout = '5s';

-- Open supply (GET /purchase-order-lines) pages OPEN lines by delivery date, PO and line number: the
-- planner walks this index in keyset order and stops after one page, whatever the warehouse filter
-- (the effective warehouse is the line's own or the PO header's, so no single-table index serves it).
CREATE INDEX IF NOT EXISTS purchase_order_line_open_due ON purchase_order_line (tenant_id, delivery_date, purchase_order_id, line_no)
  WHERE status = 'OPEN';
-- Open supply of one product (planning nets per item).
CREATE INDEX IF NOT EXISTS purchase_order_line_product_open ON purchase_order_line (tenant_id, product_id, delivery_date, purchase_order_id, line_no)
  WHERE status = 'OPEN';
