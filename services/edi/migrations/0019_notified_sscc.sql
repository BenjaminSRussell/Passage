-- #35 (35-1): the containers each partner was sent an advance ship notice for. A container is notified once per partner and set:
-- the notice that claims it first (its idempotency key) owns it, so a packing slip posted after the warehouse confirmed the shipment
-- (or the other way round) leaves out the containers already notified and registers no self-billing line for them twice.
CREATE TABLE IF NOT EXISTS notified_sscc (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  transaction_set text NOT NULL CHECK (transaction_set IN ('856', 'DESADV')),
  sscc text NOT NULL CHECK (char_length(sscc) BETWEEN 1 AND 35),
  legal_entity_id uuid NOT NULL,
  notice_key text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, partner_id, transaction_set, sscc)
);
CREATE INDEX IF NOT EXISTS notified_sscc_key ON notified_sscc (tenant_id, notice_key);
ALTER TABLE notified_sscc ENABLE ROW LEVEL SECURITY;
ALTER TABLE notified_sscc FORCE ROW LEVEL SECURITY;
CREATE POLICY notified_sscc_tenant_access ON notified_sscc AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY notified_sscc_legal_entity_scope ON notified_sscc AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
  WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON notified_sscc TO passage_app';
  END IF;
END
$$;
