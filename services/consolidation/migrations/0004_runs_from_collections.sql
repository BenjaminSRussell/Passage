-- FR-40.7, FR-40.8, FR-40.10: what a run needs beyond a collection of balances. A unit has its own fiscal year; the chart
-- mapping is versioned and names the role of an account (so net assets, net income and the intercompany balances can be read
-- from collected balances); a unit's acquisition terms (investment cost, share capital and retained earnings at acquisition,
-- the historical rate) are stored; and a run records the collection it was computed from.

ALTER TABLE consolidation_unit
  ADD COLUMN IF NOT EXISTS fiscal_year_start_month integer NOT NULL DEFAULT 1 CHECK (fiscal_year_start_month BETWEEN 1 AND 12);

CREATE TABLE IF NOT EXISTS consolidation_mapping_version (
  tenant_id uuid NOT NULL,
  group_id uuid NOT NULL,
  version integer NOT NULL CHECK (version >= 1),
  created_by text NOT NULL CHECK (length(created_by) >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, group_id, version),
  FOREIGN KEY (tenant_id, group_id) REFERENCES consolidation_group (tenant_id, id)
);

ALTER TABLE consolidation_account_map
  ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  ADD COLUMN IF NOT EXISTS role text CHECK (role IS NULL OR role IN ('asset', 'liability', 'equity', 'ic_sales', 'ic_cost', 'ic_receivable', 'ic_payable'));
INSERT INTO consolidation_mapping_version (tenant_id, group_id, version, created_by)
  SELECT DISTINCT tenant_id, group_id, 1, 'migration' FROM consolidation_account_map ON CONFLICT DO NOTHING;
ALTER TABLE consolidation_account_map DROP CONSTRAINT IF EXISTS consolidation_account_map_pkey;
ALTER TABLE consolidation_account_map ADD PRIMARY KEY (tenant_id, group_id, version, main_account);
ALTER TABLE consolidation_account_map
  ADD CONSTRAINT consolidation_account_map_version_fk FOREIGN KEY (tenant_id, group_id, version) REFERENCES consolidation_mapping_version (tenant_id, group_id, version);

ALTER TABLE consolidation_collection ADD COLUMN IF NOT EXISTS mapping_version integer;
ALTER TABLE consolidation_collection_line ADD COLUMN IF NOT EXISTS role text;

-- Acquisition terms of a unit (the stored investment of the group in it).
CREATE TABLE IF NOT EXISTS consolidation_unit_terms (
  tenant_id uuid NOT NULL,
  unit_id uuid NOT NULL,
  investment_cost numeric(28,6) NOT NULL DEFAULT 0 CHECK (investment_cost >= 0),
  share_capital numeric(28,6) NOT NULL DEFAULT 0 CHECK (share_capital >= 0),
  pre_acquisition_re numeric(28,6) NOT NULL DEFAULT 0,
  historical_rate numeric(20,10) NOT NULL DEFAULT 1 CHECK (historical_rate > 0),
  seller_is_subsidiary boolean NOT NULL DEFAULT false,
  ic_net_investment boolean NOT NULL DEFAULT false,
  fx_gain_loss_account text CHECK (fx_gain_loss_account IS NULL OR fx_gain_loss_account ~ '^[A-Za-z0-9._:-]{1,64}$'),
  updated_by text NOT NULL CHECK (length(updated_by) >= 1),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, unit_id),
  FOREIGN KEY (tenant_id, unit_id) REFERENCES consolidation_unit (tenant_id, id)
);
ALTER TABLE consolidation_unit_terms ENABLE ROW LEVEL SECURITY;
ALTER TABLE consolidation_unit_terms FORCE ROW LEVEL SECURITY;
CREATE POLICY consolidation_unit_terms_tenant_access ON consolidation_unit_terms AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
ALTER TABLE consolidation_mapping_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE consolidation_mapping_version FORCE ROW LEVEL SECURITY;
CREATE POLICY consolidation_mapping_version_tenant_access ON consolidation_mapping_version AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE consolidation_run ADD COLUMN IF NOT EXISTS collection_id uuid;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON consolidation_mapping_version TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON consolidation_unit_terms TO passage_app';
    -- A unit can be edited and, while nothing refers to it, removed with its ownership rows.
    EXECUTE 'GRANT UPDATE, DELETE ON consolidation_unit TO passage_app';
    EXECUTE 'GRANT DELETE ON consolidation_ownership, consolidation_unit_terms TO passage_app';
    -- Mapping rows are versions now: written once, never replaced.
    EXECUTE 'REVOKE UPDATE, DELETE ON consolidation_account_map FROM passage_app';
  END IF;
END
$$;
