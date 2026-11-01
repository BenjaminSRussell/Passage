DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = 'passage_outbox_receiving') THEN
    BEGIN
      CREATE PUBLICATION passage_outbox_receiving;
    EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
    END;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_publication_tables
    WHERE pubname = 'passage_outbox_receiving' AND schemaname = current_schema() AND tablename = 'outbox'
  ) THEN
    EXECUTE format('ALTER PUBLICATION passage_outbox_receiving ADD TABLE %I.outbox', current_schema());
  END IF;
END
$$;
