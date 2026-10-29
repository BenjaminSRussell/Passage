ALTER TABLE pending_change
  ADD CONSTRAINT pending_change_decided_by_required
  CHECK (status = 'pending' OR decided_by IS NOT NULL);
