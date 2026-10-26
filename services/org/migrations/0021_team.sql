CREATE TABLE IF NOT EXISTS team_type (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
  member_kinds text[] NOT NULL CHECK (cardinality(member_kinds) >= 1 AND member_kinds <@ ARRAY['user', 'party']),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE team_type ENABLE ROW LEVEL SECURITY;
ALTER TABLE team_type FORCE ROW LEVEL SECURITY;
CREATE POLICY team_type_tenant_access ON team_type AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS team (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
  description text CHECK (char_length(description) <= 500),
  team_type_id uuid NOT NULL,
  operating_unit_id uuid,
  effective_from date NOT NULL,
  effective_to date,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  membership_rule text,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, team_type_id) REFERENCES team_type (tenant_id, id),
  FOREIGN KEY (tenant_id, operating_unit_id) REFERENCES operating_unit (tenant_id, id),
  CHECK (effective_to IS NULL OR effective_to >= effective_from)
);
CREATE INDEX IF NOT EXISTS team_team_type ON team (tenant_id, team_type_id);
CREATE INDEX IF NOT EXISTS team_operating_unit ON team (tenant_id, operating_unit_id);
ALTER TABLE team ENABLE ROW LEVEL SECURITY;
ALTER TABLE team FORCE ROW LEVEL SECURITY;
CREATE POLICY team_tenant_access ON team AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
