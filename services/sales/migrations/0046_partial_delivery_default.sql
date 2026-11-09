-- Wave 27 slice 41: partial_delivery_default (PRD 5.27.13, FR-27.6): the
-- partial delivery policy of a legal entity (ALLOWED, COMPLETE_ONLY or
-- ONE_SHIPMENT) that a product and customer without a delivery tolerance rule
-- get from the resolve call, and that a line promise follows when the caller
-- does not say whether the whole quantity must ship on one date. No new table,
-- so no new grant: sales_parameter is already granted to passage_app.
ALTER TABLE sales_parameter
  ADD COLUMN IF NOT EXISTS partial_delivery_default text NOT NULL DEFAULT 'ALLOWED'
  CHECK (partial_delivery_default IN ('ALLOWED', 'COMPLETE_ONLY', 'ONE_SHIPMENT'));
