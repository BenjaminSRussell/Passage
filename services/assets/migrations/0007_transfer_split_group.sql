-- Wave 37 slice 5: transfer and split of assets (FR-37.7) and group assets for
-- pooled tax depreciation (FR-37.16). Expand-only: existing rows keep their
-- meaning. Every new table carries tenant_id first, ENABLE and FORCE row-level
-- security, the tenant policy and the legal-entity scope.

-- A transfer or split writes a Transfer / Split transaction that empties each
-- source book and one that opens each target book.
ALTER TABLE asset_transaction DROP CONSTRAINT IF EXISTS asset_transaction_type_check;
ALTER TABLE asset_transaction ADD CONSTRAINT asset_transaction_type_check CHECK (type IN (
  'Acquisition', 'Depreciation', 'DisposalSale', 'DisposalScrap',
  'AcquisitionAdj', 'DepreciationAdj', 'Revaluation', 'Impairment',
  'Transfer', 'Split'));

-- The source books of a transfer or split end as Transferred (BR-37.2). The
-- 0001 CHECK is unnamed, so it carries the generated name asset_book_status_check.
ALTER TABLE asset_book DROP CONSTRAINT IF EXISTS asset_book_status_check;
ALTER TABLE asset_book ADD CONSTRAINT asset_book_status_check
  CHECK (status IN ('Open', 'Sold', 'Scrapped', 'Transferred'));

-- One row per transfer or split: the source asset and the date. The
-- transactions it wrote carry source_type 'transfer' or 'split' and
-- source_id = the transfer id.
CREATE TABLE IF NOT EXISTS asset_transfer (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  source_asset_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('transfer', 'split')),
  transfer_date date NOT NULL,
  created_by text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, source_asset_id) REFERENCES asset (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS asset_transfer_source ON asset_transfer (tenant_id, source_asset_id);
ALTER TABLE asset_transfer ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_transfer FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_transfer_tenant_access ON asset_transfer AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY asset_transfer_legal_entity_scope ON asset_transfer AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- One row per target asset of a transfer or split with the share it took.
-- legal_entity_id is the target's legal entity (a transfer may move the asset
-- to another legal entity of the tenant).
CREATE TABLE IF NOT EXISTS asset_transfer_part (
  tenant_id uuid NOT NULL,
  transfer_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no >= 1),
  target_asset_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  percentage numeric(5,2) NOT NULL CHECK (percentage > 0 AND percentage <= 100),
  PRIMARY KEY (tenant_id, transfer_id, line_no),
  CONSTRAINT asset_transfer_part_target_unique UNIQUE (tenant_id, target_asset_id),
  FOREIGN KEY (tenant_id, transfer_id) REFERENCES asset_transfer (tenant_id, id),
  FOREIGN KEY (tenant_id, target_asset_id) REFERENCES asset (tenant_id, id)
);
ALTER TABLE asset_transfer_part ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_transfer_part FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_transfer_part_tenant_access ON asset_transfer_part AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY asset_transfer_part_legal_entity_scope ON asset_transfer_part AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- GroupAsset (FR-37.16): members of one class and tax year pooled on one tax
-- book. The group's depreciation profile is its own; its basis is the sum of
-- its members' cost on that book, read live so adjustments and disposals of a
-- member move it.
CREATE TABLE IF NOT EXISTS group_asset (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  group_code text NOT NULL CHECK (group_code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  class_code text NOT NULL CHECK (class_code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  book_code text NOT NULL CHECK (book_code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  tax_year integer NOT NULL CHECK (tax_year BETWEEN 1900 AND 2200),
  currency char(3) NOT NULL,
  method text NOT NULL CHECK (method IN ('StraightLine', 'DecliningBalance')),
  factor numeric(9,6) NOT NULL DEFAULT 0 CHECK (factor >= 0),
  switch_to_straight_line boolean NOT NULL DEFAULT false,
  service_life_periods integer NOT NULL CHECK (service_life_periods BETWEEN 1 AND 1200),
  convention text NOT NULL CHECK (convention IN ('None', 'FullMonth', 'MidMonth15', 'HalfYear', 'MidQuarter')),
  status text NOT NULL DEFAULT 'Open' CHECK (status IN ('Open')),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT group_asset_code_unique UNIQUE (tenant_id, legal_entity_id, group_code)
);
CREATE INDEX IF NOT EXISTS group_asset_list ON group_asset (tenant_id, legal_entity_id, group_code);
ALTER TABLE group_asset ENABLE ROW LEVEL SECURITY;
ALTER TABLE group_asset FORCE ROW LEVEL SECURITY;
CREATE POLICY group_asset_tenant_access ON group_asset AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY group_asset_legal_entity_scope ON group_asset AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- BR-37.11: a book belongs to at most one group asset (a member asset has one
-- book per book code, so this is one group per book).
CREATE TABLE IF NOT EXISTS group_asset_member (
  tenant_id uuid NOT NULL,
  group_id uuid NOT NULL,
  asset_book_id uuid NOT NULL,
  asset_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  added_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, group_id, asset_book_id),
  CONSTRAINT group_asset_member_book_unique UNIQUE (tenant_id, asset_book_id),
  FOREIGN KEY (tenant_id, group_id) REFERENCES group_asset (tenant_id, id),
  FOREIGN KEY (tenant_id, asset_book_id) REFERENCES asset_book (tenant_id, id)
);
ALTER TABLE group_asset_member ENABLE ROW LEVEL SECURITY;
ALTER TABLE group_asset_member FORCE ROW LEVEL SECURITY;
CREATE POLICY group_asset_member_tenant_access ON group_asset_member AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY group_asset_member_legal_entity_scope ON group_asset_member AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Application role privileges for the transfer and group-asset tables (0006_grants.sql
-- covers the earlier tables). All four are written once and only read afterwards.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON asset_transfer, asset_transfer_part, group_asset, group_asset_member TO passage_app';
  END IF;
END
$$;
