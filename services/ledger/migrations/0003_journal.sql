-- Content-addressed dimension combinations: id = SHA-256 of the canonical tuple (FR-11.4).
CREATE TABLE IF NOT EXISTS dimension_combination (
  tenant_id uuid NOT NULL,
  id bytea NOT NULL CHECK (octet_length(id) = 32),
  main_account_id uuid NOT NULL,
  display_name text NOT NULL,
  dimensions jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, main_account_id) REFERENCES main_account (tenant_id, id)
);
ALTER TABLE dimension_combination ENABLE ROW LEVEL SECURITY;
ALTER TABLE dimension_combination FORCE ROW LEVEL SECURITY;
CREATE POLICY dimension_combination_tenant_access ON dimension_combination AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Journal entries (FR-11.5, FR-11.6). Append-only: see the trigger below.
CREATE TABLE IF NOT EXISTS journal_entry (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  document_id uuid NOT NULL,
  voucher text NOT NULL CHECK (char_length(voucher) BETWEEN 1 AND 30),
  posting_sequence bigint NOT NULL CHECK (posting_sequence >= 1),
  posting_date date NOT NULL,
  period_key text NOT NULL CHECK (period_key ~ '^[0-9]{4}-[0-9]{2}$'),
  document_date date,
  description text CHECK (char_length(description) <= 255),
  source_system text NOT NULL CHECK (char_length(source_system) BETWEEN 1 AND 64),
  source_type text NOT NULL CHECK (char_length(source_type) BETWEEN 1 AND 64),
  source_id text NOT NULL CHECK (char_length(source_id) BETWEEN 1 AND 128),
  source_uri text,
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
  payload_hash bytea NOT NULL CHECK (octet_length(payload_hash) = 32),
  reversal_of_entry_id uuid,
  posted_at timestamptz NOT NULL DEFAULT now(),
  posted_by text NOT NULL DEFAULT '',
  trace_id text NOT NULL CHECK (char_length(trace_id) = 32),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, ledger_id, voucher),
  UNIQUE (tenant_id, ledger_id, posting_sequence),
  UNIQUE (tenant_id, legal_entity_id, idempotency_key),
  UNIQUE (tenant_id, reversal_of_entry_id),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
  FOREIGN KEY (tenant_id, reversal_of_entry_id) REFERENCES journal_entry (tenant_id, id)
);
ALTER TABLE journal_entry ENABLE ROW LEVEL SECURITY;
ALTER TABLE journal_entry FORCE ROW LEVEL SECURITY;
CREATE POLICY journal_entry_tenant_access ON journal_entry AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY journal_entry_legal_entity_scope ON journal_entry AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS journal_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  entry_id uuid NOT NULL,
  line_number int NOT NULL CHECK (line_number BETWEEN 1 AND 10000),
  main_account_id uuid NOT NULL,
  dimension_combination_id bytea NOT NULL,
  debit_credit char(1) NOT NULL CHECK (debit_credit IN ('D', 'C')),
  currency_txn char(3) NOT NULL,
  amount_txn numeric(20,6) NOT NULL CHECK (amount_txn >= 0),
  amount_acct numeric(20,6) NOT NULL CHECK (amount_acct >= 0),
  negative_posting boolean NOT NULL DEFAULT false,
  line_text text CHECK (char_length(line_text) <= 255),
  source_line_ref text CHECK (char_length(source_line_ref) <= 200),
  reversal_of_line_id uuid,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, entry_id, line_number),
  FOREIGN KEY (tenant_id, entry_id) REFERENCES journal_entry (tenant_id, id),
  FOREIGN KEY (tenant_id, main_account_id) REFERENCES main_account (tenant_id, id),
  FOREIGN KEY (tenant_id, dimension_combination_id) REFERENCES dimension_combination (tenant_id, id)
);
ALTER TABLE journal_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE journal_line FORCE ROW LEVEL SECURITY;
CREATE POLICY journal_line_tenant_access ON journal_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY journal_line_legal_entity_scope ON journal_line AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Append-only enforcement: UPDATE and DELETE raise for every role, superuser included.
CREATE OR REPLACE FUNCTION ledger_reject_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'LEDGER_APPEND_ONLY: % on % is not allowed', TG_OP, TG_TABLE_NAME
    USING ERRCODE = 'insufficient_privilege';
END;
$$;
CREATE TRIGGER journal_entry_append_only BEFORE UPDATE OR DELETE ON journal_entry
  FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();
CREATE TRIGGER journal_line_append_only BEFORE UPDATE OR DELETE ON journal_line
  FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();
CREATE OR REPLACE FUNCTION ledger_reject_truncate() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'LEDGER_APPEND_ONLY: TRUNCATE on % is not allowed', TG_TABLE_NAME
    USING ERRCODE = 'insufficient_privilege';
END;
$$;
CREATE TRIGGER journal_entry_no_truncate BEFORE TRUNCATE ON journal_entry
  FOR EACH STATEMENT EXECUTE FUNCTION ledger_reject_truncate();
CREATE TRIGGER journal_line_no_truncate BEFORE TRUNCATE ON journal_line
  FOR EACH STATEMENT EXECUTE FUNCTION ledger_reject_truncate();

-- BR-11.1 balance by construction, repeated inside the database: at commit, every
-- inserted entry must have at least one line and equal debit and credit totals in
-- amount_txn and amount_acct. A deferred constraint trigger on the entry (not the
-- line) runs one aggregate per entry, after all of its lines are inserted.
CREATE OR REPLACE FUNCTION ledger_check_entry_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  lines bigint;
  txn_net numeric;
  acct_net numeric;
BEGIN
  SELECT count(*),
         coalesce(sum(CASE WHEN debit_credit = 'D' THEN amount_txn ELSE -amount_txn END), 0),
         coalesce(sum(CASE WHEN debit_credit = 'D' THEN amount_acct ELSE -amount_acct END), 0)
    INTO lines, txn_net, acct_net
    FROM journal_line
   WHERE tenant_id = NEW.tenant_id AND entry_id = NEW.id;
  IF lines = 0 OR txn_net <> 0 OR acct_net <> 0 THEN
    RAISE EXCEPTION 'LEDGER_UNBALANCED: entry % has % lines, net txn %, net acct %', NEW.id, lines, txn_net, acct_net
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER journal_entry_balanced AFTER INSERT ON journal_entry
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION ledger_check_entry_balanced();
