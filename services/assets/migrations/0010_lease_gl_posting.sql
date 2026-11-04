-- Wave 37, D4d: the IFRS 16 lease is booked in the general ledger. The lease posting
-- profile maps a legal entity to the accounts of the recognition journal (Dr ROU asset,
-- Cr lease liability) and of every schedule period (interest, payment, ROU depreciation
-- and the remeasurement of a modification); with no row the service uses its documented
-- placeholder accounts. lease.recognition_journal_id and lease_schedule_line.posted_journal_id
-- hold the ledger entry of each booked document. A booking in flight is kept in
-- asset_pending_booking (book_code LEASE) like the asset bookings, so a retry resends it unchanged.

ALTER TABLE lease ADD COLUMN IF NOT EXISTS recognition_journal_id uuid;

CREATE TABLE IF NOT EXISTS lease_posting_profile (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  rou_asset_account text NOT NULL,
  rou_accumulated_account text NOT NULL,
  liability_account text NOT NULL,
  interest_expense_account text NOT NULL,
  depreciation_expense_account text NOT NULL,
  payment_clearing_account text NOT NULL,
  remeasurement_gain_account text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id)
);

ALTER TABLE lease_posting_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE lease_posting_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY lease_posting_profile_tenant_access ON lease_posting_profile AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY lease_posting_profile_legal_entity_scope ON lease_posting_profile AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON lease_posting_profile TO passage_app';
    EXECUTE 'GRANT UPDATE ON lease_schedule_line TO passage_app';
  END IF;
END
$$;
