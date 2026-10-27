-- Slice 3 (PSG-4103): contract value, change orders and claims (FR-41.18,
-- BR-41.13) and the risk and issue register (FR-41.20). Additive only.

-- The project's contract value (BR-41.1 reads it; BR-41.13 adjusts it).
ALTER TABLE project ADD COLUMN IF NOT EXISTS contract_value numeric(20,6) NOT NULL DEFAULT 0 CHECK (contract_value >= 0);

-- BR-41.13: applying a change order creates a new BASELINE from the current one.
-- The old baseline stays (locked, readable) and points at its successor; only
-- one current (not superseded) BASELINE per project.
ALTER TABLE plan_version ADD COLUMN IF NOT EXISTS superseded_by_version_id uuid;
ALTER TABLE plan_version ADD CONSTRAINT plan_version_superseded_by_fk
  FOREIGN KEY (tenant_id, superseded_by_version_id) REFERENCES plan_version (tenant_id, id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE plan_version ADD CONSTRAINT plan_version_superseded_baseline_only
  CHECK (superseded_by_version_id IS NULL OR kind = 'BASELINE');
DROP INDEX IF EXISTS plan_version_one_baseline;
CREATE UNIQUE INDEX IF NOT EXISTS plan_version_one_current_baseline ON plan_version (tenant_id, project_id)
  WHERE kind = 'BASELINE' AND superseded_by_version_id IS NULL;

-- ProjectChangeOrder (§8.41.4). number is CO-0001, CO-0002, ... per project
-- (gap-free under the project row lock) until the kernel sequence PROJ-CO exists.
CREATE TABLE IF NOT EXISTS project_change_order (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  project_id text NOT NULL,
  number text NOT NULL CHECK (number ~ '^CO-[0-9]{4,}$'),
  co_type text NOT NULL CHECK (co_type IN ('SCOPE_CHANGE', 'CLAIM_CUSTOMER', 'CLAIM_VENDOR', 'INTERNAL')),
  title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
  status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'APPLIED')),
  customer_approval_ref text CHECK (customer_approval_ref IS NULL OR char_length(customer_approval_ref) BETWEEN 1 AND 200),
  vendor_party_id uuid,
  created_by text NOT NULL CHECK (char_length(created_by) >= 1),
  decided_by text,
  applied_by text,
  applied_baseline_version_id uuid,
  applied_at timestamptz,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES project (tenant_id, id),
  FOREIGN KEY (tenant_id, applied_baseline_version_id) REFERENCES plan_version (tenant_id, id),
  UNIQUE (tenant_id, project_id, number),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (co_type <> 'CLAIM_VENDOR' OR vendor_party_id IS NOT NULL),
  CHECK ((status = 'APPLIED') = (applied_at IS NOT NULL))
);
ALTER TABLE project_change_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_change_order FORCE ROW LEVEL SECURITY;
CREATE POLICY project_change_order_tenant_access ON project_change_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
-- Legal-entity scope through the owning project (itself scoped by RLS).
CREATE POLICY project_change_order_legal_entity_scope ON project_change_order AS RESTRICTIVE
  USING (EXISTS (SELECT 1 FROM project p WHERE p.tenant_id = project_change_order.tenant_id AND p.id = project_change_order.project_id));
CREATE INDEX IF NOT EXISTS project_change_order_project_idx ON project_change_order (tenant_id, project_id, number);

-- Impact lines. wbs_code is the WBS node the line names, resolved from
-- wbs_node_id when the change order is created; applying maps the line onto
-- the current baseline by code (node ids differ between plan versions, and
-- WORKING nodes are replaced wholesale, so there is no foreign key to wbs_node).
CREATE TABLE IF NOT EXISTS project_change_order_line (
  tenant_id uuid NOT NULL,
  change_order_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  wbs_node_id uuid,
  wbs_code text CHECK (wbs_code IS NULL OR char_length(wbs_code) BETWEEN 1 AND 40),
  cost_impact numeric(20,6) NOT NULL,
  revenue_impact numeric(20,6) NOT NULL,
  effort_hours_delta numeric(12,2) NOT NULL DEFAULT 0,
  schedule_days_delta integer NOT NULL DEFAULT 0 CHECK (schedule_days_delta BETWEEN -100000 AND 100000),
  PRIMARY KEY (tenant_id, change_order_id, line_no),
  FOREIGN KEY (tenant_id, change_order_id) REFERENCES project_change_order (tenant_id, id) ON DELETE CASCADE,
  CHECK ((wbs_node_id IS NULL) = (wbs_code IS NULL))
);
ALTER TABLE project_change_order_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_change_order_line FORCE ROW LEVEL SECURITY;
CREATE POLICY project_change_order_line_tenant_access ON project_change_order_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY project_change_order_line_legal_entity_scope ON project_change_order_line AS RESTRICTIVE
  USING (EXISTS (SELECT 1 FROM project_change_order c WHERE c.tenant_id = project_change_order_line.tenant_id AND c.id = project_change_order_line.change_order_id));

-- ProjectRiskIssue (§8.41.4, FR-41.20). The score (probability × impact) is derived.
CREATE TABLE IF NOT EXISTS project_risk_issue (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  project_id text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('RISK', 'ISSUE')),
  title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
  probability smallint NOT NULL CHECK (probability BETWEEN 1 AND 5),
  impact smallint NOT NULL CHECK (impact BETWEEN 1 AND 5),
  owner_worker_id uuid,
  mitigation text NOT NULL DEFAULT '' CHECK (char_length(mitigation) <= 2000),
  due_date date,
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'MITIGATED', 'CLOSED')),
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES project (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE project_risk_issue ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_risk_issue FORCE ROW LEVEL SECURITY;
CREATE POLICY project_risk_issue_tenant_access ON project_risk_issue AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY project_risk_issue_legal_entity_scope ON project_risk_issue AS RESTRICTIVE
  USING (EXISTS (SELECT 1 FROM project p WHERE p.tenant_id = project_risk_issue.tenant_id AND p.id = project_risk_issue.project_id));
CREATE INDEX IF NOT EXISTS project_risk_issue_project_idx ON project_risk_issue (tenant_id, project_id, status, id);
