DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT INSERT ON cost_calculation TO passage_app';
    EXECUTE 'GRANT UPDATE (status) ON cost_calculation TO passage_app';
  END IF;
END
$$;
