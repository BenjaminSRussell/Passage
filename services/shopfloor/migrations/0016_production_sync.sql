-- PSG-25 slice 17: shop floor follows production's operation status (FR-25.3). Production (w21)
-- emits dev.passage.production.operation.statuschanged.v1 when an operation is finished (by the
-- finish command or the last report as finished) and again when it is reopened. The consumer keeps
-- the latest status per (order, operation) here, apart from shop_job, so an event that arrives
-- before the schedule has projected the job is not lost. Job starts at or after the finish are
-- refused (SF-0157) until production reopens the operation.
--
-- consumer_inbox is the platform eventing inbox (event id per consumer, once). Both tables carry
-- tenant_id with forced row-level security. New tables, so no lock on a live table.

CREATE TABLE IF NOT EXISTS consumer_inbox (
  tenant_id uuid NOT NULL,
  consumer text NOT NULL,
  event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, consumer, event_id)
);
ALTER TABLE consumer_inbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE consumer_inbox FORCE ROW LEVEL SECURITY;
CREATE POLICY consumer_inbox_tenant_access ON consumer_inbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- order_ref is the production order id (lower case) and operation_ref the operation number as
-- text, the references the schedule projects on shop_job. Events are ordered by (order_version,
-- changed_at): the order version production bumps under the order lock on every finish and
-- reopen, then the event's occurredAt. An older event never overwrites a newer one (Kafka
-- redelivery, a dead-letter replay, two production commands racing on one order).
CREATE TABLE IF NOT EXISTS operation_production_status (
  tenant_id uuid NOT NULL,
  order_ref text NOT NULL CHECK (char_length(order_ref) BETWEEN 1 AND 64),
  operation_ref text NOT NULL CHECK (char_length(operation_ref) BETWEEN 1 AND 64),
  finished boolean NOT NULL,
  production_status text NOT NULL CHECK (char_length(production_status) BETWEEN 1 AND 32),
  changed_at timestamptz NOT NULL,
  order_version integer NOT NULL DEFAULT 0,
  actor text NOT NULL DEFAULT '',
  event_id text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, order_ref, operation_ref)
);
ALTER TABLE operation_production_status ENABLE ROW LEVEL SECURITY;
ALTER TABLE operation_production_status FORCE ROW LEVEL SECURITY;
CREATE POLICY operation_production_status_tenant_access ON operation_production_status AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON consumer_inbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON operation_production_status TO passage_app';
  END IF;
END
$$;
