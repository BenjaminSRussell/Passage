-- Pricing context, wave 26 slice 1: condition records with validity, quantity
-- breaks and the activation overlap guard (FR-26.2, BR-26.6, BR-26.9).

CREATE TABLE IF NOT EXISTS condition_record (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  condition_type text NOT NULL CHECK (condition_type IN ('PRICE', 'LINE_DISC', 'TOTAL_DISC')),
  customer_kind text NOT NULL CHECK (customer_kind IN ('customer', 'customer_group', 'all')),
  customer_key text NOT NULL DEFAULT '',
  product_kind text NOT NULL CHECK (product_kind IN ('product', 'product_group', 'variant', 'all')),
  product_key text NOT NULL DEFAULT '',
  variant text NOT NULL DEFAULT '',
  currency char(3) NOT NULL,
  unit text NOT NULL DEFAULT '',
  from_quantity numeric(19,6) NOT NULL DEFAULT 0 CHECK (from_quantity >= 0),
  amount numeric(19,6) NOT NULL DEFAULT 0 CHECK (amount >= 0),
  disc_pct_1 numeric(7,4) NOT NULL DEFAULT 0 CHECK (disc_pct_1 BETWEEN 0 AND 100),
  disc_pct_2 numeric(7,4) NOT NULL DEFAULT 0 CHECK (disc_pct_2 BETWEEN 0 AND 100),
  valid_from date NOT NULL,
  valid_to date NOT NULL,
  search_next boolean NOT NULL DEFAULT false,
  exclusive boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'ACTIVE', 'EXPIRED', 'WITHDRAWN')),
  version integer NOT NULL DEFAULT 1,
  key_hash text NOT NULL,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (valid_from <= valid_to),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS condition_record_resolve_ix ON condition_record (tenant_id, legal_entity_id, condition_type, status, currency);
CREATE INDEX IF NOT EXISTS condition_record_key_ix ON condition_record (tenant_id, key_hash) WHERE status = 'ACTIVE';

ALTER TABLE condition_record ENABLE ROW LEVEL SECURITY;
ALTER TABLE condition_record FORCE ROW LEVEL SECURITY;
CREATE POLICY condition_record_tenant_access ON condition_record AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY condition_record_legal_entity_scope ON condition_record AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

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
