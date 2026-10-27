CREATE TABLE IF NOT EXISTS plan (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE plan ENABLE ROW LEVEL SECURITY;
ALTER TABLE plan FORCE ROW LEVEL SECURITY;
CREATE POLICY plan_tenant_access ON plan AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS plan_version (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_id uuid NOT NULL,
  number integer NOT NULL CHECK (number >= 1),
  kind text NOT NULL DEFAULT 'static' CHECK (kind IN ('static', 'dynamic')),
  site_id uuid NOT NULL,
  as_of date NOT NULL,
  status text NOT NULL CHECK (status IN ('running', 'complete', 'failed', 'current')),
  items_planned integer NOT NULL CHECK (items_planned >= 0),
  planned_orders_count integer NOT NULL CHECK (planned_orders_count >= 0),
  messages_count integer NOT NULL CHECK (messages_count >= 0),
  duration_ms bigint NOT NULL CHECK (duration_ms >= 0),
  plan_hash text NOT NULL,
  input_hash text NOT NULL,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, plan_id, number),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES plan (tenant_id, id)
);
-- FR-22.1: exactly one current version per plan; the pointer swap is one tx.
CREATE UNIQUE INDEX IF NOT EXISTS plan_version_one_current ON plan_version (tenant_id, plan_id) WHERE status = 'current';
ALTER TABLE plan_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE plan_version FORCE ROW LEVEL SECURITY;
CREATE POLICY plan_version_tenant_access ON plan_version AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS planned_order (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_version_id uuid NOT NULL,
  number text NOT NULL,
  item_id uuid NOT NULL,
  order_type text NOT NULL CHECK (order_type IN ('purchase', 'production', 'transfer')),
  qty numeric(18,6) NOT NULL CHECK (qty > 0),
  order_date date NOT NULL,
  delivery_date date NOT NULL,
  futures_date date,
  delay_days integer NOT NULL DEFAULT 0 CHECK (delay_days >= 0),
  lot_rule text NOT NULL,
  status text NOT NULL DEFAULT 'unprocessed' CHECK (status IN ('unprocessed', 'approved', 'firmed')),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, plan_version_id, number),
  FOREIGN KEY (tenant_id, plan_version_id) REFERENCES plan_version (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS planned_order_item ON planned_order (tenant_id, plan_version_id, item_id);
ALTER TABLE planned_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE planned_order FORCE ROW LEVEL SECURITY;
CREATE POLICY planned_order_tenant_access ON planned_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS pegging (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_version_id uuid NOT NULL,
  item_id uuid NOT NULL,
  supply_ref text NOT NULL,
  demand_ref text NOT NULL,
  demand_ref_type text NOT NULL,
  demand_date date NOT NULL,
  qty numeric(18,6) NOT NULL CHECK (qty > 0),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_version_id) REFERENCES plan_version (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS pegging_supply ON pegging (tenant_id, plan_version_id, supply_ref);
CREATE INDEX IF NOT EXISTS pegging_demand ON pegging (tenant_id, plan_version_id, demand_ref);
ALTER TABLE pegging ENABLE ROW LEVEL SECURITY;
ALTER TABLE pegging FORCE ROW LEVEL SECURITY;
CREATE POLICY pegging_tenant_access ON pegging AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS action_message (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_version_id uuid NOT NULL,
  item_id uuid NOT NULL,
  action text NOT NULL CHECK (action IN ('delay', 'postpone', 'advance', 'shortage')),
  supply_ref text NOT NULL DEFAULT '',
  demand_ref text NOT NULL DEFAULT '',
  suggested_date date,
  delay_days integer NOT NULL DEFAULT 0,
  qty numeric(18,6) NOT NULL CHECK (qty > 0),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_version_id) REFERENCES plan_version (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS action_message_version ON action_message (tenant_id, plan_version_id, action);
ALTER TABLE action_message ENABLE ROW LEVEL SECURITY;
ALTER TABLE action_message FORCE ROW LEVEL SECURITY;
CREATE POLICY action_message_tenant_access ON action_message AS PERMISSIVE
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
