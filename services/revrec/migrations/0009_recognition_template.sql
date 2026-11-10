-- Recognition templates (29-4): how the revenue of a product is recognized when its revenue contract is opened from a
-- posted invoice. A product with no template is recognized at the posting date (POINT_IN_TIME); a RATABLE template spreads
-- the invoiced amount over term_months from the posting date.
CREATE TABLE IF NOT EXISTS recognition_template (
  tenant_id uuid NOT NULL,
  product_id uuid NOT NULL,
  method text NOT NULL CHECK (method IN ('POINT_IN_TIME', 'RATABLE')),
  term_months integer CHECK (term_months BETWEEN 1 AND 120),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, product_id),
  CHECK ((method = 'RATABLE') = (term_months IS NOT NULL))
);
ALTER TABLE recognition_template ENABLE ROW LEVEL SECURITY;
ALTER TABLE recognition_template FORCE ROW LEVEL SECURITY;
CREATE POLICY recognition_template_tenant_access ON recognition_template AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON recognition_template TO passage_app';
  END IF;
END
$$;
