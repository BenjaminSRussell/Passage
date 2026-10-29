-- Child rows inherit legal-entity scope through their parent rows (#241).
DO $$
DECLARE
  child_table text;
  parent_table text;
  parent_key text;
BEGIN
  FOR child_table, parent_table, parent_key IN
    SELECT * FROM (VALUES
      ('price_journal_line', 'price_journal', 'journal_id'),
      ('rebate_period', 'rebate_agreement', 'agreement_id'),
      ('sales_group_rep', 'sales_group', 'sales_group_id')
    ) AS scope(child_table, parent_table, parent_key)
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', child_table);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', child_table);
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
