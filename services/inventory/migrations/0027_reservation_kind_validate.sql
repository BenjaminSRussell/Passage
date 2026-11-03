-- Second half of 0026: VALIDATE CONSTRAINT takes SHARE UPDATE EXCLUSIVE, so reads and writes go on.
SET LOCAL lock_timeout = '5s';

ALTER TABLE inventory_reservation VALIDATE CONSTRAINT inventory_reservation_kind_check;
