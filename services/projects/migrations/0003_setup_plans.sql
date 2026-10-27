-- Slice 2 (PSG-4102): project groups, posting profiles, plan versions and WBS.

-- ProjectGroup (FR-41.1, §8.41.4): the group a project belongs to. The id is a
-- code like ENG; "*" is reserved as the posting-profile wildcard and cannot be a group.
CREATE TABLE IF NOT EXISTS project_group (
  tenant_id uuid NOT NULL,
  id text NOT NULL CHECK (id ~ '^[A-Z0-9][A-Z0-9-]{0,9}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  default_project_type text CHECK (default_project_type IN ('TIME_MATERIAL', 'FIXED_PRICE', 'INTERNAL', 'COST', 'INVESTMENT', 'TIME')),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
ALTER TABLE project_group ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_group FORCE ROW LEVEL SECURITY;
CREATE POLICY project_group_tenant_access ON project_group AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- project.project_group_id becomes a real FK (§8.41.4). NOT VALID: projects
-- created by slice 1 before groups existed stay readable; every new or changed
-- row is checked.
ALTER TABLE project ADD CONSTRAINT project_project_group_fk
  FOREIGN KEY (tenant_id, project_group_id) REFERENCES project_group (tenant_id, id) NOT VALID;

-- PostingProfile (FR-41.5). Only the three keys the resolver reads can be stored:
-- (group, category, property), (group, *, *) and (*, *, *).
CREATE TABLE IF NOT EXISTS posting_profile (
  tenant_id uuid NOT NULL,
  project_group text NOT NULL CHECK (project_group = '*' OR project_group ~ '^[A-Z0-9][A-Z0-9-]{0,9}$'),
  category text NOT NULL CHECK (char_length(category) BETWEEN 1 AND 20),
  line_property text NOT NULL CHECK (line_property IN ('CHARGEABLE', 'NON_CHARGEABLE', '*')),
  cost_account text NOT NULL CHECK (char_length(cost_account) BETWEEN 1 AND 40),
  wip_account text NOT NULL CHECK (char_length(wip_account) BETWEEN 1 AND 40),
  accrued_revenue_account text NOT NULL CHECK (char_length(accrued_revenue_account) BETWEEN 1 AND 40),
  invoiced_revenue_account text NOT NULL CHECK (char_length(invoiced_revenue_account) BETWEEN 1 AND 40),
  on_account_account text NOT NULL CHECK (char_length(on_account_account) BETWEEN 1 AND 40),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, project_group, category, line_property),
  CHECK ((category = '*') = (line_property = '*')),
  CHECK (project_group <> '*' OR category = '*')
);
ALTER TABLE posting_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE posting_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY posting_profile_tenant_access ON posting_profile AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- PlanVersion (FR-41.2, §8.41.4): one WORKING and one BASELINE per project. A
-- BASELINE or SIMULATION is a copy of another version; a BASELINE is locked.
CREATE TABLE IF NOT EXISTS plan_version (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  project_id text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('BASELINE', 'WORKING', 'SIMULATION')),
  sequence integer NOT NULL CHECK (sequence >= 1),
  copied_from_version_id uuid,
  locked boolean NOT NULL DEFAULT false,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES project (tenant_id, id),
  FOREIGN KEY (tenant_id, copied_from_version_id) REFERENCES plan_version (tenant_id, id),
  UNIQUE (tenant_id, project_id, sequence),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (kind = 'WORKING' OR copied_from_version_id IS NOT NULL)
);
CREATE UNIQUE INDEX IF NOT EXISTS plan_version_one_baseline ON plan_version (tenant_id, project_id) WHERE kind = 'BASELINE';
CREATE UNIQUE INDEX IF NOT EXISTS plan_version_one_working ON plan_version (tenant_id, project_id) WHERE kind = 'WORKING';
ALTER TABLE plan_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE plan_version FORCE ROW LEVEL SECURITY;
CREATE POLICY plan_version_tenant_access ON plan_version AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
-- Legal-entity scope through the owning project (itself scoped by RLS).
CREATE POLICY plan_version_legal_entity_scope ON plan_version AS RESTRICTIVE
  USING (EXISTS (SELECT 1 FROM project p WHERE p.tenant_id = plan_version.tenant_id AND p.id = plan_version.project_id));

