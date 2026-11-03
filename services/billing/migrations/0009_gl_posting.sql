-- The invoice journal (FR-29.3): one ledger entry per posted invoice or credit note, its id in
-- invoice.journal_posting_id. A refusal by the ledger is recorded on the document (attempt count, last
-- error, next attempt with backoff) so it never blocks the documents behind it, and the document is
-- parked with its reason code after the attempt limit until an operator asks for a retry.

ALTER TABLE invoice
  ADD COLUMN IF NOT EXISTS gl_attempt_count integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS gl_last_error text,
  ADD COLUMN IF NOT EXISTS gl_next_attempt_at timestamptz,
  ADD COLUMN IF NOT EXISTS gl_parked_code text,
  ADD COLUMN IF NOT EXISTS gl_refusal_count integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS gl_split jsonb;

-- gl_split: an invoice that booked the revrec allocation instead of its billed nets (ASC 606) keeps, per line number,
-- the allocated amount and the part of it credited to 2400; a credit note mirrors its original line in those proportions.
-- gl_refusal_count counts the ledger's verdicts only (parking); gl_attempt_count counts every failed attempt (backoff).

CREATE INDEX IF NOT EXISTS invoice_gl_pending_idx ON invoice (tenant_id, legal_entity_id, posting_date, created_at, id)
  WHERE journal_posting_id IS NULL AND gl_parked_code IS NULL AND status IN ('POSTED', 'PARTIALLY_SETTLED', 'SETTLED');
