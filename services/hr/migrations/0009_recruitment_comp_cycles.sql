CREATE TABLE IF NOT EXISTS hr_requisition (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  requisition_number text NOT NULL CHECK (requisition_number ~ '^[A-Z0-9][A-Z0-9-]{0,19}$'),
  position_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  headcount int NOT NULL CHECK (headcount BETWEEN 1 AND 500),
  hiring_manager_worker_id uuid NOT NULL,
  planned_start_date date NOT NULL,
  allow_overfill boolean NOT NULL DEFAULT false,
  status text NOT NULL CHECK (status IN ('DRAFT', 'APPROVED', 'OPEN', 'FILLED', 'CANCELED')),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, requisition_number),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, position_id) REFERENCES hr_position (tenant_id, id),
  FOREIGN KEY (tenant_id, hiring_manager_worker_id) REFERENCES worker (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS hr_candidate (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  requisition_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  stage text NOT NULL CHECK (stage IN ('APPLIED', 'SCREEN', 'INTERVIEW', 'OFFER', 'HIRED', 'REJECTED')),
  source text CHECK (source IS NULL OR length(source) BETWEEN 1 AND 60),
  full_name_sealed bytea,
  email_sealed bytea,
  phone_sealed bytea,
  stage_changed_at timestamptz NOT NULL DEFAULT now(),
  erased_at timestamptz,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, requisition_id) REFERENCES hr_requisition (tenant_id, id),
  CHECK ((erased_at IS NULL) = (full_name_sealed IS NOT NULL)),
  CHECK (erased_at IS NULL OR (email_sealed IS NULL AND phone_sealed IS NULL))
);
CREATE INDEX IF NOT EXISTS hr_candidate_requisition ON hr_candidate (tenant_id, requisition_id, stage);

CREATE TABLE IF NOT EXISTS hr_consent_record (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  subject_type text NOT NULL CHECK (subject_type = 'CANDIDATE'),
  subject_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  purpose text NOT NULL CHECK (purpose ~ '^[A-Z0-9][A-Z0-9_-]{0,39}$'),
  granted_at timestamptz NOT NULL DEFAULT now(),
  granted_by text NOT NULL,
  withdrawn_at timestamptz,
  withdrawn_by text,
  PRIMARY KEY (tenant_id, id),
  CHECK ((withdrawn_at IS NULL) = (withdrawn_by IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS hr_consent_record_active ON hr_consent_record (tenant_id, subject_type, subject_id, purpose) WHERE withdrawn_at IS NULL;

CREATE TABLE IF NOT EXISTS hr_retention_policy (
  tenant_id uuid NOT NULL,
  data_class text NOT NULL CHECK (data_class IN ('CANDIDATE')),
  retention_months int NOT NULL CHECK (retention_months BETWEEN 1 AND 240),
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, data_class)
);

CREATE TABLE IF NOT EXISTS hr_offer (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  candidate_id uuid NOT NULL,
  requisition_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  amount numeric(20, 6) NOT NULL CHECK (amount >= 0),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  start_date date NOT NULL,
  status text NOT NULL CHECK (status IN ('DRAFT', 'SENT', 'ACCEPTED', 'DECLINED', 'WITHDRAWN')),
  hire_action_id uuid,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, candidate_id) REFERENCES hr_candidate (tenant_id, id),
  FOREIGN KEY (tenant_id, requisition_id) REFERENCES hr_requisition (tenant_id, id),
  FOREIGN KEY (tenant_id, hire_action_id) REFERENCES hr_personnel_action (tenant_id, id),
  CHECK (hire_action_id IS NULL OR status = 'ACCEPTED')
);
CREATE INDEX IF NOT EXISTS hr_offer_requisition ON hr_offer (tenant_id, requisition_id, status);

CREATE TABLE IF NOT EXISTS hr_comp_cycle (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  kind text NOT NULL CHECK (kind IN ('MERIT', 'BONUS', 'BOTH')),
  effective_date date NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  allow_over_budget boolean NOT NULL DEFAULT false,
  guideline_matrix jsonb NOT NULL,
  status text NOT NULL CHECK (status IN ('PLANNING', 'MANAGER_INPUT', 'APPROVAL', 'APPLIED', 'CANCELED')),
  applied_at timestamptz,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((status = 'APPLIED') = (applied_at IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS hr_comp_cycle_budget (
  tenant_id uuid NOT NULL,
  cycle_id uuid NOT NULL,
  department_id uuid NOT NULL,
  budget_amount numeric(20, 6) NOT NULL CHECK (budget_amount >= 0),
  PRIMARY KEY (tenant_id, cycle_id, department_id),
  FOREIGN KEY (tenant_id, cycle_id) REFERENCES hr_comp_cycle (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS hr_comp_worksheet_row (
  tenant_id uuid NOT NULL,
  cycle_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  manager_worker_id uuid NOT NULL,
  employment_id uuid NOT NULL,
  department_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  current_pay numeric(20, 6) NOT NULL,
  rating int NOT NULL CHECK (rating BETWEEN 1 AND 5),
  compa_ratio numeric(8, 2),
  guideline_pct_min numeric(8, 3),
  guideline_pct_max numeric(8, 3),
  proposed_pct numeric(8, 3) NOT NULL,
  proposed_amount numeric(20, 6) NOT NULL,
  justification text CHECK (justification IS NULL OR length(justification) BETWEEN 1 AND 1000),
  status text NOT NULL CHECK (status IN ('DRAFT', 'SUBMITTED')),
  action_id uuid,
  PRIMARY KEY (tenant_id, cycle_id, worker_id),
  FOREIGN KEY (tenant_id, cycle_id) REFERENCES hr_comp_cycle (tenant_id, id),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  FOREIGN KEY (tenant_id, manager_worker_id) REFERENCES worker (tenant_id, id),
  FOREIGN KEY (tenant_id, employment_id) REFERENCES employment (tenant_id, id),
  FOREIGN KEY (tenant_id, action_id) REFERENCES hr_personnel_action (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS hr_comp_worksheet_row_manager ON hr_comp_worksheet_row (tenant_id, cycle_id, manager_worker_id);

ALTER TABLE hr_requisition ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_requisition FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_requisition_tenant_access ON hr_requisition AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY hr_requisition_legal_entity_scope ON hr_requisition AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE hr_candidate ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_candidate FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_candidate_tenant_access ON hr_candidate AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY hr_candidate_legal_entity_scope ON hr_candidate AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE hr_consent_record ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_consent_record FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_consent_record_tenant_access ON hr_consent_record AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY hr_consent_record_legal_entity_scope ON hr_consent_record AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE hr_retention_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_retention_policy FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_retention_policy_tenant_access ON hr_retention_policy AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_offer ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_offer FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_offer_tenant_access ON hr_offer AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY hr_offer_legal_entity_scope ON hr_offer AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE hr_comp_cycle ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_comp_cycle FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_comp_cycle_tenant_access ON hr_comp_cycle AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_comp_cycle_budget ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_comp_cycle_budget FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_comp_cycle_budget_tenant_access ON hr_comp_cycle_budget AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_comp_worksheet_row ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_comp_worksheet_row FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_comp_worksheet_row_tenant_access ON hr_comp_worksheet_row AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY hr_comp_worksheet_row_legal_entity_scope ON hr_comp_worksheet_row AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON hr_requisition, hr_candidate, hr_offer, hr_comp_cycle, hr_comp_worksheet_row, hr_retention_policy TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON hr_consent_record TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON hr_comp_cycle_budget TO passage_app';
  END IF;
END
$$;
