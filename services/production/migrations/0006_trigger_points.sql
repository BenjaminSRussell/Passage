-- Wave 21 slice 6: trigger points on operation feedback (FR-21.17, BR-21.13).
-- Additive (expand): no existing row changes.

-- A trigger point is a rule on one operation of an order (PRD 4.21.4 TriggerPoint).
-- operations holds the operations insert-operations inserts, or the route of the rework
-- order create-rework-order creates; their numbers are assigned when the trigger fires.
-- The operation reference cascades: an operation removed by an order edit (FR-21.18,
-- only possible before any feedback) takes its trigger points with it.
CREATE TABLE IF NOT EXISTS production_trigger_point (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  oper_number integer NOT NULL CHECK (oper_number > 0),
  sequence integer NOT NULL CHECK (sequence > 0),
  condition text NOT NULL CHECK (char_length(condition) BETWEEN 1 AND 200),
  function text NOT NULL CHECK (function IN ('insert-operations', 'create-rework-order', 'release-next', 'start-workflow', 'notify')),
  operations jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(operations) = 'array'),
  repeatable boolean NOT NULL DEFAULT false,
  fired_count integer NOT NULL DEFAULT 0 CHECK (fired_count >= 0),
  last_fired_at timestamptz,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, order_id, oper_number, sequence),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, order_id, oper_number) REFERENCES production_operation (tenant_id, order_id, oper_number) ON DELETE CASCADE
);

-- One row per due trigger point on a route-card post: fired, or failed with the reason
-- (PROD-0019). A failure never blocks the post, and the trigger stays due.
CREATE TABLE IF NOT EXISTS production_trigger_firing (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  trigger_point_id uuid NOT NULL,
  journal_id uuid NOT NULL,
  oper_number integer NOT NULL,
  function text NOT NULL,
  status text NOT NULL CHECK (status IN ('fired', 'failed')),
  reason text,
  hours numeric(18,6) NOT NULL,
  qty_good numeric(18,6) NOT NULL,
  qty_error numeric(18,6) NOT NULL,
  inserted_operations integer[] NOT NULL DEFAULT '{}',
  rework_order_id uuid,
  actor text NOT NULL,
  fired_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, trigger_point_id) REFERENCES production_trigger_point (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, journal_id) REFERENCES production_journal (tenant_id, id),
  FOREIGN KEY (tenant_id, rework_order_id) REFERENCES production_order (tenant_id, id),
  CHECK ((status = 'failed') = (reason IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS production_trigger_firing_point ON production_trigger_firing (tenant_id, trigger_point_id, fired_at);
CREATE INDEX IF NOT EXISTS production_trigger_firing_journal ON production_trigger_firing (tenant_id, journal_id);

ALTER TABLE production_trigger_point ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_trigger_point FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS production_trigger_point_tenant_access ON production_trigger_point;
CREATE POLICY production_trigger_point_tenant_access ON production_trigger_point AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS production_trigger_point_legal_entity_scope ON production_trigger_point;
CREATE POLICY production_trigger_point_legal_entity_scope ON production_trigger_point AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE production_trigger_firing ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_trigger_firing FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS production_trigger_firing_tenant_access ON production_trigger_firing;
CREATE POLICY production_trigger_firing_tenant_access ON production_trigger_firing AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS production_trigger_firing_legal_entity_scope ON production_trigger_firing;
CREATE POLICY production_trigger_firing_legal_entity_scope ON production_trigger_firing AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON production_trigger_point, production_trigger_firing TO passage_app';
  END IF;
END
$$;
