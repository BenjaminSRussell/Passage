-- Ledger transactions inquiry (FR-11.14, AC-11.10): lines of one account and a
-- date range, in posting order. journal_entry (tenant_id, ledger_id,
-- posting_sequence) is already unique; these cover the date and account filters.
CREATE INDEX IF NOT EXISTS journal_entry_ledger_date_ix
    ON journal_entry (tenant_id, ledger_id, posting_date, posting_sequence);
CREATE INDEX IF NOT EXISTS journal_line_account_ix
    ON journal_line (tenant_id, main_account_id, entry_id);
