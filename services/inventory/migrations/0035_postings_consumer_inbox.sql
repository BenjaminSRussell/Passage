-- #480: the inventory postings consumer (cmd/inventory consume) books
-- receiving's receipt.posted and sales' DELIVERED orderprogress.recorded as
-- inventory transactions. consumer_inbox is the platform eventing inbox: one
-- row per consumer and event id, written after the transactions are booked
-- (each booking is idempotent by its own key), so a redelivery is skipped.
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
