-- Second half of 0030: VALIDATE CONSTRAINT takes SHARE UPDATE EXCLUSIVE.
SET LOCAL lock_timeout = '5s';

ALTER TABLE sales_order_line VALIDATE CONSTRAINT sales_order_line_catch_weight_check;
