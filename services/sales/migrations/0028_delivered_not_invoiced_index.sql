-- Wave 27 slice 25: delivered, not yet invoiced goods (BR-27.3
-- deliveredNotInvoiced) are summed from the customer's ORDER orders that are
-- PARTIALLY_SHIPPED or SHIPPED at every credit check and profile read; this
-- partial index keeps the lookup to those orders only. No table is added, so
-- no grants are needed.
CREATE INDEX IF NOT EXISTS sales_order_delivered_exposure
  ON sales_order (tenant_id, legal_entity_id, customer_id, currency)
  WHERE status IN ('PARTIALLY_SHIPPED', 'SHIPPED') AND order_type = 'ORDER';
