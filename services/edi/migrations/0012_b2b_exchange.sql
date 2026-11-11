-- #35 B2B batch (35-1 to 35-5): event-driven 856/810 outbound, the e-invoice store and mandate framework, punchout sessions,
-- cXML inbound exchange and self-billing intake. Every table is tenant-scoped; the ones that belong to one legal entity also
-- carry the restrictive legal-entity scope policy the trade profile uses (0005).

-- 35-1: the transaction sets a partner receives automatically when a shipment ships or an invoice posts.
CREATE TABLE IF NOT EXISTS partner_auto_outbound (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  transaction_sets text[] NOT NULL CHECK (cardinality(transaction_sets) BETWEEN 1 AND 4
    AND transaction_sets <@ ARRAY['856', '810', 'DESADV', 'INVOIC']::text[]),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, partner_id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);

-- 35-2: seller and buyer master data an e-invoice needs and billing does not carry (address, VAT id, endpoint), and the
-- e-invoice itself, one per (invoice, mandate).
CREATE TABLE IF NOT EXISTS einvoice_party (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  role text NOT NULL CHECK (role IN ('SELLER', 'BUYER')),
  subject_id uuid NOT NULL,
  party jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, role, subject_id)
);

CREATE TABLE IF NOT EXISTS einvoice (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  source_invoice_id uuid NOT NULL,
  source_number text NOT NULL,
  mandate text NOT NULL CHECK (mandate IN ('PEPPOL', 'IT_SDI', 'MX_CFDI')),
  format text NOT NULL,
  document_type text NOT NULL CHECK (document_type IN ('INVOICE', 'CREDIT_NOTE')),
  status text NOT NULL CHECK (status IN ('VALID', 'INVALID', 'SUBMITTED', 'ACCEPTED', 'REJECTED')),
  xml text,
  issues jsonb NOT NULL DEFAULT '[]'::jsonb,
  submission_ref text,
  response_text text,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, source_invoice_id, mandate)
);
CREATE INDEX IF NOT EXISTS einvoice_status ON einvoice (tenant_id, status, created_at DESC);

-- 35-3: the supplier's punchout settings (BR-35.13 inputs) and one session per catalog visit. The session secret is stored as
-- a hash; the return URL carries the secret and is the only credential of the supplier's browser post.
CREATE TABLE IF NOT EXISTS punchout_supplier (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  setup_url text NOT NULL CHECK (setup_url ~ '^https?://'),
  buyer_identity text NOT NULL CHECK (char_length(buyer_identity) BETWEEN 1 AND 80),
  supplier_identity text NOT NULL CHECK (char_length(supplier_identity) BETWEEN 1 AND 80),
  shared_secret_ref text NOT NULL DEFAULT '',
  currencies text[] NOT NULL CHECK (cardinality(currencies) >= 1),
  uom_map jsonb NOT NULL DEFAULT '{}'::jsonb,
  tolerance_pct numeric(5, 2) NOT NULL DEFAULT 0 CHECK (tolerance_pct BETWEEN 0 AND 20),
  reference_prices jsonb NOT NULL DEFAULT '{}'::jsonb,
  session_minutes integer NOT NULL DEFAULT 60 CHECK (session_minutes BETWEEN 5 AND 480),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, partner_id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS punchout_session (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  requester_id uuid NOT NULL,
  format text NOT NULL CHECK (format IN ('CXML', 'OCI')),
  status text NOT NULL CHECK (status IN ('OPEN', 'RETURNED', 'REJECTED', 'CANCELLED')),
  secret_hash text NOT NULL,
  buyer_cookie text NOT NULL,
  start_url text,
  expires_at timestamptz NOT NULL,
  returned_at timestamptz,
  cart jsonb,
  issues jsonb NOT NULL DEFAULT '[]'::jsonb,
  requisition_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS punchout_session_requester ON punchout_session (tenant_id, requester_id, created_at DESC);

-- 35-4: the cXML identity of a partner (network identity, shared secret hash, the endpoint id of its URL) and the payloads
-- already seen (BR-35.14): the same payloadID with the same content replays the first answer, other content is EDI-0135.
CREATE TABLE IF NOT EXISTS partner_cxml (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  endpoint_id uuid NOT NULL,
  network_identity text NOT NULL CHECK (char_length(network_identity) BETWEEN 1 AND 80),
  secret_hash text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, partner_id),
  UNIQUE (endpoint_id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS cxml_payload (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  payload_id text NOT NULL,
  doc_type text NOT NULL,
  content_hash text NOT NULL,
  response_code integer NOT NULL,
  response_text text NOT NULL,
  message_id uuid,
  received_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, partner_id, payload_id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);

-- 35-5: self-billing. The shipments a partner may bill itself for (registered when an 856 or DESADV is generated, or by
-- the integration), the partner's settings, the documents received (INVOIC 389) with their matched lines, and the disputes.
CREATE TABLE IF NOT EXISTS self_billing_config (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  quantity_tolerance numeric(18, 6) NOT NULL DEFAULT 0 CHECK (quantity_tolerance >= 0),
  price_tolerance numeric(18, 6) NOT NULL DEFAULT 0.01 CHECK (price_tolerance >= 0),
  wait_days integer NOT NULL DEFAULT 30 CHECK (wait_days BETWEEN 1 AND 120),
  tax_rate_pct numeric(7, 4) NOT NULL DEFAULT 0 CHECK (tax_rate_pct BETWEEN 0 AND 100),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, partner_id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS self_billing_shipment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  delivery_note text NOT NULL,
  po_line text NOT NULL,
  item text NOT NULL,
  quantity numeric(18, 6) NOT NULL,
  unit_price numeric(18, 6) NOT NULL,
  tax_rate_pct numeric(7, 4) NOT NULL DEFAULT 0,
  shipped_on date NOT NULL,
  source_shipment_id uuid,
  billed_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, partner_id, delivery_note, po_line, item),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS self_billing_shipment_open ON self_billing_shipment (tenant_id, partner_id, shipped_on) WHERE billed_by IS NULL;

CREATE TABLE IF NOT EXISTS self_billing_document (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  message_id uuid,
  number text NOT NULL,
  document_date text NOT NULL,
  currency text,
  status text NOT NULL CHECK (status IN ('MATCHED', 'DISPUTED', 'RESOLVED')),
  matched_lines integer NOT NULL DEFAULT 0,
  disputed_lines integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, partner_id, number),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS self_billing_line (
  tenant_id uuid NOT NULL,
  document_id uuid NOT NULL,
  line text NOT NULL,
  delivery_note text NOT NULL,
  po_line text NOT NULL,
  item text NOT NULL,
  quantity numeric(18, 6) NOT NULL,
  unit_price numeric(18, 6) NOT NULL,
  outcome text NOT NULL CHECK (outcome IN ('MATCHED', 'DIFFERENCE', 'UNMATCHED')),
  shipment_id uuid,
  differences jsonb NOT NULL DEFAULT '[]'::jsonb,
  PRIMARY KEY (tenant_id, document_id, line),
  FOREIGN KEY (tenant_id, document_id) REFERENCES self_billing_document (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS self_billing_dispute (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('DIFFERENCE', 'UNMATCHED', 'UNBILLED')),
  code text NOT NULL,
  document_id uuid,
  line text,
  shipment_id uuid,
  detail text NOT NULL,
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'ACCEPTED', 'REJECTED')),
  resolution text,
  resolved_by uuid,
  resolved_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);
-- A line or shipment is disputed once while the dispute is open, however often the sweep runs.
CREATE UNIQUE INDEX IF NOT EXISTS self_billing_dispute_line ON self_billing_dispute (tenant_id, document_id, line) WHERE document_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS self_billing_dispute_unbilled ON self_billing_dispute (tenant_id, shipment_id) WHERE kind = 'UNBILLED';
CREATE INDEX IF NOT EXISTS self_billing_dispute_open ON self_billing_dispute (tenant_id, status, created_at DESC);

DO $do$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['partner_auto_outbound', 'einvoice_party', 'einvoice', 'punchout_supplier', 'punchout_session', 'partner_cxml',
    'cxml_payload', 'self_billing_config', 'self_billing_shipment', 'self_billing_document', 'self_billing_line', 'self_billing_dispute']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t || '_tenant_access', t);
    IF t NOT IN ('cxml_payload', 'self_billing_line') THEN
      EXECUTE format($p$CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting('passage.legal_entity_scopes', true) = '*'
        OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))$p$, t || '_legal_entity_scope', t);
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
      EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO passage_app', t);
    END IF;
  END LOOP;
