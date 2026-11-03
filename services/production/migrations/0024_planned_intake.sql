-- Wave 21 slice 25: the planned-order intake (FR-22.10, BR-22.9).
-- Additive (expand): a new table only. Planning writes plannedorder.firmed.v1 for a production planned
-- order and the owning context creates the target order; this table is that inbox. The consumer inserts
-- a row per firmed planned order, a planner converts it into a production order (the target id becomes
-- the order id) or dismisses it.
-- legal_entity_id is the planned order's legal entity (its plan version's), from the event's
-- legal_entity_id or the CloudEvents legalentityid extension; the order a conversion creates takes it.
-- plannedorder.firmed.v1 does not carry it yet (handoff to wave 22), so it is nullable: a NULL row
-- matches no scoped caller and is visible only to all-entity callers, as planning's own NULL-entity rows.

CREATE TABLE IF NOT EXISTS production_planned_intake (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid,
  planned_order_id uuid NOT NULL,
  plan_version_id uuid NOT NULL,
  batch_id uuid NOT NULL,
  target_id uuid NOT NULL,
  product_id uuid NOT NULL,
  site_id uuid NOT NULL,
  warehouse_id uuid,
  quantity numeric(18,6) NOT NULL CHECK (quantity > 0),
  order_date date NOT NULL,
  delivery_date date NOT NULL,
  quota_arrangement_id uuid,
  quota_source_id uuid,
  event_id text NOT NULL,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'converted', 'dismissed')),
  order_id uuid,
  dismiss_reason text,
  version integer NOT NULL DEFAULT 1,
  received_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by text,
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT production_planned_intake_converted_has_order CHECK ((status = 'converted') = (order_id IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS production_planned_intake_planned_order
  ON production_planned_intake (tenant_id, planned_order_id);
CREATE INDEX IF NOT EXISTS production_planned_intake_status
  ON production_planned_intake (tenant_id, status, delivery_date);

ALTER TABLE production_planned_intake ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_planned_intake FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS production_planned_intake_tenant_access ON production_planned_intake;
CREATE POLICY production_planned_intake_tenant_access ON production_planned_intake AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS production_planned_intake_legal_entity_scope ON production_planned_intake;
CREATE POLICY production_planned_intake_legal_entity_scope ON production_planned_intake AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The consumer inserts, the store updates (conversion, dismissal) and locks rows, never deletes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON production_planned_intake TO passage_app';
  END IF;
END
$$;
