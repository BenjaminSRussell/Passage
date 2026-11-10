-- #33 AC2: issue claims are tallied across kinds. issue_ref names the document
-- line a claim moves units for (a sales order line, a production order item):
-- claims that share a ref are successive steps of the same units (a pick, then
-- the ship of what was picked) and count once, as the largest per-kind total
-- of the ref. ISSUED rows record the part of an inventory issue booked for a
-- ref (the postings consumer's DELIVERED booking) that settles its claims, so
-- stock issued through inventory and claimed before is not counted twice.
-- inventory_issue_claim is a live table: the column with a constant default
-- and the NOT VALID checks only change the catalog, and the lock_timeout keeps
-- the migration from queueing behind a long transaction. 0039 validates.
SET LOCAL lock_timeout = '5s';

ALTER TABLE inventory_issue_claim ADD COLUMN IF NOT EXISTS issue_ref text NOT NULL DEFAULT '';
ALTER TABLE inventory_issue_claim DROP CONSTRAINT IF EXISTS inventory_issue_claim_issue_ref_check;
ALTER TABLE inventory_issue_claim ADD CONSTRAINT inventory_issue_claim_issue_ref_check
  CHECK (char_length(issue_ref) <= 255 AND (kind <> 'ISSUED' OR issue_ref <> '')) NOT VALID;
ALTER TABLE inventory_issue_claim DROP CONSTRAINT IF EXISTS inventory_issue_claim_kind_check;
ALTER TABLE inventory_issue_claim ADD CONSTRAINT inventory_issue_claim_kind_check
  CHECK (kind IN ('PICK', 'SHIP', 'CONSUME', 'ISSUED')) NOT VALID;
