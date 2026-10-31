-- Injury and illness register (FR-42.16, BR-42.11), loaned items (FR-42.17) and
-- onboarding/offboarding checklists with worker tasks (FR-42.19, BR-42.12).
-- Every table carries tenant_id and a tenant policy. The free-text medical
-- details of an incident (description, witnesses, treatments, root cause,
-- corrective action) are one fieldcrypt-sealed JSON value, never plaintext.

CREATE TABLE IF NOT EXISTS hr_incident (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  case_no int NOT NULL CHECK (case_no > 0),
  worker_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  site_id uuid NOT NULL,
  incident_type text NOT NULL CHECK (incident_type IN ('INJURY', 'SKIN_DISORDER', 'RESPIRATORY', 'POISONING', 'HEARING_LOSS', 'OTHER_ILLNESS')),
  occurred_at timestamptz NOT NULL,
  reported_at timestamptz NOT NULL,
  location_text text NOT NULL CHECK (length(location_text) BETWEEN 1 AND 200),
  body_parts text[] NOT NULL DEFAULT '{}' CHECK (cardinality(body_parts) <= 20),
  death boolean NOT NULL DEFAULT false,
  loss_of_consciousness boolean NOT NULL DEFAULT false,
  medical_treatment boolean NOT NULL DEFAULT false,
  days_away int NOT NULL DEFAULT 0 CHECK (days_away BETWEEN 0 AND 365),
  days_restricted int NOT NULL DEFAULT 0 CHECK (days_restricted BETWEEN 0 AND 365),
  osha_recordable boolean NOT NULL,
  recordable_override_reason text CHECK (recordable_override_reason IS NULL OR length(recordable_override_reason) BETWEEN 10 AND 500),
  outcome text NOT NULL CHECK (outcome IN ('DEATH', 'DAYS_AWAY', 'RESTRICTED', 'OTHER_RECORDABLE', 'NONE')),
  privacy_case boolean NOT NULL DEFAULT false,
  details_sealed bytea NOT NULL,
  asset_id uuid,
  status text NOT NULL DEFAULT 'REPORTED' CHECK (status IN ('REPORTED', 'UNDER_REVIEW', 'CLOSED')),
  supersedes_id uuid,
  reported_by text NOT NULL,
  reviewed_by text,
  closed_by text,
  closed_at timestamptz,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, case_no),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  FOREIGN KEY (tenant_id, supersedes_id) REFERENCES hr_incident (tenant_id, id),
  CHECK (reported_at >= occurred_at),
  CHECK ((status = 'CLOSED') = (closed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS hr_incident_year ON hr_incident (tenant_id, legal_entity_id, site_id, occurred_at);
CREATE INDEX IF NOT EXISTS hr_incident_worker ON hr_incident (tenant_id, worker_id);
CREATE UNIQUE INDEX IF NOT EXISTS hr_incident_one_correction ON hr_incident (tenant_id, supersedes_id) WHERE supersedes_id IS NOT NULL;

-- BR-42.11: a CLOSED incident is immutable; corrections are new rows linked by
-- supersedes_id.
CREATE OR REPLACE FUNCTION hr_incident_closed_immutable() RETURNS trigger AS $fn$
BEGIN
  IF OLD.status = 'CLOSED' THEN
    RAISE EXCEPTION 'closed incident % is immutable; record a correction that supersedes it', OLD.id
      USING ERRCODE = 'integrity_constraint_violation';
  END IF;
  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$fn$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS hr_incident_closed_immutable ON hr_incident;
CREATE TRIGGER hr_incident_closed_immutable BEFORE UPDATE OR DELETE ON hr_incident
  FOR EACH ROW EXECUTE FUNCTION hr_incident_closed_immutable();

-- Case numbers (the PRD's HCM-INC sequence until wave-5 numbering exists). A
-- counter row, not max(case_no), because a caller scoped to one legal entity
-- cannot see every incident and would pick a number already taken.
CREATE TABLE IF NOT EXISTS hr_incident_counter (
  tenant_id uuid PRIMARY KEY,
  last_case_no int NOT NULL CHECK (last_case_no > 0)
);

CREATE TABLE IF NOT EXISTS hr_loaned_item (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  worker_id uuid NOT NULL,
  loan_type text NOT NULL CHECK (loan_type ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  item_description text NOT NULL CHECK (length(item_description) BETWEEN 1 AND 200),
  asset_id uuid,
  serial text CHECK (serial IS NULL OR length(serial) BETWEEN 1 AND 80),
  issued_on date NOT NULL,
  due_on date,
  closed_on date,
  condition_out text CHECK (condition_out IS NULL OR length(condition_out) BETWEEN 1 AND 200),
  condition_in text CHECK (condition_in IS NULL OR length(condition_in) BETWEEN 1 AND 200),
  status text NOT NULL DEFAULT 'OUT' CHECK (status IN ('OUT', 'RETURNED', 'LOST')),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  CHECK (due_on IS NULL OR due_on >= issued_on),
  CHECK ((status = 'OUT') = (closed_on IS NULL)),
  CHECK (closed_on IS NULL OR closed_on >= issued_on)
);
CREATE INDEX IF NOT EXISTS hr_loaned_item_worker ON hr_loaned_item (tenant_id, worker_id, status);
CREATE INDEX IF NOT EXISTS hr_loaned_item_due ON hr_loaned_item (tenant_id, due_on) WHERE status = 'OUT';

CREATE TABLE IF NOT EXISTS hr_checklist_template (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  kind text NOT NULL CHECK (kind IN ('ONBOARDING', 'OFFBOARDING', 'TRANSFER')),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS hr_checklist_template_task (
  tenant_id uuid NOT NULL,
  template_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,29}$'),
  position int NOT NULL CHECK (position >= 0),
  title text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  owner_role text NOT NULL CHECK (owner_role IN ('MANAGER', 'WORKER', 'HR_PARTNER', 'IT')),
  due_offset_days int NOT NULL CHECK (due_offset_days BETWEEN -365 AND 365),
  instructions text CHECK (instructions IS NULL OR length(instructions) <= 2000),
  evidence_required boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, template_id, code),
  FOREIGN KEY (tenant_id, template_id) REFERENCES hr_checklist_template (tenant_id, id)
);

-- One checklist per worker, template and effective date: starting it again
-- returns the stored one. The effective date is the personnel action's date
-- (BR-42.12); personnel actions arrive with FR-42.5 and will call the same path.
CREATE TABLE IF NOT EXISTS hr_checklist (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  worker_id uuid NOT NULL,
  template_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('ONBOARDING', 'OFFBOARDING', 'TRANSFER')),
  effective_date date NOT NULL,
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'COMPLETED')),
  completed_at timestamptz,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, worker_id, template_id, effective_date),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  FOREIGN KEY (tenant_id, template_id) REFERENCES hr_checklist_template (tenant_id, id),
  CHECK ((status = 'COMPLETED') = (completed_at IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS hr_worker_task (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  checklist_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  task_code text NOT NULL,
  title text NOT NULL,
  owner_role text NOT NULL CHECK (owner_role IN ('MANAGER', 'WORKER', 'HR_PARTNER', 'IT')),
  owner_worker_id uuid,
  due_on date NOT NULL,
  evidence_required boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'DONE', 'SKIPPED')),
  evidence_ref text CHECK (evidence_ref IS NULL OR length(evidence_ref) BETWEEN 1 AND 500),
  completed_by text,
  completed_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, checklist_id, task_code),
  FOREIGN KEY (tenant_id, checklist_id) REFERENCES hr_checklist (tenant_id, id),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  FOREIGN KEY (tenant_id, owner_worker_id) REFERENCES worker (tenant_id, id),
  CHECK ((status = 'OPEN') = (completed_at IS NULL))
);
CREATE INDEX IF NOT EXISTS hr_worker_task_owner ON hr_worker_task (tenant_id, owner_worker_id, status, due_on);
CREATE INDEX IF NOT EXISTS hr_worker_task_checklist ON hr_worker_task (tenant_id, checklist_id);

