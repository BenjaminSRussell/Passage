-- PSG-25 #25 subtask 25-5: control recipes (FR-25.15, BR-25.11, BR-25.15, D6). A control recipe is the
-- ISA-88 structure of one order (procedure, unit procedures, operations, phases, process instructions)
-- generated for one destination: the kiosk, or a process control system reached through an edge node
-- (pcs-opcua writes setpoints to allowlisted nodes, pcs-mqtt publishes to a topic). It is sent when it is
-- generated and tracked: sent, received, in-execution, completed, error. send_count counts every send, so
-- a resend after an error is send_count 2 and the fifth resend is send_count 6 (BR-25.11 allows 5).
-- control_recipe_event is the append-only history of every status change and resend. edge_write_audit is the
-- append-only record of every setpoint write the connector made (or refused) and the value it read back
-- (BR-25.15). New tables, so no lock on a live one.

CREATE TABLE IF NOT EXISTS control_recipe (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  order_id uuid NOT NULL,
  resource_id uuid NOT NULL,
  destination text NOT NULL CHECK (destination IN ('kiosk', 'pcs-opcua', 'pcs-mqtt')),
  edge_id uuid,
  topic text CHECK (topic IS NULL OR char_length(topic) BETWEEN 1 AND 300),
  status text NOT NULL CHECK (status IN ('sent', 'received', 'in-execution', 'completed', 'error')),
  recipe jsonb NOT NULL,
  send_count integer NOT NULL DEFAULT 1 CHECK (send_count BETWEEN 1 AND 6),
  last_sent_at timestamptz NOT NULL DEFAULT now(),
  last_error text CHECK (last_error IS NULL OR char_length(last_error) <= 1000),
  version integer NOT NULL DEFAULT 1,
  created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, edge_id) REFERENCES edge_node (tenant_id, id),
  CHECK ((destination = 'kiosk' AND edge_id IS NULL AND topic IS NULL)
      OR (destination = 'pcs-opcua' AND edge_id IS NOT NULL AND topic IS NULL)
      OR (destination = 'pcs-mqtt' AND edge_id IS NOT NULL AND topic IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS control_recipe_order ON control_recipe (tenant_id, order_id, created_at DESC);
CREATE INDEX IF NOT EXISTS control_recipe_status ON control_recipe (tenant_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS control_recipe_edge ON control_recipe (tenant_id, edge_id, status) WHERE edge_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS control_recipe_resource ON control_recipe (tenant_id, resource_id, status) WHERE destination = 'kiosk';
ALTER TABLE control_recipe ENABLE ROW LEVEL SECURITY;
ALTER TABLE control_recipe FORCE ROW LEVEL SECURITY;
CREATE POLICY control_recipe_tenant_access ON control_recipe AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS control_recipe_event (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  recipe_id uuid NOT NULL,
  from_status text,
  to_status text NOT NULL,
  actor text NOT NULL CHECK (char_length(actor) BETWEEN 1 AND 200),
  note text CHECK (note IS NULL OR char_length(note) <= 1000),
  forced boolean NOT NULL DEFAULT false,
  at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, recipe_id) REFERENCES control_recipe (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS control_recipe_event_recipe ON control_recipe_event (tenant_id, recipe_id, at);
ALTER TABLE control_recipe_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE control_recipe_event FORCE ROW LEVEL SECURITY;
CREATE POLICY control_recipe_event_tenant_access ON control_recipe_event AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS edge_write_audit (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  recipe_id uuid NOT NULL,
  edge_id uuid NOT NULL,
  phase_path text NOT NULL CHECK (char_length(phase_path) BETWEEN 1 AND 500),
  node text NOT NULL CHECK (char_length(node) BETWEEN 1 AND 300),
  value numeric NOT NULL,
  readback numeric,
  written boolean NOT NULL,
  deviation boolean NOT NULL DEFAULT false,
  reason text CHECK (reason IS NULL OR char_length(reason) <= 1000),
  at timestamptz NOT NULL,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, recipe_id) REFERENCES control_recipe (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS edge_write_audit_recipe ON edge_write_audit (tenant_id, recipe_id, at);
ALTER TABLE edge_write_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE edge_write_audit FORCE ROW LEVEL SECURITY;
CREATE POLICY edge_write_audit_tenant_access ON edge_write_audit AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON control_recipe TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON control_recipe_event TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON edge_write_audit TO passage_app';
  END IF;
END
$$;
