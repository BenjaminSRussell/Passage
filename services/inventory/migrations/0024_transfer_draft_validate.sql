-- Second half of 0023, kept apart so no step here runs under ACCESS EXCLUSIVE: VALIDATE CONSTRAINT
-- takes SHARE UPDATE EXCLUSIVE, so reads and writes of transfer_order go on while it scans.
SET LOCAL lock_timeout = '5s';

ALTER TABLE transfer_order VALIDATE CONSTRAINT transfer_order_status_check;
ALTER TABLE transfer_order VALIDATE CONSTRAINT transfer_order_delivery_terms_check;
ALTER TABLE transfer_order VALIDATE CONSTRAINT transfer_order_mode_of_delivery_check;
