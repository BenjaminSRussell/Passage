-- Payment prediction (FR-47.15, 9.47 data model): models, predictions, per-tenant settings and the outbox.
-- Every table carries tenant_id with ENABLE + FORCE row-level security and the tenant policy. Run as the owner
-- role, never as passage_app (which would own the tables and could switch row-level security off).

DO $do$
BEGIN
  IF current_user = 'passage_app' THEN
    RAISE EXCEPTION 'prediction migrations must run as the owner role, not passage_app';
  END IF;
END
$do$;

-- One row per trained model. artifact holds the trees and the customer statistics scoring needs (domain.py
-- model_artifact). At most one ACTIVE model per tenant; a model above the MAE gate stays CANDIDATE (AC-47.12).
CREATE TABLE IF NOT EXISTS prediction_model (
  tenant_id uuid NOT NULL,
  model_version text NOT NULL CHECK (model_version ~ '^pm-[0-9]{8}-[0-9]+$'),
  trained_at timestamptz NOT NULL,
  rows integer NOT NULL CHECK (rows > 0),
  mae_days numeric(8,3) NOT NULL CHECK (mae_days >= 0),
  status text NOT NULL CHECK (status IN ('CANDIDATE', 'ACTIVE', 'RETIRED')),
  min_rows integer NOT NULL CHECK (min_rows BETWEEN 100 AND 10000),
  mae_max_days numeric(5,2) NOT NULL CHECK (mae_max_days BETWEEN 1 AND 30),
  artifact jsonb NOT NULL CHECK (jsonb_typeof(artifact) = 'object'),
  PRIMARY KEY (tenant_id, model_version)
);
CREATE UNIQUE INDEX IF NOT EXISTS prediction_model_one_active ON prediction_model (tenant_id) WHERE status = 'ACTIVE';
ALTER TABLE prediction_model ENABLE ROW LEVEL SECURITY;
ALTER TABLE prediction_model FORCE ROW LEVEL SECURITY;
CREATE POLICY prediction_model_tenant_access ON prediction_model AS PERMISSIVE
  USING (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

-- The latest prediction per open invoice. actual_paid_date is set when the invoice turns up paid in a retrain,
-- which is what the accuracy report reads.
CREATE TABLE IF NOT EXISTS payment_prediction (
  tenant_id uuid NOT NULL,
  invoice_id text NOT NULL CHECK (char_length(invoice_id) BETWEEN 1 AND 64),
  model_version text NOT NULL,
  predicted_payment_date date NOT NULL,
  p_late_30d numeric(5,4) NOT NULL CHECK (p_late_30d BETWEEN 0 AND 1),
  features_hash text NOT NULL CHECK (features_hash ~ '^[0-9a-f]{32}$'),
  due_date date NOT NULL,
  scored_at timestamptz NOT NULL,
  actual_paid_date date,
  PRIMARY KEY (tenant_id, invoice_id),
  FOREIGN KEY (tenant_id, model_version) REFERENCES prediction_model (tenant_id, model_version)
);
CREATE INDEX IF NOT EXISTS payment_prediction_outcomes ON payment_prediction (tenant_id, model_version) WHERE actual_paid_date IS NOT NULL;
ALTER TABLE payment_prediction ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_prediction FORCE ROW LEVEL SECURITY;
CREATE POLICY payment_prediction_tenant_access ON payment_prediction AS PERMISSIVE
  USING (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

-- prediction.payment.* per tenant (9.47.20). A tenant without a row uses the service defaults.
CREATE TABLE IF NOT EXISTS prediction_settings (
  tenant_id uuid PRIMARY KEY,
  min_rows integer NOT NULL CHECK (min_rows BETWEEN 100 AND 10000),
  mae_max_days numeric(5,2) NOT NULL CHECK (mae_max_days BETWEEN 1 AND 30),
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE prediction_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE prediction_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY prediction_settings_tenant_access ON prediction_settings AS PERMISSIVE
  USING (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

-- Transactional outbox (kernel dbx.OutboxDDL shape): prediction.payment.scored rows are written with the predictions.
CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON prediction_model TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON payment_prediction TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON prediction_settings TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON outbox TO passage_app';
  END IF;
END
$$;
