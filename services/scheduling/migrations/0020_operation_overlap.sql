-- Wave 23 slice 28: overlap quantity between operations (PRD BR-23.5).
--
-- quantity is the order quantity the operation works on and overlap_quantity
-- the part of it that must be through the operation before the next operation
-- of the order may start (a transfer batch). The next operation then starts
-- once the changeover and that share of the process minutes are done, instead
-- of at the end, and still never ends before this one does. Both are optional
-- (an overlap needs a quantity, and cannot exceed it); existing operations
-- keep NULL, which is no overlap. No new table: the grants of 0001 cover
-- scheduling_operation.
SET LOCAL lock_timeout = '5s';

ALTER TABLE scheduling_operation
  ADD COLUMN IF NOT EXISTS quantity numeric(18, 4) CHECK (quantity > 0),
  ADD COLUMN IF NOT EXISTS overlap_quantity numeric(18, 4) CHECK (overlap_quantity > 0),
  ADD CONSTRAINT scheduling_operation_overlap_within_quantity
    CHECK (overlap_quantity IS NULL OR (quantity IS NOT NULL AND overlap_quantity <= quantity));
