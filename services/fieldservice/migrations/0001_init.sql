-- Field service, slice 1 (PSG-45): SLA profiles, agreements, cases, work orders,
-- entitlement usage, number counters, outbox and idempotency. Every table is
-- tenant-scoped under RLS; legal-entity-bearing tables add the restrictive scope policy.

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

CREATE TABLE IF NOT EXISTS service_number_counter (
  tenant_id uuid NOT NULL,
  kind text NOT NULL,
  next bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, kind)
);
ALTER TABLE service_number_counter ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_number_counter FORCE ROW LEVEL SECURITY;
CREATE POLICY service_number_counter_tenant_access ON service_number_counter AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS service_sla_profile (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  time_zone text NOT NULL,
  all_day boolean NOT NULL DEFAULT false,
  open_minute integer NOT NULL DEFAULT 480 CHECK (open_minute BETWEEN 0 AND 1439),
  close_minute integer NOT NULL DEFAULT 1080 CHECK (close_minute BETWEEN 1 AND 1440),
  pause_on_parts boolean NOT NULL DEFAULT false,
  holidays date[] NOT NULL DEFAULT '{}',
  targets jsonb NOT NULL,
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (all_day OR open_minute < close_minute)
);
ALTER TABLE service_sla_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_sla_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY service_sla_profile_tenant_access ON service_sla_profile AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS service_agreement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  customer_party_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  valid_from date NOT NULL,
  valid_to date,
  sla_profile_id uuid NOT NULL,
  billing_model text NOT NULL CHECK (billing_model IN ('PREPAID', 'PER_VISIT', 'SUBSCRIPTION')),
  priority_rank smallint NOT NULL CHECK (priority_rank BETWEEN 1 AND 100),
  status text NOT NULL CHECK (status IN ('DRAFT', 'ACTIVE', 'SUSPENDED', 'EXPIRED', 'CANCELED')),
  lines jsonb NOT NULL DEFAULT '[]',
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, sla_profile_id) REFERENCES service_sla_profile (tenant_id, id),
  CHECK (valid_to IS NULL OR valid_to >= valid_from)
);
CREATE INDEX IF NOT EXISTS service_agreement_customer ON service_agreement (tenant_id, customer_party_id, status);
CREATE INDEX IF NOT EXISTS service_agreement_recent ON service_agreement (tenant_id, created_at DESC, id DESC);
ALTER TABLE service_agreement ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_agreement FORCE ROW LEVEL SECURITY;
CREATE POLICY service_agreement_tenant_access ON service_agreement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_agreement_legal_entity_scope ON service_agreement AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS service_case (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  customer_party_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  service_object_id uuid,
  channel text NOT NULL CHECK (channel IN ('PHONE', 'EMAIL', 'PORTAL', 'IOT', 'INTERNAL')),
  priority smallint NOT NULL CHECK (priority BETWEEN 1 AND 4),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 4000),
  agreement_id uuid,
  sla_profile_id uuid NOT NULL,
  entitlement_trace jsonb NOT NULL DEFAULT '[]',
  opened_at timestamptz NOT NULL,
  sla_response_due timestamptz NOT NULL,
  sla_resolution_due timestamptz NOT NULL,
  sla_paused_minutes integer NOT NULL DEFAULT 0 CHECK (sla_paused_minutes >= 0),
  sla_state text NOT NULL DEFAULT 'RUNNING' CHECK (sla_state IN ('RUNNING', 'PAUSED', 'MET', 'BREACHED')),
  status text NOT NULL DEFAULT 'NEW' CHECK (status IN ('NEW', 'IN_PROGRESS', 'WAITING_CUSTOMER', 'WAITING_PARTS', 'RESOLVED', 'CLOSED', 'CANCELED')),
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, sla_profile_id) REFERENCES service_sla_profile (tenant_id, id),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES service_agreement (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS service_case_sla ON service_case (tenant_id, sla_state, sla_resolution_due);
CREATE INDEX IF NOT EXISTS service_case_recent ON service_case (tenant_id, created_at DESC, id DESC);
ALTER TABLE service_case ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_case FORCE ROW LEVEL SECURITY;
CREATE POLICY service_case_tenant_access ON service_case AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_case_legal_entity_scope ON service_case AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS service_work_order (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  case_id uuid,
  agreement_id uuid,
  customer_party_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  territory_id uuid NOT NULL,
  incident_type_id uuid,
  est_duration_minutes integer NOT NULL CHECK (est_duration_minutes BETWEEN 1 AND 1440),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  status text NOT NULL DEFAULT 'UNSCHEDULED' CHECK (status IN ('UNSCHEDULED', 'SCHEDULED', 'DISPATCHED', 'TRAVELING', 'IN_PROGRESS', 'ON_HOLD', 'COMPLETED', 'POSTED', 'CANCELED')),
  lines jsonb NOT NULL DEFAULT '[]',
  posting jsonb,
  post_key text,
  billable_total numeric(20, 6) NOT NULL DEFAULT 0,
  warranty_cost numeric(20, 6) NOT NULL DEFAULT 0,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, post_key),
  FOREIGN KEY (tenant_id, case_id) REFERENCES service_case (tenant_id, id),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES service_agreement (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS service_work_order_territory ON service_work_order (tenant_id, territory_id, status);
CREATE INDEX IF NOT EXISTS service_work_order_recent ON service_work_order (tenant_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS service_work_order_case ON service_work_order (tenant_id, case_id);
ALTER TABLE service_work_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_work_order FORCE ROW LEVEL SECURITY;
CREATE POLICY service_work_order_tenant_access ON service_work_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_work_order_legal_entity_scope ON service_work_order AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS service_entitlement_usage (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  work_order_id uuid NOT NULL,
  hours numeric(12, 4) NOT NULL CHECK (hours > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, work_order_id),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES service_agreement (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS service_entitlement_usage_agreement ON service_entitlement_usage (tenant_id, agreement_id);
ALTER TABLE service_entitlement_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_entitlement_usage FORCE ROW LEVEL SECURITY;
CREATE POLICY service_entitlement_usage_tenant_access ON service_entitlement_usage AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
