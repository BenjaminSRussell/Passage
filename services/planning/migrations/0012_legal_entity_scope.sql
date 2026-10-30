-- Plans, forecast models and quota arrangements carry the owning legal entity
-- and their line tables follow it (#241). The column is nullable so existing
-- rows stay valid: a NULL entity matches no scoped caller and is visible only to
-- all-entity callers until it is assigned.
ALTER TABLE plan ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE plan_version ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE forecast_model ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE quota_arrangement ADD COLUMN IF NOT EXISTS legal_entity_id uuid;

DO $$
DECLARE
  parent_table text;
BEGIN
  FOREACH parent_table IN ARRAY ARRAY['plan', 'plan_version', 'forecast_model', 'quota_arrangement']
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
      ('planned_order', 'plan_version', 'plan_version_id'),
      ('pegging', 'plan_version', 'plan_version_id'),
      ('action_message', 'plan_version', 'plan_version_id'),
      ('net_requirement', 'plan_version', 'plan_version_id'),
      ('forecast_line', 'forecast_model', 'model_id'),
      ('quota_source', 'quota_arrangement', 'arrangement_id')
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
