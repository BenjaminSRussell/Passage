alter table batch_run add column if not exists claimed_until timestamptz;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (started_at, claimed_until) ON batch_run TO passage_app';
  END IF;
END
$$;
