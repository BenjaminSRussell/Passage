-- Courses and learning (FR-42.14, BR-42.9): a catalog course with the skills
-- it trains and the certificate it issues, scheduled sessions with a seat
-- capacity and a waitlist, and registrations whose completion with PASS
-- writes worker_skill and hr_certification rows. Every table carries
-- tenant_id and a tenant policy; money is numeric with a currency, never
-- float (CONVENTIONS).
CREATE TABLE IF NOT EXISTS hr_course (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  course_group text CHECK (course_group IS NULL OR length(course_group) BETWEEN 1 AND 60),
  -- The certificate a PASS issues. There is no certificate-type catalog yet
  -- (slice 4 keeps certificate codes as free text), so the course names it.
  certification_code text CHECK (certification_code IS NULL OR certification_code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  certification_name text CHECK (certification_name IS NULL OR length(certification_name) BETWEEN 1 AND 120),
  -- 0 = the certificate does not expire.
  validity_months int NOT NULL DEFAULT 0 CHECK (validity_months BETWEEN 0 AND 240),
  requires_manager_approval boolean NOT NULL DEFAULT false,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((certification_code IS NULL) = (certification_name IS NULL)),
  CHECK (validity_months = 0 OR certification_code IS NOT NULL)
);

-- course.skill_outcomes[] (8.42.4) as rows, so each outcome references a
-- catalog skill. The rating is checked against the skill's scale in the store.
CREATE TABLE IF NOT EXISTS hr_course_skill_outcome (
  tenant_id uuid NOT NULL,
  course_id uuid NOT NULL,
  skill_id uuid NOT NULL,
  rating int NOT NULL CHECK (rating BETWEEN 1 AND 10),
  PRIMARY KEY (tenant_id, course_id, skill_id),
  FOREIGN KEY (tenant_id, course_id) REFERENCES hr_course (tenant_id, id),
  FOREIGN KEY (tenant_id, skill_id) REFERENCES hr_skill (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS hr_course_session (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  course_id uuid NOT NULL,
  starts_at timestamptz NOT NULL,
  ends_at timestamptz NOT NULL,
  -- One of a location or a virtual link (8.42.4). Locations and classrooms
  -- are free text until a location catalog exists.
  location_text text CHECK (location_text IS NULL OR length(location_text) BETWEEN 1 AND 200),
  virtual_url text CHECK (virtual_url IS NULL OR length(virtual_url) BETWEEN 1 AND 500),
  instructor_worker_id uuid,
  instructor_party_id uuid,
  capacity int NOT NULL CHECK (capacity BETWEEN 1 AND 10000),
  waitlist_capacity int NOT NULL CHECK (waitlist_capacity BETWEEN 0 AND 100),
  cost_per_attendee numeric(20, 6) CHECK (cost_per_attendee IS NULL OR cost_per_attendee >= 0),
  currency char(3) CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, course_id) REFERENCES hr_course (tenant_id, id),
  FOREIGN KEY (tenant_id, instructor_worker_id) REFERENCES worker (tenant_id, id),
  CHECK (ends_at > starts_at),
  CHECK (location_text IS NOT NULL OR virtual_url IS NOT NULL),
  CHECK (instructor_worker_id IS NULL OR instructor_party_id IS NULL),
  CHECK ((cost_per_attendee IS NULL) = (currency IS NULL))
);
CREATE INDEX IF NOT EXISTS hr_course_session_course ON hr_course_session (tenant_id, course_id, starts_at);

-- One registration per worker and session (the PRD's idempotency "key per
-- (worker, session)"). queued_at is the registration order seats and the
-- waitlist follow (BR-42.9); registering again after a cancel queues anew.
CREATE TABLE IF NOT EXISTS hr_course_registration (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  session_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('REQUESTED', 'CONFIRMED', 'WAITLISTED', 'ATTENDED', 'NO_SHOW', 'CANCELED')),
  result text CHECK (result IS NULL OR result IN ('PASS', 'FAIL')),
  score numeric(5, 2) CHECK (score IS NULL OR score >= 0),
  completed_on date,
  queued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, session_id, worker_id),
  FOREIGN KEY (tenant_id, session_id) REFERENCES hr_course_session (tenant_id, id),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  CHECK ((result IS NULL AND score IS NULL) OR status = 'ATTENDED'),
  CHECK ((completed_on IS NOT NULL) = (status IN ('ATTENDED', 'NO_SHOW')))
);
CREATE INDEX IF NOT EXISTS hr_course_registration_queue ON hr_course_registration (tenant_id, session_id, status, queued_at);
CREATE INDEX IF NOT EXISTS hr_course_registration_worker ON hr_course_registration (tenant_id, worker_id);

ALTER TABLE hr_course ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_course FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_course_tenant_access ON hr_course AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_course_skill_outcome ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_course_skill_outcome FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_course_skill_outcome_tenant_access ON hr_course_skill_outcome AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_course_session ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_course_session FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_course_session_tenant_access ON hr_course_session AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_course_registration ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_course_registration FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_course_registration_tenant_access ON hr_course_registration AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application-role grants (0004_grants.sql pattern): courses and their skill
-- outcomes are write-once. A session is write-once but its row is locked FOR
-- UPDATE to allocate seats one at a time, which needs UPDATE. Registrations
-- change status. Nothing is deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON hr_course, hr_course_skill_outcome TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON hr_course_session, hr_course_registration TO passage_app';
  END IF;
END
$$;
