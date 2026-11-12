-- #35 round 2, 35-2: a SUBMITTING claim so two concurrent :submit calls cannot both reach the access point, the Factur-X
-- mandate, and the customers an e-invoice is built for automatically when their invoice posts.

-- The status check and the mandate check were created inline (generated names), so find them by their definition.
DO $do$
DECLARE
  c record;
BEGIN
  FOR c IN SELECT conname FROM pg_constraint
    WHERE conrelid = 'einvoice'::regclass AND contype = 'c'
      AND (pg_get_constraintdef(oid) LIKE '%status%' OR pg_get_constraintdef(oid) LIKE '%mandate%')
  LOOP
    EXECUTE format('ALTER TABLE einvoice DROP CONSTRAINT %I', c.conname);
  END LOOP;
END
$do$;

ALTER TABLE einvoice ADD CONSTRAINT einvoice_status_check
  CHECK (status IN ('VALID', 'INVALID', 'SUBMITTING', 'SUBMITTED', 'ACCEPTED', 'REJECTED'));
ALTER TABLE einvoice ADD CONSTRAINT einvoice_mandate_check
  CHECK (mandate IN ('PEPPOL', 'IT_SDI', 'MX_CFDI', 'FACTURX'));
-- The claim: when the access point call began and the status to return to when it fails.
ALTER TABLE einvoice ADD COLUMN IF NOT EXISTS submitting_at timestamptz;
ALTER TABLE einvoice ADD COLUMN IF NOT EXISTS submit_from text CHECK (submit_from IN ('VALID', 'REJECTED'));

-- The customers of a legal entity that are e-invoiced under a mandate when their invoice posts. auto_submit also hands the
-- document to the access point once it is VALID.
CREATE TABLE IF NOT EXISTS einvoice_mandate_customer (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  mandate text NOT NULL CHECK (mandate IN ('PEPPOL', 'IT_SDI', 'MX_CFDI', 'FACTURX')),
  auto_submit boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, customer_id, mandate)
);

DO $do$
DECLARE
  t text := 'einvoice_mandate_customer';
BEGIN
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
  EXECUTE format($p$CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t || '_tenant_access', t);
  EXECUTE format($p$CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting('passage.legal_entity_scopes', true) = '*'
    OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))$p$, t || '_legal_entity_scope', t);
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO passage_app', t);
  END IF;
END
$do$;
