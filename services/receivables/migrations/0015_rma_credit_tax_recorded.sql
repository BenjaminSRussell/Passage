ALTER TABLE rma_credit ADD COLUMN IF NOT EXISTS tax_recorded boolean NOT NULL DEFAULT false;
