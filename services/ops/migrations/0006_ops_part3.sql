-- Ops control plane part 3 (PSG-50, FR-50.1, FR-50.8, FR-50.9, FR-50.12): tenant provisioning, tenant cost statements, the hourly
-- licence check with reported user counts, SLO breach state, and the outbox for the ops events. The ops tables are platform
-- state, not tenant data: the tenant they describe is a key column (customer_tenant, licensee_id), never tenant_id, and they
-- have no RLS (allowlisted in tools/rlscheck). The outbox is the platform shape with tenant RLS; the store sets the event's
-- tenant for the one insert.

ALTER TABLE ops_failover ADD COLUMN completed_at timestamptz;

CREATE TABLE ops_tenant_provision (
  customer_tenant uuid PRIMARY KEY,
  name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  residency       text NOT NULL,
  region          text NOT NULL,
  tier            text NOT NULL CHECK (tier IN ('PROD', 'DEV', 'TEST', 'UAT')),
  status          text NOT NULL DEFAULT 'PROVISIONING' CHECK (status IN ('PROVISIONING', 'PROVISIONED', 'FAILED')),
  idempotency_key text NOT NULL UNIQUE,
  request_hash    text NOT NULL,
  requested_by    text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  provisioned_at  timestamptz
);

CREATE TABLE ops_tenant_provision_step (
  customer_tenant uuid NOT NULL REFERENCES ops_tenant_provision (customer_tenant),
  action          text NOT NULL CHECK (action IN ('realm', 'databases', 'kafka-acls', 'lakehouse', 'license')),
  ord             integer NOT NULL,
  status          text NOT NULL CHECK (status IN ('DONE', 'FAILED')),
  detail          text NOT NULL DEFAULT '',
  reported_by     text NOT NULL,
  recorded_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (customer_tenant, action)
);

-- One statement per tenant and month (cost-model.md: tenant_cost, PK tenant and month). A restatement replaces the row and
-- keeps its model version, so the month is reproducible from the query version.
CREATE TABLE ops_tenant_cost (
  customer_tenant uuid NOT NULL,
  month           text NOT NULL CHECK (month ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  statement       jsonb NOT NULL,
  cost_usd        numeric(18, 2) NOT NULL CHECK (cost_usd >= 0),
  cost_eur        numeric(18, 2) NOT NULL CHECK (cost_eur >= 0),
  model_version   text NOT NULL,
  recorded_by     text NOT NULL,
  recorded_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (customer_tenant, month)
);

-- The last reported active user counts and the last evaluated state of each licence. The hourly check compares the state it
-- evaluates with last_state and writes license.changed when it differs.
CREATE TABLE ops_license_check (
  licensee_id        uuid PRIMARY KEY,
  active_full        integer CHECK (active_full >= 0),
  active_operational integer CHECK (active_operational >= 0),
  active_selfservice integer CHECK (active_selfservice >= 0),
  counted_at         timestamptz,
  reported_by        text,
  last_state         text CHECK (last_state IN ('VALID', 'GRACE', 'EXPIRED')),
  checked_at         timestamptz
);

-- One row per service the SLO check has seen breached; breached_since is cleared when it recovers.
CREATE TABLE ops_slo_state (
  service        text PRIMARY KEY,
  breached       boolean NOT NULL,
  breached_since timestamptz,
  checked_at     timestamptz NOT NULL DEFAULT now(),
  last_window    text NOT NULL
);

CREATE TABLE outbox (
  id             text PRIMARY KEY,
  tenant_id      uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id   text NOT NULL,
  type           text NOT NULL,
  payload        jsonb NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (completed_at) ON ops_failover TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ops_tenant_provision, ops_tenant_provision_step, ops_tenant_cost, ops_license_check, ops_slo_state TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON outbox TO passage_app';
  END IF;
END
$$;
