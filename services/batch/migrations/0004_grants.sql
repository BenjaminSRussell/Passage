-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies. The outbox is insert-only (Debezium relays it).
-- batch_processed_rows and consumer_inbox are claim-only (INSERT ... ON CONFLICT DO
-- NOTHING); workflow_definition, batch_run, notify_template and event_schema are
-- append-only. Tables the store updates or upserts get SELECT, INSERT, UPDATE.
-- idempotency_keys is not used by this service. Skipped when the role does not exist
-- (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox, consumer_inbox, batch_processed_rows, notify_template TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON workflow_definition, batch_run, event_schema TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON workflow_instance, workflow_work_item, batch_job, batch_checkpoints, notify_outbound_message, notify_preference TO passage_app';
  END IF;
END
$$;
