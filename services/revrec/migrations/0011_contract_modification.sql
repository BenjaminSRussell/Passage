-- #29: contract modifications that re-allocate the existing obligations (ASC 606-10-25-10 to 25-13). POST /v1/revrec/contracts/{id}:modify
-- re-allocates the transaction price after a price change or an SSP revision, either prospectively (the consideration still to
-- recognize is spread over the performance left) or with a cumulative catch-up (revenue to date is re-stated at the effective date).
-- One contract_modification row records each; allocation_delta holds the change of every obligation's allocated amount, dated at the
-- effective date, so the deferred revenue roll-forward adds it in the period of the modification and not in the contract's.
CREATE TABLE IF NOT EXISTS contract_modification (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  contract_id uuid NOT NULL,
  modification_type text NOT NULL CHECK (modification_type IN ('PROSPECTIVE', 'CATCH_UP')),
  effective_date date NOT NULL,
  price_before numeric(20,6) NOT NULL,
  price_after numeric(20,6) NOT NULL CHECK (price_after > 0),
  catch_up_amount numeric(20,6) NOT NULL DEFAULT 0,
  reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, contract_id) REFERENCES revenue_contract (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS contract_modification_contract_idx ON contract_modification (tenant_id, contract_id, effective_date);
ALTER TABLE contract_modification ENABLE ROW LEVEL SECURITY;
ALTER TABLE contract_modification FORCE ROW LEVEL SECURITY;
CREATE POLICY contract_modification_tenant_access ON contract_modification AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY contract_modification_legal_entity_scope ON contract_modification AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
  WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS allocation_delta (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  modification_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  contract_id uuid NOT NULL,
  obligation_id uuid NOT NULL,
  effective_date date NOT NULL,
  allocated_before numeric(20,6) NOT NULL,
  allocated_after numeric(20,6) NOT NULL,
  -- allocated_after less allocated_before; negative when the obligation's share falls.
  delta numeric(20,6) NOT NULL,
  ssp_before numeric(20,6) NOT NULL,
  ssp_after numeric(20,6) NOT NULL,
  catch_up numeric(20,6) NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, modification_id) REFERENCES contract_modification (tenant_id, id),
  FOREIGN KEY (tenant_id, obligation_id) REFERENCES performance_obligation (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS allocation_delta_obligation_idx ON allocation_delta (tenant_id, obligation_id, effective_date);
CREATE INDEX IF NOT EXISTS allocation_delta_modification_idx ON allocation_delta (tenant_id, modification_id);
ALTER TABLE allocation_delta ENABLE ROW LEVEL SECURITY;
ALTER TABLE allocation_delta FORCE ROW LEVEL SECURITY;
CREATE POLICY allocation_delta_tenant_access ON allocation_delta AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY allocation_delta_legal_entity_scope ON allocation_delta AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
  WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON contract_modification, allocation_delta TO passage_app';
  END IF;
END
$$;
