-- Pricing context, wave 26 slice 13: broker contracts (FR-26.16) and royalty agreements with
-- minimum royalty (FR-26.17). Both accrue per posted invoice line, per period, and are claimed
-- per period through w19 AP. Additive only.

-- A broker contract pays a broker vendor a percentage of net or an amount per unit on the
-- invoices of its customer and product scope. An empty scope key means all customers or products.
CREATE TABLE IF NOT EXISTS broker_contract (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  broker_vendor_id uuid NOT NULL,
  customer_id uuid,
  customer_group_id uuid,
  product_id text NOT NULL DEFAULT '' CHECK (length(product_id) <= 64),
  product_group_id text NOT NULL DEFAULT '' CHECK (length(product_group_id) <= 64),
  rate_kind text NOT NULL CHECK (rate_kind IN ('PERCENT_NET', 'PER_UNIT')),
  rate numeric(19,6) NOT NULL CHECK (rate > 0),
  currency char(3) NOT NULL,
  valid_from date NOT NULL,
  valid_to date NOT NULL,
  payable_account text NOT NULL DEFAULT '2175',
  expense_account text NOT NULL DEFAULT '6210',
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'CLOSED')),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (valid_from <= valid_to),
  CHECK (customer_id IS NULL OR customer_group_id IS NULL),
  CHECK (product_id = '' OR product_group_id = ''),
  CHECK (rate_kind <> 'PERCENT_NET' OR rate <= 100)
);
CREATE INDEX IF NOT EXISTS broker_contract_vendor_ix ON broker_contract (tenant_id, broker_vendor_id);

-- A royalty agreement pays a licensor vendor tiered rates on one product or product group,
-- on net sales or on quantity, with a minimum royalty per period.
CREATE TABLE IF NOT EXISTS royalty_agreement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  licensor_vendor_id uuid NOT NULL,
  product_id text NOT NULL DEFAULT '' CHECK (length(product_id) <= 64),
  product_group_id text NOT NULL DEFAULT '' CHECK (length(product_group_id) <= 64),
  basis text NOT NULL CHECK (basis IN ('NET_SALES', 'QUANTITY')),
  period text NOT NULL CHECK (period IN ('MONTH', 'QUARTER', 'YEAR')),
  tiers jsonb NOT NULL,
  minimum_royalty numeric(19,6) NOT NULL DEFAULT 0 CHECK (minimum_royalty >= 0),
  currency char(3) NOT NULL,
  valid_from date NOT NULL,
  valid_to date NOT NULL,
  payable_account text NOT NULL DEFAULT '2176',
  expense_account text NOT NULL DEFAULT '6220',
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'CLOSED')),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (valid_from <= valid_to),
  CHECK ((product_id = '') <> (product_group_id = ''))
);
CREATE INDEX IF NOT EXISTS royalty_agreement_vendor_ix ON royalty_agreement (tenant_id, licensor_vendor_id);

-- The cumulative basis and accrual of one contract in one period; the row lock serializes the
-- accruals, the shortfall posting and the claims of the period. contract_kind is BROKER or
-- ROYALTY and contract_id names a broker_contract or a royalty_agreement.
CREATE TABLE IF NOT EXISTS contract_period (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  contract_kind text NOT NULL CHECK (contract_kind IN ('BROKER', 'ROYALTY')),
  contract_id uuid NOT NULL,
  period_key text NOT NULL,
  basis_value numeric(20,6) NOT NULL DEFAULT 0,
  basis_qty numeric(20,6) NOT NULL DEFAULT 0,
  accrued_total numeric(20,6) NOT NULL DEFAULT 0,
  shortfall numeric(20,6) NOT NULL DEFAULT 0 CHECK (shortfall >= 0),
  closed boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, contract_kind, contract_id, period_key)
);

