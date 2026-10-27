CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int NOT NULL,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY idempotency_keys_tenant_access ON idempotency_keys AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Control rule versions (FR-38.3). Each row is one immutable version; a new
-- version of the same code takes the next version number.
CREATE TABLE IF NOT EXISTS budget_control_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{2,40}$'),
  version integer NOT NULL CHECK (version >= 1),
  warn_pct numeric(9,6) NOT NULL CHECK (warn_pct >= 0 AND warn_pct <= 100),
  block_pct numeric(9,6) NOT NULL CHECK (block_pct >= warn_pct AND block_pct <= 100),
  currency char(3) NOT NULL,
  effective_from date NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, code, version)
);
ALTER TABLE budget_control_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_control_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_control_rule_tenant_access ON budget_control_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_control_rule_legal_entity_scope ON budget_control_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Per-combination balance cache (FR-38.4). Rebuilt from events in later slices;
-- here it is written by register postings and checks under FOR UPDATE.
CREATE TABLE IF NOT EXISTS budget_control_balance (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  rule_version_id uuid NOT NULL,
  control_dimension_key text NOT NULL,
  time_span_key text NOT NULL,
  budget_total numeric(19,4) NOT NULL DEFAULT 0,
  actuals numeric(19,4) NOT NULL DEFAULT 0,
  encumbrances numeric(19,4) NOT NULL DEFAULT 0,
  pre_encumbrances numeric(19,4) NOT NULL DEFAULT 0,
  version bigint NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, rule_version_id, control_dimension_key, time_span_key),
  FOREIGN KEY (tenant_id, rule_version_id) REFERENCES budget_control_rule (tenant_id, id)
);
ALTER TABLE budget_control_balance ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_control_balance FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_control_balance_tenant_access ON budget_control_balance AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_control_balance_legal_entity_scope ON budget_control_balance AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Budget register entries (FR-38.1). Posted entries are immutable (BR-38.2).
CREATE TABLE IF NOT EXISTS budget_register_entry (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  rule_version_id uuid NOT NULL,
  entry_type text NOT NULL CHECK (entry_type IN ('Original', 'Revision', 'Transfer', 'CarryForward')),
  status text NOT NULL DEFAULT 'Posted' CHECK (status = 'Posted'),
  lines jsonb NOT NULL,
  total numeric(19,4) NOT NULL,
  posted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, rule_version_id) REFERENCES budget_control_rule (tenant_id, id)
);
ALTER TABLE budget_register_entry ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_register_entry FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_register_entry_tenant_access ON budget_register_entry AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_register_entry_legal_entity_scope ON budget_register_entry AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Encumbrances (FR-38.5): one per source line; reserved grows on checks and
-- relieved grows on invoice relief. lines holds the Reserve/Relieve history.
CREATE TABLE IF NOT EXISTS encumbrance (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  balance_id uuid NOT NULL,
  source_type text NOT NULL CHECK (source_type IN ('Requisition', 'PurchaseOrder', 'Invoice', 'Journal')),
  source_line_id uuid NOT NULL,
  source_document_id uuid NOT NULL,
  reserved numeric(19,4) NOT NULL DEFAULT 0,
  relieved numeric(19,4) NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'Open' CHECK (status IN ('Open', 'PartiallyRelieved', 'Relieved', 'Released')),
  lines jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, source_line_id),
  CHECK (relieved >= 0 AND relieved <= reserved),
  FOREIGN KEY (tenant_id, balance_id) REFERENCES budget_control_balance (tenant_id, id)
);
ALTER TABLE encumbrance ENABLE ROW LEVEL SECURITY;
ALTER TABLE encumbrance FORCE ROW LEVEL SECURITY;
CREATE POLICY encumbrance_tenant_access ON encumbrance AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY encumbrance_legal_entity_scope ON encumbrance AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Budget check results (FR-38.4 step 8): every check is recorded, pass, warn
-- or fail, with its per-line outcome and step trace. Retained 7 years.
CREATE TABLE IF NOT EXISTS budget_check_result (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  rule_version_id uuid NOT NULL,
  document_id uuid NOT NULL,
  document_type text NOT NULL CHECK (document_type IN ('Requisition', 'PurchaseOrder', 'Invoice', 'Journal')),
  result text NOT NULL CHECK (result IN ('pass', 'warn', 'fail')),
  overridden boolean NOT NULL DEFAULT false,
  lines jsonb NOT NULL,
  checked_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, rule_version_id) REFERENCES budget_control_rule (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS budget_check_result_document ON budget_check_result (tenant_id, document_id, checked_at);
ALTER TABLE budget_check_result ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_check_result FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_check_result_tenant_access ON budget_check_result AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_check_result_legal_entity_scope ON budget_check_result AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
