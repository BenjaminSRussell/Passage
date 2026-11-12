-- fieldservice publishes its outbox through the outbox-fieldservice Debezium connector (publication passage_outbox_fieldservice),
-- so passage.service.workorder.completed and the other service events reach Kafka (#45). pg_publication_rel names the published
-- relations without opening them, so a dropped sibling test schema cannot fail the check (see budgeting 0008).
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = 'passage_outbox_fieldservice') THEN
    BEGIN
      CREATE PUBLICATION passage_outbox_fieldservice;
    EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
    END;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_publication_rel pr JOIN pg_publication p ON p.oid = pr.prpubid
    WHERE p.pubname = 'passage_outbox_fieldservice' AND pr.prrelid = to_regclass(format('%I.outbox', current_schema()))
  ) THEN
    EXECUTE format('ALTER PUBLICATION passage_outbox_fieldservice ADD TABLE %I.outbox', current_schema());
  END IF;
END
$$;
