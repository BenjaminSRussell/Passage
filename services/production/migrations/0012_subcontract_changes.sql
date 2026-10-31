-- Wave 21 slice 15: purchase request changes and cancellations for subcontract operations (FR-21.10).
-- Additive (expand): no existing row changes.

-- When the operation's vendor PO request was withdrawn (event subcontract.pocancelled): the
-- order was cancelled or merged away, or an edit removed the operation, turned subcontracting
-- off or changed the vendor. po_requested_at is cleared at the same time, so turning the
-- operation back on or releasing the order again requests a fresh PO; claiming a request
-- clears this column.
ALTER TABLE production_operation ADD COLUMN IF NOT EXISTS po_cancelled_at timestamptz;
