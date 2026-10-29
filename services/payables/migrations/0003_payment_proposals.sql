ALTER TABLE ap_open_item ADD COLUMN IF NOT EXISTS payment_hold boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS ap_vendor_block (
  tenant_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  reason text NOT NULL CHECK (reason <> ''),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, vendor_id)
);
ALTER TABLE ap_vendor_block ENABLE ROW LEVEL SECURITY;
ALTER TABLE ap_vendor_block FORCE ROW LEVEL SECURITY;
CREATE POLICY ap_vendor_block_tenant_access ON ap_vendor_block AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS ap_payment_proposal (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  run_date date NOT NULL,
  run_id text NOT NULL CHECK (run_id <> '' AND length(run_id) <= 64),
  due_by date NOT NULL,
  rate_date date NOT NULL,
  legal_entity_currency char(3) NOT NULL CHECK (legal_entity_currency ~ '^[A-Z]{3}$'),
  payment_method text NOT NULL,
  request_hash text NOT NULL,
  status text NOT NULL CHECK (status IN ('DRAFT', 'APPROVED')),
  created_by text NOT NULL CHECK (created_by <> ''),
  created_at timestamptz NOT NULL DEFAULT now(),
  approved_by text,
  approved_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, run_date, run_id),
  CHECK ((status = 'APPROVED') = (approved_by IS NOT NULL AND approved_at IS NOT NULL)),
  CHECK (approved_by IS NULL OR approved_by <> created_by)
);
ALTER TABLE ap_payment_proposal ENABLE ROW LEVEL SECURITY;
ALTER TABLE ap_payment_proposal FORCE ROW LEVEL SECURITY;
CREATE POLICY ap_payment_proposal_tenant_access ON ap_payment_proposal AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ap_payment_proposal_legal_entity_scope ON ap_payment_proposal AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS ap_payment_proposal_group (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  proposal_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  payment_method text NOT NULL,
  gross_amount numeric(20,6) NOT NULL,
  discount_amount numeric(20,6) NOT NULL,
  net_amount numeric(20,6) NOT NULL,
  valued_amount numeric(20,6) NOT NULL,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, proposal_id) REFERENCES ap_payment_proposal (tenant_id, id),
  UNIQUE (tenant_id, proposal_id, vendor_id, currency, payment_method)
);
ALTER TABLE ap_payment_proposal_group ENABLE ROW LEVEL SECURITY;
ALTER TABLE ap_payment_proposal_group FORCE ROW LEVEL SECURITY;
CREATE POLICY ap_payment_proposal_group_tenant_access ON ap_payment_proposal_group AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ap_payment_proposal_group_legal_entity_scope ON ap_payment_proposal_group AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS ap_payment_proposal_line (
  tenant_id uuid NOT NULL,
  open_item_id uuid NOT NULL,
  proposal_id uuid NOT NULL,
  group_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  open_amount numeric(20,6) NOT NULL,
  discount_amount numeric(20,6) NOT NULL,
  pay_amount numeric(20,6) NOT NULL CHECK (pay_amount > 0),
  rate numeric NOT NULL CHECK (rate > 0),
  valued_amount numeric(20,6) NOT NULL,
  PRIMARY KEY (tenant_id, open_item_id),
  FOREIGN KEY (tenant_id, open_item_id) REFERENCES ap_open_item (tenant_id, id),
  FOREIGN KEY (tenant_id, proposal_id) REFERENCES ap_payment_proposal (tenant_id, id),
  FOREIGN KEY (tenant_id, group_id) REFERENCES ap_payment_proposal_group (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS ap_payment_proposal_line_group ON ap_payment_proposal_line (tenant_id, group_id);
ALTER TABLE ap_payment_proposal_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE ap_payment_proposal_line FORCE ROW LEVEL SECURITY;
CREATE POLICY ap_payment_proposal_line_tenant_access ON ap_payment_proposal_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ap_payment_proposal_line_legal_entity_scope ON ap_payment_proposal_line AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
