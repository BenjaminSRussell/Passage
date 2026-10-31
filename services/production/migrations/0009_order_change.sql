-- Wave 21 slice 10: order change management (FR-21.16, BR-21.12).
-- Additive (expand): new tables only.

-- OCM profile per legal entity × item group × site (either may be 'All'): the mode and
-- the change-cost surcharge (a fixed amount plus a percentage of a positive delta).
CREATE TABLE IF NOT EXISTS production_ocm_profile (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  item_group text NOT NULL,
  site_key text NOT NULL,
  mode text NOT NULL CHECK (mode IN ('automatic', 'manual', 'none')),
  surcharge_fixed numeric(18,4) NOT NULL DEFAULT 0 CHECK (surcharge_fixed >= 0),
  surcharge_pct numeric(9,4) NOT NULL DEFAULT 0 CHECK (surcharge_pct >= 0),
  version integer NOT NULL DEFAULT 1,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, item_group, site_key)
);

-- OrderChangeRecord: one row per (order, ECO) detected as superseded, with the new
-- BOM and route snapshot the ECO brings (null = untouched), the delta and change cost at
-- detection, and, once applied, the before and after snapshot hashes (FR-21.16).
CREATE TABLE IF NOT EXISTS production_order_change (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  eco_id text NOT NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'applied', 'dismissed')),
  profile_mode text NOT NULL CHECK (profile_mode IN ('automatic', 'manual')),
  needs_review boolean NOT NULL DEFAULT false,
  new_bom jsonb,
  new_operations jsonb,
  request_hash text NOT NULL,
  lines_added integer NOT NULL DEFAULT 0,
  lines_removed integer NOT NULL DEFAULT 0,
  lines_changed integer NOT NULL DEFAULT 0,
  operations_added integer NOT NULL DEFAULT 0,
  operations_removed integer NOT NULL DEFAULT 0,
  operations_changed integer NOT NULL DEFAULT 0,
  currency char(3) NOT NULL,
  change_cost numeric(18,4) NOT NULL DEFAULT 0,
  before_hash text,
  after_hash text,
  note text NOT NULL DEFAULT '',
  version integer NOT NULL DEFAULT 1,
  detected_by text NOT NULL,
  detected_at timestamptz NOT NULL DEFAULT now(),
  resolved_by text,
  resolved_at timestamptz,
  apply_idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, order_id, eco_id),
  UNIQUE (tenant_id, apply_idempotency_key),
  FOREIGN KEY (tenant_id, order_id) REFERENCES production_order (tenant_id, id),
  CHECK (new_bom IS NOT NULL OR new_operations IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS production_order_change_worklist ON production_order_change (tenant_id, status, detected_at);
CREATE INDEX IF NOT EXISTS production_order_change_eco ON production_order_change (tenant_id, eco_id);

ALTER TABLE production_ocm_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_ocm_profile FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS production_ocm_profile_tenant_access ON production_ocm_profile;
CREATE POLICY production_ocm_profile_tenant_access ON production_ocm_profile AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS production_ocm_profile_legal_entity_scope ON production_ocm_profile;
CREATE POLICY production_ocm_profile_legal_entity_scope ON production_ocm_profile AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE production_order_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_order_change FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS production_order_change_tenant_access ON production_order_change;
CREATE POLICY production_order_change_tenant_access ON production_order_change AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS production_order_change_legal_entity_scope ON production_order_change;
CREATE POLICY production_order_change_legal_entity_scope ON production_order_change AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The profile store upserts and updates; the change store inserts and updates, never deletes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON production_ocm_profile TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON production_order_change TO passage_app';
  END IF;
END
$$;
