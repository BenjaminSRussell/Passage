-- Ops control plane (PSG-50, FR-50.5, BR-50.1). Platform state, not tenant data: no tenant_id and no RLS
-- (allowlisted in tools/rlscheck), reached only by the PlatformAdmin and PlatformView duties.
CREATE TABLE ops_region (
  name        text PRIMARY KEY,
  role        text NOT NULL CHECK (role IN ('PRIMARY', 'PASSIVE', 'FENCED')),
  lag_seconds integer NOT NULL DEFAULT 0 CHECK (lag_seconds >= 0),
  version     bigint NOT NULL DEFAULT 1,
  updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Never two PRIMARY regions (BR-50.1): at most one row may hold the role.
CREATE UNIQUE INDEX ops_region_one_primary ON ops_region ((role)) WHERE role = 'PRIMARY';

-- One row per failover, keyed by the caller's event id (Idempotency-Key), so a retried request returns the first outcome.
CREATE TABLE ops_failover (
  event_id         text PRIMARY KEY,
  from_region      text NOT NULL REFERENCES ops_region (name),
  to_region        text NOT NULL REFERENCES ops_region (name),
  accept_data_loss boolean NOT NULL,
  lag_seconds      integer NOT NULL,
  requested_by     text NOT NULL,
  approved_by      text,
  plan             jsonb NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now()
);
