CREATE TABLE IF NOT EXISTS vendor_role (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  party_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  account_number text NOT NULL CHECK (char_length(account_number) BETWEEN 1 AND 20),
  vendor_group_id uuid NOT NULL,
  payment_term_id uuid,
  currency_code char(3) NOT NULL,
  blocked text NOT NULL DEFAULT 'none' CHECK (blocked IN ('none', 'payment', 'all')),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, id, legal_entity_id),
  UNIQUE (tenant_id, party_id, legal_entity_id),
  UNIQUE (tenant_id, legal_entity_id, account_number),
  FOREIGN KEY (tenant_id, party_id) REFERENCES party (tenant_id, id),
  FOREIGN KEY (tenant_id, payment_term_id) REFERENCES payment_term (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS vendor_bank_account (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  account_holder text NOT NULL CHECK (char_length(account_holder) BETWEEN 1 AND 140),
  bank_country char(2) NOT NULL,
  currency_code char(3) NOT NULL,
  account_last4 text NOT NULL CHECK (char_length(account_last4) BETWEEN 1 AND 4),
  account_number_sealed bytea NOT NULL,
  routing_number_sealed bytea,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, vendor_id, legal_entity_id) REFERENCES vendor_role (tenant_id, id, legal_entity_id)
);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['vendor_role', 'vendor_bank_account'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_access', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS PERMISSIVE
      USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t || '_tenant_access', t);
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_legal_entity_scope', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS RESTRICTIVE
      USING (current_setting('passage.legal_entity_scopes', true) = '*'
             OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))$p$, t || '_legal_entity_scope', t);
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON vendor_role TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON vendor_bank_account TO passage_app';
  END IF;
END
$$;
