-- Balance projection: period-to-date per (ledger, account, combination, period, currency type) (FR-11.7).
-- Upserted in the posting transaction; rebuilt from the journal by the rebuild endpoint (BR-11.7).
CREATE TABLE IF NOT EXISTS balance (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  main_account_id uuid NOT NULL,
  dimension_combination_id bytea NOT NULL,
  period_key text NOT NULL CHECK (period_key ~ '^[0-9]{4}-[0-9]{2}$'),
  currency_type text NOT NULL DEFAULT 'acct' CHECK (currency_type IN ('txn', 'acct', 'rpt')),
  currency char(3) NOT NULL,
  debit numeric(20,6) NOT NULL DEFAULT 0 CHECK (debit >= 0),
  credit numeric(20,6) NOT NULL DEFAULT 0 CHECK (credit >= 0),
  net numeric(20,6) NOT NULL DEFAULT 0,
  line_count bigint NOT NULL DEFAULT 0 CHECK (line_count >= 0),
  last_posting_sequence bigint NOT NULL DEFAULT 0 CHECK (last_posting_sequence >= 0),
  PRIMARY KEY (tenant_id, ledger_id, main_account_id, dimension_combination_id, period_key, currency_type),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
  FOREIGN KEY (tenant_id, main_account_id) REFERENCES main_account (tenant_id, id),
  FOREIGN KEY (tenant_id, dimension_combination_id) REFERENCES dimension_combination (tenant_id, id)
);
ALTER TABLE balance ENABLE ROW LEVEL SECURITY;
ALTER TABLE balance FORCE ROW LEVEL SECURITY;
CREATE POLICY balance_tenant_access ON balance AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY balance_legal_entity_scope ON balance AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
