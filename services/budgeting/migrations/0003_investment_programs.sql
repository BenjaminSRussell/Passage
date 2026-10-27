-- Investment programs per approval year (FR-38.21, BR-38.15). The program budget
-- is the root of the distribution; positions form a hierarchy of at most 6 levels
-- and Σ child budgets never exceed the parent's budget. Every distribution locks
-- the program row, so the invariant is kept under concurrency.
CREATE TABLE IF NOT EXISTS investment_program (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{2,40}$'),
  name text NOT NULL DEFAULT '' CHECK (char_length(name) <= 200),
  approval_year integer NOT NULL CHECK (approval_year BETWEEN 1900 AND 2999),
  program_type text NOT NULL DEFAULT 'Capex' CHECK (program_type = 'Capex'),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  budget numeric(19,4) NOT NULL CHECK (budget >= 0),
  status text NOT NULL DEFAULT 'Open' CHECK (status IN ('Open', 'Closed')),
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, code, approval_year)
);
ALTER TABLE investment_program ENABLE ROW LEVEL SECURITY;
ALTER TABLE investment_program FORCE ROW LEVEL SECURITY;
CREATE POLICY investment_program_tenant_access ON investment_program AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY investment_program_legal_entity_scope ON investment_program AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Position codes are unique within a program. plan, actual and commitment are the
-- sums of the objects assigned to this position directly; the rollup over the
-- subtree is computed on read.
CREATE TABLE IF NOT EXISTS program_position (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  program_id uuid NOT NULL,
  parent_id uuid,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_.-]{1,40}$'),
  level integer NOT NULL CHECK (level BETWEEN 1 AND 6),
  category text NOT NULL CHECK (category IN ('Capex', 'Opex-related', 'Contingency')),
  budget numeric(19,4) NOT NULL DEFAULT 0 CHECK (budget >= 0),
  distributed numeric(19,4) NOT NULL DEFAULT 0 CHECK (distributed >= 0),
  plan numeric(19,4) NOT NULL DEFAULT 0 CHECK (plan >= 0),
  actual numeric(19,4) NOT NULL DEFAULT 0 CHECK (actual >= 0),
  commitment numeric(19,4) NOT NULL DEFAULT 0 CHECK (commitment >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, program_id, code),
  CHECK (distributed <= budget),
  CHECK (parent_id IS NULL OR parent_id <> id),
  FOREIGN KEY (tenant_id, program_id) REFERENCES investment_program (tenant_id, id),
  FOREIGN KEY (tenant_id, parent_id) REFERENCES program_position (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS program_position_parent ON program_position (tenant_id, program_id, parent_id);
ALTER TABLE program_position ENABLE ROW LEVEL SECURITY;
ALTER TABLE program_position FORCE ROW LEVEL SECURITY;
CREATE POLICY program_position_tenant_access ON program_position AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY program_position_legal_entity_scope ON program_position AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Capex requests (wave 37), assets under construction and projects (wave 41)
-- assigned to a position, with the plan, actual and commitment their owning
-- service reports. An object sits on one position of a program at a time.
CREATE TABLE IF NOT EXISTS program_assignment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  program_id uuid NOT NULL,
  position_id uuid NOT NULL,
  object_type text NOT NULL CHECK (object_type IN ('CapexRequest', 'AssetUnderConstruction', 'Project')),
  object_ref text NOT NULL CHECK (char_length(object_ref) BETWEEN 1 AND 80),
  plan numeric(19,4) NOT NULL DEFAULT 0 CHECK (plan >= 0),
  actual numeric(19,4) NOT NULL DEFAULT 0 CHECK (actual >= 0),
  commitment numeric(19,4) NOT NULL DEFAULT 0 CHECK (commitment >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, program_id, object_type, object_ref),
  FOREIGN KEY (tenant_id, program_id) REFERENCES investment_program (tenant_id, id),
  FOREIGN KEY (tenant_id, position_id) REFERENCES program_position (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS program_assignment_position ON program_assignment (tenant_id, position_id);
ALTER TABLE program_assignment ENABLE ROW LEVEL SECURITY;
ALTER TABLE program_assignment FORCE ROW LEVEL SECURITY;
CREATE POLICY program_assignment_tenant_access ON program_assignment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY program_assignment_legal_entity_scope ON program_assignment AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Unused budget carried to the next approval year: once per position (a program
-- is one approval year, so once per position and year, BR-38.15).
CREATE TABLE IF NOT EXISTS investment_carry_forward (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  program_id uuid NOT NULL,
  position_id uuid NOT NULL,
  from_year integer NOT NULL,
  to_year integer NOT NULL,
  amount numeric(19,4) NOT NULL CHECK (amount > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, position_id),
  CHECK (to_year = from_year + 1),
  FOREIGN KEY (tenant_id, program_id) REFERENCES investment_program (tenant_id, id),
  FOREIGN KEY (tenant_id, position_id) REFERENCES program_position (tenant_id, id)
);
ALTER TABLE investment_carry_forward ENABLE ROW LEVEL SECURITY;
ALTER TABLE investment_carry_forward FORCE ROW LEVEL SECURITY;
CREATE POLICY investment_carry_forward_tenant_access ON investment_carry_forward AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY investment_carry_forward_legal_entity_scope ON investment_carry_forward AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
