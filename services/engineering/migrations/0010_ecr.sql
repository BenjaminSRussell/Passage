-- FR-9.7 engineering change requests: an ECR (title, reason, priority, affected products, requester) moves
-- draft -> submitted -> under_review -> approved | rejected (or cancelled) and feeds at most one live ECO.
CREATE TABLE IF NOT EXISTS ecr (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  title text NOT NULL,
  reason text NOT NULL DEFAULT '',
  priority text NOT NULL CHECK (priority IN ('low', 'medium', 'high', 'critical')),
  state text NOT NULL CHECK (state IN ('draft', 'submitted', 'under_review', 'approved', 'rejected', 'cancelled')),
  requested_by text NOT NULL,
  decided_by text NOT NULL DEFAULT '',
  decision_reason text NOT NULL DEFAULT '',
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number)
);
CREATE TABLE IF NOT EXISTS ecr_product (
  tenant_id uuid NOT NULL,
  ecr_id uuid NOT NULL,
  product_id text NOT NULL,
  PRIMARY KEY (tenant_id, ecr_id, product_id),
  FOREIGN KEY (tenant_id, ecr_id) REFERENCES ecr (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS "IX-ENG-08" ON ecr_product (tenant_id, product_id);
ALTER TABLE eco ADD COLUMN IF NOT EXISTS ecr_id uuid;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'eco_ecr_fk' AND conrelid = 'eco'::regclass) THEN
    ALTER TABLE eco ADD CONSTRAINT eco_ecr_fk FOREIGN KEY (tenant_id, ecr_id) REFERENCES ecr (tenant_id, id);
  END IF;
END
$$;
-- At most one live ECO per ECR: a rejected or cancelled ECO frees the request for another.
CREATE UNIQUE INDEX IF NOT EXISTS "IX-ENG-09" ON eco (tenant_id, ecr_id) WHERE ecr_id IS NOT NULL AND state NOT IN ('rejected', 'cancelled');
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['ecr', 'ecr_product'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
  END LOOP;
END
$$;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ecr TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON ecr_product TO passage_app';
  END IF;
END
$$;
