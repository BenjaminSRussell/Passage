-- The store never deletes from any treasury table, so passage_app loses DELETE on
-- treasury_facility, idempotency_keys, bank_account, bank_check and
-- treasury_facility_instrument. No table keeps DELETE. Skipped when the role does not
-- exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'REVOKE DELETE ON treasury_facility, idempotency_keys, bank_account, bank_check, treasury_facility_instrument FROM passage_app';
  END IF;
END
$$;
