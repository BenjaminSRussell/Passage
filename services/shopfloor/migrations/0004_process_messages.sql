-- PSG-25 slice 4: process messages and their per-destination deliveries (FR-25.17, BR-25.13).
-- Unique (tenant, source, source_message_id) makes ingest idempotent by the source id.

CREATE TABLE IF NOT EXISTS process_message (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  source text NOT NULL CHECK (source IN ('kiosk', 'edge', 'manual')),
  source_message_id text NOT NULL CHECK (char_length(source_message_id) BETWEEN 1 AND 128),
  category_code text NOT NULL CHECK (category_code IN ('phase-start', 'phase-end', 'consumption', 'yield',
    'process-data', 'quality-result', 'deviation', 'order-status', 'custom')),
  order_id uuid NOT NULL,
  phase_path text NOT NULL DEFAULT '' CHECK (char_length(phase_path) <= 255),
  characteristics jsonb NOT NULL DEFAULT '[]',
  occurred_at timestamptz NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now(),
  status text NOT NULL CHECK (status IN ('received', 'delivered', 'partially-delivered', 'error')),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, source, source_message_id)
);
CREATE INDEX IF NOT EXISTS process_message_status_received ON process_message (tenant_id, status, received_at);
CREATE INDEX IF NOT EXISTS process_message_order ON process_message (tenant_id, order_id, received_at);
ALTER TABLE process_message ENABLE ROW LEVEL SECURITY;
ALTER TABLE process_message FORCE ROW LEVEL SECURITY;
CREATE POLICY process_message_tenant_access ON process_message AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS process_message_delivery (
  tenant_id uuid NOT NULL,
  message_id uuid NOT NULL,
  destination text NOT NULL CHECK (destination IN ('ebr', 'journal', 'quality', 'kafka', 'webhook')),
  status text NOT NULL CHECK (status IN ('pending', 'delivered', 'error')),
  attempts int NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
  last_error text CHECK (char_length(last_error) <= 1000),
  next_attempt_at timestamptz,
  delivered_at timestamptz,
  reprocess_reason text CHECK (char_length(reprocess_reason) <= 500),
  PRIMARY KEY (tenant_id, message_id, destination),
  FOREIGN KEY (tenant_id, message_id) REFERENCES process_message (tenant_id, id),
  CHECK ((status = 'delivered') = (delivered_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS process_message_delivery_due ON process_message_delivery (tenant_id, status, next_attempt_at);
ALTER TABLE process_message_delivery ENABLE ROW LEVEL SECURITY;
ALTER TABLE process_message_delivery FORCE ROW LEVEL SECURITY;
CREATE POLICY process_message_delivery_tenant_access ON process_message_delivery AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
