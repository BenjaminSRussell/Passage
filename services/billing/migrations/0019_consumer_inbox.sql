-- The platform eventing inbox (eventing.NewPgInbox): one row per consumer and event id, written in the transaction that applies the
-- event, so an Idempotent consumer applies a redelivered event once.
CREATE TABLE IF NOT EXISTS consumer_inbox (
  tenant_id uuid NOT NULL,
  consumer text NOT NULL,
  event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, consumer, event_id)
);
ALTER TABLE consumer_inbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE consumer_inbox FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS consumer_inbox_tenant_access ON consumer_inbox;
CREATE POLICY consumer_inbox_tenant_access ON consumer_inbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON consumer_inbox TO passage_app';
  END IF;
END
$$;
