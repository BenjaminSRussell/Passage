-- Wave 37 slice 2: insurance policies with insured-value index series
-- (FR-37.15) and capital appropriation requests with variants (FR-37.17).
-- Expand-only. Every table carries tenant_id first, ENABLE and FORCE row-level
-- security, the tenant policy and (with legal_entity_id) the legal-entity scope.
-- Index series are tenant-wide master data (insurance.indexSeries, 7.37.13).

CREATE TABLE IF NOT EXISTS asset_index_series (
  tenant_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,31}$'),
  country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
  class_code text NOT NULL CHECK (class_code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, code)
);
ALTER TABLE asset_index_series ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_index_series FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_index_series_tenant_access ON asset_index_series AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS asset_index_value (
  tenant_id uuid NOT NULL,
  series_code text NOT NULL,
  year integer NOT NULL CHECK (year BETWEEN 1900 AND 2999),
  value numeric(9,4) NOT NULL CHECK (value > 0),
  PRIMARY KEY (tenant_id, series_code, year),
  FOREIGN KEY (tenant_id, series_code) REFERENCES asset_index_series (tenant_id, code)
);
ALTER TABLE asset_index_value ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_index_value FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_index_value_tenant_access ON asset_index_value AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- base_value is the replacement value at the index base year; indexed_value is
-- base_value × index(index_year_used) ÷ index(index_base_year) for the
-- valuation year, as computed when the policy was recorded (BR-37.10).
-- index_year_used differs from valuation_year when the series had no value for
-- it (AST-0531). sum_insured is the policy's cover; below 90% of indexed_value
-- the asset is under-insured (AST-0532).
CREATE TABLE IF NOT EXISTS asset_insurance_policy (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  asset_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  insurer_party_id uuid NOT NULL,
  policy_number text NOT NULL CHECK (char_length(policy_number) BETWEEN 1 AND 60),
  insurance_type text NOT NULL CHECK (insurance_type IN ('Property', 'MachineryBreakdown', 'Transit', 'Fleet')),
  coverage_from date NOT NULL,
  coverage_to date NOT NULL,
  premium_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (premium_amount >= 0),
  currency char(3) NOT NULL,
  basis text NOT NULL CHECK (basis IN ('Replacement', 'NBV', 'Agreed')),
  base_value numeric(20,6) NOT NULL CHECK (base_value >= 0),
  sum_insured numeric(20,6) NOT NULL CHECK (sum_insured >= 0),
  index_series_code text,
  index_base_year integer CHECK (index_base_year IS NULL OR index_base_year BETWEEN 1900 AND 2999),
  valuation_year integer,
  index_year_used integer,
  indexed_value numeric(20,6),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT asset_insurance_policy_number_unique UNIQUE (tenant_id, asset_id, policy_number),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id),
  FOREIGN KEY (tenant_id, index_series_code) REFERENCES asset_index_series (tenant_id, code),
  CHECK (coverage_to > coverage_from),
  CHECK ((index_series_code IS NULL) = (index_base_year IS NULL)),
  CHECK ((index_series_code IS NULL) = (indexed_value IS NULL))
);
CREATE INDEX IF NOT EXISTS asset_insurance_policy_asset ON asset_insurance_policy (tenant_id, asset_id, coverage_from);
CREATE INDEX IF NOT EXISTS asset_insurance_policy_expiry ON asset_insurance_policy (tenant_id, legal_entity_id, coverage_to);
ALTER TABLE asset_insurance_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_insurance_policy FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_insurance_policy_tenant_access ON asset_insurance_policy AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY asset_insurance_policy_legal_entity_scope ON asset_insurance_policy AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS capex_request (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  number text NOT NULL CHECK (char_length(number) BETWEEN 1 AND 40),
  title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
  requester_id text NOT NULL CHECK (char_length(requester_id) BETWEEN 1 AND 200),
  status text NOT NULL DEFAULT 'Draft' CHECK (status IN (
    'Draft', 'Submitted', 'InApproval', 'Approved', 'Released', 'Closed', 'Rejected', 'Postponed')),
  currency char(3) NOT NULL,
  selected_variant_id uuid,
  approval_level text CHECK (approval_level IN ('PlantManager', 'CFO', 'Board')),
  requested_amount numeric(20,6) CHECK (requested_amount IS NULL OR requested_amount > 0),
  approved_amount numeric(20,6) CHECK (approved_amount IS NULL OR approved_amount >= 0),
  program_position_id uuid,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT capex_request_number_unique UNIQUE (tenant_id, legal_entity_id, number)
);
CREATE INDEX IF NOT EXISTS capex_request_status ON capex_request (tenant_id, status, number);
ALTER TABLE capex_request ENABLE ROW LEVEL SECURITY;
ALTER TABLE capex_request FORCE ROW LEVEL SECURITY;
CREATE POLICY capex_request_tenant_access ON capex_request AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY capex_request_legal_entity_scope ON capex_request AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS capex_request_variant (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  request_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  position integer NOT NULL CHECK (position >= 1),
  description text NOT NULL CHECK (char_length(description) BETWEEN 1 AND 200),
  amounts_by_year jsonb NOT NULL,
  total_amount numeric(20,6) NOT NULL CHECK (total_amount > 0),
  in_service_date date NOT NULL,
  profile_assumption text NOT NULL CHECK (char_length(profile_assumption) BETWEEN 1 AND 60),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT capex_request_variant_position_unique UNIQUE (tenant_id, request_id, position),
  FOREIGN KEY (tenant_id, request_id) REFERENCES capex_request (tenant_id, id)
);
ALTER TABLE capex_request_variant ENABLE ROW LEVEL SECURITY;
ALTER TABLE capex_request_variant FORCE ROW LEVEL SECURITY;
CREATE POLICY capex_request_variant_tenant_access ON capex_request_variant AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY capex_request_variant_legal_entity_scope ON capex_request_variant AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
