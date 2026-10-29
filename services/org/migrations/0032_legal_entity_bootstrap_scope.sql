DROP POLICY IF EXISTS legal_entity_legal_entity_scope ON legal_entity;
CREATE POLICY legal_entity_legal_entity_scope ON legal_entity AS RESTRICTIVE
  USING (coalesce(current_setting('passage.legal_entity_scopes', true), '') IN ('', '*')
         OR id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
DROP POLICY IF EXISTS legal_entity_name_legal_entity_scope ON legal_entity_name;
CREATE POLICY legal_entity_name_legal_entity_scope ON legal_entity_name AS RESTRICTIVE
  USING (coalesce(current_setting('passage.legal_entity_scopes', true), '') IN ('', '*')
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
