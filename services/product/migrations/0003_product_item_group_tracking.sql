ALTER TABLE product ADD COLUMN IF NOT EXISTS item_group_id uuid;
ALTER TABLE product ADD COLUMN IF NOT EXISTS batch_tracked boolean NOT NULL DEFAULT false;
ALTER TABLE product ADD COLUMN IF NOT EXISTS serial_tracked boolean NOT NULL DEFAULT false;
ALTER TABLE product ADD COLUMN IF NOT EXISTS catch_weight boolean NOT NULL DEFAULT false;
