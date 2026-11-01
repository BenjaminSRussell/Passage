-- FR-17.18 procurement catalogs: a catalog per legal entity with vendor product
-- entries, published to operating units for a validity window. A requisition line
-- may point at an entry (catalog_entry_id); a line without one is a non-catalog line.
CREATE SEQUENCE IF NOT EXISTS procurement_catalog_number_seq;

CREATE TABLE IF NOT EXISTS procurement_catalog (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL CHECK (length(btrim(name)) > 0),
  description text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'PUBLISHED', 'RETIRED')),
  valid_from date NOT NULL,
  valid_to date,
  -- Operating units the catalog is published to; empty means every unit.
  org_unit_ids uuid[] NOT NULL DEFAULT '{}',
  row_version integer NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number),
  CHECK (valid_to IS NULL OR valid_to >= valid_from)
);
CREATE INDEX IF NOT EXISTS procurement_catalog_status ON procurement_catalog (tenant_id, status, id);

CREATE TABLE IF NOT EXISTS procurement_catalog_entry (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  catalog_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  entry_no integer NOT NULL CHECK (entry_no > 0),
  vendor_id uuid NOT NULL,
  vendor_part_no text NOT NULL DEFAULT '',
  product_id uuid,
  name text NOT NULL CHECK (length(btrim(name)) > 0),
  description text NOT NULL DEFAULT '',
  category text NOT NULL CHECK (length(btrim(category)) > 0),
  unit text NOT NULL CHECK (length(btrim(unit)) > 0),
  unit_price numeric(20,6) NOT NULL CHECK (unit_price >= 0),
  currency char(3) NOT NULL,
  lead_time_days integer NOT NULL DEFAULT 0 CHECK (lead_time_days >= 0),
  preferred boolean NOT NULL DEFAULT false,
  image_url text NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, catalog_id, entry_no),
  FOREIGN KEY (tenant_id, catalog_id) REFERENCES procurement_catalog (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS procurement_catalog_entry_search ON procurement_catalog_entry (tenant_id, legal_entity_id, category, id);

ALTER TABLE purchase_requisition_line ADD COLUMN IF NOT EXISTS catalog_entry_id uuid;

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['procurement_catalog', 'procurement_catalog_entry']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))) WITH CHECK (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
                   t || '_legal_entity_scope', t);
  END LOOP;
END $$;

-- passage_app: catalogs change status and version (UPDATE); entries are added and, in
-- a DRAFT catalog, removed (DELETE).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT USAGE ON SEQUENCE procurement_catalog_number_seq TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON procurement_catalog TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON procurement_catalog_entry TO passage_app';
  END IF;
END
$$;
