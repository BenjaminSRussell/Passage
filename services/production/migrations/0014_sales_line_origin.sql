ALTER TABLE production_order ADD COLUMN IF NOT EXISTS configuration_id uuid;

ALTER TABLE production_order DROP CONSTRAINT IF EXISTS production_order_configuration_origin;
ALTER TABLE production_order ADD CONSTRAINT production_order_configuration_origin
  CHECK (configuration_id IS NULL OR origin_type = 'SalesOrderLine') NOT VALID;

ALTER TABLE production_order DROP CONSTRAINT IF EXISTS production_order_sales_line_has_origin;
ALTER TABLE production_order ADD CONSTRAINT production_order_sales_line_has_origin
  CHECK (origin_type <> 'SalesOrderLine' OR origin_id IS NOT NULL) NOT VALID;

CREATE UNIQUE INDEX IF NOT EXISTS production_order_sales_line_origin
  ON production_order (tenant_id, origin_id) WHERE origin_type = 'SalesOrderLine';
