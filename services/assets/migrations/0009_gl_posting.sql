-- Wave 37, D2j: asset transactions and depreciation runs are booked in the general ledger.
-- The posting profile maps a legal entity and asset class (ALL is the wildcard) to the
-- accounts the journals use; with no row the service uses its documented placeholder
-- accounts. asset_gl_entry links every booked transaction to its ledger entry (it is
-- append-only because asset_transaction is, so the link cannot live on the transaction).

CREATE TABLE IF NOT EXISTS asset_posting_profile (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  class_code text NOT NULL CHECK (class_code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  cost_account text NOT NULL,
  accumulated_account text NOT NULL,
  depreciation_expense_account text NOT NULL,
  impairment_account text NOT NULL,
  revaluation_account text NOT NULL,
  acquisition_clearing_account text NOT NULL,
  proceeds_account text NOT NULL,
  gain_account text NOT NULL,
  loss_account text NOT NULL,
  transfer_clearing_account text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, class_code)
);

CREATE TABLE IF NOT EXISTS asset_gl_entry (
  tenant_id uuid NOT NULL,
  transaction_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  journal_entry_id uuid,
  posting_date date NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, transaction_id),
  FOREIGN KEY (tenant_id, transaction_id) REFERENCES asset_transaction (tenant_id, id)
);

-- A booking is written here, in the transaction that decides it, before the ledger is called; retries resend exactly this payload and the row is
-- deleted when the ledger has confirmed it and asset_gl_entry links its transactions.
CREATE TABLE IF NOT EXISTS asset_pending_booking (
  tenant_id uuid NOT NULL,
  idempotency_key text NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  book_code text NOT NULL,
  currency char(3) NOT NULL,
  source_type text NOT NULL,
  document_id text NOT NULL,
  posting_date date NOT NULL,
  lines jsonb NOT NULL,
  transaction_ids uuid[] NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, idempotency_key)
);

ALTER TABLE asset_posting_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_posting_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_posting_profile_tenant_access ON asset_posting_profile AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY asset_posting_profile_legal_entity_scope ON asset_posting_profile AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE asset_pending_booking ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_pending_booking FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_pending_booking_tenant_access ON asset_pending_booking AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY asset_pending_booking_legal_entity_scope ON asset_pending_booking AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE asset_gl_entry ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_gl_entry FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_gl_entry_tenant_access ON asset_gl_entry AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY asset_gl_entry_legal_entity_scope ON asset_gl_entry AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON asset_posting_profile TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON asset_gl_entry TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON asset_pending_booking TO passage_app';
  END IF;
END
$$;
