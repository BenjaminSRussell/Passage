-- Wave 17 purchasing slice 14: delivery schedules on PO lines (FR-17.10). One PO line has one or
-- more rows (quantity, delivery date, vendor-confirmed date) whose quantities sum to the line
-- quantity. Additive only (expand/contract). Tenant RLS on the table; the row carries no legal
-- entity and is scoped through its PO like the other PO child tables.
CREATE TABLE IF NOT EXISTS purchase_delivery_schedule (
  tenant_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  line_no integer NOT NULL,
  seq integer NOT NULL CHECK (seq > 0),
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  delivery_date date NOT NULL,
  confirmed_date date,
  confirmed_by uuid,
  confirmed_at timestamptz,
  PRIMARY KEY (tenant_id, purchase_order_id, line_no, seq),
  FOREIGN KEY (tenant_id, purchase_order_id, line_no) REFERENCES purchase_order_line (tenant_id, purchase_order_id, line_no),
  CHECK ((confirmed_date IS NULL) = (confirmed_at IS NULL))
);
CREATE INDEX IF NOT EXISTS purchase_delivery_schedule_due ON purchase_delivery_schedule (tenant_id, delivery_date);

ALTER TABLE purchase_delivery_schedule ENABLE ROW LEVEL SECURITY;
ALTER TABLE purchase_delivery_schedule FORCE ROW LEVEL SECURITY;
CREATE POLICY purchase_delivery_schedule_tenant_access ON purchase_delivery_schedule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY purchase_delivery_schedule_legal_entity_scope ON purchase_delivery_schedule AS RESTRICTIVE
  USING (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM purchase_order AS scope_parent
      WHERE scope_parent.tenant_id = purchase_delivery_schedule.tenant_id
        AND scope_parent.id = purchase_delivery_schedule.purchase_order_id
        AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  ) WITH CHECK (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM purchase_order AS scope_parent
      WHERE scope_parent.tenant_id = purchase_delivery_schedule.tenant_id
        AND scope_parent.id = purchase_delivery_schedule.purchase_order_id
        AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  );

-- passage_app: a schedule is replaced (INSERT, DELETE), kept in step with the line quantity and
-- given a confirmed date (UPDATE).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON purchase_delivery_schedule TO passage_app';
  END IF;
END
$$;
