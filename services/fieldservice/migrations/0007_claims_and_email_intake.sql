-- Field service, slice 5 (PSG-4505): supplier warranty claims with vendor recovery
-- (FR-45.15, BR-45.12) and email-to-case intake (FR-45.16, BR-45.13). Tenant RLS on
-- every table; the tables that carry legal_entity_id also get the restrictive
-- legal-entity scope policy.

-- A case mailbox routes inbound mail of one address to a default category.
CREATE TABLE IF NOT EXISTS service_case_mailbox (
  tenant_id uuid NOT NULL,
  -- Lower-cased address; the natural key of the mailbox.
  address text NOT NULL CHECK (char_length(address) BETWEEN 3 AND 320 AND address = lower(address)),
  legal_entity_id uuid NOT NULL,
  category_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, address),
  FOREIGN KEY (tenant_id, category_id) REFERENCES service_case_category (tenant_id, id)
);
ALTER TABLE service_case_mailbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_case_mailbox FORCE ROW LEVEL SECURITY;
CREATE POLICY service_case_mailbox_tenant_access ON service_case_mailbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_case_mailbox_legal_entity_scope ON service_case_mailbox AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- One inbound email. message_id (the RFC 5322 Message-ID) is unique per tenant, so a
-- redelivery is answered from the stored row. Every message lands on a case: it is
-- attached to an open one or opened a new one (route).
CREATE TABLE IF NOT EXISTS service_inbound_message (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  mailbox text NOT NULL,
  legal_entity_id uuid NOT NULL,
  message_id text NOT NULL CHECK (char_length(message_id) BETWEEN 1 AND 998),
  in_reply_to text NOT NULL DEFAULT '' CHECK (char_length(in_reply_to) <= 998),
  from_address text NOT NULL CHECK (char_length(from_address) BETWEEN 3 AND 320),
  subject text NOT NULL DEFAULT '' CHECK (char_length(subject) <= 998),
  body text NOT NULL DEFAULT '' CHECK (char_length(body) <= 100000),
  received_at timestamptz NOT NULL,
  case_id uuid NOT NULL,
  route text NOT NULL CHECK (route IN ('ATTACHED', 'NEW_CASE')),
  -- The closed case a NEW_CASE message continued (BR-45.13 reopened_from).
  reopened_from uuid,
  automated boolean NOT NULL DEFAULT false,
  -- True when the hub must send the one acknowledgment of the new case.
  acknowledge boolean NOT NULL DEFAULT false,
  -- [{"name", "contentType", "sizeBytes", "objectRef"}]: metadata only, bytes stay in the object store.
  attachments jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(attachments) = 'array'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, message_id),
  CHECK (route = 'NEW_CASE' OR (reopened_from IS NULL AND NOT acknowledge)),
  FOREIGN KEY (tenant_id, mailbox) REFERENCES service_case_mailbox (tenant_id, address),
  FOREIGN KEY (tenant_id, case_id) REFERENCES service_generic_case (tenant_id, id),
  FOREIGN KEY (tenant_id, reopened_from) REFERENCES service_generic_case (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS service_inbound_message_recent ON service_inbound_message (tenant_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS service_inbound_message_case ON service_inbound_message (tenant_id, case_id);
ALTER TABLE service_inbound_message ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_inbound_message FORCE ROW LEVEL SECURITY;
CREATE POLICY service_inbound_message_tenant_access ON service_inbound_message AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_inbound_message_legal_entity_scope ON service_inbound_message AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Email lines on the case log (CASE-MAIL-001).
ALTER TABLE service_case_log DROP CONSTRAINT IF EXISTS service_case_log_kind_check;
ALTER TABLE service_case_log ADD CONSTRAINT service_case_log_kind_check
  CHECK (kind IN ('CREATED', 'STAGE', 'STATUS', 'ASSOCIATION', 'REOPENED', 'EMAIL'));

-- A supplier warranty claim (SWC number). Lines are
-- [{"itemId", "serial", "failureCode", "partAmount", "laborHours", "laborRate", "claimedAmount", "acceptedAmount", "sourceId"}]
-- with amounts as decimal strings in the claim currency.
CREATE TABLE IF NOT EXISTS service_supplier_claim (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  vendor_party_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  source_kind text NOT NULL CHECK (source_kind IN ('SERVICE_WO', 'EAM_WO')),
  source_ids jsonb NOT NULL CHECK (jsonb_typeof(source_ids) = 'array'),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  labor_rate numeric(20,6) NOT NULL DEFAULT 0 CHECK (labor_rate >= 0),
  lines jsonb NOT NULL CHECK (jsonb_typeof(lines) = 'array'),
  claimed_total numeric(20,6) NOT NULL CHECK (claimed_total >= 0),
  accepted_total numeric(20,6) NOT NULL DEFAULT 0 CHECK (accepted_total >= 0 AND accepted_total <= claimed_total),
  recovered_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (recovered_amount >= 0),
  status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'ACCEPTED', 'PARTIALLY_ACCEPTED',
    'REJECTED', 'SETTLED', 'CLOSED')),
  -- The work order completion date the claim window counts from (BR-45.12).
  completed_on date NOT NULL,
  window_days integer NOT NULL DEFAULT 30 CHECK (window_days BETWEEN 1 AND 365),
  return_required boolean NOT NULL DEFAULT false,
  return_order_id text NOT NULL DEFAULT '' CHECK (char_length(return_order_id) <= 100),
  submitted_on date,
  vendor_reference text NOT NULL DEFAULT '' CHECK (char_length(vendor_reference) <= 100),
  decision_reason text NOT NULL DEFAULT '' CHECK (char_length(decision_reason) <= 500),
  decided_at timestamptz,
  credit_note_id text NOT NULL DEFAULT '' CHECK (char_length(credit_note_id) <= 100),
  settled_at timestamptz,
  closed_at timestamptz,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (status <> 'SETTLED' OR (recovered_amount = accepted_total AND credit_note_id <> ''))
);
CREATE INDEX IF NOT EXISTS service_supplier_claim_recent ON service_supplier_claim (tenant_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS service_supplier_claim_vendor ON service_supplier_claim (tenant_id, vendor_party_id, status);
ALTER TABLE service_supplier_claim ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_supplier_claim FORCE ROW LEVEL SECURITY;
CREATE POLICY service_supplier_claim_tenant_access ON service_supplier_claim AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_supplier_claim_legal_entity_scope ON service_supplier_claim AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- One row per claimed source work order, so a work order is claimed once.
CREATE TABLE IF NOT EXISTS service_supplier_claim_source (
  tenant_id uuid NOT NULL,
  source_kind text NOT NULL CHECK (source_kind IN ('SERVICE_WO', 'EAM_WO')),
  source_id uuid NOT NULL,
  claim_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, source_kind, source_id),
  FOREIGN KEY (tenant_id, claim_id) REFERENCES service_supplier_claim (tenant_id, id)
);
ALTER TABLE service_supplier_claim_source ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_supplier_claim_source FORCE ROW LEVEL SECURITY;
CREATE POLICY service_supplier_claim_source_tenant_access ON service_supplier_claim_source AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges, in the style of 0002. Inbound messages and claim
-- sources are append-only. The store upserts service_case_mailbox and updates
-- service_supplier_claim (submit, decision, settle, close), locking it FOR UPDATE.
-- Nothing is deleted. Skipped when the role does not exist (developer databases
-- without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON service_inbound_message, service_supplier_claim_source TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON service_case_mailbox, service_supplier_claim TO passage_app';
  END IF;
END
$$;
