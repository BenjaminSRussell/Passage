-- Wave 23 slice 2: resource networks (FR-23.13), setup matrices and cleanout
-- rules (FR-23.15), campaigns (FR-23.14) and capacity blocks (FR-23.17).
-- Every table is tenant-scoped with row-level security.

CREATE TABLE IF NOT EXISTS scheduling_resource_network (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,31}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  active_from date NOT NULL,
  active_to date,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (active_to IS NULL OR active_to >= active_from)
);
ALTER TABLE scheduling_resource_network ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_resource_network FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_resource_network_tenant_access ON scheduling_resource_network AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS scheduling_network_edge (
  tenant_id uuid NOT NULL,
  network_id uuid NOT NULL,
  from_resource_id uuid NOT NULL,
  to_resource_id uuid NOT NULL,
  transfer_minutes integer NOT NULL DEFAULT 0 CHECK (transfer_minutes BETWEEN 0 AND 10080),
  max_wait_minutes integer CHECK (max_wait_minutes BETWEEN 0 AND 10080),
  secondary_resource_id uuid,
  PRIMARY KEY (tenant_id, network_id, from_resource_id, to_resource_id),
  CHECK (from_resource_id <> to_resource_id),
  FOREIGN KEY (tenant_id, network_id) REFERENCES scheduling_resource_network (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, from_resource_id) REFERENCES scheduling_resource (tenant_id, id),
  FOREIGN KEY (tenant_id, to_resource_id) REFERENCES scheduling_resource (tenant_id, id),
  FOREIGN KEY (tenant_id, secondary_resource_id) REFERENCES scheduling_resource (tenant_id, id)
);
ALTER TABLE scheduling_network_edge ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_network_edge FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_network_edge_tenant_access ON scheduling_network_edge AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- One matrix per resource and version; the active one for a date is the
-- highest version whose active_from is on or before it. Costs are in the
-- matrix currency.
CREATE TABLE IF NOT EXISTS scheduling_setup_matrix (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  resource_id uuid NOT NULL,
  version integer NOT NULL CHECK (version >= 1),
  active_from date NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, resource_id, version),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, resource_id) REFERENCES scheduling_resource (tenant_id, id)
);
ALTER TABLE scheduling_setup_matrix ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_setup_matrix FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_setup_matrix_tenant_access ON scheduling_setup_matrix AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS scheduling_setup_matrix_entry (
  tenant_id uuid NOT NULL,
  matrix_id uuid NOT NULL,
  from_setup_group text NOT NULL CHECK (char_length(from_setup_group) BETWEEN 1 AND 32),
  to_setup_group text NOT NULL CHECK (char_length(to_setup_group) BETWEEN 1 AND 32),
  changeover_minutes integer NOT NULL CHECK (changeover_minutes BETWEEN 0 AND 10080),
  changeover_cost numeric(20,6) NOT NULL DEFAULT 0 CHECK (changeover_cost >= 0),
  cleaning_operation_set_id text CHECK (char_length(cleaning_operation_set_id) BETWEEN 1 AND 64),
  PRIMARY KEY (tenant_id, matrix_id, from_setup_group, to_setup_group),
  CHECK (from_setup_group <> to_setup_group),
  FOREIGN KEY (tenant_id, matrix_id) REFERENCES scheduling_setup_matrix (tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE scheduling_setup_matrix_entry ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_setup_matrix_entry FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_setup_matrix_entry_tenant_access ON scheduling_setup_matrix_entry AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- BR-23.13: a cleanout is due after N batches or M hours, whichever first,
-- and at campaign end when at_campaign_end. A rule needs at least one trigger.
CREATE TABLE IF NOT EXISTS scheduling_cleanout_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  setup_group text NOT NULL CHECK (char_length(setup_group) BETWEEN 1 AND 32),
  duration_minutes integer NOT NULL CHECK (duration_minutes BETWEEN 1 AND 10080),
  operation_set_id text CHECK (char_length(operation_set_id) BETWEEN 1 AND 64),
  after_batches integer CHECK (after_batches BETWEEN 1 AND 10000),
  after_hours integer CHECK (after_hours BETWEEN 1 AND 8760),
  at_campaign_end boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, setup_group),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (after_batches IS NOT NULL OR after_hours IS NOT NULL OR at_campaign_end)
);
ALTER TABLE scheduling_cleanout_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_cleanout_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_cleanout_rule_tenant_access ON scheduling_cleanout_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A campaign runs contiguously on one resource. plan holds the last computed
-- sequence: changeovers (the first is the leading setup), cleanouts and the
-- SCHED-0012 pairs without a matrix entry.
CREATE TABLE IF NOT EXISTS scheduling_campaign (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  resource_id uuid NOT NULL,
  campaign_key text NOT NULL CHECK (char_length(campaign_key) BETWEEN 1 AND 64),
  status text NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed', 'planned', 'started', 'completed', 'cancelled')),
  planned_start timestamptz NOT NULL,
  planned_end timestamptz,
  max_orders integer CHECK (max_orders BETWEEN 1 AND 1000),
  max_qty numeric(18,6) CHECK (max_qty > 0),
  max_minutes integer CHECK (max_minutes BETWEEN 1 AND 1000000),
  sequence_rule text NOT NULL DEFAULT 'nearest-neighbor' CHECK (sequence_rule IN ('nearest-neighbor', 'manual')),
  current_setup_group text CHECK (char_length(current_setup_group) BETWEEN 1 AND 32),
  run_minutes integer NOT NULL DEFAULT 0 CHECK (run_minutes >= 0),
  total_changeover_minutes integer NOT NULL DEFAULT 0 CHECK (total_changeover_minutes >= 0),
  cleanout_minutes integer NOT NULL DEFAULT 0 CHECK (cleanout_minutes >= 0),
  plan jsonb,
  sequenced_at timestamptz,
  started_at timestamptz,
  cleanout_order_id uuid,
  setup_order_id uuid,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, resource_id) REFERENCES scheduling_resource (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS scheduling_campaign_created ON scheduling_campaign (tenant_id, created_at DESC, id DESC);
ALTER TABLE scheduling_campaign ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_campaign FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_campaign_tenant_access ON scheduling_campaign AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- An order belongs to at most one campaign (UNIQUE tenant_id, order_id).
CREATE TABLE IF NOT EXISTS scheduling_campaign_order (
  tenant_id uuid NOT NULL,
  campaign_id uuid NOT NULL,
  order_id uuid NOT NULL,
  setup_group text NOT NULL CHECK (char_length(setup_group) BETWEEN 1 AND 32),
  quantity numeric(18,6) NOT NULL CHECK (quantity > 0),
  run_minutes integer NOT NULL CHECK (run_minutes BETWEEN 0 AND 100000),
  route_setup_minutes integer NOT NULL DEFAULT 0 CHECK (route_setup_minutes BETWEEN 0 AND 10080),
  due_at timestamptz,
  position integer NOT NULL CHECK (position >= 1),
  sequence integer CHECK (sequence >= 1),
  PRIMARY KEY (tenant_id, campaign_id, order_id),
  UNIQUE (tenant_id, order_id),
  FOREIGN KEY (tenant_id, campaign_id) REFERENCES scheduling_campaign (tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE scheduling_campaign_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_campaign_order FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_campaign_order_tenant_access ON scheduling_campaign_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A block is either weekly (windows: shifts in the resource's time zone) or
-- dated (start_at/end_at), never both. condition is a list of predicates.
CREATE TABLE IF NOT EXISTS scheduling_capacity_block (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  resource_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,31}$'),
  windows jsonb CHECK (windows IS NULL OR (jsonb_typeof(windows) = 'array' AND jsonb_array_length(windows) BETWEEN 1 AND 14)),
  start_at timestamptz,
  end_at timestamptz,
  condition jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(condition) = 'array'),
  priority integer NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 1000),
  active boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, resource_id, code),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((windows IS NOT NULL AND start_at IS NULL AND end_at IS NULL)
      OR (windows IS NULL AND start_at IS NOT NULL AND end_at > start_at)),
  FOREIGN KEY (tenant_id, resource_id) REFERENCES scheduling_resource (tenant_id, id)
);
ALTER TABLE scheduling_capacity_block ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_capacity_block FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_capacity_block_tenant_access ON scheduling_capacity_block AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON scheduling_resource_network, scheduling_network_edge, scheduling_setup_matrix, scheduling_setup_matrix_entry, scheduling_cleanout_rule, scheduling_campaign, scheduling_campaign_order, scheduling_capacity_block TO passage_app';
  END IF;
END
$$;
