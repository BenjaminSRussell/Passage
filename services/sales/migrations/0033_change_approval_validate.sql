-- Second half of 0032, kept apart so the scan runs without ACCESS EXCLUSIVE:
-- VALIDATE CONSTRAINT takes SHARE UPDATE EXCLUSIVE, so reads and writes on
-- sales_order go on while it checks the existing rows (all of which carry the
-- zero default). dbx.Migrate runs every file in its own transaction.
SET LOCAL lock_timeout = '5s';

ALTER TABLE sales_order VALIDATE CONSTRAINT sales_order_unapproved_change_value_check;
