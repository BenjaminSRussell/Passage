-- Fulfillment slice 21: the drop-ship of an order line follows sales' cancelled remainder.
-- A vendor confirmation booked above the quantity sales kept is a supervisor case like the
-- slip posted beyond it, so the exception kind list gains DROPSHIP_CONFIRMED_BEYOND_KEPT.
-- No new table (grants of 0024 stand); fulfillment_exception is small and a CHECK on it is cheap.
ALTER TABLE fulfillment_exception
  DROP CONSTRAINT IF EXISTS fulfillment_exception_kind_check,
  ADD CONSTRAINT fulfillment_exception_kind_check
    CHECK (kind IN ('SLIP_POSTED_BEYOND_KEPT', 'PACKED_BEYOND_KEPT', 'DROPSHIP_CONFIRMED_BEYOND_KEPT'));
