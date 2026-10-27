CREATE TABLE IF NOT EXISTS trading_partner (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  isa_qualifier char(2) NOT NULL CHECK (isa_qualifier ~ '^[A-Z0-9]{2}$'),
  isa_id text NOT NULL CHECK (isa_id ~ '^[A-Za-z0-9]{1,15}$'),
  environment text NOT NULL CHECK (environment IN ('TEST', 'PROD')),
  ack_sla_minutes integer NOT NULL DEFAULT 60 CHECK (ack_sla_minutes BETWEEN 5 AND 1440),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, isa_qualifier, isa_id, environment)
);
ALTER TABLE trading_partner ENABLE ROW LEVEL SECURITY;
ALTER TABLE trading_partner FORCE ROW LEVEL SECURITY;
CREATE POLICY trading_partner_tenant_access ON trading_partner AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Gap-free control numbers per partner, direction and standard (BR-35.5). The
-- row is locked and advanced inside the transaction that records the interchange.
CREATE TABLE IF NOT EXISTS control_number_counter (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  direction text NOT NULL CHECK (direction IN ('IN', 'OUT')),
  standard text NOT NULL CHECK (standard IN ('X12', 'EDIFACT', 'UBL', 'CXML')),
  last_value bigint NOT NULL DEFAULT 0 CHECK (last_value >= 0),
  PRIMARY KEY (tenant_id, partner_id, direction, standard),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);
ALTER TABLE control_number_counter ENABLE ROW LEVEL SECURITY;
ALTER TABLE control_number_counter FORCE ROW LEVEL SECURITY;
CREATE POLICY control_number_counter_tenant_access ON control_number_counter AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS interchange (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  partner_id uuid NOT NULL,
  direction text NOT NULL CHECK (direction IN ('IN', 'OUT')),
  standard text NOT NULL CHECK (standard IN ('X12', 'EDIFACT', 'UBL', 'CXML')),
  version text NOT NULL,
  control_number text NOT NULL,
  raw_payload text NOT NULL,
  ack_status text NOT NULL CHECK (ack_status IN ('PENDING', 'ACCEPTED', 'ACCEPTED_WITH_ERRORS', 'REJECTED', 'TIMEOUT')),
  ack_payload text,
  received_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);
-- A control number may come back after the duplicate window (BR-35.5,
-- EDI_DUP_WINDOW_DAYS), so it is indexed rather than unique; the receiving
-- transaction takes an advisory lock on (partner, direction, standard, control
-- number) before it checks the window.
CREATE INDEX IF NOT EXISTS interchange_control ON interchange (tenant_id, partner_id, direction, standard, control_number, received_at DESC);
CREATE INDEX IF NOT EXISTS interchange_received ON interchange (tenant_id, received_at DESC);
ALTER TABLE interchange ENABLE ROW LEVEL SECURITY;
ALTER TABLE interchange FORCE ROW LEVEL SECURITY;
CREATE POLICY interchange_tenant_access ON interchange AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS message (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  interchange_id uuid NOT NULL,
  transaction_set text NOT NULL,
  st_control text NOT NULL,
  status text NOT NULL CHECK (status IN ('RECEIVED', 'MAPPED', 'POSTED', 'FAILED', 'SENT', 'ACKED')),
  canonical jsonb,
  error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, interchange_id) REFERENCES interchange (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS message_status_created ON message (tenant_id, status, created_at);
CREATE INDEX IF NOT EXISTS message_interchange ON message (tenant_id, interchange_id);
ALTER TABLE message ENABLE ROW LEVEL SECURITY;
ALTER TABLE message FORCE ROW LEVEL SECURITY;
CREATE POLICY message_tenant_access ON message AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

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

-- Idempotency-Key replay store (dbx.IdempotencyDDL): a replay with the same key
-- and request returns the stored response; another request under the key is
-- refused with idempotency_key_reuse.
CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int NOT NULL,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY idempotency_keys_tenant_access ON idempotency_keys AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
