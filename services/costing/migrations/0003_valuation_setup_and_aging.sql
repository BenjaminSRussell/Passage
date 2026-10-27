-- Wave 20 slice 4: valuation setup registries (FR-20.15, FR-20.16), saved aging
-- bucket sets and stored month-end aging snapshots (FR-20.17). Additive only.

-- A valuation ledger binds one costing method, currency and exchange rate type
-- to a wave 12 ledger group. At most one ledger per legal entity is primary (its
-- results drive cost_amount_adjustment, BR-20.13), and a ledger group carries at
-- most one valuation ledger, so a close never posts two adjustments to it.
CREATE TABLE IF NOT EXISTS valuation_ledger (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_group_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,31}$'),
  method text NOT NULL CHECK (method IN ('FIFO', 'LIFO', 'LIFO_DATE', 'WEIGHTED_AVG_RUNNING',
                                         'WEIGHTED_AVG_PERIODIC', 'MOVING_AVG', 'STANDARD')),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  rate_type text NOT NULL CHECK (rate_type IN ('SPOT', 'AVERAGE', 'HISTORICAL')),
  is_primary boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT valuation_ledger_code_unique UNIQUE (tenant_id, legal_entity_id, code),
  CONSTRAINT valuation_ledger_group_unique UNIQUE (tenant_id, legal_entity_id, ledger_group_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS valuation_ledger_one_primary
  ON valuation_ledger (tenant_id, legal_entity_id) WHERE is_primary;
ALTER TABLE valuation_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE valuation_ledger FORCE ROW LEVEL SECURITY;
CREATE POLICY valuation_ledger_tenant_access ON valuation_ledger AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY valuation_ledger_legal_entity_scope ON valuation_ledger AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- A valuation category splits a product's stock into valuation types (SAP
-- OMWC/OMWD). It is tenant-wide (3.20.13). types is a jsonb array of
-- {code, account}: the account is the inventory account of that type (the
-- valuation class equivalent). A BATCH category has no fixed types, because each
-- batch becomes its own valuation type at receipt (BR-20.14).
CREATE TABLE IF NOT EXISTS valuation_category (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,31}$'),
  is_batch boolean NOT NULL DEFAULT false,
  types jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(types) = 'array'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT valuation_category_code_unique UNIQUE (tenant_id, code),
  CHECK (is_batch = (jsonb_array_length(types) = 0))
);
ALTER TABLE valuation_category ENABLE ROW LEVEL SECURITY;
ALTER TABLE valuation_category FORCE ROW LEVEL SECURITY;
CREATE POLICY valuation_category_tenant_access ON valuation_category AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Aging bucket sets are saved per legal entity; bounds is the ascending list of
-- upper bounds in days (the last bucket is open-ended).
CREATE TABLE IF NOT EXISTS aging_bucket_set (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
  bounds jsonb NOT NULL CHECK (jsonb_typeof(bounds) = 'array' AND jsonb_array_length(bounds) BETWEEN 1 AND 12),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT aging_bucket_set_name_unique UNIQUE (tenant_id, legal_entity_id, name)
);
ALTER TABLE aging_bucket_set ENABLE ROW LEVEL SECURITY;
ALTER TABLE aging_bucket_set FORCE ROW LEVEL SECURITY;
CREATE POLICY aging_bucket_set_tenant_access ON aging_bucket_set AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY aging_bucket_set_legal_entity_scope ON aging_bucket_set AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- A month-end aging snapshot is evidence (3.20.15): it is written once and never
-- updated. body holds the whole snapshot (buckets, totals, lines); the columns
-- serve lookup, listing and idempotency. body_hash is the SHA-256 of what the
-- snapshot computed, so a reused Idempotency-Key with other input is refused.
CREATE TABLE IF NOT EXISTS aging_snapshot (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  valuation_ledger_id uuid,
  as_of date NOT NULL,
  method text NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  total_value numeric(20,6) NOT NULL,
  idempotency_key text NOT NULL,
  body_hash text NOT NULL,
  body jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT aging_snapshot_idempotency UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS aging_snapshot_entity_asof ON aging_snapshot (tenant_id, legal_entity_id, as_of DESC, id DESC);
ALTER TABLE aging_snapshot ENABLE ROW LEVEL SECURITY;
ALTER TABLE aging_snapshot FORCE ROW LEVEL SECURITY;
CREATE POLICY aging_snapshot_tenant_access ON aging_snapshot AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY aging_snapshot_legal_entity_scope ON aging_snapshot AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
