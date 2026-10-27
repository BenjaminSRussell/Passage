-- Wave 37 slice 3: lease remeasurement on modification (FR-37.10, BR-37.8)
-- with the effective periodic rate option (lease.periodicRateMethod, 7.37.13),
-- and physical inventory count sessions (FR-37.11). Expand-only: existing rows
-- keep their meaning (Nominal rate, no remeasurement). Every new table carries
-- tenant_id first, ENABLE and FORCE row-level security, the tenant policy and
-- the legal-entity scope.

ALTER TABLE lease ADD COLUMN IF NOT EXISTS rate_method text NOT NULL DEFAULT 'Nominal'
  CHECK (rate_method IN ('Nominal', 'Effective'));

-- remeasurement is added to opening_liability before the period's payment and
-- interest; it is non-zero only on the first period after a modification.
ALTER TABLE lease_schedule_line ADD COLUMN IF NOT EXISTS remeasurement numeric(20,6) NOT NULL DEFAULT 0;

-- One row per remeasurement. liability_before is the carrying liability at the
-- start of the effective period under the terms in force, liability_after the
-- present value of the remaining payments under the revised terms; adjustment
-- is their difference. The ROU moves by rou_adjustment (equal to adjustment
-- unless the ROU reached zero, when profit_loss_excess takes the rest).
CREATE TABLE IF NOT EXISTS lease_modification (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  lease_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  effective_period integer NOT NULL CHECK (effective_period >= 1),
  term_months integer NOT NULL CHECK (term_months BETWEEN 1 AND 600),
  payment_amount numeric(20,6) NOT NULL CHECK (payment_amount > 0),
  ibr_rate numeric(9,6) NOT NULL CHECK (ibr_rate > 0 AND ibr_rate <= 0.5),
  scope_change boolean NOT NULL,
  liability_before numeric(20,6) NOT NULL,
  liability_after numeric(20,6) NOT NULL CHECK (liability_after >= 0),
  adjustment numeric(20,6) NOT NULL,
  rou_before numeric(20,6) NOT NULL CHECK (rou_before >= 0),
  rou_after numeric(20,6) NOT NULL CHECK (rou_after >= 0),
  rou_adjustment numeric(20,6) NOT NULL,
  profit_loss_excess numeric(20,6) NOT NULL CHECK (profit_loss_excess >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, lease_id) REFERENCES lease (tenant_id, id),
  CHECK (liability_after - liability_before = adjustment),
  CHECK (rou_after - rou_before = rou_adjustment)
);
CREATE INDEX IF NOT EXISTS lease_modification_lease ON lease_modification (tenant_id, lease_id, created_at);
ALTER TABLE lease_modification ENABLE ROW LEVEL SECURITY;
ALTER TABLE lease_modification FORCE ROW LEVEL SECURITY;
CREATE POLICY lease_modification_tenant_access ON lease_modification AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY lease_modification_legal_entity_scope ON lease_modification AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- A count session reconciles the scanned barcodes against the Open assets of a
-- legal entity that carry a barcode (the expected register).
CREATE TABLE IF NOT EXISTS asset_count_session (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  location text NOT NULL CHECK (char_length(location) BETWEEN 1 AND 80),
  counted_date date NOT NULL,
  expected_count integer NOT NULL CHECK (expected_count >= 0),
  found_count integer NOT NULL CHECK (found_count >= 0),
  missing_count integer NOT NULL CHECK (missing_count >= 0),
  unknown_count integer NOT NULL CHECK (unknown_count >= 0),
  duplicate_count integer NOT NULL CHECK (duplicate_count >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (found_count + missing_count = expected_count)
);
CREATE INDEX IF NOT EXISTS asset_count_session_entity ON asset_count_session (tenant_id, legal_entity_id, created_at);
ALTER TABLE asset_count_session ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_count_session FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_count_session_tenant_access ON asset_count_session AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY asset_count_session_legal_entity_scope ON asset_count_session AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- One line per distinct barcode: Found and Missing reference the asset,
-- Unknown has none.
CREATE TABLE IF NOT EXISTS asset_count_line (
  tenant_id uuid NOT NULL,
  session_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  barcode text NOT NULL CHECK (char_length(barcode) BETWEEN 1 AND 64),
  outcome text NOT NULL CHECK (outcome IN ('Found', 'Missing', 'Unknown')),
  asset_id uuid,
  PRIMARY KEY (tenant_id, session_id, barcode),
  FOREIGN KEY (tenant_id, session_id) REFERENCES asset_count_session (tenant_id, id),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id),
  CHECK ((outcome = 'Unknown') = (asset_id IS NULL))
);
ALTER TABLE asset_count_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_count_line FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_count_line_tenant_access ON asset_count_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY asset_count_line_legal_entity_scope ON asset_count_line AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
