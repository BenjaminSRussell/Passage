DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc';
  ELSE
    RAISE WARNING 'role debezium_cdc does not exist: the outbox-receiving connector cannot read outbox; create the role, then delete migration 0015 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
