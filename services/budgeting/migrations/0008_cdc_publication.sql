-- The publication is database-wide while every test schema (and, in dev, every tenant schema) has its own outbox, so another
-- session can drop a schema whose table is still published while this one runs. Reading pg_publication_tables opens every
-- published relation and fails with "could not open relation with OID" when it meets one that was just dropped; pg_publication_rel
-- names the relations without opening them, so the check below reads that instead.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = 'passage_outbox_budgeting') THEN
    BEGIN
      CREATE PUBLICATION passage_outbox_budgeting;
    EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
    END;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_publication_rel pr JOIN pg_publication p ON p.oid = pr.prpubid
    WHERE p.pubname = 'passage_outbox_budgeting' AND pr.prrelid = to_regclass(format('%I.outbox', current_schema()))
  ) THEN
    EXECUTE format('ALTER PUBLICATION passage_outbox_budgeting ADD TABLE %I.outbox', current_schema());
  END IF;
END
$$;
