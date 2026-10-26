CREATE TABLE IF NOT EXISTS team_member (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  team_id uuid NOT NULL,
  member_kind text NOT NULL CHECK (member_kind IN ('user', 'party')),
  member_id uuid NOT NULL,
  member_role text NOT NULL DEFAULT 'member' CHECK (member_role IN ('member', 'lead')),
  valid_from date NOT NULL,
  valid_to date,
  source text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'rule')),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, team_id, member_kind, member_id, valid_from),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, team_id) REFERENCES team (tenant_id, id),
  CHECK (valid_to IS NULL OR valid_to >= valid_from)
);
CREATE INDEX IF NOT EXISTS team_member_team ON team_member (tenant_id, team_id);
CREATE INDEX IF NOT EXISTS team_member_member ON team_member (tenant_id, member_id);
ALTER TABLE team_member ENABLE ROW LEVEL SECURITY;
ALTER TABLE team_member FORCE ROW LEVEL SECURITY;
CREATE POLICY team_member_tenant_access ON team_member AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
