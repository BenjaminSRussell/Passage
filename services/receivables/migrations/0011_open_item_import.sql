-- Opening-item import (integration handoff H5): the ERP migration loader loads a
-- cutover snapshot of open AR items. Credit-side items (credit notes, unapplied
-- payments) are stored with negative amounts, as FR-30.1 specifies, so balances,
-- aging and statements net them against the debits. The slice-1 constraints
-- (amount > 0, 0 <= open_amount <= amount) become sign-aware.
ALTER TABLE open_item DROP CONSTRAINT IF EXISTS open_item_amount_check;
ALTER TABLE open_item DROP CONSTRAINT IF EXISTS open_item_check;
ALTER TABLE open_item ADD CONSTRAINT open_item_amount_nonzero CHECK (amount <> 0);
ALTER TABLE open_item ADD CONSTRAINT open_item_open_amount_range CHECK (
  (amount > 0 AND open_amount >= 0 AND open_amount <= amount)
  OR (amount < 0 AND open_amount <= 0 AND open_amount >= amount));
ALTER TABLE open_item ADD CONSTRAINT open_item_credit_source CHECK (
  amount > 0 OR source_type IN ('CREDIT_NOTE', 'PAYMENT', 'ADJUSTMENT'));

-- import_source names the legacy system (SAP-PRD, AX-2012-EU); legacy_key is the
-- row's key there. The pair is unique per tenant, so a rerun finds the item it
-- loaded before. imported_open_amount is the open amount the import last set: an
-- item whose open amount differs was changed in Passage (settled, cleared,
-- deducted) and a rerun refuses it.
ALTER TABLE open_item ADD COLUMN IF NOT EXISTS import_source text;
ALTER TABLE open_item ADD COLUMN IF NOT EXISTS legacy_key text;
ALTER TABLE open_item ADD COLUMN IF NOT EXISTS imported_open_amount numeric(20,6);
ALTER TABLE open_item ADD COLUMN IF NOT EXISTS imported_at timestamptz;
ALTER TABLE open_item ADD CONSTRAINT open_item_import_columns CHECK (
  (import_source IS NULL) = (legacy_key IS NULL)
  AND (import_source IS NULL) = (imported_open_amount IS NULL)
  AND (import_source IS NULL) = (imported_at IS NULL));
CREATE UNIQUE INDEX IF NOT EXISTS open_item_import_key ON open_item (tenant_id, import_source, legacy_key)
  WHERE import_source IS NOT NULL;
CREATE INDEX IF NOT EXISTS open_item_import_source ON open_item (tenant_id, import_source, customer_id)
  WHERE import_source IS NOT NULL;
