ALTER TABLE pending_change DROP CONSTRAINT IF EXISTS pending_change_decided_by_required;
ALTER TABLE pending_change DROP CONSTRAINT IF EXISTS pending_change_decided_by_iff_decided;
ALTER TABLE pending_change
  ADD CONSTRAINT pending_change_decided_by_iff_decided
  CHECK ((status = 'pending') = (decided_by IS NULL));
