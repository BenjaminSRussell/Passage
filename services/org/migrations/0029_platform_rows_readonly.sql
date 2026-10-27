-- passage_app may not write the seeded platform rows (tenant_id = the nil UUID), even
-- if a caller presents the nil UUID as its tenant. The owner role that seeds them is
-- not named in these policies. Skipped when the role does not exist.
DO $$
DECLARE
  t text;
  sys constant uuid := '00000000-0000-0000-0000-000000000000';
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    FOREACH t IN ARRAY ARRAY['country', 'currency', 'region', 'language', 'unit_of_measure', 'uom_conversion'] LOOP
      EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR INSERT TO passage_app WITH CHECK (tenant_id <> %L::uuid)', t || '_no_sys_insert', t, sys);
      EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE TO passage_app USING (tenant_id <> %L::uuid) WITH CHECK (tenant_id <> %L::uuid)', t || '_no_sys_update', t, sys, sys);
      EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE TO passage_app USING (tenant_id <> %L::uuid)', t || '_no_sys_delete', t, sys);
    END LOOP;
  END IF;
END
$$;
