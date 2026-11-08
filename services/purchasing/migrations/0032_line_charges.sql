-- Wave 17 purchasing slice 32: line charges and auto charges keyed on a product or product group (FR-17.9).
-- A PO charge may belong to one line (line_no): PER_UNIT then multiplies that line's quantity and PERCENT takes
-- a percentage of that line's amount after discount. It still counts in charges_total, so the total identity
-- trigger needs no change. Lines are never deleted (a removed line is CANCELLED), so the foreign key holds.
ALTER TABLE purchase_order_charge ADD COLUMN IF NOT EXISTS line_no integer CHECK (line_no IS NULL OR line_no > 0);
ALTER TABLE purchase_order_charge DROP CONSTRAINT IF EXISTS purchase_order_charge_line_fk;
ALTER TABLE purchase_order_charge ADD CONSTRAINT purchase_order_charge_line_fk
  FOREIGN KEY (tenant_id, purchase_order_id, line_no) REFERENCES purchase_order_line (tenant_id, purchase_order_id, line_no);

-- An auto charge is keyed on a vendor (account_code) and now also on an item: ALL keeps the header charge
-- of before, PRODUCT or GROUP adds a line charge to every line of that product or product group.
ALTER TABLE auto_charge ADD COLUMN IF NOT EXISTS item_code text NOT NULL DEFAULT 'ALL' CHECK (item_code IN ('PRODUCT', 'GROUP', 'ALL'));
ALTER TABLE auto_charge ADD COLUMN IF NOT EXISTS item_id uuid;
ALTER TABLE auto_charge DROP CONSTRAINT IF EXISTS auto_charge_item_check;
ALTER TABLE auto_charge ADD CONSTRAINT auto_charge_item_check CHECK ((item_code = 'ALL') = (item_id IS NULL));

-- The charges auto charges have added to a PO: a charge code id for a header charge, "<code id>#<line no>" for
-- a line charge, so a line charge the buyer removed stays removed.
ALTER TABLE purchase_order ALTER COLUMN auto_charge_codes DROP DEFAULT;
ALTER TABLE purchase_order ALTER COLUMN auto_charge_codes TYPE text[] USING auto_charge_codes::text[];
ALTER TABLE purchase_order ALTER COLUMN auto_charge_codes SET DEFAULT '{}';
