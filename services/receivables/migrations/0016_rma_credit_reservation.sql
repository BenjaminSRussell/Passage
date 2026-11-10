ALTER TABLE rma_credit ADD COLUMN IF NOT EXISTS billing_invoice_id uuid;
ALTER TABLE rma_credit ADD COLUMN IF NOT EXISTS reservation_lines jsonb;
ALTER TABLE rma_credit ADD COLUMN IF NOT EXISTS billing_reservation_id uuid;
ALTER TABLE rma_credit ADD COLUMN IF NOT EXISTS reservation_committed boolean NOT NULL DEFAULT false;
