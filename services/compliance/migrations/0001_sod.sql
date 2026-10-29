-- Segregation-of-duties conflicts and their mitigations (FR-48.4, FR-48.5, BR-48.1).
-- Rules are the FR-48.4 seed set shipped in code; tenant-maintained rules and the
-- GRC import (sod_rule) arrive with a later slice.
CREATE TABLE IF NOT EXISTS sod_conflict (
  tenant_id uuid NOT NULL,
  conflict_id uuid NOT NULL,
  user_id uuid NOT NULL,
  rule_id text NOT NULL CHECK (rule_id ~ '^[A-Z0-9]+-[0-9]{3}$'),
  risk_id text NOT NULL,
  severity text NOT NULL CHECK (severity IN ('HIGH', 'MEDIUM', 'LOW')),
  duty_a text NOT NULL,
  duty_b text NOT NULL,
  roles_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  state text NOT NULL DEFAULT 'OPEN' CHECK (state IN ('OPEN', 'MITIGATED', 'RESOLVED', 'ACCEPTED_RISK')),
  mitigation_id uuid,
  mitigation_expires_at timestamptz,
  decided_by text,
  decision_reason text,
  detected_at timestamptz NOT NULL,
  resolved_at timestamptz,
  updated_at timestamptz NOT NULL,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  PRIMARY KEY (tenant_id, conflict_id),
  UNIQUE (tenant_id, user_id, rule_id),
  CHECK ((state = 'MITIGATED') = (mitigation_id IS NOT NULL AND mitigation_expires_at IS NOT NULL)),
  CHECK (state <> 'ACCEPTED_RISK' OR severity <> 'HIGH')
);
CREATE INDEX IF NOT EXISTS sod_conflict_state_severity ON sod_conflict (tenant_id, state, severity);
CREATE INDEX IF NOT EXISTS sod_conflict_listing ON sod_conflict (tenant_id, detected_at DESC, conflict_id DESC);
CREATE INDEX IF NOT EXISTS sod_conflict_mitigation_expiry ON sod_conflict (tenant_id, mitigation_expires_at) WHERE state = 'MITIGATED';

-- Append-only record of every approved mitigation (AC2: the workflow records approvals).
CREATE TABLE IF NOT EXISTS sod_mitigation (
  tenant_id uuid NOT NULL,
  mitigation_id uuid NOT NULL,
  conflict_id uuid NOT NULL,
  control_ref text NOT NULL CHECK (btrim(control_ref) <> ''),
  reason text NOT NULL CHECK (btrim(reason) <> ''),
  approver_id text NOT NULL CHECK (btrim(approver_id) <> ''),
  approved_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL CHECK (expires_at > approved_at),
  PRIMARY KEY (tenant_id, mitigation_id),
  FOREIGN KEY (tenant_id, conflict_id) REFERENCES sod_conflict (tenant_id, conflict_id)
);
CREATE INDEX IF NOT EXISTS sod_mitigation_conflict ON sod_mitigation (tenant_id, conflict_id, approved_at DESC);

ALTER TABLE sod_conflict ENABLE ROW LEVEL SECURITY;
ALTER TABLE sod_conflict FORCE ROW LEVEL SECURITY;
CREATE POLICY sod_conflict_tenant_access ON sod_conflict AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE sod_mitigation ENABLE ROW LEVEL SECURITY;
ALTER TABLE sod_mitigation FORCE ROW LEVEL SECURITY;
CREATE POLICY sod_mitigation_tenant_access ON sod_mitigation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
