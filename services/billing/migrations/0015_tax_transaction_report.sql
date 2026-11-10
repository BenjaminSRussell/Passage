-- #29: the output tax of an invoice or credit note booked to the GL is recorded with the tax
-- service (POST /v1/tax/transactions) outside the booking transaction. MarkGLBooked writes one
-- PENDING report per document in the same transaction that stores the ledger entry, with the
-- request body frozen (tax per code as billing computed it, negative on a credit note, fresh
-- transaction ids), so every attempt from any replica sends the same body and tax answers a
-- repeat from its stored transactions. The reporter (cmd/billing, TAX_URL) claims due rows with a
-- lease, sends them signed as service:billing-tax-report, and records REPORTED, PENDING again
-- with a back-off (outage, 5xx, throttling, a refused token) or PARKED (tax refused the body, or
-- the attempts ran out). A parked report keeps its plain reason in last_error.
CREATE TABLE IF NOT EXISTS tax_transaction_report (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  original_invoice_id uuid,
  kind text NOT NULL CHECK (kind IN ('INVOICE', 'CREDIT_NOTE')),
  source_ref text NOT NULL CHECK (length(source_ref) BETWEEN 1 AND 100),
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
  UNIQUE (tenant_id, invoice_id, kind),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoice (tenant_id, id),
  CHECK ((kind = 'CREDIT_NOTE') = (original_invoice_id IS NOT NULL)),
  CHECK ((status = 'REPORTED') = (reported_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS tax_transaction_report_pending_idx ON tax_transaction_report (tenant_id, next_attempt_at) WHERE status = 'PENDING';
CREATE INDEX IF NOT EXISTS tax_transaction_report_status_idx ON tax_transaction_report (tenant_id, status, created_at DESC);

ALTER TABLE tax_transaction_report ENABLE ROW LEVEL SECURITY;
ALTER TABLE tax_transaction_report FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tax_transaction_report_tenant_access ON tax_transaction_report;
CREATE POLICY tax_transaction_report_tenant_access ON tax_transaction_report AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS tax_transaction_report_legal_entity_scope ON tax_transaction_report;
CREATE POLICY tax_transaction_report_legal_entity_scope ON tax_transaction_report AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Cross-tenant index of the due reports, maintained by a SECURITY DEFINER trigger and read only
-- by the SECURITY DEFINER function below; passage_app has no grant on it.
CREATE TABLE IF NOT EXISTS tax_transaction_report_due (
  tenant_id uuid NOT NULL,
  report_id uuid NOT NULL,
  next_attempt_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, report_id)
);
CREATE INDEX IF NOT EXISTS tax_transaction_report_due_next_idx ON tax_transaction_report_due (next_attempt_at);
DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION tax_transaction_report_track_due() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    BEGIN
      IF NEW.status = 'PENDING' THEN
        INSERT INTO tax_transaction_report_due (tenant_id, report_id, next_attempt_at) VALUES (NEW.tenant_id, NEW.id, NEW.next_attempt_at)
        ON CONFLICT (tenant_id, report_id) DO UPDATE SET next_attempt_at = EXCLUDED.next_attempt_at;
      ELSE
        DELETE FROM tax_transaction_report_due WHERE tenant_id = NEW.tenant_id AND report_id = NEW.id;
      END IF;
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION billing_tax_report_due_tenants() RETURNS SETOF uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT DISTINCT tenant_id FROM tax_transaction_report_due WHERE next_attempt_at <= pg_catalog.now() $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION tax_transaction_report_track_due() FROM PUBLIC;
REVOKE ALL ON FUNCTION billing_tax_report_due_tenants() FROM PUBLIC;
DROP TRIGGER IF EXISTS tax_transaction_report_due ON tax_transaction_report;
CREATE TRIGGER tax_transaction_report_due AFTER INSERT OR UPDATE OF status, next_attempt_at ON tax_transaction_report
  FOR EACH ROW EXECUTE FUNCTION tax_transaction_report_track_due();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON tax_transaction_report TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION billing_tax_report_due_tenants() TO passage_app';
  END IF;
END
$$;
