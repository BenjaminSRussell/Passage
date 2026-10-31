-- The debezium_cdc role (REPLICATION, BYPASSRLS) is created with the database. It reads outbox for the
-- initial snapshot and streams it through the pre-created publication; skipped where the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc';
  END IF;
END
$$;
