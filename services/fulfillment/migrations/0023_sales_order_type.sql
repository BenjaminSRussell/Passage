-- Fulfillment slice 19: fulfillment follows sales' order.confirmed.v1 and keeps
-- the order type per sales order (and, from orderhold.applied / released, its holds), so release, drop-ship creation and the
-- JOURNAL refusals no longer depend on the caller naming the type (BR-27.7).
-- New tables only, so no lock on a live table.
--
-- consumer_inbox is the platform eventing inbox (one row per consumer and event
-- id, so a redelivery is processed once).
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

-- sales_order_type is the order type of a confirmed sales order. order_version
-- is the sales order version of the event; an older event never overwrites a
-- newer one (redelivery, dead-letter replay).
CREATE TABLE IF NOT EXISTS sales_order_type (
  tenant_id uuid NOT NULL,
  order_id uuid NOT NULL,
  order_type text NOT NULL CHECK (order_type IN ('ORDER', 'JOURNAL', 'SUBSCRIPTION', 'RETURNED')),
  order_version integer NOT NULL DEFAULT 0 CHECK (order_version >= 0),
  event_id text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, order_id)
);
ALTER TABLE sales_order_type ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_order_type FORCE ROW LEVEL SECURITY;
CREATE POLICY sales_order_type_tenant_access ON sales_order_type AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- sales_order_hold is the hold state of a sales order, one row per hold (sales
-- orderhold.applied.v1 sets it active, orderhold.released.v1 inactive). Posting a
-- packing slip is refused while an order has an active hold (FUL-0001, section
-- 5.28.5), and release skips the lines of such an order. order_version is the sales
-- order version of the event; an older event never overwrites a newer one.
CREATE TABLE IF NOT EXISTS sales_order_hold (
  tenant_id uuid NOT NULL,
  hold_id uuid NOT NULL,
  order_id uuid NOT NULL,
  hold_type text NOT NULL CHECK (char_length(hold_type) BETWEEN 1 AND 64),
  active boolean NOT NULL,
  order_version integer NOT NULL DEFAULT 0 CHECK (order_version >= 0),
  event_id text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, hold_id)
);
CREATE INDEX IF NOT EXISTS sales_order_hold_active_idx ON sales_order_hold (tenant_id, order_id) WHERE active;
ALTER TABLE sales_order_hold ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_order_hold FORCE ROW LEVEL SECURITY;
CREATE POLICY sales_order_hold_tenant_access ON sales_order_hold AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON consumer_inbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON sales_order_type, sales_order_hold TO passage_app';
  END IF;
END
$$;
