-- Disputes, promises to pay, dunning runs and letters, interest notes (wave 30 slice 2).

CREATE TABLE IF NOT EXISTS dispute (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  open_item_id uuid NOT NULL,
  reason_code text NOT NULL CHECK (reason_code ~ '^[A-Z][A-Z_]{1,31}$'),
  disputed_amount numeric(20,6) NOT NULL CHECK (disputed_amount > 0),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  owner text NOT NULL CHECK (char_length(owner) BETWEEN 1 AND 120),
  status text NOT NULL CHECK (status IN ('OPEN', 'UNDER_REVIEW', 'RESOLVED_CREDIT', 'RESOLVED_PAID', 'RESOLVED_WRITE_OFF', 'REJECTED')),
  resolution_note text,
  sla_due timestamptz NOT NULL,
  idempotency_key text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, open_item_id) REFERENCES open_item (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS dispute_one_active_per_item ON dispute (tenant_id, open_item_id)
  WHERE status IN ('OPEN', 'UNDER_REVIEW');
ALTER TABLE dispute ENABLE ROW LEVEL SECURITY;
ALTER TABLE dispute FORCE ROW LEVEL SECURITY;
CREATE POLICY dispute_tenant_access ON dispute AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY dispute_legal_entity_scope ON dispute AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS promise_to_pay (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  open_item_id uuid NOT NULL,
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  promise_date date NOT NULL,
  idempotency_key text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, open_item_id) REFERENCES open_item (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS promise_open_item ON promise_to_pay (tenant_id, open_item_id, promise_date);
ALTER TABLE promise_to_pay ENABLE ROW LEVEL SECURITY;
ALTER TABLE promise_to_pay FORCE ROW LEVEL SECURITY;
CREATE POLICY promise_to_pay_tenant_access ON promise_to_pay AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY promise_to_pay_legal_entity_scope ON promise_to_pay AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS dunning_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  run_date date NOT NULL,
  grace_days integer NOT NULL CHECK (grace_days BETWEEN 0 AND 60),
  status text NOT NULL CHECK (status IN ('DRAFT', 'REVIEWED', 'ISSUED', 'CANCELLED')),
  exclusions jsonb NOT NULL DEFAULT '[]'::jsonb,
  idempotency_key text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  issued_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE dunning_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE dunning_run FORCE ROW LEVEL SECURITY;
CREATE POLICY dunning_run_tenant_access ON dunning_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY dunning_run_legal_entity_scope ON dunning_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS dunning_letter (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  run_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  level smallint NOT NULL CHECK (level BETWEEN 1 AND 4),
  prior_level smallint NOT NULL CHECK (prior_level BETWEEN 0 AND 4),
  amount numeric(20,6) NOT NULL CHECK (amount >= 0),
  fee_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (fee_amount >= 0),
  items jsonb NOT NULL,
  status text NOT NULL CHECK (status IN ('DRAFT', 'ISSUED', 'SKIPPED')),
  document_ref text,
  issued_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES dunning_run (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS dunning_letter_run ON dunning_letter (tenant_id, run_id);
ALTER TABLE dunning_letter ENABLE ROW LEVEL SECURITY;
ALTER TABLE dunning_letter FORCE ROW LEVEL SECURITY;
CREATE POLICY dunning_letter_tenant_access ON dunning_letter AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY dunning_letter_legal_entity_scope ON dunning_letter AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS interest_note (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  open_item_id uuid NOT NULL,
  interest_item_id uuid NOT NULL,
  period text NOT NULL CHECK (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  calculation_date date NOT NULL,
  days integer NOT NULL CHECK (days >= 0),
  annual_rate numeric(9,6) NOT NULL CHECK (annual_rate BETWEEN 0 AND 1),
  basis text NOT NULL CHECK (basis IN ('ACT_365', 'ACT_360')),
  principal numeric(20,6) NOT NULL CHECK (principal > 0),
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, open_item_id, period),
  FOREIGN KEY (tenant_id, open_item_id) REFERENCES open_item (tenant_id, id),
  FOREIGN KEY (tenant_id, interest_item_id) REFERENCES open_item (tenant_id, id)
);
ALTER TABLE interest_note ENABLE ROW LEVEL SECURITY;
ALTER TABLE interest_note FORCE ROW LEVEL SECURITY;
CREATE POLICY interest_note_tenant_access ON interest_note AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY interest_note_legal_entity_scope ON interest_note AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
