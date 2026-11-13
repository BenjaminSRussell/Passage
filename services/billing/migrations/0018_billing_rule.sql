-- #35 (35-5): the billing rule of a customer in a legal entity. SELF_BILLED (a self-billing agreement, EU VAT Directive art. 224)
-- means the customer issues the invoice for our deliveries, so billing proposes no invoice of its own for that customer's shipments
-- while the agreement is valid; the customer's self-billed document comes in through the EDI engine.
CREATE TABLE IF NOT EXISTS billing_rule (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  rule text NOT NULL CHECK (rule IN ('STANDARD', 'SELF_BILLED')),
  agreement_ref text NOT NULL DEFAULT '' CHECK (char_length(agreement_ref) <= 100),
  valid_from date,
  valid_to date,
  version integer NOT NULL DEFAULT 1,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, customer_id),
  CHECK (rule <> 'SELF_BILLED' OR (agreement_ref <> '' AND valid_from IS NOT NULL)),
  CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to >= valid_from)
);
ALTER TABLE billing_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY billing_rule_tenant_access ON billing_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY billing_rule_legal_entity_scope ON billing_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
  WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON billing_rule TO passage_app';
  END IF;
END
$$;
