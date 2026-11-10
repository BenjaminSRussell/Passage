-- #496: what a posted packing slip delivered is reported to sales'
-- POST /v1/sales/orders/{id}:record-progress (kind DELIVERED) outside the post
-- transaction. The post writes one PENDING report per (slip, order) with the
-- request body frozen and a stable Idempotency-Key, so every attempt sends the
-- same body and key and sales answers a repeat from its stored report. The
-- reporter (cmd/fulfillment, SALES_URL) claims due rows with a lease (attempts
-- is bumped and next_attempt_at moved past the lease), sends them signed as
-- service:fulfillment-sales-report, and records REPORTED, PENDING again with a
-- back-off (outage, 5xx, throttling, a refused token) or PARKED (sales refused
-- the report: 4xx, or the attempts ran out). A parked report waits for
-- :retry. Cancelling the slip before it was reported withdraws it (WITHDRAWN);
-- one already reported stays REPORTED with cancelled_at set, because sales has
-- no reversal of a delivery (returns are wave 30).
CREATE TABLE IF NOT EXISTS sales_delivery_report (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  packing_slip_id uuid NOT NULL,
  source_ref text NOT NULL CHECK (length(source_ref) BETWEEN 1 AND 100),
  order_id uuid NOT NULL,
  idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
  body jsonb NOT NULL,
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'REPORTED', 'PARKED', 'WITHDRAWN')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_status integer,
  last_error text CHECK (last_error IS NULL OR length(last_error) <= 1000),
  reported_at timestamptz,
  cancelled_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, packing_slip_id, order_id),
  FOREIGN KEY (tenant_id, packing_slip_id) REFERENCES packing_slip (tenant_id, id),
  CHECK ((status = 'REPORTED') = (reported_at IS NOT NULL)),
  CHECK (status <> 'WITHDRAWN' OR cancelled_at IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS sales_delivery_report_pending_idx ON sales_delivery_report (tenant_id, next_attempt_at) WHERE status = 'PENDING';
CREATE INDEX IF NOT EXISTS sales_delivery_report_status_idx ON sales_delivery_report (tenant_id, status, created_at DESC);

ALTER TABLE sales_delivery_report ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_delivery_report FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS sales_delivery_report_tenant_access ON sales_delivery_report;
CREATE POLICY sales_delivery_report_tenant_access ON sales_delivery_report AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS sales_delivery_report_legal_entity_scope ON sales_delivery_report;
CREATE POLICY sales_delivery_report_legal_entity_scope ON sales_delivery_report AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The reporter finds the tenants with due reports here: the report table is
-- tenant-scoped (FORCE RLS binds its owner too), so a trigger keeps this
-- cross-tenant index and only the SECURITY DEFINER function below reads it.
-- passage_app has no grant on it.
CREATE TABLE IF NOT EXISTS sales_delivery_report_due (
  tenant_id uuid NOT NULL,
  report_id uuid NOT NULL,
  next_attempt_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, report_id)
);
CREATE INDEX IF NOT EXISTS sales_delivery_report_due_next_idx ON sales_delivery_report_due (next_attempt_at);
DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION sales_delivery_report_track_due() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    BEGIN
      IF NEW.status = 'PENDING' THEN
        INSERT INTO sales_delivery_report_due (tenant_id, report_id, next_attempt_at) VALUES (NEW.tenant_id, NEW.id, NEW.next_attempt_at)
        ON CONFLICT (tenant_id, report_id) DO UPDATE SET next_attempt_at = EXCLUDED.next_attempt_at;
      ELSE
        DELETE FROM sales_delivery_report_due WHERE tenant_id = NEW.tenant_id AND report_id = NEW.id;
      END IF;
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION fulfillment_sales_report_due_tenants() RETURNS SETOF uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT DISTINCT tenant_id FROM sales_delivery_report_due WHERE next_attempt_at <= pg_catalog.now() $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION sales_delivery_report_track_due() FROM PUBLIC;
REVOKE ALL ON FUNCTION fulfillment_sales_report_due_tenants() FROM PUBLIC;
DROP TRIGGER IF EXISTS sales_delivery_report_due ON sales_delivery_report;
CREATE TRIGGER sales_delivery_report_due AFTER INSERT OR UPDATE OF status, next_attempt_at ON sales_delivery_report
  FOR EACH ROW EXECUTE FUNCTION sales_delivery_report_track_due();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON sales_delivery_report TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION fulfillment_sales_report_due_tenants() TO passage_app';
  END IF;
END
$$;
