-- Lean manufacturing, wave 24 slice 1 (PRD 4.24.4): production flows with
-- their activities, kanban rules, kanban cards, idempotency records and the
-- transactional outbox. Every table carries tenant_id first and has tenant RLS
-- (dbx.RLSPolicy). None carries legal_entity_id: flows, rules and cards belong
-- to a site. Kanban jobs and flow costing periods arrive with the slices that
-- persist them (see NOT-IMPLEMENTED.md).

CREATE TABLE IF NOT EXISTS production_flow (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  -- The wave-6 operating unit of type value stream.
  value_stream_id uuid NOT NULL,
  site_id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  takt_time_s integer CHECK (takt_time_s IS NULL OR takt_time_s > 0),
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'expired')),
  active_from date,
  active_to date,
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (active_to IS NULL OR active_from IS NULL OR active_to >= active_from),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, value_stream_id, name),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS production_flow_list_ix ON production_flow (tenant_id, created_at DESC, id DESC);

-- Activities of a flow. A process activity without an output location is
-- allowed in a draft and refused on activation (FR-24.8, LEAN-0004).
CREATE TABLE IF NOT EXISTS flow_activity (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  flow_id uuid NOT NULL,
  sequence integer NOT NULL CHECK (sequence >= 1),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
  kind text NOT NULL CHECK (kind IN ('process', 'transfer', 'service')),
  cycle_time_s integer NOT NULL DEFAULT 0 CHECK (cycle_time_s >= 0),
  work_cell_resource_id uuid,
  input_location_id uuid,
  output_location_id uuid,
  predecessor_ids uuid[] NOT NULL DEFAULT '{}',
  picking_required boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, flow_id, sequence),
  UNIQUE (tenant_id, flow_id, name),
  FOREIGN KEY (tenant_id, flow_id) REFERENCES production_flow (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS kanban_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_.-]{0,31}$'),
  item_id uuid NOT NULL,
  site_id uuid NOT NULL,
  flow_id uuid,
  rule_type text NOT NULL CHECK (rule_type IN ('manufacturing', 'withdrawal')),
  replenishment text NOT NULL CHECK (replenishment IN ('fixed', 'scheduled', 'event')),
  fixed_qty numeric(18,6) NOT NULL CHECK (fixed_qty > 0),
  number_of_kanbans integer CHECK (number_of_kanbans IS NULL OR number_of_kanbans BETWEEN 1 AND 1000),
  lead_time_h integer NOT NULL DEFAULT 0 CHECK (lead_time_h BETWEEN 0 AND 8760),
  event_source text CHECK (event_source IS NULL OR event_source IN ('sales', 'bom', 'kanban', 'min-stock')),
  exact_qty boolean NOT NULL DEFAULT false,
  -- Next card number of this rule (cards are numbered <code>-0001, -0002, ...).
  next_card integer NOT NULL DEFAULT 1 CHECK (next_card >= 1),
  effective_from date NOT NULL,
  effective_to date,
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (effective_to IS NULL OR effective_to >= effective_from),
  CHECK (replenishment <> 'fixed' OR number_of_kanbans IS NOT NULL),
  CHECK (replenishment <> 'event' OR event_source IS NOT NULL),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, flow_id) REFERENCES production_flow (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS kanban (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  rule_id uuid NOT NULL,
  card_number text NOT NULL,
  qty numeric(18,6) NOT NULL CHECK (qty > 0),
  state text NOT NULL CHECK (state IN ('not-planned', 'planned', 'prepared', 'in-progress', 'completed', 'emptied')),
  due timestamptz,
  event_ref_type text CHECK (event_ref_type IS NULL OR char_length(event_ref_type) BETWEEN 1 AND 40),
  event_ref_id text CHECK (event_ref_id IS NULL OR char_length(event_ref_id) BETWEEN 1 AND 80),
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, rule_id, card_number),
  FOREIGN KEY (tenant_id, rule_id) REFERENCES kanban_rule (tenant_id, id)
);
-- PRD 4.24.4 index: Kanban (rule_id, state).
CREATE INDEX IF NOT EXISTS kanban_rule_state_ix ON kanban (tenant_id, rule_id, state);

-- Replay records for commands whose result is not a single new row (activate,
-- generate, move). Same shape as dbx.IdempotencyDDL.
CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int NOT NULL,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);

-- Transactional outbox (dbx.OutboxDDL); Debezium relays rows to Kafka.
CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Tenant isolation on every table (dbx.RLSPolicy).
ALTER TABLE production_flow ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_flow FORCE ROW LEVEL SECURITY;
CREATE POLICY production_flow_tenant_access ON production_flow AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE flow_activity ENABLE ROW LEVEL SECURITY;
ALTER TABLE flow_activity FORCE ROW LEVEL SECURITY;
CREATE POLICY flow_activity_tenant_access ON flow_activity AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE kanban_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE kanban_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY kanban_rule_tenant_access ON kanban_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE kanban ENABLE ROW LEVEL SECURITY;
ALTER TABLE kanban FORCE ROW LEVEL SECURITY;
CREATE POLICY kanban_tenant_access ON kanban AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY idempotency_keys_tenant_access ON idempotency_keys AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The application role reads and writes through RLS; it never owns the tables.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON production_flow, flow_activity, kanban_rule, kanban, idempotency_keys, outbox TO passage_app';
  END IF;
END
$$;