END
$do$;

-- The two public endpoints (the cXML URL of a partner and the punchout return URL of a session) are called by a partner's
-- network or browser with no Passage identity, so the tenant and partner come from this index, like transport_work: no
-- passage_app grant, kept by SECURITY DEFINER triggers and read only by the SECURITY DEFINER function below.
CREATE TABLE IF NOT EXISTS public_endpoint (
  kind text NOT NULL CHECK (kind IN ('CXML_ENDPOINT', 'PUNCHOUT_RETURN')),
  ref uuid NOT NULL,
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  PRIMARY KEY (kind, ref)
);

DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION edi_public_endpoint_track() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    DECLARE
      k text := CASE TG_TABLE_NAME WHEN 'partner_cxml' THEN 'CXML_ENDPOINT' ELSE 'PUNCHOUT_RETURN' END;
    BEGIN
      IF TG_TABLE_NAME = 'partner_cxml' THEN
        IF TG_OP IN ('UPDATE', 'DELETE') THEN
          DELETE FROM public_endpoint WHERE kind = k AND ref = OLD.endpoint_id;
        END IF;
        IF TG_OP <> 'DELETE' THEN
          INSERT INTO public_endpoint (kind, ref, tenant_id, partner_id) VALUES (k, NEW.endpoint_id, NEW.tenant_id, NEW.partner_id);
        END IF;
      ELSIF TG_OP = 'INSERT' THEN
        INSERT INTO public_endpoint (kind, ref, tenant_id, partner_id) VALUES (k, NEW.id, NEW.tenant_id, NEW.partner_id);
      END IF;
      IF TG_OP = 'DELETE' THEN
        RETURN OLD;
      END IF;
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION edi_public_endpoint(endpoint_kind text, endpoint uuid) RETURNS TABLE (tenant_id uuid, partner_id uuid)
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT e.tenant_id, e.partner_id FROM public_endpoint e WHERE e.kind = endpoint_kind AND e.ref = endpoint $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION edi_public_endpoint_track() FROM PUBLIC;
REVOKE ALL ON FUNCTION edi_public_endpoint(text, uuid) FROM PUBLIC;
DROP TRIGGER IF EXISTS partner_cxml_endpoint ON partner_cxml;
CREATE TRIGGER partner_cxml_endpoint AFTER INSERT OR UPDATE OR DELETE ON partner_cxml FOR EACH ROW EXECUTE FUNCTION edi_public_endpoint_track();
DROP TRIGGER IF EXISTS punchout_session_endpoint ON punchout_session;
CREATE TRIGGER punchout_session_endpoint AFTER INSERT ON punchout_session FOR EACH ROW EXECUTE FUNCTION edi_public_endpoint_track();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION edi_public_endpoint(text, uuid) TO passage_app';
  END IF;
END
$$;
