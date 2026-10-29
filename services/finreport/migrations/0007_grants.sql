-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies. The outbox is insert-only (Debezium relays it).
-- reporting_balance and reporting_hwm are the read model: this service only reads
-- them (the consumer that fills them is not built yet). statement_cell,
-- audit_trail_entry, reporting_journal_line and idempotency_keys are append-only
-- (inserts use ON CONFLICT DO NOTHING). Tables the store updates or row-locks
-- (FOR UPDATE needs UPDATE) get SELECT, INSERT, UPDATE. Skipped when the role does
-- not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT ON reporting_balance, reporting_hwm TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON statement_cell, audit_trail_entry, reporting_journal_line, idempotency_keys TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON report_definition, report_hierarchy, report_schedule, statement_run TO passage_app';
  END IF;
END
$$;
