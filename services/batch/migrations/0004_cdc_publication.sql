DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = 'passage_outbox_batch') THEN
    CREATE PUBLICATION passage_outbox_batch;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_publication_tables
    WHERE pubname = 'passage_outbox_batch' AND schemaname = current_schema() AND tablename = 'outbox'
  ) THEN
    EXECUTE format('ALTER PUBLICATION passage_outbox_batch ADD TABLE %I.outbox', current_schema());
  END IF;
END
$$;
