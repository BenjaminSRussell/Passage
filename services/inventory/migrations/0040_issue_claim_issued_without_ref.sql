-- An inventory issue also settles the claims that name no ref (a warehouse
-- pick no document line is known for), so an ISSUED row may carry an empty
-- ref. 0041 validates, as 0039 does for 0038.
SET LOCAL lock_timeout = '5s';

ALTER TABLE inventory_issue_claim DROP CONSTRAINT IF EXISTS inventory_issue_claim_issue_ref_check;
ALTER TABLE inventory_issue_claim ADD CONSTRAINT inventory_issue_claim_issue_ref_check
  CHECK (char_length(issue_ref) <= 255) NOT VALID;
