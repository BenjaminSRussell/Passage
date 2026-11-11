-- 30-2: the GL account a payment's cash is booked to. Receipts and down payments booked Dr 1000 whatever bank the money came to;
-- a legal entity now maps each of its bank accounts, by the statement's own account id (the camt.053 Stmt/Acct/Id, an IBAN
-- or other id, upper case without spaces) and currency, to its ledger account. An entry with an empty account_ref is the
-- entity's default account for the currency. A journal with no mapping still books 1000.
CREATE TABLE IF NOT EXISTS ar_bank_account (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  account_ref text NOT NULL DEFAULT '' CHECK (length(account_ref) <= 40 AND account_ref ~ '^[A-Z0-9]*$'),
  gl_account text NOT NULL CHECK (gl_account ~ '^[0-9A-Za-z._-]{1,20}$'),
  name text NOT NULL DEFAULT '' CHECK (length(name) <= 100),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, currency, account_ref)
);
ALTER TABLE ar_bank_account ENABLE ROW LEVEL SECURITY;
ALTER TABLE ar_bank_account FORCE ROW LEVEL SECURITY;
CREATE POLICY ar_bank_account_tenant_access ON ar_bank_account AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ar_bank_account_legal_entity_scope ON ar_bank_account AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
ALTER TABLE payment ADD COLUMN IF NOT EXISTS bank_account_ref text CHECK (bank_account_ref IS NULL OR bank_account_ref ~ '^[A-Z0-9]{1,40}$');
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON ar_bank_account TO passage_app';
  END IF;
END
$$;
