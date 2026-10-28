-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies. The outbox is insert-only (Debezium relays it); bank_statement,
-- bank_statement_line, cash_forecast_entry, treasury_loan and
-- treasury_loan_schedule_line are append-only. Skipped when the role does not exist
-- (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON bank_statement, bank_statement_line, cash_forecast_entry, treasury_loan, treasury_loan_schedule_line TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON treasury_facility, idempotency_keys, bank_account, bank_check, treasury_facility_instrument TO passage_app';
  END IF;
END
$$;
