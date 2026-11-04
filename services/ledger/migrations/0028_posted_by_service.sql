-- The verified service that posted an entry: the issuer of the service token, never a request field. A balance inquiry
-- that leaves out the year-end closing entries (exclude_year_end_closing) trusts only entries the close service posted.
-- The partial index is built CONCURRENTLY in 0032 so the build does not block journal_entry writes.
ALTER TABLE journal_entry ADD COLUMN IF NOT EXISTS posted_by_service text;
