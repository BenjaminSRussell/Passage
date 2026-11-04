-- Statistical quantity on recurring journal template lines (BR-11.2, FR-11.10). A template
-- line may carry a quantity with a unit of measure, the way a batch line does since 0025;
-- generating the template copies both onto the draft batch line unchanged (a quantity does
-- not scale with the base amount). Quantity and unit are both set or both absent. No new
-- table, so the grants of 0013 already cover the columns. The table holds templates only
-- (a few rows per ledger), so the checks are written inline.
SET LOCAL lock_timeout = '5s';

ALTER TABLE recurring_journal_line ADD COLUMN IF NOT EXISTS quantity numeric(18,6);
ALTER TABLE recurring_journal_line ADD COLUMN IF NOT EXISTS unit text;
ALTER TABLE recurring_journal_line ADD CONSTRAINT recurring_journal_line_quantity_check
  CHECK (quantity IS NULL OR quantity > 0);
ALTER TABLE recurring_journal_line ADD CONSTRAINT recurring_journal_line_unit_check
  CHECK (unit IS NULL OR unit ~ '^[A-Z0-9]{1,10}$');
ALTER TABLE recurring_journal_line ADD CONSTRAINT recurring_journal_line_quantity_unit
  CHECK ((quantity IS NULL) = (unit IS NULL));
