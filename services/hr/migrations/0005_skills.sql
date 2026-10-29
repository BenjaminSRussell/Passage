-- Skills and certifications (FR-42.10) and skill mapping (FR-42.18). A skill
-- is a catalog entry with a rating scale 1..scale_max; a worker's profile is
-- one current rating per skill (no row = not rated, read as 0). A position's
-- skill profile is its minimum rating per skill; gap analysis compares the
-- two (AC-42.16).
CREATE TABLE IF NOT EXISTS hr_skill (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  scale_max int NOT NULL CHECK (scale_max BETWEEN 1 AND 10),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);

-- The rating is checked against the skill's scale in the store (the scale is
-- on another row); the column check is the widest scale.
CREATE TABLE IF NOT EXISTS worker_skill (
  tenant_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  skill_id uuid NOT NULL,
  rating int NOT NULL CHECK (rating BETWEEN 1 AND 10),
  assessed_on date NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, worker_id, skill_id),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  FOREIGN KEY (tenant_id, skill_id) REFERENCES hr_skill (tenant_id, id)
);
-- Skill search (8.42.19: "GIN on WorkerSkill (skill_id, rating)"): a btree on
-- (skill, rating) answers "rated at least n on skill s" as a range scan.
CREATE INDEX IF NOT EXISTS worker_skill_search ON worker_skill (tenant_id, skill_id, rating);

-- A certificate a worker holds, valid on [issued_on, expires_on); a NULL
-- expiry never expires (BR-42.8). The code names the certificate type.
CREATE TABLE IF NOT EXISTS hr_certification (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  worker_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  issuer text CHECK (issuer IS NULL OR length(issuer) BETWEEN 1 AND 120),
  issued_on date NOT NULL,
  expires_on date,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, worker_id, code, issued_on),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  CHECK (expires_on IS NULL OR expires_on > issued_on)
);
CREATE INDEX IF NOT EXISTS hr_certification_expiry ON hr_certification (tenant_id, expires_on) WHERE expires_on IS NOT NULL;

CREATE TABLE IF NOT EXISTS position_skill_requirement (
  tenant_id uuid NOT NULL,
  position_id uuid NOT NULL,
  skill_id uuid NOT NULL,
  min_rating int NOT NULL CHECK (min_rating BETWEEN 1 AND 10),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, position_id, skill_id),
  FOREIGN KEY (tenant_id, position_id) REFERENCES hr_position (tenant_id, id),
  FOREIGN KEY (tenant_id, skill_id) REFERENCES hr_skill (tenant_id, id)
);

ALTER TABLE hr_skill ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_skill FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_skill_tenant_access ON hr_skill AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE worker_skill ENABLE ROW LEVEL SECURITY;
ALTER TABLE worker_skill FORCE ROW LEVEL SECURITY;
CREATE POLICY worker_skill_tenant_access ON worker_skill AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_certification ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_certification FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_certification_tenant_access ON hr_certification AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE position_skill_requirement ENABLE ROW LEVEL SECURITY;
ALTER TABLE position_skill_requirement FORCE ROW LEVEL SECURITY;
CREATE POLICY position_skill_requirement_tenant_access ON position_skill_requirement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application-role grants (0004_grants.sql pattern): the catalog and
-- certificates are append-only; ratings and position requirements are
-- upserted, and a requirement can be removed.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON hr_skill, hr_certification TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON worker_skill TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON position_skill_requirement TO passage_app';
  END IF;
END
$$;
