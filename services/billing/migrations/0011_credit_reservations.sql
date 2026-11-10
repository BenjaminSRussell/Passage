-- Credit reservations (#513): a credit that does not come from a billing credit
-- note (an RMA credit in receivables) takes its share of the original invoice
-- here first, under the invoice's row lock, so credit notes plus reservations
-- never exceed what was invoiced. Amounts are positive magnitudes.

CREATE TABLE IF NOT EXISTS credit_reservation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  reference text NOT NULL CHECK (length(reference) BETWEEN 1 AND 255),
  status text NOT NULL DEFAULT 'RESERVED' CHECK (status IN ('RESERVED', 'RELEASED')),
  net_amount numeric(20,6) NOT NULL CHECK (net_amount >= 0),
  charge_amount numeric(20,6) NOT NULL CHECK (charge_amount >= 0),
  tax_amount numeric(20,6) NOT NULL CHECK (tax_amount >= 0),
  gross_amount numeric(20,6) NOT NULL CHECK (gross_amount > 0),
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  released_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoice (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS credit_reservation_invoice_idx ON credit_reservation (tenant_id, invoice_id, status);
ALTER TABLE credit_reservation ENABLE ROW LEVEL SECURITY;
ALTER TABLE credit_reservation FORCE ROW LEVEL SECURITY;
CREATE POLICY credit_reservation_tenant_access ON credit_reservation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY credit_reservation_legal_entity_scope ON credit_reservation AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS credit_reservation_line (
  tenant_id uuid NOT NULL,
  reservation_id uuid NOT NULL,
  original_line_no integer NOT NULL CHECK (original_line_no > 0),
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  line_net numeric(20,6) NOT NULL CHECK (line_net >= 0),
  tax_amount numeric(20,6) NOT NULL CHECK (tax_amount >= 0),
  PRIMARY KEY (tenant_id, reservation_id, original_line_no),
  FOREIGN KEY (tenant_id, reservation_id) REFERENCES credit_reservation (tenant_id, id)
);
ALTER TABLE credit_reservation_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE credit_reservation_line FORCE ROW LEVEL SECURITY;
CREATE POLICY credit_reservation_line_tenant_access ON credit_reservation_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY credit_reservation_line_legal_entity_scope ON credit_reservation_line AS RESTRICTIVE
  USING (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM credit_reservation AS scope_parent
      WHERE scope_parent.tenant_id = credit_reservation_line.tenant_id
        AND scope_parent.id = credit_reservation_line.reservation_id
        AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))))
  WITH CHECK (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM credit_reservation AS scope_parent
      WHERE scope_parent.tenant_id = credit_reservation_line.tenant_id
        AND scope_parent.id = credit_reservation_line.reservation_id
        AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON credit_reservation TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON credit_reservation_line TO passage_app';
  END IF;
END
$$;
