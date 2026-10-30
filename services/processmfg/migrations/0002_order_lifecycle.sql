-- Process manufacturing, wave 24 slice 9: the batch-order lifecycle (release,
-- start, report as finished, end) and the co-product / by-product cost
-- allocation an order carries once it has ended (FR-24.4, BR-24.3, AC-24.3).
-- The columns of batch_order and batch_order_line keep the 0001 policies and
-- grants; the new table gets its own below.

ALTER TABLE batch_order
  ADD COLUMN IF NOT EXISTS released_at timestamptz,
  ADD COLUMN IF NOT EXISTS started_at timestamptz,
  ADD COLUMN IF NOT EXISTS reported_at timestamptz,
  ADD COLUMN IF NOT EXISTS ended_at timestamptz,
  -- Quantity of the order's own item reported as finished (RAF).
  ADD COLUMN IF NOT EXISTS actual_qty numeric(18,6) CHECK (actual_qty IS NULL OR actual_qty > 0),
  -- Cost the order ended with, in currency (rounded to the currency's minor unit).
  ADD COLUMN IF NOT EXISTS batch_cost numeric(18,4) CHECK (batch_cost IS NULL OR batch_cost >= 0),
  ADD COLUMN IF NOT EXISTS currency text CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),
  ADD COLUMN IF NOT EXISTS allocation_method text CHECK (allocation_method IS NULL OR allocation_method IN ('none', 'manual', 'tca', 'quantity'));

-- Reported quantity of a co-product or by-product line.
ALTER TABLE batch_order_line
  ADD COLUMN IF NOT EXISTS raf_qty numeric(18,6) CHECK (raf_qty IS NULL OR raf_qty >= 0);

-- One row per recipient of the batch cost: the order's main item (line 0), each
-- co-product line and each by-product burden. The amounts of an order add up to
-- its batch_cost exactly; rows are written once, when the order ends.
CREATE TABLE IF NOT EXISTS batch_order_allocation (
  tenant_id uuid NOT NULL,
  order_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  role text NOT NULL CHECK (role IN ('main', 'co-product', 'by-product-burden')),
  line_number integer NOT NULL CHECK (line_number >= 0),
  item_id uuid NOT NULL,
  raf_qty numeric(18,6) NOT NULL CHECK (raf_qty >= 0),
  amount numeric(18,4) NOT NULL CHECK (amount >= 0),
  percent numeric(9,4) NOT NULL DEFAULT 0 CHECK (percent >= 0 AND percent <= 100),
  PRIMARY KEY (tenant_id, order_id, line_number),
  FOREIGN KEY (tenant_id, order_id) REFERENCES batch_order (tenant_id, id)
);

ALTER TABLE batch_order_allocation ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_order_allocation FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_order_allocation_tenant_access ON batch_order_allocation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY batch_order_allocation_legal_entity_scope ON batch_order_allocation AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Allocations are only ever read and inserted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON batch_order_allocation TO passage_app';
  END IF;
END
$$;
