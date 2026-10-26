-- Main accounts. A chart is a code namespace in this slice (ChartOfAccounts entity deferred).
CREATE TABLE IF NOT EXISTS main_account (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  chart_code text NOT NULL CHECK (chart_code ~ '^[A-Z0-9]{1,10}$'),
  account_number text NOT NULL CHECK (account_number ~ '^[A-Za-z0-9._-]{1,20}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  account_type text NOT NULL CHECK (account_type IN ('asset', 'liability', 'equity', 'revenue', 'expense',
    'profit_and_loss_total', 'balance_sheet_total', 'reporting')),
  is_postable boolean NOT NULL,
  suspended_from date,
  reconciliation_account_type text NOT NULL DEFAULT 'none'
    CHECK (reconciliation_account_type IN ('none', 'customer', 'vendor', 'bank', 'inventory', 'fixed_asset')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, chart_code, account_number),
  CHECK (is_postable OR account_type IN ('profit_and_loss_total', 'balance_sheet_total', 'reporting'))
);
ALTER TABLE main_account ENABLE ROW LEVEL SECURITY;
ALTER TABLE main_account FORCE ROW LEVEL SECURITY;
CREATE POLICY main_account_tenant_access ON main_account AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Ledgers: one accounting currency, up to 20 dimension attributes (FR-11.2).
CREATE TABLE IF NOT EXISTS ledger (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9]{1,10}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  chart_code text NOT NULL CHECK (chart_code ~ '^[A-Z0-9]{1,10}$'),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  dimension_attributes text[] NOT NULL DEFAULT '{}' CHECK (cardinality(dimension_attributes) <= 20),
  first_open_date date,
  future_posting_days int NOT NULL DEFAULT 366 CHECK (future_posting_days BETWEEN 0 AND 730),
  next_sequence bigint NOT NULL DEFAULT 1 CHECK (next_sequence >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger FORCE ROW LEVEL SECURITY;
CREATE POLICY ledger_tenant_access ON ledger AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ledger_legal_entity_scope ON ledger AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
