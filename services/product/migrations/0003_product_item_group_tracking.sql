ALTER TABLE product ADD COLUMN IF NOT EXISTS item_group_id uuid;
ALTER TABLE product ADD COLUMN IF NOT EXISTS batch_tracked boolean NOT NULL DEFAULT false;
ALTER TABLE product ADD COLUMN IF NOT EXISTS serial_tracked boolean NOT NULL DEFAULT false;
ALTER TABLE product ADD COLUMN IF NOT EXISTS catch_weight boolean NOT NULL DEFAULT false;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'product_item_group_fk' AND conrelid = 'product'::regclass) THEN
    ALTER TABLE product ADD CONSTRAINT product_item_group_fk FOREIGN KEY (tenant_id, item_group_id) REFERENCES item_group (tenant_id, id);
  END IF;
END $$;
