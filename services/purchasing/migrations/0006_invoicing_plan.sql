-- Wave 17 purchasing slice 5: invoicing plans on PO lines (FR-17.21, BR-17.17).
-- Additive only (expand/contract). Tenant RLS on every new table; plans carry
-- no legal entity and are read through the PO row and its scope policy.

-- One plan per PO line. line_amount and currency are the line's when the rows
-- were last written: Σ rows = line_amount (BR-17.17).
CREATE TABLE IF NOT EXISTS purchase_invoicing_plan (
  tenant_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  line_no integer NOT NULL,
  kind text NOT NULL CHECK (kind IN ('PERIODIC', 'PARTIAL')),
  frequency text CHECK (frequency IN ('MONTHLY', 'QUARTERLY')),
  billing_day integer CHECK (billing_day BETWEEN 1 AND 31),
  settle_without_receipt boolean NOT NULL DEFAULT true,
  line_amount numeric(20,6) NOT NULL CHECK (line_amount >= 0),
  currency char(3) NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, purchase_order_id, line_no),
  FOREIGN KEY (tenant_id, purchase_order_id, line_no) REFERENCES purchase_order_line (tenant_id, purchase_order_id, line_no),
  CHECK ((kind = 'PERIODIC') = (frequency IS NOT NULL AND billing_day IS NOT NULL))
);

-- Scheduled invoices. Wave 18 (FR-18.17) moves a row OPEN -> INVOICED.
CREATE TABLE IF NOT EXISTS purchase_invoicing_plan_row (
  tenant_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  line_no integer NOT NULL,
  seq integer NOT NULL CHECK (seq > 0),
  due_date date NOT NULL,
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  percent numeric(9,6) CHECK (percent > 0 AND percent <= 100),
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'INVOICED')),
  PRIMARY KEY (tenant_id, purchase_order_id, line_no, seq),
  FOREIGN KEY (tenant_id, purchase_order_id, line_no)
    REFERENCES purchase_invoicing_plan (tenant_id, purchase_order_id, line_no) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS purchase_invoicing_plan_row_due ON purchase_invoicing_plan_row (tenant_id, status, due_date);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['purchase_invoicing_plan', 'purchase_invoicing_plan_row']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
  END LOOP;
END $$;
