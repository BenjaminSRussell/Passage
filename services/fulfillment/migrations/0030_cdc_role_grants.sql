DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_fulfillment') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc_fulfillment', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc_fulfillment';
  ELSE
    RAISE WARNING 'role debezium_cdc_fulfillment does not exist: the outbox-fulfillment connector cannot read outbox; create the role, then delete migration 0030 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
