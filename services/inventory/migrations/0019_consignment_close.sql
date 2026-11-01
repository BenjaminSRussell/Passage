-- Wave 16 slice 18: closing and cancelling vendor replenishment orders (FR-16.17). Additive:
-- two terminal statuses on consignment_order (CLOSED gives up the open quantity of a partly
-- received order, CANCELLED ends an order nothing was received against) and who closed it,
-- when and why. No new table, so no new grant (consignment_order is already granted SELECT,
-- INSERT and UPDATE to passage_app).
ALTER TABLE consignment_order ADD COLUMN IF NOT EXISTS closed_at timestamptz;
ALTER TABLE consignment_order ADD COLUMN IF NOT EXISTS closed_by text;
ALTER TABLE consignment_order ADD COLUMN IF NOT EXISTS close_reason text
  CHECK (close_reason IS NULL OR char_length(close_reason) BETWEEN 1 AND 200);
DO $$
DECLARE c text;
BEGIN
  FOR c IN SELECT conname FROM pg_constraint
    WHERE conrelid = 'consignment_order'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%PARTIALLY_RECEIVED%'
  LOOP
    EXECUTE format('ALTER TABLE consignment_order DROP CONSTRAINT %I', c);
  END LOOP;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'consignment_order_status_check' AND conrelid = 'consignment_order'::regclass) THEN
    ALTER TABLE consignment_order ADD CONSTRAINT consignment_order_status_check
      CHECK (status IN ('CREATED', 'PARTIALLY_RECEIVED', 'RECEIVED', 'CLOSED', 'CANCELLED'));
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'consignment_order_closed_check' AND conrelid = 'consignment_order'::regclass) THEN
    ALTER TABLE consignment_order ADD CONSTRAINT consignment_order_closed_check
      CHECK ((status IN ('CLOSED', 'CANCELLED')) = (closed_at IS NOT NULL));
  END IF;
END $$;
