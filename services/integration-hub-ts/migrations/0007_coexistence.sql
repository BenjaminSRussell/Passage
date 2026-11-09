-- Integration hub: the state of the legacy coexistence adapters (wave 49, FR-49.7, AC-49.7).
-- The migration factory flips an entity kind of a legacy company between LEGACY (the legacy
-- system masters it and its adapter brings the changes in) and PASSAGE (cut over: the adapter's
-- flow is suspended), and a cutover rollback flips every cut-over kind of the plan's companies
-- back. The hub records the mode each adapter should be in from
-- dev.passage.migration.coexistence.changed.v1 and dev.passage.cutover.plan.rolledback.v1, in flag
-- version order, and a sweep suspends or resumes the adapter, backing off and parking a failure.

CREATE TABLE IF NOT EXISTS hub_coexistence_adapter (
  tenant_id uuid NOT NULL,
  source_system text NOT NULL CHECK (source_system IN ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text NOT NULL CHECK (length(source_company) BETWEEN 1 AND 64),
  entity text NOT NULL CHECK (length(entity) BETWEEN 1 AND 64),
  -- What the factory's flag says now, and its version (versions only grow; an older event is ignored).
  desired_mode text NOT NULL CHECK (desired_mode IN ('LEGACY', 'PASSAGE')),
  desired_version integer NOT NULL CHECK (desired_version >= 1),
  -- What the adapter was last told successfully (null, 0: never).
  applied_mode text CHECK (applied_mode IS NULL OR applied_mode IN ('LEGACY', 'PASSAGE')),
  applied_version integer NOT NULL DEFAULT 0 CHECK (applied_version >= 0),
  status text NOT NULL CHECK (status IN ('pending', 'applied', 'parked')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  last_code text NOT NULL DEFAULT '',
  last_error text NOT NULL DEFAULT '' CHECK (length(last_error) <= 1000),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  -- The cutover plan whose rollback asked for the change, when one did.
  plan_id uuid,
  last_event_id text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, source_system, source_company, entity),
  CHECK (status <> 'applied' OR (applied_mode = desired_mode AND applied_version = desired_version))
);
CREATE INDEX IF NOT EXISTS hub_coexistence_adapter_due ON hub_coexistence_adapter (next_attempt_at) WHERE status = 'pending';

ALTER TABLE hub_coexistence_adapter ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_coexistence_adapter FORCE ROW LEVEL SECURITY;
CREATE POLICY hub_coexistence_adapter_tenant_access ON hub_coexistence_adapter AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The sweep finds due rows across tenants as the retry poller does (0001): an owner-only policy and
-- a SECURITY DEFINER function that returns keys only, pins search_path with pg_temp last and only
-- passage_app may execute. The sweep then works inside each tenant under the tenant policy.
DO $do$
BEGIN
  IF current_user = 'passage_app' THEN
    RAISE EXCEPTION 'integration-hub migrations must run as the owner role, not passage_app';
  END IF;
  EXECUTE 'CREATE POLICY hub_coexistence_adapter_owner_scan ON hub_coexistence_adapter AS PERMISSIVE FOR SELECT TO CURRENT_USER USING (true)';
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION %1$I.hub_coexistence_due(now_ts timestamptz, max_rows integer)
    RETURNS TABLE (tenant_id uuid, source_system text, source_company text, entity text)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = %1$I, pg_temp AS $body$
      SELECT a.tenant_id, a.source_system, a.source_company, a.entity
        FROM %1$I.hub_coexistence_adapter a
       WHERE a.status = 'pending' AND a.next_attempt_at <= now_ts
       ORDER BY a.next_attempt_at, a.tenant_id, a.source_system, a.source_company, a.entity
       LIMIT greatest(least(max_rows, 1000), 1)
    $body$
    $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION hub_coexistence_due(timestamptz, integer) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON hub_coexistence_adapter TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION hub_coexistence_due(timestamptz, integer) TO passage_app';
  END IF;
END
$$;
