-- Second half of 0008, kept apart so the scan runs without ACCESS EXCLUSIVE:
-- VALIDATE CONSTRAINT takes SHARE UPDATE EXCLUSIVE, so reads and writes on
-- batch_genealogy go on while it checks the existing rows (all of which are
-- 'merge'). dbx.Migrate runs every file in its own transaction. No new tables.
SET LOCAL lock_timeout = '5s';

ALTER TABLE batch_genealogy VALIDATE CONSTRAINT batch_genealogy_relation_check;