ALTER TABLE hr_incident ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_incident FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_incident_tenant_access ON hr_incident AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY hr_incident_legal_entity_scope ON hr_incident AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE hr_incident_counter ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_incident_counter FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_incident_counter_tenant_access ON hr_incident_counter AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_loaned_item ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_loaned_item FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_loaned_item_tenant_access ON hr_loaned_item AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_checklist_template ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_checklist_template FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_checklist_template_tenant_access ON hr_checklist_template AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_checklist_template_task ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_checklist_template_task FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_checklist_template_task_tenant_access ON hr_checklist_template_task AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_checklist ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_checklist FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_checklist_tenant_access ON hr_checklist AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_worker_task ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_worker_task FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_worker_task_tenant_access ON hr_worker_task AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application-role grants (0004_grants.sql pattern). An incident changes only
-- its workflow columns, and the trigger above freezes it once CLOSED. Loans and
-- tasks change status; templates, their tasks and checklists are write-once
-- apart from a checklist completing. Nothing is deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON hr_incident, hr_checklist_template, hr_checklist_template_task TO passage_app';
    EXECUTE 'GRANT UPDATE (status, reviewed_by, closed_by, closed_at, updated_at) ON hr_incident TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON hr_incident_counter, hr_loaned_item, hr_checklist, hr_worker_task TO passage_app';
  END IF;
END
$$;
