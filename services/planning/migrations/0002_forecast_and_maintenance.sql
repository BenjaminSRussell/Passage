-- FR-22.11: planned-order maintenance. version is the ETag; status 'deleted'
-- hides an order from lists while the pegging of its version stays resolvable.
ALTER TABLE planned_order ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1 CHECK (version >= 1);
ALTER TABLE planned_order ADD COLUMN IF NOT EXISTS vendor_id uuid;
ALTER TABLE planned_order DROP CONSTRAINT IF EXISTS planned_order_status_check;
ALTER TABLE planned_order ADD CONSTRAINT planned_order_status_check
  CHECK (status IN ('unprocessed', 'approved', 'firmed', 'deleted'));
ALTER TABLE planned_order ADD CONSTRAINT planned_order_dates_check CHECK (order_date <= delivery_date);

-- FR-22.8: forecast models with their reduction setup, and ForecastLine cells.
-- version 0 means the model has not been written yet (the first write sends If-Match "0").
CREATE TABLE IF NOT EXISTS forecast_model (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  version integer NOT NULL DEFAULT 0 CHECK (version >= 0),
  reduction_principle text NOT NULL DEFAULT 'transactions'
    CHECK (reduction_principle IN ('none', 'percent', 'transactions', 'transactions-dynamic')),
  reduction_percent numeric(9,6) NOT NULL DEFAULT 0 CHECK (reduction_percent >= 0 AND reduction_percent <= 100),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE forecast_model ENABLE ROW LEVEL SECURITY;
ALTER TABLE forecast_model FORCE ROW LEVEL SECURITY;
CREATE POLICY forecast_model_tenant_access ON forecast_model AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- sales_qty is the open sales quantity of the period as the planner entered it
-- until sales orders (w27) feed it; reduced_qty is recomputed on every write.
CREATE TABLE IF NOT EXISTS forecast_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  model_id uuid NOT NULL,
  item_id uuid NOT NULL,
  site_id uuid NOT NULL,
  period_start date NOT NULL,
  period_end date NOT NULL,
  qty numeric(18,6) NOT NULL CHECK (qty >= 0),
  source text NOT NULL CHECK (source IN ('statistical', 'manual', 'consensus')),
  sales_qty numeric(18,6) NOT NULL DEFAULT 0 CHECK (sales_qty >= 0),
  reduced_qty numeric(18,6) NOT NULL DEFAULT 0 CHECK (reduced_qty >= 0 AND reduced_qty <= qty),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, model_id, item_id, site_id, period_start),
  CHECK (period_end >= period_start),
  FOREIGN KEY (tenant_id, model_id) REFERENCES forecast_model (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS forecast_line_site ON forecast_line (tenant_id, model_id, site_id, period_end);
ALTER TABLE forecast_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE forecast_line FORCE ROW LEVEL SECURITY;
CREATE POLICY forecast_line_tenant_access ON forecast_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
