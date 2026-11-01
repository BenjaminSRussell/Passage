-- FR-17.20 category vendor assignment, category managers and the category access mode.
-- A procurement category is the free-text category of requisition lines, PO lines and
-- catalog entries. A category with at least one APPROVED or PROVISIONAL assignment is
-- restricted: the vendors on its lines must hold a valid APPROVED (or PROVISIONAL)
-- assignment (PRC-CAT-002). A BLOCKED assignment in force blocks that vendor in any
-- category; it does not restrict the category for other vendors.
CREATE TABLE IF NOT EXISTS procurement_category_vendor (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  category text NOT NULL CHECK (length(category) BETWEEN 1 AND 200),
  vendor_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('APPROVED', 'PROVISIONAL', 'BLOCKED')),
  valid_from date NOT NULL,
  valid_to date,
  notes text NOT NULL DEFAULT '',
  row_version integer NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (valid_to IS NULL OR valid_to >= valid_from)
);
-- One assignment per vendor and category; a change of status or window is an update.
CREATE UNIQUE INDEX IF NOT EXISTS procurement_category_vendor_unique
  ON procurement_category_vendor (tenant_id, legal_entity_id, category, vendor_id);
CREATE INDEX IF NOT EXISTS procurement_category_vendor_list
  ON procurement_category_vendor (tenant_id, id DESC);

CREATE TABLE IF NOT EXISTS procurement_category_manager (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  category text NOT NULL CHECK (length(category) BETWEEN 1 AND 200),
  worker_id uuid NOT NULL,
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS procurement_category_manager_unique
  ON procurement_category_manager (tenant_id, legal_entity_id, category, worker_id);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['procurement_category_vendor', 'procurement_category_manager']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))) WITH CHECK (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
                   t || '_legal_entity_scope', t);
  END LOOP;
END $$;

-- category_access_mode (PRD 3.17.10, default SOFT): HARD refuses a vendor with no valid
-- assignment on a restricted category, SOFT warns, OFF does not check.
ALTER TABLE purchasing_policy ADD COLUMN IF NOT EXISTS category_access_mode text NOT NULL DEFAULT 'SOFT'
  CHECK (category_access_mode IN ('HARD', 'SOFT', 'OFF'));

-- The procurement category of a PO line (PRD data model "product_id or category_id"):
-- copied from the requisition line it orders, or given on a direct line, so the category
-- access rule also covers POs raised without a requisition. It is set when the line is
-- created and does not change afterwards; lines written before this migration carry none.
ALTER TABLE purchase_order_line ADD COLUMN IF NOT EXISTS category text NOT NULL DEFAULT ''
  CHECK (length(category) <= 200);

-- passage_app: an assignment changes status and window (UPDATE) and is never deleted, a
-- BLOCKED row is the record; a category manager row is removed (DELETE).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON procurement_category_vendor TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON procurement_category_manager TO passage_app';
  END IF;
END
$$;
