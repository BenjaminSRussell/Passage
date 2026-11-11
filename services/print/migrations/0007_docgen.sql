-- Docgen (#46): document templates, standard text blocks with language versions, and the
-- output rules that say where the original and the copies of a document go.
CREATE TABLE IF NOT EXISTS print_document_template (
  tenant_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z][A-Z0-9_-]{1,39}$'),
  name text NOT NULL,
  document_type text NOT NULL CHECK (document_type IN ('PURCHASE_ORDER', 'CUSTOMER_INVOICE', 'PACKING_SLIP', 'REMITTANCE_ADVICE', 'PERIODIC_STATEMENT', 'PAYMENT_NOTICE', 'BALANCE_CONFIRMATION')),
  is_default boolean NOT NULL DEFAULT false,
  version integer NOT NULL DEFAULT 1,
  config jsonb NOT NULL DEFAULT '{}'::jsonb,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, code)
);
CREATE UNIQUE INDEX IF NOT EXISTS print_document_template_default ON print_document_template (tenant_id, document_type) WHERE is_default;
ALTER TABLE print_document_template ENABLE ROW LEVEL SECURITY;
ALTER TABLE print_document_template FORCE ROW LEVEL SECURITY;
CREATE POLICY print_document_template_tenant_access ON print_document_template AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS print_text_block (
  tenant_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z][A-Z0-9_-]{1,39}$'),
  language text NOT NULL CHECK (language ~ '^[a-z]{2}(-[A-Z]{2})?$'),
  body text NOT NULL CHECK (length(btrim(body)) > 0),
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, code, language)
);
ALTER TABLE print_text_block ENABLE ROW LEVEL SECURITY;
ALTER TABLE print_text_block FORCE ROW LEVEL SECURITY;
CREATE POLICY print_text_block_tenant_access ON print_text_block AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS print_output_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  document_type text NOT NULL CHECK (document_type IN ('PURCHASE_ORDER', 'CUSTOMER_INVOICE', 'PACKING_SLIP', 'REMITTANCE_ADVICE', 'PERIODIC_STATEMENT', 'PAYMENT_NOTICE', 'BALANCE_CONFIRMATION')),
  legal_entity_id text,
  account_id text,
  copy_kind text NOT NULL CHECK (copy_kind IN ('ORIGINAL', 'COPY')),
  channel text NOT NULL CHECK (channel IN ('PRINTER', 'EMAIL', 'EINVOICE', 'ARCHIVE', 'PORTAL')),
  destination text NOT NULL DEFAULT '',
  copies integer NOT NULL DEFAULT 1 CHECK (copies BETWEEN 1 AND 9),
  sequence integer NOT NULL CHECK (sequence >= 1),
  idempotency_key text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE print_output_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE print_output_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY print_output_rule_tenant_access ON print_output_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON print_document_template, print_text_block, print_output_rule TO passage_app';
  END IF;
END
$$;
