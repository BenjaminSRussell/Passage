-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies. The outbox is insert-only (Debezium relays it). audit_entry,
-- alert_rule and alert_inbox are append-only; audit_entry.seq is a bigserial, so
-- inserts need USAGE on its sequence. Skipped when the role does not exist
-- (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON audit_entry, alert_rule, alert_inbox TO passage_app';
    EXECUTE 'GRANT USAGE ON SEQUENCE audit_entry_seq_seq TO passage_app';
  END IF;
END
$$;
