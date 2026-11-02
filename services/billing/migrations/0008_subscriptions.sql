-- Subscriptions (FR-29.10): the plan terms a customer is billed on and the billing
-- periods already invoiced. A subscription is billed by period, and a period is
-- invoiced once; its invoice is an ordinary SUBSCRIPTION invoice proposal.

CREATE TABLE IF NOT EXISTS subscription (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  invoice_account_id uuid NOT NULL,
  currency char(3) NOT NULL,
  period_months integer NOT NULL CHECK (period_months IN (1, 3, 6, 12)),
  start_date date NOT NULL,
  cancel_effective date,
  status text NOT NULL CHECK (status IN ('ACTIVE', 'CANCELLED')),
  revenue_account text NOT NULL,
  tax_code text NOT NULL,
  payment_term_days integer NOT NULL CHECK (payment_term_days BETWEEN 0 AND 365),
  version integer NOT NULL DEFAULT 1,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (cancel_effective IS NULL OR cancel_effective >= start_date),
  CHECK ((status = 'CANCELLED') = (cancel_effective IS NOT NULL))
);
ALTER TABLE subscription ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscription FORCE ROW LEVEL SECURITY;
CREATE POLICY subscription_tenant_access ON subscription AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY subscription_legal_entity_scope ON subscription AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS subscription_term (
  tenant_id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  seq integer NOT NULL CHECK (seq > 0),
  plan_code text NOT NULL,
  price numeric(20,6) NOT NULL CHECK (price >= 0),
  effective_from date NOT NULL,
  PRIMARY KEY (tenant_id, subscription_id, seq),
  UNIQUE (tenant_id, subscription_id, effective_from),
  FOREIGN KEY (tenant_id, subscription_id) REFERENCES subscription (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS subscription_period (
  tenant_id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  period_from date NOT NULL,
  period_to date NOT NULL,
  invoice_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, subscription_id, period_from),
  CHECK (period_to >= period_from),
  FOREIGN KEY (tenant_id, subscription_id) REFERENCES subscription (tenant_id, id),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoice (tenant_id, id)
);

DO $$
DECLARE
  child_table text;
BEGIN
  FOREACH child_table IN ARRAY ARRAY['subscription_term', 'subscription_period']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', child_table);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', child_table);
    EXECUTE format(
      'CREATE POLICY %I ON %I AS PERMISSIVE
         USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)
         WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
      child_table || '_tenant_access', child_table);
    EXECUTE format(
      'CREATE POLICY %I ON %I AS RESTRICTIVE USING (
         current_setting(''passage.legal_entity_scopes'', true) = ''*''
         OR EXISTS (
           SELECT 1 FROM subscription AS scope_parent
           WHERE scope_parent.tenant_id = %I.tenant_id
             AND scope_parent.id = %I.subscription_id
             AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))
         )
       ) WITH CHECK (
         current_setting(''passage.legal_entity_scopes'', true) = ''*''
         OR EXISTS (
           SELECT 1 FROM subscription AS scope_parent
           WHERE scope_parent.tenant_id = %I.tenant_id
             AND scope_parent.id = %I.subscription_id
             AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))
         )
       )',
      child_table || '_legal_entity_scope', child_table, child_table, child_table, child_table, child_table);
  END LOOP;
END $$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON subscription TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON subscription_term, subscription_period TO passage_app';
  END IF;
END
$$;
