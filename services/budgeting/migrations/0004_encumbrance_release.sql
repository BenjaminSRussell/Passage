-- Encumbrance release (FR-38.5): a PO line cancellation or a PO close
-- releases the open remainder of the line's reservation. released is the
-- amount given back to available funds; relieved (invoices) and released
-- together never exceed what was reserved. A release is final: the line is
-- Released and takes no further checks or consumption.
ALTER TABLE encumbrance ADD COLUMN IF NOT EXISTS released numeric(19,4) NOT NULL DEFAULT 0;
ALTER TABLE encumbrance DROP CONSTRAINT IF EXISTS encumbrance_check;
ALTER TABLE encumbrance DROP CONSTRAINT IF EXISTS encumbrance_amounts_check;
ALTER TABLE encumbrance ADD CONSTRAINT encumbrance_amounts_check
  CHECK (relieved >= 0 AND released >= 0 AND relieved + released <= reserved);
