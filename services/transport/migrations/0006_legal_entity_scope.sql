-- Loads, freight bills, carrier invoices, gate visits and goods-in-transit orders
-- carry the owning legal entity and their line tables follow it (#241). The column is nullable so existing
-- rows stay valid: a NULL entity matches no scoped caller and is visible only to
-- all-entity callers until it is assigned.
ALTER TABLE load ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE freight_bill ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE gate_visit ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE goods_in_transit_order ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE carrier_invoice ADD COLUMN IF NOT EXISTS legal_entity_id uuid;

DO $$
DECLARE
  parent_table text;
BEGIN
  FOREACH parent_table IN ARRAY ARRAY['load', 'freight_bill', 'gate_visit', 'goods_in_transit_order', 'carrier_invoice']
  LOOP
    EXECUTE format(
      'CREATE POLICY %I ON %I AS RESTRICTIVE
         USING (current_setting(''passage.legal_entity_scopes'', true) = ''*''
                OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
      parent_table || '_legal_entity_scope', parent_table
    );
  END LOOP;
END $$;

DO $$
DECLARE
  child_table text;
  parent_table text;
  parent_key text;
BEGIN
  FOR child_table, parent_table, parent_key IN
    SELECT * FROM (VALUES
      ('load_shipment', 'load', 'load_id'),
      ('dg_check', 'load', 'load_id'),
      ('screening_hold', 'load', 'load_id'),
      ('freight_bill_decision', 'freight_bill', 'freight_bill_id'),
      ('gate_event', 'gate_visit', 'visit_id'),
      ('goods_in_transit_line', 'goods_in_transit_order', 'order_id')
    ) AS scope(child_table, parent_table, parent_key)
  LOOP
    EXECUTE format(
      'CREATE POLICY %I ON %I AS RESTRICTIVE USING (
         current_setting(''passage.legal_entity_scopes'', true) = ''*''
         OR EXISTS (
           SELECT 1 FROM %I AS scope_parent
           WHERE scope_parent.tenant_id = %I.tenant_id
             AND scope_parent.id = %I.%I
             AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))
         )
       ) WITH CHECK (
         current_setting(''passage.legal_entity_scopes'', true) = ''*''
         OR EXISTS (
           SELECT 1 FROM %I AS scope_parent
           WHERE scope_parent.tenant_id = %I.tenant_id
             AND scope_parent.id = %I.%I
             AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))
         )
       )',
      child_table || '_legal_entity_scope', child_table, parent_table,
      child_table, child_table, parent_key,
      parent_table, child_table, child_table, parent_key
    );
  END LOOP;
END $$;
