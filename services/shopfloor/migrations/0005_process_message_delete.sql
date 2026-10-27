-- PSG-25 slice 5: delete a process message with a reason (FR-25.17) and keyset paging of the monitor.
-- A deleted message stays for audit (deleted_at, deleted_by, delete_reason); it leaves the monitor,
-- cannot be reported or reprocessed, and the retry sweep skips it.

ALTER TABLE process_message ADD COLUMN IF NOT EXISTS deleted_at timestamptz;
ALTER TABLE process_message ADD COLUMN IF NOT EXISTS delete_reason text
  CHECK (delete_reason IS NULL OR char_length(delete_reason) BETWEEN 1 AND 500);
ALTER TABLE process_message ADD COLUMN IF NOT EXISTS deleted_by text CHECK (char_length(deleted_by) <= 200);
ALTER TABLE process_message ADD CONSTRAINT process_message_delete_consistent
  CHECK ((deleted_at IS NULL) = (delete_reason IS NULL));

-- Monitor pages walk (received_at, id) newest first and hide deleted messages.
CREATE INDEX IF NOT EXISTS process_message_monitor ON process_message (tenant_id, received_at DESC, id DESC)
  WHERE deleted_at IS NULL;
