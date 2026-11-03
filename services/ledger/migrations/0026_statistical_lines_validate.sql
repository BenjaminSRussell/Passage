-- Second half of 0025, kept apart so no step here runs under ACCESS EXCLUSIVE:
-- VALIDATE CONSTRAINT takes SHARE UPDATE EXCLUSIVE, so postings and reads go on
-- while journal_line is scanned. Every existing row has quantity and unit NULL.
SET LOCAL lock_timeout = '5s';

ALTER TABLE journal_line VALIDATE CONSTRAINT journal_line_quantity_check;
ALTER TABLE journal_line VALIDATE CONSTRAINT journal_line_unit_check;
ALTER TABLE journal_line VALIDATE CONSTRAINT journal_line_quantity_unit;

ALTER TABLE journal_batch_line VALIDATE CONSTRAINT journal_batch_line_quantity_check;
ALTER TABLE journal_batch_line VALIDATE CONSTRAINT journal_batch_line_unit_check;
ALTER TABLE journal_batch_line VALIDATE CONSTRAINT journal_batch_line_quantity_unit;
