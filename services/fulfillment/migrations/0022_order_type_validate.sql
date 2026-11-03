-- Second half of 0021, kept apart so the scan runs without ACCESS EXCLUSIVE:
-- VALIDATE CONSTRAINT takes SHARE UPDATE EXCLUSIVE, so reads and writes on
-- shipment go on while it checks the existing rows (all of which carry the
-- defaults). dbx.Migrate runs every file in its own transaction.
SET LOCAL lock_timeout = '5s';

ALTER TABLE shipment VALIDATE CONSTRAINT shipment_order_type_check;
ALTER TABLE shipment VALIDATE CONSTRAINT shipment_priority_check;