-- WbsNode (FR-41.2, §8.41.4). duration_days is whole working days (the CPM
-- scheduler's unit); early/late dates, slack and is_critical are scheduler output.
CREATE TABLE IF NOT EXISTS wbs_node (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_version_id uuid NOT NULL,
  wbs_code text NOT NULL CHECK (char_length(wbs_code) BETWEEN 1 AND 40),
  parent_id uuid,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  effort_hours numeric(12,2) CHECK (effort_hours IS NULL OR effort_hours >= 0),
  duration_days integer NOT NULL DEFAULT 0 CHECK (duration_days BETWEEN 0 AND 100000),
  constraint_type text NOT NULL DEFAULT 'ASAP' CHECK (constraint_type IN ('ASAP', 'SNET', 'FNLT', 'MSO')),
  constraint_date date,
  role_id uuid,
  is_billable_milestone boolean NOT NULL DEFAULT false,
  early_start date,
  early_finish date,
  late_start date,
  late_finish date,
  slack_days integer,
  is_critical boolean,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_version_id) REFERENCES plan_version (tenant_id, id),
  FOREIGN KEY (tenant_id, parent_id) REFERENCES wbs_node (tenant_id, id) DEFERRABLE INITIALLY DEFERRED,
  UNIQUE (tenant_id, plan_version_id, wbs_code),
  CHECK ((constraint_type = 'ASAP') = (constraint_date IS NULL))
);
CREATE INDEX IF NOT EXISTS wbs_node_plan_parent_idx ON wbs_node (tenant_id, plan_version_id, parent_id);
ALTER TABLE wbs_node ENABLE ROW LEVEL SECURITY;
ALTER TABLE wbs_node FORCE ROW LEVEL SECURITY;
CREATE POLICY wbs_node_tenant_access ON wbs_node AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY wbs_node_legal_entity_scope ON wbs_node AS RESTRICTIVE
  USING (EXISTS (SELECT 1 FROM plan_version v WHERE v.tenant_id = wbs_node.tenant_id AND v.id = wbs_node.plan_version_id));

CREATE TABLE IF NOT EXISTS wbs_dependency (
  tenant_id uuid NOT NULL,
  plan_version_id uuid NOT NULL,
  predecessor_id uuid NOT NULL,
  successor_id uuid NOT NULL,
  dep_type text NOT NULL CHECK (dep_type IN ('FS', 'SS', 'FF', 'SF')),
  lag_days integer NOT NULL DEFAULT 0 CHECK (lag_days BETWEEN -3650 AND 3650),
  PRIMARY KEY (tenant_id, plan_version_id, predecessor_id, successor_id),
  FOREIGN KEY (tenant_id, plan_version_id) REFERENCES plan_version (tenant_id, id),
  FOREIGN KEY (tenant_id, predecessor_id) REFERENCES wbs_node (tenant_id, id),
  FOREIGN KEY (tenant_id, successor_id) REFERENCES wbs_node (tenant_id, id),
  CHECK (predecessor_id <> successor_id)
);
ALTER TABLE wbs_dependency ENABLE ROW LEVEL SECURITY;
ALTER TABLE wbs_dependency FORCE ROW LEVEL SECURITY;
CREATE POLICY wbs_dependency_tenant_access ON wbs_dependency AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY wbs_dependency_legal_entity_scope ON wbs_dependency AS RESTRICTIVE
  USING (EXISTS (SELECT 1 FROM plan_version v WHERE v.tenant_id = wbs_dependency.tenant_id AND v.id = wbs_dependency.plan_version_id));
