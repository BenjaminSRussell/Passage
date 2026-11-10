-- Usage lines of a subscription (29-5, FR-29.10): metered quantities recorded against a subscription and invoiced with the
-- billing period they fall in. A usage record is billed once: invoice_id is set in the transaction that stores the period's
-- invoice proposal, so a repeated or concurrent bill never invoices it twice. external_id is the caller's key for the
-- record (a meter reading id), unique per subscription, so a resent reading is a replay.
CREATE TABLE IF NOT EXISTS subscription_usage (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  external_id text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 100),
  meter_code text NOT NULL CHECK (length(meter_code) BETWEEN 1 AND 50),
  usage_date date NOT NULL,
  quantity numeric(20,6) NOT NULL CHECK (quantity > 0),
  unit_price numeric(20,6) NOT NULL CHECK (unit_price >= 0),
  request_hash text NOT NULL,
  invoice_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, subscription_id, external_id),
  FOREIGN KEY (tenant_id, subscription_id) REFERENCES subscription (tenant_id, id),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoice (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS subscription_usage_unbilled ON subscription_usage (tenant_id, subscription_id, usage_date) WHERE invoice_id IS NULL;
ALTER TABLE subscription_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscription_usage FORCE ROW LEVEL SECURITY;
CREATE POLICY subscription_usage_tenant_access ON subscription_usage AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY subscription_usage_legal_entity_scope ON subscription_usage AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON subscription_usage TO passage_app';
  END IF;
END
$$;
