-- PSG-1505 (wave 15, slice 5): journal line read model for drill level 2
-- (FR-15.7, BR-15.4). One row per ledger line, keyed like the ledger's own
-- order (posting sequence, line number). A line belongs to the reporting
-- balance (ledger, period, account, combination, currency type); net is
-- debit - credit. Lines carry the source document link and the trace id.

CREATE TABLE IF NOT EXISTS reporting_journal_line (
  tenant_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  posting_sequence bigint NOT NULL CHECK (posting_sequence >= 1),
  line_number integer NOT NULL CHECK (line_number >= 1),
  posting_date date NOT NULL,
  period_year integer NOT NULL,
  period_no integer NOT NULL CHECK (period_no >= 0),
  account text NOT NULL,
  combination text NOT NULL,
  currency_type text NOT NULL CHECK (currency_type IN ('acct', 'rpt', 'txn')),
  voucher text NOT NULL DEFAULT '',
  debit numeric(20,6) NOT NULL DEFAULT 0,
  credit numeric(20,6) NOT NULL DEFAULT 0,
  net numeric(20,6) GENERATED ALWAYS AS (debit - credit) STORED,
  closing_entry boolean NOT NULL DEFAULT false,
  source_uri text NOT NULL DEFAULT '',
  trace_id text NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, ledger_id, posting_sequence, line_number)
);
-- Drill reads the lines of a set of balances in BR-15.4 order.
CREATE INDEX IF NOT EXISTS reporting_journal_line_balance
  ON reporting_journal_line (tenant_id, ledger_id, period_year, period_no, account, combination, currency_type,
    posting_date, posting_sequence, line_number);
ALTER TABLE reporting_journal_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE reporting_journal_line FORCE ROW LEVEL SECURITY;
CREATE POLICY reporting_journal_line_tenant_access ON reporting_journal_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
