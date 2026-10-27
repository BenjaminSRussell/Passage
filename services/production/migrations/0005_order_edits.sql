-- Wave 21 slice 5: order-specific BOM and route edits (FR-21.18) and the
-- cross-order WIP balance and variance reports (PRD 4.21.14).
-- Additive (expand): existing orders were never edited.

-- order_bom_modified flags an order whose exploded BOM or route was edited
-- after explosion (PRD 4.21.4 ProductionOrder additions); OCM (FR-21.16)
-- leaves such orders to manual review.
ALTER TABLE production_order
  ADD COLUMN IF NOT EXISTS order_bom_modified boolean NOT NULL DEFAULT false;

-- One audit row per applied edit: the line or operation before and after it
-- (null before an add and after a remove), who made it and the request's trace id.
CREATE TABLE IF NOT EXISTS production_order_edit (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  edit_seq integer NOT NULL CHECK (edit_seq > 0),
  target text NOT NULL CHECK (target IN ('line', 'operation')),
  op text NOT NULL CHECK (op IN ('add', 'remove', 'change')),
  target_key text NOT NULL,
  before_image jsonb,
  after_image jsonb,
  order_version integer NOT NULL,
  actor text NOT NULL,
  trace_id text NOT NULL DEFAULT '',
  occurred_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, order_id, order_version, edit_seq),
  FOREIGN KEY (tenant_id, order_id) REFERENCES production_order (tenant_id, id),
  CHECK (before_image IS NOT NULL OR after_image IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS production_order_edit_order ON production_order_edit (tenant_id, order_id, occurred_at);

-- The reports read WIP rows by posting date and variances by order.
CREATE INDEX IF NOT EXISTS production_wip_posting_date ON production_wip_posting (tenant_id, posting_date) WHERE wip;
CREATE INDEX IF NOT EXISTS production_variance_order ON production_variance (tenant_id, order_id);

ALTER TABLE production_order_edit ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_order_edit FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS production_order_edit_tenant_access ON production_order_edit;
CREATE POLICY production_order_edit_tenant_access ON production_order_edit AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS production_order_edit_legal_entity_scope ON production_order_edit;
CREATE POLICY production_order_edit_legal_entity_scope ON production_order_edit AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON production_order_edit TO passage_app';
  END IF;
END
$$;