-- One row per invoice line (ACCRUAL), per credit note line (CREDIT, negative) and per period
-- close that posted a minimum royalty shortfall (SHORTFALL). line_key is the invoice line id,
-- or MIN:<period> for the shortfall, so a redelivery cannot post twice.
CREATE TABLE IF NOT EXISTS contract_accrual (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  contract_kind text NOT NULL CHECK (contract_kind IN ('BROKER', 'ROYALTY')),
  contract_id uuid NOT NULL,
  period_key text NOT NULL,
  line_key text NOT NULL CHECK (length(line_key) BETWEEN 1 AND 128),
  kind text NOT NULL CHECK (kind IN ('ACCRUAL', 'CREDIT', 'SHORTFALL')),
  invoice_date date NOT NULL,
  basis_value numeric(20,6) NOT NULL DEFAULT 0,
  basis_qty numeric(20,6) NOT NULL DEFAULT 0,
  amount numeric(20,6) NOT NULL,
  currency char(3) NOT NULL,
  reverses_line_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, contract_kind, contract_id, line_key),
  CHECK ((kind = 'CREDIT') = (reverses_line_key IS NOT NULL)),
  FOREIGN KEY (tenant_id, contract_kind, contract_id, period_key)
    REFERENCES contract_period (tenant_id, contract_kind, contract_id, period_key)
);
CREATE INDEX IF NOT EXISTS contract_accrual_period_ix ON contract_accrual (tenant_id, contract_kind, contract_id, period_key);

-- A period claim: the period's accruals (and shortfall) less the earlier claims of the period.
-- Numbers BC-000001 / RY-000001 run per legal entity and kind; they name the claim statement.
CREATE TABLE IF NOT EXISTS contract_claim (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  contract_kind text NOT NULL CHECK (contract_kind IN ('BROKER', 'ROYALTY')),
  contract_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  period_key text NOT NULL,
  number text NOT NULL CHECK (number ~ '^(BC|RY)-[0-9]{6,}$'),
  accrued_total numeric(20,6) NOT NULL,
  prior_claimed numeric(20,6) NOT NULL,
  amount numeric(20,6) NOT NULL CHECK (amount <> 0),
  currency char(3) NOT NULL,
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'APPROVED')),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_by text NOT NULL DEFAULT '',
  approved_by text,
  approved_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, number),
  CHECK ((status = 'APPROVED') = (approved_at IS NOT NULL)),
  CHECK ((contract_kind = 'BROKER') = (number LIKE 'BC-%')),
  FOREIGN KEY (tenant_id, contract_kind, contract_id, period_key)
    REFERENCES contract_period (tenant_id, contract_kind, contract_id, period_key)
);
CREATE INDEX IF NOT EXISTS contract_claim_period_ix ON contract_claim (tenant_id, contract_kind, contract_id, period_key);

ALTER TABLE broker_contract ENABLE ROW LEVEL SECURITY;
ALTER TABLE broker_contract FORCE ROW LEVEL SECURITY;
CREATE POLICY broker_contract_tenant_access ON broker_contract AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY broker_contract_legal_entity_scope ON broker_contract AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE royalty_agreement ENABLE ROW LEVEL SECURITY;
ALTER TABLE royalty_agreement FORCE ROW LEVEL SECURITY;
CREATE POLICY royalty_agreement_tenant_access ON royalty_agreement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY royalty_agreement_legal_entity_scope ON royalty_agreement AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE contract_period ENABLE ROW LEVEL SECURITY;
ALTER TABLE contract_period FORCE ROW LEVEL SECURITY;
CREATE POLICY contract_period_tenant_access ON contract_period AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY contract_period_legal_entity_scope ON contract_period AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE contract_accrual ENABLE ROW LEVEL SECURITY;
ALTER TABLE contract_accrual FORCE ROW LEVEL SECURITY;
CREATE POLICY contract_accrual_tenant_access ON contract_accrual AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY contract_accrual_legal_entity_scope ON contract_accrual AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE contract_claim ENABLE ROW LEVEL SECURITY;
ALTER TABLE contract_claim FORCE ROW LEVEL SECURITY;
CREATE POLICY contract_claim_tenant_access ON contract_claim AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY contract_claim_legal_entity_scope ON contract_claim AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Application role privileges. contract_accrual is append-only. The store updates (closes,
-- approves) or row-locks the other four tables, so they get UPDATE; nothing is deleted.
-- Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON contract_accrual TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON broker_contract, royalty_agreement, contract_period, contract_claim TO passage_app';
  END IF;
END
$$;
