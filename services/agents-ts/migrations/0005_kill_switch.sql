-- Kill switch shared by every pod (AC-47.6, BR-47.5, FR-47.12). Each toggle is one row with a version from
-- kill_switch_version_seq; every pod polls `version > applied` and applies the rows in order, and a pod that
-- cannot read the table for 10 s fails closed (KillSwitchRegistry). Toggles take a transaction-scoped advisory
-- lock before they draw a version, so versions commit in order and a poll can never skip one.
--
-- tenant_id is the tenant of the operator who wrote the row (the RLS write check). target_tenant_id is the tenant
-- the switch halts, NULL for the global switch. Every pod must read every toggle, whatever tenant it serves, so
-- reading is open; the rows carry no business data and the API never shows a global operator's user id.

CREATE SEQUENCE IF NOT EXISTS kill_switch_version_seq;

CREATE TABLE IF NOT EXISTS kill_switch_change (
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT nextval('kill_switch_version_seq') PRIMARY KEY,
  scope text NOT NULL CHECK (scope IN ('GLOBAL', 'TENANT')),
  target_tenant_id uuid,
  active boolean NOT NULL,
  by_user text NOT NULL,
  changed_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((scope = 'GLOBAL') = (target_tenant_id IS NULL)),
  CHECK (scope = 'GLOBAL' OR target_tenant_id = tenant_id)
);
CREATE INDEX IF NOT EXISTS kill_switch_change_scope ON kill_switch_change (scope, target_tenant_id, version DESC);
ALTER TABLE kill_switch_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE kill_switch_change FORCE ROW LEVEL SECURITY;
-- nullif: the pods read on pooled connections where an earlier transaction left passage.tenant_id as ''.
CREATE POLICY kill_switch_change_tenant_access ON kill_switch_change AS PERMISSIVE
  USING (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);
-- Every pod reads every toggle (allowlisted in tools/rlscheck): writes stay bound by the tenant policy above.
CREATE POLICY kill_switch_change_fleet_read ON kill_switch_change AS PERMISSIVE FOR SELECT USING (true);

-- Toggles are append-only: no UPDATE or DELETE grant.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON kill_switch_change TO passage_app';
    EXECUTE 'GRANT USAGE ON SEQUENCE kill_switch_version_seq TO passage_app';
  END IF;
END
$$;
