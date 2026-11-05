-- #480: the delivery destination of a receipt line, copied from its PO line
-- (purchasing returns the effective site and warehouse of the line) when the
-- line is bound to it. receipt.posted carries them so inventory can book the
-- receipt under the mandatory site dimension. NULL for a line without a PO
-- line. receiving_receipt_line is a live table: nullable columns without a
-- default only change the catalog, and the lock_timeout keeps the migration
-- from queueing behind a long transaction.
SET LOCAL lock_timeout = '5s';

ALTER TABLE receiving_receipt_line ADD COLUMN IF NOT EXISTS site_id uuid;
ALTER TABLE receiving_receipt_line ADD COLUMN IF NOT EXISTS warehouse_id uuid;
