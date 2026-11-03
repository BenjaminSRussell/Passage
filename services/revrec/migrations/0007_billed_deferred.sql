-- The deferred consideration billing booked to 2400 for the contract (the invoice's lines on deferred revenue accounts). It is what
-- the roll-forward adds to the deferred balance; the allocation only drives when it is recognized. Null: the allocated deferred
-- amounts stand in for it.
ALTER TABLE revenue_contract ADD COLUMN IF NOT EXISTS billed_deferred numeric(20,6) CHECK (billed_deferred >= 0);
