-- PSG-1514 (wave 15, slice 14): reporting tree revisions (FR-15.4).
-- report_tree holds only the latest body. A run records the tree revision it
-- used (statement_run.tree_revision); to show that run's units later, every
-- revision body is kept here, the way hierarchy versions are kept. Rows are
-- written by the store in the same transaction as the tree and never change.
CREATE TABLE IF NOT EXISTS report_tree_revision (
  tenant_id uuid NOT NULL,
  tree_code text NOT NULL CHECK (tree_code ~ '^[A-Z][A-Z0-9-]{1,29}$'),
  revision integer NOT NULL CHECK (revision >= 1),
  body jsonb NOT NULL,
  updated_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, tree_code, revision)
);
ALTER TABLE report_tree_revision ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_tree_revision FORCE ROW LEVEL SECURITY;
CREATE POLICY report_tree_revision_tenant_access ON report_tree_revision AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Trees saved before this migration keep their current revision only; earlier
-- revisions were not stored, so a run made on one cannot show its units. The
-- backfill reads every tenant's trees, so it needs a role that bypasses
-- row-level security (report_tree is FORCE ROW LEVEL SECURITY); any other role
-- would see no rows, so it is skipped with a notice. Nothing is lost then: the
-- store reads a current revision that has no row from report_tree and stores it
-- under the row lock of the next replace.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) THEN
    RAISE NOTICE 'report_tree_revision backfill skipped: role % does not bypass row-level security', current_user;
    RETURN;
  END IF;
  INSERT INTO report_tree_revision(tenant_id, tree_code, revision, body, updated_by, created_at)
    SELECT tenant_id, code, revision, body, updated_by, updated_at FROM report_tree
    ON CONFLICT DO NOTHING;
END
$$;

-- The store inserts and reads revisions; it never updates or deletes them.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON report_tree_revision TO passage_app';
  END IF;
END
$$;
