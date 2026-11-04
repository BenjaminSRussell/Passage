-- Second half of 0029: VALIDATE CONSTRAINT takes SHARE UPDATE EXCLUSIVE.
SET LOCAL lock_timeout = '5s';

ALTER TABLE inventory_transaction VALIDATE CONSTRAINT inventory_transaction_cw_qty_check;
ALTER TABLE inventory_journal_line VALIDATE CONSTRAINT inventory_journal_line_cw_qty_check;
