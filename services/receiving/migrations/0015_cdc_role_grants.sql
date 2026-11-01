DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc';
  END IF;
END
$$;
