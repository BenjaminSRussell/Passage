-- Wave 16 slice 9: TRANSFER journals (FR-16.3 stock moves between dimension sets).
-- Additive: widens two CHECK lists and gives journal lines a destination dimension
-- set. No new table, so the passage_app grants of 0001 (inventory_journal_line) and
-- the origin table already cover it.

ALTER TABLE inventory_journal DROP CONSTRAINT IF EXISTS inventory_journal_journal_type_check;
ALTER TABLE inventory_journal ADD CONSTRAINT inventory_journal_journal_type_check
  CHECK (journal_type IN ('MOVEMENT', 'ADJUSTMENT', 'OPENING_BALANCE', 'COUNT', 'OWNERSHIP_CHANGE', 'TRANSFER'));

-- TRANSFER_IN origins are the destination receipt legs of a TRANSFER journal line; the
-- issue leg keeps INVENT_JOURNAL_LINE. The origin key stays unique per leg.
ALTER TABLE inventory_transaction_origin DROP CONSTRAINT IF EXISTS inventory_transaction_origin_source_type_check;
ALTER TABLE inventory_transaction_origin ADD CONSTRAINT inventory_transaction_origin_source_type_check
  CHECK (source_type IN ('INVENT_JOURNAL_LINE', 'PURCH_LINE', 'RECEIPT_LINE', 'SALES_LINE', 'PROD_LINE', 'COUNT_LINE', 'TRANSFER_LINE',
                         'OPENING', 'BLOCKING', 'CONSIGNMENT_LINE', 'OWNERSHIP_CHANGE', 'TRANSFER_IN'));

ALTER TABLE inventory_journal_line
  ADD COLUMN IF NOT EXISTS to_dimension_set_hash bytea CHECK (to_dimension_set_hash IS NULL OR octet_length(to_dimension_set_hash) = 32),
  ADD COLUMN IF NOT EXISTS to_dims jsonb;
ALTER TABLE inventory_journal_line DROP CONSTRAINT IF EXISTS inventory_journal_line_to_dims_pair;
ALTER TABLE inventory_journal_line ADD CONSTRAINT inventory_journal_line_to_dims_pair
  CHECK ((to_dims IS NULL) = (to_dimension_set_hash IS NULL));
