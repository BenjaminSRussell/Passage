-- Revrec slice 2: revenue contracts with their performance obligations and
-- recognition schedules (FR-29.7, FR-29.8), the recognition run record (one per
-- legal entity and period end, FR-29.8) and the transactional outbox relayed by
-- Debezium. Every table carries tenant_id first and enables row-level security
-- (dbx.RLSPolicy); tables with legal_entity_id also carry the restrictive
-- legal-entity scope policy (dbx.LegalEntityScopePolicy). Obligations and
-- schedule lines are reached only through their contract, so the contract's
-- legal-entity policy scopes them as well.

CREATE TABLE IF NOT EXISTS revenue_contract (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  currency char(3) NOT NULL,
  transaction_price numeric(20,6) NOT NULL CHECK (transaction_price >= 0),
  residual_rule text NOT NULL CHECK (residual_rule IN ('LARGEST', 'FIRST', 'LAST')),
  -- The invoice date that opens the deferral; the roll-forward books additions on it.
  contract_date date NOT NULL,
  status text NOT NULL CHECK (status IN ('OPEN', 'COMPLETE', 'CANCELLED')),
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE revenue_contract ENABLE ROW LEVEL SECURITY;
ALTER TABLE revenue_contract FORCE ROW LEVEL SECURITY;
CREATE POLICY revenue_contract_tenant_access ON revenue_contract AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY revenue_contract_legal_entity_scope ON revenue_contract AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
-- Contracts list (newest first) and the roll-forward scan per entity and currency.
CREATE INDEX IF NOT EXISTS revenue_contract_created_idx ON revenue_contract (tenant_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS revenue_contract_entity_idx ON revenue_contract (tenant_id, legal_entity_id, currency, contract_date);

CREATE TABLE IF NOT EXISTS performance_obligation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  contract_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  product_id uuid NOT NULL,
  ssp_amount numeric(20,6) NOT NULL CHECK (ssp_amount > 0),
  ssp_source text NOT NULL CHECK (ssp_source IN ('EXPLICIT', 'LIST', 'PCT_OF_LIST', 'RESIDUAL')),
  allocated_amount numeric(20,6) NOT NULL CHECK (allocated_amount >= 0),
  method text NOT NULL CHECK (method IN ('POINT_IN_TIME', 'RATABLE', 'MILESTONE', 'USAGE')),
  term_start date,
  term_end date,
  satisfied_on date,
  deferred_account text NOT NULL DEFAULT '2400',
  revenue_account text NOT NULL DEFAULT '4000',
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, contract_id, line_no),
  FOREIGN KEY (tenant_id, contract_id) REFERENCES revenue_contract (tenant_id, id),
  CHECK (method <> 'RATABLE' OR (term_start IS NOT NULL AND term_end IS NOT NULL AND term_end >= term_start)),
  CHECK (method <> 'POINT_IN_TIME' OR satisfied_on IS NOT NULL)
);
ALTER TABLE performance_obligation ENABLE ROW LEVEL SECURITY;
ALTER TABLE performance_obligation FORCE ROW LEVEL SECURITY;
CREATE POLICY performance_obligation_tenant_access ON performance_obligation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- One row per planned recognition. scheduled_date is null for a milestone
-- awaiting satisfaction; a RECOGNIZED line always carries recognized_on.
CREATE TABLE IF NOT EXISTS revenue_schedule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  obligation_id uuid NOT NULL,
  seq integer NOT NULL CHECK (seq > 0),
  scheduled_date date,
  amount numeric(20,6) NOT NULL CHECK (amount >= 0),
  status text NOT NULL CHECK (status IN ('SCHEDULED', 'RECOGNIZED', 'REVERSED')),
  recognized_on date,
  journal_posting_id uuid,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, obligation_id, seq),
  FOREIGN KEY (tenant_id, obligation_id) REFERENCES performance_obligation (tenant_id, id),
  CHECK (status <> 'RECOGNIZED' OR recognized_on IS NOT NULL)
);
ALTER TABLE revenue_schedule ENABLE ROW LEVEL SECURITY;
ALTER TABLE revenue_schedule FORCE ROW LEVEL SECURITY;
CREATE POLICY revenue_schedule_tenant_access ON revenue_schedule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
-- The recognition run scans due lines by status and date (§5.29.4 schedules (period, status)).
CREATE INDEX IF NOT EXISTS revenue_schedule_status_idx ON revenue_schedule (tenant_id, status, scheduled_date);

-- A recognition run: key = (legal entity, period end), so a rerun of a period
-- returns the recorded run and recognizes nothing twice.
CREATE TABLE IF NOT EXISTS recognition_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  period_end date NOT NULL,
  line_count integer NOT NULL CHECK (line_count >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, period_end)
);
ALTER TABLE recognition_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE recognition_run FORCE ROW LEVEL SECURITY;
CREATE POLICY recognition_run_tenant_access ON recognition_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY recognition_run_legal_entity_scope ON recognition_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
CREATE INDEX IF NOT EXISTS recognition_run_created_idx ON recognition_run (tenant_id, period_end DESC, created_at DESC);

-- Transactional outbox relayed by Debezium (platform dbx.OutboxDDL); excluded from RLS.
CREATE TABLE IF NOT EXISTS outbox (
  id text primary key,
  tenant_id uuid not null,
  aggregate_type text not null,
  aggregate_id text not null,
  type text not null,
  payload jsonb not null,
  created_at timestamptz not null default now()
);
ALTER TABLE outbox REPLICA IDENTITY FULL;
