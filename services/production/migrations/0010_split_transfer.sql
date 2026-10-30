-- Wave 21 slice 11: split of a Started order with the proportional WIP transfer
-- (FR-21.15, BR-21.11). Additive (expand): no existing row changes.

-- The transfer is a pair of system journals of the new type, one on each order;
-- counterpart_order_id names the other order of the pair. Users cannot create the
-- type; the posted lines and WIP postings move consumption, hours and WIP.
ALTER TABLE production_journal DROP CONSTRAINT IF EXISTS production_journal_journal_type_check;
ALTER TABLE production_journal ADD CONSTRAINT production_journal_journal_type_check
  CHECK (journal_type IN ('picking-list', 'route-card', 'report-as-finished', 'split-transfer'));

ALTER TABLE production_journal ADD COLUMN IF NOT EXISTS counterpart_order_id uuid;
ALTER TABLE production_journal DROP CONSTRAINT IF EXISTS production_journal_counterpart_fk;
ALTER TABLE production_journal ADD CONSTRAINT production_journal_counterpart_fk
  FOREIGN KEY (tenant_id, counterpart_order_id) REFERENCES production_order (tenant_id, id);
ALTER TABLE production_journal DROP CONSTRAINT IF EXISTS production_journal_counterpart_check;
ALTER TABLE production_journal ADD CONSTRAINT production_journal_counterpart_check
  CHECK ((counterpart_order_id IS NOT NULL) = (journal_type = 'split-transfer'));

CREATE INDEX IF NOT EXISTS production_journal_transfer
  ON production_journal (tenant_id, order_id) WHERE journal_type = 'split-transfer';
