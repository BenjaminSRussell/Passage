-- FR-38.5: an approved requisition reserves funds as a pre-encumbrance (stage pre); the confirmed purchase order
-- converts it into an encumbrance (stage encumbrance) on the PO line. The balance keeps both totals apart.
ALTER TABLE encumbrance ADD COLUMN IF NOT EXISTS stage text NOT NULL DEFAULT 'encumbrance' CHECK (stage IN ('pre', 'encumbrance'));
ALTER TABLE encumbrance ADD COLUMN IF NOT EXISTS converted_at timestamptz;
