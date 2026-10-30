ALTER TABLE metadata_usage_daily ADD CONSTRAINT metadata_usage_daily_legal_entity_form
  CHECK (legal_entity_id = '' OR legal_entity_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') NOT VALID;

CREATE POLICY metadata_usage_daily_legal_entity_scope ON metadata_usage_daily AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
