-- The verified service that posted an entry: the issuer of the service token, never a request field. A balance inquiry
-- that leaves out the year-end closing entries (exclude_year_end_closing) trusts only entries the close service posted.
ALTER TABLE journal_entry ADD COLUMN IF NOT EXISTS posted_by_service text;
CREATE INDEX IF NOT EXISTS journal_entry_year_end_closing_ix ON journal_entry (tenant_id, ledger_id)
  WHERE posted_by_service = 'close' AND source_type = 'year_end_closing';
