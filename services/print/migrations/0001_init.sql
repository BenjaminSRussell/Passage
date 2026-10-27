CREATE TABLE IF NOT EXISTS print_device (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,39}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  device_type text NOT NULL CHECK (device_type IN ('ZPL_II', 'PDF_IPP', 'PCL_6')),
  dpi integer NOT NULL CHECK (dpi IN (203, 300, 600)),
  host text NOT NULL CHECK (char_length(host) BETWEEN 1 AND 253),
  port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
  queue_code text NOT NULL CHECK (queue_code ~ '^[A-Z0-9][A-Z0-9_-]{1,39}$'),
  failover_rank integer NOT NULL CHECK (failover_rank >= 1),
  idempotency_key text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, queue_code, failover_rank),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE print_device ENABLE ROW LEVEL SECURITY;
ALTER TABLE print_device FORCE ROW LEVEL SECURITY;
CREATE POLICY print_device_tenant_access ON print_device AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS print_job (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  template_code text NOT NULL,
  queue_code text NOT NULL,
  printer_id uuid,
  printer_code text,
  zpl text NOT NULL,
  status text NOT NULL DEFAULT 'QUEUED' CHECK (status IN ('QUEUED', 'PRINTED', 'FAILED')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  last_error text,
  reprint_of uuid,
  request_hash text NOT NULL,
  idempotency_key text NOT NULL,
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS print_job_printer ON print_job (tenant_id, printer_code, created_at);
CREATE INDEX IF NOT EXISTS print_job_log ON print_job (tenant_id, created_at DESC, id);
ALTER TABLE print_job ENABLE ROW LEVEL SECURITY;
ALTER TABLE print_job FORCE ROW LEVEL SECURITY;
CREATE POLICY print_job_tenant_access ON print_job AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Transactional outbox relayed by Debezium. Tenant RLS like every other table
-- (the relay connects with a BYPASSRLS replication role).
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
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
