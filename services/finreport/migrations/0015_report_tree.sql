-- PSG-1513 (wave 15, slice 13): reporting trees (FR-15.4, PRD §2.15.5 /trees).
-- A tree lists reporting units (values of one dimension attribute, with parents
-- for roll-up). One row per code; body holds the tree as the API returns it.
-- Replacing a tree is revision-checked with If-Match.
CREATE TABLE IF NOT EXISTS report_tree (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z][A-Z0-9-]{1,29}$'),
  revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
  body jsonb NOT NULL,
  updated_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE report_tree ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_tree FORCE ROW LEVEL SECURITY;
CREATE POLICY report_tree_tenant_access ON report_tree AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A run limited to a tree unit records the tree, the unit (empty for the whole
-- tree) and the tree revision it used. Plain runs leave all three NULL.
-- statement_run keeps the grants and policies it already has.
ALTER TABLE statement_run ADD COLUMN IF NOT EXISTS tree_code text;
ALTER TABLE statement_run ADD COLUMN IF NOT EXISTS tree_unit text;
ALTER TABLE statement_run ADD COLUMN IF NOT EXISTS tree_revision integer;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'statement_run_tree_complete') THEN
    ALTER TABLE statement_run ADD CONSTRAINT statement_run_tree_complete
      CHECK ((tree_code IS NULL) = (tree_unit IS NULL) AND (tree_code IS NULL) = (tree_revision IS NULL));
  END IF;
END
$$;

-- The store inserts a tree, row-locks it (FOR UPDATE) and updates it on replace; it never deletes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON report_tree TO passage_app';
  END IF;
END
$$;
