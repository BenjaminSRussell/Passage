-- Integration hub: webhook subscriptions, deliveries and delivery attempts (FR-35.9).
-- Every table carries tenant_id with ENABLE + FORCE row-level security and the
-- tenant policy. passage_app is granted exactly what the store does.

CREATE TABLE IF NOT EXISTS hub_subscription (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  url text NOT NULL,
  -- The webhook signing secret, sealed with field encryption (fieldcrypt envelope
  -- v1, field hub_subscription.secret, bound to tenant_id). Never plain text.
  secret_sealed bytea NOT NULL CHECK (substring(secret_sealed FROM 1 FOR 1) = '\x01'::bytea),
  patterns text[] NOT NULL CHECK (cardinality(patterns) BETWEEN 1 AND 50),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
  paused_reason text,
  paused_at timestamptz,
  version integer NOT NULL DEFAULT 1,
  -- A poller replica owns the subscription until this unix time (seconds).
  lease_until double precision,
  idempotency_key text,
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE hub_subscription ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_subscription FORCE ROW LEVEL SECURITY;
CREATE POLICY hub_subscription_tenant_access ON hub_subscription AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS hub_delivery (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  event_id text NOT NULL,
  event_type text NOT NULL,
  aggregate_id text NOT NULL,
  sequence bigint NOT NULL,
  occurred_at double precision NOT NULL,
  data jsonb NOT NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'dead_letter')),
  retries_done integer NOT NULL DEFAULT 0,
  next_retry_at double precision NOT NULL,
  replay boolean NOT NULL DEFAULT false,
  attempts integer NOT NULL DEFAULT 0,
  last_status integer,
  dead_letter_code text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, subscription_id, event_id),
  FOREIGN KEY (tenant_id, subscription_id) REFERENCES hub_subscription (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS hub_delivery_due ON hub_delivery (status, next_retry_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS hub_delivery_list ON hub_delivery (tenant_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS hub_delivery_pending ON hub_delivery (tenant_id, subscription_id, aggregate_id, sequence) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS hub_delivery_archive ON hub_delivery (tenant_id, subscription_id, occurred_at, id) WHERE status <> 'pending';
ALTER TABLE hub_delivery ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_delivery FORCE ROW LEVEL SECURITY;
CREATE POLICY hub_delivery_tenant_access ON hub_delivery AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS hub_attempt (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  delivery_id uuid NOT NULL,
  attempt_no integer NOT NULL,
  http_status integer,
  attempted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, delivery_id) REFERENCES hub_delivery (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS hub_attempt_delivery ON hub_attempt (tenant_id, delivery_id, attempt_no);
ALTER TABLE hub_attempt ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_attempt FORCE ROW LEVEL SECURITY;
CREATE POLICY hub_attempt_tenant_access ON hub_attempt AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The retry poller has no tenant until it finds one. It asks hub_due_subscriptions,
-- which reads across tenants as the table owner: a policy for the owner role only
-- (never passage_app) lets the owner see every row despite FORCE ROW LEVEL SECURITY.
-- The function returns (tenant, subscription) ids only, takes no input but the
-- clock, pins search_path to this schema with pg_temp last (a caller's temporary
-- table cannot stand in for a hub table), and only passage_app may execute it. The
-- poller then works inside each tenant under the tenant policies.
-- Migrations must run as the owner role: run as passage_app, the application role
-- would own the tables and could switch row-level security off.
DO $do$
BEGIN
  IF current_user = 'passage_app' THEN
    RAISE EXCEPTION 'integration-hub migrations must run as the owner role, not passage_app';
  END IF;
  EXECUTE 'CREATE POLICY hub_delivery_owner_scan ON hub_delivery AS PERMISSIVE FOR SELECT TO CURRENT_USER USING (true)';
  EXECUTE 'CREATE POLICY hub_subscription_owner_scan ON hub_subscription AS PERMISSIVE FOR SELECT TO CURRENT_USER USING (true)';
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION %1$I.hub_due_subscriptions(now_seconds double precision)
    RETURNS TABLE (tenant_id uuid, subscription_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = %1$I, pg_temp AS $body$
      SELECT DISTINCT d.tenant_id, d.subscription_id
        FROM %1$I.hub_delivery d
        JOIN %1$I.hub_subscription s ON s.tenant_id = d.tenant_id AND s.id = d.subscription_id
       WHERE d.status = 'pending' AND d.next_retry_at <= now_seconds AND s.status = 'active'
         AND (s.lease_until IS NULL OR s.lease_until <= now_seconds)
    $body$
    $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION hub_due_subscriptions(double precision) FROM PUBLIC;

-- Application role privileges (skipped when the role does not exist: developer
-- databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON hub_subscription TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON hub_delivery TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON hub_attempt TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION hub_due_subscriptions(double precision) TO passage_app';
  END IF;
END
$$;
