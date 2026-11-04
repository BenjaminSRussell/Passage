-- Wave 24 AC4: catch-weight dual quantity on inventory transactions and journal
-- lines (FR-24.7). qty remains the inventory (weight) quantity used for on-hand
-- and cost; cw_qty is the optional catch-weight count (cases). Nullable so
-- existing rows and non-catch-weight postings are unchanged.
--
-- inventory_transaction is append-only; the trigger is replaced so cw_qty is
-- part of the immutable identity. The check is NOT VALID so this file only
-- changes the catalog. 0030 validates it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE inventory_transaction ADD COLUMN IF NOT EXISTS cw_qty integer;
ALTER TABLE inventory_transaction ADD CONSTRAINT inventory_transaction_cw_qty_check
  CHECK (cw_qty IS NULL OR cw_qty >= 1) NOT VALID;

ALTER TABLE inventory_journal_line ADD COLUMN IF NOT EXISTS cw_qty integer;
ALTER TABLE inventory_journal_line ADD CONSTRAINT inventory_journal_line_cw_qty_check
  CHECK (cw_qty IS NULL OR cw_qty >= 1) NOT VALID;

CREATE OR REPLACE FUNCTION inventory_transaction_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'inventory_transaction is append-only' USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.tenant_id, NEW.legal_entity_id, NEW.id, NEW.origin_id, NEW.product_id, NEW.dimension_set_hash, NEW.qty, NEW.entered_qty,
      NEW.entered_unit, NEW.cw_qty, NEW.currency, NEW.created_at)
     IS DISTINCT FROM
     (OLD.tenant_id, OLD.legal_entity_id, OLD.id, OLD.origin_id, OLD.product_id, OLD.dimension_set_hash, OLD.qty, OLD.entered_qty,
      OLD.entered_unit, OLD.cw_qty, OLD.currency, OLD.created_at) THEN
    RAISE EXCEPTION 'inventory_transaction quantity and identity are immutable' USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END
$$;
