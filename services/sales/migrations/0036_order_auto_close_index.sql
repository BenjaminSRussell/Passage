-- Wave 27 slice 31 review: the nightly order auto-close (POST
-- /orders:auto-close) lists INVOICED orders of type ORDER by last change,
-- oldest first. sales_order had no index on status or updated_at, so each run
-- read every order of the tenant. This partial index holds only the orders the
-- job can close; CLOSED orders leave it. No new table, so no grant.

SET LOCAL lock_timeout = '5s';
CREATE INDEX IF NOT EXISTS sales_order_auto_close ON sales_order (tenant_id, updated_at, id)
  WHERE status = 'INVOICED' AND order_type = 'ORDER';
