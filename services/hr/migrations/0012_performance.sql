CREATE TABLE IF NOT EXISTS job_skill_requirement (
  tenant_id uuid NOT NULL,
  job_id uuid NOT NULL,
  skill_id uuid NOT NULL,
  min_rating int NOT NULL CHECK (min_rating BETWEEN 1 AND 10),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, job_id, skill_id),
  FOREIGN KEY (tenant_id, job_id) REFERENCES hr_job (tenant_id, id),
  FOREIGN KEY (tenant_id, skill_id) REFERENCES hr_skill (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS hr_goal (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  worker_id uuid NOT NULL,
  legal_entity_id uuid not null,
  title text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  description text CHECK (description IS NULL OR length(description) BETWEEN 1 AND 2000),
  due_on date,
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'ACHIEVED', 'CANCELLED')),
  progress_pct int NOT NULL DEFAULT 0 CHECK (progress_pct BETWEEN 0 AND 100),
  created_by text NOT NULL,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS hr_goal_worker ON hr_goal (tenant_id, worker_id, created_at);

CREATE TABLE IF NOT EXISTS hr_review (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  worker_id uuid NOT NULL,
  legal_entity_id uuid not null,
  period_start date NOT NULL,
  period_end date NOT NULL,
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'self_submitted', 'signed_off')),
  self_achievements text CHECK (self_achievements IS NULL OR length(self_achievements) BETWEEN 1 AND 4000),
  self_development text CHECK (self_development IS NULL OR length(self_development) BETWEEN 1 AND 4000),
  self_submitted_at timestamptz,
  manager_comments text CHECK (manager_comments IS NULL OR length(manager_comments) BETWEEN 1 AND 4000),
  overall_rating int CHECK (overall_rating IS NULL OR overall_rating BETWEEN 1 AND 5),
  signed_off_by text,
  signed_off_at timestamptz,
  created_by text NOT NULL,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, worker_id, period_start, period_end),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  CHECK (period_end >= period_start),
  CHECK ((status = 'draft') = (self_submitted_at IS NULL)),
  CHECK ((status = 'signed_off') = (signed_off_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS hr_review_worker ON hr_review (tenant_id, worker_id, period_start);

ALTER TABLE job_skill_requirement ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_skill_requirement FORCE ROW LEVEL SECURITY;
CREATE POLICY job_skill_requirement_tenant_access ON job_skill_requirement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_goal ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_goal FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_goal_tenant_access ON hr_goal AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY hr_goal_legal_entity_scope ON hr_goal AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE hr_review ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_review FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_review_tenant_access ON hr_review AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY hr_review_legal_entity_scope ON hr_review AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON job_skill_requirement TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON hr_goal, hr_review TO passage_app';
  END IF;
END
$$;
