-- Receiving settles invoicing-plan rows and tells purchasing (#289). A settled
-- row is flipped OPEN -> INVOICED and one insert-only record keeps the
-- settlement reference, amount and actor. The record is keyed by the row and
-- carries no foreign key on purpose: a plan rewrite deletes and re-inserts its
-- rows, and the settlement must outlive that.
CREATE TABLE IF NOT EXISTS purchase_invoicing_plan_row_settlement (
  tenant_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  line_no integer NOT NULL,
  seq integer NOT NULL CHECK (seq > 0),
  settlement_ref text NOT NULL CHECK (settlement_ref <> ''),
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  settled_by uuid NOT NULL,
  settled_via text NOT NULL DEFAULT '',
  settled_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, purchase_order_id, line_no, seq)
);

ALTER TABLE purchase_invoicing_plan_row_settlement ENABLE ROW LEVEL SECURITY;
ALTER TABLE purchase_invoicing_plan_row_settlement FORCE ROW LEVEL SECURITY;
CREATE POLICY purchase_invoicing_plan_row_settlement_tenant_access ON purchase_invoicing_plan_row_settlement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY purchase_invoicing_plan_row_settlement_legal_entity_scope ON purchase_invoicing_plan_row_settlement AS RESTRICTIVE
  USING (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM purchase_order scope_parent
      WHERE scope_parent.tenant_id = purchase_invoicing_plan_row_settlement.tenant_id
        AND scope_parent.id = purchase_invoicing_plan_row_settlement.purchase_order_id
        AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  )
  WITH CHECK (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM purchase_order scope_parent
      WHERE scope_parent.tenant_id = purchase_invoicing_plan_row_settlement.tenant_id
        AND scope_parent.id = purchase_invoicing_plan_row_settlement.purchase_order_id
        AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  );

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON purchase_invoicing_plan_row_settlement TO passage_app';
    EXECUTE 'GRANT UPDATE (status) ON purchase_invoicing_plan_row TO passage_app';
  END IF;
END
$$;
