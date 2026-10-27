-- Allocation reruns (BR-38.7): a rerun reverses the period's completed run
-- and posts a new one. The reversed run keeps its entries and their negations
-- (both carry its id, so each run still nets to zero) and links to the run
-- that replaced it. The old run is marked Reversed before the new one is
-- inserted (one Completed run per period), so the back link is checked at
-- commit.
ALTER TABLE allocation_run DROP CONSTRAINT IF EXISTS allocation_run_status_check;
ALTER TABLE allocation_run ADD CONSTRAINT allocation_run_status_check CHECK (status IN ('Completed', 'Reversed'));
ALTER TABLE allocation_run ADD COLUMN IF NOT EXISTS reverses_run_id uuid;
ALTER TABLE allocation_run ADD COLUMN IF NOT EXISTS reversed_by_run_id uuid;
DO $$ BEGIN
  ALTER TABLE allocation_run ADD CONSTRAINT allocation_run_reverses_fk
    FOREIGN KEY (tenant_id, reverses_run_id) REFERENCES allocation_run (tenant_id, id);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
  ALTER TABLE allocation_run ADD CONSTRAINT allocation_run_reversed_by_fk
    FOREIGN KEY (tenant_id, reversed_by_run_id) REFERENCES allocation_run (tenant_id, id) DEFERRABLE INITIALLY DEFERRED;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
ALTER TABLE allocation_run DROP CONSTRAINT IF EXISTS allocation_run_reversed_link;
ALTER TABLE allocation_run ADD CONSTRAINT allocation_run_reversed_link
  CHECK ((status = 'Reversed') = (reversed_by_run_id IS NOT NULL));

-- Cost behavior (FR-38.16): an optional Fixed / Variable default per cost
-- element, and effective-dated rules per element, optionally narrowed to one
-- cost object kind. Journal mirrors are classified when they are written.
ALTER TABLE cost_element ADD COLUMN IF NOT EXISTS default_behavior text;
ALTER TABLE cost_element DROP CONSTRAINT IF EXISTS cost_element_default_behavior_check;
ALTER TABLE cost_element ADD CONSTRAINT cost_element_default_behavior_check
  CHECK (default_behavior IS NULL OR default_behavior IN ('Fixed', 'Variable'));

CREATE TABLE IF NOT EXISTS cost_behavior_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  cost_element_id uuid NOT NULL,
  object_kind text CHECK (object_kind IN ('CostCenter', 'InternalOrder', 'Product', 'Project')),
  behavior text NOT NULL CHECK (behavior IN ('Fixed', 'Variable')),
  effective_from date NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, cost_element_id) REFERENCES cost_element (tenant_id, id)
);
-- One rule per element, object kind (or any kind) and date, so the
-- classification is deterministic.
CREATE UNIQUE INDEX IF NOT EXISTS cost_behavior_rule_once
  ON cost_behavior_rule (tenant_id, legal_entity_id, cost_element_id, coalesce(object_kind, ''), effective_from);

ALTER TABLE cost_behavior_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE cost_behavior_rule FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS cost_behavior_rule_tenant_access ON cost_behavior_rule;
CREATE POLICY cost_behavior_rule_tenant_access ON cost_behavior_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS cost_behavior_rule_legal_entity_scope ON cost_behavior_rule;
CREATE POLICY cost_behavior_rule_legal_entity_scope ON cost_behavior_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
