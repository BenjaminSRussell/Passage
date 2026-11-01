-- FR-17.18 vendor catalog import: a vendor file is staged row by row (ACCEPTABLE or ERROR
-- with a reason), the buyer accepts or rejects rows, and the commit adds the accepted rows
-- to the catalog as entries. The staging tables keep the file as the vendor sent it (raw).
CREATE TABLE IF NOT EXISTS procurement_catalog_import (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  catalog_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  format text NOT NULL CHECK (format IN ('CSV', 'JSON')),
  file_name text NOT NULL DEFAULT '',
  -- sha256 of the vendor and the file's rows: the same file is staged once (PRD 3.17.16).
  file_hash text NOT NULL CHECK (length(file_hash) = 64),
  status text NOT NULL DEFAULT 'STAGED' CHECK (status IN ('STAGED', 'COMMITTED', 'CANCELLED')),
  entries_added integer NOT NULL DEFAULT 0 CHECK (entries_added >= 0),
  row_version integer NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, catalog_id) REFERENCES procurement_catalog (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS procurement_catalog_import_catalog ON procurement_catalog_import (tenant_id, catalog_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS procurement_catalog_import_file ON procurement_catalog_import (tenant_id, catalog_id, vendor_id, file_hash)
  WHERE status <> 'CANCELLED';

CREATE TABLE IF NOT EXISTS procurement_catalog_import_row (
  tenant_id uuid NOT NULL,
  import_id uuid NOT NULL,
  row_no integer NOT NULL CHECK (row_no > 0),
  legal_entity_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('ACCEPTABLE', 'ERROR', 'ACCEPTED', 'REJECTED')),
  reason text NOT NULL DEFAULT '',
  raw jsonb NOT NULL,
  -- Typed values of a row that passed validation; null on an ERROR row.
  vendor_part_no text,
  product_id uuid,
  name text,
  description text,
  category text,
  unit text,
  unit_price numeric(20,6),
  currency char(3),
  lead_time_days integer,
  image_url text,
  PRIMARY KEY (tenant_id, import_id, row_no),
  FOREIGN KEY (tenant_id, import_id) REFERENCES procurement_catalog_import (tenant_id, id),
  CHECK ((status = 'ERROR') = (name IS NULL))
);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['procurement_catalog_import', 'procurement_catalog_import_row']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))) WITH CHECK (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
                   t || '_legal_entity_scope', t);
  END LOOP;
END $$;

-- passage_app: an import changes status and version, its rows change status on a buyer
-- decision (UPDATE); nothing is deleted, a cancelled import stays as the record.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON procurement_catalog_import, procurement_catalog_import_row TO passage_app';
  END IF;
END
$$;
