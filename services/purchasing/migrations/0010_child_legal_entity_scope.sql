-- Child tables inherit legal-entity scope through their header rows.
DO $$
DECLARE
  child_table text;
  parent_table text;
  parent_key text;
BEGIN
  FOR child_table, parent_table, parent_key IN
    SELECT * FROM (VALUES
      ('purchase_requisition_line', 'purchase_requisition', 'requisition_id'),
      ('purchase_order_line', 'purchase_order', 'purchase_order_id'),
      ('purchase_order_charge', 'purchase_order', 'purchase_order_id'),
      ('purchase_order_version', 'purchase_order', 'purchase_order_id'),
      ('purchase_agreement_line', 'purchase_agreement', 'agreement_id'),
      ('purchase_invoicing_plan', 'purchase_order', 'purchase_order_id'),
      ('purchase_invoicing_plan_row', 'purchase_order', 'purchase_order_id'),
      ('purchase_rfq_line', 'purchase_rfq', 'rfq_id'),
      ('purchase_rfq_invitation', 'purchase_rfq', 'rfq_id'),
      ('purchase_rfq_reply', 'purchase_rfq', 'rfq_id'),
      ('purchase_rfq_criterion', 'purchase_rfq', 'rfq_id'),
      ('purchase_rfq_score', 'purchase_rfq', 'rfq_id'),
      ('purchase_sa_schedule_line', 'purchase_scheduling_agreement', 'agreement_id'),
      ('purchase_sa_release', 'purchase_scheduling_agreement', 'agreement_id')
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

CREATE POLICY purchase_sa_release_line_legal_entity_scope ON purchase_sa_release_line AS RESTRICTIVE
  USING (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1
      FROM purchase_sa_release r
      JOIN purchase_scheduling_agreement a ON a.tenant_id = r.tenant_id AND a.id = r.agreement_id
      WHERE r.tenant_id = purchase_sa_release_line.tenant_id
        AND r.id = purchase_sa_release_line.release_id
        AND a.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  )
  WITH CHECK (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1
      FROM purchase_sa_release r
      JOIN purchase_scheduling_agreement a ON a.tenant_id = r.tenant_id AND a.id = r.agreement_id
      WHERE r.tenant_id = purchase_sa_release_line.tenant_id
        AND r.id = purchase_sa_release_line.release_id
        AND a.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  );
