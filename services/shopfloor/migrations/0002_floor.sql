-- Shop floor machines, machine events, downtime and andon, and payroll export
-- batches (PSG-25 slice 2). Every table is tenant scoped with forced RLS.

CREATE TABLE IF NOT EXISTS machine (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (char_length(code) BETWEEN 1 AND 64),
  resource_id uuid NOT NULL,
  site_id uuid,
  ideal_cycle_s numeric(10,3) NOT NULL CHECK (ideal_cycle_s > 0 AND ideal_cycle_s <= 86400),
  last_seq bigint NOT NULL DEFAULT 0 CHECK (last_seq >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  -- Slice limit: one machine per resource, so OEE streams never merge.
  UNIQUE (tenant_id, resource_id)
);
ALTER TABLE machine ENABLE ROW LEVEL SECURITY;
ALTER TABLE machine FORCE ROW LEVEL SECURITY;
CREATE POLICY machine_tenant_access ON machine AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- BR-25.4: (machine, seq) is unique. Monthly range partitions are a later slice.
CREATE TABLE IF NOT EXISTS machine_event (
  tenant_id uuid NOT NULL,
  machine_id uuid NOT NULL,
  seq bigint NOT NULL CHECK (seq >= 1),
  kind text NOT NULL CHECK (kind IN ('count', 'state', 'downtime', 'alarm', 'measurement')),
  ts timestamptz NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now(),
  good_count bigint NOT NULL DEFAULT 0 CHECK (good_count >= 0),
  reject_count bigint NOT NULL DEFAULT 0 CHECK (reject_count >= 0),
  state text CHECK (state IN ('RUNNING', 'IDLE', 'DOWN', 'SETUP', 'OFF')),
  reason_code text CHECK (char_length(reason_code) <= 32),
  measurement_name text,
  measurement_value numeric(20,6),
  unit text,
  job_id uuid,
  PRIMARY KEY (tenant_id, machine_id, seq),
  FOREIGN KEY (tenant_id, machine_id) REFERENCES machine (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS machine_event_machine_ts ON machine_event (tenant_id, machine_id, ts);
ALTER TABLE machine_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE machine_event FORCE ROW LEVEL SECURITY;
CREATE POLICY machine_event_tenant_access ON machine_event AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS downtime_record (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  machine_id uuid NOT NULL,
  resource_id uuid NOT NULL,
  started_at timestamptz NOT NULL,
  ended_at timestamptz,
  reason_code text CHECK (char_length(reason_code) BETWEEN 1 AND 32),
  category text NOT NULL DEFAULT 'unplanned' CHECK (category IN ('planned', 'unplanned')),
  andon_raised boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, machine_id) REFERENCES machine (tenant_id, id),
  CHECK (ended_at IS NULL OR ended_at >= started_at)
);
-- FR-25.11: at most one open downtime per machine.
CREATE UNIQUE INDEX IF NOT EXISTS downtime_record_open ON downtime_record (tenant_id, machine_id) WHERE ended_at IS NULL;
CREATE INDEX IF NOT EXISTS downtime_record_started ON downtime_record (tenant_id, started_at DESC, id);
ALTER TABLE downtime_record ENABLE ROW LEVEL SECURITY;
ALTER TABLE downtime_record FORCE ROW LEVEL SECURITY;
CREATE POLICY downtime_record_tenant_access ON downtime_record AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- BR-25.10: one open alert per (resource, kind).
CREATE TABLE IF NOT EXISTS andon_alert (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  site_id uuid,
  resource_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('downtime')),
  severity text NOT NULL DEFAULT 'warning' CHECK (severity IN ('info', 'warning', 'error')),
  downtime_id uuid,
  started_at timestamptz NOT NULL,
  cleared_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, downtime_id) REFERENCES downtime_record (tenant_id, id),
  CHECK (cleared_at IS NULL OR cleared_at >= started_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS andon_alert_open ON andon_alert (tenant_id, resource_id, kind) WHERE cleared_at IS NULL;
ALTER TABLE andon_alert ENABLE ROW LEVEL SECURITY;
ALTER TABLE andon_alert FORCE ROW LEVEL SECURITY;
CREATE POLICY andon_alert_tenant_access ON andon_alert AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- FR-25.8: a pay line joins at most one export batch. Approved lines are not
-- edited, so exports are tracked in a side table rather than on pay_line.
CREATE TABLE IF NOT EXISTS pay_export_batch (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  from_date date NOT NULL,
  to_date date NOT NULL,
  line_count integer NOT NULL CHECK (line_count > 0),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (to_date >= from_date)
);
ALTER TABLE pay_export_batch ENABLE ROW LEVEL SECURITY;
ALTER TABLE pay_export_batch FORCE ROW LEVEL SECURITY;
CREATE POLICY pay_export_batch_tenant_access ON pay_export_batch AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS pay_export_line (
  tenant_id uuid NOT NULL,
  pay_line_id uuid NOT NULL,
  batch_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, pay_line_id),
  FOREIGN KEY (tenant_id, pay_line_id) REFERENCES pay_line (tenant_id, id),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES pay_export_batch (tenant_id, id)
);
ALTER TABLE pay_export_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE pay_export_line FORCE ROW LEVEL SECURITY;
CREATE POLICY pay_export_line_tenant_access ON pay_export_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE INDEX IF NOT EXISTS pay_export_line_batch ON pay_export_line (tenant_id, batch_id);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON machine, machine_event, downtime_record, andon_alert, pay_export_batch, pay_export_line TO passage_app';
  END IF;
END
$$;
