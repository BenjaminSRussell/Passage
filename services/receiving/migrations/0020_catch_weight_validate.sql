-- Second half of 0019: VALIDATE CONSTRAINT takes SHARE UPDATE EXCLUSIVE.
SET LOCAL lock_timeout = '5s';

ALTER TABLE receiving_receipt_line VALIDATE CONSTRAINT receiving_receipt_line_catch_weight_check;
