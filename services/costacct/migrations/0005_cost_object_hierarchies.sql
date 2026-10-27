-- Cost object hierarchies (FR-38.18): a hierarchy arranges cost objects of one
-- legal entity in a tree (for example product group -> product -> production
-- order). In a Collective hierarchy a node may collect costs that cannot be
-- assigned to a lower object; at period end a distribution run moves what the
-- node holds to its child nodes by a basis. A Reporting hierarchy only groups.
CREATE TABLE IF NOT EXISTS cost_object_hierarchy (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  type text NOT NULL CHECK (type IN ('Reporting', 'Collective')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT cost_object_hierarchy_code_once UNIQUE (tenant_id, legal_entity_id, code)
);

-- A cost object appears at most once per hierarchy. The parent is a node of
-- the same hierarchy (composite key), so a new node can never close a cycle.
-- Only a collecting node has a distribution basis.
CREATE TABLE IF NOT EXISTS cost_hierarchy_node (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  hierarchy_id uuid NOT NULL,
  parent_node_id uuid,
  cost_object_id uuid NOT NULL,
  collects boolean NOT NULL DEFAULT false,
  distribution_basis text CHECK (distribution_basis IN ('ActualOutput', 'StandardCost', 'FixedPercent')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, hierarchy_id, id),
  CONSTRAINT cost_hierarchy_node_object_once UNIQUE (tenant_id, hierarchy_id, cost_object_id),
  CHECK (collects = (distribution_basis IS NOT NULL)),
  CHECK (parent_node_id IS NULL OR parent_node_id <> id),
  FOREIGN KEY (tenant_id, hierarchy_id) REFERENCES cost_object_hierarchy (tenant_id, id),
  FOREIGN KEY (tenant_id, hierarchy_id, parent_node_id) REFERENCES cost_hierarchy_node (tenant_id, hierarchy_id, id),
  FOREIGN KEY (tenant_id, cost_object_id) REFERENCES cost_object (tenant_id, id)
);

-- One row per distribution run: what the node held for one element, period
-- and currency, what went to the children and what stayed (CO-0645).
CREATE TABLE IF NOT EXISTS hierarchy_distribution_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  hierarchy_id uuid NOT NULL,
  node_id uuid NOT NULL,
  period_key text NOT NULL CHECK (period_key ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  currency char(3) NOT NULL,
  cost_element_id uuid NOT NULL,
  basis text NOT NULL CHECK (basis IN ('ActualOutput', 'StandardCost', 'FixedPercent')),
  amount numeric(19,4) NOT NULL CHECK (amount > 0),
  distributed numeric(19,4) NOT NULL CHECK (distributed >= 0),
  residual numeric(19,4) NOT NULL CHECK (residual >= 0),
  shares jsonb NOT NULL,
  status text NOT NULL CHECK (status = 'Completed'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (distributed + residual = amount),
  FOREIGN KEY (tenant_id, hierarchy_id, node_id) REFERENCES cost_hierarchy_node (tenant_id, hierarchy_id, id),
  FOREIGN KEY (tenant_id, cost_element_id) REFERENCES cost_element (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS hierarchy_distribution_run_hierarchy
  ON hierarchy_distribution_run (tenant_id, hierarchy_id, period_key, created_at);

-- Collected and distributed entries carry the node they belong to, so a node's
-- balance is the sum of its own entries even when its cost object is a node
-- of another hierarchy too. Distribution entries also point to their run.
ALTER TABLE cost_entry DROP CONSTRAINT IF EXISTS cost_entry_origin_check;
ALTER TABLE cost_entry ADD CONSTRAINT cost_entry_origin_check
  CHECK (origin IN ('Journal', 'Allocation', 'Activity', 'Statistical', 'Manual', 'HierarchyDistribution'));
ALTER TABLE cost_entry ADD COLUMN IF NOT EXISTS hierarchy_node_id uuid;
ALTER TABLE cost_entry ADD COLUMN IF NOT EXISTS hierarchy_run_id uuid;
DO $$ BEGIN
  ALTER TABLE cost_entry ADD CONSTRAINT cost_entry_hierarchy_node_fk
    FOREIGN KEY (tenant_id, hierarchy_node_id) REFERENCES cost_hierarchy_node (tenant_id, id);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
  ALTER TABLE cost_entry ADD CONSTRAINT cost_entry_hierarchy_run_fk
    FOREIGN KEY (tenant_id, hierarchy_run_id) REFERENCES hierarchy_distribution_run (tenant_id, id);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
CREATE INDEX IF NOT EXISTS cost_entry_hierarchy_node
  ON cost_entry (tenant_id, hierarchy_node_id, cost_element_id, period_key) WHERE hierarchy_node_id IS NOT NULL;

ALTER TABLE cost_object_hierarchy ENABLE ROW LEVEL SECURITY;
ALTER TABLE cost_object_hierarchy FORCE ROW LEVEL SECURITY;
ALTER TABLE cost_hierarchy_node ENABLE ROW LEVEL SECURITY;
ALTER TABLE cost_hierarchy_node FORCE ROW LEVEL SECURITY;
ALTER TABLE hierarchy_distribution_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE hierarchy_distribution_run FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS cost_object_hierarchy_tenant_access ON cost_object_hierarchy;
CREATE POLICY cost_object_hierarchy_tenant_access ON cost_object_hierarchy AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS cost_object_hierarchy_legal_entity_scope ON cost_object_hierarchy;
CREATE POLICY cost_object_hierarchy_legal_entity_scope ON cost_object_hierarchy AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DROP POLICY IF EXISTS cost_hierarchy_node_tenant_access ON cost_hierarchy_node;
CREATE POLICY cost_hierarchy_node_tenant_access ON cost_hierarchy_node AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS cost_hierarchy_node_legal_entity_scope ON cost_hierarchy_node;
CREATE POLICY cost_hierarchy_node_legal_entity_scope ON cost_hierarchy_node AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DROP POLICY IF EXISTS hierarchy_distribution_run_tenant_access ON hierarchy_distribution_run;
CREATE POLICY hierarchy_distribution_run_tenant_access ON hierarchy_distribution_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS hierarchy_distribution_run_legal_entity_scope ON hierarchy_distribution_run;
CREATE POLICY hierarchy_distribution_run_legal_entity_scope ON hierarchy_distribution_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
