-- #35 round 2, 35-4: outbound cXML to a network (OrderRequest, confirmations, ship notices, invoices) and the mapping of inbound
-- cXML documents onto purchasing and receiving.

-- Where we post to the partner's network, and who we are there. The secret is a reference into EDI_SECRETS_DIR (like the transports),
-- never the secret.
ALTER TABLE partner_cxml ADD COLUMN IF NOT EXISTS outbound_url text CHECK (outbound_url IS NULL OR outbound_url ~ '^https://');
ALTER TABLE partner_cxml ADD COLUMN IF NOT EXISTS our_identity text;
ALTER TABLE partner_cxml ADD COLUMN IF NOT EXISTS outbound_secret_ref text;
-- The vendor this partner is for: only purchase orders of this vendor are sent to it as OrderRequest.
ALTER TABLE partner_cxml ADD COLUMN IF NOT EXISTS vendor_id uuid;

-- What an inbound document became downstream: a purchase order that a ConfirmationRequest confirmed, a receiving ASN a
-- ShipNoticeRequest created. downstream_kind names the service object.
ALTER TABLE message ADD COLUMN IF NOT EXISTS downstream_kind text CHECK (downstream_kind IN ('PURCHASE_ORDER', 'ASN'));
ALTER TABLE message ADD COLUMN IF NOT EXISTS downstream_ref uuid;

-- The purchase orders sent to a partner as OrderRequest under their orderID (the PO number), so a confirmation or ship notice that
-- names the orderID finds the PO again, with the vendor and legal entity it was sent for.
CREATE TABLE IF NOT EXISTS cxml_order (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  order_id text NOT NULL CHECK (char_length(order_id) BETWEEN 1 AND 64),
  purchase_order_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  sent_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, partner_id, order_id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS cxml_order_po ON cxml_order (tenant_id, purchase_order_id);

DO $do$
DECLARE
  t text := 'cxml_order';
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
