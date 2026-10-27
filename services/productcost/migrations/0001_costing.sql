-- Wave 39 costing schema. Slice 1 computes stateless; these tables hold the
-- persisted versions, calculations, splits and variances for the next slice.
CREATE TABLE IF NOT EXISTS costing_version (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL CHECK (char_length(code) BETWEEN 1 AND 30),
  type text NOT NULL CHECK (type IN ('Standard', 'Planned', 'Simulation', 'Conversion')),
  status text NOT NULL DEFAULT 'Open' CHECK (status IN ('Open', 'Blocked', 'Active', 'Closed')),
  fallback_principle text NOT NULL DEFAULT 'Active' CHECK (fallback_principle IN ('None', 'Active', 'SpecifiedVersion')),
  fallback_version_id uuid,
  site_ids uuid[] NOT NULL DEFAULT '{}',
  effective_from timestamptz,
  activated_at timestamptz,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, code),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE costing_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE costing_version FORCE ROW LEVEL SECURITY;
CREATE POLICY costing_version_tenant_access ON costing_version AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY costing_version_legal_entity_scope ON costing_version AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS cost_component (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (char_length(code) BETWEEN 1 AND 20),
  name text NOT NULL,
  cost_group_type text NOT NULL CHECK (cost_group_type IN ('DirectMaterial', 'DirectManufacturing', 'Indirect', 'Undefined')),
  behaviour text NOT NULL DEFAULT 'Variable' CHECK (behaviour IN ('Fixed', 'Variable')),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE cost_component ENABLE ROW LEVEL SECURITY;
ALTER TABLE cost_component FORCE ROW LEVEL SECURITY;
CREATE POLICY cost_component_tenant_access ON cost_component AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS cost_calculation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  costing_version_id uuid NOT NULL,
  item_id uuid NOT NULL,
  site_id uuid NOT NULL,
  calc_type text NOT NULL DEFAULT 'Item' CHECK (calc_type IN ('Item', 'SalesOrder', 'AdHoc', 'Mixed')),
  valuation_view text NOT NULL DEFAULT 'Legal' CHECK (valuation_view IN ('Legal', 'Group', 'ProfitCenter')),
  calc_quantity numeric(19,4) NOT NULL CHECK (calc_quantity > 0),
  explosion_mode text NOT NULL CHECK (explosion_mode IN ('Multilevel', 'SingleLevel', 'MakeToOrder', 'ByLineType')),
  bom_version_id uuid NOT NULL,
  route_version_id uuid NOT NULL,
  unit_cost numeric(19,6) NOT NULL CHECK (unit_cost >= 0),
  status text NOT NULL CHECK (status IN ('Pending', 'Active', 'Superseded', 'Error')),
  diagnostics jsonb NOT NULL DEFAULT '[]',
  run_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, costing_version_id) REFERENCES costing_version (tenant_id, id)
);
-- Exactly one Active calculation per (version, item, site, valuation view) (FR-39.17).
CREATE UNIQUE INDEX IF NOT EXISTS cost_calculation_one_active
  ON cost_calculation (tenant_id, costing_version_id, item_id, site_id, valuation_view)
  WHERE status = 'Active';
ALTER TABLE cost_calculation ENABLE ROW LEVEL SECURITY;
ALTER TABLE cost_calculation FORCE ROW LEVEL SECURITY;
CREATE POLICY cost_calculation_tenant_access ON cost_calculation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY cost_calculation_legal_entity_scope ON cost_calculation AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS cost_component_split (
  tenant_id uuid NOT NULL,
  calculation_id uuid NOT NULL,
  component_id uuid NOT NULL,
  level text NOT NULL CHECK (level IN ('Own', 'Rolled')),
  amount numeric(19,6) NOT NULL,
  fixed_amount numeric(19,6) NOT NULL DEFAULT 0,
  variable_amount numeric(19,6) NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, calculation_id, component_id, level),
  FOREIGN KEY (tenant_id, calculation_id) REFERENCES cost_calculation (tenant_id, id)
);
ALTER TABLE cost_component_split ENABLE ROW LEVEL SECURITY;
ALTER TABLE cost_component_split FORCE ROW LEVEL SECURITY;
CREATE POLICY cost_component_split_tenant_access ON cost_component_split AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS variance_entry (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  source_type text NOT NULL CHECK (source_type IN ('ProductionOrder', 'Receipt', 'Invoice', 'Activation')),
  source_id uuid NOT NULL,
  type text NOT NULL CHECK (type IN ('LotSize', 'Quantity', 'Price', 'Substitution', 'PurchasePrice', 'CostChange')),
  component_id uuid NOT NULL,
  item_id uuid NOT NULL,
  site_id uuid NOT NULL,
  period text NOT NULL,
  amount numeric(19,4) NOT NULL,
  journal_entry_id uuid,
  detail jsonb NOT NULL DEFAULT '{}',
  PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS variance_entry_site_period_type
  ON variance_entry (tenant_id, site_id, period, type);
ALTER TABLE variance_entry ENABLE ROW LEVEL SECURITY;
ALTER TABLE variance_entry FORCE ROW LEVEL SECURITY;
CREATE POLICY variance_entry_tenant_access ON variance_entry AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY variance_entry_legal_entity_scope ON variance_entry AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
