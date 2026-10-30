-- Tenders and tracking events are children of a load (#241): they follow the
-- load's legal entity, so a caller sees them only when it can see the load.
DO $$
DECLARE
  child_table text;
BEGIN
  FOREACH child_table IN ARRAY ARRAY['tender', 'tracking_event']
  LOOP
    EXECUTE format(
      'CREATE POLICY %I ON %I AS RESTRICTIVE USING (
         current_setting(''passage.legal_entity_scopes'', true) = ''*''
         OR EXISTS (
           SELECT 1 FROM load AS scope_parent
           WHERE scope_parent.tenant_id = %I.tenant_id
             AND scope_parent.id = %I.load_id
             AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))
         )
       ) WITH CHECK (
         current_setting(''passage.legal_entity_scopes'', true) = ''*''
         OR EXISTS (
           SELECT 1 FROM load AS scope_parent
           WHERE scope_parent.tenant_id = %I.tenant_id
             AND scope_parent.id = %I.load_id
             AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))
         )
       )',
      child_table || '_legal_entity_scope', child_table,
      child_table, child_table, child_table, child_table
    );
  END LOOP;
END $$;
