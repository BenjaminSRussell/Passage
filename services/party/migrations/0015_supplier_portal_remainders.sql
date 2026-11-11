-- Supplier portal remainders (#511): the structured change a PROFILE_CHANGE request asks for (applied to the party when a buyer
-- approves it), public prospect registrations a buyer reviews, and vendor-admin invitations of colleagues. Additive only.

ALTER TABLE vendor_request ADD COLUMN IF NOT EXISTS changes jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(changes) = 'object');
ALTER TABLE vendor_request ADD COLUMN IF NOT EXISTS applied_at timestamptz;

-- A company that registered itself on the public page. It is not a party: nothing in the master data exists until a buyer accepts
-- it, which creates the party and a blocked vendor role and opens the onboarding questionnaire (vendor_request, kind ONBOARDING).
CREATE TABLE IF NOT EXISTS vendor_prospect (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  company_name text NOT NULL CHECK (char_length(company_name) BETWEEN 1 AND 200),
  contact_name text NOT NULL CHECK (char_length(contact_name) BETWEEN 1 AND 140),
  email text NOT NULL CHECK (char_length(email) BETWEEN 3 AND 254),
  phone text NOT NULL DEFAULT '' CHECK (char_length(phone) <= 40),
  country char(2) NOT NULL,
  tax_id text NOT NULL DEFAULT '' CHECK (char_length(tax_id) <= 40),
  website text NOT NULL DEFAULT '' CHECK (char_length(website) <= 200),
  message text NOT NULL DEFAULT '' CHECK (char_length(message) <= 2000),
  status text NOT NULL DEFAULT 'NEW' CHECK (status IN ('NEW', 'ACCEPTED', 'REJECTED')),
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_by text,
  decided_at timestamptz,
  decision_note text NOT NULL DEFAULT '' CHECK (char_length(decision_note) <= 1000),
  party_id uuid,
  vendor_id uuid,
  request_id uuid,
  PRIMARY KEY (tenant_id, id),
  CHECK ((decided_at IS NULL) = (decided_by IS NULL)),
  CHECK ((status = 'NEW') = (decided_at IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS vendor_prospect_one_new_per_email ON vendor_prospect (tenant_id, legal_entity_id, lower(email)) WHERE status = 'NEW';
CREATE INDEX IF NOT EXISTS vendor_prospect_status ON vendor_prospect (tenant_id, status, created_at DESC);

-- An invitation of a colleague by a vendor administrator. Only the SHA-256 of the secret is stored; the token is shown once.
CREATE TABLE IF NOT EXISTS vendor_invite (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  email text NOT NULL CHECK (char_length(email) BETWEEN 3 AND 254),
  display_name text NOT NULL DEFAULT '' CHECK (char_length(display_name) <= 140),
  role text NOT NULL DEFAULT 'supplier-user' CHECK (role = 'supplier-user'),
  token_hash text NOT NULL CHECK (token_hash ~ '^[0-9a-f]{64}$'),
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'ACCEPTED', 'REVOKED')),
  expires_at timestamptz NOT NULL,
  invited_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  accepted_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, vendor_id, legal_entity_id) REFERENCES vendor_role (tenant_id, id, legal_entity_id),
  CHECK ((status = 'ACCEPTED') = (accepted_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS vendor_invite_one_pending_per_email ON vendor_invite (tenant_id, vendor_id, lower(email)) WHERE status = 'PENDING';
CREATE INDEX IF NOT EXISTS vendor_invite_vendor ON vendor_invite (tenant_id, vendor_id, created_at DESC);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['vendor_prospect', 'vendor_invite'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_access', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS PERMISSIVE
      USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t || '_tenant_access', t);
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_legal_entity_scope', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS RESTRICTIVE
      USING (current_setting('passage.legal_entity_scopes', true) = '*'
             OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
      WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
             OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))$p$, t || '_legal_entity_scope', t);
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON vendor_prospect, vendor_invite TO passage_app';
  END IF;
END
$$;
