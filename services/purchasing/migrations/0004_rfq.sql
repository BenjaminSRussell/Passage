-- Wave 17 purchasing slice 4: request for quotation cycle (FR-17.4, BR-17.11).
-- Additive only (expand/contract). Tenant RLS on every table; the legal entity
-- scope policy sits on the header, as for requisitions and agreements.

CREATE SEQUENCE IF NOT EXISTS purchase_rfq_number_seq;

CREATE TABLE IF NOT EXISTS purchase_rfq (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  legal_entity_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SENT', 'REPLIES_OPEN', 'CLOSED', 'AWARDED', 'CANCELLED')),
  reply_deadline timestamptz NOT NULL,
  ranking_rule text NOT NULL DEFAULT 'LOWEST_PRICE' CHECK (ranking_rule IN ('LOWEST_PRICE', 'LEAD_TIME')),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number)
);
CREATE INDEX IF NOT EXISTS purchase_rfq_status ON purchase_rfq (tenant_id, status);

CREATE TABLE IF NOT EXISTS purchase_rfq_line (
  tenant_id uuid NOT NULL,
  rfq_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  id uuid NOT NULL,
  product_id uuid,
  description text NOT NULL DEFAULT '',
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  unit text NOT NULL,
  required_date date,
  PRIMARY KEY (tenant_id, rfq_id, line_no),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, rfq_id) REFERENCES purchase_rfq (tenant_id, id),
  CHECK (product_id IS NOT NULL OR description <> '')
);

CREATE TABLE IF NOT EXISTS purchase_rfq_invitation (
  tenant_id uuid NOT NULL,
  rfq_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'INVITED' CHECK (status IN ('INVITED', 'REPLIED')),
  sent_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, rfq_id, vendor_id),
  FOREIGN KEY (tenant_id, rfq_id) REFERENCES purchase_rfq (tenant_id, id)
);

-- One reply per vendor and line; a revision before the deadline overwrites it.
CREATE TABLE IF NOT EXISTS purchase_rfq_reply (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  rfq_id uuid NOT NULL,
  line_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  unit_price numeric(20,6) NOT NULL CHECK (unit_price >= 0),
  price_unit numeric(20,6) NOT NULL DEFAULT 1 CHECK (price_unit > 0),
  currency char(3) NOT NULL,
  lead_time_days integer NOT NULL DEFAULT 0 CHECK (lead_time_days >= 0),
  valid_until date,
  notes text NOT NULL DEFAULT '',
  received_at timestamptz NOT NULL DEFAULT now(),
  late boolean NOT NULL DEFAULT false,
  awarded boolean NOT NULL DEFAULT false,
  -- BR-17.11: the buyer's reason for awarding a late reply.
  override_reason text,
  CHECK (override_reason IS NULL OR (awarded AND late)),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, rfq_id, line_id, vendor_id),
  FOREIGN KEY (tenant_id, rfq_id, vendor_id) REFERENCES purchase_rfq_invitation (tenant_id, rfq_id, vendor_id),
  FOREIGN KEY (tenant_id, line_id) REFERENCES purchase_rfq_line (tenant_id, id)
);

-- A PO line created by an RFQ award remembers the reply it was priced from.
ALTER TABLE purchase_order_line ADD COLUMN IF NOT EXISTS rfq_reply_id uuid;
ALTER TABLE purchase_order_line ADD CONSTRAINT purchase_order_line_rfq_reply_link
  FOREIGN KEY (tenant_id, rfq_reply_id) REFERENCES purchase_rfq_reply (tenant_id, id);

-- Row-level security, same template as 0001 and 0002.
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['purchase_rfq', 'purchase_rfq_line', 'purchase_rfq_invitation', 'purchase_rfq_reply']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
  END LOOP;
  EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
                 'purchase_rfq_legal_entity_scope', 'purchase_rfq');
END $$;
