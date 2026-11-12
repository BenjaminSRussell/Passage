-- Requires CREATE on the database and ownership of outbox; the Debezium connector must start after this migration.
-- Reads pg_publication_rel, not pg_publication_tables: the publication is database-wide, and pg_publication_tables opens every published
-- relation, so it fails with "could not open relation with OID" when another session has just dropped a schema whose outbox is still
-- published. pg_publication_rel only names the relations. Re-running this on a database that already ran it changes nothing.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = 'passage_outbox_batch') THEN
    BEGIN
      CREATE PUBLICATION passage_outbox_batch;
    EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
    END;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_publication_rel pr JOIN pg_publication p ON p.oid = pr.prpubid
    WHERE p.pubname = 'passage_outbox_batch' AND pr.prrelid = to_regclass(format('%I.outbox', current_schema()))
  ) THEN
    EXECUTE format('ALTER PUBLICATION passage_outbox_batch ADD TABLE %I.outbox', current_schema());
  END IF;
END
$$;
