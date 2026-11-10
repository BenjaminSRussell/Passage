-- Second half of 0038, kept apart so the scan runs under SHARE UPDATE
-- EXCLUSIVE (claims go on being taken), not under the ACCESS EXCLUSIVE lock
-- the ALTER of 0038 holds until commit.
SET LOCAL lock_timeout = '5s';

ALTER TABLE inventory_issue_claim VALIDATE CONSTRAINT inventory_issue_claim_issue_ref_check;
ALTER TABLE inventory_issue_claim VALIDATE CONSTRAINT inventory_issue_claim_kind_check;
