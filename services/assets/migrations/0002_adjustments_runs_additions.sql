-- Wave 37 slice 2: adjustments, revaluation and impairment, depreciation runs,
-- and asset additions. Expand-only: existing rows and columns keep their
-- meaning. Every new table carries tenant_id first, ENABLE and FORCE row-level
-- security, the tenant policy and (with legal_entity_id) the legal-entity scope.

ALTER TABLE asset_transaction DROP CONSTRAINT IF EXISTS asset_transaction_type_check;
ALTER TABLE asset_transaction ADD CONSTRAINT asset_transaction_type_check CHECK (type IN (
  'Acquisition', 'Depreciation', 'DisposalSale', 'DisposalScrap',
  'AcquisitionAdj', 'DepreciationAdj', 'Revaluation', 'Impairment'));
-- The impairment share of accumulated_amount, so reversals can be capped (FR-37.9).
ALTER TABLE asset_transaction ADD COLUMN IF NOT EXISTS impairment_amount numeric(20,6) NOT NULL DEFAULT 0;

-- acquisition_cost is the schedule basis the run spreads charges by; NULL on
-- books created before this migration means "use cost".
ALTER TABLE asset_book ADD COLUMN IF NOT EXISTS acquisition_cost numeric(20,6);
ALTER TABLE asset_book ADD COLUMN IF NOT EXISTS last_depreciated_period text
  CHECK (last_depreciated_period IS NULL OR last_depreciated_period ~ '^[0-9]{4}(-(0[1-9]|1[0-2]))?$');
ALTER TABLE asset_book DROP CONSTRAINT IF EXISTS asset_book_accumulated_within_cost;
ALTER TABLE asset_book ADD CONSTRAINT asset_book_accumulated_within_cost CHECK (accumulated_depreciation <= cost);

-- One run per (legal entity, book code, period); BR-37.9 adds an advisory lock.
CREATE TABLE IF NOT EXISTS depreciation_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  book_code text NOT NULL CHECK (book_code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  period text NOT NULL CHECK (period ~ '^[0-9]{4}(-(0[1-9]|1[0-2]))?$'),
  currency char(3),
  status text NOT NULL DEFAULT 'Proposed' CHECK (status IN ('Proposed', 'Posted')),
  asset_count integer NOT NULL DEFAULT 0 CHECK (asset_count >= 0),
  exception_count integer NOT NULL DEFAULT 0 CHECK (exception_count >= 0),
  total_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (total_amount >= 0),
  journal_entry_id uuid,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  posted_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT depreciation_run_unique UNIQUE (tenant_id, legal_entity_id, book_code, period)
);
CREATE INDEX IF NOT EXISTS depreciation_run_recent ON depreciation_run (tenant_id, created_at DESC, id);
ALTER TABLE depreciation_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE depreciation_run FORCE ROW LEVEL SECURITY;
CREATE POLICY depreciation_run_tenant_access ON depreciation_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY depreciation_run_legal_entity_scope ON depreciation_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS depreciation_run_line (
  tenant_id uuid NOT NULL,
  run_id uuid NOT NULL,
  asset_book_id uuid NOT NULL,
  asset_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('Due', 'Posted', 'Exception')),
  amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (amount >= 0),
  scheduled_amount numeric(20,6) NOT NULL DEFAULT 0,
  exception_code text,
  detail text,
  PRIMARY KEY (tenant_id, run_id, asset_book_id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES depreciation_run (tenant_id, id),
  FOREIGN KEY (tenant_id, asset_book_id) REFERENCES asset_book (tenant_id, id)
);
ALTER TABLE depreciation_run_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE depreciation_run_line FORCE ROW LEVEL SECURITY;
CREATE POLICY depreciation_run_line_tenant_access ON depreciation_run_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY depreciation_run_line_legal_entity_scope ON depreciation_run_line AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- AssetAddition (FR-37.14). A financial addition's AcquisitionAdj transactions
-- carry source_type 'addition' and source_id = the addition id.
CREATE TABLE IF NOT EXISTS asset_addition (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  asset_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  type text NOT NULL CHECK (type IN ('Improvement', 'Component', 'Accessory')),
  description text NOT NULL CHECK (char_length(description) BETWEEN 1 AND 200),
  quantity numeric(19,4) NOT NULL DEFAULT 1 CHECK (quantity > 0),
  serial_number text CHECK (serial_number IS NULL OR char_length(serial_number) BETWEEN 1 AND 64),
  added_date date NOT NULL,
  financial boolean NOT NULL DEFAULT false,
  cost_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (cost_amount >= 0),
  currency char(3),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id),
  CHECK (financial = (currency IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS asset_addition_asset ON asset_addition (tenant_id, asset_id, added_date);
ALTER TABLE asset_addition ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_addition FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_addition_tenant_access ON asset_addition AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY asset_addition_legal_entity_scope ON asset_addition AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
CREATE INDEX IF NOT EXISTS asset_transaction_source ON asset_transaction (tenant_id, source_type, source_id) WHERE source_id IS NOT NULL;
