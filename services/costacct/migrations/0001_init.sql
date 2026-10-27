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

-- Cost objects (FR-38.6): cost centers, internal orders, products, projects as references.
CREATE TABLE IF NOT EXISTS cost_object (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('CostCenter', 'InternalOrder', 'Product', 'Project')),
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{2,40}$'),
  name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, code)
);

-- Cost elements (FR-38.6): primary elements map a main account range, secondary elements carry allocations.
CREATE TABLE IF NOT EXISTS cost_element (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('Primary', 'Secondary')),
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{2,40}$'),
  name text NOT NULL,
  main_account_from text,
  main_account_to text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, code),
  CHECK ((kind = 'Primary') = (main_account_from IS NOT NULL AND main_account_to IS NOT NULL)),
  CHECK (main_account_from IS NULL OR main_account_from <= main_account_to)
);

-- Allocation runs (FR-38.7): one completed run per legal entity and period.
CREATE TABLE IF NOT EXISTS allocation_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  period_key text NOT NULL CHECK (period_key ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  currency char(3) NOT NULL,
  secondary_element_id uuid NOT NULL,
  method text NOT NULL,
  iterations integer NOT NULL CHECK (iterations >= 0),
  residual numeric(28,12) NOT NULL,
  converged boolean NOT NULL,
  swept numeric(19,4) NOT NULL DEFAULT 0,
  total_in numeric(19,4) NOT NULL,
  total_out numeric(19,4) NOT NULL,
  status text NOT NULL CHECK (status = 'Completed'),
  cost_object_ids jsonb NOT NULL,
  flows jsonb NOT NULL,
  postings jsonb NOT NULL,
  unallocated jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (total_in = total_out),
  FOREIGN KEY (tenant_id, secondary_element_id) REFERENCES cost_element (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS allocation_run_one_completed
  ON allocation_run (tenant_id, legal_entity_id, period_key) WHERE status = 'Completed';

-- Cost entries (FR-38.6): append-only. Journal mirrors are unique per journal line.
CREATE TABLE IF NOT EXISTS cost_entry (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  cost_object_id uuid NOT NULL,
  cost_element_id uuid NOT NULL,
  period_key text NOT NULL CHECK (period_key ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  currency char(3) NOT NULL,
  amount numeric(19,4) NOT NULL,
  behavior text NOT NULL DEFAULT 'Unclassified' CHECK (behavior IN ('Fixed', 'Variable', 'Unclassified')),
  origin text NOT NULL CHECK (origin IN ('Journal', 'Allocation', 'Activity', 'Statistical', 'Manual')),
  source_id uuid NOT NULL,
  allocation_run_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, cost_object_id) REFERENCES cost_object (tenant_id, id),
  FOREIGN KEY (tenant_id, cost_element_id) REFERENCES cost_element (tenant_id, id),
  FOREIGN KEY (tenant_id, allocation_run_id) REFERENCES allocation_run (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS cost_entry_journal_once
  ON cost_entry (tenant_id, source_id) WHERE origin = 'Journal';
CREATE INDEX IF NOT EXISTS cost_entry_object_element_period
  ON cost_entry (tenant_id, cost_object_id, cost_element_id, period_key);

ALTER TABLE cost_object ENABLE ROW LEVEL SECURITY;
ALTER TABLE cost_object FORCE ROW LEVEL SECURITY;
ALTER TABLE cost_element ENABLE ROW LEVEL SECURITY;
ALTER TABLE cost_element FORCE ROW LEVEL SECURITY;
ALTER TABLE allocation_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE allocation_run FORCE ROW LEVEL SECURITY;
ALTER TABLE cost_entry ENABLE ROW LEVEL SECURITY;
ALTER TABLE cost_entry FORCE ROW LEVEL SECURITY;

CREATE POLICY cost_object_tenant_access ON cost_object AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY cost_object_legal_entity_scope ON cost_object AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE POLICY cost_element_tenant_access ON cost_element AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY cost_element_legal_entity_scope ON cost_element AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE POLICY allocation_run_tenant_access ON allocation_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY allocation_run_legal_entity_scope ON allocation_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE POLICY cost_entry_tenant_access ON cost_entry AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY cost_entry_legal_entity_scope ON cost_entry AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
