-- FR-38.5 / FR-17: a requisition line names the general ledger account and cost center it is charged to, so budget control can
-- check an approved requisition against the plan and reserve a pre-encumbrance. A line without both is not budget controlled.
ALTER TABLE purchase_requisition_line
  ADD COLUMN IF NOT EXISTS gl_account text CHECK (gl_account IS NULL OR gl_account ~ '^[A-Za-z0-9._-]{1,20}$'),
  ADD COLUMN IF NOT EXISTS cost_center text CHECK (cost_center IS NULL OR cost_center ~ '^[A-Za-z0-9._-]{1,20}$');

-- Calls to budget control that did not get an answer, kept so they can be repeated (FR-38.5): converting a requisition
-- line's pre-encumbrance when a purchase order line is created from it, and releasing the pre-encumbrance of a cancelled
-- requisition line. A row is written in the transaction that causes the call, so a crash after the commit loses nothing.
CREATE TABLE budget_call (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('convert', 'release')),
  requisition_id uuid,
  requisition_line_id uuid,
  purchase_order_id uuid,
  purchase_order_line_id uuid,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done')),
  attempts int NOT NULL DEFAULT 0,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  attempted_at timestamptz,
  done_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  CHECK ((kind = 'convert' AND requisition_line_id IS NOT NULL AND purchase_order_id IS NOT NULL AND purchase_order_line_id IS NOT NULL)
      OR (kind = 'release' AND requisition_id IS NOT NULL AND requisition_line_id IS NOT NULL))
);
CREATE UNIQUE INDEX ux_budget_call_convert ON budget_call (tenant_id, purchase_order_line_id) WHERE kind = 'convert';
CREATE UNIQUE INDEX ux_budget_call_release ON budget_call (tenant_id, requisition_line_id) WHERE kind = 'release';
CREATE INDEX ix_budget_call_pending ON budget_call (tenant_id, created_at) WHERE status = 'pending';

ALTER TABLE budget_call ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_call FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_call_tenant_access ON budget_call AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY budget_call_legal_entity_scope ON budget_call AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON budget_call TO passage_app';
  END IF;
END
$$;
