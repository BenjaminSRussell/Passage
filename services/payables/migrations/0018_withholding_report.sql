-- #13: the tax a payment run withheld is recorded with the tax service through an outbox instead of during post-journal. post-journal
-- queues one report per run with the entries frozen (stable entry ids, so tax records each once); the reporter (cmd/payables,
-- TAX_URL) claims due rows with a lease, sends them signed as service:payables-withholding, and records REPORTED, PENDING again with
-- a back-off (outage, 5xx, throttling, a refused token), or PARKED (tax refused the entries, or the attempts ran out). A parked report
-- puts its reason on the run (ap_payment_run.withholding_record_error, AP-RUN-019); repeating post-journal queues it again.
CREATE TABLE IF NOT EXISTS ap_withholding_report (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  run_id uuid NOT NULL,
  body jsonb NOT NULL,
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'REPORTED', 'PARKED')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_status integer,
  last_error text CHECK (last_error IS NULL OR length(last_error) <= 1000),
  reported_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, run_id),
  CHECK ((status = 'REPORTED') = (reported_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS ap_withholding_report_pending_idx ON ap_withholding_report (tenant_id, next_attempt_at) WHERE status = 'PENDING';
ALTER TABLE ap_withholding_report ENABLE ROW LEVEL SECURITY;
ALTER TABLE ap_withholding_report FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ap_withholding_report_tenant_access ON ap_withholding_report;
CREATE POLICY ap_withholding_report_tenant_access ON ap_withholding_report AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS ap_withholding_report_legal_entity_scope ON ap_withholding_report;
CREATE POLICY ap_withholding_report_legal_entity_scope ON ap_withholding_report AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The reporter finds the tenants with due reports through this cross-tenant index, kept by a trigger and read only by the SECURITY
-- DEFINER function (FORCE RLS binds the table owner too). passage_app has no grant on the index. Its tenant column is report_tenant, not
-- tenant_id: it is the one deliberately cross-tenant table, outside the tenant RLS rule the tests assert for every tenant_id table.
CREATE TABLE IF NOT EXISTS ap_withholding_report_due (
  report_tenant uuid NOT NULL,
  report_id uuid NOT NULL,
  next_attempt_at timestamptz NOT NULL,
  PRIMARY KEY (report_tenant, report_id)
);
CREATE INDEX IF NOT EXISTS ap_withholding_report_due_next_idx ON ap_withholding_report_due (next_attempt_at);
DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION ap_withholding_report_track_due() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    BEGIN
      IF NEW.status = 'PENDING' THEN
        INSERT INTO ap_withholding_report_due (report_tenant, report_id, next_attempt_at) VALUES (NEW.tenant_id, NEW.id, NEW.next_attempt_at)
        ON CONFLICT (report_tenant, report_id) DO UPDATE SET next_attempt_at = EXCLUDED.next_attempt_at;
      ELSE
        DELETE FROM ap_withholding_report_due WHERE report_tenant = NEW.tenant_id AND report_id = NEW.id;
      END IF;
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION payables_withholding_report_due_tenants() RETURNS SETOF uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT DISTINCT report_tenant FROM ap_withholding_report_due WHERE next_attempt_at <= pg_catalog.now() $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION ap_withholding_report_track_due() FROM PUBLIC;
REVOKE ALL ON FUNCTION payables_withholding_report_due_tenants() FROM PUBLIC;
DROP TRIGGER IF EXISTS ap_withholding_report_due ON ap_withholding_report;
CREATE TRIGGER ap_withholding_report_due AFTER INSERT OR UPDATE OF status, next_attempt_at ON ap_withholding_report
  FOR EACH ROW EXECUTE FUNCTION ap_withholding_report_track_due();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ap_withholding_report TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION payables_withholding_report_due_tenants() TO passage_app';
  END IF;
END
$$;
