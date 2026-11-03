-- Second half of 0026, kept apart so the scan runs without ACCESS EXCLUSIVE:
-- VALIDATE CONSTRAINT takes SHARE UPDATE EXCLUSIVE, so reads and writes on
-- sales_order_line go on while it checks the existing rows (all of which carry
-- the zero defaults). dbx.Migrate runs every file in its own transaction.
SET LOCAL lock_timeout = '5s';

ALTER TABLE sales_order_line VALIDATE CONSTRAINT sales_order_line_progress_check;
