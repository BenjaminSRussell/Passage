-- Pricing context, wave 26 slice 4: trade promotion funds with usage tracking (FR-26.19, BR-26.15)
-- and promotion plans with baseline, lift and actuals (FR-26.20). Additive only.

CREATE TABLE IF NOT EXISTS trade_fund (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  fund_type text NOT NULL CHECK (length(fund_type) BETWEEN 1 AND 32),
  currency char(3) NOT NULL,
  customer_scope text NOT NULL DEFAULT '' CHECK (length(customer_scope) <= 64),
  owner text NOT NULL CHECK (length(owner) BETWEEN 1 AND 64),
  budget_per_period numeric(20,6) NOT NULL CHECK (budget_per_period >= 0),
  allow_overspend boolean NOT NULL DEFAULT false,
  closed boolean NOT NULL DEFAULT false,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);

-- Usage attributed to a fund in a fiscal period (YYYY-MM). Usage rows are never updated:
-- the fund row lock serializes attributions and closing a fund stops new rows (PRC-0011).
-- Idempotency-Key replay for attributions goes through idempotency_keys (0001).
CREATE TABLE IF NOT EXISTS trade_fund_usage (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  fund_id uuid NOT NULL,
  period text NOT NULL CHECK (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  source text NOT NULL CHECK (source IN ('CAMPAIGN_ACCRUAL', 'REBATE_ACCRUAL', 'CLAIM', 'DEDUCTION')),
  reference text NOT NULL CHECK (length(reference) BETWEEN 1 AND 128),
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, fund_id) REFERENCES trade_fund (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS trade_fund_usage_period_ix ON trade_fund_usage (tenant_id, fund_id, period);

-- Promotion plan on one product. baseline_weekly_units holds the 13 prior weeks, oldest first.
CREATE TABLE IF NOT EXISTS promotion_plan (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  event_category text NOT NULL DEFAULT '' CHECK (length(event_category) <= 64),
  event_type text NOT NULL DEFAULT '' CHECK (length(event_type) <= 64),
  customer_scope text NOT NULL DEFAULT '' CHECK (length(customer_scope) <= 64),
  fund_id uuid,
  product_id text NOT NULL CHECK (length(product_id) BETWEEN 1 AND 64),
  currency char(3) NOT NULL,
  start_date date NOT NULL,
  end_date date NOT NULL CHECK (end_date >= start_date),
  baseline_weekly_units numeric(20,6)[] NOT NULL CHECK (cardinality(baseline_weekly_units) = 13),
  lift_pct numeric(9,4) NOT NULL CHECK (lift_pct >= -100),
  planned_spend numeric(20,6) NOT NULL CHECK (planned_spend >= 0),
  actual_units numeric(20,6) CHECK (actual_units >= 0),
  actual_weeks integer CHECK (actual_weeks >= 1),
  actual_spend numeric(20,6) CHECK (actual_spend >= 0),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, fund_id) REFERENCES trade_fund (tenant_id, id)
);

ALTER TABLE trade_fund ENABLE ROW LEVEL SECURITY;
ALTER TABLE trade_fund FORCE ROW LEVEL SECURITY;
CREATE POLICY trade_fund_tenant_access ON trade_fund AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY trade_fund_legal_entity_scope ON trade_fund AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE trade_fund_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE trade_fund_usage FORCE ROW LEVEL SECURITY;
CREATE POLICY trade_fund_usage_tenant_access ON trade_fund_usage AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY trade_fund_usage_legal_entity_scope ON trade_fund_usage AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE promotion_plan ENABLE ROW LEVEL SECURITY;
ALTER TABLE promotion_plan FORCE ROW LEVEL SECURITY;
CREATE POLICY promotion_plan_tenant_access ON promotion_plan AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY promotion_plan_legal_entity_scope ON promotion_plan AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
