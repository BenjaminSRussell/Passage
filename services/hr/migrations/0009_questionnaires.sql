-- Questionnaire engine (FR-42.15, BR-42.10). A questionnaire carries its
-- questions as one validated JSON document (type, answer group, weight,
-- result group); a schedule targets a population by a CEL rule over worker
-- facts and snapshots how many it invited. An anonymous schedule stores no
-- respondent id on the answer session; hr_questionnaire_responded only stops a
-- second answer and is never joined to a session.
CREATE TABLE IF NOT EXISTS hr_questionnaire (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  anonymous boolean NOT NULL DEFAULT false,
  questions jsonb NOT NULL CHECK (jsonb_typeof(questions) = 'array' AND jsonb_array_length(questions) BETWEEN 1 AND 100),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS hr_questionnaire_schedule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  questionnaire_id uuid NOT NULL,
  population text NOT NULL DEFAULT '' CHECK (length(population) <= 500),
  opens_at timestamptz NOT NULL,
  closes_at timestamptz NOT NULL,
  invited_count int NOT NULL CHECK (invited_count >= 0),
  idempotency_key text,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, questionnaire_id) REFERENCES hr_questionnaire (tenant_id, id),
  CHECK (closes_at > opens_at)
);
CREATE INDEX IF NOT EXISTS hr_questionnaire_schedule_open ON hr_questionnaire_schedule (tenant_id, opens_at, closes_at);

CREATE TABLE IF NOT EXISTS hr_questionnaire_responded (
  tenant_id uuid NOT NULL,
  schedule_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  legal_entity_id uuid,
  PRIMARY KEY (tenant_id, schedule_id, worker_id),
  FOREIGN KEY (tenant_id, schedule_id) REFERENCES hr_questionnaire_schedule (tenant_id, id),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id)
);

-- respondent_worker_id is NULL on an anonymous schedule. department_id is the
-- respondent's department when they answered, kept so results can be grouped;
-- completed_on is a date, not a timestamp, so an anonymous session cannot be
-- matched to a sign-in time. scores is {result group: score}.
CREATE TABLE IF NOT EXISTS hr_questionnaire_session (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  schedule_id uuid NOT NULL,
  respondent_worker_id uuid,
  department_id uuid,
  legal_entity_id uuid,
  completed_on date NOT NULL,
  answers jsonb NOT NULL,
  scores jsonb NOT NULL DEFAULT '{}',
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, schedule_id) REFERENCES hr_questionnaire_schedule (tenant_id, id),
  FOREIGN KEY (tenant_id, respondent_worker_id) REFERENCES worker (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS hr_questionnaire_session_schedule ON hr_questionnaire_session (tenant_id, schedule_id);
CREATE UNIQUE INDEX IF NOT EXISTS hr_questionnaire_session_one_per_worker
  ON hr_questionnaire_session (tenant_id, schedule_id, respondent_worker_id) WHERE respondent_worker_id IS NOT NULL;

ALTER TABLE hr_questionnaire ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_questionnaire FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_questionnaire_tenant_access ON hr_questionnaire AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_questionnaire_schedule ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_questionnaire_schedule FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_questionnaire_schedule_tenant_access ON hr_questionnaire_schedule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_questionnaire_responded ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_questionnaire_responded FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_questionnaire_responded_tenant_access ON hr_questionnaire_responded AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_questionnaire_session ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_questionnaire_session FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_questionnaire_session_tenant_access ON hr_questionnaire_session AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE POLICY hr_questionnaire_responded_legal_entity_scope ON hr_questionnaire_responded AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
CREATE POLICY hr_questionnaire_session_legal_entity_scope ON hr_questionnaire_session AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Everything is write-once: a questionnaire, its schedule, an answer session
-- and the response marker. Nothing is updated or deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON hr_questionnaire, hr_questionnaire_schedule, hr_questionnaire_responded, hr_questionnaire_session TO passage_app';
  END IF;
END
$$;
