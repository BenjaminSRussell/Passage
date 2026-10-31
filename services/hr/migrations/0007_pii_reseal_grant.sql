-- hr reseal-pii (#335) rewrites sealed P3 values under the current fieldcrypt key.
-- The grant is column-scoped: the app role still cannot move a row to another
-- worker or tenant, and worker_pii keeps no DELETE grant.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (national_id_sealed, bank_account_sealed) ON worker_pii TO passage_app';
  END IF;
END
$$;
