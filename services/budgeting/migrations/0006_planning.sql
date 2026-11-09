-- Budget planning (FR-38.2, AC-38.7): a planning process per fiscal year moves
-- Draft, Submitted, Reviewed, Approved. Stage owners are named per org unit and
-- a stage completes when every org unit's owner has signed. Scenarios hold the
-- plan grid (account x cost center x period); the approved scenario becomes an
-- Original register entry on the process's control rule.
CREATE TABLE IF NOT EXISTS budget_plan_process (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  rule_version_id uuid NOT NULL,
  ledger_id uuid,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
  fiscal_year integer NOT NULL CHECK (fiscal_year BETWEEN 1900 AND 2999),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  status text NOT NULL DEFAULT 'Draft' CHECK (status IN ('Draft', 'Submitted', 'Reviewed', 'Approved')),
  submission integer NOT NULL DEFAULT 0 CHECK (submission >= 0),
  submitted_by text,
  approved_scenario_id uuid,
  register_entry_id uuid,
  version bigint NOT NULL DEFAULT 1,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, fiscal_year, name),
  FOREIGN KEY (tenant_id, rule_version_id) REFERENCES budget_control_rule (tenant_id, id)
);
ALTER TABLE budget_plan_process ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_plan_process FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_plan_process_tenant_access ON budget_plan_process AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_plan_process_legal_entity_scope ON budget_plan_process AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS budget_plan_stage_owner (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  process_id uuid NOT NULL,
  stage text NOT NULL CHECK (stage IN ('Review', 'Approve')),
  org_unit_id uuid NOT NULL,
  owner text NOT NULL CHECK (char_length(owner) BETWEEN 1 AND 200),
  signed_by text,
  signed_at timestamptz,
  PRIMARY KEY (tenant_id, process_id, stage, org_unit_id),
  FOREIGN KEY (tenant_id, process_id) REFERENCES budget_plan_process (tenant_id, id)
);
ALTER TABLE budget_plan_stage_owner ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_plan_stage_owner FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_plan_stage_owner_tenant_access ON budget_plan_stage_owner AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_plan_stage_owner_legal_entity_scope ON budget_plan_stage_owner AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- version is the ETag of the scenario's grid: every cell write bumps it.
CREATE TABLE IF NOT EXISTS budget_plan_scenario (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  process_id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  kind text NOT NULL CHECK (kind IN ('Baseline', 'Optimistic', 'Pessimistic', 'Custom')),
  copied_from_id uuid,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, process_id, name),
  FOREIGN KEY (tenant_id, process_id) REFERENCES budget_plan_process (tenant_id, id)
);
ALTER TABLE budget_plan_scenario ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_plan_scenario FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_plan_scenario_tenant_access ON budget_plan_scenario AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_plan_scenario_legal_entity_scope ON budget_plan_scenario AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS budget_plan_cell (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  process_id uuid NOT NULL,
  scenario_id uuid NOT NULL,
  account text NOT NULL CHECK (char_length(account) BETWEEN 1 AND 100),
  cost_center text NOT NULL CHECK (char_length(cost_center) BETWEEN 1 AND 100),
  period char(7) NOT NULL CHECK (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  amount numeric(19,4) NOT NULL CHECK (amount >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, scenario_id, account, cost_center, period),
  FOREIGN KEY (tenant_id, scenario_id) REFERENCES budget_plan_scenario (tenant_id, id)
);
ALTER TABLE budget_plan_cell ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_plan_cell FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_plan_cell_tenant_access ON budget_plan_cell AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_plan_cell_legal_entity_scope ON budget_plan_cell AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Insert-only trail of submissions, sign-offs and rejections.
CREATE TABLE IF NOT EXISTS budget_plan_decision (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  process_id uuid NOT NULL,
  submission integer NOT NULL,
  stage text NOT NULL CHECK (stage IN ('Submit', 'Review', 'Approve')),
  org_unit_id uuid,
  actor text NOT NULL,
  decision text NOT NULL CHECK (decision IN ('Submitted', 'SignedOff', 'Rejected')),
  reason text,
  decided_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, process_id) REFERENCES budget_plan_process (tenant_id, id)
);
ALTER TABLE budget_plan_decision ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_plan_decision FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_plan_decision_tenant_access ON budget_plan_decision AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_plan_decision_legal_entity_scope ON budget_plan_decision AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON budget_plan_process, budget_plan_scenario, budget_plan_cell TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON budget_plan_stage_owner TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON budget_plan_decision TO passage_app';
  END IF;
END
$$;
