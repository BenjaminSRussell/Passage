-- Least-privilege grants for vendor_group (0011). Groups are created and read;
-- the code is never updated or deleted because ledger lines carry it.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON vendor_group TO passage_app';
  END IF;
END
$$;
