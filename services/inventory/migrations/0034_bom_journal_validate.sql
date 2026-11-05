-- Second half of 0033: VALIDATE CONSTRAINT takes SHARE UPDATE EXCLUSIVE.
SET LOCAL lock_timeout = '5s';

ALTER TABLE inventory_journal VALIDATE CONSTRAINT inventory_journal_journal_type_check;
ALTER TABLE inventory_journal_name VALIDATE CONSTRAINT inventory_journal_name_journal_type_check;
