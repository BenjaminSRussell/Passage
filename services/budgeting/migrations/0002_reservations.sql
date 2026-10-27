-- Budget reservation documents, earmarked funds (FR-38.20, BR-38.14).
--
-- A FundsReservation, Precommitment or FundsBlocking holds its open amount in
-- budget_control_balance.reservations, so available funds drop when the document
-- is created. Consumption by a follow-on document moves the consumed amount from
-- reservations to encumbrances in one transaction: available funds are unchanged
-- by the conversion (AC-38.12). Close and expiry release the residual.
-- A RevenueForecast holds nothing (revenue-increases-budget is not built yet).
ALTER TABLE budget_control_balance ADD COLUMN IF NOT EXISTS reservations numeric(19,4) NOT NULL DEFAULT 0;

-- Reservations are a document type under control (FR-38.3): their creation check is recorded.
ALTER TABLE budget_check_result DROP CONSTRAINT IF EXISTS budget_check_result_document_type_check;
ALTER TABLE budget_check_result ADD CONSTRAINT budget_check_result_document_type_check
  CHECK (document_type IN ('Requisition', 'PurchaseOrder', 'Invoice', 'Journal', 'Reservation'));

CREATE TABLE IF NOT EXISTS budget_reservation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  rule_version_id uuid NOT NULL,
  reservation_type text NOT NULL CHECK (reservation_type IN ('FundsReservation', 'Precommitment', 'FundsBlocking', 'RevenueForecast')),
  number text NOT NULL,
  description text NOT NULL DEFAULT '',
  expiry_date date,
  status text NOT NULL DEFAULT 'Open' CHECK (status IN ('Open', 'PartiallyConsumed', 'Consumed', 'Closed', 'Expired')),
  total numeric(19,4) NOT NULL CHECK (total > 0),
  consumed numeric(19,4) NOT NULL DEFAULT 0,
  released numeric(19,4) NOT NULL DEFAULT 0,
  check_id uuid,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  closed_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, number),
  CHECK (consumed >= 0 AND released >= 0 AND consumed + released <= total),
  CHECK (reservation_type <> 'FundsBlocking' OR (consumed = 0 AND expiry_date IS NULL)),
  FOREIGN KEY (tenant_id, rule_version_id) REFERENCES budget_control_rule (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS budget_reservation_rule ON budget_reservation (tenant_id, rule_version_id, created_at);
CREATE INDEX IF NOT EXISTS budget_reservation_expiry ON budget_reservation (tenant_id, expiry_date)
  WHERE status IN ('Open', 'PartiallyConsumed');
ALTER TABLE budget_reservation ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_reservation FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_reservation_tenant_access ON budget_reservation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_reservation_legal_entity_scope ON budget_reservation AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- One row per reservation line: the control combination it holds funds on.
CREATE TABLE IF NOT EXISTS budget_reservation_line (
  tenant_id uuid NOT NULL,
  reservation_id uuid NOT NULL,
  line_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  balance_id uuid NOT NULL,
  amount numeric(19,4) NOT NULL CHECK (amount > 0),
  consumed numeric(19,4) NOT NULL DEFAULT 0,
  released numeric(19,4) NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, reservation_id, line_id),
  UNIQUE (tenant_id, line_id),
  CHECK (consumed >= 0 AND released >= 0 AND consumed + released <= amount),
  FOREIGN KEY (tenant_id, reservation_id) REFERENCES budget_reservation (tenant_id, id),
  FOREIGN KEY (tenant_id, balance_id) REFERENCES budget_control_balance (tenant_id, id)
);
ALTER TABLE budget_reservation_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_reservation_line FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_reservation_line_tenant_access ON budget_reservation_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_reservation_line_legal_entity_scope ON budget_reservation_line AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Consumption history: each follow-on document line that drew on a reservation line.
CREATE TABLE IF NOT EXISTS budget_reservation_consumption (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  reservation_id uuid NOT NULL,
  line_id uuid NOT NULL,
  encumbrance_id uuid NOT NULL,
  document_type text NOT NULL CHECK (document_type IN ('Requisition', 'PurchaseOrder', 'Invoice', 'Journal')),
  document_id uuid NOT NULL,
  document_line_id uuid NOT NULL,
  amount numeric(19,4) NOT NULL CHECK (amount > 0),
  consumed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, reservation_id, line_id) REFERENCES budget_reservation_line (tenant_id, reservation_id, line_id),
  FOREIGN KEY (tenant_id, encumbrance_id) REFERENCES encumbrance (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS budget_reservation_consumption_reservation ON budget_reservation_consumption (tenant_id, reservation_id, consumed_at);
ALTER TABLE budget_reservation_consumption ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_reservation_consumption FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_reservation_consumption_tenant_access ON budget_reservation_consumption AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_reservation_consumption_legal_entity_scope ON budget_reservation_consumption AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Per-type number sequences per legal entity (reservation.documentTypes):
-- FundsReservation FR-, Precommitment PC-, FundsBlocking FB-, RevenueForecast RF-.
-- A counter row per (tenant, legal entity, type) keeps numbering tenant-private and gap-free.
CREATE TABLE IF NOT EXISTS budget_reservation_number (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  reservation_type text NOT NULL,
  last_value bigint NOT NULL,
  PRIMARY KEY (tenant_id, legal_entity_id, reservation_type)
);
ALTER TABLE budget_reservation_number ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_reservation_number FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_reservation_number_tenant_access ON budget_reservation_number AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_reservation_number_legal_entity_scope ON budget_reservation_number AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
