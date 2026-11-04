-- Integration hub: API client registry with per-client quotas and usage (FR-35.8,
-- BR-35.8) and the transactional outbox for the hub's own events.
-- The client secret lives in Keycloak (client credentials); this table is the
-- hub's registry of the Keycloak client: scopes, quota, sandbox tenant, owner.
-- Every table carries tenant_id with ENABLE + FORCE row-level security and the
-- tenant policy. passage_app is granted exactly what the store does: clients are
-- revoked, never deleted; buckets and usage are upserted; the outbox is insert-only.

CREATE TABLE IF NOT EXISTS hub_api_client (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  -- The Keycloak client id the edge sees in the verified token.
  client_id text NOT NULL CHECK (client_id ~ '^[a-z0-9][a-z0-9._-]{2,63}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  -- Scopes are duty keys the client may use (scopes mapped to duties, FR-35.8).
  scopes text[] NOT NULL CHECK (cardinality(scopes) BETWEEN 1 AND 50),
  quota_per_min integer NOT NULL DEFAULT 600 CHECK (quota_per_min BETWEEN 10 AND 100000),
  burst integer NOT NULL DEFAULT 100 CHECK (burst BETWEEN 1 AND 100000),
  sandbox_tenant_id uuid,
  owner_contact text NOT NULL CHECK (length(owner_contact) BETWEEN 3 AND 320),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
  revoked_at timestamptz,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, client_id),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS hub_api_client_list ON hub_api_client (tenant_id, created_at DESC, id DESC);
ALTER TABLE hub_api_client ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_api_client FORCE ROW LEVEL SECURITY;
CREATE POLICY hub_api_client_tenant_access ON hub_api_client AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The token bucket of one client (src/quota.ts): fractional tokens and the
-- millisecond clock of the last refill. Locked FOR UPDATE by a consume.
CREATE TABLE IF NOT EXISTS hub_client_bucket (
  tenant_id uuid NOT NULL,
  api_client_id uuid NOT NULL,
  tokens double precision NOT NULL,
  updated_at_ms double precision NOT NULL,
  PRIMARY KEY (tenant_id, api_client_id),
  FOREIGN KEY (tenant_id, api_client_id) REFERENCES hub_api_client (tenant_id, id)
);
ALTER TABLE hub_client_bucket ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_client_bucket FORCE ROW LEVEL SECURITY;
CREATE POLICY hub_client_bucket_tenant_access ON hub_client_bucket AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Requests per client per minute (the portal usage chart, AC-35.6): units
-- admitted and units refused (a request costs one unit, GraphQL one per 100 points).
CREATE TABLE IF NOT EXISTS hub_client_usage (
  tenant_id uuid NOT NULL,
  api_client_id uuid NOT NULL,
  minute timestamptz NOT NULL,
  allowed bigint NOT NULL DEFAULT 0 CHECK (allowed >= 0),
  throttled bigint NOT NULL DEFAULT 0 CHECK (throttled >= 0),
  PRIMARY KEY (tenant_id, api_client_id, minute),
  FOREIGN KEY (tenant_id, api_client_id) REFERENCES hub_api_client (tenant_id, id)
);
ALTER TABLE hub_client_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_client_usage FORCE ROW LEVEL SECURITY;
CREATE POLICY hub_client_usage_tenant_access ON hub_client_usage AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Transactional outbox, relayed by Debezium (kernel dbx.OutboxDDL); the payload is
-- the whole CloudEvents envelope, as platform/go/outbox writes it.
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

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON hub_api_client TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON hub_client_bucket TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON hub_client_usage TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON outbox TO passage_app';
  END IF;
END
$$;
