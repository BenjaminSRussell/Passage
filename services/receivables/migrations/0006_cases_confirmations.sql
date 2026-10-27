-- Minimal collection cases (FR-30.18, interim until the wave 45 case framework) and
-- customer balance confirmations (FR-30.20, BR-30.13) with immutable replies.

CREATE TABLE IF NOT EXISTS collection_case (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  category text NOT NULL CHECK (category IN ('COLLECTION', 'DISPUTE', 'DEDUCTION')),
  owner text NOT NULL CHECK (length(owner) BETWEEN 1 AND 120),
  priority text NOT NULL DEFAULT 'NORMAL' CHECK (priority IN ('LOW', 'NORMAL', 'HIGH')),
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'CLOSED')),
  reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 500),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  open_item_id uuid,
  attached_amount numeric(20,6) CHECK (attached_amount IS NULL OR attached_amount >= 0),
  sla_hours integer NOT NULL CHECK (sla_hours >= 1),
  sla_due timestamptz NOT NULL,
  close_note text CHECK (close_note IS NULL OR length(close_note) <= 2000),
  closed_at timestamptz,
  idempotency_key text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, open_item_id) REFERENCES open_item (tenant_id, id),
  CHECK ((status = 'CLOSED') = (closed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS collection_case_status_idx ON collection_case (tenant_id, status, sla_due);
CREATE INDEX IF NOT EXISTS collection_case_customer_idx ON collection_case (tenant_id, customer_id);
ALTER TABLE collection_case ENABLE ROW LEVEL SECURITY;
ALTER TABLE collection_case FORCE ROW LEVEL SECURITY;
CREATE POLICY collection_case_tenant_access ON collection_case AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY collection_case_legal_entity_scope ON collection_case AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS balance_confirmation_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  as_of_date date NOT NULL,
  threshold numeric(20,6) NOT NULL CHECK (threshold >= 0),
  sample_size integer CHECK (sample_size IS NULL OR sample_size >= 1),
  include_zero boolean NOT NULL DEFAULT false,
  reminder_days integer NOT NULL DEFAULT 14 CHECK (reminder_days BETWEEN 1 AND 60),
  idempotency_key text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS balance_confirmation_run_created_idx ON balance_confirmation_run (tenant_id, created_at DESC);
ALTER TABLE balance_confirmation_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE balance_confirmation_run FORCE ROW LEVEL SECURITY;
CREATE POLICY balance_confirmation_run_tenant_access ON balance_confirmation_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY balance_confirmation_run_legal_entity_scope ON balance_confirmation_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS balance_confirmation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  run_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  our_balance numeric(20,6) NOT NULL,
  status text NOT NULL DEFAULT 'NO_REPLY' CHECK (status IN ('NO_REPLY', 'AGREED', 'DISAGREED')),
  customer_balance numeric(20,6),
  comment text CHECK (comment IS NULL OR length(comment) <= 500),
  replied_at timestamptz,
  case_id uuid,
  version integer NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, run_id, customer_id, currency),
  FOREIGN KEY (tenant_id, run_id) REFERENCES balance_confirmation_run (tenant_id, id),
  FOREIGN KEY (tenant_id, case_id) REFERENCES collection_case (tenant_id, id),
  CHECK ((status = 'NO_REPLY') = (replied_at IS NULL)),
  CHECK (status <> 'DISAGREED' OR (customer_balance IS NOT NULL AND case_id IS NOT NULL))
);
ALTER TABLE balance_confirmation ENABLE ROW LEVEL SECURITY;
ALTER TABLE balance_confirmation FORCE ROW LEVEL SECURITY;
CREATE POLICY balance_confirmation_tenant_access ON balance_confirmation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY balance_confirmation_legal_entity_scope ON balance_confirmation AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
