-- Top-down distribution runs (FR-38.19, BR-38.13): an amount booked on an
-- aggregated cost object is distributed on the same cost element to detailed
-- segments by a reference basis. The source is credited and the segments are
-- debited, so the run's entries net to zero. One completed run per source,
-- element and period.
CREATE TABLE IF NOT EXISTS top_down_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  period_key text NOT NULL CHECK (period_key ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  reference_period text CHECK (reference_period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  currency char(3) NOT NULL,
  source_cost_object_id uuid NOT NULL,
  cost_element_id uuid NOT NULL,
  amount numeric(19,4) NOT NULL CHECK (amount > 0),
  shares jsonb NOT NULL,
  excluded jsonb NOT NULL,
  status text NOT NULL CHECK (status = 'Completed'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, source_cost_object_id) REFERENCES cost_object (tenant_id, id),
  FOREIGN KEY (tenant_id, cost_element_id) REFERENCES cost_element (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS top_down_run_one_completed
  ON top_down_run (tenant_id, legal_entity_id, period_key, source_cost_object_id, cost_element_id) WHERE status = 'Completed';

-- Internal order settlements (FR-38.9, BR-38.9): collected cost of an internal
-- order moves to a receiver cost object on a secondary (settlement) element.
-- Settlement may be partial; Σ settled never exceeds the cost collected.
CREATE TABLE IF NOT EXISTS internal_order_settlement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  period_key text NOT NULL CHECK (period_key ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  currency char(3) NOT NULL,
  order_id uuid NOT NULL,
  receiver_id uuid NOT NULL,
  secondary_element_id uuid NOT NULL,
  amount numeric(19,4) NOT NULL CHECK (amount > 0),
  collected_before numeric(19,4) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (order_id <> receiver_id),
  CHECK (amount <= collected_before),
  FOREIGN KEY (tenant_id, order_id) REFERENCES cost_object (tenant_id, id),
  FOREIGN KEY (tenant_id, receiver_id) REFERENCES cost_object (tenant_id, id),
  FOREIGN KEY (tenant_id, secondary_element_id) REFERENCES cost_element (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS internal_order_settlement_order
  ON internal_order_settlement (tenant_id, order_id, period_key, created_at);

-- Entries posted by a top-down run or a settlement point back to it (origin Allocation).
ALTER TABLE cost_entry ADD COLUMN IF NOT EXISTS top_down_run_id uuid;
ALTER TABLE cost_entry ADD COLUMN IF NOT EXISTS settlement_id uuid;
DO $$ BEGIN
  ALTER TABLE cost_entry ADD CONSTRAINT cost_entry_top_down_run_fk
    FOREIGN KEY (tenant_id, top_down_run_id) REFERENCES top_down_run (tenant_id, id);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
  ALTER TABLE cost_entry ADD CONSTRAINT cost_entry_settlement_fk
    FOREIGN KEY (tenant_id, settlement_id) REFERENCES internal_order_settlement (tenant_id, id);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

ALTER TABLE top_down_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE top_down_run FORCE ROW LEVEL SECURITY;
ALTER TABLE internal_order_settlement ENABLE ROW LEVEL SECURITY;
ALTER TABLE internal_order_settlement FORCE ROW LEVEL SECURITY;

CREATE POLICY top_down_run_tenant_access ON top_down_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY top_down_run_legal_entity_scope ON top_down_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE POLICY internal_order_settlement_tenant_access ON internal_order_settlement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY internal_order_settlement_legal_entity_scope ON internal_order_settlement AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
