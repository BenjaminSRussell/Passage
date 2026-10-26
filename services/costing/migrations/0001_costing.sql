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

-- Costing versions (AX CostingVersion, SAP CK11N estimate). STANDARD versions
-- hold the standard cost per product x site; PLANNED versions are estimates.
-- Codes are unique per legal entity: every entity has its own STD-{year}.
CREATE TABLE IF NOT EXISTS costing_version (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,31}$'),
  type text NOT NULL CHECK (type IN ('STANDARD', 'PLANNED')),
  status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'ACTIVE', 'ARCHIVED')),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  effective_from date NOT NULL,
  block_activation boolean NOT NULL DEFAULT false,
  idempotency_key text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, code),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE costing_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE costing_version FORCE ROW LEVEL SECURITY;
CREATE POLICY costing_version_tenant_access ON costing_version AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY costing_version_legal_entity_scope ON costing_version AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Item costs of one version. Money is numeric(20,6); the currency is the
-- version's. Status PENDING until activation (activation is a later slice).
CREATE TABLE IF NOT EXISTS item_cost (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  version_id uuid NOT NULL,
  product_id uuid NOT NULL,
  site_id uuid NOT NULL,
  unit_cost numeric(20,6) NOT NULL CHECK (unit_cost >= 0),
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'ACTIVE', 'SUPERSEDED')),
  source text NOT NULL DEFAULT 'IMPORT' CHECK (source IN ('MANUAL', 'IMPORT', 'COPY', 'ROLLUP')),
  effective_from date NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, version_id, product_id, site_id),
  FOREIGN KEY (tenant_id, version_id) REFERENCES costing_version (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS item_cost_product_site_status ON item_cost (tenant_id, product_id, site_id, status);
ALTER TABLE item_cost ENABLE ROW LEVEL SECURITY;
ALTER TABLE item_cost FORCE ROW LEVEL SECURITY;
CREATE POLICY item_cost_tenant_access ON item_cost AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY item_cost_legal_entity_scope ON item_cost AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
