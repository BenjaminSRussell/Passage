-- Credits posted before billing reservations existed get their reservation from sales (#557): the driver
-- marks an order once receivables holds a reservation for its credit, so it is not asked again.
ALTER TABLE return_order ADD COLUMN IF NOT EXISTS reservation_backfilled_at timestamptz;

-- A credit receivables never confirmed can be recomputed (#557): its stored tax reversal is replaced.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT DELETE ON return_order_tax TO passage_app';
  END IF;
END
$$;
