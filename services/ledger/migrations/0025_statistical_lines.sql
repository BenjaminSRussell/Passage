-- Statistical quantity on lines (BR-11.2): a line may carry a quantity with a
-- unit of measure. A zero-amount line with a quantity is a statistical posting.
-- Quantity and unit are both set or both absent; the quantity is unsigned like
-- the amount, so the line's debit_credit side carries its sign. No new table, so
-- the grants of 0005 and 0009 already cover the columns; journal_line stays
-- append-only.
--
-- journal_line is the largest table of the service, so this file only changes
-- the catalog: nullable columns without a default and NOT VALID checks take
-- their ACCESS EXCLUSIVE lock for an instant and never scan or rewrite (a check
-- written inline on ADD COLUMN would scan the whole table under that lock). The
-- checks hold for every new row at once. The lock_timeout makes the migration
-- fail (and the rollout retry) instead of queueing behind a long transaction
-- while every later query queues behind it. 0026 validates the checks without
-- that lock.
SET LOCAL lock_timeout = '5s';

ALTER TABLE journal_line ADD COLUMN IF NOT EXISTS quantity numeric(18,6);
ALTER TABLE journal_line ADD COLUMN IF NOT EXISTS unit text;
ALTER TABLE journal_line ADD CONSTRAINT journal_line_quantity_check
  CHECK (quantity IS NULL OR quantity > 0) NOT VALID;
ALTER TABLE journal_line ADD CONSTRAINT journal_line_unit_check
  CHECK (unit IS NULL OR unit ~ '^[A-Z0-9]{1,10}$') NOT VALID;
ALTER TABLE journal_line ADD CONSTRAINT journal_line_quantity_unit
  CHECK ((quantity IS NULL) = (unit IS NULL)) NOT VALID;

ALTER TABLE journal_batch_line ADD COLUMN IF NOT EXISTS quantity numeric(18,6);
ALTER TABLE journal_batch_line ADD COLUMN IF NOT EXISTS unit text;
ALTER TABLE journal_batch_line ADD CONSTRAINT journal_batch_line_quantity_check
  CHECK (quantity IS NULL OR quantity > 0) NOT VALID;
ALTER TABLE journal_batch_line ADD CONSTRAINT journal_batch_line_unit_check
  CHECK (unit IS NULL OR unit ~ '^[A-Z0-9]{1,10}$') NOT VALID;
ALTER TABLE journal_batch_line ADD CONSTRAINT journal_batch_line_quantity_unit
  CHECK ((quantity IS NULL) = (unit IS NULL)) NOT VALID;
