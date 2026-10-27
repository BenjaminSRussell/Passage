-- Process manufacturing, wave 24 slice 1 (PRD 4.24.4): formulas with their
-- lines, batch orders scaled from a formula snapshot, the per-legal-entity
-- batch-order number counter, idempotency records and the transactional outbox.
-- Every table carries tenant_id first and has tenant RLS (dbx.RLSPolicy); the
-- tables carrying legal_entity_id also get dbx.LegalEntityScopePolicy.
-- Batch attributes, co-product allocation on orders and the other wave-24
-- entities arrive with the slices that persist them (see NOT-IMPLEMENTED.md).

CREATE TABLE IF NOT EXISTS formula (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_.-]{0,39}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  item_id uuid NOT NULL,
  -- The wave-9 BOM version this formula specialises (FR-24.1); set when the
  -- formula is maintained through an engineering change order.
  bom_version_id uuid,
  formula_size numeric(18,6) NOT NULL CHECK (formula_size > 0),
  unit text NOT NULL CHECK (char_length(unit) BETWEEN 1 AND 10),
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS formula_line (
  tenant_id uuid NOT NULL,
  formula_id uuid NOT NULL,
  line_number integer NOT NULL CHECK (line_number > 0),
  item_id uuid NOT NULL,
  line_type text NOT NULL CHECK (line_type IN ('ingredient', 'active-ingredient', 'phantom', 'co-product', 'by-product')),
  qty numeric(18,6) NOT NULL DEFAULT 0 CHECK (qty >= 0),
  percent numeric(9,4) NOT NULL DEFAULT 0 CHECK (percent >= 0 AND percent <= 100),
  scalable boolean NOT NULL DEFAULT true,
  percent_controlled boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, formula_id, line_number),
  FOREIGN KEY (tenant_id, formula_id) REFERENCES formula (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS formula_line_item_ix ON formula_line (tenant_id, item_id);

-- Next batch-order number per legal entity (PRD 4.24.13 "Batch order number
-- sequence"). Gap-free: the counter row is locked by the creating transaction.
CREATE TABLE IF NOT EXISTS batch_order_sequence (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  next_value bigint NOT NULL CHECK (next_value > 0),
  PRIMARY KEY (tenant_id, legal_entity_id)
);

CREATE TABLE IF NOT EXISTS batch_order (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_number text NOT NULL,
  formula_id uuid NOT NULL,
  formula_code text NOT NULL,
  formula_version integer NOT NULL,
  item_id uuid NOT NULL,
  unit text NOT NULL,
  formula_size numeric(18,6) NOT NULL CHECK (formula_size > 0),
  batch_size numeric(18,6) NOT NULL CHECK (batch_size > 0),
  scale_factor numeric(18,9) NOT NULL CHECK (scale_factor > 0),
  status text NOT NULL DEFAULT 'planned' CHECK (status IN ('planned', 'released', 'started', 'reported', 'ended', 'closed')),
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by text NOT NULL DEFAULT '',
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, order_number),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, formula_id) REFERENCES formula (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS batch_order_list_ix ON batch_order (tenant_id, created_at DESC, id DESC);

-- The formula lines as the order took them (FR-24.1: an order snapshots its
-- formula at creation), with the quantity at the current batch size.
CREATE TABLE IF NOT EXISTS batch_order_line (
  tenant_id uuid NOT NULL,
  order_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  line_number integer NOT NULL CHECK (line_number > 0),
  item_id uuid NOT NULL,
  line_type text NOT NULL CHECK (line_type IN ('ingredient', 'active-ingredient', 'phantom', 'co-product', 'by-product')),
  formula_qty numeric(18,6) NOT NULL CHECK (formula_qty >= 0),
  percent numeric(9,4) NOT NULL CHECK (percent >= 0 AND percent <= 100),
  scalable boolean NOT NULL,
  percent_controlled boolean NOT NULL,
  qty numeric(18,6) NOT NULL CHECK (qty >= 0),
  PRIMARY KEY (tenant_id, order_id, line_number),
  FOREIGN KEY (tenant_id, order_id) REFERENCES batch_order (tenant_id, id)
);

-- Replay records for commands on an existing row (rescale). Same shape as
-- dbx.IdempotencyDDL.
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

-- Tenant isolation on every table (dbx.RLSPolicy), plus the legal-entity scope
-- where the row carries legal_entity_id (dbx.LegalEntityScopePolicy).
ALTER TABLE formula ENABLE ROW LEVEL SECURITY;
ALTER TABLE formula FORCE ROW LEVEL SECURITY;
CREATE POLICY formula_tenant_access ON formula AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE formula_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE formula_line FORCE ROW LEVEL SECURITY;
CREATE POLICY formula_line_tenant_access ON formula_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE batch_order_sequence ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_order_sequence FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_order_sequence_tenant_access ON batch_order_sequence AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY batch_order_sequence_legal_entity_scope ON batch_order_sequence AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE batch_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_order FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_order_tenant_access ON batch_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY batch_order_legal_entity_scope ON batch_order AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE batch_order_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_order_line FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_order_line_tenant_access ON batch_order_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY batch_order_line_legal_entity_scope ON batch_order_line AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

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
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON formula, formula_line, batch_order_sequence, batch_order, batch_order_line, idempotency_keys, outbox TO passage_app';
  END IF;
END
$$;
