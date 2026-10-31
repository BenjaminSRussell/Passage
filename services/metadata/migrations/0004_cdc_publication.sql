-- Requires CREATE on the database and ownership of outbox; the Debezium connector must start after this migration.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = 'passage_outbox_metadata') THEN
    BEGIN
      CREATE PUBLICATION passage_outbox_metadata;
    EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
    END;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_publication_tables
    WHERE pubname = 'passage_outbox_metadata' AND schemaname = current_schema() AND tablename = 'outbox'
  ) THEN
    EXECUTE format('ALTER PUBLICATION passage_outbox_metadata ADD TABLE %I.outbox', current_schema());
  END IF;
END
$$;
