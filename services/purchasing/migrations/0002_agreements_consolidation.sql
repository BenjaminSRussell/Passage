-- Wave 17 purchasing slice 2: purchase agreements with release tracking (FR-17.5,
-- BR-17.10) and consolidation of approved requisition lines into purchase orders
-- (FR-17.3). Additive only (expand/contract): new tables, nullable columns, and
-- foreign keys added NOT VALID so rows written before this migration are not rechecked.

CREATE SEQUENCE IF NOT EXISTS purchase_agreement_number_seq;

CREATE TABLE IF NOT EXISTS purchase_agreement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  currency char(3) NOT NULL,
  valid_from date NOT NULL,
  valid_to date,
  status text NOT NULL CHECK (status IN ('DRAFT', 'EFFECTIVE', 'ON_HOLD', 'CLOSED')),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number),
  CHECK (valid_to IS NULL OR valid_to >= valid_from)
);
CREATE INDEX IF NOT EXISTS purchase_agreement_vendor_status ON purchase_agreement (tenant_id, vendor_id, status);
CREATE INDEX IF NOT EXISTS purchase_agreement_status ON purchase_agreement (tenant_id, status, id);

CREATE TABLE IF NOT EXISTS purchase_agreement_line (
  tenant_id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  id uuid NOT NULL,
  commitment_type text NOT NULL CHECK (commitment_type IN ('QUANTITY', 'VALUE', 'CATEGORY_VALUE', 'TOTAL_VALUE')),
  product_id uuid,
  category text NOT NULL DEFAULT '',
  unit text NOT NULL DEFAULT '',
  committed_qty numeric(20,6) NOT NULL DEFAULT 0 CHECK (committed_qty >= 0),
  committed_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (committed_amount >= 0),
  unit_price numeric(20,6) NOT NULL DEFAULT 0 CHECK (unit_price >= 0),
  discount_pct numeric(9,6) NOT NULL DEFAULT 0 CHECK (discount_pct >= 0 AND discount_pct <= 100),
  max_enforced text NOT NULL DEFAULT 'HARD' CHECK (max_enforced IN ('HARD', 'SOFT', 'NONE')),
  price_override boolean NOT NULL DEFAULT false,
  released_qty numeric(20,6) NOT NULL DEFAULT 0 CHECK (released_qty >= 0),
  released_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (released_amount >= 0),
  PRIMARY KEY (tenant_id, agreement_id, line_no),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES purchase_agreement (tenant_id, id),
  -- FR-17.5: each commitment type carries the measure it is enforced on.
  CHECK (
    (commitment_type = 'QUANTITY' AND product_id IS NOT NULL AND unit <> '' AND committed_qty > 0) OR
    (commitment_type = 'VALUE' AND product_id IS NOT NULL AND committed_amount > 0) OR
    (commitment_type = 'CATEGORY_VALUE' AND category <> '' AND committed_amount > 0) OR
    (commitment_type = 'TOTAL_VALUE' AND committed_amount > 0)
  ),
  -- 3.17.4 invariant: released <= committed when HARD, on the line's measure.
  CHECK (max_enforced <> 'HARD' OR
         (commitment_type = 'QUANTITY' AND released_qty <= committed_qty) OR
         (commitment_type <> 'QUANTITY' AND released_amount <= committed_amount))
);

-- A PO line may release against an agreement line (FR-17.5) and may come from a
-- requisition line (FR-17.3); both references are now enforced.
ALTER TABLE purchase_order_line ADD COLUMN IF NOT EXISTS agreement_line_id uuid;
ALTER TABLE purchase_order_line ADD CONSTRAINT purchase_order_line_agreement_line
  FOREIGN KEY (tenant_id, agreement_line_id) REFERENCES purchase_agreement_line (tenant_id, id) NOT VALID;
ALTER TABLE purchase_order_line ADD CONSTRAINT purchase_order_line_requisition_line
  FOREIGN KEY (tenant_id, requisition_line_id) REFERENCES purchase_requisition_line (tenant_id, id) NOT VALID;
CREATE INDEX IF NOT EXISTS purchase_order_line_agreement_line ON purchase_order_line (tenant_id, agreement_line_id)
  WHERE agreement_line_id IS NOT NULL;

-- A consolidated requisition line points at the PO line that ordered it.
ALTER TABLE purchase_requisition_line ADD COLUMN IF NOT EXISTS purchase_order_id uuid;
ALTER TABLE purchase_requisition_line ADD COLUMN IF NOT EXISTS purchase_order_line_no integer;
ALTER TABLE purchase_requisition_line ADD CONSTRAINT purchase_requisition_line_po_line
  FOREIGN KEY (tenant_id, purchase_order_id, purchase_order_line_no)
  REFERENCES purchase_order_line (tenant_id, purchase_order_id, line_no) NOT VALID;
ALTER TABLE purchase_requisition_line ADD CONSTRAINT purchase_requisition_line_consolidated_link
  CHECK ((status = 'CONSOLIDATED') = (purchase_order_id IS NOT NULL AND purchase_order_line_no IS NOT NULL)) NOT VALID;
-- The consolidation board reads OPEN lines across requisitions.
CREATE INDEX IF NOT EXISTS purchase_requisition_line_status ON purchase_requisition_line (tenant_id, status, id);

-- Row-level security for the new tables, same template as 0001.
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['purchase_agreement', 'purchase_agreement_line']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
  END LOOP;
  EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
                 'purchase_agreement_legal_entity_scope', 'purchase_agreement');
END $$;
