-- Wave 27 slice 18: credit_check_points (FR-27.7, PRD 5.27.13): the points at
-- which the credit rules run for a legal entity. No new table, so no new grant:
-- sales_parameter is already granted to passage_app. The column accepts every
-- PRD value; the service refuses packing_slip and invoice until the fulfillment
-- and billing waves run those checks, and requires confirm.
ALTER TABLE sales_parameter
  ADD COLUMN IF NOT EXISTS credit_check_points text[] NOT NULL DEFAULT ARRAY['confirm', 'release']::text[]
  CHECK (credit_check_points <@ ARRAY['entry', 'confirm', 'release', 'packing_slip', 'invoice']::text[]
         AND cardinality(credit_check_points) >= 1);

-- Open-order exposure (BR-27.3 BALANCE_ALL) is summed from the customer's open
-- orders at every credit check and profile read; this partial index keeps the
-- sum to the customer's open orders only, read from the index.
CREATE INDEX IF NOT EXISTS sales_order_open_exposure
  ON sales_order (tenant_id, legal_entity_id, customer_id, currency) INCLUDE (net_total)
  WHERE status IN ('CONFIRMED', 'PARTIALLY_SHIPPED') AND order_type IN ('ORDER', 'SUBSCRIPTION');
