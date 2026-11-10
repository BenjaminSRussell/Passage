-- #35 P4a: an inbound 850 or ORDERS becomes a sales order. A partner's item reference (PO106/PO107 qualifier BP, VP, UP or EN)
-- is resolved to a Passage product, with the site and warehouse it ships from, in partner_item_xref; the invoice account and
-- the currency an order without CUR defaults to are partner_sales_defaults. Both belong to the legal entity of the partner's
-- trade profile and, like the profile, are visible only to callers scoped to it. A message posted to sales records the
-- sales order id; one that cannot be posted is parked FAILED with park_code and stays in the message monitor.
CREATE TABLE IF NOT EXISTS partner_item_xref (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  qualifier text NOT NULL CHECK (qualifier IN ('BP', 'VP', 'UP', 'EN')),
  partner_item text NOT NULL CHECK (char_length(partner_item) BETWEEN 1 AND 48),
  product_id uuid NOT NULL,
  site_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, partner_id, qualifier, partner_item),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);
ALTER TABLE partner_item_xref ENABLE ROW LEVEL SECURITY;
ALTER TABLE partner_item_xref FORCE ROW LEVEL SECURITY;
CREATE POLICY partner_item_xref_tenant_access ON partner_item_xref AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY partner_item_xref_legal_entity_scope ON partner_item_xref AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS partner_sales_defaults (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  invoice_account_id uuid NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, partner_id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);
ALTER TABLE partner_sales_defaults ENABLE ROW LEVEL SECURITY;
ALTER TABLE partner_sales_defaults FORCE ROW LEVEL SECURITY;
CREATE POLICY partner_sales_defaults_tenant_access ON partner_sales_defaults AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY partner_sales_defaults_legal_entity_scope ON partner_sales_defaults AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE message ADD COLUMN IF NOT EXISTS sales_order_id uuid;
ALTER TABLE message ADD COLUMN IF NOT EXISTS park_code text;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON partner_item_xref TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON partner_sales_defaults TO passage_app';
  END IF;
END
$$;
