ALTER TABLE receiving_vendor_invoice
  ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'MANUAL' CHECK (source IN ('MANUAL', 'ERS')),
  ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'POSTED' CHECK (status IN ('POSTED', 'AUDIT_BLOCKED', 'AUDIT_REJECTED')),
  ADD COLUMN IF NOT EXISTS posted_by text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS released_by text,
  ADD COLUMN IF NOT EXISTS released_at timestamptz,
  ADD COLUMN IF NOT EXISTS rejected_by text,
  ADD COLUMN IF NOT EXISTS rejected_at timestamptz,
  ADD COLUMN IF NOT EXISTS reject_reason text;

ALTER TABLE receiving_vendor_invoice_line
  ADD COLUMN IF NOT EXISTS grir_cleared numeric(20,6);

CREATE TABLE IF NOT EXISTS receiving_ers_vendor (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  enabled boolean NOT NULL,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, vendor_id)
);

CREATE TABLE IF NOT EXISTS receiving_audit_policy (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  sample_rate_pct numeric(5,2) NOT NULL CHECK (sample_rate_pct >= 0 AND sample_rate_pct <= 100),
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id)
);

CREATE TABLE IF NOT EXISTS receiving_ers_vendor_history (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  old_enabled boolean NOT NULL,
  new_enabled boolean NOT NULL,
  changed_by text NOT NULL,
  changed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS receiving_audit_policy_history (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  legal_entity_id uuid NOT NULL,
  old_rate_pct numeric(5,2) NOT NULL,
  new_rate_pct numeric(5,2) NOT NULL,
  changed_by text NOT NULL,
  changed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);

CREATE OR REPLACE FUNCTION receiving_invoice_status_guard() RETURNS trigger AS $fn$
BEGIN
  IF OLD.status <> 'AUDIT_BLOCKED' THEN
    RAISE EXCEPTION 'invoice in status % cannot be updated', OLD.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END
$fn$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS receiving_vendor_invoice_status_guard ON receiving_vendor_invoice;
CREATE TRIGGER receiving_vendor_invoice_status_guard BEFORE UPDATE ON receiving_vendor_invoice
  FOR EACH ROW EXECUTE FUNCTION receiving_invoice_status_guard();

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['receiving_ers_vendor', 'receiving_audit_policy', 'receiving_ers_vendor_history', 'receiving_audit_policy_history'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
      EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
      EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
      EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
      EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
    END IF;
  END LOOP;
END
$$;

GRANT SELECT, INSERT, UPDATE ON receiving_ers_vendor, receiving_audit_policy TO passage_app;
GRANT SELECT, INSERT ON receiving_ers_vendor_history, receiving_audit_policy_history TO passage_app;
GRANT UPDATE (ledger_entry_id, status, released_by, released_at, rejected_by, rejected_at, reject_reason) ON receiving_vendor_invoice TO passage_app;
